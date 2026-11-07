package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"mcode/internal/nmem"
	"mcode/internal/store"
)

// topicCall 记录话题工具收到的参数，供断言。
type topicCall struct {
	projectID     string
	title         string
	request       string
	brief         string
	openQuestions int
	threadID      string
	reason        string
}

// newTopicTestHooks 返回一套话题钩子：steward-1 是总管，topic-1 是话题，其余都是普通会话。
func newTopicTestHooks(call *topicCall) *StewardHooks {
	return &StewardHooks{
		IsSteward: func(threadID string) bool { return threadID == "steward-1" },
		IsTopic:   func(threadID string) bool { return threadID == "topic-1" },
		OpenTopic: func(_ context.Context, projectID, title, request string) (string, error) {
			call.projectID, call.title, call.request = projectID, title, request
			return "topic-2", nil
		},
		UpdateBrief: func(_ context.Context, threadID, brief string, openQuestions int) error {
			call.threadID, call.brief, call.openQuestions = threadID, brief, openQuestions
			return nil
		},
		StartTopicGoal: func(_ context.Context, threadID, reason string) error {
			call.threadID, call.reason = threadID, reason
			return nil
		},
	}
}

// withSearchTools 打开检索类工具，否则它们会在 tools/list 里被过滤掉。
func withSearchTools(opts *Options) {
	opts.ThreadSearch = func(context.Context, string, string, int) ([]nmem.ThreadHit, error) { return nil, nil }
	opts.ThreadFetch = func(context.Context, string, string, int, int) (nmem.ThreadPage, error) {
		return nmem.ThreadPage{}, nil
	}
	opts.MemorySearch = func(context.Context, string, nmem.SearchQuery) ([]nmem.Memory, error) { return nil, nil }
	opts.MemoryGet = func(context.Context, string, string) (nmem.Memory, error) { return nmem.Memory{}, nil }
}

// TestStewardOpenTopicOnlyForSteward 校验开话题只对总管主会话开放，且参数原样传给钩子。
func TestStewardOpenTopicOnlyForSteward(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")
	registerThread(t, st, "topic-1", "proj-test-1")
	var call topicCall
	srv.SetSteward(newTopicTestHooks(&call))

	if !threadToolNames(t, srv, "steward-1")[stewardOpenTopicToolName] {
		t.Fatal("总管主会话应当能看到 steward_open_topic")
	}
	if threadToolNames(t, srv, "topic-1")[stewardOpenTopicToolName] {
		t.Fatal("话题会话不该看到 steward_open_topic")
	}

	text, isError := callStewardTool(t, srv, "steward-1", stewardOpenTopicToolName, map[string]any{
		"project": "proj-test-1",
		"title":   "整理登录流程",
		"request": "把登录流程理顺",
	})
	if isError {
		t.Fatalf("开话题不该出错: %s", text)
	}
	payload := decodeStewardPayload(t, text)
	if payload["threadId"] != "topic-2" {
		t.Errorf("threadId = %v，期望 topic-2", payload["threadId"])
	}
	if message, _ := payload["message"].(string); !strings.Contains(message, "已开话题") {
		t.Errorf("message = %q，期望包含「已开话题」", message)
	}
	if call.projectID != "proj-test-1" || call.title != "整理登录流程" || call.request != "把登录流程理顺" {
		t.Errorf("钩子收到的参数不对: %+v", call)
	}

	// 话题会话直接调用也拿不到：按未知工具拒绝，与列表裁剪一致。
	text, isError = callStewardTool(t, srv, "topic-1", stewardOpenTopicToolName, map[string]any{
		"project": "proj-test-1",
		"title":   "标题",
		"request": "需求",
	})
	if !isError || !strings.Contains(text, "Unknown tool") {
		t.Fatalf("话题会话调用开话题应按未知工具拒绝: %q error=%v", text, isError)
	}
}

