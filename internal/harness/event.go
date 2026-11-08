package harness

import (
	"encoding/json"
	"errors"
	"fmt"
)

// EventType 表示事件类型。
type EventType string

const (
	EventText       EventType = "text"
	EventReasoning  EventType = "reasoning"
	EventToolStart  EventType = "tool_start"
	EventToolOutput EventType = "tool_output"
	EventToolEnd    EventType = "tool_end"
	EventFileChange EventType = "file_change"
	EventSession    EventType = "session"
	EventUsage      EventType = "usage"
	EventError      EventType = "error"
	EventDone       EventType = "done"

	// EventTextRetract 撤回此前以 EventText 流出、但上游最终没有确认的一段正文：Text 是这段正文全文，
	// 一定是已流出正文的末尾。CLI 在流中断后会重新请求，模型看不到这段残文，会从头再写。
	EventTextRetract EventType = "text_retract"

	// EventContextCompacted 表示 provider 已经把已有上下文压缩成摘要（pi 的 compaction_end、
	// claude 的 compact_boundary、codex 的 thread/compacted）。它不发正文，时间线据此落一条
	// 独立的压缩边界行：不能被当成 reasoning 归进回合折叠。
	EventContextCompacted EventType = "context_compacted"

	// EventTurnAccepted 表示 provider 已确认把本轮用户输入交给 CLI：codex 以 turn/start 成功
	// 返回为准，claude 以 prompt 写进子进程 stdin 为准，pi 以 RPC prompt 写入成功为准（弱保证，
	// 只说明帧进了进程管道）。它不带正文，只用于把「已送达」和「还没有任何输出」区分开。
	EventTurnAccepted EventType = "turn_accepted"

	// EventBackgroundTaskStart 表示 provider 托管的后台任务已经开始（claude 的 task_started 帧）。
	// ToolID 是发起它的工具调用：Bash 的 run_in_background 会立刻返回 tool_result，但命令仍在
	// 后台运行，所以这条工具消息不能按普通完成处理。
	EventBackgroundTaskStart EventType = "background_task_start"

	// EventBackgroundTaskEnd 表示 provider 托管的后台任务已经结束（claude 的 task_notification 帧）。
	// 可以只带 TaskID，由远端按 task id 回查发起它的工具；TaskStatus 原样带上 provider 上报的结束状态。
	EventBackgroundTaskEnd EventType = "background_task_end"
)

// Usage 记录令牌消耗量。
type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	CachedTokens int64 `json:"cached_tokens"`
	// ContextTokens 是本轮最后一次模型请求实际带入的上下文大小（输入 + 输出，tokens），只用于
	// 「上下文窗口占用」口径；InputTokens/OutputTokens 仍可能是整轮累计的计费用量，远超窗口上限。
	// provider 拿不到单次请求大小时留 0，界面回退到「输入 + 输出」。
	ContextTokens int64 `json:"context_tokens,omitempty"`
	DurationMs    int64 `json:"duration_ms,omitempty"` // 自本轮开始运行到上报该用量的耗时（毫秒），供界面计算输出速度
	// Requests 是本轮已完成的每次模型请求的速度样本，按完成顺序排列。它始终是本轮至今的完整
	// 列表而非增量；provider 拿不到时留空，界面回退到整轮耗时口径。
	Requests []RequestSpeed `json:"requests,omitempty"`
	// ContextWindow 是当前模型的上下文窗口上限（tokens），取自 CLI 的 result.modelUsage；
	// provider 拿不到时留 0，界面回退到自己维护的窗口表。
	ContextWindow int64 `json:"context_window,omitempty"`
	// CostUSD 是本轮（或整次会话）的费用（美元），取自 CLI 的 result.total_cost_usd；拿不到时留 0。
	CostUSD float64 `json:"cost_usd,omitempty"`
}

