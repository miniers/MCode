package mcodeui

import (
	"reflect"
	"testing"
)

func TestTimelineIncrementalTurnTimingFingerprint(t *testing.T) {
	base := Message{ID: "m", TurnID: "t"}
	for _, changed := range []Message{
		{ID: "m", TurnID: "t", TurnStartedAt: "2026-11-06T08:00:00Z"},
		{ID: "m", TurnID: "t", TurnCompletedAt: "2026-11-06T08:00:12Z"},
		{ID: "m", TurnID: "t", TurnStatus: "completed"},
	} {
		if makeMessageFingerprint(base) == makeMessageFingerprint(changed) {
			t.Fatalf("轮次元数据变化未更新指纹: %+v", changed)
		}
	}
}

// helperBuildAndAssertIncremental 运行增量重建并断言结果与全量 buildTimelineItems 严格一致。
func helperBuildAndAssertIncremental(t *testing.T, s *chatState, threadID string, messages []Message, expanded map[string]bool, testCase string) {
	t.Helper()

	// 1. 获取全量构建的黄金结果：回合折叠是重建的最后一步后处理，
	// 黄金结果必须走同一条路径，否则每一条增量用例都会与折叠后的实际结果不等。
	wantItems := applyTurnWorkFold(s, threadID, buildTimelineItems(messages, expanded))

	// 2. 运行增量构建
	gotItems, newFps := s.rebuildTimelineIncremental(threadID, messages, expanded)

	// 3. 断言两者严格相等
	if !reflect.DeepEqual(gotItems, wantItems) {
		t.Fatalf("[%s] 增量重建结果与全量构建不匹配:\ngot:  %+v\nwant: %+v", testCase, gotItems, wantItems)
	}

	// 4. 更新 state 中的快照以模拟实际渲染链路
	s.timelineCacheItems = gotItems
	s.timelineSnapshotThreadID = threadID
	s.timelineSnapshotMsgs = newFps
	s.timelineSnapshotExpanded = cloneBoolMap(expanded)
}

// TestTimelineIncremental_Append 验证末尾追加普通消息和工具消息时增量重建结果与全量一致。
func TestTimelineIncremental_Append(t *testing.T) {
	s := &chatState{}
	threadID := "thread-1"

	// 初始状态：一条用户消息与助手消息
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "你好", TurnIndex: 1},
		{ID: "a1", Role: "assistant", Text: "你好！有什么我可以帮你的？", TurnIndex: 1},
	}
	helperBuildAndAssertIncremental(t, s, threadID, msgs, nil, "初始消息列表")

	// 追加第二轮用户消息
	msgs = append(msgs, Message{ID: "u2", Role: "user", Text: "查看当前文件", TurnIndex: 2})
	helperBuildAndAssertIncremental(t, s, threadID, msgs, nil, "追加第二轮用户消息")

	// 追加工具调用消息
	msgs = append(msgs, Message{ID: "t1", Role: "tool", ToolName: "ls", Text: "main.go", TurnIndex: 2})
	helperBuildAndAssertIncremental(t, s, threadID, msgs, nil, "追加工具消息")

	// 连续追加第二个工具调用（触发工具组聚合）
	msgs = append(msgs, Message{ID: "t2", Role: "tool", ToolName: "cat", Text: "package main", TurnIndex: 2})
	helperBuildAndAssertIncremental(t, s, threadID, msgs, nil, "追加第二个工具消息")

	// 追加最终助手回复
	msgs = append(msgs, Message{ID: "a2", Role: "assistant", Text: "已为您查看完毕。", TurnIndex: 2})
	helperBuildAndAssertIncremental(t, s, threadID, msgs, nil, "追加助手最终回复")
}

// TestTimelineIncremental_ModifyIntermediate 验证修改中间消息时增量重建结果与全量一致。
func TestTimelineIncremental_ModifyIntermediate(t *testing.T) {
	s := &chatState{}
	threadID := "thread-1"

	msgs := []Message{
		{ID: "u1", Role: "user", Text: "最初提问", TurnIndex: 1},
		{ID: "a1", Role: "assistant", Text: "中间消息初态", TurnIndex: 1},
		{ID: "u2", Role: "user", Text: "后续提问", TurnIndex: 2},
		{ID: "a2", Role: "assistant", Text: "末尾消息", TurnIndex: 2},
	}
	helperBuildAndAssertIncremental(t, s, threadID, msgs, nil, "初始四条消息")

	// 修改中间消息 a1 的文本内容
	msgs[1] = Message{ID: "a1", Role: "assistant", Text: "中间消息已发生实质修改并变长", TurnIndex: 1}
	helperBuildAndAssertIncremental(t, s, threadID, msgs, nil, "修改中间消息内容")

	// 修改中间消息 u1 的文本内容
	msgs[0] = Message{ID: "u1", Role: "user", Text: "最初提问（编辑后）", TurnIndex: 1}
	helperBuildAndAssertIncremental(t, s, threadID, msgs, nil, "修改首条消息内容")
}

