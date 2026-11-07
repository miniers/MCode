package mcodeui

import (
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 测试 effortSliderSnapIndex 的边界情况与最近刻度吸附。
func TestEffortSliderSnapIndex(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

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
	t.Setenv("HOME", t.TempDir())
	efforts := []string{"low", "medium", "high", "max"}
	current := "low"
	tt := ui.NewTester(func(c *ui.Context) {
		renderEffortSliderCard(c, EffortSliderCardProps{
			Efforts: efforts, Current: current,
			OnEffort: func(effort string) { current = effort },
		})
	}, 330, 240)
	tt.Frame()
	r, ok := tt.Find("推理强度")
	if !ok || r.W <= 24 {
		t.Fatalf("missing slider: %+v", r)
	}
	// 滑轨左右各留半个滑块宽度（40）的内边距，取值范围在其间线性映射。
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
	// 档位名不再铺在轨道下方，只出现在大标题（当前档 Max）与无障碍值里。
	tt.Frame()
	for _, label := range []string{"Low", "Medium", "High", "低", "中", "高", "最大"} {
		if tt.HasText(label) {
			t.Fatalf("tick label %q must not be rendered under the track", label)
		}
	}
	if !tt.HasText("Max") {
		t.Fatalf("expected title Max, got %v", tt.Texts())
	}
}

func TestEffortSliderCardSingleLevel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
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
	t.Setenv("HOME", t.TempDir())
	p := EffortSliderCardProps{Efforts: []string{"low", "medium", "high"}}
	if got := effortCardTitle(p); got != "Medium" {
		t.Fatalf("default title = %q", got)
	}
	p.Current = "high"
	if got := effortCardTitle(p); got != "High" {
		t.Fatalf("selected title = %q", got)
	}
	p.Efforts = nil
	if got := effortCardTitle(p); got != "标准" {
		t.Fatalf("single-level title = %q", got)
	}
}

// 测试 effortTickPositions 等距分布与边界。
func TestEffortTickPositions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

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
	t.Setenv("HOME", t.TempDir())

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
	t.Setenv("HOME", t.TempDir())

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
	t.Setenv("HOME", t.TempDir())

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

// 测试卡片大标题显示当前档位英文名，并在切换档位后同步更新；不再有“当前：”行。
func TestEffortSliderCardShowsCurrentTierTitle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	current := "low"
	tt := ui.NewTester(func(c *ui.Context) {
		renderEffortSliderCard(c, EffortSliderCardProps{
			Efforts: []string{"low", "medium", "high", "max"}, Current: current,
			OnEffort: func(effort string) { current = effort },
		})
	}, 330, 260)
	tt.Frame()
	if !tt.HasText("Low") {
		t.Fatalf("expected title 'Low', got %v", tt.Texts())
	}
	if tt.HasText("当前：") {
		t.Fatalf("current-tier row must be gone, got %v", tt.Texts())
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
	if !tt.HasText("Max") {
		t.Fatalf("expected title 'Max' after change, got %v", tt.Texts())
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

// 测试配对说明行：主模型、加号与子代理模型均能在卡片中正确渲染。
func TestEffortSliderCardPairedSubtitleRow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tt := ui.NewTester(func(c *ui.Context) {
		renderEffortSliderCard(c, EffortSliderCardProps{
			Paired:         true,
			IsBees:         false,
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
}
