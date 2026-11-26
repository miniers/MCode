package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/agentconf"
	"mcode/internal/mcp"
	"mcode/internal/testenv"
)

func assertUnfoldMovesTargetDown(t *testing.T, tt *ui.Tester, clickLabel, targetLabel string) {
	t.Helper()
	if err := tt.Click(clickLabel); err != nil {
		t.Fatalf("点击 %q 失败: %v", clickLabel, err)
	}
	first, ok := tt.Find(targetLabel)
	if !ok {
		t.Fatalf("首帧未找到下方元素 %q: %v", targetLabel, tt.Texts())
	}

	tt.Advance(motionUnfold / 2)
	mid, ok := tt.Find(targetLabel)
	if !ok {
		t.Fatalf("展开途中未找到下方元素 %q: %v", targetLabel, tt.Texts())
	}

	tt.Advance(motionUnfold + time.Second)
	if tt.FramePending() {
		t.Fatal("展开动画结束后不应继续请求下一帧")
	}
	end, ok := tt.Find(targetLabel)
	if !ok {
		t.Fatalf("展开结束后未找到下方元素 %q: %v", targetLabel, tt.Texts())
	}
	if !(first.Y < mid.Y && mid.Y < end.Y) {
		t.Fatalf("元素 %q 的 Y 坐标应平滑递增：首帧=%v 途中=%v 结束=%v", targetLabel, first.Y, mid.Y, end.Y)
	}
}

func assertUnfoldReducedMotion(t *testing.T, tt *ui.Tester, clickLabel, targetLabel string) {
	t.Helper()
	before, ok := tt.Find(targetLabel)
	if !ok {
		t.Fatalf("展开前未找到下方元素 %q: %v", targetLabel, tt.Texts())
	}
	if err := tt.Click(clickLabel); err != nil {
		t.Fatalf("点击 %q 失败: %v", clickLabel, err)
	}
	if tt.FramePending() {
		t.Fatal("ReduceMotion 开启时展开不应请求后续帧")
	}
	first, ok := tt.Find(targetLabel)
	if !ok {
		t.Fatalf("首帧未找到下方元素 %q: %v", targetLabel, tt.Texts())
	}
	tt.Advance(motionUnfold + time.Second)
	end, ok := tt.Find(targetLabel)
	if !ok {
		t.Fatalf("推进后未找到下方元素 %q: %v", targetLabel, tt.Texts())
	}
	if first.Y <= before.Y || first.Y != end.Y {
		t.Fatalf("ReduceMotion 开启时首帧应直接到达最终位置：展开前=%v 首帧=%v 结束=%v", before.Y, first.Y, end.Y)
	}
}

func newMCPDetailUnfoldTestSetup(t *testing.T, expanded bool) (*SettingsState, *mcpPageUIState) {
	t.Helper()
	home := testenv.TempHome(t)
	dataDir := settingsTestDir(t)
	srv := agentconf.MCPServer{
		Name:      "unfold-mcp",
		Agent:     "custom",
		Scope:     "global",
		Transport: "stdio",
		Command:   "echo",
	}
	if err := agentconf.SaveMCPServer(agentconf.Options{Home: home, DataDir: dataDir}, srv); err != nil {
		t.Fatalf("保存自定义 MCP 服务失败: %v", err)
	}
	st := NewSettingsState(dataDir, nil, nil)
	st.mu.Lock()
	st.agentConfLoaded = true
	st.mcpServers = []agentconf.MCPServer{srv}
	st.mu.Unlock()

	uiSt := getMCPUIState(st)
	key := mcp.EndpointKey(srv.Agent, srv.Scope, srv.Name)
	uiSt.mu.Lock()
	uiSt.selectedServer = key
	uiSt.expandedServers[key] = expanded
	uiSt.toolsCache[key] = []agentconf.MCPToolInfo{{Name: "tool-1"}, {Name: "tool-2"}}
	uiSt.mu.Unlock()
	return st, uiSt
}

