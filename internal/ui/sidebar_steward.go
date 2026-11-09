package mcodeui

import (
	"fmt"
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"
)

// 总管段左侧圆形头像的直径，比侧栏 bee 头像大一圈，作为常驻会话的识别点。
const stewardSectionAvatarSize float32 = 22

// renderSidebarStewardSection 渲染侧栏顶部的总管段：一行里是圆形头像、「昵称 · 总管」与
// 未处理数徽标。点行打开总管会话，点徽标展开待处理列表。
// 客户端没有总管能力、或总管会话还没建好时，退回只渲染原来的「待处理」行，
// 保证收件箱在这些情况下依然可达。
func renderSidebarStewardSection(c *ui.Context, s *chatState) {
	if _, ok := s.stewardClient(); !ok {
		renderAttentionInboxEntry(c, s)
		return
	}
	if enabled, known := s.stewardEnabledState(); known && !enabled {
		renderSidebarStewardDisabledRow(c, s)
		renderAttentionInboxEntry(c, s)
		return
	}
	threadID := s.stewardThreadID()
	if threadID == "" {
		renderAttentionInboxEntry(c, s)
		return
	}
	renderSidebarStewardRow(c, s, threadID)
}

// settingsPageSteward 是总管设置所在的页 ID。
const settingsPageSteward = "steward"

// renderSidebarStewardDisabledRow 渲染总管未启用时的置灰行：头像与标签，
// 点行不建会话，直接打开设置面板并定位到「启用总管」那一行。
func renderSidebarStewardDisabledRow(c *ui.Context, s *chatState) {
	nickname, avatar := s.stewardIdentity()
	label := stewardLabel(nickname) + "（未启用）"

	row := ui.Row(c.Key("sidebar-steward-disabled")).
		MinHeight(HeightThreadRow).
		Padding(Space1, ListInset, Space1, ListInset).
		Margin(Space0, ListInset, 0, ListInset).
		Gap(Space3).
		AlignItems(ui.Center).
		Radius(RowCornerRadius).
		Label(label)

	if row.Hovered() {
		row.Background(ThemeHover(c))
	}

	if row.Clicked() {
		openStewardSettings(s)
	}

	row.Children(func() {
		renderStewardSectionAvatar(c, "sidebar-steward-avatar-disabled", avatar)
		ui.Text(c, label).
			FontSize(scaledFont(FontSizeThreadTitle)).
			TextColor(ThemeSecondaryText(c)).
			Grow(1).
			SingleLine()
	})
}

// openStewardSettings 打开设置面板并定位到「启用总管」那一行：
// 切页、滚动与高亮都复用逐项搜索的 focusSettingsItem，免得另造一套定位逻辑。
func openStewardSettings(s *chatState) {
	openSettingsAt(s, "row-steward-enabled")
}

// stewardThreadUnread 是总管行的未读判断：hasThreadUnread 收的是会话本身，
// 这里先按 ID 从会话列表里找出总管会话。调用方不得持有 s.mu（内部按需取锁）。
func (s *chatState) stewardThreadUnread(threadID string) bool {
	s.mu.Lock()
	var th Thread
	found := false
	for _, candidate := range s.threads {
		if candidate.ID == threadID {
			th, found = candidate, true
			break
		}
	}
	s.mu.Unlock()
	if !found {
		return false
	}
	return s.hasThreadUnread(th)
}

// stewardSelected 判断某个会话是不是总管会话（侧栏高亮、空态文案都靠它）。
// 不想多同步一次锁：s.stewardThreadID() 每次都会从客户端现问，拿到的就是最新值。
func stewardSelected(s *chatState, threadID string) bool {
	if s == nil || threadID == "" {
		return false
	}
	return s.stewardThreadID() == threadID
}

