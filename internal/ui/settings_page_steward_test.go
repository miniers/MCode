package mcodeui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

func clearStewardTestDefaults(st *SettingsState) {
	st.cfg.Orchestration.Steward.Name = ""
	st.cfg.Orchestration.Steward.Avatar = ""
	st.cfg.Orchestration.Steward.QuietStart = ""
	st.cfg.Orchestration.Steward.QuietEnd = ""
}

func newStewardSettingsTester(t *testing.T, st *SettingsState, _ bool) *ui.Tester {
	t.Helper()
	tt := ui.NewTester(func(c *ui.Context) {
		renderPageSwarm(c, st)
		renderSettingsTreeSheet(c, st, sheetSteward)
	}, 1000, 4000)
	label := "总管 关闭"
	if st.cfg.Orchestration.Steward.Enabled {
		label = "总管 开启"
	}
	if err := tt.Click(label); err != nil {
		t.Fatal(err)
	}
	return tt
}

// assertNoStewardWord 断言设置页渲染出的文案不含 forbidden 词。
func assertNoStewardWord(t *testing.T, tt *ui.Tester, forbidden string) {
	t.Helper()
	for _, text := range tt.Texts() {
		if strings.Contains(text, forbidden) {
			t.Fatalf("设置页文案不应出现 %q: %q", forbidden, text)
		}
	}
}

// TestStewardSettings_SectionsAndKeys 验证总管设置页在启用状态下渲染出昵称、头像、说明、模型、
// 主目录，并且未配置时展示归一化后的默认值；权限档已删除，页面文案不含「唤醒」「权限档」。
func TestStewardSettings_SectionsAndKeys(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)

	dataDir := t.TempDir()
	st := NewSettingsState(dataDir, nil, s)
	st.cfg.Orchestration.Steward.Enabled = true
	st.cfg.Orchestration.Steward.DailyWakeLimit = 45

	// 存储分组（记忆与状态目录）在「编排」页底部的高级区里，断言前先展开。
	st.setSettingsAdvancedOpen("swarm", true)
	tt := newStewardSettingsTester(t, st, true)

	for _, text := range []string{
		"昵称",
		"头像",
		"启用总管",
		"总管模型",
		"记忆与状态目录", filepath.Join(dataDir, "steward"),
	} {
		if !tt.HasText(text) {
			t.Fatalf("总管设置页缺少文案 %q: %v", text, tt.Texts())
		}
	}

	// 权限档整行已删除，选项文案也不应再出现在页面上。
	for _, text := range []string{"权限档", "只观察和通知", "可以催促和重跑", "审批只给建议，由你确认"} {
		if tt.HasText(text) {
			t.Fatalf("权限档设置行应已删除，却仍渲染 %q: %v", text, tt.Texts())
		}
	}
	assertNoStewardWord(t, tt, "唤醒")
	assertNoStewardWord(t, tt, "权限档")
	// 内部术语不再出现在设置页上：模型来源与方案模型的档位名都改成了人话。
	assertNoStewardWord(t, tt, "provider")
	assertNoStewardWord(t, tt, "deep 档")
}

// TestStewardSettings_AdvancedAtEnd 验证高级配置位于面板末尾并读取配置值。
func TestStewardSettings_AdvancedAtEnd(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.cfg.Orchestration.Steward.Enabled = true
	st.cfg.Orchestration.Steward.DailyWakeLimit = 45
	sections := stewardSections(st)
	if sections[len(sections)-1].ID != "sec-steward-advanced" {
		t.Fatal("高级配置应位于总管面板末尾")
	}
	tt := newStewardSettingsTester(t, st, false)
	for _, text := range []string{"每天主动提醒上限", "记忆与状态目录"} {
		if !tt.HasText(text) {
			t.Fatalf("总管面板缺少 %q: %v", text, tt.Texts())
		}
	}
	tt.Scroll(500, 2000, 0, 1e6)
	if err := tt.Click("编辑每天主动提醒上限"); err != nil {
		t.Fatal(err)
	}
	tt.Command("selectAll")
	tt.Command("copy")
	if got := tt.Clipboard(); got != "45" {
		t.Fatalf("提醒上限 = %q，想要 45", got)
	}
}

