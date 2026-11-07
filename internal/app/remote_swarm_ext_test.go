package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
	"mcode/internal/swarm"
)

const (
	remoteSwarmReportTestLead      = "11111111-1111-1111-1111-111111111111"
	remoteSwarmReportTestBee       = "22222222-2222-2222-2222-222222222222"
	remoteSwarmReportTestOtherLead = "33333333-3333-3333-3333-333333333333"
)

// newRemoteSwarmReportEnv 造一个带汇报存档的编排服务：主会话下有一只已结束的 bee，
// 存档里记着 bee 名字与完整汇报，覆盖 threads.swarm.report 的成功与失败分支。
func newRemoteSwarmReportEnv(t *testing.T) (remoteSwarmExt, *store.Store) {
	t.Helper()
	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	t.Cleanup(hub.Close)
	backend := remote.NewMemoryBackend(hub)
	t.Cleanup(backend.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "swarm-report.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := backend.AttachStore(context.Background(), st); err != nil {
		t.Fatalf("AttachStore: %v", err)
	}

	mgr := swarm.NewManager(swarm.Options{Persister: staticLeadPersister{records: []swarm.LeadRecord{{
		LeadThreadID: remoteSwarmReportTestLead,
		Enabled:      true,
		Bees:         []swarm.Bee{{ThreadID: remoteSwarmReportTestBee, Name: "Aspen", Status: swarm.StatusCompleted}},
	}}}})
	if err := mgr.Restore(context.Background()); err != nil {
		t.Fatalf("restore swarm: %v", err)
	}

	client := NewClient(backend)
	// 直接挂管理器，避免 SetSwarmManager 顺带启动后台循环。
	client.swarm = mgr
	return remoteSwarmExt{client: client}, st
}

// remoteSwarmReportExtError 断言错误是扩展方法的 ExtError，便于按错误码判定。
func remoteSwarmReportExtError(t *testing.T, err error) *remote.ExtError {
	t.Helper()
	var extErr *remote.ExtError
	if !errors.As(err, &extErr) {
		t.Fatalf("expected *remote.ExtError, got %v", err)
	}
	return extErr
}

// 成功路径：返回存档全文与 bee 名字，truncated 恒为 false。
func TestRemoteSwarmExtReportFull(t *testing.T) {
	svc, st := newRemoteSwarmReportEnv(t)
	full := "## 结论\n- 完成 A\n- 完成 B"
	if err := st.SaveSwarmReport(context.Background(), store.SwarmReport{
		BeeID:        remoteSwarmReportTestBee,
		LeadThreadID: remoteSwarmReportTestLead,
		Name:         "Aspen",
		Summary:      full,
	}); err != nil {
		t.Fatalf("SaveSwarmReport: %v", err)
	}

	res, err := svc.ReportFull(context.Background(), remoteSwarmReportTestLead, remoteSwarmReportTestBee)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Text != full {
		t.Fatalf("text = %q, want %q", res.Text, full)
	}
	if res.Name != "Aspen" || res.BeeID != remoteSwarmReportTestBee || res.Truncated {
		t.Fatalf("unexpected result: %+v", res)
	}
}

// bee 属于别的主会话时按参数错误拒绝，避免跨会话读存档。
func TestRemoteSwarmExtReportFullRejectsForeignBee(t *testing.T) {
	svc, st := newRemoteSwarmReportEnv(t)
	if err := st.SaveSwarmReport(context.Background(), store.SwarmReport{
		BeeID:        remoteSwarmReportTestBee,
		LeadThreadID: remoteSwarmReportTestOtherLead,
		Summary:      "别人的汇报",
	}); err != nil {
		t.Fatalf("SaveSwarmReport: %v", err)
	}

	_, err := svc.ReportFull(context.Background(), remoteSwarmReportTestLead, remoteSwarmReportTestBee)
	extErr := remoteSwarmReportExtError(t, err)
	if extErr.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams, got %+v", extErr)
	}
}

// 存档缺失时给出稳定的 ResourceNotFound 错误，前端据此提示而不是渲染空白。
func TestRemoteSwarmExtReportFullArchiveMissing(t *testing.T) {
	svc, _ := newRemoteSwarmReportEnv(t)

	_, err := svc.ReportFull(context.Background(), remoteSwarmReportTestLead, remoteSwarmReportTestBee)
	extErr := remoteSwarmReportExtError(t, err)
	if extErr.Code != wire.ErrCodeResourceNotFound {
		t.Fatalf("expected ResourceNotFound, got %+v", extErr)
	}
	if extErr.Message != "找不到这条子代理的完整汇报" {
		t.Fatalf("unexpected message: %q", extErr.Message)
	}
}

// 完全未知的 bee 同样按参数错误拒绝：既不在主会话的 bee 列表里，也没有存档。
func TestRemoteSwarmExtReportFullUnknownBee(t *testing.T) {
	svc, _ := newRemoteSwarmReportEnv(t)

	_, err := svc.ReportFull(context.Background(), remoteSwarmReportTestLead, remoteSwarmReportTestOtherLead)
	extErr := remoteSwarmReportExtError(t, err)
	if extErr.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams, got %+v", extErr)
	}
}
