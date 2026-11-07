package cliupdate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// stubRunCommand 替换 runCommand，测试结束自动恢复。
func stubRunCommand(t *testing.T, command func(context.Context, string, ...string) (string, error)) {
	t.Helper()
	previous := runCommand
	runCommand = command
	t.Cleanup(func() { runCommand = previous })
}

// versionCommand 让 runCommand 只接受 `--version` 并返回固定输出。
func versionCommand(output string) func(context.Context, string, ...string) (string, error) {
	return func(_ context.Context, name string, args ...string) (string, error) {
		if len(args) == 1 && args[0] == "--version" {
			return output, nil
		}
		return "", fmt.Errorf("测试里出现了未预期的命令: %s %v", name, args)
	}
}

// stubRegistryURL 替换某个 provider 的来源地址，测试结束自动恢复。
func stubRegistryURL(t *testing.T, provider, url string) {
	t.Helper()
	previous, existed := registryURLs[provider]
	registryURLs[provider] = url
	t.Cleanup(func() {
		if existed {
			registryURLs[provider] = previous
			return
		}
		delete(registryURLs, provider)
	})
}

// stubHTTPClient 替换 HTTP 客户端，测试结束自动恢复。
func stubHTTPClient(t *testing.T, client *http.Client) {
	t.Helper()
	previous := httpClient
	httpClient = client
	t.Cleanup(func() { httpClient = previous })
}

// stubNPMRegistryBase 替换 npm registry 根地址（OpenCode 2.x 的包名在运行时拼 URL），测试结束自动恢复。
func stubNPMRegistryBase(t *testing.T, base string) {
	t.Helper()
	previous := npmRegistryBase
	npmRegistryBase = base
	t.Cleanup(func() { npmRegistryBase = previous })
}

func TestParseVersion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{name: "claude 输出", output: "2.0.14 (Claude Code)", want: "2.0.14"},
		{name: "codex 输出", output: "codex-cli 0.46.0", want: "0.46.0"},
		{name: "v 前缀", output: "v1.2.3", want: "1.2.3"},
		{name: "缺一段的版本", output: "1.2", want: "1.2"},
		{name: "预发布后缀", output: "1.2.3-beta.1", want: "1.2.3-beta"},
		{name: "构建后缀", output: "1.2.3+build.5", want: "1.2.3"},
		{name: "多行输出取第一个", output: "droid 0.25.0\ndroid 0.25.0", want: "0.25.0"},
		{name: "ANSI 颜色", output: "\x1b[32m2.1.0\x1b[0m", want: "2.1.0"},
		{name: "空白输出", output: "   \n", want: ""},
		{name: "没有版本号", output: "command not found", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ParseVersion(test.output); got != test.want {
				t.Fatalf("ParseVersion(%q) = %q, want %q", test.output, got, test.want)
			}
		})
	}
}

func TestCompareVersions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tests := []struct {
		name string
		a    string
		b    string
		want int
	}{
		{name: "相等", a: "1.2.3", b: "1.2.3", want: 0},
		{name: "补零后相等", a: "1.2", b: "1.2.0", want: 0},
		{name: "补零后相等（反向）", a: "1.2.0", b: "1.2", want: 0},
		{name: "补零后更大", a: "1.2.1", b: "1.2", want: 1},
		{name: "补零后更小", a: "1.2", b: "1.2.1", want: -1},
		{name: "按数字大小而非字典序", a: "1.9.0", b: "1.10.0", want: -1},
		{name: "主版本更大", a: "2.0.0", b: "1.99.99", want: 1},
		{name: "预发布后缀不参与比较", a: "2.0.24-beta", b: "2.0.24", want: 0},
		{name: "预发布后缀不参与比较（反向）", a: "2.0.24", b: "2.0.24-beta", want: 0},
		{name: "预发布但数字更大", a: "2.0.25-beta", b: "2.0.24", want: 1},
		{name: "v 前缀", a: "v1.2.3", b: "1.2.3", want: 0},
		{name: "v 前缀更旧", a: "v1.2.2", b: "1.2.3", want: -1},
		{name: "构建元数据不参与比较", a: "1.2.3+build.5", b: "1.2.3", want: 0},
		{name: "两侧都是空串", a: "", b: "", want: 0},
		{name: "空串对有效版本", a: "", b: "1.2.3", want: 0},
		{name: "有效版本对空串", a: "1.2.3", b: "", want: 0},
		{name: "无法解析", a: "not-a-version", b: "1.2.3", want: 0},
		{name: "只有一段数字", a: "1", b: "0.9", want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := CompareVersions(test.a, test.b); got != test.want {
				t.Fatalf("CompareVersions(%q, %q) = %d, want %d", test.a, test.b, got, test.want)
			}
		})
	}
}

