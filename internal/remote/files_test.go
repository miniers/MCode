package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"mcode/internal/git"
	"mcode/internal/remote/wire"
)

// setupWorkspaceTestEnv 辅助函数：创建测试专用的项目与会话，并绑定到真实临时目录。
func setupWorkspaceTestEnv(t *testing.T) (*MemoryBackend, *Dispatcher, string, string, string) {
	t.Helper()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	t.Cleanup(func() { hub.Close() })

	b := NewMemoryBackend(hub)
	t.Cleanup(func() { b.Close() })

	workDir := t.TempDir()
	evalWorkDir, err := filepath.EvalSymlinks(workDir)
	if err == nil {
		workDir = evalWorkDir
	}

	projectID := uuid.New().String()
	threadID := uuid.New().String()

	b.AddProject(wire.ProjectSummary{
		ID:   projectID,
		Name: "Test Project",
		Path: workDir,
	})

	b.AddThread(wire.ThreadSummary{
		ID:        threadID,
		ProjectID: projectID,
		Title:     "Test Thread",
	}, nil)

	auth := &fakeAuth{}
	d := NewDispatcher(auth, b)

	return b, d, workDir, projectID, threadID
}

// 1. files.list 成功用例、目录在前排序及 1000 项截断
func TestFilesList_SuccessAndTruncation(t *testing.T) {
	ctx := context.Background()
	b, d, workDir, _, threadID := setupWorkspaceTestEnv(t)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	// 创建目录与文件：b_dir, a_dir, z_file.txt, a_file.txt
	if err := os.MkdirAll(filepath.Join(workDir, "b_dir"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workDir, "a_dir"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "z_file.txt"), []byte("z"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "a_file.txt"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}

	// 通过 Dispatcher.Handle 调用 files.list
	req := fmt.Sprintf(`{"jsonrpc":"2.0","method":"files.list","params":{"threadID":"%s"},"id":1}`, threadID)
	respRaw := d.Handle(ctx, conn, []byte(req))
	var resp struct {
		Result wire.RemoteFilesListResult `json:"result"`
		Error  *wire.RPCError             `json:"error"`
	}
	if err := json.Unmarshal(respRaw, &resp); err != nil {
		t.Fatalf("unmarshal error: %v, raw: %s", err, string(respRaw))
	}
	if resp.Error != nil {
		t.Fatalf("unexpected rpc error: %+v", resp.Error)
	}
	if resp.Result.Truncated {
		t.Errorf("expected not truncated")
	}

	// 验证排序：目录在前且名称不区分大小写
	entries := resp.Result.Entries
	if len(entries) < 4 {
		t.Fatalf("expected at least 4 entries, got %d", len(entries))
	}
	if entries[0].Kind != wire.RemoteFileEntryKindDirectory || entries[0].Name != "a_dir" {
		t.Errorf("expected first entry to be a_dir, got %s", entries[0].Name)
	}
	if entries[1].Kind != wire.RemoteFileEntryKindDirectory || entries[1].Name != "b_dir" {
		t.Errorf("expected second entry to be b_dir, got %s", entries[1].Name)
	}
	if entries[2].Kind != wire.RemoteFileEntryKindFile || entries[2].Name != "a_file.txt" {
		t.Errorf("expected third entry to be a_file.txt, got %s", entries[2].Name)
	}
	if entries[3].Kind != wire.RemoteFileEntryKindFile || entries[3].Name != "z_file.txt" {
		t.Errorf("expected fourth entry to be z_file.txt, got %s", entries[3].Name)
	}

	// 1000 项截断测试
	truncDir := filepath.Join(workDir, "big_dir")
	if err := os.MkdirAll(truncDir, 0755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1050; i++ {
		fn := filepath.Join(truncDir, fmt.Sprintf("file_%04d.txt", i))
		_ = os.WriteFile(fn, []byte("x"), 0644)
	}

	listRes, err := b.FilesList(ctx, wire.RemoteFilesListParams{
		ThreadID: threadID,
		Subpath:  "big_dir",
	})
	if err != nil {
		t.Fatalf("FilesList error: %v", err)
	}
	if !listRes.Truncated {
		t.Errorf("expected truncated=true for > 1000 items")
	}
	if len(listRes.Entries) != 1000 {
		t.Errorf("expected exactly 1000 entries, got %d", len(listRes.Entries))
	}
}

// 2. files.read 文本 512KB 分块、图片 256KB 分块、图片超 8MB 报错及 UTF-8 边界截断回退
func TestFilesRead_TextAndImageBounds(t *testing.T) {
	ctx := context.Background()
	b, _, workDir, _, threadID := setupWorkspaceTestEnv(t)

	// 文本文件：600KB，验证默认单块 512KB 限制与截断标志
	text600K := strings.Repeat("A", 600*1024)
	textPath := filepath.Join(workDir, "large.txt")
	if err := os.WriteFile(textPath, []byte(text600K), 0644); err != nil {
		t.Fatal(err)
	}

	readRes, err := b.FilesRead(ctx, wire.RemoteFilesReadParams{
		ThreadID: threadID,
		Path:     "large.txt",
	})
	if err != nil {
		t.Fatalf("FilesRead text failed: %v", err)
	}
	if readRes.Kind != wire.RemoteFileContentKindText {
		t.Errorf("expected kind=text, got %s", readRes.Kind)
	}
	if readRes.Text == nil || len(*readRes.Text) != 512*1024 {
		t.Errorf("expected text length 512KiB, got %d", len(*readRes.Text))
	}
	if !readRes.Truncated || readRes.NextOffset == nil || *readRes.NextOffset != 512*1024 {
		t.Errorf("expected truncated with NextOffset=524288, got truncated=%v, nextOffset=%v", readRes.Truncated, readRes.NextOffset)
	}

	// UTF-8 边界回退测试：在边界放置一个 3 字节中文字符并限制读取长度使其被切断
	chineseStr := "hello世界你好"
	utf8File := filepath.Join(workDir, "utf8.txt")
	if err := os.WriteFile(utf8File, []byte(chineseStr), 0644); err != nil {
		t.Fatal(err)
	}
	// "hello" 是 5 字节，"世" 是 3 字节（6-8 字节）。如果指定 length=6，则截断了 "世" 的首字节，应回退到 5 字节
	sixLen := 6
	utf8Res, err := b.FilesRead(ctx, wire.RemoteFilesReadParams{
		ThreadID: threadID,
		Path:     "utf8.txt",
		Length:   &sixLen,
	})
	if err != nil {
		t.Fatalf("utf8 read failed: %v", err)
	}
	if utf8Res.Text == nil || *utf8Res.Text != "hello" {
		t.Errorf("expected cleanly truncated utf8 text 'hello', got %q", *utf8Res.Text)
	}
	if utf8Res.NextOffset == nil || *utf8Res.NextOffset != 5 {
		t.Errorf("expected nextOffset 5, got %v", utf8Res.NextOffset)
	}

	// 图片文件：300KB JPEG，验证默认单块 256KB 限制
	jpegHeader := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00}
	jpegData := append(jpegHeader, bytes.Repeat([]byte{0x55}, 300*1024-len(jpegHeader))...)
	jpegPath := filepath.Join(workDir, "photo.jpg")
	if err := os.WriteFile(jpegPath, jpegData, 0644); err != nil {
		t.Fatal(err)
	}

	imgRes, err := b.FilesRead(ctx, wire.RemoteFilesReadParams{
		ThreadID: threadID,
		Path:     "photo.jpg",
	})
	if err != nil {
		t.Fatalf("FilesRead image failed: %v", err)
	}
	if imgRes.Kind != wire.RemoteFileContentKindImage {
		t.Errorf("expected kind=image, got %s", imgRes.Kind)
	}
	if !imgRes.Truncated || imgRes.NextOffset == nil || *imgRes.NextOffset != 256*1024 {
		t.Errorf("expected image truncated at 256KiB, got nextOffset=%v", imgRes.NextOffset)
	}
	if imgRes.Base64 == nil || *imgRes.Base64 == "" {
		t.Errorf("expected base64 encoded image chunk")
	}

	// 图片超 8MB 报错测试
	hugeImgPath := filepath.Join(workDir, "huge.jpg")
	hugeImgFile, err := os.Create(hugeImgPath)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = hugeImgFile.Write(jpegHeader)
	// 扩展到 9MB
	_ = hugeImgFile.Truncate(9 * 1024 * 1024)
	hugeImgFile.Close()

	_, err = b.FilesRead(ctx, wire.RemoteFilesReadParams{
		ThreadID: threadID,
		Path:     "huge.jpg",
	})
	if err == nil {
		t.Fatal("expected error for image > 8MB, got nil")
	}
	rpcErr, ok := err.(*wire.RPCError)
	if !ok || rpcErr.Code != wire.ErrCodeMediaTooLarge {
		t.Errorf("expected ErrCodeMediaTooLarge (%d), got: %v", wire.ErrCodeMediaTooLarge, err)
	}
}

// 3. 路径穿越与符号链接防护：../x、绝对路径、越界符号链接均被拒绝
func TestFilesPathSecurity(t *testing.T) {
	ctx := context.Background()
	b, _, workDir, _, threadID := setupWorkspaceTestEnv(t)

	outsideDir := t.TempDir()
	outsideSecret := filepath.Join(outsideDir, "secret.txt")
	_ = os.WriteFile(outsideSecret, []byte("super_secret"), 0644)

	// 在工作区创建指向外部的符号链接
	symlinkEscape := filepath.Join(workDir, "escape_link")
	_ = os.Symlink(outsideDir, symlinkEscape)

	// 1. "../x" 越界访问尝试
	_, err := b.FilesRead(ctx, wire.RemoteFilesReadParams{
		ThreadID: threadID,
		Path:     "../outside.txt",
	})
	if err == nil {
		t.Errorf("expected error for ../ traversal")
	}
	if rpcErr, ok := err.(*wire.RPCError); !ok || rpcErr.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams for parent traversal, got: %v", err)
	}

	// 2. 绝对路径尝试
	_, err = b.FilesRead(ctx, wire.RemoteFilesReadParams{
		ThreadID: threadID,
		Path:     "/etc/passwd",
	})
	if err == nil {
		t.Errorf("expected error for absolute path")
	}
	if rpcErr, ok := err.(*wire.RPCError); !ok || rpcErr.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams for absolute path, got: %v", err)
	}

	// 3. 越界符号链接尝试
	_, err = b.FilesRead(ctx, wire.RemoteFilesReadParams{
		ThreadID: threadID,
		Path:     "escape_link/secret.txt",
	})
	if err == nil {
		t.Errorf("expected error for out-of-bounds symlink escape")
	}
	if rpcErr, ok := err.(*wire.RPCError); !ok || rpcErr.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams for symlink escape, got: %v", err)
	}

	// 4. files.list 传入 "../"、绝对路径与越界符号链接同样被拒绝
	for _, sub := range []string{"../", "/etc", "escape_link"} {
		if _, err := b.FilesList(ctx, wire.RemoteFilesListParams{ThreadID: threadID, Subpath: sub}); err == nil {
			t.Errorf("expected error for files.list subpath %q", sub)
		} else if rpcErr, ok := err.(*wire.RPCError); !ok || rpcErr.Code != wire.InvalidParams {
			t.Errorf("expected InvalidParams for files.list subpath %q, got: %v", sub, err)
		}
	}
}

