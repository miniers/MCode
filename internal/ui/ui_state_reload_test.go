package mcodeui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// resetLastSelectionCacheForReload 把“最近选择”的内存缓存与文件修改时间记录恢复成未加载状态，
// 并把数据目录指向 dataDir，便于断言磁盘上的改动是否被读到。
func resetLastSelectionCacheForReload(t *testing.T, dataDir string) string {
	t.Helper()
	t.Setenv("MCODE_DATA", dataDir)
	t.Cleanup(waitUIStateWrites)
	lastModelSelectionMu.Lock()
	lastModelSelectionPath = ""
	lastModelSelectionLoaded = false
	lastModelSelectionCache = nil
	lastSwarmSelectionCache = nil
	lastModelSelectionModTime = time.Time{}
	lastModelSelectionCheckedAt = time.Time{}
	lastSelectionWritesPending = 0
	lastModelSelectionMu.Unlock()
	return filepath.Join(dataDir, uiStateFileName)
}

// writeLastSelectionFile 直接改写 ui-state.json（模拟另一个进程的写入），并把修改时间设为 modTime。
func writeLastSelectionFile(t *testing.T, path string, sel ModelSelection, modTime time.Time) {
	t.Helper()
	data, err := json.MarshalIndent(uiStateFile{LastModel: sel}, "", "  ")
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

// waitLastSelectionWritesDone 等待后台写盘结束（待写计数归零）。
func waitLastSelectionWritesDone(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		lastModelSelectionMu.Lock()
		pending := lastSelectionWritesPending
		lastModelSelectionMu.Unlock()
		if pending == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待写盘完成超时")
}

// TestLastModelSelectionReloadsAfterExternalChange 验证 ui-state.json 被外部改写后，
// 下一次读取会重新读盘拿到新值（而不是一直用进程内缓存）。
func TestLastModelSelectionReloadsAfterExternalChange(t *testing.T) {
	dataDir := settingsTestDir(t)
	path := resetLastSelectionCacheForReload(t, dataDir)

	firstModTime := time.Now().Add(-time.Hour)
	writeLastSelectionFile(t, path, ModelSelection{Provider: "claude", Model: "sonnet"}, firstModTime)

	sel, ok := loadLastModelSelection()
	if !ok || sel.Provider != "claude" || sel.Model != "sonnet" {
		t.Fatalf("首次加载应读到 claude/sonnet, got %+v ok=%v", sel, ok)
	}

	// 另一个进程改写文件并改变修改时间。
	writeLastSelectionFile(t, path, ModelSelection{Provider: "codex", Model: "gpt-5.5"}, firstModTime.Add(time.Minute))

	sel, ok = loadLastModelSelection()
	if !ok || sel.Provider != "codex" || sel.Model != "gpt-5.5" {
		t.Fatalf("文件被外部改写后应读到 codex/gpt-5.5, got %+v ok=%v", sel, ok)
	}
}

// TestSaveLastModelSelectionSurvivesReload 验证本进程写盘不会被再次读盘覆盖：
// 刚保存的值在写盘前和写盘后都应读到，磁盘重读不会退回旧内容。
func TestSaveLastModelSelectionSurvivesReload(t *testing.T) {
	dataDir := settingsTestDir(t)
	path := resetLastSelectionCacheForReload(t, dataDir)

	writeLastSelectionFile(t, path, ModelSelection{Provider: "claude", Model: "sonnet"}, time.Now().Add(-time.Hour))
	if sel, ok := loadLastModelSelection(); !ok || sel.Provider != "claude" {
		t.Fatalf("首次加载应读到 claude, got %+v ok=%v", sel, ok)
	}

	want := ModelSelection{Provider: "pi", Model: "default", Effort: "high", Fast: true}
	saveLastModelSelection(want)

	// 写盘尚未完成时读取：待写计数大于零，不会读回磁盘上的旧值。
	if sel, ok := loadLastModelSelection(); !ok || sel != want {
		t.Fatalf("写盘完成前应读到刚保存的值 %+v, got %+v ok=%v", want, sel, ok)
	}

	waitLastSelectionWritesDone(t)

	// 写盘完成后修改时间已刷新，再次读取不会触发重载，也不会回到旧值。
	if sel, ok := loadLastModelSelection(); !ok || sel != want {
		t.Fatalf("写盘完成后应仍读到最后保存的值 %+v, got %+v ok=%v", want, sel, ok)
	}

	file, ok := loadUIStateFile()
	if !ok || file.LastModel != want {
		t.Fatalf("磁盘上应写入 %+v, got %+v ok=%v", want, file.LastModel, ok)
	}
}

// setLastSelectionStatInterval 在测试期间把 Stat ui-state.json 的最小间隔设为 interval，
// 并在用例结束后恢复为 0（TestMain 里设定的默认值），避免影响其它用例。
func setLastSelectionStatInterval(t *testing.T, interval time.Duration) {
	t.Helper()
	lastModelSelectionMu.Lock()
	lastSelectionStatInterval = interval
	lastModelSelectionMu.Unlock()
	t.Cleanup(func() {
		lastModelSelectionMu.Lock()
		lastSelectionStatInterval = 0
		lastModelSelectionMu.Unlock()
	})
}

// TestLastSelectionStatThrottled 验证间隔内不重复 Stat：加载后外部改写文件，间隔未过时
// 仍返回旧值且不触发重新加载。
func TestLastSelectionStatThrottled(t *testing.T) {
	dataDir := settingsTestDir(t)
	path := resetLastSelectionCacheForReload(t, dataDir)
	setLastSelectionStatInterval(t, time.Hour)

	firstModTime := time.Now().Add(-time.Hour)
	writeLastSelectionFile(t, path, ModelSelection{Provider: "claude", Model: "sonnet"}, firstModTime)
	if sel, ok := loadLastModelSelection(); !ok || sel.Provider != "claude" {
		t.Fatalf("首次加载应读到 claude, got %+v ok=%v", sel, ok)
	}

	lastModelSelectionMu.Lock()
	checkedAt := lastModelSelectionCheckedAt
	lastModelSelectionMu.Unlock()

	secondModTime := firstModTime.Add(time.Minute)
	writeLastSelectionFile(t, path, ModelSelection{Provider: "codex", Model: "gpt-5.5"}, secondModTime)

	for i := 0; i < 5; i++ {
		sel, ok := loadLastModelSelection()
		if !ok || sel.Provider != "claude" || sel.Model != "sonnet" {
			t.Fatalf("第 %d 次读取仍在限频间隔内，应读到缓存的 claude/sonnet, got %+v ok=%v", i, sel, ok)
		}
	}

	lastModelSelectionMu.Lock()
	checkedAfter := lastModelSelectionCheckedAt
	modTimeAfter := lastModelSelectionModTime
	lastModelSelectionMu.Unlock()
	if !checkedAfter.Equal(checkedAt) {
		t.Fatalf("间隔内不应再 Stat（Stat 时刻应保持 %v, got %v）", checkedAt, checkedAfter)
	}
	if modTimeAfter.Equal(secondModTime) {
		t.Fatalf("间隔内不应重新加载（修改时间记录应仍是旧值, got %v）", modTimeAfter)
	}
}

// TestLastSelectionStatIntervalElapsedReloads 验证间隔过后能发现外部改写：
// 把上次 Stat 时刻清零后再次读取，读到新值。
func TestLastSelectionStatIntervalElapsedReloads(t *testing.T) {
	dataDir := settingsTestDir(t)
	path := resetLastSelectionCacheForReload(t, dataDir)
	setLastSelectionStatInterval(t, time.Hour)

	firstModTime := time.Now().Add(-time.Hour)
	writeLastSelectionFile(t, path, ModelSelection{Provider: "claude", Model: "sonnet"}, firstModTime)
	if sel, ok := loadLastModelSelection(); !ok || sel.Provider != "claude" {
		t.Fatalf("首次加载应读到 claude, got %+v ok=%v", sel, ok)
	}

	writeLastSelectionFile(t, path, ModelSelection{Provider: "codex", Model: "gpt-5.5"}, firstModTime.Add(time.Minute))

	// 模拟间隔已过：清零上次 Stat 时刻。
	lastModelSelectionMu.Lock()
	lastModelSelectionCheckedAt = time.Time{}
	lastModelSelectionMu.Unlock()

	sel, ok := loadLastModelSelection()
	if !ok || sel.Provider != "codex" || sel.Model != "gpt-5.5" {
		t.Fatalf("间隔过后应读到 codex/gpt-5.5, got %+v ok=%v", sel, ok)
	}
}

// TestLastSelectionStatSwitchDataDirReloads 验证数据目录变化不受限频影响，立即加载新路径内容。
func TestLastSelectionStatSwitchDataDirReloads(t *testing.T) {
	dataDir := settingsTestDir(t)
	path := resetLastSelectionCacheForReload(t, dataDir)
	setLastSelectionStatInterval(t, time.Hour)

	modTime := time.Now().Add(-time.Hour)
	writeLastSelectionFile(t, path, ModelSelection{Provider: "claude", Model: "sonnet"}, modTime)
	if sel, ok := loadLastModelSelection(); !ok || sel.Provider != "claude" {
		t.Fatalf("首次加载应读到 claude, got %+v ok=%v", sel, ok)
	}

	otherDir := settingsTestDir(t)
	otherPath := filepath.Join(otherDir, uiStateFileName)
	writeLastSelectionFile(t, otherPath, ModelSelection{Provider: "codex", Model: "gpt-5.5"}, modTime.Add(time.Minute))

	// 切换数据目录：路径变化应立刻重新读盘。
	t.Setenv("MCODE_DATA", otherDir)
	sel, ok := loadLastModelSelection()
	if !ok || sel.Provider != "codex" || sel.Model != "gpt-5.5" {
		t.Fatalf("切换数据目录后应立即读到 codex/gpt-5.5, got %+v ok=%v", sel, ok)
	}
}

// TestLastSelectionStatLocalWriteVisible 验证本进程写入后立刻读取能读到刚写入的值，不受限频影响。
func TestLastSelectionStatLocalWriteVisible(t *testing.T) {
	dataDir := settingsTestDir(t)
	path := resetLastSelectionCacheForReload(t, dataDir)
	setLastSelectionStatInterval(t, time.Hour)

	writeLastSelectionFile(t, path, ModelSelection{Provider: "claude", Model: "sonnet"}, time.Now().Add(-time.Hour))
	if sel, ok := loadLastModelSelection(); !ok || sel.Provider != "claude" {
		t.Fatalf("首次加载应读到 claude, got %+v ok=%v", sel, ok)
	}

	want := ModelSelection{Provider: "pi", Model: "default", Effort: "high", Fast: true}
	saveLastModelSelection(want)
	if sel, ok := loadLastModelSelection(); !ok || sel != want {
		t.Fatalf("写入后应立刻读到 %+v, got %+v ok=%v", want, sel, ok)
	}

	waitLastSelectionWritesDone(t)
	if sel, ok := loadLastModelSelection(); !ok || sel != want {
		t.Fatalf("写盘完成后应仍读到 %+v, got %+v ok=%v", want, sel, ok)
	}
}
