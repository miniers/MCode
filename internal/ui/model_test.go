package mcodeui

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"mcode/internal/testenv"
)

func TestUpsertMessages(t *testing.T) {
	initial := []Message{
		{ID: "m1", Role: "user", Text: "Hello", TurnIndex: 1},
		{ID: "m2", Role: "assistant", Text: "Hi there", TurnIndex: 2},
		{ID: "m3", Role: "user", Text: "How are you?", TurnIndex: 3},
	}

	// 测试：替换 m2，追加 m4，删除 m1
	updates := []Message{
		{ID: "m2", Role: "assistant", Text: "Hi there updated!", TurnIndex: 2},
		{ID: "m4", Role: "assistant", Text: "I am doing well", TurnIndex: 4},
	}
	removed := []string{"m1"}

	got := UpsertMessages(initial, updates, removed)

	want := []Message{
		{ID: "m2", Role: "assistant", Text: "Hi there updated!", TurnIndex: 2},
		{ID: "m3", Role: "user", Text: "How are you?", TurnIndex: 3},
		{ID: "m4", Role: "assistant", Text: "I am doing well", TurnIndex: 4},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("UpsertMessages mismatch:\ngot  %+v\nwant %+v", got, want)
	}
}

func TestUpsertMessagesSortTurnIndex(t *testing.T) {
	// 测试无序的 updates 能按 TurnIndex 稳定升序排列
	initial := []Message{
		{ID: "m3", TurnIndex: 3, Text: "third"},
	}
	updates := []Message{
		{ID: "m1", TurnIndex: 1, Text: "first"},
		{ID: "m2", TurnIndex: 2, Text: "second"},
	}

	got := UpsertMessages(initial, updates, nil)
	if len(got) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(got))
	}
	if got[0].ID != "m1" || got[1].ID != "m2" || got[2].ID != "m3" {
		t.Fatalf("unexpected order: %+v", got)
	}
}

func TestSortThreads(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 1, 3, 10, 0, 0, 0, time.UTC)

	threads := []Thread{
		{ID: "th1", UpdatedAt: t1},
		{ID: "th3", UpdatedAt: t3},
		{ID: "th2", UpdatedAt: t2},
	}

	sorted := SortThreads(threads)

	if len(sorted) != 3 {
		t.Fatalf("expected 3 threads, got %d", len(sorted))
	}
	if sorted[0].ID != "th3" || sorted[1].ID != "th2" || sorted[2].ID != "th1" {
		t.Fatalf("threads not sorted by UpdatedAt desc: %+v", sorted)
	}
}

// TestSortThreadsByActivity 验证置顶优先、LastUserMessageAt 优先于 UpdatedAt、零值回退。
func TestSortThreadsByActivity(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 1, 3, 10, 0, 0, 0, time.UTC)

	cases := []struct {
		name    string
		threads []Thread
		want    []string
	}{
		{
			name: "置顶优先",
			threads: []Thread{
				{ID: "old-pinned", Pinned: true, UpdatedAt: t1},
				{ID: "new", UpdatedAt: t3, LastUserMessageAt: t3},
			},
			want: []string{"old-pinned", "new"},
		},
		{
			name: "LastUserMessageAt 优先于 UpdatedAt",
			threads: []Thread{
				// b 的助手回复更新，但用户最后发消息的是 a，a 应排前面。
				{ID: "a", UpdatedAt: t1, LastUserMessageAt: t3},
				{ID: "b", UpdatedAt: t2, LastUserMessageAt: t2},
			},
			want: []string{"a", "b"},
		},
		{
			name: "零值回退到 UpdatedAt",
			threads: []Thread{
				{ID: "a", UpdatedAt: t1},
				{ID: "b", UpdatedAt: t3},
				{ID: "c", UpdatedAt: t2, LastUserMessageAt: t1},
			},
			want: []string{"b", "a", "c"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SortThreads(tc.threads)
			if len(got) != len(tc.want) {
				t.Fatalf("会话数不匹配: got %d want %d", len(got), len(tc.want))
			}
			for i, id := range tc.want {
				if got[i].ID != id {
					t.Fatalf("第 %d 位应为 %s，实际 %+v（期望顺序 %v）", i, id, got, tc.want)
				}
			}
		})
	}
}

