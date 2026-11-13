package mcodeui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/agentconf"
	"mcode/internal/testenv"
)

// 场景 1：覆盖 detectCompletion 各类触发与不触发情形。
func TestDetectCompletion(t *testing.T) {
	tests := []struct {
		name      string
		draft     string
		wantKind  completionKind
		wantStart int
		wantQuery string
	}{
		{
			name:      "斜杠指令正常触发",
			draft:     "/pl",
			wantKind:  completionSlash,
			wantStart: 0,
			wantQuery: "pl",
		},
		{
			name:      "草稿不以斜杠开头不触发斜杠补全",
			draft:     "hello /x",
			wantKind:  completionNone,
			wantStart: 0,
			wantQuery: "",
		},
		{
			name:      "提及文件正常触发",
			draft:     "see @src/ma",
			wantKind:  completionMention,
			wantStart: 4,
			wantQuery: "src/ma",
		},
		{
			name:      "token内部含@不触发提及",
			draft:     "a@b",
			wantKind:  completionNone,
			wantStart: 0,
			wantQuery: "",
		},
		{
			name:      "空草稿不触发",
			draft:     "",
			wantKind:  completionNone,
			wantStart: 0,
			wantQuery: "",
		},
		{
			name:      "末尾为空格不触发",
			draft:     "see @src/ma ",
			wantKind:  completionNone,
			wantStart: 0,
			wantQuery: "",
		},
		{
			name:      "单独@开头触发",
			draft:     "@main.go",
			wantKind:  completionMention,
			wantStart: 0,
			wantQuery: "main.go",
		},
		{
			name:      "单独/触发",
			draft:     "/",
			wantKind:  completionSlash,
			wantStart: 0,
			wantQuery: "",
		},
		{name: "全角空格后的提及", draft: "查看　@文件", wantKind: completionMention, wantStart: len("查看　"), wantQuery: "文件"},
		{name: "全角空格结束提及", draft: "@文件　", wantKind: completionNone},
		{name: "全角空格结束命令", draft: "/plan　", wantKind: completionNone},
		{name: "行注释开头不触发命令补全", draft: "// TODO: 修复"},
		{name: "块注释开头不触发命令补全", draft: "/* util */"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, start, query := detectCompletion(tt.draft)
			if kind != tt.wantKind || start != tt.wantStart || query != tt.wantQuery {
				t.Fatalf("detectCompletion(%q) = (%v, %d, %q), want (%v, %d, %q)",
					tt.draft, kind, start, query, tt.wantKind, tt.wantStart, tt.wantQuery)
			}
		})
	}
}

// 场景 2：filterCommands 排序（前缀匹配在前，包含匹配在后）与返回上限（limit=8）。
func TestFilterCommands_OrderAndLimit(t *testing.T) {
	cmds := []SlashCommand{
		{Name: "replan", Detail: "重新规划"},
		{Name: "plan", Detail: "制定计划"},
		{Name: "clear", Detail: "清屏"},
		{Name: "plant", Detail: "种植"},
		{Name: "explain", Detail: "解释代码"},
		{Name: "myplan-custom", Detail: "自定义计划"},
		{Name: "plan-now", Detail: "立即计划"},
		{Name: "pl", Detail: "缩写"},
		{Name: "extra-plan-1", Detail: "扩展1"},
		{Name: "extra-plan-2", Detail: "扩展2"},
	}

	filtered := filterCommands(cmds, "pl", 8)
	if len(filtered) != 8 {
		t.Fatalf("expected 8 items, got %d", len(filtered))
	}

	// 验证前四个全部为以 "pl" 开头的前缀匹配
	for i := 0; i < 4; i++ {
		name := filtered[i].Name
		if len(name) < 2 || name[:2] != "pl" {
			t.Fatalf("expected prefix match at index %d, got %s", i, name)
		}
	}

	// 测试上限溢出（总共匹配很多时，截断到 8 条）
	var manyCmds []SlashCommand
	for i := 0; i < 20; i++ {
		manyCmds = append(manyCmds, SlashCommand{Name: fmt.Sprintf("cmd-%d", i)})
	}
	capped := filterCommands(manyCmds, "cmd", 8)
	if len(capped) != 8 {
		t.Fatalf("expected 8 items capped, got %d", len(capped))
	}
}

// 场景 3：Tab 只补全不执行，Enter 确认后才执行无参数本地命令。
func TestHandleCompletionKey_TabAndEnterConfirm(t *testing.T) {
	fake := NewFakeClient()

	// 1. Tab：只把选中项补进草稿，不执行本地命令
	s1 := newChatState(fake)
	s1.draft = "/mo"
	s1.completionKind = completionSlash
	s1.completionStart = 0
	s1.completionItems = []completionItem{
		{Label: "/mode", Insert: "/mode", Detail: "Toggle plan mode"},
		{Label: "/model", Insert: "/model", Detail: "Switch the active language model"},
	}
	s1.completionSelected = 0

	newText, handled := s1.handleCompletionKey(ui.KeyTab, 0)
	if !handled {
		t.Fatalf("expected Tab to be handled")
	}
	if newText != "/mode " {
		t.Fatalf("expected newText '/mode ', got %q", newText)
	}
	if s1.draft != "/mode " {
		t.Fatalf("expected s.draft '/mode ', got %q", s1.draft)
	}
	if s1.completionKind != completionNone {
		t.Fatalf("expected completion closed, got %v", s1.completionKind)
	}
	if !s1.completionCaretToEnd {
		t.Fatalf("completion must request the caret at the end of the draft")
	}
	if s1.draft != "/mode " {
		t.Fatalf("Tab must only fill the draft and keep it, got %q", s1.draft)
	}

	// 2. 回车执行无参数命令时才生效：/clear 会新建会话并清空草稿
	s1.draft = "/cl"
	s1.completionKind = completionSlash
	s1.completionStart = 0
	s1.completionItems = []completionItem{{Label: "/clear", Insert: "/clear"}}
	s1.completionSelected = 0
	newDraft, handled := s1.handleCompletionKey(ui.KeyEnter, 0)
	if !handled {
		t.Fatalf("expected Enter to be handled")
	}
	if newDraft != "" || s1.draft != "" {
		t.Fatalf("expected Enter to run /clear and empty the draft, got newText=%q draft=%q", newDraft, s1.draft)
	}

	// 2. 测试 Enter 确认提及文件，且选择下一项
	s2 := newChatState(fake)
	s2.draft = "see @src/m"
	s2.completionKind = completionMention
	s2.completionStart = 4
	s2.completionItems = []completionItem{
		{Label: "src/main.go", Insert: "@src/main.go"},
		{Label: "src/model.go", Insert: "@src/model.go"},
	}
	s2.completionSelected = 0

	// Down 移动到下一项
	_, downHandled := s2.handleCompletionKey(ui.KeyDown, 0)
	if !downHandled || s2.completionSelected != 1 {
		t.Fatalf("expected Down to select index 1, got handled=%v, selected=%d", downHandled, s2.completionSelected)
	}

	// Enter 确认
	newText2, enterHandled := s2.handleCompletionKey(ui.KeyEnter, 0)
	if !enterHandled {
		t.Fatalf("expected Enter to be handled")
	}
	if newText2 != "see @src/model.go " {
		t.Fatalf("expected 'see @src/model.go ', got %q", newText2)
	}
	if s2.draft != "see @src/model.go " {
		t.Fatalf("expected s.draft 'see @src/model.go ', got %q", s2.draft)
	}
}

