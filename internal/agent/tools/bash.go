package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"mcode/internal/agent/gate"
	"mcode/internal/agent/shell"
)

// BashTool 返回在工作目录中执行命令的工具。
func BashTool() Tool {
	return Tool{
		Name:        "bash",
		Description: "Run a command in the working directory. Long output retains the end.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"},"timeout":{"type":"number"}},"required":["command"]}`),
		ReadOnly:    false,
		Run:         runBash,
	}
}

// CodingTools 返回内置 coding agent 所需的工具。
func CodingTools() []Tool {
	files := FileTools()
	return []Tool{files[0], BashTool(), files[2], files[1], files[5], files[4], files[3]}
}

// runBash 运行命令并将退出状态附加到输出。
func runBash(ctx context.Context, env Env, raw json.RawMessage) (Result, error) {
	var args struct {
		Command string  `json:"command"`
		Timeout float64 `json:"timeout"`
	}
	if err := parse(raw, &args); err != nil {
		return failure(err)
	}
	if args.Command == "" {
		return failure("command is required")
	}
	result, err := shell.Run(ctx, shell.Options{
		Command: args.Command,
		WorkDir: env.WorkDir,
		Env:     env.ExtraEnv,
		Timeout: gate.ClampBashTimeout(args.Timeout),
		Sandbox: env.Sandbox,
		Mode:    env.Mode,
	}, env.OnOutput)
	if err != nil {
		return Result{}, err
	}
	text := result.Output
	appendStatus := func(status string) {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += status
	}
	if result.ExitCode != 0 {
		appendStatus(fmt.Sprintf("Exit code: %d", result.ExitCode))
	}
	if result.TimedOut {
		appendStatus("Command timed out.")
	}
	if result.Cancelled {
		appendStatus("Command cancelled.")
	}
	return Result{Text: text, IsError: result.ExitCode != 0 || result.TimedOut || result.Cancelled}, nil
}
