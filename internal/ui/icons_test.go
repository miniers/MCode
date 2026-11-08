package mcodeui

import (
	"testing"
)

// 测试所有常见 Provider ID 与别名均能正确解析出图标，且未知 ID 正确回退到通用脑部图标。
func TestProviderIcon_ResolutionAndFallback(t *testing.T) {
	cases := []struct {
		providerID string
		wantGlobe  bool
		wantBrain  bool
	}{
		{providerID: "claude"},
		{providerID: "codex"},
		{providerID: "openai"},
		{providerID: "agy"},
		{providerID: "antigravity"},
		{providerID: "copilot"},
		{providerID: "mcode", wantBrain: true},
		{providerID: "pi"},
		{providerID: "mu"},
		{providerID: "omp"},
		{providerID: "commandcode"},
		{providerID: "cmd"},
		{providerID: "cline"},
		{providerID: "acp"},
		{providerID: "droid"},
		{providerID: "deepseek"},
		{providerID: "zai"},
		{providerID: "meta"},
		{providerID: "custom-my-endpoint", wantGlobe: true},
		{providerID: "unknown-provider-xyz", wantBrain: true},
		{providerID: "", wantBrain: true},
	}

	for _, tc := range cases {
		t.Run("provider_"+tc.providerID, func(t *testing.T) {
			svg := ProviderIcon(tc.providerID)
			if svg == nil {
				t.Fatalf("ProviderIcon(%q) returned nil", tc.providerID)
			}
			w, h := svg.Size()
			if w <= 0 || h <= 0 {
				t.Fatalf("ProviderIcon(%q) has invalid size %vx%v", tc.providerID, w, h)
			}
			if tc.wantGlobe && svg != iconGlobe {
				t.Fatalf("expected iconGlobe for %q, got %v", tc.providerID, svg)
			}
			if tc.wantBrain && svg != iconBrain {
				t.Fatalf("expected iconBrain for %q, got %v", tc.providerID, svg)
			}

			// 彩色版本也必须存在并有正尺寸
			colorSVG := ProviderIconColor(tc.providerID)
			if colorSVG == nil {
				t.Fatalf("ProviderIconColor(%q) returned nil", tc.providerID)
			}
			cw, ch := colorSVG.Size()
			if cw <= 0 || ch <= 0 {
				t.Fatalf("ProviderIconColor(%q) has invalid size %vx%v", tc.providerID, cw, ch)
			}
		})
	}
}

// 测试向上箭头图标（IconArrowUp 与 IconSend）的尺寸和定义。
func TestIconArrowUp_AndSend(t *testing.T) {
	up := IconArrowUp()
	if up == nil {
		t.Fatal("IconArrowUp() returned nil")
	}
	w, h := up.Size()
	if w != 24 || h != 24 {
		t.Fatalf("IconArrowUp() size = %vx%v, want 24x24", w, h)
	}

	send := IconSend()
	if send == nil {
		t.Fatal("IconSend() returned nil")
	}
	sw, sh := send.Size()
	if sw != 24 || sh != 24 {
		t.Fatalf("IconSend() size = %vx%v, want 24x24", sw, sh)
	}
}

// 测试停止态方形图标（IconStopFilled）存在且非空。
func TestIconStopFilled(t *testing.T) {
	stop := IconStopFilled()
	if stop == nil {
		t.Fatal("IconStopFilled() returned nil")
	}
	w, h := stop.Size()
	if w <= 0 || h <= 0 {
		t.Fatalf("IconStopFilled() size = %vx%v, want positive size", w, h)
	}
}

// 测试上下文用量圆环生成 SVG 在深浅模式下均正常且带有效路径。
func TestMakeContextMeterSVG(t *testing.T) {
	for _, dark := range []bool{true, false} {
		for _, fraction := range []float64{0.0, 0.5, 0.9, 1.2} {
			svg := makeContextMeterSVG(dark, fraction, fraction > 0.85)
			if svg == nil {
				t.Fatalf("makeContextMeterSVG(dark=%v, fraction=%v) returned nil", dark, fraction)
			}
			w, h := svg.Size()
			if w != 24 || h != 24 {
				t.Fatalf("makeContextMeterSVG size = %vx%v, want 24x24", w, h)
			}
		}
	}
}

// 测试 providerIconKey 对常见 Provider ID、别名及带短横线前缀（如 commandcode-plan）的归一化映射。
func TestProviderIconKey(t *testing.T) {
	cases := []struct {
		id   string
		want string
	}{
		{id: "agy", want: "antigravity"},
		{id: "commandcode-plan", want: "commandcode"},
		{id: "codex", want: "codex"},
		{id: "workbuddy", want: "workbuddy"},
	}
	for _, tc := range cases {
		got := providerIconKey(tc.id)
		if got != tc.want {
			t.Errorf("providerIconKey(%q) = %q, want %q", tc.id, got, tc.want)
		}
	}
}
