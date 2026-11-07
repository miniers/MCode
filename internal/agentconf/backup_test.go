package agentconf

import (
	"os"
	"path/filepath"
	"testing"

	"mcode/internal/config"
)

func TestBackupMCPSecretRefs(t *testing.T) {
	dataDir := t.TempDir()
	project := t.TempDir()
	data := []byte(`{"mcpServers":{"example":{"url":"https://example.com","secretEnvKeys":["TOKEN"],"secretHeaderKeys":["Authorization"]}}}`)
	if err := os.WriteFile(filepath.Join(dataDir, "mcp.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(project, ".mcode"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".mcode", "mcp.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	refs, err := BackupMCPSecretRefs(dataDir, []string{project, filepath.Join(dataDir, "missing")})
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[config.SecretRef]bool)
	for _, ref := range refs {
		if got[ref] {
			t.Fatalf("duplicate reference: %+v", ref)
		}
		got[ref] = true
	}
	id := (MCPServer{Name: "example", URL: "https://example.com"}).AuthID()
	want := []config.SecretRef{
		{Service: "mcode.mcp", Account: "mcp.example.TOKEN"},
		{Service: "mcode.mcp", Account: "mcp.example.header.Authorization"},
		{Service: "mcode.mcp-oauth", Account: "mcp-oauth:" + id + ":token"},
		{Service: "mcode.mcp-oauth", Account: "mcp-oauth:" + id + ":client"},
		{Service: "mcode.mcp-oauth", Account: "mcp-oauth:" + id + ":cimd"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d refs, want %d", len(got), len(want))
	}
	for _, ref := range want {
		if !got[ref] {
			t.Errorf("missing reference: %+v", ref)
		}
	}
	if err := os.WriteFile(filepath.Join(project, ".mcode", "mcp.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := BackupMCPSecretRefs(dataDir, []string{project}); err == nil {
		t.Fatal("malformed project configuration was accepted")
	}
}
