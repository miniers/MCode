package app

// 本文件把桌面端的自动化服务接到远程扩展方法上：
// automations.list / automations.runs / automations.run / automations.markRead /
// automations.archiveRun / automations.create / automations.update / automations.delete /
// automations.setEnabled / automations.reset 由 internal/remote/automation_ext_methods.go 负责，
// 它只依赖这里注入的 "automation" 服务。
//
// 写操作与桌面端同源：
//   - create 用 automationExtInputToUI 转成 mcodeui.AutomationInput 后调 Client.CreateAutomation；
//   - update 先按 ID 取当前自动化拿 ProjectID，再调 Client.UpdateAutomation（可带 revision）；
//   - delete 直调 Client.DeleteAutomation；
//   - setEnabled/reset 取当前自动化后调 Client.SetAutomationEnabled，Enabled=true 时后端会同时
//     清空连续失败计数，与桌面端「恢复/重置」完全一致。
//
// 错误保持桌面端的处理方式：Client 的方法已经用 wrapConflictErr 包装过
// store.ErrRevisionConflict，这里再统一转成 remote.ErrAutomationExtConflict，
// 远程层据此回版本冲突错误码；其余错误保留原文回给客户端。

import (
	"context"
	"errors"
	"fmt"
	"time"

	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	mcodeui "mcode/internal/ui"
)

// remoteAutomationService 是注入给远程扩展方法的自动化服务，实现 remote.automationExtService。
type remoteAutomationService struct {
	client *Client
}

func (s remoteAutomationService) Preview(ctx context.Context, schedule wire.AutomationExtSchedule, after time.Time, count int) (wire.AutomationExtPreviewResult, error) {
	if s.client == nil {
		return wire.AutomationExtPreviewResult{}, errors.New(automationServiceUnavailable)
	}
	sch := automationExtInputToUI("", wire.AutomationExtInput{Schedule: schedule}).Schedule
	preview, err := s.client.PreviewAutomationSchedule(ctx, sch, after, count)
	if err != nil {
		return wire.AutomationExtPreviewResult{}, err
	}
	result := wire.AutomationExtPreviewResult{Times: make([]string, 0, len(preview.Times)), Exhausted: preview.Exhausted}
	for _, t := range preview.Times {
		result.Times = append(result.Times, t.UTC().Format(time.RFC3339Nano))
	}
	return result, nil
}

