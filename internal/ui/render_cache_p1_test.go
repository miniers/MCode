package mcodeui

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"mcode/internal/testenv"
)

func TestRenderCacheP1AttachNoSyncReadDirAndHitbox(t *testing.T) {
	testenv.TempHome(t)
	clearAttachmentDirCache()
	t.Cleanup(clearAttachmentDirCache)

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}

	var readDirCalls atomic.Int64
	gate := make(chan struct{})
	oldReadDir := attachmentReadDir
	attachmentReadDir = func(name string) ([]os.DirEntry, error) {
		readDirCalls.Add(1)
		<-gate
		return oldReadDir(name)
	}
	t.Cleanup(func() {
		attachmentReadDir = oldReadDir
	})

	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)

	// 首次冷缓存调用不能阻塞在同步 os.ReadDir 上，应立即返回并仅在后台扫描。
	res, fresh, done := getCachedAttachmentDir(s, tmpDir)
	if fresh || len(res.files) != 0 || done == nil {
		t.Fatalf("expected cold miss with async scan channel, got fresh=%v files=%v done=%v", fresh, res.files, done)
	}
	close(gate)

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for async directory scan")
	}

	if got := readDirCalls.Load(); got != 1 {
		t.Fatalf("expected 1 os.ReadDir call after initial scan, got %d", got)
	}

	// TTL 内再次读取与 addAttachmentFiles 均命中缓存，不再重复扫盘。
	res2, fresh2, done2 := getCachedAttachmentDir(s, tmpDir)
	if !fresh2 || done2 != nil || len(res2.files) != 2 {
		t.Fatalf("expected cache hit with 2 sorted files, got fresh=%v done=%v files=%v", fresh2, done2, res2.files)
	}
	if filepath.Base(res2.files[0]) != "a.txt" || filepath.Base(res2.files[1]) != "b.txt" {
		t.Fatalf("unexpected sorted file order: %v", res2.files)
	}
	s.addAttachmentFiles([]string{tmpDir})
	waitAttachmentFiles(t, s, 2, "")
	if got := readDirCalls.Load(); got != 1 {
		t.Fatalf("cache hit should not re-scan directory, got %d os.ReadDir calls", got)
	}

	// 模拟 TTL 过期后，允许后台重新扫盘刷新缓存。
	attachmentDirCacheMu.Lock()
	entry := attachmentDirCache[tmpDir]
	entry.lastScan = time.Now().Add(-attachmentDirCacheTTL - time.Second)
	attachmentDirCache[tmpDir] = entry
	attachmentDirCacheMu.Unlock()

	_, fresh3, done3 := getCachedAttachmentDir(s, tmpDir)
	if fresh3 || done3 == nil {
		t.Fatalf("expected expired cache entry to trigger background refresh, got fresh=%v done=%v", fresh3, done3)
	}
	select {
	case <-done3:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for expired directory refresh")
	}
	if got := readDirCalls.Load(); got != 2 {
		t.Fatalf("expected 2 os.ReadDir calls after TTL expiry, got %d", got)
	}

	// 验证附件删除与重试按钮热区尺寸均升级到 ControlSM 及以上（inline 与 overlay 两种形态）。
	s.mu.Lock()
	s.attachments = []composerAttachment{
		{
			LocalID:   "fail-file",
			Name:      "fail.txt",
			MimeType:  "text/plain",
			Size:      12,
			Err:       "上传失败",
			RetryData: []byte("hello"),
		},
		{
			LocalID:   "fail-img",
			Name:      "fail.png",
			MimeType:  "image/png",
			Size:      12,
			Err:       "上传失败",
			RetryData: []byte{0x89, 'P', 'N', 'G'},
		},
	}
	s.mu.Unlock()

	tt := composerTester(s)
	for _, label := range []string{
		"删除附件 fail.txt",
		"重试上传 fail.txt",
		"删除附件 fail.png",
		"重试上传 fail.png",
	} {
		waitFor(t, tt, func() bool {
			_, ok := tt.Find(label)
			return ok
		})
		rect, ok := tt.Find(label)
		if !ok {
			t.Fatalf("button %q not found", label)
		}
		if rect.W < ControlSM || rect.H < ControlSM {
			t.Fatalf("button %q hitbox (%vx%v) smaller than ControlSM (%v)", label, rect.W, rect.H, ControlSM)
		}
	}
}

