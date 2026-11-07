package mcodeui

import (
	"math"
	"testing"
	"time"
)

// 场景 1：已用比例不足 5% 时不预测。
func TestQuotaPaceSkipsLowUsage(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	win := ProviderQuotaWindow{Period: "5h", UsedPercent: 4, ResetsAt: now.Add(2 * time.Hour)}

	state, runOutAt := quotaPace(now, win)
	if state != paceStateUnknown {
		t.Fatalf("已用 4%% 不应预测，得到 state=%v", state)
	}
	if !runOutAt.IsZero() {
		t.Fatalf("不预测时用尽时间应为零值，得到 %v", runOutAt)
	}
}

// 场景 2：已过去时长不足窗口 3% 时不预测（5h 窗口的 3% 是 9 分钟）。
func TestQuotaPaceSkipsEarlyWindow(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	// 重置在 4 小时 58 分钟后，说明窗口只过去了 2 分钟。
	win := ProviderQuotaWindow{Period: "5h", UsedPercent: 60, ResetsAt: now.Add(4*time.Hour + 58*time.Minute)}

	state, _ := quotaPace(now, win)
	if state != paceStateUnknown {
		t.Fatalf("过去 2 分钟（不足窗口 3%%）不应预测，得到 state=%v", state)
	}
}

// 场景 3：周期信息缺失时不预测。
func TestQuotaPaceWithoutPeriod(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	win := ProviderQuotaWindow{Label: "未知窗口", UsedPercent: 80, ResetsAt: now.Add(time.Hour)}

	if state, _ := quotaPace(now, win); state != paceStateUnknown {
		t.Fatalf("没有周期信息不应预测，得到 state=%v", state)
	}
}

// 场景 4：已用满 100% 时视为刚好用尽，用尽时间就是当前时刻。
func TestQuotaPaceUsedUp(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	win := ProviderQuotaWindow{Period: "5h", UsedPercent: 100, ResetsAt: now.Add(2 * time.Hour)}

	state, runOutAt := quotaPace(now, win)
	if state != paceStateEarly {
		t.Fatalf("已用满应判为将提前用尽，得到 state=%v", state)
	}
	if !runOutAt.Equal(now) {
		t.Fatalf("已用满的用尽时间应为 now，得到 %v", runOutAt)
	}
}

// 场景 5：按「已用 / 已过去」外推，预计早于重置时间时判为将提前用尽并给出用尽时间。
func TestQuotaPaceEarlyRunOut(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	// 5h 窗口过去了 1 小时（重置在 4 小时后），已用 50%：按此速度 1 小时后用尽。
	win := ProviderQuotaWindow{Period: "5h", UsedPercent: 50, ResetsAt: now.Add(4 * time.Hour)}

	state, runOutAt := quotaPace(now, win)
	if state != paceStateEarly {
		t.Fatalf("预计 1 小时后用尽、重置在 4 小时后，应为将提前用尽，得到 state=%v", state)
	}
	if want := now.Add(time.Hour); !runOutAt.Equal(want) {
		t.Fatalf("预计用尽时间应为 %v，得到 %v", want, runOutAt)
	}
	if got, want := paceRunOutText(now, runOutAt, state), "预计约 1 小时后用尽"; got != want {
		t.Fatalf("提示文案应为 %q，得到 %q", want, got)
	}
}

// 场景 6：预测用尽时间正好等于重置时间时按节奏正常处理。
func TestQuotaPaceExactlyAtReset(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	// 5h 窗口过去 2.5 小时（重置在 2.5 小时后），已用 50%：正好在重置时刻用尽。
	win := ProviderQuotaWindow{Period: "5h", UsedPercent: 50, ResetsAt: now.Add(150 * time.Minute)}

	state, runOutAt := quotaPace(now, win)
	if state != paceStateNormal {
		t.Fatalf("正好在重置时刻用尽应判为节奏正常，得到 state=%v", state)
	}
	if !runOutAt.IsZero() {
		t.Fatalf("节奏正常时不返回用尽时间，得到 %v", runOutAt)
	}
}

