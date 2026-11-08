package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// setLastModelSelection 在测试里直接写入“上次模型选择”的进程内缓存，sel 为 nil 表示清空。
func setLastModelSelection(sel *ModelSelection) {
	lastModelSelectionMu.Lock()
	path := uiStatePath()
	lastModelSelectionPath = path
	lastModelSelectionLoaded = true
	lastModelSelectionCache = sel
	lastSwarmSelectionCache = nil
	// 把记录的修改时间对齐磁盘现状（文件不存在则清零），
	// 避免 ui_state.go 的“按修改时间发现外部改动”逻辑把注入的缓存当成过期而重读盘覆盖。
	lastModelSelectionModTime = time.Time{}
	if info, err := os.Stat(path); err == nil {
		lastModelSelectionModTime = info.ModTime()
	}
	lastModelSelectionMu.Unlock()
}

// setLastSwarmSelection 在测试里直接写入“上次 Swarm 选择”的进程内缓存，h 为 nil 表示清空。
func setLastSwarmSelection(h *LastSwarmSelection) {
	lastModelSelectionMu.Lock()
	lastSwarmSelectionCache = h
	lastModelSelectionMu.Unlock()
}

func TestNewThreadOptionsForProject_Global(t *testing.T) {
	trueVal, falseVal := true, false
	providers := []ProviderInfo{
		{ID: "claude", Name: "Claude", Models: []ModelInfo{{ID: "", IsDefault: true}, {ID: "sonnet"}}},
		{ID: "codex", Name: "Codex", Models: []ModelInfo{{ID: "gpt-5.5", IsDefault: true}}},
		{ID: "pi", Name: "Pi", Models: []ModelInfo{{ID: "default", IsDefault: true}}},
	}
	tests := []struct {
		name      string
		provider  string
		last      *ModelSelection
		swarm     *LastSwarmSelection
		emptyProv bool
		want      NewThreadOptions
	}{
		{
			name:     "无记录时只用默认 provider",
			provider: "claude",
			want: NewThreadOptions{
				Provider: "claude",
			},
		},
		{
			name:     "上次模型选择优先于默认 provider",
			provider: "claude",
			last:     &ModelSelection{Provider: "codex", Model: "gpt-5.5", Effort: "high", Fast: true},
			want: NewThreadOptions{
				Provider: "codex",
				Model:    "gpt-5.5",
				Effort:   "high",
				Fast:     true,
			},
		},
		{
			name:     "上次选择的 provider 已不在目录时退回默认 provider",
			provider: "claude",
			last:     &ModelSelection{Provider: "gone", Model: "whatever"},
			want: NewThreadOptions{
				Provider: "claude",
			},
		},
		{
			name:     "上次选择的模型已不在目录中时退回默认 provider",
			provider: "claude",
			last:     &ModelSelection{Provider: "codex", Model: "gpt-5.0-removed"},
			want: NewThreadOptions{
				Provider: "claude",
			},
		},
		{
			name:     "上次 Swarm 开关与执行侧优先于默认",
			provider: "claude",
			swarm:    &LastSwarmSelection{Enabled: &trueVal, Bees: &ModelSelection{Provider: "claude", Model: "sonnet", Effort: "high"}},
			want: NewThreadOptions{
				Provider:     "claude",
				BeesProvider: "claude",
				BeesModel:    "sonnet",
				BeesEffort:   "high",
				SwarmEnabled: true,
			},
		},
		{
			name:     "上次执行侧跟随主导时保持为空",
			provider: "claude",
			swarm:    &LastSwarmSelection{Bees: &ModelSelection{Effort: "low"}},
			want: NewThreadOptions{
				Provider:   "claude",
				BeesEffort: "low",
			},
		},
		{
			name:     "上次执行侧模型已不可用时只记过开关不动执行侧",
			provider: "claude",
			swarm:    &LastSwarmSelection{Enabled: &falseVal, Bees: &ModelSelection{Provider: "gone", Model: "x"}},
			want: NewThreadOptions{
				Provider: "claude",
			},
		},
		{
			name:     "无记录时取 provider 的首个模型",
			provider: "codex",
			want: NewThreadOptions{
				Provider: "codex",
				Model:    "gpt-5.5",
			},
		},
		{
			name:     "CLI 动态供应商（pi）上次模型与子线程选项成功恢复",
			provider: "claude",
			last:     &ModelSelection{Provider: "pi", Model: "magpie/jp/auto/deepseek-v4.1-flash", Effort: "max"},
			swarm:    &LastSwarmSelection{Enabled: &trueVal, Bees: &ModelSelection{Provider: "pi", Model: "magpie/jp/auto/deepseek-v4.1-flash", Effort: "max"}},
			want: NewThreadOptions{
				Provider:     "pi",
				Model:        "magpie/jp/auto/deepseek-v4.1-flash",
				Effort:       "max",
				BeesProvider: "pi",
				BeesModel:    "magpie/jp/auto/deepseek-v4.1-flash",
				BeesEffort:   "max",
				SwarmEnabled: true,
			},
		},
		{
			name:      "供应商列表为空时保留上次模型与子线程记录",
			provider:  "claude",
			emptyProv: true,
			last:      &ModelSelection{Provider: "pi", Model: "magpie/jp/auto/deepseek-v4.1-flash", Effort: "max"},
			swarm:     &LastSwarmSelection{Enabled: &trueVal, Bees: &ModelSelection{Provider: "pi", Model: "magpie/jp/auto/deepseek-v4.1-flash", Effort: "max"}},
			want: NewThreadOptions{
				Provider:     "pi",
				Model:        "magpie/jp/auto/deepseek-v4.1-flash",
				Effort:       "max",
				BeesProvider: "pi",
				BeesModel:    "magpie/jp/auto/deepseek-v4.1-flash",
				BeesEffort:   "max",
				SwarmEnabled: true,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setLastModelSelection(tc.last)
			setLastSwarmSelection(tc.swarm)
			defer setLastModelSelection(nil)
			curProviders := providers
			if tc.emptyProv {
				curProviders = nil
			}
			got := newThreadOptionsForProject("", tc.provider, curProviders, nil)
			if got != tc.want {
				t.Fatalf("newThreadOptionsForProject(\"\", %q) = %+v, want %+v", tc.provider, got, tc.want)
			}
		})
	}
}

