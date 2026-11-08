package remote

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// sysMsg 构造一条系统消息，role 为空时按 system 处理。
func sysMsg(role, kind, text string) wire.ThreadMessage {
	m := wire.ThreadMessage{Role: role, Text: text}
	if m.Role == "" {
		m.Role = "system"
	}
	if kind != "" {
		k := kind
		m.Kind = &k
	}
	return m
}

func TestPendingSplitTable(t *testing.T) {
	cases := []struct {
		name    string
		history []wire.ThreadMessage
		want    bool
	}{
		{name: "空历史", history: nil, want: false},
		{name: "无分割线", history: []wire.ThreadMessage{sysMsg("user", "", "a"), sysMsg("assistant", "", "b")}, want: false},
		{name: "分割线在末尾", history: []wire.ThreadMessage{sysMsg("user", "", "a"), sysMsg("assistant", "", "b"), sysMsg("", "split_session", "已分割")}, want: true},
		{name: "分割线后已有用户消息", history: []wire.ThreadMessage{sysMsg("user", "", "a"), sysMsg("", "split_session", "已分割"), sysMsg("user", "", "c")}, want: false},
		{name: "分割线后只有系统消息", history: []wire.ThreadMessage{sysMsg("", "split_session", "已分割"), sysMsg("", "info", "提示")}, want: true},
		{name: "多条分割线以最后一条为准-后无消息", history: []wire.ThreadMessage{sysMsg("user", "", "a"), sysMsg("", "split_session", "s1"), sysMsg("user", "", "b"), sysMsg("", "split_session", "s2")}, want: true},
		{name: "多条分割线以最后一条为准-后已有消息", history: []wire.ThreadMessage{sysMsg("", "split_session", "s1"), sysMsg("user", "", "a"), sysMsg("", "split_session", "s2"), sysMsg("assistant", "", "b")}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pendingSplit(tc.history); got != tc.want {
				t.Fatalf("pendingSplit = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHistoryAfterLastSplitTable(t *testing.T) {
	cases := []struct {
		name    string
		history []wire.ThreadMessage
		want    []string
	}{
		{name: "空历史", history: nil, want: nil},
		{name: "无分割线原样返回", history: []wire.ThreadMessage{sysMsg("user", "", "a"), sysMsg("assistant", "", "b")}, want: []string{"a", "b"}},
		{name: "分割线在末尾返回空", history: []wire.ThreadMessage{sysMsg("user", "", "a"), sysMsg("", "split_session", "s")}, want: nil},
		{name: "多条分割线只取最后一条之后", history: []wire.ThreadMessage{sysMsg("user", "", "a"), sysMsg("", "split_session", "s1"), sysMsg("user", "", "b"), sysMsg("", "split_session", "s2"), sysMsg("user", "", "c")}, want: []string{"c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := historyAfterLastSplit(tc.history)
			if len(got) != len(tc.want) {
				t.Fatalf("historyAfterLastSplit 长度 = %d, want %d (%v)", len(got), len(tc.want), got)
			}
			for i, want := range tc.want {
				if got[i].Text != want {
					t.Fatalf("第 %d 条 = %q, want %q", i, got[i].Text, want)
				}
			}
		})
	}
}

func TestRequestSplit(t *testing.T) {
	backend, _, _, threadID := rotationTestBackend(t, false)
	ctx := context.Background()
	sendGuidanceMessage(t, backend, threadID, "第一条消息", "00000000-0000-0000-0000-000000000601")

	if err := backend.RequestSplit(threadID); err != nil {
		t.Fatalf("RequestSplit 失败: %v", err)
	}
	// 幂等：再点一次不追加第二条分割线。
	if err := backend.RequestSplit(threadID); err != nil {
		t.Fatalf("重复 RequestSplit 应返回 nil, got %v", err)
	}

	page, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("ThreadHistory 失败: %v", err)
	}
	splits := 0
	for _, m := range page.Messages {
		if m.Kind != nil && *m.Kind == splitSessionKind {
			splits++
			if m.Text != splitSessionText {
				t.Fatalf("分割线文案 = %q, want %q", m.Text, splitSessionText)
			}
			if m.Role != "system" {
				t.Fatalf("分割线 Role = %q, want system", m.Role)
			}
		}
	}
	if splits != 1 {
		t.Fatalf("分割线条数 = %d, want 1", splits)
	}
	if !pendingSplit(page.Messages) {
		t.Fatalf("追加分割线后应处于待分割状态")
	}
	if got := historyAfterLastSplit(page.Messages); len(got) != 0 {
		t.Fatalf("分割线在末尾，之后应无消息, got %d 条", len(got))
	}
}

// splitMessageText 取出历史里唯一一条分割线的正文。
func splitMessageText(t *testing.T, page wire.HistoryPage) string {
	t.Helper()
	text, found := "", false
	for _, m := range page.Messages {
		if m.Kind != nil && *m.Kind == splitSessionKind {
			if found {
				t.Fatalf("历史里应只有一条分割线")
			}
			text, found = m.Text, true
		}
	}
	if !found {
		t.Fatalf("历史里应有分割线")
	}
	return text
}

func TestRequestSplitErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("线程不存在", func(t *testing.T) {
		backend, _, _, _ := rotationTestBackend(t, false)
		err := backend.RequestSplit("00000000-0000-0000-0000-0000000000ff")
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("err = %v, want store.ErrNotFound", err)
		}
	})

	t.Run("空会话", func(t *testing.T) {
		backend, _, _, threadID := rotationTestBackend(t, false)
		if err := backend.RequestSplit(threadID); !errors.Is(err, ErrSplitEmpty) {
			t.Fatalf("err = %v, want ErrSplitEmpty", err)
		}
	})

	t.Run("回合运行中", func(t *testing.T) {
		backend, _, _, threadID := rotationTestBackend(t, false)
		sendGuidanceMessage(t, backend, threadID, "第一条消息", "00000000-0000-0000-0000-000000000602")
		backend.mu.Lock()
		td := backend.threads[threadID]
		prev := td.runTurnID
		td.runTurnID = "turn-running"
		backend.mu.Unlock()
		defer func() {
			backend.mu.Lock()
			td.runTurnID = prev
			backend.mu.Unlock()
		}()
		if err := backend.RequestSplit(threadID); !errors.Is(err, ErrSplitRunning) {
			t.Fatalf("err = %v, want ErrSplitRunning", err)
		}
		page, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
		if err != nil {
			t.Fatalf("ThreadHistory 失败: %v", err)
		}
		if pendingSplit(page.Messages) {
			t.Fatalf("运行中被拒绝时不应写分割线")
		}
	})
}

func TestSplitSessionEndToEnd(t *testing.T) {
	backend, _, mock, threadID := rotationTestBackend(t, true)
	ctx := context.Background()

	var (
		mu    sync.Mutex
		asked []SessionRotationInfo
	)
	backend.SetSessionRotator(func(ctx context.Context, info SessionRotationInfo) (bool, string) {
		mu.Lock()
		asked = append(asked, info)
		mu.Unlock()
		if info.Manual {
			return true, "SPLIT-PREAMBLE"
		}
		return false, ""
	})

	runTwoTurns(t, backend, mock, threadID)
	if err := backend.RequestSplit(threadID); err != nil {
		t.Fatalf("RequestSplit 失败: %v", err)
	}

	sendGuidanceMessage(t, backend, threadID, "第三条消息", "00000000-0000-0000-0000-000000000503")
	calls := mock.getTurnCalls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 harness calls, got %d", len(calls))
	}
	third := calls[2]

	if !strings.Contains(third.Prompt, "SPLIT-PREAMBLE") {
		t.Fatalf("分割后首轮应带 rotator 前言, got %q", third.Prompt)
	}
	if !strings.Contains(third.Prompt, rotationGuidance) {
		t.Fatalf("分割后首轮应重新注入 guidance")
	}
	if !strings.HasSuffix(third.Prompt, "第三条消息") {
		t.Fatalf("分割后首轮应以用户文本结尾, got %q", third.Prompt)
	}
	if strings.Contains(third.Prompt, "第一条消息") || strings.Contains(third.Prompt, "第二条消息") {
		t.Fatalf("分割后首轮不应带分割线以上的历史, got %q", third.Prompt)
	}
	if strings.Contains(third.Prompt, "[mcode: recent conversation]") {
		t.Fatalf("手动分割不应拼最近对话, got %q", third.Prompt)
	}
	if third.SessionID != "" {
		t.Fatalf("分割后应重开会话, got sessionID %q", third.SessionID)
	}

	page, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("ThreadHistory 失败: %v", err)
	}
	rotationNotices := 0
	for _, m := range page.Messages {
		if strings.Contains(m.Text, "已换新会话") {
			rotationNotices++
		}
	}
	if rotationNotices != 0 {
		t.Fatalf("手动分割不应写自动换新提示, got %d 条", rotationNotices)
	}
	if pendingSplit(page.Messages) {
		t.Fatalf("分割线之后已出现用户消息，不应再待分割")
	}

	// 第四轮：分割是一次性的，不再触发 Manual；mock 在换新那一轮没有上报会话 ID，
	// 所以本轮没有 provider 会话可续，只能带上分割线之后的历史补上下文。
	sendGuidanceMessage(t, backend, threadID, "第四条消息", "00000000-0000-0000-0000-000000000504")
	calls = mock.getTurnCalls()
	if len(calls) != 4 {
		t.Fatalf("expected 4 harness calls, got %d", len(calls))
	}
	fourth := calls[3]
	if strings.Contains(fourth.Prompt, "SPLIT-PREAMBLE") {
		t.Fatalf("第四轮不应再按手动分割处理, got %q", fourth.Prompt)
	}
	if fourth.SessionID != "" {
		t.Fatalf("mock 未建立新会话，第四轮不应带会话 ID, got %q", fourth.SessionID)
	}
	if !strings.Contains(fourth.Prompt, "第三条消息") {
		t.Fatalf("没有 provider 会话时应用分割线之后的历史补上下文, got %q", fourth.Prompt)
	}
	if strings.Contains(fourth.Prompt, "第一条消息") || strings.Contains(fourth.Prompt, "第二条消息") {
		t.Fatalf("补上下文不应回放分割线以上的历史, got %q", fourth.Prompt)
	}
	if !strings.HasSuffix(fourth.Prompt, "第四条消息") {
		t.Fatalf("第四轮应以本轮用户文本结尾, got %q", fourth.Prompt)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, info := range asked {
		if info.Manual && info.ThreadID != threadID {
			t.Fatalf("Manual 只应针对本线程, got %q", info.ThreadID)
		}
	}
	manualCount := 0
	for _, info := range asked {
		if info.Manual {
			manualCount++
		}
	}
	if manualCount != 1 {
		t.Fatalf("Manual 只应生效一次, got %d", manualCount)
	}
}

