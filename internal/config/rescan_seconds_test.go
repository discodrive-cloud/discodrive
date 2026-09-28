package config_test

import (
	"testing"

	"discodrive/internal/config"
)

// RESCAN_SECONDS is gone: setting it must not break startup, only earn a notice.
func TestLoadNoticesRescanSeconds(t *testing.T) {
	t.Setenv("RESCAN_SECONDS", "30")
	if !config.Load().RescanSecondsIgnored {
		t.Fatal("RESCAN_SECONDS is set but not reported as ignored")
	}
	t.Setenv("RESCAN_SECONDS", "")
	if config.Load().RescanSecondsIgnored {
		t.Fatal("reported without RESCAN_SECONDS set")
	}
}
