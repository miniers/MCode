package agy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/providers/loginenv"
	"mcode/internal/testenv"
)

func TestResolveBinaryLocalFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("此测试使用 Unix 可执行权限和 HOME")
	}
	const bin = "mcode-test-agy-local-fallback"
	_, _ = loginenv.LookPath(bin)
	home := t.TempDir()
	testenv.SetHome(t, home)
	candidate := filepath.Join(home, ".local", "bin", bin)
	if err := os.MkdirAll(filepath.Dir(candidate), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveBinary(bin); got != candidate {
		t.Fatalf("resolveBinary(%q) = %q，期望 %q", bin, got, candidate)
	}
}

const fakeAgyEnv = "MCODE_TEST_FAKE_AGY"
const fakeAgyStatusEnv = "MCODE_TEST_FAKE_AGY_STATUS"

type fakeInvocationRecord struct {
	Args  []string `json:"args"`
	Stdin string   `json:"stdin"`
}

func recordFakeInvocation(recordFile string, args []string, stdin string) int {
	if recordFile == "" {
		return 1
	}
	var records []fakeInvocationRecord
	if data, err := os.ReadFile(recordFile); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, &records)
	}
	records = append(records, fakeInvocationRecord{
		Args:  args,
		Stdin: stdin,
	})
	if data, err := json.Marshal(records); err == nil {
		_ = os.WriteFile(recordFile, data, 0o644)
	}
	return len(records)
}

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeAgyEnv); mode != "" {
		runFakeAgy(mode)
		return
	}
	os.Exit(m.Run())
}

