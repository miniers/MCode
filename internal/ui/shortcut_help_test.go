package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// TestSettingsSearchHighlightPaintIsBorderPulse 设置搜索命中只画 accent 描边，不再铺选中底色（D-13）。
func TestSettingsSearchHighlightPaintIsBorderPulse(t *testing.T) {
	hit := settingsRowHighlightPaintFor("row-models", "row-models", 1)
	if hit.SelectedBackground {
		t.Fatal("搜索命中行不应使用 ThemeSelected 底色")
	}
	if hit.BorderWidth != 2 {
		t.Fatalf("命中行描边应为 2pt，实际 %v", hit.BorderWidth)
	}
	if hit.BorderAlpha != settingsSearchPulseMaxAlpha {
		t.Fatalf("脉冲峰值透明度应为 %v，实际 %v", settingsSearchPulseMaxAlpha, hit.BorderAlpha)
	}

	half := settingsRowHighlightPaintFor("row-models", "row-models", 0.5)
	if half.BorderAlpha != settingsSearchPulseMaxAlpha/2 {
		t.Fatalf("脉冲半程透明度应为 %v，实际 %v", settingsSearchPulseMaxAlpha/2, half.BorderAlpha)
	}

	if miss := settingsRowHighlightPaintFor("row-models", "row-other", 1); miss != (settingsRowHighlightPaint{}) {
		t.Fatalf("未命中高亮 key 时不应画描边，实际 %+v", miss)
	}
	if clamped := settingsRowHighlightPaintFor("row-models", "row-models", -3); clamped.BorderAlpha != 0 {
		t.Fatalf("脉冲进度应夹到 0，实际 %v", clamped.BorderAlpha)
	}
}

// TestSettingsSearchPulseKeyframesPlayTwice 脉冲时长 1.3s，透明度 0→1→0 播两次（D-13）。
func TestSettingsSearchPulseKeyframesPlayTwice(t *testing.T) {
	if settingsItemHighlightDuration != 1300*time.Millisecond {
		t.Fatalf("高亮时长应为 1.3s，实际 %v", settingsItemHighlightDuration)
	}
	if settingsSearchPulseStep*4 != settingsItemHighlightDuration {
		t.Fatalf("两轮脉冲总长应等于高亮时长：step=%v total=%v", settingsSearchPulseStep, settingsItemHighlightDuration)
	}

	frames := settingsSearchPulseKeyframes()
	wantValues := []float32{0, 1, 0, 1, 0}
	if len(frames) != len(wantValues) {
		t.Fatalf("脉冲应有 %d 帧，实际 %d 帧", len(wantValues), len(frames))
	}
	if frames[0].At != 0 {
		t.Fatalf("首帧必须在 0 时刻，实际 %v", frames[0].At)
	}
	for i, want := range wantValues {
		if frames[i].Value != want {
			t.Fatalf("第 %d 帧透明度应为 %v，实际 %v", i, want, frames[i].Value)
		}
		if i > 0 && frames[i].At <= frames[i-1].At {
			t.Fatalf("关键帧时刻必须严格递增：第 %d 帧 %v", i, frames[i].At)
		}
	}
	if last := frames[len(frames)-1].At; last != settingsItemHighlightDuration {
		t.Fatalf("末帧应停在 %v，实际 %v", settingsItemHighlightDuration, last)
	}
}

