//go:build windows

package loginenv

import (
	"os/exec"
	"reflect"
	"sync"
	"testing"

	"mcode/internal/procattr"
)

func TestCommandShellSelection(t *testing.T) {
	const command = `Write-Output "a & b"`
	for _, tc := range []struct {
		name, pref, bash, pwsh, powershell, want, kind string
	}{
		{"自动优先 Git Bash", "", "bash-path", "pwsh-path", "ps-path", "bash-path", "Git Bash"},
		{"自动回退 pwsh", "", "", "pwsh-path", "ps-path", "pwsh-path", "PowerShell 7"},
		{"自动回退系统 PowerShell", "", "", "", "ps-path", "ps-path", "Windows PowerShell"},
		{"未找到任何 shell", "", "", "", "", "powershell.exe", "Windows PowerShell"},
		{"指定 Git Bash", "git-bash", "bash-path", "pwsh-path", "ps-path", "bash-path", "Git Bash"},
		{"指定 pwsh", "pwsh", "bash-path", "pwsh-path", "ps-path", "pwsh-path", "PowerShell 7"},
		{"指定系统 PowerShell", "powershell", "bash-path", "pwsh-path", "ps-path", "ps-path", "Windows PowerShell"},
		{"Git Bash 不可用", "git-bash", "", "pwsh-path", "ps-path", "pwsh-path", "PowerShell 7"},
		{"pwsh 不可用", "pwsh", "bash-path", "", "ps-path", "bash-path", "Git Bash"},
		{"系统 PowerShell 不可用", "powershell", "bash-path", "pwsh-path", "", "bash-path", "Git Bash"},
		{"全部偏好不可用", "pwsh", "", "", "", "powershell.exe", "Windows PowerShell"},
		{"未知偏好", "unknown", "bash-path", "pwsh-path", "ps-path", "bash-path", "Git Bash"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(name string) (string, error) {
				path := map[string]string{"pwsh.exe": tc.pwsh, "powershell.exe": tc.powershell}[name]
				if path == "" {
					return "", exec.ErrNotFound
				}
				return path, nil
			}
			name, args, kind := selectCommandShell(tc.pref, command, func() string { return tc.bash }, lookup)
			wantArgs := []string{"-NoProfile", "-NonInteractive", "-Command", command}
			if tc.kind == "Git Bash" {
				wantArgs = []string{"-c", command}
			}
			if name != tc.want || kind != tc.kind || !reflect.DeepEqual(args, wantArgs) {
				t.Fatalf("得到 %q %q %q，期望 %q %q %q", name, args, kind, tc.want, wantArgs, tc.kind)
			}
		})
	}
}

func TestAvailableCommandShells(t *testing.T) {
	for mask := range 8 {
		var want []string
		for i, pref := range []string{"git-bash", "pwsh", "powershell"} {
			if mask&(1<<i) != 0 {
				want = append(want, pref)
			}
		}
		got := availableCommandShells(func() string {
			if mask&1 != 0 {
				return "bash-path"
			}
			return ""
		}, func(name string) (string, error) {
			if (name == "pwsh.exe" && mask&2 != 0) || (name == "powershell.exe" && mask&4 != 0) {
				return name, nil
			}
			return "", exec.ErrNotFound
		})
		if !reflect.DeepEqual(got, want) {
			t.Errorf("可用集合 %d：得到 %q，期望 %q", mask, got, want)
		}
	}
}

func TestAvailableCommandShellsCached(t *testing.T) {
	first := AvailableCommandShells()
	second := AvailableCommandShells()
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("两次调用结果不一致：%q 与 %q", first, second)
	}
}

func TestCommandShellPreferenceAndProcattr(t *testing.T) {
	old, _ := commandShellPreference.Load().(string)
	t.Cleanup(func() { SetCommandShell(old) })
	for _, pref := range append([]string{"", "unknown", " pwsh "}, AvailableCommandShells()...) {
		SetCommandShell(pref)
		name, args, _ := CommandShell("echo hi")
		platformName, platformArgs := procattr.ShellArgs("echo hi")
		if name != platformName || !reflect.DeepEqual(args, platformArgs) {
			t.Fatalf("偏好 %q：两个入口选择不同，%q %q 与 %q %q", pref, name, args, platformName, platformArgs)
		}
	}
	SetCommandShell(" pwsh ")
	if got := commandShellPreference.Load(); got != "pwsh" {
		t.Fatalf("偏好没有去除空白：%q", got)
	}
}

func TestCommandShellPreferenceConcurrent(t *testing.T) {
	old, _ := commandShellPreference.Load().(string)
	t.Cleanup(func() { SetCommandShell(old) })
	var wg sync.WaitGroup
	for _, pref := range []string{"", "git-bash", "pwsh", "powershell"} {
		wg.Go(func() {
			for range 20 {
				SetCommandShell(pref)
				name, args, kind := CommandShell("echo hi")
				if name == "" || len(args) == 0 || kind == "" || args[len(args)-1] != "echo hi" {
					t.Errorf("并发切换返回异常：%q %q %q", name, args, kind)
				}
			}
		})
	}
	wg.Wait()
}
