// Package worker runs discodrive background jobs without Redis: periodic
// tickers for version/trash GC and rescan, plus an fsnotify watcher that triggers
// a rescan almost instantly when files change on disk outside the service.
package worker

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"discodrive/internal/bookmarks"
	"discodrive/internal/db"
	"discodrive/internal/ebook"
	"discodrive/internal/music"
	"discodrive/internal/notify"
	"discodrive/internal/podcast"
	"discodrive/internal/quota"
	"discodrive/internal/saved"
	"discodrive/internal/storage"
)

// Config holds intervals and parameters for background jobs.
type Config struct {
	RescanInterval    time.Duration
	TrimInterval      time.Duration
	TrashInterval     time.Duration
	TrashRetention    time.Duration
	QuotaInterval     time.Duration
	PairingGCInterval time.Duration
	VersionKeep       int
	Debounce          time.Duration

	// StorageTotal is the server-wide cap (STORAGE_TOTAL_GB) in bytes, 0 = unlimited.
	// The storage-alert job needs it to say how much of the *allotted* space is left,
	// which is not the same question as how much of the disk is left.
	StorageTotal int64

	PodcastRefreshInterval time.Duration
	PodcastKeepPerChannel  int

	SavedScanInterval    time.Duration
	SavedCleanupInterval time.Duration

	BookmarkEnrichInterval time.Duration
	BookmarkGCInterval     time.Duration
}

// Default returns sensible defaults; keep/trashDays/rescanSeconds come from config.
func Default(versionKeep, trashDays, rescanSeconds int) Config {
	return Config{
		RescanInterval:    time.Duration(rescanSeconds) * time.Second,
		TrimInterval:      60 * time.Second,
		TrashInterval:     time.Hour,
		TrashRetention:    time.Duration(trashDays) * 24 * time.Hour,
		QuotaInterval:     15 * time.Minute,
		PairingGCInterval: 5 * time.Minute,
		VersionKeep:       versionKeep,
		Debounce:          500 * time.Millisecond,

		PodcastRefreshInterval: 6 * time.Hour,
		PodcastKeepPerChannel:  5,

		SavedScanInterval:    30 * time.Second,
		SavedCleanupInterval: 10 * time.Minute,

		BookmarkEnrichInterval: time.Minute,
		BookmarkGCInterval:     24 * time.Hour,
	}
}

// liveSnapshotIdle is how long a node must have been untouched before the
// prune-live-snapshots job will look at it — long enough that no push can still be
// mid-write on it.
const liveSnapshotIdle = 10 * time.Minute

type Worker struct {
	fs       *storage.FileService
	root     string
	q        *db.Queries
	notify   *notify.Notifier
	cfg      Config
	idx      *music.Indexer     // nil if music indexing is not configured
	ebookIdx *ebook.Indexer     // nil if ebook indexing is not configured
	saved    *saved.Service     // nil if saved items are not configured
	bm       *bookmarks.Service // nil if bookmark sync is not configured
	// disk is storage.DiskUsage, replaced in tests: the free space of the machine the
	// tests run on is not something a test can assert against.
	disk func(path string) (total, free uint64, err error)
}

func New(fs *storage.FileService, root string, q *db.Queries, notifier *notify.Notifier, cfg Config, idx *music.Indexer, ebookIdx *ebook.Indexer, savedSvc *saved.Service, bookmarksSvc *bookmarks.Service) *Worker {
	return &Worker{fs: fs, root: root, q: q, notify: notifier, cfg: cfg, idx: idx, ebookIdx: ebookIdx, saved: savedSvc, bm: bookmarksSvc, disk: storage.DiskUsage}
}

