package magpiecfg

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// zeroedBackupLayout 是修复前把全零文件另存出来时用的时间戳格式。
const zeroedBackupLayout = "20060102-150405"

// RepairZeroed 修复被掉电写成整份 0x00 的接管配置，返回修复过的配置文件路径。
//
// Windows 上文件长度先落盘、数据后落盘：写到一半掉电会留下一个长度正确、
// 内容全零的文件。它既不是空文件也不是合法 TOML/JSON，checkConfig 只会拒绝
// 改动（见 connect.go），于是 codex 这类 agent 每次都起不来，而谁也不会去修。
// 全零文件里没有任何用户内容可丢，所以这里把它另存为
// <path>.zeroed-<时间>.bak，再按 applied 记录把 mcode 管理的字段写回去——只
// 补回我们写过的值，其余内容本来就已经没了。
//
// 只处理 <数据目录>/agentcfg 下有接管记录的 agent；文件正常、不存在或没有
// 记录时什么都不做。
func RepairZeroed(dataDir string) ([]string, error) {
	agents, err := agentStateDirs(dataDir)
	if err != nil {
		return nil, err
	}
	var repaired []string
	var errs []error
	for _, agent := range agents {
		paths, err := repairZeroedAgent(dataDir, agent)
		repaired = append(repaired, paths...)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return repaired, errors.Join(errs...)
}

// agentStateDirs 列出 <数据目录>/agentcfg 下的 agent 名；目录不存在时返回
// 空列表。
func agentStateDirs(dataDir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(dataDir, "agentcfg"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("magpiecfg: 读取 agentcfg 目录: %w", err)
	}
	var agents []string
	for _, entry := range entries {
		if entry.IsDir() {
			agents = append(agents, entry.Name())
		}
	}
	slices.Sort(agents)
	return agents, nil
}

// repairZeroedAgent 修复一个 agent 的接管文件。单个文件失败不影响其它文件，
// 已修好的路径照样返回。
func repairZeroedAgent(dataDir, agent string) ([]string, error) {
	applied, err := loadApplied(dataDir, agent)
	if err != nil {
		return nil, err
	}
	var repaired []string
	var errs []error
	for _, path := range slices.Sorted(maps.Keys(applied.Files)) {
		target := applied.Files[path]
		raw, err := Read(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("magpiecfg: 读取 %s: %w", path, err))
			continue
		}
		if !allNUL(raw) {
			continue
		}
		// 先把记录还原成字段：记录本身坏掉时不动文件，留给用户处理。
		fields := make([]Field, 0, len(target.Fields))
		bad := false
		for _, key := range slices.Sorted(maps.Keys(target.Fields)) {
			f, err := decodeRecordedField(Format(target.Format), key, target.Fields[key])
			if err != nil {
				errs = append(errs, fmt.Errorf("magpiecfg: 恢复 %s 的 %s: %w", path, key, err))
				bad = true
				break
			}
			fields = append(fields, f)
		}
		if bad {
			continue
		}
		if err := backupZeroed(path, raw); err != nil {
			errs = append(errs, err)
			continue
		}
		// 先清空再按记录写回：全零文件不是有效的 TOML/JSON，字段编辑原语
		// 没法在它上面工作。写回失败时把全零内容放回去，文件状态不变。
		if err := WriteAtomic(path, nil); err != nil {
			errs = append(errs, fmt.Errorf("magpiecfg: 清空 %s: %w", path, err))
			continue
		}
		if err := writeFileFields(FilePlan{Path: path, Format: Format(target.Format), Fields: fields}); err != nil {
			_ = WriteAtomic(path, raw)
			errs = append(errs, err)
			continue
		}
		repaired = append(repaired, path)
	}
	return repaired, errors.Join(errs...)
}

// allNUL 报告 b 是否非空且每个字节都是 0x00。
func allNUL(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// backupZeroed 把全零文件原样另存为 <path>.zeroed-<时间>.bak；同名时加序号，
// 不覆盖更早的备份。
func backupZeroed(path string, raw []byte) error {
	stamp := time.Now().Format(zeroedBackupLayout)
	for i := 0; i < 100; i++ {
		name := path + ".zeroed-" + stamp + ".bak"
		if i > 0 {
			name = fmt.Sprintf("%s.zeroed-%s-%d.bak", path, stamp, i)
		}
		f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("magpiecfg: 备份 %s: %w", path, err)
		}
		_, werr := f.Write(raw)
		cerr := f.Close()
		if werr != nil || cerr != nil {
			return fmt.Errorf("magpiecfg: 备份 %s: %w", path, errors.Join(werr, cerr))
		}
		return nil
	}
	return fmt.Errorf("magpiecfg: 备份 %s: 同名备份过多", path)
}
