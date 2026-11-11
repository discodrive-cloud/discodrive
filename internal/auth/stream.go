package auth

import (
	"context"
	"errors"
	"github.com/golang-jwt/jwt/v5"

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
	claims, _, err := s.validateScoped(ctx, tokenStr, purposeStream, nodeID)
	if err != nil {
		return "", err
	}
	return claims.Subject, nil
}

// validateScoped checks a URL-carried token of one purpose against the one thing it is
// for (a node for a stream token, a request path for a download ticket) and against the
// session or device it was minted by, which has to be alive still.
func (s *Service) validateScoped(ctx context.Context, tokenStr, purpose, scope string) (*Claims, db.User, error) {
	claims, err := s.issuer.Parse(tokenStr)
	if err != nil {
		return nil, db.User{}, ErrStreamToken
	}
	// A session JWT pasted into the URL must not work: URLs leak (logs, history), and a
	// leaked session is a far bigger prize than a leaked single-file token.
	// Legacy URLs lacking device/browser provenance must be re-minted.
	if claims.Pur != purpose || claims.Nid == "" || claims.Nid != scope {
		return nil, db.User{}, ErrStreamToken
	}
	uid, err := db.ParseUUID(claims.Subject)
	if err != nil {
		return nil, db.User{}, ErrStreamToken
	}
	u, err := s.lookupUser(ctx, uid)
	if err != nil {
		return nil, db.User{}, ErrStreamToken
	}
	// Same revocation semantics as sessions: a password change (token_version bump)
	// kills outstanding URLs too. Locked accounts get nothing either.
	if claims.Ver != u.TokenVersion || u.MustChangePassword {
		return nil, db.User{}, ErrStreamToken
	}
	if claims.DeviceID != "" {
		did, err := db.ParseUUID(claims.DeviceID)
		if err != nil {
			return nil, db.User{}, ErrStreamToken
		}
		device, err := s.q.GetDevice(ctx, did)
		if err != nil || device.UserID != uid || !device.TokenHash.Valid || device.TokenVersion != claims.Ver {
			return nil, db.User{}, ErrStreamToken
		}
	}
	if claims.DeviceID == "" && (claims.SessionID == "" || s.sessionActive == nil || !s.sessionActive(ctx, claims)) {
		return nil, db.User{}, ErrStreamToken
	}
	return claims, u, nil
}

// StreamMinter returns a per-request closure that mints stream tokens for one user.
// The user row is read once (token_version), then each node token is a pure HMAC
// signing — cheap enough to mint inline for every playable file in a folder listing.
func (s *Service) StreamMinter(ctx context.Context, userID string) (func(nodeID string) (string, error), error) {
	return s.scopedMinter(ctx, userID, purposeStream)
}

// DownloadTicket mints a ticket for one GET of one path as the session of ctx (see
// MiddlewareOrTicket). Like a stream token it names the user and nothing else: the
// route's own access checks run on every use.
func (s *Service) DownloadTicket(ctx context.Context, path string) (string, error) {
	mint, err := s.scopedMinter(ctx, UserID(ctx), purposeDownload)
	if err != nil {
		return "", err
	}
	return mint(path)
}

func (s *Service) scopedMinter(ctx context.Context, userID, purpose string) (func(scope string) (string, error), error) {
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
	sessionID := SessionID(ctx)
	if UserID(ctx) != userID || (deviceID == "" && (sessionID == "" || s.sessionActive == nil || !s.sessionActive(ctx, &Claims{SessionID: sessionID, Ver: u.TokenVersion, RegisteredClaims: jwt.RegisteredClaims{Subject: userID}}))) {
		return nil, ErrStreamToken
	}
	return func(scope string) (string, error) {
		return s.issuer.issueScoped(purpose, userID, scope, u.TokenVersion, deviceID, sessionID)
	}, nil
}
