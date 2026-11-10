package accounts

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"mcode/internal/fsutil"
	"mcode/internal/providers/multiaccount"
)

const (
	accountsFileName = "agy-accounts.json"
	routingFileName  = "agy-routing.json"
	bindingsFileName = "agy-bindings.json"
	filePerm0600     = 0600
	dirPerm0700      = 0700
)

// atomicWriteJSON 将数据以 0600 权限原子写入指定路径：
// 同目录临时文件 -> 刷盘 Sync -> 显式 Chmod 0600 -> 关闭 -> os.Rename 替换。
func atomicWriteJSON(path string, v any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirPerm0700); err != nil {
		return fmt.Errorf("create parent directory %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode json to %s: %w", path, err)
	}
	return fsutil.WriteFileAtomic(path, append(data, '\n'), filePerm0600)
}

// loadIndex 从 <dataDir>/agy-accounts.json 读取索引。
// 若文件不存在，则创建默认索引；若系统账号不存在，自动补全；defaultAccountId 为空时重置为 system。
func (m *Manager) loadIndex() error {
	filePath := filepath.Join(m.opts.DataDir, accountsFileName)
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			m.index = Index{
				Version:          1,
				DefaultAccountID: SystemAccountID,
				Accounts:         []Account{defaultSystemAccount(m.opts.Now())},
			}
			return atomicWriteJSON(filePath, m.index)
		}
		return fmt.Errorf("read index file %s: %w", filePath, err)
	}

	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return fmt.Errorf("unmarshal index json: %w", err)
	}

	// 检查是否包含系统账号，若无则前置或补上
	hasSystem := false
	for _, acc := range idx.Accounts {
		if acc.ID == SystemAccountID {
			hasSystem = true
			break
		}
	}
	if !hasSystem {
		idx.Accounts = append([]Account{defaultSystemAccount(m.opts.Now())}, idx.Accounts...)
	}

	if idx.DefaultAccountID == "" {
		idx.DefaultAccountID = SystemAccountID
	}
	if idx.Version == 0 {
		idx.Version = 1
	}

	m.index = idx
	return atomicWriteJSON(filePath, m.index)
}

// defaultSystemAccount 生成默认的系统账号实体。
func defaultSystemAccount(now time.Time) Account {
	return Account{
		ID:            SystemAccountID,
		Email:         "",
		Name:          "System",
		Label:         "系统账号",
		Status:        StatusActive,
		StatusReason:  "",
		On:            true,
		Order:         0,
		Weight:        100,
		ProjectID:     "",
		Tier:          "",
		DeviceProfile: DeviceProfile{},
		Quota:         make(map[string]QuotaGroup),
		QuotaReadAt:   "",
		QuotaStale:    false,
		AddedAt:       now.UTC().Format(time.RFC3339),
	}
}

// saveIndexLocked 将当前索引原子写回磁盘，调用前必须持有 m.mu。
func (m *Manager) saveIndexLocked() error {
	filePath := filepath.Join(m.opts.DataDir, accountsFileName)
	return atomicWriteJSON(filePath, m.index)
}

// loadRouting 从 <dataDir>/agy-routing.json 加载路由配置。
func (m *Manager) loadRouting() error {
	filePath := filepath.Join(m.opts.DataDir, routingFileName)
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			m.routing = RoutingStore{
				Config:   multiaccount.DefaultConfig(),
				Accounts: make(map[string]AccountRoutingOverride),
			}
			return atomicWriteJSON(filePath, m.routing)
		}
		return fmt.Errorf("read routing file %s: %w", filePath, err)
	}

	var store RoutingStore
	if err := json.Unmarshal(data, &store); err != nil {
		return fmt.Errorf("unmarshal routing json: %w", err)
	}
	store.Config = store.Config.Normalize()
	if store.Accounts == nil {
		store.Accounts = make(map[string]AccountRoutingOverride)
	}
	m.routing = store
	return nil
}

// saveRoutingLocked 将路由配置原子写回磁盘，调用前必须持有 m.mu。
func (m *Manager) saveRoutingLocked() error {
	filePath := filepath.Join(m.opts.DataDir, routingFileName)
	return atomicWriteJSON(filePath, m.routing)
}

// loadBindings 从 <dataDir>/agy-bindings.json 加载会话绑定映射。
// 文档中规划将绑定状态持久化到本地数据库，此处为了轻量独立、降低多表耦合及避免引入额外数据库表结构，
// 先用原子落盘的 JSON 文件代替，接口与行为完全一致，并在未来可无缝迁移至本地 SQLite。
func (m *Manager) loadBindings() (map[string]string, error) {
	filePath := filepath.Join(m.opts.DataDir, bindingsFileName)
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			b := make(map[string]string)
			return b, atomicWriteJSON(filePath, b)
		}
		return nil, fmt.Errorf("read bindings file %s: %w", filePath, err)
	}

	var b map[string]string
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("unmarshal bindings json: %w", err)
	}
	if b == nil {
		b = make(map[string]string)
	}
	return b, nil
}

// saveBindings 将会话绑定原子写回磁盘。
func (m *Manager) saveBindings(bindings map[string]string) error {
	filePath := filepath.Join(m.opts.DataDir, bindingsFileName)
	return atomicWriteJSON(filePath, bindings)
}
