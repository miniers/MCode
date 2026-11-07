package steward

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"mcode/internal/attention"
	"mcode/internal/config"
	"mcode/internal/kanban"
	"mcode/internal/store"
)

// ledgerRig 构造一个只接账本所需依赖的测试运行时。
func ledgerRig(t *testing.T) *stewardRig {
	t.Helper()
	return newStewardRig(t, enabledConfig())
}

// ledgerEntriesOf 取出某个分区里的条目，保持账本顺序。
func ledgerEntriesOf(entries []LedgerEntry, section string) []LedgerEntry {
	var out []LedgerEntry
	for _, entry := range entries {
		if entry.Section == section {
			out = append(out, entry)
		}
	}
	return out
}

// TestLedgerAddUpdateRemove 验证账本的增、改、删：新增返回条目并落库，改写只动指定字段，
// 删除后条目消失，非法的分区与超长正文被拒绝。
func TestLedgerAddUpdateRemove(t *testing.T) {
	rig := ledgerRig(t)
	ctx := context.Background()
	rt := rig.rt

	added, err := rt.AddLedgerEntry(ctx, LedgerInProgress, "  把登录流程  理顺 \n 补上错误提示 ", "")
	if err != nil {
		t.Fatalf("新增账本条目失败: %v", err)
	}
	if added.ID == "" {
		t.Fatal("新增的条目应当有 ID")
	}
	if added.Text != "把登录流程 理顺 补上错误提示" {
		t.Fatalf("正文应当压成一行并去多余空白，得到 %q", added.Text)
	}

	entries, err := rt.Ledger(ctx)
	if err != nil {
		t.Fatalf("读取账本失败: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != added.ID {
		t.Fatalf("账本里应当只有刚新增的条目，实际 %+v", entries)
	}

	rig.clk.Advance(time.Minute)
	updated, err := rt.UpdateLedgerEntry(ctx, added.ID, LedgerPendingUser, "登录流程要不要一并换掉鉴权库？")
	if err != nil {
		t.Fatalf("改写账本条目失败: %v", err)
	}
	if updated.Section != LedgerPendingUser || !strings.Contains(updated.Text, "鉴权库") {
		t.Fatalf("改写结果不对: %+v", updated)
	}
	if !updated.UpdatedAt.After(updated.CreatedAt) {
		t.Fatalf("改写后 UpdatedAt 应当晚于 CreatedAt，实际 %v / %v", updated.UpdatedAt, updated.CreatedAt)
	}

	if _, err := rt.UpdateLedgerEntry(ctx, "l-不存在", "", "随便改写"); err == nil {
		t.Fatal("改写不存在的条目应当报错")
	}
	if _, err := rt.UpdateLedgerEntry(ctx, added.ID, "", ""); err == nil {
		t.Fatal("既不换分区也不改正文时应当报错")
	}
	if err := rt.RemoveLedgerEntry(ctx, added.ID); err != nil {
		t.Fatalf("删除账本条目失败: %v", err)
	}
	if entries, _ := rt.Ledger(ctx); len(entries) != 0 {
		t.Fatalf("删除后账本应为空，实际 %+v", entries)
	}
	if err := rt.RemoveLedgerEntry(ctx, added.ID); err == nil {
		t.Fatal("重复删除应当报错")
	}

	if _, err := rt.AddLedgerEntry(ctx, "todo", "分区不合法", ""); err == nil {
		t.Fatal("非法分区应当报错")
	}
	if _, err := rt.AddLedgerEntry(ctx, LedgerInProgress, "   ", ""); err == nil {
		t.Fatal("空正文应当报错")
	}
	tooLong := strings.Repeat("长", LedgerTextMaxRunes+1)
	if _, err := rt.AddLedgerEntry(ctx, LedgerInProgress, tooLong, ""); err == nil {
		t.Fatal("超过长度上限的正文应当报错")
	}
	if _, err := rt.AddLedgerEntry(ctx, LedgerInProgress, strings.Repeat("长", LedgerTextMaxRunes), ""); err != nil {
		t.Fatalf("正好等于上限的正文应当接受: %v", err)
	}
}

// TestLedgerSectionLimits 验证各分区上限：decision 满了淘汰最旧的一条，
// in_progress 与 pending_user 满了拒绝新增并给出中文提示。
func TestLedgerSectionLimits(t *testing.T) {
	rig := ledgerRig(t)
	ctx := context.Background()
	rt := rig.rt

	first, err := rt.AddLedgerEntry(ctx, LedgerDecision, "第一条决策", "")
	if err != nil {
		t.Fatalf("新增决策失败: %v", err)
	}
	rig.clk.Advance(time.Second)
	for i := 1; i < ledgerSectionLimits[LedgerDecision]; i++ {
		if _, err := rt.AddLedgerEntry(ctx, LedgerDecision, "后续决策", ""); err != nil {
			t.Fatalf("新增第 %d 条决策失败: %v", i+1, err)
		}
		rig.clk.Advance(time.Second)
	}
	if _, err := rt.AddLedgerEntry(ctx, LedgerDecision, "第五十一条决策", ""); err != nil {
		t.Fatalf("decision 超出上限时应当淘汰最旧的一条: %v", err)
	}
	entries, _ := rt.Ledger(ctx)
	decisions := ledgerEntriesOf(entries, LedgerDecision)
	if len(decisions) != ledgerSectionLimits[LedgerDecision] {
		t.Fatalf("decision 应保持 %d 条，实际 %d 条", ledgerSectionLimits[LedgerDecision], len(decisions))
	}
	for _, entry := range decisions {
		if entry.ID == first.ID {
			t.Fatal("最旧的一条决策应被淘汰")
		}
	}

	for _, section := range []string{LedgerInProgress, LedgerPendingUser} {
		for i := 0; i < ledgerSectionLimits[section]; i++ {
			if _, err := rt.AddLedgerEntry(ctx, section, "占位", ""); err != nil {
				t.Fatalf("%s 第 %d 条应当成功: %v", section, i+1, err)
			}
		}
		if _, err := rt.AddLedgerEntry(ctx, section, "再来一条", ""); err == nil {
			t.Fatalf("%s 超出上限时应当拒绝新增", section)
		} else if !strings.Contains(err.Error(), "已满") {
			t.Fatalf("%s 的报错应当说明已满，实际 %v", section, err)
		}
	}
}

// TestRenderLedger 验证渲染：三个小标题、每条一行、话题写进括号；
// 超过总长预算时从最旧的条目截起，并注明省略了几条。
func TestRenderLedger(t *testing.T) {
	rig := ledgerRig(t)
	ctx := context.Background()
	rt := rig.rt

	if _, err := rt.AddLedgerEntry(ctx, LedgerInProgress, "写登录流程任务书", "topic-1"); err != nil {
		t.Fatalf("新增失败: %v", err)
	}
	rendered, err := rt.RenderLedger(ctx)
	if err != nil {
		t.Fatalf("渲染账本失败: %v", err)
	}
	for _, want := range []string{"### 进行中", "### 已定决策", "### 等用户确认",
		"写登录流程任务书", "（话题 topic-1）", "（无）"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("渲染结果缺少 %q:\n%s", want, rendered)
		}
	}

	// 塞满进行中分区，用长正文把总长顶到预算以上；决策最后写，作最新的条目应当保留。
	for i := 0; i < ledgerSectionLimits[LedgerInProgress]-1; i++ {
		if _, err := rt.AddLedgerEntry(ctx, LedgerInProgress, strings.Repeat("长", LedgerTextMaxRunes), ""); err != nil {
			t.Fatalf("新增长条目失败: %v", err)
		}
		rig.clk.Advance(time.Second)
	}
	if _, err := rt.AddLedgerEntry(ctx, LedgerDecision, "统一用 pnpm", ""); err != nil {
		t.Fatalf("新增决策失败: %v", err)
	}
	rendered, err = rt.RenderLedger(ctx)
	if err != nil {
		t.Fatalf("渲染账本失败: %v", err)
	}
	if n := utf8.RuneCountInString(rendered); n > ledgerRenderMaxRunes {
		t.Fatalf("渲染长度 %d 超过预算 %d", n, ledgerRenderMaxRunes)
	}
	if !strings.Contains(rendered, "省略了") {
		t.Fatal("超出预算时应当注明省略了几条")
	}
	// 最旧的那条（写登录流程任务书）应当被先截掉，最新的决策保留。
	if strings.Contains(rendered, "写登录流程任务书") {
		t.Fatal("应当从最旧的条目开始截断")
	}
	if !strings.Contains(rendered, "统一用 pnpm") {
		t.Fatal("较新的条目应当保留")
	}
}

