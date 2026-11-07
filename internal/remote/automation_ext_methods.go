package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

// 本文件注册自动化运行与管理的扩展方法：
//   - automations.list：项目的自动化列表与未读数；
//   - automations.runs：某个自动化的运行记录（含已读/归档状态）；
//   - automations.run：立即触发一次运行；
//   - automations.markRead：把一条运行记录标为已读；
//   - automations.archiveRun：归档或取消归档一条运行记录；
//   - automations.create：新建自动化；
//   - automations.update：修改自动化（带可选 revision 乐观并发）；
//   - automations.delete：删除自动化（必须显式 confirm）；
//   - automations.setEnabled：暂停/恢复自动化（恢复时同时清空连续失败计数）；
//   - automations.reset：重置（等价于 setEnabled(true)，即桌面端的「重置」）。
//
// 写操作的边界：
//   - 只改自动化定义本身，不提供任何执行任意命令的新通道（立即运行仍只走 automations.run）；
//   - 入参只允许桌面端表单能改的字段：项目只能随 create 指定，update 不能改项目，
//     运行时状态（status/nextRunAt/consecutiveFailures/revision）一律不可写；
//   - create 校验 projectID 必须是当前可见项目，避免写出孤儿记录；
//   - delete 与桌面端的二次确认对齐，要求 confirm=true；
//   - 写成功后沿用 automations.changed 广播，桌面端与其它客户端一并刷新。
//
// 数据与桌面端同源：internal/app/remote_automation.go 注入 "automation" 服务，
// 由它转调 Client.Automations / CreateAutomation / UpdateAutomation / DeleteAutomation /
// AutomationRunsState / RunAutomation / MarkAutomationRunRead / SetAutomationRunArchived。

const (
	// automationExtServiceName 是自动化服务在服务注册表里的名字，与 internal/app 的注入保持一致。
	automationExtServiceName = "automation"
	// automationExtChangedMethod 是自动化或运行记录变化后广播的通知方法名。
	automationExtChangedMethod = "automations.changed"
	// automationExtRunsDefaultLimit 是 automations.runs 省略 limit 时的返回条数。
	automationExtRunsDefaultLimit = 30
	// automationExtRunsMaxLimit 是 automations.runs 允许的最大返回条数。
	automationExtRunsMaxLimit = 100
	// automationExtMaxNameRunes 是自动化名称长度上限（按字符数计）。
	automationExtMaxNameRunes = 64
	// automationExtMaxPromptRunes 是提示词长度上限。
	automationExtMaxPromptRunes = 8000
	// automationExtMaxStopWhenRunes 是停止条件长度上限。
	automationExtMaxStopWhenRunes = 2000
	// automationExtMaxConsecutiveFailures 是连续失败上限的上限值（下限为 1）。
	automationExtMaxConsecutiveFailures = 20
	// automationExtMaxRuns 是最大运行次数（maxRuns）的上限值（0 表示不限制）。
	automationExtMaxRuns = 1000
	// automationExtMaxIterations 是旧字段 maxIterations 的上限值，与 maxRuns 同步提到 1000，
	// 否则镜像写入的较大值会在下一次远程更新时被旧校验拒绝。
	automationExtMaxIterations = 1000
	// automationExtDefaultConsecutiveFailures 是省略 maxConsecutiveFailures 时的默认值，与桌面端表单一致。
	automationExtDefaultConsecutiveFailures = 3
)

// ErrAutomationExtConflict 表示自动化版本冲突（revision 与最新版本不一致）。
// 服务实现（internal/app/remote_automation.go）把客户端的 mcodeui.ErrRevisionConflict
// 包成它，远程层据此回 wire.ErrCodeVersionConflict，客户端可以提示用户刷新后重试。
var ErrAutomationExtConflict = errors.New("automation revision conflict")

