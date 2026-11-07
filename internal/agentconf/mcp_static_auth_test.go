package agentconf

import (
	"os"
	"path/filepath"
	"testing"
)

// TestStaticAuthServerKeys 覆盖：只有带静态鉴权头且填了取值的远程服务才算静态鉴权。
func TestStaticAuthServerKeys(t *testing.T) {
	useTestSecretStore(t)

	home := t.TempDir()
	projectDir := t.TempDir()
	dataDir := t.TempDir()

	// .claude.json 里放三个服务：带 Authorization 的 http、无头的 http、stdio。
	claudeCfg := `{
  "mcpServers": {
    "mem-with-auth": {
      "type": "http",
      "url": "https://mem.example.com/mcp",
      "headers": {"Authorization": "Bearer abc123"}
    },
    "web-with-api-key": {
      "type": "http",
      "url": "https://web.example.com/mcp",
      "headers": {"X-Team-API-Key": "k-1"}
    },
    "remote-no-header": {
      "type": "http",
      "url": "https://plain.example.com/mcp"
    },
    "local-stdio": {
      "command": "npx",
      "args": ["-y", "some-mcp"]
    }
  }
}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(claudeCfg), 0o600); err != nil {
		t.Fatalf("写入 .claude.json 失败：%v", err)
	}

	opts := Options{Home: home, ProjectDir: projectDir, DataDir: dataDir}
	keys := StaticAuthServerKeys(opts)

	want := map[string]bool{
		"claude:global:mem-with-auth":    true,
		"claude:global:web-with-api-key": true,
	}
	if len(keys) != len(want) {
		t.Fatalf("静态鉴权服务数量 = %d，期望 %d，实际 %v", len(keys), len(want), keys)
	}
	for key := range want {
		if !keys[key] {
			t.Errorf("期望 %q 出现在静态鉴权集合里，实际 %v", key, keys)
		}
	}
	for _, key := range []string{"claude:global:remote-no-header", "claude:global:local-stdio"} {
		if keys[key] {
			t.Errorf("%q 不应被判为静态鉴权", key)
		}
	}
}

// TestStaticAuthServerKeysEmptyHeaderValue 覆盖：头存在但取值为空（或只有 Bearer）时不算静态鉴权。
func TestStaticAuthServerKeysEmptyHeaderValue(t *testing.T) {
	useTestSecretStore(t)

	home := t.TempDir()
	claudeCfg := `{
  "mcpServers": {
    "empty-value": {
      "type": "http",
      "url": "https://a.example.com/mcp",
      "headers": {"Authorization": "   "}
    },
    "bearer-only": {
      "type": "http",
      "url": "https://b.example.com/mcp",
      "headers": {"Authorization": "Bearer   "}
    }
  }
}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(claudeCfg), 0o600); err != nil {
		t.Fatalf("写入 .claude.json 失败：%v", err)
	}

	keys := StaticAuthServerKeys(Options{Home: home, ProjectDir: t.TempDir(), DataDir: t.TempDir()})
	if len(keys) != 0 {
		t.Fatalf("空凭据不应计入静态鉴权，实际 %v", keys)
	}
}

// TestStaticAuthServerKeysScanError 覆盖：扫描出错时返回空集合而不是崩掉。
func TestStaticAuthServerKeysScanError(t *testing.T) {
	useTestSecretStore(t)

	// 把 Home 与 ProjectDir 都指到普通文件上：四个配置来源全部读取失败，
	// ScanMCP 会返回错误，此时应得到空集合。
	dir := t.TempDir()
	brokenHome := filepath.Join(dir, "home-is-a-file")
	brokenProject := filepath.Join(dir, "project-is-a-file")
	for _, p := range []string{brokenHome, brokenProject} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("准备损坏的目录路径失败：%v", err)
		}
	}

	keys := StaticAuthServerKeys(Options{Home: brokenHome, ProjectDir: brokenProject, DataDir: t.TempDir()})
	if keys == nil {
		t.Fatal("扫描出错时应返回空集合而不是 nil")
	}
	if len(keys) != 0 {
		t.Fatalf("扫描出错时应返回空集合，实际 %v", keys)
	}
}
