package mcodeui

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// composerViewState 维护 Composer 的轻量 UI 视图状态（仅在 UI 线程访问）。
type composerViewState struct {
	planPillOpen              bool                 // 待办进度胶囊的弹层是否打开
	backgroundTasksOpen       bool                 // 后台任务胶囊的浮层是否打开
	backgroundTaskExpanded    string               // 当前展开的消息 ID。
	backgroundTaskThread      string               // 后台任务浮层所属会话。
	backgroundTaskLog         string               // 最近一次获取的日志。
	backgroundTaskLogID       string               // 日志所属消息 ID。
	backgroundTaskLogPending  bool                 // 是否有日志请求在途。
	backgroundTaskLogNext     time.Time            // 下一次日志请求时间。
	backgroundTaskGeneration  uint64               // 展开状态变更时递增，用于丢弃过期回包。
	backgroundTaskStopping    map[string]bool      // 正在停止的消息。
	backgroundTaskStopped     map[string]time.Time // 已停止消息的动画起始时间。
	backgroundTaskScroll      ui.ScrollState       // 日志滚动位置。
	backgroundTaskScrollOwner string               // 当前或最后展开的日志所属消息。
	backgroundTaskScrolledLog string               // 上次滚动到底部时的日志。
	downloadsOpen             bool
	usageOpen                 bool
	quotaOpen                 bool                          // 额度弹层是否打开
	quotaPopoverProvider      string                        // 额度弹层当前锚定的 provider（关闭时清空）
	history                   composerHistoryState          // 方向键历史导航状态
	lastThreadID              string                        // 上一帧的选中会话，用于切换会话时重置历史导航
	dockThreadID              string                        // 上一帧 Hero/Docked 判定所属的会话
	dockDocked                bool                          // 上一帧是否停靠（仅当 dockThreadID 与当前会话一致时有效）
	quotaLastSeen             map[string]quotaUsageSnapshot // 按 provider+账户记录上一次观察到的已用百分比
	quotaChangedAt            map[string]time.Time          // 按账户记录额度最近一次变动的时间
	activeQuotaAccount        map[string]string             // 后端 Active 标记给出的在用账户（按 provider）
	// sendIconState 是发送按钮上一帧的图标状态，sendIconPrevState 是切换前的状态：
	// 两者不同且淡入进度未走完时，旧图标继续淡出、新图标淡入。
	sendIconState     sendDraftState
	sendIconPrevState sendDraftState
	// mcpMenuOpen 表示附件浮层里已进入「MCP 服务器」二级页；切会话时重置。
	mcpMenuOpen bool
	// mcpPendingDisabled 是 MCP 二级页里待提交的会话级禁用集合（键为 mcp.EndpointKey）；
	// 点「应用」才下发，未提交前不改会话。
	mcpPendingDisabled map[string]bool
	// mcpSaving 表示会话 MCP 设置正在下发，期间置灰「应用」避免重复提交。
	mcpSaving bool
}

var (
	iconSteer      = parseLucideSVG(`<polyline points="15 10 20 15 15 20"/><path d="M4 4v7a4 4 0 0 0 4 4h12"/>`)
	iconPlayFilled = parseLucideSVG(`<polygon points="6 3 20 12 6 21 6 3" fill="currentColor" stroke="none"/>`)
)

func composerKeyPrefix(s *chatState, threadID string) string {
	if s.embeddedBee {
		return "bee-" + threadID + "-composer-"
	}
	return "composer-"
}

// renderComposerTabSlot 渲染输入框上方的悬浮 Tab 槽位。
// 有排队消息时优先显示排队 Tab，否则显示当前会话的改动统计 Tab。
// 只有真正渲染了改动统计行时才把 capsules 传进去并返回 true，表示状态胶囊已并入那一行；
// 其余路径返回 false，由调用方另行渲染胶囊行。
// composerHoverBg 是输入框底部控件统一的悬停/展开反馈：指针悬停或面板已展开时
// 叠加 ThemeHover 提高背景亮度，否则透明（保留调用方已有的底色时传 active=true）。
// 只改背景、不动内边距与尺寸，因此悬停切换不会带来布局抖动。
func composerHoverBg(c *ui.Context, btn ui.Element, active bool) {
	var bg ui.Color
	if active {
		bg = ThemeHover(c)
	} else {
		bg = ui.Transparent
	}
	btn.Background(bg)
}

func renderComposerTabSlot(c *ui.Context, s *chatState, threadID string, items []QueuedFollowUp, isRunning bool, capsules []composerCapsule) bool {
	sortableScope := fmt.Sprintf("%p/%s/queue", s, threadID)
	// 浮动面板里嵌入的 composer 不显示改动统计标签，只保留排队消息。
	if !s.embeddedBee {
		s.ensureThreadChangeStats(threadID, isRunning)
	}

	if len(items) == 0 {
		if s.embeddedBee {
			return false
		}
		// 只要变更文件数大于 0 就展示胶囊，不要求有增删行（重命名、纯权限变更等也会改文件）。
		if add, del, files, ok := threadChangeStatsFor(s, threadID); ok && files > 0 {
			renderThreadChangesTab(c, s, add, del, files, capsules)
			return true
		}
		return false
	}

	isEditing := s.editingQueuedID() != ""
	// 插入按钮只在 provider 明确支持 steer 时显示；provider 尚未确定（空串）时保持原行为，
	// 点击后由 steerQueued 再给出明确提示。
	provider := s.queuedThreadProvider(threadID)
	canSteer := provider == "" || providerSupportsSteer(provider)

	ui.Column(c.Key("composer-tab-slot")).Gap(Space2).Children(func() {
		// D-14：排队区默认折叠成 28pt 单行——数量 + 首条 12pt 摘要 + 展开箭头。
		// 展开状态按会话记住，点这一行切换。
		expanded := queueExpandedFor(s, threadID)
		tabText := fmt.Sprintf("排队 %d", len(items))
		tabBtn := ui.ButtonBase(c.Key("composer-queue-tab")).
			Label(tabText).
			Tooltip("排队消息").
			Height(ControlMD).
			Radius(RadiusXL).
			Padding(Space1, Space3).
			Border(Hairline, ThemeBorder(c)).
			OnClick(func() {
				toggleQueueExpanded(s, threadID)
			})
		// 排队胶囊本身已有浅底，悬停或展开时再提亮一级。
		composerHoverBg(c, tabBtn, tabBtn.Hovered() || expanded)

		chevronRotation := float32(0)
		if expanded {
			chevronRotation = 180
		}

		tabBtn.Children(func() {
			ui.Row(c.Key("composer-queue-tab-content")).
				Gap(Space2).
				AlignItems(ui.Center).
				Children(func() {
					ui.Icon(c, iconClock).
						Size(IconSM, IconSM).
						TextColor(ThemeSecondaryText(c))
					ui.Text(c, tabText).
						FontSize(uiFont(TypeFootnote)).
						TextColor(ThemePrimaryText(c))
					if summary := queueCollapsedSummary(items); summary != "" {
						ui.Text(c, "·").
							FontSize(uiFont(TypeFootnote)).
							TextColor(ThemeSecondaryText(c))
						ui.Text(c, summary).
							FontSize(uiFont(TypeFootnote)).
							MaxLines(1).
							TextColor(ThemeSecondaryText(c))
					}
					ui.Icon(c, iconChevronDown).
						Size(IconSM, IconSM).
						Rotate(chevronRotation).
						TextColor(ThemeSecondaryText(c))
				})
		})

		// 展开时渲染排队消息抽屉面板
		if expanded {
			ui.Column(c.Key("composer-queue-panel")).
				Background(ThemeSurface(c)).
				Border(Hairline, ThemeBorder(c)).
				Radius(RadiusXL).
				Padding(Space3, Space4, Space3, Space4).
				Gap(Space3).
				Children(func() {
					// 顶部操作栏与编辑提示
					ui.Row(c.Key("composer-queue-panel-header")).
						AlignItems(ui.Center).
						Justify(ui.SpaceBetween).
						Children(func() {
							ui.Row(c.Key("composer-queue-header-left")).
								Gap(Space3).
								AlignItems(ui.Center).
								Children(func() {
									ui.Text(c, "排队队列").
										FontSize(uiFont(TypeBody)).
										TextColor(ThemePrimaryText(c))
									if isEditing {
										ui.Text(c, "正在编辑排队消息 · Esc 取消").
											FontSize(uiFont(TypeCaption)).
											TextColor(ThemeAccent(c))
									}
								})

							ui.Row(c.Key("composer-queue-header-actions")).
								Gap(Space3).
								AlignItems(ui.Center).
								Children(func() {
									ui.ButtonBase(c.Key("composer-queue-clear-all")).
										Label("清空全部").
										Tooltip("清空全部").
										Height(ControlXS).
										Radius(RadiusPill).
										Padding(0, Space3).
										Background(ThemeHover(c)).
										Border(Hairline, ThemeBorder(c)).
										OnClick(func() {
											s.clearQueue()
										}).
										Children(func() {
											ui.Text(c, "清空全部").
												FontSize(uiFont(TypeCaption)).
												TextColor(ThemeSecondaryText(c))
										})

									// 正在编辑排队项时不提供全部退回，避免编辑态与草稿合并互相覆盖。
									if !isEditing {
										ui.ButtonBase(c.Key("composer-queue-recall-all")).
											Label("全部退回编辑").
											Tooltip("把排队消息按顺序放回输入框").
											Height(ControlXS).
											Radius(RadiusPill).
											Padding(0, Space3).
											Background(ThemeHover(c)).
											Border(Hairline, ThemeBorder(c)).
											OnClick(func() {
												s.recallAllQueued()
											}).
											Children(func() {
												ui.Text(c, "全部退回编辑").
													FontSize(uiFont(TypeCaption)).
													TextColor(ThemeSecondaryText(c))
											})
									}
								})
						})

					// 排队项列表：展开后最多约 3 行高（132pt）就滚动。
					ui.Scroll(c.Key("composer-queue-items-scroll")).
						MaxHeight(composerQueueScrollMaxHeight).
						Children(func() {
							queueList := ui.Column(c.Key("composer-queue")).Gap(Space2)
							// 拖到行与行之间的空隙时，按最近一次悬停的落点重排。
							if _, draggedID := SortableTarget(queueList, sortableScope, ""); draggedID != "" {
								if before := queueDropBeforeFor(s, draggedID); before != "" && before != draggedID {
									s.moveQueued(draggedID, before)
									setQueueDropBefore(s, draggedID, "")
								}
							}
							queueList.Children(func() {
								for _, item := range items {
									item := item
									isCurEditing := isEditing && s.editingQueuedID() == item.ID

									row := ui.Row(c.Key("composer-queue-"+item.ID)).
										AlignItems(ui.Center).
										Background(ThemeHover(c)).
										Radius(RadiusMD).
										Padding(Space2, Space3, Space2, Space3).
										Gap(Space3)

									if isCurEditing {
										row.Background(ThemeAccentSubtle(c))
									}
									// 拖拽排队顺序：只有行首手柄是拖拽源（Drag 挂手柄），
									// DragOver/Drop 仍挂在整行上，落点画成目标行上沿的 2pt 插入线。
									draggedID, droppedID := SortableTarget(row, sortableScope, item.ID)
									dropTarget := draggedID != ""
									if dropTarget {
										setQueueDropBefore(s, draggedID, item.ID)
									}
									if droppedID != "" {
										s.moveQueued(droppedID, item.ID)
										setQueueDropBefore(s, droppedID, "")
									}

									row.Children(func() {
										if dropTarget {
											renderQueueDropLine(c, item.ID)
										}

										// 行首六点手柄：唯一可拖拽的部分。
										renderQueueGrip(c, s, item, sortableScope)

										// 意图标签：区分普通排队与「本意插入但被降级成排队」。
										if label := queuedIntentLabel(item); label != "" {
											labelColor := ThemeSecondaryText(c)
											if item.Intent == "insert" {
												labelColor = ThemeWarning(c)
											}
											ui.Row(c.Key("composer-queue-intent-"+item.ID)).
												Background(ThemeSurface(c)).
												Radius(RadiusSM).
												Padding(Space0, Space2, Space0, Space2).
												Children(func() {
													ui.Text(c, label).
														FontSize(uiFont(TypeCaption)).
														TextColor(labelColor)
												})
										}

										// 目标标签
										ui.Row(c.Key("composer-queue-target-"+item.ID)).
											Background(ThemeSurface(c)).
											Radius(RadiusSM).
											Padding(Space0, Space2, Space0, Space2).
											Children(func() {
												ui.Text(c, "主代理").
													FontSize(uiFont(TypeCaption)).
													TextColor(ThemeSecondaryText(c))
											})

										// 系统条目（priority 汇报/提问/通知）只读：显示标签，不提供编辑与删除。
										if item.System {
											ui.Row(c.Key("composer-queue-system-"+item.ID)).
												Background(ThemeSurface(c)).
												Radius(RadiusSM).
												Padding(Space0, Space2, Space0, Space2).
												Children(func() {
													ui.Text(c, "系统消息").
														FontSize(uiFont(TypeCaption)).
														TextColor(ThemeSecondaryText(c))
												})
										}

										// 附件缩略图：排队数据只有数量、没有附件字节，用 20x20 占位块替代「N 个附件」文字。
										if item.AttachmentCount > 0 {
											renderQueueAttachmentThumb(c, item)
										}

										// 消息文本摘要
										ui.Text(c, item.Text).
											TextColor(ThemePrimaryText(c)).
											FontSize(uiFont(TypeFootnote)).
											MaxLines(1).
											Grow(1)

										ui.Spacer(c)

										// 编辑按钮：系统条目只读，不提供。
										if !item.System {
											ui.ButtonBase(c.Key("composer-queue-edit-"+item.ID)).
												Label("编辑").
												Tooltip("编辑").
												Height(ControlXS).
												Radius(RadiusPill).
												Padding(0, Space3).
												Background(ThemeSurface(c)).
												Border(Hairline, ThemeBorder(c)).
												OnClick(func() {
													s.beginEditQueued(item.ID)
												}).
												Children(func() {
													ui.Text(c, "编辑").
														FontSize(uiFont(TypeCaption)).
														TextColor(ThemePrimaryText(c))
												})
										}

										// 插入按钮：把这条排队消息插进当前轮次（不打断正在执行的回复）。
										// 带附件的排队项后端无法插入当前轮；系统条目不可撤回，插入流程也无从进行。
										// 已被降级为排队的项（Intent=insert）不再提供插入，避免重复点击。
										if !item.System && item.Intent != "insert" && canSteer && isRunning && item.AttachmentCount == 0 {
											ui.ButtonBase(c.Key("composer-queue-steer-"+item.ID)).
												Label("插入到当前轮次 "+item.Text).
												Tooltip("把这条排队消息插入当前轮次").
												Height(ControlXS).
												Radius(RadiusPill).
												Padding(0, Space3).
												Background(ThemeSurface(c)).
												Border(Hairline, ThemeBorder(c)).
												OnClick(func() {
													s.steerQueued(item)
												}).
												Children(func() {
													ui.Text(c, "插入").
														FontSize(uiFont(TypeCaption)).
														TextColor(ThemeAccent(c))
												})
										}

										// 立即发送按钮
										ui.ButtonBase(c.Key("composer-queue-send-"+item.ID)).
											Label("立即发送").
											Tooltip("立即发送").
											Height(ControlXS).
											Radius(RadiusPill).
											Padding(0, Space3).
											Background(ThemeSurface(c)).
											Border(Hairline, ThemeBorder(c)).
											OnClick(func() {
												s.sendQueuedNow(item.ID)
											}).
											Children(func() {
												ui.Text(c, "立即发送").
													FontSize(uiFont(TypeCaption)).
													TextColor(ThemePrimaryText(c))
											})

										// 删除按钮：系统条目只读，不提供。
										if !item.System {
											ui.ButtonBase(c.Key("composer-queue-remove-"+item.ID)).
												Label("删除").
												Tooltip("删除").
												Height(ControlXS).
												Radius(RadiusPill).
												Padding(0, Space3).
												Background(ThemeSurface(c)).
												Border(Hairline, ThemeBorder(c)).
												OnClick(func() {
													s.removeQueued(item.ID)
												}).
												Children(func() {
													ui.Text(c, "删除").
														FontSize(uiFont(TypeCaption)).
														TextColor(ThemeSecondaryText(c))
												})
										}
									})
								}
							})
						})
				})
		}
	})
	return false
}

