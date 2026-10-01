package api

import (
	"context"
	"discodrive/internal/db"
	"discodrive/internal/storage"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestRegressionPurgeThenScopeRename(t *testing.T) {
	for _, ancestor := range []bool{false, true} {
		t.Run(fmt.Sprintf("ancestor=%v", ancestor), func(t *testing.T) {
			ctx := context.Background()
			pool, q, svc := bootstrapPairingDB(t)
			s := &Server{auth: svc, q: q, files: storage.NewFileService(pool, storage.NewLocalDisk(t.TempDir()))}
			tok, user, err := svc.Register(ctx, "review@x.test", "password12")
			if err != nil {
				t.Fatal(err)
			}
			uid := db.UUIDString(user.ID)
			var parentID *string
			if ancestor {
				p, err := s.files.CreateFolder(ctx, uid, nil, "parent")
				if err != nil {
					t.Fatal(err)
				}
				id := db.UUIDString(p.ID)
				parentID = &id
			}
			sync, err := s.files.CreateFolder(ctx, uid, parentID, "sync")
			if err != nil {
				t.Fatal(err)
			}
			_, err = q.UpsertSyncSettings(ctx, db.UpsertSyncSettingsParams{UserID: user.ID, Enabled: true, FolderNodeID: sync.ID})
			if err != nil {
				t.Fatal(err)
			}
			sid := db.UUIDString(sync.ID)
			f, err := s.files.Push(ctx, uid, &sid, "f.txt", nil, "i", strings.NewReader("hello"))
			if err != nil {
				t.Fatal(err)
			}
			fid := db.UUIDString(f.Node.ID)
			h := svc.Middleware(http.HandlerFunc(s.handleSyncChanges))
			initial := changesReqFeatures(t, h, "/sync/changes?since=0", tok, true, "delete-by-id")
			cursor := int64(initial["cursor"].(float64))
			if err = s.files.Delete(ctx, uid, fid); err != nil {
				t.Fatal(err)
			}
			if err = s.files.Purge(ctx, uid, fid); err != nil {
				t.Fatal(err)
			}
			target := sid
			if ancestor {
				target = *parentID
			}
			if _, err = s.files.Rename(ctx, uid, target, "renamed"); err != nil {
				t.Fatal(err)
			}
			after := changesReqFeatures(t, h, fmt.Sprintf("/sync/changes?since=%d", cursor), tok, true, "delete-by-id")
			if after["scope_epoch"].(float64) > initial["scope_epoch"].(float64) {
				reset := changesReqFeatures(t, h, "/sync/changes?since=0", tok, true, "delete-by-id")
				for _, v := range reset["changes"].([]any) {
					if v.(map[string]any)["node_id"] == fid {
						t.Fatal("purged file resurrected after reset")
					}
				}
				return
			}
			for _, v := range after["changes"].([]any) {
				if v.(map[string]any)["node_id"] == fid {
					return
				}
			}
			t.Fatalf("purged f.txt never reaches scoped client after scope rename: initial=%v after=%v", initial, after)
		})
	}

}
