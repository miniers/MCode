package mcodeui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func historyVisualTester(runs []AutomationRun) (*ui.Tester, *chatState, *automationState, *automationRunsTestClient) {
	f := &automationRunsTestClient{FakeClient: NewFakeClient(), runs: runs}
	s := &chatState{client: f}
	as := getAutomationState(s)
	as.projectID, as.selectedID, as.runRead, as.runs = "p1", "auto-1", map[string]time.Time{}, runs
	tt := ui.NewTesterWith(func(c *ui.Context) {
		s.applyWorkspaceUpdates()
		automationMu.Lock()
		open := as.openRunID
		reads := make(map[string]time.Time, len(as.runRead))
		for id, at := range as.runRead {
			reads[id] = at
		}
		automationMu.Unlock()
		ui.Scroll(c).Fill().Children(func() {
			renderAutomationRunHistory(c, s, as, false, "auto-1", 1, false, false, "", runs, reads, nil, open)
		})
	}, 620, 800, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	return tt, s, as, f
}

func TestAutomationHistoryVisualNarrowRows(t *testing.T) {
	now := time.Now()
	long := strings.Repeat("longerror", 200)
	runs := []AutomationRun{{ID: "failed", Status: "failed", Error: long, StartedAt: now, FinishedAt: now}, {ID: "running", Status: "running", Summary: long, StartedAt: now.AddDate(0, 0, -1)}}
	tt, _, _, _ := historyVisualTester(runs)
	waitFor(t, tt, func() bool {
		return tt.HasText("今天") && tt.HasText("昨天") && tt.HasText("0s") && tt.HasText("—")
	})
	for _, label := range []string{"运行记录 failed", "运行记录 running", long} {
		if r, ok := tt.Find(label); !ok || r.X+r.W > 620 {
			t.Fatalf("%s 越界或缺失: %+v", label[:min(len(label), 20)], r)
		}
	}
	if err := tt.Click("运行记录 failed"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("复制错误") })
	if r, ok := tt.Find(long); !ok || r.X+r.W > 620 {
		t.Fatalf("错误详情越界: %+v", r)
	}
}

func TestAutomationHistoryVisualEmptyAndLimit(t *testing.T) {
	tt, _, _, _ := historyVisualTester(nil)
	waitFor(t, tt, func() bool { return tt.HasText("暂无运行记录") })
	runs := make([]AutomationRun, 101)
	for i := range runs {
		runs[i] = AutomationRun{ID: fmt.Sprint(i), Status: "succeeded", StartedAt: time.Now().Add(-time.Duration(i) * time.Minute)}
	}
	tt, _, _, _ = historyVisualTester(runs)
	waitFor(t, tt, func() bool { return tt.HasText("仅显示最近 100 条") })
	if _, ok := tt.Find("运行记录 100"); ok {
		t.Fatal("超过 100 条")
	}
	if err := tt.Click("筛选运行记录"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("筛选失败"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("最近 100 条中无匹配") })
}

func TestAutomationHistoryKeyboardOpen(t *testing.T) {
	tt, _, as, f := historyVisualTester([]AutomationRun{{ID: "r1", Status: "failed", StartedAt: time.Now()}})
	tt.Frame()
	for i := 0; i < 8 && !tt.Focused("运行记录 r1"); i++ {
		tt.Key(0, ui.KeyTab)
	}
	if !tt.Focused("运行记录 r1") {
		t.Fatal("运行行无法聚焦")
	}
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool { return as.openRunID == "r1" })
	waitFor(t, tt, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.reads) == 1 })
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reads) != 1 || f.reads[0] != "r1" {
		t.Fatal(f.reads)
	}
}

func TestAutomationHistoryClickMarksReadOnce(t *testing.T) {
	tt, _, _, f := historyVisualTester([]AutomationRun{{ID: "r1", Status: "succeeded", StartedAt: time.Now()}})
	tt.Frame()
	if err := tt.Click("运行记录 r1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.reads) > 0 })
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reads) != 1 {
		t.Fatal(f.reads)
	}
}
