package mcodeui

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 会话导出（/export 命令）：把会话元数据与消息写成 Markdown、HTML 或 JSON 文件。
// 文件名清洗、正文构建、写盘彼此独立；exportThread 负责取历史与弹保存对话框。

// threadExportFileName 把会话标题清洗成文件名：
// 去掉 / \ : * ? " < > | 与换行等非法字符、去掉首尾空白，最长 80 个字符，
// 清洗后为空时回退为 "mcode-会话"，最后拼上扩展名（".md"、".html" 或 ".json"）。
func threadExportFileName(title string, ext string) string {
	var b strings.Builder
	for _, r := range title {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', '\n', '\r':
			continue
		}
		b.WriteRune(r)
	}
	name := strings.TrimSpace(b.String())
	if runes := []rune(name); len(runes) > 80 {
		name = string(runes[:80])
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "mcode-会话"
	}
	return name + ext
}

// threadExportTime 以 2006-01-02 15:04:05 的本地时间格式输出时间。
func threadExportTime(t time.Time) string {
	return t.Format("2006-01-02 15:04:05")
}

// threadExportRoleHeading 按角色给出 Markdown 小节标题。
func threadExportRoleHeading(role string) string {
	switch role {
	case "user":
		return "## 用户"
	case "assistant":
		return "## 助手"
	default:
		return "## " + role
	}
}

// threadExportShouldCollapse 判断消息是否属于工具类或推理类，需要在导出时折叠。
func threadExportShouldCollapse(m Message) bool {
	switch {
	case m.Kind == "tool", m.Kind == "reasoning", m.Role == "tool", m.Role == "reasoning":
		return true
	}
	return false
}

