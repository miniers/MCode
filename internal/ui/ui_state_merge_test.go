package mcodeui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mcode/internal/testenv"
)

// clearLastSelectionGlobals 把最近选择的缓存、脏标记与待写侧栏状态恢复成初始值。
func clearLastSelectionGlobals() {
	lastModelSelectionMu.Lock()
	lastModelSelectionPath = ""
	lastModelSelectionLoaded = false
	lastModelSelectionCache = nil
	lastSwarmSelectionCache = nil
	lastModelSelectionModTime = time.Time{}
	lastSelectionWritesPending = 0
	lastModelDirty = false
	lastSwarmEnabledDirty = false
	lastSwarmBeesDirty = false
	lastSelectionPendingSidebar = pendingUISidebarState{}
	lastModelSelectionMu.Unlock()
}

// resetLastSelectionMergeState 把全局状态恢复成初始值并把数据目录指向独立临时目录，
// 返回 ui-state.json 路径；测试结束时先等写盘完成再清理，避免污染后续测试。
func resetLastSelectionMergeState(t *testing.T) string {
	t.Helper()
	// 先等上一轮测试的后台写盘结束，再重置全局状态。
	waitUIStateWrites()
	dir := t.TempDir()
	t.Setenv("MCODE_DATA", dir)
	testenv.TempHome(t)
	t.Cleanup(func() {
		waitUIStateWrites()
		clearLastSelectionGlobals()
	})
	clearLastSelectionGlobals()
	return filepath.Join(dir, uiStateFileName)
}

// writeRawUIStateFile 直接改写 ui-state.json（模拟另一个进程的写入），并把修改时间设为 modTime。
func writeRawUIStateFile(t *testing.T, path string, file uiStateFile, modTime time.Time) {
	t.Helper()
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		t.Fatalf("marshal ui-state: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write ui-state: %v", err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("chtimes ui-state: %v", err)
	}
}

// mustReadUIStateFile 读取并解析落盘的 ui-state.json。
func mustReadUIStateFile(t *testing.T, path string) uiStateFile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 ui-state.json 失败: %v", err)
	}
	var file uiStateFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("解析 ui-state.json 失败: %v", err)
	}
	return file
}

// TestSaveLastModelSelectionKeepsLatest 验证快速连续保存三次模型选择时，落盘的是最后一次的值。
func TestSaveLastModelSelectionKeepsLatest(t *testing.T) {
	path := resetLastSelectionMergeState(t)

	selections := []ModelSelection{
		{Provider: "claude", Model: "sonnet"},
		{Provider: "codex", Model: "gpt-5.5", Effort: "medium"},
		{Provider: "pi", Model: "default", Effort: "high", Fast: true},
	}
	for _, sel := range selections {
		saveLastModelSelection(sel)
	}
	waitUIStateWrites()

	want := selections[len(selections)-1]
	file := mustReadUIStateFile(t, path)
	if file.LastModel != want {
		t.Fatalf("落盘的 last_model 应为最后一次保存的 %+v, got %+v", want, file.LastModel)
	}
	if sel, ok := loadLastModelSelection(); !ok || sel != want {
		t.Fatalf("内存缓存应为最后一次保存的 %+v, got %+v ok=%v", want, sel, ok)
	}
}

