package claude

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"mcode/internal/harness"
)

const (
	fakeClaudeEnv        = "MCODE_TEST_FAKE_CLAUDE"
	fakeClaudePwdFileEnv = "MCODE_TEST_FAKE_CLAUDE_PWD_FILE"
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeClaudeAuthEnv) == "1" {
		runFakeClaudeAuth()
		return
	}
	if mode := os.Getenv(fakeClaudeEnv); mode != "" {
		runFakeClaude(mode)
		return
	}
	os.Exit(m.Run())
}

func runFakeClaudeAuth() {
	_, _ = os.Stdout.WriteString(os.Getenv(fakeClaudeAuthStdoutEnv))
	code, _ := strconv.Atoi(os.Getenv(fakeClaudeAuthExitEnv))
	os.Exit(code)
}

func runFakeClaude(mode string) {
	if pwdFile := os.Getenv(fakeClaudePwdFileEnv); pwdFile != "" {
		_ = os.WriteFile(pwdFile, []byte(os.Getenv("PWD")), 0600)
	}

	// MCODE_TEST_FAKE_LOG 非空时把本次启动的 argv 追加成一行：用例据此断言“启动了几次”
	// 以及 argv 里有没有 --resume、--input-format 这类关键参数；不设置时对既有模式没有任何影响。
	if logPath := os.Getenv(fakeClaudeLogEnv); logPath != "" {
		if file, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600); err == nil {
			_, _ = file.WriteString(strings.Join(os.Args[1:], " ") + "\n")
			_ = file.Close()
		}
	}

	// hang：不产出任何输出也不退出，用于验证输出看门狗把挂起回报为错误。
	if mode == "hang" {
		time.Sleep(10 * time.Minute)
		os.Exit(0)
	}

	// die_silent_then:<marker>|<fixture>：首次运行不产出任何输出直接退出（模拟空闲后子进程已被回收、管道已断），
	// 之后运行输出 fixture，用于验证 Run 会用 resume 重新拉起并重发同一 prompt。
	if strings.HasPrefix(mode, "die_silent_then:") {
		parts := strings.SplitN(strings.TrimPrefix(mode, "die_silent_then:"), "|", 2)
		if len(parts) == 2 {
			marker, fixture := parts[0], parts[1]
			if _, err := os.Stat(marker); err != nil {
				_ = os.WriteFile(marker, []byte("1"), 0600)
				os.Exit(1)
			}
			mode = fixture
		}
	}

	if mode == "exit_error" {
		_, _ = fmt.Fprintln(os.Stderr, "claude failed with fatal error")
		os.Exit(1)
	}

	// read_silent:<counter>：读完 stdin 后不产出任何输出直接失败，并把尝试次数追加到 counter。
	// 用于验证 prompt 已写进子进程 stdin 时 Run 不再自动重试（否则工具副作用会重放）。
	if strings.HasPrefix(mode, "read_silent:") {
		counter := strings.TrimPrefix(mode, "read_silent:")
		if file, err := os.OpenFile(counter, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600); err == nil {
			_, _ = file.WriteString("x\n")
			_ = file.Close()
		}
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(1)
	}

	// resident / resident_die_after_first / resident_block：常驻假 CLI（实现见 resident_test.go），
	// 逐行读 stdin 的 stream-json 帧并回放 fixture。
	if strings.HasPrefix(mode, fakeResidentModePrefix) {
		runFakeResidentClaude(mode)
		return
	}

	// read_then:<fixture>：先把 stdin 读空再回放 fixture；用于验证送达信号排在第一段正文之前。
	if strings.HasPrefix(mode, "read_then:") {
		_, _ = io.Copy(io.Discard, os.Stdin)
		mode = strings.TrimPrefix(mode, "read_then:")
	}

	data, err := os.ReadFile(mode)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to read fixture file %s: %v\n", mode, err)
		os.Exit(2)
	}
	_, _ = os.Stdout.Write(data)
	os.Exit(0)
}

func TestBuildArgs(t *testing.T) {
	// 目录未加载时 auto 保持不变：固定成空目录，避免受本机真实模型目录影响。
	withModelsForTest(t, nil)
	tests := []struct {
		name     string
		turn     harness.Turn
		extra    []string
		wantArgs []string
	}{
		{
			name: "默认与ModeAgent模式",
			turn: harness.Turn{
				Mode: harness.ModeAgent,
			},
			wantArgs: []string{
				"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--disallowed-tools", "Agent,Task",
				"--permission-mode", "auto",
			},
		},
		{
			name: "空权限模式默认为auto",
			turn: harness.Turn{},
			wantArgs: []string{
				"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--disallowed-tools", "Agent,Task",
				"--permission-mode", "auto",
			},
		},
		{
			name: "ModeReadOnly映射为plan",
			turn: harness.Turn{
				Mode: harness.ModeReadOnly,
			},
			wantArgs: []string{
				"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--disallowed-tools", "Agent,Task",
				"--permission-mode", "plan",
			},
		},
		{
			name: "ModeSupervised映射为default",
			turn: harness.Turn{
				Mode: harness.ModeSupervised,
			},
			wantArgs: []string{
				"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--disallowed-tools", "Agent,Task",
				"--permission-mode", "default",
			},
		},
		{
			name: "ModeAutoAcceptEdits映射为auto",
			turn: harness.Turn{
				Mode: harness.ModeAutoAcceptEdits,
			},
			wantArgs: []string{
				"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--disallowed-tools", "Agent,Task",
				"--permission-mode", "auto",
			},
		},
		{
			name: "ModeAuto映射为auto",
			turn: harness.Turn{
				Mode: harness.ModeAuto,
			},
			wantArgs: []string{
				"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--disallowed-tools", "Agent,Task",
				"--permission-mode", "auto",
			},
		},
		{
			name: "ModeFullAccess映射为bypassPermissions",
			turn: harness.Turn{
				Mode: harness.ModeFullAccess,
			},
			wantArgs: []string{
				"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--disallowed-tools", "Agent,Task",
				"--allow-dangerously-skip-permissions",
				"--permission-mode", "bypassPermissions",
			},
		},
		{
			name: "包含Model、SessionID与ExtraArgs",
			turn: harness.Turn{
				Model:     "claude-3-7-sonnet",
				SessionID: "sess-resume-123",
				Mode:      harness.ModeAgent,
			},
			extra: []string{"--custom-flag", "val"},
			wantArgs: []string{
				"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--disallowed-tools", "Agent,Task",
				"--model", "claude-3-7-sonnet",
				"--resume", "sess-resume-123",
				"--permission-mode", "auto",
				"--custom-flag", "val",
			},
		},
		{
			name: "Model为default时不传--model",
			turn: harness.Turn{Model: "default"},
			wantArgs: []string{
				"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--disallowed-tools", "Agent,Task",
				"--permission-mode", "auto",
			},
		},
		{
			name: "传递推理强度",
			turn: harness.Turn{Effort: "high"},
			wantArgs: []string{
				"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--disallowed-tools", "Agent,Task",
				"--effort", "high", "--permission-mode", "auto",
			},
		},
		{
			name: "Plan交互模式覆盖为plan",
			turn: harness.Turn{
				Mode:            harness.ModeFullAccess,
				InteractionMode: "plan",
			},
			wantArgs: []string{
				"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--disallowed-tools", "Agent,Task",
				"--allow-dangerously-skip-permissions",
				"--permission-mode", "plan",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildArgs(tc.turn, tc.extra...)
			if !reflect.DeepEqual(got, tc.wantArgs) {
				t.Errorf("buildArgs() = %v, want %v", got, tc.wantArgs)
			}
		})
	}
}

