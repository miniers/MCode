package mcodeui

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"mcode/internal/swarm"
)

type designPickTestPage struct {
	mu       sync.Mutex
	evals    []string
	evalFn   func(string) (any, error)
	navigate func(string)
	finish   func()
	title    func(*mygo.TitleEvent)
	url      string
}

func (p *designPickTestPage) Eval(script string) (any, error) {
	p.mu.Lock()
	p.evals = append(p.evals, script)
	fn := p.evalFn
	p.mu.Unlock()
	if fn != nil {
		return fn(script)
	}
	return nil, nil
}

func (p *designPickTestPage) Destroy() {}
func (p *designPickTestPage) URL() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.url
}
func (p *designPickTestPage) CanGoBack() bool    { return false }
func (p *designPickTestPage) CanGoForward() bool { return false }
func (p *designPickTestPage) GoBack()            {}
func (p *designPickTestPage) GoForward()         {}
func (p *designPickTestPage) Reload()            {}
func (p *designPickTestPage) Stop()              {}
func (p *designPickTestPage) IsLoading() bool    { return false }
func (p *designPickTestPage) OnDidNavigate(fn func(string)) func() {
	p.mu.Lock()
	p.navigate = fn
	p.mu.Unlock()
	return func() {}
}
func (p *designPickTestPage) OnDidFinishLoad(fn func()) func() {
	p.mu.Lock()
	p.finish = fn
	p.mu.Unlock()
	return func() {}
}
func (p *designPickTestPage) OnPageTitleUpdated(fn func(*mygo.TitleEvent)) func() {
	p.mu.Lock()
	p.title = fn
	p.mu.Unlock()
	return func() {}
}

func (p *designPickTestPage) evalScripts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.evals...)
}

type designPickFinalizeCall struct {
	LeadID  string
	BeeID   string
	Choices []swarm.DesignChoice
	Note    string
}

type designPickTestClient struct {
	*inspectorContextClient
	mu    sync.Mutex
	calls []designPickFinalizeCall
	err   error
}

func (c *designPickTestClient) FinalizeDesignBee(ctx context.Context, leadThreadID, beeThreadID string, choices []swarm.DesignChoice, note string) error {
	c.mu.Lock()
	c.calls = append(c.calls, designPickFinalizeCall{
		LeadID:  leadThreadID,
		BeeID:   beeThreadID,
		Choices: append([]swarm.DesignChoice(nil), choices...),
		Note:    note,
	})
	forcedErr := c.err
	c.mu.Unlock()
	if forcedErr != nil {
		return forcedErr
	}
	return c.inspectorContextClient.FinalizeDesignBee(ctx, leadThreadID, beeThreadID, choices, note)
}

func setupPreviewDesignPickState(t *testing.T, beeStatus string, awaitingFinalize, finalizeRequested bool) (*chatState, *designPickTestClient, *browserTab) {
	t.Helper()
	const leadID = "t1"
	const beeID = "bee-1"

	s, baseClient := inspectorContextSetup(t)
	client := &designPickTestClient{inspectorContextClient: baseClient}
	s.mu.Lock()
	s.client = client
	s.threads = append(s.threads, Thread{ID: beeID, ProjectID: "p1", ParentThreadID: leadID})
	s.mu.Unlock()

	st := SwarmState{
		LeadThreadID: leadID,
		Available:    true,
		Enabled:      true,
		Bees: []SwarmBee{{
			ThreadID:          beeID,
			Name:              "DesignBee",
			Status:            beeStatus,
			AwaitingFinalize:  awaitingFinalize,
			FinalizeRequested: finalizeRequested,
		}},
	}
	client.SetSwarmState(leadID, st)
	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	hp.loadedFor = leadID
	hp.setStateLocked(st)
	swarmPanelMu.Unlock()

	tab := getPreviewState(s)
	t.Cleanup(func() {
		previewMu.Lock()
		delete(previewFinalizing, tab)
		previewMu.Unlock()
		browserReleaseView(tab)
		swarmPanelMu.Lock()
		delete(swarmPanelStates, s)
		swarmPanelMu.Unlock()
		swarmFloatMu.Lock()
		delete(swarmFloatPanelDismissed, leadID)
		delete(swarmFloatDismissedBees, leadID)
		delete(swarmSelectedBeeID, leadID)
		delete(swarmBeeAutoHideHandled, leadID)
		delete(swarmPulseTime, leadID+"|"+beeID)
		swarmFloatMu.Unlock()
	})

	previewMu.Lock()
	tab.visible = true
	tab.localPreview = true
	tab.previewThreadID = beeID
	previewMu.Unlock()
	return s, client, tab
}

