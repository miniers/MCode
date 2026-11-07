package mcodeui

import (
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

// 场景 1：点击 Build 胶囊切换为 Plan，假 Client 收到 InteractionMode="plan"，界面显示 Plan。
func TestComposerPlan_ClickToggle(t *testing.T) {
	composerModelPickerState.MenuOpen = false
	fake := NewFakeClient()
	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})
	waitFor(t, tt, func() bool {
		return tt.HasText("构建")
	})

	if err := tt.Click("构建"); err != nil {
		t.Fatalf("click Build toggle failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		tid, prefs, ok := fake.LastComposerPrefsCall()
		return ok && tid == "t1" && prefs.InteractionMode == "plan"
	})
	waitFor(t, tt, func() bool {
		return tt.HasText("计划")
	})
}

func TestComposerPlan_SaveFailureBlocksSendAndRollsBack(t *testing.T) {
	composerModelPickerState.MenuOpen = false
	fake := NewFakeClient()
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	fake.composerPrefsHook = func(string, ComposerPrefs) error {
		<-release
		return errors.New("保存失败")
	}
	state, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("构建") })
	if err := tt.Click("构建"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return fake.ComposerPrefsCallCount() == 1 && tt.HasText("计划") })
	state.toggleInteractionMode()
	state.sendMessage("不能使用尚未保存的 Plan 偏好发送")
	if fake.ComposerPrefsCallCount() != 1 || fake.SendCalls() != 0 {
		t.Fatal("保存期间不应重复保存或发送")
	}
	close(release)
	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		for _, th := range state.threads {
			if th.ID == "t1" {
				return !state.composerPrefsSaving["t1"] && th.InteractionMode == "build"
			}
		}
		return false
	})
	waitFor(t, tt, func() bool { return tt.HasText("构建") && tt.HasText("切换 Build/Plan 模式失败: 保存失败") })
}

// 场景 2：⇧⌘P 在 build 与 plan 之间来回切换；toggleInteractionMode 再次切回 build。
func TestComposerPlan_ShortcutAndToggleBack(t *testing.T) {
	composerModelPickerState.MenuOpen = false
	fake := NewFakeClient()
	state, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1") && tt.HasText("构建")
	})

	tt.Key(ui.Cmd|ui.Shift, ui.KeyP)
	waitFor(t, tt, func() bool {
		tid, prefs, ok := fake.LastComposerPrefsCall()
		return ok && tid == "t1" && prefs.InteractionMode == "plan"
	})
	waitFor(t, tt, func() bool {
		return tt.HasText("计划")
	})

	state.toggleInteractionMode()
	waitFor(t, tt, func() bool {
		_, prefs, ok := fake.LastComposerPrefsCall()
		return ok && prefs.InteractionMode == "build" && fake.ComposerPrefsCallCount() == 2
	})
	waitFor(t, tt, func() bool {
		return tt.HasText("构建")
	})

	// ⇧⌘P 不再切换预览
	if pv := getPreviewState(state); pv != nil && pv.visible {
		t.Fatalf("⇧⌘P should no longer toggle preview")
	}
}

// 场景 3：切换模式时保留会话已有的 effort 与 Bees 配置。
func TestComposerPlan_TogglePreservesOtherPrefs(t *testing.T) {
	composerModelPickerState.MenuOpen = false
	fake := NewFakeClient()
	fake.mu.Lock()
	for _, list := range fake.threads {
		for i := range list {
			if list[i].ID == "t1" {
				list[i].Effort = "high"
				list[i].BeesProvider = "codex"
				list[i].BeesModel = "o3-mini"
				list[i].BeesEffort = "low"
			}
		}
	}
	fake.mu.Unlock()
	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1") && tt.HasText("构建")
	})
	if err := tt.Click("构建"); err != nil {
		t.Fatalf("click Build toggle failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, prefs, ok := fake.LastComposerPrefsCall()
		return ok && prefs == ComposerPrefs{
			InteractionMode: "plan",
			Effort:          "high",
			BeesProvider:    "codex",
			BeesModel:       "o3-mini",
			BeesEffort:      "low",
		}
	})
}

