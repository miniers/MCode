package remote

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mcode/internal/remote/wire"
)

// 远程添加项目的扩展方法：
//   - projects.add：把 Mac 上的目录登记为项目，成功后广播 projects.changed；
//   - projects.validatePath：只校验路径并给出规范化结果，供 Web 输入时实时提示。
//
// 与桌面端同一条写入路径：界面选择目录后调用 chatState.addProject
// （internal/ui/model.go:1654 的 s.client.AddProject），而 Client.AddProject
// （internal/app/client.go:244）只是 c.backend.AddProjectPath 的 UI 转换，没有额外的
// 持久化或刷新动作。所以这里直接调 MemoryBackend.AddProjectPath
// （internal/remote/memory_backend.go:1709）：写内存项目表并 UpsertProject 落库，
// 桌面端刷新项目列表就能看到，不需要 *Client，也不需要 app 层注入服务。

const (
	// projectExtAddMethod 是添加项目的远程方法名。
	projectExtAddMethod = "projects.add"
	// projectExtValidatePathMethod 是只读校验路径的方法名。
	projectExtValidatePathMethod = "projects.validatePath"
	// projectExtCloneMethod 是克隆 Git 仓库并添加项目的方法名。
	projectExtCloneMethod = "projects.clone"
	// projectExtListDirMethod 是只列子目录的浏览方法名。
	projectExtListDirMethod = "projects.listDir"
	// projectExtChangedMethod 是项目列表变化后的广播方法名。
	projectExtChangedMethod = "projects.changed"
	// projectExtMaxPathLength 限制路径长度：远程输入不可信，过长的串没有意义。
	projectExtMaxPathLength = 4096
	// projectExtListDirLimit 是 projects.listDir 单次返回的子目录上限。
	projectExtListDirLimit = 500
)

// projectExtAdder 是 projects.add 需要的后端能力，由 *MemoryBackend 实现。
type projectExtAdder interface {
	AddProjectPath(ctx context.Context, path string) (wire.ProjectSummary, error)
}

func init() {
	RegisterMethod(projectExtAddMethod, handleProjectExtAdd)
	RegisterMethod(projectExtValidatePathMethod, handleProjectExtValidatePath)
	RegisterMethod(projectExtCloneMethod, handleProjectExtClone)
	RegisterMethod(projectExtListDirMethod, handleProjectExtListDir)
}

// handleProjectExtAdd 校验路径后添加项目，并广播 projects.changed。
// 重复添加同一路径不报错：AddProjectPath 直接返回已有项目。
func handleProjectExtAdd(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.ProjectExtAddParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	cleanPath, reason := projectExtNormalizePath(p.Path)
	if reason != "" {
		return nil, ExtInvalidParams("Invalid params: " + reason)
	}
	backend, ok := d.backend.(projectExtAdder)
	if !ok || backend == nil {
		return nil, ExtNotImplemented()
	}
	proj, err := backend.AddProjectPath(ctx, cleanPath)
	if err != nil {
		// 校验与添加之间目录可能被删掉或失去权限：仍属客户端输入问题，不回内部错误。
		return nil, ExtInvalidParams("Invalid params: 添加项目失败，请确认目录仍然存在且可访问")
	}
	// 项目列表没有内置的变更通知，这里补一次广播，其它已连接客户端可据此刷新。
	d.NotifyAuthed(projectExtChangedMethod, wire.ProjectExtChangedParams{ProjectID: proj.ID})
	return wire.ProjectExtAddResult{Project: proj}, nil
}

// handleProjectExtValidatePath 只校验路径，返回规范化结果或中文原因，不写入任何数据。
func handleProjectExtValidatePath(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.ProjectExtValidateParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	cleanPath, reason := projectExtNormalizePath(p.Path)
	return wire.ProjectExtValidateResult{
		OK:             reason == "",
		NormalizedPath: cleanPath,
		Reason:         reason,
	}, nil
}