// automationExtService 是自动化服务，由 app 层注入（见 internal/app/remote_automation.go）。
// 返回值已经是协议层结构，错误保持桌面端的原始文案（冲突错误已由 Client 包装）。
type automationExtService interface {
	// Automations 返回项目下的自动化与各自未读数；projectID 为空表示全部项目。
	Automations(ctx context.Context, projectID string) ([]wire.AutomationExtAutomation, map[string]int, error)
	// Runs 返回运行记录，includeArchived 为 true 时一并返回已归档记录。
	Runs(ctx context.Context, automationID string, limit int, includeArchived bool) ([]wire.AutomationExtRun, error)
	// RunNow 立即触发一次运行并返回新建的运行记录。
	RunNow(ctx context.Context, automationID string) (wire.AutomationExtRun, error)
	// MarkRunRead 把一条运行记录标为已读。
	MarkRunRead(ctx context.Context, runID string) error
	// ArchiveRun 归档或取消归档一条运行记录。
	ArchiveRun(ctx context.Context, runID string, archived bool) error
	// Create 新建自动化；projectID 的存在性由调用方校验。
	Create(ctx context.Context, projectID string, in wire.AutomationExtInput) (wire.AutomationExtAutomation, error)
	// Update 修改自动化；revision<=0 时由实现取当前版本号（即不校验并发）。
	Update(ctx context.Context, automationID string, in wire.AutomationExtInput, revision int) (wire.AutomationExtAutomation, error)
	// Delete 删除自动化；不存在或不可见时返回可读错误。
	Delete(ctx context.Context, automationID string) error
	// SetEnabled 暂停或恢复自动化；enabled=true 同时清空连续失败计数（即桌面端的「重置」）。
	SetEnabled(ctx context.Context, automationID string, enabled bool, revision int) (wire.AutomationExtAutomation, error)
}

func init() {
	RegisterMethod("automations.list", handleAutomationsList)
	RegisterMethod("automations.runs", handleAutomationsRuns)
	RegisterMethod("automations.run", handleAutomationsRun)
	RegisterMethod("automations.markRead", handleAutomationsMarkRead)
	RegisterMethod("automations.archiveRun", handleAutomationsArchiveRun)
	RegisterMethod("automations.create", handleAutomationsCreate)
	RegisterMethod("automations.update", handleAutomationsUpdate)
	RegisterMethod("automations.delete", handleAutomationsDelete)
	RegisterMethod("automations.setEnabled", handleAutomationsSetEnabled)
	RegisterMethod("automations.reset", handleAutomationsReset)
}

