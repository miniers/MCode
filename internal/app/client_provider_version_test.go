package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"mcode/internal/config"
	"mcode/internal/testenv"
)

// writeFakeProvider 在 dir 下写一个假的 provider CLI：只接受 --version 并输出固定版本，
// 让用例不执行真实 CLI、也不访问网络。
func writeFakeProvider(t *testing.T, dir, name, version string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".version", []byte(version+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runFakeProvider() bool {
	base := filepath.Base(os.Args[0])
	base = strings.TrimSuffix(base, filepath.Ext(base))
	switch base {
	case "claude", "codex", "copilot", "droid", "pi", "cmd":
	default:
		return false
	}
	if len(os.Args) != 2 || os.Args[1] != "--version" {
		os.Exit(1)
	}
	data, err := os.ReadFile(os.Args[0] + ".version")
	if err != nil {
		os.Exit(1)
	}
	_, _ = os.Stdout.Write(data)
	return true
}

// stubProviderBinaryFinder 把登录 shell 的 PATH 查找替换成进程 PATH + 显式回退目录，
// 让用例不依赖运行测试的机器的登录环境。
func stubProviderBinaryFinder(t *testing.T) {
	t.Helper()
	original := providerBinaryFinder
	providerBinaryFinder = func(names []string, fallbackDirs []string) (string, bool) {
		for _, name := range names {
			if name == "" {
				continue
			}
			if path, err := exec.LookPath(name); err == nil {
				return path, true
			}
		}
		for _, dir := range fallbackDirs {
			for _, name := range names {
				candidate := filepath.Join(dir, name)
				if runtime.GOOS == "windows" && filepath.Ext(candidate) == "" {
					candidate += ".exe"
				}
				info, err := os.Stat(candidate)
				if err == nil && !info.IsDir() && fakeProviderExecutable(candidate, info) {
					return candidate, true
				}
			}
		}
		return "", false
	}
	t.Cleanup(func() { providerBinaryFinder = original })
}

func fakeProviderExecutable(path string, info os.FileInfo) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Ext(path), ".exe")
	}
	return info.Mode().Perm()&0o111 != 0
}

func TestProviderVersionFromPath(t *testing.T) {
	testenv.TempHome(t)
	stubProviderBinaryFinder(t)
	binDir := t.TempDir()
	want := writeFakeProvider(t, binDir, "claude", "1.2.3")
	t.Setenv("PATH", binDir)

	info, err := (&Client{}).ProviderVersion(context.Background(), "claude")
	if err != nil {
		t.Fatalf("ProviderVersion 出错: %v", err)
	}
	if info.Provider != "claude" || info.Installed != "1.2.3" || info.ExecutablePath != want {
		t.Fatalf("ProviderVersion = %+v, want provider=claude installed=1.2.3 executable=%s", info, want)
	}
	if info.Latest != "" || info.UpdateAvailable {
		t.Fatalf("只看已安装版本时不应带更新信息: %+v", info)
	}
}

