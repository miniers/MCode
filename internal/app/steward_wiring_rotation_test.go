package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/config"
	"mcode/internal/kanban"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	"mcode/internal/steward"
	"mcode/internal/store"
	"mcode/internal/swarm"
)

// fakeStewardSessions 是总管会话能力的测试替身：只报告会话已存在，不真的建会话或发消息。
type fakeStewardSessions struct{}

func (fakeStewardSessions) StartSession(context.Context, kanban.SessionRequest) (string, error) {
	return "steward-new", nil
}

func (fakeStewardSessions) SessionState(context.Context, string) (kanban.SessionState, error) {
	return kanban.SessionState{}, nil
}

func (fakeStewardSessions) SendSwarmReport(context.Context, string, string) error { return nil }

func (fakeStewardSessions) SendHostEvent(context.Context, string, string) error { return nil }

func (fakeStewardSessions) SendStewardWake(context.Context, string, string) error { return nil }

func (fakeStewardSessions) AppendAssistantMessage(string, string) error { return nil }

// newRotationStewardRuntime 构造一个已认领主会话的总管运行时：设置里记着 steward-1，
// Ensure 复用该会话后 IsSteward 立即可用；换新阈值由 cfg 决定。
func newRotationStewardRuntime(t *testing.T, cfg config.StewardConfig) *steward.Runtime {
	t.Helper()
	settings := newFakeStewardSettings()
	if err := settings.SetSetting(context.Background(), "steward.thread_id", "steward-1"); err != nil {
		t.Fatalf("准备总管会话 ID 失败: %v", err)
	}
	rt := steward.New(steward.Deps{
		Sessions: fakeStewardSessions{},
		Settings: settings,
		Config:   func() config.StewardConfig { return cfg },
		DataDir:  t.TempDir(),
	})
	t.Cleanup(rt.Stop)
	id, err := rt.Ensure(context.Background())
	if err != nil {
		t.Fatalf("准备总管会话失败: %v", err)
	}
	if id != "steward-1" {
		t.Fatalf("总管会话 ID = %q，期望 steward-1", id)
	}
	return rt
}

// TestStewardSessionRotator 验证换新钩子：非总管会话一律不换新也不读配置；
// 总管主会话空闲超过阈值时换新并带回账本小标题的前言，未到阈值时不换新。
func TestStewardSessionRotator(t *testing.T) {
	ctx := context.Background()
	// 空闲换新默认关闭，这里显式配置一小时阈值来验证换新钩子。
	rt := newRotationStewardRuntime(t, config.StewardConfig{Enabled: true, Name: "总管", RotateIdleMinutes: 60})
	rotate := stewardSessionRotator(rt)

	t.Run("空运行时与非总管会话都不换新", func(t *testing.T) {
		if got, preamble := stewardSessionRotator(nil)(ctx, rotationInfo("steward-1")); got || preamble != "" {
			t.Fatalf("nil 运行时不应换新，实际 %v %q", got, preamble)
		}
		if got, preamble := rotate(ctx, rotationInfo("lead-1")); got || preamble != "" {
			t.Fatalf("非总管会话不应换新，实际 %v %q", got, preamble)
		}
	})

	t.Run("空闲两小时换新并带前言", func(t *testing.T) {
		info := rotationInfo("steward-1")
		info.Idle = 2 * time.Hour
		got, preamble := rotate(ctx, info)
		if !got {
			t.Fatal("总管主会话空闲两小时应当换新")
		}
		if strings.TrimSpace(preamble) == "" {
			t.Fatal("换新前言不应为空")
		}
		if !strings.Contains(preamble, "## 账本") {
			t.Fatalf("换新前言应含账本小标题，实际 %q", preamble)
		}
	})

	t.Run("空闲五分钟且上下文很小不换新", func(t *testing.T) {
		info := rotationInfo("steward-1")
		info.Idle = 5 * time.Minute
		info.ContextTokens = 1000
		if got, preamble := rotate(ctx, info); got || preamble != "" {
			t.Fatalf("未到阈值不应换新，实际 %v %q", got, preamble)
		}
	})
}

// rotationInfo 返回指定会话的换新询问参数，默认空闲与上下文都为 0（未知）。
func rotationInfo(threadID string) remote.SessionRotationInfo {
	return remote.SessionRotationInfo{ThreadID: threadID}
}

// TestRotationBeeLinesMapsAndOrders 验证子代理行按开始时间新到旧排、状态翻成中文、
// 合并失败优先，并且不改动编排状态给的入参切片。
func TestRotationBeeLinesMapsAndOrders(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	bees := []swarm.Bee{
		{Name: "bee-old", Task: "改旧流程", Status: swarm.StatusCompleted, StartedAt: base, LinesAdded: 3, LinesDeleted: 1},
		{Name: "bee-new", Task: "改新流程", Status: swarm.StatusRunning, StartedAt: base.Add(2 * time.Hour), LinesAdded: 10, LinesDeleted: 2},
		{Name: "bee-mid", Task: "改中流程", Status: swarm.StatusFailed, StartedAt: base.Add(time.Hour), Interrupted: true},
	}

	got := rotationBeeLines(bees)
	if len(got) != len(bees) {
		t.Fatalf("子代理行数 = %d，期望 %d", len(got), len(bees))
	}
	wantNames := []string{"bee-new", "bee-mid", "bee-old"}
	wantStatus := []string{"运行中", "已打断", "已完成"}
	for i := range wantNames {
		if got[i].Name != wantNames[i] {
			t.Fatalf("第 %d 行子代理 = %q，期望 %q", i, got[i].Name, wantNames[i])
		}
		if got[i].Status != wantStatus[i] {
			t.Fatalf("%s 的状态 = %q，期望 %q", wantNames[i], got[i].Status, wantStatus[i])
		}
	}
	if got[2].Task != "改旧流程" || got[2].LinesAdded != 3 || got[2].LinesDeleted != 1 {
		t.Fatalf("子代理行应直接取任务与增删行数，得到 %+v", got[2])
	}

	mergeFailed := rotationBeeLines([]swarm.Bee{{Name: "bee-merge", Status: swarm.StatusRunning, MergeError: "与主工作区冲突"}})
	if len(mergeFailed) != 1 || mergeFailed[0].Status != "合并失败" {
		t.Fatalf("合并失败应显示为「合并失败」，得到 %+v", mergeFailed)
	}

	if bees[0].Name != "bee-old" || bees[2].Name != "bee-mid" {
		t.Fatalf("入参切片顺序不应被改动，得到 %s、%s", bees[0].Name, bees[2].Name)
	}
}

