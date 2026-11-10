package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A stolen bearer token must not allow guessing the account password without bound just
// because every guess comes from a new address: wrong guesses are counted per account.
func TestPasswordGuessesAreCountedPerAccount(t *testing.T) {
	ctx := context.Background()
	_, q, svc := bootstrapPairingDB(t)
	victim, _, err := svc.Register(ctx, "victim@x.test", "password12")
	if err != nil {
		t.Fatal(err)
	}
	bystander, _, err := svc.Register(ctx, "bystander@x.test", "password12")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{auth: svc, q: q, loginLimiter: newLoginLimiter(), accountLimiter: newAccountLimiter()}
	h := svc.Middleware(s.passwordChecked(s.handleChangePassword))

	guess := func(tok, current string, n int) int {
		body := fmt.Sprintf(`{"current_password":%q,"new_password":"password34"}`, current)
		req := httptest.NewRequest(http.MethodPut, "/me/password", bytes.NewReader([]byte(body)))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.RemoteAddr = fmt.Sprintf("203.0.%d.%d:4000", n/250, n%250+1) // a new address every time
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	blockedAt := -1
	for i := 0; i < 40; i++ {
		code := guess(victim, "wrong-guess", i)
		if code == http.StatusTooManyRequests {
			blockedAt = i
			break
		}
		if code != http.StatusUnauthorized {
			t.Fatalf("guess %d: got %d, want 401 or 429", i, code)
		}
	}
	if blockedAt < 0 {
		t.Fatal("40 wrong passwords from 40 addresses were all checked: nothing counts guesses per account")
	}
	if code := guess(bystander, "password12", 100); code != http.StatusOK {
		t.Fatalf("another account changing its password: got %d, want 200", code)
	}
}
