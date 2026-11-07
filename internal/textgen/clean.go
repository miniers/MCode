package textgen

import (
	"strings"
	"unicode"
)

// punctuationToTrim 标题前后需要去除的标点符号与引号集合。
const punctuationToTrim = "\"'`*#.。,，;；:：!！?？-—_ “”‘’《》【】()（）"

// invalidTitleOpeners 标题开头的无效回复前缀。
var invalidTitleOpeners = []string{
	"i ", "i'", "sorry", "sure", "here", "please", "could you", "can you",
	"what ", "it seems", "it looks", "unfortunately", "there is no",
	"not enough", "need more", "maybe ", "the request", "this request",
	"抱歉", "对不起", "好的", "请问", "需要更多", "作为一个ai", "作为人工智能",
}

// invalidTitlePhrases 包含这些短语的回复不视为有效标题。
var invalidTitlePhrases = []string{
	"more context", "meaningful title", "as an ai", "cannot", "can't", "unable to",
	"更多上下文", "无法生成", "作为人工智能", "作为语言模型",
}

// CleanTitle 清洗模型输出为合规的会话标题。
// 去除引号、标点、前缀，取第一行非空，限制在 30 个字符以内。
// 若输出包含废话或无意义句子，返回空字符串。
func CleanTitle(raw string) string {
	lines := strings.Split(raw, "\n")
	var candidate string
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed != "" {
			candidate = trimmed
			break
		}
	}
	if candidate == "" {
		return ""
	}

	candidate = strings.Trim(candidate, punctuationToTrim)

	prefixes := []string{"title:", "title：", "标题:", "标题：", "建议标题:", "建议标题："}
	for _, p := range prefixes {
		if strings.HasPrefix(strings.ToLower(candidate), p) {
			candidate = strings.Trim(candidate[len(p):], punctuationToTrim)
			break
		}
	}

	if candidate == "" {
		return ""
	}

	lower := strings.ToLower(candidate)
	for _, op := range invalidTitleOpeners {
		if strings.HasPrefix(lower, op) {
			return ""
		}
	}
	for _, ph := range invalidTitlePhrases {
		if strings.Contains(lower, ph) {
			return ""
		}
	}

	// 截断到 30 个字符（按 unicode rune 计数）
	runes := []rune(candidate)
	if len(runes) > 30 {
		runes = runes[:30]
	}
	res := strings.Trim(string(runes), punctuationToTrim)
	return res
}

// CleanCommitMessage 清洗模型输出为合规的 Git 提交信息。
// 移除代码围栏与多余的首尾空白。
func CleanCommitMessage(raw string) string {
	cleaned := strings.TrimSpace(raw)
	// 去掉代码围栏 ```git, ```等
	lines := strings.Split(cleaned, "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			continue
		}
		filtered = append(filtered, line)
	}
	result := strings.TrimSpace(strings.Join(filtered, "\n"))
	return result
}

// stripCodeFences 去掉文本中的代码围栏行（如 ```、```markdown）并去除首尾空白。
func stripCodeFences(raw string) string {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// ParseMemoryCondense 解析记忆精简模型的输出，返回标题与正文。
// 第一行以 TITLE: 开头（大小写不敏感，允许全角冒号）时取其后为标题，剩余部分为正文；
// 没有该行时标题为空、全部内容都归正文。代码围栏会被去掉。
func ParseMemoryCondense(raw string) (title, content string) {
	cleaned := stripCodeFences(raw)
	if cleaned == "" {
		return "", ""
	}
	lines := strings.Split(cleaned, "\n")
	first := strings.TrimSpace(lines[0])
	lower := strings.ToLower(first)
	var rest string
	switch {
	case strings.HasPrefix(lower, "title:"):
		rest = first[len("title:"):]
	case strings.HasPrefix(lower, "title："):
		rest = first[len("title："):]
	default:
		return "", cleaned
	}
	title = strings.Trim(strings.TrimSpace(rest), "`\"'“”‘’")
	content = strings.TrimSpace(strings.Join(lines[1:], "\n"))
	return title, content
}

// CleanBranchName 清洗模型输出为合规的 Git 分支名称 slug。
func CleanBranchName(raw string) string {
	lines := strings.Split(raw, "\n")
	var candidate string
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed != "" {
			candidate = trimmed
			break
		}
	}
	if candidate == "" {
		return ""
	}

	candidate = strings.ToLower(strings.Trim(candidate, "\"'`*#. -_"))
	// 先剥除本地分支前缀，避免 slug 里残留 mcode-。
	if strings.HasPrefix(candidate, BranchPrefix) {
		candidate = strings.TrimPrefix(candidate, BranchPrefix)
	}

	var sb strings.Builder
	lastHyphen := false
	for _, r := range candidate {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
			lastHyphen = false
		} else if r == '-' || r == '_' || unicode.IsSpace(r) {
			if !lastHyphen && sb.Len() > 0 {
				sb.WriteByte('-')
				lastHyphen = true
			}
		}
	}

	slug := strings.Trim(sb.String(), "-")
	runes := []rune(slug)
	if len(runes) > 40 {
		runes = runes[:40]
	}
	return strings.Trim(string(runes), "-")
}