func TestInstalled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var calledName string
	var calledArgs []string
	stubRunCommand(t, func(_ context.Context, name string, args ...string) (string, error) {
		calledName, calledArgs = name, args
		return "2.0.14 (Claude Code)", nil
	})

	info, err := Installed(context.Background(), "claude", "/usr/local/bin/claude")
	if err != nil {
		t.Fatalf("Installed 出错: %v", err)
	}
	if calledName != "/usr/local/bin/claude" || !slices.Equal(calledArgs, []string{"--version"}) {
		t.Fatalf("命令 = %s %v, want /usr/local/bin/claude [--version]", calledName, calledArgs)
	}
	if info.Provider != "claude" || info.ExecutablePath != "/usr/local/bin/claude" {
		t.Fatalf("Info 的 provider/executable 不对: %+v", info)
	}
	if info.Installed != "2.0.14" {
		t.Fatalf("Installed = %q, want 2.0.14", info.Installed)
	}
}

func TestInstalledErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Run("命令失败", func(t *testing.T) {
		stubRunCommand(t, func(context.Context, string, ...string) (string, error) {
			return "", errors.New("exec: not found")
		})
		if _, err := Installed(context.Background(), "claude", "claude"); err == nil {
			t.Fatal("命令失败时应当报错")
		}
	})
	t.Run("输出里没有版本号", func(t *testing.T) {
		stubRunCommand(t, versionCommand("no version here"))
		if _, err := Installed(context.Background(), "claude", "claude"); err == nil {
			t.Fatal("解析不出版本号时应当报错")
		}
	})
	t.Run("没有可执行文件", func(t *testing.T) {
		called := false
		stubRunCommand(t, func(context.Context, string, ...string) (string, error) {
			called = true
			return "", nil
		})
		if _, err := Installed(context.Background(), "claude", ""); err == nil {
			t.Fatal("executable 为空时应当报错")
		}
		if called {
			t.Fatal("executable 为空时不应执行命令")
		}
	})
}

func TestUpdateCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tests := []struct {
		provider string
		want     []string
	}{
		{provider: "claude", want: []string{"claude", "update"}},
		{provider: "codex", want: []string{"codex", "update"}},
		{provider: "agy", want: []string{"agy", "update"}},
		{provider: "antigravity", want: []string{"agy", "update"}},
		{provider: "cursor", want: []string{"cursor-agent", "update"}},
		{provider: "grok", want: []string{"grok", "update"}},
		{provider: "copilot", want: []string{"copilot", "update"}},
		{provider: "commandcode", want: []string{"cmd", "update"}},
		{provider: "opencode", want: []string{"opencode", "upgrade"}},
		{provider: "opencode-go", want: []string{"opencode", "upgrade"}},
		{provider: "pi", want: []string{"pi", "update", "self"}},
		{provider: "droid", want: nil},
		{provider: "cline", want: []string{"cline", "update"}},
		{provider: "", want: nil},
		{provider: "unknown", want: nil},
	}
	for _, test := range tests {
		if got := UpdateCommand(test.provider); !slices.Equal(got, test.want) {
			t.Fatalf("UpdateCommand(%q) = %v, want %v", test.provider, got, test.want)
		}
	}
}

