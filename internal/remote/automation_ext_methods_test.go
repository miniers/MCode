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

// fakeAutomationExtService 是注入用的假自动化服务，记录调用参数方便断言。
type fakeAutomationExtService struct {
	automations []wire.AutomationExtAutomation
	counts      map[string]int
	runs        []wire.AutomationExtRun
	run         wire.AutomationExtRun
	err         error

	calls              int
	gotProjectID       string
	gotAutomationID    string
	gotLimit           int
	gotIncludeArchived bool
	gotRunID           string
	gotArchived        bool
	archiveCalls       int

	// 写操作记录：入参、版本号、目标状态与调用次数。
	created   wire.AutomationExtAutomation
	updated   wire.AutomationExtAutomation
	gotInput  wire.AutomationExtInput
	gotSubPID string
	gotRev    int
	gotOn     bool
	createCnt int
	updateCnt int
	deleteCnt int
	setOnCnt  int
}

func (f *fakeAutomationExtService) Automations(ctx context.Context, projectID string) ([]wire.AutomationExtAutomation, map[string]int, error) {
	f.calls++
	f.gotProjectID = projectID
	if f.err != nil {
		return nil, nil, f.err
	}
	return f.automations, f.counts, nil
}

func (f *fakeAutomationExtService) Runs(ctx context.Context, automationID string, limit int, includeArchived bool) ([]wire.AutomationExtRun, error) {
	f.calls++
	f.gotAutomationID = automationID
	f.gotLimit = limit
	f.gotIncludeArchived = includeArchived
	if f.err != nil {
		return nil, f.err
	}
	return f.runs, nil
}

func (f *fakeAutomationExtService) RunNow(ctx context.Context, automationID string) (wire.AutomationExtRun, error) {
	f.calls++
	f.gotAutomationID = automationID
	if f.err != nil {
		return wire.AutomationExtRun{}, f.err
	}
	return f.run, nil
}

func (f *fakeAutomationExtService) MarkRunRead(ctx context.Context, runID string) error {
	f.calls++
	f.gotRunID = runID
	return f.err
}

func (f *fakeAutomationExtService) ArchiveRun(ctx context.Context, runID string, archived bool) error {
	f.calls++
	f.archiveCalls++
	f.gotRunID = runID
	f.gotArchived = archived
	return f.err
}

func (f *fakeAutomationExtService) Create(ctx context.Context, projectID string, in wire.AutomationExtInput) (wire.AutomationExtAutomation, error) {
	f.calls++
	f.createCnt++
	f.gotSubPID = projectID
	f.gotInput = in
	if f.err != nil {
		return wire.AutomationExtAutomation{}, f.err
	}
	created := f.created
	if created.ID == "" {
		created = wire.AutomationExtAutomation{ProjectID: projectID, Name: in.Name, Prompt: in.Prompt, Schedule: in.Schedule, Enabled: in.Enabled, Revision: 1}
	}
	return created, nil
}

func (f *fakeAutomationExtService) Update(ctx context.Context, automationID string, in wire.AutomationExtInput, revision int) (wire.AutomationExtAutomation, error) {
	f.calls++
	f.updateCnt++
	f.gotAutomationID = automationID
	f.gotInput = in
	f.gotRev = revision
	if f.err != nil {
		return wire.AutomationExtAutomation{}, f.err
	}
	updated := f.updated
	if updated.ID == "" {
		updated = wire.AutomationExtAutomation{ID: automationID, Name: in.Name, Prompt: in.Prompt, Schedule: in.Schedule, Enabled: in.Enabled, Revision: revision + 1}
	}
	return updated, nil
}

func (f *fakeAutomationExtService) Delete(ctx context.Context, automationID string) error {
	f.calls++
	f.deleteCnt++
	f.gotAutomationID = automationID
	return f.err
}

func (f *fakeAutomationExtService) SetEnabled(ctx context.Context, automationID string, enabled bool, revision int) (wire.AutomationExtAutomation, error) {
	f.calls++
	f.setOnCnt++
	f.gotAutomationID = automationID
	f.gotOn = enabled
	f.gotRev = revision
	if f.err != nil {
		return wire.AutomationExtAutomation{}, f.err
	}
	updated := f.updated
	if updated.ID == "" {
		updated = wire.AutomationExtAutomation{ID: automationID, Enabled: enabled, Revision: revision + 1}
	}
	return updated, nil
}

// automationExtValidInput 造一份合法入参，供新建/修改用例复用。
func automationExtValidInput(name string) wire.AutomationExtInput {
	failures := 3
	iterations := 0
	return wire.AutomationExtInput{
		Name:                   name,
		Prompt:                 "整理昨天的对话并写入周报",
		StopWhen:               "没有新内容时停止",
		Schedule:               wire.AutomationExtSchedule{Type: "daily", TimeOfDay: "09:00", Timezone: "Asia/Shanghai"},
		Enabled:                true,
		MaxConsecutiveFailures: &failures,
		MaxIterations:          &iterations,
	}
}

