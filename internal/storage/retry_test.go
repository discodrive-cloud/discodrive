package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestWithRetry(t *testing.T) {
	ctx := context.Background()
	deadlock := &pgconn.PgError{Code: "40P01"}
	serialization := &pgconn.PgError{Code: "40001"}
	other := errors.New("boom")

	cases := []struct {
		name      string
		errs      []error // returned by successive calls; nil after the list ends
		wantCalls int
		want      func(error) bool
	}{
		{"deadlock then success", []error{deadlock, deadlock}, 3, func(err error) bool { return err == nil }},
		{"stale path then success", []error{errStale}, 2, func(err error) bool { return err == nil }},
		{"serialization every time", []error{serialization, serialization, serialization, serialization}, retryAttempts,
			func(err error) bool { return errors.Is(err, ErrBusy) }},
		{"other error is not retried", []error{other}, 1, func(err error) bool { return errors.Is(err, other) }},
		{"not found is not retried", []error{ErrNotFound}, 1, func(err error) bool { return errors.Is(err, ErrNotFound) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			err := withRetry(ctx, func() error {
				calls++
				if calls <= len(c.errs) {
					return c.errs[calls-1]
				}
				return nil
			})
			if calls != c.wantCalls || !c.want(err) {
				t.Fatalf("calls=%d err=%v, want calls=%d", calls, err, c.wantCalls)
			}
		})
	}

	t.Run("value", func(t *testing.T) {
		calls := 0
		v, err := retryValue(ctx, func() (int, error) {
			calls++
			if calls == 1 {
				return 0, deadlock
			}
			return 42, nil
		})
		if err != nil || v != 42 || calls != 2 {
			t.Fatalf("v=%d err=%v calls=%d", v, err, calls)
		}
	})

	t.Run("cancelled context stops", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		calls := 0
		err := withRetry(cctx, func() error {
			calls++
			cancel()
			return deadlock
		})
		if calls != 1 || !errors.Is(err, context.Canceled) {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
	})
}
