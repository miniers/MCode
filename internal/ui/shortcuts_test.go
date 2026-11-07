package mcodeui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func TestMatchShortcut_TableDriven(t *testing.T) {
	tests := []struct {
		name      string
		mods      ui.Modifiers
		key       ui.Key
		wantID    string
		wantIndex int
		wantOK    bool
	}{
		// 窗口与面板类
		{name: "⌘K command-palette", mods: ui.Cmd, key: ui.KeyK, wantID: "command-palette", wantOK: true},
		{name: "⌘B toggle-sidebar", mods: ui.Cmd, key: ui.KeyB, wantID: "toggle-sidebar", wantOK: true},
		{name: "⌘D toggle-diff", mods: ui.Cmd, key: ui.KeyD, wantID: "toggle-diff", wantOK: true},
		{name: "⌘J toggle-terminal", mods: ui.Cmd, key: ui.KeyJ, wantID: "toggle-terminal", wantOK: true},
		{name: "^` toggle-terminal (fallback)", mods: ui.Ctrl, key: ui.KeyBackquote, wantID: "toggle-terminal", wantOK: true},
		{name: "⇧⌘B toggle-preview", mods: ui.Cmd | ui.Shift, key: ui.KeyB, wantID: "toggle-preview", wantOK: true},
		{name: "⇧⌘E open-editor", mods: ui.Cmd | ui.Shift, key: ui.KeyE, wantID: "open-editor", wantOK: true},
		{name: "⇧⌘F toggle-file-manager", mods: ui.Cmd | ui.Shift, key: ui.KeyF, wantID: "toggle-file-manager", wantOK: true},
		{name: "⌥⌘U toggle-activity", mods: ui.Cmd | ui.Alt, key: ui.KeyU, wantID: "toggle-activity", wantOK: true},
		{name: "⌘, open-settings", mods: ui.Cmd, key: ui.KeyComma, wantID: "open-settings", wantOK: true},
		{name: "⌘⇧O add-project", mods: ui.Cmd | ui.Shift, key: ui.KeyO, wantID: "add-project", wantOK: true},
		{name: "⇧⌘K toggle-context", mods: ui.Cmd | ui.Shift, key: ui.KeyK, wantID: "toggle-context", wantOK: true},
		{name: "Esc close-or-escape", mods: 0, key: ui.KeyEscape, wantID: "close-or-escape", wantOK: true},

		// 会话操作类
		{name: "⌘N new-thread", mods: ui.Cmd, key: ui.KeyN, wantID: "new-thread", wantOK: true},
		{name: "⌥⌘N new-worktree-thread", mods: ui.Cmd | ui.Alt, key: ui.KeyN, wantID: "new-worktree-thread", wantOK: true},
		{name: "⇧⌘P toggle-plan-mode", mods: ui.Cmd | ui.Shift, key: ui.KeyP, wantID: "toggle-plan-mode", wantOK: true},
		{name: "⌥⌘R run-script", mods: ui.Cmd | ui.Alt, key: ui.KeyR, wantID: "run-script", wantOK: true},
		{name: "⌥⌘↑ previous-thread", mods: ui.Cmd | ui.Alt, key: ui.KeyUp, wantID: "previous-thread", wantOK: true},
		{name: "⌥⌘↓ next-thread", mods: ui.Cmd | ui.Alt, key: ui.KeyDown, wantID: "next-thread", wantOK: true},
		{name: "⇧⌘S dormant-thread", mods: ui.Cmd | ui.Shift, key: ui.KeyS, wantID: "dormant-thread", wantOK: true},
		{name: "⌃⌘A archive-thread", mods: ui.Cmd | ui.Ctrl, key: ui.KeyA, wantID: "archive-thread", wantOK: true},
		{name: "⇧⌘D forward-thread", mods: ui.Cmd | ui.Shift, key: ui.KeyD, wantID: "forward-thread", wantOK: true},

		// 未注册组合
		{name: "unregistered ⌘W（关闭窗口留给原生菜单）", mods: ui.Cmd, key: ui.KeyW, wantOK: false},
		{name: "unregistered ⌘0", mods: ui.Cmd, key: ui.Key0, wantOK: false},
		{name: "unregistered ⌘Z", mods: ui.Cmd, key: ui.KeyZ, wantOK: false},
		{name: "unregistered A without mods", mods: 0, key: ui.KeyA, wantOK: false},
		{name: "unregistered ^C", mods: ui.Ctrl, key: ui.KeyC, wantOK: false},
		{name: "unregistered ⇧⌘Z", mods: ui.Cmd | ui.Shift, key: ui.KeyZ, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotID, gotIndex, gotOK := matchShortcut(tt.mods, tt.key)
			if gotOK != tt.wantOK {
				t.Fatalf("matchShortcut(%v, %v) ok = %v, want %v", tt.mods, tt.key, gotOK, tt.wantOK)
			}
			if gotID != tt.wantID {
				t.Errorf("matchShortcut(%v, %v) id = %q, want %q", tt.mods, tt.key, gotID, tt.wantID)
			}
			if gotIndex != tt.wantIndex {
				t.Errorf("matchShortcut(%v, %v) index = %d, want %d", tt.mods, tt.key, gotIndex, tt.wantIndex)
			}
		})
	}
}

func TestRootThreadShortcutDispatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.threads["p1"] = []Thread{
		{ID: "t1", ProjectID: "p1", Title: "first"},
		{ID: "t2", ProjectID: "p1", Title: "second"},
	}
	fake.mu.Unlock()
	state, root := buildRoot(fake)
	tt := ui.NewTester(root, 1000, 700)
	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return len(state.threads) == 2
	})
	selected := func(id string) bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.selectedThreadID == id
	}
	tt.Key(ui.Cmd, ui.Key2)
	waitFor(t, tt, func() bool { return selected("t2") })
	tt.Key(ui.Cmd|ui.Alt, ui.KeyUp)
	waitFor(t, tt, func() bool { return selected("t1") })
	tt.Key(ui.Cmd|ui.Alt, ui.KeyDown)
	waitFor(t, tt, func() bool { return selected("t2") })
	tt.Key(ui.Cmd|ui.Shift, ui.KeyS)
	waitFor(t, tt, func() bool { return len(fake.SetThreadDormantCalls()) == 1 })
	// ⌥⌘N 先进入待创建会话（Worktree 模式），输入后才建会话并迁入自动命名的 Worktree。
	tt.Key(ui.Cmd|ui.Alt, ui.KeyN)
	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.pendingThread != nil && state.pendingThread.Worktree
	})
	state.noteComposerDraft("", "你好")
	waitFor(t, tt, func() bool { return fake.CreateThreadCalls() == 1 })
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.moveToWorktreeCalls) == 1
	})
	fake.mu.Lock()
	call := fake.moveToWorktreeCalls[0]
	fake.mu.Unlock()
	if call.ThreadID == "" {
		t.Fatal("MoveToWorktree 的 ThreadID 不应为空")
	}
	if call.Branch != "" || call.TransferFiles {
		t.Fatalf("应迁入自动命名的 Worktree（branch=\"\", transferFiles=false），got %+v", call)
	}
}

// TestRootForwardThreadShortcut 选中会话后按 ⇧⌘D 转发，参数是当前会话 ID。
func TestRootForwardThreadShortcut(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	state, root := buildRoot(fake)
	tt := ui.NewTester(root, 1000, 700)
	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.selectedThreadID != ""
	})
	state.mu.Lock()
	wantID := state.selectedThreadID
	state.mu.Unlock()

	tt.Key(ui.Cmd|ui.Shift, ui.KeyD)
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.ForwardCalls) == 1
	})
	fake.mu.Lock()
	got := fake.ForwardCalls[0]
	fake.mu.Unlock()
	if got != wantID {
		t.Fatalf("转发参数应为选中会话 %q，实际 %q", wantID, got)
	}
}

// TestForwardThreadSkipsInflight 预置进行中标记后，同一会话再触发不应再创建新会话。
func TestForwardThreadSkipsInflight(t *testing.T) {
	fake := NewFakeClient()
	state := newChatState(fake)
	state.mu.Lock()
	state.forwardInflight = map[string]bool{"t1": true}
	state.mu.Unlock()

	state.forwardThread("p1", "t1")

	fake.mu.Lock()
	calls := len(fake.ForwardCalls)
	fake.mu.Unlock()
	if calls != 0 {
		t.Fatalf("转发进行中时不应重复创建，ForwardCalls 长度 = %d", calls)
	}
}

