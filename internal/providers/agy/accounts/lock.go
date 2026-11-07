package accounts

import (
	"io"
	"path/filepath"
	"sync"

	"mcode/internal/filelock"
)

// acquireFileLock 复用公共 filelock 包的阻塞加锁实现，锁被占用时在此等待。
func acquireFileLock(path string) (io.Closer, error) {
	return filelock.Acquire(path)
}

// tokenLockForAccount 获取指定账号的进程内互斥锁。
func (m *Manager) tokenLockForAccount(id string) *sync.Mutex {
	m.tokenLocksMu.Lock()
	defer m.tokenLocksMu.Unlock()

	mu, ok := m.tokenLocks[id]
	if !ok {
		mu = &sync.Mutex{}
		m.tokenLocks[id] = mu
	}
	return mu
}

// withTokenLock 先获取该账号的进程内互斥锁，再获取跨进程文件锁，随后在回调中传入 token 文件的绝对路径。
// 所有对凭证文件的读写都必须经由本函数执行，以保证凭证唯一物理源的一致性（满足 §2.3）。
func (m *Manager) withTokenLock(id string, fn func(path string) error) error {
	mu := m.tokenLockForAccount(id)
	mu.Lock()
	defer mu.Unlock()

	lockPath := filepath.Join(m.accountDir(id), ".token.lock")
	flock, err := acquireFileLock(lockPath)
	if err != nil {
		return err
	}
	defer flock.Close()

	var tokenPath string
	if id == SystemAccountID {
		tokenPath = filepath.Join(m.opts.HostHome, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	} else {
		tokenPath = filepath.Join(m.accountHomeDir(id), ".gemini", "antigravity-cli", "antigravity-oauth-token")
	}

	return fn(tokenPath)
}