func TestCheckUpdateAvailable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"version":"2.0.15"}`)
	}))
	defer server.Close()
	stubHTTPClient(t, server.Client())
	stubRegistryURL(t, "claude", server.URL)
	stubRunCommand(t, versionCommand("2.0.14 (Claude Code)"))

	info, err := Check(context.Background(), "claude", "claude")
	if err != nil {
		t.Fatalf("Check 出错: %v", err)
	}
	if info.Provider != "claude" || info.ExecutablePath != "claude" {
		t.Fatalf("Info 的 provider/executable 不对: %+v", info)
	}
	if info.Installed != "2.0.14" || info.Latest != "2.0.15" {
		t.Fatalf("版本不对: installed = %q, latest = %q", info.Installed, info.Latest)
	}
	if !info.UpdateAvailable {
		t.Fatalf("有新版本时 UpdateAvailable 应为 true: %+v", info)
	}
	if info.UpdateCommand != "claude update" {
		t.Fatalf("UpdateCommand = %q, want %q", info.UpdateCommand, "claude update")
	}
}

func TestCheckUpToDate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Run("最新版相同", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"version":"2.0.14"}`)
		}))
		defer server.Close()
		stubHTTPClient(t, server.Client())
		stubRegistryURL(t, "claude", server.URL)
		stubRunCommand(t, versionCommand("2.0.14 (Claude Code)"))

		info, err := Check(context.Background(), "claude", "claude")
		if err != nil {
			t.Fatalf("Check 出错: %v", err)
		}
		if info.UpdateAvailable {
			t.Fatalf("已是最新版本时 UpdateAvailable 应为 false: %+v", info)
		}
		if info.Latest != "2.0.14" {
			t.Fatalf("Latest = %q, want 2.0.14", info.Latest)
		}
	})
	t.Run("最新版更旧", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"version":"2.0.13"}`)
		}))
		defer server.Close()
		stubHTTPClient(t, server.Client())
		stubRegistryURL(t, "claude", server.URL)
		stubRunCommand(t, versionCommand("2.0.14 (Claude Code)"))

		info, err := Check(context.Background(), "claude", "claude")
		if err != nil {
			t.Fatalf("Check 出错: %v", err)
		}
		if info.UpdateAvailable {
			t.Fatalf("来源比已安装更旧时 UpdateAvailable 应为 false: %+v", info)
		}
	})
}

func TestCheckHTTPError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()
	stubHTTPClient(t, server.Client())
	stubRegistryURL(t, "claude", server.URL)
	stubRunCommand(t, versionCommand("2.0.14 (Claude Code)"))

	info, err := Check(context.Background(), "claude", "claude")
	if err == nil {
		t.Fatal("来源返回 500 时 Check 应当报错")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("错误信息 = %v, 期望带上状态码 500", err)
	}
	if info.Installed != "2.0.14" {
		t.Fatalf("出错时也该带回已安装版本: %+v", info)
	}
}

func TestCheckConnectionError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()
	stubHTTPClient(t, server.Client())
	stubRegistryURL(t, "claude", url)
	stubRunCommand(t, versionCommand("2.0.14"))

	if _, err := Check(context.Background(), "claude", "claude"); err == nil {
		t.Fatal("连不上来源时 Check 应当报错")
	}
}

func TestCheckBadPayload(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "not json")
	}))
	defer server.Close()
	stubHTTPClient(t, server.Client())
	stubRegistryURL(t, "claude", server.URL)
	stubRunCommand(t, versionCommand("2.0.14"))

	if _, err := Check(context.Background(), "claude", "claude"); err == nil {
		t.Fatal("来源不是 JSON 时 Check 应当报错")
	}
}

func TestCheckNotInstalled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubRunCommand(t, func(context.Context, string, ...string) (string, error) {
		return "", errors.New(`exec: "claude": executable file not found in $PATH`)
	})

	info, err := Check(context.Background(), "claude", "claude")
	if err == nil {
		t.Fatal("没有安装 CLI 时 Check 应当报错")
	}
	if info.Installed != "" || info.Latest != "" {
		t.Fatalf("未安装时不应有版本信息: %+v", info)
	}
}

func TestCheckUnsupportedProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	called := false
	stubRunCommand(t, func(context.Context, string, ...string) (string, error) {
		called = true
		return "1.0.0", nil
	})

	for _, provider := range []string{"droid", "cline", "devin", "omp", "unknown"} {
		info, err := Check(context.Background(), provider, provider)
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("Check(%q) 的错误 = %v, want ErrUnsupported", provider, err)
		}
		if info.Installed != "" {
			t.Fatalf("Check(%q) 不应带已安装版本: %+v", provider, info)
		}
	}
	if called {
		t.Fatal("不支持的 provider 不应执行任何命令")
	}
}

func TestCheckGrok(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubRunCommand(t, func(_ context.Context, name string, args ...string) (string, error) {
		if len(args) == 1 && args[0] == "--version" {
			return "1.0.46", nil
		}
		if slices.Equal(args, []string{"update", "--check"}) {
			return "available: 1.0.46 -> 1.0.50", nil
		}
		return "", fmt.Errorf("测试里出现了未预期的命令: %s %v", name, args)
	})

	info, err := Check(context.Background(), "grok", "grok")
	if err != nil {
		t.Fatalf("Check 出错: %v", err)
	}
	if info.Latest != "1.0.50" || !info.UpdateAvailable {
		t.Fatalf("grok 结果不对: %+v", info)
	}
	if info.UpdateCommand != "grok update" {
		t.Fatalf("UpdateCommand = %q, want %q", info.UpdateCommand, "grok update")
	}
}

func TestCheckGrokWithoutAvailableVersion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubRunCommand(t, func(_ context.Context, name string, args ...string) (string, error) {
		if len(args) == 1 && args[0] == "--version" {
			return "1.0.46", nil
		}
		if slices.Equal(args, []string{"update", "--check"}) {
			return "grok is up to date", nil
		}
		return "", fmt.Errorf("测试里出现了未预期的命令: %s %v", name, args)
	})

	info, err := Check(context.Background(), "grok", "grok")
	if err != nil {
		t.Fatalf("Check 出错: %v", err)
	}
	if info.Latest != "1.0.46" {
		t.Fatalf("没有可用版本时应回落已安装版本: Latest = %q", info.Latest)
	}
	if info.UpdateAvailable {
		t.Fatalf("没有可用版本时 UpdateAvailable 应为 false: %+v", info)
	}
}

func TestCheckAntigravityAlias(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"version":"1.21.0"}`)
	}))
	defer server.Close()
	stubHTTPClient(t, server.Client())
	stubRegistryURL(t, "antigravity", server.URL)
	stubRunCommand(t, versionCommand("1.20.0"))

	info, err := Check(context.Background(), "agy", "agy")
	if err != nil {
		t.Fatalf("Check 出错: %v", err)
	}
	if info.Provider != "agy" {
		t.Fatalf("Provider = %q, want agy", info.Provider)
	}
	if info.Latest != "1.21.0" || !info.UpdateAvailable {
		t.Fatalf("antigravity 结果不对: %+v", info)
	}
	if info.UpdateCommand != "agy update" {
		t.Fatalf("UpdateCommand = %q, want %q", info.UpdateCommand, "agy update")
	}
}

