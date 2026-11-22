package git

import (
	"context"
	"errors"
	"reflect"
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
	t.Parallel()
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
  "isCrossRepository": false,
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
	t.Parallel()
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
	if detail.CrossRepo {
		t.Error("同仓库 PR 的 CrossRepo 应为 false")
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	if _, err := parseGHPRList([]byte("not json")); err == nil {
		t.Fatal("非法 JSON 应返回错误")
	} else if !strings.Contains(err.Error(), "解析 gh pr list 输出失败") {
		t.Errorf("错误信息 = %q", err.Error())
	}
}

// TestParsePRDetailCrossRepo 覆盖 gh 的 isCrossRepository 与 glab 的源/目标项目号比较。
func TestParsePRDetailCrossRepo(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, input string
		want        bool
		parse       func([]byte) (PRDetail, error)
	}{
		{"gh 同仓库", `{"isCrossRepository":false}`, false, parseGHPRDetail},
		{"gh fork", `{"isCrossRepository":true}`, true, parseGHPRDetail},
		{"gh 缺字段", `{}`, false, parseGHPRDetail},
		{"glab 同项目", `{"source_project_id":7,"target_project_id":7}`, false, parseGLabMRDetail},
		{"glab fork", `{"source_project_id":8,"target_project_id":7}`, true, parseGLabMRDetail},
		{"glab 缺字段", `{}`, false, parseGLabMRDetail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detail, err := tc.parse([]byte(tc.input))
			if err != nil || detail.CrossRepo != tc.want {
				t.Fatalf("CrossRepo = %v, err = %v，期望 %v", detail.CrossRepo, err, tc.want)
			}
		})
	}
}

func TestParsePRDetailMergeable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, input, want string
		parse             func([]byte) (PRDetail, error)
	}{
		{"gh mergeable", `{"mergeable":"mergeable"}`, "MERGEABLE", parseGHPRDetail},
		{"gh conflicting", `{"mergeable":"CONFLICTING"}`, "CONFLICTING", parseGHPRDetail},
		{"gh dirty", `{"mergeable":"MERGEABLE","mergeStateStatus":"DIRTY"}`, "CONFLICTING", parseGHPRDetail},
		{"gh unknown", `{"mergeable":"UNKNOWN"}`, "UNKNOWN", parseGHPRDetail},
		{"gh absent", `{}`, "", parseGHPRDetail},
		{"glab mergeable", `{"has_conflicts":false}`, "MERGEABLE", parseGLabMRDetail},
		{"glab conflicting", `{"has_conflicts":true,"merge_status":"can_be_merged"}`, "CONFLICTING", parseGLabMRDetail},
		{"glab status mergeable", `{"merge_status":"can_be_merged"}`, "MERGEABLE", parseGLabMRDetail},
		{"glab status conflicting", `{"merge_status":"cannot_be_merged"}`, "CONFLICTING", parseGLabMRDetail},
		{"glab checking", `{"has_conflicts":false,"merge_status":"checking"}`, "UNKNOWN", parseGLabMRDetail},
		{"glab absent", `{}`, "", parseGLabMRDetail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			detail, err := tc.parse([]byte(tc.input))
			if err != nil || detail.Mergeable != tc.want {
				t.Fatalf("Mergeable = %q, err = %v，期望 %q", detail.Mergeable, err, tc.want)
			}
		})
	}
}

