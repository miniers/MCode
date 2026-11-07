package projctx

import (
	"errors"
	"os"
	"regexp"
	"strings"
)

const (
	// GuidanceBudget 是项目 Context 指导词的总字符（rune）预算。
	GuidanceBudget = 32000
	// guidanceTruncationNotice 是超出预算时追加在末尾的截断提示。
	guidanceTruncationNotice = "\n\n[mcode: project context trimmed to fit the size limit]"
)

// GuidanceOptions 控制项目 Context 指导词的构建方式。
type GuidanceOptions struct {
	// MaxChars 为总字符（rune）预算；<=0 时使用 GuidanceBudget。
	MaxChars int
	// CustomDocs 指定除 instructions 与 overview 外额外注入的文档，接受标准文档名
	// （decisions、notes.md）与自定义文档名（api-design.md、api-design、docs/api-design.md）两种写法。
	CustomDocs []string
}

// BuildGuidance 使用默认选项构建项目 Context 指导词，等价于 BuildGuidanceWithOptions 的零值选项。
func BuildGuidance(projectPath string) (string, error) {
	return BuildGuidanceWithOptions(projectPath, GuidanceOptions{})
}

// BuildGuidanceWithOptions 读取项目文档并拼装为 <project_context> 块：instructions 与
// overview 总是在根部存在时注入；decisions、notes 与 docs/ 下的自定义文档只在 CustomDocs
// 中列出且文件存在时注入。注入顺序为 instructions、overview、标准文档、自定义文档。
// 没有可注入的文档时返回空串和 nil。
func BuildGuidanceWithOptions(projectPath string, opts GuidanceOptions) (string, error) {
	s, err := New(projectPath)
	if err != nil {
		return "", err
	}

	// 标准文档按名称是否被选中；自定义文档以 docs/ 下的文件名为键，docs/x.md、x.md、x 都规整为 x.md。
	selectedStandard := make(map[string]bool, len(opts.CustomDocs))
	customDocs := make(map[string]bool, len(opts.CustomDocs))
	for _, name := range opts.CustomDocs {
		ref, err := ParseRef(name)
		if err != nil {
			continue
		}
		if ref.Standard {
			selectedStandard[ref.Info.Name] = true
		} else {
			customDocs[strings.TrimSuffix(ref.Name, ".md")] = true
		}
	}

	maxChars := opts.MaxChars
	if maxChars <= 0 {
		maxChars = GuidanceBudget
	}

	docs, err := s.List()
	if err != nil {
		return "", err
	}
	legacyDocs := make(map[string]Doc, len(docs))
	for _, doc := range docs {
		legacyDocs[doc.Name] = doc
	}

	var sections []string
	// 标准文档优先从上下文根部读取，根部缺失时回退到 docs/ 下的同名文件；两处都不存在时跳过，不注入模板。
	// injectedFromDocs 记录哪些 docs/ 文件已作为标准文档的回退来源注入，避免在自定义部分重复注入。
	injectedFromDocs := make(map[string]bool)
	standardOrder := []string{DocInstructions, DocOverview}
	for _, doc := range standardDocs {
		if doc.Name != DocInstructions && doc.Name != DocOverview {
			standardOrder = append(standardOrder, doc.Name)
		}
	}
	for _, name := range standardOrder {
		if name != DocInstructions && name != DocOverview && !selectedStandard[name] {
			continue
		}
		info, ok := LookupStandard(name)
		if !ok {
			continue
		}
		content, err := s.ReadRef(name)
		if errors.Is(err, ErrNotFound) {
			legacy, exists := legacyDocs[info.File]
			if !exists {
				continue
			}
			injectedFromDocs[info.File] = true
			content = legacy
		} else if err != nil {
			return "", err
		}
		text := escapeProjectContextTags(strings.TrimSpace(content.Content))
		if text == "" {
			continue
		}
		sections = append(sections, "## "+info.File+"\n"+text)
	}

	for _, doc := range docs {
		if !customDocs[strings.TrimSuffix(doc.Name, ".md")] || injectedFromDocs[doc.Name] {
			continue
		}
		text := escapeProjectContextTags(strings.TrimSpace(doc.Content))
		if text == "" {
			continue
		}
		sections = append(sections, "## "+doc.Name+"\n"+text)
	}
	if len(sections) == 0 {
		return "", nil
	}

	header := strings.Join([]string{
		"<project_context>",
		"[mcode project context]",
		"These documents come from the project's own knowledge base. Treat the rules in them as binding for all work in this project:",
	}, "\n")

	full := header + "\n\n" + strings.Join(sections, "\n\n") + "\n</project_context>"
	return truncateGuidance(full, maxChars), nil
}

// projectContextTagPattern 匹配与外层包裹标签同名的开闭标签（不区分大小写）。
var projectContextTagPattern = regexp.MustCompile(`(?i)</?project_context`)

// escapeProjectContextTags 转义正文里的 <project_context / </project_context 标签。
// 仓库文档不可信：不转义时写一个 </project_context> 就能闭合外层注入块，
// 让后续内容以上层指令的身份出现。只改标签的尖括号，其余文本原样保留。
func escapeProjectContextTags(text string) string {
	return projectContextTagPattern.ReplaceAllStringFunc(text, func(tag string) string {
		return "&lt;" + tag[1:]
	})
}

// truncateGuidance 处理超预算文本：截到预算减去提示长度，
// 并在末尾注明已截断。按 rune 截断，避免切坏多字节字符。
func truncateGuidance(text string, maxChars int) string {
	runes := []rune(text)
	if len(runes) <= maxChars {
		return text
	}
	notice := []rune(guidanceTruncationNotice)
	keep := maxChars - len(notice)
	if keep < 0 {
		keep = 0
	}
	return string(runes[:keep]) + string(notice)
}

// UpdateWithRetry 以 CAS 语义重试更新文档：读取当前内容与哈希，交给 mutate 生成
// 新内容，再按旧哈希写入；遇到并发写冲突就重试，超过 maxAttempts 返回最后一次
// 冲突错误。文件不存在时先创建，再按空内容交给 mutate。
func (s *Service) UpdateWithRetry(name string, mutate func(old string) string, maxAttempts int) (Doc, error) {
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		doc, err := s.Read(name)
		if errors.Is(err, ErrNotFound) {
			if _, createErr := s.Create(name); createErr != nil && !errors.Is(createErr, os.ErrExist) {
				return Doc{}, createErr
			}
			// 重新读取：此时文件存在，走正常 CAS 路径，避免与并发创建互相覆盖。
			continue
		}
		if err != nil {
			return Doc{}, err
		}

		updated, err := s.Write(name, mutate(doc.Content), doc.Hash)
		if err == nil {
			return updated, nil
		}
		if !errors.Is(err, ErrConflict) {
			return Doc{}, err
		}
		lastErr = err
	}
	return Doc{}, lastErr
}