// projectExtNormalizePath 校验远程传入的路径并返回规范化结果。
// 成功时返回 (绝对路径, "")；失败时返回 ("", 中文原因)。
// 规则：展开开头的 ~，Clean 后必须是已存在的目录，且不能是根目录或用户主目录。
// 只读：不做任何创建或修改。
func projectExtNormalizePath(raw string) (string, string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", "路径不能为空"
	}
	if len(trimmed) > projectExtMaxPathLength {
		return "", "路径过长"
	}
	if strings.ContainsRune(trimmed, 0) {
		return "", "路径包含非法字符"
	}
	expanded, ok := projectExtExpandHome(trimmed)
	if !ok {
		return "", "无法解析当前用户主目录"
	}
	if !filepath.IsAbs(expanded) {
		return "", "路径必须是绝对路径（以 / 开头）"
	}
	cleanPath := filepath.Clean(expanded)
	if cleanPath == string(filepath.Separator) {
		return "", "不能把根目录添加为项目"
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && cleanPath == filepath.Clean(home) {
		return "", "不能把主目录添加为项目"
	}
	fi, err := os.Stat(cleanPath)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return "", "路径不存在"
		case errors.Is(err, fs.ErrPermission):
			return "", "没有权限访问该路径"
		default:
			return "", "无法访问该路径"
		}
	}
	if !fi.IsDir() {
		return "", "不是目录"
	}
	return cleanPath, ""
}

// projectExtHomeDirFunc 返回当前用户主目录，测试里可替换成临时目录。
var projectExtHomeDirFunc = os.UserHomeDir

// projectExtExpandHome 把开头的 ~ 或 ~/ 展开成当前用户主目录。
// 其它形式的路径原样返回；读不到主目录时返回 false。
// ~user 这类跨用户写法不支持，交给后面的绝对路径检查拒绝。
func projectExtExpandHome(path string) (string, bool) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, true
	}
	home, err := projectExtHomeDirFunc()
	if err != nil || home == "" {
		return "", false
	}
	if path == "~" {
		return home, true
	}
	return filepath.Join(home, path[2:]), true
}

// handleProjectExtClone 校验地址与父目录后交给宿主执行 git clone，
// 成功后按 projects.add 同一路径登记项目并广播 projects.changed。
// 克隆在宿主上同步执行（最长 10 分钟），handler 本身不持任何锁。
func handleProjectExtClone(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.ProjectExtCloneParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	url, reason := projectExtNormalizeCloneURL(p.URL)
	if reason != "" {
		return nil, ExtInvalidParams("Invalid params: " + reason)
	}
	name := ""
	if p.Name != nil {
		name = strings.TrimSpace(*p.Name)
	}
	if name == "" {
		name = projectExtCloneDirName(url)
	}
	if reason := projectExtValidateCloneName(name); reason != "" {
		return nil, ExtInvalidParams("Invalid params: " + reason)
	}
	parentRaw := ""
	if p.ParentPath != nil {
		parentRaw = *p.ParentPath
	}
	// 桌面端没有默认克隆目录（必须先选目录），远程缺省时落到用户主目录。
	parentDir, reason := projectExtResolveBrowseDir(parentRaw)
	if reason != "" {
		return nil, ExtInvalidParams("Invalid params: " + reason)
	}

	backend, ok := d.backend.(projectExtCloner)
	if !ok || backend == nil {
		return nil, ExtNotImplemented()
	}
	proj, err := backend.CloneProject(ctx, url, parentDir, name)
	if err != nil {
		if errors.Is(err, errProjectCloneTargetExists) {
			return nil, ExtInvalidParams("Invalid params: " + err.Error())
		}
		return nil, &ExtError{Code: wire.ErrCodeOperationRejected, Message: "克隆失败：" + err.Error()}
	}
	// 与 projects.add 一致：项目列表没有内置通知，这里补一次广播。
	d.NotifyAuthed(projectExtChangedMethod, wire.ProjectExtChangedParams{ProjectID: proj.ID})
	return wire.ProjectExtCloneResult{Project: proj}, nil
}

