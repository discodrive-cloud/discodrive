package notify

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

type fakeChannel struct {
	mu   sync.Mutex
	name string
	sent []Message
}

func (f *fakeChannel) Name() string { return f.name }
func (f *fakeChannel) Send(_ context.Context, m Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m)
	return nil
}

type fakeStore struct {
	enabled string // value of notifications.enabled ("" = row absent)
	prefs   []db.NotificationPrefsForEventRow
}

func (s *fakeStore) GetUserByID(_ context.Context, _ pgtype.UUID) (db.User, error) {
	return db.User{Email: "u@example.com"}, nil
}
func (s *fakeStore) GetSetting(_ context.Context, key string) (db.Setting, error) {
	if key == "notifications.enabled" && s.enabled != "" {
		return db.Setting{Key: key, Value: s.enabled}, nil
	}
	return db.Setting{}, pgx.ErrNoRows
}
func (s *fakeStore) NotificationPrefsForEvent(_ context.Context, _ db.NotificationPrefsForEventParams) ([]db.NotificationPrefsForEventRow, error) {
	return s.prefs, nil
}

// valid UUID string for Emit
const testUID = "11111111-1111-1111-1111-111111111111"

func newTestNotifier(st store, ch Channel) *Notifier {
	return &Notifier{q: st, channels: map[string]Channel{ch.Name(): ch}}
}

// waitSent polls the fake channel (Emit is async) until want messages have been sent.
// For want==0 it allows enough time to confirm no messages arrive.
func waitSent(t *testing.T, ch *fakeChannel, want int) {
	t.Helper()
	for i := 0; i < 60; i++ {
		if ch.count() >= want && (want > 0 || i > 10) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if ch.count() != want {
		t.Fatalf("sent %d, expected %d", ch.count(), want)
	}
}

func TestEmitMandatoryBypassesPrefs(t *testing.T) {
	ch := &fakeChannel{name: "email"}
	st := &fakeStore{enabled: "false", prefs: []db.NotificationPrefsForEventRow{{Channel: "email", Enabled: false}}}
	n := newTestNotifier(st, ch)
	n.Emit(context.Background(), testUID, "device.password_added", map[string]any{"DeviceName": "Mac"})
	waitSent(t, ch, 1)
}

func TestEmitOptionalRespectsPref(t *testing.T) {
	ch := &fakeChannel{name: "email"}
	st := &fakeStore{prefs: []db.NotificationPrefsForEventRow{{Channel: "email", Enabled: false}}}
	n := newTestNotifier(st, ch)
	n.Emit(context.Background(), testUID, "share.received", map[string]any{"NodeName": "f", "SharerEmail": "a@b"})
	waitSent(t, ch, 0)
}

func TestEmitKillSwitchSilencesOptional(t *testing.T) {
	ch := &fakeChannel{name: "email"}
	st := &fakeStore{enabled: "false"}
	n := newTestNotifier(st, ch)
	n.Emit(context.Background(), testUID, "share.received", map[string]any{"NodeName": "f", "SharerEmail": "a@b"})
	waitSent(t, ch, 0)
}

func (f *fakeChannel) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.sent) }