// newAutomationExtTestBackend 造一个空的 MemoryBackend 用于挂服务与建 Dispatcher。
func newAutomationExtTestBackend(t *testing.T) *MemoryBackend {
	t.Helper()
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	t.Cleanup(hub.Close)
	backend := NewMemoryBackend(hub)
	t.Cleanup(backend.Close)
	backend.SetDataDir(t.TempDir())
	return backend
}

// automationExtResult 断言响应无错误并解码结果。
func automationExtResult[T any](t *testing.T, resp wire.Response) T {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return out
}

func TestAutomationsExtList(t *testing.T) {
	backend := newAutomationExtTestBackend(t)
	projID := newUUID()
	svc := &fakeAutomationExtService{
		automations: []wire.AutomationExtAutomation{
			{ID: newUUID(), ProjectID: projID, Name: "夜间整理", ScheduleText: "每天 03:00", Enabled: true},
			{ID: newUUID(), ProjectID: projID, Name: "周末巡检", ScheduleText: "未设置调度", Enabled: false},
		},
		counts: map[string]int{},
	}
	svc.counts[svc.automations[0].ID] = 2
	backend.SetService(automationExtServiceName, svc)
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"automations.list","params":{"projectID":"`+projID+`"},"id":1}`)
	result := automationExtResult[wire.AutomationExtListResult](t, resp)

	if svc.gotProjectID != projID {
		t.Fatalf("unexpected projectID: %q", svc.gotProjectID)
	}
	if len(result.Automations) != 2 || result.Automations[0].Name != "夜间整理" {
		t.Fatalf("unexpected automations: %+v", result.Automations)
	}
	// 未读数以 counts 为准合并进列表项。
	if result.Automations[0].UnreadCount != 2 || result.Automations[1].UnreadCount != 0 {
		t.Fatalf("unexpected unread counts: %+v", result.Automations)
	}
	if result.UnreadCounts[svc.automations[0].ID] != 2 {
		t.Fatalf("unexpected unreadCounts: %+v", result.UnreadCounts)
	}
}

func TestAutomationsExtRunsLimitAndArchived(t *testing.T) {
	automationID := newUUID()
	cases := []struct {
		name     string
		params   string
		want     int
		archived bool
	}{
		{"default", `{"automationID":"` + automationID + `"}`, automationExtRunsDefaultLimit, false},
		{"zeroUsesDefault", `{"automationID":"` + automationID + `","limit":0}`, automationExtRunsDefaultLimit, false},
		{"explicit", `{"automationID":"` + automationID + `","limit":5,"includeArchived":true}`, 5, true},
		{"clamped", `{"automationID":"` + automationID + `","limit":500}`, automationExtRunsMaxLimit, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend := newAutomationExtTestBackend(t)
			svc := &fakeAutomationExtService{runs: []wire.AutomationExtRun{
				{ID: newUUID(), AutomationID: automationID, Status: "succeeded", Read: true, Summary: "改了 2 个文件"},
			}}
			backend.SetService(automationExtServiceName, svc)
			d := NewDispatcher(&fakeAuth{}, backend)
			conn := &fakeConn{info: ClientInfo{Authenticated: true}}

			resp := callExtension(t, d, conn,
				`{"jsonrpc":"2.0","method":"automations.runs","params":`+tc.params+`,"id":1}`)
			result := automationExtResult[wire.AutomationExtRunsResult](t, resp)
			if svc.gotLimit != tc.want {
				t.Fatalf("unexpected limit: got %d want %d", svc.gotLimit, tc.want)
			}
			if svc.gotIncludeArchived != tc.archived {
				t.Fatalf("unexpected includeArchived: %v", svc.gotIncludeArchived)
			}
			if result.AutomationID != automationID || len(result.Runs) != 1 || !result.Runs[0].Read {
				t.Fatalf("unexpected runs result: %+v", result)
			}
		})
	}
}

func TestAutomationsExtRunNowBroadcast(t *testing.T) {
	backend := newAutomationExtTestBackend(t)
	automationID := newUUID()
	runID := newUUID()
	svc := &fakeAutomationExtService{run: wire.AutomationExtRun{
		ID:           runID,
		AutomationID: automationID,
		Status:       "running",
		StartedAt:    time.Now().UTC().Format(time.RFC3339),
	}}
	backend.SetService(automationExtServiceName, svc)
	d := NewDispatcher(&fakeAuth{}, backend)
	rec := &recordingConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}
	d.addAuthedConn(rec)

	resp := callExtension(t, d, rec,
		`{"jsonrpc":"2.0","method":"automations.run","params":{"automationID":"`+automationID+`"},"id":1}`)
	result := automationExtResult[wire.AutomationExtRunResult](t, resp)
	if result.Run.ID != runID || result.Run.AutomationID != automationID {
		t.Fatalf("unexpected run: %+v", result.Run)
	}
	if svc.gotAutomationID != automationID {
		t.Fatalf("unexpected automationID: %q", svc.gotAutomationID)
	}
	if len(rec.notifications) != 1 || rec.notifications[0] != automationExtChangedMethod {
		t.Fatalf("unexpected notifications: %+v", rec.notifications)
	}
}

func TestAutomationsExtMarkReadAndArchive(t *testing.T) {
	backend := newAutomationExtTestBackend(t)
	runID := newUUID()
	svc := &fakeAutomationExtService{}
	backend.SetService(automationExtServiceName, svc)
	d := NewDispatcher(&fakeAuth{}, backend)
	rec := &recordingConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}
	d.addAuthedConn(rec)

	resp := callExtension(t, d, rec,
		`{"jsonrpc":"2.0","method":"automations.markRead","params":{"runID":"`+runID+`"},"id":1}`)
	marked := automationExtResult[wire.AutomationExtMarkReadResult](t, resp)
	if marked.RunID != runID || svc.gotRunID != runID {
		t.Fatalf("unexpected markRead result: %+v (svc runID %q)", marked, svc.gotRunID)
	}

	resp = callExtension(t, d, rec,
		`{"jsonrpc":"2.0","method":"automations.archiveRun","params":{"runID":"`+runID+`","archived":true},"id":2}`)
	archived := automationExtResult[wire.AutomationExtArchiveRunResult](t, resp)
	if !archived.Archived || archived.RunID != runID {
		t.Fatalf("unexpected archiveRun result: %+v", archived)
	}
	if svc.archiveCalls != 1 || !svc.gotArchived {
		t.Fatalf("archive not forwarded: calls=%d archived=%v", svc.archiveCalls, svc.gotArchived)
	}
	// 两次写操作各广播一次 automations.changed。
	if len(rec.notifications) != 2 || rec.notifications[0] != automationExtChangedMethod || rec.notifications[1] != automationExtChangedMethod {
		t.Fatalf("unexpected notifications: %+v", rec.notifications)
	}
}

func TestAutomationsExtInvalidParams(t *testing.T) {
	backend := newAutomationExtTestBackend(t)
	svc := &fakeAutomationExtService{}
	backend.SetService(automationExtServiceName, svc)
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	// maxRuns 越界的 create 参数：构造完整合法入参，只把 maxRuns 设成越界值。
	badMaxRunsCreate := func(v int) string {
		in := automationExtValidInput("夜间整理")
		in.MaxIterations = nil
		in.MaxRuns = &v
		raw, err := json.Marshal(wire.AutomationExtCreateParams{ProjectID: newUUID(), Input: in})
		if err != nil {
			t.Fatalf("marshal params: %v", err)
		}
		return string(raw)
	}

	cases := []struct {
		name   string
		method string
		params string
	}{
		{"badProjectID", "automations.list", `{"projectID":"proj-1"}`},
		{"unknownField", "automations.list", `{"projectID":"` + newUUID() + `","extra":1}`},
		{"badRunsAutomationID", "automations.runs", `{"automationID":"auto-1"}`},
		{"badRunAutomationID", "automations.run", `{"automationID":""}`},
		{"badMarkReadRunID", "automations.markRead", `{"runID":"run-1"}`},
		{"badArchiveRunID", "automations.archiveRun", `{"runID":""}`},
		{"negativeMaxRuns", "automations.create", badMaxRunsCreate(-1)},
		{"maxRunsTooLarge", "automations.create", badMaxRunsCreate(1001)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := callExtension(t, d, conn,
				`{"jsonrpc":"2.0","method":"`+tc.method+`","params":`+tc.params+`,"id":1}`)
			if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
				t.Fatalf("expected InvalidParams, got: %+v", resp.Error)
			}
		})
	}
	if svc.calls != 0 {
		t.Fatalf("service should not be called on invalid params, calls=%d", svc.calls)
	}
}

func TestAutomationsExtWithoutService(t *testing.T) {
	backend := newAutomationExtTestBackend(t)
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	methods := []string{
		`"automations.list","params":{"projectID":"` + newUUID() + `"}`,
		`"automations.runs","params":{"automationID":"` + newUUID() + `"}`,
		`"automations.run","params":{"automationID":"` + newUUID() + `"}`,
		`"automations.markRead","params":{"runID":"` + newUUID() + `"}`,
		`"automations.archiveRun","params":{"runID":"` + newUUID() + `","archived":true}`,
	}
	for _, call := range methods {
		resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":`+call+`,"id":1}`)
		if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
			t.Fatalf("%s: expected MethodNotFound, got: %+v", call, resp.Error)
		}
	}
}

