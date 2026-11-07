package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/providers/credits"
)

// planFixture 读取 testdata 下的录制样本，测试不访问网络也不启动 CLI。
func planFixture(t *testing.T, name string) json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("读取 fixture %s 失败: %v", name, err)
	}
	return data
}

func planFixedNow() time.Time { return time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC) }

func TestParsePlanLimits_Individual(t *testing.T) {
	now := planFixedNow()
	limits, err := ParsePlanLimits(planFixture(t, "plan_limits_individual.json"), now)
	if err != nil {
		t.Fatalf("ParsePlanLimits 失败: %v", err)
	}
	if limits.Plan != "Copilot Individual" {
		t.Errorf("Plan = %q，期望 %q", limits.Plan, "Copilot Individual")
	}
	if !limits.ReadAt.Equal(now) {
		t.Errorf("ReadAt = %v，期望 %v", limits.ReadAt, now)
	}
	want := []credits.Window{
		{Label: "Premium requests", UsedPercent: 25, ResetsAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
		{Label: "Chat messages", UsedPercent: 40, ResetsAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
		{Label: "Code completions", UsedPercent: 0, ResetsAt: time.Date(2026, 11, 1, 0, 0, 0, 500000000, time.UTC)},
	}
	if len(limits.Windows) != len(want) {
		t.Fatalf("窗口数 = %d，期望 %d: %+v", len(limits.Windows), len(want), limits.Windows)
	}
	for i := range want {
		got := limits.Windows[i]
		if got.Label != want[i].Label || got.UsedPercent != want[i].UsedPercent || !got.ResetsAt.Equal(want[i].ResetsAt) {
			t.Errorf("窗口 %d = %+v，期望 %+v", i, got, want[i])
		}
	}
}

// 无限配额（isUnlimitedEntitlement）、null 快照与非正配额都应被跳过，窗口为空不算错误。
func TestParsePlanLimits_UnlimitedAndMissingSkipped(t *testing.T) {
	limits, err := ParsePlanLimits(planFixture(t, "plan_limits_business.json"), planFixedNow())
	if err != nil {
		t.Fatalf("ParsePlanLimits 失败: %v", err)
	}
	if limits.Plan != "Copilot Business" {
		t.Errorf("Plan = %q，期望 %q", limits.Plan, "Copilot Business")
	}
	if len(limits.Windows) != 0 {
		t.Errorf("期望无窗口，实际 %+v", limits.Windows)
	}
}

func TestParsePlanLimits_PercentClampAndComputed(t *testing.T) {
	raw := json.RawMessage(`{"quotaSnapshots":{
		"premium_interactions":{"entitlementRequests":100,"usedRequests":40,"remainingPercentage":-20},
		"chat":{"entitlementRequests":50,"usedRequests":200}}}`)
	limits, err := ParsePlanLimits(raw, planFixedNow())
	if err != nil {
		t.Fatalf("ParsePlanLimits 失败: %v", err)
	}
	if len(limits.Windows) != 2 {
		t.Fatalf("窗口数 = %d，期望 2: %+v", len(limits.Windows), limits.Windows)
	}
	if limits.Windows[0].UsedPercent != 100 {
		t.Errorf("remainingPercentage=-20 应钳到 100，实际 %v", limits.Windows[0].UsedPercent)
	}
	if limits.Windows[1].UsedPercent != 100 {
		t.Errorf("used/entitlement=400%% 应钳到 100，实际 %v", limits.Windows[1].UsedPercent)
	}
	if !limits.Windows[1].ResetsAt.IsZero() {
		t.Errorf("缺 resetDate 应为零值，实际 %v", limits.Windows[1].ResetsAt)
	}
}

func TestParsePlanLimits_Malformed(t *testing.T) {
	if _, err := ParsePlanLimits(planFixture(t, "plan_limits_malformed.json"), planFixedNow()); err == nil {
		t.Fatal("格式错误的响应应返回错误")
	}
}

func TestParsePlanLimits_MissingSnapshots(t *testing.T) {
	limits, err := ParsePlanLimits(planFixture(t, "plan_limits_missing.json"), planFixedNow())
	if err != nil {
		t.Fatalf("缺少 quotaSnapshots 应按无窗口处理: %v", err)
	}
	if limits.Plan != "" || len(limits.Windows) != 0 {
		t.Errorf("期望空结果，实际 %+v", limits)
	}
}

func TestReadPlanLimits_CallsQuotaMethod(t *testing.T) {
	fixture := planFixture(t, "plan_limits_individual.json")
	var gotMethod string
	var gotParams any
	call := func(_ context.Context, method string, params any) (json.RawMessage, error) {
		gotMethod = method
		gotParams = params
		return fixture, nil
	}
	limits, err := ReadPlanLimits(context.Background(), call, planFixedNow())
	if err != nil {
		t.Fatalf("ReadPlanLimits 失败: %v", err)
	}
	if gotMethod != "account.getQuota" {
		t.Errorf("RPC 方法 = %q，期望 %q", gotMethod, "account.getQuota")
	}
	if params, ok := gotParams.(map[string]any); !ok || len(params) != 0 {
		t.Errorf("RPC 参数 = %#v，期望空表", gotParams)
	}
	if limits.Plan != "Copilot Individual" || len(limits.Windows) != 3 {
		t.Errorf("解析结果不符: %+v", limits)
	}
}

func TestReadPlanLimits_PropagatesError(t *testing.T) {
	sentinel := errors.New("rpc 失败")
	call := func(_ context.Context, _ string, _ any) (json.RawMessage, error) {
		return nil, sentinel
	}
	if _, err := ReadPlanLimits(context.Background(), call, planFixedNow()); !errors.Is(err, sentinel) {
		t.Fatalf("错误未透传: %v", err)
	}
}