// sendDraft 发送草稿前优先拦截并本地执行斜杠命令。
func (s *chatState) sendDraft(threadID, text string) bool {
	if s.runLocalSlashCommand(threadID, text) {
		return true
	}
	s.sendMessage(text)
	return false
}

// renderDraftThreadMentions 渲染草稿上方的会话引用胶囊行：
// 每颗胶囊是「@会话标题」加一个 ✕ 移除按钮，点击 ✕ 把该会话从草稿引用里去掉。
func renderDraftThreadMentions(c *ui.Context, s *chatState, mentions []ThreadMention) {
	if len(mentions) == 0 {
		return
	}
	ui.Row(c.Key("composer-mention-row")).
		Gap(Space2).
		Wrap().
		AlignItems(ui.Center).
		Padding(0, 0, Space0, 0).
		Children(func() {
			for _, m := range mentions {
				mention := m
				title := mention.Title
				if title == "" {
					title = "未命名会话"
				}
				ui.Row(c.Key("composer-mention-"+mention.ThreadID)).
					Gap(Space1).
					Padding(Space0, Space3, Space0, Space1).
					Radius(RadiusLG).
					Background(ThemeHover(c)).
					Border(Hairline, ThemeBorder(c)).
					AlignItems(ui.Center).
					Children(func() {
						ui.Text(c, "💬").FontSize(uiFont(TypeCaption))
						ui.Text(c, draftThreadMentionLabel(title)).
							FontSize(uiFont(TypeFootnote)).
							TextColor(ThemePrimaryText(c))
						ui.ButtonBase(c.Key("composer-mention-remove-"+mention.ThreadID)).
							Label("移除引用 "+title).
							Tooltip("移除引用").
							Size(IconMD, IconMD).
							Radius(RadiusMD).
							Background(ui.Transparent).
							OnClick(func() {
								s.removeDraftThreadMention(mention.ThreadID)
							}).
							Children(func() {
								ui.Text(c, "✕").
									FontSize(uiFont(TypeCaption)).
									TextColor(ThemeSecondaryText(c))
							})
					})
			}
		})
}

// draftThreadMentionLabel 生成胶囊文案「@会话标题」，过长的标题截断到 24 个字符。
func draftThreadMentionLabel(title string) string {
	runes := []rune(title)
	if len(runes) > 24 {
		return "@" + string(runes[:24]) + "…"
	}
	return "@" + title
}

// 输入卡片尺寸常量。
const (
	composerTextMinHeight float32 = 44 // token-exempt: 输入区规定的最小高度
	composerActionBtnSize float32 = ControlMD
	composerControlGap            = Space2
	// 右下角控件（模式切换、上下文圆环、发送）共用的图标视觉尺寸
	composerControlIconSize       float32 = IconMD
	composerContextRingDiameter           = composerControlIconSize
	composerContextMeterImageSize         = composerContextRingDiameter * 24 / 20.5 // SVG 外径 20.5，画布宽 24
	composerDockPadBottom         float32 = 10                                      // 有消息时卡片距窗口底部
	composerEmbeddedPadBottom     float32 = 8                                       // 浮动面板/子代理窗口内的输入框底距
	// composerQueueScrollMaxHeight 是排队列表展开后的滚动高度，约 3 行。
	composerQueueScrollMaxHeight float32 = 132
)

func composerShellRadii(hero bool) (float32, float32) {
	if hero {
		return RadiusXL, RadiusMD
	}
	return RadiusLG, RadiusSM
}

func composerCardMaxHeight(viewportHeight float32) float32 {
	return min(viewportHeight*0.4, 384) // token-exempt: 设计规定的视口比例和高度上限
}

// composerSendIconFadeKey 是发送按钮图标淡入进度在元素动画通道里的键。
const composerSendIconFadeKey = "composer-send-icon-fade"

