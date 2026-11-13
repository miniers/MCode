//go:build darwin

package keyring

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 替身 CLI 不访问真实钥匙串。
func fakeSecurity(t *testing.T, script string) Backend {
	t.Helper()
	resetCache()
	t.Cleanup(resetCache)
	path := filepath.Join(t.TempDir(), "security")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := SecurityBin
	SecurityBin = path
	t.Cleanup(func() { SecurityBin = old })
	return NewDefaultBackend()
}

func TestSecurityGetCache(t *testing.T) {
	for _, tc := range []struct {
		name, result string
		wantErr      error
		calls        int
	}{
		{"value", "printf 'token\\n'", nil, 1},
		{"missing", "exit 44", ErrNotFound, 1},
		{"failure", "exit 1", errors.New("failure"), 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count := filepath.Join(t.TempDir(), "count")
			backend := fakeSecurity(t, fmt.Sprintf("printf 'x\\n' >> %s\n%s", QuoteArg(count), tc.result))
			for i := 0; i < 2; i++ {
				value, err := backend.Get("mcode", "key")
				if tc.wantErr == nil && (err != nil || value != "token") {
					t.Fatalf("Get: %q, %v", value, err)
				}
				if tc.wantErr != nil && (err == nil || (tc.wantErr == ErrNotFound && !errors.Is(err, ErrNotFound))) {
					t.Fatalf("Get error: %v", err)
				}
			}
			data, err := os.ReadFile(count)
			if err != nil || strings.Count(string(data), "x\n") != tc.calls {
				t.Fatalf("security calls: %q, %v; want %d", data, err, tc.calls)
			}
		})
	}
}

func TestSecurityGetConcurrent(t *testing.T) {
	backend := fakeSecurity(t, "sleep 0.3; printf 'token\\n'")
	start := time.Now()
	results := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func(account string) {
			value, err := backend.Get("mcode", account)
			if err == nil && value != "token" {
				err = fmt.Errorf("unexpected value %q", value)
			}
			results <- err
		}(fmt.Sprint(i))
	}
	for i := 0; i < 4; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed >= 900*time.Millisecond {
		t.Fatalf("concurrent Gets took %v", elapsed)
	}
}

func TestSecurityGetDoesNotCacheBeforeSet(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	release := filepath.Join(dir, "release")
	backend := fakeSecurity(t, fmt.Sprintf(`
if [ "$1" = "-i" ]; then exit 0; fi
if [ ! -f %[1]s ]; then
  touch %[1]s
  while [ ! -f %[2]s ]; do sleep 0.01; done
  printf 'old\n'
else
  printf 'new\n'
fi`, QuoteArg(started), QuoteArg(release)))
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	type result struct {
		value string
		err   error
	}
	done := make(chan result, 1)
	go func() {
		value, err := backend.Get("mcode", "key")
		done <- result{value, err}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Get did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := backend.Set("mcode", "key", "new"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := <-done; got.err != nil || got.value != "old" {
		t.Fatalf("first Get: %q, %v", got.value, got.err)
	}
	if value, err := backend.Get("mcode", "key"); err != nil || value != "new" {
		t.Fatalf("second Get: %q, %v", value, err)
	}
}

func TestSecurityCacheInvalidation(t *testing.T) {
	count := filepath.Join(t.TempDir(), "count")
	backend := fakeSecurity(t, fmt.Sprintf("if [ \"$1\" = find-generic-password ]; then printf 'x\\n' >> %s; printf 'token\\n'; fi", QuoteArg(count)))
	get := func() {
		t.Helper()
		if value, err := backend.Get("mcode", "key"); err != nil || value != "token" {
			t.Fatalf("Get: %q, %v", value, err)
		}
	}
	get()
	if err := backend.Set("mcode", "key", "new"); err != nil {
		t.Fatal(err)
	}
	get()
	if err := backend.Delete("mcode", "key"); err != nil {
		t.Fatal(err)
	}
	get()
	oldTTL := cacheTTL
	cacheTTL = time.Nanosecond
	t.Cleanup(func() { cacheTTL = oldTTL })
	time.Sleep(time.Millisecond)
	get()
	data, err := os.ReadFile(count)
	if err != nil || strings.Count(string(data), "x\n") != 4 {
		t.Fatalf("security calls: %q, %v; want 4", data, err)
	}
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
