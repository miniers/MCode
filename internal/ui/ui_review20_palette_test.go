package mcodeui

import (
	"bytes"
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

type uiReview20SearchClient struct {
	*FakeClient
	mu          sync.Mutex
	started     chan struct{}
	release     chan struct{}
	hasDeadline bool
	err         error
	hits        []MessageSearchHit
}

func (c *uiReview20SearchClient) SearchMessages(ctx context.Context, query string, limit int) ([]MessageSearchHit, error) {
	_, ok := ctx.Deadline()
	c.mu.Lock()
	c.hasDeadline = ok
	err := c.err
	hits := c.hits
	c.mu.Unlock()

	if c.started != nil {
		select {
		case c.started <- struct{}{}:
		default:
		}
	}
	if c.release != nil {
		<-c.release
	}
	return hits, err
}

// TestUIReview20PaletteFilterCache 验证面板打开后连续渲染多帧且 query 不变时，build/filter 只执行一次。
func TestUIReview20PaletteFilterCache(t *testing.T) {
	fake := NewFakeClient()
	state := newChatState(fake)
	t.Cleanup(state.close)

	openPalette(state)
	paletteVisibleBuildCount.Store(0)

	tt := ui.NewTester(func(c *ui.Context) {
		state.init(c)
		state.applyWorkspaceUpdates()
		renderCommandPalette(c, state)
	}, 1000, 700)

	for i := 0; i < 5; i++ {
		tt.Frame()
	}
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyUp)

	if got := paletteVisibleBuildCount.Load(); got != 1 {
		t.Fatalf("query 不变连续渲染多帧及上下键后重算次数 = %d，期望 1", got)
	}

	p := getPaletteState(state)
	p.query = "新建"
	tt.Frame()
	tt.Frame()
	if got := paletteVisibleBuildCount.Load(); got != 2 {
		t.Fatalf("query 变化后连续渲染重算次数 = %d，期望 2", got)
	}
}

// TestUIReview20PaletteMessageSearchLoadingAndError 验证消息搜索在途显示「搜索中…」，且有命令命中时也展示错误行。
func TestUIReview20PaletteMessageSearchLoadingAndError(t *testing.T) {
	client := &uiReview20SearchClient{
		FakeClient: NewFakeClient(),
		started:    make(chan struct{}, 4),
		release:    make(chan struct{}),
		err:        errors.New("后端搜索失败"),
	}
	state := newChatState(client)
	t.Cleanup(state.close)

	tt := ui.NewTester(func(c *ui.Context) {
		state.init(c)
		state.applyWorkspaceUpdates()
		if c.Shortcut(ui.Cmd, ui.KeyK) {
			togglePalette(state)
		}
		renderCommandPalette(c, state)
	}, 1000, 700)

	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Type("zzzz_no_cmd_match")
	tt.Frame()

	if !tt.HasText("搜索中…") {
		t.Fatalf("搜索在途应显示「搜索中…」，实际：%v", tt.Texts())
	}
	if tt.HasText("没有匹配项") {
		t.Fatalf("搜索在途不应显示「没有匹配项」，实际：%v", tt.Texts())
	}

	select {
	case <-client.started:
	case <-time.After(3 * time.Second):
		t.Fatal("等待 SearchMessages 发起超时")
	}
	close(client.release)

	waitFor(t, tt, func() bool { return tt.HasText("搜索失败") })
	if !tt.HasText("后端搜索失败") {
		t.Fatalf("应显示具体错误信息，实际：%v", tt.Texts())
	}
	if tt.HasText("搜索中…") || tt.HasText("没有匹配项") {
		t.Fatalf("搜索结束后不应再显示加载中或没有匹配项：%v", tt.Texts())
	}

	// 有命令命中时，错误行仍应显示在列表末尾。
	closePalette(state)
	openPalette(state)
	tt.Frame()
	tt.Type("打开设置")
	waitFor(t, tt, func() bool { return tt.HasText("打开设置") && tt.HasText("后端搜索失败") })
}

// TestUIReview20PaletteMessageSearchContextDeadline 验证 SearchMessages 收到的 context 带 Deadline。
func TestUIReview20PaletteMessageSearchContextDeadline(t *testing.T) {
	client := &uiReview20SearchClient{
		FakeClient: NewFakeClient(),
		started:    make(chan struct{}, 1),
		release:    make(chan struct{}),
	}
	close(client.release)

	state := newChatState(client)
	t.Cleanup(state.close)
	p := getPaletteState(state)

	scheduleMessageSearch(state, p, "关键词")

	select {
	case <-client.started:
	case <-time.After(3 * time.Second):
		t.Fatal("等待 SearchMessages 调用超时")
	}

	client.mu.Lock()
	hasDeadline := client.hasDeadline
	client.mu.Unlock()
	if !hasDeadline {
		t.Fatal("SearchMessages 收到的 ctx 应带有 Deadline")
	}
}

