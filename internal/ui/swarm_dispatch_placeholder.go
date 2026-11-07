package mcodeui

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
)

// 派发占位胶囊：主代理正在流式输出派单块（三引号加 swarm 开头），或者派单块已经闭合、子代理却还没出现在
// bees 里（还在排队、还没启动）时，在子线程胶囊行之前显示一枚不可点击的占位胶囊，
// 让这两段时间的时间线末尾不是空白。真实胶囊出现后占位自然消失。

// swarmDispatchRowID 是派发占位行在时间线里的固定 Key。
const swarmDispatchRowID = "swarm-dispatch-placeholder"

// swarmDispatchPulsePeriod 是状态点呼吸动画一轮的时长：透明度在 0.35 与 1 之间往返循环。
const swarmDispatchPulsePeriod = 1400 * time.Millisecond

// swarmDispatchPulseSteps 把一个呼吸周期量化成几档相位：每档请求一次重绘，取代按屏幕刷新率跑的动画帧。
const swarmDispatchPulseSteps = 8

// swarmDispatchPulseAlpha 按帧时间算出状态点这一帧的透明度：周期为 swarmDispatchPulsePeriod 的三角波，
// 在 0.35 与 1 之间往返，相位量化成 swarmDispatchPulseSteps 档，配合 renderSwarmDispatchPlaceholder
// 里的低频定时重绘，得到与原先呼吸动画视觉接近、但重建次数低一个数量级的效果。
func swarmDispatchPulseAlpha(now time.Time) float64 {
	const low, high = 0.35, 1.0
	step := int64(now.UnixNano() / int64(swarmDispatchPulsePeriod/swarmDispatchPulseSteps))
	phase := float64(step%swarmDispatchPulseSteps) / float64(swarmDispatchPulseSteps)
	// 折叠成 0→1→0 的三角波：前半周期上升，后半周期下降。
	if phase > 0.5 {
		phase = 1 - phase
	}
	return low + (high-low)*(phase*2)
}

// swarmDispatchPlaceholderView 是一帧占位胶囊要展示的内容；Visible 为 false 时整行不渲染。
type swarmDispatchPlaceholderView struct {
	Visible bool
	Text    string
}

// appendSwarmDispatchRow 在时间线末尾追加派发占位行。与子线程胶囊行一样常驻列表：
// 渲染时按当前状态决定是否显示，不显示时整行既不占高度也不留间距。
func appendSwarmDispatchRow(items []timelineItem) []timelineItem {
	return append(items, timelineItem{kind: timelineItemSwarmDispatch, id: swarmDispatchRowID, turnIndex: -1})
}

// swarmDispatchSourceMessage 返回用来判定派单块的那条消息：渲染快照里最后一条助手消息。
// streaming 表示这条消息此刻正在流式输出（liveID 与它相同），因此它的派单块可能还没写完。
// followed 表示这条消息之后还有别的消息（任意角色，包括新回合的工具调用、[编排] 汇报等），
// 说明主代理已经进入后续回合，这条消息里的派单早已结束。
func swarmDispatchSourceMessage(s *chatState, liveID string) (Message, bool, bool, bool) {
	if s == nil {
		return Message{}, false, false, false
	}
	s.mu.Lock()
	msgs := s.timelineCacheMsgs
	s.mu.Unlock()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "assistant" {
			continue
		}
		return msgs[i], liveID != "" && msgs[i].ID == liveID, i < len(msgs)-1, true
	}
	return Message{}, false, false, false
}

// swarmDispatchThreadRunning 报告当前会话主线程是否仍在运行：只有还在跑 turn 时才可能补齐派单块
// 或继续派出子代理，运行结束后占位就该收掉。
func swarmDispatchThreadRunning(s *chatState) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, th := range s.threads {
		if th.ID == s.selectedThreadID {
			return th.IsRunning
		}
	}
	return false
}

// swarmDispatchNewBeeCount 统计本条派单消息之后才启动的子代理数：CreatedAt 可用时只数 StartedAt
// 不早于它的 bee（StartedAt 为零值无法判断先后，按本条消息之后启动算）；CreatedAt 为空或格式不对时
// 退回统计全部有效子代理。
func swarmDispatchNewBeeCount(bees []SwarmBee, createdAt string) int {
	created, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return len(bees)
	}
	count := 0
	for _, bee := range bees {
		if bee.StartedAt.IsZero() || !bee.StartedAt.Before(created) {
			count++
		}
	}
	return count
}

