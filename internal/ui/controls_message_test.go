package mcodeui

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

func TestMessageCardStyles(t *testing.T) {
	seen := make(map[messageCardAppearance]MessageRole)
	for _, role := range []MessageRole{MessageRoleUser, MessageRoleAssistant, MessageRoleTool, MessageRoleSystem} {
		style := messageCardStyle(role)
		if previous, ok := seen[style]; ok {
			t.Fatalf("角色 %s 和 %s 的样式相同", previous, role)
		}
		seen[style] = role
	}
	if !messageCardStyle(MessageRoleUser).surface || messageCardStyle(MessageRoleUser).border {
		t.Fatal("用户消息应使用 SurfaceCard 底色且无边框")
	}
}

func TestBubbleMaxWidthFor(t *testing.T) {
	for _, tc := range []struct {
		width   float32
		setting string
		want    float32
	}{
		{768, config.ContentWidthStandard, 652.8},
		{1120, config.ContentWidthWide, 952},
	} {
		if got := bubbleMaxWidthFor(tc.width, tc.setting, 100, contentColumnPadX); math.Abs(float64(got-tc.want)) > 0.001 {
			t.Errorf("宽度 %v：得到 %v，期望 %v", tc.width, got, tc.want)
		}
	}
	for _, tc := range []struct {
		width   float32
		percent int
	}{{400, 100}, {1200, 70}, {2000, 100}} {
		state := newProportionalWidthTestState(t, tc.percent, tc.width)
		setting, percent, padX := contentWidthPrefs(state)
		if got, want := bubbleMaxWidthFor(tc.width, setting, percent, padX), userBubbleMaxWidth(state); got != want {
			t.Errorf("按比例宽度 %v/%d：得到 %v，期望 %v", tc.width, tc.percent, got, want)
		}
	}
}

func TestToolStateMarker(t *testing.T) {
	seen := make(map[toolMarkerShape]ToolState)
	for _, tc := range []struct {
		state ToolState
		shape toolMarkerShape
		tone  Tone
	}{
		{ToolPending, toolMarkerHollow, ToneNeutral},
		{ToolRunning, toolMarkerSpinner, ToneAccent},
		{ToolDone, toolMarkerCheck, ToneSuccess},
		{ToolError, toolMarkerAlert, ToneDanger},
	} {
		shape, tone := toolStateMarker(tc.state)
		if previous, ok := seen[shape]; ok {
			t.Fatalf("工具状态 %v 和 %v 的标记相同", previous, tc.state)
		}
		seen[shape] = tc.state
		if shape != tc.shape || tone != tc.tone {
			t.Errorf("工具状态 %v：标记 = %v，颜色 = %v", tc.state, shape, tone)
		}
	}
}

func TestToolChipClickAndDuration(t *testing.T) {
	clicked := 0
	tt := ui.NewTester(func(c *ui.Context) {
		ToolChip(c, ToolChipProps{Title: "读取文件", State: ToolDone, Duration: "1.2s", OnClick: func() { clicked++ }})
	}, 300, 100)
	if !tt.HasText("读取文件") {
		t.Fatalf("未渲染标题：%q", tt.Texts())
	}
	if !tt.HasText("1.2s") {
		t.Fatalf("未渲染耗时：%q", tt.Texts())
	}
	if err := tt.Click("读取文件"); err != nil {
		t.Fatalf("点击工具胶囊失败：%v", err)
	}
	if clicked != 1 {
		t.Fatalf("点击回调次数 = %d，期望 1", clicked)
	}
}
