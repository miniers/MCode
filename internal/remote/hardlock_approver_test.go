package remote

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

type hardLockTestApprover struct {
	decision harness.Decision
	calls    int
	lastReq  harness.ApprovalRequest
}

func (a *hardLockTestApprover) Approve(_ context.Context, req harness.ApprovalRequest) (harness.Decision, error) {
	a.calls++
	a.lastReq = req
	return a.decision, nil
}

func commandRequest(command string) harness.ApprovalRequest {
	input, _ := json.Marshal(map[string]any{"command": command})
	return harness.ApprovalRequest{
		ToolName: "bash",
		Kind:     harness.RequestKindCommand,
		Input:    input,
		Summary:  command,
	}
}

func newHardLockTestApprover(t *testing.T, locked bool, override string) (*hardLockApprover, *hardLockTestApprover, *MemoryBackend, string) {
	t.Helper()
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	t.Cleanup(hub.Close)
	backend := NewMemoryBackend(hub)
	t.Cleanup(backend.Close)
	threadID := "00000000-0000-0000-0000-000000000901"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "任务线程"}, nil)
	inner := &hardLockTestApprover{decision: harness.DecisionAllow}
	return &hardLockApprover{
		inner:      inner,
		b:          backend,
		threadID:   threadID,
		hardLocked: locked,
		override:   override,
	}, inner, backend, threadID
}

func TestHardLockApproverDeniesLockedCommand(t *testing.T) {
	approver, inner, backend, threadID := newHardLockTestApprover(t, true, "")

	decision, err := approver.Approve(context.Background(), commandRequest("gh pr merge 12"))
	if err != nil {
		t.Fatalf("Approve 失败: %v", err)
	}
	if decision != harness.DecisionDeny {
		t.Fatalf("decision = %v, want deny", decision)
	}
	if inner.calls != 0 {
		t.Fatalf("命中硬锁后不应转给内层，实际调用 %d 次", inner.calls)
	}

	backend.mu.Lock()
	messages := append([]wire.ThreadMessage(nil), backend.threads[threadID].messages...)
	backend.mu.Unlock()
	if len(messages) != 1 {
		t.Fatalf("应追加 1 条系统消息，实际 %d", len(messages))
	}
	msg := messages[0]
	if msg.Role != "system" || msg.Kind == nil || *msg.Kind != "info" {
		t.Fatalf("系统消息字段不匹配: %+v", msg)
	}
	if !strings.Contains(msg.Text, "危险操作已锁定，需要用户答决策卡后解锁") {
		t.Fatalf("系统消息正文不匹配: %q", msg.Text)
	}
}

func TestHardLockApproverForwardsOtherRequests(t *testing.T) {
	cases := []struct {
		name     string
		locked   bool
		override string
		command  string
	}{
		{name: "未命中", locked: true, command: "git status"},
		{name: "未锁定", locked: false, command: "gh pr merge 12"},
		{name: "带 override", locked: true, override: "D-42", command: "gh pr merge 12"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			approver, inner, backend, threadID := newHardLockTestApprover(t, tc.locked, tc.override)
			decision, err := approver.Approve(context.Background(), commandRequest(tc.command))
			if err != nil {
				t.Fatalf("Approve 失败: %v", err)
			}
			if decision != harness.DecisionAllow {
				t.Fatalf("decision = %v, want allow", decision)
			}
			if inner.calls != 1 {
				t.Fatalf("应转给内层 1 次，实际 %d", inner.calls)
			}
			backend.mu.Lock()
			count := len(backend.threads[threadID].messages)
			backend.mu.Unlock()
			if count != 0 {
				t.Fatalf("不应追加系统消息，实际 %d 条", count)
			}
		})
	}
}
