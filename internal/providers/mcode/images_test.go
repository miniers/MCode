package mcode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/agent/llm"
	"mcode/internal/harness"
)

func TestAcceptsImages(t *testing.T) {
	image := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(image, []byte("image data"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		accepts   func(string) bool
		wantImage bool
	}{
		{"不支持", func(model string) bool { return model != "provider/model" }, false},
		{"未配置", nil, true},
		{"支持", func(string) bool { return true }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeClient{responses: []*llm.Response{answer()}}
			h := harnessFor(client, Options{AcceptsImages: tc.accepts})
			var events []harness.Event
			imagePath := image
			if !tc.wantImage {
				imagePath = filepath.Join(t.TempDir(), "missing.png")
			}
			err := h.Run(context.Background(), harness.Turn{Model: "provider/model", Prompt: "看看", Images: []string{imagePath}}, func(event harness.Event) { events = append(events, event) })
			if err != nil {
				t.Fatal(err)
			}
			if len(client.requests) != 1 {
				t.Fatalf("请求数 = %d", len(client.requests))
			}
			var hasImage, hasNotice bool
			for _, message := range client.requests[0].Messages {
				for _, part := range message.Content {
					hasImage = hasImage || part.Type == "image"
					hasNotice = hasNotice || strings.Contains(part.Text, "当前模型不支持图片，已忽略 1 张附图")
				}
			}
			if hasImage != tc.wantImage || hasNotice == tc.wantImage {
				t.Fatalf("图片 = %v，忽略说明 = %v", hasImage, hasNotice)
			}
			for _, event := range events {
				if event.Type == harness.EventError {
					t.Fatalf("意外错误：%s", event.Err)
				}
			}
		})
	}
}