func TestAutomationsExtServiceErrorKeepsMessage(t *testing.T) {
	backend := newAutomationExtTestBackend(t)
	svc := &fakeAutomationExtService{err: errors.New("自动化正在运行中")}
	backend.SetService(automationExtServiceName, svc)
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	resp := callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"automations.run","params":{"automationID":"`+newUUID()+`"},"id":1}`)
	if resp.Error == nil || resp.Error.Code != wire.ErrCodeOperationRejected {
		t.Fatalf("expected OperationRejected, got: %+v", resp.Error)
	}
	if resp.Error.Message != "自动化正在运行中" {
		t.Fatalf("error message should keep the original text, got %q", resp.Error.Message)
	}
}

func TestAutomationsExtCreate(t *testing.T) {
	backend := newAutomationExtTestBackend(t)
	projID := newUUID()
	backend.AddProject(wire.ProjectSummary{ID: projID, Name: "mcode", Path: "/tmp/mcode"})
	svc := &fakeAutomationExtService{}
	backend.SetService(automationExtServiceName, svc)
	d := NewDispatcher(&fakeAuth{}, backend)
	rec := &recordingConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}
	d.addAuthedConn(rec)

	in := automationExtValidInput("夜间整理")
	body, err := json.Marshal(wire.AutomationExtCreateParams{ProjectID: projID, Input: in})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	resp := callExtension(t, d, rec,
		`{"jsonrpc":"2.0","method":"automations.create","params":`+string(body)+`,"id":1}`)
	result := automationExtResult[wire.AutomationExtAutomationResult](t, resp)
	if svc.createCnt != 1 || svc.gotSubPID != projID {
		t.Fatalf("create not forwarded: calls=%d projectID=%q", svc.createCnt, svc.gotSubPID)
	}
	if svc.gotInput.Name != "夜间整理" || svc.gotInput.Schedule.TimeOfDay != "09:00" || !svc.gotInput.Enabled {
		t.Fatalf("unexpected input: %+v", svc.gotInput)
	}
	if svc.gotInput.MaxConsecutiveFailures == nil || *svc.gotInput.MaxConsecutiveFailures != 3 {
		t.Fatalf("unexpected maxConsecutiveFailures: %+v", svc.gotInput.MaxConsecutiveFailures)
	}
	if result.Automation.Name != "夜间整理" || result.Automation.ProjectID != projID {
		t.Fatalf("unexpected result: %+v", result.Automation)
	}
	if len(rec.notifications) != 1 || rec.notifications[0] != automationExtChangedMethod {
		t.Fatalf("unexpected notifications: %+v", rec.notifications)
	}
}

func TestAutomationsExtCreateRejectsUnknownProject(t *testing.T) {
	backend := newAutomationExtTestBackend(t)
	svc := &fakeAutomationExtService{}
	backend.SetService(automationExtServiceName, svc)
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	body, err := json.Marshal(wire.AutomationExtCreateParams{ProjectID: newUUID(), Input: automationExtValidInput("夜间整理")})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	resp := callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"automations.create","params":`+string(body)+`,"id":1}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams, got: %+v", resp.Error)
	}
	if svc.createCnt != 0 {
		t.Fatalf("service should not be called for unknown project, calls=%d", svc.createCnt)
	}
}

func TestAutomationsExtUpdate(t *testing.T) {
	backend := newAutomationExtTestBackend(t)
	automationID := newUUID()
	svc := &fakeAutomationExtService{}
	backend.SetService(automationExtServiceName, svc)
	d := NewDispatcher(&fakeAuth{}, backend)
	rec := &recordingConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}
	d.addAuthedConn(rec)

	body, err := json.Marshal(wire.AutomationExtUpdateParams{AutomationID: automationID, Input: automationExtValidInput("改名后"), Revision: 7})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	resp := callExtension(t, d, rec,
		`{"jsonrpc":"2.0","method":"automations.update","params":`+string(body)+`,"id":1}`)
	result := automationExtResult[wire.AutomationExtAutomationResult](t, resp)
	if svc.updateCnt != 1 || svc.gotAutomationID != automationID || svc.gotRev != 7 {
		t.Fatalf("update not forwarded: calls=%d id=%q revision=%d", svc.updateCnt, svc.gotAutomationID, svc.gotRev)
	}
	if svc.gotInput.Name != "改名后" {
		t.Fatalf("unexpected input: %+v", svc.gotInput)
	}
	if result.Automation.Name != "改名后" {
		t.Fatalf("unexpected result: %+v", result.Automation)
	}
	if len(rec.notifications) != 1 || rec.notifications[0] != automationExtChangedMethod {
		t.Fatalf("unexpected notifications: %+v", rec.notifications)
	}
}

func TestAutomationsExtUpdateConflict(t *testing.T) {
	backend := newAutomationExtTestBackend(t)
	automationID := newUUID()
	svc := &fakeAutomationExtService{err: fmt.Errorf("%w", ErrAutomationExtConflict)}
	backend.SetService(automationExtServiceName, svc)
	d := NewDispatcher(&fakeAuth{}, backend)
	rec := &recordingConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}
	d.addAuthedConn(rec)

	body, err := json.Marshal(wire.AutomationExtUpdateParams{AutomationID: automationID, Input: automationExtValidInput("改名后"), Revision: 1})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	resp := callExtension(t, d, rec,
		`{"jsonrpc":"2.0","method":"automations.update","params":`+string(body)+`,"id":1}`)
	if resp.Error == nil || resp.Error.Code != wire.ErrCodeVersionConflict {
		t.Fatalf("expected VersionConflict, got: %+v", resp.Error)
	}
	if len(rec.notifications) != 0 {
		t.Fatalf("conflict should not broadcast, got: %+v", rec.notifications)
	}
}

func TestAutomationsExtDeleteRequiresConfirm(t *testing.T) {
	backend := newAutomationExtTestBackend(t)
	automationID := newUUID()
	svc := &fakeAutomationExtService{}
	backend.SetService(automationExtServiceName, svc)
	d := NewDispatcher(&fakeAuth{}, backend)
	rec := &recordingConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}
	d.addAuthedConn(rec)

	resp := callExtension(t, d, rec,
		`{"jsonrpc":"2.0","method":"automations.delete","params":{"automationID":"`+automationID+`"},"id":1}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams without confirm, got: %+v", resp.Error)
	}
	if svc.deleteCnt != 0 {
		t.Fatalf("delete should not be forwarded without confirm, calls=%d", svc.deleteCnt)
	}

	resp = callExtension(t, d, rec,
		`{"jsonrpc":"2.0","method":"automations.delete","params":{"automationID":"`+automationID+`","confirm":true},"id":2}`)
	result := automationExtResult[wire.AutomationExtDeleteResult](t, resp)
	if !result.Deleted || result.AutomationID != automationID {
		t.Fatalf("unexpected result: %+v", result)
	}
	if svc.deleteCnt != 1 || svc.gotAutomationID != automationID {
		t.Fatalf("delete not forwarded: calls=%d id=%q", svc.deleteCnt, svc.gotAutomationID)
	}
	if len(rec.notifications) != 1 || rec.notifications[0] != automationExtChangedMethod {
		t.Fatalf("unexpected notifications: %+v", rec.notifications)
	}
}