func TestDispatchActiveShortcutRegistersEveryFrame(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var got []string
	tt := ui.NewTester(func(c *ui.Context) {
		if id, _, ok := dispatchActiveShortcut(c); ok {
			got = append(got, id)
		}
	}, 200, 100)
	tt.Key(ui.Cmd, ui.KeyN)
	tt.Key(ui.Cmd|ui.Alt, ui.KeyUp)
	tt.Key(ui.Cmd, ui.Key9)
	if fmt.Sprint(got) != "[new-thread previous-thread select-thread-n]" {
		t.Fatalf("shortcuts lost between frames: %v", got)
	}
}

func TestRootRunScriptLoadsCurrentThreadScripts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubTerminals(t)
	fake := NewFakeClient()
	fake.SetScripts([]Script{{Name: "current", Runner: "npm"}})
	state, root := buildRoot(fake)
	t.Cleanup(func() { closeTerminals(state) })
	tt := ui.NewTester(root, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	var command string
	sendTerminal = func(_ *terminal.Terminal, data []byte) { command = string(data) }
	tt.Key(ui.Cmd|ui.Alt, ui.KeyR)
	waitFor(t, tt, func() bool { return command != "" })
	if !strings.Contains(command, "'current'") || strings.Contains(command, "'stale'") {
		t.Fatalf("wrong script dispatched: %s", command)
	}
}

func TestMatchShortcut_ThreadNumberDirect(t *testing.T) {
	// 覆盖 ⌘1 ~ ⌘9 直达第 N 个会话
	keySlice := []ui.Key{
		ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5, ui.Key6, ui.Key7, ui.Key8, ui.Key9,
	}

	for i, k := range keySlice {
		expectedIndex := i + 1
		t.Run(fmt.Sprintf("⌘%d", expectedIndex), func(t *testing.T) {
			id, index, ok := matchShortcut(ui.Cmd, k)
			if !ok {
				t.Fatalf("expected ⌘%d to match, got ok=false", expectedIndex)
			}
			if id != "select-thread-n" {
				t.Errorf("expected id 'select-thread-n', got %q", id)
			}
			if index != expectedIndex {
				t.Errorf("expected index %d, got %d", expectedIndex, index)
			}
		})
	}
}

func TestAppShortcuts_UniqueIDs(t *testing.T) {
	shortcuts := appShortcuts()
	seen := make(map[string]bool)

	for _, sc := range shortcuts {
		if sc.id == "" {
			t.Errorf("found empty shortcut id: %+v", sc)
		}
		if seen[sc.id] {
			t.Errorf("duplicate shortcut id: %q", sc.id)
		}
		seen[sc.id] = true
	}

	// 确认包含本任务要求补齐的关键条目
	requiredIDs := []string{
		"toggle-terminal",
		"toggle-preview",
		"open-editor",
		"toggle-activity",
		"run-script",
		"select-thread-1-9",
		"previous-thread",
		"next-thread",
		"new-worktree-thread",
		"dormant-thread",
		"archive-thread",
		"forward-thread",
	}

	for _, reqID := range requiredIDs {
		if !seen[reqID] {
			t.Errorf("required shortcut id %q missing from appShortcuts()", reqID)
		}
	}
}

