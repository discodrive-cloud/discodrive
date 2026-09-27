package db

import (
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultMaxConns is the pool size used unless DATABASE_URL sets pool_max_conns. Every
// in-flight upload holds a connection (AcquireConnection keeps two spare), so pgx's
// CPU-count default left a 4-core machine with just two concurrent uploads.
const DefaultMaxConns = 16

// DefaultMaxConnIdleTime closes pooled connections idle for longer, unless DATABASE_URL
// sets pool_max_conn_idle_time. Each one is a PostgreSQL backend with its own memory,
// and pgx's 30-minute default kept a whole burst's worth of them around.
const DefaultMaxConnIdleTime = time.Minute

// PoolConfig parses databaseURL, applying DefaultMaxConns and DefaultMaxConnIdleTime
// where the URL does not set them itself.
func PoolConfig(databaseURL string) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	// Both URL and keyword/value connection strings may carry the setting.
	if !strings.Contains(databaseURL, "pool_max_conns") {
		cfg.MaxConns = max(cfg.MaxConns, DefaultMaxConns)
	}
	if !strings.Contains(databaseURL, "pool_max_conn_idle_time") {
		cfg.MaxConnIdleTime = DefaultMaxConnIdleTime
	}
	return cfg, nil
}
