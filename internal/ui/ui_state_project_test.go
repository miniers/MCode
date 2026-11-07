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

	// 只记过交互模式的项目，仍然退回全局模型记忆。
	saveProjectInteractionMode("p2", "plan")
	if got := resolveModelPickerSelectionForProject(providers, ModelSelection{}, "p2", nil); got.Model != "sonnet" {
		t.Fatalf("只记过交互模式时应退回全局记忆，got %+v", got)
	}
	if sel, ok := loadProjectModelSelection("p2"); !ok || sel.InteractionMode != "plan" || sel.Provider != "" {
		t.Fatalf("p2 应只记录交互模式，got %+v ok=%v", sel, ok)
	}
}

// TestPendingThreadUsesProjectInteractionMode 验证新建会话的交互模式按项目记忆，没有记录时用 build。
func TestPendingThreadUsesProjectInteractionMode(t *testing.T) {
	resetProjectModelState(t)
	_, state := newPendingTestState(t)
	t.Cleanup(state.close)

	saveProjectInteractionMode("p1", "plan")
	state.createThread("p1")

	state.mu.Lock()
	pending := state.pendingThread
	state.mu.Unlock()
	if pending == nil {
		t.Fatal("应进入待创建会话")
	}
	if got := pending.Thread.InteractionMode; got != "plan" {
		t.Fatalf("应沿用项目上次的 plan 模式，got %q", got)
	}

	// 另一个没记录过的项目回落到 build。
	state.mu.Lock()
	state.projects = append(state.projects, Project{ID: "p2", Name: "Proj2"})
	state.threadsByProj["p2"] = nil
	state.mu.Unlock()
	state.createThread("p2")
	state.mu.Lock()
	pending = state.pendingThread
	state.mu.Unlock()
	if pending == nil || pending.Thread.InteractionMode != "build" {
		t.Fatalf("没有项目记录时应回落到 build，got %+v", pending)
	}
}
