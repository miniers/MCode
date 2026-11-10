package mcodeui

import (
	"context"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

type motionPreviewClient struct {
	*FakeClient
	started  chan struct{}
	release  chan struct{}
	finished chan struct{}
}

func (c *motionPreviewClient) PreviewAutomationSchedule(ctx context.Context, _ AutomationSchedule, _ time.Time, _ int) (AutomationSchedulePreview, error) {
	close(c.started)
	<-c.release
	defer close(c.finished)
	return AutomationSchedulePreview{Times: []time.Time{time.Now()}}, nil
}

func TestAutomationClosedStopsWork(t *testing.T) {
	for _, inflight := range []bool{false, true} {
		t.Run(map[bool]string{false: "debounce", true: "inflight"}[inflight], func(t *testing.T) {
			client := &motionPreviewClient{FakeClient: NewFakeClient(), started: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
			s := &chatState{client: client}
			as := getAutomationState(s)
			as.scheduleType, as.intervalInput, as.intervalUnit = "interval", "1", "minutes"
			as.updateSchedulePreview(s)
			if inflight {
				select {
				case <-client.started:
				case <-time.After(3 * time.Second):
					t.Fatal("预览未启动")
				}
			}
			stopAutomationWatch(s)
			close(client.release)
			if inflight {
				select {
				case <-client.finished:
				case <-time.After(3 * time.Second):
					t.Fatal("预览未结束")
				}
			} else {
				select {
				case <-client.started:
					t.Fatal("关闭后发出预览请求")
				case <-time.After(300 * time.Millisecond):
				}
			}
			time.Sleep(30 * time.Millisecond)
			ws := getWorkspaceState(s)
			workspaceMu.Lock()
			queued := len(ws.updates)
			workspaceMu.Unlock()
			if queued != 0 || as.previewVisible {
				t.Fatal("关闭后预览仍更新界面")
			}
		})
	}
}

func TestAutomationMotionReduceMotion(t *testing.T) {
	for _, reduced := range []bool{true, false} {
		t.Run(map[bool]string{true: "reduced", false: "normal"}[reduced], func(t *testing.T) {
			s := &chatState{client: NewFakeClient()}
			as := getAutomationState(s)
			runs := []AutomationRun{{ID: "r1", Status: "running", StartedAt: time.Now()}}
			tab := 0
			open := ""
			tt := ui.NewTesterWith(func(c *ui.Context) {
				if tab == 0 {
					LoadingIndicator(c, "automation-motion-loading", "运行中", IconSM)
				} else {
					renderAutomationRunHistory(c, s, as, false, "a", 0, false, false, "", runs, nil, nil, open)
				}
			}, 1000, 800, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: reduced}})
			tt.Frame()
			if !tt.HasText("运行中") {
				t.Fatal("运行状态不可见")
			}
			if reduced && tt.FramePending() {
				t.Fatalf("加载续帧状态错误：%v", tt.FramePending())
			}
			tab = 1
			tt.Frame()
			open = "r1"
			tt.Frame()
			if !reduced && !tt.FramePending() {
				t.Fatal("普通模式切换详情应有动效")
			}
			tt.Frame()
			if !tt.HasText("运行中") {
				t.Fatal("详情状态不可见")
			}
			if reduced && tt.FramePending() {
				t.Fatal("减少动效时切换后仍续帧")
			}
		})
	}
}
