package dav

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"discodrive/internal/db"
)

// ErrNotFound — collection/object not found (or does not belong to the user).
var ErrNotFound = errors.New("dav: not found")

// Service — CalDAV/CardDAV storage backed by Postgres. Ownership is scoped by user_id.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, q: db.New(pool)}
}

func etagOf(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// createSlug retries creation on a UNIQUE(uri) collision.
func createSlug[T any](fn func(uri string) (T, error)) (T, error) {
	var zero T
	for attempt := 0; attempt < 5; attempt++ {
		v, err := fn(newSlug())
		if err == nil {
			return v, nil
		}
		if !isUniqueViolation(err) {
			return zero, err
		}
	}
	return zero, errors.New("dav: failed to generate a unique slug")
}

// --- calendars ---

func (s *Service) CreateCalendar(ctx context.Context, userID, name, color string) (db.Calendar, error) {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return db.Calendar{}, err
	}
	return createSlug(func(uri string) (db.Calendar, error) {
		return s.q.CreateCalendar(ctx, db.CreateCalendarParams{UserID: uid, Uri: uri, Name: name, Color: color})
	})
}

// CreateCalendarWithURI creates a collection with the given uri and component-set (for
// MKCALENDAR from a client that chooses its own path). Idempotent: if the uri already
// exists and belongs to this user, returns the existing collection (client retries MKCALENDAR).
func (s *Service) CreateCalendarWithURI(ctx context.Context, userID, uri, name, components string) (db.Calendar, error) {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return db.Calendar{}, err
	}
	c, err := s.q.CreateCalendarWithComponents(ctx, db.CreateCalendarWithComponentsParams{
		UserID: uid, Uri: uri, Name: name, Color: "", Components: components,
	})
	if err == nil {
		return c, nil
	}
	if isUniqueViolation(err) {
		if existing, gerr := s.q.GetCalendarByURI(ctx, uri); gerr == nil && db.UUIDString(existing.UserID) == userID {
			return existing, nil
		}
	}
	return db.Calendar{}, err
}

func (s *Service) ListCalendars(ctx context.Context, userID string) ([]db.Calendar, error) {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return nil, err
	}
	return s.q.ListCalendars(ctx, uid)
}

func (s *Service) CalendarByURI(ctx context.Context, uri string) (db.Calendar, error) {
	c, err := s.q.GetCalendarByURI(ctx, uri)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Calendar{}, ErrNotFound
	}
	return c, err
}

func (s *Service) GetCalendar(ctx context.Context, calID string) (db.Calendar, error) {
	cid, err := db.ParseUUID(calID)
	if err != nil {
		return db.Calendar{}, err
	}
	c, err := s.q.GetCalendar(ctx, cid)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Calendar{}, ErrNotFound
	}
	return c, err
}

func (s *Service) SetCalendarColor(ctx context.Context, userID, calID, color string) error {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return err
	}
	cid, err := db.ParseUUID(calID)
	if err != nil {
		return err
	}
	return s.q.SetCalendarColor(ctx, db.SetCalendarColorParams{ID: cid, Color: color, UserID: uid})
}

func (s *Service) SetCalendarOrder(ctx context.Context, userID, calID string, order int32) error {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return err
	}
	cid, err := db.ParseUUID(calID)
	if err != nil {
		return err
	}
	return s.q.SetCalendarOrder(ctx, db.SetCalendarOrderParams{ID: cid, SortOrder: pgtype.Int4{Int32: order, Valid: true}, UserID: uid})
}

func (s *Service) SetCalendarName(ctx context.Context, userID, calID, name string) error {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return err
	}
	cid, err := db.ParseUUID(calID)
	if err != nil {
		return err
	}
	return s.q.SetCalendarName(ctx, db.SetCalendarNameParams{ID: cid, Name: name, UserID: uid})
}