// threadExportFence 计算包裹 text 所需的反引号围栏：
// 长度为正文中最长连续反引号数 +1，至少 3 个。
func threadExportFence(text string) string {
	longest, run := 0, 0
	for _, r := range text {
		if r == '`' {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	n := longest + 1
	if n < 3 {
		n = 3
	}
	return strings.Repeat("`", n)
}

// threadExportDetailsBlock 用折叠块包裹工具/推理类正文，正文放在围栏代码块中。
func threadExportDetailsBlock(text string) string {
	fence := threadExportFence(text)
	body := text
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return "<details><summary>工具/推理</summary>\n\n" + fence + "\n" + body + fence + "\n\n</details>"
}

// threadExportMediaLabel 给出附件的展示名，优先名称，其次 MIME。
func threadExportMediaLabel(media MessageMedia) string {
	if media.Name != "" {
		return media.Name
	}
	if media.MimeType != "" {
		return media.MimeType
	}
	return "未命名附件"
}

// threadExportMessageSection 渲染单条消息的小节（标题、正文、附件列表）。
func threadExportMessageSection(m Message) string {
	if isPlanDecisionMessage(m) {
		if plan, ok := parsePlanDecisionMessage(m); ok {
			return resolvedPlanExport(plan)
		}
		return ""
	}
	parts := []string{threadExportRoleHeading(m.Role)}
	if m.Text != "" {
		if threadExportShouldCollapse(m) {
			parts = append(parts, threadExportDetailsBlock(m.Text))
		} else {
			parts = append(parts, m.Text)
		}
	}
	if len(m.Media) > 0 {
		lines := make([]string, 0, len(m.Media))
		for _, media := range m.Media {
			lines = append(lines, "- 附件："+threadExportMediaLabel(media))
		}
		parts = append(parts, strings.Join(lines, "\n"))
	}
	return strings.Join(parts, "\n\n")
}

// buildThreadExportMarkdown 把会话元数据与全部消息渲染为 Markdown 文本。
// 正文为空且没有附件的消息会被跳过。
func buildThreadExportMarkdown(t Thread, msgs []Message, exportedAt time.Time) string {
	var b strings.Builder
	b.WriteString(threadExportHeader(t, exportedAt))

	for _, m := range msgs {
		if m.Text == "" && len(m.Media) == 0 {
			continue
		}
		// 问答记录消息的正文是前端用的 JSON，Markdown 导出里没有可读内容：
		// 跳过它，完整的原始记录仍保留在 JSON 导出里。
		if isQuestionAnswerMessage(m) {
			continue
		}
		b.WriteString("\n")
		b.WriteString(threadExportMessageSection(m))
		b.WriteString("\n")
	}
	return b.String()
}

// threadExportHeader 渲染导出开头的标题与元信息（不含消息正文），
// 完整导出与预算版导出共用它，保证两种输出的开头一致。
func threadExportHeader(t Thread, exportedAt time.Time) string {
	var b strings.Builder
	b.WriteString("# " + t.Title + "\n\n")
	meta := []string{
		"- 会话 ID：" + t.ID,
		"- Provider：" + t.Provider,
		"- Model：" + t.Model,
		"- 更新时间：" + threadExportTime(t.UpdatedAt),
		"- 导出时间：" + threadExportTime(exportedAt),
	}
	b.WriteString(strings.Join(meta, "\n"))
	b.WriteString("\n")
	return b.String()
}

// 导出长度预算与逐级降级参数：
//   - threadExportBudgetChars 是 Markdown 存文件 / HTML / 复制到剪贴板的上限，超出就逐级降级；
//   - threadExportRecentTurns 是保留「不额外降级」的最近回合数，更老的回合先降一级；
//   - threadExportToolTextCap 是降级等级 1 时工具/推理正文的最大字符数；
//   - threadExportThinkingKeep 是降级等级 4 时思考正文头尾各保留的字符数。
const (
	threadExportBudgetChars  = 150_000
	threadExportRecentTurns  = 4
	threadExportToolTextCap  = 4000
	threadExportThinkingKeep = 800
	// threadExportLevelMax 是降级等级上限：0 原样、1 截断工具输出、2 丢弃终端输出、
	// 3 工具调用折叠成计数、4 思考只保留头尾。
	threadExportLevelMax = 4
)

// threadExportDegradeNote 是超预算导出时追加在元信息后的降级说明行。
const threadExportDegradeNote = "> 本会话过长，导出已自动降级：工具输出截断 → 丢弃终端输出 → 工具调用折叠成计数 → 思考只保留头尾，较早回合先降级。\n"

// threadExportIsTerminalOutput 判断消息是不是终端命令输出（bash / shell 一类工具的结果），
// 这类内容体量最大、可读价值最低，降级等级 2 时整条丢弃。
func threadExportIsTerminalOutput(m Message) bool {
	if !threadExportShouldCollapse(m) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(m.ToolName)) {
	case "bash", "shell", "sh", "zsh", "terminal", "exec", "command", "run_command":
		return true
	}
	return strings.EqualFold(m.Kind, "terminal") || strings.EqualFold(m.Role, "terminal")
}

// threadExportTruncateText 把正文截到 limit 个字符（按 rune 计），并附一行说明。
func threadExportTruncateText(text string, limit int) string {
	if limit <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "\n…（已截断）"
}

// threadExportKeepHeadAndTail 只保留正文头尾各 keep 个字符，中间折叠成一行说明；
// 正文不超过 2*keep（本来就短）时原样返回。
func threadExportKeepHeadAndTail(text string, keep int) string {
	if keep <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= 2*keep {
		return text
	}
	head := string(runes[:keep])
	tail := string(runes[len(runes)-keep:])
	return head + "\n…（已省略中间内容）…\n" + tail
}

// threadExportIsReasoning 判断消息是不是模型的思考内容（推理过程）。
// 思考不属于「工具调用」，所以最高降级等级才把它压成头尾，而不是折叠成计数。
func threadExportIsReasoning(m Message) bool {
	return m.Kind == "reasoning" || m.Role == "reasoning"
}

// threadExportMessageSectionAt 按降级等级渲染单条消息的小节：
// 用户与助手的正文在任何等级都保留原文，只有工具 / 推理类内容递归降级。
// 返回空串表示这条消息在该等级下被整条丢弃。
func threadExportMessageSectionAt(m Message, level int) string {
	if isPlanDecisionMessage(m) {
		return threadExportMessageSection(m)
	}
	if level <= 0 || !threadExportShouldCollapse(m) || m.Text == "" {
		return threadExportMessageSection(m)
	}
	if level >= 3 && !threadExportIsReasoning(m) {
		// 工具调用塌缩成计数：由调用方统计连续条数并写一行汇总，这里不单独输出小节。
		return ""
	}
	if level >= 2 && threadExportIsTerminalOutput(m) {
		return ""
	}
	text := m.Text
	if level >= threadExportLevelMax && (m.Kind == "reasoning" || m.Role == "reasoning") {
		text = threadExportKeepHeadAndTail(text, threadExportThinkingKeep)
	} else {
		text = threadExportTruncateText(text, threadExportToolTextCap)
	}
	trimmed := m
	trimmed.Text = text
	return threadExportMessageSection(trimmed)
}

// threadExportSameTurn 判断两条消息是否属于同一个回合；缺少回合信息时按消息各自成回合。
func threadExportSameTurn(a, b Message) bool {
	if a.TurnID != "" || b.TurnID != "" {
		return a.TurnID == b.TurnID
	}
	return a.TurnIndex == b.TurnIndex
}

// threadExportLevels 按长度预算给出每条消息的降级等级（纯函数，budget <= 0 表示不降级）：
// 先按回合统计正文长度选择全局等级，再让最近 threadExportRecentTurns 个回合用全局等级、
// 更老的回合高一级，这样预算紧张时总是老回合先降级。
func threadExportLevels(msgs []Message, budget int) []int {
	levels := make([]int, len(msgs))
	if budget <= 0 || len(msgs) == 0 {
		return levels
	}

	// 回合边界：回合号或回合 ID 变化即视为新回合。
	turnOf := make([]int, len(msgs))
	turn := 0
	for i := range msgs {
		if i > 0 && !threadExportSameTurn(msgs[i-1], msgs[i]) {
			turn++
		}
		turnOf[i] = turn
	}
	turnCount := turn + 1
	recentFrom := max(0, turnCount-threadExportRecentTurns)

	plain := 0
	for _, m := range msgs {
		plain += len([]rune(threadExportMessageSectionAt(m, 0)))
	}
	if plain <= budget {
		// 预算充足：全部不降级，输出与完整导出逐字符一致。
		return levels
	}

	total := func(global int) int {
		sum := 0
		for i, m := range msgs {
			level := global
			if turnOf[i] < recentFrom {
				level = min(global+1, threadExportLevelMax)
			}
			sum += len([]rune(threadExportMessageSectionAt(m, level)))
		}
		return sum
	}

	global := 0
	for global < threadExportLevelMax && total(global) > budget {
		global++
	}
	for i := range levels {
		levels[i] = global
		if turnOf[i] < recentFrom {
			levels[i] = min(global+1, threadExportLevelMax)
		}
	}
	return levels
}

// buildThreadExportMarkdownBudgeted 按长度预算渲染 Markdown 导出，第二个返回值报告是否发生降级：
// budget <= 0 或正文没超预算时直接复用 buildThreadExportMarkdown，输出逐字符一致；
// 超预算时按 threadExportLevels 逐条降级，在元信息后追加降级说明行，仍然超出预算时在末尾硬截断，
// 保证结果长度不超过预算。用户与助手的正文永远保留原文。
func buildThreadExportMarkdownBudgeted(t Thread, msgs []Message, exportedAt time.Time, budget int) (string, bool) {
	full := buildThreadExportMarkdown(t, msgs, exportedAt)
	if budget <= 0 || len([]rune(full)) <= budget {
		return full, false
	}

	levels := threadExportLevels(msgs, budget)
	var b strings.Builder
	b.WriteString(threadExportHeader(t, exportedAt))
	b.WriteString(threadExportDegradeNote)

	omitted := 0
	flushOmitted := func() {
		if omitted == 0 {
			return
		}
		b.WriteString("\n> 已省略 ")
		b.WriteString(fmt.Sprintf("%d", omitted))
		b.WriteString(" 条工具调用记录\n")
		omitted = 0
	}
	for i, m := range msgs {
		if m.Text == "" && len(m.Media) == 0 {
			continue
		}
		if isQuestionAnswerMessage(m) {
			continue
		}
		if levels[i] >= 3 && threadExportShouldCollapse(m) && !threadExportIsReasoning(m) {
			omitted++
			continue
		}
		section := threadExportMessageSectionAt(m, levels[i])
		if section == "" {
			continue
		}
		flushOmitted()
		b.WriteString("\n")
		b.WriteString(section)
		b.WriteString("\n")
	}
	flushOmitted()

	out := b.String()
	if runes := []rune(out); len(runes) > budget {
		// 极端情况下（正文本身就超预算）只能硬截断，仍要守住预算：
		// 截断标记的长度也算在预算里，预算太小时干脆不留标记。
		marker := "\n…（已按长度预算截断）\n"
		keep := budget - len([]rune(marker))
		if keep < 0 {
			keep = 0
			marker = ""
		}
		out = string(runes[:keep]) + marker
	}
	return out, true
}

// threadExportMedia 是 JSON 导出中的附件元信息，不包含二进制数据。
type threadExportMedia struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	MimeType string `json:"mimeType"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

// threadExportMessage 是 JSON 导出中的单条消息。
type threadExportMessage struct {
	ID        string              `json:"id"`
	Role      string              `json:"role"`
	Kind      string              `json:"kind"`
	Text      string              `json:"text"`
	TurnIndex int                 `json:"turnIndex"`
	TurnID    string              `json:"turnId"`
	IsFinal   bool                `json:"isFinal"`
	Media     []threadExportMedia `json:"media"`
}

// threadExportThread 是 JSON 导出中的会话元数据。
type threadExportThread struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	Title     string `json:"title"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	UpdatedAt string `json:"updatedAt"`
}

