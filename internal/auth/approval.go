package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"discodrive/internal/db"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
)

var ErrApproval = errors.New("confirm your identity to manage passkeys")

func validPasskeyAction(action string) bool {
	if action == "register" {
		return true
	}
	id, ok := strings.CutPrefix(action, "delete:")
	if !ok {
		return false
	}
	_, err := db.ParseUUID(id)
	return err == nil
}

func (s *Service) issueApproval(u db.User, action string) (string, error) {
	if !validPasskeyAction(action) {
		return "", ErrApproval
	}
	id, err := newDeviceCode()
	if err != nil {
		return "", err
	}
	now := time.Now()
	claims := Claims{Pur: "passkey-approval", Nid: action, Ver: u.TokenVersion, RegisteredClaims: jwt.RegisteredClaims{Subject: db.UUIDString(u.ID), ID: id, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(mfaTokenTTL))}}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.issuer.secret)
}

// ApprovePasskeyWithPassword requires the user's existing factors, not just a session.
func (s *Service) ApprovePasskeyWithPassword(ctx context.Context, userID, password, code, action string) (string, error) {
	uid, err := db.ParseUUID(userID)
	if err != nil || !validPasskeyAction(action) {
		return "", ErrApproval
	}
	u, err := s.q.GetUserByID(ctx, uid)
	if err != nil {
		return "", ErrApproval
	}
	if ver, ok := TokenVersion(ctx); ok && (UserID(ctx) != userID || ver != u.TokenVersion) {
		return "", ErrApproval
	}
	ok, err := VerifyPassword(password, u.PasswordHash)
	if err != nil || !ok {
		return "", ErrApproval
	}
	factors, err := s.q.AvailableMFAFactors(ctx, uid)
	if err != nil {
		return "", err
	}
	if factors.HasTotp && !s.verifyTOTP(ctx, uid, code) {
		return "", ErrApproval
	}
	return s.issueApproval(u, action)
}

func (s *Service) approval(token, userID, action string, version int64) (*Claims, error) {
	c, err := s.issuer.Parse(token)
	if err != nil || c.Pur != "passkey-approval" || c.Subject != userID || c.Nid != action || c.Ver != version || c.ID == "" || c.ExpiresAt == nil {
		return nil, ErrApproval
	}
	return c, nil
}

func (s *Service) BeginPasskeyApproval(ctx context.Context, userID, action string) ([]byte, string, error) {
	if s.wa == nil {
		return nil, "", ErrWebAuthnNotConfigured
	}
	if !validPasskeyAction(action) {
		return nil, "", ErrApproval
	}
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return nil, "", ErrApproval
	}
	u, err := s.loadWAUser(ctx, uid)
	if err != nil || len(u.creds) == 0 {
		return nil, "", ErrApproval
	}
	if ver, ok := TokenVersion(ctx); ok && (UserID(ctx) != userID || ver != u.u.TokenVersion) {
		return nil, "", ErrApproval
	}
	options, sd, err := s.wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, "", err
	}
	data, err := json.Marshal(sd)
	if err != nil {
		return nil, "", err
	}
	token, err := s.issuer.issueWACeremony(waSessionClaims{Pur: "webauthn-approval", Data: base64.StdEncoding.EncodeToString(data), Ver: u.u.TokenVersion, Action: action, RegisteredClaims: jwt.RegisteredClaims{Subject: userID}})
	if err != nil {
		return nil, "", err
	}
	out, err := json.Marshal(options)
	return out, token, err
}

func (s *Service) FinishPasskeyApproval(ctx context.Context, userID, token string, assertion []byte) (string, error) {
	c, err := s.issuer.parseWebAuthnSession(token)
	if err != nil || c.Pur != "webauthn-approval" || c.Subject != userID || !validPasskeyAction(c.Action) {
		return "", ErrApproval
	}
	res, err := s.finishWebAuthnLogin(ctx, token, assertion, "webauthn-approval")
	if err != nil {
		return "", ErrApproval
	}
	if db.UUIDString(res.User.ID) != userID || res.User.TokenVersion != c.Ver {
		return "", ErrApproval
	}
	return s.issueApproval(res.User, c.Action)
}

func (s *Service) DeletePasskey(ctx context.Context, userID, id, approvalToken string) error {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return ErrApproval
	}
	cid, err := db.ParseUUID(id)
	if err != nil {
		return ErrApproval
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	u, err := q.GetUserForAuthUpdate(ctx, uid)
	if err != nil {
		return ErrApproval
	}
	c, err := s.approval(approvalToken, userID, "delete:"+id, u.TokenVersion)
	if err != nil {
		return err
	}
	consumed, err := consumeChallenge(ctx, q, c.Pur+":"+c.ID, c.ExpiresAt.Time)
	if err != nil {
		return err
	}
	if !consumed {
		return ErrApproval
	}
	if err := q.DeleteWebAuthnCredential(ctx, db.DeleteWebAuthnCredentialParams{ID: cid, UserID: uid}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return tx.Commit(ctx)
}
