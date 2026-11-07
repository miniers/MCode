package remote

import (
	"context"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

// hostToolsHarness 是声明了宿主工具能力的假 harness：available 为假表示该 provider
// 的会话没有接入 mcode 的 MCP 工具（例如 omp）。
type hostToolsHarness struct {
	*mockHarness
	available bool
}

var _ harness.HostToolsReporter = (*hostToolsHarness)(nil)

func (h *hostToolsHarness) HostToolsAvailable() bool { return h.available }

// TestSessionState_HostToolsUnavailable 验证 SessionState 按线程实际使用的 provider 查 harness：
// 声明没有宿主工具的 provider 返回 HostToolsUnavailable=true，普通 harness 保持 false。
func TestSessionState_HostToolsUnavailable(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()

	// omp 不接 MCP：会话调不到 automation_report_result、kanban_submit_result 等回执工具。
	b.RegisterHarness(&hostToolsHarness{mockHarness: &mockHarness{kind: "omp"}, available: false})
	// 未声明该能力的 harness 按「有宿主工具」处理，保持原有的追问行为。
	b.RegisterHarness(&mockHarness{kind: "claude"})
	b.AddProject(wire.ProjectSummary{ID: "p1", Path: t.TempDir()})

	b.AddThread(wire.ThreadSummary{ID: "thread-no-host-tools", ProjectID: "p1", Title: "无宿主工具", Provider: "omp"}, nil)
	b.AddThread(wire.ThreadSummary{ID: "thread-with-host-tools", ProjectID: "p1", Title: "有宿主工具", Provider: "claude"}, nil)

	st, err := b.SessionState(ctx, "thread-no-host-tools")
	if err != nil {
		t.Fatalf("SessionState 失败: %v", err)
	}
	if !st.HostToolsUnavailable {
		t.Fatal("omp 会话的 HostToolsUnavailable 应为 true")
	}

	st, err = b.SessionState(ctx, "thread-with-host-tools")
	if err != nil {
		t.Fatalf("SessionState 失败: %v", err)
	}
	if st.HostToolsUnavailable {
		t.Fatal("未声明能力的普通 harness 不应标记 HostToolsUnavailable")
	}
}
