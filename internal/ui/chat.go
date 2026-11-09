package mcodeui

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"mcode/internal/config"
	"mcode/resources"
)

// TrafficLightInset 是侧边栏隐藏时聊天标题栏为窗口交通灯预留的左边距（78pt）。
const TrafficLightInset float32 = 78

// chatHeaderBaseInset 是侧边栏显示时聊天标题栏的常规水平内边距。
const chatHeaderBaseInset float32 = 20

// chatHeaderHeight 为内容 28、上下内边距各 8 与底部分隔线 1 之和。
const chatHeaderHeight = HeaderHeight

// chatHeroFontSize 是空态引导大标语的字号：字阶最高到 TypePageTitle，这一处展示标题单独取值。
const chatHeroFontSize float32 = 28

// chatHeaderLeadingInsetFrom 按侧边栏显示进度，在给定的前导边距与常规内边距之间插值：
// 侧边栏完全隐藏（进度 0）时让出前导边距（macOS 是交通灯，Windows 为 0），
// 完全显示（进度 1）时回到常规内边距，中间按进度插值。
func chatHeaderLeadingInsetFrom(leadingInset, sidebarVisibleProgress float32) float32 {
	if sidebarVisibleProgress < 0 {
		sidebarVisibleProgress = 0
	} else if sidebarVisibleProgress > 1 {
		sidebarVisibleProgress = 1
	}
	return leadingInset + (chatHeaderBaseInset-leadingInset)*sidebarVisibleProgress
}

// chatHeaderLeadingInset 按侧边栏显示进度计算聊天标题栏的左边距：
// 侧边栏完全隐藏（进度 0）时预留交通灯区域，完全显示（进度 1）时回到常规内边距，中间按进度插值。
func chatHeaderLeadingInset(sidebarVisibleProgress float32) float32 {
	return chatHeaderLeadingInsetFrom(TrafficLightInset, sidebarVisibleProgress)
}

