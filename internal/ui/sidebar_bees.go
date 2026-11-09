package mcodeui

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

var (
	sidebarBeesMu    sync.Mutex
	sidebarBeeFolded = make(map[string]bool)
)

// sidebarBeeGlyphRadius 是状态徽章里 3px 白色方块的圆角：只磨掉一丝棱角，仍读作方块。
const sidebarBeeGlyphRadius = float32(0.5)

// IsBeeFolded 判断指定父会话的 bees 是否折叠。默认展开（返回 false）。
func IsBeeFolded(parentID string) bool {
	sidebarBeesMu.Lock()
	defer sidebarBeesMu.Unlock()
	return sidebarBeeFolded[parentID]
}

// SetBeeFolded 设置指定父会话的 bees 折叠状态。
func SetBeeFolded(parentID string, folded bool) {
	sidebarBeesMu.Lock()
	sidebarBeeFolded[parentID] = folded
	sidebarBeesMu.Unlock()
}

// ToggleBeeFold 切换指定父会话的 bees 折叠状态。
func ToggleBeeFold(parentID string) {
	sidebarBeesMu.Lock()
	sidebarBeeFolded[parentID] = !sidebarBeeFolded[parentID]
	sidebarBeesMu.Unlock()
}

// ResetBeeFolded 清空折叠状态，供测试或重置使用。
func ResetBeeFolded() {
	sidebarBeesMu.Lock()
	sidebarBeeFolded = make(map[string]bool)
	sidebarBeesMu.Unlock()
}

// MapBeeStatus 将原始状态与运行标记映射为统一 5 态：
// idle、running、waiting_review、completed、errored。
func MapBeeStatus(status string, isRunning bool) string {
	norm := strings.ToLower(strings.TrimSpace(status))
	if norm == "" {
		if isRunning {
			return swarmStatusRunning
		}
		return swarmStatusIdle
	}
	mapped := normalizeSwarmStatus(norm)
	if mapped == swarmStatusIdle && isRunning {
		return swarmStatusRunning
	}
	return mapped
}

// OrderSwarmBees 对 bee 会话进行排序：运行中优先，同状态下按更新时间（UpdatedAt）倒序。
func OrderSwarmBees(bees []Thread) []Thread {
	if len(bees) <= 1 {
		res := make([]Thread, len(bees))
		copy(res, bees)
		return res
	}
	res := make([]Thread, len(bees))
	copy(res, bees)
	sort.SliceStable(res, func(i, j int) bool {
		iRunning := res[i].IsRunning
		jRunning := res[j].IsRunning
		if iRunning != jRunning {
			return iRunning
		}
		if !res[i].UpdatedAt.Equal(res[j].UpdatedAt) {
			return res[i].UpdatedAt.After(res[j].UpdatedAt)
		}
		return res[i].ID < res[j].ID
	})
	return res
}