// 场景 3b：面板打开期间会话被删除，失效的 @ 会话引用不能被键盘插入。
func TestHandleCompletionKey_SkipsUnavailableThreadMention(t *testing.T) {
	s := newChatState(NewFakeClient())
	s.selectedThreadID = "t1"
	s.threads = []Thread{
		{ID: "t1"},
		{ID: "t2", Title: "第二个会话", UpdatedAt: time.Now()},
		{ID: "t3", Title: "第三个会话"},
	}
	s.draft = "@"
	s.completionKind = completionMention
	s.completionStart = 0
	s.completionItems = recentThreadMentionItems(s.threads, "", "t1", "", time.Now(), 5)
	if len(s.completionItems) != 2 || s.completionItems[0].ThreadID != "t2" || s.completionItems[1].ThreadID != "t3" {
		t.Fatalf("unexpected mention items: %+v", s.completionItems)
	}
	// 模拟面板打开期间第二个会话被删除：快照里还留着它，实时会话列表已经没有了。
	s.threads = []Thread{{ID: "t1"}, {ID: "t3", Title: "第三个会话"}}
	if got := completionThreadUnavailable(s, s.completionItems[0]); got != "该会话已不存在" {
		t.Fatalf("unavailable reason = %q", got)
	}
	if got := completionThreadUnavailable(s, s.completionItems[1]); got != "" {
		t.Fatalf("live thread must be available, got %q", got)
	}

	// 向下键跳过失效项；绕回起点时继续跳过。
	s.completionSelected = 0
	if _, handled := s.handleCompletionKey(ui.KeyDown, 0); !handled || s.completionSelected != 1 {
		t.Fatalf("Down must skip the unavailable item, handled=%v selected=%d", handled, s.completionSelected)
	}
	if _, handled := s.handleCompletionKey(ui.KeyDown, 0); !handled || s.completionSelected != 1 {
		t.Fatalf("Down must keep skipping, handled=%v selected=%d", handled, s.completionSelected)
	}
	// 向上键同样跳过。
	if _, handled := s.handleCompletionKey(ui.KeyUp, 0); !handled || s.completionSelected != 1 {
		t.Fatalf("Up must skip the unavailable item, handled=%v selected=%d", handled, s.completionSelected)
	}

	// Tab / Enter 落在失效项上：按键被吞掉，草稿与面板都不变。
	for _, key := range []ui.Key{ui.KeyTab, ui.KeyEnter} {
		s.completionSelected = 0
		newText, handled := s.handleCompletionKey(key, 0)
		if !handled {
			t.Fatalf("%v on unavailable item must be handled", key)
		}
		if newText != "" || s.draft != "@" {
			t.Fatalf("%v must not touch the draft, got %q draft %q", key, newText, s.draft)
		}
		if s.completionKind != completionMention || len(s.draftThreadMentions) != 0 {
			t.Fatalf("%v must keep the panel open and insert nothing: kind=%v mentions=%+v", key, s.completionKind, s.draftThreadMentions)
		}
	}

	// 正常项回归：选中存活会话后 Tab 照旧插入引用胶囊。
	s.completionSelected = 1
	if _, handled := s.handleCompletionKey(ui.KeyTab, 0); !handled {
		t.Fatalf("expected Tab on a live item to be handled")
	}
	if len(s.draftThreadMentions) != 1 || s.draftThreadMentions[0].ThreadID != "t3" {
		t.Fatalf("live mention not inserted: %+v (draft %q)", s.draftThreadMentions, s.draft)
	}
}

// 场景 4：Esc 取消后，同 token 不再弹出，token 变更后重新弹出。
func TestCompletion_DismissEsc(t *testing.T) {
	fake := NewFakeClient()
	fake.SetCommands([]SlashCommand{{Name: "plan", Detail: "计划"}})

	s := newChatState(fake)
	s.draft = "/pl"
	s.completionKind = completionSlash
	s.completionStart = 0
	s.completionThreadID = "t1"
	s.commandsCache["t1"] = []SlashCommand{{Name: "plan"}}
	s.completionItems = []completionItem{{Label: "/plan", Insert: "/plan"}}

	// 按 Esc 取消
	_, handled := s.handleCompletionKey(ui.KeyEscape, 0)
	if !handled {
		t.Fatalf("expected Esc to be handled")
	}
	if s.completionKind != completionNone {
		t.Fatalf("expected completion closed after Esc")
	}
	if s.completionDismissedFor != "/pl" {
		t.Fatalf("expected completionDismissedFor '/pl', got %q", s.completionDismissedFor)
	}

	// 重新调用 updateCompletion（草稿仍为 "/pl"），不应该重新弹出
	s.updateCompletion("t1")
	if s.completionKind != completionNone {
		t.Fatalf("expected completion to stay closed for dismissed token, got %v", s.completionKind)
	}

	// 草稿输入改变为 "/pla"，token 发生变化，恢复弹出
	s.draft = "/pla"
	s.updateCompletion("t1")
	if s.completionKind != completionSlash {
		t.Fatalf("expected completion to reactivate after token change, got %v", s.completionKind)
	}
	if s.completionDismissedFor != "" {
		t.Fatalf("expected dismissedFor cleared, got %q", s.completionDismissedFor)
	}
}

