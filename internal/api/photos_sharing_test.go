package api

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"discodrive/internal/db"
)

func (e *photosEnv) user3() (string, db.User) {
	e.t.Helper()
	tok, u, err := e.svc.Register(e.ctx, "c@x.test", "password12")
	if err != nil {
		e.t.Fatal(err)
	}
	return tok, u
}

func (e *photosEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.pool.Exec(e.ctx, sql, args...); err != nil {
		e.t.Fatal(err)
	}
}

func bucketTotal(e *photosEnv, tok, query string) int {
	e.t.Helper()
	var bs []struct{ Count int }
	e.json(e.do(tok, "GET", "/me/photos/v1/buckets"+query, nil), 200, &bs)
	n := 0
	for _, b := range bs {
		n += b.Count
	}
	return n
}

type rowResp struct {
	ID       string
	Own      bool
	Favorite bool
}

func monthIDs(e *photosEnv, tok, month, query string) map[string]rowResp {
	e.t.Helper()
	var rows []rowResp
	e.json(e.do(tok, "GET", "/me/photos/v1/months/"+month+query, nil), 200, &rows)
	out := map[string]rowResp{}
	for _, r := range rows {
		out[r.ID] = r
	}
	return out
}

// A partner sees the owner's gallery read-only: not the hidden cards, not files a third
// person shared with the owner, and in a mixed feed one copy of a shared file, their own.
func TestPhotosPartner_FeedAndCards(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	_, carol := e.user3()
	hash := strings.Repeat("d", 64)
	a1 := e.asset(e.user, assetOpts{taken: tm(2020, 3)})
	aDup := e.asset(e.user, assetOpts{taken: tm(2021, 6)})
	aHidden := e.asset(e.user, assetOpts{taken: tm(2020, 3), hidden: true})
	aForeign := e.asset(e.user, assetOpts{taken: tm(2020, 3)}) // its file is Carol's
	e.exec(`UPDATE nodes SET user_id = $2 WHERE id = $1`, aForeign.NodeID, carol.ID)
	b1 := e.asset(e.user2, assetOpts{taken: tm(2022, 1)})
	bDup := e.asset(e.user2, assetOpts{taken: tm(2021, 6)})
	e.exec(`UPDATE photo_assets SET content_hash = $2 WHERE id = ANY($1)`, []any{aDup.ID, bDup.ID}, hash)
	owner := db.UUIDString(e.user.ID)
	id := db.UUIDString

	if c := e.do(e.tok2, "GET", "/me/photos/v1/buckets?owner="+owner, nil).Code; c != 404 {
		t.Fatalf("feed of a stranger: %d", c)
	}
	if c := e.do(e.tok2, "GET", "/me/photos/v1/assets/"+id(a1.ID), nil).Code; c != 404 {
		t.Fatalf("card of a stranger: %d", c)
	}
	for _, email := range []string{"nobody@x.test", "a@x.test"} {
		if c := e.do(e.tok, "POST", "/me/photos/v1/partners", map[string]string{"email": email}).Code; c != 404 {
			t.Fatalf("sharing with %s: %d", email, c)
		}
	}
	var st struct{ SharedWithMe bool }
	e.json(e.do(e.tok2, "GET", "/me/photos/v1/settings", nil), 200, &st)
	if st.SharedWithMe {
		t.Fatal("shared with B before sharing")
	}
	e.json(e.do(e.tok, "POST", "/me/photos/v1/partners", map[string]string{"email": "b@x.test"}), 200, nil)
	e.json(e.do(e.tok2, "GET", "/me/photos/v1/settings", nil), 200, &st)
	if !st.SharedWithMe {
		t.Fatal("settings do not tell B a gallery is open to them")
	}

	var ps struct {
		Sharing, SharedWithMe []struct {
			UserID, Email string
			InFeed        *bool
		}
	}
	e.json(e.do(e.tok2, "GET", "/me/photos/v1/partners", nil), 200, &ps)
	if len(ps.SharedWithMe) != 1 || ps.SharedWithMe[0].UserID != owner || *ps.SharedWithMe[0].InFeed {
		t.Fatalf("shared with B: %+v", ps)
	}

	if n := bucketTotal(e, e.tok2, "?owner="+owner); n != 2 {
		t.Fatalf("A's gallery as B sees it: %d cards, want a1 and aDup", n)
	}
	rows := monthIDs(e, e.tok2, "2020-03", "?owner="+owner)
	if len(rows) != 1 || rows[id(a1.ID)].Own {
		t.Fatalf("March 2020 as B: %+v", rows)
	}

	var card struct {
		Own   bool
		Owner *string
		File  map[string]any
	}
	e.json(e.do(e.tok2, "GET", "/me/photos/v1/assets/"+id(a1.ID), nil), 200, &card)
	if card.Own || card.Owner == nil || *card.Owner != "a@x.test" || card.File["nodeId"] != nil || card.File["folderPath"] != nil {
		t.Fatalf("A's card as B: %+v", card)
	}
	for name, a := range map[string]db.PhotoAsset{"hidden": aHidden, "foreign file": aForeign} {
		if c := e.do(e.tok2, "GET", "/me/photos/v1/assets/"+id(a.ID), nil).Code; c != 404 {
			t.Fatalf("%s card: %d", name, c)
		}
		if c := e.do(e.tok2, "GET", "/me/photos/v1/assets/"+id(a.ID)+"/original", nil).Code; c != 404 {
			t.Fatalf("%s original: %d", name, c)
		}
	}
	if rec := e.do(e.tok2, "GET", "/me/photos/v1/assets/"+id(a1.ID)+"/original", nil); rec.Code != 200 || rec.Body.String() != "x" {
		t.Fatalf("original: %d %q", rec.Code, rec.Body.String())
	}
	// A partner plays A's video through the gallery, not through A's Files.
	vid := e.asset(e.user, assetOpts{kind: "video", codec: "h264", taken: tm(2020, 3)})
	var play struct {
		URL   string
		Ready bool
	}
	e.json(e.do(e.tok2, "POST", "/me/photos/v1/assets/"+id(vid.ID)+"/play", nil), 200, &play)
	if !play.Ready || !strings.Contains(play.URL, "/me/photos/v1/assets/"+id(vid.ID)+"/video?") || !strings.Contains(play.URL, "src=original") {
		t.Fatalf("play as partner: %+v", play)
	}
	if rec := e.do("", "GET", play.URL, nil); rec.Code != 200 || rec.Body.String() != "x" {
		t.Fatalf("partner's stream: %d %q", rec.Code, rec.Body.String())
	}
	// Read-only: B's edits do not reach A's cards.
	e.do(e.tok2, "PATCH", "/me/photos/v1/assets", map[string]any{"ids": []string{id(a1.ID)}, "hidden": true})
	if a, _ := e.q.GetPhotoAssetByID(e.ctx, a1.ID); a.Hidden {
		t.Fatal("B hid A's photo")
	}

	// B's own feed until B mixes A in; then the file both have counts once, as B's.
	if n := bucketTotal(e, e.tok2, ""); n != 2 {
		t.Fatalf("B's feed: %d", n)
	}
	if c := e.do(e.tok2, "PUT", "/me/photos/v1/partners/"+owner+"/feed", map[string]bool{"inFeed": true}).Code; c != 204 {
		t.Fatalf("in feed: %d", c)
	}
	if n := bucketTotal(e, e.tok2, ""); n != 4 {
		t.Fatalf("mixed feed: %d cards, want b1, bDup, a1, vid", n)
	}
	june := monthIDs(e, e.tok2, "2021-06", "")
	if _, ok := june[id(bDup.ID)]; !ok || len(june) != 1 {
		t.Fatalf("June 2021 mixed: %+v (want only B's copy)", june)
	}
	// Favourites are the viewer's own.
	e.exec(`UPDATE photo_assets SET favorite = true WHERE id = $1`, a1.ID)
	if n := bucketTotal(e, e.tok2, "?favorite=true"); n != 0 {
		t.Fatalf("A's favourite counted as B's: %d", n)
	}
	if r := monthIDs(e, e.tok2, "2020-03", ""); r[id(a1.ID)].Favorite {
		t.Fatal("A's favourite shown as B's")
	}
	_ = b1

	// Closing it closes everything at once.
	if c := e.do(e.tok, "DELETE", "/me/photos/v1/partners/"+id(e.user2.ID), nil).Code; c != 204 {
		t.Fatalf("revoke: %d", c)
	}
	if c := e.do(e.tok2, "GET", "/me/photos/v1/assets/"+id(a1.ID), nil).Code; c != 404 {
		t.Fatalf("card after revoking: %d", c)
	}
	if n := bucketTotal(e, e.tok2, ""); n != 2 {
		t.Fatalf("B's feed after revoking: %d", n)
	}
	// The viewer can walk away too.
	e.json(e.do(e.tok, "POST", "/me/photos/v1/partners", map[string]string{"email": "b@x.test"}), 200, nil)
	if c := e.do(e.tok2, "DELETE", "/me/photos/v1/partners/"+owner+"/received", nil).Code; c != 204 {
		t.Fatalf("leave: %d", c)
	}
	if c := e.do(e.tok2, "GET", "/me/photos/v1/buckets?owner="+owner, nil).Code; c != 404 {
		t.Fatalf("feed after leaving: %d", c)
	}
}