func TestProviderVersionPrefersConfiguredBinary(t *testing.T) {
	testenv.TempHome(t)
	t.Setenv("MCODE_CLAUDE_BIN", "")
	pathDir := t.TempDir()
	writeFakeProvider(t, pathDir, "claude", "9.9.9")
	t.Setenv("PATH", pathDir)
	binDir := t.TempDir()
	configured := writeFakeProvider(t, binDir, "claude", "1.2.3")

	dataDir := t.TempDir()
	if err := config.Update(dataDir, func(latest *config.Config) error {
		latest.Providers.Claude.Bin = configured
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	info, err := (&Client{dataDir: dataDir}).ProviderVersion(context.Background(), "claude")
	if err != nil {
		t.Fatalf("ProviderVersion 出错: %v", err)
	}
	if info.Installed != "1.2.3" || info.ExecutablePath != configured {
		t.Fatalf("配置里的用户路径未优先生效: %+v", info)
	}
}

func TestProviderVersionUsesACPDroidBinary(t *testing.T) {
	testenv.TempHome(t)
	t.Setenv("PATH", t.TempDir())
	binDir := t.TempDir()
	configured := writeFakeProvider(t, binDir, "droid", "1.2.3")

	dataDir := t.TempDir()
	if err := config.Update(dataDir, func(latest *config.Config) error {
		latest.Providers.ACP = map[string]config.ProviderConfig{"droid": {Bin: configured}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	info, err := (&Client{dataDir: dataDir}).ProviderVersion(context.Background(), "droid")
	if err != nil {
		t.Fatalf("ProviderVersion 出错: %v", err)
	}
	if info.Installed != "1.2.3" || info.ExecutablePath != configured {
		t.Fatalf("providers.acp 里的 droid 路径未生效: %+v", info)
	}
}

func TestProviderVersionErrors(t *testing.T) {
	testenv.TempHome(t)
	t.Setenv("PATH", t.TempDir())
	stubProviderBinaryFinder(t)
	t.Run("PATH 里没有可执行文件", func(t *testing.T) {
		if _, err := (&Client{}).ProviderVersion(context.Background(), "claude"); err == nil {
			t.Fatal("找不到可执行文件时应当报错")
		}
	})
	t.Run("没有已知的 provider", func(t *testing.T) {
		if _, err := (&Client{}).ProviderVersion(context.Background(), "unknown"); err == nil {
			t.Fatal("未知 provider 应当报错")
		}
	})
}

// 登录 shell 的 PATH 里找不到时回退到常见安装目录。
func TestProviderVersionUsesFallbackDirs(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("PATH", t.TempDir())
	stubProviderBinaryFinder(t)

	binDir := filepath.Join(home, ".npm-global", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := writeFakeProvider(t, binDir, "pi", "1.3.0")

	info, err := (&Client{}).ProviderVersion(context.Background(), "pi")
	if err != nil {
		t.Fatalf("ProviderVersion(pi) 出错: %v", err)
	}
	if info.Installed != "1.3.0" || info.ExecutablePath != want {
		t.Fatalf("ProviderVersion(pi) = %+v, want installed=1.3.0 executable=%s", info, want)
	}
}

// Command Code 的候选命令名：command-code → commandcode → cmd。
func TestProviderVersionCommandCodeAliases(t *testing.T) {
	testenv.TempHome(t)
	binDir := t.TempDir()
	t.Setenv("PATH", binDir)

	var seen []string
	original := providerBinaryFinder
	providerBinaryFinder = func(names []string, fallbackDirs []string) (string, bool) {
		seen = append([]string(nil), names...)
		path, err := exec.LookPath("cmd")
		if err != nil {
			return "", false
		}
		return path, true
	}
	t.Cleanup(func() { providerBinaryFinder = original })

	want := writeFakeProvider(t, binDir, "cmd", "1.84.0")
	info, err := (&Client{}).ProviderVersion(context.Background(), "commandcode")
	if err != nil {
		t.Fatalf("ProviderVersion(commandcode) 出错: %v", err)
	}
	if info.Installed != "1.84.0" || info.ExecutablePath != want {
		t.Fatalf("ProviderVersion(commandcode) = %+v, want installed=1.84.0 executable=%s", info, want)
	}
	if got := strings.Join(seen, ","); got != "command-code,commandcode,cmd" {
		t.Fatalf("候选命令名 = %q, want command-code,commandcode,cmd", got)
	}
}

func TestCheckProviderUpdateFallsBackForUnsupportedProvider(t *testing.T) {
	testenv.TempHome(t)
	stubProviderBinaryFinder(t)
	binDir := t.TempDir()
	want := writeFakeProvider(t, binDir, "droid", "1.2.3")
	t.Setenv("PATH", binDir)

	// droid 没有最新版本来源（cliupdate.ErrUnsupported），应回落成只报已安装版本，
	// 既不算失败，也不会访问网络。
	info, err := (&Client{}).CheckProviderUpdate(context.Background(), "droid")
	if err != nil {
		t.Fatalf("没有更新来源的 provider 不应报错: %v", err)
	}
	if info.Provider != "droid" || info.Installed != "1.2.3" || info.ExecutablePath != want {
		t.Fatalf("CheckProviderUpdate = %+v, want provider=droid installed=1.2.3 executable=%s", info, want)
	}
	if info.Latest != "" || info.UpdateAvailable {
		t.Fatalf("没有更新来源时不应有最新版本信息: %+v", info)
	}
}

func TestCheckProviderUpdateErrors(t *testing.T) {
	testenv.TempHome(t)
	t.Setenv("PATH", t.TempDir())
	stubProviderBinaryFinder(t)
	if _, err := (&Client{}).CheckProviderUpdate(context.Background(), "claude"); err == nil {
		t.Fatal("找不到可执行文件时应当报错")
	}
	if _, err := (&Client{}).CheckProviderUpdate(context.Background(), "unknown"); err == nil {
		t.Fatal("未知 provider 应当报错")
	}
}
