package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 场景 1：空列表显示“还没有自动化”
func TestAutomationWorkspace_Empty(t *testing.T) {
	fake := NewFakeClient()
	fake.SetAutomations(nil)

	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("自动化")
	})

	if err := tt.Click("自动化"); err != nil {
		t.Fatalf("click '自动化' nav failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("还没有自动化")
	})
}

// 场景 2：填写表单并保存后，CreateAutomationCalls 记录了正确的 Name 和 Schedule
func TestAutomationWorkspace_Create(t *testing.T) {
	fake := NewFakeClient()
	fake.SetAutomations([]Automation{})

	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("自动化")
	})

	if err := tt.Click("自动化"); err != nil {
		t.Fatalf("click '自动化' nav failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("还没有自动化")
	})

	// 点击空态的“创建第一个自动化”按钮进入新建表单
	if err := tt.Click("automation-empty-create"); err != nil {
		if clickErr := tt.Click("automation-new-btn"); clickErr != nil {
			t.Fatalf("click 'automation-empty-create' failed: %v", err)
		}
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("新建自动化")
	})

	// 填写名称
	if err := tt.Click("automation-name-input"); err != nil {
		t.Fatalf("focus 'automation-name-input' failed: %v", err)
	}
	tt.Type("安全漏洞巡检")

	// 填写 Prompt
	if err := tt.Click("automation-prompt-input"); err != nil {
		t.Fatalf("focus 'automation-prompt-input' failed: %v", err)
	}
	tt.Type("执行静态代码检查并汇报高风险项")

	// 点击保存
	if err := tt.Click("automation-save"); err != nil {
		if clickErr := tt.Click("保存"); clickErr != nil {
			t.Fatalf("click 'automation-save' failed: %v", err)
		}
	}

	// 断言 CreateAutomationCalls 记录了正确的 Name 和 Schedule
	waitFor(t, tt, func() bool {
		calls := fake.CreateAutomationCalls()
		if len(calls) == 0 {
			return false
		}
		c := calls[0]
		return c.Name == "安全漏洞巡检" &&
			c.Prompt == "执行静态代码检查并汇报高风险项" &&
			c.Schedule.Type == "interval" &&
			c.Schedule.Seconds == 3600 // 默认 60 分钟 * 60 秒
	})
	waitFor(t, tt, func() bool { return tt.HasText("编辑自动化") })
	if err := tt.Click("automation-save"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return len(fake.UpdateAutomationCalls()) == 1 })
	if len(fake.CreateAutomationCalls()) != 1 {
		t.Fatal("saving again created a duplicate automation")
	}
}

// 场景 3：必填项为空或运行约束非法时不调用 CreateAutomation
func TestAutomationWorkspace_ValidationFailed(t *testing.T) {
	fake := NewFakeClient()
	fake.SetAutomations([]Automation{})

	tt := ui.NewTester(Root(fake), 1000, 860)

	waitFor(t, tt, func() bool {
		return tt.HasText("自动化")
	})

	if err := tt.Click("自动化"); err != nil {
		t.Fatalf("click '自动化' nav failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("还没有自动化")
	})

	// 点击空态的“创建第一个自动化”按钮
	if err := tt.Click("automation-empty-create"); err != nil {
		_ = tt.Click("automation-new-btn")
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("新建自动化")
	})

	// 名称与 Prompt 都为空时保存按钮禁用：点击既不提交，也不进入表单校验
	if err := tt.Click("automation-save"); err != nil {
		_ = tt.Click("保存")
	}
	if got := len(fake.CreateAutomationCalls()); got != 0 {
		t.Fatalf("expected 0 CreateAutomationCalls with empty name and prompt, got %d", got)
	}
	if tt.HasText("自动化名称不能为空") {
		t.Fatal("save ran although the save button is disabled")
	}

	// 填写必填项，保存按钮变为可用
	if err := tt.Click("automation-name-input"); err != nil {
		t.Fatalf("focus 'automation-name-input' failed: %v", err)
	}
	tt.Type("安全漏洞巡检")
	if err := tt.Click("automation-prompt-input"); err != nil {
		t.Fatalf("focus 'automation-prompt-input' failed: %v", err)
	}
	tt.Type("执行静态代码检查并汇报高风险项")

	// 最大运行次数改成 -1，触发运行约束校验并在顶部横幅提示
	if err := tt.Click("automation-max-runs-input"); err != nil {
		t.Fatalf("focus 'automation-max-runs-input' failed: %v", err)
	}
	tt.Command("selectAll")
	tt.Type("-1")

	if err := tt.Click("automation-save"); err != nil {
		_ = tt.Click("保存")
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("请选择项目，连续失败次数须为正整数，最大运行次数须为 0 到 1000 的整数")
	})

	if got := len(fake.CreateAutomationCalls()); got != 0 {
		t.Fatalf("expected 0 CreateAutomationCalls on validation failure, got %d", got)
	}
}

