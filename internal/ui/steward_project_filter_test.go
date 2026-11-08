package mcodeui

import "testing"

// withoutStewardProject 剔除指定项目、projectID 为空时原样返回、且不修改入参。
func TestWithoutStewardProject(t *testing.T) {
	input := []Project{
		{ID: "p1", Name: "普通项目", Path: "/path/to/p1"},
		{ID: "p2", Name: "总管的家", Path: "/path/to/p2"},
	}

	got := withoutStewardProject("p2", input)
	if len(got) != 1 || got[0].ID != "p1" {
		t.Fatalf("应只保留普通项目，实际: %+v", got)
	}
	if len(input) != 2 || input[0].ID != "p1" || input[1].ID != "p2" {
		t.Fatalf("不应修改入参切片，实际: %+v", input)
	}

	// projectID 为空时原样返回全部项目，但仍然是一份可安全修改的副本。
	all := withoutStewardProject("", input)
	if len(all) != 2 || all[0].ID != "p1" || all[1].ID != "p2" {
		t.Fatalf("projectID 为空时应原样返回全部项目，实际: %+v", all)
	}
	all[0].Name = "改副本"
	if input[0].Name != "普通项目" {
		t.Fatalf("返回的切片不应与入参共享底层数组，实际: %+v", input)
	}

	// 列表里没有该项目时同样原样保留。
	missing := withoutStewardProject("p9", input)
	if len(missing) != 2 {
		t.Fatalf("项目不在列表里时应全部保留，实际: %+v", missing)
	}
}

// 总管项目 ID 从平铺会话或分组表里都能推出来；没有总管会话时是空串。
func TestStewardProjectIDForThread(t *testing.T) {
	threads := []Thread{
		{ID: "t1", ProjectID: "p1"},
		{ID: "t9", ProjectID: "p2"},
	}
	byProj := map[string][]Thread{
		"p1": {{ID: "t1", ProjectID: "p1"}},
		"p2": {{ID: "t9", ProjectID: "p2"}},
	}

	if got := stewardProjectIDForThread("t9", threads, byProj); got != "p2" {
		t.Fatalf("平铺视图里应能找出总管项目，实际: %q", got)
	}
	// 平铺视图里没有这条会话时退回分组表。
	if got := stewardProjectIDForThread("t9", nil, byProj); got != "p2" {
		t.Fatalf("分组表里应能找出总管项目，实际: %q", got)
	}
	if got := stewardProjectIDForThread("", threads, byProj); got != "" {
		t.Fatalf("没有总管会话时应返回空串，实际: %q", got)
	}
	if got := stewardProjectIDForThread("t404", threads, byProj); got != "" {
		t.Fatalf("找不到总管会话时应返回空串，实际: %q", got)
	}
}

// chatState 上的包装方法在没有总管客户端、或客户端还没给出总管会话时返回空串。
func TestChatStateStewardProjectID(t *testing.T) {
	var nilState *chatState
	if got := nilState.stewardProjectID(); got != "" {
		t.Fatalf("空状态应返回空串，实际: %q", got)
	}

	noSteward := &chatState{}
	if got := noSteward.stewardProjectID(); got != "" {
		t.Fatalf("没有总管客户端时应返回空串，实际: %q", got)
	}

	// 客户端还没有总管会话：即使分组表里放着同名会话也不该认领。
	empty := &chatState{
		client: newStewardFakeClient(""),
		threads: []Thread{
			{ID: "t1", ProjectID: "p1"},
		},
		threadsByProj: map[string][]Thread{"p1": {{ID: "t1", ProjectID: "p1"}}},
	}
	if got := empty.stewardProjectID(); got != "" {
		t.Fatalf("总管会话未建好时应返回空串，实际: %q", got)
	}

	withSteward := &chatState{
		client: newStewardFakeClient("t9"),
		threads: []Thread{
			{ID: "t1", ProjectID: "p1"},
			{ID: "t9", ProjectID: "p2"},
		},
		threadsByProj: map[string][]Thread{
			"p1": {{ID: "t1", ProjectID: "p1"}},
			"p2": {{ID: "t9", ProjectID: "p2"}},
		},
	}
	if got := withSteward.stewardProjectID(); got != "p2" {
		t.Fatalf("应返回总管会话所属项目，实际: %q", got)
	}
	// 方法只读，不改状态。
	if len(withSteward.projects) != 0 || len(withSteward.threads) != 2 {
		t.Fatalf("查询总管项目不应修改状态，实际: projects=%+v threads=%+v", withSteward.projects, withSteward.threads)
	}
}

// withoutStewardThread 仅从项目与会话副本中移除 feed 会话及其项目。
func TestWithoutStewardThreadFiltersFeedOnly(t *testing.T) {
	projects := []Project{{ID: "p1"}, {ID: "p2"}}
	threads := []Thread{{ID: "feed", ProjectID: "p2"}, {ID: "topic", ProjectID: "p1"}, {ID: "plain", ProjectID: "p1"}}
	byProject := map[string][]Thread{
		"p1": {{ID: "topic", ProjectID: "p1"}, {ID: "plain", ProjectID: "p1"}},
		"p2": {{ID: "feed", ProjectID: "p2"}},
	}
	gotProjects, gotThreads, gotByProject := withoutStewardThread("feed", projects, threads, byProject)
	if len(gotProjects) != 1 || gotProjects[0].ID != "p1" {
		t.Fatalf("应仅移除 feed 项目，得到 %+v", gotProjects)
	}
	if len(gotThreads) != 2 || gotThreads[0].ID != "topic" || gotThreads[1].ID != "plain" {
		t.Fatalf("应保留其他线程，包括旧 topic 线程，得到 %+v", gotThreads)
	}
	if len(gotByProject["p1"]) != 2 || gotByProject["p2"] != nil {
		t.Fatalf("分组应仅移除 feed 项目，得到 %+v", gotByProject)
	}
}
