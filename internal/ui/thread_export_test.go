package mcodeui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestThreadExportFileName(t *testing.T) {
	cases := []struct {
		name  string
		title string
		ext   string
		want  string
	}{
		{"普通标题", "普通标题", ".md", "普通标题.md"},
		{"去掉非法字符", `a/b\c:d*e?f"g<h>i|j`, ".md", "abcdefghij.md"},
		{"去掉换行", "line1\nline2\r\nline3", ".md", "line1line2line3.md"},
		{"去掉首尾空白", "  两侧空白  ", ".json", "两侧空白.json"},
		{"空标题回退", "", ".json", "mcode-会话.json"},
		{"全非法字符回退", "///", ".md", "mcode-会话.md"},
		{"保留中文与空格", "我的 会话", ".md", "我的 会话.md"},
	}
	for _, tc := range cases {
		if got := threadExportFileName(tc.title, tc.ext); got != tc.want {
			t.Errorf("%s: threadExportFileName(%q, %q) = %q, want %q", tc.name, tc.title, tc.ext, got, tc.want)
		}
	}

	long := strings.Repeat("长", 100)
	want := strings.Repeat("长", 80) + ".md"
	if got := threadExportFileName(long, ".md"); got != want {
		t.Errorf("超长标题应截断到 80 个字符，got %q（%d 个字符）", got, len([]rune(got)))
	}
}

func TestBuildThreadExportMarkdown(t *testing.T) {
	thread := Thread{
		ID:        "th-1",
		ProjectID: "p-1",
		Title:     "测试会话",
		Provider:  "claude",
		Model:     "opus",
		UpdatedAt: time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC),
	}
	exportedAt := time.Date(2026, 10, 19, 9, 30, 0, 0, time.UTC)
	msgs := []Message{
		{ID: "m1", Role: "user", Text: "你好"},
		{ID: "m2", Role: "assistant", Kind: "text", Text: "回答内容", Media: []MessageMedia{
			{ID: "a1", Name: "cat.png", MimeType: "image/png"},
			{ID: "a2", MimeType: "image/webp"},
		}},
		{ID: "m3", Role: "assistant", Kind: "reasoning", Text: "先想一下"},
		{ID: "m4", Role: "tool", Text: "run ```python\nprint(1)\n```"},
		{ID: "m5", Role: "assistant", Text: ""}, // 无正文无附件，应跳过
		{ID: "m6", Role: "user", Media: []MessageMedia{{ID: "a3", MimeType: "application/pdf"}}},
	}

	out := buildThreadExportMarkdown(thread, msgs, exportedAt)

	for _, want := range []string{
		"# 测试会话",
		"- 会话 ID：th-1",
		"- Provider：claude",
		"- Model：opus",
		"- 更新时间：2026-10-18 10:00:00",
		"- 导出时间：2026-10-19 09:30:00",
		"## 用户",
		"## 助手",
		"## tool",
		"你好",
		"回答内容",
		"- 附件：cat.png",
		"- 附件：image/webp",
		"- 附件：application/pdf",
		"<details><summary>工具/推理</summary>",
		"```\n先想一下\n```",
		"````\nrun ```python\nprint(1)\n```\n````",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("导出 Markdown 缺少 %q，实际内容：\n%s", want, out)
		}
	}

	if got := strings.Count(out, "## 助手"); got != 2 {
		t.Errorf("空消息应被跳过，## 助手 出现 %d 次，want 2", got)
	}
	if got := strings.Count(out, "## 用户"); got != 2 {
		t.Errorf("## 用户 出现 %d 次，want 2", got)
	}
	if !strings.HasPrefix(out, "# 测试会话\n") {
		t.Errorf("导出应以标题开头，实际：\n%s", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Error("导出应以换行结尾")
	}
}