// TestPersistUIStateAndModelSaveKeepBoth 验证 persistUIState 与 saveLastModelSelection 交替执行后，
// last_model 与 sidebar_width 都保留各自的最新值（侧栏写盘不再用旧 last_* 覆盖新记忆）。
func TestPersistUIStateAndModelSaveKeepBoth(t *testing.T) {
	path := resetLastSelectionMergeState(t)
	s := newChatState(NewFakeClient())
	WithUIStatePersistence()(s)

	for i := 0; i < 3; i++ {
		saveLastModelSelection(ModelSelection{Provider: "pi", Model: fmt.Sprintf("model-%d", i)})
		s.mu.Lock()
		s.sidebarWidth = float32(300 + i)
		s.mu.Unlock()
		persistUIState(s)
	}
	waitUIStateWrites()

	file := mustReadUIStateFile(t, path)
	if file.LastModel.Provider != "pi" || file.LastModel.Model != "model-2" {
		t.Fatalf("last_model 应保留最后一次保存的记忆，got %+v", file.LastModel)
	}
	if file.SidebarWidth != 302 {
		t.Fatalf("sidebar_width 应保留最后一次侧栏宽度 302, got %v", file.SidebarWidth)
	}
	if sel, ok := loadLastModelSelection(); !ok || sel.Model != "model-2" {
		t.Fatalf("内存缓存不应被侧栏写盘冲掉，got %+v ok=%v", sel, ok)
	}
}

// TestFlushKeepsWritesArrivingDuringDiskWrite 验证写盘期间进来的新保存不会被这次写盘清脏覆盖：
// 第一个写者取完快照、释放锁、还没写盘时插入一次 saveLastModelSelection(B) 与一次
// persistUIState(新宽度)，等所有写盘结束后磁盘与缓存里都必须是 B 和新宽度。
func TestFlushKeepsWritesArrivingDuringDiskWrite(t *testing.T) {
	path := resetLastSelectionMergeState(t)
	s := newChatState(NewFakeClient())
	WithUIStatePersistence()(s)

	first := ModelSelection{Provider: "pi", Model: "model-first"}
	late := ModelSelection{Provider: "codex", Model: "gpt-5.5", Effort: "medium"}
	var once sync.Once
	uiStateBeforeWriteHook = func() {
		once.Do(func() {
			saveLastModelSelection(late)
			s.mu.Lock()
			s.sidebarWidth = 302
			s.mu.Unlock()
			persistUIState(s)
		})
	}
	t.Cleanup(func() { uiStateBeforeWriteHook = nil })

	// 第一次写盘的快照里只有 first，写盘期间才进来 late 与新宽度。
	saveLastModelSelection(first)
	waitUIStateWrites()

	file := mustReadUIStateFile(t, path)
	if file.LastModel != late {
		t.Fatalf("落盘的 last_model 应为写盘期间保存的 %+v, got %+v", late, file.LastModel)
	}
	if file.SidebarWidth != 302 {
		t.Fatalf("落盘的 sidebar_width 应为写盘期间的新宽度 302, got %v", file.SidebarWidth)
	}
	if sel, ok := loadLastModelSelection(); !ok || sel != late {
		t.Fatalf("缓存里的模型选择应为 %+v, got %+v ok=%v", late, sel, ok)
	}
}

// TestSaveModelKeepsExternalSwarmHeads 验证外部改写只动了 last_swarm.heads 之后，
// 本进程保存模型不会把外部写入的 heads 冲掉。
func TestSaveModelKeepsExternalSwarmHeads(t *testing.T) {
	path := resetLastSelectionMergeState(t)

	base := time.Now().Add(-time.Hour)
	off := false
	writeRawUIStateFile(t, path, uiStateFile{
		LastSwarm: &LastSwarmSelection{
			Enabled: &off,
			Bees:    &ModelSelection{Provider: "claude", Model: "sonnet"},
		},
	}, base)

	// 触发本进程加载缓存。
	if sel, ok := loadLastModelSelection(); ok {
		t.Fatalf("文件里没有 last_model，加载应返回 false, got %+v", sel)
	}
	if swarm := loadLastSwarmSelection(); swarm.Bees == nil || swarm.Bees.Model != "sonnet" {
		t.Fatalf("首次加载应读到 sonnet, got %+v", swarm.Bees)
	}

	// 另一个进程只把 heads 换成 codex。
	writeRawUIStateFile(t, path, uiStateFile{
		LastSwarm: &LastSwarmSelection{
			Enabled: &off,
			Bees:    &ModelSelection{Provider: "codex", Model: "gpt-5.5"},
		},
	}, base.Add(time.Minute))

	wantModel := ModelSelection{Provider: "pi", Model: "default", Effort: "high"}
	saveLastModelSelection(wantModel)
	waitUIStateWrites()

	file := mustReadUIStateFile(t, path)
	if file.LastModel != wantModel {
		t.Fatalf("last_model 应为本进程保存的 %+v, got %+v", wantModel, file.LastModel)
	}
	if file.LastSwarm == nil || file.LastSwarm.Bees == nil {
		t.Fatalf("last_swarm.heads 不应丢失, got %+v", file.LastSwarm)
	}
	if file.LastSwarm.Bees.Provider != "codex" || file.LastSwarm.Bees.Model != "gpt-5.5" {
		t.Fatalf("外部写入的 heads 应被保留, got %+v", file.LastSwarm.Bees)
	}
	if file.LastSwarm.Enabled == nil || *file.LastSwarm.Enabled {
		t.Fatalf("外部写入的 enabled=false 应被保留, got %+v", file.LastSwarm.Enabled)
	}
}

