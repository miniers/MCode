package mcodeui

import (
	"bytes"
	"image"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// hoverRow 把指针移到 title 所在会话行的左侧，并渲染一帧。
func hoverRow(t *testing.T, tt *ui.Tester, title string) {
	t.Helper()
	rect, ok := tt.Find(title)
	if !ok {
		t.Fatalf("expected to find thread title %q, texts: %q", title, tt.Texts())
	}
	tt.Move(rect.X+8, rect.Y+rect.H/2)
	tt.Frame()
}

// cloneImage 复制当前帧像素：Tester.Image 复用的是同一块缓冲。
func cloneImage(src *image.RGBA) *image.RGBA {
	dst := image.NewRGBA(src.Bounds())
	copy(dst.Pix, src.Pix)
	return dst
}

// bandDiffers 比较两帧在矩形区域内的像素是否不同（区域先做边界裁剪）。
func bandDiffers(a, b *image.RGBA, r image.Rectangle) bool {
	r = r.Intersect(a.Bounds()).Intersect(b.Bounds())
	if r.Empty() {
		return false
	}
	row := r.Dx() * 4
	for y := r.Min.Y; y < r.Max.Y; y++ {
		ao, bo := a.PixOffset(r.Min.X, y), b.PixOffset(r.Min.X, y)
		if !bytes.Equal(a.Pix[ao:ao+row], b.Pix[bo:bo+row]) {
			return true
		}
	}
	return false
}

// titleBand 返回标题所占的横向条带：从侧边栏左缘到标题被裁剪后的右缘。
func titleBand(tt *ui.Tester, title string) (image.Rectangle, bool) {
	rect, ok := tt.Find(title)
	if !ok {
		return image.Rectangle{}, false
	}
	b := tt.Image().Bounds()
	return image.Rect(b.Min.X, int(rect.Y), int(rect.X+rect.W), int(rect.Y+rect.H)), true
}

// TestSidebarHoverCardAppearsAfterDelay 验证悬停卡：不足 650ms 预热不出现，
// 预热结束后出现并带上标题、项目、模型、状态、最近活动等字段，移开后消失。
func TestSidebarHoverCardAppearsAfterDelay(t *testing.T) {
	resetSidebarHoverCardForTest()
	fake := NewFakeClient()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/hovercard"}},
		map[string][]Thread{"p1": {{
			ID:        "t1",
			ProjectID: "p1",
			Title:     "悬停卡会话",
			Provider:  "claude",
			Model:     "claude-3-7-sonnet",
			UpdatedAt: time.Now().Add(-2 * time.Hour),
		}}},
	)
	_, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("悬停卡会话") })

	hoverRow(t, tt, "悬停卡会话")

	// 预热时长之内不出现卡片。
	tt.Advance(600 * time.Millisecond)
	tt.Frame()
	if tt.HasText("最近活动") || tt.HasText("Claude · claude-3-7-sonnet") {
		t.Fatalf("hover card should not show before the warmup, texts: %q", tt.Texts())
	}

	// 越过 650ms 预热后出现，内容来自会话已有字段。
	tt.Advance(100 * time.Millisecond)
	tt.Frame()
	if !tt.HasText("最近活动") {
		t.Fatalf("expected hover card after the warmup, texts: %q", tt.Texts())
	}
	for _, want := range []string{"悬停卡会话", "hovercard", "Claude · claude-3-7-sonnet", "空闲", "2小时前", "模型"} {
		if !tt.HasText(want) {
			t.Fatalf("expected hover card to contain %q, texts: %q", want, tt.Texts())
		}
	}
	if tt.HasText("待审批") {
		t.Fatalf("idle thread should not show 待审批")
	}

	// 指针移开后卡片消失（宽限期 180ms 之后）。
	tt.Move(900, 600)
	tt.Frame()
	if !tt.HasText("最近活动") {
		t.Fatalf("hover card should stay during the grace period, texts: %q", tt.Texts())
	}
	tt.Advance(250 * time.Millisecond)
	tt.Frame()
	if tt.HasText("最近活动") {
		t.Fatalf("hover card should be gone after leaving, texts: %q", tt.Texts())
	}
}