// threadExportDoc 是 JSON 导出的顶层结构。
type threadExportDoc struct {
	Version    int                   `json:"version"`
	ExportedAt string                `json:"exportedAt"`
	Thread     threadExportThread    `json:"thread"`
	Messages   []threadExportMessage `json:"messages"`
}

// buildThreadExportJSON 把会话导出为两空格缩进的 JSON，时间字段使用 RFC3339。
func buildThreadExportJSON(t Thread, msgs []Message, exportedAt time.Time) ([]byte, error) {
	doc := threadExportDoc{
		Version:    1,
		ExportedAt: exportedAt.Format(time.RFC3339),
		Thread: threadExportThread{
			ID:        t.ID,
			ProjectID: t.ProjectID,
			Title:     t.Title,
			Provider:  t.Provider,
			Model:     t.Model,
			UpdatedAt: t.UpdatedAt.Format(time.RFC3339),
		},
		Messages: make([]threadExportMessage, 0, len(msgs)),
	}
	for _, m := range msgs {
		media := make([]threadExportMedia, 0, len(m.Media))
		for _, item := range m.Media {
			media = append(media, threadExportMedia{
				ID:       item.ID,
				Name:     item.Name,
				MimeType: item.MimeType,
				Width:    item.Width,
				Height:   item.Height,
			})
		}
		doc.Messages = append(doc.Messages, threadExportMessage{
			ID:        m.ID,
			Role:      m.Role,
			Kind:      m.Kind,
			Text:      m.Text,
			TurnIndex: m.TurnIndex,
			TurnID:    m.TurnID,
			IsFinal:   m.IsFinal,
			Media:     media,
		})
	}
	return json.MarshalIndent(doc, "", "  ")
}

