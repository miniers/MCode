package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

var (
	automationMu     sync.Mutex
	automationStates = make(map[*chatState]*automationState)
)

type automationEnableClient interface {
	SetAutomationEnabled(ctx context.Context, id string, enabled bool, revision int) (Automation, error)
}

const (
	// automationEmptyIconSize 是空态插图的图标边长。
	automationEmptyIconSize float32 = 44
	automationListWidth     float32 = 320
	automationNarrowWidth   float32 = 1000
	// automationPromptMinHeight 是任务指令输入框的最小高度。
	automationPromptMinHeight float32 = 90
)

// automationState 维护自动化工作区列表、表单与运行记录的本地状态。
type automationState struct {
	projectID   string
	initialized bool
	loading     bool
	err         string
	conflict    bool
	autos       []Automation
	loadID      int
	runsLoadID  int
	saving      bool
	saveID      int
	editSession int
	watchStop   func()

	// 卡片级操作：正在立即运行的自动化、已暂停/重置请求中的自动化
	runningIDs map[string]bool
	busyIDs    map[string]bool

	selectedID   string // 当前选中的自动化 ID，为空且 isNew 为 true 时为新建
	isNew        bool
	editRevision int
	schedule     AutomationSchedule

	// 表单草稿
	name             string
	prompt           string
	scheduleType     string // "interval", "once", "daily", "weekdays", "weekly", "cron"
	intervalInput    string // 间隔分钟文本，默认 "60"
	intervalUnit     string
	timezone         string
	timeOfDay        string // "09:00"
	typeTimes        [3]string
	dayOfWeek        int    // 0-6 (0 为周日，1 为周一)
	cronExpr         string // "*/15"
	onceDateStr      string // "2006-01-02 15:04"
	stopWhen         string
	enabled          bool
	maxFailuresInput string // 默认 "3"
	maxRunsInput     string // 默认 "0"，0 表示不限制
	catchUp          bool   // 错过触发时是否补跑，默认开启
	formErr          string
	scheduleErrors   map[string]string
	draftBaseline    automationDraft
	previewSchedule  AutomationSchedule
	previewValid     bool
	previewID        int
	previewCancel    context.CancelFunc
	previewTimer     *time.Timer
	preview          AutomationSchedulePreview
	previewVisible   bool

	// 每次运行新建会话使用的模型与权限：空串表示跟随默认
	provider    string
	model       string
	effort      string
	runtimeMode string
	// runMode 为服务端当前的运行方式（standalone/dedicated/heartbeat），
	// heartbeat 投递到已有会话，模型与权限设置对它不生效，表单里置灰。
	runMode string

	// 运行记录
	runsLoading     bool
	runsErr         string
	runs            []AutomationRun
	schedulerStatus AutomationSchedulerStatus
	// 运行记录的已读/归档状态（runID -> 状态）与是否显示已归档记录
	runRead         map[string]time.Time
	runArchived     map[string]bool
	includeArchived bool
	// openRunID 为当前展开详情的运行记录 ID
	openRunID string
	// unread 为项目下各自动化的未读运行记录数（automationID -> 未读数）
	unread map[string]int

	// 删除确认
	confirmDelete      bool
	editorOpen         bool
	confirmDiscard     bool
	focusEditorTrigger bool
	detailTab          int
	filter             int
	unreadOnly         bool
	narrowDetail       bool
	moreOpen           bool
}

func getAutomationState(s *chatState) *automationState {
	if s == nil {
		return nil
	}
	automationMu.Lock()
	defer automationMu.Unlock()
	st, ok := automationStates[s]
	if !ok {
		st = &automationState{
			scheduleType:     "interval",
			intervalInput:    "60",
			timeOfDay:        "09:00",
			dayOfWeek:        1,
			cronExpr:         "*/15 * * * *",
			onceDateStr:      time.Now().Add(24 * time.Hour).Format("2006-01-02 15:04"),
			enabled:          true,
			maxFailuresInput: "3",
			maxRunsInput:     "0",
			catchUp:          true,
			runningIDs:       make(map[string]bool),
			busyIDs:          make(map[string]bool),
		}
		automationStates[s] = st
	}
	return st
}

// loadAutomations 异步获取自动化列表。
func (s *chatState) loadAutomations(projectID string) {
	s.loadAutomationList(projectID, true)
}

func stopAutomationWatch(s *chatState) {
	automationMu.Lock()
	defer automationMu.Unlock()
	if as := automationStates[s]; as != nil {
		as.initialized = false
		if as.watchStop != nil {
			as.watchStop()
			as.watchStop = nil
		}
	}
}

func (s *chatState) startAutomationWatch(projectID string) {
	stopAutomationWatch(s)
	as := getAutomationState(s)
	stop := s.client.WatchAutomations(func() {
		s.loadAutomationList(projectID, false)
		s.updateWorkspace(func() {
			if as.projectID == projectID && as.selectedID != "" {
				s.loadAutomationRuns(as.selectedID)
			}
		})
	})
	automationMu.Lock()
	as.watchStop = stop
	as.initialized = true
	automationMu.Unlock()
}

