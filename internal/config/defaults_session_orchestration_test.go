package config

import "testing"

func TestSessionOrchestrationDefaultsAndDisabledRoundTrip(t *testing.T) {
	cfg := Defaults()
	if cfg.Session.PinSentMessage == nil || *cfg.Session.PinSentMessage || cfg.Session.ConciseRepliesEnabled == nil || *cfg.Session.ConciseRepliesEnabled || !cfg.Session.QueueWhileRunning || !cfg.Session.InsertIntoRunningTurn {
		t.Fatalf("会话默认值不正确: %+v", cfg.Session)
	}
	if !cfg.Providers.Pi.Enabled || cfg.Providers.Pi.Bin != "" {
		t.Fatalf("提供方默认值不正确: %+v", cfg.Providers)
	}
	o := cfg.Orchestration
	if o.MaxBees != 4 || o.MaxRounds != 10 || o.BeeTimeoutMinutes != 60 || !o.RetryFailed || !o.AutoMerge || !o.LeadPlansOnly || !o.DelegateTool || !o.AutoClearFinished || !o.AutoHidesIdleBees || !o.AutoPopsBees || o.AutoForwardQuestionSeconds != 180 {
		t.Fatalf("编排默认值不正确: %+v", o)
	}
	if o.Steward.Name != "MoCo" || o.Steward.Avatar != "🤖" || o.Steward.QuietStart != "00:00" || o.Steward.QuietEnd != "06:00" {
		t.Fatalf("总管默认值不正确: %+v", o.Steward)
	}
	if (SessionConfig{}).PinSentMessageEnabled() || (SessionConfig{}).ConciseReplies() {
		t.Fatal("会话空指针应视为关闭")
	}

	dir := t.TempDir()
	if err := Update(dir, func(c *Config) error {
		c.Session.PinSentMessage = boolPtr(false)
		c.Session.ConciseRepliesEnabled = boolPtr(false)
		c.Orchestration.AutoForwardQuestionSeconds = 0
		c.Orchestration.DelegateTool = false
		c.Orchestration.Steward.Name = ""
		c.Orchestration.Steward.Avatar = ""
		c.Orchestration.Steward.QuietStart = ""
		c.Orchestration.Steward.QuietEnd = ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Session.PinSentMessage == nil || *loaded.Session.PinSentMessage || loaded.Session.ConciseRepliesEnabled == nil || *loaded.Session.ConciseRepliesEnabled || loaded.Orchestration.AutoForwardQuestionSeconds != 0 || loaded.Orchestration.DelegateTool {
		t.Fatalf("关闭设置未保持: %+v", loaded)
	}
	s := loaded.Orchestration.Steward
	if s.Name != "" || s.Avatar != "" || s.QuietStart != "" || s.QuietEnd != "" {
		t.Fatalf("总管空设置未保持: %+v", s)
	}
}