// renderChat 渲染右侧主会话区。
// 标题栏左边距按本帧的侧边栏显示进度插值。
func renderChat(c *ui.Context, s *chatState) {
	renderBackupStartupResult(c, s)
	// 先记录本帧聊天区可用宽度：按比例档的内容宽度要靠它换算（侧边栏与右侧分栏都要扣掉）。
	updateChatPaneWidth(c, s)

	// 撤销轮次预览弹窗（撤销入口）
	renderRevertDialog(c, s)

	// 「显示完整输出」弹窗挂在会话根上：时间线行在虚拟列表内部，放在行里不会成为根级弹窗。
	renderToolOutputDialog(c, s)

	// 切换「自动折叠思考内容」后工具组与回合折叠的默认开合要跟着变，即使没有新消息也需重建。
	groupsOpenChanged := timelineSettingsChanged(s)

	// 发送后置顶消息的开关：isPinSentMessageEnabled 会锁设置状态的锁，必须在 s.mu 之前取。
	pinEnabled := isPinSentMessageEnabled(s)

	s.mu.Lock()
	selectedID := s.selectedThreadID
	expandedRev := s.expandedRev
	needRebuild := s.messagesRev != s.timelineCacheRev || s.timelineCacheMsgs == nil || expandedRev != s.timelineCacheExpandedRev || selectedID != s.timelineSnapshotThreadID || groupsOpenChanged
	rev := s.messagesRev
	var messages []Message
	if needRebuild {
		messages = make([]Message, len(s.messages))
		copy(messages, s.messages)
	}
	curUsage := s.threadUsage
	var curResolvedPlans []resolvedPlan
	if s.resolvedPlans != nil && selectedID != "" {
		curResolvedPlans = slices.Clone(s.resolvedPlans[selectedID])
	}
	var curAnsweredQuestions []answeredQuestion
	if s.answeredQuestions != nil && selectedID != "" {
		curAnsweredQuestions = slices.Clone(s.answeredQuestions[selectedID])
	}
	// 发送后锚定刚发出的消息：sending 或乐观运行中时先记下锚点，等消息到达后顶对齐（见 chat_anchor.go）。
	holdAnchor := s.syncChatAnchorLocked(selectedID, pinEnabled)
	// 切换会话或发送消息后滚到底并恢复跟随；等消息到达后再消费，其余时候不干预用户的滚动。
	if s.scrollToEnd && len(s.messages) > 0 {
		s.scrollToEnd = false
		s.chatListState.FollowEnd = true
		s.chatListState.ScrollToEnd()
	}
	if holdAnchor {
		// 锚定期间默认不跟随到底：刚发出的消息要停在视口顶部，回复在下方流入时视口不动。
		// 底部垫高动画推进的那几帧会由 applyChatAnchor（列表构建之后、布局之前）重新打开
		// FollowEnd，靠 follow-end 排版把锚点行一路顶上来。
		s.scrollToEnd = false
		s.chatListState.FollowEnd = false
	}
	var expandedCopy map[string]bool
	if len(s.expandedTool) > 0 {
		expandedCopy = make(map[string]bool, len(s.expandedTool))
		for k, v := range s.expandedTool {
			expandedCopy[k] = v
		}
	}
	running := false
	mainRunning := false
	for _, th := range s.threads {
		if th.ID == selectedID {
			// 子线程仍在跑时这一轮没结束，末尾活动组仍是“进行中”的组。
			mainRunning = th.IsRunning
			running = mainRunning || th.SwarmBusy
			break
		}
	}
	s.mu.Unlock()

	// 会话仍在运行且末尾仍是活动段时，末尾活动组就是“进行中”的组：构建时默认展开它，
	// 回合结束后不再传入，组回到默认折叠。
	liveGroupID := liveToolGroupID(messages, running)

	if needRebuild {
		items, newFps := s.rebuildTimelineIncremental(selectedID, messages, expandedCopy, liveGroupID)
		s.mu.Lock()
		s.timelineCacheRev = rev
		s.timelineCacheMsgs = messages
		s.timelineCacheItems = items
		s.timelineCacheExpandedRev = expandedRev
		s.timelineSnapshotThreadID = selectedID
		s.timelineSnapshotMsgs = newFps
		s.timelineSnapshotExpanded = cloneBoolMap(expandedCopy)
		s.mu.Unlock()
	} else {
		messages = s.timelineCacheMsgs
	}

	// 问答记录消息已经进来时，同一请求的内存记录不再交给时间线：卡片由消息渲染，
	// 内存记录只在消息到达前用于即时显示，两者同时出现就是两张卡片。
	curAnsweredQuestions = dropAnsweredQuestionsRecordedInMessages(curAnsweredQuestions, messages)

	// 没有选中会话时：待创建会话（尚未在后端建会话）照常显示空会话与 composer，否则显示空状态提示。
	if selectedID == "" {
		s.mu.Lock()
		pendingActive := s.pendingThread != nil
		s.mu.Unlock()
		if !pendingActive {
			ui.Column(c.Key("chat-empty")).Grow(1).FillHeight().Justify(ui.Center).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "选择或新建一个会话").TextColor(ThemeSecondaryText(c)).FontSize(TypePageTitle)
				Button(c, ButtonOpts{Key: "chat-onboarding", Text: "新用户引导", Variant: ButtonSecondary, OnClick: func() { openOnboarding(s) }})
			})
			return
		}
	}

	ui.Column(c.Key("chat-main")).Grow(1).FillHeight().Children(func() {
		// 会话头部：显示会话标题与项目动作
		renderChatHeader(c, s, selectedID, curUsage, sidebarVisibleProgress(s))
		renderStewardTaskBand(c, s, selectedID)

		// 子代理面板停靠在聊天列两侧：先算出它们让出的宽度，再渲染面板
		swarmReserve := swarmFloatReserve(c, s)
		renderSwarmBeeFloats(c, s)

		// 计算当前会话的运行中状态与正在输出的消息 ID（决定时间线的“回复中”指示与流式光标）
		var liveID string
		s.mu.Lock()
		isRunning := running
		if mainRunning && len(messages) > 0 {
			lastMsg := messages[len(messages)-1]
			if (lastMsg.Role == "assistant" || lastMsg.Kind == "reasoning") && lastMsg.Role != "user" && lastMsg.Role != "tool" && lastMsg.Role != "system" {
				liveID = lastMsg.ID
			}
		}
		if liveID != "" {
			s.lastLiveID = liveID
		}
		lastLiveID := s.lastLiveID
		s.mu.Unlock()

		// 聚合并组织时间线渲染项（连续工具调用合并为汇总组，并插入已裁决的计划历史态）；
		// 拼装结果按会话/消息版本/展开状态/计划指纹缓存，静止时直接复用上一帧的那份。
		timelineItems := s.assembledTimelineItemsWithQuestions(curResolvedPlans, curAnsweredQuestions)
		// 回合已结束但仍有后台任务在跑时，在最后一条回复下方贴一条等待条：会话不是在空闲，
		// 而是在等这些任务。放在会话总用量之前，让它紧贴最后一条回复。
		if waitData := backgroundTasksFor(s, selectedID); shouldShowBackgroundWait(isRunning, waitData) {
			timelineItems = append(timelineItems, backgroundWaitItem(selectedID))
		}
		// 会话总用量固定放在时间线底部；即使“回复中”指示出现，它仍在指示之上。
		if curUsage.Total.Input+curUsage.Total.Output > 0 {
			timelineItems = append(timelineItems, sessionUsageItem(selectedID))
		}
		// 会话运行中但还没有流式输出（刚发送完、等待首包或工具执行中）时，末尾补一条“回复中”指示，
		// 让发送后立刻能看到 AI 正在回复，直到后端报告 turn 结束或出错。
		if isRunning && liveID == "" {
			timelineItems = append(timelineItems, runningIndicatorItem(selectedID))
		}
		// 回合进行中每一步（每次模型调用）结束都会先写一次该回合用量：只看用量有值会在步骤之间的间隙
		// 提前冒出「已完成」，所以会话仍在跑时压掉进行中那一轮的完成块，之前的历史回合不受影响。
		turnRunning, latestTurn := s.runningTurn(selectedID)

		// 回合跳转快捷键：跳到上一个或下一个用户消息所在的 item。
		// ⌥⌘↑ 和 ⌥⌘↓ 已被 shortcuts.go 中的 previous-thread / next-thread 占用；
		// 依要求改用未被占用的快捷键 ⌃⌥↑ / ⌃⌥↓ (ui.Ctrl|ui.Alt 加 ui.KeyUp / ui.KeyDown)。
		if c.Shortcut(ui.Ctrl|ui.Alt, ui.KeyUp) {
			jumpToAdjacentUserMessage(s, timelineItems, -1)
		} else if c.Shortcut(ui.Ctrl|ui.Alt, ui.KeyDown) {
			jumpToAdjacentUserMessage(s, timelineItems, 1)
		}

		// 会话内查找（默认 ⌘F）：按有效绑定检测，改键后同样生效。
		// 注册表里该条目不进每帧注册集合，与 ⌘K、⌘B 的做法一致，避免被 dispatchActiveShortcut 抢占。
		if mods, key, ok := shortcutBindingFor("find-in-thread"); ok && c.Shortcut(mods, key) {
			s.openChatFind()
		}

		ensureChatMarkdown(c, s)

		// 消息列表（虚拟滚动，稳定 Key；FollowEnd 在 newChatState 中设置一次，
		// KeepOffset 为 false 使 anchorKey 锚定条目，FollowAnimation 为 nil 避免展开时位移动画）
		s.chatListState.Key = func(i int) any {
			if i >= 0 && i < len(timelineItems) {
				return timelineItems[i].id
			}
			return i
		}

		// 每个回合最后一条助手消息（按 messagesRev 缓存，长会话不再逐帧重建）
		lastAssistantPerTurn := s.lastAssistantPerTurn()

		// 消息列表的间距与上下留白跟界面密度走：紧凑档一起收窄。
		dens := currentDensity(c, s)
		listGap := dens.Gap
		listPadY := dens.PadY

		// 消息列表与输入框布局（两侧为停靠的子代理面板让出宽度，聊天内容在剩余空间里居中）
		chatBody := ui.Column(c.Key("chat-body-container")).Grow(1).MinHeight(0).FillWidth()
		setComposerFileDragOver(s, chatBody.FileDragOver())
		if dropped := chatBody.DroppedFiles(); len(dropped) > 0 {
			s.addAttachmentFiles(dropped)
		}
		// 让位宽度变化是整列位移，用略长于浮窗退场的时长，浮窗淡完后时间线再落定。
		chatBody.Padding(0,
			chatBody.AnimateWith("swarm-reserve-trailing", swarmReserve.trailing, animateDuration(c, DurSheet), motionEaseStandard),
			0,
			chatBody.AnimateWith("swarm-reserve-leading", swarmReserve.leading, animateDuration(c, DurSheet), motionEaseStandard))
		chatBody.Children(func() {
			// Hero 判定与输入框停靠共用 composerThreadEmpty：两处口径一致，
			// 才不会出现「输入框已停靠到底部、引导屏还占着中间」的错位。
			hero := composerThreadEmpty(len(messages), len(curResolvedPlans), len(curAnsweredQuestions)) && !s.hasForwardedContextBody(selectedID)
			// dockDec 是输入框本帧的布局结论；切换帧（首条消息刚发出）时才播位移与淡入。
			dockDec := s.composerDockDecisionFor(hero, selectedID)
			if !dockDec.Docked {
				// Hero 模式：当前会话无内容时输入框垂直居中，上方展示空态引导屏。
				renderSwarmQuestionBanner(c, s)
				renderApproval(c, s)
				// 上下两个弹性占位把「引导块 + 输入框」夹在中间：居中位置与改动前一致，
				// 而且输入框的父级与兄弟关系在 Hero/Docked 两态下不变，
				// mygo 的同父 FLIP 才能把同一个输入框元素从中间插值到底部。
				ui.Spacer(c)
				heroCol := ui.Column(c.Key("chat-hero-container")).FillWidth().AlignItems(ui.Center)
				// 引导块淡出：消失时播放（切到有消息的会话也会淡出，约 220ms）。
				// 过渡常驻注册、只在真正消失时播；输入框不放进来，否则会多出一份淡出的幽灵副本。
				// 必须显式写 Colors: true：mygo 的 ElementTransition 在 Position/Size/Colors
				// 全不设时等于三项都动画，引导块就会跟着布局变化做位置插值——审批卡长高把它挤走后，
				// 它会在旧位置停留，直到过渡停止那一帧才瞬移到位，中途盖住「提交」按钮并截走点击。
				// 只开 Colors 后只剩颜色通道与 Exit 淡出（Exit 走独立的 animateExits，不受这三项影响），
				// 不做位置和尺寸动画，布局一变就直接落在新位置。
				heroCol.Transition(ui.ElementTransition{
					Duration: composerHeroExitDuration,
					Colors:   true,
					Exit:     &ui.Motion{Opacity: 0},
				})
				heroCol.Children(func() {
					// 转发/分叉出来的新会话还没有消息：改为展示来源引导卡片，不再显示通用的新会话引导。
					if !renderContinuationStartCard(c, s, selectedID) {
						renderNewThreadPrompt(c, s, selectedID)
					}
					// 新会话首轮：后端还没推回用户消息时（messages 仍为空）也要能看到“回复中”，
					// 否则发送后引导屏毫无变化，看起来像没有反应。
					if isRunning {
						renderRunningIndicator(c, runningIndicatorKey(selectedID)+"-row")
					}
					// 总管会话还没开口时，输入框上方给三枚起步胶囊。
					renderStewardStarterCapsules(c, s)
				})
				renderComposer(c, s)
				ui.Spacer(c)
			} else {
				// Docked 停靠模式：有消息时展示时间线并底部停靠输入框
				// 锚点行在条目里的下标：列表构建时靠它接住锚点行元素，读上一帧的位置。
				anchorRow := -1
				if anchorID := s.chatAnchorRowID(); anchorID != "" {
					anchorRow = anchorRowIndex(timelineItems, anchorID)
				}
				// 锚点行与列表的上一帧窗口坐标，列表构建时再填（Bounds 只能读上一帧的值）。
				var anchorRowTop float32
				var listEl ui.Element
				timeline := ui.Row(c.Key("timeline-container")).Grow(1).MinHeight(0).FillWidth().AlignItems(ui.Stretch)
				// 时间线淡入：切换帧（dockDec.Promote）注册，之后只要过渡还在跑就继续注册——
				// 过渡只在这一帧注册、下一帧就停，淡入会被掐断，所以要跟到 Transitioning() 为假为止。
				// 切会话、打开已有消息的会话时时间线本来就在且没有过渡在跑，不会重播淡入。
				// Colors: true 让时间线只跟颜色通道，后续布局变化不做位置动画。
				if dockDec.Promote || timeline.Transitioning() {
					timeline.Transition(ui.ElementTransition{
						Duration: composerHeroExitDuration,
						Colors:   true,
						Enter:    &ui.Motion{Opacity: 0},
					})
				}
				timeline.Children(func() {
					// 锚定期间在列表底部垫出一屏空白，刚发出的消息才能停在视口顶部：
					// 动画推进时这块空白逐帧增长，follow-end 排版就把锚点行一路顶上来。
					listPadBottom := listPadY + s.chatAnchorPadExtra(c, chatBody)
					listEl = ui.List(c.Key("message-list"), &s.chatListState, len(timelineItems), func(i int) {
						item := timelineItems[i]
						isLastAssistant := itemHoldsTurnEnd(item, lastAssistantPerTurn) &&
							!turnRunSuppressesEnd(turnRunning, latestTurn, item.turnIndex)
						turnUsage, hasUsage := curUsage.Turns[item.turnIndex]

						// 工具组内的节拍由 timelineItemTopGap 自己决定间距，列表不设 Gap
						el := renderTimelineItem(c, s, item, liveID, lastLiveID, isLastAssistant, hasUsage, turnUsage, timelineItemTopGap(timelineItems, i, listGap))
						if i == anchorRow {
							// 锚点行已排过版（高度非 0）才记位置：没渲染的行留 0，交给“等几何量”分支。
							if b := el.Bounds(); b.H > 0 {
								anchorRowTop = b.Y
							}
						}
					}).Grow(1).MinHeight(0).Padding(listPadY, 0, listPadBottom, 0)
					// 列表构建完再处理锚定：推进垫高动画、下发收口滚动、判断是否该解除。
					s.applyChatAnchor(selectedID, timelineItems, anchorGeometry{
						rowTop:       anchorRowTop,
						listTop:      listEl.Bounds().Y,
						padTop:       listPadY,
						reduceMotion: reduceMotion(c),
					})
					// 时间线迷你地图在容器内绝对定位贴右缘、不占列表宽度，不走 Overlay，以免盖住浮窗
					renderTimelineMinimap(c, s, buildMinimapEntries(timelineItems))
					// 会话内查找条浮在聊天区右上角，横向让出迷你地图的宽度
					renderChatFindBar(c, s, selectedID, rev, messages, timelineItems)
				})

				// 跳到最新按钮浮在时间线右下，不占布局高度。
				atEnd := s.chatListState.AtEnd()
				unread := s.observeTimelineUnread(selectedID, messages, atEnd || s.chatAnchorHolding(selectedID))
				if shouldShowJumpToLatest(atEnd, -1) || unread > 0 {
					ui.Overlay(c, func() {
						// 悬停提示带上当前键位；离开底部后有新消息时，胶囊文字换成未读条数。
						jumpLabel := shortcutTitleWithKey("jump-to-latest", "跳到最新")
						text := "跳到最新"
						if unread > 0 {
							text = unreadJumpLabel(unread)
						}
						btn := Capsule(c, CapsuleProps{
							Key:         "chat-jump-latest",
							Text:        text,
							Icon:        IconChevronDown(),
							Tone:        ToneNeutral,
							Size:        ControlMD,
							Interactive: true,
							Tooltip:     jumpLabel,
							OnClick:     func() { s.jumpChatToLatest(selectedID) },
						})
						btn.AttachTo(timeline, ui.AnchorBottomRight, ui.AnchorBottomRight).Margin(0, 24, 10, 0)
					})
				}

				// 审批卡片区域
				renderSwarmQuestionBanner(c, s)
				renderApproval(c, s)

				// 长会话压缩建议卡片：输入框上方（压缩卡片优先，避免与转发建议叠在一起）
				if !renderCompactSuggestionCard(c, s, selectedID, curUsage) {
					renderForwardSuggestionCard(c, s, selectedID, isRunning)
				}

				// 总管会话的起步胶囊：用户还没开口时停在输入框上方，发出后条件不再满足自然消失。
				renderStewardStarterCapsules(c, s)

				// 底部输入框区域
				renderComposer(c, s)
			}

			// D-18 首帧骨架：只在会话首次进入、历史还没落地时显示，数据到达后用 motionFast 淡出。
			// 它是绝对定位的 PassThrough 层，不参与布局，也不改变已有滚动位置。
			renderTimelineSkeletonLayer(c, s, selectedID, rev, len(messages) > 0)
			// D-9 拖文件覆盖层：铺在时间线与输入区整块区域上方，不拦截拖放。
			renderComposerDropOverlay(c, s)
		})
	})
}

