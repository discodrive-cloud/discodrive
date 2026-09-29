package podcast

import (
	"net/http"
	"testing"
)

// An environment proxy makes the transport dial the proxy instead of the target, so
// safeDialer's IP check would only see the proxy address: the stream client (which
// relays upstream bodies to the caller) must never use one.
func TestStreamClientIgnoresEnvironmentProxy(t *testing.T) {
	if streamClient.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("streamClient uses a Proxy func: it bypasses the dial-time SSRF check")
	}
}
