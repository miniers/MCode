// Package mcode 将内置 coding agent 接入 harness。
package mcode

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"strings"
	"sync"

	"mcode/internal/agent"
	"mcode/internal/agent/gate"
	"mcode/internal/agent/llm"
	"mcode/internal/agent/session"
	"mcode/internal/agent/tools"
	"mcode/internal/harness"
)

// Options 配置内置 agent harness。
type Options struct {
	// Kind 是 provider 种类标识；为空时使用 mcode。
	Kind harness.Kind
	// DisplayName 是展示给用户的名称；为空时回落到 Kind。
	DisplayName string
	// ModelIDs 返回该来源当前可选的模型；为空时表示没有额外模型。
	ModelIDs func() []string
	// RefreshModels 刷新模型目录；为空时不做任何事。
	RefreshModels func(context.Context) error
	// Available 报告该来源是否可用，不可用时给出原因；为空表示始终可用。
	Available     func() (bool, string)
	StoreDir      string
	ResolveClient func(model string, affinityKey string) (client llm.Client, modelID string, err error)
	SystemPrompt  func(turn harness.Turn, toolNames []string) string
	ExtraTools    func(ctx context.Context, turn harness.Turn) []tools.Tool
	DefaultModel string
	MaxRounds    int
	// ContextWindow 返回模型的上下文窗口（tokens），参数是解析前的完整模型串；
	// 可以为 nil，返回 0 表示未知。
	ContextWindow func(model string) int
	// MaxTokens 返回每个模型请求的输出上限（tokens），参数是解析前的完整模型串；
	// 可以为 nil，返回 0 表示交给 llm 层按模型取默认值。
	MaxTokens func(model string) int
	// CostUSD 返回本轮费用（美元），参数是解析前的完整模型串与本轮用量；
	// 返回假表示不填费用。可以为 nil。
	CostUSD func(model string, usage harness.Usage) (float64, bool)
}

// Harness 驱动内置 coding agent。
type Harness struct {
	opts  Options
	mu    sync.Mutex
	gates map[string]*gate.Session
	runs  map[string]*activeRun
	// pids 记录各线程当前正在运行的命令进程，供 ProcessRootPIDs 定位命令子进程树。
	pids map[string]map[int]bool
}

// activeRun 是一次正在执行的会话，pending 保存尚未注入的插话文本。
type activeRun struct {
	mu      sync.Mutex
	pending []string
	// closed 表示这一轮已经收尾，之后到达的插话要退回给调用方排队。
	closed bool
}

// enqueue 追加一段插话；队列已关闭时返回 false，表示本轮不再接收。
func (r *activeRun) enqueue(text string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	r.pending = append(r.pending, text)
	return true
}

func (r *activeRun) drain() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	texts := r.pending
	r.pending = nil
	return texts
}

// closeAndTake 关闭队列，并在同一把锁下取出剩余文本。
// 关闭后 enqueue 返回 false，插话不会再落进一个没人处理的队列。
func (r *activeRun) closeAndTake() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	texts := r.pending
	r.pending = nil
	return texts
}

// New 创建内置 agent harness。
func New(opts Options) *Harness {
	if opts.Kind == "" {
		opts.Kind = harness.Kind("mcode")
	}
	return &Harness{
		opts:  opts,
		gates: make(map[string]*gate.Session),
		runs:  make(map[string]*activeRun),
		pids:  make(map[string]map[int]bool),
	}
}

// Kind 返回 provider 种类。
func (h *Harness) Kind() harness.Kind {
	if h.opts.Kind != "" {
		return h.opts.Kind
	}
	return harness.Kind("mcode")
}

