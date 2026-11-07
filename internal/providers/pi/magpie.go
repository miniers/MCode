package pi

// 本文件实现 Pi 的 Magpie 账号桥：把 mcode 设置里的 Magpie 端点（JSON 放在
// MCODE_PI_MAGPIE 环境变量里）注册成 pi 的 provider，并在每次回复后接收桥回传的
// 实际路由账号。扩展源码是内嵌的 magpie_bridge.ts，以额外的 `-e` 加载（见 buildArgs）。
// apiKey 只走环境变量，不进命令行参数，也不写日志。

import (
	_ "embed"
	"os"
	"path/filepath"
	"strings"
)

const (
	// MagpieEnvVar 是 Magpie 桥读取端点清单的环境变量名，须与 magpie_bridge.ts 一致。
	MagpieEnvVar = "MCODE_PI_MAGPIE"
	// MagpieBridgeEnvVar 存放 Magpie 桥扩展的路径，供门禁扩展里的 vision 子进程加载同一个桥。
	MagpieBridgeEnvVar = "MCODE_PI_MAGPIE_BRIDGE"
	// magpieBridgeFile 是桥写到磁盘上的文件名。
	magpieBridgeFile = "mcode-magpie-bridge.ts"
	// magpieMemberStatusKey 是桥回传账号所用的 setStatus 键，须与 magpie_bridge.ts 一致。
	magpieMemberStatusKey = "mcode-magpie-member"
)

// magpieBridgeSource 是 Magpie 桥扩展的源码（TypeScript，pi 加载时编译）。
//
//go:embed magpie_bridge.ts
var magpieBridgeSource string

// InstallMagpieBridge 把内嵌的 Magpie 桥写到 dir 下并返回路径（对照 InstallGate）。
// 调用方在启动适配器时建一次目录；重复写入按覆盖处理。
func InstallMagpieBridge(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, magpieBridgeFile)
	if err := os.WriteFile(path, []byte(magpieBridgeSource), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// magpieInjection 返回本轮要注入的桥扩展路径与 MCODE_PI_MAGPIE 取值。
// 扩展路径与端点 JSON 都非空才启用；任一为空时 pi 完全用它自己的配置。
func (o Options) magpieInjection() (bridge string, envValue string) {
	if o.MagpieBridge == "" || o.MagpieEnv == nil {
		return "", ""
	}
	value := strings.TrimSpace(o.MagpieEnv())
	if value == "" {
		return "", ""
	}
	return o.MagpieBridge, value
}

// magpieEnvPairs 返回本轮要追加给 pi 子进程的 Magpie 环境变量：端点清单，以及桥扩展路径。
// 端点清单为空时整组都不注入；vision 子进程要用桥路径把同一个桥带进自己的干净进程。
func magpieEnvPairs(envValue, bridge string) []string {
	if envValue == "" {
		return nil
	}
	pairs := []string{MagpieEnvVar + "=" + envValue}
	if bridge != "" {
		pairs = append(pairs, MagpieBridgeEnvVar+"="+bridge)
	}
	return pairs
}

// magpieMember 是桥经 setStatus 回传的账号信息：mcode 端点、实际上游 provider 与模型。
type magpieMember struct {
	Endpoint string `json:"endpoint"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}
