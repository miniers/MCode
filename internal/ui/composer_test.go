package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestComposerRunningStewardDoesNotShowStop(t *testing.T) {
	for _, tc := range []struct {
		name      string
		stewardID string
		wantStop  bool
	}{
		{name: "总管", stewardID: "t1"},
		{name: "普通会话", stewardID: "other", wantStop: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newStewardFakeClient(tc.stewardID)
			s, view := buildRoot(fake, WithInitialThread("t1"))
			t.Cleanup(s.close)
			tt := ui.NewTester(view, 1000, 700)
			waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
			fake.TriggerRunningUpdate("t1", true)
			waitFor(t, tt, func() bool {
				s.mu.Lock()
				defer s.mu.Unlock()
				for _, thread := range s.threads {
					if thread.ID == "t1" {
						return thread.IsRunning
					}
				}
				return false
			})
			tt.Frame()
			if got := tt.HasText("停止"); got != tc.wantStop {
				t.Fatalf("停止按钮 = %v，期望 %v: %q", got, tc.wantStop, tt.Texts())
			}
		})
	}
}
