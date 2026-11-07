package mcp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// entryActionHelp 为入口工具用于查看操作与参数的 action 名。
const entryActionHelp = "help"

// toolFamily 描述一个入口工具族：普通会话只看到族入口，内部类型化工具按 action 转发。
//
// 入口工具只做两件事：action=help 返回操作清单，其余 action 在 handleToolCall 里改写成
// 对应类型化工具的调用。改写后参数校验、项目校验、能力裁剪、幂等与写串行化全部照旧生效，
// 因此族内工具可以继续按最细粒度实现，只有声明给模型的那一层收敛成一个工具。
type toolFamily struct {
	// entry 是入口工具名，如 kanban。
	entry string
	// prefix 是内部工具名前缀，如 kanban_。
	prefix string
	// description 是入口工具的说明，写给模型看。
	description string
	// capability 是入口工具的能力标签，供 tools/list 的按项目裁剪使用。
	capability string
	// overview 是 action=help 概览的首行。
	overview string
	// exclude 是名字带本族前缀却不属于本族的工具，例如总管专属工具。
	exclude map[string]bool
	// aliases 是显式的 action → 工具名映射，用于不合前缀规律的族内工具，
	// 例如记忆族里名字不带 memory_ 前缀的 thread_search。
	aliases map[string]string
}

// toolFamilies 是全部入口工具族，顺序与注册顺序一致。
var toolFamilies = []*toolFamily{kanbanFamily, automationFamily, memoryFamily}

// actionOf 把内部工具名还原成入口操作名。
func (f *toolFamily) actionOf(toolName string) (string, bool) {
	for action, name := range f.aliases {
		if name == toolName {
			return action, true
		}
	}
	if !strings.HasPrefix(toolName, f.prefix) || f.exclude[toolName] {
		return "", false
	}
	return strings.TrimPrefix(toolName, f.prefix), true
}

// toolName 把操作名还原成内部工具名；不属于本族时返回 false。
func (f *toolFamily) toolName(action string) (string, bool) {
	if name, ok := f.aliases[action]; ok {
		return name, true
	}
	name := f.prefix + action
	if _, deleted := f.exclude[name]; deleted {
		return "", false
	}
	return name, true
}

// hint 返回模型用错 action 时的下一步提示。
func (f *toolFamily) hint() string {
	return "Call this tool with action=help to see the available actions and their arguments."
}

// entryFamilyOf 按入口工具名查找族；不是入口工具时返回 nil。
func entryFamilyOf(toolName string) *toolFamily {
	for _, f := range toolFamilies {
		if f.entry == toolName {
			return f
		}
	}
	return nil
}

// entryFamilyByTool 按内部工具名查找所属族；不属于任何族时返回 nil。
func entryFamilyByTool(toolName string) *toolFamily {
	for _, f := range toolFamilies {
		if _, ok := f.actionOf(toolName); ok {
			return f
		}
	}
	return nil
}

// entryActions 按注册顺序返回本族的操作名；allowed 非 nil 时再过滤一次。
// 只有已注册的工具才算操作，exclude 与未注册的总管专属工具都不会出现在 enum 里。
func (r *Registry) entryActions(f *toolFamily, allowed func(toolName string) bool) []string {
	names := make([]string, 0, len(r.order))
	for _, name := range r.order {
		action, ok := f.actionOf(name)
		if !ok {
			continue
		}
		if allowed != nil && !allowed(name) {
			continue
		}
		names = append(names, action)
	}
	return names
}

// definition 生成入口工具定义；allowed 非 nil 时只列出通过过滤的操作。
// tools/list 会在未接入检索能力时用它重算 enum，避免暴露必然失败的 action。
func (f *toolFamily) definition(r *Registry, allowed func(toolName string) bool) ToolDefinition {
	actions := r.entryActions(f, allowed)
	enum := make([]string, 0, len(actions)+1)
	enum = append(enum, actions...)
	enum = append(enum, entryActionHelp)

	return ToolDefinition{
		Name:        f.entry,
		Description: f.description,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        enum,
					"description": "要执行的操作名（族内工具名去掉前缀），或用 help 查看全部操作。",
				},
				"args": map[string]any{
					"type":        "object",
					"description": "该操作的参数对象，可省略；字段含义用 action=help 加 args.action 查询。",
				},
			},
			"required":             []string{"action"},
			"additionalProperties": false,
		},
		IsWrite:    false,
		Capability: f.capability,
		Handler:    func(tc ToolContext, args map[string]any) ToolResult { return f.handleHelp(tc, args) },
	}
}

// register 在全部族内工具注册之后注册入口工具，enum 才能从已注册工具名收集。
func (f *toolFamily) register(r *Registry) {
	r.Register(f.definition(r, nil))
}