// Event 表示 Agent 后端派发给消费者的一条事件。
type Event struct {
	Type       EventType       `json:"type"`
	Text       string          `json:"text,omitempty"`
	ToolID     string          `json:"tool_id,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
	Output     string          `json:"output,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
	Blocked    bool            `json:"blocked,omitempty"` // 工具调用被 hook 拦截或改道，不是工具故障；仅在 IsError 为 true 时有意义
	Path       string          `json:"path,omitempty"`
	Patch      string          `json:"patch,omitempty"`
	SessionID  string          `json:"session_id,omitempty"`
	Usage      *Usage          `json:"usage,omitempty"`
	Err        string          `json:"err,omitempty"`
	ErrKind    string          `json:"err_kind,omitempty"` // EventError 的失败类别（见 ErrKind* 常量），判不准时留空
	StopReason string          `json:"stop_reason,omitempty"`

	// TaskID / TaskStatus 只用于后台任务事件：TaskID 是 provider 分配的后台任务 id；
	// TaskStatus 只在 EventBackgroundTaskEnd 上有意义，是 provider 原样上报的结束状态。
	TaskID     string `json:"task_id,omitempty"`
	TaskStatus string `json:"task_status,omitempty"`

	// CompactTokensBefore / CompactTokensAfter 是 EventContextCompacted 携带的压缩前后上下文大小
	// （tokens）。0 表示该 provider 没有上报这个数字（例如 codex 的通知里没有 token 数），
	// 界面按「未知」渲染，不能当成「0 tokens」。
	CompactTokensBefore int64 `json:"compact_tokens_before,omitempty"`
	CompactTokensAfter  int64 `json:"compact_tokens_after,omitempty"`
}

// EventError 的失败类别：用于区分「需要用户干预」的失败（未登录、限流）和普通接口故障。
// 取值来自 CLI 的结构化字段（assistant.error、result.terminal_reason、result.subtype、
// result.api_error_status），判不准时统一归为 ErrKindAPIError。
const (
	// ErrKindAuthRequired 未登录或凭证失效，需要用户在终端重新登录（claude /login）。
	ErrKindAuthRequired = "auth_required"
	// ErrKindAPIKeyRejected 第三方 API / API key 被上游拒绝（401/403），需要检查 key，而不是 claude /login。
	ErrKindAPIKeyRejected = "api_key_rejected"
	// ErrKindRateLimited 触发限流（HTTP 429 或 overloaded），稍后重发即可。
	ErrKindRateLimited = "rate_limited"
	// ErrKindContextExhausted 提示词或上下文超出模型窗口，需要压缩或裁剪。
	ErrKindContextExhausted = "context_exhausted"
	// ErrKindMaxTurns 达到单轮最大轮数上限。
	ErrKindMaxTurns = "max_turns"
	// ErrKindCancelled 本轮被用户或客户端中止（terminal_reason 为 aborted_*）。
	ErrKindCancelled = "cancelled"
	// ErrKindAPIError 其他接口/服务端失败，是判不出具体类别时的兜底。
	ErrKindAPIError = "api_error"
)

// Validate 校验事件字段是否完整合法。
func (e Event) Validate() error {
	switch e.Type {
	case EventText:
		if e.Text == "" {
			return errors.New("event text: text is required")
		}
	case EventTextRetract:
		if e.Text == "" {
			return errors.New("event text_retract: text is required")
		}
	case EventReasoning:
		if e.Text == "" {
			return errors.New("event reasoning: text is required")
		}
	case EventToolStart:
		if e.ToolID == "" || e.ToolName == "" {
			return errors.New("event tool_start: tool_id and tool_name are required")
		}
	case EventToolOutput:
		// tool_output 携带工具运行期间的增量输出片段（追加语义，不是全量替换），只用于实时展示；
		// 最终结果仍以 tool_end 的 output 为准。
		if e.ToolID == "" || e.Text == "" {
			return errors.New("event tool_output: tool_id and text are required")
		}
	case EventToolEnd:
		if e.ToolID == "" {
			return errors.New("event tool_end: tool_id is required")
		}
	case EventBackgroundTaskStart:
		if e.ToolID == "" {
			return fmt.Errorf("event %s: tool_id is required", e.Type)
		}
	case EventBackgroundTaskEnd:
		if e.ToolID == "" && e.TaskID == "" {
			return errors.New("event background_task_end: tool_id or task_id is required")
		}
	case EventFileChange:
		if e.Path == "" {
			return errors.New("event file_change: path is required")
		}
	case EventSession:
		if e.SessionID == "" {
			return errors.New("event session: session_id is required")
		}
	case EventUsage:
		if e.Usage == nil {
			return errors.New("event usage: usage is required")
		}
	case EventError:
		if e.Err == "" {
			return errors.New("event error: err is required")
		}
	case EventContextCompacted:
		// 压缩事件只作边界提示，token 数都可能未知（0），没有必填字段。
	case EventTurnAccepted:
		// 送达信号不带正文，也没有必填字段。
	case EventDone:
		// done 事件结束标记，无需额外必填字段
	default:
		return fmt.Errorf("unknown event type: %q", e.Type)
	}
	return nil
}
