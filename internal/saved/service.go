// Package saved implements the "Saved" subsystem: bookmarks, read-later
// articles, and server-side downloads created from the browser extension or
// the web UI. Items live in the saved_items table; processing runs in
// background goroutines claimed atomically per item (same pattern as podcast
// episode downloads), with a periodic worker job as the pending-queue backstop.
package saved

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/fetchguard"
	"discodrive/internal/quota"
	"discodrive/internal/secret"
	"discodrive/internal/storage"
)

// Item kinds and statuses (mirror the saved_items CHECK constraints).
const (
	KindArticle  = "article"
	KindDownload = "download"

	StatusPending    = "pending"
	StatusProcessing = "processing"
	StatusDone       = "done"
	StatusError      = "error"
)

// errDeleted signals that the item row disappeared mid-processing (the user
// deleted it); the processor aborts and cleans up without reporting an error.
var errDeleted = errors.New("saved: item deleted")

// Processing deadlines per kind: a large download may legitimately run for
// hours on a slow upstream; metadata fetches must not.
const (
	downloadDeadline = 2 * time.Hour
	metadataDeadline = 2 * time.Minute
)

// pendingBatch bounds how many pending items one worker tick kicks off.
const pendingBatch = 10

// Finished download rows are an internal queue artifact: the file itself lives
// in the user's Files → Downloads. Rows linger only long enough for the
// extension to poll the outcome, then the cleanup tick removes them.
const (
	downloadDoneTTL  = time.Hour
	downloadErrorTTL = 7 * 24 * time.Hour
)

// Service processes saved items. Client and Validate are the SSRF-guarded
// defaults; tests replace them to reach httptest servers on 127.0.0.1.
type Service struct {
	q           *db.Queries
	st          storage.Storage
	maxDownload int64 // bytes; 0 = unlimited
	// quota bounds what the item's owner may write; nil = no limits configured.
	quota  *quota.Checker
	cipher *secret.Cipher

	Client   *http.Client
	Validate func(string) error
}

// NewService returns a Service writing through st (rooted at the storage root).
func NewService(q *db.Queries, st storage.Storage, maxDownloadMB int) *Service {
	return &Service{
		q:           q,
		st:          st,
		maxDownload: int64(maxDownloadMB) << 20,
		Client:      fetchguard.NewClient(0),
		Validate:    fetchguard.ValidateURL,
	}
}

// SetQuota installs the quota checker. Called once at startup, before processing runs.
func (s *Service) SetQuota(c *quota.Checker) { s.quota = c }

// SetCipher enables encrypted, temporary cookies for authenticated downloads.
func (s *Service) SetCipher(c *secret.Cipher) { s.cipher = c }

// cookiePayload binds ciphertext to one user and URL so copying DB values cannot
// send credentials to another destination.
type cookiePayload struct {
	Cookie string
	User   string
	URL    string
}

func (s *Service) sealCookie(userID pgtype.UUID, rawURL, cookie string) (string, error) {
	if cookie == "" {
		return "", nil
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return "", fetchguard.ErrBlocked
	}
	if s.cipher == nil {
		return "", secret.ErrNoKey
	}
	data, err := json.Marshal(cookiePayload{cookie, db.UUIDString(userID), rawURL})
	if err != nil {
		return "", err
	}
	encrypted, err := s.cipher.Encrypt(string(data))
	if err != nil {
		return "", err
	}
	return "enc:v1:" + encrypted, nil
}

func (s *Service) openCookie(item db.SavedItem) (string, error) {
	if !item.CookieHeader.Valid || item.CookieHeader.String == "" {
		return "", nil
	}
	if s.cipher == nil || !item.CookieExpiresAt.Valid || !time.Now().Before(item.CookieExpiresAt.Time) {
		return "", errors.New("download credentials expired; submit the download again")
	}
	encrypted, ok := strings.CutPrefix(item.CookieHeader.String, "enc:v1:")
	if !ok {
		return "", errors.New("invalid download credentials")
	}
	data, err := s.cipher.Decrypt(encrypted)
	if err != nil {
		return "", errors.New("invalid download credentials")
	}
	var payload cookiePayload
	if json.Unmarshal([]byte(data), &payload) != nil || payload.User != db.UUIDString(item.UserID) || payload.URL != item.Url {
		return "", errors.New("invalid download credentials")
	}
	return payload.Cookie, nil
}

// budget is how many bytes the item's owner may still receive. Saved items land
// straight on disk (the node row appears at the next rescan), so the quota has to be
// enforced here — nothing else on this path checks it.
func (s *Service) budget(ctx context.Context, item db.SavedItem) (int64, error) {
	return s.quota.Allowance(ctx, item.UserID)
}

