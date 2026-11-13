package kanban

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mcode/internal/store"
	"mcode/internal/testenv"
)

type blockingDispatchDriver struct {
	*mockDriver
	entered chan struct{}
	resume  chan struct{}
	fail    bool
}

func (d *blockingDispatchDriver) StartSession(ctx context.Context, req SessionRequest) (string, error) {
	close(d.entered)
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-d.resume:
	}
	if d.fail {
		return "", errors.New("dispatch failed")
	}
	return d.mockDriver.StartSession(ctx, req)
}

func TestBoardDuringRealDispatch(t *testing.T) {
	testenv.TempHome(t)
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			svc, _, mock := setupTestService(t)
			driver := &blockingDispatchDriver{mockDriver: mock, entered: make(chan struct{}), resume: make(chan struct{}), fail: fail}
			svc.opts.Driver = driver
			var clock atomic.Int64
			clock.Store(time.Now().UnixNano())
			svc.opts.Now = func() time.Time { return time.Unix(0, clock.Load()) }
			task, err := svc.CreateTask(context.Background(), CreateTaskInput{Title: "dispatch"})
			if err != nil {
				t.Fatal(err)
			}
			var once sync.Once
			unblock := func() { once.Do(func() { close(driver.resume) }) }
			defer unblock()
			done := make(chan error, 1)
			go func() {
				_, err := svc.StartTask(context.Background(), task.ID, task.Revision)
				done <- err
			}()
			<-driver.entered

			readBoard := func() Board {
				t.Helper()
				type result struct {
					board Board
					err   error
				}
				ch := make(chan result, 1)
				go func() {
					board, err := svc.Board(context.Background())
					ch <- result{board, err}
				}()
				select {
				case res := <-ch:
					if res.err != nil {
						t.Fatal(res.err)
					}
					return res.board
				case <-time.After(2 * time.Second):
					unblock()
					<-done
					t.Fatal("看板读取被会话启动阻塞")
					return Board{}
				}
			}
			board := readBoard()
			if got := board.Tasks[0].Derived; got.DisplayColumn != DisplayColumnInProgress || got.CanStart || got.CanMove {
				t.Fatalf("派发中的推导错误: %+v", got)
			}
			clock.Add(int64(31 * time.Second))
			if got := readBoard().Tasks[0].Derived.DisplayColumn; got != DisplayColumnTodo {
				t.Fatalf("派发超过 30s 未恢复: %s", got)
			}
			unblock()
			if err := <-done; (err != nil) != fail {
				t.Fatalf("启动结果: %v", err)
			}
			svc.optimisticMu.Lock()
			remaining := len(svc.optimistic)
			svc.optimisticMu.Unlock()
			if remaining != 0 {
				t.Fatal("派发结束后未清理标记")
			}
		})
	}
}

func TestReviewProgressStalledNotifications(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()
	svc, db, driver := setupTestService(t)
	goal := store.Goal{ID: "review-goal", ProjectID: "p", Title: "review", Status: string(GoalStatusRunning)}
	if err := db.CreateGoal(ctx, goal); err != nil {
		t.Fatal(err)
	}
	threadID := "review-thread"
	task := store.Task{ID: "review-task", GoalID: &goal.ID, Title: "review", Status: string(TaskStatusReview), ReviewThreadID: &threadID}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := db.SetGoalTasks(ctx, goal.ID, []string{task.ID}, 1); err != nil {
		t.Fatal(err)
	}
	var notifications atomic.Int64
	svc.SetOnChange(func() { notifications.Add(1) })
	scheduler := NewGoalScheduler(svc, time.Minute)
	for i, running := range []bool{true, false, false, true, true, false} {
		driver.setThreadState(threadID, SessionState{Running: running})
		before := svc.revision.Load()
		scheduler.Tick(ctx)
		latest, err := db.GetGoal(ctx, goal.ID)
		if err != nil {
			t.Fatal(err)
		}
		if latest.Progress["stalled"] != !running {
			t.Fatalf("tick %d: 活跃审查停滞判定错误: %+v", i, latest.Progress)
		}
		expectedNotifications := []int64{0, 1, 1, 2, 2, 3}[i]
		if notifications.Load() != expectedNotifications {
			t.Fatalf("tick %d: 通知次数为 %d，期望 %d", i, notifications.Load(), expectedNotifications)
		}
		if (i == 1 || i == 3 || i == 5) && svc.revision.Load() <= before {
			t.Fatalf("tick %d: 停滞变化未递增看板版本", i)
		}
	}
}