func TestBuildEnv(t *testing.T) {
	tests := []struct {
		name    string
		base    []string
		workDir string
		extra   []string
		want    []string
	}{
		{
			name:    "去掉旧的PWD并追加workDir",
			base:    []string{"PATH=/bin", "PWD=/old/path", "USER=alice"},
			workDir: "/new/path",
			extra:   nil,
			want:    []string{"PATH=/bin", "USER=alice", "PWD=/new/path", "CLAUDE_CODE_ENABLE_TODO_TOOLS=1"},
		},
		{
			name:    "去掉多个旧的PWD项",
			base:    []string{"PWD=/old1", "A=1", "PWD=/old2", "B=2"},
			workDir: "/target/path",
			extra:   []string{"C=3"},
			want:    []string{"A=1", "B=2", "PWD=/target/path", "CLAUDE_CODE_ENABLE_TODO_TOOLS=1", "C=3"},
		},
		{
			name:    "workDir为空时不加PWD",
			base:    []string{"PATH=/bin", "PWD=/old/path", "USER=bob"},
			workDir: "",
			extra:   []string{"EXTRA=1"},
			want:    []string{"PATH=/bin", "USER=bob", "CLAUDE_CODE_ENABLE_TODO_TOOLS=1", "EXTRA=1"},
		},
		{
			name:    "extra里的PWD排在最后",
			base:    []string{"PATH=/usr/bin", "PWD=/old/path"},
			workDir: "/work/path",
			extra:   []string{"EXTRA_VAR=1", "PWD=/override/pwd"},
			want:    []string{"PATH=/usr/bin", "PWD=/work/path", "CLAUDE_CODE_ENABLE_TODO_TOOLS=1", "EXTRA_VAR=1", "PWD=/override/pwd"},
		},
		{
			name:    "base为空时不设置子代理模型",
			base:    nil,
			workDir: "",
			extra:   nil,
			want:    []string{"CLAUDE_CODE_ENABLE_TODO_TOOLS=1"},
		},
		{
			name:    "base已设置子代理模型时不重复追加",
			base:    []string{"PATH=/bin", "CLAUDE_CODE_SUBAGENT_MODEL=sonnet"},
			workDir: "",
			extra:   nil,
			want:    []string{"PATH=/bin", "CLAUDE_CODE_SUBAGENT_MODEL=sonnet", "CLAUDE_CODE_ENABLE_TODO_TOOLS=1"},
		},
		{
			name:    "extra里的子代理模型排在最后并生效",
			base:    []string{"PATH=/bin"},
			workDir: "",
			extra:   []string{"CLAUDE_CODE_SUBAGENT_MODEL=opus"},
			want:    []string{"PATH=/bin", "CLAUDE_CODE_ENABLE_TODO_TOOLS=1", "CLAUDE_CODE_SUBAGENT_MODEL=opus"},
		},
		{
			name:    "base已禁用待办工具时保留设置且不重复追加",
			base:    []string{"CLAUDE_CODE_ENABLE_TODO_TOOLS=0"},
			workDir: "",
			extra:   nil,
			want:    []string{"CLAUDE_CODE_ENABLE_TODO_TOOLS=0"},
		},
		{
			name:    "extra里的待办工具设置排在最后并生效",
			base:    nil,
			workDir: "",
			extra:   []string{"CLAUDE_CODE_ENABLE_TODO_TOOLS=0"},
			want:    []string{"CLAUDE_CODE_ENABLE_TODO_TOOLS=1", "CLAUDE_CODE_ENABLE_TODO_TOOLS=0"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildEnv(tc.base, tc.workDir, tc.extra)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("buildEnv() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParser_Deduplication(t *testing.T) {
	p := newParser()
	var events []harness.Event
	emit := func(e harness.Event) {
		events = append(events, e)
	}

	lines := []string{
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_1"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello "}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world!"}}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Hello world!"}]}}`,
	}

	for _, l := range lines {
		if err := p.handleLine([]byte(l), emit); err != nil {
			t.Fatalf("handleLine error: %v", err)
		}
	}

	var textEvents []harness.Event
	for _, e := range events {
		if e.Type == harness.EventText {
			textEvents = append(textEvents, e)
		}
	}

	if len(textEvents) != 2 {
		t.Fatalf("expected 2 text events, got %d: %+v", len(textEvents), textEvents)
	}
	if textEvents[0].Text != "Hello " || textEvents[1].Text != "world!" {
		t.Errorf("unexpected text content: %+v", textEvents)
	}
}

func TestParser_ReasoningDeduplication(t *testing.T) {
	p := newParser()
	var events []harness.Event
	emit := func(e harness.Event) {
		events = append(events, e)
	}

	lines := []string{
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_1"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"thinking part 1"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":" and part 2"}}}`,
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"thinking part 1 and part 2"}]}}`,
	}

	for _, l := range lines {
		if err := p.handleLine([]byte(l), emit); err != nil {
			t.Fatalf("handleLine error: %v", err)
		}
	}

	var reasoningEvents []harness.Event
	for _, e := range events {
		if e.Type == harness.EventReasoning {
			reasoningEvents = append(reasoningEvents, e)
		}
	}

	if len(reasoningEvents) != 2 {
		t.Fatalf("expected 2 reasoning events, got %d", len(reasoningEvents))
	}
	if reasoningEvents[0].Text != "thinking part 1" || reasoningEvents[1].Text != " and part 2" {
		t.Errorf("unexpected reasoning content: %+v", reasoningEvents)
	}
}

func TestParser_PrefixDeduplicationAndSubagentFilter(t *testing.T) {
	fixtureDir, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatalf("filepath.Abs error: %v", err)
	}

	tests := []struct {
		name     string
		fixture  string
		validate func(t *testing.T, events []harness.Event)
	}{
		{
			name:    "(a) thinking_delta后正文以整块到达能完整发出",
			fixture: "thinking_then_text_block.jsonl",
			validate: func(t *testing.T, events []harness.Event) {
				var reasoningEvents []harness.Event
				var textEvents []harness.Event
				for _, e := range events {
					switch e.Type {
					case harness.EventReasoning:
						reasoningEvents = append(reasoningEvents, e)
					case harness.EventText:
						textEvents = append(textEvents, e)
					}
				}
				if len(reasoningEvents) != 1 || reasoningEvents[0].Text != "thinking deeply" {
					t.Errorf("expected 1 reasoning event with 'thinking deeply', got: %+v", reasoningEvents)
				}
				if len(textEvents) != 1 || textEvents[0].Text != "here is the final answer" {
					t.Errorf("expected 1 text event with 'here is the final answer', got: %+v", textEvents)
				}
			},
		},
		{
			name:    "(b) text_delta前半段到达后补发后半段且不重复",
			fixture: "partial_text_delta.jsonl",
			validate: func(t *testing.T, events []harness.Event) {
				var textEvents []harness.Event
				for _, e := range events {
					if e.Type == harness.EventText {
						textEvents = append(textEvents, e)
					}
				}
				if len(textEvents) != 2 {
					t.Fatalf("expected 2 text events, got %d: %+v", len(textEvents), textEvents)
				}
				if textEvents[0].Text != "Hello " {
					t.Errorf("expected first delta 'Hello ', got %q", textEvents[0].Text)
				}
				if textEvents[1].Text != "world!" {
					t.Errorf("expected suffix 'world!', got %q", textEvents[1].Text)
				}
				var combined strings.Builder
				for _, e := range textEvents {
					combined.WriteString(e.Text)
				}
				if combined.String() != "Hello world!" {
					t.Errorf("expected total text 'Hello world!', got %q", combined.String())
				}
			},
		},
		{
			name:    "(c) text_delta完整送达后整块到达不重复发出",
			fixture: "full_text_delta.jsonl",
			validate: func(t *testing.T, events []harness.Event) {
				var textEvents []harness.Event
				for _, e := range events {
					if e.Type == harness.EventText {
						textEvents = append(textEvents, e)
					}
				}
				if len(textEvents) != 2 {
					t.Fatalf("expected exactly 2 text events from stream, got %d: %+v", len(textEvents), textEvents)
				}
				if textEvents[0].Text != "Hello " || textEvents[1].Text != "world!" {
					t.Errorf("unexpected text events: %+v", textEvents)
				}
			},
		},
		{
			name:    "(d) 带parent_tool_use_id忽略正文与思考但保留tool_use",
			fixture: "subagent_filter.jsonl",
			validate: func(t *testing.T, events []harness.Event) {
				for _, e := range events {
					if e.Type == harness.EventText || e.Type == harness.EventReasoning {
						t.Errorf("subagent text/reasoning should be ignored, got: %+v", e)
					}
				}
				if len(events) != 2 {
					t.Fatalf("expected 2 events (tool_start, tool_end), got %d: %+v", len(events), events)
				}
				if events[0].Type != harness.EventToolStart || events[0].ToolID != "sub_tool_1" || events[0].ToolName != "Bash" {
					t.Errorf("expected tool_start for sub_tool_1/Bash, got: %+v", events[0])
				}
				if events[1].Type != harness.EventToolEnd || events[1].ToolID != "sub_tool_1" || events[1].Output != "/tmp\n" {
					t.Errorf("expected tool_end for sub_tool_1, got: %+v", events[1])
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			filePath := filepath.Join(fixtureDir, tc.fixture)
			data, err := os.ReadFile(filePath)
			if err != nil {
				t.Fatalf("failed to read fixture file %s: %v", filePath, err)
			}

			p := newParser()
			var events []harness.Event
			emit := func(e harness.Event) {
				events = append(events, e)
			}

			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				if err := p.handleLine([]byte(line), emit); err != nil {
					t.Fatalf("handleLine error: %v", err)
				}
			}

			tc.validate(t, events)
		})
	}
}

func TestParser_MultiBlockAndMismatch(t *testing.T) {
	t.Run("多text块各自按index去重与补发", func(t *testing.T) {
		p := newParser()
		var events []harness.Event
		emit := func(e harness.Event) {
			events = append(events, e)
		}

		lines := []string{
			`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_multi"}}}`,
			`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Block 0 full"}}}`,
			`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Block 1 "}}}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"Block 0 full"},{"type":"text","text":"Block 1 suffix"}]}}`,
		}

		for _, l := range lines {
			if err := p.handleLine([]byte(l), emit); err != nil {
				t.Fatalf("handleLine error: %v", err)
			}
		}

		// 预期：
		// index 0 流式发送 "Block 0 full" -> assistant 整块完全匹配，不发出
		// index 1 流式发送 "Block 1 " -> assistant 补发后缀 "suffix"
		if len(events) != 3 {
			t.Fatalf("expected 3 events, got %d: %+v", len(events), events)
		}
		if events[0].Text != "Block 0 full" || events[1].Text != "Block 1 " || events[2].Text != "suffix" {
			t.Errorf("unexpected events: %+v", events)
		}
	})

	t.Run("流与整块内容不一致时不重复发出整块", func(t *testing.T) {
		p := newParser()
		var events []harness.Event
		emit := func(e harness.Event) {
			events = append(events, e)
		}

		lines := []string{
			`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_mis"}}}`,
			`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Stream prefix"}}}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"Different text entirely"}]}}`,
		}

		for _, l := range lines {
			if err := p.handleLine([]byte(l), emit); err != nil {
				t.Fatalf("handleLine error: %v", err)
			}
		}

		// 不一致时只保留流式内容，不重复发出整块
		if len(events) != 1 || events[0].Text != "Stream prefix" {
			t.Fatalf("expected 1 event with 'Stream prefix', got: %+v", events)
		}
	})
}

