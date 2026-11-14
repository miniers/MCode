package remote

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"mcode/internal/fsutil"
	"mcode/internal/remote/wire"
)

// files_ops_test.go 覆盖工作区文件操作后端（files_ops.go）与对应 RPC 方法。
// 所有用例都在 setupWorkspaceTestEnv 提供的临时工作区内进行；
// 废纸篓通过替换 fsutil.TrashDir 指到临时目录，绝不碰真实的 ~/.Trash。

func requireRPCError(t *testing.T, err error, wantCode int) *wire.RPCError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected RPC error %d, got nil", wantCode)
	}
	var rpcErr *wire.RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("expected *wire.RPCError, got %T: %v", err, err)
	}
	if rpcErr.Code != wantCode {
		t.Fatalf("unexpected error code: got %d want %d (%s)", rpcErr.Code, wantCode, rpcErr.Message)
	}
	return rpcErr
}

func TestFilesCreate_FileAndDirectory(t *testing.T) {
	b, _, workDir, _, threadID := setupWorkspaceTestEnv(t)
	ctx := context.Background()

	res, err := b.FilesCreate(ctx, wire.FilesCreateParams{ThreadID: threadID, Path: "notes.txt"})
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	if res.Path != "notes.txt" {
		t.Fatalf("unexpected result path: %q", res.Path)
	}
	fi, err := os.Stat(filepath.Join(workDir, "notes.txt"))
	if err != nil {
		t.Fatalf("stat created file: %v", err)
	}
	if !fi.Mode().IsRegular() || fi.Size() != 0 {
		t.Fatalf("created file must be an empty regular file, got mode=%v size=%d", fi.Mode(), fi.Size())
	}
	if perm := fi.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o644 {
		t.Fatalf("unexpected file permission: %04o", perm)
	}

	res, err = b.FilesCreate(ctx, wire.FilesCreateParams{ThreadID: threadID, Path: "docs", Dir: true})
	if err != nil {
		t.Fatalf("create dir: %v", err)
	}
	if res.Path != "docs" {
		t.Fatalf("unexpected result path: %q", res.Path)
	}
	fi, err = os.Stat(filepath.Join(workDir, "docs"))
	if err != nil {
		t.Fatalf("stat created dir: %v", err)
	}
	if !fi.IsDir() {
		t.Fatalf("created entry must be a directory")
	}
}