func composerTester(s *chatState) *ui.Tester {
	s.selectedThreadID = "t1"
	s.threads = []Thread{{ID: "t1"}}
	return ui.NewTester(func(c *ui.Context) {
		renderComposer(c, s)
	}, 900, 500)
}

func TestComposerCompletionKeyboard(t *testing.T) {
	for _, key := range []ui.Key{ui.KeyTab, ui.KeyEnter} {
		t.Run(fmt.Sprint(key), func(t *testing.T) {
			s := newChatState(NewFakeClient())
			s.commandsCache["t1"] = []SlashCommand{{Name: "plan"}, {Name: "plugin"}}
			tt := composerTester(s)
			tt.Type("/pl")
			waitFor(t, tt, func() bool { return tt.HasText("/plan") })
			tt.Key(0, ui.KeyDown)
			tt.Key(0, key)
			tt.Frame()
			if s.draft != "/plugin " {
				t.Fatalf("completion = %q", s.draft)
			}
			if s.client.(*FakeClient).SendCalls() != 0 {
				t.Fatal("confirmation also sent a message")
			}
		})
	}
}

func TestComposerCompletionEscapeAndIME(t *testing.T) {
	s := newChatState(NewFakeClient())
	s.commandsCache["t1"] = []SlashCommand{{Name: "plan"}}
	tt := composerTester(s)
	tt.Type("/pl")
	waitFor(t, tt, func() bool { return tt.HasText("/plan") })
	tt.Key(0, ui.KeyEscape)
	// 关闭带渐隐收起动画，需等动画结束。
	tt.Advance(400 * time.Millisecond) // 等收起动画走完（测试时钟需手动推进）
	if tt.HasText("/plan") {
		t.Fatal("completion still visible after close animation")
	}
	tt.Type("a")
	waitFor(t, tt, func() bool { return tt.HasText("/plan") })
	tt.Compose("ni", 2)
	tt.Advance(400 * time.Millisecond)
	if tt.HasText("/plan") {
		t.Fatal("completion is visible during composition")
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if s.draft == "/plan " || s.client.(*FakeClient).SendCalls() != 0 {
		t.Fatalf("composition changed or sent draft: %q", s.draft)
	}
}

func TestComposerMentionTagAndMouseConfirmation(t *testing.T) {
	fake := NewFakeClient()
	fake.SetFiles([]FileMatch{{Name: "文件 名.go", Path: "src/文件 名.go"}})
	s := newChatState(fake)
	tt := composerTester(s)
	tt.Type("查看　@")
	waitFor(t, tt, func() bool { return tt.HasText("src/文件 名.go") })
	if err := tt.Click("src/文件 名.go"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if s.draft != "查看　@src/文件 名.go " {
		t.Fatalf("mention = %q", s.draft)
	}
	ranges := s.mentionRanges(ui.RGB(0, 100, 200))
	if len(ranges) != 1 || s.draftText.AttributesAt(ranges[0].Start).Tag != "src/文件 名.go" {
		t.Fatalf("missing file Tag: %+v", ranges)
	}
	s.draft += "说明"
	s.syncDraftTags()
	if len(s.mentionRanges(ui.RGB(0, 100, 200))) != 1 {
		t.Fatal("editing after mention lost its Tag")
	}
}

type completionRequest struct {
	threadID, query string
	reply           chan []FileMatch
}

type delayedCompletionClient struct {
	*FakeClient
	requests chan completionRequest
}

func (f *delayedCompletionClient) SearchFiles(_ context.Context, threadID, query string) ([]FileMatch, error) {
	req := completionRequest{threadID: threadID, query: query, reply: make(chan []FileMatch)}
	f.requests <- req
	return <-req.reply, nil
}

func nextCompletionRequest(t *testing.T, f *delayedCompletionClient) completionRequest {
	t.Helper()
	select {
	case req := <-f.requests:
		return req
	case <-time.After(2 * time.Second):
		t.Fatal("file search was not started")
		return completionRequest{}
	}
}

func TestCompletionRejectsStaleResults(t *testing.T) {
	fake := &delayedCompletionClient{FakeClient: NewFakeClient(), requests: make(chan completionRequest, 4)}
	s := newChatState(fake)
	s.draft = "@"
	s.updateCompletion("t1")
	first := nextCompletionRequest(t, fake)
	if first.query != "" {
		t.Fatalf("bare @ query = %q", first.query)
	}
	// 同一帧重复更新不应重复发起搜索。
	s.updateCompletion("t1")
	select {
	case <-fake.requests:
		t.Fatal("duplicate search")
	default:
	}
	s.draft = "@new"
	s.updateCompletion("t2")
	second := nextCompletionRequest(t, fake)
	first.reply <- []FileMatch{{Path: "old.go"}}
	second.reply <- []FileMatch{{Path: "new.go"}}
	tt := ui.NewTester(func(c *ui.Context) {}, 100, 100)
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.completionLoaded
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	if second.threadID != "t2" || len(s.completionItems) != 1 || s.completionItems[0].Label != "new.go" {
		t.Fatalf("stale results: %+v", s.completionItems)
	}
}

func TestCompletionCacheIsScopedToThread(t *testing.T) {
	s := newChatState(NewFakeClient())
	s.commandsCache["t1"] = []SlashCommand{{Name: "plan"}}
	s.commandsCache["t2"] = []SlashCommand{{Name: "compact"}}
	s.draft = "/"
	s.updateCompletion("t1")
	s.updateCompletion("t2")
	if len(s.completionItems) != 1 || s.completionItems[0].Label != "/compact" {
		t.Fatalf("wrong thread commands: %+v", s.completionItems)
	}
}

func TestComposerCompletionDoesNotReplaceAtMiddleCaret(t *testing.T) {
	s := newChatState(NewFakeClient())
	s.commandsCache["t1"] = []SlashCommand{{Name: "plan"}}
	tt := composerTester(s)
	tt.Type("/pl")
	waitFor(t, tt, func() bool { return tt.HasText("/plan") })
	tt.Key(0, ui.KeyLeft)
	tt.Advance(400 * time.Millisecond) // 等收起动画走完（测试时钟需手动推进）
	if tt.HasText("/plan") {
		t.Fatal("completion still visible after close animation")
	}
}

// 场景 5：输入法组字中（isComposing=true）按键与更新不被补全处理。
func TestCompletion_IsComposing(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)
	s.draft = "/pl"
	s.completionKind = completionSlash
	s.completionStart = 0
	s.completionItems = []completionItem{{Label: "/plan", Insert: "/plan"}}
	s.isComposing = true

	// 组字中所有按键均应返回 false
	if _, handled := s.handleCompletionKey(ui.KeyEnter, 0); handled {
		t.Fatalf("expected KeyEnter to not be handled during composing")
	}
	if _, handled := s.handleCompletionKey(ui.KeyTab, 0); handled {
		t.Fatalf("expected KeyTab to not be handled during composing")
	}
	if _, handled := s.handleCompletionKey(ui.KeyEscape, 0); handled {
		t.Fatalf("expected KeyEscape to not be handled during composing")
	}

	// 组字中 updateCompletion 也不应执行变更
	s.draft = "/new"
	s.updateCompletion("t1")
	if s.completionQuery != "" {
		t.Fatalf("expected completionQuery not updated during composing, got %q", s.completionQuery)
	}
}

// filterCommands 对 "mo" 前缀过滤与排序测试。
func TestFilterCommands_ModeAndModelPrefix(t *testing.T) {
	testenv.TempHome(t)

	cmds := []SlashCommand{
		{Name: "compact", Detail: "Summarize conversation (more context)"},
		{Name: "model", Detail: "Switch the active language model"},
		{Name: "mode", Detail: "Toggle plan mode"},
		{Name: "demo", Detail: "Demonstration mode"},
	}

	filtered := filterCommands(cmds, "mo", 8)
	if len(filtered) < 2 {
		t.Fatalf("expected at least 2 matches for 'mo', got %d", len(filtered))
	}

	// 验证以 "mo" 开头的前缀项排在前面（mode 与 model 均在前缀组内）
	firstTwoNames := []string{filtered[0].Name, filtered[1].Name}
	hasMode := firstTwoNames[0] == "mode" || firstTwoNames[1] == "mode"
	hasModel := firstTwoNames[0] == "model" || firstTwoNames[1] == "model"
	if !hasMode || !hasModel {
		t.Fatalf("expected mode and model as leading prefix matches, got: %v", firstTwoNames)
	}

	// 验证包含匹配（compact 包含 "mo"）排在前缀匹配之后
	var foundPrefixEnd bool
	for _, c := range filtered {
		isPrefix := len(c.Name) >= 2 && c.Name[:2] == "mo"
		if !isPrefix {
			foundPrefixEnd = true
		} else if foundPrefixEnd {
			t.Fatalf("prefix match %s appeared after contains match", c.Name)
		}
	}
}

// detectCompletion 加 confirmCompletion 的选择流程测试。
func TestComposerCompletion_DetectAndConfirmFlow(t *testing.T) {
	testenv.TempHome(t)

	fake := NewFakeClient()
	s := newChatState(fake)
	s.draft = "/mo"

	kind, start, query := detectCompletion(s.draft)
	if kind != completionSlash || start != 0 || query != "mo" {
		t.Fatalf("detectCompletion('/mo') = (%v, %d, %q), want (completionSlash, 0, 'mo')", kind, start, query)
	}

	cmds := []SlashCommand{
		{Name: "mode", Detail: "Toggle plan mode"},
		{Name: "model", Detail: "Switch the active language model"},
	}
	filtered := filterCommands(cmds, query, 8)
	items := slashCommandsToItems(filtered, query)
	if len(items) != 2 {
		t.Fatalf("expected 2 completion items, got %d", len(items))
	}

	s.completionKind = kind
	s.completionStart = start
	s.completionItems = items
	s.completionSelected = 1 // 选中第二项 /model

	confirmed := s.confirmCompletion(items[1])
	if confirmed != "/model " {
		t.Fatalf("confirmCompletion expected '/model ', got %q", confirmed)
	}
	if s.draft != "/model " {
		t.Fatalf("expected s.draft '/model ', got %q", s.draft)
	}
	if s.completionKind != completionNone {
		t.Fatalf("expected completion closed, got %v", s.completionKind)
	}
}

// runLocalSlashCommand 对本地命令执行、清空草稿及非本地命令转发测试。
func TestRunLocalSlashCommand_FastAndCompact(t *testing.T) {
	testenv.TempHome(t)
	t.Cleanup(waitUIStateWrites)

	fake := NewFakeClient()
	s := newChatState(fake)

	th := Thread{
		ID:        "t1",
		ProjectID: "p1",
		Title:     "会话1",
		Provider:  "codex",
		Model:     "o3-mini",
	}
	s.threads = []Thread{th}
	s.selectedThreadID = "t1"

	pin := ModelPin{Provider: "codex", ModelID: "o3-mini"}

	// 1. 验证 /fast：本地执行成功、切换快速模式状态、清空草稿
	if s.modelPreference(pin).FastMode {
		t.Fatalf("initial FastMode should be false")
	}
	s.draft = "/fast"
	ok := s.runLocalSlashCommand("t1", s.draft)
	if !ok {
		t.Fatalf("runLocalSlashCommand('/fast') expected true, got false")
	}
	if s.draft != "" {
		t.Fatalf("runLocalSlashCommand('/fast') should clear draft, got: %q", s.draft)
	}
	if !s.modelPreference(pin).FastMode {
		t.Fatalf("expected FastMode to be toggled to true")
	}

	// 2. 再次执行 /fast：翻转回 false，清空草稿
	s.draft = "  /fast  "
	ok = s.runLocalSlashCommand("t1", s.draft)
	if !ok {
		t.Fatalf("runLocalSlashCommand('  /fast  ') expected true, got false")
	}
	if s.draft != "" {
		t.Fatalf("runLocalSlashCommand('  /fast  ') should clear draft, got: %q", s.draft)
	}
	if s.modelPreference(pin).FastMode {
		t.Fatalf("expected FastMode to be toggled back to false")
	}

	// 3. 验证 /compact：非本地拦截命令，必须返回 false，草稿保留给后端发送
	s.draft = "/compact"
	ok = s.runLocalSlashCommand("t1", s.draft)
	if ok {
		t.Fatalf("runLocalSlashCommand('/compact') expected false, got true")
	}
	if s.draft != "/compact" {
		t.Fatalf("runLocalSlashCommand('/compact') should preserve draft, got: %q", s.draft)
	}
}

func TestRunLocalSlashCommand_ClearResetsStewardOnly(t *testing.T) {
	fake := newStewardFakeClient("t2")
	s := newChatState(fake)
	t.Cleanup(s.close)
	s.threads = []Thread{{ID: "t1", ProjectID: "p1"}, {ID: "t2", ProjectID: "p1"}}
	s.selectedThreadID = "t2"
	if !s.runLocalSlashCommand("t2", "/clear") {
		t.Fatal("总管 /clear 应被本地处理")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && fake.resetCalls() == 0 {
		time.Sleep(time.Millisecond)
	}
	if got := fake.resetCalls(); got != 1 {
		t.Fatalf("总管 /clear 应调用一次 ResetStewardThread，实际 %d", got)
	}

	ordinary := newStewardFakeClient("t2")
	o := newChatState(ordinary)
	t.Cleanup(o.close)
	o.threads = []Thread{{ID: "t1", ProjectID: "p1"}, {ID: "t2", ProjectID: "p1"}}
	o.selectedThreadID = "t1"
	if !o.runLocalSlashCommand("t1", "/clear") {
		t.Fatal("普通会话 /clear 应被本地处理")
	}
	if got := ordinary.resetCalls(); got != 0 {
		t.Fatalf("普通会话 /clear 不应调用 ResetStewardThread，实际 %d", got)
	}
	o.mu.Lock()
	pending := o.pendingThread != nil
	o.mu.Unlock()
	if !pending {
		t.Fatal("普通会话 /clear 应创建新会话")
	}
}

// /export 已接入导出功能，必须本地拦截并触发导出，不能当作普通消息发给 Agent。
func TestRunLocalSlashCommand_ExportNotForwarded(t *testing.T) {
	testenv.TempHome(t)

	s := newChatState(NewFakeClient())
	s.threads = []Thread{{ID: "t1", ProjectID: "p1", Title: "会话1", Provider: "claude", Model: "claude-sonnet-4"}}
	s.selectedThreadID = "t1"
	s.draft = "/export"

	// 替换保存对话框：返回空路径模拟用户取消，避免测试弹出真实窗口
	dialogCalled := make(chan struct{}, 1)
	old := saveExportDialog
	saveExportDialog = func(defaultPath string) (string, error) {
		select {
		case dialogCalled <- struct{}{}:
		default:
		}
		return "", nil
	}
	t.Cleanup(func() { saveExportDialog = old })

	if !s.runLocalSlashCommand("t1", s.draft) {
		t.Fatalf("runLocalSlashCommand('/export') expected true so it is not sent as text")
	}
	s.mu.Lock()
	draft, toast := s.draft, s.toastMessage
	s.mu.Unlock()
	if draft != "" {
		t.Fatalf("expected draft cleared after /export, got %q", draft)
	}
	if strings.Contains(toast, "暂不支持") {
		t.Fatalf("expected /export to trigger the export flow, got toast %q", toast)
	}

	// 导出在后台 goroutine 中执行：等待保存对话框被调用，确认 exportThread 已接线
	select {
	case <-dialogCalled:
	case <-time.After(2 * time.Second):
		t.Fatalf("expected exportThread to open the save dialog within 2s")
	}

	// 其他 provider 命令（如 /review）仍交给后端，不在本地拦截
	s.draft = "/review"
	if s.runLocalSlashCommand("t1", s.draft) {
		t.Fatalf("expected /review to be forwarded to the backend")
	}
}

// 别名命中（前缀与包含）与名称子序列模糊匹配。
func TestFilterCommands_AliasAndFuzzyMatch(t *testing.T) {
	cmds := []SlashCommand{
		{Name: "model", Detail: "切换模型"},
		{Name: "effort", Detail: "推理强度"},
		{Name: "compact", Detail: "总结会话"},
		{Name: "pythagoras", Detail: "无关命令"},
		{Name: "clear", Detail: "清空"},
	}

	// 别名前缀（effort 的别名 think）优先于名称包含（pythagoras 含 "th"）
	byAliasPrefix := filterCommands(cmds, "th", 8)
	if len(byAliasPrefix) != 2 || byAliasPrefix[0].Name != "effort" || byAliasPrefix[1].Name != "pythagoras" {
		t.Fatalf("expected [effort pythagoras] for alias prefix 'th', got %+v", byAliasPrefix)
	}

	// 大小写不敏感
	byUpper := filterCommands(cmds, "TH", 8)
	if len(byUpper) != 2 || byUpper[0].Name != "effort" {
		t.Fatalf("expected case-insensitive alias match, got %+v", byUpper)
	}

	// 别名包含（think 含 "ink" 但非前缀）
	byAliasContains := filterCommands(cmds, "ink", 8)
	if len(byAliasContains) != 1 || byAliasContains[0].Name != "effort" {
		t.Fatalf("expected effort via alias contains 'ink', got %+v", byAliasContains)
	}

	// 名称子序列模糊匹配：mdl → model
	byFuzzy := filterCommands(cmds, "mdl", 8)
	if len(byFuzzy) != 1 || byFuzzy[0].Name != "model" {
		t.Fatalf("expected model via subsequence 'mdl', got %+v", byFuzzy)
	}
}

// 六档匹配优先级的排序（名称前缀 > 别名前缀 > 名称包含 > 别名包含 > Detail 包含 > 名称子序列）。
func TestFilterCommands_MatchPriorityOrder(t *testing.T) {
	cmds := []SlashCommand{
		// Detail 含 "ink"
		{Name: "zzz", Detail: "blink once"},
		// 别名 think 含 "ink"
		{Name: "effort", Detail: "推理强度"},
		// 名称子序列 i-n-k
		{Name: "invoke-k", Detail: "无关"},
		// 名称包含 "ink"
		{Name: "linking", Detail: "无关"},
		// 名称前缀 "ink"
		{Name: "inkwell", Detail: "无关"},
		// 完全不匹配
		{Name: "clear", Detail: "无关"},
	}

	filtered := filterCommands(cmds, "ink", 8)
	want := []string{"inkwell", "linking", "effort", "zzz", "invoke-k"}
	if len(filtered) != len(want) {
		t.Fatalf("expected %d matches, got %+v", len(want), filtered)
	}
	for i, name := range want {
		if filtered[i].Name != name {
			t.Fatalf("match priority at %d = %q, want %q (full: %+v)", i, filtered[i].Name, name, filtered)
		}
	}
}

// 别名命中的菜单项追加「（别名 /xx）」，参数提示来自 slashCommandArgHints。
func TestSlashCommandsToItems_AliasDetailAndHint(t *testing.T) {
	// 靠别名命中：Detail 追加别名说明，Hint 填参数提示
	items := slashCommandsToItems([]SlashCommand{{Name: "effort", Detail: "推理强度"}}, "th")
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if !strings.Contains(items[0].Detail, "（别名 /think）") {
		t.Fatalf("expected alias suffix in detail, got %q", items[0].Detail)
	}
	if items[0].Hint != "low | medium | high" {
		t.Fatalf("unexpected hint %q", items[0].Hint)
	}
	if items[0].Insert != "/effort" {
		t.Fatalf("insert must stay the canonical command, got %q", items[0].Insert)
	}

	// 靠名称命中：不追加别名说明
	byName := slashCommandsToItems([]SlashCommand{{Name: "model", Detail: "切换模型"}}, "mo")
	if byName[0].Detail != "切换模型" {
		t.Fatalf("name match must not add alias suffix, got %q", byName[0].Detail)
	}
	if byName[0].Hint != "打开模型选择" {
		t.Fatalf("unexpected hint %q", byName[0].Hint)
	}

	// query 为空：不追加别名说明
	empty := slashCommandsToItems([]SlashCommand{{Name: "model", Detail: "切换模型"}}, "")
	if empty[0].Detail != "切换模型" {
		t.Fatalf("empty query must not add alias suffix, got %q", empty[0].Detail)
	}
}

// Tab 把选中的斜杠命令补成 "/name "（带空格）且不发送；Enter 对非无参数本地命令只补全。
func TestCompletionTab_InsertsSlashCommandWithSpace(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)
	s.draft = "/mo"

	kind, start, query := detectCompletion(s.draft)
	s.completionKind = kind
	s.completionStart = start
	s.completionItems = slashCommandsToItems(filterCommands([]SlashCommand{
		{Name: "model", Detail: "切换模型"},
		{Name: "mode", Detail: "切换模式"},
	}, query, 8), query)
	s.completionSelected = 0

	newText, handled := s.handleCompletionKey(ui.KeyTab, 0)
	if !handled {
		t.Fatalf("expected Tab to be handled")
	}
	if newText != "/model " || s.draft != "/model " {
		t.Fatalf("Tab must insert '/model ' with trailing space, got %q / %q", newText, s.draft)
	}
	if s.completionKind != completionNone {
		t.Fatalf("expected completion menu closed, got %v", s.completionKind)
	}
	if !s.completionCaretToEnd {
		t.Fatalf("completion must request the caret at the end of the draft")
	}
	if fake.SendCalls() != 0 {
		t.Fatalf("Tab must not send a message")
	}

	// Enter 对非无参数本地命令（/compact）只补全为 "/compact "，不发送
	s2 := newChatState(fake)
	s2.draft = "/comp"
	s2.completionKind = completionSlash
	s2.completionStart = 0
	s2.completionItems = []completionItem{{Label: "/compact", Insert: "/compact", Hint: "[保留重点]"}}
	s2.completionSelected = 0
	newDraft, handled := s2.handleCompletionKey(ui.KeyEnter, 0)
	if !handled || newDraft != "/compact " || s2.draft != "/compact " {
		t.Fatalf("Enter must only complete '/compact ', got handled=%v newText=%q draft=%q", handled, newDraft, s2.draft)
	}
	if fake.SendCalls() != 0 {
		t.Fatalf("Enter on a non-local command must not send a message")
	}
}

// 确认会话引用项时不再写入正文，只清掉 @ 触发词并入草稿引用胶囊。
func TestConfirmCompletion_ThreadMention(t *testing.T) {
	s := newChatState(NewFakeClient())
	s.draft = "@新"
	s.completionKind = completionMention
	s.completionStart = 0
	item := completionItem{Label: "新会话", Insert: "@新会话", ThreadID: "t9", Icon: "thread"}
	s.completionItems = []completionItem{item}
	s.completionSelected = 0

	got := s.confirmCompletion(item)
	if got != "" || s.draft != "" {
		t.Fatalf("confirmCompletion = %q, draft = %q", got, s.draft)
	}
	if len(s.draftThreadMentions) != 1 || s.draftThreadMentions[0].ThreadID != "t9" || s.draftThreadMentions[0].Title != "新会话" {
		t.Fatalf("missing draftThreadMentions entry: %+v", s.draftThreadMentions)
	}
	if ranges := s.mentionRanges(ui.RGB(0, 0, 0)); len(ranges) != 0 {
		t.Fatalf("thread mention must not tag the draft text, got %+v", ranges)
	}

	// 同一会话重复补全时只保留一颗胶囊。
	s.addDraftThreadMention("t9", "新会话")
	if len(s.draftThreadMentions) != 1 {
		t.Fatalf("thread mention must be deduped, got %+v", s.draftThreadMentions)
	}

	// 光标在起始处的 Backspace 删最后一颗胶囊。
	if !s.removeLastDraftThreadMention() || len(s.draftThreadMentions) != 0 {
		t.Fatalf("removeLastDraftThreadMention must drop the chip, got %+v", s.draftThreadMentions)
	}
}

// '$' 与 '@' 同规则触发技能补全，且只认 token 开头的触发符。
func TestDetectCompletion_SkillSigil(t *testing.T) {
	tests := []struct {
		name      string
		draft     string
		wantKind  completionKind
		wantStart int
		wantQuery string
	}{
		{"bare", "$", completionSkill, 0, ""},
		{"after space", "看看 $de", completionSkill, 7, "de"},
		{"at start", "$demo", completionSkill, 0, "demo"},
		{"in word", "a$b", completionNone, 0, ""},
		{"trailing space", "$demo ", completionNone, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, start, query := detectCompletion(tt.draft)
			if kind != tt.wantKind || start != tt.wantStart || query != tt.wantQuery {
				t.Fatalf("detectCompletion(%q) = (%v, %d, %q), want (%v, %d, %q)",
					tt.draft, kind, start, query, tt.wantKind, tt.wantStart, tt.wantQuery)
			}
		})
	}
}

