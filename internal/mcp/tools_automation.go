package mcp

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"mcode/internal/automation"
	"mcode/internal/harness"
	"mcode/internal/store"
)

// automationRunView 把运行记录转成工具返回结构，附带只读的已读与归档状态。
// read 表示该运行记录已被用户读过（read_at 非空），archived 表示已被归档。
func automationRunView(r store.AutomationRun) map[string]any {
	return map[string]any{
		"id":             r.ID,
		"automation_id":  r.AutomationID,
		"thread_id":      r.ThreadID,
		"status":         r.Status,
		"started_at":     r.StartedAt,
		"finished_at":    r.FinishedAt,
		"result_summary": r.ResultSummary,
		"error":          r.Error,
		"changed_files":  r.ChangedFiles,
		"read":           r.ReadAt != nil,
		"archived":       r.Archived,
		"trigger":        r.Trigger,
		"missed_count":   r.MissedCount,
	}
}

// automationScopeThread 返回调用方线程作用域 ID；调用方没有线程作用域时返回空串。
func automationScopeThread(ctx context.Context) string {
	threadID, _ := ctx.Value(threadScopeKey{}).(string)
	return strings.TrimSpace(threadID)
}

// automationCallerIsReviewThread 判断调用方线程是否为某个看板任务的评审线程。
func automationCallerIsReviewThread(tc ToolContext, threadID string) bool {
	st := tc.Server.opts.Store
	if st == nil || threadID == "" {
		return false
	}
	tasks, err := st.ListTasks(tc.Context, tc.Project.ID)
	if err != nil {
		return false
	}
	for _, task := range tasks {
		if task.ReviewThreadID != nil && *task.ReviewThreadID == threadID {
			return true
		}
	}
	return false
}

// automationWriteGuard 拒绝由评审线程发起的自动化写操作：评审会话只读，
// 不得创建、修改或取消自动化（结算自己运行的 report_result 不受此限）。
func automationWriteGuard(tc ToolContext) *ToolResult {
	if automationCallerIsReviewThread(tc, automationScopeThread(tc.Context)) {
		res := toolError(
			CodeForbidden,
			"Automations cannot be created, changed or cancelled from a review thread.",
			false,
			"Ask for this from the task's own thread or from the lead conversation.",
		)
		return &res
	}
	return nil
}

// automationCreateProject 返回这次创建自动化落在哪个项目。
// 普通会话只能用当前项目（server 层已拦下其他 project_id）；总管会话可以用 project_id 指向别的项目，
// 所以这里按项目 ID 在项目清单里查一次，查不到就明确报错，不默默落到当前项目。
func automationCreateProject(tc ToolContext, rawProjectID string) (string, *ToolResult) {
	projectID := strings.TrimSpace(rawProjectID)
	if projectID == "" || projectID == tc.Project.ID {
		return tc.Project.ID, nil
	}
	st := tc.Server.opts.Store
	if st == nil {
		res := toolError(CodeInternal, "Project store is unavailable", false)
		return "", &res
	}
	projects, err := st.ListProjects(tc.Context)
	if err != nil {
		res := toolError(CodeInternal, "Failed to list projects: "+err.Error(), true)
		return "", &res
	}
	for _, p := range projects {
		if p.ID == projectID {
			return projectID, nil
		}
	}
	res := toolError(CodeInvalidArguments, fmt.Sprintf("Unknown project_id '%s'.", projectID), false,
		"List the projects first, then pass one project ID from that list.")
	return "", &res
}

