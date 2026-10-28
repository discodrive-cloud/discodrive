package auth

import (
	"context"
	"time"

	"discodrive/internal/db"
	"github.com/golang-jwt/jwt/v5"
)

// photosHiddenTTL is how long the unlocked gallery stays open.
const photosHiddenTTL = 10 * time.Minute

// IssuePhotosHidden trades a single-use approval token (action "photos:hidden", from the
// password or passkey flow) for a short-lived token that opens the hidden part of the
// gallery. It carries the user's token version, so a password change closes it.
func (s *Service) IssuePhotosHidden(ctx context.Context, userID, approvalToken string) (string, time.Time, error) {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return "", time.Time{}, ErrApproval
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", time.Time{}, err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	u, err := q.GetUserForAuthUpdate(ctx, uid)
	if err != nil {
		return "", time.Time{}, ErrApproval
	}
	c, err := s.approval(approvalToken, userID, "photos:hidden", u.TokenVersion)
	if err != nil {
		return "", time.Time{}, err
	}
	consumed, err := consumeChallenge(ctx, q, c.Pur+":"+c.ID, c.ExpiresAt.Time)
	if err != nil {
		return "", time.Time{}, err
	}
	if !consumed {
		return "", time.Time{}, ErrApproval
	}
	if err := tx.Commit(ctx); err != nil {
		return "", time.Time{}, err
	}
	now := time.Now()
	exp := now.Add(photosHiddenTTL)
	claims := Claims{Pur: "photos-hidden", Ver: u.TokenVersion, RegisteredClaims: jwt.RegisteredClaims{
		Subject: userID, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(exp)}}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.issuer.secret)
	if err != nil {
		return "", time.Time{}, err
	}
	return tok, exp, nil
}

// ValidatePhotosHidden checks an unlock token: purpose, owner, expiry and that the
// user's token version has not moved on since it was issued.
func (s *Service) ValidatePhotosHidden(ctx context.Context, userID, token string) error {
	c, err := s.issuer.Parse(token)
	if err != nil || c.Pur != "photos-hidden" || c.Subject != userID || c.ExpiresAt == nil {
		return ErrApproval
	}
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return ErrApproval
	}
	u, err := s.lookupUser(ctx, uid)
	if err != nil || u.TokenVersion != c.Ver || u.MustChangePassword {
		return ErrApproval
	}
	return nil
}