// 技能候选按「名称前缀 > 名称包含 > 路径包含」排序，插入时名字里的空格换成 '-'。
func TestSkillCompletionItems_InsertDashesSpaces(t *testing.T) {
	skills := []agentconf.Skill{
		{Agent: "claude", Scope: "external", Name: "other", Path: "/p/plugins/demo/other/SKILL.md"},
		{Agent: "claude", Scope: "project", Name: "my demo", Path: "/p/my-demo/SKILL.md"},
		{Agent: "claude", Scope: "project", Name: "demo skill", Path: "/p/demo-skill/SKILL.md"},
	}

	items := skillCompletionItems(skills, "demo")
	if len(items) != 3 {
		t.Fatalf("items = %+v", items)
	}
	want := []string{"$demo-skill", "$my-demo", "$other"}
	for i, insert := range want {
		if items[i].Insert != insert {
			t.Fatalf("item %d insert = %q, want %q (order: %+v)", i, items[i].Insert, insert, items)
		}
	}
	if items[0].Detail != "claude · project" {
		t.Fatalf("detail = %q", items[0].Detail)
	}

	// 空查询列出全部技能；不匹配的查询返回空。
	if got := skillCompletionItems(skills, ""); len(got) != 3 {
		t.Fatalf("empty query items = %+v", got)
	}
	if got := skillCompletionItems(skills, "zzz"); len(got) != 0 {
		t.Fatalf("unmatched query items = %+v", got)
	}
	// 禁用的技能不进候选。
	if got := skillCompletionItems([]agentconf.Skill{{Name: "off", Disabled: true}}, ""); len(got) != 0 {
		t.Fatalf("disabled skill must be skipped, got %+v", got)
	}
}