func TestUnfoldMotionSettingsMCPDetailEdit(t *testing.T) {
	setAppReduceMotion(t, false)
	st, _ := newMCPDetailUnfoldTestSetup(t, true)
	tt := ui.NewTester(func(c *ui.Context) { renderMCPDetailSheet(c, st) }, 900, 1200)
	waitFor(t, tt, func() bool {
		return tt.HasText("编辑") && tt.HasText("支持的工具 (2):")
	})
	settleFrames(t, tt)
	assertUnfoldMovesTargetDown(t, tt, "编辑", "支持的工具 (2):")
}

func TestUnfoldMotionSettingsMCPTools(t *testing.T) {
	setAppReduceMotion(t, false)
	st, _ := newMCPDetailUnfoldTestSetup(t, false)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("test-mcp-tools-shell")).Width(900).Children(func() {
			renderMCPDetailSheet(c, st)
			ui.Text(c, "mcp-tools-below")
		})
	}, 900, 1200)
	waitFor(t, tt, func() bool {
		return tt.HasText("展开工具") && tt.HasText("mcp-tools-below")
	})
	settleFrames(t, tt)
	assertUnfoldMovesTargetDown(t, tt, "展开工具", "mcp-tools-below")
}

func TestUnfoldMotionSettingsMCPCatalogForm(t *testing.T) {
	setAppReduceMotion(t, false)
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.mu.Lock()
	st.agentConfLoaded = true
	st.mu.Unlock()
	catSt := getMCPCatalogUIState(st)
	uiSt := getMCPUIState(st)
	row := mcpCatalogRow{
		view: mcpCatalogEntryView{
			key: "mcp-catalog-tavily",
			entry: agentconf.MCPCatalogEntry{
				ID:      "tavily",
				Name:    "Tavily",
				Vendor:  "Tavily",
				Summary: "网页搜索",
				Fields: []agentconf.MCPCatalogField{
					{Key: "key", Label: "API 密钥", Kind: agentconf.MCPCatalogFieldSecret},
				},
			},
		},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("test-mcp-catalog-shell")).Width(900).Children(func() {
			renderMCPCatalogCard(c, st, catSt, uiSt, row, nil)
			ui.Text(c, "mcp-catalog-below")
		})
	}, 900, 1200)
	waitFor(t, tt, func() bool {
		return tt.HasText("添加") && tt.HasText("mcp-catalog-below")
	})
	settleFrames(t, tt)
	assertUnfoldMovesTargetDown(t, tt, "添加", "mcp-catalog-below")
}

func TestUnfoldMotionSettingsAboutChangelog(t *testing.T) {
	setAppReduceMotion(t, false)
	resetAboutPageStateForTest()
	t.Cleanup(resetAboutPageStateForTest)
	aboutChangelogExpanded = map[string]bool{}
	t.Cleanup(func() { aboutChangelogExpanded = map[string]bool{} })

	versions := changelogVersions()
	if len(versions) < 2 {
		t.Fatal("缺少至少两个更新日志版本")
	}
	firstVer := "版本 " + versions[0].Version
	secondVer := "版本 " + versions[1].Version

	fc := &generalAboutClient{&fakeUpdateClient{
		FakeClient: NewFakeClient(),
		version:    "1.2.3",
		status:     AppUpdateStatus{Current: "1.2.3", Latest: "1.2.4", URL: "https://example.com/dl", Available: true},
	}}
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(fc))
	tt := ui.NewTester(func(c *ui.Context) { renderPageAbout(c, st) }, 640, 4000)
	waitFor(t, tt, func() bool {
		_, ok1 := tt.Find(firstVer)
		_, ok2 := tt.Find(secondVer)
		return ok1 && ok2
	})
	settleFrames(t, tt)
	assertUnfoldMovesTargetDown(t, tt, firstVer, secondVer)
}

func TestUnfoldMotionSettingsOpenInAddForm(t *testing.T) {
	setAppReduceMotion(t, false)
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("test-openin-shell")).Width(800).Children(func() {
			renderPageOpenIn(c, st)
			ui.Text(c, "openin-below")
		})
	}, 800, 1000)
	waitFor(t, tt, func() bool {
		return tt.HasText("添加应用…") && tt.HasText("openin-below")
	})
	settleFrames(t, tt)
	assertUnfoldMovesTargetDown(t, tt, "添加应用…", "openin-below")
}