func (r *Registry) registerAutomationTools(s *Server) {
	r.Register(ToolDefinition{
		Name: "automation_create",
		Description: "在本项目里建一个按固定节奏运行的自动化：每隔 N 秒、每天、工作日、每周，或按 cron 表达式。" +
			"一次性的事不建自动化；被事件唤醒的那一轮也不要创建。" +
			"每个会话最多保留 50 条启用中的定时任务。" +
			"总管会话可以用 project_id 指定在其他项目里创建。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "显示名称。",
				},
				"prompt": map[string]any{
					"type":        "string",
					"description": "每次运行时 agent 要做什么。",
				},
				"schedule_type": map[string]any{
					"type":        "string",
					"enum":        []string{"interval", "daily", "weekdays", "weekly", "cron"},
					"description": "频率类型。",
				},
				"schedule_value": map[string]any{
					"type":        "string",
					"description": "运行时间，按 schedule_type 取：interval 写间隔秒数（'3600'），daily 或 weekdays 写 HH:mm（'09:00'），weekly 写 dayOfWeek,HH:mm（'1,09:00'，1 = 周一），cron 写 cron 表达式。",
				},
				"stop_when": map[string]any{
					"type":        "string",
					"description": "可选的自然语言停止条件；一旦满足，这次自动化就结束，不再运行。",
				},
				"check": map[string]any{
					"type":        "string",
					"description": "可选。定时触发前执行的只读检查命令，退出码 0 才运行，非 0 跳过本轮",
				},
				"max_consecutive_failures": map[string]any{
					"type":        "integer",
					"description": "连续失败多少次后自动暂停（默认 3）。",
				},
				"max_runs": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"maximum":     automationMaxRunsLimit,
					"description": "总运行次数上限（0 到 1000，0 = 不限制，默认 0）；达到后自动停用。",
				},
				"catch_up": map[string]any{
					"type":        "boolean",
					"description": "错过触发点时是否补跑一次（默认 true）。",
				},
				"run_mode": map[string]any{
					"type":        "string",
					"enum":        []string{"standalone", "dedicated", "heartbeat"},
					"description": "运行方式：standalone（默认）每次新建任务，dedicated 固定复用同一条任务，heartbeat 把每次运行发进创建它的会话。",
				},
				"provider": map[string]any{
					"type":        "string",
					"description": "可选。每次运行新建会话使用的供应商 ID（如 anthropic）；不填则跟随默认。heartbeat 模式投递到已有会话，该设置不生效。",
				},
				"model": map[string]any{
					"type":        "string",
					"description": "可选。每次运行新建会话使用的模型 ID；不填则跟随默认。heartbeat 模式投递到已有会话，该设置不生效。",
				},
				"effort": map[string]any{
					"type":        "string",
					"description": "可选。推理强度（如 low、medium、high）；不填则跟随默认。heartbeat 模式投递到已有会话，该设置不生效。",
				},
				"runtime_mode": map[string]any{
					"type":        "string",
					"enum":        []string{"readonly", "supervised", "auto", "fullAccess"},
					"description": "可选。每次运行新建会话的权限模式：readonly 只读、supervised 需审批、auto 自动、fullAccess 完全访问；不填则跟随默认。heartbeat 模式投递到已有会话，该设置不生效。",
				},
				"project_id": map[string]any{
					"type": "string",
					"description": "可选项目 UUID。普通会话只能填当前会话的项目；" +
						"总管会话可以填其他项目，用来在那个项目里创建自动化。",
				},
				"request_id": map[string]any{
					"type":        "string",
					"description": "可选幂等键（也接受 requestId）。同一个键、同一组参数重复调用会返回原结果，不会重复创建。",
				},
			},
			"required":             []string{"name", "prompt", "schedule_type", "schedule_value"},
			"additionalProperties": false,
		},
		IsWrite:    true,
		Capability: CapAutomation,
		Handler:    handleAutomationCreate,
	})

	r.Register(ToolDefinition{
		Name:        "automation_list",
		Description: "Show this project's automations together with their latest runs.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"project_id": map[string]any{
					"type":        "string",
					"description": "Optional project UUID.",
				},
			},
		},
		IsWrite:    false,
		Capability: CapAutomation,
		Handler:    handleAutomationList,
	})

	r.Register(ToolDefinition{
		Name:        "automation_update",
		Description: "Change an automation: turn it on or off, replace its prompt or stop condition, or change the model and permission mode used by each new run.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"automation_id": map[string]any{
					"type":        "string",
					"description": "UUID of the automation to change.",
				},
				"is_enabled": map[string]any{
					"type":        "boolean",
					"description": "true to run the automation, false to pause it.",
				},
				"prompt": map[string]any{
					"type":        "string",
					"description": "Updated objective prompt.",
				},
				"stop_when": map[string]any{
					"type":        "string",
					"description": "New plain-language stop condition.",
				},
				"check": map[string]any{
					"type":        "string",
					"description": "可选。定时触发前执行的只读检查命令，退出码 0 才运行，非 0 跳过本轮",
				},
				"max_runs": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"maximum":     automationMaxRunsLimit,
					"description": "可选。总运行次数上限（0 到 1000，0 = 不限制）；不传表示不改。",
				},
				"catch_up": map[string]any{
					"type":        "boolean",
					"description": "可选。错过触发点时是否补跑一次；不传表示不改。",
				},
				"provider": map[string]any{
					"type":        "string",
					"description": "可选。每次运行新建会话使用的供应商 ID；不传表示不改，传空串表示恢复默认。heartbeat 模式不生效。",
				},
				"model": map[string]any{
					"type":        "string",
					"description": "可选。每次运行新建会话使用的模型 ID；不传表示不改，传空串表示恢复默认。heartbeat 模式不生效。",
				},
				"effort": map[string]any{
					"type":        "string",
					"description": "可选。推理强度（如 low、medium、high）；不传表示不改，传空串表示恢复默认。heartbeat 模式不生效。",
				},
				"runtime_mode": map[string]any{
					"type":        "string",
					"enum":        []string{"readonly", "supervised", "auto", "fullAccess"},
					"description": "可选。每次运行新建会话的权限模式；不传表示不改，传空串表示跟随默认。heartbeat 模式不生效。",
				},
			},
			"required":             []string{"automation_id"},
			"additionalProperties": false,
		},
		IsWrite:    true,
		Capability: CapAutomation,
		Handler:    handleAutomationUpdate,
	})

	r.Register(ToolDefinition{
		Name:        "automation_cancel",
		Description: "Stop an automation and delete it. Calling this for an automation that is already gone still succeeds and says so.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"automation_id": map[string]any{
					"type":        "string",
					"description": "UUID of the automation to remove.",
				},
			},
			"required":             []string{"automation_id"},
			"additionalProperties": false,
		},
		IsWrite:    true,
		Capability: CapAutomation,
		Handler:    handleAutomationCancel,
	})

	r.Register(ToolDefinition{
		Name:        "automation_report_result",
		Description: "Record how one automation run ended. The run_id and automation_id are given in this run's prompt; call it once before the run ends.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"run_id": map[string]any{
					"type":        "string",
					"description": "UUID of the run to report on, as given in this run's prompt.",
				},
				"automation_id": map[string]any{
					"type":        "string",
					"description": "UUID of the automation.",
				},
				"status": map[string]any{
					"type":        "string",
					"enum":        []string{"succeeded", "failed"},
					"description": "Outcome status.",
				},
				"summary": map[string]any{
					"type":        "string",
					"description": "What the run found or changed.",
				},
				"stop_condition_met": map[string]any{
					"type":        "boolean",
					"description": "Set to true if you think the automation's stop condition now holds.",
				},
				"stop_condition_reason": map[string]any{
					"type":        "string",
					"description": "Why the stop condition holds, with the supporting evidence.",
				},
			},
			"required":             []string{"run_id", "automation_id", "status", "summary"},
			"additionalProperties": false,
		},
		IsWrite:    true,
		Capability: CapAutomation,
		Handler:    handleAutomationReportResult,
	})

	// 入口工具放在最后注册：此时全部类型化自动化工具已在列表中，enum 可直接从它们收集。
	automationFamily.register(r)
}

