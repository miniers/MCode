package mcp

import "encoding/json"

// MCP 工具错误码。
const (
	CodeInvalidArguments  = "invalid_arguments"
	CodeUnknownField      = "unknown_field"
	CodeReadOnly          = "read_only"
	CodeNotFound          = "not_found"
	CodeForbidden         = "forbidden"
	CodeSelfTarget        = "self_target"
	CodeRequestIDConflict = "request_id_conflict"
	CodeRequestInProgress = "request_in_progress"
	CodeAlreadySettled    = "already_settled"
	CodePartialFailure    = "partial_failure"
	CodeInternal          = "internal"
	CodeIntegrityError    = "integrity_error"
	CodeConflict          = "conflict"
)

// ToolErrorPayload 描述 MCP 业务错误载荷。
type ToolErrorPayload struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	Hint      string `json:"hint,omitempty"`
}

// ToolResult 描述工具调用的执行结果。
type ToolResult struct {
	Text    string
	IsError bool
}

func toolError(code, message string, retryable bool, hint ...string) ToolResult {
	payload := ToolErrorPayload{
		Code:      code,
		Message:   message,
		Retryable: retryable,
	}
	if len(hint) > 0 && hint[0] != "" {
		payload.Hint = hint[0]
	}
	b, _ := json.Marshal(payload)
	return ToolResult{
		Text:    string(b),
		IsError: true,
	}
}

func toolSuccess(data any) ToolResult {
	switch v := data.(type) {
	case string:
		return ToolResult{Text: v, IsError: false}
	default:
		b, _ := json.Marshal(v)
		return ToolResult{Text: string(b), IsError: false}
	}
}

// toolErrorCode 提取错误结果载荷中的业务错误码，无法解析时返回空串。
func toolErrorCode(res ToolResult) string {
	if !res.IsError {
		return ""
	}
	var payload ToolErrorPayload
	if err := json.Unmarshal([]byte(res.Text), &payload); err != nil {
		return ""
	}
	return payload.Code
}
