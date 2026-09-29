package auth

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

var (
	ErrSelfDemote = errors.New("you cannot remove your own admin role")
	ErrSelfDelete = errors.New("cannot delete your own account")
	ErrLastAdmin  = errors.New("the last admin cannot be demoted or deleted")
)

// QuotaChange is a tri-state quota update: Set=false leaves the quota as it is,
// Set with a nil Bytes removes it (only the server-wide cap applies), otherwise
// Bytes becomes the new quota.
type QuotaChange struct {
	Set   bool
	Bytes *int64
}

// AdminUpdateUser changes a user's role ("" = unchanged) and quota. The admin rows are
// locked for the transaction, so neither a self-demotion nor two admins demoting each
// other at once can leave the server without an admin: the role is read on every
// request and the bootstrap latch never reopens, so that state could only be undone
// in SQL. Returns pgx.ErrNoRows for an unknown user.
func (s *Service) AdminUpdateUser(ctx context.Context, caller, uid pgtype.UUID, role string, quota QuotaChange) (db.User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return db.User{}, err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	admins, err := q.LockAdmins(ctx)
	if err != nil {
		return db.User{}, err
	}
	u, err := q.GetUserForAuthUpdate(ctx, uid)
	if err != nil {
		return db.User{}, err
	}
	if role != "" && role != u.Role {
		if u.Role == "admin" {
			if uid == caller {
				return db.User{}, ErrSelfDemote
			}
			if len(admins) <= 1 {
				return db.User{}, ErrLastAdmin
			}
		}
		if u, err = q.SetUserRole(ctx, db.SetUserRoleParams{ID: uid, Role: role}); err != nil {
			return db.User{}, err
		}
	}
	if quota.Set {
		v := pgtype.Int8{}
		if quota.Bytes != nil {
			v = pgtype.Int8{Int64: *quota.Bytes, Valid: true}
		}
		if u, err = q.SetUserQuota(ctx, db.SetUserQuotaParams{ID: uid, StorageQuota: v}); err != nil {
			return db.User{}, err
		}
	}
	return u, tx.Commit(ctx)
}

// AdminDeleteUser deletes a user (cascading to their rows) unless it is the caller or
// the last admin, under the same admin lock as AdminUpdateUser. Returns pgx.ErrNoRows
// for an unknown user. Files on disk are the caller's to clean up once this commits.
func (s *Service) AdminDeleteUser(ctx context.Context, caller, uid pgtype.UUID) error {
	if uid == caller {
		return ErrSelfDelete
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	admins, err := q.LockAdmins(ctx)
	if err != nil {
		return err
	}
	u, err := q.GetUserForAuthUpdate(ctx, uid)
	if err != nil {
		return err
	}
	if u.Role == "admin" && len(admins) <= 1 {
		return ErrLastAdmin
	}
	if err := q.DeleteUser(ctx, uid); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
