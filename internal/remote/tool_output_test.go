package remote

import (
	"strings"
	"testing"
	"time"
)

// 追加语义、节流窗口、字节阈值与最终覆盖都要按约定工作。
func TestToolOutputStreamAppendThrottle(t *testing.T) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	stream := newToolOutputStream("Bash {\"cmd\":\"make\"}")

	// 第一条片段：距上一次下发（零值）已远超间隔，立即下发。
	if !stream.append("first\n", base) {
		t.Fatal("第一条片段应立即下发")
	}
	stream.markFlushed(base)
	if got, want := stream.render(), "Bash {\"cmd\":\"make\"}\nfirst\n"; got != want {
		t.Fatalf("首次下发后条目正文 = %q, want %q", got, want)
	}

	// 节流窗口内的片段只进缓冲，条目正文不变。
	if stream.append("second\n", base.Add(10*time.Millisecond)) {
		t.Fatal("间隔内的片段不应下发")
	}
	if stream.append("third\n", base.Add(50*time.Millisecond)) {
		t.Fatal("间隔内的片段不应下发")
	}
	if got, want := stream.render(), "Bash {\"cmd\":\"make\"}\nfirst\n"; got != want {
		t.Fatalf("缓冲中的片段不应出现在条目正文里, got %q, want %q", got, want)
	}

	// 超过间隔后，缓冲里的片段一次合并下发。
	flushAt := base.Add(toolOutputFlushInterval)
	if !stream.due(flushAt) {
		t.Fatal("超过间隔后应下发")
	}
	stream.markFlushed(flushAt)
	want := "Bash {\"cmd\":\"make\"}\nfirst\nsecond\nthird\n"
	if got := stream.render(); got != want {
		t.Fatalf("合并下发后条目正文 = %q, want %q", got, want)
	}

	// 累计达到字节阈值时不等间隔也要下发。
	big := strings.Repeat("x", toolOutputFlushBytes)
	if !stream.append(big, flushAt.Add(time.Millisecond)) {
		t.Fatal("累计达到字节阈值应立即下发")
	}
}

// tool_end 的最终输出覆盖运行中流出的增量，未下发的残余片段随之作废。
func TestToolOutputStreamOverrideFinalOutput(t *testing.T) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	stream := newToolOutputStream("Bash")
	if !stream.append("partial\n", base) {
		t.Fatal("首条片段应立即下发")
	}
	stream.markFlushed(base)
	stream.append("tail\n", base.Add(time.Millisecond)) // 还在缓冲里，tool_end 时应作废

	if got, want := stream.override("final output\n"), "Bash\nfinal output\n"; got != want {
		t.Fatalf("最终输出应覆盖增量, got %q, want %q", got, want)
	}
}

// provider 只在运行中推增量、tool_end 不带 output 时，残余片段必须先刷出。
func TestToolOutputStreamOverrideKeepsStreamedOutput(t *testing.T) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	stream := newToolOutputStream("Bash")
	if !stream.append("first\n", base) {
		t.Fatal("首条片段应立即下发")
	}
	stream.markFlushed(base)
	stream.append("tail\n", base.Add(time.Millisecond)) // 残余片段

	if got, want := stream.override(""), "Bash\nfirst\ntail\n"; got != want {
		t.Fatalf("没有最终输出时应保留并刷出全部增量, got %q, want %q", got, want)
	}
}

// 增量输出与最终输出都按 2000 字符截断，避免工具条目无限膨胀。
func TestToolOutputStreamTruncate(t *testing.T) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	stream := newToolOutputStream("Bash")
	long := strings.Repeat("啊", toolOutputStreamMaxRunes+50)
	if !stream.append(long, base) {
		t.Fatal("首条片段应立即下发")
	}
	stream.markFlushed(base)
	got := stream.render()
	if want := "Bash\n" + strings.Repeat("啊", toolOutputStreamMaxRunes); got != want {
		t.Fatalf("增量输出应按 %d 字符截断, got %d 字符", toolOutputStreamMaxRunes, len([]rune(got)))
	}

	if got, want := stream.override(long), "Bash\n"+strings.Repeat("啊", toolOutputStreamMaxRunes); got != want {
		t.Fatalf("最终输出同样应截断, got %d 字符", len([]rune(got)))
	}
}

// 空缓冲时条目正文只剩固定正文，且不会下发。
func TestToolOutputStreamEmpty(t *testing.T) {
	stream := newToolOutputStream("Bash")
	if stream.due(time.Now()) {
		t.Fatal("没有待发片段时不应下发")
	}
	if got := stream.render(); got != "Bash" {
		t.Fatalf("空缓冲时条目正文应只剩固定正文, got %q", got)
	}
	if got := stream.override(""); got != "Bash" {
		t.Fatalf("没有增量也没有最终输出时条目正文应只剩固定正文, got %q", got)
	}
}