func parseAutomationSchedule(schedType, schedVal string) (automation.Schedule, error) {
	switch schedType {
	case "interval":
		sec, err := strconv.Atoi(schedVal)
		if err != nil || sec <= 0 {
			return automation.Schedule{}, errors.New("interval must be positive integer seconds")
		}
		return automation.Schedule{Type: "interval", Seconds: sec}, nil
	case "daily":
		return automation.Schedule{Type: "daily", TimeOfDay: schedVal}, nil
	case "weekdays":
		return automation.Schedule{Type: "weekdays", TimeOfDay: schedVal}, nil
	case "weekly":
		parts := strings.Split(schedVal, ",")
		if len(parts) != 2 {
			return automation.Schedule{}, errors.New("weekly format: dayOfWeek,HH:mm")
		}
		day, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil || day < 0 || day > 6 {
			return automation.Schedule{}, errors.New("dayOfWeek must be 0-6")
		}
		return automation.Schedule{Type: "weekly", DayOfWeek: day, TimeOfDay: strings.TrimSpace(parts[1])}, nil
	case "cron":
		return automation.Schedule{Type: "cron", Expression: schedVal}, nil
	default:
		return automation.Schedule{}, fmt.Errorf("unknown schedule type: %s", schedType)
	}
}

// automationMaxRunsLimit 是 max_runs 的统一上限（下限为 0，0 表示不限制）。
const automationMaxRunsLimit = 1000

