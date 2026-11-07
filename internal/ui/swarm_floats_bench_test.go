package mcodeui

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 子代理浮窗多时的性能基准。
//
// 场景对齐用户反馈的极端情况：主会话派出 30 个子代理（4 个运行中、其余已完成并带长汇报），
// 打开“显示子代理详细步骤”与“自动分散展示”，4 个浮窗同时可见，
// 每个运行中的子代理各有约 200 条消息（长 Markdown 正文、推理与工具调用）。
// 静止渲染基准测一帧的耗时与分配；流式增量基准模拟 4 个 bee 交替追加增量，
// 每次增量都走一遍真实的会话更新路径并重绘一帧。
//
//	go test -run '^$' -bench SwarmFloats -benchmem -cpuprofile /tmp/swarm.cpu -timeout 300s ./internal/ui/

const (
	benchSwarmLeadID     = "t1"
	benchSwarmBeeTotal   = 30
	benchSwarmRunBees    = 4
	benchSwarmMsgsPerRun = 200
)

// benchSwarmLongText 是一段接近真实长正文的 Markdown：段落、加粗、行内代码、列表与代码块。
func benchSwarmLongText(seed string) string {
	var b strings.Builder
	b.WriteString("## 第 ")
	b.WriteString(seed)
	b.WriteString(" 步核对结果\n\n")
	b.WriteString("已经核对 `renderSwarmBeeFloats` 与 `swarmFloatFrameFor` 的调用链，")
	b.WriteString("确认本帧只算一次面板场景，**没有把整份 SwarmState 再拷贝一遍**。\n\n")
	b.WriteString("- 派生视图按 `stateRev` 缓存，渲染路径只读\n")
	b.WriteString("- 嵌入状态按子线程缓存 composer 状态\n")
	b.WriteString("- 时间线列表只渲染可见行\n\n")
	b.WriteString("```go\nfunc (s *chatState) invalidate() {\n\ts.services.Invalidate()\n}\n```\n\n")
	b.WriteString("接下来继续核对自动收起与提问弹窗的时序，确保外观与交互行为不变。\n")
	return b.String()
}

// benchSwarmBeeMessages 造一个运行中 bee 的会话记录：用户指令、推理、长正文与工具调用交替。
func benchSwarmBeeMessages(threadID string, n int) []Message {
	msgs := make([]Message, 0, n+1)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%s-m%d", threadID, i)
		turn := i / 4
		switch i % 4 {
		case 0:
			msgs = append(msgs, Message{
				ID: id, Role: "user", Kind: "text", IsFinal: true, TurnIndex: turn,
				Text: "请继续推进第 " + strconv.Itoa(turn) + " 步，并把核对结论写进汇报。",
			})
		case 1:
			msgs = append(msgs, Message{
				ID: id, Role: "assistant", Kind: "reasoning", TurnIndex: turn,
				Text: benchSwarmLongText(strconv.Itoa(turn)),
			})
		case 2:
			msgs = append(msgs, Message{
				ID: id, Role: "tool", Kind: "tool", TurnIndex: turn, IsFinal: true,
				ToolName: "bash", ToolStatus: "completed",
				ToolInput: `{"command":"go test -run '^$' -bench SwarmFloats -benchmem -timeout 300s ./internal/ui/"}`,
				Text:      "ok  \tmcode/internal/ui\t1.234s",
			})
		default:
			msgs = append(msgs, Message{
				ID: id, Role: "assistant", Kind: "text", TurnIndex: turn,
				Text: benchSwarmLongText(strconv.Itoa(turn)),
			})
		}
	}
	// 末尾留一条正在流式输出的正文，流式增量基准往它上面追加。
	msgs = append(msgs, Message{
		ID: threadID + "-live", Role: "assistant", Kind: "text", TurnIndex: n / 4,
		Text: benchSwarmLongText("live"),
	})
	return msgs
}

// benchSwarmLeadMessages 造主会话自己的时间线，让主聊天列也在每帧真实渲染。
func benchSwarmLeadMessages(n int) []Message {
	msgs := make([]Message, 0, n)
	for i := 0; i < n; i++ {
		role := "assistant"
		if i%2 == 0 {
			role = "user"
		}
		msgs = append(msgs, Message{
			ID: fmt.Sprintf("lead-m%d", i), Role: role, Kind: "text", TurnIndex: i / 2,
			IsFinal: true, Text: benchSwarmLongText(strconv.Itoa(i)),
		})
	}
	return msgs
}

// benchSwarmProviders 造一份真实规模的供应商目录：嵌入状态每帧要把父会话的供应商切片克隆给自己。
func benchSwarmProviders() []ProviderInfo {
	providers := make([]ProviderInfo, 0, 6)
	for p := 0; p < 6; p++ {
		models := make([]ModelInfo, 0, 40)
		for m := 0; m < 40; m++ {
			models = append(models, ModelInfo{ID: fmt.Sprintf("model-%d-%d", p, m), Name: fmt.Sprintf("Model %d-%d", p, m)})
		}
		providers = append(providers, ProviderInfo{
			ID:        fmt.Sprintf("provider-%d", p),
			Name:      fmt.Sprintf("Provider %d", p),
			Available: true,
			Models:    models,
		})
	}
	return providers
}

