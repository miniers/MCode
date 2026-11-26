package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSwarmCapsuleActions_StopAndDiscard(t *testing.T) {
	const leadID = "lead-capsule-actions"

	t.Run("running shows stop only", func(t *testing.T) {
		fake := NewFakeClient()
		bee := SwarmBee{ThreadID: "bee-run", Name: "Aspen", Status: "running", PendingMerge: true}
		fake.SetSwarmState(leadID, SwarmState{
			LeadThreadID: leadID,
			Available:    true,
			Enabled:      true,
			Bees:         []SwarmBee{bee},
		})
		s := newChatState(fake)
		t.Cleanup(s.close)
		s.selectedThreadID = leadID

		tt := ui.NewTester(func(c *ui.Context) {
			renderSwarmCapsuleOpenFloatButton(c, s, leadID, bee, "cap-run", nil)
		}, 500, 200)
		tt.Frame()

		if _, ok := tt.AccessByRole(ui.RoleButton, "停止"); !ok {
			t.Fatalf("running 状态应渲染停止按钮，当前元素: %+v", tt.AccessAll())
		}
		if _, ok := tt.AccessByRole(ui.RoleButton, "放弃改动"); ok {
			t.Fatal("running 状态不应渲染放弃改动按钮")
		}

		if err := tt.Click("停止"); err != nil {
			t.Fatalf("点击停止按钮失败: %v", err)
		}
		waitFor(t, tt, func() bool {
			stopped := fake.StoppedBees()
			return len(stopped) == 1 && stopped[0] == "bee-run"
		})
	})

	t.Run("completed pending merge shows discard only", func(t *testing.T) {
		const key = "cap-discard"
		t.Cleanup(func() {
			swarmFloatMu.Lock()
			delete(swarmCapsuleDiscardBee, key)
			swarmFloatMu.Unlock()
		})

		fake := NewFakeClient()
		bee := SwarmBee{ThreadID: "bee-pending", Name: "Birch", Status: "completed", PendingMerge: true}
		fake.SetSwarmState(leadID, SwarmState{
			LeadThreadID: leadID,
			Available:    true,
			Enabled:      true,
			Bees:         []SwarmBee{bee},
		})
		s := newChatState(fake)
		t.Cleanup(s.close)
		s.selectedThreadID = leadID

		tt := ui.NewTester(func(c *ui.Context) {
			renderSwarmCapsuleOpenFloatButton(c, s, leadID, bee, key, nil)
		}, 500, 260)
		tt.Frame()

		if _, ok := tt.AccessByRole(ui.RoleButton, "放弃改动"); !ok {
			t.Fatalf("PendingMerge=true 时应渲染放弃改动按钮，当前元素: %+v", tt.AccessAll())
		}
		if _, ok := tt.AccessByRole(ui.RoleButton, "停止"); ok {
			t.Fatal("completed 状态不应渲染停止按钮")
		}

		if err := tt.Click("放弃改动"); err != nil {
			t.Fatalf("点击放弃改动按钮失败: %v", err)
		}
		waitFor(t, tt, func() bool { return tt.HasText("放弃这些改动？") })
		if calls := fake.DiscardMergeCalls(); len(calls) != 0 {
			t.Fatalf("二次确认前不应调用 DiscardSwarmMerge，实际: %v", calls)
		}

		if err := tt.Click("放弃改动"); err != nil {
			t.Fatalf("点击二次确认的放弃改动失败: %v", err)
		}
		waitFor(t, tt, func() bool {
			calls := fake.DiscardMergeCalls()
			return len(calls) == 1 && calls[0] == "bee-pending"
		})
	})

	t.Run("completed without pending merge shows neither", func(t *testing.T) {
		fake := NewFakeClient()
		bee := SwarmBee{ThreadID: "bee-done", Name: "Cedar", Status: "completed", PendingMerge: false}
		s := newChatState(fake)
		t.Cleanup(s.close)
		s.selectedThreadID = leadID

		tt := ui.NewTester(func(c *ui.Context) {
			renderSwarmCapsuleOpenFloatButton(c, s, leadID, bee, "cap-done", nil)
		}, 500, 200)
		tt.Frame()

		if _, ok := tt.AccessByRole(ui.RoleButton, "停止"); ok {
			t.Fatal("completed 且 PendingMerge=false 不应渲染停止按钮")
		}
		if _, ok := tt.AccessByRole(ui.RoleButton, "放弃改动"); ok {
			t.Fatal("completed 且 PendingMerge=false 不应渲染放弃改动按钮")
		}
	})
}
