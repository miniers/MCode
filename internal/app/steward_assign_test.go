package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"mcode/internal/config"
	"mcode/internal/mcp"
	"mcode/internal/steward"
	"mcode/internal/swarm"
)

// stewardAssignTestProjectID 是夹具固定的总管会话所属项目 ID。
const stewardAssignTestProjectID = "proj-assign"

// stewardAssignTestWritePaths 是夹具固定的写入白名单。
func stewardAssignTestWritePaths() []string {
	return []string{"internal/steward/**", "README.md"}
}

// stewardAssignTestAssigner 构造一个只依赖假函数的派活器，捕获最后一次派发请求。
func stewardAssignTestAssigner(captured *swarm.SingleEntryRequest) *stewardAssigner {
	return &stewardAssigner{
		dispatch: func(_ context.Context, req swarm.SingleEntryRequest) (swarm.Bee, []string, error) {
			*captured = req
			return swarm.Bee{Name: "Kestrel", ThreadID: "bee-1"}, []string{"bee-1"}, nil
		},
		resolveProject: func(context.Context, string) (string, string, error) {
			return "mcode", "/work/mcode", nil
		},
		workDir: func(string) (string, bool) { return "/work/steward", true },
		threadProject: func(context.Context, string) (string, error) {
			return stewardAssignTestProjectID, nil
		},
	}
}

// 宿主必须显式给出权限模式、项目 ID 与写入白名单：总管自己只读，继承过去改动会被丢弃。
func TestStewardAssignerPassesHostOverrideAndProject(t *testing.T) {
	var captured swarm.SingleEntryRequest
	assigner := stewardAssignTestAssigner(&captured)
	writePaths := stewardAssignTestWritePaths()
	result, err := assigner.Request(context.Background(), "steward-1", mcp.AssignTaskInput{
		Project:    " mcode ",
		Task:       "把登录失败的提示改清楚",
		Context:    "现象见 internal/steward/hooks.go",
		WritePaths: writePaths,
	})
	if err != nil {
		t.Fatalf("Request 失败: %v", err)
	}
	if captured.Host == nil {
		t.Fatal("Host 不应为空")
	}
	if captured.Host.RuntimeMode != config.ModeAuto {
		t.Fatalf("权限模式应为 %q，实际 %q", config.ModeAuto, captured.Host.RuntimeMode)
	}
	if len(captured.Host.WritePaths) != len(writePaths) {
		t.Fatalf("写入白名单应与入参一致: %v", captured.Host.WritePaths)
	}
	for i, want := range writePaths {
		if captured.Host.WritePaths[i] != want {
			t.Fatalf("写入白名单第 %d 项应为 %q，实际 %q", i, want, captured.Host.WritePaths[i])
		}
	}
	if captured.ProjectID != stewardAssignTestProjectID {
		t.Fatalf("ProjectID 应取会话所属项目 %q，实际 %q", stewardAssignTestProjectID, captured.ProjectID)
	}
	if captured.LeadThreadID != "steward-1" || captured.LeadWorkDir != "/work/steward" {
		t.Fatalf("Lead 信息不符: %+v", captured)
	}
	if captured.Entry.Project != "/work/mcode" {
		t.Fatalf("Entry.Project 应为解析出的目录，实际 %q", captured.Entry.Project)
	}
	// 档位留空：走默认子代理档，不在这里钉死具体档位。
	if captured.Entry.Tier != "" {
		t.Fatalf("Entry.Tier 应留空走默认档位，实际 %q", captured.Entry.Tier)
	}
	if captured.Entry.Task != "执行：把登录失败的提示改清楚" {
		t.Fatalf("Entry.Task 不符: %q", captured.Entry.Task)
	}
	if result.BeeName != "Kestrel" || result.ThreadID != "bee-1" || result.ProjectName != "mcode" || result.WorkDir != "/work/mcode" {
		t.Fatalf("返回值不符: %+v", result)
	}
}

// 总管会话没有项目上下文时直接报错：空 ProjectID 会让后端查项目报 not found。
func TestStewardAssignerRequiresProjectID(t *testing.T) {
	dispatched := 0
	assigner := &stewardAssigner{
		dispatch: func(context.Context, swarm.SingleEntryRequest) (swarm.Bee, []string, error) {
			dispatched++
			return swarm.Bee{Name: "Kestrel", ThreadID: "bee-1"}, nil, nil
		},
		resolveProject: func(context.Context, string) (string, string, error) {
			return "mcode", "/work/mcode", nil
		},
		workDir: func(string) (string, bool) { return "/work/steward", true },
		threadProject: func(context.Context, string) (string, error) {
			return "  ", nil
		},
	}
	_, err := assigner.Request(context.Background(), "steward-1", mcp.AssignTaskInput{Project: "mcode", Task: "改一下"})
	if err == nil {
		t.Fatal("项目 ID 为空时应报错")
	}
	if !strings.Contains(err.Error(), "取不到总管会话所属项目") {
		t.Fatalf("错误文案应说明取不到项目: %v", err)
	}
	if dispatched != 0 {
		t.Fatalf("项目 ID 为空时不应派发，实际派发 %d 次", dispatched)
	}
}

