package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// 假 app-server 的行为模式，供 codexAppServerCommand 使用。
const (
	// appServerPong 应答 initialize/thread.*/turn.*，并立即推一条 PONG 文本结束该轮。
	appServerPong = "pong"
	// appServerTurnLog 同 pong，但 initialize 带回 codex-mock/0.165.0 的 userAgent，轮次文本为 OK。
	appServerTurnLog = "turnlog"
	// appServerMalformed 同 pong，但在 turn/start 应答前推三行无法解析的坏行。
	appServerMalformed = "malformed"
	// appServerSlow 同 pong，但当轮输入包含 SLOW 时延迟 2 秒才结束该轮。
	appServerSlow = "slow"
	// appServerEvictSlow 在 turn/start 应答后延迟一秒才结束该轮，便于在轮次进行中观察进程池。
	appServerEvictSlow = "evict-slow"
	// appServerInterrupt 在 turn/start 应答后挂起，等 turn/interrupt 才结束该轮。
	appServerInterrupt = "interrupt"
	// appServerSlash 支持 thread/compact/start 与 review/start，普通轮次推 PLAIN 文本。
	appServerSlash = "slash"
	// appServerCompactWithoutStart 的 compact 只推文本与 turn/completed，不发 turn/started。
	appServerCompactWithoutStart = "compact_no_started"
	// appServerSteer 在 turn/start 后挂起，等 turn/steer 才结束该轮。
	appServerSteer = "steer"
)

type codexAppServerConfig struct {
	Mode    string `json:"mode"`
	LogPath string `json:"logPath,omitempty"`
}

// codexAppServerCommand 返回以给定模式运行假 app-server 的测试二进制，logPath 非空时记录收到的每条请求。
func codexAppServerCommand(ctx context.Context, mode, logPath string) *exec.Cmd {
	cfg, _ := json.Marshal(codexAppServerConfig{Mode: mode, LogPath: logPath})
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCodexAppServerProcess$")
	cmd.Env = append(os.Environ(), "MCODE_CODEX_APPSERVER="+string(cfg))
	return cmd
}

// TestCodexAppServerProcess 在设置了 MCODE_CODEX_APPSERVER 时扮演假 app-server，否则立即返回。
func TestCodexAppServerProcess(t *testing.T) {
	_ = t
	raw := os.Getenv("MCODE_CODEX_APPSERVER")
	if raw == "" {
		return
	}
	var cfg codexAppServerConfig
	if json.Unmarshal([]byte(raw), &cfg) != nil {
		os.Exit(2)
	}
	write := func(v any) {
		if json.NewEncoder(os.Stdout).Encode(v) != nil {
			os.Exit(2)
		}
	}
	response := func(id any, result any) { write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result}) }
	notify := func(method string, params any) {
		write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	}
	completed := func(status string) {
		notify("turn/completed", map[string]any{"turn": map[string]any{"id": "turn_mock_abc", "status": status}})
	}
	logRequest := func(line []byte) {
		if cfg.LogPath == "" {
			return
		}
		f, err := os.OpenFile(cfg.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			os.Exit(2)
		}
		if _, err = fmt.Fprintln(f, string(line)); err != nil || f.Close() != nil {
			os.Exit(2)
		}
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Bytes()
		var msg map[string]any
		if json.Unmarshal(line, &msg) != nil {
			continue
		}
		logRequest(line)
		method, _ := msg["method"].(string)
		id := msg["id"]
		switch method {
		case "initialize":
			result := map[string]any{"capabilities": map[string]any{}}
			if cfg.Mode == appServerTurnLog {
				result["userAgent"] = "codex-mock/0.165.0"
			}
			response(id, result)
		case "thread/start", "thread/resume":
			response(id, map[string]any{"thread": map[string]any{"id": "th_mock_123"}})
		case "thread/compact/start":
			response(id, map[string]any{})
			noStarted := cfg.Mode == appServerCompactWithoutStart
			if !noStarted {
				notify("turn/started", map[string]any{"threadId": "th_mock_123", "turn": map[string]any{"id": "turn_mock_abc"}})
			}
			text := "COMPACTED"
			if noStarted {
				text = "NOS"
			}
			notify("item/agentMessage/delta", map[string]any{"itemId": "msg_1", "delta": text})
			completed("completed")
		case "review/start":
			response(id, map[string]any{"reviewThreadId": "th_mock_123", "turn": map[string]any{"id": "turn_mock_abc"}})
			notify("item/agentMessage/delta", map[string]any{"itemId": "msg_1", "delta": "REVIEWED"})
			completed("completed")
		case "turn/start":
			if cfg.Mode == appServerMalformed {
				fmt.Fprintln(os.Stdout, "bad token=secretvalue")
				fmt.Fprintln(os.Stdout, "second bad line")
				fmt.Fprintln(os.Stdout, "third bad line")
			}
			response(id, map[string]any{"turn": map[string]any{"id": "turn_mock_abc"}})
			params, _ := msg["params"].(map[string]any)
			input, _ := params["input"].([]any)
			text := ""
			if len(input) > 0 {
				if first, ok := input[0].(map[string]any); ok {
					text, _ = first["text"].(string)
				}
			}
			switch cfg.Mode {
			case appServerSteer, appServerInterrupt:
				continue
			case appServerEvictSlow:
				time.Sleep(time.Second)
			case appServerSlow:
				if strings.Contains(text, "SLOW") {
					time.Sleep(2 * time.Second)
				}
			}
			delta := "PONG"
			switch cfg.Mode {
			case appServerTurnLog:
				delta = "OK"
			case appServerSlash:
				delta = "PLAIN"
			}
			notify("item/agentMessage/delta", map[string]any{"itemId": "msg_1", "delta": delta})
			completed("completed")
		case "turn/steer":
			response(id, map[string]any{})
			completed("completed")
		case "turn/interrupt":
			response(id, map[string]any{})
			completed("interrupted")
		}
	}
	os.Exit(0)
}