// TestStewardSettings_DisabledHint 验证未启用时隐藏模型与高级配置，只提示启用后可配置。
func TestStewardSettings_DisabledHint(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)

	st := NewSettingsState(settingsTestDir(t), nil, s)
	st.cfg.Orchestration.Steward.Enabled = false

	tt := newStewardSettingsTester(t, st, false)

	for _, text := range []string{"启用总管", "尚未启用"} {
		if !tt.HasText(text) {
			t.Fatalf("未启用时缺少文案 %q: %v", text, tt.Texts())
		}
	}
	for _, text := range []string{"只观察和通知", "可以催促和重跑", "方案模型"} {
		if tt.HasText(text) {
			t.Fatalf("未启用时不应显示 %q: %v", text, tt.Texts())
		}
	}
	assertNoStewardWord(t, tt, "唤醒")
}

// TestStewardSettings_EnabledToggleWritesChoice 验证切换启用开关后写入 EnabledChoice，
// 用户亲手选过的状态不再被后续迁移改写。
func TestStewardSettings_EnabledToggleWritesChoice(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.cfg.Orchestration.Steward.Enabled = false
	if st.cfg.Orchestration.Steward.EnabledChoice {
		t.Fatalf("EnabledChoice 默认应为 false")
	}

	tt := newStewardSettingsTester(t, st, false)
	if err := tt.Click("切换启用总管"); err != nil {
		t.Fatalf("切换启用开关失败: %v", err)
	}
	if !st.cfg.Orchestration.Steward.Enabled {
		t.Fatalf("启用开关应打开")
	}
	if !st.cfg.Orchestration.Steward.EnabledChoice {
		t.Fatalf("切换启用开关后 EnabledChoice 应为 true")
	}
}

// TestStewardNav_Searchable 验证设置导航能按中文别名与英文关键词搜到并入了总管配置的「编排」页。
func TestStewardNav_Searchable(t *testing.T) {
	var swarm *settingsNavDef
	pages := settingsPages()
	for i := range pages {
		if pages[i].id == "swarm" {
			swarm = &pages[i]
		}
	}
	if swarm == nil {
		t.Fatalf("settingsPages() 缺少 id 为 swarm 的页面")
	}
	if swarm.title != "编排" {
		t.Fatalf("编排页标题不正确: %q", swarm.title)
	}

	for _, q := range []string{"默默", "昵称", "总管", "跟进", "总调度", "steward", "待处理", "分诊"} {
		needle := strings.ToLower(q)
		matched := strings.Contains(strings.ToLower(swarm.title), needle) ||
			strings.Contains(strings.ToLower(swarm.id), needle)
		if !matched {
			for _, kw := range swarm.keywords {
				if strings.Contains(strings.ToLower(kw), needle) {
					matched = true
					break
				}
			}
		}
		if !matched {
			t.Fatalf("设置导航搜索 %q 未能命中总管关键词，关键词为 %v", q, swarm.keywords)
		}
	}
}

