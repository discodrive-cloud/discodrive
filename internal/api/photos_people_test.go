package api

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/photos"
	"discodrive/internal/photos/faces"
)

type personResp struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Hidden bool    `json:"hidden"`
	Count  int     `json:"count"`
	Cover  *string `json:"cover"`
}

// withFaces turns people on in the router (a face service that is never called: the
// tests store faces themselves).
func (e *photosEnv) withFaces(th *photos.Thumbs) {
	f := photos.NewFaces(e.q, th, &photos.MLClient{URL: "http://127.0.0.1:1", Model: "buffalo_l", MinScore: 0.7},
		faces.Params{MaxDistance: 0.5, MinFaces: 3}, 1)
	e.h = e.router(WithPhotoFaces(f))
}

func (e *photosEnv) person(u db.User, name string) pgtype.UUID {
	e.t.Helper()
	p, err := e.q.CreatePhotoPerson(e.ctx, u.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	if name != "" {
		if _, err := e.q.UpdatePhotoPerson(e.ctx, db.UpdatePhotoPersonParams{ID: p, UserID: u.ID,
			Name: pgtype.Text{String: name, Valid: true}}); err != nil {
			e.t.Fatal(err)
		}
	}
	return p
}

// face puts a face of person p on card a; size is the side of the box (the cover is the
// largest face).
func (e *photosEnv) face(a db.PhotoAsset, p pgtype.UUID, size float32) pgtype.UUID {
	e.t.Helper()
	id, err := e.q.InsertPhotoFace(e.ctx, db.InsertPhotoFaceParams{UserID: a.UserID, AssetID: a.ID, PersonID: p,
		X1: 0.2, Y1: 0.2, X2: 0.2 + size, Y2: 0.2 + size, Score: 0.9, Embedding: make([]byte, 2048), Model: "auraface"})
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *photosEnv) people(tok, query string) []personResp {
	e.t.Helper()
	var out []personResp
	e.json(e.do(tok, "GET", "/me/photos/v1/people"+query, nil), 200, &out)
	return out
}

func TestPeopleOffWithoutML(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	for _, c := range []struct{ method, path string }{
		{"GET", "/me/photos/v1/people"}, {"PUT", "/me/photos/v1/faces"},
		{"GET", "/me/photos/v1/people/" + db.UUIDString(e.person(e.user, ""))},
	} {
		if rec := e.do(e.tok, c.method, c.path, map[string]any{"enabled": true}); rec.Code != 404 {
			t.Fatalf("%s %s: %d", c.method, c.path, rec.Code)
		}
	}
	var s struct {
		Faces struct{ Available bool } `json:"faces"`
	}
	e.json(e.do(e.tok, "GET", "/me/photos/v1/settings", nil), 200, &s)
	if s.Faces.Available {
		t.Fatal("people offered without a face service")
	}
}

func TestPeopleToggleAndProgress(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	e.withFaces(nil)
	e.asset(e.user, assetOpts{})
	e.json(e.do(e.tok, "PUT", "/me/photos/v1/faces", map[string]any{"enabled": true}), 200, nil)
	var s struct {
		Faces struct {
			Available, Enabled, MLOnline bool
			Done, Total, Pending, Failed int
		} `json:"faces"`
	}
	e.json(e.do(e.tok, "GET", "/me/photos/v1/settings", nil), 200, &s)
	if !s.Faces.Available || !s.Faces.Enabled || s.Faces.Total != 1 || s.Faces.Pending != 1 {
		t.Fatalf("settings %+v", s.Faces)
	}
}

func TestPeopleListCountsVisibleOnly(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	e.withFaces(nil)
	a1, a2 := e.asset(e.user, assetOpts{}), e.asset(e.user, assetOpts{})
	p := e.person(e.user, "")
	e.face(a1, p, 0.1)
	big := e.face(a2, p, 0.3)
	got := e.people(e.tok, "")
	if len(got) != 1 || got[0].Count != 2 || got[0].Cover == nil || *got[0].Cover != db.UUIDString(big) {
		t.Fatalf("people %+v", got)
	}
	// Hiding the photo of the cover: the count drops and the cover moves.
	e.json(e.do(e.tok, "PATCH", "/me/photos/v1/assets", map[string]any{"ids": []string{db.UUIDString(a2.ID)}, "hidden": true}), 200, nil)
	got = e.people(e.tok, "")
	if len(got) != 1 || got[0].Count != 1 || got[0].Cover == nil || *got[0].Cover == db.UUIDString(big) {
		t.Fatalf("after hiding: %+v", got)
	}
	// With no visible photo left the person is not listed.
	e.json(e.do(e.tok, "PATCH", "/me/photos/v1/assets", map[string]any{"ids": []string{db.UUIDString(a1.ID)}, "hidden": true}), 200, nil)
	if got = e.people(e.tok, ""); len(got) != 0 {
		t.Fatalf("person of hidden photos listed: %+v", got)
	}
}

func TestPeopleRenameHideMerge(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	e.withFaces(nil)
	a1, a2, a3 := e.asset(e.user, assetOpts{}), e.asset(e.user, assetOpts{}), e.asset(e.user, assetOpts{})
	target, other, named := e.person(e.user, ""), e.person(e.user, ""), e.person(e.user, "Аня")
	e.face(a1, target, 0.2)
	e.face(a2, other, 0.2)
	e.face(a3, named, 0.2)
	path := "/me/photos/v1/people/" + db.UUIDString(target)

	// Merging into a person without a name takes the first name of the merged ones.
	var merged personResp
	e.json(e.do(e.tok, "POST", path+"/merge", map[string]any{"ids": []string{db.UUIDString(other), db.UUIDString(named)}}), 200, &merged)
	if merged.Name != "Аня" || merged.Count != 3 {
		t.Fatalf("merged %+v", merged)
	}
	if got := e.people(e.tok, ""); len(got) != 1 || got[0].ID != db.UUIDString(target) {
		t.Fatalf("after merge %+v", got)
	}
	if rec := e.do(e.tok, "GET", "/me/photos/v1/people/"+db.UUIDString(other), nil); rec.Code != 404 {
		t.Fatalf("merged-away person: %d", rec.Code)
	}

	e.json(e.do(e.tok, "PATCH", path, map[string]any{"name": "  Анна  "}), 200, nil)
	e.json(e.do(e.tok, "PATCH", path, map[string]any{"hidden": true}), 200, nil)
	if got := e.people(e.tok, ""); len(got) != 0 {
		t.Fatalf("hidden person listed: %+v", got)
	}
	got := e.people(e.tok, "?hidden=true")
	if len(got) != 1 || got[0].Name != "Анна" || !got[0].Hidden {
		t.Fatalf("hidden list %+v", got)
	}
	// A merge with itself or with nothing is refused.
	if rec := e.do(e.tok, "POST", path+"/merge", map[string]any{"ids": []string{db.UUIDString(target)}}); rec.Code != 400 {
		t.Fatalf("merge with itself: %d", rec.Code)
	}
}

func TestPeopleHideMany(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	e.withFaces(nil)
	a1, a2, a3 := e.asset(e.user, assetOpts{}), e.asset(e.user, assetOpts{}), e.asset(e.user, assetOpts{})
	p1, p2, keep := e.person(e.user, ""), e.person(e.user, ""), e.person(e.user, "Аня")
	e.face(a1, p1, 0.2)
	e.face(a2, p2, 0.2)
	e.face(a3, keep, 0.2)
	ids := []string{db.UUIDString(p1), db.UUIDString(p2), db.UUIDString(p1)}

	var res struct {
		Updated int `json:"updated"`
	}
	e.json(e.do(e.tok, "POST", "/me/photos/v1/people/hide", map[string]any{"ids": ids, "hidden": true}), 200, &res)
	if res.Updated != 2 {
		t.Fatalf("updated %d", res.Updated)
	}
	if got := e.people(e.tok, ""); len(got) != 1 || got[0].ID != db.UUIDString(keep) {
		t.Fatalf("visible after hide %+v", got)
	}
	if got := e.people(e.tok, "?hidden=true"); len(got) != 2 {
		t.Fatalf("hidden after hide %+v", got)
	}
	e.json(e.do(e.tok, "POST", "/me/photos/v1/people/hide", map[string]any{"ids": ids[:1], "hidden": false}), 200, &res)
	if got := e.people(e.tok, ""); len(got) != 2 {
		t.Fatalf("visible after show %+v", got)
	}

	// Somebody else's person is not touched; bad input is refused.
	other := e.person(e.user2, "")
	e.json(e.do(e.tok, "POST", "/me/photos/v1/people/hide", map[string]any{"ids": []string{db.UUIDString(other)}, "hidden": true}), 200, &res)
	if res.Updated != 0 {
		t.Fatalf("hid somebody else's person: %d", res.Updated)
	}
	for _, body := range []map[string]any{
		{"ids": []string{}, "hidden": true},
		{"ids": []string{"nope"}, "hidden": true},
		{"ids": ids},
	} {
		if rec := e.do(e.tok, "POST", "/me/photos/v1/people/hide", body); rec.Code != 400 {
			t.Fatalf("%v: %d", body, rec.Code)
		}
	}
}

func TestPeoplePrivate(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	e.withFaces(nil)
	a := e.asset(e.user, assetOpts{})
	p := e.person(e.user, "Аня")
	e.face(a, p, 0.2)
	path := "/me/photos/v1/people/" + db.UUIDString(p)
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", path, nil}, {"PATCH", path, map[string]any{"name": "x"}},
		{"POST", path + "/merge", map[string]any{"ids": []string{db.UUIDString(e.person(e.user2, ""))}}},
	} {
		if rec := e.do(e.tok2, c.method, c.path, c.body); rec.Code != 404 {
			t.Fatalf("other user %s %s: %d", c.method, c.path, rec.Code)
		}
	}
	// Merging another user's person into mine is refused too.
	theirs := e.person(e.user2, "")
	if rec := e.do(e.tok, "POST", path+"/merge", map[string]any{"ids": []string{db.UUIDString(theirs)}}); rec.Code != 404 {
		t.Fatalf("merge of a foreign person: %d", rec.Code)
	}
	// A partner sees the photo but not who is on it.
	e.json(e.do(e.tok, "POST", "/me/photos/v1/partners", map[string]any{"email": "b@x.test"}), 200, nil)
	var card struct {
		People []struct{ ID string } `json:"people"`
	}
	e.json(e.do(e.tok2, "GET", "/me/photos/v1/assets/"+db.UUIDString(a.ID), nil), 200, &card)
	if len(card.People) != 0 {
		t.Fatalf("partner sees people: %+v", card.People)
	}
	e.json(e.do(e.tok, "PUT", "/me/photos/v1/faces", map[string]any{"enabled": true}), 200, nil)
	e.json(e.do(e.tok, "GET", "/me/photos/v1/assets/"+db.UUIDString(a.ID), nil), 200, &card)
	if len(card.People) != 1 || card.People[0].ID != db.UUIDString(p) {
		t.Fatalf("owner's card people: %+v", card.People)
	}
	// Recognition turned off: the card no longer says who is on it.
	e.json(e.do(e.tok, "PUT", "/me/photos/v1/faces", map[string]any{"enabled": false}), 200, nil)
	card.People = nil
	e.json(e.do(e.tok, "GET", "/me/photos/v1/assets/"+db.UUIDString(a.ID), nil), 200, &card)
	if len(card.People) != 0 {
		t.Fatalf("people with recognition off: %+v", card.People)
	}
	// The person filter is the user's own feed only.
	q := "?person=" + db.UUIDString(p) + "&owner=" + db.UUIDString(e.user.ID)
	if rec := e.do(e.tok2, "GET", "/me/photos/v1/buckets"+q, nil); rec.Code != 400 {
		t.Fatalf("person with owner: %d", rec.Code)
	}
	if rec := e.do(e.tok2, "GET", "/me/photos/v1/buckets?person="+db.UUIDString(p), nil); rec.Code != 404 {
		t.Fatalf("someone else's person as a filter: %d", rec.Code)
	}
}