// 真实 CLI 输出中，正文块的 stream index 为 1（思考块占 0），assistant 事件里该块位于 content[0]。
// 若按位置查找流式内容会找不到，整段正文被当作新内容再发一次。
func TestParser_TextBlockAtNonZeroIndexNotDuplicated(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "text_block_after_thinking.jsonl"))
	if err != nil {
		t.Fatalf("failed to read fixture: %v", err)
	}

	p := newParser()
	var events []harness.Event
	emit := func(e harness.Event) {
		events = append(events, e)
	}

	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if err := p.handleLine([]byte(strings.TrimSpace(line)), emit); err != nil {
			t.Fatalf("handleLine error: %v", err)
		}
	}

	var text, reasoning strings.Builder
	for _, e := range events {
		switch e.Type {
		case harness.EventText:
			text.WriteString(e.Text)
		case harness.EventReasoning:
			reasoning.WriteString(e.Text)
		}
	}

	if want := "明天东京晴，最高气温 22 度。"; text.String() != want {
		t.Errorf("final text = %q, want %q", text.String(), want)
	}
	if want := "需要先搜索天气。整理结果。"; reasoning.String() != want {
		t.Errorf("reasoning = %q, want %q", reasoning.String(), want)
	}
}

func TestParser_ToolUseAndResult(t *testing.T) {
	p := newParser()
	var events []harness.Event
	emit := func(e harness.Event) {
		events = append(events, e)
	}

	lines := []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tool_call_1","name":"Bash","input":{"command":"ls -la"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tool_call_1","content":"file1\nfile2","is_error":false}]}}`,
	}

	for _, l := range lines {
		if err := p.handleLine([]byte(l), emit); err != nil {
			t.Fatalf("handleLine error: %v", err)
		}
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d: %+v", len(events), events)
	}

	if events[0].Type != harness.EventToolStart || events[0].ToolID != "tool_call_1" || events[0].ToolName != "Bash" {
		t.Errorf("unexpected tool_start event: %+v", events[0])
	}
	if events[1].Type != harness.EventToolEnd || events[1].ToolID != "tool_call_1" || events[1].Output != "file1\nfile2" || events[1].IsError {
		t.Errorf("unexpected tool_end event: %+v", events[1])
	}
}

func TestParser_EditEmitsFileChange(t *testing.T) {
	p := newParser()
	var events []harness.Event
	emit := func(e harness.Event) {
		events = append(events, e)
	}

	lines := []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"internal/app.go","old_string":"a","new_string":"b"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t2","name":"Write","input":{"file_path":"README.md","content":"doc"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t3","name":"MultiEdit","input":{"file_path":"main.go"}}]}}`,
	}

	for _, l := range lines {
		if err := p.handleLine([]byte(l), emit); err != nil {
			t.Fatalf("handleLine error: %v", err)
		}
	}

	// 每个 tool_use 发出 1 个 tool_start 和 1 个 file_change，共 6 个事件
	if len(events) != 6 {
		t.Fatalf("expected 6 events, got %d: %+v", len(events), events)
	}

	expectedPaths := []string{"internal/app.go", "README.md", "main.go"}
	for i, path := range expectedPaths {
		startEv := events[i*2]
		fileChangeEv := events[i*2+1]

		if startEv.Type != harness.EventToolStart {
			t.Errorf("event %d: expected tool_start, got %v", i*2, startEv.Type)
		}
		if fileChangeEv.Type != harness.EventFileChange || fileChangeEv.Path != path {
			t.Errorf("event %d: expected file_change with path %s, got %+v", i*2+1, path, fileChangeEv)
		}
	}
}