// renderSidebarStewardRow 渲染合并后的总管行。
func renderSidebarStewardRow(c *ui.Context, s *chatState, threadID string) {
	nickname, avatar := s.stewardIdentity()

	// 待处理数据与「待处理」行同源：同一份数量、同一套订阅与同一张浮层，
	// 合并成一行后不需要第二套状态。
	var st *attentionInboxState
	count := 0
	if _, ok := s.attentionClient(); ok {
		s.refreshAttentionInbox()
		s.ensureAttentionWatch()
		st = getAttentionInboxState(s)
		if st != nil {
			st.mu.Lock()
			count = st.count
			st.mu.Unlock()
		}
	}

	// 决策数在后台按最小间隔刷新，渲染线程只读缓存，不同步查库。
	s.refreshStewardDecisionCount()
	decisionCount, decisionCountSupported := s.stewardDecisionCount()

	s.mu.Lock()
	isSelected := s.selectedThreadID == threadID
	s.mu.Unlock()

	// 总管这一轮是否还在跑：与普通会话行同一判据（自己运行中、子线程排队、子代理未结束）。
	busy := s.stewardRowBusy(threadID)

	popoverOpen := st != nil && st.open
	label := stewardLabel(nickname)
	row := ui.Row(c.Key("sidebar-steward")).
		MinHeight(HeightThreadRow).
		Padding(Space1, ListInset, Space1, ListInset).
		Margin(Space0, ListInset, 0, ListInset).
		Gap(Space3).
		AlignItems(ui.Center).
		Radius(RowCornerRadius).
		Label(label)

	if isSelected || popoverOpen {
		row.Background(ThemeSelected(c))
	} else if row.Hovered() {
		row.Background(ThemeHover(c))
	}

	if row.Clicked() {
		s.openStewardThread()
	}

	row.Children(func() {
		if busy {
			renderStewardSectionAvatarBusy(c, "sidebar-steward-avatar", avatar, ThemeAccent(c))
		} else {
			renderStewardSectionAvatar(c, "sidebar-steward-avatar", avatar)
		}
		ui.Text(c, label).
			FontSize(scaledFont(FontSizeThreadTitle)).
			TextColor(ThemePrimaryText(c).Alpha(0.92)).
			Grow(1).
			SingleLine()
		// 未读点与普通会话行同款：总管回复完而用户没看时能一眼看出来（可与待处理徽标共存）。
		if !isSelected && s.stewardThreadUnread(threadID) {
			StatusDot(c, "unread-dot-"+threadID, StatusIdle, "未读消息").
				Background(ThemeAccent(c)).
				Margin(0, Space0, 0, Space0)
		}
		if decisionCountSupported && decisionCount > 0 {
			renderAttentionCountBadge(c, "sidebar-steward-decisions", fmt.Sprintf("未答决策 %d 张", decisionCount), decisionCount)
		}
		// 徽标本身就是按钮：只切换待处理浮层，不抢行走的「打开会话」。
		if count > 0 {
			renderAttentionCountBadge(c, "sidebar-steward-attention", fmt.Sprintf("待处理 %d 条", count), count).
				OnClick(func() {
					st.open = !st.open
					s.invalidate()
				})
		}
	})

	// 浮层沿用原来的 key 与渲染函数，只是锚点从「待处理」行换成了总管行。
	if st != nil {
		appPopover(c.Key("sidebar-attention-inbox-popover"), row, &st.open, func() {
			st.open = false
		}, func() {
			renderAttentionInboxPopover(c, s)
		})
	}
}

// withoutStewardThread 从侧栏的三份副本里去掉总管 feed 会话及其项目。
// 入参都是 renderSidebar 复制出来的副本，不会改到 s.projects / s.threads。
func withoutStewardThread(stewardThread string, projects []Project, threads []Thread, threadsByProj map[string][]Thread) ([]Project, []Thread, map[string][]Thread) {
	hidden := func(threadID string) bool {
		if threadID == "" {
			return false
		}
		if stewardThread != "" && threadID == stewardThread {
			return true
		}
		return false
	}

	if projectID := stewardProjectIDForThread(stewardThread, threads, threadsByProj); projectID != "" {
		projects = withoutStewardProject(projectID, projects)
		delete(threadsByProj, projectID)
	}

	keptThreads := threads[:0]
	for _, th := range threads {
		if !hidden(th.ID) {
			keptThreads = append(keptThreads, th)
		}
	}
	// 分组表里的总管会话也一并摘掉：项目被删时整组已删，剩下的组各自过滤。
	for pid, list := range threadsByProj {
		keptList := list[:0]
		for _, th := range list {
			if !hidden(th.ID) {
				keptList = append(keptList, th)
			}
		}
		threadsByProj[pid] = keptList
	}
	return projects, keptThreads, threadsByProj
}

// stewardProjectIDForThread 从会话列表或分组表里找出总管会话所属的项目 ID：
// 平铺视图没有时再查分组表（总管会话被删的项目不会留在平铺视图里）。找不到返回空串。
func stewardProjectIDForThread(stewardThread string, threads []Thread, threadsByProj map[string][]Thread) string {
	if stewardThread == "" {
		return ""
	}
	for _, th := range threads {
		if th.ID == stewardThread {
			if th.ProjectID != "" {
				return th.ProjectID
			}
			break
		}
	}
	for pid, list := range threadsByProj {
		for _, th := range list {
			if th.ID == stewardThread {
				return pid
			}
		}
	}
	return ""
}

// stewardProjectID 返回总管 feed 所在项目的 ID，供侧栏和项目选择器过滤固定入口。
func (s *chatState) stewardProjectID() string {
	if s == nil {
		return ""
	}
	threadID := s.stewardThreadID()
	if threadID == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return stewardProjectIDForThread(threadID, s.threads, s.threadsByProj)
}