// TestSettings_CreateThreadIgnoresRoles 覆盖 Roles 下线后的行为：即使配置里写了
// Roles.Lead（codex/gpt-5.5）与 Roles.Coder，新建会话也不采用，而是沿用默认 provider。
func TestSettings_CreateThreadIgnoresRoles(t *testing.T) {
	// TestMain 的 MCODE_DATA 由全包共享，异步写盘可能串到其他用例。
	lastSelectionWriteWG.Wait()
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(lastSelectionWriteWG.Wait)
	// 新建会话会沿用全局和项目的模型选择，清空两种缓存以避免其它用例影响断言。
	setLastModelSelection(nil)
	clearProjectModelBuckets()
	t.Cleanup(clearProjectModelBuckets)

	fake := NewFakeClient()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "Proj1"}},
		map[string][]Thread{"p1": {}},
	)
	fake.SetProviders([]ProviderInfo{
		{ID: "claude", Name: "Claude", Available: true},
		{ID: "codex", Name: "Codex", Available: true},
	})

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Models.Roles.Lead = config.ModelRoleConfig{Provider: "codex", Model: "gpt-5.5"}
	cfg.Models.Roles.Coder = config.ModelRoleConfig{Provider: "claude", Model: "sonnet"}
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatalf("config.Save failed: %v", err)
	}

	var chat *chatState
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil), func(s *chatState) {
		chat = s
	}), 1000, 700)

	waitFor(t, tt, func() bool {
		chat.mu.Lock()
		defer chat.mu.Unlock()
		return chat.initialized
	})

	chat.createThread("p1")
	// 待创建会话在首个非空输入后才真正创建，这里模拟输入触发。
	chat.noteComposerDraft("", "你好")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		opts := fake.LastNewThreadOptions()
		if opts.Provider == "claude" {
			if opts.Model == "gpt-5.5" || opts.BeesProvider == "claude" || opts.BeesModel == "sonnet" {
				t.Fatalf("新建会话不应采用 Roles 配置，got %+v", opts)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("expected lastNewThreadOpts to follow default provider claude, got %+v", fake.LastNewThreadOptions())
}

func TestModelPureFunctions_ReorderPins(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	pins := []ModelPin{
		{Provider: "p", ModelID: "m0"},
		{Provider: "p", ModelID: "m1"},
		{Provider: "p", ModelID: "m2"},
		{Provider: "p", ModelID: "m3"},
	}

	// 前移：将 m2 移到 0
	resForward := reorderPins(pins, 2, 0)
	if len(resForward) != 4 || resForward[0].ModelID != "m2" || resForward[1].ModelID != "m0" || resForward[2].ModelID != "m1" || resForward[3].ModelID != "m3" {
		t.Fatalf("前移结果错误: %+v", resForward)
	}

	// 后移：将 m0 移到 2
	resBackward := reorderPins(pins, 0, 2)
	if len(resBackward) != 4 || resBackward[0].ModelID != "m1" || resBackward[1].ModelID != "m2" || resBackward[2].ModelID != "m0" || resBackward[3].ModelID != "m3" {
		t.Fatalf("后移结果错误: %+v", resBackward)
	}

	// 越界与同位处理
	resOOB1 := reorderPins(pins, -1, 2)
	if len(resOOB1) != 4 || resOOB1[0].ModelID != "m0" {
		t.Fatalf("越界处理错误: %+v", resOOB1)
	}
	resOOB2 := reorderPins(pins, 1, 10)
	if len(resOOB2) != 4 || resOOB2[0].ModelID != "m0" {
		t.Fatalf("越界处理错误: %+v", resOOB2)
	}
	resSame := reorderPins(pins, 1, 1)
	if len(resSame) != 4 || resSame[1].ModelID != "m1" {
		t.Fatalf("同位处理错误: %+v", resSame)
	}
}

func TestModelPureFunctions_DragTargetIndex(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// 向下移动
	if target := modelDragTargetIndex(1, 50, 50, 4); target != 2 {
		t.Fatalf("向下移动一行目标错误: %d", target)
	}
	// 向上移动
	if target := modelDragTargetIndex(2, -50, 50, 4); target != 1 {
		t.Fatalf("向上移动一行目标错误: %d", target)
	}
	// 越界截断
	if target := modelDragTargetIndex(1, -200, 50, 4); target != 0 {
		t.Fatalf("向上越界截断错误: %d", target)
	}
	if target := modelDragTargetIndex(2, 200, 50, 4); target != 3 {
		t.Fatalf("向下越界截断错误: %d", target)
	}
	// n <= 0
	if target := modelDragTargetIndex(0, 50, 50, 0); target != 0 {
		t.Fatalf("n<=0 处理错误: %d", target)
	}
}

func TestModelPureFunctions_FilterProviderModels(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	providers := []ProviderInfo{
		{
			ID:   "claude",
			Name: "Claude",
			Models: []ModelInfo{
				{ID: "sonnet", Name: "Sonnet"},
				{ID: "haiku", Name: "Haiku"},
			},
		},
		{
			ID:   "codex",
			Name: "Codex",
			Models: []ModelInfo{
				{ID: "gpt-5.5", Name: "GPT-5.5"},
			},
		},
	}

	// 空 query 原样返回
	emptyFilter := filterProviderModels(providers, "")
	if len(emptyFilter) != 2 {
		t.Fatalf("空 query 应返回全部供应商")
	}

	// 按名称匹配与大小写无关
	matchName := filterProviderModels(providers, "SONNET")
	if len(matchName) != 1 || matchName[0].ID != "claude" || len(matchName[0].Models) != 1 {
		t.Fatalf("大小写无关匹配失败: %+v", matchName)
	}

	// 去掉空供应商
	noMatch := filterProviderModels(providers, "nonexistent-model")
	if len(noMatch) != 0 {
		t.Fatalf("未匹配项应为空切片: %+v", noMatch)
	}
}

func TestModelPureFunctions_CanAddModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	pins := []ModelPin{
		{Provider: "claude", ModelID: "sonnet"},
	}

	// 正常未满未重复
	if !canAddModel(pins, 30, ModelPin{Provider: "codex", ModelID: "gpt-5.5"}) {
		t.Fatalf("正常模型应可添加")
	}

	// 重复返回 false
	if canAddModel(pins, 30, ModelPin{Provider: "claude", ModelID: "sonnet"}) {
		t.Fatalf("重复模型不可添加")
	}

	// 满 30 返回 false
	var fullPins []ModelPin
	for i := 0; i < 30; i++ {
		fullPins = append(fullPins, ModelPin{Provider: "p", ModelID: fmt.Sprintf("m%d", i)})
	}
	if canAddModel(fullPins, 30, ModelPin{Provider: "new", ModelID: "model"}) {
		t.Fatalf("达到上限不可添加")
	}
	for _, limit := range []int{0, -1, 100} {
		if canAddModel(fullPins, limit, ModelPin{Provider: "new", ModelID: "model"}) {
			t.Fatalf("limit=%d 绕过了上限 30", limit)
		}
	}
	if canAddModel(nil, 30, ModelPin{}) {
		t.Fatal("不能添加空模型")
	}
}

func TestSettings_ModelsPage_CommitModelReorder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	pins := []ModelPin{
		{Provider: "claude", ModelID: "sonnet"},
		{Provider: "codex", ModelID: "gpt-5.5"},
		{Provider: "agy", ModelID: "gemini"},
	}
	for _, p := range pins {
		_, _ = fake.AddModelToList(context.Background(), p)
	}

	newOrder, err := commitModelReorder(fake, pins, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(newOrder) != 3 || newOrder[0].Key() != "codex/gpt-5.5" || newOrder[2].Key() != "claude/sonnet" {
		t.Fatalf("commitModelReorder 返回顺序错误: %+v", newOrder)
	}

	persisted, _, err := fake.ModelList(context.Background())
	if err != nil || len(persisted) != 3 || persisted[2].Key() != "claude/sonnet" {
		t.Fatalf("持久化顺序错误: %+v", persisted)
	}
}

func TestSettings_ModelsPage_AddAndRemoveModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	fake.SetProviders([]ProviderInfo{
		{ID: "claude", Name: "Claude", Available: true, Models: []ModelInfo{{ID: "sonnet", Name: "Sonnet"}}},
	})

	dataDir := settingsTestDir(t)
	var chat *chatState
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil), func(s *chatState) {
		chat = s
	}), 1000, 2400)

	waitFor(t, tt, func() bool {
		chat.mu.Lock()
		defer chat.mu.Unlock()
		return chat.initialized
	})

	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click '设置' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("模型")
	})
	if err := tt.Click("模型与供应商"); err != nil {
		t.Fatalf("click '模型与供应商' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("收藏模型")
	})

	// 从收藏区打开模型选择器。
	if err := settingsClick(t, tt, "添加收藏"); err != nil {
		t.Fatal(err)
	}
	settingsClickModelPickerID(t, tt, "sonnet")

	// fake.ModelList 里出现该模型
	waitFor(t, tt, func() bool {
		list, _, err := fake.ModelList(context.Background())
		return err == nil && len(list) == 1 && list[0].Key() == "claude/sonnet"
	})

	// 2. 先关掉浮层，再从「收藏模型」里移除该模型
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	waitFor(t, tt, func() bool {
		return tt.HasText("移除收藏 Claude Sonnet")
	})
	if err := settingsClick(t, tt, "移除收藏 Claude Sonnet"); err != nil {
		t.Fatalf("click '移除收藏 Claude Sonnet' failed: %v", err)
	}

	// 该模型从 fake.ModelList 中被移除
	waitFor(t, tt, func() bool {
		list, _, err := fake.ModelList(context.Background())
		return err == nil && len(list) == 0
	})
}

