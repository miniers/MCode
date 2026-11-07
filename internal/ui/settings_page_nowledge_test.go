package mcodeui

import (
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

func renderTestNowledgeSections(c *ui.Context, st *SettingsState) {
	ui.Column(c.Key("test-nowledge-sections")).Children(func() {
		for _, section := range nowledgeSections {
			renderSettingsTreeSection(c, st, section, false)
		}
	})
}

// TestNowledgeSettings_SetMemoryBackendPersists 验证后端选择是页面树里的选项行：
// 点选后立即写内存配置并落盘。
func TestNowledgeSettings_SetMemoryBackendPersists(t *testing.T) {
	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, nil)
	tt := ui.NewTester(func(c *ui.Context) { renderTestNowledgeSections(c, st) }, 1000, 1600)

	// 后端是分段控件，按可见文案定位（Tester 认 Label，不认 c.Key）。
	if err := tt.Click("本地文档"); err != nil {
		t.Fatalf("选择本地后端失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.cfg.Memory.Backend == config.MemoryBackendLocal
	})
	cfg, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("load saved config failed: %v", err)
	}
	if cfg.Memory.Backend != config.MemoryBackendLocal {
		t.Fatalf("backend 未落盘: %q", cfg.Memory.Backend)
	}
}

// TestNowledgeSettings_SectionsAndKeys 验证记忆设置页的行标题都渲染、旧说明文字全部删除。
func TestNowledgeSettings_SectionsAndKeys(t *testing.T) {
	s := newChatState(NewFakeClient())
	st := NewSettingsState(settingsTestDir(t), nil, s)
	tt := ui.NewTester(func(c *ui.Context) { renderPageNowledge(c, st) }, 1000, 5000)

	for _, text := range []string{
		"Nowledge Mem", // 后端分段控件的选项
		"记忆后端",
		"新会话注入 Working Memory",
		"同步会话到 Nowledge",
		"派发子代理时附带相关记忆",
	} {
		if !tt.HasText(text) {
			t.Fatalf("记忆设置页缺少文案 %q: %v", text, tt.Texts())
		}
	}

	// 说明文字一律删除：检索词进了别名，默认值进了 placeholder，单位单独显示。
	for _, text := range []string{
		"默认开启；首轮把当前项目的记忆摘要写进提示词",
		"默认开启；首轮把记忆工具用法写进提示词",
		"默认 8000 字符，超出会截断",
		"默认开启；空闲 20 分钟或归档时自动同步，记忆仍需确认",
		"默认关闭；子代理会话多是任务说明，没有保存价值",
		"默认关闭；开启后自动化运行也会同步",
		"按任务检索最多 5 条记忆附在提示词后",
		"8k 字符",
	} {
		if tt.HasText(text) {
			t.Fatalf("旧说明文字不应再出现 %q: %v", text, tt.Texts())
		}
	}

	// 开关按「切换+标题」作为 Label，逐个确认可定位。
	for _, label := range []string{
		"切换新会话注入 Working Memory",
		"切换同步会话到 Nowledge",
		"切换派发子代理时附带相关记忆",
	} {
		if _, ok := tt.Find(label); !ok {
			t.Fatalf("记忆设置页缺少开关 %q", label)
		}
	}
}

