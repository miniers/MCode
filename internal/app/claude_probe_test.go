package app

import (
	"context"
	"errors"
	"os"
	"testing"

	"mcode/internal/providers/claude"
)

// stubClaudeProbe 替换 probeClaudeBinary 的环境来源与检测实现：用例既不读真实登录 Shell 环境，
// 也不起子进程，二进制的存在性用测试进程自身的可执行文件满足 exec.LookPath。
// 返回的各计数器由用例断言「哪些路径被走过」。
func stubClaudeProbe(t *testing.T,
	processEnv, loginEnv []string,
	check func(ctx context.Context, binary string, env []string) (claude.AuthStatus, bool),
	read func(ctx context.Context, binary string, env []string) (claude.AuthStatus, bool),
) (loginEnvCalls, readCalls *int) {
	t.Helper()
	loginEnvCalls = new(int)
	readCalls = new(int)

	prevProcessEnv, prevLoginEnv := claudeProbeProcessEnv, claudeProbeLoginEnv
	prevCheck, prevRead := claudeProbeCheck, claudeProbeReadStatus
	claudeProbeProcessEnv = func() []string { return processEnv }
	claudeProbeLoginEnv = func() []string {
		(*loginEnvCalls)++
		return loginEnv
	}
	claudeProbeCheck = check
	claudeProbeReadStatus = func(ctx context.Context, binary string, env []string) (claude.AuthStatus, bool) {
		(*readCalls)++
		return read(ctx, binary, env)
	}
	t.Cleanup(func() {
		claudeProbeProcessEnv, claudeProbeLoginEnv = prevProcessEnv, prevLoginEnv
		claudeProbeCheck, claudeProbeReadStatus = prevCheck, prevRead
	})
	return loginEnvCalls, readCalls
}

// testClaudeBin 返回一个确实存在的可执行文件，让 probeClaudeBinary 的 LookPath 通过。
func testClaudeBin(t *testing.T) string {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatalf("取测试进程可执行文件失败: %v", err)
	}
	return bin
}

// TestProbeClaudeBinaryFastPathLoggedIn 验证进程环境已登录时直接判定可用，不再读登录 Shell 环境。
func TestProbeClaudeBinaryFastPathLoggedIn(t *testing.T) {
	var checkedEnv []string
	loginEnvCalls, readCalls := stubClaudeProbe(t,
		[]string{"HOME=/tmp"},
		[]string{"HOME=/tmp", "ANTHROPIC_API_KEY=sk-ant-login"},
		func(ctx context.Context, binary string, env []string) (claude.AuthStatus, bool) {
			checkedEnv = env
			return claude.AuthStatus{LoggedIn: true, Method: "oauth_token"}, true
		},
		func(ctx context.Context, binary string, env []string) (claude.AuthStatus, bool) {
			t.Fatal("快路径已命中登录态，不应再用登录环境复核")
			return claude.AuthStatus{}, false
		},
	)

	lookPathErr, authErr := probeClaudeBinary(testClaudeBin(t))
	if lookPathErr != nil || authErr != nil {
		t.Fatalf("probeClaudeBinary = (%v, %v), want (nil, nil)", lookPathErr, authErr)
	}
	if len(checkedEnv) != 1 || checkedEnv[0] != "HOME=/tmp" {
		t.Fatalf("快路径应使用进程环境检测，got %v", checkedEnv)
	}
	if *loginEnvCalls != 0 {
		t.Fatalf("快路径命中时不应读登录环境，loginEnvCalls=%d", *loginEnvCalls)
	}
	if *readCalls != 0 {
		t.Fatalf("快路径命中时不应走无缓存检测，readCalls=%d", *readCalls)
	}
}

// TestProbeClaudeBinaryLoginEnvAPIKey 验证进程环境未登录、只有登录环境配了 API key 时仍按
// 「不需要登录」处理，且不会因快路径的未登录结论误报 ErrNotLoggedIn。
func TestProbeClaudeBinaryLoginEnvAPIKey(t *testing.T) {
	loginEnvCalls, readCalls := stubClaudeProbe(t,
		[]string{"HOME=/tmp"},
		[]string{"HOME=/tmp", "ANTHROPIC_API_KEY=sk-ant-login"},
		func(ctx context.Context, binary string, env []string) (claude.AuthStatus, bool) {
			return claude.AuthStatus{Method: "none"}, true
		},
		func(ctx context.Context, binary string, env []string) (claude.AuthStatus, bool) {
			t.Fatal("登录环境配了 API key，不应再起检测子进程")
			return claude.AuthStatus{}, false
		},
	)

	lookPathErr, authErr := probeClaudeBinary(testClaudeBin(t))
	if lookPathErr != nil || authErr != nil {
		t.Fatalf("probeClaudeBinary = (%v, %v), want (nil, nil)", lookPathErr, authErr)
	}
	if *loginEnvCalls != 1 {
		t.Fatalf("应回退到登录环境判断，loginEnvCalls=%d", *loginEnvCalls)
	}
	if *readCalls != 0 {
		t.Fatalf("不需要登录时不应执行检测，readCalls=%d", *readCalls)
	}
}