// TestParseFormatShortcutSpec 覆盖按键规格解析、符号格式化与往返。
func TestParseFormatShortcutSpec(t *testing.T) {
	tests := []struct {
		name string
		spec string
		mods ui.Modifiers
		key  ui.Key
		keys string
	}{
		{name: "Cmd+Shift+P", spec: "Cmd+Shift+P", mods: ui.Cmd | ui.Shift, key: ui.KeyP, keys: "⇧⌘P"},
		{name: "小写 cmd+b", spec: "cmd+b", mods: ui.Cmd, key: ui.KeyB, keys: "⌘B"},
		{name: "Option+Cmd+Up", spec: "Option+Cmd+Up", mods: ui.Cmd | ui.Alt, key: ui.KeyUp, keys: "⌥⌘↑"},
		{name: "Ctrl+Backquote", spec: "Ctrl+Backquote", mods: ui.Ctrl, key: ui.KeyBackquote, keys: "⌃`"},
		{name: "Cmd+逗号", spec: "Cmd+,", mods: ui.Cmd, key: ui.KeyComma, keys: "⌘,"},
		{name: "Cmd+Period", spec: "Cmd+Period", mods: ui.Cmd, key: ui.KeyPeriod, keys: "⌘."},
		{name: "Cmd+9", spec: "Cmd+9", mods: ui.Cmd, key: ui.Key9, keys: "⌘9"},
		{name: "Escape", spec: "Escape", mods: 0, key: ui.KeyEscape, keys: "Esc"},
		{name: "Shift+Ctrl+Left", spec: "Shift+Ctrl+Left", mods: ui.Ctrl | ui.Shift, key: ui.KeyLeft, keys: "⌃⇧←"},
		{name: "符号形式 ⇧⌘P", spec: "⇧⌘P", mods: ui.Cmd | ui.Shift, key: ui.KeyP, keys: "⇧⌘P"},
		{name: "CmdOrCtrl 别名", spec: "CmdOrCtrl+Shift+Space", mods: ui.Cmd | ui.Shift, key: ui.KeySpace, keys: "⇧⌘Space"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mods, key, err := parseShortcutSpec(tc.spec)
			if err != nil {
				t.Fatalf("parseShortcutSpec(%q) failed: %v", tc.spec, err)
			}
			if mods != tc.mods || key != tc.key {
				t.Fatalf("parseShortcutSpec(%q) = (%v, %v), want (%v, %v)", tc.spec, mods, key, tc.mods, tc.key)
			}
			if got := formatShortcutKeys(mods, key); got != tc.keys {
				t.Errorf("formatShortcutKeys(%v, %v) = %q, want %q", mods, key, got, tc.keys)
			}
			// 格式化结果可再次解析（符号形式往返）
			mods2, key2, err := parseShortcutSpec(formatShortcutKeys(mods, key))
			if err != nil {
				t.Fatalf("parseShortcutSpec(%q) failed: %v", formatShortcutKeys(mods, key), err)
			}
			if mods2 != mods || key2 != key {
				t.Errorf("roundtrip = (%v, %v), want (%v, %v)", mods2, key2, mods, key)
			}
		})
	}

	invalid := []string{"", "   ", "Cmd+", "Cmd+Shift+", "Cmd+Foo", "Hello", "Cmd+Shift+P+Q", "Foo+B"}
	for _, spec := range invalid {
		t.Run("非法 "+spec, func(t *testing.T) {
			if _, _, err := parseShortcutSpec(spec); err == nil {
				t.Errorf("parseShortcutSpec(%q) 应返回错误", spec)
			}
		})
	}
}

// TestNormalizeShortcutSpec 覆盖输入规范化：修饰键别名与大小写统一，按键保持原样。
func TestNormalizeShortcutSpec(t *testing.T) {
	check := func(in, want string) {
		t.Helper()
		if got := normalizeShortcutSpec(in); got != want {
			t.Errorf("normalizeShortcutSpec(%q) = %q，想要 %q", in, got, want)
		}
	}
	check("  cmd+shift+d ", "Cmd+Shift+D")
	check("COMMAND+Option+W", "Cmd+Alt+W")
	check("CmdOrCtrl+A", "Cmd+A")
	check("⌃⌘A", "⌃⌘A")
	check("Esc", "Esc")
	check("", "")
}

// TestShortcutReservedCombos 覆盖系统/菜单保留组合：命中即提示且不写入（J-06）。
func TestShortcutReservedCombos(t *testing.T) {
	reserved := []string{"⌘Q", "Cmd+W", "⌘H", "Cmd+M", "Cmd+Tab", "⌘Space", "Cmd+,", "⌘⌥Esc", "cmd+tab", "CmdOrCtrl+Tab"}
	for _, spec := range reserved {
		if got := shortcutOverrideError(spec); got != shortcutReservedMessage {
			t.Errorf("shortcutOverrideError(%q) = %q，想要 %q", spec, got, shortcutReservedMessage)
		}
	}

	// 应用自己可用的组合不受影响（Esc 与 ⌘⌥Esc 不是同一个键位）。
	for _, spec := range []string{"Cmd+Shift+D", "⌥⌘U", "Esc", "Cmd+E"} {
		if got := shortcutOverrideError(spec); got != "" {
			t.Errorf("shortcutOverrideError(%q) = %q，想要空串", spec, got)
		}
	}
}

