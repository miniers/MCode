package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/providers/agy/quota"
	"mcode/internal/testenv"
)

func agyAccountTestWindows(acc2FiveHourUsed float64) []ProviderQuotaWindow {
	return []ProviderQuotaWindow{
		{
			Label:       "个人账号 · " + quota.GroupGemini,
			Account:     "个人账号",
			AccountID:   "acc-1",
			Period:      "5h",
			UsedPercent: 18,
			Active:      true,
		},
		{
			Label:       "个人账号 · " + quota.GroupGemini + " 7d",
			Account:     "个人账号",
			AccountID:   "acc-1",
			Period:      "7d",
			UsedPercent: 25,
			Active:      true,
		},
		{
			Label:       "工作账号 · " + quota.GroupGemini,
			Account:     "工作账号",
			AccountID:   "acc-2",
			Period:      "5h",
			UsedPercent: acc2FiveHourUsed,
		},
		{
			Label:       "工作账号 · " + quota.GroupGemini + " 7d",
			Account:     "工作账号",
			AccountID:   "acc-2",
			Period:      "7d",
			UsedPercent: 40,
		},
	}
}

func newAgyAccountTestState(t *testing.T, th Thread, windows []ProviderQuotaWindow) (*FakeClient, *chatState) {
	t.Helper()
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.threads["p1"] = []Thread{th}
	fake.providerQuotaResult = append([]ProviderQuotaWindow(nil), windows...)

	s := newChatState(fake)
	t.Cleanup(s.close)
	s.selectedThreadID = th.ID
	s.threads = []Thread{th}
	s.threadsByProj = map[string][]Thread{"p1": {th}}
	SetChatQuotaWindowsForProvider(s, th.Provider, windows)
	t.Cleanup(func() { SetChatQuotaWindowsForProvider(s, th.Provider, nil) })
	return fake, s
}

func currentTestThreadSnapshot(s *chatState, threadID string, fallback Thread) *Thread {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.threads {
		if s.threads[i].ID == threadID {
			cp := s.threads[i]
			return &cp
		}
	}
	cp := fallback
	return &cp
}

func TestComposerAgyAccountPopoverListsAutoAndAccounts(t *testing.T) {
	th := Thread{ID: "t1", ProjectID: "p1", Provider: "agy"}
	_, s := newAgyAccountTestState(t, th, agyAccountTestWindows(32))

	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("agy-account-host")).Fill().Children(func() {
			ui.Box(c.Key("agy-account-spacer")).Grow(1)
			renderComposerQuotaStrip(c, s, currentTestThreadSnapshot(s, th.ID, th))
		})
	}, 520, 480)

	// 自动模式下额度条不展示账号名，只展示默认 Active 账号的读数。
	if tt.HasText("个人账号") || tt.HasText("工作账号") {
		t.Fatalf("自动模式额度条不应显示账号名，texts=%q", tt.Texts())
	}
	if !tt.HasText("18%") {
		t.Fatalf("自动模式额度条应显示当前在用账号读数，texts=%q", tt.Texts())
	}

	if err := tt.Click("额度 agy"); err != nil {
		t.Fatalf("打开 agy 额度弹层失败: %v", err)
	}
	// 等 PopoverCard 入场淡入动画（DurBase）结束后再取像素断言勾选图标。
	tt.Advance(DurBase + 50*time.Millisecond)
	tt.Frame()
	for _, want := range []string{"自动", "个人账号", "工作账号", "18%", "32%"} {
		if !tt.HasText(want) {
			t.Fatalf("弹层缺少 %q，texts=%q", want, tt.Texts())
		}
	}

	autoBtn, ok := tt.AccessByRole(ui.RoleButton, "自动")
	if !ok {
		t.Fatalf("找不到「自动」选项按钮")
	}
	if got := countRightSlotInk(tt.Image(), autoBtn.Bounds); got == 0 {
		t.Fatalf("Account 为空时应勾选「自动」，右侧像素=%d", got)
	}
}