// TestReceiptTopicWritesDecisionOnce 验证话题回执：写一条 decision（标题 + 目标总结）、
// 删掉该话题的 in_progress 条目、往唤醒批次里排一行通知，同一个话题只回执一次。
func TestReceiptTopicWritesDecisionOnce(t *testing.T) {
	rig, board := newTopicRig(t)
	ctx := context.Background()
	rt := rig.rt

	seedTopic(t, rig, Topic{
		ThreadID:  "topic-1",
		ProjectID: "proj-1",
		Title:     "整理登录流程",
		GoalID:    "goal-1",
		CreatedAt: rig.clk.Now(),
		UpdatedAt: rig.clk.Now(),
	})
	board.goals = []store.Goal{{
		ID:            "goal-1",
		Status:        string(kanban.GoalStatusCompleted),
		ResultSummary: stringPtr("登录流程已理顺，错误提示补全"),
	}}
	if _, err := rt.AddLedgerEntry(ctx, LedgerInProgress, "等登录流程收尾", "topic-1"); err != nil {
		t.Fatalf("预置在办条目失败: %v", err)
	}
	if _, err := rt.AddLedgerEntry(ctx, LedgerInProgress, "另一件事", "topic-2"); err != nil {
		t.Fatalf("预置在办条目失败: %v", err)
	}

	ok, err := rt.ReceiptTopic(ctx, "topic-1")
	if err != nil || !ok {
		t.Fatalf("回执失败: ok=%v err=%v", ok, err)
	}
	entries, _ := rt.Ledger(ctx)
	decisions := ledgerEntriesOf(entries, LedgerDecision)
	if len(decisions) != 1 {
		t.Fatalf("应当写出一条决策，实际 %+v", decisions)
	}
	if !strings.Contains(decisions[0].Text, "整理登录流程") || !strings.Contains(decisions[0].Text, "登录流程已理顺") {
		t.Fatalf("决策正文应含话题标题与目标总结，实际 %q", decisions[0].Text)
	}
	if decisions[0].TopicThreadID != "topic-1" {
		t.Fatalf("决策应记下话题会话，实际 %q", decisions[0].TopicThreadID)
	}
	inProgress := ledgerEntriesOf(entries, LedgerInProgress)
	if len(inProgress) != 1 || inProgress[0].TopicThreadID != "topic-2" {
		t.Fatalf("该话题的在办条目应被删掉、别的保留，实际 %+v", inProgress)
	}

	topic, found, err := rt.Topic(ctx, "topic-1")
	if err != nil || !found {
		t.Fatalf("读取话题失败: found=%v err=%v", found, err)
	}
	if topic.ReceiptAt == nil {
		t.Fatal("回执后话题应当带上回执时刻")
	}

	if ok, err := rt.ReceiptTopic(ctx, "topic-1"); err != nil || ok {
		t.Fatalf("同一话题不应重复回执: ok=%v err=%v", ok, err)
	}
	if decisions, _ := rt.Ledger(ctx); len(ledgerEntriesOf(decisions, LedgerDecision)) != 1 {
		t.Fatal("重复回执不应再写一条决策")
	}

	select {
	case note := <-rt.notes:
		if !strings.Contains(note, "整理登录流程") {
			t.Fatalf("排进唤醒批次的通知应含话题标题，实际 %q", note)
		}
	default:
		t.Fatal("回执应往唤醒批次里排一行通知")
	}

	if ok, err := rt.ReceiptTopic(ctx, "不是话题"); err != nil || ok {
		t.Fatalf("非话题会话不应回执: ok=%v err=%v", ok, err)
	}
}

