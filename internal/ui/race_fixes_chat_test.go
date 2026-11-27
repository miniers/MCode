package mcodeui

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
	"mcode/internal/git"
	"mcode/internal/testenv"
)

func TestRaceQuickRepliesRender(t *testing.T) {
	testenv.TempHome(t)
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	st := NewSettingsState(t.TempDir(), nil, s)
	setChatSettings(s, st)
	t.Cleanup(func() { setChatSettings(s, nil) })

	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.threads = []Thread{{ID: "t1", ProjectID: "p1", IsRunning: false}}
	s.messages = []Message{{ID: "m1", Role: "user", Text: "先做个开头", TurnIndex: 1}}
	s.draft = ""
	s.mu.Unlock()

	st.mu.Lock()
	st.cfgMu.Lock()
	st.cfg.QuickReplies = []config.QuickReply{{Label: "继续", Message: "继续按计划推进"}}
	st.cfgMu.Unlock()
	st.mu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) {
		renderQuickReplies(c, s)
	}, 800, 200)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 200 {
			st.mu.Lock()
			st.cfgMu.Lock()
			if i%2 == 0 {
				st.cfg.QuickReplies = []config.QuickReply{
					{Label: "继续", Message: "继续按计划推进"},
				}
			} else {
				st.cfg.QuickReplies = []config.QuickReply{
					{Label: "复查", Message: "请复查改动"},
					{Label: "总结", Message: "总结现状"},
				}
			}
			st.cfgMu.Unlock()
			st.mu.Unlock()
		}
	}()

	for {
		tt.Frame()
		select {
		case <-done:
			tt.Frame()
			return
		default:
		}
	}
}

func TestRaceSessionStatsCollect(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)

	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.threads = []Thread{{ID: "t1", Provider: "openai", Model: "gpt-test"}}
	s.messages = []Message{{ID: "m1", Role: "assistant", TurnIndex: 1, TurnStartedAt: "2026-11-06T08:00:00Z", TurnCompletedAt: "2026-11-06T08:00:10Z"}}
	s.threadUsage = ThreadUsage{
		Requests:  1,
		ToolCalls: 2,
		Total:     TokenUsage{Input: 100, Output: 50},
		Turns: map[int]TokenUsage{
			1: {Input: 100, Output: 50, DurationMs: 1000, Requests: []RequestSpeed{{OutputTokens: 50, DurationMs: 1000}}},
		},
		TopTools: []ToolCallCount{{Name: "read_file", Count: 2}},
	}
	s.providers = []ProviderInfo{{
		ID: "openai",
		Models: []ModelInfo{{
			ID:      "gpt-test",
			Efforts: []string{"low", "high"},
			Cost:    &ModelCost{Input: 1.0, Output: 2.0},
		}},
	}}
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 200 {
			s.mu.Lock()
			if s.threadUsage.Turns == nil {
				s.threadUsage.Turns = make(map[int]TokenUsage)
			}
			s.threadUsage.Turns[i%4] = TokenUsage{
				Input:      int64(100 + i),
				Output:     int64(50 + i),
				DurationMs: 1000,
				Requests:   []RequestSpeed{{OutputTokens: int64(50 + i), DurationMs: 1000}},
			}
			s.threadUsage.TopTools = []ToolCallCount{{Name: fmt.Sprintf("tool_%d", i%3), Count: i + 1}}
			s.threadUsage.Total = TokenUsage{Input: int64(100 + i), Output: int64(50 + i)}
			s.providers = []ProviderInfo{{
				ID: "openai",
				Models: []ModelInfo{{
					ID:      "gpt-test",
					Efforts: []string{"medium", "high"},
					Cost:    &ModelCost{Input: float64(1 + i%3), Output: 2.0},
				}},
			}}
			s.mu.Unlock()
		}
	}()

	for {
		summary := collectSessionStats(s, "t1", time.Now())
		_ = len(summary.usage.Turns)
		_ = len(summary.topTools)
		_ = summary.speed
		_ = summary.costUSD
		select {
		case <-done:
			return
		default:
		}
	}
}

