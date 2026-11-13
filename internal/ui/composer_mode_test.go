package mcodeui

import (
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
					renderInteractionModeToggle(c, s, tc.mode, false, radius)
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
