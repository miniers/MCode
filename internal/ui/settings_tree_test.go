package mcodeui

import (
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

var update = flag.Bool("update", false, "rewrite settings tree golden files")

// TestSettingsBind_PtrValInverted 验证三种绑定：指针字段遇 nil 返回默认值、
// 值字段写回默认值、反向字段读写取反且 reset 仍写字段默认值。
func TestSettingsBind_PtrValInverted(t *testing.T) {
	var cfg config.Config

	preventSleep := ptr(func(c *config.Config) **bool { return &c.Automation.PreventSleep }, true)
	if got := preventSleep.value(&cfg); got != true {
		t.Fatalf("nil 指针应返回默认值 true，实际 %v", got)
	}
	preventSleep.write(&cfg, false)
	if cfg.Automation.PreventSleep == nil || *cfg.Automation.PreventSleep != false {
		t.Fatalf("write 后指针字段应为 false，实际 %#v", cfg.Automation.PreventSleep)
	}
	if got := preventSleep.value(&cfg); got != false {
		t.Fatalf("write 后读回应为 false，实际 %v", got)
	}
	preventSleep.reset(&cfg)
	if cfg.Automation.PreventSleep != nil {
		t.Fatalf("reset 后指针应为 nil，实际 %#v", cfg.Automation.PreventSleep)
	}

	dormantDays := ptr(func(c *config.Config) **int { return &c.Archive.SidebarAutoDormantDays }, 7)
	if got := dormantDays.value(&cfg); got != 7 {
		t.Fatalf("nil 指针应返回默认值 7，实际 %d", got)
	}
	dormantDays.write(&cfg, 3)
	if cfg.Archive.SidebarAutoDormantDays == nil || *cfg.Archive.SidebarAutoDormantDays != 3 {
		t.Fatalf("write 后指针字段应为 3，实际 %#v", cfg.Archive.SidebarAutoDormantDays)
	}
	dormantDays.reset(&cfg)
	if cfg.Archive.SidebarAutoDormantDays != nil {
		t.Fatalf("reset 后指针应为 nil，实际 %#v", cfg.Archive.SidebarAutoDormantDays)
	}

	notifyWhenFinished := val(func(c *config.Config) *bool { return &c.Notifications.NotifyWhenFinished }, true)
	notifyWhenFinished.write(&cfg, false)
	if cfg.Notifications.NotifyWhenFinished {
		t.Fatalf("值字段 write 后应为 false")
	}
	notifyWhenFinished.reset(&cfg)
	if !cfg.Notifications.NotifyWhenFinished {
		t.Fatalf("值字段 reset 后应写回默认值 true")
	}

	notifyWhenForeground := inverted(val(func(c *config.Config) *bool { return &c.Notifications.NotifyWhenFinished }, true))
	if got := notifyWhenForeground.value(&cfg); got != false {
		t.Fatalf("反向绑定读值应为 false，实际 %v", got)
	}
	notifyWhenForeground.write(&cfg, true)
	if cfg.Notifications.NotifyWhenFinished {
		t.Fatalf("反向绑定写 true 应把字段写成 false")
	}
	if got := notifyWhenForeground.value(&cfg); got != true {
		t.Fatalf("反向绑定写后读回应为 true，实际 %v", got)
	}
	notifyWhenForeground.reset(&cfg)
	if !cfg.Notifications.NotifyWhenFinished {
		t.Fatalf("反向绑定 reset 应把字段写回默认值 true")
	}
	if got := notifyWhenForeground.value(&cfg); got != false {
		t.Fatalf("反向绑定 reset 后读回应为 false，实际 %v", got)
	}
}

// settingsTreeFixture 返回渲染、派生与格式测试共用的小页面树。
func settingsTreeFixture() (settingsPage, settingsSheet) {
	page := settingsPage{
		ID:    "general",
		Title: "通用",
		Sections: []settingsSection{{
			ID:    "general.permissions",
			Title: "权限",
			Rows: []setting{
				{ID: "row-mode", Title: "默认权限", Aliases: []string{"mode"},
					Control: choice(val(func(c *config.Config) *string { return &c.Appearance.ContentWidth }, "standard"),
						[]choiceOption{{Value: "standard", Label: "标准"}, {Value: "wide", Label: "宽"}})},
				{ID: "row-days", Title: "保留天数",
					Control: number(ptr(func(c *config.Config) **int { return &c.Archive.SidebarAutoDormantDays }, 7), "天", 0, 90)},
				{ID: "row-shortcuts", Title: "快捷键",
					Control: link(func(st *SettingsState) string { return "⌘K" }, "shortcuts")},
				{ID: "row-providers", Title: "供应商",
					Control: list("row-provider-", func(st *SettingsState) []settingListRow {
						return []settingListRow{{ID: "a", Title: "甲"}}
					})},
			},
		}},
	}
	sheet := settingsSheet{
		ID:    "shortcuts",
		Title: "快捷键",
		Sections: []settingsSection{{
			ID:    "shortcuts.keys",
			Title: "按键",
			Rows: []setting{{
				ID:    "row-global-shortcut",
				Title: "全局快捷键",
				Control: text(val(func(c *config.Config) *string { return &c.Shortcuts.GlobalShortcut }, ""),
					"例如 ⌘⇧K"),
			}},
		}},
	}
	return page, sheet
}

// TestSettingsTreeItems_FromTree 验证派生条目带上页、section、面板归属，list 只出前缀一条。
func TestSettingsTreeItems_FromTree(t *testing.T) {
	page, sheet := settingsTreeFixture()
	items := deriveSettingsTreeItems([]settingsPage{page}, []settingsSheet{sheet})

	want := []struct {
		id, title, page, section, sheet string
	}{
		{"row-mode", "默认权限", "general", "general.permissions", ""},
		{"row-days", "保留天数", "general", "general.permissions", ""},
		{"row-shortcuts", "快捷键", "general", "general.permissions", ""},
		{"row-provider-", "供应商", "general", "general.permissions", ""},
		{"row-global-shortcut", "全局快捷键", "", "shortcuts.keys", "shortcuts"},
	}
	if len(items) != len(want) {
		t.Fatalf("条目数 = %d，想要 %d: %#v", len(items), len(want), items)
	}
	for i, w := range want {
		got := items[i]
		if got.ID != w.id || got.Title != w.title || got.Page != w.page || got.Section != w.section || got.Sheet != w.sheet {
			t.Fatalf("第 %d 条 = %#v，想要 id=%q title=%q page=%q section=%q sheet=%q",
				i, got, w.id, w.title, w.page, w.section, w.sheet)
		}
	}
	if len(items[0].Aliases) != 1 || items[0].Aliases[0] != "mode" {
		t.Fatalf("别名未带出: %#v", items[0].Aliases)
	}
}

// TestSettingsTreeReset_SkipsLocked 验证整页重置跳过锁定行，未锁行回到默认。
func TestSettingsTreeReset_SkipsLocked(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), map[string]bool{"locked-switch": true}, newChatState(NewFakeClient()))
	st.cfg.Notifications.NotifyWhenFinished = false
	st.cfg.Notifications.NotifyWhenForeground = false

	page := settingsPage{
		ID:    "general",
		Title: "通用",
		Sections: []settingsSection{{
			ID:    "general.notifications",
			Title: "通知",
			Rows: []setting{
				{ID: "row-open", Title: "未锁开关",
					Control: toggle(val(func(c *config.Config) *bool { return &c.Notifications.NotifyWhenFinished }, true))},
				{ID: "row-locked", Title: "已锁开关", Lock: "locked-switch",
					Control: toggle(val(func(c *config.Config) *bool { return &c.Notifications.NotifyWhenForeground }, true))},
			},
		}},
	}

	resetSettingsTreePage(st, page)

	if !st.cfg.Notifications.NotifyWhenFinished {
		t.Fatalf("未锁行重置后应回到默认值 true")
	}
	if st.cfg.Notifications.NotifyWhenForeground {
		t.Fatalf("锁定行重置后应保持 false")
	}
}

