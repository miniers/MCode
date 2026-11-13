//go:build windows

package tools

import (
	"strings"
	"testing"

	"mcode/internal/providers/loginenv"
)

func TestBashToolCommandShellDescription(t *testing.T) {
	t.Cleanup(func() { loginenv.SetCommandShell("") })
	for _, pref := range append([]string{""}, loginenv.AvailableCommandShells()...) {
		loginenv.SetCommandShell(pref)
		_, _, kind := loginenv.CommandShell("")
		for _, tool := range CodingToolsFor("gpt-6") {
			if tool.Name != "bash" {
				continue
			}
			if !strings.Contains(tool.Description, "Commands run in "+kind) {
				t.Errorf("偏好 %q 的工具描述未反映当前 shell：%q", pref, tool.Description)
			}
			if got := strings.Contains(tool.Description, "use PowerShell syntax"); got != (kind != "Git Bash") {
				t.Errorf("语法提示与 shell %q 不一致：%q", kind, tool.Description)
			}
		}
	}
}