func runFakeAgy(mode string) {
	writeMsg := func(v any) {
		data, _ := json.Marshal(v)
		data = append(data, '\n')
		_, _ = os.Stdout.Write(data)
	}

	reader := bufio.NewReader(os.Stdin)
	// 读取可能的 user prompt
	firstLine, _ := reader.ReadBytes('\n')
	_ = firstLine

	switch mode {
	case "normal":
		writeMsg(map[string]any{
			"event":           "init",
			"conversation_id": "test-conv-001",
		})
		writeMsg(map[string]any{
			"event": "step_update",
			"step_update": map[string]any{
				"step_index": 0,
				"step_type":  "agent_response",
				"text_delta": "Hello from mock agy!",
			},
		})
		writeMsg(map[string]any{
			"event": "result",
			"result": map[string]any{
				"status":          "SUCCESS",
				"conversation_id": "test-conv-001",
			},
		})
		os.Exit(0)

	case "usage":
		writeMsg(map[string]any{
			"event":           "init",
			"conversation_id": "usage-conv-001",
		})
		// 两个 agent_response 步骤，各带本次模型请求的 usage（单次请求口径）。
		for i, u := range []map[string]int{
			{"input_tokens": 1200, "output_tokens": 100, "cache_read_tokens": 800},
			{"input_tokens": 1300, "output_tokens": 120, "cache_read_tokens": 900},
		} {
			writeMsg(map[string]any{
				"event": "step_update",
				"step_update": map[string]any{
					"step_index": i,
					"step_type":  "agent_response",
					"text_delta": fmt.Sprintf("step %d", i),
					"usage":      u,
				},
			})
		}
		// result 的 usage 是整轮多步累计的计费用量，不是最后一次请求实际带入的上下文。
		writeMsg(map[string]any{
			"event": "result",
			"result": map[string]any{
				"status":          "SUCCESS",
				"conversation_id": "usage-conv-001",
				"usage": map[string]int{
					"input_tokens":      2500,
					"output_tokens":     220,
					"cache_read_tokens": 1700,
				},
			},
		})
		os.Exit(0)

	case "request_speed":
		writeMsg(map[string]any{
			"event":           "init",
			"conversation_id": "speed-conv-001",
		})
		// 两次带正文的请求：正文帧开始计时，usage 帧结算。
		for i, u := range []map[string]int{
			{"input_tokens": 100, "output_tokens": 20},
			{"input_tokens": 200, "output_tokens": 30},
		} {
			writeMsg(map[string]any{
				"event": "step_update",
				"step_update": map[string]any{
					"step_index": i,
					"step_type":  "agent_response",
					"text_delta": fmt.Sprintf("step %d", i),
				},
			})
			writeMsg(map[string]any{
				"event": "step_update",
				"step_update": map[string]any{
					"step_index": i,
					"step_type":  "agent_response",
					"usage":      u,
				},
			})
		}
		// 第三次请求没有正文帧：只有 usage，不产生速度样本。
		writeMsg(map[string]any{
			"event": "step_update",
			"step_update": map[string]any{
				"step_index": 2,
				"step_type":  "agent_response",
				"usage":      map[string]int{"input_tokens": 300, "output_tokens": 40},
			},
		})
		writeMsg(map[string]any{
			"event": "result",
			"result": map[string]any{
				"status":          "SUCCESS",
				"conversation_id": "speed-conv-001",
				"usage":           map[string]int{"input_tokens": 600, "output_tokens": 90},
			},
		})
		os.Exit(0)

	case "check_env":
		home := os.Getenv("HOME")
		forceFile := os.Getenv("GEMINI_FORCE_FILE_STORAGE")
		autoUpdate := os.Getenv("AGY_CLI_DISABLE_AUTO_UPDATE")

		hasGeminiDir := false
		for _, arg := range os.Args {
			if strings.HasPrefix(arg, "--gemini_dir=/mock/isolated/home/.gemini") {
				hasGeminiDir = true
				break
			}
		}

		if home != "/mock/isolated/home" || forceFile != "true" || autoUpdate != "true" || !hasGeminiDir {
			_, _ = fmt.Fprintf(os.Stderr, "check_env failed: HOME=%s, FORCE_FILE=%s, AUTO_UPDATE=%s, hasGeminiDir=%v\n",
				home, forceFile, autoUpdate, hasGeminiDir)
			os.Exit(2)
		}

		writeMsg(map[string]any{
			"event":           "init",
			"conversation_id": "env-conv-123",
		})
		writeMsg(map[string]any{
			"event": "step_update",
			"step_update": map[string]any{
				"step_index": 0,
				"step_type":  "agent_response",
				"text_delta": "Env and args check passed",
			},
		})
		writeMsg(map[string]any{
			"event": "result",
			"result": map[string]any{
				"status":          "SUCCESS",
				"conversation_id": "env-conv-123",
			},
		})
		os.Exit(0)

	case "failover_rate_limit":
		hasConv := false
		for _, arg := range os.Args {
			if arg == "--conversation" {
				hasConv = true
				break
			}
		}

		if hasConv {
			// 第二次启动带了 --conversation，成功响应
			writeMsg(map[string]any{
				"event": "step_update",
				"step_update": map[string]any{
					"step_index": 0,
					"step_type":  "agent_response",
					"text_delta": "Success on account 2",
				},
			})
			writeMsg(map[string]any{
				"event": "result",
				"result": map[string]any{
					"status":          "SUCCESS",
					"conversation_id": "conv-retry-999",
				},
			})
			os.Exit(0)
		} else {
			// 第一次启动没有 --conversation，输出 init 帧后抛出限流错误
			writeMsg(map[string]any{
				"event":           "init",
				"conversation_id": "conv-retry-999",
			})
			_, _ = os.Stderr.WriteString("RESOURCE_EXHAUSTED: quota exceeded 429\n")
			writeMsg(map[string]any{
				"event": "result",
				"result": map[string]any{
					"status": "ERROR",
					"error":  "RESOURCE_EXHAUSTED: quota exceeded",
				},
			})
			os.Exit(1)
		}

	case "strict_rate_limit":
		_, _ = os.Stderr.WriteString("RESOURCE_EXHAUSTED: quota exceeded 429\n")
		writeMsg(map[string]any{
			"event": "result",
			"result": map[string]any{
				"status": "ERROR",
				"error":  "RESOURCE_EXHAUSTED: quota exceeded",
			},
		})
		os.Exit(1)

	case "success_nonzero_exit":
		status := os.Getenv(fakeAgyStatusEnv)
		if status == "" {
			status = "SUCCESS"
		}
		writeMsg(map[string]any{
			"event":           "init",
			"conversation_id": "success-nonzero-conv",
		})
		writeMsg(map[string]any{
			"event": "result",
			"result": map[string]any{
				"status":          status,
				"conversation_id": "success-nonzero-conv",
				"response":        "666",
			},
		})
		os.Exit(1)

	case "failure_nonzero_exit":
		writeMsg(map[string]any{
			"event":           "init",
			"conversation_id": "failure-nonzero-conv",
		})
		writeMsg(map[string]any{
			"event": "result",
			"result": map[string]any{
				"status":          "ERROR",
				"conversation_id": "failure-nonzero-conv",
				"error":           "mock failure",
			},
		})
		os.Exit(1)

	case "body_nonzero_exit":
		writeMsg(map[string]any{
			"event":           "init",
			"conversation_id": "body-nonzero-conv",
		})
		writeMsg(map[string]any{
			"event": "step_update",
			"step_update": map[string]any{
				"step_index": 0,
				"step_type":  "agent_response",
				"text_delta": "body without terminal result",
			},
		})
		os.Exit(1)

	case "lingering":
		// 模拟真实 agy（1.4.0 实测）：stream-json 模式下输出 result 后不会自行退出，
		// 而是继续等待 stdin 的下一轮输入，直到 stdin 关闭（EOF）才退出。
		writeMsg(map[string]any{
			"event":           "init",
			"conversation_id": "lingering-conv-777",
		})
		writeMsg(map[string]any{
			"event": "step_update",
			"step_update": map[string]any{
				"step_index": 0,
				"step_type":  "agent_response",
				"text_delta": "Lingering reply",
			},
		})
		writeMsg(map[string]any{
			"event": "result",
			"result": map[string]any{
				"status":          "SUCCESS",
				"conversation_id": "lingering-conv-777",
			},
		})
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)

	case "denied_action":
		writeMsg(map[string]any{
			"event":           "init",
			"conversation_id": "denied-conv-001",
		})
		writeMsg(map[string]any{
			"event": "result",
			"result": map[string]any{
				"status":          "SUCCESS",
				"conversation_id": "denied-conv-001",
				"response":        "",
				"denied_actions": []map[string]any{
					{"action": "command", "display_name": "RunCommand"},
				},
			},
		})
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)

	case "denied_action_nonzero_exit":
		writeMsg(map[string]any{
			"event":           "init",
			"conversation_id": "denied-nonzero-conv",
		})
		writeMsg(map[string]any{
			"event": "result",
			"result": map[string]any{
				"status":          "SUCCESS",
				"conversation_id": "denied-nonzero-conv",
				"response":        "",
				"denied_actions": []map[string]any{
					{"action": "command", "display_name": "RunCommand"},
				},
			},
		})
		os.Exit(1)

	case "duplicate_result":
		// 正文既流式给过，result.response 又原样给一遍：不应重复发送。
		writeMsg(map[string]any{
			"event":           "init",
			"conversation_id": "dup-conv",
		})
		writeMsg(map[string]any{
			"event": "step_update",
			"step_update": map[string]any{
				"step_index": 0,
				"step_type":  "agent_response",
				"text_delta": "Hello from Gemini",
			},
		})
		writeMsg(map[string]any{
			"event": "result",
			"result": map[string]any{
				"status":          "SUCCESS",
				"conversation_id": "dup-conv",
				"response":        "Hello from Gemini",
			},
		})
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)

	case "result_last_segment":
		// result.response 只是最后一段（不以前文为前缀）：应追加而不是丢掉。
		writeMsg(map[string]any{
			"event":           "init",
			"conversation_id": "last-seg-conv",
		})
		writeMsg(map[string]any{
			"event": "step_update",
			"step_update": map[string]any{
				"step_index": 0,
				"step_type":  "agent_response",
				"text_delta": "先检查目录。",
			},
		})
		writeMsg(map[string]any{
			"event": "step_update",
			"step_update": map[string]any{
				"step_index": 1,
				"step_type":  "tool",
				"tool_name":  "run_command",
				"tool_id":    "tool-1",
				"state":      "DONE",
				"tool_info": map[string]any{
					"parameters": map[string]any{"command": "ls"},
					"output":     "a.txt",
				},
			},
		})
		writeMsg(map[string]any{
			"event": "result",
			"result": map[string]any{
				"status":          "SUCCESS",
				"conversation_id": "last-seg-conv",
				"response":        "最终结论：一切正常。",
			},
		})
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)

	case "text_field_only":
		// 正文不在 text_delta 里，而在 text 字段里：也要能展示，且不与 result 重复。
		writeMsg(map[string]any{
			"event":           "init",
			"conversation_id": "text-field-conv",
		})
		writeMsg(map[string]any{
			"event": "step_update",
			"step_update": map[string]any{
				"step_index": 0,
				"step_type":  "agent_response",
				"text":       "来自 text 字段的正文。",
			},
		})
		writeMsg(map[string]any{
			"event": "result",
			"result": map[string]any{
				"status":          "SUCCESS",
				"conversation_id": "text-field-conv",
				"response":        "来自 text 字段的正文。",
			},
		})
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)

	case "text_only_in_result":
		// 模拟真实 agy 的收尾路径：没有任何 agent_response 增量，正文只在 result 里给。
		writeMsg(map[string]any{
			"event":           "init",
			"conversation_id": "result-only-conv",
		})
		writeMsg(map[string]any{
			"event": "result",
			"result": map[string]any{
				"status":          "SUCCESS",
				"conversation_id": "result-only-conv",
				"response":        "只有 result 携带的收尾正文。",
			},
		})
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)

	case "truncated_stream":
		// 模拟流式增量被截断（超时/收尾异常）：result.response 比已流式内容更完整。
		writeMsg(map[string]any{
			"event":           "init",
			"conversation_id": "truncated-conv",
		})
		writeMsg(map[string]any{
			"event": "step_update",
			"step_update": map[string]any{
				"step_index": 0,
				"step_type":  "agent_response",
				"state":      "ACTIVE",
				"text_delta": "前半句。",
			},
		})
		writeMsg(map[string]any{
			"event": "result",
			"result": map[string]any{
				"status":          "SUCCESS",
				"conversation_id": "truncated-conv",
				"response":        "前半句。后半句被截断过。",
			},
		})
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)

	case "check_visible_guard":
		var msg struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(firstLine, &msg); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "check_visible_guard: bad prompt line: %v\n", err)
			os.Exit(2)
		}
		if !strings.Contains(msg.Message.Content, "Keep your work visible in the transcript") {
			_, _ = fmt.Fprintf(os.Stderr, "check_visible_guard: guard missing in %q\n", msg.Message.Content)
			os.Exit(2)
		}
		writeMsg(map[string]any{
			"event":           "init",
			"conversation_id": "guard-conv",
		})
		writeMsg(map[string]any{
			"event": "result",
			"result": map[string]any{
				"status":          "SUCCESS",
				"conversation_id": "guard-conv",
				"response":        "guard ok",
			},
		})
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)

	case "transient_400_retry":
		callIndex := recordFakeInvocation(os.Getenv("MCODE_TEST_AGY_RECORD_FILE"), os.Args, string(firstLine))
		if callIndex == 1 {
			// 第一次启动：输出带 conversation_id 的 init 帧，然后输出 400 错误
			writeMsg(map[string]any{
				"event":           "init",
				"conversation_id": "conv-transient-400",
			})
			writeMsg(map[string]any{
				"event": "result",
				"result": map[string]any{
					"status":          "ERROR",
					"conversation_id": "conv-transient-400",
					"error":           "INVALID_ARGUMENT (code 400): Request contains an invalid argument.",
				},
			})
			_, _ = io.Copy(io.Discard, os.Stdin)
			os.Exit(0)
		}

		// 第二次启动（自动续跑）：正常完成
		writeMsg(map[string]any{
			"event":           "init",
			"conversation_id": "conv-transient-400",
		})
		writeMsg(map[string]any{
			"event": "step_update",
			"step_update": map[string]any{
				"step_index": 0,
				"step_type":  "agent_response",
				"text_delta": "Second turn completed successfully",
			},
		})
		writeMsg(map[string]any{
			"event": "result",
			"result": map[string]any{
				"status":          "SUCCESS",
				"conversation_id": "conv-transient-400",
				"response":        "Second turn completed successfully",
			},
		})
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)

	case "transient_400_always":
		_ = recordFakeInvocation(os.Getenv("MCODE_TEST_AGY_RECORD_FILE"), os.Args, string(firstLine))
		// 每次启动都返回 400 错误
		writeMsg(map[string]any{
			"event":           "init",
			"conversation_id": "conv-transient-always",
		})
		writeMsg(map[string]any{
			"event": "result",
			"result": map[string]any{
				"status":          "ERROR",
				"conversation_id": "conv-transient-always",
				"error":           "INVALID_ARGUMENT (code 400): Request contains an invalid argument.",
			},
		})
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)

	case "network_eof_after_text":
		// 正文已完整流式输出后，result 才报网络 EOF：任务实际已完成，不应判失败。
		_ = recordFakeInvocation(os.Getenv("MCODE_TEST_AGY_RECORD_FILE"), os.Args, string(firstLine))
		writeMsg(map[string]any{
			"event":           "init",
			"conversation_id": "conv-net-eof-done",
		})
		writeMsg(map[string]any{
			"event": "step_update",
			"step_update": map[string]any{
				"step_index": 0,
				"step_type":  "agent_response",
				"text_delta": "All checks passed. Task complete.",
			},
		})
		writeMsg(map[string]any{
			"event": "result",
			"result": map[string]any{
				"status":          "ERROR",
				"conversation_id": "conv-net-eof-done",
				"error":           "agy error: API error (attempt 1): request failed: Post \"https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse\": EOF",
			},
		})
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)

	case "network_eof_during_tool":
		callIndex := recordFakeInvocation(os.Getenv("MCODE_TEST_AGY_RECORD_FILE"), os.Args, string(firstLine))
		if callIndex == 1 {
			// 工具尚在进行中时网络中断：正文没走完，应在同一会话内自动续跑。
			writeMsg(map[string]any{
				"event":           "init",
				"conversation_id": "conv-net-eof-tool",
			})
			writeMsg(map[string]any{
				"event": "step_update",
				"step_update": map[string]any{
					"step_index": 0,
					"step_type":  "agent_response",
					"text_delta": "Let me check the repo first.",
				},
			})
			writeMsg(map[string]any{
				"event": "step_update",
				"step_update": map[string]any{
					"step_index": 1,
					"step_type":  "tool",
					"tool_name":  "RunCommand",
					"tool_id":    "tool-1",
					"state":      "ACTIVE",
				},
			})
			writeMsg(map[string]any{
				"event": "result",
				"result": map[string]any{
					"status":          "ERROR",
					"conversation_id": "conv-net-eof-tool",
					"error":           "read tcp 10.0.0.1:443: stream error: connection reset by peer",
				},
			})
			_, _ = io.Copy(io.Discard, os.Stdin)
			os.Exit(0)
		}

		// 第二次启动（自动续跑）：正常完成。
		writeMsg(map[string]any{
			"event":           "init",
			"conversation_id": "conv-net-eof-tool",
		})
		writeMsg(map[string]any{
			"event": "step_update",
			"step_update": map[string]any{
				"step_index": 0,
				"step_type":  "agent_response",
				"text_delta": "Resumed and finished.",
			},
		})
		writeMsg(map[string]any{
			"event": "result",
			"result": map[string]any{
				"status":          "SUCCESS",
				"conversation_id": "conv-net-eof-tool",
				"response":        "Resumed and finished.",
			},
		})
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)

	default:
		_, _ = fmt.Fprintf(os.Stderr, "unknown mode %s\n", mode)
		os.Exit(1)
	}
}