type albumResp struct {
	ID, Name, OwnerEmail string
	Own                  bool
	Count, Members       int
	Cover                *rowResp
}

func TestPhotosAlbums_MembersAndItems(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	tok3, _ := e.user3()
	id := db.UUIDString
	a1 := e.asset(e.user, assetOpts{taken: tm(2020, 3), jpeg: true})
	a2 := e.asset(e.user, assetOpts{taken: tm(2020, 4)})
	b1 := e.asset(e.user2, assetOpts{taken: tm(2020, 5)})

	var created struct {
		ID    string
		Added int
	}
	e.json(e.do(e.tok, "POST", "/me/photos/v1/albums", map[string]any{"name": " Trip ", "assetIds": []string{id(a1.ID)}}), 201, &created)
	if created.Added != 1 {
		t.Fatalf("created %+v", created)
	}
	al := "/me/photos/v1/albums/" + created.ID
	if c := e.do(e.tok, "POST", "/me/photos/v1/albums", map[string]any{"name": "  "}).Code; c != 400 {
		t.Fatalf("empty name: %d", c)
	}
	// Somebody else's card cannot be put in by a stranger to it.
	if c := e.do(e.tok, "POST", "/me/photos/v1/albums", map[string]any{"name": "x", "assetIds": []string{id(b1.ID)}}).Code; c != 404 {
		t.Fatalf("stranger's card: %d", c)
	}

	if c := e.do(e.tok2, "GET", al, nil).Code; c != 404 {
		t.Fatalf("album of a stranger: %d", c)
	}
	e.json(e.do(e.tok, "POST", al+"/members", map[string]string{"email": "b@x.test"}), 200, nil)

	var albums []albumResp
	e.json(e.do(e.tok2, "GET", "/me/photos/v1/albums", nil), 200, &albums)
	if len(albums) != 1 || albums[0].Own || albums[0].Name != "Trip" || albums[0].Count != 1 || albums[0].OwnerEmail != "a@x.test" ||
		albums[0].Cover == nil || albums[0].Cover.ID != id(a1.ID) {
		t.Fatalf("B's albums: %+v", albums)
	}
	if n := bucketTotal(e, e.tok2, "?album="+created.ID); n != 1 {
		t.Fatalf("album as B: %d", n)
	}
	if rows := monthIDs(e, e.tok2, "2020-03", "?album="+created.ID); len(rows) != 1 || rows[id(a1.ID)].Own {
		t.Fatalf("album month as B: %+v", rows)
	}
	// A member sees the album's cards and may add their own; not the owner's others.
	if c := e.do(e.tok2, "GET", "/me/photos/v1/assets/"+id(a1.ID)+"/thumb", nil).Code; c != 200 {
		t.Fatalf("album card's thumbnail for a member: %d", c)
	}
	if c := e.do(e.tok2, "GET", "/me/photos/v1/assets/"+id(a2.ID), nil).Code; c != 404 {
		t.Fatalf("owner's card outside the album: %d", c)
	}
	e.json(e.do(e.tok2, "POST", al+"/items", map[string]any{"assetIds": []string{id(b1.ID)}}), 200, nil)
	if c := e.do(e.tok2, "POST", al+"/items", map[string]any{"assetIds": []string{id(a2.ID)}}).Code; c != 404 {
		t.Fatalf("member adding an unseen card: %d", c)
	}
	if n := bucketTotal(e, e.tok, "?album="+created.ID); n != 2 {
		t.Fatalf("album as A: %d", n)
	}
	// A member takes out only what they put in.
	var rm struct{ Removed, Kept int }
	e.json(e.do(e.tok2, "POST", al+"/items/remove", map[string]any{"assetIds": []string{id(a1.ID), id(b1.ID)}}), 200, &rm)
	if rm.Removed != 1 || rm.Kept != 1 {
		t.Fatalf("member removing: %+v", rm)
	}

	// Hidden cards stay in the album without showing.
	e.exec(`UPDATE photo_assets SET hidden = true WHERE id = $1`, a1.ID)
	if n := bucketTotal(e, e.tok, "?album="+created.ID); n != 0 {
		t.Fatalf("hidden card shown in the album: %d", n)
	}
	if c := e.do(e.tok2, "GET", "/me/photos/v1/assets/"+id(a1.ID), nil).Code; c != 404 {
		t.Fatalf("hidden card for a member: %d", c)
	}
	e.exec(`UPDATE photo_assets SET hidden = false WHERE id = $1`, a1.ID)
	if n := bucketTotal(e, e.tok, "?album="+created.ID); n != 1 {
		t.Fatalf("unhidden card back: %d", n)
	}

	// Only the owner manages; a member leaves.
	if c := e.do(tok3, "GET", "/me/photos/v1/buckets?album="+created.ID, nil).Code; c != 404 {
		t.Fatalf("stranger in the album: %d", c)
	}
	if c := e.do(e.tok2, "PATCH", al, map[string]string{"name": "Mine"}).Code; c != 403 {
		t.Fatalf("member renaming: %d", c)
	}
	if c := e.do(e.tok2, "POST", al+"/links", map[string]any{}).Code; c != 403 {
		t.Fatalf("member making a link: %d", c)
	}
	if c := e.do(e.tok2, "DELETE", al, nil).Code; c != 204 {
		t.Fatalf("leave: %d", c)
	}
	e.json(e.do(e.tok2, "GET", "/me/photos/v1/albums", nil), 200, &albums)
	if len(albums) != 0 {
		t.Fatalf("left album still listed: %+v", albums)
	}
	if c := e.do(e.tok, "PATCH", al, map[string]string{"name": "Trip 2020"}).Code; c != 204 {
		t.Fatalf("rename: %d", c)
	}
	if c := e.do(e.tok, "DELETE", al, nil).Code; c != 204 {
		t.Fatalf("delete: %d", c)
	}
	if a, err := e.q.GetPhotoAssetByID(e.ctx, a1.ID); err != nil || a.TrashedAt.Valid {
		t.Fatalf("deleting the album touched the photo: %v %+v", err, a)
	}
}

