package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5"

	"discodrive/internal/db"
	"discodrive/internal/photos"
)

const dupsUsage = `usage: server dups --user <email> [--max-timed N] [--max-untimed N]
  read-only: prints the groups of duplicate photos the finder would offer, with the
  Hamming distance of every copy to the first one (the one kept); writes nothing
`

// runDups is `server dups --user <email>`: it lists duplicate groups and changes nothing.
func runDups(ctx context.Context, q db.Querier, args []string, out io.Writer) int {
	p := photos.DefaultDupParams
	fs := flag.NewFlagSet("dups", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	email := fs.String("user", "", "")
	fs.IntVar(&p.MaxDistTimed, "max-timed", p.MaxDistTimed, "")
	fs.IntVar(&p.MaxDistUntimed, "max-untimed", p.MaxDistUntimed, "")
	if err := fs.Parse(args); err != nil || *email == "" || fs.NArg() != 0 {
		fmt.Fprint(out, dupsUsage)
		return 2
	}
	u, err := q.GetUserByEmail(ctx, *email)
	if errors.Is(err, pgx.ErrNoRows) {
		fmt.Fprintf(out, "no user with email %s\n", *email)
		return 1
	} else if err != nil {
		fmt.Fprintf(out, "looking up the user: %v\n", err)
		return 1
	}
	groups, err := photos.LoadDuplicateGroupsWith(ctx, q, u.ID, p)
	if err != nil {
		fmt.Fprintf(out, "loading groups: %v\n", err)
		return 1
	}
	var reclaim int64
	for i, g := range groups {
		var size int64
		for _, c := range g {
			size += c.Bytes
		}
		fmt.Fprintf(out, "group %d: %s, %d bytes\n", i+1, g[0].TakenAt.UTC().Format("2006-01-02 15:04:05"), size)
		for j, c := range g {
			mark := "  "
			if j == 0 {
				mark = "* "
			} else {
				reclaim += c.Bytes
			}
			fmt.Fprintf(out, "  %sd=%d %s %s %dx%d %d bytes", mark, photos.Hamming(g[0].Hash, c.Hash), c.Name, c.DiskPath, c.W, c.H, c.Bytes)
			if j == 0 {
				fmt.Fprint(out, " (keep)")
			}
			fmt.Fprintln(out)
		}
	}
	fmt.Fprintf(out, "total: %d groups, %d bytes reclaimable\n", len(groups), reclaim)
	return 0
}
