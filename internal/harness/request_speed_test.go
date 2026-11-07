package harness

import (
	"encoding/json"
	"testing"
	"time"
)

func TestRequestClockFinishWithoutBegin(t *testing.T) {
	var c RequestClock
	if _, ok := c.Finish(time.Now(), 100); ok {
		t.Fatal("未 Begin 时应返回 false")
	}
}

func TestRequestClockBeginOnlyFirstWins(t *testing.T) {
	var c RequestClock
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c.Begin(start)
	c.Begin(start.Add(500 * time.Millisecond))

	speed, ok := c.Finish(start.Add(time.Second), 200)
	if !ok {
		t.Fatal("正常请求应返回 true")
	}
	if speed.DurationMs != 1000 {
		t.Fatalf("重复 Begin 不应改写起点：期望 1000ms，实际 %dms", speed.DurationMs)
	}
	if speed.OutputTokens != 200 {
		t.Fatalf("OutputTokens 期望 200，实际 %d", speed.OutputTokens)
	}
}

func TestRequestClockShortDurationIgnored(t *testing.T) {
	var c RequestClock
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c.Begin(start)
	if _, ok := c.Finish(start.Add(99*time.Millisecond), 10); ok {
		t.Fatal("时长不足 100ms 时应返回 false")
	}
	if _, ok := c.Finish(start.Add(time.Second), 0); ok {
		t.Fatal("输出为 0 时应返回 false")
	}
}

func TestRequestClockFinishResets(t *testing.T) {
	var c RequestClock
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c.Begin(start)
	if _, ok := c.Finish(start.Add(2*time.Second), 50); !ok {
		t.Fatal("第一轮应返回 true")
	}
	if _, ok := c.Finish(start.Add(3*time.Second), 50); ok {
		t.Fatal("Finish 之后 clock 应已重置")
	}
}

func TestRequestSpeedJSONRoundTrip(t *testing.T) {
	want := Usage{
		InputTokens:  10,
		OutputTokens: 20,
		Requests: []RequestSpeed{
			{OutputTokens: 12, DurationMs: 4000},
			{OutputTokens: 8, DurationMs: 1500},
		},
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal usage: %v", err)
	}
	var got Usage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal usage: %v", err)
	}
	if len(got.Requests) != 2 || got.Requests[0] != want.Requests[0] || got.Requests[1] != want.Requests[1] {
		t.Fatalf("Requests 往返不一致: %s", raw)
	}
}