func TestPreviewDesignPickPreloadScript(t *testing.T) {
	opts := previewWebViewOptions(true, false)
	if !strings.Contains(opts.Page.PreloadScript, "__mcodePick") {
		t.Fatalf("PreloadScript 未拼接 designpick.Script")
	}
}

func TestPreviewDesignPickStateJSONParse(t *testing.T) {
	validJSON := `{
		"version": 2,
		"changed": 1,
		"locked": true,
		"choices": [
			{"question": "列表每项显示几行字？", "option": "B", "recommended": false, "options": ["A", "B", "C"]},
			{"question": "侧栏放在哪一边？", "option": "A", "recommended": true, "options": ["A", "B"]}
		]
	}`
	st, ok := previewDesignPickFromJSON(validJSON)
	if !ok {
		t.Fatal("合法 state JSON 解析失败")
	}
	if st.Version != 2 || st.Changed != 1 || !st.Locked || len(st.Choices) != 2 {
		t.Fatalf("解析字段不匹配: %+v", st)
	}
	if st.Choices[0].Question != "列表每项显示几行字？" || st.Choices[0].Option != "B" || st.Choices[0].Recommended {
		t.Fatalf("第 1 题字段错误: %+v", st.Choices[0])
	}
	if !st.Choices[1].Recommended || len(st.Choices[1].Options) != 2 {
		t.Fatalf("第 2 题字段错误: %+v", st.Choices[1])
	}

	choices := previewDesignChoicesFromState(st)
	if len(choices) != 2 || choices[0].Option != "B" || choices[0].Recommended || !choices[1].Recommended {
		t.Fatalf("转换 DesignChoice 失败: %+v", choices)
	}

	// 负 changed 钳位到 0，且空 question/option 在转换时过滤。
	neg, ok := previewDesignPickFromJSON(`{"version":0,"changed":-3,"choices":[{"question":"","option":"A"},{"question":"Q","option":"A","recommended":true}]}`)
	if !ok || neg.Changed != 0 {
		t.Fatalf("负 changed 未钳位到 0: ok=%v st=%+v", ok, neg)
	}
	if conv := previewDesignChoicesFromState(neg); len(conv) != 1 || conv[0].Question != "Q" {
		t.Fatalf("空问题未过滤: %+v", conv)
	}

	invalidCases := []string{"", "   ", "null", "{bad", "[]", "123", `"str"`}
	for _, raw := range invalidCases {
		if got, ok := previewDesignPickFromJSON(raw); ok || len(got.Choices) != 0 {
			t.Fatalf("非法或空输入 %q 不应解析成功: ok=%v st=%+v", raw, ok, got)
		}
	}
	if _, ok := previewParseDesignPickState(nil); ok {
		t.Fatal("nil 不应解析成功")
	}
}

