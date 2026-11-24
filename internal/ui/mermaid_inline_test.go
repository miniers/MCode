package mcodeui

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

const minimalMermaidSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 120 60"><rect width="120" height="60" rx="6"/><text x="16" y="34">A to B</text></svg>`

func setupMermaidInlineTest(t *testing.T, fn func(code string, dark bool) (string, error)) {
	t.Helper()
	mermaidInlineRenderer.resetForTest()
	prev := mermaidRenderSVG
	mermaidRenderSVG = fn
	t.Cleanup(func() {
		mermaidRenderSVG = prev
		mermaidInlineRenderer.resetForTest()
	})
}

func waitMermaidEntryDone(t *testing.T, code string, dark bool) mermaidCacheEntry {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if entry, ok := mermaidInlineRenderer.getEntry(code, dark); ok && entry.status != mermaidStatusPending {
			return entry
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待 mermaid 渲染完成超时: dark=%v", dark)
	return mermaidCacheEntry{}
}

func TestMermaidInlineCacheDedupAndDone(t *testing.T) {
	var calls atomic.Int32
	setupMermaidInlineTest(t, func(code string, dark bool) (string, error) {
		calls.Add(1)
		time.Sleep(15 * time.Millisecond)
		return minimalMermaidSVG, nil
	})

	code := "graph TD\nA-->B\n"
	for i := 0; i < 4; i++ {
		mermaidInlineRenderer.lookupOrStart(ui.Services{}, nil, code, false)
	}

	entry := waitMermaidEntryDone(t, code, false)
	if entry.status != mermaidStatusDone || entry.svg == nil || entry.err != nil {
		t.Fatalf("期望状态 done 且 SVG 非 nil，实际 status=%v svg=%v err=%v", entry.status, entry.svg != nil, entry.err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("同一 code 多次请求应只调用一次，实际调用 %d 次", got)
	}

	// 完成后再次查询命中缓存，不再增加调用次数。
	again := mermaidInlineRenderer.lookupOrStart(ui.Services{}, nil, code, false)
	if again.status != mermaidStatusDone || again.svg == nil {
		t.Fatalf("缓存命中状态异常: status=%v svg=%v", again.status, again.svg != nil)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("命中缓存后不应再调用渲染，实际调用 %d 次", got)
	}
}

func TestMermaidInlineErrorAndForeignObjectFailed(t *testing.T) {
	t.Run("render error", func(t *testing.T) {
		setupMermaidInlineTest(t, func(code string, dark bool) (string, error) {
			return "", errors.New("boom")
		})
		code := "graph TD\nErr-->Fail\n"
		mermaidInlineRenderer.lookupOrStart(ui.Services{}, nil, code, false)
		entry := waitMermaidEntryDone(t, code, false)
		if entry.status != mermaidStatusFailed || entry.svg != nil {
			t.Fatalf("渲染报错应记为 failed 且 SVG 为 nil，实际 status=%v svg=%v", entry.status, entry.svg != nil)
		}
	})

	t.Run("foreignObject unsupported", func(t *testing.T) {
		setupMermaidInlineTest(t, func(code string, dark bool) (string, error) {
			return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 120 60"><foreignObject width="100" height="40"><div>hi</div></foreignObject></svg>`, nil
		})
		code := "graph TD\nFO-->Fail\n"
		mermaidInlineRenderer.lookupOrStart(ui.Services{}, nil, code, false)
		entry := waitMermaidEntryDone(t, code, false)
		if entry.status != mermaidStatusFailed || entry.svg != nil {
			t.Fatalf("含 foreignObject 的 SVG 应记为 failed 且 SVG 为 nil，实际 status=%v svg=%v", entry.status, entry.svg != nil)
		}
	})
}

func TestMermaidInlineThemeKeySeparate(t *testing.T) {
	var mu sync.Mutex
	var seen []bool
	setupMermaidInlineTest(t, func(code string, dark bool) (string, error) {
		mu.Lock()
		seen = append(seen, dark)
		mu.Unlock()
		return minimalMermaidSVG, nil
	})

	code := "graph LR\nLight-->Dark\n"
	mermaidInlineRenderer.lookupOrStart(ui.Services{}, nil, code, false)
	mermaidInlineRenderer.lookupOrStart(ui.Services{}, nil, code, true)

	light := waitMermaidEntryDone(t, code, false)
	dark := waitMermaidEntryDone(t, code, true)
	if light.status != mermaidStatusDone || dark.status != mermaidStatusDone {
		t.Fatalf("深浅色条目均应为 done，实际 light=%v dark=%v", light.status, dark.status)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("深浅色不同 key 应各请求一次，实际请求 %d 次 (%v)", len(seen), seen)
	}
}

func TestMermaidInlineHeadlessRenderDoneAndFailed(t *testing.T) {
	t.Run("done shows image and toggles source", func(t *testing.T) {
		prevDark := themeDark.Load()
		themeDark.Store(false)
		t.Cleanup(func() { themeDark.Store(prevDark) })

		setupMermaidInlineTest(t, func(code string, dark bool) (string, error) {
			return minimalMermaidSVG, nil
		})

		s := sidePaneTestState(t)
		defer s.close()

		source := "```mermaid\ngraph TD\nA-->B\n"
		tester := ui.NewTester(func(c *ui.Context) {
			markdown.View(c, source, &s.mdOpts)
		}, 600, 400)

		if _, ok := tester.AccessByRole(ui.RoleImage, "Mermaid 图表"); ok {
			t.Fatal("流式未闭合时不应该渲染内联图表")
		}
		if tester.HasText("预览") {
			t.Fatal("流式未闭合时不应该显示预览按钮")
		}

		source += "```\n"
		tester.Frame()
		waitMermaidEntryDone(t, "graph TD\nA-->B\n", false)
		tester.Frame()

		if _, ok := tester.AccessByRole(ui.RoleImage, "Mermaid 图表"); !ok {
			t.Fatal("done 后组件树中应存在内联 Mermaid 图表 Image")
		}
		if !tester.HasText("源码") || !tester.HasText("预览") {
			t.Fatalf("done 后应保留源码与预览按钮，当前文本: %v", tester.Texts())
		}

		if err := tester.Click("源码"); err != nil {
			t.Fatalf("点击源码按钮失败: %v", err)
		}
		if _, ok := tester.AccessByRole(ui.RoleImage, "Mermaid 图表"); ok {
			t.Fatal("切换到源码后不应再渲染图表 Image")
		}
		if !tester.HasText("图表") || !tester.HasText("A-->B") {
			t.Fatalf("切换到源码后应显示原代码与切回图表按钮，当前文本: %v", tester.Texts())
		}

		if err := tester.Click("图表"); err != nil {
			t.Fatalf("点击图表按钮失败: %v", err)
		}
		if _, ok := tester.AccessByRole(ui.RoleImage, "Mermaid 图表"); !ok {
			t.Fatal("切回图表后应重新显示 Image")
		}

		tester.Advance(time.Second)
		if tester.FramePending() {
			t.Fatal("渲染稳定后推进时钟不应继续请求下一帧")
		}
	})

	t.Run("failed falls back to source and preview button", func(t *testing.T) {
		prevDark := themeDark.Load()
		themeDark.Store(false)
		t.Cleanup(func() { themeDark.Store(prevDark) })

		setupMermaidInlineTest(t, func(code string, dark bool) (string, error) {
			return "", errors.New("render failed")
		})

		s := sidePaneTestState(t)
		defer s.close()

		source := "```mermaid\ngraph TD\nFail-->Source\n```\n"
		tester := ui.NewTester(func(c *ui.Context) {
			markdown.View(c, source, &s.mdOpts)
		}, 600, 400)

		waitMermaidEntryDone(t, "graph TD\nFail-->Source\n", false)
		tester.Frame()

		if _, ok := tester.AccessByRole(ui.RoleImage, "Mermaid 图表"); ok {
			t.Fatal("failed 时不应渲染图表 Image")
		}
		if !tester.HasText("预览") {
			t.Fatal("failed 时应回到预览按钮")
		}
		if tester.HasText("源码") {
			t.Fatal("failed 时不应显示源码切换按钮")
		}
		if !tester.HasText("Fail-->Source") {
			t.Fatalf("failed 时应显示原代码块，当前文本: %v", tester.Texts())
		}

		tester.Advance(time.Second)
		if tester.FramePending() {
			t.Fatal("回退态稳定后推进时钟不应继续请求下一帧")
		}
	})
}

func waitMermaidIdle(t *testing.T, expectedEntriesCheck func(entryCount, orderCount int) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mermaidInlineRenderer.mu.Lock()
		idle := !mermaidInlineRenderer.running && len(mermaidInlineRenderer.queue) == 0
		entryCount := len(mermaidInlineRenderer.entries)
		orderCount := len(mermaidInlineRenderer.order)
		mermaidInlineRenderer.mu.Unlock()
		if idle && (expectedEntriesCheck == nil || expectedEntriesCheck(entryCount, orderCount)) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("等待 mermaid 渲染队列空闲超时")
}

func TestMermaidInlineIdleRelease(t *testing.T) {
	const (
		code1       = "graph TD\nIdle1-->Done\n"
		code2       = "graph TD\nIdle2-->Arm\n"
		blockedCode = "graph TD\nIdle3-->Hold\n"
	)

	started := make(chan struct{})
	unblock := make(chan struct{})
	var startedOnce sync.Once
	var unblockOnce sync.Once
	closeUnblock := func() {
		unblockOnce.Do(func() { close(unblock) })
	}

	setupMermaidInlineTest(t, func(code string, dark bool) (string, error) {
		if code == blockedCode {
			startedOnce.Do(func() { close(started) })
			<-unblock
		}
		return minimalMermaidSVG, nil
	})

	prevIdle := mermaidInlineIdleRelease
	prevRelease := mermaidReleaseIdleWebView
	var releaseCalls atomic.Int32
	releaseCh := make(chan struct{}, 8)
	mermaidInlineIdleRelease = 50 * time.Millisecond
	mermaidReleaseIdleWebView = func() {
		releaseCalls.Add(1)
		prevRelease()
		releaseCh <- struct{}{}
	}
	t.Cleanup(func() {
		closeUnblock()
		mermaidInlineRenderer.resetForTest()
		mermaidInlineIdleRelease = prevIdle
		mermaidReleaseIdleWebView = prevRelease
	})

	mermaidInlineRenderer.lookupOrStart(ui.Services{}, nil, code1, false)
	waitMermaidEntryDone(t, code1, false)

	select {
	case <-releaseCh:
	case <-time.After(2 * time.Second):
		t.Fatal("等待第一次空闲回收超时")
	}
	if got := releaseCalls.Load(); got != 1 {
		t.Fatalf("一次渲染空闲后应回收恰好 1 次，实际 %d 次", got)
	}

	// 再次渲染完成后等待定时器挂起，在到期前入队阻塞任务，验证运行期间不触发回收。
	mermaidInlineRenderer.lookupOrStart(ui.Services{}, nil, code2, false)
	deadline := time.Now().Add(2 * time.Second)
	armed := false
	for time.Now().Before(deadline) {
		mermaidInlineRenderer.mu.Lock()
		armed = !mermaidInlineRenderer.running && mermaidInlineRenderer.idleTimer != nil
		mermaidInlineRenderer.mu.Unlock()
		if armed {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !armed {
		t.Fatal("等待空闲回收定时器挂载超时")
	}

	mermaidInlineRenderer.lookupOrStart(ui.Services{}, nil, blockedCode, false)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("等待新任务开始执行超时")
	}

	select {
	case <-releaseCh:
		t.Fatalf("定时器到点前入队的新任务运行期间不应触发回收，当前次数 %d", releaseCalls.Load())
	case <-time.After(90 * time.Millisecond):
	}
	if got := releaseCalls.Load(); got != 1 {
		t.Fatalf("新任务运行期间回收次数应保持为 1，实际 %d", got)
	}

	closeUnblock()
	waitMermaidEntryDone(t, blockedCode, false)

	select {
	case <-releaseCh:
	case <-time.After(2 * time.Second):
		t.Fatal("等待新任务结束后的空闲回收超时")
	}
	if got := releaseCalls.Load(); got != 2 {
		t.Fatalf("新任务空闲后应再回收 1 次（共 2 次），实际 %d 次", got)
	}
}

func TestMermaidInlineCacheBound(t *testing.T) {
	t.Run("evicts oldest beyond cache max", func(t *testing.T) {
		const total = mermaidInlineCacheMax + 10
		var rendered atomic.Int32
		setupMermaidInlineTest(t, func(code string, dark bool) (string, error) {
			rendered.Add(1)
			return minimalMermaidSVG, nil
		})

		var oldestCode, latestCode string
		for i := 0; i < total; i++ {
			code := fmt.Sprintf("graph TD\nN%d-->M%d\n", i, i)
			if i == 0 {
				oldestCode = code
			}
			latestCode = code
			mermaidInlineRenderer.lookupOrStart(ui.Services{}, nil, code, false)
		}

		waitMermaidIdle(t, func(entryCount, orderCount int) bool {
			return rendered.Load() == total
		})

		mermaidInlineRenderer.mu.Lock()
		entryCount := len(mermaidInlineRenderer.entries)
		orderCount := len(mermaidInlineRenderer.order)
		mermaidInlineRenderer.mu.Unlock()

		if entryCount > mermaidInlineCacheMax {
			t.Fatalf("entries 数量应不超过上限 %d，实际 %d", mermaidInlineCacheMax, entryCount)
		}
		if orderCount != entryCount {
			t.Fatalf("order 长度应与 entries 数量一致，entries=%d order=%d", entryCount, orderCount)
		}
		latest, ok := mermaidInlineRenderer.getEntry(latestCode, false)
		if !ok || latest.status != mermaidStatusDone || latest.svg == nil {
			t.Fatalf("最新一段代码应保留在缓存中且为 done，实际 ok=%v status=%v", ok, latest.status)
		}
		if _, ok := mermaidInlineRenderer.getEntry(oldestCode, false); ok {
			t.Fatal("最旧一段代码应已被淘汰")
		}
	})

	t.Run("skips pending entry during eviction", func(t *testing.T) {
		const pendingCode = "graph TD\nPending-->Keep\n"
		const extra = mermaidInlineCacheMax + 2

		started := make(chan struct{})
		unblock := make(chan struct{})
		var startedOnce sync.Once
		var unblockOnce sync.Once
		closeUnblock := func() {
			unblockOnce.Do(func() { close(unblock) })
		}
		var rendered atomic.Int32
		setupMermaidInlineTest(t, func(code string, dark bool) (string, error) {
			if code == pendingCode {
				startedOnce.Do(func() { close(started) })
				<-unblock
			}
			rendered.Add(1)
			return minimalMermaidSVG, nil
		})
		t.Cleanup(closeUnblock)

		mermaidInlineRenderer.lookupOrStart(ui.Services{}, nil, pendingCode, false)
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("等待 pending 任务开始执行超时")
		}

		var latestCode string
		for i := 0; i < extra; i++ {
			latestCode = fmt.Sprintf("graph TD\nP%d-->Q%d\n", i, i)
			mermaidInlineRenderer.lookupOrStart(ui.Services{}, nil, latestCode, false)
		}

		pendingEntry, ok := mermaidInlineRenderer.getEntry(pendingCode, false)
		if !ok || pendingEntry.status != mermaidStatusPending {
			t.Fatalf("pending 状态的条目在淘汰时应被跳过保留，实际 ok=%v status=%v", ok, pendingEntry.status)
		}

		closeUnblock()
		waitMermaidIdle(t, func(entryCount, orderCount int) bool {
			return rendered.Load() == extra+1
		})

		mermaidInlineRenderer.mu.Lock()
		entryCount := len(mermaidInlineRenderer.entries)
		orderCount := len(mermaidInlineRenderer.order)
		mermaidInlineRenderer.mu.Unlock()

		if entryCount > mermaidInlineCacheMax {
			t.Fatalf("全部完成后 entries 数量应不超过上限 %d，实际 %d", mermaidInlineCacheMax, entryCount)
		}
		if orderCount != entryCount {
			t.Fatalf("order 长度应与 entries 数量一致，entries=%d order=%d", entryCount, orderCount)
		}
		if got := rendered.Load(); got != extra+1 {
			t.Fatalf("pending 条目完成后不应重复入队渲染，期望总渲染 %d 次，实际 %d 次", extra+1, got)
		}
		latest, ok := mermaidInlineRenderer.getEntry(latestCode, false)
		if !ok || latest.status != mermaidStatusDone {
			t.Fatalf("最新条目应保留在缓存中且为 done，实际 ok=%v status=%v", ok, latest.status)
		}
	})
}
