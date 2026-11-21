package mcodeui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

// kanbanBadgeRefreshInterval 是底栏看板待审阅徽标的后台刷新间隔。
const kanbanBadgeRefreshInterval = 30 * time.Second

// sidebarFooterCompactWidth 是底栏由文字按钮切换为紧凑图标按钮的可用宽度阈值（DIP）。
const sidebarFooterCompactWidth float32 = 280

// sidebarFooterState 维护底栏浮层的展开状态与看板待审阅徽标缓存，按 chatState 附加（同 chatHeaderState）。
// addProjectOpen 绑定在控件上，只在 UI 线程读写；
// kanbanReviewCount、kanbanFetchedAt、kanbanLoading 由后台 goroutine 写入，受 sidebarFooterMu 保护。
type sidebarFooterState struct {
	addProjectOpen     bool
	chatAddProjectOpen bool
	updateVisible      bool
	updateOpen         bool
	whatsNewOpen       bool
	whatsNewChecked    bool
	whatsNewVersion    string
	whatsNewSection    string

	kanbanReviewCount int
	kanbanFetchedAt   time.Time
	kanbanLoading     bool
}

var (
	sidebarFooterMu     sync.Mutex
	sidebarFooterStates = make(map[*chatState]*sidebarFooterState)
)

// getSidebarFooterState 返回 chatState 对应的底栏状态，首次访问时创建。
func getSidebarFooterState(s *chatState) *sidebarFooterState {
	sidebarFooterMu.Lock()
	defer sidebarFooterMu.Unlock()
	st, ok := sidebarFooterStates[s]
	if !ok {
		st = &sidebarFooterState{}
		sidebarFooterStates[s] = st
	}
	return st
}

// renderUnreadDot 渲染 6x6 的强调色圆点；样式与侧边栏会话行的未读点一致，返回元素以便调用方继续定位。
func renderUnreadDot(c *ui.Context, key string) ui.Element {
	return ui.Box(c.Key(key)).
		Size(6, 6).
		Radius(RadiusXS).
		Background(ThemeAccent(c))
}

// renderSidebarBottomBar 渲染底部常驻工具栏：
// 添加项目（浮层：选择文件夹 / 从 GitHub 克隆）、
// 任务与看板（带待审阅圆点）、自动化、设置齿轮与源码编辑器图标按钮。
func renderSidebarBottomBar(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	count, ok := sidebarKanbanAttentionCount(s)
	if !ok {
		refreshKanbanBadge(s)
		count = sidebarKanbanReviewCount(s)
	}

	fs := getSidebarFooterState(s)
	s.mu.Lock()
	sbW := s.sidebarWidth
	s.mu.Unlock()
	winW, _ := c.Size()
	availW := sbW
	if availW <= 0 || (winW > 0 && winW < availW) {
		availW = winW
	}
	compact := availW > 0 && availW < sidebarFooterCompactWidth

	renderAction := func(key, label, tooltip string, icon *ui.SVG, onClick func()) ui.Element {
		if compact {
			if tooltip == "" {
				tooltip = label
			}
			return IconButton(c, IconButtonProps{
				Key:     key,
				Icon:    icon,
				Size:    ControlSM,
				Variant: ButtonGhost,
				Label:   label,
				Tooltip: tooltip,
				OnClick: onClick,
			})
		}
		return Button(c, ButtonOpts{
			Key:     key,
			Text:    label,
			Icon:    icon,
			Size:    ControlSM,
			Variant: ButtonGhost,
			Label:   label,
			Tooltip: tooltip,
			OnClick: onClick,
		})
	}

	ui.Row(c.Key("sidebar-bottom-bar")).
		FillWidth().
		MinWidth(0).
		Height(HeightBottomBar).
		Margin(Space1, ListInset, Space3, ListInset).
		Gap(Space2).
		AlignItems(ui.Center).
		Children(func() {
			// 点击展开「选择文件夹 / 从 GitHub 克隆」浮层。
			addBtn := renderAction("sidebar-add-project-btn", "添加项目", "添加项目", IconPlus(), func() {
				togglePopoverOpen(&fs.addProjectOpen)
				s.invalidate()
			})
			appPopover(c.Key("sidebar-add-project-popover"), addBtn, &fs.addProjectOpen, func() {
				fs.addProjectOpen = false
			}, func() {
				renderAddProjectPopover(c, s)
			})

			ui.Spacer(c)

			// 任务与看板入口：待审阅任务数大于 0 时在按钮右上角显示强调色小圆点
			ui.Row(c.Key("sidebar-kanban-wrapper")).Children(func() {
				renderAction("sidebar-kanban-btn", "任务", "", IconColumns3(), func() {
					s.openTasksWindow("tasks")
				})
				if count > 0 {
					ui.Box(c.Key("sidebar-kanban-review-dot")).
						Background(ThemeHover(c)).Radius(RadiusSM).
						Attach(ui.AnchorTopRight, ui.AnchorCenter).Children(func() {
						rollingNumber(c, fmt.Sprintf("sidebar-kanban-count-%p", s), count)
					})
				}
			})

			renderAction("sidebar-automations-btn", "自动化", "", IconCalendarClock(), func() {
				s.openTasksWindow("automations")
			})

			// 设置入口图标按钮
			if st := getChatSettings(s); st != nil {
				initWhatsNew(s, st, fs)
				settingsBtn := IconButton(c, IconButtonProps{
					Key:     "sidebar-settings-btn",
					Icon:    IconSettings(),
					Size:    ControlSM,
					Variant: ButtonGhost,
					Label:   "设置",
					OnClick: func() {
						openSettingsWindow(s, "")
					},
				})
				appPopoverAbove(c.Key("sidebar-whats-new-popover"), settingsBtn, &fs.whatsNewOpen, nil, func() {
					renderUpdatePopover(c, s, st, "已更新到 "+fs.whatsNewVersion, fs.whatsNewSection, nil)
				})
				status, _ := latestUpdateStatus(s.client)
				if status.Available {
					renderUpdateCapsule(c, s, st, fs, status)
				} else {
					fs.updateOpen = false
				}
				fs.updateVisible = status.Available
			}

			// 源码编辑器图标按钮（与 ⇧⌘E 快捷键同一入口）。
			IconButton(c, IconButtonProps{
				Key:     "sidebar-editor-btn",
				Icon:    IconPencilLine(),
				Size:    ControlSM,
				Variant: ButtonGhost,
				Label:   "源码编辑器 " + shortcutDisplay("⇧⌘E"),
				Tooltip: "源码编辑器 " + shortcutDisplay("⇧⌘E"),
				OnClick: func() {
					openMiniEditorFromPicker(s)
					s.invalidate()
				},
			})
		})
}

