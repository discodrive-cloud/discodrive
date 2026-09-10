package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/quota"
)

var (
	ErrUploadNotFound  = errors.New("upload session not found")
	ErrChunkOutOfOrder = errors.New("chunk out of order")
	// ErrUploadSize is returned when the staged bytes do not add up to the total the
	// client declared at Init — either Complete was called early or a chunk overshot.
	ErrUploadSize = errors.New("upload size mismatch")
)

type uploadSession struct {
	mu       sync.Mutex
	userID   string
	parentID *string
	name     string
	// relPath, when set, is where the file lands, resolved the way PUT /sync/file resolves
	// its path (folder chain created on the way); parentID and name are then unused.
	relPath string
	// baseVersion is the version the client edited, nil when it does not care; a mismatch
	// at Complete makes the upload a conflict copy rather than an overwrite.
	baseVersion *int64
	tmpRel      string
	total       int64     // size the client declared at Init; 0 = not declared
	modifiedAt  time.Time // content date the client declared at Init; zero = use server time
	nextChunk   int
	lastTouch   time.Time
}

// Uploads manages resumable chunked uploads: sessions are held in memory,
// data is staged in .uploads/<id>, and on completion the assembled file goes through
// Push (versioning/conflicts). Chunking is a transport concern; the file lands on disk whole.
type Uploads struct {
	mu sync.Mutex
	m  map[string]*uploadSession
	st Storage
	fs *FileService
	// quota bounds what a session may stage; nil = no limits configured. Held here
	// rather than reached through fs, which is nil in tests of the session bookkeeping.
	quota *quota.Checker
}

func NewUploads(st Storage, fs *FileService) *Uploads {
	return &Uploads{m: make(map[string]*uploadSession), st: st, fs: fs}
}

// SetQuota installs the quota checker. Called once at startup.
func (u *Uploads) SetQuota(c *quota.Checker) { u.quota = c }

// uid parses a session's user ID for the quota queries.
func uid(userID string) (pgtype.UUID, error) {
	id, err := db.ParseUUID(userID)
	if err != nil {
		return pgtype.UUID{}, ErrNotFound
	}
	return id, nil
}

// Init creates an upload session and returns its ID. total is the full size the client
// intends to send; it is what Complete checks the assembled file against. Pass 0 when the
// size is genuinely unknown — the session then works as before, with no size check.
// meta carries optional client metadata (its zero value means none) and is applied by
// Complete, since that is where the file is actually published.
func (u *Uploads) Init(ctx context.Context, userID string, parentID *string, name string, total int64, meta PushMeta) (string, error) {
	return u.init(ctx, userID, parentID, name, "", nil, total, meta)
}

// InitByPath opens a session that lands at relPath (user-relative, scope already applied)
// the way PUT /sync/file would, with an optional base version for conflict detection.
// This is the sync engines' transport for files too large to send in one request.
func (u *Uploads) InitByPath(ctx context.Context, userID, relPath string, baseVersion *int64, total int64, meta PushMeta) (string, error) {
	rel := strings.Trim(filepath.ToSlash(relPath), "/")
	if rel == "" {
		return "", ErrNotFound
	}
	return u.init(ctx, userID, nil, path.Base(rel), rel, baseVersion, total, meta)
}

func (u *Uploads) init(ctx context.Context, userID string, parentID *string, name, relPath string, baseVersion *int64, total int64, meta PushMeta) (string, error) {
	if err := validateName(name); err != nil {
		return "", err
	}
	if total < 0 {
		return "", ErrUploadSize
	}
	// Refuse a file that cannot possibly fit before the client starts sending it —
	// the per-chunk check would otherwise only stop it once the quota is full.
	if u.quota != nil {
		owner, err := uid(userID)
		if err != nil {
			return "", err
		}
		if err := u.quota.Check(ctx, owner, total); err != nil {
			return "", err
		}
	}
	id := randomHex()
	u.mu.Lock()
	u.m[id] = &uploadSession{userID: userID, parentID: parentID, name: name,
		relPath: relPath, baseVersion: baseVersion,
		tmpRel: ".uploads/" + id, total: total,
		modifiedAt: meta.ModifiedAt, lastTouch: time.Now()}
	u.mu.Unlock()
	return id, nil
}

// GC removes sessions idle for longer than maxAge, deleting their staged temp files.
// Prevents abandoned resumable uploads from leaking memory and disk indefinitely.
//
// Lock discipline: u.mu and a session's s.mu are never held together — Complete
// acquires them in the opposite order, and holding u.mu while waiting on a busy
// session freezes Init/Chunk/Status for everyone (AB-BA deadlock; froze every
// upload in prod until restart).
func (u *Uploads) GC(maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	u.mu.Lock()
	candidates := make(map[string]*uploadSession, len(u.m))
	maps.Copy(candidates, u.m)
	u.mu.Unlock()

	var staleIDs []string
	var stale []*uploadSession
	for id, s := range candidates {
		if !s.mu.TryLock() {
			continue // an in-flight chunk/complete holds the session: it is active
		}
		idle := s.lastTouch.Before(cutoff)
		s.mu.Unlock()
		if idle {
			staleIDs = append(staleIDs, id)
			stale = append(stale, s)
		}
	}
	if len(staleIDs) == 0 {
		return
	}
	// A session touched in the window between the idle check and this delete is
	// dropped anyway: it had been idle past maxAge, the client gets
	// ErrUploadNotFound on its next request and restarts the upload.
	u.mu.Lock()
	for _, id := range staleIDs {
		delete(u.m, id)
	}
	u.mu.Unlock()
	for _, s := range stale {
		_ = u.st.Remove(s.tmpRel)
	}
}

