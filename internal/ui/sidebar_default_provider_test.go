package mcodeui

import (
	"testing"
	"time"
)

func TestDefaultNewThreadProviderRecentThreads(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	providers := []ProviderInfo{
		{ID: "deepseek", Available: true},
		{ID: "custom-x", Available: true},
		{ID: "mcode", Available: true},
		{ID: "unavailable"},
	}
	tests := []struct {
		name    string
		threads []Thread
		steward string
		want    string
	}{
		{"最近使用", []Thread{{Provider: "custom-x", UpdatedAt: now}, {Provider: "deepseek", UpdatedAt: now.Add(-time.Hour)}}, "", "custom-x"},
		{"跳过 mcode", []Thread{{Provider: "mcode", UpdatedAt: now}, {Provider: "custom-x", UpdatedAt: now.Add(-time.Hour)}}, "", "custom-x"},
		{"不可用时回退", []Thread{{Provider: "unavailable", UpdatedAt: now}}, "", "deepseek"},
		{"跳过 helper", []Thread{{Provider: "custom-x", IsHelper: true, UpdatedAt: now}}, "", "deepseek"},
		{"跳过总管", []Thread{{ID: "steward", Provider: "custom-x", UpdatedAt: now}}, "steward", "deepseek"},
		{"没有会话", nil, "", "deepseek"},
		{"按用户活动排序", []Thread{{Provider: "custom-x", UpdatedAt: now, LastUserMessageAt: now.Add(-2 * time.Hour)}, {Provider: "deepseek", UpdatedAt: now.Add(-time.Hour)}}, "", "deepseek"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := defaultNewThreadProvider(providers, newThreadProviderContext{tt.threads, tt.steward})
			if got != tt.want {
				t.Fatalf("默认来源 = %q，期望 %q", got, tt.want)
			}
		})
	}
}

func TestDefaultNewThreadProviderFallback(t *testing.T) {
	providers := []ProviderInfo{{ID: "mcode", Available: true}, {ID: "custom-x"}}
	if got := defaultNewThreadProvider(providers); got != "custom-x" {
		t.Fatalf("默认来源 = %q，期望 custom-x", got)
	}
	if got := defaultNewThreadProvider(providers[:1]); got != "" {
		t.Fatalf("默认来源 = %q，期望为空", got)
	}
}

func TestRefreshCustomEndpointProvidersUsesRecentThread(t *testing.T) {
	fake := NewFakeClient()
	fake.SetProviders([]ProviderInfo{{ID: "deepseek", Available: true}, {ID: "custom-x", Available: true}})
	chat := &chatState{
		client:            fake,
		newThreadProvider: "deleted",
		threads:           []Thread{{Provider: "custom-x", UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}},
	}
	st := NewSettingsState(settingsTestDir(t), nil, chat)
	refreshCustomEndpointProviders(st, fake)
	chat.mu.Lock()
	defer chat.mu.Unlock()
	if chat.newThreadProvider != "custom-x" {
		t.Fatalf("刷新后默认来源 = %q，期望 custom-x", chat.newThreadProvider)
	}
}
