package mcodeui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// gitCommitPaths 把勾选项换算成提交路径：全选时沿用全量提交，重命名带新旧路径，列表为空时视为全选。
func TestGitCommitPaths(t *testing.T) {
	modified := GitChangedFile{Path: "a.go", Status: "M"}
	spaced := GitChangedFile{Path: "b c.go", Status: "?"}
	renamed := GitChangedFile{Path: "new.go", OrigPath: "old.go", Status: "R"}
	files := []GitChangedFile{modified, spaced, renamed}

	tests := []struct {
		name     string
		files    []GitChangedFile
		selected map[string]bool
		paths    string
		all      bool
	}{
		{
			name:     "全部勾选",
			files:    files,
			selected: map[string]bool{"a.go": true, "b c.go": true, "new.go": true},
			paths:    "a.go|b c.go|old.go|new.go",
			all:      true,
		},
		{
			name:     "只勾一个普通文件",
			files:    files,
			selected: map[string]bool{"b c.go": true},
			paths:    "b c.go",
			all:      false,
		},
		{
			name:     "重命名同时带新旧路径",
			files:    files,
			selected: map[string]bool{"new.go": true},
			paths:    "old.go|new.go",
			all:      false,
		},
		{
			name:     "一个都没勾",
			files:    files,
			selected: map[string]bool{},
			paths:    "",
			all:      false,
		},
		{
			name:  "列表还没读出来时沿用全量提交",
			files: nil,
			paths: "",
			all:   true,
		},
	}

	for _, tt := range tests {
		paths, all := gitCommitPaths(tt.files, tt.selected)
		if got := strings.Join(paths, "|"); got != tt.paths {
			t.Errorf("%s: paths = %q, want %q", tt.name, got, tt.paths)
		}
		if all != tt.all {
			t.Errorf("%s: all = %v, want %v", tt.name, all, tt.all)
		}
	}
}

// gitSelectedFileCount 只数勾选的条目，重命名算一个文件。
func TestGitSelectedFileCount(t *testing.T) {
	files := []GitChangedFile{
		{Path: "a.go", Status: "M"},
		{Path: "new.go", OrigPath: "old.go", Status: "R"},
	}
	if got := gitSelectedFileCount(files, map[string]bool{"a.go": true, "new.go": true}); got != 2 {
		t.Errorf("全选计数 = %d, want 2", got)
	}
	if got := gitSelectedFileCount(files, map[string]bool{"new.go": true}); got != 1 {
		t.Errorf("部分勾选计数 = %d, want 1", got)
	}
	if got := gitSelectedFileCount(nil, nil); got != 0 {
		t.Errorf("空列表计数 = %d, want 0", got)
	}
}

// gitChangedFileLabel 用状态字符加路径显示，重命名写成一整行的「旧 -> 新」。
func TestGitChangedFileLabel(t *testing.T) {
	if got := gitChangedFileLabel(GitChangedFile{Path: "a.go", Status: "M"}); got != "M  a.go" {
		t.Errorf("普通文件行 = %q", got)
	}
	if got := gitChangedFileLabel(GitChangedFile{Path: "new.go", OrigPath: "old.go", Status: "R"}); got != "R  old.go -> new.go" {
		t.Errorf("重命名行 = %q", got)
	}
}

// 部分勾选走按路径提交，全部勾选仍走原来的一次性提交全部改动。
func TestGitFormSubmitCommitDispatch(t *testing.T) {
	fake := newGitFakeClient()
	s, root := buildRoot(fake)
	tt := ui.NewTester(root, 1000, 700)
	files := []GitChangedFile{{Path: "a.go", Status: "M"}, {Path: "b.go", Status: "M"}}

	partial := &gitForm{
		s:            s,
		threadID:     "t1",
		kind:         gitFormCommit,
		commitMsg:    "只提交 a.go",
		changedFiles: files,
		fileSelected: map[string]bool{"a.go": true, "b.go": false},
		filesLoaded:  true,
	}
	partial.submitCommit()
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.gitCommitSelectedCalls) == 1
	})
	fake.mu.Lock()
	call := fake.gitCommitSelectedCalls[0]
	fullCommits := len(fake.gitCommitMessages)
	fake.mu.Unlock()
	if got := strings.Join(call.Paths, "|"); got != "a.go" || call.Message != "只提交 a.go" {
		t.Fatalf("按路径提交参数不对: paths=%q message=%q", got, call.Message)
	}
	if fullCommits != 0 {
		t.Fatalf("部分勾选不应触发全量提交，已调用 %d 次", fullCommits)
	}

	full := &gitForm{
		s:            s,
		threadID:     "t1",
		kind:         gitFormCommit,
		commitMsg:    "全部提交",
		changedFiles: files,
		fileSelected: map[string]bool{"a.go": true, "b.go": true},
		filesLoaded:  true,
	}
	full.submitCommit()
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.gitCommitMessages) == 1
	})
	fake.mu.Lock()
	message := fake.gitCommitMessages[0]
	selectedCalls := len(fake.gitCommitSelectedCalls)
	fake.mu.Unlock()
	if message != "全部提交" || selectedCalls != 1 {
		t.Fatalf("全部勾选应走全量提交: message=%q 按路径提交次数=%d", message, selectedCalls)
	}
}

