package mcodeui

import (
	"testing"
	"time"

	"mcode/internal/git"
)

// TestGitPRStateLabel 覆盖状态与草稿标签的文案。
func TestGitPRStateLabel(t *testing.T) {
	cases := []struct {
		name  string
		state string
		draft bool
		want  string
	}{
		{"开放", "open", false, "开放"},
		{"大写开放", " OPEN ", false, "开放"},
		{"已关闭", "closed", false, "已关闭"},
		{"已合并", "merged", false, "已合并"},
		{"已锁定", "locked", false, "已锁定"},
		{"草稿优先于开放", "open", true, "草稿"},
		{"空状态", "", false, "未知状态"},
		{"未知状态原样显示", "archived", false, "archived"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gitPRStateLabel(tc.state, tc.draft); got != tc.want {
				t.Errorf("gitPRStateLabel(%q, %v) = %q, want %q", tc.state, tc.draft, got, tc.want)
			}
		})
	}
}

// TestGitPRChecksLabel 覆盖 CI 汇总状态的文案。
func TestGitPRChecksLabel(t *testing.T) {
	cases := map[string]string{
		"success":  "检查通过",
		" FAILURE": "检查失败",
		"pending":  "检查进行中",
		"none":     "无检查",
		"":         "检查未知",
		"weird":    "检查未知",
	}
	for in, want := range cases {
		if got := gitPRChecksLabel(in); got != want {
			t.Errorf("gitPRChecksLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestGitPRReviewLabel 覆盖评审结论的文案。
func TestGitPRReviewLabel(t *testing.T) {
	cases := map[string]string{
		"APPROVED":          "评审通过",
		"changes_requested": "评审要求修改",
		"REVIEW_REQUIRED":   "等待评审",
		"":                  "暂无评审",
		"other":             "评审未定",
	}
	for in, want := range cases {
		if got := gitPRReviewLabel(in); got != want {
			t.Errorf("gitPRReviewLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestGitPRChangeStats 覆盖改动统计的拼装。
func TestGitPRChangeStats(t *testing.T) {
	if got := gitPRChangeStats(120, 8, 5); got != "+120 · -8 · 5 个文件" {
		t.Errorf("gitPRChangeStats = %q", got)
	}
	// 文件数为 0（例如 glab 不提供）时只显示增删行数。
	if got := gitPRChangeStats(0, 0, 0); got != "+0 · -0" {
		t.Errorf("gitPRChangeStats(0,0,0) = %q", got)
	}
}

// TestGitPRMetaLine 覆盖元信息行的拼装与空字段跳过。
func TestGitPRMetaLine(t *testing.T) {
	full := git.PRSummary{
		Author:     "octocat",
		HeadBranch: "feat/pr-view",
		BaseBranch: "main",
		UpdatedAt:  time.Now().Add(-2 * time.Hour),
	}
	if got := gitPRMetaLine(full); got != "作者 octocat · feat/pr-view → main · 更新于 2小时前" {
		t.Errorf("gitPRMetaLine = %q", got)
	}
	// 只有目标分支时补一个箭头，时间零值不显示。
	if got := gitPRMetaLine(git.PRSummary{BaseBranch: "main"}); got != "→ main" {
		t.Errorf("gitPRMetaLine(BaseBranch) = %q", got)
	}
	if got := gitPRMetaLine(git.PRSummary{}); got != "" {
		t.Errorf("gitPRMetaLine(空) = %q, 期望空字符串", got)
	}
}

// TestGitPRUpdatedLabel 覆盖零值时间不显示。
func TestGitPRUpdatedLabel(t *testing.T) {
	if got := gitPRUpdatedLabel(time.Time{}); got != "" {
		t.Errorf("零值时间 = %q, 期望空字符串", got)
	}
	if got := gitPRUpdatedLabel(time.Now().Add(-30 * time.Second)); got != "刚刚" {
		t.Errorf("30 秒前 = %q, 期望 刚刚", got)
	}
}