func TestUnfoldMotionSettingsMagpieOwnGroup(t *testing.T) {
	setAppReduceMotion(t, false)
	testenv.TempHome(t)
	magpieOwnModelGroups.Lock()
	delete(magpieOwnModelGroups.expanded, "claude")
	magpieOwnModelGroups.Unlock()
	t.Cleanup(func() {
		magpieOwnModelGroups.Lock()
		delete(magpieOwnModelGroups.expanded, "claude")
		magpieOwnModelGroups.Unlock()
	})

	st := NewSettingsState(settingsTestDir(t), nil, nil)
	g := &piModelGroup{
		Title: "claude 自身配置",
		Rows: []piModelRow{
			{Model: ModelInfo{ID: "claude-sonnet", Name: "Claude Sonnet"}},
			{Model: ModelInfo{ID: "claude-haiku", Name: "Claude Haiku"}},
		},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("test-magpie-own-shell")).Width(800).Children(func() {
			renderMagpieOwnModelGroup(c, st, nil, nil, ProviderInfo{}, "claude", nil, g, false)
			ui.Text(c, "magpie-own-below")
		})
	}, 800, 1000)
	waitFor(t, tt, func() bool {
		return tt.HasText("claude 自身配置") && tt.HasText("magpie-own-below")
	})
	settleFrames(t, tt)
	assertUnfoldMovesTargetDown(t, tt, "展开 claude 自身配置", "magpie-own-below")
}

func newMemoryProposalUnfoldTestTester(t *testing.T, reduced bool) *ui.Tester {
	t.Helper()
	fake := NewFakeClient()
	s := newChatState(fake)
	t.Cleanup(s.close)

	st := getMemoryPanelState(s)
	memoryPanelMu.Lock()
	st.open = true
	st.projectPath = ""
	st.proposals = []MemoryProposal{
		{
			ID:         "mp-unfold-1",
			Title:      "提议一",
			Content:    "第一条提议内容",
			UnitType:   "fact",
			Importance: 0.7,
			Source:     "agent",
			Status:     "pending",
		},
		{
			ID:         "mp-unfold-2",
			Title:      "提议二",
			Content:    "第二条提议内容",
			UnitType:   "fact",
			Importance: 0.6,
			Source:     "agent",
			Status:     "pending",
		},
	}
	memoryPanelMu.Unlock()

	return ui.NewTesterWith(func(c *ui.Context) { renderMemoryModal(c, s) }, 760, 1000, ui.TesterOptions{
		Preferences: ui.Preferences{ReduceMotion: reduced},
	})
}

func TestUnfoldMotionSettingsMemoryProposalEditor(t *testing.T) {
	setAppReduceMotion(t, false)
	tt := newMemoryProposalUnfoldTestTester(t, false)
	waitFor(t, tt, func() bool {
		return tt.HasText("提议一") && tt.HasText("提议二")
	})
	settleFrames(t, tt)
	assertUnfoldMovesTargetDown(t, tt, "提议一", "提议二")
}