// renderComposer 渲染底部多行输入框与发送控制。
func renderComposer(c *ui.Context, s *chatState) {
	s.mu.Lock()
	if s.sentDraft != nil {
		if strings.TrimSpace(s.draft) == *s.sentDraft {
			s.draft = ""
			// 引用胶囊随草稿一起清空（发送/入队后才会走到这里）。
			s.draftThreadMentions = nil
			// 草稿随发送清空，退出方向键历史导航
			s.composerView.history.reset()
		}
		s.sentDraft = nil
	}
	if fill, ok := s.consumePendingDraftFillLocked(); ok {
		s.draft = fill
	}
	selectedID := s.selectedThreadID
	// s.messages 只含当前会话，这里算出本帧方向键历史导航的条目
	historyEntries := s.composerHistoryEntriesCachedLocked()
	var disabledReason string
	var isRunning bool
	var currentThreadID string
	var currentThread *Thread
	for _, th := range s.threads {
		if th.ID == s.selectedThreadID {
			t := th
			currentThread = &t
			currentThreadID = t.ID
			// SwarmBusy 为真说明主会话本体空闲、但名下的子线程还在跑：这一轮没结束，输入区仍按忙处理。
			if th.IsRunning || th.SwarmBusy {
				isRunning = true
			}
			break
		}
	}
	if currentThreadID == "" {
		currentThreadID = s.selectedThreadID
	}
	pendingActive := false
	if currentThread == nil && s.selectedThreadID == "" {
		// 待创建会话：用本地占位会话渲染模型/强度控件，首次输入或发送时才真正建会话。
		if th, ok := s.pendingComposerThreadLocked(); ok {
			currentThread = th
			pendingActive = true
		}
	}
	// 有待处理审批（th.HasPendingRequest 或 len(s.approvals)>0）时无论是否运行都保持禁用输入框。
	// 这是 ⌘Return 不冲突的前提：禁用状态下输入框不占用焦点（s.composerFocused 为 false），
	// 窗口级快捷键 ⌘Return 才能直接批准待处理审批。
	if (currentThread != nil && currentThread.HasPendingRequest) || len(s.approvals) > 0 {
		disabledReason = "存在待处理的审批请求"
	}
	var queueItems []QueuedFollowUp
	if currentThreadID != "" && s.queue != nil {
		queueItems = append([]QueuedFollowUp(nil), s.queue[currentThreadID]...)
	}
	continueState := s.continuePillSnapshotLocked(currentThreadID)
	attachmentCount := len(s.attachments)
	hasMessages := len(s.messages) > 0
	// 浮动面板里的子代理会话早已存在，模型选择器按「有消息」锁定 provider，不走新会话的宽松态。
	if s.embeddedBee {
		hasMessages = true
	}
	// 停靠判定与 chat.go 的 hero 判定共用 composerThreadEmpty：消息、已裁决计划、已答复提问都为空才算 Hero。
	// 已答复提问这里取原始条数，chat.go 那边过了一道「已在消息里出现」的过滤；
	// 过滤只在消息非空时才会丢掉条目，两种口径的结论不会分叉。
	empty := composerThreadEmpty(
		len(s.messages),
		len(s.resolvedPlans[currentThreadID]),
		len(s.answeredQuestions[currentThreadID]),
	)
	dockDec := s.composerDockDecisionFor(empty, currentThreadID)
	docked, promote := dockDec.Docked, dockDec.Promote
	promoteArmed := s.composerDockPromoArmed(currentThreadID)
	providers := append([]ProviderInfo(nil), s.providers...)
	// 引用胶囊快照：放到卡片顶部渲染，避免在子元素闭包里再次加锁。
	draftMentions := append([]ThreadMention(nil), s.draftThreadMentions...)
	newThreadProvider := s.newThreadProvider
	stopping := s.stopping
	sending := s.sending
	prefsSaving := s.composerPrefsSaving[currentThreadID]
	focusRequested := s.composerFocusRequest
	s.composerFocusRequest = false
	// 消息已推回（会话不再为空）时撤掉透视标记：之后不靠标记停靠，只靠真实内容。
	// renderComposer 每帧跑在 UI 线程，这里改标记是安全的。
	if !empty && promoteArmed {
		s.disarmComposerDockPromote(currentThreadID)
	}

	hasUploadedAttachment := false
	hasUploadingAttachment := false
	for _, att := range s.attachments {
		if att.Uploading || att.Err != "" || att.ID == "" {
			hasUploadingAttachment = true
		}
		if att.ID != "" {
			hasUploadedAttachment = true
		}
	}
	// 粘贴卡片和已上传附件一样算作可发送的内容。
	hasUploadedAttachment = hasUploadedAttachment || len(s.snippets) > 0
	s.mu.Unlock()

	// 切换会话时退出历史导航，避免把上一个会话的输入回填到新会话
	if s.composerView.lastThreadID != selectedID {
		s.composerView.lastThreadID = selectedID
		s.composerView.history.reset()
		// 待办弹层属于上一个会话，切换时收起。
		s.composerView.planPillOpen = false
		// MCP 二级页属于上一个会话的服务列表，切换时回到附件浮层一级页。
		s.composerView.mcpMenuOpen = false
		s.composerView.mcpPendingDisabled = nil
		// 引用胶囊不跟草稿一起持久化，切会话时清空，避免把上一个会话的引用带到新会话。
		s.mu.Lock()
		s.draftThreadMentions = nil
		s.mu.Unlock()
		draftMentions = nil
	}

	// 窗口级快捷键只交给一个 composer：嵌入的子代理输入框聚焦时归它，否则归主 composer。
	shortcutsOwned := composerOwnsShortcuts(s)
	// 运行中注册 ⌘. 快捷键中断当前会话
	if isRunning && shortcutsOwned {
		if c.Shortcut(ui.Cmd, ui.KeyPeriod) {
			s.interruptCurrent()
		}
	}
	// 快捷键 ⌘⇧M 与 ⇧⌘P 切换 Build/Plan 模式
	if shortcutsOwned && (c.Shortcut(ui.Cmd|ui.Shift, ui.KeyM) || c.Shortcut(ui.Cmd|ui.Shift, ui.KeyP)) {
		s.toggleInteractionMode()
	}

	trimmedDraft := strings.TrimSpace(s.draft)
	hasContent := (trimmedDraft != "" || hasUploadedAttachment)
	isEditingQueued := s.editingQueuedID() != ""
	isSteward := currentThreadID != "" && currentThreadID == s.stewardThreadID()
	canShowContinue, continueKind := shouldShowContinuePill(continueState)
	// 有附件时继续入口也不出现：继续请求只带提示词，附件会丢。
	if attachmentCount > 0 {
		canShowContinue = false
	}
	canContinue := canShowContinue
	// 发送键由设置决定：默认 Enter 发送，也可切成 Shift+Enter 发送。
	sendKey := s.composerSendKey()
	sendState := runningSendDraftState(isRunning, hasContent, canContinue, s.queueWhileRunningEnabled(), s.insertIntoRunningTurnEnabled(), isEditingQueued)
	if isSteward && sendState == sendStateStop {
		sendState = sendStateSend
	}
	// 占位符只在草稿为空时显示，而此时 sendState 多为 sendStateStop；
	// 这里按「有内容」再算一次，让运行中的占位符说明按下 Enter 后实际会执行的动作。
	runningPlaceholderState := runningSendDraftState(isRunning, true, canContinue, s.queueWhileRunningEnabled(), s.insertIntoRunningTurnEnabled(), isEditingQueued)

	isSendingDisabled := disabledReason != "" || sending || (currentThreadID == "" && !pendingActive) || prefsSaving
	interactionMode := "build"
	if currentThread != nil {
		interactionMode = normalizeInteractionMode(currentThread.InteractionMode)
	}

	// 底部间距以 250ms ease-out 平滑过渡。
	// 输入框宽度上限固定为 composerDockMaxWidth，不随「内容宽度」设置变宽。
	dockWidth := composerDockMaxWidth
	dockPadBottom := composerDockPadBottom
	if !docked {
		dockPadBottom = 0
	}
	cardRadius, buttonRadius := composerShellRadii(!docked)
	_, viewportHeight := c.Size()
	textMaxHeight := max(composerTextMinHeight, composerCardMaxHeight(viewportHeight)-ControlMD-Space2)
	const composerDockDuration = 250 * time.Millisecond
	// 卡片最小高度的收缩时长与 composerDockTravelAnimation 的位移时长（420ms）对齐：
	// 两者同时起止，卡片在滑动过程中从 120 平滑收到 0。
	const composerCardMinHDuration = 420 * time.Millisecond
	const composerCardHeroMinHeight float32 = 120

	// Hero 和停靠模式均以标准宽度居中。
	container := ui.Row(c.Key("composer-container")).FillWidth().Justify(ui.Center)
	if s.embeddedBee {
		// 浮动面板里的输入框填满面板宽度，没有 Hero 与停靠的过渡。
		container.Padding(0, Space3, composerEmbeddedPadBottom, Space3)
	} else {
		padBottom := container.AnimateWith("composer-dock-pad", dockPadBottom, animateDuration(c, composerDockDuration), ui.EaseOut)
		if promote {
			// 切换帧把底距直接钉到终值：位移的目标 y 全程稳定，不被 250ms 的边距动画反复改目标。
			padBottom = container.Jump("composer-dock-pad", dockPadBottom)
		}
		container.Padding(0, Space6, padBottom, Space6)
		// 记下输入区域的高度：窄屏时浮动面板要停到它上方。
		if h := container.Bounds().H; h > 0 {
			s.mu.Lock()
			s.composerAreaH = h
			s.mu.Unlock()
		}
	}
	// 过渡注册：
	// 1. 切换帧与动画续帧注册「只跟位置」的过渡，让 mygo 的同父 FLIP 把输入框从居中位置插值到停靠位置；
	// 2. Hero 的每一帧注册一个只跟颜色（不碰位置/尺寸）的过渡，作用仅仅是让引擎的 r.target 每帧都
	//    记得居中位置——元素上一帧没注册过过渡时算 fresh，会直接瞬移到目标，那样子就没有位移可见。
	//    Hero 帧不注册位置，所以多行文本让卡片变高时不会被弹簧拖着走。
	// 3. 其余停靠帧不注册：切会话、打开已有消息的会话、第二条以后的消息都不会误播位移。
	if !s.embeddedBee {
		switch {
		case promote || container.Transitioning():
			container.Transition(motionTransition(c, ui.ElementTransition{
				Position:  true,
				Animation: &composerDockTravelAnimation,
			}))
		case !docked:
			container.Transition(motionFeedback(c))
		}
	}
	// 记住本帧布局，供下一帧判断「是不是切换帧」。
	s.composerView.dockThreadID = currentThreadID
	s.composerView.dockDocked = docked
	container.Children(func() {
		outerCol := ui.Column(c.Key("composer-outer-col")).Gap(Space3)
		if s.embeddedBee {
			outerCol.Grow(1)
		} else {
			width := outerCol.AnimateWith("composer-dock-width", dockWidth, animateDuration(c, composerDockDuration), ui.EaseOut)
			outerCol.Grow(1).MaxWidth(width)
		}

		outerCol.Children(func() {
			// 输入区状态胶囊：待办进度、继续提示与运行中改动统一成一行，
			// 状态类在左、内容类在右；没有胶囊时整行连同间距都不渲染。
			pillKind := continuePillNone
			if canShowContinue {
				pillKind = continueKind
			}
			capsules := composerCapsules(c, s, currentThreadID, pillKind, continueState.lastMessageID, isRunning, isEditingQueued, hasContent)
			// TabSlot 悬浮 Tab 槽位（排队消息等）；出现改动统计行时胶囊并入那一行，
			// 否则胶囊仍单独成行，渲染顺序保持 tab 槽在前、胶囊行在后。
			merged := renderComposerTabSlot(c, s, currentThreadID, queueItems, isRunning, capsules)
			if !merged {
				renderComposerCapsuleRow(c, s, capsules)
			}

			// 附件胶囊行在输入卡片之上。
			renderAttachments(c, s)

			// 快捷回复行：只在配置了快捷回复、会话已开始、输入框空闲且草稿为空时出现。
			renderQuickReplies(c, s)

			// 悬浮卡片主体。
			card := ui.Column(c.Key("composer-card")).
				Label("消息输入框").
				Radius(cardRadius).
				MaxHeight(composerCardMaxHeight(viewportHeight)).
				Background(ThemeSurface(c)).
				Padding(0, 0, Space2, 0)

			if !docked {
				card.FillWidth()
			} else {
				card.Grow(1)
			}

			// 最小高度走卡片自己的动画通道：切换帧若把 120 直接清成 0，卡片会瞬间变矮，
			// 底栏先上跳约 50pt 再随位移下滑。
			// 只有切换帧（promote）和位移续帧（container.Transitioning()）才播收缩动画；
			// 其他帧直接落位，这样切会话这类非首条消息的翻转不会播放收缩动画。
			minH := composerCardHeroMinHeight
			if docked {
				minH = 0
			}
			if promote || container.Transitioning() {
				minH = card.AnimateWith("composer-card-min-h", minH, animateDuration(c, composerCardMinHDuration), ui.EaseOut)
			} else {
				minH = card.Jump("composer-card-min-h", minH)
			}
			if minH > 0 {
				card.MinHeight(minH)
			}

			// D-9：聊天区整体接收拖放，输入卡保持普通描边，提示由覆盖层显示。
			card.Border(Hairline, ThemeBorder(c))

			card.Children(func() {
				// 卡片顶部不再堆提示行：运行中提示、编辑排队消息提示都并入输入框占位符或底栏胶囊。
				// 待审批等禁用原因同理——草稿为空时占位符本身就显示原因，占位符被草稿盖住时才补这一行。
				// 占位符沿用平时的颜色（框架固定用 TextMuted），不额外高亮。
				if disabledReason != "" && trimmedDraft != "" {
					ui.Row(c.Key("composer-disabled-hint")).Padding(Space0, Space1, 0, Space1).Children(func() {
						ui.Text(c, disabledReason).TextColor(ThemeSecondaryText(c)).FontSize(uiFont(TypeFootnote))
					})
				}

				// 顶行：文本输入列。
				ui.Row(c.Key("composer-top-row")).Padding(0, Space4).Gap(Space2).AlignItems(ui.Start).Grow(1).Children(func() {
					// 文本输入列
					ui.Column(c.Key("composer-text-col")).Grow(1).Children(func() {
						// 会话引用胶囊行位于输入框上方：引用的会话不再写进正文，而是排成独立胶囊。
						renderDraftThreadMentions(c, s, draftMentions)
						// 回合已结束、后台任务还在跑时，占位文字说明完成后会自动继续；
						// 审批禁用、编辑排队消息等提示各有更重要的信息，保持原样。
						waitingBackground := !isRunning && !isEditingQueued && disabledReason == "" &&
							!backgroundTasksFor(s, currentThreadID).empty()
						// 文字区使用统一内距，达到卡片上限后由输入框内部滚动。
						in := ui.TextArea(c.Key("composer-textarea"), &s.draft).
							Lines(1, int(textMaxHeight)).
							FontSize(uiFont(TypeProse)).
							LineHeight(1.3).
							MinHeight(composerTextMinHeight).
							MaxHeight(textMaxHeight).
							Placeholder(composerWaitingPlaceholder(composerPlaceholder(interactionMode, isRunning, runningPlaceholderState, disabledReason, sendKey), waitingBackground)).
							TextColor(ThemePrimaryText(c)).
							Background(ui.Transparent).
							BorderWidth(0).
							Padding(Space4, 0, Space4, 0).
							Grow(1).
							Disabled(isSendingDisabled)
						// 浮动面板里的输入框不抢焦点：面板随子代理出现，不该打断用户正在主输入框里的输入。
						if !s.embeddedBee {
							in.AutoFocus()
						}

						// 进入待创建会话时输入框往往早已挂载（元素 id 未变），AutoFocus 不再是首次出现；
						// 点击“新建会话”时指针按下又清掉了焦点，这里主动要回一次，点完新建即可直接打字。
						if focusRequested {
							in.Focus()
						}

						// 补全改写草稿后光标会留在原处，这里在新文本生效的下一帧把它放到文末。
						if s.completionCaretToEnd {
							s.completionCaretToEnd = false
							n := utf8.RuneCountInString(s.draft)
							in.SetTextSelection(n, n)
						}

						// 粘贴图片或文件时作为附件加入并吞掉这次粘贴；长文本收成附件行里的卡片，短文本仍交给输入框正常插入。
						// 框架的粘贴走 OnPaste，不经过 OnTextKey，必须在入口处拦截。
						in.OnPaste(func(text string) bool {
							return s.handlePaste(text)
						})

						// 记录输入框聚焦状态，供审批卡片判定是否注册 ⌘Return；审批卡片先于 composer 渲染，读到的是上一帧的值。
						// renderApproval 在 s.mu 内读取该字段，这里同样加锁写入。
						// 禁用状态的输入框不接收按键，不会和 composer 的 ⌘Return 入队冲突，所以不算占用焦点（有待处理审批时 composer 一定是禁用的）。
						if focused := in.Focused() && !isSendingDisabled; s.composerFocused != focused {
							s.mu.Lock()
							s.composerFocused = focused
							s.mu.Unlock()
							noteEmbeddedComposerFocus(s, focused)
							s.invalidate()
						}
						if s.isComposing != in.Composing() {
							s.isComposing = in.Composing()
							s.invalidate()
						}
						start, end := in.TextSelection()
						suspended := start != end || end != utf8.RuneCountInString(s.draft) || isSendingDisabled
						if s.completionSuspended != suspended {
							s.completionSuspended = suspended
							// 补全层先于输入框构建，读到的是上一帧的值；变化时补一帧让它开始收起或重新展开。
							s.invalidate()
						}
						s.syncDraftTags()
						// 记录当前会话草稿（防抖落盘），未切换会话直接退出时也能恢复；编辑排队项时输入框是排队项文本，跳过。
						if s.editingQueuedID() == "" {
							s.noteComposerDraft(currentThreadID, s.draft)
						}
						in.TextRanges(s.mentionRanges(ThemeAccent(c))...)

						// 读完 draft 与 isComposing 后，更新补全状态
						s.updateCompletion(currentThreadID)

						in.OnTextKey(func(k ui.TextKey) bool {
							// TextKey 的文本可能领先于上一帧绑定的草稿，不能用旧候选覆盖新输入。
							s.draft = k.Text()
							s.syncDraftTags()
							// 输入即记录草稿（1 秒防抖落盘），编辑排队项时不记录。
							if s.editingQueuedID() == "" {
								s.noteComposerDraft(currentThreadID, s.draft)
							}
							// 光标在起始处按 Backspace：正文没东西可删时删最后一颗引用胶囊。
							// start/end 取上一帧的光标位置。
							if !s.isComposing && k.Key == ui.KeyBackspace && k.Mods == 0 && start == 0 && end == 0 {
								if s.removeLastDraftThreadMention() {
									return true
								}
							}
							s.updateCompletion(currentThreadID)

							// 用户改动了回填的历史文本时退出导航，保留已输入的文本
							if h := &s.composerView.history; h.index != 0 && k.Text() != h.recalled {
								h.reset()
							}

							// 快捷键 ⌘⇧M / ⇧⌘P 切换 Build/Plan 模式
							if (k.Key == ui.KeyM || k.Key == ui.KeyP) && ((k.Mods&ui.Cmd != 0 || k.Mods&ui.Super != 0) && (k.Mods&ui.Shift != 0)) {
								s.toggleInteractionMode()
								return true
							}

							// 补全按键处理：Up/Down/Tab/Enter/Esc（组字中跳过）
							if !s.isComposing {
								if newText, handled := s.handleCompletionKey(k.Key, k.Mods); handled {
									if newText != "" {
										k.SetText(newText)
									} else if s.draft != k.Text() {
										// 会话引用胶囊化后正文可能被清空（newText 为空串），输入框还留着旧触发词，需显式同步。
										k.SetText(s.draft)
									}
									s.invalidate()
									return true
								}
							}

							// 方向键历史导航：首行 ↑ 翻更旧、末行 ↓ 翻更新。
							// ⌘⌥↑/↓ 的会话切换与补全菜单的上下键已在上方被处理，不会走到这里。
							if !s.isComposing && k.Mods == 0 && start == end && s.editingQueuedID() == "" {
								switch {
								case k.Key == ui.KeyUp && caretOnFirstLine(k.Text(), start):
									if text, ok := s.composerView.history.prev(historyEntries, k.Text()); ok {
										k.SetText(text)
										s.completionCaretToEnd = true
										s.invalidate()
										return true
									}
								case k.Key == ui.KeyDown && s.composerView.history.index != 0 && caretOnLastLine(k.Text(), start):
									if text, ok := s.composerView.history.next(historyEntries); ok {
										k.SetText(text)
										s.completionCaretToEnd = true
										s.invalidate()
										return true
									}
								}
							}

							// Esc 处理：正在编辑排队项时取消编辑；历史导航中恢复草稿；运行中按 Esc 停止当前回合
							if k.Key == ui.KeyEscape && k.Mods == 0 {
								if s.editingQueuedID() != "" {
									s.cancelQueuedEdit()
									return true
								}
								// 补全打开时 Esc 已在上方被补全处理消费，这里只处理历史导航
								if !s.isComposing {
									if text, ok := s.composerView.history.restore(); ok {
										k.SetText(text)
										s.completionCaretToEnd = true
										s.invalidate()
										return true
									}
								}
								if isRunning {
									s.interruptCurrent()
									return true
								}
							}

							// ⌘Return / ⌘Enter 快捷键
							if k.Key == ui.KeyEnter && (k.Mods == ui.Cmd || k.Mods == ui.Super || (k.Mods&ui.Cmd != 0) || (k.Mods&ui.Super != 0)) {
								// 组字中返回 false
								if s.isComposing {
									return false
								}
								text := strings.TrimSpace(k.Text())

								s.mu.Lock()
								hasUploaded := false
								hasUploading := false
								for _, att := range s.attachments {
									if att.Uploading {
										hasUploading = true
									}
									if att.ID != "" {
										hasUploaded = true
									}
								}
								// 粘贴卡片和已上传附件一样算作可发送的内容。
								hasUploaded = hasUploaded || len(s.snippets) > 0
								s.mu.Unlock()

								hasContentNow := (text != "" || hasUploaded)
								if s.editingQueuedID() != "" {
									if !isSendingDisabled {
										s.saveQueuedEdit(text)
									}
									return true
								}
								if isRunning {
									// 运行中总是入队
									if hasContentNow && !hasUploading && !isSendingDisabled {
										s.enqueueMessage(text)
									}
									return true
								}
								// 空闲时等同发送
								if hasContentNow && !hasUploading && !isSendingDisabled {
									if s.sendDraft(currentThreadID, text) {
										k.SetText("")
									}
								}
								return true
							}

							// 普通 Enter 快捷键：按设置匹配发送键（默认 Enter 发送，Shift+Enter 换行）
							if k.Key == ui.KeyEnter && k.Mods == composerSendMods(sendKey) {
								// 组字中按 Enter 不发送
								if s.isComposing {
									return false
								}
								text := strings.TrimSpace(k.Text())

								// 正在编辑排队项时按 Return 保存修改
								if s.editingQueuedID() != "" {
									s.saveQueuedEdit(text)
									return true
								}

								s.mu.Lock()
								hasUploaded := false
								hasUploading := false
								for _, att := range s.attachments {
									if att.Uploading {
										hasUploading = true
									}
									if att.ID != "" {
										hasUploaded = true
									}
								}
								// 粘贴卡片和已上传附件一样算作可发送的内容。
								hasUploaded = hasUploaded || len(s.snippets) > 0
								s.mu.Unlock()

								hasContentNow := (text != "" || hasUploaded)

								if isRunning {
									// 运行中有内容时：根据运行中发送状态决定插入当前回合 (inject)、转向 (steer) 或排队 (queue)
									if hasContentNow && !hasUploading && !isSendingDisabled {
										runState := runningSendDraftState(isRunning, hasContentNow, canContinue, s.queueWhileRunningEnabled(), s.insertIntoRunningTurnEnabled(), false)
										if runState == sendStateInject {
											s.injectMessage(text)
										} else if runState == sendStateSteer {
											s.steerMessage(text)
										} else if runState == sendStateQueue {
											s.enqueueMessage(text)
										}
									}
									return true
								}

								// 空闲状态
								if (text == "" && !hasUploaded) || hasUploading || isSendingDisabled {
									return false
								}
								if s.sendDraft(currentThreadID, text) {
									k.SetText("")
								}
								return true
							}
							return false
						})
					})
				})

				// 底行：左侧配额槽位（低优先级）+ 右侧控制按钮组（模式指示器、模型胶囊、上下文仪表、发送按钮）
				// 底栏上下各留 Space0，与输入正文的 Space4 内距叠成正文到按钮之间的呼吸空间。
				bottomRow := ui.Row(c.Key("composer-bottom-row")).Padding(Space0, Space2).AlignItems(ui.Center).ClipX()
				bottomRow.Children(func() {
					tier := composerChinTierFor(s, bottomRow.Bounds().W)
					attachBtn := ui.ButtonBase(c.Key("composer-attach-btn")).
						Label("添加附件").Tooltip("添加附件").Disabled(isSendingDisabled).
						Size(composerActionBtnSize, composerActionBtnSize).Radius(buttonRadius).
						OnClick(func() { s.composerView.downloadsOpen = !s.composerView.downloadsOpen })
					composerHoverBg(c, attachBtn, s.composerView.downloadsOpen || attachBtn.Hovered())
					attachBtn.Children(func() {
						ui.Icon(c, IconPaperclip()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
					})
					bg := ThemeSurface(c)
					host := c.Services()
					appFlyoutPopover(c, composerKeyPrefix(s, currentThreadID)+"downloads-popover", attachBtn, &s.composerView.downloadsOpen, func() {
						s.composerView.downloadsOpen = false
						s.composerView.mcpMenuOpen = false
					}, appFlyoutProps{Width: 360, Placement: mygo.PlacementTopStart, Background: &bg}, func(fc *ui.Context) {
						renderAttachMenuPanel(fc, s, currentThreadID, func() {
							s.composerView.downloadsOpen = false
							host.Invalidate()
						})
					})
					// 左侧供应商额度条：空间不足时优先收缩，不挤压右侧控件。
					// 浮动面板里的子代理输入框（embeddedBee）同样显示该子代理会话的额度条。
					ui.Row(c.Key("composer-quota-slot")).AlignItems(ui.Center).Shrink(1).ClipX().Children(func() {
						// 编辑排队消息时，左槽位改放编辑状态胶囊，额度条暂时让位，同一行不至于太挤。
						if isEditingQueued {
							renderEditQueuedPill(c, s, tier)
							return
						}
						renderComposerQuotaStrip(c, s, currentThread, tier)
					})

					ui.Spacer(c)

					// 右侧控制按钮组
					ui.Row(c.Key("composer-controls")).AlignItems(ui.Center).Shrink(0).Gap(composerControlGap).Children(func() {
						if stewardID := s.stewardThreadID(); stewardID != "" && ((currentThread != nil && currentThread.ID == stewardID) || (currentThread == nil && selectedID == stewardID)) {
							renderStewardComposerModel(c, s)
						} else if currentThread != nil {
							renderInteractionModeToggle(c, s, interactionMode, prefsSaving || sending, buttonRadius)
							renderModelSelector(c, s, currentThread, providers, newThreadProvider, hasMessages, bottomRow.Bounds().W)
							renderContextMeter(c, s, currentThread)
						}

						// 5 态主操作按钮：Key 固定（不随状态变），按钮元素跨状态保持同一身份，
						// 底色 120ms 过渡与图标交叉淡入才能连续播放；状态差异全在 Label / 图标 / 回调上。
						sendBtnKey := "composer-primary-btn"
						var sendBtnLabel string
						var sendBtnTooltip string
						var sendBtnDisabled bool
						var sendBtnAction func()
						var sendBtnIsDanger bool

						switch sendState {
						case sendStateStop:
							sendBtnLabel = "停止"
							sendBtnTooltip = "停止"
							sendBtnDisabled = stopping
							sendBtnIsDanger = true
							sendBtnAction = func() {
								s.stopOrRecall()
							}

						case sendStateQueue:
							sendBtnLabel = "排队"
							sendBtnTooltip = "排队"
							sendBtnDisabled = hasUploadingAttachment || isSendingDisabled || s.isComposing
							sendBtnAction = func() {
								text := strings.TrimSpace(s.draft)
								if (text != "" || hasUploadedAttachment) && !hasUploadingAttachment && !isSendingDisabled {
									s.enqueueMessage(text)
								}
							}

						case sendStateSteer:
							sendBtnLabel = "转向"
							sendBtnTooltip = "转向"
							sendBtnDisabled = hasUploadingAttachment || isSendingDisabled || s.isComposing
							sendBtnAction = func() {
								text := strings.TrimSpace(s.draft)
								if (text != "" || hasUploadedAttachment) && !hasUploadingAttachment && !isSendingDisabled {
									s.steerMessage(text)
								}
							}

						case sendStateInject:
							sendBtnLabel = "插入"
							sendBtnTooltip = "插入当前回合（服务商不支持时排队）"
							sendBtnDisabled = hasUploadingAttachment || isSendingDisabled || s.isComposing
							sendBtnAction = func() {
								text := strings.TrimSpace(s.draft)
								if (text != "" || hasUploadedAttachment) && !hasUploadingAttachment && !isSendingDisabled {
									s.injectMessage(text)
								}
							}

						case sendStateContinue:
							sendBtnLabel = "继续"
							sendBtnTooltip = "继续执行 (Resume)"
							sendBtnDisabled = isSendingDisabled
							sendBtnAction = func() {
								s.continueLastTurn()
							}

						case sendStateSaveEdit:
							sendBtnLabel = "保存修改"
							sendBtnTooltip = "保存修改"
							sendBtnDisabled = isSendingDisabled || s.isComposing
							sendBtnAction = func() {
								text := strings.TrimSpace(s.draft)
								s.saveQueuedEdit(text)
							}

						default: // sendStateSend
							trimmed := strings.TrimSpace(s.draft)
							canSend := (trimmed != "" || hasUploadedAttachment) && !hasUploadingAttachment && !isSendingDisabled && !s.isComposing
							sendBtnLabel = "发送"
							sendBtnTooltip = "发送"
							sendBtnDisabled = !canSend
							sendBtnAction = func() {
								text := strings.TrimSpace(s.draft)
								if (text != "" || hasUploadedAttachment) && !hasUploadingAttachment && !isSendingDisabled {
									s.sendDraft(currentThreadID, text)
								}
							}
						}

						// 图标随状态切换：切换那一帧记下旧状态并重置淡入进度，旧图标随后淡出。
						iconChanged := s.composerView.sendIconState != sendState
						if iconChanged {
							s.composerView.sendIconPrevState = s.composerView.sendIconState
							s.composerView.sendIconState = sendState
						}
						sendBtnIcon, sendBtnIconSize := composerSendIconFor(sendState)

						btn := ui.ButtonBase(c.Key(sendBtnKey)).
							Label(sendBtnLabel).
							Tooltip(sendBtnTooltip).
							Disabled(sendBtnDisabled).
							Size(composerActionBtnSize, composerActionBtnSize).
							Radius(buttonRadius).
							Transition(motionFeedback(c))
						if iconChanged {
							// 把淡入进度拉回起点，下一帧起按 90ms ease-out 插值到 1。
							btn.Jump(composerSendIconFadeKey, 0)
						}
						iconFade := btn.AnimateTo(composerSendIconFadeKey, 1, ui.Animation{
							Duration: animateDuration(c, motionIconFade),
							Ease:     ui.EaseOut,
						})
						var prevIcon *ui.SVG
						var prevIconSize float32
						if iconFade < 1 && !reduceMotion(c) {
							// 淡入就没结束：旧图标继续在同一个位置淡出。
							prevIcon, prevIconSize = composerSendIconFor(s.composerView.sendIconPrevState)
						}

						if sendBtnIsDanger {
							// 危险态（停止）：保留红色底，悬停时叠加提亮以给出反馈。
							btn.Background(ThemeDanger(c))
							if btn.Hovered() {
								btn.Background(ThemeDanger(c).Mix(ThemeOnAccent(c), 0.16))
							}
							btn.OnClick(sendBtnAction)
							btn.Children(func() {
								renderComposerSendIcon(c, sendBtnIcon, prevIcon, sendBtnIconSize, prevIconSize, iconFade, ThemeOnAccent(c))
							})
						} else if !sendBtnDisabled {
							// 可发送：accent 实心底，悬停时提亮。
							btn.Background(ThemeAccent(c))
							if btn.Hovered() {
								btn.Background(ThemeAccent(c).Mix(ThemeOnAccent(c), 0.16))
							}
							btn.OnClick(sendBtnAction)
							btn.Children(func() {
								renderComposerSendIcon(c, sendBtnIcon, prevIcon, sendBtnIconSize, prevIconSize, iconFade, ThemeOnAccent(c))
							})
						} else {
							// 空闲（禁用）发送按钮：保留浅底，悬停时提亮。
							composerHoverBg(c, btn, btn.Hovered())
							btn.Children(func() {
								renderComposerSendIcon(c, sendBtnIcon, prevIcon, sendBtnIconSize, prevIconSize, iconFade, ThemeSecondaryText(c))
							})
						}
						if sendState == sendStateStop && stopping {
							// 停止请求已发出：整枚按钮淡出，不再额外换文案或加转圈行。
							btn.Opacity(0.6)
						}
					})
				})
			})

			// 补全弹层挂在输入卡片上沿的浮层里，不占用聊天区布局空间；需在卡片之后构建。
			renderCompletionMenu(c, s, card)
		})
	})
}

// composerSendIconFor 返回某一发送状态的主操作图标与尺寸，与按钮的五态一一对应。
func composerSendIconFor(state sendDraftState) (*ui.SVG, float32) {
	switch state {
	case sendStateStop:
		return IconStopFilled(), 11
	case sendStateQueue:
		return iconClock, composerControlIconSize
	case sendStateSteer, sendStateInject:
		return iconSteer, composerControlIconSize
	case sendStateContinue:
		return iconPlayFilled, composerControlIconSize - 1
	case sendStateSaveEdit:
		return IconCheck(), composerControlIconSize
	default: // sendStateSend
		return IconSend(), composerControlIconSize
	}
}

// renderComposerSendIcon 渲染主操作按钮的图标：fade 未到 1 时旧图标淡出、新图标淡入，
// 两层都贴在按钮中心（不位移，尺寸各自保持不变）；fade 到 1 后只画新图标。
func renderComposerSendIcon(c *ui.Context, cur, prev *ui.SVG, curSize, prevSize, fade float32, color ui.Color) {
	if prev == nil || fade >= 1 {
		ui.Icon(c, cur).
			Size(curSize, curSize).
			TextColor(color)
		return
	}
	ui.Row(c.Key("composer-send-icon-layer")).
		Size(composerActionBtnSize, composerActionBtnSize).
		Center().
		Children(func() {
			ui.Icon(c, prev).
				Size(prevSize, prevSize).
				TextColor(color).
				Attach(ui.AnchorCenter, ui.AnchorCenter).
				Opacity(1 - fade)
			ui.Icon(c, cur).
				Size(curSize, curSize).
				TextColor(color).
				Attach(ui.AnchorCenter, ui.AnchorCenter).
				Opacity(fade)
		})
}

// normalizeInteractionMode 把空值或未知值归一为 "build"。
func normalizeInteractionMode(mode string) string {
	if mode == "plan" {
		return "plan"
	}
	return "build"
}

// renderEditQueuedPill 渲染底栏左侧的「编辑排队消息」胶囊：点击等同于按 Esc 取消编辑。
func renderEditQueuedPill(c *ui.Context, s *chatState, tier composerChinSize) {
	text := "编辑排队消息 · Esc 取消"
	if tier >= composerChinNoShortcut {
		text = "编辑排队消息"
	}
	pill := ui.ButtonBase(c.Key("composer-editing-pill")).
		Label("编辑排队消息 · Esc 取消").
		Tooltip("取消编辑 (Esc)").
		Height(ControlSM).
		Radius(RadiusPill).
		Padding(0, Space3).
		Background(ThemeAccent(c).Alpha(0.14)).
		OnClick(func() {
			s.cancelQueuedEdit()
		})
	pill.Children(func() {
		ui.Text(c, text).
			FontSize(uiFont(TypeFootnote)).
			TextColor(ThemeAccent(c))
	})
}

// composerPlaceholder 按禁用原因、编辑排队状态、Build/Plan 模式与运行状态返回输入框占位符。
// 禁用原因（待审批等）优先级最高，输入框此时完全不可用，先说明原因；
// 编辑排队消息时说明发送键保存、Esc 取消；
// 运行中（AI 回答中）只给一行提示，说明发送键的实际动作与 ⌘. 停止；
// state 传「假设有内容时」的发送状态，避免空草稿的 sendStateStop 掩盖真实动作；
// sendKey 是设置里的发送键，换行键随之互换（默认 Enter 发送、Shift+Enter 换行）。
func composerPlaceholder(mode string, isRunning bool, state sendDraftState, disabledReason string, sendKey string) string {
	if reason := strings.TrimSpace(disabledReason); reason != "" {
		return reason
	}
	sendKeyLabel, newlineKeyLabel := "Enter", "Shift+Enter"
	if sendKey == config.ComposerSendKeyShiftEnter {
		sendKeyLabel, newlineKeyLabel = "Shift+Enter", "Enter"
	}
	if state == sendStateSaveEdit {
		return "编辑排队消息 · " + sendKeyLabel + " 保存，Esc 取消"
	}
	if isRunning {
		action := "加入队列"
		switch state {
		case sendStateSteer:
			action = "打断并发送"
		case sendStateInject:
			action = "插入当前回合"
		}
		return "回复中 · " + sendKeyLabel + " " + action + "，⌘. 停止"
	}
	if normalizeInteractionMode(mode) == "plan" {
		return "描述你要规划的任务，" + sendKeyLabel + " 发送，" + newlineKeyLabel + " 换行"
	}
	return "输入消息，" + sendKeyLabel + " 发送，" + newlineKeyLabel + " 换行"
}

// composerWaitingPlaceholderText 是回合已结束、后台任务仍在跑时的输入框占位文字。
const composerWaitingPlaceholderText = "后台任务运行中，完成后自动继续；也可以直接输入"

// composerWaitingPlaceholder 在等待后台任务时替换常规占位文字：说明完成后会自动继续，
// 同时保留直接输入的入口；不等待时原样返回 base。
func composerWaitingPlaceholder(base string, waiting bool) string {
	if waiting {
		return composerWaitingPlaceholderText
	}
	return base
}

func interactionModeIcon(mode string) *ui.SVG {
	if mode == "plan" {
		return IconListChecks()
	}
	return IconHammer()
}

// renderInteractionModeToggle 渲染构建/计划模式切换按钮。
func renderInteractionModeToggle(c *ui.Context, s *chatState, mode string, disabled bool, buttonRadius float32) {
	title := "构建"
	if mode == "plan" {
		title = "计划"
	}
	btn := ui.ButtonBase(c.Key("composer-interaction-toggle")).
		Label(title).
		Disabled(disabled).
		Tooltip(title+" · ⌘⇧M").
		Size(composerActionBtnSize, composerActionBtnSize).
		Radius(buttonRadius).
		OnClick(func() {
			s.toggleInteractionMode()
		})
	textColor := ThemePrimaryText(c)
	// 切换胶囊；悬停或展开时提亮。构建模式为透明胶囊，计划模式保留紫色底并再叠加一层提亮。
	hovered := btn.Hovered() && !disabled
	if mode == "plan" {
		btn.Background(ThemePlan(c).Alpha(0.18))
		if hovered {
			btn.Background(ThemePlan(c).Alpha(0.26))
		}
		textColor = ThemePlan(c)
	} else {
		composerHoverBg(c, btn, hovered)
	}
	btn.Children(func() {
		ui.Icon(c, interactionModeIcon(mode)).Size(composerControlIconSize, composerControlIconSize).TextColor(textColor)
	})
}

// composerPrefsOf 取会话当前保存的 composer 偏好。
func composerPrefsOf(th Thread) ComposerPrefs {
	return ComposerPrefs{
		InteractionMode: normalizeInteractionMode(th.InteractionMode),
		Effort:          th.Effort,
		BeesProvider:    th.BeesProvider,
		BeesModel:       th.BeesModel,
		BeesEffort:      th.BeesEffort,
		Persona:         th.Persona,
	}
}

// applyComposerPrefsLocked 把偏好写回本地会话列表（平铺视图与按项目分组视图），调用方需持有 s.mu。
func (s *chatState) applyComposerPrefsLocked(threadID string, p ComposerPrefs) {
	apply := func(th *Thread) {
		th.InteractionMode = p.InteractionMode
		th.Effort = p.Effort
		th.BeesProvider = p.BeesProvider
		th.BeesModel = p.BeesModel
		th.BeesEffort = p.BeesEffort
		th.Persona = p.Persona
	}
	for i := range s.threads {
		if s.threads[i].ID == threadID {
			apply(&s.threads[i])
			break
		}
	}
	for _, list := range s.threadsByProj {
		for i := range list {
			if list[i].ID == threadID {
				apply(&list[i])
				break
			}
		}
	}
}

// updateComposerPrefs 乐观更新指定会话的 composer 偏好并异步保存，失败时回滚并提示。
// mutate 只修改需要变更的字段，其余字段保留会话当前值，避免整体覆盖时丢失。
func (s *chatState) updateComposerPrefs(threadID string, mutate func(p *ComposerPrefs), failMsg string) {
	s.updateComposerPrefsWith(threadID, mutate, failMsg, nil)
}

// updateComposerPrefsWith 与 updateComposerPrefs 相同，额外在保存成功后回调 onSaved（可为 nil）。
// 「按项目记忆交互模式」靠它：只有后端确认保存成功才写项目桶，失败时保留旧记录。
func (s *chatState) updateComposerPrefsWith(threadID string, mutate func(p *ComposerPrefs), failMsg string, onSaved func(next ComposerPrefs)) {
	s.mu.Lock()
	if s.sending || s.composerPrefsSaving[threadID] {
		s.mu.Unlock()
		return
	}
	if s.isPendingThreadLocked(threadID) {
		// 待创建会话：偏好只写进待创建状态，创建时随会话一起下发。
		prev := composerPrefsOf(s.pendingThread.Thread)
		prefs := prev
		mutate(&prefs)
		s.applyPendingComposerPrefsLocked(prefs)
		s.mu.Unlock()
		s.invalidate()
		if onSaved != nil {
			onSaved(prefs)
		}
		// 执行侧的模型与强度只能从这里改（选模型、调强度、跟随主导、配对、重置），待创建会话也记下供后续新建沿用。
		if prefs.BeesProvider != prev.BeesProvider || prefs.BeesModel != prev.BeesModel || prefs.BeesEffort != prev.BeesEffort {
			rememberSwarmBees(prefs.BeesProvider, prefs.BeesModel, prefs.BeesEffort)
		}
		return
	}
	var prev ComposerPrefs
	found := false
	for _, th := range s.threads {
		if th.ID == threadID {
			prev = composerPrefsOf(th)
			found = true
			break
		}
	}
	if !found {
		s.mu.Unlock()
		return
	}
	next := prev
	mutate(&next)
	if s.composerPrefsSaving == nil {
		s.composerPrefsSaving = make(map[string]bool)
	}
	s.composerPrefsSaving[threadID] = true
	s.applyComposerPrefsLocked(threadID, next)
	s.mu.Unlock()
	s.invalidate()

	go func() {
		err := s.client.SetThreadComposerPrefs(threadID, next)
		s.mu.Lock()
		delete(s.composerPrefsSaving, threadID)
		if err != nil {
			s.applyComposerPrefsLocked(threadID, prev)
			s.toastMessage = failMsg + ": " + err.Error()
		} else {
			s.applyComposerPrefsLocked(threadID, next)
		}
		s.mu.Unlock()
		s.invalidate()
		// 执行侧的模型与强度只能从这里改（选模型、调强度、跟随主导、配对、重置），统一在这里记下供新建会话沿用。
		if err == nil && (next.BeesProvider != prev.BeesProvider || next.BeesModel != prev.BeesModel || next.BeesEffort != prev.BeesEffort) {
			rememberSwarmBees(next.BeesProvider, next.BeesModel, next.BeesEffort)
		}
		if err == nil && onSaved != nil {
			onSaved(next)
		}
	}()
}

// enableSwarmWithGlobal 切换当前会话的 Swarm。
// 开启时若全局总开关未开且未被启动参数锁定，先联动打开全局开关并持久化；关闭时不改全局开关。
func (s *chatState) enableSwarmWithGlobal(enabled bool) {
	if s == nil {
		return
	}
	if enabled {
		if st := getChatSettings(s); st != nil && !st.isLocked("swarm-enabled") && !st.cfg.Orchestration.IsEnabled() {
			v := true
			// 改 cfg 要同时持有 mu 与 cfgMu（顺序 mu → cfgMu），save 在两把锁之外调。
			st.mu.Lock()
			st.cfgMu.Lock()
			st.cfg.Orchestration.Enabled = &v
			st.cfgMu.Unlock()
			st.mu.Unlock()
			st.save()
		}
	}
	s.setSwarmEnabled(enabled)
}

// applySwarmPair 一键把会话切换为指定 Swarm 配对（C-HY-19）：
// Lead 侧复用单模型切换路径，Bees 侧与 Lead 强度一次性写入 ComposerPrefs，
// 随后联动开启会话 Swarm 并关闭整个弹层。
func (s *chatState) applySwarmPair(threadID string, p SwarmPair) {
	if s == nil || s.client == nil {
		return
	}
	leadEffort := ""
	if p.LeadEffort != nil {
		leadEffort = *p.LeadEffort
	}
	workerEffort := ""
	if p.WorkerEffort != nil {
		workerEffort = *p.WorkerEffort
	}
	// 配对模板没带主导强度时，按新主导模型的默认强度补齐：换模型就该套新模型的默认档。
	// 模板显式指定了强度则原样保留，不覆盖用户的编排意图。这里不持锁，取值函数要读偏好缓存。
	if leadEffort == "" && (p.LeadProvider != "" || p.LeadModel != "") {
		s.mu.Lock()
		providers := append([]ProviderInfo(nil), s.providers...)
		s.mu.Unlock()
		leadEffort = s.defaultEffortForModel(p.LeadProvider, modelPickerModelInfo(providers, p.LeadProvider, p.LeadModel))
	}

	s.mu.Lock()
	if s.isPendingThreadLocked(threadID) {
		// 待创建会话：配对直接写进待创建选项，创建时再下发。
		s.applyPendingSwarmPairLocked(p, leadEffort, workerEffort)
		// 记忆只写配对实际生效的取值：配对未给强度时沿用原强度，快速模式沿用记忆里同模型的记录。
		applied := ModelSelection{}
		if cur := s.pendingThread; cur != nil {
			applied = ModelSelection{Provider: cur.Thread.Provider, Model: cur.Thread.Model, Effort: cur.Thread.Effort}
		}
		s.mu.Unlock()
		if applied.Provider != "" && applied.Model != "" {
			applied.Fast = rememberedFastFor(applied.Provider, applied.Model)
			s.rememberModelSelectionForThread(threadID, applied)
		}
		// 配对模板换掉主导 Agent 时同样记为“上次使用”。
		s.rememberUsedProvider(p.LeadProvider)
		rememberSwarmBees(p.WorkerProvider, p.WorkerModel, workerEffort)
		s.enableSwarmWithGlobal(true)
		resetComposerModelPickerState(threadID)
		s.invalidate()
		return
	}
	s.mu.Unlock()

	// 配对变更时后端自动清空档位覆盖，并记下本次选中的配对；本地乐观同步清空档位。
	s.mu.Lock()
	for i := range s.threads {
		if s.threads[i].ID == threadID {
			s.threads[i].BeeTiers = ""
			s.threads[i].PairID = p.ID
			break
		}
	}
	for _, list := range s.threadsByProj {
		for i := range list {
			if list[i].ID == threadID {
				list[i].BeeTiers = ""
				list[i].PairID = p.ID
				break
			}
		}
	}
	s.mu.Unlock()

	// 记下选中的配对：后端据此认定会话生效的档位映射组，并在配对变化时清空会话级档位覆盖。
	go func() {
		if err := s.client.SetThreadSwarmPairID(threadID, p.ID); err != nil {
			s.setToast("记录编排配对失败: " + err.Error())
		}
	}()

	// Lead 侧：provider 与当前一致时不重复下发，只切换模型（与模型行点击一致）。
	if p.LeadProvider != "" || p.LeadModel != "" {
		reqProvider := p.LeadProvider
		remembered := ModelSelection{Provider: p.LeadProvider, Model: p.LeadModel, Effort: leadEffort}
		s.mu.Lock()
		for _, th := range s.threads {
			if th.ID == threadID {
				if th.Provider == p.LeadProvider {
					reqProvider = ""
				}
				if remembered.Provider == "" {
					remembered.Provider = th.Provider
				}
				if remembered.Effort == "" {
					remembered.Effort = th.Effort
				}
				break
			}
		}
		s.mu.Unlock()

		// 配对模板会换掉主导 Agent，同样记为“上次使用”。
		s.rememberUsedProvider(p.LeadProvider)

		go func(reqProv string) {
			if err := s.client.SetThreadAgent(threadID, reqProv, p.LeadModel); err != nil {
				s.setToast("切换配对主代理模型失败: " + err.Error())
				return
			}
			if remembered.Provider != "" && remembered.Model != "" {
				s.rememberModelSelectionForThread(threadID, remembered)
			}
			s.mu.Lock()
			delete(s.commandsCache, threadID)
			if s.completionThreadID == threadID {
				s.completionKind = completionNone
				s.completionItems = nil
				s.mentionSeq++
			}
			applyLead := func(th *Thread) {
				if reqProv != "" {
					th.Provider = p.LeadProvider
				}
				if p.LeadModel != "" || reqProv == "" {
					th.Model = p.LeadModel
				}
			}
			for i := range s.threads {
				if s.threads[i].ID == threadID {
					applyLead(&s.threads[i])
					break
				}
			}
			for _, list := range s.threadsByProj {
				for i := range list {
					if list[i].ID == threadID {
						applyLead(&list[i])
						break
					}
				}
			}
			s.mu.Unlock()
			s.invalidate()
		}(reqProvider)
	}

	// Bees 侧与 Lead 强度一次性写入 ComposerPrefs（整体覆盖，必须单次提交）。
	s.updateComposerPrefs(threadID, func(prefs *ComposerPrefs) {
		if leadEffort != "" {
			prefs.Effort = leadEffort
		}
		prefs.BeesProvider = p.WorkerProvider
		prefs.BeesModel = p.WorkerModel
		prefs.BeesEffort = workerEffort
	}, "应用编排配对失败")

	// 联动开启会话 Swarm（必要时先打开全局总开关）。
	s.enableSwarmWithGlobal(true)

	// 关闭整个弹层并清理列表与宽度快照状态。
	resetComposerModelPickerState(threadID)
	s.invalidate()
}

// toggleInteractionMode 在当前会话的 build 与 plan 之间切换；没有选中会话时不做任何事。
func (s *chatState) toggleInteractionMode() {
	s.mu.Lock()
	threadID := s.selectedThreadID
	s.mu.Unlock()
	if threadID == "" {
		return
	}
	s.updateComposerPrefsWith(threadID, func(p *ComposerPrefs) {
		if p.InteractionMode == "plan" {
			p.InteractionMode = "build"
		} else {
			p.InteractionMode = "plan"
		}
	}, "切换 Build/Plan 模式失败", nil)
}

// runtimeModeOption 定义单个运行权限档位的元数据。
type runtimeModeOption struct {
	mode    string
	title   string
	summary string
}

var runtimeModeOptions = []runtimeModeOption{
	{
		mode:    "supervised",
		title:   "监督",
		summary: "询问后再执行命令和改文件",
	},
	{
		mode:    "auto",
		title:   "自动",
		summary: "改文件和只读命令不询问",
	},
	{
		mode:    "fullAccess",
		title:   "完全访问",
		summary: "不询问直接执行",
	},
}

// runtimeModeTitle 根据模式字符串获取对应的中文显示标题。
func runtimeModeTitle(mode string) string {
	for _, opt := range runtimeModeOptions {
		if opt.mode == mode {
			return opt.title
		}
	}
	if mode == "" || mode == "autoAcceptEdits" || mode == "agent" {
		return "自动"
	}
	if mode == "readonly" {
		return "只读"
	}
	// 未知档位不把原始英文模式名回显给用户。
	return "自定义"
}

// renderProviderIcon 根据 provider 标识渲染对应的 Agent 图标。
func renderProviderIcon(c *ui.Context, provider string, size float32) {
	renderProviderIconColor(c, provider, size, ThemeSecondaryText(c))
}

// composerModelPickerState 是 Composer 模型选择组件的展开状态（弹层、分段、二级列表与并发上限菜单）。
var composerModelPickerState = &ModelPickerState{}

// resetComposerModelPickerState 清理 Composer 模型选择组件的全部展开与残留状态。
func resetComposerModelPickerState(scopeKey string) {
	if composerModelPickerState != nil {
		composerModelPickerState.Reset()
	}
	if scopeKey != "" {
		clearModelCapsuleSnapshot(scopeKey)
		clearModelPanelMoreOpen(scopeKey, "lead")
		clearModelPanelMoreOpen(scopeKey, "bees")
	}
}

// pinnedModelPins 读取全局设置中固定的模型列表（一级页精选模型）。
func pinnedModelPins(s *chatState) []ModelPin {
	st := getChatSettings(s)
	if st == nil {
		return nil
	}
	return st.cfg.Models.Pins
}

// providerEnabledState 读取配置快照里各供应商的启用状态，供模型选择器隐藏已关闭供应商的模型。
// 只读内存中的配置，不发请求；ProviderSetting 侧的开关需要设置页缓存，Composer 没有缓存，
// 因此这里只覆盖 claude/codex/agy 与自定义端点这些由 config 控制的开关。
func providerEnabledState(s *chatState) map[string]bool {
	st := getChatSettings(s)
	if st == nil {
		return nil
	}
	return providerEnabledMap(st.cfg, nil, nil)
}

// swarmMaxBees 读取全局设置中的并发 Bee 上限。
func swarmMaxBees(s *chatState) int {
	st := getChatSettings(s)
	if st == nil {
		return 4
	}
	return st.cfg.Orchestration.MaxBees
}

// composerBeeNameMinWidth 是胶囊里子代理短名的最小可用宽度：底行比这窄时只留子代理图标。
const composerBeeNameMinWidth = float32(480)

// renderModelSelector 渲染输入框底部的模型选择组件（Swarm 模式：主导/执行两侧、配对模板与并发上限）。
// 组件实现见 model_picker.go，这里只把会话与全局设置的状态、偏好读写接到组件回调上。
// rowWidth 是底行实测宽度，用于在窄宽度下隐藏胶囊里的子代理短名。
func renderModelSelector(c *ui.Context, s *chatState, currentThread *Thread, providers []ProviderInfo, defaultProvider string, hasMessages bool, rowWidth float32) {
	s.mu.Lock()
	prefsSaving := s.composerPrefsSaving[currentThread.ID] || s.sending
	s.mu.Unlock()

	currentProvider := currentThread.Provider
	if currentProvider == "" {
		currentProvider = defaultProvider
	}
	if currentProvider == "" && len(providers) > 0 {
		currentProvider = providers[0].ID
	}

	// 主导侧：会话未指定模型时优先使用上次记录的模型（先按会话所属项目，其次全局），
	// 其次使用该 provider 模型列表的第一个。
	leadSelection := resolveModelPickerSelectionForProject(providers, ModelSelection{Provider: currentProvider, Model: currentThread.Model}, currentThread.ProjectID, pinnedModelPins(s))
	leadSelection.Effort = currentThread.Effort
	leadInfo := modelPickerModelInfo(providers, currentProvider, leadSelection.Model)
	leadPin := modelPreferencePin(currentProvider, leadInfo.ID)
	s.loadModelPreference(leadPin)
	// 快速模式读当前会话的 Thread.FastMode，模型偏好只作为新建会话的默认值。
	leadSelection.Fast = currentThread.FastMode

	// 执行侧：未指定 provider/model 时跟随主导，复用主导的偏好 pin。
	beesFollowsLead := currentThread.BeesProvider == "" && currentThread.BeesModel == ""
	beesSelection := ModelSelection{Provider: currentThread.BeesProvider, Model: currentThread.BeesModel, Effort: currentThread.BeesEffort}
	beesPin := leadPin
	if !beesFollowsLead {
		beesInfo := modelPickerModelInfo(providers, currentThread.BeesProvider, currentThread.BeesModel)
		beesPin = modelPreferencePin(currentThread.BeesProvider, beesInfo.ID)
		s.loadModelPreference(beesPin)
	}
	// 执行侧跟随主导时没有独立的会话快速字段，直接沿用会话值；自定义执行侧仍读其模型偏好。
	if beesFollowsLead {
		beesSelection.Fast = currentThread.FastMode
	} else {
		beesSelection.Fast = s.modelPreference(beesPin).FastMode
	}

	swarmOn := false
	// 子代理自己不带团队：嵌入的输入框不拉取 Swarm 状态，也不显示主导/执行配对。
	if !s.embeddedBee {
		s.ensureSwarm(c)
		if hp := getSwarmPanelState(s); hp != nil {
			swarmPanelMu.Lock()
			swarmOn = hp.state.Enabled
			swarmPanelMu.Unlock()
		}
	}
	if currentThread.ID == "" {
		// 待创建会话没有后端 Swarm 状态：开关取自待创建选项。
		s.mu.Lock()
		if p := s.pendingThread; p != nil {
			swarmOn = p.Options.SwarmEnabled
		}
		s.mu.Unlock()
	}

	// 胶囊固定显示模型短名，配对名放在悬停提示和面板里。
	modelTitle := modelPickerDisplayName(providers, currentProvider, leadSelection.Model)
	capsuleLabel := modelShortDisplayName(providers, currentProvider, leadSelection.Model)
	if composerChinTierFor(s, rowWidth) == composerChinNarrow {
		name := []rune(capsuleLabel)
		if len(name) > 8 {
			capsuleLabel = string(name[:8]) + "…"
		}
	}
	pairs := swarmPairsFromSettings(s, providers, pinnedModelPins(s))
	var pairTierOverrides map[string]config.SwarmTierOverride
	modelTooltip := "模型: " + modelTitle
	matchedPair := false
	if swarmOn {
		if pair, ok := currentSwarmPairFor(pairs, currentThread.PairID, currentProvider, leadSelection.Model, currentThread.BeesProvider, currentThread.BeesModel); ok {
			modelTooltip = swarmPairTooltip(pair, currentThread.Effort, providers)
			matchedPair = true
			pairTierOverrides = pair.BeeTiers
		}
	}
	if tip := effortSignalTip(leadInfo.Efforts, currentThread.Effort, leadInfo.DefaultEffort); tip != "" {
		modelTooltip += "\n" + tip
	}
	// 编组开启且子代理另配模型时，悬停提示补一行子代理全名与强度。
	if swarmOn && !beesFollowsLead && !matchedPair {
		beeInfo := modelPickerModelInfo(providers, currentThread.BeesProvider, currentThread.BeesModel)
		line := "子代理: " + modelPickerDisplayName(providers, currentThread.BeesProvider, currentThread.BeesModel)
		if tip := effortSignalTip(beeInfo.Efforts, currentThread.BeesEffort, beeInfo.DefaultEffort); tip != "" {
			line += " · " + tip
		}
		modelTooltip += "\n" + line
	}

	tiers := swarmTiersFromSettings(s)
	var tierOverrides map[string]config.SwarmTierOverride
	if strings.TrimSpace(currentThread.BeeTiers) != "" {
		_ = json.Unmarshal([]byte(currentThread.BeeTiers), &tierOverrides)
	}

	// 档位绑定的模型会覆盖会话子线程模型，胶囊文字只显示会话模型，差异补在悬停提示里。
	if swarmOn {
		tierBeesProvider, tierBeesModel := currentThread.BeesProvider, currentThread.BeesModel
		if tierBeesProvider == "" {
			tierBeesProvider = currentProvider
		}
		if tierBeesModel == "" {
			tierBeesModel = leadSelection.Model
		}
		if lines := tierModelDiffLines(tiers, pairTierOverrides, tierOverrides, tierBeesProvider, tierBeesModel); len(lines) > 0 {
			modelTooltip += "\n子代理档位另配模型：\n" + strings.Join(lines, "\n")
		}
	}

	threadID := currentThread.ID
	// 子代理输入框（嵌入的 bee composer）里改模型或强度不代表用户的新会话选择：
	// 不写入“上次使用的模型”，否则新建会话会被子代理模型顶替。只有主输入框才记忆。
	rememberLead := func(sel ModelSelection) {
		if s.embeddedBee {
			return
		}
		s.rememberModelSelectionForThread(threadID, sel)
	}
	pickerMode, pickerState := ModelPickerSwarm, composerModelPickerState
	if s.embeddedBee {
		// 子代理的输入框只选单个模型，不列配对，展开状态也用自己的一份，不与主输入框联动。
		pickerMode, pickerState = ModelPickerSimple, s.embeddedPicker
		pairs = nil
	}
	// 模型目录为空时，选择器里提供「重新探测」按钮：重新拉取供应商目录。
	renderModelPicker(c, modelPickerCatalogFor(s).apply(ModelPickerProps{
		Mode:              pickerMode,
		KeyPrefix:         composerKeyPrefix(s, threadID),
		ScopeKey:          threadID,
		State:             pickerState,
		Selection:         leadSelection,
		Bees:              beesSelection,
		SwarmOn:           swarmOn,
		HasMessages:       hasMessages,
		Disabled:          prefsSaving,
		LeadDisabled:      prefsSaving || s.modelPreferenceBusy(leadPin),
		BeesDisabled:      prefsSaving || s.modelPreferenceBusy(beesPin),
		Pairs:             pairs,
		PairID:            currentThread.PairID,
		MaxBees:           swarmMaxBees(s),
		Tiers:             tiers,
		TierOverrides:     tierOverrides,
		PairTierOverrides: pairTierOverrides,
		CapsuleLabel:      capsuleLabel,
		HideBeeName:       rowWidth > 0 && rowWidth < composerBeeNameMinWidth,
		NoEffortSignal:    composerChinTierFor(s, rowWidth) == composerChinNarrow,
		Tooltip:           modelTooltip,
		OnPickModel: func(side ModelPickerSide, prov ProviderInfo, m ModelInfo) {
			if side == ModelPickerSideLead {
				// 新模型配了默认强度就记它，否则沿用旧强度；不支持就记空强度。
				// 这里和 applyModelPickerPick 用同一个取值函数，保证“上次使用”记忆与会话实际强度一致。
				effort := s.defaultEffortForModel(prov.ID, m)
				if effort == "" {
					effort = currentThread.Effort
					if !modelSupportsEffort(m, effort) {
						effort = ""
					}
				}
				rememberLead(ModelSelection{Provider: prov.ID, Model: m.ID, Effort: effort, Fast: leadSelection.Fast})
			}
			s.applyModelPickerPick(threadID, side, currentProvider, prov, m)
		},
		OnEffort: func(side ModelPickerSide, effort string) {
			if side == ModelPickerSideBees {
				s.updateComposerPrefs(threadID, func(p *ComposerPrefs) { p.BeesEffort = effort }, "设置推理强度失败")
				s.setModelEffortPreference(beesPin, effort)
				return
			}
			s.updateComposerPrefs(threadID, func(p *ComposerPrefs) { p.Effort = effort }, "设置推理强度失败")
			s.setModelEffortPreference(leadPin, effort)
			rememberLead(ModelSelection{Provider: leadSelection.Provider, Model: leadSelection.Model, Effort: effort, Fast: leadSelection.Fast})
		},
		OnToggleFast: func(side ModelPickerSide) {
			if side == ModelPickerSideBees {
				s.toggleModelFastMode(beesPin)
				return
			}
			// 主导侧按会话切换快速模式（模型偏好不再跟随，只留给新建会话作默认值）。
			s.toggleThreadFastMode(threadID, leadSelection.Fast)
			rememberLead(ModelSelection{Provider: leadSelection.Provider, Model: leadSelection.Model, Effort: leadSelection.Effort, Fast: !leadSelection.Fast})
		},
		OnReset: func(side ModelPickerSide) {
			tab := "lead"
			pin := leadPin
			if side == ModelPickerSideBees {
				tab = "bees"
				pin = beesPin
				rememberSwarmBees("", "", "")
			} else {
				// 重置会清空强度与快速模式偏好，记录也随之回到默认。
				rememberLead(ModelSelection{Provider: leadSelection.Provider, Model: leadSelection.Model})
			}
			s.resetModelPreference(threadID, tab, pin)
		},
		OnFollowLead: func() {
			s.updateComposerPrefs(threadID, func(p *ComposerPrefs) {
				p.BeesProvider = ""
				p.BeesModel = ""
			}, "设置子代理模型失败")
			s.invalidate()
		},
		OnToggleSwarm: func(on bool) {
			s.enableSwarmWithGlobal(on)
		},
		OnApplyPair: func(pair SwarmPair) {
			s.applySwarmPair(threadID, pair)
		},
		OnSetMaxBees: func(n int) {
			if st := getChatSettings(s); st != nil {
				// 改 cfg 要同时持有 mu 与 cfgMu（顺序 mu → cfgMu），save 在两把锁之外调。
				st.mu.Lock()
				st.cfgMu.Lock()
				st.cfg.Orchestration.MaxBees = n
				st.cfgMu.Unlock()
				st.mu.Unlock()
				st.save()
			}
			s.invalidate()
		},
		OnSetTierOverride: func(tierID string, ov *config.SwarmTierOverride) {
			s.mu.Lock()
			curBeeTiers := ""
			for _, th := range s.threads {
				if th.ID == threadID {
					curBeeTiers = th.BeeTiers
					break
				}
			}
			s.mu.Unlock()

			newOverrides := make(map[string]config.SwarmTierOverride)
			if strings.TrimSpace(curBeeTiers) != "" {
				_ = json.Unmarshal([]byte(curBeeTiers), &newOverrides)
			}
			if ov == nil {
				delete(newOverrides, tierID)
				if composerModelPickerState != nil && composerModelPickerState.TierEditing == tierID {
					composerModelPickerState.TierEditing = ""
				}
			} else {
				newOverrides[tierID] = *ov
			}

			payload := ""
			if len(newOverrides) > 0 {
				if data, err := json.Marshal(newOverrides); err == nil {
					payload = string(data)
				}
			}

			s.mu.Lock()
			for i := range s.threads {
				if s.threads[i].ID == threadID {
					s.threads[i].BeeTiers = payload
					break
				}
			}
			for _, list := range s.threadsByProj {
				for i := range list {
					if list[i].ID == threadID {
						list[i].BeeTiers = payload
						break
					}
				}
			}
			// 待创建会话与真实会话共用同一个入口：在本创建时随选项下发。
			s.setPendingBeeTiersLocked(payload)
			s.mu.Unlock()
			s.invalidate()

			if s.client != nil && threadID != "" {
				go func(tid, p string) {
					if err := s.client.SetThreadBeeTiers(tid, p); err != nil {
						s.setToast("更新档位配置失败: " + err.Error())
					}
				}(threadID, payload)
			}
		},
		OnResetTierOverrides: func() {
			if composerModelPickerState != nil {
				composerModelPickerState.TierEditing = ""
			}
			payload := ""
			s.mu.Lock()
			for i := range s.threads {
				if s.threads[i].ID == threadID {
					s.threads[i].BeeTiers = payload
					break
				}
			}
			for _, list := range s.threadsByProj {
				for i := range list {
					if list[i].ID == threadID {
						list[i].BeeTiers = payload
						break
					}
				}
			}
			s.setPendingBeeTiersLocked(payload)
			s.mu.Unlock()
			s.invalidate()

			if s.client != nil && threadID != "" {
				go func(tid string) {
					if err := s.client.SetThreadBeeTiers(tid, ""); err != nil {
						s.setToast("重置档位配置失败: " + err.Error())
					}
				}(threadID)
			}
		},
		Invalidate: s.invalidate,
	}))
}

// rememberModelSelection 记录最近一次在模型选择组件中确认的选择，供新建会话与组件初始化复用。
func rememberModelSelection(sel ModelSelection) {
	saveLastModelSelection(sel)
}

// rememberModelSelectionForThread 记录“上次使用”的模型选择：全局一份，同时按该会话所属项目记一份。
// threadID 为空（待创建会话）时用待创建选项里的项目；两者都取不到项目时落到 projectSelectionNoneKey。
func (s *chatState) rememberModelSelectionForThread(threadID string, sel ModelSelection) {
	rememberModelSelection(sel)
	if s == nil {
		return
	}
	s.mu.Lock()
	projectID := s.projectIDForRememberLocked(threadID)
	s.mu.Unlock()
	saveProjectModelSelection(projectID, sel)
}

// projectIDForRememberLocked 返回用于写项目桶的项目 ID：优先会话自身所属项目，
// 会话不在列表里（待创建会话、空 ID）时退回待创建选项里的项目。调用方持有 s.mu。
func (s *chatState) projectIDForRememberLocked(threadID string) string {
	if threadID != "" {
		if id := s.projectIDForThreadLocked(threadID); id != "" {
			return id
		}
		if s.pendingThread != nil && s.pendingThread.Thread.ID == threadID {
			return s.pendingThread.Thread.ProjectID
		}
		return ""
	}
	if s.pendingThread != nil {
		return s.pendingThread.Thread.ProjectID
	}
	return ""
}

// rememberedFastFor 返回记忆里同一 provider+model 的快速模式；模型不同时按未开启处理。
func rememberedFastFor(provider, model string) bool {
	if last, ok := loadLastModelSelection(); ok && last.Provider == provider && last.Model == model {
		return last.Fast
	}
	return false
}

// threadSnapshotLocked 返回指定会话在本地列表里的副本，找不到时 ok=false。调用方需持有 s.mu。
func (s *chatState) threadSnapshotLocked(threadID string) (Thread, bool) {
	if threadID == "" {
		return Thread{}, false
	}
	for i := range s.threads {
		if s.threads[i].ID == threadID {
			return s.threads[i], true
		}
	}
	for _, list := range s.threadsByProj {
		for i := range list {
			if list[i].ID == threadID {
				return list[i], true
			}
		}
	}
	return Thread{}, false
}

// modelSupportsEffort 判断模型是否支持指定推理强度：空值表示跟随模型默认，总是支持。
func modelSupportsEffort(m ModelInfo, effort string) bool {
	if effort == "" {
		return true
	}
	for _, e := range m.Efforts {
		if e == effort {
			return true
		}
	}
	return false
}

// applyModelPickerPick 处理模型选择组件中点击某个模型后的动作：
// 执行侧只写入会话的 Bees 偏好；主导侧走单模型切换路径（provider 与当前一致时不重复下发）。
func (s *chatState) applyModelPickerPick(threadID string, side ModelPickerSide, currentProvider string, prov ProviderInfo, m ModelInfo) {
	// 新模型的默认强度（用户偏好 > 模型默认档）；为空表示该模型没有可用默认值，走下面的原有清理逻辑。
	targetEffort := s.defaultEffortForModel(prov.ID, m)
	if side == ModelPickerSideBees {
		s.updateComposerPrefs(threadID, func(p *ComposerPrefs) {
			p.BeesProvider = prov.ID
			p.BeesModel = m.ID
			// 新模型配了默认强度就直接套用，否则新模型不支持当前子代理推理强度时一并重置，避免旧模型的档位残留。
			if targetEffort != "" {
				p.BeesEffort = targetEffort
			} else if !modelSupportsEffort(m, p.BeesEffort) {
				p.BeesEffort = ""
			}
		}, "设置子代理模型失败")
		s.invalidate()
		return
	}

	reqProvider := ""
	if prov.ID != currentProvider {
		reqProvider = prov.ID
	}
	reqModel := m.ID
	newProvider := prov.ID

	// 主导侧选中的 Agent 就是“上次使用”，写回设置后当前进程的新建会话立即沿用。
	s.rememberUsedProvider(newProvider)

	s.mu.Lock()
	if s.isPendingThreadLocked(threadID) {
		// 待创建会话：只改本地占位会话，创建时随选项下发。
		s.setPendingAgentLocked(reqProvider, reqModel)
		// 旧模型的强度与快速模式未必被新模型支持，切换时按新模型能力就地重置。
		remembered := ModelSelection{}
		if p := s.pendingThread; p != nil {
			if targetEffort != "" {
				// 新模型有默认强度：会话与创建选项必须一起改，创建会话时下发的值是 Options。
				p.Thread.Effort = targetEffort
				p.Options.Effort = targetEffort
			} else if !modelSupportsEffort(m, p.Thread.Effort) {
				p.Thread.Effort = ""
				p.Options.Effort = ""
			}
			if !m.SupportsFast {
				s.setPendingFastLocked(false)
			}
			p.Thread.SpeedTier = ""
			remembered = ModelSelection{Provider: p.Thread.Provider, Model: p.Thread.Model, Effort: p.Thread.Effort, Fast: p.Thread.FastMode}
		}
		s.mu.Unlock()
		// 重置之后再记一次，避免把新模型不支持的旧强度留在记忆里。
		if !s.embeddedBee && remembered.Model != "" {
			s.rememberModelSelectionForThread(threadID, remembered)
		}
		s.invalidate()
		return
	}
	s.mu.Unlock()

	go func() {
		if err := s.client.SetThreadAgent(threadID, reqProvider, reqModel); err != nil {
			s.setToast("切换模型失败: " + err.Error())
			return
		}
		s.mu.Lock()
		// 切换前的强度/快速模式：模型切换不动这些字段，先读旧值，写入新模型后按新模型能力重置。
		prevEffort, prevFast, prevSpeedTier := "", false, ""
		if prev, ok := s.threadSnapshotLocked(threadID); ok {
			prevEffort, prevFast, prevSpeedTier = prev.Effort, prev.FastMode, prev.SpeedTier
		}
		delete(s.commandsCache, threadID)
		if s.completionThreadID == threadID {
			s.completionKind = completionNone
			s.completionItems = nil
			s.mentionSeq++
		}
		for i := range s.threads {
			if s.threads[i].ID == threadID {
				if reqProvider != "" {
					s.threads[i].Provider = newProvider
				}
				s.threads[i].Model = reqModel
				break
			}
		}
		for _, list := range s.threadsByProj {
			for i := range list {
				if list[i].ID == threadID {
					if reqProvider != "" {
						list[i].Provider = newProvider
					}
					list[i].Model = reqModel
					break
				}
			}
		}
		s.mu.Unlock()

		// 旧模型的强度、快速模式与速率档位未必被新模型支持：不支持就重置，避免参数残留到新模型。
		effortAfter := prevEffort
		if targetEffort != "" {
			// 新模型有默认强度：直接套用；与切换前一致时不重复下发，避免多余的 RPC 与界面抖动。
			effortAfter = targetEffort
			if targetEffort != prevEffort {
				s.updateComposerPrefs(threadID, func(p *ComposerPrefs) { p.Effort = targetEffort }, "重置推理强度失败")
			}
		} else if !modelSupportsEffort(m, prevEffort) {
			effortAfter = ""
			s.updateComposerPrefs(threadID, func(p *ComposerPrefs) { p.Effort = "" }, "重置推理强度失败")
		}
		if nextFast := prevFast && m.SupportsFast; nextFast != prevFast || prevSpeedTier != "" {
			if err := s.client.SetThreadFastMode(threadID, nextFast, ""); err != nil {
				s.setToast("重置快速模式失败: " + err.Error())
			} else {
				s.mu.Lock()
				s.applyThreadFastModeLocked(threadID, nextFast, "")
				s.mu.Unlock()
			}
		}
		// 重置之后按切换后的模型再记一次，避免记忆里留下未被支持的旧强度/旧快速模式。
		if !s.embeddedBee && newProvider != "" && reqModel != "" {
			s.rememberModelSelectionForThread(threadID, ModelSelection{Provider: newProvider, Model: reqModel, Effort: effortAfter, Fast: prevFast && m.SupportsFast})
		}
		s.invalidate()
	}()
}

// renderSwarmSideTabs 渲染顶部主导/执行分段控件，供斜杠命令与测试直接复用。
func renderSwarmSideTabs(c *ui.Context, s *chatState, th *Thread, swarmOn bool) {
	renderModelPickerSideTabs(c, ModelPickerProps{
		Mode:      ModelPickerSwarm,
		KeyPrefix: "composer-",
		ScopeKey:  th.ID,
		State:     composerModelPickerState,
		Selection: ModelSelection{Effort: th.Effort},
		Bees:      ModelSelection{Effort: th.BeesEffort},
		SwarmOn:   swarmOn,
		OnToggleSwarm: func(on bool) {
			s.enableSwarmWithGlobal(on)
		},
		Invalidate: s.invalidate,
	}, composerModelPickerState)
}

// getProviderQuotaUsed 从设置状态中获取指定 provider 的用量百分比缓存。
func getProviderQuotaUsed(s *chatState, providerID string) (float64, bool) {
	st := getChatSettings(s)
	if st == nil {
		return 0, false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.providerRows.quota == nil {
		return 0, false
	}
	windows, ok := st.providerRows.quota[providerID]
	if !ok || len(windows) == 0 {
		return 0, false
	}
	return windows[0].UsedPercent, true
}

// swarmPairsFromSettings 返回当前目录就绪的原始配对。
func swarmPairsFromSettings(s *chatState, providers []ProviderInfo, pins []ModelPin) []SwarmPair {
	st := getChatSettings(s)
	if st == nil {
		return nil
	}
	st.cfgMu.Lock()
	cfg := st.cfg
	st.cfgMu.Unlock()
	return readySwarmPairs(cfg, cfg.Orchestration.Pairs, providers, pins)
}

// swarmTiersFromSettings 读取全局设置中的 Swarm 档位列表。
func swarmTiersFromSettings(s *chatState) []config.SwarmTier {
	st := getChatSettings(s)
	if st == nil {
		return nil
	}
	return st.cfg.Orchestration.Tiers
}

// tierModelDiffLines 列出与会话子线程模型不同的档位绑定模型，供模型胶囊悬停提示说明。
// 未绑定模型的档位跟随会话子线程模型，不计入差异。
func tierModelDiffLines(tiers []config.SwarmTier, pairOverrides, sessionOverrides map[string]config.SwarmTierOverride, beesProvider, beesModel string) []string {
	var lines []string
	for _, tier := range tiers {
		prov, model, _, _ := effectiveTierModel(tier, pairOverrides, sessionOverrides)
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if prov == beesProvider && model == beesModel {
			continue
		}
		name := tier.Name
		if name == "" {
			name = tier.ID
		}
		line := name + "档位："
		if prov != "" {
			line += prov + "/"
		}
		lines = append(lines, line+model)
	}
	return lines
}

// swarmPanelEnabled 只读返回当前会话的 Swarm 面板开关状态。
func swarmPanelEnabled(s *chatState) bool {
	hp := getSwarmPanelState(s)
	if hp == nil {
		return false
	}
	swarmPanelMu.Lock()
	defer swarmPanelMu.Unlock()
	return hp.state.Enabled
}

// modelPreferencePin 为 CLI 的空默认模型 ID 提供可持久化的标识，不改变运行时模型 ID。
func modelPreferencePin(provider, modelID string) ModelPin {
	if modelID == "" {
		modelID = "default"
	}
	return ModelPin{Provider: provider, ModelID: modelID}
}

var iconChevronLeft = parseLucideSVG(`<path d="m15 18-6-6 6-6"/>`)

// contextUsageFraction 计算已用 tokens 占上下文窗口上限的比例（0.0 ~ 1.0）。
// 若 limit <= 0 或 used <= 0 则返回 0；超过上限时截断到 1.0。
func contextUsageFraction(used, limit int64) float64 {
	if limit <= 0 || used <= 0 {
		return 0
	}
	f := float64(used) / float64(limit)
	if f > 1.0 {
		return 1.0
	}
	return f
}

// contextUsagePercent 计算已用 tokens 占上下文窗口上限的百分比（0.0 ~ 100.0）。
func contextUsagePercent(used, limit int64) float64 {
	return contextUsageFraction(used, limit) * 100.0
}

// contextUsageTooltip 生成上下文圆环悬停提示文本，格式为「已用 X / 上限 Y tokens」。
func contextUsageTooltip(used, limit int64) string {
	return fmt.Sprintf("已用 %s / 上限 %s tokens", formatTokens(used), formatTokens(limit))
}

// makeContextMeterSVG 生成包含背景圆环与进度圆弧的矢量图。
// dark 参数用于保证在深色背景下底环和进度弧均清晰明亮。
// contextMeterSVGCache 缓存已解析的上下文用量圆环（键为外观与进度），上限 64 条后整表清空。
// 圆环每帧在每个 composer 里都画一次，进度以 0.01 周长为精度（与 dasharray 的两位小数一致），
// 不缓存就是每帧重新拼 SVG 文本并整份解析。
var (
	contextMeterSvgMu    sync.Mutex
	contextMeterSvgCache = make(map[string]*ui.SVG, 64)
)

const contextMeterSvgCacheMax = 64

func makeContextMeterSVG(dark bool, fraction float64, isWarning bool) *ui.SVG {
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1.0 {
		fraction = 1.0
	}
	// 周长 2 * π * r (r=9, 对应 24x24 viewBox)
	const circumference = 56.548667764616276
	dash := fraction * circumference
	gap := circumference - dash
	cacheKey := fmt.Sprintf("%t|%t|%.2f", dark, isWarning, dash)
	contextMeterSvgMu.Lock()
	cached := contextMeterSvgCache[cacheKey]
	contextMeterSvgMu.Unlock()
	if cached != nil {
		return cached
	}
	bgStroke := "rgba(255, 255, 255, 0.30)"
	activeStroke := "#60A5FA" // 亮蓝色，在深色背景下清晰可见
	if !dark {
		bgStroke = "rgba(0, 0, 0, 0.18)"
		activeStroke = "#2563EB"
	}
	if isWarning {
		activeStroke = "#F59E0B"
	}
	content := fmt.Sprintf(
		`<circle cx="12" cy="12" r="9" fill="none" stroke="%s" stroke-width="2.5"/>`+
			`<circle cx="12" cy="12" r="9" fill="none" stroke="%s" stroke-width="2.5" stroke-linecap="round" stroke-dasharray="%.2f %.2f" transform="rotate(-90 12 12)"/>`,
		bgStroke, activeStroke, dash, gap,
	)
	data := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24">%s</svg>`, content)
	svg := ui.MustParseSVG([]byte(data))
	contextMeterSvgMu.Lock()
	if len(contextMeterSvgCache) >= contextMeterSvgCacheMax {
		contextMeterSvgCache = make(map[string]*ui.SVG, contextMeterSvgCacheMax)
	}
	contextMeterSvgCache[cacheKey] = svg
	contextMeterSvgMu.Unlock()
	return svg
}

// latestTurnUsageLocked 返回会话里索引最大的一轮用量，即最近一轮真正进入模型的上下文。
// 调用方需持有 s.mu；不能直接拿 threadUsage.Total，那是全会话累计计费，多轮后会远超实际占用。
func latestTurnUsageLocked(u ThreadUsage) (TokenUsage, bool) {
	latest := -1
	var usage TokenUsage
	for idx, turn := range u.Turns {
		if idx > latest {
			latest = idx
			usage = turn
		}
	}
	return usage, latest >= 0
}

// currentThreadContextTokens 获取当前会话的上下文占用与上下文上限 tokens。
// 占用优先取该轮最后一次模型请求实际带入的上下文（ContextTokens），拿不到时退回
// 最新一轮的输入 + 输出；不是全会话累计计费的 Total。
// 上限优先取最新一轮用量里 CLI 上报的 ContextWindow（实测值，能认出 1M 变体、聚合型 CLI 的模型名），
// 没有时取当前会话模型在模型目录（models.dev）里的 ContextLimit；都拿不到时返回 ok=false，
// 不再用固定默认值兜底，否则小窗口模型被低估、大窗口模型被高估，圆环比例完全失真。
func currentThreadContextTokens(s *chatState, currentThread *Thread) (used int64, limit int64, ok bool) {
	if s == nil || currentThread == nil {
		return 0, 0, false
	}
	s.mu.Lock()
	turn, hasTurn := latestTurnUsageLocked(s.threadUsage)
	for _, p := range s.providers {
		if p.ID == currentThread.Provider {
			limit = resolveModelInfo(p.Models, currentThread.Model).ContextLimit
			if limit <= 0 {
				// 模型 id 对不上目录（pi/omp 等聚合型 CLI 的模型名带上游前缀、目录里只有占位行）时，
				// 退回该 provider 目录里任一带窗口的行，避免圆环恒显示「暂无上下文用量数据」。
				// 目录里已经有该模型、只是没给出上限时仍报无数据，不拿别的模型顶分母。
				if _, found := findModelInfo(p.Models, currentThread.Model); !found {
					limit = firstModelContextLimit(p.Models)
				}
			}
			break
		}
	}
	s.mu.Unlock()

	if !hasTurn {
		return 0, limit, false
	}
	used = turn.Input + turn.Output
	// agy 等 provider 收尾上报的 Input 是整轮多步累计的计费用量（可远超窗口上限），
	// 单次请求实际带入的上下文由 ContextTokens 单独给出；缺省为 0 时沿用旧口径。
	if turn.ContextTokens > 0 {
		used = turn.ContextTokens
	}
	// CLI 上报的窗口上限比目录里的静态值准（例如 1M 变体、目录里只有占位行的聚合型 CLI），
	// 有值时优先用它；为 0 时沿用上面按目录解析出的 limit。
	if turn.ContextWindow > 0 {
		limit = turn.ContextWindow
	}
	if limit <= 0 {
		return used, 0, false
	}
	return used, limit, true
}

// firstModelContextLimit 返回目录里第一个已知的上下文上限，未知时返回 0。
func firstModelContextLimit(models []ModelInfo) int64 {
	for _, m := range models {
		if m.ContextLimit > 0 {
			return m.ContextLimit
		}
	}
	return 0
}

// renderContextMeter 渲染上下文用量圆环，并以圆环为锚点打开用量面板。
func renderContextMeter(c *ui.Context, s *chatState, currentThread *Thread) {
	if currentThread == nil {
		return
	}
	used, limit, ok := currentThreadContextTokens(s, currentThread)
	fraction := contextUsageFraction(used, limit)
	tooltip := contextUsageTooltip(used, limit)
	if !ok || limit <= 0 {
		tooltip = "暂无上下文用量数据"
	}
	isWarning := fraction > 0.85

	meter := ui.ButtonBase(c.Key("composer-context-meter")).
		Label("上下文用量").
		Tooltip(tooltip).
		Size(composerActionBtnSize, composerActionBtnSize).
		Radius(RadiusMD).
		OnClick(func() {
			s.composerView.usageOpen = !s.composerView.usageOpen
		})
	composerHoverBg(c, meter, s.composerView.usageOpen || meter.Hovered())

	meter.Children(func() {
		ui.Image(c, makeContextMeterSVG(isDark(c), fraction, isWarning)).
			Size(composerContextMeterImageSize, composerContextMeterImageSize).
			Fit(ui.Contain)
	})
	appPopover(c.Key(composerKeyPrefix(s, currentThread.ID)+"usage-popover"), meter, &s.composerView.usageOpen, func() {
		s.composerView.usageOpen = false
	}, func() {
		renderUsagePanel(c, s, currentThread)
	})
}

// composerChinTier 按底栏实测宽度选择收缩档位；首帧宽度未知时保留完整布局。
type composerChinSize int

const (
	composerChinFull composerChinSize = iota
	composerChinNoShortcut
	composerChinIcons
	composerChinNarrow
)

// composerChinTierFor 在通用档位之上给浮动面板里的子代理输入框放宽一档：
// 浮窗只有单模型胶囊，主输入框的阈值对它偏紧，所以同等宽度下少收一档。
func composerChinTierFor(s *chatState, width float32) composerChinSize {
	tier := composerChinTier(width)
	if s != nil && s.embeddedBee && tier > composerChinFull {
		return tier - 1
	}
	return tier
}

func composerChinTier(width float32) composerChinSize {
	switch {
	case width <= 0 || width >= 576:
		return composerChinFull
	case width >= 480:
		return composerChinNoShortcut
	case width >= 384:
		return composerChinIcons
	default:
		return composerChinNarrow
	}
}
