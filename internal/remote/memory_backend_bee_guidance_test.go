package remote

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/projctx"
)

// writeBeeGuidanceSkill 在 home 下写一个全局技能，供技能清单注入用例使用。
func writeBeeGuidanceSkill(t *testing.T, home, name, description string) {
	t.Helper()
	dir := filepath.Join(home, ".agents", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\n正文"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 普通会话（lead）注入技能目录且描述上限 200 个字符，子代理线程（bee）上限 100 个字符。
func TestBuildGuidance_CompactSkillIndexLimits(t *testing.T) {
	home := t.TempDir()
	workDir := t.TempDir()
	t.Setenv("HOME", home)
	longDescription := strings.Repeat("很长的技能描述", 40)
	writeBeeGuidanceSkill(t, home, "demo", longDescription)

	backend := NewMemoryBackend(nil)
	t.Cleanup(backend.Close)

	runes := []rune(longDescription)
	if len(runes) <= projctx.LeadSkillIndexDescriptionMaxRunes {
		t.Fatalf("前置条件失败：描述只有 %d 个字符", len(runes))
	}
	leadDescription := string(runes[:projctx.LeadSkillIndexDescriptionMaxRunes]) + "…"
	beeDescription := string(runes[:beeSkillIndexDescriptionMaxRunes]) + "…"

	lead := backend.buildGuidance(workDir, projctx.LeadSkillIndexDescriptionMaxRunes, false)
	if !strings.Contains(lead, "[mcode 技能目录]") {
		t.Fatalf("普通会话应注入技能目录，got %q", lead)
	}
	if !strings.Contains(lead, leadDescription) {
		t.Fatalf("普通会话的技能描述应截断为 %d 个字符，got %q", projctx.LeadSkillIndexDescriptionMaxRunes, lead)
	}
	if strings.Contains(lead, longDescription) {
		t.Fatalf("普通会话不应带完整描述，got %q", lead)
	}
	if !strings.Contains(lead, filepath.Join(home, ".agents", "skills", "demo", "SKILL.md")) {
		t.Fatalf("技能目录应包含技能 path，got %q", lead)
	}
	if strings.Contains(lead, "[mcode 已启用技能]") {
		t.Fatalf("普通会话不应再注入全量清单标题，got %q", lead)
	}

	compact := backend.buildGuidance(workDir, beeSkillIndexDescriptionMaxRunes, true)
	if !strings.Contains(compact, "[mcode 技能目录]") {
		t.Fatalf("子代理引导词应带精简技能目录，got %q", compact)
	}
	if !strings.Contains(compact, "demo") {
		t.Fatalf("精简目录应包含 demo，got %q", compact)
	}
	if !strings.Contains(compact, filepath.Join(home, ".agents", "skills", "demo", "SKILL.md")) {
		t.Fatalf("精简目录应包含技能 path，got %q", compact)
	}
	if !strings.Contains(compact, beeDescription) {
		t.Fatalf("子代理技能描述应截断为 %d 个字符，got %q", beeSkillIndexDescriptionMaxRunes, compact)
	}
	if strings.Contains(compact, leadDescription) {
		t.Fatalf("子代理技能描述不应超过 %d 个字符，got %q", beeSkillIndexDescriptionMaxRunes, compact)
	}
	if strings.Contains(compact, longDescription) {
		t.Fatalf("精简目录不应带完整描述，got %q", compact)
	}
	if strings.Contains(compact, "[mcode 已启用技能]") {
		t.Fatalf("子代理引导词不应包含全量清单标题，got %q", compact)
	}

	// maxSkillRunes 为 0 时仍走带完整描述的全量清单分支。
	full := backend.buildGuidance(workDir, 0, false)
	if !strings.Contains(full, "[mcode 已启用技能]") {
		t.Fatalf("maxSkillRunes 为 0 时应注入全量技能清单，got %q", full)
	}
	if !strings.Contains(full, longDescription) {
		t.Fatalf("maxSkillRunes 为 0 时应带完整描述，got %q", full)
	}

	if got := backend.defaultGuidanceBuilder(workDir); got != lead {
		t.Fatalf("defaultGuidanceBuilder 应与 buildGuidance(workDir, %d, false) 一致：\ngot  %q\nwant %q", projctx.LeadSkillIndexDescriptionMaxRunes, got, lead)
	}
	if got := backend.beeGuidanceBuilder(workDir); got != compact {
		t.Fatalf("beeGuidanceBuilder 应与 buildGuidance(workDir, %d, true) 一致：\ngot  %q\nwant %q", beeSkillIndexDescriptionMaxRunes, got, compact)
	}
}
