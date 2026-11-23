package mcodeui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

type hangingRevertPreviewClient struct {
	*FakeClient
}

func (h *hangingRevertPreviewClient) PreviewRevert(ctx context.Context, threadID, turnID string) (*RevertPreview, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestUIReview28RevertDialogConcurrentCloseAndRender 验证撤销弹窗后台关闭与渲染并发执行时无数据竞争。
func TestUIReview28RevertDialogConcurrentCloseAndRender(t *testing.T) {
	t.Cleanup(func() { modalDragReset(modalDragRevert) })

	fake := NewFakeClient()
	s := newChatState(fake)
	t.Cleanup(s.close)
	st := getRevertDialogState(s)

	tt := ui.NewTesterWith(func(c *ui.Context) {
		applyAppTheme(c)
		renderRevertDialog(c, s)
	}, 800, 600, ui.TesterOptions{
		Preferences: ui.Preferences{ReduceMotion: true},
	})

	for i := 0; i < 20; i++ {
		st.mu.Lock()
		st.open = true
		st.threadID = "thread-1"
		st.turnID = "turn-1"
		st.loading = false
		st.executing = false
		st.err = ""
		st.preview = &RevertPreview{
			TurnID: "turn-1",
			Files:  []RevertFile{{Path: "a.go", Additions: 1}},
		}
		st.mu.Unlock()

		s.confirmRevert()
		for j := 0; j < 5; j++ {
			tt.Frame()
		}

		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			st.mu.Lock()
			done := !st.open && !st.executing
			st.mu.Unlock()
			if done {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}
}

// TestUIReview28RevertPreviewTimeout 验证预览请求超时后弹窗离开加载态并显示错误。
func TestUIReview28RevertPreviewTimeout(t *testing.T) {
	t.Cleanup(func() { modalDragReset(modalDragRevert) })

	oldTimeout := revertPreviewTimeout
	revertPreviewTimeout = 20 * time.Millisecond
	t.Cleanup(func() { revertPreviewTimeout = oldTimeout })

	s := newChatState(&hangingRevertPreviewClient{FakeClient: NewFakeClient()})
	t.Cleanup(s.close)
	st := getRevertDialogState(s)

	tt := ui.NewTesterWith(func(c *ui.Context) {
		applyAppTheme(c)
		renderRevertDialog(c, s)
	}, 800, 600, ui.TesterOptions{
		Preferences: ui.Preferences{ReduceMotion: true},
	})

	s.openRevertPreview("thread-1", "turn-1")
	tt.Frame()
	if !tt.HasText("正在预览改动…") {
		t.Fatalf("刚打开预览时应处于加载态: %v", tt.Texts())
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tt.Frame()
		st.mu.Lock()
		loading := st.loading
		errMsg := st.err
		st.mu.Unlock()
		if !loading && errMsg != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	tt.Frame()
	if tt.HasText("正在预览改动…") {
		t.Fatalf("超时后应离开加载态: %v", tt.Texts())
	}
	if !tt.HasText("关闭") {
		t.Fatalf("超时出错后应显示关闭按钮: %v", tt.Texts())
	}
	joined := strings.Join(tt.Texts(), "\n")
	if !strings.Contains(joined, "加载撤销预览失败") {
		t.Fatalf("超时后应显示加载失败错误文案: %q", joined)
	}
}

// TestUIReview28LongPathTruncationAndButtonBounds 验证窄容器下超长路径截断且右侧状态与按钮仍在容器内。
func TestUIReview28LongPathTruncationAndButtonBounds(t *testing.T) {
	t.Cleanup(func() { modalDragReset(modalDragRevert) })

	// 400 宽窗口减去 40 留白，撤销弹窗卡片实际宽度为 360（居于 [20, 380]）。
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	rst := getRevertDialogState(s)
	rst.mu.Lock()
	rst.open = true
	rst.preview = &RevertPreview{
		TurnID: "turn-1",
		Files: []RevertFile{
			{
				Path:      "/repo/" + strings.Repeat("nested_module_directory/", 10) + "target.go",
				Additions: 12,
				Deletions: 3,
				IsKept:    true,
			},
			{
				Path:      "main.go",
				Additions: 2,
			},
		},
	}
	rst.mu.Unlock()

	revertTester := ui.NewTesterWith(func(c *ui.Context) {
		applyAppTheme(c)
		renderRevertDialog(c, s)
	}, 400, 560, ui.TesterOptions{
		Preferences: ui.Preferences{ReduceMotion: true},
	})

	if revertTester.HasText("已保留（轮次结束后被修改过）") {
		t.Fatalf("不应再显示旧的冗长保留文案: %v", revertTester.Texts())
	}
	keptRect, ok := revertTester.Find("已保留")
	if !ok {
		t.Fatalf("应显示简短文案“已保留”: %v", revertTester.Texts())
	}
	addRect, ok := revertTester.Find("+12")
	if !ok {
		t.Fatalf("应显示新增行数 +12: %v", revertTester.Texts())
	}
	delRect, ok := revertTester.Find("-3")
	if !ok {
		t.Fatalf("应显示删除行数 -3: %v", revertTester.Texts())
	}
	confirmRect, ok := revertTester.Find("还原")
	if !ok {
		t.Fatalf("应显示还原按钮: %v", revertTester.Texts())
	}

	const cardLeft, cardRight float32 = 20, 380
	for name, r := range map[string]ui.Rect{
		"已保留": keptRect,
		"+12": addRect,
		"-3":  delRect,
		"还原":  confirmRect,
	} {
		if r.W <= 0 || r.X < cardLeft || r.X+r.W > cardRight {
			t.Fatalf("%s 应留在 360 宽撤销弹窗卡片 [%v, %v] 内, got %+v", name, cardLeft, cardRight, r)
		}
	}
}

// TestUIReview28_RevertDialogClosesByUser 验证撤销弹窗打开后可通过 Esc 与点击“取消”按钮关闭。
func TestUIReview28_RevertDialogClosesByUser(t *testing.T) {
	t.Cleanup(func() { modalDragReset(modalDragRevert) })

	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	st := getRevertDialogState(s)

	openDialog := func() {
		st.mu.Lock()
		st.open = true
		st.loading = false
		st.executing = false
		st.err = ""
		st.preview = &RevertPreview{
			TurnID: "turn-1",
			Files:  []RevertFile{{Path: "a.go", Additions: 1}},
		}
		st.mu.Unlock()
	}

	openDialog()
	tt := ui.NewTesterWith(func(c *ui.Context) {
		applyAppTheme(c)
		renderRevertDialog(c, s)
	}, 800, 600, ui.TesterOptions{
		Preferences: ui.Preferences{ReduceMotion: true},
	})

	if !tt.HasText("撤销此轮改动") {
		t.Fatalf("打开撤销弹窗后应显示标题: %v", tt.Texts())
	}

	tt.Key(0, ui.KeyEscape)
	tt.Frame()

	st.mu.Lock()
	openAfterEsc := st.open
	st.mu.Unlock()
	if openAfterEsc {
		t.Fatalf("按 Esc 后 st.open 应变为 false")
	}
	if tt.HasText("撤销此轮改动") || tt.HasText("取消") {
		t.Fatalf("按 Esc 后弹窗元素应消失: %v", tt.Texts())
	}

	openDialog()
	tt.Frame()
	if !tt.HasText("撤销此轮改动") || !tt.HasText("取消") {
		t.Fatalf("重新打开撤销弹窗后应显示标题与取消按钮: %v", tt.Texts())
	}

	if err := tt.Click("取消"); err != nil {
		t.Fatalf("点击取消按钮失败: %v", err)
	}
	tt.Frame()

	st.mu.Lock()
	openAfterCancel := st.open
	st.mu.Unlock()
	if openAfterCancel {
		t.Fatalf("点击取消按钮后 st.open 应变为 false")
	}
	if tt.HasText("撤销此轮改动") || tt.HasText("取消") {
		t.Fatalf("点击取消按钮后弹窗元素应消失: %v", tt.Texts())
	}
}