func TestPhotosAlbumLinks_Public(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	id := db.UUIDString
	a1 := e.asset(e.user, assetOpts{taken: tm(2020, 3), jpeg: true})
	var created struct{ ID string }
	e.json(e.do(e.tok, "POST", "/me/photos/v1/albums", map[string]any{"name": "Wedding", "assetIds": []string{id(a1.ID)}}), 201, &created)
	al := "/me/photos/v1/albums/" + created.ID

	type link struct {
		ID, Token   string
		HasPassword bool
	}
	var open link
	e.json(e.do(e.tok, "POST", al+"/links", map[string]any{}), 201, &open)
	pub := "/public/photos/v1/" + open.Token
	var meta struct {
		Name          string
		Locked        bool
		AllowDownload bool
		Count         int
	}
	e.json(e.do("", "GET", pub, nil), 200, &meta)
	if meta.Name != "Wedding" || meta.Locked || meta.AllowDownload || meta.Count != 1 {
		t.Fatalf("meta %+v", meta)
	}
	var items struct {
		Items []rowResp
		Next  *string
	}
	e.json(e.do("", "GET", pub+"/items", nil), 200, &items)
	if len(items.Items) != 1 || items.Items[0].ID != id(a1.ID) || items.Items[0].Own || items.Next != nil {
		t.Fatalf("items %+v", items)
	}
	if c := e.do("", "GET", pub+"/assets/"+id(a1.ID)+"/thumb", nil).Code; c != 200 {
		t.Fatalf("thumb: %d", c)
	}
	if c := e.do("", "GET", pub+"/assets/"+id(a1.ID)+"/original", nil).Code; c != 403 {
		t.Fatalf("original with downloads off: %d", c)
	}
	other := e.asset(e.user, assetOpts{})
	if c := e.do("", "GET", pub+"/assets/"+id(other.ID)+"/thumb", nil).Code; c != 404 {
		t.Fatalf("card outside the album: %d", c)
	}

	// Password and downloads.
	var locked link
	e.json(e.do(e.tok, "POST", al+"/links", map[string]any{"password": "s3cret", "allowDownload": true}), 201, &locked)
	lp := "/public/photos/v1/" + locked.Token
	meta.Count = 0
	e.json(e.do("", "GET", lp, nil), 200, &meta)
	if !meta.Locked || meta.Count != 0 || !meta.AllowDownload {
		t.Fatalf("locked meta %+v", meta)
	}
	if c := e.do("", "GET", lp+"/items", nil).Code; c != 401 {
		t.Fatalf("items without the password: %d", c)
	}
	if c := e.do("", "POST", lp+"/unlock", map[string]string{"password": "nope"}).Code; c != 401 {
		t.Fatalf("wrong password: %d", c)
	}
	var key struct{ Key string }
	e.json(e.do("", "POST", lp+"/unlock", map[string]string{"password": "s3cret"}), 200, &key)
	if c := e.do("", "GET", lp+"/items?k="+key.Key, nil).Code; c != 200 {
		t.Fatalf("items with the key: %d", c)
	}
	if c := e.do("", "GET", pub+"/items?k="+key.Key, nil).Code; c != 200 {
		t.Fatalf("open link with a stray key: %d", c)
	}
	if rec := e.do("", "GET", lp+"/assets/"+id(a1.ID)+"/original?k="+key.Key, nil); rec.Code != 200 || rec.Body.Len() == 0 {
		t.Fatalf("original with downloads on: %d", rec.Code)
	}
	// A key opens its own link only.
	var third link
	e.json(e.do(e.tok, "POST", al+"/links", map[string]any{"password": "other"}), 201, &third)
	if c := e.do("", "GET", "/public/photos/v1/"+third.Token+"/items?k="+key.Key, nil).Code; c != 401 {
		t.Fatalf("key of another link: %d", c)
	}

	// Hidden cards are not shown; expired and deleted links are gone.
	e.exec(`UPDATE photo_assets SET hidden = true WHERE id = $1`, a1.ID)
	e.json(e.do("", "GET", pub+"/items", nil), 200, &items)
	if len(items.Items) != 0 {
		t.Fatalf("hidden card on the public page: %+v", items)
	}
	e.exec(`UPDATE photo_album_links SET expires_at = $2 WHERE token = $1`, open.Token, time.Now().Add(-time.Minute))
	if c := e.do("", "GET", pub, nil).Code; c != 404 {
		t.Fatalf("expired link: %d", c)
	}
	if c := e.do(e.tok, "DELETE", al+"/links/"+locked.ID, nil).Code; c != 204 {
		t.Fatalf("delete link: %d", c)
	}
	if c := e.do("", "GET", lp+"/items?k="+key.Key, nil).Code; c != 404 {
		t.Fatalf("deleted link: %d", c)
	}
	var links []link
	e.json(e.do(e.tok, "GET", al+"/links", nil), 200, &links)
	if len(links) != 2 {
		t.Fatalf("links left: %+v", links)
	}
}