// renderAddProjectPopover 渲染「添加项目」浮层：选择本地文件夹或从 GitHub 克隆。
func renderAddProjectPopover(c *ui.Context, s *chatState) {
	fs := getSidebarFooterState(s)
	popoverCard(c, ui.Column(c.Key("sidebar-add-project-menu"))).
		Gap(Space0).
		Padding(Space1).
		MinWidth(190).
		FocusGroup(ui.Vertical).
		Children(func() {
			menuRow(c, menuRowProps{
				Key:   "sidebar-add-project-folder",
				Label: "选择文件夹…",
				Title: "选择文件夹…",
				OnClick: func() {
					fs.addProjectOpen = false
					fs.chatAddProjectOpen = false
					s.promptAddProject()
					s.invalidate()
				},
			})
			menuRow(c, menuRowProps{
				Key:   "sidebar-add-project-clone",
				Label: "从 GitHub 克隆…",
				Title: "从 GitHub 克隆…",
				OnClick: func() {
					fs.addProjectOpen = false
					fs.chatAddProjectOpen = false
					openGitHubClonePanel(s)
					s.invalidate()
				},
			})
		})
}

// sidebarKanbanReviewCount 返回缓存的待审阅任务数（Column 为 review 或带 CanReview 标记）。
func sidebarKanbanReviewCount(s *chatState) int {
	if s == nil {
		return 0
	}
	sidebarFooterMu.Lock()
	defer sidebarFooterMu.Unlock()
	st := sidebarFooterStates[s]
	if st == nil {
		return 0
	}
	return st.kanbanReviewCount
}

func sidebarKanbanAttentionCount(s *chatState) (int, bool) {
	if _, ok := s.attentionClient(); !ok {
		return 0, false
	}
	s.refreshAttentionInbox()
	s.ensureAttentionWatch()
	st := getAttentionInboxState(s)
	st.mu.Lock()
	defer st.mu.Unlock()
	count := 0
	for _, item := range st.items {
		if strings.HasPrefix(item.Kind, "kanban_") {
			count++
		}
	}
	return count, true
}

// sidebarAttentionCount 合并统计看板中需要处理的任务和待审阅目标，并按 ID 去重。
func sidebarAttentionCount(board KanbanBoard) int {
	tasks, goals := make(map[string]bool), make(map[string]bool)
	for _, task := range board.Tasks {
		needs := task.Column == "review" || task.CanReview || task.Status == "review" || task.Status == "failed" || task.Status == "needs_approval" || task.Status == "awaiting_input"
		for _, flag := range task.AttentionFlags {
			switch flag {
			case "awaiting_approval", "awaiting_input", "failed", "needs_review":
				needs = true
			}
		}
		if needs {
			tasks[task.ID] = true
		}
	}
	for _, goal := range board.Goals {
		if goal.Status == "plan_review" {
			goals[goal.ID] = true
		}
	}
	return len(tasks) + len(goals)
}

// refreshKanbanBadge 在无进行中的请求、且距上次刷新超过 30 秒（或从未刷新）时，
// 异步拉取看板并更新待审阅任务数；client 为空或请求出错时静默跳过。
func refreshKanbanBadge(s *chatState) {
	if s == nil || s.client == nil {
		return
	}
	st := getSidebarFooterState(s)

	sidebarFooterMu.Lock()
	if st.kanbanLoading {
		sidebarFooterMu.Unlock()
		return
	}
	if !st.kanbanFetchedAt.IsZero() && time.Since(st.kanbanFetchedAt) < kanbanBadgeRefreshInterval {
		sidebarFooterMu.Unlock()
		return
	}
	st.kanbanLoading = true
	sidebarFooterMu.Unlock()

	go func() {
		ctx := context.Background()
		board, err := s.client.KanbanBoard(ctx, "")
		sidebarFooterMu.Lock()
		st.kanbanLoading = false
		st.kanbanFetchedAt = time.Now()
		if err == nil {
			st.kanbanReviewCount = sidebarAttentionCount(board)
		}
		sidebarFooterMu.Unlock()
		s.invalidate()
	}()
}
