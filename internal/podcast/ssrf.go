package podcast

import (
	"time"

	"discodrive/internal/fetchguard"
)

// The SSRF guard lives in internal/fetchguard (shared with saved items); these
// aliases keep the podcast package's call sites and public names unchanged.

// ErrBlockedURL is returned for URLs that fail the SSRF guard.
var ErrBlockedURL = fetchguard.ErrBlocked

// ValidateURL enforces http/https and rejects hosts that resolve to loopback,
// private, or link-local addresses.
func ValidateURL(raw string) error { return fetchguard.ValidateURL(raw) }

// safeDialer pins the SSRF check to the actually dialed IP (DNS-rebinding safe).
var safeDialer = fetchguard.Dialer

// httpTimeout bounds a whole feed or cover exchange, and the header phase of an episode
// download. A variable so tests can shorten it.
var httpTimeout = 30 * time.Second

// stallTimeout aborts a download whose body stops arriving. Episode downloads have no
// whole-exchange deadline (see newDownloadClient), so this is what ends a hung transfer.
var stallTimeout = 60 * time.Second
