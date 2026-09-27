package auth

import (
	"context"
	"discodrive/internal/db"
)

func (s *Service) createBrowserSession(ctx context.Context, u db.User) (string, error) {
	id, err := newDeviceCode()
	if err != nil {
		return "", err
	}
	if err := s.q.CreateBrowserSession(ctx, db.CreateBrowserSessionParams{ID: id, UserID: u.ID, TokenVersion: u.TokenVersion}); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Service) browserSessionActive(ctx context.Context, c *Claims) bool {
	if c.SessionID == "" {
		return false
	}
	uid, err := db.ParseUUID(c.Subject)
	if err != nil {
		return false
	}
	ok, err := s.q.BrowserSessionActive(ctx, db.BrowserSessionActiveParams{ID: c.SessionID, UserID: uid, TokenVersion: c.Ver})
	return err == nil && ok
}

func SessionID(ctx context.Context) string { v, _ := ctx.Value(ctxSessionID).(string); return v }

// Logout removes authority rather than blacklisting a particular JWT encoding.
// Renewals keep the same session ID and cannot recreate a deleted row.
func (s *Service) Logout(ctx context.Context, token string) error {
	claims, err := s.issuer.parseLogout(token)
	if err != nil {
		return ErrInvalidCreds
	}
	uid, err := db.ParseUUID(claims.Subject)
	if err != nil {
		return ErrInvalidCreds
	}
	return s.q.DeleteBrowserSession(ctx, db.DeleteBrowserSessionParams{ID: claims.SessionID, UserID: uid})
}