// 添加收藏浮层跟随「在模型选择器中显示」开关：关闭时整组不列出 Magpie 模型。
func TestSettings_ModelsPage_AddPinHidesMagpieModels(t *testing.T) {
	for _, hide := range []bool{true, false} {
		name := "开关打开时仍列出 Magpie 模型"
		if hide {
			name = "开关关闭时隐藏 Magpie 模型"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			gateway := config.CustomEndpointConfig{ID: "gw", Name: "测试网关", BaseURL: "https://example.com/v1", Enabled: true, Magpie: true}
			providers := []ProviderInfo{
				{ID: "claude", Name: "Claude", Available: true, Models: []ModelInfo{{ID: "sonnet", Name: "Sonnet"}}},
				{ID: config.CustomEndpointProviderKind(gateway), Name: gateway.Name, Available: true, Models: []ModelInfo{
					{ID: "magpie-a", Name: "MagpieA"},
					{ID: "magpie-b", Name: "MagpieB"},
				}},
			}
			fake := NewFakeClient()
			fake.SetProviders(providers)
			tt, st := modelsPageWithProvidersTester(t, fake, providers)
			st.mu.Lock()
			st.cfg.Providers.Custom = []config.CustomEndpointConfig{gateway}
			st.cfg.Providers.HideMagpieModelsInPicker = hide
			st.mu.Unlock()
			st.invalidate()

			if err := settingsClick(t, tt, "添加收藏"); err != nil {
				t.Fatal(err)
			}
			waitFor(t, tt, func() bool { return tt.HasText("Sonnet") })
			if hide {
				if tt.HasText("MagpieA") || tt.HasText("MagpieB") {
					t.Fatalf("隐藏开关打开时浮层仍列出 Magpie 模型: %q", tt.Texts())
				}
				return
			}
			waitFor(t, tt, func() bool { return tt.HasText("MagpieA") && tt.HasText("MagpieB") })
		})
	}
}

// 从收藏模型区移除模型后，页面配置与持久化列表同步更新。
func TestSettings_ModelsPage_RemoveFromProviderRow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	pin := ModelPin{Provider: "claude", ModelID: "sonnet"}
	fake := NewFakeClient()
	if _, err := fake.AddModelToList(context.Background(), pin); err != nil {
		t.Fatal(err)
	}
	providers := []ProviderInfo{
		{ID: "claude", Name: "Claude", Available: true, Models: []ModelInfo{{ID: "sonnet", Name: "Sonnet"}}},
	}
	// 页面加载优先用 client.Providers() 的结果，同一份目录也要设进 fake。
	fake.SetProviders(providers)
	tt, st := modelsPageWithProvidersTester(t, fake, providers)

	waitFor(t, tt, func() bool { return tt.HasText("移除收藏 Claude Sonnet") })
	st.mu.Lock()
	pinsBefore := append([]ModelPin(nil), st.cfg.Models.Pins...)
	st.mu.Unlock()
	if len(pinsBefore) != 1 || pinsBefore[0] != pin {
		t.Fatalf("「收藏模型」初始状态不对: %+v", pinsBefore)
	}

	if err := tt.Click("移除收藏 Claude Sonnet"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return !tt.HasText("移除收藏 Claude Sonnet") })
	st.mu.Lock()
	pinsAfter := append([]ModelPin(nil), st.cfg.Models.Pins...)
	st.mu.Unlock()
	if len(pinsAfter) != 0 {
		t.Fatalf("「收藏模型」未同步移除: %+v", pinsAfter)
	}

	// fake 的持久化列表里也已经没有它。
	waitFor(t, tt, func() bool {
		list, _, err := fake.ModelList(context.Background())
		return err == nil && len(list) == 0
	})
}

func TestSettings_ModelsPage_LimitAndCollapse(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	for i := 0; i < 30; i++ {
		_, _ = fake.AddModelToList(context.Background(), ModelPin{Provider: "test", ModelID: fmt.Sprintf("model-%d", i)})
	}
	fake.SetProviders([]ProviderInfo{
		{ID: "claude", Name: "Claude", Available: true, Models: []ModelInfo{{ID: "sonnet", Name: "Sonnet"}}},
	})

	dataDir := settingsTestDir(t)
	var chat *chatState
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil), func(s *chatState) {
		chat = s
	}), 1000, 3000)

	waitFor(t, tt, func() bool {
		chat.mu.Lock()
		defer chat.mu.Unlock()
		return chat.initialized
	})

	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click '设置' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("模型")
	})
	if err := tt.Click("模型与供应商"); err != nil {
		t.Fatalf("click '模型与供应商' failed: %v", err)
	}

	// 预置 30 个 pin 后出现上限提示文字
	waitFor(t, tt, func() bool {
		return tt.HasText("收藏模型已达上限（30 个），先移除一些再添加")
	})

	if err := settingsClick(t, tt, "添加收藏"); err != nil {
		t.Fatal(err)
	}
	// 达到上限后浮层里仍有候选模型，但勾选框禁用，点击不会加入。
	waitFor(t, tt, func() bool {
		return tt.HasText("收藏模型已达上限（30 个），先移除一些再添加") && tt.HasText("Sonnet")
	})
	// 禁用的勾选框点击后，收藏列表应保持不变。
	settingsClickModelPickerID(t, tt, "sonnet")
	if pins, _, err := fake.ModelList(context.Background()); err != nil || len(pins) != 30 {
		t.Fatalf("达到上限后添加改变了列表: %+v, %v", pins, err)
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()

}

