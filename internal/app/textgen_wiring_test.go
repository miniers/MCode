package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"mcode/internal/config"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
)

// TestGenerateAutoTitleUsesInjectedGenerator 验证 generateAutoTitle 走可注入的生成入口，
// 并复用首条用户消息构造提示词。
func TestGenerateAutoTitleUsesInjectedGenerator(t *testing.T) {
	const want = "修复登录按钮样式"
	var gotPrompt string

	orig := autoTitleGenerateText
	autoTitleGenerateText = func(ctx context.Context, cfg config.Config, secrets config.SecretStore, provider, model, effort, prompt string) (string, error) {
		gotPrompt = prompt
		return want, nil
	}
	t.Cleanup(func() { autoTitleGenerateText = orig })

	in := autoTitleInput{firstUserMsg: "hello world", firstReply: "echo: hello world"}
	title, err := generateAutoTitle(context.Background(), t.TempDir(), in)
	if err != nil {
		t.Fatalf("generateAutoTitle error: %v", err)
	}
	if title != want {
		t.Errorf("title = %q, want %q", title, want)
	}
	if !strings.Contains(gotPrompt, "hello world") {
		t.Errorf("prompt 未包含首条用户消息, got %q", gotPrompt)
	}
}

func TestIsDefaultTitle(t *testing.T) {
	tests := []struct {
		title string
		want  bool
	}{
		{"新会话", true},
		{"", true},
		{"   ", true},
		{"\t\n", true},
		{"我的新功能开发", false},
		{"手动输入的标题", false},
		{"新会话（已重命名）", false},
	}

	for _, tc := range tests {
		got := IsDefaultTitle(tc.title)
		if got != tc.want {
			t.Errorf("IsDefaultTitle(%q) = %v, want %v", tc.title, got, tc.want)
		}
	}
}

func TestAutoTitleOnlyAttemptOnce(t *testing.T) {
	threadID := "thread-test-once-1"
	ResetAutoTitleAttempted(threadID)

	// 第一次检查应返回 false（表示未尝试过，本次登记）
	if HasAttemptedAutoTitle(threadID) {
		t.Fatalf("first HasAttemptedAutoTitle should return false")
	}

	// 第二次检查应返回 true（表示已经尝试过，不应再次触发）
	if !HasAttemptedAutoTitle(threadID) {
		t.Fatalf("second HasAttemptedAutoTitle should return true")
	}
}

func TestAutoTitleNotTriggeredWhenCustomTitle(t *testing.T) {
	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	defer hub.Close()
	backend := remote.NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()
	projID := "proj-custom-title"
	backend.AddProject(wire.ProjectSummary{ID: projID, Name: "Test Proj", Path: "/mock"})

	thID := "thread-custom-title-1"
	ResetAutoTitleAttempted(thID)
	backend.AddThread(wire.ThreadSummary{
		ID:        thID,
		ProjectID: projID,
		Title:     "用户自定义的标题",
		Provider:  "memory",
	}, []wire.ThreadMessage{
		{ID: "m1", Role: "user", Text: "帮我实现一个功能"},
		{ID: "m2", Role: "assistant", Text: "好的，马上实现"},
	})

	// 触发结束回调
	HandleTurnFinished(backend, t.TempDir(), thID, projID, "好的，马上实现", true)

	// 验证标题仍为自定义标题
	th, err := backend.GetThread(ctx, thID)
	if err != nil {
		t.Fatalf("get thread: %v", err)
	}
	if th.Title != "用户自定义的标题" {
		t.Errorf("expected title to remain custom, got %q", th.Title)
	}
}

func TestAutoTitleNotTriggeredWhenNotCompleted(t *testing.T) {
	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	defer hub.Close()
	backend := remote.NewMemoryBackend(hub)
	defer backend.Close()

	projID := "proj-not-completed"
	backend.AddProject(wire.ProjectSummary{ID: projID, Name: "Test Proj", Path: "/mock"})

	thID := "thread-not-completed-1"
	ResetAutoTitleAttempted(thID)
	backend.AddThread(wire.ThreadSummary{
		ID:        thID,
		ProjectID: projID,
		Title:     DefaultThreadTitle,
		Provider:  "memory",
	}, []wire.ThreadMessage{
		{ID: "m1", Role: "user", Text: "帮我实现一个功能"},
	})

	// completed 为 false 时不触发
	HandleTurnFinished(backend, t.TempDir(), thID, projID, "", false)

	// 未被登记为已尝试
	if HasAttemptedAutoTitle(thID) {
		t.Errorf("uncompleted turn should not mark thread as attempted")
	}
}