func newWorkspaceTester(t *testing.T, client Client, view string) (*ui.Tester, *chatState) {
	t.Helper()
	var state *chatState
	tt := ui.NewTester(Root(client, func(s *chatState) { state = s }), 1320, 860)
	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return len(state.projects) > 0
	})
	state.openWorkspace(view)
	return tt, state
}

func testAutomation(id, projectID string) Automation {
	return Automation{
		ID: id, ProjectID: projectID, Name: id, Prompt: "检查项目",
		Schedule: AutomationSchedule{Type: "daily", TimeOfDay: "09:00", Timezone: "Asia/Shanghai"},
		Enabled:  true, MaxConsecutiveFailures: 3, Revision: 1,
	}
}

func TestAutomationWorkspace_RefreshConflictAndDraftRevision(t *testing.T) {
	fake := NewFakeClient()
	auto := testAutomation("auto-1", "p1")
	fake.SetAutomations([]Automation{auto})
	tt, s := newWorkspaceTester(t, fake, "automations")
	waitFor(t, tt, func() bool { return tt.HasText(auto.Name) })
	if err := tt.Click(auto.Name); err != nil {
		t.Fatal(err)
	}
	as := getAutomationState(s)
	as.name = "本地草稿"
	auto.Name = "远程配置"
	auto.Revision = 2
	fake.SetAutomations([]Automation{auto})
	fake.TriggerAutomationChange()
	waitFor(t, tt, func() bool {
		automationMu.Lock()
		defer automationMu.Unlock()
		return len(as.autos) == 1 && as.autos[0].Revision == 2
	})
	if as.name != "本地草稿" || as.editRevision != 1 {
		t.Fatal("subscription replaced the draft or advanced its revision")
	}
	if err := tt.Click("automation-save"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("数据已在别处被修改，请刷新后重试") })
	if err := tt.Click("automation-conflict-refresh"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return as.editRevision == 2 && as.name == "远程配置" })
	if err := tt.Click("automation-save"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return len(fake.UpdateAutomationCalls()) == 2 })
	calls := fake.UpdateAutomationCalls()
	if calls[1].Name != auto.Name || calls[1].Schedule.Timezone != "Asia/Shanghai" {
		t.Fatalf("refresh or save lost configuration: %+v", calls[1])
	}
}

func TestAutomationWorkspace_ProjectIsolation(t *testing.T) {
	fake := NewFakeClient()
	fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "项目一"}, {ID: "p2", Name: "项目二"}}, nil)
	fake.SetAutomations([]Automation{testAutomation("auto-p1", "p1"), testAutomation("auto-p2", "p2")})
	tt, s := newWorkspaceTester(t, fake, "automations")
	waitFor(t, tt, func() bool { return tt.HasText("auto-p1") })
	if err := tt.Click("auto-p1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("编辑自动化") })
	if err := tt.Click("workspace-project-p2"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("auto-p2") && !tt.HasText("auto-p1") && !tt.HasText("编辑自动化")
	})
	as := getAutomationState(s)
	if as.selectedID != "" || as.isNew {
		t.Fatal("selection from another project survived the switch")
	}
}

func TestAutomationWorkspace_PreservesScheduleOnSave(t *testing.T) {
	for _, schedule := range []AutomationSchedule{
		{Type: "interval", Seconds: 90},
		{Type: "once", Date: time.Date(2030, 1, 2, 3, 4, 37, 0, time.UTC)},
	} {
		t.Run(schedule.Type, func(t *testing.T) {
			fake := NewFakeClient()
			auto := testAutomation("auto-1", "p1")
			auto.Schedule = schedule
			fake.SetAutomations([]Automation{auto})
			tt, _ := newWorkspaceTester(t, fake, "automations")
			waitFor(t, tt, func() bool { return tt.HasText("auto-1") })
			if err := tt.Click("auto-1"); err != nil {
				t.Fatal(err)
			}
			if err := tt.Click("automation-save"); err != nil {
				t.Fatal(err)
			}
			waitFor(t, tt, func() bool { return len(fake.UpdateAutomationCalls()) == 1 })
			got := fake.UpdateAutomationCalls()[0].Schedule
			if got.Seconds != schedule.Seconds || !got.Date.Equal(schedule.Date) {
				t.Fatalf("schedule changed on save: got %+v, want %+v", got, schedule)
			}
		})
	}
}

