package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"mcode/internal/remote/wire"
)

// projectCloneTestHome 把主目录指向临时目录，返回该目录；测试结束自动恢复。
func projectCloneTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	prev := projectExtHomeDirFunc
	projectExtHomeDirFunc = func() (string, error) { return home, nil }
	t.Cleanup(func() { projectExtHomeDirFunc = prev })
	return home
}

// projectCloneTestSetCloner 注入假的克隆执行器并返回捕获到的调用参数。
func projectCloneTestSetCloner(t *testing.T, backend *MemoryBackend, fn projectCloneRunner) *[]string {
	t.Helper()
	calls := &[]string{}
	backend.SetProjectCloner(func(ctx context.Context, url, dir string) error {
		*calls = append(*calls, url+" "+dir)
		return fn(ctx, url, dir)
	})
	return calls
}

// 地址与父目录的校验全部在转发前完成：非 https/git@ 地址、非法仓库名、相对或不存在父目录都返回 -32602，
// 且不会调用克隆执行器、不会新增项目。
func TestProjectExtCloneRejectsInvalidParams(t *testing.T) {
	home := projectCloneTestHome(t)
	backend := newProjectExtTestBackend(t)
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}
	calls := projectCloneTestSetCloner(t, backend, func(ctx context.Context, url, dir string) error { return nil })

	notDir := filepath.Join(home, "file.txt")
	if err := os.WriteFile(notDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	badName := "../evil"

	cases := []struct {
		name   string
		params wire.ProjectExtCloneParams
	}{
		{name: "empty url", params: wire.ProjectExtCloneParams{URL: "  "}},
		{name: "http url", params: wire.ProjectExtCloneParams{URL: "http://example.com/owner/repo.git"}},
		{name: "plain path", params: wire.ProjectExtCloneParams{URL: "/tmp/repo"}},
		{name: "git@ without colon", params: wire.ProjectExtCloneParams{URL: "git@example.com/owner/repo.git"}},
		{name: "whitespace in url", params: wire.ProjectExtCloneParams{URL: "https://example.com/a b.git"}},
		{name: "illegal name", params: wire.ProjectExtCloneParams{URL: "https://example.com/owner/repo.git", Name: &badName}},
		{name: "relative parent", params: wire.ProjectExtCloneParams{URL: "https://example.com/owner/repo.git", ParentPath: strPtr("relative/dir")}},
		{name: "missing parent", params: wire.ProjectExtCloneParams{URL: "https://example.com/owner/repo.git", ParentPath: strPtr(filepath.Join(home, "missing"))}},
		{name: "parent is file", params: wire.ProjectExtCloneParams{URL: "https://example.com/owner/repo.git", ParentPath: &notDir}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, _ := projectExtTestCall(t, d, conn, projectExtCloneMethod, tc.params)
			if resp.Error == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
			if resp.Error.Code != wire.InvalidParams {
				t.Fatalf("code = %d, want %d (%+v)", resp.Error.Code, wire.InvalidParams, resp.Error)
			}
		})
	}
	if len(*calls) != 0 {
		t.Fatalf("clone runner should not be called, got %v", *calls)
	}
	if count := projectExtTestCount(t, backend); count != 0 {
		t.Fatalf("project count = %d, want 0", count)
	}
}

// 目标目录已存在时返回参数错误，且不执行克隆。
func TestProjectExtCloneTargetExists(t *testing.T) {
	home := projectCloneTestHome(t)
	parent := filepath.Join(home, "work")
	target := filepath.Join(parent, "repo")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}

	backend := newProjectExtTestBackend(t)
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}
	calls := projectCloneTestSetCloner(t, backend, func(ctx context.Context, url, dir string) error { return nil })

	resp, _ := projectExtTestCall(t, d, conn, projectExtCloneMethod, wire.ProjectExtCloneParams{
		URL:        "https://example.com/owner/repo.git",
		ParentPath: &parent,
	})
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected invalid params error, got %+v", resp.Error)
	}
	if len(*calls) != 0 {
		t.Fatalf("clone runner should not be called, got %v", *calls)
	}
}