// TestSidebarCompactTitleMarquee 验证紧凑模式下超长标题悬停后横向滚动、
// 短标题不滚动，且未悬停时不挂动画。
func TestSidebarCompactTitleMarquee(t *testing.T) {
	longTitle := strings.Repeat("紧凑模式超长会话标题", 4)
	shortTitle := "短标题"

	build := func(t *testing.T, title string) (*ui.Tester, *chatState) {
		resetSidebarHoverCardForTest()
		fake := NewFakeClient()
		fake.threads["p1"] = []Thread{{
			ID:        "t1",
			ProjectID: "p1",
			Title:     title,
			UpdatedAt: time.Now(),
		}}
		state, view := buildRoot(fake)
		st := NewSettingsState(settingsTestDir(t), nil, state)
		st.cfg.Appearance.SidebarCompact = true
		setChatSettings(state, st)
		tt := ui.NewTester(view, 1000, 700)
		waitFor(t, tt, func() bool { return tt.HasText(title) })
		return tt, state
	}

	t.Run("超长标题悬停后滚动", func(t *testing.T) {
		tt, _ := build(t, longTitle)

		bandBefore, ok := titleBand(tt, longTitle)
		if !ok {
			t.Fatalf("expected to find the title, texts: %q", tt.Texts())
		}
		// 未悬停：同一帧重复渲染像素不变，说明没有常驻动画。
		first := cloneImage(tt.Image())
		tt.Advance(800 * time.Millisecond)
		tt.Frame()
		if bandDiffers(first, tt.Image(), bandBefore) {
			t.Fatalf("title should not scroll while the pointer is outside the row")
		}

		hoverRow(t, tt, longTitle)
		tt.Advance(time.Second)
		tt.Frame()
		band, ok := titleBand(tt, longTitle)
		if !ok {
			t.Fatalf("expected to find the title after hover, texts: %q", tt.Texts())
		}
		first = cloneImage(tt.Image())
		tt.Advance(800 * time.Millisecond)
		tt.Frame()
		if !bandDiffers(first, tt.Image(), band) {
			t.Fatalf("expected the long title to scroll on hover")
		}
	})

	t.Run("短标题不滚动", func(t *testing.T) {
		tt, _ := build(t, shortTitle)
		hoverRow(t, tt, shortTitle)
		tt.Advance(time.Second)
		tt.Frame()
		band, ok := titleBand(tt, shortTitle)
		if !ok {
			t.Fatalf("expected to find the title, texts: %q", tt.Texts())
		}
		first := cloneImage(tt.Image())
		tt.Advance(800 * time.Millisecond)
		tt.Frame()
		if bandDiffers(first, tt.Image(), band) {
			t.Fatalf("short title should not scroll")
		}
	})
}

// TestSidebarCompactTitleEllipsis 验证紧凑标题与框架的省略号文本渲染一致。
func TestSidebarCompactTitleEllipsis(t *testing.T) {
	for _, tc := range []struct {
		name  string
		title string
	}{
		{"长标题", strings.Repeat("很长的会话标题", 8)},
		{"短标题", "短标题"},
		{"无空格英文", strings.Repeat("abcdefgh", 20)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actual := ui.NewTester(func(c *ui.Context) {
				ui.Row(c).Width(100).Children(func() {
					renderSidebarScrollingTitle(c, "title", tc.title, false, 0)
				})
			}, 120, 40)
			expected := ui.NewTester(func(c *ui.Context) {
				ui.Row(c).Width(100).Children(func() {
					ui.Row(c.Key("title")).Grow(1).Shrink(1).MinWidth(0).ClipX().Children(func() {
						ui.Text(c, tc.title).SingleLine().Grow(1).Shrink(1).MinWidth(0).Ellipsis("…")
					})
				})
			}, 120, 40)
			actual.Frame()
			expected.Frame()
			if !bytes.Equal(actual.Image().Pix, expected.Image().Pix) {
				t.Fatal("标题渲染与省略号文本不一致")
			}
		})
	}
}

