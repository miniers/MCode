package mcodeui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// lastBulkInfo 记录最近一次批量或单条操作的信息，用于侧边栏撤销条与项目右键菜单。
type lastBulkInfo struct {
	projectID string
	token     string
	kind      string // "archive"、"dormant" 或 "read"
	count     int
	at        time.Time
}

// bulkPreviewResult 记录整理浮窗的筛选结果与跳过的运行中会话数。
type bulkPreviewResult struct {
	affected       []Thread
	skippedRunning int
}

// previewBulkThreads 根据时间范围与筛选选项计算受影响的会话和跳过会话数。
func previewBulkThreads(threads []Thread, days int, includePinned, includeDormant bool) bulkPreviewResult {
	var res bulkPreviewResult
	var cutoff time.Time
	if days > 0 {
		cutoff = time.Now().AddDate(0, 0, -days)
	}

	for _, th := range threads {
		if days > 0 {
			if !th.UpdatedAt.IsZero() && !th.UpdatedAt.Before(cutoff) {
				continue
			}
		}
		if th.Pinned && !includePinned {
			continue
		}
		if th.Dormant && !includeDormant {
			continue
		}
		if th.IsRunning || th.SwarmBusy {
			res.skippedRunning++
			continue
		}
		res.affected = append(res.affected, th)
	}
	return res
}