// 长会话压缩建议的判定阈值（空闲 1 小时，上下文占用达到窗口的 70%）。
// compactSuggestionMinimumTokens 是阈值的上限：窗口未知或窗口很大（如 1M）时，
// 上下文要涨到这个绝对量才提示，避免大窗口模型过早弹出建议。
const (
	compactSuggestionMinimumTokens int64 = 100_000
	compactSuggestionIdleInterval        = time.Hour

	// compactSuggestionWindowRatioNum/Den 表示阈值取模型上下文窗口的 70%（用整数分子分母避免浮点）。
	compactSuggestionWindowRatioNum int64 = 7
	compactSuggestionWindowRatioDen int64 = 10
)

// compactSuggestionThreshold 按模型上下文窗口上限计算压缩建议的上下文占用阈值。
// limit 为上下文窗口上限（tokens）：大于 0 时取 min(100k, limit*70%)，
// 32k 窗口得到 22.4k（70%），200k 及以上窗口固定 100k；limit <= 0（窗口未知）时返回 100k。
func compactSuggestionThreshold(limit int64) int64 {
	if limit <= 0 {
		return compactSuggestionMinimumTokens
	}
	threshold := limit * compactSuggestionWindowRatioNum / compactSuggestionWindowRatioDen
	if threshold > compactSuggestionMinimumTokens {
		return compactSuggestionMinimumTokens
	}
	return threshold
}

