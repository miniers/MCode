package backup

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mcode/internal/agentconf"
	"mcode/internal/config"
	"mcode/internal/store"
)

// DiscoverSecretRefs returns the configured credential references in stable order.
func DiscoverSecretRefs(dataDir string, cfg config.Config, projectPaths []string) ([]config.SecretRef, error) {
	seen := make(map[config.SecretRef]bool)
	for _, ref := range config.LegacySecretRefs(nil) {
		seen[ref] = true
	}
	add := func(account string) {
		seen[config.SecretRef{Service: "mcode", Account: account}] = true
	}
	for _, id := range []string{"openai", "droid", "deepseek", "zai", "meta", "magpie1", "magpie2", "magpie3", "magpie4"} {
		add(id + ".apiKey")
	}
	for id := range cfg.Providers.ACP {
		add(id + ".apiKey")
	}
	for id := range cfg.Providers.Presets {
		add(id + ".apiKey")
	}
	for _, endpoint := range cfg.Providers.Custom {
		add(config.CustomEndpointSecretAccount(endpoint.ID))
		add(config.CustomEndpointWebKeySecretAccount(endpoint.ID))
		for _, id := range endpoint.QuotaProviders {
			add(id + ".apiKey")
		}
	}
	refs, err := agentconf.BackupMCPSecretRefs(dataDir, projectPaths)
	if err != nil {
		return nil, err
	}
	for _, ref := range refs {
		seen[ref] = true
	}
	refs = make([]config.SecretRef, 0, len(seen))
	for ref := range seen {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Service != refs[j].Service {
			return refs[i].Service < refs[j].Service
		}
		return refs[i].Account < refs[j].Account
	})
	return refs, nil
}

const (
	bkSmallLimit int64 = 512 << 20
	bkLargeLimit int64 = 64 << 30
)

type bkFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type bkManifest struct {
	Format         int      `json:"format"`
	IncludeHistory bool     `json:"include_history"`
	ConfigVersion  int      `json:"config_version"`
	RestoreID      string   `json:"restore_id"`
	Files          []bkFile `json:"files"`
	Missing        []string `json:"missing,omitempty"`
}

type bkSecret struct {
	Service string `json:"service"`
	Account string `json:"account"`
	Value   string `json:"value"`
}

var bkRoots = []string{"config.json", "ui-state.json", "mcp.json", "mcp_tool_policy.json", "skills.json", "skills-projects", "always_allow.json", "remote/project-scripts.json", "remote/script-favorites.json", "review-checks", "agentcfg"}

