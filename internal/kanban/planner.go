package kanban

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"mcode/internal/store"
)

// replanStartTimeout 是复用原规划会话追加规划回合时，等待该回合启动的最长时间。
var replanStartTimeout = 30 * time.Second

// planJSONExample 是规划输出 JSON 的示例格式，首次规划与复用规划会话共用同一份文案。
const planJSONExample = "{\"summary\":\"...\",\"tasks\":[{\"title\":\"...\",\"notes\":\"...\",\"dependsOn\":[0],\"acceptanceCriteria\":[\"[命令] `go test ./...`\"]}]}"

// planPromptRequirements 说明规划 JSON 字段含义与验收标准的分配要求。
const planPromptRequirements = "要求：\n" +
	"- dependsOn 用任务在 tasks 数组里的下标表示前置任务。\n" +
	"- 每个任务可带 acceptanceCriteria 数组，逐条写出该任务的验收标准。\n" +
	"- 如果目标提示词里有『验收标准』一节，把每条标准分配给负责它的任务，原样保留行首标签（[命令]、[审阅]、[你验]）与其中用反引号包住的命令。"

// buildPlanPrompt 组装首次规划提示词；feedback 非空时在末尾附上补充反馈。
func buildPlanPrompt(goal store.Goal, feedback string) string {
	prompt := fmt.Sprintf("请根据以下目标进行规划，并只输出一个 JSON 对象，格式为：\n%s\n\n%s\n\n目标提示词:\n%s",
		planJSONExample, planPromptRequirements, goal.Prompt)
	if feedback != "" {
		prompt += fmt.Sprintf("\n\n补充反馈:\n%s", feedback)
	}
	return prompt
}

// buildReplanPrompt 组装复用原规划会话时的重规划提示词。
func buildReplanPrompt(feedback string) string {
	return fmt.Sprintf("用户看过你的计划，希望按以下反馈调整：\n\n%s\n\n请回复完整的修订后计划（不只是变更部分），并只输出一个 JSON 对象，格式为：\n%s\n\n%s\n未执行过的旧任务已被移除。",
		feedback, planJSONExample, planPromptRequirements)
}

// PlannedTask 表示规划阶段生成的任务规格。
type PlannedTask struct {
	Title              string   `json:"title"`
	Notes              string   `json:"notes"`
	DependsOn          []int    `json:"dependsOn"`
	AcceptanceCriteria []string `json:"acceptanceCriteria"`
}

// PlanResult 包含规划结果摘要、会话 ID 及生成的任务列表。
type PlanResult struct {
	Summary  string        `json:"summary"`
	ThreadID string        `json:"threadId"`
	Tasks    []PlannedTask `json:"tasks"`
}

// Planner 定义目标任务规划器接口。
type Planner interface {
	Plan(ctx context.Context, goal store.Goal, feedback string) (PlanResult, error)
}

// maxPlanRepairs 是模型输出无法解析为计划时最多补交的次数。
const maxPlanRepairs = 2

// buildPlanRepairPrompt 组装补交提示词：说明上一条回复无法解析，并要求只按原格式重新输出计划。
func buildPlanRepairPrompt(err error) string {
	return fmt.Sprintf("你上一条回复无法解析为计划，具体错误是：%v\n\n请只按原格式重新输出完整的计划，不要做别的事，并只输出一个 JSON 对象，格式为：\n%s\n\n%s",
		err, planJSONExample, planPromptRequirements)
}

// AgentPlanner 是基于 AI 会话驱动的目标任务规划器实现。
type AgentPlanner struct {
	Driver       SessionDriver
	Projects     ProjectResolver
	PollInterval time.Duration
	// ReplanStartTimeout 覆盖等待复用会话回合启动或补交回复的最长时间，零值时使用 replanStartTimeout。
	ReplanStartTimeout time.Duration
}

// startTimeout 返回等待回合启动或补交回复的最长时间。
func (a *AgentPlanner) startTimeout() time.Duration {
	if a.ReplanStartTimeout > 0 {
		return a.ReplanStartTimeout
	}
	return replanStartTimeout
}

// NewAgentPlanner 创建一个 AgentPlanner 实例，默认轮询间隔为 2s。
func NewAgentPlanner(d SessionDriver, p ProjectResolver) *AgentPlanner {
	return &AgentPlanner{
		Driver:       d,
		Projects:     p,
		PollInterval: 2 * time.Second,
	}
}

