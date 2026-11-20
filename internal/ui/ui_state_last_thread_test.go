package mcodeui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mcode/internal/testenv"
)

// TestResolveStartupThread 验证启动选中：上次会话优先，失效时回到最新活跃会话。
func TestResolveStartupThread(t *testing.T) {
	now := time.Now()
	threads := SortThreads([]Thread{
		{ID: "latest", ProjectID: "p1", UpdatedAt: now},
		{ID: "older", ProjectID: "p1", UpdatedAt: now.Add(-time.Hour)},
		{ID: "dormant", ProjectID: "p1", UpdatedAt: now.Add(-2 * time.Hour), Dormant: true},
	})
	tests := []struct {
		name string
		last string
		want string
	}{
		{"回到上次会话", "older", "older"},
		{"没有记录选最新", "", "latest"},
		{"休眠的旧会话不抢回页面", "dormant", "latest"},
		{"已消失的旧会话回落到最新", "gone", "latest"},
	}
	for _, tc := range tests {
		if got := resolveStartupThread(threads, tc.last); got != tc.want {
			t.Errorf("%s: resolveStartupThread(last=%q) = %q, want %q", tc.name, tc.last, got, tc.want)
		}
	}
}

// TestStartupRestoresLastThread 验证重启后回到上次退出的会话，而不是最新活跃会话。
func TestStartupRestoresLastThread(t *testing.T) {
	t.Setenv("MCODE_DATA", settingsTestDir(t))
	testenv.TempHome(t)
	now := time.Now()
	fake := NewFakeClient()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "Proj1"}},
		map[string][]Thread{"p1": {
			{ID: "latest", ProjectID: "p1", Title: "最新会话", UpdatedAt: now},
			{ID: "older", ProjectID: "p1", Title: "上次会话", UpdatedAt: now.Add(-time.Hour)},
		}},
	)
	seedJSON, err := json.Marshal(uiStateFile{SidebarWidth: SidebarDefaultWidth, LastThreadID: "older"})
	if err != nil {
		t.Fatal(err)
	}
	uiStatePathOnDisk := filepath.Join(os.Getenv("MCODE_DATA"), uiStateFileName)
	if err := os.WriteFile(uiStatePathOnDisk, seedJSON, 0o644); err != nil {
		t.Fatal(err)
	}

	state := newPersistentChatState(fake)
	state.loadProjects()
	waitSelectedThread(t, state, "older")
	waitUIStateWrites()
	if got := loadLastThread(); got != "older" {
		t.Fatalf("启动恢复不改写 last_thread，got %q", got)
	}
}

// TestLastThreadFollowsSelection 验证切换会话与打开新会话首页都会落到 ui-state.json。
func TestLastThreadFollowsSelection(t *testing.T) {
	t.Setenv("MCODE_DATA", settingsTestDir(t))
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "Proj1"}},
		map[string][]Thread{"p1": {
			{ID: "t1", ProjectID: "p1", Title: "会话 1", UpdatedAt: time.Now()},
			{ID: "t2", ProjectID: "p1", Title: "会话 2", UpdatedAt: time.Now().Add(-time.Hour)},
		}},
	)
	state := newPersistentChatState(fake)
	state.loadProjects()
	waitSelectedThread(t, state, "t1")
	state.selectThread("t2")
	waitSelectedThread(t, state, "t2")
	// 选择先落到内存，写盘由后台写者完成，轮询直到落盘。
	if got := waitLastThreadOnDisk("t2"); got != "t2" {
		t.Fatalf("切换会话后应记录 last_thread=t2，got %q", got)
	}

	state.createProvisionalThread("p1", false)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && fake.LastCreatedProjectID() == "" {
		time.Sleep(10 * time.Millisecond)
	}
	if got := waitLastThreadOnDisk(""); got != "" {
		t.Fatalf("打开新会话首页应清掉 last_thread，got %q", got)
	}
}

// newPersistentChatState 返回开启了界面状态持久化的聊天状态，并按主窗口启动流程恢复界面状态。
func newPersistentChatState(client Client) *chatState {
	state := newChatState(client)
	WithUIStatePersistence()(state)
	applyUIState(state, loadUIState())
	applyStartupThread(state)
	return state
}

func waitSelectedThread(t *testing.T, state *chatState, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if state.chatSelectedThreadID() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected selectedThreadID %q, got %q", want, state.chatSelectedThreadID())
}

// waitLastThreadOnDisk 轮询等待 ui-state.json 里的 last_thread 变成 want 并返回最终值。
func waitLastThreadOnDisk(want string) string {
	deadline := time.Now().Add(2 * time.Second)
	got := loadLastThread()
	for time.Now().Before(deadline) && got != want {
		time.Sleep(5 * time.Millisecond)
		got = loadLastThread()
	}
	return got
}

// TestLastThreadSurvivesStartupWrite 验证启动时选中会话写盘、随后的恢复回写都不会把记录冲掉。
// 这里的恢复回写发生在写盘取完快照之后，和真实启动里「列表加载选中」与「侧栏写盘」的交错一致。
func TestLastThreadSurvivesStartupWrite(t *testing.T) {
	t.Setenv("MCODE_DATA", settingsTestDir(t))
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "Proj1"}},
		map[string][]Thread{"p1": {{ID: "t1", ProjectID: "p1", Title: "会话 1", UpdatedAt: time.Now()}}},
	)
	state := newPersistentChatState(fake)
	t.Cleanup(func() { uiStateBeforeWriteHook = nil })
	var once sync.Once
	uiStateBeforeWriteHook = func() {
		once.Do(func() { rememberSelectedThread("t1") })
	}

	state.loadProjects()
	waitSelectedThread(t, state, "t1")
	if got := waitLastThreadOnDisk("t1"); got != "t1" {
		t.Fatalf("写盘期间记住的会话应被后续写者写出，got %q", got)
	}
}
