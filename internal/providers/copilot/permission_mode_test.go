package copilot

import (
	"encoding/json"
	"testing"

	"mcode/internal/harness"
)

func TestLegacyAutoPermissionMode(t *testing.T) {
	if got := permissionModeName(harness.ModeAutoAcceptEdits); got != permissionModeName(harness.ModeAuto) {
		t.Fatalf("旧权限模式 = %q，与 auto 不同", got)
	}
	request := json.RawMessage(`{"kind":"shell","assistedApproval":{"recommendation":"approve"}}`)
	for _, mode := range []harness.PermissionMode{harness.ModeAuto, harness.ModeAutoAcceptEdits} {
		h := eventHandler{turn: harness.Turn{Mode: mode}}
		if h.automaticDecision(request, nil) == nil {
			t.Errorf("%s 未采用分类器建议", mode)
		}
	}
}