// Run starts all background jobs and blocks until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	go w.tick(ctx, w.cfg.RescanInterval, "rescan", func(ctx context.Context) error {
		return w.fs.Rescan(ctx)
	})
	go w.tick(ctx, w.cfg.TrimInterval, "trim-versions", func(ctx context.Context) error {
		return w.fs.TrimVersions(ctx, w.cfg.VersionKeep)
	})
	// Reclaims the space taken by snapshots of content that is still live — the copies
	// left behind by the older versioning scheme. Once they are gone this is a no-op.
	go w.tick(ctx, w.cfg.TrashInterval, "prune-live-snapshots", func(ctx context.Context) error {
		return w.fs.PruneLiveSnapshots(ctx, liveSnapshotIdle)
	})
	go w.tick(ctx, w.cfg.TrashInterval, "trash-gc", func(ctx context.Context) error {
		return w.fs.TrashGC(ctx, w.cfg.TrashRetention)
	})
	go w.tick(ctx, w.cfg.QuotaInterval, "quota-notify", w.quotaNotify)
	go w.tick(ctx, w.cfg.QuotaInterval, "storage-alert", w.storageAlert)
	go w.tick(ctx, w.cfg.PairingGCInterval, "pairing-gc", func(ctx context.Context) error {
		if err := w.q.DeleteExpiredPairings(ctx); err != nil {
			return err
		}
		return w.q.DeleteExpiredAuthChallenges(ctx)
	})
	go w.tick(ctx, w.cfg.PodcastRefreshInterval, "podcast-refresh", w.podcastRefresh)
	if w.idx != nil {
		go w.tick(ctx, w.cfg.RescanInterval, "music-scan", w.musicScan)
	}
	if w.ebookIdx != nil {
		go w.tick(ctx, w.cfg.RescanInterval, "ebook-scan", w.ebookScan)
	}
	if w.saved != nil {
		go w.tick(ctx, w.cfg.SavedScanInterval, "saved-scan", w.saved.ProcessPending)
		go w.tick(ctx, w.cfg.SavedCleanupInterval, "saved-cleanup", w.saved.CleanupDownloads)
	}
	if w.bm != nil {
		go w.tick(ctx, w.cfg.BookmarkEnrichInterval, "bookmark-enrich", w.bm.EnrichPending)
		go w.tick(ctx, w.cfg.BookmarkGCInterval, "bookmark-gc", w.bm.GCTombstones)
	}
	w.watch(ctx)
}

// musicScan indexes audio files for all users who have music enabled.
func (w *Worker) musicScan(ctx context.Context) error {
	users, err := w.q.EnabledMusicUsers(ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		if !u.FolderNodeID.Valid {
			continue
		}
		userID := db.UUIDString(u.UserID)
		folderID := db.UUIDString(u.FolderNodeID)
		n, err := w.idx.ScanFolder(ctx, userID, folderID)
		if err != nil {
			log.Printf("discodrive: music-scan user=%s: %v", userID, err)
			continue
		}
		if n > 0 {
			log.Printf("discodrive: music-scan user=%s: indexed %d file(s)", userID, n)
		}
	}
	return nil
}

// ebookScan indexes e-book files for all users who have ebook indexing enabled.
func (w *Worker) ebookScan(ctx context.Context) error {
	users, err := w.q.EnabledEbookUsers(ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		if !u.FolderNodeID.Valid {
			continue
		}
		userID := db.UUIDString(u.UserID)
		folderID := db.UUIDString(u.FolderNodeID)
		n, err := w.ebookIdx.ScanFolder(ctx, userID, folderID)
		if err != nil {
			log.Printf("discodrive: ebook-scan user=%s: %v", userID, err)
			continue
		}
		if n > 0 {
			log.Printf("discodrive: ebook-scan user=%s: indexed %d file(s)", userID, n)
		}
	}
	return nil
}

