package mcodeui

import (
	"math"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

// 侧边栏会话行的悬停信息卡，以及紧凑模式下超长标题的跑马灯。
//
// 两者都依赖「指针停在行上」这一瞬时状态，但 mygo 的悬停只在指针位于元素上时为真：
// 指针移到卡片上时行会立刻失去悬停。因此卡片用包级状态机记住进入/离开时刻，再用
// 180ms 宽限窗口盖住「把指针移到卡片上」这段时间。chatState 在禁改的 model.go 里，
// 状态只能放包级变量（与 sidebarBeeFolded 同一做法），并提供测试用的 reset。

const (
	// sidebarHoverCardWarmup 是鼠标停在会话行上多久后才显示悬停卡。
	sidebarHoverCardWarmup = 650 * time.Millisecond
	// sidebarHoverCardGrace 是指针离开会话行后的宽限时间：留出移到卡片上的时间。
	sidebarHoverCardGrace = 180 * time.Millisecond
	// sidebarHoverCardWarm 是「温热」窗口：离开后 3 秒内回到同一行立即出卡，避免反复闪卡。
	sidebarHoverCardWarm = 3 * time.Second
	// sidebarHoverCardWidth 是卡片宽度。
	sidebarHoverCardWidth float32 = 288
	// sidebarHoverCardIn 是卡片入场（右移 4px + 淡入）的时长。
	sidebarHoverCardIn = 160 * time.Millisecond
	// sidebarMarqueeDelay 是悬停后开始滚动的延迟。
	sidebarMarqueeDelay = 600 * time.Millisecond
	// sidebarMarqueeMinOverflow 是启用跑马灯的最小溢出宽度（px）：差得太少不值得滚。
	sidebarMarqueeMinOverflow float32 = 8
)

// sidebarHoverState 记录当前悬停的会话行与进入/离开时刻。
// ctx 用来把状态绑定到某个窗口：Context 是窗口级稳定对象，换窗口时整份状态清零。
type sidebarHoverState struct {
	mu        sync.Mutex
	ctx       *ui.Context
	threadID  string
	projectID string
	enteredAt time.Time
	leftAt    time.Time
}

var sidebarHover sidebarHoverState

// resetSidebarHoverStateLocked 清空状态，调用方需持有 mu。
func (st *sidebarHoverState) resetLocked(ctx *ui.Context) {
	st.ctx = ctx
	st.threadID = ""
	st.projectID = ""
	st.enteredAt = time.Time{}
	st.leftAt = time.Time{}
}

// resetSidebarHoverCardForTest 清空悬停卡的包级状态，避免测试之间串味。
func resetSidebarHoverCardForTest() {
	sidebarHover.mu.Lock()
	sidebarHover.resetLocked(nil)
	sidebarHover.mu.Unlock()
}

// observe 推进悬停状态机：返回本帧是否应显示卡片，以及需要等多久再请求下一帧。
// 返回的等待时长用于 c.After：预热结束时出卡、宽限期结束时收卡，两处都要下一帧。
// 状态挂在窗口级的 c 上（和 c.Key 同一层隔离），换窗口不会继承上一个窗口的悬停。
func (st *sidebarHoverState) observe(ctx *ui.Context, now time.Time, threadID, projectID string, hovered bool) (bool, time.Duration) {
	st.mu.Lock()
	defer st.mu.Unlock()

	// 状态按窗口隔离：上一个窗口刚离开的会话行与时刻不能带进新窗口，
	// 否则新窗口第一帧就会落进 180ms 宽限期，在从未悬停的情况下把卡片显示出来。
	if st.ctx != ctx {
		st.resetLocked(ctx)
	}

	if hovered {
		switch {
		case st.threadID != threadID:
			st.threadID, st.projectID = threadID, projectID
			st.enteredAt, st.leftAt = now, time.Time{}
		case !st.leftAt.IsZero():
			// 回到同一行：3 秒内视为温热，直接跳过预热。
			if now.Sub(st.leftAt) < sidebarHoverCardWarm {
				st.enteredAt = now.Add(-sidebarHoverCardWarmup)
			} else {
				st.enteredAt = now
			}
			st.leftAt = time.Time{}
		}
		if elapsed := now.Sub(st.enteredAt); elapsed < sidebarHoverCardWarmup {
			return false, sidebarHoverCardWarmup - elapsed
		}
		return true, 0
	}

	// 未悬停：只有当前记录的那一行负责记离开时间与收卡。
	if st.threadID != threadID {
		return false, 0
	}
	if st.leftAt.IsZero() {
		st.leftAt = now
	}
	if since := now.Sub(st.leftAt); since < sidebarHoverCardGrace {
		return true, sidebarHoverCardGrace - since
	}
	return false, 0
}

// renderSidebarHoverCard 渲染会话行的悬停信息卡。
// 只读展示 Thread 现有字段，不新增后端接口；卡片外壳用 ui.PopoverBase（禁 ui.Popover）。
func renderSidebarHoverCard(c *ui.Context, s *chatState, row ui.Element, hovered bool, o sidebarThreadRowOptions, thVal Thread) {
	if s == nil || thVal.ID == "" {
		return
	}
	open, wait := sidebarHover.observe(c, c.Now(), thVal.ID, o.projectID, hovered)
	if wait > 0 {
		c.After(wait)
	}
	if !open {
		return
	}

	// 局部开关：PopoverBase 点外部或 Esc 时把它置回 false，下一帧由状态机重新决定。
	panelOpen := true
	winW, winH := c.Size()
	panel := PopoverCard(c, PopoverProps{Key: "hover-card-" + thVal.ID, Anchor: row, Open: &panelOpen, Width: sidebarHoverCardWidth, MaxWidth: max(1, winW-Space8), MaxHeight: max(1, winH-Space8)}, func() {
		ui.Scroll(c.Key("hover-card-scroll-" + thVal.ID)).MaxHeight(max(1, winH-Space8-Space4)).Children(func() {
			ui.Column(c.Key("hover-card-body-" + thVal.ID)).
				FillWidth().
				Gap(Space2).
				Children(func() {
					renderSidebarHoverCardBody(c, s, thVal)
				})
		})
	})
	panel.AttachTo(row, ui.AnchorTopEnd, ui.AnchorTopStart)
}

// renderSidebarHoverCardBody 渲染卡片内容：标题、派生来源、项目、分支、worktree、
// 供应商与模型、状态、最近活动。没有数据源的字段（如改动行数）不展示。
func renderSidebarHoverCardBody(c *ui.Context, s *chatState, thVal Thread) {
	title := thVal.Title
	if title == "" {
		title = "新会话"
	}
	_, winH := c.Size()
	ui.Scroll(c.Key("hover-card-title-scroll-" + thVal.ID)).FillWidth().MaxHeight(max(1, winH/3)).Children(func() {
		ui.Text(c, title).
			FontSize(sidebarFont(FontSizeThreadTitle)).
			FontWeight(500).
			TextColor(ThemePrimaryText(c))
	})

	if thVal.ForkedFromThreadID != "" {
		source := s.threadTitleByID(thVal.ForkedFromThreadID)
		if source == "" {
			source = thVal.ForkedFromThreadID
		}
		label := "继续自"
		if thVal.ForkedFromMessageID != "" {
			label = "分叉自"
		}
		hoverCardField(c, "hover-card-fork-"+thVal.ID, label, source)
	}

	if name := filepath.Base(s.projectPathByID(thVal.ProjectID)); name != "" && name != "." && name != string(filepath.Separator) {
		hoverCardField(c, "hover-card-project-"+thVal.ID, "项目", name)
	}

	worktree := s.aiWorktree(thVal.ID)
	if worktree.Branch != "" {
		hoverCardField(c, "hover-card-branch-"+thVal.ID, "分支", worktree.Branch)
	}
	if worktree.Path != "" {
		hoverCardField(c, "hover-card-worktree-"+thVal.ID, "Worktree", worktree.Path)
	}

	var modelParts []string
	if provider := providerDisplayName(thVal.Provider); provider != "" {
		modelParts = append(modelParts, provider)
	}
	if thVal.Model != "" {
		modelParts = append(modelParts, thVal.Model)
	}
	hoverCardField(c, "hover-card-model-"+thVal.ID, "模型", strings.Join(modelParts, " · "))

	status := sidebarHoverCardStatus(s, thVal)
	dot := StatusIdle
	switch status {
	case "运行中":
		dot = StatusRunning
	case "待审批":
		dot = StatusAttention
	case "失败":
		dot = StatusError
	}
	ui.Row(c.Key("hover-card-status-" + thVal.ID)).FillWidth().Gap(Space3).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "状态").FontSize(sidebarFont(FontSizeProjectName)).TextColor(ThemeSecondaryText(c)).MinWidth(52)
		StatusDot(c, nil, dot, status)
		ui.Text(c, status).FontSize(sidebarFont(FontSizeCallout)).TextColor(ThemePrimaryText(c)).SingleLine()
	})

	if activity := threadActivityTime(thVal); !activity.IsZero() {
		hoverCardField(c, "hover-card-activity-"+thVal.ID, "最近活动", formatRelativeTime(activity))
	}
}