// withoutStewardProject 返回剔除总管项目后的新切片，不修改入参；projectID 为空时原样复制一份。
func withoutStewardProject(projectID string, projects []Project) []Project {
	kept := make([]Project, 0, len(projects))
	for _, p := range projects {
		if projectID != "" && p.ID == projectID {
			continue
		}
		kept = append(kept, p)
	}
	return kept
}

// renderStewardSectionAvatar 渲染总管段左侧的圆形头像：深色底、细描边，里面是头像字符。
// 结构与侧栏 bee 头像一致，只是尺寸更大。
func renderStewardSectionAvatar(c *ui.Context, key, avatar string) {
	acc := ThemeAccent(c)
	Avatar(c, AvatarProps{
		Key: key, Text: avatar, Size: stewardSectionAvatarSize, FontSize: stewardSectionAvatarSize * 0.48,
		Foreground: acc, Background: acc.Alpha(0.18), Border: acc.Alpha(0.55),
	})
}

// stewardRowBusy 返回总管行是否该显示运行中角标。找不到对应会话时按不在忙处理：
// 宁可少一个动画，也不要凭空亮灯。threadStillBusy 内部会取锁，在这里统一取一次再调。
func (s *chatState) stewardRowBusy(threadID string) bool {
	if threadID == "" {
		return false
	}
	s.mu.Lock()
	var th *Thread
	for i := range s.threads {
		if s.threads[i].ID == threadID {
			th = &s.threads[i]
			break
		}
	}
	s.mu.Unlock()
	return th != nil && s.threadStillBusy(*th)
}

// renderStewardSectionAvatarBusy 在头像右上角叠一颗呼吸圆点，表示总管这一轮仍在工作。
// 普通会话行用行首 3×3 点阵表示运行中，但总管行行首是识别用的头像，
// 换成点阵会丢掉识别点，所以改成角标叠加、不动布局。
func renderStewardSectionAvatarBusy(c *ui.Context, key, avatar string, color ui.Color) {
	ringColor := ThemeWindowBg(c)
	ui.Row(c.Key(key)).
		Size(stewardSectionAvatarSize, stewardSectionAvatarSize).
		Shrink(0).
		Children(func() {
			renderStewardSectionAvatar(c, key+"-img", avatar)
			ui.Box(c.Key(key+"-busy")).
				Size(9, 9).
				Attach(ui.AnchorTopRight, ui.AnchorTopRight).
				Draw(func(p *ui.Painter, r ui.Rect) {
					// 1000ms 一轮、120ms 一帧：只重绘这颗点，与点阵图标的省帧口径一致。
					alpha := float32(1)
					if !reduceMotion(c) {
						const period, frame = 1000, 120
						ms := int(p.Now().UnixMilli() % period)
						p.After(time.Duration(frame-ms%frame) * time.Millisecond)
						alpha = 0.35 + 0.65*(1-float32(ms)/period)
					}
					p.Fill(r, ringColor, 4.5)
					p.Fill(ui.Rect{X: r.X + 1.5, Y: r.Y + 1.5, W: 6, H: 6}, color.Alpha(alpha), 3)
				})
		})
}

// stewardStarterPrompts 是总管会话开场的三枚起步胶囊；用户开口后不再显示。
var stewardStarterPrompts = []string{
	"有什么要我处理的？",
	"今天发生了什么？",
	"我想做一件事",
}

// stewardWantsStarter 判断当前是否应该显示起步胶囊：打开的是总管会话，
// 且这段会话里还没有用户自己说的话（宿主转来的事件消息不算用户开口）。
func stewardWantsStarter(s *chatState) bool {
	if s == nil {
		return false
	}
	threadID := s.stewardThreadID()
	if threadID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.selectedThreadID != threadID {
		return false
	}
	for _, m := range s.messages {
		if m.Role == "user" && !isHiddenTriggerKind(m.Kind) {
			return false
		}
	}
	return true
}

// renderStewardStarterCapsules 在总管会话开头渲染三枚起步胶囊，点击即按正常发送路径发出去。
func renderStewardStarterCapsules(c *ui.Context, s *chatState) {
	if !stewardWantsStarter(s) {
		return
	}
	s.mu.Lock()
	threadID := s.selectedThreadID
	s.mu.Unlock()

	ui.Row(c.Key("steward-starter-capsules")).
		FillWidth().
		Justify(ui.Center).
		Gap(Space3).
		Margin(0, 0, Space3, 0).
		Children(func() {
			for i, prompt := range stewardStarterPrompts {
				text := prompt
				Button(c, ButtonOpts{
					Key:     "steward-starter-" + strconv.Itoa(i),
					Text:    text,
					Variant: ButtonSecondary,
					Size:    ControlMD,
					Label:   text,
					OnClick: func() {
						// 走输入框同一条发送路径：斜杠命令、忙碌排队等规则与手打一致。
						s.sendDraft(threadID, text)
						s.invalidate()
					},
				})
			}
		})
}
