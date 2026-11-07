package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportDiagnosticsToRedactsSecrets(t *testing.T) {
	home := isolateLogDir(t)

	f, path, err := openLogFile("")
	if err != nil {
		t.Fatalf("openLogFile: %v", err)
	}
	line := "password=hunter2hunter2 token=supersecretvalue dir=" + home + "/proj ip=10.1.2.3\n"
	if _, err := f.Write([]byte(line)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "diag.log")
	client := &Client{}
	if err := client.ExportDiagnosticsTo(dst); err != nil {
		t.Fatalf("ExportDiagnosticsTo: %v", err)
	}

	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read diagnostics: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "password=") {
		t.Fatalf("diagnostics missing log content: %s", got)
	}
	for _, bad := range []string{"hunter2hunter2", "supersecretvalue", home, "10.1.2.3"} {
		if strings.Contains(got, bad) {
			t.Errorf("diagnostics leaked %q: %s", bad, got)
		}
	}
	if !strings.Contains(got, "~/proj") || !strings.Contains(got, "x.x.x.x") {
		t.Errorf("diagnostics not sanitized as expected: %s", got)
	}
	if !strings.Contains(got, filepath.Join("mcode", "logs", logFileName)) {
		t.Errorf("diagnostics missing file header: %s", got)
	}
	if strings.Contains(got, path) {
		t.Errorf("diagnostics header leaked raw path %q: %s", path, got)
	}
}

func TestExportDiagnosticsToRejectsEmptyDestination(t *testing.T) {
	isolateLogDir(t)
	client := &Client{}
	if err := client.ExportDiagnosticsTo(""); err == nil {
		t.Fatal("expected error for empty destination")
	}
}
