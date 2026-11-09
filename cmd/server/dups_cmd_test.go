package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

func TestDupsCmd_PrintsGroups(t *testing.T) {
	q, uid := rescanCmdDB(t)
	ctx := context.Background()
	when := time.Date(2020, 1, 1, 10, 0, 0, 0, time.UTC)
	for _, name := range []string{"IMG_1170.jpg", "IMG_1170 (1).jpg"} {
		n, err := q.CreateNode(ctx, db.CreateNodeParams{UserID: uid, Name: name,
			Size: pgtype.Int8{Int64: 1000, Valid: true}, DiskPath: pgtype.Text{String: "p/" + name, Valid: true}})
		if err != nil {
			t.Fatal(err)
		}
		a, err := q.UpsertPhotoAsset(ctx, db.UpsertPhotoAssetParams{UserID: uid, NodeID: n.ID, Kind: "photo", DateSource: "exif",
			TakenAt: pgtype.Timestamptz{Time: when, Valid: true}, TakenLocal: pgtype.Timestamp{Time: when, Valid: true}})
		if err != nil {
			t.Fatal(err)
		}
		if err := q.SetThumbState(ctx, db.SetThumbStateParams{ID: a.ID, ThumbState: "ready"}); err != nil {
			t.Fatal(err)
		}
		if err := q.SetThumbReady(ctx, db.SetThumbReadyParams{ID: a.ID, Phash: pgtype.Int8{Int64: 12345, Valid: true}}); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if code := runDups(ctx, q, []string{"--user", "a@x"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	s := out.String()
	for _, want := range []string{"IMG_1170.jpg", "IMG_1170 (1).jpg", "1 groups"} {
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %q:\n%s", want, s)
		}
	}
}

func TestDupsCmd_Usage(t *testing.T) {
	var out bytes.Buffer
	if code := runDups(context.Background(), nil, nil, &out); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}