// waitProviderPreferencePersisted 等后台 goroutine 把「上次使用的 Agent」写进 dataDir 的配置。
// 超时不判失败：真要出问题，t.TempDir() 的清理会自己报出 unlinkat，不会静默吞掉。
func waitProviderPreferencePersisted(t *testing.T, dataDir string, provider string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		cfg, err := config.Load(dataDir)
		if err == nil && cfg.Models.ProviderSelected && cfg.Models.Provider == provider {
			return
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type modelsPageTestClient struct {
	Client
	list    func(context.Context) ([]ModelPin, int, error)
	add     func(context.Context, ModelPin) (bool, error)
	remove  func(context.Context, ModelPin) (bool, error)
	reorder func(context.Context, []ModelPin) error
}

// countingModelsClient 统计 ModelList 调用次数，用于断言目录作废只重载目录。
type countingModelsClient struct {
	Client
	mu    sync.Mutex
	calls int
}

func (c *countingModelsClient) ModelList(ctx context.Context) ([]ModelPin, int, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.Client.ModelList(ctx)
}

func (c *countingModelsClient) modelListCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

type modelsRefreshTestClient struct {
	Client
	refresh   func(context.Context) ([]ProviderInfo, error)
	providers func(context.Context) ([]ProviderInfo, error)
}

func (c modelsRefreshTestClient) RefreshProviders(ctx context.Context) ([]ProviderInfo, error) {
	return c.refresh(ctx)
}

func (c modelsRefreshTestClient) Providers(ctx context.Context) ([]ProviderInfo, error) {
	if c.providers != nil {
		return c.providers(ctx)
	}
	return c.Client.Providers(ctx)
}

func TestComposerRefreshProvidersForcesRefreshOnce(t *testing.T) {
	fake := NewFakeClient()
	started, release := make(chan struct{}), make(chan struct{})
	var refreshCalls, providerCalls atomic.Int32
	updated := []ProviderInfo{{ID: "claude", Models: []ModelInfo{{ID: "opus"}}}}
	client := modelsRefreshTestClient{
		Client: fake,
		refresh: func(context.Context) ([]ProviderInfo, error) {
			refreshCalls.Add(1)
			close(started)
			<-release
			return updated, nil
		},
		providers: func(context.Context) ([]ProviderInfo, error) {
			providerCalls.Add(1)
			return nil, nil
		},
	}
	chat := newChatState(client)
	chat.refreshProviders()
	<-started
	chat.refreshProviders()
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		chat.mu.Lock()
		got := append([]ProviderInfo(nil), chat.providers...)
		chat.mu.Unlock()
		if reflect.DeepEqual(got, updated) {
			if refreshCalls.Load() != 1 || providerCalls.Load() != 0 {
				t.Fatalf("强制刷新次数 %d，缓存读取次数 %d", refreshCalls.Load(), providerCalls.Load())
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("强制刷新未更新会话目录")
}

func TestBackgroundCatalogUpdateInvalidatesSettingsSnapshot(t *testing.T) {
	fake := NewFakeClient()
	updated := []ProviderInfo{{ID: "claude", Name: "Claude", Available: true, Models: []ModelInfo{{ID: "opus", Name: "Opus"}}}}
	fake.SetProviders(updated)
	chat := newChatState(fake)
	st := NewSettingsState(settingsTestDir(t), nil, chat)
	setChatSettings(chat, st)
	t.Cleanup(func() { setChatSettings(chat, nil) })
	ps := modelsPageStateOf(st)
	ps.seeded = true
	ps.providers = []ProviderInfo{{ID: "claude", Name: "Claude", Available: true}}
	ps.pins = []ModelPin{{Provider: "claude", ModelID: "saved"}}
	ps.load = make(chan modelsPageLoadResult, 1)
	ps.loadSince = time.Now()
	ps.loadCatalogOnly = true
	ps.load <- modelsPageLoadResult{providers: ps.providers}
	if _, ok := chat.reloadProviderCatalog(); !ok {
		t.Fatal("后台目录读取失败")
	}
	st.mu.Lock()
	version := st.modelCatalogVersion
	st.mu.Unlock()
	if version == 0 {
		t.Fatal("后台目录更新应递增版本")
	}
	tt := ui.NewTester(func(c *ui.Context) { modelsPageLibrary(c, st) }, 1000, 1000)
	tt.Frame()
	if !reflect.DeepEqual(ps.providers, updated) {
		t.Fatalf("设置页应使用新目录且忽略旧异步结果: %+v", ps.providers)
	}
	if len(ps.pins) != 1 || ps.pins[0].ModelID != "saved" {
		t.Fatal("更新目录不应改变收藏")
	}
}

func TestSettingsModelsRefreshUpdatesCatalogAndPreservesPins(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial_failure_%t", fail), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			fake := NewFakeClient()
			pin := ModelPin{Provider: "claude", ModelID: "old"}
			_, _ = fake.AddModelToList(context.Background(), pin)
			old := []ProviderInfo{{ID: "claude", Name: "Claude", Available: true, Models: []ModelInfo{{ID: "old", Name: "Old Model"}}}}
			updated := []ProviderInfo{{ID: "claude", Name: "Claude", Available: true, Models: []ModelInfo{{ID: "new", Name: "New Model"}}}}
			fake.SetProviders(old)
			started, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			// 合并后的 models 页刷新路径（refreshModels + loadProviderRows）可能重复进入这里，
			// started 只能关一次，用 Once 兜住。
			var startOnce sync.Once
			client := modelsRefreshTestClient{Client: fake, refresh: func(context.Context) ([]ProviderInfo, error) {
				calls.Add(1)
				startOnce.Do(func() { close(started) })
				<-release
				if fail {
					return updated, errors.New("以下供应商刷新失败：Codex")
				}
				return updated, nil
			}}
			chat := newChatState(client)
			chat.providers = old
			st := NewSettingsState(settingsTestDir(t), nil, chat)
			var releaseOnce sync.Once
			releaseLoad := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(releaseLoad)
			st.currentPage = "models"
			tt := ui.NewTester(func(c *ui.Context) { renderSettingsDetail(c, st) }, 1000, 3000)
			waitFor(t, tt, func() bool { return tt.HasText("Old Model") })
			if err := tt.Click("刷新"); err != nil {
				t.Fatal(err)
			}
			<-started
			waitFor(t, tt, func() bool { return tt.HasText("刷新中…") })
			_ = tt.Click("刷新中…")
			if calls.Load() != 1 {
				t.Fatal("刷新期间不应重复启动")
			}
			releaseLoad()
			// 已添加计数先随刷新前的结果渲染，结果提示出现时刷新才真正结束、布局才稳定。
			waitFor(t, tt, func() bool {
				return (tt.HasText("移除收藏 Claude Old") || tt.HasText("移除收藏 Claude Old Model")) && (tt.HasText("模型列表已刷新") || tt.HasText("以下供应商刷新失败：Codex"))
			})
			// 旧名称已随目录刷新消失，新目录里的模型要到添加模型浮层里看。
			clickWhenSettled(t, tt, "添加收藏")
			waitFor(t, tt, func() bool { return tt.HasText("New Model") })
			if fail && !tt.HasText("以下供应商刷新失败：Codex") {
				t.Fatal("部分失败时应提示失败的供应商")
			}
			if !fail && !tt.HasText("模型列表已刷新") {
				t.Fatal("成功时应显示刷新完成")
			}
			pins, _, err := fake.ModelList(context.Background())
			if err != nil || !reflect.DeepEqual(pins, []ModelPin{pin}) {
				t.Fatalf("刷新不应修改固定模型: %v, %v", pins, err)
			}
			chat.mu.Lock()
			defer chat.mu.Unlock()
			if len(chat.providers) != 1 || chat.providers[0].Models[0].ID != "new" {
				t.Fatalf("会话模型选择器未更新: %+v", chat.providers)
			}
		})
	}
}

func TestSettingsModelsRefreshIgnoresOlderPageLoad(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	old := []ProviderInfo{{ID: "claude", Name: "Claude", Available: true, Models: []ModelInfo{{ID: "old", Name: "Old Model"}}}}
	updated := []ProviderInfo{{ID: "claude", Name: "Claude", Available: true, Models: []ModelInfo{{ID: "new", Name: "New Model"}}}}
	started, release := make(chan struct{}), make(chan struct{})
	// Providers 会被渲染（页面加载）与刷新（loadProviderRows）各调一次，started 只关一次。
	var startOnce sync.Once
	client := modelsRefreshTestClient{
		Client: fake,
		providers: func(context.Context) ([]ProviderInfo, error) {
			startOnce.Do(func() { close(started) })
			<-release
			return old, nil
		},
		refresh: func(context.Context) ([]ProviderInfo, error) { return updated, nil },
	}
	chat := newChatState(client)
	chat.providers = old
	st := NewSettingsState(settingsTestDir(t), nil, chat)
	var releaseOnce sync.Once
	releaseLoad := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseLoad)
	st.currentPage = "models"
	state := observeModelsPageState(t)
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsDetail(c, st) }, 1000, 1600)
	<-started
	if err := tt.Click("刷新"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("模型列表已刷新") })
	clickWhenSettled(t, tt, "添加收藏")
	waitFor(t, tt, func() bool { return tt.HasText("New Model") })
	releaseLoad()
	waitModelsPageLoaded(t, tt, state)
	tt.Frame()
	if !tt.HasText("New Model") || tt.HasText("Old Model") {
		t.Fatal("较早的页面加载不应覆盖刷新后的模型目录")
	}
}