// TestRotationBeeStatus 验证子代理各运行时状态翻成中文的对应关系：
// 合并失败与已打断优先于运行状态，其余按失败、完成、已停止、运行中区分。
func TestRotationBeeStatus(t *testing.T) {
	cases := []struct {
		name string
		bee  swarm.Bee
		want string
	}{
		{name: "运行中", bee: swarm.Bee{Status: swarm.StatusRunning}, want: "运行中"},
		{name: "已完成", bee: swarm.Bee{Status: swarm.StatusCompleted}, want: "已完成"},
		{name: "未打断的失败", bee: swarm.Bee{Status: swarm.StatusFailed}, want: "失败"},
		{name: "已停止", bee: swarm.Bee{Status: swarm.StatusStopped}, want: "已停止"},
		{name: "被打断", bee: swarm.Bee{Status: swarm.StatusFailed, Interrupted: true}, want: "已打断"},
		{name: "合并失败优先", bee: swarm.Bee{Status: swarm.StatusCompleted, MergeError: "与主工作区冲突"}, want: "合并失败"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rotationBeeStatus(tc.bee); got != tc.want {
				t.Fatalf("状态 = %q，期望 %q", got, tc.want)
			}
		})
	}
}

// TestRotationBeeLinesCapsAtMax 验证子代理超过上限时只保留最新的一批。
func TestRotationBeeLinesCapsAtMax(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	bees := make([]swarm.Bee, 0, steward.RotationBeesMax+2)
	for i := 0; i < steward.RotationBeesMax+2; i++ {
		bees = append(bees, swarm.Bee{Name: fmt.Sprintf("bee-%d", i), StartedAt: base.Add(time.Duration(i) * time.Minute)})
	}

	got := rotationBeeLines(bees)
	if len(got) != steward.RotationBeesMax {
		t.Fatalf("子代理行数 = %d，期望截到 %d", len(got), steward.RotationBeesMax)
	}
	if got[0].Name != "bee-6" || got[len(got)-1].Name != "bee-2" {
		t.Fatalf("截断后应保留最新的一批，得到 %s 到 %s", got[0].Name, got[len(got)-1].Name)
	}
}

// TestStewardRecentBeesNilManager 验证编排管理器未构造时返回 nil 函数，总管据此按未接线处理。
func TestStewardRecentBeesNilManager(t *testing.T) {
	if stewardRecentBees(nil) != nil {
		t.Fatal("编排管理器为 nil 时应返回 nil 查询函数")
	}
}

// TestStewardRuntimeCarriesRecentBees 验证接线参数里的子代理查询会透传给总管运行时，
// 换新前言因此带上最近派出的子代理小节。
func TestStewardRuntimeCarriesRecentBees(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "steward.db"))
	if err != nil {
		t.Fatalf("打开存储失败: %v", err)
	}
	hub := remote.NewStreamHub(remote.StreamHubOptions{Interval: time.Hour})
	backend := remote.NewMemoryBackend(hub)
	// 先登记好总管会话：Ensure 读到 setting 里的 steward-1 时能直接复用它，不再新建会话。
	backend.AddThread(wire.ThreadSummary{ID: "steward-1"}, nil)
	t.Cleanup(func() {
		backend.Close()
		hub.Close()
		if err := st.Close(); err != nil {
			t.Logf("关闭存储失败: %v", err)
		}
	})
	if err := st.SetSetting(context.Background(), "steward.thread_id", "steward-1"); err != nil {
		t.Fatalf("准备总管会话 ID 失败: %v", err)
	}
	// 运行时每次从数据目录现读配置，先落一份已启用的配置，Ensure 才会复用已有会话。
	dataDir := t.TempDir()
	cfg := config.Defaults()
	cfg.Orchestration.Steward.Enabled = true
	cfg.Orchestration.Steward.Name = "总管"
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
	var gotLead string
	rt := newStewardRuntime(stewardWiringOptions{
		Backend:  backend,
		Settings: st,
		DataDir:  dataDir,
		RecentBees: func(leadThreadID string) []steward.RotationBee {
			gotLead = leadThreadID
			return []steward.RotationBee{{Name: "bee-wired", Task: "改登录流程", Status: "已完成", LinesAdded: 5}}
		},
	})
	t.Cleanup(rt.Stop)
	if _, err := rt.Ensure(context.Background()); err != nil {
		t.Fatalf("准备总管会话失败: %v", err)
	}

	preamble := rt.RotationPreamble(context.Background())
	for _, want := range []string{"## 最近派出的子代理", "bee-wired", "改登录流程", "已完成"} {
		if !strings.Contains(preamble, want) {
			t.Fatalf("换新前言缺少 %q:\n%s", want, preamble)
		}
	}
	if gotLead != "steward-1" {
		t.Fatalf("子代理查询的 lead 会话 = %q，期望 steward-1", gotLead)
	}
}
