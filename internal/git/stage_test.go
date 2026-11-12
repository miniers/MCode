package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPartialPatch(t *testing.T) {
	header := "diff --git a/f b/f\n--- a/f\n+++ b/f\n"
	add := "@@ -1,1 +1,3 @@\n c\n+one\n+two\n"
	del := "@@ -1,3 +1,1 @@\n c\n-one\n-two\n"
	tests := []struct {
		name, input, want string
		sels              []LineSel
		reverse           bool
		empty             bool
	}{
		{"部分新增", add, "@@ -1,1 +1,2 @@\n c\n+one\n", []LineSel{{Start: 2, End: 2}}, false, false},
		{"部分删除", del, "@@ -1,3 +1,2 @@\n c\n-one\n two\n", []LineSel{{Old: true, Start: 2, End: 2}}, false, false},
		{"反向新增", add, "@@ -1,2 +1,3 @@\n c\n+one\n two\n", []LineSel{{Start: 2, End: 2}}, true, false},
		{"反向删除", del, "@@ -1,2 +1,1 @@\n c\n-one\n", []LineSel{{Old: true, Start: 2, End: 2}}, true, false},
		{"第二块", add + "@@ -10,1 +12,2 @@ 函数\n z\n+last\n", "@@ -10,1 +10,2 @@ 函数\n z\n+last\n", []LineSel{{Start: 13, End: 13}}, false, false},
		{"反向第二块", add + "@@ -10,1 +12,2 @@\n z\n+last\n", "@@ -12,1 +12,2 @@\n z\n+last\n", []LineSel{{Start: 13, End: 13}}, true, false},
		{"无末尾换行", "@@ -1 +1 @@\n-old\n\\ No newline at end of file\n+new\n\\ No newline at end of file\n", "@@ -1,1 +1,1 @@\n-old\n\\ No newline at end of file\n+new\n\\ No newline at end of file\n", []LineSel{{Old: true, Start: 1, End: 1}, {Start: 1, End: 1}}, false, false},
		{"删除行变上下文保留换行标记", "@@ -1 +1 @@\n-old\n\\ No newline at end of file\n+new\n", "@@ -1,1 +1,2 @@\n old\n\\ No newline at end of file\n+new\n", []LineSel{{Start: 1, End: 1}}, false, false},
		{"空选择", add, "", nil, false, true},
		{"只有上下文", add, "", []LineSel{{Old: true, Start: 1, End: 1}}, false, true},
		{"零行锚点", "@@ -0,0 +1,2 @@\n+one\n+two\n", "@@ -0,0 +1,1 @@\n+two\n", []LineSel{{Start: 2, End: 2}}, false, false},
		{"跨块累计坐标", add + "@@ -10,1 +12,2 @@\n z\n+last\n", "@@ -1,1 +1,2 @@\n c\n+one\n@@ -10,1 +11,2 @@\n z\n+last\n", []LineSel{{Start: 2, End: 2}, {Start: 13, End: 13}}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PartialPatch([]byte(header+tt.input), tt.sels, tt.reverse)
			if tt.empty {
				if !errors.Is(err, ErrEmptySelection) {
					t.Fatalf("应返回 ErrEmptySelection: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != header+tt.want {
				t.Fatalf("补丁不符\n得到: %s\n期望: %s", got, header+tt.want)
			}
		})
	}
	for _, input := range []string{"@@ 无效\n", "@@ -1 +1 @@\n+only\n"} {
		if _, err := PartialPatch([]byte(header+input), []LineSel{{Start: 1, End: 1}}, false); err == nil {
			t.Fatal("应拒绝无效补丁")
		}
	}
	if _, err := PartialPatch([]byte(header+add), []LineSel{{Start: 0, End: 1}}, false); err == nil {
		t.Fatal("应拒绝从零开始的行号")
	}
}

func TestStageUnstageLines(t *testing.T) {
	for _, kind := range []string{"新增行", "删除行", "未跟踪文件", "无末尾换行", "中文 空格.txt"} {
		t.Run(kind, func(t *testing.T) {
			r, root := setupTestRepo(t)
			ctx := context.Background()
			path, before, after := "file.txt", "base\n", "base\none\ntwo\n"
			sel := []LineSel{{Start: 2, End: 2}}
			wantIndex := "base\none\n"
			switch kind {
			case "删除行":
				before, after, wantIndex = "base\none\ntwo\n", "base\n", "base\ntwo\n"
				sel[0].Old = true
			case "未跟踪文件":
				before, after, wantIndex = "", "one\ntwo\n", "one\n"
				sel[0].Start, sel[0].End = 1, 1
			case "无末尾换行":
				before, after, wantIndex = "base\n", "base\none\ntwo", "base\none\n"
			case "中文 空格.txt":
				path = kind
			}
			if kind != "未跟踪文件" {
				writeTestFile(t, root, path, before)
				if err := r.CommitSelected(ctx, root, []string{path}, "准备"); err != nil {
					t.Fatal(err)
				}
			}
			writeTestFile(t, root, path, after)
			if err := r.StageLines(ctx, root, path, sel); err != nil {
				t.Fatal(err)
			}
			if got := runGitOutput(t, r, root, "show", ":"+path); got != wantIndex {
				t.Fatalf("索引: %q，期望 %q", got, wantIndex)
			}
			cached := runGitOutput(t, r, root, "diff", "--cached", "--", path)
			if !strings.Contains(cached, "one") || strings.Contains(cached, "+two") {
				t.Fatalf("暂存范围错误: %s", cached)
			}
			if err := r.UnstageLines(ctx, root, path, sel); err != nil {
				t.Fatal(err)
			}
			if got := runGitOutput(t, r, root, "diff", "--cached", "--", path); got != "" {
				t.Fatalf("索引未恢复: %s", got)
			}
			if got := readTestFile(t, root, path); got != after {
				t.Fatalf("工作区被改动: %q", got)
			}
		})
	}
}

func TestUnstageLinesPartialNewFile(t *testing.T) {
	r, root := setupTestRepo(t)
	writeTestFile(t, root, "new.txt", "one\ntwo\n")
	if err := r.StageFiles(context.Background(), root, []string{"new.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := r.UnstageLines(context.Background(), root, "new.txt", []LineSel{{Start: 1, End: 1}}); err != nil {
		t.Fatal(err)
	}
	if got := runGitOutput(t, r, root, "show", ":new.txt"); got != "two\n" {
		t.Fatalf("索引: %q", got)
	}
}

func TestStageUnstageLinesWholeDeletion(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "整文件删除", true: "部分删除"}[partial], func(t *testing.T) {
			r, root := setupTestRepo(t)
			ctx := context.Background()
			writeTestFile(t, root, "file.txt", "one\ntwo\n")
			if err := r.CommitSelected(ctx, root, []string{"file.txt"}, "准备"); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(root, "file.txt")); err != nil {
				t.Fatal(err)
			}
			end := 2
			if partial {
				end = 1
			}
			sel := []LineSel{{Old: true, Start: 1, End: end}}
			if err := r.StageLines(ctx, root, "file.txt", sel); err != nil {
				t.Fatal(err)
			}
			if partial {
				if got := runGitOutput(t, r, root, "show", ":file.txt"); got != "two\n" {
					t.Fatalf("索引: %q", got)
				}
			}
			if err := r.UnstageLines(ctx, root, "file.txt", sel); err != nil {
				t.Fatal(err)
			}
			if got := runGitOutput(t, r, root, "diff", "--cached"); got != "" {
				t.Fatalf("索引未恢复: %s", got)
			}
			if _, err := os.Lstat(filepath.Join(root, "file.txt")); !os.IsNotExist(err) {
				t.Fatalf("工作区文件不应重建: %v", err)
			}
		})
	}
}

