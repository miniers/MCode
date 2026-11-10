package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"mcode/internal/agent/llm"
	"mcode/internal/agent/session"
	"mcode/internal/agent/tools"
)

func recallText(message llm.Message) string {
	var parts []string
	for _, part := range message.Content {
		if part.Type == "text" {
			parts = append(parts, part.Text)
		}
	}
	for _, call := range message.ToolCalls {
		parts = append(parts, call.Name+" "+string(call.Arguments))
	}
	return strings.Join(parts, "\n")
}

func recallRole(message llm.Message) string {
	if message.Role == "tool" {
		return "tool (" + message.ToolCallID + ")"
	}
	return message.Role
}

func recallTool(store *session.Store, id string) tools.Tool {
	return tools.Tool{
		Name:        "recall",
		Description: "Search earlier original messages compacted from this session. Use when the summary lacks details.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"index":{"type":"integer","minimum":1},"limit":{"type":"integer","minimum":1}}}`),
		ReadOnly:    true,
		Run: func(_ context.Context, _ tools.Env, raw json.RawMessage) (tools.Result, error) {
			var args struct {
				Query string `json:"query"`
				Index *int   `json:"index"`
				Limit int    `json:"limit"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return tools.Result{Text: err.Error(), IsError: true}, nil
			}
			if args.Limit < 0 || (args.Index != nil && *args.Index < 1) {
				return tools.Result{Text: "Invalid index or limit", IsError: true}, nil
			}
			if args.Limit == 0 {
				args.Limit = 5
			}
			entries, err := store.Load(id)
			if err != nil {
				return tools.Result{}, err
			}
			messages := foldedFromEntries(entries)
			if args.Index != nil {
				if *args.Index > len(messages) {
					return tools.Result{Text: "No message at that index."}, nil
				}
				message := messages[*args.Index-1]
				text := []rune(recallText(message))
				suffix := ""
				if len(text) > 20000 {
					text = text[:20000]
					suffix = "\n[truncated]"
				}
				return tools.Result{Text: fmt.Sprintf("#%d %s: %s%s", *args.Index, recallRole(message), string(text), suffix)}, nil
			}
			if args.Query == "" {
				return tools.Result{Text: "Provide a query or index."}, nil
			}
			query := []rune(strings.Map(unicode.ToLower, args.Query))
			var out strings.Builder
			total := 0
			for i, message := range messages {
				text := []rune(recallText(message))
				lower := []rune(strings.Map(unicode.ToLower, string(text)))
				at := strings.Index(string(lower), string(query))
				if at < 0 {
					continue
				}
				pos := len([]rune(string(lower)[:at]))
				total++
				if total <= args.Limit {
					snippet := string(text[max(0, pos-300):min(len(text), pos+len(query)+300)])
					snippet = strings.ReplaceAll(snippet, "\n", " ")
					fmt.Fprintf(&out, "#%d %s: %s\n", i+1, recallRole(message), snippet)
				}
			}
			if total == 0 {
				return tools.Result{Text: "No matches."}, nil
			}
			fmt.Fprintf(&out, "Total matches: %d", total)
			return tools.Result{Text: out.String()}, nil
		},
	}
}