// forwardSuggestionRatio 是“转发到新会话”建议的上下文占用阈值（窗口的 80%）。
// 用浮点常量直接写比例，与 contextUsageFraction 的浮点结果比较。
const forwardSuggestionRatio float64 = 0.8

// shouldOfferForward 判定是否显示“转发到新会话”建议卡片。
// dismissed 表示本会话已忽略；running 表示会话仍在跑；used/limit 为上下文占用与窗口上限
// （limit <= 0 表示窗口未知，此时不提示，避免用错分母）；草稿以“/”开头（命令）时不打扰。
func shouldOfferForward(dismissed, running bool, used, limit int64, draft string) bool {
	if dismissed || running {
		return false
	}
	if strings.HasPrefix(strings.TrimSpace(draft), "/") {
		return false
	}
	if limit <= 0 {
		return false
	}
	return contextUsageFraction(used, limit) >= forwardSuggestionRatio
}

// shouldOfferCompact 判定是否显示长会话压缩建议卡片。
// 条件：开启“恢复旧会话前询问压缩”、本会话未被忽略、
// 当前上下文占用（contextTokens）达到按模型窗口换算的阈值（limit 为窗口上限，0 表示未知）、
// 会话空闲超过阈值、当前草稿不是以“/”开头的命令。
func shouldOfferCompact(askCompact, dismissed bool, contextTokens, limit int64, idle time.Duration, draft string) bool {
	if !askCompact || dismissed {
		return false
	}
	if strings.HasPrefix(strings.TrimSpace(draft), "/") {
		return false
	}
	if contextTokens < compactSuggestionThreshold(limit) {
		return false
	}
	return idle >= compactSuggestionIdleInterval
}

