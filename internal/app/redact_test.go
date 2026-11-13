package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/testenv"
)

func TestRedact(t *testing.T) {
	got := Redact("auth Bearer abcdef1234567890XYZ and sk-ant-api03-abcdefghijklmnop")
	if strings.Contains(got, "abcdef1234567890XYZ") || strings.Contains(got, "api03") {
		t.Fatalf("leaked: %q", got)
	}
	if Redact("普通日志 hello") != "普通日志 hello" {
		t.Fatal("plain text changed")
	}
}

func TestRedactWriter(t *testing.T) {
	var buf bytes.Buffer
	in := "token=supersecretvalue\n"
	n, err := redactWriter{&buf}.Write([]byte(in))
	if err != nil || n != len(in) {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if strings.Contains(buf.String(), "supersecretvalue") {
		t.Fatalf("leaked: %q", buf.String())
	}
}

func TestExportDiagnostics(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	dir := t.TempDir()
	log1 := filepath.Join(dir, "a.log")
	content := "open " + home + "/proj from 192.168.1.20\npassword=hunter2hunter2\n"
	if err := os.WriteFile(log1, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out.txt")
	if err := ExportDiagnostics(dst, []string{log1, filepath.Join(dir, "missing.log")}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(dst)
	s := string(data)
	for _, bad := range []string{home, "192.168.1.20", "hunter2hunter2"} {
		if strings.Contains(s, bad) {
			t.Errorf("output leaked %q: %s", bad, s)
		}
	}
	if !strings.Contains(s, "~/proj") || !strings.Contains(s, "x.x.x.x") {
		t.Errorf("unexpected output: %s", s)
	}
}
