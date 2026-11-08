package links

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/net/html/charset"

	"discodrive/internal/db"
	"discodrive/internal/fetchguard"
	"discodrive/internal/quota"
)

// Fetch limits. The server reads only what it needs to recognise a link: the start of
// the page and one image, never the page itself as content.
const (
	maxPageBytes  = 512 << 10
	fetchTimeout  = 10 * time.Second
	enrichTimeout = 30 * time.Second
	maxRedirects  = 5
	maxParallel   = 4
	maxWaiting    = 64
	// one user's links fetching or waiting at once: a burst of shares from one phone
	// still gets previews right away, and most of the queue stays for others
	maxPerUser  = 16
	enrichBatch = 20
	listLimit   = 500
	maxOEmbed   = 64 << 10
)

// UserAgent is what link-preview fetches identify as. Instagram, Threads and X hand
// their og: tags only to link-preview bots; "like TwitterBot" is the convention
// messengers use to be recognised as one while naming themselves.
const UserAgent = "Mozilla/5.0 (compatible; DiscoDriveBot/1.0; +https://discodrive.app; link preview; like TwitterBot)"

// defaultOEmbed lists the sources whose pages hide metadata from bots (Reddit) or bury
// it a megabyte deep (YouTube) but answer the official oEmbed endpoint.
var defaultOEmbed = map[string]string{
	"reddit":  "https://www.reddit.com/oembed",
	"youtube": "https://www.youtube.com/oembed",
}

// ErrNotFound: no such link for this user.
var ErrNotFound = errors.New("links: not found")

// ErrTooMany: the user's inbox and archive together hold maxLinksPerUser links already.
var ErrTooMany = errors.New("links: too many saved links")

// maxLinksPerUser bounds one user's rows. Only thumbnails count toward the quota, so
// without a bound the URLs and titles of one account could grow the database without end.
const maxLinksPerUser = 10000

// Link is one inbox row as the API shows it.
type Link struct {
	ID       string    `json:"id"`
	URL      string    `json:"url"`
	Host     string    `json:"host"`
	Source   string    `json:"source"`
	Title    string    `json:"title"`
	Author   string    `json:"author"`
	HasThumb bool      `json:"has_thumb"`
	SavedAt  time.Time `json:"saved_at"`
	Archived bool      `json:"archived"`

	id       pgtype.UUID
	enriched bool
}

// Page is a listing plus what the list header needs.
type Page struct {
	Items      []Link   `json:"items"`
	InboxCount int64    `json:"inbox_count"`
	Sources    []string `json:"sources"`
}

// Service stores links and fetches their metadata in the background. Client and
// Validate are the SSRF-guarded defaults; tests replace them to reach httptest servers.
type Service struct {
	q        *db.Queries
	Client   *http.Client
	Validate func(string) error

	// oembed maps a source to its oEmbed endpoint (tests point it at httptest).
	oembed map[string]string

	sem     chan struct{}
	waiting atomic.Int32
	mu      sync.Mutex
	perUser map[pgtype.UUID]int // links of each user fetching or waiting (kick)

	// quota, when set, keeps a thumbnail out once the owner has no room for it.
	quota *quota.Checker
	wg    sync.WaitGroup
	// noFetch turns the background fetch off (tests that only exercise storage).
	noFetch bool
}

func NewService(q *db.Queries) *Service {
	c := fetchguard.NewClient(fetchTimeout)
	guard := c.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return errors.New("too many redirects")
		}
		return guard(req, via)
	}
	return &Service{q: q, Client: c, Validate: fetchguard.ValidateURL, oembed: defaultOEmbed, sem: make(chan struct{}, maxParallel), perUser: map[pgtype.UUID]int{}}
}

// SetQuota installs the quota checker. Called once at startup, before links are saved.
func (s *Service) SetQuota(c *quota.Checker) { s.quota = c }

