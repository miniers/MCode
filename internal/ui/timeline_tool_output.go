package mcodeui

import "strings"

// toolOutputPreviewMode 决定超长工具输出默认保留头部还是尾部。
type toolOutputPreviewMode int

const (
	// toolPreviewHead 保留头部（读文件、搜索）
	toolPreviewHead toolOutputPreviewMode = iota
	// toolPreviewTail 保留尾部（命令、编辑、未知工具）
	toolPreviewTail
)

// toolOutputPreview 把工具输出按 mode 截到最多 limit 行。
// 返回预览文本、尾部模式下被省略的前置行数，以及是否发生截断。
// limit <= 0 表示不截断，原样返回。
func toolOutputPreview(text string, mode toolOutputPreviewMode, limit int) (preview string, omitted int, truncated bool) {
	if limit <= 0 {
		return text, 0, false
	}
	lines := strings.Split(text, "\n")
	if len(lines) <= limit {
		return text, 0, false
	}
	if mode == toolPreviewTail {
		return strings.Join(lines[len(lines)-limit:], "\n"), len(lines) - limit, true
	}
	return strings.Join(lines[:limit], "\n"), 0, true
}

// toolOutputPrefersTail 判断这类工具的输出详情默认看尾部。
// 命令与编辑工具的报错、统计都在末尾；读文件与搜索从第一行才有用，保留头部；其余按尾部处理。
func toolOutputPrefersTail(m Message) bool {
	if isCommandTool(m) {
		return true
	}
	if isFileEditTool(m) {
		return true
	}
	switch classifyToolCategory(toolMessageName(m)) {
	case toolCategoryRead, toolCategorySearch:
		return false
	default:
		return true
	}
}

// toolOutputPreviewModeFor 把工具消息映射为预览模式。
func toolOutputPreviewModeFor(m Message) toolOutputPreviewMode {
	if toolOutputPrefersTail(m) {
		return toolPreviewTail
	}
	return toolPreviewHead
}
