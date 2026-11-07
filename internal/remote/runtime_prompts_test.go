package remote

import (
	"fmt"
	"strings"
	"testing"

	"mcode/internal/remote/wire"
)

func TestBundleFollowUpText(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	tests := []struct {
		name     string
		items    []wire.FollowUp
		contains []string
		equals   string
	}{
		{
			name:   "empty items",
			items:  []wire.FollowUp{},
			equals: "",
		},
		{
			name: "single item without attachment",
			items: []wire.FollowUp{
				{Text: "single prompt"},
			},
			equals: "single prompt",
		},
		{
			name: "multiple items without attachments",
			items: []wire.FollowUp{
				{Text: "first prompt"},
				{Text: "  "},
				{Text: "second prompt"},
			},
			equals: "first prompt\n\nsecond prompt",
		},
		{
			name: "multiple items with attachments",
			items: []wire.FollowUp{
				{
					Text: "first task",
					Attachments: []wire.Attachment{
						{ID: "att-1", Name: "spec.png", MimeType: "image/png"},
						{ID: "att-2", Name: "manual.pdf", MimeType: "application/pdf"},
					},
				},
				{
					Text: "second task",
					Attachments: []wire.Attachment{
						{ID: "att-3", Name: "diagram.jpeg", MimeType: "image/jpeg"},
					},
				},
			},
			contains: []string{
				"The user queued 2 messages while you were working; they arrive together below.",
				"Queued message 1:\nfirst task\nAttachments: image #1 \"spec.png\", file \"manual.pdf\"",
				"Queued message 2:\nsecond task\nAttachments: image #2 \"diagram.jpeg\"",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := bundleFollowUpText(tc.items)
			if tc.equals != "" && res != tc.equals {
				t.Fatalf("expected %q, got %q", tc.equals, res)
			}
			for _, sub := range tc.contains {
				if !strings.Contains(res, sub) {
					t.Fatalf("expected result to contain %q, but got %q", sub, res)
				}
			}
		})
	}
}

func TestBuildLimitHandoffText(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	longText := strings.Repeat("A", 500)
	toolKind := "tool"

	tests := []struct {
		name     string
		messages []wire.ThreadMessage
		title    string
		contains []string
	}{
		{
			name:     "empty messages",
			messages: nil,
			title:    "MyProject",
			contains: []string{
				`This picks up a task from the chat "MyProject".`,
				"Original request:\n\n(no request text was saved)",
				"Please finish the remaining work here. If anything is ambiguous, ask me instead of guessing.",
			},
		},
		{
			name: "truncate over 400 chars for assistant",
			messages: []wire.ThreadMessage{
				{Role: "user", Text: "Please fix bug"},
				{Role: "assistant", Text: longText},
				{Role: "tool", Kind: &toolKind, Text: "git diff"},
			},
			title: "BugFix",
			contains: []string{
				`This picks up a task from the chat "BugFix".`,
				"Original request:\n\nPlease fix bug",
				"Progress before the cutoff:",
				"- Reply: " + strings.Repeat("A", 400) + "…",
				"- Tool: git diff",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := BuildLimitHandoffText(tc.messages, tc.title)
			for _, sub := range tc.contains {
				if !strings.Contains(res, sub) {
					t.Fatalf("expected result to contain %q, but got %q", sub, res)
				}
			}
		})
	}

	// 40 行上限截断测试
	t.Run("max 40 lines limit", func(t *testing.T) {
		var msgs []wire.ThreadMessage
		msgs = append(msgs, wire.ThreadMessage{Role: "user", Text: "user request"})
		for i := 0; i < 60; i++ {
			msgs = append(msgs, wire.ThreadMessage{
				Role: "assistant",
				Text: fmt.Sprintf("step %d", i+1),
			})
		}
		res := BuildLimitHandoffText(msgs, "ManySteps")
		lines := strings.Split(res, "\n")
		var doneLines []string
		inDone := false
		for _, line := range lines {
			if strings.HasPrefix(line, "Progress before the cutoff:") {
				inDone = true
				continue
			}
			if inDone && strings.HasPrefix(line, "- Reply:") {
				doneLines = append(doneLines, line)
			}
		}
		if len(doneLines) != 40 {
			t.Fatalf("expected exactly 40 lines in done section, got %d", len(doneLines))
		}
	})
}