// TestSaveModelWithCorruptFileDoesNotZero 验证 ui-state.json 解析失败时保存模型不会把文件写成零值。
func TestSaveModelWithCorruptFileDoesNotZero(t *testing.T) {
	path := resetLastSelectionMergeState(t)

	corrupt := []byte("{not json")
	if err := os.WriteFile(path, corrupt, 0o644); err != nil {
		t.Fatal(err)
	}

	saveLastModelSelection(ModelSelection{Provider: "pi", Model: "default"})
	waitUIStateWrites()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 ui-state.json 失败: %v", err)
	}
	if string(got) != string(corrupt) {
		t.Fatalf("损坏的文件不应被覆盖，got %q", string(got))
	}
}

// TestLegacyHydraKeyMigratesToSwarm 验证旧键 last_hydra 在保存时迁移成 last_swarm，并保留其它字段。
func TestLegacyHydraKeyMigratesToSwarm(t *testing.T) {
	path := resetLastSelectionMergeState(t)

	on := true
	legacy := uiStateFile{
		UIState:         UIState{SidebarWidth: 300},
		LastModel:       ModelSelection{Provider: "codex", Model: "gpt-5.5"},
		LegacyLastSwarm: &LastSwarmSelection{Enabled: &on, Bees: &ModelSelection{Provider: "claude", Model: "sonnet"}},
	}
	writeRawUIStateFile(t, path, legacy, time.Now().Add(-time.Hour))

	// 触发本进程加载旧键。
	if swarm := loadLastSwarmSelection(); swarm.Enabled == nil || !*swarm.Enabled {
		t.Fatalf("旧键 last_hydra 应被读到, got %+v", swarm.Enabled)
	}

	wantModel := ModelSelection{Provider: "pi", Model: "default"}
	saveLastModelSelection(wantModel)
	waitUIStateWrites()

	file := mustReadUIStateFile(t, path)
	if file.LegacyLastSwarm != nil {
		t.Fatalf("旧键 last_hydra 不应再写回, got %+v", file.LegacyLastSwarm)
	}
	if file.LastSwarm == nil || file.LastSwarm.Enabled == nil || !*file.LastSwarm.Enabled {
		t.Fatalf("last_swarm.enabled 应保留旧值, got %+v", file.LastSwarm)
	}
	if file.LastSwarm.Bees == nil || file.LastSwarm.Bees.Provider != "claude" || file.LastSwarm.Bees.Model != "sonnet" {
		t.Fatalf("last_swarm.heads 应保留旧值, got %+v", file.LastSwarm.Bees)
	}
	if file.LastModel != wantModel {
		t.Fatalf("last_model 应为新保存的 %+v, got %+v", wantModel, file.LastModel)
	}
	if file.SidebarWidth != 300 {
		t.Fatalf("无关字段 sidebar_width 应保留 300, got %v", file.SidebarWidth)
	}
}