// (a) 一轮正常会话产生文本和完成事件，且拿到会话 id
func TestAgy_NormalSession(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	h := New(Options{
		Binary: exe,
	})

	var mu sync.Mutex
	var events []harness.Event

	turn := harness.Turn{
		Prompt: "Hello agy",
		Mode:   harness.ModeSupervised,
		Env:    []string{fakeAgyEnv + "=normal"},
	}

	err = h.Run(context.Background(), turn, func(e harness.Event) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	var hasSession, hasText, hasDone bool
	for _, e := range events {
		switch e.Type {
		case harness.EventSession:
			if e.SessionID == "test-conv-001" {
				hasSession = true
			}
		case harness.EventText:
			if strings.Contains(e.Text, "Hello from mock agy!") {
				hasText = true
			}
		case harness.EventDone:
			if e.StopReason == "SUCCESS" {
				hasDone = true
			}
		}
	}

	if !hasSession {
		t.Errorf("missing EventSession with test-conv-001, got events: %+v", events)
	}
	if !hasText {
		t.Errorf("missing EventText, got events: %+v", events)
	}
	if !hasDone {
		t.Errorf("missing EventDone, got events: %+v", events)
	}
}

// 用量事件要区分两个口径：计费仍按 result 的整轮累计用量，上下文占用只能取最后一次单步 usage。
func TestAgy_UsageSplitsBillingAndContext(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	h := New(Options{Binary: exe})

	var mu sync.Mutex
	var events []harness.Event

	turn := harness.Turn{
		Prompt: "usage test",
		Mode:   harness.ModeSupervised,
		Env:    []string{fakeAgyEnv + "=usage"},
	}
	if err := h.Run(context.Background(), turn, func(e harness.Event) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	var usages []harness.Usage
	for _, e := range events {
		if e.Type == harness.EventUsage && e.Usage != nil {
			usages = append(usages, *e.Usage)
		}
	}
	if len(usages) != 3 {
		t.Fatalf("want 3 usage events (2 steps + result), got %d: %+v", len(usages), usages)
	}

	// 单步事件本身就带上下文占用，最后一步的两项之和即 1300 + 120。
	if got := usages[1].ContextTokens; got != 1420 {
		t.Fatalf("step usage context tokens = %d, want 1420", got)
	}

	last := usages[len(usages)-1]
	// 计费口径：后到的 result 覆盖整轮累计用量，不能被上下文口径破坏。
	if last.InputTokens != 2500 || last.OutputTokens != 220 || last.CachedTokens != 1700 {
		t.Fatalf("result usage must keep cumulative billing, got %+v", last)
	}
	// 上下文口径：仍是最后一次单步 usage，而不是整轮累计的 2720。
	if last.ContextTokens != 1420 {
		t.Fatalf("context tokens = %d, want 1420 (last step input+output)", last.ContextTokens)
	}
}

// 请求速度样本：首个内容帧到本次 usage 帧计时，没有内容帧的请求不产生样本；
// 单步与 result 两条 usage 都带本轮至今的完整样本列表。
func TestAgy_RequestSpeedSamples(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	h := New(Options{Binary: exe})
	// 假时钟每次调用前进 200 毫秒：不必真的等待，只要 Begin 与 Finish 之间取到足够时长。
	var ticks int
	h.now = func() time.Time {
		ticks++
		return time.Unix(0, 0).Add(time.Duration(ticks) * 200 * time.Millisecond)
	}

	var events []harness.Event
	turn := harness.Turn{
		Prompt: "request speed",
		Mode:   harness.ModeSupervised,
		Env:    []string{fakeAgyEnv + "=request_speed"},
	}
	if err := h.Run(context.Background(), turn, func(e harness.Event) {
		events = append(events, e)
	}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	var usages []harness.Usage
	for _, e := range events {
		if e.Type == harness.EventUsage && e.Usage != nil {
			usages = append(usages, *e.Usage)
		}
	}
	if len(usages) != 4 {
		t.Fatalf("want 4 usage events (3 steps + result), got %d: %+v", len(usages), usages)
	}

	// 第一次请求：一个样本，输出 20 tokens，时长不小于 100 毫秒。
	first := usages[0].Requests
	if len(first) != 1 || first[0].OutputTokens != 20 || first[0].DurationMs < 100 {
		t.Fatalf("第一次请求的速度样本 = %+v, want 1 条 20 tokens 且时长 >= 100ms", first)
	}
	// 第二次请求：样本累积为 2 条，按完成顺序排列。
	second := usages[1].Requests
	if len(second) != 2 || second[0].OutputTokens != 20 || second[1].OutputTokens != 30 || second[1].DurationMs < 100 {
		t.Fatalf("第二次请求的速度样本 = %+v, want [20 30]", second)
	}
	// 第三次请求没有内容帧：不产生样本，列表仍是 2 条。
	if len(usages[2].Requests) != 2 {
		t.Fatalf("无内容帧的请求不应产生样本，得到 %+v", usages[2].Requests)
	}
	// result 的整轮 usage 也带完整样本列表。
	if len(usages[3].Requests) != 2 || usages[3].Requests[1].OutputTokens != 30 {
		t.Fatalf("result usage 的速度样本 = %+v, want 完整 2 条", usages[3].Requests)
	}
}

type fakeEnvRouter struct {
	boundConvID    string
	boundAccountID string
}

func (r *fakeEnvRouter) Route(conversationID, model string, exclude []string) (string, []string, []string, error) {
	return "acc-001",
		[]string{"HOME=/mock/isolated/home", "GEMINI_FORCE_FILE_STORAGE=true"},
		[]string{"--gemini_dir=/mock/isolated/home/.gemini"},
		nil
}

func (r *fakeEnvRouter) Bind(conversationID, accountID string) {
	r.boundConvID = conversationID
	r.boundAccountID = accountID
}

func (r *fakeEnvRouter) MarkLimited(conversationID, accountID string, resetAt time.Time) bool {
	return false
}

// (b) 用 fake Router 断言子进程实际收到 Router 给的 HOME、GEMINI_FORCE_FILE_STORAGE、--gemini_dir，以及 AGY_CLI_DISABLE_AUTO_UPDATE=true，并且 Bind 被调用
func TestAgy_RouterEnvAndArgs(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	router := &fakeEnvRouter{}
	h := New(Options{
		Binary: exe,
		Router: router,
	})

	turn := harness.Turn{
		Prompt: "Check env",
		Mode:   harness.ModeAutoAcceptEdits,
		Env:    []string{fakeAgyEnv + "=check_env"},
	}

	err = h.Run(context.Background(), turn, nil)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if router.boundConvID != "env-conv-123" {
		t.Errorf("want boundConvID env-conv-123, got %q", router.boundConvID)
	}
	if router.boundAccountID != "acc-001" {
		t.Errorf("want boundAccountID acc-001, got %q", router.boundAccountID)
	}
}

type failoverRouter struct {
	mu             sync.Mutex
	routeCalls     int
	markLimited    bool
	limitedAccID   string
	limitedConvID  string
	excludePassed  []string
	boundConvID    string
	boundAccountID string
}

func (r *failoverRouter) Route(conversationID, model string, exclude []string) (string, []string, []string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routeCalls++
	r.excludePassed = append([]string{}, exclude...)
	if len(exclude) == 0 {
		return "acc-first", nil, nil, nil
	}
	return "acc-second", nil, nil, nil
}

func (r *failoverRouter) Bind(conversationID, accountID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.boundConvID = conversationID
	r.boundAccountID = accountID
}

func (r *failoverRouter) MarkLimited(conversationID, accountID string, resetAt time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.markLimited = true
	r.limitedConvID = conversationID
	r.limitedAccID = accountID
	return true
}

// (c) 假二进制第一次输出 RESOURCE_EXHAUSTED 后退出码为 1，第二个账号成功：断言 MarkLimited 被调用、exclude 中含第一个账号、第二次启动带了 --conversation
func TestAgy_FailoverRateLimited(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	router := &failoverRouter{}
	h := New(Options{
		Binary: exe,
		Router: router,
	})

	var mu sync.Mutex
	var events []harness.Event

	turn := harness.Turn{
		Prompt: "Test failover",
		Mode:   harness.ModeSupervised,
		Env:    []string{fakeAgyEnv + "=failover_rate_limit"},
	}

	err = h.Run(context.Background(), turn, func(e harness.Event) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	router.mu.Lock()
	defer router.mu.Unlock()

	if !router.markLimited {
		t.Errorf("expected MarkLimited to be called")
	}
	if router.limitedAccID != "acc-first" {
		t.Errorf("expected MarkLimited on acc-first, got %s", router.limitedAccID)
	}
	if !slices.Contains(router.excludePassed, "acc-first") {
		t.Errorf("expected exclude to contain acc-first, got: %v", router.excludePassed)
	}

	mu.Lock()
	defer mu.Unlock()
	var hasSecondText bool
	for _, e := range events {
		if e.Type == harness.EventText && strings.Contains(e.Text, "Success on account 2") {
			hasSecondText = true
		}
	}
	if !hasSecondText {
		t.Errorf("expected text from second account, got: %+v", events)
	}
}

type strictRouter struct {
	mu                sync.Mutex
	routeCalls        int
	markLimitedCalled bool
}

func (r *strictRouter) Route(conversationID, model string, exclude []string) (string, []string, []string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routeCalls++
	return "acc-strict", nil, nil, nil
}

func (r *strictRouter) Bind(conversationID, accountID string) {}

func (r *strictRouter) MarkLimited(conversationID, accountID string, resetAt time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.markLimitedCalled = true
	return false // 严格模式不允许换号
}

// (d) MarkLimited 返回 false 时直接报错，不重试
func TestAgy_StrictRateLimited(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	router := &strictRouter{}
	h := New(Options{
		Binary: exe,
		Router: router,
	})

	turn := harness.Turn{
		Prompt: "Test strict",
		Mode:   harness.ModeSupervised,
		Env:    []string{fakeAgyEnv + "=strict_rate_limit"},
	}

	err = h.Run(context.Background(), turn, nil)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}

	router.mu.Lock()
	defer router.mu.Unlock()

	if !router.markLimitedCalled {
		t.Errorf("expected MarkLimited to be called")
	}
	if router.routeCalls != 1 {
		t.Errorf("expected exactly 1 Route call without retry, got %d", router.routeCalls)
	}
}

// (e) 真实 agy 输出 result 后仍在等待 stdin 输入：Run 必须在收到 result 时关闭 stdin，
// 让子进程退出；否则整轮会话会挂死到 print-timeout 或 context 超时。
func TestAgy_ClosesStdinAfterResult(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	h := New(Options{Binary: exe})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var mu sync.Mutex
	var events []harness.Event

	turn := harness.Turn{
		Prompt: "Ping",
		Mode:   harness.ModeSupervised,
		Env:    []string{fakeAgyEnv + "=lingering"},
	}

	err = h.Run(ctx, turn, func(e harness.Event) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	})
	if err != nil {
		t.Fatalf("Run failed (result 之后子进程未随 stdin 关闭而退出？): %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	var hasText, hasDone bool
	for _, e := range events {
		if e.Type == harness.EventText && strings.Contains(e.Text, "Lingering reply") {
			hasText = true
		}
		if e.Type == harness.EventDone {
			hasDone = true
		}
	}
	if !hasText || !hasDone {
		t.Errorf("expected text and done events, got: %+v", events)
	}
}

// (f) 权限模式到 agy 命令行参数的映射：supervised 不加参数（保持默认审查并软拒绝未批准动作）；
// readonly 映射为 plan 并附加 --dangerously-skip-permissions；其余模式统一 accept-edits + 跳过权限。
func TestModeArguments(t *testing.T) {
	skip := []string{"--mode", "accept-edits", "--dangerously-skip-permissions"}
	planSkip := []string{"--mode", "plan", "--dangerously-skip-permissions"}

	tests := []struct {
		name string
		mode harness.PermissionMode
		want []string
	}{
		{"supervised", harness.ModeSupervised, nil},
		{"readonly 映射为 plan 并跳过权限", harness.ModeReadOnly, planSkip},
		{"autoAcceptEdits", harness.ModeAutoAcceptEdits, skip},
		{"auto", harness.ModeAuto, skip},
		{"fullAccess", harness.ModeFullAccess, skip},
		{"空模式", "", skip},
		{"未知模式按 autoAcceptEdits 处理", "unknown-mode", skip},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := modeArguments(tc.mode)
			if !slices.Equal(got, tc.want) {
				t.Errorf("modeArguments(%q) = %v, want %v", tc.mode, got, tc.want)
			}
			if tc.mode == harness.ModeReadOnly {
				if !slices.Contains(got, "--mode") || !slices.Contains(got, "plan") || !slices.Contains(got, "--dangerously-skip-permissions") {
					t.Errorf("readonly 模式期望包含 --mode plan 与 --dangerously-skip-permissions: %v", got)
				}
			}
		})
	}
}

// (f-2) 当 CLI 输出 result 且含有 denied_actions 且没有任何正文时，应上报错误而不是静默成功
func TestAgy_DeniedActionWithoutOutputProducesError(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	h := New(Options{Binary: exe})
	for _, mode := range []string{"denied_action", "denied_action_nonzero_exit"} {
		t.Run(mode, func(t *testing.T) {
			turn := harness.Turn{
				Prompt: "Test denied",
				Mode:   harness.ModeReadOnly,
				Env:    []string{fakeAgyEnv + "=" + mode},
			}

			err = h.Run(context.Background(), turn, nil)
			if err == nil {
				t.Fatalf("expected error when action is denied without output, got nil")
			}
			if mode == "denied_action_nonzero_exit" {
				// 非零退出时错误由 waitErr 主导，原文不带 resultErr。
				if !strings.Contains(err.Error(), "process exited") {
					t.Errorf("expected error to mention 'process exited', got %v", err)
				}
				return
			}
			if !strings.Contains(err.Error(), "action denied") {
				t.Errorf("expected error to mention 'action denied', got %v", err)
			}
		})
	}
}

// 成功终态已经发出后，agy 收尾阶段的非零退出不应覆盖正文和完成事件。
func TestAgy_SuccessResultSurvivesNonzeroExit(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	tests := []struct {
		name   string
		status string
	}{
		{name: "SUCCESS", status: "SUCCESS"},
		{name: "WAITING", status: "WAITING"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := New(Options{Binary: exe})
			turn := harness.Turn{
				Prompt: "success result with nonzero exit",
				Mode:   harness.ModeSupervised,
				Env: []string{
					fakeAgyEnv + "=success_nonzero_exit",
					fakeAgyStatusEnv + "=" + tc.status,
				},
			}
			var events []harness.Event
			err := h.Run(context.Background(), turn, func(e harness.Event) {
				events = append(events, e)
			})
			if err != nil {
				t.Fatalf("Run failed: %v", err)
			}

			var body strings.Builder
			var doneCount, errorCount int
			for _, e := range events {
				switch e.Type {
				case harness.EventText:
					body.WriteString(e.Text)
				case harness.EventDone:
					doneCount++
				case harness.EventError:
					errorCount++
				}
			}
			if got := body.String(); got != "666" {
				t.Errorf("body = %q, want %q", got, "666")
			}
			if doneCount != 1 {
				t.Errorf("EventDone count = %d, want 1; events: %+v", doneCount, events)
			}
			if errorCount != 0 {
				t.Errorf("EventError count = %d, want 0; events: %+v", errorCount, events)
			}
		})
	}
}

// ERROR 终态和没有成功终态的正文都不能掩盖非零退出。
func TestAgy_FailedResultStillReturnsExitError(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	tests := []struct {
		name string
		mode string
	}{
		{name: "ERROR result", mode: "failure_nonzero_exit"},
		{name: "body without terminal result", mode: "body_nonzero_exit"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := New(Options{Binary: exe})
			turn := harness.Turn{
				Prompt: "failed result with nonzero exit",
				Mode:   harness.ModeSupervised,
				Env:    []string{fakeAgyEnv + "=" + tc.mode},
			}
			err := h.Run(context.Background(), turn, nil)
			if err == nil {
				t.Fatal("expected nonzero exit error, got nil")
			}
			if !strings.Contains(err.Error(), "process exited") {
				t.Errorf("error = %v, want process exited", err)
			}
		})
	}
}

