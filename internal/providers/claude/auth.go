package claude

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"mcode/internal/procattr"
)

// 登录态检测：claude 二进制存在不代表可用——没登录时 CLI 要等到真正发消息才在错误里报出来。
// 这里执行一次性子命令 `claude auth status --json` 提前拿到登录态，供启动路径与设置页使用。

const (
	// authStatusTimeout 是单次 `claude auth status --json` 的硬超时，避免 CLI 卡住时长期占用调用方。
	authStatusTimeout = 5 * time.Second
	// AuthProbeBudget 是启动路径等待登录态检测的预算：正常约 0.15 秒返回，
	// 超过预算就按「未上报」（known=false）处理，绝不让登录检测拖慢启动。
	AuthProbeBudget = 500 * time.Millisecond
	// authCacheTTL 是登录态结果的缓存时长，避免设置页反复刷新时反复起子进程。
	authCacheTTL = 60 * time.Second
)

// ErrNotLoggedIn 是 claude CLI 已安装但本机未登录时给出的不可用原因（可直接展示给用户）。
var ErrNotLoggedIn = errors.New("Claude Code 尚未登录，请在终端运行 claude /login")

// authSkipKeys 列出本机走自定义网关/API key/云厂商时不需要 claude.ai 登录的环境变量。
var authSkipKeys = []string{
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"CLAUDE_CODE_USE_BEDROCK",
	"CLAUDE_CODE_USE_VERTEX",
}

// AuthStatus 是 `claude auth status --json` 的解析结果。
// 实测 CLI 2.1.x 的输出字段（authMethod 取值 oauth_token / api_key / none）：
//
//	{"loggedIn":true,"authMethod":"oauth_token","apiProvider":"firstParty",
//	 "analyticsDisabled":true,"projectsDirectory":"...","configDirectory":"..."}
//
// 未登录时同样输出合法 JSON，只是 loggedIn=false、authMethod=none，且进程以退出码 1 退出；
// email 只在部分版本/登录方式下出现，缺失时留空。
type AuthStatus struct {
	// LoggedIn 表示本机已有可用的登录凭证。
	LoggedIn bool
	// Method 是 authMethod 字段（oauth_token、api_key、none 等）。
	Method string
	// Email 是登录账号邮箱（字段 email），CLI 未上报时为空；不要在日志里打印。
	Email string
	// Provider 是 apiProvider 字段（firstParty、bedrock、vertex 等）。
	Provider string
	// ConfigDir 是 configDirectory 字段，即本次检测实际使用的配置目录。
	ConfigDir string
}

// authStatusJSON 是 `claude auth status --json` 的原始字段映射。
// LoggedIn 用指针区分「字段缺失」与「值为 false」：字段缺失视为未上报。
type authStatusJSON struct {
	LoggedIn    *bool  `json:"loggedIn"`
	AuthMethod  string `json:"authMethod"`
	Email       string `json:"email"`
	APIProvider string `json:"apiProvider"`
	ConfigDir   string `json:"configDirectory"`
}

// parseAuthStatus 解析命令 stdout。要求整段输出是一个含 loggedIn 字段的 JSON 对象；
// 空输出、非 JSON、缺字段都返回 known=false。
func parseAuthStatus(stdout []byte) (AuthStatus, bool) {
	trimmed := strings.TrimSpace(string(stdout))
	if trimmed == "" {
		return AuthStatus{}, false
	}
	var raw authStatusJSON
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil || raw.LoggedIn == nil {
		return AuthStatus{}, false
	}
	return AuthStatus{
		LoggedIn:  *raw.LoggedIn,
		Method:    strings.TrimSpace(raw.AuthMethod),
		Email:     strings.TrimSpace(raw.Email),
		Provider:  strings.TrimSpace(raw.APIProvider),
		ConfigDir: strings.TrimSpace(raw.ConfigDir),
	}, true
}

// ReadAuthStatus 执行 `binary auth status --json` 并解析登录态。
//   - env 为空时子进程继承当前进程环境；非空时按传入的完整环境变量列表启动子进程，
//     与 claude.go 里启动 CLI 时使用 loginenv.Environ() 的方式保持一致；
//   - 命令不存在、超时、输出非 JSON 都返回 known=false（视为「未上报」，由调用方按可用处理）；
//   - 未登录时 CLI 以退出码 1 退出但 stdout 仍是合法 JSON，因此不检查退出码，只解析 stdout。
func ReadAuthStatus(ctx context.Context, binary string, env []string) (AuthStatus, bool) {
	if strings.TrimSpace(binary) == "" {
		binary = "claude"
	}
	runCtx, cancel := context.WithTimeout(ctx, authStatusTimeout)
	defer cancel()

	cmd := procattr.HideWindow(exec.CommandContext(runCtx, binary, "auth", "status", "--json"))
	if len(env) > 0 {
		cmd.Env = env
	}
	// 忽略退出码与错误：未登录时 CLI 以退出码 1 退出，stdout 仍是合法 JSON，只解析 stdout。
	stdout, _ := cmd.Output()
	return parseAuthStatus(stdout)
}

