package swarm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"mcode/internal/kanban"
	"mcode/internal/reviewcheck"
)

type noMessengerDriver struct {
	kanban.SessionDriver
}

func TestParseDelegationsAcceptance(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		raw  string
		want []string
	}{
		{`[" true ", "", "printf 'a,b'", false]`, []string{"true", "printf 'a,b'"}},
		{`" printf 'a,b' "`, []string{"printf 'a,b'"}},
		{`false`, nil},
		{`{"command":"true"}`, nil},
		{`["", 1]`, nil},
		{`["1","2","3","4","5","6","7","8","9"]`, []string{"1", "2", "3", "4", "5", "6", "7", "8"}},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()
			entries, _, err := ParseDelegations("```swarm\n[{\"prompt\":\"edit\",\"acceptance\":" + tc.raw + "}]\n```")
			if err != nil || len(entries) != 1 {
				t.Fatalf("parse: %v %v", entries, err)
			}
			if !reflect.DeepEqual(entries[0].Acceptance, tc.want) {
				t.Fatalf("acceptance=%v want=%v", entries[0].Acceptance, tc.want)
			}
		})
	}
}

func TestManagerAcceptanceModes(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"ai", "human", "off"} {
		for _, pass := range []bool{false, true} {
			name := mode + "/false"
			command := "printf 'acceptance failed' >&2; false"
			if pass {
				name = mode + "/true"
				command = "test -f internal/feature.go"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				fake := &funnelFake{}
				m, dir := newFunnelManager(t, fake)
				defer m.Stop()
				settings := m.Settings()
				settings.ReviewMode = mode
				settings.RetryFailed = true
				m.SetSettingsFunc(func() Settings { return settings })
				bees, err := m.Dispatch(context.Background(), DispatchRequest{
					LeadThreadID: "lead-acceptance", LeadWorkDir: dir,
					Bees: []BeeSpec{{Name: "Aspen", Task: "修改 internal/feature.go", Acceptance: []string{command}}},
				})
				if err != nil {
					t.Fatal(err)
				}
				bee := bees[0]
				writeBeeFile(t, bee, "internal/feature.go", "package feature\n")
				completeBee(t, m, bee, "完成")
				h := waitFunnel(t, m, "lead-acceptance", bee.ThreadID, func(b Bee) bool {
					if mode == "off" {
						if pass {
							return b.Merged
						}
						return b.Status == StatusFailed
					}
					if !pass {
						return isRejected(b)
					}
					if mode == "human" {
						return isNeedsHuman(b)
					}
					return isApproved(b)
				})
				if mode == "off" {
					if check, generate := fake.calls(); check != 0 || generate != 0 {
						t.Fatalf("off ran project check or review: %d, %d", check, generate)
					}
				}
				if !pass {
					if h.Merged {
						t.Fatal("failed acceptance merged")
					}
					if _, err := os.Stat(filepath.Join(dir, "internal/feature.go")); !os.IsNotExist(err) {
						t.Fatalf("main copy changed: %v", err)
					}
					if mode == "off" {
						if h.Status != StatusFailed || h.PendingMerge || !strings.Contains(BuildReport([]Bee{h}), "验收命令失败："+command+"（退出码 1）") || !strings.Contains(BuildReport([]Bee{h}), "acceptance failed") {
							t.Fatalf("failed result: %+v", h)
						}
						if _, err := os.Stat(h.WorktreePath); err != nil {
							t.Fatal(err)
						}
						if _, err := os.Stat(h.ArchivedPatchPath); err != nil {
							t.Fatal(err)
						}
						m.Tick(context.Background())
						if got := m.State("lead-acceptance").Bees; len(got) != 1 || got[0].ThreadID != bee.ThreadID {
							t.Fatalf("unexpected retry: %+v", got)
						}
					} else if !strings.HasPrefix(h.ReviewReason, "验收命令失败：") || !strings.Contains(h.ReviewReason, "退出码 1") || !strings.Contains(h.ReviewReason, "acceptance failed") {
						t.Fatalf("reason=%q", h.ReviewReason)
					}
					return
				}
				if !containsReviewConcern(h.ReviewConcerns, "验收命令通过：1 条") {
					t.Fatalf("missing concern: %v", h.ReviewConcerns)
				}
				if mode == "human" {
					if h.Merged {
						t.Fatal("human review skipped")
					}
					if _, err := m.ApproveMerge(context.Background(), "lead-acceptance", bee.ThreadID); err != nil {
						t.Fatal(err)
					}
				} else if !h.Merged {
					t.Fatalf("not merged: %+v", h)
				}
			})
		}
	}
}

func TestRunAcceptanceStopsAndSkips(t *testing.T) {
	t.Parallel()
	m := NewManager(Options{})
	dir := t.TempDir()
	h := Bee{WorktreePath: dir, ChangedFiles: []string{"a.go"}, Acceptance: []string{"printf before; exit 7", "touch should-not-run"}}
	passed, detail := m.runAcceptance(context.Background(), "lead", h)
	if passed || !strings.Contains(detail, "退出码 7") || !strings.Contains(detail, "before") {
		t.Fatalf("passed=%v detail=%q", passed, detail)
	}
	if _, err := os.Stat(filepath.Join(dir, "should-not-run")); !os.IsNotExist(err) {
		t.Fatalf("second command ran: %v", err)
	}
	for _, skip := range []Bee{
		{ReadOnly: true, WorktreePath: dir, ChangedFiles: h.ChangedFiles, Acceptance: []string{"false"}},
		{WorktreePath: dir, Acceptance: []string{"false"}},
		{WorktreePath: dir, ChangedFiles: h.ChangedFiles},
	} {
		if ok, detail := m.runAcceptance(context.Background(), "lead", skip); !ok || detail != "" {
			t.Fatalf("skip: %v %q", ok, detail)
		}
	}
}

func TestRunAcceptanceReportsProgress(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"通过", "失败", "取消", "超时"} {
		t.Run(outcome, func(t *testing.T) {
			t.Parallel()
			m := NewManager(Options{ReviewProjectConfig: func(string, string) (reviewcheck.ProjectConfig, error) {
				return reviewcheck.ProjectConfig{TimeoutSeconds: 1}, nil
			}})
			dir := t.TempDir()
			command := "touch started; i=0; while [ ! -f finish ] && [ $i -lt 300 ]; do sleep 0.01; i=$((i+1)); done; test -f finish"
			if outcome == "失败" {
				command += "; exit 7"
			}
			h := Bee{ThreadID: "bee", WorktreePath: dir, ChangedFiles: []string{"a.go"}, Acceptance: []string{"true", command}}
			m.leads["lead"] = &leadState{leadThreadID: "lead", bees: []Bee{h}}
			updates := make(chan Bee, 4)
			m.SetOnChange(func(id string) { updates <- m.State(id).Bees[0] })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type result struct {
				passed bool
				detail string
			}
			done := make(chan result, 1)
			go func() {
				passed, detail := m.runAcceptance(ctx, "lead", h)
				done <- result{passed, detail}
			}()
			// 失败时也取消并等待，避免验收子进程或通知回调遗留到下一用例。
			finished := false
			defer func() {
				cancel()
				if !finished {
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Error("验收取消后未退出")
					}
				}
			}()
			for step := 1; step <= 2; step++ {
				select {
				case b := <-updates:
					if b.AcceptanceStep != step || b.AcceptanceStartedAt == nil {
						t.Fatalf("命令进度不符: %+v", b)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("未收到验收进度通知")
				}
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(dir, "started")); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("第 2 条命令未启动")
				}
				time.Sleep(5 * time.Millisecond)
			}
			b := m.State("lead").Bees[0]
			if b.AcceptanceStep != 2 || b.AcceptanceStartedAt == nil {
				t.Fatalf("运行中的进度不符: %+v", b)
			}
			// UI 快照保留进度并隔离时间指针，JSON 持久化则丢弃瞬时状态。
			*b.AcceptanceStartedAt = time.Time{}
			if m.State("lead").Bees[0].AcceptanceStartedAt.IsZero() {
				t.Fatal("快照未独立复制验收时间")
			}
			data, err := json.Marshal(m.State("lead").Bees[0])
			if err != nil {
				t.Fatal(err)
			}
			var restored Bee
			if err := json.Unmarshal(data, &restored); err != nil || restored.AcceptanceStep != 0 || restored.AcceptanceStartedAt != nil {
				t.Fatalf("持久化恢复了瞬时进度: %+v, %v", restored, err)
			}
			switch outcome {
			case "通过", "失败":
				if err := os.WriteFile(filepath.Join(dir, "finish"), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			case "取消":
				cancel()
			}
			select {
			case got := <-done:
				finished = true
				if got.passed != (outcome == "通过") || (outcome == "超时" && !strings.Contains(got.detail, "超时")) {
					t.Fatalf("验收结果不符: %+v", got)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("验收未结束")
			}
			b = m.State("lead").Bees[0]
			if b.AcceptanceStep != 0 || b.AcceptanceStartedAt != nil {
				t.Fatalf("验收结束未清理进度: %+v", b)
			}
			if b := <-updates; b.AcceptanceStep != 0 || b.AcceptanceStartedAt != nil {
				t.Fatalf("未通知进度清理: %+v", b)
			}
		})
	}
}