// Automations 返回项目下的自动化列表与未读数；projectID 为空表示全部项目。
func (s remoteAutomationService) Automations(ctx context.Context, projectID string) ([]wire.AutomationExtAutomation, map[string]int, error) {
	if s.client == nil {
		return nil, nil, errors.New("自动化服务不可用")
	}
	autos, err := s.client.Automations(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	counts, err := s.client.AutomationUnreadCounts(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	res := make([]wire.AutomationExtAutomation, 0, len(autos))
	for _, a := range autos {
		res = append(res, automationExtAutomation(a))
	}
	return res, counts, nil
}

// Runs 返回运行记录及其已读/归档状态；includeArchived 为 true 时一并返回已归档记录。
func (s remoteAutomationService) Runs(ctx context.Context, automationID string, limit int, includeArchived bool) ([]wire.AutomationExtRun, error) {
	if s.client == nil {
		return nil, errors.New("自动化服务不可用")
	}
	state, err := s.client.AutomationRunsState(ctx, automationID, limit, includeArchived)
	if err != nil {
		return nil, err
	}
	// Runs 与 States 来自同一次查询、顺序一致，但按 RunID 建索引更稳。
	meta := make(map[string]mcodeui.AutomationRunReadState, len(state.States))
	for _, item := range state.States {
		meta[item.RunID] = item
	}
	res := make([]wire.AutomationExtRun, 0, len(state.Runs))
	for _, run := range state.Runs {
		item := automationExtRun(run)
		if info, ok := meta[run.ID]; ok {
			item.Read = !info.ReadAt.IsZero()
			item.Archived = info.Archived
		}
		res = append(res, item)
	}
	return res, nil
}

// RunNow 立即触发一次运行并返回新建的运行记录。
func (s remoteAutomationService) RunNow(ctx context.Context, automationID string) (wire.AutomationExtRun, error) {
	if s.client == nil {
		return wire.AutomationExtRun{}, errors.New("自动化服务不可用")
	}
	run, err := s.client.RunAutomation(ctx, automationID)
	if err != nil {
		return wire.AutomationExtRun{}, err
	}
	return automationExtRun(run), nil
}

// MarkRunRead 把一条运行记录标为已读。
func (s remoteAutomationService) MarkRunRead(ctx context.Context, runID string) error {
	if s.client == nil {
		return errors.New("自动化服务不可用")
	}
	return s.client.MarkAutomationRunRead(ctx, runID)
}

// ArchiveRun 归档或取消归档一条运行记录。
func (s remoteAutomationService) ArchiveRun(ctx context.Context, runID string, archived bool) error {
	if s.client == nil {
		return errors.New("自动化服务不可用")
	}
	return s.client.SetAutomationRunArchived(ctx, runID, archived)
}

// automationServiceUnavailable 是服务未接线时的统一文案，仅用于本文件内。
const automationServiceUnavailable = "自动化服务不可用"

// Create 新建自动化；写入后返回带 ID/Revision 的完整记录。
func (s remoteAutomationService) Create(ctx context.Context, projectID string, in wire.AutomationExtInput) (wire.AutomationExtAutomation, error) {
	if s.client == nil {
		return wire.AutomationExtAutomation{}, errors.New(automationServiceUnavailable)
	}
	created, err := s.client.CreateAutomation(ctx, automationExtInputToUI(projectID, in))
	if err != nil {
		return wire.AutomationExtAutomation{}, remoteAutomationExtError(err)
	}
	return automationExtAutomation(created), nil
}

// Update 修改自动化；revision<=0 时取当前版本号，即不校验并发（远程表单没带版本时）。
func (s remoteAutomationService) Update(ctx context.Context, automationID string, in wire.AutomationExtInput, revision int) (wire.AutomationExtAutomation, error) {
	if s.client == nil {
		return wire.AutomationExtAutomation{}, errors.New(automationServiceUnavailable)
	}
	current, err := s.automationByID(ctx, automationID)
	if err != nil {
		return wire.AutomationExtAutomation{}, err
	}
	if revision <= 0 {
		revision = current.Revision
	}
	input := automationExtInputToUI(current.ProjectID, in)
	// update 不允许改项目，始终沿用当前值。
	input.ProjectID = current.ProjectID
	// 模型与权限：缺省（nil）表示不改，沿用当前值；空串表示恢复默认。
	input.Provider = automationExtPickString(in.Provider, current.Provider)
	input.Model = automationExtPickString(in.Model, current.Model)
	input.Effort = automationExtPickString(in.Effort, current.Effort)
	input.RuntimeMode = automationExtPickString(in.RuntimeMode, current.RuntimeMode)
	updated, err := s.client.UpdateAutomation(ctx, automationID, input, revision)
	if err != nil {
		return wire.AutomationExtAutomation{}, remoteAutomationExtError(err)
	}
	return automationExtAutomation(updated), nil
}

// Delete 删除自动化。
func (s remoteAutomationService) Delete(ctx context.Context, automationID string) error {
	if s.client == nil {
		return errors.New(automationServiceUnavailable)
	}
	if err := s.client.DeleteAutomation(ctx, automationID); err != nil {
		return remoteAutomationExtError(err)
	}
	return nil
}

// SetEnabled 暂停或恢复自动化；enabled=true 时后端同时清空连续失败计数，即桌面端的「重置」。
func (s remoteAutomationService) SetEnabled(ctx context.Context, automationID string, enabled bool, revision int) (wire.AutomationExtAutomation, error) {
	if s.client == nil {
		return wire.AutomationExtAutomation{}, errors.New(automationServiceUnavailable)
	}
	current, err := s.automationByID(ctx, automationID)
	if err != nil {
		return wire.AutomationExtAutomation{}, err
	}
	if revision <= 0 {
		revision = current.Revision
	}
	updated, err := s.client.SetAutomationEnabled(ctx, automationID, enabled, revision)
	if err != nil {
		return wire.AutomationExtAutomation{}, remoteAutomationExtError(err)
	}
	return automationExtAutomation(updated), nil
}

// automationByID 按 ID 取自动化：Automations 传空项目表示列出全部项目，与桌面端一致。
// 找不到时返回可读错误（可能已被其它客户端删除）。
func (s remoteAutomationService) automationByID(ctx context.Context, automationID string) (mcodeui.Automation, error) {
	autos, err := s.client.Automations(ctx, "")
	if err != nil {
		return mcodeui.Automation{}, remoteAutomationExtError(err)
	}
	for _, a := range autos {
		if a.ID == automationID {
			return a, nil
		}
	}
	return mcodeui.Automation{}, fmt.Errorf("自动化不存在或已被删除：%s", automationID)
}

// remoteAutomationExtError 把客户端的版本冲突转成远程层哨兵，其余错误原样上抛。
func remoteAutomationExtError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, mcodeui.ErrRevisionConflict) {
		return fmt.Errorf("%w: %v", remote.ErrAutomationExtConflict, err)
	}
	return err
}