// TestNowledgeSettings_ToggleAndPersist 验证扩展页开关切换后写回配置并落盘。
func TestNowledgeSettings_ToggleAndPersist(t *testing.T) {
	s := newChatState(NewFakeClient())
	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, s)
	tt := ui.NewTester(func(c *ui.Context) { renderTestNowledgeSections(c, st) }, 1000, 1600)

	initialInject := st.cfg.Memory.WorkingMemoryEnabled()
	if err := tt.Click("切换新会话注入 Working Memory"); err != nil {
		t.Fatalf("click working memory switch failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return st.cfg.Memory.InjectWorkingMemory != nil &&
			*st.cfg.Memory.InjectWorkingMemory != initialInject
	})

	initialSync := st.cfg.Memory.ThreadSyncEnabled()
	if err := tt.Click("切换同步会话到 Nowledge"); err != nil {
		t.Fatalf("click thread sync switch failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return st.cfg.Memory.SyncThreads != nil && *st.cfg.Memory.SyncThreads != initialSync
	})

	initialBeeRecall := st.cfg.Memory.BeeRecallEnabled()
	if err := tt.Click("切换派发子代理时附带相关记忆"); err != nil {
		t.Fatalf("click bee recall switch failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return st.cfg.Memory.BeeRecall != nil && *st.cfg.Memory.BeeRecall != initialBeeRecall
	})

	cfg, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("load saved config failed: %v", err)
	}
	if cfg.Memory.WorkingMemoryEnabled() == initialInject {
		t.Fatalf("Working Memory 注入开关未持久化: %+v", cfg.Memory)
	}
	if cfg.Memory.ThreadSyncEnabled() == initialSync {
		t.Fatalf("会话同步开关未持久化: %+v", cfg.Memory)
	}
	if cfg.Memory.BeeRecallEnabled() == initialBeeRecall {
		t.Fatalf("bee 记忆召回开关未持久化: %+v", cfg.Memory)
	}
	if cfg.Memory.WorkingMemoryBudget() != config.DefaultWorkingMemoryMaxCharacters {
		t.Fatalf("默认注入上限应保持 %d，实际 %d", config.DefaultWorkingMemoryMaxCharacters, cfg.Memory.WorkingMemoryBudget())
	}
}

// TestApplySpaceOverride 验证下拉框选择写回 space_overrides 的三种情况。
func TestApplySpaceOverride(t *testing.T) {
	t.Run("选具体空间写入", func(t *testing.T) {
		got := applySpaceOverride(map[string]string{"/repo/a": "old", "/repo/b": "keep"}, "/repo/a", "team")
		if got["/repo/a"] != "team" {
			t.Fatalf("写入失败: %+v", got)
		}
		if got["/repo/b"] != "keep" {
			t.Fatalf("不应改动其他项目: %+v", got)
		}
	})
	t.Run("选自动匹配删除该项", func(t *testing.T) {
		got := applySpaceOverride(map[string]string{"/repo/a": "team", "/repo/b": "keep"}, "/repo/a", memorySpaceAutoChoice)
		if _, ok := got["/repo/a"]; ok {
			t.Fatalf("应删除 /repo/a: %+v", got)
		}
		if got["/repo/b"] != "keep" {
			t.Fatalf("不应改动其他项目: %+v", got)
		}
	})
	t.Run("nil map 也能写", func(t *testing.T) {
		got := applySpaceOverride(nil, "/repo/a", "team")
		if got == nil || got["/repo/a"] != "team" {
			t.Fatalf("nil map 写入失败: %+v", got)
		}
	})
	t.Run("nil map 上选自动匹配不 panic", func(t *testing.T) {
		if got := applySpaceOverride(nil, "/repo/a", memorySpaceAutoChoice); len(got) != 0 {
			t.Fatalf("nil map 选自动匹配应保持空: %+v", got)
		}
	})
}