// TestShortcutOverrideMatching 覆盖生效后命中新键、旧键失效，注册表与展示键位同步更新。
func TestShortcutOverrideMatching(t *testing.T) {
	// 示例键位改用 ⇧⌘G：⇧⌘D 已归「转发到新会话」，再拿它做覆盖示例会同键重复。
	setShortcutOverrides(map[string]string{"toggle-diff": "Cmd+Shift+G"})
	t.Cleanup(func() { setShortcutOverrides(nil) })

	if id, _, ok := matchShortcut(ui.Cmd|ui.Shift, ui.KeyG); !ok || id != "toggle-diff" {
		t.Fatalf("覆盖后 ⇧⌘G 应命中 toggle-diff，得到 id=%q ok=%v", id, ok)
	}
	if _, _, ok := matchShortcut(ui.Cmd, ui.KeyD); ok {
		t.Fatal("覆盖后旧键 ⌘D 应失效")
	}
	if id, _, ok := matchShortcut(ui.Cmd, ui.KeyJ); !ok || id != "toggle-terminal" {
		t.Fatalf("未覆盖的 ⌘J 应保持命中 toggle-terminal，得到 id=%q ok=%v", id, ok)
	}

	registered := make(map[registeredShortcut]bool, len(registeredShortcutsList))
	for _, sc := range registeredShortcutsList {
		registered[sc] = true
	}
	if !registered[registeredShortcut{ui.Cmd | ui.Shift, ui.KeyG}] {
		t.Fatal("registeredShortcutsList 未包含覆盖后的 ⇧⌘G")
	}
	if registered[registeredShortcut{ui.Cmd, ui.KeyD}] {
		t.Fatal("registeredShortcutsList 仍包含旧键 ⌘D")
	}

	found := false
	for _, sc := range appShortcuts() {
		if sc.id != "toggle-diff" {
			continue
		}
		found = true
		if sc.keys != "⇧⌘G" {
			t.Errorf("toggle-diff 展示键位应为 ⇧⌘G，得到 %q", sc.keys)
		}
	}
	if !found {
		t.Fatal("appShortcuts() 缺少 toggle-diff")
	}
}

// TestShortcutOverrideInvalidFallsBack 非法或空覆盖忽略并回退默认绑定。
func TestShortcutOverrideInvalidFallsBack(t *testing.T) {
	setShortcutOverrides(map[string]string{"toggle-diff": "Cmd+NotAKey", "toggle-preview": "   "})
	t.Cleanup(func() { setShortcutOverrides(nil) })

	if id, _, ok := matchShortcut(ui.Cmd, ui.KeyD); !ok || id != "toggle-diff" {
		t.Fatalf("非法覆盖应回退默认 ⌘D，得到 id=%q ok=%v", id, ok)
	}
	if id, _, ok := matchShortcut(ui.Cmd|ui.Shift, ui.KeyB); !ok || id != "toggle-preview" {
		t.Fatalf("空覆盖应回退默认 ⇧⌘B，得到 id=%q ok=%v", id, ok)
	}
}

// TestArchiveThreadDefaultShortcut 覆盖 U-043：归档会话默认键位为 ⌃⌘A，
// 不再与文件菜单 RoleClose 的 ⌘W 撞车。
func TestArchiveThreadDefaultShortcut(t *testing.T) {
	setShortcutOverrides(nil)

	if id, _, ok := matchShortcut(ui.Cmd|ui.Ctrl, ui.KeyA); !ok || id != "archive-thread" {
		t.Fatalf("⌃⌘A 应命中 archive-thread，得到 id=%q ok=%v", id, ok)
	}
	if id, _, ok := matchShortcut(ui.Cmd, ui.KeyW); ok {
		t.Fatalf("⌘W 应留给原生菜单的关闭窗口，却命中了 %q", id)
	}
	if want := formatMenuAccelerator(ui.Cmd|ui.Ctrl, ui.KeyA); want == "" {
		t.Fatal("⌃⌘A 应能格式化出菜单 Accelerator")
	}
}