// TestSettingsTreeRender_Basic 验证声明式页面只画 section 标题与行标题、没有说明句，
// 点击开关会写配置，锁定行显示「锁定」徽标。
func TestSettingsTreeRender_Basic(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), map[string]bool{"locked-switch": true}, newChatState(NewFakeClient()))

	page := settingsPage{
		ID:    "general",
		Title: "通用",
		Sections: []settingsSection{{
			ID:    "general.permissions",
			Title: "权限",
			Rows: []setting{
				{ID: "row-auto-sleep", Title: "自动化防休眠",
					Control: toggle(ptr(func(c *config.Config) **bool { return &c.Automation.PreventSleep }, true))},
				{ID: "row-locked", Title: "锁定开关", Lock: "locked-switch",
					Control: toggle(val(func(c *config.Config) *bool { return &c.Notifications.NotifyWhenFinished }, true))},
			},
		}},
	}

	tt := ui.NewTester(func(c *ui.Context) { renderSettingsTreePage(c, st, page) }, 1000, 800)

	for _, text := range []string{"权限", "自动化防休眠", "锁定开关", "锁定", "切换自动化防休眠", "切换锁定开关"} {
		if !tt.HasText(text) {
			t.Fatalf("页面缺少文案 %q: %v", text, tt.Texts())
		}
	}
	// 声明式行不带说明句：页面上只允许出现上面这些文案。
	allowed := map[string]bool{
		"权限": true, "自动化防休眠": true, "锁定开关": true,
		"锁定": true, "切换自动化防休眠": true, "切换锁定开关": true,
	}
	for _, text := range tt.Texts() {
		if text == "" || allowed[text] || strings.HasPrefix(text, "设置卡片 ") {
			continue
		}
		t.Fatalf("声明式页面出现了多余文案 %q，不允许说明句: %v", text, tt.Texts())
	}

	if err := tt.Click("切换自动化防休眠"); err != nil {
		t.Fatalf("点击开关失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return st.cfg.Automation.PreventSleep != nil && !*st.cfg.Automation.PreventSleep
	})

	// 锁定行的开关处于禁用态：命中与否都不应翻值。
	_ = tt.Click("切换锁定开关")
	if !st.cfg.Notifications.NotifyWhenFinished {
		t.Fatalf("锁定开关的值不应被改变")
	}
}

