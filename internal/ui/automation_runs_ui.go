package mcodeui

import (
	"context"
	"time"
)

// AutomationRunReadState 描述一条运行记录的已读与归档状态。
// 单独成结构而不加字段到 AutomationRun，是为了不改动 internal/ui/client.go 里已有的类型定义。
type AutomationRunReadState struct {
	RunID    string
	ReadAt   time.Time // 零值表示未读
	Archived bool
}

// AutomationRunsState 是一次「运行记录 + 已读/归档状态」查询的结果。
type AutomationRunsState struct {
	Runs   []AutomationRun
	States []AutomationRunReadState
}

// 所有字段均由 automationMu 保护；视图状态随自动化切换重置。
type automationRunsView struct {
	automationID           string
	failedOnly, unreadOnly bool
	err                    string
	retry                  func()
	sequence               uint64
	pending                map[string]uint64
}

var automationRunsViews = make(map[*automationState]*automationRunsView)
var automationUnreadRequests = make(map[*automationState]uint64)

func automationRunsViewFor(as *automationState, id string) *automationRunsView {
	v := automationRunsViews[as]
	if v == nil || v.automationID != id {
		v = &automationRunsView{automationID: id, pending: make(map[string]uint64)}
		automationRunsViews[as] = v
	}
	return v
}

func (v *automationRunsView) next(key string) uint64 {
	v.sequence++
	v.pending[key] = v.sequence
	v.err, v.retry = "", nil
	return v.sequence
}

func invalidateAutomationRunsLoad(as *automationState) {
	as.runsLoadID++
	as.runsLoading = false
}

// automationRunsStateClient 由 app.Client 实现；UI 通过类型断言取得，
// 因此不必扩充 internal/ui/client.go 里的 Client 接口。
type automationRunsStateClient interface {
	// AutomationRunsState 读取运行记录及已读/归档状态，includeArchived 为 true 时包含已归档记录。
	AutomationRunsState(ctx context.Context, automationID string, limit int, includeArchived bool) (AutomationRunsState, error)
	// MarkAutomationRunRead 把单条运行记录标记为已读。
	MarkAutomationRunRead(ctx context.Context, runID string) error
	// MarkAllAutomationRunsRead 把指定自动化下的运行记录全部标为已读，返回受影响条数。
	MarkAllAutomationRunsRead(ctx context.Context, automationID string) (int, error)
	// SetAutomationRunArchived 归档或取消归档单条运行记录。
	SetAutomationRunArchived(ctx context.Context, runID string, archived bool) error
	// AutomationUnreadCounts 返回项目下每个自动化的未读运行记录数。
	AutomationUnreadCounts(ctx context.Context, projectID string) (map[string]int, error)
}

// automationRunsStateClient 返回支持已读/归档状态的客户端；旧客户端或测试替身返回 false。
func (s *chatState) automationRunsStateClient() (automationRunsStateClient, bool) {
	if s == nil || s.client == nil {
		return nil, false
	}
	c, ok := s.client.(automationRunsStateClient)
	return c, ok
}

// fetchAutomationRuns 读取运行记录及其已读/归档状态。
// 客户端不支持状态接口时退回旧接口，此时状态未知，一律按已读处理，避免全部显示未读。
func (s *chatState) fetchAutomationRuns(ctx context.Context, automationID string, limit int, includeArchived bool) ([]AutomationRun, map[string]time.Time, map[string]bool, error) {
	if c, ok := s.automationRunsStateClient(); ok {
		st, err := c.AutomationRunsState(ctx, automationID, limit, includeArchived)
		if err != nil {
			return nil, nil, nil, err
		}
		reads := make(map[string]time.Time, len(st.States))
		archived := make(map[string]bool, len(st.States))
		for _, meta := range st.States {
			if !meta.ReadAt.IsZero() {
				reads[meta.RunID] = meta.ReadAt
			}
			if meta.Archived {
				archived[meta.RunID] = true
			}
		}
		return st.Runs, reads, archived, nil
	}
	runs, err := s.client.AutomationRuns(ctx, automationID, limit)
	return runs, nil, nil, err
}

// automationRunUnread 判断一条运行记录是否未读。
// reads 为 nil 表示客户端不支持已读状态（旧客户端），此时按已读处理。
func automationRunUnread(reads map[string]time.Time, runID string) bool {
	if reads == nil {
		return false
	}
	at, ok := reads[runID]
	return !ok || at.IsZero()
}

// automationUnreadTotal 汇总给定自动化集合的未读数，用于入口角标。
func automationUnreadTotal(unread map[string]int, autos []Automation) int {
	total := 0
	for _, a := range autos {
		total += unread[a.ID]
	}
	return total
}

// loadAutomationUnread 异步刷新项目下各自动化的未读运行记录数，供入口角标使用。
func (s *chatState) loadAutomationUnread(projectID string) {
	client, ok := s.automationRunsStateClient()
	if !ok {
		return
	}
	as := getAutomationState(s)
	if as == nil {
		return
	}
	automationMu.Lock()
	if as.projectID != projectID {
		automationMu.Unlock()
		return
	}
	automationUnreadRequests[as]++
	request := automationUnreadRequests[as]
	automationMu.Unlock()
	go func(pid string) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		counts, err := client.AutomationUnreadCounts(ctx, pid)
		s.updateWorkspace(func() {
			automationMu.Lock()
			if as.projectID != pid || automationUnreadRequests[as] != request {
				automationMu.Unlock()
				return
			}
			as.unreadKnown = err == nil
			if err == nil {
				as.unread = counts
			}
			automationMu.Unlock()
			s.invalidate()
		})
	}(projectID)
}

