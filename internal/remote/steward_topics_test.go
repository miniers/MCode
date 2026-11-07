package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// TestStewardTopicItemsPhaseMapping 覆盖目标状态到话题阶段的映射与任务进度。
func TestStewardTopicItemsPhaseMapping(t *testing.T) {
	raw := `[
		{"threadId":"t-draft","projectId":"p1","title":"追问","brief":"任务书","openQuestions":2,"updatedAt":"2026-10-31T09:00:00Z"},
		{"threadId":"t-planning","projectId":"p1","title":"规划","goalId":"g-planning","updatedAt":"2026-10-31T08:00:00Z"},
		{"threadId":"t-plan-failed","projectId":"p1","title":"规划失败","goalId":"g-plan-failed","updatedAt":"2026-10-31T07:00:00Z"},
		{"threadId":"t-plan-review","projectId":"p1","title":"审计划","goalId":"g-plan-review","updatedAt":"2026-10-31T06:00:00Z"},
		{"threadId":"t-running","projectId":"p1","title":"执行","goalId":"g-running","updatedAt":"2026-10-31T05:00:00Z"},
		{"threadId":"t-paused","projectId":"p1","title":"暂停","goalId":"g-paused","updatedAt":"2026-10-31T04:00:00Z"},
		{"threadId":"t-completed","projectId":"p1","title":"完成","goalId":"g-completed","updatedAt":"2026-10-31T03:00:00Z"},
		{"threadId":"t-cancelled","projectId":"p1","title":"取消","goalId":"g-cancelled","updatedAt":"2026-10-31T02:00:00Z"},
		{"threadId":"t-unknown","projectId":"p1","title":"未知","goalId":"g-unknown","updatedAt":"2026-10-31T01:00:00Z"}
	]`
	goals := []store.Goal{
		{ID: "g-planning", Status: "planning"},
		{ID: "g-plan-failed", Status: "plan_failed"},
		{ID: "g-plan-review", Status: "plan_review"},
		{ID: "g-running", Status: "running", Progress: map[string]any{"done": float64(2), "total": float64(5)}},
		{ID: "g-paused", Status: "paused"},
		{ID: "g-completed", Status: "completed"},
		{ID: "g-cancelled", Status: "cancelled"},
		{ID: "g-unknown", Status: "weird"},
	}
	items, err := stewardTopicItems(raw, goals)
	if err != nil {
		t.Fatalf("stewardTopicItems: %v", err)
	}
	if len(items) != 9 {
		t.Fatalf("want 9 topics, got %d", len(items))
	}
	// 列表按更新时间倒序。
	if items[0].ThreadID != "t-draft" {
		t.Fatalf("want newest first, got %s", items[0].ThreadID)
	}
	byThread := make(map[string]wire.StewardTopicItem, len(items))
	for _, item := range items {
		byThread[item.ThreadID] = item
	}
	cases := []struct {
		threadID string
		phase    string
		done     int
		total    int
	}{
		{"t-draft", stewardPhaseDrafting, 0, 0},
		{"t-planning", stewardPhasePlanning, 0, 0},
		{"t-plan-failed", stewardPhasePlanFailed, 0, 0},
		{"t-plan-review", stewardPhasePlanReview, 0, 0},
		{"t-running", stewardPhaseRunning, 2, 5},
		{"t-paused", stewardPhasePaused, 0, 0},
		{"t-completed", stewardPhaseCompleted, 0, 0},
		{"t-cancelled", stewardPhaseCancelled, 0, 0},
		{"t-unknown", stewardPhaseDrafting, 0, 0},
	}
	for _, tc := range cases {
		got := byThread[tc.threadID]
		if got.Phase != tc.phase || got.TasksDone != tc.done || got.TasksTotal != tc.total {
			t.Errorf("%s: got phase=%s done=%d total=%d, want phase=%s done=%d total=%d",
				tc.threadID, got.Phase, got.TasksDone, got.TasksTotal, tc.phase, tc.done, tc.total)
		}
	}
	// updatedAt 统一转成 UTC RFC3339。
	if items[0].UpdatedAt != "2026-10-31T09:00:00Z" {
		t.Errorf("unexpected updatedAt: %s", items[0].UpdatedAt)
	}
	if items[0].OpenQuestions != 2 || items[0].Brief != "任务书" {
		t.Errorf("topic fields not preserved: %+v", items[0])
	}
}