// TestSidebarHoverCardFullTitle 验证悬停卡标题可显示超过两行。
func TestSidebarHoverCardFullTitle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	state, _ := buildRoot(NewFakeClient())
	title := strings.Repeat("这是可以换行的完整会话标题 ", 12)
	th := Thread{ID: "title-test", Title: title}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(sidebarHoverCardWidth).Children(func() {
			renderSidebarHoverCardBody(c, state, th)
		})
	}, 400, 900)
	tt.Frame()
	rect, ok := tt.Find(title)
	if !ok || rect.H <= scaledFont(FontSizeThreadTitle)*2 {
		t.Fatalf("完整标题应超过两行，尺寸: %+v，文本: %q", rect, tt.Texts())
	}
}

// TestSidebarMarqueeDecision 验证跑马灯的判定：未悬停不滚、溢出不足 8px 不滚、
// 溢出足够且悬停才滚，尺寸还没量到时也不滚。
func TestSidebarMarqueeDecision(t *testing.T) {
	cases := []struct {
		name     string
		natural  float32
		avail    float32
		reserved float32
		hovered  bool
		want     bool
	}{
		{"未悬停", 200, 100, 0, false, false},
		{"溢出不足 8px", 107, 100, 0, true, false},
		{"刚好超过 8px", 109, 100, 0, true, true},
		{"溢出很多", 400, 100, 0, true, true},
		{"还没量到尺寸", 0, 0, 0, true, false},
		// 预留宽度（行尾状态位/操作按钮）会吞掉可用宽度：100-60=40，150 溢出 110，应滚。
		{"扣掉预留宽度后溢出", 150, 100, 60, true, true},
		// 同一对尺寸：有预留时溢出 30px 应滚，无预留时正好装得下不滚。
		{"加上预留后才溢出", 120, 120, 30, true, true},
		{"无预留时不溢出", 120, 120, 0, true, false},
	}
	for _, tc := range cases {
		if got := sidebarMarqueeActive(tc.natural, tc.avail, tc.reserved, tc.hovered); got != tc.want {
			t.Fatalf("%s: sidebarMarqueeActive(%v, %v, %v, %v) = %v, want %v",
				tc.name, tc.natural, tc.avail, tc.reserved, tc.hovered, got, tc.want)
		}
	}
}

// TestSidebarHoverCardNoShowWithoutHover 回归测试：没有任何鼠标移动时，即使包级状态里
// 残留着「同一会话行刚离开」的记录（上一个窗口/上一个测试留下的），新窗口翻页推进 2 秒
// 也不能把悬停卡显示出来——卡片只在真实悬停满预热后出现。
func TestSidebarHoverCardNoShowWithoutHover(t *testing.T) {
	resetSidebarHoverCardForTest()
	// 伪造上一个窗口的残留状态：threadID 与下面构造的会话相同，离开时刻就在刚刚。
	seedSidebarHoverLeftForTest("t1", "p1", time.Now())

	fake := NewFakeClient()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/hovercard"}},
		map[string][]Thread{"p1": {{
			ID:        "t1",
			ProjectID: "p1",
			Title:     "未悬停会话",
			Provider:  "claude",
			Model:     "claude-3-7-sonnet",
			UpdatedAt: time.Now().Add(-2 * time.Hour),
		}}},
	)
	_, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("未悬停会话") })

	// 不移动鼠标，只推进时间：卡片内容不得出现。
	// 判据只能用只在悬停卡里出现的字段文案；「2小时前」这类相对时间也会作为会话行行尾状态位显示。
	tt.Advance(2 * time.Second)
	tt.Frame()
	for _, unwanted := range []string{"最近活动", "状态"} {
		if tt.HasText(unwanted) {
			t.Fatalf("hover card content %q should not appear without a real hover, texts: %q", unwanted, tt.Texts())
		}
	}
}

