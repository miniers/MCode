package reviewcheck

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// TestProjectConfigRoundTrip 验证保存后可完整读回，缺文件返回零值。
func TestProjectConfigRoundTrip(t *testing.T) {
	dataDir := t.TempDir()
	projectDir := t.TempDir()
	cfg := ProjectConfig{Command: "  make check  ", TimeoutSeconds: 90, RunTests: true}
	if err := SaveProjectConfig(dataDir, projectDir, cfg); err != nil {
		t.Fatalf("SaveProjectConfig: %v", err)
	}
	got, err := LoadProjectConfig(dataDir, projectDir)
	if err != nil {
		t.Fatalf("LoadProjectConfig: %v", err)
	}
	if got.Command != "make check" || got.TimeoutSeconds != 90 || !got.RunTests {
		t.Fatalf("读回配置 = %+v，期望命令去空白且原样保留 90 秒、run_tests=true", got)
	}
	info, err := os.Stat(ProjectConfigPath(dataDir, projectDir))
	if err != nil {
		t.Fatalf("Stat 配置: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("配置权限 = %o，期望 600", perm)
	}

	otherProject := t.TempDir()
	zero, err := LoadProjectConfig(dataDir, otherProject)
	if err != nil {
		t.Fatalf("读取不存在的配置: %v", err)
	}
	if zero != (ProjectConfig{}) {
		t.Errorf("不存在的配置 = %+v，期望零值", zero)
	}
}

// TestProjectConfigTimeoutClamp 验证 0 按 180、越界夹到 30..600。
func TestProjectConfigTimeoutClamp(t *testing.T) {
	dataDir := t.TempDir()
	projectDir := t.TempDir()
	cases := []struct {
		saved int
		want  int
	}{
		{0, 180},
		{5, 30},
		{90, 90},
		{9999, 600},
	}
	for _, tc := range cases {
		if err := SaveProjectConfig(dataDir, projectDir, ProjectConfig{TimeoutSeconds: tc.saved}); err != nil {
			t.Fatalf("保存 %d 秒: %v", tc.saved, err)
		}
		got, err := LoadProjectConfig(dataDir, projectDir)
		if err != nil {
			t.Fatalf("读取 %d 秒: %v", tc.saved, err)
		}
		if got.TimeoutSeconds != tc.want {
			t.Errorf("TimeoutSeconds(%d) = %d，期望 %d", tc.saved, got.TimeoutSeconds, tc.want)
		}
	}
}

// TestProjectConfigPathMatchesSkillsHash 验证路径哈希与 internal/agentconf 的
// 项目技能文件一致：绝对项目路径 sha256 前 8 字节 hex。
func TestProjectConfigPathMatchesSkillsHash(t *testing.T) {
	dataDir := t.TempDir()
	projectDir := t.TempDir()
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	sum := sha256.Sum256([]byte(abs))
	want := filepath.Join(dataDir, "review-checks", hex.EncodeToString(sum[:8])+".json")
	if got := ProjectConfigPath(dataDir, projectDir); got != want {
		t.Fatalf("ProjectConfigPath = %q，期望 %q", got, want)
	}
	if got := ProjectConfigPath(dataDir, ""); got != "" {
		t.Errorf("空项目目录路径 = %q，期望空", got)
	}
	if _, err := LoadProjectConfig("", ""); err != nil {
		t.Errorf("空目录 LoadProjectConfig 返回错误: %v", err)
	}
}