// rewriteEntryCall 把入口调用改写成内部类型化工具的调用。
// action=help 原样返回，交给入口工具自己的 Handler 处理。
func (s *Server) rewriteEntryCall(f *toolFamily, rawArgs json.RawMessage) (string, json.RawMessage, *ToolResult) {
	call := map[string]any{}
	if len(rawArgs) > 0 {
		if err := json.Unmarshal(rawArgs, &call); err != nil {
			res := toolError(CodeInvalidArguments, "Arguments must be a valid JSON object.", false)
			return "", nil, &res
		}
	}

	action, _ := call["action"].(string)
	action = strings.TrimSpace(action)
	if action == entryActionHelp {
		return f.entry, rawArgs, nil
	}
	if action == "" {
		res := toolError(CodeNotFound, "Unknown action: missing 'action'.", false, f.hint())
		return "", nil, &res
	}

	toolName, isAction := f.toolName(action)
	if !isAction {
		res := toolError(CodeNotFound, fmt.Sprintf("Unknown action '%s'.", action), false, f.hint())
		return "", nil, &res
	}
	if _, ok := s.registry.Get(toolName); !ok {
		res := toolError(CodeNotFound, fmt.Sprintf("Unknown action '%s'.", action), false, f.hint())
		return "", nil, &res
	}

	innerArgs := map[string]any{}
	if raw, exists := call["args"]; exists && raw != nil {
		parsed, isObject := raw.(map[string]any)
		if !isObject {
			res := toolError(CodeInvalidArguments, "Argument 'args' must be a JSON object.", false)
			return "", nil, &res
		}
		innerArgs = parsed
	}
	encoded, err := json.Marshal(innerArgs)
	if err != nil {
		res := toolError(CodeInternal, "Failed to convert entry arguments: "+err.Error(), false)
		return "", nil, &res
	}
	return toolName, encoded, nil
}

// handleHelp 只处理 action=help；其余 action 已在 handleToolCall 里改写成内部工具调用。
func (f *toolFamily) handleHelp(tc ToolContext, args map[string]any) ToolResult {
	action, _ := args["action"].(string)
	action = strings.TrimSpace(action)
	if action != entryActionHelp {
		return toolError(CodeInvalidArguments, fmt.Sprintf("Unknown action '%s'.", action), false, f.hint())
	}

	inner, _ := args["args"].(map[string]any)
	subject, _ := inner["action"].(string)
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return toolSuccess(f.overviewText(tc.Server.registry))
	}

	name, isAction := f.toolName(subject)
	if !isAction {
		return toolError(CodeNotFound, fmt.Sprintf("Unknown action '%s'.", subject), false, f.hint())
	}
	tool, ok := tc.Server.registry.Get(name)
	if !ok {
		return toolError(CodeNotFound, fmt.Sprintf("Unknown action '%s'.", subject), false, f.hint())
	}
	return toolSuccess(f.detail(tool))
}

// overviewText 返回每个操作一行“操作名：description 第一句”的清单。
func (f *toolFamily) overviewText(r *Registry) string {
	var b strings.Builder
	b.WriteString(f.overview)
	b.WriteString("\n\n")
	for _, name := range r.order {
		action, isAction := f.actionOf(name)
		if !isAction {
			continue
		}
		tool, ok := r.Get(name)
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "%s：%s\n", action, firstSentence(tool.Description))
	}
	fmt.Fprintf(&b, "\n查看单个操作的完整说明与参数：{\"action\":\"help\",\"args\":{\"action\":\"%s\"}}", firstActionOf(f, r))
	return b.String()
}

// detail 返回单个操作的完整说明与参数的紧凑文字说明。
func (f *toolFamily) detail(tool ToolDefinition) string {
	action, _ := f.actionOf(tool.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "%s：%s\n\n参数：\n", action, tool.Description)

	properties, _ := tool.InputSchema["properties"].(map[string]any)
	required := map[string]bool{}
	switch list := tool.InputSchema["required"].(type) {
	case []string:
		for _, name := range list {
			required[name] = true
		}
	case []any:
		for _, item := range list {
			if name, ok := item.(string); ok {
				required[name] = true
			}
		}
	}

	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		b.WriteString("- 无参数\n")
		return b.String()
	}
	for _, name := range names {
		spec, _ := properties[name].(map[string]any)
		typ, _ := spec["type"].(string)
		if strings.TrimSpace(typ) == "" {
			typ = "any"
		}
		duty := "可选"
		if required[name] {
			duty = "必填"
		}
		fmt.Fprintf(&b, "- %s（%s，%s）", name, typ, duty)
		if desc, _ := spec["description"].(string); strings.TrimSpace(desc) != "" {
			b.WriteString("：" + strings.TrimSpace(desc))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// firstActionOf 返回族内第一个操作名，供 help 示例使用；族为空时返回 help 本身。
func firstActionOf(f *toolFamily, r *Registry) string {
	for _, name := range r.order {
		if action, ok := f.actionOf(name); ok {
			return action
		}
	}
	return entryActionHelp
}

// firstSentence 截取描述的第一句，供入口工具的操作清单使用。
func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if idx := strings.Index(s, ". "); idx >= 0 {
		return s[:idx+1]
	}
	if idx := strings.Index(s, "。"); idx >= 0 {
		return s[:idx+len("。")]
	}
	return s
}
