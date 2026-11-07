package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/config"
	"mcode/internal/native/keyring"
	"mcode/internal/store"
)

type bkFailStore struct {
	config.SecretStore
	getErr, setErr error
}

func (s bkFailStore) Get(a string) (string, error) {
	if s.getErr != nil {
		return "", s.getErr
	}
	return s.SecretStore.Get(a)
}
func (s bkFailStore) Set(a, v string) error {
	if s.setErr != nil {
		return s.setErr
	}
	return s.SecretStore.Set(a, v)
}
func bkSetup(t *testing.T) map[string]config.SecretStore {
	t.Helper()
	frameSetup(t)
	old := SecretStoreFor
	stores := map[string]config.SecretStore{}
	SecretStoreFor = func(service string) config.SecretStore {
		if stores[service] == nil {
			stores[service] = config.NewMemorySecretStore()
		}
		return stores[service]
	}
	t.Cleanup(func() { SecretStoreFor = old; bkApplyHook = nil })
	return stores
}
func bkWrite(t *testing.T, dir, name, data string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
func bkOpen(t *testing.T, dir string) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(dir, "mcode.db"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func bkThread(t *testing.T, s *store.Store, id, dir string) {
	t.Helper()
	ctx := context.Background()
	if err := s.UpsertProject(ctx, store.Project{ID: id, Name: id, Path: dir}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateThread(ctx, store.Thread{ID: id, ProjectID: id, Title: id, Provider: "claude"}); err != nil {
		t.Fatal(err)
	}
}
func bkCreate(t *testing.T, source string, history bool) string {
	t.Helper()
	dest := filepath.Join(t.TempDir(), "backup")
	if err := Create(context.Background(), CreateOptions{DataDir: source, Destination: dest, Password: []byte("password"), IncludeHistory: history}); err != nil {
		t.Fatal(err)
	}
	return dest
}
func bkStage(t *testing.T, dest, source string) {
	t.Helper()
	if _, err := Stage(context.Background(), StageOptions{DataDir: dest, Source: source, Password: []byte("password")}); err != nil {
		t.Fatal(err)
	}
}

func TestBackupRoundTrip(t *testing.T) {
	for _, history := range []bool{false, true} {
		t.Run(fmt.Sprint(history), func(t *testing.T) {
			stores := bkSetup(t)
			ctx := context.Background()
			source, dest := t.TempDir(), t.TempDir()
			s := bkOpen(t, source)
			bkThread(t, s, "source", source)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			d := bkOpen(t, dest)
			bkThread(t, d, "target", dest)
			if err := d.Close(); err != nil {
				t.Fatal(err)
			}
			if err := SecretStoreFor("mcode").Set("openai.apiKey", "secret"); err != nil {
				t.Fatal(err)
			}
			bkWrite(t, source, "config.json", `{"version":9,"providers":{"claude":{"bin":"source"}},"remote":{"enabled":true}}`)
			bkWrite(t, dest, "config.json", `{"version":9,"providers":{"claude":{"bin":"target"}},"remote":{"enabled":false}}`)
			archive := bkCreate(t, source, history)
			stores["mcode"] = config.NewMemorySecretStore()
			bkStage(t, dest, archive)
			if result, err := ApplyPending(dest); err != nil || result.Applied {
				t.Fatalf("unconfirmed: %+v %v", result, err)
			}
			if err := ConfirmPending(dest); err != nil {
				t.Fatal(err)
			}
			if result, err := ApplyPending(dest); err != nil || !result.Applied {
				t.Fatalf("apply: %+v %v", result, err)
			}
			d = bkOpen(t, dest)
			if result, err := CompletePending(ctx, dest, d); err != nil || !result.Applied {
				t.Fatalf("complete: %+v %v", result, err)
			}
			id := "target"
			if history {
				id = "source"
			}
			threads, err := d.ListThreads(ctx, id, false)
			if err != nil || len(threads) != 1 {
				t.Fatalf("threads: %+v %v", threads, err)
			}
			_ = d.Close()
			value, err := SecretStoreFor("mcode").Get("openai.apiKey")
			if err != nil || value != "secret" {
				t.Fatalf("secret: %v", err)
			}
			var cfg map[string]any
			data, err := os.ReadFile(filepath.Join(dest, "config.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(data, &cfg); err != nil {
				t.Fatal(err)
			}
			providers := cfg["providers"].(map[string]any)
			if providers["claude"].(map[string]any)["bin"] != "target" || cfg["remote"].(map[string]any)["enabled"] != false {
				t.Fatalf("local settings lost: %s", data)
			}
			if r, err := TakeResult(dest); err != nil || !r.Applied {
				t.Fatalf("result: %+v %v", r, err)
			}
			if r, err := TakeResult(dest); err != nil || r.Applied {
				t.Fatalf("second result: %+v %v", r, err)
			}
		})
	}
}

func TestBackupExclusionsAndFailures(t *testing.T) {
	stores := bkSetup(t)
	src, dst := t.TempDir(), t.TempDir()
	bkWrite(t, src, "agentcfg/ok", "ok")
	bkWrite(t, src, "agentcfg/logs/hidden", "no")
	bkWrite(t, src, "remote-tls/key", "no")
	bkWrite(t, src, "logs/log", "no")
	archive := bkCreate(t, src, false)
	bkStage(t, dst, archive)
	for _, name := range []string{"remote-tls/key", "logs/log", "agentcfg/logs/hidden"} {
		if _, err := os.Stat(filepath.Join(dst, "restore-pending", "files", filepath.FromSlash(name))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("included %s", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "restore-pending", "files", "agentcfg", "ok")); err != nil {
		t.Fatal(err)
	}
	stores["mcode"] = bkFailStore{SecretStore: config.NewMemorySecretStore(), getErr: errors.New("failed")}
	if err := Create(context.Background(), CreateOptions{DataDir: src, Destination: filepath.Join(t.TempDir(), "bad"), Password: []byte("x")}); err == nil {
		t.Fatal("expected Get failure")
	}
}
func TestBackupSecretSetFailureAndResume(t *testing.T) {
	stores := bkSetup(t)
	src, dst := t.TempDir(), t.TempDir()
	bkWrite(t, src, "ui-state.json", "new")
	bkWrite(t, dst, "ui-state.json", "old")
	_ = SecretStoreFor("mcode").Set("openai.apiKey", "secret")
	archive := bkCreate(t, src, false)
	bkStage(t, dst, archive)
	if err := ConfirmPending(dst); err != nil {
		t.Fatal(err)
	}
	bkApplyHook = func() error { return errors.New("interrupted") }
	if result, err := ApplyPending(dst); err != nil || result.Error == "" {
		t.Fatalf("expected rollback: %+v %v", result, err)
	}
	bkApplyHook = nil
	bkStage(t, dst, archive)
	if err := ConfirmPending(dst); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPending(dst); err != nil {
		t.Fatal(err)
	}
	stores["mcode"] = bkFailStore{SecretStore: config.NewMemorySecretStore(), setErr: keyring.ErrUnsupported}
	s := bkOpen(t, dst)
	defer s.Close()
	result, err := CompletePending(context.Background(), dst, s)
	if err != nil || len(result.SecretFailures) != 1 || result.SecretFailures[0].Reason != "系统凭据库不可用" {
		t.Fatalf("failures: %+v %v", result, err)
	}
	data, err := os.ReadFile(filepath.Join(dst, "ui-state.json"))
	if err != nil || string(data) != "new" {
		t.Fatalf("restored: %s %v", data, err)
	}
}
func TestApplyPendingRollsBackInstalledFiles(t *testing.T) {
	bkSetup(t)
	src, dst := t.TempDir(), t.TempDir()
	bkWrite(t, src, "config.json", `{"version":9,"theme":"new"}`)
	bkWrite(t, src, "ui-state.json", "new")
	bkWrite(t, dst, "config.json", `{"version":9,"theme":"old"}`)
	archive := bkCreate(t, src, false)
	bkStage(t, dst, archive)
	if err := ConfirmPending(dst); err != nil {
		t.Fatal(err)
	}
	bkApplyHook = func() error {
		var journal bkJournal
		if err := bkReadJSON(filepath.Join(dst, "restore-pending", "journal.json"), &journal); err != nil {
			return err
		}
		installed := map[string]bool{}
		for _, e := range journal.Entries {
			installed[e.Path] = e.Installed
		}
		if installed["config.json"] && installed["ui-state.json"] {
			return errors.New("interrupted")
		}
		return nil
	}
	result, err := ApplyPending(dst)
	if err != nil || result.Error == "" {
		t.Fatalf("apply: %+v %v", result, err)
	}
	data, err := os.ReadFile(filepath.Join(dst, "config.json"))
	if err != nil || string(data) != `{"version":9,"theme":"old"}` {
		t.Fatalf("config: %s %v", data, err)
	}
	for _, name := range []string{"ui-state.json", "restore-pending"} {
		if _, err := os.Stat(filepath.Join(dst, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s still exists: %v", name, err)
		}
	}
	if saved, err := TakeResult(dst); err != nil || saved.Error != result.Error {
		t.Fatalf("result: %+v %v", saved, err)
	}
}

func TestCompletePendingRollsBackMergeFailure(t *testing.T) {
	bkSetup(t)
	ctx := context.Background()
	src, dst := t.TempDir(), t.TempDir()
	source := bkOpen(t, src)
	bkThread(t, source, "source", src)
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	target := bkOpen(t, dst)
	bkThread(t, target, "target", dst)
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	bkWrite(t, src, "config.json", `{"version":9,"theme":"new"}`)
	bkWrite(t, dst, "config.json", `{"version":9,"theme":"old"}`)
	archive := bkCreate(t, src, false)
	bkStage(t, dst, archive)
	if err := ConfirmPending(dst); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPending(dst); err != nil {
		t.Fatal(err)
	}
	bkWrite(t, dst, "restore-pending/db/mcode.db", "corrupt")
	target = bkOpen(t, dst)
	defer target.Close()
	result, err := CompletePending(ctx, dst, target)
	if err != nil || result.Error == "" {
		t.Fatalf("complete: %+v %v", result, err)
	}
	threads, err := target.ListThreads(ctx, "target", false)
	if err != nil || len(threads) != 1 {
		t.Fatalf("threads: %+v %v", threads, err)
	}
	data, err := os.ReadFile(filepath.Join(dst, "config.json"))
	if err != nil || string(data) != `{"version":9,"theme":"old"}` {
		t.Fatalf("config: %s %v", data, err)
	}
	if saved, err := TakeResult(dst); err != nil || saved.Error != result.Error {
		t.Fatalf("result: %+v %v", saved, err)
	}
}

func TestBackupMaliciousArchives(t *testing.T) {
	bkSetup(t)
	for _, tc := range []struct {
		name string
		typ  byte
	}{{"../escape", tar.TypeReg}, {"/absolute", tar.TypeReg}, {"files/agentcfg/link", tar.TypeSymlink}} {
		t.Run(tc.name, func(t *testing.T) {
			var plain, encrypted bytes.Buffer
			tw := tar.NewWriter(&plain)
			if err := tw.WriteHeader(&tar.Header{Name: tc.name, Mode: 0600, Typeflag: tc.typ}); err != nil {
				t.Fatal(err)
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := Encrypt(context.Background(), &encrypted, []byte("password"), &plain); err != nil {
				t.Fatal(err)
			}
			dst := t.TempDir()
			file := filepath.Join(t.TempDir(), "archive")
			if err := os.WriteFile(file, encrypted.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Stage(context.Background(), StageOptions{DataDir: dst, Source: file, Password: []byte("password")}); !errors.Is(err, ErrInvalidArchive) {
				t.Fatalf("got %v", err)
			}
		})
	}
}
func TestBackupWrongPasswordAndVersion(t *testing.T) {
	bkSetup(t)
	src, dst := t.TempDir(), t.TempDir()
	archive := bkCreate(t, src, false)
	if _, err := Stage(context.Background(), StageOptions{DataDir: dst, Source: archive, Password: []byte("wrong")}); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "restore-pending")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pending exists")
	}
	// Create a valid backup first, then replace its config with a newer version before encryption.
	manifest := bkManifest{Format: 1, ConfigVersion: config.Version + 1, RestoreID: strings.Repeat("a", 32)}
	data, _ := json.Marshal(manifest)
	var plain, encrypted bytes.Buffer
	tw := tar.NewWriter(&plain)
	_ = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(data))})
	_, _ = tw.Write(data)
	_ = tw.Close()
	if err := Encrypt(context.Background(), &encrypted, []byte("password"), &plain); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive, encrypted.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Stage(context.Background(), StageOptions{DataDir: dst, Source: archive, Password: []byte("password")}); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("got %v", err)
	}
}