func TestAutomationsExtSetEnabledAndReset(t *testing.T) {
	backend := newAutomationExtTestBackend(t)
	automationID := newUUID()
	svc := &fakeAutomationExtService{}
	backend.SetService(automationExtServiceName, svc)
	d := NewDispatcher(&fakeAuth{}, backend)
	rec := &recordingConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}
	d.addAuthedConn(rec)

	resp := callExtension(t, d, rec,
		`{"jsonrpc":"2.0","method":"automations.setEnabled","params":{"automationID":"`+automationID+`","enabled":false,"revision":4},"id":1}`)
	result := automationExtResult[wire.AutomationExtAutomationResult](t, resp)
	if svc.setOnCnt != 1 || svc.gotOn || svc.gotRev != 4 {
		t.Fatalf("setEnabled not forwarded: calls=%d enabled=%v revision=%d", svc.setOnCnt, svc.gotOn, svc.gotRev)
	}
	if result.Automation.ID != automationID || result.Automation.Enabled {
		t.Fatalf("unexpected result: %+v", result.Automation)
	}

	// reset 等价于 setEnabled(true)，桌面端重置也没有二次确认。
	resp = callExtension(t, d, rec,
		`{"jsonrpc":"2.0","method":"automations.reset","params":{"automationID":"`+automationID+`"},"id":2}`)
	reset := automationExtResult[wire.AutomationExtAutomationResult](t, resp)
	if svc.setOnCnt != 2 || !svc.gotOn {
		t.Fatalf("reset should enable the automation: calls=%d enabled=%v", svc.setOnCnt, svc.gotOn)
	}
	if !reset.Automation.Enabled {
		t.Fatalf("unexpected reset result: %+v", reset.Automation)
	}
	if len(rec.notifications) != 2 {
		t.Fatalf("unexpected notifications: %+v", rec.notifications)
	}
}