func (s *Service) DeleteCalendar(ctx context.Context, userID, calID string) error {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return err
	}
	cid, err := db.ParseUUID(calID)
	if err != nil {
		return err
	}
	return s.q.DeleteCalendar(ctx, db.DeleteCalendarParams{ID: cid, UserID: uid})
}

func (s *Service) EnsureDefaultCalendar(ctx context.Context, userID string) (db.Calendar, error) {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return db.Calendar{}, err
	}
	cals, err := s.q.ListCalendars(ctx, uid)
	if err != nil {
		return db.Calendar{}, err
	}
	for _, c := range cals {
		if strings.Contains(c.Components, "VEVENT") {
			return c, nil
		}
	}
	return s.CreateCalendar(ctx, userID, "Calendar", "")
}

// EnsureDefaultTaskList returns the user's default VTODO collection: the earliest
// by created_at whose Components contains VTODO (matches the Reminders list for
// device round-trips). Creates one if none exists.
func (s *Service) EnsureDefaultTaskList(ctx context.Context, userID string) (db.Calendar, error) {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return db.Calendar{}, err
	}
	cals, err := s.q.ListCalendars(ctx, uid)
	if err != nil {
		return db.Calendar{}, err
	}
	for _, c := range cals {
		if strings.Contains(c.Components, "VTODO") {
			return c, nil
		}
	}
	return createSlug(func(uri string) (db.Calendar, error) {
		return s.q.CreateCalendarWithComponents(ctx, db.CreateCalendarWithComponentsParams{
			UserID: uid, Uri: uri, Name: "Reminders", Color: "", Components: "VTODO",
		})
	})
}

func (s *Service) PutCalendarObject(ctx context.Context, calID, uid, data string) (string, error) {
	cid, err := db.ParseUUID(calID)
	if err != nil {
		return "", err
	}
	parsedUID, parsed := parseICal(data)
	if uid == "" {
		uid = parsedUID
	}
	etag := etagOf(data)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)
	if _, err = qtx.UpsertCalendarObject(ctx, db.UpsertCalendarObjectParams{
		CalendarID: cid, Uid: uid, Data: data, Etag: etag, Parsed: parsed,
	}); err != nil {
		return "", err
	}
	if err = qtx.BumpCalendarCtag(ctx, cid); err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return etag, nil
}

func (s *Service) GetCalendarObject(ctx context.Context, calID, uid string) (string, string, error) {
	cid, err := db.ParseUUID(calID)
	if err != nil {
		return "", "", err
	}
	o, err := s.q.GetCalendarObject(ctx, db.GetCalendarObjectParams{CalendarID: cid, Uid: uid})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	return o.Data, o.Etag, nil
}

func (s *Service) ListCalendarObjects(ctx context.Context, calID string) ([]db.CalendarObject, error) {
	cid, err := db.ParseUUID(calID)
	if err != nil {
		return nil, err
	}
	return s.q.ListCalendarObjects(ctx, cid)
}

func (s *Service) DeleteCalendarObject(ctx context.Context, calID, uid string) error {
	cid, err := db.ParseUUID(calID)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)
	n, err := qtx.DeleteCalendarObject(ctx, db.DeleteCalendarObjectParams{CalendarID: cid, Uid: uid})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if err = qtx.BumpCalendarCtag(ctx, cid); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// --- addressbooks ---

func (s *Service) CreateAddressbook(ctx context.Context, userID, name string) (db.Addressbook, error) {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return db.Addressbook{}, err
	}
	return createSlug(func(uri string) (db.Addressbook, error) {
		return s.q.CreateAddressbook(ctx, db.CreateAddressbookParams{UserID: uid, Uri: uri, Name: name})
	})
}

