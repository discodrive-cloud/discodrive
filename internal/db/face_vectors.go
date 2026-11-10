package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
)

// EachUserFaceVector is ListUserFaceVectors read row by row: fn gets each face as it
// arrives; Embedding is only valid during the call (it is the driver's row buffer). The
// generated method returns all rows at once — for a big library the raw vectors alone
// are hundreds of megabytes, held while the caller converts them into what it works with.
func (q *Queries) EachUserFaceVector(ctx context.Context, arg ListUserFaceVectorsParams, fn func(ListUserFaceVectorsRow) error) error {
	rows, err := q.db.Query(ctx, listUserFaceVectors, arg.UserID, arg.Model)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r ListUserFaceVectorsRow
		var embedding pgtype.DriverBytes
		if err := rows.Scan(&r.ID, &r.PersonID, &embedding, &r.Clustered); err != nil {
			return err
		}
		r.Embedding = embedding
		if err := fn(r); err != nil {
			return err
		}
	}
	return rows.Err()
}
