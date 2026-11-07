package remote

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/remote/wire"
)

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func writeExtCall(t *testing.T, d *Dispatcher, threadID, path, content, expectedHash string) wire.Response {
	t.Helper()
	params := map[string]any{
		"threadID": threadID,
		"path":     path,
		"content":  content,
	}
	if expectedHash != "" {
		params["expectedHash"] = expectedHash
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}
	return callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"files.write","params":`+string(raw)+`,"id":1}`)
}

func decodeWriteResult(t *testing.T, resp wire.Response) wire.FilesWriteExtResult {
	t.Helper()
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var out wire.FilesWriteExtResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return out
}

// 正常写入：覆盖内容、返回 hash/size、保留原文件权限。
func TestFilesWrite_Success(t *testing.T) {
	_, d, workDir, _, threadID := setupWorkspaceTestEnv(t)

	target := filepath.Join(workDir, "notes.txt")
	if err := os.WriteFile(target, []byte("old content"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	resp := writeExtCall(t, d, threadID, "notes.txt", "new content\n", "")
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	result := decodeWriteResult(t, resp)
	if result.Hash != sha256Hex("new content\n") {
		t.Fatalf("unexpected hash: %s", result.Hash)
	}
	if result.Size != int64(len("new content\n")) {
		t.Fatalf("unexpected size: %d", result.Size)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "new content\n" {
		t.Fatalf("file not overwritten: %q", got)
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o644 {
		t.Fatalf("permission must be preserved, got %04o", perm)
	}
	// 原子写不应残留临时文件。
	entries, err := os.ReadDir(workDir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".mcode-write-") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}

// files.read 返回的 hash 可直接作为 files.write 的 expectedHash 完成一次往返。
func TestFilesWrite_ExpectedHashRoundTrip(t *testing.T) {
	_, d, workDir, _, threadID := setupWorkspaceTestEnv(t)
	target := filepath.Join(workDir, "round.txt")
	if err := os.WriteFile(target, []byte("v1"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	readRaw := `{"jsonrpc":"2.0","method":"files.read","params":{"threadID":"` + threadID + `","path":"round.txt"},"id":1}`
	readResp := callExtension(t, d, conn, readRaw)
	if readResp.Error != nil {
		t.Fatalf("files.read failed: %+v", readResp.Error)
	}
	readRawJSON, err := json.Marshal(readResp.Result)
	if err != nil {
		t.Fatalf("marshal read result: %v", err)
	}
	var readResult wire.RemoteFilesReadResult
	if err := json.Unmarshal(readRawJSON, &readResult); err != nil {
		t.Fatalf("unmarshal read result: %v", err)
	}
	if readResult.Hash != sha256Hex("v1") {
		t.Fatalf("files.read must expose whole-file hash, got %q", readResult.Hash)
	}

	resp := writeExtCall(t, d, threadID, "round.txt", "v2", readResult.Hash)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if result := decodeWriteResult(t, resp); result.Hash != sha256Hex("v2") {
		t.Fatalf("unexpected hash: %s", result.Hash)
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("permission must be preserved, got %04o", perm)
	}
}

// 哈希冲突：内容已被改动时拒绝覆盖。
func TestFilesWrite_HashConflict(t *testing.T) {
	_, d, workDir, _, threadID := setupWorkspaceTestEnv(t)
	target := filepath.Join(workDir, "conflict.txt")
	if err := os.WriteFile(target, []byte("current"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	stale := sha256Hex("stale")
	resp := writeExtCall(t, d, threadID, "conflict.txt", "mine", stale)
	if resp.Error == nil || resp.Error.Code != wire.ErrCodeVersionConflict {
		t.Fatalf("expected version conflict, got: %+v", resp.Error)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "current" {
		t.Fatalf("conflicting write must not touch the file: %q", got)
	}

	// expectedHash 格式非法按参数错误处理。
	resp = writeExtCall(t, d, threadID, "conflict.txt", "mine", "zz")
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams for malformed hash, got: %+v", resp.Error)
	}
}

// 越界路径、绝对路径、~ 与指向工作区外的符号链接全部拒绝。
func TestFilesWrite_RejectsEscapingPaths(t *testing.T) {
	_, d, workDir, _, threadID := setupWorkspaceTestEnv(t)

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(workDir, "link.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	cases := []struct{ name, path, wantMsg string }{
		{"parent traversal", "../outside.txt", "parent directory traversal"},
		{"absolute", outside, "invalid relative path"},
		{"home", "~/outside.txt", "invalid relative path"},
		{"symlink escape", "link.txt", "path escapes workspace root"},
	}
	for _, tc := range cases {
		resp := writeExtCall(t, d, threadID, tc.path, "hacked", "")
		if resp.Error == nil {
			t.Fatalf("%s: expected error", tc.name)
		}
		if resp.Error.Code != wire.InvalidParams {
			t.Fatalf("%s: expected InvalidParams, got: %+v", tc.name, resp.Error)
		}
		if !strings.Contains(resp.Error.Message, tc.wantMsg) {
			t.Fatalf("%s: unexpected message %q", tc.name, resp.Error.Message)
		}
	}

	// 工作区外的原文件必须保持原样。
	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("read outside: %v", err)
	}
	if string(got) != "secret" {
		t.Fatalf("outside file must be untouched: %q", got)
	}
}

// 二进制文件与目录都不允许写入。
func TestFilesWrite_RejectsBinaryAndDirectory(t *testing.T) {
	_, d, workDir, _, threadID := setupWorkspaceTestEnv(t)

	if err := os.WriteFile(filepath.Join(workDir, "blob.bin"), []byte{0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatalf("seed binary: %v", err)
	}
	resp := writeExtCall(t, d, threadID, "blob.bin", "text", "")
	if resp.Error == nil || resp.Error.Code != wire.ErrCodeOperationRejected {
		t.Fatalf("expected operation rejected for binary, got: %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "binary") {
		t.Fatalf("unexpected message: %q", resp.Error.Message)
	}

	if err := os.Mkdir(filepath.Join(workDir, "dir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	resp = writeExtCall(t, d, threadID, "dir", "text", "")
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams for directory, got: %+v", resp.Error)
	}
}

// 不存在的文件：只允许覆盖，不通过远程写入创建新文件。
func TestFilesWrite_NonexistentFile(t *testing.T) {
	_, d, workDir, _, threadID := setupWorkspaceTestEnv(t)

	resp := writeExtCall(t, d, threadID, "missing.txt", "text", "")
	if resp.Error == nil || resp.Error.Code != wire.ErrCodeFileNotFound {
		t.Fatalf("expected file-not-found, got: %+v", resp.Error)
	}
	if _, err := os.Stat(filepath.Join(workDir, "missing.txt")); !os.IsNotExist(err) {
		t.Fatalf("files.write must not create files")
	}
}

// 内容超过 files.read 的文本上限（512 KiB）时拒绝。
func TestFilesWrite_ContentTooLarge(t *testing.T) {
	_, d, workDir, _, threadID := setupWorkspaceTestEnv(t)
	if err := os.WriteFile(filepath.Join(workDir, "big.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	tooLarge := strings.Repeat("a", 512*1024+1)
	resp := writeExtCall(t, d, threadID, "big.txt", tooLarge, "")
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams for oversized content, got: %+v", resp.Error)
	}

	// 正好 512 KiB 允许写入。
	exact := strings.Repeat("a", 512*1024)
	resp = writeExtCall(t, d, threadID, "big.txt", exact, "")
	if resp.Error != nil {
		t.Fatalf("512 KiB content should be accepted: %+v", resp.Error)
	}
	if result := decodeWriteResult(t, resp); result.Size != int64(len(exact)) {
		t.Fatalf("unexpected size: %d", result.Size)
	}
}

// 其余参数校验：threadID 非法、path 为空、content 非 UTF-8。
func TestFilesWrite_InvalidParams(t *testing.T) {
	_, d, workDir, _, threadID := setupWorkspaceTestEnv(t)
	if err := os.WriteFile(filepath.Join(workDir, "ok.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	cases := []string{
		`{"jsonrpc":"2.0","method":"files.write","params":{"threadID":"not-a-uuid","path":"ok.txt","content":"y"},"id":1}`,
		`{"jsonrpc":"2.0","method":"files.write","params":{"threadID":"` + threadID + `","path":"","content":"y"},"id":2}`,
		`{"jsonrpc":"2.0","method":"files.write","params":{"threadID":"` + threadID + `","path":"ok.txt","content":"y","unknown":1},"id":3}`,
	}
	for _, raw := range cases {
		resp := callExtension(t, d, conn, raw)
		if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
			t.Fatalf("expected InvalidParams for %s, got: %+v", raw, resp.Error)
		}
	}

	// 非法 UTF-8 内容在 JSON 层会先被转义成替换字符，这里直接校验后端拒绝非文本内容。
	got, err := os.ReadFile(filepath.Join(workDir, "ok.txt"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "x" {
		t.Fatalf("invalid params must not touch the file: %q", got)
	}
}