// What a partner put in albums goes when the partnership does (or the owner's gallery is
// switched off): no album or link keeps the owner's photos open.
func TestPhotosSharing_RevokingReachesAlbums(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	tok3, _ := e.user3()
	id := db.UUIDString
	a1 := e.asset(e.user, assetOpts{taken: tm(2020, 3)})
	b1 := e.asset(e.user2, assetOpts{taken: tm(2020, 4)})
	e.json(e.do(e.tok, "POST", "/me/photos/v1/partners", map[string]string{"email": "b@x.test"}), 200, nil)

	var created struct{ ID string }
	e.json(e.do(e.tok2, "POST", "/me/photos/v1/albums", map[string]any{"name": "B's", "assetIds": []string{id(a1.ID), id(b1.ID)}}), 201, &created)
	al := "/me/photos/v1/albums/" + created.ID
	e.json(e.do(e.tok2, "POST", al+"/members", map[string]string{"email": "c@x.test"}), 200, nil)
	var link struct{ Token string }
	e.json(e.do(e.tok2, "POST", al+"/links", map[string]any{"allowDownload": true}), 201, &link)
	pub := "/public/photos/v1/" + link.Token

	// C sees A's photo through B's album, but not who A is.
	var card struct{ Owner *string }
	e.json(e.do(tok3, "GET", "/me/photos/v1/assets/"+id(a1.ID), nil), 200, &card)
	if card.Owner != nil {
		t.Fatalf("a stranger to A sees A's email: %q", *card.Owner)
	}

	// A switches the gallery off: closed everywhere.
	e.exec(`UPDATE photo_settings SET enabled = false WHERE user_id = $1`, e.user.ID)
	for name, tok := range map[string]string{"partner": e.tok2, "member": tok3} {
		if c := e.do(tok, "GET", "/me/photos/v1/assets/"+id(a1.ID), nil).Code; c != 404 {
			t.Fatalf("%s sees a card of a switched-off gallery: %d", name, c)
		}
	}
	if n := bucketTotal(e, e.tok2, "?album="+created.ID); n != 1 {
		t.Fatalf("album with A's gallery off: %d, want B's own only", n)
	}
	e.exec(`UPDATE photo_settings SET enabled = true WHERE user_id = $1`, e.user.ID)
	if n := bucketTotal(e, e.tok2, "?album="+created.ID); n != 2 {
		t.Fatalf("album with A's gallery back on: %d", n)
	}

	// A closes the gallery to B.
	if c := e.do(e.tok, "DELETE", "/me/photos/v1/partners/"+id(e.user2.ID), nil).Code; c != 204 {
		t.Fatalf("revoke: %d", c)
	}
	for name, tok := range map[string]string{"partner": e.tok2, "member": tok3} {
		if c := e.do(tok, "GET", "/me/photos/v1/assets/"+id(a1.ID)+"/original", nil).Code; c != 404 {
			t.Fatalf("%s still reads A's original through the album: %d", name, c)
		}
	}
	if n := bucketTotal(e, e.tok2, "?album="+created.ID); n != 1 {
		t.Fatalf("album after revoking: %d, want B's own only", n)
	}
	var items struct{ Items []rowResp }
	e.json(e.do("", "GET", pub+"/items", nil), 200, &items)
	if len(items.Items) != 1 || items.Items[0].ID != id(b1.ID) {
		t.Fatalf("public page after revoking: %+v", items.Items)
	}
	if c := e.do("", "GET", pub+"/assets/"+id(a1.ID)+"/original", nil).Code; c != 404 {
		t.Fatalf("public original of A's photo after revoking: %d", c)
	}
}