func bkAllowed(p string, history bool) bool {
	if p == "db/mcode.db" || p == "secrets.json" {
		return true
	}
	if !strings.HasPrefix(p, "files/") {
		return false
	}
	p = strings.TrimPrefix(p, "files/")
	for _, part := range strings.Split(p, "/") {
		switch part {
		case "remote-devices.json", "remote-webpush-vapid.pem", "remote-tls", "tailnet", "logs", "cache", "caches", "attachments", "worktrees", "swarm-worktrees", "shadow-git", "swarm-patches", "swarm-landed", "swarm-reviews", "config.v7.json", "config.json.bak", "mcode.db.compacted":
			return false
		}
		if strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	roots := bkRoots
	if history {
		roots = append(append([]string(nil), roots...), "goals", "media")
	}
	for _, root := range roots {
		if p == root {
			return true
		}
		switch root {
		case "skills-projects", "review-checks", "agentcfg", "goals", "media":
			if strings.HasPrefix(p, root+"/") {
				return true
			}
		}
	}
	return false
}

func bkLimit(history bool) int64 {
	if history {
		return bkLargeLimit
	}
	return bkSmallLimit
}

func Create(ctx context.Context, opts CreateOptions) (err error) {
	if err = os.MkdirAll(opts.DataDir, 0700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(opts.DataDir, ".backup-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	cfg, err := config.Load(opts.DataDir)
	if err != nil {
		return err
	}
	manifest := bkManifest{Format: 1, IncludeHistory: opts.IncludeHistory, ConfigVersion: cfg.Version}
	var id [16]byte
	if _, err = rand.Read(id[:]); err != nil {
		return err
	}
	manifest.RestoreID = hex.EncodeToString(id[:])
	paths := map[string]string{}
	db := filepath.Join(opts.DataDir, "mcode.db")
	if _, statErr := os.Stat(db); statErr == nil {
		snapshot := filepath.Join(tmp, "snapshot.db")
		if err = bkNoSymlink(opts.DataDir, "mcode.db"); err != nil {
			return err
		}
		if err = store.SnapshotFile(ctx, db, snapshot, opts.IncludeHistory); err != nil {
			return err
		}
		if err = os.Chmod(snapshot, 0600); err != nil {
			return err
		}
		paths["db/mcode.db"] = snapshot
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	var projects []string
	if snapshot := paths["db/mcode.db"]; snapshot != "" {
		projectMap, e := store.SnapshotProjectPaths(ctx, snapshot)
		if e != nil {
			return e
		}
		for _, p := range projectMap {
			projects = append(projects, p)
		}
	}
	refs, err := DiscoverSecretRefs(opts.DataDir, cfg, projects)
	if err != nil {
		return err
	}
	secrets := []bkSecret{}
	for _, ref := range refs {
		if err = ctx.Err(); err != nil {
			return err
		}
		value, e := SecretStoreFor(ref.Service).Get(ref.Account)
		if errors.Is(e, config.ErrSecretNotFound) {
			continue
		}
		if e != nil {
			return fmt.Errorf("read secret %s/%s: %w", ref.Service, ref.Account, e)
		}
		secrets = append(secrets, bkSecret{ref.Service, ref.Account, value})
	}
	secretJSON, err := json.Marshal(secrets)
	if err != nil {
		return err
	}
	roots := append([]string(nil), bkRoots...)
	if opts.IncludeHistory {
		roots = append(roots, "goals", "media")
	}
	for _, root := range roots {
		base := filepath.Join(opts.DataDir, filepath.FromSlash(root))
		if e := bkNoSymlink(opts.DataDir, root); e != nil {
			return e
		}
		_, e := os.Lstat(base)
		if errors.Is(e, os.ErrNotExist) {
			manifest.Missing = append(manifest.Missing, root)
			continue
		}
		if e != nil {
			return e
		}
		e = filepath.WalkDir(base, func(p string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if e := ctx.Err(); e != nil {
				return e
			}
			rel, e := filepath.Rel(opts.DataDir, p)
			if e != nil {
				return e
			}
			name := "files/" + filepath.ToSlash(rel)
			if !bkAllowed(name, opts.IncludeHistory) {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.Type().IsRegular() {
				paths[name] = p
			}
			return nil
		})
		if e != nil {
			return e
		}
	}
	names := make([]string, 0, len(paths)+1)
	for name := range paths {
		names = append(names, name)
	}
	names = append(names, "secrets.json")
	sort.Strings(names)
	var total int64
	for _, name := range names {
		var size int64
		hash := sha256.New()
		if name == "secrets.json" {
			size = int64(len(secretJSON))
			_, _ = hash.Write(secretJSON)
		} else {
			f, e := os.Open(paths[name])
			if e != nil {
				return e
			}
			size, e = io.Copy(hash, io.LimitReader(f, bkLimit(opts.IncludeHistory)-total+1))
			closeErr := f.Close()
			if e != nil {
				return e
			}
			if closeErr != nil {
				return closeErr
			}
		}
		if size > bkLimit(opts.IncludeHistory)-total {
			return fmt.Errorf("backup size exceeds limit")
		}
		total += size
		manifest.Files = append(manifest.Files, bkFile{name, size, hex.EncodeToString(hash.Sum(nil))})
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(opts.Destination), ".mcodebackup-*")
	if err != nil {
		return err
	}
	defer func() {
		_ = out.Close()
		if err != nil {
			_ = os.Remove(out.Name())
		}
	}()
	if err = out.Chmod(0600); err != nil {
		return err
	}
	reader, writer := io.Pipe()
	go func() {
		tw := tar.NewWriter(writer)
		write := func(name string, size int64, src io.Reader) error {
			if e := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: size, Typeflag: tar.TypeReg}); e != nil {
				return e
			}
			_, e := io.CopyN(tw, src, size)
			return e
		}
		e := write("manifest.json", int64(len(manifestJSON)), strings.NewReader(string(manifestJSON)))
		for _, item := range manifest.Files {
			if e != nil {
				break
			}
			if e = ctx.Err(); e != nil {
				break
			}
			if item.Path == "secrets.json" {
				e = write(item.Path, item.Size, strings.NewReader(string(secretJSON)))
				continue
			}
			var f *os.File
			f, e = os.Open(paths[item.Path])
			if e != nil {
				break
			}
			hash := sha256.New()
			e = write(item.Path, item.Size, io.TeeReader(f, hash))
			closeErr := f.Close()
			if e == nil {
				e = closeErr
			}
			if e == nil && hex.EncodeToString(hash.Sum(nil)) != item.SHA256 {
				e = fmt.Errorf("file changed while backing up: %s", item.Path)
			}
		}
		if e == nil {
			e = tw.Close()
		}
		_ = writer.CloseWithError(e)
	}()
	err = Encrypt(ctx, out, opts.Password, reader)
	_ = reader.CloseWithError(err)
	if err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	return os.Rename(out.Name(), opts.Destination)
}
