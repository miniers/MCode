package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

func TestEmbeddedBeeComposerHidesModePermission(t *testing.T) {
	testenv.TempHome(t)
	resetComposerModelPickerForTest(t)

	// 普通输入框应正常渲染模式切换与权限切换控件。
	plainFake := NewFakeClient()
	plain := newChatState(plainFake)
	t.Cleanup(plain.close)

	plainTT := composerTester(plain)
	waitFor(t, plainTT, func() bool {
		_, okMode := plainTT.Find("构建")
		_, okPerm := plainTT.Find("权限：自动")
		return okMode && okPerm
	})

	// embeddedBee 输入框隐藏模式与权限控件，同时保留其余底栏控件。
	beeFake := NewFakeClient()
	bee := newChatState(beeFake)
	bee.embeddedBee = true
	bee.embeddedPicker = &ModelPickerState{}
	t.Cleanup(func() {
		releaseEmbeddedBeeState(bee)
	})

	beeTT := composerTester(bee)
	waitFor(t, beeTT, func() bool {
		_, okAttach := beeTT.Find("添加附件")
		_, okMeter := beeTT.Find("上下文用量")
		_, okSend := beeTT.Find("发送")
		return okAttach && okMeter && okSend
	})

	for _, label := range []string{"构建", "计划", "权限：自动", "权限：监督", "权限：完全访问"} {
		if _, found := beeTT.Find(label); found {
			t.Fatalf("embeddedBee 输入框不应渲染模式或权限控件 %q", label)
		}
	}

	// 聚焦 embeddedBee 输入框后按 ⌘⇧M，交互模式保持不变。
	bee.mu.Lock()
	bee.composerFocusRequest = true
	bee.mu.Unlock()
	waitFor(t, beeTT, func() bool {
		bee.mu.Lock()
		focused := bee.composerFocused
		bee.mu.Unlock()
		return focused && composerOwnsShortcuts(bee)
	})

	beeTT.Key(ui.Cmd|ui.Shift, ui.KeyM)
	beeTT.Frame()
	if beeFake.ComposerPrefsCallCount() != 0 {
		t.Fatalf("embeddedBee 输入框按 ⌘⇧M 不应切换交互模式，调用次数=%d", beeFake.ComposerPrefsCallCount())
	}
	bee.mu.Lock()
	gotMode := bee.threads[0].InteractionMode
	bee.mu.Unlock()
	if normalizeInteractionMode(gotMode) != "build" {
		t.Fatalf("embeddedBee 输入框按 ⌘⇧M 后交互模式=%q，want build", gotMode)
	}
}
