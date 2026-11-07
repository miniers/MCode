package backup

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"mcode/internal/config"
	"mcode/internal/native/keyring"
	"mcode/internal/store"
)

type bkJournalEntry struct {
	Path      string `json:"path"`
	Existed   bool   `json:"existed"`
	Moved     bool   `json:"moved"`
	Installed bool   `json:"installed"`
}
type bkJournal struct {
	RestoreID         string           `json:"restore_id"`
	History           bool             `json:"history"`
	RollbackDir       string           `json:"rollback_dir"`
	Entries           []bkJournalEntry `json:"entries"`
	Applied           bool             `json:"applied"`
	InvalidProjectIDs []string         `json:"invalid_project_ids,omitempty"`
}

// bkApplyHook permits tests to simulate interruption after a durable journal update.
var bkApplyHook func() error

func bkAtomicJSON(name string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return bkAtomicWrite(name, data)
}
func bkAtomicWrite(name string, data []byte) (err error) {
	f, err := os.CreateTemp(filepath.Dir(name), ".backup-write-*")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), name); err != nil {
		return err
	}
	return bkSyncDir(filepath.Dir(name))
}
func bkSyncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func bkReadJSON(name string, value any) error {
	data, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}
func bkSafeName(name string) bool {
	if name == "" || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") || strings.Contains(name, ":") || path.Clean(name) != name {
		return false
	}
	for _, p := range strings.Split(name, "/") {
		if p == ".." || p == "." || p == "" {
			return false
		}
	}
	return true
}
func bkInvalid(err error) error { return fmt.Errorf("%w: %v", ErrInvalidArchive, err) }