// TestSettingsTreeSheet_NoLinkRow 验证面板渲染时跳过 link 入口行：面板内只放控件。
func TestSettingsTreeSheet_NoLinkRow(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	sheet := settingsSheet{
		ID:    "shortcuts",
		Title: "快捷键",
		Sections: []settingsSection{{
			ID:    "shortcuts.keys",
			Title: "按键",
			Rows: []setting{
				{ID: "row-target", Title: "目标开关",
					Control: toggle(val(func(c *config.Config) *bool { return &c.Notifications.NotifyWhenFinished }, true))},
				{ID: "row-entry", Title: "全局快捷键",
					Control: link(func(st *SettingsState) string { return "⌘K" }, "other")},
			},
		}},
	}
	openSettingsSheet(st, "shortcuts", "")

	tt := ui.NewTester(func(c *ui.Context) { renderSettingsTreeSheet(c, st, sheet) }, 1000, 900)

	for _, text := range []string{"快捷键", "按键", "目标开关"} {
		if !tt.HasText(text) {
			t.Fatalf("面板缺少文案 %q: %v", text, tt.Texts())
		}
	}
	if tt.HasText("全局快捷键") || tt.HasText("⌘K") {
		t.Fatalf("面板内不应出现入口行: %v", tt.Texts())
	}
}

func TestSettingsTreeRowJumpOnce(t *testing.T) {
	old := settingsRowFocus
	t.Cleanup(func() { settingsRowFocus = old })
	count := 0
	settingsRowFocus.jumpID = "jump-row"
	settingsRowFocus.highlightID = "jump-row"
	settingsRowFocus.onJump = func(key string) {
		if key != "jump-row" {
			t.Errorf("跳转目标 = %q", key)
		}
		count++
	}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Scroll(c).Fill().Children(func() {
			renderSettingRow(c, "jump-row", "跳转标题", func() { ui.Text(c, "跳转控件") })
		})
	}, 320, 200)
	tt.Frame()
	if count != 1 || settingsRowFocus.jumpID != "" || settingsRowFocus.highlightID != "jump-row" {
		t.Fatalf("跳转次数或高亮状态错误：count=%d jump=%q highlight=%q", count, settingsRowFocus.jumpID, settingsRowFocus.highlightID)
	}
}