// TestShortcutHelpOverlaySearchAndOpenSettings ⌘/ 打开浮层，搜索过滤，点按钮跳到快捷键设置页（D-15）。
func TestShortcutHelpOverlaySearchAndOpenSettings(t *testing.T) {
	fake := NewFakeClient()
	state, view := buildRoot(fake, WithSettings(settingsTestDir(t), nil))
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.composerFocused
	})

	tt.Key(ui.Cmd, ui.KeySlash)
	st := getShortcutHelpState(state)
	waitFor(t, tt, func() bool {
		return st.IsOpen() &&
			tt.HasText("显示快捷键") &&
			tt.HasText("新建会话") &&
			tt.HasText("停止当前回复") &&
			tt.HasText("在设置中编辑")
	})

	tt.Click("搜索快捷键")
	tt.Type("新建")
	waitFor(t, tt, func() bool {
		return tt.HasText("新建会话") &&
			tt.HasText("在 Worktree 新建会话") &&
			!tt.HasText("停止当前回复") &&
			!tt.HasText("显示快捷键")
	})

	tt.Click("在设置中编辑")
	waitFor(t, tt, func() bool {
		settings := getChatSettings(state)
		if settings == nil {
			return false
		}
		settings.mu.Lock()
		page, sheet := settings.currentPage, settings.openSheet
		settings.mu.Unlock()
		return settings.IsOpen() && page == "general" && sheet == "shortcuts" && !st.IsOpen()
	})
}

// TestShortcutHelpHidesSettingsButtonWithoutSettingsState 未注入设置状态时，浮层不渲染「在设置中编辑」。
func TestShortcutHelpHidesSettingsButtonWithoutSettingsState(t *testing.T) {
	fake := NewFakeClient()
	state, view := buildRoot(fake)
	if getChatSettings(state) != nil {
		t.Fatal("未传 WithSettings 时不应有设置状态")
	}
	tt := ui.NewTester(view, 1000, 700)

	state.toggleShortcutHelp()
	st := getShortcutHelpState(state)
	waitFor(t, tt, func() bool {
		return st.IsOpen() && tt.HasText("显示快捷键")
	})
	if tt.HasText("在设置中编辑") {
		t.Fatal("没有设置状态时不应显示「在设置中编辑」按钮")
	}
}

// TestShortcutHelpGroupsKeepTableOrder 浮层分组保持快捷键表顺序，过滤后丢弃空分组。
func TestShortcutHelpGroupsKeepTableOrder(t *testing.T) {
	groups := shortcutHelpGroups(shortcutDefaults())
	if len(groups) < 2 || groups[0].name != "会话" || groups[1].name != "窗口" {
		t.Fatalf("分组顺序应与快捷键表一致，实际 %+v", groups)
	}
	filtered := filterShortcutHelpGroups(groups, "显示快捷键")
	if len(filtered) != 1 || filtered[0].name != "窗口" {
		t.Fatalf("过滤后应只剩窗口组，实际 %+v", filtered)
	}
	if len(filtered[0].items) != 1 || filtered[0].items[0].id != "show-shortcuts" {
		t.Fatalf("过滤后应只剩 show-shortcuts，实际 %+v", filtered[0].items)
	}
	if got := filterShortcutHelpGroups(groups, "不存在的快捷键"); len(got) != 0 {
		t.Fatalf("无命中时应返回空分组，实际 %+v", got)
	}
}

// TestShowShortcutsDefaultBindingIsConflictFree ⌘/ 默认绑定不与既有快捷键冲突（D-15）。
func TestShowShortcutsDefaultBindingIsConflictFree(t *testing.T) {
	withShortcutGOOS(t, "darwin")
	setShortcutOverrides(nil)

	if got := shortcutConflicts(effectiveShortcutBindings())["show-shortcuts"]; len(got) != 0 {
		t.Fatalf("show-shortcuts 默认绑定不应冲突，实际 %v", got)
	}
	mods, key, ok := shortcutBindingFor("show-shortcuts")
	if !ok || mods != ui.Cmd || key != ui.KeySlash {
		t.Fatalf("show-shortcuts 默认键应为 ⌘/，实际 mods=%v key=%v ok=%v", mods, key, ok)
	}
	if got := formatShortcutKeys(mods, key); got != "⌘/" {
		t.Fatalf("展示键位应为 ⌘/，实际 %q", got)
	}
	parsedMods, parsedKey, err := parseShortcutSpec("Cmd+/")
	if err != nil || parsedMods != ui.Cmd || parsedKey != ui.KeySlash {
		t.Fatalf("解析 Cmd+/ 应得到 ⌘/，实际 mods=%v key=%v err=%v", parsedMods, parsedKey, err)
	}
}
