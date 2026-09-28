package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/rescan"
)

// runRescan queues a reconciliation for the running server: `server rescan
// [--user email] [--wait]`. It never reconciles by itself — only the server knows which
// paths uploads are changing right now.
func runRescan(ctx context.Context, q *db.Queries, args []string, out io.Writer, poll time.Duration) int {
	flags := flag.NewFlagSet("rescan", flag.ContinueOnError)
	flags.SetOutput(out)
	email := flags.String("user", "", "reconcile only this user (email); default: every user")
	wait := flags.Bool("wait", false, "wait until the server has finished and print the outcome")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	var uid pgtype.UUID
	if *email != "" {
		u, err := q.GetUserByEmail(ctx, *email)
		if errors.Is(err, pgx.ErrNoRows) {
			fmt.Fprintf(out, "no user with email %s\n", *email)
			return 1
		} else if err != nil {
			fmt.Fprintf(out, "looking up the user: %v\n", err)
			return 1
		}
		uid = u.ID
	}
	id, err := rescan.Enqueue(ctx, q, uid, "cli")
	if err != nil {
		fmt.Fprintf(out, "queueing: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "rescan #%d queued; the running server executes it (or the server, when it next starts)\n", id)
	if !*wait {
		return 0
	}
	for {
		req, err := q.GetRescanRequest(ctx, id)
		if err != nil {
			fmt.Fprintf(out, "reading status: %v\n", err)
			return 1
		}
		if req.FinishedAt.Valid {
			fmt.Fprintf(out, "done: imported %d, missing %d, changed %d, errors %d\n",
				req.Imported, req.Missing, req.Changed, req.Errors)
			if req.ErrorText.Valid {
				fmt.Fprintln(out, req.ErrorText.String)
			}
			if req.Errors > 0 {
				return 1
			}
			return 0
		}
		select {
		case <-ctx.Done():
			return 1
		case <-time.After(poll):
		}
	}
}