// TestStewardTopicsListRPC 从设置键与目标表读取话题，并按项目过滤。
func TestStewardTopicsListRPC(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "steward-topics.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	if err := st.SetSetting(ctx, stewardTopicsSettingKey, `[
		{"threadId":"t1","projectId":"p1","title":"升级登录","openQuestions":1,"updatedAt":"2026-10-31T09:00:00Z"},
		{"threadId":"t2","projectId":"p2","title":"修构建","goalId":"g2","closed":true,"updatedAt":"2026-10-31T08:00:00Z"}
	]`); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := st.CreateGoal(ctx, store.Goal{
		ID: "g2", ProjectID: "p2", Title: "修构建", Prompt: "修构建", Status: "running",
		Progress: map[string]any{"done": 1, "total": 3},
	}); err != nil {
		t.Fatalf("CreateGoal: %v", err)
	}

	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	if err := backend.AttachStore(ctx, st); err != nil {
		t.Fatalf("AttachStore: %v", err)
	}
	d, _ := newMemoryExtTestDispatcher(t, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn, fmt.Sprintf(
		`{"jsonrpc":"2.0","method":%q,"params":{"projectID":"p2"},"id":1}`, stewardTopicsListMethod))
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var result wire.StewardTopicsListResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(result.Topics) != 1 {
		t.Fatalf("want 1 topic for p2, got %d", len(result.Topics))
	}
	topic := result.Topics[0]
	if topic.ThreadID != "t2" || topic.Phase != stewardPhaseRunning || topic.TasksDone != 1 || topic.TasksTotal != 3 || !topic.Closed {
		t.Fatalf("unexpected topic: %+v", topic)
	}
}

// stewardExtTestTopics 是写操作测试用的话题表：p1 下有一条话题，p2 下另有一条。
const stewardExtTestTopics = `[
	{"threadId":"topic-1","projectId":"p1","title":"升级登录","updatedAt":"2026-10-31T09:00:00Z"},
	{"threadId":"topic-2","projectId":"p2","title":"修构建","updatedAt":"2026-10-31T08:00:00Z"}
]`

// fakeStewardExtService 是注入用的假总管服务，记录收到的参数并可按需返回错误。
type fakeStewardExtService struct {
	openProjectID string
	openThreadID  string
	openErr       error

	startProjectID string
	startThreadID  string
	startRequest   string
	startNewID     string
	startErr       error

	// topicCalls 按动作名记录收到的 threadID。
	topicCalls map[string]string
	topicErr   error

	reviewThreadID string
	reviewTaskID   string
	reviewDecision string
	reviewFeedback string
	reviewRevision int
	reviewErr      error
}

func (f *fakeStewardExtService) Open(ctx context.Context, projectID string) (string, error) {
	f.openProjectID = projectID
	return f.openThreadID, f.openErr
}

func (f *fakeStewardExtService) StartTopic(ctx context.Context, projectID, threadID, request string) (string, error) {
	f.startProjectID = projectID
	f.startThreadID = threadID
	f.startRequest = request
	return f.startNewID, f.startErr
}

func (f *fakeStewardExtService) recordTopic(action, threadID string) error {
	if f.topicCalls == nil {
		f.topicCalls = make(map[string]string, 4)
	}
	f.topicCalls[action] = threadID
	return f.topicErr
}

func (f *fakeStewardExtService) ReplanTopic(ctx context.Context, threadID string) error {
	return f.recordTopic(stewardTopicsReplanMethod, threadID)
}

func (f *fakeStewardExtService) ConfirmTopic(ctx context.Context, threadID string) error {
	return f.recordTopic(stewardTopicsConfirmMethod, threadID)
}

func (f *fakeStewardExtService) CloseTopic(ctx context.Context, threadID string) error {
	return f.recordTopic(stewardTopicsCloseMethod, threadID)
}

func (f *fakeStewardExtService) DeleteTopic(ctx context.Context, threadID string) error {
	return f.recordTopic(stewardTopicsDeleteMethod, threadID)
}

func (f *fakeStewardExtService) ReviewTask(ctx context.Context, threadID, taskID, decision, feedback string, revision int) error {
	f.reviewThreadID = threadID
	f.reviewTaskID = taskID
	f.reviewDecision = decision
	f.reviewFeedback = feedback
	f.reviewRevision = revision
	return f.reviewErr
}

// newStewardExtTestDispatcher 造一个挂了话题表的 MemoryBackend 与 Dispatcher，
// 并在注入服务后返回已认证的录通知连接（notification 都落在它身上）。
func newStewardExtTestDispatcher(t *testing.T, svc stewardExtService) (*Dispatcher, *memoryExtRecordingConn) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "steward-ext.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.SetSetting(ctx, stewardTopicsSettingKey, stewardExtTestTopics); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	t.Cleanup(hub.Close)
	backend := NewMemoryBackend(hub)
	t.Cleanup(backend.Close)
	if err := backend.AttachStore(ctx, st); err != nil {
		t.Fatalf("AttachStore: %v", err)
	}
	if svc != nil {
		backend.SetService(stewardExtServiceName, svc)
	}
	return newMemoryExtTestDispatcher(t, backend)
}

// TestStewardOpenSuccess 校验 steward.open 透传 projectID 并返回总管会话 ID。
func TestStewardOpenSuccess(t *testing.T) {
	svc := &fakeStewardExtService{openThreadID: "steward-thread"}
	d, rec := newStewardExtTestDispatcher(t, svc)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn, fmt.Sprintf(
		`{"jsonrpc":"2.0","method":%q,"params":{"projectID":"p1"},"id":1}`, stewardOpenMethod))
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if svc.openProjectID != "p1" {
		t.Fatalf("projectID 未透传: %q", svc.openProjectID)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var result wire.StewardOpenResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.ThreadID != "steward-thread" {
		t.Fatalf("threadID = %q", result.ThreadID)
	}
	// steward.open 只是读取会话，不广播变化。
	if len(rec.notifications) != 0 {
		t.Fatalf("open 不该广播，得到 %+v", rec.notifications)
	}
}

// TestStewardTopicsStartSuccess 校验开话题的参数透传、结果字段与变更广播。
func TestStewardTopicsStartSuccess(t *testing.T) {
	svc := &fakeStewardExtService{startNewID: "topic-new"}
	d, rec := newStewardExtTestDispatcher(t, svc)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"steward.topics.start",`+
		`"params":{"projectID":"p1","threadID":"topic-1","request":"把登录改成扫码"},"id":1}`)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if svc.startProjectID != "p1" || svc.startThreadID != "topic-1" || svc.startRequest != "把登录改成扫码" {
		t.Fatalf("参数未透传: %+v", svc)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var result wire.StewardTopicsStartResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.ThreadID != "topic-new" || result.Queued {
		t.Fatalf("unexpected result: %+v", result)
	}
	assertStewardChanged(t, rec, "p1")
}

