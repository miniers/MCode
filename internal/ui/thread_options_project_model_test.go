package mcodeui

import (
	"testing"
)

// clearProjectModelBuckets 清掉按项目选择的全局缓存，避免同包其它用例留下的项目记录影响断言。
func clearProjectModelBuckets() {
	lastModelSelectionMu.Lock()
	projectModelCache = nil
	projectModelDirty = nil
	projectModelGeneration = nil
	lastModelSelectionMu.Unlock()
}

// TestNewThreadOptionsUsesProjectModel 验证新建会话按项目恢复模型：
// 项目 A 有记录时用项目 A 的模型，项目 B 没记录时退回全局最近选择。
func TestNewThreadOptionsUsesProjectModel(t *testing.T) {
	resetLastSelectionMergeState(t)
	clearProjectModelBuckets()

	global := ModelSelection{Provider: "claude", Model: "sonnet"}
	setLastModelSelection(&global)
	defer setLastModelSelection(nil)

	saveProjectModelSelection("proj-a", ModelSelection{Provider: "codex", Model: "o3"})

	providers := []ProviderInfo{
		{ID: "claude", Name: "Claude", Models: []ModelInfo{{ID: "", IsDefault: true}, {ID: "sonnet"}}},
		{ID: "codex", Name: "Codex", Models: []ModelInfo{{ID: "gpt-5.5", IsDefault: true}, {ID: "o3"}}},
	}

	gotA := newThreadOptionsForProject("proj-a", "claude", providers, nil)
	if gotA.Provider != "codex" || gotA.Model != "o3" {
		t.Fatalf("项目 A 应使用项目记忆 codex/o3，got %+v", gotA)
	}

	gotB := newThreadOptionsForProject("proj-b", "claude", providers, nil)
	if gotB.Provider != "claude" || gotB.Model != "sonnet" {
		t.Fatalf("项目 B 无记录时应退回全局 claude/sonnet，got %+v", gotB)
	}

	// 项目桶有完整记录时优先于全局（gotA 已覆盖）；不带项目上下文时只用全局记忆。
	if blank := newThreadOptionsForProject("", "claude", providers, nil); blank != gotB {
		t.Fatalf("空项目 ID 应只用全局记忆，got %+v，want %+v", blank, gotB)
	}
}
