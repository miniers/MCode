package remote

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// alwaysAllowFileName 是「始终允许」持久化文件在 mcode 数据目录下的文件名。
const alwaysAllowFileName = "always_allow.json"

// alwaysAllowFile 是落盘结构：项目键 -> 记账键列表（写入时已去重并保持加入顺序）。
type alwaysAllowFile struct {
	Projects map[string][]string `json:"projects"`
}

// alwaysAllowStore 把审批的「始终允许」按项目持久化到 mcode 数据目录，使同一项目目录下的放行结论
// 跨会话、跨重启保留。命令类工具按可记账前缀或完整命令生成键，
// 只写 mcode 自己的数据目录，不写 .claude 目录或用户的 Claude 配置。
type alwaysAllowStore struct {
	mu      sync.Mutex
	path    string
	entries map[string][]string
}

// alwaysAllowStorePath 返回数据目录下的「始终允许」文件路径；数据目录为空时返回空路径（不落盘）。
func alwaysAllowStorePath(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, alwaysAllowFileName)
}

// newAlwaysAllowStore 创建存储并从指定路径加载已有记录。
func newAlwaysAllowStore(path string) *alwaysAllowStore {
	s := &alwaysAllowStore{path: path, entries: make(map[string][]string)}
	s.load()
	return s
}

// normalizeAlwaysAllowProject 把项目路径规范成记账键：Clean 后尽力解析符号链接，
// 解析失败（目录已删除、键本身是项目 ID 等）沿用 Clean 结果。
func normalizeAlwaysAllowProject(project string) string {
	p := strings.TrimSpace(project)
	if p == "" {
		return ""
	}
	p = filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(p); err == nil && resolved != "" {
		return filepath.Clean(resolved)
	}
	return p
}

// load 读取持久化文件：文件不存在按空处理，解析失败时把原文件改名为 .bak 保留现场后按空处理。
func (s *alwaysAllowStore) load() {
	if s.path == "" {
		return
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("load always-allow store from %s failed: %v", s.path, err)
		}
		return
	}

	var file alwaysAllowFile
	if err := json.Unmarshal(data, &file); err != nil {
		log.Printf("unmarshal always-allow store from %s failed: %v", s.path, err)
		if bakErr := os.Rename(s.path, s.path+".bak"); bakErr != nil {
			log.Printf("keep corrupted always-allow store backup %s failed: %v", s.path+".bak", bakErr)
		}
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for project, keys := range file.Projects {
		if project == "" {
			continue
		}
		kept := make([]string, 0, len(keys))
		seen := make(map[string]bool, len(keys))
		for _, key := range keys {
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			kept = append(kept, key)
		}
		if len(kept) > 0 {
			s.entries[project] = kept
		}
	}
}

// saveLocked 把内存记录写回文件（写临时文件再 rename 的原子写）；调用方必须持有 s.mu。
func (s *alwaysAllowStore) saveLocked() error {
	if s.path == "" {
		return nil
	}
	file := alwaysAllowFile{Projects: make(map[string][]string, len(s.entries))}
	for project, keys := range s.entries {
		if project == "" || len(keys) == 0 {
			continue
		}
		file.Projects[project] = append([]string(nil), keys...)
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal always-allow store: %w", err)
	}
	if err := writeAtomic(s.path, data, 0o600); err != nil {
		return fmt.Errorf("write always-allow store %s: %w", s.path, err)
	}
	return nil
}

// Allowed 判断指定项目下是否已记录该记账键。
func (s *alwaysAllowStore) Allowed(project, key string) bool {
	project, key = normalizeAlwaysAllowProject(project), strings.TrimSpace(key)
	if project == "" || key == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.entries[project] {
		if existing == key {
			return true
		}
	}
	return false
}

// Add 在指定项目下记录一个记账键；已存在时不重复写入。项目、键为空或没有落盘路径（数据目录为空）时不做任何事。
func (s *alwaysAllowStore) Add(project, key string) error {
	project, key = normalizeAlwaysAllowProject(project), strings.TrimSpace(key)
	if project == "" || key == "" || s.path == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.entries[project] {
		if existing == key {
			return nil
		}
	}
	s.entries[project] = append(s.entries[project], key)
	return s.saveLocked()
}

// Remove 撤销指定项目下的一个记账键；不存在、没有落盘路径（数据目录为空）时幂等返回 nil。
func (s *alwaysAllowStore) Remove(project, key string) error {
	project, key = normalizeAlwaysAllowProject(project), strings.TrimSpace(key)
	if project == "" || key == "" || s.path == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keys, ok := s.entries[project]
	if !ok {
		return nil
	}
	kept := make([]string, 0, len(keys))
	for _, existing := range keys {
		if existing != key {
			kept = append(kept, existing)
		}
	}
	if len(kept) == len(keys) {
		return nil
	}
	if len(kept) == 0 {
		delete(s.entries, project)
	} else {
		s.entries[project] = kept
	}
	return s.saveLocked()
}

// List 返回指定项目下已记录的记账键副本（按字典序排序）。
func (s *alwaysAllowStore) List(project string) []string {
	project = normalizeAlwaysAllowProject(project)
	if project == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := s.entries[project]
	if len(keys) == 0 {
		return nil
	}
	out := append([]string(nil), keys...)
	sort.Strings(out)
	return out
}

// ListAll 返回全部项目的记账键副本，供撤销界面一次性展示。
func (s *alwaysAllowStore) ListAll() map[string][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string][]string, len(s.entries))
	for project, keys := range s.entries {
		if len(keys) == 0 {
			continue
		}
		copied := append([]string(nil), keys...)
		sort.Strings(copied)
		out[project] = copied
	}
	return out
}

// alwaysAllowStoreFactory 按文件路径创建存储实例；测试可替换它为临时目录。
var alwaysAllowStoreFactory = newAlwaysAllowStore

var (
	alwaysAllowStoreMu sync.Mutex
	alwaysAllowStores  = make(map[string]*alwaysAllowStore)
)

// alwaysAllowStoreFor 返回数据目录对应的存储实例，同一路径复用同一实例，保证会话内存与撤销接口看到同一份记录。
// 数据目录为空时返回只写内存、不落盘的实例，调用方仍可安全读写。
func alwaysAllowStoreFor(dataDir string) *alwaysAllowStore {
	path := alwaysAllowStorePath(dataDir)
	alwaysAllowStoreMu.Lock()
	defer alwaysAllowStoreMu.Unlock()
	if store, ok := alwaysAllowStores[path]; ok {
		return store
	}
	store := alwaysAllowStoreFactory(path)
	alwaysAllowStores[path] = store
	return store
}

// forgetAlwaysAllowStore 丢弃某数据目录的缓存实例，下次访问重新从磁盘加载（测试用来模拟进程重启）。
func forgetAlwaysAllowStore(dataDir string) {
	path := alwaysAllowStorePath(dataDir)
	alwaysAllowStoreMu.Lock()
	delete(alwaysAllowStores, path)
	alwaysAllowStoreMu.Unlock()
}