// handleProjectExtListDir 只列出子目录（不列文件），供远程选择项目目录。
// 路径必须位于用户主目录之内（解析符号链接后判断），否则返回参数错误。
func handleProjectExtListDir(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.ProjectExtListDirParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	home, err := projectExtHomeDirFunc()
	if err != nil || home == "" {
		return nil, ExtInvalidParams("Invalid params: 无法解析当前用户主目录")
	}
	homeResolved, err := filepath.EvalSymlinks(filepath.Clean(home))
	if err != nil || homeResolved == "" {
		homeResolved = filepath.Clean(home)
	}
	rawPath := ""
	if p.Path != nil {
		rawPath = *p.Path
	}
	dir, reason := projectExtResolveBrowseDir(rawPath)
	if reason != "" {
		return nil, ExtInvalidParams("Invalid params: " + reason)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, ExtInvalidParams("Invalid params: 无法解析该路径（符号链接可能已失效）")
	}
	if !projectExtWithinHome(resolved, homeResolved) {
		return nil, ExtInvalidParams("Invalid params: 路径必须位于用户主目录之内")
	}
	entries, truncated, err := projectExtReadSubdirs(resolved, p.ShowHidden)
	if err != nil {
		return nil, ExtInvalidParams("Invalid params: 无法读取该目录")
	}
	result := wire.ProjectExtListDirResult{Path: resolved, Entries: entries, Truncated: truncated}
	if parent := filepath.Dir(resolved); projectExtWithinHome(parent, homeResolved) {
		result.Parent = &parent
	}
	return result, nil
}

// projectExtResolveBrowseDir 解析浏览/克隆用的目录路径：空串表示用户主目录。
// 成功时返回 (绝对路径, "")；失败时返回 ("", 中文原因)。
// 与 projectExtNormalizePath 的区别：允许是用户主目录本身，因为浏览与克隆父目录都可以是主目录。
func projectExtResolveBrowseDir(raw string) (string, string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		trimmed = "~"
	}
	if len(trimmed) > projectExtMaxPathLength {
		return "", "路径过长"
	}
	if strings.ContainsRune(trimmed, 0) {
		return "", "路径包含非法字符"
	}
	expanded, ok := projectExtExpandHome(trimmed)
	if !ok {
		return "", "无法解析当前用户主目录"
	}
	if !filepath.IsAbs(expanded) {
		return "", "路径必须是绝对路径（以 / 开头）"
	}
	cleanPath := filepath.Clean(expanded)
	fi, err := os.Stat(cleanPath)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return "", "路径不存在"
		case errors.Is(err, fs.ErrPermission):
			return "", "没有权限访问该路径"
		default:
			return "", "无法访问该路径"
		}
	}
	if !fi.IsDir() {
		return "", "不是目录"
	}
	return cleanPath, ""
}

// projectExtWithinHome 判断 path 是否位于 home 之内（含 home 本身）。
// 两个参数都应已经过符号链接解析，比较前再做一次 Clean。
func projectExtWithinHome(path, home string) bool {
	rel, err := filepath.Rel(filepath.Clean(home), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// projectExtReadSubdirs 读取目录下的子目录（跟随符号链接判断类型，失效链接跳过），
// 按名称不区分大小写排序，超过 projectExtListDirLimit 时截断。
func projectExtReadSubdirs(dir string, showHidden bool) ([]wire.ProjectExtDirEntry, bool, error) {
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil, false, err
	}
	entries := make([]wire.ProjectExtDirEntry, 0, len(items))
	for _, item := range items {
		name := item.Name()
		if !showHidden && strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(dir, name)
		fi, err := os.Stat(full)
		if err != nil || !fi.IsDir() {
			continue
		}
		entries = append(entries, wire.ProjectExtDirEntry{
			Name:      name,
			Path:      full,
			IsGitRepo: projectExtIsGitRepo(full),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		li, lj := strings.ToLower(entries[i].Name), strings.ToLower(entries[j].Name)
		if li != lj {
			return li < lj
		}
		return entries[i].Name < entries[j].Name
	})
	if len(entries) > projectExtListDirLimit {
		return entries[:projectExtListDirLimit], true, nil
	}
	return entries, false, nil
}

// projectExtIsGitRepo 判断目录下是否存在 .git（工作区或 worktree 的 .git 文件都算）。
func projectExtIsGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}
