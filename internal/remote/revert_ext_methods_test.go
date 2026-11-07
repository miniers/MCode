package remote

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"mcode/internal/git"
	"mcode/internal/remote/wire"
)

// revertExtTestThreadID / revertExtTestTurnID 是测试用的合法 UUID。
const (
	revertExtTestThreadID = "22222222-2222-2222-2222-222222222222"
	revertExtTestTurnID   = "33333333-3333-3333-3333-333333333333"
)

// revertExtTestBackend 只实现撤销能力，其余 Backend 方法由内嵌的 nil 接口兜底。
type revertExtTestBackend struct {
	Backend
	preview   *git.RestorePreview
	err       error
	previewed int
	executed  int
}

func (b *revertExtTestBackend) PreviewTurnRevert(ctx context.Context, threadID, turnID string) (*git.RestorePreview, error) {
	if b.err != nil {
		return nil, b.err
	}
	b.previewed++
	return b.preview, nil
}

func (b *revertExtTestBackend) ExecuteTurnRevert(ctx context.Context, threadID, turnID string) (*git.RestorePreview, error) {
	if b.err != nil {
		return nil, b.err
	}
	b.executed++
	return b.preview, nil
}

// revertExtTestPreview 构造一份含三种文件状态的预览：正常回退、保留、区间内新增。
func revertExtTestPreview() *git.RestorePreview {
	return &git.RestorePreview{
		Base:   "base-sha",
		Finish: "finish-sha",
		Files: []git.RestoreFile{
			{Path: "internal/a.go", Additions: 12, Deletions: 3, InBase: true},
			{Path: "internal/kept.go", Additions: 5, Deletions: 1, IsKept: true, InBase: true},
			{Path: "web/new.ts", Additions: 40},
		},
	}
}

func TestRevertExtMethodsRegistered(t *testing.T) {
	want := []string{"threads.revert.preview", "threads.revert.apply"}
	have := make(map[string]bool, len(extensionMethodNames()))
	for _, name := range extensionMethodNames() {
		have[name] = true
	}
	for _, name := range want {
		if !have[name] {
			t.Fatalf("method %s should be registered, got %v", name, extensionMethodNames())
		}
	}
}

func TestRevertExtInvalidParams(t *testing.T) {
	backend := &revertExtTestBackend{preview: revertExtTestPreview()}
	d, conn := newGitExtTestDispatcher(backend)

	cases := []struct {
		name   string
		method string
		params string
	}{
		{"threadID 非 UUID", "threads.revert.preview", `{"threadID":"nope","turnID":"` + revertExtTestTurnID + `"}`},
		{"turnID 非 UUID", "threads.revert.preview", `{"threadID":"` + revertExtTestThreadID + `","turnID":"nope"}`},
		{"threadID 缺失", "threads.revert.preview", `{"turnID":"` + revertExtTestTurnID + `"}`},
		{"未知字段", "threads.revert.preview", `{"threadID":"` + revertExtTestThreadID + `","turnID":"` + revertExtTestTurnID + `","extra":1}`},
		{"apply 参数非法", "threads.revert.apply", `{"threadID":"","turnID":""}`},
	}
	for i, tc := range cases {
		resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"`+tc.method+`","params":`+tc.params+`,"id":`+strconv.Itoa(i+1)+`}`)
		if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
			t.Fatalf("%s: expected InvalidParams, got: %+v", tc.name, resp.Error)
		}
	}
	if backend.previewed != 0 || backend.executed != 0 {
		t.Fatalf("参数非法时不应调用后端: previewed=%d executed=%d", backend.previewed, backend.executed)
	}
}

func TestRevertExtPreview(t *testing.T) {
	backend := &revertExtTestBackend{preview: revertExtTestPreview()}
	d, conn := newGitExtTestDispatcher(backend)

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"threads.revert.preview","params":{"threadID":"`+revertExtTestThreadID+`","turnID":"`+revertExtTestTurnID+`"},"id":1}`)
	var res wire.ThreadsRevertPreviewResult
	gitExtResult(t, resp, &res)

	if backend.previewed != 1 || backend.executed != 0 {
		t.Fatalf("expected only one preview call, got previewed=%d executed=%d", backend.previewed, backend.executed)
	}
	if res.ThreadID != revertExtTestThreadID || res.TurnID != revertExtTestTurnID {
		t.Fatalf("unexpected ids: %+v", res)
	}
	if res.Base != "base-sha" || res.Finish != "finish-sha" {
		t.Fatalf("unexpected checkpoints: %+v", res)
	}
	if res.Total != 3 || res.Restorable != 2 || res.KeptCount != 1 {
		t.Fatalf("unexpected counts: total=%d restorable=%d kept=%d", res.Total, res.Restorable, res.KeptCount)
	}
	if len(res.Kept) != 1 || res.Kept[0] != "internal/kept.go" {
		t.Fatalf("unexpected kept list: %+v", res.Kept)
	}
	if len(res.Files) != 3 {
		t.Fatalf("expected 3 files, got %+v", res.Files)
	}
	first := res.Files[0]
	if first.Path != "internal/a.go" || first.Additions != 12 || first.Deletions != 3 || first.Kept || !first.InBase || !first.Restorable {
		t.Fatalf("unexpected first file: %+v", first)
	}
	second := res.Files[1]
	if second.Path != "internal/kept.go" || !second.Kept || second.Restorable {
		t.Fatalf("unexpected second file: %+v", second)
	}
	third := res.Files[2]
	if third.Path != "web/new.ts" || third.InBase || !third.Restorable {
		t.Fatalf("unexpected third file: %+v", third)
	}
}