// TestStewardSettings_QuietHours 验证安静时段开关：打开时写入默认时段并显示范围，关闭时清空配置。
func TestStewardSettings_QuietHours(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)

	st := NewSettingsState(settingsTestDir(t), nil, s)
	clearStewardTestDefaults(st)
	st.cfg.Orchestration.Steward.Enabled = true

	tt := newStewardSettingsTester(t, st, false)
	if !tt.HasText("启用安静时段") || tt.HasText("时间范围") {
		t.Fatalf("关闭时只显示开关: %v", tt.Texts())
	}

	if err := tt.Click("切换启用安静时段"); err != nil {
		t.Fatalf("打开安静时段失败: %v", err)
	}
	tt.Frame()
	if st.cfg.Orchestration.Steward.QuietStart != stewardQuietDefaultStart || st.cfg.Orchestration.Steward.QuietEnd != stewardQuietDefaultEnd {
		t.Fatalf("打开后应写入默认时段: %q %q", st.cfg.Orchestration.Steward.QuietStart, st.cfg.Orchestration.Steward.QuietEnd)
	}
	if !tt.HasText("时间范围") {
		t.Fatalf("打开后应显示时间范围: %v", tt.Texts())
	}
	// 没有行副标题后，起止时刻仍是两个可点的选择框。
	for _, label := range []string{"安静时段开始", "安静时段结束"} {
		if textIndex(tt, label) < 0 {
			t.Fatalf("缺少 %q 选择框: %v", label, tt.Texts())
		}
	}

	if err := tt.Click("切换启用安静时段"); err != nil {
		t.Fatalf("关闭安静时段失败: %v", err)
	}
	tt.Frame()
	if st.cfg.Orchestration.Steward.QuietStart != "" || st.cfg.Orchestration.Steward.QuietEnd != "" {
		t.Fatalf("关闭后应清空时段: %q %q", st.cfg.Orchestration.Steward.QuietStart, st.cfg.Orchestration.Steward.QuietEnd)
	}
}

// TestStewardQuietClockOptions 验证选项含整点与半点，手改配置的时刻也能显示。
func TestStewardQuietClockOptions(t *testing.T) {
	options := stewardQuietClockOptions("23:30")
	if len(options) != 48 || options[0] != "00:00" || options[47] != "23:30" {
		t.Fatalf("默认选项不对: %v", options)
	}
	options = stewardQuietClockOptions("07:15")
	if len(options) != 49 || options[15] != "07:15" {
		t.Fatalf("未知时刻应按序插入: %v", options)
	}
}

// TestStewardSettings_OpenSessionRow 验证「总管会话」入口只在启用且客户端支持总管接口时出现。
func TestStewardSettings_OpenSessionRow(t *testing.T) {
	cases := []struct {
		name    string
		enabled bool
		client  Client
		want    bool
	}{
		{name: "启用且支持", enabled: true, client: newStewardFakeClient("t1"), want: true},
		{name: "未启用", enabled: false, client: newStewardFakeClient("t1"), want: false},
		{name: "客户端不支持", enabled: true, client: NewFakeClient(), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newChatState(tc.client)
			st := NewSettingsState(settingsTestDir(t), nil, s)
			st.cfg.Orchestration.Steward.Enabled = tc.enabled

			tt := newStewardSettingsTester(t, st, false)
			got := tt.HasText("打开总管会话")
			if got != tc.want {
				t.Fatalf("「打开总管会话」入口存在性不对，期望 %v 实际 %v: %v", tc.want, got, tt.Texts())
			}
			if tc.want {
				for _, text := range []string{"总管会话", "打开会话"} {
					if !tt.HasText(text) {
						t.Fatalf("缺少文案 %q: %v", text, tt.Texts())
					}
				}
			}
		})
	}
}

// TestStewardSettings_NameAndAvatar 验证昵称与头像输入：昵称去空白，头像只留前 2 个 rune。
func TestStewardSettings_NameAndAvatar(t *testing.T) {
	s := newChatState(NewFakeClient())
	st := NewSettingsState(settingsTestDir(t), nil, s)
	clearStewardTestDefaults(st)
	st.cfg.Orchestration.Steward.Enabled = true

	tt := newStewardSettingsTester(t, st, false)
	if textIndex(tt, "昵称") < 0 || !tt.HasText("头像") {
		t.Fatalf("缺少昵称或头像行: %v", tt.Texts())
	}

	if err := tt.Click("编辑昵称"); err != nil {
		t.Fatalf("聚焦昵称输入失败: %v", err)
	}
	tt.Type("  小助  ")
	if got := st.cfg.Orchestration.Steward.Name; got != "小助" {
		t.Fatalf("昵称应去空白后写入配置，实际 %q", got)
	}

	if err := tt.Click("编辑头像"); err != nil {
		t.Fatalf("聚焦头像输入失败: %v", err)
	}
	tt.Type("🤖🤖🤖")
	if got := st.cfg.Orchestration.Steward.Avatar; got != "🤖🤖" {
		t.Fatalf("头像只应保留前 2 个 rune，实际 %q", got)
	}
}

