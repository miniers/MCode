package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"math"
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
	// automationEmptyHintMaxWidth 是空态说明文字的最大宽度，超出换行。
	automationEmptyHintMaxWidth float32 = 480
	// automationPillSpinnerSize 是运行中状态胶囊里转圈的边长。
	automationPillSpinnerSize float32 = 9
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
	timeOfDay        string // "09:00"
	dayOfWeek        int    // 0-6 (0 为周日，1 为周一)
	cronExpr         string // "*/15"
	onceDateStr      string // "2006-01-02 15:04"
	stopWhen         string
	enabled          bool
	maxFailuresInput string // 默认 "3"
	maxRunsInput     string // 默认 "0"，0 表示不限制
	catchUp          bool   // 错过触发时是否补跑，默认开启
	formErr          string

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
	confirmDelete bool
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
						if refreshForm {
							as.fillAutomation(a)
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
	if auto.Schedule.TimeOfDay != "" {
		as.timeOfDay = auto.Schedule.TimeOfDay
	} else {
		as.timeOfDay = "09:00"
	}
	as.dayOfWeek = auto.Schedule.DayOfWeek
	if auto.Schedule.Expression != "" {
		as.cronExpr = auto.Schedule.Expression
	} else {
		as.cronExpr = "*/15 * * * *"
	}
	if !auto.Schedule.Date.IsZero() {
		as.onceDateStr = auto.Schedule.Date.Local().Format("2006-01-02 15:04")
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
}

// resetToNewAutomation 重置表单进入新建状态。
func (as *automationState) resetToNewAutomation(s *chatState) {
	automationMu.Lock()
	as.editSession++
	as.selectedID = ""
	as.isNew = true
	as.editRevision = 0
	as.schedule = AutomationSchedule{}
	as.name = ""
	as.prompt = ""
	as.scheduleType = "interval"
	as.intervalInput = "60"
	as.timeOfDay = "09:00"
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
	automationMu.Unlock()
	s.invalidate()
}

// automationDraftSnapshot 用于保留保存请求发出后的编辑。
func (as *automationState) automationDraftSnapshot() ([15]string, int, bool, bool) {
	return [15]string{as.name, as.prompt, as.scheduleType, as.intervalInput, as.timeOfDay,
		as.cronExpr, as.onceDateStr, as.stopWhen, as.maxFailuresInput, as.maxRunsInput,
		as.provider, as.model, as.effort, as.runtimeMode, as.runMode}, as.dayOfWeek, as.enabled, as.catchUp
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
	schedType := as.scheduleType
	if schedType == "" {
		schedType = "interval"
	}
	intervalMinutes, intervalErr := strconv.ParseFloat(strings.TrimSpace(as.intervalInput), 64)
	timeOfDay := strings.TrimSpace(as.timeOfDay)
	dayOfWeek := as.dayOfWeek
	cronExpr := strings.TrimSpace(as.cronExpr)
	onceDateStr := strings.TrimSpace(as.onceDateStr)
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
	originalSchedule := as.schedule
	editSession := as.editSession
	draft, draftDay, draftEnabled, draftCatchUp := as.automationDraftSnapshot()
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

	var sched AutomationSchedule
	sched.Type = schedType
	sched.Timezone = originalSchedule.Timezone

	switch schedType {
	case "interval":
		// 间隔未改动时原样保留原始秒数，避免把非预设值（如 90 秒）吸附成预设档
		if originalSchedule.Type == "interval" && originalSchedule.Seconds > 0 &&
			originalSchedule.Seconds == int(math.Round(intervalMinutes*60)) {
			sched.Seconds = originalSchedule.Seconds
			break
		}
		seconds := intervalMinutes * 60
		if intervalErr != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) ||
			seconds < 1 || seconds >= float64(math.MaxInt64/int64(time.Second)) {
			automationMu.Lock()
			as.formErr = "运行间隔必须大于 0 分钟"
			automationMu.Unlock()
			s.invalidate()
			return
		}
		sched.Seconds = int(math.Round(seconds))
	case "daily", "weekdays":
		if _, err := time.Parse("15:04", timeOfDay); err != nil {
			automationMu.Lock()
			as.formErr = "时间格式必须符合 HH:MM"
			automationMu.Unlock()
			s.invalidate()
			return
		}
		sched.TimeOfDay = timeOfDay
	case "weekly":
		_, err := time.Parse("15:04", timeOfDay)
		if err != nil || dayOfWeek < 0 || dayOfWeek > 6 {
			automationMu.Lock()
			as.formErr = "时间格式必须符合 HH:MM"
			automationMu.Unlock()
			s.invalidate()
			return
		}
		sched.TimeOfDay = timeOfDay
		sched.DayOfWeek = dayOfWeek
	case "cron":
		if len(strings.Fields(cronExpr)) != 5 {
			automationMu.Lock()
			as.formErr = "cron 表达式必须包含五个字段，例如 */15 * * * *"
			automationMu.Unlock()
			s.invalidate()
			return
		}
		sched.Expression = cronExpr
	case "once":
		parsedTime, err := time.ParseInLocation("2006-01-02 15:04", onceDateStr, time.Local)
		if err != nil {
			automationMu.Lock()
			as.formErr = "单次时间格式必须为 2006-01-02 15:04"
			automationMu.Unlock()
			s.invalidate()
			return
		}
		sched.Date = parsedTime
		// 时间文本未改动时保留原始 Date，避免截断秒
		if originalSchedule.Type == "once" && !originalSchedule.Date.IsZero() &&
			onceDateStr == originalSchedule.Date.Local().Format("2006-01-02 15:04") {
			sched.Date = originalSchedule.Date
		}
	default:
		automationMu.Lock()
		as.formErr = "请选择有效的调度类型"
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
			if as.editSession == editSession && as.selectedID == selectedID && as.isNew == isNew && err == nil {
				currentDraft, day, enabled, catchUp := as.automationDraftSnapshot()
				if currentDraft == draft && day == draftDay && enabled == draftEnabled && catchUp == draftCatchUp {
					as.fillAutomation(saved)
				} else {
					as.selectedID = saved.ID
					as.isNew = false
					as.editRevision = saved.Revision
					as.schedule = saved.Schedule
				}
			}
			automationMu.Unlock()
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
	case !auto.Enabled && auto.ConsecutiveFailures > 0:
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
	conflict := as.conflict
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
	visible := filterAutomations(autos, searchText)
	// 未读运行记录总数：自动化入口角标依据
	totalUnread := automationUnreadTotal(unread, visible)
	// 当前选中自动化在已加载记录中是否还有未读：“全部标为已读”按钮依据
	selectedUnread := 0
	for _, run := range runs {
		if automationRunUnread(runRead, run.ID) {
			selectedUnread++
		}
	}

	// 汇总：已启用数量与最近一次计划运行时间
	enabledCount := 0
	var nextRunAt time.Time
	for _, a := range visible {
		if !a.Enabled {
			continue
		}
		enabledCount++
		if !a.NextRunAt.IsZero() && (nextRunAt.IsZero() || a.NextRunAt.Before(nextRunAt)) {
			nextRunAt = a.NextRunAt
		}
	}

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
			if !loading && errMsg == "" && len(autos) == 0 && !isNew && selectedID == "" {
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
								ui.Text(c, "自动化会按计划自动启动 Agent 会话，完成巡检、审计或看护等周期任务，并在满足停止条件时自动结束。").
									FontSize(TypeBody).
									TextColor(ThemeSecondaryText(c)).
									TextAlign(ui.Center).
									MaxLines(2).
									MaxWidth(automationEmptyHintMaxWidth)
							})
						Button(c.Key("automation-empty-create"), ButtonOpts{
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
					})
				return
			}

			// 页面顶栏汇总：已启用 X / 共 Y、下次运行、新建
			ui.Row(c.Key("automation-summary")).
				FillWidth().
				Padding(Space3, Space6).
				Radius(RadiusMD).
				Background(ThemeOverlay(c, 0.04)).
				AlignItems(ui.Center).
				Gap(Space5).
				Children(func() {
					ui.Row(c.Key("automation-summary-active")).Gap(Space2).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, IconCircleCheck()).
							Size(IconSM, IconSM).
							TextColor(ThemeAccent(c))
						ui.Text(c, fmt.Sprintf("已启用 %d / 共 %d", enabledCount, len(visible))).
							FontSize(TypeBody).
							FontWeight(500).
							TextColor(ThemePrimaryText(c))
					})
					if !nextRunAt.IsZero() {
						ui.Row(c.Key("automation-summary-next")).Gap(Space2).AlignItems(ui.Center).Children(func() {
							ui.Icon(c, IconClock()).
								Size(IconSM, IconSM).
								TextColor(ThemeSecondaryText(c))
							ui.Text(c, "下次运行 "+formatRelativeUntil(nextRunAt.Local())).
								FontSize(TypeCaption).
								TextColor(ThemeSecondaryText(c)).
								SingleLine()
						})
					}
					ui.Box(c.Key("automation-summary-spacer")).Grow(1)
					Button(c.Key("automation-summary-new-btn"), ButtonOpts{
						Icon:     IconPlus(),
						Text:     "新建自动化",
						Label:    "automation-summary-new-btn",
						Disabled: curPID == "" || saving,
						OnClick: func() {
							as.resetToNewAutomation(s)
						},
						Variant: ButtonPrimary,
						Size:    ControlSM,
					})
				})
			if errMsg != "" && len(autos) > 0 {
				ui.Text(c, "刷新失败: "+errMsg).FontSize(TypeFootnote).TextColor(ThemeDanger(c))
			}
			// 1. 版本冲突横幅
			if conflict {
				ui.Row(c.Key("automation-conflict-banner")).
					FillWidth().
					Padding(Space3, Space4).
					Radius(RadiusMD).
					Background(ThemeWarning(c).Alpha(0.16)).
					AlignItems(ui.Center).
					Gap(Space4).
					Children(func() {
						ui.Icon(c, IconOctagonAlert()).
							Size(IconMD, IconMD).
							TextColor(ThemeWarning(c))
						ui.Text(c, "数据已在别处被修改，请刷新后重试").
							FontSize(TypeBody).
							FontWeight(500).
							TextColor(ThemeWarning(c)).
							Grow(1)
						Button(c.Key("automation-conflict-refresh"), ButtonOpts{
							Text:  "刷新",
							Label: "automation-conflict-refresh",
							OnClick: func() {
								automationMu.Lock()
								as.conflict = false
								automationMu.Unlock()
								s.loadAutomations(curPID)
							},
							Variant: ButtonPrimary,
						})
					})
			}

			// 2. 左右双栏：左侧列表，右侧编辑与运行详情
			ui.Row(c.Key("automation-main-row")).
				Grow(1).
				FillWidth().
				Gap(Space5).
				Children(func() {
					// 左侧自动化列表区
					ui.Column(c.Key("automation-list-col")).
						Width(240).
						FillHeight().
						Radius(RadiusMD).
						Background(ThemeOverlay(c, 0.06)).
						BorderWidth(Hairline).
						BorderColor(ThemeBorder(c)).
						Padding(Space3, Space4).
						Gap(Space3).
						Children(func() {
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

									Button(c.Key("automation-new-btn"), ButtonOpts{
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
								})

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
							if len(visible) == 0 && searchText != "" {
								ui.Column(c.Key("automation-search-empty")).
									Grow(1).
									Center().
									Padding(Space6, Space4).
									Gap(Space3).
									Children(func() {
										ui.Icon(c, IconSearch()).
											Size(ControlLG, ControlLG).
											TextColor(ThemeSecondaryText(c))
										ui.Text(c, "没有匹配的自动化").
											FontSize(TypeBody).
											TextColor(ThemeSecondaryText(c))
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
												isRunning := runningIDs[autoVal.ID] || autoVal.LastRunStatus == "running"
												isBusy := busyIDs[autoVal.ID]
												pillText, pillKind := automationCardStatus(autoVal, runningIDs[autoVal.ID])
												pillColor := automationCardStatusColor(c, pillKind)
												itemRow := ui.Column(c.Key("automation-item-"+autoVal.ID)).
													Label("自动化项 "+autoVal.Name).
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

												// 后端缺对应方法的右键菜单项已省略
												itemRow.ContextMenu(func(m *ui.Menu) {
													if !isRunning {
														if m.Item("立即运行").Chosen() {
															s.runAutomationNow(autoVal.ID)
														}
													}
													if m.Item("查看运行记录").Chosen() {
														as.selectAutomation(autoVal, s)
													}
													if m.Item("编辑").Chosen() {
														as.selectAutomation(autoVal, s)
													}
													if autoVal.ConsecutiveFailures > 0 {
														if m.Item("重置失败计数").Chosen() {
															s.setAutomationEnabled(autoVal, true)
														}
													}
													m.Separator()
													toggleLabel := "暂停"
													if !autoVal.Enabled {
														toggleLabel = "恢复"
													}
													if m.Item(toggleLabel).Chosen() {
														s.setAutomationEnabled(autoVal, !autoVal.Enabled)
													}
													if m.Item("复制").Chosen() {
														s.duplicateAutomation(autoVal)
													}
													m.Separator()
													if m.Item("删除").Danger().Chosen() {
														s.deleteAutomation(autoVal.ID)
													}
												})

												itemRow.Children(func() {
													// 第一行：启用开关、名称、状态胶囊
													ui.Row(c.Key("automation-item-row1-" + autoVal.ID)).
														FillWidth().
														AlignItems(ui.Center).
														Gap(Space3).
														Children(func() {
															enabledVal := autoVal.Enabled
															// 标签只交给无障碍树，不作为可见文字显示
															ui.Switch(c.Key("automation-item-toggle-"+autoVal.ID), &enabledVal).
																Label("启用自动化 " + autoVal.Name)
															if enabledVal != autoVal.Enabled {
																s.setAutomationEnabled(autoVal, enabledVal)
															}

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

															if pillText != "" {
																ui.Row(c.Key("auto-status-pill-"+autoVal.ID)).
																	Padding(Space1, Space2).
																	Radius(RadiusMD).
																	AlignItems(ui.Center).
																	Gap(Space1).
																	Background(pillColor.Alpha(0.15)).
																	Children(func() {
																		if pillKind == "running" {
																			LoadingIndicator(c, nil, "", automationPillSpinnerSize, pillColor)
																		}
																		ui.Text(c, pillText).
																			FontSize(TypeCaption).
																			FontWeight(700).
																			TextColor(pillColor)
																	})
															}
														})

													// 调度描述、下次运行、上次运行
													ui.Row(c.Key("automation-item-row2-" + autoVal.ID)).
														FillWidth().
														Wrap().
														Gap(Space3).
														AlignItems(ui.Center).
														Children(func() {
															ui.Row(c.Key("auto-schedule-chip-"+autoVal.ID)).
																Padding(Space1, Space2).
																Radius(RadiusXS).
																AlignItems(ui.Center).
																Background(ThemeAccent(c).Alpha(0.10)).
																Children(func() {
																	ui.Text(c, automationScheduleText(autoVal.Schedule)).
																		FontSize(TypeCaption).
																		FontWeight(500).
																		TextColor(ThemeAccent(c)).
																		SingleLine()
																})
															// 已启用但没有下次运行时间时不显示占位文案
															if autoVal.Enabled {
																if !autoVal.NextRunAt.IsZero() {
																	ui.Text(c, "下次 "+formatRelativeUntil(autoVal.NextRunAt.Local())).
																		FontSize(TypeCaption).
																		TextColor(ThemeSecondaryText(c)).
																		SingleLine()
																}
															} else if strings.TrimSpace(autoVal.StopReason) != "" {
																ui.Text(c, autoVal.StopReason).
																	FontSize(TypeCaption).
																	TextColor(ThemeWarning(c)).
																	SingleLine()
															}
															if !autoVal.LastRunAt.IsZero() {
																ui.Text(c, "上次 "+formatRelativeTime(autoVal.LastRunAt.Local())).
																	FontSize(TypeCaption).
																	TextColor(ThemeSecondaryText(c)).
																	SingleLine()
															}
														})

													// Prompt 预览，最多两行
													if strings.TrimSpace(autoVal.Prompt) != "" {
														ui.Text(c, autoVal.Prompt).
															FontSize(TypeFootnote).
															TextColor(ThemeSecondaryText(c)).
															MaxLines(2)
													}

													// 收敛目标（stopWhen）
													if strings.TrimSpace(autoVal.StopWhen) != "" {
														ui.Text(c, "收敛目标: "+autoVal.StopWhen).
															FontSize(TypeCaption).
															FontWeight(500).
															TextColor(ThemeAccent(c)).
															SingleLine()
													}

													// 连续失败警告与重置
													if autoVal.ConsecutiveFailures > 0 {
														ui.Row(c.Key("auto-failures-"+autoVal.ID)).
															FillWidth().
															Padding(Space1, Space2).
															Radius(RadiusXS).
															Background(ThemeDanger(c).Alpha(0.08)).
															AlignItems(ui.Center).
															Gap(Space2).
															Children(func() {
																ui.Icon(c, IconOctagonAlert()).
																	Size(IconSM, IconSM).
																	TextColor(ThemeDanger(c))
																ui.Text(c, fmt.Sprintf("连续失败 %d 次（上限 %d）", autoVal.ConsecutiveFailures, autoVal.MaxConsecutiveFailures)).
																	FontSize(TypeCaption).
																	TextColor(ThemeDanger(c)).
																	Grow(1).
																	SingleLine()
																Button(c.Key("auto-reset-failures-"+autoVal.ID), ButtonOpts{
																	Text:     "重置",
																	Label:    "auto-reset-failures-" + autoVal.ID,
																	Disabled: isBusy,
																	OnClick: func() {
																		s.setAutomationEnabled(autoVal, true)
																	},
																	Variant: ButtonSecondary,
																	Size:    ControlXS,
																})
															})
													}

													// 操作行前的 1px 分隔线
													ui.Box(c.Key("auto-actions-divider-" + autoVal.ID)).
														FillWidth().
														Height(Hairline).
														Background(ThemeOverlay(c, 0.2))

													// 操作：立即运行、运行记录、复制；卡片窄时换行，避免「复制」被裁掉。
													ui.Row(c.Key("automation-item-actions-" + autoVal.ID)).
														FillWidth().
														Wrap().
														AlignItems(ui.Center).
														Gap(Space3).
														Children(func() {
															ui.Box(c.Key("auto-actions-spacer-" + autoVal.ID)).Grow(1)

															Button(c.Key("auto-run-now-"+autoVal.ID), ButtonOpts{
																Icon:     IconPlay(),
																Text:     "立即运行",
																Label:    "auto-run-now-" + autoVal.ID,
																Disabled: isRunning || isBusy,
																OnClick: func() {
																	s.runAutomationNow(autoVal.ID)
																},
																Variant: ButtonSecondary,
																Size:    ControlXS,
															})

															Button(c.Key("auto-runs-"+autoVal.ID), ButtonOpts{
																Text:  fmt.Sprintf("运行记录(%d)", autoVal.TotalRunCount),
																Label: "auto-runs-" + autoVal.ID,
																OnClick: func() {
																	as.selectAutomation(autoVal, s)
																},
																Variant: ButtonSecondary,
																Size:    ControlXS,
															})

															Button(c.Key("auto-duplicate-"+autoVal.ID), ButtonOpts{
																Text:     "复制",
																Label:    "auto-duplicate-" + autoVal.ID,
																Disabled: saving || isBusy,
																OnClick: func() {
																	s.duplicateAutomation(autoVal)
																},
																Variant: ButtonSecondary,
																Size:    ControlXS,
															})
														})
												})
											}
										})
								})
						})

					// 右侧编辑表单与运行记录区
					ui.Column(c.Key("automation-detail-col")).
						Grow(1).
						FillHeight().
						Radius(RadiusMD).
						Background(ThemeSurface(c)).
						BorderWidth(Hairline).
						BorderColor(ThemeBorder(c)).
						Padding(Space5, Space5).
						Gap(Space4).
						Children(func() {
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

							renderAutomationEditor(c, s, as, isNew, selectedID, formErr, saving, confirmDelete, runningIDs, busyIDs, selectedUnread, includeArchived, runsLoading, runsErr, runs, runRead, runArchived, openRunID)
						})
				})
		})
}