func TestParser_ResultErrorOrder(t *testing.T) {
	p := newParser()
	var events []harness.Event
	emit := func(e harness.Event) {
		events = append(events, e)
	}

	line := `{"type":"result","subtype":"error","is_error":true,"result":"rate limit exceeded","stop_reason":"error","usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":20}}`
	if err := p.handleLine([]byte(line), emit); err != nil {
		t.Fatalf("handleLine error: %v", err)
	}

	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d: %+v", len(events), events)
	}

	// 1. usage
	if events[0].Type != harness.EventUsage || events[0].Usage == nil {
		t.Fatalf("first event should be usage, got %+v", events[0])
	}
	if events[0].Usage.InputTokens != 100 || events[0].Usage.OutputTokens != 50 || events[0].Usage.CachedTokens != 20 {
		t.Errorf("usage tokens mismatch: %+v", events[0].Usage)
	}

	// 2. error
	if events[1].Type != harness.EventError || events[1].Err != "rate limit exceeded" {
		t.Errorf("second event should be error with 'rate limit exceeded', got %+v", events[1])
	}

	// 3. done
	if events[2].Type != harness.EventDone || events[2].StopReason != "error" {
		t.Errorf("third event should be done with StopReason 'error', got %+v", events[2])
	}

	if !p.sawResult {
		t.Error("parser should mark sawResult = true")
	}
}

func TestParser_UnknownTypeAndInvalidJSON(t *testing.T) {
	p := newParser()
	var events []harness.Event
	emit := func(e harness.Event) {
		events = append(events, e)
	}

	badLines := [][]byte{
		[]byte("not a json string at all"),
		[]byte(`{"type":"future_type_xyz","data":"some payload"}`),
		[]byte(`{"foo":"bar"}`),
	}

	for _, bl := range badLines {
		if err := p.handleLine(bl, emit); err != nil {
			t.Errorf("handleLine should not return error for %s, got: %v", string(bl), err)
		}
	}

	if len(events) != 0 {
		t.Errorf("expected 0 events, got %d", len(events))
	}
}

// TestRun_EmitsTurnAcceptedBeforeContent 验证 prompt 写进子进程 stdin 后恰好上报一次
// EventTurnAccepted，且排在第一段正文之前。
func TestRun_EmitsTurnAcceptedBeforeContent(t *testing.T) {
	fixtureDir, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatalf("filepath.Abs error: %v", err)
	}

	h := New(Options{Binary: os.Args[0]})
	var events []harness.Event
	turn := harness.Turn{
		WorkDir: t.TempDir(),
		Prompt:  "你好",
		Env:     []string{fakeClaudeEnv + "=read_then:" + filepath.Join(fixtureDir, "basic.jsonl")},
	}
	if err := h.Run(context.Background(), turn, func(e harness.Event) {
		events = append(events, e)
	}); err != nil {
		t.Fatalf("Run() returned unexpected error: %v", err)
	}

	accepted, firstContent, count := -1, -1, 0
	for i, event := range events {
		switch event.Type {
		case harness.EventTurnAccepted:
			count++
			if accepted < 0 {
				accepted = i
			}
		case harness.EventText, harness.EventReasoning, harness.EventToolStart:
			if firstContent < 0 {
				firstContent = i
			}
		}
	}
	if count != 1 {
		t.Fatalf("EventTurnAccepted 应恰好出现一次，实际 %d 次: %+v", count, events)
	}
	if firstContent >= 0 && accepted > firstContent {
		t.Fatalf("EventTurnAccepted 应早于第一段正文: %+v", events)
	}
}

