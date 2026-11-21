package mcodeui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

func TestVirtualListP1ConflictAndConsole(t *testing.T) {
	t.Run("conflict_virtualized", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("package main\n")
		b.WriteString("<<<<<<< ours\nconflict-ours-0\n=======\nconflict-theirs-0\n>>>>>>> theirs\n")
		for i := 1; i <= 120; i++ {
			fmt.Fprintf(&b, "line-%03d\n", i)
		}
		b.WriteString("<<<<<<< ours\nconflict-ours-1\n=======\nconflict-theirs-1\n>>>>>>> theirs\n")
		for i := 121; i <= 210; i++ {
			fmt.Fprintf(&b, "line-%03d\n", i)
		}
		text := b.String()
		regions := git.ParseConflicts(text)
		if len(regions) != 2 {
			t.Fatalf("期望 2 处冲突，实际 %d", len(regions))
		}
		cs := &gitConflictState{
			text:    text,
			regions: regions,
			choices: map[int]git.ConflictChoice{},
		}

		root := t.TempDir()
		base := &gitPaneFakeClient{FakeClient: NewFakeClient(), state: GitState{WorkDir: root}}
		f := &gitDiffFakeClient{gitPaneFakeClient: base}
		s := newChatState(f)
		ps := getGitPaneState(s)
		st := &gitDiffViewState{}
		t.Cleanup(func() { s.close(); gitDiffStates.Delete(ps) })

		key := gitDiffKey{threadID: "t1", root: root, path: "conflict.go", conflict: true}
		result := &gitDiffResult{conflict: cs}

		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c).Fill().MinWidth(0).MinHeight(0).Children(func() {
				renderGitConflict(c, s, ps, st, f, key, result, false, 640, nil)
			})
		}, 640, 320)
		tt.Frame()

		if !tt.HasText("conflict-ours-0") {
			t.Fatal("顶部冲突行未渲染")
		}
		if !tt.HasText("第 1/2 处") {
			t.Fatal("顶部冲突操作条未渲染")
		}
		if tt.HasText("line-195") {
			t.Fatal("远超视口的第 195 行不应被渲染")
		}

		if err := tt.Click("第 1 处 保留 当前"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if cs.choices[0] != git.ConflictOurs {
			t.Fatalf("选择未更新: got %q, want %q", cs.choices[0], git.ConflictOurs)
		}
	})

	t.Run("console_virtualized", func(t *testing.T) {
		s, _ := inspectorContextSetup(t)
		tab := getPreviewState(s)
		previewMu.Lock()
		tab.consoleOpen = true
		for i := range 150 {
			level := "log"
			if i%2 == 1 {
				level = "error"
			}
			tab.consoleErrors = append(tab.consoleErrors, previewConsoleError{
				Sequence: uint64(i + 1),
				Time:     "12:00:00",
				Kind:     level,
				Message:  fmt.Sprintf("console-msg-%03d", i+1),
			})
		}
		previewMu.Unlock()

		tt := ui.NewTester(func(c *ui.Context) {
			previewMu.Lock()
			snap := previewSnapshotLocked(tab)
			previewMu.Unlock()
			renderPreviewConsoleDrawer(c, s, tab, snap)
		}, 700, 250)
		tt.Advance(DurSheet + 50*time.Millisecond)

		rendered := 0
		for _, txt := range tt.Texts() {
			if strings.Contains(txt, "console-msg-") {
				rendered++
			}
		}
		if rendered == 0 || rendered >= 150 {
			t.Fatalf("控制台日志应虚拟化渲染，当前渲染条数=%d", rendered)
		}
		if !tt.HasText("console-msg-150") {
			t.Fatal("FollowEnd 默认滚底后应渲染末尾条目 console-msg-150")
		}
		if tt.HasText("console-msg-001") {
			t.Fatal("视口外顶部条目 console-msg-001 不应被渲染")
		}

		if err := tt.Click("12:00:00 error: console-msg-150"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		previewMu.Lock()
		selected := tab.consoleSelected[150]
		previewMu.Unlock()
		if !selected {
			t.Fatal("勾选条目 150 未写回 tab.consoleSelected")
		}

		if err := tt.Click("log"); err != nil {
			t.Fatal(err)
		}
		tt.Advance(DurSheet + 50*time.Millisecond)
		if !tt.HasText("console-msg-149") {
			t.Fatal("切换到 log 过滤后应显示 console-msg-149")
		}
		if tt.HasText("console-msg-150") {
			t.Fatal("切换到 log 过滤后不应显示 error 条目 console-msg-150")
		}
		if tt.HasText("console-msg-001") {
			t.Fatal("过滤后视口外条目 console-msg-001 仍不应被渲染")
		}
	})
}
