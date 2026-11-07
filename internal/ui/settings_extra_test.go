package mcodeui

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mcode/internal/agentconf"
	"mcode/internal/config"
)

func init() {
	// 参照 settings_test.go / main_test.go 绕开 mygo.Theme.SetSource 阻塞
	applyThemeSource = func(mygo.ThemeSource) {}
}

// TestSettingsExtra_NavItems 覆盖：导航里出现扩展、通用、版本控制和记忆。
func TestSettingsExtra_NavItems(t *testing.T) {
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 3000)

	waitFor(t, tt, func() bool {
		return tt.HasText("设置")
	})

	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click '设置' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("扩展") && tt.HasText("通用") && tt.HasText("版本控制") && tt.HasText("记忆")
	})
}

// TestSettingsExtra_SkillsPage 覆盖：进入技能页能看到假 skill 名称与供应商分组。
func TestSettingsExtra_SkillsPage(t *testing.T) {
	origScanSkills := scanSkills
	origScanMCP := scanMCP
	defer func() {
		scanSkills = origScanSkills
		scanMCP = origScanMCP
	}()

	scanSkills = func(opts agentconf.Options) ([]agentconf.Skill, error) {
		return []agentconf.Skill{
			{
				Agent:       "claude",
				Scope:       "global",
				Name:        "fake-custom-skill",
				Description: "这是一个测试专用的假技能描述文本",
				Path:        "/dummy/path/to/skill",
			},
		}, nil
	}
	scanMCP = func(opts agentconf.Options) ([]agentconf.MCPServer, error) {
		return nil, nil
	}

	fake := NewFakeClient()
	dataDir := settingsTestDir(t)
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 3000)

	waitFor(t, tt, func() bool {
		return tt.HasText("设置")
	})

	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click '设置' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("扩展")
	})

	if err := tt.Click("扩展"); err != nil {
		t.Fatalf("click '技能' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("fake-custom-skill") &&
			tt.HasText("Claude 全局技能 (1)")
	})
}

// TestSettingsExtra_MCPPage 覆盖：进入 MCP 页能看到假服务名、命令与环境变量名且看不到任何值。
func TestSettingsExtra_MCPPage(t *testing.T) {
	origScanSkills := scanSkills
	origScanMCP := scanMCP
	defer func() {
		scanSkills = origScanSkills
		scanMCP = origScanMCP
	}()

	scanSkills = func(opts agentconf.Options) ([]agentconf.Skill, error) {
		return nil, nil
	}
	scanMCP = func(opts agentconf.Options) ([]agentconf.MCPServer, error) {
		return []agentconf.MCPServer{
			{
				Agent:     "claude",
				Scope:     "project",
				Name:      "fake-database-mcp",
				Transport: "stdio",
				Command:   "docker-compose-mcp",
				Args:      []string{"--arg1", "val1"},
				EnvKeys:   []string{"SECRET_API_KEY", "DB_PASSWORD"},
				Source:    "/workspace/.mcp.json",
			},
		}, nil
	}

	fake := NewFakeClient()
	dataDir := settingsTestDir(t)
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 3000)

	waitFor(t, tt, func() bool {
		return tt.HasText("设置")
	})

	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click '设置' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("扩展")
	})

	if err := tt.Click("扩展"); err != nil {
		t.Fatalf("click 'MCP' failed: %v", err)
	}

	openMCPDetail(t, tt, "fake-database-mcp")
	waitFor(t, tt, func() bool {
		return tt.HasText("fake-database-mcp") &&
			tt.HasText("docker-compose-mcp --arg1 val1") &&
			tt.HasText("环境变量：SECRET_API_KEY, DB_PASSWORD")
	})

	// 验证环境变相仅显示名字，绝无敏感值或真实值泄露
	allTexts := strings.Join(tt.Texts(), " ")
	if strings.Contains(allTexts, "SECRET_API_KEY=") || strings.Contains(allTexts, "DB_PASSWORD=") {
		t.Fatalf("expected no env values in rendered output, got: %s", allTexts)
	}
}

// TestSettingsExtra_ShortcutsPage 覆盖：快捷键页能看到“新建会话”和“⌘N”。
func TestSettingsExtra_ShortcutsPage(t *testing.T) {
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 3000)

	openShortcutsSettingsPage(t, tt)

	waitFor(t, tt, func() bool {
		return tt.HasText("新建会话") &&
			tt.HasText("⌘N") &&
			tt.HasText("关闭设置") &&
			tt.HasText("Esc")
	})
}