// podcastRefresh re-fetches every subscribed podcast feed, upserts new episodes,
// refreshes channel metadata, and prunes downloaded episodes beyond the keep limit.
func (w *Worker) podcastRefresh(ctx context.Context) error {
	channels, err := w.q.ListAllPodcastChannels(ctx)
	if err != nil {
		return err
	}
	for _, ch := range channels {
		if err := podcast.RefreshChannel(ctx, w.q, w.root, ch); err != nil {
			log.Printf("discodrive: podcast-refresh channel=%s: %v", db.UUIDString(ch.ID), err)
			continue
		}

		// Prune downloaded episodes beyond the keep limit (rows are newest-first).
		rows, _ := w.q.ListCompletedEpisodesByChannelDesc(ctx, ch.ID)
		start := pruneStart(len(rows), w.cfg.PodcastKeepPerChannel)
		for _, row := range rows[start:] {
			if row.DiskPath.Valid {
				_ = storage.NewLocalDisk(w.root).Remove(row.DiskPath.String)
			}
			if err := w.q.ClearEpisodeDownload(ctx, db.ClearEpisodeDownloadParams{ID: row.ID, UserID: ch.UserID}); err != nil {
				log.Printf("discodrive: podcast-refresh prune channel=%s: %v", db.UUIDString(ch.ID), err)
			}
		}
	}
	return nil
}

// pruneStart returns the start index of the prune tail for completed episodes
// ordered newest-first: rows[start:] should be pruned, rows[:start] kept.
func pruneStart(total, keep int) int {
	if keep < 0 {
		keep = 0
	}
	if total <= keep {
		return total
	}
	return keep
}

func (w *Worker) tick(ctx context.Context, every time.Duration, name string, job func(context.Context) error) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := job(ctx); err != nil {
				log.Printf("discodrive: job %s: %v", name, err)
			}
		}
	}
}

// watch monitors the data tree and debounces rescan on disk changes.
func (w *Worker) watch(ctx context.Context) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("discodrive: fsnotify unavailable: %v", err)
		<-ctx.Done()
		return
	}
	defer watcher.Close()

	if err := os.MkdirAll(w.root, 0o755); err != nil {
		log.Printf("discodrive: creating data root: %v", err)
	}
	w.addRecursive(watcher, w.root)

	debounce := time.NewTimer(time.Hour)
	debounce.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-watcher.Events:
			if !ok {
				return
			}
			// also watch newly created directories
			if e.Op&fsnotify.Create != 0 {
				if fi, err := os.Lstat(e.Name); err == nil && fi.IsDir() {
					w.addRecursive(watcher, e.Name)
				}
			}
			debounce.Reset(w.cfg.Debounce)
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Printf("discodrive: fsnotify: %v", err)
		case <-debounce.C:
			if err := w.fs.Rescan(ctx); err != nil {
				log.Printf("discodrive: rescan (fsnotify): %v", err)
			}
			if w.idx != nil {
				if err := w.musicScan(ctx); err != nil {
					log.Printf("discodrive: music-scan (fsnotify): %v", err)
				}
			}
			if w.ebookIdx != nil {
				if err := w.ebookScan(ctx); err != nil {
					log.Printf("discodrive: ebook-scan (fsnotify): %v", err)
				}
			}
		}
	}
}

// quotaNotify sends a notification to users who have crossed 90% of their quota (once),
// and clears the flag for those who have dropped back below the threshold.
func (w *Worker) quotaNotify(ctx context.Context) error {
	// users.storage_used is a cache: nothing on the write path maintains it, so without
	// this refresh it stays at 0 and the notification below never fires. The quota check
	// itself never reads the column — it sums the live totals — so a cache that is up to
	// one tick stale only delays a warning, it can never let a write through.
	if err := w.q.RefreshStorageUsed(ctx); err != nil {
		return err
	}
	rows, err := w.q.ListQuotaCandidates(ctx)
	if err != nil {
		return err
	}
	for _, u := range rows {
		limit := u.StorageQuota.Int64
		percent := 0
		if limit > 0 {
			percent = int(u.StorageUsed * 100 / limit)
		}
		// Used/Quota go straight into the email body, so they are formatted here —
		// "107374182400 of 214748364800" is not something to send a person.
		w.notify.Emit(ctx, db.UUIDString(u.ID), "quota.near_limit", map[string]any{
			"Percent": percent,
			"Used":    quota.HumanBytes(u.StorageUsed),
			"Quota":   quota.HumanBytes(limit),
		})
		if err := w.q.MarkQuotaNotified(ctx, u.ID); err != nil {
			return err
		}
	}
	return w.q.ClearQuotaNotified(ctx)
}