// TestShouldAutoGenerateTitle 验证自动标题的覆盖判定：
// 空/默认占位标题、仍沿用转发源会话标题（用户未手动改名），
// 或仍是首条消息本地截取值的标题可被覆盖；用户手动改过的标题不覆盖。
func TestShouldAutoGenerateTitle(t *testing.T) {
	msg := "请帮我重构 internal/config 包并补充单元测试"
	derived := remote.ThreadTitleFromText(msg)
	if derived == DefaultThreadTitle {
		t.Fatalf("本地截取标题不应是默认占位值，得到 %q", derived)
	}

	tests := []struct {
		name           string
		title          string
		msg            string
		inheritedTitle string
		want           bool
	}{
		{"空标题", "", msg, "", true},
		{"默认占位标题", DefaultThreadTitle, msg, "", true},
		{"本地截取标题可覆盖", derived, msg, "", true},
		{"用户手动改名不覆盖", "手动改的标题", msg, "", false},
		{"无用户消息时默认标题仍可覆盖", DefaultThreadTitle, "", "", true},
		{"无用户消息且已命名不覆盖", "手动改的标题", "", "", false},
		// (a) inheritedTitle 等于 title 时返回 true
		{"沿用转发标题未修改时可覆盖", "源会话原始标题", msg, "源会话原始标题", true},
		{"沿用转发标题首尾空格不影响覆盖", "  源会话原始标题  ", msg, "源会话原始标题", true},
		// (b) inheritedTitle 非空但 title 已被用户改成别的时返回 false
		{"转发标题已被用户修改时不覆盖", "用户改后的新标题", msg, "源会话原始标题", false},
		// (c) inheritedTitle 为空时行为与之前一致
		{"inheritedTitle 为空时根据本地截取标题判定可覆盖", derived, msg, "", true},
		{"inheritedTitle 为空且标题不匹配本地截取时不覆盖", "无关标题", msg, "", false},
	}
	for _, tc := range tests {
		if got := shouldAutoGenerateTitle(tc.title, tc.msg, tc.inheritedTitle); got != tc.want {
			t.Errorf("%s: shouldAutoGenerateTitle(%q, %q, %q) = %v, want %v", tc.name, tc.title, tc.msg, tc.inheritedTitle, got, tc.want)
		}
	}
}

