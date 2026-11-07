package omp

// 本文件内嵌 omp 的工具门禁扩展（mcode-gate.ts，以 `-e` 加载）：
// 扩展监听 tool_call，除只读与 yield 之外的调用都以标题 "mcode-gate {…}" 的 select
// 回到适配器，由它按运行模式文件（GateEnvVar）与 turn.Approver 裁决。

import (
	_ "embed"
	"os"
	"path/filepath"
)

// gateExtensionFile 是门禁扩展写到磁盘上的文件名。
const gateExtensionFile = "mcode-gate.ts"

// gateExtensionSource 是 mcode 门禁扩展的源码（TypeScript，omp 加载时编译）。
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
