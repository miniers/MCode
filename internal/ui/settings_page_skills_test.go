package mcodeui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/agentconf"
)

// TestSettings_SkillToggle 测试技能启停开关的 UI 交互与持久化
func TestSettings_SkillToggle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	origScanSkills := scanSkills
	origScanMCP := scanMCP
	defer func() {
		scanSkills = origScanSkills
		scanMCP = origScanMCP
	}()

	testSkill := agentconf.Skill{
		Agent:       "claude",
		Scope:       "global",
		Name:        "toggleable-skill",
		Description: "可切换启用的技能说明",
		Path:        "/dummy/path/to/toggleable-skill/SKILL.md",
		Enabled:     true,
	}

	scanSkills = func(opts agentconf.Options) ([]agentconf.Skill, error) {
		sk := testSkill
		sk.Enabled = agentconf.IsSkillActive(opts, sk)
		sk.Disabled = !sk.Enabled
		return []agentconf.Skill{sk}, nil
	}
	scanMCP = func(opts agentconf.Options) ([]agentconf.MCPServer, error) {
		return nil, nil
	}

	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, nil)
	tt := ui.NewTester(func(c *ui.Context) { renderPageExtensions(c, st) }, 1000, 2000)

	// 确认技能名称与在访达中显示按钮存在
	waitFor(t, tt, func() bool {
		return tt.HasText("toggleable-skill") && tt.HasText("在访达中显示")
	})

	// 点击技能启停开关
	switchKey := "切换toggleable-skill"
	if err := tt.Click(switchKey); err != nil {
		t.Fatalf("click switch failed: %v", err)
	}

	// 验证持久化层中该技能已变为非激活状态
	opts := agentconf.Options{DataDir: dataDir}
	waitFor(t, tt, func() bool {
		return !agentconf.IsSkillActive(opts, testSkill)
	})
}

func TestSkills_SingleCard(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.agentConfLoaded = true
	st.skills = []agentconf.Skill{
		{Scope: "project", Name: "project-skill", Enabled: true},
		{Agent: "claude", Scope: "global", Name: "global-skill", Enabled: true},
		{Scope: "external", Name: "external-skill", Enabled: true},
	}
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsTreeSection(c, st, sectionSkills, false) }, 1000, 2000)
	for _, text := range []string{"设置卡片 skills.list", "项目技能 (1)", "Claude 全局技能 (1)", "外部与插件技能 (1)", "3 个技能", "打开目录", "重新扫描"} {
		if _, ok := tt.Find(text); !ok {
			t.Fatalf("缺少 %q", text)
		}
	}
	for _, label := range []string{"设置卡片 sec-skills-project", "设置卡片 sec-skills-external", "sec-skills-global-claude-card"} {
		if _, ok := tt.Find(label); ok {
			t.Fatalf("不应存在嵌套卡片 %q", label)
		}
	}
	if err := tt.Click("搜索技能"); err != nil {
		t.Fatalf("点击搜索框失败: %v", err)
	}
	tt.Type("global-skill")
	if !tt.HasText("global-skill") || tt.HasText("project-skill") || tt.HasText("external-skill") {
		t.Fatalf("搜索过滤结果不正确: %v", tt.Texts())
	}
}

func TestSettingsPageSkills_Tree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, nil)
	st.agentConfLoaded = true
	first := agentconf.Skill{Agent: "claude", Scope: "global", Name: "first-skill", Enabled: true}
	second := agentconf.Skill{Agent: "claude", Scope: "global", Name: "second-skill", Enabled: true}
	st.skills = []agentconf.Skill{first, second}
	tt := ui.NewTester(func(c *ui.Context) { renderPageExtensions(c, st) }, 1000, 1000)
	if !tt.HasText("技能") {
		t.Fatal("缺少技能标题")
	}
	a, ok := tt.Find("first-skill")
	if !ok {
		t.Fatal("缺少第一项技能")
	}
	b, ok := tt.Find("second-skill")
	if !ok || b.Y <= a.Y || b.X != a.X {
		t.Fatalf("技能应纵向排列: first=%+v second=%+v found=%v", a, b, ok)
	}
	for _, old := range []string{"技能通过标准 SKILL.md 文件定义说明", "停用仅影响 mcode 注入的技能提示和斜杠命令"} {
		if tt.HasText(old) {
			t.Fatalf("旧说明仍然可见: %s", old)
		}
	}
	if err := tt.Click("切换first-skill"); err != nil {
		t.Fatalf("切换技能失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return !agentconf.IsSkillActive(agentconf.Options{DataDir: dataDir}, first)
	})
}