func TestRunAcceptanceRewritesAbsoluteCdToWorktree(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	worktree := filepath.Join(parent, "copy")
	other := source + "-other"
	for _, dir := range []string{source, worktree, other} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(worktree, "only-copy"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManager(Options{})
	h := Bee{TargetWorkDir: source, WorktreePath: worktree, ChangedFiles: []string{"a.go"}, Acceptance: []string{"cd '" + source + "' && test -f only-copy"}}
	if passed, detail := m.runAcceptance(context.Background(), "lead", h); !passed {
		t.Fatalf("passed=%v detail=%q", passed, detail)
	}
	h.Acceptance = []string{"cd '" + other + "' && test ! -f only-copy"}
	if passed, detail := m.runAcceptance(context.Background(), "lead", h); !passed {
		t.Fatalf("同前缀路径不应改写: passed=%v detail=%q", passed, detail)
	}
	h.Acceptance = []string{"cd '" + source + "' && false"}
	if passed, detail := m.runAcceptance(context.Background(), "lead", h); passed || !strings.Contains(detail, h.Acceptance[0]) {
		t.Fatalf("失败详情应保留原始命令: passed=%v detail=%q", passed, detail)
	}
}

func TestRunAcceptanceTimeoutAndCancel(t *testing.T) {
	t.Parallel()
	m := NewManager(Options{ReviewProjectConfig: func(string, string) (reviewcheck.ProjectConfig, error) {
		return reviewcheck.ProjectConfig{TimeoutSeconds: 1}, nil
	}})
	h := Bee{WorktreePath: t.TempDir(), ChangedFiles: []string{"a.go"}, Acceptance: []string{"sleep 10", "touch should-not-run"}}
	if passed, detail := m.runAcceptance(context.Background(), "lead", h); passed || !strings.Contains(detail, "超时") {
		t.Fatalf("timeout: %v %q", passed, detail)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if passed, _ := m.runAcceptance(ctx, "lead", h); passed {
		t.Fatal("canceled acceptance passed")
	}
	if budget := m.reviewCheckBudget("lead", h); budget != 3*time.Second+reviewCheckBudgetMargin {
		t.Fatalf("budget=%v", budget)
	}
}

func TestAcceptancePlanAndPersistence(t *testing.T) {
	t.Parallel()
	m, dir := newFunnelManager(t, &funnelFake{})
	commands := []string{"true", "false"}
	plan := m.planEntry(context.Background(), "lead", Delegation{Prompt: "edit", Acceptance: commands}, nil, "", dir, map[int]BeeStatus{}, 0)
	commands[0] = "changed"
	if !reflect.DeepEqual(plan.spec.Acceptance, []string{"true", "false"}) {
		t.Fatal(plan.spec.Acceptance)
	}
	for _, value := range []any{plan.spec, Bee{Acceptance: plan.spec.Acceptance}} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var decoded struct {
			Acceptance []string `json:"acceptance"`
		}
		if err := json.Unmarshal(data, &decoded); err != nil || !reflect.DeepEqual(decoded.Acceptance, plan.spec.Acceptance) {
			t.Fatalf("roundtrip: %s %v", data, err)
		}
	}
	m.mu.Lock()
	m.leads["lead"] = &leadState{leadThreadID: "lead"}
	m.mu.Unlock()
	m.recordFailedBee("lead", "", dir, plan.spec, 0, nil)
	if got := m.State("lead").Bees; len(got) != 1 || !reflect.DeepEqual(got[0].Acceptance, plan.spec.Acceptance) {
		t.Fatalf("启动失败丢失验收命令：%+v", got)
	}
	bee := Bee{Acceptance: plan.spec.Acceptance}
	copy := bee.clone()
	copy.Acceptance[0] = "changed"
	if bee.Acceptance[0] != "true" {
		t.Fatal("clone shares acceptance")
	}
}

func TestAcceptanceContinuesToL2(t *testing.T) {
	t.Parallel()
	fake := &funnelFake{}
	m, dir := newFunnelManager(t, fake)
	settings := m.Settings()
	settings.ReviewMaxChangedLines = 0
	m.SetSettingsFunc(func() Settings { return settings })
	bees, err := m.Dispatch(context.Background(), DispatchRequest{
		LeadThreadID: "lead-l2-acceptance", LeadWorkDir: dir,
		Bees: []BeeSpec{{Name: "Aspen", Task: "修改 internal/feature.go", Acceptance: []string{"true"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	writeBeeFile(t, bees[0], "internal/feature.go", "package feature\n")
	completeBee(t, m, bees[0], "完成")
	h := waitFunnel(t, m, "lead-l2-acceptance", bees[0].ThreadID, isApproved)
	if h.ReviewLayer != "l2" || !containsReviewConcern(h.ReviewConcerns, "验收命令通过：1 条") {
		t.Fatalf("L2丢失验收结果：%+v", h)
	}
}

func TestAcceptanceCommitLanding(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"true", "false"} {
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			m, _, root := newCommitLandingManager(t)
			bee := startCommitLandingBee(t, m, root, "lead-commit-acceptance", 0)
			m.mu.Lock()
			m.leads["lead-commit-acceptance"].bees[0].Acceptance = []string{command}
			m.mu.Unlock()
			before := gitTestRun(t, root, "rev-parse", "HEAD")
			writePipelineFile(t, bee.WorktreePath, "lines.txt", strings.Replace(landingBase, "one\n", "changed\n", 1))
			completeBee(t, m, bee, "完成")
			bee = waitFunnel(t, m, "lead-commit-acceptance", bee.ThreadID, func(b Bee) bool {
				return b.Merged || b.Status == StatusFailed
			})
			m.wg.Wait()
			if command == "false" {
				if bee.Merged || bee.Status != StatusFailed || gitTestRun(t, root, "rev-parse", "HEAD") != before {
					t.Fatalf("failed acceptance landed: %+v", bee)
				}
				assertLandingFile(t, root, landingBase)
			} else {
				assertLandingCommit(t, m, root, bee)
			}
		})
	}
}

func TestAcceptanceOffTickDoesNotBlock(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"sleep 0.9", ""} {
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			fake := &funnelFake{}
			m, dir := newFunnelManager(t, fake)
			settings := m.Settings()
			settings.ReviewMode = "off"
			m.SetSettingsFunc(func() Settings { return settings })
			var acceptance []string
			if command != "" {
				acceptance = []string{command}
			}
			bees, err := m.Dispatch(context.Background(), DispatchRequest{
				LeadThreadID: "lead-async-acceptance", LeadWorkDir: dir,
				Bees: []BeeSpec{{Name: "Aspen", Task: "修改 internal/feature.go", Acceptance: acceptance}},
			})
			if err != nil {
				t.Fatal(err)
			}
			bee := bees[0]
			writeBeeFile(t, bee, "internal/feature.go", "package feature\n")
			completeBee(t, m, bee, "完成")
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer func() { cancel(); m.wg.Wait() }()
			started := time.Now()
			m.Tick(ctx)
			if elapsed := time.Since(started); command != "" && elapsed >= 750*time.Millisecond {
				t.Fatalf("Tick blocked: %v", elapsed)
			}
			h := beeByThread(t, m, "lead-async-acceptance", bee.ThreadID)
			if command == "" {
				if !h.Merged || h.PendingMerge || h.ReviewState != ReviewNone {
					t.Fatalf("bee without acceptance did not merge directly: %+v", h)
				}
			} else {
				if !h.PendingMerge || h.Merged || h.ReviewStage != ReviewStageL0 {
					t.Fatalf("acceptance not queued: %+v", h)
				}
				waitFunnel(t, m, "lead-async-acceptance", bee.ThreadID, isApproved)
			}
			if check, generate := fake.calls(); check != 0 || generate != 0 {
				t.Fatalf("off ran project check or review: %d, %d", check, generate)
			}
		})
	}
}

