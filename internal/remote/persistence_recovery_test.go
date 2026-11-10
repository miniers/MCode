package remote

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeviceStoreCorruptFileIsBackedUp(t *testing.T) {
	for _, content := range []string{"invalid json", ""} {
		t.Run(content, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "remote-devices.json")
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			store, err := NewDeviceStore(path, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(store.AllRecords()) != 0 {
				t.Fatal("expected an empty device store")
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("expected original path to be absent, got %v", err)
			}
			backups, err := filepath.Glob(path + ".corrupt-*")
			if err != nil || len(backups) != 1 {
				t.Fatalf("expected one backup, got %v: %v", backups, err)
			}
			data, err := os.ReadFile(backups[0])
			if err != nil || string(data) != content {
				t.Fatalf("backup content = %q, error = %v", data, err)
			}
		})
	}
}

func TestDeviceStoreReadError(t *testing.T) {
	store, err := NewDeviceStore(t.TempDir(), nil)
	if err == nil || store != nil {
		t.Fatalf("expected read error and nil store, got %v, %v", store, err)
	}
}

func TestDeviceStoreWriteFailurePreservesFlushState(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	lastWrite := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := &DeviceStore{
		filePath:        filepath.Join(parent, "devices.json"),
		lastWrite:       lastWrite,
		hasPendingFlush: true,
	}
	if err := store.writeLocked(lastWrite.Add(time.Minute)); err == nil {
		t.Fatal("expected write error")
	}
	if store.lastWrite != lastWrite || !store.hasPendingFlush {
		t.Fatalf("write failure changed flush state: lastWrite=%v pending=%v", store.lastWrite, store.hasPendingFlush)
	}
}

func TestStoredKeysPreserveCorruptFilesAndGenerateMissing(t *testing.T) {
	loaders := []struct {
		name string
		load func(string) (*ecdsa.PrivateKey, error)
	}{
		{name: "VAPID", load: func(dir string) (*ecdsa.PrivateKey, error) {
			return loadOrCreateVAPIDKey(filepath.Join(dir, "key"))
		}},
		{name: "ACME", load: func(dir string) (*ecdsa.PrivateKey, error) {
			return loadOrGenerateAccountKey(dir)
		}},
	}
	for _, loader := range loaders {
		t.Run(loader.name, func(t *testing.T) {
			fileName := "key"
			if loader.name == "ACME" {
				fileName = "account.key"
			}
			for _, tc := range []struct {
				name    string
				content string
			}{
				{name: "empty", content: ""},
				{name: "invalid PEM", content: "invalid PEM"},
				{name: "invalid private key", content: string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte{1, 2, 3}}))},
			} {
				t.Run("corrupt/"+tc.name, func(t *testing.T) {
					dir := t.TempDir()
					path := filepath.Join(dir, fileName)
					original := []byte(tc.content)
					if err := os.WriteFile(path, original, 0600); err != nil {
						t.Fatal(err)
					}
					if _, err := loader.load(dir); err == nil {
						t.Fatal("expected corrupt key error")
					} else if loader.name == "ACME" && !strings.Contains(err.Error(), "account.key") {
						t.Fatalf("error does not identify account.key: %v", err)
					}
					data, err := os.ReadFile(path)
					if err != nil || !bytes.Equal(data, original) {
						t.Fatalf("key file changed: %q, error: %v", data, err)
					}
				})
			}
			t.Run("missing", func(t *testing.T) {
				dir := t.TempDir()
				key, err := loader.load(dir)
				if err != nil || key == nil {
					t.Fatalf("generate key: %v", err)
				}
				if data, err := os.ReadFile(filepath.Join(dir, fileName)); err != nil || len(data) == 0 {
					t.Fatalf("key was not persisted: %v", err)
				}
				reloaded, err := loader.load(dir)
				if err != nil || reloaded == nil || key.D.Cmp(reloaded.D) != 0 {
					t.Fatalf("reload generated key: %v", err)
				}
			})
			t.Run("read error", func(t *testing.T) {
				dir := t.TempDir()
				if err := os.Mkdir(filepath.Join(dir, fileName), 0700); err != nil {
					t.Fatal(err)
				}
				if _, err := loader.load(dir); err == nil {
					t.Fatal("expected read error")
				}
			})
		})
	}
}

func TestCloudflareDefaultClientAndResponseLimit(t *testing.T) {
	provider := newCloudflareDNS("", "token", nil, 0)
	if provider.client.Timeout != 30*time.Second {
		t.Fatalf("default timeout = %v", provider.client.Timeout)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, strings.Repeat(" ", 1<<20), `{"success":true,"result":[{"id":"zone"}]}`)
	}))
	defer server.Close()
	provider = newCloudflareDNS(server.URL, "token", server.Client(), 0)
	if _, err := provider.findZoneID(t.Context(), "example.com"); err == nil {
		t.Fatal("expected error for response exceeding size limit")
	}
}