func TestRenderCacheP1SortCacheInvalidatesOnChange(t *testing.T) {
	now := time.Now()

	// 1. 归档分组排序缓存
	clearArchiveGroupCache()
	t.Cleanup(clearArchiveGroupCache)
	projects := map[string]string{"p1": "Alpha", "p2": "Beta"}
	threads := []Thread{
		{ID: "th-1", ProjectID: "p1", Title: "旧会话", UpdatedAt: now.Add(-2 * time.Hour)},
		{ID: "th-2", ProjectID: "p1", Title: "新会话", UpdatedAt: now.Add(-time.Hour)},
	}
	g1 := archiveGroupByProject(threads, projects)
	g2 := archiveGroupByProject(threads, projects)
	archiveGroupCacheMu.Lock()
	archSorts := archiveGroupCacheSortCount
	archiveGroupCacheMu.Unlock()
	if archSorts != 1 || len(g1) != 1 || len(g2) != 1 || &g1[0] != &g2[0] {
		t.Fatalf("archive sort cache did not reuse output: sorts=%d", archSorts)
	}
	if g2[0].threads[0].ID != "th-2" {
		t.Fatalf("unexpected initial archive order: %+v", g2[0].threads)
	}
	threads[0].UpdatedAt = now // 修改旧会话时间戳使其跃居首位
	g3 := archiveGroupByProject(threads, projects)
	archiveGroupCacheMu.Lock()
	archSorts = archiveGroupCacheSortCount
	archiveGroupCacheMu.Unlock()
	if archSorts != 2 || g3[0].threads[0].ID != "th-1" {
		t.Fatalf("archive sort cache did not invalidate on update: sorts=%d first=%s", archSorts, g3[0].threads[0].ID)
	}
	threads = append(threads, Thread{ID: "th-3", ProjectID: "p2", Title: "新增组", UpdatedAt: now})
	g4 := archiveGroupByProject(threads, projects)
	if len(g4) != 2 || g4[1].name != "Beta" {
		t.Fatalf("archive sort cache did not invalidate on append: %+v", g4)
	}

	// 2. 自动化列表排序缓存
	clearAutomationFilterCache()
	t.Cleanup(clearAutomationFilterCache)
	autos := []Automation{
		{ID: "a1", Name: "B", Enabled: true, NextRunAt: now.Add(2 * time.Hour)},
		{ID: "a2", Name: "A", Enabled: true, NextRunAt: now.Add(time.Hour)},
	}
	f1 := filterAutomations(autos, "")
	f2 := filterAutomations(autos, "")
	automationFilterCacheMu.Lock()
	autoSorts := automationFilterCacheSortCount
	automationFilterCacheMu.Unlock()
	if autoSorts != 1 || len(f1) != 2 || len(f2) != 2 || &f1[0] != &f2[0] || f2[0].ID != "a2" {
		t.Fatalf("automation sort cache did not reuse output: sorts=%d first=%s", autoSorts, f2[0].ID)
	}
	autos[0].NextRunAt = now.Add(10 * time.Minute)
	f3 := filterAutomations(autos, "")
	automationFilterCacheMu.Lock()
	autoSorts = automationFilterCacheSortCount
	automationFilterCacheMu.Unlock()
	if autoSorts != 2 || f3[0].ID != "a1" {
		t.Fatalf("automation sort cache did not invalidate on update: sorts=%d first=%s", autoSorts, f3[0].ID)
	}

	// 3. 看板列表分组排序缓存
	clearKanbanListGroupsCache()
	t.Cleanup(clearKanbanListGroupsCache)
	tasks := []KanbanTask{
		{ID: "k1", Title: "旧任务", Status: "todo", UpdatedAt: now.Add(-2 * time.Hour)},
		{ID: "k2", Title: "新任务", Status: "todo", UpdatedAt: now.Add(-time.Hour)},
	}
	k1 := kanbanListGroups(tasks)
	k2 := kanbanListGroups(tasks)
	kanbanListGroupsCacheMu.Lock()
	kanbanSorts := kanbanListGroupsCacheSortCount
	kanbanListGroupsCacheMu.Unlock()
	if kanbanSorts != 1 || len(k1) != 1 || len(k2) != 1 || &k1[0] != &k2[0] || k2[0].tasks[0].ID != "k2" {
		t.Fatalf("kanban sort cache did not reuse output: sorts=%d", kanbanSorts)
	}
	tasks[0].UpdatedAt = now
	k3 := kanbanListGroups(tasks)
	kanbanListGroupsCacheMu.Lock()
	kanbanSorts = kanbanListGroupsCacheSortCount
	kanbanListGroupsCacheMu.Unlock()
	if kanbanSorts != 2 || k3[0].tasks[0].ID != "k1" {
		t.Fatalf("kanban sort cache did not invalidate on update: sorts=%d first=%s", kanbanSorts, k3[0].tasks[0].ID)
	}
	tasks = append(tasks, KanbanTask{ID: "k3", Title: "进行中", Status: "in_progress", UpdatedAt: now})
	k4 := kanbanListGroups(tasks)
	if len(k4) != 2 || k4[1].status != "in_progress" {
		t.Fatalf("kanban sort cache did not invalidate on append: %+v", k4)
	}

	// 4. 自动化运行历史排序缓存
	clearAutomationRunsCache()
	t.Cleanup(clearAutomationRunsCache)
	runs := []AutomationRun{
		{ID: "r1", Status: "succeeded", StartedAt: now.Add(-time.Hour)},
		{ID: "r2", Status: "running", StartedAt: now},
	}
	r1 := sortedAutomationRuns("auto-1", runs)
	r2 := sortedAutomationRuns("auto-1", runs)
	automationRunsCacheMu.Lock()
	runSorts := automationRunsCacheSortCount
	automationRunsCacheMu.Unlock()
	if runSorts != 1 || len(r1) != 2 || len(r2) != 2 || &r1[0] != &r2[0] || r2[0].ID != "r2" {
		t.Fatalf("automation runs sort cache did not reuse output: sorts=%d", runSorts)
	}
	runs[0].StartedAt = now.Add(time.Minute)
	runs[0].Status = "failed"
	r3 := sortedAutomationRuns("auto-1", runs)
	automationRunsCacheMu.Lock()
	runSorts = automationRunsCacheSortCount
	automationRunsCacheMu.Unlock()
	if runSorts != 2 || r3[0].ID != "r1" || r3[0].Status != "failed" {
		t.Fatalf("automation runs sort cache did not invalidate on update: sorts=%d first=%+v", runSorts, r3[0])
	}

	// 5. 模型选择分组排序缓存
	clearModelPickerCache()
	t.Cleanup(clearModelPickerCache)
	models := []modelPickerItem{
		{Key: "m1", ID: "gpt-4o", Name: "GPT-4o"},
		{Key: "m2", ID: "claude-3-7-sonnet", Name: "Claude 3.7 Sonnet"},
	}
	m1 := getSortedModelPickerSections("pop-1", models, "")
	m2 := getSortedModelPickerSections("pop-1", models, "")
	modelPickerCacheMu.Lock()
	modelSorts := modelPickerCacheSortCount
	modelPickerCacheMu.Unlock()
	if modelSorts != 1 || len(m1.sections) != 1 || len(m2.sections) != 1 || &m1.sections[0] != &m2.sections[0] {
		t.Fatalf("model picker sort cache did not reuse output: sorts=%d", modelSorts)
	}
	models[0].Checked = true
	m3 := getSortedModelPickerSections("pop-1", models, "")
	modelPickerCacheMu.Lock()
	modelSorts = modelPickerCacheSortCount
	modelPickerCacheMu.Unlock()
	if modelSorts != 2 || len(m3.sections) != 1 {
		t.Fatalf("model picker sort cache did not invalidate on item update: sorts=%d", modelSorts)
	}
}