// TestSettingsExtra_GlobalShortcutToggle 覆盖：全局快捷键行用统一开关控件，在通用页按 Label
// 点开关就能开关全局热键，并把结果落盘。不在快捷键面板里点：面板是模态框，背后开关点不中。
func TestSettingsExtra_GlobalShortcutToggle(t *testing.T) {
	dataDir := settingsTestDir(t)
	tt := ui.NewTester(Root(NewFakeClient(), WithSettings(dataDir, nil)), 1000, 4000)

	openGeneralSettingsPage(t, tt)
	waitFor(t, tt, func() bool {
		return tt.HasText("全局快捷键") && tt.HasText("全部快捷键")
	})

	// 默认关闭；点一下打开并落盘。
	if err := settingsClick(t, tt, "切换全局快捷键"); err != nil {
		t.Fatalf("click '切换全局快捷键' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		cfg, err := config.Load(dataDir)
		return err == nil && cfg.Shortcuts.GlobalShortcutEnabled
	})

	// 再点一下关掉，写盘同样跟随。
	if err := settingsClick(t, tt, "切换全局快捷键"); err != nil {
		t.Fatalf("click '切换全局快捷键' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		cfg, err := config.Load(dataDir)
		return err == nil && !cfg.Shortcuts.GlobalShortcutEnabled
	})
}

// TestSettingsExtra_MCPAuthButtons 覆盖：URL 服务器显示登录按钮，stdio 服务器不显示。
func TestSettingsExtra_MCPAuthButtons(t *testing.T) {
	origScanSkills := scanSkills
	origScanMCP := scanMCP
	defer func() {
		scanSkills = origScanSkills
		scanMCP = origScanMCP
	}()

	scanSkills = func(opts agentconf.Options) ([]agentconf.Skill, error) {
		return nil, nil
	}
	scanMCP = func(opts agentconf.Options) ([]agentconf.MCPServer, error) {
		return []agentconf.MCPServer{
			{
				Agent:     "claude",
				Scope:     "global",
				Name:      "remote-http-mcp",
				Transport: "http",
				URL:       "https://mcp.example.com",
				Source:    "/home/user/.claude.json",
			},
			{
				Agent:     "claude",
				Scope:     "global",
				Name:      "local-stdio-mcp",
				Transport: "stdio",
				Command:   "node",
				Args:      []string{"server.js"},
				Source:    "/home/user/.claude.json",
			},
		}, nil
	}

	fake := NewFakeClient()
	dataDir := settingsTestDir(t)
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 3000)

	waitFor(t, tt, func() bool {
		return tt.HasText("设置")
	})

	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click '设置' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("扩展")
	})

	if err := tt.Click("扩展"); err != nil {
		t.Fatalf("click 'MCP' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("remote-http-mcp") && tt.HasText("local-stdio-mcp")
	})

	openMCPDetail(t, tt, "local-stdio-mcp")
	waitFor(t, tt, func() bool { return tt.HasText("node server.js") })
	if tt.HasText("登录") {
		t.Fatal("stdio 服务不应显示登录按钮")
	}
	if err := tt.Click("关闭MCP 服务"); err != nil {
		t.Fatal(err)
	}
	openMCPDetail(t, tt, "remote-http-mcp")
	waitFor(t, tt, func() bool { return tt.HasText("登录") && tt.HasText("未登录") })

	// URL 服务器显示“未登录”状态和“登录”按钮
	if !tt.HasText("登录") {
		t.Fatalf("expected '登录' button rendered for remote-http-mcp")
	}
	if !tt.HasText("未登录") {
		t.Fatalf("expected '未登录' status rendered for remote-http-mcp")
	}

	// 验证 stdio 服务器不显示登录按钮（整个界面只有 1 个“登录”按钮）
	texts := tt.Texts()
	loginCount := 0
	for _, txt := range texts {
		if txt == "登录" {
			loginCount++
		}
	}
	if loginCount != 1 {
		t.Errorf("expected exactly 1 '登录' button (for URL server only, stdio should not show), got %d", loginCount)
	}

	fake.mu.Lock()
	fake.mcpSignInErr = fmt.Errorf("OAuth test failure")
	fake.mu.Unlock()
	if err := tt.Click("登录"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("OAuth test failure") })
	fake.mu.Lock()
	fake.mcpSignInErr = nil
	fake.mu.Unlock()
	if err := tt.Click("登录"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("已登录") && tt.HasText("重新授权") && tt.HasText("退出") })
	if err := tt.Click("重新授权"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		calls := len(fake.mcpSignInCalls)
		fake.mu.Unlock()
		return calls == 3 && tt.HasText("退出")
	})
	if err := tt.Click("退出"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("未登录") && tt.HasText("登录") })
	fake.mu.Lock()
	defer fake.mu.Unlock()
	wantID := (agentconf.MCPServer{Name: "remote-http-mcp", URL: "https://mcp.example.com"}).AuthID()
	if len(fake.mcpSignOutCalls) != 1 || fake.mcpSignOutCalls[0] != wantID {
		t.Fatal("OAuth 按钮未使用与代理一致的凭据 ID")
	}
}

