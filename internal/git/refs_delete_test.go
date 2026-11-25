package git

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestDeleteRefsWithPrefix(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git 可执行文件在 PATH 中未找到，跳过测试")
	}

	runner, dir := initGitRepo(t)
	ctx := context.Background()

	writeTestFile(t, dir, "a.txt", "hello\n")
	runTestGit(t, dir, "add", "a.txt")
	runTestGit(t, dir, "commit", "-qm", "initial")
	head := runTestGit(t, dir, "rev-parse", "HEAD")

	targetRefs := []string{
		"refs/mcode/swarm-base/lead1111",
		"refs/mcode/swarm-fold/lead1111/0",
		"refs/mcode/swarm-fold/lead1111/1",
		"refs/mcode/swarm-land/lead1111/0",
	}
	keepRefs := []string{
		"refs/mcode/swarm-base/lead2222",
		"refs/mcode/swarm-base/lead11110",
		"refs/mcode/swarm-fold/lead2222/0",
		"refs/mcode/swarm-land/lead2222/0",
		"refs/mcode/checkpoints/lead1111/turn-1",
	}
	for _, ref := range append(append([]string{}, targetRefs...), keepRefs...) {
		if err := runner.UpdateRef(ctx, dir, ref, head); err != nil {
			t.Fatalf("update-ref %s: %v", ref, err)
		}
	}

	t.Run("非法前缀报错且不删除引用", func(t *testing.T) {
		for _, bad := range []string{"", "refs/heads/", "refs/mcode", "refs/mcode/../heads/main", "refs/mcode/swarm-base/lead2222 bad"} {
			if err := runner.DeleteRefsWithPrefix(ctx, dir, bad); err == nil {
				t.Fatalf("DeleteRefsWithPrefix(%q) 应报错", bad)
			}
		}
		if err := runner.DeleteRefsWithPrefix(ctx, dir, "refs/mcode/swarm-base/lead2222", "refs/heads/"); err == nil {
			t.Fatal("混合非法前缀应报错")
		}
		if got := runTestGit(t, dir, "rev-parse", "refs/mcode/swarm-base/lead2222"); got != head {
			t.Fatalf("非法前缀报错后不应删除已有引用: %s", got)
		}
	})

	t.Run("无匹配前缀直接返回nil", func(t *testing.T) {
		if err := runner.DeleteRefsWithPrefix(ctx, dir); err != nil {
			t.Fatalf("空前缀列表应返回 nil: %v", err)
		}
		if err := runner.DeleteRefsWithPrefix(ctx, dir, "refs/mcode/swarm-base/missing"); err != nil {
			t.Fatalf("无匹配前缀应返回 nil: %v", err)
		}
	})

	t.Run("批量删除指定前缀且不误删其他前缀", func(t *testing.T) {
		if err := runner.DeleteRefsWithPrefix(
			ctx,
			dir,
			"refs/mcode/swarm-base/lead1111",
			"refs/mcode/swarm-fold/lead1111/",
			"refs/mcode/swarm-land/lead1111/",
		); err != nil {
			t.Fatalf("DeleteRefsWithPrefix failed: %v", err)
		}

		allRefs, err := runner.ListRefs(ctx, dir, "refs/")
		if err != nil {
			t.Fatalf("ListRefs failed: %v", err)
		}
		present := make(map[string]bool, len(allRefs))
		for _, r := range allRefs {
			present[r.Name] = true
		}

		for _, deleted := range targetRefs {
			if present[deleted] {
				t.Errorf("引用未被删除: %s", deleted)
			}
		}
		for _, kept := range keepRefs {
			if !present[kept] {
				t.Errorf("无关前缀引用被误删: %s", kept)
			}
		}
		if got := strings.TrimSpace(runTestGit(t, dir, "rev-parse", "HEAD")); got != head {
			t.Errorf("HEAD 不应受影响: got=%s want=%s", got, head)
		}
	})
}