// TestShortcutConflicts 覆盖 toggle-diff 为 ⌘B 后与 toggle-sidebar 互相冲突，默认键位无冲突。
func TestShortcutConflicts(t *testing.T) {
	conflicts := shortcutConflicts(effectiveShortcutBindingsFor(map[string]string{"toggle-diff": "Cmd+B"}))

	if !containsShortcutID(conflicts["toggle-diff"], "toggle-sidebar") {
		t.Fatalf("toggle-diff 应与 toggle-sidebar 冲突，得到 %v", conflicts["toggle-diff"])
	}
	if !containsShortcutID(conflicts["toggle-sidebar"], "toggle-diff") {
		t.Fatalf("toggle-sidebar 应与 toggle-diff 冲突，得到 %v", conflicts["toggle-sidebar"])
	}
	if got := shortcutConflicts(effectiveShortcutBindingsFor(nil)); len(got) != 0 {
		t.Fatalf("默认键位不应存在冲突，得到 %v", got)
	}
}

// TestShortcutOverridesKeepFixedBindings 覆盖不影响固定绑定：⌘1…9、Ctrl+`、Esc、⇧⌘K。
func TestShortcutOverridesKeepFixedBindings(t *testing.T) {
	setShortcutOverrides(map[string]string{"toggle-diff": "Cmd+Shift+Y"})
	t.Cleanup(func() { setShortcutOverrides(nil) })

	if id, index, ok := matchShortcut(ui.Cmd, ui.Key3); !ok || id != "select-thread-n" || index != 3 {
		t.Fatalf("⌘3 应命中 select-thread-n(3)，得到 id=%q index=%d ok=%v", id, index, ok)
	}
	if id, _, ok := matchShortcut(ui.Ctrl, ui.KeyBackquote); !ok || id != "toggle-terminal" {
		t.Fatalf("Ctrl+` 应保持命中 toggle-terminal，得到 id=%q ok=%v", id, ok)
	}
	if id, _, ok := matchShortcut(0, ui.KeyEscape); !ok || id != "close-or-escape" {
		t.Fatalf("Esc 应保持命中 close-or-escape，得到 id=%q ok=%v", id, ok)
	}
	if id, _, ok := matchShortcut(ui.Cmd|ui.Shift, ui.KeyK); !ok || id != "toggle-context" {
		t.Fatalf("⇧⌘K 应保持命中 toggle-context，得到 id=%q ok=%v", id, ok)
	}
}

// TestAppShortcutsKeysFollowDefaults 无覆盖时展示键位与默认绑定一致。
func TestAppShortcutsKeysFollowDefaults(t *testing.T) {
	want := map[string]string{
		"new-thread":           "⌘N",
		"new-worktree-thread":  "⌥⌘N",
		"toggle-plan-mode":     "⇧⌘P",
		"toggle-file-manager":  "⇧⌘F",
		"add-project":          "⇧⌘O",
		"jump-to-latest":       "⌘↓",
		"open-attention-inbox": "⇧⌘I",
		"open-settings":        "⌘,",
		"select-thread-1-9":    "⌘1…9",
		"send-message":         "↩",
		"close-settings":       "Esc",
		"archive-thread":       formatShortcutKeys(ui.Cmd|ui.Ctrl, ui.KeyA),
	}
	for _, sc := range appShortcuts() {
		w, ok := want[sc.id]
		if !ok {
			continue
		}
		if sc.keys != w {
			t.Errorf("%s 默认展示键位应为 %q，得到 %q", sc.id, w, sc.keys)
		}
	}
}