// CreateAddressbookWithURI creates a book with the given uri (for client MKCOL/discovery).
// Idempotent: if the uri already exists and belongs to the user, returns the existing one.
func (s *Service) CreateAddressbookWithURI(ctx context.Context, userID, uri, name string) (db.Addressbook, error) {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return db.Addressbook{}, err
	}
	a, err := s.q.CreateAddressbook(ctx, db.CreateAddressbookParams{UserID: uid, Uri: uri, Name: name})
	if err == nil {
		return a, nil
	}
	if isUniqueViolation(err) {
		if existing, gerr := s.q.GetAddressbookByURI(ctx, uri); gerr == nil && db.UUIDString(existing.UserID) == userID {
			return existing, nil
		}
	}
	return db.Addressbook{}, err
}

func (s *Service) ListAddressbooks(ctx context.Context, userID string) ([]db.Addressbook, error) {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return nil, err
	}
	return s.q.ListAddressbooks(ctx, uid)
}

func (s *Service) AddressbookByURI(ctx context.Context, uri string) (db.Addressbook, error) {
	ab, err := s.q.GetAddressbookByURI(ctx, uri)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Addressbook{}, ErrNotFound
	}
	return ab, err
}

func (s *Service) SetAddressbookName(ctx context.Context, userID, abID, name string) error {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return err
	}
	aid, err := db.ParseUUID(abID)
	if err != nil {
		return err
	}
	return s.q.SetAddressbookName(ctx, db.SetAddressbookNameParams{ID: aid, Name: name, UserID: uid})
}

func (s *Service) DeleteAddressbook(ctx context.Context, userID, abID string) error {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return err
	}
	aid, err := db.ParseUUID(abID)
	if err != nil {
		return err
	}
	return s.q.DeleteAddressbook(ctx, db.DeleteAddressbookParams{ID: aid, UserID: uid})
}

func (s *Service) EnsureDefaultAddressbook(ctx context.Context, userID string) (db.Addressbook, error) {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return db.Addressbook{}, err
	}
	n, err := s.q.CountAddressbooks(ctx, uid)
	if err != nil {
		return db.Addressbook{}, err
	}
	if n > 0 {
		abs, err := s.q.ListAddressbooks(ctx, uid)
		if err != nil {
			return db.Addressbook{}, err
		}
		return abs[0], nil
	}
	return s.CreateAddressbook(ctx, userID, "Contacts")
}

func (s *Service) PutAddressbookObject(ctx context.Context, abID, uid, data string) (string, error) {
	aid, err := db.ParseUUID(abID)
	if err != nil {
		return "", err
	}
	parsedUID, parsed := parseVCard(data)
	if uid == "" {
		uid = parsedUID
	}
	etag := etagOf(data)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)
	if _, err = qtx.UpsertAddressbookObject(ctx, db.UpsertAddressbookObjectParams{
		AddressbookID: aid, Uid: uid, Data: data, Etag: etag, Parsed: parsed,
	}); err != nil {
		return "", err
	}
	if err = qtx.BumpAddressbookCtag(ctx, aid); err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return etag, nil
}

func (s *Service) GetAddressbookObject(ctx context.Context, abID, uid string) (string, string, error) {
	aid, err := db.ParseUUID(abID)
	if err != nil {
		return "", "", err
	}
	o, err := s.q.GetAddressbookObject(ctx, db.GetAddressbookObjectParams{AddressbookID: aid, Uid: uid})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	return o.Data, o.Etag, nil
}

func (s *Service) ListAddressbookObjects(ctx context.Context, abID string) ([]db.AddressbookObject, error) {
	aid, err := db.ParseUUID(abID)
	if err != nil {
		return nil, err
	}
	return s.q.ListAddressbookObjects(ctx, aid)
}

func (s *Service) DeleteAddressbookObject(ctx context.Context, abID, uid string) error {
	aid, err := db.ParseUUID(abID)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)
	n, err := qtx.DeleteAddressbookObject(ctx, db.DeleteAddressbookObjectParams{AddressbookID: aid, Uid: uid})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if err = qtx.BumpAddressbookCtag(ctx, aid); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