func TestSettingsTree_RowVisibility(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	showSection, showMiddle := true, false
	ctl := toggle(val(func(c *config.Config) *bool { return &c.Notifications.NotifyWhenFinished }, true))
	section := settingsSection{ID: "visibility", Title: "可见分组", Visible: func(*SettingsState) bool { return showSection }, Rows: []setting{
		{ID: "first", Title: "首行", Control: ctl, Visible: func(*SettingsState) bool { return false }},
		{ID: "middle", Title: "中行", Control: ctl, Visible: func(*SettingsState) bool { return showMiddle }},
		{ID: "last", Title: "末行", Control: ctl, Visible: func(*SettingsState) bool { return false }},
	}}
	page := settingsPage{ID: "visibility", Sections: []settingsSection{section}}
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsTreePage(c, st, page) }, 1000, 800)
	if tt.HasText("可见分组") || tt.HasText("设置卡片 visibility") {
		t.Fatal("全部隐藏时不应渲染分组或卡片")
	}
	showMiddle = true
	tt = ui.NewTester(func(c *ui.Context) { renderSettingsTreePage(c, st, page) }, 1000, 800)
	if !tt.HasText("中行") || tt.HasText("首行") || tt.HasText("末行") {
		t.Fatalf("行可见性错误: %v", tt.Texts())
	}
	section.Rows = append(section.Rows, setting{ID: "second", Title: "第二行", Control: ctl})
	rows := visibleSettingsRows(st, section, false)
	if len(rows) != 2 || rows[0].ID != "middle" || rows[1].ID != "second" {
		t.Fatalf("分隔线应只放在相邻可见行之间: %#v", rows)
	}
	showSection = false
	tt = ui.NewTester(func(c *ui.Context) { renderSettingsTreePage(c, st, page) }, 1000, 800)
	if tt.HasText("可见分组") || tt.HasText("中行") {
		t.Fatal("隐藏分组不应渲染")
	}
}

func TestSettingsTree_ResetSkipsHiddenRows(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), map[string]bool{"locked": true}, newChatState(NewFakeClient()))
	st.cfg.Notifications.NotifyWhenFinished = false
	st.cfg.Notifications.NotifyWhenForeground = false
	st.cfg.Notifications.NotifyWhenFinished = true
	page := settingsPage{Sections: []settingsSection{{Rows: []setting{
		{ID: "visible", Control: toggle(val(func(c *config.Config) *bool { return &c.Notifications.NotifyWhenFinished }, true))},
		{ID: "hidden", Visible: func(st *SettingsState) bool { st.cfgMu.Lock(); defer st.cfgMu.Unlock(); return false }, Control: toggle(val(func(c *config.Config) *bool { return &c.Notifications.NotifyWhenForeground }, true))},
		{ID: "locked", Lock: "locked", Control: toggle(val(func(c *config.Config) *bool { return &c.Notifications.NotifyWhenFinished }, true))},
	}}, {Visible: func(*SettingsState) bool { return false }, Rows: []setting{
		{ID: "hidden-section", Control: toggle(val(func(c *config.Config) *bool { return &c.Notifications.NotifyWhenForeground }, true))},
	}}}}
	resetSettingsTreePage(st, page)
	if !st.cfg.Notifications.NotifyWhenFinished || st.cfg.Notifications.NotifyWhenForeground {
		t.Fatal("重置应只改变可见且未锁定的行")
	}
}

func TestSettingsTree_VisibleSnapshot(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	show := false
	page, sheet := settingsTreeFixture()
	page.Sections[0].Rows[0].Visible = func(st *SettingsState) bool { return st != nil && show }
	all := serializeSettingsTree([]settingsPage{page}, []settingsSheet{sheet})
	before := serializeVisibleSettingsTree(st, []settingsPage{page}, []settingsSheet{sheet})
	show = true
	after := serializeVisibleSettingsTree(st, []settingsPage{page}, []settingsSheet{sheet})
	if !strings.Contains(all, "row row-mode ") || strings.Contains(before, "row row-mode ") || !strings.Contains(after, "row row-mode ") {
		t.Fatal("状态快照与完整结构快照不一致")
	}
}

