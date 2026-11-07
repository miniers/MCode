package mcodeui

import (
	"testing"
)

func TestComputeSendDraftState_TableDriven(t *testing.T) {
	tests := []struct {
		name              string
		isRunning         bool
		hasContent        bool
		canContinue       bool
		queueWhileRunning bool
		editingQueued     bool
		want              sendDraftState
	}{
		{
			name:              "editing_queued_overrides_all",
			isRunning:         true,
			hasContent:        true,
			canContinue:       true,
			queueWhileRunning: true,
			editingQueued:     true,
			want:              sendStateSaveEdit,
		},
		{
			name:              "editing_queued_idle_no_content",
			isRunning:         false,
			hasContent:        false,
			canContinue:       true,
			queueWhileRunning: false,
			editingQueued:     true,
			want:              sendStateSaveEdit,
		},
		{
			name:              "running_with_content_queue_enabled",
			isRunning:         true,
			hasContent:        true,
			canContinue:       false,
			queueWhileRunning: true,
			editingQueued:     false,
			want:              sendStateQueue,
		},
		{
			name:              "running_with_content_queue_disabled_steer",
			isRunning:         true,
			hasContent:        true,
			canContinue:       false,
			queueWhileRunning: false,
			editingQueued:     false,
			want:              sendStateSteer,
		},
		{
			name:              "running_no_content_stop",
			isRunning:         true,
			hasContent:        false,
			canContinue:       false,
			queueWhileRunning: false,
			editingQueued:     false,
			want:              sendStateStop,
		},
		{
			name:              "running_no_content_even_if_can_continue",
			isRunning:         true,
			hasContent:        false,
			canContinue:       true,
			queueWhileRunning: true,
			editingQueued:     false,
			want:              sendStateStop,
		},
		{
			name:              "idle_with_content_send",
			isRunning:         false,
			hasContent:        true,
			canContinue:       false,
			queueWhileRunning: false,
			editingQueued:     false,
			want:              sendStateSend,
		},
		{
			name:              "idle_with_content_and_can_continue_favors_send",
			isRunning:         false,
			hasContent:        true,
			canContinue:       true,
			queueWhileRunning: false,
			editingQueued:     false,
			want:              sendStateSend,
		},
		{
			name:              "idle_no_content_can_continue",
			isRunning:         false,
			hasContent:        false,
			canContinue:       true,
			queueWhileRunning: false,
			editingQueued:     false,
			want:              sendStateSend,
		},
		{
			name:              "idle_no_content_cannot_continue_send",
			isRunning:         false,
			hasContent:        false,
			canContinue:       false,
			queueWhileRunning: false,
			editingQueued:     false,
			want:              sendStateSend,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeSendDraftState(tt.isRunning, tt.hasContent, tt.canContinue, tt.queueWhileRunning, tt.editingQueued)
			if got != tt.want {
				t.Fatalf("computeSendDraftState(%v, %v, %v, %v, %v) = %v, want %v",
					tt.isRunning, tt.hasContent, tt.canContinue, tt.queueWhileRunning, tt.editingQueued, got, tt.want)
			}
		})
	}
}