func TestPreviewDesignPickLoadAndPolling(t *testing.T) {
	setAppReduceMotion(t, true)
	origPoll := previewConsolePollInterval
	origPickPoll := previewDesignPickPollInterval
	previewConsolePollInterval = 15 * time.Millisecond
	previewDesignPickPollInterval = 400 * time.Millisecond
	t.Cleanup(func() {
		previewConsolePollInterval = origPoll
		previewDesignPickPollInterval = origPickPoll
	})

	s, _, tab := setupPreviewDesignPickState(t, "completed", true, false)

	var stateVersion atomic.Int32
	page := &designPickTestPage{
		url: "http://127.0.0.1:9999/design.html",
		evalFn: func(script string) (any, error) {
			if strings.Contains(script, "window.__mcodePick.state()") {
				v := stateVersion.Load()
				changed := 0
				opt := "A"
				if v > 0 {
					changed = 1
					opt = "B"
				}
				return jsonMarshalMust(map[string]any{
					"version": v,
					"changed": changed,
					"choices": []map[string]any{
						{"question": "配色风格？", "option": opt, "recommended": v == 0, "options": []string{"A", "B"}},
					},
				}), nil
			}
			return nil, nil
		},
	}

	previewMu.Lock()
	browserPromoteTabLocked(tab)
	previewNextPageID++
	tab.page = page
	tab.pageID = previewNextPageID
	tab.url = page.url
	token := previewPageTokenLocked(tab)
	previewMu.Unlock()
	previewBindPageCallbacks(s, token, page)

	// 触发页面加载完成：应调用 setReduceMotion(true) 并读取初始 state。
	page.finish()
	waitDesignPickUntil(t, 2*time.Second, func() bool {
		previewMu.Lock()
		defer previewMu.Unlock()
		return tab.designPickValid && !tab.designPickPolling
	})

	scripts := page.evalScripts()
	hasReduceMotion := false
	hasState := false
	for _, sc := range scripts {
		if strings.Contains(sc, "setReduceMotion(true)") {
			hasReduceMotion = true
		}
		if strings.Contains(sc, "window.__mcodePick.state()") {
			hasState = true
		}
	}
	if !hasReduceMotion || !hasState {
		t.Fatalf("加载完成后未调用 setReduceMotion(true) 或 state(): %v", scripts)
	}

	// 验证轮询条件：仅标签可见、bee 待定稿且页面有题时轮询。
	view := swarmStateViewFor(s)
	previewMu.Lock()
	if !previewTabNeedsDesignPickPollingLocked(tab, view) {
		previewMu.Unlock()
		t.Fatal("可见、待定稿且有题时应允许轮询")
	}
	tab.visible = false
	if previewTabNeedsDesignPickPollingLocked(tab, view) {
		previewMu.Unlock()
		t.Fatal("标签不可见时不应轮询")
	}
	tab.visible = true
	previewMu.Unlock()

	// 允许快速测试轮询间隔并验证 version 不变时不 invalidate。
	previewDesignPickPollInterval = 15 * time.Millisecond
	browserStartPolling(s, tab)
	beforeInv := s.invalidateGen.Load()
	time.Sleep(60 * time.Millisecond)
	afterSameVer := s.invalidateGen.Load()
	if afterSameVer != beforeInv {
		t.Fatalf("version 未变化不应 invalidate: before=%d after=%d", beforeInv, afterSameVer)
	}

	// version 变化时应更新并 invalidate。
	stateVersion.Store(1)
	waitDesignPickUntil(t, 2*time.Second, func() bool {
		previewMu.Lock()
		defer previewMu.Unlock()
		return tab.designPick.Version == 1 && s.invalidateGen.Load() > afterSameVer
	})

	// 已定稿页面刷新后应重新调用 setLocked(true)。
	s2, _, tab2 := setupPreviewDesignPickState(t, "completed", false, true)
	page2 := &designPickTestPage{
		url: "http://127.0.0.1:9999/design.html",
		evalFn: func(script string) (any, error) {
			if strings.Contains(script, "window.__mcodePick.state()") {
				return `{"version":0,"changed":0,"locked":true,"choices":[{"question":"Q","option":"A","recommended":true,"options":["A","B"]}]}`, nil
			}
			return nil, nil
		},
	}
	previewMu.Lock()
	browserPromoteTabLocked(tab2)
	previewNextPageID++
	tab2.page = page2
	tab2.pageID = previewNextPageID
	token2 := previewPageTokenLocked(tab2)
	previewMu.Unlock()
	previewBindPageCallbacks(s2, token2, page2)
	page2.finish()
	waitDesignPickUntil(t, 2*time.Second, func() bool {
		for _, sc := range page2.evalScripts() {
			if strings.Contains(sc, "setLocked(true)") {
				return true
			}
		}
		return false
	})
}

