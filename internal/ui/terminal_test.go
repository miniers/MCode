package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// 测试工作目录解析：通过 s.selectedThreadID 在 s.threads 里找到 ProjectID，再到 s.projects 里找 Path
func TestTerminal_WorkDirResolution(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)

	s.mu.Lock()
	s.projects = []Project{
		{ID: "p1", Name: "proj1", Path: "/path/to/project1"},
		{ID: "p2", Name: "proj2", Path: "/path/to/project2"},
	}
	s.threads = []Thread{
		{ID: "t1", ProjectID: "p1", Title: "Thread 1"},
		{ID: "t2", ProjectID: "p2", Title: "Thread 2"},
		{ID: "t3", ProjectID: "non-existent", Title: "Thread 3"},
	}
	s.selectedThreadID = "t1"
	s.mu.Unlock()

	tid, dir := getSelectedThreadWorkDir(s)
	if tid != "t1" || dir != "/path/to/project1" {
		t.Fatalf("expected t1 and /path/to/project1, got tid=%q, dir=%q", tid, dir)
	}

	s.mu.Lock()
	s.selectedThreadID = "t2"
	s.mu.Unlock()

	tid, dir = getSelectedThreadWorkDir(s)
	if tid != "t2" || dir != "/path/to/project2" {
		t.Fatalf("expected t2 and /path/to/project2, got tid=%q, dir=%q", tid, dir)
	}

	// 选中无对应 Project 的会话
	s.mu.Lock()
	s.selectedThreadID = "t3"
	s.mu.Unlock()

	tid, dir = getSelectedThreadWorkDir(s)
	if tid != "t3" || dir != "" {
		t.Fatalf("expected t3 and empty dir, got tid=%q, dir=%q", tid, dir)
	}

	// 没有选中会话
	s.mu.Lock()
	s.selectedThreadID = ""
	s.mu.Unlock()

	tid, dir = getSelectedThreadWorkDir(s)
	if tid != "" || dir != "" {
		t.Fatalf("expected empty tid and dir, got tid=%q, dir=%q", tid, dir)
	}
}

// 测试 toggleTerminal 切换显示状态
func TestTerminal_Toggle(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)

	tt := ui.NewTester(func(c *ui.Context) {
		s.services = c.Services()
		renderTerminalDock(c, s)
	}, 800, 600)
	_ = tt

	st := getTerminalState(s)
	if st.visible {
		t.Fatalf("expected terminal dock to be initially hidden")
	}

	toggleTerminal(s)
	if !st.visible {
		t.Fatalf("expected terminal dock to be visible after toggle")
	}

	toggleTerminal(s)
	if st.visible {
		t.Fatalf("expected terminal dock to be hidden after second toggle")
	}
}

// 测试 closeTerminals 会 Close 掉全部终端并从 map 里删掉记录
func TestTerminal_CloseTerminals(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)
	s.selectedThreadID = "t1"

	origNewTerminal := newTerminal
	defer func() { newTerminal = origNewTerminal }()
	newTerminal = func(opts terminal.Options) (*terminal.Terminal, error) {
		return &terminal.Terminal{}, nil
	}

	origCloseTerminal := closeTerminal
	defer func() { closeTerminal = origCloseTerminal }()
	closeCalls := 0
	closeTerminal = func(t *terminal.Terminal) error {
		closeCalls++
		return nil
	}

	origTerminalDone := terminalDone
	defer func() { terminalDone = origTerminalDone }()
	terminalDone = func(t *terminal.Terminal) <-chan struct{} {
		return nil
	}

	tt := ui.NewTester(func(c *ui.Context) {
		s.services = c.Services()
	}, 800, 600)
	_ = tt

	st := getTerminalState(s)
	workDir := "/path/to/project"
	term, err := getOrCreateTerminal(s, st, workDir)
	if err != nil || term == nil {
		t.Fatalf("failed to create terminal: %v", err)
	}
	// 再为 t1 创建一个标签，测试多标签同时存在时的清理
	_, err = createTerminalTab(s, st, workDir, false)
	if err != nil {
		t.Fatalf("failed to create second terminal tab: %v", err)
	}

	terminalMu.Lock()
	termCount := 0
	for _, tabList := range st.tabs {
		termCount += len(tabList)
	}
	terminalMu.Unlock()
	if termCount != 2 {
		t.Fatalf("expected 2 terminals in tabs, got %d", termCount)
	}

	closeTerminals(s)

	if closeCalls != termCount {
		t.Fatalf("expected closeTerminal to be called %d times, got %d", termCount, closeCalls)
	}

	terminalMu.Lock()
	_, exists := terminalStates[s]
	terminalMu.Unlock()
	if exists {
		t.Fatalf("expected terminalState to be deleted from terminalStates map")
	}

	// 重新获取会得到全新清空的状态
	newSt := getTerminalState(s)
	terminalMu.Lock()
	newCount := len(newSt.tabs)
	terminalMu.Unlock()
	if newCount != 0 {
		t.Fatalf("expected new terminal state to have 0 terminals, got %d", newCount)
	}
}

// 测试没有选中会话时不创建终端
func TestTerminal_NoSelectedThreadDoesNotCreate(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)

	s.mu.Lock()
	s.selectedThreadID = ""
	s.mu.Unlock()

	created := false
	origNewTerminal := newTerminal
	defer func() { newTerminal = origNewTerminal }()
	newTerminal = func(opts terminal.Options) (*terminal.Terminal, error) {
		created = true
		return &terminal.Terminal{}, nil
	}

	origCloseTerminal := closeTerminal
	defer func() { closeTerminal = origCloseTerminal }()
	closeCalls := 0
	closeTerminal = func(t *terminal.Terminal) error {
		closeCalls++
		return nil
	}
	_ = closeCalls

	origTerminalDone := terminalDone
	defer func() { terminalDone = origTerminalDone }()
	terminalDone = func(t *terminal.Terminal) <-chan struct{} {
		return nil
	}

	st := getTerminalState(s)
	st.visible = true

	tt := ui.NewTester(func(c *ui.Context) {
		s.services = c.Services()
		renderTerminalDock(c, s)
	}, 800, 600)

	waitFor(t, tt, func() bool {
		return tt.HasText("没有选中会话")
	})

	if created {
		t.Fatalf("expected newTerminal not to be called when no thread is selected")
	}
	terminalMu.Lock()
	tabLen := len(st.tabs)
	terminalMu.Unlock()
	if tabLen != 0 {
		t.Fatalf("expected 0 terminals created, got %d", tabLen)
	}
}

// 测试 runScriptInTerminal 确保终端创建并可见
func TestTerminal_RunScriptInTerminal(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)

	s.mu.Lock()
	s.projects = []Project{{ID: "p1", Name: "proj1", Path: "/test/project"}}
	s.threads = []Thread{{ID: "t1", ProjectID: "p1", Title: "Thread 1"}}
	s.selectedThreadID = "t1"
	s.mu.Unlock()

	var capturedOpts terminal.Options
	origNewTerminal := newTerminal
	defer func() { newTerminal = origNewTerminal }()
	newTerminal = func(opts terminal.Options) (*terminal.Terminal, error) {
		capturedOpts = opts
		return &terminal.Terminal{}, nil
	}

	origCloseTerminal := closeTerminal
	defer func() { closeTerminal = origCloseTerminal }()
	closeCalls := 0
	closeTerminal = func(t *terminal.Terminal) error {
		closeCalls++
		return nil
	}
	_ = closeCalls

	origTerminalDone := terminalDone
	defer func() { terminalDone = origTerminalDone }()
	terminalDone = func(t *terminal.Terminal) <-chan struct{} {
		return nil
	}

	tt := ui.NewTester(func(c *ui.Context) {
		s.services = c.Services()
	}, 800, 600)
	_ = tt

	sc := Script{
		Name:    "build",
		Command: "pnpm run build",
		Runner:  "pnpm",
	}

	s.runScriptInTerminal(sc)

	st := getTerminalState(s)
	terminalMu.Lock()
	vis := st.visible
	act := activeTerminalTab(st, "t1")
	terminalMu.Unlock()
	if !vis {
		t.Fatalf("expected terminal to become visible after runScriptInTerminal")
	}
	if capturedOpts.Dir != "/test/project" {
		t.Fatalf("expected Dir to be /test/project, got %q", capturedOpts.Dir)
	}
	if act == nil || act.term == nil {
		t.Fatalf("expected terminal to be cached for t1")
	}
}

