package db

import (
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultMaxConns is the pool size used unless DATABASE_URL sets pool_max_conns. Every
// in-flight upload holds a connection (AcquireConnection keeps two spare), so pgx's
// CPU-count default left a 4-core machine with just two concurrent uploads.
const DefaultMaxConns = 16

// PoolConfig parses databaseURL, applying DefaultMaxConns when the URL does not size
// the pool itself.
func PoolConfig(databaseURL string) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	// Both URL and keyword/value connection strings may carry the setting.
	if !strings.Contains(databaseURL, "pool_max_conns") {
		cfg.MaxConns = max(cfg.MaxConns, DefaultMaxConns)
	}
	return cfg, nil
}
