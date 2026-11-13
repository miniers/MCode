package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

type retryHelperConfig struct {
	Mode      string
	FailTimes int
	CountPath string
	LogPath   string
}

func retryHelperCommand(ctx context.Context, config string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRetryHelperProcess$")
	cmd.Env = append(os.Environ(), "MCODE_RETRY_HELPER="+config)
	return cmd
}

func TestRetryHelperProcess(t *testing.T) {
	raw := os.Getenv("MCODE_RETRY_HELPER")
	if raw == "" {
		return
	}
	var cfg retryHelperConfig
	if json.Unmarshal([]byte(raw), &cfg) != nil {
		os.Exit(2)
	}
	attempt := 0
	if cfg.CountPath != "" {
		data, _ := os.ReadFile(cfg.CountPath)
		attempt, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		attempt++
		if os.WriteFile(cfg.CountPath, []byte(strconv.Itoa(attempt)), 0o644) != nil {
			os.Exit(2)
		}
	}
	failing := attempt <= cfg.FailTimes
	write := func(v any) {
		if json.NewEncoder(os.Stdout).Encode(v) != nil {
			os.Exit(2)
		}
	}
	response := func(id any, result any) { write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result}) }
	notify := func(method string, params any) {
		write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var msg map[string]any
		if json.Unmarshal(scanner.Bytes(), &msg) != nil {
			continue
		}
		if cfg.LogPath != "" {
			f, err := os.OpenFile(cfg.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
			if err != nil {
				os.Exit(2)
			}
			_, err = fmt.Fprintln(f, string(scanner.Bytes()))
			if f.Close() != nil || err != nil {
				os.Exit(2)
			}
		}
		id := msg["id"]
		switch msg["method"] {
		case "initialize":
			response(id, map[string]any{"capabilities": map[string]any{}})
		case "thread/start", "thread/resume":
			if failing && cfg.Mode == "error" {
				write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32000, "message": "boom"}})
			} else {
				response(id, map[string]any{"thread": map[string]any{"id": "th_mock_123"}})
				if failing && cfg.Mode == "exit" {
					os.Exit(0)
				}
			}
		case "turn/start":
			response(id, map[string]any{"turn": map[string]any{"id": "turn_mock_abc"}})
			if cfg.Mode == "accepted" {
				notify("turn/started", map[string]any{"threadId": "th_mock_123", "turn": map[string]any{"id": "turn_mock_abc"}})
			}
			notify("item/agentMessage/delta", map[string]any{"itemId": "msg_1", "delta": "OK"})
			if failing && cfg.Mode == "emit_then_exit" {
				os.Exit(0)
			}
			notify("turn/completed", map[string]any{"turn": map[string]any{"id": "turn_mock_abc", "status": "completed"}})
		}
	}
	os.Exit(0)
}