// Save stores a link shared from a phone (or brings an existing one back to the top of
// the inbox) and starts fetching its metadata. A link that resolves to an internal
// address is refused with fetchguard.ErrBlocked; one whose host does not resolve right
// now is kept — losing a shared link is worse than a row without a preview.
func (s *Service) Save(ctx context.Context, userID pgtype.UUID, rawURL, title string) (Link, error) {
	return s.SaveShared(ctx, userID, rawURL, title, time.Now())
}

// maxShareAge bounds how far back a client may date a share it kept in its outbox.
const maxShareAge = 30 * 24 * time.Hour

// SaveShared is Save for a link the phone shared at sharedAt and is sending only now (it
// waited in the outbox). The time decides whether the share is newer than the user's
// archiving; it is clamped to the last 30 days and never later than now.
func (s *Service) SaveShared(ctx context.Context, userID pgtype.UUID, rawURL, title string, sharedAt time.Time) (Link, error) {
	now := time.Now()
	if sharedAt.IsZero() || sharedAt.After(now) {
		sharedAt = now
	}
	if sharedAt.Before(now.Add(-maxShareAge)) {
		sharedAt = now.Add(-maxShareAge)
	}
	norm, err := Normalize(rawURL)
	if err != nil {
		return Link{}, err
	}
	if err := s.Validate(norm); err != nil && !errors.Is(err, fetchguard.ErrUnresolved) {
		return Link{}, fmt.Errorf("%w: %v", fetchguard.ErrBlocked, err)
	}
	if n, err := s.q.CountSavedLinks(ctx, userID); err != nil {
		return Link{}, err
	} else if n >= maxLinksPerUser {
		// at the cap a link already saved may still come back to the top, a new one not
		if ok, err := s.q.SavedLinkExists(ctx, db.SavedLinkExistsParams{UserID: userID, Url: norm}); err != nil {
			return Link{}, err
		} else if !ok {
			return Link{}, ErrTooMany
		}
	}
	u, _ := url.Parse(norm)
	row, err := s.q.UpsertSavedLink(ctx, db.UpsertSavedLinkParams{
		UserID:   userID,
		Url:      norm,
		Source:   SourceOf(u.Hostname()),
		Title:    cleanText(title, maxTitleRunes),
		SharedAt: pgtype.Timestamptz{Time: sharedAt, Valid: true},
	})
	if err != nil {
		return Link{}, err
	}
	l := toLink(db.ListSavedLinksRow(row))
	if !l.enriched && !s.noFetch {
		s.kick(userID, row.ID, row.Url)
	}
	return l, nil
}

// List returns the inbox (archived=false) or the archive, optionally one source only.
func (s *Service) List(ctx context.Context, userID pgtype.UUID, archived bool, source string) (Page, error) {
	rows, err := s.q.ListSavedLinks(ctx, db.ListSavedLinksParams{UserID: userID, Limit: listLimit, Archived: archived, Source: source})
	if err != nil {
		return Page{}, err
	}
	count, err := s.q.CountInboxLinks(ctx, userID)
	if err != nil {
		return Page{}, err
	}
	sources, err := s.q.SavedLinkSources(ctx, db.SavedLinkSourcesParams{UserID: userID, Archived: archived})
	if err != nil {
		return Page{}, err
	}
	p := Page{Items: make([]Link, 0, len(rows)), InboxCount: count, Sources: sources}
	for _, r := range rows {
		p.Items = append(p.Items, toLink(r))
	}
	return p, nil
}