func TestStageLinesMultipleHunks(t *testing.T) {
	r, root := setupTestRepo(t)
	ctx := context.Background()
	var lines []string
	for _, letter := range "abcdefghijklmnopqrstuvwxyz" {
		lines = append(lines, string(letter))
	}
	before := strings.Join(lines, "\n") + "\n"
	writeTestFile(t, root, "file.txt", before)
	if err := r.CommitSelected(ctx, root, []string{"file.txt"}, "准备"); err != nil {
		t.Fatal(err)
	}
	lines = append(lines[:1], append([]string{"first"}, lines[1:]...)...)
	lines = append(lines[:21], append([]string{"second"}, lines[21:]...)...)
	after := strings.Join(lines, "\n") + "\n"
	writeTestFile(t, root, "file.txt", after)
	if err := r.StageLines(ctx, root, "file.txt", []LineSel{{Start: 22, End: 22}}); err != nil {
		t.Fatal(err)
	}
	cached := runGitOutput(t, r, root, "diff", "--cached")
	if strings.Contains(cached, "first") || !strings.Contains(cached, "+second") {
		t.Fatalf("跨块暂存错误: %s", cached)
	}
	// 第一处新增未暂存，索引中新行的行号因此比工作区小 1。
	if err := r.UnstageLines(ctx, root, "file.txt", []LineSel{{Start: 21, End: 21}}); err != nil {
		t.Fatal(err)
	}
	if got := runGitOutput(t, r, root, "diff", "--cached"); got != "" {
		t.Fatalf("索引未恢复: %s", got)
	}
	if got := readTestFile(t, root, "file.txt"); got != after {
		t.Fatal("工作区被修改")
	}
}