// 场景 7：周窗口同样按周周期推导已过去时长。
func TestQuotaPaceWeekWindow(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	// 7d 窗口过去 1 天（重置在 6 天后），已用 60%：剩余 40% 约 16 小时用完。
	win := ProviderQuotaWindow{Period: "7d", UsedPercent: 60, ResetsAt: now.Add(6 * 24 * time.Hour)}

	state, runOutAt := quotaPace(now, win)
	if state != paceStateEarly {
		t.Fatalf("周窗口预计提前用尽，得到 state=%v", state)
	}
	if got := runOutAt.Sub(now); got != 16*time.Hour {
		t.Fatalf("预计用尽应在 16 小时后，得到 %v", got)
	}
}

// 场景 8：已到重置时刻（数据未刷新）时不预测。
func TestQuotaPaceAfterResetTime(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	win := ProviderQuotaWindow{Period: "5h", UsedPercent: 90, ResetsAt: now.Add(-time.Minute)}

	if state, _ := quotaPace(now, win); state != paceStateUnknown {
		t.Fatalf("已到重置时刻不应预测，得到 state=%v", state)
	}
}

// 场景 9：formatPaceRunOut 覆盖各时间档位，paceRunOutText 只在将提前用尽时生成提示。
func TestFormatPaceRunOut(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		runOut time.Time
		want   string
	}{
		{name: "零值", runOut: time.Time{}, want: ""},
		{name: "已过", runOut: now.Add(-time.Minute), want: ""},
		{name: "不足 1 分钟", runOut: now.Add(30 * time.Second), want: "1 分钟内"},
		{name: "分钟档", runOut: now.Add(30 * time.Minute), want: "约 30 分钟后"},
		{name: "小时档", runOut: now.Add(2 * time.Hour), want: "约 2 小时后"},
		{name: "小时分钟档", runOut: now.Add(2*time.Hour + 30*time.Minute), want: "约 2 时 30 分后"},
		{name: "天档", runOut: now.Add(50 * time.Hour), want: "约 2 天后"},
	}

	for _, tt := range tests {
		if got := formatPaceRunOut(now, tt.runOut); got != tt.want {
			t.Fatalf("%s：formatPaceRunOut 应为 %q，得到 %q", tt.name, tt.want, got)
		}
	}

	if got, want := paceRunOutText(now, now.Add(time.Hour), paceStateNormal), ""; got != want {
		t.Fatalf("节奏正常不应生成提示，得到 %q", got)
	}
	if got, want := paceRunOutText(now, now.Add(time.Hour), paceStateEarly), "预计约 1 小时后用尽"; got != want {
		t.Fatalf("将提前用尽的提示应为 %q，得到 %q", want, got)
	}
}

// quotaElapsedRatio 按「重置时间 - 窗口时长」倒推起点，算出时间已过比例。
func TestQuotaElapsedRatio(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	// 5 小时窗口，重置在 2 小时后：起点是 3 小时前，时间已过 3/5 = 0.6。
	win := ProviderQuotaWindow{Period: "5h", ResetsAt: now.Add(2 * time.Hour)}

	got, ok := quotaElapsedRatio(now, win)
	if !ok {
		t.Fatalf("有周期与重置时间时应能算出时间进度")
	}
	if math.Abs(got-0.6) > 1e-6 {
		t.Fatalf("时间已过比例应为 0.6，得到 %v", got)
	}

	// 重置时间早于一个完整周期（窗口还没开始）时无法计算。
	notStarted := ProviderQuotaWindow{Period: "5h", ResetsAt: now.Add(6 * time.Hour)}
	if _, ok := quotaElapsedRatio(now, notStarted); ok {
		t.Fatalf("窗口还没开始时应返回 false")
	}
	// 重置时刻为零或周期未知时无法计算。
	if _, ok := quotaElapsedRatio(now, ProviderQuotaWindow{Period: "5h"}); ok {
		t.Fatalf("重置时刻为零时应返回 false")
	}
	if _, ok := quotaElapsedRatio(now, ProviderQuotaWindow{UsedPercent: 50, ResetsAt: now.Add(time.Hour)}); ok {
		t.Fatalf("周期未知时应返回 false")
	}
}