func TestAutomationWorkspace_InvalidLimits(t *testing.T) {
	for _, input := range []string{"abc", "-1", "1001"} {
		t.Run(input, func(t *testing.T) {
			fake := NewFakeClient()
			tt, s := newWorkspaceTester(t, fake, "automations")
			waitFor(t, tt, func() bool { return tt.HasText("还没有自动化") })
			as := getAutomationState(s)
			as.resetToNewAutomation(s)
			as.name, as.prompt, as.maxRunsInput = "检查", "检查项目", input
			s.saveAutomation()
			if as.formErr == "" || len(fake.CreateAutomationCalls()) != 0 {
				t.Fatal("invalid limits were not rejected")
			}
		})
	}
}

// 汇总栏显示“已启用 X / 共 Y”，并展示最近一次计划运行时间
func TestAutomationWorkspace_Summary(t *testing.T) {
	fake := NewFakeClient()
	now := time.Now()
	nextA := Automation{
		ID: "auto-a", ProjectID: "p1", Name: "甲", Prompt: "检查", Enabled: true,
		Schedule: AutomationSchedule{Type: "interval", Seconds: 3600}, Revision: 1,
		NextRunAt: now.Add(2 * time.Hour),
	}
	nextB := Automation{
		ID: "auto-b", ProjectID: "p1", Name: "乙", Prompt: "检查", Enabled: true,
		Schedule: AutomationSchedule{Type: "daily", TimeOfDay: "09:00"}, Revision: 1,
		NextRunAt: now.Add(90 * time.Minute),
	}
	paused := Automation{
		ID: "auto-c", ProjectID: "p1", Name: "丙", Prompt: "检查", Enabled: false,
		Schedule: AutomationSchedule{Type: "daily", TimeOfDay: "09:00"}, Revision: 1,
	}
	fake.SetAutomations([]Automation{nextA, nextB, paused})
	tt, _ := newWorkspaceTester(t, fake, "automations")

	waitFor(t, tt, func() bool { return tt.HasText("已启用 2 / 共 3") })
	waitFor(t, tt, func() bool {
		return tt.HasText("下次运行 " + formatRelativeUntil(nextB.NextRunAt.Local()))
	})
	if !tt.HasText("新建自动化") {
		t.Fatal("summary lost the new-automation button")
	}
}

// 空态提供“创建第一个自动化”按钮，点击后进入新建表单
func TestAutomationWorkspace_EmptyCreateButton(t *testing.T) {
	fake := NewFakeClient()
	tt, _ := newWorkspaceTester(t, fake, "automations")
	waitFor(t, tt, func() bool { return tt.HasText("创建第一个自动化") })
	if !tt.HasText("自动化会按计划自动启动 Agent 会话") {
		t.Fatal("empty state is missing its description")
	}
	if err := tt.Click("automation-empty-create"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("新建自动化") })
}