func TestRun_Integration(t *testing.T) {
	fixtureDir, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatalf("filepath.Abs error: %v", err)
	}

	t.Run("basic流程集成测试", func(t *testing.T) {
		h := New(Options{
			Binary: os.Args[0],
		})

		var events []harness.Event
		turn := harness.Turn{
			WorkDir: t.TempDir(),
			Env:     []string{fakeClaudeEnv + "=" + filepath.Join(fixtureDir, "basic.jsonl")},
		}

		err := h.Run(context.Background(), turn, func(e harness.Event) {
			events = append(events, e)
		})
		if err != nil {
			t.Fatalf("Run() returned unexpected error: %v", err)
		}

		// 断言收到的事件序列
		expectedTypes := []harness.EventType{
			harness.EventSession,
			harness.EventText,
			harness.EventText,
			harness.EventUsage,
			harness.EventDone,
		}
		if len(events) != len(expectedTypes) {
			t.Fatalf("expected %d events, got %d: %+v", len(expectedTypes), len(events), events)
		}
		for i, exp := range expectedTypes {
			if events[i].Type != exp {
				t.Errorf("event %d: expected type %v, got %v", i, exp, events[i].Type)
			}
		}
	})

	t.Run("tool_use流程集成测试", func(t *testing.T) {
		h := New(Options{
			Binary: os.Args[0],
		})

		var events []harness.Event
		turn := harness.Turn{
			WorkDir: t.TempDir(),
			Env:     []string{fakeClaudeEnv + "=" + filepath.Join(fixtureDir, "tool_use.jsonl")},
		}

		err := h.Run(context.Background(), turn, func(e harness.Event) {
			events = append(events, e)
		})
		if err != nil {
			t.Fatalf("Run() returned unexpected error: %v", err)
		}

		expectedTypes := []harness.EventType{
			harness.EventSession,
			harness.EventToolStart,
			harness.EventFileChange,
			harness.EventToolEnd,
			harness.EventUsage,
			harness.EventDone,
		}
		if len(events) != len(expectedTypes) {
			t.Fatalf("expected %d events, got %d: %+v", len(expectedTypes), len(events), events)
		}
		for i, exp := range expectedTypes {
			if events[i].Type != exp {
				t.Errorf("event %d: expected type %v, got %v", i, exp, events[i].Type)
			}
		}
	})

	t.Run("非零退出且没有result行用例", func(t *testing.T) {
		h := New(Options{
			Binary: os.Args[0],
		})

		var events []harness.Event
		turn := harness.Turn{
			WorkDir: t.TempDir(),
			Env:     []string{fakeClaudeEnv + "=exit_error"},
		}

		err := h.Run(context.Background(), turn, func(e harness.Event) {
			events = append(events, e)
		})
		if err == nil {
			t.Fatal("Run() should have failed on non-zero exit without result")
		}

		var errorEvents []harness.Event
		for _, e := range events {
			if e.Type == harness.EventError {
				errorEvents = append(errorEvents, e)
			}
		}

		if len(errorEvents) != 1 {
			t.Fatalf("expected 1 error event, got %d: %+v", len(errorEvents), errorEvents)
		}
		if !strings.Contains(errorEvents[0].Err, "claude failed with fatal error") {
			t.Errorf("error event Err should contain stderr tail, got: %q", errorEvents[0].Err)
		}
	})

	t.Run("PWD环境变量与WorkDir一致", func(t *testing.T) {
		h := New(Options{
			Binary: os.Args[0],
		})

		workDir := t.TempDir()
		pwdFile := filepath.Join(t.TempDir(), "pwd.txt")

		var events []harness.Event
		turn := harness.Turn{
			WorkDir: workDir,
			Env: []string{
				fakeClaudeEnv + "=" + filepath.Join(fixtureDir, "basic.jsonl"),
				fakeClaudePwdFileEnv + "=" + pwdFile,
			},
		}

		err := h.Run(context.Background(), turn, func(e harness.Event) {
			events = append(events, e)
		})
		if err != nil {
			t.Fatalf("Run() returned unexpected error: %v", err)
		}

		data, err := os.ReadFile(pwdFile)
		if err != nil {
			t.Fatalf("failed to read recorded pwd file: %v", err)
		}
		if got := string(data); got != workDir {
			t.Errorf("expected PWD %q, got %q", workDir, got)
		}
	})

	t.Run("首包超时把挂起回报为错误", func(t *testing.T) {
		h := New(Options{
			Binary:             os.Args[0],
			FirstOutputTimeout: 200 * time.Millisecond,
		})

		var events []harness.Event
		turn := harness.Turn{
			WorkDir: t.TempDir(),
			Env:     []string{fakeClaudeEnv + "=hang"},
		}

		start := time.Now()
		err := h.Run(context.Background(), turn, func(e harness.Event) {
			events = append(events, e)
		})
		elapsed := time.Since(start)
		if err == nil {
			t.Fatal("Run() 应在首包超时后返回错误，而不是无声挂起")
		}
		if !strings.Contains(err.Error(), "produced no output") {
			t.Errorf("错误信息应说明没有输出，实际: %v", err)
		}
		// hang 进程始终不产出任何输出，会先被内部 resume 重试一次：耗时至少两轮首包超时。
		if elapsed < 350*time.Millisecond {
			t.Errorf("挂起进程应经过 resume 重试后才失败，耗时仅 %s", elapsed)
		}

		var errorEvents []harness.Event
		for _, e := range events {
			if e.Type == harness.EventError {
				errorEvents = append(errorEvents, e)
			}
		}
		// 首轮失败属于内部重试，不上报；只有重试后仍失败的那次上报一个错误事件。
		if len(errorEvents) != 1 {
			t.Fatalf("expected 1 error event (仅最终失败上报), got %d: %+v", len(errorEvents), errorEvents)
		}
		if !strings.Contains(errorEvents[0].Err, "produced no output") {
			t.Errorf("error event Err should explain the timeout, got: %q", errorEvents[0].Err)
		}
	})

	t.Run("无输出失效后用resume重试成功", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "first-run-done")
		mode := "die_silent_then:" + marker + "|" + filepath.Join(fixtureDir, "basic.jsonl")

		h := New(Options{
			Binary:             os.Args[0],
			FirstOutputTimeout: 5 * time.Second,
		})

		var events []harness.Event
		turn := harness.Turn{
			WorkDir:   t.TempDir(),
			SessionID: "sess-idle-resume",
			Env:       []string{fakeClaudeEnv + "=" + mode},
		}

		err := h.Run(context.Background(), turn, func(e harness.Event) {
			events = append(events, e)
		})
		if err != nil {
			t.Fatalf("Run() 应在 resume 重试后成功，实际错误: %v", err)
		}
		if _, statErr := os.Stat(marker); statErr != nil {
			t.Fatalf("首次尝试没有执行: %v", statErr)
		}

		sessionEvents := 0
		for _, e := range events {
			if e.Type == harness.EventSession {
				sessionEvents++
			}
			if e.Type == harness.EventError {
				t.Errorf("内部 resume 重试成功时不应上报错误事件: %+v", e)
			}
		}
		if sessionEvents != 1 {
			t.Fatalf("重试后应只产出 1 个 session 事件，实际 %d: %+v", sessionEvents, events)
		}
	})
}

// TestRun_NoRetryAfterPromptDelivered 验证 prompt 已经写进子进程 stdin 后不再自动重试：
// 用同一个 prompt 重跑会把工具调用等副作用再执行一遍。
func TestRun_NoRetryAfterPromptDelivered(t *testing.T) {
	newTurn := func(marker, prompt string) harness.Turn {
		return harness.Turn{
			WorkDir:   t.TempDir(),
			SessionID: "sess-no-retry",
			Prompt:    prompt,
			Env:       []string{fakeClaudeEnv + "=read_silent:" + marker},
		}
	}
	attempts := func(marker string) int {
		data, err := os.ReadFile(marker)
		if err != nil {
			return 0
		}
		return len(strings.Split(strings.TrimSpace(string(data)), "\n"))
	}

	t.Run("prompt已写入时只尝试一次", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "attempts")
		h := New(Options{Binary: os.Args[0], FirstOutputTimeout: 5 * time.Second})
		if err := h.Run(context.Background(), newTurn(marker, "先删掉 build 目录再告诉我结果"), func(harness.Event) {}); err == nil {
			t.Fatalf("Run() 应在 CLI 静默失败时返回错误")
		}
		if got := attempts(marker); got != 1 {
			t.Errorf("prompt 已投递后不应重试，实际尝试 %d 次", got)
		}
	})

	t.Run("没有prompt时仍允许重试一次", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "attempts")
		h := New(Options{Binary: os.Args[0], FirstOutputTimeout: 5 * time.Second})
		if err := h.Run(context.Background(), newTurn(marker, ""), func(harness.Event) {}); err == nil {
			t.Fatalf("Run() 应在 CLI 静默失败时返回错误")
		}
		if got := attempts(marker); got != 2 {
			t.Errorf("未投递 prompt 时应重试一次，实际尝试 %d 次", got)
		}
	})
}

// writeTestImage 在 dir 下生成一个测试图片文件，返回绝对路径（只看扩展名与字节内容）。
func writeTestImage(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("写测试图片失败: %v", err)
	}
	return path
}

