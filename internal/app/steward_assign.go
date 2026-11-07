package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"mcode/internal/config"
	"mcode/internal/mcp"
	"mcode/internal/swarm"
)

const (
	// stewardAssignTaskTitleMaxRunes 是派活任务名里保留的任务字数，只用于在侧边栏认出这条派发。
	stewardAssignTaskTitleMaxRunes = 40
	// stewardAssignRuntimeMode 是派活子代理的权限模式：总管本身跟随全局默认权限档位，不显式给出时子代理会
	// 继承 readonly 档，改动会被判为只读违规并丢弃；子代理无人值守，也不能卡在审批上，且它在
	// 独立工作副本里运行，改动经合并才回到目标项目，故选 auto。
	stewardAssignRuntimeMode = config.ModeAuto
)

// stewardAssigner 把 steward_assign_task 变成一次派发：字段全是函数，测试时换成假实现即可，
// 不必启动真实的派发管理器。
type stewardAssigner struct {
	// dispatch 派发单个委派条目。
	dispatch func(context.Context, swarm.SingleEntryRequest) (swarm.Bee, []string, error)
	// resolveProject 把项目 ID、名字或目录解析成项目名与目录。
	resolveProject func(ctx context.Context, ref string) (name, path string, err error)
	// workDir 返回会话所在目录，派发时当 Lead 工作目录。
	workDir func(threadID string) (string, bool)
	// threadProject 返回总管会话所属的项目 ID：总管线程没有开编排，派发时项目参数留空，
	// 只能从会话本身取，否则后端按空项目 ID 查项目会报 not found。
	threadProject func(ctx context.Context, threadID string) (string, error)
}

// Request 在目标项目里派一个可写子代理把 req.Task 做完：档位留空走默认子代理档，
// 权限模式固定 auto，写入白名单沿用调用方给的仓库相对路径。
func (p *stewardAssigner) Request(ctx context.Context, threadID string, req mcp.AssignTaskInput) (mcp.AssignTaskResult, error) {
	leadWorkDir, ok := p.workDir(threadID)
	if !ok || strings.TrimSpace(leadWorkDir) == "" {
		return mcp.AssignTaskResult{}, fmt.Errorf("取不到总管会话目录，无法派发子代理")
	}
	projectID, err := p.threadProject(ctx, threadID)
	if err != nil {
		return mcp.AssignTaskResult{}, fmt.Errorf("取不到总管会话所属项目: %w", err)
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return mcp.AssignTaskResult{}, fmt.Errorf("取不到总管会话所属项目，无法派发子代理")
	}
	name, path, err := p.resolveProject(ctx, strings.TrimSpace(req.Project))
	if err != nil {
		return mcp.AssignTaskResult{}, err
	}
	entry := swarm.Delegation{
		Task:    "执行：" + trimStewardAssignTaskTitle(req.Task),
		Project: path,
		Prompt:  buildStewardAssignPrompt(req.Task, req.Context, req.WritePaths),
	}
	bee, _, err := p.dispatch(ctx, swarm.SingleEntryRequest{
		LeadThreadID: threadID,
		ProjectID:    projectID,
		LeadWorkDir:  leadWorkDir,
		Entry:        entry,
		Host: &swarm.HostOverride{
			RuntimeMode: stewardAssignRuntimeMode,
			WritePaths:  req.WritePaths,
		},
	})
	if err != nil {
		if errors.Is(err, swarm.ErrSwarmDisabled) {
			// 宿主没开编排不是错误：告诉总管自己动手即可，不要让工具报错中断这一轮。
			return mcp.AssignTaskResult{Message: "宿主没开编排，请你自己做。"}, nil
		}
		return mcp.AssignTaskResult{}, err
	}
	if strings.TrimSpace(bee.Name) == "" {
		return mcp.AssignTaskResult{}, fmt.Errorf("子代理没有派发成功")
	}
	return mcp.AssignTaskResult{
		Dispatched:  true,
		BeeName:     bee.Name,
		ThreadID:    bee.ThreadID,
		ProjectName: name,
		WorkDir:     path,
	}, nil
}

// trimStewardAssignTaskTitle 截取任务首行的前 stewardAssignTaskTitleMaxRunes 个 rune 当任务名：
// 按 rune 截断，中文不会被切出乱码。
func trimStewardAssignTaskTitle(task string) string {
	firstLine := strings.TrimSpace(task)
	if idx := strings.IndexByte(firstLine, '\n'); idx >= 0 {
		firstLine = strings.TrimSpace(firstLine[:idx])
	}
	runes := []rune(firstLine)
	if len(runes) <= stewardAssignTaskTitleMaxRunes {
		return firstLine
	}
	return string(runes[:stewardAssignTaskTitleMaxRunes])
}

// buildStewardAssignPrompt 写给派活子代理的完整 brief：任务原文、补充信息、只改目标项目、
// 路径一律用仓库相对路径、不许推远端，最后要求它汇报改了什么与验证了什么。
func buildStewardAssignPrompt(task, context string, writePaths []string) string {
	var b strings.Builder
	b.WriteString("你的任务是在目标项目里把下面这件事做完，做完汇报结果。\n\n")
	b.WriteString("要做的事与验收要求：\n" + strings.TrimSpace(task) + "\n")
	if ctx := strings.TrimSpace(context); ctx != "" {
		b.WriteString("\n补充信息：\n" + ctx + "\n")
	}
	b.WriteString("\n约束：\n")
	b.WriteString("1. 只在目标项目里改动，不要碰其他项目，也不要碰宿主主检出。\n")
	b.WriteString("2. 任务与汇报中的路径一律用仓库相对路径（例如 internal/app/steward_assign.go:12），" +
		"禁止写宿主主检出的绝对路径，否则改动会漏提交。\n")
	if len(writePaths) > 0 {
		b.WriteString("3. 只能改这些路径，其他文件的改动会在合并时被丢弃：\n")
		for _, p := range writePaths {
			b.WriteString("   - " + p + "\n")
		}
	} else {
		b.WriteString("3. 改动范围要贴合任务，不要顺手改无关文件。\n")
	}
	b.WriteString("4. 不要 git push，不要改远程分支或远程仓库。\n")
	b.WriteString("5. 完成后汇报：改了哪些文件、跑了什么验证、还有什么没做。\n")
	return b.String()
}
