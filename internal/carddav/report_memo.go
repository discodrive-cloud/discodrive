package carddav

import (
	"context"
	"net/url"

	"github.com/beevik/etree"

	"discodrive/internal/dav"
	"discodrive/internal/db"
)

// reportMemo holds what one REPORT has already read. go-webdav answers a multiget by
// asking the backend for every href separately, and the raw-vCard pass (augmentReport)
// asks again: without the memo each card cost a collection lookup, an access check and
// two reads of the card. A REPORT changes nothing, so within it nothing goes stale.
type reportMemo struct {
	books map[string]resolvedBook // by collection uri
	// cards holds, per address book id, the cards fetched ahead for a multiget; a uid
	// missing from a fetched book does not exist.
	cards map[string]map[string]dav.StoredObject
}

type resolvedBook struct {
	book db.Addressbook
	err  error
}

type reportMemoKey struct{}

func withReportMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, reportMemoKey{}, &reportMemo{
		books: map[string]resolvedBook{}, cards: map[string]map[string]dav.StoredObject{},
	})
}

func reportMemoOf(ctx context.Context) *reportMemo {
	m, _ := ctx.Value(reportMemoKey{}).(*reportMemo)
	return m
}

// object returns a card's data and ETag, from the memo when the REPORT fetched it ahead.
func (b *Backend) object(ctx context.Context, abID, uid string) (data, etag string, err error) {
	if m := reportMemoOf(ctx); m != nil {
		if cards, fetched := m.cards[abID]; fetched {
			if o, ok := cards[uid]; ok {
				return o.Data, o.ETag, nil
			}
			return "", "", dav.ErrNotFound
		}
	}
	return b.svc.GetAddressbookObject(ctx, abID, uid)
}

// prefetchMultiget reads the cards an addressbook-multiget names with one query per
// address book. Anything it cannot make sense of is left to the per-href path.
func (b *Backend) prefetchMultiget(ctx context.Context, body []byte) {
	m := reportMemoOf(ctx)
	if m == nil || len(body) == 0 {
		return
	}
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(body); err != nil {
		return
	}
	root := doc.Root()
	if root == nil || root.Tag != "addressbook-multiget" {
		return
	}
	uids := map[string][]string{} // by collection uri
	for _, href := range root.SelectElements("href") {
		u, err := url.Parse(href.Text()) // a path or an absolute URL; Path comes unescaped
		if err != nil {
			continue
		}
		if _, uri, obj := parsePath(u.Path); uri != "" && obj != "" {
			uids[uri] = append(uids[uri], obj)
		}
	}
	for uri, list := range uids {
		ab, err := b.resolveAddressbook(ctx, uri)
		if err != nil {
			continue
		}
		id := db.UUIDString(ab.ID)
		if cards, err := b.svc.AddressbookObjectsByUIDs(ctx, id, list); err == nil {
			m.cards[id] = cards
		}
	}
}