func (c modelsPageTestClient) ModelList(ctx context.Context) ([]ModelPin, int, error) {
	if c.list != nil {
		return c.list(ctx)
	}
	return c.Client.ModelList(ctx)
}

func (c modelsPageTestClient) AddModelToList(ctx context.Context, pin ModelPin) (bool, error) {
	if c.add != nil {
		return c.add(ctx, pin)
	}
	return c.Client.AddModelToList(ctx, pin)
}

func (c modelsPageTestClient) RemoveModelFromList(ctx context.Context, pin ModelPin) (bool, error) {
	if c.remove != nil {
		return c.remove(ctx, pin)
	}
	return c.Client.RemoveModelFromList(ctx, pin)
}

func (c modelsPageTestClient) ReorderModelList(ctx context.Context, pins []ModelPin) error {
	if c.reorder != nil {
		return c.reorder(ctx, pins)
	}
	return c.Client.ReorderModelList(ctx, pins)
}

// observeModelsPageState 让测试在渲染时拿到模型页状态，并在用例结束时撤销钩子。
func observeModelsPageState(t *testing.T) func() *modelsPageState {
	t.Helper()
	var ps *modelsPageState
	prev := modelsPageStateHook
	modelsPageStateHook = func(s *modelsPageState) { ps = s }
	t.Cleanup(func() { modelsPageStateHook = prev })
	return func() *modelsPageState { return ps }
}

// waitModelsPageLoaded 等待模型页当前这轮加载结束（成功或失败都算）。
func waitModelsPageLoaded(t *testing.T, tt *ui.Tester, state func() *modelsPageState) {
	t.Helper()
	waitFor(t, tt, func() bool {
		ps := state()
		return ps != nil && ps.load == nil && ps.loaded
	})
}

func TestSettingsModels_TwoSections(t *testing.T) {
	page, ok := settingsTreePageOf("models")
	if !ok || len(page.Sections) != 2 || page.Sections[0].ID != "sec-models-pinned" || page.Sections[1].ID != "sec-providers" {
		t.Fatal("模型页的两个区块不符合预期")
	}
	found := false
	for _, row := range page.Sections[0].Rows {
		found = found || row.ID == "row-models-add"
	}
	if !found {
		t.Fatal("缺少添加收藏行")
	}
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsTreePage(c, st, pageModels) }, 1000, 4000)
	tt.Frame()
	first := modelsPageStateOf(st)
	tt.Frame()
	if first != modelsPageStateOf(st) {
		t.Fatal("模型页状态未复用")
	}
}

func modelsPageTester(t *testing.T, client Client, providers []ProviderInfo) (*ui.Tester, *SettingsState) {
	t.Helper()
	chat := newChatState(client)
	chat.providers = providers
	st := NewSettingsState(settingsTestDir(t), nil, chat)
	state := observeModelsPageState(t)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("models-test-page")).Children(func() {
			renderSettingsTreePage(c, st, pageModels)
		})
	}, 1000, 4000)
	waitModelsPageLoaded(t, tt, state)
	return tt, st
}

// modelsPageWithProvidersTester 渲染完整模型页及供应商面板。
func modelsPageWithProvidersTester(t *testing.T, client Client, providers []ProviderInfo) (*ui.Tester, *SettingsState) {
	t.Helper()
	chat := newChatState(client)
	chat.providers = providers
	st := NewSettingsState(settingsTestDir(t), nil, chat)
	st.currentPage = "models"
	state := observeModelsPageState(t)
	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsDetail(c, st)
	}, 1000, 4000)
	waitModelsPageLoaded(t, tt, state)
	return tt, st
}

func modelsDialogWithProvidersTester(t *testing.T, client Client, providers []ProviderInfo) (*ui.Tester, *SettingsState) {
	t.Helper()
	chat := newChatState(client)
	chat.providers = providers
	st := NewSettingsState(settingsTestDir(t), nil, chat)
	st.currentPage = "models"
	st.open = true
	state := observeModelsPageState(t)
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsWindow(c, st) }, 1000, 4000)
	waitModelsPageLoaded(t, tt, state)
	return tt, st
}

// TestSettingsModelsFirstFrameShowsConfigPins 首帧就用配置里的固定模型画出「收藏模型」，
// 不等后台加载，也不再显示「正在加载模型…」。
func TestSettingsModelsFirstFrameShowsConfigPins(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	pin := ModelPin{Provider: "claude", ModelID: "configured"}
	started, release := make(chan struct{}), make(chan struct{})
	client := modelsPageTestClient{Client: fake, list: func(context.Context) ([]ModelPin, int, error) {
		close(started)
		<-release
		return nil, 0, nil
	}}
	chat := newChatState(client)
	st := NewSettingsState(settingsTestDir(t), nil, chat)
	var releaseOnce sync.Once
	releaseLoad := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseLoad)
	st.cfg.Models.Pins = []ModelPin{pin}
	st.cfg.Models.SetModelPreference(pin, ModelPreference{FastMode: true})
	state := observeModelsPageState(t)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("models-test-page")).Children(func() {
			renderSettingsTreePage(c, st, pageModels)
		})
	}, 1000, 4000)
	<-started
	tt.Frame()
	if ps := state(); ps == nil || ps.load == nil {
		t.Fatal("首帧应处于加载中")
	}
	if tt.HasText("正在加载模型…") {
		t.Fatal("首帧不应再显示加载提示")
	}
	if !tt.HasText("移除收藏 Claude Configured") {
		t.Fatalf("首帧应直接显示配置里的已选模型: %q", tt.Texts())
	}
	// 偏好也从配置里种子，行摘要直接带上「快速模式」。
	if !tt.HasText("快速模式") {
		t.Fatalf("首帧应显示配置里的模型偏好: %q", tt.Texts())
	}
	releaseLoad()
	waitModelsPageLoaded(t, tt, state)
}

// TestSettingsModelsCatalogStaleKeepsPinsAndCatalog 目录作废只重载目录：
// 「收藏模型」和旧目录在重载期间保持显示，也不会重新拉固定模型列表。
func TestSettingsModelsCatalogStaleKeepsPinsAndCatalog(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	pin := ModelPin{Provider: "claude", ModelID: "sonnet"}
	if _, err := fake.AddModelToList(context.Background(), pin); err != nil {
		t.Fatal(err)
	}
	providers := []ProviderInfo{{ID: "claude", Name: "Claude", Models: []ModelInfo{
		{ID: "sonnet", Name: "Sonnet"},
		{ID: "haiku", Name: "Haiku"},
	}}}
	fake.SetProviders(providers)
	client := &countingModelsClient{Client: fake}
	tt, st := modelsPageWithProvidersTester(t, client, providers)
	clickWhenSettled(t, tt, "添加收藏")
	waitFor(t, tt, func() bool { return tt.HasText("Haiku") })
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	before := client.modelListCalls()

	st.mu.Lock()
	st.catalogStale = true
	st.mu.Unlock()
	st.invalidate()
	tt.Frame()

	if !tt.HasText("移除收藏 Claude Sonnet") {
		t.Fatalf("目录作废时「收藏模型」不应消失: %q", tt.Texts())
	}
	if tt.HasText("正在加载模型…") {
		t.Fatal("目录重载不应显示加载提示")
	}
	if !tt.HasText("Sonnet") || tt.HasText("尚未加载模型") {
		t.Fatalf("目录作废时供应商目录不应清空: %q", tt.Texts())
	}

	// 留出后台目录重载的执行时间：它不应重新读取固定模型，也不应覆盖「收藏模型」。
	time.Sleep(50 * time.Millisecond)
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	if got := client.modelListCalls(); got != before {
		t.Fatalf("目录作废重新读了固定模型列表: %d -> %d", before, got)
	}
	if !tt.HasText("移除收藏 Claude Sonnet") || !tt.HasText("Sonnet") {
		t.Fatalf("目录重载后「收藏模型」或目录丢失: %q", tt.Texts())
	}
	// 目录里的候选模型仍在浮层中。
	clickWhenSettled(t, tt, "添加收藏")
	waitFor(t, tt, func() bool { return tt.HasText("Haiku") })
}

