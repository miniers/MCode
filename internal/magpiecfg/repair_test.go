package magpiecfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testCodexPlan(path string) Plan {
	return Plan{
		Agent: CodexAgent,
		Files: []FilePlan{{
			Path:   path,
			Format: FormatTOML,
			Fields: []Field{
				{
					Path:      "model_providers.mcode-magpie",
					TOMLTable: true,
					Value: map[string]any{
						"name":                      "Magpie",
						"base_url":                  "https://magpie.test/v1",
						"wire_api":                  "responses",
						"requires_openai_auth":      true,
						"experimental_bearer_token": "sk-test",
					},
				},
				{Path: "model", Value: "gateway-model"},
				{Path: "model_provider", Value: "mcode-magpie"},
			},
		}},
	}
}

// 掉电把文件写成整份 0x00 时，按 applied 记录补回 mcode 管理的字段，并把全零
// 文件原样备份出来。
func TestRepairZeroedRestoresTOMLFields(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(t.TempDir(), "config.toml")
	writeSettings(t, path, "model = \"user-model\"\n")
	if err := Connect(dataDir, testCodexPlan(path)); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	before := readSettings(t, path)

	zeroed := make([]byte, len(before))
	if err := os.WriteFile(path, zeroed, 0o644); err != nil {
		t.Fatalf("模拟全零文件: %v", err)
	}

	repaired, err := RepairZeroed(dataDir)
	if err != nil {
		t.Fatalf("RepairZeroed: %v", err)
	}
	if len(repaired) != 1 || repaired[0] != path {
		t.Fatalf("应只修复 %s，实际 %v", path, repaired)
	}
	if got := readSettings(t, path); got != before {
		t.Fatalf("修复后的内容应与最后一次写入一致：\n got=%q\nwant=%q", got, before)
	}

	backups, err := filepath.Glob(path + ".zeroed-*.bak")
	if err != nil || len(backups) != 1 {
		t.Fatalf("应留下一份全零备份，实际 %v err=%v", backups, err)
	}
	saved, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatalf("读备份: %v", err)
	}
	if string(saved) != string(zeroed) {
		t.Fatalf("备份应是原样的全零文件，实际 %q", saved)
	}

	if status := Drift(dataDir, CodexAgent); status.State != StatusApplied {
		t.Fatalf("修复后应仍是已接管状态，实际 %+v", status)
	}
}

// 正常文件不动，也不留备份。
func TestRepairZeroedLeavesHealthyFileAlone(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(t.TempDir(), "config.toml")
	writeSettings(t, path, "model = \"user-model\"\n")
	if err := Connect(dataDir, testCodexPlan(path)); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	before := readSettings(t, path)

	repaired, err := RepairZeroed(dataDir)
	if err != nil {
		t.Fatalf("RepairZeroed: %v", err)
	}
	if len(repaired) != 0 {
		t.Fatalf("正常文件不该被修复，实际 %v", repaired)
	}
	if got := readSettings(t, path); got != before {
		t.Fatalf("正常文件不该被改写：\n got=%q\nwant=%q", got, before)
	}
	if backups, _ := filepath.Glob(path + ".zeroed-*.bak"); len(backups) != 0 {
		t.Fatalf("正常文件不该留备份，实际 %v", backups)
	}
}

// 没有接管记录时，全零文件也不碰：那不是我们写的东西。
func TestRepairZeroedSkipsFilesWithoutAppliedRecord(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(t.TempDir(), "config.toml")
	zeroed := make([]byte, 1094)
	if err := os.WriteFile(path, zeroed, 0o644); err != nil {
		t.Fatalf("写全零文件: %v", err)
	}

	repaired, err := RepairZeroed(dataDir)
	if err != nil {
		t.Fatalf("RepairZeroed: %v", err)
	}
	if len(repaired) != 0 {
		t.Fatalf("没有接管记录时不该修复，实际 %v", repaired)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(zeroed) {
		t.Fatalf("没有接管记录时不该改文件：len=%d err=%v", len(got), err)
	}
}

// JSON 配置（claude 的 settings.json）走同一条修复路径。
func TestRepairZeroedRestoresJSONFields(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(t.TempDir(), "settings.json")
	writeSettings(t, path, claudeOriginal)
	if err := Connect(dataDir, testClaudePlan(path)); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	before := readSettings(t, path)

	if err := os.WriteFile(path, make([]byte, len(before)), 0o600); err != nil {
		t.Fatalf("模拟全零文件: %v", err)
	}
	repaired, err := RepairZeroed(dataDir)
	if err != nil {
		t.Fatalf("RepairZeroed: %v", err)
	}
	if len(repaired) != 1 || repaired[0] != path {
		t.Fatalf("应只修复 %s，实际 %v", path, repaired)
	}
	got := readSettings(t, path)
	if !strings.Contains(got, "https://magpie.test") || !strings.Contains(got, "gateway-model") {
		t.Fatalf("修复后应含接管字段，实际 %q", got)
	}
	if status := Drift(dataDir, ClaudeAgent); status.State != StatusApplied {
		t.Fatalf("修复后应仍是已接管状态，实际 %+v", status)
	}
}