// TestTimelineIncremental_ModifyLastMessageStreaming 验证流式追加修改最后一条消息时增量重建结果与全量一致。
func TestTimelineIncremental_ModifyLastMessageStreaming(t *testing.T) {
	s := &chatState{}
	threadID := "thread-1"

	msgs := []Message{
		{ID: "u1", Role: "user", Text: "请输出代码", TurnIndex: 1},
		{ID: "a1", Role: "assistant", Text: "package", TurnIndex: 1},
	}
	helperBuildAndAssertIncremental(t, s, threadID, msgs, nil, "流式首包")

	// 模拟连续追加流式 Token
	tokens := []string{" main\n", "func main() {\n", "    println(\"hello\")\n", "}\n"}
	for i, tok := range tokens {
		msgs[1].Text += tok
		helperBuildAndAssertIncremental(t, s, threadID, msgs, nil, "流式打字中-轮次")
		if i == len(tokens)-1 {
			msgs[1].IsFinal = true
			helperBuildAndAssertIncremental(t, s, threadID, msgs, nil, "流式结束完成")
		}
	}
}

// TestTimelineIncremental_ToolMessagesAddAndModify 验证增删修改工具消息时的聚合与增量重建。
func TestTimelineIncremental_ToolMessagesAddAndModify(t *testing.T) {
	s := &chatState{}
	threadID := "thread-1"

	msgs := []Message{
		{ID: "u1", Role: "user", Text: "开始任务", TurnIndex: 1},
		{ID: "t1", Role: "tool", ToolName: "cmd1", Text: "运行中...", ToolStatus: "running", TurnIndex: 1},
	}
	helperBuildAndAssertIncremental(t, s, threadID, msgs, nil, "单个工具运行中")

	// 工具 1 执行完成
	msgs[1].ToolStatus = "completed"
	msgs[1].Text = "执行成功"
	msgs[1].ToolCompletedAt = "2026-10-20T10:00:00Z"
	helperBuildAndAssertIncremental(t, s, threadID, msgs, nil, "单个工具完成")

	// 增加工具 2（单工具转为工具组）
	msgs = append(msgs, Message{ID: "t2", Role: "tool", ToolName: "cmd2", Text: "运行中...", ToolStatus: "running", TurnIndex: 1})
	helperBuildAndAssertIncremental(t, s, threadID, msgs, nil, "增加第二个工具成为工具组")

	// 工具 2 状态修改
	msgs[2].ToolStatus = "completed"
	msgs[2].Text = "全部通过"
	helperBuildAndAssertIncremental(t, s, threadID, msgs, nil, "工具组内工具修改完成")

	// 增加工具 3
	msgs = append(msgs, Message{ID: "t3", Role: "tool", ToolName: "cmd3", Text: "第三项任务完成", ToolStatus: "completed", TurnIndex: 1})
	helperBuildAndAssertIncremental(t, s, threadID, msgs, nil, "工具组内增加第三个工具")
}

// TestTimelineIncremental_ExpandCollapse 验证工具组展开和折叠时行级虚拟化与增量机制的正确性。
func TestTimelineIncremental_ExpandCollapse(t *testing.T) {
	s := &chatState{}
	threadID := "thread-1"

	msgs := []Message{
		{ID: "u1", Role: "user", Text: "构建项目", TurnIndex: 1},
		{ID: "t1", Role: "tool", ToolName: "go_mod", Text: "downloading...", TurnIndex: 1},
		{ID: "t2", Role: "tool", ToolName: "go_build", Text: "compiling...", TurnIndex: 1},
		{ID: "t3", Role: "tool", ToolName: "go_test", Text: "ok", TurnIndex: 1},
		{ID: "a1", Role: "assistant", Text: "构建并测试完毕", TurnIndex: 1},
	}

	groupID := "toolgroup-t1"

	// 1. 初始为折叠状态：应该只有 1 个工具组项
	expanded := map[string]bool{groupID: false}
	helperBuildAndAssertIncremental(t, s, threadID, msgs, expanded, "初始折叠状态")

	// 2. 展开工具组：触发退回全量重建，生成 1 个组头项 + 1 个节拍项（这三条工具之间没有思考消息，连续工具合为同一个节拍）
	expanded = map[string]bool{groupID: true}
	helperBuildAndAssertIncremental(t, s, threadID, msgs, expanded, "展开工具组")

	// 3. 在展开状态下末尾追加新消息，验证增量重建与展开子项兼容
	msgs = append(msgs, Message{ID: "u2", Role: "user", Text: "继续", TurnIndex: 2})
	helperBuildAndAssertIncremental(t, s, threadID, msgs, expanded, "展开状态下末尾追加消息")

	// 4. 收起工具组：触发退回全量重建，恢复为单个组项
	expanded = map[string]bool{groupID: false}
	helperBuildAndAssertIncremental(t, s, threadID, msgs, expanded, "收起工具组")
}

