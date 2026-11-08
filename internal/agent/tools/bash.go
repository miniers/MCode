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
		Description: "Run a command in the working directory. Long output retains the end. Set run_in_background for long-running services or watchers.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"},"timeout":{"type":"number"},"run_in_background":{"type":"boolean"}},"required":["command"]}`),
		ReadOnly:    false,
		Run:         runBash,
	}
}

// BashOutputTool 返回读取后台任务新输出的工具。
func BashOutputTool() Tool {
	return Tool{
		Name:        "bash_output",
		Description: "Read new output from a background bash job started with run_in_background.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
		ReadOnly:    true,
		Run:         runBashOutput,
	}
}

// KillBashTool 返回停止后台任务的工具。
func KillBashTool() Tool {
	return Tool{
		Name:        "kill_bash",
		Description: "Stop a background bash job started with run_in_background.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
		ReadOnly:    false,
		Run:         runKillBash,
	}
}

// CodingTools 返回内置 coding agent 所需的工具。
func CodingTools() []Tool {
	files := FileTools()
	return []Tool{files[0], BashTool(), BashOutputTool(), KillBashTool(), files[2], files[1], files[5], files[4], files[3]}
}

// runBash 运行命令并将退出状态附加到输出。
func runBash(ctx context.Context, env Env, raw json.RawMessage) (Result, error) {
	var args struct {
		Command         string  `json:"command"`
		Timeout         float64 `json:"timeout"`
		RunInBackground bool    `json:"run_in_background"`
	}
	if err := parse(raw, &args); err != nil {
		return failure(err)
	}
	if args.Command == "" {
		return failure("command is required")
	}
	opts := shell.Options{
		Command: args.Command,
		WorkDir: env.WorkDir,
		Env:     env.ExtraEnv,
		Timeout: gate.ClampBashTimeout(args.Timeout),
		Sandbox: env.Sandbox,
		Mode:    env.Mode,
	}
	if args.RunInBackground {
		id, pid, err := shell.Start(ctx, opts)
		if err != nil {
			return Result{}, err
		}
		return Result{Text: fmt.Sprintf("Started background job %s (pid %d). Use bash_output to read new output and kill_bash to stop it.", id, pid)}, nil
	}
	result, err := shell.Run(ctx, opts, env.OnOutput)
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

// runBashOutput 返回后台任务自上次读取之后的新输出。
func runBashOutput(ctx context.Context, env Env, raw json.RawMessage) (Result, error) {
	var args struct {
		ID string `json:"id"`
	}
	if err := parse(raw, &args); err != nil {
		return failure(err)
	}
	if args.ID == "" {
		return failure("id is required")
	}
	text, running, exitCode, err := shell.Output(args.ID)
	if err != nil {
		return failure(err)
	}
	status := "running"
	if !running {
		status = fmt.Sprintf("exited (code %d)", exitCode)
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return Result{Text: text + "Status: " + status}, nil
}

// runKillBash 停止后台任务。
func runKillBash(ctx context.Context, env Env, raw json.RawMessage) (Result, error) {
	var args struct {
		ID string `json:"id"`
	}
	if err := parse(raw, &args); err != nil {
		return failure(err)
	}
	if args.ID == "" {
		return failure("id is required")
	}
	if err := shell.Kill(args.ID); err != nil {
		return failure(err)
	}
	return Result{Text: "Stopped background job " + args.ID + "."}, nil
}
