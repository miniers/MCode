package mcodeui

import (
	"os"
	"strings"
	"testing"
)

// TestPanelsHaveNoColorLiterals 断言记忆、上下文、托盘额度与 PR 四个面板
// 不再直接写颜色字面量，颜色统一走 Theme* 令牌。
func TestPanelsHaveNoColorLiterals(t *testing.T) {
	files := []string{
		"memory_panel_views.go",
		"context_panel_views.go",
		"tray_quota_panel.go",
		"git_pr_ui.go",
	}
	banned := []string{"ui.Hex(", "ui.RGBA(0, 0, 0"}
	for _, name := range files {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("读取 %s: %v", name, err)
		}
		text := string(src)
		for _, literal := range banned {
			if strings.Contains(text, literal) {
				t.Errorf("%s 含颜色字面量 %q，请改用 Theme* 令牌", name, literal)
			}
		}
	}
}