func TestRunningSendDraftState_TableDriven(t *testing.T) {
	tests := []struct {
		name                  string
		isRunning             bool
		hasContent            bool
		canContinue           bool
		queueWhileRunning     bool
		insertIntoRunningTurn bool
		editingQueued         bool
		want                  sendDraftState
	}{
		{
			name:                  "insert_off_queue_on_keeps_queue",
			isRunning:             true,
			hasContent:            true,
			queueWhileRunning:     true,
			insertIntoRunningTurn: false,
			want:                  sendStateQueue,
		},
		{
			name:                  "insert_off_queue_off_keeps_steer",
			isRunning:             true,
			hasContent:            true,
			queueWhileRunning:     false,
			insertIntoRunningTurn: false,
			want:                  sendStateSteer,
		},
		{
			name:                  "insert_on_queue_on_injects",
			isRunning:             true,
			hasContent:            true,
			queueWhileRunning:     true,
			insertIntoRunningTurn: true,
			want:                  sendStateInject,
		},
		{
			name:                  "insert_on_queue_off_injects",
			isRunning:             true,
			hasContent:            true,
			queueWhileRunning:     false,
			insertIntoRunningTurn: true,
			want:                  sendStateInject,
		},
		{
			name:                  "insert_on_running_without_content_stops",
			isRunning:             true,
			hasContent:            false,
			queueWhileRunning:     true,
			insertIntoRunningTurn: true,
			want:                  sendStateStop,
		},
		{
			name:                  "insert_on_idle_with_content_sends",
			isRunning:             false,
			hasContent:            true,
			insertIntoRunningTurn: true,
			want:                  sendStateSend,
		},
		{
			name:                  "insert_on_idle_continue_stays_send",
			isRunning:             false,
			hasContent:            false,
			canContinue:           true,
			insertIntoRunningTurn: true,
			want:                  sendStateSend,
		},
		{
			name:                  "insert_on_editing_queued_stays_save_edit",
			isRunning:             true,
			hasContent:            true,
			queueWhileRunning:     true,
			insertIntoRunningTurn: true,
			editingQueued:         true,
			want:                  sendStateSaveEdit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runningSendDraftState(tt.isRunning, tt.hasContent, tt.canContinue, tt.queueWhileRunning, tt.insertIntoRunningTurn, tt.editingQueued)
			if got != tt.want {
				t.Fatalf("runningSendDraftState(%v, %v, %v, %v, %v, %v) = %v, want %v",
					tt.isRunning, tt.hasContent, tt.canContinue, tt.queueWhileRunning, tt.insertIntoRunningTurn, tt.editingQueued, got, tt.want)
			}
		})
	}
}

func TestCanRecallTurn_TableDriven(t *testing.T) {
	tests := []struct {
		name string
		msgs []Message
		want bool
	}{
		{
			name: "nil_messages",
			msgs: nil,
			want: false,
		},
		{
			name: "empty_messages",
			msgs: []Message{},
			want: false,
		},
		{
			name: "single_user_message",
			msgs: []Message{
				{Role: "user", Text: "hello world"},
			},
			want: true,
		},
		{
			name: "user_followed_by_assistant",
			msgs: []Message{
				{Role: "user", Text: "prompt"},
				{Role: "assistant", Text: "reply"},
			},
			want: false,
		},
		{
			name: "user_followed_by_tool",
			msgs: []Message{
				{Role: "user", Text: "prompt"},
				{Role: "tool", Text: "executing"},
			},
			want: false,
		},
		{
			name: "user_followed_by_reasoning",
			msgs: []Message{
				{Role: "user", Text: "prompt"},
				{Role: "reasoning", Text: "thinking"},
			},
			want: false,
		},
		{
			name: "user_followed_by_system",
			msgs: []Message{
				{Role: "user", Text: "prompt"},
				{Role: "system", Text: "system notice"},
			},
			want: false,
		},
		{
			name: "multi_turn_ending_with_user",
			msgs: []Message{
				{Role: "user", Text: "turn 1 prompt"},
				{Role: "assistant", Text: "turn 1 reply"},
				{Role: "user", Text: "turn 2 prompt"},
			},
			want: true,
		},
		{
			name: "multi_turn_ending_with_assistant",
			msgs: []Message{
				{Role: "user", Text: "turn 1 prompt"},
				{Role: "assistant", Text: "turn 1 reply"},
				{Role: "user", Text: "turn 2 prompt"},
				{Role: "assistant", Text: "turn 2 reply"},
			},
			want: false,
		},
		{
			name: "only_assistant_message",
			msgs: []Message{
				{Role: "assistant", Text: "welcome"},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := canRecallTurn(tt.msgs)
			if got != tt.want {
				t.Fatalf("canRecallTurn(%v) = %v, want %v", tt.msgs, got, tt.want)
			}
		})
	}
}