func TestComposerAgyAccountSelectSwitchesAccountAndStripReadout(t *testing.T) {
	th := Thread{ID: "t1", ProjectID: "p1", Provider: "agy"}
	fake, s := newAgyAccountTestState(t, th, agyAccountTestWindows(32))

	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("agy-account-host")).Fill().Children(func() {
			ui.Box(c.Key("agy-account-spacer")).Grow(1)
			renderComposerQuotaStrip(c, s, currentTestThreadSnapshot(s, th.ID, th))
		})
	}, 520, 480)

	if err := tt.Click("额度 agy"); err != nil {
		t.Fatalf("打开 agy 额度弹层失败: %v", err)
	}
	if err := tt.Click("工作账号"); err != nil {
		t.Fatalf("点击「工作账号」失败: %v", err)
	}

	if s.composerView.quotaOpen || s.composerView.quotaPopoverKey != "" {
		t.Fatalf("选中账号后应关闭浮层，open=%v key=%q", s.composerView.quotaOpen, s.composerView.quotaPopoverKey)
	}
	waitFor(t, tt, func() bool {
		calls := fake.SetThreadAccountCalls()
		return len(calls) == 1 && calls[0].ThreadID == "t1" && calls[0].AccountID == "acc-2"
	})
	waitFor(t, tt, func() bool {
		return tt.HasText("工作账号") && tt.HasText("32%") && !tt.HasText("18%")
	})
}

func TestComposerAgyAccountHiddenForSingleAccountOrNonAgy(t *testing.T) {
	t.Run("只有一个未停用账号时无账号行", func(t *testing.T) {
		th := Thread{ID: "t1", ProjectID: "p1", Provider: "agy"}
		windows := []ProviderQuotaWindow{
			{Label: "个人账号 · " + quota.GroupGemini, Account: "个人账号", AccountID: "acc-1", Period: "5h", UsedPercent: 18, Active: true},
			{Label: "停用账号 · " + quota.GroupGemini, Account: "停用账号", AccountID: "acc-2", Period: "5h", UsedPercent: 40, Disabled: true},
		}
		_, s := newAgyAccountTestState(t, th, windows)
		tt := quotaPopoverTester(s, &th, 480, 400)
		if err := tt.Click("额度 agy"); err != nil {
			t.Fatalf("打开弹层失败: %v", err)
		}
		if tt.HasText("自动") {
			t.Fatalf("只有一个可用账号时不应出现「自动」行，texts=%q", tt.Texts())
		}
	})

	t.Run("非agy线程无账号行", func(t *testing.T) {
		th := Thread{ID: "t2", ProjectID: "p1", Provider: "claude"}
		windows := []ProviderQuotaWindow{
			{Label: "a · 5 hours", Account: "a", AccountID: "acc-1", Period: "5h", UsedPercent: 10, Active: true},
			{Label: "b · 5 hours", Account: "b", AccountID: "acc-2", Period: "5h", UsedPercent: 20},
		}
		_, s := newAgyAccountTestState(t, th, windows)
		tt := quotaPopoverTester(s, &th, 480, 400)
		if err := tt.Click("额度 claude"); err != nil {
			t.Fatalf("打开弹层失败: %v", err)
		}
		if tt.HasText("自动") {
			t.Fatalf("非 agy 线程不应出现「自动」行，texts=%q", tt.Texts())
		}
	})
}

func TestComposerAgyAccountLockingInPopover(t *testing.T) {
	t.Run("已有消息时全部账号行禁用且保留勾选", func(t *testing.T) {
		th := Thread{ID: "t1", ProjectID: "p1", Provider: "agy"}
		_, s := newAgyAccountTestState(t, th, agyAccountTestWindows(32))
		s.messages = []Message{{ID: "m1", Role: "user", Text: "hello"}}

		tt := ui.NewTester(func(c *ui.Context) {
			renderComposerQuotaPopover(c, s, &th, "agy", 0)
		}, 480, 420)

		for _, label := range []string{"自动", "个人账号", "工作账号"} {
			node, ok := tt.AccessByRole(ui.RoleButton, label)
			if !ok || !node.Disabled {
				t.Fatalf("已有消息时 %q 应禁用: ok=%v node=%+v", label, ok, node)
			}
		}
		autoBtn, _ := tt.AccessByRole(ui.RoleButton, "自动")
		if got := countRightSlotInk(tt.Image(), autoBtn.Bounds); got == 0 {
			t.Fatalf("锁定时仍应显示当前勾选图标，像素=%d", got)
		}
	})

	t.Run("已有消息但指定账号5h满额时解锁换号", func(t *testing.T) {
		th := Thread{ID: "t1", ProjectID: "p1", Provider: "agy", Account: "acc-2"}
		_, s := newAgyAccountTestState(t, th, agyAccountTestWindows(100))
		s.messages = []Message{{ID: "m1", Role: "user", Text: "hello"}}

		tt := ui.NewTester(func(c *ui.Context) {
			renderComposerQuotaPopover(c, s, &th, "agy", 0)
		}, 480, 420)

		for _, label := range []string{"自动", "个人账号"} {
			node, ok := tt.AccessByRole(ui.RoleButton, label)
			if !ok || node.Disabled {
				t.Fatalf("指定账号 5h 满额解锁后 %q 应可用: ok=%v node=%+v", label, ok, node)
			}
		}
		fullNode, ok := tt.AccessByRole(ui.RoleButton, "工作账号")
		if !ok || !fullNode.Disabled {
			t.Fatalf("5h 满额的账号行自身应保持禁用: ok=%v node=%+v", ok, fullNode)
		}
	})
}