// TestStewardTopicActionsSuccess 覆盖 replan/confirm/close/delete 四条写路径的参数透传与广播。
func TestStewardTopicActionsSuccess(t *testing.T) {
	methods := []string{
		stewardTopicsReplanMethod,
		stewardTopicsConfirmMethod,
		stewardTopicsCloseMethod,
		stewardTopicsDeleteMethod,
	}
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			svc := &fakeStewardExtService{}
			d, rec := newStewardExtTestDispatcher(t, svc)
			conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

			resp := callExtension(t, d, conn, fmt.Sprintf(
				`{"jsonrpc":"2.0","method":%q,"params":{"projectID":"p1","threadID":"topic-1"},"id":1}`, method))
			if resp.Error != nil {
				t.Fatalf("unexpected error: %+v", resp.Error)
			}
			if got := svc.topicCalls[method]; got != "topic-1" {
				t.Fatalf("threadID 未透传: %q", got)
			}
			raw, err := json.Marshal(resp.Result)
			if err != nil {
				t.Fatalf("marshal result: %v", err)
			}
			if string(raw) != "{}" {
				t.Fatalf("结果应为空对象，得到 %s", raw)
			}
			assertStewardChanged(t, rec, "p1")
		})
	}
}

// TestStewardTopicTaskReviewSuccess 校验验收通过路径的参数透传。
func TestStewardTopicTaskReviewSuccess(t *testing.T) {
	svc := &fakeStewardExtService{}
	d, rec := newStewardExtTestDispatcher(t, svc)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"steward.topics.tasks.review",`+
		`"params":{"projectID":"p1","threadID":"topic-1","taskID":"task-9","decision":"accept","revision":7},"id":1}`)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if svc.reviewThreadID != "topic-1" || svc.reviewTaskID != "task-9" || svc.reviewDecision != "accept" ||
		svc.reviewFeedback != "" || svc.reviewRevision != 7 {
		t.Fatalf("参数未透传: %+v", svc)
	}
	assertStewardChanged(t, rec, "p1")
}

// TestStewardTopicTaskReviewRejectNeedsFeedback 校验 decision 白名单与打回必带理由。
func TestStewardTopicTaskReviewRejectNeedsFeedback(t *testing.T) {
	svc := &fakeStewardExtService{}
	d, rec := newStewardExtTestDispatcher(t, svc)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"steward.topics.tasks.review",`+
		`"params":{"projectID":"p1","threadID":"topic-1","taskID":"task-9","decision":"reject","revision":7},"id":1}`)
	assertStewardError(t, resp, wire.InvalidParams)
	if svc.reviewTaskID != "" {
		t.Fatalf("参数不合法时不该调服务，得到 %+v", svc)
	}

	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"steward.topics.tasks.review",`+
		`"params":{"projectID":"p1","threadID":"topic-1","taskID":"task-9","decision":"maybe","feedback":"再改改"},"id":1}`)
	assertStewardError(t, resp, wire.InvalidParams)

	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"steward.topics.tasks.review",`+
		`"params":{"projectID":"p1","threadID":"topic-1","taskID":"task-9","decision":"reject","feedback":" 少个测试  ","revision":7},"id":1}`)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if svc.reviewDecision != "reject" || svc.reviewFeedback != "少个测试" || svc.reviewRevision != 7 {
		t.Fatalf("打回参数未透传: %+v", svc)
	}
	assertStewardChanged(t, rec, "p1")
}