// 测试 LANG 环境变量补全兜底
func TestTerminal_EnvLangFallback(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)

	s.mu.Lock()
	s.projects = []Project{{ID: "p1", Name: "proj1", Path: "/test/project"}}
	s.threads = []Thread{{ID: "t1", ProjectID: "p1", Title: "Thread 1"}}
	s.selectedThreadID = "t1"
	s.mu.Unlock()

	t.Setenv("LANG", "")
	t.Setenv("LC_ALL", "")

	var capturedOpts terminal.Options
	origNewTerminal := newTerminal
	defer func() { newTerminal = origNewTerminal }()
	newTerminal = func(opts terminal.Options) (*terminal.Terminal, error) {
		capturedOpts = opts
		return &terminal.Terminal{}, nil
	}

	origCloseTerminal := closeTerminal
	defer func() { closeTerminal = origCloseTerminal }()
	closeCalls := 0
	closeTerminal = func(t *terminal.Terminal) error {
		closeCalls++
		return nil
	}
	_ = closeCalls

	origTerminalDone := terminalDone
	defer func() { terminalDone = origTerminalDone }()
	terminalDone = func(t *terminal.Terminal) <-chan struct{} {
		return nil
	}

	tt := ui.NewTester(func(c *ui.Context) {
		s.services = c.Services()
	}, 800, 600)
	_ = tt

	st := getTerminalState(s)
	_, _ = getOrCreateTerminal(s, st, "/test/project")

	hasLang := false
	for _, env := range capturedOpts.Env {
		if env == "LANG=en_US.UTF-8" {
			hasLang = true
			break
		}
	}
	if !hasLang {
		t.Fatalf("expected LANG=en_US.UTF-8 to be added to Env when LANG and LC_ALL are empty")
	}
}

// 测试 FakeClient 的 Scripts 与 SetScripts
func TestFakeClient_Scripts(t *testing.T) {
	fake := NewFakeClient()
	fake.SetScripts([]Script{
		{Name: "dev", Command: "pnpm run dev", Runner: "pnpm", Favorite: true},
	})
	scripts, err := fake.Scripts(context.Background(), "t1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scripts) != 1 || scripts[0].Name != "dev" || !scripts[0].Favorite {
		t.Fatalf("expected 1 favorite script with name dev, got %+v", scripts)
	}
}

func stubTerminals(t *testing.T) {
	t.Helper()
	oldNew, oldClose, oldDone, oldSend := newTerminal, closeTerminal, terminalDone, sendTerminal
	t.Cleanup(func() {
		newTerminal, closeTerminal, terminalDone, sendTerminal = oldNew, oldClose, oldDone, oldSend
	})
	newTerminal = func(terminal.Options) (*terminal.Terminal, error) { return &terminal.Terminal{}, nil }
	closeTerminal = func(*terminal.Terminal) error { return nil }
	terminalDone = func(*terminal.Terminal) <-chan struct{} { return nil }
	sendTerminal = func(*terminal.Terminal, []byte) {}
}

func TestTerminal_IsolatesThreadsAndErrors(t *testing.T) {
	stubTerminals(t)
	s := newChatState(NewFakeClient())
	s.selectedThreadID = "t1"
	t.Cleanup(func() { closeTerminals(s) })
	st := getTerminalState(s)
	newTerminal = func(terminal.Options) (*terminal.Terminal, error) { return nil, errors.New("failed") }
	if _, err := getOrCreateTerminal(s, st, "/same"); err == nil {
		t.Fatal("expected creation error")
	}
	s.selectedThreadID = "t2"
	newTerminal = func(terminal.Options) (*terminal.Terminal, error) { return &terminal.Terminal{}, nil }
	second, err := getOrCreateTerminal(s, st, "/same")
	terminalMu.Lock()
	errT2 := st.errors["t2"]
	errT1 := st.errors["t1"]
	terminalMu.Unlock()
	if err != nil || errT2 != "" || errT1 == "" {
		t.Fatalf("terminal error leaked between threads: %v, errT1=%q, errT2=%q", err, errT1, errT2)
	}
	s.selectedThreadID = "t1"
	first, err := getOrCreateTerminal(s, st, "/same")
	terminalMu.Lock()
	tabsT1 := len(st.tabs["t1"])
	tabsT2 := len(st.tabs["t2"])
	terminalMu.Unlock()
	if err != nil || first == second || tabsT1 != 1 || tabsT2 != 1 {
		t.Fatalf("threads must have separate terminals: %v, tabsT1=%d, tabsT2=%d", err, tabsT1, tabsT2)
	}
}

// 测试脚本命令拼接：工作目录与会话名都要转义成单个 shell 参数。
func TestTerminal_RunScriptQuotesArgs(t *testing.T) {
	stubTerminals(t)
	s := newChatState(NewFakeClient())
	s.projects = []Project{{ID: "p1", Path: "/project with space"}}
	s.threads = []Thread{{ID: "t1", ProjectID: "p1"}}
	s.selectedThreadID = "t1"
	t.Cleanup(func() { closeTerminals(s) })
	name := "build'; echo unsafe; #"
	var command string
	sendTerminal = func(_ *terminal.Terminal, data []byte) { command = string(data) }
	s.runScriptInTerminal(Script{Name: name, Runner: "npm"})
	expected := "cd -- '/project with space' && npm run 'build'\\''; echo unsafe; #'\r"
	if runtime.GOOS == "windows" {
		expected = "Set-Location -LiteralPath '/project with space'; if ($?) { npm run 'build''; echo unsafe; #' }\r"
	}
	if command != expected {
		t.Fatalf("command = %q, want %q", command, expected)
	}
}

type terminalWorkDirClient struct{ *FakeClient }

func (terminalWorkDirClient) ThreadWorkDir(string) (string, bool) {
	return "/actual/worktree", true
}

func TestTerminal_UsesActualThreadWorkDir(t *testing.T) {
	s := newChatState(terminalWorkDirClient{NewFakeClient()})
	s.selectedThreadID = "t1"
	s.projects = []Project{{ID: "p1", Path: "/project"}}
	s.threads = []Thread{{ID: "t1", ProjectID: "p1"}}
	id, dir := getSelectedThreadWorkDir(s)
	if id != "t1" || dir != "/actual/worktree" {
		t.Fatalf("workspace = %q, %q", id, dir)
	}
}

