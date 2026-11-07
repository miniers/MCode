package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"mcode/internal/remote/wire"
)

// fakeMemoryExtService 是注入用的假记忆面板服务，记录调用参数方便断言。
type fakeMemoryExtService struct {
	proposals []wire.MemoryExtProposal
	listErr   error
	acceptErr error
	rejectErr error
	createErr error
	searchErr error

	listCalls    int
	projectPath  string
	status       string
	acceptedID   string
	acceptedEdit *wire.MemoryExtProposalEdit
	rejectedID   string
	createInput  wire.MemoryExtCreateInput
	searchQuery  string
	searchPath   string
	searchLimit  int
	searchHits   []wire.MemoryExtMemory
}

func (f *fakeMemoryExtService) ListProposals(ctx context.Context, projectPath, status string) ([]wire.MemoryExtProposal, error) {
	f.listCalls++
	f.projectPath = projectPath
	f.status = status
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.proposals, nil
}

func (f *fakeMemoryExtService) AcceptProposal(ctx context.Context, id string, edit *wire.MemoryExtProposalEdit) (wire.MemoryExtProposal, error) {
	f.acceptedID = id
	f.acceptedEdit = edit
	if f.acceptErr != nil {
		return wire.MemoryExtProposal{}, f.acceptErr
	}
	proposal := wire.MemoryExtProposal{ID: id, Title: "已接受", Status: memoryExtStatusAccepted}
	if edit != nil {
		if edit.Title != nil {
			proposal.Title = *edit.Title
		}
		if edit.Content != nil {
			proposal.Content = *edit.Content
		}
		if edit.Type != nil {
			proposal.UnitType = *edit.Type
		}
		if edit.Importance != nil {
			proposal.Importance = *edit.Importance
		}
	}
	return proposal, nil
}

func (f *fakeMemoryExtService) CreateMemory(ctx context.Context, in wire.MemoryExtCreateInput) (wire.MemoryExtProposal, error) {
	f.createInput = in
	if f.createErr != nil {
		return wire.MemoryExtProposal{}, f.createErr
	}
	return wire.MemoryExtProposal{
		ID:          "p-new",
		ProjectPath: in.ProjectPath,
		ThreadID:    in.ThreadID,
		Title:       in.Title,
		Content:     in.Content,
		UnitType:    in.Type,
		Importance:  0.5,
		Source:      "user",
		Status:      memoryExtStatusPending,
	}, nil
}

func (f *fakeMemoryExtService) SearchMemories(ctx context.Context, projectPath, query string, limit int) ([]wire.MemoryExtMemory, error) {
	f.searchPath = projectPath
	f.searchQuery = query
	f.searchLimit = limit
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	return f.searchHits, nil
}

func (f *fakeMemoryExtService) RejectProposal(ctx context.Context, id string) error {
	f.rejectedID = id
	return f.rejectErr
}

// memoryExtRecordingConn 记录广播收到的通知方法与载荷。
type memoryExtRecordingConn struct {
	info          ClientInfo
	notifications []memoryExtRecordedNotification
}

type memoryExtRecordedNotification struct {
	method string
	params any
}

func (c *memoryExtRecordingConn) Info() ClientInfo { return c.info }

func (c *memoryExtRecordingConn) MarkAuthenticated(deviceID, label string) {
	c.info.Authenticated = true
	c.info.DeviceID = deviceID
	c.info.Label = label
}

func (c *memoryExtRecordingConn) Notify(method string, params any) error {
	c.notifications = append(c.notifications, memoryExtRecordedNotification{method: method, params: params})
	return nil
}

func (c *memoryExtRecordingConn) Close() error { return nil }

// newMemoryExtTestBackend 造一个带项目的 MemoryBackend，返回后端与项目摘要。
func newMemoryExtTestBackend(t *testing.T) (*MemoryBackend, wire.ProjectSummary) {
	t.Helper()
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	t.Cleanup(hub.Close)
	backend := NewMemoryBackend(hub)
	t.Cleanup(backend.Close)
	backend.SetDataDir(t.TempDir())

	project := wire.ProjectSummary{ID: newUUID(), Name: "proj", Path: t.TempDir()}
	backend.AddProject(project)
	return backend, project
}

