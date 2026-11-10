package sandbox

import (
	"context"
	"os/exec"
	"testing"

	"mcode/internal/harness"
)

type fakeSandbox struct{}

func (fakeSandbox) Command(context.Context, string, harness.PermissionMode, string, ...string) (*exec.Cmd, func(), error) {
	return nil, nil, nil
}

func TestIsReadOnly(t *testing.T) {
	for _, s := range []harness.Sandbox{nil, fakeSandbox{}} {
		if IsReadOnly(s) {
			t.Errorf("IsReadOnly(%T) = true", s)
		}
	}
}