// markAutomationRunRead 把一条运行记录标记为已读（打开其详情时调用）。
// 本地先置位，避免等待后端返回期间圆点仍在；后端变化再经通知刷新。
func (s *chatState) markAutomationRunRead(automationID, runID string) {
	client, ok := s.automationRunsStateClient()
	if !ok || runID == "" {
		return
	}
	as := getAutomationState(s)
	if as == nil {
		return
	}

	automationMu.Lock()
	if as.selectedID != automationID || as.runRead == nil {
		automationMu.Unlock()
		return
	}
	old, had := as.runRead[runID]
	if had && !old.IsZero() {
		automationMu.Unlock()
		return
	}
	view := automationRunsViewFor(as, automationID)
	key := "read:" + runID
	seq := view.next(key)
	automationUnreadRequests[as]++
	invalidateAutomationRunsLoad(as)
	session := as.editSession
	as.runRead[runID] = time.Now().UTC()
	decremented := false
	if as.unread != nil && automationID != "" && as.unread[automationID] > 0 {
		as.unread[automationID]--
		decremented = true
	}
	projectID := as.projectID
	automationMu.Unlock()
	s.invalidate()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := client.MarkAutomationRunRead(ctx, runID)
		s.updateWorkspace(func() {
			automationMu.Lock()
			if as.projectID != projectID || as.selectedID != automationID || as.editSession != session || automationRunsViews[as] != view || view.pending[key] != seq {
				automationMu.Unlock()
				return
			}
			if err != nil {
				if as.runRead == nil {
					as.runRead = make(map[string]time.Time)
				}
				if had {
					as.runRead[runID] = old
				} else {
					delete(as.runRead, runID)
				}
				if decremented && as.unread != nil {
					as.unread[automationID]++
				}
				view.err = "标记已读失败：" + err.Error()
				view.retry = func() { s.markAutomationRunRead(automationID, runID) }
			}
			automationMu.Unlock()
			s.loadAutomationUnread(projectID)
		})
	}()
}

// markAllAutomationRunsRead 把当前自动化的所有运行记录标为已读。
func (s *chatState) markAllAutomationRunsRead(automationID string) {
	client, ok := s.automationRunsStateClient()
	if !ok || automationID == "" {
		return
	}
	as := getAutomationState(s)
	if as == nil {
		return
	}

	automationMu.Lock()
	if as.selectedID != automationID || as.runRead == nil {
		automationMu.Unlock()
		return
	}
	projectID := as.projectID
	session := as.editSession
	view := automationRunsViewFor(as, automationID)
	seq := view.next("all")
	automationUnreadRequests[as]++
	invalidateAutomationRunsLoad(as)
	snapshot := make(map[string]time.Time, len(as.runRead))
	for id, at := range as.runRead {
		snapshot[id] = at
	}
	oldCount := as.unread[automationID]
	for _, run := range as.runs {
		as.runRead[run.ID] = time.Now().UTC()
		view.pending["read:"+run.ID] = seq
	}
	if as.unread != nil {
		as.unread[automationID] = 0
	}
	automationMu.Unlock()
	s.invalidate()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := client.MarkAllAutomationRunsRead(ctx, automationID)
		s.updateWorkspace(func() {
			automationMu.Lock()
			if as.projectID != projectID || as.selectedID != automationID || as.editSession != session || automationRunsViews[as] != view || view.pending["all"] != seq {
				automationMu.Unlock()
				return
			}
			if err != nil {
				if as.runRead == nil {
					as.runRead = make(map[string]time.Time)
				}
				for _, run := range as.runs {
					if view.pending["read:"+run.ID] == seq {
						if at, ok := snapshot[run.ID]; ok {
							as.runRead[run.ID] = at
						} else {
							delete(as.runRead, run.ID)
						}
					}
				}
				if as.unread != nil {
					as.unread[automationID] = oldCount
				}
				view.err = "全部标为已读失败：" + err.Error()
				view.retry = func() { s.markAllAutomationRunsRead(automationID) }
			}
			automationMu.Unlock()
			s.loadAutomationUnread(projectID)
		})
	}()
}

// setAutomationRunArchived 归档或取消归档一条运行记录。
func (s *chatState) setAutomationRunArchived(runID string, archived bool) {
	client, ok := s.automationRunsStateClient()
	if !ok || runID == "" {
		return
	}
	as := getAutomationState(s)
	if as == nil {
		return
	}

	automationMu.Lock()
	automationID := as.selectedID
	if automationID == "" {
		automationMu.Unlock()
		return
	}
	if as.runArchived == nil {
		as.runArchived = make(map[string]bool)
	}
	old, had := as.runArchived[runID]
	view := automationRunsViewFor(as, automationID)
	key := "archive:" + runID
	seq := view.next(key)
	automationUnreadRequests[as]++
	invalidateAutomationRunsLoad(as)
	session := as.editSession
	as.runArchived[runID] = archived
	projectID := as.projectID
	automationMu.Unlock()
	s.invalidate()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := client.SetAutomationRunArchived(ctx, runID, archived)
		s.updateWorkspace(func() {
			automationMu.Lock()
			if as.projectID != projectID || as.selectedID != automationID || as.editSession != session || automationRunsViews[as] != view || view.pending[key] != seq {
				automationMu.Unlock()
				return
			}
			if err != nil {
				if as.runArchived == nil {
					as.runArchived = make(map[string]bool)
				}
				if had {
					as.runArchived[runID] = old
				} else {
					delete(as.runArchived, runID)
				}
				view.err = "归档操作失败：" + err.Error()
				view.retry = func() { s.setAutomationRunArchived(runID, archived) }
			}
			automationMu.Unlock()
			s.loadAutomationUnread(projectID)
		})
	}()
}
