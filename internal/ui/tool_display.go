package mcodeui

import (
	"encoding/json"
	"strings"
)

var (
	// toolDisplayCommandKeys 为命令类输入字段的候选键。
	toolDisplayCommandKeys = []string{"command", "cmd", "CommandLine", "command_line", "commandline", "script"}
	// toolDisplayPathKeys 为路径类输入字段的候选键。
	toolDisplayPathKeys = []string{"file_path", "filePath", "path", "notebook_path"}
	// toolDisplayTextKeys 为文本类输入字段的候选键（搜索词、URL、描述等）。
	toolDisplayTextKeys = []string{"pattern", "query", "glob", "url", "description", "prompt"}
)

// toolDisplayFieldsFromText 从工具文本「工具名 {JSON}\n输出」中解析输入字段。
// JSON 可能被截断或后接执行输出：优先整体解码首个 JSON 对象，失败时回退到按已知键提取字符串值。
func toolDisplayFieldsFromText(text string) map[string]any {
	firstLine := text
	if idx := strings.IndexByte(firstLine, '\n'); idx != -1 {
		firstLine = firstLine[:idx]
	}
	idx := strings.IndexByte(firstLine, '{')
	if idx == -1 {
		return nil
	}
	var fields map[string]any
	if err := json.NewDecoder(strings.NewReader(text[idx:])).Decode(&fields); err == nil && len(fields) > 0 {
		return fields
	}
	return toolDisplayFallbackFields(text[idx:])
}

// toolDisplayFallbackFields 在 JSON 被截断无法解码时，按已知键提取字符串值。
// 键名大小写不敏感（“Path”“FILE_PATH”与“file_path”等价），返回的 map 键统一为候选列表里的规范键名，
// 调用方可直接用同一组键取值。值从键名后的引号之后读取，直到下一个未转义的引号或文本结尾，并反转义常见转义序列。
func toolDisplayFallbackFields(text string) map[string]any {
	fields := make(map[string]any)
	keys := make([]string, 0, len(toolDisplayCommandKeys)+len(toolDisplayPathKeys)+len(toolDisplayTextKeys))
	keys = append(keys, toolDisplayCommandKeys...)
	keys = append(keys, toolDisplayPathKeys...)
	keys = append(keys, toolDisplayTextKeys...)
	// 只对整段文本做一次 ASCII 小写化：ASCII 大小写转换不改变字节长度，下标可直接回原文取值。
	lowerText := asciiLower(text)
	for _, key := range keys {
		if val, ok := toolDisplayRawStringValue(text, lowerText, key); ok {
			fields[key] = val
		}
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

// asciiLower 仅把 ASCII 大写字母转成小写，其余字节原样保留，保证与输入等长、下标一一对应。
func asciiLower(s string) string {
	b := []byte(s)
	for i := 0; i < len(b); i++ {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// toolDisplayRawStringValue 在 JSON 文本中查找 "key": "..." 的字符串值，键名大小写不敏感。
// text 为原文，lowerText 为其 ASCII 小写化结果（用于定位，长度与 text 相同）。
// 引号未闭合（输入被截断）时返回截至文本结尾的内容。
func toolDisplayRawStringValue(text, lowerText, key string) (string, bool) {
	marker := `"` + asciiLower(key) + `":"`
	idx := strings.Index(lowerText, marker)
	if idx == -1 {
		marker = `"` + asciiLower(key) + `": "`
		if idx = strings.Index(lowerText, marker); idx == -1 {
			return "", false
		}
	}
	rest := text[idx+len(marker):]
	var b strings.Builder
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		switch {
		case c == '\\' && i+1 < len(rest):
			i++
			switch rest[i] {
			case 'n', 'r', 't':
				b.WriteByte(' ')
			default:
				b.WriteByte(rest[i])
			}
		case c == '"':
			val := strings.TrimSpace(b.String())
			return val, val != ""
		default:
			b.WriteByte(c)
		}
	}
	val := strings.TrimSpace(b.String())
	return val, val != ""
}