// MergeSwarmStateBees 将当前会话列表中的 bee 线程与 SwarmState 中的 bees 进行兜底合并。
// 规则：
// 1. 去重（按 ThreadID）：已存在于 listBees 中的 bee，沿用其标题与时间，运行标记以 SwarmState 为准；
// 2. 对于 SwarmState.Bees 中存在、但在 listBees 中尚不存在的 bee，根据其 ThreadID、Name、Status 构造为 Thread 补充进列表；
// 3. 排序保持运行中优先、同状态下稳定排序。
func MergeSwarmStateBees(listBees []Thread, swarmBees []SwarmBee, leadThreadID, leadProjectID string) []Thread {
	if len(swarmBees) == 0 {
		return OrderSwarmBees(listBees)
	}

	existingIDs := make(map[string]bool, len(listBees))
	res := make([]Thread, 0, len(listBees)+len(swarmBees))
	// Swarm 状态里的 bee 状态是权威值：会话列表里的 IsRunning 只是某次加载时的快照，
	// 已结束的 bee 会一直停在运行中，折叠行因此一直画运行中图标。
	statusByBee := make(map[string]string, len(swarmBees))
	for _, h := range swarmBees {
		if h.ThreadID != "" {
			statusByBee[h.ThreadID] = MapSwarmBeeStatus(h)
		}
	}
	for _, th := range listBees {
		if th.ID != "" {
			existingIDs[th.ID] = true
		}
		if status, ok := statusByBee[th.ID]; ok {
			switch status {
			case swarmStatusRunning:
				th.IsRunning = true
			case swarmStatusWaitingReview:
				// 待审查只影响角标，不写进 LastStatus：那里沿用会话终态的四态文案。
				th.IsRunning = false
			case swarmStatusCompleted, swarmStatusErrored, swarmStatusIdle:
				th.IsRunning = false
				if th.LastStatus == "" {
					th.LastStatus = status
				}
			}
		}
		res = append(res, th)
	}

	now := time.Now()
	for _, h := range swarmBees {
		if h.ThreadID == "" || existingIDs[h.ThreadID] {
			continue
		}
		existingIDs[h.ThreadID] = true
		t := h.StartedAt
		if t.IsZero() {
			t = now
		}
		title := h.Name
		if title == "" {
			title = "子代理"
		}
		res = append(res, Thread{
			ID:             h.ThreadID,
			ProjectID:      leadProjectID,
			ParentThreadID: leadThreadID,
			Title:          title,
			IsHelper:       true,
			IsRunning:      MapSwarmBeeStatus(h) == swarmStatusRunning,
			LastStatus:     h.Status,
			UpdatedAt:      t,
		})
	}

	return OrderSwarmBees(res)
}

// GroupThreadsByParent 将会话列表分为顶层会话列表与每个父会话挂载的子会话列表。
// 子列表只装 bee 子代理；派生会话（ForkedFromThreadID 非空）是独立会话，
// 始终留在顶层，不挂到来源会话下面。
// 规则：
//  1. 若会话的 IsHelper 为 true，且 ParentThreadID 非空，且 ParentThreadID 存在于当前列表中：
//     该会话从顶层会话中移除，归入其父会话的子列表中。
//  2. 找不到父会话时（父会话不在当前列表中，或 ParentThreadID 为空，或 IsHelper 为 false），
//     仍按普通会话显示在顶层。
//  3. 每组的子行排序走 OrderSwarmBees（运行中优先、时间倒序）。
func GroupThreadsByParent(threads []Thread) (topLevel []Thread, beesByParent map[string][]Thread) {
	if len(threads) == 0 {
		return nil, make(map[string][]Thread)
	}

	presentIDs := make(map[string]bool, len(threads))
	for _, th := range threads {
		presentIDs[th.ID] = true
	}

	childrenByParent := make(map[string][]Thread)
	topLevel = make([]Thread, 0, len(threads))

	// 只处理 bee 子代理——父在列表内就挂上去，否则保持顶层；派生会话始终留在顶层。
	for _, th := range threads {
		if th.IsHelper && th.ParentThreadID != "" && presentIDs[th.ParentThreadID] {
			childrenByParent[th.ParentThreadID] = append(childrenByParent[th.ParentThreadID], th)
		} else {
			topLevel = append(topLevel, th)
		}
	}

	beesByParent = make(map[string][]Thread, len(childrenByParent))
	for pID, list := range childrenByParent {
		beesByParent[pID] = OrderSwarmBees(list)
	}

	return topLevel, beesByParent
}

// FilterIdleSwarmBees 只保留运行中、等待回答或正被查看（keepID 为当前选中会话）的子代理，用于侧栏隐藏空闲子代理。
func FilterIdleSwarmBees(bees []Thread, keepID string) []Thread {
	if len(bees) == 0 {
		return nil
	}
	res := make([]Thread, 0, len(bees))
	for _, h := range bees {
		if h.IsRunning || h.HasPendingRequest || h.ID == keepID {
			res = append(res, h)
		}
	}
	return res
}

// BeeFoldSummary 返回折叠状态下的统计信息：子行总数、是否有正在运行的子行，以及文案。
func BeeFoldSummary(bees []Thread) (count int, hasRunning bool, label string) {
	count = len(bees)
	for _, h := range bees {
		if h.IsRunning {
			hasRunning = true
			break
		}
	}
	return count, hasRunning, fmt.Sprintf("%d 个子代理", count)
}

