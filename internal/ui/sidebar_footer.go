package mcodeui

import (
	"context"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

// kanbanBadgeRefreshInterval 是底栏看板待审阅徽标的后台刷新间隔。
const kanbanBadgeRefreshInterval = 30 * time.Second

// sidebarFooterState 维护底栏两个浮层的展开状态与看板待审阅徽标缓存，按 chatState 附加（同 chatHeaderState）。
// addProjectOpen、moreMenuOpen 绑定在控件上，只在 UI 线程读写；
// kanbanReviewCount、kanbanFetchedAt、kanbanLoading 由后台 goroutine 写入，受 sidebarFooterMu 保护。
type sidebarFooterState struct {
	addProjectOpen bool
	moreMenuOpen   bool
	updateVisible  bool

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
// 任务与看板（带待审阅圆点）、自动化、设置齿轮与更多菜单。
func renderSidebarBottomBar(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	// 异步刷新看板待审阅数量，供本帧与后续帧的圆点使用（30 秒节流，出错静默）。
	refreshKanbanBadge(s)

	fs := getSidebarFooterState(s)

	ui.Row(c.Key("sidebar-bottom-bar")).
		Height(HeightBottomBar).
		Margin(Space1, ListInset, Space3, ListInset).
		Gap(Space2).
		AlignItems(ui.Center).
		Children(func() {
			// 点击展开「选择文件夹 / 从 GitHub 克隆」浮层。
			addBtn := Button(c, ButtonOpts{
				Key:     "sidebar-add-project-btn",
				Text:    "新建",
				Icon:    IconPlus(),
				Size:    ControlSM,
				Variant: ButtonGhost,
				Label:   "添加项目",
				Tooltip: "添加项目",
				OnClick: func() {
					fs.addProjectOpen = !fs.addProjectOpen
					fs.moreMenuOpen = false
					s.invalidate()
				},
			})
			appPopover(c.Key("sidebar-add-project-popover"), addBtn, &fs.addProjectOpen, func() {
				fs.addProjectOpen = false
			}, func() {
				renderAddProjectPopover(c, s)
			})

			ui.Spacer(c)

			// 任务与看板入口：待审阅任务数大于 0 时在按钮右上角显示强调色小圆点
			ui.Row(c.Key("sidebar-kanban-wrapper")).Children(func() {
				IconButton(c, IconButtonProps{
					Key:     "sidebar-kanban-btn",
					Icon:    IconColumns3(),
					Size:    ControlSM,
					Variant: ButtonGhost,
					Label:   "任务与看板",
					OnClick: func() {
						s.openTasksWindow("tasks")
					},
				})
				if sidebarKanbanReviewCount(s) > 0 {
					renderUnreadDot(c, "sidebar-kanban-review-dot").
						Attach(ui.AnchorTopRight, ui.AnchorCenter)
				}
			})

			IconButton(c, IconButtonProps{
				Key:     "sidebar-automations-btn",
				Icon:    IconCalendarClock(),
				Size:    ControlSM,
				Variant: ButtonGhost,
				Label:   "自动化",
				OnClick: func() {
					s.openTasksWindow("automations")
				},
			})

			// 设置入口图标按钮
			if st := getChatSettings(s); st != nil {
				ui.Row(c.Key("sidebar-settings-wrapper")).Children(func() {
					IconButton(c, IconButtonProps{
						Key:     "sidebar-settings-btn",
						Icon:    IconSettings(),
						Size:    ControlSM,
						Variant: ButtonGhost,
						Label:   "设置",
						OnClick: func() {
							st.Open()
						},
					})
					available := updateAvailable(s.client)
					if available {
						renderUpdateDot(c, "sidebar-settings-update-dot", !fs.updateVisible).Attach(ui.AnchorTopRight, ui.AnchorCenter)
					}
					fs.updateVisible = available
				})
			}

			// 更多操作图标按钮：点击展开更多菜单
			moreBtn := IconButton(c, IconButtonProps{
				Key:     "sidebar-more-btn",
				Icon:    IconMoreHorizontal(),
				Size:    ControlSM,
				Variant: ButtonGhost,
				Label:   "更多",
				Active:  fs.moreMenuOpen,
				OnClick: func() {
					fs.moreMenuOpen = !fs.moreMenuOpen
					fs.addProjectOpen = false
					s.invalidate()
				},
			})
			appPopover(c.Key("sidebar-more-popover"), moreBtn, &fs.moreMenuOpen, func() {
				fs.moreMenuOpen = false
			}, func() {
				renderSidebarMoreMenu(c, s)
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
		Children(func() {
			renderGitMenuOption(c, "sidebar-add-project-folder", "选择文件夹…", "选择文件夹…", "", false, false, func() {
				fs.addProjectOpen = false
				s.promptAddProject()
				s.invalidate()
			})
			renderGitMenuOption(c, "sidebar-add-project-clone", "从 GitHub 克隆…", "从 GitHub 克隆…", "", false, false, func() {
				fs.addProjectOpen = false
				openGitHubClonePanel(s)
				s.invalidate()
			})
		})
}

// renderSidebarMoreMenu 渲染「更多」浮层：源码编辑器入口（与 ⇧⌘E 快捷键同一函数）。
func renderSidebarMoreMenu(c *ui.Context, s *chatState) {
	fs := getSidebarFooterState(s)
	popoverCard(c, ui.Column(c.Key("sidebar-more-menu"))).
		Gap(Space0).
		Padding(Space1).
		MinWidth(190).
		Children(func() {
			renderGitMenuOption(c, "sidebar-more-editor", "源码编辑器", "源码编辑器", "⇧⌘E", false, false, func() {
				fs.moreMenuOpen = false
				openMiniEditorFromPicker(s)
				s.invalidate()
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
			count := 0
			for _, task := range board.Tasks {
				if task.Column == "review" || task.CanReview {
					count++
				}
			}
			st.kanbanReviewCount = count
		}
		sidebarFooterMu.Unlock()
		s.invalidate()
	}()
}