// automationRuntimeModes 是允许写入 Spec.RuntimeMode 的取值，空串表示跟随默认。
var automationRuntimeModes = []string{"readonly", "supervised", "auto", "fullAccess"}

// applyAutomationRunSettings 解析 provider、model、effort、runtime_mode 四个可选参数并写入 spec。
// spec 为 nil 时只做校验。返回值 present 表示请求里出现了这四个参数中的至少一个，
// 调用方据此决定是否整份替换 Spec（这样传空串就能把对应项恢复默认）。
func applyAutomationRunSettings(args map[string]any, spec *automation.Spec) (bool, *ToolResult) {
	present := false
	for _, key := range []string{"provider", "model", "effort", "runtime_mode"} {
		raw, ok := args[key]
		if !ok {
			continue
		}
		val, isString := raw.(string)
		if !isString {
			res := toolError(CodeInvalidArguments, fmt.Sprintf("'%s' must be a string.", key), false)
			return false, &res
		}
		val = strings.TrimSpace(val)
		if key == "runtime_mode" && val != "" {
			val = harness.NormalizeMode(val)
			valid := false
			for _, mode := range automationRuntimeModes {
				if val == mode {
					valid = true
					break
				}
			}
			if !valid {
				res := toolError(
					CodeInvalidArguments,
					fmt.Sprintf("Unknown runtime_mode '%s'.", val),
					false,
					"runtime_mode is 'readonly', 'supervised', 'auto', 'fullAccess' or an empty string for the default.",
				)
				return false, &res
			}
		}
		if spec != nil {
			switch key {
			case "provider":
				spec.Provider = val
			case "model":
				spec.Model = val
			case "effort":
				spec.Effort = val
			case "runtime_mode":
				spec.RuntimeMode = val
			}
		}
		present = true
	}
	return present, nil
}

// applyAutomationMaxRuns 解析 max_runs 参数并写入 spec：取值 0..1000，0 表示不限制。
// 返回值 present 表示请求里出现了该参数。同时镜像旧字段 MaxIterations，
// 传 0 时一并清零，否则旧上限仍会通过 EffectiveMaxRuns 生效。
func applyAutomationMaxRuns(args map[string]any, spec *automation.Spec) (bool, *ToolResult) {
	raw, ok := args["max_runs"]
	if !ok {
		return false, nil
	}
	var value int
	switch n := raw.(type) {
	case float64:
		if math.Trunc(n) != n || n < 0 || n > automationMaxRunsLimit {
			res := toolError(
				CodeInvalidArguments,
				fmt.Sprintf("'max_runs' must be an integer between 0 and %d.", automationMaxRunsLimit),
				false,
			)
			return true, &res
		}
		value = int(n)
	case int:
		if n < 0 || n > automationMaxRunsLimit {
			res := toolError(
				CodeInvalidArguments,
				fmt.Sprintf("'max_runs' must be an integer between 0 and %d.", automationMaxRunsLimit),
				false,
			)
			return true, &res
		}
		value = n
	default:
		res := toolError(
			CodeInvalidArguments,
			fmt.Sprintf("'max_runs' must be an integer between 0 and %d.", automationMaxRunsLimit),
			false,
		)
		return true, &res
	}
	if spec != nil {
		spec.MaxRuns = value
		spec.MaxIterations = value
	}
	return true, nil
}

// applyAutomationCatchUp 解析 catch_up 参数并写入 spec；缺省时保持原值，nil 等价于开启。
func applyAutomationCatchUp(args map[string]any, spec *automation.Spec) (bool, *ToolResult) {
	raw, ok := args["catch_up"]
	if !ok {
		return false, nil
	}
	value, isBool := raw.(bool)
	if !isBool {
		res := toolError(CodeInvalidArguments, "'catch_up' must be a boolean.", false)
		return true, &res
	}
	if spec != nil {
		spec.CatchUp = &value
	}
	return true, nil
}

