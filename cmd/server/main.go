// Command server is the single discodrive binary (API + embedded UI).
// Stage 0, step 0.0: initially only started an HTTP server with /health.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"discodrive"
	"discodrive/internal/api"
	"discodrive/internal/auth"
	"discodrive/internal/bookmarks"
	caldavpkg "discodrive/internal/caldav"
	carddavpkg "discodrive/internal/carddav"
	"discodrive/internal/config"
	davpkg "discodrive/internal/dav"
	"discodrive/internal/db"
	"discodrive/internal/ebook"
	"discodrive/internal/httpsecurity"
	"discodrive/internal/kosync"
	"discodrive/internal/music"
	"discodrive/internal/notify"
	"discodrive/internal/opds"
	"discodrive/internal/quota"
	"discodrive/internal/saved"
	"discodrive/internal/secret"
	"discodrive/internal/storage"
	"discodrive/internal/subsonic"
	webdavpkg "discodrive/internal/webdav"
	"discodrive/internal/worker"

	godavcaldav "github.com/emersion/go-webdav/caldav"
	godavcarddav "github.com/emersion/go-webdav/carddav"
)

// tokenTTL is the fallback session lifetime: it covers device (sync daemon) tokens and
// anything issued before a user row is at hand. Web sessions use the user's own
// users.session_ttl_minutes, which starts at this same hour.
const tokenTTL = time.Hour

func main() {
	cfg := config.Load()

	// Migration subcommand: `server migrate [up|down]` (default: up).
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		runMigrate(cfg, os.Args[2:])
		return
	}

	runServer(cfg)
}

func runMigrate(cfg config.Config, args []string) {
	if cfg.DatabaseURL == "" {
		log.Fatal("discodrive: DATABASE_URL is not set")
	}
	direction := "up"
	if len(args) > 0 {
		direction = args[0]
	}
	switch direction {
	case "up":
		if err := db.MigrateUp(cfg.DatabaseURL); err != nil {
			log.Fatalf("discodrive: migrate up: %v", err)
		}
		log.Println("discodrive: migrations applied (up)")
	case "down":
		if err := db.MigrateDown(cfg.DatabaseURL); err != nil {
			log.Fatalf("discodrive: migrate down: %v", err)
		}
		log.Println("discodrive: migrations rolled back (down)")
	default:
		log.Fatalf("discodrive: unknown migration direction %q (expected up|down)", direction)
	}
}

var inlineScriptRe = regexp.MustCompile(`(?s)<script([^>]*)>(.*?)</script>`)

// inlineScriptHashes scans the embedded SPA HTML for executable inline <script> blocks
// (the anti-FOUC theme toggle and Nuxt's bootstrap) and returns their CSP 'sha256-...'
// source expressions. Computing them from the very bytes we serve keeps the hashes in
// sync across web rebuilds, so the CSP can drop 'unsafe-inline' for scripts.
func inlineScriptHashes(ui fs.FS) []string {
	seen := map[string]bool{}
	var out []string
	_ = fs.WalkDir(ui, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".html") {
			return nil
		}
		data, rerr := fs.ReadFile(ui, p)
		if rerr != nil {
			return nil
		}
		for _, m := range inlineScriptRe.FindAllSubmatch(data, -1) {
			attrs := string(m[1])
			if strings.Contains(attrs, "src=") || strings.Contains(attrs, "application/json") {
				continue // external scripts are covered by 'self'; JSON blocks aren't executed
			}
			sum := sha256.Sum256(m[2])
			expr := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
			if !seen[expr] {
				seen[expr] = true
				out = append(out, expr)
			}
		}
		return nil
	})
	return out
}

