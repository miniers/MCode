package remote

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/remote/wire"
)

// newProjectExtTestBackend 造一个空的 MemoryBackend：hub 定时器放大到形同关闭，
// 数据目录指向临时目录，避免测试写到真实 profile。
func newProjectExtTestBackend(t *testing.T) *MemoryBackend {
	t.Helper()
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	t.Cleanup(hub.Close)
	backend := NewMemoryBackend(hub)
	t.Cleanup(backend.Close)
	backend.SetDataDir(t.TempDir())
	return backend
}

// projectExtTestCall 发一条扩展方法请求并返回解码后的响应。
func projectExtTestCall(t *testing.T, d *Dispatcher, conn Conn, method string, params any) (wire.Response, json.RawMessage) {
	t.Helper()
	rawParams, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  json.RawMessage(rawParams),
		"id":      1,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp := callExtension(t, d, conn, string(req))
	if resp.Result == nil {
		return resp, nil
	}
	rawResult, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	return resp, rawResult
}

// projectExtTestAdd 调 projects.add 并返回解码后的结果。
func projectExtTestAdd(t *testing.T, d *Dispatcher, conn Conn, path string) wire.ProjectExtAddResult {
	t.Helper()
	resp, raw := projectExtTestCall(t, d, conn, projectExtAddMethod, wire.ProjectExtAddParams{Path: path})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	var result wire.ProjectExtAddResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return result
}

// projectExtTestValidate 调 projects.validatePath 并返回解码后的结果。
func projectExtTestValidate(t *testing.T, d *Dispatcher, conn Conn, path string) wire.ProjectExtValidateResult {
	t.Helper()
	resp, raw := projectExtTestCall(t, d, conn, projectExtValidatePathMethod, wire.ProjectExtValidateParams{Path: path})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	var result wire.ProjectExtValidateResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return result
}

// projectExtTestCount 返回后端当前项目数。
func projectExtTestCount(t *testing.T, backend *MemoryBackend) int {
	t.Helper()
	list, err := backend.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	return len(list.Projects)
}

func TestProjectExtAdd_Success(t *testing.T) {
	backend := newProjectExtTestBackend(t)
	conn := &recordingConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}
	d := NewDispatcher(&fakeAuth{}, backend)
	d.addAuthedConn(conn)

	dir := t.TempDir()
	result := projectExtTestAdd(t, d, conn, dir)

	if result.Project.ID == "" {
		t.Fatal("expected a project ID")
	}
	if result.Project.Path != dir {
		t.Fatalf("path = %q, want %q", result.Project.Path, dir)
	}
	if result.Project.Name != filepath.Base(dir) {
		t.Fatalf("name = %q, want %q", result.Project.Name, filepath.Base(dir))
	}
	if count := projectExtTestCount(t, backend); count != 1 {
		t.Fatalf("project count = %d, want 1", count)
	}
	// 项目列表没有内置通知，添加成功后必须广播 projects.changed。
	if len(conn.notifications) != 1 || conn.notifications[0] != projectExtChangedMethod {
		t.Fatalf("unexpected notifications: %+v", conn.notifications)
	}
}

func TestProjectExtAdd_RejectsInvalidPath(t *testing.T) {
	backend := newProjectExtTestBackend(t)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}
	d := NewDispatcher(&fakeAuth{}, backend)

	home, homeErr := os.UserHomeDir()
	existFile := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(existFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	type projectExtPathCase struct {
		name    string
		path    string
		wantMsg string
	}
	cases := []projectExtPathCase{
		{"empty", "   ", "路径不能为空"},
		{"relative", "code/app", "路径必须是绝对路径"},
		{"missing", filepath.Join(t.TempDir(), "missing"), "路径不存在"},
		{"file", existFile, "不是目录"},
		{"root", filepath.VolumeName(existFile) + string(filepath.Separator), "不能把根目录添加为项目"},
	}
	// 拿不到主目录的环境（极少数 CI）分别跳过这两条，不影响其它用例。
	if homeErr == nil && home != "" {
		cases = append(cases,
			projectExtPathCase{"home", home, "不能把主目录添加为项目"},
			projectExtPathCase{"tildeHome", "~", "不能把主目录添加为项目"},
		)
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			resp, _ := projectExtTestCall(t, d, conn, projectExtAddMethod, wire.ProjectExtAddParams{Path: tc.path})
			if resp.Error == nil {
				t.Fatalf("expected error for %q", tc.path)
			}
			if resp.Error.Code != wire.InvalidParams {
				t.Fatalf("code = %d, want %d", resp.Error.Code, wire.InvalidParams)
			}
			if !strings.Contains(resp.Error.Message, tc.wantMsg) {
				t.Fatalf("message = %q, want contains %q", resp.Error.Message, tc.wantMsg)
			}
		})
	}
	if count := projectExtTestCount(t, backend); count != 0 {
		t.Fatalf("rejected paths must not create projects, count = %d", count)
	}
}