// TestBuildUserContent 验证 user 消息 content 的组装：无图片时保持字符串，有图片时
// 是 text 块 + image 块，读不了/不支持/过大的图片被跳过。
func TestBuildUserContent(t *testing.T) {
	dir := t.TempDir()
	pngData := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a}
	pngPath := writeTestImage(t, dir, "shot.png", pngData)
	jpegPath := writeTestImage(t, dir, "photo.jpeg", []byte{0xff, 0xd8, 0xff, 0xe0})
	textPath := writeTestImage(t, dir, "note.txt", []byte("not an image"))
	missingPath := filepath.Join(dir, "gone.png")

	// 用稀疏文件把体积撑到上限以上，不真的占 5MB 磁盘。
	largePath := filepath.Join(dir, "huge.png")
	if err := os.WriteFile(largePath, nil, 0o600); err != nil {
		t.Fatalf("创建超大图片占位文件失败: %v", err)
	}
	if err := os.Truncate(largePath, maxUserImageBytes+1); err != nil {
		t.Fatalf("撑大超大图片失败: %v", err)
	}

	t.Run("无图片时仍是字符串", func(t *testing.T) {
		content := buildUserContent(harness.Turn{Prompt: "看看这个"})
		got, ok := content.(string)
		if !ok || got != "看看这个" {
			t.Fatalf("无图片时 content 应是 prompt 原文，实际 %#v", content)
		}
		// 序列化形状与改造前完全一致：content 仍是 JSON 字符串。
		data, err := json.Marshal(stdinUserMessage{Role: "user", Content: content})
		if err != nil {
			t.Fatalf("序列化 user 消息失败: %v", err)
		}
		if want := `{"role":"user","content":"看看这个"}`; string(data) != want {
			t.Errorf("无图片时序列化结果应为 %s，实际 %s", want, data)
		}
	})

	t.Run("图片按扩展名输出base64块", func(t *testing.T) {
		content := buildUserContent(harness.Turn{Prompt: "看图", Images: []string{pngPath, jpegPath}})
		blocks, ok := content.([]userContentBlock)
		if !ok || len(blocks) != 3 {
			t.Fatalf("应产出 1 个 text 块 + 2 个 image 块，实际 %#v", content)
		}
		if blocks[0].Type != "text" || blocks[0].Text != "看图" {
			t.Errorf("首个块应是 prompt 文本，实际 %#v", blocks[0])
		}
		if blocks[1].Source == nil || blocks[1].Source.MediaType != "image/png" || blocks[1].Source.Type != "base64" {
			t.Errorf("png 图片块不对: %#v", blocks[1])
		}
		if want := base64.StdEncoding.EncodeToString(pngData); blocks[1].Source.Data != want {
			t.Errorf("png base64 应为 %q，实际 %q", want, blocks[1].Source.Data)
		}
		if blocks[2].Source == nil || blocks[2].Source.MediaType != "image/jpeg" {
			t.Errorf("jpeg 图片块不对: %#v", blocks[2])
		}
	})

	t.Run("无扩展名的png按内容识别", func(t *testing.T) {
		// 会话媒体目录里的历史文件名是不带扩展名的 UUID，只能靠魔数识别。
		namelessPath := writeTestImage(t, dir, "f47ac10b-58cc-4372-a567-0e02b2c3d479", []byte("\x89PNG\r\n\x1a\n"))
		content := buildUserContent(harness.Turn{Prompt: "看图", Images: []string{namelessPath}})
		blocks, ok := content.([]userContentBlock)
		if !ok || len(blocks) != 2 {
			t.Fatalf("无扩展名的 png 也应产出 1 个 text 块 + 1 个 image 块，实际 %#v", content)
		}
		if blocks[1].Source == nil || blocks[1].Source.MediaType != "image/png" {
			t.Errorf("无扩展名的 png 图片块不对: %#v", blocks[1])
		}
	})

	t.Run("无效图片被跳过", func(t *testing.T) {
		content := buildUserContent(harness.Turn{
			Prompt: "混合附件",
			Images: []string{missingPath, textPath, largePath, pngPath},
		})
		blocks, ok := content.([]userContentBlock)
		if !ok || len(blocks) != 2 {
			t.Fatalf("只应保留一张可用图片（不存在的/非图片/超大的都跳过），实际 %#v", content)
		}
		if blocks[1].Source == nil || blocks[1].Source.MediaType != "image/png" {
			t.Errorf("保留的应是那张 png，实际 %#v", blocks[1])
		}
	})

	t.Run("一张可用图片都没有时退回字符串", func(t *testing.T) {
		content := buildUserContent(harness.Turn{Prompt: "只有坏图", Images: []string{missingPath, textPath, largePath}})
		if got, ok := content.(string); !ok || got != "只有坏图" {
			t.Fatalf("没有可用图片时 content 应退回字符串，实际 %#v", content)
		}
	})
}