func TestUnfoldMotionSettingsContextDraftPreview(t *testing.T) {
	t.Run("attachment", func(t *testing.T) {
		setAppReduceMotion(t, false)
		testenv.TempHome(t)
		s := newChatState(NewFakeClient())
		t.Cleanup(s.close)
		s.selectedThreadID = "t1"
		s.threads = []Thread{{ID: "t1"}}

		if !s.addAttachmentToThread("t1", "评论.txt", "text/plain", []byte("第一行评论\n第二行评论\n第三行评论\n第四行评论"), nil) {
			t.Fatal("添加 评论.txt 失败")
		}
		waitAttachmentsUploaded(t, s, 1)

		s.mu.Lock()
		s.snippets = []composerSnippet{
			{ID: 1, Text: "片段一标题\n片段一正文\n片段一第三行\n片段一第四行"},
		}
		s.mu.Unlock()
		openComposerContextDraftModal(s, "")

		tt := ui.NewTester(func(c *ui.Context) { renderComposerContextDraftModal(c, s) }, 800, 800)
		waitFor(t, tt, func() bool {
			return tt.HasText("评论.txt") && tt.HasText("片段一标题")
		})
		settleFrames(t, tt)

		assertUnfoldMovesTargetDown(t, tt, "展开 评论.txt", "片段一标题")
	})

	t.Run("snippet", func(t *testing.T) {
		setAppReduceMotion(t, false)
		testenv.TempHome(t)
		s := newChatState(NewFakeClient())
		t.Cleanup(s.close)
		s.selectedThreadID = "t1"
		s.threads = []Thread{{ID: "t1"}}
		s.mu.Lock()
		s.snippets = []composerSnippet{
			{ID: 1, Text: "片段一标题\n片段一正文\n片段一第三行\n片段一第四行"},
			{ID: 2, Text: "片段二标题"},
		}
		s.mu.Unlock()
		openComposerContextDraftModal(s, "")

		tt := ui.NewTester(func(c *ui.Context) { renderComposerContextDraftModal(c, s) }, 800, 800)
		waitFor(t, tt, func() bool {
			return tt.HasText("片段一标题") && tt.HasText("片段二标题")
		})
		settleFrames(t, tt)

		assertUnfoldMovesTargetDown(t, tt, "展开 片段一标题", "片段二标题")
	})
}

func TestUnfoldMotionSettingsReduceMotion(t *testing.T) {
	t.Run("mcp_tools", func(t *testing.T) {
		setAppReduceMotion(t, true)
		st, _ := newMCPDetailUnfoldTestSetup(t, false)
		tt := ui.NewTesterWith(func(c *ui.Context) {
			ui.Column(c.Key("test-mcp-tools-reduced")).Width(900).Children(func() {
				renderMCPDetailSheet(c, st)
				ui.Text(c, "mcp-tools-below")
			})
		}, 900, 1200, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
		waitFor(t, tt, func() bool {
			return tt.HasText("展开工具") && tt.HasText("mcp-tools-below")
		})
		settleFrames(t, tt)
		assertUnfoldReducedMotion(t, tt, "展开工具", "mcp-tools-below")
	})

	t.Run("about_changelog", func(t *testing.T) {
		setAppReduceMotion(t, true)
		resetAboutPageStateForTest()
		t.Cleanup(resetAboutPageStateForTest)
		aboutChangelogExpanded = map[string]bool{}
		t.Cleanup(func() { aboutChangelogExpanded = map[string]bool{} })

		versions := changelogVersions()
		if len(versions) < 2 {
			t.Fatal("缺少至少两个更新日志版本")
		}
		firstVer := "版本 " + versions[0].Version
		secondVer := "版本 " + versions[1].Version
		fc := &generalAboutClient{&fakeUpdateClient{
			FakeClient: NewFakeClient(),
			version:    "1.2.3",
			status:     AppUpdateStatus{Current: "1.2.3", Latest: "1.2.4", URL: "https://example.com/dl", Available: true},
		}}
		st := NewSettingsState(settingsTestDir(t), nil, newChatState(fc))
		tt := ui.NewTesterWith(func(c *ui.Context) { renderPageAbout(c, st) }, 640, 4000, ui.TesterOptions{
			Preferences: ui.Preferences{ReduceMotion: true},
		})
		waitFor(t, tt, func() bool {
			_, ok1 := tt.Find(firstVer)
			_, ok2 := tt.Find(secondVer)
			return ok1 && ok2
		})
		settleFrames(t, tt)
		assertUnfoldReducedMotion(t, tt, firstVer, secondVer)
	})

	t.Run("memory_proposal", func(t *testing.T) {
		setAppReduceMotion(t, true)
		tt := newMemoryProposalUnfoldTestTester(t, true)
		waitFor(t, tt, func() bool {
			return tt.HasText("提议一") && tt.HasText("提议二")
		})
		settleFrames(t, tt)
		assertUnfoldReducedMotion(t, tt, "提议一", "提议二")
	})
}
