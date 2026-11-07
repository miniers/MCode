package steward

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"mcode/internal/attention"
)

// TestRotationPreambleListsUnresolvedEvents 验证换新前言列出未消解事件的 ID、类型与标题，
// 已消解的事件不再出现。
func TestRotationPreambleListsUnresolvedEvents(t *testing.T) {
	rig := ledgerRig(t)
	ctx := context.Background()

	open := ev("ev-open", "thread-9", "proj-1")
	open.Title = "未消解标题"
	open.Detail = "未消解详情"
	rig.events.emit(open)

	done := ev("ev-done", "thread-8", "proj-1")
	done.Title = "已消解标题"
	resolvedAt := rig.clk.Now()
	done.ResolvedAt = &resolvedAt
	rig.events.emit(done)

	preamble := rig.rt.RotationPreamble(ctx)
	for _, want := range []string{"## 未消解事件", "ev-open", kindLabel(string(attention.KindThreadFailed)), "未消解标题"} {
		if !strings.Contains(preamble, want) {
			t.Fatalf("换新前言缺少 %q:\n%s", want, preamble)
		}
	}
	if strings.Contains(preamble, "已消解标题") {
		t.Fatalf("已消解的事件不应出现在换新前言里:\n%s", preamble)
	}
}

// TestRotationPreambleWithoutEvents 验证没有未消解事件时该节写明「没有未消解事件」。
func TestRotationPreambleWithoutEvents(t *testing.T) {
	rig := ledgerRig(t)
	preamble := rig.rt.RotationPreamble(context.Background())
	if !strings.Contains(preamble, "（没有未消解事件）") {
		t.Fatalf("没有事件时前言应写明没有未消解事件:\n%s", preamble)
	}
}

// TestRotationPreambleListsRecentBees 验证换新前言列出最近派出的子代理的名字、任务、状态与增删行数。
func TestRotationPreambleListsRecentBees(t *testing.T) {
	rig := ledgerRig(t)
	rig.rt.recentBees = func(string) []RotationBee {
		return []RotationBee{{Name: "bee-a", Task: "改登录流程", Status: "已完成", LinesAdded: 12, LinesDeleted: 3}}
	}
	preamble := rig.rt.RotationPreamble(context.Background())
	for _, want := range []string{"## 最近派出的子代理", "bee-a", "改登录流程", "已完成", "+12/-3"} {
		if !strings.Contains(preamble, want) {
			t.Fatalf("换新前言缺少 %q:\n%s", want, preamble)
		}
	}
}

// TestRotationPreambleOmitsBeesWhenUnwired 验证 recentBees 未接线时整节不出现。
func TestRotationPreambleOmitsBeesWhenUnwired(t *testing.T) {
	rig := ledgerRig(t)
	preamble := rig.rt.RotationPreamble(context.Background())
	if strings.Contains(preamble, "## 最近派出的子代理") {
		t.Fatalf("recentBees 未接线时不应出现子代理小节:\n%s", preamble)
	}
}

// TestRotationPreambleHandoffCounts 验证交接摘要行的四个数字与账本、话题、事件、子代理的实际条数一致。
func TestRotationPreambleHandoffCounts(t *testing.T) {
	rig := ledgerRig(t)
	ctx := context.Background()

	if _, err := rig.rt.AddLedgerEntry(ctx, LedgerInProgress, "写登录流程任务书", ""); err != nil {
		t.Fatalf("新增账本条目失败: %v", err)
	}
	if _, err := rig.rt.AddLedgerEntry(ctx, LedgerDecision, "鉴权库不换", ""); err != nil {
		t.Fatalf("新增账本条目失败: %v", err)
	}
	seedTopicList(t, rig, []Topic{{ThreadID: "topic-1", ProjectID: "proj-1", Title: "整理登录流程"}})
	rig.events.emit(ev("ev-1", "thread-9", "proj-1"))
	rig.rt.recentBees = func(string) []RotationBee {
		return []RotationBee{{Name: "bee-a"}, {Name: "bee-b"}}
	}

	preamble := rig.rt.RotationPreamble(ctx)
	want := "- 账本 2 条（进行中 1、已定决策 1、等用户确认 0）、未结项话题 1 个、未消解事件 1 条、最近子代理 2 只"
	if !strings.Contains(preamble, want) {
		t.Fatalf("交接摘要行不正确，期望包含 %q:\n%s", want, preamble)
	}
}

