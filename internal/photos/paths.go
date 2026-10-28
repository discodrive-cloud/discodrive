// Package photos is the «Фото» gallery: an index of the photos and videos under the
// user's source folders, kept in step with the change log by library.Tracker.
package photos

import (
	"hash/fnv"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"discodrive/internal/db"
)

// DerivPath is where a derivative of an asset lives: outside every user tree (rescan
// never walks it, quota never counts it), sharded by the first two characters of the
// asset id. kind is "thumb", "preview" (JPEG) or "h264" (MP4).
func DerivPath(storageRoot, userID, assetID, kind string) string {
	ext := ".jpg"
	if kind == "h264" || strings.HasPrefix(kind, "h264-") {
		ext = ".mp4"
	}
	return filepath.Join(derivUserDir(storageRoot, userID), shard(assetID), assetID+"-"+kind+ext)
}

func derivUserDir(storageRoot, userID string) string {
	return filepath.Join(storageRoot, ".photos", userID)
}

func shard(assetID string) string {
	if len(assetID) < 2 {
		return "00"
	}
	return assetID[:2]
}

// removeDerivatives deletes every derivative of the asset, of all kinds. Missing files
// are not an error.
func removeDerivatives(storageRoot, userID, assetID string) {
	if assetID == "" || strings.ContainsAny(assetID, `/\*?[`) {
		return
	}
	matches, _ := filepath.Glob(filepath.Join(derivUserDir(storageRoot, userID), shard(assetID), assetID+"-*"))
	for _, m := range matches {
		_ = os.Remove(m)
	}
}

// AssetDerivPath is the path of an asset's derivative. The viewer preview and the h264
// copy are made on demand and kept: their names carry a key of the content they were made
// from, so a file overwritten in place (an edit in another app, a re-upload) gets a new
// preview instead of the old one (the thumbnail goes back to pending on any change and is
// re-rendered in place).
func AssetDerivPath(storageRoot string, a db.PhotoAsset, kind string) string {
	if kind == "preview" || kind == "h264" {
		kind += "-" + contentKey(a)
	}
	return DerivPath(storageRoot, db.UUIDString(a.UserID), db.UUIDString(a.ID), kind)
}

// contentKey names the asset's content: a short hash of its content hash, or of the node
// version when the hash is unknown. A rename or a trash and restore keeps it (they keep
// the content hash), so a finished transcode survives them.
func contentKey(a db.PhotoAsset) string {
	h := fnv.New64a()
	if a.ContentHash.Valid && a.ContentHash.String != "" {
		h.Write([]byte("h:" + a.ContentHash.String))
	} else {
		h.Write([]byte("v:" + strconv.FormatInt(a.NodeVersion.Int64, 10)))
	}
	return strconv.FormatUint(h.Sum64(), 36)
}

// dropStaleDerivatives removes the asset's previews and h264 copies made from other
// content than its current one.
func dropStaleDerivatives(storageRoot string, a db.PhotoAsset) {
	uid, id := db.UUIDString(a.UserID), db.UUIDString(a.ID)
	keep := map[string]bool{
		AssetDerivPath(storageRoot, a, "preview"): true,
		AssetDerivPath(storageRoot, a, "h264"):    true,
	}
	dir := filepath.Join(derivUserDir(storageRoot, uid), shard(id))
	for _, kind := range []string{"preview", "h264"} {
		matches, _ := filepath.Glob(filepath.Join(dir, id+"-"+kind+"*"))
		for _, m := range matches {
			if !keep[m] {
				_ = os.Remove(m)
			}
		}
	}
}