// renderBulkPopover 渲染整理会话浮窗，外观按性能模式无毛玻璃设计。
func renderBulkPopover(c *ui.Context, s *chatState, projectID string) {
	primaryText := ThemePrimaryText(c)
	secondaryText := ThemeSecondaryText(c)

	s.mu.Lock()
	days := s.bulkRangeDays
	incPinned := s.bulkIncludePinned
	incDormant := s.bulkIncludeDormant
	ths := s.threadsByProj[projectID]
	s.mu.Unlock()

	preview := previewBulkThreads(ths, days, incPinned, incDormant)
	canExecute := len(preview.affected) > 0

	previewText := fmt.Sprintf("将影响 %d 个会话", len(preview.affected))
	if preview.skippedRunning > 0 {
		previewText += fmt.Sprintf("，跳过 %d 个运行中的会话", preview.skippedRunning)
	}

	card := ui.Column(c.Key("bulk-popover-card")).
		Width(310).
		Padding(Space4).
		Gap(Space3).
		Background(ThemeSurface(c)).
		Border(1, ThemeBorder(c)).
		Radius(RadiusMD).
		Children(func() {
			// 标题
			ui.Text(c, "整理会话").
				FontSize(scaledFont(TypeProse)).
				FontWeight(600).
				TextColor(primaryText)

			// 时间范围分段选择器
			ui.Text(c, "最后活跃于").
				FontSize(scaledFont(TypeFootnote)).
				TextColor(secondaryText)

			ranges := []struct {
				label string
				days  int
			}{
				{"1 天", 1},
				{"3 天", 3},
				{"7 天", 7},
				{"30 天", 30},
				{"全部", 0},
			}

			ui.Row(c.Key("bulk-range-segmented")).
				FillWidth().
				Radius(RadiusSM).
				Background(ThemeHover(c).Alpha(0.25)).
				Padding(Space0).
				Gap(Space0).
				Children(func() {
					for _, r := range ranges {
						rVal := r
						isSelected := (days == rVal.days)
						btn := ui.ButtonBase(c.Key("bulk-range-" + rVal.label)).
							Grow(1).
							Height(ControlSM).
							Radius(RadiusXS).
							Center().
							Label(rVal.label).
							OnClick(func() {
								s.mu.Lock()
								s.bulkRangeDays = rVal.days
								s.mu.Unlock()
								s.invalidate()
							})
						if isSelected {
							btn.Background(ThemeSelected(c))
						} else if btn.Hovered() {
							btn.Background(ThemeHover(c))
						}
						btn.Children(func() {
							textColor := primaryText
							if isSelected {
								textColor = ThemeAccent(c)
							}
							t := ui.Text(c, rVal.label).
								FontSize(scaledFont(TypeFootnote)).
								TextColor(textColor)
							if isSelected {
								t.FontWeight(600)
							}
						})
					}
				})

			// 包含置顶与包含已休眠复选项
			ui.Column(c.Key("bulk-checkboxes")).
				Gap(Space2).
				Padding(Space0, 0).
				Children(func() {
					// 包含置顶
					pinnedRow := ui.Row(c.Key("bulk-check-pinned")).
						Gap(Space3).
						AlignItems(ui.Center).
						Radius(RadiusXS).
						Label("包含置顶")
					if pinnedRow.Hovered() {
						pinnedRow.Background(ThemeHover(c).Alpha(0.2))
					}
					pinnedRow.OnClick(func() {
						s.mu.Lock()
						s.bulkIncludePinned = !s.bulkIncludePinned
						s.mu.Unlock()
						s.invalidate()
					})
					pinnedRow.Children(func() {
						box := ui.Box(c.Key("bulk-check-pinned-box")).
							Size(14, 14).
							Radius(RadiusXS).
							Center()
						if incPinned {
							box.Background(ThemeAccent(c))
							box.Children(func() {
								ui.Icon(c, IconCheck()).
									Size(10, 10).
									TextColor(ThemeOnAccent(c))
							})
						} else {
							box.Border(1, ThemeBorder(c)).
								Background(ThemeHover(c).Alpha(0.1))
						}
						ui.Text(c, "包含置顶").
							FontSize(scaledFont(TypeFootnote)).
							TextColor(primaryText)
					})

					// 包含已休眠
					dormantRow := ui.Row(c.Key("bulk-check-dormant")).
						Gap(Space3).
						AlignItems(ui.Center).
						Radius(RadiusXS).
						Label("包含已休眠")
					if dormantRow.Hovered() {
						dormantRow.Background(ThemeHover(c).Alpha(0.2))
					}
					dormantRow.OnClick(func() {
						s.mu.Lock()
						s.bulkIncludeDormant = !s.bulkIncludeDormant
						s.mu.Unlock()
						s.invalidate()
					})
					dormantRow.Children(func() {
						box := ui.Box(c.Key("bulk-check-dormant-box")).
							Size(14, 14).
							Radius(RadiusXS).
							Center()
						if incDormant {
							box.Background(ThemeAccent(c))
							box.Children(func() {
								ui.Icon(c, IconCheck()).
									Size(10, 10).
									TextColor(ThemeOnAccent(c))
							})
						} else {
							box.Border(1, ThemeBorder(c)).
								Background(ThemeHover(c).Alpha(0.1))
						}
						ui.Text(c, "包含已休眠").
							FontSize(scaledFont(TypeFootnote)).
							TextColor(primaryText)
					})
				})

			// 预览文字
			ui.Text(c, previewText).
				FontSize(scaledFont(TypeFootnote)).
				TextColor(secondaryText).
				Padding(Space0, 0)

			// 底部操作按钮：标为已读与归档
			ui.Row(c.Key("bulk-popover-actions")).
				FillWidth().
				Justify(ui.End).
				Gap(Space3).
				Children(func() {
					Button(c.Key("bulk-btn-mark-read"), ButtonOpts{
						Text:     "标为已读",
						Variant:  ButtonSecondary,
						Size:     ControlSM,
						Disabled: !canExecute,
						OnClick: func() {
							var ids []string
							for _, th := range preview.affected {
								ids = append(ids, th.ID)
							}
							s.mu.Lock()
							s.bulkPopoverProjectID = ""
							s.mu.Unlock()
							s.bulkMarkRead(projectID, ids)
							s.invalidate()
						},
					})

					Button(c.Key("bulk-btn-archive"), ButtonOpts{
						Text:     "归档",
						Variant:  ButtonPrimary,
						Size:     ControlSM,
						Disabled: !canExecute,
						OnClick: func() {
							var ids []string
							for _, th := range preview.affected {
								ids = append(ids, th.ID)
							}
							s.mu.Lock()
							s.bulkPopoverProjectID = ""
							s.mu.Unlock()
							s.bulkArchive(projectID, ids)
							s.invalidate()
						},
					})
				})
		})
	// 阴影取统一浮层层级 ElevPopover，不再写死一档 rgba 阴影。
	card = applyElevation(c, card, ElevPopover)
}

// renderBulkUndoNotice 在侧边栏底部栏上方渲染一条不透明撤销提示条。
func renderBulkUndoNotice(c *ui.Context, s *chatState) {
	s.mu.Lock()
	lb := s.lastBulk
	s.mu.Unlock()

	if lb == nil || time.Since(lb.at) > 10*time.Second {
		return
	}

	noticeText := fmt.Sprintf("已将 %d 个会话标为已读", lb.count)
	if lb.kind == "archive" {
		noticeText = fmt.Sprintf("已归档 %d 个会话", lb.count)
	}
	if lb.kind == "dormant" {
		noticeText = fmt.Sprintf("已休眠 %d 个会话", lb.count)
	}

	ui.Row(c.Key("sidebar-bulk-undo-notice")).
		Height(ControlLG).
		Margin(Space0, ListInset, Space1, ListInset).
		Padding(0, Space3).
		Background(ThemeSurface(c)).
		Border(1, ThemeBorder(c)).
		Radius(RadiusSM).
		AlignItems(ui.Center).
		Children(func() {
			ui.Text(c, noticeText).
				FontSize(scaledFont(TypeFootnote)).
				TextColor(ThemePrimaryText(c)).
				Grow(1).
				SingleLine()

			if lb.token != "" {
				Button(c.Key("sidebar-bulk-undo-btn"), ButtonOpts{
					Text:    "撤销",
					Size:    ControlXS,
					Variant: ButtonSecondary,
					OnClick: func() {
						s.undoLastBulk()
					},
				})
			}
		})
}

