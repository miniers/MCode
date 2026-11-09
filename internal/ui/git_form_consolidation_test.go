package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestGitFormActionsOrderAndDisabled(t *testing.T) {
	for _, tc := range []struct {
		name     string
		busy     bool
		disabled bool
	}{
		{name: "busy", busy: true},
		{name: "disabled", disabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			submitted := 0
			tester := ui.NewTester(func(c *ui.Context) {
				renderGitFormActions(c, tc.busy, "执行", tc.disabled, "primary", func() { submitted++ }, nil)
			}, 400, 100)
			cancel, ok := tester.Find("取消")
			if !ok {
				t.Fatal("找不到取消按钮")
			}
			primary, ok := tester.Find("执行")
			if !ok || cancel.X >= primary.X {
				t.Fatalf("按钮顺序不正确：取消=%v，主按钮=%v", cancel, primary)
			}
			if err := tester.Click("执行"); err != nil {
				t.Fatal(err)
			}
			if submitted != 0 {
				t.Fatalf("禁用时提交了 %d 次", submitted)
			}
		})
	}
}

func TestGitFormEnterSubmitsLatestBranchName(t *testing.T) {
	fake := newGitFakeClient()
	s, _ := buildRoot(fake)
	f := &gitForm{s: s, threadID: "t1", kind: gitFormNewBranch}
	tester := gitFormTester(t, f)
	if err := tester.Click("新分支名"); err != nil {
		t.Fatal(err)
	}
	tester.Type("feature/latest")
	tester.Key(0, ui.KeyEnter)
	waitFor(t, tester, func() bool { return len(fake.GitCreateBranchCalls()) > 0 })
	calls := fake.GitCreateBranchCalls()
	if len(calls) != 1 || calls[0] != [2]string{"t1", "feature/latest"} {
		t.Fatalf("创建分支调用 = %v", calls)
	}
}

func TestGitFormEscapeClosesNewBranch(t *testing.T) {
	fake := newGitFakeClient()
	s, _ := buildRoot(fake)
	f := &gitForm{s: s, threadID: "escape-test", kind: gitFormNewBranch}
	key := gitFormKey{threadID: f.threadID, kind: f.kind}
	gitFormMu.Lock()
	gitFormWindows[key] = f
	gitFormMu.Unlock()
	t.Cleanup(func() {
		gitFormMu.Lock()
		delete(gitFormWindows, key)
		gitFormMu.Unlock()
	})
	tester := gitFormTester(t, f)
	if err := tester.Click("新分支名"); err != nil {
		t.Fatal(err)
	}
	tester.Key(0, ui.KeyEscape)
	tester.Frame()
	gitFormMu.Lock()
	_, open := gitFormWindows[key]
	gitFormMu.Unlock()
	if open {
		t.Fatal("Escape 后窗口仍在注册表中")
	}
}
