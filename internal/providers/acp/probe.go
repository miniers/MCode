package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"mcode/internal/harness"
	"mcode/internal/procattr"
	"mcode/internal/procutil"
	"mcode/internal/providers/loginenv"
)

// ProbedModel 是 Agent 在 session/new 应答里广告的一个模型（ACP models.availableModels）。
type ProbedModel struct {
	ID          string
	Name        string
	Description string
	Current     bool // 会话开局时 Agent 自己选中的模型
}

// ProbeModels 使用当前供应商配置的可执行文件读取目录。
func (h *Harness) ProbeModels(ctx context.Context) ([]ProbedModel, error) {
	bin, err := loginenv.LookPath(h.opts.Binary)
	if err != nil {
		return nil, err
	}
	return ProbeModels(ctx, h.opts.Agent, bin)
}

// ProbeModels 启动一次 ACP 会话只为读取模型目录：
// initialize、在临时目录 session/new，读出应答里的 models 后结束进程。
// 后台读取目录不触发登录（不调用 authenticate）。
func ProbeModels(ctx context.Context, agent Agent, binary string) ([]ProbedModel, error) {
	if binary == "" {
		binary = DefaultBinary(agent)
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	dir := filepath.Join(os.TempDir(), "mcode-acp-catalog")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	cmd := procattr.HideWindow(exec.Command(binary, LaunchArgs(agent, harness.ModeSupervised)...))
	setProcessGroup(cmd)
	cmd.Dir = dir
	cmd.Env = buildEnv(loginenv.Environ(), dir, ExtraEnv(agent), nil)
	stderrBuf := procutil.NewTailBuffer(4096)
	cmd.Stderr = stderrBuf
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := waitLaunchSlot(ctx); err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	conn := NewConn(stdout, stdin)
	defer func() {
		_ = conn.Close()
		_ = killProcessGroup(cmd, syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = killProcessGroup(cmd, syscall.SIGKILL)
		}
	}()
	// 探测期间 Agent 不该发请求；万一发了一律拒绝，避免它卡在等待应答上。
	conn.SetRequestHandler(func(method string, _ json.RawMessage) (any, error) {
		return nil, fmt.Errorf("acp probe: %s not supported", method)
	})

	initParams := map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{"fs": map[string]any{"readTextFile": false, "writeTextFile": false}, "terminal": false},
		"clientInfo":         map[string]any{"name": "mcode", "title": "mcode", "version": "1.0.0"},
	}
	if err := conn.Call(ctx, "initialize", initParams, nil); err != nil {
		return nil, probeError("initialize", err, stderrBuf)
	}

	var res struct {
		Models *struct {
			CurrentModelID  string `json:"currentModelId"`
			AvailableModels []struct {
				ModelID     string `json:"modelId"`
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"availableModels"`
		} `json:"models"`
	}
	if err := conn.Call(ctx, "session/new", map[string]any{"cwd": dir, "mcpServers": []any{}}, &res); err != nil {
		return nil, probeError("session/new", err, stderrBuf)
	}
	if res.Models == nil {
		return nil, nil
	}
	out := make([]ProbedModel, 0, len(res.Models.AvailableModels))
	for _, m := range res.Models.AvailableModels {
		if strings.TrimSpace(m.ModelID) == "" {
			continue
		}
		out = append(out, ProbedModel{
			ID:          m.ModelID,
			Name:        m.Name,
			Description: m.Description,
			Current:     m.ModelID == res.Models.CurrentModelID,
		})
	}
	return out, nil
}

func probeError(step string, err error, stderr *procutil.TailBuffer) error {
	if tail := strings.TrimSpace(stderr.String()); tail != "" {
		return fmt.Errorf("acp probe %s: %w (stderr: %s)", step, err, tail)
	}
	return fmt.Errorf("acp probe %s: %w", step, err)
}