// 成功路径：克隆到 parentPath/<仓库名>，按 projects.add 同一路径登记项目并广播 projects.changed。
func TestProjectExtCloneSuccess(t *testing.T) {
	home := projectCloneTestHome(t)
	parent := filepath.Join(home, "code")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}

	backend := newProjectExtTestBackend(t)
	conn := &recordingConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}
	d := NewDispatcher(&fakeAuth{}, backend)
	d.addAuthedConn(conn)
	calls := projectCloneTestSetCloner(t, backend, func(ctx context.Context, url, dir string) error {
		return os.MkdirAll(dir, 0o755)
	})

	resp, raw := projectExtTestCall(t, d, conn, projectExtCloneMethod, wire.ProjectExtCloneParams{
		URL:        "https://github.com/example/demo.git",
		ParentPath: &parent,
	})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	var result wire.ProjectExtCloneResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	wantPath := filepath.Join(parent, "demo")
	if result.Project.Path != wantPath {
		t.Fatalf("path = %q, want %q", result.Project.Path, wantPath)
	}
	if result.Project.Name != "demo" {
		t.Fatalf("name = %q, want %q", result.Project.Name, "demo")
	}
	if result.Project.ID == "" {
		t.Fatal("expected a project ID")
	}
	if len(*calls) != 1 || (*calls)[0] != "https://github.com/example/demo.git "+wantPath {
		t.Fatalf("unexpected clone calls: %v", *calls)
	}
	if count := projectExtTestCount(t, backend); count != 1 {
		t.Fatalf("project count = %d, want 1", count)
	}
	if len(conn.notifications) != 1 || conn.notifications[0] != projectExtChangedMethod {
		t.Fatalf("unexpected notifications: %+v", conn.notifications)
	}
}

// parentPath 缺省时落到用户主目录（桌面端没有默认克隆目录）。
func TestProjectExtCloneDefaultsToHomeParent(t *testing.T) {
	home := projectCloneTestHome(t)
	backend := newProjectExtTestBackend(t)
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}
	calls := projectCloneTestSetCloner(t, backend, func(ctx context.Context, url, dir string) error {
		return os.MkdirAll(dir, 0o755)
	})

	resp, _ := projectExtTestCall(t, d, conn, projectExtCloneMethod, wire.ProjectExtCloneParams{
		URL: "git@github.com:owner/repo.git",
	})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	wantDir := filepath.Join(home, "repo")
	if len(*calls) != 1 || (*calls)[0] != "git@github.com:owner/repo.git "+wantDir {
		t.Fatalf("unexpected clone calls: %v", *calls)
	}
}

// 克隆失败返回 -32000，并清掉半成品目录，项目数不变。
func TestProjectExtCloneFailureCleansUp(t *testing.T) {
	home := projectCloneTestHome(t)
	parent := filepath.Join(home, "code")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}

	backend := newProjectExtTestBackend(t)
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}
	projectCloneTestSetCloner(t, backend, func(ctx context.Context, url, dir string) error {
		if err := os.MkdirAll(filepath.Join(dir, "partial"), 0o755); err != nil {
			return err
		}
		return errors.New("fatal: repository not found")
	})

	resp, _ := projectExtTestCall(t, d, conn, projectExtCloneMethod, wire.ProjectExtCloneParams{
		URL:        "https://example.com/owner/demo.git",
		ParentPath: &parent,
	})
	if resp.Error == nil || resp.Error.Code != wire.ErrCodeOperationRejected {
		t.Fatalf("expected operation rejected, got %+v", resp.Error)
	}
	if _, err := os.Stat(filepath.Join(parent, "demo")); !os.IsNotExist(err) {
		t.Fatalf("half-cloned dir should be removed, stat err = %v", err)
	}
	if count := projectExtTestCount(t, backend); count != 0 {
		t.Fatalf("project count = %d, want 0", count)
	}
}

