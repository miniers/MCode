package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"mcode/internal/config"
	"mcode/internal/mcp"
	"mcode/internal/swarm"
)

// stewardTestProjectID 是夹具固定的总管会话所属项目 ID。
const stewardTestProjectID = "proj-steward"

// stewardTestPlanner 构造一个只依赖假函数的方案派发器，捕获最后一次派发请求。
func stewardTestPlanner(t *testing.T, cfg config.StewardConfig, captured *swarm.SingleEntryRequest) *stewardPlanner {
	t.Helper()
	return &stewardPlanner{
		dispatch: func(_ context.Context, req swarm.SingleEntryRequest) (swarm.Bee, []string, error) {
			if captured != nil {
				*captured = req
			}
			return swarm.Bee{Name: "Cove"}, []string{"bee-1"}, nil
		},
		resolveProject: func(context.Context, string) (string, string, error) {
			return "mcode", "/work/mcode", nil
		},
		workDir: func(string) (string, bool) { return "/work/steward", true },
		threadProject: func(context.Context, string) (string, error) {
			return stewardTestProjectID, nil
		},
		settings: func() (config.StewardConfig, error) {
			return cfg, nil
		},
		now: func() time.Time { return time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC) },
	}
}

// 用户亲自发消息的轮次才算用户轮次：事件唤醒、子代理汇报、已结束的轮次都不算。
func TestStewardTurnIsUser(t *testing.T) {
	cases := []struct {
		name   string
		kind   string
		text   string
		active bool
		want   bool
	}{
		{name: "用户消息", kind: "", text: "帮我看下这个报错", active: true, want: true},
		{name: "宿主事件", kind: "host_event", text: "会话卡住了", active: true, want: false},
		{name: "子代理汇报", kind: "", text: "[编排] bee 完成了", active: true, want: false},
		{name: "带空白前缀的汇报", kind: "", text: "  \n [编排] bee 完成了", active: true, want: false},
		{name: "轮次已结束", kind: "", text: "帮我看下这个报错", active: false, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stewardTurnIsUser(tc.kind, tc.text, tc.active); got != tc.want {
				t.Errorf("stewardTurnIsUser(%q, %q, %v) = %v，想要 %v", tc.kind, tc.text, tc.active, got, tc.want)
			}
		})
	}
}

// 方案模型配置原样进 Host，写入白名单只含方案目录，档位固定 deep。
func TestStewardPlannerRequestUsesPlanModel(t *testing.T) {
	cfg := config.StewardConfig{PlanProvider: "claude", PlanModel: "opus", PlanEffort: "high"}
	var captured swarm.SingleEntryRequest
	planner := stewardTestPlanner(t, cfg, &captured)
	result, err := planner.Request(context.Background(), "steward-1", mcp.PlanRequestInput{
		Project: "mcode",
		Goal:    "重构派发流程",
		Context: "现有实现耦合太紧",
	})
	if err != nil {
		t.Fatalf("Request 失败: %v", err)
	}
	if captured.Host == nil {
		t.Fatal("Host 不应为空")
	}
	if captured.Host.Provider != "claude" || captured.Host.Model != "opus" || captured.Host.Effort != "high" {
		t.Errorf("Host 模型配置 = %+v，想要 claude/opus/high", *captured.Host)
	}
	if len(captured.Host.WritePaths) != 1 || captured.Host.WritePaths[0] != stewardPlanWritePattern {
		t.Errorf("WritePaths = %v，想要只有 %q", captured.Host.WritePaths, stewardPlanWritePattern)
	}
	if captured.Entry.Tier != "deep" {
		t.Errorf("Entry.Tier = %q，想要 deep", captured.Entry.Tier)
	}
	if captured.LeadThreadID != "steward-1" {
		t.Errorf("LeadThreadID = %q，想要 steward-1", captured.LeadThreadID)
	}
	if captured.LeadWorkDir != "/work/steward" {
		t.Errorf("LeadWorkDir = %q，想要 /work/steward", captured.LeadWorkDir)
	}
	if captured.ProjectID != stewardTestProjectID {
		t.Errorf("ProjectID = %q，想要总管会话所属项目 %q", captured.ProjectID, stewardTestProjectID)
	}
	if captured.Host.RuntimeMode != config.ModeAuto {
		t.Errorf("Host.RuntimeMode = %q，想要 %q", captured.Host.RuntimeMode, config.ModeAuto)
	}
	if captured.Entry.Project != "/work/mcode" {
		t.Errorf("Entry.Project = %q，想要解析到的项目目录", captured.Entry.Project)
	}
	if result.BeeName != "Cove" || result.ProjectName != "mcode" || result.WorkDir != "/work/mcode" || result.PlanDir != stewardPlanDir {
		t.Errorf("结果 = %+v，字段不对", result)
	}
}

