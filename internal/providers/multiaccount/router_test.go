package multiaccount

import (
	"sync"
	"testing"
	"time"
)

func enabled(states ...AccountState) []AccountState {
	for i := range states {
		states[i].Enabled = true
	}
	return states
}

func TestConfig_Normalize(t *testing.T) {
	tests := []struct {
		name string
		in   Config
		want Config
	}{
		{
			name: "default values",
			in:   Config{},
			want: Config{Strategy: StrategySmart, Sink: false, Affinity: AffinitySession},
		},
		{
			name: "unknown strategy and affinity",
			in:   Config{Strategy: "unknown", Sink: true, Affinity: "invalid"},
			want: Config{Strategy: StrategySmart, Sink: true, Affinity: AffinitySession},
		},
		{
			name: "valid custom values",
			in:   Config{Strategy: StrategyOrder, Sink: true, Affinity: AffinityStrict},
			want: Config{Strategy: StrategyOrder, Sink: true, Affinity: AffinityStrict},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.in.Normalize()
			if got != tt.want {
				t.Errorf("Normalize() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestRouter_ErrNoAccount(t *testing.T) {
	r := NewRouter(nil, nil)
	now := time.Now()

	// 1. 空候选人列表
	_, err := r.Pick(DefaultConfig(), "", nil, nil, now)
	if err != ErrNoAccount {
		t.Errorf("want ErrNoAccount, got %v", err)
	}

	// 2. 所有候选人均处于冷却中
	r.MarkLimited("acc-1", now.Add(10*time.Minute), now)
	_, err = r.Pick(DefaultConfig(), "", enabled(AccountState{ID: "acc-1"}), nil, now)
	if err != ErrNoAccount {
		t.Errorf("want ErrNoAccount when all cooling, got %v", err)
	}

	// 3. 账号未启用
	_, err = r.Pick(DefaultConfig(), "", []AccountState{{ID: "acc-off"}}, nil, now)
	if err != ErrNoAccount {
		t.Errorf("want ErrNoAccount when all disabled, got %v", err)
	}
}

func TestRouter_Strategies(t *testing.T) {
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)

	t.Run("smart", func(t *testing.T) {
		r := NewRouter(nil, nil)
		cfg := Config{Strategy: StrategySmart}

		// A: fine 档 (80% used), ResetAt 2 小时后
		// B: fine 档 (80% used), ResetAt 1 小时后
		// C: low 档 (95% used)
		// D: spent 档 (99% used)
		// E: fine 档 (80% used), ResetAt 为零值
		cands := []AccountState{
			{ID: "A", UsedPercent: 80, ResetAt: now.Add(2 * time.Hour), Order: 1},
			{ID: "B", UsedPercent: 80, ResetAt: now.Add(1 * time.Hour), Order: 2},
			{ID: "C", UsedPercent: 95, Order: 3},
			{ID: "D", UsedPercent: 99, Order: 4},
			{ID: "E", UsedPercent: 80, ResetAt: time.Time{}, Order: 0},
		}

		// fine 档中 B 重置时间最早 (1h)，排在最前
		id, err := r.Pick(cfg, "", enabled(cands...), nil, now)
		if err != nil || id != "B" {
			t.Errorf("smart pick 1: want B, got %s (err: %v)", id, err)
		}

		// 排除 B 后选 A
		candsWithoutB := []AccountState{cands[0], cands[2], cands[3], cands[4]}
		id, err = r.Pick(cfg, "", enabled(candsWithoutB...), nil, now)
		if err != nil || id != "A" {
			t.Errorf("smart pick 2: want A, got %s (err: %v)", id, err)
		}

		// 排除 A 后选 E (fine 档虽然零值，但仍高于 low 档 C)
		candsWithoutAB := []AccountState{cands[2], cands[3], cands[4]}
		id, err = r.Pick(cfg, "", enabled(candsWithoutAB...), nil, now)
		if err != nil || id != "E" {
			t.Errorf("smart pick 3: want E, got %s (err: %v)", id, err)
		}

		// 排除 E 后进入 low 档选 C
		candsWithoutABE := []AccountState{cands[2], cands[3]}
		id, err = r.Pick(cfg, "", enabled(candsWithoutABE...), nil, now)
		if err != nil || id != "C" {
			t.Errorf("smart pick 4: want C, got %s (err: %v)", id, err)
		}

		// 最后兜底 spent 档 D
		id, err = r.Pick(cfg, "", enabled(cands[3]), nil, now)
		if err != nil || id != "D" {
			t.Errorf("smart pick 5: want D, got %s (err: %v)", id, err)
		}
	})

	t.Run("order", func(t *testing.T) {
		r := NewRouter(nil, nil)
		cfg := Config{Strategy: StrategyOrder}

		cands := []AccountState{
			{ID: "A", UsedPercent: 50, Order: 2},
			{ID: "B", UsedPercent: 60, Order: 1},
			{ID: "C", UsedPercent: 100, Order: 0},
		}

		// C 用尽 (>=100%)，B 和 A 可用，B 的 Order 小于 A，应选 B
		id, err := r.Pick(cfg, "", enabled(cands...), nil, now)
		if err != nil || id != "B" {
			t.Errorf("order pick: want B, got %s (err: %v)", id, err)
		}

		// 全用尽时选 Order 最小的 C
		allFull := []AccountState{
			{ID: "A", UsedPercent: 100, Order: 2},
			{ID: "C", UsedPercent: 100, Order: 0},
		}
		id, err = r.Pick(cfg, "", enabled(allFull...), nil, now)
		if err != nil || id != "C" {
			t.Errorf("order pick all full: want C, got %s (err: %v)", id, err)
		}
	})

	t.Run("rotate", func(t *testing.T) {
		r := NewRouter(nil, nil)
		cfg := Config{Strategy: StrategyRotate}

		cands := enabled(
			AccountState{ID: "A"},
			AccountState{ID: "B"},
			AccountState{ID: "C"},
		)

		expected := []string{"A", "B", "C", "A"}
		for i, want := range expected {
			id, err := r.Pick(cfg, "", cands, nil, now)
			if err != nil || id != want {
				t.Errorf("rotate pick %d: want %s, got %s (err: %v)", i, want, id, err)
			}
		}
	})

	t.Run("usage", func(t *testing.T) {
		r := NewRouter(nil, nil)
		cfg := Config{Strategy: StrategyUsage}

		cands := []AccountState{
			{ID: "A", UsedPercent: 50, RecentTokens: 1000},
			{ID: "B", UsedPercent: 40, RecentTokens: 2000},
			{ID: "C", UsedPercent: 40, RecentTokens: 500},
		}

		// B 与 C 的 UsedPercent 均为 40，C 的 RecentTokens 更低 (500 < 2000)
		id, err := r.Pick(cfg, "", enabled(cands...), nil, now)
		if err != nil || id != "C" {
			t.Errorf("usage pick: want C, got %s (err: %v)", id, err)
		}
	})

	t.Run("pace", func(t *testing.T) {
		r := NewRouter(nil, nil)
		cfg := Config{Strategy: StrategyPace}

		// pace = (100 - used) / max(hours, 1)
		// A: used 20 (remain 80), reset in 4h -> pace = 20
		// B: used 21 (remain 79), reset in 4h -> pace = 19.75 (与 A 在 10% 以内同档，top=20, 19.75 > 18)
		//    A 的 RecentTokens 1000, B 的 RecentTokens 500 -> 同档内 B 的 tokens 较小优先
		// C: used 50 (remain 50), reset in 5h -> pace = 10 (不同档)
		cands := []AccountState{
			{ID: "A", UsedPercent: 20, ResetAt: now.Add(4 * time.Hour), RecentTokens: 1000, Order: 1},
			{ID: "B", UsedPercent: 21, ResetAt: now.Add(4 * time.Hour), RecentTokens: 500, Order: 2},
			{ID: "C", UsedPercent: 50, ResetAt: now.Add(5 * time.Hour), RecentTokens: 100, Order: 3},
		}

		id, err := r.Pick(cfg, "", enabled(cands...), nil, now)
		if err != nil || id != "B" {
			t.Errorf("pace pick: want B, got %s (err: %v)", id, err)
		}
	})

	t.Run("weight", func(t *testing.T) {
		r := NewRouter(nil, nil)
		cfg := Config{Strategy: StrategyWeight}

		// 权重 3:1
		cands := enabled(
			AccountState{ID: "A", Weight: 3},
			AccountState{ID: "B", Weight: 1},
		)

		// 平滑加权轮询：A(3), B(1) 序列应为 A, A, B, A
		expected := []string{"A", "A", "B", "A"}
		for i, want := range expected {
			id, err := r.Pick(cfg, "", cands, nil, now)
			if err != nil || id != want {
				t.Errorf("weight round %d: want %s, got %s (err: %v)", i, want, id, err)
			}
		}
	})
}

func TestRouter_CoolingAndBackoff(t *testing.T) {
	r := NewRouter(nil, nil)
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

	// 1. 指定 resetAt 时的冷却与恢复
	resetAt := now.Add(5 * time.Minute)
	r.MarkLimited("acc-fixed", resetAt, now)
	if !r.Cooling("acc-fixed", now.Add(1*time.Minute)) {
		t.Errorf("want Cooling true at +1m")
	}
	if r.Cooling("acc-fixed", resetAt.Add(1*time.Second)) {
		t.Errorf("want Cooling false after resetAt")
	}

	// 2. 阶梯翻倍与 30 分钟封顶
	// 初始触发：1m
	r.MarkLimited("acc-step", time.Time{}, now)
	rec := r.cooling["acc-step"]
	if rec.until != now.Add(1*time.Minute) {
		t.Errorf("step 1: want until +1m, got %v", rec.until.Sub(now))
	}

	// 10s 后再次触发：翻倍到 2m
	t2 := now.Add(10 * time.Second)
	r.MarkLimited("acc-step", time.Time{}, t2)
	if rec.until != t2.Add(2*time.Minute) {
		t.Errorf("step 2: want until +2m, got %v", rec.until.Sub(t2))
	}

	// 再次触发：4m
	t3 := t2.Add(10 * time.Second)
	r.MarkLimited("acc-step", time.Time{}, t3)
	if rec.until != t3.Add(4*time.Minute) {
		t.Errorf("step 3: want until +4m, got %v", rec.until.Sub(t3))
	}

	// 连续触发至封顶 30m (8m -> 16m -> 32m 截断为 30m)
	curT := t3
	for i := 0; i < 5; i++ {
		curT = curT.Add(10 * time.Second)
		r.MarkLimited("acc-step", time.Time{}, curT)
	}
	if rec.step != 30*time.Minute {
		t.Errorf("step cap: want 30m, got %v", rec.step)
	}

	// 3. 超过 30 分钟后重置为 1m
	tReset := curT.Add(31 * time.Minute)
	r.MarkLimited("acc-step", time.Time{}, tReset)
	if rec.step != 1*time.Minute || rec.until != tReset.Add(1*time.Minute) {
		t.Errorf("step reset: want 1m after 30m, got %v", rec.step)
	}
}

func TestRouter_Sink(t *testing.T) {
	r := NewRouter(nil, nil)
	now := time.Date(2026, 10, 17, 14, 0, 0, 0, time.UTC)

	// A 的 Order 小于 B，策略优先选 A
	cands := enabled(
		AccountState{ID: "A", Order: 1, UsedPercent: 50},
		AccountState{ID: "B", Order: 2, UsedPercent: 50},
	)

	// A 限流冷却并在 10 分钟后解冻
	r.MarkLimited("A", now.Add(5*time.Minute), now)
	afterCool := now.Add(10 * time.Minute)

	// Sink = false：保持原位，策略依然优先选 A
	cfgNoSink := Config{Strategy: StrategyOrder, Sink: false}
	id, err := r.Pick(cfgNoSink, "", cands, nil, afterCool)
	if err != nil || id != "A" {
		t.Errorf("Sink=false: want A, got %s (err: %v)", id, err)
	}

	// Sink = true：曾冷却过的 A 排在从未冷却过的 B 之后，选 B
	cfgSink := Config{Strategy: StrategyOrder, Sink: true}
	id, err = r.Pick(cfgSink, "", cands, nil, afterCool)
	if err != nil || id != "B" {
		t.Errorf("Sink=true: want B, got %s (err: %v)", id, err)
	}
}

func TestRouter_SessionAffinity(t *testing.T) {
	now := time.Date(2026, 10, 17, 15, 0, 0, 0, time.UTC)
	cfg := Config{Strategy: StrategyOrder, Affinity: AffinitySession}
	accounts := enabled(
		AccountState{ID: "A", Order: 1},
		AccountState{ID: "B", Order: 2},
	)

	r := NewRouter(nil, nil)
	picked, err := r.Pick(cfg, "conv-1", accounts, nil, now)
	if err != nil || picked != "A" {
		t.Fatalf("first pick: want A, got %s (err: %v)", picked, err)
	}

	// 绑定后即使 B 的优先级更高，也继续使用 A
	preferredB := enabled(
		AccountState{ID: "A", Order: 2},
		AccountState{ID: "B", Order: 1},
	)
	picked, err = r.Pick(cfg, "conv-1", preferredB, nil, now)
	if err != nil || picked != "A" {
		t.Errorf("session affinity: want A, got %s (err: %v)", picked, err)
	}

	// A 被排除后重新选号并更新绑定
	picked, err = r.Pick(cfg, "conv-1", preferredB, []string{"A"}, now)
	if err != nil || picked != "B" {
		t.Fatalf("exclude bound account: want B, got %s (err: %v)", picked, err)
	}
	bound, _ := r.Bound("conv-1")
	if bound != "B" {
		t.Errorf("binding not updated after failover: got %s", bound)
	}

	// A 被禁用后同样换号
	picked, err = r.Pick(cfg, "conv-2", enabled(AccountState{ID: "A", Order: 1}), nil, now)
	if err != nil || picked != "A" {
		t.Fatalf("conv-2 first pick: want A, got %s (err: %v)", picked, err)
	}
	disabledA := []AccountState{
		{ID: "A", Order: 1},
		{ID: "B", Order: 2, Enabled: true},
	}
	picked, err = r.Pick(cfg, "conv-2", disabledA, nil, now)
	if err != nil || picked != "B" {
		t.Errorf("disabled bound account: want B, got %s (err: %v)", picked, err)
	}
}

func TestRouter_StrictAffinityKeepsBoundAccount(t *testing.T) {
	now := time.Date(2026, 10, 17, 15, 0, 0, 0, time.UTC)
	cfg := Config{Strategy: StrategyOrder, Affinity: AffinityStrict}

	r := NewRouter(nil, nil)
	r.Bind("conv-strict", "A")
	r.MarkLimited("A", now.Add(10*time.Minute), now)

	// 冷却中且被排除时，strict 仍沿用绑定账号
	picked, err := r.Pick(cfg, "conv-strict", []AccountState{
		{ID: "A"},
		{ID: "B", Enabled: true},
	}, []string{"A"}, now)
	if err != nil || picked != "A" {
		t.Errorf("strict affinity: want A, got %s (err: %v)", picked, err)
	}

	// 绑定账号被删除后回退到正常选号
	picked, err = r.Pick(cfg, "conv-strict", []AccountState{
		{ID: "B", Enabled: true},
	}, nil, now)
	if err != nil || picked != "B" {
		t.Errorf("strict fallback after account removal: want B, got %s (err: %v)", picked, err)
	}
}

func TestRouter_TurnAffinityReroutesWithoutRebinding(t *testing.T) {
	now := time.Date(2026, 10, 17, 15, 0, 0, 0, time.UTC)
	cfg := Config{Strategy: StrategyOrder, Affinity: AffinityTurn}

	r := NewRouter(nil, nil)
	r.Bind("conv-turn", "A")
	picked, err := r.Pick(cfg, "conv-turn", enabled(
		AccountState{ID: "A", Order: 2},
		AccountState{ID: "B", Order: 1},
	), nil, now)
	if err != nil || picked != "B" {
		t.Fatalf("turn affinity: want B, got %s (err: %v)", picked, err)
	}
	bound, _ := r.Bound("conv-turn")
	if bound != "A" {
		t.Errorf("turn affinity must not rewrite binding, got %s", bound)
	}
}

func TestRouter_BindingsAndPersistence(t *testing.T) {
	now := time.Date(2026, 10, 17, 15, 0, 0, 0, time.UTC)
	cfg := Config{Strategy: StrategyOrder, Affinity: AffinitySession}

	saved := make(map[string]string)
	r := NewRouter(map[string]string{"legacy": "A"}, func(b map[string]string) error {
		saved = b
		return nil
	})

	if id, ok := r.Bound("legacy"); !ok || id != "A" {
		t.Errorf("initial binding not loaded: %q %v", id, ok)
	}

	if _, err := r.Pick(cfg, "conv-new", enabled(AccountState{ID: "A"}), nil, now); err != nil {
		t.Fatalf("pick failed: %v", err)
	}
	if saved["conv-new"] != "A" || saved["legacy"] != "A" {
		t.Errorf("binding not persisted: %v", saved)
	}

	// 空会话或空账号的 Bind 被忽略
	r.Bind("", "B")
	r.Bind("conv-empty", "")
	if _, ok := r.Bound(""); ok {
		t.Errorf("empty session key must be ignored")
	}
	if _, ok := r.Bound("conv-empty"); ok {
		t.Errorf("empty account id must be ignored")
	}

	if got := r.BoundSessions("A"); len(got) != 2 {
		t.Errorf("BoundSessions(A) = %v, want 2 entries", got)
	}

	r.ForgetAccount("A")
	if _, ok := r.Bound("legacy"); ok {
		t.Errorf("ForgetAccount did not remove bindings")
	}
	if len(saved) != 0 {
		t.Errorf("ForgetAccount did not persist: %v", saved)
	}
}

func TestRouter_EmptySessionDoesNotBind(t *testing.T) {
	now := time.Now()
	r := NewRouter(nil, func(map[string]string) error {
		t.Fatalf("persist must not be called for empty session key")
		return nil
	})

	if _, err := r.Pick(DefaultConfig(), "", enabled(AccountState{ID: "A"}), nil, now); err != nil {
		t.Fatalf("pick failed: %v", err)
	}
}

func TestRouter_ConcurrentPick(t *testing.T) {
	r := NewRouter(nil, nil)
	now := time.Now()
	cands := enabled(
		AccountState{ID: "A", Weight: 2, Order: 1},
		AccountState{ID: "B", Weight: 1, Order: 2},
	)
	cfg := Config{Strategy: StrategyWeight}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = r.Pick(cfg, "", cands, nil, now)
			}
		}()
	}
	wg.Wait()
}