// TestRegisteredShortcutsDefaultSet 默认每帧注册集合与重构前完全一致：
// ⌘N、⌥⌘N、⌘D、⌘J、⌃`、⇧⌘P、⇧⌘B、⇧⌘E、⇧⌘F、⌥⌘U、⌥⌘R、⌥⌘↑、⌥⌘↓、⇧⌘S、⌃⌘A、⌘,、⇧⌘O、⇧⌘K、⌘1…9、⌘/，
// 不含 Esc、⌘K、⌘B（这三者若注册会被 dispatchActiveShortcut 每帧抢占）。
// 「跳到最新」⌘↓ 与「打开待处理收件箱」⇧⌘I 也在默认注册集合里。
func TestRegisteredShortcutsDefaultSet(t *testing.T) {
	setShortcutOverrides(nil)

	want := []registeredShortcut{
		{ui.Cmd, ui.KeyN},
		{ui.Cmd | ui.Alt, ui.KeyN},
		{ui.Cmd, ui.KeyD},
		{ui.Cmd, ui.KeyJ},
		{ui.Ctrl, ui.KeyBackquote},
		{ui.Cmd | ui.Shift, ui.KeyP},
		{ui.Cmd | ui.Shift, ui.KeyB},
		{ui.Cmd | ui.Shift, ui.KeyE},
		{ui.Cmd | ui.Alt, ui.KeyU},
		{ui.Cmd | ui.Alt, ui.KeyR},
		{ui.Cmd | ui.Alt, ui.KeyUp},
		{ui.Cmd | ui.Alt, ui.KeyDown},
		{ui.Cmd, ui.KeyDown},
		{ui.Cmd | ui.Shift, ui.KeyI},
		{ui.Cmd | ui.Shift, ui.KeyS},
		{ui.Cmd | ui.Ctrl, ui.KeyA},
		{ui.Cmd, ui.KeyComma},
		{ui.Cmd | ui.Shift, ui.KeyO},
		{ui.Cmd | ui.Shift, ui.KeyK},
		{ui.Cmd | ui.Shift, ui.KeyF},
		{ui.Cmd | ui.Shift, ui.KeyT},
		{ui.Cmd | ui.Shift, ui.KeyD},
		{ui.Cmd, ui.KeySlash},
	}
	for key := ui.Key1; key <= ui.Key9; key++ {
		want = append(want, registeredShortcut{ui.Cmd, key})
	}

	got := registeredShortcutsFrom(effectiveShortcutBindings())
	if len(got) != len(want) {
		t.Fatalf("默认注册集合应有 %d 项，得到 %d 项: %v", len(want), len(got), got)
	}
	registered := make(map[registeredShortcut]bool, len(got))
	for _, sc := range got {
		registered[sc] = true
	}
	for _, sc := range want {
		if !registered[sc] {
			t.Errorf("默认注册集合缺少 %+v", sc)
		}
	}

	if registered[registeredShortcut{0, ui.KeyEscape}] {
		t.Error("Esc 不应注册（会抢占输入框的 Esc 取消）")
	}
	if registered[registeredShortcut{ui.Cmd, ui.KeyK}] {
		t.Error("⌘K 不应注册（命令面板由 root.go 按有效绑定检测）")
	}
	if registered[registeredShortcut{ui.Cmd, ui.KeyB}] {
		t.Error("⌘B 不应注册（侧边栏由 root.go 按有效绑定检测）")
	}
	if id, _, ok := matchShortcut(0, ui.KeyEscape); !ok || id != "close-or-escape" {
		t.Errorf("Esc 仍应参与匹配 close-or-escape，得到 id=%q ok=%v", id, ok)
	}
}

// TestStewardStartTopicShortcut 新增的「交给总管」快捷键：默认键 ⇧⌘T（⌘⇧S 已被休眠会话占用），
// 分组与会话类一致、参与注册与匹配，且不与既有绑定冲突。
func TestStewardStartTopicShortcut(t *testing.T) {
	setShortcutOverrides(nil)
	t.Cleanup(func() { setShortcutOverrides(nil) })

	var def *appShortcut
	for _, sc := range shortcutDefaults() {
		if sc.id == "steward-start-topic" {
			picked := sc
			def = &picked
			break
		}
	}
	if def == nil {
		t.Fatal("shortcutDefaults 缺少 steward-start-topic")
	}
	if !def.bound || !def.registered || !def.customizable {
		t.Fatalf("steward-start-topic 应参与绑定、注册且可自定义，实际 %+v", *def)
	}
	if def.title != "交给总管" || def.group != "会话" {
		t.Fatalf("标题/分组应为「交给总管」/「会话」，实际 %q/%q", def.title, def.group)
	}
	if def.mods != ui.Cmd|ui.Shift || def.key != ui.KeyT {
		t.Fatalf("默认绑定应为 ⇧⌘T，实际 mods=%v key=%v", def.mods, def.key)
	}
	if def.keys != "⇧⌘T" {
		t.Fatalf("展示键位应为 ⇧⌘T，实际 %q", def.keys)
	}
	if want := formatShortcutKeys(def.mods, def.key); def.keys != want {
		t.Fatalf("展示键位应与格式化结果 %q 一致，实际 %q", want, def.keys)
	}

	if id, _, ok := matchShortcut(def.mods, def.key); !ok || id != "steward-start-topic" {
		t.Fatalf("⇧⌘T 应命中 steward-start-topic，得到 id=%q ok=%v", id, ok)
	}

	// 默认键位整体无冲突（⇧⌘T 与其他条目都不重复）。
	if conflicts := shortcutConflicts(effectiveShortcutBindingsFor(nil)); len(conflicts) != 0 {
		t.Fatalf("默认键位不应存在冲突，得到 %v", conflicts)
	}
	for _, b := range effectiveShortcutBindingsFor(nil) {
		if b.id == "steward-start-topic" {
			continue
		}
		if b.mods == def.mods && b.key == def.key {
			t.Fatalf("⇧⌘T 与 %s 冲突", b.id)
		}
	}
}