// TestSortProjectsByActivity 验证项目按内部会话最大活跃时间倒序、空项目排最后并保持原顺序。
func TestSortProjectsByActivity(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 1, 3, 10, 0, 0, 0, time.UTC)

	cases := []struct {
		name     string
		projects []Project
		threads  map[string][]Thread
		want     []string
	}{
		{
			name:     "按项目内会话最大活跃时间倒序",
			projects: []Project{{ID: "p1"}, {ID: "p2"}},
			threads: map[string][]Thread{
				"p1": {{ID: "t1", LastUserMessageAt: t1}},
				"p2": {{ID: "t2", LastUserMessageAt: t2}, {ID: "t3", LastUserMessageAt: t3}},
			},
			want: []string{"p2", "p1"},
		},
		{
			name:     "空项目排在后面且保持原顺序",
			projects: []Project{{ID: "p3"}, {ID: "p1"}, {ID: "p4"}, {ID: "p2"}},
			threads: map[string][]Thread{
				"p1": {{ID: "t1", LastUserMessageAt: t1}},
				"p2": {{ID: "t2", LastUserMessageAt: t3}},
			},
			want: []string{"p2", "p1", "p3", "p4"},
		},
		{
			name:     "会话无用户消息时回退到 UpdatedAt",
			projects: []Project{{ID: "p1"}, {ID: "p2"}},
			threads: map[string][]Thread{
				"p1": {{ID: "t1", UpdatedAt: t1}},
				"p2": {{ID: "t2", LastUserMessageAt: t2}},
			},
			want: []string{"p2", "p1"},
		},
		{
			name:     "threadsByProj 为 nil 时保持原顺序",
			projects: []Project{{ID: "p1"}, {ID: "p2"}},
			threads:  nil,
			want:     []string{"p1", "p2"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SortProjectsByActivity(tc.projects, tc.threads)
			if len(got) != len(tc.want) {
				t.Fatalf("项目数不匹配: got %d want %d", len(got), len(tc.want))
			}
			for i, id := range tc.want {
				if got[i].ID != id {
					t.Fatalf("第 %d 位应为 %s，实际 %+v（期望顺序 %v）", i, id, got, tc.want)
				}
			}
		})
	}
}

func TestModel_OnWatchUpdateProjectRouting(t *testing.T) {
	fake := NewFakeClient()
	state := newChatState(fake)
	state.projects = []Project{
		{ID: "p1", Name: "Proj1"},
		{ID: "p2", Name: "Proj2"},
	}
	t1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	state.threadsByProj["p1"] = []Thread{{ID: "th1", ProjectID: "p1", Title: "Old1", UpdatedAt: t1}}
	state.threadsByProj["p2"] = []Thread{{ID: "th2", ProjectID: "p2", Title: "Old2", UpdatedAt: t2}}
	state.threads = []Thread{
		{ID: "th2", ProjectID: "p2", Title: "Old2", UpdatedAt: t2},
		{ID: "th1", ProjectID: "p1", Title: "Old1", UpdatedAt: t1},
	}

	// 收到属于 p1 的更新
	t1Updated := time.Date(2026, 1, 3, 10, 0, 0, 0, time.UTC)
	state.onWatchUpdate(ThreadUpdate{
		ThreadID: "th1",
		Thread: Thread{
			ID:        "th1",
			ProjectID: "p1",
			Title:     "Updated1",
			UpdatedAt: t1Updated,
		},
	})

	state.mu.Lock()
	defer state.mu.Unlock()

	// 验证 p1 分组被更新
	if len(state.threadsByProj["p1"]) != 1 || state.threadsByProj["p1"][0].Title != "Updated1" {
		t.Fatalf("p1 group not updated: %+v", state.threadsByProj["p1"])
	}
	// 验证 p2 分组保持不变
	if len(state.threadsByProj["p2"]) != 1 || state.threadsByProj["p2"][0].Title != "Old2" {
		t.Fatalf("p2 group unexpectedly changed: %+v", state.threadsByProj["p2"])
	}
	// 验证全局 threads 排序重新调整（th1 变最新）
	if len(state.threads) != 2 || state.threads[0].ID != "th1" {
		t.Fatalf("global threads not sorted after update: %+v", state.threads)
	}
}