// 取不到总管会话所属项目时不能派发：项目 ID 留空会让后端按空 ID 查项目失败。
func TestStewardPlannerRequestRequiresThreadProject(t *testing.T) {
	cases := []struct {
		name string
		err  error
		id   string
	}{
		{name: "查询报错", err: errors.New("thread not found")},
		{name: "项目为空", id: "  "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dispatched := false
			planner := stewardTestPlanner(t, config.StewardConfig{}, nil)
			planner.dispatch = func(context.Context, swarm.SingleEntryRequest) (swarm.Bee, []string, error) {
				dispatched = true
				return swarm.Bee{Name: "Cove"}, nil, nil
			}
			planner.threadProject = func(context.Context, string) (string, error) {
				return tc.id, tc.err
			}
			if _, err := planner.Request(context.Background(), "steward-1", mcp.PlanRequestInput{Goal: "出个方案"}); err == nil {
				t.Fatal("取不到项目时应返回错误")
			}
			if dispatched {
				t.Error("取不到项目时不应派发")
			}
		})
	}
}

// 方案模型全空时仍要带 Host：写入白名单由宿主固定，不能因为没配置模型就丢。
func TestStewardPlannerRequestKeepsHostWithoutPlanModel(t *testing.T) {
	var captured swarm.SingleEntryRequest
	planner := stewardTestPlanner(t, config.StewardConfig{}, &captured)
	if _, err := planner.Request(context.Background(), "steward-1", mcp.PlanRequestInput{Goal: "出个方案"}); err != nil {
		t.Fatalf("Request 失败: %v", err)
	}
	if captured.Host == nil {
		t.Fatal("配置为空时 Host 也应非空")
	}
	if captured.Host.Provider != "" || captured.Host.Model != "" || captured.Host.Effort != "" {
		t.Errorf("Host 模型配置 = %+v，想要全空沿用 deep 档", *captured.Host)
	}
	if len(captured.Host.WritePaths) != 1 || captured.Host.WritePaths[0] != stewardPlanWritePattern {
		t.Errorf("WritePaths = %v，想要只有 %q", captured.Host.WritePaths, stewardPlanWritePattern)
	}
}

// 不指定项目时派到总管主目录：Entry.Project 留空，结果里报告会话目录。
func TestStewardPlannerRequestWithoutProject(t *testing.T) {
	var captured swarm.SingleEntryRequest
	planner := stewardTestPlanner(t, config.StewardConfig{}, &captured)
	planner.resolveProject = func(context.Context, string) (string, string, error) {
		t.Fatal("项目为空时不应解析项目")
		return "", "", nil
	}
	result, err := planner.Request(context.Background(), "steward-1", mcp.PlanRequestInput{Goal: "出个方案"})
	if err != nil {
		t.Fatalf("Request 失败: %v", err)
	}
	if captured.Entry.Project != "" {
		t.Errorf("Entry.Project = %q，想要空", captured.Entry.Project)
	}
	if captured.ProjectID != stewardTestProjectID {
		t.Errorf("ProjectID = %q，想要总管会话所属项目 %q", captured.ProjectID, stewardTestProjectID)
	}
	if result.ProjectName != stewardPlanHomeName {
		t.Errorf("ProjectName = %q，想要 %q", result.ProjectName, stewardPlanHomeName)
	}
	if result.WorkDir != "/work/steward" {
		t.Errorf("WorkDir = %q，想要会话目录", result.WorkDir)
	}
}

