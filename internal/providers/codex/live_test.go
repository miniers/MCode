package codex

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/harness"
)

func TestLiveCodexRun(t *testing.T) {
	if os.Getenv("MCODE_CODEX_LIVE") != "1" {
		t.Skip("skipping live codex test; set MCODE_CODEX_LIVE=1 to run")
	}

	workDir := t.TempDir()
	h := New(Options{})
	defer func() {
		_ = h.Close()
	}()

	// Round 1: 创建新会话
	ctx1, cancel1 := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel1()

	var mu sync.Mutex
	var events1 []harness.Event
	var r1SessionID string
	var r1FirstText time.Duration
	r1Start := time.Now()

	emit1 := func(e harness.Event) {
		mu.Lock()
		defer mu.Unlock()
		events1 = append(events1, e)
		if e.Type == harness.EventSession && e.SessionID != "" {
			r1SessionID = e.SessionID
		}
		if e.Type == harness.EventText && r1FirstText == 0 {
			r1FirstText = time.Since(r1Start)
		}
	}

	turn1 := harness.Turn{
		WorkDir: workDir,
		Mode:    harness.ModeAgent,
		Prompt:  "请回复：PONG",
	}

	if err := h.Run(ctx1, turn1, emit1); err != nil {
		t.Fatalf("live codex run round 1 failed: %v", err)
	}
	r1Duration := time.Since(r1Start)

	if r1SessionID == "" {
		t.Error("expected to see EventSession with session_id in round 1")
	}
	t.Logf("Round 1 成功完成: 首字耗时=%v, 总耗时=%v, SessionID=%s", r1FirstText.Round(time.Millisecond), r1Duration.Round(time.Millisecond), r1SessionID)

	// Round 2: 续接会话，复用常驻进程
	ctx2, cancel2 := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel2()

	var events2 []harness.Event
	var r2FirstText time.Duration
	r2Start := time.Now()

	emit2 := func(e harness.Event) {
		mu.Lock()
		defer mu.Unlock()
		events2 = append(events2, e)
		if e.Type == harness.EventText && r2FirstText == 0 {
			r2FirstText = time.Since(r2Start)
		}
	}

	turn2 := harness.Turn{
		WorkDir:   workDir,
		Mode:      harness.ModeAgent,
		Prompt:    "请回复：PONG",
		SessionID: r1SessionID,
	}

	if err := h.Run(ctx2, turn2, emit2); err != nil {
		t.Fatalf("live codex run round 2 failed: %v", err)
	}
	r2Duration := time.Since(r2Start)

	mu.Lock()
	var sawText2 bool
	var sawDone2 bool
	var fullText2 strings.Builder
	for _, e := range events2 {
		switch e.Type {
		case harness.EventText:
			sawText2 = true
			fullText2.WriteString(e.Text)
		case harness.EventDone:
			sawDone2 = true
		}
	}
	mu.Unlock()

	if !sawText2 {
		t.Error("expected to see EventText in round 2")
	}
	if !sawDone2 {
		t.Error("expected to see EventDone in round 2")
	}
	t.Logf("Round 2 成功完成: 首字耗时=%v, 总耗时=%v, 内容=%q", r2FirstText.Round(time.Millisecond), r2Duration.Round(time.Millisecond), fullText2.String())
}
