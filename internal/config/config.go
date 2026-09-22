// Package config reads configuration from the environment.
//
// At step 0.0 only the listen address is required. The other secrets
// (DATABASE_URL, JWT_SECRET, SETTINGS_ENCRYPTION_KEY, BASE_DOMAIN)
// are listed in .env.example and will be wired in on subsequent steps.
package config

import (
	"net"
	"os"
	"path/filepath"
	"strconv"

	"discodrive/internal/httpsecurity"
)

// Config holds the process configuration.
type Config struct {
	// Host is the listen interface (defaults to loopback; Docker overrides it).
	Host string
	// TrustedProxyCIDRs identifies the immediate peers allowed to assert HTTPS.
	TrustedProxyCIDRs string
	// AllowInsecureHTTP is an explicit loopback-only development exception.
	AllowInsecureHTTP bool
	// Port is the HTTP server TCP port.
	Port string
	// DatabaseURL is the Postgres connection string (postgres://...).
	DatabaseURL string
	// JWTSecret is the JWT signing key (HS256).
	JWTSecret string
	// SettingsEncryptionKey is the AES-256 key for secrets in the DB (exactly 32 bytes). Empty = secrets forbidden.
	SettingsEncryptionKey string
	// BaseDomain is the public server domain (used as the WebAuthn relying-party ID).
	BaseDomain string
	// StorageRoot is the root data directory on disk (mirror of user trees).
	StorageRoot string
	// SetupTokenFile stores the one-time bootstrap secret, outside user file trees.
	SetupTokenFile string
	// VersionKeep is how many file versions to retain (extras are pruned by the GC job).
	VersionKeep int
	// TrashDays is the number of days after which a tombstone is physically removed by the GC job.
	TrashDays int
	// RescanSeconds is the period of the background disk rescan (backstop for fsnotify).
	RescanSeconds int
	// XAccelEnabled: serve files via nginx X-Accel-Redirect (true) or stream directly
	// (false, for deployments without nginx and for client tests). Range requests work out of the box.
	XAccelEnabled bool
	// SavedMaxDownloadMB caps a single server-side download (saved items); 0 = unlimited.
	// Generous by default: distro ISOs and archives are the whole point of
	// server-side downloads (a 2 GB cap rejected a plain Ubuntu server ISO).
	SavedMaxDownloadMB int
	// StorageTotalGB is how much of the disk discodrive may occupy in total; 0 = the
	// whole disk. It bounds both what admins can hand out (the sum of user quotas)
	// and what users can actually write, so the service cannot fill a disk it shares
	// with other things.
	StorageTotalGB int
	// DefaultUserQuotaGB is the quota given to a newly created user when none is
	// specified; 0 = no quota (only the server-wide cap applies). Existing users are
	// never touched by this.
	DefaultUserQuotaGB int
}

// GiB is the byte multiplier for the *_GB settings: they are binary gigabytes,
// matching how the admin UI and formatBytes present sizes.
const GiB = int64(1) << 30

// StorageTotalBytes returns the server-wide storage cap in bytes (0 = unlimited).
func (c Config) StorageTotalBytes() int64 { return int64(c.StorageTotalGB) * GiB }

// DefaultUserQuotaBytes returns the default per-user quota in bytes (0 = none).
func (c Config) DefaultUserQuotaBytes() int64 { return int64(c.DefaultUserQuotaGB) * GiB }

// Load reads configuration from the environment, applying defaults.
func Load() Config {
	return Config{
		Host:                  getenv("APP_HOST", "127.0.0.1"),
		TrustedProxyCIDRs:     getenv("TRUSTED_PROXY_CIDRS", httpsecurity.DefaultTrustedProxies),
		AllowInsecureHTTP:     getenvBool("ALLOW_INSECURE_HTTP", false),
		Port:                  getenv("APP_PORT", "8080"),
		DatabaseURL:           os.Getenv("DATABASE_URL"),
		JWTSecret:             os.Getenv("JWT_SECRET"),
		SettingsEncryptionKey: os.Getenv("SETTINGS_ENCRYPTION_KEY"),
		BaseDomain:            os.Getenv("BASE_DOMAIN"),
		StorageRoot:           getenv("STORAGE_ROOT", "/data"),
		SetupTokenFile:        getenv("SETUP_TOKEN_FILE", filepath.Join(getenv("STORAGE_ROOT", "/data"), ".bootstrap", "setup-token")),
		VersionKeep:           getenvInt("VERSION_KEEP", 10),
		TrashDays:             getenvInt("TRASH_DAYS", 30),
		RescanSeconds:         getenvInt("RESCAN_SECONDS", 30),
		XAccelEnabled:         getenvBool("XACCEL_ENABLED", true),
		SavedMaxDownloadMB:    getenvInt("SAVED_MAX_DOWNLOAD_MB", 32768),
		StorageTotalGB:        getenvInt("STORAGE_TOTAL_GB", 0),
		DefaultUserQuotaGB:    getenvInt("DEFAULT_USER_QUOTA_GB", 0),
	}
}

// Addr returns the address string for http.Server (host:port).
func (c Config) Addr() string {
	return net.JoinHostPort(c.Host, c.Port)
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getenvBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
