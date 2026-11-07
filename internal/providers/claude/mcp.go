package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// mcpConfigFileMode 是临时 MCP 配置文件的权限：文件里有 MCP 的 Bearer 令牌，只允许当前用户读写。
const mcpConfigFileMode = 0o600

// externalizeMCPConfig 把命令行里内联的 "--mcp-config <JSON>" 换成 0600 临时文件的路径。
// 配置里有 mcode 与外部 MCP 的 Bearer 令牌，放在 argv 里同机其他用户用 ps 就能看到；
// claude CLI 的 --mcp-config 支持传文件路径。返回替换后的参数与清理函数，
// 调用方应在本轮子进程结束后调用清理函数删除临时文件。
func externalizeMCPConfig(args []string) ([]string, func(), error) {
	out := make([]string, 0, len(args))
	var files []string
	cleanup := func() {
		for _, file := range files {
			_ = os.Remove(file)
		}
	}

	for i := 0; i < len(args); i++ {
		if args[i] != "--mcp-config" || i+1 >= len(args) {
			out = append(out, args[i])
			continue
		}
		value := args[i+1]
		i++
		if !strings.HasPrefix(strings.TrimSpace(value), "{") {
			// 已经是文件路径（mergeMCPArgs 只产出内联 JSON，这里按防御性放行处理）。
			out = append(out, "--mcp-config", value)
			continue
		}
		name, err := writeMCPConfigFile(value)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		files = append(files, name)
		out = append(out, "--mcp-config", name)
	}
	return out, cleanup, nil
}

// writeMCPConfigFile 把内联 JSON 写成权限 0600 的临时文件并返回路径。
func writeMCPConfigFile(data string) (string, error) {
	file, err := os.CreateTemp("", "mcode-claude-mcp-*.json")
	if err != nil {
		return "", fmt.Errorf("claude: 创建 MCP 配置临时文件: %w", err)
	}
	name := file.Name()
	// CreateTemp 建出的文件本来就是 0600，这里显式设定以免依赖实现细节。
	_ = file.Chmod(mcpConfigFileMode)
	if _, err := file.WriteString(data); err != nil {
		_ = file.Close()
		_ = os.Remove(name)
		return "", fmt.Errorf("claude: 写入 MCP 配置临时文件: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("claude: 写入 MCP 配置临时文件: %w", err)
	}
	return name, nil
}

// mergeMCPArgs 保留显式配置中的其他服务器，同名服务器以 mcode 代理为准。
func mergeMCPArgs(generated, extra []string) ([]string, error) {
	servers := make(map[string]json.RawMessage)
	var args []string
	// 先读用户配置，后读代理配置，最终只生成一个 --mcp-config。
	for _, source := range [][]string{extra, generated} {
		for i := 0; i < len(source); i++ {
			arg := source[i]
			var value string
			if arg == "--mcp-config" {
				i++
				if i >= len(source) {
					return nil, fmt.Errorf("claude: missing --mcp-config value")
				}
				value = source[i]
			} else if strings.HasPrefix(arg, "--mcp-config=") {
				value = strings.TrimPrefix(arg, "--mcp-config=")
			} else {
				args = append(args, arg)
				continue
			}
			data := []byte(value)
			if !strings.HasPrefix(strings.TrimSpace(value), "{") {
				var err error
				data, err = os.ReadFile(value)
				if err != nil {
					return nil, fmt.Errorf("claude: read MCP configuration: %w", err)
				}
			}
			var config struct {
				Servers map[string]json.RawMessage `json:"mcpServers"`
			}
			if err := json.Unmarshal(data, &config); err != nil {
				return nil, fmt.Errorf("claude: invalid MCP configuration")
			}
			for name, entry := range config.Servers {
				servers[name] = entry
			}
		}
	}
	if len(servers) > 0 {
		data, err := json.Marshal(map[string]any{"mcpServers": servers})
		if err != nil {
			return nil, err
		}
		args = append(args, "--mcp-config", string(data))
	}
	return args, nil
}
