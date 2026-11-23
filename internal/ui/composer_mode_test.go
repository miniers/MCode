package mcodeui

import (
	"image"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// runtimeModeTitle 把权限档位翻成中文：readonly 显示「只读」，空值回退「自动」，
// 未知档位不把原始英文模式名回显给用户。
func TestRuntimeModeTitle(t *testing.T) {
	for _, tc := range []struct{ mode, want string }{
		{mode: "", want: "自动"},
		{mode: "autoAcceptEdits", want: "自动"},
		{mode: "agent", want: "自动"},
		{mode: "auto", want: "自动"},
		{mode: "fullAccess", want: "完全访问"},
		{mode: "readonly", want: "只读"},
		{mode: "futureMode", want: "自定义"},
	} {
		if got := runtimeModeTitle(tc.mode); got != tc.want {
			t.Fatalf("runtimeModeTitle(%q) = %q，期望 %q", tc.mode, got, tc.want)
		}
	}
}

func TestInteractionModeIcon(t *testing.T) {
	build := interactionModeIcon("build")
	plan := interactionModeIcon("plan")
	if build == nil || plan == nil {
		t.Fatal("构建和计划模式图标均不能为空")
	}
	if build == plan {
		t.Fatal("构建和计划模式图标应不同")
	}
	for _, name := range []string{"hammer", "list-checks"} {
		if IconByName(name) == nil {
			t.Errorf("图标 %q 不能为空", name)
		}
	}
}

func TestInteractionModeToggleIconAndTooltip(t *testing.T) {
	for _, width := range []int{600, 320} {
		for _, tc := range []struct{ mode, title string }{
			{"build", "构建"},
			{"plan", "计划"},
		} {
			t.Run(composerChinTierName(width)+tc.title, func(t *testing.T) {
				s := newChatState(NewFakeClient())
				t.Cleanup(s.close)
				_, radius := composerShellRadii(false)
				tt := ui.NewTester(func(c *ui.Context) {
					renderModePermissionCombo(c, s, "t1", tc.mode, false, radius)
				}, width, 400)
				bounds, found := tt.Find(tc.title)
				if !found {
					t.Fatalf("找不到模式按钮 %s", tc.title)
				}
				if bounds.W != composerActionBtnSize || bounds.H != composerActionBtnSize {
					t.Fatalf("模式按钮应为方形图标按钮，bounds=%+v", bounds)
				}
				tt.Move(bounds.X+bounds.W/2, bounds.Y+bounds.H/2)
				tt.Advance(time.Second)
				if want := tc.title + " · " + shortcutDisplay("⇧⌘M"); !tt.HasText(want) {
					t.Fatalf("缺少提示 %q: %v", want, tt.Texts())
				}
			})
		}
	}
}

// lastRuntimeModeCall 获取假 Client 最后一次收到的 SetThreadRuntimeMode 调用信息。
func lastRuntimeModeCall(fake *FakeClient) (string, string) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.setRuntimeModeCalls) == 0 {
		return "", ""
	}
	last := fake.setRuntimeModeCalls[len(fake.setRuntimeModeCalls)-1]
	return last.ThreadID, last.Mode
}

func rectPixelsNear(t *testing.T, img *image.RGBA, a, b ui.Rect, tol int) bool {
	t.Helper()
	w := int(a.W)
	h := int(a.H)
	if int(b.W) != w || int(b.H) != h {
		t.Fatalf("对照矩形尺寸不一致: %+v vs %+v", a, b)
	}
	for dy := 0; dy < h; dy++ {
		for dx := 0; dx < w; dx++ {
			ia := img.PixOffset(int(a.X)+dx, int(a.Y)+dy)
			ib := img.PixOffset(int(b.X)+dx, int(b.Y)+dy)
			pa := [3]int{int(img.Pix[ia]), int(img.Pix[ia+1]), int(img.Pix[ia+2])}
			pb := [3]int{int(img.Pix[ib]), int(img.Pix[ib+1]), int(img.Pix[ib+2])}
			if !colorNear(pa, pb, tol) {
				return false
			}
		}
	}
	return true
}

