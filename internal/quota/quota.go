// Package quota enforces how much space a user may occupy (users.storage_quota) and
// how much the whole service may occupy on its disk (STORAGE_TOTAL_GB).
//
// Occupied space is computed fresh from the database on every check — see the
// UserStorageUsage query. It counts live files, files sitting in the trash (they are
// removed from disk only after TRASH_DAYS), persisted upload reservations, version
// snapshots and downloaded podcast
// episodes, because all of them are bytes the user is actually holding on the disk.
// users.storage_used is only a cache for the "near limit" notification and is never
// trusted here.
//
// A nil *Checker means "no limits configured": every method then reports unlimited
// space and nil errors, so callers can hold an optional checker without nil guards.
package quota

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

// ErrExceeded is returned when a write would push the user past their quota, or the
// service past its server-wide cap. Handlers map it to 507 Insufficient Storage.
var ErrExceeded = errors.New("insufficient storage")

// ErrOvercommit is returned when an admin tries to hand out more quota than the
// server-wide cap allows.
var ErrOvercommit = errors.New("quota exceeds the server storage limit")

// Unlimited is the allowance reported when nothing constrains a write.
//
// One incoming byte costs exactly one byte of disk. That holds for an overwrite too:
// the content it replaces is moved into .versions rather than freed, so the file tree
// plus its history grows by what the client sent.
const Unlimited = int64(math.MaxInt64)

// Free-space thresholds, in percent of a storage limit that is still free. They drive
// both the colour of the admin dashboard tiles and the alert email to administrators,
// so they live here rather than in either of those two places: the panel must warn
// about exactly what the mail warns about.
const (
	WarnFreePercent = 20
	CritFreePercent = 10
	// recoverMargin keeps a limit hovering on a threshold from alternating between two
	// levels on every tick (and mailing the admin each time it crosses upwards): the
	// level only drops once free space is this many points clear of the threshold.
	recoverMargin = 2
)

// Level is how alarming the remaining free space is.
type Level int

const (
	LevelOK Level = iota
	LevelWarn
	LevelCritical
)

// LevelFor maps percent-free to a level. A negative percent means "no limit, or a disk
// we could not stat" and reports LevelOK — an unknown limit is not an alarm.
func LevelFor(freePercent int) Level {
	switch {
	case freePercent < 0:
		return LevelOK
	case freePercent <= CritFreePercent:
		return LevelCritical
	case freePercent <= WarnFreePercent:
		return LevelWarn
	default:
		return LevelOK
	}
}

// SettledLevel is LevelFor with hysteresis: `was` is the level currently in force, and
// the result only steps down when free space has climbed recoverMargin points past the
// threshold that would hold it there.
func SettledLevel(freePercent int, was Level) Level {
	now := LevelFor(freePercent)
	if now >= was {
		return now
	}
	switch was {
	case LevelCritical:
		if freePercent < CritFreePercent+recoverMargin {
			return LevelCritical
		}
	case LevelWarn:
		if freePercent < WarnFreePercent+recoverMargin {
			return LevelWarn
		}
	}
	return now
}

// FreePercent is free as a percentage of total, rounded down. Total 0 means there is
// no limit to be a percentage of, and is reported as -1 ("unknown"), which LevelFor
// reads as LevelOK.
func FreePercent(free, total int64) int {
	if total <= 0 {
		return -1
	}
	if free <= 0 {
		return 0
	}
	return int(free * 100 / total)
}

// Checker answers "may this user write n more bytes?".
type Checker struct {
	q *db.Queries
	// total is the server-wide cap in bytes; 0 = unlimited.
	total int64
}

// New returns a Checker. totalBytes is the server-wide cap (0 = unlimited).
func New(q *db.Queries, totalBytes int64) *Checker {
	return &Checker{q: q, total: totalBytes}
}

// Total returns the server-wide cap in bytes (0 = unlimited).
func (c *Checker) Total() int64 {
	if c == nil {
		return 0
	}
	return c.total
}

// Used reports how much the service occupies in total — the same sum the server-wide
// cap is checked against, so the admin dashboard shows the number that actually decides
// whether the next write is refused.
func (c *Checker) Used(ctx context.Context) (int64, error) {
	if c == nil {
		return 0, nil
	}
	return c.q.TotalStorageUsage(ctx)
}

// Allowance reports how many more bytes the user may write: the smaller of what is
// left of their own quota and what is left of the server-wide cap. Unlimited when
// neither applies.
func (c *Checker) Allowance(ctx context.Context, userID pgtype.UUID) (int64, error) {
	if c == nil {
		return Unlimited, nil
	}
	allowance := Unlimited

	user, err := c.q.GetUserByID(ctx, userID)
	if err != nil {
		return 0, err
	}
	if user.StorageQuota.Valid {
		used, err := c.q.UserStorageUsage(ctx, userID)
		if err != nil {
			return 0, err
		}
		allowance = remaining(user.StorageQuota.Int64, used)
	}
	if c.total > 0 {
		totalUsed, err := c.q.TotalStorageUsage(ctx)
		if err != nil {
			return 0, err
		}
		allowance = min(allowance, remaining(c.total, totalUsed))
	}
	return allowance, nil
}

