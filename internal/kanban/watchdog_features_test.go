package kanban

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/store"
)

func TestStewardOwnerCompletionWatchdog(t *testing.T) {
	testStewardOwnerCompletion(t, true)
}

func TestWatchdogLeaderGate(t *testing.T) {
	ctx := context.Background()
	svc, db, _, watchdog := setupWatchdogTestService(t, true, false, true, time.Minute, time.Hour, nil)
	leader := false
	svc.opts.Leader = func() bool { return leader }
	threadID := "missing-session"
	svc.opts.Driver = &orphanDriver{missing: map[string]bool{threadID: true}}
	if err := db.CreateTask(ctx, store.Task{ID: "running", Title: "task", Status: "in_progress", ThreadID: &threadID}); err != nil {
		t.Fatal(err)
	}
	if err := watchdog.Check(ctx); err != nil {
		t.Fatal(err)
	}
	task, err := db.GetTask(ctx, "running")
	if err != nil || task.Status != "in_progress" || task.ThreadID == nil || *task.ThreadID != threadID {
		t.Fatalf("未持锁仍重置任务: %+v, %v", task, err)
	}
	leader = true
	if err := watchdog.Check(ctx); err != nil {
		t.Fatal(err)
	}
	task, err = db.GetTask(ctx, "running")
	if err != nil || task.Status != "todo" || task.ThreadID != nil {
		t.Fatalf("持锁后未重置丢失会话: %+v, %v", task, err)
	}
}

// setupWatchdogTestService 辅助创建用于测试自动化特性的看板服务。
func setupWatchdogTestService(t *testing.T, autoReview, autoStart, autoRescue bool, nudgeTimeout, interruptTimeout time.Duration, nowFn func() time.Time) (*Service, *store.Store, *mockDriver, *Watchdog) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test_features.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	driver := newMockDriver()
	projects := &mockProjects{dir: t.TempDir()}

	svc := NewService(Options{
		Store:                 s,
		Git:                   nil,
		Driver:                driver,
		Projects:              projects,
		AutoReview:            func() bool { return autoReview },
		AutoStartOnCreate:     func() bool { return autoStart },
		AutoRescueStuck:       func() bool { return autoRescue },
		StuckNudgeTimeout:     func() time.Duration { return nudgeTimeout },
		StuckInterruptTimeout: func() time.Duration { return interruptTimeout },
		Now:                   nowFn,
	})
	t.Cleanup(func() { svc.Close() })

	wd := NewWatchdog(svc, 0)
	return svc, s, driver, wd
}

// TestKanbanAutoStartOnCreate 测试新建任务自动排队启动。
func TestKanbanAutoStartOnCreate(t *testing.T) {
	ctx := context.Background()

	t.Run("开启自启且无依赖时创建后立即为in_progress", func(t *testing.T) {
		svc, _, _, _ := setupWatchdogTestService(t, true, true, true, 600*time.Second, 1800*time.Second, nil)
		task, err := svc.CreateTask(ctx, CreateTaskInput{
			ProjectID: "proj-1",
			Title:     "自启任务",
		})
		if err != nil {
			t.Fatalf("创建任务失败: %v", err)
		}
		if task.Status != string(TaskStatusInProgress) {
			t.Fatalf("期望状态为 in_progress, 实际为 %s", task.Status)
		}
		if task.ThreadID == nil || *task.ThreadID == "" {
			t.Fatalf("期望任务已绑定 threadID")
		}
	})

	t.Run("开启自启但有未完成前置依赖时保持todo", func(t *testing.T) {
		svc, _, _, _ := setupWatchdogTestService(t, true, true, true, 600*time.Second, 1800*time.Second, nil)
		// 前置任务（未完成）
		err := svc.opts.Store.CreateTask(ctx, store.Task{
			ID:     "dep-1",
			Title:  "前置任务",
			Status: string(TaskStatusInProgress),
		})
		if err != nil {
			t.Fatalf("创建前置任务失败: %v", err)
		}

		task, err := svc.CreateTask(ctx, CreateTaskInput{
			ProjectID: "proj-1",
			Title:     "后续任务",
			DependsOn: []string{"dep-1"},
		})
		if err != nil {
			t.Fatalf("创建后续任务失败: %v", err)
		}
		if task.Status != string(TaskStatusTodo) {
			t.Fatalf("前置依赖未完成时期望状态为 todo, 实际为 %s", task.Status)
		}
	})

	t.Run("关闭自启时保持todo", func(t *testing.T) {
		svc, _, _, _ := setupWatchdogTestService(t, true, false, true, 600*time.Second, 1800*time.Second, nil)
		task, err := svc.CreateTask(ctx, CreateTaskInput{
			ProjectID: "proj-1",
			Title:     "普通任务",
		})
		if err != nil {
			t.Fatalf("创建任务失败: %v", err)
		}
		if task.Status != string(TaskStatusTodo) {
			t.Fatalf("关闭自启期望状态为 todo, 实际为 %s", task.Status)
		}
	})
}

