package db

import "testing"

// Every in-flight upload holds a pool connection (see AcquireConnection), and pgx sizes
// the pool by CPU count: a 4-core box allowed only 2 uploads, fewer than the web UI
// sends at once. An explicit pool_max_conns in the URL still wins.
func TestPoolConfigSize(t *testing.T) {
	cfg, err := PoolConfig("postgres://u:p@localhost:5432/d?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConns < DefaultMaxConns {
		t.Fatalf("MaxConns = %d, want at least %d", cfg.MaxConns, DefaultMaxConns)
	}
	cfg, err = PoolConfig("postgres://u:p@localhost:5432/d?sslmode=disable&pool_max_conns=5")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConns != 5 {
		t.Fatalf("explicit pool_max_conns ignored: MaxConns = %d", cfg.MaxConns)
	}
	cfg, err = PoolConfig("host=localhost user=u password=p dbname=d pool_max_conns=3")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConns != 3 {
		t.Fatalf("keyword pool_max_conns ignored: MaxConns = %d", cfg.MaxConns)
	}
}