// TestStewardSettings_DailyReport 验证日报开关与时间输入：开关写入配置，时间去空白后写入。
func TestStewardSettings_DailyReport(t *testing.T) {
	s := newChatState(NewFakeClient())
	st := NewSettingsState(settingsTestDir(t), nil, s)
	clearStewardTestDefaults(st)
	st.cfg.Orchestration.Steward.Enabled = true

	tt := newStewardSettingsTester(t, st, false)
	if !tt.HasText("日报") {
		t.Fatalf("缺少日报行: %v", tt.Texts())
	}
	if st.cfg.Orchestration.Steward.DailyReport {
		t.Fatalf("日报默认应关闭")
	}

	if err := tt.Click("切换日报"); err != nil {
		t.Fatalf("打开日报失败: %v", err)
	}
	if !st.cfg.Orchestration.Steward.DailyReport {
		t.Fatalf("打开日报后应写入配置")
	}

	// 时间输入不再有副标题说明，标题「日报时间」与输入框自己说明用途。
	if textIndex(tt, "日报时间") < 0 {
		t.Fatalf("缺少日报时间行: %v", tt.Texts())
	}
	tt.Scroll(500, 2000, 0, 1e6)
	if err := tt.Click("编辑日报时间"); err != nil {
		t.Fatalf("聚焦日报时间输入失败: %v", err)
	}
	tt.Type(" 09:30 ")
	if got := st.cfg.Orchestration.Steward.DailyReportAt; got != "09:30" {
		t.Fatalf("日报时间应去空白后写入配置，实际 %q", got)
	}
}

// TestStewardSettings_Passthrough 验证话题直通开关：默认开启，点击后关闭，再点恢复开启。
func TestStewardSettings_Passthrough(t *testing.T) {
	s := newChatState(NewFakeClient())
	st := NewSettingsState(settingsTestDir(t), nil, s)
	st.cfg.Orchestration.Steward.Enabled = true

	tt := newStewardSettingsTester(t, st, false)
	if !tt.HasText("话题事件直通") {
		t.Fatalf("缺少话题直通行: %v", tt.Texts())
	}
	if st.cfg.Orchestration.Steward.PassthroughOff {
		t.Fatalf("话题直通默认应开启")
	}

	tt.Scroll(500, 2000, 0, 1e6)
	if err := tt.Click("切换话题事件直通"); err != nil {
		t.Fatalf("关闭话题直通失败: %v", err)
	}
	if !st.cfg.Orchestration.Steward.PassthroughOff {
		t.Fatalf("关闭话题直通后应写入配置")
	}

	if err := tt.Click("切换话题事件直通"); err != nil {
		t.Fatalf("重新开启话题直通失败: %v", err)
	}
	if st.cfg.Orchestration.Steward.PassthroughOff {
		t.Fatalf("重新开启话题直通后应恢复配置")
	}
}

// stewardWakeSettingsFake 给设置页一个能报今日提醒次数的客户端替身。
type stewardWakeSettingsFake struct {
	*stewardFakeClient
	used, limit int
	ok          bool
}

func (f *stewardWakeSettingsFake) StewardWakesToday() (int, int, bool) {
	return f.used, f.limit, f.ok
}

// 有今日数据时，「每天主动提醒上限」的说明后面补一句已提醒与剩余次数。
func TestStewardSettings_WakeRemainingToday(t *testing.T) {
	fake := &stewardWakeSettingsFake{stewardFakeClient: newStewardFakeClient(""), used: 42, limit: 45, ok: true}
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(fake))
	st.cfg.Orchestration.Steward.Enabled = true
	st.cfg.Orchestration.Steward.DailyWakeLimit = 45
	st.setSettingsAdvancedOpen("swarm", true)

	tt := newStewardSettingsTester(t, st, true)
	waitFor(t, tt, func() bool { return tt.HasText("今天已提醒 42 次，还剩 3 次") })
}