// extractBeeName 从会话 Title 提取 bee 名字（如 "Aspen: 子任务 1" -> "Aspen"）。
func extractBeeName(title string) string {
	trimmed := strings.TrimSpace(title)
	if idx := strings.Index(trimmed, ":"); idx != -1 {
		n := strings.TrimSpace(trimmed[:idx])
		if n != "" {
			return n
		}
	}
	if idx := strings.Index(trimmed, "："); idx != -1 {
		n := strings.TrimSpace(trimmed[:idx])
		if n != "" {
			return n
		}
	}
	return trimmed
}

// beeStatusFromLastStatus 把会话最近一轮的终态映射为 bee 展示用的状态标识。
func beeStatusFromLastStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "complete", "succeeded", "success", "done":
		return "completed"
	case "failed", "error":
		return "failed"
	case "interrupted", "cancelled", "canceled", "stopped":
		return "stopped"
	}
	return ""
}

// resolveBeeStatus 解析指定 bee 对应的最终展示状态。
// 状态优先取父会话 SwarmState 中对应 SwarmBee.Status；没有时用线程最近一轮的终态，最后才回退到 IsRunning。
// 查表走派生视图的 statusByThread，侧边栏每帧对每个子代理调用也不会拷贝整份 SwarmState。
func resolveBeeStatus(s *chatState, parentID string, bee Thread) string {
	if s != nil {
		if view := swarmStateViewFor(s); view != nil && (view.leadID == parentID || parentID == "") {
			if status, ok := view.statusByThread[bee.ID]; ok && bee.ID != "" {
				return MapBeeStatus(status, bee.IsRunning)
			}
		}
	}
	if bee.IsRunning {
		return swarmStatusRunning
	}
	if status := beeStatusFromLastStatus(bee.LastStatus); status != "" {
		return MapBeeStatus(status, false)
	}
	return swarmStatusIdle
}

// threadHasRunningBees 判断指定主会话下是否还有子代理（Swarm bee）在执行。
// 数据只取已缓存的会话列表与 Swarm 面板状态（与侧边栏头像条的判定同源），不发起任何请求：
// 1. 会话列表中挂在该主会话下的 helper 会话，用 resolveBeeStatus 解析状态；
// 2. 会话列表还没刷新到刚派出的 bee 时，用 Swarm 面板状态兜底（该面板只缓存当前 lead）。
// 侧栏渲染期间走 beeBusyFrame 的帧内缓存（renderSidebar 每帧只算一次），其余调用方实时计算。
func (s *chatState) threadHasRunningBees(threadID string) bool {
	if s == nil || threadID == "" {
		return false
	}
	if s.beeBusyFrame != nil {
		return s.beeBusyFrame[threadID]
	}
	return s.computeRunningBeeParents()[threadID]
}

// computeRunningBeeParents 一次遍历算出所有「名下还有子代理在跑」的主会话 ID 集合。
// 侧栏每帧要对每个会话多次判定忙碌，逐次全表扫描在几千个会话时是 O(N²)，所以先整体算一遍再复用。
// 判定规则与逐会话版本一致：会话列表里挂在该主会话下、resolveBeeStatus 为 running 的 helper，
// 或 Swarm 面板报了一个列表里还没有的 runningID。
// 调用方不得持有 s.mu（内部会取锁读会话列表）。
func (s *chatState) computeRunningBeeParents() map[string]bool {
	if s == nil {
		return nil
	}

	// 会话元素会被原地改写（标题、运行模式等），锁内先拷出 helper 会话，解锁后再解析状态，
	// 避免和 swarmPanelMu 形成锁顺序冲突。
	s.mu.Lock()
	helpersByParent := make(map[string][]Thread)
	for i := range s.threads {
		th := &s.threads[i]
		if !th.IsHelper || th.ParentThreadID == "" || th.ID == "" {
			continue
		}
		helpersByParent[th.ParentThreadID] = append(helpersByParent[th.ParentThreadID], *th)
	}
	selectedID := s.selectedThreadID
	s.mu.Unlock()

	result := make(map[string]bool, len(helpersByParent))
	for parentID, bees := range helpersByParent {
		for _, bee := range bees {
			if resolveBeeStatus(s, parentID, bee) == swarmStatusRunning {
				result[parentID] = true
				break
			}
		}
	}

	view := swarmStateViewFor(s)
	if view == nil {
		return result
	}
	leadID := view.leadID
	if leadID == "" {
		leadID = selectedID
	}
	if leadID == "" || result[leadID] {
		return result
	}
	known := make(map[string]bool, len(helpersByParent[leadID]))
	for _, bee := range helpersByParent[leadID] {
		known[bee.ID] = true
	}
	// 只需要看“还在跑、却不在会话列表里”的子代理，遍历 runningIDs 即可（通常不超过并发上限）。
	for _, id := range view.runningIDs {
		if !known[id] {
			result[leadID] = true
			break
		}
	}
	return result
}

