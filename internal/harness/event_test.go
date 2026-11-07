package harness

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEventValidate(t *testing.T) {
	tests := []struct {
		name    string
		event   Event
		wantErr bool
	}{
		// EventText
		{
			name:    "valid text event",
			event:   Event{Type: EventText, Text: "hello world"},
			wantErr: false,
		},
		{
			name:    "missing text in text event",
			event:   Event{Type: EventText, Text: ""},
			wantErr: true,
		},

		// EventReasoning
		{
			name:    "valid reasoning event",
			event:   Event{Type: EventReasoning, Text: "reasoning step"},
			wantErr: false,
		},
		{
			name:    "missing text in reasoning event",
			event:   Event{Type: EventReasoning, Text: ""},
			wantErr: true,
		},

		// EventToolStart
		{
			name:    "valid tool start event",
			event:   Event{Type: EventToolStart, ToolID: "call_1", ToolName: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)},
			wantErr: false,
		},
		{
			name:    "tool start missing tool id",
			event:   Event{Type: EventToolStart, ToolName: "bash"},
			wantErr: true,
		},
		{
			name:    "tool start missing tool name",
			event:   Event{Type: EventToolStart, ToolID: "call_1"},
			wantErr: true,
		},

		// EventToolOutput
		{
			name:    "valid tool output event",
			event:   Event{Type: EventToolOutput, ToolID: "call_1", Text: "line 1\n"},
			wantErr: false,
		},
		{
			name:    "tool output missing tool id",
			event:   Event{Type: EventToolOutput, Text: "line 1\n"},
			wantErr: true,
		},
		{
			name:    "tool output missing text",
			event:   Event{Type: EventToolOutput, ToolID: "call_1"},
			wantErr: true,
		},

		// EventToolEnd
		{
			name:    "valid tool end event",
			event:   Event{Type: EventToolEnd, ToolID: "call_1", Output: "done"},
			wantErr: false,
		},
		{
			name:    "tool end missing tool id",
			event:   Event{Type: EventToolEnd, Output: "done"},
			wantErr: true,
		},

		// EventBackgroundTaskStart
		{
			name:    "valid background task start event",
			event:   Event{Type: EventBackgroundTaskStart, ToolID: "toolu_1", TaskID: "task-1", Text: "sleep 5"},
			wantErr: false,
		},
		{
			name:    "background task start missing tool id",
			event:   Event{Type: EventBackgroundTaskStart, TaskID: "task-1"},
			wantErr: true,
		},

		// EventBackgroundTaskEnd
		{
			name:    "valid background task end event",
			event:   Event{Type: EventBackgroundTaskEnd, ToolID: "toolu_1", TaskID: "task-1", TaskStatus: "completed"},
			wantErr: false,
		},
		{
			name:    "background task end missing tool id",
			event:   Event{Type: EventBackgroundTaskEnd, TaskStatus: "completed"},
			wantErr: true,
		},

		// EventFileChange
		{
			name:    "valid file change event",
			event:   Event{Type: EventFileChange, Path: "main.go", Patch: "--- a\n+++ b\n"},
			wantErr: false,
		},
		{
			name:    "file change missing path",
			event:   Event{Type: EventFileChange, Patch: "--- a\n+++ b\n"},
			wantErr: true,
		},

		// EventSession
		{
			name:    "valid session event",
			event:   Event{Type: EventSession, SessionID: "sess_123"},
			wantErr: false,
		},
		{
			name:    "session missing session id",
			event:   Event{Type: EventSession, SessionID: ""},
			wantErr: true,
		},

		// EventUsage
		{
			name: "valid usage event",
			event: Event{
				Type:  EventUsage,
				Usage: &Usage{InputTokens: 100, OutputTokens: 50, CachedTokens: 20},
			},
			wantErr: false,
		},
		{
			name:    "usage missing usage payload",
			event:   Event{Type: EventUsage, Usage: nil},
			wantErr: true,
		},

		// EventError
		{
			name:    "valid error event",
			event:   Event{Type: EventError, Err: "something failed"},
			wantErr: false,
		},
		{
			name:    "error missing err string",
			event:   Event{Type: EventError, Err: ""},
			wantErr: true,
		},

		// EventDone
		{
			name:    "valid done event without stop reason",
			event:   Event{Type: EventDone},
			wantErr: false,
		},
		{
			name:    "valid done event with stop reason",
			event:   Event{Type: EventDone, StopReason: "end_turn"},
			wantErr: false,
		},

		// EventTurnAccepted
		{
			name:    "valid turn accepted event",
			event:   Event{Type: EventTurnAccepted},
			wantErr: false,
		},

		// 未知类型
		{
			name:    "empty event type",
			event:   Event{Type: ""},
			wantErr: true,
		},
		{
			name:    "unknown custom event type",
			event:   Event{Type: "custom_type"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.event.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Event.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestEmit(t *testing.T) {
	t.Run("emit valid event", func(t *testing.T) {
		var received *Event
		err := Emit(func(e Event) {
			received = &e
		}, Event{
			Type: EventText,
			Text: "hello",
		})
		if err != nil {
			t.Fatalf("Emit returned unexpected error: %v", err)
		}
		if received == nil || received.Text != "hello" {
			t.Fatalf("Emit did not forward event correctly: %+v", received)
		}
	})

	t.Run("emit invalid event suppresses emit and returns error", func(t *testing.T) {
		called := false
		err := Emit(func(e Event) {
			called = true
		}, Event{
			Type: EventText,
			Text: "",
		})
		if err == nil {
			t.Fatal("expected error for invalid event, got nil")
		}
		if called {
			t.Fatal("emit callback should not be invoked when validation fails")
		}
	})

	t.Run("emit with nil callback does not panic", func(t *testing.T) {
		err := Emit(nil, Event{
			Type: EventDone,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

// TestUsageOptionalFieldsJSON 校验新增的上下文窗口与费用字段的序列化：为零时省略，有值时按约定键名输出，
// 并且不影响 Validate。
func TestUsageOptionalFieldsJSON(t *testing.T) {
	zero, err := json.Marshal(Usage{InputTokens: 1})
	if err != nil {
		t.Fatalf("marshal zero usage: %v", err)
	}
	if strings.Contains(string(zero), "context_window") || strings.Contains(string(zero), "cost_usd") {
		t.Errorf("为零的可选字段应省略: %s", zero)
	}

	full, err := json.Marshal(Usage{ContextTokens: 10, ContextWindow: 200000, CostUSD: 0.5})
	if err != nil {
		t.Fatalf("marshal full usage: %v", err)
	}
	for _, want := range []string{`"context_tokens":10`, `"context_window":200000`, `"cost_usd":0.5`} {
		if !strings.Contains(string(full), want) {
			t.Errorf("序列化结果缺少 %s: %s", want, full)
		}
	}

	if err := (Event{Type: EventUsage, Usage: &Usage{ContextWindow: 200000, CostUSD: 1}}).Validate(); err != nil {
		t.Errorf("新增字段不应影响 Validate: %v", err)
	}
}

// TestToolOutputEventJSON 校验增量输出事件的序列化形态：tool_id 与 text 沿用既有键名，
// 方便 provider 侧直接构造。
func TestToolOutputEventJSON(t *testing.T) {
	raw, err := json.Marshal(Event{Type: EventToolOutput, ToolID: "call-1", Text: "chunk"})
	if err != nil {
		t.Fatalf("marshal tool_output event: %v", err)
	}
	if want := `{"type":"tool_output","text":"chunk","tool_id":"call-1"}`; string(raw) != want {
		t.Errorf("tool_output 事件序列化 = %s, want %s", raw, want)
	}
}

// TestErrorEventErrKindJSON 校验错误类别的序列化：没有类别时省略键，有类别时按约定键名输出，
// 并且不影响 Validate。
func TestErrorEventErrKindJSON(t *testing.T) {
	bare, err := json.Marshal(Event{Type: EventError, Err: "boom"})
	if err != nil {
		t.Fatalf("marshal error event: %v", err)
	}
	if strings.Contains(string(bare), "err_kind") {
		t.Errorf("没有类别时应省略 err_kind: %s", bare)
	}

	kinded, err := json.Marshal(Event{Type: EventError, Err: "Not logged in · Please run /login", ErrKind: ErrKindAuthRequired})
	if err != nil {
		t.Fatalf("marshal error event with kind: %v", err)
	}
	if !strings.Contains(string(kinded), `"err_kind":"auth_required"`) {
		t.Errorf("序列化结果缺少 err_kind: %s", kinded)
	}

	if err := (Event{Type: EventError, Err: "boom", ErrKind: ErrKindRateLimited}).Validate(); err != nil {
		t.Errorf("新增字段不应影响 Validate: %v", err)
	}
}
