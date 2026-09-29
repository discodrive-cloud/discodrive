package api

import (
	"context"
	"net/http"
	"sync"
	"testing"
)

// Approving one pairing code twice at once used to create a device for each request:
// the device was inserted before the pending→approved flip, and the loser's device
// stayed behind as an orphan (a live device nobody holds a token for). Creating the
// device and approving the pairing now commit together, so one approval, one device.
func TestPairApproveRaceCreatesOneDevice(t *testing.T) {
	ctx := context.Background()
	_, q, svc := bootstrapPairingDB(t)
	tok, u, err := svc.Register(ctx, "race@x.test", "password12")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{auth: svc, q: q, loginLimiter: newLoginLimiter()}
	mux := http.NewServeMux()
	mux.Handle("POST /pair/{code}/approve", svc.Middleware(http.HandlerFunc(s.handlePairApprove)))

	for round := range 5 {
		_, m := doPost(http.HandlerFunc(s.handlePairInit), "/pair/init", "", map[string]any{"name": "laptop"})
		code, _ := m["user_code"].(string)
		if code == "" {
			t.Fatalf("round %d: init failed: %v", round, m)
		}
		const n = 8
		var wg sync.WaitGroup
		oks := make(chan int, n)
		for range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rec, _ := doPost(mux, "/pair/"+code+"/approve", tok, map[string]any{})
				oks <- rec.Code
			}()
		}
		wg.Wait()
		close(oks)
		approved := 0
		for c := range oks {
			if c == http.StatusOK {
				approved++
			}
		}
		if approved != 1 {
			t.Fatalf("round %d: %d approvals succeeded, want 1", round, approved)
		}
		devs, err := q.ListDevicesForUser(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		desktops := 0
		for _, d := range devs {
			if d.Kind == "desktop" {
				desktops++
			}
		}
		if desktops != round+1 {
			t.Fatalf("round %d: %d desktop devices for %d approved pairings (orphans from lost races)", round, desktops, round+1)
		}
	}
}