// hoverCardField 渲染卡片里的一行「标签 + 值」；值为空时整行不渲染。
func hoverCardField(c *ui.Context, key, label, value string) {
	if value == "" {
		return
	}
	ui.Row(c.Key(key)).FillWidth().Gap(8).Children(func() {
		ui.Text(c, label).
			FontSize(sidebarFont(FontSizeProjectName)).
			TextColor(ThemeSecondaryText(c)).
			MinWidth(52)
		ui.Text(c, value).
			FontSize(sidebarFont(FontSizeCallout)).
			TextColor(ThemePrimaryText(c)).
			Grow(1).
			MaxLines(2)
	})
}

// sidebarHoverCardStatus 返回会话当前状态的短文案：运行中 / 待审批 / 失败 / 空闲。
// 运行与待审批优先于回合终态：仍在跑或等审批时，上一轮的结束状态已经没有意义。
func sidebarHoverCardStatus(s *chatState, thVal Thread) string {
	switch {
	case s.threadStillBusy(thVal):
		return "运行中"
	case thVal.HasPendingRequest:
		return "待审批"
	case s.threadTurnFinished(thVal.ID) == turnDoneKindFailed:
		return "失败"
	default:
		return "空闲"
	}
}

// sidebarMarqueeDuration 返回跑马灯单程时长：max(3.8s, 2.4s + 溢出px/30)。
// 溢出越多滚得越久，保持大致恒定的滚动速度。
func sidebarMarqueeDuration(over float32) time.Duration {
	sec := math.Max(3.8, 2.4+float64(over)/30)
	return time.Duration(sec * float64(time.Second))
}