func TestSettings_ModelsPage_PinnedModelAccessibilityNames(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	providers := []ProviderInfo{
		{ID: "claude", Name: "Claude", Models: []ModelInfo{{ID: "shared", Name: "Shared"}}},
		{ID: "codex", Name: "Codex", Models: []ModelInfo{{ID: "shared", Name: "Shared"}}},
	}
	for _, provider := range providers {
		if _, err := fake.AddModelToList(context.Background(), ModelPin{Provider: provider.ID, ModelID: "shared"}); err != nil {
			t.Fatal(err)
		}
	}
	tt, _ := modelsPageTester(t, fake, providers)
	for _, name := range []string{"Claude Shared", "Codex Shared"} {
		for _, label := range []string{name, "移除收藏 " + name, "拖动排序 " + name} {
			if _, ok := tt.Find(label); !ok {
				t.Errorf("缺少无障碍名称 %q", label)
			}
		}
	}
	for _, label := range []string{"body-claude/shared", "body-codex/shared", "models-pin-remove-claude/shared", "models-pin-remove-codex/shared"} {
		if _, ok := tt.Find(label); ok {
			t.Errorf("不应暴露内部键 %q", label)
		}
	}
}

func TestSettings_ModelsPage_DragGesture(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	pins := []ModelPin{
		{Provider: "claude", ModelID: "first"},
		{Provider: "claude", ModelID: "second"},
		{Provider: "claude", ModelID: "third"},
	}
	for _, pin := range pins {
		if _, err := fake.AddModelToList(context.Background(), pin); err != nil {
			t.Fatal(err)
		}
	}
	tt, st := modelsPageTester(t, fake, nil)
	handle, ok := tt.Find("拖动排序 Claude First")
	if !ok {
		t.Fatal("缺少拖拽手柄")
	}
	body, _ := tt.Find("Claude First")
	neighbor, _ := tt.Find("Claude Second")
	x, y := handle.X+handle.W/2, handle.Y+handle.H/2
	tt.Press(x, y)
	tt.Move(x, y+118)
	moved, _ := tt.Find("Claude First")
	shifted, _ := tt.Find("Claude Second")
	if moved.Y <= body.Y+100 || shifted.Y >= neighbor.Y {
		t.Fatalf("拖动时整行应跟随且邻居应让位: body=%+v moved=%+v neighbor=%+v shifted=%+v", body, moved, neighbor, shifted)
	}
	tt.Release(x, y+118)
	want := []ModelPin{pins[1], pins[2], pins[0]}
	persisted, _, err := fake.ModelList(context.Background())
	if err != nil || !reflect.DeepEqual(persisted, want) || !reflect.DeepEqual(st.cfg.Models.Pins, want) {
		t.Fatalf("拖动松手未持久化顺序: %+v, %v", persisted, err)
	}
	handle, _ = tt.Find("拖动排序 Claude First")
	x, y = handle.X+handle.W/2, handle.Y+handle.H/2
	tt.Press(x, y)
	tt.Move(x, y-118)
	tt.Release(x, y-118)
	persisted, _, _ = fake.ModelList(context.Background())
	if !reflect.DeepEqual(persisted, pins) {
		t.Fatalf("第二次反向拖动失败: %+v", persisted)
	}
}

func TestSettings_ModelsPage_PreferencesAndBudgetPersistence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	pin := ModelPin{Provider: "claude", ModelID: "sonnet"}
	providers := []ProviderInfo{{ID: "claude", Name: "Claude", Models: []ModelInfo{{
		ID: "sonnet", Name: "Sonnet", Efforts: []string{"low", "medium", "high"}, DefaultEffort: "medium", SupportsFast: true,
	}}}}
	tt, st := modelsPageWithProvidersTester(t, fake, providers)
	click := func(label string) {
		t.Helper()
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
	}
	click("添加收藏")
	waitFor(t, tt, func() bool { return tt.HasText("Sonnet") })
	click("Sonnet")
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	click("Claude Sonnet")
	clickEffortSlider(t, tt, 1)
	click("快速模式")
	pref, _ := fake.ModelPreferences(context.Background(), pin)
	if pref.Effort == nil || *pref.Effort != "high" || !pref.FastMode {
		t.Fatalf("偏好未保存: %+v", pref)
	}
	// 其他设置保存整份快照时，不能覆盖 Client 刚写入的列表和偏好。
	st.save()
	if st.errorMessage != "" {
		t.Fatal(st.errorMessage)
	}
	cfg, err := config.Load(st.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	savedPref := cfg.Models.ModelPreferenceFor(pin)
	if !reflect.DeepEqual(cfg.Models.Pins, []ModelPin{pin}) || savedPref.Effort == nil || *savedPref.Effort != "high" || !savedPref.FastMode {
		t.Fatalf("预算保存覆盖了模型配置: %+v, %+v", cfg.Models.Pins, savedPref)
	}
	tt.Key(0, ui.KeyEscape)
	click("移除收藏 Claude Sonnet")
	click("添加收藏")
	waitFor(t, tt, func() bool { return tt.HasText("Sonnet") })
	click("Sonnet")
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	click("Claude Sonnet")
	pref, _ = fake.ModelPreferences(context.Background(), pin)
	if pref.Effort == nil || *pref.Effort != "high" || !pref.FastMode || !tt.HasText("推理强度 高 · 快速模式") {
		t.Fatalf("移除后重新添加丢失偏好: %+v", pref)
	}
	click("重置强度与快速模式")
	pref, _ = fake.ModelPreferences(context.Background(), pin)
	if pref.Effort != nil || pref.FastMode {
		t.Fatalf("重置未持久化: %+v", pref)
	}
}