// 场景 4：两种模式下占位符不同；禁用原因优先级最高，编辑排队消息有独立文案。
func TestComposerPlan_Placeholder(t *testing.T) {
	build := composerPlaceholder("build", false, sendStateSend, "", config.ComposerSendKeyEnter)
	if build != "输入消息，Enter 发送，Shift+Enter 换行" {
		t.Fatalf("unexpected build placeholder: %q", build)
	}
	if got := composerPlaceholder("", false, sendStateSend, "", config.ComposerSendKeyEnter); got != build {
		t.Fatalf("empty mode should fall back to build placeholder, got %q", got)
	}
	if got := composerPlaceholder("plan", false, sendStateSend, "", config.ComposerSendKeyEnter); got != "描述你要规划的任务，Enter 发送，Shift+Enter 换行" {
		t.Fatalf("unexpected plan placeholder: %q", got)
	}

	// 编辑排队消息时不分模式、不分是否运行中，统一提示 Enter 保存、Esc 取消。
	cases := []struct {
		mode    string
		running bool
	}{
		{"build", false},
		{"build", true},
		{"plan", true},
	}
	for _, tc := range cases {
		if got := composerPlaceholder(tc.mode, tc.running, sendStateSaveEdit, "", config.ComposerSendKeyEnter); got != "编辑排队消息 · Enter 保存，Esc 取消" {
			t.Fatalf("save-edit placeholder (mode=%q running=%v): %q", tc.mode, tc.running, got)
		}
	}

	// 禁用原因优先级最高：运行中与编辑排队都不能盖掉它。
	if got := composerPlaceholder("build", true, sendStateQueue, "存在待处理的审批请求", config.ComposerSendKeyEnter); got != "存在待处理的审批请求" {
		t.Fatalf("disabled reason should win over running placeholder, got %q", got)
	}
	if got := composerPlaceholder("plan", false, sendStateSaveEdit, "存在待处理的审批请求", config.ComposerSendKeyEnter); got != "存在待处理的审批请求" {
		t.Fatalf("disabled reason should win over save-edit placeholder, got %q", got)
	}
}

// 场景 4b：回合结束但有后台任务在跑时，占位文字换成等待文案；不等待时原样返回。
func TestComposerWaitingPlaceholder(t *testing.T) {
	base := composerPlaceholder("build", false, sendStateSend, "", config.ComposerSendKeyEnter)
	if got := composerWaitingPlaceholder(base, true); got != "后台任务运行中，完成后自动继续；也可以直接输入" {
		t.Fatalf("等待后台任务时的占位文字 = %q", got)
	}
	if got := composerWaitingPlaceholder(base, false); got != base {
		t.Fatalf("不等待时应原样返回 base, got %q", got)
	}
}

// 场景 5：切到 Shift+Enter 发送后，所有带发送键的文案都把两个键互换。
func TestComposerPlan_PlaceholderShiftEnterSend(t *testing.T) {
	const key = config.ComposerSendKeyShiftEnter

	cases := []struct {
		name string
		got  string
		want string
	}{
		{"build", composerPlaceholder("build", false, sendStateSend, "", key), "输入消息，Shift+Enter 发送，Enter 换行"},
		{"plan", composerPlaceholder("plan", false, sendStateSend, "", key), "描述你要规划的任务，Shift+Enter 发送，Enter 换行"},
		{"save-edit", composerPlaceholder("build", false, sendStateSaveEdit, "", key), "编辑排队消息 · Shift+Enter 保存，Esc 取消"},
		{"queue", composerPlaceholder("build", true, sendStateQueue, "", key), "回复中 · Shift+Enter 加入队列，⌘. 停止"},
		{"steer", composerPlaceholder("build", true, sendStateSteer, "", key), "回复中 · Shift+Enter 打断并发送，⌘. 停止"},
		{"inject", composerPlaceholder("build", true, sendStateInject, "", key), "回复中 · Shift+Enter 插入当前回合，⌘. 停止"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, tc.got, tc.want)
		}
	}

	// 非法发送键按 Enter 发送处理，文案与默认一致。
	if got := composerPlaceholder("build", false, sendStateSend, "", "shift"); got != "输入消息，Enter 发送，Shift+Enter 换行" {
		t.Fatalf("unknown send key should fall back to Enter send, got %q", got)
	}
}
