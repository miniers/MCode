package git

import (
	"sync"
	"time"
)

const AutoFetchInterval = 5 * time.Minute

var autoFetchThrottle = struct {
	sync.Mutex
	last map[string]time.Time
}{last: make(map[string]time.Time)}

func AutoFetchDue(last, now time.Time) bool {
	return last.IsZero() || now.Sub(last) >= AutoFetchInterval
}

// ClaimAutoFetch limits desktop and remote clients together for each repository.
func ClaimAutoFetch(root string, now time.Time) bool {
	autoFetchThrottle.Lock()
	defer autoFetchThrottle.Unlock()
	if !AutoFetchDue(autoFetchThrottle.last[root], now) {
		return false
	}
	autoFetchThrottle.last[root] = now
	return true
}
