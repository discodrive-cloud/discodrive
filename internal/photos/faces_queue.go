package photos

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/photos/faces"
)

// passEvery is how many new faces of a user start a clustering pass while their queue is
// still busy (the first run over a big library: people show up as it goes).
const passEvery = 2000

// passDue reports whether fresh new faces are enough for a pass while the queue is busy,
// given that known faces took part in the last one. A pass reads every face of the user
// and compares the new ones with all of them, so its cost grows with the library: a fixed
// step would make a first run over 200 000 faces a hundred ever heavier passes. The step
// grows with the library instead — an eighth of it once that is more than passEvery.
// (When the queue runs dry a pass is made regardless: nothing waits for long.)
func passDue(fresh, known int) bool {
	return fresh >= max(passEvery, known/8)
}

// Faces finds the faces on the users' cards (through the ML service) and groups them into
// persons. It runs only when a face service is configured and background workers exist.
type Faces struct {
	q       *db.Queries
	th      *Thumbs
	ml      *MLClient
	params  faces.Params
	workers int
	retry   time.Duration // pause after the service did not answer
	idle    time.Duration // how often an idle worker looks for work (and at a service down)

	kick   chan struct{}
	online atomic.Bool

	mu       sync.Mutex
	inflight map[pgtype.UUID]bool
	fresh    map[pgtype.UUID]int // faces stored since the user's last pass
	known    map[pgtype.UUID]int // faces that took part in the user's last pass
	strikes  map[pgtype.UUID]int // failed answers of a service that is up, per card

	// Begin, when set (before Run), opens the transaction a card's faces are replaced in.
	Begin   func(context.Context) (pgx.Tx, error)
	users   map[pgtype.UUID]*sync.Mutex // the user's faces and persons (store, merge, a pass's writes)
	passes  map[pgtype.UUID]*sync.Mutex // one clustering pass of the user at a time
	claimMu sync.Mutex
}

// NewFaces creates the face queue. workers is how many pictures are in flight at once.
func NewFaces(q *db.Queries, th *Thumbs, ml *MLClient, p faces.Params, workers int) *Faces {
	f := &Faces{q: q, th: th, ml: ml, params: p, workers: max(workers, 1), retry: time.Minute, idle: pollInterval * 5,
		kick: make(chan struct{}, 1), inflight: map[pgtype.UUID]bool{}, fresh: map[pgtype.UUID]int{}, known: map[pgtype.UUID]int{},
		strikes: map[pgtype.UUID]int{}, users: map[pgtype.UUID]*sync.Mutex{}, passes: map[pgtype.UUID]*sync.Mutex{}}
	f.online.Store(true)
	return f
}

// Kick wakes the workers (a thumbnail became ready, people were turned on). Never blocks.
func (f *Faces) Kick() {
	select {
	case f.kick <- struct{}{}:
	default:
	}
}

// Online reports whether the face service answered last time it was asked.
func (f *Faces) Online() bool { return f.online.Load() }

func (f *Faces) setOnline(on bool, why error) {
	if f.online.Swap(on) != on {
		if on {
			log.Printf("discodrive: photos: face service is back")
		} else {
			log.Printf("discodrive: photos: face service unavailable, retrying every %s: %v", f.retry, why)
		}
	}
}