// compactThread 发送 /compact 命令压缩指定会话的上下文，并记录本会话不再提示。
func (s *chatState) compactThread(threadID string) {
	if s == nil || threadID == "" {
		return
	}
	dismissCompactSuggestion(s, threadID)
	s.setToast("正在压缩上下文…")
	go func() {
		if _, err := s.client.Send(context.Background(), threadID, "/compact"); err != nil {
			s.setToast("压缩失败: " + err.Error())
		}
	}()
}

// renderCompactSuggestionCard 渲染长会话压缩建议卡片：输入框上方、会话较长且久未活动时出现。
// 返回是否真的渲染了卡片：调用方据此决定要不要再展示“转发到新会话”建议（两张卡片不叠加）。
func renderCompactSuggestionCard(c *ui.Context, s *chatState, threadID string, curUsage ThreadUsage) bool {
	if s == nil || threadID == "" {
		return false
	}
	s.mu.Lock()
	draft := s.draft
	var idle time.Duration
	var currentThread Thread
	var found bool
	for i := range s.threads {
		if s.threads[i].ID == threadID {
			idle = time.Since(s.threads[i].UpdatedAt)
			// 复制一份再解锁使用：s.threads 可能被并发追加扩容，直接持有切片元素地址不安全。
			currentThread, found = s.threads[i], true
			break
		}
	}
	s.mu.Unlock()
	if !found {
		return false
	}

	// 优先按“当前上下文占用 / 模型窗口”判定；currentThreadContextTokens 内部会加锁，
	// 必须在上面解锁之后调用。拿不到窗口信息时退回累计用量与固定 100k 阈值（与旧行为一致）。
	usedTokens := curUsage.Total.Input + curUsage.Total.Output
	var limit int64
	if used, window, ok := currentThreadContextTokens(s, &currentThread); ok {
		usedTokens, limit = used, window
	}
	if !shouldOfferCompact(s.askCompactOnResumeEnabled(), compactSuggestionDismissed(s, threadID), usedTokens, limit, idle, draft) {
		return false
	}

	ui.Column(c.Key("chat-compact-offer")).FillWidth().Padding(Space3, Space7).AlignItems(ui.Center).Children(func() {
		ui.Column(c.Key("chat-compact-offer-card")).
			AlignItems(ui.End).
			Background(ThemeSurface(c)).
			Border(1, ThemeBorder(c)).
			Radius(RadiusXL).
			Padding(Space5).
			Gap(Space4).
			Children(func() {
				ui.Text(c, "这个会话较长，继续之前要先压缩上下文吗？").
					FontSize(TypeBody).
					TextColor(ThemeSecondaryText(c))

				ui.Row(c.Key("chat-compact-offer-actions")).Gap(Space3).Children(func() {
					Button(c.Key("chat-compact-offer-dismiss-btn"), ButtonOpts{
						Text:    "不用了",
						Variant: ButtonSecondary,
						Size:    ControlMD,
						OnClick: func() {
							dismissCompactSuggestion(s, threadID)
							s.invalidate()
						},
					})
					// 不压缩也可以直接转发到新会话（同样先记下忽略，切回本会话时不再弹压缩卡片）。
					Button(c.Key("chat-compact-offer-forward-btn"), ButtonOpts{
						Text:    "转发到新会话",
						Variant: ButtonSecondary,
						Size:    ControlMD,
						OnClick: func() {
							dismissCompactSuggestion(s, threadID)
							s.forwardThread(currentThread.ProjectID, threadID)
						},
					})
					Button(c.Key("chat-compact-offer-accept-btn"), ButtonOpts{
						Text:    "压缩",
						Variant: ButtonPrimary,
						Size:    ControlMD,
						OnClick: func() {
							s.compactThread(threadID)
						},
					})
				})
			})
	})
	return true
}