func TestComposerModePermissionQuietColors(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	s.mu.Lock()
	s.threads = []Thread{{ID: "t1", RuntimeMode: "fullAccess", InteractionMode: "build"}}
	s.mu.Unlock()
	_, radius := composerShellRadii(false)

	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Gap(Space2).Children(func() {
			renderModePermissionCombo(c, s, "t1", "build", false, radius)
			ui.Row(c).Gap(Space1).Children(func() {
				ui.ButtonBase(c.Key("ref-mode-tertiary")).
					Label("ref-mode-tertiary").
					Size(composerActionBtnSize, composerActionBtnSize).
					Radius(radius).
					Children(func() {
						ui.Icon(c, interactionModeIcon("build")).Size(IconSM, IconSM).TextColor(ThemeTertiaryText(c))
					})
				ui.ButtonBase(c.Key("ref-mode-warning")).
					Label("ref-mode-warning").
					Size(composerActionBtnSize, composerActionBtnSize).
					Radius(radius).
					Children(func() {
						ui.Icon(c, interactionModeIcon("build")).Size(IconSM, IconSM).TextColor(ThemeWarning(c))
					})
				ui.ButtonBase(c.Key("ref-perm-tertiary")).
					Label("ref-perm-tertiary").
					Size(composerActionBtnSize, composerActionBtnSize).
					Radius(radius).
					Children(func() {
						ui.Icon(c, composerPermissionIcon("fullAccess")).Size(IconSM, IconSM).TextColor(ThemeTertiaryText(c))
					})
				ui.ButtonBase(c.Key("ref-perm-warning")).
					Label("ref-perm-warning").
					Size(composerActionBtnSize, composerActionBtnSize).
					Radius(radius).
					Children(func() {
						ui.Icon(c, composerPermissionIcon("fullAccess")).Size(IconSM, IconSM).TextColor(ThemeWarning(c))
					})
			})
		})
	}, 400, 200)

	tt.Move(390, 190)
	tt.Advance(200 * time.Millisecond)

	modeRect, ok := tt.Find("构建")
	if !ok {
		t.Fatal("找不到构建按钮")
	}
	permRect, ok := tt.Find("权限：完全访问")
	if !ok {
		t.Fatal("找不到完全访问权限按钮")
	}
	refModeTertiary, ok := tt.Find("ref-mode-tertiary")
	if !ok {
		t.Fatal("找不到 ref-mode-tertiary")
	}
	refModeWarning, ok := tt.Find("ref-mode-warning")
	if !ok {
		t.Fatal("找不到 ref-mode-warning")
	}
	refPermTertiary, ok := tt.Find("ref-perm-tertiary")
	if !ok {
		t.Fatal("找不到 ref-perm-tertiary")
	}
	refPermWarning, ok := tt.Find("ref-perm-warning")
	if !ok {
		t.Fatal("找不到 ref-perm-warning")
	}

	img := tt.Image()
	if rectPixelsNear(t, img, refModeTertiary, refModeWarning, 2) {
		t.Fatal("对照图标 ThemeTertiaryText 与 ThemeWarning 应有明显色差")
	}
	if rectPixelsNear(t, img, refPermTertiary, refPermWarning, 2) {
		t.Fatal("对照权限图标 ThemeTertiaryText 与 ThemeWarning 应有明显色差")
	}
	if !rectPixelsNear(t, img, modeRect, refModeTertiary, 2) {
		t.Fatal("构建按钮未悬停时图标颜色应等于 ThemeTertiaryText")
	}
	if rectPixelsNear(t, img, modeRect, refModeWarning, 2) {
		t.Fatal("构建按钮未悬停时图标颜色不应等于 ThemeWarning")
	}
	if !rectPixelsNear(t, img, permRect, refPermTertiary, 2) {
		t.Fatal("完全访问权限按钮未悬停时图标颜色应等于 ThemeTertiaryText")
	}
	if rectPixelsNear(t, img, permRect, refPermWarning, 2) {
		t.Fatal("完全访问权限按钮未悬停时图标颜色不应等于 ThemeWarning")
	}
}
