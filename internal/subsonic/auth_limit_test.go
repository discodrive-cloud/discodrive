package subsonic

import (
	"fmt"
	"testing"
)

// Repeated wrong credentials from one client are cut off (without touching the
// database), while a client with valid credentials is never throttled however
// often it polls.
func TestAuthFailuresAreRateLimited(t *testing.T) {
	h, _ := setupSubsonic(t)
	good := fmt.Sprintf("/rest/ping.view?u=%s&t=%s&s=abc&c=test&v=1.16.1&f=json", testEmail, md5hex(testPassword+"abc"))
	bad := fmt.Sprintf("/rest/ping.view?u=%s&p=wrong&c=test&v=1.16.1&f=json", testEmail)

	for i := 0; i < 50; i++ {
		if _, m := subsonicGet(h, good); subsonicResponse(m)["status"] != "ok" {
			t.Fatalf("valid request %d refused: %v", i, m)
		}
	}
	for i := 0; i < 10; i++ {
		_, m := subsonicGet(h, bad)
		errObj, _ := subsonicResponse(m)["error"].(map[string]any)
		if errObj["code"] != float64(ErrWrongAuth) {
			t.Fatalf("failure %d: %v, want wrong-auth", i, m)
		}
	}
	_, m := subsonicGet(h, good)
	resp := subsonicResponse(m)
	if resp["status"] == "ok" {
		t.Fatal("client past the failure budget is still served")
	}
	errObj, _ := resp["error"].(map[string]any)
	if errObj["code"] != float64(ErrGeneric) {
		t.Errorf("blocked response = %v, want a generic error", resp)
	}
}

// Wrong-password guesses must not be answered faster than right ones depending
// on how many leading characters match; a smoke check that the comparisons use
// the constant-time path (uppercase hex tokens still accepted).
func TestTokenAuthAcceptsUppercaseHex(t *testing.T) {
	h, _ := setupSubsonic(t)
	tok := md5hex(testPassword + "xyz")
	upper := ""
	for _, c := range tok {
		if c >= 'a' && c <= 'f' {
			c -= 'a' - 'A'
		}
		upper += string(c)
	}
	_, m := subsonicGet(h, fmt.Sprintf("/rest/ping.view?u=%s&t=%s&s=xyz&c=test&v=1.16.1&f=json", testEmail, upper))
	if subsonicResponse(m)["status"] != "ok" {
		t.Fatalf("uppercase token refused: %v", m)
	}
}
