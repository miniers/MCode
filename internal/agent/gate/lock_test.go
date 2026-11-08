package gate

import (
	"slices"
	"testing"
)

func TestCheckHardLockDefaults(t *testing.T) {
	hits := []struct {
		command string
		id      string
	}{
		{"gh pr merge 12", "merge"},
		{"git push --force origin feature", "push-force"},
		{"git push -f origin feature", "push-force"},
		{"git push -uf origin feature", "push-force"},
		{"git push --force-with-lease origin feature", "push-force"},
		{"git push origin main", "push-main"},
		{"git push origin master", "push-main"},
		{"git push --tags", "push-tags"},
		{"git push origin --tags", "push-tags"},
		{"git push --follow-tags", "push-tags"},
		{"cd repo && git push origin --follow-tags; echo done", "push-tags"},
		{"git tag v1 && git push origin v1", "tag-push"},
		{"npm publish", "publish"},
		{"bun publish", "publish"},
		{"cargo publish", "publish"},
		{"gh release create v1", "release"},
		{"gh secret set TOKEN", "secret"},
		{"gh repo delete owner/repo --yes", "repo-delete"},
		{"gh repo edit owner/repo --visibility private", "repo-edit"},
		{"rm -rf /", "destructive"},
		{"rm -rf ~", "destructive"},
		{"rm -fr ~/", "destructive"},
		{"cd repo && gh pr merge 1", "merge"},
		{"cd repo; git push -f", "push-force"},
		{"cd repo && npm publish", "publish"},
	}
	for _, tc := range hits {
		t.Run(tc.command, func(t *testing.T) {
			lock, hit := CheckHardLock(tc.command, "")
			if !hit {
				t.Fatalf("期望命中硬锁: %s", tc.command)
			}
			if lock.ID != tc.id {
				t.Fatalf("命中规则 = %s, want %s", lock.ID, tc.id)
			}
		})
	}
}

func TestCheckHardLockAllowsSafeCommands(t *testing.T) {
	for _, command := range []string{
		"git push origin feature-x",
		"git status",
		"npm test",
		"git tag -l",
		"git tag v1",
		"git push origin feature --tags-extra",
		"rm -rf /tmp/build",
		"gh pr view 12",
	} {
		if lock, hit := CheckHardLock(command, ""); hit {
			t.Fatalf("不应命中硬锁: %s（命中 %s）", command, lock.ID)
		}
	}
}

func TestCheckHardLockOverride(t *testing.T) {
	if lock, hit := CheckHardLock("gh pr merge 12", "D-42"); hit {
		t.Fatalf("带 override 时不应命中硬锁: %s", lock.ID)
	}
	if _, hit := CheckHardLock("gh pr merge 12", "  "); !hit {
		t.Fatal("空白 override 不应解锁")
	}
}

func TestClaudeDeniedPatterns(t *testing.T) {
	patterns := ClaudeDeniedPatterns()
	if !slices.Contains(patterns, "Bash(gh pr merge:*)") {
		t.Fatalf("缺少 gh pr merge 模式: %v", patterns)
	}
	if !slices.Contains(patterns, "Bash(git push --force:*)") {
		t.Fatalf("缺少强制推送模式: %v", patterns)
	}
	for _, pattern := range []string{
		"Bash(git push --tags:*)",
		"Bash(git push origin --tags:*)",
		"Bash(git push --follow-tags:*)",
	} {
		if !slices.Contains(patterns, pattern) {
			t.Fatalf("缺少标签推送模式 %q: %v", pattern, patterns)
		}
	}
	if slices.Contains(patterns, "Bash(git tag:*)") {
		t.Fatalf("不应禁止本地标签操作: %v", patterns)
	}
}