// bulkMarkRead 批量标记项目内指定会话为已读。
func (s *chatState) bulkMarkRead(projectID string, threadIDs []string) {
	if len(threadIDs) == 0 {
		return
	}
	go func() {
		ctx := context.Background()
		err := s.client.BulkMarkRead(ctx, projectID, threadIDs)
		if err != nil {
			s.setToast("标记已读失败: " + err.Error())
			return
		}
		s.mu.Lock()
		targetMap := make(map[string]bool, len(threadIDs))
		for _, id := range threadIDs {
			targetMap[id] = true
		}
		if list, ok := s.threadsByProj[projectID]; ok {
			for i := range list {
				if targetMap[list[i].ID] {
					list[i].HasUnread = false
				}
			}
		}
		for i := range s.threads {
			if targetMap[s.threads[i].ID] {
				s.threads[i].HasUnread = false
			}
		}
		// 「标记为已读」等同于用户已经查看过：完成小红点一并撤掉。
		for _, id := range threadIDs {
			s.clearTurnFinishedLocked(id)
		}
		s.lastBulk = &lastBulkInfo{
			projectID: projectID,
			token:     "",
			kind:      "read",
			count:     len(threadIDs),
			at:        time.Now(),
		}
		s.mu.Unlock()
		s.scheduleLastBulkExpiry()
		s.invalidate()
	}()
}

// bulkArchive 批量归档项目内指定会话。
func (s *chatState) bulkArchive(projectID string, threadIDs []string) {
	if len(threadIDs) == 0 {
		return
	}
	go func() {
		ctx := context.Background()
		token, err := s.client.BulkArchive(ctx, projectID, threadIDs)
		if err != nil {
			s.setToast("归档失败: " + err.Error())
			return
		}
		s.mu.Lock()
		targetMap := make(map[string]bool, len(threadIDs))
		for _, id := range threadIDs {
			targetMap[id] = true
		}
		if list, ok := s.threadsByProj[projectID]; ok {
			var remaining []Thread
			for _, th := range list {
				if !targetMap[th.ID] {
					remaining = append(remaining, th)
				}
			}
			s.threadsByProj[projectID] = remaining
		}
		var remainingAll []Thread
		for _, th := range s.threads {
			if !targetMap[th.ID] {
				remainingAll = append(remainingAll, th)
			}
		}
		s.threads = remainingAll
		// 批量归档会从列表里删掉会话，待处理请求集合需要重算。
		s.bumpThreadsRuntimeLocked()
		if targetMap[s.selectedThreadID] {
			s.selectedThreadID = ""
		}
		s.lastBulk = &lastBulkInfo{
			projectID: projectID,
			token:     token,
			kind:      "archive",
			count:     len(threadIDs),
			at:        time.Now(),
		}
		s.mu.Unlock()
		s.scheduleLastBulkExpiry()
		s.invalidate()
	}()
}

// archiveThreadWithUndo 归档单条会话：成功后记录撤销令牌，
// 10 秒内可从侧边栏撤销条或项目右键菜单撤销；归档当前选中会话时清空选中与消息态。
func (s *chatState) archiveThreadWithUndo(projectID, threadID string) {
	if threadID == "" {
		return
	}
	go func() {
		token, err := s.client.ArchiveThreadWithUndo(threadID)
		if err != nil {
			s.setToast("归档失败: " + err.Error())
			return
		}
		s.mu.Lock()
		if list, ok := s.threadsByProj[projectID]; ok {
			var remaining []Thread
			for _, th := range list {
				if th.ID != threadID {
					remaining = append(remaining, th)
				}
			}
			s.threadsByProj[projectID] = remaining
		}
		var remainingAll []Thread
		for _, th := range s.threads {
			if th.ID != threadID {
				remainingAll = append(remainingAll, th)
			}
		}
		s.threads = remainingAll
		// 归档会从列表里删掉会话，待处理请求集合需要重算。
		s.bumpThreadsRuntimeLocked()
		if s.selectedThreadID == threadID {
			s.selectedThreadID = ""
			s.messages = nil
			s.messagesRev++
			s.approvals = nil
			s.threadUsage = ThreadUsage{Turns: make(map[int]TokenUsage)}
			if s.watchStop != nil {
				s.watchStop()
				s.watchStop = nil
			}
		}
		// 后端未登记撤销（token 为空）时不显示撤销条，避免出现点了无效的按钮。
		if token != "" {
			s.lastBulk = &lastBulkInfo{
				projectID: projectID,
				token:     token,
				kind:      "archive",
				count:     1,
				at:        time.Now(),
			}
		}
		s.mu.Unlock()
		if token != "" {
			s.scheduleLastBulkExpiry()
		}
		s.invalidate()
	}()
}

