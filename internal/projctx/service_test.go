package projctx

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestService_New(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("expected error for empty path")
	}
	if _, err := New("   "); err == nil {
		t.Fatal("expected error for whitespace path")
	}
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s == nil {
		t.Fatal("expected non-nil service")
	}
}

func TestService_Ensure(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	if err := s.Ensure(); err != nil {
		t.Fatalf("Ensure failed: %v", err)
	}

	fi, err := os.Stat(s.DocsPath())
	if err != nil || !fi.IsDir() {
		t.Fatalf("docs directory was not created: %v", err)
	}
	fi, err = os.Stat(s.InboxPath())
	if err != nil || !fi.IsDir() {
		t.Fatalf("inbox directory was not created: %v", err)
	}
}

func TestService_WriteAndRead(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	content := "# Heading\nHello world!"
	doc, err := s.Write("test.md", content, "")
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if doc.Name != "test.md" {
		t.Fatalf("expected Name test.md, got %q", doc.Name)
	}
	if doc.Content != content {
		t.Fatalf("expected Content %q, got %q", content, doc.Content)
	}
	expectedHash := HashContent(content)
	if doc.Hash != expectedHash {
		t.Fatalf("expected Hash %q, got %q", expectedHash, doc.Hash)
	}

	readDoc, err := s.Read("test.md")
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if readDoc.Name != doc.Name || readDoc.Content != doc.Content || readDoc.Hash != doc.Hash {
		t.Fatalf("Read mismatch: got %+v, want %+v", readDoc, doc)
	}

	// 读取不存在的文件
	if _, err := s.Read("missing.md"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing file, got %v", err)
	}
}

func TestService_WriteCAS(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// 初始写入
	d1, err := s.Write("cas.md", "version 1", "")
	if err != nil {
		t.Fatalf("initial write failed: %v", err)
	}

	// 正确的 expectedHash 更新
	d2, err := s.Write("cas.md", "version 2", d1.Hash)
	if err != nil {
		t.Fatalf("CAS write failed: %v", err)
	}
	if d2.Content != "version 2" {
		t.Fatalf("expected version 2, got %q", d2.Content)
	}

	// 使用旧哈希再次写入，必须返回 ErrConflict，且文件内容不变
	_, err = s.Write("cas.md", "version 3", d1.Hash)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	readDoc, err := s.Read("cas.md")
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if readDoc.Content != "version 2" {
		t.Fatalf("content should remain version 2, got %q", readDoc.Content)
	}

	// 写入相同内容（ghost conflict），即使 expectedHash 错误也成功
	dGhost, err := s.Write("cas.md", "version 2", "wrong-hash")
	if err != nil {
		t.Fatalf("ghost conflict write should succeed, got: %v", err)
	}
	if dGhost.Content != "version 2" {
		t.Fatalf("expected version 2, got %q", dGhost.Content)
	}

	// 文件不存在且 expectedHash 非空，返回冲突
	_, err = s.Write("newdoc.md", "content", "some-hash")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for nonexistent file with hash, got %v", err)
	}
}

func TestService_List(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// 目录尚不存在时返回空切片
	list, err := s.List()
	if err != nil {
		t.Fatalf("List on non-existent dir failed: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty slice, got %d items", len(list))
	}

	// 写入三个文档，乱序
	if _, err := s.Write("c.md", "content c", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write("a.md", "content a", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write("b.md", "content b", ""); err != nil {
		t.Fatal(err)
	}

	// 放置非 .md 文件、子目录和符号链接，需被忽略
	if err := os.WriteFile(filepath.Join(s.DocsPath(), "ignored.txt"), []byte("txt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(s.DocsPath(), "subdir.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(s.DocsPath(), "a.md"), filepath.Join(s.DocsPath(), "link.md")); err != nil {
		t.Fatal(err)
	}

	docs, err := s.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(docs) != 3 {
		t.Fatalf("expected 3 docs, got %d", len(docs))
	}
	expectedOrder := []string{"a.md", "b.md", "c.md"}
	for i, want := range expectedOrder {
		if docs[i].Name != want {
			t.Fatalf("doc %d: expected name %q, got %q", i, want, docs[i].Name)
		}
		if docs[i].Content == "" || docs[i].Hash == "" {
			t.Fatalf("doc %d (%s) missing Content or Hash", i, want)
		}
	}
}

func TestService_ListRefusesSymlinkedDocs(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Ensure(); err != nil {
		t.Fatal(err)
	}

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "leak.md"), []byte("outside secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(s.DocsPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, s.DocsPath()); err != nil {
		t.Fatal(err)
	}

	docs, err := s.List()
	if err != nil {
		t.Fatalf("List with symlinked docs failed: %v", err)
	}
	if len(docs) != 0 {
		t.Fatalf("expected no docs for symlinked docs dir, got %d (outside content leaked)", len(docs))
	}
}

func TestService_PathTraversalAndBoundaries(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	invalidNames := []string{"../x.md", "a/b.md", "..", ".hidden.md", "x.txt"}
	for _, name := range invalidNames {
		if _, err := s.Read(name); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("Read(%q) expected ErrInvalidName, got %v", name, err)
		}
		if _, err := s.Write(name, "hello", ""); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("Write(%q) expected ErrInvalidName, got %v", name, err)
		}
	}

	// 把 docs 目录替换为指向 TempDir 外部目录的符号链接
	outsideDir := t.TempDir()
	if err := s.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.DocsPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, s.DocsPath()); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Write("escape.md", "payload", ""); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("expected ErrInvalidName when docs is symlink to outside, got %v", err)
	}
	outsideEntries, err := os.ReadDir(outsideDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(outsideEntries) != 0 {
		t.Fatalf("expected no files created in outside dir, found %d", len(outsideEntries))
	}

	// 恢复正常的 docs 目录，并在 docs 下放一个指向外部文件的符号链接 evil.md
	if err := os.Remove(s.DocsPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.DocsPath(), 0o755); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outsideDir, "secret.md")
	if err := os.WriteFile(outsideFile, []byte("sensitive"), 0o644); err != nil {
		t.Fatal(err)
	}
	evilLink := filepath.Join(s.DocsPath(), "evil.md")
	if err := os.Symlink(outsideFile, evilLink); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Read("evil.md"); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("Read(evil.md) expected ErrInvalidName, got %v", err)
	}
	if _, err := s.Write("evil.md", "overwritten", ""); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("Write(evil.md) expected ErrInvalidName, got %v", err)
	}

	// 把 .mcode 本身做成符号链接
	projectWithLinkedMcode := t.TempDir()
	externalMcode := filepath.Join(t.TempDir(), "external-mcode")
	if err := os.MkdirAll(externalMcode, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalMcode, filepath.Join(projectWithLinkedMcode, ".mcode")); err != nil {
		t.Fatal(err)
	}
	sLinked, err := New(projectWithLinkedMcode)
	if err != nil {
		t.Fatal(err)
	}
	if err := sLinked.Ensure(); !errors.Is(err, ErrRedirected) {
		t.Fatalf("Ensure with symlinked .mcode expected ErrRedirected, got %v", err)
	}
	if _, err := sLinked.Write("a.md", "content", ""); !errors.Is(err, ErrRedirected) {
		t.Fatalf("Write with symlinked .mcode expected ErrRedirected, got %v", err)
	}
}

