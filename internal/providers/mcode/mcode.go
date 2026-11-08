// Package mcode 将内置 coding agent 接入 harness。
package mcode

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
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
	StoreDir      string
	ResolveClient func(model string) (client llm.Client, modelID string, err error)
	SystemPrompt  func(turn harness.Turn, toolNames []string) string
	ExtraTools    func(ctx context.Context, turn harness.Turn) []tools.Tool
	DefaultModel  string
	MaxRounds     int
}

// Harness 驱动内置 coding agent。
type Harness struct {
	opts  Options
	mu    sync.Mutex
	gates map[string]*gate.Session
}

// New 创建内置 agent harness。
func New(opts Options) *Harness {
	return &Harness{opts: opts, gates: make(map[string]*gate.Session)}
}

// Kind 返回 provider 种类。
func (*Harness) Kind() harness.Kind { return harness.Kind("mcode") }

// Run 执行一轮内置 agent 对话。
func (h *Harness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	model := turn.Model
	if model == "" {
		model = h.opts.DefaultModel
	}
	if h.opts.ResolveClient == nil {
		return modelError(emit, fmt.Errorf("未配置模型解析器，无法连接模型 %q", model))
	}
	client, modelID, err := h.opts.ResolveClient(model)
	if err != nil {
		return modelError(emit, fmt.Errorf("无法解析模型 %q：%w", model, err))
	}

	toolset := tools.CodingTools()
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
	return agent.Run(ctx, agent.Options{
		Client: client, Model: modelID, Effort: turn.Effort, System: system, Tools: toolset,
		Env:  tools.Env{WorkDir: turn.WorkDir, AdditionalDirs: turn.AdditionalDirs, Sandbox: turn.Sandbox, Mode: turn.Mode, ExtraEnv: extraEnv},
		Mode: turn.Mode, Plan: turn.InteractionMode == "plan", Approver: turn.Approver, Gate: h.gateFor(turn.ThreadID),
		Store: store, SessionID: turn.SessionID, MaxRounds: h.opts.MaxRounds, Emit: emit,
	}, turn.Prompt, images)
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