// (h) result.response 是整轮正文：没有流式增量时也要补发，避免答复整体丢失。
func TestAgy_TextFallsBackToResultResponse(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	h := New(Options{Binary: exe})
	var mu sync.Mutex
	var texts []string

	turn := harness.Turn{
		Prompt: "only result text",
		Mode:   harness.ModeSupervised,
		Env:    []string{fakeAgyEnv + "=text_only_in_result"},
	}
	if err := h.Run(context.Background(), turn, func(e harness.Event) {
		if e.Type != harness.EventText {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		texts = append(texts, e.Text)
	}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(texts, ""); !strings.Contains(got, "只有 result 携带的收尾正文") {
		t.Fatalf("expected result.response to be emitted, got %q", got)
	}
}

// (i) 流式增量被截断时，result.response 的剩余部分要补发，且不与已流式内容重复。
func TestAgy_ResultResponseCompletesTruncatedStream(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	h := New(Options{Binary: exe})
	var mu sync.Mutex
	var texts []string

	turn := harness.Turn{
		Prompt: "truncated stream",
		Mode:   harness.ModeSupervised,
		Env:    []string{fakeAgyEnv + "=truncated_stream"},
	}
	if err := h.Run(context.Background(), turn, func(e harness.Event) {
		if e.Type != harness.EventText {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		texts = append(texts, e.Text)
	}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	got := strings.Join(texts, "")
	if want := "前半句。后半句被截断过。"; got != want {
		t.Fatalf("expected concatenated text %q, got %q", want, got)
	}
	if strings.Count(got, "前半句。") != 1 {
		t.Fatalf("expected no duplicate prefix, got %q", got)
	}
}

// (i1) result.response 与已流式内容完全相同时不应重复发送。
func TestAgy_ResultResponseDuplicateIsDeduped(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	h := New(Options{Binary: exe})
	var mu sync.Mutex
	var texts []string

	turn := harness.Turn{
		Prompt: "duplicate result",
		Mode:   harness.ModeSupervised,
		Env:    []string{fakeAgyEnv + "=duplicate_result"},
	}
	if err := h.Run(context.Background(), turn, func(e harness.Event) {
		if e.Type != harness.EventText {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		texts = append(texts, e.Text)
	}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(texts, ""); got != "Hello from Gemini" {
		t.Fatalf("expected concatenated text %q, got %q", "Hello from Gemini", got)
	}
	if len(texts) != 1 {
		t.Fatalf("expected a single text event, got %d: %q", len(texts), texts)
	}
}

// (i2) result.response 只是最后一段（不以前文为前缀）时应追加，不吞掉。
func TestAgy_ResultResponseLastSegmentIsAppended(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	h := New(Options{Binary: exe})
	var mu sync.Mutex
	var texts []string

	turn := harness.Turn{
		Prompt: "result last segment",
		Mode:   harness.ModeSupervised,
		Env:    []string{fakeAgyEnv + "=result_last_segment"},
	}
	if err := h.Run(context.Background(), turn, func(e harness.Event) {
		if e.Type != harness.EventText {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		texts = append(texts, e.Text)
	}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	got := strings.Join(texts, "")
	if !strings.Contains(got, "最终结论：一切正常。") {
		t.Fatalf("expected last segment from result.response, got %q", got)
	}
	if strings.Count(got, "先检查目录。") != 1 {
		t.Fatalf("expected streamed text exactly once, got %q", got)
	}
}

// (i3) 正文只在 agent_response 的 text 字段里时也要展示，且不与 result.response 重复。
func TestAgy_AgentResponseTextFieldIsShown(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	h := New(Options{Binary: exe})
	var mu sync.Mutex
	var texts []string

	turn := harness.Turn{
		Prompt: "text field only",
		Mode:   harness.ModeSupervised,
		Env:    []string{fakeAgyEnv + "=text_field_only"},
	}
	if err := h.Run(context.Background(), turn, func(e harness.Event) {
		if e.Type != harness.EventText {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		texts = append(texts, e.Text)
	}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(texts, ""); got != "来自 text 字段的正文。" {
		t.Fatalf("expected concatenated text %q, got %q", "来自 text 字段的正文。", got)
	}
}

// (j) 每轮都要注入可见正文约束，避免模型静默连调工具导致界面只剩工具卡片。
func TestAgy_InjectsVisibleOutputGuard(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	for _, mode := range []harness.PermissionMode{harness.ModeSupervised, harness.ModeReadOnly} {
		h := New(Options{Binary: exe})
		turn := harness.Turn{
			Prompt: "guard check",
			Mode:   mode,
			Env:    []string{fakeAgyEnv + "=check_visible_guard"},
		}
		if err := h.Run(context.Background(), turn, func(harness.Event) {}); err != nil {
			t.Fatalf("mode %s: fake agy rejected prompt: %v", mode, err)
		}
	}
}

// (g) buildEnv 的去重与注入语义：剔除 PWD、同名后出现者覆盖（route > turn > base）、
// 强制注入 AGY_CLI_DISABLE_AUTO_UPDATE=true。
func TestBuildEnvDedupAndAutoUpdate(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"PWD=/stale",
		"HOME=/host-home",
		"AGY_CLI_DISABLE_AUTO_UPDATE=false",
	}
	env := buildEnv(base, "/work/dir",
		[]string{"FOO=bar", "HOME=/turn-home"},
		[]string{"HOME=/isolated-home", "GEMINI_FORCE_FILE_STORAGE=true"},
	)

	toMap := func(list []string) map[string]string {
		m := make(map[string]string, len(list))
		for _, kv := range list {
			if idx := strings.IndexByte(kv, '='); idx != -1 {
				m[kv[:idx]] = kv[idx+1:]
			}
		}
		return m
	}
	m := toMap(env)

	if m["PWD"] != "/work/dir" {
		t.Errorf("PWD should be the turn workdir, got %q", m["PWD"])
	}
	if m["HOME"] != "/isolated-home" {
		t.Errorf("HOME should be overridden by route env, got %q", m["HOME"])
	}
	if m["FOO"] != "bar" {
		t.Errorf("turn env should be present, got %q", m["FOO"])
	}
	if m["GEMINI_FORCE_FILE_STORAGE"] != "true" {
		t.Errorf("route env should be present, got %q", m["GEMINI_FORCE_FILE_STORAGE"])
	}
	if m["AGY_CLI_DISABLE_AUTO_UPDATE"] != "true" {
		t.Errorf("auto update must be disabled, got %q", m["AGY_CLI_DISABLE_AUTO_UPDATE"])
	}
	if m["PATH"] != "/usr/bin" {
		t.Errorf("base PATH should be preserved, got %q", m["PATH"])
	}
}

// 7a: isTransientInvalidArgument 表驱动测试
func TestAgy_IsTransientInvalidArgument(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name:  "screenshot exact original text",
			input: "INVALID_ARGUMENT (code 400): Request contains an invalid argument.",
			want:  true,
		},
		{
			name:  "lowercase invalid_argument and code 400",
			input: "invalid_argument (code 400): Request contains an invalid argument.",
			want:  true,
		},
		{
			name:  "uppercase CODE 400",
			input: "INVALID_ARGUMENT (CODE 400): Request contains an invalid argument.",
			want:  true,
		},
		{
			name:  "wrapped in agy error prefix",
			input: "agy error: INVALID_ARGUMENT (code 400): Request contains an invalid argument.",
			want:  true,
		},
		{
			name:  "rate limit keyword 429",
			input: "RESOURCE_EXHAUSTED: quota exceeded 429",
			want:  false,
		},
		{
			name:  "rate limit keyword usage limit",
			input: "usage limit reached, please retry later",
			want:  false,
		},
		{
			name:  "general error message",
			input: "process exited with status 1: context deadline exceeded",
			want:  false,
		},
		{
			name:  "invalid argument without 400",
			input: "INVALID_ARGUMENT: unknown flag --foo",
			want:  false,
		},
		{
			name:  "code 400 without invalid argument",
			input: "HTTP response code 400 Bad Request",
			want:  false,
		},
		{
			name:  "empty string",
			input: "",
			want:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransientInvalidArgument(tc.input); got != tc.want {
				t.Errorf("isTransientInvalidArgument(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}

	// 确认 isRateLimited 对 400 文本返回 false
	const text400 = "INVALID_ARGUMENT (code 400): Request contains an invalid argument."
	if isRateLimited(text400) {
		t.Errorf("isRateLimited(%q) = true, want false", text400)
	}
}

type trackingRouter struct {
	mu            sync.Mutex
	routeCalls    int
	markLimited   bool
	excludePassed []string
}

func (r *trackingRouter) Route(conversationID, model string, exclude []string) (string, []string, []string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routeCalls++
	r.excludePassed = append([]string{}, exclude...)
	return "acc-primary", nil, nil, nil
}

func (r *trackingRouter) Bind(conversationID, accountID string) {}

func (r *trackingRouter) MarkLimited(conversationID, accountID string, resetAt time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.markLimited = true
	return true
}

// 7b: 端到端测试 - 第一次 400 自动续跑，第二次成功完成
func TestAgy_TransientInvalidArgumentRetrySuccess(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	recordFile := filepath.Join(t.TempDir(), "record.json")
	router := &trackingRouter{}
	h := New(Options{
		Binary: exe,
		Router: router,
	})

	const originalPrompt = "user original prompt for transient 400 test"
	turn := harness.Turn{
		Prompt: originalPrompt,
		Mode:   harness.ModeSupervised,
		Env: []string{
			fakeAgyEnv + "=transient_400_retry",
			"MCODE_TEST_AGY_RECORD_FILE=" + recordFile,
		},
	}

	err = h.Run(context.Background(), turn, nil)
	if err != nil {
		t.Fatalf("Run expected nil error, got: %v", err)
	}

	router.mu.Lock()
	defer router.mu.Unlock()
	if router.markLimited {
		t.Errorf("router.MarkLimited should not be called for transient 400")
	}
	if len(router.excludePassed) > 0 {
		t.Errorf("exclude list should remain empty (no failover switch), got: %v", router.excludePassed)
	}

	data, err := os.ReadFile(recordFile)
	if err != nil {
		t.Fatalf("read record file: %v", err)
	}
	var records []fakeInvocationRecord
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("unmarshal records: %v", err)
	}

	if len(records) != 2 {
		t.Fatalf("expected exactly 2 invocations, got %d", len(records))
	}

	// 第一次启动：turn.SessionID 为空，不带 --conversation 参数，stdin 包含原始 prompt
	firstArgs := records[0].Args
	if slices.Contains(firstArgs, "--conversation") {
		t.Errorf("first invocation should not have --conversation, got args: %v", firstArgs)
	}
	if !strings.Contains(records[0].Stdin, originalPrompt) {
		t.Errorf("first invocation stdin should contain original prompt, got: %s", records[0].Stdin)
	}

	// 第二次启动（自动续跑）：带同一个 --conversation，未换号，stdin 包含 transientRetryPrompt 且不含原始 prompt
	secondArgs := records[1].Args
	convIdx := slices.Index(secondArgs, "--conversation")
	if convIdx == -1 || convIdx+1 >= len(secondArgs) || secondArgs[convIdx+1] != "conv-transient-400" {
		t.Errorf("second invocation missing or incorrect --conversation: %v", secondArgs)
	}

	secondStdin := records[1].Stdin
	if !strings.Contains(secondStdin, transientRetryPrompt) {
		t.Errorf("second invocation stdin should contain transientRetryPrompt, got: %s", secondStdin)
	}
	if strings.Contains(secondStdin, originalPrompt) {
		t.Errorf("second invocation stdin should NOT contain original prompt, got: %s", secondStdin)
	}
}

// 7c: 端到端测试 - 两次都 400 时 Run 返回错误，含“已自动续跑 1 次”，进程只启动了 2 次
func TestAgy_TransientInvalidArgumentRetryAlwaysFails(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	recordFile := filepath.Join(t.TempDir(), "record.json")
	h := New(Options{
		Binary: exe,
	})

	turn := harness.Turn{
		Prompt: "user original prompt for always fail test",
		Mode:   harness.ModeSupervised,
		Env: []string{
			fakeAgyEnv + "=transient_400_always",
			"MCODE_TEST_AGY_RECORD_FILE=" + recordFile,
		},
	}

	err = h.Run(context.Background(), turn, nil)
	if err == nil {
		t.Fatalf("Run expected error, got nil")
	}

	if !strings.Contains(err.Error(), "已自动续跑 1 次") {
		t.Errorf("expected error to contain '已自动续跑 1 次', got: %v", err)
	}

	data, err := os.ReadFile(recordFile)
	if err != nil {
		t.Fatalf("read record file: %v", err)
	}
	var records []fakeInvocationRecord
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("unmarshal records: %v", err)
	}

	if len(records) != 2 {
		t.Fatalf("expected exactly 2 invocations (initial + 1 retry), got %d", len(records))
	}
}

// 7d: isTransientNetworkError 表驱动测试
func TestAgy_IsTransientNetworkError(t *testing.T) {
	const realEOF = `agy error: API error (attempt 1): request failed: Post "https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse": EOF`

	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name:  "real streamGenerateContent EOF",
			input: realEOF,
			want:  true,
		},
		{
			name:  "unexpected EOF",
			input: "read tcp 10.0.0.1:443: unexpected EOF",
			want:  true,
		},
		{
			name:  "connection reset",
			input: "read tcp 10.0.0.1:443: connection reset by peer",
			want:  true,
		},
		{
			name:  "broken pipe",
			input: "write |1: broken pipe",
			want:  true,
		},
		{
			name:  "io timeout",
			input: "net/http: request canceled: i/o timeout",
			want:  true,
		},
		{
			name:  "TLS handshake timeout",
			input: "net/http: TLS handshake timeout",
			want:  true,
		},
		{
			name:  "connection refused",
			input: "dial tcp 127.0.0.1:8080: connect: connection refused",
			want:  true,
		},
		{
			name:  "no such host",
			input: "dial tcp: lookup example.com: no such host",
			want:  true,
		},
		{
			name:  "http2 stream error",
			input: "http2: stream error: PROTOCOL_ERROR",
			want:  true,
		},
		{
			name:  "api error with status 503",
			input: "API error (attempt 1): request failed with status 503 Service Unavailable",
			want:  true,
		},
		{
			name:  "http 502 bad gateway",
			input: "HTTP 502: Bad Gateway",
			want:  true,
		},
		{
			name:  "code 504 gateway timeout",
			input: "request failed: code 504 gateway timeout",
			want:  true,
		},
		{
			name:  "invalid argument 400",
			input: "INVALID_ARGUMENT (code 400): Request contains an invalid argument.",
			want:  false,
		},
		{
			name:  "http 401 unauthorized",
			input: "API error (attempt 1): request failed with status 401 Unauthorized",
			want:  false,
		},
		{
			name:  "http 403 forbidden",
			input: "API error: status 403 Forbidden",
			want:  false,
		},
		{
			name:  "rate limit 429",
			input: "RESOURCE_EXHAUSTED: quota exceeded 429",
			want:  false,
		},
		{
			name:  "usage limit",
			input: "usage limit reached, please retry later",
			want:  false,
		},
		{
			name:  "generic process failure",
			input: "process exited with status 1",
			want:  false,
		},
		{
			name:  "empty string",
			input: "",
			want:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransientNetworkError(tc.input); got != tc.want {
				t.Errorf("isTransientNetworkError(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// 7e: 正文已完整输出后 result 带 EOF，Run 视为成功、只 emit 一次 Done 且不续跑。
func TestAgy_NetworkErrorAfterCompletedOutput(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	recordFile := filepath.Join(t.TempDir(), "record.json")
	h := New(Options{Binary: exe})

	var mu sync.Mutex
	var events []harness.Event

	turn := harness.Turn{
		Prompt: "network eof after completed output",
		Mode:   harness.ModeSupervised,
		Env: []string{
			fakeAgyEnv + "=network_eof_after_text",
			"MCODE_TEST_AGY_RECORD_FILE=" + recordFile,
		},
	}

	err = h.Run(context.Background(), turn, func(e harness.Event) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	})
	if err != nil {
		t.Fatalf("Run expected nil error after completed output, got: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	var doneCount int
	var hasText bool
	for _, e := range events {
		switch e.Type {
		case harness.EventDone:
			doneCount++
			if e.StopReason != "SUCCESS" {
				t.Errorf("done stop_reason = %q, want SUCCESS", e.StopReason)
			}
		case harness.EventText:
			if strings.Contains(e.Text, "All checks passed") {
				hasText = true
			}
		}
	}
	if doneCount != 1 {
		t.Errorf("expected exactly 1 done event, got %d", doneCount)
	}
	if !hasText {
		t.Errorf("missing completed assistant text, got events: %+v", events)
	}

	data, err := os.ReadFile(recordFile)
	if err != nil {
		t.Fatalf("read record file: %v", err)
	}
	var records []fakeInvocationRecord
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("unmarshal records: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("completed turn should not be retried, expected 1 invocation, got %d", len(records))
	}
}

// 7f: 工具进行中遇 EOF，在同一会话内自动续跑一次后成功。
func TestAgy_NetworkErrorRetryAfterIncompleteOutput(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	recordFile := filepath.Join(t.TempDir(), "record.json")
	router := &trackingRouter{}
	h := New(Options{
		Binary: exe,
		Router: router,
	})

	const originalPrompt = "user original prompt for network retry test"
	turn := harness.Turn{
		Prompt: originalPrompt,
		Mode:   harness.ModeSupervised,
		Env: []string{
			fakeAgyEnv + "=network_eof_during_tool",
			"MCODE_TEST_AGY_RECORD_FILE=" + recordFile,
		},
	}

	var mu sync.Mutex
	var doneCount int

	err = h.Run(context.Background(), turn, func(e harness.Event) {
		if e.Type != harness.EventDone {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		doneCount++
	})
	if err != nil {
		t.Fatalf("Run expected nil error after network retry, got: %v", err)
	}

	router.mu.Lock()
	if router.markLimited {
		t.Errorf("router.MarkLimited should not be called for network errors")
	}
	if len(router.excludePassed) > 0 {
		t.Errorf("exclude list should remain empty (no failover switch), got: %v", router.excludePassed)
	}
	router.mu.Unlock()

	mu.Lock()
	if doneCount != 1 {
		t.Errorf("expected exactly 1 done event, got %d", doneCount)
	}
	mu.Unlock()

	data, err := os.ReadFile(recordFile)
	if err != nil {
		t.Fatalf("read record file: %v", err)
	}
	var records []fakeInvocationRecord
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("unmarshal records: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected exactly 2 invocations (initial + 1 retry), got %d", len(records))
	}

	secondArgs := records[1].Args
	convIdx := slices.Index(secondArgs, "--conversation")
	if convIdx == -1 || convIdx+1 >= len(secondArgs) || secondArgs[convIdx+1] != "conv-net-eof-tool" {
		t.Errorf("second invocation missing or incorrect --conversation: %v", secondArgs)
	}

	secondStdin := records[1].Stdin
	if !strings.Contains(secondStdin, networkRetryPrompt) {
		t.Errorf("second invocation stdin should contain networkRetryPrompt, got: %s", secondStdin)
	}
	if strings.Contains(secondStdin, originalPrompt) {
		t.Errorf("second invocation stdin should NOT contain original prompt, got: %s", secondStdin)
	}
}