func handleAutomationCreate(tc ToolContext, args map[string]any) ToolResult {
	if guard := automationWriteGuard(tc); guard != nil {
		return *guard
	}
	autoSvc := tc.Server.getAutomation()
	if autoSvc == nil {
		return toolError(CodeInternal, "Automation service is unavailable", false)
	}

	rawProjectID, _ := args["project_id"].(string)
	projectID, badProject := automationCreateProject(tc, rawProjectID)
	if badProject != nil {
		return *badProject
	}

	name, _ := args["name"].(string)
	if strings.TrimSpace(name) == "" {
		return toolError(CodeInvalidArguments, "Missing required parameter 'name'.", false)
	}
	name = strings.TrimSpace(name)

	prompt, _ := args["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		return toolError(CodeInvalidArguments, "Missing required parameter 'prompt'.", false)
	}
	prompt = strings.TrimSpace(prompt)

	schedType, _ := args["schedule_type"].(string)
	schedVal, _ := args["schedule_value"].(string)
	if strings.TrimSpace(schedType) == "" || strings.TrimSpace(schedVal) == "" {
		return toolError(CodeInvalidArguments, "Missing required parameters 'schedule_type' and 'schedule_value'.", false)
	}

	sched, err := parseAutomationSchedule(schedType, schedVal)
	if err != nil {
		return toolError(CodeInvalidArguments, "Invalid schedule: "+err.Error(), false)
	}

	// mcode 支持三种运行模式，默认为 standalone。
	runMode := automation.RunModeStandalone
	if rawMode, ok := args["run_mode"]; ok {
		mode, isString := rawMode.(string)
		if !isString {
			return toolError(CodeInvalidArguments, "'run_mode' must be a string.", false)
		}
		mode = strings.TrimSpace(mode)
		switch mode {
		case "", automation.RunModeStandalone:
			runMode = automation.RunModeStandalone
		case automation.RunModeDedicated, automation.RunModeHeartbeat:
			runMode = mode
		default:
			return toolError(
				CodeInvalidArguments,
				fmt.Sprintf("Unknown run_mode '%s'.", strings.TrimSpace(mode)),
				false,
				"run_mode is 'standalone', 'dedicated' or 'heartbeat'.",
			)
		}
	}
	callerThreadID := automationScopeThread(tc.Context)
	if runMode == automation.RunModeHeartbeat && callerThreadID == "" {
		return toolError(
			CodeInvalidArguments,
			"run_mode 'heartbeat' requires a conversation thread scope.",
			false,
			"Heartbeat automations can only be set up from an agent conversation, not from an untrusted client.",
		)
	}

	maxFailures := 3
	if mf, ok := args["max_consecutive_failures"].(float64); ok && mf >= 1 {
		maxFailures = int(mf)
	}

	stopWhen, _ := args["stop_when"].(string)
	check, _ := args["check"].(string)

	// catch_up 缺省开启，显式传 false 才关闭。
	catchUp := true
	spec := automation.Spec{
		Schedule:               sched,
		StopWhen:               strings.TrimSpace(stopWhen),
		Check:                  check,
		MaxConsecutiveFailures: maxFailures,
		CatchUp:                &catchUp,
		RunMode:                runMode,
	}
	// 模型与权限设置：不传则留空（跟随默认）。
	if _, badRunSettings := applyAutomationRunSettings(args, &spec); badRunSettings != nil {
		return *badRunSettings
	}
	if _, badMaxRuns := applyAutomationMaxRuns(args, &spec); badMaxRuns != nil {
		return *badMaxRuns
	}
	if _, badCatchUp := applyAutomationCatchUp(args, &spec); badCatchUp != nil {
		return *badCatchUp
	}

	created, err := autoSvc.Create(tc.Context, automation.CreateInput{
		ProjectID:       projectID,
		Name:            name,
		Prompt:          prompt,
		Spec:            spec,
		Enabled:         true,
		OriginThreadID:  callerThreadID,
		CreatorThreadID: callerThreadID,
	})
	if err != nil {
		return toolError(CodeInternal, "Failed to create automation: "+err.Error(), true)
	}

	return toolSuccess(map[string]any{
		"success":       true,
		"automation_id": created.ID,
		"name":          created.Name,
		"schedule":      created.Spec.Schedule,
		"run_mode":      created.Spec.NormalizedRunMode(),
	})
}