// swarmFloatsBenchState 是本基准的固定场景：主会话状态、测试窗口、30 个 bee 与 4 个可见 bee 的会话状态。
type swarmFloatsBenchState struct {
	s    *chatState
	tt   *ui.Tester
	bees []SwarmBee
	kids []*chatState
	// baseTexts 是各 bee 最后一条正文的初始内容：流式增量在它后面换一段固定长度的新文本，
	// 让每次增量的正文长度稳定，基准数值不会随迭代次数增长。
	baseTexts []string
}

// waitBench 轮询等待基准场景的就绪条件（异步加载：会话列表、供应商目录、bee 历史）。
func waitBench(b *testing.B, what string, cond func() bool) {
	b.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	b.Fatalf("基准场景未就绪：%s", what)
}

// newSwarmFloatsBenchState 构造基准场景并跑热一帧，保证测量的是稳态。
func newSwarmFloatsBenchState(b *testing.B) *swarmFloatsBenchState {
	b.Helper()
	resetSwarmFloatsBenchGlobals(benchSwarmLeadID)

	fake := NewFakeClient()
	fake.SetProviders(benchSwarmProviders())
	fake.SetMessages(benchSwarmLeadID, benchSwarmLeadMessages(40))

	now := time.Now()
	bees := make([]SwarmBee, 0, benchSwarmBeeTotal)
	for i := 0; i < benchSwarmRunBees; i++ {
		id := fmt.Sprintf("h-run-%d", i)
		bees = append(bees, SwarmBee{
			ThreadID:  id,
			Name:      swarmRosterPersonas[i%len(swarmRosterPersonas)].Name,
			Task:      "核对子代理浮窗在多个 bee 同时运行时的性能，找出每帧重复计算的部分",
			Status:    "running",
			Activity:  "正在读取 internal/ui/swarm_bee_floats.go 并核对调用链",
			Provider:  "provider-0",
			Model:     "model-0-0",
			Index:     i,
			Tokens:    18_000 + i*1200,
			ToolCalls: 12 + i,
			StartedAt: now.Add(-time.Duration(4+i) * time.Minute),
		})
		fake.SetMessages(id, benchSwarmBeeMessages(id, benchSwarmMsgsPerRun))
	}
	summary := strings.Repeat(benchSwarmLongText("汇报"), 6)
	for i := benchSwarmRunBees; i < benchSwarmBeeTotal; i++ {
		finished := now.Add(-time.Duration(10+i) * time.Minute)
		bees = append(bees, SwarmBee{
			ThreadID:   fmt.Sprintf("h-done-%d", i),
			Name:       swarmRosterPersonas[i%len(swarmRosterPersonas)].Name,
			Task:       "整理文档与脚本",
			Status:     "completed",
			Summary:    summary,
			Provider:   "provider-0",
			Model:      "model-0-0",
			Index:      i,
			Tokens:     9_000 + i*300,
			ToolCalls:  5,
			StartedAt:  finished.Add(-12 * time.Minute),
			FinishedAt: &finished,
		})
	}
	swarmState := SwarmState{
		Available:    true,
		Enabled:      true,
		LeadThreadID: benchSwarmLeadID,
		Bees:         bees,
	}
	fake.SetSwarmState(benchSwarmLeadID, swarmState)

	s, view := buildRoot(fake)
	st := NewSettingsState(b.TempDir(), nil, s)
	st.cfg.Orchestration.ShowsBeeDetails = true
	st.cfg.Orchestration.AutoPopsBees = true
	setChatSettings(s, st)

	frozen := func(c *ui.Context) {
		// 基准只量“静止的一帧”：动画（侧边栏滑动、面板停靠、逐字揭示）会让测试框架反复请求下一帧，
		// 关闭动画后每次 Frame 就是一次完整的界面构建，数值才是可比较的单帧成本。
		c.DisableAnimations()
		view(c)
	}
	tt := ui.NewTester(frozen, 1920, 1080)
	// 会话列表与供应商目录都是异步加载的，等主会话选中并且供应商目录落地。
	waitBench(b, "主会话选中与供应商目录加载", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.selectedThreadID == benchSwarmLeadID && len(s.providers) > 0
	})

	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	hp.loadedFor = benchSwarmLeadID
	hp.setStateLocked(swarmState)
	swarmPanelMu.Unlock()

	// 模拟用户选中第一个 bee：团队面板看它，其余 3 个运行中的 bee 各自一个面板。
	swarmFloatMu.Lock()
	swarmSelectedBeeID[benchSwarmLeadID] = bees[0].ThreadID
	swarmFloatMu.Unlock()

	// 32 位色深与字体度量都在第一次渲染时建立，先热一帧再让 bee 历史加载完。
	tt.Frame()
	kids := make([]*chatState, benchSwarmRunBees)
	baseTexts := make([]string, benchSwarmRunBees)
	for i := 0; i < benchSwarmRunBees; i++ {
		kids[i] = beeChatState(s, bees[i])
	}
	waitBench(b, "4 个 bee 的历史消息加载", func() bool {
		for _, child := range kids {
			child.mu.Lock()
			ready := child.selectedThreadID != "" && len(child.messages) == benchSwarmMsgsPerRun+1
			child.mu.Unlock()
			if !ready {
				return false
			}
		}
		return true
	})
	for i, child := range kids {
		child.mu.Lock()
		if n := len(child.messages); n > 0 {
			baseTexts[i] = child.messages[n-1].Text
		}
		child.mu.Unlock()
	}
	tt.Frame()

	// 稳态检查：4 个浮窗都可见（团队面板 + 3 个独立面板）。
	slots := 0
	probe := ui.NewTester(func(c *ui.Context) {
		slots = len(swarmFloatFrameFor(c, s).scene.slots)
	}, 1920, 1080)
	_ = probe
	if slots != 4 {
		b.Fatalf("基准场景应有 4 个可见浮窗，实际 %d", slots)
	}

	b.Cleanup(func() {
		closeEmbeddedBees(s)
		resetSwarmFloatsBenchGlobals(benchSwarmLeadID)
	})
	return &swarmFloatsBenchState{s: s, tt: tt, bees: bees, kids: kids, baseTexts: baseTexts}
}

