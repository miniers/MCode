package agentconf

import (
	"os"
	"path/filepath"
	"testing"
)

// 缺配置文件时没有默认停用名单，表示新会话全部默认启用。
func TestNewThreadDisabledServersMissingFile(t *testing.T) {
	opts := Options{Home: t.TempDir(), DataDir: t.TempDir()}
	if got := NewThreadDisabledServers(opts); len(got) != 0 {
		t.Fatalf("缺文件时应返回空名单，got %v", got)
	}
}

// 写入后能读回同一份名单；空名单等价于「全部默认启用」，也要能清空旧值。
func TestNewThreadDisabledServersRoundTrip(t *testing.T) {
	opts := Options{Home: t.TempDir(), DataDir: t.TempDir()}
	want := []string{"claude:global:ctx7", "custom:project:db"}
	if err := SetNewThreadDisabledServers(opts, want); err != nil {
		t.Fatalf("写入默认停用名单失败：%v", err)
	}

	got := NewThreadDisabledServers(opts)
	if len(got) != len(want) {
		t.Fatalf("读回的名单长度不对：got %v want %v", got, want)
	}
	seen := make(map[string]bool, len(got))
	for _, k := range got {
		seen[k] = true
	}
	for _, k := range want {
		if !seen[k] {
			t.Fatalf("读回的名单缺少 %s：got %v", k, got)
		}
	}

	if err := SetNewThreadDisabledServers(opts, nil); err != nil {
		t.Fatalf("清空默认停用名单失败：%v", err)
	}
	if got := NewThreadDisabledServers(opts); len(got) != 0 {
		t.Fatalf("清空后应返回空名单，got %v", got)
	}

	// 旧版本文件没有 newThreadDisabled 键，读出必须是空名单而不是报错。
	legacy := `{"mcpServers":{},"disabledServers":["legacy-srv"]}`
	if err := os.WriteFile(filepath.Join(opts.DataDir, "mcp.json"), []byte(legacy), 0600); err != nil {
		t.Fatalf("写入旧配置失败：%v", err)
	}
	if got := NewThreadDisabledServers(opts); len(got) != 0 {
		t.Fatalf("旧配置应读出空名单，got %v", got)
	}
}

// newThreadDisabled 与 disabledServers 写的是同一份文件，互不覆盖，其它字段也要原样保留。
func TestNewThreadDisabledServersKeepOtherFields(t *testing.T) {
	useTestSecretStore(t)
	opts := Options{Home: t.TempDir(), DataDir: t.TempDir()}

	if err := SaveMCPServer(opts, MCPServer{
		Name:      "ctx7",
		Scope:     "global",
		Transport: "stdio",
		Command:   "npx",
		Args:      []string{"ctx7"},
	}); err != nil {
		t.Fatalf("保存自定义服务失败：%v", err)
	}
	if err := SetMCPServerEnabled(opts, "ctx7", false); err != nil {
		t.Fatalf("写入停用列表失败：%v", err)
	}
	if err := SetNewThreadDisabledServers(opts, []string{"claude:global:ctx7"}); err != nil {
		t.Fatalf("写入默认停用名单失败：%v", err)
	}

	cfg, err := loadCustomMCPConfig(getMCPConfigPath(opts, "global"))
	if err != nil {
		t.Fatalf("读取全局配置失败：%v", err)
	}
	if _, ok := cfg.MCPServers["ctx7"]; !ok {
		t.Fatal("写默认名单不应覆盖 mcpServers")
	}
	if len(cfg.DisabledServers) != 1 || cfg.DisabledServers[0] != "ctx7" {
		t.Fatalf("写默认名单不应覆盖 disabledServers：%v", cfg.DisabledServers)
	}
	if len(cfg.NewThreadDisabled) != 1 || cfg.NewThreadDisabled[0] != "claude:global:ctx7" {
		t.Fatalf("默认停用名单写入错误：%v", cfg.NewThreadDisabled)
	}

	// 反向：再改停用列表，默认名单不能被抹掉。
	if err := SetMCPServerEnabled(opts, "other", false); err != nil {
		t.Fatalf("再次写入停用列表失败：%v", err)
	}
	cfg, err = loadCustomMCPConfig(getMCPConfigPath(opts, "global"))
	if err != nil {
		t.Fatalf("再次读取全局配置失败：%v", err)
	}
	if len(cfg.NewThreadDisabled) != 1 || cfg.NewThreadDisabled[0] != "claude:global:ctx7" {
		t.Fatalf("改停用列表不应覆盖默认名单：%v", cfg.NewThreadDisabled)
	}
	if len(cfg.DisabledServers) != 2 {
		t.Fatalf("停用列表应有两条记录：%v", cfg.DisabledServers)
	}
}
