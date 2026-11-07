package pi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/harness"
)

// TestMCPServersEnvValue 验证 MCODE_MCP_SERVERS 的组装规则：有线程 ID 时用按线程的地址
// 且不带 X-Mcode-Workdir；没有时用共享地址并带上该头；外部端点按名字过滤。
func TestMCPServersEnvValue(t *testing.T) {
	builtin := harness.BuiltinMCP{
		ThreadBase:  "http://127.0.0.1:9/mcp/mcode/",
		ThreadToken: "thread-token",
		URL:         "http://127.0.0.1:9/mcp",
		Token:       "shared-token",
	}
	external := []harness.MCPEndpoint{
		{Name: "mcode", URL: "http://should-be-skipped"},
		{Name: "mcode_approval", URL: "http://should-be-skipped-too"},
		{Name: "remote", URL: "http://remote/mcp", Token: "remote-token"},
		{Name: "open", URL: "http://open/mcp"},
	}
	tests := []struct {
		name    string
		turn    harness.Turn
		builtin harness.BuiltinMCP
		ext     []harness.MCPEndpoint
		want    string
		wantOK  bool
	}{
		{
			name:    "有线程 ID 时用按线程的地址",
			turn:    harness.Turn{ThreadID: "t42", WorkDir: "/repo"},
			builtin: builtin,
			want:    `[{"name":"mcode","url":"http://127.0.0.1:9/mcp/mcode/t42","headers":{"Authorization":"Bearer thread-token"}}]`,
			wantOK:  true,
		},
		{
			name:    "没有线程 ID 时用共享地址并带 workdir 头",
			turn:    harness.Turn{WorkDir: "/repo"},
			builtin: builtin,
			want:    `[{"name":"mcode","url":"http://127.0.0.1:9/mcp","headers":{"Authorization":"Bearer shared-token","X-Mcode-Workdir":"/repo"}}]`,
			wantOK:  true,
		},
		{
			name: "外部端点过滤重名服务",
			ext:  external,
			want: `[{"name":"remote","url":"http://remote/mcp","headers":{"Authorization":"Bearer remote-token"}},` +
				`{"name":"open","url":"http://open/mcp"}]`,
			wantOK: true,
		},
		{
			name:    "内置与外部合并",
			turn:    harness.Turn{ThreadID: "t42", WorkDir: "/repo"},
			builtin: builtin,
			ext:     external,
			want: `[{"name":"mcode","url":"http://127.0.0.1:9/mcp/mcode/t42","headers":{"Authorization":"Bearer thread-token"}},` +
				`{"name":"remote","url":"http://remote/mcp","headers":{"Authorization":"Bearer remote-token"}},` +
				`{"name":"open","url":"http://open/mcp"}]`,
			wantOK: true,
		},
		{
			name:   "未启用时不注入",
			turn:   harness.Turn{WorkDir: "/repo"},
			want:   "",
			wantOK: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var gotWorkDir string
			value, ok := mcpServersEnvValue(test.turn, test.builtin, func(workDir, threadID string) []harness.MCPEndpoint {
				gotWorkDir = workDir
				return test.ext
			})
			if ok != test.wantOK {
				t.Fatalf("ok = %v, want %v", ok, test.wantOK)
			}
			if value != test.want {
				t.Fatalf("清单错误:\n得到 %s\n期望 %s", value, test.want)
			}
			if test.ext != nil && gotWorkDir != test.turn.WorkDir {
				t.Fatalf("外部端点应以 workDir 查询: 得到 %q, 期望 %q", gotWorkDir, test.turn.WorkDir)
			}
		})
	}
}

// TestInstallMCPBridge 验证桥接扩展被写到给定目录，且内容就是嵌入的源码。
func TestInstallMCPBridge(t *testing.T) {
	dir := t.TempDir()
	path, err := installMCPBridge(dir)
	if err != nil {
		t.Fatalf("写入 MCP 桥: %v", err)
	}
	if path != filepath.Join(dir, mcpBridgeFile) {
		t.Fatalf("路径错误: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 MCP 桥: %v", err)
	}
	if string(data) != mcpBridgeSource || !strings.Contains(string(data), "loadMCPTools") {
		t.Fatalf("桥内容与嵌入源码不一致")
	}
}