// StartGC runs GC on a ticker until ctx is cancelled.
func (u *Uploads) StartGC(ctx context.Context, every, maxAge time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			u.GC(maxAge)
		}
	}
}

func (u *Uploads) get(id, userID string) (*uploadSession, error) {
	u.mu.Lock()
	s, ok := u.m[id]
	u.mu.Unlock()
	if !ok || s.userID != userID {
		return nil, ErrUploadNotFound
	}
	return s, nil
}

// Chunk appends chunk n. Returns the next expected chunk number.
// An already-accepted chunk is ignored (idempotent); a future one → ErrChunkOutOfOrder.
func (u *Uploads) Chunk(ctx context.Context, id, userID string, n int, r io.Reader) (int, error) {
	s, err := u.get(id, userID)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastTouch = time.Now()

	switch {
	case n < s.nextChunk:
		_, _ = io.Copy(io.Discard, r)
		return s.nextChunk, nil
	case n > s.nextChunk:
		_, _ = io.Copy(io.Discard, r)
		return s.nextChunk, ErrChunkOutOfOrder
	}
	// Append writes straight into the staging file, so a body that dies mid-chunk leaves a
	// partial tail behind. nextChunk does not advance, and the client retries this very
	// chunk (useUploads.ts does, up to MAX_RETRIES) — appending the full chunk after the
	// orphaned bytes. Roll back to the pre-chunk length so the retry starts clean.
	before, err := u.st.Size(s.tmpRel)
	if err != nil {
		return s.nextChunk, err
	}
	// Staged bytes live in .uploads/, outside the node tree, so the quota sum cannot see
	// them yet — they are passed as reserved so each chunk is measured against what is
	// left after the ones already staged. Init has usually caught an oversized file
	// already; this stops the case Init cannot judge, an upload with no declared size,
	// and a quota that filled up while the upload was running.
	limited := io.Reader(r)
	if u.quota != nil {
		owner, err := uid(s.userID)
		if err != nil {
			return s.nextChunk, err
		}
		if limited, err = u.quota.ReaderReserving(ctx, owner, before, r); err != nil {
			return s.nextChunk, err
		}
	}
	if err := u.st.Append(s.tmpRel, limited); err != nil {
		if terr := u.st.Truncate(s.tmpRel, before); terr != nil {
			return s.nextChunk, terr
		}
		return s.nextChunk, err
	}
	// A chunk that pushes the staging file past the declared total means the client is
	// sending something other than the file it announced; refuse it rather than let
	// Complete publish the mismatch.
	if s.total > 0 {
		after, err := u.st.Size(s.tmpRel)
		if err != nil {
			return s.nextChunk, err
		}
		if after > s.total {
			if terr := u.st.Truncate(s.tmpRel, before); terr != nil {
				return s.nextChunk, terr
			}
			return s.nextChunk, ErrUploadSize
		}
	}
	s.nextChunk++
	return s.nextChunk, nil
}

// Status returns the next expected chunk number (for resuming an upload).
func (u *Uploads) Status(id, userID string) (int, error) {
	s, err := u.get(id, userID)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nextChunk, nil
}

// Complete finalizes the upload: the assembled file goes through Push, and the
// session and temp file are removed. The session mutex is released before the
// map delete — see the lock-discipline note on GC.
func (u *Uploads) Complete(ctx context.Context, id, userID string) (PushResult, error) {
	s, err := u.get(id, userID)
	if err != nil {
		return PushResult{}, err
	}
	s.mu.Lock()
	// Verify before publishing: without this the session happily pushes whatever chunks
	// happened to land, and Push computes content_hash over those bytes — so a short or
	// duplicated upload is self-consistent and no later integrity check can spot it.
	if s.total > 0 {
		staged, err := u.st.Size(s.tmpRel)
		if err != nil {
			s.mu.Unlock()
			return PushResult{}, err
		}
		if staged != s.total {
			s.mu.Unlock()
			return PushResult{}, fmt.Errorf("%w: staged %d of %d declared bytes", ErrUploadSize, staged, s.total)
		}
	}
	f, err := u.st.Open(s.tmpRel)
	if err != nil {
		s.mu.Unlock()
		return PushResult{}, err
	}
	var res PushResult
	if s.relPath != "" {
		res, err = u.fs.PushByPathWithMeta(ctx, s.userID, s.relPath, s.baseVersion, f,
			PushMeta{ModifiedAt: s.modifiedAt})
	} else {
		res, err = u.fs.PushWithMeta(ctx, s.userID, s.parentID, s.name, s.baseVersion, "", f,
			PushMeta{ModifiedAt: s.modifiedAt})
	}
	_ = f.Close()
	if err != nil {
		s.mu.Unlock()
		return PushResult{}, err
	}
	_ = u.st.Remove(s.tmpRel)
	s.mu.Unlock()

	u.mu.Lock()
	delete(u.m, id)
	u.mu.Unlock()
	return res, nil
}

// Abort cancels an upload: removes the temp file and session. Unknown or foreign ID → no-op.
func (u *Uploads) Abort(userID, id string) {
	u.mu.Lock()
	s, ok := u.m[id]
	if ok && s.userID == userID {
		delete(u.m, id)
	}
	u.mu.Unlock()
	if ok && s.userID == userID {
		_ = u.st.Remove(s.tmpRel)
	}
}

func randomHex() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