// TestProbeClaudeBinaryLoginEnvLoggedOut 验证两个环境都未登录时仍返回 ErrNotLoggedIn，
// 并确认复核走的是无缓存入口（快路径的缓存结论不会被复用）。
func TestProbeClaudeBinaryLoginEnvLoggedOut(t *testing.T) {
	loginEnvCalls, readCalls := stubClaudeProbe(t,
		[]string{"HOME=/tmp"},
		[]string{"HOME=/tmp"},
		func(ctx context.Context, binary string, env []string) (claude.AuthStatus, bool) {
			return claude.AuthStatus{Method: "none"}, true
		},
		func(ctx context.Context, binary string, env []string) (claude.AuthStatus, bool) {
			return claude.AuthStatus{Method: "none"}, true
		},
	)

	lookPathErr, authErr := probeClaudeBinary(testClaudeBin(t))
	if lookPathErr != nil {
		t.Fatalf("LookPath 不应失败: %v", lookPathErr)
	}
	if !errors.Is(authErr, claude.ErrNotLoggedIn) {
		t.Fatalf("authErr = %v, want ErrNotLoggedIn", authErr)
	}
	if *loginEnvCalls != 1 {
		t.Fatalf("应回退到登录环境判断，loginEnvCalls=%d", *loginEnvCalls)
	}
	if *readCalls != 1 {
		t.Fatalf("登录环境应复核一次，readCalls=%d", *readCalls)
	}
}

// TestProbeClaudeBinaryLoginEnvReports 验证快路径未上报（未登录或超时）时登录环境报告的已登录结果
// 会被采纳——这是「其他用户判断结果与原来一致」的关键分支。
func TestProbeClaudeBinaryLoginEnvReports(t *testing.T) {
	cases := []struct {
		name      string
		fastKnown bool
		fastLogin bool
	}{
		{name: "快路径未登录", fastKnown: true, fastLogin: false},
		{name: "快路径未上报", fastKnown: false, fastLogin: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loginEnvCalls, readCalls := stubClaudeProbe(t,
				[]string{"HOME=/tmp"},
				[]string{"HOME=/tmp", "CLAUDE_CONFIG_DIR=/tmp/claude-alt"},
				func(ctx context.Context, binary string, env []string) (claude.AuthStatus, bool) {
					return claude.AuthStatus{LoggedIn: tc.fastLogin}, tc.fastKnown
				},
				func(ctx context.Context, binary string, env []string) (claude.AuthStatus, bool) {
					if len(env) != 2 || env[1] != "CLAUDE_CONFIG_DIR=/tmp/claude-alt" {
						t.Fatalf("复核应使用登录环境，got %v", env)
					}
					return claude.AuthStatus{LoggedIn: true, Method: "oauth_token"}, true
				},
			)

			lookPathErr, authErr := probeClaudeBinary(testClaudeBin(t))
			if lookPathErr != nil || authErr != nil {
				t.Fatalf("probeClaudeBinary = (%v, %v), want (nil, nil)", lookPathErr, authErr)
			}
			if *loginEnvCalls != 1 || *readCalls != 1 {
				t.Fatalf("应各走一次登录环境与复核，loginEnvCalls=%d readCalls=%d", *loginEnvCalls, *readCalls)
			}
		})
	}
}

// TestProbeClaudeBinaryMissingBinary 验证二进制不存在时直接返回 LookPath 错误，不做任何检测。
func TestProbeClaudeBinaryMissingBinary(t *testing.T) {
	loginEnvCalls, readCalls := stubClaudeProbe(t,
		[]string{"HOME=/tmp"},
		[]string{"HOME=/tmp"},
		func(ctx context.Context, binary string, env []string) (claude.AuthStatus, bool) {
			t.Fatal("二进制不存在时不应检测登录态")
			return claude.AuthStatus{}, false
		},
		func(ctx context.Context, binary string, env []string) (claude.AuthStatus, bool) {
			t.Fatal("二进制不存在时不应检测登录态")
			return claude.AuthStatus{}, false
		},
	)

	lookPathErr, authErr := probeClaudeBinary("mcode-claude-not-installed-测试占位")
	if lookPathErr == nil {
		t.Fatal("不存在的二进制应返回 LookPath 错误")
	}
	if authErr != nil {
		t.Fatalf("authErr = %v, want nil", authErr)
	}
	if *loginEnvCalls != 0 || *readCalls != 0 {
		t.Fatalf("不应走任何检测路径，loginEnvCalls=%d readCalls=%d", *loginEnvCalls, *readCalls)
	}
}
