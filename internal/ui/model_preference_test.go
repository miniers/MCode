package mcodeui

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

type preferenceTestClient struct {
	Client
	read  func(context.Context, ModelPin) (ModelPreference, error)
	write func(context.Context, ModelPin, ModelPreference) error
}

func (c preferenceTestClient) ModelPreferences(ctx context.Context, pin ModelPin) (ModelPreference, error) {
	if c.read != nil {
		return c.read(ctx, pin)
	}
	return c.Client.ModelPreferences(ctx, pin)
}

func (c preferenceTestClient) UpdateModelPreference(ctx context.Context, pin ModelPin, pref ModelPreference) error {
	if c.write != nil {
		return c.write(ctx, pin, pref)
	}
	return c.Client.UpdateModelPreference(ctx, pin, pref)
}

func awaitPreferenceWrites(t *testing.T, s *chatState, pin ModelPin) {
	t.Helper()
	s.mu.Lock()
	done := s.modelPrefWrites[pin.Key()]
	s.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("model preference write did not finish")
		}
	}
}

func TestModelPreferenceOrderedWrites(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	pin := modelPreferencePin("claude", "")
	if pin.ModelID != "default" {
		t.Fatalf("default model pin = %+v", pin)
	}
	effort := "medium"
	if err := fake.UpdateModelPreference(context.Background(), pin, ModelPreference{Effort: &effort}); err != nil {
		t.Fatal(err)
	}
	s := newChatState(fake)
	s.toggleModelFastMode(pin)
	s.setModelEffortPreference(pin, "high")
	awaitPreferenceWrites(t, s, pin)
	pref, _ := fake.ModelPreferences(context.Background(), pin)
	if pref.Effort == nil || *pref.Effort != "high" || !pref.FastMode {
		t.Fatalf("ordered edits lost a field: %+v", pref)
	}
	s.resetModelPreference("", "lead", pin)
	s.toggleModelFastMode(pin)
	awaitPreferenceWrites(t, s, pin)
	pref, _ = fake.ModelPreferences(context.Background(), pin)
	if pref.Effort != nil || !pref.FastMode {
		t.Fatalf("reset overtook a later edit: %+v", pref)
	}
}

func TestModelPreferenceLoadCannotOverwriteWrite(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	started, release := make(chan struct{}), make(chan struct{})
	var reads atomic.Int32
	client := preferenceTestClient{
		Client: fake,
		read: func(ctx context.Context, pin ModelPin) (ModelPreference, error) {
			if reads.Add(1) == 1 {
				close(started)
				select {
				case <-release:
					return ModelPreference{}, nil
				case <-ctx.Done():
					return ModelPreference{}, ctx.Err()
				}
			}
			return fake.ModelPreferences(ctx, pin)
		},
	}
	pin := ModelPin{Provider: "claude", ModelID: "opus"}
	s := newChatState(client)
	s.loadModelPreference(pin)
	<-started
	s.toggleModelFastMode(pin)
	awaitPreferenceWrites(t, s, pin)
	close(release)
	tt := ui.NewTester(func(c *ui.Context) {}, 100, 100)
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.modelPrefLoading[pin.Key()]
	})
	if !s.modelPreference(pin).FastMode {
		t.Fatal("stale load overwrote the saved fast mode")
	}
}

func TestModelPreferenceWriteFailurePreservesCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := preferenceTestClient{
		Client: NewFakeClient(),
		write: func(context.Context, ModelPin, ModelPreference) error {
			return errors.New("write failed")
		},
	}
	s := newChatState(client)
	pin := ModelPin{Provider: "claude", ModelID: "opus"}
	effort := "high"
	s.modelPrefCache[pin.Key()] = ModelPreference{Effort: &effort}
	s.toggleModelFastMode(pin)
	awaitPreferenceWrites(t, s, pin)
	pref := s.modelPreference(pin)
	if pref.FastMode || pref.Effort == nil || *pref.Effort != effort || s.modelPreferenceBusy(pin) {
		t.Fatalf("failed save changed cache or left controls busy: %+v", pref)
	}
}

// 覆盖：选中会话的 Lead 模型与 pin 匹配时，toggleModelFastMode 同时把快速模式写穿到该会话。
func TestToggleModelFastModeSyncsSelectedThread(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	s := newChatState(fake)
	pin := modelPreferencePin("claude", "")
	s.threads = []Thread{{ID: "t1", ProjectID: "p1", Provider: "claude"}}
	s.threadsByProj = map[string][]Thread{"p1": {{ID: "t1", ProjectID: "p1", Provider: "claude"}}}
	s.selectedThreadID = "t1"

	s.toggleModelFastMode(pin)
	awaitPreferenceWrites(t, s, pin)

	call, ok := fake.LastFastModeCall()
	if !ok || call.ThreadID != "t1" || !call.FastMode || call.SpeedTier != "" {
		t.Fatalf("unexpected SetThreadFastMode call: %+v ok=%v", call, ok)
	}
	s.mu.Lock()
	flat, grouped := s.threads[0], s.threadsByProj["p1"][0]
	s.mu.Unlock()
	if !flat.FastMode || flat.SpeedTier != "" || !grouped.FastMode || grouped.SpeedTier != "" {
		t.Fatalf("local thread fast mode not updated: %+v / %+v", flat, grouped)
	}
}

// 覆盖：选中会话的 Lead 模型与 pin 不匹配时，toggleModelFastMode 不写穿快速模式。
func TestToggleModelFastModeSkipsMismatchedThread(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	s := newChatState(fake)
	pin := ModelPin{Provider: "codex", ModelID: "o3-mini"}
	s.threads = []Thread{{ID: "t1", ProjectID: "p1", Provider: "claude", Model: "opus"}}
	s.selectedThreadID = "t1"

	s.toggleModelFastMode(pin)
	awaitPreferenceWrites(t, s, pin)
	if n := fake.FastModeCallCount(); n != 0 {
		t.Fatalf("SetThreadFastMode called %d times, want 0", n)
	}
}
