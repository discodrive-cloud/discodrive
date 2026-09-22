package auth

import (
	"context"
	"errors"

	"discodrive/internal/db"
)

// ErrStreamToken covers every stream-token rejection. Callers map it to 401 without
// distinguishing causes: the response must not reveal whether a guessed token was
// expired, mis-scoped or revoked.
var ErrStreamToken = errors.New("auth: invalid stream token")

// ValidateStreamToken checks a purpose=stream-v2 token against a node ID and returns the
// user it was minted for. The token alone is NOT enough to read the file: the stream
// handler must still run the live node access check under the returned user, so that
// share revocation and access loss take effect immediately, not at token expiry.
func (s *Service) ValidateStreamToken(ctx context.Context, tokenStr, nodeID string) (string, error) {
	claims, err := s.issuer.Parse(tokenStr)
	if err != nil {
		return "", ErrStreamToken
	}
	// A session JWT pasted into ?t= must not work: URLs leak (logs, history), and a
	// leaked session is a far bigger prize than a leaked single-file token.
	// Legacy stream URLs lack device provenance and must be re-minted.
	if claims.Pur != "stream-v2" || claims.Nid == "" || claims.Nid != nodeID {
		return "", ErrStreamToken
	}
	uid, err := db.ParseUUID(claims.Subject)
	if err != nil {
		return "", ErrStreamToken
	}
	u, err := s.lookupUser(ctx, uid)
	if err != nil {
		return "", ErrStreamToken
	}
	// Same revocation semantics as sessions: a password change (token_version bump)
	// kills outstanding stream URLs too. Locked accounts don't stream either.
	if claims.Ver != u.TokenVersion || u.MustChangePassword {
		return "", ErrStreamToken
	}
	if claims.DeviceID != "" {
		did, err := db.ParseUUID(claims.DeviceID)
		if err != nil {
			return "", ErrStreamToken
		}
		device, err := s.q.GetDevice(ctx, did)
		if err != nil || device.UserID != uid || !device.TokenHash.Valid || device.TokenVersion != claims.Ver {
			return "", ErrStreamToken
		}
	}
	return claims.Subject, nil
}

// StreamMinter returns a per-request closure that mints stream tokens for one user.
// The user row is read once (token_version), then each node token is a pure HMAC
// signing — cheap enough to mint inline for every playable file in a folder listing.
func (s *Service) StreamMinter(ctx context.Context, userID string) (func(nodeID string) (string, error), error) {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return nil, err
	}
	u, err := s.lookupUser(ctx, uid)
	if err != nil {
		return nil, err
	}
	// A request authenticated just before password change must not mint URLs for
	// the new generation. Keep the original device binding too.
	if version, ok := TokenVersion(ctx); ok && (UserID(ctx) != userID || version != u.TokenVersion) {
		return nil, ErrStreamToken
	}
	deviceID := DeviceID(ctx)
	return func(nodeID string) (string, error) {
		return s.issuer.IssueStream(userID, nodeID, u.TokenVersion, deviceID)
	}, nil
}
