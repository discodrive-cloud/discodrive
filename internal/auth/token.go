package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims is the JWT payload. Subject = user_id.
type Claims struct {
	TenantID string `json:"tid"`
	Role     string `json:"role"`
	// Ver is the token version at issue time (users.token_version). A password change
	// increments the counter in the DB, causing old tokens to stop matching → 401.
	Ver int64 `json:"ver"`
	// DeviceID is set for tokens issued to a device (sync daemon): the devices row ID.
	// Empty for web sessions. Middleware checks whether the device is still alive → instant revocation.
	DeviceID string `json:"did,omitempty"`
	// Pur is the token purpose. Empty for full session tokens (back-compatible).
	// "mfa" marks a short-lived intermediate token: password proven, second factor pending.
	// "stream-v2" marks a URL-carried media token scoped to a single node (Nid).
	// The main middleware rejects any token with a non-empty purpose.
	Pur string `json:"pur,omitempty"`
	// Nid is set only on purpose=stream-v2 tokens: the single node the token may read.
	Nid string `json:"nid,omitempty"`
	// Reject ceremony tokens issued before purpose separation as well.
	LegacyWebAuthn string `json:"was,omitempty"`
	jwt.RegisteredClaims
}

// TokenIssuer issues and validates JWTs (HS256, short TTL).
type TokenIssuer struct {
	secret []byte
	ttl    time.Duration
}

func NewTokenIssuer(secret string, ttl time.Duration) *TokenIssuer {
	return &TokenIssuer{secret: []byte(secret), ttl: ttl}
}

// Issue issues a JWT with the issuer's default TTL. deviceID is non-empty only for
// sync-device tokens (daemon); pass "" for web sessions.
func (t *TokenIssuer) Issue(userID, tenantID, role string, ver int64, deviceID string) (string, error) {
	return t.IssueTTL(userID, tenantID, role, ver, deviceID, t.ttl)
}

// IssueTTL is Issue with an explicit lifetime — the session length the user chose for
// themselves (users.session_ttl_minutes). ttl <= 0 issues a token with no exp claim at
// all: the user asked never to be signed out. Such a token is still revocable, since
// the middleware re-checks users.token_version (bumped by a password change), the role
// and, for devices, whether the device still exists on every single request.
func (t *TokenIssuer) IssueTTL(userID, tenantID, role string, ver int64, deviceID string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		TenantID: tenantID,
		Role:     role,
		Ver:      ver,
		DeviceID: deviceID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:  userID,
			IssuedAt: jwt.NewNumericDate(now),
		},
	}
	if ttl > 0 {
		claims.ExpiresAt = jwt.NewNumericDate(now.Add(ttl))
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
}

// SessionTTL converts users.session_ttl_minutes into a duration. 0 (never expires) stays
// 0, which IssueTTL reads as "no exp claim".
func SessionTTL(minutes int32) time.Duration {
	if minutes <= 0 {
		return 0
	}
	return time.Duration(minutes) * time.Minute
}

// mfaTokenTTL bounds the window to complete a second factor after the password step.
const mfaTokenTTL = 5 * time.Minute

// IssueMFA issues a short-lived intermediate token (purpose=mfa) for the
// password-proven-but-second-factor-pending state. It grants no access on its own:
// the main middleware rejects it; only the /auth/mfa/* completion handlers accept it (A.3/A.5).
func (t *TokenIssuer) IssueMFA(userID, tenantID string, ver int64) (string, error) {
	id, err := newDeviceCode()
	if err != nil {
		return "", err
	}
	now := time.Now()
	claims := Claims{
		TenantID: tenantID,
		Pur:      "mfa",
		Ver:      ver,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        id,
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(mfaTokenTTL)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
}

// streamTokenTTL bounds how long a minted stream URL stays valid. Long enough to
// play a full track with pauses; expired URLs are re-minted by the player via the
// media-listing endpoint, so a short TTL costs one extra request, not a broken player.
const streamTokenTTL = time.Hour

// IssueStream issues a purpose=stream-v2 token scoped to a single node. It grants no
// session access (the main middleware rejects non-empty purposes); only the stream
// endpoint accepts it, and that endpoint re-checks node access on every request.
func (t *TokenIssuer) IssueStream(userID, nodeID string, ver int64, deviceID string) (string, error) {
	now := time.Now()
	claims := Claims{
		Ver:      ver,
		Pur:      "stream-v2",
		DeviceID: deviceID,
		Nid:      nodeID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(streamTokenTTL)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
}

// waSessionClaims carries a marshaled webauthn.SessionData between the begin and finish
// steps of a WebAuthn ceremony, signed so the client cannot tamper with the challenge.
type waSessionClaims struct {
	Pur      string `json:"pur"`
	Data     string `json:"was"`
	Ver      int64  `json:"ver"`
	Action   string `json:"action,omitempty"`
	Approval string `json:"approval,omitempty"`
	jwt.RegisteredClaims
}

// IssueWebAuthnSession signs a short-lived token (5 min) carrying base64 SessionData for
// the given user. Successful ceremonies are consumed in the shared database.
func (t *TokenIssuer) IssueWebAuthnSession(userID, data string, ver int64) (string, error) {
	purpose := "webauthn-login"
	if userID != "" {
		purpose = "webauthn-register"
	}
	return t.issueWACeremony(waSessionClaims{Pur: purpose, Data: data, Ver: ver, RegisteredClaims: jwt.RegisteredClaims{Subject: userID}})
}

func (t *TokenIssuer) issueWACeremony(claims waSessionClaims) (string, error) {
	id, err := newDeviceCode()
	if err != nil {
		return "", err
	}
	now := time.Now()
	claims.ID = id
	claims.IssuedAt = jwt.NewNumericDate(now)
	claims.ExpiresAt = jwt.NewNumericDate(now.Add(mfaTokenTTL))
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
}

// ParseWebAuthnSession validates the token and returns the subject and the base64 SessionData.
func (t *TokenIssuer) ParseWebAuthnSession(tokenStr string) (userID, data string, err error) {
	claims, err := t.parseWebAuthnSession(tokenStr)
	if err != nil {
		return "", "", err
	}
	return claims.Subject, claims.Data, nil
}

func (t *TokenIssuer) parseWebAuthnSession(tokenStr string) (*waSessionClaims, error) {
	claims := &waSessionClaims{}
	if _, err := jwt.ParseWithClaims(tokenStr, claims, func(tok *jwt.Token) (any, error) {
		if _, ok := tok.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected JWT signing method")
		}
		return t.secret, nil
	}); err != nil {
		return nil, err
	}
	expected := "webauthn-login"
	if claims.Subject != "" {
		expected = "webauthn-register"
	}
	if (claims.Pur != expected && claims.Pur != "webauthn-approval") || claims.Data == "" || claims.ID == "" || claims.ExpiresAt == nil {
		return nil, errors.New("invalid WebAuthn token purpose")
	}
	return claims, nil
}

func (t *TokenIssuer) Parse(tokenStr string) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(tokenStr, claims, func(tok *jwt.Token) (any, error) {
		if _, ok := tok.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected JWT signing method")
		}
		return t.secret, nil
	})
	if err != nil {
		return nil, err
	}
	if claims.LegacyWebAuthn != "" {
		return nil, errors.New("WebAuthn ceremony is not an access token")
	}
	return claims, nil
}
