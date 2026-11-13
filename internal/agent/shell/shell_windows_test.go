//go:build windows

package shell

import (
	"context"
	"strings"
	"testing"
	"time"

	"mcode/internal/providers/loginenv"
)

func TestCommandShellRunPreference(t *testing.T) {
	t.Cleanup(func() { loginenv.SetCommandShell("") })
	for _, pref := range loginenv.AvailableCommandShells() {
		t.Run(pref, func(t *testing.T) {
			loginenv.SetCommandShell(pref)
			_, _, kind := loginenv.CommandShell("")
			command := `$value = 'command-shell-marker'; Write-Output $value`
			if kind == "Git Bash" {
				command = `value='command-shell-marker'; printf '%s\n' "$value"`
			}
			result, err := Run(context.Background(), Options{
				Command: command,
				WorkDir: t.TempDir(),
				Timeout: 5 * time.Second,
			}, nil)
			if err != nil || result.ExitCode != 0 || strings.TrimSpace(result.Output) != "command-shell-marker" {
				t.Fatalf("偏好 %q 未按对应语法执行：%+v，错误 %v", pref, result, err)
			}
		})
	}
}