// quotaBarSegments 按顺序拆出正常填充、超速段、预测虚影、空轨道与时间竖线。
func TestQuotaBarSegments(t *testing.T) {
	tests := []struct {
		name      string
		used      float64
		elapsed   float64
		paceKnown bool
		projected bool
		want      []quotaBarSegment
	}{
		{
			// 已用 40%、时间过了 60%，不悬停：只有正常填充和空轨道，竖线落在 0.6。
			name: "未超速且不悬停", used: 0.4, elapsed: 0.6, paceKnown: true,
			want: []quotaBarSegment{
				{kind: quotaBarSegmentNormal, grow: 0.4},
				{kind: quotaBarSegmentEmpty, grow: 0.2},
				{kind: quotaBarSegmentMarker},
				{kind: quotaBarSegmentEmpty, grow: 0.4},
			},
		},
		{
			// 已用 70%、时间过了 50%：50% 之后到 70% 是超速段，竖线在 0.5。
			name: "超速", used: 0.7, elapsed: 0.5, paceKnown: true,
			want: []quotaBarSegment{
				{kind: quotaBarSegmentNormal, grow: 0.5},
				{kind: quotaBarSegmentMarker},
				{kind: quotaBarSegmentOver, grow: 0.2},
				{kind: quotaBarSegmentEmpty, grow: 0.3},
			},
		},
		{
			// 已用 40%、时间过了 60%，预测到重置时用 40/60 ≈ 66.7%：虚影正好止于 p。
			name: "未超速且悬停", used: 0.4, elapsed: 0.6, paceKnown: true, projected: true,
			want: []quotaBarSegment{
				{kind: quotaBarSegmentNormal, grow: 0.4},
				{kind: quotaBarSegmentGhost, grow: 0.2},
				{kind: quotaBarSegmentMarker},
				{kind: quotaBarSegmentGhost, grow: 1.0 / 15.0},
				{kind: quotaBarSegmentEmpty, grow: 1.0 / 3.0},
			},
		},
		{
			// 已用 70%、时间过了 50%：预测到重置时会用 140%，虚影以危险色延伸到末端。
			name: "超速且悬停", used: 0.7, elapsed: 0.5, paceKnown: true, projected: true,
			want: []quotaBarSegment{
				{kind: quotaBarSegmentNormal, grow: 0.5},
				{kind: quotaBarSegmentMarker},
				{kind: quotaBarSegmentOver, grow: 0.2},
				{kind: quotaBarSegmentGhostOver, grow: 0.3},
			},
		},
		{
			// 数据不足不判断节奏：即使已用超过时间进度也全部按正常填充处理，竖线仍在 0.1。
			name: "paceKnown 为 false", used: 0.8, elapsed: 0.1, paceKnown: false, projected: true,
			want: []quotaBarSegment{
				{kind: quotaBarSegmentNormal, grow: 0.1},
				{kind: quotaBarSegmentMarker},
				{kind: quotaBarSegmentNormal, grow: 0.7},
				{kind: quotaBarSegmentEmpty, grow: 0.2},
			},
		},
		{
			name: "已用为零", used: 0, elapsed: 0.5, paceKnown: true, projected: true,
			want: []quotaBarSegment{
				{kind: quotaBarSegmentEmpty, grow: 0.5},
				{kind: quotaBarSegmentMarker},
				{kind: quotaBarSegmentEmpty, grow: 0.5},
			},
		},
		{
			// 已用满：时间进度之前是正常填充，之后全是超速段。
			name: "已用满", used: 1, elapsed: 0.5, paceKnown: true,
			want: []quotaBarSegment{
				{kind: quotaBarSegmentNormal, grow: 0.5},
				{kind: quotaBarSegmentMarker},
				{kind: quotaBarSegmentOver, grow: 0.5},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := quotaBarSegments(tt.used, tt.elapsed, tt.paceKnown, tt.projected)
			if len(got) != len(tt.want) {
				t.Fatalf("段数应为 %d，得到 %d：%+v", len(tt.want), len(got), got)
			}
			for i := range tt.want {
				if got[i].kind != tt.want[i].kind {
					t.Fatalf("第 %d 段的类型应为 %v，得到 %v", i, tt.want[i].kind, got[i].kind)
				}
				if math.Abs(float64(got[i].grow-tt.want[i].grow)) > 1e-5 {
					t.Fatalf("第 %d 段的 grow 应为 %v，得到 %v", i, tt.want[i].grow, got[i].grow)
				}
			}
		})
	}
}