// 测试同一会话创建多个标签，递增 id 并激活最新标签
func TestTerminal_CreateMultipleTabs(t *testing.T) {
	stubTerminals(t)
	s := newChatState(NewFakeClient())
	s.selectedThreadID = "t1"
	t.Cleanup(func() { closeTerminals(s) })
	st := getTerminalState(s)

	tab1, err := createTerminalTab(s, st, "/dir1", false)
	if err != nil {
		t.Fatalf("create tab1 failed: %v", err)
	}
	tab2, err := createTerminalTab(s, st, "/dir2", false)
	if err != nil {
		t.Fatalf("create tab2 failed: %v", err)
	}

	terminalMu.Lock()
	tabs := st.tabs["t1"]
	tabCount := len(tabs)
	act := activeTerminalTab(st, "t1")
	terminalMu.Unlock()

	if tabCount != 2 {
		t.Fatalf("expected 2 tabs, got %d", tabCount)
	}
	if tab1.id == tab2.id {
		t.Fatalf("expected different tab ids, got %d and %d", tab1.id, tab2.id)
	}
	if act == nil || act.id != tab2.id {
		t.Fatalf("expected active tab to be tab2 (id=%d), got %+v", tab2.id, act)
	}
}

// 测试切换标签、getOrCreateTerminal 及 runScriptInTerminal 针对活动标签生效
func TestTerminal_SwitchTab(t *testing.T) {
	stubTerminals(t)
	s := newChatState(NewFakeClient())
	s.projects = []Project{{ID: "p1", Path: "/test/project"}}
	s.threads = []Thread{{ID: "t1", ProjectID: "p1"}}
	s.selectedThreadID = "t1"
	t.Cleanup(func() { closeTerminals(s) })
	st := getTerminalState(s)

	tab1, err := createTerminalTab(s, st, "/test/project", false)
	if err != nil {
		t.Fatalf("create tab1 failed: %v", err)
	}
	_, err = createTerminalTab(s, st, "/test/project", false)
	if err != nil {
		t.Fatalf("create tab2 failed: %v", err)
	}

	// 此时 active 是 tab2，切换到 tab1
	switchTerminalTab(s, st, "t1", tab1.id)

	terminalMu.Lock()
	act := activeTerminalTab(st, "t1")
	terminalMu.Unlock()
	if act == nil || act.id != tab1.id {
		t.Fatalf("expected active tab to be tab1 (id=%d), got %+v", tab1.id, act)
	}

	gotTerm, err := getOrCreateTerminal(s, st, "/test/project")
	if err != nil {
		t.Fatalf("getOrCreateTerminal failed: %v", err)
	}
	if gotTerm != tab1.term {
		t.Fatalf("expected getOrCreateTerminal to return tab1's term")
	}

	var targetTerm *terminal.Terminal
	sendTerminal = func(term *terminal.Terminal, data []byte) {
		targetTerm = term
	}

	sc := Script{Name: "build", Command: "npm run build", Runner: "npm"}
	s.runScriptInTerminal(sc)

	if targetTerm != tab1.term {
		t.Fatalf("expected runScriptInTerminal to target tab1's term")
	}
}

// 测试关闭活动标签后激活相邻标签，并在关闭最后一个标签后面板隐藏
func TestTerminal_CloseTab(t *testing.T) {
	stubTerminals(t)
	var closedTerm *terminal.Terminal
	closeTerminalCalls := 0
	closeTerminal = func(term *terminal.Terminal) error {
		closedTerm = term
		closeTerminalCalls++
		return nil
	}

	s := newChatState(NewFakeClient())
	s.selectedThreadID = "t1"
	t.Cleanup(func() { closeTerminals(s) })
	st := getTerminalState(s)
	st.visible = true

	tab1, err := createTerminalTab(s, st, "/dir1", false)
	if err != nil {
		t.Fatalf("create tab1 failed: %v", err)
	}
	tab2, err := createTerminalTab(s, st, "/dir2", false)
	if err != nil {
		t.Fatalf("create tab2 failed: %v", err)
	}
	tab3, err := createTerminalTab(s, st, "/dir3", false)
	if err != nil {
		t.Fatalf("create tab3 failed: %v", err)
	}

	// 当前 active 是 tab3（末尾）
	closeTerminalCalls = 0
	closedTerm = nil
	closeTerminalTab(s, st, "t1", tab3.id)
	if closeTerminalCalls != 1 || closedTerm != tab3.term {
		t.Fatalf("expected tab3.term to be closed once, calls=%d, term=%v", closeTerminalCalls, closedTerm)
	}

	terminalMu.Lock()
	act := activeTerminalTab(st, "t1")
	terminalMu.Unlock()
	if act == nil || act.id != tab2.id {
		t.Fatalf("expected active tab to transfer to tab2 (id=%d), got %+v", tab2.id, act)
	}

	// 切换到 tab1 并关闭 tab1（开头）
	switchTerminalTab(s, st, "t1", tab1.id)
	closeTerminalCalls = 0
	closedTerm = nil
	closeTerminalTab(s, st, "t1", tab1.id)
	if closeTerminalCalls != 1 || closedTerm != tab1.term {
		t.Fatalf("expected tab1.term to be closed once, calls=%d, term=%v", closeTerminalCalls, closedTerm)
	}

	terminalMu.Lock()
	act = activeTerminalTab(st, "t1")
	terminalMu.Unlock()
	if act == nil || act.id != tab2.id {
		t.Fatalf("expected active tab to transfer to tab2 (id=%d), got %+v", tab2.id, act)
	}

	// 关闭最后一个标签 tab2
	closeTerminalCalls = 0
	closedTerm = nil
	closeTerminalTab(s, st, "t1", tab2.id)
	if closeTerminalCalls != 1 || closedTerm != tab2.term {
		t.Fatalf("expected tab2.term to be closed once, calls=%d, term=%v", closeTerminalCalls, closedTerm)
	}

	terminalMu.Lock()
	remainingTabs := len(st.tabs["t1"])
	vis := st.visible
	terminalMu.Unlock()

	if remainingTabs != 0 {
		t.Fatalf("expected 0 tabs remaining, got %d", remainingTabs)
	}
	if vis {
		t.Fatalf("expected st.visible to be false after closing last tab")
	}
}

// 测试 worktree 与项目主目录分别打开终端时传递正确的 Dir
func TestTerminal_DirSelection(t *testing.T) {
	stubTerminals(t)
	var capturedDirs []string
	newTerminal = func(opts terminal.Options) (*terminal.Terminal, error) {
		capturedDirs = append(capturedDirs, opts.Dir)
		return &terminal.Terminal{}, nil
	}

	client := terminalWorkDirClient{NewFakeClient()}
	s := newChatState(client)
	s.selectedThreadID = "t1"
	s.projects = []Project{{ID: "p1", Path: "/project/main"}}
	s.threads = []Thread{{ID: "t1", ProjectID: "p1"}}
	t.Cleanup(func() { closeTerminals(s) })
	st := getTerminalState(s)

	threadID, workDir, mainDir := getSelectedThreadDirs(s)
	if threadID != "t1" {
		t.Fatalf("expected threadID t1, got %q", threadID)
	}
	if workDir != "/actual/worktree" {
		t.Fatalf("expected workDir /actual/worktree, got %q", workDir)
	}
	if mainDir != "/project/main" {
		t.Fatalf("expected mainDir /project/main, got %q", mainDir)
	}

	_, err := createTerminalTab(s, st, mainDir, true)
	if err != nil {
		t.Fatalf("create main tab failed: %v", err)
	}
	_, err = createTerminalTab(s, st, workDir, false)
	if err != nil {
		t.Fatalf("create worktree tab failed: %v", err)
	}

	if len(capturedDirs) != 2 {
		t.Fatalf("expected 2 newTerminal calls, got %d", len(capturedDirs))
	}
	if capturedDirs[0] != "/project/main" {
		t.Fatalf("expected first terminal Dir to be /project/main, got %q", capturedDirs[0])
	}
	if capturedDirs[1] != "/actual/worktree" {
		t.Fatalf("expected second terminal Dir to be /actual/worktree, got %q", capturedDirs[1])
	}
}

