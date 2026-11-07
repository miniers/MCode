package mcodeui

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// settingsFileBudgetLines 是单个设置页生产文件的行数上限。
// 阈值来自现有最大值 2111 行的 1.1 倍，取整为 2322；后续拆分设置页时逐轮下调，不要再放宽。
const settingsFileBudgetLines = 2322

// TestSettingsFileBudget 遍历 internal/ui 下的 settings_*.go（排除测试文件），
// 任何文件超过预算就失败，并列出文件名与实际行数。
func TestSettingsFileBudget(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读取 internal/ui 目录失败: %v", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "settings_") ||
			!strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", name, err)
		}
		lines := bytes.Count(data, []byte("\n"))
		if len(data) > 0 && data[len(data)-1] != '\n' {
			lines++
		}
		if lines > settingsFileBudgetLines {
			t.Errorf("%s 有 %d 行，超过预算 %d 行；拆分后再下调预算", name, lines, settingsFileBudgetLines)
		}
	}
}