// SetArchived moves a link to the archive or back to the inbox.
func (s *Service) SetArchived(ctx context.Context, userID pgtype.UUID, id string, archived bool) error {
	uid, err := db.ParseUUID(id)
	if err != nil {
		return ErrNotFound
	}
	n, err := s.q.SetSavedLinkArchived(ctx, db.SetSavedLinkArchivedParams{ID: uid, UserID: userID, Archived: archived})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a link for good.
func (s *Service) Delete(ctx context.Context, userID pgtype.UUID, id string) error {
	uid, err := db.ParseUUID(id)
	if err != nil {
		return ErrNotFound
	}
	n, err := s.q.DeleteSavedLink(ctx, db.DeleteSavedLinkParams{ID: uid, UserID: userID})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Thumb returns the stored preview (a JPEG made by MakeThumb).
func (s *Service) Thumb(ctx context.Context, userID pgtype.UUID, id string) ([]byte, error) {
	uid, err := db.ParseUUID(id)
	if err != nil {
		return nil, ErrNotFound
	}
	b, err := s.q.GetSavedLinkThumb(ctx, db.GetSavedLinkThumbParams{ID: uid, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return b, err
}

// EnrichPending is the worker tick: it fetches metadata for links the immediate fetch
// missed (all slots busy, or the server restarted).
func (s *Service) EnrichPending(ctx context.Context) error {
	rows, err := s.q.ListLinksNeedingEnrich(ctx, enrichBatch)
	if err != nil {
		return err
	}
	for _, r := range rows {
		select {
		case s.sem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		s.enrich(r.UserID, r.ID, r.Url)
		<-s.sem
	}
	return nil
}

// Wait blocks until background fetches started by Save have finished (tests).
func (s *Service) Wait() { s.wg.Wait() }

// kick fetches in the background: at most maxParallel at once, the rest waiting for a
// slot. Past maxWaiting queued links, or maxPerUser of one user's, the worker tick picks
// the rest up instead: a flood cannot pile up goroutines, and one user's links cannot
// take the slots everyone's previews wait for.
func (s *Service) kick(userID, id pgtype.UUID, rawURL string) {
	s.mu.Lock()
	if s.perUser[userID] >= maxPerUser {
		s.mu.Unlock()
		return
	}
	s.perUser[userID]++
	s.mu.Unlock()
	done := func() {
		s.mu.Lock()
		if s.perUser[userID]--; s.perUser[userID] <= 0 {
			delete(s.perUser, userID)
		}
		s.mu.Unlock()
	}
	if s.waiting.Add(1) > maxWaiting {
		s.waiting.Add(-1)
		done()
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer done()
		s.sem <- struct{}{}
		s.waiting.Add(-1)
		defer func() { <-s.sem }()
		s.enrich(userID, id, rawURL)
	}()
}

// enrich fetches the page's metadata and preview and records the outcome. The row is
// marked as tried whatever happens, so a page that refuses is not fetched forever.
func (s *Service) enrich(userID, id pgtype.UUID, rawURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), enrichTimeout)
	defer cancel()
	var meta Meta
	var thumb []byte
	defer func() {
		if p := recover(); p != nil {
			log.Printf("discodrive: links enrich %s: panic: %v\n%s", db.UUIDString(id), p, debug.Stack())
			meta, thumb = Meta{}, nil
		}
		// The thumbnail counts toward the owner's quota: keep it out when it does not fit.
		if thumb != nil && s.quota != nil {
			if err := s.quota.Check(context.Background(), userID, int64(len(thumb))); err != nil {
				thumb = nil
			}
		}
		if err := s.q.SetSavedLinkEnriched(context.Background(), db.SetSavedLinkEnrichedParams{
			ID: id, Title: meta.Title, Author: meta.Author, Thumb: thumb,
		}); err != nil {
			log.Printf("discodrive: links enrich %s: store: %v", db.UUIDString(id), err)
		}
	}()
	var pageURL *url.URL
	var err error
	if ep, ok := s.oembed[sourceOfURL(rawURL)]; ok {
		meta, pageURL, err = s.fetchOEmbed(ctx, ep, rawURL)
		if err != nil {
			log.Printf("discodrive: links enrich %s: oembed: %v", db.UUIDString(id), causeOf(err))
		}
	}
	if meta.Title == "" {
		meta, pageURL, err = s.fetchMeta(ctx, rawURL)
	}
	if err != nil {
		// No URL in the log: the error of an http.Client names it, so only its cause.
		log.Printf("discodrive: links enrich %s: page: %v", db.UUIDString(id), causeOf(err))
		return
	}
	if meta.Image == "" {
		return
	}
	img, err := pageURL.Parse(meta.Image)
	if err != nil {
		return
	}
	thumb, err = s.fetchThumb(ctx, img.String())
	if err != nil {
		log.Printf("discodrive: links enrich %s: preview: %v", db.UUIDString(id), causeOf(err))
		thumb = nil
	}
}

// fetchMeta reads the start of an HTML page and returns its metadata and the final
// URL after redirects (what relative og:image values resolve against).
func (s *Service) fetchMeta(ctx context.Context, rawURL string) (Meta, *url.URL, error) {
	if err := s.Validate(rawURL); err != nil {
		return Meta{}, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Meta{}, nil, err
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := s.Client.Do(req)
	if err != nil {
		return Meta{}, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Meta{}, nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mt != "text/html" && mt != "application/xhtml+xml" {
		return Meta{}, resp.Request.URL, nil // not a page: nothing to read, not an error
	}
	body := io.LimitReader(resp.Body, maxPageBytes)
	decoded, err := charset.NewReader(body, resp.Header.Get("Content-Type"))
	if err != nil {
		decoded = body
	}
	return parseHead(decoded), resp.Request.URL, nil
}

// fetchOEmbed asks the source's oEmbed endpoint about the link. The answer is untrusted
// JSON like any page: capped, cleaned, and its thumbnail goes through fetchThumb.
func (s *Service) fetchOEmbed(ctx context.Context, endpoint, rawURL string) (Meta, *url.URL, error) {
	ep, err := url.Parse(endpoint)
	if err != nil {
		return Meta{}, nil, err
	}
	qv := ep.Query()
	qv.Set("url", rawURL)
	qv.Set("format", "json")
	ep.RawQuery = qv.Encode()
	if err := s.Validate(ep.String()); err != nil {
		return Meta{}, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.String(), nil)
	if err != nil {
		return Meta{}, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := s.Client.Do(req)
	if err != nil {
		return Meta{}, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Meta{}, nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var out struct {
		Title        string `json:"title"`
		AuthorName   string `json:"author_name"`
		ThumbnailURL string `json:"thumbnail_url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxOEmbed)).Decode(&out); err != nil {
		return Meta{}, nil, err
	}
	return Meta{
		Title:  cleanText(out.Title, maxTitleRunes),
		Author: cleanText(out.AuthorName, maxAuthorRunes),
		Image:  out.ThumbnailURL,
	}, resp.Request.URL, nil
}

func sourceOfURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "other"
	}
	return SourceOf(u.Hostname())
}

func (s *Service) fetchThumb(ctx context.Context, imgURL string) ([]byte, error) {
	if u, err := url.Parse(imgURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, ErrNotImage
	}
	if err := s.Validate(imgURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imgURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "image/webp,image/png,image/jpeg,image/gif;q=0.9")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	if resp.ContentLength > maxImageBytes {
		return nil, ErrImageTooLarge
	}
	return MakeThumb(resp.Body)
}

// causeOf strips the request URL an *url.Error carries.
func causeOf(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	if errors.Is(err, fetchguard.ErrBlocked) {
		return fetchguard.ErrBlocked
	}
	return err
}

func toLink(r db.ListSavedLinksRow) Link {
	host := ""
	if u, err := url.Parse(r.Url); err == nil {
		host = u.Hostname()
	}
	return Link{
		ID:       db.UUIDString(r.ID),
		URL:      r.Url,
		Host:     host,
		Source:   r.Source,
		Title:    r.Title,
		Author:   r.Author,
		HasThumb: r.HasThumb,
		SavedAt:  r.SavedAt.Time,
		Archived: r.ArchivedAt.Valid,
		id:       r.ID,
		enriched: r.EnrichedAt.Valid,
	}
}