func TestSettings_ModelsPage_WriteFailures(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	failure := errors.New("disk full")
	pin := ModelPin{Provider: "claude", ModelID: "sonnet"}
	providers := []ProviderInfo{{ID: "claude", Models: []ModelInfo{{ID: "sonnet", Name: "Sonnet", Efforts: []string{"low", "high"}}}}}
	for _, refused := range []bool{false, true} {
		t.Run(fmt.Sprintf("add-refused-%t", refused), func(t *testing.T) {
			fake := NewFakeClient()
			client := modelsPageTestClient{Client: fake, add: func(context.Context, ModelPin) (bool, error) {
				if refused {
					return false, nil
				}
				return false, failure
			}}
			tt, st := modelsPageWithProvidersTester(t, client, providers)
			clickWhenSettled(t, tt, "添加收藏")
			waitFor(t, tt, func() bool { return tt.HasText("Sonnet") })
			if err := tt.Click("Sonnet"); err != nil {
				t.Fatal(err)
			}
			if len(st.cfg.Models.Pins) != 0 || tt.HasText("移除收藏 Claude Sonnet") {
				t.Fatal("添加失败仍显示已收藏")
			}
			if !refused && !tt.HasText("添加模型失败：disk full") {
				t.Fatal("缺少添加失败提示")
			}
		})
	}
	fake := NewFakeClient()
	_, _ = fake.AddModelToList(context.Background(), pin)
	client := modelsPageTestClient{Client: fake,
		remove:  func(context.Context, ModelPin) (bool, error) { return false, failure },
		reorder: func(context.Context, []ModelPin) error { return failure },
	}
	got, err := commitModelReorder(client, []ModelPin{pin, {Provider: "claude", ModelID: "other"}}, 0, 1)
	if !errors.Is(err, failure) || got[0] != pin {
		t.Fatalf("排序失败未保留旧顺序: %+v, %v", got, err)
	}
	tt, st := modelsPageTester(t, client, providers)
	if err := tt.Click("移除收藏 Claude Sonnet"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("移除模型失败：disk full") || len(st.cfg.Models.Pins) != 1 {
		t.Fatal("移除失败没有保留配置或显示提示")
	}
	prefClient := preferenceTestClient{Client: fake, write: func(context.Context, ModelPin, ModelPreference) error { return failure }}
	tt, st = modelsPageTester(t, prefClient, providers)
	if err := tt.Click("Claude Sonnet"); err != nil {
		t.Fatal(err)
	}
	clickEffortSlider(t, tt, 1)
	if !tt.HasText("保存模型偏好失败：disk full") || st.cfg.Models.ModelPreferenceFor(pin).Effort != nil {
		t.Fatal("偏好保存失败仍更新了配置")
	}
}

func TestSettings_ModelsPage_LoadFailureAndRetry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	fail := true
	client := modelsPageTestClient{Client: fake, list: func(ctx context.Context) ([]ModelPin, int, error) {
		if fail {
			return nil, 0, errors.New("unavailable")
		}
		return fake.ModelList(ctx)
	}}
	tt, _ := modelsPageTester(t, client, nil)
	if !tt.HasText("加载模型失败：unavailable") {
		t.Fatal("加载失败被显示为空列表")
	}
	// 等首次加载结束后再更改读取行为。
	fail = false
	if err := tt.Click("重新加载"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("尚未选择模型") })
}

func TestSettings_ModelsPage_LoadingDisablesAdd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	pin := ModelPin{Provider: "claude", ModelID: "sonnet"}
	_, _ = fake.AddModelToList(context.Background(), pin)
	release := make(chan struct{})
	client := modelsPageTestClient{Client: fake, list: func(ctx context.Context) ([]ModelPin, int, error) {
		<-release
		return fake.ModelList(ctx)
	}}
	chat := newChatState(client)
	chat.providers = []ProviderInfo{{ID: "claude", Models: []ModelInfo{{ID: "other", Name: "Other"}}}}
	st := NewSettingsState(settingsTestDir(t), nil, chat)
	var releaseOnce sync.Once
	releaseLoad := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseLoad)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("page")).Children(func() {
			renderSettingsTreePage(c, st, pageModels)
		})
	}, 1000, 4000)
	clickWhenSettled(t, tt, "添加收藏")
	waitFor(t, tt, func() bool { return tt.HasText("Other") })
	// 加载完成前浮层里的候选模型是禁用的，点了也不会写入。
	if err := tt.Click("Other"); err != nil {
		t.Fatal(err)
	}
	got, _, _ := fake.ModelList(context.Background())
	releaseLoad()
	if len(got) != 1 {
		t.Fatal("加载完成前允许添加，可能被加载结果覆盖")
	}
	waitFor(t, tt, func() bool { return tt.HasText("移除收藏 Claude Sonnet") })
	// 加载完成后同一次点击就能正常加入。
	if err := tt.Click("Other"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		pins, _, err := fake.ModelList(context.Background())
		return err == nil && len(pins) == 2
	})
}

// 没有模型的供应商（未启用、未登录或没配 API Key）不进入模型库，搜索与否都一样。
func TestFilterProviderModelsDropsProvidersWithoutModels(t *testing.T) {
	providers := []ProviderInfo{
		{ID: "claude", Name: "Claude", Available: true, Models: []ModelInfo{{ID: "opus", Name: "Opus"}}},
		{ID: "openai", Name: "OpenAI"},
	}
	got := filterProviderModels(providers, "")
	if len(got) != 1 || got[0].ID != "claude" {
		t.Fatalf("empty query should drop providers without models: %+v", got)
	}
}

// 添加模型浮层里，次要模型与主模型一起展示，不再折叠在“更多模型（N）”里。
func TestSettings_ModelsPage_SecondaryModelsInPicker(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	providers := []ProviderInfo{{ID: "cline", Name: "Cline", Models: []ModelInfo{
		{ID: "cline-pass/deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash", IsDefault: true},
		{ID: "cline-free/glm-5.3", Name: "GLM-5.3", Secondary: true},
		{ID: "cline-free/kimi-k3", Name: "Kimi K3", Secondary: true},
	}}}
	tt, _ := modelsPageWithProvidersTester(t, fake, providers)
	clickWhenSettled(t, tt, "添加收藏")
	waitFor(t, tt, func() bool {
		return tt.HasText("DeepSeek V4.1 Flash") && tt.HasText("GLM-5.3") && tt.HasText("Kimi K3")
	})
}

// leftoverProviderModels 只保留没有对应供应商行的供应商，并保持原顺序。
func TestLeftoverProviderModels(t *testing.T) {
	providers := []ProviderInfo{{ID: "claude"}, {ID: "pi"}, {ID: "deepseek"}}
	got := leftoverProviderModels(providers, []string{"claude", "codex"})
	if len(got) != 2 || got[0].ID != "pi" || got[1].ID != "deepseek" {
		t.Fatalf("剩余供应商不对或顺序被打乱: %+v", got)
	}
	if all := leftoverProviderModels(providers, nil); len(all) != 3 {
		t.Fatalf("没有供应商行时不应丢掉模型: %+v", all)
	}
}

// providerModelsFor 在模型库里按 ID 查供应商；没有模型库或 ID 不存在时返回 false。
func TestProviderModelsFor(t *testing.T) {
	if _, ok := providerModelsFor(nil, "claude"); ok {
		t.Fatal("nil 模型库不应命中")
	}
	lib := &modelsLibraryView{providers: []ProviderInfo{{ID: "claude", Name: "Claude"}}}
	if _, ok := providerModelsFor(lib, "codex"); ok {
		t.Fatal("不存在的供应商不应命中")
	}
	got, ok := providerModelsFor(lib, "claude")
	if !ok || got.Name != "Claude" {
		t.Fatalf("应命中的供应商未命中: %+v %v", got, ok)
	}
}

// 模型库并入供应商行：折叠时不渲染模型小节，展开后只列已添加的模型，
// 其余模型从「添加模型」浮层勾选。
func TestSettings_ModelsPage_ProviderRowShowsModels(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	providers := []ProviderInfo{{ID: "claude", Name: "Claude", Available: true, Models: []ModelInfo{{ID: "sonnet", Name: "Sonnet"}}}}
	fake.SetProviders(providers)
	tt, st := modelsDialogWithProvidersTester(t, fake, providers)
	settingsRevealInDetail(t, tt, providerDisplayName("claude"))
	clickProviderDetailTabForTest(t, tt, "claude", "")
	// 模型节在供应商面板中直接可见。
	selectProviderSheetSectionForTest(t, tt, st, "sec-provider-models")
	waitFor(t, tt, func() bool { return tt.HasText("Sonnet") })
	if tt.HasText("添加 Claude 模型") || tt.HasText("取消收藏 Sonnet") || tt.HasText("收藏 Sonnet") {
		t.Fatal("供应商目录不应提供收藏入口")
	}
}