// Check reports whether the user may write n more bytes.
func (c *Checker) Check(ctx context.Context, userID pgtype.UUID, n int64) error {
	if c == nil || n <= 0 {
		return nil
	}
	allowance, err := c.Allowance(ctx, userID)
	if err != nil {
		return err
	}
	if n > allowance {
		return exceeded(allowance)
	}
	return nil
}

// Reader wraps r so that reading more than the user's current allowance fails with
// ErrExceeded instead of writing the bytes out. This is what makes uploads of unknown
// or misdeclared length safe: the transfer dies at the limit rather than after the
// whole file has landed in staging.
//
// The allowance is sampled once, when the reader is created. Concurrent writes by the
// same user can therefore overshoot slightly; the next check sees the real total and
// refuses, so the overshoot is bounded by what is in flight — not a leak.
func (c *Checker) Reader(ctx context.Context, userID pgtype.UUID, r io.Reader) (io.Reader, error) {
	return c.ReaderReserving(ctx, userID, 0, r)
}

// ReaderReserving is Reader with reserved bytes already spoken for but not yet visible
// in the usage sum — the chunks a resumable upload holds in .uploads/ until it
// completes. Without it every chunk would be measured against the same allowance and an
// upload could stage its way past the quota.
func (c *Checker) ReaderReserving(ctx context.Context, userID pgtype.UUID, reserved int64, r io.Reader) (io.Reader, error) {
	if c == nil {
		return r, nil
	}
	allowance, err := c.Allowance(ctx, userID)
	if err != nil {
		return nil, err
	}
	budget := allowance
	if budget != Unlimited {
		budget = max(0, budget-reserved)
	}
	if budget <= 0 {
		return nil, exceeded(allowance)
	}
	if budget == Unlimited {
		return r, nil
	}
	return &limitReader{r: r, left: budget, allowance: allowance}, nil
}

// Assignable returns how much quota is still free to hand out: the server-wide cap
// minus the quotas of all other users. exclude is the user being edited (its current
// quota does not count against itself); pass the zero UUID when creating a user.
// The second result is false when there is no cap at all.
func (c *Checker) Assignable(ctx context.Context, exclude pgtype.UUID) (int64, bool, error) {
	if c == nil || c.total <= 0 {
		return 0, false, nil
	}
	assigned, err := c.q.SumAssignedQuotas(ctx, exclude)
	if err != nil {
		return 0, false, err
	}
	return remaining(c.total, assigned), true, nil
}

// CheckAssign validates a quota an admin wants to give a user. quota == nil means
// "no personal quota" — always allowed, the server-wide cap still bounds the writes.
func (c *Checker) CheckAssign(ctx context.Context, exclude pgtype.UUID, quota *int64) error {
	if c == nil || quota == nil {
		return nil
	}
	free, capped, err := c.Assignable(ctx, exclude)
	if err != nil {
		return err
	}
	if !capped {
		return nil
	}
	if *quota > free {
		return fmt.Errorf("%w: %s of %s is still unassigned", ErrOvercommit,
			HumanBytes(free), HumanBytes(c.total))
	}
	return nil
}

// remaining is limit-used, floored at zero (usage can exceed a quota lowered after
// the fact, or one raised then filled — a negative allowance would read as unlimited
// once it met min()).
func remaining(limit, used int64) int64 {
	if used >= limit {
		return 0
	}
	return limit - used
}

func exceeded(allowance int64) error {
	return fmt.Errorf("%w: %s left", ErrExceeded, HumanBytes(allowance))
}

// limitReader fails the read that crosses the allowance. Unlike io.LimitedReader it
// reports an error instead of a clean EOF: a silent EOF would publish a truncated
// file as if the client had sent exactly that much.
type limitReader struct {
	r         io.Reader
	left      int64
	allowance int64
}

func (l *limitReader) Read(p []byte) (int, error) {
	if l.left < 0 {
		return 0, exceeded(l.allowance)
	}
	// Read one byte past what is left: a source that stops exactly at the allowance is
	// fine (left hits 0 and the next read returns EOF), one that keeps going pushes left
	// negative — which tells the two apart without an extra round trip.
	if int64(len(p)) > l.left+1 {
		p = p[:l.left+1]
	}
	n, err := l.r.Read(p)
	l.left -= int64(n)
	if l.left < 0 {
		return n, exceeded(l.allowance)
	}
	return n, err
}

// HumanBytes formats a byte count for messages that reach the user.
func HumanBytes(n int64) string {
	const unit = 1024
	if n == Unlimited {
		return "unlimited"
	}
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}
