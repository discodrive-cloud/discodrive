package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5"

	"discodrive/internal/db"
	"discodrive/internal/photos"
	"discodrive/internal/photos/faces"
)

const facesUsage = `usage: server faces recluster|redetect <email>
  recluster  regroup the faces of persons the user neither named nor hid (after changing
             PHOTOS_FACES_MAX_DISTANCE or PHOTOS_FACES_MIN_FACES)
  redetect   look for faces on every photo again (after changing PHOTOS_FACES_MIN_SCORE or
             the model); the running server does it in the background, persons are kept
`

// runFaces is `server faces recluster|redetect <email>`.
func runFaces(ctx context.Context, q *db.Queries, args []string, out io.Writer, model string, p faces.Params) int {
	if len(args) != 2 || (args[0] != "recluster" && args[0] != "redetect") {
		fmt.Fprint(out, facesUsage)
		return 2
	}
	u, err := q.GetUserByEmail(ctx, args[1])
	if errors.Is(err, pgx.ErrNoRows) {
		fmt.Fprintf(out, "no user with email %s\n", args[1])
		return 1
	} else if err != nil {
		fmt.Fprintf(out, "looking up the user: %v\n", err)
		return 1
	}
	switch args[0] {
	case "recluster":
		// Only faces made by the model the server runs: others are not clustered (their
		// vectors do not compare) until the server looks at them again.
		if n, err := q.CountUserFacesOfModel(ctx, db.CountUserFacesOfModelParams{UserID: u.ID, Model: model}); err != nil {
			fmt.Fprintf(out, "counting faces: %v\n", err)
			return 1
		} else if n == 0 {
			fmt.Fprintf(out, "no faces of model %s for %s (is PHOTOS_ML_MODEL the server's?)\n", model, args[1])
			return 1
		}
		if err := q.UnassignUnnamedPersons(ctx, db.UnassignUnnamedPersonsParams{UserID: u.ID, Model: model}); err != nil {
			fmt.Fprintf(out, "releasing faces: %v\n", err)
			return 1
		}
		if err := photos.Cluster(ctx, q, u.ID, model, p, nil); err != nil {
			fmt.Fprintf(out, "clustering: %v\n", err)
			return 1
		}
		people, _ := q.ListPhotoPeople(ctx, db.ListPhotoPeopleParams{UserID: u.ID})
		fmt.Fprintf(out, "done: %d persons with photos\n", len(people))
	case "redetect":
		if err := q.ResetFaceState(ctx, u.ID); err != nil {
			fmt.Fprintf(out, "queueing: %v\n", err)
			return 1
		}
		fmt.Fprintln(out, "queued; the running server looks at every photo again (hours for a big library)")
	}
	return 0
}
