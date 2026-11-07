package swarm

import (
	"context"
	"testing"

	"mcode/internal/kanban"
)

// lead 被用户中断、汇报暂扣时，未汇报的 bee 不再算忙，否则 web 端会一直显示运行中；
// 用户下一句话之后 lead 正常跑完，汇报照旧投递并标记已汇报。
func TestBusyIgnoresUnreportedBeesWhenLeadInterrupted(t *testing.T) {
	ctx := context.Background()
	driver := newFakeDriver()
	const leadID = "lead-interrupted"
	reports := 0
	mgr := NewManager(Options{
		Driver: driver,
		OnReport: func(leadThreadID, message string) error {
			reports++
			return nil
		},
	})
	mgr.mu.Lock()
	mgr.leads[leadID] = &leadState{
		leadThreadID: leadID,
		enabled:      true,
		bees: []Bee{{
			ThreadID: "bee-stopped",
			Name:     "Aspen",
			Task:     "改文件",
			Status:   StatusStopped,
		}},
	}
	mgr.mu.Unlock()

	driver.setSessionState(leadID, kanban.SessionState{Interrupted: true})

	if !mgr.Busy(leadID) {
		t.Fatal("checkReports 之前未汇报的 bee 应算忙")
	}
	mgr.checkReports(ctx)
	if mgr.Busy(leadID) {
		t.Fatal("lead 被中断、汇报暂扣时不应算忙")
	}
	if reports != 0 {
		t.Fatalf("暂扣期间不应投递汇报，实际 %d 次", reports)
	}

	// 用户说了下一句话，lead 这一轮正常跑完，暂扣解除。
	driver.setSessionState(leadID, kanban.SessionState{})
	mgr.checkReports(ctx)
	if reports != 1 {
		t.Fatalf("放行后应投递一次汇报，实际 %d 次", reports)
	}
	if !mgr.State(leadID).Bees[0].Reported {
		t.Fatal("投递后 bee 应标记已汇报")
	}
	if mgr.Busy(leadID) {
		t.Fatal("汇报投递后不应算忙")
	}
}