// 测试终端退出后置空 term 并可重启恢复
func TestTerminal_TabExitAndRestart(t *testing.T) {
	stubTerminals(t)
	doneCh := make(chan struct{})
	closeCalls := 0
	closeTerminal = func(*terminal.Terminal) error {
		closeCalls++
		return nil
	}

	s := newChatState(NewFakeClient())
	s.selectedThreadID = "t1"
	t.Cleanup(func() { closeTerminals(s) })
	st := getTerminalState(s)

	tab, err := createTerminalTab(s, st, "/test/dir", false)
	if err != nil {
		t.Fatalf("create tab failed: %v", err)
	}
	terminalDone = func(*terminal.Terminal) <-chan struct{} { return doneCh }
	finished := attachTerminalListener(s, "t1", tab, tab.term)

	// 关闭 channel 模拟终端进程退出
	close(doneCh)

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("terminal listener did not finish")
	}
	if tab.term != nil || tab.err != "终端已退出" || closeCalls != 1 {
		t.Fatalf("unexpected exit state: %+v, closeCalls=%d", tab, closeCalls)
	}

	terminalDone = func(*terminal.Terminal) <-chan struct{} { return nil }
	restartTerminalTab(s, st, "t1", tab.id)

	terminalMu.Lock()
	isRestored := tab.term != nil && tab.err == ""
	terminalMu.Unlock()
	if !isRestored {
		t.Fatalf("expected tab to be restored after restartTerminalTab")
	}
}

func TestTerminal_RestartDiscardedAfterClose(t *testing.T) {
	for _, closeWindow := range []bool{false, true} {
		t.Run(fmt.Sprint("window=", closeWindow), func(t *testing.T) {
			stubTerminals(t)
			s := newChatState(NewFakeClient())
			s.selectedThreadID = "t1"
			t.Cleanup(func() { closeTerminals(s) })
			st := getTerminalState(s)
			tab := &terminalTab{id: 1, dir: "/project", err: "终端已退出"}
			st.tabs["t1"] = []*terminalTab{tab}
			st.active["t1"] = tab.id

			orphan := &terminal.Terminal{}
			newTerminal = func(terminal.Options) (*terminal.Terminal, error) {
				if closeWindow {
					closeTerminals(s)
				} else {
					closeTerminalTab(s, st, "t1", tab.id)
				}
				return orphan, nil
			}
			closeCalls := 0
			closeTerminal = func(term *terminal.Terminal) error {
				if term != orphan {
					t.Fatal("closed an unexpected terminal")
				}
				closeCalls++
				return nil
			}
			term, err := restartTerminalTab(s, st, "t1", tab.id)
			if err == nil || term != nil || tab.term != nil || closeCalls != 1 {
				t.Fatalf("restart retained a closed tab's process: term=%p, err=%v, closes=%d", term, err, closeCalls)
			}
		})
	}
}

func TestTerminal_ScriptRestartsActiveTabInChosenDir(t *testing.T) {
	stubTerminals(t)
	s := newChatState(terminalWorkDirClient{NewFakeClient()})
	s.selectedThreadID = "t1"
	s.projects = []Project{{ID: "p1", Path: "/project/main"}}
	s.threads = []Thread{{ID: "t1", ProjectID: "p1"}}
	t.Cleanup(func() { closeTerminals(s) })
	st := getTerminalState(s)
	tab := &terminalTab{id: 1, dir: "/project/main", main: true, err: "终端已退出"}
	st.tabs["t1"] = []*terminalTab{tab}
	st.active["t1"] = tab.id
	var dir, command string
	newTerminal = func(opts terminal.Options) (*terminal.Terminal, error) {
		dir = opts.Dir
		return &terminal.Terminal{}, nil
	}
	var target *terminal.Terminal
	sendTerminal = func(term *terminal.Terminal, data []byte) {
		target, command = term, string(data)
	}
	s.runScriptInTerminal(Script{Name: "build", Runner: "npm"})
	if len(st.tabs["t1"]) != 1 || activeTerminalTab(st, "t1") != tab || target != tab.term {
		t.Fatal("script must restart and use the existing active tab")
	}
	expected := "cd -- '/project/main' && npm run 'build'\r"
	if runtime.GOOS == "windows" {
		expected = "Set-Location -LiteralPath '/project/main'; if ($?) { npm run 'build' }\r"
	}
	if dir != "/project/main" || command != expected {
		t.Fatalf("script ignored selected directory: dir=%q, command=%q", dir, command)
	}
}

func TestTerminal_CloseAndExitCleanUpOnce(t *testing.T) {
	for _, closeWindow := range []bool{false, true} {
		t.Run(fmt.Sprint("window=", closeWindow), func(t *testing.T) {
			stubTerminals(t)
			s := newChatState(NewFakeClient())
			s.selectedThreadID = "t1"
			t.Cleanup(func() { closeTerminals(s) })
			st := getTerminalState(s)
			tab, err := createTerminalTab(s, st, "/project", false)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			terminalDone = func(*terminal.Terminal) <-chan struct{} { return done }
			finished := attachTerminalListener(s, "t1", tab, tab.term)
			closeCalls := 0
			closeTerminal = func(*terminal.Terminal) error {
				closeCalls++
				close(done)
				return nil
			}
			if closeWindow {
				closeTerminals(s)
			} else {
				closeTerminalTab(s, st, "t1", tab.id)
			}
			select {
			case <-finished:
			case <-time.After(2 * time.Second):
				t.Fatal("terminal listener did not finish")
			}
			if closeCalls != 1 {
				t.Fatalf("terminal closed %d times, want once", closeCalls)
			}
		})
	}
}

func TestTerminal_DockTabControls(t *testing.T) {
	stubTerminals(t)
	s := newChatState(terminalWorkDirClient{NewFakeClient()})
	s.selectedThreadID = "t1"
	s.projects = []Project{{ID: "p1", Path: "/project/main"}}
	s.threads = []Thread{{ID: "t1", ProjectID: "p1"}}
	t.Cleanup(func() { closeTerminals(s) })
	st := getTerminalState(s)
	st.visible = true
	tt := ui.NewTester(func(c *ui.Context) {
		if !s.initialized {
			s.services = c.Services()
			s.initialized = true
		}
		renderTerminalDock(c, s)
	}, 800, 600)
	for _, label := range []string{"新建终端", "主项目目录", "新建终端", "Worktree 目录"} {
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
	}
	if len(st.tabs["t1"]) != 3 || st.tabs["t1"][1].dir != "/project/main" || st.tabs["t1"][2].dir != "/actual/worktree" {
		t.Fatalf("directory selection failed: %+v", st.tabs["t1"])
	}
	if err := tt.Click("1 worktree"); err != nil {
		t.Fatal(err)
	}
	if st.active["t1"] != st.tabs["t1"][0].id {
		t.Fatal("tab click did not select the first tab")
	}
	closeCalls := 0
	closeTerminal = func(*terminal.Terminal) error { closeCalls++; return nil }
	if err := tt.Click("关闭标签"); err != nil {
		t.Fatal(err)
	}
	if len(st.tabs["t1"]) != 2 || closeCalls != 1 {
		t.Fatal("close button did not remove and close the tab")
	}
}

