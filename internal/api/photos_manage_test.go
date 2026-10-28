package api

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/auth"
	"discodrive/internal/db"
)

// unlock runs the password approval for photos:hidden and trades it for a hidden token.
func (e *photosEnv) unlock(tok string) string {
	e.t.Helper()
	var ap map[string]string
	e.json(e.do(tok, "POST", "/me/identity/approval/password", map[string]any{"password": "password12", "action": "photos:hidden"}), 200, &ap)
	var out map[string]string
	e.json(e.do(tok, "POST", "/me/photos/v1/hidden/unlock", map[string]any{"approvalToken": ap["approval_token"]}), 200, &out)
	if out["token"] == "" || out["expiresAt"] == "" {
		e.t.Fatalf("unlock = %v", out)
	}
	return out["token"]
}

func (e *photosEnv) month(tok, path string, hdr ...string) []photoRowDTO {
	e.t.Helper()
	var rows []photoRowDTO
	e.json(e.do(tok, "GET", path, nil, hdr...), 200, &rows)
	return rows
}

func TestPhotosHidden_NotInTimeline(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	a := e.asset(e.user, assetOpts{})
	id := db.UUIDString(a.ID)
	e.json(e.do(e.tok, "PATCH", "/me/photos/v1/assets", map[string]any{"ids": []string{id}, "hidden": true}), 200, nil)
	if rows := e.month(e.tok, "/me/photos/v1/months/2019-07"); len(rows) != 0 {
		t.Fatalf("hidden card in the timeline: %+v", rows)
	}
	var b []map[string]any
	e.json(e.do(e.tok, "GET", "/me/photos/v1/buckets", nil), 200, &b)
	if len(b) != 0 {
		t.Fatalf("buckets = %v", b)
	}
	e.json(e.do(e.tok, "GET", "/me/photos/v1/assets/"+id, nil), 404, nil)

	h := e.unlock(e.tok)
	if rows := e.month(e.tok, "/me/photos/v1/hidden?month=2019-07", "X-Photos-Hidden", h); len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("hidden list = %+v", rows)
	}
	e.json(e.do(e.tok, "GET", "/me/photos/v1/assets/"+id, nil, "X-Photos-Hidden", h), 200, nil)

	// A foreign user's token does not open it, and unhiding brings the card back.
	e.json(e.do(e.tok2, "GET", "/me/photos/v1/assets/"+id, nil, "X-Photos-Hidden", e.unlock(e.tok2)), 404, nil)
	e.json(e.do(e.tok, "PATCH", "/me/photos/v1/assets", map[string]any{"ids": []string{id}, "hidden": false}), 404, nil) // locked
	e.json(e.do(e.tok, "PATCH", "/me/photos/v1/assets", map[string]any{"ids": []string{id}, "hidden": false}, "X-Photos-Hidden", h), 200, nil)
	if rows := e.month(e.tok, "/me/photos/v1/months/2019-07"); len(rows) != 1 {
		t.Fatalf("unhidden card missing: %+v", rows)
	}
}

