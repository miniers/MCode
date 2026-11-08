package claude

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 实测 claude 2.1.x 的输出样本（邮箱等私有信息已去掉）。
const (
	authJSONLoggedIn    = `{"loggedIn":true,"authMethod":"oauth_token","apiProvider":"firstParty","analyticsDisabled":true,"projectsDirectory":"/tmp/p/projects","configDirectory":"/tmp/p"}`
	authJSONLoggedOut   = `{"loggedIn":false,"authMethod":"none","apiProvider":"firstParty","analyticsDisabled":true,"projectsDirectory":"/tmp/p/projects","configDirectory":"/tmp/p"}`
	authJSONWithEmail   = `{"loggedIn":true,"authMethod":"oauth_token","email":"user@example.com","apiProvider":"firstParty","configDirectory":"/tmp/p"}`
	authJSONMissingFlag = `{"authMethod":"oauth_token","apiProvider":"firstParty"}`
)

func TestParseAuthStatus(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		want   AuthStatus
		known  bool
	}{
		{
			name:   "已登录",
			stdout: authJSONLoggedIn,
			want:   AuthStatus{LoggedIn: true, Method: "oauth_token", Provider: "firstParty", ConfigDir: "/tmp/p"},
			known:  true,
		},
		{
			name:   "未登录（CLI 退出码 1 时 stdout 仍合法）",
			stdout: authJSONLoggedOut,
			want:   AuthStatus{LoggedIn: false, Method: "none", Provider: "firstParty", ConfigDir: "/tmp/p"},
			known:  true,
		},
		{
			name:   "带邮箱字段",
			stdout: authJSONWithEmail,
			want:   AuthStatus{LoggedIn: true, Method: "oauth_token", Email: "user@example.com", Provider: "firstParty", ConfigDir: "/tmp/p"},
			known:  true,
		},
		{name: "空输出", stdout: "", known: false},
		{name: "只有空白", stdout: " \n\t ", known: false},
		{name: "非 JSON", stdout: "Not logged in. Run `claude /login`", known: false},
		{name: "JSON 数组", stdout: `[{"loggedIn":true}]`, known: false},
		{name: "缺 loggedIn 字段", stdout: authJSONMissingFlag, known: false},
		{name: "loggedIn 类型不对", stdout: `{"loggedIn":"yes"}`, known: false},
		{name: "JSON 截断", stdout: `{"loggedIn":tru`, known: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseAuthStatus([]byte(tt.stdout))
			if ok != tt.known {
				t.Fatalf("parseAuthStatus() known = %v, want %v", ok, tt.known)
			}
			if !tt.known {
				if got != (AuthStatus{}) {
					t.Fatalf("parseAuthStatus() = %+v, want 零值", got)
				}
				return
			}
			if got != tt.want {
				t.Fatalf("parseAuthStatus() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestUsesThirdPartyAPI(t *testing.T) {
	dir := t.TempDir()
	env := []string{"CLAUDE_CONFIG_DIR=" + dir}
	if UsesThirdPartyAPI(env) {
		t.Fatal("空配置不应被识别为第三方 API")
	}
	if !UsesThirdPartyAPI(append(env, "ANTHROPIC_API_KEY=example")) {
		t.Fatal("环境变量中的 API key 应被识别")
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"env":{"ANTHROPIC_AUTH_TOKEN":"example"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if !UsesThirdPartyAPI(env) {
		t.Fatal("settings.json 中的令牌应被识别")
	}
}

func TestAuthNotRequired(t *testing.T) {
	tests := []struct {
		name string
		env  []string
		want bool
	}{
		{name: "普通登录环境", env: []string{"HOME=/tmp", "PATH=/usr/bin"}, want: false},
		{name: "API key", env: []string{"ANTHROPIC_API_KEY=sk-ant-xxx"}, want: true},
		{name: "自定义网关令牌", env: []string{"ANTHROPIC_AUTH_TOKEN=token"}, want: true},
		{name: "Bedrock", env: []string{"CLAUDE_CODE_USE_BEDROCK=1"}, want: true},
		{name: "Vertex", env: []string{"CLAUDE_CODE_USE_VERTEX=1"}, want: true},
		{name: "同名变量为空串不算配置", env: []string{"ANTHROPIC_API_KEY="}, want: false},
		{name: "只匹配完整键名", env: []string{"ANTHROPIC_API_KEY_EXTRA=x"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AuthNotRequired(tt.env); got != tt.want {
				t.Fatalf("AuthNotRequired(%v) = %v, want %v", tt.env, got, tt.want)
			}
		})
	}
}

// writeAuthScript 写一个假的 claude 可执行脚本：向 stdout 输出 stdout 内容后以 exitCode 退出。
func writeAuthScript(t *testing.T, stdout string, exitCode int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude-fake")
	script := "#!/bin/sh\nprintf '%s' '" + stdout + "'\nexit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("写假 claude 脚本失败: %v", err)
	}
	return path
}

func TestReadAuthStatus(t *testing.T) {
	// 未登录时 CLI 以退出码 1 退出，但只要 stdout 是合法 JSON 就必须照常解析。
	if got, ok := ReadAuthStatus(context.Background(), writeAuthScript(t, authJSONLoggedOut, 1), nil); !ok || got.LoggedIn || got.Method != "none" {
		t.Fatalf("未登录脚本: ReadAuthStatus() = %+v, %v, want {LoggedIn:false Method:none} true", got, ok)
	}
	if got, ok := ReadAuthStatus(context.Background(), writeAuthScript(t, authJSONLoggedIn, 0), nil); !ok || !got.LoggedIn {
		t.Fatalf("已登录脚本: ReadAuthStatus() = %+v, %v, want LoggedIn true", got, ok)
	}
	// 输出非 JSON 视为未上报，而不是「未登录」。
	if got, ok := ReadAuthStatus(context.Background(), writeAuthScript(t, "boom", 1), nil); ok {
		t.Fatalf("非 JSON 输出: ReadAuthStatus() = %+v, true, want known=false", got)
	}
	// 命令不存在时也是未上报。
	if got, ok := ReadAuthStatus(context.Background(), filepath.Join(t.TempDir(), "missing"), nil); ok {
		t.Fatalf("命令不存在: ReadAuthStatus() = %+v, true, want known=false", got)
	}
}

func TestAuthCheckerCacheHit(t *testing.T) {
	resetAuthCache()
	t.Cleanup(resetAuthCache)

	var calls int32
	orig := readAuthStatusFunc
	readAuthStatusFunc = func(ctx context.Context, binary string, env []string) (AuthStatus, bool) {
		atomic.AddInt32(&calls, 1)
		if ctx.Err() != nil {
			return AuthStatus{}, false
		}
		// 标记本次读到的是哪个配置目录，便于验证缓存键包含 CLAUDE_CONFIG_DIR。
		return AuthStatus{LoggedIn: false, Method: authEnvValue(env, "CLAUDE_CONFIG_DIR")}, true
	}
	t.Cleanup(func() { readAuthStatusFunc = orig })

	checker := NewAuthChecker("claude", []string{"CLAUDE_CONFIG_DIR=/tmp/d1"})
	for i := 0; i < 3; i++ {
		status, known := checker.Check(context.Background())
		if !known || status.LoggedIn {
			t.Fatalf("第 %d 次 Check() = %+v, %v, want 未登录且 known", i, status, known)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("缓存命中: 执行次数 = %d, want 1", got)
	}

	// 不同配置目录是不同的登录态，必须重新检测。
	other := NewAuthChecker("claude", []string{"CLAUDE_CONFIG_DIR=/tmp/d2"})
	if status, known := other.Check(context.Background()); !known || status.Method != "/tmp/d2" {
		t.Fatalf("不同配置目录 Check() = %+v, %v, want 重新检测", status, known)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("缓存键: 执行次数 = %d, want 2", got)
	}
}

func TestAuthCheckerDedupesConcurrentCalls(t *testing.T) {
	resetAuthCache()
	t.Cleanup(resetAuthCache)

	var calls int32
	orig := readAuthStatusFunc
	release := make(chan struct{})
	readAuthStatusFunc = func(ctx context.Context, binary string, env []string) (AuthStatus, bool) {
		atomic.AddInt32(&calls, 1)
		<-release
		return AuthStatus{LoggedIn: true, Method: "oauth_token"}, true
	}
	t.Cleanup(func() { readAuthStatusFunc = orig })
	checker := NewAuthChecker("claude", nil)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if status, known := checker.Check(context.Background()); !known || !status.LoggedIn {
				t.Errorf("并发 Check() = %+v, %v, want 已登录", status, known)
			}
		}()
	}
	// 等首批检测进入执行中，再放行；并发调用应等待同一个结果而不是再起进程。
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("去重: 执行次数 = %d, want 1", got)
	}
}

func TestAuthCheckerDoesNotCacheCancelledCheck(t *testing.T) {
	resetAuthCache()
	t.Cleanup(resetAuthCache)

	var calls int32
	orig := readAuthStatusFunc
	readAuthStatusFunc = func(ctx context.Context, binary string, env []string) (AuthStatus, bool) {
		atomic.AddInt32(&calls, 1)
		<-ctx.Done()
		return AuthStatus{}, false
	}
	t.Cleanup(func() { readAuthStatusFunc = orig })

	checker := NewAuthChecker("claude", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, known := checker.Check(ctx); known {
		t.Fatal("超时: Check() known = true, want false")
	}

	// 超时结果不写缓存：下一次调用必须重新检测，而不是复用「未上报」缓存。
	readAuthStatusFunc = func(ctx context.Context, binary string, env []string) (AuthStatus, bool) {
		atomic.AddInt32(&calls, 1)
		return AuthStatus{LoggedIn: true}, true
	}
	if status, known := checker.Check(context.Background()); !known || !status.LoggedIn {
		t.Fatalf("重试 Check() = %+v, %v, want 已登录", status, known)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("执行次数 = %d, want 2", got)
	}
}

// ErrNotLoggedIn 的文案直接展示给用户，必须与界面约定一致。
func TestErrNotLoggedInMessage(t *testing.T) {
	if got := ErrNotLoggedIn.Error(); got != "Claude Code 尚未登录，请在终端运行 claude /login" {
		t.Fatalf("ErrNotLoggedIn = %q", got)
	}
}
