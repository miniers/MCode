package git

import (
	"strings"
	"testing"
	"time"
)

// ghPRListJSON 是一份固定的 gh pr list --json 输出样例。
const ghPRListJSON = `[
  {
    "number": 42,
    "title": "feat: 只读 PR 视图",
    "state": "OPEN",
    "isDraft": false,
    "author": {"login": "octocat"},
    "headRefName": "feat/pr-view",
    "baseRefName": "main",
    "url": "https://github.com/acme/mcode/pull/42",
    "updatedAt": "2026-01-02T03:04:05Z",
    "reviewDecision": "APPROVED",
    "statusCheckRollup": [
      {"__typename": "CheckRun", "name": "build", "status": "COMPLETED", "conclusion": "SUCCESS"},
      {"__typename": "StatusContext", "context": "ci/lint", "state": "SUCCESS"}
    ]
  },
  {
    "number": 7,
    "title": "chore: 草稿",
    "state": "OPEN",
    "isDraft": true,
    "author": {"login": "draft-fan"},
    "headRefName": "chore/wip",
    "baseRefName": "main",
    "url": "https://github.com/acme/mcode/pull/7",
    "updatedAt": "2026-01-01T00:00:00Z",
    "reviewDecision": "",
    "statusCheckRollup": [
      {"__typename": "CheckRun", "name": "test", "status": "IN_PROGRESS", "conclusion": ""}
    ]
  }
]`

// TestParseGHPRList 覆盖：编号、作者、分支、URL、草稿、评审结论与检查汇总。
func TestParseGHPRList(t *testing.T) {
	items, err := parseGHPRList([]byte(ghPRListJSON))
	if err != nil {
		t.Fatalf("parseGHPRList 失败: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("条数 = %d, 期望 2: %+v", len(items), items)
	}

	first := items[0]
	if first.Number != 42 || first.Title != "feat: 只读 PR 视图" {
		t.Errorf("第一条 = #%d %q, 期望 #42 与标题", first.Number, first.Title)
	}
	if first.State != "open" || first.IsDraft {
		t.Errorf("第一条状态 = %q draft=%v, 期望 open 且非草稿", first.State, first.IsDraft)
	}
	if first.Author != "octocat" {
		t.Errorf("作者 = %q, 期望 octocat", first.Author)
	}
	if first.HeadBranch != "feat/pr-view" || first.BaseBranch != "main" {
		t.Errorf("分支 = %q -> %q, 期望 feat/pr-view -> main", first.HeadBranch, first.BaseBranch)
	}
	if first.URL != "https://github.com/acme/mcode/pull/42" {
		t.Errorf("URL = %q", first.URL)
	}
	if !first.UpdatedAt.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("更新时间 = %v", first.UpdatedAt)
	}
	if first.ReviewDecision != "APPROVED" {
		t.Errorf("评审结论 = %q, 期望 APPROVED", first.ReviewDecision)
	}
	if first.ChecksState != prChecksSuccess {
		t.Errorf("检查汇总 = %q, 期望 %q", first.ChecksState, prChecksSuccess)
	}

	second := items[1]
	if !second.IsDraft {
		t.Errorf("第二条应为草稿: %+v", second)
	}
	if second.ChecksState != prChecksPending {
		t.Errorf("草稿条目的检查汇总 = %q, 期望 %q", second.ChecksState, prChecksPending)
	}
}

// ghPRViewJSON 是一份固定的 gh pr view --json 输出样例。
const ghPRViewJSON = `{
  "number": 42,
  "title": "feat: 只读 PR 视图",
  "state": "OPEN",
  "isDraft": false,
  "author": {"login": "octocat"},
  "headRefName": "feat/pr-view",
  "baseRefName": "main",
  "url": "https://github.com/acme/mcode/pull/42",
  "updatedAt": "2026-01-02T03:04:05Z",
  "reviewDecision": "CHANGES_REQUESTED",
  "statusCheckRollup": [
    {"__typename": "CheckRun", "name": "build", "status": "COMPLETED", "conclusion": "FAILURE"},
    {"__typename": "StatusContext", "context": "ci/lint", "state": "PENDING"}
  ],
  "body": "  正文第一行\n正文第二行  ",
  "additions": 120,
  "deletions": 8,
  "changedFiles": 5,
  "comments": [
    {"author": {"login": "reviewer"}, "body": "这里要改一下", "createdAt": "2026-01-03T00:00:00Z"},
    {"author": {"login": "noise"}, "body": "   ", "createdAt": "2026-01-03T00:00:00Z"}
  ]
}`