// openGeneralSettingsPage 打开设置并切到通用页，不打开快捷键面板。
func openGeneralSettingsPage(t *testing.T, tt *ui.Tester) {
	t.Helper()
	waitFor(t, tt, func() bool { return tt.HasText("设置") })
	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click '设置' failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("通用") })
	if err := settingsClick(t, tt, "通用"); err != nil {
		t.Fatalf("click '通用' failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("全部快捷键") })
}

// openShortcutsSettingsPage 从通用页打开快捷键面板。
func openShortcutsSettingsPage(t *testing.T, tt *ui.Tester) {
	t.Helper()
	openGeneralSettingsPage(t, tt)
	countLabel := ""
	for _, text := range tt.Texts() {
		if strings.HasSuffix(text, " 个") {
			countLabel = text
			break
		}
	}
	if countLabel == "" {
		t.Fatalf("找不到快捷键数量: %v", tt.Texts())
	}
	if err := settingsClick(t, tt, countLabel); err != nil {
		t.Fatalf("click %q failed: %v", countLabel, err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("关闭快捷键") && tt.HasText("全部恢复默认") })
}

// clickShortcutControl 在面板内滚动到控件后点击。
func clickShortcutControl(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	for i := 0; i < 40; i++ {
		sheet, ok := tt.Find("设置面板 快捷键")
		if !ok {
			t.Fatalf("找不到快捷键面板: %v", tt.Texts())
		}
		if r, found := tt.Find(label); found {
			y := r.Y + r.H/2
			if y >= sheet.Y+50 && y <= sheet.Y+sheet.H-20 {
				if err := tt.Click(label); err != nil {
					t.Fatal(err)
				}
				return
			}
			dy := float32(200)
			if y < sheet.Y+50 {
				dy = -200
			}
			tt.Scroll(sheet.X+sheet.W/2, sheet.Y+sheet.H/2, 0, dy)
		} else {
			tt.Scroll(sheet.X+sheet.W/2, sheet.Y+sheet.H/2, 0, 200)
		}
		tt.Frame()
	}
	t.Fatalf("快捷键面板中元素 %q 始终不可见: %v", label, tt.Texts())
}

// TestSettingsExtra_ShortcutsCustomizeAndReset 覆盖：输入框改键立即生效并落盘，行重置与全部恢复默认清空覆盖。
func TestSettingsExtra_ShortcutsCustomizeAndReset(t *testing.T) {
	t.Cleanup(func() { setShortcutOverrides(nil) })
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 3000)

	openShortcutsSettingsPage(t, tt)

	if tt.HasText("点击输入框修改，格式如 Cmd+Shift+P；留空恢复默认") {
		t.Fatal("快捷键面板不应显示自定义说明")
	}
	if tt.HasText("快捷键暂不支持自定义。") {
		t.Fatal("旧的只读提示不应再出现")
	}

	// 输入框使用操作标题作为无障碍名称，不暴露内部键。
	waitFor(t, tt, func() bool { _, ok := tt.Find("快捷键 显示/隐藏改动"); return ok })
	if _, ok := tt.Find("input-shortcut-toggle-diff"); ok {
		t.Fatal("快捷键输入框不应暴露内部键")
	}

	// 修改「显示/隐藏改动」为 ⌥⌘D（⇧⌘D 已归「转发到新会话」，改用空闲的 Alt+Cmd+D）
	clickShortcutControl(t, tt, "快捷键 显示/隐藏改动")
	tt.Type("Alt+Cmd+D")
	waitFor(t, tt, func() bool {
		id, _, ok := matchShortcut(ui.Cmd|ui.Alt, ui.KeyD)
		return ok && id == "toggle-diff"
	})
	waitFor(t, tt, func() bool { return tt.HasText("⌥⌘D") })
	waitFor(t, tt, func() bool {
		cfg, err := config.Load(dataDir)
		return err == nil && cfg.Shortcuts.ShortcutOverrides["toggle-diff"] == "Alt+Cmd+D"
	})

	// 行内「重置」清空该条覆盖，旧键恢复
	waitFor(t, tt, func() bool { return tt.HasText("重置 显示/隐藏改动") })
	clickShortcutControl(t, tt, "重置 显示/隐藏改动")
	waitFor(t, tt, func() bool {
		id, _, ok := matchShortcut(ui.Cmd, ui.KeyD)
		return ok && id == "toggle-diff"
	})
	waitFor(t, tt, func() bool {
		cfg, err := config.Load(dataDir)
		return err == nil && len(cfg.Shortcuts.ShortcutOverrides) == 0
	})

	// 再设一条覆盖后「全部恢复默认」
	clickShortcutControl(t, tt, "快捷键 显示/隐藏改动")
	tt.Type("Alt+Cmd+D")
	waitFor(t, tt, func() bool {
		id, _, ok := matchShortcut(ui.Cmd|ui.Alt, ui.KeyD)
		return ok && id == "toggle-diff"
	})
	if err := settingsClick(t, tt, "全部恢复默认"); err != nil {
		t.Fatalf("click reset all failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, _, ok := matchShortcut(ui.Cmd|ui.Alt, ui.KeyD)
		return !ok
	})
	waitFor(t, tt, func() bool {
		cfg, err := config.Load(dataDir)
		return err == nil && len(cfg.Shortcuts.ShortcutOverrides) == 0
	})
}