// TestReceiptTopicWithoutSummaryUsesPhase 验证没有目标总结时用最终阶段收尾，
// 并且 CloseTopic 结项也会触发回执。
func TestReceiptTopicWithoutSummaryUsesPhase(t *testing.T) {
	rig, board := newTopicRig(t)
	ctx := context.Background()

	seedTopic(t, rig, Topic{
		ThreadID:  "topic-1",
		ProjectID: "proj-1",
		Title:     "换掉旧鉴权库",
		GoalID:    "goal-1",
		CreatedAt: rig.clk.Now(),
		UpdatedAt: rig.clk.Now(),
	})
	board.goals = []store.Goal{{
		ID:     "goal-1",
		Status: string(kanban.GoalStatusCancelled),
	}}

	if err := rig.rt.CloseTopic(ctx, "topic-1"); err != nil {
		t.Fatalf("结项失败: %v", err)
	}
	entries, _ := rig.rt.Ledger(ctx)
	decisions := ledgerEntriesOf(entries, LedgerDecision)
	if len(decisions) != 1 {
		t.Fatalf("结项应回执一条决策，实际 %+v", decisions)
	}
	if !strings.Contains(decisions[0].Text, "换掉旧鉴权库") || !strings.Contains(decisions[0].Text, "最终阶段：已取消") {
		t.Fatalf("决策正文应含标题与最终阶段，实际 %q", decisions[0].Text)
	}
}

