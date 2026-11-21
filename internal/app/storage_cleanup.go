package app

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mcode/internal/fsutil"
	mcodeui "mcode/internal/ui"
)

// moveToTrash 是存储清理使用的废纸篓移动函数，测试中可替换为桩函数。
var moveToTrash = fsutil.MoveToTrash

// StorageScan 扫描数据目录，统计总空间并只列出确定安全的四类残留候选项。
func (c *Client) StorageScan(ctx context.Context) (mcodeui.StorageReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return mcodeui.StorageReport{}, err
	}
	if c == nil {
		return mcodeui.StorageReport{}, errors.New("客户端不可用")
	}
	dataDir := c.effectiveDataDir()
	if dataDir == "" {
		return mcodeui.StorageReport{}, errors.New("数据目录不可用")
	}

	totalBytes, err := dirSizeNoSymlink(ctx, dataDir)
	if err != nil {
		return mcodeui.StorageReport{}, err
	}

	var items []mcodeui.StorageItem

	// a. 已删会话残留：仅在 store 可用时扫描，避免无法核对存活线程而误列。
	if c.backend != nil {
		if st := c.backend.Store(); st != nil {
			threadIDs, sessionIDs, err := st.ThreadAndSessionIDs(ctx)
			if err != nil {
				return mcodeui.StorageReport{}, err
			}
			deletedItems, err := scanDeletedSessionItems(ctx, dataDir, threadIDs, sessionIDs)
			if err != nil {
				return mcodeui.StorageReport{}, err
			}
			items = append(items, deletedItems...)
		}
	}

	// b. 无主补丁：仅在 swarm Manager 可用时扫描；Manager 不可用时这一类整体不列。
	c.kanbanMu.Lock()
	swarmMgr := c.swarm
	c.kanbanMu.Unlock()
	if swarmMgr != nil {
		patchItems, err := scanOrphanPatchItems(ctx, dataDir, swarmMgr.PatchPathsInUse())
		if err != nil {
			return mcodeui.StorageReport{}, err
		}
		items = append(items, patchItems...)
	}

	// c. 旧备份：swarm-worktree-backup-* 目录。
	backupItems, err := scanOldBackupItems(ctx, dataDir)
	if err != nil {
		return mcodeui.StorageReport{}, err
	}
	items = append(items, backupItems...)

	// d. 旧日志：logs/ 下轮转出的文件（如 *.1），不含当前正在写的日志。
	logItems, err := scanOldLogItems(ctx, dataDir)
	if err != nil {
		return mcodeui.StorageReport{}, err
	}
	items = append(items, logItems...)

	sort.Slice(items, func(i, j int) bool {
		if items[i].Category != items[j].Category {
			return items[i].Category < items[j].Category
		}
		return items[i].ID < items[j].ID
	})

	return mcodeui.StorageReport{
		TotalBytes: totalBytes,
		Items:      items,
	}, nil
}

// StorageClean 重新扫描后仅将同时出现在最新扫描结果中的候选项移到废纸篓，
// 其余 ID 一律忽略。部分失败时继续处理其余项并返回首个错误与实际释放字节数。
func (c *Client) StorageClean(ctx context.Context, ids []string) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if c == nil {
		return 0, errors.New("客户端不可用")
	}
	if len(ids) == 0 {
		return 0, nil
	}

	report, err := c.StorageScan(ctx)
	if err != nil {
		return 0, err
	}
	dataDir := c.effectiveDataDir()

	allowed := make(map[string]mcodeui.StorageItem, len(report.Items))
	for _, it := range report.Items {
		allowed[it.ID] = it
	}

	seen := make(map[string]bool, len(ids))
	var freed int64
	var firstErr error
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			break
		}
		item, ok := allowed[id]
		if !ok || seen[id] {
			continue
		}
		seen[id] = true

		relOS := filepath.FromSlash(item.ID)
		if !filepath.IsLocal(relOS) || relOS == "" || relOS == "." {
			continue
		}
		target := filepath.Join(dataDir, relOS)
		if info, lerr := os.Lstat(target); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if err := moveToTrash(target); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		freed += item.Bytes
	}
	return freed, firstErr
}

func scanDeletedSessionItems(ctx context.Context, dataDir string, threadIDs, sessionIDs map[string]bool) ([]mcodeui.StorageItem, error) {
	var items []mcodeui.StorageItem
	for _, dirName := range []string{"media", "tool-outputs"} {
		subDir := filepath.Join(dataDir, dirName)
		entries, err := os.ReadDir(subDir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if e.Type()&os.ModeSymlink != 0 || !e.IsDir() {
				continue
			}
			tid := e.Name()
			if tid == "" || threadIDs[tid] {
				continue
			}
			size, err := dirSizeNoSymlink(ctx, filepath.Join(subDir, tid))
			if err != nil {
				return nil, err
			}
			items = append(items, mcodeui.StorageItem{
				ID:       dirName + "/" + tid,
				Category: mcodeui.StorageCategoryDeletedSessions,
				Bytes:    size,
			})
		}
	}

	sessionsDir := filepath.Join(dataDir, "mcode", "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if e.Type()&os.ModeSymlink != 0 || e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		sid := strings.TrimSuffix(name, ".jsonl")
		if sid == "" || sessionIDs[sid] {
			continue
		}
		info, err := e.Info()
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		items = append(items, mcodeui.StorageItem{
			ID:       "mcode/sessions/" + name,
			Category: mcodeui.StorageCategoryDeletedSessions,
			Bytes:    info.Size(),
		})
	}
	return items, nil
}