func TestTerminal_NewTabFailureKeepsActiveTerminalVisible(t *testing.T) {
	stubTerminals(t)
	s := newChatState(NewFakeClient())
	s.selectedThreadID = "t1"
	s.projects = []Project{{ID: "p1", Path: "/project"}}
	s.threads = []Thread{{ID: "t1", ProjectID: "p1"}}
	t.Cleanup(func() { closeTerminals(s) })
	st := getTerminalState(s)
	st.visible = true
	tt := ui.NewTester(func(c *ui.Context) { renderTerminalDock(c, s) }, 800, 600)
	first := st.tabs["t1"][0]
	newTerminal = func(terminal.Options) (*terminal.Terminal, error) {
		return nil, errors.New("start failed")
	}
	if err := tt.Click("新建终端"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("新建终端失败: start failed") || !tt.HasText("Terminal") || st.active["t1"] != first.id {
		t.Fatal("failed creation must show its error without hiding the active terminal")
	}
	newTerminal = func(terminal.Options) (*terminal.Terminal, error) {
		return &terminal.Terminal{}, nil
	}
	if err := tt.Click("新建终端"); err != nil {
		t.Fatal(err)
	}
	if len(st.tabs["t1"]) != 2 || tt.HasText("start failed") {
		t.Fatal("successful creation must clear the previous creation error")
	}
}

func TestTerminal_RenderDuringExit(t *testing.T) {
	stubTerminals(t)
	s := newChatState(NewFakeClient())
	s.selectedThreadID = "t1"
	s.projects = []Project{{ID: "p1", Path: "/project"}}
	s.threads = []Thread{{ID: "t1", ProjectID: "p1"}}
	t.Cleanup(func() { closeTerminals(s) })
	st := getTerminalState(s)
	st.visible = true
	for range 8 {
		if _, err := createTerminalTab(s, st, "/project", false); err != nil {
			t.Fatal(err)
		}
	}
	tt := ui.NewTester(func(c *ui.Context) {
		if !s.initialized {
			s.services = c.Services()
			s.initialized = true
		}
		renderTerminalDock(c, s)
	}, 800, 600)
	done := make(chan struct{})
	terminalDone = func(*terminal.Terminal) <-chan struct{} { return done }
	var listeners []<-chan struct{}
	for _, tab := range st.tabs["t1"] {
		listeners = append(listeners, attachTerminalListener(s, "t1", tab, tab.term))
	}
	close(done)
	for range 8 {
		tt.Frame()
	}
	for _, finished := range listeners {
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Fatal("terminal listener did not finish")
		}
	}
	tt.Frame()
	if !tt.HasText("终端已退出") || !tt.HasText("重启") {
		t.Fatal("exited active tab must offer a restart")
	}
}

func TestTerminal_PersistDuringExit(t *testing.T) {
	stubTerminals(t)
	t.Setenv("MCODE_DATA", t.TempDir())
	s := newChatState(NewFakeClient())
	s.selectedThreadID = "t1"
	s.projects = []Project{{ID: "p1", Path: "/project"}}
	s.threads = []Thread{{ID: "t1", ProjectID: "p1"}}
	t.Cleanup(func() { closeTerminals(s) })
	st := getTerminalState(s)
	for range 4 {
		if _, err := createTerminalTab(s, st, "/project", false); err != nil {
			t.Fatal(err)
		}
	}
	terminalMu.Lock()
	tabs := append([]*terminalTab(nil), st.tabs["t1"]...)
	terminalMu.Unlock()

	// 退出监听在 terminalMu 内把 tab.term 置 nil 再还原，同时多个 goroutine 在锁外
	// 调 persistTerminalState 构建快照：-race 下能直接暴露 tab 字段的无锁读写。
	stop := make(chan struct{})
	var toggler sync.WaitGroup
	toggler.Add(1)
	go func() {
		defer toggler.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			for _, tab := range tabs {
				terminalMu.Lock()
				term := tab.term
				tab.term = nil
				terminalMu.Unlock()
				terminalMu.Lock()
				tab.term = term
				tab.err = ""
				terminalMu.Unlock()
			}
		}
	}()

	var writers sync.WaitGroup
	for range 4 {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for range 20 {
				persistTerminalState(s, "t1")
			}
		}()
	}
	writers.Wait()
	close(stop)
	toggler.Wait()

	// 收尾时约定所有 term 已还原，落盘结果仍应是完整的一份快照。
	snap, ok := loadTerminalThreadSnapshot("t1")
	if !ok || len(snap.Tabs) != len(tabs) {
		t.Fatalf("snapshot = %+v, ok=%v, want %d tabs", snap.Tabs, ok, len(tabs))
	}
	if snap.Active < 0 || snap.Active >= len(snap.Tabs) {
		t.Fatalf("active index %d out of range", snap.Active)
	}
	for _, rec := range snap.Tabs {
		if rec.Dir != "/project" {
			t.Fatalf("tab dir = %q, want /project", rec.Dir)
		}
	}
}

func TestTerminal_ScrollbackConfiguration(t *testing.T) {
	s := newChatState(NewFakeClient())
	// 默认情况
	if lines := getTerminalScrollbackLines(s); lines != config.DefaultTerminalScrollback {
		t.Fatalf("expected default scrollback %d, got %d", config.DefaultTerminalScrollback, lines)
	}

	// 关联 SettingsState 并配置自定义 ScrollbackLines
	settings := NewSettingsState(settingsTestDir(t), nil, s)
	settings.cfg.Workbench.TerminalScrollbackLines = 5000
	setChatSettings(s, settings)

	if lines := getTerminalScrollbackLines(s); lines != 5000 {
		t.Fatalf("expected configured scrollback 5000, got %d", lines)
	}

	// 验证 startTerminalProcess 会将配置传入 Options.Scrollback
	origNewTerminal := newTerminal
	defer func() { newTerminal = origNewTerminal }()

	var capturedOpts terminal.Options
	newTerminal = func(opts terminal.Options) (*terminal.Terminal, error) {
		capturedOpts = opts
		return &terminal.Terminal{}, nil
	}

	s.selectedThreadID = "t1"
	s.projects = []Project{{ID: "p1", Path: "/project"}}
	s.threads = []Thread{{ID: "t1", ProjectID: "p1"}}
	st := getTerminalState(s)

	_, err := createTerminalTab(s, st, "/project", false)
	if err != nil {
		t.Fatalf("createTerminalTab failed: %v", err)
	}
	if capturedOpts.Scrollback != 5000 {
		t.Fatalf("expected captured scrollback 5000, got %d", capturedOpts.Scrollback)
	}
}