// TestStewardMethodsRejectMissingParams 校验缺参数被拒绝。
func TestStewardMethodsRejectMissingParams(t *testing.T) {
	svc := &fakeStewardExtService{}
	d, _ := newStewardExtTestDispatcher(t, svc)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	cases := []string{
		fmt.Sprintf(`{"jsonrpc":"2.0","method":%q,"params":{},"id":1}`, stewardOpenMethod),
		`{"jsonrpc":"2.0","method":"steward.topics.start","params":{"projectID":"p1"},"id":1}`,
		`{"jsonrpc":"2.0","method":"steward.topics.start","params":{"request":"干活"},"id":1}`,
		`{"jsonrpc":"2.0","method":"steward.topics.replan","params":{"projectID":"p1"},"id":1}`,
		`{"jsonrpc":"2.0","method":"steward.topics.confirm","params":{"threadID":"topic-1"},"id":1}`,
		`{"jsonrpc":"2.0","method":"steward.topics.close","params":{"projectID":"p1"},"id":1}`,
		`{"jsonrpc":"2.0","method":"steward.topics.delete","params":{},"id":1}`,
		`{"jsonrpc":"2.0","method":"steward.topics.tasks.review","params":{"projectID":"p1","threadID":"topic-1","decision":"accept"},"id":1}`,
	}
	for _, raw := range cases {
		assertStewardError(t, callExtension(t, d, conn, raw), wire.InvalidParams)
	}
	if len(svc.topicCalls) != 0 || svc.openProjectID != "" || svc.startRequest != "" {
		t.Fatalf("参数不合法时不该调服务，得到 %+v", svc)
	}
}

// TestStewardMethodsRejectUnknownFields 校验未知字段被严格解码拒绝。
func TestStewardMethodsRejectUnknownFields(t *testing.T) {
	svc := &fakeStewardExtService{}
	d, _ := newStewardExtTestDispatcher(t, svc)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	cases := []string{
		fmt.Sprintf(`{"jsonrpc":"2.0","method":%q,"params":{"projectID":"p1","unexpected":1},"id":1}`, stewardOpenMethod),
		`{"jsonrpc":"2.0","method":"steward.topics.start","params":{"projectID":"p1","request":"干活","unexpected":1},"id":1}`,
		`{"jsonrpc":"2.0","method":"steward.topics.replan","params":{"projectID":"p1","threadID":"topic-1","unexpected":1},"id":1}`,
		`{"jsonrpc":"2.0","method":"steward.topics.tasks.review","params":{"projectID":"p1","threadID":"topic-1","taskID":"task-9","decision":"accept","unexpected":1},"id":1}`,
	}
	for _, raw := range cases {
		assertStewardError(t, callExtension(t, d, conn, raw), wire.InvalidParams)
	}
}