// 提交窗口列出改动文件时默认全选，点「全选」可以一次全部取消。
func TestGitFormCommitFileSelectionToggle(t *testing.T) {
	fake := newGitFakeClient()
	s, _ := buildRoot(fake)
	files := []GitChangedFile{
		{Path: "a.go", Status: "M"},
		{Path: "new.go", OrigPath: "old.go", Status: "R"},
	}
	f := &gitForm{
		s:            s,
		threadID:     "t1",
		kind:         gitFormCommit,
		commitMsg:    "提交信息",
		changedFiles: files,
		fileSelected: map[string]bool{"a.go": true, "new.go": true},
		filesLoaded:  true,
	}
	wt := gitFormTester(t, f)
	waitFor(t, wt, func() bool { return wt.HasText("已选 2/2") })
	if !wt.HasText("M  a.go") || !wt.HasText("R  old.go -> new.go") {
		t.Fatalf("改动文件没有列出来: %q", wt.Texts())
	}
	if err := wt.Click("全选"); err != nil {
		t.Fatalf("点击全选失败: %v", err)
	}
	waitFor(t, wt, func() bool { return wt.HasText("已选 0/2") })
	f.mu.Lock()
	selected := map[string]bool{}
	for path, checked := range f.fileSelected {
		selected[path] = checked
	}
	f.mu.Unlock()
	if selected["a.go"] || selected["new.go"] {
		t.Fatalf("取消全选后仍有勾选项: %+v", selected)
	}
}

// 提交排队时按钮文字缩短为「等待提交…」，无障碍名称仍是「生成完成后自动提交」；
// 未排队时文字与无障碍名称都保持「提交」。
func TestGitFormCommitSubmitTextWhenQueued(t *testing.T) {
	fake := newGitFakeClient()
	s, _ := buildRoot(fake)
	f := &gitForm{
		s:            s,
		threadID:     "t1",
		kind:         gitFormCommit,
		commitMsg:    "feat: 排队提交",
		changedFiles: []GitChangedFile{{Path: "a.go", Status: "M"}},
		fileSelected: map[string]bool{"a.go": true},
		filesLoaded:  true,
	}
	wt := gitFormTester(t, f)
	waitFor(t, wt, func() bool { return wt.HasText("已选 1/1") })

	// 未排队：按钮文字是「提交」，没有排队的 Label 与文案。
	if _, ok := wt.Find("提交"); !ok {
		t.Fatalf("未排队时找不到提交按钮: %q", wt.Texts())
	}
	if _, ok := wt.Find("生成完成后自动提交"); ok {
		t.Fatalf("未排队时不应出现排队的无障碍名称: %q", wt.Texts())
	}
	if wt.HasText("等待提交…") {
		t.Fatalf("未排队时不应显示等待文案: %q", wt.Texts())
	}

	// 排队：AI 正在生成且用户已点过提交，按钮文字换成「等待提交…」，
	// 无障碍名称保留完整含义，界面不再出现超长的旧文案。
	f.mu.Lock()
	f.generating = true
	f.commitWhenGenerated = true
	f.mu.Unlock()
	wt.Frame()

	if _, ok := wt.Find("生成完成后自动提交"); !ok {
		t.Fatalf("排队时找不到无障碍名称为「生成完成后自动提交」的提交按钮: %q", wt.Texts())
	}
	if !wt.HasText("等待提交…") {
		t.Fatalf("排队时按钮应显示「等待提交…」: %q", wt.Texts())
	}
	if wt.HasText("生成完成后自动提交…") {
		t.Fatalf("排队时不应再出现超长的旧文案: %q", wt.Texts())
	}
}
