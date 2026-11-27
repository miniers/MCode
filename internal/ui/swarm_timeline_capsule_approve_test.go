package mcodeui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

type approveCaptureClient struct {
	*FakeClient
	mu      sync.Mutex
	calls   [][2]string
	started chan struct{}
	release chan struct{}
	err     error
}

func (c *approveCaptureClient) ApproveSwarmMerge(ctx context.Context, leadThreadID, beeThreadID string) error {
	c.mu.Lock()
	c.calls = append(c.calls, [2]string{leadThreadID, beeThreadID})
	started := c.started
	release := c.release
	err := c.err
	c.mu.Unlock()

	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err != nil {
		return err
	}
	return c.FakeClient.ApproveSwarmMerge(ctx, leadThreadID, beeThreadID)
}

func (c *approveCaptureClient) recordedCalls() [][2]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][2]string, len(c.calls))
	copy(out, c.calls)
	return out
}

func TestSwarmCapsuleApproveButton(t *testing.T) {
	const leadID = "lead-capsule-approve"

	t.Run("needs_human and pending merge shows approve before discard", func(t *testing.T) {
		fake := NewFakeClient()
		bee := SwarmBee{
			ThreadID:     "bee-needs-human",
			Name:         "Aspen",
			Status:       "completed",
			PendingMerge: true,
			ReviewState:  "needs_human",
			MergeError:   "与未提交改动冲突",
		}
		s := newChatState(fake)
		t.Cleanup(s.close)
		s.selectedThreadID = leadID

		tt := ui.NewTester(func(c *ui.Context) {
			renderSwarmCapsuleOpenFloatButton(c, s, leadID, bee, "cap-needs-human", nil)
		}, 600, 200)
		tt.Frame()

		approveAcc, ok := tt.AccessByRole(ui.RoleButton, "批准合并")
		if !ok {
			t.Fatalf("ReviewState=needs_human 且 PendingMerge=true 时应渲染批准合并按钮，当前元素: %+v", tt.AccessAll())
		}
		discardAcc, ok := tt.AccessByRole(ui.RoleButton, "放弃改动")
		if !ok {
			t.Fatalf("ReviewState=needs_human 且 PendingMerge=true 时应渲染放弃改动按钮，当前元素: %+v", tt.AccessAll())
		}
		if approveAcc.Bounds.X >= discardAcc.Bounds.X {
			t.Fatalf("批准合并按钮应位于放弃改动按钮之前: approve=%+v discard=%+v", approveAcc.Bounds, discardAcc.Bounds)
		}
	})

	for _, tc := range []struct {
		name         string
		pendingMerge bool
		reviewState  string
	}{
		{name: "review state empty", pendingMerge: true, reviewState: ""},
		{name: "review state none", pendingMerge: true, reviewState: "none"},
		{name: "review state l0", pendingMerge: true, reviewState: "l0"},
		{name: "review state approved", pendingMerge: true, reviewState: "approved"},
		{name: "review state rejected", pendingMerge: true, reviewState: "rejected"},
		{name: "review state running", pendingMerge: true, reviewState: "running"},
		{name: "not pending merge", pendingMerge: false, reviewState: "needs_human"},
	} {
		t.Run("hides approve for "+tc.name, func(t *testing.T) {
			fake := NewFakeClient()
			bee := SwarmBee{
				ThreadID:     "bee-other",
				Name:         "Birch",
				Status:       "completed",
				PendingMerge: tc.pendingMerge,
				ReviewState:  tc.reviewState,
			}
			s := newChatState(fake)
			t.Cleanup(s.close)
			s.selectedThreadID = leadID

			tt := ui.NewTester(func(c *ui.Context) {
				renderSwarmCapsuleOpenFloatButton(c, s, leadID, bee, "cap-other", nil)
			}, 600, 200)
			tt.Frame()

			if _, ok := tt.AccessByRole(ui.RoleButton, "批准合并"); ok {
				t.Fatalf("pendingMerge=%v reviewState=%q 时不应渲染批准合并按钮", tc.pendingMerge, tc.reviewState)
			}
		})
	}

	t.Run("click approves using capsule leadID and handles busy state", func(t *testing.T) {
		client := &approveCaptureClient{
			FakeClient: NewFakeClient(),
			started:    make(chan struct{}, 2),
			release:    make(chan struct{}),
		}
		bee := SwarmBee{
			ThreadID:     "bee-approve-click",
			Name:         "Cedar",
			Status:       "completed",
			PendingMerge: true,
			ReviewState:  "needs_human",
		}
		client.SetSwarmState(leadID, SwarmState{
			LeadThreadID: leadID,
			Available:    true,
			Enabled:      true,
			Bees:         []SwarmBee{bee},
		})
		s := newChatState(client)
		t.Cleanup(s.close)
		s.selectedThreadID = "different-thread"

		tt := ui.NewTester(func(c *ui.Context) {
			renderSwarmCapsuleOpenFloatButton(c, s, leadID, bee, "cap-click", nil)
		}, 600, 200)
		tt.Frame()

		if err := tt.Click("批准合并"); err != nil {
			t.Fatalf("点击批准合并失败: %v", err)
		}
		select {
		case <-client.started:
		case <-time.After(2 * time.Second):
			t.Fatal("ApproveSwarmMerge 请求未开始")
		}

		tt.Frame()
		if !s.swarmActionBusy("approve:" + bee.ThreadID) {
			t.Fatal("请求进行中时应标记 approve 忙碌态")
		}
		btn, ok := tt.AccessByRole(ui.RoleButton, "批准合并")
		if !ok || !btn.Disabled {
			t.Fatalf("忙碌态时批准合并按钮应禁用: ok=%v btn=%+v", ok, btn)
		}

		close(client.release)
		waitFor(t, tt, func() bool {
			return !s.swarmActionBusy("approve:" + bee.ThreadID)
		})

		calls := client.recordedCalls()
		if len(calls) != 1 || calls[0] != [2]string{leadID, bee.ThreadID} {
			t.Fatalf("ApproveSwarmMerge 调用参数不符: got=%v want=[[%q %q]]", calls, leadID, bee.ThreadID)
		}
	})

	t.Run("error shows toast and clears busy", func(t *testing.T) {
		client := &approveCaptureClient{
			FakeClient: NewFakeClient(),
			err:        errors.New("请提交或暂存冲突文件"),
		}
		bee := SwarmBee{
			ThreadID:     "bee-approve-err",
			Name:         "Dogwood",
			Status:       "completed",
			PendingMerge: true,
			ReviewState:  "needs_human",
		}
		s := newChatState(client)
		t.Cleanup(s.close)
		s.selectedThreadID = leadID

		tt := ui.NewTester(func(c *ui.Context) {
			renderSwarmCapsuleOpenFloatButton(c, s, leadID, bee, "cap-err", nil)
		}, 600, 200)
		tt.Frame()

		if err := tt.Click("批准合并"); err != nil {
			t.Fatalf("点击批准合并失败: %v", err)
		}
		waitFor(t, tt, func() bool {
			s.mu.Lock()
			msg := s.toastMessage
			s.mu.Unlock()
			return strings.Contains(msg, "批准合并失败") && !s.swarmActionBusy("approve:"+bee.ThreadID)
		})
	})
}