// resetSwarmFloatsBenchGlobals 清掉基准用主会话的浮窗记账，避免用例之间互相影响。
func resetSwarmFloatsBenchGlobals(leadID string) {
	swarmFloatMu.Lock()
	defer swarmFloatMu.Unlock()
	delete(swarmFloatPanelDismissed, leadID)
	delete(swarmFloatDismissedBees, leadID)
	delete(swarmSelectedBeeID, leadID)
	delete(swarmTeamDock, leadID)
	delete(swarmAutoDocks, leadID)
	delete(swarmAutoHeld, leadID)
	delete(swarmStackOrder, leadID)
	delete(swarmAutoPopsOverride, leadID)
	delete(swarmFloatForced, leadID)
	delete(swarmFloatForcedHadBees, leadID)
	delete(swarmBeeDoneSince, leadID)
	delete(swarmBeeAutoHideHandled, leadID)
	delete(swarmBeeSeenActive, leadID)
	delete(swarmQuestionPopped, leadID)
	for key, timer := range swarmAutoHideTimers {
		if strings.HasPrefix(key, leadID+"|") {
			timer.Stop()
			delete(swarmAutoHideTimers, key)
		}
	}
	for key, timer := range swarmPulseTimers {
		if strings.HasPrefix(key, leadID+"|") {
			timer.Stop()
			delete(swarmPulseTimers, key)
		}
	}
}

// appendStreamDelta 模拟一个 bee 的流式增量：把它最后一条正文换成“原文 + 一段新增量”，
// 再走一遍与后端 Watch 增量相同的更新路径（会请求重绘）。
func (f *swarmFloatsBenchState) appendStreamDelta(i int) {
	kid := i % len(f.kids)
	child := f.kids[kid]
	child.mu.Lock()
	if len(child.messages) == 0 {
		child.mu.Unlock()
		return
	}
	last := child.messages[len(child.messages)-1]
	last.Text = f.baseTexts[kid] + "\n\n增量片段 " + strconv.Itoa(i) +
		"：继续核对面板场景与时间线缓存，确认让位宽度、自动收起与聚合行为不变。"
	var th Thread
	for _, t := range child.threads {
		if t.ID == child.selectedThreadID {
			th = t
			break
		}
	}
	child.mu.Unlock()
	child.onWatchUpdate(ThreadUpdate{ThreadID: th.ID, Thread: th, Messages: []Message{last}})
}

// BenchmarkSwarmFloatsIdleFrame 测量 4 个浮窗静止时渲染一帧的耗时与分配。
func BenchmarkSwarmFloatsIdleFrame(b *testing.B) {
	f := newSwarmFloatsBenchState(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f.tt.Frame()
	}
	b.StopTimer()
	if f.tt.FramePending() {
		b.Logf("注意：静止帧之后仍有一帧待渲染")
	}
}

// BenchmarkSwarmFloatsStreamDelta 测量 4 个 bee 交替追加流式增量时，每次增量的耗时与分配。
func BenchmarkSwarmFloatsStreamDelta(b *testing.B) {
	f := newSwarmFloatsBenchState(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f.appendStreamDelta(i)
		f.tt.Frame()
	}
}