// TestStewardUpdateBriefFromTopic 校验任务书只能由话题会话写入。
func TestStewardUpdateBriefFromTopic(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")
	registerThread(t, st, "topic-1", "proj-test-1")
	var call topicCall
	srv.SetSteward(newTopicTestHooks(&call))

	text, isError := callStewardTool(t, srv, "topic-1", stewardUpdateBriefToolName, map[string]any{
		"brief":          "## 目标\n把登录流程理顺",
		"open_questions": 2,
	})
	if isError {
		t.Fatalf("写任务书不该出错: %s", text)
	}
	if call.threadID != "topic-1" || call.brief != "## 目标\n把登录流程理顺" || call.openQuestions != 2 {
		t.Errorf("钩子收到的参数不对: %+v", call)
	}
	if payload := decodeStewardPayload(t, text); payload["openQuestions"] != float64(2) {
		t.Errorf("openQuestions = %v，期望 2", payload["openQuestions"])
	}

	// 缺 open_questions 要报参数错误：缺省成 0 会误报「任务书已就绪」。
	text, isError = callStewardTool(t, srv, "topic-1", stewardUpdateBriefToolName, map[string]any{
		"brief": "## 目标\n随便",
	})
	if !isError || !strings.Contains(text, "open_questions") {
		t.Fatalf("缺 open_questions 应当报参数错误: %q error=%v", text, isError)
	}

	// 总管主会话没有这个工具。
	text, isError = callStewardTool(t, srv, "steward-1", stewardUpdateBriefToolName, map[string]any{
		"brief":          "## 目标\n随便",
		"open_questions": 0,
	})
	if !isError || !strings.Contains(text, "Unknown tool") {
		t.Fatalf("总管主会话写任务书应按未知工具拒绝: %q error=%v", text, isError)
	}
}

// TestStewardUpdateBriefAutoConfirmsWhenNoOpenQuestions 校验待定问题清零时自动开工：
// ConfirmTopic 被调用一次，结果带 started: true。
func TestStewardUpdateBriefAutoConfirmsWhenNoOpenQuestions(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "topic-1", "proj-test-1")
	var call topicCall
	hooks := newTopicTestHooks(&call)
	confirms := 0
	hooks.ConfirmTopic = func(_ context.Context, threadID string) error {
		confirms++
		call.threadID = threadID
		return nil
	}
	srv.SetSteward(hooks)

	text, isError := callStewardTool(t, srv, "topic-1", stewardUpdateBriefToolName, map[string]any{
		"brief":          "## 目标\n把登录流程理顺",
		"open_questions": 0,
	})
	if isError {
		t.Fatalf("写任务书不该出错: %s", text)
	}
	if confirms != 1 {
		t.Fatalf("open_questions 为 0 时应自动开工一次，实际 %d 次", confirms)
	}
	if call.threadID != "topic-1" {
		t.Fatalf("ConfirmTopic 应收到会话 ID topic-1，实际为 %q", call.threadID)
	}
	payload := decodeStewardPayload(t, text)
	if payload["started"] != true {
		t.Fatalf("started = %v，期望 true", payload["started"])
	}
	if message, _ := payload["message"].(string); !strings.Contains(message, "宿主已开始规划") {
		t.Fatalf("message = %q，期望包含「宿主已开始规划」", message)
	}
}

// TestStewardUpdateBriefKeepsBriefWhenOpenQuestionsRemain 校验还有待定问题时不自动开工。
func TestStewardUpdateBriefKeepsBriefWhenOpenQuestionsRemain(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "topic-1", "proj-test-1")
	var call topicCall
	hooks := newTopicTestHooks(&call)
	confirms := 0
	hooks.ConfirmTopic = func(context.Context, string) error {
		confirms++
		return nil
	}
	srv.SetSteward(hooks)

	text, isError := callStewardTool(t, srv, "topic-1", stewardUpdateBriefToolName, map[string]any{
		"brief":          "## 目标\n把登录流程理顺",
		"open_questions": 2,
	})
	if isError {
		t.Fatalf("写任务书不该出错: %s", text)
	}
	if confirms != 0 {
		t.Fatalf("还有待定问题不应自动开工，实际调用 %d 次", confirms)
	}
	if payload := decodeStewardPayload(t, text); payload["started"] != nil {
		t.Fatalf("还有待定问题时不应带 started 字段: %v", payload)
	}
}

// TestStewardUpdateBriefTreatsConfirmErrorAsSoft 校验开工失败不算工具失败：
// 返回 ok: true、started: false，并把原错误文案放进 note。
func TestStewardUpdateBriefTreatsConfirmErrorAsSoft(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "topic-1", "proj-test-1")
	var call topicCall
	hooks := newTopicTestHooks(&call)
	hooks.ConfirmTopic = func(context.Context, string) error {
		return errors.New("这个话题已经开工，无需再次确认")
	}
	srv.SetSteward(hooks)

	text, isError := callStewardTool(t, srv, "topic-1", stewardUpdateBriefToolName, map[string]any{
		"brief":          "## 目标\n把登录流程理顺",
		"open_questions": 0,
	})
	if isError {
		t.Fatalf("开工失败不应把任务书写入判为失败: %s", text)
	}
	payload := decodeStewardPayload(t, text)
	if payload["ok"] != true {
		t.Fatalf("ok = %v，期望 true", payload["ok"])
	}
	if payload["started"] != false {
		t.Fatalf("started = %v，期望 false", payload["started"])
	}
	if note, _ := payload["note"].(string); !strings.Contains(note, "这个话题已经开工") {
		t.Fatalf("note = %q，期望带上原错误文案", note)
	}
}

