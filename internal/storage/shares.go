package storage

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

var (
	ErrNotOwner   = errors.New("only the owner can manage access")
	ErrShareInput = errors.New("invalid access parameters")
)

func validAccess(a string) bool { return a == "read" || a == "read_write" }

// ownerNode verifies that the node exists and belongs to ownerUser (only the owner
// may manage access).
func (s *FileService) ownerNode(ctx context.Context, ownerUser, nodeID string) (db.Node, error) {
	nid, err := db.ParseUUID(nodeID)
	if err != nil {
		return db.Node{}, ErrNotFound
	}
	node, err := s.q.GetNode(ctx, nid)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Node{}, ErrNotFound
	}
	if err != nil {
		return db.Node{}, err
	}
	if db.UUIDString(node.UserID) != ownerUser {
		return db.Node{}, ErrNotOwner
	}
	return node, nil
}

// ShareToUser grants a user (by email) access to a file_node.
func (s *FileService) ShareToUser(ctx context.Context, ownerUser, nodeID, withEmail, access string, expiresAt *time.Time) (db.ResourceShare, error) {
	sh, _, err := s.ShareToUserFresh(ctx, ownerUser, nodeID, withEmail, access, expiresAt)
	return sh, err
}

// ShareToUserFresh is ShareToUser that also reports whether the share is new. Sharing a
// node again with the same user updates the existing share (access, expiry) instead of
// adding another one, and must not notify them again.
func (s *FileService) ShareToUserFresh(ctx context.Context, ownerUser, nodeID, withEmail, access string, expiresAt *time.Time) (db.ResourceShare, bool, error) {
	if !validAccess(access) {
		return db.ResourceShare{}, false, ErrShareInput
	}
	node, err := s.ownerNode(ctx, ownerUser, nodeID)
	if err != nil {
		return db.ResourceShare{}, false, err
	}
	target, err := s.q.GetUserByEmail(ctx, withEmail)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.ResourceShare{}, false, ErrNotFound
	}
	if err != nil {
		return db.ResourceShare{}, false, err
	}
	existing, err := s.q.GetUserFileShare(ctx, db.GetUserFileShareParams{ResourceID: node.ID, SharedWithUser: target.ID})
	if err == nil {
		sh, err := s.q.UpdateShareAccess(ctx, db.UpdateShareAccessParams{ID: existing.ID, Access: access, ExpiresAt: tsPtr(expiresAt)})
		return sh, false, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return db.ResourceShare{}, false, err
	}
	sh, err := s.q.CreateShare(ctx, db.CreateShareParams{
		ResourceType:   "file_node",
		ResourceID:     node.ID,
		OwnerID:        node.UserID,
		SharedWithUser: target.ID,
		Access:         access,
		ExpiresAt:      tsPtr(expiresAt),
	})
	return sh, err == nil, err
}

// ShareByLink creates a public link for a file_node, returning the share and token.
func (s *FileService) ShareByLink(ctx context.Context, ownerUser, nodeID, access string, expiresAt *time.Time) (db.ResourceShare, string, error) {
	if !validAccess(access) {
		return db.ResourceShare{}, "", ErrShareInput
	}
	node, err := s.ownerNode(ctx, ownerUser, nodeID)
	if err != nil {
		return db.ResourceShare{}, "", err
	}
	token := randomHex() + randomHex()
	share, err := s.q.CreateShare(ctx, db.CreateShareParams{
		ResourceType:   "file_node",
		ResourceID:     node.ID,
		OwnerID:        node.UserID,
		ShareLinkToken: text(token),
		Access:         access,
		ExpiresAt:      tsPtr(expiresAt),
	})
	return share, token, err
}

// LeaveShare removes a share from the grantee's side — "remove from my view".
// Unlike Revoke (owner), here the right belongs to the person the share was granted to.
func (s *FileService) LeaveShare(ctx context.Context, granteeUser, shareID string) error {
	sid, err := db.ParseUUID(shareID)
	if err != nil {
		return ErrNotFound
	}
	share, err := s.q.GetShare(ctx, sid)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !share.SharedWithUser.Valid || db.UUIDString(share.SharedWithUser) != granteeUser {
		return ErrNotOwner
	}
	return s.q.DeleteShare(ctx, sid)
}

// Revoke revokes a share (owner only). Access is closed immediately.
func (s *FileService) Revoke(ctx context.Context, ownerUser, shareID string) error {
	sid, err := db.ParseUUID(shareID)
	if err != nil {
		return ErrNotFound
	}
	share, err := s.q.GetShare(ctx, sid)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if db.UUIDString(share.OwnerID) != ownerUser {
		return ErrNotOwner
	}
	return s.q.DeleteShare(ctx, sid)
}

// SharedWithUser returns the list of active shares granted to the user (for "shared with me").
func (s *FileService) SharedWithUser(ctx context.Context, userID string) ([]db.ResourceShare, error) {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return nil, ErrNotFound
	}
	return s.q.ListSharesForUser(ctx, uid)
}

// NodeByLink resolves a file node by an active (non-expired) link token.
func (s *FileService) NodeByLink(ctx context.Context, token string) (db.Node, error) {
	share, err := s.q.GetActiveShareByToken(ctx, token)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Node{}, ErrNotFound
	}
	if err != nil {
		return db.Node{}, err
	}
	node, err := s.q.GetNode(ctx, share.ResourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Node{}, ErrNotFound
	}
	if err != nil {
		return db.Node{}, err
	}
	if node.IsDir {
		return db.Node{}, ErrNotFound // link sharing for directories deferred to 0.8
	}
	return node, nil
}

// SharesForNode returns the outgoing shares for a node (owner only) — for management/revocation.
func (s *FileService) SharesForNode(ctx context.Context, ownerUser, nodeID string) ([]db.ResourceShare, error) {
	node, err := s.ownerNode(ctx, ownerUser, nodeID)
	if err != nil {
		return nil, err
	}
	return s.q.ListSharesForResource(ctx, db.ListSharesForResourceParams{
		ResourceType: "file_node", ResourceID: node.ID,
	})
}

func tsPtr(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}