// Run 执行一轮内置 agent 对话。
func (h *Harness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) (retErr error) {
	// 引擎在进程内运行，也作为 swarm bee 的运行时并发执行，任何 panic 都会带崩整个进程。
	// 这里先注册、后执行（defer 后进先出），排在注销 activeRun 之后，panic 时注销仍然会跑。
	// recover 里不再 emit，避免 emit 自身出错时二次 panic；remote 按普通的 Run 错误结束本轮。
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("内置引擎 panic：%v\n%s", recovered, debug.Stack())
			retErr = fmt.Errorf("内置引擎内部错误：%v", recovered)
		}
	}()
	model := turn.Model
	if model == "" {
		model = h.opts.DefaultModel
	}
	if h.opts.ResolveClient == nil {
		return modelError(emit, fmt.Errorf("未配置模型解析器，无法连接模型 %q", model))
	}
	contextWindow := 0
	if h.opts.ContextWindow != nil {
		contextWindow = h.opts.ContextWindow(model)
	}
	client, modelID, err := h.opts.ResolveClient(model, affinityKey(turn))
	if err != nil {
		return modelError(emit, fmt.Errorf("无法解析模型 %q：%w", model, err))
	}

	toolset := tools.CodingToolsFor(modelID)
	toolset = append(toolset, agent.BuiltinTools(turn.Approver)...)
	seen := make(map[string]bool, len(toolset))
	for _, tool := range toolset {
		seen[tool.Name] = true
	}
	if h.opts.ExtraTools != nil {
		for _, tool := range h.opts.ExtraTools(ctx, turn) {
			if !seen[tool.Name] {
				seen[tool.Name] = true
				toolset = append(toolset, tool)
			}
		}
	}
	toolNames := make([]string, 0, len(toolset))
	for _, tool := range toolset {
		toolNames = append(toolNames, tool.Name)
	}
	system := "你是一个 coding agent。"
	if h.opts.SystemPrompt != nil {
		system = h.opts.SystemPrompt(turn, toolNames)
	}

	images := make([]llm.Part, 0, len(turn.Images))
	for _, path := range turn.Images {
		mimeType, ok := harness.ImageMediaType(path)
		if !ok {
			emitEvent(emit, harness.Event{Type: harness.EventError, Err: fmt.Sprintf("无法识别图片类型，已跳过：%s", path)})
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			emitEvent(emit, harness.Event{Type: harness.EventError, Err: fmt.Sprintf("读取图片失败，已跳过 %s：%v", path, err)})
			continue
		}
		images = append(images, llm.Part{Type: "image", MimeType: mimeType, Data: base64.StdEncoding.EncodeToString(data)})
	}

	var store *session.Store
	if h.opts.StoreDir != "" {
		store = session.NewStore(h.opts.StoreDir)
	}
	extraEnv := append(append([]string(nil), turn.Env...), harness.NonInteractiveEnv()...)
	// 会话 ID 由 agent.Run 生成，只能从它发出的事件里拿到真实值后登记，Steer 才能找到本轮。
	run := &activeRun{}
	var sessionID string
	runEmit := func(event harness.Event) {
		if event.Type == harness.EventSession && event.SessionID != "" && event.SessionID != sessionID {
			h.registerRun(event.SessionID, run)
			sessionID = event.SessionID
		}
		// 费用只能在上报前写上：复制一份 Usage，不改 agent 内部持有的对象。
		if event.Type == harness.EventUsage && event.Usage != nil && h.opts.CostUSD != nil {
			if cost, ok := h.opts.CostUSD(model, *event.Usage); ok {
				usage := *event.Usage
				usage.CostUSD = cost
				event.Usage = &usage
			}
		}
		emitEvent(emit, event)
	}
	defer func() { h.unregisterRun(sessionID) }()
	agentOpts := agent.Options{
		Client: client, Model: modelID, Effort: turn.Effort, System: system, Tools: toolset,
		Env:  tools.Env{WorkDir: turn.WorkDir, AdditionalDirs: turn.AdditionalDirs, ReadOnlyDirs: turn.ReadOnlyDirs, Sandbox: turn.Sandbox, Mode: turn.Mode, ExtraEnv: extraEnv},
		Mode: turn.Mode, Plan: turn.InteractionMode == "plan", Approver: turn.Approver, Gate: h.gateFor(turn.ThreadID),
		Store: store, SessionID: turn.SessionID, MaxRounds: h.opts.MaxRounds, ContextWindow: contextWindow,
		Emit: runEmit,
	}
	if h.opts.MaxTokens != nil {
		// 复用解析前的完整模型串，与 ContextWindow 的取值方式一致。
		agentOpts.MaxTokens = func(string) int { return h.opts.MaxTokens(model) }
	}
	prompt := turn.Prompt
	// 把线程的进程登记回调带进工具上下文，上层据此终止当前命令。
	runCtx := h.withProcessTracker(ctx, turn.ThreadID)
	for {
		agentOpts.Steer = run.drain
		runErr := agent.Run(runCtx, agentOpts, prompt, images)
		if runErr != nil || ctx.Err() != nil {
			// 出错或被取消时不再续跑，剩余插话随队列关闭退回调用方排队。
			run.closeAndTake()
			if runErr == nil {
				runErr = ctx.Err()
			}
			return runErr
		}
		leftovers := run.closeAndTake()
		if len(leftovers) == 0 {
			return nil
		}
		// 最后一次 drain 之后仍有插话落到收尾窗口：重新打开队列并续跑同一轮。
		run = &activeRun{}
		if sessionID != "" {
			agentOpts.SessionID = sessionID
			h.registerRun(sessionID, run)
		}
		prompt = strings.Join(leftovers, "\n\n")
		images = nil
	}
}

// Steer 向正在执行的会话追加一段插话，下一次模型调用前作为 user 消息注入。
// 找不到对应会话时返回 harness.ErrNotRunning；空文本直接忽略。
func (h *Harness) Steer(ctx context.Context, sessionID string, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	h.mu.Lock()
	run := h.runs[sessionID]
	h.mu.Unlock()
	if sessionID == "" || run == nil {
		return harness.ErrNotRunning
	}
	if !run.enqueue(text) {
		return harness.ErrNotRunning
	}
	return nil
}

// registerRun 登记正在执行的会话，供 Steer 找到它的待处理队列。
func (h *Harness) registerRun(sessionID string, run *activeRun) {
	if sessionID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.runs == nil {
		h.runs = make(map[string]*activeRun)
	}
	h.runs[sessionID] = run
}

// unregisterRun 注销已结束的会话。
func (h *Harness) unregisterRun(sessionID string) {
	if sessionID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.runs, sessionID)
}

// affinityKey 返回会话亲和键：优先线程 ID，未分配线程时退回会话 ID。
func affinityKey(turn harness.Turn) string {
	if turn.ThreadID != "" {
		return turn.ThreadID
	}
	return turn.SessionID
}

func (h *Harness) gateFor(threadID string) *gate.Session {
	if threadID == "" {
		return &gate.Session{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.gates == nil {
		h.gates = make(map[string]*gate.Session)
	}
	if h.gates[threadID] == nil {
		h.gates[threadID] = &gate.Session{}
	}
	return h.gates[threadID]
}

func modelError(emit func(harness.Event), err error) error {
	emitEvent(emit, harness.Event{Type: harness.EventError, Err: err.Error()})
	emitEvent(emit, harness.Event{Type: harness.EventDone})
	return err
}

func emitEvent(emit func(harness.Event), event harness.Event) {
	_ = harness.Emit(emit, event)
}

var _ harness.Harness = (*Harness)(nil)
var _ harness.Steerer = (*Harness)(nil)
