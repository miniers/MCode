package procattr

import (
	"context"
	"reflect"
	"testing"
)

// TestShellArgs 验证各平台执行用户命令的 shell：Windows 用 PowerShell，其余用 /bin/sh -c。
func TestShellArgs(t *testing.T) {
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