// buildThreadExportHTML 把会话导出为最小 HTML 文档：正文沿用 Markdown 文本（受导出长度预算约束），
// 整体包在标准 <pre> 结构里并做 HTML 转义，浏览器打开就能看到纯文本排版。
func buildThreadExportHTML(t Thread, msgs []Message, exportedAt time.Time) string {
	body, _ := buildThreadExportMarkdownBudgeted(t, msgs, exportedAt, threadExportBudgetChars)
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html lang=\"zh-CN\">\n<head>\n<meta charset=\"utf-8\">\n<title>")
	b.WriteString(html.EscapeString(t.Title))
	b.WriteString("</title>\n</head>\n<body>\n<pre>")
	b.WriteString(html.EscapeString(body))
	b.WriteString("</pre>\n</body>\n</html>\n")
	return b.String()
}

// writeThreadExport 按扩展名（不区分大小写）选择格式写盘：.json 用 JSON，.html/.htm 用 HTML，其余用 Markdown。
func writeThreadExport(path string, t Thread, msgs []Message, now time.Time) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	var data []byte
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".json":
		jsonData, err := buildThreadExportJSON(t, msgs, now)
		if err != nil {
			return err
		}
		data = jsonData
	case ".html", ".htm":
		data = []byte(buildThreadExportHTML(t, msgs, now))
	default:
		// Markdown 存文件走预算版：超长会话逐级降级，避免写出几十万字打不开的文件。
		md, _ := buildThreadExportMarkdownBudgeted(t, msgs, now, threadExportBudgetChars)
		data = []byte(md)
	}
	return os.WriteFile(path, data, 0o644)
}

// saveExportDialog 是可注入的保存对话框，测试时替换以避免弹出真实窗口。
var saveExportDialog func(defaultPath string) (string, error) = defaultSaveExportDialog

// defaultSaveExportDialog 唤起系统原生保存对话框，返回用户选择的路径；取消时路径为空。
func defaultSaveExportDialog(defaultPath string) (string, error) {
	path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
		Title:       "导出会话",
		DefaultPath: defaultPath,
		ButtonLabel: "导出",
		Filters: []mygo.FileFilter{
			{Name: "Markdown", Extensions: []string{"md"}},
			{Name: "HTML", Extensions: []string{"html"}},
			{Name: "JSON", Extensions: []string{"json"}},
		},
		CreateDirectories: true,
	})
	if err != nil {
		return "", err
	}
	return path, nil
}