// TestSkillAgentLabel 测试 Agent 徽标可读名称映射：非 ASCII 首字按 rune 大写，空串保持空。
func TestSkillAgentLabel(t *testing.T) {
	cases := []struct {
		name  string
		agent string
		want  string
	}{
		{"empty", "", ""},
		{"known", "antigravity", "Antigravity"},
		{"ascii", "mcode", "Mcode"},
		{"chinese", "中文", "中文"},
		{"emoji", "🤖bot", "🤖bot"},
		{"accented", "éclair", "Éclair"},
		{"invalid-utf8", "\xffbot", "\xffbot"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := skillAgentLabel(tc.agent); got != tc.want {
				t.Fatalf("skillAgentLabel(%q) = %q, want %q", tc.agent, got, tc.want)
			}
		})
	}
}

// TestCapitalizeFirstRune 测试按 rune 首字母大写：空串、ASCII、中文首字与多字节符号都不能被切碎。
func TestCapitalizeFirstRune(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"空串", "", ""},
		{"ascii", "claude", "Claude"},
		{"首字中文", "中文技能", "中文技能"},
		{"首字多字节符号", "★技能", "★技能"},
		{"拉丁重音", "émile", "Émile"},
		{"首字大写已大写", "Mcode", "Mcode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := capitalizeFirstRune(tc.in); got != tc.want {
				t.Fatalf("capitalizeFirstRune(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSkillMatchesQuery 测试技能搜索匹配：名称、描述、作用域、Agent 标签与路径。
func TestSkillMatchesQuery(t *testing.T) {
	sk := agentconf.Skill{
		Agent:       "cursor",
		Scope:       "global",
		Name:        "find-skills",
		Description: "发现技能",
		Path:        "/tmp/cursor-skills/find-skills/SKILL.md",
	}
	cases := []struct {
		query string
		want  bool
	}{
		{"", true},
		{"find", true},
		{"CURSOR", true},
		{"发现", true},
		{"global", true},
		{"/tmp/cursor", true},
		{"antigravity", false},
		{"codex", false},
	}
	for _, c := range cases {
		if got := skillMatchesQuery(sk, c.query); got != c.want {
			t.Errorf("skillMatchesQuery(%q) = %v, want %v", c.query, got, c.want)
		}
	}
}

// TestApplySkillEnabledRollsBackOnWriteError 覆盖：技能启停写盘失败时不改内存状态（开关即回弹）
// 并把原因写进页面错误区，批量切换失败汇总条数（S-113）。
func TestApplySkillEnabledRollsBackOnWriteError(t *testing.T) {
	dataDir := settingsTestDir(t)
	// 用同名目录占住 skills.json，让写盘必然失败。
	if err := os.MkdirAll(filepath.Join(dataDir, "skills.json"), 0o755); err != nil {
		t.Fatal(err)
	}

	st := NewSettingsState(dataDir, nil, nil)
	sk := agentconf.Skill{Agent: "claude", Scope: "global", Name: "demo-skill", Enabled: true}
	st.skills = []agentconf.Skill{sk}

	applySkillEnabled(st, sk, false)

	if !st.skills[0].Enabled || st.skills[0].Disabled {
		t.Fatalf("写盘失败时不应改内存状态，实际 Enabled=%v Disabled=%v", st.skills[0].Enabled, st.skills[0].Disabled)
	}
	if !strings.Contains(st.agentConfWriteErr, "demo-skill") {
		t.Fatalf("写盘失败应写进页面错误区，实际 %q", st.agentConfWriteErr)
	}

	st.setAgentConfWriteErr("")
	setSkillsEnabled(st, []agentconf.Skill{sk}, false)
	if !strings.Contains(st.agentConfWriteErr, "1 个技能切换失败") {
		t.Fatalf("批量切换失败应汇总条数，实际 %q", st.agentConfWriteErr)
	}
	if !st.skills[0].Enabled {
		t.Fatalf("批量切换失败时也不应改内存状态")
	}
}

// TestSameSkillDistinguishesPlugin 覆盖：同名同作用域但插件不同的两条技能不是同一条，
// 否则停用一个插件里的技能会把其他插件里的同名技能一起改掉。
func TestSameSkillDistinguishesPlugin(t *testing.T) {
	base := agentconf.Skill{Agent: "claude", Scope: "external", Plugin: "superpowers", Name: "brainstorming"}
	other := agentconf.Skill{Agent: "claude", Scope: "external", Plugin: "qa", Name: "brainstorming"}

	if !sameSkill(base, base) {
		t.Fatalf("完全相同的一条记录应判定为同一条技能")
	}
	if sameSkill(base, other) {
		t.Fatalf("仅插件名不同不应判定为同一条技能")
	}
	if sameSkill(other, base) {
		t.Fatalf("仅插件名不同不应判定为同一条技能（反向）")
	}
}

// TestRenderSkillListShowsPluginBadge 覆盖：插件技能在列表里显示「插件 · <插件名>」标签，
// 并把本页的停用影响提示渲染出来。
func TestRenderSkillListShowsPluginBadge(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	origScanSkills := scanSkills
	origScanMCP := scanMCP
	defer func() {
		scanSkills = origScanSkills
		scanMCP = origScanMCP
	}()

	scanSkills = func(opts agentconf.Options) ([]agentconf.Skill, error) {
		return []agentconf.Skill{{
			Agent:       "claude",
			Scope:       "external",
			Plugin:      "superpowers",
			Name:        "brainstorming",
			Description: "插件里的技能",
			Path:        "/dummy/path/to/brainstorming/SKILL.md",
			Enabled:     true,
		}}, nil
	}
	scanMCP = func(opts agentconf.Options) ([]agentconf.MCPServer, error) {
		return nil, nil
	}

	st := NewSettingsState(settingsTestDir(t), nil, nil)
	tt := ui.NewTester(func(c *ui.Context) { renderPageExtensions(c, st) }, 1000, 2000)

	waitFor(t, tt, func() bool {
		return tt.HasText("插件 · superpowers") && tt.HasText("brainstorming")
	})
	if tt.HasText("停用仅影响 mcode 注入的技能提示和斜杠命令") {
		t.Fatalf("技能页不应显示旧说明")
	}
}

// TestRenderSkillListShowsInjectBadge 覆盖：列表按路径标注技能是否会被 mcode 注入——
// 全局 .agents/skills 下的技能显示「mcode 注入」，.claude/skills 下的显示「Agent 自行加载」，
// 即使两者的 Agent 标签都不是注入主力也不按 Agent 判定（M-301）。
func TestRenderSkillListShowsInjectBadge(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	origScanSkills := scanSkills
	origScanMCP := scanMCP
	defer func() {
		scanSkills = origScanSkills
		scanMCP = origScanMCP
	}()

	scanSkills = func(opts agentconf.Options) ([]agentconf.Skill, error) {
		return []agentconf.Skill{
			{
				Agent:       "codex",
				Scope:       "global",
				Name:        "injected-skill",
				Description: "由 mcode 注入的技能",
				Path:        filepath.Join(home, ".agents", "skills", "injected-skill", "SKILL.md"),
				Enabled:     true,
			},
			{
				Agent:       "claude",
				Scope:       "global",
				Name:        "agent-loaded-skill",
				Description: "由 Agent 自行加载的技能",
				Path:        filepath.Join(home, ".claude", "skills", "agent-loaded-skill", "SKILL.md"),
				Enabled:     true,
			},
		}, nil
	}
	scanMCP = func(opts agentconf.Options) ([]agentconf.MCPServer, error) {
		return nil, nil
	}

	st := NewSettingsState(settingsTestDir(t), nil, nil)
	tt := ui.NewTester(func(c *ui.Context) { renderPageExtensions(c, st) }, 1000, 2000)

	waitFor(t, tt, func() bool {
		return tt.HasText("mcode 注入") && tt.HasText("Agent 自行加载") && tt.HasText("injected-skill")
	})
	if tt.HasText("标记为\"mcode 注入\"的技能会进入提示词") {
		t.Fatalf("技能页不应显示旧说明")
	}
}

// TestSettingsPageSkills_SearchFiltersByTitle 覆盖顶部搜索框：按标题过滤、无匹配时给空态提示。
// 搜索文本住在 SettingsState 上（见 getSkillsSearchText），所以直接改它也能驱动过滤。
func TestSettingsPageSkills_SearchFiltersByTitle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.agentConfLoaded = true
	st.skills = []agentconf.Skill{
		{Agent: "claude", Scope: "global", Name: "first-skill", Enabled: true},
		{Agent: "claude", Scope: "global", Name: "second-skill", Enabled: true},
	}
	tt := ui.NewTester(func(c *ui.Context) { renderPageExtensions(c, st) }, 1000, 4000)
	if !tt.HasText("first-skill") || !tt.HasText("second-skill") {
		t.Fatalf("未过滤时应列出全部技能: %v", tt.Texts())
	}

	// 点搜索框再输入：Tester 认 Label「搜索技能」，不认 c.Key。
	if err := tt.Click("搜索技能"); err != nil {
		t.Fatalf("点击搜索框失败: %v", err)
	}
	tt.Type("FIRST")
	if !tt.HasText("first-skill") {
		t.Fatalf("命中搜索词的技能应保留: %v", tt.Texts())
	}
	if tt.HasText("second-skill") {
		t.Fatalf("不匹配搜索词的技能应被过滤: %v", tt.Texts())
	}

	// 换一个不存在的关键字：只剩空态提示。
	tt.Command("selectAll")
	tt.Type("不存在的技能")
	if tt.HasText("first-skill") || tt.HasText("second-skill") {
		t.Fatalf("无匹配时不应列出技能: %v", tt.Texts())
	}
	if !tt.HasText("无匹配的技能") {
		t.Fatalf("无匹配时应给空态提示: %v", tt.Texts())
	}
}

// TestSettingsPageSkills_StateLivesOnSettingsState 覆盖页面状态收口：同一 st 两次取 getter 拿到同一个指针，
// 关面板后 skillsSearch 与 nowledgePage 清空，再打开时新建。
func TestSettingsPageSkills_StateLivesOnSettingsState(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	search := getSkillsSearchText(st)
	if search != getSkillsSearchText(st) {
		t.Fatal("同一 st 两次取搜索文本应返回同一指针")
	}
	*search = "first"
	space := getNowledgeSpacePageState(st)
	if space != getNowledgeSpacePageState(st) {
		t.Fatal("同一 st 两次取记忆空间页状态应返回同一指针")
	}

	st.Close()

	st.mu.Lock()
	searchField, spaceField := st.skillsSearch, st.nowledgePage
	st.mu.Unlock()
	if searchField != nil || spaceField != nil {
		t.Fatalf("Close 应清空页面状态，实际 skillsSearch=%v nowledgePage=%v", searchField, spaceField)
	}
	if getSkillsSearchText(st) == search {
		t.Fatal("重新打开应新建搜索文本，不复用已清空的对象")
	}
	if getNowledgeSpacePageState(st) == space {
		t.Fatal("重新打开应新建记忆空间页状态，不复用已清空的对象")
	}
}

// TestApplySkillEnabledOnlyTouchesSameAgent 覆盖 S-114：开关的乐观更新按 Agent + 作用域 + 名字匹配，
// 停用 claude 下的 foo 不应把 cursor 下的同名 foo 也改掉。
func TestApplySkillEnabledOnlyTouchesSameAgent(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	claudeSkill := agentconf.Skill{Agent: "claude", Scope: "global", Name: "foo", Enabled: true}
	cursorSkill := agentconf.Skill{Agent: "cursor", Scope: "global", Name: "foo", Enabled: true}
	st.skills = []agentconf.Skill{claudeSkill, cursorSkill}

	applySkillEnabled(st, claudeSkill, false)

	if st.agentConfWriteErr != "" {
		t.Fatalf("写盘应成功，实际错误 %q", st.agentConfWriteErr)
	}
	if st.skills[0].Enabled || !st.skills[0].Disabled {
		t.Fatalf("claude 的 foo 应被停用，实际 Enabled=%v Disabled=%v", st.skills[0].Enabled, st.skills[0].Disabled)
	}
	if !st.skills[1].Enabled || st.skills[1].Disabled {
		t.Fatalf("cursor 的 foo 不应受影响，实际 Enabled=%v Disabled=%v", st.skills[1].Enabled, st.skills[1].Disabled)
	}
}