// sidebarMarqueeActive 判断是否需要挂跑马灯：必须已悬停、已量到尺寸，
// 且自然宽超出可用宽超过 sidebarMarqueeMinOverflow（差得太少不值得滚）。
func sidebarMarqueeActive(natural, avail, reserved float32, hovered bool) bool {
	if !hovered || natural <= 0 || avail <= 0 {
		return false
	}
	return natural-(avail-reserved) > sidebarMarqueeMinOverflow
}

// renderSidebarScrollingTitle 渲染会话标题：外层 ClipX 负责裁掉溢出部分，
// 内层文本保持自然宽度（Shrink(0)），因此可以按 LayoutBounds 量出溢出量。
// 只有紧凑单行模式才调用；悬停且溢出超过 8px 时挂 Keyframes 横向滚动，
// 鼠标离开后不再挂动画，标题立即回位（避免常驻无限动画持续重绘）。
//
// 返回值是文本元素本身，调用方仍可继续设置字号与颜色。
func renderSidebarScrollingTitle(c *ui.Context, key, title string, hovered bool, reserved float32) ui.Element {
	var text ui.Element
	clip := ui.Row(c.Key(key)).Grow(1).Shrink(1).MinWidth(0).ClipX()
	clip.Children(func() {
		text = ui.Text(c, title).SingleLine()
		if hovered && !reduceMotion(c) {
			text.Shrink(0)
		} else {
			text.Grow(1).Shrink(1).MinWidth(0).Ellipsis("…")
		}
	})
	if !hovered || reduceMotion(c) {
		return text
	}

	natural := text.LayoutBounds().W
	avail := clip.LayoutBounds().W
	if !sidebarMarqueeActive(natural, avail, reserved, hovered) {
		return text
	}
	over := natural - (avail - reserved)

	k := text.Keyframes("marquee", []ui.Keyframe{
		{Value: 0},
		{At: sidebarMarqueeDuration(over), Value: 1},
	}, ui.KeyframeOptions{
		Repeat:    ui.RepeatForever,
		Alternate: true,
		Delay:     sidebarMarqueeDelay,
	})
	text.Translate(-over*k, 0)
	return text
}