// TestParseGHPRDetail 覆盖：正文去空白、增删行数、逐项检查与空评论过滤。
func TestParseGHPRDetail(t *testing.T) {
	detail, err := parseGHPRDetail([]byte(ghPRViewJSON))
	if err != nil {
		t.Fatalf("parseGHPRDetail 失败: %v", err)
	}
	if detail.Number != 42 || detail.ReviewDecision != "CHANGES_REQUESTED" {
		t.Errorf("摘要字段不对: %+v", detail.PRSummary)
	}
	if detail.Body != "正文第一行\n正文第二行" {
		t.Errorf("正文 = %q", detail.Body)
	}
	if detail.Additions != 120 || detail.Deletions != 8 || detail.ChangedFiles != 5 {
		t.Errorf("行数 = +%d -%d %d 文件", detail.Additions, detail.Deletions, detail.ChangedFiles)
	}
	if detail.ChecksState != prChecksFailure {
		t.Errorf("检查汇总 = %q, 期望 %q", detail.ChecksState, prChecksFailure)
	}
	if len(detail.Checks) != 2 {
		t.Fatalf("逐项检查条数 = %d, 期望 2: %+v", len(detail.Checks), detail.Checks)
	}
	if detail.Checks[0].Name != "build" || detail.Checks[0].State != prChecksFailure {
		t.Errorf("第一条检查 = %+v", detail.Checks[0])
	}
	// StatusContext 取 context 作名称，PENDING 归一化为进行中。
	if detail.Checks[1].Name != "ci/lint" || detail.Checks[1].State != prChecksPending {
		t.Errorf("第二条检查 = %+v", detail.Checks[1])
	}
	if len(detail.Comments) != 1 {
		t.Fatalf("评论条数 = %d, 期望 1（空正文被跳过）: %+v", len(detail.Comments), detail.Comments)
	}
	if detail.Comments[0].Author != "reviewer" || detail.Comments[0].Body != "这里要改一下" {
		t.Errorf("评论 = %+v", detail.Comments[0])
	}
	if !detail.Comments[0].CreatedAt.Equal(time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("评论时间 = %v", detail.Comments[0].CreatedAt)
	}
}

// glabMRListJSON 是一份固定的 glab mr list -F json 输出样例。
const glabMRListJSON = `[
  {
    "iid": 12,
    "title": "feat: 只读 MR 视图",
    "state": "opened",
    "draft": false,
    "author": {"username": "gitlab-user", "name": "GitLab User"},
    "source_branch": "feat/mr-view",
    "target_branch": "master",
    "web_url": "https://gitlab.com/acme/mcode/-/merge_requests/12",
    "updated_at": "2026-02-03T04:05:06Z",
    "description": "正文",
    "head_pipeline": {"status": "failed"}
  },
  {
    "iid": 13,
    "title": "草稿 MR",
    "state": "opened",
    "draft": true,
    "author": {"name": "只有名字"},
    "source_branch": "wip",
    "target_branch": "master",
    "web_url": "https://gitlab.com/acme/mcode/-/merge_requests/13",
    "updated_at": "",
    "description": ""
  }
]`