// newMemoryExtTestDispatcher 用真实 MemoryBackend 构造 Dispatcher，并挂一个已认证的录制连接。
func newMemoryExtTestDispatcher(t *testing.T, backend *MemoryBackend) (*Dispatcher, *memoryExtRecordingConn) {
	t.Helper()
	d := NewDispatcher(&fakeAuth{}, backend)
	rec := &memoryExtRecordingConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}
	d.addAuthedConn(rec)
	t.Cleanup(func() { d.RemoveConn(rec) })
	return d, rec
}

func TestMemoryProposalsList_Success(t *testing.T) {
	backend, project := newMemoryExtTestBackend(t)
	svc := &fakeMemoryExtService{proposals: []wire.MemoryExtProposal{
		{ID: "p-1", Title: "用 SQLite 存提议", Status: memoryExtStatusPending, Source: "agent"},
	}}
	backend.SetService(memoryExtServiceName, svc)
	d, _ := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn, fmt.Sprintf(
		`{"jsonrpc":"2.0","method":"memory.proposals.list","params":{"projectID":%q,"status":"pending"},"id":1}`, project.ID))
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var result wire.MemoryExtListResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(result.Proposals) != 1 || result.Proposals[0].ID != "p-1" {
		t.Fatalf("unexpected proposals: %s", raw)
	}
	// projectID 必须换算成项目路径再查收件箱。
	if svc.projectPath != project.Path || svc.status != memoryExtStatusPending {
		t.Fatalf("service called with unexpected args: %+v", svc)
	}
	if svc.listCalls != 1 {
		t.Fatalf("list should be called once, got %d", svc.listCalls)
	}

	// 服务返回 nil 时输出空数组，客户端不必处理 null。
	svc.proposals = nil
	resp = callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"memory.proposals.list","params":{},"id":2}`)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	raw, err = json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if string(raw) != `{"proposals":[]}` {
		t.Fatalf("empty list should marshal as [], got %s", raw)
	}
	// 不带 projectID 表示全部项目：路径为空、状态为空。
	if svc.projectPath != "" || svc.status != "" {
		t.Fatalf("全部项目应传空路径与空状态，得到 path=%q status=%q", svc.projectPath, svc.status)
	}
}

func TestMemoryProposalsList_UnknownProject(t *testing.T) {
	backend, _ := newMemoryExtTestBackend(t)
	svc := &fakeMemoryExtService{}
	backend.SetService(memoryExtServiceName, svc)
	d, _ := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"memory.proposals.list","params":{"projectID":"no-such-project"},"id":1}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("未知项目应返回 InvalidParams，得到 %+v", resp.Error)
	}
	// 未知项目不能被当成「全部项目」去查收件箱。
	if svc.listCalls != 0 {
		t.Fatalf("未知项目不该调用服务，调用次数 %d", svc.listCalls)
	}
}

func TestMemoryProposalsList_InvalidStatus(t *testing.T) {
	backend, _ := newMemoryExtTestBackend(t)
	backend.SetService(memoryExtServiceName, &fakeMemoryExtService{})
	d, _ := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"memory.proposals.list","params":{"status":"whatever"},"id":1}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("非法状态应返回 InvalidParams，得到 %+v", resp.Error)
	}
}

func TestMemoryProposalsAccept_NotifiesChanged(t *testing.T) {
	backend, _ := newMemoryExtTestBackend(t)
	svc := &fakeMemoryExtService{}
	backend.SetService(memoryExtServiceName, svc)
	d, rec := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"memory.proposals.accept","params":{"id":"p-1"},"id":1}`)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var proposal wire.MemoryExtProposal
	if err := json.Unmarshal(raw, &proposal); err != nil {
		t.Fatalf("unmarshal proposal: %v", err)
	}
	if proposal.ID != "p-1" || proposal.Status != memoryExtStatusAccepted {
		t.Fatalf("accept 应返回更新后的提议，得到 %s", raw)
	}
	if svc.acceptedID != "p-1" {
		t.Fatalf("accept 未带上 id，得到 %q", svc.acceptedID)
	}
	memoryExtAssertChanged(t, rec, "p-1", memoryExtStatusAccepted)

	// 服务报错时不广播状态变化。
	svc.acceptErr = errors.New("nowledge 未配置")
	rec.notifications = nil
	resp = callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"memory.proposals.accept","params":{"id":"p-2"},"id":2}`)
	if resp.Error == nil {
		t.Fatal("服务报错时应返回错误")
	}
	if len(rec.notifications) != 0 {
		t.Fatalf("失败时不该广播，得到 %+v", rec.notifications)
	}
}

func TestMemoryProposalsReject_NotifiesChanged(t *testing.T) {
	backend, _ := newMemoryExtTestBackend(t)
	svc := &fakeMemoryExtService{}
	backend.SetService(memoryExtServiceName, svc)
	d, rec := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"memory.proposals.reject","params":{"id":"p-9"},"id":1}`)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if string(raw) != `{"ok":true}` {
		t.Fatalf("reject 应返回 {ok:true}，得到 %s", raw)
	}
	if svc.rejectedID != "p-9" {
		t.Fatalf("reject 未带上 id，得到 %q", svc.rejectedID)
	}
	memoryExtAssertChanged(t, rec, "p-9", memoryExtStatusRejected)
}

