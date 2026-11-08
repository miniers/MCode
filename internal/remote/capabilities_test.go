package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"mcode/internal/config"
	"mcode/internal/remote/wire"
)

func TestDefaultFeaturesIncludesTransportTailscale(t *testing.T) {
	features := DefaultFeatures()
	if len(features) == 0 {
		t.Fatal("DefaultFeatures() returned no features")
	}
	if last := features[len(features)-1]; last != "transport.tailscale" {
		t.Fatalf("expected last feature to be %q (appended at the end), got %q", "transport.tailscale", last)
	}
}

// Capabilities 要跟随桌面端配置的附件上限，而不是写死默认值。
func TestCapabilitiesAttachmentLimitFollowsConfig(t *testing.T) {
	backend := newProjectExtTestBackend(t)
	dataDir := backend.AgentConfDataDir()
	setLimit := func(limit int) {
		t.Helper()
		if err := config.Update(dataDir, func(cfg *config.Config) error {
			cfg.Session.ComposerAttachmentLimit = limit
			return nil
		}); err != nil {
			t.Fatalf("update config: %v", err)
		}
	}

	setLimit(10)
	caps, err := backend.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	if caps.AttachmentLimit != 10 {
		t.Fatalf("attachmentLimit = %d, want 10", caps.AttachmentLimit)
	}

	// 换成非默认值，确认真正读取了配置。
	setLimit(7)
	caps, err = backend.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	if caps.AttachmentLimit != 7 {
		t.Fatalf("attachmentLimit = %d, want 7", caps.AttachmentLimit)
	}
}

type attachmentLimitTestBackend struct {
	*fakeBackend
	limit int
}

func (b *attachmentLimitTestBackend) attachmentLimit() int { return b.limit }

// Dispatcher 的附件校验要使用后端声明的上限：10 时接受 6 个，超过 10 才拒绝。
func TestDispatcherAttachmentLimit(t *testing.T) {
	backend := &attachmentLimitTestBackend{fakeBackend: &fakeBackend{}, limit: 10}
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-1"}}
	threadID := "11111111-1111-4111-8111-111111111111"
	actionID := "22222222-2222-4222-8222-222222222222"

	send := func(count int) wire.Response {
		t.Helper()
		attachments := make([]string, count)
		for i := range attachments {
			attachments[i] = fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
		}
		params, err := json.Marshal(map[string]any{
			"threadID":    threadID,
			"actionID":    actionID,
			"text":        "hi",
			"attachments": attachments,
		})
		if err != nil {
			t.Fatalf("marshal params: %v", err)
		}
		raw, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"method":  "threads.sendMessage",
			"params":  json.RawMessage(params),
			"id":      1,
		})
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		return callExtension(t, d, conn, string(raw))
	}

	if resp := send(6); resp.Error != nil {
		t.Fatalf("6 attachments should be accepted: %+v", resp.Error)
	}
	if resp := send(11); resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("11 attachments should be rejected with InvalidParams, got %+v", resp.Error)
	}
}
