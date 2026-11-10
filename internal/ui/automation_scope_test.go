package mcodeui

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestAutomationScopeEditProjectBinding(t *testing.T) {
	as := &automationState{projectID: ""}
	as.fillAutomation(Automation{ID: "a", ProjectID: "p2"})
	if as.projectID != "" || as.editorProjectID != "p2" {
		t.Fatalf("scope = %q, editor project = %q; want empty scope and p2", as.projectID, as.editorProjectID)
	}
}

type automationScopeProbe struct {
	*FakeClient
	listScopes []string
	counts     []string
}

func (c *automationScopeProbe) Automations(ctx context.Context, scope string) ([]Automation, error) {
	c.listScopes = append(c.listScopes, scope)
	return c.FakeClient.Automations(ctx, scope)
}

func (c *automationScopeProbe) AutomationUnreadCounts(_ context.Context, scope string) (map[string]int, error) {
	c.counts = append(c.counts, scope)
	return map[string]int{}, nil
}

func (*automationScopeProbe) AutomationRunsState(context.Context, string, int, bool) (AutomationRunsState, error) {
	return AutomationRunsState{}, nil
}
func (*automationScopeProbe) MarkAutomationRunRead(context.Context, string) error { return nil }
func (*automationScopeProbe) MarkAllAutomationRunsRead(context.Context, string) (int, error) {
	return 0, nil
}
func (*automationScopeProbe) SetAutomationRunArchived(context.Context, string, bool) error {
	return nil
}

func TestAutomationScopeAllProjects(t *testing.T) {
	client := &automationScopeProbe{FakeClient: NewFakeClient()}
	s := &chatState{client: client, projects: []Project{{ID: "p1"}}}
	as := getAutomationState(s)
	as.projectID, as.initialized = "", true
	s.loadAutomationList("", true)
	testUI := ui.NewTester(func(c *ui.Context) { s.applyWorkspaceUpdates() }, 800, 600)
	waitFor(t, testUI, func() bool { return len(client.listScopes) > 0 })
	s.loadAutomationUnread("")
	waitFor(t, testUI, func() bool { return as.unreadKnown })
	if len(client.listScopes) == 0 || client.listScopes[0] != "" || len(client.counts) == 0 || client.counts[0] != "" {
		t.Fatalf("未使用全项目范围: lists=%v counts=%v", client.listScopes, client.counts)
	}
	if count, known := automationScopeUnread(s); !known || count != 0 {
		t.Fatalf("全范围未读 = %d, known=%v", count, known)
	}
}

func TestAutomationScopeLateResult(t *testing.T) {
	client := &delayedScopeListClient{FakeClient: NewFakeClient(), started: make(chan string, 2), release: make(chan struct{})}
	client.SetAutomations([]Automation{testAutomation("p2-item", "p2")})
	s := &chatState{client: client, projects: []Project{{ID: "p1"}, {ID: "p2"}}}
	scope := "p1"
	tt := ui.NewTester(func(c *ui.Context) {
		s.applyWorkspaceUpdates()
		renderAutomationWorkspaceInScope(c, s, automationWorkspaceProps{ScopeID: scope})
	}, 800, 600)
	tt.Frame()
	select {
	case <-client.started:
	case <-time.After(5 * time.Second):
		t.Fatal("单项目请求未启动")
	}
	as := getAutomationState(s)
	scope = ""
	tt.Frame()
	select {
	case <-client.started:
	case <-time.After(5 * time.Second):
		t.Fatal("全部项目请求未启动")
	}
	close(client.release)
	waitFor(t, tt, func() bool { return as.projectID == "" && !as.loading })
	if as.projectID != "" || len(as.autos) != 1 || as.autos[0].ID != "p2-item" {
		t.Fatal("迟到的单项目响应覆盖了全部项目结果")
	}
}

type delayedScopeListClient struct {
	*FakeClient
	started chan string
	release chan struct{}
	mu      sync.Mutex
	first   bool
}

func (c *delayedScopeListClient) Automations(ctx context.Context, scope string) ([]Automation, error) {
	c.mu.Lock()
	delayed := !c.first
	c.first = true
	c.mu.Unlock()
	c.started <- scope
	if delayed {
		select {
		case <-c.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return c.FakeClient.Automations(ctx, scope)
}
