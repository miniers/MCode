package mcodeui

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// swarmDispatchTestBlock 生成一段含已闭合派单块的助手回复，派单条目数由 n 决定。
func swarmDispatchTestBlock(n int) string {
	entries := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		entries = append(entries, fmt.Sprintf(`{"task":"任务 %d","prompt":"执行任务 %d","bee":"Bee%d"}`, i, i, i))
	}
	return fmt.Sprintf("派出 %d 个子代理：\n\n```swarm\n[%s]\n```\n\n请等待结果汇报。", n, strings.Join(entries, ","))
}

// swarmDispatchTestState 构造派发占位测试用的主会话：选中 lead 线程且主线程运行中，
// 时间线快照用传入的消息，Swarm 状态直接写进面板缓存。
func swarmDispatchTestState(t *testing.T, msgs []Message, st SwarmState) *chatState {
	t.Helper()
	s := newChatState(NewFakeClient())
	t.Cleanup(func() { closeEmbeddedBees(s) })
	s.selectedThreadID = st.LeadThreadID
	s.threads = []Thread{{ID: st.LeadThreadID, IsRunning: true}}
	s.timelineCacheMsgs = msgs
	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	hp.setStateLocked(st)
	swarmPanelMu.Unlock()
	return s
}

// TestSwarmDispatchPlaceholderHiddenAfterNextTurnStarts 覆盖回归场景：派单块已闭合、子代理都已结束，
// 主代理进入下一回合后时间线末尾已有工具调用消息，占位必须收掉。这里同时覆盖面板状态里还列着 4 个子代理
// 与状态已被清空两种情况：两种都应该靠“后面还有新消息”判定为已结束，而不靠子代理数凑齐。
func TestSwarmDispatchPlaceholderHiddenAfterNextTurnStarts(t *testing.T) {
	created := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	bees := make([]SwarmBee, 0, 4)
	for i := 1; i <= 4; i++ {
		bees = append(bees, SwarmBee{
			ThreadID:  fmt.Sprintf("bee-%d", i),
			Name:      fmt.Sprintf("Bee%d", i),
			Status:    "completed",
			StartedAt: created.Add(time.Duration(i) * time.Second),
		})
	}
	msgs := []Message{
		{ID: "u1", Role: "user", TurnIndex: 0, CreatedAt: "2026-01-01T09:59:00Z"},
		{ID: "a1", Role: "assistant", TurnIndex: 0, CreatedAt: created.Format(time.RFC3339), Text: swarmDispatchTestBlock(4)},
		{ID: "t1", Role: "tool", TurnIndex: 1, CreatedAt: "2026-01-01T10:05:00Z", ToolName: "bash"},
	}
	for _, tc := range []struct {
		name string
		bees []SwarmBee
	}{
		{"四个子代理都已结束", bees},
		{"面板状态已经清空", nil},
	} {
		s := swarmDispatchTestState(t, msgs, SwarmState{
			Available:    true,
			Enabled:      true,
			LeadThreadID: "lead1",
			Bees:         tc.bees,
		})
		if view := swarmDispatchPlaceholder(s, ""); view.Visible {
			t.Fatalf("%s：进入下一回合后不应再显示派发占位，实际: %+v", tc.name, view)
		}
	}
}

// TestSwarmDispatchPlaceholderShownWhileBeesStarting 验证派单块已闭合、后面还没有新消息时，
// 子代理还没落地就继续显示占位，且文案带上条目数。
func TestSwarmDispatchPlaceholderShownWhileBeesStarting(t *testing.T) {
	msgs := []Message{
		{ID: "u1", Role: "user", TurnIndex: 0, CreatedAt: "2026-01-01T09:59:00Z"},
		{ID: "a1", Role: "assistant", TurnIndex: 0, CreatedAt: "2026-01-01T10:00:00Z", Text: swarmDispatchTestBlock(4)},
	}
	s := swarmDispatchTestState(t, msgs, SwarmState{
		Available:    true,
		Enabled:      true,
		LeadThreadID: "lead1",
	})

	view := swarmDispatchPlaceholder(s, "")
	if !view.Visible || view.Text != "正在派发 4 个子代理…" {
		t.Fatalf("派单块闭合、子代理未落地时应显示占位，实际: %+v", view)
	}
}

// TestSwarmDispatchPlaceholderSecondRoundIgnoresOldBees 验证第二轮派单只数本条消息之后启动的子代理：
// 上一轮结束的 4 个旧子代理不能把本轮 2 条目的占位顶掉。
func TestSwarmDispatchPlaceholderSecondRoundIgnoresOldBees(t *testing.T) {
	created := time.Date(2026, 1, 1, 10, 10, 0, 0, time.UTC)
	bees := make([]SwarmBee, 0, 4)
	for i := 1; i <= 4; i++ {
		bees = append(bees, SwarmBee{
			ThreadID:  fmt.Sprintf("old-%d", i),
			Name:      fmt.Sprintf("Old%d", i),
			Status:    "completed",
			StartedAt: created.Add(-10 * time.Minute),
		})
	}
	msgs := []Message{
		{ID: "a1", Role: "assistant", TurnIndex: 0, CreatedAt: "2026-01-01T09:50:00Z", Text: swarmDispatchTestBlock(4)},
		{ID: "a2", Role: "assistant", TurnIndex: 1, CreatedAt: created.Format(time.RFC3339), Text: swarmDispatchTestBlock(2)},
	}
	s := swarmDispatchTestState(t, msgs, SwarmState{
		Available:    true,
		Enabled:      true,
		LeadThreadID: "lead1",
		Bees:         bees,
	})

	view := swarmDispatchPlaceholder(s, "")
	if !view.Visible || view.Text != "正在派发 2 个子代理…" {
		t.Fatalf("第二轮派单不应被旧子代理顶掉，实际: %+v", view)
	}
}

// TestSwarmDispatchPlaceholderStreamingOpenBlock 验证流式输出中派单块还没闭合时仍然显示占位。
func TestSwarmDispatchPlaceholderStreamingOpenBlock(t *testing.T) {
	text := "派出 2 个子代理：\n\n```swarm\n[{\"task\":\"任务 1\",\"prompt\":\"执行任务 1\",\"bee\":\"Bee1\"},{\"task\":\"任务 2\","
	msgs := []Message{
		{ID: "u1", Role: "user", TurnIndex: 0, CreatedAt: "2026-01-01T10:00:00Z"},
		{ID: "a1", Role: "assistant", TurnIndex: 0, CreatedAt: "2026-01-01T10:00:05Z", Text: text},
	}
	s := swarmDispatchTestState(t, msgs, SwarmState{
		Available:    true,
		Enabled:      true,
		LeadThreadID: "lead1",
	})

	view := swarmDispatchPlaceholder(s, "a1")
	if !view.Visible || view.Text != "正在派发 2 个子代理…" {
		t.Fatalf("流式中未闭合派单块应显示占位，实际: %+v", view)
	}
}