// swarmDispatchPlaceholder 计算这一帧是否显示派发占位以及它的文案：
//   - 最后一条助手消息正在流式输出，且正文里有尚未闭合的派单块；
//   - 或该消息含已闭合的派单块，同时队列里还有没启动的子代理（Queued>0），
//     或者这条消息之后启动的子代理还没凑齐写出的条目数、且主线程仍在运行。
//
// 消息不再流式输出、后面又已经有了新回合的消息时，这条消息的派发已经结束：此时只有排队数还能让占位
// 留下（显示“排队中 N 个”），不再因为后一回合的子代理没凑齐而一直显示。子代理也只数本条消息之后
// 启动的那些，上一轮派出的旧子代理不能让第二轮派单提前收掉占位。
// bees 数量不少于条目数、队列也空了以后不再显示（真实胶囊已经接管这段时间线）。
func swarmDispatchPlaceholder(s *chatState, liveID string) swarmDispatchPlaceholderView {
	msg, streaming, followed, ok := swarmDispatchSourceMessage(s, liveID)
	if !ok {
		return swarmDispatchPlaceholderView{}
	}
	open, closed, count := swarmFenceDispatchInfo(msg.Text)
	if !open && !closed {
		return swarmDispatchPlaceholderView{}
	}
	// 占位判定只需要“有效子线程数 + 排队数”，直接读派生视图，不再每帧拷贝整份 SwarmState。
	view := swarmStateViewFor(s)
	if view == nil || view.isBee {
		// 当前会话本身就是子代理时，不显示主代理的派发占位。
		return swarmDispatchPlaceholderView{}
	}
	beeCount := swarmDispatchNewBeeCount(view.active, msg.CreatedAt)
	queued := view.queued

	// 不在流式输出、后面又有新回合的消息：本条派单已结束，只有排队中的子代理还能显示占位。
	stale := !streaming && followed
	streamingPending := streaming && open
	closedPending := closed && (queued > 0 || (!stale && beeCount < count && swarmDispatchThreadRunning(s)))
	if !streamingPending && !closedPending {
		return swarmDispatchPlaceholderView{}
	}
	// 条目已经全部落地、队列也空了：占位自然消失，交给真实胶囊。
	if queued == 0 && beeCount >= count {
		return swarmDispatchPlaceholderView{}
	}

	text := "正在派发…"
	switch {
	case queued > 0 && !streamingPending:
		text = fmt.Sprintf("排队中 %d 个", queued)
	case count > 0:
		text = fmt.Sprintf("正在派发 %d 个子代理…", count)
	}
	return swarmDispatchPlaceholderView{Visible: true, Text: text}
}

// renderSwarmDispatchPlaceholder 渲染派发占位胶囊：尺寸、圆角、内边距与描边沿用 renderSwarmCapsuleButton
// 的胶囊外观（同一套 RadiusXL / Space 令牌），但不带点击与浮窗，左侧是一个按定时器低频重建的呼吸状态点。
func renderSwarmDispatchPlaceholder(c *ui.Context, key string, view swarmDispatchPlaceholderView) {
	if !view.Visible {
		return
	}
	renderSwarmCapsuleFlow(c, key+"-row", func() {
		pill := ui.Row(c.Key(key)).
			MaxWidthPercent(100).
			Radius(RadiusXL).
			Padding(Space1, Space3, Space1, Space3).
			Border(1, ThemeBorder(c)).
			Background(ThemeSurface(c)).
			AlignItems(ui.Center).
			Gap(Space2)
		// 呼吸动画改由低频定时重建驱动：按帧时间算三角波透明度并量化成 8 档，每周期请求 8 次重绘，
		// 不再用框架动画帧（那会按屏幕刷新率、最高 120Hz 整窗重建）。占位消失后本函数不再运行，
		// 定时器也就自然停下。
		pulse := swarmDispatchPulseAlpha(c.Now())
		c.After(swarmDispatchPulsePeriod / swarmDispatchPulseSteps)
		pill.Children(func() {
			ui.Box(c.Key(key+"-dot")).
				Size(Space2, Space2).
				Radius(RadiusPill).
				Background(ThemeAccent(c).Alpha(float32(pulse)))
			ui.Text(c, view.Text).
				FontSize(scaledFont(TypeFootnote)).
				FontWeight(600).
				TextColor(ThemeSecondaryText(c)).
				SingleLine()
		})
	})
}
