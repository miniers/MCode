package mcodeui

import (
	"bytes"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

func testGoroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	line := buf[:n]
	const prefix = "goroutine "
	if !bytes.HasPrefix(line, []byte(prefix)) {
		return 0
	}
	rest := line[len(prefix):]
	end := bytes.IndexByte(rest, ' ')
	if end <= 0 {
		return 0
	}
	id, err := strconv.ParseUint(string(rest[:end]), 10, 64)
	if err != nil {
		return 0
	}
	return id
}

func TestRenderDoesNotLoadConfigSync(t *testing.T) {
	dataDir := settingsTestDir(t)
	t.Setenv("MCODE_DATA", dataDir)

	oldLoad := loadFallbackConfigFunc
	oldNow := fallbackConfigNow
	oldSnap := fallbackConfigCache.Swap(nil)
	t.Cleanup(func() {
		deadline := time.Now().Add(2 * time.Second)
		for fallbackConfigInFlight.Load() && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		loadFallbackConfigFunc = oldLoad
		fallbackConfigNow = oldNow
		fallbackConfigCache.Store(oldSnap)
	})

	var (
		clockMu sync.Mutex
		curNow  = time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	)
	fallbackConfigNow = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return curNow
	}

	var (
		renderGID  atomic.Uint64
		syncCalls  atomic.Int32
		totalCalls atomic.Int32
	)
	loadCh := make(chan struct{}, 8)
	loadFallbackConfigFunc = func(dir string) (config.Config, error) {
		if gid := renderGID.Load(); gid != 0 && testGoroutineID() == gid {
			syncCalls.Add(1)
		}
		totalCalls.Add(1)
		select {
		case loadCh <- struct{}{}:
		default:
		}
		cfg := config.Defaults()
		cfg.Session.ComposerSendKey = config.ComposerSendKeyShiftEnter
		return cfg, nil
	}

	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.threads = []Thread{{ID: "t1", ProjectID: "p1", Title: "测试会话1"}}
	s.mu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) {
		s.setUIServices(c.Services())
		renderGID.Store(testGoroutineID())
		ui.Column(c).Fill().Children(func() {
			renderComposer(c, s)
		})
	}, 1000, 700)

	select {
	case <-loadCh:
	case <-time.After(2 * time.Second):
		t.Fatal("等待首次后台加载配置超时")
	}
	waitFor(t, tt, func() bool {
		return !fallbackConfigInFlight.Load() && s.composerSendKey() == config.ComposerSendKeyShiftEnter
	})

	// 推进超过 2 秒并再次渲染多帧，触发第二次后台刷新。
	clockMu.Lock()
	curNow = curNow.Add(2500 * time.Millisecond)
	clockMu.Unlock()
	tt.Advance(2500 * time.Millisecond)
	s.invalidate()
	tt.Frame()
	tt.Frame()

	select {
	case <-loadCh:
	case <-time.After(2 * time.Second):
		t.Fatal("推进时钟超过 2 秒后等待后台重新加载配置超时")
	}
	waitFor(t, tt, func() bool {
		return !fallbackConfigInFlight.Load()
	})

	if got := syncCalls.Load(); got != 0 {
		t.Fatalf("渲染线程不应同步调用 config.Load，got %d 次", got)
	}
	if got := totalCalls.Load(); got < 2 {
		t.Fatalf("推进时钟超过 2 秒后应触发至少 2 次后台加载，got %d 次", got)
	}
	if got := s.composerSendKey(); got != config.ComposerSendKeyShiftEnter {
		t.Fatalf("后台加载完成后应读到 shift_enter，got %q", got)
	}
}

func TestToolDiffStatCached(t *testing.T) {
	toolParseCacheReset()
	toolDiffStatCacheMu.Lock()
	toolDiffStatCache = make(map[toolDiffStatCacheKey]toolDiffStatCacheEntry, 128)
	toolDiffStatCacheMu.Unlock()

	oldCompute := toolCallDiffStatCompute
	t.Cleanup(func() {
		toolCallDiffStatCompute = oldCompute
		toolDiffStatCacheMu.Lock()
		toolDiffStatCache = make(map[toolDiffStatCacheKey]toolDiffStatCacheEntry, 128)
		toolDiffStatCacheMu.Unlock()
	})

	var parseCalls atomic.Int32
	toolCallDiffStatCompute = func(m Message) (int, int) {
		parseCalls.Add(1)
		return toolCallDiffStatUncached(m)
	}

	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	m := Message{
		ID:        "diff-cache-1",
		Role:      "tool",
		ToolName:  "edit_file",
		ToolInput: `{"file_path":"a.go","old_string":"a","new_string":"a\nb\nc"}`,
	}

	var renderFrames atomic.Int32
	tt := ui.NewTester(func(c *ui.Context) {
		s.setUIServices(c.Services())
		renderFrames.Add(1)
		renderToolChip(c, s, m, nil)
	}, 800, 600)

	s.invalidate()
	tt.Frame()

	if renderFrames.Load() < 2 {
		t.Fatalf("应至少渲染 2 帧，got %d", renderFrames.Load())
	}
	if got := parseCalls.Load(); got != 1 {
		t.Fatalf("同一条消息连续渲染两帧时 diff 解析应只发生 1 次，got %d", got)
	}
	if !tt.HasText("+2") {
		t.Fatalf("应渲染缓存的 diff 统计 +2，got %q", tt.Texts())
	}
}

func TestToolOutputExpandTruncated(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	text := strings.Repeat("a", 500_000)
	m := Message{ID: "expand-trunc-1", Role: "tool", Text: text}

	tt := ui.NewTester(func(c *ui.Context) {
		s.setUIServices(c.Services())
		renderToolDetail(c, s, m)
	}, 800, 600)

	if !tt.HasText("展开全部") {
		t.Fatalf("超长输出折叠时应显示「展开全部」，got %q", tt.Texts())
	}
	clickText(t, tt, "展开全部")
	waitFor(t, tt, func() bool {
		return tt.HasText(toolOutputTruncatedNotice)
	})

	for _, txt := range tt.Texts() {
		if len(txt) > toolOutputExpandedMaxBytes {
			t.Fatalf("展开后渲染文本字节数 %d 超过上限 %d", len(txt), toolOutputExpandedMaxBytes)
		}
		if lines := strings.Count(txt, "\n") + 1; lines > toolOutputExpandedMaxLines {
			t.Fatalf("展开后渲染文本行数 %d 超过上限 %d", lines, toolOutputExpandedMaxLines)
		}
	}
}