// 找不到项目时把解析错误原样返回，且不派发。
func TestStewardAssignerPropagatesProjectError(t *testing.T) {
	dispatched := 0
	resolveErr := fmt.Errorf("找不到项目 nope，请先用 steward_projects_list 查项目")
	assigner := &stewardAssigner{
		dispatch: func(context.Context, swarm.SingleEntryRequest) (swarm.Bee, []string, error) {
			dispatched++
			return swarm.Bee{Name: "Kestrel", ThreadID: "bee-1"}, nil, nil
		},
		resolveProject: func(context.Context, string) (string, string, error) {
			return "", "", resolveErr
		},
		workDir: func(string) (string, bool) { return "/work/steward", true },
		threadProject: func(context.Context, string) (string, error) {
			return stewardAssignTestProjectID, nil
		},
	}
	_, err := assigner.Request(context.Background(), "steward-1", mcp.AssignTaskInput{Project: "nope", Task: "改一下"})
	if !errors.Is(err, resolveErr) {
		t.Fatalf("应原样返回项目解析错误，实际 %v", err)
	}
	if dispatched != 0 {
		t.Fatalf("项目解析失败时不应派发，实际派发 %d 次", dispatched)
	}
}

// 编排未开启时返回软结果：不报错，只告诉总管自己做。
func TestStewardAssignerSoftResultWhenSwarmDisabled(t *testing.T) {
	assigner := &stewardAssigner{
		dispatch: func(context.Context, swarm.SingleEntryRequest) (swarm.Bee, []string, error) {
			return swarm.Bee{}, nil, swarm.ErrSwarmDisabled
		},
		resolveProject: func(context.Context, string) (string, string, error) {
			return "mcode", "/work/mcode", nil
		},
		workDir: func(string) (string, bool) { return "/work/steward", true },
		threadProject: func(context.Context, string) (string, error) {
			return stewardAssignTestProjectID, nil
		},
	}
	res, err := assigner.Request(context.Background(), "steward-1", mcp.AssignTaskInput{Project: "mcode", Task: "改一下"})
	if err != nil {
		t.Fatalf("编排未开启时不应报错，实际 %v", err)
	}
	if res.Dispatched {
		t.Fatal("编排未开启时不应标记为已派发")
	}
	if res.BeeName != "" || res.ThreadID != "" {
		t.Fatalf("软结果不应带子代理信息，实际 %+v", res)
	}
	if !strings.Contains(res.Message, "没开编排") {
		t.Fatalf("软结果应说明宿主没开编排，实际 %q", res.Message)
	}
}

// 任务名按 rune 截断并只取首行：长中文不会切出乱码，侧边栏也能认出这条派发。
func TestStewardAssignerTrimsTaskTitle(t *testing.T) {
	var captured swarm.SingleEntryRequest
	assigner := stewardAssignTestAssigner(&captured)
	long := strings.Repeat("改", 60)
	_, err := assigner.Request(context.Background(), "steward-1", mcp.AssignTaskInput{
		Project: "mcode",
		Task:    long + "\n第二行是补充说明",
	})
	if err != nil {
		t.Fatalf("Request 失败: %v", err)
	}
	want := "执行：" + strings.Repeat("改", stewardAssignTaskTitleMaxRunes)
	if captured.Entry.Task != want {
		t.Fatalf("任务名应截断到 %d 个 rune，实际 %q", stewardAssignTaskTitleMaxRunes, captured.Entry.Task)
	}
	if !utf8.ValidString(captured.Entry.Task) {
		t.Fatalf("任务名不应切坏中文: %q", captured.Entry.Task)
	}
	if strings.Contains(captured.Entry.Task, "\n") {
		t.Fatalf("任务名只取首行: %q", captured.Entry.Task)
	}
}

// 派活 brief 必须禁止绝对路径与推远端，并把写入白名单列出来。
func TestStewardAssignPromptForbidsAbsolutePaths(t *testing.T) {
	prompt := buildStewardAssignPrompt("把登录失败的提示改清楚", "现象见 hooks.go", stewardAssignTestWritePaths())
	for _, want := range []string{"仓库相对路径", "git push", "internal/steward/**", "README.md", "只能改这些路径"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("brief 应包含 %q: %s", want, prompt)
		}
	}
	noPaths := buildStewardAssignPrompt("把提示改清楚", "", nil)
	if !strings.Contains(noPaths, "仓库相对路径") || !strings.Contains(noPaths, "git push") {
		t.Fatalf("没有写入白名单时也要限制路径与推远端: %s", noPaths)
	}
}

// ConfirmTopic 钩子由总管运行时直接给出，不依赖编排是否构造；
// 否则任务书待定问题清零后无法自动开工。
func TestStewardHooksConfirmTopicWired(t *testing.T) {
	rt := steward.New(steward.Deps{})
	hooks := rt.Hooks(nil, nil)
	if hooks.ConfirmTopic == nil {
		t.Fatal("Hooks 应接入 ConfirmTopic，任务书清零后由宿主自动开工")
	}
}
