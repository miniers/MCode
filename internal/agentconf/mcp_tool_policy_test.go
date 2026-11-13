package agentconf

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMCPToolPolicyLoadMissingFile(t *testing.T) {
	dir := t.TempDir()
	policy, err := LoadMCPToolPolicy(MCPToolPolicyPath(dir))
	if err != nil {
		t.Fatalf("文件不存在时应返回空策略且不报错, got: %v", err)
	}
	if len(policy.Disabled) != 0 || len(policy.AutoApprove) != 0 {
		t.Fatalf("文件不存在时应返回空策略, got: %+v", policy)
	}
	if policy.IsDisabled("srv", "tool") || policy.IsAutoApproved("srv", "tool") {
		t.Fatal("空策略不应命中任何工具")
	}

	// 路径为空时同样返回空策略
	if policy, err := LoadMCPToolPolicy(""); err != nil || policy.IsDisabled("srv", "tool") {
		t.Fatalf("空路径应返回空策略, got policy=%+v err=%v", policy, err)
	}
}

func TestMCPToolPolicySaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := MCPToolPolicyPath(dir)
	if path != filepath.Join(dir, "mcp_tool_policy.json") {
		t.Fatalf("策略文件路径不符: %s", path)
	}

	policy := MCPToolPolicy{
		Disabled:    map[string]map[string]bool{"demo": {"danger": true}},
		AutoApprove: map[string]map[string]bool{"demo": {"echo": true}},
	}
	if err := SaveMCPToolPolicy(path, policy); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("策略文件应存在: %v", err)
	}
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o600 {
		t.Fatalf("策略文件权限应为 0600, got %o", perm)
	}

	loaded, err := LoadMCPToolPolicy(path)
	if err != nil {
		t.Fatalf("读取策略失败: %v", err)
	}
	if !loaded.IsDisabled("demo", "danger") {
		t.Fatal("禁用状态应被读回")
	}
	if !loaded.IsAutoApproved("demo", "echo") {
		t.Fatal("免审批状态应被读回")
	}
	if loaded.IsDisabled("demo", "echo") || loaded.IsAutoApproved("demo", "danger") {
		t.Fatal("未配置的工具不应命中策略")
	}
	if loaded.IsDisabled("other", "danger") {
		t.Fatal("其他服务不应命中策略")
	}
}

func TestMCPToolPolicySaveOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := MCPToolPolicyPath(dir)

	if err := SaveMCPToolPolicy(path, MCPToolPolicy{Disabled: map[string]map[string]bool{"demo": {"a": true}}}); err != nil {
		t.Fatalf("首次保存失败: %v", err)
	}
	if err := SaveMCPToolPolicy(path, MCPToolPolicy{AutoApprove: map[string]map[string]bool{"demo": {"b": true}}}); err != nil {
		t.Fatalf("覆盖保存失败: %v", err)
	}

	loaded, err := LoadMCPToolPolicy(path)
	if err != nil {
		t.Fatalf("读取策略失败: %v", err)
	}
	if loaded.IsDisabled("demo", "a") {
		t.Fatal("覆盖保存后旧禁用状态应被清除")
	}
	if !loaded.IsAutoApproved("demo", "b") {
		t.Fatal("覆盖保存后新免审批状态应生效")
	}
}

func TestMCPToolPolicyLoadInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := MCPToolPolicyPath(dir)
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMCPToolPolicy(path); err == nil {
		t.Fatal("损坏的策略文件应返回错误")
	}
}

func TestMCPToolPolicyFoldThresholdRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := MCPToolPolicyPath(dir)

	// 默认值 0：表示使用默认阈值，且不落盘为显式字段
	if err := SaveMCPToolPolicy(path, MCPToolPolicy{}); err != nil {
		t.Fatalf("保存默认策略失败: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取策略文件失败: %v", err)
	}
	if strings.Contains(string(raw), "foldThreshold") {
		t.Fatalf("默认阈值不应写入显式字段, got: %s", raw)
	}
	loaded, err := LoadMCPToolPolicy(path)
	if err != nil {
		t.Fatalf("读取策略失败: %v", err)
	}
	if loaded.FoldThreshold != 0 || loaded.EffectiveFoldThreshold() != DefaultMCPFoldThreshold {
		t.Fatalf("未配置时应使用默认阈值 %d, got field=%d effective=%d",
			DefaultMCPFoldThreshold, loaded.FoldThreshold, loaded.EffectiveFoldThreshold())
	}

	// 关闭折叠写 -1，并且不能顺手清掉工具级开关
	if err := SaveMCPToolPolicy(path, MCPToolPolicy{
		Disabled:      map[string]map[string]bool{"demo": {"danger": true}},
		AutoApprove:   map[string]map[string]bool{"demo": {"echo": true}},
		FoldThreshold: -1,
	}); err != nil {
		t.Fatalf("保存关闭折叠的策略失败: %v", err)
	}
	loaded, err = LoadMCPToolPolicy(path)
	if err != nil {
		t.Fatalf("读取策略失败: %v", err)
	}
	if loaded.FoldThreshold != -1 || loaded.EffectiveFoldThreshold() != -1 {
		t.Fatalf("关闭折叠的 -1 应被读回, got field=%d effective=%d", loaded.FoldThreshold, loaded.EffectiveFoldThreshold())
	}
	if !loaded.IsDisabled("demo", "danger") || !loaded.IsAutoApproved("demo", "echo") {
		t.Fatal("保存折叠阈值不应影响工具级开关")
	}

	// 自定义阈值按原值生效
	if err := SaveMCPToolPolicy(path, MCPToolPolicy{FoldThreshold: 12}); err != nil {
		t.Fatalf("保存自定义阈值失败: %v", err)
	}
	loaded, err = LoadMCPToolPolicy(path)
	if err != nil {
		t.Fatalf("读取策略失败: %v", err)
	}
	if loaded.EffectiveFoldThreshold() != 12 {
		t.Fatalf("自定义阈值应为 12, got %d", loaded.EffectiveFoldThreshold())
	}
}

func TestSplitMCPToolName(t *testing.T) {
	cases := []struct {
		name       string
		wantServer string
		wantTool   string
		wantOK     bool
	}{
		{"mcp__demo__echo", "demo", "echo", true},
		{"mcp__my_server__run_cmd", "my_server", "run_cmd", true},
		// 工具名含双下划线时只按第一段切分
		{"mcp__demo__a__b", "demo", "a__b", true},
		{"Bash", "", "", false},
		{"mcp__demo", "", "", false},
		{"mcp__demo__", "", "", false},
		{"mcp____echo", "", "", false},
	}
	for _, c := range cases {
		server, tool, ok := SplitMCPToolName(c.name)
		if ok != c.wantOK || server != c.wantServer || tool != c.wantTool {
			t.Errorf("SplitMCPToolName(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.name, server, tool, ok, c.wantServer, c.wantTool, c.wantOK)
		}
	}
}