// seedSidebarHoverLeftForTest 写入「某个会话行刚刚失去悬停」的包级状态，
// 用来模拟上一个窗口残留的悬停记录（新窗口必须无视它）。
func seedSidebarHoverLeftForTest(threadID, projectID string, leftAt time.Time) {
	sidebarHover.mu.Lock()
	sidebarHover.threadID = threadID
	sidebarHover.projectID = projectID
	sidebarHover.enteredAt = leftAt.Add(-time.Second)
	sidebarHover.leftAt = leftAt
	sidebarHover.mu.Unlock()
}

// TestSidebarDeleteThreadDialogCallbacks 验证迁移后的删除会话确认弹窗：
// 文案保持原样，取消只关弹窗，确认走原有删除流程。
func TestSidebarDeleteThreadDialogCallbacks(t *testing.T) {
	fake := NewFakeClient()
	state, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })

	state.openDeleteModal("t1", "测试会话1")
	tt.Frame()
	if !state.deleteModalOpen {
		t.Fatalf("expected deleteModalOpen to be true")
	}
	if !tt.HasText("删除后无法恢复，确定要删除会话 \"测试会话1\" 吗？") {
		t.Fatalf("expected the original delete warning, texts: %q", tt.Texts())
	}
	if !tt.HasText("删除会话") {
		t.Fatalf("expected the delete dialog title, texts: %q", tt.Texts())
	}

	// 取消：关闭弹窗，会话仍在。
	if err := tt.Click("取消"); err != nil {
		t.Fatalf("click 取消 failed: %v", err)
	}
	tt.Frame()
	state.mu.Lock()
	open := state.deleteModalOpen
	state.mu.Unlock()
	if open {
		t.Fatalf("expected deleteModalOpen to be false after cancel")
	}
	if !tt.HasText("测试会话1") {
		t.Fatalf("cancel should not delete the thread, texts: %q", tt.Texts())
	}

	// 确认：走原有删除流程，会话从侧边栏消失。
	state.openDeleteModal("t1", "测试会话1")
	tt.Frame()
	if err := tt.Click("删除"); err != nil {
		t.Fatalf("click 删除 failed: %v", err)
	}
	waitFor(t, tt, func() bool { return !tt.HasText("测试会话1") })
}

// TestSidebarRemoveProjectDialogCallbacks 验证迁移后的移除项目确认弹窗：
// 文案保持原样，取消只关弹窗，确认走原有移除流程。
func TestSidebarRemoveProjectDialogCallbacks(t *testing.T) {
	fake := NewFakeClient()
	state, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("mcode-test") })

	openRemove := func() {
		state.mu.Lock()
		state.removeProjModalOpen = true
		state.removeProjID = "p1"
		state.removeProjName = "mcode-test"
		state.mu.Unlock()
		state.invalidate()
		tt.Frame()
	}

	openRemove()
	if !tt.HasText("确定移除项目 \"mcode-test\" 吗？该项目的会话记录会一并从 mcode 中删除，磁盘上的文件不会被删除。") {
		t.Fatalf("expected the original remove warning, texts: %q", tt.Texts())
	}
	if !tt.HasText("移除项目") {
		t.Fatalf("expected the remove dialog title, texts: %q", tt.Texts())
	}

	// 取消：关闭弹窗，项目仍在。
	if err := tt.Click("取消"); err != nil {
		t.Fatalf("click 取消 failed: %v", err)
	}
	tt.Frame()
	state.mu.Lock()
	open := state.removeProjModalOpen
	state.mu.Unlock()
	if open {
		t.Fatalf("expected removeProjModalOpen to be false after cancel")
	}
	if !tt.HasText("mcode-test") {
		t.Fatalf("cancel should not remove the project, texts: %q", tt.Texts())
	}

	// 确认：走原有移除流程，项目从侧边栏消失。
	openRemove()
	if err := tt.Click("移除"); err != nil {
		t.Fatalf("click 移除 failed: %v", err)
	}
	waitFor(t, tt, func() bool { return !tt.HasText("mcode-test") })
}