func (s *chatState) loadAutomationList(projectID string, refreshForm bool) {
	if s == nil || s.client == nil || projectID == "" {
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
	as.loadID++
	loadID := as.loadID
	as.loading = true
	as.err = ""
	automationMu.Unlock()
	s.invalidate()

	go func(pid string) {
		ctx := context.Background()
		autos, err := s.client.Automations(ctx, pid)
		schedStatus, schedErr := s.client.AutomationSchedulerStatus(ctx)
		if schedErr != nil {
			schedStatus = AutomationSchedulerStatus{State: AutomationSchedulerPassiveError, Error: schedErr.Error()}
		}

		s.updateWorkspace(func() {
			automationMu.Lock()
			if as.projectID != pid || as.loadID != loadID {
				automationMu.Unlock()
				return
			}
			as.loading = false
			as.schedulerStatus = schedStatus
			if err != nil {
				if errors.Is(err, ErrRevisionConflict) {
					as.conflict = true
				} else {
					as.err = err.Error()
				}
			} else {
				as.autos = autos
				as.err = ""
				var found bool
				for _, a := range autos {
					if a.ID == as.selectedID {
						found = true
						if refreshForm && !as.dirty() {
							as.fillAutomation(a)
						} else if refreshForm && a.Revision != as.editRevision {
							as.conflict = true
						}
						break
					}
				}
				if !found && !as.isNew {
					as.selectedID = ""
				}
			}
			automationMu.Unlock()
			s.invalidate()
		})
		// 未读运行记录数：入口角标依据
		s.loadAutomationUnread(pid)
	}(projectID)
}

// loadAutomationRuns 异步获取指定自动化的运行记录。
func (s *chatState) loadAutomationRuns(automationID string) {
	if s == nil || s.client == nil || automationID == "" {
		return
	}
	as := getAutomationState(s)
	if as == nil {
		return
	}

	automationMu.Lock()
	if as.selectedID != automationID {
		automationMu.Unlock()
		return
	}
	as.runsLoadID++
	loadID := as.runsLoadID
	projectID := as.projectID
	includeArchived := as.includeArchived
	as.runsLoading = true
	as.runsErr = ""
	automationMu.Unlock()
	s.invalidate()

	go func(id string) {
		ctx := context.Background()
		runs, runRead, runArchived, err := s.fetchAutomationRuns(ctx, id, 100, includeArchived)

		automationMu.Lock()
		defer automationMu.Unlock()
		if as.selectedID != id || as.projectID != projectID || as.runsLoadID != loadID {
			return
		}
		as.runsLoading = false
		if err != nil {
			as.runsErr = err.Error()
			as.runs = nil
			as.runRead = nil
			as.runArchived = nil
		} else {
			as.runs = runs
			as.runsErr = ""
			as.runRead = runRead
			as.runArchived = runArchived
		}
		s.invalidate()
	}(automationID)
}

// selectAutomation 选中某自动化并填充编辑表单。
func (as *automationState) selectAutomation(auto Automation, s *chatState) {
	automationMu.Lock()
	as.fillAutomation(auto)
	as.detailTab = 0
	as.narrowDetail = true
	as.runs = nil
	as.runRead = nil
	as.runArchived = nil
	as.openRunID = ""
	as.runsErr = ""
	automationMu.Unlock()

	s.loadAutomationRuns(auto.ID)
	s.invalidate()
}

// fillAutomation 在 UI 线程持锁调用，保证草稿与其 revision 始终来自同一快照。
func (as *automationState) fillAutomation(auto Automation) {
	as.editSession++
	as.selectedID = auto.ID
	as.isNew = false
	as.editRevision = auto.Revision
	as.schedule = auto.Schedule
	as.name = auto.Name
	as.prompt = auto.Prompt
	as.scheduleType = auto.Schedule.Type
	if as.scheduleType == "" {
		as.scheduleType = "interval"
	}
	if auto.Schedule.Seconds > 0 {
		as.intervalInput = formatIntervalMinutesText(auto.Schedule.Seconds)
	} else {
		as.intervalInput = "60"
	}
	as.intervalUnit = "分钟"
	as.timezone = auto.Schedule.Timezone
	if auto.Schedule.TimeOfDay != "" {
		as.timeOfDay = auto.Schedule.TimeOfDay
	} else {
		as.timeOfDay = "09:00"
	}
	as.dayOfWeek = auto.Schedule.DayOfWeek
	as.typeTimes = [3]string{as.timeOfDay, as.timeOfDay, as.timeOfDay}
	if auto.Schedule.Expression != "" {
		as.cronExpr = auto.Schedule.Expression
	} else {
		as.cronExpr = "*/15 * * * *"
	}
	if !auto.Schedule.Date.IsZero() {
		loc := time.Local
		if tz, err := time.LoadLocation(as.timezone); as.timezone != "" && err == nil {
			loc = tz
		}
		as.onceDateStr = auto.Schedule.Date.In(loc).Format("2006-01-02 15:04")
	} else {
		as.onceDateStr = time.Now().Add(24 * time.Hour).Format("2006-01-02 15:04")
	}
	as.stopWhen = auto.StopWhen
	as.enabled = auto.Enabled
	as.maxFailuresInput = strconv.Itoa(auto.MaxConsecutiveFailures)
	maxRuns := auto.MaxRuns
	if maxRuns == 0 && auto.MaxIterations > 0 {
		maxRuns = auto.MaxIterations
	}
	as.maxRunsInput = strconv.Itoa(maxRuns)
	as.catchUp = auto.CatchUp
	as.provider = auto.Provider
	as.model = auto.Model
	as.effort = auto.Effort
	as.runtimeMode = auto.RuntimeMode
	as.runMode = auto.RunMode
	as.formErr = ""
	as.confirmDelete = false
	as.draftBaseline = as.draft()
}

// resetToNewAutomation 重置表单进入新建状态。
func (as *automationState) resetToNewAutomation(s *chatState) {
	automationMu.Lock()
	as.editSession++
	as.selectedID = ""
	as.isNew = true
	as.editorOpen = true
	as.confirmDiscard = false
	as.editRevision = 0
	as.schedule = AutomationSchedule{}
	as.name = ""
	as.prompt = ""
	as.scheduleType = "interval"
	as.intervalInput = "60"
	as.intervalUnit = "分钟"
	as.timezone = ""
	as.timeOfDay = "09:00"
	as.typeTimes = [3]string{"09:00", "09:00", "09:00"}
	as.dayOfWeek = 1
	as.cronExpr = "*/15 * * * *"
	as.onceDateStr = time.Now().Add(24 * time.Hour).Format("2006-01-02 15:04")
	as.stopWhen = ""
	as.enabled = true
	as.maxFailuresInput = "3"
	as.maxRunsInput = "0"
	as.catchUp = true
	// 新建时默认跟随默认模型与权限
	as.provider = ""
	as.model = ""
	as.effort = ""
	as.runtimeMode = ""
	as.runMode = ""
	as.formErr = ""
	as.confirmDelete = false
	as.runs = nil
	as.runsErr = ""
	as.draftBaseline = as.draft()
	automationMu.Unlock()
	s.invalidate()
}

// automationDraftSnapshot 用于保留保存请求发出后的编辑。
type automationDraft struct {
	fields           [17]string
	day              int
	enabled, catchUp bool
	typeTimes        [3]string
}

func (as *automationState) draft() automationDraft {
	return automationDraft{[17]string{as.name, as.prompt, as.scheduleType, as.intervalInput, as.timeOfDay,
		as.cronExpr, as.onceDateStr, as.stopWhen, as.maxFailuresInput, as.maxRunsInput,
		as.provider, as.model, as.effort, as.runtimeMode, as.runMode, as.intervalUnit, as.timezone}, as.dayOfWeek, as.enabled, as.catchUp, as.typeTimes}
}

func (as *automationState) dirty() bool {
	return as.draft() != as.draftBaseline
}

// saveAutomation 校验并保存自动化。
func (s *chatState) saveAutomation() {
	if s == nil || s.client == nil {
		return
	}
	as := getAutomationState(s)
	if as == nil {
		return
	}

	automationMu.Lock()
	if as.saving {
		automationMu.Unlock()
		return
	}
	name := strings.TrimSpace(as.name)
	prompt := strings.TrimSpace(as.prompt)
	sched, scheduleErrors := automationScheduleFromDraft(as)
	stopWhen := strings.TrimSpace(as.stopWhen)
	enabled := as.enabled
	maxFailures, failuresErr := strconv.Atoi(strings.TrimSpace(as.maxFailuresInput))
	// 最大运行次数留空按 0（不限制）处理
	maxRuns := 0
	runsErr := error(nil)
	if raw := strings.TrimSpace(as.maxRunsInput); raw != "" {
		maxRuns, runsErr = strconv.Atoi(raw)
	}
	catchUp := as.catchUp
	selectedID := as.selectedID
	isNew := as.isNew || selectedID == ""
	curPID := as.projectID
	// 心跳模式沿用原会话设置，模型与权限不参与保存
	provider := as.provider
	model := as.model
	effort := as.effort
	runtimeMode := as.runtimeMode
	if as.runMode == "heartbeat" {
		provider, model, effort, runtimeMode = "", "", "", ""
	}

	curRevision := as.editRevision
	editSession := as.editSession
	draft := as.draft()
	automationMu.Unlock()

	// 表单校验
	if curPID == "" || failuresErr != nil || maxFailures <= 0 || runsErr != nil || maxRuns < 0 || maxRuns > 1000 {
		automationMu.Lock()
		as.formErr = "请选择项目，连续失败次数须为正整数，最大运行次数须为 0 到 1000 的整数"
		automationMu.Unlock()
		s.invalidate()
		return
	}
	if name == "" {
		automationMu.Lock()
		as.formErr = "自动化名称不能为空"
		automationMu.Unlock()
		s.invalidate()
		return
	}
	if prompt == "" {
		automationMu.Lock()
		as.formErr = "Prompt 不能为空"
		automationMu.Unlock()
		s.invalidate()
		return
	}

	if len(scheduleErrors) != 0 {
		automationMu.Lock()
		as.scheduleErrors = scheduleErrors
		automationMu.Unlock()
		s.invalidate()
		return
	}

	automationMu.Lock()
	if as.saving || as.editSession != editSession || as.projectID != curPID {
		automationMu.Unlock()
		return
	}
	as.formErr = ""
	as.scheduleErrors = nil
	as.saving = true
	as.saveID++
	saveID := as.saveID
	automationMu.Unlock()

	input := AutomationInput{
		ProjectID:              curPID,
		Name:                   name,
		Prompt:                 prompt,
		StopWhen:               stopWhen,
		Schedule:               sched,
		Enabled:                enabled,
		MaxConsecutiveFailures: maxFailures,
		MaxIterations:          maxRuns,
		MaxRuns:                maxRuns,
		CatchUp:                &catchUp,
		Provider:               provider,
		Model:                  model,
		Effort:                 effort,
		RuntimeMode:            runtimeMode,
	}

	go func() {
		ctx := context.Background()
		var err error
		var saved Automation
		if isNew {
			saved, err = s.client.CreateAutomation(ctx, input)
		} else {
			saved, err = s.client.UpdateAutomation(ctx, selectedID, input, curRevision)
		}
		s.updateWorkspace(func() {
			automationMu.Lock()
			if as.saveID != saveID || as.projectID != curPID {
				automationMu.Unlock()
				return
			}
			as.saving = false
			loadRuns := false
			if as.editSession == editSession && as.selectedID == selectedID && as.isNew == isNew && err == nil {
				if as.draft() == draft {
					as.fillAutomation(saved)
					as.editorOpen = false
					if isNew {
						as.detailTab = 0
						as.narrowDetail = true
						loadRuns = true
					}
				} else {
					as.selectedID = saved.ID
					as.isNew = false
					as.editRevision = saved.Revision
					as.schedule = saved.Schedule
					as.draftBaseline = draft
				}
			}
			automationMu.Unlock()
			if loadRuns {
				s.loadAutomationRuns(saved.ID)
			}
			s.invalidate()
		})
		if err != nil {
			if errors.Is(err, ErrRevisionConflict) {
				automationMu.Lock()
				if as.projectID == curPID && as.editSession == editSession {
					as.conflict = true
				}
				automationMu.Unlock()
			} else {
				s.setToast("保存自动化失败: " + err.Error())
			}
			s.invalidate()
			return
		}

		s.loadAutomationList(curPID, false)
	}()
}

func (as *automationState) closeEditor() {
	if as.dirty() {
		as.confirmDiscard = true
		return
	}
	as.editorOpen = false
	as.focusEditorTrigger = true
	if as.isNew {
		as.isNew = false
	}
}

// deleteAutomation 删除当前选中的自动化。
func (s *chatState) deleteAutomation(id string) {
	if s == nil || s.client == nil || id == "" {
		return
	}
	as := getAutomationState(s)
	if as == nil {
		return
	}

	automationMu.Lock()
	curPID := as.projectID
	automationMu.Unlock()
	go func() {
		ctx := context.Background()
		err := s.client.DeleteAutomation(ctx, id)
		if err != nil {
			if errors.Is(err, ErrRevisionConflict) {
				automationMu.Lock()
				as.conflict = true
				automationMu.Unlock()
			} else {
				s.setToast("删除自动化失败: " + err.Error())
			}
			s.invalidate()
			return
		}

		s.updateWorkspace(func() {
			automationMu.Lock()
			defer automationMu.Unlock()
			if as.projectID == curPID && as.selectedID == id {
				as.selectedID = ""
				as.isNew = false
			}
		})

		s.loadAutomationList(curPID, false)
	}()
}

// runAutomationNow 立即执行指定自动化，执行期间卡片展示 Spinner 并禁用按钮。
func (s *chatState) runAutomationNow(id string) {
	if s == nil || s.client == nil || id == "" {
		return
	}
	as := getAutomationState(s)
	if as == nil {
		return
	}
	automationMu.Lock()
	if as.runningIDs[id] {
		automationMu.Unlock()
		return
	}
	if hint := automationSchedulerHint(as.schedulerStatus); hint != "" {
		automationMu.Unlock()
		s.setToast(hint + "，无法立即运行")
		return
	}
	if as.runningIDs == nil {
		as.runningIDs = make(map[string]bool)
	}
	as.runningIDs[id] = true
	automationMu.Unlock()
	s.invalidate()

	go func() {
		ctx := context.Background()
		_, err := s.client.RunAutomation(ctx, id)

		automationMu.Lock()
		delete(as.runningIDs, id)
		automationMu.Unlock()
		s.invalidate()

		if err != nil {
			if errors.Is(err, ErrAutomationSchedulerPassive) {
				automationMu.Lock()
				hint := automationSchedulerHint(as.schedulerStatus)
				automationMu.Unlock()
				if hint == "" {
					hint = "定时任务已暂停：另一个 mcode 实例正在调度"
				}
				s.setToast(hint + "，无法立即运行")
			} else {
				s.setToast("运行自动化失败: " + err.Error())
			}
			s.invalidate()
			return
		}
		s.loadAutomationRuns(id)
	}()
}

// setAutomationEnabled 切换启用开关：复用 Update 调用，只改变 Enabled 字段。
// 重置后端连续失败计数依赖 Enabled 非空：暂停时传 false，恢复/重置时传 true。
func (s *chatState) setAutomationEnabled(auto Automation, enabled bool) {
	if s == nil || s.client == nil || auto.ID == "" {
		return
	}
	as := getAutomationState(s)
	if as == nil {
		return
	}

	automationMu.Lock()
	if as.busyIDs[auto.ID] {
		automationMu.Unlock()
		return
	}
	if as.busyIDs == nil {
		as.busyIDs = make(map[string]bool)
	}
	as.busyIDs[auto.ID] = true
	projectID := as.projectID
	editSession := as.editSession
	automationMu.Unlock()
	s.invalidate()

	go func(a Automation) {
		ctx := context.Background()
		var saved Automation
		var err error
		if client, ok := s.client.(automationEnableClient); ok {
			saved, err = client.SetAutomationEnabled(ctx, a.ID, enabled, a.Revision)
		} else {
			saved, err = s.client.UpdateAutomation(ctx, a.ID, automationInputFrom(a, enabled), a.Revision)
		}

		automationMu.Lock()
		delete(as.busyIDs, a.ID)
		automationMu.Unlock()
		if err != nil {
			if errors.Is(err, ErrRevisionConflict) {
				automationMu.Lock()
				as.conflict = true
				automationMu.Unlock()
			} else if enabled {
				s.setToast("恢复自动化失败: " + err.Error())
			} else {
				s.setToast("暂停自动化失败: " + err.Error())
			}
			s.invalidate()
			return
		}

		// 编辑区草稿同步到新状态，避免表单里的启用开关与卡片不一致。
		s.updateWorkspace(func() {
			automationMu.Lock()
			if as.projectID == projectID && as.editSession == editSession && saved.ID == as.selectedID && !as.isNew &&
				as.editRevision == a.Revision && saved.Revision == a.Revision+1 {
				as.enabled = saved.Enabled
				as.draftBaseline.enabled = saved.Enabled
				as.editRevision = saved.Revision
			}
			automationMu.Unlock()
		})
		s.loadAutomationList(projectID, false)
	}(auto)
}

// duplicateAutomation 以原配置新建一个默认停用的副本，并选中副本以便继续编辑。
func (s *chatState) duplicateAutomation(auto Automation) {
	if s == nil || s.client == nil || auto.ID == "" {
		return
	}
	as := getAutomationState(s)
	if as == nil {
		return
	}

	automationMu.Lock()
	if as.saving || as.busyIDs[auto.ID] {
		automationMu.Unlock()
		return
	}
	as.saving = true
	as.saveID++
	saveID := as.saveID
	projectID := as.projectID
	automationMu.Unlock()
	s.invalidate()

	input := automationInputFrom(auto, false)
	input.Name = auto.Name + " (副本)"
	if strings.TrimSpace(auto.ProjectID) != "" {
		input.ProjectID = auto.ProjectID
	}

	go func(in AutomationInput) {
		ctx := context.Background()
		saved, err := s.client.CreateAutomation(ctx, in)

		automationMu.Lock()
		if as.saveID != saveID {
			automationMu.Unlock()
			return
		}
		as.saving = false
		automationMu.Unlock()
		if err != nil {
			s.setToast("复制自动化失败: " + err.Error())
			s.invalidate()
			return
		}

		s.updateWorkspace(func() {
			automationMu.Lock()
			sameProject := as.projectID == projectID
			automationMu.Unlock()
			if sameProject {
				as.selectAutomation(saved, s)
			}
		})
		s.loadAutomationList(projectID, false)
	}(input)
}

// automationInputFrom 用自动化现有字段组装 Create/Update 入参，不含 ConsecutiveFailures 等运行时状态。
func automationInputFrom(auto Automation, enabled bool) AutomationInput {
	maxRuns := auto.MaxRuns
	if maxRuns == 0 {
		maxRuns = auto.MaxIterations
	}
	return AutomationInput{
		ProjectID:              auto.ProjectID,
		Name:                   auto.Name,
		Prompt:                 auto.Prompt,
		StopWhen:               auto.StopWhen,
		Schedule:               auto.Schedule,
		Enabled:                enabled,
		MaxConsecutiveFailures: auto.MaxConsecutiveFailures,
		MaxIterations:          auto.MaxIterations,
		MaxRuns:                maxRuns,
		CatchUp:                &auto.CatchUp,
		Provider:               auto.Provider,
		Model:                  auto.Model,
		Effort:                 auto.Effort,
		RuntimeMode:            auto.RuntimeMode,
	}
}

// automationRuntimeModeOption 是权限模式下拉的单个选项：Label 为界面文案，Value 为协议取值。
// Value 为空串表示跟随默认，与 automation.Spec.RuntimeMode 的取值语义一致。
type automationRuntimeModeOption struct {
	Label string
	Value string
}

// automationRuntimeModeOptions 是权限模式下拉的全部选项，顺序即下拉顺序。
var automationRuntimeModeOptions = []automationRuntimeModeOption{
	{Label: "跟随默认", Value: ""},
	{Label: "只读", Value: "readonly"},
	{Label: "需审批", Value: "supervised"},
	{Label: "自动", Value: "auto"},
	{Label: "完全访问", Value: "fullAccess"},
}

// automationRuntimeModeLabel 把权限模式取值转成下拉文案；未知值回退到「跟随默认」。
func automationRuntimeModeLabel(value string) string {
	if value == "autoAcceptEdits" {
		value = "auto"
	}
	for _, opt := range automationRuntimeModeOptions {
		if opt.Value == value {
			return opt.Label
		}
	}
	return automationRuntimeModeOptions[0].Label
}

// automationCardStatus 生成卡片状态胶囊：运行中、已结束（含失败原因）或上次运行结果。
func automationCardStatus(auto Automation, running bool) (text string, kind string) {
	switch {
	case running || auto.LastRunStatus == "running":
		return "运行中", "running"
	case auto.Status == "completed":
		// 停止条件命中、迭代次数用尽或一次性调度跑完：任务已结束，不是暂停。
		return "已完成", "succeeded"
	case !auto.Enabled && auto.Status == "failed" && auto.ConsecutiveFailures > 0:
		return "连续失败后暂停", "failed"
	case !auto.Enabled:
		return "已暂停", "paused"
	case auto.LastRunStatus == "failed":
		return "上次失败", "failed"
	case auto.LastRunStatus == "succeeded":
		return "上次成功", "succeeded"
	case auto.LastRunStatus == "stopped", auto.LastRunStatus == "canceled", auto.LastRunStatus == "cancelled":
		return "已停止", "stopped"
	}
	return "", ""
}

// automationCardStatusColor 按胶囊类别返回配色。
func automationCardStatusColor(c *ui.Context, kind string) ui.Color {
	switch kind {
	case "running":
		return ThemeAccent(c)
	case "succeeded":
		return ThemeSuccess(c)
	case "failed":
		return ThemeDanger(c)
	case "paused":
		return ThemeWarning(c)
	case "stopped":
		return ThemePlan(c)
	}
	return ThemeSecondaryText(c)
}

// automationSchedulerHint 把调度锁状态转成页面提示；active 或未知状态返回空串。
func automationSchedulerHint(st AutomationSchedulerStatus) string {
	switch st.State {
	case AutomationSchedulerPassiveHeld:
		return "定时任务已暂停：另一个 mcode 实例正在调度"
	case AutomationSchedulerPassiveError:
		msg := st.Error
		if msg == "" {
			msg = "未知错误"
		}
		return "定时任务已暂停：无法获取调度锁（" + msg + "）"
	}
	return ""
}

// automationSearchMatch 判断自动化是否命中搜索关键词。
func automationSearchMatch(a Automation, query string) bool {
	if query == "" {
		return true
	}
	return strings.Contains(strings.ToLower(a.Name), query) ||
		strings.Contains(strings.ToLower(a.Prompt), query) ||
		strings.Contains(strings.ToLower(a.StopWhen), query) ||
		strings.Contains(strings.ToLower(automationScheduleText(a.Schedule)), query)
}

// filterAutomations 按搜索词过滤后再排序：已启用优先 → NextRunAt 升序（零值最后）→ 名称升序。
// 返回新切片，避免改动状态里保存的列表顺序。
func filterAutomations(autos []Automation, query string) []Automation {
	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]Automation, 0, len(autos))
	for _, a := range autos {
		if automationSearchMatch(a, q) {
			out = append(out, a)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Enabled != b.Enabled {
			return a.Enabled && !b.Enabled
		}
		az, bz := a.NextRunAt.IsZero(), b.NextRunAt.IsZero()
		if az != bz {
			return !az
		}
		if !az && !a.NextRunAt.Equal(b.NextRunAt) {
			return a.NextRunAt.Before(b.NextRunAt)
		}
		return a.Name < b.Name
	})
	return out
}