// alertLevelSetting is where storageAlert remembers the level it last mailed about.
// Without it a server parked below the threshold would mail every administrator on
// every tick; with it, one mail per step down, and silence until things get worse.
const alertLevelSetting = "storage.alert_level"

// storageAlert warns the administrators before the service runs out of room. It watches
// two limits at once — the physical disk and the server-wide cap (STORAGE_TOTAL_GB) —
// because either can fill up while the other still looks comfortable, and reports
// whichever is tighter.
func (w *Worker) storageAlert(ctx context.Context) error {
	diskTotal, diskFree, err := w.disk(w.root)
	if err != nil {
		return err
	}
	diskPercent := quota.FreePercent(int64(diskFree), int64(diskTotal))

	// -1 means "no cap configured", which quota.LevelFor reads as "not an alarm".
	limitPercent := -1
	var limitFree int64
	if w.cfg.StorageTotal > 0 {
		used, err := w.q.TotalStorageUsage(ctx)
		if err != nil {
			return err
		}
		limitFree = max(int64(0), w.cfg.StorageTotal-used)
		limitPercent = quota.FreePercent(limitFree, w.cfg.StorageTotal)
	}
	worst := diskPercent
	if limitPercent >= 0 && (worst < 0 || limitPercent < worst) {
		worst = limitPercent
	}

	was := w.storedAlertLevel(ctx)
	level := quota.SettledLevel(worst, was)
	if level == was {
		return nil
	}
	if err := w.q.UpsertSetting(ctx, db.UpsertSettingParams{
		Key: alertLevelSetting, Value: strconv.Itoa(int(level)),
	}); err != nil {
		return err
	}
	if level < was {
		return nil // space was freed; record the recovery, but do not mail about it
	}

	limitNote := "no cap"
	if limitPercent >= 0 {
		limitNote = strconv.Itoa(limitPercent) + "%"
	}
	log.Printf("discodrive: storage-alert: %d%% free (disk %d%%, limit %s) — notifying admins",
		worst, diskPercent, limitNote)
	admins, err := w.q.ListAdminIDs(ctx)
	if err != nil {
		return err
	}
	data := map[string]any{
		"Percent":      worst,
		"DiskFree":     quota.HumanBytes(int64(diskFree)),
		"DiskTotal":    quota.HumanBytes(int64(diskTotal)),
		"DiskPercent":  diskPercent,
		"HasLimit":     w.cfg.StorageTotal > 0,
		"LimitFree":    quota.HumanBytes(limitFree),
		"LimitTotal":   quota.HumanBytes(w.cfg.StorageTotal),
		"LimitPercent": limitPercent,
	}
	for _, id := range admins {
		w.notify.Emit(ctx, db.UUIDString(id), "storage.low_space", data)
	}
	return nil
}

// storedAlertLevel reads back the level storageAlert last acted on. An unreadable or
// malformed value counts as "no alert yet" — worst case the admins get one more mail.
func (w *Worker) storedAlertLevel(ctx context.Context) quota.Level {
	row, err := w.q.GetSetting(ctx, alertLevelSetting)
	if err != nil {
		return quota.LevelOK
	}
	n, err := strconv.Atoi(row.Value)
	if err != nil || n < int(quota.LevelOK) || n > int(quota.LevelCritical) {
		return quota.LevelOK
	}
	return quota.Level(n)
}

// addRecursive adds a directory and all its subdirectories to the watcher, skipping
// the internal .versions/.tmp directories (they are outside the tree mirror).
func (w *Worker) addRecursive(watcher *fsnotify.Watcher, dir string) {
	_ = filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err != nil || !e.IsDir() {
			return nil
		}
		if base := filepath.Base(p); strings.HasPrefix(base, ".versions") || strings.HasPrefix(base, ".tmp") {
			return filepath.SkipDir
		}
		_ = watcher.Add(p)
		return nil
	})
}
