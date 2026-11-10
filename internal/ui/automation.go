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
	projectID       string
	editorProjectID string
	initialized     bool
	loading         bool
	err             string
	conflict        bool
	autos           []Automation
	loadID          int
	runsLoadID      int
	saving          bool
	saveID          int
	editSession     int
	watchStop       func()

	// 卡片级操作：正在立即运行的自动化、已暂停/重置请求中的自动化
	runningIDs       map[string]bool
	busyIDs          map[string]bool
	runRequest       int
	selectionVersion int
	confirmRun       bool
	runAfterSave     bool
	listScroll       ui.ScrollState
	historyScroll    ui.ScrollState
	keyboardFocusID  string

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
	previewEpoch     int
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
	unread      map[string]int
	unreadKnown bool

	// 删除确认
	confirmDelete        bool
	editorOpen           bool
	confirmDiscard       bool
	pendingLeave         func()
	pendingProjectChoice func(string)
	chooseProjectOpen    bool
	focusEditorTrigger   bool
	detailTab            int
	filter               int
	unreadOnly           bool
	narrowDetail         bool
	moreOpen             bool
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
		as.stopSchedulePreview()
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
	if s == nil || s.client == nil {
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
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
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
			if as.openRunID != "" {
				found := false
				for _, run := range runs {
					if run.ID == as.openRunID {
						found = true
						break
					}
				}
				if !found {
					for _, run := range as.runs {
						if run.ID == as.openRunID {
							runs = append([]AutomationRun{run}, runs...)
							break
						}
					}
				}
			}
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
	as.selectionVersion++
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
	as.editorProjectID = auto.ProjectID
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
	as.editorProjectID = as.projectID
	as.selectionVersion++
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
	editorPID := as.editorProjectID
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
	if editorPID == "" || failuresErr != nil || maxFailures <= 0 || runsErr != nil || maxRuns < 0 || maxRuns > 1000 {
		automationMu.Lock()
		as.runAfterSave = false
		as.formErr = "请选择项目，连续失败次数须为正整数，最大运行次数须为 0 到 1000 的整数"
		automationMu.Unlock()
		s.invalidate()
		return
	}
	if name == "" {
		automationMu.Lock()
		as.runAfterSave = false
		as.formErr = "自动化名称不能为空"
		automationMu.Unlock()
		s.invalidate()
		return
	}
	if prompt == "" {
		automationMu.Lock()
		as.runAfterSave = false
		as.formErr = "Prompt 不能为空"
		automationMu.Unlock()
		s.invalidate()
		return
	}

	if len(scheduleErrors) != 0 {
		automationMu.Lock()
		as.runAfterSave = false
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
		ProjectID:              editorPID,
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
			runSaved := false
			var leave func()
			if as.editSession == editSession && as.selectedID == selectedID && as.isNew == isNew && err == nil {
				if as.draft() == draft {
					runSaved = as.runAfterSave
					as.fillAutomation(saved)
					as.editorOpen = false
					as.confirmDiscard = false
					leave = as.pendingLeave
					as.pendingLeave = nil
					as.stopSchedulePreview()
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
			as.runAfterSave = false
			automationMu.Unlock()
			if leave != nil {
				leave()
			}
			if runSaved {
				s.runAutomationNow(saved.ID)
			}
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
	as.stopSchedulePreview()
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
	as.runRequest++
	request, projectID, selection := as.runRequest, as.projectID, as.selectionVersion
	automationMu.Unlock()
	s.invalidate()

	go func() {
		ctx := context.Background()
		run, err := s.client.RunAutomation(ctx, id)
		automationMu.Lock()
		if as.projectID != projectID {
			automationMu.Unlock()
			return
		}
		delete(as.runningIDs, id)
		current := as.runRequest == request && as.selectedID == id && as.selectionVersion == selection
		if current && err == nil {
			openAutomationRun(as, run.ID)
			as.runs = append([]AutomationRun{run}, as.runs...)
		}
		automationMu.Unlock()
		s.invalidate()
		if !current {
			return
		}

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
		if run.Status == "failed" || run.Status == "error" {
			s.setToast("运行自动化失败: " + run.Error)
		}
		s.loadAutomationRuns(id)
	}()
}

func (s *chatState) requestAutomationRun(id string) {
	as := getAutomationState(s)
	if as.selectedID == id && as.dirty() {
		as.editorOpen = true
		as.confirmRun = true
		s.invalidate()
		return
	}
	if as.editorOpen {
		as.editorOpen = false
	}
	s.runAutomationNow(id)
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
	if s != nil {
		renderAutomationWorkspaceInScope(c, s, automationWorkspaceProps{ScopeID: s.currentWorkspaceProjectID(), SearchText: searchText})
	}
}
