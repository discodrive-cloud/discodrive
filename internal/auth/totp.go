package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pquerna/otp"
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
// encryption key and a fresh identity proof; an already-enabled 2FA must be disabled
// before re-enrolling. The same proof must be presented when confirming the secret.
func (s *Service) SetupTOTP(ctx context.Context, userID string, approvals ...string) (otpauthURL, secret string, err error) {
	if len(approvals) != 1 {
		return "", "", ErrApproval
	}
	if s.cipher == nil || !s.cipher.Enabled() {
		return "", "", ErrTOTPNotConfigured
	}
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return "", "", err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	u, err := q.GetUserForAuthUpdate(ctx, uid)
	if err != nil {
		return "", "", err
	}
	c, err := s.approval(approvals[0], userID, "totp:setup", u.TokenVersion)
	if err != nil {
		return "", "", err
	}
	if ver, ok := TokenVersion(ctx); ok && (UserID(ctx) != userID || ver != u.TokenVersion) {
		return "", "", ErrApproval
	}
	// Serialize setup, confirmation, disable and backup regeneration on the user.
	if existing, err := q.GetUserTOTP(ctx, uid); err == nil && existing.Enabled {
		return "", "", ErrTOTPAlreadyOn
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", "", err
	}
	consumed, err := consumeChallenge(ctx, q, "totp-setup-start:"+c.ID, c.ExpiresAt.Time)
	if err != nil {
		return "", "", err
	}
	if !consumed {
		return "", "", ErrApproval
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: totpIssuer, AccountName: u.Email})
	if err != nil {
		return "", "", err
	}
	enc, err := s.cipher.Encrypt(key.Secret())
	if err != nil {
		return "", "", err
	}
	n, err := q.StartApprovedTOTP(ctx, db.StartApprovedTOTPParams{UserID: uid, Secret: enc, ApprovalID: c.ID})
	if err != nil {
		return "", "", err
	}
	if n != 1 {
		return "", "", ErrTOTPAlreadyOn
	}
	if err := tx.Commit(ctx); err != nil {
		return "", "", err
	}
	return key.URL(), key.Secret(), nil
}

// ConfirmTOTP enables exactly the enrollment authorized by the short-lived proof.
// A failed code leaves the proof available; success consumes it with the backup codes.
func (s *Service) ConfirmTOTP(ctx context.Context, userID, code string, approvals ...string) ([]string, error) {
	if len(approvals) != 1 {
		return nil, ErrApproval
	}
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	u, err := q.GetUserForAuthUpdate(ctx, uid)
	if err != nil {
		return nil, err
	}
	c, err := s.approval(approvals[0], userID, "totp:setup", u.TokenVersion)
	if err != nil {
		return nil, err
	}
	if ver, ok := TokenVersion(ctx); ok && (UserID(ctx) != userID || ver != u.TokenVersion) {
		return nil, ErrApproval
	}
	row, err := q.GetUserTOTP(ctx, uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTOTPNotEnabled
	}
	if err != nil {
		return nil, err
	}
	if row.Enabled {
		return nil, ErrTOTPAlreadyOn
	}
	if row.ApprovalID != c.ID {
		return nil, ErrApproval
	}
	secret, err := s.cipher.Decrypt(row.Secret)
	if err != nil {
		return nil, err
	}
	if ok, err := claimTOTPCode(ctx, q, uid, code, secret); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrInvalidTOTPCode
	}
	consumed, err := consumeChallenge(ctx, q, c.Pur+":"+c.ID, c.ExpiresAt.Time)
	if err != nil {
		return nil, err
	}
	if !consumed {
		return nil, ErrApproval
	}
	n, err := q.ConfirmApprovedTOTP(ctx, db.ConfirmApprovedTOTPParams{UserID: uid, ApprovalID: c.ID, Secret: row.Secret})
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, ErrApproval
	}
	codes, err := issueBackupCodes(ctx, q, uid)
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
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	u, err := q.GetUserForAuthUpdate(ctx, uid)
	if err != nil {
		return nil, err
	}
	if ver, ok := TokenVersion(ctx); ok && (UserID(ctx) != userID || ver != u.TokenVersion) {
		return nil, ErrApproval
	}
	if !s.verifyTOTPWithQueries(ctx, q, uid, code) {
		return nil, ErrInvalidTOTPCode
	}
	codes, err := issueBackupCodes(ctx, q, uid)
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
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)
	u, err := qtx.GetUserForAuthUpdate(ctx, uid)
	if err != nil {
		return err
	}
	if ver, ok := TokenVersion(ctx); ok && (UserID(ctx) != userID || ver != u.TokenVersion) {
		return ErrApproval
	}
	ok, err := VerifyPassword(password, u.PasswordHash)
	if err != nil {
		return err
	}
	if !ok {
		return ErrInvalidCreds
	}
	row, err := qtx.GetUserTOTP(ctx, uid)
	if err != nil || !row.Enabled {
		return ErrTOTPNotEnabled
	}
	secret, err := s.cipher.Decrypt(row.Secret)
	if err != nil {
		return err
	}
	if ok, err := claimTOTPCode(ctx, qtx, uid, code, secret); err != nil {
		return err
	} else if !ok {
		return ErrInvalidTOTPCode
	}

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
	token, err := s.issueFor(ctx, u)
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
	ok, err := claimTOTPCode(ctx, q, uid, code, secret)
	return err == nil && ok
}

// totpNow is the clock TOTP codes are checked against; tests move it forward.
var totpNow = time.Now

// totpOpts are totp.Validate's defaults: 30 s steps, ±1 step of skew, 6 SHA-1 digits.
var totpOpts = totp.ValidateOpts{Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1}

// totpStep returns the time step code is valid for (within the skew), or false.
func totpStep(code, secret string, now time.Time) (int64, bool) {
	if len(code) != int(totpOpts.Digits) {
		return 0, false
	}
	step := now.Unix() / int64(totpOpts.Period)
	for _, d := range []int64{0, -1, 1} {
		at := time.Unix((step+d)*int64(totpOpts.Period), 0)
		want, err := totp.GenerateCodeCustom(secret, at, totpOpts)
		if err == nil && subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return step + d, true
		}
	}
	return 0, false
}

// claimTOTPCode accepts code only if it is valid now and its time step comes after the
// last accepted one, recording the step through q (the caller's transaction, if any).
// A valid code thus works once: an observed or phished code cannot be replayed for the
// ~90 s it stays valid, not even by a concurrent request.
func claimTOTPCode(ctx context.Context, q *db.Queries, uid pgtype.UUID, code, secret string) (bool, error) {
	step, ok := totpStep(code, secret, totpNow())
	if !ok {
		return false, nil
	}
	n, err := q.ClaimTOTPStep(ctx, db.ClaimTOTPStepParams{UserID: uid, Step: step})
	if err != nil {
		return false, err
	}
	return n == 1, nil
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