// 复制会新建“<原名> (副本)”、默认停用且其余 Spec 与原自动化一致
func TestAutomationWorkspace_Duplicate(t *testing.T) {
	fake := NewFakeClient()
	auto := testAutomation("auto-1", "p1")
	auto.StopWhen = "所有测试通过"
	auto.MaxIterations = 5
	fake.SetAutomations([]Automation{auto})
	tt, s := newWorkspaceTester(t, fake, "automations")
	waitFor(t, tt, func() bool { return tt.HasText("auto-1") })

	if err := tt.Click("auto-duplicate-auto-1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return len(fake.CreateAutomationCalls()) == 1 })
	c := fake.CreateAutomationCalls()[0]
	if c.Name != "auto-1 (副本)" || c.Enabled {
		t.Fatalf("unexpected duplicate input: %+v", c)
	}
	if c.ProjectID != auto.ProjectID || c.Prompt != auto.Prompt || c.StopWhen != auto.StopWhen ||
		c.Schedule != auto.Schedule || c.MaxConsecutiveFailures != auto.MaxConsecutiveFailures ||
		c.MaxIterations != auto.MaxIterations {
		t.Fatalf("duplicate lost spec fields: %+v", c)
	}
	waitFor(t, tt, func() bool {
		as := getAutomationState(s)
		automationMu.Lock()
		defer automationMu.Unlock()
		return as.name == "auto-1 (副本)" && !as.isNew && !as.enabled
	})
}

// 卡片启用开关通过 Update 只切换 Enabled，并同步表单草稿
func TestAutomationWorkspace_TogglePause(t *testing.T) {
	fake := NewFakeClient()
	auto := testAutomation("auto-1", "p1")
	fake.SetAutomations([]Automation{auto})
	tt, s := newWorkspaceTester(t, fake, "automations")
	waitFor(t, tt, func() bool { return tt.HasText("auto-1") })
	as := getAutomationState(s)
	automationMu.Lock()
	as.selectedID = auto.ID
	as.isNew = false
	automationMu.Unlock()

	if err := tt.Click("启用自动化 auto-1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return len(fake.UpdateAutomationCalls()) == 1 })
	call := fake.UpdateAutomationCalls()[0]
	if call.Enabled || call.Name != auto.Name || call.Schedule != auto.Schedule {
		t.Fatalf("pause changed more than Enabled: %+v", call)
	}
	waitFor(t, tt, func() bool {
		automationMu.Lock()
		defer automationMu.Unlock()
		return !as.enabled
	})

	if err := tt.Click("启用自动化 auto-1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return len(fake.UpdateAutomationCalls()) == 2 })
	if !fake.UpdateAutomationCalls()[1].Enabled {
		t.Fatal("resume did not enable the automation")
	}
}

// U-011：completed 的自动化已经结束（Enabled=false），不能显示成「已暂停」。
func TestAutomationCardStatusCompleted(t *testing.T) {
	cases := []struct {
		name string
		auto Automation
		want string
		kind string
	}{
		{name: "命中停止条件", auto: Automation{Status: "completed", Enabled: false, StopReason: "Completed: stop condition satisfied"}, want: "已完成", kind: "succeeded"},
		{name: "迭代用尽", auto: Automation{Status: "completed", Enabled: false, ConsecutiveFailures: 0}, want: "已完成", kind: "succeeded"},
		{name: "连续失败暂停", auto: Automation{Status: "failed", Enabled: false, ConsecutiveFailures: 3}, want: "连续失败后暂停", kind: "failed"},
		{name: "普通暂停", auto: Automation{Status: "paused", Enabled: false}, want: "已暂停", kind: "paused"},
		{name: "运行中优先", auto: Automation{Status: "completed", Enabled: false, LastRunStatus: "running"}, want: "运行中", kind: "running"},
	}
	for _, tc := range cases {
		text, kind := automationCardStatus(tc.auto, false)
		if text != tc.want || kind != tc.kind {
			t.Errorf("%s: automationCardStatus = (%q, %q), want (%q, %q)", tc.name, text, kind, tc.want, tc.kind)
		}
	}
}

// U-012：表单能保存「最多运行次数」和「错过时补跑」，保存后草稿同步。
func TestAutomationWorkspace_MaxRunsAndCatchUp(t *testing.T) {
	fake := NewFakeClient()
	auto := testAutomation("auto-1", "p1")
	auto.MaxRuns = 3
	auto.MaxIterations = 3
	auto.CatchUp = true
	fake.SetAutomations([]Automation{auto})
	tt, s := newWorkspaceTester(t, fake, "automations")
	waitFor(t, tt, func() bool { return tt.HasText(auto.Name) })
	if err := tt.Click(auto.Name); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("编辑自动化") })

	if err := tt.Click("automation-max-runs-input"); err != nil {
		t.Fatal(err)
	}
	tt.Command("selectAll")
	tt.Type("5")
	if err := tt.Click("automation-catch-up-checkbox"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		as := getAutomationState(s)
		automationMu.Lock()
		defer automationMu.Unlock()
		return as.maxRunsInput == "5" && !as.catchUp
	})
	if err := tt.Click("automation-save"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return len(fake.UpdateAutomationCalls()) == 1 })
	call := fake.UpdateAutomationCalls()[0]
	if call.MaxRuns != 5 || call.CatchUp == nil || *call.CatchUp {
		t.Fatalf("saved max runs/catch up = %d/%v, want 5/false", call.MaxRuns, call.CatchUp)
	}
	waitFor(t, tt, func() bool {
		as := getAutomationState(s)
		automationMu.Lock()
		defer automationMu.Unlock()
		return as.maxRunsInput == "5" && !as.catchUp
	})
}

