package carddav

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/carddav"

	"discodrive/internal/dav"
	"discodrive/internal/db"
)

type ctxKey int

const ctxUserKey ctxKey = 0

// WithUserID stores the userID in the context (called by the auth middleware).
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, ctxUserKey, userID)
}

func userID(ctx context.Context) string {
	if v, ok := ctx.Value(ctxUserKey).(string); ok {
		return v
	}
	return ""
}

// Backend implements carddav.Backend on top of dav.Service.
type Backend struct {
	svc *dav.Service
}

func New(svc *dav.Service) *Backend { return &Backend{svc: svc} }

func notFound() error { return webdav.NewHTTPError(404, errors.New("not found")) }

func (b *Backend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return principalPath(userID(ctx)), nil
}

func (b *Backend) AddressBookHomeSetPath(ctx context.Context) (string, error) {
	return homeSetPath(userID(ctx)), nil
}

func (b *Backend) ListAddressBooks(ctx context.Context) ([]carddav.AddressBook, error) {
	uid := userID(ctx)
	abs, err := b.svc.ListAddressbooks(ctx, uid)
	if err != nil {
		return nil, err
	}
	// Auto-provision: the client must have at least one collection on discovery.
	if len(abs) == 0 {
		def, derr := b.svc.EnsureDefaultAddressbook(ctx, uid)
		if derr != nil {
			return nil, derr
		}
		abs = []db.Addressbook{def}
	}
	out := make([]carddav.AddressBook, 0, len(abs))
	for _, a := range abs {
		out = append(out, toDAVAddressbook(uid, a))
	}
	if shared, serr := b.svc.SharedAddressbooksForUser(ctx, uid); serr == nil {
		for _, sa := range shared {
			out = append(out, toDAVAddressbook(uid, sa.Addressbook))
		}
	}
	return out, nil
}

func (b *Backend) GetAddressBook(ctx context.Context, path string) (*carddav.AddressBook, error) {
	uid := userID(ctx)
	_, uri, _ := parsePath(path)
	a, err := b.svc.AddressbookByURI(ctx, uri)
	if errors.Is(err, dav.ErrNotFound) {
		return nil, notFound()
	}
	if err != nil {
		return nil, err
	}
	if ok, _ := b.svc.CanAccessAddressbook(ctx, uid, db.UUIDString(a.ID)); !ok {
		return nil, notFound()
	}
	ab := toDAVAddressbook(uid, a)
	return &ab, nil
}

func (b *Backend) CreateAddressBook(ctx context.Context, ab *carddav.AddressBook) error {
	uid := userID(ctx)
	_, uri, _ := parsePath(ab.Path)
	if uri != "" {
		_, err := b.svc.CreateAddressbookWithURI(ctx, uid, uri, ab.Name)
		return err
	}
	_, err := b.svc.CreateAddressbook(ctx, uid, ab.Name)
	return err
}

func (b *Backend) DeleteAddressBook(ctx context.Context, path string) error {
	uid := userID(ctx)
	_, uri, _ := parsePath(path)
	a, err := b.svc.AddressbookByURI(ctx, uri)
	if errors.Is(err, dav.ErrNotFound) || (err == nil && db.UUIDString(a.UserID) != uid) {
		return notFound()
	}
	if err != nil {
		return err
	}
	return b.svc.DeleteAddressbook(ctx, uid, db.UUIDString(a.ID))
}

func toDAVAddressbook(uid string, a db.Addressbook) carddav.AddressBook {
	return carddav.AddressBook{
		Path: addressbookPath(uid, a.Uri), Name: a.Name,
		SupportedAddressData: []carddav.AddressDataType{
			{ContentType: "text/vcard", Version: "3.0"},
			{ContentType: "text/vcard", Version: "4.0"},
		},
	}
}

// resolveAddressbook looks up the address book by URI and checks access (owner or share).
func (b *Backend) resolveAddressbook(ctx context.Context, uri string) (db.Addressbook, error) {
	a, err := b.svc.AddressbookByURI(ctx, uri)
	if errors.Is(err, dav.ErrNotFound) {
		return db.Addressbook{}, notFound()
	}
	if err != nil {
		return db.Addressbook{}, err
	}
	if ok, _ := b.svc.CanAccessAddressbook(ctx, userID(ctx), db.UUIDString(a.ID)); !ok {
		return db.Addressbook{}, notFound()
	}
	return a, nil
}

