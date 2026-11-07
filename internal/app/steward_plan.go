package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"mcode/internal/config"
	"mcode/internal/mcp"
	"mcode/internal/remote"
	"mcode/internal/swarm"
)

const (
	// stewardPlanDir 是方案文档目录：方案子代理只能写这里，其他改动会在合并时被丢弃。
	stewardPlanDir = "docs/superpowers/plans"
	// stewardPlanWritePattern 是方案子代理的写入白名单，由宿主代码固定，不接受模型参数放宽。
	stewardPlanWritePattern = stewardPlanDir + "/**/*.md"
	// stewardPlanTier 是方案子代理的档位：出方案要读代码、想清楚，按 deep 档解析模型。
	stewardPlanTier = "deep"
	// stewardPlanHomeName 是没有指定项目时方案子代理所在的项目名，即总管主目录。
	stewardPlanHomeName = "总管主目录"
	// stewardPlanTaskLimit 是派发任务名里保留的目标字数，只用于在侧边栏认出这条派发。
	stewardPlanTaskLimit = 40
	// stewardPlanRuntimeMode 是方案子代理的权限模式：总管本身是只读的，不能让方案子代理
	// 继承该模式（否则写不了方案文档）；子代理无人值守，也不能卡在审批上，且它在独立
	// 工作副本里运行，合并时只留白名单内的文件，故选 auto。
	stewardPlanRuntimeMode = config.ModeAuto
)

// stewardTurnIsUser 判断这一轮是否由用户亲自发消息开起：宿主事件唤醒的轮次
// （会话卡住、任务失败、日报等）与收到子代理汇报的轮次都不算，且必须仍在本轮内。
func stewardTurnIsUser(kind, text string, active bool) bool {
	if !active || kind != "" {
		return false
	}
	return !swarm.IsNotice(strings.TrimSpace(text))
}

// resolveStewardPlanProject 把 plan_request 的项目参数解析成项目名与项目根目录：
// 依次按项目 ID 全等、名字不区分大小写、清理后的路径全等匹配，都不命中就报错，
// 让总管去查项目清单后重试。
func resolveStewardPlanProject(ctx context.Context, backend *remote.MemoryBackend, ref string) (name, path string, err error) {
	if backend == nil {
		return "", "", fmt.Errorf("会话后端未接入，找不到项目 %s", strings.TrimSpace(ref))
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", fmt.Errorf("找不到项目 %s，请先用 steward_projects_list 查项目", ref)
	}
	list, err := backend.ListProjects(ctx)
	if err != nil {
		return "", "", err
	}
	for _, project := range list.Projects {
		if project.ID == ref {
			return project.Name, project.Path, nil
		}
	}
	for _, project := range list.Projects {
		if strings.EqualFold(project.Name, ref) {
			return project.Name, project.Path, nil
		}
	}
	cleaned := filepath.Clean(ref)
	for _, project := range list.Projects {
		if project.Path != "" && filepath.Clean(project.Path) == cleaned {
			return project.Name, project.Path, nil
		}
	}
	return "", "", fmt.Errorf("找不到项目 %s，请先用 steward_projects_list 查项目", ref)
}

// stewardPlanner 把 plan_request 变成一次派发：字段全是函数，测试时换成假实现即可，
// 不必启动真实的派发管理器。
type stewardPlanner struct {
	// dispatch 派发单个委派条目。
	dispatch func(context.Context, swarm.SingleEntryRequest) (swarm.Bee, []string, error)
	// resolveProject 把项目 ID、名字或目录解析成项目名与目录。
	resolveProject func(ctx context.Context, ref string) (name, path string, err error)
	// workDir 返回会话所在目录，派发时当 Lead 工作目录。
	workDir func(threadID string) (string, bool)
	// threadProject 返回总管会话所属的项目 ID：总管线程没有开编排，派发时项目参数留空，
	// 只能从会话本身取，否则后端按空项目 ID 查项目会报 not found。
	threadProject func(ctx context.Context, threadID string) (string, error)
	// settings 现读配置，方案模型与推理强度来自这里。
	settings func() (config.StewardConfig, error)
	// now 提供当前时间，用于方案文件名里的日期。
	now func() time.Time
}

