package mcodeui

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

// TestComposerDockFloatsOverTimeline 验证 Docked 模式下底部卡片与输入框悬浮于时间线之上：
// (a) 时间线容器（与 message-list）的底边低于输入框顶边，即时间线延伸到悬浮区下方；
// (b) 列表滚到末尾后，最后一行的底边不低于 chat-bottom-dock 的顶边（完整露在悬浮区上方）；
// (c) 推进时钟后不再请求下一帧（静止不重绘）。
func TestComposerDockFloatsOverTimeline(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)

	fake := NewFakeClient()
	var msgs []Message
	for i := 0; i < 80; i++ {
		msgs = append(msgs, Message{
			ID:        fmt.Sprintf("u%02d", i),
			Role:      "user",
			Text:      fmt.Sprintf("消息-%02d", i),
			TurnIndex: i + 1,
			IsFinal:   true,
		})
	}
	fake.SetMessages("t1", msgs)

	state, view := buildRoot(fake, WithInitialThread("t1"))
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1200, 700)

	waitFor(t, tt, func() bool {
		state.mu.Lock()
		dockH := state.chatBottomDockH
		state.mu.Unlock()
		return dockH > 0 && state.chatListState.AtEnd() && tt.HasText("消息-79")
	})

	// 推进时钟等待入场过渡与迷你地图空闲变淡都落定。
	tt.Advance(3 * time.Second)
	tt.Frame()

	composerRect, ok := tt.Find("消息输入框")
	if !ok {
		t.Fatalf("找不到消息输入框")
	}
	lastMsgRect, ok := tt.Find("消息-79")
	if !ok {
		t.Fatalf("找不到最后一条消息 消息-79")
	}

	state.mu.Lock()
	timelineRect := state.chatTimelineBounds
	listRect := state.chatListBounds
	dockRect := state.chatBottomDockBounds
	state.mu.Unlock()

	if timelineRect.H <= 0 || listRect.H <= 0 || dockRect.H <= 0 {
		t.Fatalf("几何矩形未就绪: timeline=%+v list=%+v dock=%+v", timelineRect, listRect, dockRect)
	}

	// (a) 时间线容器与 message-list 底边低于输入框顶边，延伸到悬浮区下方。
	timelineBottom := timelineRect.Y + timelineRect.H
	listBottom := listRect.Y + listRect.H
	if timelineBottom <= composerRect.Y {
		t.Fatalf("timeline-container 底边 %.1f 应低于输入框顶边 %.1f", timelineBottom, composerRect.Y)
	}
	if listBottom <= composerRect.Y {
		t.Fatalf("message-list 底边 %.1f 应低于输入框顶边 %.1f", listBottom, composerRect.Y)
	}

	// (b) 列表滚到末尾后，最后一行的底边不低于 chat-bottom-dock 顶边（在屏幕坐标中 <= dockRect.Y）。
	lastMsgBottom := lastMsgRect.Y + lastMsgRect.H
	if lastMsgBottom > dockRect.Y {
		t.Fatalf("滚到末尾时最后一行底边 %.1f 应不低于 chat-bottom-dock 顶边 %.1f", lastMsgBottom, dockRect.Y)
	}

	// 验证悬浮区两侧空隙不拦截时间线滚轮（可穿透到底下 message-list），且「跳到最新」保持在悬浮区上方。
	if composerRect.X > dockRect.X+8 {
		tt.Scroll(dockRect.X+4, composerRect.Y+composerRect.H/2, 0, -1200)
		tt.Frame()
		if state.chatListState.AtEnd() {
			t.Fatalf("在悬浮区左侧空隙滚动应穿透到时间线并离开底部")
		}
		jumpRect, ok := tt.Find("跳到最新")
		if !ok {
			t.Fatalf("离开底部后应显示跳到最新按钮")
		}
		if jumpRect.Y+jumpRect.H > dockRect.Y {
			t.Fatalf("跳到最新按钮底边 %.1f 应位于悬浮区顶边 %.1f 上方", jumpRect.Y+jumpRect.H, dockRect.Y)
		}
		if err := tt.Click("跳到最新"); err != nil {
			t.Fatalf("点击跳到最新失败: %v", err)
		}
		waitFor(t, tt, func() bool { return state.chatListState.AtEnd() })
	}

	// (c) 推进时钟后不再请求下一帧（静止不重绘）。
	tt.Advance(3 * time.Second)
	if tt.FramePending() {
		t.Fatalf("静止后不应再请求下一帧")
	}
}