// TestTimelineIncremental_ThreadSwitch 验证切换会话时退回全量重建。
func TestTimelineIncremental_ThreadSwitch(t *testing.T) {
	s := &chatState{}

	msgs1 := []Message{
		{ID: "u1", Role: "user", Text: "会话 1 的内容", TurnIndex: 1},
		{ID: "a1", Role: "assistant", Text: "会话 1 的回复", TurnIndex: 1},
	}
	helperBuildAndAssertIncremental(t, s, "thread-1", msgs1, nil, "会话 1 构建")

	msgs2 := []Message{
		{ID: "u2", Role: "user", Text: "会话 2 的内容", TurnIndex: 1},
		{ID: "a2", Role: "assistant", Text: "会话 2 的回复", TurnIndex: 1},
	}
	helperBuildAndAssertIncremental(t, s, "thread-2", msgs2, nil, "切换到会话 2 构建")
}

// TestTimelineIncremental_AppendToolAfterReasoning 验证同回合推理消息之后追加工具消息时，
// 推理项会随活动消息整段重建，不会被缓存前缀拆到工具组之外。
func TestTimelineIncremental_AppendToolAfterReasoning(t *testing.T) {
	s := &chatState{}
	threadID := "thread-1"

	msgs := []Message{
		{ID: "u1", Role: "user", Text: "先想一想", TurnIndex: 1},
		{ID: "r1", Role: "reasoning", Kind: "reasoning", Text: "需要先查看文件", TurnIndex: 1},
	}
	helperBuildAndAssertIncremental(t, s, threadID, msgs, nil, "推理消息进入缓存")

	// 追加同回合的工具消息
	msgs = append(msgs, Message{ID: "t1", Role: "tool", ToolName: "ls", Text: "main.go", TurnIndex: 1})

	// 篡改缓存前缀项：若回退不足导致直接复用前缀，该标记会残留；若退回全量重建则被丢弃。
	s.timelineCacheItems[0].id = "sentinel-prefix"
	gotItems, _ := s.rebuildTimelineIncremental(threadID, msgs, nil)
	if len(gotItems) == 0 || gotItems[0].id != "sentinel-prefix" {
		t.Fatalf("前缀项未复用，增量重建退化成全量重建: %+v", gotItems)
	}
	s.timelineCacheItems[0].id = "u1"

	helperBuildAndAssertIncremental(t, s, threadID, msgs, nil, "推理后追加同回合工具消息")
}

// TestTimelineIncremental_TurnWorkFoldKeepsPrefixReuse 验证回合折叠后，追加新回合时
// 已折叠的第一回合仍然作为缓存前缀复用，且增量结果与全量构建完全一致。
func TestTimelineIncremental_TurnWorkFoldKeepsPrefixReuse(t *testing.T) {
	s := &chatState{}
	threadID := "thread-1"

	msgs := []Message{
		{ID: "u1", Role: "user", Text: "跑一下构建", TurnIndex: 1},
		{ID: "t1", Role: "tool", Text: "go build ./...", TurnIndex: 1},
		{ID: "a1", Role: "assistant", Text: "构建通过", TurnIndex: 1},
	}
	helperBuildAndAssertIncremental(t, s, threadID, msgs, nil, "折叠后首次构建")

	if len(s.timelineCacheItems) == 0 || s.timelineCacheItems[0].kind != timelineItemSingle || s.timelineCacheItems[1].kind != timelineItemTurnWork {
		t.Fatalf("第一回合应折叠成一项且跟在自己的用户消息之后，got %+v", s.timelineCacheItems)
	}
	foldIdx := 1
	foldID := s.timelineCacheItems[foldIdx].id

	// 追加一个全新的回合：第一回合的折叠项必须被原样复用，而不是重建。
	msgs = append(msgs,
		Message{ID: "u2", Role: "user", Text: "再跑一下测试", TurnIndex: 2},
		Message{ID: "t2", Role: "tool", Text: "go test ./...", TurnIndex: 2},
		Message{ID: "a2", Role: "assistant", Text: "测试通过", TurnIndex: 2},
	)
	s.timelineCacheItems[foldIdx].id = "sentinel-" + foldID
	gotItems, _ := s.rebuildTimelineIncremental(threadID, msgs, nil)
	if len(gotItems) <= foldIdx || gotItems[foldIdx].id != "sentinel-"+foldID {
		t.Fatalf("回合折叠项未作为前缀复用: %+v", gotItems)
	}
	s.timelineCacheItems[foldIdx].id = foldID

	helperBuildAndAssertIncremental(t, s, threadID, msgs, nil, "追加新回合后折叠保持")

	// 两个回合应各自折叠成一项，最终正文都留在折叠项之外。
	folds := 0
	for _, it := range s.timelineCacheItems {
		if it.kind == timelineItemTurnWork {
			folds++
		}
	}
	if folds != 2 {
		t.Fatalf("两个回合应各自折叠，got %d 项折叠", folds)
	}
}
