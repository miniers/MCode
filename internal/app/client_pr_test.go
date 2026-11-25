package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"mcode/internal/git"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
)

func TestGitPRCleanupWorktreeSwitchesThreads(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("测试使用 POSIX git 包装脚本")
	}
	for _, running := range []bool{false, true} {
		t.Run(map[bool]string{false: "空闲会话切回主仓库", true: "运行中保留工作区"}[running], func(t *testing.T) {
			c, root, first := branchClient(t)
			c.dataDir = t.TempDir()
			ctx := context.Background()
			bare := filepath.Join(t.TempDir(), "origin.git")
			branchGit(t, root, "init", "--bare", bare)
			branchGit(t, root, "remote", "add", "origin", bare)
			base := branchGit(t, root, "branch", "--show-current")
			branchGit(t, root, "push", "origin", "HEAD:refs/heads/"+base, "HEAD:refs/heads/pr-head", "HEAD:refs/pull/1/head")

			// 仅平台探测使用 GitHub 地址，所有实际 Git 操作仍访问本地裸仓库。
			gitBin, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			wrapper := filepath.Join(t.TempDir(), "git")
			script := "#!/bin/sh\nif [ \"$1\" = remote ] && [ \"$2\" = get-url ]; then\n  echo https://github.com/test/repo.git\n  exit 0\nfi\nexec '" + strings.ReplaceAll(gitBin, "'", "'\\''") + "' \"$@\"\n"
			if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			c.git = git.NewRunner(wrapper, 2)
			path, err := c.GitPRResolveWorktree(ctx, first, 1, "pr-head", base)
			if err != nil {
				t.Fatal(err)
			}
			if err := c.GitSwitchWorktree(ctx, first, path); err != nil {
				t.Fatal(err)
			}
			if reused, err := c.GitPRResolveWorktree(ctx, first, 1, "pr-head", base); err != nil || reused != path {
				t.Fatalf("从 PR worktree 复用的路径 = %q，期望 %q，错误 = %v", reused, path, err)
			}
			second, err := c.backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"})
			if err != nil {
				t.Fatal(err)
			}
			alias := filepath.Join(t.TempDir(), "alias")
			if err := os.Symlink(path, alias); err != nil {
				t.Fatal(err)
			}
			if err := c.GitSwitchWorktree(ctx, second.ThreadID, alias); err != nil {
				t.Fatal(err)
			}
			archived := true
			if _, err := c.backend.UpdateThread(ctx, wire.ThreadUpdateParams{ThreadID: second.ThreadID, Archived: &archived}); err != nil {
				t.Fatal(err)
			}
			if running {
				feed, err := c.backend.EnsureStewardFeedThread(ctx, "p1")
				if err != nil {
					t.Fatal(err)
				}
				if err := c.GitSwitchWorktree(ctx, feed, path); err != nil {
					t.Fatal(err)
				}
				c.backend.SetStewardBusy(feed, true)
				defer c.backend.SetStewardBusy(feed, false)
			}
			reason, err := c.GitPRCleanupWorktree(ctx, first, 1)
			if err != nil {
				t.Fatal(err)
			}
			if running {
				if reason != "会话正在运行" {
					t.Fatalf("保留原因 = %q", reason)
				}
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("运行中的工作区必须保留: %v", err)
				}
				return
			}
			if reason != "" {
				t.Fatalf("清理保留原因 = %q", reason)
			}
			realRoot, err := filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{first, second.ThreadID} {
				dir, ok := c.ThreadWorkDir(id)
				realDir, err := filepath.EvalSymlinks(dir)
				if !ok || err != nil || realDir != realRoot {
					t.Fatalf("会话 %s 未切回主仓库: %q，错误 = %v", id, dir, err)
				}
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("worktree 目录未删除: %v", err)
			}
			if out := branchGit(t, root, "branch", "--list", "mcode/pr-1"); out != "" {
				t.Fatalf("PR 本地分支未删除: %s", out)
			}
		})
	}
}