// TestStewardMethodsRejectNonStewardTopic 校验不属于该项目或不是话题的 threadID 被拒绝。
func TestStewardMethodsRejectNonStewardTopic(t *testing.T) {
	svc := &fakeStewardExtService{}
	d, _ := newStewardExtTestDispatcher(t, svc)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	cases := []string{
		// 会话不是话题。
		`{"jsonrpc":"2.0","method":"steward.topics.replan","params":{"projectID":"p1","threadID":"thread-random"},"id":1}`,
		// 话题属于别的项目。
		`{"jsonrpc":"2.0","method":"steward.topics.confirm","params":{"projectID":"p1","threadID":"topic-2"},"id":1}`,
		`{"jsonrpc":"2.0","method":"steward.topics.start","params":{"projectID":"p1","threadID":"topic-2","request":"干活"},"id":1}`,
		`{"jsonrpc":"2.0","method":"steward.topics.tasks.review","params":{"projectID":"p1","threadID":"thread-random","taskID":"task-9","decision":"accept"},"id":1}`,
	}
	for _, raw := range cases {
		assertStewardError(t, callExtension(t, d, conn, raw), wire.InvalidParams)
	}
	if len(svc.topicCalls) != 0 || svc.reviewTaskID != "" || svc.startRequest != "" {
		t.Fatalf("归属校验失败时不该调服务，得到 %+v", svc)
	}
}

// TestStewardMethodsNotImplemented 校验服务未注入时返回 MethodNotFound。
func TestStewardMethodsNotImplemented(t *testing.T) {
	d, _ := newStewardExtTestDispatcher(t, nil)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	cases := []string{
		fmt.Sprintf(`{"jsonrpc":"2.0","method":%q,"params":{"projectID":"p1"},"id":1}`, stewardOpenMethod),
		`{"jsonrpc":"2.0","method":"steward.topics.start","params":{"projectID":"p1","request":"干活"},"id":1}`,
		`{"jsonrpc":"2.0","method":"steward.topics.replan","params":{"projectID":"p1","threadID":"topic-1"},"id":1}`,
		`{"jsonrpc":"2.0","method":"steward.topics.confirm","params":{"projectID":"p1","threadID":"topic-1"},"id":1}`,
		`{"jsonrpc":"2.0","method":"steward.topics.close","params":{"projectID":"p1","threadID":"topic-1"},"id":1}`,
		`{"jsonrpc":"2.0","method":"steward.topics.delete","params":{"projectID":"p1","threadID":"topic-1"},"id":1}`,
		`{"jsonrpc":"2.0","method":"steward.topics.tasks.review","params":{"projectID":"p1","threadID":"topic-1","taskID":"task-9","decision":"accept"},"id":1}`,
	}
	for _, raw := range cases {
		assertStewardError(t, callExtension(t, d, conn, raw), wire.MethodNotFound)
	}
}

// TestStewardTopicReviewConflictCode 校验修订号冲突映射成版本冲突错误码。
func TestStewardTopicReviewConflictCode(t *testing.T) {
	svc := &fakeStewardExtService{reviewErr: fmt.Errorf("%w: revision 7 != 9", ErrStewardRevisionConflict)}
	d, _ := newStewardExtTestDispatcher(t, svc)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"steward.topics.tasks.review",`+
		`"params":{"projectID":"p1","threadID":"topic-1","taskID":"task-9","decision":"accept","revision":7},"id":1}`)
	assertStewardError(t, resp, wire.ErrCodeVersionConflict)
}

// assertStewardChanged 断言只广播了一条 steward.changed，且载荷是期望的项目。
func assertStewardChanged(t *testing.T, rec *memoryExtRecordingConn, projectID string) {
	t.Helper()
	if len(rec.notifications) != 1 {
		t.Fatalf("应广播一条通知，得到 %+v", rec.notifications)
	}
	got := rec.notifications[0]
	if got.method != stewardExtChangedMethod {
		t.Fatalf("通知方法 = %q", got.method)
	}
	changed, ok := got.params.(wire.StewardExtChanged)
	if !ok {
		t.Fatalf("通知载荷类型不对: %T", got.params)
	}
	if changed.ProjectID != projectID {
		t.Fatalf("projectID = %q, 期望 %q", changed.ProjectID, projectID)
	}
}

// assertStewardError 断言响应带指定错误码。
func assertStewardError(t *testing.T, resp wire.Response, code int) {
	t.Helper()
	if resp.Error == nil {
		t.Fatalf("期望错误码 %d，实际成功: %+v", code, resp.Result)
	}
	if resp.Error.Code != code {
		t.Fatalf("错误码 = %d (%s), 期望 %d", resp.Error.Code, resp.Error.Message, code)
	}
}
