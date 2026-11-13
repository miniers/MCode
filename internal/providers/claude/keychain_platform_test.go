package claude

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSecuritySkippedOutsideDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("darwin uses keychain")
	}
	old := securityExecutable
	defer func() { securityExecutable = old }()
	securityExecutable = filepath.Join(t.TempDir(), "security")
	if err := os.WriteFile(securityExecutable, []byte("invalid"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, ok := readLoginFromKeychain(""); ok {
		t.Fatal("unexpected keychain login")
	}
}
