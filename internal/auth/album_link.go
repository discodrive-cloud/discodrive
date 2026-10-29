package auth

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// albumLinkTTL is how long a password once given opens an album link.
const albumLinkTTL = 12 * time.Hour

// IssueAlbumLink gives the visitor of a password-protected album link a token for that link
// alone. It is carried in the URL of every picture and video (an <img> sends no headers),
// so it names the link and nothing else; deleting the link shuts it at once, since each
// request looks the link up again.
func (s *Service) IssueAlbumLink(linkID string) (string, error) {
	now := time.Now()
	claims := Claims{Pur: "album-link", RegisteredClaims: jwt.RegisteredClaims{
		Subject: linkID, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(albumLinkTTL))}}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.issuer.secret)
}

// ValidAlbumLink reports whether token opens the link.
func (s *Service) ValidAlbumLink(linkID, token string) bool {
	if token == "" {
		return false
	}
	c, err := s.issuer.Parse(token)
	return err == nil && c.Pur == "album-link" && c.Subject == linkID && c.ExpiresAt != nil
}
