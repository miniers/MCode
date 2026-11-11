package textgen

import (
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// truncateUTF8 把 s 截断到最多 maxBytes 字节，并且回退到完整的 UTF-8 字符边界，
// 避免中文字符被切成非法字节序列（乱码）。
func truncateUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// TitlePrompt 构造会话标题生成的提示词。
// 要求标题不超过 30 个字，不加标点和引号。
func TitlePrompt(firstUserMsg, firstReply string) string {
	return TitlePromptWithAttachments(firstUserMsg, firstReply, nil)
}

// TitlePromptWithAttachments 在 TitlePrompt 基础上感知附件：
// 用户文本为空但有附件时，以 "Task with attachments: a.png, b.go" 作为请求文本（仅取文件 base name）。
func TitlePromptWithAttachments(firstUserMsg, firstReply string, attachments []string) string {
	var sb strings.Builder
	sb.WriteString("请为下面的编程对话生成一个简明扼要的中文标题，便于用户日后快速识别与回顾。\n")
	sb.WriteString("规则：\n")
	sb.WriteString("- 严格控制在 30 个汉字以内\n")
	sb.WriteString("- 严禁包含任何标点符号（如逗号、句号、冒号、感叹号、问号等）\n")
	sb.WriteString("- 严禁包含任何引号、书名号或特殊符号（如 \" ' ` 《 》 【 】 等）\n")
	sb.WriteString("- 概括核心任务与操作对象，禁止照抄整句话，禁止输出“标题：”等前缀\n")
	sb.WriteString("- 严禁输出解释、说明或问句，直接输出标题本身\n\n")

	sb.WriteString("用户请求：\n")
	userMsg := strings.TrimSpace(firstUserMsg)
	if userMsg == "" {
		var names []string
		for _, a := range attachments {
			if n := filepath.Base(strings.TrimSpace(a)); n != "" && n != "." && n != "/" {
				names = append(names, n)
			}
		}
		if len(names) > 0 {
			userMsg = "Task with attachments: " + strings.Join(names, ", ")
		}
	}
	if len(userMsg) > 2000 {
		userMsg = truncateUTF8(userMsg, 2000)
	}
	sb.WriteString(userMsg)

	reply := strings.TrimSpace(firstReply)
	if reply != "" {
		sb.WriteString("\n\n助手答复：\n")
		if len(reply) > 2000 {
			reply = truncateUTF8(reply, 2000)
		}
		sb.WriteString(reply)
	}

	return sb.String()
}

// CommitMessagePrompt 构造 Git 提交信息的提示词。
// 要求第一行不超过 72 个字符，patch 截断到 12k 字节（回退到完整 UTF-8 字符边界）。
func CommitMessagePrompt(diffStat, diffPatch, instructions string) string {
	var sb strings.Builder
	sb.WriteString("请根据以下代码改动撰写符合规范的 Git 提交信息（Commit Message）。\n")
	sb.WriteString("规则：\n")
	sb.WriteString("- 第一行必须是简明扼要的主题行，使用祈使语气，不超过 72 个字符，末尾不加句号\n")
	sb.WriteString("- 若改动包含多项重要细节，可空一行后附带要点列表（使用 '-' 引导短句）\n")
	sb.WriteString("- 只输出提交信息正文，严禁使用代码块围栏（```）包裹，严禁包含多余解释\n")
	sb.WriteString("- 直接给出提交信息本身，不要输出思考过程、写作计划、自检或「最终输出如下」之类的引导语\n")

	inst := strings.TrimSpace(instructions)
	if inst != "" {
		sb.WriteString("\n用户补充规范：\n")
		sb.WriteString(inst)
		sb.WriteString("\n")
	}

	sb.WriteString("\n改动文件概览：\n")
	stat := strings.TrimSpace(diffStat)
	if len(stat) > 3000 {
		stat = truncateUTF8(stat, 3000)
	}
	sb.WriteString(stat)

	sb.WriteString("\n\nDiff 详情：\n")
	patch := strings.TrimSpace(diffPatch)
	if len(patch) > 12000 {
		patch = truncateUTF8(patch, 12000)
	}
	sb.WriteString(patch)

	return sb.String()
}

// MemoryCondensePrompt 构造记忆精简提示词。
// mergeWith 非空时是要合并进来的已有记忆全文，要求把两者合并为一条后精简。
func MemoryCondensePrompt(title, content, mergeWith string) string {
	var sb strings.Builder
	sb.WriteString("Condense the memory below into a compact, durable note.\n")
	sb.WriteString("Rules:\n")
	sb.WriteString("- Write the output in the same language as the input memory.\n")
	sb.WriteString("- Put the conclusion in the first sentence, then state the reason and when it applies.\n")
	sb.WriteString("- Keep the body under 500 characters when possible, and never exceed 1000 characters.\n")
	sb.WriteString("- Drop logs, long quotations, code blocks and step-by-step narration.\n")
	sb.WriteString("- Keep necessary identifiers and commands verbatim.\n")
	sb.WriteString("- The title is a single line of at most 40 characters.\n")
	sb.WriteString("- Output format is strict: first line `TITLE: <title>`, then one blank line, then the body.\n")
	sb.WriteString("  Do not add any other explanation and do not wrap the answer in code fences.\n")

	merged := strings.TrimSpace(mergeWith)
	if merged != "" {
		sb.WriteString("- The block labeled `Existing memory` is an existing memory. Merge it with the new\n")
		sb.WriteString("  memory into a single note, remove duplicates, and prefer the new memory on conflicts.\n")
	}

	sb.WriteString("\nNew memory title: ")
	sb.WriteString(truncateUTF8(strings.TrimSpace(title), 8000))
	sb.WriteString("\nNew memory body:\n")
	sb.WriteString(truncateUTF8(strings.TrimSpace(content), 8000))

	if merged != "" {
		sb.WriteString("\nExisting memory:\n")
		sb.WriteString(truncateUTF8(merged, 8000))
	}

	return sb.String()
}

// BranchNamePrompt 构造 Git 分支名生成的提示词。
func BranchNamePrompt(taskText string) string {
	var sb strings.Builder
	sb.WriteString("请为以下开发任务生成一个简短的 Git 分支名称 slug。\n")
	sb.WriteString("规则：\n")
	sb.WriteString("- 由 2 至 5 个英文单词组成，使用连字符（kebab-case）连接\n")
	sb.WriteString("- 仅使用小写英文字母（a-z）和数字（0-9），总长度不超过 40 个字符\n")
	sb.WriteString("- 不要带分支前缀（如 feature/ 或 mcode/）\n")
	sb.WriteString("- 严禁输出任何多余的标点、引号、解释或 Markdown\n\n")

	sb.WriteString("任务内容：\n")
	text := strings.TrimSpace(taskText)
	if len(text) > 2000 {
		text = truncateUTF8(text, 2000)
	}
	sb.WriteString(text)

	return sb.String()
}