// startSidebarBeesWatch 订阅 Swarm 状态变更，用于刷新侧边栏里未选中主会话下的子代理状态：
// 只有选中的会话有 Watch，其它会话的 Bee 结束后侧边栏会一直停在旧的运行中。
func (s *chatState) startSidebarBeesWatch() {
	s.mu.Lock()
	if s.stopBeesWatch != nil || s.client == nil || s.closed {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	stop := s.client.WatchSwarm(func(leadThreadID string) {
		s.refreshBeeThreads(leadThreadID)
	})

	s.mu.Lock()
	if s.closed {
		if stop != nil {
			stop()
		}
	} else {
		s.stopBeesWatch = stop
	}
	s.mu.Unlock()
}

// refreshBeeThreads 重新拉取主会话所在项目的会话列表，用后端的运行标记与最近终态覆盖本地快照。
// 刷新按项目节流：子代理一多，Swarm 变更事件非常密集，每个事件都重拉整份会话列表会让
// 界面一直处在“RPC + 全量转换 + 重绘”里；窗口内的后续事件合并成一次尾随刷新。
func (s *chatState) refreshBeeThreads(leadThreadID string) {
	if leadThreadID == "" || s.client == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	projectID := ""
	for _, th := range s.threads {
		if th.ID == leadThreadID {
			projectID = th.ProjectID
			break
		}
	}
	s.mu.Unlock()
	if projectID == "" {
		return
	}
	key := beeThreadsRefreshKey{state: s, projectID: projectID}
	if !throttleBeeThreadsRefresh(key, s) {
		return
	}
	s.loadThreads(projectID, false)
}

// beeThreadsRefreshKey 标识一份“被 Swarm 事件驱动的项目会话列表刷新”。
type beeThreadsRefreshKey struct {
	state     *chatState
	projectID string
}

// beeThreadsRefreshInterval 是 Swarm 事件触发的会话列表刷新的最小间隔。
const beeThreadsRefreshInterval = time.Second

var (
	beeThreadsRefreshMu   sync.Mutex
	beeThreadsRefreshLast = make(map[beeThreadsRefreshKey]time.Time)
	beeThreadsRefreshTail = make(map[beeThreadsRefreshKey]*time.Timer)
)

// throttleBeeThreadsRefresh 报告这次刷新是否应该立即执行；被窗口拦下时排一次尾随刷新，
// 保证事件沉寂后还能得到一次最新的会话列表。
func throttleBeeThreadsRefresh(key beeThreadsRefreshKey, s *chatState) bool {
	now := time.Now()
	beeThreadsRefreshMu.Lock()
	defer beeThreadsRefreshMu.Unlock()
	if last, ok := beeThreadsRefreshLast[key]; ok && now.Sub(last) < beeThreadsRefreshInterval {
		if _, pending := beeThreadsRefreshTail[key]; !pending {
			wait := beeThreadsRefreshInterval - now.Sub(last)
			beeThreadsRefreshTail[key] = time.AfterFunc(wait, func() {
				beeThreadsRefreshMu.Lock()
				delete(beeThreadsRefreshTail, key)
				beeThreadsRefreshLast[key] = time.Now()
				beeThreadsRefreshMu.Unlock()
				if s == nil {
					return
				}
				s.mu.Lock()
				closed := s.closed
				s.mu.Unlock()
				if closed || s.client == nil {
					return
				}
				s.loadThreads(key.projectID, false)
			})
		}
		return false
	}
	beeThreadsRefreshLast[key] = now
	return true
}

// resetBeeThreadsRefresh 清掉某个 chatState 的刷新节流记录。
// 关闭窗口时必须调用：尾随定时器会拿着 *chatState 去拉会话列表，
// 若该指针被复用（或窗口已关）就会对别的状态发起刷新；同时避免节流表无限增长。
func resetBeeThreadsRefresh(s *chatState) {
	beeThreadsRefreshMu.Lock()
	defer beeThreadsRefreshMu.Unlock()
	for key, timer := range beeThreadsRefreshTail {
		if key.state != s {
			continue
		}
		if timer != nil {
			timer.Stop()
		}
		delete(beeThreadsRefreshTail, key)
	}
	for key := range beeThreadsRefreshLast {
		if key.state == s {
			delete(beeThreadsRefreshLast, key)
		}
	}
}

// renderRunningGlyph 渲染「进行中」动态图标：3×3 点阵，亮度沿对角线循环扫过（约 1.2s 一圈）。
// 只重绘该图形，不触发整个视图重建。
func renderRunningGlyph(c *ui.Context, key string, size float32, color ui.Color) {
	ui.Box(c.Key(key)).
		Size(size, size).
		Shrink(0).
		Draw(func(p *ui.Painter, r ui.Rect) {
			// 步长 200ms（6 帧一圈 1.2s）：只重绘该图形，比 ui.Spinner 的 75ms 整窗重绘省很多。
			const grid, period, frame = 3, 1200, 200
			ms := int(p.Now().UnixMilli() % period)
			p.After(time.Duration(frame-ms%frame) * time.Millisecond)
			cell := r.W / grid
			dot := cell * 0.62
			for row := 0; row < grid; row++ {
				for col := 0; col < grid; col++ {
					// 对角线序号 0..4 映射到一圈内的相位，亮点之后逐渐衰减。
					at := float32(row+col) / float32(2*(grid-1))
					phase := float32(ms)/period - at
					phase -= float32(int(phase))
					if phase < 0 {
						phase++
					}
					alpha := 0.25 + 0.75*(1-phase)
					x := r.X + float32(col)*cell + (cell-dot)/2
					y := r.Y + float32(row)*cell + (cell-dot)/2
					p.Fill(ui.Rect{X: x, Y: y, W: dot, H: dot}, color.Alpha(alpha), dot*0.25)
				}
			}
		})
}

// beesEffectivelyFolded 返回子行容器当前是否折叠：选中其中某个 bee 时强制展开保持可见。
// children 是父行下的全部子行；折叠状态仍按 parentID 记一份。
func beesEffectivelyFolded(s *chatState, parentID string, children []Thread) bool {
	if !IsBeeFolded(parentID) {
		return false
	}
	if s == nil {
		return true
	}
	s.mu.Lock()
	selectedID := s.selectedThreadID
	s.mu.Unlock()
	for _, h := range children {
		if h.ID == selectedID {
			return false
		}
	}
	return true
}

// renderSidebarBeeFoldLine 渲染折叠态的单行摘要（"› N 个子代理"），点击展开；
// 有运行中的子行时右侧显示动态图标。
func renderSidebarBeeFoldLine(c *ui.Context, s *chatState, o sidebarThreadRowOptions) {
	parentID := o.thread.ID
	_, hasRunning, label := BeeFoldSummary(o.bees)
	secondaryText := ThemeSecondaryText(c)

	btn := ui.ButtonBase(c.Key("bee-fold-btn-"+o.projectID+"-"+parentID)).
		Margin(Space0, ListInset, Space1, ListInset+o.indent+10).
		Padding(Space0, Space1).
		Radius(RadiusXS).
		Justify(ui.Start).
		AlignItems(ui.Center).
		Label(label)
	if btn.Hovered() {
		btn.Background(ThemeHover(c))
	}
	if btn.Clicked() {
		ToggleBeeFold(parentID)
		if s != nil {
			s.invalidate()
		}
	}
	btn.Children(func() {
		ui.Row(c.Key("bee-fold-row-" + o.projectID + "-" + parentID)).
			AlignItems(ui.Center).
			Gap(Space1).
			Children(func() {
				ui.Icon(c, IconChevronRight()).
					Size(Space3, Space3).
					TextColor(secondaryText)
				ui.Text(c, label).
					FontSize(scaledFont(FontSizeProjectName)).
					TextColor(secondaryText).
					SingleLine()
				if hasRunning {
					renderRunningGlyph(c, "bee-fold-run-"+o.projectID+"-"+parentID, 10, ThemeAccent(c))
				}
			})
	})
}

// renderSidebarBeeSubRow 在父会话行下方渲染圆角容器、虚线连接线，以及容器里的 bee 头像条。
func renderSidebarBeeSubRow(c *ui.Context, s *chatState, o sidebarThreadRowOptions) {
	bees := o.bees
	if len(bees) == 0 {
		return
	}

	parentID := o.thread.ID

	var selectedID string
	if s != nil {
		s.mu.Lock()
		selectedID = s.selectedThreadID
		s.mu.Unlock()
	}

	if beesEffectivelyFolded(s, parentID, bees) {
		renderSidebarBeeFoldLine(c, s, o)
		return
	}

	// 子行整行容器：包含左侧连接线与右侧圆角容器
	row := ui.Row(c.Key("bee-subrow-"+o.projectID+"-"+parentID)).
		Margin(Space0, ListInset, Space1, ListInset+o.indent).
		AlignItems(ui.Stretch)

	row.Children(func() {
		// 1. 左侧虚线连接线：从父行引出，点击可折叠
		connectorBtn := ui.ButtonBase(c.Key("bee-connector-btn-" + parentID)).
			Width(Space7).
			Shrink(0).
			Label("收起子会话").
			Tooltip("收起子会话")

		connectorHovered := connectorBtn.Hovered()
		connectorBtn.Draw(func(painter *ui.Painter, r ui.Rect) {
			lineCol := ThemeSecondaryText(c).Alpha(0.35)
			if connectorHovered {
				lineCol = ThemeSecondaryText(c).Alpha(0.85)
			}
			lineX := r.X + 14
			midY := r.Y + r.H/2

			// 竖直点状虚线：从顶部走到中点
			for y := r.Y; y < midY; y += 4 {
				endY := y + 1.8
				if endY > midY {
					endY = midY
				}
				painter.Line(lineX, y, lineX, endY, 1, lineCol)
			}

			// 水平点状虚线：从中点向右连接到容器
			for x := lineX; x < r.X+r.W; x += 4 {
				endX := x + 1.8
				if endX > r.X+r.W {
					endX = r.X + r.W
				}
				painter.Line(x, midY, endX, midY, 1, lineCol)
			}
		})

		if connectorBtn.Clicked() {
			ToggleBeeFold(parentID)
			if s != nil {
				s.invalidate()
			}
		}

		// 2. 右侧圆角容器（细描边、内边距约 6pt，头像自动换行）
		card := ui.Box(c.Key("bee-container-box-" + o.projectID + "-" + parentID)).
			Radius(RadiusSM).
			Padding(Space2).
			Background(ThemeOverlayHover(c)).
			BorderWidth(1).
			BorderColor(ThemeBorder(c).Alpha(0.45)).
			Grow(1)

		card.Children(func() {
			ui.Row(c.Key("bee-container-row-" + parentID)).
				Wrap().
				Gap(Space2).
				AlignItems(ui.Center).
				Children(func() {
					for _, bee := range bees {
						hVal := bee
						isBeeSelected := (selectedID == hVal.ID)
						status := resolveBeeStatus(s, parentID, hVal)
						renderSidebarSingleBeeAvatar(c, s, parentID, hVal, status, isBeeSelected)
					}
				})
		})
	})
}

// renderSidebarSingleBeeAvatar 渲染单个 bee 的约 14pt 头像及约 8pt 带描边的状态角标。
func renderSidebarSingleBeeAvatar(c *ui.Context, s *chatState, parentID string, bee Thread, status string, isSelected bool) {
	name := extractBeeName(bee.Title)
	if name == "" {
		name = "子代理"
	}
	persona := findSwarmPersona(name)
	icon := safePersonaIcon(persona.Asset)
	const avatarSize float32 = IconSM
	const btnSize float32 = 18

	btnKey := fmt.Sprintf("bee-avatar-btn-%s-%s", parentID, bee.ID)
	btn := ui.ButtonBase(c.Key(btnKey)).
		Size(btnSize, btnSize).
		Radius(btnSize / 2).
		Justify(ui.Center).
		AlignItems(ui.Center).
		Label(persona.Name).
		Tooltip(fmt.Sprintf("%s · %s", persona.Name, bee.Title))

	if isSelected {
		btn.Background(ThemeAccent(c).Alpha(0.24)).
			BorderWidth(1.2).
			BorderColor(ThemeAccent(c))
	} else if btn.Hovered() {
		btn.Background(persona.Color.Alpha(0.20))
	}

	if btn.Clicked() {
		if s != nil {
			s.closeWorkspace()
			s.selectThread(bee.ID)
		}
	}

	btn.Children(func() {
		// 头像容器（约 14pt，人设专属色底与边框）
		Avatar(c, AvatarProps{
			Key: btnKey + "-glyph", Name: name, Icon: icon, Size: avatarSize, FontSize: avatarSize * 0.48,
			Foreground: persona.Color, Background: persona.Color.Alpha(0.18), Border: persona.Color.Alpha(0.55),
		})

		// 约 8pt 带深色描边的状态角标
		renderSidebarBeeBadge(c, btnKey, status)
	})
}

// renderSidebarBeeBadge 渲染约 8pt 的状态角标：带深色描边与头像区分。
func renderSidebarBeeBadge(c *ui.Context, keyPrefix string, status string) {
	const badgeSize float32 = Space3
	badgeKey := keyPrefix + "-badge"
	box := ui.Box(c.Key(badgeKey)).
		Absolute().
		Right(-1).
		Bottom(-1).
		Size(badgeSize, badgeSize).
		Radius(badgeSize / 2).
		BorderWidth(1).
		BorderColor(ThemeSurface(c)).
		Justify(ui.Center).
		AlignItems(ui.Center)

	switch status {
	case swarmStatusRunning:
		// 运行中：深色底与描边，内部点阵扫光动画（自绘，200ms 一帧，不用 ui.Spinner）
		box.Background(ThemeSurface(c)).
			Children(func() {
				renderRunningGlyph(c, badgeKey+"-run", badgeSize-1, ThemeAccent(c))
			})
	case swarmStatusCompleted:
		// 绿底白勾表示完成
		box.Background(ThemeSuccess(c)).
			Children(func() {
				ui.Icon(c, IconCheck()).
					Size(5, 5).
					TextColor(ThemeOnAccent(c))
			})
	case swarmStatusWaitingReview:
		// 黄底白点表示等待人工审查
		box.Background(ThemeWarning(c)).
			Children(func() {
				ui.Box(c.Key(badgeKey+"-review-dot")).
					Size(3, 3).
					Radius(RadiusPill).
					Background(ThemeOnAccent(c))
			})
	case swarmStatusErrored:
		// 红底白叉表示失败
		box.Background(ThemeDanger(c)).
			Children(func() {
				ui.Icon(c, IconCircleX()).
					Size(5, 5).
					TextColor(ThemeOnAccent(c))
			})
	case swarmStatusIdle:
		// 灰底白方块表示空闲或已停止
		box.Background(ThemeSecondaryText(c).Alpha(0.75)).
			Children(func() {
				ui.Box(c.Key(badgeKey+"-stop-sq")).
					Size(3.2, 3.2).
					Radius(sidebarBeeGlyphRadius).
					Background(ThemeOnAccent(c))
			})
	default:
		// 未知状态：更淡的灰底白方块
		box.Background(ThemeSecondaryText(c).Alpha(0.60)).
			Children(func() {
				ui.Box(c.Key(badgeKey+"-idle-sq")).
					Size(3, 3).
					Radius(sidebarBeeGlyphRadius).
					Background(ThemeOnAccent(c))
			})
	}
}