func TestPreviewDesignPickCapsuleAndPopover(t *testing.T) {
	setAppReduceMotion(t, true)
	s, _, tab := setupPreviewDesignPickState(t, "completed", true, false)

	var currentOpt atomic.Value
	currentOpt.Store("B")
	var currentVer atomic.Int32
	currentVer.Store(1)
	var mu sync.Mutex
	var evalLog []string

	specialQ := `标题含"引号"与<标签>`
	page := &designPickTestPage{
		evalFn: func(script string) (any, error) {
			mu.Lock()
			evalLog = append(evalLog, script)
			mu.Unlock()
			if strings.Contains(script, "select") {
				currentOpt.Store("A")
				currentVer.Store(2)
				return nil, nil
			}
			if strings.Contains(script, "window.__mcodePick.state()") {
				opt := currentOpt.Load().(string)
				ver := currentVer.Load()
				changed := 0
				if opt != "A" {
					changed = 1
				}
				return jsonMarshalMust(map[string]any{
					"version": ver,
					"changed": changed,
					"choices": []map[string]any{
						{"question": specialQ, "option": opt, "recommended": opt == "A", "options": []string{"A", "B"}},
					},
				}), nil
			}
			return nil, nil
		},
	}

	previewMu.Lock()
	tab.page = page
	tab.designPickValid = true
	tab.designPick = previewDesignPickState{
		Version: 1,
		Changed: 1,
		Choices: []previewDesignPickChoice{
			{Question: specialQ, Option: "B", Recommended: false, Options: []string{"A", "B"}},
		},
	}
	previewMu.Unlock()

	tester := ui.NewTesterWith(func(c *ui.Context) { renderPreviewPanel(c, s) }, 800, 600, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	tester.Frame()
	tester.Advance(time.Second)

	trigger, ok := tester.AccessByRole(ui.RoleButton, "已改 1/1")
	if !ok {
		t.Fatalf("未找到进度胶囊「已改 1/1」，texts=%q", tester.Texts())
	}
	finalizeBtn, ok := tester.AccessByRole(ui.RoleButton, "定稿")
	if !ok || trigger.Bounds.X >= finalizeBtn.Bounds.X {
		t.Fatalf("进度胶囊应在「定稿」左侧: trigger=%+v finalize=%+v", trigger.Bounds, finalizeBtn.Bounds)
	}

	// 点击胶囊展开向上一览浮层。
	tester.ClickAt(trigger.Bounds.X+trigger.Bounds.W/2, trigger.Bounds.Y+trigger.Bounds.H/2)
	tester.Frame()
	tester.Advance(time.Second)

	if !tester.HasText("方案选择（已改 1/1）") {
		t.Fatalf("未展开方案一览浮层，texts=%q", tester.Texts())
	}

	// 点击问题文字 -> scrollTo（参数应通过 json.Marshal 安全转义）。
	qBtn, ok := tester.AccessByRole(ui.RoleButton, "1. "+specialQ)
	if !ok {
		t.Fatalf("未找到问题行按钮，texts=%q", tester.Texts())
	}
	tester.ClickAt(qBtn.Bounds.X+qBtn.Bounds.W/2, qBtn.Bounds.Y+qBtn.Bounds.H/2)
	expectedQArg := jsonMarshalMust(specialQ)
	waitDesignPickUntil(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, sc := range evalLog {
			if strings.Contains(sc, "scrollTo") && strings.Contains(sc, expectedQArg) {
				return true
			}
		}
		return false
	})

	// 点击选项分段按钮 "A" -> select 后立即重读 state 并更新 UI。
	optBtn, ok := tester.AccessByRole(ui.RoleRadio, "A")
	if !ok {
		optBtn, ok = tester.AccessByRole(ui.RoleButton, "A")
	}
	if !ok {
		t.Fatalf("未找到选项 A 分段按钮，texts=%q", tester.Texts())
	}
	tester.ClickAt(optBtn.Bounds.X+optBtn.Bounds.W/2, optBtn.Bounds.Y+optBtn.Bounds.H/2)

	waitDesignPickUntil(t, 2*time.Second, func() bool {
		previewMu.Lock()
		defer previewMu.Unlock()
		return tab.designPick.Version == 2 && len(tab.designPick.Choices) == 1 && tab.designPick.Choices[0].Option == "A"
	})

	tester.Frame()
	tester.Advance(time.Second)
	if !tester.HasText("1 项推荐") {
		t.Fatalf("改回推荐方案后胶囊应显示「1 项推荐」，texts=%q", tester.Texts())
	}
}

