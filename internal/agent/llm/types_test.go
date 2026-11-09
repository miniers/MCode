package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestThinkingBlocksJSON(t *testing.T) {
	message := Message{Role: "assistant", ThinkingBlocks: []ThinkingBlock{
		{Type: "thinking", Thinking: "思考", Signature: "sig"},
		{Type: "redacted_thinking", Data: "secret"},
		{Type: "thinking", Signature: "empty"},
	}}
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Message
	if err := json.Unmarshal(data, &decoded); err != nil || !reflect.DeepEqual(decoded, message) {
		t.Fatalf("message: %+v, error: %v", decoded, err)
	}
}

func TestRetryAfterOf(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", &APIError{RetryAfter: 3 * time.Second})
	if RetryAfterOf(err) != 3*time.Second || RetryAfterOf(errors.New("other")) != 0 || RetryAfterOf(nil) != 0 {
		t.Fatal("RetryAfterOf")
	}
}

func TestIsContextOverflowRateLimits(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"429", fmt.Errorf("wrapped: %w", &APIError{Status: 429, Body: "too many tokens"}), false},
		{"529", &APIError{Status: 529, Body: "too many tokens"}, false},
		{"overflow", &APIError{Status: 400, Body: "prompt is too long"}, true},
		{"rate limit", &APIError{Status: 400, Body: "Rate limit reached for tokens per min"}, false},
		{"tpm", errors.New("too many tokens: TPM exceeded"), false},
		{"rpm", errors.New("too many tokens: RPM exceeded"), false},
		{"per minute", errors.New("too many tokens per minute"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsContextOverflow(tt.err); got != tt.want {
				t.Fatalf("IsContextOverflow(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