func TestComposerAgyAccountLockedRules(t *testing.T) {
	cases := []struct {
		name        string
		th          *Thread
		hasMessages bool
		isRunning   bool
		windows     []ProviderQuotaWindow
		wantLocked  bool
	}{
		{
			name:        "无消息且未运行解锁",
			th:          &Thread{ID: "t1", Provider: "agy"},
			hasMessages: false,
			isRunning:   false,
			windows:     agyAccountTestWindows(32),
			wantLocked:  false,
		},
		{
			name:        "运行中即使无消息也锁定",
			th:          &Thread{ID: "t1", Provider: "agy"},
			hasMessages: false,
			isRunning:   true,
			windows:     agyAccountTestWindows(32),
			wantLocked:  true,
		},
		{
			name:        "运行中即使指定账号满额仍锁定",
			th:          &Thread{ID: "t1", Provider: "agy", Account: "acc-2"},
			hasMessages: true,
			isRunning:   true,
			windows:     agyAccountTestWindows(100),
			wantLocked:  true,
		},
		{
			name:        "有消息且自动模式锁定",
			th:          &Thread{ID: "t1", Provider: "agy"},
			hasMessages: true,
			isRunning:   false,
			windows:     agyAccountTestWindows(32),
			wantLocked:  true,
		},
		{
			name:        "有消息且指定账号可用时锁定",
			th:          &Thread{ID: "t1", Provider: "agy", Account: "acc-2"},
			hasMessages: true,
			isRunning:   false,
			windows:     agyAccountTestWindows(32),
			wantLocked:  true,
		},
		{
			name:        "有消息且指定账号不在窗口里时解锁",
			th:          &Thread{ID: "t1", Provider: "agy", Account: "acc-missing"},
			hasMessages: true,
			isRunning:   false,
			windows:     agyAccountTestWindows(32),
			wantLocked:  false,
		},
		{
			name:        "有消息且指定账号已停用时解锁",
			th:          &Thread{ID: "t1", Provider: "agy", Account: "acc-2"},
			hasMessages: true,
			isRunning:   false,
			windows: []ProviderQuotaWindow{
				{Label: "个人账号 · " + quota.GroupGemini, Account: "个人账号", AccountID: "acc-1", UsedPercent: 18},
				{Label: "工作账号 · " + quota.GroupGemini, Account: "工作账号", AccountID: "acc-2", UsedPercent: 32, Disabled: true},
			},
			wantLocked: false,
		},
		{
			name:        "有消息且指定账号5h满额时解锁",
			th:          &Thread{ID: "t1", Provider: "agy", Account: "acc-2"},
			hasMessages: true,
			isRunning:   false,
			windows:     agyAccountTestWindows(100),
			wantLocked:  false,
		},
		{
			name:        "有消息且指定账号7d窗口满额时解锁",
			th:          &Thread{ID: "t1", Provider: "agy", Account: "acc-2"},
			hasMessages: true,
			isRunning:   false,
			windows: []ProviderQuotaWindow{
				{Label: "个人账号 · " + quota.GroupGemini, Account: "个人账号", AccountID: "acc-1", UsedPercent: 18},
				{Label: "工作账号 · " + quota.GroupGemini, Account: "工作账号", AccountID: "acc-2", UsedPercent: 32},
				{Label: "工作账号 · " + quota.GroupGemini + " 7d", Account: "工作账号", AccountID: "acc-2", UsedPercent: 100},
			},
			wantLocked: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := agyAccountLocked(tc.th, tc.hasMessages, tc.isRunning, tc.windows); got != tc.wantLocked {
				t.Fatalf("agyAccountLocked() = %v, want %v", got, tc.wantLocked)
			}
		})
	}
}