// nonCategoryItems 过滤掉分类行，便于断言叶子候选的数量与顺序。
func nonCategoryItems(items []completionItem) []completionItem {
	out := make([]completionItem, 0, len(items))
	for _, it := range items {
		if !it.Category {
			out = append(out, it)
		}
	}
	return out
}

// waitCompletionLoaded 轮询等待异步补全（文件搜索）落地。
func waitCompletionLoaded(t *testing.T, s *chatState) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mu.Lock()
		done := s.completionLoaded && !s.completionLoading
		s.mu.Unlock()
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("completion did not load in time")
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// 分类行下钻后能返回上一层：草稿里的前缀就是层级本身，删掉前缀即回到分类列表。
func TestCompletionCategories_DescendAndBack(t *testing.T) {
	fake := NewFakeClient()
	fake.SetFiles([]FileMatch{{Name: "main.go", Path: "src/main.go"}})
	s := newChatState(fake)
	s.draft = "@"
	s.updateCompletion("t1")
	s.mu.Lock()
	items := append([]completionItem(nil), s.completionItems...)
	s.mu.Unlock()
	if len(items) < 2 || !items[0].Category || items[0].Insert != "@file:" || !items[1].Category || items[1].Insert != "@thread:" {
		t.Fatalf("bare @ must lead with file/thread categories, got %+v", items)
	}

	// Tab 选中「文件」分类：草稿留下前缀，面板保持打开。
	s.mu.Lock()
	s.completionSelected = 0
	s.mu.Unlock()
	newText, handled := s.handleCompletionKey(ui.KeyTab, 0)
	if !handled || newText != "@file:" || s.draft != "@file:" {
		t.Fatalf("Tab on category = (%v, %q), draft = %q", handled, newText, s.draft)
	}
	if s.completionKind != completionMention {
		t.Fatalf("panel must stay open after descending, kind = %v", s.completionKind)
	}

	s.updateCompletion("t1")
	waitCompletionLoaded(t, s)
	s.mu.Lock()
	for _, it := range s.completionItems {
		if it.Category {
			s.mu.Unlock()
			t.Fatalf("descended list must not repeat categories, got %+v", s.completionItems)
		}
	}
	if len(s.completionItems) == 0 {
		s.mu.Unlock()
		t.Fatalf("descended list is empty")
	}
	s.mu.Unlock()

	// 删掉前缀回到分类列表。
	s.draft = "@"
	s.updateCompletion("t1")
	s.mu.Lock()
	back := append([]completionItem(nil), s.completionItems...)
	s.mu.Unlock()
	if len(back) < 2 || !back[0].Category || back[0].Insert != "@file:" {
		t.Fatalf("going back must restore categories, got %+v", back)
	}
}

