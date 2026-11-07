package mcodeui

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// 界面快照导出测试。默认（未设置 MCODE_SNAPSHOTS）只渲染每个界面并用文本断言确认它出来了，
// 不写任何文件；设置 MCODE_SNAPSHOTS 后把它的值当输出目录，为每个界面按浅色/深色各导出一张 PNG：
//
//	MCODE_SNAPSHOTS=/tmp/mcode-snaps go test ./internal/ui -run TestSnapshotViews
//
// 值为空或 "1" 时退回临时目录并把路径打进日志。快照只生成、不比对：跨机器字体差异不会让 CI 变红，
// 视觉回归靠人工看图，所以这里只额外断言深浅两张图确实不同。

// snapshotEnv 是快照输出目录的环境变量名。
const snapshotEnv = "MCODE_SNAPSHOTS"

// snapshotSettle 是每帧之间推进的时长，够跑完弹窗/面板的入场动画。
const snapshotSettle = 400 * time.Millisecond

// snapshotTheme 描述一次导出的主题。
type snapshotTheme struct {
	label string
	dark  bool
}

// snapshotView 是一个待导出的界面：build 每次调用都新建自己的 FakeClient 与状态，
// 保证浅色与深色两次渲染互不影响；setup 用于需要先交互（例如输入 "/"）才会有画面的界面。
type snapshotView struct {
	name  string
	w, h  int
	text  string
	build func(t *testing.T) (func(c *ui.Context), func(tt *ui.Tester))
}

// snapshotViews 返回要导出的代表界面。
func snapshotViews() []snapshotView {
	return []snapshotView{
		{name: "main", w: 1100, h: 720, text: "侧边栏状态位", build: buildSnapshotMain},
		{name: "composer-complete", w: 900, h: 500, text: "/plan", build: buildSnapshotComposer},
		{name: "settings-quick-replies", w: 900, h: 900, text: "快捷回复", build: buildSnapshotQuickReplies},
		{name: "settings-archive", w: 1000, h: 760, text: "已归档", build: buildSnapshotArchive},
		{name: "confirm-dialog", w: 800, h: 600, text: "删除这个会话？", build: buildSnapshotConfirm},
	}
}

// TestSnapshotViews 为每个代表界面导出浅色与深色快照。
// 不用 t.Parallel()：包级 UI 状态（侧边栏悬停卡、bee 折叠、归档页）要在用例之间清干净。
func TestSnapshotViews(t *testing.T) {
	dir := ""
	if os.Getenv(snapshotEnv) != "" {
		dir = snapshotDir(t)
	}

	themes := []snapshotTheme{{label: "light"}, {label: "dark", dark: true}}
	if dir == "" {
		// 未开启导出只渲染浅色一帧，够用来断言界面能出来。
		themes = themes[:1]
	}

	for _, tc := range snapshotViews() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var light *image.RGBA
			for _, theme := range themes {
				resetSnapshotUIState()
				view, setup := tc.build(t)
				tt := ui.NewTester(view, tc.w, tc.h)
				tt.SetDark(theme.dark)
				tt.Frame()
				tt.Advance(snapshotSettle)
				tt.Frame()
				if setup != nil {
					setup(tt)
				}
				waitFor(t, tt, func() bool { return tt.HasText(tc.text) })
				tt.Advance(snapshotSettle)
				tt.Frame()

				if dir == "" {
					if len(tt.Texts()) == 0 {
						t.Fatalf("%s 没渲染出任何文本", tc.name)
					}
					continue
				}

				img := cloneImage(tt.Image())
				if theme.dark {
					if light == nil {
						t.Fatalf("%s 缺少浅色快照，无法比较深浅差异", tc.name)
					}
					if bytes.Equal(light.Pix, img.Pix) {
						t.Fatalf("%s 的深浅两色快照完全相同，主题切换没有生效", tc.name)
					}
				} else {
					light = img
				}
				writeSnapshotPNG(t, img, dir, tc.name, theme.label)
			}
			if dir == "" {
				t.Logf("未设置 %s，只渲染 %s 不导出图片", snapshotEnv, tc.name)
			}
		})
	}
}

