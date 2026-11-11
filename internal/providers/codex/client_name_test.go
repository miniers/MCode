package codex

import (
	"strings"
	"testing"
)

func TestClientNameIsCodexForGateway(t *testing.T) {
	if clientName != "codex-mcode" {
		t.Fatalf("clientName = %q, want %q", clientName, "codex-mcode")
	}
	if !strings.HasPrefix(clientName, "codex") {
		t.Fatalf("clientName = %q, want prefix %q", clientName, "codex")
	}
	if clientName == "mcode" {
		t.Fatalf("clientName must not be %q", "mcode")
	}
}
