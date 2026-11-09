package gate

import (
	"encoding/json"
	"os"
	"testing"

	"mcode/internal/harness"
)

func TestHomeControlAndCredentials(t *testing.T) {
	if home, _ := os.UserHomeDir(); home == "" { t.Skip("没有主目录") }
	for _, tc := range []struct{ command, want string }{
		{"rm -rf ~/.mcode", "controlPlane"},
		{"rm -rf $HOME/.mcode", "controlPlane"},
		{"rm -rf ${HOME}/.mcode", "controlPlane"},
		{"rm -rf \"$HOME\"/.mcode", "controlPlane"},
		{"rm -rf \"${HOME}/.mcode\"", "controlPlane"},
		{"cat ~/.pi/agent/file", "controlPlane"},
		{"rm -rf $HOME/.pi/agent", "controlPlane"},
		{"rm -rf ${HOME}/.pi/agent", "controlPlane"},
		{"cat \"$HOME/.ssh/id_rsa\"", "credential"},
		{"rm -rf ~/.mcode-foo", ""},
		{"rm -rf ~/.pi/agent-other", ""},
	} {
		t.Run(tc.command, func(t *testing.T) {
			args, _ := json.Marshal(map[string]string{"command": tc.command})
			got := Check(Input{ToolName: "bash", Args: args, WorkDir: t.TempDir(), Mode: harness.ModeFullAccess})
			if got.Request.Sensitive != tc.want { t.Fatalf("敏感类型 = %q，预期 %q", got.Request.Sensitive, tc.want) }
		})
	}
}
