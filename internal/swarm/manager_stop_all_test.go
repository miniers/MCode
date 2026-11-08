package swarm

import (
	"context"
	"testing"

	"mcode/internal/kanban"
)

func TestStopAllSuppressesReportsAndIdle(t *testing.T) {
	ctx := context.Background()
	runner, _ := initTestRepo(t)
	driver := newFakeDriver()
	reports, idle := 0, 0
	mgr := NewManager(Options{
		Git:        runner,
		Driver:     driver,
		OnReport:   func(string, string) error { reports++; return nil },
		OnBeesIdle: func(context.Context, string) { idle++ },
	})
	leadID := "lead-stop-all-silent"
	mgr.leads[leadID] = &leadState{leadThreadID: leadID, bees: []Bee{{ThreadID: "bee-running", Status: StatusRunning}}}

	if err := mgr.StopAll(ctx, leadID); err != nil {
		t.Fatal(err)
	}
	mgr.Tick(ctx)
	bee := mgr.State(leadID).Bees[0]
	if bee.Status != StatusStopped || !bee.Reported {
		t.Fatalf("停止的 bee 应标记为已汇报: %+v", bee)
	}
	if reports != 0 || idle != 0 || mgr.Busy(leadID) {
		t.Fatalf("停止后不应汇报、回调空闲或保持忙态: reports=%d idle=%d busy=%v", reports, idle, mgr.Busy(leadID))
	}
}

func TestStopAllHoldsCompletedReportUntilLeadRuns(t *testing.T) {
	ctx := context.Background()
	runner, _ := initTestRepo(t)
	driver := newFakeDriver()
	reports, idle := 0, 0
	mgr := NewManager(Options{
		Git:        runner,
		Driver:     driver,
		OnReport:   func(string, string) error { reports++; return nil },
		OnBeesIdle: func(context.Context, string) { idle++ },
	})
	leadID := "lead-stop-all-held"
	mgr.leads[leadID] = &leadState{leadThreadID: leadID, bees: []Bee{
		{ThreadID: "bee-completed", Index: 0, Status: StatusCompleted},
		{ThreadID: "bee-running", Index: 1, Status: StatusRunning},
	}}
	driver.setSessionState(leadID, kanban.SessionState{})

	if err := mgr.StopAll(ctx, leadID); err != nil {
		t.Fatal(err)
	}
	mgr.Tick(ctx)
	if reports != 0 || idle != 0 || mgr.State(leadID).Bees[0].Reported || mgr.Busy(leadID) {
		t.Fatalf("停止后已完成的 bee 应暂扣: reports=%d idle=%d busy=%v", reports, idle, mgr.Busy(leadID))
	}
	driver.setSessionState(leadID, kanban.SessionState{Running: true})
	mgr.Tick(ctx)
	if mgr.leads[leadID].stoppedByUser || reports != 0 {
		t.Fatalf("主会话运行时应解除暂扣但尚不汇报: stopped=%v reports=%d", mgr.leads[leadID].stoppedByUser, reports)
	}
	driver.setSessionState(leadID, kanban.SessionState{})
	mgr.Tick(ctx)
	if reports != 1 || !mgr.State(leadID).Bees[0].Reported {
		t.Fatalf("主会话空闲后应汇报已完成的 bee: reports=%d", reports)
	}
}

func TestStopAllHoldsUnreportedCompletedBee(t *testing.T) {
	ctx := context.Background()
	runner, _ := initTestRepo(t)
	driver := newFakeDriver()
	reports, idle := 0, 0
	mgr := NewManager(Options{
		Git:        runner,
		Driver:     driver,
		OnReport:   func(string, string) error { reports++; return nil },
		OnBeesIdle: func(context.Context, string) { idle++ },
	})
	leadID := "lead-stop-all-completed"
	mgr.leads[leadID] = &leadState{leadThreadID: leadID, bees: []Bee{{ThreadID: "bee-completed", Status: StatusCompleted}}}
	driver.setSessionState(leadID, kanban.SessionState{})

	if err := mgr.StopAll(ctx, leadID); err != nil {
		t.Fatal(err)
	}
	mgr.Tick(ctx)
	if reports != 0 || idle != 0 || mgr.State(leadID).Bees[0].Reported || mgr.Busy(leadID) {
		t.Fatalf("停止后应暂扣汇报且不保持忙态: reports=%d idle=%d busy=%v", reports, idle, mgr.Busy(leadID))
	}
	driver.setSessionState(leadID, kanban.SessionState{Running: true})
	mgr.Tick(ctx)
	if mgr.leads[leadID].stoppedByUser {
		t.Fatal("主会话运行时应解除暂扣")
	}
}

func TestStopAllNewDispatchResumesReports(t *testing.T) {
	ctx := context.Background()
	runner, dir := initTestRepo(t)
	driver := newFakeDriver()
	mgr := NewManager(Options{Git: runner, Driver: driver, WorktreeDir: t.TempDir()})
	leadID := "lead-stop-all-dispatch"
	mgr.leads[leadID] = &leadState{leadThreadID: leadID, bees: []Bee{{ThreadID: "bee-running", Status: StatusRunning}}}
	if err := mgr.StopAll(ctx, leadID); err != nil {
		t.Fatal(err)
	}
	if !mgr.leads[leadID].stoppedByUser {
		t.Fatal("停止全部后应暂扣汇报")
	}
	_, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees:         []BeeSpec{{Name: "Bee-1", Task: "T1", Prompt: "P1"}},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if mgr.leads[leadID].stoppedByUser {
		t.Fatal("派发新 bee 后应解除暂扣")
	}
}