func TestCheckOpenCodePackages(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Run("2.x 走 @opencode/cli", func(t *testing.T) {
		var path string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path = r.URL.EscapedPath()
			fmt.Fprint(w, `{"version":"2.1.0"}`)
		}))
		defer server.Close()
		stubHTTPClient(t, server.Client())
		stubNPMRegistryBase(t, server.URL)
		stubRunCommand(t, versionCommand("opencode 2.0.0"))

		info, err := Check(context.Background(), "opencode", "opencode")
		if err != nil {
			t.Fatalf("Check 出错: %v", err)
		}
		if info.Latest != "2.1.0" || !info.UpdateAvailable {
			t.Fatalf("2.x 结果不对: %+v", info)
		}
		if path != "/@opencode%2fcli/latest" {
			t.Fatalf("请求路径 = %q, want /@opencode%%2fcli/latest", path)
		}
	})
	t.Run("1.x 走 opencode-ai", func(t *testing.T) {
		var path string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path = r.URL.EscapedPath()
			fmt.Fprint(w, `{"version":"1.3.1"}`)
		}))
		defer server.Close()
		stubHTTPClient(t, server.Client())
		// 1.x 直接用 registryURLs 里的完整地址，桩里要带上 opencode-ai 的路径，
		// 才能验证 1.x 不会切到 2.x 的 @opencode/cli 包。
		stubRegistryURL(t, "opencode", server.URL+"/opencode-ai/latest")
		stubRunCommand(t, versionCommand("opencode 1.3.0"))

		info, err := Check(context.Background(), "opencode", "opencode")
		if err != nil {
			t.Fatalf("Check 出错: %v", err)
		}
		if info.Latest != "1.3.1" || !info.UpdateAvailable {
			t.Fatalf("1.x 结果不对: %+v", info)
		}
		if path != "/opencode-ai/latest" {
			t.Fatalf("请求路径 = %q, want /opencode-ai/latest", path)
		}
	})
}

// 清单平台名按 GOOS/GOARCH 生成；服务端只有 darwin、windows、linux 的 amd64 与 arm64 清单。
func TestAntigravityManifestPlatform(t *testing.T) {
	tests := []struct {
		goos   string
		goarch string
		want   string
	}{
		{goos: "darwin", goarch: "amd64", want: "darwin_amd64"},
		{goos: "darwin", goarch: "arm64", want: "darwin_arm64"},
		{goos: "windows", goarch: "amd64", want: "windows_amd64"},
		{goos: "windows", goarch: "arm64", want: "windows_arm64"},
		{goos: "linux", goarch: "amd64", want: "linux_amd64"},
		{goos: "linux", goarch: "arm64", want: "linux_arm64"},
		{goos: "linux", goarch: "386", want: ""},
		{goos: "freebsd", goarch: "amd64", want: ""},
	}
	for _, test := range tests {
		t.Run(test.goos+"_"+test.goarch, func(t *testing.T) {
			if got := antigravityManifestPlatform(test.goos, test.goarch); got != test.want {
				t.Fatalf("antigravityManifestPlatform(%q, %q) = %q, want %q", test.goos, test.goarch, got, test.want)
			}
		})
	}
}
