package auth

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pquerna/otp/totp"

	"discodrive/internal/db"
)

var (
	// ErrTOTPNotConfigured is returned when 2FA setup is attempted without an encryption key.
	ErrTOTPNotConfigured = errors.New("2FA requires SETTINGS_ENCRYPTION_KEY to be configured")
	ErrTOTPAlreadyOn     = errors.New("2FA is already enabled")
	ErrTOTPNotEnabled    = errors.New("2FA is not enabled")
	ErrInvalidTOTPCode   = errors.New("invalid code")
)

const (
	totpIssuer      = "discodrive"
	backupCodeCount = 10
)

// SetupTOTP generates a fresh TOTP secret (stored encrypted, not yet enabled) and returns
// the otpauth:// provisioning URI plus the base32 secret for manual entry. Requires an
// encryption key; an already-enabled 2FA must be disabled before re-enrolling.
func (s *Service) SetupTOTP(ctx context.Context, userID string) (otpauthURL, secret string, err error) {
	if s.cipher == nil || !s.cipher.Enabled() {
		return "", "", ErrTOTPNotConfigured
	}
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return "", "", err
	}
	u, err := s.q.GetUserByID(ctx, uid)
	if err != nil {
		return "", "", err
	}
	if existing, err := s.q.GetUserTOTP(ctx, uid); err == nil && existing.Enabled {
		return "", "", ErrTOTPAlreadyOn
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: totpIssuer, AccountName: u.Email})
	if err != nil {
		return "", "", err
	}
	enc, err := s.cipher.Encrypt(key.Secret())
	if err != nil {
		return "", "", err
	}
	if err := s.q.UpsertUserTOTP(ctx, db.UpsertUserTOTPParams{UserID: uid, Secret: enc}); err != nil {
		return "", "", err
	}
	return key.URL(), key.Secret(), nil
}

// ConfirmTOTP validates the first code against the pending secret, enables 2FA, and
// (re)generates one-time backup codes — returned once, in plaintext.
func (s *Service) ConfirmTOTP(ctx context.Context, userID, code string) ([]string, error) {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return nil, err
	}
	row, err := s.q.GetUserTOTP(ctx, uid)
	if err != nil {
		return nil, ErrTOTPNotEnabled
	}
	secret, err := s.cipher.Decrypt(row.Secret)
	if err != nil {
		return nil, err
	}
	if !totp.Validate(code, secret) {
		return nil, ErrInvalidTOTPCode
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)

	if err := qtx.ConfirmUserTOTP(ctx, uid); err != nil {
		return nil, err
	}
	codes, err := issueBackupCodes(ctx, qtx, uid)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return codes, nil
}

// issueBackupCodes replaces the user's backup codes with a fresh set and returns the
// plaintext codes (shown once). Runs inside the caller's transaction.
func issueBackupCodes(ctx context.Context, q *db.Queries, uid pgtype.UUID) ([]string, error) {
	if err := q.DeleteBackupCodes(ctx, uid); err != nil {
		return nil, err
	}
	codes := make([]string, 0, backupCodeCount)
	for i := 0; i < backupCodeCount; i++ {
		plain, hash, err := newBackupCode()
		if err != nil {
			return nil, err
		}
		if err := q.InsertBackupCode(ctx, db.InsertBackupCodeParams{UserID: uid, CodeHash: hash}); err != nil {
			return nil, err
		}
		codes = append(codes, plain)
	}
	return codes, nil
}

// RegenerateBackupCodes verifies a current TOTP code, then replaces all backup codes with a
// fresh set (old ones invalidated). Returns the new codes once.
func (s *Service) RegenerateBackupCodes(ctx context.Context, userID, code string) ([]string, error) {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return nil, err
	}
	if !s.verifyTOTP(ctx, uid, code) {
		return nil, ErrInvalidTOTPCode
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	codes, err := issueBackupCodes(ctx, s.q.WithTx(tx), uid)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return codes, nil
}

