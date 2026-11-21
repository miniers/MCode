package mcodeui

import (
	"reflect"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

// 测试 effortSliderSnapIndex 的边界情况与最近刻度吸附。
func TestEffortSliderSnapIndex(t *testing.T) {
	testenv.TempHome(t)

	// n <= 1 或异常宽度的边界情况
	if got := effortSliderSnapIndex(50, 100, 0); got != 0 {
		t.Fatalf("snapIndex(50, 100, 0) = %d, want 0", got)
	}
	if got := effortSliderSnapIndex(50, 100, 1); got != 0 {
		t.Fatalf("snapIndex(50, 100, 1) = %d, want 0", got)
	}
	if got := effortSliderSnapIndex(50, 0, 3); got != 0 {
		t.Fatalf("snapIndex(50, 0, 3) = %d, want 0", got)
	}
	if got := effortSliderSnapIndex(-10, 100, 3); got != 0 {
		t.Fatalf("snapIndex(-10, 100, 3) = %d, want 0", got)
	}
	if got := effortSliderSnapIndex(150, 100, 3); got != 2 {
		t.Fatalf("snapIndex(150, 100, 3) = %d, want 2", got)
	}

	// 3 档（0, 50, 100）吸附测试
	cases := []struct {
		x        float32
		width    float32
		n        int
		expected int
	}{
		{x: 0, width: 100, n: 3, expected: 0},
		{x: 24, width: 100, n: 3, expected: 0},
		{x: 25, width: 100, n: 3, expected: 1},
		{x: 50, width: 100, n: 3, expected: 1},
		{x: 74, width: 100, n: 3, expected: 1},
		{x: 75, width: 100, n: 3, expected: 2},
		{x: 100, width: 100, n: 3, expected: 2},
	}
	for _, tc := range cases {
		if got := effortSliderSnapIndex(tc.x, tc.width, tc.n); got != tc.expected {
			t.Errorf("snapIndex(%v, %v, %v) = %d, want %d", tc.x, tc.width, tc.n, got, tc.expected)
		}
	}
}

func TestEffortSliderCardInteraction(t *testing.T) {
	testenv.TempHome(t)
	efforts := []string{"low", "medium", "high", "max"}
	current := "low"
	tt := ui.NewTester(func(c *ui.Context) {
		renderEffortSliderCard(c, EffortSliderCardProps{
			ModelTitle: "Claude 3.7 Sonnet",
			Efforts:    efforts,
			Current:    current,
			OnEffort:   func(effort string) { current = effort },
		})
	}, 352, 240)
	tt.Frame()
	r, ok := tt.Find("推理强度")
	if !ok || r.W <= 24 {
		t.Fatalf("missing slider: %+v", r)
	}
	// 滑轨左右各留半个滑块宽度（32）的内边距，点击 0.55 处吸附到 high。
	tt.ClickAt(r.X+40+(r.W-80)*0.55, r.Y+r.H/2)
	if current != "high" {
		t.Fatalf("click must snap to high, got %q", current)
	}
	tt.Key(0, ui.KeyLeft)
	if current != "medium" {
		t.Fatalf("left must select medium, got %q", current)
	}
	tt.Key(0, ui.KeyEnd)
	if current != "max" {
		t.Fatalf("end must select max, got %q", current)
	}
	// 大标题展示模型名，档位只作为英文短标签画在滑块胶囊与无障碍值里，不再作为独立文本铺在卡片上。
	tt.Frame()
	for _, label := range []string{"Low", "Med", "Medium", "High", "Max", "低", "中", "高", "最大"} {
		if tt.HasText(label) {
			t.Fatalf("tier label %q must not be rendered as standalone text", label)
		}
	}
	if !tt.HasText("Claude 3.7 Sonnet") {
		t.Fatalf("expected model title Claude 3.7 Sonnet, got %v", tt.Texts())
	}
	info, ok := tt.Access("推理强度")
	if !ok || info.Value != "Max" {
		t.Fatalf("expected slider value Max, got (%+v, %v)", info, ok)
	}
}

func TestEffortSliderCardSingleLevel(t *testing.T) {
	testenv.TempHome(t)
	// 无档位时只显示说明文字；单档模型仍渲染滑轨，不出现说明。
	tt := ui.NewTester(func(c *ui.Context) {
		renderEffortSliderCard(c, EffortSliderCardProps{})
	}, 330, 240)
	tt.Frame()
	if !tt.HasText("该模型未提供可调的推理强度") {
		t.Fatalf("expected no-effort hint: %v", tt.Texts())
	}
	if _, ok := tt.Find("推理强度"); ok {
		t.Fatalf("model without efforts must hide slider: %v", tt.Texts())
	}

	tt = ui.NewTester(func(c *ui.Context) {
		renderEffortSliderCard(c, EffortSliderCardProps{Efforts: []string{"medium"}})
	}, 330, 240)
	tt.Frame()
	if tt.HasText("该模型未提供可调的推理强度") {
		t.Fatalf("single-level model must not show the no-effort hint: %v", tt.Texts())
	}
	if _, ok := tt.Find("推理强度"); !ok {
		t.Fatalf("single-level model must still render the slider: %v", tt.Texts())
	}
}

func TestEffortCardTitle(t *testing.T) {
	testenv.TempHome(t)
	p := EffortSliderCardProps{
		ModelTitle: "Claude Opus 4.6",
		Subtitle:   "fallback",
		Efforts:    []string{"low", "medium", "high"},
	}
	if got := effortCardTitle(p); got != "Claude Opus 4.6" {
		t.Fatalf("model title = %q", got)
	}
	p.ModelTitle = "  "
	p.Subtitle = "  GPT-5  "
	if got := effortCardTitle(p); got != "GPT-5" {
		t.Fatalf("fallback subtitle title = %q", got)
	}
	p.Subtitle = ""
	if got := effortCardTitle(p); got != "标准" {
		t.Fatalf("empty title fallback = %q", got)
	}
}

// 测试 effortTickPositions 等距分布与边界。
func TestEffortTickPositions(t *testing.T) {
	testenv.TempHome(t)

	if got := effortTickPositions(0, 100); got != nil {
		t.Fatalf("tickPositions(0, 100) = %v, want nil", got)
	}
	if got := effortTickPositions(1, 100); !reflect.DeepEqual(got, []float32{0}) {
		t.Fatalf("tickPositions(1, 100) = %v, want [0]", got)
	}

	want3 := []float32{0, 50, 100}
	got3 := effortTickPositions(3, 100)
	if !reflect.DeepEqual(got3, want3) {
		t.Fatalf("tickPositions(3, 100) = %v, want %v", got3, want3)
	}

	want5 := []float32{0, 25, 50, 75, 100}
	got5 := effortTickPositions(5, 100)
	if !reflect.DeepEqual(got5, want5) {
		t.Fatalf("tickPositions(5, 100) = %v, want %v", got5, want5)
	}
}

// 测试当前值、默认值、中档和首档的回退规则。
func TestEffortIndexFallback(t *testing.T) {
	testenv.TempHome(t)

	efforts := []string{"low", "medium", "high"}

	// 1. 正常匹配 current
	if got := effortIndex(efforts, "medium", "high"); got != 1 {
		t.Fatalf("effortIndex matched = %d, want 1", got)
	}
	// 2. current 为空，回退至 def
	if got := effortIndex(efforts, "", "high"); got != 2 {
		t.Fatalf("effortIndex def = %d, want 2", got)
	}
	// 3. current 不存在，回退至 def
	if got := effortIndex(efforts, "non-exist", "low"); got != 0 {
		t.Fatalf("effortIndex fallback def = %d, want 0", got)
	}
	// 4. current 和 def 均不存在，回退至中档。
	if got := effortIndex(efforts, "non-exist", "also-non-exist"); got != 1 {
		t.Fatalf("effortIndex fallback medium = %d, want 1", got)
	}
	if got := effortIndex([]string{"low", "high"}, "", ""); got != 0 {
		t.Fatalf("effortIndex fallback first = %d, want 0", got)
	}
	// 5. efforts 为空返回 0
	if got := effortIndex(nil, "low", "high"); got != 0 {
		t.Fatalf("effortIndex empty = %d, want 0", got)
	}
}

// 测试 swarmSideTabLabel 的文案规则：档位用 modelEffortTitle 的英文名，关闭的执行侧为“执行 · 关闭”。
func TestSwarmSideTabLabel(t *testing.T) {
	testenv.TempHome(t)

	cases := []struct {
		role, effort string
		off          bool
		want         string
	}{
		{"lead", "medium", false, "主代理 · Medium"},
		{"lead", "xhigh", false, "主代理 · Extra High"},
		{"lead", "", false, "主代理"},
		{"bees", "max", false, "子代理 · Max"},
		{"bees", "HIGH", false, "子代理 · High"},
		{"bees", "", false, "子代理"},
		{"bees", "high", true, "子代理 · 关闭"},
		{"bees", "", true, "子代理 · 关闭"},
	}
	for _, tc := range cases {
		if got := swarmSideTabLabel(tc.role, tc.effort, tc.off); got != tc.want {
			t.Errorf("swarmSideTabLabel(%q, %q, %v) = %q, want %q", tc.role, tc.effort, tc.off, got, tc.want)
		}
	}
}

// 测试 effortLabel 中英文档位映射。
func TestEffortLabel(t *testing.T) {
	testenv.TempHome(t)

	cases := map[string]string{
		"minimal": "最低",
		"low":     "低",
		"medium":  "中",
		"high":    "高",
		"xhigh":   "超高",
		"max":     "最大",
		"custom":  "custom",
	}
	for in, want := range cases {
		if got := effortLabel(in); got != want {
			t.Errorf("effortLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// 测试 effortThumbLabel 英文短档位映射。
func TestEffortThumbLabel(t *testing.T) {
	testenv.TempHome(t)

	cases := map[string]string{
		"minimal":    "Min",
		"none":       "Off",
		"low":        "Low",
		"medium":     "Med",
		"high":       "High",
		" HIGH ":     "High",
		"xhigh":      "XHigh",
		"extra-high": "XHigh",
		"extra_high": "XHigh",
		"extra high": "XHigh",
		"max":        "Max",
		"ultra":      "Max",
		"custom":     "Custom",
		"":           "",
		"   ":        "",
	}
	for in, want := range cases {
		if got := effortThumbLabel(in); got != want {
			t.Errorf("effortThumbLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// 测试 effortBrandColor：已知 provider 映射与未知 provider 回退主题 accent。
func TestEffortBrandColor(t *testing.T) {
	white := ui.RGB(255, 255, 255)
	cases := []struct {
		provider string
		want     ui.Color
	}{
		{provider: "claude", want: ui.Hex("#D97757")},
		{provider: "Claude", want: ui.Hex("#D97757")},
		{provider: "codex", want: ui.Hex("#10A37F")},
		{provider: "gemini", want: ui.Hex("#4285F4")},
		{provider: "antigravity", want: ui.Hex("#4285F4")},
		{provider: "agy", want: ui.Hex("#4285F4")},
		{provider: "deepseek", want: ui.Hex("#4D6BFE")},
		{provider: "meta", want: ui.Hex("#0082FB")},
		// 黑白标识品牌统一白色
		{provider: "cursor", want: white},
		{provider: "opencode", want: white},
		{provider: "opencode go", want: white},
		{provider: "copilot", want: white},
		{provider: "pi", want: white},
		{provider: "omp", want: white},
		{provider: "commandcode", want: white},
		{provider: "zai", want: white},
		// 未知 provider 回退深色主题 accent
		{provider: "no-such-provider", want: ColorAccentDark},
		{provider: "", want: ColorAccentDark},
	}
	for _, tc := range cases {
		if got := effortBrandColor(tc.provider); got != tc.want {
			t.Errorf("effortBrandColor(%q) = %v, want %v", tc.provider, got, tc.want)
		}
	}
}

// 测试 effortTrackEffect：pair 优先，其次 fusion、fast、supercharged，兜底 plain。
func TestEffortTrackEffect(t *testing.T) {
	cases := []struct {
		isMax, isFast, isPair bool
		want                  string
	}{
		{isMax: false, isFast: false, isPair: false, want: "plain"},
		{isMax: false, isFast: true, isPair: false, want: "fast"},
		{isMax: true, isFast: false, isPair: false, want: "supercharged"},
		{isMax: true, isFast: true, isPair: false, want: "fusion"},
		{isMax: false, isFast: false, isPair: true, want: "pair"},
		{isMax: true, isFast: true, isPair: true, want: "pair"},
	}
	for _, tc := range cases {
		if got := effortTrackEffect(tc.isMax, tc.isFast, tc.isPair); got != tc.want {
			t.Errorf("effortTrackEffect(%v, %v, %v) = %q, want %q",
				tc.isMax, tc.isFast, tc.isPair, got, tc.want)
		}
	}
}

// 测试 effortTitleColors：普通档 accent 提亮色、快速金色、最高档品牌色、最高档+快速品牌→金、配对 Lead→Bees。
func TestEffortTitleColors(t *testing.T) {
	plain := effortTitleColor(EffortSliderCardProps{}) // 普通档：accent 提亮色
	gold := effortFastColor()
	claude := ui.Hex("#D97757")
	codex := ui.Hex("#10A37F")

	cases := []struct {
		name       string
		mutate     func(p *EffortSliderCardProps)
		wantFrom   ui.Color
		wantTo     ui.Color
		wantColors int // 去重后颜色数量：实色 1，渐变 2
	}{
		{
			name:     "普通档 accent 提亮色",
			mutate:   func(p *EffortSliderCardProps) { p.Current = "low" },
			wantFrom: plain, wantTo: plain, wantColors: 1,
		},
		{
			name: "快速模式金色",
			mutate: func(p *EffortSliderCardProps) {
				p.Current = "medium"
				p.FastMode = true
			},
			wantFrom: gold, wantTo: gold, wantColors: 1,
		},
		{
			name:     "最高档为品牌色",
			mutate:   func(p *EffortSliderCardProps) { p.Current = "high" },
			wantFrom: claude, wantTo: claude, wantColors: 1,
		},
		{
			name: "最高档 codex 品牌色",
			mutate: func(p *EffortSliderCardProps) {
				p.Current = "high"
				p.ProviderID = "codex"
			},
			wantFrom: codex, wantTo: codex, wantColors: 1,
		},
		{
			name: "最高档+快速为品牌色→金色",
			mutate: func(p *EffortSliderCardProps) {
				p.Current = "high"
				p.FastMode = true
			},
			wantFrom: claude, wantTo: gold, wantColors: 2,
		},
		{
			name: "配对为 Lead 品牌色→Bees 品牌色",
			mutate: func(p *EffortSliderCardProps) {
				p.Paired = true
				p.PairProviderID = "codex"
				p.Current = "low"
			},
			wantFrom: claude, wantTo: codex, wantColors: 2,
		},
		{
			name: "配对+快速尾端收金色",
			mutate: func(p *EffortSliderCardProps) {
				p.Paired = true
				p.PairProviderID = "codex"
				p.Current = "low"
				p.FastMode = true
			},
			wantFrom: claude, wantTo: gold, wantColors: 2,
		},
		{
			name: "配对未配置 Bees provider 时回退 Lead 品牌色",
			mutate: func(p *EffortSliderCardProps) {
				p.Paired = true
				p.Current = "low"
			},
			wantFrom: claude, wantTo: claude, wantColors: 1,
		},
		{
			name: "单档即使位于末位也不触发品牌色（保持 accent 提亮色）",
			mutate: func(p *EffortSliderCardProps) {
				p.Efforts = []string{"medium"}
			},
			wantFrom: plain, wantTo: plain, wantColors: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := EffortSliderCardProps{
				ProviderID:   "claude",
				Efforts:      []string{"low", "medium", "high"},
				SupportsFast: true,
			}
			tc.mutate(&p)
			from, to := effortTitleColors(nil, p)
			if from != tc.wantFrom || to != tc.wantTo {
				t.Fatalf("effortTitleColors = (%v, %v), want (%v, %v)", from, to, tc.wantFrom, tc.wantTo)
			}
			colors := 1
			if from != to {
				colors = 2
			}
			if colors != tc.wantColors {
				t.Fatalf("effortTitleColors 颜色数量 = %d, want %d", colors, tc.wantColors)
			}
		})
	}
}

// 测试卡片大标题展示模型名，滑块无障碍值展示当前英文短档位名，切换档位后标题保持不变；不再有“当前：”行。
func TestEffortSliderCardShowsCurrentTierTitle(t *testing.T) {
	testenv.TempHome(t)
	current := "low"
	tt := ui.NewTester(func(c *ui.Context) {
		renderEffortSliderCard(c, EffortSliderCardProps{
			ModelTitle: "Claude 3.7 Sonnet",
			Efforts:    []string{"low", "medium", "high", "max"},
			Current:    current,
			OnEffort:   func(effort string) { current = effort },
		})
	}, 352, 260)
	tt.Frame()
	if !tt.HasText("Claude 3.7 Sonnet") {
		t.Fatalf("expected title 'Claude 3.7 Sonnet', got %v", tt.Texts())
	}
	if tt.HasText("Low") || tt.HasText("当前：") {
		t.Fatalf("tier text and current-tier row must not appear as card text, got %v", tt.Texts())
	}
	if info, ok := tt.Access("推理强度"); !ok || info.Value != "Low" {
		t.Fatalf("expected initial slider value 'Low', got (%+v, %v)", info, ok)
	}

	r, ok := tt.Find("推理强度")
	if !ok {
		t.Fatal("missing slider")
	}
	tt.ClickAt(r.X+40+(r.W-80), r.Y+r.H/2)
	if current != "max" {
		t.Fatalf("click must snap to max, got %q", current)
	}
	tt.Frame()
	if !tt.HasText("Claude 3.7 Sonnet") || tt.HasText("Max") {
		t.Fatalf("title must remain model name and not show 'Max' as card text, got %v", tt.Texts())
	}
	if info, ok := tt.Access("推理强度"); !ok || info.Value != "Max" {
		t.Fatalf("expected slider value 'Max' after change, got (%+v, %v)", info, ok)
	}
}

// 测试卡片大标题显示模型名、不重复副标题行，且切换档位时滑块无障碍值同步更新为 Low -> Med -> High -> Max。
func TestEffortSliderCardShowsModelTitleAndPillTier(t *testing.T) {
	testenv.TempHome(t)
	current := "low"
	tt := ui.NewTester(func(c *ui.Context) {
		renderEffortSliderCard(c, EffortSliderCardProps{
			ProviderID: "claude",
			ModelTitle: "Claude Opus 4.6",
			Efforts:    []string{"low", "medium", "high", "max"},
			Current:    current,
			OnEffort:   func(effort string) { current = effort },
		})
	}, 352, 260)
	tt.Frame()

	countText := func(target string) int {
		n := 0
		for _, s := range tt.Texts() {
			if s == target {
				n++
			}
		}
		return n
	}

	if got := countText("Claude Opus 4.6"); got != 1 {
		t.Fatalf("expected single model title without duplicate subtitle, got count=%d texts=%v", got, tt.Texts())
	}
	if tt.FramePending() {
		t.Fatal("idle effort slider card must not request another frame")
	}

	r, ok := tt.Find("推理强度")
	if !ok {
		t.Fatal("missing slider")
	}
	// 点击首档聚焦滑块，再依次右移档位验证 Low -> Med -> High -> Max。
	tt.ClickAt(r.X+32, r.Y+r.H/2)
	wantTiers := []string{"Low", "Med", "High", "Max"}
	for i, want := range wantTiers {
		if i > 0 {
			tt.Key(0, ui.KeyRight)
		}
		tt.Frame()
		if got := countText("Claude Opus 4.6"); got != 1 {
			t.Fatalf("step %d: expected model title once, got count=%d texts=%v", i, got, tt.Texts())
		}
		info, ok := tt.Access("推理强度")
		if !ok || info.Value != want {
			t.Fatalf("step %d: slider value = (%q, %v), want %q", i, info.Value, ok, want)
		}
	}
}

// 测试禁用时各控件 Tooltip 使用卡片给出的禁用原因。
func TestEffortLockedTip(t *testing.T) {
	if got := effortLockedTip(EffortSliderCardProps{LockedReason: "  "}, "重置强度与快速模式"); got != "重置强度与快速模式" {
		t.Fatalf("empty reason should fall back, got %q", got)
	}
	if got := effortLockedTip(EffortSliderCardProps{LockedReason: "未开启 Swarm 时执行侧不可设置"}, "x"); got != "未开启 Swarm 时执行侧不可设置" {
		t.Fatalf("reason should win, got %q", got)
	}
}

// 测试配对说明行：主模型在标题行、加号与子代理模型在副标题行，主模型不重复出现。
func TestEffortSliderCardPairedSubtitleRow(t *testing.T) {
	testenv.TempHome(t)
	tt := ui.NewTester(func(c *ui.Context) {
		renderEffortSliderCard(c, EffortSliderCardProps{
			Paired:         true,
			IsBees:         false,
			ModelTitle:     "模型A",
			Subtitle:       "模型A",
			SubtitlePair:   "模型B",
			ProviderID:     "claude",
			PairProviderID: "codex",
		})
	}, 360, 240)
	tt.Frame()
	for _, text := range []string{"模型A", "+", "模型B"} {
		if _, ok := tt.Find(text); !ok {
			t.Fatalf("missing %q in rendered card, texts: %v", text, tt.Texts())
		}
	}
	count := 0
	for _, s := range tt.Texts() {
		if s == "模型A" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected lead model title once (not repeated in subtitle), got %d in %v", count, tt.Texts())
	}
}

func TestEffortSliderCardRosterPairedLayout(t *testing.T) {
	testenv.TempHome(t)
	var selectBeesCalls, openLeadCalls int
	var favClicked bool
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(modelPickerPanelWidth).Background(modelPickerPanelBg).Children(func() {
			renderEffortSliderCard(c, EffortSliderCardProps{
				KeyPrefix:     "roster-",
				ProviderID:    "claude",
				SwarmOn:       true,
				Paired:        true,
				SupportsFast:  true,
				Efforts:       []string{"low", "medium", "high"},
				Current:       "high",
				OnToggleSwarm: func() {},
				OnToggleFast:  func() {},
				OnReset:       func() {},
				PairFav: &pairFavProps{
					Matched:  false,
					OnToggle: func() { favClicked = true },
				},
				Roster: []EffortRosterSlot{
					{
						Key:         "lead",
						Role:        "主代理",
						ProviderID:  "claude",
						ModelTitle:  "Claude Opus 4.6",
						EffortLabel: "High",
						Active:      true,
						OnSelect:    func() {},
						OnOpenList:  func() { openLeadCalls++ },
					},
					{
						Key:         "bees",
						Role:        "子代理",
						ProviderID:  "codex",
						ModelTitle:  "o3-mini",
						EffortLabel: "Med",
						Active:      false,
						OnSelect:    func() { selectBeesCalls++ },
						OnOpenList:  func() {},
					},
				},
			})
		})
	}, modelPickerPanelWidth, 300)
	tt.Frame()

	for _, want := range []string{"编排", "主代理", "Claude Opus 4.6", "High", "子代理", "o3-mini", "Med"} {
		if !tt.HasText(want) {
			t.Fatalf("缺少文案 %q, texts=%v", want, tt.Texts())
		}
	}
	if tt.HasText("+") {
		t.Fatalf("双槽配对卡不应渲染多余的 '+': %v", tt.Texts())
	}
	if _, ok := tt.Access("编排开关"); !ok {
		t.Fatalf("缺少顶栏编排开关按钮: %v", tt.AccessAll())
	}
	if _, ok := tt.Access("存为配对"); !ok {
		t.Fatalf("缺少顶栏收藏配对按钮: %v", tt.AccessAll())
	}

	if err := tt.Click("子代理"); err != nil {
		t.Fatalf("点击未激活子代理行失败: %v", err)
	}
	if selectBeesCalls != 1 {
		t.Fatalf("OnSelect 调用次数 = %d, want 1", selectBeesCalls)
	}

	if err := tt.Click("选择其他模型"); err != nil {
		t.Fatalf("点击激活主代理行失败: %v", err)
	}
	if openLeadCalls != 1 {
		t.Fatalf("OnOpenList 调用次数 = %d, want 1", openLeadCalls)
	}

	if err := tt.Click("存为配对"); err != nil {
		t.Fatalf("点击存为配对按钮失败: %v", err)
	}
	if !favClicked {
		t.Fatal("点击存为配对应触发 PairFav.OnToggle")
	}

	tt.Advance(time.Second)
	tt.Frame()
	if tt.FramePending() {
		t.Fatal("静止的双槽配对卡片不应持续请求重绘")
	}
}
