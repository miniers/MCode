package mcodeui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type queuedAutomationRunsClient struct {
	*automationRunsTestClient
	result AutomationRunsState
	err    error
}

func (c *queuedAutomationRunsClient) AutomationRunsState(context.Context, string, int, bool) (AutomationRunsState, error) {
	return c.result, c.err
}

func TestAutomationRunsApplyQueuedResult(t *testing.T) {
	for _, name := range []string{"success", "error", "selection", "project", "request"} {
		t.Run(name, func(t *testing.T) {
			readAt := time.Now().UTC()
			client := &queuedAutomationRunsClient{
				automationRunsTestClient: &automationRunsTestClient{FakeClient: NewFakeClient()},
				result: AutomationRunsState{
					Runs:   []AutomationRun{{ID: "new"}},
					States: []AutomationRunReadState{{RunID: "new", ReadAt: readAt, Archived: true}},
				},
			}
			if name == "error" {
				client.err = errors.New("读取失败")
			}
			s := &chatState{client: client}
			as := getAutomationState(s)
			as.projectID, as.selectedID = "p1", "auto-1"
			as.runs = []AutomationRun{{ID: "old"}}
			as.runRead = map[string]time.Time{"old": readAt}
			as.runArchived = map[string]bool{"old": true}
			s.loadAutomationRuns("auto-1")
			waitAutomationRunsQueued(t, s)
			if !as.runsLoading || as.runs[0].ID != "old" || as.runsErr != "" || !as.runRead["old"].Equal(readAt) || !as.runArchived["old"] {
				t.Fatal("后台请求在 UI 应用前修改了运行记录")
			}

			// 排队后改变状态，回调必须按应用时的状态判断是否过期并保留展开项。
			switch name {
			case "success":
				as.runs = []AutomationRun{{ID: "opened"}}
				as.openRunID = "opened"
			case "selection":
				as.selectedID = "auto-2"
			case "project":
				as.projectID = "p2"
			case "request":
				as.runsLoadID++
			}
			s.applyWorkspaceUpdates()
			switch name {
			case "success":
				if as.runsLoading || len(as.runs) != 2 || as.runs[0].ID != "opened" || as.runs[1].ID != "new" || !as.runRead["new"].Equal(readAt) || !as.runArchived["new"] {
					t.Fatal("运行结果未应用或丢失当前展开项")
				}
			case "error":
				if as.runsLoading || as.runsErr != "读取失败" || as.runs != nil || as.runRead != nil || as.runArchived != nil {
					t.Fatal("读取失败没有清理运行记录状态")
				}
			default:
				if !as.runsLoading || as.runs[0].ID != "old" || !as.runRead["old"].Equal(readAt) || !as.runArchived["old"] {
					t.Fatal("过期运行记录覆盖了当前状态")
				}
			}
		})
	}
}

func TestAutomationRunNowAppliesQueuedResult(t *testing.T) {
	for _, name := range []string{"success", "error", "passive", "selection", "project", "version", "request", "late-error"} {
		t.Run(name, func(t *testing.T) {
			client := NewFakeClient()
			if name == "passive" {
				client.SetAutomationRunError(ErrAutomationSchedulerPassive)
			} else if name == "error" || name == "late-error" {
				client.SetAutomationRunError(errors.New("运行被拒绝"))
			}
			s := &chatState{client: client}
			as := getAutomationState(s)
			as.projectID, as.selectedID = "p1", "auto-1"
			s.runAutomationNow("auto-1")
			waitAutomationRunsQueued(t, s)
			s.mu.Lock()
			toast := s.toastMessage
			s.mu.Unlock()
			if !as.runningIDs["auto-1"] || len(as.runs) != 0 || as.openRunID != "" || toast != "" {
				t.Fatal("后台请求在 UI 应用前修改了立即运行状态")
			}
			switch name {
			case "selection", "late-error":
				as.selectedID = "auto-2"
			case "project":
				as.projectID = "p2"
			case "version":
				as.selectionVersion++
			case "request":
				as.runRequest++
			case "passive":
				as.schedulerStatus = AutomationSchedulerStatus{State: AutomationSchedulerPassiveError, Error: "最新调度错误"}
			}
			s.applyWorkspaceUpdates()
			s.mu.Lock()
			toast = s.toastMessage
			s.mu.Unlock()
			if as.runningIDs["auto-1"] != (name == "project") {
				t.Fatal("立即运行的等待状态不符合原有过期规则")
			}
			switch name {
			case "success":
				if as.openRunID != "run-1" || len(as.runs) != 1 || as.runs[0].ID != "run-1" {
					t.Fatal("立即运行结果未打开")
				}
				waitAutomationRunsUpdate(t, s, func() bool { return !as.runsLoading })
			case "error":
				if !strings.Contains(toast, "运行被拒绝") {
					t.Fatal("缺少运行错误提示")
				}
			case "passive":
				if !strings.Contains(toast, "最新调度错误") {
					t.Fatal("没有按应用时的调度状态生成提示")
				}
			default:
				if as.openRunID != "" || len(as.runs) != 0 || toast != "" || as.runsLoading {
					t.Fatal("过期立即运行结果改变了详情或错误提示")
				}
			}
		})
	}
}
