package startuptrace

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"
)

func TestStartupTrace_DedupAndFlush(t *testing.T) {
	var buf bytes.Buffer
	origOutput := log.Writer()
	origFlags := log.Flags()
	defer func() {
		log.SetOutput(origOutput)
		log.SetFlags(origFlags)
	}()

	log.SetOutput(&buf)
	log.SetFlags(0)

	start := time.Now().Add(-100 * time.Millisecond)
	resetForTesting(start)

	// Flush 前 Mark：同名去重，缓存不写日志
	Mark("p1")
	Mark("p1")
	Mark("p2")

	if buf.Len() > 0 {
		t.Fatalf("Flush 前不应有日志输出，实际输出: %s", buf.String())
	}

	// 执行 Flush：一次性补写已缓存点
	Flush()

	got := buf.String()
	if !strings.Contains(got, "startup: p1 +") || !strings.Contains(got, "startup: p2 +") {
		t.Fatalf("Flush 后应输出 p1 与 p2，实际输出: %s", got)
	}
	if strings.Count(got, "startup: p1 +") != 1 {
		t.Fatalf("p1 应只输出 1 次，实际输出: %s", got)
	}

	buf.Reset()

	// Flush 之后 Mark：直接写日志
	Mark("p3")
	gotP3 := buf.String()
	if !strings.Contains(gotP3, "startup: p3 +") {
		t.Fatalf("Flush 后的 Mark 应当直接输出日志，实际: %s", gotP3)
	}

	// Flush 之后重复 Mark：去重忽略
	buf.Reset()
	Mark("p3")
	if buf.Len() > 0 {
		t.Fatalf("重复 Mark 应被忽略，实际输出: %s", buf.String())
	}
}

func TestStartupTrace_DisabledWritesNothing(t *testing.T) {
	var buf bytes.Buffer
	origOutput := log.Writer()
	origFlags := log.Flags()
	origEnabled := enabled
	defer func() {
		log.SetOutput(origOutput)
		log.SetFlags(origFlags)
		resetForTesting(time.Time{})
		enabled = origEnabled
	}()

	log.SetOutput(&buf)
	log.SetFlags(0)

	resetForTesting(time.Time{})
	enabled = false

	Mark("off1")
	Mark("off2")
	Flush()
	Mark("off3")
	Flush()

	if buf.Len() > 0 {
		t.Fatalf("未开启时不应有任何日志输出，实际输出: %s", buf.String())
	}
	if len(points) != 0 {
		t.Fatalf("未开启时不应缓存计时点，实际缓存: %d", len(points))
	}
}
