package frontdesk

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"

	"mcode/internal/textgen"
)

func (d *Desk) cliFallback(ctx context.Context, in Inbound, snap Snapshot) (bool, error) {
	opts := d.CLIOptions
	provider := opts.Provider
	if provider == "" {
		provider = "claude"
	}
	opts.Model = textgen.CheapestModel(provider)
	prompt := snap.Text + "\n只输出 JSON 对象：{\"reply\":\"回复\",\"calls\":[{\"tool\":\"create_task\",\"args\":{}}]}。只允许 create_task、amend_task、remember、answer_decision。"
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
	if len(raw.Reply) == 0 || string(raw.Reply) == "null" || json.Unmarshal(raw.Reply, &reply) != nil || len(raw.Calls) == 0 || string(raw.Calls) == "null" || json.Unmarshal(raw.Calls, &calls) != nil {
		return false, nil
	}
	if strings.TrimSpace(reply) != "" {
		if d.Feed == nil {
			return true, errors.New("frontdesk: 回复存储不可用")
		}
		if err := d.Feed.AppendAssistant(ctx, in.ThreadID, KindStewardReply, reply); err != nil {
			return true, err
		}
	}
	tools := d.toolbox()
	for _, call := range calls {
		if call.Tool != "create_task" && call.Tool != "amend_task" && call.Tool != "remember" && call.Tool != "answer_decision" {
			log.Printf("frontdesk: 丢弃 CLI 工具 %s", call.Tool)
			continue
		}
		tool, ok := tools.Get(call.Tool)
		if !ok {
			log.Printf("frontdesk: 未知 CLI 工具 %s", call.Tool)
			continue
		}
		if _, err := validate(tool, call.Args); err != nil {
			log.Printf("frontdesk: 丢弃 CLI 参数：%v", err)
			continue
		}
		if _, err := tool.Run(ctx, call.Args); err != nil {
			log.Printf("frontdesk: CLI 写入失败：%v", err)
		}
	}
	return true, nil
}
