package mcp

import (
	"fmt"
	"math"
)

// validateArguments 校验已声明字段的 JSON 类型，避免错误类型被 handler 当成“未提供”。
func validateArguments(tool ToolDefinition, args map[string]any) *ToolResult {
	properties, _ := tool.InputSchema["properties"].(map[string]any)
	for name, value := range args {
		raw, exists := properties[name]
		if tool.IsWrite && (name == "request_id" || name == "requestId" || name == "idempotency_key") {
			raw, exists = map[string]any{"type": "string"}, true
		}
		if !exists {
			result := toolError(CodeUnknownField, "Unknown argument: "+name, false)
			return &result
		}
		spec, _ := raw.(map[string]any)
		valid := true
		switch spec["type"] {
		case "string":
			_, valid = value.(string)
		case "boolean":
			_, valid = value.(bool)
		case "number":
			_, valid = value.(float64)
		case "integer":
			number, ok := value.(float64)
			valid = ok && math.Trunc(number) == number
		case "array":
			_, valid = value.([]any)
		case "object":
			_, valid = value.(map[string]any)
		}
		if !valid {
			result := toolError(CodeInvalidArguments, fmt.Sprintf("Argument '%s' must have type %s.", name, spec["type"]), false)
			return &result
		}
	}
	return nil
}
