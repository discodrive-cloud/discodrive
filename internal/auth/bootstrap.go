package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"discodrive/internal/db"
)

// SetupNeeded reads the durable latch, never the current number of admins.
func (s *Service) SetupNeeded(ctx context.Context) (bool, error) {
	state, err := s.q.GetBootstrap(ctx)
	return !state.Completed, err
}

// InitBootstrap runs before accepting requests. The row lock also serializes
// initialization by multiple server processes. Only the digest enters the DB.
func (s *Service) InitBootstrap(ctx context.Context, tokenFile string) error {
	if tokenFile == "" {
		return errors.New("setup token file path is required")
	}
	path, err := filepath.Abs(tokenFile)
	if err != nil {
		return err
	}
	s.setupTokenFile = path
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	state, err := q.LockBootstrap(ctx)
	if err != nil {
		return err
	}
	admins, err := q.CountAdmins(ctx)
	if err != nil {
		return err
	}
	if state.Completed || admins > 0 {
		if err = q.CompleteBootstrap(ctx); err != nil {
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		s.removeSetupToken()
		return nil
	}
	token, created, err := readOrCreateSetupToken(path)
	if err != nil {
		return err
	}
	digest := TokenHash(token)
	if !created && state.TokenHash != "" && subtle.ConstantTimeCompare([]byte(state.TokenHash), []byte(digest)) != 1 {
		return errors.New("setup token file does not match database; remove the local token file and restart to rotate it")
	}
	if err = q.SetBootstrapTokenHash(ctx, digest); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func readOrCreateSetupToken(path string) (string, bool, error) {
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return "", false, errors.New("setup token must be a regular file with mode 0600")
		}
		f, err := os.Open(path)
		if err != nil {
			return "", false, err
		}
		defer f.Close()
		current, err := f.Stat()
		if err != nil {
			return "", false, err
		}
		if !os.SameFile(info, current) {
			return "", false, errors.New("setup token file changed while opening")
		}
		data, err := io.ReadAll(io.LimitReader(f, 128))
		if err != nil {
			return "", false, err
		}
		token := strings.TrimSpace(string(data))
		raw, err := hex.DecodeString(token)
		if err != nil || len(raw) != 32 {
			return "", false, errors.New("invalid setup token file")
		}
		return token, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", false, err
	}
	var raw [32]byte
	if _, err = rand.Read(raw[:]); err != nil {
		return "", false, err
	}
	token := hex.EncodeToString(raw[:])
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", false, err
	}
	_, writeErr := io.WriteString(f, token+"\n")
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return "", false, fmt.Errorf("write setup token: %w", errors.Join(writeErr, closeErr))
	}
	return token, true, nil
}

func (s *Service) removeSetupToken() {
	if s.setupTokenFile == "" {
		return
	}
	if err := os.Remove(s.setupTokenFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		// The DB latch and cleared digest have already revoked it. Report cleanup
		// failure without returning an ambiguous failure for a committed admin.
		log.Printf("discodrive: remove revoked setup token file: %v", err)
	}
}

// SetupAdmin verifies the secret and creates the first admin under the same row
// lock as the permanent completion latch. A failed transaction consumes nothing.
func (s *Service) SetupAdmin(ctx context.Context, email, password, token string) (db.User, error) {
	if len(token) != 64 {
		return db.User{}, ErrSetupToken
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return db.User{}, err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	state, err := q.LockBootstrap(ctx)
	if err != nil {
		return db.User{}, err
	}
	if state.Completed {
		return db.User{}, ErrAdminExists
	}
	if state.TokenHash == "" || subtle.ConstantTimeCompare([]byte(state.TokenHash), []byte(TokenHash(token))) != 1 {
		return db.User{}, ErrSetupToken
	}
	admins, err := q.CountAdmins(ctx)
	if err != nil {
		return db.User{}, err
	}
	if admins > 0 {
		if err = q.CompleteBootstrap(ctx); err != nil {
			return db.User{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return db.User{}, err
		}
		s.removeSetupToken()
		return db.User{}, ErrAdminExists
	}
	hash, err := HashPassword(password)
	if err != nil {
		return db.User{}, err
	}
	tenant, err := q.CreateTenant(ctx, "admin")
	if err != nil {
		return db.User{}, err
	}
	user, err := q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: email, PasswordHash: hash, Role: "admin", StorageQuota: s.quotaFor(nil, "admin")})
	if err != nil {
		return db.User{}, err
	}
	if err = q.CompleteBootstrap(ctx); err != nil {
		return db.User{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return db.User{}, err
	}
	s.removeSetupToken()
	return user, nil
}
