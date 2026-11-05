package db_test

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

func TestPhotoFaceQueries(t *testing.T) {
	ctx, q, u, _ := photosTestDB(t)
	if _, err := q.UpsertPhotoSettings(ctx, db.UpsertPhotoSettingsParams{UserID: u.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	mk := func(name string, version int64) db.PhotoAsset {
		n, err := q.CreateNode(ctx, db.CreateNodeParams{UserID: u.ID, Name: name,
			DiskPath: pgtype.Text{String: "photos/" + name, Valid: true}})
		if err != nil {
			t.Fatal(err)
		}
		a, err := q.UpsertPhotoAsset(ctx, db.UpsertPhotoAssetParams{UserID: u.ID, NodeID: n.ID, Kind: "photo",
			DateSource: "exif", TakenAt: pgtype.Timestamptz{Time: when, Valid: true},
			TakenLocal: pgtype.Timestamp{Time: when, Valid: true}, NodeVersion: pgtype.Int8{Int64: version, Valid: true}})
		if err != nil {
			t.Fatal(err)
		}
		if err := q.SetThumbState(ctx, db.SetThumbStateParams{ID: a.ID, ThumbState: "ready"}); err != nil {
			t.Fatal(err)
		}
		return a
	}
	a1, a2 := mk("a.jpg", 1), mk("b.jpg", 1)
	// A card whose thumbnail failed is never looked at: it must not stay "to do".
	broken := mk("broken.jpg", 1)
	if err := q.SetThumbState(ctx, db.SetThumbStateParams{ID: broken.ID, ThumbState: "failed"}); err != nil {
		t.Fatal(err)
	}
	if p, err := q.FaceProgress(ctx, u.ID); err != nil || p.Total != 2 || p.Pending != 2 {
		t.Fatalf("progress %+v %v", p, err)
	}

	// Off until the user turns people on.
	if rows, _ := q.ClaimPendingFaces(ctx, db.ClaimPendingFacesParams{MaxRows: 10}); len(rows) != 0 {
		t.Fatalf("claimed %d with faces off", len(rows))
	}
	if n, err := q.SetFacesEnabled(ctx, db.SetFacesEnabledParams{UserID: u.ID, FacesEnabled: true}); err != nil || n != 1 {
		t.Fatalf("SetFacesEnabled: %d %v", n, err)
	}
	rows, err := q.ClaimPendingFaces(ctx, db.ClaimPendingFacesParams{MaxRows: 10, Exclude: []pgtype.UUID{a2.ID}})
	if err != nil || len(rows) != 1 || rows[0].ID != a1.ID {
		t.Fatalf("claim: %v %+v", err, rows)
	}

	p, err := q.CreatePhotoPerson(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []db.PhotoAsset{a1, a2} {
		if _, err := q.InsertPhotoFace(ctx, db.InsertPhotoFaceParams{UserID: u.ID, AssetID: a.ID, PersonID: p,
			X1: 0.1, Y1: 0.1, X2: 0.3, Y2: 0.4, Score: 0.9, Embedding: make([]byte, 2048), Model: "auraface"}); err != nil {
			t.Fatal(err)
		}
		if err := q.SetFaceState(ctx, db.SetFaceStateParams{ID: a.ID, FaceState: pgtype.Text{String: "done", Valid: true}}); err != nil {
			t.Fatal(err)
		}
	}
	people, err := q.ListPhotoPeople(ctx, db.ListPhotoPeopleParams{UserID: u.ID, Hidden: false})
	if err != nil || len(people) != 1 || people[0].Count != 2 || !people[0].CoverFaceID.Valid {
		t.Fatalf("people: %v %+v", err, people)
	}

	// The person filter of the feed.
	month := pgtype.Timestamp{Time: time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC), Valid: true}
	feed, err := q.ListPhotoAssetsInMonth(ctx, db.ListPhotoAssetsInMonthParams{UserIds: []pgtype.UUID{u.ID},
		ViewerID: u.ID, Month: month, PersonID: p})
	if err != nil || len(feed) != 2 {
		t.Fatalf("feed by person: %v %d", err, len(feed))
	}

	// Hidden cards leave the person's count; with none visible the person is not listed
	// and has no cover.
	if err := q.SetPhotoFlags(ctx, db.SetPhotoFlagsParams{UserID: u.ID, Ids: []pgtype.UUID{a1.ID, a2.ID},
		Hidden: pgtype.Bool{Bool: true, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if people, _ := q.ListPhotoPeople(ctx, db.ListPhotoPeopleParams{UserID: u.ID}); len(people) != 0 {
		t.Fatalf("person of hidden cards listed: %+v", people)
	}
	got, err := q.GetPhotoPerson(ctx, db.GetPhotoPersonParams{ID: p, UserID: u.ID})
	if err != nil || got.Count != 0 || got.CoverFaceID.Valid {
		t.Fatalf("person of hidden cards: %v %+v", err, got)
	}

	// New content puts the card back in the queue.
	n1, _ := q.GetNodeAnyState(ctx, a1.NodeID)
	if _, err := q.UpsertPhotoAsset(ctx, db.UpsertPhotoAssetParams{UserID: u.ID, NodeID: n1.ID, Kind: "photo",
		DateSource: "exif", TakenAt: pgtype.Timestamptz{Time: when, Valid: true},
		TakenLocal: pgtype.Timestamp{Time: when, Valid: true}, NodeVersion: pgtype.Int8{Int64: 2, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	after, _ := q.GetPhotoAssetByNode(ctx, n1.ID)
	if after.FaceState.Valid {
		t.Fatalf("face_state kept over new content: %v", after.FaceState)
	}

	// Empty persons go, unless named or hidden.
	named, _ := q.CreatePhotoPerson(ctx, u.ID)
	empty, _ := q.CreatePhotoPerson(ctx, u.ID)
	if _, err := q.UpdatePhotoPerson(ctx, db.UpdatePhotoPersonParams{ID: named, UserID: u.ID,
		Name: pgtype.Text{String: "Аня", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if err := q.DeleteEmptyPhotoPersons(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	left, _ := q.ListPhotoPersonsByIDs(ctx, db.ListPhotoPersonsByIDsParams{UserID: u.ID, Ids: []pgtype.UUID{named, empty, p}})
	if len(left) != 2 {
		t.Fatalf("after cleanup: %+v", left)
	}
	for _, l := range left {
		if l.ID == empty {
			t.Fatal("empty unnamed person kept")
		}
	}
}
