package remote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"mcode/internal/fsutil"
	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

func TestFilesRenameSymlink(t *testing.T) {
	b, _, workDir, _, threadID := setupWorkspaceTestEnv(t)
	ctx := context.Background()

	targetPath := filepath.Join(workDir, "target.txt")
	if err := os.WriteFile(targetPath, []byte("keep me"), 0o644); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	linkPath := filepath.Join(workDir, "link.txt")
	if err := os.Symlink("target.txt", linkPath); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	res, err := b.FilesRename(ctx, wire.FilesRenameParams{
		ThreadID: threadID,
		Path:     "link.txt",
		NewPath:  "renamed-link.txt",
	})
	if err != nil {
		t.Fatalf("FilesRename symlink: %v", err)
	}
	if res.Path != "renamed-link.txt" {
		t.Fatalf("unexpected result path: %q", res.Path)
	}

	if _, err := os.Lstat(linkPath); !os.IsNotExist(err) {
		t.Fatalf("old symlink must be gone, got err=%v", err)
	}
	gotTarget, err := os.ReadFile(targetPath)
	if err != nil || string(gotTarget) != "keep me" {
		t.Fatalf("target file must remain intact, got %q err=%v", gotTarget, err)
	}
	fi, err := os.Lstat(filepath.Join(workDir, "renamed-link.txt"))
	if err != nil {
		t.Fatalf("lstat renamed symlink: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("renamed path must remain a symlink, got mode=%v", fi.Mode())
	}
}

func TestFilesTrashSymlink(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("trash is only supported on darwin")
	}
	b, _, workDir, _, threadID := setupWorkspaceTestEnv(t)
	ctx := context.Background()

	trashRoot := filepath.Join(t.TempDir(), "Trash")
	if err := os.Mkdir(trashRoot, 0o700); err != nil {
		t.Fatalf("create fake trash: %v", err)
	}
	original := fsutil.TrashDir
	fsutil.TrashDir = func() (string, error) { return trashRoot, nil }
	t.Cleanup(func() { fsutil.TrashDir = original })

	targetPath := filepath.Join(workDir, "target.txt")
	if err := os.WriteFile(targetPath, []byte("keep me"), 0o644); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	linkPath := filepath.Join(workDir, "link.txt")
	if err := os.Symlink("target.txt", linkPath); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	if _, err := b.FilesTrash(ctx, wire.FilesTrashParams{
		ThreadID: threadID,
		Path:     "link.txt",
	}); err != nil {
		t.Fatalf("FilesTrash symlink: %v", err)
	}

	if _, err := os.Lstat(linkPath); !os.IsNotExist(err) {
		t.Fatalf("symlink must be moved out of workspace, got err=%v", err)
	}
	gotTarget, err := os.ReadFile(targetPath)
	if err != nil || string(gotTarget) != "keep me" {
		t.Fatalf("target file must remain intact, got %q err=%v", gotTarget, err)
	}
	trashedInfo, err := os.Lstat(filepath.Join(trashRoot, "link.txt"))
	if err != nil {
		t.Fatalf("lstat trashed symlink: %v", err)
	}
	if trashedInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("trashed entry must be the symlink itself, got mode=%v", trashedInfo.Mode())
	}
}

func TestPairDeviceNameUTF8(t *testing.T) {
	tmpDir := t.TempDir()
	devicesPath := filepath.Join(tmpDir, "remote_devices.json")

	auth, err := NewPairingAuth(PairingAuthOptions{
		DevicesPath: devicesPath,
		LANBaseURL:  func() string { return "http://192.168.1.10:43117" },
	})
	if err != nil {
		t.Fatalf("NewPairingAuth failed: %v", err)
	}

	info := auth.CurrentPairingURLs()
	code := strings.Split(info.PairingURL, "#pair=")[1]
	longChineseName := strings.Repeat("汉", 30)

	res, err := auth.Pair(code, longChineseName, ClientInfo{})
	if err != nil {
		t.Fatalf("Pair failed: %v", err)
	}
	if !utf8.ValidString(res.Label) {
		t.Fatalf("paired label must be valid UTF-8: %q", res.Label)
	}
	if len(res.Label) == 0 || len(res.Label) > 64 {
		t.Fatalf("paired label byte length must be within (0, 64], got %d", len(res.Label))
	}

	records := auth.Store().AllRecords()
	if len(records) != 1 {
		t.Fatalf("expected 1 device record, got %d", len(records))
	}
	savedLabel := records[0].Label
	if !utf8.ValidString(savedLabel) {
		t.Fatalf("saved label must be valid UTF-8: %q", savedLabel)
	}
	if len(savedLabel) == 0 || len(savedLabel) > 64 {
		t.Fatalf("saved label byte length must be within (0, 64], got %d", len(savedLabel))
	}
	if savedLabel != res.Label {
		t.Fatalf("saved label %q != result label %q", savedLabel, res.Label)
	}
}

func TestAlwaysAllowNotPersistedWhenDeliveryFails(t *testing.T) {
	dataDir := t.TempDir()
	projectDir := t.TempDir()
	ctx := context.Background()
	t.Cleanup(func() { forgetAlwaysAllowStore(dataDir) })

	threadID := "00000000-0000-0000-0000-000000000f01"
	backend := newAlwaysAllowProjectBackend(t, dataDir, projectDir, threadID)
	reqSent := alwaysAllowBashRequest("ls -la")

	backend.SetHarness(&fakeDeliveryHarness{
		confirmsDelivery: true,
		runFn: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			if _, err := turn.Approver.Approve(ctx, reqSent); err != nil {
				return err
			}
			harness.AckDelivery(turn.Approver, reqSent, errors.New("pipe closed"))
			<-ctx.Done()
			return ctx.Err()
		},
	}, projectDir, harness.ModeAgent)

	changedCh := startAlwaysAllowTurn(t, backend, threadID, "00000000-0000-0000-0000-000000000f02")
	req := waitPendingApproval(t, changedCh)

	optAlways := "allow_always"
	if _, err := backend.RespondRequest(ctx, wire.RequestsRespondParams{
		ThreadID:  threadID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		OptionID:  &optAlways,
	}); err == nil {
		t.Fatal("expected RespondRequest to fail when delivery fails")
	}

	if got := backend.ListAlwaysAllowed(projectDir); len(got) != 0 {
		t.Fatalf("alwaysAllowStore must remain empty on delivery failure, got %v", got)
	}
	if got := newAlwaysAllowStore(alwaysAllowStorePath(dataDir)).List(projectDir); len(got) != 0 {
		t.Fatalf("persisted always-allow file must remain empty on delivery failure, got %v", got)
	}
	backend.mu.Lock()
	inMemLen := len(backend.threads[threadID].alwaysAllow)
	backend.mu.Unlock()
	if inMemLen != 0 {
		t.Fatalf("thread in-memory alwaysAllow must remain empty on delivery failure, got %d keys", inMemLen)
	}
}
