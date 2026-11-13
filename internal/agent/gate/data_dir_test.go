package gate

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestDataDirProtection(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(t.TempDir(), "data")
	t.Setenv("MCODE_DATA", "")
	if slices.Contains(controlPaths(Input{}), data) || mcodeConfigJSON(filepath.Join(data, "config.json"), home) {
		t.Fatal("未设置 MCODE_DATA 时不应保护新目录")
	}
	if !slices.Contains(controlPaths(Input{}), filepath.Join(home, ".mcode")) || !mcodeConfigJSON(filepath.Join(home, ".mcode", "config.json"), home) {
		t.Fatal("默认数据目录未受保护")
	}
	t.Setenv("MCODE_DATA", data)
	if !slices.Contains(controlPaths(Input{}), data) || !mcodeConfigJSON(filepath.Join(data, "config.json"), home) {
		t.Fatal("MCODE_DATA 未受保护")
	}
	if mcodeConfigJSON(filepath.Join(data, "nested", "config.json"), home) {
		t.Fatal("嵌套 JSON 不应匹配配置文件")
	}
}