func TestPhotosFavorite_FollowsChildren(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	a := e.asset(e.user, assetOpts{})
	kid := e.asset(e.user, assetOpts{})
	if err := e.q.SetPhotoParent(e.ctx, db.SetPhotoParentParams{ID: kid.ID, ParentID: a.ID, Role: pgtype.Text{String: "live-video", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	e.json(e.do(e.tok, "PATCH", "/me/photos/v1/assets", map[string]any{"ids": []string{db.UUIDString(a.ID)}, "favorite": true, "hidden": true}), 200, nil)
	got, _ := e.q.GetPhotoAsset(e.ctx, db.GetPhotoAssetParams{ID: kid.ID, UserID: e.user.ID})
	if !got.Favorite || !got.Hidden {
		t.Fatalf("child flags = fav %v hidden %v", got.Favorite, got.Hidden)
	}
	// foreign and unknown ids fail the whole batch; an empty body is a bad request
	e.json(e.do(e.tok2, "PATCH", "/me/photos/v1/assets", map[string]any{"ids": []string{db.UUIDString(a.ID)}, "favorite": true}), 404, nil)
	e.json(e.do(e.tok, "PATCH", "/me/photos/v1/assets", map[string]any{"ids": []string{}}), 400, nil)
}

func TestPhotosHidden_UnlockRequired(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	e.json(e.do(e.tok, "GET", "/me/photos/v1/hidden?month=2019-07", nil), 403, nil)
	e.json(e.do(e.tok, "GET", "/me/photos/v1/hidden?month=2019-07", nil, "X-Photos-Hidden", "garbage"), 403, nil)
	e.json(e.do(e.tok, "POST", "/me/photos/v1/hidden/unlock", map[string]any{"approvalToken": "garbage"}), 401, nil)

	// An approval for another action does not unlock; a real one works once.
	var ap map[string]string
	e.json(e.do(e.tok, "POST", "/me/identity/approval/password", map[string]any{"password": "password12", "action": "register"}), 200, &ap)
	e.json(e.do(e.tok, "POST", "/me/photos/v1/hidden/unlock", map[string]any{"approvalToken": ap["approval_token"]}), 401, nil)
	e.json(e.do(e.tok, "POST", "/me/identity/approval/password", map[string]any{"password": "password12", "action": "photos:hidden"}), 200, &ap)
	e.json(e.do(e.tok, "POST", "/me/photos/v1/hidden/unlock", map[string]any{"approvalToken": ap["approval_token"]}), 200, nil)
	e.json(e.do(e.tok, "POST", "/me/photos/v1/hidden/unlock", map[string]any{"approvalToken": ap["approval_token"]}), 401, nil) // single use
	// Another user's approval is no use either.
	var ap2 map[string]string
	e.json(e.do(e.tok2, "POST", "/me/identity/approval/password", map[string]any{"password": "password12", "action": "photos:hidden"}), 200, &ap2)
	e.json(e.do(e.tok, "POST", "/me/photos/v1/hidden/unlock", map[string]any{"approvalToken": ap2["approval_token"]}), 401, nil)
	// A wrong password gets no approval at all.
	e.json(e.do(e.tok, "POST", "/me/identity/approval/password", map[string]any{"password": "nope", "action": "photos:hidden"}), 401, nil)
}

func TestPhotosHidden_ExpiredToken(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	claims := auth.Claims{Pur: "photos-hidden", Ver: e.user.TokenVersion, RegisteredClaims: jwt.RegisteredClaims{
		Subject: db.UUIDString(e.user.ID), ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Minute))}}
	expired, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("secret"))
	e.json(e.do(e.tok, "GET", "/me/photos/v1/hidden?month=2019-07", nil, "X-Photos-Hidden", expired), 403, nil)
	claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Minute))
	valid, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("secret"))
	e.json(e.do(e.tok, "GET", "/me/photos/v1/hidden?month=2019-07", nil, "X-Photos-Hidden", valid), 200, nil)
}

func TestPhotosHidden_TokenVersionBump(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	h := e.unlock(e.tok)
	e.json(e.do(e.tok, "GET", "/me/photos/v1/hidden?month=2019-07", nil, "X-Photos-Hidden", h), 200, nil)
	fresh, err := e.svc.ChangePassword(e.ctx, db.UUIDString(e.user.ID), "password12", "another-pass34")
	if err != nil {
		t.Fatal(err)
	}
	e.json(e.do(fresh, "GET", "/me/photos/v1/hidden?month=2019-07", nil, "X-Photos-Hidden", h), 403, nil)
}

