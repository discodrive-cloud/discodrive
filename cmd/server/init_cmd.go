package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"syscall"
	"time"
)

// serverUID/serverGID is the distroless "nonroot" account the server image runs as.
const serverUID, serverGID = 65532, 65532

// runInit prepares a Compose installation before the server and nginx start: `server
// init [-certs /certs] [-host a.example.com,localhost] [-data /data]`. It runs as root
// in a one-shot container, writes a self-signed TLS pair unless one exists and hands a
// fresh storage directory to the server account.
func runInit(args []string, out io.Writer, now time.Time) int {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(out)
	certs := flags.String("certs", "/certs", "directory for dev.pem and dev-key.pem")
	hosts := flags.String("host", "localhost", "comma-separated DNS names or IPs for the certificate")
	data := flags.String("data", "/data", "storage directory the server writes to")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if code := runGenCert([]string{"-dir", *certs, "-host", *hosts}, out, now); code != 0 {
		return code
	}
	return prepareStorage(*data, serverUID, serverGID, os.Chown, out)
}

// prepareStorage makes dir writable for uid:gid. It only ever chowns a directory that is
// missing or empty (a fresh clone ships data/.gitkeep): anything already in there belongs
// to someone, and silently re-owning it is not this command's call.
func prepareStorage(dir string, uid, gid int, chown func(string, int, int) error, out io.Writer) int {
	fi, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintf(out, "creating %s: %v\n", dir, err)
			return 1
		}
		fi, err = os.Stat(dir)
	}
	if err != nil {
		fmt.Fprintf(out, "checking %s: %v\n", dir, err)
		return 1
	}
	if writableBy(fi, uid, gid) {
		return 0
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintf(out, "reading %s: %v\n", dir, err)
		return 1
	}
	for _, e := range entries {
		if e.Name() != ".gitkeep" {
			fmt.Fprintf(out, "the server (uid %d) can't write to the storage directory and it already holds files, so it was left as is; "+
				"give it access on the host, e.g. sudo chown -R %d:%d ./data\n", uid, uid, gid)
			return 1
		}
	}
	if err := chown(dir, uid, gid); err != nil {
		fmt.Fprintf(out, "handing %s to uid %d: %v\n", dir, uid, err)
		return 1
	}
	fmt.Fprintf(out, "storage directory handed to the server account (uid %d)\n", uid)
	return 0
}

// writableBy reports whether uid:gid may create entries in the directory, judging by its
// owner and permission bits the way the kernel would (ACLs aside).
func writableBy(fi fs.FileInfo, uid, gid int) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return true
	}
	perm := fi.Mode().Perm()
	switch {
	case int(st.Uid) == uid:
		return perm&0o300 == 0o300
	case int(st.Gid) == gid:
		return perm&0o030 == 0o030
	default:
		return perm&0o003 == 0o003
	}
}
