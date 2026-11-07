package mcodeui

import (
	"strconv"
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// newQuickRepliesTestState 建一个只渲染快捷回复页的设置面板。
func newQuickRepliesTestState(t *testing.T) (*SettingsState, string) {
	t.Helper()
	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, nil)
	return st, dataDir
}

// quickRepliesTestTester 用 ui.NewTester 单独渲染快捷回复页。
func quickRepliesTestTester(t *testing.T, st *SettingsState) *ui.Tester {
	t.Helper()
	tt := conversationSheetsTester(st)
	openConversationSheet(t, tt, "快捷回复 0 个")
	return tt
}

// focusQuickReplyField 点第 row+1 行的标题或正文输入框，把它聚焦。
func focusQuickReplyField(t *testing.T, tt *ui.Tester, row int, field string) {
	t.Helper()
	label := "编辑第 " + strconv.Itoa(row+1) + " 条" + field
	if err := tt.Click(label); err != nil {
		t.Fatalf("focus %q failed: %v", label, err)
	}
}

// typeQuickReply 往第 row+1 行输入标题与正文；输入后补一帧让改动生效。
func typeQuickReply(t *testing.T, tt *ui.Tester, row int, label, message string) {
	t.Helper()
	focusQuickReplyField(t, tt, row, "标题")
	tt.Type(label)
	tt.Frame()
	focusQuickReplyField(t, tt, row, "内容")
	tt.Type(message)
	tt.Frame()
}

// TestQuickRepliesPage_PageStatesCachedAndClosed 断言页状态挂在 SettingsState 上：同一面板两次取到同一指针，Close 后字段清空。
func TestQuickRepliesPage_PageStatesCachedAndClosed(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	if getOpenInPageState(st) != getOpenInPageState(st) {
		t.Fatal("打开方式页状态两次取值不是同一指针")
	}
	if getQuickRepliesPage(st) != getQuickRepliesPage(st) {
		t.Fatal("快捷回复页状态两次取值不是同一指针")
	}
	other := NewSettingsState(settingsTestDir(t), nil, nil)
	if getOpenInPageState(st) == getOpenInPageState(other) || getQuickRepliesPage(st) == getQuickRepliesPage(other) {
		t.Fatal("两个设置面板共用了页状态")
	}
	st.Close()
	st.mu.Lock()
	openIn, quickReplies := st.openInPage, st.quickRepliesPage
	st.mu.Unlock()
	if openIn != nil || quickReplies != nil {
		t.Fatalf("Close 后页状态未清空: openIn=%v quickReplies=%v", openIn, quickReplies)
	}
}

// addQuickReply 点「添加一条」并补一帧，让新行渲染出来。
func addQuickReply(t *testing.T, tt *ui.Tester) {
	t.Helper()
	if err := tt.Click("添加一条"); err != nil {
		t.Fatalf("click 添加一条 failed: %v", err)
	}
	tt.Frame()
}

// assertQuickReplyOrder 断言磁盘上的快捷回复标题顺序与 want 一致。
func assertQuickReplyOrder(t *testing.T, dataDir string, want ...string) {
	t.Helper()
	cfg, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	got := cfg.QuickReplies
	if len(got) != len(want) {
		t.Fatalf("磁盘上的快捷回复应为 %d 条，实际 %d 条: %+v", len(want), len(got), got)
	}
	for i, label := range want {
		if got[i].Label != label {
			t.Fatalf("第 %d 条快捷回复应为 %q，实际 %q: %+v", i+1, label, got[i].Label, got)
		}
	}
}

