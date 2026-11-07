package magpiecfg

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// fieldState 是一个键在某个时刻的状态：Value 是规范化后的 JSON 文本
// （字符串带引号，对象按键排序、空白最少），Had 表示这个键当时是否存在。
// 删除键之后的状态就是 {Had: false}。旧版本写下的文件里 Value 是没有引号
// 的字符串，读取后原样保留，比较时由 sameJSONValue 按 JSON 语义处理。
//
// Table 标记这个键在 TOML 文件里是一整张表（例如
// model_providers.mcode-magpie）；旧记录没有这个字段，按普通标量处理。
//
// Pruned 记录写这个键时由我们创建、从浅到深的祖先对象（点分路径）：那些
// 对象原本不存在，恢复删除这个键后如果因此变空，从深到浅一并删掉。用户原本
// 就有的空对象不在列表里；旧记录没有这个字段时保持原有行为。
type fieldState struct {
	Value  string   `json:"value"`
	Had    bool     `json:"had"`
	Table  bool     `json:"table,omitempty"`
	Pruned []string `json:"pruned,omitempty"`
}

// stashFile 记录 connect 之前用户配置里的原值，用于 disconnect 恢复。
// Files 按配置文件路径分开记录；一次计划可以接管多个文件。
type stashFile struct {
	Files map[string]map[string]fieldState `json:"files,omitempty"`

	// Orig 按路径记录接管前 JSON 文件的原文。断开时文件若仍是最后一次写入
	// 的样子，就按这份原文逐字节写回：字段级编辑无法还原用户原来的键序、
	// 缩进和末尾换行。文件一旦在我们管理的键之外被别人改过（例如用户手改
	// 之后又点重写），这条记录就会被删掉，断开时退回字段级恢复。
	// TOML 文件不记（它有逐字节还原的编辑原语）。
	Orig map[string]string `json:"orig,omitempty"`

	// 旧版（单文件）记录，只读；loadStash 会按 applied 里的路径迁移进 Files。
	Fields map[string]fieldState `json:"fields,omitempty"`
}

// appliedTarget 是一个被接管文件的记录。
type appliedTarget struct {
	// Format 是配置文件的格式：空字符串（旧记录）就是 JSON，其它值见
	// Format 常量。
	Format string `json:"format,omitempty"`
	// Created 表示文件原本不存在、由我们创建；disconnect 恢复后只剩空对象
	// 时要把它删掉。
	Created bool `json:"created,omitempty"`
	// Fields 是写进这个文件的值。
	Fields map[string]fieldState `json:"fields,omitempty"`
	// Written 是最后一次写入后整份文件的规范化文本：断开时拿它判断文件
	// 在写入之后有没有被改过（改过就不按原文覆盖）。
	Written string `json:"written,omitempty"`
}

// appliedFile 记录我们写进所有被接管文件的值。
type appliedFile struct {
	// Kept 表示用户选择了「保留我的修改」：此后不再 drift / reapply /
	// 按 stash 恢复。
	Kept bool `json:"kept,omitempty"`
	// Files 按配置文件路径分开记录。
	Files map[string]appliedTarget `json:"files,omitempty"`

	// 旧版（单文件）记录，只读；loadApplied 会迁移进 Files，保存时清空。
	Path    string                `json:"path,omitempty"`
	Format  string                `json:"format,omitempty"`
	Created bool                  `json:"created,omitempty"`
	Fields  map[string]fieldState `json:"fields,omitempty"`
}

// stateDir 是某个 agent 的状态目录：<数据目录>/agentcfg/<agent>/。
func stateDir(dataDir, agent string) string {
	return filepath.Join(dataDir, "agentcfg", agent)
}

func stashPath(dataDir, agent string) string {
	return filepath.Join(stateDir(dataDir, agent), "stash.json")
}

func appliedPath(dataDir, agent string) string {
	return filepath.Join(stateDir(dataDir, agent), "applied.json")
}