// Plan 启动规划会话并轮询执行结果，将模型返回的 JSON 解析为 PlanResult。
// 目标已有 PlanThreadID 时（总管先开话题再规划，或带 feedback 的重规划）在该会话上追加一轮；
// 没有时首次规划以 plan 模式启动独立会话。规划成功后把会话切回 build 模式复用为协调员。
func (a *AgentPlanner) Plan(ctx context.Context, goal store.Goal, feedback string) (PlanResult, error) {
	if a.Driver == nil {
		return PlanResult{}, errors.New("kanban: session driver is nil")
	}

	var root string
	if a.Projects != nil {
		var err error
		root, err = a.Projects.ProjectRoot(ctx, goal.ProjectID)
		if err != nil {
			return PlanResult{}, fmt.Errorf("resolve project root: %w", err)
		}
	}

	prompt := buildPlanPrompt(goal, feedback)

	// 已有规划会话时优先复用：无反馈时追加发送首次规划提示词，有反馈时追加重规划文案，
	// 让规划器保留已读过的上下文（D-KB-016）。
	threadID := ""
	reused := false
	reusedAt := time.Time{}
	baselineText := ""
	// busyAtSend 记录追加规划提示词时会话是否仍在运行：此时会话停止后的文本可能只是没写完的任务书。
	busyAtSend := false
	if goal.PlanThreadID != nil && strings.TrimSpace(*goal.PlanThreadID) != "" {
		if messenger, ok := a.Driver.(SessionMessenger); ok {
			existing := strings.TrimSpace(*goal.PlanThreadID)
			if st, err := a.Driver.SessionState(ctx, existing); err == nil {
				baselineText = st.LastAssistantText
				busyAtSend = st.Running
				followUp := prompt
				if feedback != "" {
					followUp = buildReplanPrompt(feedback)
				}
				if err := messenger.SendFollowUp(ctx, existing, followUp); err == nil {
					threadID = existing
					reused = true
					reusedAt = time.Now()
				}
			}
		}
	}
	if threadID == "" {
		id, err := a.Driver.StartSession(ctx, SessionRequest{
			ProjectID:       goal.ProjectID,
			WorkDir:         root,
			Title:           "Plan: " + goal.Title,
			Prompt:          prompt,
			InteractionMode: "plan",
		})
		if err != nil {
			return PlanResult{}, fmt.Errorf("start plan session: %w", err)
		}
		threadID = id
	}

	interval := a.PollInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	startTimeout := a.startTimeout()
	sawRunning := false
	// 解析失败后的补交状态：failedText 是失败的原文，failedAt 非零表示正在为这段文本等待新回合。
	failedText := ""
	failedAt := time.Time{}
	repairs := 0
	for {
		select {
		case <-ctx.Done():
			return PlanResult{}, ctx.Err()
		case <-ticker.C:
			st, err := a.Driver.SessionState(ctx, threadID)
			if err != nil {
				return PlanResult{}, fmt.Errorf("query session state: %w", err)
			}
			if st.Running {
				// 会话重新运行说明进入了新回合，上一段失败文本的等待作废。
				sawRunning = true
				failedAt = time.Time{}
				continue
			}
			if reused && !sawRunning && failedAt.IsZero() && st.LastAssistantText == baselineText {
				// 追加的重规划回合尚未开始或尚未产出新回复：短时等待，超时判定未启动。
				if time.Since(reusedAt) > startTimeout {
					return PlanResult{}, errors.New("plan follow-up did not start")
				}
				continue
			}
			if st.Failed {
				reason := st.FailureReason
				if reason == "" {
					reason = "planning session marked as failed"
				}
				return PlanResult{}, errors.New(reason)
			}

			res, err := parsePlanOutput(st.LastAssistantText, threadID)
			if err != nil {
				// fresh 表示这是一段新的失败文本，而不是正在等待的同一段。
				fresh := failedAt.IsZero() || st.LastAssistantText != failedText
				if fresh {
					failedText = st.LastAssistantText
					failedAt = time.Time{}
				}
				if fresh && (reused || busyAtSend) {
					// 复用会话时第一段停止文本可能只是总管回合的任务书，不是计划；
					// 先等下一回合，超时仍无新回复再要求补交。
					failedAt = time.Now()
					sawRunning = false
					continue
				}
				if !failedAt.IsZero() && time.Since(failedAt) <= startTimeout {
					continue
				}
				if messenger, ok := a.Driver.(SessionMessenger); ok && repairs < maxPlanRepairs {
					if sendErr := messenger.SendFollowUp(ctx, threadID, buildPlanRepairPrompt(err)); sendErr == nil {
						repairs++
						baselineText = st.LastAssistantText
						reused = true
						reusedAt = time.Now()
						sawRunning = false
						failedAt = time.Now()
						continue
					}
				}
				return res, err
			}
			// 规划完成后把计划会话保留为目标的协调员（D-KB-018）。
			if setter, ok := a.Driver.(InteractionModeSetter); ok {
				_ = setter.SetInteractionMode(ctx, threadID, "build")
			}
			return res, nil
		}
	}
}

func parsePlanOutput(text, threadID string) (PlanResult, error) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start == -1 || end == -1 || start >= end {
		return PlanResult{}, errors.New("kanban: no valid JSON object found in assistant output")
	}
	jsonStr := text[start : end+1]

	var parsed struct {
		Summary string        `json:"summary"`
		Tasks   []PlannedTask `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		return PlanResult{}, fmt.Errorf("kanban: unmarshal plan json: %w", err)
	}

	if len(parsed.Tasks) == 0 {
		return PlanResult{}, errors.New("kanban: tasks list is empty")
	}

	n := len(parsed.Tasks)
	adj := make([][]int, n)
	for i, t := range parsed.Tasks {
		for _, dep := range t.DependsOn {
			if dep < 0 || dep >= n {
				return PlanResult{}, fmt.Errorf("kanban: dependency out of bounds: %d for task %d", dep, i)
			}
			if dep == i {
				return PlanResult{}, fmt.Errorf("kanban: task %d cannot depend on itself", i)
			}
			adj[i] = append(adj[i], dep)
		}
	}

	visited := make([]int, n) // 0: unvisited, 1: visiting, 2: visited
	var hasCycle func(u int) bool
	hasCycle = func(u int) bool {
		visited[u] = 1
		for _, v := range adj[u] {
			if visited[v] == 1 {
				return true
			}
			if visited[v] == 0 && hasCycle(v) {
				return true
			}
		}
		visited[u] = 2
		return false
	}

	for i := 0; i < n; i++ {
		if visited[i] == 0 && hasCycle(i) {
			return PlanResult{}, errors.New("kanban: cycle detected in planned tasks")
		}
	}

	return PlanResult{
		Summary:  parsed.Summary,
		ThreadID: threadID,
		Tasks:    parsed.Tasks,
	}, nil
}