func TestFilesCreate_RejectsExistingPath(t *testing.T) {
	b, _, workDir, _, threadID := setupWorkspaceTestEnv(t)
	ctx := context.Background()

	if err := os.WriteFile(filepath.Join(workDir, "taken.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	_, err := b.FilesCreate(ctx, wire.FilesCreateParams{ThreadID: threadID, Path: "taken.txt"})
	rpcErr := requireRPCError(t, err, wire.InvalidParams)
	if rpcErr.Message != "path already exists" {
		t.Fatalf("unexpected message: %q", rpcErr.Message)
	}
}

func TestFilesCreate_RejectsTraversal(t *testing.T) {
	b, _, workDir, _, threadID := setupWorkspaceTestEnv(t)
	ctx := context.Background()

	_, err := b.FilesCreate(ctx, wire.FilesCreateParams{ThreadID: threadID, Path: "../escape.txt"})
	requireRPCError(t, err, wire.InvalidParams)
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(workDir), "escape.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("path traversal must not create files outside workspace")
	}
}

func TestFilesCreate_RejectsGitAndProtectedPaths(t *testing.T) {
	b, _, workDir, _, threadID := setupWorkspaceTestEnv(t)
	ctx := context.Background()

	if err := os.Mkdir(filepath.Join(workDir, ".git"), 0o755); err != nil {
		t.Fatalf("seed .git dir: %v", err)
	}
	if _, err := b.FilesCreate(ctx, wire.FilesCreateParams{ThreadID: threadID, Path: ".git/new.txt"}); err == nil {
		t.Fatalf("creating inside .git must be rejected")
	}
	if _, err := b.FilesCreate(ctx, wire.FilesCreateParams{ThreadID: threadID, Path: ".env"}); err == nil {
		t.Fatalf("creating a protected file must be rejected")
	}
}

func TestFilesRename_Success(t *testing.T) {
	b, _, workDir, _, threadID := setupWorkspaceTestEnv(t)
	ctx := context.Background()

	if err := os.WriteFile(filepath.Join(workDir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	res, err := b.FilesRename(ctx, wire.FilesRenameParams{ThreadID: threadID, Path: "a.txt", NewPath: "b.txt"})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if res.Path != "b.txt" {
		t.Fatalf("unexpected result path: %q", res.Path)
	}
	if _, err := os.Stat(filepath.Join(workDir, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("source must be gone after rename")
	}
	got, err := os.ReadFile(filepath.Join(workDir, "b.txt"))
	if err != nil {
		t.Fatalf("read renamed file: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("content must be preserved, got %q", got)
	}
}

func TestFilesRename_RejectsExistingTarget(t *testing.T) {
	b, _, workDir, _, threadID := setupWorkspaceTestEnv(t)
	ctx := context.Background()

	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(workDir, name), []byte(name), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	_, err := b.FilesRename(ctx, wire.FilesRenameParams{ThreadID: threadID, Path: "a.txt", NewPath: "b.txt"})
	rpcErr := requireRPCError(t, err, wire.InvalidParams)
	if rpcErr.Message != "path already exists" {
		t.Fatalf("unexpected message: %q", rpcErr.Message)
	}
	got, err := os.ReadFile(filepath.Join(workDir, "b.txt"))
	if err != nil || string(got) != "b.txt" {
		t.Fatalf("existing target must not be overwritten, got %q err=%v", got, err)
	}
}

func TestFilesRename_RejectsWorkspaceRoot(t *testing.T) {
	b, _, _, _, threadID := setupWorkspaceTestEnv(t)
	ctx := context.Background()

	_, err := b.FilesRename(ctx, wire.FilesRenameParams{ThreadID: threadID, Path: ".", NewPath: "moved"})
	rpcErr := requireRPCError(t, err, wire.InvalidParams)
	if rpcErr.Message != "cannot rename workspace root" {
		t.Fatalf("unexpected message: %q", rpcErr.Message)
	}
}

func TestFilesDuplicate_GeneratesSuffixes(t *testing.T) {
	b, _, workDir, _, threadID := setupWorkspaceTestEnv(t)
	ctx := context.Background()

	source := filepath.Join(workDir, "a.txt")
	if err := os.WriteFile(source, []byte("data"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if err := os.Chmod(source, 0o600); err != nil {
		t.Fatalf("chmod source: %v", err)
	}

	res, err := b.FilesDuplicate(ctx, wire.FilesDuplicateParams{ThreadID: threadID, Path: "a.txt"})
	if err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	if res.Path != "a copy.txt" {
		t.Fatalf("unexpected first copy path: %q", res.Path)
	}
	got, err := os.ReadFile(filepath.Join(workDir, "a copy.txt"))
	if err != nil || string(got) != "data" {
		t.Fatalf("copy content mismatch: %q err=%v", got, err)
	}
	fi, err := os.Stat(filepath.Join(workDir, "a copy.txt"))
	if err != nil {
		t.Fatalf("stat copy: %v", err)
	}
	if perm := fi.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o600 {
		t.Fatalf("copy must preserve source permission, got %04o", perm)
	}

	res, err = b.FilesDuplicate(ctx, wire.FilesDuplicateParams{ThreadID: threadID, Path: "a.txt"})
	if err != nil {
		t.Fatalf("duplicate second time: %v", err)
	}
	if res.Path != "a copy 2.txt" {
		t.Fatalf("unexpected second copy path: %q", res.Path)
	}
	if _, err := os.Stat(filepath.Join(workDir, "a copy 2.txt")); err != nil {
		t.Fatalf("second copy missing: %v", err)
	}
}

func TestFilesDuplicate_RejectsDirectory(t *testing.T) {
	b, _, workDir, _, threadID := setupWorkspaceTestEnv(t)
	ctx := context.Background()

	if err := os.Mkdir(filepath.Join(workDir, "docs"), 0o755); err != nil {
		t.Fatalf("seed dir: %v", err)
	}

	_, err := b.FilesDuplicate(ctx, wire.FilesDuplicateParams{ThreadID: threadID, Path: "docs"})
	rpcErr := requireRPCError(t, err, wire.InvalidParams)
	if rpcErr.Message != "cannot duplicate a directory" {
		t.Fatalf("unexpected message: %q", rpcErr.Message)
	}
}

func TestFilesDuplicate_RejectsGitAndProtectedPaths(t *testing.T) {
	b, _, workDir, _, threadID := setupWorkspaceTestEnv(t)
	ctx := context.Background()

	if err := os.Mkdir(filepath.Join(workDir, ".git"), 0o755); err != nil {
		t.Fatalf("seed .git dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, ".git", "config"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed .git file: %v", err)
	}
	if _, err := b.FilesDuplicate(ctx, wire.FilesDuplicateParams{ThreadID: threadID, Path: ".git/config"}); err == nil {
		t.Fatalf("duplicating .git internals must be rejected")
	}

	if err := os.WriteFile(filepath.Join(workDir, ".env"), []byte("SECRET=1"), 0o644); err != nil {
		t.Fatalf("seed .env: %v", err)
	}
	if _, err := b.FilesDuplicate(ctx, wire.FilesDuplicateParams{ThreadID: threadID, Path: ".env"}); err == nil {
		t.Fatalf("duplicating a protected file must be rejected")
	}
}

func TestFilesTrash_MovesIntoTrashAndSuffixesCollision(t *testing.T) {
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

	if err := os.WriteFile(filepath.Join(workDir, "a.txt"), []byte("bye"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if _, err := b.FilesTrash(ctx, wire.FilesTrashParams{ThreadID: threadID, Path: "a.txt"}); err != nil {
		t.Fatalf("trash: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("source must be gone after trashing")
	}
	got, err := os.ReadFile(filepath.Join(trashRoot, "a.txt"))
	if err != nil || string(got) != "bye" {
		t.Fatalf("file must land in trash with content preserved, got %q err=%v", got, err)
	}

	// 同名条目已占位时，第二次移入要追加时间戳并保留原占位文件。
	if err := os.WriteFile(filepath.Join(workDir, "a.txt"), []byte("again"), 0o644); err != nil {
		t.Fatalf("seed second file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(trashRoot, "a 15.04.05.txt"), []byte("occupied"), 0o644); err != nil {
		t.Fatalf("seed colliding trash entry: %v", err)
	}
	if _, err := b.FilesTrash(ctx, wire.FilesTrashParams{ThreadID: threadID, Path: "a.txt"}); err != nil {
		t.Fatalf("trash second time: %v", err)
	}

	got, err = os.ReadFile(secondTrashFile(t, trashRoot))
	if err != nil || string(got) != "again" {
		t.Fatalf("expected sequence-suffixed trash entry, got %q err=%v", got, err)
	}
	got, err = os.ReadFile(filepath.Join(trashRoot, "a 15.04.05.txt"))
	if err != nil || string(got) != "occupied" {
		t.Fatalf("colliding entry must not be overwritten, got %q err=%v", got, err)
	}
	got, err = os.ReadFile(filepath.Join(trashRoot, "a.txt"))
	if err != nil || string(got) != "bye" {
		t.Fatalf("original trash entry must stay intact, got %q err=%v", got, err)
	}
}

func TestFilesTrash_RejectsWorkspaceRoot(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("trash is only supported on darwin")
	}
	b, _, _, _, threadID := setupWorkspaceTestEnv(t)
	ctx := context.Background()

	trashRoot := t.TempDir()
	original := fsutil.TrashDir
	fsutil.TrashDir = func() (string, error) { return trashRoot, nil }
	t.Cleanup(func() { fsutil.TrashDir = original })

	_, err := b.FilesTrash(ctx, wire.FilesTrashParams{ThreadID: threadID, Path: "."})
	rpcErr := requireRPCError(t, err, wire.InvalidParams)
	if rpcErr.Message != "cannot trash workspace root" {
		t.Fatalf("unexpected message: %q", rpcErr.Message)
	}
}

// files.create 的 RPC 级用例：确认方法已注册，并把参数校验接上。
func TestFilesCreate_RPC(t *testing.T) {
	_, d, workDir, _, threadID := setupWorkspaceTestEnv(t)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"files.create","params":{"threadID":"`+threadID+`","path":"rpc.txt","dir":false},"id":1}`)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var result wire.FilesCreateResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.Path != "rpc.txt" {
		t.Fatalf("unexpected result path: %q", result.Path)
	}
	if _, err := os.Stat(filepath.Join(workDir, "rpc.txt")); err != nil {
		t.Fatalf("file not created: %v", err)
	}

	badParams := []string{
		`{"threadID":"not-a-uuid","path":"x.txt"}`,
		`{"threadID":"` + threadID + `","path":""}`,
		`{"threadID":"` + threadID + `","path":"x.txt","unknown":1}`,
	}
	for i, params := range badParams {
		id := strconv.Itoa(i + 2)
		resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"files.create","params":`+params+`,"id":`+id+`}`)
		if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
			t.Fatalf("params %d must be rejected with InvalidParams, got %+v", i, resp.Error)
		}
	}
}

// files.rename / files.trash / files.duplicate 的方法注册与参数校验。
func TestFilesOps_RPCValidation(t *testing.T) {
	_, d, _, _, threadID := setupWorkspaceTestEnv(t)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	calls := []string{
		`{"jsonrpc":"2.0","method":"files.rename","params":{"threadID":"not-a-uuid","path":"a.txt","newPath":"b.txt"},"id":1}`,
		`{"jsonrpc":"2.0","method":"files.rename","params":{"threadID":"` + threadID + `","path":"a.txt"},"id":2}`,
		`{"jsonrpc":"2.0","method":"files.trash","params":{"threadID":"` + threadID + `","path":""},"id":3}`,
		`{"jsonrpc":"2.0","method":"files.duplicate","params":{"threadID":"` + threadID + `","path":"a.txt","extra":true},"id":4}`,
	}
	for _, raw := range calls {
		resp := callExtension(t, d, conn, raw)
		if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
			t.Fatalf("request must be rejected with InvalidParams: %s -> %+v", raw, resp.Error)
		}
	}
}

func secondTrashFile(t *testing.T, root string) string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 3 {
		t.Fatalf("unexpected trash entries: %v, %v", entries, err)
	}
	for _, entry := range entries {
		if entry.Name() != "a.txt" && entry.Name() != "a 15.04.05.txt" {
			return filepath.Join(root, entry.Name())
		}
	}
	t.Fatal("missing second trashed file")
	return ""
}
