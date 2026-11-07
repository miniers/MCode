//go:build darwin

package keyring

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 替身 CLI 不访问真实钥匙串。
func fakeSecurity(t *testing.T, script string) Backend {
	t.Helper()
	path := filepath.Join(t.TempDir(), "security")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := SecurityBin
	SecurityBin = path
	t.Cleanup(func() { SecurityBin = old })
	return NewDefaultBackend()
}

func TestSecurityGetExistingValues(t *testing.T) {
	for _, ref := range []struct{ service, account string }{
		{"mcode", "cloudflare_api_token"},
		{"mcode", "openai.apiKey"},
		{"mcode.mcp-oauth", "mcp-oauth:github:token"},
		{"mcode.mcp-oauth", "mcp-oauth:github:client"},
	} {
		t.Run(ref.account, func(t *testing.T) {
			for _, value := range []string{"old-token", "token\n", "token\r\n", EncodeValue("new-token\n")} {
				backend := fakeSecurity(t, "printf '%s\\n' '"+value+"'")
				got, err := backend.Get(ref.service, ref.account)
				want, _ := DecodeValue(value)
				if err != nil || got != want {
					t.Fatalf("Get existing value: got %q, %v; want %q", got, err, want)
				}
			}
		})
	}
}

func TestSecuritySetUsesStdin(t *testing.T) {
	backend := fakeSecurity(t, `
test "$#" -eq 1 && test "$1" = "-i" || exit 2
IFS= read -r line
test "$line" = 'add-generic-password -U -s "mcode" -a "key" -w "mcode-b64:dG9rZW4="' || exit 3
`)
	if err := backend.Set("mcode", "key", "token"); err != nil {
		t.Fatal(err)
	}
}

func TestSecurityWriteErrorDoesNotExposeSecret(t *testing.T) {
	backend := fakeSecurity(t, "IFS= read -r line; printf '%s' \"$line\" >&2; exit 1")
	value := "private-token"
	err := backend.Set("mcode", "key", value)
	if err == nil {
		t.Fatal("expected command failure")
	}
	if strings.Contains(err.Error(), value) || strings.Contains(err.Error(), EncodeValue(value)) {
		t.Fatal("write error exposed credential")
	}
}

func TestSecurityNotFound(t *testing.T) {
	backend := fakeSecurity(t, "exit 44")
	if _, err := backend.Get("mcode", "key"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing item: %v", err)
	}
	if err := backend.Delete("mcode", "key"); err != nil {
		t.Fatalf("Delete missing item: %v", err)
	}
}

func TestSecurityRejectsInvalidNames(t *testing.T) {
	backend := fakeSecurity(t, "exit 99")
	for _, name := range []string{"", "line\nbreak", "line\rbreak", "nul\x00byte"} {
		if _, err := backend.Get(name, "key"); err == nil {
			t.Fatal("Get accepted invalid name")
		}
		if err := backend.Set("mcode", name, "value"); err == nil {
			t.Fatal("Set accepted invalid name")
		}
		if err := backend.Delete(name, "key"); err == nil {
			t.Fatal("Delete accepted invalid name")
		}
	}
}