func TestPreviewDesignPickFinalizeChoicesAndNote(t *testing.T) {
	t.Run("success_sends_choices_and_note_then_clears", func(t *testing.T) {
		setAppReduceMotion(t, true)
		s, client, tab := setupPreviewDesignPickState(t, "completed", true, false)

		origTag := previewAnnotationTag
		previewAnnotationTag = func() string { return "fixedtag" }
		t.Cleanup(func() { previewAnnotationTag = origTag })

		page := &designPickTestPage{
			evalFn: func(script string) (any, error) {
				if strings.Contains(script, "window.__mcodePick.state()") {
					return `{
						"version": 3,
						"changed": 1,
						"choices": [
							{"question": "主色调", "option": "B", "recommended": false, "options": ["A", "B"]},
							{"question": "密度", "option": "A", "recommended": true, "options": ["A", "B"]}
						]
					}`, nil
				}
				return nil, nil
			},
		}

		previewMu.Lock()
		tab.page = page
		tab.feedbackText = "把主按钮再大一点"
		tab.annotations = []previewAnnotation{
			{id: 1, PageID: 1, Kind: "element", Selector: "#cta", Tag: "button", Comment: "圆角再大些", X: 10, Y: 20, W: 80, H: 32},
		}
		token := previewPageTokenLocked(tab)
		previewMu.Unlock()

		previewFinalizeFeedback(s, token)
		waitDesignPickUntil(t, 2*time.Second, func() bool {
			client.mu.Lock()
			n := len(client.calls)
			client.mu.Unlock()
			previewMu.Lock()
			inFlight := previewFinalizing[tab]
			previewMu.Unlock()
			return n == 1 && !inFlight
		})

		client.mu.Lock()
		call := client.calls[0]
		client.mu.Unlock()
		if call.LeadID != "t1" || call.BeeID != "bee-1" {
			t.Fatalf("目标会话不匹配: %+v", call)
		}
		if len(call.Choices) != 2 || call.Choices[0].Question != "主色调" || call.Choices[0].Option != "B" || call.Choices[0].Recommended {
			t.Fatalf("第 1 题 choices 不匹配: %+v", call.Choices)
		}
		if call.Choices[1].Question != "密度" || call.Choices[1].Option != "A" || !call.Choices[1].Recommended {
			t.Fatalf("第 2 题 choices 不匹配: %+v", call.Choices[1])
		}
		if !strings.Contains(call.Note, "把主按钮再大一点") || !strings.Contains(call.Note, "selector=#cta") || !strings.Contains(call.Note, "圆角再大些") {
			t.Fatalf("note 未同时包含输入框文字与标注: %q", call.Note)
		}

		previewMu.Lock()
		remText := tab.feedbackText
		remAnnot := len(tab.annotations)
		previewMu.Unlock()
		if remText != "" || remAnnot != 0 {
			t.Fatalf("定稿成功后应清空输入框与标注: text=%q annots=%d", remText, remAnnot)
		}

		var locked, cleared bool
		for _, sc := range page.evalScripts() {
			if strings.Contains(sc, "setLocked(true)") {
				locked = true
			}
			if strings.Contains(sc, "clearMarks") {
				cleared = true
			}
		}
		if !locked || !cleared {
			t.Fatalf("定稿成功后应锁定页面选项并清空页面标注: locked=%v cleared=%v scripts=%v", locked, cleared, page.evalScripts())
		}
	})

	t.Run("failure_preserves_input_and_annotations", func(t *testing.T) {
		setAppReduceMotion(t, true)
		s, client, tab := setupPreviewDesignPickState(t, "completed", true, false)
		client.err = errors.New("network error")

		page := &designPickTestPage{
			evalFn: func(script string) (any, error) {
				if strings.Contains(script, "window.__mcodePick.state()") {
					return `{"version":0,"changed":0,"choices":[{"question":"Q","option":"A","recommended":true,"options":["A","B"]}]}`, nil
				}
				return nil, nil
			},
		}

		previewMu.Lock()
		tab.page = page
		tab.feedbackText = "失败后保留的意见"
		tab.annotations = []previewAnnotation{{id: 1, PageID: 1, Selector: "#card", Comment: "保留标注"}}
		token := previewPageTokenLocked(tab)
		previewMu.Unlock()

		previewFinalizeFeedback(s, token)
		waitDesignPickUntil(t, 2*time.Second, func() bool {
			client.mu.Lock()
			n := len(client.calls)
			client.mu.Unlock()
			previewMu.Lock()
			inFlight := previewFinalizing[tab]
			previewMu.Unlock()
			return n == 1 && !inFlight
		})

		previewMu.Lock()
		remText := tab.feedbackText
		remAnnot := len(tab.annotations)
		previewMu.Unlock()
		if remText != "失败后保留的意见" || remAnnot != 1 {
			t.Fatalf("定稿失败后应保留输入框与标注: text=%q annots=%d", remText, remAnnot)
		}
		for _, sc := range page.evalScripts() {
			if strings.Contains(sc, "setLocked(true)") {
				t.Fatal("定稿失败不应锁定页面选项")
			}
		}
	})
}