// Hiding one's own copy hides the file: a partner's copy of it does not take its place,
// and the map's area grid is one's own, like the map.
func TestPhotosPartner_OwnHiddenCopyAndMap(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	id := db.UUIDString
	hash := strings.Repeat("e", 64)
	aDup := e.asset(e.user, assetOpts{taken: tm(2021, 6)})
	bDup := e.asset(e.user2, assetOpts{taken: tm(2021, 6), hidden: true})
	e.exec(`UPDATE photo_assets SET content_hash = $2 WHERE id = ANY($1)`, []any{aDup.ID, bDup.ID}, hash)
	aMap := e.asset(e.user, assetOpts{taken: tm(2020, 1)})
	bMap := e.asset(e.user2, assetOpts{taken: tm(2020, 1)})
	e.exec(`UPDATE photo_assets SET lat = 42.87, lon = 74.59 WHERE id = ANY($1)`, []any{aMap.ID, bMap.ID})
	e.json(e.do(e.tok, "POST", "/me/photos/v1/partners", map[string]string{"email": "b@x.test"}), 200, nil)
	if c := e.do(e.tok2, "PUT", "/me/photos/v1/partners/"+id(e.user.ID)+"/feed", map[string]bool{"inFeed": true}).Code; c != 204 {
		t.Fatalf("in feed: %d", c)
	}
	if rows := monthIDs(e, e.tok2, "2021-06", ""); len(rows) != 0 {
		t.Fatalf("B hid the file, A's copy shows instead: %+v", rows)
	}
	cx, cy := mercCell(42.87, 74.59, 6)
	area := "?area=6/" + strconv.FormatInt(cx, 10) + "." + strconv.FormatInt(cy, 10)
	if n := bucketTotal(e, e.tok2, area); n != 1 {
		t.Fatalf("map area grid: %d cards, want B's own only", n)
	}
}