func TestService_WorktreeResolvesToMainCheckoutAndExcludesContext(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git 可执行文件未找到，跳过测试")
	}

	mainDir := t.TempDir()

	// 1. git init
	cmd := exec.Command(gitBin, "init")
	cmd.Dir = mainDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init 失败: %v\n%s", err, out)
	}

	// 配置隔离用户信息
	cmd = exec.Command(gitBin, "config", "user.name", "test-user")
	cmd.Dir = mainDir
	_ = cmd.Run()
	cmd = exec.Command(gitBin, "config", "user.email", "test@localhost")
	cmd.Dir = mainDir
	_ = cmd.Run()

	// 2. 提交一次
	seedPath := filepath.Join(mainDir, "seed.txt")
	if err := os.WriteFile(seedPath, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write seed.txt 失败: %v", err)
	}
	cmd = exec.Command(gitBin, "add", "seed.txt")
	cmd.Dir = mainDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add 失败: %v\n%s", err, out)
	}
	cmd = exec.Command(gitBin, "commit", "-m", "initial commit")
	cmd.Dir = mainDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit 失败: %v\n%s", err, out)
	}

	// 3. git worktree add
	wtDir := filepath.Join(t.TempDir(), "wt")
	cmd = exec.Command(gitBin, "worktree", "add", wtDir)
	cmd.Dir = mainDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add 失败: %v\n%s", err, out)
	}

	// 4. 断言 New(worktreePath) 的根目录或 ContextDir 指向主仓库
	sWorktree, err := New(wtDir)
	if err != nil {
		t.Fatalf("New(wtDir) 失败: %v", err)
	}
	sMain, err := New(mainDir)
	if err != nil {
		t.Fatalf("New(mainDir) 失败: %v", err)
	}

	wtRoot, err := filepath.EvalSymlinks(sWorktree.root)
	if err != nil {
		t.Fatalf("EvalSymlinks(sWorktree.root) 失败: %v", err)
	}
	mainRoot, err := filepath.EvalSymlinks(sMain.root)
	if err != nil {
		t.Fatalf("EvalSymlinks(sMain.root) 失败: %v", err)
	}
	if wtRoot != mainRoot {
		t.Errorf("worktree service.root = %q, want %q", wtRoot, mainRoot)
	}

	wantWtCtx := filepath.Join(sWorktree.root, ".mcode", "context")
	if got := sWorktree.ContextDir(); got != wantWtCtx {
		t.Errorf("worktree service.ContextDir() = %q, want %q", got, wantWtCtx)
	}
	wantMainCtx := filepath.Join(sMain.root, ".mcode", "context")
	if got := sMain.ContextDir(); got != wantMainCtx {
		t.Errorf("main service.ContextDir() = %q, want %q", got, wantMainCtx)
	}

	// 5. 执行 Ensure 创建目录骨架
	if err := sWorktree.Ensure(); err != nil {
		t.Fatalf("Ensure 失败: %v", err)
	}

	// 6. 断言 exclude 文件中写入了 /.mcode/context/
	excludeFile := filepath.Join(sMain.root, ".git", "info", "exclude")
	excludeData, err := os.ReadFile(excludeFile)
	if err != nil {
		t.Fatalf("读取 exclude 文件失败: %v", err)
	}
	if !strings.Contains(string(excludeData), "/.mcode/context/") {
		t.Errorf("exclude 文件内容 %q 未包含 /.mcode/context/", string(excludeData))
	}
}