// 新增、编辑、上移下移、删除（需确认）之后，磁盘上的顺序与页面一致。
func TestQuickReplies_AddReorderDelete(t *testing.T) {
	st, dataDir := newQuickRepliesTestState(t)
	tt := quickRepliesTestTester(t, st)

	if !tt.HasText("还没有快捷回复") {
		t.Fatalf("空列表应提示「还没有快捷回复」，实际: %v", tt.Texts())
	}

	addQuickReply(t, tt)
	typeQuickReply(t, tt, 0, "总结", "把这次改动总结成三条要点。")
	if len(st.cfg.QuickReplies) != 1 || st.cfg.QuickReplies[0].Label != "总结" {
		t.Fatalf("输入后内存配置应已更新，实际 %+v", st.cfg.QuickReplies)
	}

	addQuickReply(t, tt)
	typeQuickReply(t, tt, 1, "写测试", "给这段代码补单元测试。")
	if len(st.cfg.QuickReplies) != 2 || st.cfg.QuickReplies[1].Label != "写测试" {
		t.Fatalf("第二条输入后内存配置应已更新，实际 %+v", st.cfg.QuickReplies)
	}

	// 上移第二条：结构改动当场落盘。
	if err := tt.Click("上移第 2 条"); err != nil {
		t.Fatalf("click 上移第 2 条 failed: %v", err)
	}
	tt.Frame()
	assertQuickReplyOrder(t, dataDir, "写测试", "总结")

	// 下移第 1 条：换回原顺序。
	if err := tt.Click("下移第 1 条"); err != nil {
		t.Fatalf("click 下移第 1 条 failed: %v", err)
	}
	tt.Frame()
	assertQuickReplyOrder(t, dataDir, "总结", "写测试")

	// 删除要确认：弹窗出现后点确认才真的删。
	if err := tt.Click("删除第 1 条"); err != nil {
		t.Fatalf("click 删除第 1 条 failed: %v", err)
	}
	tt.Frame()
	if !tt.HasText("删除快捷回复") {
		t.Fatalf("删除应弹出确认弹窗，实际: %v", tt.Texts())
	}
	if err := tt.Click("确认删除快捷回复"); err != nil {
		t.Fatalf("click 确认删除快捷回复 failed: %v", err)
	}
	tt.Frame()
	assertQuickReplyOrder(t, dataDir, "写测试")

	// 取消删除：配置保持不动。
	if err := tt.Click("删除第 1 条"); err != nil {
		t.Fatalf("click 删除第 1 条 failed: %v", err)
	}
	tt.Frame()
	if err := tt.Click("取消删除快捷回复"); err != nil {
		t.Fatalf("click 取消删除快捷回复 failed: %v", err)
	}
	tt.Frame()
	assertQuickReplyOrder(t, dataDir, "写测试")

	// 删掉最后一条：列表回到空态，磁盘上也不再保留这一项。
	if err := tt.Click("删除第 1 条"); err != nil {
		t.Fatalf("click 删除第 1 条 failed: %v", err)
	}
	tt.Frame()
	if err := tt.Click("确认删除快捷回复"); err != nil {
		t.Fatalf("click 确认删除快捷回复 failed: %v", err)
	}
	tt.Frame()
	assertQuickReplyOrder(t, dataDir)
	if !tt.HasText("还没有快捷回复") {
		t.Fatalf("删除全部后应回到空态，实际: %v", tt.Texts())
	}
}

// 空标题、空内容都不写盘：只在行内标红，磁盘上仍是上一次的合法内容。
func TestQuickReplies_RejectsEmptyLabel(t *testing.T) {
	st, dataDir := newQuickRepliesTestState(t)
	tt := quickRepliesTestTester(t, st)

	addQuickReply(t, tt)
	typeQuickReply(t, tt, 0, "总结", "把这次改动总结成三条要点。")
	st.flushTextSave(true)
	assertQuickReplyOrder(t, dataDir, "总结")

	// 只填内容、标题留空：不合法，不能写盘。
	addQuickReply(t, tt)
	focusQuickReplyField(t, tt, 1, "内容")
	tt.Type("只有内容没有标题")
	tt.Frame()
	if !tt.HasText("标题不能为空") {
		t.Fatalf("空标题应在行内标红，实际: %v", tt.Texts())
	}
	st.flushTextSave(true)
	assertQuickReplyOrder(t, dataDir, "总结")
	if len(st.cfg.QuickReplies) != 1 {
		t.Fatalf("非法行不应写进配置，实际 %+v", st.cfg.QuickReplies)
	}

	// 补上标题、正文还空着（新加的一行）：同样不合法。
	addQuickReply(t, tt)
	focusQuickReplyField(t, tt, 2, "标题")
	tt.Type("补测试")
	tt.Frame()
	if !tt.HasText("内容不能为空") {
		t.Fatalf("空内容应在行内标红，实际: %v", tt.Texts())
	}
	st.flushTextSave(true)
	assertQuickReplyOrder(t, dataDir, "总结")
	if len(st.cfg.QuickReplies) != 1 {
		t.Fatalf("非法行不应写进配置，实际 %+v", st.cfg.QuickReplies)
	}

	// 两行都补齐后就合法了，三条都写盘。
	focusQuickReplyField(t, tt, 1, "标题")
	tt.Type("只有内容")
	tt.Frame()
	focusQuickReplyField(t, tt, 2, "内容")
	tt.Type("给这段代码补单元测试。")
	tt.Frame()
	st.flushTextSave(true)
	assertQuickReplyOrder(t, dataDir, "总结", "只有内容", "补测试")
}

// 配置里的内容变了（别的窗口保存过）时，页面同步成新内容，不会把别人的改动盖回去。
func TestQuickReplies_SyncsExternalChange(t *testing.T) {
	st, dataDir := newQuickRepliesTestState(t)
	tt := quickRepliesTestTester(t, st)

	addQuickReply(t, tt)
	typeQuickReply(t, tt, 0, "旧标题", "旧内容。")
	st.flushTextSave(true)

	// 模拟别处（另一窗口 / 手改 config.json）写入了新内容。
	cfg, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	cfg.QuickReplies = []config.QuickReply{{Label: "别处改的", Message: "别处改的正文。"}}
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatalf("config.Save failed: %v", err)
	}
	st.cfg.QuickReplies = append([]config.QuickReply(nil), cfg.QuickReplies...)

	tt.Frame()
	page := getQuickRepliesPage(st)
	page.mu.Lock()
	rows := append([]*quickReplyRow(nil), page.rows...)
	page.mu.Unlock()
	if len(rows) != 1 || rows[0].Label != "别处改的" || rows[0].Message != "别处改的正文。" {
		t.Fatalf("页面草稿应同步成配置里的新内容，实际 %+v", rows)
	}
}
