package remote

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"mcode/internal/remote/wire"
)

const (
	maxActionLedgerCapacity = 256
	actionLedgerTTL         = 10 * time.Minute
)

type actionLedgerEntry struct {
	result    wire.ActionStatusResult
	timestamp time.Time
}

type persistentActionRecord struct {
	Result    wire.ActionStatusResult `json:"result"`
	Timestamp time.Time               `json:"timestamp"`
}

// actionLedger 维护最近动作结果的账本，支持内存与持久化存储、覆盖写入、容量淘汰与过期淘汰。
type actionLedger struct {
	mu       sync.Mutex
	capacity int
	ttl      time.Duration
	now      func() time.Time
	entries  map[string]actionLedgerEntry
	order    []string
	path     string
}

func newActionLedger(now func() time.Time) *actionLedger {
	if now == nil {
		now = time.Now
	}
	return &actionLedger{
		capacity: maxActionLedgerCapacity,
		ttl:      actionLedgerTTL,
		now:      now,
		entries:  make(map[string]actionLedgerEntry),
		order:    make([]string, 0, maxActionLedgerCapacity),
	}
}

// newPersistentActionLedger 创建并在指定路径加载持久化动作账本。
func newPersistentActionLedger(path string, now func() time.Time) *actionLedger {
	l := newActionLedger(now)
	l.path = path
	l.load()
	return l
}

func (l *actionLedger) setNow(now func() time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now == nil {
		now = time.Now
	}
	l.now = now
}

func (l *actionLedger) load() {
	if l.path == "" {
		return
	}
	data, err := os.ReadFile(l.path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("load persistent action ledger from %s failed: %v", l.path, err)
		}
		return
	}

	var records []persistentActionRecord
	if err := json.Unmarshal(data, &records); err != nil {
		log.Printf("unmarshal persistent action ledger from %s failed: %v", l.path, err)
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	for _, rec := range records {
		if rec.Result.ActionID == "" {
			continue
		}
		if now.Sub(rec.Timestamp) > l.ttl {
			continue
		}
		if _, exists := l.entries[rec.Result.ActionID]; exists {
			for i, id := range l.order {
				if id == rec.Result.ActionID {
					l.order = append(l.order[:i], l.order[i+1:]...)
					break
				}
			}
		} else {
			for len(l.entries) >= l.capacity && len(l.order) > 0 {
				oldest := l.order[0]
				l.order = l.order[1:]
				delete(l.entries, oldest)
			}
		}
		l.entries[rec.Result.ActionID] = actionLedgerEntry{
			result:    rec.Result,
			timestamp: rec.Timestamp,
		}
		l.order = append(l.order, rec.Result.ActionID)
	}
}

func (l *actionLedger) saveLocked() {
	if l.path == "" {
		return
	}

	now := l.now()
	var records []persistentActionRecord
	for _, id := range l.order {
		entry, ok := l.entries[id]
		if !ok || now.Sub(entry.timestamp) > l.ttl {
			continue
		}
		records = append(records, persistentActionRecord{
			Result:    entry.result,
			Timestamp: entry.timestamp,
		})
	}

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		log.Printf("marshal persistent action ledger failed: %v", err)
		return
	}

	dir := filepath.Dir(l.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("create dir %s for persistent action ledger failed: %v", dir, err)
		return
	}

	tmpFile, err := os.CreateTemp(dir, ".action-ledger-*.tmp")
	if err != nil {
		log.Printf("create temp file for action ledger in %s failed: %v", dir, err)
		return
	}
	tmpName := tmpFile.Name()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpName)
		log.Printf("write temp file for action ledger failed: %v", err)
		return
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpName)
		log.Printf("close temp file for action ledger failed: %v", err)
		return
	}

	if err := os.Rename(tmpName, l.path); err != nil {
		_ = os.Remove(tmpName)
		log.Printf("rename temp file to %s failed: %v", l.path, err)
		return
	}
}

// record 记录指定动作状态结果。同一 actionID 覆盖写入。
func (l *actionLedger) record(r wire.ActionStatusResult) {
	if r.ActionID == "" {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()

	if _, exists := l.entries[r.ActionID]; exists {
		// 覆盖写入：更新条目并移至切片末尾
		for i, id := range l.order {
			if id == r.ActionID {
				l.order = append(l.order[:i], l.order[i+1:]...)
				break
			}
		}
	} else {
		// 超出容量上限时淘汰最旧的条目
		for len(l.entries) >= l.capacity && len(l.order) > 0 {
			oldest := l.order[0]
			l.order = l.order[1:]
			delete(l.entries, oldest)
		}
	}

	l.entries[r.ActionID] = actionLedgerEntry{
		result:    r,
		timestamp: now,
	}
	l.order = append(l.order, r.ActionID)

	l.saveLocked()
}

// recordEntry 记录带显式时间戳的动作状态结果，用于账本迁移。
func (l *actionLedger) recordEntry(r wire.ActionStatusResult, t time.Time) {
	if r.ActionID == "" {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if _, exists := l.entries[r.ActionID]; exists {
		for i, id := range l.order {
			if id == r.ActionID {
				l.order = append(l.order[:i], l.order[i+1:]...)
				break
			}
		}
	} else {
		for len(l.entries) >= l.capacity && len(l.order) > 0 {
			oldest := l.order[0]
			l.order = l.order[1:]
			delete(l.entries, oldest)
		}
	}

	l.entries[r.ActionID] = actionLedgerEntry{
		result:    r,
		timestamp: t,
	}
	l.order = append(l.order, r.ActionID)

	l.saveLocked()
}

// migrateTo 将当前账本中的未过期条目迁入目标账本。
func (l *actionLedger) migrateTo(target *actionLedger) {
	if l == nil || target == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	for _, id := range l.order {
		entry, ok := l.entries[id]
		if !ok || now.Sub(entry.timestamp) > l.ttl {
			continue
		}
		target.recordEntry(entry.result, entry.timestamp)
	}
}

// get 获取指定 actionID 的状态结果。若不存在或超过 10 分钟则返回 false。
func (l *actionLedger) get(actionID string) (wire.ActionStatusResult, bool) {
	if actionID == "" {
		return wire.ActionStatusResult{}, false
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	entry, exists := l.entries[actionID]
	if !exists {
		return wire.ActionStatusResult{}, false
	}

	if l.now().Sub(entry.timestamp) > l.ttl {
		delete(l.entries, actionID)
		return wire.ActionStatusResult{}, false
	}

	return entry.result, true
}
