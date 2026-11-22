package mcodeui

import (
	"testing"
	"time"

	"mcode/internal/testenv"
)

func TestComposerPermissionCombo(t *testing.T) {
	testenv.TempHome(t)
	resetComposerModelPickerForTest(t)

	fake := NewFakeClient()
	s := newChatState(fake)
	t.Cleanup(s.close)

	tt := composerTester(s)
	waitFor(t, tt, func() bool {
		_, okMode := tt.Find("构建")
		_, okPerm := tt.Find("权限：自动")
		return okMode && okPerm
	})

	// 点击右段打开权限菜单，选中「完全访问」后会话权限变为 fullAccess 且浮层关闭。
	if err := tt.Click("权限：自动"); err != nil {
		t.Fatalf("点击权限按钮失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("完全访问")
		return ok && s.composerView.permissionMenuOpen
	})
	for _, title := range []string{"监督", "自动", "完全访问"} {
		if _, ok := tt.Find(title); !ok {
			t.Fatalf("权限浮层缺少选项 %q, texts=%v", title, tt.Texts())
		}
	}

	if err := tt.Click("完全访问"); err != nil {
		t.Fatalf("点击完全访问失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		tid, mode := lastRuntimeModeCall(fake)
		return tid == "t1" && mode == "fullAccess" && !s.composerView.permissionMenuOpen
	})
	waitFor(t, tt, func() bool {
		_, stillOpen := tt.Find("完全访问")
		_, updatedBtn := tt.Find("权限：完全访问")
		return !stillOpen && updatedBtn
	})
	if got := s.threadRuntimeMode("t1"); got != "fullAccess" {
		t.Fatalf("会话权限 = %q, want fullAccess", got)
	}

	// 切到完全访问后权限按钮保持方形单图标尺寸（不再渲染箭头），且无底色。
	tt.Move(0, 0)
	tt.Advance(200 * time.Millisecond)
	modeBounds, okMode := tt.Find("构建")
	permBounds, okPerm := tt.Find("权限：完全访问")
	if !okMode || !okPerm {
		t.Fatalf("找不到按钮 bounds: mode=%v perm=%v", okMode, okPerm)
	}
	if permBounds.W != composerActionBtnSize || permBounds.H != composerActionBtnSize {
		t.Fatalf("完全访问权限按钮应为方形单图标按钮（无箭头），bounds=%+v, want %vx%v", permBounds, composerActionBtnSize, composerActionBtnSize)
	}
	img := tt.Image()
	sample := func(x, y int) [3]int {
		i := img.PixOffset(x, y)
		return [3]int{int(img.Pix[i]), int(img.Pix[i+1]), int(img.Pix[i+2])}
	}
	modeBg := sample(int(modeBounds.X+2), int(modeBounds.Y+2))
	permCornerBg := sample(int(permBounds.X+2), int(permBounds.Y+2))
	permRightBg := sample(int(permBounds.X+permBounds.W-3), int(permBounds.Y+permBounds.H/2))
	if !colorNear(permCornerBg, modeBg, 2) || !colorNear(permRightBg, modeBg, 2) {
		t.Fatalf("完全访问权限按钮不应有底色或右侧箭头: modeBg=%v permCornerBg=%v permRightBg=%v", modeBg, permCornerBg, permRightBg)
	}

	// 左段构建/计划点击仍能切换模式。
	if err := tt.Click("构建"); err != nil {
		t.Fatalf("点击构建模式按钮失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		tid, prefs, ok := fake.LastComposerPrefsCall()
		return ok && tid == "t1" && prefs.InteractionMode == "plan"
	})
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("计划")
		return ok
	})
}