// securityHeaders sets baseline security headers on all responses. script-src drops
// 'unsafe-inline' in favour of per-script hashes (falling back to 'unsafe-inline' only
// if none could be computed, so the app never breaks); style-src keeps 'unsafe-inline'
// for Nuxt's injected styles. object-src, base-uri and framing are locked down.
func securityHeaders(scriptHashes []string, next http.Handler) http.Handler {
	scriptSrc := "script-src 'self' 'unsafe-inline'"
	if len(scriptHashes) > 0 {
		scriptSrc = "script-src 'self' " + strings.Join(scriptHashes, " ")
	}
	// blob: in img-src — needed for in-browser preview of decrypted vault files (blob URLs).
	// media-src 'self' — the player's <audio>/<video> stream from /files/{id}/stream
	// on our own origin; without an explicit directive media would inherit default-src,
	// which is fine today, but the player depends on it, so it is pinned deliberately.
	csp := "default-src 'self'; img-src 'self' data: blob:; media-src 'self'; style-src 'self' 'unsafe-inline'; " +
		scriptSrc + "; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", csp)
		next.ServeHTTP(w, r)
	})
}

func runServer(cfg config.Config) {
	transport, err := httpsecurity.New(cfg.TrustedProxyCIDRs, cfg.Host, cfg.AllowInsecureHTTP)
	if err != nil {
		log.Fatalf("discodrive: transport configuration: %v", err)
	}
	if cfg.DatabaseURL == "" {
		log.Fatal("discodrive: DATABASE_URL is not set")
	}
	if cfg.JWTSecret == "" {
		log.Fatal("discodrive: JWT_SECRET is not set")
	}
	// Refuse to start with a weak/default secret: it would allow forging any JWT,
	// including admin (the placeholder lives in .env.example in git).
	if cfg.JWTSecret == "change-me-to-a-long-random-secret" || len(cfg.JWTSecret) < 32 {
		log.Fatal("discodrive: JWT_SECRET is too weak — set a random value of at least 32 bytes")
	}

	// Graceful shutdown on SIGINT/SIGTERM (docker stop).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Run migrations at startup — a fresh deploy is ready right after `docker compose up`.
	if err := db.MigrateUp(cfg.DatabaseURL); err != nil {
		log.Fatalf("discodrive: migrations: %v", err)
	}

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("discodrive: connecting to database: %v", err)
	}
	defer pool.Close()

	queries := db.New(pool)
	cipher, err := secret.New(cfg.SettingsEncryptionKey)
	if err != nil {
		log.Fatalf("discodrive: SETTINGS_ENCRYPTION_KEY: %v", err)
	}
	notifier := notify.New(queries, notify.NewEmailChannel(queries, cipher))
	issuer := auth.NewTokenIssuer(cfg.JWTSecret, tokenTTL)
	authSvc := auth.NewService(pool, issuer, cipher)
	wa, err := auth.NewWebAuthn(cfg.BaseDomain)
	if err != nil {
		log.Fatalf("webauthn config: %v", err)
	}
	authSvc.SetWebAuthn(wa)
	store := storage.NewLocalDisk(cfg.StorageRoot)
	fileSvc := storage.NewFileService(pool, store)
	uploads := storage.NewUploads(store, fileSvc)

	// Storage limits: per-user quotas (users.storage_quota) and the server-wide cap
	// (STORAGE_TOTAL_GB), enforced on every write path — API uploads (plain and
	// resumable), WebDAV, sync, the server-side downloads and articles of "Saved", and
	// podcast episodes (through the file service the Subsonic handler holds). One
	// checker is shared, so the admin panel validates quotas against exactly what the
	// write path allows.
	quotaChecker := quota.New(queries, cfg.StorageTotalBytes())
	fileSvc.SetQuota(quotaChecker)
	if err := uploads.SetQuota(quotaChecker); err != nil {
		log.Fatalf("recover upload accounting: %v", err)
	}
	// VERSION_KEEP=0 turns version history off: overwrites replace the file and keep
	// nothing. The trim job then also clears whatever history is already stored.
	if cfg.VersionKeep <= 0 {
		fileSvc.DisableVersions()
		log.Println("discodrive: file versioning is off (VERSION_KEEP=0) — overwrites cannot be rolled back")
	}
	authSvc.SetDefaultQuota(cfg.DefaultUserQuotaBytes())
	if total := cfg.StorageTotalBytes(); total > 0 {
		log.Printf("discodrive: storage capped at %s (STORAGE_TOTAL_GB=%d)", quota.HumanBytes(total), cfg.StorageTotalGB)
	}

	// Initialize the local bootstrap secret before starting workers or HTTP.
	if err := authSvc.InitBootstrap(ctx, cfg.SetupTokenFile); err != nil {
		log.Fatalf("discodrive: bootstrap: %v", err)
	}
	if needed, err := authSvc.SetupNeeded(ctx); err != nil {
		log.Fatalf("discodrive: setup check: %v", err)
	} else if needed {
		log.Printf("discodrive: initial setup required — read token from %s via the server console and open /app/setup over HTTPS", cfg.SetupTokenFile)
	}

	// Saved items (bookmarks / read-later / server-side downloads). Stale
	// "processing" rows are re-queued before the router and worker start, so the
	// reset cannot race live processing goroutines.
	savedSvc := saved.NewService(queries, store, cfg.SavedMaxDownloadMB)
	savedSvc.SetQuota(quotaChecker)
	savedSvc.SetCipher(cipher)
	if err := savedSvc.RecoverStale(ctx); err != nil {
		log.Fatalf("discodrive: saved recover: %v", err)
	}
	// Browser bookmark sync: server-authoritative tree + favicon enrichment.
	bookmarksSvc := bookmarks.NewService(pool, queries, store)

	// Background jobs: GC for versions/trash, rescan + fsnotify + music/ebook indexing.
	musicIdx := music.NewIndexer(queries, cfg.StorageRoot)
	ebookIdx := ebook.NewIndexer(queries, cfg.StorageRoot)
	tagEditor := music.NewTagEditor(queries, fileSvc, cfg.StorageRoot)
	metaEditor := ebook.NewMetadataEditor(queries, cfg.StorageRoot)
	workerCfg := worker.Default(cfg.VersionKeep, cfg.TrashDays, cfg.RescanSeconds)
	// Lets the storage-alert job warn the admins about the cap running out, not just
	// about the disk running out.
	workerCfg.StorageTotal = cfg.StorageTotalBytes()
	go worker.New(fileSvc, cfg.StorageRoot, queries, notifier, workerCfg, musicIdx, ebookIdx, savedSvc, bookmarksSvc).Run(ctx)
	// Reap abandoned resumable-upload sessions (idle > 1h) and their staged temp files.
	go uploads.StartGC(ctx, 5*time.Minute, time.Hour)

	eventHub := api.NewEventHub(pool)
	go eventHub.Run(ctx)

	dav := webdavpkg.Auth(authSvc, queries, webdavpkg.Handler(fileSvc, "/dav"))

	davSvc := davpkg.NewService(pool)
	calBackend := caldavpkg.New(davSvc)
	calHandler := &godavcaldav.Handler{Backend: calBackend, Prefix: "/caldav"}
	caldavH := caldavpkg.Handler(authSvc, queries, calBackend, calHandler)

	cardBackend := carddavpkg.New(davSvc)
	cardHandler := &godavcarddav.Handler{Backend: cardBackend, Prefix: "/carddav"}
	carddavH := carddavpkg.Handler(authSvc, queries, cardBackend, cardHandler)

	subsonicH := subsonic.New(queries, cipher, fileSvc, cfg.StorageRoot, cfg.XAccelEnabled)
	opdsH := opds.New(queries, cipher, cfg.StorageRoot, cfg.XAccelEnabled)
	kosyncH := kosync.New(queries, cipher)

	scriptHashes := inlineScriptHashes(discodrive.WebUI())
	srv := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           transport.Handler(securityHeaders(scriptHashes, api.NewRouter(authSvc, queries, fileSvc, uploads, cfg.StorageRoot, cipher, notifier, discodrive.WebUI(), dav, caldavH, carddavH, davSvc, cfg.XAccelEnabled, eventHub, subsonicH, opdsH, kosyncH, tagEditor, metaEditor, savedSvc, bookmarksSvc))),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		if cfg.AllowInsecureHTTP {
			log.Printf("discodrive: local development HTTP listening on %s (setup still requires HTTPS)", cfg.Addr())
		} else {
			log.Printf("discodrive: private HTTP backend listening on %s; public requests require HTTPS via a trusted proxy", cfg.Addr())
		}
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("discodrive: server crashed: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("discodrive: shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("discodrive: shutdown error: %v", err)
	}
}
