package remote

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"mcode/internal/remote/wire"
)

const (
	scriptsSaveTestProjectID = "11111111-1111-4111-8111-111111111111"
	scriptsSaveTestThreadID  = "22222222-2222-4222-8222-222222222222"
)

// scriptsSaveTestDispatcher 造一个带项目和会话的后端与 dispatcher。
func scriptsSaveTestDispatcher(t *testing.T) (*MemoryBackend, *Dispatcher, Conn) {
	t.Helper()
	backend := newProjectExtTestBackend(t)
	addProjectScriptsTestThread(t, backend, scriptsSaveTestProjectID, scriptsSaveTestThreadID)
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}
	return backend, d, conn
}

// 名称或命令为空、名称重复、projectID 非法或不存在都必须被拒绝，且不写入存储。
func TestScriptsSaveRejectsInvalidParams(t *testing.T) {
	backend, d, conn := scriptsSaveTestDispatcher(t)

	emptyName := wire.ScriptsSaveScript{Name: "  ", Command: "make lint"}
	emptyCommand := wire.ScriptsSaveScript{Name: "lint", Command: "   "}
	dupA := wire.ScriptsSaveScript{Name: "lint", Command: "make lint"}
	dupB := wire.ScriptsSaveScript{Name: "lint", Command: "make lint2"}

	cases := []struct {
		name     string
		params   wire.ScriptsSaveParams
		wantCode int
	}{
		{
			name:     "invalid project id",
			params:   wire.ScriptsSaveParams{ProjectID: "p1", Scripts: []wire.ScriptsSaveScript{dupA}},
			wantCode: wire.InvalidParams,
		},
		{
			name:     "unknown project",
			params:   wire.ScriptsSaveParams{ProjectID: "33333333-3333-4333-8333-333333333333", Scripts: []wire.ScriptsSaveScript{dupA}},
			wantCode: wire.ErrCodeResourceNotFound,
		},
		{
			name:     "empty name",
			params:   wire.ScriptsSaveParams{ProjectID: scriptsSaveTestProjectID, Scripts: []wire.ScriptsSaveScript{emptyName}},
			wantCode: wire.InvalidParams,
		},
		{
			name:     "empty command",
			params:   wire.ScriptsSaveParams{ProjectID: scriptsSaveTestProjectID, Scripts: []wire.ScriptsSaveScript{emptyCommand}},
			wantCode: wire.InvalidParams,
		},
		{
			name:     "duplicate names",
			params:   wire.ScriptsSaveParams{ProjectID: scriptsSaveTestProjectID, Scripts: []wire.ScriptsSaveScript{dupA, dupB}},
			wantCode: wire.InvalidParams,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, _ := projectExtTestCall(t, d, conn, "scripts.save", tc.params)
			if resp.Error == nil || resp.Error.Code != tc.wantCode {
				t.Fatalf("expected code %d, got %+v", tc.wantCode, resp.Error)
			}
		})
	}

	scripts, err := backend.ProjectScripts(scriptsSaveTestThreadID)
	if err != nil {
		t.Fatalf("read project scripts: %v", err)
	}
	if len(scripts) != 0 {
		t.Fatalf("rejected save must not persist, got %+v", scripts)
	}
}