func TestAutomationsExtWriteRejectsInvalidInput(t *testing.T) {
	backend := newAutomationExtTestBackend(t)
	projID := newUUID()
	backend.AddProject(wire.ProjectSummary{ID: projID, Name: "mcode", Path: "/tmp/mcode"})
	svc := &fakeAutomationExtService{}
	backend.SetService(automationExtServiceName, svc)
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}
	automationID := newUUID()

	mutate := func(f func(in *wire.AutomationExtInput)) wire.AutomationExtInput {
		in := automationExtValidInput("夜间整理")
		f(&in)
		return in
	}
	// inputJSON 只序列化 input 本身，create/update 的顶层参数各自拼装，
	// 避免把整段 create 参数当成 update 的 input 嵌进去（那样测的是解码失败，不是字段校验）。
	inputJSON := func(in wire.AutomationExtInput) string {
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatalf("marshal input: %v", err)
		}
		return string(raw)
	}
	createBody := func(in wire.AutomationExtInput, extra string) string {
		return `{"projectID":"` + projID + `","input":` + inputJSON(in) + extra + `}`
	}
	updateBody := func(in wire.AutomationExtInput, extra string) string {
		return `{"automationID":"` + automationID + `","input":` + inputJSON(in) + extra + `}`
	}

	cases := []struct {
		name   string
		method string
		params string
	}{
		{"emptyName", "automations.create", createBody(mutate(func(in *wire.AutomationExtInput) { in.Name = "   " }), "")},
		{"nameTooLong", "automations.create", createBody(mutate(func(in *wire.AutomationExtInput) { in.Name = strings.Repeat("长", automationExtMaxNameRunes+1) }), "")},
		{"emptyPrompt", "automations.create", createBody(mutate(func(in *wire.AutomationExtInput) { in.Prompt = "" }), "")},
		{"promptTooLong", "automations.create", createBody(mutate(func(in *wire.AutomationExtInput) { in.Prompt = strings.Repeat("p", automationExtMaxPromptRunes+1) }), "")},
		{"stopWhenTooLong", "automations.create", createBody(mutate(func(in *wire.AutomationExtInput) { in.StopWhen = strings.Repeat("s", automationExtMaxStopWhenRunes+1) }), "")},
		{"zeroInterval", "automations.create", createBody(mutate(func(in *wire.AutomationExtInput) { in.Schedule = wire.AutomationExtSchedule{Type: "interval"} }), "")},
		{"badTimeOfDay", "automations.create", createBody(mutate(func(in *wire.AutomationExtInput) { in.Schedule.TimeOfDay = "9:00" }), "")},
		{"badWeekday", "automations.create", createBody(mutate(func(in *wire.AutomationExtInput) {
			in.Schedule = wire.AutomationExtSchedule{Type: "weekly", TimeOfDay: "09:00", DayOfWeek: 7}
		}), "")},
		{"badCron", "automations.create", createBody(mutate(func(in *wire.AutomationExtInput) {
			in.Schedule = wire.AutomationExtSchedule{Type: "cron", Expression: "0 9 * *"}
		}), "")},
		{"badOnceDate", "automations.create", createBody(mutate(func(in *wire.AutomationExtInput) {
			in.Schedule = wire.AutomationExtSchedule{Type: "once", Date: "明天九点"}
		}), "")},
		{"badTimezone", "automations.create", createBody(mutate(func(in *wire.AutomationExtInput) { in.Schedule.Timezone = "Mars/Olympus" }), "")},
		{"badScheduleType", "automations.create", createBody(mutate(func(in *wire.AutomationExtInput) { in.Schedule = wire.AutomationExtSchedule{Type: "hourly"} }), "")},
		{"badProjectID", "automations.create", `{"projectID":"proj-1","input":` + inputJSON(automationExtValidInput("夜间整理")) + `}`},
		{"unknownField", "automations.create", createBody(automationExtValidInput("夜间整理"), `,"extra":1`)},
		{"updateUnknownField", "automations.update", updateBody(automationExtValidInput("夜间整理"), `,"extra":1`)},
		{"badUpdateName", "automations.update", updateBody(mutate(func(in *wire.AutomationExtInput) { in.Name = "" }), "")},
		{"negativeRevision", "automations.update", updateBody(automationExtValidInput("夜间整理"), `,"revision":-1`)},
		{"badUpdateID", "automations.update", `{"automationID":"auto-1","input":` + inputJSON(automationExtValidInput("夜间整理")) + `}`},
		{"badDeleteID", "automations.delete", `{"automationID":"auto-1","confirm":true}`},
		{"badSetEnabledID", "automations.setEnabled", `{"automationID":"","enabled":true}`},
		{"badResetID", "automations.reset", `{"automationID":"auto-1"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := callExtension(t, d, conn,
				`{"jsonrpc":"2.0","method":"`+tc.method+`","params":`+tc.params+`,"id":1}`)
			if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
				t.Fatalf("expected InvalidParams, got: %+v", resp.Error)
			}
		})
	}
	if svc.createCnt != 0 || svc.updateCnt != 0 || svc.deleteCnt != 0 || svc.setOnCnt != 0 {
		t.Fatalf("service should not be called on invalid input: %d/%d/%d/%d", svc.createCnt, svc.updateCnt, svc.deleteCnt, svc.setOnCnt)
	}
}

func TestAutomationsExtWriteWithoutService(t *testing.T) {
	backend := newAutomationExtTestBackend(t)
	projID := newUUID()
	backend.AddProject(wire.ProjectSummary{ID: projID, Name: "mcode", Path: "/tmp/mcode"})
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	body, err := json.Marshal(wire.AutomationExtCreateParams{ProjectID: projID, Input: automationExtValidInput("夜间整理")})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	// update 的参数结构是 {automationID, input, revision}，不能把 create 的整段参数塞进 input，
	// 否则测到的是解码失败，而不是「服务未注入」。
	updateBody, err := json.Marshal(wire.AutomationExtUpdateParams{AutomationID: newUUID(), Input: automationExtValidInput("夜间整理")})
	if err != nil {
		t.Fatalf("marshal update params: %v", err)
	}
	methods := []string{
		`"automations.create","params":` + string(body),
		`"automations.update","params":` + string(updateBody),
		`"automations.delete","params":{"automationID":"` + newUUID() + `","confirm":true}`,
		`"automations.setEnabled","params":{"automationID":"` + newUUID() + `","enabled":false}`,
		`"automations.reset","params":{"automationID":"` + newUUID() + `"}`,
	}
	for _, call := range methods {
		resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":`+call+`,"id":1}`)
		if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
			t.Fatalf("%s: expected MethodNotFound, got: %+v", call, resp.Error)
		}
	}
}

