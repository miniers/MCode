package kanban

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"mcode/internal/agent/gate"
)

type ResultStatus string

const (
	ResultDone          ResultStatus = "done"
	ResultFailed        ResultStatus = "failed"
	ResultWaiting       ResultStatus = "waiting"
	ResultDelegated     ResultStatus = "delegated"
	ResultNeedsDecision ResultStatus = "needs_decision"
)

type ExecutorResult struct {
	Status         ResultStatus     `json:"status"`
	Summary        string           `json:"summary"`
	NoChange       bool             `json:"no_change,omitempty"`
	WaitingOn      string           `json:"waiting_on,omitempty"`
	WaitKind       string           `json:"wait_kind,omitempty"`
	WaitMinutes    int              `json:"wait_minutes,omitempty"`
	WaitCheck      string           `json:"wait_check,omitempty"`
	Question       string           `json:"question,omitempty"`
	Options        []DecisionOption `json:"options,omitempty"`
	Recommendation string           `json:"recommendation,omitempty"`
	Followups      []Followup       `json:"followups,omitempty"`
	Lessons        []string         `json:"lessons,omitempty"`
	Attachments    []string         `json:"attachments,omitempty"`
}

type DecisionOption struct {
	Key  string `json:"key"`
	Text string `json:"text"`
}

type Followup struct {
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
	Project  string `json:"project,omitempty"`
	Priority string `json:"priority,omitempty"`
	Agent    string `json:"agent,omitempty"`
}

func (r ExecutorResult) Validate() error {
	if r.NoChange && r.Status != ResultDone {
		return fmt.Errorf("no_change 仅允许用于 done 状态")
	}
	switch r.Status {
	case ResultDone, ResultFailed:
	case ResultWaiting:
		if r.WaitKind != "human" && r.WaitKind != "machine" {
			return fmt.Errorf("wait_kind 必须是 human 或 machine")
		}
		if r.WaitKind == "machine" && strings.TrimSpace(r.WaitCheck) == "" {
			return fmt.Errorf("machine 等待必须提供 wait_check")
		}
		if r.WaitKind == "machine" && !gate.IsReadOnlyCommand(strings.TrimSpace(r.WaitCheck)) {
			return fmt.Errorf("wait_check 必须是只读 shell 命令（在项目根目录执行，退出码 0 表示等待结束）；等待 bee 汇报或人工确认请用 human 等待，或直接提交 done")
		}
	case ResultDelegated:
		if len(r.Followups) == 0 {
			return fmt.Errorf("delegated 必须提供 followups")
		}
		for _, f := range r.Followups {
			if strings.TrimSpace(f.Title) == "" {
				return fmt.Errorf("子任务标题不能为空")
			}
		}
	case ResultNeedsDecision:
		if strings.TrimSpace(r.Question) == "" || len(r.Options) < 2 {
			return fmt.Errorf("needs_decision 必须提供问题和至少两个选项")
		}
	default:
		return fmt.Errorf("无效的结果状态 %q", r.Status)
	}
	return nil
}

func (r *ExecutorResult) clampWaitMinutes() {
	if r.WaitMinutes < 5 {
		r.WaitMinutes = 5
	}
	if r.WaitMinutes > 120 {
		r.WaitMinutes = 120
	}
}

var resultBlock = regexp.MustCompile("(?s)```json[ \\t]*\\r?\\n(.*?)```")

func ParseExecutorResult(text string) (ExecutorResult, error) {
	var r ExecutorResult
	blocks := resultBlock.FindAllStringSubmatch(text, -1)
	if len(blocks) > 0 {
		text = blocks[len(blocks)-1][1]
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &r); err != nil {
		return r, err
	}
	r.clampWaitMinutes()
	return r, r.Validate()
}