// 整表替换成功：返回保存后的列表（含 UI 需要的命令与自动运行标记），
// 列表版本随内容变化；带过期 expectedRevision 保存返回版本冲突。
func TestScriptsSaveReplacesAndDetectsConflict(t *testing.T) {
	_, d, conn := scriptsSaveTestDispatcher(t)
	symbol := "hammer"

	resp, raw := projectExtTestCall(t, d, conn, "scripts.save", wire.ScriptsSaveParams{
		ProjectID: scriptsSaveTestProjectID,
		Scripts: []wire.ScriptsSaveScript{{
			Name:                "lint",
			Command:             "make lint",
			Symbol:              &symbol,
			RunOnWorktreeCreate: true,
		}},
	})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	var saved wire.ScriptsSaveResult
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(saved.ProjectScripts) != 1 {
		t.Fatalf("projectScripts = %+v, want 1 item", saved.ProjectScripts)
	}
	item := saved.ProjectScripts[0]
	if item.ID == "" {
		t.Fatal("expected the saved script to get an ID")
	}
	if item.Name != "lint" || item.Command != "make lint" || !item.RunOnWorktreeCreate {
		t.Fatalf("unexpected saved script: %+v", item)
	}
	if item.Symbol == nil || *item.Symbol != "hammer" {
		t.Fatalf("symbol = %v, want hammer", item.Symbol)
	}
	if saved.Revision == "" {
		t.Fatal("expected a revision")
	}

	// scripts.list 与 scripts.save 同型：脚本字段与 revision 一致。
	resp, raw = projectExtTestCall(t, d, conn, "scripts.list", wire.ScriptsListParams{ThreadID: scriptsSaveTestThreadID})
	if resp.Error != nil {
		t.Fatalf("unexpected list error: %+v", resp.Error)
	}
	var listed wire.ScriptsListResult
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if listed.Revision != saved.Revision {
		t.Fatalf("list revision = %q, save revision = %q", listed.Revision, saved.Revision)
	}
	if len(listed.ProjectScripts) != 1 || listed.ProjectScripts[0].Command != "make lint" || !listed.ProjectScripts[0].RunOnWorktreeCreate {
		t.Fatalf("unexpected listed scripts: %+v", listed.ProjectScripts)
	}

	// 带上当前 revision 可以保存，内容变化后 revision 也随之变化。
	revision := listed.Revision
	resp, raw = projectExtTestCall(t, d, conn, "scripts.save", wire.ScriptsSaveParams{
		ProjectID:        scriptsSaveTestProjectID,
		ExpectedRevision: &revision,
		Scripts: []wire.ScriptsSaveScript{
			{Name: "lint", Command: "make lint"},
			{Name: "test", Command: "make test"},
		},
	})
	if resp.Error != nil {
		t.Fatalf("unexpected error on conditional save: %+v", resp.Error)
	}
	var second wire.ScriptsSaveResult
	if err := json.Unmarshal(raw, &second); err != nil {
		t.Fatalf("unmarshal second save: %v", err)
	}
	if len(second.ProjectScripts) != 2 {
		t.Fatalf("projectScripts = %+v, want 2 items", second.ProjectScripts)
	}
	if second.Revision == "" || second.Revision == revision {
		t.Fatalf("revision should change after content change, got %q", second.Revision)
	}

	// 用过期 revision 保存必须报版本冲突。
	resp, _ = projectExtTestCall(t, d, conn, "scripts.save", wire.ScriptsSaveParams{
		ProjectID:        scriptsSaveTestProjectID,
		ExpectedRevision: &revision,
		Scripts:          []wire.ScriptsSaveScript{{Name: "deploy", Command: "make deploy"}},
	})
	if resp.Error == nil || resp.Error.Code != wire.ErrCodeVersionConflict {
		t.Fatalf("expected version conflict, got %+v", resp.Error)
	}
}

// capabilities 带桌面端版本号；未注入时不输出该字段。
func TestCapabilitiesIncludesAppVersion(t *testing.T) {
	backend := newProjectExtTestBackend(t)
	backend.SetAppVersion("1.2.3")

	caps, err := backend.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	if caps.AppVersion != "1.2.3" {
		t.Fatalf("appVersion = %q, want %q", caps.AppVersion, "1.2.3")
	}

	empty := newProjectExtTestBackend(t)
	caps, err = empty.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	raw, err := json.Marshal(caps)
	if err != nil {
		t.Fatalf("marshal capabilities: %v", err)
	}
	if strings.Contains(string(raw), "appVersion") {
		t.Fatalf("empty appVersion must be omitted, got %s", raw)
	}
}