// DisableTOTP turns 2FA off after verifying the password and a current code (so a stolen
// session alone cannot disable it). Removes the secret and all backup codes.
func (s *Service) DisableTOTP(ctx context.Context, userID, password, code string) error {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return err
	}
	u, err := s.q.GetUserByID(ctx, uid)
	if err != nil {
		return err
	}
	ok, err := VerifyPassword(password, u.PasswordHash)
	if err != nil {
		return err
	}
	if !ok {
		return ErrInvalidCreds
	}
	row, err := s.q.GetUserTOTP(ctx, uid)
	if err != nil || !row.Enabled {
		return ErrTOTPNotEnabled
	}
	secret, err := s.cipher.Decrypt(row.Secret)
	if err != nil {
		return err
	}
	if !totp.Validate(code, secret) {
		return ErrInvalidTOTPCode
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)
	if err := qtx.DeleteUserTOTP(ctx, uid); err != nil {
		return err
	}
	if err := qtx.DeleteBackupCodes(ctx, uid); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CompleteMFATOTP finishes a login that requires a second factor: it validates the
// MFA-pending token, then the code (TOTP or, failing that, a one-time backup code),
// and issues a full session.
func (s *Service) CompleteMFATOTP(ctx context.Context, mfaToken, code string) (LoginResult, error) {
	claims, err := s.issuer.Parse(mfaToken)
	if err != nil || claims.Pur != "mfa" || claims.ID == "" || claims.ExpiresAt == nil {
		return LoginResult{}, ErrInvalidMFAToken
	}
	uid, err := db.ParseUUID(claims.Subject)
	if err != nil {
		return LoginResult{}, ErrInvalidMFAToken
	}
	u, err := s.q.GetUserByID(ctx, uid)
	if err != nil || claims.Ver != u.TokenVersion {
		return LoginResult{}, ErrInvalidMFAToken
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return LoginResult{}, err
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)
	// Reserve the challenge in the same transaction as the backup-code consume.
	// A wrong code rolls back both; concurrent completions cannot burn extra codes.
	consumed, err := consumeChallenge(ctx, qtx, claims.Pur+":"+claims.ID, claims.ExpiresAt.Time)
	if err != nil {
		return LoginResult{}, err
	}
	if !consumed {
		return LoginResult{}, ErrInvalidMFAToken
	}
	method := "totp"
	if !s.verifyTOTPWithQueries(ctx, qtx, uid, code) {
		if !consumeBackupCodeWithQueries(ctx, qtx, uid, code) {
			return LoginResult{}, ErrInvalidTOTPCode
		}
		method = "backup"
	}
	if err := tx.Commit(ctx); err != nil {
		return LoginResult{}, err
	}
	// Sign the version we verified above, never reload a newer generation here.
	token, err := s.issueFor(u)
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{Token: token, User: u, Method: method}, nil
}

// verifyTOTP reports whether code matches the user's enabled TOTP secret (±1 period skew).
func (s *Service) verifyTOTP(ctx context.Context, uid pgtype.UUID, code string) bool {
	return s.verifyTOTPWithQueries(ctx, s.q, uid, code)
}

func (s *Service) verifyTOTPWithQueries(ctx context.Context, q *db.Queries, uid pgtype.UUID, code string) bool {
	row, err := q.GetUserTOTP(ctx, uid)
	if err != nil || !row.Enabled {
		return false
	}
	secret, err := s.cipher.Decrypt(row.Secret)
	if err != nil {
		return false
	}
	return totp.Validate(code, secret)
}

// consumeBackupCode matches code against the user's unused backup codes and, on a hit,
// marks it used (one-time) and returns true.
func (s *Service) consumeBackupCode(ctx context.Context, uid pgtype.UUID, code string) bool {
	return consumeBackupCodeWithQueries(ctx, s.q, uid, code)
}

func consumeBackupCodeWithQueries(ctx context.Context, q *db.Queries, uid pgtype.UUID, code string) bool {
	norm := normalizeBackupCode(code)
	if norm == "" {
		return false
	}
	rows, err := q.ListUnusedBackupCodes(ctx, uid)
	if err != nil {
		return false
	}
	for _, r := range rows {
		if ok, _ := VerifyPassword(norm, r.CodeHash); ok {
			rows, err := q.MarkBackupCodeUsed(ctx, r.ID)
			return err == nil && rows == 1
		}
	}
	return false
}

// newBackupCode returns a human-friendly one-time code ("xxxx-xxxx") and the argon2id
// hash of its normalized form (dashes stripped, lower-case).
func newBackupCode() (plain, hash string, err error) {
	b := make([]byte, 5) // 40 bits → 8 base32 chars
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	raw := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
	plain = raw[:4] + "-" + raw[4:8]
	hash, err = HashPassword(normalizeBackupCode(plain))
	return plain, hash, err
}

// normalizeBackupCode strips dashes/whitespace and lower-cases, so display formatting
// does not affect verification.
func normalizeBackupCode(code string) string {
	return strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(code)))
}