func Stage(ctx context.Context, opts StageOptions) (Result, error) {
	var result Result
	if err := os.MkdirAll(opts.DataDir, 0700); err != nil {
		return result, err
	}
	dir, err := os.MkdirTemp(opts.DataDir, ".restore-*")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(dir)
	src, err := os.Open(opts.Source)
	if err != nil {
		return result, err
	}
	defer src.Close()
	reader, writer := io.Pipe()
	decrypted := make(chan error, 1)
	go func() { _, e := Decrypt(ctx, writer, opts.Password, src); _ = writer.CloseWithError(e); decrypted <- e }()
	tr := tar.NewReader(reader)
	seen := map[string]bkFile{}
	var manifest bkManifest
	var haveManifest bool
	var total int64
	var unpackErr error
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			unpackErr = e
			break
		}
		if e = ctx.Err(); e != nil {
			unpackErr = e
			break
		}
		if !bkSafeName(h.Name) || (h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA) || h.Size < 0 || total > bkLargeLimit-512 || h.Size > bkLargeLimit-total-512 {
			unpackErr = ErrInvalidArchive
			break
		}
		if _, ok := seen[h.Name]; ok {
			unpackErr = ErrInvalidArchive
			break
		}
		if h.Name != "manifest.json" && h.Name != "secrets.json" && h.Name != "db/mcode.db" && !bkAllowed(h.Name, true) {
			unpackErr = ErrInvalidArchive
			break
		}
		total += h.Size + 512
		if h.Name == "manifest.json" {
			if h.Size > 8<<20 {
				unpackErr = ErrInvalidArchive
				break
			}
			data, e := io.ReadAll(tr)
			if e != nil {
				unpackErr = e
				break
			}
			if e = json.Unmarshal(data, &manifest); e != nil {
				unpackErr = ErrInvalidArchive
				break
			}
			haveManifest = true
			seen[h.Name] = bkFile{Path: h.Name, Size: h.Size}
			continue
		}
		name := filepath.Join(dir, filepath.FromSlash(h.Name))
		if e = os.MkdirAll(filepath.Dir(name), 0700); e != nil {
			unpackErr = e
			break
		}
		f, e := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			unpackErr = e
			break
		}
		hash := sha256.New()
		_, e = io.Copy(io.MultiWriter(f, hash), tr)
		closeErr := f.Close()
		if e != nil {
			unpackErr = e
			break
		}
		if closeErr != nil {
			unpackErr = closeErr
			break
		}
		seen[h.Name] = bkFile{Path: h.Name, Size: h.Size, SHA256: hex.EncodeToString(hash.Sum(nil))}
	}
	if unpackErr != nil {
		_ = reader.CloseWithError(unpackErr)
	} else {
		_, unpackErr = io.Copy(io.Discard, reader)
		_ = reader.Close()
	}
	decryptErr := <-decrypted
	if errors.Is(decryptErr, ErrWrongPassword) || errors.Is(decryptErr, ErrUnsupportedVersion) {
		return result, decryptErr
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if unpackErr != nil || decryptErr != nil {
		return result, bkInvalid(errors.Join(unpackErr, decryptErr))
	}
	if !haveManifest || manifest.Format != 1 || len(manifest.RestoreID) != 32 || total > bkLimit(manifest.IncludeHistory) {
		return result, ErrInvalidArchive
	}
	if _, e := hex.DecodeString(manifest.RestoreID); e != nil {
		return result, ErrInvalidArchive
	}
	if manifest.ConfigVersion > config.Version {
		return result, ErrUnsupportedVersion
	}
	listed := map[string]bool{}
	for _, item := range manifest.Files {
		if listed[item.Path] || !bkAllowed(item.Path, manifest.IncludeHistory) || seen[item.Path] != item {
			return result, ErrInvalidArchive
		}
		listed[item.Path] = true
	}
	if len(listed) != len(seen)-1 || !listed["secrets.json"] {
		return result, ErrInvalidArchive
	}
	var secrets []bkSecret
	if e := bkReadJSON(filepath.Join(dir, "secrets.json"), &secrets); e != nil {
		return result, bkInvalid(e)
	}
	if listed["files/config.json"] {
		var cfg struct {
			Version int `json:"version"`
		}
		if e := bkReadJSON(filepath.Join(dir, "files", "config.json"), &cfg); e != nil {
			return result, bkInvalid(e)
		}
		if cfg.Version > config.Version {
			return result, ErrUnsupportedVersion
		}
	}
	if listed["db/mcode.db"] {
		db := filepath.Join(dir, "db", "mcode.db")
		if e := store.PrepareSnapshot(ctx, db); e != nil {
			if errors.Is(e, store.ErrSnapshotTooNew) {
				return result, fmt.Errorf("%w: %v", ErrUnsupportedVersion, e)
			}
			return result, bkInvalid(e)
		}
		projects, e := store.SnapshotProjectPaths(ctx, db)
		if e != nil {
			return result, bkInvalid(e)
		}
		for id, p := range projects {
			info, e := os.Stat(p)
			if e != nil || !info.IsDir() {
				result.InvalidProjectIDs = append(result.InvalidProjectIDs, id)
			}
		}
		sort.Strings(result.InvalidProjectIDs)
	}
	if err := bkAtomicJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		return result, err
	}
	if err := bkAtomicJSON(filepath.Join(dir, "preview.json"), result); err != nil {
		return result, err
	}
	pending := filepath.Join(opts.DataDir, "restore-pending")
	if err := os.RemoveAll(pending); err != nil {
		return result, err
	}
	if err := os.Rename(dir, pending); err != nil {
		return result, err
	}
	return result, nil
}

func ConfirmPending(dataDir string) error {
	return bkAtomicWrite(filepath.Join(dataDir, "restore-pending", "confirmed"), []byte("confirmed"))
}
func DiscardPending(dataDir string) error {
	return os.RemoveAll(filepath.Join(dataDir, "restore-pending"))
}

func bkCopyFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err = os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(dst), ".restore-file-*")
	if err != nil {
		return err
	}
	defer func() { _ = out.Close(); _ = os.Remove(out.Name()) }()
	if err = out.Chmod(0600); err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	if err = os.Rename(out.Name(), dst); err != nil {
		return err
	}
	return bkSyncDir(filepath.Dir(dst))
}