func handleAutomationList(tc ToolContext, args map[string]any) ToolResult {
	autoSvc := tc.Server.getAutomation()
	if autoSvc == nil {
		return toolError(CodeInternal, "Automation service is unavailable", false)
	}

	defs, err := autoSvc.List(tc.Context, tc.Project.ID)
	if err != nil {
		return toolError(CodeInternal, "Failed to list automations: "+err.Error(), true)
	}

	items := make([]map[string]any, len(defs))
	for i, def := range defs {
		runs, _ := autoSvc.Runs(tc.Context, def.ID, 10)
		runViews := make([]map[string]any, len(runs))
		for j, r := range runs {
			runViews[j] = automationRunView(r)
		}
		items[i] = map[string]any{
			"id":                   def.ID,
			"name":                 def.Name,
			"prompt":               def.Prompt,
			"is_enabled":           def.Enabled,
			"run_mode":             def.Spec.NormalizedRunMode(),
			"status":               def.Status,
			"schedule":             def.Schedule,
			"total_runs":           def.Spec.State.TotalRunCount,
			"consecutive_failures": def.Spec.State.ConsecutiveFailures,
			"stop_when":            def.Spec.StopWhen,
			"stop_reason":          def.Spec.State.StopReason,
			"max_runs":             def.Spec.EffectiveMaxRuns(),
			"catch_up":             def.Spec.CatchUpEnabled(),
			"recent_runs":          runViews,
		}
	}

	return toolSuccess(map[string]any{
		"count":       len(items),
		"automations": items,
	})
}

func handleAutomationUpdate(tc ToolContext, args map[string]any) ToolResult {
	if guard := automationWriteGuard(tc); guard != nil {
		return *guard
	}
	autoSvc := tc.Server.getAutomation()
	if autoSvc == nil {
		return toolError(CodeInternal, "Automation service is unavailable", false)
	}

	autoID, _ := args["automation_id"].(string)
	if strings.TrimSpace(autoID) == "" {
		return toolError(CodeInvalidArguments, "Missing required parameter 'automation_id'.", false)
	}
	autoID = strings.TrimSpace(autoID)

	auto, err := autoSvc.Get(tc.Context, autoID)
	if err != nil || (auto.ProjectID != nil && *auto.ProjectID != tc.Project.ID) {
		return toolError(CodeNotFound, "Automation not found in project.", false)
	}

	var in automation.UpdateInput
	if prompt, ok := args["prompt"].(string); ok && strings.TrimSpace(prompt) != "" {
		p := strings.TrimSpace(prompt)
		in.Prompt = &p
	}
	if isEnabled, ok := args["is_enabled"].(bool); ok {
		in.Enabled = &isEnabled
	}

	// stop_when、模型与权限、次数上限、补跑开关都落在同一份 Spec 上，
	// 任一项出现就整体提交，避免后一个分支覆盖前一个分支的改动。
	spec := auto.Spec
	specTouched := false
	if stopWhen, ok := args["stop_when"].(string); ok {
		spec.StopWhen = strings.TrimSpace(stopWhen)
		specTouched = true
	}
	if check, ok := args["check"].(string); ok {
		spec.Check = check
		specTouched = true
	}
	// 模型与权限：参数缺省表示不改，传空串表示恢复默认。
	if present, badRunSettings := applyAutomationRunSettings(args, &spec); badRunSettings != nil {
		return *badRunSettings
	} else if present {
		specTouched = true
	}
	if present, badMaxRuns := applyAutomationMaxRuns(args, &spec); badMaxRuns != nil {
		return *badMaxRuns
	} else if present {
		specTouched = true
	}
	if present, badCatchUp := applyAutomationCatchUp(args, &spec); badCatchUp != nil {
		return *badCatchUp
	} else if present {
		specTouched = true
	}
	if specTouched {
		in.Spec = &spec
	}

	updated, err := autoSvc.Update(tc.Context, autoID, in, auto.Revision)
	if err != nil {
		return toolError(CodeInternal, "Failed to update automation: "+err.Error(), true)
	}

	return toolSuccess(map[string]any{
		"success":       true,
		"automation_id": updated.ID,
		"is_enabled":    updated.Enabled,
	})
}

