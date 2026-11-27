package swarm

import (
	"path/filepath"
	"sync"
)

var (
	headLocks  sync.Map
	applyLocks sync.Map
)

func lockDir(locks *sync.Map, path string) func() {
	key, err := filepath.Abs(path)
	if err != nil {
		key = path
	}
	value, _ := locks.LoadOrStore(filepath.Clean(key), &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// lockHead 在所有 Manager 与 Pipeline 之间按目标目录串行移动 HEAD。
// 锁顺序：dispatchMu → lead mergeMu → apply → HEAD；持有 HEAD 锁时不得再取 apply、mergeMu、tickMu 或 checkoutLocks。
func lockHead(path string) func() {
	return lockDir(&headLocks, path)
}

// lockApply 在所有 Manager 之间按目标目录串行执行 applyMerge（拍快照、三方应用、冲突复原）。
// 锁顺序：dispatchMu → lead mergeMu → apply → HEAD。
func lockApply(path string) func() {
	return lockDir(&applyLocks, path)
}
