package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"time"

	"discodrive/internal/db"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
)

// ErrNoKey: the device key is not the user's or no longer exists.
var ErrNoKey = errors.New("photos: no such unlock key")

// photosKeyChallengeTTL is how long a device has to sign a challenge (a Face ID prompt).
const photosKeyChallengeTTL = 2 * time.Minute

const purposePhotosKeyChallenge = "photos-key-challenge"

// validPhotosKeySPKI accepts a P-256 public key in SubjectPublicKeyInfo DER (what a
// Secure Enclave or Android Keystore key exports).
func validPhotosKeySPKI(spki []byte) bool {
	k, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		return false
	}
	ec, ok := k.(*ecdsa.PublicKey)
	return ok && ec.Curve == elliptic.P256()
}

// verifyP256 checks an ASN.1 DER ECDSA signature over SHA-256(msg).
func verifyP256(spki, msg, sig []byte) bool {
	if !validPhotosKeySPKI(spki) {
		return false
	}
	k, _ := x509.ParsePKIXPublicKey(spki)
	h := sha256.Sum256(msg)
	return ecdsa.VerifyASN1(k.(*ecdsa.PublicKey), h[:], sig)
}

// challengeScope binds a challenge token to the key and to the challenge bytes.
func challengeScope(keyID string, challenge []byte) string {
	h := sha256.Sum256(challenge)
	return keyID + ":" + base64.RawURLEncoding.EncodeToString(h[:])
}

func issuePhotosKeyChallenge(iss *TokenIssuer, userID, keyID string, ver int64, challenge []byte) (string, error) {
	jti, err := newDeviceCode()
	if err != nil {
		return "", err
	}
	now := time.Now()
	c := Claims{Pur: purposePhotosKeyChallenge, Nid: challengeScope(keyID, challenge), Ver: ver, RegisteredClaims: jwt.RegisteredClaims{
		Subject: userID, ID: jti, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(photosKeyChallengeTTL))}}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(iss.secret)
}

func parsePhotosKeyChallenge(iss *TokenIssuer, token, userID, keyID string, challenge []byte) (*Claims, error) {
	c, err := iss.Parse(token)
	if err != nil || c.Pur != purposePhotosKeyChallenge || c.Subject != userID || c.ID == "" || c.ExpiresAt == nil ||
		c.Nid != challengeScope(keyID, challenge) {
		return nil, ErrApproval
	}
	return c, nil
}

// The challenge is not stored: the token carries its hash, and the client sends the
// challenge back with its signature.

// RegisterPhotosKey stores a device's public key. It needs a fresh "photos:hidden"
// approval (password + TOTP or passkey), which it consumes.
func (s *Service) RegisterPhotosKey(ctx context.Context, userID, approvalToken string, spki []byte, name string) (string, error) {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return "", ErrApproval
	}
	if !validPhotosKeySPKI(spki) {
		return "", ErrApproval
	}
	if len(name) > 100 {
		name = name[:100]
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	u, err := q.GetUserForAuthUpdate(ctx, uid)
	if err != nil {
		return "", ErrApproval
	}
	c, err := s.approval(approvalToken, userID, "photos:hidden", u.TokenVersion)
	if err != nil {
		return "", err
	}
	consumed, err := consumeChallenge(ctx, q, c.Pur+":"+c.ID, c.ExpiresAt.Time)
	if err != nil {
		return "", err
	}
	if !consumed {
		return "", ErrApproval
	}
	id, err := q.CreatePhotoUnlockKey(ctx, db.CreatePhotoUnlockKeyParams{UserID: uid, PublicKey: spki, Name: name, TokenVersion: u.TokenVersion})
	if err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return db.UUIDString(id), nil
}

func (s *Service) photosKey(ctx context.Context, q *db.Queries, userID, keyID string) (db.PhotoUnlockKey, db.User, error) {
	uid, err1 := db.ParseUUID(userID)
	kid, err2 := db.ParseUUID(keyID)
	if err1 != nil || err2 != nil {
		return db.PhotoUnlockKey{}, db.User{}, ErrNoKey
	}
	k, err := q.GetPhotoUnlockKey(ctx, db.GetPhotoUnlockKeyParams{ID: kid, UserID: uid})
	if errors.Is(err, pgx.ErrNoRows) {
		return k, db.User{}, ErrNoKey
	}
	if err != nil {
		return k, db.User{}, err
	}
	u, err := q.GetUserByID(ctx, uid)
	if err != nil {
		return k, u, ErrNoKey
	}
	return k, u, nil
}

// PhotosKeyChallenge hands out 32 random bytes for the device to sign, with a token
// that binds them to the user and the key.
func (s *Service) PhotosKeyChallenge(ctx context.Context, userID, keyID string) ([]byte, string, error) {
	k, u, err := s.photosKey(ctx, s.q, userID, keyID)
	if err != nil {
		return nil, "", err
	}
	if k.TokenVersion != u.TokenVersion {
		return nil, "", ErrApproval
	}
	ch := make([]byte, 32)
	if _, err := rand.Read(ch); err != nil {
		return nil, "", err
	}
	tok, err := issuePhotosKeyChallenge(s.issuer, userID, keyID, u.TokenVersion, ch)
	if err != nil {
		return nil, "", err
	}
	return ch, tok, nil
}

// UnlockWithPhotosKey checks the device's signature of a challenge and issues the same
// short-lived hidden-gallery token as IssuePhotosHidden. A challenge works once.
func (s *Service) UnlockWithPhotosKey(ctx context.Context, userID, keyID, challengeToken string, challenge, sig []byte) (string, time.Time, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", time.Time{}, err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	k, u, err := s.photosKey(ctx, q, userID, keyID)
	if err != nil {
		return "", time.Time{}, err
	}
	c, err := parsePhotosKeyChallenge(s.issuer, challengeToken, userID, keyID, challenge)
	if err != nil || c.Ver != u.TokenVersion || k.TokenVersion != u.TokenVersion || u.MustChangePassword {
		return "", time.Time{}, ErrApproval
	}
	if !verifyP256(k.PublicKey, challenge, sig) {
		return "", time.Time{}, ErrApproval
	}
	consumed, err := consumeChallenge(ctx, q, c.Pur+":"+c.ID, c.ExpiresAt.Time)
	if err != nil {
		return "", time.Time{}, err
	}
	if !consumed {
		return "", time.Time{}, ErrApproval
	}
	if err := q.TouchPhotoUnlockKey(ctx, k.ID); err != nil {
		return "", time.Time{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", time.Time{}, err
	}
	return s.issuePhotosHiddenToken(userID, u.TokenVersion)
}

// DeletePhotosKey turns a device key off.
func (s *Service) DeletePhotosKey(ctx context.Context, userID, keyID string) error {
	uid, err1 := db.ParseUUID(userID)
	kid, err2 := db.ParseUUID(keyID)
	if err1 != nil || err2 != nil {
		return ErrNoKey
	}
	n, err := s.q.DeletePhotoUnlockKey(ctx, db.DeletePhotoUnlockKeyParams{ID: kid, UserID: uid})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNoKey
	}
	return nil
}
