package frontdesk

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"

	"mcode/internal/providers/claude"
	"mcode/internal/providers/codex"
	"mcode/internal/remote"
	"mcode/internal/textgen"
)

func cliModel(model string, known []string) string {
	model = strings.TrimSpace(model)
	if _, rest, ok := strings.Cut(model, "/"); ok {
		model = rest
	}
	for _, id := range known {
		if id != "" && strings.EqualFold(model, id) {
			return model
		}
	}
	return ""
}

func (d *Desk) cliFallback(ctx context.Context, in Inbound, snap Snapshot, opts textgen.Options) (bool, error) {
	provider := strings.ToLower(strings.TrimSpace(opts.Provider))
	if provider == "" {
		provider = "claude"
	}
	var known []string
	switch provider {
	case "claude":
		known = []string{"default", "opus", "sonnet", "haiku"}
		for _, model := range claude.CachedModels() {
			known = append(known, model.ID, model.ResolvedModel)
		}
	case "codex":
		for _, model := range codex.CachedModels() {
			known = append(known, model.ID)
		}
	case "pi", "memory", "mcode":
		for _, model := range remote.ProviderModels(provider) {
			known = append(known, model.ID)
			if provider == "pi" {
				if _, id, ok := strings.Cut(model.ID, "/"); ok {
					known = append(known, id)
				}
			}
		}
	}
	known = append(known, textgen.CheapestModel(provider))
	originalModel := opts.Model
	opts.Model = cliModel(originalModel, known)
	if opts.Model == "" {
		if strings.TrimSpace(originalModel) != "" {
			log.Printf("frontdesk: 丢弃未知 CLI 模型 %q", originalModel)
		}
		opts.Model = remote.DefaultModel(provider)
		if opts.Model == "" {
			opts.Model = textgen.CheapestModel(provider)
		}
	}
	prompt := snap.Text + "\n只输出 JSON 对象：{\"reply\":\"回复\",\"calls\":[{\"tool\":\"create_task\",\"args\":{}}]}。只允许写工具 create_task、amend_task、cancel_task、set_priority、reassign_task、remember、edit_memory、answer_decision，不允许读工具；信息不足时不要编造。"
	out, err := d.Generator(ctx, opts, prompt)
	if err != nil {
		log.Printf("frontdesk: CLI 失败：%v", err)
		return false, nil
	}
	var raw struct {
		Reply json.RawMessage `json:"reply"`
		Calls json.RawMessage `json:"calls"`
	}
	if json.Unmarshal([]byte(out), &raw) != nil {
		return false, nil
	}
	var reply string
	var calls []struct {
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}
	if len(raw.Reply) == 0 || string(raw.Reply) == "null" || json.Unmarshal(raw.Reply, &reply) != nil {
		return false, nil
	}
	if len(raw.Calls) != 0 && json.Unmarshal(raw.Calls, &calls) != nil {
		return false, nil
	}
	if strings.TrimSpace(reply) == "" && len(calls) == 0 {
		return true, nil
	}
	tools := d.toolbox(in)
	for _, call := range calls {
		tool, ok := tools.Get(call.Tool)
		if !ok || !tool.Write {
			log.Printf("frontdesk: 不允许的 CLI 工具 %s", call.Tool)
			return false, nil
		}
		if _, err := validate(tool, call.Args); err != nil {
			log.Printf("frontdesk: 无效 CLI 参数：%v", err)
			return false, nil
		}
	}
	if strings.TrimSpace(reply) != "" {
		if d.Feed == nil {
			return true, errors.New("frontdesk: 回复存储不可用")
		}
		if err := d.Feed.AppendAssistant(ctx, in.ThreadID, KindStewardReply, reply); err != nil {
			return true, err
		}
	}
	var firstErr error
	succeeded := 0
	for _, call := range calls {
		tool, _ := tools.Get(call.Tool)
		_, err := tool.Run(ctx, call.Args)
		if err != nil {
			log.Printf("frontdesk: CLI 写入失败：%v", err)
			if firstErr == nil {
				firstErr = err
			}
		} else {
			succeeded++
		}
	}
	if succeeded > 0 {
		return true, nil
	}
	return true, firstErr
}
