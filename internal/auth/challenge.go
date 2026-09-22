package auth

import (
	"context"
	"time"

	"discodrive/internal/db"
	"github.com/jackc/pgx/v5/pgtype"
)

// consumeChallenge records a successful, cryptographically verified challenge.
// The signed purpose and random ID (not the JWT serialization) identify a challenge.
// Its unique hash is shared across replicas and restarts. INSERT ... ON CONFLICT
// admits exactly one winner; expiry is checked inside that same statement so GC
// cannot make an expired, previously consumed challenge usable again.
func consumeChallenge(ctx context.Context, q *db.Queries, challengeID string, expires time.Time) (bool, error) {
	n, err := q.ConsumeAuthChallenge(ctx, db.ConsumeAuthChallengeParams{
		TokenHash: tokenHash(challengeID),
		ExpiresAt: pgtype.Timestamptz{Time: expires, Valid: true},
	})
	return n == 1, err
}