func TestAcceptanceOffReportsAfterSettlement(t *testing.T) {
	t.Parallel()
	for _, result := range []string{"false", "true"} {
		t.Run(result, func(t *testing.T) {
			t.Parallel()
			m, dir := newFunnelManager(t, &funnelFake{})
			driver := m.opts.Driver.(*fakeDriver)
			if result == "false" {
				// 覆盖不实现 SessionMessenger 的驱动在首次验收失败时直接结算并汇报。
				m.opts.Driver = noMessengerDriver{SessionDriver: driver}
			}
			settings := m.Settings()
			settings.ReviewMode = "off"
			m.SetSettingsFunc(func() Settings { return settings })
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer func() { cancel(); m.wg.Wait() }()
			var reports []string
			idle := 0
			m.opts.OnReport = func(leadID, report string) error {
				h := m.State(leadID).Bees[0]
				if h.PendingMerge || h.ReviewState == ReviewRunning {
					t.Errorf("reported before settlement: %+v", h)
				}
				if result == "true" {
					if !h.Merged {
						t.Error("reported success before merge")
					}
					if _, err := os.Stat(filepath.Join(dir, "internal/feature.go")); err != nil {
						t.Errorf("reported success before file landed: %v", err)
					}
				} else if h.Status != StatusFailed {
					t.Errorf("reported failure before failed status: %+v", h)
				}
				reports = append(reports, report)
				return nil
			}
			m.opts.OnBeesIdle = func(context.Context, string) { idle++ }
			command := "touch started; while ! test -f release; do sleep 0.02; done; printf 'acceptance result'; " + result
			bees, err := m.Dispatch(ctx, DispatchRequest{
				LeadThreadID: "lead-report-acceptance", LeadWorkDir: dir,
				Bees: []BeeSpec{
					{Name: "Aspen", Task: "修改 internal/feature.go", Acceptance: []string{command}},
					{Name: "Birch", Task: "修改 internal/other.go"},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			bee := bees[0]
			writeBeeFile(t, bee, "internal/feature.go", "package feature\n")
			driver.setSessionState(bee.ThreadID, kanban.SessionState{Running: false, LastAssistantText: "完成"})
			writeBeeFile(t, bees[1], "internal/other.go", "package feature\n")
			driver.setSessionState(bees[1].ThreadID, kanban.SessionState{Running: false, LastAssistantText: "完成"})
			m.Tick(ctx)
			for {
				if _, err := os.Stat(filepath.Join(bee.WorktreePath, "started")); err == nil {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("acceptance did not start")
				case <-time.After(5 * time.Millisecond):
				}
			}
			for range 3 {
				m.Tick(ctx)
			}
			if h := beeByThread(t, m, "lead-report-acceptance", bee.ThreadID); len(reports) != 0 || idle != 0 || h.Reported {
				t.Fatalf("acceptance still running: reports=%v idle=%d reported=%v", reports, idle, h.Reported)
			}
			if h := beeByThread(t, m, "lead-report-acceptance", bees[1].ThreadID); !h.Merged || h.Reported {
				t.Fatalf("completed sibling must wait for acceptance: %+v", h)
			}
			if err := os.WriteFile(filepath.Join(bee.WorktreePath, "release"), nil, 0644); err != nil {
				t.Fatal(err)
			}
			m.wg.Wait()
			for range 3 {
				m.Tick(ctx)
			}
			if len(reports) != 1 || !beeByThread(t, m, "lead-report-acceptance", bee.ThreadID).Reported {
				t.Fatalf("expected one final report: %v", reports)
			}
			if !strings.Contains(reports[0], "Birch") || !beeByThread(t, m, "lead-report-acceptance", bees[1].ThreadID).Reported {
				t.Fatalf("missing sibling in batch report: %v", reports)
			}
			if result == "true" {
				if !strings.Contains(reports[0], "合并结果：已合入") {
					t.Fatalf("missing merge result: %s", reports[0])
				}
			} else if !strings.Contains(reports[0], "合并结果：失败") || !strings.Contains(reports[0], "验收命令失败："+command+"（退出码 1）") || !strings.Contains(reports[0], "acceptance result") {
				t.Fatalf("missing acceptance failure: %s", reports[0])
			}
		})
	}
}

func TestAcceptanceOffStoppedBeeDoesNotMerge(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"stop", "stop-all", "delete"} {
		for _, result := range []string{"true", "false"} {
			t.Run(action+"/"+result, func(t *testing.T) {
				t.Parallel()
				m, dir := newFunnelManager(t, &funnelFake{})
				settings := m.Settings()
				settings.ReviewMode = "off"
				m.SetSettingsFunc(func() Settings { return settings })
				bees, err := m.Dispatch(context.Background(), DispatchRequest{
					LeadThreadID: "lead-stop-acceptance", LeadWorkDir: dir,
					Bees: []BeeSpec{{Name: "Aspen", Task: "修改 internal/feature.go", Acceptance: []string{"touch started; while ! test -f release; do sleep 0.02; done; " + result}}},
				})
				if err != nil {
					t.Fatal(err)
				}
				bee := bees[0]
				writeBeeFile(t, bee, "internal/feature.go", "package feature\n")
				completeBee(t, m, bee, "完成")
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer func() { cancel(); m.wg.Wait() }()
				m.Tick(ctx)
				for {
					if _, err := os.Stat(filepath.Join(bee.WorktreePath, "started")); err == nil {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal("acceptance did not start")
					case <-time.After(5 * time.Millisecond):
					}
				}
				if action == "stop" {
					if err := m.StopBee(ctx, "lead-stop-acceptance", bee.ThreadID); err != nil {
						t.Fatal(err)
					}
				} else if action == "stop-all" {
					if err := m.StopAll(ctx, "lead-stop-acceptance"); err != nil {
						t.Fatal(err)
					}
				} else {
					m.BeeThreadDeleted(ctx, bee.ThreadID)
				}
				before := beeByThread(t, m, "lead-stop-acceptance", bee.ThreadID)
				if before.AcceptanceStep != 0 || before.AcceptanceStartedAt != nil {
					t.Fatal("停止或删除后仍显示验收进度")
				}
				if err := os.WriteFile(filepath.Join(bee.WorktreePath, "release"), nil, 0644); err != nil {
					t.Fatal(err)
				}
				m.wg.Wait()
				h := beeByThread(t, m, "lead-stop-acceptance", bee.ThreadID)
				if !reflect.DeepEqual(before, h) || h.Merged || h.PendingMerge || (action != "delete" && h.Status != StatusStopped) {
					t.Fatalf("late result changed stopped/deleted bee: before=%+v after=%+v", before, h)
				}
				if _, err := os.Stat(filepath.Join(dir, "internal/feature.go")); !os.IsNotExist(err) {
					t.Fatalf("main copy changed: %v", err)
				}
			})
		}
	}
}

func TestAcceptanceHumanCannotApproveBeforeChecks(t *testing.T) {
	t.Parallel()
	started, release := make(chan struct{}), make(chan struct{})
	fake := &funnelFake{checkFn: func(ctx context.Context, _ string, _ []string, _ string, _ time.Duration) reviewcheck.Result {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return reviewcheck.Result{OK: true}
	}}
	m, dir := newFunnelManager(t, fake)
	settings := m.Settings()
	settings.ReviewMode = "human"
	m.SetSettingsFunc(func() Settings { return settings })
	bees, err := m.Dispatch(context.Background(), DispatchRequest{
		LeadThreadID: "lead-human-acceptance", LeadWorkDir: dir,
		Bees: []BeeSpec{{Name: "Aspen", Task: "修改 internal/feature.go", Acceptance: []string{"false"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	writeBeeFile(t, bees[0], "internal/feature.go", "package feature\n")
	completeBee(t, m, bees[0], "完成")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m.Tick(ctx)
	defer func() { close(release); m.wg.Wait() }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("L0 did not start")
	}
	if _, err := m.ApproveMerge(ctx, "lead-human-acceptance", bees[0].ThreadID); err == nil {
		t.Fatal("approved before acceptance")
	}
	m.markReviewNeedsHuman("lead-human-acceptance", bees[0].ThreadID, "L0 检查超时")
	if _, err := m.ApproveMerge(ctx, "lead-human-acceptance", bees[0].ThreadID); err == nil {
		t.Fatal("timeout bypassed acceptance")
	}
}

func TestRewriteWorkDirBackslashBoundary(t *testing.T) {
	t.Parallel()
	got := rewriteWorkDir(`cd /source\sub`, "/source", "/copy")
	if got != `cd /copy\sub` {
		t.Fatalf("rewrite = %q", got)
	}
}

func TestStopBeeCancelsAcceptance(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"stop", "stop-all", "delete"} {
		m := NewManager(Options{})
		dir := t.TempDir()
		secondFile := filepath.Join(dir, "second-ran")
		h := Bee{
			ThreadID:      "bee-stop-cancel-" + action,
			WorktreePath:  dir,
			ChangedFiles:  []string{"a.go"},
			Acceptance:    []string{"sleep 30", "touch '" + filepath.ToSlash(secondFile) + "'"},
			Status:        StatusCompleted,
			PendingMerge:  true,
			ReviewState:   ReviewRunning,
			ReviewStage:   ReviewStageL0,
			TargetWorkDir: dir,
		}
		leadID := "lead-stop-cancel-" + action
		m.leads[leadID] = &leadState{leadThreadID: leadID, leadWorkDir: dir, bees: []Bee{h}}

		started := make(chan struct{}, 1)
		m.SetOnChange(func(id string) {
			st := m.State(id)
			if len(st.Bees) > 0 && st.Bees[0].AcceptanceStep == 1 {
				select {
				case started <- struct{}{}:
				default:
				}
			}
		})

		type accResult struct {
			passed bool
			detail string
		}
		done := make(chan accResult, 1)
		go func() {
			passed, detail := m.runAcceptance(context.Background(), leadID, h)
			done <- accResult{passed: passed, detail: detail}
		}()

		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: 验收命令未在 5 秒内启动", action)
		}

		switch action {
		case "stop":
			if err := m.StopBee(context.Background(), leadID, h.ThreadID); err != nil {
				t.Fatalf("StopBee 失败: %v", err)
			}
		case "stop-all":
			if err := m.StopAll(context.Background(), leadID); err != nil {
				t.Fatalf("StopAll 失败: %v", err)
			}
		case "delete":
			m.BeeThreadDeleted(context.Background(), h.ThreadID)
		}

		select {
		case got := <-done:
			if got.passed {
				t.Fatalf("%s: 被取消的验收不应通过: %+v", action, got)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: 取消后 runAcceptance 未在 5 秒内返回", action)
		}

		if _, err := os.Stat(secondFile); !os.IsNotExist(err) {
			t.Fatalf("%s: 被取消后不应执行第二条验收命令: %v", action, err)
		}
		after := m.State(leadID).Bees[0]
		if after.ReviewState != ReviewNone || after.PendingMerge || !after.MergeDiscarded || (action != "delete" && after.Status != StatusStopped) {
			t.Fatalf("%s: 停止后状态未按停止路径落定: %+v", action, after)
		}
	}
}

func TestAcceptanceProblem(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		command string
		want    string
	}{
		{name: "grep_short_q", command: "grep -q foo a.go", want: acceptanceErrGrepQuiet},
		{name: "grep_merged_qE", command: "grep -qE 'a|b' a.go", want: acceptanceErrGrepQuiet},
		{name: "grep_merged_Eq", command: "grep -Eq 'a|b' a.go", want: acceptanceErrGrepQuiet},
		{name: "grep_quiet", command: "grep --quiet foo a.go", want: acceptanceErrGrepQuiet},
		{name: "grep_silent", command: "grep --silent foo a.go", want: acceptanceErrGrepQuiet},
		{name: "grep_in_sh_c", command: `sh -c '! grep -q "x" a.go'`, want: acceptanceErrGrepQuiet},
		{name: "timeout_leading", command: "timeout 10 go test -run Foo ./p/", want: acceptanceErrTimeout},
		{name: "gtimeout_after_and", command: "cd sub && gtimeout 30 go test -run Foo ./p/", want: acceptanceErrTimeout},
		{name: "timeout_after_pipe", command: "cat a.go | timeout 5 grep -n foo", want: acceptanceErrTimeout},
		{name: "timeout_after_or", command: "false || timeout 5 true", want: acceptanceErrTimeout},
		{name: "timeout_after_semi", command: "true; timeout 5 true", want: acceptanceErrTimeout},
		{name: "go_test_no_run", command: "go test ./internal/ui/", want: acceptanceErrGoTestRun},
		{name: "go_test_timeout_no_run", command: "go test -count=1 -timeout 300s ./internal/ui/", want: acceptanceErrGoTestRun},
		{name: "go_test_empty_run", command: "go test -run= ./internal/ui/", want: acceptanceErrGoTestRun},
		{name: "ok_go_test_run_single_quote", command: "go test -count=1 -timeout 300s -run 'Foo' ./internal/ui/", want: ""},
		{name: "ok_go_test_run_or_pipe", command: "go test -count=1 -timeout 300s -run 'Foo|Bar' ./internal/ui/", want: ""},
		{name: "ok_go_test_run_eq", command: "go test -run=Foo ./internal/ui/", want: ""},
		{name: "ok_sh_grep_n", command: `sh -c '! grep -n "x" a.go'`, want: ""},
		{name: "ok_go_vet", command: "go vet ./...", want: ""},
		{name: "ok_cd_go_test_run", command: "cd /abs/path && go test -run X ./p/", want: ""},
		{name: "ok_grep_pattern_quiet", command: `grep -n "quiet" a.go`, want: ""},
		{name: "ok_grep_e_dash_q", command: `grep -e -q a.go`, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := acceptanceProblem(tc.command); got != tc.want {
				t.Fatalf("acceptanceProblem(%q) = %q, want %q", tc.command, got, tc.want)
			}
		})
	}
}

func TestAcceptanceLintRejectsEntryBeforeStart(t *testing.T) {
	t.Parallel()
	m, dir := newFunnelManager(t, &funnelFake{})
	defer m.Stop()
	settings := m.Settings()
	settings.Enabled = true
	settings.AutoDelegate = true
	m.SetSettingsFunc(func() Settings { return settings })

	ctx := context.Background()
	badCmd := "go test ./internal/ui/"

	// 1. DispatchFromReply：同批坏条目不启动、不占花名册名额，正常条目拿到第一个名字照常启动。
	reply := "```swarm\n" +
		`[{"task":"坏任务","prompt":"修复坏任务","acceptance":["` + badCmd + `"]},` +
		`{"task":"好任务","prompt":"修复好任务","acceptance":["go test -run TestGood ./internal/swarm/"]}]` +
		"\n```"
	bees, notices, err := m.DispatchFromReply(ctx, ReplyDispatchRequest{
		LeadThreadID: "lintbatch",
		ProjectID:    "proj",
		LeadWorkDir:  dir,
		ReplyText:    reply,
	})
	if err != nil {
		t.Fatalf("batch DispatchFromReply unexpected error: %v", err)
	}
	if len(bees) != 1 || bees[0].Name != RosterName(0) || bees[0].Task != "好任务" {
		t.Fatalf("正常条目应拿到首个名字 %s 并启动，实际 bees=%+v", RosterName(0), bees)
	}
	if len(notices) == 0 || !strings.Contains(notices[0], "条目「坏任务」未启动") ||
		!strings.Contains(notices[0], badCmd) || !strings.Contains(notices[0], acceptanceErrGoTestRun) ||
		!strings.Contains(notices[0], "改正后重新派发") {
		t.Fatalf("拒绝提示格式不符: %v", notices)
	}
	if state := m.State("lintbatch"); len(state.Bees) != 1 {
		t.Fatalf("坏条目不应产生 bee 记录: %+v", state.Bees)
	}

	// 2. DispatchSingleEntry：仅坏条目时直接拒绝，不占名字、不建副本、不启动 bee。
	bee, singleNotices, err := m.DispatchSingleEntry(ctx, SingleEntryRequest{
		LeadThreadID: "lintsingle",
		ProjectID:    "proj",
		LeadWorkDir:  dir,
		Entry: Delegation{
			Prompt:     "只写了 prompt 没有 task 的条目用于检查三十字截断效果和后续说明",
			Acceptance: []string{"grep -q foo a.go"},
		},
	})
	if err == nil {
		t.Fatal("DispatchSingleEntry 应拒绝不合规验收命令")
	}
	if bee.ThreadID != "" || bee.WorktreePath != "" {
		t.Fatalf("被拒条目不应启动或建副本: %+v", bee)
	}
	if len(singleNotices) != 1 || !strings.Contains(singleNotices[0], "未启动") || !strings.Contains(singleNotices[0], acceptanceErrGrepQuiet) {
		t.Fatalf("singleNotices=%v", singleNotices)
	}
	if state := m.State("lintsingle"); len(state.Bees) != 0 {
		t.Fatalf("被拒条目不应写入 state.Bees: %+v", state.Bees)
	}
	// 改正后重新派发仍拿到首个花名册名字 Aria。
	fixed, _, err := m.DispatchSingleEntry(ctx, SingleEntryRequest{
		LeadThreadID: "lintsingle",
		ProjectID:    "proj",
		LeadWorkDir:  dir,
		Entry: Delegation{
			Task:       "改正后的任务",
			Prompt:     "只写了 prompt 没有 task 的条目",
			Acceptance: []string{"go test -run TestFoo ./internal/swarm/"},
		},
	})
	if err != nil {
		t.Fatalf("改正后派发失败: %v", err)
	}
	if fixed.Name != RosterName(0) {
		t.Fatalf("被拒条目不应占用名字，改正后期望 %s，实际 %s", RosterName(0), fixed.Name)
	}
}

func TestAcceptanceNoTestsRunFails(t *testing.T) {
	t.Parallel()
	m := NewManager(Options{})
	dir := t.TempDir()

	noTestsCmd := "printf 'ok  \\tmcode/x\\t0.1s [no tests to run]\\n' || go test -run TestMissing ./x"
	passed, detail := m.runAcceptance(context.Background(), "lead", Bee{
		WorktreePath: dir,
		ChangedFiles: []string{"a.go"},
		Acceptance:   []string{noTestsCmd},
	})
	if passed || !strings.Contains(detail, "没有执行任何测试") || !strings.Contains(detail, "[no tests to run]") {
		t.Fatalf("只有 [no tests to run] 时应判失败并附输出: passed=%v detail=%q", passed, detail)
	}

	noFilesCmd := "printf '?   \\tmcode/x\\t[no test files]\\n' || go test -run TestMissing ./x"
	passed, detail = m.runAcceptance(context.Background(), "lead", Bee{
		WorktreePath: dir,
		ChangedFiles: []string{"a.go"},
		Acceptance:   []string{noFilesCmd},
	})
	if passed || !strings.Contains(detail, "没有执行任何测试") {
		t.Fatalf("只有 [no test files] 时应判失败: passed=%v detail=%q", passed, detail)
	}

	mixedPassCmd := "printf 'ok  \\tmcode/x\\t0.1s [no tests to run]\\nok  \\tmcode/y\\t0.2s\\n' || go test -run TestReal ./..."
	passed, detail = m.runAcceptance(context.Background(), "lead", Bee{
		WorktreePath: dir,
		ChangedFiles: []string{"a.go"},
		Acceptance:   []string{mixedPassCmd},
	})
	if !passed || detail != "" {
		t.Fatalf("至少有一个包真正跑了测试时应判通过: passed=%v detail=%q", passed, detail)
	}
}

func TestAcceptancePassedMarkerHiddenFromReport(t *testing.T) {
	t.Parallel()
	report := BuildReport([]Bee{{
		Name:           "Aspen",
		Task:           "改代码",
		Status:         StatusCompleted,
		Merged:         true,
		ReviewState:    ReviewApproved,
		ReviewLayer:    "l2",
		ReviewConcerns: []string{"验收命令通过：2 条", "internal/a.go:12 缺少错误处理"},
	}})
	if strings.Contains(report, "验收命令通过") {
		t.Fatalf("BuildReport 不应把验收通过闸门标记输出为疑点:\n%s", report)
	}
	if !strings.Contains(report, "审查疑点：internal/a.go:12 缺少错误处理") {
		t.Fatalf("BuildReport 应保留普通审查疑点:\n%s", report)
	}
}

func TestAcceptanceFailureReturnsToSameBee(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"off", "ai"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			fake := &funnelFake{}
			m, dir := newFunnelManager(t, fake)
			defer m.Stop()
			settings := m.Settings()
			settings.ReviewMode = mode
			m.SetSettingsFunc(func() Settings { return settings })

			var reports []string
			m.opts.OnReport = func(_ string, report string) error {
				reports = append(reports, report)
				return nil
			}
			driver := m.opts.Driver.(*fakeDriver)
			driver.onFollowUp = func(threadID, _ string) error {
				driver.sessions[threadID] = kanban.SessionState{Running: true}
				return nil
			}

			command := "test -f internal/fixed.txt || (printf 'missing fixed.txt' >&2; exit 2)"
			leadID := "lead-acc-return-" + mode
			bees, err := m.Dispatch(context.Background(), DispatchRequest{
				LeadThreadID: leadID, LeadWorkDir: dir,
				Bees: []BeeSpec{{Name: "Aspen", Task: "修改 internal/feature.go", Acceptance: []string{command}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			bee := bees[0]
			writeBeeFile(t, bee, "internal/feature.go", "package feature\n")
			completeBee(t, m, bee, "第一轮完成")

			h := waitFunnel(t, m, leadID, bee.ThreadID, func(b Bee) bool {
				return b.Status == StatusRunning && b.AcceptanceReturns == 1
			})
			m.wg.Wait()
			m.Tick(context.Background())

			driver.mu.Lock()
			followUps := append([]fakeFollowUp(nil), driver.followUps...)
			driver.mu.Unlock()
			if len(followUps) != 1 || followUps[0].threadID != bee.ThreadID {
				t.Fatalf("期望向原 bee 发送 1 次 followUp，实际 %+v", followUps)
			}
			if !strings.Contains(followUps[0].text, "missing fixed.txt") || !strings.Contains(followUps[0].text, "验收存疑：") {
				t.Fatalf("followUp 文本缺少失败输出或存疑提示: %q", followUps[0].text)
			}
			if h.PendingMerge || h.ReviewState != ReviewNone || h.Summary != "" || h.FinishedAt != nil {
				t.Fatalf("发回运行后字段未重置: %+v", h)
			}
			if len(reports) != 0 {
				t.Fatalf("发回续修期间不应产生汇报: %v", reports)
			}
			if entries, err := os.ReadDir(filepath.Join(m.DataDir(), "swarm-patches")); err == nil && len(entries) > 0 {
				t.Fatalf("首次验收失败发回时不应写入 swarm-patches: %v", entries)
			}

			// 模拟 bee 在当前工作副本内修复并结束第二轮。
			writeBeeFile(t, bee, "internal/fixed.txt", "ok\n")
			completeBee(t, m, bee, "已修复并通过")

			h = waitFunnel(t, m, leadID, bee.ThreadID, func(b Bee) bool { return b.Merged && b.Reported })
			if h.AcceptanceReturns != 1 || h.Status != StatusCompleted {
				t.Fatalf("第二轮修复后状态不符: %+v", h)
			}
			if _, err := os.Stat(filepath.Join(dir, "internal/fixed.txt")); err != nil {
				t.Fatalf("修复文件未合入主工作区: %v", err)
			}
			if len(reports) != 1 {
				t.Fatalf("期望最终产生 1 次汇报，实际 %d", len(reports))
			}
			driver.mu.Lock()
			followUpCount := len(driver.followUps)
			driver.mu.Unlock()
			if followUpCount != 1 {
				t.Fatalf("合并后不应再发 followUp，实际 %d", followUpCount)
			}
		})
	}
}

func TestAcceptanceFailureAfterReturnReportsFailure(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"off", "ai"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			fake := &funnelFake{}
			m, dir := newFunnelManager(t, fake)
			defer m.Stop()
			settings := m.Settings()
			settings.ReviewMode = mode
			m.SetSettingsFunc(func() Settings { return settings })

			var reports []string
			m.opts.OnReport = func(_ string, report string) error {
				reports = append(reports, report)
				return nil
			}
			driver := m.opts.Driver.(*fakeDriver)
			driver.onFollowUp = func(threadID, _ string) error {
				driver.sessions[threadID] = kanban.SessionState{Running: true}
				return nil
			}

			leadID := "lead-acc-fail2-" + mode
			bees, err := m.Dispatch(context.Background(), DispatchRequest{
				LeadThreadID: leadID, LeadWorkDir: dir,
				Bees: []BeeSpec{{Name: "Aspen", Task: "修改 internal/feature.go", Acceptance: []string{"printf 'still failing' >&2; exit 3"}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			bee := bees[0]
			writeBeeFile(t, bee, "internal/feature.go", "package feature\n")
			completeBee(t, m, bee, "第一轮")

			waitFunnel(t, m, leadID, bee.ThreadID, func(b Bee) bool {
				return b.Status == StatusRunning && b.AcceptanceReturns == 1
			})

			completeBee(t, m, bee, "第二轮仍失败")
			h := waitFunnel(t, m, leadID, bee.ThreadID, func(b Bee) bool {
				if mode == "off" {
					return b.Status == StatusFailed && b.Reported
				}
				return isRejected(b) && b.Reported
			})

			driver.mu.Lock()
			followUpCount := len(driver.followUps)
			driver.mu.Unlock()
			if followUpCount != 1 {
				t.Fatalf("第二次失败不应再次 SendFollowUp，实际 %d", followUpCount)
			}
			if h.Merged || h.AcceptanceReturns != 1 {
				t.Fatalf("第二次失败结果不符: %+v", h)
			}
			if len(reports) != 1 || !strings.Contains(reports[0], "still failing") {
				t.Fatalf("第二次失败汇报不符: %v", reports)
			}
			if mode == "off" {
				if _, err := os.Stat(h.ArchivedPatchPath); err != nil {
					t.Fatalf("off 档第二次失败应保存补丁: %v", err)
				}
			} else if !strings.HasPrefix(h.ReviewReason, "验收命令失败：") || !strings.Contains(h.ReviewReason, "still failing") {
				t.Fatalf("ai 档拒绝原因不符: %q", h.ReviewReason)
			}
		})
	}
}

func TestAcceptanceDisputeSkipsRerun(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"off", "ai"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			fake := &funnelFake{}
			m, dir := newFunnelManager(t, fake)
			defer m.Stop()
			settings := m.Settings()
			settings.ReviewMode = mode
			m.SetSettingsFunc(func() Settings { return settings })

			var reports []string
			m.opts.OnReport = func(_ string, report string) error {
				reports = append(reports, report)
				return nil
			}
			driver := m.opts.Driver.(*fakeDriver)
			driver.onFollowUp = func(threadID, _ string) error {
				driver.sessions[threadID] = kanban.SessionState{Running: true}
				return nil
			}

			marker := filepath.Join(t.TempDir(), "acceptance-runs")
			command := "printf x >> '" + filepath.ToSlash(marker) + "'; printf 'wrong command' >&2; exit 1"
			leadID := "lead-acc-dispute-" + mode
			bees, err := m.Dispatch(context.Background(), DispatchRequest{
				LeadThreadID: leadID, LeadWorkDir: dir,
				Bees: []BeeSpec{{Name: "Aspen", Task: "修改 internal/feature.go", Acceptance: []string{command}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			bee := bees[0]
			writeBeeFile(t, bee, "internal/feature.go", "package feature\n")
			completeBee(t, m, bee, "第一轮完成")

			waitFunnel(t, m, leadID, bee.ThreadID, func(b Bee) bool {
				return b.Status == StatusRunning && b.AcceptanceReturns == 1
			})

			completeBee(t, m, bee, "\n验收存疑：验收命令检查了无关模块\n\n保持现有代码。")
			h := waitFunnel(t, m, leadID, bee.ThreadID, func(b Bee) bool {
				if mode == "off" {
					return b.Status == StatusFailed && b.Reported
				}
				return isRejected(b) && b.Reported
			})

			data, err := os.ReadFile(marker)
			if err != nil || string(data) != "x" {
				t.Fatalf("存疑后不应重跑验收命令，marker=%q err=%v", string(data), err)
			}
			if h.Merged || len(reports) != 1 {
				t.Fatalf("存疑状态或汇报次数不符: h=%+v reports=%v", h, reports)
			}
			if !strings.Contains(reports[0], "验收存疑：验收命令检查了无关模块") {
				t.Fatalf("汇报缺少存疑理由:\n%s", reports[0])
			}
			if !strings.Contains(reports[0], "- 提示：bee 认为验收命令有误，先核对验收命令；工作副本已保留") {
				t.Fatalf("汇报缺少存疑提示行:\n%s", reports[0])
			}
			if strings.Contains(reports[0], "补丁处理指引") {
				t.Fatalf("存疑汇报不应输出补丁处理指引:\n%s", reports[0])
			}
			if _, err := os.Stat(h.WorktreePath); err != nil {
				t.Fatalf("存疑后工作副本应保留: %v", err)
			}
		})
	}
}

func TestAcceptanceReturnGuardsAndDisputeParsing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		summary string
		want    string
		ok      bool
	}{
		{"验收存疑：命令路径错误", "命令路径错误", true},
		{"\n  \n验收存疑: bad test\n其余说明", "bad test", true},
		{"验收存疑   缺少理由吗\n第二行", "缺少理由吗", true},
		{"验收存疑：", "", false},
		{"正常汇报\n验收存疑：第二行不算", "", false},
	} {
		got, ok := acceptanceDisputeReason(tc.summary)
		if ok != tc.ok || got != tc.want {
			t.Errorf("acceptanceDisputeReason(%q) = (%q, %v), want (%q, %v)", tc.summary, got, ok, tc.want, tc.ok)
		}
	}

	driver := newFakeDriver()
	driver.onFollowUp = func(string, string) error { return errors.New("send failed") }
	m := NewManager(Options{Driver: driver})
	h := Bee{ThreadID: "bee-1", PendingMerge: true, ReviewState: ReviewRunning, ReviewStage: ReviewStageL0}
	m.leads["lead"] = &leadState{leadThreadID: "lead", bees: []Bee{h}}
	if m.returnAcceptanceFailure(context.Background(), "lead", Bee{ThreadID: "bee-1", ReadOnly: true}, "", "fail") {
		t.Fatal("只读 bee 不应发回")
	}
	if m.returnAcceptanceFailure(context.Background(), "lead", Bee{ThreadID: "bee-1", Summary: "验收存疑：有误"}, "", "fail") {
		t.Fatal("存疑 bee 不应发回")
	}
	if m.returnAcceptanceFailure(context.Background(), "lead", h, "", "fail") {
		t.Fatal("SendFollowUp 失败时应返回 false")
	}
}

func TestAcceptanceRerunOnKeptCopy(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"off", "ai"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			fake := &funnelFake{}
			m, dir := newFunnelManager(t, fake)
			defer m.Stop()
			settings := m.Settings()
			settings.ReviewMode = mode
			m.SetSettingsFunc(func() Settings { return settings })

			var reports []string
			m.opts.OnReport = func(_ string, report string) error {
				reports = append(reports, report)
				return nil
			}

			leadID := "lead-acc-rerun-" + mode
			bees, err := m.Dispatch(context.Background(), DispatchRequest{
				LeadThreadID: leadID, LeadWorkDir: dir,
				Bees: []BeeSpec{{Name: "Aspen", Task: "修改 internal/feature.go", Acceptance: []string{"printf 'wrong check' >&2; exit 1"}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			bee := bees[0]
			// 预置 AcceptanceReturns=1，跳过首次失败发回原 bee，直接落定失败并保留副本。
			m.mu.Lock()
			m.leads[leadID].bees[0].AcceptanceReturns = acceptanceReturnLimit
			m.mu.Unlock()

			writeBeeFile(t, bee, "internal/feature.go", "package feature\n")
			completeBee(t, m, bee, "已完成改动")

			h := waitFunnel(t, m, leadID, bee.ThreadID, func(b Bee) bool {
				if mode == "off" {
					return b.Status == StatusFailed && b.Reported
				}
				return isRejected(b) && b.Reported
			})
			if h.Merged || len(reports) != 1 {
				t.Fatalf("初始失败状态异常: h=%+v reports=%v", h, reports)
			}
			wantRerunHint := "- 重跑验收：若是验收命令写错，改正后派一条 reworkOf=Aspen、rerun=true、带新 acceptance 的条目，在保留副本上重跑，不启动新 bee"
			if !strings.Contains(reports[0], wantRerunHint) {
				t.Fatalf("初始失败汇报缺少重跑验收提示:\n%s", reports[0])
			}

			fake.mu.Lock()
			startsBefore := fake.starts
			fake.mu.Unlock()
			worktreeBefore := h.WorktreePath
			worktreeEntriesBefore, err := os.ReadDir(m.opts.WorktreeDir)
			if err != nil {
				t.Fatal(err)
			}

			passCmd := "test -f internal/feature.go"
			var notices []string
			if mode == "off" {
				var created Bee
				created, notices, err = m.DispatchSingleEntry(context.Background(), SingleEntryRequest{
					LeadThreadID: leadID, LeadWorkDir: dir,
					Entry: Delegation{
						Prompt:     "重跑验收",
						ReworkOf:   bee.Name,
						Rerun:      true,
						Acceptance: []string{passCmd},
					},
				})
				if created.ThreadID != "" {
					t.Fatalf("重跑不应返回新 bee: %+v", created)
				}
			} else {
				var created []Bee
				created, notices, err = m.DispatchFromReply(context.Background(), ReplyDispatchRequest{
					LeadThreadID: leadID, LeadWorkDir: dir,
					ReplyText: "```swarm\n" + `[{"prompt":"重跑验收","reworkOf":"Aspen","rerun":true,"acceptance":["` + passCmd + `"]}]` + "\n```",
				})
				if len(created) != 0 {
					t.Fatalf("重跑不应启动新 bee: %+v", created)
				}
			}
			if err != nil {
				t.Fatalf("重跑验收派发失败: %v", err)
			}
			wantMsg := "已在 Aspen 保留的副本上重跑验收（1 条），结果稍后汇报"
			if len(notices) != 1 || notices[0] != wantMsg {
				t.Fatalf("重跑返回提示异常: %v", notices)
			}

			h = waitFunnel(t, m, leadID, bee.ThreadID, func(b Bee) bool {
				return b.Merged && b.Reported
			})

			fake.mu.Lock()
			startsAfter := fake.starts
			fake.mu.Unlock()
			if startsAfter != startsBefore {
				t.Fatalf("重跑不应启动新 bee: starts %d -> %d", startsBefore, startsAfter)
			}
			worktreeEntriesAfter, err := os.ReadDir(m.opts.WorktreeDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(worktreeEntriesAfter) != len(worktreeEntriesBefore) || h.WorktreePath != worktreeBefore {
				t.Fatalf("重跑不应新建副本: before=%d (%s) after=%d (%s)", len(worktreeEntriesBefore), worktreeBefore, len(worktreeEntriesAfter), h.WorktreePath)
			}
			if !reflect.DeepEqual(h.Acceptance, []string{passCmd}) {
				t.Fatalf("Acceptance 未替换为新命令: %v", h.Acceptance)
			}
			if _, err := os.Stat(filepath.Join(dir, "internal/feature.go")); err != nil {
				t.Fatalf("重跑通过后未合入主工作区: %v", err)
			}
			if len(reports) != 2 {
				t.Fatalf("重跑通过后应重新汇报，实际汇报 %d 次: %v", len(reports), reports)
			}
			if strings.Contains(reports[1], "- 重跑验收：") {
				t.Fatalf("重跑通过后的汇报不应再输出重跑验收提示:\n%s", reports[1])
			}
		})
	}
}

func TestAcceptanceRerunRejected(t *testing.T) {
	t.Parallel()
	fake := &funnelFake{}
	m, dir := newFunnelManager(t, fake)
	defer m.Stop()
	settings := m.Settings()
	settings.ReviewMode = "ai"
	m.SetSettingsFunc(func() Settings { return settings })

	leadID := "lead-acc-rerun-reject"
	bees, err := m.Dispatch(context.Background(), DispatchRequest{
		LeadThreadID: leadID, LeadWorkDir: dir,
		Bees: []BeeSpec{
			{Name: "Aspen", Task: "已删除副本的验收失败", Acceptance: []string{"false"}},
			{Name: "Birch", Task: "非验收失败被驳回"},
			{Name: "Cedar", Task: "仍在运行中"},
			{Name: "Dune", Task: "只读", ReadOnly: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	aspen, birch, cedar, dune := bees[0], bees[1], bees[2], bees[3]
	_ = cedar

	// Aspen：验收失败结束，随后删掉它的工作副本目录。
	if err := os.RemoveAll(aspen.WorktreePath); err != nil {
		t.Fatal(err)
	}
	// Birch：普通 L2 驳回，不是验收失败。
	// Dune：只读且置为已结束。
	m.mu.Lock()
	for i := range m.leads[leadID].bees {
		b := &m.leads[leadID].bees[i]
		switch b.ThreadID {
		case aspen.ThreadID:
			b.Status = StatusCompleted
			b.ReviewState = ReviewRejected
			b.ReviewReason = "验收命令失败：false（退出码 1）"
			b.Reported = true
		case birch.ThreadID:
			b.Status = StatusCompleted
			b.ReviewState = ReviewRejected
			b.ReviewReason = "L2 审查未通过：缺少测试"
			b.Reported = true
		case dune.ThreadID:
			b.Status = StatusFailed
			b.MergeError = "验收命令失败：x"
			b.Reported = true
		}
	}
	m.mu.Unlock()

	fake.mu.Lock()
	startsBefore := fake.starts
	fake.mu.Unlock()

	cases := []struct {
		name string
		d    Delegation
		want string
	}{
		{
			name: "missing target",
			d:    Delegation{Prompt: "重跑", ReworkOf: "NoSuchBee", Rerun: true, Acceptance: []string{"true"}},
			want: "未找到子代理",
		},
		{
			name: "reclaimed copy",
			d:    Delegation{Prompt: "重跑", ReworkOf: "Aspen", Rerun: true, Acceptance: []string{"true"}},
			want: "副本已回收",
		},
		{
			name: "not acceptance failure",
			d:    Delegation{Prompt: "重跑", ReworkOf: "Birch", Rerun: true, Acceptance: []string{"true"}},
			want: "不是因验收失败结束",
		},
		{
			name: "running target",
			d:    Delegation{Prompt: "重跑", ReworkOf: "Cedar", Rerun: true, Acceptance: []string{"true"}},
			want: "仍在运行或等待审查",
		},
		{
			name: "readonly target",
			d:    Delegation{Prompt: "重跑", ReworkOf: "Dune", Rerun: true, Acceptance: []string{"true"}},
			want: "只读子代理",
		},
		{
			name: "grep -q rejected",
			d:    Delegation{Prompt: "重跑", ReworkOf: "Aspen", Rerun: true, Acceptance: []string{"grep -q foo bar.txt"}},
			want: "不要用 grep -q",
		},
		{
			name: "empty acceptance",
			d:    Delegation{Prompt: "重跑", ReworkOf: "Aspen", Rerun: true},
			want: "未提供新的 acceptance",
		},
		{
			name: "empty reworkOf",
			d:    Delegation{Prompt: "重跑", Rerun: true, Acceptance: []string{"true"}},
			want: "未指定 reworkOf",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name+"/SingleEntry", func(t *testing.T) {
			_, _, err := m.DispatchSingleEntry(context.Background(), SingleEntryRequest{
				LeadThreadID: leadID, LeadWorkDir: dir, Entry: tc.d,
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("期望包含 %q 的错误，实际: %v", tc.want, err)
			}
		})
		t.Run(tc.name+"/FromReply", func(t *testing.T) {
			payload, _ := json.Marshal([]map[string]any{{
				"prompt": tc.d.Prompt, "reworkOf": tc.d.ReworkOf, "rerun": true, "acceptance": tc.d.Acceptance,
			}})
			_, _, err := m.DispatchFromReply(context.Background(), ReplyDispatchRequest{
				LeadThreadID: leadID, LeadWorkDir: dir,
				ReplyText: "```swarm\n" + string(payload) + "\n```",
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("FromReply 期望包含 %q 的错误，实际: %v", tc.want, err)
			}
		})
	}

	fake.mu.Lock()
	startsAfter := fake.starts
	fake.mu.Unlock()
	if startsAfter != startsBefore {
		t.Fatalf("被拒绝的重跑不应启动新 bee: %d -> %d", startsBefore, startsAfter)
	}
}

func TestAcceptanceRerunWithoutChangesRejected(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"off", "ai"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			fake := &funnelFake{}
			m, dir := newFunnelManager(t, fake)
			defer m.Stop()
			settings := m.Settings()
			settings.ReviewMode = mode
			m.SetSettingsFunc(func() Settings { return settings })

			m.opts.OnReport = func(_ string, _ string) error {
				return nil
			}

			leadID := "lead-acc-rerun-empty-" + mode
			bees, err := m.Dispatch(context.Background(), DispatchRequest{
				LeadThreadID: leadID, LeadWorkDir: dir,
				Bees: []BeeSpec{{Name: "Aspen", Task: "修改 internal/feature.go", Acceptance: []string{"printf 'wrong check' >&2; exit 1"}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			bee := bees[0]
			m.mu.Lock()
			m.leads[leadID].bees[0].AcceptanceReturns = acceptanceReturnLimit
			m.mu.Unlock()

			writeBeeFile(t, bee, "internal/feature.go", "package feature\n")
			completeBee(t, m, bee, "已完成改动")

			before := waitFunnel(t, m, leadID, bee.ThreadID, func(b Bee) bool {
				if mode == "off" {
					return b.Status == StatusFailed && b.Reported
				}
				return isRejected(b) && b.Reported
			})

			// 还原副本里的改动，使副本相对基线没有可合并改动。
			if err := os.Remove(filepath.Join(before.WorktreePath, "internal/feature.go")); err != nil {
				t.Fatal(err)
			}

			if mode == "off" {
				_, _, err = m.DispatchSingleEntry(context.Background(), SingleEntryRequest{
					LeadThreadID: leadID, LeadWorkDir: dir,
					Entry: Delegation{
						Prompt:     "重跑验收",
						ReworkOf:   bee.Name,
						Rerun:      true,
						Acceptance: []string{"true"},
					},
				})
			} else {
				_, _, err = m.DispatchFromReply(context.Background(), ReplyDispatchRequest{
					LeadThreadID: leadID, LeadWorkDir: dir,
					ReplyText: "```swarm\n" + `[{"prompt":"重跑验收","reworkOf":"Aspen","rerun":true,"acceptance":["true"]}]` + "\n```",
				})
			}
			if err == nil || !strings.Contains(err.Error(), "没有可合并的改动") {
				t.Fatalf("期望返回含 %q 的错误，实际: %v", "没有可合并的改动", err)
			}

			var after Bee
			found := false
			for _, b := range m.State(leadID).Bees {
				if b.ThreadID == bee.ThreadID {
					after = b
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("未找到目标 bee %s", bee.ThreadID)
			}
			if after.Status != before.Status {
				t.Fatalf("Status 应保持不变: got=%v want=%v", after.Status, before.Status)
			}
			if after.MergeError != before.MergeError {
				t.Fatalf("MergeError 应保持不变: got=%q want=%q", after.MergeError, before.MergeError)
			}
			if after.ReviewReason != before.ReviewReason {
				t.Fatalf("ReviewReason 应保持不变: got=%q want=%q", after.ReviewReason, before.ReviewReason)
			}
			if !reflect.DeepEqual(after.Acceptance, before.Acceptance) {
				t.Fatalf("Acceptance 应保持不变: got=%v want=%v", after.Acceptance, before.Acceptance)
			}
		})
	}
}

func TestAcceptanceRejectedCopyKeptForRerun(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	finished := now.Add(-beeCopyReclaimIdle - time.Minute)

	m := NewManager(Options{WorktreeDir: t.TempDir()})
	m.SetSettingsFunc(func() Settings {
		return Settings{Enabled: true, ReviewMode: "ai", AutoClearFinished: true}
	})

	accFailed := &Bee{
		ThreadID:     "bee-acc-fail",
		Name:         "Aspen",
		Status:       StatusCompleted,
		WorktreePath: t.TempDir(),
		FinishedAt:   &finished,
		Reported:     true,
		ReviewState:  ReviewRejected,
		ReviewReason: "验收命令失败：go test -run TestX ./p（退出码 1）",
	}
	accDisputed := &Bee{
		ThreadID:     "bee-acc-dispute",
		Name:         "Birch",
		Status:       StatusCompleted,
		WorktreePath: t.TempDir(),
		FinishedAt:   &finished,
		Reported:     true,
		ReviewState:  ReviewRejected,
		ReviewReason: "验收存疑：测试名字写错",
	}
	otherRejected := &Bee{
		ThreadID:     "bee-l2-reject",
		Name:         "Cedar",
		Status:       StatusCompleted,
		WorktreePath: t.TempDir(),
		FinishedAt:   &finished,
		Reported:     true,
		ReviewState:  ReviewRejected,
		ReviewReason: "L2 审查未通过：缺少测试",
	}

	if m.beeCopyReclaimableLocked(accFailed, now) {
		t.Fatal("ai 档因验收命令失败被驳回的副本在 10 分钟后不应被直接回收")
	}
	if m.beeCopyReclaimableLocked(accDisputed, now) {
		t.Fatal("ai 档因验收存疑被驳回的副本在 10 分钟后不应被直接回收")
	}
	if !m.beeCopyReclaimableLocked(otherRejected, now) {
		t.Fatal("非验收原因被驳回的副本在超过 beeCopyReclaimIdle 后仍应直接回收")
	}
	if d := m.beeCopyReclaimDecisionLocked(accFailed, finished.Add(beeCopyStaleIdle)); d != beeCopyArchive {
		t.Fatalf("超过 24 小时后应进入存档回收，实际 %v", d)
	}

	m.mu.Lock()
	m.leads["lead-keep"] = &leadState{leadThreadID: "lead-keep", bees: []Bee{*accFailed}}
	m.mu.Unlock()
	m.autoClearFinished(context.Background())
	if got := len(m.State("lead-keep").Bees); got != 1 {
		t.Fatalf("AutoClearFinished 不应提前删掉验收失败待重跑的 bee，实际剩 %d 个", got)
	}
}
