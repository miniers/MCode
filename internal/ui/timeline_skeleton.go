package mcodeui

import (
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

// timelineSkeletonMaxWait 是首帧骨架等待历史落地的上限。
// 历史加载失败时 selectThread 只弹 toast、不会推进 messagesRev；
// 如果首帧骨架在历史落地之后才第一次被观测到，messagesRev 也不会再变，
// 因此上限取两次呼吸周期，避免骨架永远呼吸，也避免空会话刚打开时骨架停留过久。
const timelineSkeletonMaxWait = 2 * motionSweep

// 骨架行：用户消息右对齐、助手消息左对齐、工具行居中偏左。
const (
	timelineSkeletonUserWidth      float32 = 40
	timelineSkeletonAssistantWidth float32 = 78
	timelineSkeletonToolWidth      float32 = 56
	timelineSkeletonLineHeight     float32 = 14
	timelineSkeletonToolHeight     float32 = 12
	timelineSkeletonRadius         float32 = RadiusSM
	timelineSkeletonFillAlpha      float32 = 0.35
)

// timelineSkeletonState 记录每个会话是否已经落地过历史。
// 与输入区一样用包级 map 按 *chatState 存放，不改 chatState。
type timelineSkeletonState struct {
	threadID   string
	revAtEnter uint64
	enteredAt  time.Time
	loading    bool
	// seenThreads 记录已经落地过历史的会话：再次进入时不再显示骨架。
	seenThreads map[string]bool
}

var (
	timelineSkeletonMu     sync.Mutex
	timelineSkeletonStates = make(map[*chatState]*timelineSkeletonState)
)

// getTimelineSkeletonState 取（必要时创建）会话的首帧骨架状态；会话关闭时顺手清理。
func getTimelineSkeletonState(s *chatState) *timelineSkeletonState {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()

	timelineSkeletonMu.Lock()
	defer timelineSkeletonMu.Unlock()
	if closed {
		delete(timelineSkeletonStates, s)
		return nil
	}
	st := timelineSkeletonStates[s]
	if st == nil {
		st = &timelineSkeletonState{seenThreads: make(map[string]bool)}
		timelineSkeletonStates[s] = st
	}
	return st
}

// shouldShowTimelineSkeleton 判断首帧骨架是否显示：
// 消息还没落地（列表为空）、历史仍在加载、且是首次进入该会话。
func shouldShowTimelineSkeleton(hasMessages, loading, firstEnter bool) bool {
	return !hasMessages && loading && firstEnter
}

// observe 观测当前 (threadID, messagesRev)，返回历史是否仍在加载。
// 切换会话时把首次观测到的 rev 记为起点；rev 增长即代表历史落地，
// 超过 timelineSkeletonMaxWait 也按落地处理（加载失败时 rev 不会动）。
func (st *timelineSkeletonState) observe(threadID string, rev uint64, now time.Time) bool {
	if st.threadID != threadID {
		st.threadID = threadID
		st.revAtEnter = rev
		st.enteredAt = now
		st.loading = !st.seenThreads[threadID]
		return st.loading
	}
	if st.loading && (rev != st.revAtEnter || now.Sub(st.enteredAt) > timelineSkeletonMaxWait) {
		st.loading = false
		st.seenThreads[threadID] = true
	}
	return st.loading
}

// timelineSkeletonPhase 返回当前会话的首次进入标记与加载状态。
func timelineSkeletonPhase(s *chatState, threadID string, rev uint64, now time.Time) (firstEnter, loading bool) {
	st := getTimelineSkeletonState(s)
	if st == nil || threadID == "" {
		return false, false
	}
	timelineSkeletonMu.Lock()
	defer timelineSkeletonMu.Unlock()
	loading = st.observe(threadID, rev, now)
	return !st.seenThreads[threadID], loading
}

// renderTimelineSkeleton 画 3 条呼吸中的骨架行，宽度分别为父容器的 40% / 78% / 56%。
// 减少动效时不呼吸，停在 35% 的静态填充上。
func renderTimelineSkeleton(c *ui.Context) {
	rows := []struct {
		key    string
		width  float32
		height float32
		align  ui.Align
	}{
		{key: "user", width: timelineSkeletonUserWidth, height: timelineSkeletonLineHeight, align: ui.End},
		{key: "assistant", width: timelineSkeletonAssistantWidth, height: timelineSkeletonLineHeight, align: ui.Start},
		{key: "tool", width: timelineSkeletonToolWidth, height: timelineSkeletonToolHeight, align: ui.Start},
	}

	ui.Column(c.Key("timeline-skeleton")).
		Gap(blockGap).
		Children(func() {
			for _, row := range rows {
				line := ui.Box(c.Key("timeline-skeleton-" + row.key)).
					WidthPercent(row.width).
					Height(row.height).
					Radius(timelineSkeletonRadius).
					Background(ThemeHover(c).Alpha(timelineSkeletonFillAlpha)).
					AlignSelf(row.align)
				// 透明度在 0.55 与 1 之间来回呼吸：Alternate 让每遍反向播放。
				// 减少动效时不注册循环关键帧，直接停在 35% 填充（透明度 1）。
				if reduceMotion(c) {
					line.Opacity(1)
					continue
				}
				breath := line.Keyframes("timeline-skeleton-breath", []ui.Keyframe{
					{At: 0, Value: 0.55, Ease: motionEaseStandard},
					{At: motionSweep, Value: 1, Ease: motionEaseStandard},
				}, ui.KeyframeOptions{Repeat: ui.RepeatForever, Alternate: true})
				line.Opacity(breath)
			}
		})
}

// renderTimelineSkeletonLayer 在聊天区顶部铺骨架层；数据落地后用 motionFast 淡出。
// 骨架是绝对定位的 PassThrough 层，不参与布局、不改变已有滚动位置。
func renderTimelineSkeletonLayer(c *ui.Context, s *chatState, threadID string, rev uint64, hasMessages bool) {
	firstEnter, loading := timelineSkeletonPhase(s, threadID, rev, c.Now())
	target := float32(0)
	if shouldShowTimelineSkeleton(hasMessages, loading, firstEnter) {
		target = 1
	}

	layer := ui.Column(c.Key("timeline-skeleton-layer")).
		Absolute().
		Left(0).
		Top(0).
		Right(0).
		Padding(Space5, Space7).
		PassThrough().
		IgnoreInput(true)
	alpha := layer.AnimateWith("timeline-skeleton-layer/alpha", target, animateDuration(c, motionFast), motionEaseStandard)
	layer.Opacity(alpha)
	if alpha <= 0.01 {
		return
	}
	layer.Children(func() {
		renderTimelineSkeleton(c)
	})
}