// 添加模型浮层的搜索只过滤本供应商的候选模型，不影响别的供应商。
func TestSettings_ModelsPage_ProviderPickerSearchFiltersOwnProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	providers := []ProviderInfo{
		{ID: "claude", Name: "Claude", Available: true, Models: []ModelInfo{
			{ID: "sonnet", Name: "Sonnet"},
			{ID: "haiku", Name: "Haiku"},
		}},
		{ID: "codex", Name: "Codex", Available: true, Models: []ModelInfo{{ID: "o3", Name: "O3 模型"}}},
	}
	fake.SetProviders(providers)
	tt, _ := modelsPageWithProvidersTester(t, fake, providers)

	clickWhenSettled(t, tt, "添加收藏")
	waitFor(t, tt, func() bool {
		return tt.HasText("Sonnet") && tt.HasText("Haiku") && tt.HasText("O3 模型")
	})

	if err := tt.Click("input-provider-models-search-models-add-pin"); err != nil {
		t.Fatalf("click claude picker search failed: %v", err)
	}
	tt.Type("sonnet")

	// 本供应商未命中的模型消失，另一个供应商的模型本来就只在它自己的浮层里。
	waitFor(t, tt, func() bool {
		return tt.HasText("Sonnet") && !tt.HasText("Haiku") && !tt.HasText("O3 模型")
	})
}

// 添加模型浮层的搜索也能命中次要模型。
func TestSettings_ModelsPage_ProviderPickerSearchShowsSecondaryModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	providers := []ProviderInfo{
		{ID: "claude", Name: "Claude", Available: true, Models: []ModelInfo{
			{ID: "sonnet", Name: "Sonnet"},
			{ID: "haiku", Name: "Haiku", Secondary: true},
		}},
	}
	fake.SetProviders(providers)
	tt, _ := modelsPageWithProvidersTester(t, fake, providers)

	clickWhenSettled(t, tt, "添加收藏")
	waitFor(t, tt, func() bool { return tt.HasText("Sonnet") && tt.HasText("Haiku") })

	if err := tt.Click("input-provider-models-search-models-add-pin"); err != nil {
		t.Fatalf("click picker search failed: %v", err)
	}
	tt.Type("haiku")

	waitFor(t, tt, func() bool {
		return tt.HasText("Haiku") && !tt.HasText("Sonnet")
	})
}

// piMagpieTestClient 让测试预置 Magpie 端点并记录 SetPiMagpieModels 收到的模型 ID。
type piMagpieTestClient struct {
	*FakeClient
	endpoints []PiMagpieEndpoint

	mu      sync.Mutex
	saved   map[string][]string
	saveErr error
}

func (c *piMagpieTestClient) PiMagpieEndpoints(ctx context.Context) ([]PiMagpieEndpoint, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	res := make([]PiMagpieEndpoint, len(c.endpoints))
	copy(res, c.endpoints)
	return res, nil
}

func (c *piMagpieTestClient) SetPiMagpieModels(endpointID string, modelIDs []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.saveErr != nil {
		return c.saveErr
	}
	if c.saved == nil {
		c.saved = make(map[string][]string)
	}
	c.saved[endpointID] = append([]string(nil), modelIDs...)
	// 与后端一致：保存后端点返回新的勾选，重读快照不会把界面回退到旧值。
	for i := range c.endpoints {
		if c.endpoints[i].ID != endpointID {
			continue
		}
		c.endpoints[i].Selected = append([]string(nil), modelIDs...)
		break
	}
	return nil
}

func (c *piMagpieTestClient) savedModels(endpointID string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.saved[endpointID]...)
}

// TestSettings_ModelsPage_PiWithoutMagpieHasNoInjectHint 覆盖：没有 Magpie 端点时 pi 走通用模型列表，
// 底部不显示「已启用的模型会注入…」这段说明（列表里没有 Magpie 勾选框）。
func TestSettings_ModelsPage_PiWithoutMagpieHasNoInjectHint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prevDelays := piMagpieCatalogRefreshDelays
	piMagpieCatalogRefreshDelays = nil
	t.Cleanup(func() { piMagpieCatalogRefreshDelays = prevDelays })

	providers := []ProviderInfo{
		{ID: "pi", Name: "pi", Available: true, Models: []ModelInfo{{ID: "pi-own/a", Name: "自身模型"}}},
	}
	client := &piMagpieTestClient{FakeClient: NewFakeClient()}
	client.FakeClient.providerSettings = []ProviderSetting{{ID: "pi", Name: "Pi", Kind: "cli", Enabled: true}}
	client.SetProviders(providers)

	tt, _ := modelsDialogWithProvidersTester(t, client, providers)

	settingsRevealInDetail(t, tt, providerDisplayName("pi"))
	clickProviderDetailTabForTest(t, tt, "pi", "")
	waitFor(t, tt, func() bool { return tt.HasText("自身模型") })
	if tt.HasText("已启用的模型会注入 mcode 内启动的 pi") {
		t.Fatal("没有 Magpie 端点时不应显示注入说明")
	}
}

// 搜索词命中目录里的模型 ID 时，不显示「添加自定义模型」入口。
func TestSettings_ModelsPage_PickerHidesCustomEntryForCatalogModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	providers := []ProviderInfo{{ID: "claude", Name: "Claude", Available: true, Models: []ModelInfo{
		{ID: "sonnet", Name: "Sonnet"},
		{ID: "haiku", Name: "Haiku"},
	}}}
	fake.SetProviders(providers)
	tt, _ := modelsPageWithProvidersTester(t, fake, providers)

	clickWhenSettled(t, tt, "添加收藏")

	if err := tt.Click("input-provider-models-search-models-add-pin"); err != nil {
		t.Fatalf("点击搜索框失败: %v", err)
	}
	tt.Type("sonnet")
	// Haiku 消失才说明搜索词真的生效了，此时自定义入口仍应隐藏。
	waitFor(t, tt, func() bool { return tt.HasText("Sonnet") && !tt.HasText("Haiku") })
	if tt.HasText("添加自定义模型") {
		t.Fatalf("目录内 ID 不应出现自定义入口: %v", tt.Texts())
	}
}

// 目录为空但可用（已登录）的供应商显示模型区和空状态。
func TestSettings_ModelsPage_ShowsModelsSectionForEmptyCatalogWhenAvailable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	providers := []ProviderInfo{{ID: "codex", Name: "Codex", Available: true}}
	fake.SetProviders(providers)
	tt, st := modelsDialogWithProvidersTester(t, fake, providers)

	settingsRevealInDetail(t, tt, providerDisplayName("codex"))
	clickProviderDetailTabForTest(t, tt, "codex", "")
	// 空状态也在模型节里，先切到模型节再断言。
	selectProviderSheetSectionForTest(t, tt, st, "sec-provider-models")
	waitFor(t, tt, func() bool {
		return magpieTestHasExactText(tt, "模型") && tt.HasText("暂无模型")
	})
}

// 未登录（Available=false）且目录为空的供应商保持原样：不显示模型区。
func TestSettings_ModelsPage_EmptyCatalogUnavailableStaysHidden(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	providers := []ProviderInfo{{ID: "codex", Name: "Codex", Available: false}}
	fake.SetProviders(providers)
	tt, _ := modelsDialogWithProvidersTester(t, fake, providers)

	settingsRevealInDetail(t, tt, providerDisplayName("codex"))
	clickProviderDetailTabForTest(t, tt, "codex", "")
	if magpieTestHasExactText(tt, "模型") {
		t.Fatalf("未登录的空目录供应商不应显示模型区: %v", tt.Texts())
	}
}