func TestSettingsLayout_Tokens(t *testing.T) {
	for _, tc := range []struct {
		name      string
		got, want float32
	}{
		{"detail", settingsContentMaxWidth, 680},
		{"sheet", settingsSheetWidth, 600},
		{"wide sheet", settingsSheetWideWidth, 720},
		{"section", settingsSectionGap, 24},
		{"title", settingsSectionTitleGap, 6},
		{"row x", settingsRowPadX, 16},
		{"row y", settingsRowPadY, 6},
		{"row height", settingsRowMinHeight, 44},
		{"divider", Hairline, 1},
		{"divider inset", settingsDividerInset, 16},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %v，期望 %v", tc.name, tc.got, tc.want)
		}
	}
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	page, _ := settingsTreeFixture()
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Scroll(c.Key("layout-scroll")).Fill().Children(func() {
			ui.Column(c.Key("layout-content")).FillWidth().MaxWidth(settingsContentMaxWidth).Children(func() {
				renderSettingsTreePage(c, st, page)
			})
		})
	}, 320, 120)
	card, ok := tt.Find("设置卡片 general.permissions")
	if !ok || card.W > 320 {
		t.Fatalf("窄窗口卡片宽度无效: %#v", card)
	}
	before, ok := tt.Find("保留天数")
	if !ok {
		t.Fatal("缺少行标题")
	}
	tt.Scroll(160, 60, 0, 120)
	after, ok := tt.Find("保留天数")
	if !ok || after.Y >= before.Y {
		t.Fatal("内容没有向上滚动")
	}
	textRow := setting{ID: "layout-text", Title: "长文本", Control: textFunc(
		func(*SettingsState) (string, bool) { return "", false }, nil, "")}
	stacked := ui.NewTester(func(c *ui.Context) { renderSettingsTreeSetting(c, st, textRow) }, 320, 200)
	title, titleOK := stacked.Find("长文本")
	input, inputOK := stacked.Find("编辑长文本")
	if !titleOK || !inputOK || input.Y < title.Y+title.H {
		t.Fatal("长文本控件应位于标题下方")
	}
}

// TestSettingsTree_Snapshot 锁定声明式页面树的可见结构，-update 时重写 golden。
func TestSettingsTree_Snapshot(t *testing.T) {
	got := serializeSettingsTree(settingsTreePages(), settingsTreeSheets())
	path := filepath.Join("testdata", "settings_tree.golden")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("创建 testdata 失败: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("写入 golden 失败: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 golden 失败: %v", err)
	}
	want = []byte(strings.ReplaceAll(string(want), "\r\n", "\n"))
	if runtime.GOOS == "windows" {
		want = []byte(strings.Replace(string(want), "row row-tray 菜单栏图标", "row row-tray 托盘图标", 1))
	}
	if string(want) != got {
		t.Fatalf("页面树快照不一致，运行 go test ./internal/ui -run TestSettingsTree_Snapshot -update 重写\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

// TestSettingsTreeSnapshot_Format 验证序列化格式：页只有 ID，section 与行带标题。
func TestSettingsTreeSnapshot_Format(t *testing.T) {
	page, sheet := settingsTreeFixture()
	got := serializeSettingsTree([]settingsPage{page}, []settingsSheet{sheet})
	want := strings.Join([]string{
		"# settings tree",
		"page general",
		"section general.permissions 权限",
		"row row-mode 默认权限",
		"row row-days 保留天数",
		"row row-shortcuts 快捷键",
		"row row-providers 供应商",
		"sheet shortcuts 快捷键",
		"section shortcuts.keys 按键",
		"row row-global-shortcut 全局快捷键",
		"",
	}, "\n")
	if got != want {
		t.Fatalf("序列化格式 = %q，想要 %q", got, want)
	}
}

// TestSettingsSectionCollapsed_ToggleAndMotion 验证折叠分节默认收起、点击标题展开与收起、
// 尊重 ReduceMotion 并在动画结束后不再请求下一帧。
func TestSettingsSectionCollapsed_ToggleAndMotion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reduced bool
	}{
		{"normal", false},
		{"reduced", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setAppReduceMotion(t, tc.reduced)
			const secID = "test.collapsed.toggle"
			setSettingsSectionExpanded(secID, false)
			t.Cleanup(func() { setSettingsSectionExpanded(secID, false) })

			st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
			page := settingsPage{
				ID:    "test-collapsed",
				Title: "折叠测试",
				Sections: []settingsSection{{
					ID:        secID,
					Title:     "高级设置",
					Collapsed: true,
					Rows: []setting{{
						ID:      "row-collapsed-toggle",
						Title:   "折叠内开关",
						Control: toggle(ptr(func(c *config.Config) **bool { return &c.Memory.InjectMemoryToolsNote }, true)),
					}},
				}},
			}

			tt := ui.NewTesterWith(func(c *ui.Context) {
				renderSettingsTreePage(c, st, page)
			}, 800, 600, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: tc.reduced}})
			tt.Frame()
			if tt.FramePending() {
				t.Fatal("初始静止时不应请求下一帧")
			}
			if !tt.HasText("高级设置") {
				t.Fatalf("缺少折叠分节标题: %v", tt.Texts())
			}
			if tt.HasText("折叠内开关") {
				t.Fatalf("默认折叠分节不应显示内部行: %v", tt.Texts())
			}

			if err := tt.Click("高级设置"); err != nil {
				t.Fatalf("点击展开折叠分节失败: %v", err)
			}
			if !tc.reduced {
				tt.Advance(DurBase + time.Second)
			}
			tt.Frame()
			if tt.FramePending() {
				t.Fatal("展开动画完成后不应继续请求下一帧")
			}
			if !tt.HasText("折叠内开关") {
				t.Fatalf("点击标题后应显示内部行: %v", tt.Texts())
			}

			if err := tt.Click("切换折叠内开关"); err != nil {
				t.Fatalf("点击展开后的内部开关失败: %v", err)
			}
			waitFor(t, tt, func() bool {
				return st.cfg.Memory.InjectMemoryToolsNote != nil && !*st.cfg.Memory.InjectMemoryToolsNote
			})

			if err := tt.Click("高级设置"); err != nil {
				t.Fatalf("点击收起折叠分节失败: %v", err)
			}
			if !tc.reduced {
				tt.Advance(DurBase + time.Second)
			}
			tt.Frame()
			if tt.FramePending() {
				tt.Advance(time.Millisecond)
				t.Logf("after +1ms pending=%v", tt.FramePending())
				tt.Advance(DurBase)
				t.Logf("after +DurBase pending=%v", tt.FramePending())
				t.Fatal("收起动画完成后不应继续请求下一帧")
			}
			if tt.HasText("折叠内开关") {
				t.Fatalf("收起后不应再显示内部行: %v", tt.Texts())
			}
		})
	}
}