func TestPhotosDelete_GroupAndCopies(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	card := e.asset(e.user, assetOpts{})
	live := e.asset(e.user, assetOpts{})
	copyA := e.asset(e.user, assetOpts{})
	other := e.asset(e.user, assetOpts{taken: time.Date(2019, 7, 5, 12, 0, 0, 0, time.UTC)})
	if err := e.q.SetPhotoParent(e.ctx, db.SetPhotoParentParams{ID: live.ID, ParentID: card.ID, Role: pgtype.Text{String: "live-video", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	for _, a := range []db.PhotoAsset{card, copyA} {
		if _, err := e.pool.Exec(e.ctx, `UPDATE photo_assets SET content_hash = 'same' WHERE id = $1`, a.ID); err != nil {
			t.Fatal(err)
		}
	}
	var card1 map[string]any
	e.json(e.do(e.tok, "GET", "/me/photos/v1/assets/"+db.UUIDString(card.ID), nil), 200, &card1)
	if card1["copies"] != float64(1) {
		t.Fatalf("copies = %v", card1["copies"])
	}

	var res map[string]int
	e.json(e.do(e.tok, "POST", "/me/photos/v1/assets/delete", map[string]any{"ids": []string{db.UUIDString(card.ID)}}), 200, &res)
	if res["deletedNodes"] != 3 {
		t.Fatalf("deletedNodes = %v, want 3 (card, live video, copy)", res)
	}
	for _, a := range []db.PhotoAsset{card, live, copyA} {
		n, err := e.q.GetNodeAnyState(e.ctx, a.NodeID)
		if err != nil || !n.DeletedAt.Valid {
			t.Fatalf("node of %s not trashed: %v %v", db.UUIDString(a.ID), n.DeletedAt, err)
		}
		got, _ := e.q.GetPhotoAsset(e.ctx, db.GetPhotoAssetParams{ID: a.ID, UserID: e.user.ID})
		if !got.TrashedAt.Valid {
			t.Fatalf("asset %s not in the gallery trash", db.UUIDString(a.ID))
		}
	}
	if n, _ := e.q.GetNodeAnyState(e.ctx, other.NodeID); n.DeletedAt.Valid {
		t.Fatal("an unrelated photo was deleted")
	}
	// Foreign ids are not deletable.
	e.json(e.do(e.tok2, "POST", "/me/photos/v1/assets/delete", map[string]any{"ids": []string{db.UUIDString(other.ID)}}), 404, nil)
}

func TestPhotosTrash_ListAndRestore(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	a := e.asset(e.user, assetOpts{jpeg: true})
	id := db.UUIDString(a.ID)
	e.json(e.do(e.tok, "POST", "/me/photos/v1/assets/delete", map[string]any{"ids": []string{id}}), 200, nil)
	if rows := e.month(e.tok, "/me/photos/v1/months/2019-07"); len(rows) != 0 {
		t.Fatalf("trashed card in the timeline: %+v", rows)
	}
	e.json(e.do(e.tok, "GET", "/me/photos/v1/assets/"+id, nil), 404, nil)

	var trash []struct {
		ID        string `json:"id"`
		TrashedAt string `json:"trashedAt"`
	}
	e.json(e.do(e.tok, "GET", "/me/photos/v1/trash", nil), 200, &trash)
	if len(trash) != 1 || trash[0].ID != id || trash[0].TrashedAt == "" {
		t.Fatalf("trash = %+v", trash)
	}
	// The trash shows thumbnails; nothing else of a trashed card is served.
	e.json(e.do(e.tok, "GET", "/me/photos/v1/assets/"+id+"/thumb", nil), 200, nil)
	e.json(e.do(e.tok2, "GET", "/me/photos/v1/assets/"+id+"/thumb", nil), 404, nil)
	e.json(e.do(e.tok, "POST", "/me/photos/v1/assets/"+id+"/play", map[string]any{}), 404, nil)
	var other []map[string]any
	e.json(e.do(e.tok2, "GET", "/me/photos/v1/trash", nil), 200, &other)
	if len(other) != 0 {
		t.Fatalf("foreign trash = %v", other)
	}

	e.json(e.do(e.tok2, "POST", "/me/photos/v1/trash/restore", map[string]any{"ids": []string{id}}), 404, nil)
	e.json(e.do(e.tok, "POST", "/me/photos/v1/trash/restore", map[string]any{"ids": []string{id}}), 200, nil)
	// The fixture JPEG has no EXIF, so the re-read may date it by the file: look the card up
	// by id instead of by month.
	var card map[string]any
	e.json(e.do(e.tok, "GET", "/me/photos/v1/assets/"+id, nil), 200, &card)
	var buckets []map[string]any
	e.json(e.do(e.tok, "GET", "/me/photos/v1/buckets", nil), 200, &buckets)
	if len(buckets) != 1 {
		t.Fatalf("restored card missing from the timeline: %v", buckets)
	}
	e.json(e.do(e.tok, "GET", "/me/photos/v1/trash", nil), 200, &trash)
	if len(trash) != 0 {
		t.Fatalf("trash after restore = %+v", trash)
	}
	n, _ := e.q.GetNodeAnyState(e.ctx, a.NodeID)
	if n.DeletedAt.Valid {
		t.Fatal("node still deleted")
	}
	// Restoring a card that is not in the trash is a 404.
	e.json(e.do(e.tok, "POST", "/me/photos/v1/trash/restore", map[string]any{"ids": []string{id}}), 404, nil)
}

func (e *photosEnv) setHash(h string, as ...db.PhotoAsset) {
	e.t.Helper()
	for _, a := range as {
		if _, err := e.pool.Exec(e.ctx, `UPDATE photo_assets SET content_hash = $2 WHERE id = $1`, a.ID, h); err != nil {
			e.t.Fatal(err)
		}
	}
}

func TestPhotosFlags_ApplyToCopies(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	day := time.Date(2019, 3, 15, 10, 0, 0, 0, time.UTC)
	a := e.asset(e.user, assetOpts{taken: day})
	b := e.asset(e.user, assetOpts{taken: day}) // the same file in another source
	e.setHash("same", a, b)

	e.json(e.do(e.tok, "PATCH", "/me/photos/v1/assets", map[string]any{"ids": []string{db.UUIDString(a.ID)}, "hidden": true}), 200, nil)
	if rows := e.month(e.tok, "/me/photos/v1/months/2019-03"); len(rows) != 0 {
		t.Fatalf("a twin of a hidden photo shows: %+v", rows)
	}
	var bk []map[string]any
	e.json(e.do(e.tok, "GET", "/me/photos/v1/buckets", nil), 200, &bk)
	if len(bk) != 0 {
		t.Fatalf("buckets = %v", bk)
	}
	var groups []struct {
		Assets []photoRowDTO `json:"assets"`
	}
	e.json(e.do(e.tok, "GET", "/me/photos/v1/on-this-day?date=03-15", nil), 200, &groups)
	if len(groups) != 0 {
		t.Fatalf("on-this-day shows a hidden photo: %+v", groups)
	}

	// The hidden tab: locked without the token, laid out by months with it.
	e.json(e.do(e.tok, "GET", "/me/photos/v1/hidden/buckets", nil), 403, nil)
	h := e.unlock(e.tok)
	e.json(e.do(e.tok, "GET", "/me/photos/v1/hidden/buckets", nil, "X-Photos-Hidden", h), 200, &bk)
	if len(bk) != 1 || bk[0]["month"] != "2019-03" || bk[0]["count"] != float64(2) {
		t.Fatalf("hidden buckets = %v", bk)
	}

	e.json(e.do(e.tok, "PATCH", "/me/photos/v1/assets", map[string]any{"ids": []string{db.UUIDString(b.ID)}, "hidden": false}, "X-Photos-Hidden", h), 200, nil)
	e.json(e.do(e.tok, "PATCH", "/me/photos/v1/assets", map[string]any{"ids": []string{db.UUIDString(a.ID)}, "favorite": true}), 200, nil)
	if rows := e.month(e.tok, "/me/photos/v1/months/2019-03?favorite=true"); len(rows) != 1 || !rows[0].Favorite {
		t.Fatalf("favorite filter = %+v", rows)
	}
	for _, x := range []db.PhotoAsset{a, b} {
		got, _ := e.q.GetPhotoAsset(e.ctx, db.GetPhotoAssetParams{ID: x.ID, UserID: e.user.ID})
		if !got.Favorite || got.Hidden {
			t.Fatalf("%s: favorite %v hidden %v", db.UUIDString(x.ID), got.Favorite, got.Hidden)
		}
	}
}

func TestPhotosDelete_CopiesSkipOtherCardsChildren(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	a := e.asset(e.user, assetOpts{})
	other := e.asset(e.user, assetOpts{taken: time.Date(2019, 7, 5, 12, 0, 0, 0, time.UTC)})
	raw := e.asset(e.user, assetOpts{taken: time.Date(2019, 7, 5, 12, 0, 0, 0, time.UTC)}) // an original inside another card
	if err := e.q.SetPhotoParent(e.ctx, db.SetPhotoParentParams{ID: raw.ID, ParentID: other.ID, Role: pgtype.Text{String: "original", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	e.setHash("h", a, raw)
	get := func() map[string]any {
		var m map[string]any
		e.json(e.do(e.tok, "GET", "/me/photos/v1/assets/"+db.UUIDString(a.ID), nil), 200, &m)
		return m
	}
	if get()["copies"] != float64(0) {
		t.Fatalf("copies counts a child of another card: %v", get()["copies"])
	}
	var res map[string]int
	e.json(e.do(e.tok, "POST", "/me/photos/v1/assets/delete", map[string]any{"ids": []string{db.UUIDString(a.ID)}}), 200, &res)
	if res["deletedNodes"] != 1 {
		t.Fatalf("deletedNodes = %v, want 1", res)
	}
	for _, x := range []db.PhotoAsset{other, raw} {
		if n, _ := e.q.GetNodeAnyState(e.ctx, x.NodeID); n.DeletedAt.Valid {
			t.Fatalf("node %s of another card was deleted", db.UUIDString(x.ID))
		}
	}
}

func TestPhotosMetadata_EditsDateAndPlace(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	a := e.asset(e.user, assetOpts{jpeg: true})
	id := db.UUIDString(a.ID)
	var out struct {
		Updated      []string `json:"updated"`
		Conflicts    []string `json:"conflicts"`
		Failed       []any    `json:"failed"`
		Partial      []string `json:"partial"`
		VersionBytes int64    `json:"versionBytes"`
	}
	e.json(e.do(e.tok, "PUT", "/me/photos/v1/assets/metadata", map[string]any{
		"ids": []string{id}, "takenAt": "2021-03-04T05:06:07Z", "tzOffsetMin": 60,
		"place": map[string]float64{"lat": 48.8566, "lon": 2.3522}}), 200, &out)
	if len(out.Updated) != 1 || len(out.Failed) != 0 || len(out.Partial) != 0 || len(out.Conflicts) != 0 || out.VersionBytes == 0 {
		t.Fatalf("out = %+v", out)
	}
	got, _ := e.q.GetPhotoAsset(e.ctx, db.GetPhotoAssetParams{ID: a.ID, UserID: e.user.ID})
	if got.DateSource != "edit" || !got.TakenAt.Time.Equal(time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)) ||
		got.TzOffsetMin.Int32 != 60 || !got.Lat.Valid {
		t.Fatalf("asset = %+v", got)
	}
	// place:null erases the coordinates; a shift moves the date.
	e.json(e.do(e.tok, "PUT", "/me/photos/v1/assets/metadata", map[string]any{"ids": []string{id}, "place": nil, "shiftMinutes": 90}), 200, nil)
	got, _ = e.q.GetPhotoAsset(e.ctx, db.GetPhotoAssetParams{ID: a.ID, UserID: e.user.ID})
	if got.Lat.Valid || !got.TakenAt.Time.Equal(time.Date(2021, 3, 4, 6, 36, 7, 0, time.UTC)) {
		t.Fatalf("asset = %+v", got)
	}
}

func TestPhotosMetadata_Validation(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	a := e.asset(e.user, assetOpts{jpeg: true})
	id := db.UUIDString(a.ID)
	put := func(tok string, body map[string]any, hdr ...string) int {
		return e.do(tok, "PUT", "/me/photos/v1/assets/metadata", body, hdr...).Code
	}
	many := make([]string, 201)
	for i := range many {
		many[i] = id
	}
	for name, body := range map[string]map[string]any{
		"nothing":             {"ids": []string{id}},
		"both":                {"ids": []string{id}, "takenAt": "2021-03-04T05:06:07Z", "shiftMinutes": 5},
		"offset alone":        {"ids": []string{id}, "tzOffsetMin": 60},
		"bad lat":             {"ids": []string{id}, "place": map[string]float64{"lat": 91, "lon": 0}},
		"half place":          {"ids": []string{id}, "place": map[string]float64{"lat": 10}},
		"no ids":              {"ids": []string{}, "shiftMinutes": 5},
		"too many ids (>200)": {"ids": many, "shiftMinutes": 5},
	} {
		if c := put(e.tok, body); c != 400 {
			t.Errorf("%s: status %d, want 400", name, c)
		}
	}
	if c := put(e.tok2, map[string]any{"ids": []string{id}, "shiftMinutes": 5}); c != 404 {
		t.Errorf("foreign: %d", c)
	}

	// A hidden card is edited only with the unlock token.
	h := e.asset(e.user, assetOpts{jpeg: true, hidden: true})
	hid := db.UUIDString(h.ID)
	if c := put(e.tok, map[string]any{"ids": []string{hid}, "shiftMinutes": 5}); c != 404 {
		t.Errorf("hidden locked: %d", c)
	}
	if c := put(e.tok, map[string]any{"ids": []string{hid}, "shiftMinutes": 5}, "X-Photos-Hidden", e.unlock(e.tok)); c != 200 {
		t.Errorf("hidden unlocked: %d", c)
	}
}

func TestPhotosMetadata_LiteUnavailable(t *testing.T) {
	e := newPhotosEnv(t, false, 0)
	e.json(e.do(e.tok, "PUT", "/me/photos/v1/assets/metadata", map[string]any{"ids": []string{db.UUIDString(e.user.ID)}, "shiftMinutes": 5}), 409, nil)
}