// 分类行的 Tab 不关闭面板，也不把候选清空。
func TestCompletionTabKeepsPanelOpenOnCategory(t *testing.T) {
	s := newChatState(NewFakeClient())
	s.draft = "@"
	s.updateCompletion("t1")

	s.mu.Lock()
	s.completionSelected = 1 // 「会话」分类
	s.mu.Unlock()
	if _, handled := s.handleCompletionKey(ui.KeyTab, 0); !handled {
		t.Fatalf("Tab must be handled")
	}
	if s.completionKind != completionMention || len(s.completionItems) == 0 {
		t.Fatalf("panel must stay open with items, kind = %v, items = %d", s.completionKind, len(s.completionItems))
	}
	if s.draft != "@thread:" {
		t.Fatalf("draft = %q, want @thread:", s.draft)
	}

	// 只看会话分类时不请求文件搜索，候选直接来自本地 s.threads。
	s.updateCompletion("t1")
	if s.completionKind != completionMention || s.completionLoading {
		t.Fatalf("thread-only scope must not load, kind = %v loading = %v", s.completionKind, s.completionLoading)
	}
}

// 目录项在 Tab 时进入子目录并保持打开，Enter 仍插入并关闭。
func TestCompletionTabOnDirectoryDescends(t *testing.T) {
	dir := completionItem{Label: "src/", Insert: "@src/"}

	s := newChatState(NewFakeClient())
	s.draft = "@sr"
	s.completionKind = completionMention
	s.completionStart = 0
	s.completionItems = []completionItem{dir}
	s.completionSelected = 0
	newText, handled := s.handleCompletionKey(ui.KeyTab, 0)
	if !handled || newText != "@src/" || s.draft != "@src/" {
		t.Fatalf("Tab on directory = (%v, %q), draft = %q", handled, newText, s.draft)
	}
	if s.completionKind != completionMention {
		t.Fatalf("panel must stay open after entering a directory, kind = %v", s.completionKind)
	}

	s2 := newChatState(NewFakeClient())
	s2.draft = "@sr"
	s2.completionKind = completionMention
	s2.completionStart = 0
	s2.completionItems = []completionItem{dir}
	s2.completionSelected = 0
	if _, handled := s2.handleCompletionKey(ui.KeyEnter, 0); !handled {
		t.Fatalf("Enter must be handled")
	}
	if s2.completionKind != completionNone || s2.draft != "@src/ " {
		t.Fatalf("Enter on directory must insert and close, kind = %v draft = %q", s2.completionKind, s2.draft)
	}
}

