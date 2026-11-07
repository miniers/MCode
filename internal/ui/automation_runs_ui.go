package mcodeui

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"
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
	if projectID == "" {
		return
	}
	client, ok := s.automationRunsStateClient()
	if !ok {
		return
	}
	as := getAutomationState(s)
	if as == nil {
		return
	}
	go func(pid string) {
		counts, err := client.AutomationUnreadCounts(context.Background(), pid)
		if err != nil {
			return
		}
		s.updateWorkspace(func() {
			automationMu.Lock()
			if as.projectID != pid {
				automationMu.Unlock()
				return
			}
			as.unread = counts
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
	if as.runRead == nil {
		as.runRead = make(map[string]time.Time)
	} else if at, read := as.runRead[runID]; read && !at.IsZero() {
		automationMu.Unlock()
		return
	}
	as.runRead[runID] = time.Now().UTC()
	if as.unread != nil && automationID != "" && as.unread[automationID] > 0 {
		as.unread[automationID]--
	}
	projectID := as.projectID
	automationMu.Unlock()
	s.invalidate()

	go func(id string) {
		if err := client.MarkAutomationRunRead(context.Background(), id); err != nil {
			return
		}
		s.loadAutomationUnread(projectID)
	}(runID)
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
	projectID := as.projectID
	automationMu.Unlock()

	go func(id string) {
		if _, err := client.MarkAllAutomationRunsRead(context.Background(), id); err != nil {
			return
		}
		s.loadAutomationRuns(id)
		s.loadAutomationUnread(projectID)
	}(automationID)
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
	if as.runArchived == nil {
		as.runArchived = make(map[string]bool)
	}
	as.runArchived[runID] = archived
	automationID := as.selectedID
	projectID := as.projectID
	automationMu.Unlock()
	s.invalidate()

	go func(id string) {
		if err := client.SetAutomationRunArchived(context.Background(), id, archived); err != nil {
			return
		}
		if automationID != "" {
			s.loadAutomationRuns(automationID)
		}
		s.loadAutomationUnread(projectID)
	}(runID)
}

// renderAutomationUnreadBadge 渲染未读数角标，沿用侧栏未读圆点的强调色样式。
// count <= 0 时不渲染任何内容。
func renderAutomationUnreadBadge(c *ui.Context, key string, count int) {
	if count <= 0 {
		return
	}
	ui.Row(c.Key(key)).
		Label(fmt.Sprintf("未读运行记录 %d 条", count)).
		Padding(Space0, Space2).
		Radius(RadiusMD).
		AlignItems(ui.Center).
		Background(ThemeAccent(c)).
		Children(func() {
			ui.Text(c, strconv.Itoa(count)).
				FontSize(TypeCaption).
				FontWeight(600).
				TextColor(ThemeOnAccent(c))
		})
}
