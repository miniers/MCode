package mcodeui

import (
	"testing"
	"time"
)

func TestSettingsErrorMessageUsesCfgMu(t *testing.T) {
	st := &SettingsState{}
	st.noteError("x")

	st.mu.Lock()
	defer st.mu.Unlock()

	got := make(chan string, 1)
	go func() {
		got <- swarmPairQuickSaveError(st)
	}()

	select {
	case msg := <-got:
		if msg != "x" {
			t.Fatalf("期望返回错误文案 %q，实际为 %q", "x", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("持住 st.mu 时调用 swarmPairQuickSaveError 在 1 秒内未返回")
	}
}