// '$' 空查询先给分类行，再给全部技能；有查询字符后只剩叶子候选。
func TestSkillCompletionPanelCategories(t *testing.T) {
	origScan := scanSkills
	resetSkillCache()
	t.Cleanup(func() {
		scanSkills = origScan
		resetSkillCache()
	})
	release := make(chan struct{})
	scanSkills = func(opts agentconf.Options) ([]agentconf.Skill, error) {
		<-release
		return []agentconf.Skill{
			{Agent: "claude", Scope: "project", Name: "deploy", Path: opts.ProjectDir + "/deploy/SKILL.md"},
			{Agent: "codex", Scope: "global", Name: "review", Path: opts.ProjectDir + "/review/SKILL.md"},
		}, nil
	}

	s := newChatState(NewFakeClient())
	s.projects = []Project{{ID: "p1", Name: "repo", Path: "/repo"}}
	s.threads = []Thread{{ID: "t1", ProjectID: "p1"}}
	s.draft = "$"
	s.updateCompletion("t1")

	// 扫描被 release 阻塞时面板靠 loading 行维持可见：加载中允许先出分类行（项目 / 全局 / 外部），
	// 但不得出现任何叶子候选（Category 为 false 的行）。
	if !s.completionLoading {
		t.Fatalf("first frame must be loading, loading=%v items=%+v", s.completionLoading, s.completionItems)
	}
	for _, item := range s.completionItems {
		if !item.Category {
			t.Fatalf("加载中不应出现叶子候选: %+v", item)
		}
	}
	close(release)
	waitSkillReady(t, s, "/repo")
	s.updateCompletion("t1")
	s.mu.Lock()
	items := append([]completionItem(nil), s.completionItems...)
	loading := s.completionLoading
	s.mu.Unlock()
	if loading {
		t.Fatalf("skills must be loaded")
	}
	if len(items) < 4 {
		t.Fatalf("expected 3 categories + skills, got %+v", items)
	}
	if !items[0].Category || items[0].Insert != "$project:" || items[1].Insert != "$global:" || items[2].Insert != "$external:" {
		t.Fatalf("skill categories = %+v", items[:3])
	}

	// 进入「全局」分类后只剩 global 技能。
	s.mu.Lock()
	s.completionSelected = 1
	s.mu.Unlock()
	if _, handled := s.handleCompletionKey(ui.KeyTab, 0); !handled {
		t.Fatalf("Tab must be handled")
	}
	if s.draft != "$global:" {
		t.Fatalf("draft = %q, want $global:", s.draft)
	}
	s.updateCompletion("t1")
	s.mu.Lock()
	global := append([]completionItem(nil), s.completionItems...)
	s.mu.Unlock()
	if len(global) != 1 || global[0].Insert != "$review" {
		t.Fatalf("global scope items = %+v", global)
	}
}