func filterAutomationStatus(autos []Automation, query string, filter int, unreadOnly bool, unread map[string]int) []Automation {
	visible := filterAutomations(autos, query)
	out := make([]Automation, 0, len(visible))
	for _, a := range visible {
		failed := a.LastRunStatus == "failed" || (!a.Enabled && a.Status == "failed" && a.ConsecutiveFailures > 0)
		if filter == 1 && !failed || filter == 2 && (a.Enabled || a.Status == "completed") || unreadOnly && unread[a.ID] == 0 {
			continue
		}
		out = append(out, a)
	}
	return out
}

// formatRelativeUntil 输出未来时间的相对描述，如“N 分钟后”“N 小时后”。
func formatRelativeUntil(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	diff := time.Until(t)
	if diff <= 0 {
		return "即将运行"
	}
	if diff < time.Minute {
		return "不到 1 分钟后"
	}
	if diff < time.Hour {
		return fmt.Sprintf("%d 分钟后", int(diff.Minutes()))
	}
	if diff < 24*time.Hour {
		return fmt.Sprintf("%d 小时后", int(diff.Hours()))
	}
	return fmt.Sprintf("%d 天后", int(diff.Hours()/24))
}

// renderAutomationWorkspace 渲染自动化工作区主视图。
func renderAutomationWorkspace(c *ui.Context, s *chatState) {
	renderAutomationWorkspaceWithSearch(c, s, "")
}

