package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"mcode/internal/openartifact"
)

// OpenArtifactCallFunc 定义转发给 open-artifact 桥接的调用函数签名。
type OpenArtifactCallFunc func(ctx context.Context, threadID, cwd, name string, args json.RawMessage) (json.RawMessage, error)

// RegisterTool 向内置 MCP 服务器注册一个工具定义。
func (s *Server) RegisterTool(tool ToolDefinition) {
	if s == nil || s.registry == nil {
		return
	}
	s.registry.Register(tool)
}

// UnregisterTool 从内置 MCP 服务器注销指定名称的工具。
func (s *Server) UnregisterTool(name string) {
	if s == nil || s.registry == nil {
		return
	}
	s.registry.Unregister(name)
}

// OpenArtifactToolDefinition 将 open-artifact 工具定义转换为 mcode 内置 MCP 的 ToolDefinition。
func OpenArtifactToolDefinition(def openartifact.ToolDefinition, call OpenArtifactCallFunc) ToolDefinition {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
	if len(def.InputSchema) > 0 {
		var parsed map[string]any
		if err := json.Unmarshal(def.InputSchema, &parsed); err == nil && parsed != nil {
			schema = parsed
		}
	}

	toolName := def.Name
	isWrite := toolName == "Artifact" || toolName == "ArtifactData"

	return ToolDefinition{
		Name:        toolName,
		Description: def.Description,
		InputSchema: schema,
		IsWrite:     isWrite,
		Handler: func(tc ToolContext, args map[string]any) ToolResult {
			var threadID string
			if tc.Context != nil {
				threadID = scopeThreadID(tc.Context)
			}
			if threadID == "" {
				return toolError(CodeForbidden, fmt.Sprintf("%s needs a session-scoped endpoint to identify the conversation", toolName), false)
			}
			if call == nil {
				return toolError(CodeInternal, "open-artifact 桥接不可用", false)
			}

			rawArgs, err := json.Marshal(args)
			if err != nil {
				return toolError(CodeInvalidArguments, "序列化工具参数失败: "+err.Error(), false)
			}
			raw, err := call(tc.Context, threadID, tc.WorkDir, toolName, rawArgs)
			if err != nil {
				return openArtifactRPCErrorResult(err)
			}
			return openArtifactRawResult(raw)
		},
	}
}

func openArtifactRPCErrorResult(err error) ToolResult {
	var rpcErr *openartifact.RPCError
	if errors.As(err, &rpcErr) && rpcErr != nil {
		code := rpcErr.DataCode
		if code == "" {
			code = CodeInternal
		}
		msg := rpcErr.Message
		if msg == "" {
			msg = rpcErr.Error()
		}
		return toolError(code, msg, false, rpcErr.Fix)
	}
	return toolError(CodeInternal, err.Error(), false)
}

func openArtifactRawResult(raw json.RawMessage) ToolResult {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ToolResult{Text: "{}", IsError: false}
	}
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(trimmed, &envelope); err == nil {
		errTrimmed := bytes.TrimSpace(envelope.Error)
		if len(errTrimmed) > 0 && !bytes.Equal(errTrimmed, []byte("null")) {
			return ToolResult{Text: string(trimmed), IsError: true}
		}
	}
	return ToolResult{Text: string(trimmed), IsError: false}
}