// automationExtInputToUI 把协议层入参转成桌面端 AutomationInput。
// 调度时间接受 RFC3339 与 "2006-01-02 15:04"，与 remote.ParseAutomationExtScheduleDate 同一规则。
// maxConsecutiveFailures/maxIterations 为 nil 时取 0，远程层 normalize 已补过默认值。
func automationExtInputToUI(projectID string, in wire.AutomationExtInput) mcodeui.AutomationInput {
	out := mcodeui.AutomationInput{
		ProjectID: projectID,
		Name:      in.Name,
		Prompt:    in.Prompt,
		StopWhen:  in.StopWhen,
		Schedule: mcodeui.AutomationSchedule{
			Type:       in.Schedule.Type,
			Seconds:    in.Schedule.Seconds,
			TimeOfDay:  in.Schedule.TimeOfDay,
			Timezone:   in.Schedule.Timezone,
			DayOfWeek:  in.Schedule.DayOfWeek,
			Expression: in.Schedule.Expression,
		},
		Enabled: in.Enabled,
	}
	if in.MaxConsecutiveFailures != nil {
		out.MaxConsecutiveFailures = *in.MaxConsecutiveFailures
	}
	if in.MaxIterations != nil {
		out.MaxIterations = *in.MaxIterations
	}
	if in.MaxRuns != nil {
		out.MaxRuns = *in.MaxRuns
	} else if in.MaxIterations != nil {
		// 旧客户端只传 maxIterations 时沿用旧值，避免把上限清成 0。
		out.MaxRuns = *in.MaxIterations
	}
	if in.CatchUp != nil {
		v := *in.CatchUp
		out.CatchUp = &v
	}
	// 模型与权限：nil 表示不改，空串表示恢复默认；update 时调用方会再与当前值合并。
	if in.Provider != nil {
		out.Provider = *in.Provider
	}
	if in.Model != nil {
		out.Model = *in.Model
	}
	if in.Effort != nil {
		out.Effort = *in.Effort
	}
	if in.RuntimeMode != nil {
		out.RuntimeMode = *in.RuntimeMode
	}
	if when, ok := remote.ParseAutomationExtScheduleDate(in.Schedule.Date); ok {
		out.Schedule.Date = when
	}
	return out
}

// automationExtInputFromUI 按桌面端 automationInputFrom（internal/ui/automation.go）从现有记录生成更新入参，
// 只把 Enabled 换成调用方要求的值，其余字段原样保留。
func automationExtInputFromUI(a mcodeui.Automation, enabled bool) mcodeui.AutomationInput {
	return mcodeui.AutomationInput{
		ProjectID:              a.ProjectID,
		Name:                   a.Name,
		Prompt:                 a.Prompt,
		StopWhen:               a.StopWhen,
		Schedule:               a.Schedule,
		Enabled:                enabled,
		MaxConsecutiveFailures: a.MaxConsecutiveFailures,
		MaxIterations:          a.MaxIterations,
		MaxRuns:                a.MaxRuns,
		CatchUp:                &a.CatchUp,
		Provider:               a.Provider,
		Model:                  a.Model,
		Effort:                 a.Effort,
		RuntimeMode:            a.RuntimeMode,
	}
}

