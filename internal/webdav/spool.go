package webdav

import (
	"os"
	"sync"
	"syscall"

	"discodrive/internal/quota"
)

// spoolReserve is the free space a PUT leaves on the temp disk: an upload with no quota
// to bound it must not fill the disk the server itself writes to.
const spoolReserve = 1 << 30

// spoolLimit bounds the temp buffer of a PUT: the user's allowance, but never more than
// the temp disk can take beyond spoolReserve.
func spoolLimit(allowance, free int64) int64 {
	return max(0, min(allowance, free-spoolReserve))
}

// tempFree reports the free space of the temp directory; quota.Unlimited when unknown.
func tempFree() int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(os.TempDir(), &st); err != nil {
		return quota.Unlimited
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

// spooled counts the bytes each user's PUTs hold in temp files. The allowance a PUT reads
// when it starts is a snapshot of what is published, so the PUTs in flight together must
// stay within it, not each on its own.
var spooled = struct {
	mu sync.Mutex
	by map[string]int64
}{by: make(map[string]int64)}

// spoolTake reserves n bytes of user's spool if the total stays within limit.
func spoolTake(user string, n, limit int64) bool {
	spooled.mu.Lock()
	defer spooled.mu.Unlock()
	if spooled.by[user]+n > limit {
		return false
	}
	spooled.by[user] += n
	return true
}

// spoolGive returns n bytes of user's spool.
func spoolGive(user string, n int64) {
	if n <= 0 {
		return
	}
	spooled.mu.Lock()
	defer spooled.mu.Unlock()
	if spooled.by[user] -= n; spooled.by[user] <= 0 {
		delete(spooled.by, user)
	}
}