func decodeVCard(data string) (vcard.Card, error) {
	return vcard.NewDecoder(strings.NewReader(data)).Decode()
}

func (b *Backend) GetAddressObject(ctx context.Context, path string, req *carddav.AddressDataRequest) (*carddav.AddressObject, error) {
	uid := userID(ctx)
	_, uri, obj := parsePath(path)
	ab, err := b.resolveAddressbook(ctx, uri)
	if err != nil {
		return nil, err
	}
	data, etag, err := b.svc.GetAddressbookObject(ctx, db.UUIDString(ab.ID), obj)
	if errors.Is(err, dav.ErrNotFound) {
		return nil, notFound()
	}
	if err != nil {
		return nil, err
	}
	card, err := decodeVCard(data)
	if err != nil {
		return nil, err
	}
	return &carddav.AddressObject{
		Path: objectPath(uid, uri, obj), ETag: etag,
		ContentLength: int64(len(data)), Card: card,
	}, nil
}

func (b *Backend) ListAddressObjects(ctx context.Context, path string, req *carddav.AddressDataRequest) ([]carddav.AddressObject, error) {
	uid := userID(ctx)
	_, uri, _ := parsePath(path)
	ab, err := b.resolveAddressbook(ctx, uri)
	if err != nil {
		return nil, err
	}
	objs, err := b.svc.ListAddressbookObjects(ctx, db.UUIDString(ab.ID))
	if err != nil {
		return nil, err
	}
	out := make([]carddav.AddressObject, 0, len(objs))
	for _, o := range objs {
		card, derr := decodeVCard(o.Data)
		if derr != nil {
			continue
		}
		out = append(out, carddav.AddressObject{
			Path: objectPath(uid, uri, o.Uid), ETag: o.Etag,
			ModTime: o.UpdatedAt.Time, ContentLength: int64(len(o.Data)), Card: card,
		})
	}
	return out, nil
}

func (b *Backend) QueryAddressObjects(ctx context.Context, path string, query *carddav.AddressBookQuery) ([]carddav.AddressObject, error) {
	objs, err := b.ListAddressObjects(ctx, path, nil)
	if err != nil {
		return nil, err
	}
	return carddav.Filter(query, objs)
}

func (b *Backend) PutAddressObject(ctx context.Context, path string, card vcard.Card, opts *carddav.PutAddressObjectOptions) (*carddav.AddressObject, error) {
	uid := userID(ctx)
	_, uri, obj := parsePath(path)
	ab, err := b.resolveAddressbook(ctx, uri)
	if err != nil {
		return nil, err
	}
	abID := db.UUIDString(ab.ID)

	raw := rawBody(ctx)
	if len(raw) == 0 {
		var buf bytes.Buffer
		if err := vcard.NewEncoder(&buf).Encode(card); err != nil {
			return nil, err
		}
		raw = buf.Bytes()
	}
	// If-Match / If-None-Match are checked by the service inside the write's transaction.
	etag, err := b.svc.PutAddressbookObjectIf(ctx, abID, obj, string(raw), putCheck(opts))
	if errors.Is(err, dav.ErrPrecondition) {
		return nil, webdav.NewHTTPError(http.StatusPreconditionFailed, err)
	}
	if err != nil {
		return nil, err
	}
	return &carddav.AddressObject{Path: objectPath(uid, uri, obj), ETag: etag}, nil
}

// putCheck turns the conditional headers of a PUT into a dav.PutCheck (nil: none set).
func putCheck(opts *carddav.PutAddressObjectOptions) dav.PutCheck {
	if opts == nil || (!opts.IfNoneMatch.IsSet() && !opts.IfMatch.IsSet()) {
		return nil
	}
	return func(exists bool, etag string) bool {
		if opts.IfNoneMatch.IsWildcard() && exists {
			return false
		}
		if opts.IfMatch.IsSet() {
			ok, _ := opts.IfMatch.MatchETag(etag)
			return exists && ok
		}
		return true
	}
}

func (b *Backend) DeleteAddressObject(ctx context.Context, path string) error {
	_, uri, obj := parsePath(path)
	ab, err := b.resolveAddressbook(ctx, uri)
	if err != nil {
		return err
	}
	err = b.svc.DeleteAddressbookObject(ctx, db.UUIDString(ab.ID), obj)
	if errors.Is(err, dav.ErrNotFound) {
		return notFound()
	}
	return err
}

var _ carddav.Backend = (*Backend)(nil)