func bkPreserve(dst, source map[string]any, keys ...string) {
	if len(keys) == 0 {
		return
	}
	if len(keys) == 1 {
		if v, ok := source[keys[0]]; ok {
			dst[keys[0]] = v
		} else {
			delete(dst, keys[0])
		}
		return
	}
	child, ok := dst[keys[0]].(map[string]any)
	if !ok {
		return
	}
	original, _ := source[keys[0]].(map[string]any)
	bkPreserve(child, original, keys[1:]...)
}
func bkMergeConfig(source, target string) ([]byte, error) {
	var incoming map[string]any
	if err := bkReadJSON(source, &incoming); err != nil {
		return nil, err
	}
	var current map[string]any
	if err := bkReadJSON(target, &current); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	bkPreserve(incoming, current, "remote")
	bkPreserve(incoming, current, "workbench", "terminal_shell")
	bkPreserve(incoming, current, "orchestration", "steward", "home_dir")
	providers, _ := incoming["providers"].(map[string]any)
	oldProviders, _ := current["providers"].(map[string]any)
	for key, value := range providers {
		if key == "acp" {
			acps, _ := value.(map[string]any)
			old, _ := oldProviders[key].(map[string]any)
			for id, v := range acps {
				item, _ := v.(map[string]any)
				orig, _ := old[id].(map[string]any)
				if item != nil {
					bkPreserve(item, orig, "bin")
				}
			}
			continue
		}
		item, _ := value.(map[string]any)
		orig, _ := oldProviders[key].(map[string]any)
		if item != nil {
			bkPreserve(item, orig, "bin")
		}
	}
	return json.Marshal(incoming)
}

func bkNoSymlink(root, relative string) error {
	p := root
	for _, part := range strings.Split(filepath.FromSlash(relative), string(filepath.Separator)) {
		p = filepath.Join(p, part)
		info, err := os.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("restore path contains symlink: %s", p)
		}
	}
	return nil
}

