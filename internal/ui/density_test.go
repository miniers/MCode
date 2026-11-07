package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// TestCurrentDensity 覆盖密度读取：默认舒适档，配置成紧凑档时返回紧凑密度，
// 没有会话状态时同样按舒适档。
func TestCurrentDensity(t *testing.T) {
	state := &chatState{}
	st := &SettingsState{}
	setChatSettings(state, st)
	t.Cleanup(func() {
		settingsMu.Lock()
		delete(settingsList, state)
		settingsMu.Unlock()
	})

	if got := currentDensity(nil, state); got != DensityComfortable {
		t.Fatalf("默认密度 = %+v，期望舒适档", got)
	}
	st.cfg.Appearance.ConversationDensity = config.DensityCompact
	if got := currentDensity(nil, state); got != DensityCompact {
		t.Fatalf("紧凑配置下的密度 = %+v，期望紧凑档", got)
	}
	if got := currentDensity(nil, nil); got != DensityComfortable {
		t.Fatalf("没有会话状态时的密度 = %+v，期望舒适档", got)
	}
}

// TestSidebarRowHeightFollowsDensity 用 headless Tester 渲染侧栏：
// 紧凑密度下会话行比舒适档矮，行高与 Density.RowH 一致。
func TestSidebarRowHeightFollowsDensity(t *testing.T) {
	const title = "密度测试会话"
	rowHeight := func(t *testing.T, density string) float32 {
		t.Helper()
		fake := NewFakeClient()
		fake.threads["p1"] = []Thread{{ID: "t-density", ProjectID: "p1", Title: title, UpdatedAt: time.Now()}}
		state, view := buildRoot(fake)
		t.Cleanup(state.close)
		st := NewSettingsState(settingsTestDir(t), nil, state)
		st.cfg.Appearance.ConversationDensity = density
		setChatSettings(state, st)

		tt := ui.NewTester(view, 1000, 700)
		waitFor(t, tt, func() bool { return tt.HasText(title) })
		row, ok := tt.Find("打开会话 " + title)
		if !ok {
			t.Fatalf("找不到会话行: %v", tt.Texts())
		}
		return row.H
	}

	comfortable := rowHeight(t, config.DensityComfortable)
	compact := rowHeight(t, config.DensityCompact)
	if compact >= comfortable {
		t.Fatalf("紧凑会话行高 %.1f 应小于舒适 %.1f", compact, comfortable)
	}
	if want := DensityCompact.RowH * textScaleFactor(); compact != want {
		t.Fatalf("紧凑会话行高 = %.1f，期望 %.1f", compact, want)
	}
	if want := DensityComfortable.RowH * textScaleFactor(); comfortable != want {
		t.Fatalf("舒适会话行高 = %.1f，期望 %.1f", comfortable, want)
	}
}
