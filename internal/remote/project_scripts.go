package remote

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"

	"mcode/internal/remote/wire"
)

// projectScriptsFile 是项目脚本在数据目录下的相对位置，内容为以项目 ID 为键的脚本列表。
const projectScriptsFile = "project-scripts.json"

var (
	projectScriptsMu  sync.Mutex
	memProjectScripts = make(map[*MemoryBackend]map[string][]ProjectScript)
)

// ProjectScript 表示 mcode 自己的项目级脚本，
// 与工作区 package.json 的 scripts 无关；只由本机 UI 编辑，协议里只做列表与运行。
type ProjectScript struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Command             string `json:"command"`
	Symbol              string `json:"symbol,omitempty"`
	RunOnWorktreeCreate bool   `json:"runOnWorktreeCreate"`
}

// threadProjectID 获取指定会话所属的项目 ID，用于定位项目级脚本。
func (b *MemoryBackend) threadProjectID(threadID string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	td, ok := b.threads[threadID]
	if !ok {
		return "", &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}
	projectID := strings.TrimSpace(td.summary.ProjectID)
	if projectID == "" {
		return "", &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s has no project", threadID),
		}
	}
	return projectID, nil
}

// ProjectScripts 读取会话所属项目的项目级脚本。
func (b *MemoryBackend) ProjectScripts(threadID string) ([]ProjectScript, error) {
	projectID, err := b.threadProjectID(threadID)
	if err != nil {
		return nil, err
	}
	return b.projectScriptsByProjectID(projectID)
}

// projectScriptsByProjectID 按项目 ID 读取项目级脚本，供不依赖会话的路径（如 worktree 初始化）使用。
func (b *MemoryBackend) projectScriptsByProjectID(projectID string) ([]ProjectScript, error) {
	projectScriptsMu.Lock()
	defer projectScriptsMu.Unlock()
	return b.readProjectScriptsLocked(projectID)
}

// readProjectScriptsLocked 读取项目脚本，调用方必须已持有 projectScriptsMu。
func (b *MemoryBackend) readProjectScriptsLocked(projectID string) ([]ProjectScript, error) {
	b.mu.Lock()
	dataDir := b.dataDir
	b.mu.Unlock()

	if dataDir == "" {
		return cloneProjectScripts(memProjectScripts[b][projectID]), nil
	}

	data, err := os.ReadFile(filepath.Join(dataDir, "remote", projectScriptsFile))
	if err != nil {
		if os.IsNotExist(err) {
			return []ProjectScript{}, nil
		}
		return nil, err
	}
	var stored map[string][]ProjectScript
	if err := json.Unmarshal(data, &stored); err != nil {
		return []ProjectScript{}, nil
	}
	return cloneProjectScripts(stored[projectID]), nil
}

// writeProjectScriptsLocked 覆盖写入项目脚本，调用方必须已持有 projectScriptsMu。
func (b *MemoryBackend) writeProjectScriptsLocked(projectID string, scripts []ProjectScript) error {
	b.mu.Lock()
	dataDir := b.dataDir
	b.mu.Unlock()

	if dataDir == "" {
		m, ok := memProjectScripts[b]
		if !ok {
			m = make(map[string][]ProjectScript)
			memProjectScripts[b] = m
		}
		m[projectID] = scripts
		return nil
	}

	remoteDir := filepath.Join(dataDir, "remote")
	if err := os.MkdirAll(remoteDir, 0755); err != nil {
		return err
	}
	path := filepath.Join(remoteDir, projectScriptsFile)
	stored := make(map[string][]ProjectScript)
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &stored)
	}
	stored[projectID] = scripts

	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(remoteDir, path, data)
}

// projectScriptsRevision 计算脚本列表的内容版本（sha256 前 8 字节的十六进制）。
// 存储里没有单独的版本字段，用内容摘要充当乐观锁版本：内容不变则版本不变。
func projectScriptsRevision(scripts []ProjectScript) string {
	data, err := json.Marshal(scripts)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// SetProjectScripts 覆盖保存会话所属项目的项目级脚本。
func (b *MemoryBackend) SetProjectScripts(threadID string, scripts []ProjectScript) error {
	projectID, err := b.threadProjectID(threadID)
	if err != nil {
		return err
	}
	_, err = b.SetProjectScriptsByProjectID(projectID, scripts, "")
	return err
}

// SetProjectScriptsByProjectID 按项目 ID 覆盖保存项目脚本。
// expectedRevision 非空时做乐观锁：与当前内容版本不一致返回 ErrCodeVersionConflict。
// 返回规范化后的脚本列表（补全 ID、去空白），失败时返回 nil。
func (b *MemoryBackend) SetProjectScriptsByProjectID(projectID string, scripts []ProjectScript, expectedRevision string) ([]ProjectScript, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, &wire.RPCError{Code: wire.InvalidParams, Message: "projectID must not be empty"}
	}

	b.mu.Lock()
	_, exists := b.projects[projectID]
	b.mu.Unlock()
	if !exists {
		return nil, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("project %s not found", projectID),
		}
	}

	normalized, err := normalizeProjectScripts(scripts)
	if err != nil {
		return nil, &wire.RPCError{Code: wire.InvalidParams, Message: err.Error()}
	}

	projectScriptsMu.Lock()
	defer projectScriptsMu.Unlock()

	if expectedRevision != "" {
		current, err := b.readProjectScriptsLocked(projectID)
		if err != nil {
			return nil, err
		}
		if currentRevision := projectScriptsRevision(current); currentRevision != expectedRevision {
			return nil, &wire.RPCError{
				Code:    wire.ErrCodeVersionConflict,
				Message: fmt.Sprintf("project scripts changed: current revision is %s", currentRevision),
			}
		}
	}

	if err := b.writeProjectScriptsLocked(projectID, normalized); err != nil {
		return nil, err
	}
	return cloneProjectScripts(normalized), nil
}

// writeFileAtomic 先写临时文件再 rename，避免进程中断时留下半个文件。
func writeFileAtomic(dir, path string, data []byte) error {
	tmpFile, err := os.CreateTemp(dir, "project-scripts-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmpFile.Name()
	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// normalizeProjectScripts 去掉首尾空白、补全缺失的 ID，并校验名称与命令。
func normalizeProjectScripts(scripts []ProjectScript) ([]ProjectScript, error) {
	out := make([]ProjectScript, 0, len(scripts))
	seen := make(map[string]bool, len(scripts))
	for _, sc := range scripts {
		item := ProjectScript{
			ID:                  strings.TrimSpace(sc.ID),
			Name:                strings.TrimSpace(sc.Name),
			Command:             strings.TrimSpace(sc.Command),
			Symbol:              strings.TrimSpace(sc.Symbol),
			RunOnWorktreeCreate: sc.RunOnWorktreeCreate,
		}
		if item.ID == "" {
			item.ID = uuid.NewString()
		}
		if item.Name == "" {
			return nil, fmt.Errorf("脚本名称不能为空")
		}
		if item.Command == "" {
			return nil, fmt.Errorf("脚本「%s」缺少命令", item.Name)
		}
		if seen[item.Name] {
			return nil, fmt.Errorf("脚本名称「%s」重复", item.Name)
		}
		seen[item.Name] = true
		out = append(out, item)
	}
	return out, nil
}

// cloneProjectScripts 返回列表副本，避免调用方改动内部持有的切片。
func cloneProjectScripts(scripts []ProjectScript) []ProjectScript {
	res := make([]ProjectScript, len(scripts))
	copy(res, scripts)
	return res
}