// TestGHChecksURL 覆盖 CheckRun 取 detailsUrl、StatusContext 取 targetUrl，以及两者都缺失的情况。
func TestGHChecksURL(t *testing.T) {
	t.Parallel()
	checks := ghChecks([]ghCheck{
		{TypeName: "CheckRun", Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS", DetailsURL: "https://github.com/acme/repo/actions/runs/1"},
		{TypeName: "StatusContext", Context: "ci/lint", State: "SUCCESS", TargetURL: "https://ci.example.com/build/2"},
		{TypeName: "CheckRun", Name: "deploy", Status: "COMPLETED", Conclusion: "SUCCESS"},
		{TypeName: "StatusContext", Context: "ci/both", State: "SUCCESS", TargetURL: "https://target.example.com", DetailsURL: "https://details.example.com"},
	})
	if len(checks) != 4 {
		t.Fatalf("条数 = %d, 期望 4", len(checks))
	}
	if checks[0].URL != "https://github.com/acme/repo/actions/runs/1" {
		t.Errorf("CheckRun 链接 = %q", checks[0].URL)
	}
	if checks[1].URL != "https://ci.example.com/build/2" {
		t.Errorf("StatusContext 链接 = %q", checks[1].URL)
	}
	if checks[2].URL != "" {
		t.Errorf("没有链接时应为空，得到 %q", checks[2].URL)
	}
	if checks[3].URL != "https://details.example.com" {
		t.Errorf("两种链接都有时应优先 detailsUrl，得到 %q", checks[3].URL)
	}
}

// TestPRForBranchArgs 覆盖 gh 与 glab 的参数组装及未知平台报错。
func TestPRForBranchArgs(t *testing.T) {
	t.Parallel()
	ghArgs, err := prForBranchArgs(ForgeGitHub, "feat/pr-view")
	if err != nil {
		t.Fatalf("gh prForBranchArgs 失败: %v", err)
	}
	wantGH := []string{"pr", "list", "--head", "feat/pr-view", "--state", "all", "--limit", "1", "--json", ghPRListFields}
	if !reflect.DeepEqual(ghArgs, wantGH) {
		t.Errorf("gh 参数 = %v, 期望 %v", ghArgs, wantGH)
	}

	glabArgs, err := prForBranchArgs(ForgeGitLab, "feat/mr-view")
	if err != nil {
		t.Fatalf("glab prForBranchArgs 失败: %v", err)
	}
	wantGLab := []string{"mr", "list", "--source-branch", "feat/mr-view", "--all", "-F", "json"}
	if !reflect.DeepEqual(glabArgs, wantGLab) {
		t.Errorf("glab 参数 = %v, 期望 %v", glabArgs, wantGLab)
	}

	if _, err := prForBranchArgs(ForgeUnknown, "feat/x"); !errors.Is(err, ErrForgeUnsupported) {
		t.Errorf("未知平台 err = %v, 期望 ErrForgeUnsupported", err)
	}
}

// TestPRForBranchFirstSummary 覆盖空列表返回 nil，以及有数据时取第一条（含已合并状态）。
func TestPRForBranchFirstSummary(t *testing.T) {
	t.Parallel()

	emptyGH, err := parseGHPRList([]byte("[]"))
	if err != nil {
		t.Fatalf("parseGHPRList([]) 失败: %v", err)
	}
	if got := firstPRSummary(emptyGH); got != nil {
		t.Errorf("gh 空列表期望 nil, 得到 %+v", got)
	}

	emptyGLab, err := parseGLabMRList([]byte("[]"))
	if err != nil {
		t.Fatalf("parseGLabMRList([]) 失败: %v", err)
	}
	if got := firstPRSummary(emptyGLab); got != nil {
		t.Errorf("glab 空列表期望 nil, 得到 %+v", got)
	}

	ghItems, err := parseGHPRList([]byte(ghPRListJSON))
	if err != nil {
		t.Fatalf("parseGHPRList 失败: %v", err)
	}
	firstGH := firstPRSummary(ghItems)
	if firstGH == nil || firstGH.Number != 42 || firstGH.HeadBranch != "feat/pr-view" {
		t.Errorf("gh 第一条 = %+v, 期望 #42 feat/pr-view", firstGH)
	}

	mergedGH, err := parseGHPRList([]byte(`[{"number":99,"title":"merged pr","state":"MERGED","headRefName":"feat/done","baseRefName":"main","url":"https://github.com/acme/mcode/pull/99"}]`))
	if err != nil {
		t.Fatalf("parseGHPRList(merged) 失败: %v", err)
	}
	gotMerged := firstPRSummary(mergedGH)
	if gotMerged == nil || gotMerged.Number != 99 || gotMerged.State != "merged" {
		t.Errorf("已合并 PR = %+v, 期望 #99 merged", gotMerged)
	}

	glabItems, err := parseGLabMRList([]byte(glabMRListJSON))
	if err != nil {
		t.Fatalf("parseGLabMRList 失败: %v", err)
	}
	firstGLab := firstPRSummary(glabItems)
	if firstGLab == nil || firstGLab.Number != 12 || firstGLab.HeadBranch != "feat/mr-view" {
		t.Errorf("glab 第一条 = %+v, 期望 #12 feat/mr-view", firstGLab)
	}
}

// TestPRForBranchEmptyBranch 覆盖 branch 为空或纯空白时直接返回 nil 且不调 CLI。
func TestPRForBranchEmptyBranch(t *testing.T) {
	t.Parallel()
	var r *Runner
	for _, branch := range []string{"", "   \t\n "} {
		got, err := r.PRForBranch(context.Background(), "", branch)
		if err != nil || got != nil {
			t.Errorf("PRForBranch(%q) = (%+v, %v), 期望 (nil, nil)", branch, got, err)
		}
	}
}