// 测试 resolveTerminalShell 纯函数的选择逻辑与兜底机制
func TestTerminal_ResolveTerminalShell(t *testing.T) {
	defaultSysShell := defaultFallbackShell()

	// 1. 配置了有效且存在的 shell 绝对路径
	validShell := "/bin/sh"
	if runtime.GOOS == "windows" {
		validShell, _ = os.Executable()
	}
	if got := resolveTerminalShell(validShell, "/bin/other"); got != validShell {
		t.Fatalf("expected configured valid shell %q, got %q", validShell, got)
	}

	// 2. 配置了有效路径但带有首尾空白
	if got := resolveTerminalShell("  "+validShell+"  ", "/bin/other"); got != validShell {
		t.Fatalf("expected trimmed valid shell %q, got %q", validShell, got)
	}

	// 3. 配置为空字符串时，回退到 envShell
	if got := resolveTerminalShell("", validShell); got != validShell {
		t.Fatalf("expected envShell /bin/bash, got %q", got)
	}

	// 4. 配置为纯空白字符串时，回退到 envShell
	if got := resolveTerminalShell("   ", validShell); got != validShell {
		t.Fatalf("expected envShell /bin/bash, got %q", got)
	}

	// 5. 配置为空且 envShell 也为空时，回退到系统默认 shell
	if got := resolveTerminalShell("", ""); got != defaultSysShell {
		t.Fatalf("expected default system shell %q, got %q", defaultSysShell, got)
	}
	if got := resolveTerminalShell("   ", "   "); got != defaultSysShell {
		t.Fatalf("expected default system shell %q, got %q", defaultSysShell, got)
	}

	// 6. 配置了不存在的路径时，回退到 envShell
	nonexistent := "/path/to/definitely/nonexistent/shell"
	if got := resolveTerminalShell(nonexistent, validShell); got != validShell {
		t.Fatalf("expected fallback to envShell /bin/bash for nonexistent path, got %q", got)
	}

	// 7. 配置了不存在的路径且 envShell 为空时，回退到系统默认 shell
	if got := resolveTerminalShell(nonexistent, ""); got != defaultSysShell {
		t.Fatalf("expected fallback to default system shell %q for nonexistent path with empty env, got %q", defaultSysShell, got)
	}

	// 8. 配置的是一个目录时，不应作为可执行 shell，回退到 envShell
	tmpDir := t.TempDir()
	if got := resolveTerminalShell(tmpDir, validShell); got != validShell {
		t.Fatalf("expected directory path to fallback to envShell, got %q", got)
	}
}

// 测试通过 Settings 配置 TerminalShell 对终端启动参数的影响
func TestTerminal_ShellConfiguration(t *testing.T) {
	validShell := "/bin/sh"
	if runtime.GOOS == "windows" {
		validShell, _ = os.Executable()
	}

	origNewTerminal := newTerminal
	defer func() { newTerminal = origNewTerminal }()

	var capturedOpts terminal.Options
	newTerminal = func(opts terminal.Options) (*terminal.Terminal, error) {
		capturedOpts = opts
		return &terminal.Terminal{}, nil
	}

	s := newChatState(NewFakeClient())
	s.selectedThreadID = "t1"
	s.projects = []Project{{ID: "p1", Path: "/project"}}
	s.threads = []Thread{{ID: "t1", ProjectID: "p1"}}
	st := getTerminalState(s)

	// 配置有效 shell
	settings := NewSettingsState(settingsTestDir(t), nil, s)
	settings.cfg.Workbench.TerminalShell = validShell
	setChatSettings(s, settings)

	_, err := createTerminalTab(s, st, "/project", false)
	if err != nil {
		t.Fatalf("createTerminalTab failed: %v", err)
	}
	if len(capturedOpts.Command) == 0 || capturedOpts.Command[0] != validShell {
		t.Fatalf("expected Command[0] to be %q, got %v", validShell, capturedOpts.Command)
	}

	// 配置不存在的 shell，终端启动时应兜底退回默认 shell，不能启动失败
	capturedOpts = terminal.Options{}
	settings.cfg.Workbench.TerminalShell = "/nonexistent/fake/shell"
	_, err = createTerminalTab(s, st, "/project", false)
	if err != nil {
		t.Fatalf("createTerminalTab failed with fallback shell: %v", err)
	}
	if len(capturedOpts.Command) > 0 && capturedOpts.Command[0] == "/nonexistent/fake/shell" {
		t.Fatalf("expected fallback shell, but got nonexistent command: %v", capturedOpts.Command)
	}
}

// 测试已安装终端应用的检测：Terminal 始终列举，其余按固定顺序只保留检测到的应用
func TestTerminal_DetectInstalledTerminalApps(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Fatalf("os.UserHomeDir failed: %v", err)
	}

	// 什么都没装：系统自带的 Terminal 仍然列出
	if apps := detectInstalledTerminalApps(func(string) bool { return false }); len(apps) != 1 || apps[0] != "Terminal" {
		t.Fatalf("expected only Terminal, got %v", apps)
	}

	// 检测路径覆盖 /Applications 与 ~/Applications
	queried := make(map[string]bool)
	apps := detectInstalledTerminalApps(func(path string) bool {
		queried[path] = true
		return strings.HasSuffix(path, "Ghostty.app") || strings.HasSuffix(path, "kitty.app")
	})
	if len(apps) != 3 || apps[0] != "Terminal" || apps[1] != "Ghostty" || apps[2] != "kitty" {
		t.Fatalf("expected [Terminal Ghostty kitty], got %v", apps)
	}
	if !queried["/Applications/Ghostty.app"] {
		t.Fatalf("expected /Applications to be checked, queried %v", queried)
	}
	if !queried["/Applications/Warp.app"] || !queried[filepath.Join(home, "Applications", "Warp.app")] {
		t.Fatalf("expected /Applications and ~/Applications to be checked, queried %v", queried)
	}
}

// 测试「默认终端」下拉选项：已安装应用在列，旧的配置值保留为一项并保持选中
func TestTerminal_AppOptions(t *testing.T) {
	options, selected := terminalAppOptions("Ghostty")
	if selected != "Ghostty" {
		t.Fatalf("expected selection Ghostty, got %q", selected)
	}
	if len(options) == 0 || options[0] != "Terminal" {
		t.Fatalf("expected Terminal to be offered, got %v", options)
	}

	// 旧版本保存的 shell 路径不在已安装列表里，仍作为一项保留
	options, selected = terminalAppOptions("/bin/zsh")
	if selected != "/bin/zsh" {
		t.Fatalf("expected legacy value to stay selected, got %q", selected)
	}
	if options[len(options)-1] != "/bin/zsh" {
		t.Fatalf("expected legacy value kept as option, got %v", options)
	}
	for _, opt := range options[:len(options)-1] {
		if opt == "/bin/zsh" {
			t.Fatalf("expected legacy value only once, got %v", options)
		}
	}

	// 空配置时选中第一项（Terminal 系统自带，始终在列）
	options, selected = terminalAppOptions("")
	if selected != "Terminal" {
		t.Fatalf("expected empty config to select Terminal, got %q", selected)
	}
	if len(options) == 0 {
		t.Fatal("expected non-empty options")
	}
}

// 测试配置值到终端应用的解析：应用名、.app 名称与路径都识别，未知值不识别
func TestTerminal_ResolveTerminalApp(t *testing.T) {
	for _, tc := range []struct {
		conf string
		want string
	}{
		{"Terminal", "Terminal"},
		{"iterm", "iTerm"},
		{"iTerm.app", "iTerm"},
		{"/Applications/Ghostty.app", "Ghostty"},
		{"  wezterm  ", "WezTerm"},
		{"Alacritty", "Alacritty"},
		{"kitty", "kitty"},
		{"Warp", "Warp"},
	} {
		app, ok := resolveTerminalApp(tc.conf)
		if !ok || app.Name != tc.want {
			t.Fatalf("resolveTerminalApp(%q) = %v, %v; want %q", tc.conf, app.Name, ok, tc.want)
		}
	}

	for _, conf := range []string{"", "   ", "/bin/zsh", "/usr/local/bin/fish", "Sublime Text"} {
		if app, ok := resolveTerminalApp(conf); ok {
			t.Fatalf("resolveTerminalApp(%q) should not resolve, got %v", conf, app.Name)
		}
	}
}

