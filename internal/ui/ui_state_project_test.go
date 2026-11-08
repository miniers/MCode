package mcodeui

import (
	"testing"
)

// resetProjectModelState 在 resetLastSelectionMergeState 之上清掉按项目选择的全局缓存，
// 避免同包其它用例留下的项目记录影响这里的断言。
func resetProjectModelState(t *testing.T) string {
	t.Helper()
	path := resetLastSelectionMergeState(t)
	lastModelSelectionMu.Lock()
	projectModelCache = nil
	projectModelDirty = nil
	projectModelGeneration = nil
	lastModelSelectionMu.Unlock()
	return path
}

// clearProjectModelState 清掉按项目选择的全局缓存，用来模拟“另一个进程重新读盘”。
func clearProjectModelState() {
	lastModelSelectionMu.Lock()
	projectModelCache = nil
	projectModelDirty = nil
	projectModelGeneration = nil
	lastModelSelectionMu.Unlock()
}

// TestProjectModelSelectionRoundTrip 验证按项目记录模型选择：写完能读回，落盘后重读仍在。
func TestProjectModelSelectionRoundTrip(t *testing.T) {
	resetProjectModelState(t)

	want := ModelSelection{Provider: "codex", Model: "o3", Effort: "high", Fast: true}
	saveProjectModelSelection("p1", want)

	got, ok := loadProjectModelSelection("p1")
	if !ok {
		t.Fatalf("p1 应记录到模型选择，got %+v", got)
	}
	if got.Provider != want.Provider || got.Model != want.Model || got.Effort != want.Effort || got.Fast != want.Fast {
		t.Fatalf("p1 记录 = %+v, want %+v", got, want)
	}

	// 没有项目归属的会话落到 "none" 桶，不会污染具体项目。
	saveProjectModelSelection("", ModelSelection{Provider: "claude", Model: "sonnet"})
	if sel, ok := loadProjectModelSelection(""); !ok || sel.Provider != "claude" {
		t.Fatalf("空项目 ID 应写入 none 桶，got %+v ok=%v", sel, ok)
	}
	if sel, ok := loadProjectModelSelection("p1"); !ok || sel.Provider != "codex" {
		t.Fatalf("none 桶不应覆盖 p1，got %+v ok=%v", sel, ok)
	}

	// 等落盘完成，清缓存后重新读盘，验证真的写进了 ui-state.json。
	waitUIStateWrites()
	clearLastSelectionGlobals()
	clearProjectModelState()
	got, ok = loadProjectModelSelection("p1")
	if !ok || got.Model != "o3" || got.Effort != "high" || !got.Fast {
		t.Fatalf("重读磁盘后 p1 记录 = %+v ok=%v, want %+v", got, ok, want)
	}
}

// TestProjectModelSelectionFallsBackToGlobal 验证项目桶优先、没有记录时退回全局记忆。
func TestProjectModelSelectionFallsBackToGlobal(t *testing.T) {
	resetProjectModelState(t)

	providers := []ProviderInfo{
		{ID: "claude", Name: "Claude", Models: []ModelInfo{{ID: "sonnet"}}},
		{ID: "codex", Name: "Codex", Models: []ModelInfo{{ID: "o3"}}},
	}
	global := ModelSelection{Provider: "claude", Model: "sonnet"}
	setLastModelSelection(&global)
	defer setLastModelSelection(nil)

	// 项目没有任何记录：用全局记忆。
	if got := resolveModelPickerSelectionForProject(providers, ModelSelection{}, "p-new", nil); got.Model != "sonnet" {
		t.Fatalf("没有项目记录时应退回全局记忆，got %+v", got)
	}

	// 项目桶有记录：用项目记录，即使全局记忆指向另一个模型。
	saveProjectModelSelection("p1", ModelSelection{Provider: "codex", Model: "o3"})
	if got := resolveModelPickerSelectionForProject(providers, ModelSelection{}, "p1", nil); got.Provider != "codex" || got.Model != "o3" {
		t.Fatalf("应按项目记录恢复，got %+v", got)
	}
}

// TestPendingThreadStartsInBuildMode 验证 Plan 只在当前会话生效：新建会话一律从 build 开始。
func TestPendingThreadStartsInBuildMode(t *testing.T) {
	resetProjectModelState(t)
	_, state := newPendingTestState(t)
	t.Cleanup(state.close)

	state.createThread("p1")
	state.mu.Lock()
	pending := state.pendingThread
	state.mu.Unlock()
	if pending == nil || pending.Thread.InteractionMode != "build" {
		t.Fatalf("新建会话应从 build 开始，got %+v", pending)
	}
}
