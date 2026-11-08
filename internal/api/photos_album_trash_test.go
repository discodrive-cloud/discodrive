package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/photos"
)

// A card whose file is in the trash is seen by nobody but its owner. The card's own
// trash flag is kept in sync by the indexer, which does not run while the owner's
// gallery is off — so album and link visibility must look at the file itself.
func TestAlbumHidesCardsOfTrashedFiles(t *testing.T) {
	e := newPhotosEnv(t, false, 0)
	e.fakeMediaTools()
	keep := e.asset(e.user, assetOpts{})
	gone := e.asset(e.user, assetOpts{})
	for _, a := range []db.PhotoAsset{keep, gone} {
		// a thumbnail already on disk, so the link can serve it without the tools
		p := photos.AssetDerivPath(e.root, a, "thumb")
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte("jpg"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := e.pool.Exec(e.ctx, "UPDATE photo_assets SET thumb_state='ready' WHERE id=$1", a.ID); err != nil {
			t.Fatal(err)
		}
	}
	al, err := e.q.CreatePhotoAlbum(e.ctx, db.CreatePhotoAlbumParams{OwnerID: e.user.ID, Name: "Trip"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.q.AddPhotoAlbumItems(e.ctx, db.AddPhotoAlbumItemsParams{AlbumID: al.ID, AssetIds: []pgtype.UUID{keep.ID, gone.ID}, AddedBy: e.user.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.q.CreatePhotoAlbumLink(e.ctx, db.CreatePhotoAlbumLinkParams{AlbumID: al.ID, Token: "tok123"}); err != nil {
		t.Fatal(err)
	}
	// the file goes to the trash while the indexer is not looking: the card is untouched
	if err := e.q.SoftDeleteNode(e.ctx, gone.NodeID); err != nil {
		t.Fatal(err)
	}

	var items struct{ Items []rowResp }
	e.json(e.do("", "GET", "/public/photos/v1/tok123/items", nil), 200, &items)
	if len(items.Items) != 1 || items.Items[0].ID != db.UUIDString(keep.ID) {
		t.Fatalf("public items %+v, want only the card whose file is live", items.Items)
	}
	if c := e.do("", "GET", "/public/photos/v1/tok123/assets/"+db.UUIDString(gone.ID)+"/thumb", nil).Code; c != 404 {
		t.Fatalf("thumb of a trashed file through the link: %d, want 404", c)
	}
}