// renderForwardSuggestionCard 渲染“转发到新会话”建议卡片：上下文占用达到模型窗口的 80% 时
// 出现在输入框上方。只拿到真实上下文窗口才提示，会话仍在跑或草稿是命令时不打扰。
func renderForwardSuggestionCard(c *ui.Context, s *chatState, threadID string, running bool) {
	if s == nil || threadID == "" {
		return
	}
	s.mu.Lock()
	draft := s.draft
	var currentThread Thread
	var found bool
	for i := range s.threads {
		if s.threads[i].ID == threadID {
			// 复制一份再解锁使用：s.threads 可能被并发追加扩容，直接持有切片元素地址不安全。
			currentThread, found = s.threads[i], true
			break
		}
	}
	s.mu.Unlock()
	if !found {
		return
	}

	// currentThreadContextTokens 内部会加锁，必须在上面解锁之后调用。
	// 拿不到真实窗口上限时不显示：这里不退回累计用量，否则分母失真。
	used, limit, ok := currentThreadContextTokens(s, &currentThread)
	if !ok {
		return
	}
	if !shouldOfferForward(forwardSuggestionDismissed(s, threadID), running, used, limit, draft) {
		return
	}

	ui.Column(c.Key("chat-forward-offer")).FillWidth().Padding(Space3, Space7).AlignItems(ui.Center).Children(func() {
		ui.Column(c.Key("chat-forward-offer-card")).
			AlignItems(ui.End).
			Background(ThemeSurface(c)).
			Border(1, ThemeBorder(c)).
			Radius(RadiusXL).
			Padding(Space5).
			Gap(Space4).
			Children(func() {
				ui.Text(c, fmt.Sprintf("上下文已用 %d%%，可以转发到新会话继续。", int(math.Round(contextUsagePercent(used, limit))))).
					FontSize(TypeBody).
					TextColor(ThemeSecondaryText(c))

				ui.Row(c.Key("chat-forward-offer-actions")).Gap(Space3).Children(func() {
					Button(c.Key("chat-forward-offer-dismiss-btn"), ButtonOpts{
						Text:    "不用了",
						Variant: ButtonSecondary,
						Size:    ControlMD,
						OnClick: func() {
							dismissForwardSuggestion(s, threadID)
							s.invalidate()
						},
					})
					Button(c.Key("chat-forward-offer-accept-btn"), ButtonOpts{
						Text:    "转发到新会话",
						Variant: ButtonPrimary,
						Size:    ControlMD,
						OnClick: func() {
							// 先记下忽略：转发后新会话被选中，切回原会话时不该再弹这张卡片。
							dismissForwardSuggestion(s, threadID)
							s.forwardThread(currentThread.ProjectID, threadID)
						},
					})
				})
			})
	})
}