// TestShouldRotate 验证换新判定：只对总管主会话生效，空闲达到显式配置的阈值或上下文达到阈值都换新，
// idle 为 0 表示未知、只看上下文。
func TestShouldRotate(t *testing.T) {
	rig := ledgerRig(t)
	rig.settings.set(settingThreadID, "steward-1")
	// 空闲换新默认关闭，这里显式配置一小时阈值，验证达到阈值时的换新行为。
	cfg := enabledConfig()
	cfg.RotateIdleMinutes = 60
	rig.cfg.set(cfg)
	rt := rig.rt

	cases := []struct {
		name    string
		thread  string
		idle    time.Duration
		tokens  int64
		want    bool
		reasons string
	}{
		{"非主会话一律不换新", "lead-1", 10 * time.Hour, DefaultRotateContextTokens * 2, false, "只看主会话"},
		{"空会话 ID 不换新", "", time.Hour, DefaultRotateContextTokens, false, "没有会话 ID"},
		{"都很小时不换新", "steward-1", time.Minute, 1000, false, "两个阈值都没到"},
		{"空闲刚到阈值就换新", "steward-1", time.Hour, 0, true, "空闲达到阈值"},
		{"空闲超过阈值也换新", "steward-1", 2 * time.Hour, 0, true, "空闲超过阈值"},
		{"上下文刚到阈值就换新", "steward-1", time.Minute, DefaultRotateContextTokens, true, "上下文达到阈值"},
		{"idle 未知时只看上下文：未到不换", "steward-1", 0, DefaultRotateContextTokens - 1, false, "idle 为 0"},
		{"idle 未知时只看上下文：到了换", "steward-1", 0, DefaultRotateContextTokens, true, "idle 为 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rt.ShouldRotate(tc.thread, tc.idle, tc.tokens); got != tc.want {
				t.Fatalf("%s：ShouldRotate(%q, %v, %d) = %v，期望 %v（%s）",
					tc.name, tc.thread, tc.idle, tc.tokens, got, tc.want, tc.reasons)
			}
		})
	}
}