// automationExtAutomation 把桌面端 Automation 转成协议层列表项。
func automationExtAutomation(a mcodeui.Automation) wire.AutomationExtAutomation {
	return wire.AutomationExtAutomation{
		ID:                     a.ID,
		ProjectID:              a.ProjectID,
		Name:                   a.Name,
		Prompt:                 a.Prompt,
		StopWhen:               a.StopWhen,
		Schedule:               automationExtSchedule(a.Schedule),
		ScheduleText:           automationExtScheduleText(a.Schedule),
		Enabled:                a.Enabled,
		Status:                 a.Status,
		LastRunAt:              automationExtTime(a.LastRunAt),
		LastRunStatus:          a.LastRunStatus,
		NextRunAt:              automationExtTime(a.NextRunAt),
		ConsecutiveFailures:    a.ConsecutiveFailures,
		MaxConsecutiveFailures: a.MaxConsecutiveFailures,
		MaxIterations:          a.MaxIterations,
		MaxRuns:                a.MaxRuns,
		CatchUp:                a.CatchUp,
		Provider:               a.Provider,
		Model:                  a.Model,
		Effort:                 a.Effort,
		RuntimeMode:            a.RuntimeMode,
		RunMode:                a.RunMode,
		Revision:               a.Revision,
	}
}

// automationExtPickString 按「nil 沿用当前值、空串清空」的规则合并可选字符串字段。
func automationExtPickString(in *string, current string) string {
	if in == nil {
		return current
	}
	return *in
}

// automationExtSchedule 把桌面端调度规则转成协议层结构。
func automationExtSchedule(sch mcodeui.AutomationSchedule) wire.AutomationExtSchedule {
	return wire.AutomationExtSchedule{
		Type:       sch.Type,
		Seconds:    sch.Seconds,
		Date:       automationExtTime(sch.Date),
		TimeOfDay:  sch.TimeOfDay,
		Timezone:   sch.Timezone,
		DayOfWeek:  sch.DayOfWeek,
		Expression: sch.Expression,
	}
}

// automationExtRun 把桌面端运行记录转成协议层结构；已读/归档由调用方按状态补齐。
func automationExtRun(run mcodeui.AutomationRun) wire.AutomationExtRun {
	return wire.AutomationExtRun{
		ID:           run.ID,
		AutomationID: run.AutomationID,
		Status:       run.Status,
		Trigger:      run.Trigger,
		MissedCount:  run.MissedCount,
		StartedAt:    automationExtTime(run.StartedAt),
		FinishedAt:   automationExtTime(run.FinishedAt),
		ThreadID:     run.ThreadID,
		Summary:      run.Summary,
		Error:        run.Error,
	}
}

// automationExtTime 把时间戳统一成 RFC3339，零值输出空串（对应 omitempty）。
func automationExtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// automationExtWeekdayLabels 是星期文案，索引即星期值（0 为周日）。
var automationExtWeekdayLabels = []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}

// automationExtScheduleText 生成调度描述，如「每 60 分钟」「每天 09:00」「每周一 09:00」。
// 桌面端同源逻辑见 internal/ui/automation.go 的 automationScheduleText（未导出，无法复用），
// 两侧文案需保持一致。
func automationExtScheduleText(sch mcodeui.AutomationSchedule) string {
	clock := sch.TimeOfDay
	if clock == "" {
		clock = "09:00"
	}
	weeklyDay := "每周"
	if sch.DayOfWeek >= 0 && sch.DayOfWeek < len(automationExtWeekdayLabels) {
		weeklyDay = automationExtWeekdayLabels[sch.DayOfWeek]
	}
	switch sch.Type {
	case "interval":
		seconds := sch.Seconds
		if seconds <= 0 {
			seconds = 3600
		}
		if seconds%60 != 0 {
			return fmt.Sprintf("每 %d 秒", seconds)
		}
		minutes := seconds / 60
		if minutes >= 60 && minutes%60 == 0 {
			return fmt.Sprintf("每 %d 小时", minutes/60)
		}
		return fmt.Sprintf("每 %d 分钟", minutes)
	case "daily":
		return "每天 " + clock
	case "weekdays":
		return "工作日 " + clock
	case "weekly":
		return weeklyDay + " " + clock
	case "cron":
		if sch.Expression == "" {
			return "Cron 调度"
		}
		return "Cron " + sch.Expression
	case "once":
		if sch.Date.IsZero() {
			return "单次运行"
		}
		return "单次 " + sch.Date.Local().Format("2006-01-02 15:04")
	case "":
		return "未设置调度"
	}
	return sch.Type
}

func init() {
	registerRemoteWiring(func(a *App) {
		if a == nil || a.backend == nil {
			return
		}
		a.backend.SetService("automation", remoteAutomationService{client: a.remoteClient()})
	})
}