// 4. files.search 成功用例、200 条截断与超长 query 报错
func TestFilesSearch_SuccessAndBounds(t *testing.T) {
	ctx := context.Background()
	b, _, workDir, _, threadID := setupWorkspaceTestEnv(t)

	// 成功匹配用例
	_ = os.WriteFile(filepath.Join(workDir, "controller_test.go"), []byte("ok"), 0644)
	_ = os.WriteFile(filepath.Join(workDir, "user_model.go"), []byte("ok"), 0644)

	searchRes, err := b.FilesSearch(ctx, wire.RemoteFilesSearchParams{
		ThreadID: threadID,
		Query:    "ctrl", // 模糊子序列匹配 controller_test.go
	})
	if err != nil {
		t.Fatalf("FilesSearch failed: %v", err)
	}
	if len(searchRes.Entries) != 1 || searchRes.Entries[0].Name != "controller_test.go" {
		t.Errorf("expected to match controller_test.go, got %+v", searchRes.Entries)
	}

	// query 超过 256 字符报错
	longQuery := strings.Repeat("q", 257)
	_, err = b.FilesSearch(ctx, wire.RemoteFilesSearchParams{
		ThreadID: threadID,
		Query:    longQuery,
	})
	if err == nil {
		t.Fatal("expected error for query > 256 chars")
	}
	if rpcErr, ok := err.(*wire.RPCError); !ok || rpcErr.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams for long query, got: %v", err)
	}

	// 空白 query 返回空结果而不是报错
	blankRes, err := b.FilesSearch(ctx, wire.RemoteFilesSearchParams{
		ThreadID: threadID,
		Query:    "   ",
	})
	if err != nil {
		t.Fatalf("blank query should not error: %v", err)
	}
	if len(blankRes.Entries) != 0 || blankRes.Truncated {
		t.Errorf("expected empty result for blank query, got %+v", blankRes)
	}

	// 200 条匹配结果截断测试
	searchDir := filepath.Join(workDir, "search_many")
	_ = os.MkdirAll(searchDir, 0755)
	for i := 0; i < 220; i++ {
		fn := filepath.Join(searchDir, fmt.Sprintf("target_match_%03d.txt", i))
		_ = os.WriteFile(fn, []byte("x"), 0644)
	}

	manyRes, err := b.FilesSearch(ctx, wire.RemoteFilesSearchParams{
		ThreadID: threadID,
		Query:    "target_match",
	})
	if err != nil {
		t.Fatalf("search many failed: %v", err)
	}
	if !manyRes.Truncated {
		t.Errorf("expected search truncated=true for > 200 matches")
	}
	if len(manyRes.Entries) != 200 {
		t.Errorf("expected exactly 200 entries, got %d", len(manyRes.Entries))
	}
}