// TestInheritedContinuationTitle 验证取转发来源会话标题的逻辑：
// 仅在 ForkedFromThreadID 非空且 ForkedFromMessageID 为 nil 或空串时取源会话标题；分支或普通会话返回空串。
func TestInheritedContinuationTitle(t *testing.T) {
	ctx := context.Background()
	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	defer hub.Close()
	backend := remote.NewMemoryBackend(hub)
	defer backend.Close()

	srcID := "src-thread-id"
	srcTitle := "源会话标题"
	backend.AddThread(wire.ThreadSummary{
		ID:    srcID,
		Title: srcTitle,
	}, nil)

	// backend 为 nil
	if got := inheritedContinuationTitle(ctx, nil, wire.ThreadSummary{ForkedFromThreadID: &srcID}); got != "" {
		t.Errorf("backend 为 nil 时期望返回空串，得到 %q", got)
	}

	// ForkedFromThreadID 为 nil 或空
	if got := inheritedContinuationTitle(ctx, backend, wire.ThreadSummary{}); got != "" {
		t.Errorf("ForkedFromThreadID 为 nil 时期望返回空串，得到 %q", got)
	}
	emptyID := ""
	if got := inheritedContinuationTitle(ctx, backend, wire.ThreadSummary{ForkedFromThreadID: &emptyID}); got != "" {
		t.Errorf("ForkedFromThreadID 为空串时期望返回空串，得到 %q", got)
	}

	// 分支会话（ForkedFromMessageID 非空）
	msgID := "msg-1"
	if got := inheritedContinuationTitle(ctx, backend, wire.ThreadSummary{ForkedFromThreadID: &srcID, ForkedFromMessageID: &msgID}); got != "" {
		t.Errorf("分支会话（ForkedFromMessageID 非空）期望返回空串，得到 %q", got)
	}

	// 源会话不存在
	missingID := "missing-thread"
	if got := inheritedContinuationTitle(ctx, backend, wire.ThreadSummary{ForkedFromThreadID: &missingID}); got != "" {
		t.Errorf("源会话不存在时期望返回空串，得到 %q", got)
	}

	// 转发会话（ForkedFromThreadID 有效且 ForkedFromMessageID 为空）
	emptyMsgID := ""
	if got := inheritedContinuationTitle(ctx, backend, wire.ThreadSummary{ForkedFromThreadID: &srcID, ForkedFromMessageID: nil}); got != srcTitle {
		t.Errorf("转发会话期望获取源会话标题 %q，得到 %q", srcTitle, got)
	}
	if got := inheritedContinuationTitle(ctx, backend, wire.ThreadSummary{ForkedFromThreadID: &srcID, ForkedFromMessageID: &emptyMsgID}); got != srcTitle {
		t.Errorf("ForkedFromMessageID 为空串的转发会话期望获取源会话标题 %q，得到 %q", srcTitle, got)
	}
}