func TestMemoryProposals_EmptyID(t *testing.T) {
	backend, _ := newMemoryExtTestBackend(t)
	svc := &fakeMemoryExtService{}
	backend.SetService(memoryExtServiceName, svc)
	d, _ := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	for _, method := range []string{"memory.proposals.accept", "memory.proposals.reject"} {
		resp := callExtension(t, d, conn, fmt.Sprintf(
			`{"jsonrpc":"2.0","method":%q,"params":{"id":"  "},"id":1}`, method))
		if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
			t.Fatalf("%s 的空 id 应返回 InvalidParams，得到 %+v", method, resp.Error)
		}
	}
	if svc.acceptedID != "" || svc.rejectedID != "" {
		t.Fatalf("空 id 不该调用服务：%+v", svc)
	}
}

func TestMemoryProposals_NoService(t *testing.T) {
	backend, project := newMemoryExtTestBackend(t)
	d, _ := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	requests := []string{
		fmt.Sprintf(`{"jsonrpc":"2.0","method":"memory.proposals.list","params":{"projectID":%q},"id":1}`, project.ID),
		`{"jsonrpc":"2.0","method":"memory.proposals.accept","params":{"id":"p-1"},"id":2}`,
		`{"jsonrpc":"2.0","method":"memory.proposals.reject","params":{"id":"p-1"},"id":3}`,
		fmt.Sprintf(`{"jsonrpc":"2.0","method":"memory.create","params":{"projectID":%q,"title":"T"},"id":4}`, project.ID),
		`{"jsonrpc":"2.0","method":"memory.search","params":{"query":"q"},"id":5}`,
	}
	for _, request := range requests {
		resp := callExtension(t, d, conn, request)
		if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
			t.Fatalf("未注入服务时应返回 MethodNotFound，得到 %+v（%s）", resp.Error, request)
		}
	}
}

// memoryExtAssertChanged 断言收到一条 memory.proposals.changed 通知，且载荷里是给定 id 与状态。
func memoryExtAssertChanged(t *testing.T, rec *memoryExtRecordingConn, wantID, wantStatus string) {
	t.Helper()
	if len(rec.notifications) != 1 {
		t.Fatalf("应广播一条通知，得到 %+v", rec.notifications)
	}
	got := rec.notifications[0]
	if got.method != memoryExtChangedMethod {
		t.Fatalf("通知方法名不对：%q", got.method)
	}
	payload, ok := got.params.(wire.MemoryExtChangedNotification)
	if !ok {
		t.Fatalf("通知载荷类型不对：%T", got.params)
	}
	if payload.ID != wantID || payload.Status != wantStatus {
		t.Fatalf("通知载荷不对：%+v", payload)
	}
}