// TestStewardStartGoalFromTopic 校验开工只能由话题会话发起。
func TestStewardStartGoalFromTopic(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "steward-1", "proj-test-1")
	registerThread(t, st, "topic-1", "proj-test-1")
	var call topicCall
	srv.SetSteward(newTopicTestHooks(&call))

	text, isError := callStewardTool(t, srv, "topic-1", stewardStartGoalToolName, map[string]any{
		"reason": "拆分没问题",
	})
	if isError {
		t.Fatalf("开工不该出错: %s", text)
	}
	if call.threadID != "topic-1" || call.reason != "拆分没问题" {
		t.Errorf("钩子收到的参数不对: %+v", call)
	}

	text, isError = callStewardTool(t, srv, "steward-1", stewardStartGoalToolName, map[string]any{
		"reason": "拆分没问题",
	})
	if !isError || !strings.Contains(text, "Unknown tool") {
		t.Fatalf("总管主会话开工应按未知工具拒绝: %q error=%v", text, isError)
	}
}

// TestStewardTopicToolsVisibility 校验话题会话的工具集随阶段变化：建目标前只有追问与只读工具，
// 建目标后（计划角色）再加上验收与开工工具。
func TestStewardTopicToolsVisibility(t *testing.T) {
	srv, st, _ := setupTestMCPWithOptions(t, withSearchTools)
	registerThread(t, st, "topic-1", "proj-test-1")
	var call topicCall
	srv.SetSteward(newTopicTestHooks(&call))

	names := threadToolNames(t, srv, "topic-1")
	for _, want := range []string{
		stewardUpdateBriefToolName, stewardNotifyOwnerToolName,
		"thread_search", "thread_fetch_messages", "memory_search", "memory_get",
		"kanban_read_board", "kanban_read_card", "kanban_task_diff", "kanban_goal_status",
	} {
		if !names[want] {
			t.Errorf("话题会话建目标前应当能看到 %s", want)
		}
	}
	for _, notWant := range []string{
		stewardOpenTopicToolName, stewardStartGoalToolName, stewardAttentionListToolName, stewardProjectsListToolName,
		"kanban_review_task", "kanban_create_draft", "kanban_update_card", kanbanEntryToolName,
	} {
		if names[notWant] {
			t.Errorf("话题会话建目标前不该看到 %s", notWant)
		}
	}

	// 建目标后它同时是计划角色：验收与开工工具出现，追问期的看板入口仍然不给。
	planThreadID := "topic-1"
	if err := st.CreateGoal(context.Background(), store.Goal{
		ID:           "goal-1",
		ProjectID:    "proj-test-1",
		Title:        "整理登录流程",
		Prompt:       "## 目标\n把登录流程理顺",
		Status:       "plan_review",
		PlanThreadID: &planThreadID,
	}); err != nil {
		t.Fatalf("插入目标失败: %v", err)
	}
	names = threadToolNames(t, srv, "topic-1")
	for _, want := range []string{
		stewardStartGoalToolName, stewardUpdateBriefToolName, stewardNotifyOwnerToolName,
		"kanban_review_task", "kanban_task_diff", "kanban_read_card", "kanban_goal_status",
	} {
		if !names[want] {
			t.Errorf("计划角色的话题会话应当能看到 %s", want)
		}
	}
	for _, notWant := range []string{stewardOpenTopicToolName, stewardAttentionListToolName, stewardProjectsListToolName, kanbanEntryToolName} {
		if names[notWant] {
			t.Errorf("计划角色的话题会话不该看到 %s", notWant)
		}
	}
}

// TestStewardTopicToolsHiddenFromOrdinaryThread 校验普通会话看不到话题工具。
func TestStewardTopicToolsHiddenFromOrdinaryThread(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "lead-1", "proj-test-1")
	before := threadToolNames(t, srv, "lead-1")
	var call topicCall
	srv.SetSteward(newTopicTestHooks(&call))
	after := threadToolNames(t, srv, "lead-1")
	if !stewardSameNames(before, after) {
		t.Fatalf("普通会话的工具列表被改动: 前 %v 后 %v", before, after)
	}
	for name := range stewardTopicToolNames {
		if after[name] {
			t.Errorf("普通会话不该看到 %s", name)
		}
	}
	text, isError := callStewardTool(t, srv, "lead-1", stewardUpdateBriefToolName, map[string]any{
		"brief":          "## 目标\n随便",
		"open_questions": 0,
	})
	if !isError || !strings.Contains(text, "Unknown tool") {
		t.Fatalf("普通会话写任务书应按未知工具拒绝: %q error=%v", text, isError)
	}
}