// TestShouldRotateCustomThresholds 验证阈值可配置。
func TestShouldRotateCustomThresholds(t *testing.T) {
	settings := newFakeSettings()
	settings.set(settingThreadID, "steward-1")
	rt := New(Deps{
		Events:              newFakeEvents(),
		Sessions:            newFakeSessions(),
		Settings:            settings,
		Config:              (&configHolder{cfg: enabledConfig()}).get,
		RotateIdle:          10 * time.Minute,
		RotateContextTokens: 500,
	})
	t.Cleanup(rt.Stop)

	if !rt.ShouldRotate("steward-1", 10*time.Minute, 0) {
		t.Fatal("自定义空闲阈值应当生效")
	}
	if rt.ShouldRotate("steward-1", 9*time.Minute, 0) {
		t.Fatal("未到自定义空闲阈值时不应换新")
	}
	if !rt.ShouldRotate("steward-1", 0, 500) {
		t.Fatal("自定义上下文阈值应当生效")
	}
	if rt.ShouldRotate("steward-1", 0, 499) {
		t.Fatal("低于自定义上下文阈值时不应换新")
	}
}

// TestShouldRotateConfigThresholds 验证配置里的阈值优先级最高：正数字段盖过 Deps，
// 配置为 0 时回落到 Deps，Deps 也没给时回到默认值。
func TestShouldRotateConfigThresholds(t *testing.T) {
	newRT := func(cfg config.StewardConfig, idle time.Duration, tokens int64) *Runtime {
		settings := newFakeSettings()
		settings.set(settingThreadID, "steward-1")
		holder := &configHolder{cfg: cfg}
		rt := New(Deps{
			Events:              newFakeEvents(),
			Sessions:            newFakeSessions(),
			Settings:            settings,
			Config:              holder.get,
			RotateIdle:          idle,
			RotateContextTokens: tokens,
		})
		t.Cleanup(rt.Stop)
		return rt
	}

	// 配置的正数阈值盖过 Deps 的 10 分钟 / 500 token。
	configured := newRT(config.StewardConfig{RotateIdleMinutes: 120, RotateContextTokens: 5000}, 10*time.Minute, 500)
	if configured.ShouldRotate("steward-1", 10*time.Minute, 0) {
		t.Fatal("配置的空闲阈值应当盖过 Deps：10 分钟不应换新")
	}
	if !configured.ShouldRotate("steward-1", 2*time.Hour, 0) {
		t.Fatal("配置的空闲阈值应当盖过 Deps：2 小时应换新")
	}
	if configured.ShouldRotate("steward-1", 0, 500) {
		t.Fatal("配置的上下文阈值应当盖过 Deps：500 不应换新")
	}
	if !configured.ShouldRotate("steward-1", 0, 5000) {
		t.Fatal("配置的上下文阈值应当盖过 Deps：5000 应换新")
	}

	// 配置为 0（或负数）时回落到 Deps 的 10 分钟 / 500 token。
	fallback := newRT(config.StewardConfig{RotateIdleMinutes: 0, RotateContextTokens: -1}, 10*time.Minute, 500)
	if !fallback.ShouldRotate("steward-1", 10*time.Minute, 0) {
		t.Fatal("配置为 0 时应回落到 Deps 的空闲阈值")
	}
	if fallback.ShouldRotate("steward-1", 9*time.Minute, 0) {
		t.Fatal("未到 Deps 的空闲阈值时不应换新")
	}
	if !fallback.ShouldRotate("steward-1", 0, 500) {
		t.Fatal("配置为负数时应回落到 Deps 的上下文阈值")
	}

	// 配置与 Deps 都没给时空闲换新默认关闭，上下文阈值仍回落到默认值。
	defaulted := newRT(config.StewardConfig{}, 0, 0)
	if defaulted.ShouldRotate("steward-1", 24*time.Hour, 0) {
		t.Fatal("都没配置时空闲换新应默认关闭")
	}
	if !defaulted.ShouldRotate("steward-1", 0, DefaultRotateContextTokens) {
		t.Fatal("都没配置时应回落到默认上下文阈值")
	}
	if defaulted.ShouldRotate("steward-1", time.Minute, 1000) {
		t.Fatal("两个默认阈值都没到时不应当换新")
	}
}