// TestModel_SendMarksRunningOptimistically 验证发送成功后界面立即把会话置为运行中（乐观置位），
// 并在后端给出权威摘要（turn 结束）后清除，覆盖“发送后没有回复中状态”的修复。
func TestModel_SendMarksRunningOptimistically(t *testing.T) {
	fake := NewFakeClient()
	state := newChatState(fake)
	t.Cleanup(state.close)

	now := time.Now()
	state.mu.Lock()
	state.threads = []Thread{{ID: "t1", ProjectID: "p1", Title: "会话", UpdatedAt: now}}
	state.threadsByProj["p1"] = []Thread{{ID: "t1", ProjectID: "p1", Title: "会话", UpdatedAt: now}}
	state.selectedThreadID = "t1"
	state.mu.Unlock()

	state.sendMessage("你好")

	deadline := time.Now().Add(2 * time.Second)
	optimistic := false
	for time.Now().Before(deadline) {
		state.mu.Lock()
		optimistic = state.optimisticRunning["t1"] && len(state.threads) == 1 && state.threads[0].IsRunning
		state.mu.Unlock()
		if optimistic {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !optimistic {
		t.Fatal("发送成功后应立即把会话乐观置为运行中")
	}

	// 后端推送 turn 结束（IsRunning=false）的权威摘要后，运行中状态与乐观标记都应清除。
	state.onWatchUpdate(ThreadUpdate{
		ThreadID: "t1",
		Thread:   Thread{ID: "t1", ProjectID: "p1", Title: "会话", UpdatedAt: time.Now()},
	})

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.threads[0].IsRunning {
		t.Fatalf("后端报告 turn 结束后不应再显示运行中: %+v", state.threads[0])
	}
	if state.optimisticRunning["t1"] {
		t.Fatal("后端给出权威摘要后应清除乐观运行中标记")
	}
}

func TestModel_InitialSelectLatestAcrossProjects(t *testing.T) {
	fake := NewFakeClient()
	now := time.Now()
	fake.SetProjectsAndThreads(
		[]Project{
			{ID: "p1", Name: "Proj1"},
			{ID: "p2", Name: "Proj2"},
		},
		map[string][]Thread{
			"p1": {{ID: "th-old", ProjectID: "p1", Title: "Old", UpdatedAt: now.Add(-10 * time.Minute)}},
			"p2": {{ID: "th-new", ProjectID: "p2", Title: "Newest", UpdatedAt: now}},
		},
	)

	state := newChatState(fake)
	state.loadProjects()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state.mu.Lock()
		sel := state.selectedThreadID
		state.mu.Unlock()
		if sel == "th-new" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	t.Fatalf("expected selectedThreadID to be 'th-new', got %q", state.selectedThreadID)
}

func TestModel_CreateThreadRouting(t *testing.T) {
	fake := NewFakeClient()
	fake.SetProjectsAndThreads(
		[]Project{
			{ID: "p1", Name: "Proj1"},
			{ID: "p2", Name: "Proj2"},
		},
		map[string][]Thread{
			"p1": {},
			"p2": {},
		},
	)
	state := newChatState(fake)
	state.projects = []Project{{ID: "p1"}, {ID: "p2"}}

	state.createThread("p2")
	// 待创建会话在首个非空输入后才真正创建，这里模拟输入触发。
	state.noteComposerDraft("", "你好")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fake.LastCreatedProjectID() == "p2" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected createThread called with 'p2', got %q", fake.LastCreatedProjectID())
}

func TestModel_ProvidersAndNewThreadProvider(t *testing.T) {
	fake := NewFakeClient()
	fake.SetProviders([]ProviderInfo{
		{ID: "prov-a", Name: "Provider A", Available: true},
		{ID: "prov-b", Name: "Provider B", Available: true},
	})
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "Proj1"}},
		map[string][]Thread{"p1": {}},
	)

	state := newChatState(fake)
	state.loadProjects()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state.mu.Lock()
		cur := state.newThreadProvider
		state.mu.Unlock()
		if cur == "prov-a" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	state.mu.Lock()
	if state.newThreadProvider != "prov-a" {
		state.mu.Unlock()
		t.Fatalf("expected default newThreadProvider to be 'prov-a', got %q", state.newThreadProvider)
	}
	state.mu.Unlock()

	// 1. 验证 providerDisplayName 映射
	if name := state.providerDisplayName("prov-a"); name != "Provider A" {
		t.Errorf("expected 'Provider A', got %q", name)
	}
	if name := state.providerDisplayName("prov-unknown"); name != "prov-unknown" {
		t.Errorf("expected 'prov-unknown', got %q", name)
	}
	if name := state.providerDisplayName(""); name != "" {
		t.Errorf("expected empty string for empty provider ID, got %q", name)
	}

	// 2. 切换 newThreadProvider 并调用 createThread
	state.mu.Lock()
	state.newThreadProvider = "prov-b"
	state.mu.Unlock()

	state.createThread("p1")
	state.noteComposerDraft("", "你好")

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fake.LastCreatedProvider() == "prov-b" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected createThread with 'prov-b', got %q", fake.LastCreatedProvider())
}

// sequenceProviderClient 让 Providers 依次返回不同快照，用于模拟 CLI 模型目录晚到。
type sequenceProviderClient struct {
	*FakeClient
	mu    sync.Mutex
	seq   [][]ProviderInfo
	calls int
}

func (c *sequenceProviderClient) Providers(ctx context.Context) ([]ProviderInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := c.calls
	c.calls++
	if i >= len(c.seq) {
		i = len(c.seq) - 1
	}
	return c.seq[i], nil
}

