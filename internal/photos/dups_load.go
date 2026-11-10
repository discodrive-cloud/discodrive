package photos

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

// LoadDuplicateGroups finds the user's duplicate groups with the default thresholds.
func LoadDuplicateGroups(ctx context.Context, q db.Querier, userID pgtype.UUID) ([][]DupCandidate, error) {
	return LoadDuplicateGroupsWith(ctx, q, userID, DefaultDupParams)
}

// LoadDuplicateGroupsWith loads the user's candidate cards and dismissed pairs and groups them.
func LoadDuplicateGroupsWith(ctx context.Context, q db.Querier, userID pgtype.UUID, p DupParams) ([][]DupCandidate, error) {
	rows, err := q.ListDupCandidates(ctx, userID)
	if err != nil {
		return nil, err
	}
	cands := make([]DupCandidate, 0, len(rows))
	for _, r := range rows {
		cands = append(cands, dupCandidateOf(r))
	}
	dis, err := q.ListDupDismissed(ctx, userID)
	if err != nil {
		return nil, err
	}
	dismissed := make(map[DupPair]bool, len(dis))
	for _, d := range dis {
		dismissed[MakeDupPair(d.A, d.B)] = true
	}
	return GroupDuplicatesWith(p, cands, dismissed), nil
}

// dupCandidateOf turns a candidate row into a DupCandidate, make and model normalised.
func dupCandidateOf(r db.ListDupCandidatesRow) DupCandidate {
	return DupCandidate{
		ID:         r.ID,
		Key:        r.Key,
		Kind:       r.Kind,
		TakenAt:    r.TakenAt.Time,
		TakenLocal: r.TakenLocal.Time,
		FileDated:  r.DateSource == "file",
		ExifDated:  r.DateSource == "exif",
		Make:       NormCamera(r.Make.String),
		Model:      NormCamera(r.Model.String),
		W:          r.Width.Int32,
		H:          r.Height.Int32,
		DurationMs: r.DurationMs.Int64,
		Hash:       uint64(r.Phash.Int64),
		Name:       r.Name,
		Bytes:      r.TotalBytes,
		HasKids:    r.HasKids,
		IndexedAt:  r.IndexedAt.Time,
		DiskPath:   r.DiskPath.String,
		Copies:     int(r.Copies),
	}
}

// MutualDuplicates reports whether the contents named by keys are, right now, duplicates
// of one another: each has a usable card, every two of them pass IsDup, and no pair among
// them was dismissed. That is what the members of one duplicate group are (the grouping is
// complete-linkage), and it is checked from these cards alone — a resolve or a dismiss
// does not need a pass over the whole library to know its cards belong together.
func MutualDuplicates(ctx context.Context, q db.Querier, userID pgtype.UUID, keys []string) (bool, error) {
	if len(keys) < 2 {
		return false, nil
	}
	rows, err := q.ListDupCandidatesByKeys(ctx, db.ListDupCandidatesByKeysParams{UserID: userID, Keys: keys})
	if err != nil {
		return false, err
	}
	if len(rows) != len(keys) {
		return false, nil // a content without a ready, hashed card is in no group
	}
	dis, err := q.ListDupDismissedAmong(ctx, db.ListDupDismissedAmongParams{UserID: userID, Keys: keys})
	if err != nil {
		return false, err
	}
	dismissed := make(map[DupPair]bool, len(dis))
	for _, d := range dis {
		dismissed[MakeDupPair(d.A, d.B)] = true
	}
	cands := make([]DupCandidate, len(rows))
	for i, r := range rows {
		cands[i] = dupCandidateOf(db.ListDupCandidatesRow(r))
	}
	for i := range cands {
		for j := i + 1; j < len(cands); j++ {
			if dismissed[MakeDupPair(cands[i].Key, cands[j].Key)] || !IsDup(cands[i], cands[j]) {
				return false, nil
			}
		}
	}
	return true, nil
}