func handleAutomationCancel(tc ToolContext, args map[string]any) ToolResult {
	if guard := automationWriteGuard(tc); guard != nil {
		return *guard
	}
	autoSvc := tc.Server.getAutomation()
	if autoSvc == nil {
		return toolError(CodeInternal, "Automation service is unavailable", false)
	}

	autoID, _ := args["automation_id"].(string)
	if strings.TrimSpace(autoID) == "" {
		return toolError(CodeInvalidArguments, "Missing or invalid 'automation_id'.", false)
	}
	autoID = strings.TrimSpace(autoID)

	auto, err := autoSvc.Get(tc.Context, autoID)
	if err != nil || (auto.ProjectID != nil && *auto.ProjectID != tc.Project.ID) {
		return toolSuccess(map[string]any{
			"success":           true,
			"already_cancelled": true,
			"automation_id":     autoID,
			"message":           "That automation was already cancelled, or this project has no such automation.",
		})
	}

	if err := autoSvc.Delete(tc.Context, autoID); err != nil {
		return toolError(CodeInternal, "Failed to delete automation: "+err.Error(), true)
	}

	return toolSuccess(map[string]any{
		"success":               true,
		"already_cancelled":     false,
		"deleted_automation_id": autoID,
	})
}

func handleAutomationReportResult(tc ToolContext, args map[string]any) ToolResult {
	autoSvc := tc.Server.getAutomation()
	if autoSvc == nil {
		return toolError(CodeInternal, "Automation service is unavailable", false)
	}

	runID, _ := args["run_id"].(string)
	if strings.TrimSpace(runID) == "" {
		return toolError(CodeInvalidArguments, "Missing required parameter 'run_id'.", false)
	}
	runID = strings.TrimSpace(runID)

	autoID, _ := args["automation_id"].(string)
	if strings.TrimSpace(autoID) == "" {
		return toolError(CodeInvalidArguments, "Missing required parameter 'automation_id'.", false)
	}
	autoID = strings.TrimSpace(autoID)

	status, _ := args["status"].(string)
	if status != "succeeded" && status != "failed" {
		return toolError(CodeInvalidArguments, "Missing or invalid 'status'. Must be 'succeeded' or 'failed'.", false)
	}

	summary, _ := args["summary"].(string)
	if strings.TrimSpace(summary) == "" {
		return toolError(CodeInvalidArguments, "Missing required parameter 'summary'.", false)
	}
	summary = strings.TrimSpace(summary)

	auto, err := autoSvc.Get(tc.Context, autoID)
	if err != nil || (auto.ProjectID != nil && *auto.ProjectID != tc.Project.ID) {
		return toolError(CodeNotFound, "Automation not found in project.", false)
	}

	stopMet, _ := args["stop_condition_met"].(bool)
	stopReason, _ := args["stop_condition_reason"].(string)

	// 所有权：运行记录绑定了线程时只有该线程能结算，没有线程作用域的调用方一律视为不等。
	callerThreadID := automationScopeThread(tc.Context)
	if st := tc.Server.opts.Store; st != nil {
		if run, runErr := st.GetAutomationRun(tc.Context, runID); runErr == nil {
			if run.ThreadID != nil && *run.ThreadID != "" && *run.ThreadID != callerThreadID {
				return toolError(
					CodeForbidden,
					"That run was started by another thread.",
					false,
					"You can only report runs started in this conversation; automation_list shows which ones those are.",
				)
			}
		}
	}

	err = autoSvc.ReportRun(tc.Context, autoID, runID, callerThreadID, status, summary, stopReason, stopMet)
	switch {
	case errors.Is(err, automation.ErrRunNotFound):
		return toolError(CodeNotFound, "This automation has no such run in this project.", false)
	case errors.Is(err, automation.ErrRunNotOwned):
		return toolError(
			CodeForbidden,
			"That run was started by another thread.",
			false,
			"You can only report runs started in this conversation; automation_list shows which ones those are.",
		)
	case errors.Is(err, automation.ErrRunAlreadySettled):
		return toolError(CodeAlreadySettled, "A result for this run has already been recorded.", false)
	case err != nil:
		return toolError(CodeInternal, "Failed to settle run: "+err.Error(), true)
	}

	return toolSuccess(map[string]any{
		"success":            true,
		"run_id":             runID,
		"reported_status":    status,
		"stop_condition_met": stopMet,
	})
}

// automationFamily 是自动化入口族：普通会话只看到 `automation`，按 action 调用具体操作。
// automation_run_now 沿用了自动化前缀，但它是总管专属工具，不属于本族。
var automationFamily = &toolFamily{
	entry:       "automation",
	prefix:      "automation_",
	capability:  CapAutomation,
	description: "自动化入口：创建、查看、修改、取消本项目的定时自动化，以及结算某次运行的结果。先调用 action=help 查看可用操作及其参数，再用 action 加上 args 调用目标操作。",
	overview:    "自动化操作入口：先用 action=help 查看操作，再用 action 加 args 调用。",
}