// U-013：补跑和已跳过的运行记录有明确标识。
func TestAutomationWorkspace_RunTriggerBadges(t *testing.T) {
	tests := []struct {
		name    string
		run     AutomationRun
		want    string
		notWant string
	}{
		{
			name: "补跑",
			run: AutomationRun{
				ID: "run-1", AutomationID: "auto-1", Status: "succeeded",
				Trigger: "catch_up", MissedCount: 3, StartedAt: time.Now(),
			},
			want: "补跑 · 跳过 3 次",
		},
		{
			name: "跳过",
			run: AutomationRun{
				ID: "run-2", AutomationID: "auto-1", Status: "skipped",
				Trigger: "catch_up", MissedCount: 1, StartedAt: time.Now(),
			},
			want:    "已跳过",
			notWant: "补跑 · ",
		},
		{
			name: "手动",
			run: AutomationRun{
				ID: "run-3", AutomationID: "auto-1", Status: "succeeded",
				Trigger: "manual", StartedAt: time.Now(),
			},
			want: "手动",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := NewFakeClient()
			fake.SetAutomations([]Automation{testAutomation("auto-1", "p1")})
			fake.SetAutomationRuns("auto-1", []AutomationRun{tc.run})
			tt, _ := newWorkspaceTester(t, fake, "automations")
			waitFor(t, tt, func() bool { return tt.HasText("auto-1") })
			if err := tt.Click("auto-1"); err != nil {
				t.Fatal(err)
			}
			waitFor(t, tt, func() bool { return tt.HasText(tc.want) })
			if tc.notWant != "" && tt.HasText(tc.notWant) {
				t.Fatalf("unexpected badge %q", tc.notWant)
			}
		})
	}
}

// U-014：调度锁为 passive 时页面顶部显示原因。
func TestAutomationWorkspace_SchedulerPassiveHint(t *testing.T) {
	tests := []struct {
		name   string
		status AutomationSchedulerStatus
		want   string
	}{
		{
			name:   "另一个实例持有调度锁",
			status: AutomationSchedulerStatus{State: AutomationSchedulerPassiveHeld},
			want:   "定时任务已暂停：另一个 mcode 实例正在调度",
		},
		{
			name:   "调度锁获取失败",
			status: AutomationSchedulerStatus{State: AutomationSchedulerPassiveError, Error: "permission denied"},
			want:   "定时任务已暂停：无法获取调度锁（permission denied）",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := NewFakeClient()
			fake.SetAutomations([]Automation{testAutomation("auto-1", "p1")})
			fake.SetAutomationSchedulerStatus(tc.status)
			tt, _ := newWorkspaceTester(t, fake, "automations")
			waitFor(t, tt, func() bool { return tt.HasText(tc.want) })
		})
	}
}

// U-015：调度锁正常时不显示暂停提示。
func TestAutomationWorkspace_SchedulerActiveNoHint(t *testing.T) {
	fake := NewFakeClient()
	fake.SetAutomations([]Automation{testAutomation("auto-1", "p1")})
	fake.SetAutomationSchedulerStatus(AutomationSchedulerStatus{State: AutomationSchedulerActive})
	tt, _ := newWorkspaceTester(t, fake, "automations")
	waitFor(t, tt, func() bool { return tt.HasText("auto-1") })
	if tt.HasText("定时任务已暂停") {
		t.Fatal("active scheduler should not show the passive hint")
	}
}

// U-016：passive 时「立即运行」给出提示，且不发请求。
func TestAutomationWorkspace_RunNowBlockedWhenPassive(t *testing.T) {
	fake := NewFakeClient()
	fake.SetAutomations([]Automation{testAutomation("auto-1", "p1")})
	fake.SetAutomationSchedulerStatus(AutomationSchedulerStatus{State: AutomationSchedulerPassiveHeld})
	tt, s := newWorkspaceTester(t, fake, "automations")
	waitFor(t, tt, func() bool { return tt.HasText("auto-1") })
	if err := tt.Click("auto-1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("auto-run-now-auto-1")
		return ok
	})
	if err := tt.Click("auto-run-now-auto-1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		if tt.HasText("无法立即运行") {
			return true
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.toastMessage != ""
	})
	if got := len(fake.RunAutomationCalls()); got != 0 {
		t.Fatalf("expected 0 RunAutomationCalls while passive, got %d", got)
	}
}

// U-017：服务端仍返回 ErrAutomationSchedulerPassive 时翻译成中文提示。
func TestAutomationWorkspace_RunNowPassiveError(t *testing.T) {
	fake := NewFakeClient()
	fake.SetAutomations([]Automation{testAutomation("auto-1", "p1")})
	fake.SetAutomationSchedulerStatus(AutomationSchedulerStatus{State: AutomationSchedulerActive})
	fake.SetAutomationRunError(ErrAutomationSchedulerPassive)
	tt, _ := newWorkspaceTester(t, fake, "automations")
	waitFor(t, tt, func() bool { return tt.HasText("auto-1") })
	if err := tt.Click("auto-1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("auto-run-now-auto-1")
		return ok
	})
	if err := tt.Click("auto-run-now-auto-1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("无法立即运行") })
	if tt.HasText("运行自动化失败") {
		t.Fatal("scheduler passive error should not use the generic failure text")
	}
}