// TestShouldRotateIdleOffByDefault 验证空闲换新默认关闭：不配置时空闲多久都不换新，
// 上下文达到默认阈值仍然换新；显式配置正数分钟数后空闲换新恢复生效。
func TestShouldRotateIdleOffByDefault(t *testing.T) {
	newRT := func(cfg config.StewardConfig) *Runtime {
		settings := newFakeSettings()
		settings.set(settingThreadID, "steward-1")
		holder := &configHolder{cfg: cfg}
		rt := New(Deps{
			Events:   newFakeEvents(),
			Sessions: newFakeSessions(),
			Settings: settings,
			Config:   holder.get,
		})
		t.Cleanup(rt.Stop)
		return rt
	}

	off := newRT(enabledConfig())
	if off.ShouldRotate("steward-1", 24*time.Hour, 0) {
		t.Fatal("未配置空闲阈值时空闲 24 小时不应换新")
	}
	if !off.ShouldRotate("steward-1", 24*time.Hour, DefaultRotateContextTokens) {
		t.Fatal("未配置空闲阈值时上下文达到默认阈值仍应换新")
	}

	cfg := enabledConfig()
	cfg.RotateIdleMinutes = 30
	on := newRT(cfg)
	if !on.ShouldRotate("steward-1", 30*time.Minute, 0) {
		t.Fatal("配置正数空闲阈值后空闲达到阈值应换新")
	}
	if on.ShouldRotate("steward-1", 29*time.Minute, 0) {
		t.Fatal("未到配置的空闲阈值时不应换新")
	}
}

// TestRotationPreamble 验证换新前言：带换新说明、账本内容与未结项话题索引（标题、阶段、
// 进度、限长 brief），已结项的话题不列，也不重复注入身份段。
func TestRotationPreamble(t *testing.T) {
	rig, board := newTopicRig(t)
	ctx := context.Background()
	rt := rig.rt

	if _, err := rt.AddLedgerEntry(ctx, LedgerInProgress, "写登录流程任务书", "topic-1"); err != nil {
		t.Fatalf("新增账本条目失败: %v", err)
	}
	longBrief := strings.Repeat("登录流程的细节。", 20)
	seedTopicList(t, rig, []Topic{{
		ThreadID:  "topic-1",
		ProjectID: "proj-1",
		Title:     "整理登录流程",
		Brief:     longBrief,
		GoalID:    "goal-1",
		CreatedAt: rig.clk.Now(),
		UpdatedAt: rig.clk.Now(),
	}, {
		ThreadID:  "topic-2",
		ProjectID: "proj-1",
		Title:     "已经收尾的事",
		Closed:    true,
		CreatedAt: rig.clk.Now(),
		UpdatedAt: rig.clk.Now(),
	}})
	board.goals = []store.Goal{{
		ID:     "goal-1",
		Status: string(kanban.GoalStatusRunning),
	}}

	preamble := rt.RotationPreamble(ctx)
	for _, want := range []string{"换新后的主会话", "## 账本", "### 进行中", "写登录流程任务书",
		"## 话题索引", "整理登录流程", "阶段：执行中", "0/0 已完成", "brief："} {
		if !strings.Contains(preamble, want) {
			t.Fatalf("换新前言缺少 %q:\n%s", want, preamble)
		}
	}
	if strings.Contains(preamble, "已经收尾的事") {
		t.Fatal("已结项的话题不应出现在话题索引里")
	}
	if strings.Contains(preamble, longBrief) {
		t.Fatal("brief 应当被截到限长以内")
	}
	if strings.Contains(preamble, "你是「") {
		t.Fatal("换新前言不应重复注入身份段")
	}
}