// TestComposerDockNoOpaqueMask 验证已去掉全宽遮罩：
// (b) 列表滚到非末尾位置时，至少有一条时间线行的纵向范围与 composer-card 相交；
// (c) 在输入框卡片左侧外、与卡片同高且有时间线文字经过的区域取样，像素不全为 ThemeSurfaceMain 底色。
func TestComposerDockNoOpaqueMask(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)

	fake := NewFakeClient()
	var msgs []Message
	for i := 0; i < 80; i++ {
		msgs = append(msgs, Message{
			ID:        fmt.Sprintf("a%02d", i),
			Role:      "assistant",
			Text:      fmt.Sprintf("消息-%02d 时间线穿行测试文本内容", i),
			TurnIndex: i + 1,
			IsFinal:   true,
		})
	}
	fake.SetMessages("t1", msgs)

	state, view := buildRoot(fake, WithInitialThread("t1"))
	t.Cleanup(state.close)
	// 用宽档正文使时间线正文延伸到 composer-card 两侧外，便于在卡片左侧取样验证无全宽遮罩。
	setChatSettings(state, &SettingsState{cfg: config.Config{Appearance: config.AppearanceConfig{ContentWidth: config.ContentWidthWide}}})
	t.Cleanup(func() { setChatSettings(state, nil) })

	var mainBg ui.Color
	tt := ui.NewTester(func(c *ui.Context) {
		view(c)
		mainBg = ThemeSurfaceMain(c)
	}, 1400, 700)

	waitFor(t, tt, func() bool {
		state.mu.Lock()
		dockH := state.chatBottomDockH
		state.mu.Unlock()
		return dockH > 0 && state.chatListState.AtEnd() && tt.HasText("消息-79")
	})

	tt.Advance(3 * time.Second)
	tt.Frame()

	state.mu.Lock()
	timelineRect := state.chatTimelineBounds
	state.mu.Unlock()
	if timelineRect.W <= 0 || timelineRect.H <= 0 {
		t.Fatalf("时间线几何矩形未就绪: %+v", timelineRect)
	}

	// 向上滚动离开底部，使时间线内容穿过悬浮区下方。
	tt.Scroll(timelineRect.X+timelineRect.W/2, timelineRect.Y+timelineRect.H/2, 0, -500)
	tt.Advance(500 * time.Millisecond)
	tt.Frame()
	if state.chatListState.AtEnd() {
		t.Fatalf("向上滚动后列表应离开末尾位置")
	}

	composerRect, ok := tt.Find("消息输入框")
	if !ok {
		t.Fatalf("找不到消息输入框")
	}

	// (b) 至少有一个时间线行的纵向范围与 composer-card 的纵向范围相交，且优先选取在卡片左侧有足够交叠的一行。
	var bestHit ui.Rect
	var bestOverlap float32
	for _, m := range msgs {
		r, ok := tt.Find(m.Text)
		if !ok {
			continue
		}
		overlap := min(r.Y+r.H, composerRect.Y+composerRect.H) - max(r.Y, composerRect.Y)
		if overlap > bestOverlap {
			bestOverlap = overlap
			bestHit = r
		}
	}
	if bestOverlap <= 0 {
		t.Fatalf("滚动到非末尾后应至少有一条时间线行与消息输入框纵向范围相交: composer=%+v", composerRect)
	}

	// (c) 在输入框卡片左侧外（避开阴影边缘）、与卡片同高且时间线文字经过处取样，应可见非 ThemeSurfaceMain 的文字像素。
	img := tt.Image()
	if img == nil {
		t.Fatalf("Tester.Image() 返回 nil，无法校验像素")
	}
	sampleX0 := int(bestHit.X)
	sampleX1 := min(int(bestHit.X+bestHit.W), int(composerRect.X)-32)
	sampleY0 := max(int(bestHit.Y), int(composerRect.Y))
	sampleY1 := min(int(bestHit.Y+bestHit.H), int(composerRect.Y+composerRect.H))
	if sampleX1 <= sampleX0 || sampleY1 <= sampleY0 {
		t.Fatalf("时间线行与输入框左侧外采样区为空: hit=%+v composer=%+v", bestHit, composerRect)
	}

	mainRGB := [3]int{int(mainBg.R), int(mainBg.G), int(mainBg.B)}
	nonBg := 0
	for y := sampleY0; y < sampleY1; y++ {
		for x := sampleX0; x < sampleX1; x++ {
			c := img.RGBAAt(x, y)
			if !colorNear([3]int{int(c.R), int(c.G), int(c.B)}, mainRGB, 8) {
				nonBg++
			}
		}
	}
	if nonBg == 0 {
		t.Fatalf("输入框左侧外 [%d,%d)x[%d,%d) 全为 ThemeSurfaceMain 底色 %v，时间线文字未穿出（疑似仍有遮罩）",
			sampleX0, sampleX1, sampleY0, sampleY1, mainRGB)
	}
}