func TestMemoryProposalsAccept_WithEdit(t *testing.T) {
	backend, _ := newMemoryExtTestBackend(t)
	svc := &fakeMemoryExtService{}
	backend.SetService(memoryExtServiceName, svc)
	d, rec := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"memory.proposals.accept","params":{"id":"p-1","title":"  改过的标题  ","content":"改过的正文","type":"insight","importance":0.9},"id":1}`)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if svc.acceptedEdit == nil {
		t.Fatal("带编辑字段的接受应把编辑传给服务")
	}
	// 标题去空格后写入；正文、类型、重要度按传入值写入。
	if got := *svc.acceptedEdit.Title; got != "改过的标题" {
		t.Fatalf("title 未去空格：%q", got)
	}
	if got := *svc.acceptedEdit.Content; got != "改过的正文" {
		t.Fatalf("content 未传递：%q", got)
	}
	if got := *svc.acceptedEdit.Type; got != "insight" {
		t.Fatalf("type 未传递：%q", got)
	}
	if got := *svc.acceptedEdit.Importance; got != 0.9 {
		t.Fatalf("importance 未传递：%v", got)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var proposal wire.MemoryExtProposal
	if err := json.Unmarshal(raw, &proposal); err != nil {
		t.Fatalf("unmarshal proposal: %v", err)
	}
	if proposal.Title != "改过的标题" || proposal.Content != "改过的正文" || proposal.UnitType != "insight" {
		t.Fatalf("接受结果应带上编辑后的内容，得到 %s", raw)
	}
	memoryExtAssertChanged(t, rec, "p-1", memoryExtStatusAccepted)

	// 不带编辑字段时按原样接受：服务收到的 edit 为 nil。
	rec.notifications = nil
	resp = callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"memory.proposals.accept","params":{"id":"p-2"},"id":2}`)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if svc.acceptedEdit != nil {
		t.Fatalf("未带编辑字段时 edit 应为 nil，得到 %+v", svc.acceptedEdit)
	}
}

func TestMemoryProposalsAccept_InvalidEdit(t *testing.T) {
	backend, _ := newMemoryExtTestBackend(t)
	svc := &fakeMemoryExtService{}
	backend.SetService(memoryExtServiceName, svc)
	d, _ := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	requests := []string{
		`{"jsonrpc":"2.0","method":"memory.proposals.accept","params":{"id":"p-1","title":"   "},"id":1}`,
		`{"jsonrpc":"2.0","method":"memory.proposals.accept","params":{"id":"p-1","type":"  "},"id":2}`,
		`{"jsonrpc":"2.0","method":"memory.proposals.accept","params":{"id":"p-1","importance":1.5},"id":3}`,
		`{"jsonrpc":"2.0","method":"memory.proposals.accept","params":{"id":"p-1","importance":-0.1},"id":4}`,
	}
	for _, request := range requests {
		resp := callExtension(t, d, conn, request)
		if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
			t.Fatalf("非法编辑字段应返回 InvalidParams，得到 %+v（%s）", resp.Error, request)
		}
	}
	if svc.acceptedID != "" {
		t.Fatalf("非法编辑字段不该调用服务，得到 %+v", svc)
	}
}

func TestMemoryCreate_Success(t *testing.T) {
	backend, project := newMemoryExtTestBackend(t)
	svc := &fakeMemoryExtService{}
	backend.SetService(memoryExtServiceName, svc)
	d, rec := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn, fmt.Sprintf(
		`{"jsonrpc":"2.0","method":"memory.create","params":{"projectID":%q,"title":"  新记忆  ","content":"正文","importance":0.8,"scope":"global"},"id":1}`, project.ID))
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	// projectID 换算成项目路径；标题去空格；未指定类型时补默认值 fact。
	if svc.createInput.ProjectPath != project.Path {
		t.Fatalf("projectID 应换算成项目路径，得到 %q", svc.createInput.ProjectPath)
	}
	if svc.createInput.Title != "新记忆" || svc.createInput.Content != "正文" {
		t.Fatalf("标题/正文传递不对：%+v", svc.createInput)
	}
	if svc.createInput.Type != memoryExtDefaultUnitType {
		t.Fatalf("未指定类型时应补默认值，得到 %q", svc.createInput.Type)
	}
	if svc.createInput.Importance == nil || *svc.createInput.Importance != 0.8 {
		t.Fatalf("重要度传递不对：%+v", svc.createInput.Importance)
	}
	if svc.createInput.Scope != memoryExtScopeGlobal {
		t.Fatalf("范围传递不对：%q", svc.createInput.Scope)
	}

	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var proposal wire.MemoryExtProposal
	if err := json.Unmarshal(raw, &proposal); err != nil {
		t.Fatalf("unmarshal proposal: %v", err)
	}
	if proposal.ID != "p-new" || proposal.Status != memoryExtStatusPending {
		t.Fatalf("新建记忆应返回待审提议，得到 %s", raw)
	}
	memoryExtAssertChanged(t, rec, "p-new", memoryExtStatusPending)

	// 显式指定类型时按传入值写入。
	resp = callExtension(t, d, conn, fmt.Sprintf(
		`{"jsonrpc":"2.0","method":"memory.create","params":{"projectID":%q,"title":"T","type":"insight"},"id":2}`, project.ID))
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if svc.createInput.Type != "insight" {
		t.Fatalf("显式类型未被采用：%q", svc.createInput.Type)
	}
	if svc.createInput.Importance != nil || svc.createInput.Scope != "" {
		t.Fatalf("未传的可选字段应保持为空：%+v", svc.createInput)
	}
}