// TestUIReview20PaletteHoverSelectsAndEnterRuns 验证悬停某行后同步选中下标，按 Enter 执行该行。
func TestUIReview20PaletteHoverSelectsAndEnterRuns(t *testing.T) {
	fake := NewFakeClient()
	fake.threads["p1"] = []Thread{
		{ID: "t1", ProjectID: "p1", Title: "悬停会话一"},
		{ID: "t2", ProjectID: "p1", Title: "悬停会话二"},
	}
	state := newChatState(fake)
	t.Cleanup(state.close)

	tt := ui.NewTester(func(c *ui.Context) {
		state.init(c)
		state.applyWorkspaceUpdates()
		if c.Shortcut(ui.Cmd, ui.KeyK) {
			togglePalette(state)
		}
		renderCommandPalette(c, state)
	}, 1000, 700)

	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return len(state.threads) == 2
	})

	tt.Key(ui.Cmd, ui.KeyK)
	tt.Type("悬停会话")
	tt.UseScreenReader()

	target, ok := tt.AccessByRole(ui.RoleButton, "悬停会话二")
	if !ok || target.Bounds.W <= 0 || target.Bounds.H <= 0 {
		t.Fatalf("未找到「悬停会话二」条目: ok=%v bounds=%+v", ok, target.Bounds)
	}

	tt.Move(target.Bounds.X+target.Bounds.W/2, target.Bounds.Y+target.Bounds.H/2)
	tt.Frame()

	p := getPaletteState(state)
	if p.selected != 1 {
		t.Fatalf("悬停第二行后 p.selected = %d，期望 1", p.selected)
	}

	tt.Key(0, ui.KeyEnter)
	if p.open {
		t.Fatal("按 Enter 后应关闭命令面板")
	}
	state.mu.Lock()
	selectedTID := state.selectedThreadID
	state.mu.Unlock()
	if selectedTID != "t2" {
		t.Fatalf("悬停后按 Enter 选中会话 = %q，期望 t2", selectedTID)
	}
}

// TestUIReview20OnboardingMagpieErrorStatus 验证 Magpie 有错误时状态为「连接失败」而非「检查中」，且脉冲停止。
func TestUIReview20OnboardingMagpieErrorStatus(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	st := NewSettingsState(settingsTestDir(t), nil, s)
	setChatSettings(s, st)

	st.mu.Lock()
	st.endpoints.err = "请求失败"
	st.mu.Unlock()

	if status, connected := onboardingMagpieStatus(st); status != "连接失败" || connected {
		t.Fatalf("endpoints.err 非空时状态 = (%q, %v)，期望 (\"连接失败\", false)", status, connected)
	}

	st.mu.Lock()
	st.endpoints.err = ""
	st.endpoints.loaded = true
	st.endpoints.endpoints = []CustomEndpoint{{ID: "gw", Magpie: true, Enabled: true, HasAPIKey: true}}
	st.piMagpie.err = "网关异常"
	st.mu.Unlock()

	if status, connected := onboardingMagpieStatus(st); status != "连接失败" || connected {
		t.Fatalf("piMagpie.err 非空时状态 = (%q, %v)，期望 (\"连接失败\", false)", status, connected)
	}

	setAppReduceMotion(t, false)
	tt := ui.NewTester(func(c *ui.Context) { onboardingMagpieRow(c, s, st, 0) }, 800, 700)
	tt.Frame()
	tt.Advance(2 * time.Second)
	tt.Frame()
	if !tt.HasText("连接失败") || tt.HasText("检查中") {
		t.Fatalf("Magpie 错误行应显示「连接失败」而非「检查中」：%v", tt.Texts())
	}
	if tt.FramePending() {
		t.Fatal("Magpie 连接失败时不应继续请求下一帧")
	}
	before := append([]byte(nil), tt.Image().Pix...)
	tt.Advance(motionSweep / 2)
	if !bytes.Equal(before, tt.Image().Pix) {
		t.Fatal("Magpie 连接失败时脉冲动画应已停止")
	}
}

// TestUIReview20CommandActivityWidthQuantized 验证进程 CPU 条宽度量化函数对非整数输入返回整像素值。
func TestUIReview20CommandActivityWidthQuantized(t *testing.T) {
	cases := []struct {
		rowWidth float32
		cpu      float64
	}{
		{123.4, 33.3},
		{99.7, 50.5},
		{211.3, 17.7},
		{100.0, 12.6},
	}
	for _, tc := range cases {
		got := commandActivityFillWidth(tc.rowWidth, tc.cpu)
		if got != float32(math.Round(float64(got))) {
			t.Errorf("commandActivityFillWidth(%v, %v) = %v，不是整数", tc.rowWidth, tc.cpu, got)
		}
	}
}
