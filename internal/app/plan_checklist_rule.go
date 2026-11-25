package app

import "strings"

// 首轮注入多步任务计划清单规则：模型默认不主动调用计划工具，回合头清单与计划胶囊靠它触发。
const planChecklistRule = `For a task of three or more steps, keep a checklist in your native todo tool (TaskCreate/TaskUpdate, TodoWrite, update_plan or todo_write, whichever you have; if it is deferred, load it first with your tool-search tool) before you start, and mark each step done as you finish it. The user sees it as the turn's checklist.`

func firstTurnExtraSection(dataDir, threadID string, plansOnlyLead bool) string {
	if plansOnlyLead {
		return ""
	}
	handoff := frontdeskHandoffSection(dataDir, threadID)
	if strings.TrimSpace(handoff) == "" {
		return planChecklistRule
	}
	return planChecklistRule + "\n\n" + handoff
}