// Request 用配置的方案模型派一个只写方案文档的子代理：项目为空时派到总管主目录，
// 写入白名单固定为 stewardPlanWritePattern。
func (p *stewardPlanner) Request(ctx context.Context, threadID string, req mcp.PlanRequestInput) (mcp.PlanRequestResult, error) {
	cfg, err := p.settings()
	if err != nil {
		return mcp.PlanRequestResult{}, fmt.Errorf("读取总管配置失败: %w", err)
	}
	leadWorkDir, ok := p.workDir(threadID)
	if !ok || strings.TrimSpace(leadWorkDir) == "" {
		return mcp.PlanRequestResult{}, fmt.Errorf("取不到总管会话目录，无法派发方案子代理")
	}
	projectID, err := p.threadProject(ctx, threadID)
	if err != nil {
		return mcp.PlanRequestResult{}, fmt.Errorf("取不到总管会话所属项目: %w", err)
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return mcp.PlanRequestResult{}, fmt.Errorf("取不到总管会话所属项目，无法派发方案子代理")
	}
	name, path := stewardPlanHomeName, leadWorkDir
	projectDir := ""
	if ref := strings.TrimSpace(req.Project); ref != "" {
		resolvedName, resolvedPath, err := p.resolveProject(ctx, ref)
		if err != nil {
			return mcp.PlanRequestResult{}, err
		}
		name, path, projectDir = resolvedName, resolvedPath, resolvedPath
	}
	now := time.Now
	if p.now != nil {
		now = p.now
	}
	started := now()
	entry := swarm.Delegation{
		Task:    "出方案：" + trimPlanTaskGoal(req.Goal),
		Tier:    stewardPlanTier,
		Project: projectDir,
		Prompt:  buildStewardPlanPrompt(req.Goal, req.Context, started),
	}
	bee, _, err := p.dispatch(ctx, swarm.SingleEntryRequest{
		LeadThreadID: threadID,
		ProjectID:    projectID,
		LeadWorkDir:  leadWorkDir,
		Entry:        entry,
		Host: &swarm.HostOverride{
			Provider:    cfg.PlanProvider,
			Model:       cfg.PlanModel,
			Effort:      cfg.PlanEffort,
			RuntimeMode: stewardPlanRuntimeMode,
			WritePaths:  []string{stewardPlanWritePattern},
		},
	})
	if err != nil {
		return mcp.PlanRequestResult{}, err
	}
	if strings.TrimSpace(bee.Name) == "" {
		return mcp.PlanRequestResult{}, fmt.Errorf("子代理没有派发成功")
	}
	return mcp.PlanRequestResult{
		BeeName:     bee.Name,
		ProjectName: name,
		WorkDir:     path,
		PlanDir:     stewardPlanDir,
	}, nil
}

// trimPlanTaskGoal 截取目标的前 stewardPlanTaskLimit 个 rune 当任务名：按 rune 截断，
// 中文不会被切出乱码。
func trimPlanTaskGoal(goal string) string {
	trimmed := strings.TrimSpace(goal)
	runes := []rune(trimmed)
	if len(runes) <= stewardPlanTaskLimit {
		return trimmed
	}
	return string(runes[:stewardPlanTaskLimit])
}

// buildStewardPlanPrompt 写给方案子代理的完整 brief：只允许写方案文档，先读代码再落笔，
// 方案要能直接照着实施。
func buildStewardPlanPrompt(goal, context string, date time.Time) string {
	var b strings.Builder
	b.WriteString("你的任务是为下面的目标出实现方案，不写实现代码。\n\n")
	b.WriteString("写入范围：只有 " + stewardPlanDir + "/ 下的 .md 文件能合并回项目，其他改动都会被丢弃。\n\n")
	b.WriteString("做法：\n")
	b.WriteString("1. 先读相关代码，确认现状，不要凭猜测写方案。\n")
	b.WriteString("2. 把方案写到 " + stewardPlanDir + "/ 下，文件名形如 " + date.Format("2006-01-02") + "-简短英文slug.md。\n")
	b.WriteString("3. 方案里要有：目标、现状（带 path:line）、设计决策及理由、分步实施步骤、风险与未决问题。\n")
	b.WriteString("4. 分步实施步骤的每一步都写清文件、位置、改法和验收方式。\n")
	b.WriteString("5. 完成后在汇报里给出方案文件的相对路径和三五行结论。\n\n")
	b.WriteString("目标原文：\n" + goal + "\n")
	if ctx := strings.TrimSpace(context); ctx != "" {
		b.WriteString("\n补充信息：\n" + ctx + "\n")
	}
	return b.String()
}
