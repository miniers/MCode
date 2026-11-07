package mcodeui

import "testing"

// TestSwarmProgressTextMixedStatuses 验证编排进度把失败也算作已结束，并单独追加失败个数：
// 2 个完成、1 个失败、1 个运行中应显示「3/4 已结束，1 个失败」。
func TestSwarmProgressTextMixedStatuses(t *testing.T) {
	st := SwarmState{Bees: []SwarmBee{
		{ThreadID: "h-done-1", Status: "completed"},
		{ThreadID: "h-done-2", Status: "completed"},
		{ThreadID: "h-failed", Status: "failed"},
		{ThreadID: "h-running", Status: "running"},
	}}
	if got, want := swarmProgressText(st), "3/4 已结束，1 个失败"; got != want {
		t.Fatalf("swarmProgressText() = %q, want %q", got, want)
	}

	// 有排队时排队文案保持原样，失败个数排在它前面。
	st.Queued = 2
	if got, want := swarmProgressText(st), "3/4 已结束，1 个失败，2 个排队中"; got != want {
		t.Fatalf("swarmProgressText() = %q, want %q", got, want)
	}
}

// TestSwarmProgressTextStoppedIsNotFailure 验证停止的子线程只算已结束，不追加失败文案。
func TestSwarmProgressTextStoppedIsNotFailure(t *testing.T) {
	st := SwarmState{Bees: []SwarmBee{
		{ThreadID: "h-stopped", Status: "stopped"},
		{ThreadID: "h-running", Status: "running"},
	}}
	if got, want := swarmProgressText(st), "1/2 已结束"; got != want {
		t.Fatalf("swarmProgressText() = %q, want %q", got, want)
	}
}