func TestProjectExtAdd_DuplicateReturnsExisting(t *testing.T) {
	backend := newProjectExtTestBackend(t)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}
	d := NewDispatcher(&fakeAuth{}, backend)

	dir := t.TempDir()
	first := projectExtTestAdd(t, d, conn, dir)
	second := projectExtTestAdd(t, d, conn, dir)
	if second.Project.ID != first.Project.ID {
		t.Fatalf("duplicate add created a new project: %q vs %q", second.Project.ID, first.Project.ID)
	}
	if count := projectExtTestCount(t, backend); count != 1 {
		t.Fatalf("project count = %d, want 1", count)
	}
}

func TestProjectExtAdd_TildeExpansion(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skipf("skip tilde test, no user home: %v", err)
	}
	// 直接在 $HOME 下建临时目录验证端到端展开；环境不允许写主目录时跳过。
	dir, err := os.MkdirTemp(home, ".mcode-project-ext-test-")
	if err != nil {
		t.Skipf("skip tilde end-to-end test: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	backend := newProjectExtTestBackend(t)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}
	d := NewDispatcher(&fakeAuth{}, backend)

	rel := "~/" + filepath.Base(dir)
	result := projectExtTestAdd(t, d, conn, rel)
	if result.Project.Path != dir {
		t.Fatalf("path = %q, want %q", result.Project.Path, dir)
	}
}

func TestProjectExtExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skipf("skip expandHome test, no user home: %v", err)
	}
	got, ok := projectExtExpandHome("~/code/my-app")
	if !ok {
		t.Fatal("expected ~/code/my-app to expand")
	}
	if want := filepath.Join(home, "code", "my-app"); got != want {
		t.Fatalf("expanded = %q, want %q", got, want)
	}
	if got, ok := projectExtExpandHome("~"); !ok || got != home {
		t.Fatalf("~ = %q (ok=%v), want %q", got, ok, home)
	}
	// 已经是绝对路径或 ~user 形态时原样返回。
	if got, ok := projectExtExpandHome("/tmp/x"); !ok || got != "/tmp/x" {
		t.Fatalf("absolute path changed: %q (ok=%v)", got, ok)
	}
	if got, ok := projectExtExpandHome("~someone/x"); !ok || got != "~someone/x" {
		t.Fatalf("~user path changed: %q (ok=%v)", got, ok)
	}
}

func TestProjectExtValidatePath_NoSideEffects(t *testing.T) {
	backend := newProjectExtTestBackend(t)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}
	d := NewDispatcher(&fakeAuth{}, backend)

	before := projectExtTestCount(t, backend)
	dir := t.TempDir()
	ok := projectExtTestValidate(t, d, conn, dir)
	if !ok.OK || ok.Reason != "" {
		t.Fatalf("expected ok result, got %+v", ok)
	}
	if ok.NormalizedPath != dir {
		t.Fatalf("normalizedPath = %q, want %q", ok.NormalizedPath, dir)
	}
	bad := projectExtTestValidate(t, d, conn, "relative/path")
	if bad.OK || bad.Reason == "" {
		t.Fatalf("expected failure result, got %+v", bad)
	}
	if after := projectExtTestCount(t, backend); after != before {
		t.Fatalf("validatePath wrote data: count %d -> %d", before, after)
	}
}

func TestProjectExtAdd_NotImplementedBackend(t *testing.T) {
	// fakeBackend 没有 AddProjectPath：应按「宿主不支持」回 MethodNotFound。
	d := newExtensionTestDispatcher()
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp, _ := projectExtTestCall(t, d, conn, projectExtAddMethod, wire.ProjectExtAddParams{Path: t.TempDir()})
	if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
		t.Fatalf("expected MethodNotFound, got: %+v", resp.Error)
	}
}