func TestPeopleFeedFilter(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	e.withFaces(nil)
	a1, a2 := e.asset(e.user, assetOpts{}), e.asset(e.user, assetOpts{})
	e.asset(e.user, assetOpts{})
	hid := e.asset(e.user, assetOpts{hidden: true})
	p := e.person(e.user, "")
	e.face(a1, p, 0.2)
	e.face(a2, p, 0.2)
	e.face(hid, p, 0.2)
	q := "?person=" + db.UUIDString(p)
	if n := bucketTotal(e, e.tok, q); n != 2 {
		t.Fatalf("buckets by person: %d", n)
	}
	ids := monthIDs(e, e.tok, "2019-07", q)
	if _, ok := ids[db.UUIDString(a1.ID)]; !ok || len(ids) != 2 {
		t.Fatalf("month by person: %v", ids)
	}
}

func TestPeopleFaceThumb(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	e.withFaces(nil)
	a := e.asset(e.user, assetOpts{jpeg: true})
	hid := e.asset(e.user, assetOpts{jpeg: true, hidden: true})
	p := e.person(e.user, "")
	f := e.face(a, p, 0.5)
	fh := e.face(hid, p, 0.5)
	rec := e.do(e.tok, "GET", "/me/photos/v1/people/faces/"+db.UUIDString(f)+"/thumb", nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/jpeg" || rec.Body.Len() < 100 {
		t.Fatalf("face thumb: %d %s %d", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}
	if rec := e.do(e.tok, "GET", "/me/photos/v1/people/faces/"+db.UUIDString(fh)+"/thumb", nil); rec.Code != 404 {
		t.Fatalf("face of a hidden photo: %d", rec.Code)
	}
	if rec := e.do(e.tok2, "GET", "/me/photos/v1/people/faces/"+db.UUIDString(f)+"/thumb", nil); rec.Code != 404 {
		t.Fatalf("someone else's face: %d", rec.Code)
	}
}

func TestPeopleBirthdayAndAge(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	e.withFaces(nil)
	e.json(e.do(e.tok, "PUT", "/me/photos/v1/faces", map[string]any{"enabled": true}), 200, nil)
	a := e.asset(e.user, assetOpts{}) // taken 2019-07-04
	p := e.person(e.user, "Тимур")
	f := e.face(a, p, 0.2)
	path := "/me/photos/v1/people/" + db.UUIDString(p)

	var got struct {
		BirthDate *string `json:"birthDate"`
	}
	e.json(e.do(e.tok, "PATCH", path, map[string]any{"birthDate": "2017-07-05"}), 200, &got)
	if got.BirthDate == nil || *got.BirthDate != "2017-07-05" {
		t.Fatalf("birthDate %v", got.BirthDate)
	}
	var card struct {
		People []struct {
			ID     string `json:"id"`
			FaceID string `json:"faceId"`
			Age    *int   `json:"age"`
		} `json:"people"`
	}
	e.json(e.do(e.tok, "GET", "/me/photos/v1/assets/"+db.UUIDString(a.ID), nil), 200, &card)
	// 2019-07-04 is the day before the second birthday: still 1.
	if len(card.People) != 1 || card.People[0].Age == nil || *card.People[0].Age != 1 || card.People[0].FaceID != db.UUIDString(f) {
		t.Fatalf("card people %+v", card.People)
	}
	// Born after the photo was taken: no age.
	e.json(e.do(e.tok, "PATCH", path, map[string]any{"birthDate": "2020-01-01"}), 200, nil)
	card.People = nil
	e.json(e.do(e.tok, "GET", "/me/photos/v1/assets/"+db.UUIDString(a.ID), nil), 200, &card)
	if len(card.People) != 1 || card.People[0].Age != nil {
		t.Fatalf("age before birth: %+v", card.People)
	}
	// null clears it; anything but a date is refused.
	e.json(e.do(e.tok, "PATCH", path, map[string]any{"birthDate": nil}), 200, &got)
	if got.BirthDate != nil {
		t.Fatalf("not cleared: %v", *got.BirthDate)
	}
	if rec := e.do(e.tok, "PATCH", path, map[string]any{"birthDate": "05.07.2017"}); rec.Code != 400 {
		t.Fatalf("bad date: %d", rec.Code)
	}
	if rec := e.do(e.tok, "PATCH", path, map[string]any{"birthDate": "2999-01-01"}); rec.Code != 400 {
		t.Fatalf("future date: %d", rec.Code)
	}
}

// unassignedFace puts a face of nobody on card a.
func (e *photosEnv) unassignedFace(a db.PhotoAsset) pgtype.UUID {
	return e.face(a, pgtype.UUID{}, 0.2)
}

func TestPeopleAssignFace(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	e.withFaces(nil)
	e.json(e.do(e.tok, "PUT", "/me/photos/v1/faces", map[string]any{"enabled": true}), 200, nil)
	a := e.asset(e.user, assetOpts{})
	mike := e.person(e.user, "Mike")
	e.face(a, mike, 0.2)
	lone := e.unassignedFace(a)
	other := e.unassignedFace(a)

	type card struct {
		People []struct {
			ID     string `json:"id"`
			FaceID string `json:"faceId"`
		} `json:"people"`
		UnknownFaces []struct {
			FaceID string     `json:"faceId"`
			Face   [4]float32 `json:"face"`
		} `json:"unknownFaces"`
	}
	var c card
	e.json(e.do(e.tok, "GET", "/me/photos/v1/assets/"+db.UUIDString(a.ID), nil), 200, &c)
	if len(c.People) != 1 || len(c.UnknownFaces) != 2 {
		t.Fatalf("card %+v", c)
	}

	path := func(f pgtype.UUID) string { return "/me/photos/v1/people/faces/" + db.UUIDString(f) + "/assign" }
	// To an existing person.
	var p personResp
	e.json(e.do(e.tok, "POST", path(lone), map[string]any{"personId": db.UUIDString(mike)}), 200, &p)
	if p.ID != db.UUIDString(mike) {
		t.Fatalf("assigned to %+v", p)
	}
	// To a new person, by name.
	e.json(e.do(e.tok, "POST", path(other), map[string]any{"name": "  Аня "}), 200, &p)
	if p.Name != "Аня" || p.Count != 1 {
		t.Fatalf("new person %+v", p)
	}
	c = card{}
	e.json(e.do(e.tok, "GET", "/me/photos/v1/assets/"+db.UUIDString(a.ID), nil), 200, &c)
	if len(c.People) != 2 || len(c.UnknownFaces) != 0 {
		t.Fatalf("after assigning: %+v", c)
	}
	// A face that has a person can be given to another (a wrong match).
	e.json(e.do(e.tok, "POST", path(lone), map[string]any{"personId": p.ID}), 200, nil)

	// Not mine, a person not mine, a bad body.
	if rec := e.do(e.tok2, "POST", path(lone), map[string]any{"name": "x"}); rec.Code != 404 {
		t.Fatalf("someone else's face: %d", rec.Code)
	}
	theirs := e.person(e.user2, "")
	if rec := e.do(e.tok, "POST", path(lone), map[string]any{"personId": db.UUIDString(theirs)}); rec.Code != 404 {
		t.Fatalf("someone else's person: %d", rec.Code)
	}
	for _, body := range []map[string]any{{}, {"name": "  "}, {"name": "x", "personId": db.UUIDString(mike)}} {
		if rec := e.do(e.tok, "POST", path(lone), body); rec.Code != 400 {
			t.Fatalf("body %v: %d", body, rec.Code)
		}
	}
}

// A wrong match fixed from the person's grid: their faces on the picked cards go to
// somebody else, to a new person, or nowhere (and the next pass leaves them alone).
func TestPeopleMoveFaces(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	e.withFaces(nil)
	e.json(e.do(e.tok, "PUT", "/me/photos/v1/faces", map[string]any{"enabled": true}), 200, nil)
	a1, a2, a3, a4 := e.asset(e.user, assetOpts{}), e.asset(e.user, assetOpts{}), e.asset(e.user, assetOpts{}), e.asset(e.user, assetOpts{})
	mike, anya := e.person(e.user, "Mike"), e.person(e.user, "Аня")
	e.face(a1, mike, 0.2)
	e.face(a2, mike, 0.2)
	f3 := e.face(a3, mike, 0.2)
	e.face(a4, anya, 0.2)
	e.face(a1, anya, 0.1) // Аня is on a1 too: she stays where she is
	path := "/me/photos/v1/people/" + db.UUIDString(mike) + "/faces/move"
	ids := func(as ...db.PhotoAsset) []string {
		out := []string{}
		for _, a := range as {
			out = append(out, db.UUIDString(a.ID))
		}
		return out
	}
	count := func(id pgtype.UUID) int {
		var p personResp
		e.json(e.do(e.tok, "GET", "/me/photos/v1/people/"+db.UUIDString(id), nil), 200, &p)
		return p.Count
	}
	var res struct {
		Moved  int         `json:"moved"`
		Person *personResp `json:"person"`
	}

	e.json(e.do(e.tok, "POST", path, map[string]any{"assetIds": ids(a1, a2), "personId": db.UUIDString(anya)}), 200, &res)
	if res.Moved != 2 || res.Person == nil || res.Person.ID != db.UUIDString(anya) {
		t.Fatalf("move to Аня: %+v", res)
	}
	if count(mike) != 1 || count(anya) != 3 {
		t.Fatalf("after move: mike %d, anya %d", count(mike), count(anya))
	}

	// To a new person by name.
	e.json(e.do(e.tok, "POST", "/me/photos/v1/people/"+db.UUIDString(anya)+"/faces/move",
		map[string]any{"assetIds": ids(a2), "name": "Петя"}), 200, &res)
	if res.Moved != 1 || res.Person == nil || res.Person.Name != "Петя" || res.Person.Count != 1 {
		t.Fatalf("move to a new person: %+v", res)
	}

	// Nowhere: the face becomes «Кто это?» on its card and is kept out of the passes.
	e.json(e.do(e.tok, "POST", path, map[string]any{"assetIds": ids(a3), "remove": true}), 200, &res)
	if res.Moved != 1 || res.Person != nil {
		t.Fatalf("remove: %+v", res)
	}
	var c struct {
		UnknownFaces []struct {
			FaceID string `json:"faceId"`
		} `json:"unknownFaces"`
	}
	e.json(e.do(e.tok, "GET", "/me/photos/v1/assets/"+db.UUIDString(a3.ID), nil), 200, &c)
	if len(c.UnknownFaces) != 1 || c.UnknownFaces[0].FaceID != db.UUIDString(f3) {
		t.Fatalf("removed face on its card: %+v", c)
	}
	vecs, err := e.q.ListUserFaceVectors(e.ctx, db.ListUserFaceVectorsParams{UserID: e.user.ID, Model: "auraface"})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vecs {
		if v.ID == f3 {
			t.Fatal("a face the user took off a person goes back to the passes")
		}
	}

	// Cards without the person's face, somebody else's cards: nothing moves.
	e.json(e.do(e.tok, "POST", path, map[string]any{"assetIds": ids(a4), "remove": true}), 200, &res)
	if res.Moved != 0 {
		t.Fatalf("moved from a card without the person: %d", res.Moved)
	}
	theirs := e.asset(e.user2, assetOpts{})
	e.json(e.do(e.tok, "POST", "/me/photos/v1/people/"+db.UUIDString(anya)+"/faces/move",
		map[string]any{"assetIds": ids(theirs), "remove": true}), 200, &res)
	if res.Moved != 0 {
		t.Fatalf("moved on somebody else's card: %d", res.Moved)
	}

	// Refused: to itself, both or neither of target and remove, no cards, a bad id.
	for _, body := range []map[string]any{
		{"assetIds": ids(a1), "personId": db.UUIDString(mike)},
		{"assetIds": ids(a1)},
		{"assetIds": ids(a1), "name": "x", "remove": true},
		{"assetIds": []string{}, "remove": true},
		{"assetIds": []string{"nope"}, "remove": true},
	} {
		if rec := e.do(e.tok, "POST", path, body); rec.Code != 400 {
			t.Fatalf("body %v: %d", body, rec.Code)
		}
	}
	if rec := e.do(e.tok2, "POST", path, map[string]any{"assetIds": ids(a1), "remove": true}); rec.Code != 404 {
		t.Fatalf("somebody else's person: %d", rec.Code)
	}
}

// One face taken off its person from the info panel.
func TestPeopleRemoveFace(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	e.withFaces(nil)
	e.json(e.do(e.tok, "PUT", "/me/photos/v1/faces", map[string]any{"enabled": true}), 200, nil)
	a := e.asset(e.user, assetOpts{})
	mike := e.person(e.user, "Mike")
	f := e.face(a, mike, 0.2)
	path := "/me/photos/v1/people/faces/" + db.UUIDString(f) + "/assign"
	e.json(e.do(e.tok, "POST", path, map[string]any{"remove": true}), 200, nil)
	var c struct {
		People       []struct{ ID string } `json:"people"`
		UnknownFaces []struct {
			FaceID string `json:"faceId"`
		} `json:"unknownFaces"`
	}
	e.json(e.do(e.tok, "GET", "/me/photos/v1/assets/"+db.UUIDString(a.ID), nil), 200, &c)
	if len(c.People) != 0 || len(c.UnknownFaces) != 1 {
		t.Fatalf("after remove: %+v", c)
	}
	if rec := e.do(e.tok, "POST", path, map[string]any{"remove": true, "name": "x"}); rec.Code != 400 {
		t.Fatalf("remove and a name: %d", rec.Code)
	}
}
