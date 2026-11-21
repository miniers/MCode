package mcodeui

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestMemoryPendingToastOnlyOnNewArrival(t *testing.T) {
	fake := NewFakeClient()
	projectPath := "/tmp/memory-toast-proj"
	s := newChatState(fake)
	t.Cleanup(s.close)
	s.mu.Lock()
	s.projects = []Project{{ID: "p1", Path: projectPath}}
	s.mu.Unlock()

	addProposal := func(i int) {
		fake.SetMemoryProposal(MemoryProposal{
			ID:          fmt.Sprintf("mp-%d", i),
			ProjectPath: projectPath,
			Title:       fmt.Sprintf("提议 %d", i),
			Content:     "内容",
			UnitType:    "fact",
			Status:      "pending",
		})
	}
	removeProposal := func(i int) {
		_ = fake.RejectMemoryProposal(fmt.Sprintf("mp-%d", i))
	}
	readToast := func() string {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.toastMessage
	}
	clearToast := func() {
		s.mu.Lock()
		s.toastMessage = ""
		s.mu.Unlock()
	}
	waitPendingCount := func(expected int) {
		t.Helper()
		st := getMemoryPanelState(s)
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			memoryPanelMu.Lock()
			done := !st.pendingFetching && st.pendingBaselineSet && st.pendingCount == expected
			memoryPanelMu.Unlock()
			if done {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		memoryPanelMu.Lock()
		got, fetching, baseline := st.pendingCount, st.pendingFetching, st.pendingBaselineSet
		memoryPanelMu.Unlock()
		t.Fatalf("等待 pendingCount=%d 超时: got=%d fetching=%v baseline=%v", expected, got, fetching, baseline)
	}

	// 1. 首次拉取返回 3 条旧提案，仅建立基线，不弹 Toast。
	for i := 1; i <= 3; i++ {
		addProposal(i)
	}
	s.refreshPendingMemoryCount(true)
	waitPendingCount(3)
	if got := readToast(); got != "" {
		t.Fatalf("首次基线拉取不应弹 Toast，实际=%q", got)
	}

	// 2. 再次拉取仍为 3 条，不弹 Toast。
	s.refreshPendingMemoryCount(true)
	waitPendingCount(3)
	if got := readToast(); got != "" {
		t.Fatalf("数量未变化时不应弹 Toast，实际=%q", got)
	}

	// 3. 面板关闭时拉取变为 5 条，触发一次 Toast 且包含 "收到 2 条新记忆提议"。
	addProposal(4)
	addProposal(5)
	s.refreshPendingMemoryCount(true)
	waitPendingCount(5)
	deadline := time.Now().Add(2 * time.Second)
	for readToast() == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := readToast(); !strings.Contains(got, "收到 2 条新记忆提议") {
		t.Fatalf("新增 2 条提案应触发包含「收到 2 条新记忆提议」的 Toast，实际=%q", got)
	}
	clearToast()

	// 4. 再次拉取仍为 5 条，不重复弹 Toast。
	s.refreshPendingMemoryCount(true)
	waitPendingCount(5)
	if got := readToast(); got != "" {
		t.Fatalf("相同数量再次刷新不应重复弹 Toast，实际=%q", got)
	}

	// 5. 面板打开时拉取变为 6 条，不弹 Toast。
	st := getMemoryPanelState(s)
	memoryPanelMu.Lock()
	st.open = true
	memoryPanelMu.Unlock()
	addProposal(6)
	s.refreshPendingMemoryCount(true)
	waitPendingCount(6)
	if got := readToast(); got != "" {
		t.Fatalf("面板打开时新增提案不应弹 Toast，实际=%q", got)
	}

	// 6. 数量回落到 4 条（即使面板已关闭），不弹 Toast。
	memoryPanelMu.Lock()
	st.open = false
	memoryPanelMu.Unlock()
	removeProposal(5)
	removeProposal(6)
	s.refreshPendingMemoryCount(true)
	waitPendingCount(4)
	if got := readToast(); got != "" {
		t.Fatalf("提案数量回落时不应弹 Toast，实际=%q", got)
	}
}