func TestSplitSessionSurvivesRestart(t *testing.T) {
	backend, st, _, threadID := rotationTestBackend(t, false)
	ctx := context.Background()
	sendGuidanceMessage(t, backend, threadID, "第一条消息", "00000000-0000-0000-0000-000000000605")
	if err := backend.RequestSplit(threadID); err != nil {
		t.Fatalf("RequestSplit 失败: %v", err)
	}

	// 重建后端：分割线从库里读回来，未发新消息时仍判定为待分割。
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	t.Cleanup(hub.Close)
	restarted := NewMemoryBackend(hub)
	t.Cleanup(restarted.Close)
	restarted.SetHarness(&mockHarness{}, t.TempDir(), harness.ModeAgent)
	if err := restarted.AttachStore(ctx, st); err != nil {
		t.Fatalf("AttachStore 失败: %v", err)
	}
	page, err := restarted.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("ThreadHistory 失败: %v", err)
	}
	if !pendingSplit(page.Messages) {
		t.Fatalf("重启后仍应处于待分割状态")
	}
	if got := historyAfterLastSplit(page.Messages); len(got) != 0 {
		t.Fatalf("重启后分割线之后应无消息, got %d 条", len(got))
	}
	if err := restarted.RequestSplit(threadID); err != nil {
		t.Fatalf("重启后重复分割应幂等返回 nil, got %v", err)
	}
}