func scanOrphanPatchItems(ctx context.Context, dataDir string, inUse []string) ([]mcodeui.StorageItem, error) {
	cleanDataDir := filepath.Clean(dataDir)
	realDataDir, _ := filepath.EvalSymlinks(cleanDataDir)

	inUseAbs := make(map[string]bool, len(inUse))
	inUseRel := make(map[string]bool, len(inUse))
	for _, raw := range inUse {
		if raw == "" {
			continue
		}
		cp := filepath.Clean(raw)
		inUseAbs[cp] = true
		if !filepath.IsAbs(cp) {
			inUseRel[filepath.ToSlash(cp)] = true
			continue
		}
		for _, base := range []string{cleanDataDir, realDataDir} {
			if base == "" {
				continue
			}
			if rel, err := filepath.Rel(base, cp); err == nil && filepath.IsLocal(rel) {
				inUseRel[filepath.ToSlash(rel)] = true
			}
		}
	}

	var items []mcodeui.StorageItem
	for _, sub := range []string{"swarm-landed", "swarm-reviews", "swarm-patches"} {
		dir := filepath.Join(dataDir, sub)
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if e.Type()&os.ModeSymlink != 0 || e.IsDir() {
				continue
			}
			rel := sub + "/" + e.Name()
			abs := filepath.Clean(filepath.Join(dir, e.Name()))
			if inUseRel[rel] || inUseAbs[abs] {
				continue
			}
			info, err := e.Info()
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return nil, err
			}
			items = append(items, mcodeui.StorageItem{
				ID:       rel,
				Category: mcodeui.StorageCategoryOrphanPatches,
				Bytes:    info.Size(),
			})
		}
	}
	return items, nil
}

func scanOldBackupItems(ctx context.Context, dataDir string) ([]mcodeui.StorageItem, error) {
	entries, err := os.ReadDir(dataDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var items []mcodeui.StorageItem
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if e.Type()&os.ModeSymlink != 0 || !e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "swarm-worktree-backup-") {
			continue
		}
		size, err := dirSizeNoSymlink(ctx, filepath.Join(dataDir, name))
		if err != nil {
			return nil, err
		}
		items = append(items, mcodeui.StorageItem{
			ID:       name,
			Category: mcodeui.StorageCategoryOldBackups,
			Bytes:    size,
		})
	}
	return items, nil
}

func scanOldLogItems(ctx context.Context, dataDir string) ([]mcodeui.StorageItem, error) {
	logsDir := filepath.Join(dataDir, "logs")
	entries, err := os.ReadDir(logsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	logFileMu.Lock()
	activeLogPath := logFilePath
	logFileMu.Unlock()
	if activeLogPath != "" {
		activeLogPath = filepath.Clean(activeLogPath)
	}

	var items []mcodeui.StorageItem
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if e.Type()&os.ModeSymlink != 0 || e.IsDir() {
			continue
		}
		name := e.Name()
		if name == logFileName {
			continue
		}
		abs := filepath.Clean(filepath.Join(logsDir, name))
		if activeLogPath != "" && abs == activeLogPath {
			continue
		}
		if !isRotatedLogName(name) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		items = append(items, mcodeui.StorageItem{
			ID:       "logs/" + name,
			Category: mcodeui.StorageCategoryOldLogs,
			Bytes:    info.Size(),
		})
	}
	return items, nil
}

// isRotatedLogName 判断文件名是否为按序号轮转出的旧日志（如 mcode.log.1）。
func isRotatedLogName(name string) bool {
	ext := filepath.Ext(name)
	if len(ext) <= 1 || ext[0] != '.' {
		return false
	}
	for i := 1; i < len(ext); i++ {
		if ext[i] < '0' || ext[i] > '9' {
			return false
		}
	}
	return true
}

// dirSizeNoSymlink 递归统计目录或文件字节数；遍历时不跟随符号链接，并受 ctx 超时控制。
func dirSizeNoSymlink(ctx context.Context, root string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	rootInfo, err := os.Stat(root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !rootInfo.IsDir() {
		return rootInfo.Size(), nil
	}

	var total int64
	stack := []string{root}
	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, err
		}
		for _, e := range entries {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			if e.Type()&fs.ModeSymlink != 0 {
				continue
			}
			if e.IsDir() {
				stack = append(stack, filepath.Join(dir, e.Name()))
				continue
			}
			info, err := e.Info()
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return 0, err
			}
			total += info.Size()
		}
	}
	return total, nil
}