// TestComposerDockChipsOpaque 验证 Docked 模式下悬浮区内的普通附件胶囊采用不透明底色，
// 渲染颜色等于 ThemeHover.Over(ThemeSurface) 且 alpha 为 255。
func TestComposerDockChipsOpaque(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)

	fake := NewFakeClient()
	var msgs []Message
	for i := 0; i < 80; i++ {
		msgs = append(msgs, Message{
			ID:        fmt.Sprintf("u%02d", i),
			Role:      "user",
			Text:      fmt.Sprintf("消息-%02d", i),
			TurnIndex: i + 1,
			IsFinal:   true,
		})
	}
	fake.SetMessages("t1", msgs)

	state, view := buildRoot(fake, WithInitialThread("t1"))
	t.Cleanup(state.close)

	var wantBg ui.Color
	tt := ui.NewTester(func(c *ui.Context) {
		view(c)
		wantBg = ThemeHover(c).Over(ThemeSurface(c))
	}, 1200, 700)

	waitFor(t, tt, func() bool {
		state.mu.Lock()
		dockH := state.chatBottomDockH
		state.mu.Unlock()
		return dockH > 0 && state.chatListState.AtEnd() && tt.HasText("消息-79")
	})

	state.mu.Lock()
	state.attachments = []composerAttachment{{
		LocalID:  "att-1",
		ID:       "att-1",
		Name:     "note.txt",
		MimeType: "text/plain",
	}}
	state.mu.Unlock()
	state.invalidate()

	waitFor(t, tt, func() bool {
		_, okText := tt.Find("note.txt")
		_, okRm := tt.Find("删除附件 note.txt")
		return okText && okRm
	})

	// 推进时钟等待入场过渡落定。
	tt.Advance(3 * time.Second)
	tt.Frame()

	textRect, ok := tt.Find("note.txt")
	if !ok {
		t.Fatalf("找不到附件文件名 note.txt")
	}
	rmRect, ok := tt.Find("删除附件 note.txt")
	if !ok {
		t.Fatalf("找不到删除附件按钮 删除附件 note.txt")
	}
	gap := rmRect.X - (textRect.X + textRect.W)
	if gap < 4 {
		t.Fatalf("文件名与删除按钮之间空隙不足以取样: text=%+v rm=%+v", textRect, rmRect)
	}
	sampleX := int(textRect.X + textRect.W + gap/2)
	sampleY := int(rmRect.Y + rmRect.H/2)

	img := tt.Image()
	if img == nil {
		t.Fatalf("Tester.Image() 返回 nil，无法校验像素")
	}
	if sampleX < 0 || sampleY < 0 || sampleX >= img.Rect.Dx() || sampleY >= img.Rect.Dy() {
		t.Fatalf("取样坐标 (%d,%d) 超出画布范围 %v", sampleX, sampleY, img.Rect)
	}
	if wantBg.A != 255 {
		t.Fatalf("ThemeHover.Over(ThemeSurface) alpha=%d，期望不透明 (255)", wantBg.A)
	}
	px := img.RGBAAt(sampleX, sampleY)
	if px.A != 255 {
		t.Fatalf("附件胶囊内部像素 alpha=%d，期望不透明 (255)", px.A)
	}
	wantRGB := [3]int{int(wantBg.R), int(wantBg.G), int(wantBg.B)}
	gotRGB := [3]int{int(px.R), int(px.G), int(px.B)}
	if !colorNear(gotRGB, wantRGB, 2) {
		t.Fatalf("附件胶囊内部像素 (%d,%d) 颜色=%v，期望 ThemeHover.Over(ThemeSurface)=%v",
			sampleX, sampleY, gotRGB, wantRGB)
	}
}