// currentThreadProject 返回指定会话所属项目的名称与 ID。
func currentThreadProject(s *chatState, threadID string) (name, id string) {
	if s == nil {
		return "", ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, th := range s.threads {
		if th.ID == threadID {
			id = th.ProjectID
			break
		}
	}
	if id == "" && threadID == "" && s.pendingThread != nil {
		// 待创建会话没有会话行：项目取自待创建状态。
		id = s.pendingThread.ProjectID
	}
	for _, p := range s.projects {
		if p.ID == id {
			name = p.Name
			break
		}
	}
	return name, id
}

// switchEmptyThreadProject 在新会话空态切换到指定项目：
// 优先切到该项目最近的会话，项目下还没有会话时在该项目中新建一个。
func switchEmptyThreadProject(s *chatState, projectID string) {
	if s == nil || projectID == "" {
		return
	}
	if us := getChatUIState(s); us != nil {
		us.projectPopoverOpen = false
	}
	s.mu.Lock()
	var target string
	if list := s.threadsByProj[projectID]; len(list) > 0 {
		target = list[0].ID
	}
	s.mu.Unlock()
	if target != "" {
		s.selectThread(target)
		return
	}
	s.createThread(projectID)
}

var (
	appIconOnce sync.Once
	appIconBmp  *ui.Bitmap
)

func appIconBitmap() *ui.Bitmap {
	appIconOnce.Do(func() {
		if len(resources.IconPNG) == 0 {
			return
		}
		img, _, err := image.Decode(bytes.NewReader(resources.IconPNG))
		if err != nil {
			return
		}
		appIconBmp = ui.NewBitmap(img)
	})
	return appIconBmp
}

// renderNewThreadPrompt 渲染新会话空态引导屏：
// 应用图标、大标语“在 {项目名} 中构建什么？”，项目名带下划线、悬停变色，点击弹出项目切换浮层。
// 总管会话不走项目那套说法：它不能切项目，标题换成名字，副标题说清它能做什么。
func renderNewThreadPrompt(c *ui.Context, s *chatState, selectedID string) {
	if stewardSelected(s, selectedID) {
		renderStewardEmptyPrompt(c, s)
		return
	}
	projectName, projectID := currentThreadProject(s, selectedID)

	ui.Column(c.Key("chat-new-thread-prompt")).
		AlignItems(ui.Center).
		Margin(0, 0, 22, 0).
		Children(func() {
			// 应用图标（约 72pt）；解码失败时回退为品牌图标
			if bmp := appIconBitmap(); bmp != nil {
				ui.Image(c, bmp).Size(72, 72)
			} else {
				ui.Icon(c, IconBoxes()).
					Size(72, 72).
					TextColor(ThemeSecondaryText(c).Alpha(0.55))
			}

			ui.Row(c.Key("chat-new-thread-question")).
				AlignItems(ui.Center).
				Margin(20, 0, 0, 0).
				Children(func() {
					if projectName == "" {
						ui.Text(c, "要构建什么？").
							FontSize(chatHeroFontSize).
							TextColor(ThemePrimaryText(c)).
							SingleLine()
						return
					}

					ui.Text(c, "在 ").
						FontSize(chatHeroFontSize).
						TextColor(ThemePrimaryText(c)).
						SingleLine()

					us := getChatUIState(s)
					trigger := ui.ButtonBase(c.Key("chat-new-thread-project-btn")).
						Label("切换项目").
						Padding(0)
					nameColor := ThemeSecondaryText(c)
					if trigger.Hovered() {
						nameColor = ThemeAccent(c)
					}
					trigger.Children(func() {
						ui.Text(c, projectName).
							FontSize(chatHeroFontSize).
							TextColor(nameColor).
							Underline().
							SingleLine()
					})
					if trigger.Clicked() {
						us.projectPopoverOpen = !us.projectPopoverOpen
						s.invalidate()
					}
					appPopover(c.Key("chat-new-thread-project-popover"), trigger, &us.projectPopoverOpen, func() {
						us.projectPopoverOpen = false
					}, func() {
						renderProjectChoicesPopover(c, s, projectID)
					})

					ui.Text(c, " 中构建什么？").
						FontSize(chatHeroFontSize).
						TextColor(ThemePrimaryText(c)).
						SingleLine()
				})
		})
}

// renderStewardEmptyPrompt 是空的总管会话的引导屏：「{名字}在这儿」加一行能力说明，
// 下面接 renderStewardStarterCapsules 的起步胶囊。
func renderStewardEmptyPrompt(c *ui.Context, s *chatState) {
	nickname, _ := s.stewardIdentity()

	ui.Column(c.Key("chat-steward-empty-prompt")).
		AlignItems(ui.Center).
		Margin(0, 0, 22, 0).
		Children(func() {
			// 应用图标（约 72pt）；解码失败时回退为品牌图标
			if bmp := appIconBitmap(); bmp != nil {
				ui.Image(c, bmp).Size(72, 72)
			} else {
				ui.Icon(c, IconBoxes()).
					Size(72, 72).
					TextColor(ThemeSecondaryText(c).Alpha(0.55))
			}

			ui.Text(c, stewardName(nickname)+"在这儿").
				FontSize(chatHeroFontSize).
				TextColor(ThemePrimaryText(c)).
				SingleLine().
				Margin(20, 0, 0, 0)

			ui.Text(c, "我能读代码、改代码、跑测试，也能替你盯着会话、看板和定时任务。").
				FontSize(TypeProse).
				TextColor(ThemeSecondaryText(c)).
				Wrap().
				MaxWidth(420).
				Margin(8, 0, 0, 0)
		})
}

// renderProjectChoicesPopover 渲染空态项目切换浮层：项目列表、添加项目与从 GitHub 克隆入口。
func renderProjectChoicesPopover(c *ui.Context, s *chatState, currentProjectID string) {
	s.mu.Lock()
	projects := make([]Project, len(s.projects))
	copy(projects, s.projects)
	s.mu.Unlock()
	// 总管主目录对应的项目只属于总管自己，不在切换项目浮层里露出；只过滤局部副本。
	projects = withoutStewardProject(s.stewardProjectID(), projects)

	popoverCard(c, ui.Column(c.Key("chat-new-thread-project-menu"))).
		Gap(Space0).
		Padding(Space1).
		MinWidth(200).
		Children(func() {
			ui.Text(c, "项目").
				FontSize(TypeCaption).
				TextColor(ThemeSecondaryText(c)).
				Padding(Space1, Space3)

			for _, p := range projects {
				project := p
				item := ui.ButtonBase(c.Key("chat-new-thread-project-item-"+project.ID)).
					Label("切换到项目 "+project.Name).
					Padding(Space2, Space3).
					Radius(RadiusSM).
					FillWidth()
				if item.Hovered() {
					item.Background(ThemeHover(c))
				}
				if item.Clicked() {
					switchEmptyThreadProject(s, project.ID)
				}
				item.Children(func() {
					ui.Row(c.Key("chat-new-thread-project-item-row-" + project.ID)).
						Gap(Space3).
						AlignItems(ui.Center).
						Grow(1).
						Children(func() {
							if project.ID == currentProjectID {
								ui.Icon(c, IconCheck()).Size(14, 14).TextColor(ThemeAccent(c))
							} else {
								ui.Box(c.Key("chat-new-thread-project-item-check-"+project.ID)).Size(14, 14)
							}
							ui.Text(c, project.Name).
								FontSize(TypeBody).
								TextColor(ThemePrimaryText(c)).
								SingleLine()
						})
				})
			}

			ui.Box(c.Key("chat-new-thread-project-divider")).
				Height(DividerWidth).
				FillWidth().
				Background(ThemeBorder(c)).
				Margin(4, 4)

			addBtn := ui.ButtonBase(c.Key("chat-new-thread-add-project-btn")).
				Label("添加项目…").
				Padding(Space2, Space3).
				Radius(RadiusSM).
				FillWidth()
			if addBtn.Hovered() {
				addBtn.Background(ThemeHover(c))
			}
			if addBtn.Clicked() {
				if us := getChatUIState(s); us != nil {
					us.projectPopoverOpen = false
				}
				s.promptAddProject()
			}
			addBtn.Children(func() {
				ui.Row(c.Key("chat-new-thread-add-project-row")).
					Gap(Space3).
					AlignItems(ui.Center).
					Children(func() {
						ui.Icon(c, IconPlus()).Size(14, 14).TextColor(ThemeSecondaryText(c))
						ui.Text(c, "添加项目…").
							FontSize(TypeBody).
							TextColor(ThemePrimaryText(c)).
							SingleLine()
					})
			})

			cloneBtn := ui.ButtonBase(c.Key("chat-new-thread-clone-github-btn")).
				Label("从 GitHub 克隆…").
				Padding(Space2, Space3).
				Radius(RadiusSM).
				FillWidth()
			if cloneBtn.Hovered() {
				cloneBtn.Background(ThemeHover(c))
			}
			if cloneBtn.Clicked() {
				if us := getChatUIState(s); us != nil {
					us.projectPopoverOpen = false
				}
				openGitHubClonePanel(s)
			}
			cloneBtn.Children(func() {
				ui.Row(c.Key("chat-new-thread-clone-github-row")).
					Gap(Space3).
					AlignItems(ui.Center).
					Children(func() {
						ui.Icon(c, IconGitFork()).Size(14, 14).TextColor(ThemeSecondaryText(c))
						ui.Text(c, "从 GitHub 克隆…").
							FontSize(TypeBody).
							TextColor(ThemePrimaryText(c)).
							SingleLine()
					})
			})
		})
}

// toolSummary 生成工具消息的单行摘要。
func toolSummary(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "工具调用"
	}
	if idx := strings.IndexByte(text, '\n'); idx != -1 {
		text = text[:idx]
	}
	runes := []rune(text)
	if len(runes) > 60 {
		return string(runes[:60]) + "..."
	}
	return text
}

// formatTokens 将 token 数量格式化为短字符串。
// 数值达到或超过 1000 时用 k 表示并保留一位小数。
func formatTokens(n int64) string {
	if n >= 1000 || n <= -1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000.0)
	}
	return strconv.FormatInt(n, 10)
}