// TestSettingsSectionCollapsed_SearchJumpAndHighlight 验证 jumpToItemID 与 highlightItemID
// 命中折叠分节内的行时自动强制展开，定位并高亮目标行。
func TestSettingsSectionCollapsed_SearchJumpAndHighlight(t *testing.T) {
	oldFocus := settingsRowFocus
	t.Cleanup(func() { settingsRowFocus = oldFocus })

	for _, mode := range []string{"jump", "highlight"} {
		t.Run(mode, func(t *testing.T) {
			const secID = "test.collapsed.search"
			setSettingsSectionExpanded(secID, false)
			t.Cleanup(func() { setSettingsSectionExpanded(secID, false) })

			st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
			st.mu.Lock()
			if mode == "jump" {
				st.jumpToItemID = "row-collapsed-target"
			} else {
				st.highlightItemID = "row-collapsed-target"
				st.highlightStartedAt = time.Now()
			}
			st.mu.Unlock()

			page := settingsPage{
				ID:    "test-collapsed-search",
				Title: "搜索直达",
				Sections: []settingsSection{{
					ID:        secID,
					Title:     "高级设置",
					Collapsed: true,
					Rows: []setting{{
						ID:      "row-collapsed-target",
						Title:   "目标高级项",
						Control: toggle(ptr(func(c *config.Config) **bool { return &c.Memory.InjectMemoryToolsNote }, true)),
					}},
				}},
			}

			tt := ui.NewTester(func(c *ui.Context) {
				st.syncSettingsRowFocus()
				ui.Scroll(c.Key("test-collapsed-scroll")).Fill().Children(func() {
					renderSettingsTreePage(c, st, page)
				})
			}, 800, 600)
			tt.Frame()

			if !tt.HasText("目标高级项") {
				t.Fatalf("%s 命中折叠分节后应自动展开并渲染目标行: %v", mode, tt.Texts())
			}
			if mode == "jump" {
				st.mu.Lock()
				jump := st.jumpToItemID
				st.mu.Unlock()
				if jump != "" {
					t.Fatalf("定位消费后 jumpToItemID 应清空，实际 %q", jump)
				}
			}

			tt.Advance(settingsItemHighlightDuration + DurBase + time.Second)
			tt.Frame()
			if tt.FramePending() {
				t.Fatal("搜索展开与高亮动画结束后不应继续请求下一帧")
			}
		})
	}
}