func TestStageLinesNoNewlineReplacement(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "暂存", true: "丢弃"}[reverse], func(t *testing.T) {
			r, root := setupTestRepo(t)
			ctx := context.Background()
			writeTestFile(t, root, "file.txt", "old")
			if err := r.CommitSelected(ctx, root, []string{"file.txt"}, "准备"); err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, root, "file.txt", "new")
			sels := []LineSel{{Old: true, Start: 1, End: 1}, {Start: 1, End: 1}}
			if reverse {
				s, err := r.DiscardLines(ctx, root, "file.txt", sels)
				if err != nil {
					t.Fatal(err)
				}
				if got := readTestFile(t, root, "file.txt"); got != "old" {
					t.Fatalf("丢弃结果: %q", got)
				}
				if err := r.RestoreSnapshot(ctx, s); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := r.StageLines(ctx, root, "file.txt", sels); err != nil {
					t.Fatal(err)
				}
				if got := runGitOutput(t, r, root, "show", ":file.txt"); got != "new" {
					t.Fatalf("索引: %q", got)
				}
				if err := r.UnstageLines(ctx, root, "file.txt", sels); err != nil {
					t.Fatal(err)
				}
			}
			if got := readTestFile(t, root, "file.txt"); got != "new" {
				t.Fatalf("工作区: %q", got)
			}
			if got := runGitOutput(t, r, root, "diff", "--cached"); got != "" {
				t.Fatalf("索引未恢复: %s", got)
			}
		})
	}
}

func TestDiscardLines(t *testing.T) {
	r, root := setupTestRepo(t)
	ctx := context.Background()
	writeTestFile(t, root, "file.txt", "base\n")
	if err := r.CommitSelected(ctx, root, []string{"file.txt"}, "准备"); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "file.txt", "base\none\ntwo\n")
	s, err := r.DiscardLines(ctx, root, "file.txt", []LineSel{{Start: 2, End: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, root, "file.txt"); got != "base\ntwo\n" {
		t.Fatalf("丢弃结果: %q", got)
	}
	if err := r.RestoreSnapshot(ctx, s); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, root, "file.txt"); got != "base\none\ntwo\n" {
		t.Fatalf("撤销结果: %q", got)
	}
	if got := runGitOutput(t, r, root, "diff", "--cached"); got != "" {
		t.Fatalf("索引被改动: %s", got)
	}
}