// TestKanbanAutoReview 测试 Agent 完成后自动转入审查状态。
func TestKanbanAutoReview(t *testing.T) {
	ctx := context.Background()

	t.Run("开启自动审查时完成自动转入review", func(t *testing.T) {
		svc, s, driver, wd := setupWatchdogTestService(t, true, true, true, 600*time.Second, 1800*time.Second, nil)
		task, err := svc.CreateTask(ctx, CreateTaskInput{
			ProjectID: "proj-1",
			Title:     "待审查任务",
		})
		if err != nil {
			t.Fatalf("创建任务失败: %v", err)
		}

		// 模拟 Agent 已经正常执行完成
		driver.setThreadState(*task.ThreadID, SessionState{
			Running:           false,
			Failed:            false,
			LastAssistantText: "代码修改已就绪，请审查。",
		})

		// 执行 watchdog 检查
		if err := wd.Check(ctx); err != nil {
			t.Fatalf("watchdog check 失败: %v", err)
		}

		latest, err := s.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatalf("获取最新任务失败: %v", err)
		}
		if latest.Status != string(TaskStatusReview) {
			t.Fatalf("开启 AutoReview 期望状态为 review, 实际为 %s", latest.Status)
		}
		if latest.LastActivityText == nil || *latest.LastActivityText != "代码修改已就绪，请审查。" {
			t.Fatalf("期望 LastActivityText 已更新，实际: %v", latest.LastActivityText)
		}
	})

	t.Run("两个开关都关闭时完成转入review等待人工处理", func(t *testing.T) {
		svc, s, driver, wd := setupWatchdogTestService(t, false, true, false, 600*time.Second, 1800*time.Second, nil)
		task, err := svc.CreateTask(ctx, CreateTaskInput{
			ProjectID: "proj-1",
			Title:     "人工处理任务",
		})
		if err != nil {
			t.Fatalf("创建任务失败: %v", err)
		}

		driver.setThreadState(*task.ThreadID, SessionState{
			Running:           false,
			Failed:            false,
			LastAssistantText: "已完成执行。",
		})

		if err := wd.Check(ctx); err != nil {
			t.Fatalf("watchdog check 失败: %v", err)
		}

		latest, err := s.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatalf("获取最新任务失败: %v", err)
		}
		if latest.Status != string(TaskStatusReview) {
			t.Fatalf("自动接受与自动评审都关闭时期望转为 review, 实际为 %s", latest.Status)
		}
		if latest.LastActivityText == nil || *latest.LastActivityText != manualReviewText {
			t.Fatalf("期望写入人工处理文案, 实际: %v", latest.LastActivityText)
		}
	})

	t.Run("开启自动接受时保持原状态交给调度器", func(t *testing.T) {
		_, s, driver, wd := setupWatchdogTestService(t, false, true, false, 600*time.Second, 1800*time.Second, nil)
		threadID := "thread-auto-accept"
		seedSchedulerGoal(t, ctx, s, store.Goal{
			ID:        "goal-auto-accept",
			ProjectID: "proj-1",
			Title:     "自动接受",
			Policy:    map[string]any{"autoAccept": true},
		}, store.Task{ID: "task-auto-accept", Title: "自动接受任务", Status: string(TaskStatusInProgress), ThreadID: &threadID})
		driver.setThreadState(threadID, SessionState{Running: false, LastAssistantText: "已完成执行。"})

		if err := wd.Check(ctx); err != nil {
			t.Fatalf("watchdog check 失败: %v", err)
		}

		latest, err := s.GetTask(ctx, "task-auto-accept")
		if err != nil {
			t.Fatalf("获取最新任务失败: %v", err)
		}
		if latest.Status != string(TaskStatusInProgress) {
			t.Fatalf("开启自动接受时期望保持 in_progress 交由调度器接受, 实际为 %s", latest.Status)
		}
	})

	t.Run("遇到审批请求时转入needs_approval", func(t *testing.T) {
		svc, s, driver, wd := setupWatchdogTestService(t, true, true, true, 600*time.Second, 1800*time.Second, nil)
		task, err := svc.CreateTask(ctx, CreateTaskInput{
			ProjectID: "proj-1",
			Title:     "待审批任务",
		})
		if err != nil {
			t.Fatalf("创建任务失败: %v", err)
		}

		driver.setThreadState(*task.ThreadID, SessionState{
			Running:          true,
			AwaitingApproval: true,
		})

		if err := wd.Check(ctx); err != nil {
			t.Fatalf("watchdog check 失败: %v", err)
		}

		latest, err := s.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatalf("获取最新任务失败: %v", err)
		}
		if latest.Status != string(TaskStatusNeedsApproval) {
			t.Fatalf("期望状态转为 needs_approval, 实际为 %s", latest.Status)
		}
		if latest.ThreadID == nil || *latest.ThreadID != *task.ThreadID {
			t.Fatalf("转入 needs_approval 时应保留 ThreadID, 实际为 %v", latest.ThreadID)
		}
		if latest.LastActivityText == nil || *latest.LastActivityText != awaitingUserText {
			t.Fatalf("期望写入等待文案, 实际为 %v", latest.LastActivityText)
		}
	})

	t.Run("遇到输入请求时转入needs_approval", func(t *testing.T) {
		svc, s, driver, wd := setupWatchdogTestService(t, true, true, true, 600*time.Second, 1800*time.Second, nil)
		task, err := svc.CreateTask(ctx, CreateTaskInput{
			ProjectID: "proj-1",
			Title:     "待输入任务",
		})
		if err != nil {
			t.Fatalf("创建任务失败: %v", err)
		}

		driver.setThreadState(*task.ThreadID, SessionState{
			Running:       true,
			AwaitingInput: true,
		})

		if err := wd.Check(ctx); err != nil {
			t.Fatalf("watchdog check 失败: %v", err)
		}

		latest, err := s.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatalf("获取最新任务失败: %v", err)
		}
		if latest.Status != string(TaskStatusNeedsApproval) {
			t.Fatalf("期望状态转为 needs_approval, 实际为 %s", latest.Status)
		}
		if latest.ThreadID == nil || *latest.ThreadID != *task.ThreadID {
			t.Fatalf("等待输入时应保留 ThreadID, 实际为 %v", latest.ThreadID)
		}
	})
}