// Run works the queue until ctx ends. Passes left over from an earlier run (faces stored,
// the server stopped before the pass) are made at start.
func (f *Faces) Run(ctx context.Context) {
	// Cards looked at with another face model go back to the queue.
	if n, err := f.q.ResetFacesOfOtherModel(ctx, f.ml.Model); err != nil {
		log.Printf("discodrive: photos: requeueing faces of other models: %v", err)
	} else if n > 0 {
		log.Printf("discodrive: photos: face model is now %s: %d photos to look at again", f.ml.Model, n)
	}
	if !f.ml.Ping(ctx) {
		f.setOnline(false, errors.New("no answer to ping"))
	}
	if users, err := f.q.ListFacesUsers(ctx); err == nil {
		for _, u := range users {
			if n, err := f.q.CountUnclusteredFaces(ctx, db.CountUnclusteredFacesParams{UserID: u, Model: f.ml.Model}); err == nil && n > 0 {
				_ = f.pass(ctx, u)
			}
		}
	}
	var wg sync.WaitGroup
	wake := make([]chan struct{}, f.workers)
	for i := range wake {
		wake[i] = make(chan struct{}, 1)
		wg.Add(1)
		go func(w chan struct{}) {
			defer wg.Done()
			f.work(ctx, w)
		}(wake[i])
	}
	// One kick channel fans out to every worker.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-f.kick:
				for _, w := range wake {
					select {
					case w <- struct{}{}:
					default:
					}
				}
			}
		}
	}()
	wg.Wait()
}

func (f *Faces) work(ctx context.Context, wake chan struct{}) {
	tick := time.NewTicker(f.idle)
	defer tick.Stop()
	for {
		for {
			ok, down := f.step(ctx)
			if ctx.Err() != nil {
				return
			}
			if down {
				select { // the service is down: wait, then try the same work again
				case <-ctx.Done():
					return
				case <-time.After(f.retry):
				}
				continue
			}
			if !ok {
				break
			}
		}
		f.passIdle(ctx)
		// Down when last asked (at start, while the service was still starting): ask again,
		// so the flag clears without waiting for a photo to look at.
		if !f.Online() && f.ml.Ping(ctx) {
			f.setOnline(true, nil)
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-tick.C:
		}
	}
}

// step does one card. ok: a card was claimed; down: the service did not answer.
func (f *Faces) step(ctx context.Context) (ok, down bool) {
	a, ok := f.claim(ctx)
	if !ok {
		return false, false
	}
	defer f.release(a.ID)
	defer func() {
		if r := recover(); r != nil {
			f.fail(ctx, a, fmt.Errorf("face detection panicked: %v", r))
		}
	}()
	found, err := f.detect(ctx, a)
	if ctx.Err() != nil {
		return true, false
	}
	if errors.Is(err, ErrMLWrongModel) {
		// PHOTOS_ML_MODEL does not match the service: nothing is the photos' fault.
		f.setOnline(false, err)
		return true, true
	}
	if errors.Is(err, ErrMLUnavailable) {
		// Down, or up and failing on this one picture? A service that answers its ping
		// gets the card back a few times, then the card is failed: one bad picture must
		// not stall the queue (it would be claimed first again and again).
		if !f.ml.Ping(ctx) {
			f.setOnline(false, err)
			return true, true
		}
		f.setOnline(true, nil)
		if f.strike(a.ID) < maxStrikes {
			return true, false
		}
		f.fail(ctx, a, err)
		return true, false
	}
	f.setOnline(true, nil)
	f.clearStrikes(a.ID)
	if err != nil {
		f.fail(ctx, a, err)
		return true, false
	}
	due, err := f.store(ctx, a, found)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("discodrive: photos: storing faces of %s: %v", db.UUIDString(a.ID), err)
		}
		return true, false
	}
	if due {
		_ = f.pass(ctx, a.UserID)
	}
	return true, false
}

func (f *Faces) claim(ctx context.Context) (db.PhotoAsset, bool) {
	f.claimMu.Lock()
	defer f.claimMu.Unlock()
	f.mu.Lock()
	exclude := make([]pgtype.UUID, 0, len(f.inflight))
	for id := range f.inflight {
		exclude = append(exclude, id)
	}
	f.mu.Unlock()
	rows, err := f.q.ClaimPendingFaces(ctx, db.ClaimPendingFacesParams{Exclude: exclude, MaxRows: 1})
	if err != nil || len(rows) == 0 {
		if err != nil && ctx.Err() == nil {
			log.Printf("discodrive: photos: claiming face work: %v", err)
		}
		return db.PhotoAsset{}, false
	}
	f.mu.Lock()
	f.inflight[rows[0].ID] = true
	f.mu.Unlock()
	return rows[0], true
}

