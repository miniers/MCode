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
