package mcodeui

import "testing"

// TestIsToolBlockedByHookAndFailed 验证被 PreToolUse hook 拦截/改道的结果不算工具失败，
// 以及拦截判定只认单独成行的 hook 错误行，不会被正文里零散的提及误触发。
func TestIsToolBlockedByHookAndFailed(t *testing.T) {
	cases := []struct {
		name        string
		msg         Message
		wantBlocked bool
		wantFailed  bool
	}{
		{
			name: "hook 改道并以 [failed] 结尾",
			msg: Message{
				Role: "tool",
				Text: `{"url":"https://example.com/docs"}
PreToolUse:WebFetch hook error: context-mode: WebFetch redirected.
[failed]`,
			},
			wantBlocked: true,
			wantFailed:  false,
		},
		{
			name:        "普通失败",
			msg:         Message{Role: "tool", ToolStatus: "failed", Text: "exit status 1"},
			wantBlocked: false,
			wantFailed:  true,
		},
		{
			name:        "正文只是普通文本且含 [failed]",
			msg:         Message{Role: "tool", Text: "build output\n[failed]"},
			wantBlocked: false,
			wantFailed:  true,
		},
		{
			name:        "PreToolUse: 与 hook error: 分处不同行",
			msg:         Message{Role: "tool", Text: "PreToolUse:WebFetch\nsomething hook error: unrelated"},
			wantBlocked: false,
			wantFailed:  false,
		},
		{
			name: "ToolStatus failed 但正文是 hook 拦截",
			msg: Message{
				Role:       "tool",
				ToolStatus: "failed",
				Text: `{"url":"https://example.com"}
PreToolUse:WebFetch hook error: context-mode: WebFetch redirected.
[failed]`,
			},
			wantBlocked: true,
			wantFailed:  false,
		},
		{
			name:        "ToolStatus blocked 且正文无 hook 前缀",
			msg:         Message{Role: "tool", ToolStatus: "blocked", Text: `{"url":"https://example.com"}`},
			wantBlocked: true,
			wantFailed:  false,
		},
		{
			name: "ToolStatus blocked 且正文末尾有 [failed]",
			msg: Message{
				Role:       "tool",
				ToolStatus: "blocked",
				Text:       "redirected by hook\n[failed]",
			},
			wantBlocked: true,
			wantFailed:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isToolBlockedByHook(tc.msg); got != tc.wantBlocked {
				t.Fatalf("isToolBlockedByHook() = %v, want %v", got, tc.wantBlocked)
			}
			if got := isToolFailed(tc.msg); got != tc.wantFailed {
				t.Fatalf("isToolFailed() = %v, want %v", got, tc.wantFailed)
			}
		})
	}
}

// TestToolIsRunningCommandBlocked 验证被 hook 拦截的命令类工具不再被当作「仍在运行」。
func TestToolIsRunningCommandBlocked(t *testing.T) {
	blocked := Message{Role: "tool", ToolName: "bash", ToolStatus: "blocked", Text: "命令被拦截"}
	if toolIsRunningCommand(blocked) {
		t.Fatalf("toolIsRunningCommand(blocked) = true, want false")
	}
	running := Message{Role: "tool", ToolName: "bash", ToolStatus: "running", Text: "运行中..."}
	if !toolIsRunningCommand(running) {
		t.Fatalf("toolIsRunningCommand(running) = false, want true")
	}
}

// TestInterruptedToolNoticeRendersAsFailedNotRunning 验证崩溃恢复写入的中断说明
// 让工具消息按失败态渲染，而不会被当成仍在运行。
func TestInterruptedToolNoticeRendersAsFailedNotRunning(t *testing.T) {
	m := Message{
		Role:       "tool",
		ToolName:   "bash",
		ToolStatus: "failed",
		Text:       "make test\n\n工具执行被中断：宿主重启，未收到结果。",
	}
	if !isToolFailed(m) {
		t.Fatal("isToolFailed(interrupted tool) = false, want true")
	}
	if isToolRunning(m) {
		t.Fatal("isToolRunning(interrupted tool) = true, want false")
	}
	if toolIsRunningCommand(m) {
		t.Fatal("toolIsRunningCommand(interrupted tool) = true, want false")
	}
}