// TestSettingsExtra_ShortcutInvalidOverride 覆盖：非法键位显示红色提示且不写入配置。
func TestSettingsExtra_ShortcutInvalidOverride(t *testing.T) {
	t.Cleanup(func() { setShortcutOverrides(nil) })
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 3000)

	openShortcutsSettingsPage(t, tt)

	clickShortcutControl(t, tt, "快捷键 显示/隐藏改动")
	tt.Type("Cmd+Foo")
	waitFor(t, tt, func() bool { return tt.HasText("无法解析") })

	cfg, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("load config failed: %v", err)
	}
	if len(cfg.Shortcuts.ShortcutOverrides) != 0 {
		t.Fatalf("非法输入不应写入配置，得到 %v", cfg.Shortcuts.ShortcutOverrides)
	}
	if id, _, ok := matchShortcut(ui.Cmd, ui.KeyD); !ok || id != "toggle-diff" {
		t.Fatalf("非法覆盖后 ⌘D 应保持默认绑定，得到 id=%q ok=%v", id, ok)
	}
}

// TestSettingsExtra_ShortcutConflictWarning 覆盖：把「显示/隐藏改动」改成 ⌘B 时显示冲突提示。
func TestSettingsExtra_ShortcutConflictWarning(t *testing.T) {
	t.Cleanup(func() { setShortcutOverrides(nil) })
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 3000)

	openShortcutsSettingsPage(t, tt)

	clickShortcutControl(t, tt, "快捷键 显示/隐藏改动")
	tt.Type("Cmd+B")
	waitFor(t, tt, func() bool { return tt.HasText("与「显示/隐藏侧边栏」冲突") })
}

// TestSettingsExtra_ShortcutReservedOverride 覆盖：系统保留组合显示提示、不生效、不落盘（J-06）。
func TestSettingsExtra_ShortcutReservedOverride(t *testing.T) {
	t.Cleanup(func() { setShortcutOverrides(nil) })
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 3000)

	openShortcutsSettingsPage(t, tt)

	clickShortcutControl(t, tt, "快捷键 显示/隐藏改动")
	tt.Type("Cmd+Q")
	waitFor(t, tt, func() bool { return tt.HasText(shortcutReservedMessage) })

	cfg, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("load config failed: %v", err)
	}
	if len(cfg.Shortcuts.ShortcutOverrides) != 0 {
		t.Fatalf("系统保留组合不应写入配置，得到 %v", cfg.Shortcuts.ShortcutOverrides)
	}
	if mods, key, ok := shortcutBindingFor("toggle-diff"); !ok || mods != ui.Cmd || key != ui.KeyD {
		t.Fatalf("系统保留组合不应生效，实际绑定 %v/%v", mods, key)
	}
}