// Create upserts an item and kicks off processing when the row is pending
// (fresh insert, or a retried row not yet claimed). Re-saving an existing URL
// only bumps updated_at — it never resets a done/error status. contentHTML
// (articles only) is the readable content extracted by the browser extension
// from the live DOM; with it the worker skips the server-side fetch.
// cookieHeader (downloads only) is the browser session for login-protected
// sites — the worker attaches it to the download request.
func (s *Service) Create(ctx context.Context, userID pgtype.UUID, url, kind, title, contentHTML, cookieHeader string) (db.SavedItem, error) {
	encryptedCookie, err := s.sealCookie(userID, url, cookieHeader)
	if err != nil {
		return db.SavedItem{}, err
	}
	item, err := s.q.UpsertSavedItem(ctx, db.UpsertSavedItemParams{
		UserID:       userID,
		Url:          url,
		Kind:         kind,
		Title:        title,
		ContentHtml:  pgtype.Text{String: contentHTML, Valid: contentHTML != ""},
		CookieHeader: pgtype.Text{String: encryptedCookie, Valid: encryptedCookie != ""},
	})
	if err != nil {
		return db.SavedItem{}, err
	}
	switch item.Status {
	case StatusPending:
		s.Kickoff(ctx, item)
	case StatusError:
		// Submitting the same link again is the user saying "try again";
		// without this they would keep getting the old error from a row that
		// lives until the cleanup. A done item is left alone: saving something
		// already finished is a deliberate no-op.
		n, err := s.q.RetrySavedItem(ctx, db.RetrySavedItemParams{ID: item.ID, UserID: userID})
		if err != nil {
			return item, err
		}
		if n > 0 {
			fresh, err := s.q.GetSavedItemForUser(ctx, db.GetSavedItemForUserParams{ID: item.ID, UserID: userID})
			if err != nil {
				return item, err
			}
			item = fresh
			s.Kickoff(ctx, item)
		}
	}
	return item, nil
}

// Kickoff atomically claims a pending item and processes it in a background
// goroutine. The claim closes the TOCTOU between the POST handler and the
// worker tick: only one of them can flip pending→processing.
func (s *Service) Kickoff(ctx context.Context, item db.SavedItem) {
	n, err := s.q.ClaimSavedItem(ctx, item.ID)
	if err != nil {
		log.Printf("discodrive: saved claim %s: %v", db.UUIDString(item.ID), err)
		return
	}
	if n == 0 {
		return // someone else claimed it (or status changed): no-op
	}
	go s.process(item)
}

// RecoverStale re-queues items stuck in "processing" after a server restart.
// Must run before the router and worker start, so it cannot race live goroutines.
func (s *Service) RecoverStale(ctx context.Context) error {
	n, err := s.q.ResetStaleSavedItems(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		log.Printf("discodrive: saved: re-queued %d stale processing item(s)", n)
	}
	return nil
}

// CleanupDownloads is the worker tick job removing finished download rows
// (done after 1h, error after 7d). Files on disk are not touched.
func (s *Service) CleanupDownloads(ctx context.Context) error {
	now := time.Now()
	_, err := s.q.DeleteFinishedDownloads(ctx, db.DeleteFinishedDownloadsParams{
		UpdatedAt:   pgtype.Timestamptz{Time: now.Add(-downloadDoneTTL), Valid: true},
		UpdatedAt_2: pgtype.Timestamptz{Time: now.Add(-downloadErrorTTL), Valid: true},
	})
	return err
}

// ProcessPending is the worker tick job: it claims and processes pending items
// missed by Kickoff (server restarted mid-queue, or the kickoff goroutine died).
func (s *Service) ProcessPending(ctx context.Context) error {
	if err := s.q.ClearExpiredSavedCookies(ctx); err != nil {
		return err
	}
	items, err := s.q.ListPendingSavedItems(ctx, pendingBatch)
	if err != nil {
		return err
	}
	for _, item := range items {
		s.Kickoff(ctx, item)
	}
	return nil
}

// process runs the kind-specific processor and records the outcome. It runs in
// its own goroutine with a fresh context: processing survives the HTTP request
// that triggered it (extension fire-and-forget is the whole point).
func (s *Service) process(item db.SavedItem) {
	deadline := metadataDeadline
	if item.Kind == KindDownload {
		deadline = downloadDeadline
	}
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	var res result
	var err error
	switch item.Kind {
	case KindDownload:
		res, err = s.processDownload(ctx, item)
	case KindArticle:
		res, err = s.processArticle(ctx, item)
	default:
		err = errors.New("unknown kind")
	}

	if errors.Is(err, errDeleted) {
		return // user deleted the row mid-processing; processor cleaned up
	}
	if err != nil {
		log.Printf("discodrive: saved %s %s: %v", item.Kind, db.UUIDString(item.ID), err)
		if setErr := s.q.SetSavedItemError(context.Background(), db.SetSavedItemErrorParams{
			ID:       item.ID,
			ErrorMsg: truncate(err.Error(), 500),
		}); setErr != nil {
			log.Printf("discodrive: saved set-error %s: %v", db.UUIDString(item.ID), setErr)
		}
		return
	}

	meta := res.meta
	if meta == nil {
		meta = item.Meta
	}
	n, err := s.q.SetSavedItemDone(context.Background(), db.SetSavedItemDoneParams{
		ID:          item.ID,
		ContentPath: res.contentPath,
		SizeBytes:   res.size,
		Title:       res.title,
		Meta:        meta,
	})
	if err != nil {
		log.Printf("discodrive: saved set-done %s: %v", db.UUIDString(item.ID), err)
		return
	}
	if n == 0 && res.contentPath.Valid {
		// The row was deleted while we were finishing: drop the produced file.
		// (A rescan may already have imported it; then it lands in the trash —
		// an accepted micro-race, see the design notes.)
		_ = s.st.Remove(res.contentPath.String)
	}
}

// result is what a kind processor returns on success.
type result struct {
	contentPath pgtype.Text // relative to the storage root (includes the user id)
	size        pgtype.Int8
	title       string // only set when the item had no title; empty = keep existing
	meta        []byte // nil = keep existing
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