// TestJumpLatestAndAttentionInboxShortcuts 验证新增的「跳到最新」⌘↓ 与「打开待处理收件箱」⇧⌘I：
// 都参与绑定、注册与匹配，分组与会话类一致，且不与既有绑定或系统保留组合冲突。
func TestJumpLatestAndAttentionInboxShortcuts(t *testing.T) {
	setShortcutOverrides(nil)
	t.Cleanup(func() { setShortcutOverrides(nil) })

	cases := []struct {
		id    string
		title string
		keys  string
		mods  ui.Modifiers
		key   ui.Key
	}{
		{"jump-to-latest", "跳到最新", "⌘↓", ui.Cmd, ui.KeyDown},
		{"open-attention-inbox", "打开待处理收件箱", "⇧⌘I", ui.Cmd | ui.Shift, ui.KeyI},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			var def *appShortcut
			for _, sc := range shortcutDefaults() {
				if sc.id == tc.id {
					picked := sc
					def = &picked
					break
				}
			}
			if def == nil {
				t.Fatalf("shortcutDefaults 缺少 %s", tc.id)
			}
			if !def.bound || !def.registered || !def.customizable {
				t.Fatalf("%s 应参与绑定、注册且可自定义，实际 %+v", tc.id, *def)
			}
			if def.title != tc.title || def.group != "会话" {
				t.Fatalf("标题/分组应为 %q/「会话」，实际 %q/%q", tc.title, def.title, def.group)
			}
			if def.mods != tc.mods || def.key != tc.key {
				t.Fatalf("默认绑定应为 %s，实际 mods=%v key=%v", tc.keys, def.mods, def.key)
			}
			if def.keys != tc.keys {
				t.Fatalf("展示键位应为 %q，实际 %q", tc.keys, def.keys)
			}
			if want := formatShortcutKeys(def.mods, def.key); def.keys != want {
				t.Fatalf("展示键位应与格式化结果 %q 一致，实际 %q", want, def.keys)
			}
			if isReservedShortcut(def.mods, def.key) {
				t.Fatalf("%s 不应使用系统保留组合 %s", tc.id, tc.keys)
			}
			if id, _, ok := matchShortcut(def.mods, def.key); !ok || id != tc.id {
				t.Fatalf("%s 应命中 %s，得到 id=%q ok=%v", tc.keys, tc.id, id, ok)
			}

			// 默认键位整体无冲突：既没有重复绑定，也没有别的条目占用同一个组合。
			if conflicts := shortcutConflicts(effectiveShortcutBindingsFor(nil)); len(conflicts) != 0 {
				t.Fatalf("默认键位不应存在冲突，得到 %v", conflicts)
			}
			for _, b := range effectiveShortcutBindingsFor(nil) {
				if b.id == tc.id {
					continue
				}
				if b.mods == def.mods && b.key == def.key {
					t.Fatalf("%s 与 %s 冲突", tc.keys, b.id)
				}
			}
		})
	}
}

// TestShortcutTitleWithKey 验证悬停提示带当前有效键位，未绑定时只显示标题。
func TestShortcutTitleWithKey(t *testing.T) {
	setShortcutOverrides(nil)
	t.Cleanup(func() { setShortcutOverrides(nil) })

	if got, want := shortcutTitleWithKey("jump-to-latest", "跳到最新"), "跳到最新（⌘↓）"; got != want {
		t.Fatalf("shortcutTitleWithKey = %q, want %q", got, want)
	}
	if got, want := shortcutTitleWithKey("not-a-shortcut", "跳到最新"), "跳到最新"; got != want {
		t.Fatalf("未知 id 应只返回标题，got %q want %q", got, want)
	}
}