// maxStrikes is how many failed answers a card gets from a service that is otherwise up.
const maxStrikes = 3

func (f *Faces) strike(id pgtype.UUID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.strikes[id]++
	n := f.strikes[id]
	if n >= maxStrikes {
		delete(f.strikes, id)
	}
	return n
}

func (f *Faces) clearStrikes(id pgtype.UUID) {
	f.mu.Lock()
	delete(f.strikes, id)
	f.mu.Unlock()
}

func (f *Faces) release(id pgtype.UUID) {
	f.mu.Lock()
	delete(f.inflight, id)
	f.mu.Unlock()
}

func (f *Faces) fail(ctx context.Context, a db.PhotoAsset, err error) {
	log.Printf("discodrive: photos: faces of %s: %v", db.UUIDString(a.ID), err)
	if e := f.q.SetFaceState(ctx, db.SetFaceStateParams{ID: a.ID, FaceModel: text(f.ml.Model),
		FaceState: pgtype.Text{String: "failed", Valid: true},
		FaceError: pgtype.Text{String: truncate(err.Error(), maxErrLen), Valid: true}}); e != nil && ctx.Err() == nil {
		log.Printf("discodrive: photos: recording face failure of %s: %v", db.UUIDString(a.ID), e)
	}
}

// detect renders (or reuses) the viewer preview and asks the service for its faces.
func (f *Faces) detect(ctx context.Context, a db.PhotoAsset) ([]DetectedFace, error) {
	work, err := os.MkdirTemp("", "faces-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	pic, err := f.th.PreviewInto(ctx, a, filepath.Join(work, "preview.jpg"))
	if err != nil {
		return nil, fmt.Errorf("preview: %w", err)
	}
	return f.ml.Detect(ctx, pic)
}

// store replaces the card's faces with the found ones. A new face takes the person of the
// old face it overlaps (IoU > 0.5): an edit of the date rewrites the file, the pixels stay.
// due: enough new faces of the user piled up for a pass.
func (f *Faces) store(ctx context.Context, a db.PhotoAsset, found []DetectedFace) (due bool, err error) {
	lock := f.userLock(a.UserID)
	lock.Lock()
	defer lock.Unlock()
	// One transaction: a crash between taking the old faces away and storing the new ones would
	// lose the names they carry.
	q, done := f.q, func(error) error { return nil }
	if f.Begin != nil {
		tx, err := f.Begin(ctx)
		if err != nil {
			return false, err
		}
		q = f.q.WithTx(tx)
		done = func(err error) error {
			if err != nil {
				_ = tx.Rollback(ctx)
				return err
			}
			return tx.Commit(ctx)
		}
	}
	err = func() error {
		old, err := q.ListAssetFaces(ctx, a.ID)
		if err != nil {
			return err
		}
		if err := q.DeleteAssetFaces(ctx, a.ID); err != nil {
			return err
		}
		carried := carryFaces(found, old)
		for i, d := range found {
			p := db.InsertPhotoFaceParams{UserID: a.UserID, AssetID: a.ID, X1: d.X1, Y1: d.Y1, X2: d.X2, Y2: d.Y2,
				Score: d.Score, Embedding: encodeVec(d.Vec), Model: f.ml.Model}
			if j := carried[i]; j >= 0 {
				// What the user set by hand stays set by hand.
				p.PersonID, p.Manual = old[j].PersonID, old[j].Manual
			}
			if _, err := q.InsertPhotoFace(ctx, p); err != nil {
				return err
			}
		}
		return q.SetFaceState(ctx, db.SetFaceStateParams{ID: a.ID, FaceState: pgtype.Text{String: "done", Valid: true},
			FaceModel: text(f.ml.Model)})
	}()
	if err := done(err); err != nil {
		return false, err
	}
	f.mu.Lock()
	f.fresh[a.UserID] += len(found)
	due = passDue(f.fresh[a.UserID], f.known[a.UserID])
	f.mu.Unlock()
	return due, nil
}

// carryPerson is the person a new face takes over from the old faces of the card: the one of
// the old face it overlaps most, when the boxes are the same face — IoU above 0.5, or above
// 0.3 with the new box's centre inside the old one (another detector draws its boxes
// tighter or looser: a model change must not drop the names).
func carryPerson(d DetectedFace, old []db.PhotoFace) pgtype.UUID {
	p := carryPersons([]DetectedFace{d}, old)
	return p[0]
}

// carryPersons is the person each new face takes over (see carryFaces).
func carryPersons(found []DetectedFace, old []db.PhotoFace) []pgtype.UUID {
	out := make([]pgtype.UUID, len(found))
	for i, j := range carryFaces(found, old) {
		if j >= 0 {
			out[i] = old[j].PersonID
		}
	}
	return out
}

// carryFaces matches new faces to the old ones that carry something — a person, or the
// user's hand (manual) — one to one, best overlap first: the index of the old face per new
// face, -1 for none.
func carryFaces(found []DetectedFace, old []db.PhotoFace) []int {
	type pair struct {
		n, o int
		v    float32
	}
	var pairs []pair
	for i, d := range found {
		cx, cy := (d.X1+d.X2)/2, (d.Y1+d.Y2)/2
		for j, o := range old {
			if !o.PersonID.Valid && !o.Manual {
				continue
			}
			v := iou(d, o)
			inside := cx > o.X1 && cx < o.X2 && cy > o.Y1 && cy < o.Y2
			if v > 0.5 || (v > 0.3 && inside) {
				pairs = append(pairs, pair{i, j, v})
			}
		}
	}
	sort.Slice(pairs, func(a, b int) bool { return pairs[a].v > pairs[b].v })
	out := make([]int, len(found))
	for i := range out {
		out[i] = -1
	}
	usedN, usedO := map[int]bool{}, map[int]bool{}
	for _, p := range pairs {
		if !usedN[p.n] && !usedO[p.o] {
			out[p.n] = p.o
			usedN[p.n], usedO[p.o] = true, true
		}
	}
	return out
}

func iou(d DetectedFace, o db.PhotoFace) float32 {
	ix := min(d.X2, o.X2) - max(d.X1, o.X1)
	iy := min(d.Y2, o.Y2) - max(d.Y1, o.Y1)
	if ix <= 0 || iy <= 0 {
		return 0
	}
	inter := ix * iy
	union := (d.X2-d.X1)*(d.Y2-d.Y1) + (o.X2-o.X1)*(o.Y2-o.Y1) - inter
	if union <= 0 {
		return 0
	}
	return inter / union
}

func encodeVec(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

func decodeVec(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}

func (f *Faces) userLock(u pgtype.UUID) *sync.Mutex {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.users[u]
	if m == nil {
		m = &sync.Mutex{}
		f.users[u] = m
	}
	return m
}

// passIdle makes the passes of users with new faces once the queue has run dry.
func (f *Faces) passIdle(ctx context.Context) {
	f.mu.Lock()
	var due []pgtype.UUID
	for u, n := range f.fresh {
		if n > 0 {
			due = append(due, u)
		}
	}
	f.mu.Unlock()
	for _, u := range due {
		_ = f.pass(ctx, u)
	}
}

// Locked runs fn while no pass or face store of the user runs (a merge must not race a
// pass that assigns faces to the persons it deletes).
func (f *Faces) Locked(userID pgtype.UUID, fn func() error) error {
	lock := f.userLock(userID)
	lock.Lock()
	defer lock.Unlock()
	return fn()
}

// Recluster makes a pass over the user's faces now (the recluster command, tests).
func (f *Faces) Recluster(ctx context.Context, userID pgtype.UUID) error {
	return f.pass(ctx, userID)
}

// pass groups the user's new faces into persons, then drops persons left empty.
func (f *Faces) pass(ctx context.Context, userID pgtype.UUID) error {
	// Passes of a user one at a time; the user's data lock only for writing the result,
	// so a merge or a face store waits for the writes, not for the counting.
	pm := f.passLock(userID)
	pm.Lock()
	defer pm.Unlock()
	f.mu.Lock()
	delete(f.fresh, userID)
	f.mu.Unlock()
	known := 0
	err := cluster(ctx, f.q, userID, f.ml.Model, f.params, func(write func() error) error {
		return f.Locked(userID, write)
	}, &known)
	if known > 0 {
		f.mu.Lock()
		f.known[userID] = known
		f.mu.Unlock()
	}
	if err != nil && ctx.Err() == nil {
		log.Printf("discodrive: photos: clustering faces of %s: %v", db.UUIDString(userID), err)
	}
	return err
}

func (f *Faces) passLock(u pgtype.UUID) *sync.Mutex {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.passes[u]
	if m == nil {
		m = &sync.Mutex{}
		f.passes[u] = m
	}
	return m
}

// Cluster makes one clustering pass over a user's faces (no service needed: the
// recluster command runs it from the command line). The counting runs as is; locked,
// when not nil, wraps the writes (the queue's user lock). Faces assigned meanwhile keep
// their person (AssignFaces only takes faces without one); a person merged away meanwhile
// fails the writes and the faces stay new for the next pass.
// Only faces of cards looked at with model take part: vectors of two models do not compare.
func Cluster(ctx context.Context, q *db.Queries, userID pgtype.UUID, model string, p faces.Params, locked func(func() error) error) error {
	return cluster(ctx, q, userID, model, p, locked, nil)
}

// cluster is Cluster that also reports, through known, how many faces took part.
func cluster(ctx context.Context, q *db.Queries, userID pgtype.UUID, model string, p faces.Params, locked func(func() error) error, known *int) error {
	// Row by row: the vectors are decoded as they arrive and the raw bytes are never
	// held all at once (for 200 000 faces they are 400 MB on top of the decoded ones).
	var fs []faces.Face
	var fresh []pgtype.UUID
	err := q.EachUserFaceVector(ctx, db.ListUserFaceVectorsParams{UserID: userID, Model: model}, func(r db.ListUserFaceVectorsRow) error {
		fs = append(fs, faces.Face{ID: r.ID.Bytes, Person: r.PersonID.Bytes, HasPerson: r.PersonID.Valid,
			New: !r.Clustered, Vec: decodeVec(r.Embedding)})
		if !r.Clustered {
			fresh = append(fresh, r.ID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if known != nil {
		*known = len(fs)
	}
	if len(fresh) == 0 {
		return nil
	}
	// Persons are created during the counting: nobody sees them before they have faces.
	var createErr error
	assign := faces.Cluster(fs, p, func() faces.ID {
		id, err := q.CreatePhotoPerson(ctx, userID)
		if err != nil && createErr == nil {
			createErr = err
		}
		return id.Bytes
	})
	fs = nil
	if createErr != nil {
		return createErr
	}
	byPerson := map[faces.ID][]pgtype.UUID{}
	for face, person := range assign {
		byPerson[person] = append(byPerson[person], pgtype.UUID{Bytes: face, Valid: true})
	}
	write := func() error {
		for person, ids := range byPerson {
			if err := q.AssignFaces(ctx, db.AssignFacesParams{Ids: ids, PersonID: pgtype.UUID{Bytes: person, Valid: true}}); err != nil {
				return err
			}
		}
		if err := q.MarkFacesClustered(ctx, fresh); err != nil {
			return err
		}
		return q.DeleteEmptyPhotoPersons(ctx, userID)
	}
	if locked == nil {
		return write()
	}
	return locked(write)
}
