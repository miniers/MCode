package mcodeui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

func TestCustomProviderName(t *testing.T) {
	testenv.TempHome(t)

	newState := func(t *testing.T) *chatState {
		t.Helper()
		s := newChatState(NewFakeClient())
		t.Cleanup(s.close)
		s.mu.Lock()
		s.providers = []ProviderInfo{{ID: "custom-432db46a", Name: "我的网关"}}
		s.mu.Unlock()
		return s
	}

	assertGatewayShown := func(t *testing.T, tt *ui.Tester, wantExact string) {
		t.Helper()
		if !tt.HasText("我的网关") {
			t.Fatalf("期望界面包含「我的网关」，实际文本: %q", tt.Texts())
		}
		if !tt.HasText(wantExact) {
			t.Fatalf("期望界面包含 %q，实际文本: %q", wantExact, tt.Texts())
		}
		if tt.HasText("custom-432db46a") {
			t.Fatalf("界面不应包含原始 ID custom-432db46a，实际文本: %q", tt.Texts())
		}
		for _, txt := range tt.Texts() {
			if strings.Contains(txt, "custom-432db46a") {
				t.Fatalf("界面文本 %q 不应包含 custom-432db46a", txt)
			}
		}
	}

	t.Run("SwarmCapsuleHeader", func(t *testing.T) {
		s := newState(t)
		bee := SwarmBee{
			ThreadID: "bee-custom-1",
			Index:    1,
			Name:     "Worker",
			Status:   "completed",
			Provider: "custom-432db46a",
			Model:    "claude-sonnet-4",
		}
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c).Width(420).Children(func() {
				renderSwarmCapsuleHeader(c, s, bee, "lead|bee-custom-1")
			})
		}, 480, 320)
		tt.Frame()
		assertGatewayShown(t, tt, "我的网关/claude-sonnet-4")
	})

	t.Run("SidebarHoverCardBody", func(t *testing.T) {
		s := newState(t)
		th := Thread{
			ID:       "th-custom-1",
			Title:    "自定义端点测试",
			Provider: "custom-432db46a",
			Model:    "claude-sonnet-4",
		}
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c).Width(sidebarHoverCardWidth).Children(func() {
				renderSidebarHoverCardBody(c, s, th)
			})
		}, 400, 600)
		tt.Frame()
		assertGatewayShown(t, tt, "我的网关 · claude-sonnet-4")
	})

	t.Run("SwarmBeeRow", func(t *testing.T) {
		s := newState(t)
		bee := SwarmBee{
			ThreadID: "bee-custom-2",
			Index:    2,
			Name:     "RowWorker",
			Status:   "completed",
			Provider: "custom-432db46a",
			Model:    "claude-sonnet-4",
		}
		tt := ui.NewTester(func(c *ui.Context) {
			renderSwarmBeeRow(c, s, bee, 0)
		}, 800, 400)
		tt.Frame()
		assertGatewayShown(t, tt, "我的网关 / claude-sonnet-4")
	})
}
