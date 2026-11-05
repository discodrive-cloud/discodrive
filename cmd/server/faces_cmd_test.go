package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/photos/faces"
)

func TestFacesCommand(t *testing.T) {
	q, uid := rescanCmdDB(t)
	ctx := context.Background()
	if _, err := q.UpsertPhotoSettings(ctx, db.UpsertPhotoSettingsParams{UserID: uid, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	when := time.Now()
	n, _ := q.CreateNode(ctx, db.CreateNodeParams{UserID: uid, Name: "a.jpg", DiskPath: pgtype.Text{String: "a.jpg", Valid: true}})
	a, err := q.UpsertPhotoAsset(ctx, db.UpsertPhotoAssetParams{UserID: uid, NodeID: n.ID, Kind: "photo", DateSource: "exif",
		TakenAt: pgtype.Timestamptz{Time: when, Valid: true}, TakenLocal: pgtype.Timestamp{Time: when, Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	_ = q.SetFaceState(ctx, db.SetFaceStateParams{ID: a.ID, FaceState: pgtype.Text{String: "done", Valid: true}, FaceModel: pgtype.Text{String: "auraface", Valid: true}})
	unnamed, _ := q.CreatePhotoPerson(ctx, uid)
	named, _ := q.CreatePhotoPerson(ctx, uid)
	_, _ = q.UpdatePhotoPerson(ctx, db.UpdatePhotoPersonParams{ID: named, UserID: uid, Name: pgtype.Text{String: "Аня", Valid: true}})
	vec := make([]byte, 2048) // the unit vector along the first axis: all faces alike
	vec[2], vec[3] = 0x80, 0x3f
	other := make([]byte, 2048) // the second axis: unlike every other face here
	other[6], other[7] = 0x80, 0x3f
	for p, e := range map[pgtype.UUID][]byte{unnamed: other, named: vec} {
		if _, err := q.InsertPhotoFace(ctx, db.InsertPhotoFaceParams{UserID: uid, AssetID: a.ID, PersonID: p,
			X1: 0, Y1: 0, X2: 0.1, Y2: 0.1, Score: 0.9, Embedding: e, Model: "auraface"}); err != nil {
			t.Fatal(err)
		}
	}
	params := faces.Params{MaxDistance: 0.5, MinFaces: 3}
	// A face the old model made, in an unnamed person: recluster leaves it alone (it is not
	// clustered until the server looks at the photo with the current model).
	oldModelPerson, _ := q.CreatePhotoPerson(ctx, uid)
	oldFace, err := q.InsertPhotoFace(ctx, db.InsertPhotoFaceParams{UserID: uid, AssetID: a.ID, PersonID: oldModelPerson,
		X1: 0.8, Y1: 0.8, X2: 0.9, Y2: 0.9, Score: 0.9, Embedding: vec, Model: "buffalo_l"})
	if err != nil {
		t.Fatal(err)
	}
	// A face an earlier pass left alone, right next to the named person's face: recluster
	// (after a looser threshold) must look at it again and give it the name.
	lone, err := q.InsertPhotoFace(ctx, db.InsertPhotoFaceParams{UserID: uid, AssetID: a.ID,
		X1: 0.5, Y1: 0.5, X2: 0.6, Y2: 0.6, Score: 0.9, Embedding: vec, Model: "auraface"})
	if err != nil {
		t.Fatal(err)
	}
	all, _ := q.ListAssetFaces(ctx, a.ID)
	var seen []pgtype.UUID
	for _, f := range all {
		seen = append(seen, f.ID) // every face went through a pass already
	}
	if err := q.MarkFacesClustered(ctx, seen); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if code := runFaces(ctx, q, []string{"recluster", "a@x"}, &out, "auraface", params); code != 0 {
		t.Fatalf("recluster exit %d: %s", code, out.String())
	}
	left, _ := q.ListPhotoPersonsByIDs(ctx, db.ListPhotoPersonsByIDsParams{UserID: uid, Ids: []pgtype.UUID{unnamed, named}})
	if len(left) != 1 || left[0].ID != named {
		t.Fatalf("after recluster: %+v", left)
	}
	fs, _ := q.ListAssetFaces(ctx, a.ID)
	for _, f := range fs {
		if f.ID == lone && f.PersonID != named {
			t.Fatalf("lone face not joined to the named person: %v", f.PersonID)
		}
		if f.ID == oldFace && f.PersonID != oldModelPerson {
			t.Fatalf("a face of the old model was released: %v", f.PersonID)
		}
	}
	// A model the user has no faces of: refused, nothing released.
	if code := runFaces(ctx, q, []string{"recluster", "a@x"}, &out, "nope", params); code != 1 {
		t.Fatalf("recluster with a model without faces: exit %d", code)
	}

	if code := runFaces(ctx, q, []string{"redetect", "a@x"}, &out, "auraface", params); code != 0 {
		t.Fatalf("redetect exit %d: %s", code, out.String())
	}
	if got, _ := q.GetPhotoAssetByNode(ctx, n.ID); got.FaceState.Valid {
		t.Fatalf("not queued again: %v", got.FaceState)
	}

	if code := runFaces(ctx, q, []string{"recluster", "nobody@x"}, &out, "auraface", params); code != 1 {
		t.Fatalf("unknown user: exit %d", code)
	}
	if code := runFaces(ctx, q, []string{"bogus"}, &out, "auraface", params); code != 2 {
		t.Fatalf("bad subcommand: exit %d", code)
	}
}