// TestRotationPreambleHandoffEventError 验证事件读取失败只影响该节，其余小节照常输出。
func TestRotationPreambleHandoffEventError(t *testing.T) {
	rig := ledgerRig(t)
	rig.events.listErr = errors.New("库挂了")

	preamble := rig.rt.RotationPreamble(context.Background())
	if !strings.Contains(preamble, "未消解事件读取失败") {
		t.Fatalf("事件读取失败时应在对应小节写明:\n%s", preamble)
	}
	for _, want := range []string{"## 交接摘要", "## 账本", "## 话题索引"} {
		if !strings.Contains(preamble, want) {
			t.Fatalf("事件读取失败不应影响 %q:\n%s", want, preamble)
		}
	}
}

// TestRotationPreambleCountsAllUnresolvedEvents 验证摘要行报告未消解事件总数，
// 而「未消解事件」小节仍按上限只列前几条。
func TestRotationPreambleCountsAllUnresolvedEvents(t *testing.T) {
	rig := ledgerRig(t)
	for i := 0; i < rotationEventsMax+2; i++ {
		rig.events.emit(ev(fmt.Sprintf("ev-%d", i), "thread-9", "proj-1"))
	}

	preamble := rig.rt.RotationPreamble(context.Background())
	want := fmt.Sprintf("未消解事件 %d 条", rotationEventsMax+2)
	if !strings.Contains(preamble, want) {
		t.Fatalf("摘要行应报告未消解总数 %q:\n%s", want, preamble)
	}
	if listed := strings.Count(preamble, "- ev-"); listed != rotationEventsMax {
		t.Fatalf("未消解事件小节应只列 %d 条，实际 %d 条:\n%s", rotationEventsMax, listed, preamble)
	}
}

// TestHandoffCounts 验证分割线计数与换新前言同源：账本总条数、未结项话题数（已结项不算）、
// 未消解事件总数（超过前言列表上限也照数）。
func TestHandoffCounts(t *testing.T) {
	rig := ledgerRig(t)
	ctx := context.Background()

	if _, err := rig.rt.AddLedgerEntry(ctx, LedgerInProgress, "写登录流程任务书", ""); err != nil {
		t.Fatalf("新增账本条目失败: %v", err)
	}
	if _, err := rig.rt.AddLedgerEntry(ctx, LedgerDecision, "鉴权库不换", ""); err != nil {
		t.Fatalf("新增账本条目失败: %v", err)
	}
	seedTopicList(t, rig, []Topic{
		{ThreadID: "topic-1", ProjectID: "proj-1", Title: "整理登录流程"},
		{ThreadID: "topic-2", ProjectID: "proj-1", Title: "已结项的话题", Closed: true},
	})
	for i := 0; i < rotationEventsMax+2; i++ {
		rig.events.emit(ev(fmt.Sprintf("ev-%d", i), "thread-9", "proj-1"))
	}

	counts, ok := rig.rt.HandoffCounts(ctx)
	if !ok {
		t.Fatalf("三项都读得到时应返回 ok=true")
	}
	want := HandoffCounts{Ledger: 2, Topics: 1, Pending: rotationEventsMax + 2}
	if counts != want {
		t.Fatalf("HandoffCounts = %+v, want %+v", counts, want)
	}
}

// TestHandoffCountsFailsWhenUnwired 验证任何一项读不到时整体返回 ok=false：
// 分割线只写基础文案，不写半真半假的计数。
func TestHandoffCountsFailsWhenUnwired(t *testing.T) {
	t.Run("事件能力未接线", func(t *testing.T) {
		rig := ledgerRig(t)
		rig.rt.events = nil
		if _, ok := rig.rt.HandoffCounts(context.Background()); ok {
			t.Fatalf("事件能力未接线时应返回 ok=false")
		}
	})

	t.Run("事件读取失败", func(t *testing.T) {
		rig := ledgerRig(t)
		rig.events.listErr = errors.New("库挂了")
		if _, ok := rig.rt.HandoffCounts(context.Background()); ok {
			t.Fatalf("事件读取失败时应返回 ok=false")
		}
	})

	t.Run("账本读取失败", func(t *testing.T) {
		rig := ledgerRig(t)
		// 账本内容坏了等价于读库失败：整行计数都不写。
		if err := rig.settings.SetSetting(context.Background(), settingLedger, "{坏 JSON"); err != nil {
			t.Fatalf("写入坏账本失败: %v", err)
		}
		if _, ok := rig.rt.HandoffCounts(context.Background()); ok {
			t.Fatalf("账本解析失败时应返回 ok=false")
		}
	})
}