// 测试外部打开目录：每个取出的应用都用 open -a <App名> <目录> 打开，未知值退回 Terminal
func TestTerminal_OpenDirInExternalTerminal(t *testing.T) {
	origRunner := externalTerminalRunner
	defer func() { externalTerminalRunner = origRunner }()

	var got [][]string
	externalTerminalRunner = func(args ...string) error {
		got = append(got, append([]string(nil), args...))
		return nil
	}

	for _, app := range []string{"Terminal", "iTerm", "Ghostty", "Warp", "WezTerm", "Alacritty", "kitty"} {
		if err := openDirInExternalTerminal("/repo", app); err != nil {
			t.Fatalf("openDirInExternalTerminal(%q) failed: %v", app, err)
		}
		last := got[len(got)-1]
		if len(last) != 3 || last[0] != "-a" || last[1] != app || last[2] != "/repo" {
			t.Fatalf("expected open -a %s /repo, got %v", app, last)
		}
	}

	// 未配置与旧值都退回 Terminal
	for _, conf := range []string{"", "/bin/zsh"} {
		if err := openDirInExternalTerminal("/repo", conf); err != nil {
			t.Fatalf("openDirInExternalTerminal(%q) failed: %v", conf, err)
		}
		last := got[len(got)-1]
		if len(last) != 3 || last[1] != "Terminal" {
			t.Fatalf("expected fallback to Terminal for %q, got %v", conf, last)
		}
	}

	// 工作目录为空时不打开
	if err := openDirInExternalTerminal("   ", "Ghostty"); err == nil {
		t.Fatal("expected error for empty dir")
	}
}

// Windows 上的终端候选：Windows Terminal 需在 PATH 中找到，PowerShell 与命令提示符始终在列。
func TestTerminal_DetectInstalledTerminalAppsWindows(t *testing.T) {
	origGOOS := terminalGOOS
	defer func() { terminalGOOS = origGOOS }()
	origLookPath := terminalAppLookPath
	defer func() { terminalAppLookPath = origLookPath }()

	terminalGOOS = "windows"
	terminalAppLookPath = func(string) (string, error) { return "", errors.New("not found") }
	if apps := detectInstalledTerminalApps(nil); !reflect.DeepEqual(apps, []string{"PowerShell", "命令提示符"}) {
		t.Fatalf("windows 未装 Windows Terminal 时应为 [PowerShell 命令提示符], got %v", apps)
	}

	terminalAppLookPath = func(name string) (string, error) {
		if name == "wt.exe" {
			return filepath.Join("C:", "Windows", "wt.exe"), nil
		}
		return "", errors.New("not found")
	}
	want := []string{"Windows Terminal", "PowerShell", "命令提示符"}
	if apps := detectInstalledTerminalApps(nil); !reflect.DeepEqual(apps, want) {
		t.Fatalf("windows 已装 Windows Terminal 时应为 %v, got %v", want, apps)
	}
}

// Windows 上的终端配置解析：应用名与可执行文件名都识别，macOS 专属终端不识别。
func TestTerminal_ResolveTerminalAppWindows(t *testing.T) {
	origGOOS := terminalGOOS
	defer func() { terminalGOOS = origGOOS }()
	terminalGOOS = "windows"

	for _, tc := range []struct {
		conf string
		want string
	}{
		{"Windows Terminal", "Windows Terminal"},
		{"wt.exe", "Windows Terminal"},
		{"powershell", "PowerShell"},
		{"powershell.exe", "PowerShell"},
		{"cmd.exe", "命令提示符"},
		{"命令提示符", "命令提示符"},
	} {
		app, ok := resolveTerminalApp(tc.conf)
		if !ok || app.Name != tc.want {
			t.Fatalf("resolveTerminalApp(%q) = %v, %v; want %q", tc.conf, app.Name, ok, tc.want)
		}
	}

	for _, conf := range []string{"", "   ", "Terminal", "iTerm", "/Applications/Ghostty.app", "/bin/zsh"} {
		if app, ok := resolveTerminalApp(conf); ok {
			t.Fatalf("resolveTerminalApp(%q) should not resolve on windows, got %v", conf, app.Name)
		}
	}
}

