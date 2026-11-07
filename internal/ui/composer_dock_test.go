package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// TestComposerThreadEmpty 验证「会话内容为空」的唯一口径：三者都为空才算 Hero。
func TestComposerThreadEmpty(t *testing.T) {
	cases := []struct {
		name      string
		messages  int
		plans     int
		questions int
		want      bool
	}{
		{"全空才是 Hero", 0, 0, 0, true},
		{"有消息", 1, 0, 0, false},
		{"只有已裁决计划", 0, 1, 0, false},
		{"只有已答复提问", 0, 0, 1, false},
		{"全有", 3, 2, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := composerThreadEmpty(tc.messages, tc.plans, tc.questions); got != tc.want {
				t.Fatalf("composerThreadEmpty(%d, %d, %d) = %v, want %v",
					tc.messages, tc.plans, tc.questions, got, tc.want)
			}
		})
	}
}

// TestDecideComposerDock 验证停靠判定：内嵌面板恒停靠不播位移，
// 内容非空直接停靠，只有「带透视标记且上一帧还没停靠」才是切换帧。
func TestDecideComposerDock(t *testing.T) {
	cases := []struct {
		name string
		in   composerDockInput
		want composerDockDecision
	}{
		{
			name: "空会话居中的 Hero",
			in:   composerDockInput{Empty: true},
			want: composerDockDecision{},
		},
		{
			name: "有内容的会话直接停靠",
			in:   composerDockInput{Empty: false},
			want: composerDockDecision{Docked: true},
		},
		{
			name: "发送首条消息：切换帧要播位移",
			in:   composerDockInput{Empty: true, PromoteArmed: true},
			want: composerDockDecision{Docked: true, Promote: true},
		},
		{
			name: "已经停靠过就不再算切换帧",
			in:   composerDockInput{Empty: true, PromoteArmed: true, PrevDocked: true},
			want: composerDockDecision{Docked: true},
		},
		{
			name: "浮层面板的会话恒停靠且不播位移",
			in:   composerDockInput{Empty: true, EmbeddedBee: true},
			want: composerDockDecision{Docked: true},
		},
		{
			name: "浮层面板里打着标记也不播位移",
			in:   composerDockInput{Empty: false, PromoteArmed: true, EmbeddedBee: true},
			want: composerDockDecision{Docked: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decideComposerDock(tc.in); got != tc.want {
				t.Fatalf("decideComposerDock(%+v) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

// TestComposerDockPromoteLifecycle 验证透视标记的生命周期：
// 发送首条消息时置位、消息推回后清除、换会话时清除、发送失败时清除。
func TestComposerDockPromoteLifecycle(t *testing.T) {
	s := newChatState(NewFakeClient())

	// 发送首条消息：打标后该会话就按停靠布局走。
	s.armComposerDockPromote("t1")
	if !s.composerDockPromoArmed("t1") {
		t.Fatalf("发出首条消息后应当带透视标记")
	}
	if s.composerDockPromoArmed("t2") {
		t.Fatalf("标记只属于发出消息的那个会话")
	}

	// 消息推回（会话不再为空）：撤标记，之后靠真实内容停靠。
	s.disarmComposerDockPromote("t1")
	if s.composerDockPromoArmed("t1") {
		t.Fatalf("消息推回后应当撤掉透视标记")
	}

	// 切到别的会话：标记不影响新会话。
	s.armComposerDockPromote("t1")
	// selectThread 用读到的旧 selectedThreadID 撤标，先把当前会话设成 t1，撤的才是 t1 的标记。
	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.mu.Unlock()
	s.selectThread("t2")
	if s.composerDockPromoArmed("t1") {
		t.Fatalf("切走会话时应当撤掉旧会话的透视标记")
	}

	// 发送失败：回退到居中，不留停靠但为空的状态。
	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.mu.Unlock()
	s.armComposerDockPromote("t1")
	s.mu.Lock()
	s.finishOutgoingMessageLocked("t1", "你好", nil, false, errTestSendFailed{}, "发送消息失败")
	s.mu.Unlock()
	if s.composerDockPromoArmed("t1") {
		t.Fatalf("发送失败后应当撤掉透视标记，退回居中布局")
	}
}

// errTestSendFailed 是发送失败路径用的假错误。
type errTestSendFailed struct{}

func (errTestSendFailed) Error() string { return "发送失败" }

// composerToggleY 取输入框里「构建」胶囊的窗口坐标 y：它在输入框卡片内，
// 跟着卡片一起移动，用它测量输入框的位移。
func composerToggleY(tt *ui.Tester) (float32, bool) {
	r, ok := tt.Find("构建")
	return r.Y, ok
}

// setTestThreadMessages 直接替换当前会话的消息并作废时间线缓存：
// 模拟「打开一个已有消息的会话」（不打透视标记）。
func setTestThreadMessages(s *chatState, msgs []Message) {
	s.mu.Lock()
	s.messages = msgs
	s.messagesRev++
	s.mu.Unlock()
}

// testDockMessages 是一对最小的问答消息，足以让会话进入停靠布局。
func testDockMessages() []Message {
	return []Message{
		{ID: "u1", Role: "user", Kind: "text", Text: "你好", TurnIndex: 1},
		{ID: "a1", Role: "assistant", Kind: "text", Text: "你好，有什么可以帮你的？", TurnIndex: 1},
	}
}

// TestComposerHeroToDockSlidesDown 验证 Hero→Docked 的滑动：
// 空会话时输入框居中；发送首条消息当帧切到停靠布局，y 经过若干中间值才到底部；
// 而「切到已有消息的会话」第一帧就在终点，没有中间帧。
func TestComposerHeroToDockSlidesDown(t *testing.T) {
	var s *chatState
	tt := ui.NewTester(Root(NewFakeClient(), func(cs *chatState) { s = cs }), 1000, 700)
	waitFor(t, tt, func() bool { return s != nil && tt.HasText("测试会话1") })
	if err := tt.Click("测试会话1"); err != nil {
		t.Fatalf("打开空会话失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("构建") })
	// Hero 静置：等进场动画结束再记起点。
	tt.Advance(600 * time.Millisecond)
	heroY, ok := composerToggleY(tt)
	if !ok {
		t.Fatalf("Hero 布局里找不到输入框的构建胶囊，当前文本：%v", tt.Texts())
	}

	// 参照终点：同一会话有消息时输入框的停靠位置。
	setTestThreadMessages(s, testDockMessages())
	tt.Frame()
	// 等淡入与排版稳定后再取参照值。
	tt.Advance(600 * time.Millisecond)
	dockedY, ok := composerToggleY(tt)
	if !ok {
		t.Fatalf("停靠布局里找不到输入框的构建胶囊，当前文本：%v", tt.Texts())
	}
	if dockedY <= heroY+40 {
		t.Fatalf("停靠位应明显在居中位下方：hero=%.1f docked=%.1f", heroY, dockedY)
	}

	// 回到空会话：反向翻转是瞬时的，不播位移；只剩容器底距 10pt 的停靠间距动画（AnimateWith("composer-dock-pad")），
	// 所以第一帧允许差这 10pt，超过就不是瞬时落位而是长距离滑动了。
	setTestThreadMessages(s, nil)
	tt.Frame()
	backY, ok := composerToggleY(tt)
	if !ok {
		t.Fatalf("回到 Hero 后找不到输入框的构建胶囊")
	}
	if backY < heroY-(composerDockPadBottom+1) || backY > heroY+(composerDockPadBottom+1) {
		t.Fatalf("回到居中的 Hero 不应长距离滑动：期望约 %.1f（允许 ±%.0f），实际 %.1f", heroY, composerDockPadBottom+1, backY)
	}
	tt.Advance(300 * time.Millisecond)
	if backY, ok = composerToggleY(tt); !ok {
		t.Fatalf("回到 Hero 后找不到输入框的构建胶囊")
	}
	if backY < heroY-1 || backY > heroY+1 {
		t.Fatalf("回到居中的 Hero 应当落位到起点：期望约 %.1f，实际 %.1f", heroY, backY)
	}

	// 切到已有消息的会话（这里表现为会话突然有消息）：第一帧就差到停靠间距以内，没有长距离位移。
	setTestThreadMessages(s, testDockMessages())
	tt.Frame()
	firstY, ok := composerToggleY(tt)
	if !ok {
		t.Fatalf("停靠布局里找不到输入框的构建胶囊")
	}
	if firstY < dockedY-(composerDockPadBottom+1) || firstY > dockedY+(composerDockPadBottom+1) {
		t.Fatalf("有消息的会话不应播位移：第一帧就应在停靠位 %.1f 附近，实际 %.1f", dockedY, firstY)
	}
	tt.Advance(300 * time.Millisecond)
	if firstY, ok = composerToggleY(tt); !ok {
		t.Fatalf("停靠布局里找不到输入框的构建胶囊")
	}
	if firstY < dockedY-1 || firstY > dockedY+1 {
		t.Fatalf("有消息的会话应当落位到停靠位：期望约 %.1f，实际 %.1f", dockedY, firstY)
	}

	// 回到空会话，准备测发送首条消息时的滑动。
	setTestThreadMessages(s, nil)
	tt.Frame()
	if y, ok := composerToggleY(tt); !ok || y < heroY-(composerDockPadBottom+1) || y > heroY+(composerDockPadBottom+1) {
		t.Fatalf("回到 Hero 不应长距离滑动：期望约 %.1f（允许 ±%.0f），实际 %.1f", heroY, composerDockPadBottom+1, y)
	}
	tt.Advance(300 * time.Millisecond)
	if y, ok := composerToggleY(tt); !ok || y < heroY-1 || y > heroY+1 {
		t.Fatalf("回到 Hero 应当落位到起点：期望约 %.1f，实际 %.1f", heroY, y)
	}

	// 发送首条消息：发送当帧就切到停靠布局（打标 → promote）。
	s.mu.Lock()
	s.armComposerDockPromote("t1")
	s.mu.Unlock()
	tt.Frame()
	if y, ok := composerToggleY(tt); !ok || y < heroY-1 || y > heroY+1 {
		t.Fatalf("切换当帧应当还在起点附近（动画刚起步）：期望约 %.1f，实际 %.1f", heroY, y)
	}

	// 逐帧采样：必须出现严格位于起点和终点之间的中间帧，否则证明不了有位移。
	var mids []float32
	sawDocked := false
	for i := 0; i < 40; i++ {
		tt.Advance(20 * time.Millisecond)
		y, ok := composerToggleY(tt)
		if !ok {
			t.Fatalf("位移过程中丢失了输入框的构建胶囊")
		}
		if y > heroY+4 && y < dockedY-4 {
			mids = append(mids, y)
		}
		if y >= dockedY-1 {
			sawDocked = true
			break
		}
	}
	if len(mids) == 0 {
		t.Fatalf("没有测到起点与终点之间的中间帧，说明输入框是瞬移而非滑动：hero=%.1f docked=%.1f", heroY, dockedY)
	}
	// 中间帧要落在起点和终点连成的线段上，且逐帧递增（弹簧不回弹）。
	for i, y := range mids {
		if i > 0 && y < mids[i-1]-1 {
			t.Fatalf("位移应当单调向下，第 %d 个中间帧 %.1f 比上一帧 %.1f 更靠上", i, y, mids[i-1])
		}
	}
	if !sawDocked {
		tt.Advance(800 * time.Millisecond)
		finalY, ok := composerToggleY(tt)
		if !ok {
			t.Fatalf("动画结束后找不到输入框的构建胶囊")
		}
		if finalY < dockedY-1 || finalY > dockedY+1 {
			t.Fatalf("动画结束应停在停靠位：期望约 %.1f，实际 %.1f", dockedY, finalY)
		}
	}
}