func TestRevertExtApply(t *testing.T) {
	backend := &revertExtTestBackend{preview: revertExtTestPreview()}
	d, conn := newGitExtTestDispatcher(backend)

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"threads.revert.apply","params":{"threadID":"`+revertExtTestThreadID+`","turnID":"`+revertExtTestTurnID+`"},"id":1}`)
	var res wire.ThreadsRevertApplyResult
	gitExtResult(t, resp, &res)

	if backend.executed != 1 || backend.previewed != 0 {
		t.Fatalf("expected one execute call, got previewed=%d executed=%d", backend.previewed, backend.executed)
	}
	if !res.OK {
		t.Fatalf("expected ok result, got %+v", res)
	}
	if res.Total != 3 || res.Restorable != 2 || len(res.Files) != 3 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.ThreadID != revertExtTestThreadID || res.TurnID != revertExtTestTurnID {
		t.Fatalf("unexpected ids: %+v", res)
	}
}

func TestRevertExtBackendErrors(t *testing.T) {
	backend := &revertExtTestBackend{err: errors.New("会话正在运行，无法撤销")}
	d, conn := newGitExtTestDispatcher(backend)

	for i, method := range []string{"threads.revert.preview", "threads.revert.apply"} {
		resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"`+method+`","params":{"threadID":"`+revertExtTestThreadID+`","turnID":"`+revertExtTestTurnID+`"},"id":`+strconv.Itoa(i+1)+`}`)
		if resp.Error == nil || resp.Error.Code != wire.ErrCodeOperationRejected {
			t.Fatalf("%s: expected operationRejected, got: %+v", method, resp.Error)
		}
		if !strings.Contains(resp.Error.Message, "无法撤销") {
			t.Fatalf("%s: original error text should be preserved, got %q", method, resp.Error.Message)
		}
	}

	// 后端没有撤销能力时按未实现处理
	d2, conn2 := newGitExtTestDispatcher(&fakeBackend{})
	resp := callExtension(t, d2, conn2, `{"jsonrpc":"2.0","method":"threads.revert.preview","params":{"threadID":"`+revertExtTestThreadID+`","turnID":"`+revertExtTestTurnID+`"},"id":3}`)
	if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
		t.Fatalf("expected MethodNotFound, got: %+v", resp.Error)
	}
}