func TestMemoryCreate_Validation(t *testing.T) {
	backend, project := newMemoryExtTestBackend(t)
	svc := &fakeMemoryExtService{}
	backend.SetService(memoryExtServiceName, svc)
	d, _ := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	requests := []string{
		`{"jsonrpc":"2.0","method":"memory.create","params":{"title":"T"},"id":1}`,
		fmt.Sprintf(`{"jsonrpc":"2.0","method":"memory.create","params":{"projectID":%q,"title":"  "},"id":2}`, project.ID),
		fmt.Sprintf(`{"jsonrpc":"2.0","method":"memory.create","params":{"projectID":%q,"title":"T","threadID":"not-a-uuid"},"id":3}`, project.ID),
		fmt.Sprintf(`{"jsonrpc":"2.0","method":"memory.create","params":{"projectID":%q,"title":"T","importance":1.2},"id":4}`, project.ID),
		fmt.Sprintf(`{"jsonrpc":"2.0","method":"memory.create","params":{"projectID":%q,"title":"T","scope":"team"},"id":5}`, project.ID),
	}
	for _, request := range requests {
		resp := callExtension(t, d, conn, request)
		if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
			t.Fatalf("非法参数应返回 InvalidParams，得到 %+v（%s）", resp.Error, request)
		}
	}
	if svc.createInput.Title != "" {
		t.Fatalf("非法参数不该调用服务，得到 %+v", svc.createInput)
	}
}

func TestMemoryCreate_UnknownProject(t *testing.T) {
	backend, _ := newMemoryExtTestBackend(t)
	svc := &fakeMemoryExtService{}
	backend.SetService(memoryExtServiceName, svc)
	d, _ := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"memory.create","params":{"projectID":"no-such-project","title":"T"},"id":1}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("未知项目应返回 InvalidParams，得到 %+v", resp.Error)
	}
	if svc.createInput.Title != "" {
		t.Fatalf("未知项目不该调用服务，得到 %+v", svc.createInput)
	}
}

func TestMemoryCreate_ServiceError(t *testing.T) {
	backend, project := newMemoryExtTestBackend(t)
	svc := &fakeMemoryExtService{createErr: errors.New("记忆服务不可用")}
	backend.SetService(memoryExtServiceName, svc)
	d, rec := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn, fmt.Sprintf(
		`{"jsonrpc":"2.0","method":"memory.create","params":{"projectID":%q,"title":"T"},"id":1}`, project.ID))
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "记忆服务不可用") {
		t.Fatalf("服务不可用时应把原因回给前端，得到 %+v", resp.Error)
	}
	if len(rec.notifications) != 0 {
		t.Fatalf("失败时不该广播，得到 %+v", rec.notifications)
	}
}