// handleAutomationsList 返回项目下的自动化列表与未读数。
func handleAutomationsList(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.AutomationExtListParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(p.ProjectID)
	if projectID != "" && !isValidUUID(projectID) {
		return nil, ExtInvalidParams("Invalid params: 'projectID' must be a valid UUID")
	}
	svc, ok := automationExtServiceOf(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	automations, counts, err := svc.Automations(ctx, projectID)
	if err != nil {
		return nil, automationExtWrapError(err)
	}
	if automations == nil {
		// 统一输出空数组，别让客户端处理 null。
		automations = []wire.AutomationExtAutomation{}
	}
	// 未读数以 counts 为准，避免列表与未读角标两处数据不一致。
	for i := range automations {
		automations[i].UnreadCount = counts[automations[i].ID]
	}
	if counts == nil {
		counts = map[string]int{}
	}
	return wire.AutomationExtListResult{Automations: automations, UnreadCounts: counts}, nil
}

// handleAutomationsRuns 返回某个自动化的运行记录。
func handleAutomationsRuns(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.AutomationExtRunsParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	automationID := strings.TrimSpace(p.AutomationID)
	if !isValidUUID(automationID) {
		return nil, ExtInvalidParams("Invalid params: 'automationID' must be a valid UUID")
	}
	svc, ok := automationExtServiceOf(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	runs, err := svc.Runs(ctx, automationID, automationExtRunsLimit(p.Limit), p.IncludeArchived)
	if err != nil {
		return nil, automationExtWrapError(err)
	}
	if runs == nil {
		runs = []wire.AutomationExtRun{}
	}
	return wire.AutomationExtRunsResult{AutomationID: automationID, Runs: runs}, nil
}

// handleAutomationsRun 立即触发一次运行，成功后广播变更。
func handleAutomationsRun(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.AutomationExtRunParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	automationID := strings.TrimSpace(p.AutomationID)
	if !isValidUUID(automationID) {
		return nil, ExtInvalidParams("Invalid params: 'automationID' must be a valid UUID")
	}
	svc, ok := automationExtServiceOf(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	run, err := svc.RunNow(ctx, automationID)
	if err != nil {
		return nil, automationExtWrapError(err)
	}
	automationExtNotifyChanged(d, automationID, run.ID)
	return wire.AutomationExtRunResult{Run: run}, nil
}

// handleAutomationsMarkRead 把一条运行记录标为已读。
func handleAutomationsMarkRead(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.AutomationExtMarkReadParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	runID := strings.TrimSpace(p.RunID)
	if !isValidUUID(runID) {
		return nil, ExtInvalidParams("Invalid params: 'runID' must be a valid UUID")
	}
	svc, ok := automationExtServiceOf(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	if err := svc.MarkRunRead(ctx, runID); err != nil {
		return nil, automationExtWrapError(err)
	}
	automationExtNotifyChanged(d, "", runID)
	return wire.AutomationExtMarkReadResult{RunID: runID}, nil
}

// handleAutomationsArchiveRun 归档或取消归档一条运行记录。
func handleAutomationsArchiveRun(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.AutomationExtArchiveRunParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	runID := strings.TrimSpace(p.RunID)
	if !isValidUUID(runID) {
		return nil, ExtInvalidParams("Invalid params: 'runID' must be a valid UUID")
	}
	svc, ok := automationExtServiceOf(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	if err := svc.ArchiveRun(ctx, runID, p.Archived); err != nil {
		return nil, automationExtWrapError(err)
	}
	automationExtNotifyChanged(d, "", runID)
	return wire.AutomationExtArchiveRunResult{RunID: runID, Archived: p.Archived}, nil
}

// handleAutomationsCreate 新建自动化：校验项目与入参后写入，并广播变更。
func handleAutomationsCreate(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.AutomationExtCreateParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(p.ProjectID)
	if !isValidUUID(projectID) {
		return nil, ExtInvalidParams("Invalid params: 'projectID' must be a valid UUID")
	}
	input, err := automationExtNormalizeInput(p.Input)
	if err != nil {
		return nil, err
	}
	svc, ok := automationExtServiceOf(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	exists, err := automationExtProjectExists(ctx, d, projectID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ExtInvalidParams("Invalid params: 'projectID' 不存在或不可访问")
	}
	created, err := svc.Create(ctx, projectID, input)
	if err != nil {
		return nil, automationExtWrapError(err)
	}
	// 创建成功就已改变该项目下的自动化列表，必须广播。
	// 这里不用 automationExtNotifyChanged：它要求 automationID/runID 至少一个非空，
	// 而服务若没回 ID 就会静默丢掉这次广播，客户端列表便停留在旧状态。
	if d != nil {
		d.NotifyAuthed(automationExtChangedMethod, wire.AutomationExtChanged{AutomationID: strings.TrimSpace(created.ID)})
	}
	return wire.AutomationExtAutomationResult{Automation: created}, nil
}

// handleAutomationsUpdate 修改自动化；revision 非 0 时做乐观并发校验。
func handleAutomationsUpdate(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.AutomationExtUpdateParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	automationID := strings.TrimSpace(p.AutomationID)
	if !isValidUUID(automationID) {
		return nil, ExtInvalidParams("Invalid params: 'automationID' must be a valid UUID")
	}
	if p.Revision < 0 {
		return nil, ExtInvalidParams("Invalid params: 'revision' 不能为负数")
	}
	input, err := automationExtNormalizeInput(p.Input)
	if err != nil {
		return nil, err
	}
	svc, ok := automationExtServiceOf(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	updated, err := svc.Update(ctx, automationID, input, p.Revision)
	if err != nil {
		return nil, automationExtWrapError(err)
	}
	automationExtNotifyChanged(d, automationID, "")
	return wire.AutomationExtAutomationResult{Automation: updated}, nil
}

// handleAutomationsDelete 删除自动化。桌面端删除有二次确认，远程同样要求显式 confirm=true。
func handleAutomationsDelete(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.AutomationExtDeleteParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	automationID := strings.TrimSpace(p.AutomationID)
	if !isValidUUID(automationID) {
		return nil, ExtInvalidParams("Invalid params: 'automationID' must be a valid UUID")
	}
	if !p.Confirm {
		return nil, ExtInvalidParams("Invalid params: 删除自动化必须显式确认（confirm=true）")
	}
	svc, ok := automationExtServiceOf(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	if err := svc.Delete(ctx, automationID); err != nil {
		return nil, automationExtWrapError(err)
	}
	automationExtNotifyChanged(d, automationID, "")
	return wire.AutomationExtDeleteResult{AutomationID: automationID, Deleted: true}, nil
}

// handleAutomationsSetEnabled 暂停或恢复自动化；恢复时由服务层同时清空连续失败计数。
func handleAutomationsSetEnabled(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.AutomationExtSetEnabledParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	automationID := strings.TrimSpace(p.AutomationID)
	if !isValidUUID(automationID) {
		return nil, ExtInvalidParams("Invalid params: 'automationID' must be a valid UUID")
	}
	if p.Revision < 0 {
		return nil, ExtInvalidParams("Invalid params: 'revision' 不能为负数")
	}
	svc, ok := automationExtServiceOf(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	saved, err := svc.SetEnabled(ctx, automationID, p.Enabled, p.Revision)
	if err != nil {
		return nil, automationExtWrapError(err)
	}
	automationExtNotifyChanged(d, automationID, "")
	return wire.AutomationExtAutomationResult{Automation: saved}, nil
}

// handleAutomationsReset 重置自动化：与桌面端「重置」一致，等价于 setEnabled(true)，
// 启用并清空连续失败计数。桌面端重置没有二次确认，所以这里也不要求 confirm。
func handleAutomationsReset(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.AutomationExtResetParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	automationID := strings.TrimSpace(p.AutomationID)
	if !isValidUUID(automationID) {
		return nil, ExtInvalidParams("Invalid params: 'automationID' must be a valid UUID")
	}
	if p.Revision < 0 {
		return nil, ExtInvalidParams("Invalid params: 'revision' 不能为负数")
	}
	svc, ok := automationExtServiceOf(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	saved, err := svc.SetEnabled(ctx, automationID, true, p.Revision)
	if err != nil {
		return nil, automationExtWrapError(err)
	}
	automationExtNotifyChanged(d, automationID, "")
	return wire.AutomationExtAutomationResult{Automation: saved}, nil
}

// automationExtProjectExists 确认 projectID 在当前可见项目列表里。
// 后端不支持项目列表时按不存在处理，避免把自动化写到不可访问的项目下。
func automationExtProjectExists(ctx context.Context, d *Dispatcher, projectID string) (bool, error) {
	if d == nil || d.backend == nil {
		return false, nil
	}
	list, err := d.backend.ListProjects(ctx)
	if err != nil {
		return false, err
	}
	for _, p := range list.Projects {
		if p.ID == projectID {
			return true, nil
		}
	}
	return false, nil
}

// automationExtRuntimeModes 是新建会话允许的权限模式取值，空串表示跟随默认。
var automationExtRuntimeModes = []string{"readonly", "supervised", "auto", "fullAccess"}

// automationExtNormalizeInput 校验并归一化写操作入参：去首尾空白、补默认值。
// 任一字段不合法就整体拒绝，不做部分写入。
func automationExtNormalizeInput(in wire.AutomationExtInput) (wire.AutomationExtInput, error) {
	out := in
	out.Name = strings.TrimSpace(in.Name)
	out.Prompt = strings.TrimSpace(in.Prompt)
	out.StopWhen = strings.TrimSpace(in.StopWhen)
	out.Schedule.Type = strings.TrimSpace(in.Schedule.Type)
	out.Schedule.Date = strings.TrimSpace(in.Schedule.Date)
	out.Schedule.TimeOfDay = strings.TrimSpace(in.Schedule.TimeOfDay)
	out.Schedule.Timezone = strings.TrimSpace(in.Schedule.Timezone)
	out.Schedule.Expression = strings.TrimSpace(in.Schedule.Expression)
	// 模型与权限：nil 保持 nil（表示不改），有值时去首尾空白。
	if in.Provider != nil {
		v := strings.TrimSpace(*in.Provider)
		out.Provider = &v
	}
	if in.Model != nil {
		v := strings.TrimSpace(*in.Model)
		out.Model = &v
	}
	if in.Effort != nil {
		v := strings.TrimSpace(*in.Effort)
		out.Effort = &v
	}
	if in.RuntimeMode != nil {
		v := harness.NormalizeMode(strings.TrimSpace(*in.RuntimeMode))
		if v != "" {
			valid := false
			for _, mode := range automationExtRuntimeModes {
				if v == mode {
					valid = true
					break
				}
			}
			if !valid {
				return out, ExtInvalidParams("Invalid params: 'runtimeMode' 只能是 readonly、supervised、auto、fullAccess 或空串（跟随默认）")
			}
		}
		out.RuntimeMode = &v
	}

	if out.Name == "" {
		return out, ExtInvalidParams("Invalid params: 自动化名称不能为空")
	}
	if utf8.RuneCountInString(out.Name) > automationExtMaxNameRunes {
		return out, ExtInvalidParams(fmt.Sprintf("Invalid params: 自动化名称不能超过 %d 个字符", automationExtMaxNameRunes))
	}
	if out.Prompt == "" {
		return out, ExtInvalidParams("Invalid params: 提示词（input.prompt）不能为空")
	}
	if utf8.RuneCountInString(out.Prompt) > automationExtMaxPromptRunes {
		return out, ExtInvalidParams(fmt.Sprintf("Invalid params: 提示词不能超过 %d 个字符", automationExtMaxPromptRunes))
	}
	if utf8.RuneCountInString(out.StopWhen) > automationExtMaxStopWhenRunes {
		return out, ExtInvalidParams(fmt.Sprintf("Invalid params: 停止条件不能超过 %d 个字符", automationExtMaxStopWhenRunes))
	}

	if out.MaxConsecutiveFailures == nil {
		def := automationExtDefaultConsecutiveFailures
		out.MaxConsecutiveFailures = &def
	} else if v := *out.MaxConsecutiveFailures; v < 1 || v > automationExtMaxConsecutiveFailures {
		return out, ExtInvalidParams(fmt.Sprintf("Invalid params: 'maxConsecutiveFailures' 必须在 1..%d 之间", automationExtMaxConsecutiveFailures))
	}
	// maxRuns 是规范字段，maxIterations 仅为旧客户端保留：
	// 只传旧字段时回填新字段，只传新字段时镜像旧字段，两者都传时必须一致。
	switch {
	case out.MaxRuns == nil && out.MaxIterations == nil:
		zero := 0
		out.MaxRuns = &zero
		out.MaxIterations = &zero
	case out.MaxRuns == nil:
		out.MaxRuns = out.MaxIterations
	case out.MaxIterations == nil:
		out.MaxIterations = out.MaxRuns
	default:
		if *out.MaxRuns != *out.MaxIterations {
			return out, ExtInvalidParams("Invalid params: 'maxRuns' 与 'maxIterations' 必须一致")
		}
	}
	if v := *out.MaxRuns; v < 0 || v > automationExtMaxRuns {
		return out, ExtInvalidParams(fmt.Sprintf("Invalid params: 'maxRuns' 必须在 0..%d 之间", automationExtMaxRuns))
	}
	if v := *out.MaxIterations; v < 0 || v > automationExtMaxIterations {
		return out, ExtInvalidParams(fmt.Sprintf("Invalid params: 'maxIterations' 必须在 0..%d 之间", automationExtMaxIterations))
	}
	// 补跑开关缺省开启，与 automation.Spec.CatchUpEnabled 的 nil 语义一致。
	if out.CatchUp == nil {
		enabled := true
		out.CatchUp = &enabled
	}

	if err := automationExtValidateSchedule(out.Schedule); err != nil {
		return out, err
	}
	return out, nil
}

// automationExtValidateSchedule 按桌面端表单的规则校验调度：类型、间隔、时刻、星期、cron 字段数、单次时间。
func automationExtValidateSchedule(sch wire.AutomationExtSchedule) error {
	// 桌面端没选类型时按 interval 处理，这里保持一致。
	if sch.Type == "" {
		sch.Type = "interval"
	}
	switch sch.Type {
	case "interval":
		if sch.Seconds <= 0 {
			return ExtInvalidParams("Invalid params: 运行间隔（schedule.seconds）必须大于 0")
		}
	case "daily", "weekdays", "weekly":
		if !automationExtValidClock(sch.TimeOfDay) {
			return ExtInvalidParams("Invalid params: 执行时刻（schedule.timeOfDay）必须为 HH:MM")
		}
		if sch.Type == "weekly" && (sch.DayOfWeek < 0 || sch.DayOfWeek > 6) {
			return ExtInvalidParams("Invalid params: 星期（schedule.dayOfWeek）必须在 0..6 之间")
		}
	case "cron":
		if len(strings.Fields(sch.Expression)) != 5 {
			return ExtInvalidParams("Invalid params: cron 表达式（schedule.expression）必须包含五个字段")
		}
	case "once":
		if _, ok := ParseAutomationExtScheduleDate(sch.Date); !ok {
			return ExtInvalidParams("Invalid params: 单次执行时间（schedule.date）必须为 RFC3339 或 2006-01-02 15:04")
		}
	default:
		return ExtInvalidParams("Invalid params: 不支持的调度类型（schedule.type）")
	}
	if sch.Timezone != "" {
		if _, err := time.LoadLocation(sch.Timezone); err != nil {
			return ExtInvalidParams("Invalid params: 时区（schedule.timezone）不合法")
		}
	}
	return nil
}

// automationExtValidClock 判断执行时刻是否为两位时、两位分的 HH:MM。
// time.Parse("15:04") 本身接受 "9:00" 这种单位数小时，协议里写的是 HH:MM，
// 所以再用格式化回写比对一次，把不规范的写法挡在入口。
func automationExtValidClock(raw string) bool {
	parsed, err := time.Parse("15:04", raw)
	if err != nil {
		return false
	}
	return parsed.Format("15:04") == raw
}

// ParseAutomationExtScheduleDate 解析单次调度的时间文本，接受 RFC3339 与桌面端表单的本地时间格式。
// app 层转换入参时复用同一份解析规则，避免两边格式判断漂移。
func ParseAutomationExtScheduleDate(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, true
	}
	t, err := time.ParseInLocation("2006-01-02 15:04", raw, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// automationExtServiceOf 取出 app 层注入的自动化服务；未接线或类型不符时返回 false。
func automationExtServiceOf(d *Dispatcher) (automationExtService, bool) {
	if d == nil {
		return nil, false
	}
	return backendService[automationExtService](d.backend, automationExtServiceName)
}

// automationExtRunsLimit 归一化运行记录的返回条数：省略或非正数取默认值，超过上限按上限。
func automationExtRunsLimit(limit *int) int {
	if limit == nil || *limit <= 0 {
		return automationExtRunsDefaultLimit
	}
	if *limit > automationExtRunsMaxLimit {
		return automationExtRunsMaxLimit
	}
	return *limit
}

// automationExtNotifyChanged 广播自动化或运行记录变更。
// automationID 与 runID 至少给一个：自动化级操作给前者，已读/归档这类运行级操作只能给后者。
func automationExtNotifyChanged(d *Dispatcher, automationID, runID string) {
	if d == nil {
		return
	}
	automationID = strings.TrimSpace(automationID)
	runID = strings.TrimSpace(runID)
	if automationID == "" && runID == "" {
		return
	}
	d.NotifyAuthed(automationExtChangedMethod, wire.AutomationExtChanged{AutomationID: automationID, RunID: runID})
}

// automationExtWrapError 把业务错误转成可读的拒绝错误。
// 与桌面端一致：错误原文直接回给客户端（冲突错误已由 Client 的 wrapConflictErr 包装），
// 不再被统一按内部错误脱敏成一条笼统提示。
func automationExtWrapError(err error) error {
	if err == nil {
		return nil
	}
	var extErr *ExtError
	if errors.As(err, &extErr) {
		return extErr
	}
	// 版本冲突单独给错误码，客户端可以据此提示「刷新后重试」而不是笼统报错。
	if errors.Is(err, ErrAutomationExtConflict) {
		return &ExtError{Code: wire.ErrCodeVersionConflict, Message: "自动化已被修改（版本冲突），请刷新后重试"}
	}
	return &ExtError{Code: wire.ErrCodeOperationRejected, Message: err.Error()}
}