// validateAgent 确保 agent 名不会把状态文件带出数据目录。
func validateAgent(agent string) error {
	if agent == "" {
		return errors.New("magpiecfg: agent 名为空")
	}
	if agent == "." || agent == ".." || strings.ContainsAny(agent, `/\`) {
		return fmt.Errorf("magpiecfg: agent 名 %q 不能用于路径", agent)
	}
	return nil
}

// readState 把 path 的内容解码进 v；文件不存在或为空时保持 v 不变。
func readState(path string, v any) error {
	data, err := Read(path)
	if err != nil {
		return fmt.Errorf("magpiecfg: 读取状态文件 %s: %w", path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("magpiecfg: 解析状态文件 %s: %w", path, err)
	}
	return nil
}

// writeState 原子地写入状态文件：目录 0700、文件 0600。stash 里有明文
// API Key，权限和 agent 自己的配置一样敏感。
func writeState(path string, v any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("magpiecfg: 创建状态目录 %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("magpiecfg: 编码状态文件 %s: %w", path, err)
	}
	data = append(data, '\n')
	// WriteAtomic 对新文件用 0644，只保留已有文件的权限。先按 0600 建出
	// 文件，让随后的原子写沿用这个权限。
	created := false
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	switch {
	case err == nil:
		created = true
		_ = f.Close()
	case !errors.Is(err, fs.ErrExist):
		return fmt.Errorf("magpiecfg: 创建状态文件 %s: %w", path, err)
	}
	if err := WriteAtomic(path, data); err != nil {
		if created {
			_ = os.Remove(path)
		}
		return fmt.Errorf("magpiecfg: 写入状态文件 %s: %w", path, err)
	}
	// 老版本或用户手动改过权限时可能更宽，写完后收紧。
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("magpiecfg: 设置状态文件权限 %s: %w", path, err)
	}
	return nil
}

func loadStash(dataDir, agent string) (*stashFile, error) {
	if err := validateAgent(agent); err != nil {
		return nil, err
	}
	st := &stashFile{}
	if err := readState(stashPath(dataDir, agent), st); err != nil {
		return nil, err
	}
	if st.Files == nil {
		st.Files = map[string]map[string]fieldState{}
	}
	// 旧版 stash 只有 Fields，没有路径；按 applied 里记录的单文件路径迁移。
	if len(st.Files) == 0 && len(st.Fields) > 0 {
		if path := legacyAppliedPath(dataDir, agent); path != "" {
			st.Files[path] = st.Fields
		}
	}
	st.Fields = nil
	return st, nil
}

// legacyAppliedPath 返回旧版 applied 记录里的单文件路径；没有旧记录时返回空。
func legacyAppliedPath(dataDir, agent string) string {
	var ap appliedFile
	if err := readState(appliedPath(dataDir, agent), &ap); err != nil {
		return ""
	}
	if path := strings.TrimSpace(ap.Path); path != "" {
		return path
	}
	for path := range ap.Files {
		return path
	}
	return ""
}

func saveStash(dataDir, agent string, st *stashFile) error {
	if err := validateAgent(agent); err != nil {
		return err
	}
	if st.Files == nil {
		st.Files = map[string]map[string]fieldState{}
	}
	st.Fields = nil
	return writeState(stashPath(dataDir, agent), st)
}

func loadApplied(dataDir, agent string) (*appliedFile, error) {
	if err := validateAgent(agent); err != nil {
		return nil, err
	}
	ap := &appliedFile{}
	if err := readState(appliedPath(dataDir, agent), ap); err != nil {
		return nil, err
	}
	if ap.Files == nil {
		ap.Files = map[string]appliedTarget{}
	}
	// 旧版状态文件只有一个文件：path/format/created/fields 搬到 Files 里。
	if len(ap.Files) == 0 && strings.TrimSpace(ap.Path) != "" {
		ap.Files[ap.Path] = appliedTarget{Format: ap.Format, Created: ap.Created, Fields: ap.Fields}
	}
	ap.Path, ap.Format, ap.Created, ap.Fields = "", "", false, nil
	for path, target := range ap.Files {
		if target.Fields == nil {
			target.Fields = map[string]fieldState{}
			ap.Files[path] = target
		}
	}
	return ap, nil
}

// Applied 报告是否有全局配置接管记录；兼容旧版单文件记录，损坏的状态返回错误。
func Applied(dataDir, agent string) (bool, error) {
	ap, err := loadApplied(dataDir, agent)
	if err != nil {
		return false, err
	}
	return len(ap.Files) > 0, nil
}

func saveApplied(dataDir, agent string, ap *appliedFile) error {
	if err := validateAgent(agent); err != nil {
		return err
	}
	if ap.Files == nil {
		ap.Files = map[string]appliedTarget{}
	}
	ap.Path, ap.Format, ap.Created, ap.Fields = "", "", false, nil
	for path, target := range ap.Files {
		if target.Fields == nil {
			target.Fields = map[string]fieldState{}
			ap.Files[path] = target
		}
	}
	return writeState(appliedPath(dataDir, agent), ap)
}

// removeState 删掉 applied 与 stash；两者都不存在不算错误。状态目录空了
// 就顺手删掉，里面还有别的东西时保留。
func removeState(dataDir, agent string) error {
	if err := validateAgent(agent); err != nil {
		return err
	}
	var errs []error
	for _, path := range []string{appliedPath(dataDir, agent), stashPath(dataDir, agent)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("magpiecfg: 删除状态文件 %s: %w", path, err))
		}
	}
	_ = os.Remove(stateDir(dataDir, agent))
	return errors.Join(errs...)
}