// listDir 只列子目录：排除文件与隐藏目录，按名称排序，带 .git 的标记为 git 仓库。
func TestProjectExtListDirFiltersAndSorts(t *testing.T) {
	home := projectCloneTestHome(t)
	resolvedHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	for _, dir := range []string{"alpha", "Beta", ".hidden", "gamma"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(home, "gamma", ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	backend := newProjectExtTestBackend(t)
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp, raw := projectExtTestCall(t, d, conn, projectExtListDirMethod, wire.ProjectExtListDirParams{})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	var result wire.ProjectExtListDirResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.Path != resolvedHome {
		t.Fatalf("path = %q, want %q", result.Path, resolvedHome)
	}
	if result.Parent != nil {
		t.Fatalf("home has no parent inside home, got %q", *result.Parent)
	}
	if result.Truncated {
		t.Fatal("did not expect truncation")
	}
	wantNames := []string{"alpha", "Beta", "gamma"}
	if len(result.Entries) != len(wantNames) {
		t.Fatalf("entries = %+v, want %v", result.Entries, wantNames)
	}
	for i, want := range wantNames {
		if result.Entries[i].Name != want {
			t.Fatalf("entry %d name = %q, want %q", i, result.Entries[i].Name, want)
		}
		if result.Entries[i].Path != filepath.Join(resolvedHome, want) {
			t.Fatalf("entry %d path = %q", i, result.Entries[i].Path)
		}
		if got, wantRepo := result.Entries[i].IsGitRepo, want == "gamma"; got != wantRepo {
			t.Fatalf("entry %s isGitRepo = %v, want %v", want, got, wantRepo)
		}
	}

	// showHidden=true 时带上点目录。
	resp, raw = projectExtTestCall(t, d, conn, projectExtListDirMethod, wire.ProjectExtListDirParams{ShowHidden: true})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	names := make([]string, len(result.Entries))
	for i, entry := range result.Entries {
		names[i] = entry.Name
	}
	if len(names) != 4 || names[0] != ".hidden" {
		t.Fatalf("entries with hidden = %v", names)
	}

	// 子目录的 parent 指回主目录。
	sub := filepath.Join(home, "alpha")
	resp, raw = projectExtTestCall(t, d, conn, projectExtListDirMethod, wire.ProjectExtListDirParams{Path: &sub})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.Parent == nil || *result.Parent != resolvedHome {
		t.Fatalf("parent = %v, want %q", result.Parent, resolvedHome)
	}
}

// 越界、相对、不存在、符号链接指向主目录之外的路径一律 -32602。
func TestProjectExtListDirRejectsOutsideHome(t *testing.T) {
	home := projectCloneTestHome(t)
	sub := filepath.Join(home, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	link := filepath.Join(home, "escape")
	if err := os.Symlink("/etc", link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	backend := newProjectExtTestBackend(t)
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	outside := "/etc"
	relative := "sub"
	missing := filepath.Join(home, "missing")
	cases := []struct {
		name string
		path *string
	}{
		{name: "outside home", path: &outside},
		{name: "relative path", path: &relative},
		{name: "missing path", path: &missing},
		{name: "symlink escape", path: &link},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, _ := projectExtTestCall(t, d, conn, projectExtListDirMethod, wire.ProjectExtListDirParams{Path: tc.path})
			if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
				t.Fatalf("expected invalid params, got %+v", resp.Error)
			}
		})
	}
}

// 子目录超过 500 个时只返回前 500 项并置 truncated。
func TestProjectExtListDirTruncates(t *testing.T) {
	home := projectCloneTestHome(t)
	many := filepath.Join(home, "many")
	if err := os.MkdirAll(many, 0o755); err != nil {
		t.Fatalf("mkdir many: %v", err)
	}
	for i := 0; i < projectExtListDirLimit+5; i++ {
		if err := os.Mkdir(filepath.Join(many, fmt.Sprintf("dir-%03d", i)), 0o755); err != nil {
			t.Fatalf("mkdir dir-%03d: %v", i, err)
		}
	}

	backend := newProjectExtTestBackend(t)
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp, raw := projectExtTestCall(t, d, conn, projectExtListDirMethod, wire.ProjectExtListDirParams{Path: &many})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	var result wire.ProjectExtListDirResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(result.Entries) != projectExtListDirLimit {
		t.Fatalf("entries = %d, want %d", len(result.Entries), projectExtListDirLimit)
	}
	if !result.Truncated {
		t.Fatal("expected truncated")
	}
}