func TestBuildThreadExportJSON(t *testing.T) {
	type media struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		MimeType string `json:"mimeType"`
		Width    int    `json:"width"`
		Height   int    `json:"height"`
	}
	type message struct {
		ID        string  `json:"id"`
		Role      string  `json:"role"`
		Kind      string  `json:"kind"`
		Text      string  `json:"text"`
		TurnIndex int     `json:"turnIndex"`
		TurnID    string  `json:"turnId"`
		IsFinal   bool    `json:"isFinal"`
		Media     []media `json:"media"`
	}
	type thread struct {
		ID        string `json:"id"`
		ProjectID string `json:"projectId"`
		Title     string `json:"title"`
		Provider  string `json:"provider"`
		Model     string `json:"model"`
		UpdatedAt string `json:"updatedAt"`
	}

	updatedAt := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)
	exportedAt := time.Date(2026, 10, 19, 9, 30, 0, 0, time.UTC)
	threadIn := Thread{
		ID:        "th-2",
		ProjectID: "p-2",
		Title:     "JSON 会话",
		Provider:  "codex",
		Model:     "gpt-5",
		UpdatedAt: updatedAt,
	}
	msgs := []Message{
		{ID: "m1", Role: "user", Kind: "", Text: "hi", TurnIndex: 3, TurnID: "turn-1", IsFinal: true,
			Media: []MessageMedia{{ID: "a1", Name: "x.png", MimeType: "image/png", Width: 10, Height: 20}}},
		{ID: "m2", Role: "assistant", Kind: "text"},
	}

	data, err := buildThreadExportJSON(threadIn, msgs, exportedAt)
	if err != nil {
		t.Fatalf("buildThreadExportJSON 出错: %v", err)
	}

	var doc struct {
		Version    int       `json:"version"`
		ExportedAt string    `json:"exportedAt"`
		Thread     thread    `json:"thread"`
		Messages   []message `json:"messages"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("导出的 JSON 无法回读: %v\n%s", err, data)
	}

	if doc.Version != 1 {
		t.Errorf("version = %d, want 1", doc.Version)
	}
	expAt, err := time.Parse(time.RFC3339, doc.ExportedAt)
	if err != nil {
		t.Fatalf("exportedAt 不是 RFC3339: %q", doc.ExportedAt)
	}
	if !expAt.Equal(exportedAt) {
		t.Errorf("exportedAt = %v, want %v", expAt, exportedAt)
	}
	if doc.Thread.ID != "th-2" || doc.Thread.ProjectID != "p-2" || doc.Thread.Title != "JSON 会话" ||
		doc.Thread.Provider != "codex" || doc.Thread.Model != "gpt-5" {
		t.Errorf("thread 元数据不正确: %+v", doc.Thread)
	}
	updAt, err := time.Parse(time.RFC3339, doc.Thread.UpdatedAt)
	if err != nil || !updAt.Equal(updatedAt) {
		t.Errorf("thread.updatedAt = %q（err=%v）, want %v", doc.Thread.UpdatedAt, err, updatedAt)
	}

	if len(doc.Messages) != 2 {
		t.Fatalf("messages 数量 = %d, want 2", len(doc.Messages))
	}
	first := doc.Messages[0]
	if first.ID != "m1" || first.Role != "user" || first.Kind != "" || first.Text != "hi" ||
		first.TurnIndex != 3 || first.TurnID != "turn-1" || !first.IsFinal {
		t.Errorf("messages[0] 字段不正确: %+v", first)
	}
	if len(first.Media) != 1 {
		t.Fatalf("messages[0].media 数量 = %d, want 1", len(first.Media))
	}
	if first.Media[0].ID != "a1" || first.Media[0].Name != "x.png" ||
		first.Media[0].MimeType != "image/png" || first.Media[0].Width != 10 || first.Media[0].Height != 20 {
		t.Errorf("messages[0].media 字段不正确: %+v", first.Media[0])
	}
	if second := doc.Messages[1]; second.Media == nil || len(second.Media) != 0 {
		t.Errorf("无附件的消息应导出为空的 media 数组，got %+v", second.Media)
	}

	raw := string(data)
	if !strings.Contains(raw, `"media": []`) {
		t.Errorf("无附件的消息应序列化为 [] 而非 null：\n%s", raw)
	}
	if !strings.Contains(raw, `"mimeType": "image/png"`) {
		t.Errorf("media 元信息应包含 mimeType：\n%s", raw)
	}
}

func TestWriteThreadExportByExtension(t *testing.T) {
	dir := t.TempDir()
	thread := Thread{
		ID:        "th-9",
		Title:     "写盘会话",
		Provider:  "claude",
		Model:     "opus",
		UpdatedAt: time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC),
	}
	msgs := []Message{{ID: "m1", Role: "user", Text: "hello"}}
	now := time.Date(2026, 10, 19, 9, 30, 0, 0, time.UTC)

	// 扩展名大写 + 父目录不存在：应写 Markdown 并自动建目录。
	mdPath := filepath.Join(dir, "nested", "会话.MD")
	if err := writeThreadExport(mdPath, thread, msgs, now); err != nil {
		t.Fatalf("写 Markdown 导出失败: %v", err)
	}
	mdData, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatalf("读取 Markdown 导出失败: %v", err)
	}
	if !strings.HasPrefix(string(mdData), "# 写盘会话") {
		t.Errorf("Markdown 导出应含标题，实际：\n%s", mdData)
	}
	if !strings.Contains(string(mdData), "## 用户") {
		t.Errorf("Markdown 导出应含用户小节，实际：\n%s", mdData)
	}

	jsonPath := filepath.Join(dir, "会话.JSON")
	if err := writeThreadExport(jsonPath, thread, msgs, now); err != nil {
		t.Fatalf("写 JSON 导出失败: %v", err)
	}
	jsonData, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("读取 JSON 导出失败: %v", err)
	}
	var doc struct {
		Version int `json:"version"`
		Thread  struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(jsonData, &doc); err != nil {
		t.Fatalf("JSON 导出无法回读: %v\n%s", err, jsonData)
	}
	if doc.Version != 1 || doc.Thread.ID != "th-9" {
		t.Errorf("JSON 导出内容不正确: %+v", doc)
	}
}

// threadExportBudgetTestMessages 造一份「6 个回合、每回合工具输出 + 思考 + 助手 + 用户」的长会话，
// 用来验证长度预算与逐级降级。
func threadExportBudgetTestMessages(t *testing.T) []Message {
	t.Helper()
	msgs := make([]Message, 0, 24)
	for turn := 0; turn < 6; turn++ {
		msgs = append(msgs,
			Message{ID: fmt.Sprintf("tool-%d", turn), Role: "tool", Kind: "tool", ToolName: "bash",
				Text: strings.Repeat("x", 6000), TurnIndex: turn, TurnID: fmt.Sprintf("turn-%d", turn)},
			Message{ID: fmt.Sprintf("think-%d", turn), Role: "reasoning", Kind: "reasoning",
				Text: strings.Repeat("思", 500), TurnIndex: turn, TurnID: fmt.Sprintf("turn-%d", turn)},
			Message{ID: fmt.Sprintf("asst-%d", turn), Role: "assistant", Kind: "text",
				Text: fmt.Sprintf("第 %d 回合的回答", turn), TurnIndex: turn, TurnID: fmt.Sprintf("turn-%d", turn)},
			Message{ID: fmt.Sprintf("user-%d", turn), Role: "user",
				Text: fmt.Sprintf("第 %d 回合的提问", turn), TurnIndex: turn, TurnID: fmt.Sprintf("turn-%d", turn)},
		)
	}
	return msgs
}

// TestThreadExportLevels 校验降级等级分配：预算充足全为 0、预算紧张时老回合先升级、最近回合等级最低。
func TestThreadExportLevels(t *testing.T) {
	msgs := threadExportBudgetTestMessages(t)
	plain := len([]rune(buildThreadExportMarkdown(Thread{ID: "th-b", Title: "预算"}, msgs, time.Now())))

	t.Run("预算为 0 表示不降级", func(t *testing.T) {
		for i, level := range threadExportLevels(msgs, 0) {
			if level != 0 {
				t.Fatalf("第 %d 条消息的等级 = %d，预算为 0 时应全部为 0", i, level)
			}
		}
	})

	t.Run("预算充足全部不降级", func(t *testing.T) {
		for i, level := range threadExportLevels(msgs, plain+1000) {
			if level != 0 {
				t.Fatalf("第 %d 条消息的等级 = %d，预算充足时应全部为 0", i, level)
			}
		}
	})

	t.Run("预算紧张时老回合先降级", func(t *testing.T) {
		levels := threadExportLevels(msgs, plain/3)
		if len(levels) != len(msgs) {
			t.Fatalf("等级数量 = %d，期望 %d", len(levels), len(msgs))
		}
		recentFrom := 6 - threadExportRecentTurns
		for i, m := range msgs {
			if m.TurnIndex < recentFrom && levels[i] < 1 {
				t.Fatalf("第 %d 回合属于老回合，等级应至少为 1，得到 %d", m.TurnIndex, levels[i])
			}
			if levels[i] > threadExportLevelMax {
				t.Fatalf("第 %d 条消息的等级 = %d，超过上限 %d", i, levels[i], threadExportLevelMax)
			}
		}
		if levels[0] <= levels[len(levels)-1] {
			t.Fatalf("最老回合的等级（%d）应高于最新回合（%d）", levels[0], levels[len(levels)-1])
		}
		// 最近 threadExportRecentTurns 个回合共用同一个（最低的）等级。
		want := levels[len(levels)-1]
		for i, m := range msgs {
			if m.TurnIndex >= recentFrom && levels[i] != want {
				t.Fatalf("第 %d 回合是最近回合，等级应为 %d，得到 %d", m.TurnIndex, want, levels[i])
			}
		}
	})
}

// TestThreadExportBudgeted 校验预算版 Markdown：预算充足与完整导出逐字符一致，超预算时逐级降级且守住预算。
func TestThreadExportBudgeted(t *testing.T) {
	thread := Thread{ID: "th-b", Title: "预算会话", Provider: "claude", Model: "opus",
		UpdatedAt: time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)}
	exportedAt := time.Date(2026, 10, 19, 9, 30, 0, 0, time.UTC)
	msgs := threadExportBudgetTestMessages(t)
	full := buildThreadExportMarkdown(thread, msgs, exportedAt)

	t.Run("预算为 0 时与完整导出一致", func(t *testing.T) {
		got, trimmed := buildThreadExportMarkdownBudgeted(thread, msgs, exportedAt, 0)
		if trimmed {
			t.Fatal("预算为 0 表示不降级，不应报告降级")
		}
		if got != full {
			t.Fatal("预算为 0 时输出应与完整导出逐字符一致")
		}
	})

	t.Run("预算充足时与完整导出一致", func(t *testing.T) {
		got, trimmed := buildThreadExportMarkdownBudgeted(thread, msgs, exportedAt, len([]rune(full))+1)
		if trimmed {
			t.Fatal("预算充足时不应报告降级")
		}
		if got != full {
			t.Fatal("预算充足时输出应与完整导出逐字符一致")
		}
	})

	t.Run("超预算时按顺序降级并守住预算", func(t *testing.T) {
		budget := len([]rune(full)) / 3
		got, trimmed := buildThreadExportMarkdownBudgeted(thread, msgs, exportedAt, budget)
		if !trimmed {
			t.Fatal("超预算时应报告降级")
		}
		if n := len([]rune(got)); n > budget {
			t.Fatalf("降级后长度 %d 超过预算 %d", n, budget)
		}
		if !strings.Contains(got, "已自动降级") {
			t.Fatalf("降级输出应包含降级说明行，实际：\n%.300s", got)
		}
		// 老回合的工具调用被折叠成计数。
		if !strings.Contains(got, "已省略") {
			t.Fatalf("老回合的工具调用应折叠成计数，实际：\n%.300s", got)
		}
		// 终端输出的完整长正文不再出现（被截断或丢弃）。
		if strings.Contains(got, strings.Repeat("x", threadExportToolTextCap+1)) {
			t.Fatal("降级后不应保留超长终端输出原文")
		}
		// 用户与助手的正文必须完整保留。
		for _, m := range msgs {
			if m.Role == "user" || (m.Role == "assistant" && m.Kind == "text") {
				if !strings.Contains(got, m.Text) {
					t.Fatalf("用户/助手正文 %q 在降级输出里丢失", m.Text)
				}
			}
		}
	})

	t.Run("正文本身超预算时硬截断", func(t *testing.T) {
		huge := []Message{{ID: "m1", Role: "user", Text: strings.Repeat("长", 20000)}}
		const budget = 5000
		got, trimmed := buildThreadExportMarkdownBudgeted(thread, huge, exportedAt, budget)
		if !trimmed {
			t.Fatal("超预算时应报告降级")
		}
		if n := len([]rune(got)); n > budget {
			t.Fatalf("硬截断后长度 %d 超过预算 %d", n, budget)
		}
		if !strings.Contains(got, "已按长度预算截断") {
			t.Fatalf("应提示已按长度预算截断，实际：\n%.200s", got)
		}
	})
}

// TestThreadExportThinkingKeepsHeadAndTail 校验最高降级等级下思考只保留头尾、短思考原样、正文永不动。
func TestThreadExportThinkingKeepsHeadAndTail(t *testing.T) {
	text := strings.Repeat("头", threadExportThinkingKeep) + strings.Repeat("中", 500) + strings.Repeat("尾", threadExportThinkingKeep)
	m := Message{ID: "r1", Role: "reasoning", Kind: "reasoning", Text: text}
	got := threadExportMessageSectionAt(m, threadExportLevelMax)
	if !strings.Contains(got, strings.Repeat("头", 20)) || !strings.Contains(got, strings.Repeat("尾", 20)) {
		t.Fatalf("思考的头尾都应保留，实际：\n%.200s…", got)
	}
	if strings.Contains(got, "中中中") {
		t.Fatal("思考中段应被省略")
	}
	if !strings.Contains(got, "已省略中间内容") {
		t.Fatal("省略处应有说明")
	}
	if n := len([]rune(got)); n > 2*threadExportThinkingKeep+100 {
		t.Fatalf("降级后的思考长度 %d 超出头尾保留量", n)
	}

	short := Message{ID: "r2", Role: "reasoning", Kind: "reasoning", Text: "先想一下"}
	if got := threadExportMessageSectionAt(short, threadExportLevelMax); !strings.Contains(got, "先想一下") {
		t.Fatalf("短思考正文应原样保留，实际：%q", got)
	}

	user := Message{ID: "u1", Role: "user", Text: strings.Repeat("正文", 3000)}
	if got := threadExportMessageSectionAt(user, threadExportLevelMax); !strings.Contains(got, user.Text) {
		t.Fatal("用户正文在任何等级下都应保留原文")
	}
}

// TestThreadCopySourceUsesSelectedMessagesOnly 验证复制来源：只有当前选中会话才复用已加载的
// s.messages；其它会话必须标记为需要取历史，不能把当前会话的正文复制到别的会话名下。
func TestThreadCopySourceUsesSelectedMessagesOnly(t *testing.T) {
	threads := []Thread{{ID: "t1", Title: "会话一"}, {ID: "t2", Title: "会话二"}}
	messages := []Message{{ID: "m1", Role: "user", Text: "当前会话内容"}}

	thread, got, needFetch := threadCopySource("t1", "t1", threads, messages)
	if needFetch {
		t.Fatal("当前选中会话应直接复用已加载消息")
	}
	if len(got) != 1 || got[0].Text != "当前会话内容" {
		t.Fatalf("当前会话应使用 s.messages，实际 %+v", got)
	}
	if thread.ID != "t1" || thread.Title != "会话一" {
		t.Fatalf("会话元数据不对：%+v", thread)
	}

	thread, got, needFetch = threadCopySource("t2", "t1", threads, messages)
	if !needFetch {
		t.Fatal("非当前会话必须从后端取历史")
	}
	if len(got) != 0 {
		t.Fatalf("非当前会话不能使用 s.messages，实际 %+v", got)
	}
	if thread.ID != "t2" || thread.Title != "会话二" {
		t.Fatalf("会话元数据不对：%+v", thread)
	}
}