// TestRenderLedgerEntryTimes 验证账本每条渲染带上记录时间，零值时不出现无意义的 0001-01-01；
// 改动过的条目额外标出更新时间。
func TestRenderLedgerEntryTimes(t *testing.T) {
	created := time.Date(2026, 5, 6, 14, 30, 0, 0, time.Local)
	updated := time.Date(2026, 5, 7, 9, 5, 0, 0, time.Local)
	entries := []LedgerEntry{
		{ID: "e1", Section: LedgerInProgress, Text: "写登录流程任务书", CreatedAt: created, UpdatedAt: created},
		{ID: "e2", Section: LedgerDecision, Text: "统一用 pnpm", CreatedAt: created, UpdatedAt: updated},
		{ID: "e3", Section: LedgerPendingUser, Text: "老数据条目"},
	}
	rendered := renderLedgerSections(entries, nil)
	if !strings.Contains(rendered, "记于 2026-05-06 14:30") {
		t.Fatalf("渲染结果应含记录时间:\n%s", rendered)
	}
	if !strings.Contains(rendered, "记于 2026-05-06 14:30，更新于 2026-05-07 09:05") {
		t.Fatalf("改动过的条目应同时标出更新时间:\n%s", rendered)
	}
	if strings.Count(rendered, "更新于") != 1 {
		t.Fatalf("只有改动过的条目才带更新时间:\n%s", rendered)
	}
	if strings.Contains(rendered, "0001-") {
		t.Fatalf("零值时间不应渲染成年份 1:\n%s", rendered)
	}
	if !strings.Contains(rendered, "- [e3] 老数据条目") {
		t.Fatalf("零值时间条目应不带时间后缀:\n%s", rendered)
	}
}

// TestRotationPreambleEmptyState 验证没有账本条目与话题时前言仍可用。
func TestRotationPreambleEmptyState(t *testing.T) {
	rig := ledgerRig(t)
	preamble := rig.rt.RotationPreamble(context.Background())
	for _, want := range []string{"## 账本", "### 进行中", "## 话题索引", "没有未结项的话题"} {
		if !strings.Contains(preamble, want) {
			t.Fatalf("空状态前言缺少 %q:\n%s", want, preamble)
		}
	}
}

// seedTopicList 把一组话题写进设置，等价于连续写入多条话题。
func seedTopicList(t *testing.T, rig *stewardRig, topics []Topic) {
	t.Helper()
	payload, err := json.Marshal(topics)
	if err != nil {
		t.Fatalf("序列化话题失败: %v", err)
	}
	rig.settings.set(settingTopics, string(payload))
}

// stringPtr 返回字符串指针，用于构造带总结的目标。
func stringPtr(text string) *string { return &text }