// exportThread 导出指定会话：threadID 为空时取当前选中会话，
// 在后台取历史并弹出保存对话框，最后写入 Markdown 或 JSON 文件（/export 命令）。
func (s *chatState) exportThread(threadID string) {
	if s == nil {
		return
	}

	s.mu.Lock()
	id := threadID
	if id == "" {
		id = s.selectedThreadID
	}
	if id == "" {
		s.mu.Unlock()
		s.setToast("没有可导出的会话")
		return
	}
	var thread Thread
	found := false
	for i := range s.threads {
		if s.threads[i].ID == id {
			thread = s.threads[i]
			found = true
			break
		}
	}
	client := s.client
	s.mu.Unlock()

	if !found {
		// 会话列表里还没有该条目时，只带 ID 导出仍然可用。
		thread = Thread{ID: id}
	}

	s.setToast("正在导出…")
	go func() {
		// 无论成功、取消还是失败，结束时都刷新一次界面。
		defer s.invalidate()

		if client == nil {
			s.setToast("导出失败: 会话客户端未初始化")
			return
		}
		msgs, err := client.History(context.Background(), id)
		if err != nil {
			s.setToast("导出失败: " + err.Error())
			return
		}

		saver := saveExportDialog
		if saver == nil {
			saver = defaultSaveExportDialog
		}
		defaultPath := filepath.Join(downloadsDir(), threadExportFileName(thread.Title, ".md"))
		path, err := saver(defaultPath)
		if err != nil {
			s.setToast("导出失败: " + err.Error())
			return
		}
		if path == "" {
			return // 用户取消，静默返回
		}

		if err := writeThreadExport(path, thread, msgs, time.Now()); err != nil {
			s.setToast("导出失败: " + err.Error())
			return
		}
		s.setToast("已导出到 " + path)
	}()
}

// threadCopySource 决定复制 Markdown 用哪份消息：id 是当前选中会话时直接复用已加载的
// messages；其它会话返回 needFetch=true，由调用方从后端取历史，绝不能把当前会话的正文
// 复制到别的会话名下。thread 会尽量从 threads 里补齐标题等元数据，找不到时只带 ID。
func threadCopySource(id, selectedID string, threads []Thread, messages []Message) (Thread, []Message, bool) {
	thread := Thread{ID: id}
	for i := range threads {
		if threads[i].ID == id {
			thread = threads[i]
			break
		}
	}
	if id == selectedID {
		return thread, append([]Message(nil), messages...), false
	}
	return thread, nil, true
}

// copyThreadMarkdown 把指定会话（threadID 为空时取当前选中会话）的 Markdown 复制到剪贴板。
// 当前会话直接用已加载的消息；其它会话在后台取历史，避免复制错会话的正文。
// 剪贴板没有保存对话框这一层保护，直接用预算版（threadExportBudgetChars），
// 避免几十万字符粘进输入框把界面卡住；写剪贴板与提示都回到主线程执行。
func (s *chatState) copyThreadMarkdown(c *ui.Context, threadID string) {
	if s == nil || c == nil {
		return
	}

	s.mu.Lock()
	id := threadID
	if id == "" {
		id = s.selectedThreadID
	}
	if id == "" {
		s.mu.Unlock()
		s.setToast("没有可复制的会话")
		return
	}
	thread, msgs, needFetch := threadCopySource(id, s.selectedThreadID, s.threads, s.messages)
	client := s.client
	// ui.Context 只在当前构建帧有效，取出持久的 Services 供后台回写剪贴板使用。
	services := c.Services()
	s.mu.Unlock()

	if !needFetch {
		s.writeThreadMarkdownClipboard(services, thread, msgs)
		return
	}

	s.setToast("正在复制…")
	go func() {
		if client == nil {
			s.setToast("复制失败：会话客户端未初始化")
			return
		}
		msgs, err := client.History(context.Background(), id)
		if err != nil {
			s.setToast("复制失败：" + err.Error())
			return
		}
		mygo.RunOnMain(func() {
			s.writeThreadMarkdownClipboard(services, thread, msgs)
		})
	}()
}

// writeThreadMarkdownClipboard 生成预算版 Markdown 写入剪贴板，并提示复制结果。
// 剪贴板要求在主线程写入，调用方负责线程约束；services 跨帧有效，可在后台回调里使用。
func (s *chatState) writeThreadMarkdownClipboard(services ui.Services, thread Thread, msgs []Message) {
	md, trimmed := buildThreadExportMarkdownBudgeted(thread, msgs, time.Now(), threadExportBudgetChars)
	services.WriteClipboard(md)
	if trimmed {
		s.setToast("已复制会话 Markdown（内容过长，已按长度预算降级）")
		return
	}
	s.setToast("已复制会话 Markdown")
}