func TestPreviewDesignPickFinalizeStatesAndMotion(t *testing.T) {
	t.Run("state_detection_and_render", func(t *testing.T) {
		setAppReduceMotion(t, true)

		// 1. 待定稿：显示进度胶囊和「定稿」按钮。
		s1, _, tab1 := setupPreviewDesignPickState(t, "completed", true, false)
		if got := previewFeedbackFinalizeState(s1, "bee-1"); got != previewFinalizeAwaiting {
			t.Fatalf("state = %v, want previewFinalizeAwaiting", got)
		}
		previewMu.Lock()
		tab1.designPick = previewDesignPickState{
			Choices: []previewDesignPickChoice{{Question: "Q", Option: "A", Recommended: true, Options: []string{"A", "B"}}},
		}
		previewMu.Unlock()
		tt1 := ui.NewTesterWith(func(c *ui.Context) { renderPreviewPanel(c, s1) }, 800, 600, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
		tt1.Frame()
		tt1.Advance(time.Second)
		if !tt1.HasText("1 项推荐") || !tt1.HasText("定稿") {
			t.Fatalf("待定稿状态应显示进度胶囊和「定稿」，texts=%q", tt1.Texts())
		}

		// 2. 定稿中：finalized && running -> 显示不可点击「定稿中」，隐藏进度胶囊。
		s2, _, tab2 := setupPreviewDesignPickState(t, "running", false, true)
		if got := previewFeedbackFinalizeState(s2, "bee-1"); got != previewFinalizeRunning {
			t.Fatalf("state = %v, want previewFinalizeRunning", got)
		}
		previewMu.Lock()
		tab2.designPick = previewDesignPickState{
			Choices: []previewDesignPickChoice{{Question: "Q", Option: "A", Recommended: true, Options: []string{"A", "B"}}},
		}
		previewMu.Unlock()
		tt2 := ui.NewTesterWith(func(c *ui.Context) { renderPreviewPanel(c, s2) }, 800, 600, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
		tt2.Frame()
		tt2.Advance(time.Second)
		btn2, ok := tt2.AccessByRole(ui.RoleButton, "定稿中")
		if !ok || !btn2.Disabled {
			t.Fatalf("定稿中应显示禁用加载按钮「定稿中」: ok=%v disabled=%v", ok, btn2.Disabled)
		}
		if tt2.HasText("1 项推荐") || tt2.HasText("已定稿") {
			t.Fatalf("定稿中不应显示进度胶囊或「已定稿」，texts=%q", tt2.Texts())
		}

		// 3. 已定稿：finalized && !running -> 显示不可点击「已定稿」标记，隐藏进度胶囊。
		s3, _, tab3 := setupPreviewDesignPickState(t, "completed", false, true)
		if got := previewFeedbackFinalizeState(s3, "bee-1"); got != previewFinalizeDone {
			t.Fatalf("state = %v, want previewFinalizeDone", got)
		}
		previewMu.Lock()
		tab3.designPick = previewDesignPickState{
			Choices: []previewDesignPickChoice{{Question: "Q", Option: "A", Recommended: true, Options: []string{"A", "B"}}},
		}
		previewMu.Unlock()
		tt3 := ui.NewTesterWith(func(c *ui.Context) { renderPreviewPanel(c, s3) }, 800, 600, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
		tt3.Frame()
		tt3.Advance(time.Second)
		if !tt3.HasText("已定稿") {
			t.Fatalf("已定稿状态应显示「已定稿」，texts=%q", tt3.Texts())
		}
		if _, ok := tt3.AccessByRole(ui.RoleButton, "已定稿"); ok {
			t.Fatal("「已定稿」状态标记不应为可点击按钮")
		}
		if _, ok := tt3.AccessByRole(ui.RoleButton, "定稿"); ok || tt3.HasText("1 项推荐") || slices.Contains(tt3.Texts(), "定稿") {
			t.Fatalf("已定稿状态不应显示进度胶囊或「定稿」，texts=%q", tt3.Texts())
		}
	})

	t.Run("animations_settle_and_respect_reduce_motion", func(t *testing.T) {
		for _, reduced := range []bool{false, true} {
			setAppReduceMotion(t, reduced)
			s, client, tab := setupPreviewDesignPickState(t, "completed", true, false)
			previewMu.Lock()
			tab.designPick = previewDesignPickState{
				Choices: []previewDesignPickChoice{{Question: "Q", Option: "A", Recommended: true, Options: []string{"A", "B"}}},
			}
			previewMu.Unlock()

			tt := ui.NewTesterWith(func(c *ui.Context) { renderPreviewPanel(c, s) }, 800, 600, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: reduced}})
			tt.Frame()
			if reduced && tt.FramePending() {
				t.Fatal("ReduceMotion 开启时首帧不应请求下一帧")
			}
			tt.Advance(time.Second)
			if tt.FramePending() {
				t.Fatal("进度胶囊进场动画结束后不应继续请求下一帧")
			}

			// 展开一览浮层再关闭，动画结束后应静止。
			trigger, ok := tt.AccessByRole(ui.RoleButton, "1 项推荐")
			if !ok {
				t.Fatal("未找到进度胶囊")
			}
			tt.ClickAt(trigger.Bounds.X+trigger.Bounds.W/2, trigger.Bounds.Y+trigger.Bounds.H/2)
			tt.Frame()
			if reduced && tt.FramePending() {
				t.Fatal("ReduceMotion 开启时弹出一览不应请求下一帧")
			}
			tt.Advance(time.Second)
			if tt.FramePending() {
				t.Fatal("一览弹出动画结束后不应继续请求下一帧")
			}

			// 切换到已定稿状态，「已定稿」徽标入场动画结束后应静止。
			doneState := SwarmState{
				LeadThreadID: "t1",
				Available:    true,
				Enabled:      true,
				Bees: []SwarmBee{{
					ThreadID:          "bee-1",
					Name:              "DesignBee",
					Status:            "completed",
					AwaitingFinalize:  false,
					FinalizeRequested: true,
				}},
			}
			client.SetSwarmState("t1", doneState)
			hp := getSwarmPanelState(s)
			swarmPanelMu.Lock()
			hp.setStateLocked(doneState)
			swarmPanelMu.Unlock()

			tt.Frame()
			if reduced && tt.FramePending() {
				t.Fatal("ReduceMotion 开启时切换为已定稿不应请求下一帧")
			}
			tt.Advance(time.Second)
			if tt.FramePending() {
				t.Fatal("已定稿徽标过渡结束后不应继续请求下一帧")
			}
		}
	})
}

func jsonMarshalMust(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func waitDesignPickUntil(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}
