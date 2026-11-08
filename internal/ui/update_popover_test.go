package mcodeui

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestShouldShowWhatsNew(t *testing.T) {
	tests := []struct {
		name, current, lastSeen, section string
		show, persist                    bool
	}{
		{"dev", "dev", "1.0", "内容", false, false},
		{"空版本", "", "1.0", "内容", false, false},
		{"首次安装", "2.0", "", "内容", false, true},
		{"版本相同", "2.0", "2.0", "内容", false, false},
		{"没有说明", "2.0", "1.0", "", false, true},
		{"版本更新", "2.0", "1.0", "内容", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			show, persist := shouldShowWhatsNew(tt.current, tt.lastSeen, tt.section)
			if show != tt.show || persist != tt.persist {
				t.Errorf("got (%v, %v), want (%v, %v)", show, persist, tt.show, tt.persist)
			}
		})
	}
}

// TestPersistLastSeenVersionWritesOnce 验证同一版本连续触发只起一次写盘 goroutine。
func TestPersistLastSeenVersionWritesOnce(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	t.Cleanup(func() {
		whatsNewPersistMu.Lock()
		delete(whatsNewPersisted, st.dataDir)
		whatsNewPersistMu.Unlock()
	})

	var saves atomic.Int64
	settingsSaveHook = func() { saves.Add(1) }
	t.Cleanup(func() { settingsSaveHook = nil })

	persistLastSeenVersion(st, "2.0")
	persistLastSeenVersion(st, "2.0")

	deadline := time.Now().Add(2 * time.Second)
	for saves.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	// 等写盘 goroutine 真正结束，避免临时目录在删除时还有写入。
	time.Sleep(20 * time.Millisecond)
	settingsSaveWriters.Wait()
	if got := saves.Load(); got != 1 {
		t.Fatalf("同一版本应只写盘一次，实际 %d 次", got)
	}
}

func TestUpdateButtonText(t *testing.T) {
	if got := updateButtonText(true); got != "更新并重启" {
		t.Errorf("installable: got %q", got)
	}
	if got := updateButtonText(false); got != "下载" {
		t.Errorf("not installable: got %q", got)
	}
}