// 5. git.status 与 git.branches 在非仓库与真实仓库两种情况下的表现
func TestGitStatusAndBranches(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed on system, skipping git tests")
	}

	ctx := context.Background()
	b, _, workDir, _, threadID := setupWorkspaceTestEnv(t)

	// 1. 非仓库情况测试
	statusRes, err := b.GitStatus(ctx, wire.RemoteGitStatusParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("GitStatus non-repo failed: %v", err)
	}
	if statusRes.IsRepository {
		t.Errorf("expected isRepository=false on non-git dir")
	}

	branchesRes, err := b.GitBranches(ctx, wire.RemoteGitBranchesParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("GitBranches non-repo failed: %v", err)
	}
	if branchesRes.IsRepository {
		t.Errorf("expected isRepository=false on non-git dir")
	}

	// 2. 初始化为真实仓库
	b.SetGitRunner(git.NewRunner("git", 4))

	cmd := exec.CommandContext(ctx, "git", "init")
	cmd.Dir = workDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init failed: %v", err)
	}

	// 配置 git user 保证 commit 成功
	_ = exec.CommandContext(ctx, "git", "-C", workDir, "config", "user.name", "Tester").Run()
	_ = exec.CommandContext(ctx, "git", "-C", workDir, "config", "user.email", "test@example.com").Run()

	// 写入文件并提交
	readmePath := filepath.Join(workDir, "README.md")
	_ = os.WriteFile(readmePath, []byte("# Hello Git"), 0644)
	_ = exec.CommandContext(ctx, "git", "-C", workDir, "add", "README.md").Run()
	_ = exec.CommandContext(ctx, "git", "-C", workDir, "commit", "-m", "initial commit").Run()

	// 创建未跟踪文件与修改文件
	_ = os.WriteFile(filepath.Join(workDir, "untracked.txt"), []byte("untracked"), 0644)
	_ = os.WriteFile(readmePath, []byte("# Hello Git (Modified)"), 0644)

	// 创建额外本地分支
	_ = exec.CommandContext(ctx, "git", "-C", workDir, "branch", "feature-test").Run()

	// 查询 git.status
	repoStatus, err := b.GitStatus(ctx, wire.RemoteGitStatusParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("GitStatus repo failed: %v", err)
	}
	if !repoStatus.IsRepository {
		t.Errorf("expected isRepository=true")
	}
	if repoStatus.Branch == nil || *repoStatus.Branch == "" {
		t.Errorf("expected non-empty branch, got %v", repoStatus.Branch)
	}
	if len(repoStatus.ModifiedFiles) != 1 || repoStatus.ModifiedFiles[0] != "README.md" {
		t.Errorf("expected modified README.md, got %+v", repoStatus.ModifiedFiles)
	}
	if len(repoStatus.UntrackedFiles) != 1 || repoStatus.UntrackedFiles[0] != "untracked.txt" {
		t.Errorf("expected untracked untracked.txt, got %+v", repoStatus.UntrackedFiles)
	}
	if repoStatus.IsClean {
		t.Errorf("expected isClean=false")
	}

	// 查询 git.branches
	repoBranches, err := b.GitBranches(ctx, wire.RemoteGitBranchesParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("GitBranches repo failed: %v", err)
	}
	if !repoBranches.IsRepository {
		t.Errorf("expected isRepository=true")
	}
	if repoBranches.Current == nil || *repoBranches.Current == "" {
		t.Errorf("expected non-empty current branch, got %v", repoBranches.Current)
	}
	foundFeature := false
	for _, br := range repoBranches.Local {
		if br == "feature-test" {
			foundFeature = true
			break
		}
	}
	if !foundFeature {
		t.Errorf("expected to find feature-test branch in local list: %+v", repoBranches.Local)
	}
}