// TestLedgerSubjectPersistsAndOldDataLoads 验证关联对象随条目落库并按字段读回；
// 没有 subjectType/subjectId 的旧 JSON 也能正常加载。
func TestLedgerSubjectPersistsAndOldDataLoads(t *testing.T) {
	rig := ledgerRig(t)
	ctx := context.Background()
	rt := rig.rt

	if _, err := rt.AddLedgerEntry(ctx, LedgerPendingUser, "批一下登录流程的改动", "",
		LedgerSubject{Type: string(attention.SubjectKanbanTask), ID: "t1"}); err != nil {
		t.Fatalf("新增带对象的账本条目失败: %v", err)
	}
	entries, err := rt.Ledger(ctx)
	if err != nil {
		t.Fatalf("读取账本失败: %v", err)
	}
	if len(entries) != 1 || entries[0].SubjectType != string(attention.SubjectKanbanTask) || entries[0].SubjectID != "t1" {
		t.Fatalf("关联对象应随条目保存，实际 %+v", entries)
	}

	// 旧数据没有这两个字段，读出来应为空值且不报错。
	rig.settings.set(settingLedger, `[{"id":"old-1","section":"pending_user","text":"旧条目",`+
		`"createdAt":"2025-03-04T09:00:00Z","updatedAt":"2025-03-04T09:00:00Z"}]`)
	entries, err = rt.Ledger(ctx)
	if err != nil {
		t.Fatalf("加载旧账本失败: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != "old-1" {
		t.Fatalf("旧账本应正常加载，实际 %+v", entries)
	}
	if entries[0].SubjectType != "" || entries[0].SubjectID != "" {
		t.Fatalf("旧数据没有关联对象时应为空值，实际 %+v", entries[0])
	}
}

// TestResolvedEventClosesPendingUserEntry 验证消解通知会关闭关联对象的 pending_user 条目，
// 并给总管投一条「用户已处理」；不相关对象的条目不受影响。
func TestResolvedEventClosesPendingUserEntry(t *testing.T) {
	rig := ledgerRig(t)
	ctx := context.Background()
	rt := rig.rt
	if _, err := rt.AddLedgerEntry(ctx, LedgerPendingUser, "批一下 t1 的改动", "",
		LedgerSubject{Type: string(attention.SubjectKanbanTask), ID: "t1"}); err != nil {
		t.Fatalf("新增 t1 的账本条目失败: %v", err)
	}
	if _, err := rt.AddLedgerEntry(ctx, LedgerPendingUser, "另一件事等确认", "",
		LedgerSubject{Type: string(attention.SubjectKanbanTask), ID: "t2"}); err != nil {
		t.Fatalf("新增 t2 的账本条目失败: %v", err)
	}
	rig.startSteward()

	at := time.Date(2025, 3, 4, 9, 30, 0, 0, time.Local)
	rig.events.emit(attention.Event{
		ID:          "ev-t1",
		Kind:        string(attention.KindKanbanReview),
		SubjectType: string(attention.SubjectKanbanTask),
		SubjectID:   "t1",
		ResolvedAt:  &at,
		ResolvedBy:  attention.ResolvedByUser,
	})
	rig.advanceUntil("消解通知关闭 t1 的条目", 10*time.Millisecond, 2*time.Second, func() bool {
		entries, err := rt.Ledger(ctx)
		return err == nil && len(entries) == 1
	})

	entries, err := rt.Ledger(ctx)
	if err != nil {
		t.Fatalf("读取账本失败: %v", err)
	}
	if len(entries) != 1 || entries[0].SubjectID != "t2" {
		t.Fatalf("应只关闭 t1 的条目，实际 %+v", entries)
	}

	// 通知只随下一次唤醒送达，不单独触发唤醒。
	rig.events.emit(attention.Event{ID: "ev-next", Kind: string(attention.KindThreadFailed),
		SubjectType: string(attention.SubjectThread), SubjectID: "thread-9", ThreadID: "thread-9"})
	rig.advanceUntil("下一次唤醒带上已处理通知", time.Second, 5*time.Minute, func() bool {
		return len(rig.sessions.messages()) == 1
	})
	if text := rig.lastText(); !strings.Contains(text, "用户已处理：批一下 t1 的改动") {
		t.Fatalf("唤醒消息应带上「用户已处理」，实际为：\n%s", text)
	}
}

// TestStewardResolvedEventClosesWithoutNote 验证总管自己消解的事件同样关闭条目，但不再给自己投通知。
func TestStewardResolvedEventClosesWithoutNote(t *testing.T) {
	rig := ledgerRig(t)
	ctx := context.Background()
	rt := rig.rt
	if _, err := rt.AddLedgerEntry(ctx, LedgerPendingUser, "等用户看一眼 t3", "",
		LedgerSubject{Type: string(attention.SubjectKanbanTask), ID: "t3"}); err != nil {
		t.Fatalf("新增 t3 的账本条目失败: %v", err)
	}
	rig.startSteward()

	at := time.Date(2025, 3, 4, 9, 30, 0, 0, time.Local)
	rig.events.emit(attention.Event{
		ID:          "ev-t3",
		Kind:        string(attention.KindKanbanFailed),
		SubjectType: string(attention.SubjectKanbanTask),
		SubjectID:   "t3",
		ResolvedAt:  &at,
		ResolvedBy:  attention.ResolvedBySteward,
	})
	rig.advanceUntil("总管消解后关闭 t3 的条目", 10*time.Millisecond, 2*time.Second, func() bool {
		entries, err := rt.Ledger(ctx)
		return err == nil && len(entries) == 0
	})

	// 后续唤醒里不应出现「用户已处理」：总管自己知道这次消解。
	rig.events.emit(attention.Event{ID: "ev-next", Kind: string(attention.KindThreadFailed),
		SubjectType: string(attention.SubjectThread), SubjectID: "thread-8", ThreadID: "thread-8"})
	rig.advanceUntil("下一次唤醒", time.Second, 5*time.Minute, func() bool {
		return len(rig.sessions.messages()) == 1
	})
	if text := rig.lastText(); strings.Contains(text, "用户已处理") {
		t.Fatalf("总管自己消解时不应投通知，实际为：\n%s", text)
	}
}