// TestForwardedThreadAutoTitleReplacesInheritedTitle 验证转发会话首轮对话结束后自动生成标题能覆盖源标题，
// 且用户手动改名或分支会话不受影响。
func TestForwardedThreadAutoTitleReplacesInheritedTitle(t *testing.T) {
	const wantGeneratedTitle = "生成的AI新标题"
	orig := autoTitleGenerateText
	autoTitleGenerateText = func(ctx context.Context, cfg config.Config, secrets config.SecretStore, provider, model, effort, prompt string) (string, error) {
		return wantGeneratedTitle, nil
	}
	t.Cleanup(func() { autoTitleGenerateText = orig })

	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	defer hub.Close()
	backend := remote.NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()
	projID := "proj-forward-test"
	backend.AddProject(wire.ProjectSummary{ID: projID, Name: "Test Proj", Path: "/mock"})

	srcID := "thread-src-1"
	srcTitle := "源会话原始标题"
	backend.AddThread(wire.ThreadSummary{
		ID:        srcID,
		ProjectID: projID,
		Title:     srcTitle,
		Provider:  "memory",
	}, []wire.ThreadMessage{
		{ID: "m0", Role: "user", Text: "源会话的问题"},
		{ID: "m1", Role: "assistant", Text: "源会话的回答"},
	})

	// 1. 转发会话沿用源会话标题：首轮结束后标题应被替换成生成结果
	fwdID := "thread-fwd-1"
	ResetAutoTitleAttempted(fwdID)
	backend.AddThread(wire.ThreadSummary{
		ID:                 fwdID,
		ProjectID:          projID,
		Title:              srcTitle,
		Provider:           "memory",
		ForkedFromThreadID: &srcID,
	}, []wire.ThreadMessage{
		{ID: "fwd-m1", Role: "user", Text: "转发会话首轮用户提问"},
		{ID: "fwd-m2", Role: "assistant", Text: "转发会话首轮回复"},
	})

	TriggerAutoTitle(ctx, backend, t.TempDir(), fwdID, "转发会话首轮回复")

	deadline := time.Now().Add(2 * time.Second)
	var finalTitle string
	for time.Now().Before(deadline) {
		th, err := backend.GetThread(ctx, fwdID)
		if err == nil && th.Title == wantGeneratedTitle {
			finalTitle = th.Title
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if finalTitle != wantGeneratedTitle {
		th, _ := backend.GetThread(ctx, fwdID)
		t.Fatalf("转发会话标题未被替换成生成结果: got %q, want %q", th.Title, wantGeneratedTitle)
	}

	// 2. 转发会话若被用户手动改过标题，首轮结束后不覆盖
	fwdRenamedID := "thread-fwd-renamed"
	ResetAutoTitleAttempted(fwdRenamedID)
	manualTitle := "用户手动修改的标题"
	backend.AddThread(wire.ThreadSummary{
		ID:                 fwdRenamedID,
		ProjectID:          projID,
		Title:              manualTitle,
		Provider:           "memory",
		ForkedFromThreadID: &srcID,
	}, []wire.ThreadMessage{
		{ID: "fr-m1", Role: "user", Text: "用户提问"},
		{ID: "fr-m2", Role: "assistant", Text: "回复"},
	})

	TriggerAutoTitle(ctx, backend, t.TempDir(), fwdRenamedID, "回复")
	time.Sleep(50 * time.Millisecond)
	thRenamed, err := backend.GetThread(ctx, fwdRenamedID)
	if err != nil {
		t.Fatalf("get thread: %v", err)
	}
	if thRenamed.Title != manualTitle {
		t.Fatalf("用户手动改名后不应被覆盖: got %q, want %q", thRenamed.Title, manualTitle)
	}

	// 3. 分支会话（ForkedFromMessageID 非空）不受影响，不覆盖源标题
	branchID := "thread-branch-1"
	forkMsgID := "m0"
	ResetAutoTitleAttempted(branchID)
	backend.AddThread(wire.ThreadSummary{
		ID:                  branchID,
		ProjectID:           projID,
		Title:               srcTitle,
		Provider:            "memory",
		ForkedFromThreadID:  &srcID,
		ForkedFromMessageID: &forkMsgID,
	}, []wire.ThreadMessage{
		{ID: "b-m1", Role: "user", Text: "分支提问"},
		{ID: "b-m2", Role: "assistant", Text: "分支回复"},
	})

	TriggerAutoTitle(ctx, backend, t.TempDir(), branchID, "分支回复")
	time.Sleep(50 * time.Millisecond)
	thBranch, err := backend.GetThread(ctx, branchID)
	if err != nil {
		t.Fatalf("get thread: %v", err)
	}
	if thBranch.Title != srcTitle {
		t.Fatalf("分支会话不应被覆盖标题: got %q, want %q", thBranch.Title, srcTitle)
	}
}

// TestCollectAutoTitleInput 验证从会话历史取首条用户消息（含附件名）与首条答复，后续消息不参与。
func TestCollectAutoTitleInput(t *testing.T) {
	hist := wire.HistoryPage{Messages: []wire.ThreadMessage{
		{ID: "m1", Role: "user", Text: "修复登录崩溃", Media: []wire.MediaRef{{Name: "/tmp/shot.png"}}},
		{ID: "m2", Role: "assistant", Text: "已经定位到空指针"},
		{ID: "m3", Role: "user", Text: "第二条消息不该采用"},
	}}

	in := collectAutoTitleInput(hist, "")
	if in.firstUserMsg != "修复登录崩溃" {
		t.Errorf("firstUserMsg = %q，期望 %q", in.firstUserMsg, "修复登录崩溃")
	}
	if len(in.attachments) != 1 || in.attachments[0] != "/tmp/shot.png" {
		t.Errorf("attachments = %v，期望 [/tmp/shot.png]", in.attachments)
	}
	if in.firstReply != "已经定位到空指针" {
		t.Errorf("firstReply = %q，期望 %q", in.firstReply, "已经定位到空指针")
	}
	if in.empty() {
		t.Error("有用户消息时不应视为空输入")
	}

	// 传入的答复优先使用，不必等历史回写。
	if got := collectAutoTitleInput(hist, "流式答复").firstReply; got != "流式答复" {
		t.Errorf("firstReply = %q，期望 %q", got, "流式答复")
	}

	// 仅附件也算有输入；两者都没有时为空输入。
	onlyMedia := wire.HistoryPage{Messages: []wire.ThreadMessage{{Role: "user", Media: []wire.MediaRef{{Name: "a.png"}}}}}
	if collectAutoTitleInput(onlyMedia, "").empty() {
		t.Error("仅有附件时不应视为空输入")
	}
	if !collectAutoTitleInput(wire.HistoryPage{}, "").empty() {
		t.Error("没有用户消息与附件时应视为空输入")
	}
}