// TestMemorySpaceSubtitle 验证手动指定、自动匹配命中、未匹配三种副标题文案。
func TestMemorySpaceSubtitle(t *testing.T) {
	cases := []struct {
		name string
		res  MemorySpaceResolution
		want string
	}{
		{"手动指定", MemorySpaceResolution{Space: "team", FromOverride: true}, "手动指定：team"},
		{"自动匹配", MemorySpaceResolution{Space: "mcode", Hit: true}, "自动匹配：mcode"},
		{"未匹配", MemorySpaceResolution{Space: "other"}, "未匹配，使用默认空间"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := memorySpaceSubtitle(tc.res); got != tc.want {
				t.Fatalf("副标题 = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestNowledgeSpaceSettings_PersistOverride 验证选中项写回配置并落盘，选回「自动匹配」删除该项。
func TestNowledgeSpaceSettings_PersistOverride(t *testing.T) {
	s := newChatState(NewFakeClient())
	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, s)

	st.setProjectSpaceOverride("/repo/a", "team")
	cfg, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("load saved config failed: %v", err)
	}
	if got := cfg.Memory.SpaceFor("/repo/a"); got != "team" {
		t.Fatalf("space_overrides 未落盘: %+v", cfg.Memory.SpaceOverrides)
	}

	st.setProjectSpaceOverride("/repo/a", memorySpaceAutoChoice)
	cfg, err = config.Load(dataDir)
	if err != nil {
		t.Fatalf("load saved config failed: %v", err)
	}
	if got := cfg.Memory.SpaceFor("/repo/a"); got != "" {
		t.Fatalf("选自动匹配应删除该项: %+v", cfg.Memory.SpaceOverrides)
	}
}

// TestNowledgeSpaceSettings_Rows 验证每个项目一行：项目名、解析结果副标题与下拉框都在。
func TestNowledgeSpaceSettings_Rows(t *testing.T) {
	fc := NewFakeClient()
	fc.SetMemorySpaces([]MemorySpace{{ID: "default", Name: "Default"}, {ID: "mcode", Name: "mcode"}})
	s := newChatState(fc)
	s.projects = []Project{
		{ID: "p1", Name: "MCode", Path: "/repo/mcode"},
		{ID: "p2", Name: "Other", Path: "/repo/other"},
	}
	st := NewSettingsState(settingsTestDir(t), nil, s)
	tt := ui.NewTester(func(c *ui.Context) {
		renderTestNowledgeSections(c, st)
	}, 1000, 1600)

	// 等加载完成：项目行解析出结果后才会渲染「未匹配，使用默认空间」。
	waitFor(t, tt, func() bool {
		return tt.HasText("未匹配，使用默认空间")
	})
	if !tt.HasText("自动匹配：mcode") {
		t.Fatalf("缺少自动匹配文案: %v", tt.Texts())
	}
	if !tt.HasText("未匹配，使用默认空间") {
		t.Fatalf("缺少未匹配文案: %v", tt.Texts())
	}
	// 每个项目名都要出现（项目名由行内 Text 与下拉框 Label 渲染）。
	for _, name := range []string{"MCode", "Other"} {
		if !tt.HasText(name) {
			t.Fatalf("缺少项目名 %q: %v", name, tt.Texts())
		}
	}
	// 项目名文本先于同名 Select 的 Label，点击当前值才能命中下拉框。
	if err := tt.Click(memorySpaceAutoChoice); err != nil {
		t.Fatalf("打开项目空间选择失败: %v", err)
	}
	tt.Frame()
	if err := tt.Click("mcode"); err != nil {
		t.Fatalf("选择记忆空间失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.cfg.Memory.SpaceFor("/repo/mcode") == "mcode"
	})
	// 覆盖写回后重新解析：副标题从自动匹配变成手动指定。
	waitFor(t, tt, func() bool {
		return tt.HasText("手动指定：mcode")
	})
}

// TestNowledgeSpaceSettings_LoadFailureShowsOneGrayLine 覆盖：空间加载失败时只留一行灰字和重试按钮，
// 不弹窗也不列项目行；故障排除后点重试能把项目行拉回来。
func TestNowledgeSpaceSettings_LoadFailureShowsOneGrayLine(t *testing.T) {
	fc := NewFakeClient()
	fc.SetMemorySpaces([]MemorySpace{{ID: "mcode", Name: "mcode"}})
	fc.mu.Lock()
	fc.memoryWorkingErr = errors.New("nmem 不可用")
	fc.mu.Unlock()
	s := newChatState(fc)
	s.projects = []Project{{ID: "p1", Name: "MCode", Path: "/repo/mcode"}}
	st := NewSettingsState(settingsTestDir(t), nil, s)
	tt := ui.NewTester(func(c *ui.Context) { renderTestNowledgeSections(c, st) }, 1000, 4000)

	waitFor(t, tt, func() bool { return tt.HasText("读取记忆空间失败：nmem 不可用") })
	if tt.HasText("MCode") {
		t.Fatalf("失败时不应列出项目行: %v", tt.Texts())
	}

	fc.mu.Lock()
	fc.memoryWorkingErr = nil
	fc.mu.Unlock()
	if err := tt.Click("重试"); err != nil {
		t.Fatalf("点击重试失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("自动匹配：mcode") })
	if !tt.HasText("MCode") {
		t.Fatalf("重试成功后应列出项目行: %v", tt.Texts())
	}
}

// TestLocalMemorySettings 验证本地文档后端：nmem 专属行仍在但禁用，项目记忆空间整块不出现。
func TestLocalMemorySettings(t *testing.T) {
	fake := NewFakeClient()
	fake.memoryBackend = "local"
	s := newChatState(fake)
	t.Cleanup(s.close)
	st := NewSettingsState(settingsTestDir(t), nil, s)
	tt := ui.NewTester(func(c *ui.Context) { renderPageNowledge(c, st) }, 1000, 5000)

	for _, text := range []string{
		"记忆后端", "当前生效：本地文档",
		"派发子代理时附带相关记忆",
	} {
		if !tt.HasText(text) {
			t.Fatalf("缺少 %q: %v", text, tt.Texts())
		}
	}
	if tt.HasText("项目记忆空间") {
		t.Fatalf("本地后端不应显示项目记忆空间: %v", tt.Texts())
	}

	// nmem 专属行照常显示但不可写：禁用态的开关命中也翻不了值。
	// config.Defaults 已经把 SyncThreads 置成指向 true 的指针，先清回「未设置」，
	// 这样断言 nil 才能看出这次点击有没有写配置。
	st.mu.Lock()
	st.cfg.Memory.SyncThreads = nil
	st.mu.Unlock()
	if _, ok := tt.Find("切换同步会话到 Nowledge"); !ok {
		t.Fatalf("本地后端下同步会话行应仍渲染但禁用")
	}
	_ = tt.Click("切换同步会话到 Nowledge")
	tt.Frame()
	st.mu.Lock()
	syncThreads := st.cfg.Memory.SyncThreads
	st.mu.Unlock()
	if syncThreads != nil {
		t.Fatalf("本地后端下开关不应写配置: %+v", *syncThreads)
	}
}

// 逐项搜索按标题与别名匹配：后端无关的行两种后端都能搜到；nmem 专属行在本地后端下
// 仍渲染但禁用，所以照样能命中，不会点进去落到一个不存在的行上。
func TestNowledgeSettings_SearchFindsRows(t *testing.T) {
	for _, backend := range []string{"local", "nmem"} {
		t.Run(backend, func(t *testing.T) {
			fake := NewFakeClient()
			fake.memoryBackend = backend
			s := newChatState(fake)
			t.Cleanup(s.close)
			st := NewSettingsState(settingsTestDir(t), nil, s)
			st.open = true
			st.currentPage = "nowledge"

			// 旧说明文字里的检索词搬到了别名：搜「空闲」仍能命中会话同步行。
			st.navSearch = "空闲"
			tt := ui.NewTester(func(c *ui.Context) { renderSettingsNav(c, st) }, 900, 1400)
			tt.Frame()
			if !tt.HasText("settings-nav-hit-row-nowledge-sync-threads") {
				t.Fatalf("%s 后端搜索空闲少了会话同步行：%v", backend, tt.Texts())
			}
		})
	}
}

// TestSettingsPageNowledge_Tree 验证扩展页保留记忆主区和项目空间，且高级项已移出。
func TestSettingsPageNowledge_Tree(t *testing.T) {
	fc := NewFakeClient()
	fc.SetMemorySpaces([]MemorySpace{{ID: "default", Name: "Default"}, {ID: "mcode", Name: "mcode"}})
	s := newChatState(fc)
	s.projects = []Project{{ID: "p1", Name: "MCode", Path: "/repo/mcode"}}
	st := NewSettingsState(settingsTestDir(t), nil, s)
	tt := ui.NewTester(func(c *ui.Context) { renderTestNowledgeSections(c, st) }, 1000, 1600)

	if !tt.HasText("记忆") {
		t.Fatalf("缺少节标题「记忆」: %v", tt.Texts())
	}
	// 进阶项交由高级页组装。
	if tt.HasText("工作记忆预算") {
		t.Fatal("扩展页不应显示高级项")
	}
	if !tt.HasText("记忆后端") {
		t.Fatalf("缺少主区行: %v", tt.Texts())
	}
	// 项目记忆空间仍是一块列表，加载完成后出现项目行。
	if !tt.HasText("项目记忆空间") {
		t.Fatalf("缺少项目记忆空间: %v", tt.Texts())
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("MCode")
	})

	initial := st.cfg.Memory.BeeRecallEnabled()
	if err := tt.Click("切换派发子代理时附带相关记忆"); err != nil {
		t.Fatalf("click bee recall switch failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return st.cfg.Memory.BeeRecall != nil && *st.cfg.Memory.BeeRecall != initial
	})
}