func TestStageUnstageFiles(t *testing.T) {
	for _, initial := range []bool{false, true} {
		t.Run(map[bool]string{false: "空仓库", true: "已有提交"}[initial], func(t *testing.T) {
			r, root := initGitRepo(t)
			if initial {
				r, root = setupTestRepo(t)
			}
			writeTestFile(t, root, "new.txt", "内容\n")
			ctx := context.Background()
			if err := r.StageFiles(ctx, root, []string{"new.txt"}); err != nil {
				t.Fatal(err)
			}
			if got := runGitOutput(t, r, root, "diff", "--cached", "--name-only"); got != "new.txt\n" {
				t.Fatalf("暂存失败: %q", got)
			}
			writeTestFile(t, root, "new.txt", "后续修改\n")
			if err := r.UnstageFiles(ctx, root, []string{"new.txt"}); err != nil {
				t.Fatal(err)
			}
			if got := runGitOutput(t, r, root, "diff", "--cached"); got != "" {
				t.Fatalf("撤销暂存失败: %s", got)
			}
			if got := readTestFile(t, root, "new.txt"); got != "后续修改\n" {
				t.Fatal("工作区被改动")
			}
		})
	}
}

func TestDiscardFiles(t *testing.T) {
	r, root := setupTestRepo(t)
	ctx := context.Background()
	writeTestFile(t, root, "README.md", "暂存内容\n")
	if err := r.StageFiles(ctx, root, []string{"README.md"}); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "README.md", "未暂存内容\n")
	writeTestFile(t, root, "new.txt", "新内容\n")
	s, err := r.DiscardFiles(ctx, root, []string{"README.md", "new.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, root, "README.md"); got != "暂存内容\n" {
		t.Fatalf("没有恢复至索引: %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("未跟踪文件未删除: %v", err)
	}
	if err := r.RestoreSnapshot(ctx, s); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, root, "README.md"); got != "未暂存内容\n" {
		t.Fatal("未恢复跟踪文件")
	}
	if got := readTestFile(t, root, "new.txt"); got != "新内容\n" {
		t.Fatal("未恢复新文件")
	}
	if got := runGitOutput(t, r, root, "show", ":README.md"); got != "暂存内容\n" {
		t.Fatal("索引被修改")
	}
}

func TestCommitIndexLastCommit(t *testing.T) {
	r, root := setupTestRepo(t)
	ctx := context.Background()
	writeTestFile(t, root, "new.txt", "内容\n")
	if err := r.StageFiles(ctx, root, []string{"new.txt"}); err != nil {
		t.Fatal(err)
	}
	sha, err := r.CommitIndex(ctx, root, "标题\n\n正文", false)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(runGitOutput(t, r, root, "rev-parse", "HEAD")); got != sha || len(sha) < 40 {
		t.Fatalf("sha: %q", sha)
	}
	message, err := r.LastCommitMessage(ctx, root)
	if err != nil || message != "标题\n\n正文" {
		t.Fatalf("提交信息: %q %v", message, err)
	}
	count := runGitOutput(t, r, root, "rev-list", "--count", "HEAD")
	newSHA, err := r.CommitIndex(ctx, root, "修改信息", true)
	if err != nil || newSHA == sha {
		t.Fatalf("amend: %s %v", newSHA, err)
	}
	if got := runGitOutput(t, r, root, "rev-list", "--count", "HEAD"); got != count {
		t.Fatal("amend 增加了提交数")
	}
	message, err = r.LastCommitMessage(ctx, root)
	if err != nil || message != "修改信息" {
		t.Fatalf("amend 信息: %q %v", message, err)
	}
	if _, err := r.CommitIndex(ctx, root, "  ", true); err == nil {
		t.Fatal("应拒绝空信息")
	}
}

func TestHeadPushed(t *testing.T) {
	r, root, bare := initSyncRemote(t)
	ctx := context.Background()
	writeTestFile(t, root, "new.txt", "内容\n")
	if err := r.CommitSelected(ctx, root, []string{"new.txt"}, "尚未推送"); err != nil {
		t.Fatal(err)
	}
	if pushed, err := r.HeadPushed(ctx, root); err != nil || pushed {
		t.Fatalf("未推送: %v %v", pushed, err)
	}
	// 只推送到 t.TempDir 创建的本地裸仓库。
	runGitOutput(t, r, root, "push", bare, "HEAD:refs/heads/main")
	runGitOutput(t, r, root, "fetch", "origin")
	if pushed, err := r.HeadPushed(ctx, root); err != nil || !pushed {
		t.Fatalf("已推送: %v %v", pushed, err)
	}
}