// TestSettingsState_AgentConfWriteOptionsFollowScanDir 覆盖：技能/MCP 的写盘参数沿用扫描时记下的
// 项目目录，切项目（当前目录与扫描目录不一致）后重新扫描（J-07）。
func TestSettingsState_AgentConfWriteOptionsFollowScanDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	origScanSkills, origScanMCP := scanSkills, scanMCP
	defer func() {
		scanSkills, scanMCP = origScanSkills, origScanMCP
	}()

	var mu sync.Mutex
	var scannedDirs []string
	scanSkills = func(opts agentconf.Options) ([]agentconf.Skill, error) {
		mu.Lock()
		scannedDirs = append(scannedDirs, opts.ProjectDir)
		mu.Unlock()
		return nil, nil
	}
	scanMCP = func(agentconf.Options) ([]agentconf.MCPServer, error) { return nil, nil }

	// chat 为 nil 时当前项目目录为空，用来模拟「扫描时是 A，当前变成空」的切换。
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.loadAgentConf("/tmp/project-a")
	waitForAutoDormant(t, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.agentConfLoaded
	})

	if got := st.agentConfWriteOptions().ProjectDir; got != "/tmp/project-a" {
		t.Fatalf("写盘参数应沿用扫描时的项目目录，实际 %q", got)
	}

	st.ensureAgentConfCurrent()
	waitForAutoDormant(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(scannedDirs) == 2
	})
	if got := st.agentConfWriteOptions().ProjectDir; got != "" {
		t.Fatalf("切项目后应重扫并采用新目录，实际 %q", got)
	}
}

// TestSettingsExtra_ShortcutDraftDebouncedSave 覆盖快捷键输入的落盘节流：连续输入只改草稿、
// 合并成一次落盘；失焦不等防抖窗口，立即落盘。
func TestSettingsExtra_ShortcutDraftDebouncedSave(t *testing.T) {
	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, nil)

	var saves atomic.Int64
	settingsSaveHook = func() { saves.Add(1) }
	t.Cleanup(func() {
		settingsSaveHook = nil
		setShortcutOverrides(nil)
	})
	// 用例结束时清掉还没落盘的草稿，避免临时数据目录被删后再写盘。
	t.Cleanup(func() { st.flushShortcutDrafts(true) })

	draft := shortcutDraftFor("toggle-diff")
	for _, text := range []string{"A", "Al", "Alt", "Alt+Cmd+D"} {
		*draft = text
		st.noteShortcutDraftEdit("toggle-diff")
	}
	base := saves.Load()

	// 防抖窗口内不落盘（输入期间只动草稿）。
	time.Sleep(settingsShortcutSaveDebounce / 2)
	st.flushShortcutDrafts(false)
	if got := saves.Load(); got != base {
		t.Fatalf("防抖窗口内不应落盘，实际多写了 %d 次", got-base)
	}
	if cfg, err := config.Load(dataDir); err == nil && len(cfg.Shortcuts.ShortcutOverrides) != 0 {
		t.Fatalf("防抖窗口内磁盘上不应有覆盖，实际 %v", cfg.Shortcuts.ShortcutOverrides)
	}

	// 停手超过窗口后只写一次，值取最后一次输入。
	time.Sleep(settingsShortcutSaveDebounce/2 + settingsTextSaveGrace)
	st.flushShortcutDrafts(false)
	if got := saves.Load(); got != base+1 {
		t.Fatalf("连续输入多个字符应只落盘一次，实际 %d 次", got-base)
	}
	cfg, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("load saved config failed: %v", err)
	}
	if got := cfg.Shortcuts.ShortcutOverrides["toggle-diff"]; got != "Alt+Cmd+D" {
		t.Fatalf("落盘值应为最后一次输入 Alt+Cmd+D，实际 %q", got)
	}
	if id, _, ok := matchShortcut(ui.Cmd|ui.Alt, ui.KeyD); !ok || id != "toggle-diff" {
		t.Fatalf("落盘后应生效: id=%q ok=%v", id, ok)
	}

	// 失焦立即落盘，不等防抖窗口。
	*draft = "Alt+Cmd+K"
	st.trackShortcutDraftFocus("toggle-diff", true)
	st.noteShortcutDraftEdit("toggle-diff")
	if !st.trackShortcutDraftFocus("toggle-diff", false) {
		t.Fatal("获得过焦点后的失焦应被识别")
	}
	blurBase := saves.Load()
	st.flushShortcutDrafts(true)
	if got := saves.Load(); got != blurBase+1 {
		t.Fatalf("失焦应立即落盘一次，实际写了 %d 次", got-blurBase)
	}
	cfg, err = config.Load(dataDir)
	if err != nil {
		t.Fatalf("load saved config failed: %v", err)
	}
	if got := cfg.Shortcuts.ShortcutOverrides["toggle-diff"]; got != "Alt+Cmd+K" {
		t.Fatalf("失焦后的落盘值应为 Alt+Cmd+K，实际 %q", got)
	}
}
