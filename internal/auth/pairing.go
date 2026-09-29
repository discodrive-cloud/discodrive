package auth

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

// ErrPairingProcessed means the pairing was no longer pending (approved by a
// concurrent request, or consumed).
var ErrPairingProcessed = errors.New("pairing already processed")

// ApprovePairing creates the user's desktop device and flips the pairing from pending
// to approved in one transaction: a request that loses the race rolls its device back
// instead of leaving a live device nobody holds a token for.
func (s *Service) ApprovePairing(ctx context.Context, pairingID, userID pgtype.UUID, name string, tokenVersion int64) (db.Device, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return db.Device{}, err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	dev, err := q.CreateDesktopDevice(ctx, db.CreateDesktopDeviceParams{UserID: userID, Name: name, TokenVersion: tokenVersion})
	if err != nil {
		return db.Device{}, err
	}
	if _, err := q.ApprovePairing(ctx, db.ApprovePairingParams{ID: pairingID, UserID: userID, DeviceID: dev.ID}); errors.Is(err, pgx.ErrNoRows) {
		return db.Device{}, ErrPairingProcessed
	} else if err != nil {
		return db.Device{}, err
	}
	return dev, tx.Commit(ctx)
}
