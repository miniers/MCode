package procattr

import (
	"context"
	"os/exec"
	"reflect"
	"sync"
	"testing"
)

// TestShellArgs 验证未注册解析器时的平台 shell。
func TestShellArgs(t *testing.T) {
	oldResolver, oldLook := windowsShell.Load(), shellLookPath
	t.Cleanup(func() { windowsShell.Store(oldResolver); shellLookPath = oldLook })
	SetWindowsShellResolver(nil)
	shellLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	cases := []struct {
		goos     string
		wantName string
		wantArgs []string
	}{
		{"windows", "powershell.exe", []string{"-NoProfile", "-Command", "go test ./..."}},
		{"darwin", "/bin/sh", []string{"-c", "go test ./..."}},
		{"linux", "/bin/sh", []string{"-c", "go test ./..."}},
	}
	for _, tc := range cases {
		name, args := shellArgs(tc.goos, "go test ./...")
		if name != tc.wantName || !reflect.DeepEqual(args, tc.wantArgs) {
			t.Fatalf("%s: shellArgs = %q %q，期望 %q %q", tc.goos, name, args, tc.wantName, tc.wantArgs)
		}
	}
}

// TestShellCommandUsesPlatformShell 验证 ShellCommand 按 shellGOOS 组装命令行。
func TestShellCommandUsesPlatformShell(t *testing.T) {
	old := shellGOOS
	t.Cleanup(func() { shellGOOS = old })
	shellGOOS = "linux"

	cmd := ShellCommand(context.Background(), "echo hi")
	if want := []string{"/bin/sh", "-c", "echo hi"}; !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("ShellCommand Args = %q，期望 %q", cmd.Args, want)
	}
}

func TestWindowsShellFallbacks(t *testing.T) {
	oldResolver, oldLook := windowsShell.Load(), shellLookPath
	t.Cleanup(func() { windowsShell.Store(oldResolver); shellLookPath = oldLook })
	SetWindowsShellResolver(nil)
	for _, tc := range []struct{ pwsh, want string }{{"pwsh", "pwsh"}, {"", "powershell.exe"}} {
		shellLookPath = func(string) (string, error) {
			if tc.pwsh == "" {
				return "", exec.ErrNotFound
			}
			return tc.pwsh, nil
		}
		name, args := shellArgs("windows", "a && b")
		wantArgs := []string{"-NoProfile", "-Command", "a && b"}
		if name != tc.want || !reflect.DeepEqual(args, wantArgs) {
			t.Fatalf("got %q %q", name, args)
		}
	}
}

func TestWindowsShellResolver(t *testing.T) {
	oldResolver, oldGOOS := windowsShell.Load(), shellGOOS
	t.Cleanup(func() { windowsShell.Store(oldResolver); shellGOOS = oldGOOS })
	shellGOOS = "windows"
	SetWindowsShellResolver(func(command string) (string, []string) {
		return "custom-shell", []string{"custom-flag", command}
	})
	const command = `echo "a & b"`
	name, args := ShellArgs(command)
	want := []string{"custom-shell", "custom-flag", command}
	if name != want[0] || !reflect.DeepEqual(args, want[1:]) {
		t.Fatalf("解析器未生效：%q %q", name, args)
	}
	if cmd := ShellCommand(context.Background(), command); !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("ShellCommand 未使用解析器：%q", cmd.Args)
	}
	if name, _ := shellArgs("linux", command); name != "/bin/sh" {
		t.Fatalf("Windows 解析器改变了其他平台：%q", name)
	}
}

func TestWindowsShellResolverConcurrent(t *testing.T) {
	old := windowsShell.Load()
	t.Cleanup(func() { windowsShell.Store(old) })
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				SetWindowsShellResolver(func(command string) (string, []string) {
					return "custom-shell", []string{"-c", command}
				})
				name, args := shellArgs("windows", "echo hi")
				if name != "custom-shell" || !reflect.DeepEqual(args, []string{"-c", "echo hi"}) {
					t.Errorf("并发解析返回异常：%q %q", name, args)
				}
			}
		})
	}
	wg.Wait()
}