// renderAutomationWorkspaceWithSearch 带搜索词渲染自动化工作区并协调详情。
// 调用处（看板「自动化」标签页）应传入看板搜索框文本 ks.searchText；
// 传空串时等价于不过滤。
func renderAutomationWorkspaceWithSearch(c *ui.Context, s *chatState, searchText string) {
	if s == nil {
		return
	}
	as := getAutomationState(s)
	if as == nil {
		return
	}

	curPID := s.currentWorkspaceProjectID()
	if curPID != "" && (as.projectID != curPID || !as.initialized) {
		automationMu.Lock()
		if as.projectID != curPID {
			if as.previewTimer != nil {
				as.previewTimer.Stop()
			}
			if as.previewCancel != nil {
				as.previewCancel()
			}
			*as = automationState{loadID: as.loadID, runsLoadID: as.runsLoadID, saveID: as.saveID, editSession: as.editSession + 1, watchStop: as.watchStop, runningIDs: make(map[string]bool), busyIDs: make(map[string]bool)}
		}
		as.projectID = curPID
		as.initialized = true
		automationMu.Unlock()
		s.startAutomationWatch(curPID)
		s.loadAutomations(curPID)
	}

	automationMu.Lock()
	loading := as.loading
	errMsg := as.err
	autos := as.autos
	selectedID := as.selectedID
	isNew := as.isNew
	formErr := as.formErr
	runsLoading := as.runsLoading
	runsErr := as.runsErr
	runs := as.runs
	runRead := as.runRead
	runArchived := as.runArchived
	includeArchived := as.includeArchived
	openRunID := as.openRunID
	schedulerStatus := as.schedulerStatus
	unread := as.unread
	confirmDelete := as.confirmDelete
	saving := as.saving
	runningIDs := make(map[string]bool, len(as.runningIDs))
	for id, running := range as.runningIDs {
		runningIDs[id] = running
	}
	busyIDs := make(map[string]bool, len(as.busyIDs))
	for id, busy := range as.busyIDs {
		busyIDs[id] = busy
	}
	automationMu.Unlock()

	// 搜索过滤与排序：汇总计数与列表都只看过滤后的集合
	visible := filterAutomationStatus(autos, searchText, as.filter, as.unreadOnly, unread)
	// 未读运行记录总数：自动化入口角标依据
	totalUnread := automationUnreadTotal(unread, autos)
	// 当前选中自动化在已加载记录中是否还有未读：“全部标为已读”按钮依据
	selectedUnread := 0
	for _, run := range runs {
		if automationRunUnread(runRead, run.ID) {
			selectedUnread++
		}
	}
	var selected Automation
	for _, auto := range autos {
		if auto.ID == selectedID {
			selected = auto
			break
		}
	}

	width, _ := c.Size()
	narrow := width < automationNarrowWidth

	ui.Column(c.Key("automation-workspace-root")).
		Fill().
		Background(ThemeWindowBg(c)).
		Padding(Space5, Space6).
		Gap(Space4).
		Children(func() {
			ui.Row(c.Key("automation-toolbar")).FillWidth().Gap(Space3).AlignItems(ui.Center).Children(func() {
				renderWorkspaceProjectPicker(c, s, curPID)
				ui.Box(c.Key("automation-toolbar-spacer")).Grow(1)
				IconButton(c.Key("automation-refresh"), IconButtonProps{
					Icon: IconRefreshCw(), Label: "刷新自动化",
					OnClick: func() { s.loadAutomations(curPID) },
					Variant: ButtonGhost,
				})
			})
			if automationHint := automationSchedulerHint(schedulerStatus); automationHint != "" {
				ui.Text(c, automationHint).
					Key("automation-scheduler-hint").
					FontSize(TypeFootnote).
					TextColor(ThemeWarning(c))
			}

			// 没有任何自动化且未在新建时整页展示空态
			if !loading && errMsg == "" && len(autos) == 0 && !isNew && selectedID == "" && strings.TrimSpace(searchText) == "" && as.filter == 0 && !as.unreadOnly {
				ui.Column(c.Key("automation-empty-full")).
					Grow(1).
					FillWidth().
					Center().
					Padding(Space8, Space8).
					Gap(Space5).
					Children(func() {
						ui.Icon(c, IconCalendarClock()).
							Size(automationEmptyIconSize, automationEmptyIconSize).
							TextColor(ThemeAccent(c))
						ui.Column(c.Key("automation-empty-full-text")).
							Gap(Space2).
							AlignItems(ui.Center).
							Children(func() {
								ui.Text(c, "还没有自动化").
									FontSize(TypePageTitle).
									FontWeight(700).
									TextColor(ThemePrimaryText(c))
							})
						newButton := Button(c.Key("automation-empty-create"), ButtonOpts{
							Icon:     IconPlus(),
							Text:     "创建第一个自动化",
							Label:    "automation-empty-create",
							Disabled: curPID == "" || saving,
							OnClick: func() {
								as.resetToNewAutomation(s)
							},
							Variant: ButtonPrimary,
							Size:    ControlMD,
						})
						if as.focusEditorTrigger {
							newButton.Focus()
							as.focusEditorTrigger = false
						}
					})
				return
			}

			if errMsg != "" && len(autos) > 0 {
				ui.Text(c, "刷新失败: "+errMsg).FontSize(TypeFootnote).TextColor(ThemeDanger(c))
			}
			if as.conflict && !as.editorOpen {
				ui.Row(c.Key("automation-conflict-banner")).FillWidth().Gap(Space2).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "数据已在别处被修改，请刷新后重试").TextColor(ThemeWarning(c)).Grow(1)
					Button(c.Key("automation-conflict-refresh"), ButtonOpts{Text: "刷新", Label: "automation-conflict-refresh", Variant: ButtonSecondary, OnClick: func() {
						as.conflict = false
						s.loadAutomationList(curPID, false)
					}})
				})
			}
			// 2. 左右双栏：左侧列表，右侧编辑与运行详情
			ui.Row(c.Key("automation-main-row")).
				Grow(1).
				FillWidth().
				Gap(Space5).
				Children(func() {
					// 左侧自动化列表区
					if !narrow || !as.narrowDetail || selectedID == "" {
						listCol := ui.Column(c.Key("automation-list-col")).
							FillHeight().
							Radius(RadiusMD).
							Background(ThemeOverlay(c, 0.06)).
							BorderWidth(Hairline).
							BorderColor(ThemeBorder(c)).
							Padding(Space3, Space4).
							Gap(Space3)
						if narrow {
							listCol.Grow(1)
						} else {
							listCol.Width(automationListWidth)
						}
						listCol.Children(func() {
							// 列表头与新建按钮
							ui.Row(c.Key("automation-list-header")).
								FillWidth().
								AlignItems(ui.Center).
								Children(func() {
									ui.Text(c, "自动化").
										FontSize(TypeProse).
										FontWeight(600).
										TextColor(ThemePrimaryText(c))
									// 未读运行记录数角标：沿用侧栏未读圆点的强调色样式
									renderAutomationUnreadBadge(c, "automation-list-unread-badge", totalUnread)
									ui.Box(c.Key("automation-list-header-spacer")).Grow(1)

									newButton := Button(c.Key("automation-new-btn"), ButtonOpts{
										Icon:     IconPlus(),
										Text:     "新建",
										Label:    "automation-new-btn",
										Disabled: curPID == "" || saving,
										OnClick: func() {
											as.resetToNewAutomation(s)
										},
										Variant: ButtonPrimary,
										Size:    ControlSM,
									})
									if as.focusEditorTrigger && selectedID == "" {
										newButton.Focus()
										as.focusEditorTrigger = false
									}
								})
							Segmented(c, "automation-status-filter", &as.filter, []Segment{{Label: "全部"}, {Label: "失败"}, {Label: "暂停"}}, func(i int) { as.filter = i; s.invalidate() })
							unreadOnly := as.unreadOnly
							if ui.Switch(c.Key("automation-unread-filter"), &unreadOnly).Label("未读").Changed() {
								as.unreadOnly = unreadOnly
								s.invalidate()
							}

							// 三态处理
							if loading && len(autos) == 0 {
								ui.Column(c.Key("automation-loading-box")).
									Grow(1).
									Center().
									Gap(Space2).
									Children(func() {
										LoadingIndicator(c, nil, "正在加载自动化…", IconMD)
									})
								return
							}

							if errMsg != "" && len(autos) == 0 {
								ui.Column(c.Key("automation-error-box")).
									Grow(1).
									Center().
									Gap(Space2).
									Children(func() {
										ui.Icon(c, IconOctagonAlert()).
											Size(IconLG, IconLG).
											TextColor(ThemeDanger(c))
										ui.Text(c, errMsg).
											FontSize(TypeFootnote).
											TextColor(ThemeDanger(c))
										Button(c.Key("automation-retry-btn"), ButtonOpts{
											Text:  "重试",
											Label: "重试",
											OnClick: func() {
												s.loadAutomations(curPID)
											},
											Variant: ButtonSecondary,
										})
									})
								return
							}

							// 搜索无命中时展示搜索空态
							if len(visible) == 0 && (strings.TrimSpace(searchText) != "" || as.filter != 0 || as.unreadOnly) {
								ui.Column(c.Key("automation-search-empty")).
									Grow(1).
									Center().
									Padding(Space6, Space4).
									Gap(Space3).
									Children(func() {
										ui.Icon(c, IconSearch()).
											Size(ControlLG, ControlLG).
											TextColor(ThemeSecondaryText(c))
										ui.Text(c, "未找到").
											FontSize(TypeBody).
											TextColor(ThemeSecondaryText(c))
										Button(c.Key("automation-clear-filter"), ButtonOpts{Text: "清除筛选", OnClick: func() { as.filter = 0; as.unreadOnly = false; getKanbanState(s).searchText = ""; s.invalidate() }, Variant: ButtonSecondary})
									})
								return
							}

							// 自动化列表项滚动区域
							ui.Scroll(c.Key("automation-scroll-list")).
								Grow(1).
								FillWidth().
								Children(func() {
									ui.Column(c.Key("automation-items-col")).
										FillWidth().
										Gap(Space3).
										Children(func() {
											for _, a := range visible {
												autoVal := a
												isSelected := autoVal.ID == selectedID && !isNew
												pillText, pillKind := automationCardStatus(autoVal, runningIDs[autoVal.ID])
												pillColor := automationCardStatusColor(c, pillKind)
												itemRow := ui.Column(c.Key("automation-item-"+autoVal.ID)).
													Label("自动化项 "+autoVal.Name).
													Transition(motionListItem(c)).
													FillWidth().
													Padding(Space4, Space4).
													Radius(RadiusMD).
													BorderWidth(Hairline).
													Gap(Space3)

												// 悬停：强调色边框 + 半透明底色
												if isSelected {
													itemRow.Background(ThemeSelected(c)).BorderColor(ThemeAccent(c).Alpha(0.4))
												} else if itemRow.Hovered() {
													itemRow.Background(ThemeSurface(c).Alpha(0.6)).BorderColor(ThemeAccent(c).Alpha(0.4))
												} else {
													itemRow.Background(ThemeSurface(c).Alpha(0.4)).BorderColor(ThemeOverlay(c, 0.08))
												}

												if itemRow.Clicked() {
													as.selectAutomation(autoVal, s)
												}

												itemRow.Children(func() {
													// 第一行：启用开关、名称、状态胶囊
													ui.Row(c.Key("automation-item-row1-" + autoVal.ID)).
														FillWidth().
														AlignItems(ui.Center).
														Gap(Space3).
														Children(func() {
															ui.Box(c.Key("automation-status-dot-"+autoVal.ID)).Size(8, 8).Radius(4).Background(pillColor).Label(pillText)

															nameColor := ThemeSecondaryText(c)
															if autoVal.Enabled {
																nameColor = ThemePrimaryText(c)
															}
															ui.Text(c, autoVal.Name).
																FontSize(TypeProse).
																FontWeight(700).
																TextColor(nameColor).
																Grow(1).
																SingleLine()

															if unread[autoVal.ID] > 0 {
																ui.Box(c.Key("automation-unread-dot-"+autoVal.ID)).Size(6, 6).Radius(3).Background(ThemeAccent(c)).Label("未读")
															}
														})

													// 调度描述、下次运行、上次运行
													ui.Row(c.Key("automation-item-row2-" + autoVal.ID)).
														FillWidth().
														Wrap().
														Gap(Space3).
														AlignItems(ui.Center).
														Children(func() {
															if autoVal.Enabled && !autoVal.NextRunAt.IsZero() {
																ui.Text(c, automationShortTime(autoVal.NextRunAt.Local(), time.Now().Local())).FontSize(TypeCaption).TextColor(ThemeSecondaryText(c)).SingleLine()
															} else if pillText != "" {
																ui.Text(c, pillText).FontSize(TypeCaption).TextColor(ThemeSecondaryText(c)).SingleLine()
															}
														})

												})
											}
										})
								})
						})

					}
					// 右侧编辑表单与运行记录区
					if !narrow || (as.narrowDetail && selectedID != "") {
						ui.Column(c.Key("automation-detail-col")).
							Grow(1).
							Transition(motionTransition(c, ui.ElementTransition{Duration: DurBase, Enter: &ui.Motion{Opacity: 0}})).
							FillHeight().
							Radius(RadiusMD).
							Background(ThemeSurface(c)).
							BorderWidth(Hairline).
							BorderColor(ThemeBorder(c)).
							Padding(Space5, Space5).
							Gap(Space4).
							Children(func() {
								if narrow {
									Button(c.Key("automation-back"), ButtonOpts{Text: "← 自动化", OnClick: func() { as.narrowDetail = false; s.invalidate() }, Variant: ButtonGhost})
								}
								if !isNew && selectedID == "" {
									ui.Column(c.Key("automation-select-prompt")).
										Grow(1).
										Center().
										Children(func() {
											ui.Text(c, "从左侧选择一项自动化查看，或点击“新建”创建").
												FontSize(TypeBody).
												TextColor(ThemeSecondaryText(c))
										})
									return
								}

								if selectedID != "" && !isNew {
									ui.Row(c.Key("automation-selected-header")).FillWidth().Gap(Space2).AlignItems(ui.Center).Children(func() {
										ui.Text(c, selected.Name).FontSize(TypeTitle).TextColor(ThemePrimaryText(c)).Grow(1)
										enabled := selected.Enabled
										if ui.Switch(c.Key("automation-detail-toggle"), &enabled).Label("启用自动化").Disabled(busyIDs[selectedID]).Changed() {
											s.setAutomationEnabled(selected, enabled)
										}
										Button(c.Key("automation-detail-run"), ButtonOpts{Text: "立即运行", Label: "auto-run-now-" + selectedID, Disabled: runningIDs[selectedID] || busyIDs[selectedID], OnClick: func() { s.runAutomationNow(selectedID) }, Variant: ButtonSecondary})
										more := IconButton(c.Key("automation-detail-more"), IconButtonProps{Icon: IconMoreHorizontal(), Label: "自动化更多操作", Variant: ButtonGhost, OnClick: func() { as.moreOpen = true }})
										appPopover(c, more, &as.moreOpen, nil, func() {
											popoverCard(c, ui.Column(c.Key("automation-more-menu"))).Padding(Space2, Space2).Gap(Space1).Children(func() {
												Button(c.Key("automation-copy"), ButtonOpts{Text: "复制", Label: "auto-duplicate-" + selectedID, Disabled: saving || busyIDs[selectedID], Variant: ButtonGhost, OnClick: func() { as.moreOpen = false; s.duplicateAutomation(selected) }})
												Button(c.Key("automation-delete"), ButtonOpts{Text: "删除", Variant: ButtonDanger, OnClick: func() { as.moreOpen = false; s.deleteAutomation(selectedID) }})
											})
										})
									})
									ui.Text(c, automationScheduleText(selected.Schedule)).FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
									if status, _ := automationCardStatus(selected, runningIDs[selectedID]); status != "" {
										ui.Text(c, status).FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
									}
									if selected.StopReason != "" {
										ui.Text(c, selected.StopReason).FontSize(TypeFootnote).TextColor(ThemeWarning(c))
									}
									Segmented(c, "automation-detail-tabs", &as.detailTab, []Segment{{Label: "运行记录"}, {Label: "配置"}}, func(idx int) { as.detailTab = idx; s.invalidate() })
									if as.detailTab == 0 {
										scrollKey := "automation-history-scroll"
										if openRunID != "" {
											scrollKey = "automation-history-detail-scroll"
										}
										ui.Scroll(c.Key(scrollKey)).Grow(1).FillWidth().Children(func() {
											renderAutomationRunHistory(c, s, as, false, selectedID, selectedUnread, includeArchived, runsLoading, runsErr, runs, runRead, runArchived, openRunID)
										})
									} else {
										ui.Text(c, selected.Prompt).TextColor(ThemePrimaryText(c))
										editButton := Button(c.Key("automation-edit"), ButtonOpts{Text: "编辑", OnClick: func() { as.fillAutomation(selected); as.editorOpen = true; s.invalidate() }, Variant: ButtonSecondary})
										if as.focusEditorTrigger {
											editButton.Focus()
											as.focusEditorTrigger = false
										}
									}
								}
							})
					}
				})
		})
	if as.editorOpen {
		open := true
		modalDragModal(c, "automation-editor-modal", &open, func() {
			_, height := c.Size()
			ui.Column(c.Key("automation-editor-card")).Width(700).Height(650).MaxWidth(700).MaxHeight(height-modalDragPanelInset).Background(ThemeSurface(c)).Radius(RadiusMD).Padding(Space5, Space5).Gap(Space4).Children(func() {
				renderAutomationEditor(c, s, as, isNew, selectedID, formErr, saving, confirmDelete, runningIDs, busyIDs, selectedUnread, includeArchived, runsLoading, runsErr, runs, runRead, runArchived, openRunID)
			})
		})
		if !open {
			as.closeEditor()
			s.invalidate()
		}
	}
}