// AuthNotRequired 判断给定环境是否走本地不需要 claude.ai 登录的路径
// （API key、自定义网关鉴权令牌、Bedrock、Vertex）。命中时调用方应跳过登录态检测。
func AuthNotRequired(env []string) bool {
	if env == nil {
		env = os.Environ()
	}
	for _, key := range authSkipKeys {
		if authEnvValue(env, key) != "" {
			return true
		}
	}
	return false
}

// authEnvValue 取环境变量列表 env 中 key 的值（最后出现的项优先，与子进程行为一致）。
func authEnvValue(env []string, key string) string {
	prefix := key + "="
	value := ""
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			value = e[len(prefix):]
		}
	}
	return value
}

// readAuthStatusFunc 是 AuthChecker 读取登录态的实现，单测可替换成假实现。
var readAuthStatusFunc = ReadAuthStatus

// authCacheEntry 是一条缓存结果；known=false 也缓存，避免反复起注定失败的子进程。
type authCacheEntry struct {
	status AuthStatus
	known  bool
	at     time.Time
}

// authFlight 表示一次进行中的检测，供并发调用方等待同一个结果（singleflight 式去重）。
type authFlight struct {
	done chan struct{}
}

var (
	authMu       sync.Mutex
	authCache    = map[string]authCacheEntry{}
	authInFlight = map[string]*authFlight{}
)

// AuthChecker 带缓存与并发去重的登录态检测器，可安全并发调用。
type AuthChecker struct {
	// Binary 是 claude 可执行文件（绝对路径或 PATH 中的名字）。
	Binary string
	// Env 是子进程环境变量；为空表示继承当前进程环境。
	Env []string
}

// NewAuthChecker 构造检测器；binary 为空时回退到 "claude"。
func NewAuthChecker(binary string, env []string) *AuthChecker {
	return &AuthChecker{Binary: binary, Env: env}
}

// Check 返回登录态；known=false 表示未上报（超时、命令不存在、输出非 JSON），调用方应按可用处理。
// 结果按 binary + CLAUDE_CONFIG_DIR 缓存 authCacheTTL；同一键的并发调用只起一个子进程。
// ctx 被取消导致的失败不写缓存，避免把「没来得及检测」当成结果缓存下来。
func (c *AuthChecker) Check(ctx context.Context) (AuthStatus, bool) {
	if c == nil {
		return AuthStatus{}, false
	}
	key := c.cacheKey()

	authMu.Lock()
	if entry, ok := authCache[key]; ok && time.Since(entry.at) < authCacheTTL {
		authMu.Unlock()
		return entry.status, entry.known
	}
	if flight, ok := authInFlight[key]; ok {
		authMu.Unlock()
		select {
		case <-flight.done:
			return c.cached(key)
		case <-ctx.Done():
			return AuthStatus{}, false
		}
	}
	flight := &authFlight{done: make(chan struct{})}
	authInFlight[key] = flight
	authMu.Unlock()

	status, known := readAuthStatusFunc(ctx, c.Binary, c.Env)

	authMu.Lock()
	if ctx.Err() == nil {
		authCache[key] = authCacheEntry{status: status, known: known, at: time.Now()}
	}
	delete(authInFlight, key)
	close(flight.done)
	authMu.Unlock()
	return status, known
}

// cached 读取 Check 之后的缓存结果（调用方需保证对应 flight 已结束）。
func (c *AuthChecker) cached(key string) (AuthStatus, bool) {
	authMu.Lock()
	defer authMu.Unlock()
	entry, ok := authCache[key]
	if !ok {
		return AuthStatus{}, false
	}
	return entry.status, entry.known
}

// cacheKey 由 binary 与 CLAUDE_CONFIG_DIR 组成：两者不同代表在检测不同账号的登录态。
func (c *AuthChecker) cacheKey() string {
	binary := strings.TrimSpace(c.Binary)
	if binary == "" {
		binary = "claude"
	}
	return binary + "\x00" + authEnvValue(c.Env, "CLAUDE_CONFIG_DIR")
}

// resetAuthCache 清空登录态缓存，仅供测试使用。
func resetAuthCache() {
	authMu.Lock()
	defer authMu.Unlock()
	authCache = map[string]authCacheEntry{}
	authInFlight = map[string]*authFlight{}
}