// Windows 上打开外部终端：不调用 open，按终端类型拼接命令，未配置时优先 Windows Terminal。
func TestTerminal_OpenDirInExternalTerminalWindows(t *testing.T) {
	origGOOS := terminalGOOS
	defer func() { terminalGOOS = origGOOS }()
	origExec := externalTerminalExecRunner
	defer func() { externalTerminalExecRunner = origExec }()
	origRunner := externalTerminalRunner
	defer func() { externalTerminalRunner = origRunner }()
	origLookPath := terminalAppLookPath
	defer func() { terminalAppLookPath = origLookPath }()

	terminalGOOS = "windows"
	var got [][]string
	externalTerminalExecRunner = func(name string, args ...string) error {
		got = append(got, append([]string{name}, args...))
		return nil
	}
	var opens [][]string
	externalTerminalRunner = func(args ...string) error {
		opens = append(opens, append([]string(nil), args...))
		return nil
	}

	// 未安装 Windows Terminal：默认 PowerShell
	terminalAppLookPath = func(string) (string, error) { return "", errors.New("not found") }
	if err := openDirInExternalTerminal(`C:\repo`, ""); err != nil {
		t.Fatalf("openDirInExternalTerminal failed: %v", err)
	}

	// 已安装 Windows Terminal：默认用它，旧配置值也退回它
	terminalAppLookPath = func(name string) (string, error) {
		if name == "wt.exe" {
			return filepath.Join("C:", "Windows", "wt.exe"), nil
		}
		return "", errors.New("not found")
	}
	for _, conf := range []string{"", "/bin/zsh"} {
		if err := openDirInExternalTerminal(`C:\repo`, conf); err != nil {
			t.Fatalf("openDirInExternalTerminal(%q) failed: %v", conf, err)
		}
	}
	if err := openDirInExternalTerminal(`C:\repo`, "命令提示符"); err != nil {
		t.Fatalf("openDirInExternalTerminal failed: %v", err)
	}

	want := [][]string{
		{"cmd", "/c", "start", "", "/D", `C:\repo`, "powershell.exe", "-NoExit"},
		{"wt.exe", "-d", `C:\repo`},
		{"wt.exe", "-d", `C:\repo`},
		{"cmd", "/c", "start", "", "/D", `C:\repo`, "cmd.exe"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("windows 终端启动参数 = %+v, want %+v", got, want)
	}
	if len(opens) != 0 {
		t.Fatalf("windows 上不应调用 open, got %+v", opens)
	}

	// 工作目录为空时不打开
	if err := openDirInExternalTerminal("   ", "PowerShell"); err == nil {
		t.Fatal("expected error for empty dir")
	}
}

// 测试内嵌终端不会把配置里的终端应用名当成 shell 启动
func TestTerminal_ResolveShellIgnoresTerminalApp(t *testing.T) {
	for _, conf := range []string{"Ghostty", "iTerm.app", "/Applications/Warp.app"} {
		if got := resolveTerminalShell(conf, ""); got != defaultFallbackShell() {
			t.Fatalf("resolveTerminalShell(%q) = %q, want default shell %q", conf, got, defaultFallbackShell())
		}
	}
}

// 终端 dock 高度钳制：下限 120；上限为窗口高度减去聊天区保留的 200，且不低于下限；
// 窗口高度未知（<=0）时只做下限钳制。
func TestClampTerminalDockHeight(t *testing.T) {
	cases := []struct {
		h, winH, want float32
	}{
		{0, 600, TerminalDockMinHeight},
		{50, 600, TerminalDockMinHeight},
		{260, 600, 260},
		{500, 600, 400},
		{260, 300, TerminalDockMinHeight},
		{500, 0, 500},
		{10, 0, TerminalDockMinHeight},
	}
	for _, tc := range cases {
		if got := clampTerminalDockHeight(tc.h, tc.winH); got != tc.want {
			t.Fatalf("clampTerminalDockHeight(%v, %v) = %v, want %v", tc.h, tc.winH, got, tc.want)
		}
	}
}

// 打开终端后 dock 使用默认高度 260，且渲染出上边缘拖拽条。
func TestTerminalDock_DefaultHeight(t *testing.T) {
	stubTerminals(t)
	s := newChatState(NewFakeClient())
	t.Cleanup(func() { closeTerminals(s) })

	tt := ui.NewTester(func(c *ui.Context) {
		s.services = c.Services()
		renderTerminalDock(c, s)
	}, 800, 600)

	if _, ok := tt.Find("拖动调整终端高度"); ok {
		t.Fatal("终端未显示时不应渲染拖拽条")
	}

	toggleTerminal(s)
	tt.Frame()
	if _, ok := tt.Find("拖动调整终端高度"); !ok {
		t.Fatalf("终端显示后应渲染拖拽条, texts: %q", tt.Texts())
	}

	st := getTerminalState(s)
	terminalMu.Lock()
	height := st.height
	terminalMu.Unlock()
	if height != TerminalDockDefaultHeight {
		t.Fatalf("dock 默认高度 = %v, want %v", height, TerminalDockDefaultHeight)
	}
}

// 拖拽 dock 上边缘：向上拖增高、向下拖变矮，超出范围时被钳制到合法区间。
func TestTerminalDock_ResizerDrag(t *testing.T) {
	stubTerminals(t)
	s := newChatState(NewFakeClient())
	t.Cleanup(func() { closeTerminals(s) })

	// 顶部垫一块空白，让拖拽条不贴窗口上缘，向上拖时指针仍在窗口内。
	tt := ui.NewTester(func(c *ui.Context) {
		s.services = c.Services()
		ui.Column(c.Key("terminal-dock-test-shell")).Fill().Children(func() {
			ui.Box(c.Key("terminal-dock-test-spacer")).FillWidth().Height(200)
			renderTerminalDock(c, s)
		})
	}, 800, 600)
	st := getTerminalState(s)
	toggleTerminal(s)
	tt.Frame()

	dockHeight := func() float32 {
		terminalMu.Lock()
		defer terminalMu.Unlock()
		return st.height
	}

	r, ok := tt.Find("拖动调整终端高度")
	if !ok {
		t.Fatalf("缺少拖拽条, texts: %q", tt.Texts())
	}
	if r.H != terminalDockResizerHeight {
		t.Fatalf("拖拽条高度 = %v, want %v", r.H, terminalDockResizerHeight)
	}

	// 向上拖 40pt：dock 增高 40。
	x, y := r.X+r.W/2, r.Y+r.H/2
	tt.Press(x, y)
	tt.Move(x, y-40)
	tt.Release(x, y-40)
	tt.Frame()
	if got, want := dockHeight(), TerminalDockDefaultHeight+40; got != want {
		t.Fatalf("向上拖拽后高度 = %v, want %v", got, want)
	}

	// 向下拖 300pt：超过下限被钳制到最小高度。
	r, ok = tt.Find("拖动调整终端高度")
	if !ok {
		t.Fatalf("拖拽后缺少拖拽条, texts: %q", tt.Texts())
	}
	x, y = r.X+r.W/2, r.Y+r.H/2
	tt.Press(x, y)
	tt.Move(x, y+300)
	tt.Release(x, y+300)
	tt.Frame()
	if got := dockHeight(); got != TerminalDockMinHeight {
		t.Fatalf("向下拖拽后高度 = %v, want %v", got, TerminalDockMinHeight)
	}
}

// 高度偏好独立于 terminalState：关闭终端后再打开仍沿用持久化高度。
func TestTerminalDock_HeightPreferenceSurvivesClose(t *testing.T) {
	stubTerminals(t)
	s := newChatState(NewFakeClient())
	t.Cleanup(func() { closeTerminals(s) })

	setTerminalDockHeightPreference(s, 320)
	if got := terminalDockHeightForPersist(s); got != 320 {
		t.Fatalf("没有终端状态时应回落到偏好值, got %v", got)
	}

	st := getTerminalState(s)
	terminalMu.Lock()
	height := st.height
	terminalMu.Unlock()
	if height != 320 {
		t.Fatalf("新建终端状态应使用偏好高度, got %v", height)
	}

	// 关闭终端会删掉 terminalState，但偏好仍在。
	closeTerminals(s)
	if got := terminalDockHeightForPersist(s); got != 320 {
		t.Fatalf("关闭终端后偏好应保留, got %v", got)
	}
	reopened := getTerminalState(s)
	terminalMu.Lock()
	height = reopened.height
	terminalMu.Unlock()
	if height != 320 {
		t.Fatalf("重新打开终端应沿用偏好高度, got %v", height)
	}
}

// terminalDockOccupiedHeight 只报控制台当前占住窗口底部的高度：没有状态、未显示时为 0，
// 可见时返回钳制后的高度，且不改写 st.height。
func TestTerminalDockOccupiedHeight(t *testing.T) {
	if got := terminalDockOccupiedHeight(nil, 800); got != 0 {
		t.Fatalf("nil chatState 的占用高度 = %v, want 0", got)
	}

	s := newChatState(NewFakeClient())
	t.Cleanup(func() { closeTerminals(s) })
	if got := terminalDockOccupiedHeight(s, 800); got != 0 {
		t.Fatalf("没有终端状态时占用高度 = %v, want 0", got)
	}

	st := getTerminalState(s)
	terminalMu.Lock()
	st.height = 300
	terminalMu.Unlock()
	if got := terminalDockOccupiedHeight(s, 800); got != 0 {
		t.Fatalf("控制台未显示时占用高度 = %v, want 0", got)
	}

	terminalMu.Lock()
	st.visible = true
	terminalMu.Unlock()
	if got := terminalDockOccupiedHeight(s, 800); got != 300 {
		t.Fatalf("可见时占用高度 = %v, want 300", got)
	}

	want := clampTerminalDockHeight(300, 400)
	if got := terminalDockOccupiedHeight(s, 400); got != want {
		t.Fatalf("窗口变矮后占用高度 = %v, want %v", got, want)
	}
	terminalMu.Lock()
	height := st.height
	terminalMu.Unlock()
	if height != 300 {
		t.Fatalf("计算占用高度不应改写 st.height, got %v", height)
	}
}

func TestTerminalScriptWithoutWorkDirToast(t *testing.T) {
	s := newChatState(NewFakeClient())
	s.runScriptInTerminal(Script{Name: "build", Runner: "npm"})
	if s.toastMessage != "没有可用的工作目录" {
		t.Fatalf("toast = %q", s.toastMessage)
	}
}

func TestTerminal_InvalidEnvShell(t *testing.T) {
	if got := resolveTerminalShell("", filepath.Join(t.TempDir(), "missing-shell")); got != defaultFallbackShell() {
		t.Fatalf("?? SHELL ????: %q", got)
	}
}