// TestStdinPrompt 验证一次性路径写到 stdin 的内容：无图片时就是 prompt 原文，
// 有图片时是一帧 stream-json user 消息。
func TestStdinPrompt(t *testing.T) {
	dir := t.TempDir()
	pngPath := writeTestImage(t, dir, "shot.png", []byte{0x89, 'P', 'N', 'G'})

	t.Run("无图片时原样返回prompt", func(t *testing.T) {
		if got := stdinPrompt(harness.Turn{Prompt: "1+1=?"}); got != "1+1=?" {
			t.Errorf("无图片时应原样写 prompt，实际 %q", got)
		}
	})

	t.Run("有图片时是一帧stream-json", func(t *testing.T) {
		line := stdinPrompt(harness.Turn{Prompt: "看图", Images: []string{pngPath}})
		if !strings.HasSuffix(line, "\n") {
			t.Errorf("stdin 帧应以换行结尾，实际 %q", line)
		}
		if strings.Contains(line, "\n\n") {
			t.Errorf("stdin 帧应是单行，实际 %q", line)
		}
		var frame struct {
			Type    string           `json:"type"`
			Message stdinUserMessage `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatalf("解析 stdin 帧失败: %v（帧：%s）", err, line)
		}
		if frame.Type != "user" || frame.Message.Role != "user" {
			t.Fatalf("帧形状不对: %+v", frame)
		}
		blocks, ok := frame.Message.Content.([]any)
		if !ok || len(blocks) != 2 {
			t.Fatalf("content 应是 text+image 两块，实际 %s", line)
		}
		if first, _ := blocks[0].(map[string]any); first["type"] != "text" || first["text"] != "看图" {
			t.Errorf("首个块应是 prompt 文本，实际 %#v", blocks[0])
		}
		second, _ := blocks[1].(map[string]any)
		source, _ := second["source"].(map[string]any)
		if second["type"] != "image" || source["type"] != "base64" || source["media_type"] != "image/png" {
			t.Errorf("图片块不对: %#v", second)
		}
	})
}

// TestRun_ImageTurnUsesStreamJSONInput 验证一次性路径只在带图片时改成 stream-json 输入，
// 且不引入常驻路径专用的 --replay-user-messages。
func TestRun_ImageTurnUsesStreamJSONInput(t *testing.T) {
	fixture := fixtureAbs(t, "basic.jsonl")
	pngPath := writeTestImage(t, t.TempDir(), "shot.png", []byte{0x89, 'P', 'N', 'G'})

	cases := []struct {
		name   string
		images []string
		want   bool
	}{
		{name: "带图片", images: []string{pngPath}, want: true},
		{name: "无图片", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "starts.log")
			h := New(Options{Binary: os.Args[0]})
			turn := harness.Turn{
				WorkDir: t.TempDir(),
				Prompt:  "看看这张图",
				Images:  tc.images,
				Env:     []string{fakeClaudeEnv + "=" + fixture, fakeClaudeLogEnv + "=" + logPath},
			}
			if err := h.Run(context.Background(), turn, func(harness.Event) {}); err != nil {
				t.Fatalf("Run() 返回错误: %v", err)
			}

			starts := fakeStartArgvs(t, logPath)
			if len(starts) != 1 {
				t.Fatalf("应只启动一次进程，实际 %d 次: %v", len(starts), starts)
			}
			if got := strings.Contains(starts[0], "--input-format stream-json"); got != tc.want {
				t.Errorf("--input-format stream-json 存在=%v，期望 %v（argv：%s）", got, tc.want, starts[0])
			}
			if strings.Contains(starts[0], "--replay-user-messages") {
				t.Errorf("一次性路径不该带 --replay-user-messages: %s", starts[0])
			}
		})
	}
}

type testDummyApprover struct{}

func (testDummyApprover) Approve(ctx context.Context, req harness.ApprovalRequest) (harness.Decision, error) {
	return harness.DecisionAllow, nil
}

func TestApprovalArgs(t *testing.T) {
	dummyApprover := testDummyApprover{}
	serverURL := "http://127.0.0.1:54321/mcp"
	token := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	t.Run("有Approver且模式为agent", func(t *testing.T) {
		turn := harness.Turn{
			Mode:     harness.ModeAgent,
			Approver: dummyApprover,
		}
		args := buildMCPArgs(turn, serverURL, token, harness.BuiltinMCP{}, nil)
		if len(args) == 0 {
			t.Fatalf("expected approval args, got empty")
		}

		var hasPromptTool, hasMCPConfig bool
		var configStr string
		for i := 0; i < len(args); i++ {
			if args[i] == "--permission-prompt-tool" && i+1 < len(args) {
				if args[i+1] == "mcp__mcode_approval__approve" {
					hasPromptTool = true
				}
			}
			if args[i] == "--mcp-config" && i+1 < len(args) {
				hasMCPConfig = true
				configStr = args[i+1]
			}
		}

		if !hasPromptTool {
			t.Errorf("expected --permission-prompt-tool mcp__mcode_approval__approve in args: %v", args)
		}
		if !hasMCPConfig {
			t.Fatalf("expected --mcp-config in args: %v", args)
		}

		var cfg mcpConfig
		if err := json.Unmarshal([]byte(configStr), &cfg); err != nil {
			t.Fatalf("unmarshal mcpConfig failed: %v", err)
		}
		mcodeServer, ok := cfg.MCPServers["mcode_approval"]
		if !ok {
			t.Fatalf("mcode_approval server not found in config: %s", configStr)
		}
		if mcodeServer.Type != "http" {
			t.Errorf("expected type http, got %s", mcodeServer.Type)
		}
		if mcodeServer.URL != serverURL {
			t.Errorf("expected url %s, got %s", serverURL, mcodeServer.URL)
		}
		if mcodeServer.Headers["Authorization"] != "Bearer "+token {
			t.Errorf("expected Authorization Bearer %s, got %s", token, mcodeServer.Headers["Authorization"])
		}

		// 测试与 buildArgs 组合后的完整命令行
		fullArgs := buildArgs(turn, args...)
		foundPromptTool := false
		foundMCPConfig := false
		for i := 0; i < len(fullArgs); i++ {
			if fullArgs[i] == "--permission-prompt-tool" && i+1 < len(fullArgs) && fullArgs[i+1] == "mcp__mcode_approval__approve" {
				foundPromptTool = true
			}
			if fullArgs[i] == "--mcp-config" {
				foundMCPConfig = true
			}
		}
		if !foundPromptTool || !foundMCPConfig {
			t.Errorf("fullArgs missing approval flags: %v", fullArgs)
		}
	})

	t.Run("fullAccess模式也包含审批参数", func(t *testing.T) {
		// AskUserQuestion 只有带 --permission-prompt-tool 时 CLI 才会提供。
		turn := harness.Turn{
			Mode:     harness.ModeFullAccess,
			Approver: dummyApprover,
		}
		args := buildMCPArgs(turn, serverURL, token, harness.BuiltinMCP{}, nil)
		foundPromptTool := false
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "--permission-prompt-tool" && args[i+1] == "mcp__mcode_approval__approve" {
				foundPromptTool = true
			}
		}
		if !foundPromptTool {
			t.Errorf("fullAccess should keep --permission-prompt-tool, got %v", args)
		}
	})

	t.Run("没有Approver时不包含审批参数", func(t *testing.T) {
		turn := harness.Turn{
			Mode:     harness.ModeAgent,
			Approver: nil,
		}
		args := buildMCPArgs(turn, serverURL, token, harness.BuiltinMCP{}, nil)
		if len(args) != 0 {
			t.Errorf("expected no approval args when Approver is nil, got %v", args)
		}

		fullArgs := buildArgs(turn, args...)
		for _, a := range fullArgs {
			if a == "--permission-prompt-tool" || a == "--mcp-config" {
				t.Errorf("fullArgs should not contain approval flag %s when Approver is nil", a)
			}
		}
	})
}

func TestBuildMCPArgs(t *testing.T) {
	dummyApprover := testDummyApprover{}
	approvalURL := "http://127.0.0.1:54321/mcp"
	approvalToken := "token-approval"
	builtinURL := "http://127.0.0.1:12345/mcp"
	builtinToken := "token-builtin"

	t.Run("审批与内置server合并为一个mcp-config且包含header", func(t *testing.T) {
		turn := harness.Turn{
			Mode:     harness.ModeAgent,
			Approver: dummyApprover,
			WorkDir:  "/workspace/test",
		}

		args := buildMCPArgs(turn, approvalURL, approvalToken, harness.BuiltinMCP{URL: builtinURL, Token: builtinToken}, []harness.MCPEndpoint{{Name: "external", URL: "http://external"}})

		mcpConfigCount := 0
		var configStr string
		hasPromptTool := false
		for i := 0; i < len(args); i++ {
			if args[i] == "--mcp-config" && i+1 < len(args) {
				mcpConfigCount++
				configStr = args[i+1]
			}
			if args[i] == "--permission-prompt-tool" && i+1 < len(args) {
				if args[i+1] == "mcp__mcode_approval__approve" {
					hasPromptTool = true
				}
			}
		}

		if mcpConfigCount != 1 {
			t.Fatalf("expected exactly 1 --mcp-config, got %d", mcpConfigCount)
		}
		if !hasPromptTool {
			t.Errorf("expected --permission-prompt-tool mcp__mcode_approval__approve")
		}

		var cfg mcpConfig
		if err := json.Unmarshal([]byte(configStr), &cfg); err != nil {
			t.Fatalf("unmarshal mcpConfig failed: %v", err)
		}

		// 断言审批 server
		apprSrv, ok := cfg.MCPServers["mcode_approval"]
		if !ok {
			t.Fatalf("mcode_approval not found in servers: %+v", cfg.MCPServers)
		}
		if apprSrv.Type != "http" || apprSrv.URL != approvalURL {
			t.Errorf("unexpected mcode_approval: %+v", apprSrv)
		}
		if apprSrv.Headers["Authorization"] != "Bearer "+approvalToken {
			t.Errorf("unexpected Authorization header for approval: %v", apprSrv.Headers)
		}
		if apprSrv.Timeout != 86400000 {
			t.Errorf("expected approval timeout 86400000, got %d", apprSrv.Timeout)
		}
		var rawConfig struct {
			MCPServers map[string]map[string]json.RawMessage `json:"mcpServers"`
		}
		if err := json.Unmarshal([]byte(configStr), &rawConfig); err != nil {
			t.Fatalf("unmarshal raw config: %v", err)
		}
		for _, name := range []string{"mcode", "external"} {
			if _, ok := rawConfig.MCPServers[name]["timeout"]; ok {
				t.Errorf("unexpected timeout on %s", name)
			}
		}

		// 断言内置 server 与 header
		builtinSrv, ok := cfg.MCPServers["mcode"]
		if !ok {
			t.Fatalf("mcode not found in servers: %+v", cfg.MCPServers)
		}
		if builtinSrv.Type != "http" || builtinSrv.URL != builtinURL {
			t.Errorf("unexpected mcode: %+v", builtinSrv)
		}
		if builtinSrv.Headers["Authorization"] != "Bearer "+builtinToken {
			t.Errorf("unexpected Authorization header for builtin: %v", builtinSrv.Headers)
		}
		if builtinSrv.Headers["X-Mcode-Workdir"] != "/workspace/test" {
			t.Errorf("expected X-Mcode-Workdir header %q, got %q", "/workspace/test", builtinSrv.Headers["X-Mcode-Workdir"])
		}
	})

	t.Run("仅内置server无审批", func(t *testing.T) {
		turn := harness.Turn{
			Mode:    harness.ModeAgent,
			WorkDir: "/workspace/test",
		}

		args := buildMCPArgs(turn, "", "", harness.BuiltinMCP{URL: builtinURL, Token: builtinToken}, nil)

		mcpConfigCount := 0
		var configStr string
		for i := 0; i < len(args); i++ {
			if args[i] == "--mcp-config" && i+1 < len(args) {
				mcpConfigCount++
				configStr = args[i+1]
			}
			if args[i] == "--permission-prompt-tool" {
				t.Errorf("did not expect --permission-prompt-tool without approval")
			}
		}

		if mcpConfigCount != 1 {
			t.Fatalf("expected exactly 1 --mcp-config, got %d", mcpConfigCount)
		}

		var cfg mcpConfig
		if err := json.Unmarshal([]byte(configStr), &cfg); err != nil {
			t.Fatalf("unmarshal mcpConfig failed: %v", err)
		}
		if _, ok := cfg.MCPServers["mcode_approval"]; ok {
			t.Errorf("expected no mcode_approval in config")
		}
		if _, ok := cfg.MCPServers["mcode"]; !ok {
			t.Errorf("expected mcode in config")
		}
		if cfg.MCPServers["mcode"].Headers["X-Mcode-Workdir"] != "/workspace/test" {
			t.Errorf("expected X-Mcode-Workdir header %q, got %q", "/workspace/test", cfg.MCPServers["mcode"].Headers["X-Mcode-Workdir"])
		}
	})

	t.Run("两者皆无时不产生--mcp-config", func(t *testing.T) {
		turn := harness.Turn{
			Mode: harness.ModeAgent,
		}
		args := buildMCPArgs(turn, "", "", harness.BuiltinMCP{}, nil)
		if len(args) != 0 {
			t.Errorf("expected empty args, got %v", args)
		}
	})

	t.Run("外部endpoint合并、跳过保留名、与内置mcode共存", func(t *testing.T) {
		turn := harness.Turn{
			Mode:    harness.ModeAgent,
			WorkDir: "/workspace/test",
		}
		external := []harness.MCPEndpoint{
			{Name: "custom_server", URL: "http://127.0.0.1:4000/mcp/custom_server", Token: "custom-token"},
			{Name: "mcode", URL: "http://attacker.com", Token: "bad-token"},
			{Name: "mcode_approval", URL: "http://attacker.com", Token: "bad-token"},
		}
		args := buildMCPArgs(turn, "", "", harness.BuiltinMCP{URL: builtinURL, Token: builtinToken}, external)
		if len(args) == 0 {
			t.Fatalf("expected args, got empty")
		}
		var cfg mcpConfig
		for i := 0; i < len(args); i++ {
			if args[i] == "--mcp-config" && i+1 < len(args) {
				if err := json.Unmarshal([]byte(args[i+1]), &cfg); err != nil {
					t.Fatalf("unmarshal failed: %v", err)
				}
			}
		}
		// custom_server 应该被合并
		custom, ok := cfg.MCPServers["custom_server"]
		if !ok {
			t.Fatalf("custom_server not found in servers")
		}
		if custom.Type != "http" || custom.URL != "http://127.0.0.1:4000/mcp/custom_server" {
			t.Errorf("unexpected custom server: %+v", custom)
		}
		if custom.Headers["Authorization"] != "Bearer custom-token" {
			t.Errorf("expected Bearer custom-token, got %v", custom.Headers["Authorization"])
		}
		// mcode 应该保留内置配置，跳过外部同名项
		mcodeSrv, ok := cfg.MCPServers["mcode"]
		if !ok {
			t.Fatalf("mcode not found in servers")
		}
		if mcodeSrv.URL != builtinURL {
			t.Errorf("mcode was overwritten by external: %s", mcodeSrv.URL)
		}
		// mcode_approval 未启用审批时不应存在
		if _, ok := cfg.MCPServers["mcode_approval"]; ok {
			t.Errorf("mcode_approval should be skipped")
		}
	})

	t.Run("有线程ID时使用按会话路由地址且不带workdir头", func(t *testing.T) {
		threadID := "0f8fad5b-d9cb-469f-a165-70867728950e"
		turn := harness.Turn{
			Mode:     harness.ModeAgent,
			WorkDir:  "/workspace/test",
			ThreadID: threadID,
		}
		builtin := harness.BuiltinMCP{
			ThreadBase:  "http://127.0.0.1:23456/mcp/mcode/",
			ThreadToken: "token-thread",
			URL:         builtinURL,
			Token:       builtinToken,
		}
		args := buildMCPArgs(turn, "", "", builtin, nil)

		var cfg mcpConfig
		found := false
		for i := 0; i < len(args); i++ {
			if args[i] == "--mcp-config" && i+1 < len(args) {
				found = true
				if err := json.Unmarshal([]byte(args[i+1]), &cfg); err != nil {
					t.Fatalf("unmarshal failed: %v", err)
				}
			}
		}
		if !found {
			t.Fatalf("expected --mcp-config in args: %v", args)
		}
		mcodeSrv, ok := cfg.MCPServers["mcode"]
		if !ok {
			t.Fatalf("mcode not found in servers: %+v", cfg.MCPServers)
		}
		if want := "http://127.0.0.1:23456/mcp/mcode/" + threadID; mcodeSrv.URL != want {
			t.Errorf("expected thread url %q, got %q", want, mcodeSrv.URL)
		}
		if mcodeSrv.Headers["Authorization"] != "Bearer token-thread" {
			t.Errorf("expected thread token, got %q", mcodeSrv.Headers["Authorization"])
		}
		if _, ok := mcodeSrv.Headers["X-Mcode-Workdir"]; ok {
			t.Errorf("thread endpoint must not carry X-Mcode-Workdir: %v", mcodeSrv.Headers)
		}
	})
}