func TestBuildMentionContext(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	tests := []struct {
		name     string
		threads  []mentionedThread
		contains []string
		equals   string
	}{
		{
			name:    "empty threads",
			threads: nil,
			equals:  "",
		},
		{
			name: "single thread with goal and conclusion",
			threads: []mentionedThread{
				{
					ID:    "th-123",
					Title: `Design "Doc"`,
					Messages: []wire.ThreadMessage{
						{Role: "user", Text: "First user goal"},
						{Role: "assistant", Text: "Step 1"},
						{Role: "user", Text: "Followup"},
						{Role: "assistant", Text: "Final conclusion"},
					},
				},
			},
			contains: []string{
				`Using @mentions, the user pointed to a previous chat in this project: "Design \"Doc\"".`,
				"===== REFERENCED CHATS: START =====",
				`<referenced_chat id="th-123" title="Design &quot;Doc&quot;">`,
				"First request: First user goal",
				"Latest exchanges:\nAssistant: Step 1\nUser: Followup",
				"Final reply: Final conclusion",
				"</referenced_chat>",
				"===== REFERENCED CHATS: END =====",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := buildMentionContext(tc.threads)
			if tc.equals != "" && res != tc.equals {
				t.Fatalf("expected %q, got %q", tc.equals, res)
			}
			for _, sub := range tc.contains {
				if !strings.Contains(res, sub) {
					t.Fatalf("expected result to contain %q, but got %q", sub, res)
				}
			}
		})
	}

	// 预算超额与保底截断测试
	t.Run("context budget truncation and per-thread minimum", func(t *testing.T) {
		var threads []mentionedThread
		// 创建 15 个超长会话，总长度远超 12000
		for i := 0; i < 15; i++ {
			threads = append(threads, mentionedThread{
				ID:    fmt.Sprintf("th-%d", i),
				Title: fmt.Sprintf("Thread %d", i),
				Messages: []wire.ThreadMessage{
					{Role: "user", Text: strings.Repeat("Goal ", 400)},
					{Role: "assistant", Text: strings.Repeat("Answer ", 600)},
				},
			})
		}
		res := buildMentionContext(threads)
		if !strings.Contains(res, mentionTrimmedNotice) {
			t.Fatalf("expected truncation notice in preamble")
		}
		if !strings.Contains(res, "[… trimmed to stay within the size budget …]") {
			t.Fatalf("expected marker in truncated block")
		}
		if len([]rune(res)) > mentionContextBudget {
			t.Fatalf("result exceeded expected character budget: %d runes", len([]rune(res)))
		}
	})
}

func TestProviderHandoffBudget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var history []wire.ThreadMessage
	for i := 0; i < 300; i++ {
		history = append(history, wire.ThreadMessage{Role: "user", Text: strings.Repeat("界", 5000)})
	}
	if got := len([]rune(buildProviderHandoffPrompt(history, ""))); got > 32000 {
		t.Fatalf("handoff exceeds 32k budget: %d", got)
	}
}

func TestBuildProviderHandoffPrompt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	tests := []struct {
		name     string
		history  []wire.ThreadMessage
		prompt   string
		contains []string
		equals   string
	}{
		{
			name:    "empty history returns original prompt",
			history: nil,
			prompt:  "new instruction",
			equals:  "new instruction",
		},
		{
			name: "recent history preserves full exchange",
			history: []wire.ThreadMessage{
				{Role: "user", Text: "Hello agent"},
				{Role: "assistant", Text: "Hello user"},
			},
			prompt: "continue task",
			contains: []string{
				"[mcode: history from before the agent switch]",
				"User: Hello agent",
				"Assistant: Hello user",
				"[mcode: end of history]",
				"continue task",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := buildProviderHandoffPrompt(tc.history, tc.prompt)
			if tc.equals != "" && res != tc.equals {
				t.Fatalf("expected %q, got %q", tc.equals, res)
			}
			for _, sub := range tc.contains {
				if !strings.Contains(res, sub) {
					t.Fatalf("expected result to contain %q, but got %q", sub, res)
				}
			}
		})
	}

	// 历史轮次压缩单行及 32000 字符预算截断
	t.Run("older history compressed to single line and omitted warning", func(t *testing.T) {
		var hist []wire.ThreadMessage
		// 构造 20 轮（40 条消息），前 34 条属于 older，后 6 条属于 recent
		for i := 0; i < 20; i++ {
			hist = append(hist, wire.ThreadMessage{
				Role: "user",
				Text: fmt.Sprintf("Question %d with\nnewline", i),
			})
			hist = append(hist, wire.ThreadMessage{
				Role: "assistant",
				Text: fmt.Sprintf("Answer %d with\nmulti\nlines", i),
			})
		}
		res := buildProviderHandoffPrompt(hist, "final prompt")
		if !strings.Contains(res, "User: Question 0 with newline") {
			t.Fatalf("expected older messages flattened into single line")
		}
		if !strings.Contains(res, "[mcode: history from before the agent switch]") {
			t.Fatalf("expected header block")
		}
		if !strings.Contains(res, "final prompt") {
			t.Fatalf("expected trailing user prompt")
		}
	})
}