// snapshotDir 返回快照输出目录：环境变量为空或 "1" 时退回临时目录下的子目录。
func snapshotDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv(snapshotEnv)
	if dir == "" || dir == "1" {
		dir = filepath.Join(t.TempDir(), "snapshots")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建快照目录失败: %v", err)
	}
	t.Logf("快照输出目录: %s", dir)
	return dir
}

// writeSnapshotPNG 把一帧画面写成 <dir>/<name>-<theme>.png。
func writeSnapshotPNG(t *testing.T, img *image.RGBA, dir, name, theme string) {
	t.Helper()
	path := filepath.Join(dir, name+"-"+theme+".png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("创建快照文件失败: %v", err)
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		t.Fatalf("编码快照失败: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("关闭快照文件失败: %v", err)
	}
	t.Logf("已导出 %s", path)
}

// resetSnapshotUIState 清掉可能跨用例串味的包级 UI 状态。
func resetSnapshotUIState() {
	resetSidebarHoverCardForTest()
	ResetBeeFolded()
	resetArchivePageStateForTest()
}

// buildSnapshotMain 主界面：三个项目、若干会话（含 bee 子代理会话、派生会话、置顶与未读），
// 当前会话里有一段带工具调用的对话。
func buildSnapshotMain(t *testing.T) (func(c *ui.Context), func(tt *ui.Tester)) {
	t.Helper()
	fake := NewFakeClient()
	now := time.Now()
	fake.SetProjectsAndThreads([]Project{
		{ID: "p1", Name: "mcode", Path: "/repo/mcode"},
		{ID: "p2", Name: "lody-mygo", Path: "/repo/lody-mygo"},
		{ID: "p3", Name: "nowledge-mem", Path: "/repo/nowledge-mem"},
	}, map[string][]Thread{
		"p1": {
			{ID: "t1", ProjectID: "p1", Title: "侧边栏状态位", UpdatedAt: now.Add(-2 * time.Minute), Pinned: true},
			{ID: "t2", ProjectID: "p1", Title: "统一确认弹窗", UpdatedAt: now.Add(-35 * time.Minute), HasUnread: true},
			{ID: "bee1", ProjectID: "p1", Title: "Aspen", UpdatedAt: now.Add(-5 * time.Minute), IsHelper: true, ParentThreadID: "t1"},
			{ID: "fork1", ProjectID: "p1", Title: "派生：归档页批量操作", UpdatedAt: now.Add(-20 * time.Minute), ForkedFromThreadID: "t2"},
		},
		"p2": {{ID: "t3", ProjectID: "p2", Title: "悬停信息卡", UpdatedAt: now.Add(-3 * time.Hour)}},
		"p3": {{ID: "t4", ProjectID: "p3", Title: "工作记忆面板", UpdatedAt: now.Add(-26 * time.Hour)}},
	})
	fake.SetMessages("t1", []Message{
		{ID: "u1", Role: "user", Text: "把侧边栏行尾的几个标记收敛成一个状态位。", TurnIndex: 1, IsFinal: true},
		{ID: "a1", Role: "assistant", Text: "先按优先级排序：待审批 > 待办 > 未读 > 时间，只显示最高的那个。", TurnIndex: 1, IsFinal: true},
		{ID: "tool1", Role: "tool", Text: `Bash {"command":"go test ./internal/ui -run TestSidebarStatus"}`, TurnIndex: 1, ToolName: "Bash", ToolInput: `{"command":"go test ./internal/ui -run TestSidebarStatus"}`, ToolStatus: "completed"},
		{ID: "a2", Role: "assistant", Text: "改完了：状态位只留一个槽位，测试通过。", TurnIndex: 1, IsFinal: true},
	})
	// 显式选中 t1，不依赖排序结果。
	view := Root(fake, func(cs *chatState) { cs.initialThreadID = "t1" })
	return view, nil
}

// buildSnapshotComposer 输入 "/" 后弹出的补全面板；命令列表预先塞进缓存，面板同步就能出来。
func buildSnapshotComposer(t *testing.T) (func(c *ui.Context), func(tt *ui.Tester)) {
	t.Helper()
	fake := NewFakeClient()
	s := newChatState(fake)
	s.threads = []Thread{{ID: "t1", ProjectID: "p1", Title: "侧边栏状态位"}}
	s.selectedThreadID = "t1"
	s.commandsCache["t1"] = []SlashCommand{
		{Name: "plan", Detail: "进入计划模式，先出方案再动手"},
		{Name: "models", Detail: "切换当前会话使用的模型"},
		{Name: "compact", Detail: "压缩当前会话的上下文"},
		{Name: "export", Detail: "导出这份会话记录"},
	}
	view := func(c *ui.Context) { renderComposer(c, s) }
	setup := func(tt *ui.Tester) {
		tt.Type("/")
		tt.Advance(snapshotSettle)
		tt.Frame()
	}
	return view, setup
}

// buildSnapshotQuickReplies 设置页「工作台」页里的快捷回复分节。
func buildSnapshotQuickReplies(t *testing.T) (func(c *ui.Context), func(tt *ui.Tester)) {
	t.Helper()
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.currentPage = "workbench"
	st.cfg.QuickReplies = []config.QuickReply{
		{Label: "总结改动", Message: "用要点总结这次改动，并指出风险点。"},
		{Label: "补测试", Message: "为这段逻辑补单元测试，覆盖边界情况。"},
		{Label: "解释报错", Message: "解释这个报错的原因并给出修复方案。"},
	}
	view := func(c *ui.Context) { renderSettingsDetail(c, st) }
	return view, nil
}

// buildSnapshotArchive 设置页的「归档」页，预置三条已归档会话。
func buildSnapshotArchive(t *testing.T) (func(c *ui.Context), func(tt *ui.Tester)) {
	t.Helper()
	fake := NewFakeClient()
	chat := newChatState(fake)
	st := NewSettingsState(settingsTestDir(t), nil, chat)
	now := time.Now()
	globalArchiveState.mu.Lock()
	globalArchiveState.loaded = true
	globalArchiveState.threads = []Thread{
		{ID: "arch-1", ProjectID: "p1", Title: "已归档：清理构建缓存", UpdatedAt: now.Add(-26 * time.Hour)},
		{ID: "arch-2", ProjectID: "p1", Title: "已归档：侧边栏置顶排序", UpdatedAt: now.Add(-3 * 24 * time.Hour)},
		{ID: "arch-3", ProjectID: "p2", Title: "已归档：悬停卡延迟", UpdatedAt: now.Add(-9 * 24 * time.Hour)},
	}
	globalArchiveState.projects = map[string]string{"p1": "mcode", "p2": "lody-mygo"}
	globalArchiveState.mu.Unlock()
	t.Cleanup(resetArchivePageStateForTest)
	view := func(c *ui.Context) {
		ui.Column(c.Key("snapshot-archive")).Width(900).Children(func() {
			renderPageArchive(c, st)
		})
	}
	return view, nil
}

// buildSnapshotConfirm 一个危险态确认弹窗。
func buildSnapshotConfirm(t *testing.T) (func(c *ui.Context), func(tt *ui.Tester)) {
	t.Helper()
	d := ConfirmDialog{
		Key:          "snapshot-confirm",
		Open:         true,
		Tone:         ConfirmDanger,
		Title:        "删除这个会话？",
		Body:         "会话记录会一并删除，删除后无法恢复。",
		Detail:       []string{"会话：侧边栏状态位", "子代理会话：1 条"},
		ConfirmLabel: "删除",
		CancelLabel:  "取消",
	}
	view := func(c *ui.Context) { RenderConfirmDialog(c, d) }
	return view, nil
}