// formatTurnUsageText 格式化单轮用量提示文本。
func formatTurnUsageText(u TokenUsage) string {
	if u.Input == 0 && u.Output == 0 && u.Cached == 0 {
		return ""
	}
	if u.Cached > 0 {
		return fmt.Sprintf("输入 %s · 输出 %s · 缓存 %s tokens", formatTokens(u.Input), formatTokens(u.Output), formatTokens(u.Cached))
	}
	return fmt.Sprintf("输入 %s · 输出 %s tokens", formatTokens(u.Input), formatTokens(u.Output))
}

// jumpToAdjacentUserMessage 根据视口顶部可见项定位上一个或下一个用户消息所在的 item，并用 ScrollTo 定位。
// direction < 0 为跳到上一个用户消息，direction > 0 为跳到下一个用户消息。
// 定位走 scrollToTimelineRow：目标被收进回合折叠项或工具组时先展开再滚动，
// 折叠后按 items[i].msg.ID 直接比较会漏掉那些落在组内或折叠区内的消息。
func jumpToAdjacentUserMessage(s *chatState, items []timelineItem, direction int) {
	if s == nil || len(items) == 0 {
		return
	}
	first, _ := s.chatListState.Visible()
	if direction < 0 {
		for i := first - 1; i >= 0; i-- {
			if i < len(items) && items[i].kind == timelineItemSingle && items[i].msg.Role == "user" {
				scrollToTimelineRow(s, items, i)
				return
			}
		}
	} else if direction > 0 {
		for i := first + 1; i < len(items); i++ {
			if items[i].kind == timelineItemSingle && items[i].msg.Role == "user" {
				scrollToTimelineRow(s, items, i)
				return
			}
		}
	}
}

// ensureChatMarkdown 按外观设置初始化或更新会话 Markdown 渲染配置；主会话与浮动面板里嵌入的会话共用。
func ensureChatMarkdown(c *ui.Context, s *chatState) {
	// Markdown 视觉配置（保持实例稳定避免每帧 clear 段落缓存）
	var appCfg config.AppearanceConfig
	if st := getChatSettings(s); st != nil {
		appCfg = st.cfg.Appearance
	}
	wantedCodeFont := codeFontFamily(appCfg)
	wantedCodeSize := codeFontSize(appCfg) / 13.0 // 13 为基础字号，CodeSize 为 em 单位
	// 助手正文的字号跟随「文字大小」；标题与代码块按 em 相对字号，自然一起缩放。
	wantedTextSize := scaledFont(13)

	if s.mdOpts.Theme == nil || s.mdOpts.Theme.Text != ThemePrimaryText(c) {
		mdTheme := buildMarkdownTheme(c, wantedTextSize, wantedCodeFont, wantedCodeSize)
		s.mdOpts.Theme = mdTheme
		s.mdStaticOpts.Theme = mdTheme
	} else {
		if s.mdOpts.Theme.CodeFont != wantedCodeFont || s.mdOpts.Theme.CodeSize != wantedCodeSize || s.mdOpts.Theme.FontSize != wantedTextSize {
			s.mdOpts.Theme.CodeFont = wantedCodeFont
			s.mdOpts.Theme.CodeSize = wantedCodeSize
			s.mdStaticOpts.Theme.CodeFont = wantedCodeFont
			s.mdStaticOpts.Theme.CodeSize = wantedCodeSize
			s.mdOpts.Theme.FontSize = wantedTextSize
			s.mdStaticOpts.Theme.FontSize = wantedTextSize
		}
	}

	if reduceMotion(c) {
		if s.mdOpts.Reveal == nil || s.mdOpts.Reveal.Unit != markdown.AtOnce {
			s.mdOpts.Reveal = &markdown.Reveal{Unit: markdown.AtOnce, Fade: -1}
		}
	} else if s.mdOpts.Reveal == nil || s.mdOpts.Reveal.Unit != markdown.ByCharacter {
		s.mdOpts.Reveal = &markdown.Reveal{Unit: markdown.ByCharacter, CatchUp: 250 * time.Millisecond}
	}
	if s.mdStaticOpts.Reveal == nil {
		s.mdStaticOpts.Reveal = &markdown.Reveal{Unit: markdown.AtOnce, Fade: -1}
	}
}

func buildMarkdownTheme(c *ui.Context, textSize float32, codeFont string, codeSize float32) *markdown.Theme {
	mdTheme := markdown.DefaultTheme(c.Theme())
	mdTheme.FontSize = textSize
	mdTheme.LineHeight = 1.45
	mdTheme.Text = ThemePrimaryText(c)
	mdTheme.Muted = ThemeSecondaryText(c)
	mdTheme.Link = ThemeAccent(c)
	mdTheme.CodeFont = codeFont
	mdTheme.CodeSize = codeSize
	mdTheme.CodeBlockRadius = 12
	mdTheme.CodeBlockBackground = ThemeSurface(c)
	mdTheme.CodeBlockBorder = ThemeBorder(c)
	mdTheme.QuoteBarWidth = 3
	mdTheme.QuoteBar = ThemeBorder(c)
	mdTheme.QuoteText = ThemeSecondaryText(c)
	mdTheme.Headings = [6]markdown.HeadingStyle{
		{Size: 1.30, Weight: 600, Top: 0.46, Bottom: 0.30}, // H1 17pt
		{Size: 1.15, Weight: 600, Top: 0.46, Bottom: 0.30}, // H2 15pt
		{Size: 1.00, Weight: 600, Top: 0.20, Bottom: 0.20}, // H3 13pt
		{Size: 0.85, Weight: 600, Top: 0.15, Bottom: 0.15}, // H4 11pt
		{Size: 0.85, Weight: 600, Top: 0.15, Bottom: 0.15}, // H5 11pt
		{Size: 0.85, Weight: 600, Top: 0.15, Bottom: 0.15}, // H6 11pt
	}
	return mdTheme
}
