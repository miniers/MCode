package app

import (
	"context"
	"strings"

	"mcode/internal/agent/llm"
	"mcode/internal/config"
)

// mcodeGenerateText 用内置 mcode provider 的模型完成一次性文本生成。
// 模型按 resolveMcodeClient 的规则解析到 Magpie 端点或 Anthropic；空值与 "default"
// 的归一化也在 resolveMcodeClient 内完成。请求只带一条 user 消息、不带工具，effort 原样透传；
// 返回最终 assistant 消息里的纯文本，去掉首尾空白。
func mcodeGenerateText(ctx context.Context, cfg config.Config, secrets config.SecretStore, model, effort, prompt string) (string, error) {
	client, modelID, err := resolveMcodeClient(ctx, cfg, secrets, model, "")
	if err != nil {
		return "", err
	}
	return streamOnce(ctx, client, modelID, effort, prompt)
}

// streamOnce 发送无工具的单次请求并拼接文本。
func streamOnce(ctx context.Context, client llm.Client, modelID, effort, prompt string) (string, error) {
	resp, err := client.Stream(ctx, llm.Request{
		Model:  modelID,
		Effort: strings.TrimSpace(effort),
		Messages: []llm.Message{{
			Role:    "user",
			Content: []llm.Part{{Type: "text", Text: prompt}},
		}},
	}, nil)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", nil
	}
	var text strings.Builder
	for _, part := range resp.Message.Content {
		if part.Type == "text" {
			text.WriteString(part.Text)
		}
	}
	return strings.TrimSpace(text.String()), nil
}