// 客户端拿不到今日数据时不显示这一句，说明保持原文案。
func TestStewardSettings_WakeRemainingTodayHiddenWhenUnavailable(t *testing.T) {
	fake := &stewardWakeSettingsFake{stewardFakeClient: newStewardFakeClient(""), ok: false}
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(fake))
	st.cfg.Orchestration.Steward.Enabled = true
	st.cfg.Orchestration.Steward.DailyWakeLimit = 45
	st.setSettingsAdvancedOpen("swarm", true)

	tt := newStewardSettingsTester(t, st, true)
	// 等后台真的取过一次（ok=false 也算取过），避免只是「还没取到」就下结论。
	waitFor(t, tt, func() bool {
		st.stewardWake.mu.Lock()
		defer st.stewardWake.mu.Unlock()
		return !st.stewardWake.at.IsZero() && !st.stewardWake.ok
	})
	tt.Frame()
	if tt.HasText("还剩") {
		t.Fatalf("拿不到今日数据时不应显示剩余次数，当前文本: %q", tt.Texts())
	}
	if !tt.HasText("每天主动提醒上限") {
		t.Fatalf("提醒上限行本身应照常渲染，当前文本: %q", tt.Texts())
	}
}

func TestStewardSettings_MovedRows(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.cfg.Orchestration.Steward.Enabled = true
	for _, id := range []string{"row-steward-model-plan", "row-steward-home-dir", "row-steward-wake-limit"} {
		for _, sections := range [][]settingsSection{stewardSections(nil), stewardSections(st)} {
			count := 0
			for _, sec := range sections {
				for _, row := range sec.Rows {
					if row.ID == id {
						count++
					}
				}
			}
			if count != 1 {
				t.Fatalf("%s 出现 %d 次", id, count)
			}
		}
		for _, sec := range pageSwarm.Sections {
			for _, row := range sec.Rows {
				if row.ID == id {
					t.Fatalf("%s 不应在编排页出现", id)
				}
			}
		}
		var found bool
		for _, item := range settingsItems() {
			if item.ID != id || item.Sheet != "steward" {
				continue
			}
			for _, hit := range matchVisibleSettingsItemsIn(st, item.Title, []settingsItem{item}) {
				st.focusSettingsItem(hit)
				if sheet, row := settingsTreeSheetState(st); sheet != "steward" || row != id {
					t.Fatalf("搜索跳转错误: %q %q", sheet, row)
				}
				found = true
			}
		}
		if !found {
			t.Fatalf("搜索未找到 %s", id)
		}
	}
	st.cfg.Orchestration.Steward.Enabled = false
	for _, row := range stewardModelSection.Rows {
		if row.ID == "row-steward-model-plan" && settingVisible(st, row) {
			t.Fatal("总管关闭时方案模型不应可见")
		}
	}
}

func TestSettingsSteward_SheetOperations(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.cfg.Orchestration.Steward.Enabled = false
	tt := ui.NewTester(func(c *ui.Context) { renderPageSwarm(c, st); renderSettingsTreeSheet(c, st, sheetSteward) }, 1000, 4000)
	if err := tt.Click("总管 关闭"); err != nil {
		t.Fatal(err)
	}
	if id, _ := settingsTreeSheetState(st); id != "steward" {
		t.Fatalf("面板未打开: %s", id)
	}
	if err := tt.Click("切换启用总管"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(st.dataDir)
	if err != nil || !cfg.Orchestration.Steward.Enabled || !cfg.Orchestration.Steward.EnabledChoice {
		t.Fatalf("总管开关未落盘: %+v, %v", cfg.Orchestration.Steward, err)
	}
}