// markDormantWithUndo 休眠单条会话：成功后原地标记休眠并记录撤销令牌，
// 10 秒内可从侧边栏撤销条或项目右键菜单撤销。
func (s *chatState) markDormantWithUndo(projectID, threadID string) {
	if threadID == "" {
		return
	}
	go func() {
		token, err := s.client.MarkDormantWithUndo(threadID)
		if err != nil {
			s.setToast("标记休眠失败: " + err.Error())
			return
		}
		s.mu.Lock()
		if list, ok := s.threadsByProj[projectID]; ok {
			for i := range list {
				if list[i].ID == threadID {
					list[i].Dormant = true
				}
			}
			s.threadsByProj[projectID] = SortThreads(list)
		}
		for i := range s.threads {
			if s.threads[i].ID == threadID {
				s.threads[i].Dormant = true
			}
		}
		s.threads = SortThreads(s.threads)
		// 后端未登记撤销（token 为空）时不显示撤销条，避免出现点了无效的按钮。
		if token != "" {
			s.lastBulk = &lastBulkInfo{
				projectID: projectID,
				token:     token,
				kind:      "dormant",
				count:     1,
				at:        time.Now(),
			}
		}
		s.mu.Unlock()
		if token != "" {
			s.scheduleLastBulkExpiry()
		}
		s.invalidate()
		s.playDormantChime()
	}()
}

// undoLastBulk 撤销最近一次批量操作。
func (s *chatState) undoLastBulk() {
	s.mu.Lock()
	lb := s.lastBulk
	if lb == nil {
		s.mu.Unlock()
		return
	}
	if time.Since(lb.at) > 10*time.Second {
		s.lastBulk = nil
		s.mu.Unlock()
		s.setToast("撤销已过期")
		s.invalidate()
		return
	}
	projectID := lb.projectID
	token := lb.token
	if token == "" {
		s.mu.Unlock()
		return
	}
	s.lastBulk = nil
	s.mu.Unlock()

	go func() {
		ctx := context.Background()
		err := s.client.UndoBulkOperation(ctx, projectID, token)
		if err != nil {
			errMsg := err.Error()
			if strings.Contains(strings.ToLower(errMsg), "expire") || strings.Contains(errMsg, "过期") {
				s.setToast("撤销已过期")
			} else {
				s.setToast("撤销失败: " + errMsg)
			}
			return
		}
		// 撤销成功后重新从后端加载会话列表
		threads, err := s.client.Threads(ctx, projectID)
		if err == nil {
			s.mu.Lock()
			s.threadsByProj[projectID] = SortThreads(threads)
			var all []Thread
			for _, list := range s.threadsByProj {
				all = append(all, list...)
			}
			s.threads = SortThreads(all)
			// 撤销后重载的列表可能带来新的待处理请求/运行标记。
			s.bumpThreadsRuntimeLocked()
			s.mu.Unlock()
		}
		s.invalidate()
	}()
}

// scheduleLastBulkExpiry 调度 10 秒后自动清除撤销条。
func (s *chatState) scheduleLastBulkExpiry() {
	time.AfterFunc(10*time.Second, func() {
		s.mu.Lock()
		if s.lastBulk != nil && time.Since(s.lastBulk.at) >= 10*time.Second {
			s.lastBulk = nil
		}
		s.mu.Unlock()
		s.invalidate()
	})
}

// handleSearchKey 处理搜索栏中的键盘事件（上下键导航高亮，回车选中会话）。
func (s *chatState) handleSearchKey(k ui.TextKey, matches []Thread) bool {
	if len(matches) == 0 {
		return false
	}
	if k.Key == ui.KeyUp {
		s.mu.Lock()
		if s.searchHighlightIndex > 0 {
			s.searchHighlightIndex--
		} else {
			s.searchHighlightIndex = 0
		}
		s.mu.Unlock()
		s.invalidate()
		return true
	}
	if k.Key == ui.KeyDown {
		s.mu.Lock()
		if s.searchHighlightIndex < len(matches)-1 {
			s.searchHighlightIndex++
		} else {
			s.searchHighlightIndex = len(matches) - 1
		}
		s.mu.Unlock()
		s.invalidate()
		return true
	}
	if k.Key == ui.KeyEnter {
		s.mu.Lock()
		idx := s.searchHighlightIndex
		var targetID string
		if idx >= 0 && idx < len(matches) {
			targetID = matches[idx].ID
		}
		s.searchFocusInput = true
		s.mu.Unlock()
		if targetID != "" {
			s.closeWorkspace()
			s.selectThread(targetID)
		}
		s.invalidate()
		return true
	}
	return false
}