func TestMemorySearch_Success(t *testing.T) {
	backend, project := newMemoryExtTestBackend(t)
	svc := &fakeMemoryExtService{searchHits: []wire.MemoryExtMemory{{
		ID:         "m-1",
		Title:      "用 SQLite 存提议",
		Content:    "正文",
		UnitType:   "fact",
		Importance: 0.7,
		Labels:     []string{"store"},
		CreatedAt:  "2026-01-02T03:04:05Z",
	}}}
	backend.SetService(memoryExtServiceName, svc)
	d, _ := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn, fmt.Sprintf(
		`{"jsonrpc":"2.0","method":"memory.search","params":{"projectID":%q,"query":"  sqlite  "},"id":1}`, project.ID))
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	// 查询词去空格；项目 ID 换算成项目路径；未传 limit 时默认 20。
	if svc.searchQuery != "sqlite" || svc.searchPath != project.Path || svc.searchLimit != memoryExtDefaultSearchLimit {
		t.Fatalf("搜索参数不对：query=%q path=%q limit=%d", svc.searchQuery, svc.searchPath, svc.searchLimit)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var result wire.MemoryExtSearchResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(result.Results) != 1 || result.Results[0].ID != "m-1" || result.Results[0].CreatedAt != "2026-01-02T03:04:05Z" {
		t.Fatalf("搜索结果不对：%s", raw)
	}

	// 服务返回 nil 时输出空数组，客户端不必处理 null。
	svc.searchHits = nil
	resp = callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"memory.search","params":{"query":"sqlite"},"id":2}`)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	raw, err = json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if string(raw) != `{"results":[]}` {
		t.Fatalf("空结果应marshal成 []，得到 %s", raw)
	}
	// 不带 projectID 表示全部项目：路径为空。
	if svc.searchPath != "" {
		t.Fatalf("全部项目的路径应为空，得到 %q", svc.searchPath)
	}
}

func TestMemorySearch_LimitAndValidation(t *testing.T) {
	backend, _ := newMemoryExtTestBackend(t)
	svc := &fakeMemoryExtService{searchHits: []wire.MemoryExtMemory{
		{ID: "m-1"}, {ID: "m-2"}, {ID: "m-3"},
	}}
	backend.SetService(memoryExtServiceName, svc)
	d, _ := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	// limit 传给服务，并按 limit 截断服务返回的条数。
	resp := callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"memory.search","params":{"query":"q","limit":2},"id":1}`)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if svc.searchLimit != 2 {
		t.Fatalf("limit 未传给服务：%d", svc.searchLimit)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var result wire.MemoryExtSearchResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(result.Results) != 2 {
		t.Fatalf("结果应按 limit 截断为 2 条，得到 %s", raw)
	}

	// 上限 50 合法，0 与 51 都算参数错误；空查询词也报参数错误。
	svc.searchHits = nil
	if resp := callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"memory.search","params":{"query":"q","limit":50},"id":2}`); resp.Error != nil {
		t.Fatalf("limit=50 应合法：%+v", resp.Error)
	}
	for _, request := range []string{
		`{"jsonrpc":"2.0","method":"memory.search","params":{"query":"q","limit":0},"id":3}`,
		`{"jsonrpc":"2.0","method":"memory.search","params":{"query":"q","limit":51},"id":4}`,
		`{"jsonrpc":"2.0","method":"memory.search","params":{"query":"   "},"id":5}`,
	} {
		resp := callExtension(t, d, conn, request)
		if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
			t.Fatalf("非法参数应返回 InvalidParams，得到 %+v（%s）", resp.Error, request)
		}
	}
}

func TestMemorySearch_ServiceError(t *testing.T) {
	backend, _ := newMemoryExtTestBackend(t)
	svc := &fakeMemoryExtService{searchErr: errors.New("Nowledge Mem 未配置")}
	backend.SetService(memoryExtServiceName, svc)
	d, _ := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"memory.search","params":{"query":"q"},"id":1}`)
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "Nowledge Mem 未配置") {
		t.Fatalf("后端不可用时应把原因回给前端，得到 %+v", resp.Error)
	}
}

func TestMemoryExtMethods_Implemented(t *testing.T) {
	methods := make(map[string]bool)
	for _, name := range ImplementedMethods() {
		methods[name] = true
	}
	for _, name := range []string{"memory.create", "memory.search"} {
		if !methods[name] {
			t.Fatalf("%s 应出现在 capabilities 的方法清单里", name)
		}
	}
}
