package swarm

import (
	"path/filepath"
	"sync"
)

var headLocks sync.Map

// lockHead 在所有 Manager 与 Pipeline 之间按目标目录串行移动 HEAD。
// 锁顺序：lead mergeMu → HEAD；持有 HEAD 锁时不得再取 mergeMu、tickMu 或 checkoutLocks。
func lockHead(path string) func() {
	key, err := filepath.Abs(path)
	if err != nil {
		key = path
	}
	value, _ := headLocks.LoadOrStore(filepath.Clean(key), &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}