func TestAutomationsExtCreateKeepsEnabledFalse(t *testing.T) {
	backend := newAutomationExtTestBackend(t)
	projID := newUUID()
	backend.AddProject(wire.ProjectSummary{ID: projID, Name: "mcode", Path: "/tmp/mcode"})
	svc := &fakeAutomationExtService{}
	backend.SetService(automationExtServiceName, svc)
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	in := automationExtValidInput("先禁用")
	in.Enabled = false
	in.MaxConsecutiveFailures = nil
	body, err := json.Marshal(wire.AutomationExtCreateParams{ProjectID: projID, Input: in})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	resp := callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"automations.create","params":`+string(body)+`,"id":1}`)
	automationExtResult[wire.AutomationExtAutomationResult](t, resp)
	if svc.gotInput.Enabled {
		t.Fatalf("enabled=false should be kept, got %+v", svc.gotInput)
	}
	// 省略 maxConsecutiveFailures 时补桌面端默认值 3，maxIterations 补 0。
	if svc.gotInput.MaxConsecutiveFailures == nil || *svc.gotInput.MaxConsecutiveFailures != 3 {
		t.Fatalf("unexpected default failures: %+v", svc.gotInput.MaxConsecutiveFailures)
	}
	if svc.gotInput.MaxIterations == nil || *svc.gotInput.MaxIterations != 0 {
		t.Fatalf("unexpected default iterations: %+v", svc.gotInput.MaxIterations)
	}
}

// automationExtCallCreate 用给定入参发起 automations.create，返回原始响应。
func automationExtCallCreate(t *testing.T, d *Dispatcher, conn Conn, projectID string, in wire.AutomationExtInput) wire.Response {
	t.Helper()
	body, err := json.Marshal(wire.AutomationExtCreateParams{ProjectID: projectID, Input: in})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return callExtension(t, d, conn,
		`{"jsonrpc":"2.0","method":"automations.create","params":`+string(body)+`,"id":1}`)
}

// TestAutomationsExtCreateMaxRunsAndCatchUp 验证新客户端只传 maxRuns/catchUp 的路径：
// 规范字段生效，旧字段被镜像；catchUp 省略时按开启处理。
func TestAutomationsExtCreateMaxRunsAndCatchUp(t *testing.T) {
	backend := newAutomationExtTestBackend(t)
	projID := newUUID()
	backend.AddProject(wire.ProjectSummary{ID: projID, Name: "mcode", Path: "/tmp/mcode"})
	svc := &fakeAutomationExtService{}
	backend.SetService(automationExtServiceName, svc)
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	maxRuns := 200
	catchUp := false
	in := automationExtValidInput("新客户端")
	in.MaxIterations = nil
	in.MaxRuns = &maxRuns
	in.CatchUp = &catchUp
	resp := automationExtCallCreate(t, d, conn, projID, in)
	automationExtResult[wire.AutomationExtAutomationResult](t, resp)
	if svc.gotInput.MaxRuns == nil || *svc.gotInput.MaxRuns != 200 {
		t.Fatalf("maxRuns 未透传: %+v", svc.gotInput.MaxRuns)
	}
	// 镜像写入旧字段，保证旧版本二进制仍能按 maxIterations 拦截。
	if svc.gotInput.MaxIterations == nil || *svc.gotInput.MaxIterations != 200 {
		t.Fatalf("maxIterations 未镜像: %+v", svc.gotInput.MaxIterations)
	}
	if svc.gotInput.CatchUp == nil || *svc.gotInput.CatchUp {
		t.Fatalf("catchUp=false 未透传: %+v", svc.gotInput.CatchUp)
	}

	// 两者都省略时：maxRuns/maxIterations 为 0，catchUp 默认开启。
	defaultIn := automationExtValidInput("默认值")
	defaultIn.MaxIterations = nil
	resp = automationExtCallCreate(t, d, conn, projID, defaultIn)
	automationExtResult[wire.AutomationExtAutomationResult](t, resp)
	if svc.gotInput.MaxRuns == nil || *svc.gotInput.MaxRuns != 0 {
		t.Fatalf("省略 maxRuns 时应回填 0: %+v", svc.gotInput.MaxRuns)
	}
	if svc.gotInput.CatchUp == nil || !*svc.gotInput.CatchUp {
		t.Fatalf("省略 catchUp 时应默认开启: %+v", svc.gotInput.CatchUp)
	}
}

// TestAutomationsExtMaxRunsLegacyFallback 验证旧客户端只传 maxIterations 时回填 maxRuns，
// 且 200 这类超过旧上限 50 的值不再被拒。
func TestAutomationsExtMaxRunsLegacyFallback(t *testing.T) {
	backend := newAutomationExtTestBackend(t)
	projID := newUUID()
	backend.AddProject(wire.ProjectSummary{ID: projID, Name: "mcode", Path: "/tmp/mcode"})
	svc := &fakeAutomationExtService{}
	backend.SetService(automationExtServiceName, svc)
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	iterations := 200
	in := automationExtValidInput("旧客户端")
	in.MaxIterations = &iterations
	in.MaxRuns = nil
	resp := automationExtCallCreate(t, d, conn, projID, in)
	automationExtResult[wire.AutomationExtAutomationResult](t, resp)
	if svc.gotInput.MaxRuns == nil || *svc.gotInput.MaxRuns != 200 {
		t.Fatalf("maxRuns 未从 maxIterations 回填: %+v", svc.gotInput.MaxRuns)
	}
}

// TestAutomationsExtRejectsConflictingMaxRuns 验证新旧字段同时出现且不一致时报参数错误。
func TestAutomationsExtRejectsConflictingMaxRuns(t *testing.T) {
	backend := newAutomationExtTestBackend(t)
	projID := newUUID()
	backend.AddProject(wire.ProjectSummary{ID: projID, Name: "mcode", Path: "/tmp/mcode"})
	svc := &fakeAutomationExtService{}
	backend.SetService(automationExtServiceName, svc)
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	iterations := 6
	maxRuns := 5
	in := automationExtValidInput("冲突")
	in.MaxIterations = &iterations
	in.MaxRuns = &maxRuns
	resp := automationExtCallCreate(t, d, conn, projID, in)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams, got: %+v", resp.Error)
	}
	if svc.createCnt != 0 {
		t.Fatalf("冲突时不应调用服务, calls=%d", svc.createCnt)
	}
}

// TestAutomationsExtMaxIterationsAccepts1000 验证旧字段上限已提到 1000。
func TestAutomationsExtMaxIterationsAccepts1000(t *testing.T) {
	backend := newAutomationExtTestBackend(t)
	projID := newUUID()
	backend.AddProject(wire.ProjectSummary{ID: projID, Name: "mcode", Path: "/tmp/mcode"})
	svc := &fakeAutomationExtService{}
	backend.SetService(automationExtServiceName, svc)
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	iterations := 1000
	in := automationExtValidInput("旧上限")
	in.MaxIterations = &iterations
	in.MaxRuns = nil
	resp := automationExtCallCreate(t, d, conn, projID, in)
	automationExtResult[wire.AutomationExtAutomationResult](t, resp)
	if svc.gotInput.MaxIterations == nil || *svc.gotInput.MaxIterations != 1000 ||
		svc.gotInput.MaxRuns == nil || *svc.gotInput.MaxRuns != 1000 {
		t.Fatalf("1000 未被接受: %+v", svc.gotInput)
	}
}
