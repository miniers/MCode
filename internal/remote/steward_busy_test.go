package remote

import (
	"context"
	"reflect"
	"testing"
	"time"

	"mcode/internal/remote/wire"
)

func TestSetStewardBusy(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	b.AddThread(wire.ThreadSummary{ID: "feed", Provider: "steward"}, nil)
	b.mu.Lock()
	b.stewardFeedID = "feed"
	b.mu.Unlock()
	var states []bool
	remove := hub.AddObserver(func(id, method string, value any) {
		if id == "feed" && method == "thread.summary" {
			states = append(states, value.(wire.ThreadSummary).IsRunning)
		}
	})
	defer remove()
	b.SetStewardBusy("feed", true)
	summary, err := b.GetThread(context.Background(), "feed")
	if err != nil || !summary.IsRunning {
		t.Fatalf("前台处理中，摘要 %+v，错误 %v", summary, err)
	}
	if !reflect.DeepEqual(states, []bool{true}) {
		t.Fatalf("发布状态 %v", states)
	}
	b.mu.Lock()
	td := b.threads["feed"]
	runTurnID := td.runTurnID
	messageCount := len(td.messages)
	b.mu.Unlock()
	if runTurnID != "" {
		t.Fatalf("前台处理中存在执行轮次 %q", runTurnID)
	}
	b.SetStewardBusy("feed", true)
	if !reflect.DeepEqual(states, []bool{true}) {
		t.Fatalf("重复设置后发布状态 %v", states)
	}
	result, err := b.Interrupt(context.Background(), wire.InterruptParams{ThreadID: "feed"})
	if err != nil || result.State != wire.InterruptStateAlreadyStopped {
		t.Fatalf("中断结果 %+v，错误 %v", result, err)
	}
	b.mu.Lock()
	newMessageCount := len(td.messages)
	hasWatchdog := td.watchdogStop != nil
	b.mu.Unlock()
	if newMessageCount != messageCount || hasWatchdog {
		t.Fatalf("中断后消息数 %d -> %d，看门狗 %v", messageCount, newMessageCount, hasWatchdog)
	}
	b.SetStewardBusy("feed", false)
	summary, err = b.GetThread(context.Background(), "feed")
	if err != nil || summary.IsRunning {
		t.Fatalf("前台空闲时摘要 %+v，错误 %v", summary, err)
	}
	if !reflect.DeepEqual(states, []bool{true, false}) {
		t.Fatalf("发布状态 %v", states)
	}
}
