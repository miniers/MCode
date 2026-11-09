package frontdesk

import "context"

// AutomationService 提供定时任务创建能力。
type AutomationService interface {
	CreateAutomation(context.Context, AutomationInput) (AutomationResult, error)
}

// AutomationInput 描述需要独立运行的定时任务。
type AutomationInput struct {
	ProjectID, Name, Prompt, Schedule string
}

// AutomationResult 描述创建的定时任务。
type AutomationResult struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	NextRunAt string `json:"next_run_at,omitempty"`
}