func bkRollback(dataDir string, journal bkJournal) error {
	for i := len(journal.Entries) - 1; i >= 0; i-- {
		e := journal.Entries[i]
		if err := bkNoSymlink(dataDir, e.Path); err != nil {
			return err
		}
		target := filepath.Join(dataDir, filepath.FromSlash(e.Path))
		old := filepath.Join(journal.RollbackDir, filepath.FromSlash(e.Path))
		if e.Installed || (e.Moved && !e.Existed) {
			if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if e.Existed && e.Moved {
			if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := os.Rename(old, target); err != nil {
				return err
			}
			if err := bkSyncDir(filepath.Dir(old)); err != nil {
				return err
			}
		}
		if e.Moved || e.Installed {
			if err := bkSyncDir(filepath.Dir(target)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	if err := bkSyncDir(dataDir); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(dataDir, "restore-pending")); err != nil {
		return err
	}
	return bkSyncDir(dataDir)
}

func bkRestoreFailure(dataDir string, journal *bkJournal, cause error) (Result, error) {
	if journal != nil {
		if err := bkRollback(dataDir, *journal); err != nil {
			return Result{}, errors.Join(cause, err)
		}
	} else if err := os.RemoveAll(filepath.Join(dataDir, "restore-pending")); err != nil {
		return Result{}, errors.Join(cause, err)
	}
	result := Result{Error: "恢复失败，已还原：" + cause.Error()}
	if err := bkAtomicJSON(filepath.Join(dataDir, "restore-result.json"), result); err != nil {
		return Result{}, errors.Join(cause, err)
	}
	return result, nil
}

func ApplyPending(dataDir string) (Result, error) {
	var journal bkJournal
	result, err := bkApplyPending(dataDir, &journal)
	if err == nil {
		return result, nil
	}
	if journal.RollbackDir == "" {
		journalErr := bkReadJSON(filepath.Join(dataDir, "restore-pending", "journal.json"), &journal)
		if journalErr != nil && !errors.Is(journalErr, os.ErrNotExist) {
			return Result{}, errors.Join(err, journalErr)
		}
	}
	if journal.RollbackDir == "" {
		return bkRestoreFailure(dataDir, nil, err)
	}
	return bkRestoreFailure(dataDir, &journal, err)
}

func bkApplyPending(dataDir string, journal *bkJournal) (Result, error) {
	var result Result
	pending := filepath.Join(dataDir, "restore-pending")
	if _, err := os.Stat(filepath.Join(pending, "confirmed")); errors.Is(err, os.ErrNotExist) {
		return result, nil
	} else if err != nil {
		return result, err
	}
	var manifest bkManifest
	if err := bkReadJSON(filepath.Join(pending, "manifest.json"), &manifest); err != nil {
		return result, err
	}
	journalPath := filepath.Join(pending, "journal.json")
	err := bkReadJSON(journalPath, journal)
	if errors.Is(err, os.ErrNotExist) {
		*journal = bkJournal{RestoreID: manifest.RestoreID, History: manifest.IncludeHistory, RollbackDir: filepath.Join(dataDir, "pre-restore-"+time.Now().Format("20060102-150405.000000000"))}
		var preview Result
		if e := bkReadJSON(filepath.Join(pending, "preview.json"), &preview); e != nil {
			return result, e
		}
		journal.InvalidProjectIDs = preview.InvalidProjectIDs
		for _, item := range manifest.Files {
			if strings.HasPrefix(item.Path, "files/") {
				journal.Entries = append(journal.Entries, bkJournalEntry{Path: strings.TrimPrefix(item.Path, "files/")})
			}
		}
		if manifest.IncludeHistory {
			for _, item := range manifest.Files {
				if item.Path == "db/mcode.db" {
					journal.Entries = append(journal.Entries, bkJournalEntry{Path: "mcode.db-wal"}, bkJournalEntry{Path: "mcode.db-shm"}, bkJournalEntry{Path: "mcode.db"})
					break
				}
			}
		}
		if err = os.MkdirAll(journal.RollbackDir, 0700); err != nil {
			return result, err
		}
		for i := range journal.Entries {
			p := journal.Entries[i].Path
			if err = bkNoSymlink(dataDir, p); err != nil {
				return result, err
			}
			_, e := os.Stat(filepath.Join(dataDir, filepath.FromSlash(p)))
			if e == nil {
				journal.Entries[i].Existed = true
			} else if !errors.Is(e, os.ErrNotExist) {
				return result, e
			}
		}
		// Keep a consistent database rollback copy even when the database will be merged.
		if !manifest.IncludeHistory {
			if err = bkNoSymlink(dataDir, "mcode.db"); err != nil {
				return result, err
			}
			db := filepath.Join(dataDir, "mcode.db")
			if _, e := os.Stat(db); e == nil {
				rollbackDB := filepath.Join(journal.RollbackDir, "mcode.db")
				if e = store.SnapshotFile(context.Background(), db, rollbackDB, true); e != nil {
					return result, e
				}
				if e = os.Chmod(rollbackDB, 0600); e != nil {
					return result, e
				}
			} else if !errors.Is(e, os.ErrNotExist) {
				return result, e
			}
		}
		if err = bkAtomicJSON(journalPath, journal); err != nil {
			return result, err
		}
	} else if err != nil {
		return result, err
	}
	for i := range journal.Entries {
		e := &journal.Entries[i]
		if err = bkNoSymlink(dataDir, e.Path); err != nil {
			return result, err
		}
		target := filepath.Join(dataDir, filepath.FromSlash(e.Path))
		old := filepath.Join(journal.RollbackDir, filepath.FromSlash(e.Path))
		if !e.Moved {
			if e.Existed {
				if err = os.MkdirAll(filepath.Dir(old), 0700); err != nil {
					return result, err
				}
				if _, statErr := os.Stat(old); errors.Is(statErr, os.ErrNotExist) {
					if err = os.Rename(target, old); err != nil {
						return result, err
					}
					e.Moved = true
					if err = bkSyncDir(filepath.Dir(target)); err != nil {
						return result, err
					}
					if err = bkSyncDir(filepath.Dir(old)); err != nil {
						return result, err
					}
				} else if statErr != nil {
					return result, statErr
				}
				e.Moved = true
				if err = os.Chmod(old, 0600); err != nil {
					return result, err
				}
			}
			e.Moved = true
			if err = bkAtomicJSON(journalPath, journal); err != nil {
				return result, err
			}
			if bkApplyHook != nil {
				if err = bkApplyHook(); err != nil {
					return result, err
				}
			}
		}
		if !e.Installed {
			e.Installed = true
			source := filepath.Join(pending, "files", filepath.FromSlash(e.Path))
			if e.Path == "mcode.db" {
				source = filepath.Join(pending, "db", "mcode.db")
			}
			if e.Path != "mcode.db-wal" && e.Path != "mcode.db-shm" {
				if e.Path == "config.json" {
					var data []byte
					data, err = bkMergeConfig(source, old)
					if err == nil {
						err = os.MkdirAll(filepath.Dir(target), 0700)
					}
					if err == nil {
						err = bkAtomicWrite(target, data)
					}
				} else {
					err = bkCopyFile(source, target)
				}
				if err != nil {
					return result, err
				}
			}
			e.Installed = true
			if err = bkAtomicJSON(journalPath, journal); err != nil {
				return result, err
			}
			if bkApplyHook != nil {
				if err = bkApplyHook(); err != nil {
					return result, err
				}
			}
		}
	}
	journal.Applied = true
	if err = bkAtomicJSON(journalPath, journal); err != nil {
		return result, err
	}
	return Result{Applied: true, RollbackDir: journal.RollbackDir, InvalidProjectIDs: journal.InvalidProjectIDs}, nil
}

func CompletePending(ctx context.Context, dataDir string, s *store.Store) (Result, error) {
	var result Result
	pending := filepath.Join(dataDir, "restore-pending")
	var journal bkJournal
	if err := bkReadJSON(filepath.Join(pending, "journal.json"), &journal); errors.Is(err, os.ErrNotExist) {
		return result, nil
	} else if err != nil {
		return result, err
	}
	if !journal.Applied {
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	var secrets []bkSecret
	if err := bkReadJSON(filepath.Join(pending, "secrets.json"), &secrets); err != nil {
		return bkRestoreFailure(dataDir, &journal, err)
	}
	if !journal.History {
		db := filepath.Join(pending, "db", "mcode.db")
		if _, err := os.Stat(db); err == nil {
			if err = s.MergeBackup(ctx, db, journal.RestoreID); err != nil {
				if ctx.Err() != nil {
					return result, ctx.Err()
				}
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return result, err
				}
				return bkRestoreFailure(dataDir, &journal, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return bkRestoreFailure(dataDir, &journal, err)
		}
	}
	result = Result{Applied: true, RollbackDir: journal.RollbackDir, InvalidProjectIDs: journal.InvalidProjectIDs}
	for _, secret := range secrets {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		if err := SecretStoreFor(secret.Service).Set(secret.Account, secret.Value); err != nil {
			reason := "写入失败"
			if errors.Is(err, keyring.ErrUnsupported) {
				reason = "系统凭据库不可用"
			}
			result.SecretFailures = append(result.SecretFailures, SecretFailure{Service: secret.Service, Account: secret.Account, Reason: reason})
		}
	}
	if err := bkAtomicJSON(filepath.Join(dataDir, "restore-result.json"), result); err != nil {
		return Result{}, err
	}
	if err := os.RemoveAll(pending); err != nil {
		return Result{}, err
	}
	return result, nil
}
func TakeResult(dataDir string) (Result, error) {
	var result Result
	name := filepath.Join(dataDir, "restore-result.json")
	if err := bkReadJSON(name, &result); errors.Is(err, os.ErrNotExist) {
		return result, nil
	} else if err != nil {
		return Result{}, err
	}
	if err := os.Remove(name); err != nil {
		return Result{}, err
	}
	return result, nil
}