// TestWatchdog_StuckRescue 测试卡死救援（提醒与强制中断）。
func TestWatchdog_StuckRescue(t *testing.T) {
	ctx := context.Background()
	currTime := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	nowFn := func() time.Time { return currTime }

	nudgeTimeout := 600 * time.Second
	interruptTimeout := 1800 * time.Second

	svc, s, driver, wd := setupWatchdogTestService(t, true, true, true, nudgeTimeout, interruptTimeout, nowFn)

	task, err := svc.CreateTask(ctx, CreateTaskInput{
		ProjectID: "proj-1",
		Title:     "长时间卡死任务",
	})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	driver.setThreadState(*task.ThreadID, SessionState{
		Running:           true,
		LastAssistantText: "开始处理...",
	})

	// 第一次检查，建立初始活动时间戳
	if err := wd.Check(ctx); err != nil {
		t.Fatalf("首次 check 失败: %v", err)
	}

	// 1. 未超过 nudge 阈值（500 秒），不应触发提醒
	currTime = currTime.Add(500 * time.Second)
	if err := wd.Check(ctx); err != nil {
		t.Fatalf("check 失败: %v", err)
	}
	latest, _ := s.GetTask(ctx, task.ID)
	if latest.LastActivityText != nil && strings.Contains(*latest.LastActivityText, "无活动") {
		t.Fatalf("未超时时不应发送无活动提醒")
	}

	// 2. 超过 nudge 阈值（650 秒），触发催促（mockDriver 不支持 SessionMessenger，记录发送失败）
	currTime = currTime.Add(150 * time.Second) // 累计 650 秒
	if err := wd.Check(ctx); err != nil {
		t.Fatalf("check 失败: %v", err)
	}
	latest, _ = s.GetTask(ctx, task.ID)
	if latest.LastActivityText == nil || !strings.Contains(*latest.LastActivityText, "催促发送失败") {
		t.Fatalf("超过阈值期望触发催促，实际: %v", latest.LastActivityText)
	}

	// 3. 超过 interrupt 阈值（累计 1900 秒），且 AutoRescueStuck 开启；mockDriver 不支持中断，
	// 中断失败时保留任务原状态与 ThreadID，留待下轮重试
	currTime = currTime.Add(1250 * time.Second) // 累计 1900 秒
	if err := wd.Check(ctx); err != nil {
		t.Fatalf("check 失败: %v", err)
	}
	latest, _ = s.GetTask(ctx, task.ID)
	if latest.LastFailureReason == nil || !strings.Contains(*latest.LastFailureReason, "卡死救援中断失败") {
		t.Fatalf("期望标记中断失败原因，实际: %v", latest.LastFailureReason)
	}
	if latest.Status != string(TaskStatusInProgress) {
		t.Fatalf("中断失败时不应重置任务，实际为 %s", latest.Status)
	}
	if latest.ThreadID == nil || *latest.ThreadID != *task.ThreadID {
		t.Fatalf("中断失败时不应清空 ThreadID，实际为 %v", latest.ThreadID)
	}
}