// prReviewTestPatch 是用于解析测试的两文件补丁：a.go 新侧有效行号 1-3，b.go 为 1-2。
const prReviewTestPatch = `diff --git a/a.go b/a.go
index 1111111..2222222 100644
--- a/a.go
+++ b/a.go
@@ -1,2 +1,3 @@
 package a
+func New() {}
 func Old() {}
diff --git a/b.go b/b.go
--- /dev/null
+++ b/b.go
@@ -0,0 +1,2 @@
+package b
+func B() {}
`

func TestGitPRAIReviewParse(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		want    []git.PRReviewComment
		wantErr bool
	}{
		{
			name: "代码围栏与说明文字",
			out:  "审查结果如下：\n```json\n[{\"path\":\"a.go\",\"line\":2,\"body\":\"新增的函数缺少空值判断\"}]\n```\n以上。",
			want: []git.PRReviewComment{{Path: "a.go", Line: 2, Side: "RIGHT", Body: "新增的函数缺少空值判断"}},
		},
		{
			name: "越界行号被丢弃",
			out:  `[{"path":"a.go","line":4,"body":"越界"},{"path":"b.go","line":3,"body":"越界"}]`,
			want: []git.PRReviewComment{},
		},
		{
			name: "不存在的路径被丢弃",
			out:  `[{"path":"c.go","line":1,"body":"不在 diff 里"}]`,
			want: []git.PRReviewComment{},
		},
		{
			name: "重复项去重",
			out:  `[{"path":"a.go","line":3,"body":"第一条"},{"path":"a.go","line":3,"body":"重复"},{"path":"b.go","line":2,"body":"另一文件"}]`,
			want: []git.PRReviewComment{
				{Path: "a.go", Line: 3, Side: "RIGHT", Body: "第一条"},
				{Path: "b.go", Line: 2, Side: "RIGHT", Body: "另一文件"},
			},
		},
		{
			name: "空数组",
			out:  "[]",
			want: []git.PRReviewComment{},
		},
		{
			name:    "非法 JSON",
			out:     `[{"path":"a.go","line":2,"body":}]`,
			wantErr: true,
		},
		{
			name:    "没有数组",
			out:     "这里没有评论",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePRAIReview(tc.out, prReviewTestPatch)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "AI 审查结果无法解析") {
					t.Fatalf("错误 = %v，期望解析失败", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if got == nil {
				t.Fatal("空结果也必须是空切片，不能是 nil")
			}
			if len(got) != len(tc.want) {
				t.Fatalf("条数 = %d, 期望 %d: %+v", len(got), len(tc.want), got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("第 %d 条 = %+v, 期望 %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestPRReviewBlock(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		want  string
		found bool
	}{
		{
			name: "没有块",
			text: "审查完成，没有发现问题。",
		},
		{
			name:  "单个块",
			text:  "```pr-review\n[{\"path\":\"a.go\",\"line\":2,\"body\":\"问题\"}]\n```",
			want:  `[{"path":"a.go","line":2,"body":"问题"}]`,
			found: true,
		},
		{
			name:  "同一消息多个块取最后一个",
			text:  "```pr-review\n[]\n```\n修订后：\n```pr-review\n[{\"path\":\"b.go\",\"line\":1,\"body\":\"最后一条\"}]\n```",
			want:  `[{"path":"b.go","line":1,"body":"最后一条"}]`,
			found: true,
		},
		{
			name:  "块前后有说明文字",
			text:  "我审查了这个 PR。\n\n```pr-review\n[]\n```\n\n以上是结果。",
			want:  "[]",
			found: true,
		},
		{
			name: "未闭合块",
			text: "```pr-review\n[{\"path\":\"a.go\",\"line\":2,\"body\":\"问题\"}]",
		},
		{
			name: "只有其他语言的围栏",
			text: "```json\n[]\n```",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := prReviewBlock(tc.text)
			if ok != tc.found {
				t.Fatalf("prReviewBlock() found = %v, want %v", ok, tc.found)
			}
			if got != tc.want {
				t.Fatalf("prReviewBlock() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestGitPRAIReviewWithoutResult 验证会话里没有 pr-review 块时直接报错。
func TestGitPRAIReviewWithoutResult(t *testing.T) {
	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	defer hub.Close()
	backend := remote.NewMemoryBackend(hub)
	defer backend.Close()
	backend.AddProject(wire.ProjectSummary{ID: "proj-pr-review", Name: "Proj", Path: "/mock"})
	backend.AddThread(wire.ThreadSummary{ID: "thread-pr-review", ProjectID: "proj-pr-review", Provider: "memory", Title: "审查"}, []wire.ThreadMessage{
		{ID: "m1", Role: "user", Text: "审查这个 PR"},
		{ID: "m2", Role: "assistant", Text: "我没有输出审查块。"},
	})

	_, err := (&Client{backend: backend}).GitPRAIReview(context.Background(), "thread-pr-review", 1)
	if err == nil || err.Error() != "当前会话没有 AI 审查结果" {
		t.Fatalf("错误 = %v, 期望当前会话没有 AI 审查结果", err)
	}
}

// TestGitPRAIReviewParseLimit 覆盖最多保留 10 条的限制。
func TestGitPRAIReviewParseLimit(t *testing.T) {
	patch := "diff --git a/many.go b/many.go\n--- a/many.go\n+++ b/many.go\n@@ -0,0 +1,12 @@\n"
	items := make([]string, 0, 12)
	for i := 1; i <= 12; i++ {
		patch += "+line " + strconv.Itoa(i) + "\n"
		items = append(items, `{"path":"many.go","line":`+strconv.Itoa(i)+`,"body":"问题 `+strconv.Itoa(i)+`"}`)
	}
	got, err := parsePRAIReview("["+strings.Join(items, ",")+"]", patch)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("条数 = %d, 期望最多 10 条", len(got))
	}
	for i, c := range got {
		if c.Line != i+1 {
			t.Fatalf("第 %d 条行号 = %d, 期望 %d", i, c.Line, i+1)
		}
	}
}

func TestPRDiffNewLinesHunkPlusPlus(t *testing.T) {
	patch := strings.Join([]string{
		"diff --git a/a.go b/a.go",
		"index 1111111..2222222 100644",
		"--- a/a.go",
		"+++ b/a.go",
		"@@ -1,2 +1,4 @@",
		" package a",
		"+++ x",
		"+func New() {}",
		" func Old() {}",
		"diff --git a/b.go b/b.go",
		"--- /dev/null",
		"+++ b/b.go",
		"@@ -0,0 +1,1 @@",
		"+package b",
	}, "\n")

	valid := prDiffNewLines(patch)
	for _, line := range []int{1, 2, 3, 4} {
		if !valid["a.go"][line] {
			t.Fatalf("a.go 第 %d 行应有效: %+v", line, valid)
		}
	}
	if !valid["b.go"][1] {
		t.Fatalf("b.go 第 1 行应有效: %+v", valid)
	}
	if len(valid) != 2 {
		t.Fatalf("文件映射数量 = %d, 期望 2: %+v", len(valid), valid)
	}
}

func TestParsePRReviewBracketPreamble(t *testing.T) {
	out := "[PR #12] 说明如下：\n```json\n[{\"path\":\"a.go\",\"line\":2,\"body\":\"缺少边界检查\"}]\n```\n[注] 结束"
	got, err := parsePRAIReview(out, prReviewTestPatch)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(got) != 1 || got[0].Path != "a.go" || got[0].Line != 2 || got[0].Body != "缺少边界检查" {
		t.Fatalf("解析结果 = %+v", got)
	}

	emptyGot, err := parsePRAIReview("[PR #12] 无问题 []", prReviewTestPatch)
	if err != nil || len(emptyGot) != 0 {
		t.Fatalf("空数组解析 = (%+v, %v)", emptyGot, err)
	}

	if _, err := parsePRAIReview("[PR #12] 没有 JSON", prReviewTestPatch); err == nil {
		t.Fatal("缺少 JSON 数组时应返回错误")
	}
}

// TestGitPRAIReviewParseVerdictObject 覆盖新版结论对象格式及旧数组格式兼容性。
func TestGitPRAIReviewParseVerdictObject(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		want    []git.PRReviewComment
		wantErr bool
	}{
		{
			name: "对象格式带说明文字前缀并按 diff 校验",
			out: "说明文字 {前置说明} 如下：\n```json\n" +
				`{"verdict":"fix","summary":"发现一处问题","comments":[{"path":"a.go","line":2,"body":"缺少空值判断"},{"path":"a.go","line":99,"body":"越界行应被过滤"}]}` +
				"\n```\n以上。",
			want: []git.PRReviewComment{{Path: "a.go", Line: 2, Side: "RIGHT", Body: "缺少空值判断"}},
		},
		{
			name: "summary 含方括号与花括号字符",
			out:  `{"verdict":"comment","summary":"涉及 [配置] 与 {上下文} 的边界","comments":[{"path":"b.go","line":2,"body":"建议补充断言"}]}`,
			want: []git.PRReviewComment{{Path: "b.go", Line: 2, Side: "RIGHT", Body: "建议补充断言"}},
		},
		{
			name: "对象 comments 为空数组",
			out:  `{"verdict":"merge","summary":"整体通过","comments":[]}`,
			want: []git.PRReviewComment{},
		},
		{
			name: "旧数组格式仍可用",
			out:  `[{"path":"a.go","line":1,"body":"旧格式评论"}]`,
			want: []git.PRReviewComment{{Path: "a.go", Line: 1, Side: "RIGHT", Body: "旧格式评论"}},
		},
		{
			name:    "对象缺少 comments 键",
			out:     `{"verdict":"merge","summary":"只有结论"}`,
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePRAIReview(tc.out, prReviewTestPatch)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "AI 审查结果无法解析") {
					t.Fatalf("错误 = %v，期望解析失败", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if got == nil {
				t.Fatal("空结果也必须是空切片，不能是 nil")
			}
			if len(got) != len(tc.want) {
				t.Fatalf("条数 = %d, 期望 %d: %+v", len(got), len(tc.want), got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("第 %d 条 = %+v, 期望 %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestGitPRDiffQuotedPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git 不可用")
	}
	root := t.TempDir()
	branchGit(t, root, "init")
	branchGit(t, root, "config", "user.name", "测试")
	branchGit(t, root, "config", "user.email", "test@example.com")
	branchGit(t, root, "config", "core.quotePath", "true")
	branchGit(t, root, "config", "diff.noprefix", "true")

	writeRevertRepoFile(t, root, "seed.txt", "seed\n")
	branchGit(t, root, "add", "seed.txt")
	branchGit(t, root, "commit", "-m", "base")
	base := branchGit(t, root, "rev-parse", "HEAD")

	const cnPath = "中文目录/示例.go"
	if err := os.MkdirAll(filepath.Join(root, filepath.Dir(cnPath)), 0o755); err != nil {
		t.Fatal(err)
	}
	writeRevertRepoFile(t, root, cnPath, "package cn\nfunc Hello() {}\n")
	branchGit(t, root, "add", cnPath)
	branchGit(t, root, "commit", "-m", "add chinese file")
	head := branchGit(t, root, "rev-parse", "HEAD")

	patch, err := gitPRDiff(context.Background(), root, base, head)
	if err != nil {
		t.Fatalf("gitPRDiff 失败: %v", err)
	}
	valid := prDiffNewLines(patch)
	if !valid[cnPath][1] || !valid[cnPath][2] {
		t.Fatalf("中文路径未正确解析: %+v\npatch:\n%s", valid, patch)
	}
}

// TestGitPRAIReviewParseStructured 覆盖结构化评论字段解析与旧 body 兼容。
func TestGitPRAIReviewParseStructured(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want []git.PRReviewComment
	}{
		{
			name: "新格式对象含阻塞前缀并忽略 patch",
			out: `{"verdict":"fix","reason":"需先修复空指针后再合并","comments":[` +
				`{"path":"a.go","line":2,"severity":"blocking","headline":"缺少空值检查","impact":"输入为 nil 时会 panic","fix":"在入口处补充判空返回","patch":"@@ -1,2 +1,4 @@\n+if x == nil {\n+ return\n+}"},` +
				`{"path":"b.go","line":2,"severity":"suggestion","headline":"命名可以更明确","impact":"降低后续阅读效率","fix":"改为 NewRunner"}` +
				`]}`,
			want: []git.PRReviewComment{
				{Path: "a.go", Line: 2, Side: "RIGHT", Body: "[阻塞] 缺少空值检查\n\n影响：输入为 nil 时会 panic\n\n建议：在入口处补充判空返回"},
				{Path: "b.go", Line: 2, Side: "RIGHT", Body: "命名可以更明确\n\n影响：降低后续阅读效率\n\n建议：改为 NewRunner"},
			},
		},
		{
			name: "跳过空的 impact 或 fix",
			out: `{"verdict":"comment","reason":"部分信息可省略","comments":[` +
				`{"path":"a.go","line":2,"severity":"blocking","headline":"越界访问","impact":"","fix":"先校验长度"},` +
				`{"path":"b.go","line":2,"severity":"suggestion","headline":"缺少错误透传","impact":"调用方无法区分错误原因","fix":""}` +
				`]}`,
			want: []git.PRReviewComment{
				{Path: "a.go", Line: 2, Side: "RIGHT", Body: "[阻塞] 越界访问\n\n建议：先校验长度"},
				{Path: "b.go", Line: 2, Side: "RIGHT", Body: "缺少错误透传\n\n影响：调用方无法区分错误原因"},
			},
		},
		{
			name: "只给旧 body 时原样使用",
			out:  `{"verdict":"fix","reason":"兼容旧字段","comments":[{"path":"a.go","line":2,"body":"旧格式完整正文"}]}`,
			want: []git.PRReviewComment{
				{Path: "a.go", Line: 2, Side: "RIGHT", Body: "旧格式完整正文"},
			},
		},
		{
			name: "只有 headline 时正文就是 headline",
			out:  `{"verdict":"comment","reason":"仅标题","comments":[{"path":"a.go","line":2,"severity":"suggestion","headline":"这里需要加锁"}]}`,
			want: []git.PRReviewComment{
				{Path: "a.go", Line: 2, Side: "RIGHT", Body: "这里需要加锁"},
			},
		},
		{
			name: "结构化正文全空时跳过",
			out:  `{"verdict":"merge","reason":"无有效正文","comments":[{"path":"a.go","line":2,"severity":"blocking","patch":"@@ -1 +1 @@"}]}`,
			want: []git.PRReviewComment{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePRAIReview(tc.out, prReviewTestPatch)
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if got == nil {
				t.Fatal("空结果也必须是空切片，不能是 nil")
			}
			if len(got) != len(tc.want) {
				t.Fatalf("条数 = %d, 期望 %d: %+v", len(got), len(tc.want), got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("第 %d 条 = %+v, 期望 %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestGitPRCreateValidation 验证空标题与空目标分支在调用任何 git 或 backend 之前就报错。
func TestGitPRCreateValidation(t *testing.T) {
	c := NewClient(nil)
	ctx := context.Background()

	for _, title := range []string{"", "   \t\n "} {
		if _, err := c.GitPRCreate(ctx, "no-thread", title, "body", "main"); err == nil || !strings.Contains(err.Error(), "标题") {
			t.Fatalf("空标题 (%q) err = %v, 期望返回标题校验错误", title, err)
		}
	}
	for _, base := range []string{"", "   \t\n "} {
		if _, err := c.GitPRCreate(ctx, "no-thread", "feat: add pr", "body", base); err == nil || !strings.Contains(err.Error(), "目标分支") {
			t.Fatalf("空目标分支 (%q) err = %v, 期望返回目标分支校验错误", base, err)
		}
	}
}

// TestGitPRCreateNoRemote 验证临时 git 仓库没有 remote 时返回 git.ErrForgeUnsupported。
func TestGitPRCreateNoRemote(t *testing.T) {
	c, _, thread := branchClient(t)
	if _, err := c.GitPRCreate(context.Background(), thread, "feat: add pr", "body", "main"); !errors.Is(err, git.ErrForgeUnsupported) {
		t.Fatalf("无 remote 时 err = %v, 期望 git.ErrForgeUnsupported", err)
	}
}