// 项目解析失败时不能派发。
func TestStewardPlannerRequestStopsOnResolveError(t *testing.T) {
	dispatched := false
	planner := stewardTestPlanner(t, config.StewardConfig{}, nil)
	planner.dispatch = func(context.Context, swarm.SingleEntryRequest) (swarm.Bee, []string, error) {
		dispatched = true
		return swarm.Bee{Name: "Cove"}, nil, nil
	}
	planner.resolveProject = func(context.Context, string) (string, string, error) {
		return "", "", errors.New("找不到项目 nope")
	}
	_, err := planner.Request(context.Background(), "steward-1", mcp.PlanRequestInput{Project: "nope", Goal: "出个方案"})
	if err == nil {
		t.Fatal("项目解析失败时应返回错误")
	}
	if dispatched {
		t.Error("项目解析失败时不应派发")
	}
}

// 编排关闭时派发返回 ErrSwarmDisabled，错误要能被上层识别。
func TestStewardPlannerRequestPropagatesSwarmDisabled(t *testing.T) {
	planner := stewardTestPlanner(t, config.StewardConfig{}, nil)
	planner.dispatch = func(context.Context, swarm.SingleEntryRequest) (swarm.Bee, []string, error) {
		return swarm.Bee{}, nil, swarm.ErrSwarmDisabled
	}
	_, err := planner.Request(context.Background(), "steward-1", mcp.PlanRequestInput{Goal: "出个方案"})
	if !errors.Is(err, swarm.ErrSwarmDisabled) {
		t.Fatalf("err = %v，想要 ErrSwarmDisabled", err)
	}
}

// 派发回来没有子代理名时算失败，避免上层报告一个空名字。
func TestStewardPlannerRequestRequiresBeeName(t *testing.T) {
	planner := stewardTestPlanner(t, config.StewardConfig{}, nil)
	planner.dispatch = func(context.Context, swarm.SingleEntryRequest) (swarm.Bee, []string, error) {
		return swarm.Bee{}, nil, nil
	}
	if _, err := planner.Request(context.Background(), "steward-1", mcp.PlanRequestInput{Goal: "出个方案"}); err == nil {
		t.Fatal("Bee.Name 为空时应返回错误")
	}
}

// 超长中文目标按 rune 截断，不能产生乱码。
func TestStewardPlannerRequestTrimsLongGoal(t *testing.T) {
	goal := strings.Repeat("重构方案", 30)
	var captured swarm.SingleEntryRequest
	planner := stewardTestPlanner(t, config.StewardConfig{}, &captured)
	if _, err := planner.Request(context.Background(), "steward-1", mcp.PlanRequestInput{Goal: goal}); err != nil {
		t.Fatalf("Request 失败: %v", err)
	}
	task := strings.TrimPrefix(captured.Entry.Task, "出方案：")
	if got := len([]rune(task)); got != 40 {
		t.Errorf("Task 目标长度 = %d，想要 40", got)
	}
	if !strings.HasPrefix(goal, task) {
		t.Errorf("Task 里的目标 %q 不是原文前缀（可能切坏了中文）", task)
	}
}

// 方案 brief 要写清写入范围、文件名日期与结构，并带上目标与补充信息。
func TestBuildStewardPlanPrompt(t *testing.T) {
	date := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	prompt := buildStewardPlanPrompt("重构派发流程", "现有实现耦合太紧", date)
	for _, want := range []string{
		"重构派发流程",
		"现有实现耦合太紧",
		stewardPlanDir,
		"2026-03-04",
		"不写实现代码",
		"补充信息",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("brief 缺少 %q:\n%s", want, prompt)
		}
	}
	withoutContext := buildStewardPlanPrompt("重构派发流程", "   ", date)
	if strings.Contains(withoutContext, "补充信息") {
		t.Errorf("没有补充信息时不应出现补充信息段:\n%s", withoutContext)
	}
	if !strings.Contains(withoutContext, "重构派发流程") {
		t.Errorf("brief 缺少目标:\n%s", withoutContext)
	}
}