func (c *sequenceProviderClient) providerCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// 可用的供应商但模型目录为空（CLI 目录尚在后台探测）也算未就绪，
// 让 providerCatalogRetryDelays 定义的有限次数补拉继续生效。
func TestCatalogPending_EmptyAvailableCatalog(t *testing.T) {
	if !catalogPending([]ProviderInfo{{ID: "claude", Name: "Claude", Available: true}}) {
		t.Fatal("可用但空目录的供应商应算未就绪")
	}
	if !catalogPending([]ProviderInfo{{ID: "pi", Available: true, Models: []ModelInfo{{ID: "default", Name: "默认"}}}}) {
		t.Fatal("占位目录应算未就绪")
	}
	ready := []ProviderInfo{{ID: "claude", Available: true, Models: []ModelInfo{{ID: "opus", Name: "Opus"}}}}
	if catalogPending(ready) {
		t.Fatal("已有模型的供应商不应算未就绪")
	}
	if catalogPending([]ProviderInfo{{ID: "openai", Available: false}}) {
		t.Fatal("不可用的供应商不应触发补拉")
	}
}

func TestModel_RefreshProviderCatalogAfterPlaceholder(t *testing.T) {
	placeholder := []ProviderInfo{{
		ID: "pi", Name: "pi", Available: true,
		Models: []ModelInfo{{ID: "default", Name: "默认"}},
	}}
	ready := []ProviderInfo{{
		ID: "pi", Name: "pi", Available: true,
		Models: []ModelInfo{{ID: "pi-model", Name: "Pi 模型", Efforts: []string{"low", "high"}, DefaultEffort: "high"}},
	}}

	fake := NewFakeClient()
	fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "Proj1"}}, map[string][]Thread{"p1": {}})

	origDelays := providerCatalogRetryDelays
	providerCatalogRetryDelays = []time.Duration{10 * time.Millisecond, 30 * time.Millisecond}
	defer func() { providerCatalogRetryDelays = origDelays }()

	client := &sequenceProviderClient{FakeClient: fake, seq: [][]ProviderInfo{placeholder, ready}}
	state := newChatState(client)
	state.loadProjects()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state.mu.Lock()
		got := len(state.providers) == 1 && len(state.providers[0].Models) == 1 && state.providers[0].Models[0].ID == "pi-model"
		state.mu.Unlock()
		if got {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	state.mu.Lock()
	models := state.providers[0].Models
	provider := state.newThreadProvider
	state.mu.Unlock()
	if len(models) != 1 || models[0].ID != "pi-model" {
		t.Fatalf("占位目录应被后台补拉替换，实际 %+v", models)
	}
	if len(models[0].Efforts) != 2 {
		t.Fatalf("补拉后应带推理档位，实际 %+v", models[0])
	}
	if provider != "pi" {
		t.Fatalf("补拉不应重置已选供应商，实际 %q", provider)
	}
	if calls := client.providerCalls(); calls < 2 {
		t.Fatalf("占位目录下应至少再拉一次，实际 %d 次", calls)
	}
}

// TestModel_ResyncFrameReloadsMessages 覆盖 U-014：消息帧体超过上限时后端改发 resync 帧，
// 帧里只有摘要没有消息，UI 收到后必须按会话重拉全量消息并替换，否则这一批消息永久缺失。
func TestModel_ResyncFrameReloadsMessages(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.SetMessages("t1", []Message{
		{ID: "m1", Role: "user", Text: "旧消息", TurnIndex: 1},
		{ID: "m2", Role: "tool", Text: "超长输出", TurnIndex: 2},
	})
	state := newChatState(fake)
	t.Cleanup(state.close)

	now := time.Now()
	state.mu.Lock()
	state.threads = []Thread{{ID: "t1", ProjectID: "p1", Title: "会话", UpdatedAt: now}}
	state.threadsByProj["p1"] = []Thread{{ID: "t1", ProjectID: "p1", Title: "会话", UpdatedAt: now}}
	state.selectedThreadID = "t1"
	state.messages = []Message{{ID: "m1", Role: "user", Text: "旧消息", TurnIndex: 1}}
	state.mu.Unlock()

	// resync 帧：只有会话摘要，没有任何消息。
	state.onWatchUpdate(ThreadUpdate{
		ThreadID: "t1",
		Thread:   Thread{ID: "t1", ProjectID: "p1", Title: "会话", UpdatedAt: time.Now(), IsRunning: true},
		Resync:   true,
	})

	deadline := time.Now().Add(2 * time.Second)
	var ids []string
	for time.Now().Before(deadline) {
		state.mu.Lock()
		ids = ids[:0]
		for _, m := range state.messages {
			ids = append(ids, m.ID)
		}
		state.mu.Unlock()
		if len(ids) == 2 && ids[0] == "m1" && ids[1] == "m2" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("resync 帧后应按会话重拉消息，got %v", ids)
}
