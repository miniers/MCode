package copilot

import (
	"fmt"
	"os"
	"strings"
)

// mcpConfigFileMode 是临时 MCP 配置文件的权限：文件里有 MCP 的 Bearer 令牌，只允许当前用户读写。
const mcpConfigFileMode = 0o600

// externalizeMCPConfig 把命令行里内联的 "--additional-mcp-config <JSON>" 换成 "@<临时文件路径>"。
// 配置里有 mcode 与外部 MCP 的 Bearer 令牌，放在 argv 里同机其他用户用 ps 就能看到；
// copilot CLI 的该参数支持 JSON 字符串或文件路径，帮助文本是
// `Additional MCP servers configuration as JSON string or file path (prefix with @)`，
// 只有带 @ 前缀的取值才按文件路径读取。返回替换后的参数与清理函数，
// 调用方应在本轮子进程退出后调用清理函数删除临时文件。
func externalizeMCPConfig(args []string) ([]string, func(), error) {
	out := make([]string, 0, len(args))
	var files []string
	cleanup := func() {
		for _, file := range files {
			_ = os.Remove(file)
		}
	}

	for i := 0; i < len(args); i++ {
		if args[i] != "--additional-mcp-config" || i+1 >= len(args) {
			out = append(out, args[i])
			continue
		}
		value := args[i+1]
		i++
		if !strings.HasPrefix(strings.TrimSpace(value), "{") {
			// 已经是 "@<路径>"（用户 ExtraArgs 里自带的配置），按原样放行。
			out = append(out, "--additional-mcp-config", value)
			continue
		}
		name, err := writeMCPConfigFile(value)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		files = append(files, name)
		out = append(out, "--additional-mcp-config", "@"+name)
	}
	return out, cleanup, nil
}

// writeMCPConfigFile 把内联 JSON 写成权限 0600 的临时文件并返回绝对路径。
func writeMCPConfigFile(data string) (string, error) {
	file, err := os.CreateTemp("", "mcode-copilot-mcp-*.json")
	if err != nil {
		return "", fmt.Errorf("copilot: 创建 MCP 配置临时文件: %w", err)
	}
	name := file.Name()
	// CreateTemp 建出的文件本来就是 0600，这里显式设定以免依赖实现细节。
	_ = file.Chmod(mcpConfigFileMode)
	if _, err := file.WriteString(data); err != nil {
		_ = file.Close()
		_ = os.Remove(name)
		return "", fmt.Errorf("copilot: 写入 MCP 配置临时文件: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("copilot: 写入 MCP 配置临时文件: %w", err)
	}
	return name, nil
}
