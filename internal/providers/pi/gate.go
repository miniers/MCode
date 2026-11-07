package pi

// 本文件实现 Pi 的工具门禁：内嵌门禁扩展 mcode-gate.ts（以 `-e` 加载），以及它每轮读取的
// 运行模式注入文件（GateEnvVar）。
// 扩展监听 tool_call，除只读之外的调用都以标题 "mcode-gate {…}" 的 select 回到适配器，
// 由它按运行模式与 turn.Approver 裁决；计划模式与最终报告直接拦住。

import (
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"

	"mcode/internal/harness"
)

// GateEnvVar 是门禁扩展读取运行模式注入文件的路径环境变量。
const GateEnvVar = "MCODE_PI_GATE"

// gateExtensionFile 是门禁扩展写到磁盘上的文件名。
const gateExtensionFile = "mcode-gate.ts"

// gateExtensionSource 是 mcode 门禁扩展的源码（TypeScript，pi 加载时编译）。
//
//go:embed mcode-gate.ts
var gateExtensionSource string

// InstallGate 把内嵌的门禁扩展写到 dir 下并返回路径。
// 调用方在启动适配器时建一次目录；重复写入按覆盖处理。
func InstallGate(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, gateExtensionFile)
	if err := os.WriteFile(path, []byte(gateExtensionSource), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// gateState 是门禁扩展读取的运行模式注入内容。
type gateState struct {
	RuntimeMode     string `json:"runtimeMode"`
	InteractionMode string `json:"interactionMode"`
	FinalReport     bool   `json:"finalReport"`
}

// runtimeModeName 把 harness 权限模式映射为门禁扩展认识的 RuntimeMode 取值
// （取值：readonly / supervised / auto / fullAccess）。
// 门禁在 readonly 下放行只读命令，写操作仍走询问，由审批方决定。
func runtimeModeName(mode harness.PermissionMode) string {
	switch mode {
	case harness.ModeReadOnly:
		return "readonly"
	case harness.ModeAutoAcceptEdits, harness.ModeAuto:
		return "auto"
	case harness.ModeFullAccess:
		return "fullAccess"
	default:
		return "supervised"
	}
}

// gateContent 生成门禁文件内容：本轮的运行模式与交互模式，finalReport 固定为 false
// （最终报告的开关由上层决定，适配器不在这里下判断）。
func gateContent(turn harness.Turn) ([]byte, error) {
	interaction := turn.InteractionMode
	if interaction == "" {
		interaction = "build"
	}
	data, err := json.Marshal(gateState{
		RuntimeMode:     runtimeModeName(turn.Mode),
		InteractionMode: interaction,
		FinalReport:     false,
	})
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// prepareGateFile 保证本轮有一份门禁运行模式文件并写入当轮模式（在拉起进程前写好）。
// 返回填好 GatePath 的 Options 与清理函数：本轮为临时文件时删除它，调用方传入的路径不动。
func (h *Harness) prepareGateFile(turn harness.Turn) (Options, func(), error) {
	opts := h.opts
	cleanup := func() {}
	if opts.GatePath == "" {
		file, err := os.CreateTemp("", "mcode-pi-gate-*.json")
		if err != nil {
			return Options{}, func() {}, err
		}
		opts.GatePath = file.Name()
		_ = file.Close()
		cleanup = func() { _ = os.Remove(opts.GatePath) }
	}
	content, err := gateContent(turn)
	if err != nil {
		cleanup()
		return Options{}, func() {}, err
	}
	if err := os.WriteFile(opts.GatePath, content, 0o600); err != nil {
		cleanup()
		return Options{}, func() {}, err
	}
	return opts, cleanup, nil
}