// TestParseGLabMRList 覆盖：iid 映射为编号、作者回退到 name、draft、流水线状态与缺失字段。
func TestParseGLabMRList(t *testing.T) {
	items, err := parseGLabMRList([]byte(glabMRListJSON))
	if err != nil {
		t.Fatalf("parseGLabMRList 失败: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("条数 = %d, 期望 2", len(items))
	}

	first := items[0]
	if first.Number != 12 || first.Title != "feat: 只读 MR 视图" {
		t.Errorf("第一条 = #%d %q", first.Number, first.Title)
	}
	if first.State != "open" {
		t.Errorf("状态 = %q, 期望 open", first.State)
	}
	if first.Author != "gitlab-user" {
		t.Errorf("作者 = %q, 期望 gitlab-user", first.Author)
	}
	if first.HeadBranch != "feat/mr-view" || first.BaseBranch != "master" {
		t.Errorf("分支 = %q -> %q", first.HeadBranch, first.BaseBranch)
	}
	if first.URL != "https://gitlab.com/acme/mcode/-/merge_requests/12" {
		t.Errorf("URL = %q", first.URL)
	}
	if first.ChecksState != prChecksFailure {
		t.Errorf("流水线汇总 = %q, 期望 %q", first.ChecksState, prChecksFailure)
	}
	// glab 没有评审结论字段，留空不报错。
	if first.ReviewDecision != "" {
		t.Errorf("评审结论 = %q, 期望为空", first.ReviewDecision)
	}

	second := items[1]
	if !second.IsDraft {
		t.Errorf("第二条应为草稿: %+v", second)
	}
	if second.Author != "只有名字" {
		t.Errorf("作者回退 = %q, 期望 只有名字", second.Author)
	}
	if second.ChecksState != prChecksNone {
		t.Errorf("无流水线时 = %q, 期望 %q", second.ChecksState, prChecksNone)
	}
	if !second.UpdatedAt.IsZero() {
		t.Errorf("空时间应解析为零值, 实际 %v", second.UpdatedAt)
	}
}

// glabMRViewJSON 是一份固定的 glab mr view -F json 输出样例。
const glabMRViewJSON = `{
  "iid": 12,
  "title": "feat: 只读 MR 视图",
  "state": "merged",
  "draft": false,
  "author": {"username": "gitlab-user"},
  "source_branch": "feat/mr-view",
  "target_branch": "master",
  "web_url": "https://gitlab.com/acme/mcode/-/merge_requests/12",
  "updated_at": "2026-02-03T04:05:06Z",
  "description": "  MR 正文  "
}`

// TestParseGLabMRDetail 覆盖：正文映射自 description，增删行数与检查留空。
func TestParseGLabMRDetail(t *testing.T) {
	detail, err := parseGLabMRDetail([]byte(glabMRViewJSON))
	if err != nil {
		t.Fatalf("parseGLabMRDetail 失败: %v", err)
	}
	if detail.Number != 12 || detail.State != "merged" {
		t.Errorf("摘要 = %+v", detail.PRSummary)
	}
	if detail.Body != "MR 正文" {
		t.Errorf("正文 = %q", detail.Body)
	}
	if detail.Additions != 0 || detail.Deletions != 0 || detail.ChangedFiles != 0 {
		t.Errorf("glab 不提供行数，期望全 0: +%d -%d %d", detail.Additions, detail.Deletions, detail.ChangedFiles)
	}
	if len(detail.Checks) != 0 || len(detail.Comments) != 0 {
		t.Errorf("glab 不提供检查与评论，期望为空: %+v %+v", detail.Checks, detail.Comments)
	}
}

// TestSummarizeCheckStates 覆盖四种汇总结果与未知状态的兜底。
func TestSummarizeCheckStates(t *testing.T) {
	cases := []struct {
		name   string
		states []string
		want   string
	}{
		{"没有检查", nil, prChecksNone},
		{"全部通过", []string{prChecksSuccess, prChecksSuccess}, prChecksSuccess},
		{"有一条失败", []string{prChecksSuccess, prChecksFailure}, prChecksFailure},
		{"有一条进行中", []string{prChecksSuccess, prChecksPending}, prChecksPending},
		{"失败优先于进行中", []string{prChecksPending, prChecksFailure}, prChecksFailure},
		{"未知状态按进行中", []string{prChecksSuccess, ""}, prChecksPending},
		{"大小写与空白", []string{" SUCCESS "}, prChecksSuccess},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := summarizeCheckStates(tc.states); got != tc.want {
				t.Errorf("summarizeCheckStates(%v) = %q, want %q", tc.states, got, tc.want)
			}
		})
	}
}

// TestNormalizePRState 覆盖各平台状态到统一状态的映射。
func TestNormalizePRState(t *testing.T) {
	cases := map[string]string{
		"OPEN":    "open",
		"opened":  "open",
		"MERGED":  "merged",
		"closed":  "closed",
		"LOCKED":  "locked",
		" weird ": "weird",
	}
	for in, want := range cases {
		if got := normalizePRState(in); got != want {
			t.Errorf("normalizePRState(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestParseGHPRListInvalid 覆盖：非法 JSON 返回中文错误而不是 panic。
func TestParseGHPRListInvalid(t *testing.T) {
	if _, err := parseGHPRList([]byte("not json")); err == nil {
		t.Fatal("非法 JSON 应返回错误")
	} else if !strings.Contains(err.Error(), "解析 gh pr list 输出失败") {
		t.Errorf("错误信息 = %q", err.Error())
	}
}