func TestRaceStewardPickerTiers(t *testing.T) {
	testenv.TempHome(t)
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	st := NewSettingsState(t.TempDir(), nil, s)
	st.stewardSecrets = config.NewMemorySecretStore()
	setChatSettings(s, st)
	t.Cleanup(func() { setChatSettings(s, nil) })

	cat := modelPickerCatalog{
		Providers: []ProviderInfo{
			{ID: "openai", Name: "OpenAI", Models: []ModelInfo{{ID: "gpt-test", Name: "GPT Test"}}},
			{ID: "anthropic", Name: "Anthropic", Models: []ModelInfo{{ID: "claude-test", Name: "Claude Test"}}},
		},
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 200 {
			st.mu.Lock()
			st.cfgMu.Lock()
			if i%2 == 0 {
				st.cfg.Orchestration.Steward.Provider = "openai"
				st.cfg.Orchestration.Steward.Model = "gpt-test"
				st.cfg.Orchestration.Tiers = []config.SwarmTier{
					{ID: "read", Name: "只读模式"},
					{ID: "edit", Name: "编码模式"},
				}
			} else {
				st.cfg.Orchestration.Steward.Provider = "anthropic"
				st.cfg.Orchestration.Steward.Model = "claude-test"
				st.cfg.Orchestration.Tiers = []config.SwarmTier{
					{ID: "read", Name: "快速只读"},
					{ID: "deep", Name: "深度推演"},
				}
			}
			st.cfgMu.Unlock()
			st.mu.Unlock()
		}
	}()

	for {
		_ = st.stewardAutoLabel(cat, ModelSelection{})
		_ = swarmTierName(s, "read")
		select {
		case <-done:
			return
		default:
		}
	}
}

func waitComposerPRSettledWithoutFrame(t *testing.T, s *chatState, threadID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		composerPRBarMu.Lock()
		st := composerPRBarStates[s]
		var done bool
		if st != nil && st.threads[threadID] != nil {
			done = st.threads[threadID].loadedOnce && !st.threads[threadID].loading
		}
		composerPRBarMu.Unlock()
		if done {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("等待 PR 状态加载超时")
}

func TestComposerPRBarReduceMotionJumps(t *testing.T) {
	fake := newComposerPRFlowFake()
	fake.setPRState("t1", git.PRThreadState{}, false, nil)

	s, tt := composerPRTestHarness(t, fake, Thread{ID: "t1", Title: "审查 PR #31"}, true)
	waitComposerPRLoaded(t, tt, s, "t1")
	if tt.HasText("#31") {
		t.Fatalf("初始非 PR 状态不应显示 PR 条: %v", tt.Texts())
	}
	if tt.FramePending() {
		t.Fatal("初始非 PR 状态不应请求下一帧")
	}

	fake.setPRState("t1", git.PRThreadState{Number: 31, LocalCommits: 1}, true, nil)
	refreshComposerPRThreadState(s, "t1")
	waitComposerPRSettledWithoutFrame(t, s, "t1")
	tt.Frame()
	if !tt.HasText("#31") || !tt.HasText("推送并合并") {
		t.Fatalf("减少动效下切换显示 PR 条后单帧应立即呈现: %v", tt.Texts())
	}
	if tt.FramePending() {
		t.Fatal("减少动效下显示 PR 条推进一帧后不应再请求下一帧")
	}

	fake.setPRState("t1", git.PRThreadState{}, false, nil)
	refreshComposerPRThreadState(s, "t1")
	waitComposerPRSettledWithoutFrame(t, s, "t1")
	tt.Frame()
	if tt.HasText("#31") {
		t.Fatalf("减少动效下隐藏 PR 条后单帧应立即收起: %v", tt.Texts())
	}
	if tt.FramePending() {
		t.Fatal("减少动效下隐藏 PR 条推进一帧后不应再请求下一帧")
	}
}
