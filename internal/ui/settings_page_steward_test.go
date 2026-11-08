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

func assertNoStewardWord(t *testing.T, tt *ui.Tester, forbidden string) {
	t.Helper()
	for _, text := range tt.Texts() {
		if strings.Contains(text, forbidden) {
			t.Fatalf("设置页文案不应出现 %q: %q", forbidden, text)
		}
	}
}

func TestStewardSettings_SectionsAndKeys(t *testing.T) {
	st := NewSettingsState(t.TempDir(), nil, newChatState(NewFakeClient()))
	st.cfg.Orchestration.Steward.Enabled = true
	st.setSettingsAdvancedOpen("swarm", true)
	tt := newStewardSettingsTester(t, st, true)

	for _, text := range []string{
		"昵称", "头像", "启用总管", "没有 API Key 时用本机 CLI", "前台模型",
		"启用安静时段", "主目录", filepath.Join(st.dataDir, "steward"),
	} {
		if !tt.HasText(text) {
			t.Fatalf("总管设置页缺少文案 %q: %v", text, tt.Texts())
		}
	}
	for _, text := range []string{"日报", "每天主动提醒上限", "方案模型", "话题事件直通", "自动折叠思考过程", "建议计分"} {
		if tt.HasText(text) {
			t.Fatalf("失效设置行仍渲染 %q: %v", text, tt.Texts())
		}
	}
	assertNoStewardWord(t, tt, "权限档")
}

func TestStewardSettings_AdvancedAtEnd(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.cfg.Orchestration.Steward.Enabled = true
	sections := stewardSections(st)
	if sections[len(sections)-1].ID != "sec-steward-advanced" {
		t.Fatal("高级配置应位于总管面板末尾")
	}
	tt := newStewardSettingsTester(t, st, false)
	if !tt.HasText("主目录") {
		t.Fatalf("总管面板缺少主目录: %v", tt.Texts())
	}
	if tt.HasText("每天主动提醒上限") {
		t.Fatal("提醒上限行已删除")
	}
}

func TestStewardSettings_Disabled(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.cfg.Orchestration.Steward.Enabled = false
	tt := newStewardSettingsTester(t, st, false)
	if !tt.HasText("启用总管") {
		t.Fatalf("未启用时缺少总开关: %v", tt.Texts())
	}
	for _, text := range []string{"前台模型", "启用安静时段", "方案模型", "尚未启用"} {
		if tt.HasText(text) {
			t.Fatalf("未启用时不应显示 %q", text)
		}
	}
}

func TestStewardSettings_CLIFallbackToggle(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.cfg.Orchestration.Steward.CLIFallback = false
	tt := newStewardSettingsTester(t, st, false)
	if err := tt.Click("切换没有 API Key 时用本机 CLI"); err != nil {
		t.Fatalf("切换 CLI 降级开关失败: %v", err)
	}
	if !st.cfg.Orchestration.Steward.CLIFallback {
		t.Fatal("CLI 降级开关应打开")
	}
}

func TestStewardSettings_EnabledToggleWritesChoice(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.cfg.Orchestration.Steward.Enabled = false
	tt := newStewardSettingsTester(t, st, false)
	if err := tt.Click("切换启用总管"); err != nil {
		t.Fatalf("切换启用开关失败: %v", err)
	}
	if !st.cfg.Orchestration.Steward.Enabled || !st.cfg.Orchestration.Steward.EnabledChoice {
		t.Fatalf("启用开关或 EnabledChoice 未写入: %+v", st.cfg.Orchestration.Steward)
	}
}

func TestStewardNav_Searchable(t *testing.T) {
	var swarm *settingsNavDef
	pages := settingsPages()
	for i := range pages {
		if pages[i].id == "swarm" {
			swarm = &pages[i]
		}
	}
	if swarm == nil || swarm.title != "编排" {
		t.Fatalf("设置导航缺少编排页")
	}
	for _, q := range []string{"默默", "昵称", "总管", "steward"} {
		matched := strings.Contains(strings.ToLower(swarm.title), strings.ToLower(q)) || strings.Contains(strings.ToLower(swarm.id), strings.ToLower(q))
		for _, kw := range swarm.keywords {
			matched = matched || strings.Contains(strings.ToLower(kw), strings.ToLower(q))
		}
		if !matched {
			t.Fatalf("设置导航搜索 %q 未命中总管关键词，关键词为 %v", q, swarm.keywords)
		}
	}
}

func TestStewardSettings_QuietHours(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
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
		t.Fatalf("打开后应写入默认时段")
	}
	if !tt.HasText("时间范围") {
		t.Fatalf("打开后应显示时间范围: %v", tt.Texts())
	}
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
		t.Fatalf("关闭后应清空时段")
	}
}

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

func TestStewardSettings_NameAndAvatar(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	clearStewardTestDefaults(st)
	st.cfg.Orchestration.Steward.Enabled = true
	tt := newStewardSettingsTester(t, st, false)
	if err := tt.Click("编辑昵称"); err != nil {
		t.Fatalf("聚焦昵称输入失败: %v", err)
	}
	tt.Type("  小助  ")
	if st.cfg.Orchestration.Steward.Name != "小助" {
		t.Fatalf("昵称应去空白后写入配置")
	}
	if err := tt.Click("编辑头像"); err != nil {
		t.Fatalf("聚焦头像输入失败: %v", err)
	}
	tt.Type("🤖🤖🤖")
	if st.cfg.Orchestration.Steward.Avatar != "🤖🤖" {
		t.Fatalf("头像只应保留前 2 个 rune")
	}
}

func TestStewardSettings_MovedRows(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.cfg.Orchestration.Steward.Enabled = true
	foundHome := false
	for _, sec := range stewardSections(st) {
		for _, row := range sec.Rows {
			foundHome = foundHome || row.ID == "row-steward-home-dir"
		}
	}
	if !foundHome {
		t.Fatal("总管设置页缺少主目录")
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
