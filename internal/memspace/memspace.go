// Package memspace 为 agent 子进程解析 Nowledge Mem 的记忆空间（NMEM_SPACE）。
//
// 只做「按项目目录匹配已存在的空间」：仓库根目录名（worktree 取主仓库）小写后，
// 与 nmem 中已有空间的 name/key/alias 比对，命中才注入；绝不触发新建空间。
// 任何失败都静默返回 nil，agent 走 Default 空间。
package memspace

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mcode/internal/nmem"
	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
)

const (
	envKey   = "NMEM_SPACE"
	cacheTTL = time.Minute
	cmdLimit = 3 * time.Second
)

type cacheEntry struct {
	value string
	at    time.Time
}

var (
	mu    sync.Mutex
	cache = map[string]cacheEntry{}
	// restCache 缓存 Resolve 的结果（值为 space ID），key 为 workDir；
	// 与 Env 用的 cache（值为小写空间名）分开，避免两种取值互相污染。
	restCache = map[string]cacheEntry{}

	// 包级可替换钩子，方便测试注入。
	runCmd = defaultRunCmd
	now    = time.Now
)

// Env 返回需要追加到 agent 子进程环境的 NMEM_SPACE=<space>；
// 用户已在环境中显式设置 NMEM_SPACE、workDir 不在 git 仓库内、或没有同名空间时返回 nil。
func Env(workDir string) []string {
	if workDir == "" || explicitlySet() {
		return nil
	}
	if space := resolve(workDir); space != "" {
		return []string{envKey + "=" + space}
	}
	return nil
}

func explicitlySet() bool {
	if strings.TrimSpace(os.Getenv(envKey)) != "" {
		return true
	}
	for _, kv := range loginenv.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok && k == envKey && strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

func resolve(workDir string) string {
	mu.Lock()
	if e, ok := cache[workDir]; ok && now().Sub(e.at) < cacheTTL {
		mu.Unlock()
		return e.value
	}
	mu.Unlock()

	value := lookup(workDir)

	mu.Lock()
	cache[workDir] = cacheEntry{value: value, at: now()}
	mu.Unlock()
	return value
}

// Resolve 返回 workDir 对应的 Nowledge 记忆空间 ID（用于 REST 接口的 space_id）。
//
// override 非空时直接返回该值（人工覆盖的空间名）；否则沿用 Env 的规则取仓库根目录名
// （worktree 取主仓库）小写后，与 c 列出的已有空间比对，命中返回 Space.ID。
// 未命中、c 为 nil、不在 git 仓库内或任何请求失败都返回空串，由调用方回落到 Default 空间。
func Resolve(ctx context.Context, c *nmem.Client, workDir, override string) string {
	if v := strings.TrimSpace(override); v != "" {
		return v
	}
	if c == nil || workDir == "" {
		return ""
	}

	mu.Lock()
	if e, ok := restCache[workDir]; ok && now().Sub(e.at) < cacheTTL {
		mu.Unlock()
		return e.value
	}
	mu.Unlock()

	value, ok := lookupREST(ctx, c, workDir)

	// 请求失败时不写缓存，下一次调用还能重试；确定性的「没有同名空间」照常缓存。
	if ok {
		mu.Lock()
		restCache[workDir] = cacheEntry{value: value, at: now()}
		mu.Unlock()
	}
	return value
}

// Resolution 是项目路径解析记忆空间的结果，供设置页展示「这个项目用哪个空间」。
type Resolution struct {
	// Space 是候选或命中的空间名：override 非空时是 override，否则是仓库目录名（小写）。
	Space string
	// ID 是命中空间的 ID；未命中时为空。
	ID string
	// FromOverride 表示本次解析用的是人工覆盖的空间名。
	FromOverride bool
	// Hit 表示在已有空间列表里按 name/key/alias 找到了匹配的空间。
	Hit bool
}

// Describe 解析 workDir 对应的记忆空间并带上命中详情，供设置页展示。
//
// override 非空时按它匹配（FromOverride 为 true）；否则沿用 Env 的规则取仓库目录名
// （worktree 取主仓库）小写后匹配。命中时 Space/ID 填命中空间的名称与 ID；未命中时
// Space 仍是候选名、ID 为空。c 为 nil、不在 git 仓库内或请求失败时只返回候选名，不报错。
func Describe(ctx context.Context, c *nmem.Client, workDir, override string) Resolution {
	want := strings.TrimSpace(override)
	res := Resolution{Space: want, FromOverride: want != ""}
	if want == "" {
		if workDir == "" {
			return res
		}
		name, err := repoSpaceName(workDir)
		if err != nil || name == "" {
			return res
		}
		want = name
		res.Space = want
	}
	if c == nil {
		return res
	}
	spaces, err := listSpaces(ctx, c)
	if err != nil {
		return res
	}
	if space, ok := nmem.ResolveSpace(spaces, want); ok {
		res.Space = space.Name
		res.ID = space.ID
		res.Hit = true
	}
	return res
}

// listSpaces 拉取空间列表，复用 Env 的 3 秒超时；调用方已有更早的截止时间时沿用调用方的。
func listSpaces(ctx context.Context, c *nmem.Client) ([]nmem.Space, error) {
	reqCtx, cancel := context.WithTimeout(ctx, cmdLimit)
	defer cancel()
	return c.Spaces(reqCtx)
}

// lookupREST 用 REST 客户端解析空间 ID；第二个返回值表示该结果是否可以缓存
// （请求失败时为 false）。
func lookupREST(ctx context.Context, c *nmem.Client, workDir string) (string, bool) {
	want, err := repoSpaceName(workDir)
	if err != nil || want == "" {
		return "", true
	}

	spaces, err := listSpaces(ctx, c)
	if err != nil {
		return "", false
	}
	if space, ok := nmem.ResolveSpace(spaces, want); ok {
		return space.ID, true
	}
	return "", true
}

// repoSpaceName 返回 workDir 对应的仓库目录名（小写，worktree 取主仓库），
// 不在 git 仓库内或目录名不可用时返回空串。
func repoSpaceName(workDir string) (string, error) {
	out, err := runCmd(workDir, "git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	common := strings.TrimSpace(string(out))
	if common == "" {
		return "", nil
	}
	want := strings.ToLower(filepath.Base(filepath.Dir(common)))
	if want == "" || want == "." || want == string(filepath.Separator) || want == "default" {
		return "", nil
	}
	return want, nil
}

func lookup(workDir string) string {
	want, err := repoSpaceName(workDir)
	if err != nil || want == "" {
		return ""
	}

	out, err := runCmd(workDir, "nmem", "--json", "spaces", "list")
	if err != nil {
		return ""
	}
	var list struct {
		Spaces []struct {
			Name    string   `json:"name"`
			Key     string   `json:"key"`
			Aliases []string `json:"aliases"`
		} `json:"spaces"`
	}
	if json.Unmarshal(out, &list) != nil {
		return ""
	}
	for _, s := range list.Spaces {
		names := append([]string{s.Name, s.Key}, s.Aliases...)
		for _, n := range names {
			if strings.ToLower(n) == want {
				return want
			}
		}
	}
	return ""
}

func defaultRunCmd(dir, name string, args ...string) ([]byte, error) {
	path, err := loginenv.LookPath(name)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), cmdLimit)
	defer cancel()
	cmd := procattr.HideWindow(exec.CommandContext(ctx, path, args...))
	cmd.Dir = dir
	cmd.Env = loginenv.Environ()
	return cmd.Output()
}
