package mcodeui

import (
	"context"
	"reflect"
	"testing"
	"testing/synctest"
)

// approvalsRefetchClient 只控制审批拉取与订阅交界，其余行为复用 FakeClient。
type approvalsRefetchClient struct {
	Client
	approvals   func(context.Context, string) ([]Approval, error)
	beforeWatch func(string)
}

func (c *approvalsRefetchClient) Approvals(ctx context.Context, threadID string) ([]Approval, error) {
	return c.approvals(ctx, threadID)
}

func (c *approvalsRefetchClient) Watch(threadID string, onUpdate func(ThreadUpdate), onApprovals func([]Approval)) func() {
	if c.beforeWatch != nil {
		c.beforeWatch(threadID)
	}
	return c.Client.Watch(threadID, onUpdate, onApprovals)
}

func TestSelectThreadApprovalsRefetch(t *testing.T) {
	for _, scenario := range []string{"watch_gap", "push_during_refetch", "switch_during_refetch"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				fake := NewFakeClient()
				old := []Approval{{ThreadID: "t1", RequestID: "old"}}
				latest := []Approval{{ThreadID: "t1", RequestID: "latest"}}
				other := []Approval{{ThreadID: "t2", RequestID: "other"}}
				fake.TriggerApprovals("t1", old)
				fake.TriggerApprovals("t2", other)

				started := make(chan struct{})
				release := make(chan struct{})
				calls := 0
				client := &approvalsRefetchClient{Client: fake}
				client.approvals = func(ctx context.Context, threadID string) ([]Approval, error) {
					result, err := fake.Approvals(ctx, threadID)
					if threadID == "t1" {
						calls++
						if calls == 2 {
							// 固定补拉快照，等待推送或切换完成后才返回旧结果。
							close(started)
							<-release
						}
					}
					return result, err
				}
				if scenario == "watch_gap" {
					client.beforeWatch = func(threadID string) {
						// 首次拉取已经结束，但 FakeClient 尚未注册 Watch，只有补拉能看到它。
						fake.TriggerApprovals(threadID, latest)
					}
				}

				s := newChatState(client)
				defer s.close()
				// 即使中途断言失败，也要放行后台请求，避免遗留阻塞的 goroutine。
				defer func() {
					close(release)
					synctest.Wait()
				}()
				s.selectThread("t1")
				synctest.Wait()
				select {
				case <-started:
				default:
					t.Fatal("Watch 建立后未开始补拉审批")
				}

				wantThread, want := "t1", latest
				switch scenario {
				case "push_during_refetch":
					fake.TriggerApprovals("t1", latest)
				case "switch_during_refetch":
					s.selectThread("t2")
					synctest.Wait()
					wantThread, want = "t2", other
				}

				// 通过发送放行，defer 中的 close 负责失败路径；Wait 保证旧结果已处理完。
				release <- struct{}{}
				synctest.Wait()
				s.mu.Lock()
				defer s.mu.Unlock()
				if s.selectedThreadID != wantThread || !reflect.DeepEqual(s.approvals, want) {
					t.Fatalf("补拉结束后 got thread=%q approvals=%+v; want thread=%q approvals=%+v",
						s.selectedThreadID, s.approvals, wantThread, want)
				}
			})
		})
	}
}
