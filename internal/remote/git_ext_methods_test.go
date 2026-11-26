package remote

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"mcode/internal/git"
	"mcode/internal/remote/wire"
)

// gitExtTestThreadID 是测试用的合法会话 ID。
const gitExtTestThreadID = "11111111-1111-1111-1111-111111111111"

// gitExtFakeService 是 git 扩展服务（app 层注入）的假实现，记录入参供断言。
type gitExtFakeService struct {
	stashEntries   []git.StashEntry
	stashMessages  []string
	stashApplied   []int
	stashDropped   []int
	prs            []git.PRSummary
	prLimit        int
	prDetail       git.PRDetail
	prMethod       string
	prEvent        string
	prReviewBody   string
	prThreadID     string
	prDeadline     bool
	prCalls        int
	prNumber       int
	submitComments []git.PRReviewComment
	aiComments     []git.PRReviewComment
	err            error

	// 写操作：pushes 统计推送次数（含失败），pushErr 只让推送失败，
	// 其余字段记录入参与返回，供新方法的断言使用。
	pushes        int
	pushErr       error
	pulls         int
	fetches       int
	checkouts     []string
	createdBranch []string
	merged        []string
	commitMsg     string
	prURL         string
	prTitle       string
	prBody        string

	// PR 审阅会话状态、一键合并与新建审查会话。
	prThreadState     git.PRThreadState
	prThreadIsPR      bool
	prMergeFlowStep   string
	prMergeFlowMethod string
	prMergeFlowFrom   string
	prMergeFlowCalls  int
	prStartReviewID   string
	prStartReviewErr  error
	prStartCalls      int
}

func (s *gitExtFakeService) GitStashList(ctx context.Context, threadID string) ([]git.StashEntry, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.stashEntries, nil
}

func (s *gitExtFakeService) GitStashPush(ctx context.Context, threadID, message string) error {
	if s.err != nil {
		return s.err
	}
	s.stashMessages = append(s.stashMessages, message)
	return nil
}

func (s *gitExtFakeService) GitStashApply(ctx context.Context, threadID string, index int) error {
	if s.err != nil {
		return s.err
	}
	s.stashApplied = append(s.stashApplied, index)
	return nil
}

func (s *gitExtFakeService) GitStashDrop(ctx context.Context, threadID string, index int) error {
	if s.err != nil {
		return s.err
	}
	s.stashDropped = append(s.stashDropped, index)
	return nil
}

func (s *gitExtFakeService) GitPRList(ctx context.Context, threadID string, limit int) ([]git.PRSummary, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.prLimit = limit
	return s.prs, nil
}

func (s *gitExtFakeService) GitPRDetail(ctx context.Context, threadID string, number int) (git.PRDetail, error) {
	if s.err != nil {
		return git.PRDetail{}, s.err
	}
	s.prNumber = number
	return s.prDetail, nil
}

func (s *gitExtFakeService) GitPRMerge(ctx context.Context, threadID string, number int, method string) error {
	s.prCalls++
	s.prThreadID, s.prNumber, s.prMethod = threadID, number, method
	_, s.prDeadline = ctx.Deadline()
	return s.err
}

func (s *gitExtFakeService) GitPRReview(ctx context.Context, threadID string, number int, event, body string) error {
	s.prCalls++
	s.prThreadID, s.prNumber, s.prEvent, s.prReviewBody = threadID, number, event, body
	_, s.prDeadline = ctx.Deadline()
	return s.err
}

func (s *gitExtFakeService) GitPRSubmitReview(ctx context.Context, threadID string, number int, event, body string, comments []git.PRReviewComment) error {
	s.prCalls++
	s.prThreadID, s.prNumber, s.prEvent, s.prReviewBody = threadID, number, event, body
	s.submitComments = comments
	_, s.prDeadline = ctx.Deadline()
	return s.err
}

func (s *gitExtFakeService) GitPRAIReview(ctx context.Context, threadID string, number int) ([]git.PRReviewComment, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.prCalls++
	s.prThreadID, s.prNumber = threadID, number
	_, s.prDeadline = ctx.Deadline()
	return s.aiComments, nil
}

func (s *gitExtFakeService) GitPush(ctx context.Context, threadID string) error {
	s.pushes++
	if s.err != nil {
		return s.err
	}
	return s.pushErr
}

func (s *gitExtFakeService) GitPull(ctx context.Context, threadID string) error {
	if s.err != nil {
		return s.err
	}
	s.pulls++
	return nil
}

func (s *gitExtFakeService) GitFetch(ctx context.Context, threadID string) error {
	if s.err != nil {
		return s.err
	}
	s.fetches++
	return nil
}

func (s *gitExtFakeService) GitCheckout(ctx context.Context, threadID, branch string) error {
	if s.err != nil {
		return s.err
	}
	s.checkouts = append(s.checkouts, branch)
	return nil
}

func (s *gitExtFakeService) GitCreateBranch(ctx context.Context, threadID, name string) error {
	if s.err != nil {
		return s.err
	}
	s.createdBranch = append(s.createdBranch, name)
	return nil
}

func (s *gitExtFakeService) GitMergeFrom(ctx context.Context, threadID, branch string) error {
	if s.err != nil {
		return s.err
	}
	s.merged = append(s.merged, branch)
	return nil
}

func (s *gitExtFakeService) GitGenerateCommitMessage(ctx context.Context, threadID string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.commitMsg, nil
}

func (s *gitExtFakeService) GitPullRequestCreateURL(ctx context.Context, threadID string) (string, string, string, error) {
	if s.err != nil {
		return "", "", "", s.err
	}
	return s.prURL, s.prTitle, s.prBody, nil
}

func (s *gitExtFakeService) GitPRThreadState(ctx context.Context, threadID string) (git.PRThreadState, bool, error) {
	s.prCalls++
	s.prThreadID = threadID
	_, s.prDeadline = ctx.Deadline()
	if s.err != nil {
		return git.PRThreadState{}, false, s.err
	}
	return s.prThreadState, s.prThreadIsPR, nil
}

func (s *gitExtFakeService) GitPRMergeFlow(ctx context.Context, threadID string, number int, method, from string, onStep func(step string)) (string, error) {
	s.prMergeFlowCalls++
	s.prCalls++
	s.prThreadID, s.prNumber, s.prMergeFlowMethod, s.prMergeFlowFrom = threadID, number, method, from
	_, s.prDeadline = ctx.Deadline()
	if s.err != nil {
		return s.prMergeFlowStep, s.err
	}
	return "", nil
}

func (s *gitExtFakeService) GitPRStartReview(ctx context.Context, threadID string, number int) (string, error) {
	s.prStartCalls++
	s.prCalls++
	s.prThreadID, s.prNumber = threadID, number
	_, s.prDeadline = ctx.Deadline()
	if s.prStartReviewErr != nil {
		return s.prStartReviewID, s.prStartReviewErr
	}
	if s.err != nil {
		return "", s.err
	}
	return s.prStartReviewID, nil
}

// gitExtTestCommit 记录一次只提交部分路径的调用。
type gitExtTestCommit struct {
	Paths   []string
	Message string
}

// gitExtTestBackend 只实现扩展方法用到的内存后端能力与命名服务注册表，
// 其余 Backend 方法由内嵌的 nil 接口兜底（测试里不会被调用）。
type gitExtTestBackend struct {
	Backend
	files    []git.ChangedFile
	commits  []gitExtTestCommit
	stashPop int
	err      error
	services map[string]any
}

func (b *gitExtTestBackend) GitChangedFiles(ctx context.Context, threadID string) ([]git.ChangedFile, error) {
	if b.err != nil {
		return nil, b.err
	}
	return b.files, nil
}

func (b *gitExtTestBackend) GitCommitSelected(ctx context.Context, threadID string, paths []string, message string) error {
	if b.err != nil {
		return b.err
	}
	b.commits = append(b.commits, gitExtTestCommit{Paths: paths, Message: message})
	return nil
}

func (b *gitExtTestBackend) GitStashPop(ctx context.Context, threadID string) error {
	if b.err != nil {
		return b.err
	}
	b.stashPop++
	return nil
}

func (b *gitExtTestBackend) Service(name string) any {
	if b.services == nil {
		return nil
	}
	return b.services[name]
}

// newGitExtTestDispatcher 构造一个使用指定后端的 Dispatcher。
func newGitExtTestDispatcher(backend Backend) (*Dispatcher, Conn) {
	return NewDispatcher(&fakeAuth{}, backend), &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "d-ext"}}
}

// gitExtResult 把扩展方法的成功结果解码为目标类型。
func gitExtResult(t *testing.T, resp wire.Response, v any) {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("unmarshal result: %v (raw=%s)", err, raw)
	}
}

func TestGitExtMethodsRegistered(t *testing.T) {
	want := []string{
		"git.changedFiles",
		"git.commitSelected",
		"git.stash.list",
		"git.stash.push",
		"git.stash.pop",
		"git.stash.apply",
		"git.stash.drop",
		"git.pr.list",
		"git.pr.detail",
		"git.pr.threadState",
		"git.pr.mergeFlow",
		"git.pr.startReview",
		"git.push",
		"git.pull",
		"git.fetch",
		"git.checkout",
		"git.createBranch",
		"git.merge",
		"git.generateCommitMessage",
		"git.pr.createUrl",
	}
	have := make(map[string]bool, len(extensionMethodNames()))
	for _, name := range extensionMethodNames() {
		have[name] = true
	}
	for _, name := range want {
		if !have[name] {
			t.Fatalf("method %s should be registered, got %v", name, extensionMethodNames())
		}
	}
}

func TestGitExtChangedFiles(t *testing.T) {
	backend := &gitExtTestBackend{files: []git.ChangedFile{
		{Path: "internal/a.go", Status: "M"},
		{Path: "internal/b.go", OrigPath: "internal/old.go", Status: "R"},
	}}
	d, conn := newGitExtTestDispatcher(backend)

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.changedFiles","params":{"threadID":"`+gitExtTestThreadID+`"},"id":1}`)
	var res wire.GitExtChangedFilesResult
	gitExtResult(t, resp, &res)
	if res.ThreadID != gitExtTestThreadID {
		t.Fatalf("unexpected threadID: %q", res.ThreadID)
	}
	if len(res.Files) != 2 {
		t.Fatalf("expected 2 files, got %+v", res.Files)
	}
	if res.Files[0].Path != "internal/a.go" || res.Files[0].Status != "M" || res.Files[0].OrigPath != "" {
		t.Fatalf("unexpected first file: %+v", res.Files[0])
	}
	if res.Files[1].OrigPath != "internal/old.go" || res.Files[1].Status != "R" {
		t.Fatalf("unexpected second file: %+v", res.Files[1])
	}

	// threadID 不是 UUID
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.changedFiles","params":{"threadID":"nope"},"id":2}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams, got: %+v", resp.Error)
	}

	// 未知字段被严格解码拒绝
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.changedFiles","params":{"threadID":"`+gitExtTestThreadID+`","extra":1},"id":3}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams for unknown field, got: %+v", resp.Error)
	}

	// 后端没有该能力时按未实现处理
	d2, conn2 := newGitExtTestDispatcher(&fakeBackend{})
	resp = callExtension(t, d2, conn2, `{"jsonrpc":"2.0","method":"git.changedFiles","params":{"threadID":"`+gitExtTestThreadID+`"},"id":4}`)
	if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
		t.Fatalf("expected MethodNotFound, got: %+v", resp.Error)
	}
}

func TestGitExtCommitSelected(t *testing.T) {
	backend := &gitExtTestBackend{}
	d, conn := newGitExtTestDispatcher(backend)

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.commitSelected","params":{"threadID":"`+gitExtTestThreadID+`","paths":[" a.go ","","b.go"],"message":"  fix: 分文件提交  "},"id":1}`)
	var res wire.GitExtCommitSelectedResult
	gitExtResult(t, resp, &res)
	if !res.OK {
		t.Fatalf("expected ok result, got %+v", res)
	}
	if len(backend.commits) != 1 {
		t.Fatalf("expected 1 commit, got %+v", backend.commits)
	}
	if got := backend.commits[0]; got.Message != "fix: 分文件提交" || len(got.Paths) != 2 || got.Paths[0] != "a.go" || got.Paths[1] != "b.go" {
		t.Fatalf("unexpected commit args: %+v", got)
	}

	// paths 为空
	for _, params := range []string{
		`{"threadID":"` + gitExtTestThreadID + `","paths":[],"message":"m"}`,
		`{"threadID":"` + gitExtTestThreadID + `","paths":["  "],"message":"m"}`,
	} {
		resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.commitSelected","params":`+params+`,"id":2}`)
		if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
			t.Fatalf("expected InvalidParams for empty paths, got: %+v", resp.Error)
		}
	}

	// message 全为空白
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.commitSelected","params":{"threadID":"`+gitExtTestThreadID+`","paths":["a.go"],"message":"   "},"id":3}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams for blank message, got: %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "'message'") {
		t.Fatalf("unexpected message: %q", resp.Error.Message)
	}

	// git 侧错误原样回给前端
	backend.err = errors.New("fatal: 无法创建 '.git/index.lock': 文件已存在")
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.commitSelected","params":{"threadID":"`+gitExtTestThreadID+`","paths":["a.go"],"message":"m"},"id":4}`)
	if resp.Error == nil || resp.Error.Code != wire.InternalError {
		t.Fatalf("expected InternalError, got: %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "index.lock") {
		t.Fatalf("original error text should be preserved, got %q", resp.Error.Message)
	}
}

func TestGitExtStashMethods(t *testing.T) {
	created := time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC)
	svc := &gitExtFakeService{stashEntries: []git.StashEntry{
		{Index: 0, Ref: "stash@{0}", Message: "WIP 说明", Branch: "main", CreatedAt: created},
	}}
	backend := &gitExtTestBackend{services: map[string]any{GitExtServiceName: svc}}
	d, conn := newGitExtTestDispatcher(backend)

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.stash.list","params":{"threadID":"`+gitExtTestThreadID+`"},"id":1}`)
	var list wire.GitExtStashListResult
	gitExtResult(t, resp, &list)
	if len(list.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %+v", list.Entries)
	}
	if entry := list.Entries[0]; entry.Index != 0 || entry.Ref != "stash@{0}" || entry.Branch != "main" || entry.CreatedAt != created.Format(time.RFC3339) {
		t.Fatalf("unexpected entry: %+v", entry)
	}

	// push 的说明文字会去掉首尾空白，未填时为空说明
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.stash.push","params":{"threadID":"`+gitExtTestThreadID+`","message":"  before rebase  "},"id":2}`)
	var action wire.GitExtStashActionResult
	gitExtResult(t, resp, &action)
	if !action.OK {
		t.Fatalf("expected ok, got %+v", action)
	}
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.stash.push","params":{"threadID":"`+gitExtTestThreadID+`"},"id":3}`)
	gitExtResult(t, resp, &action)
	if len(svc.stashMessages) != 2 || svc.stashMessages[0] != "before rebase" || svc.stashMessages[1] != "" {
		t.Fatalf("unexpected stash push messages: %+v", svc.stashMessages)
	}

	// pop 走 MemoryBackend
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.stash.pop","params":{"threadID":"`+gitExtTestThreadID+`"},"id":4}`)
	gitExtResult(t, resp, &action)
	if backend.stashPop != 1 {
		t.Fatalf("expected backend stash pop once, got %d", backend.stashPop)
	}

	// apply / drop 按序号走服务
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.stash.apply","params":{"threadID":"`+gitExtTestThreadID+`","index":2},"id":5}`)
	gitExtResult(t, resp, &action)
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.stash.drop","params":{"threadID":"`+gitExtTestThreadID+`","index":3},"id":6}`)
	gitExtResult(t, resp, &action)
	if len(svc.stashApplied) != 1 || svc.stashApplied[0] != 2 {
		t.Fatalf("unexpected stash apply: %+v", svc.stashApplied)
	}
	if len(svc.stashDropped) != 1 || svc.stashDropped[0] != 3 {
		t.Fatalf("unexpected stash drop: %+v", svc.stashDropped)
	}

	// 负序号是参数错误
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.stash.drop","params":{"threadID":"`+gitExtTestThreadID+`","index":-1},"id":7}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams for negative index, got: %+v", resp.Error)
	}

	// 未注入服务时贮藏列表按未实现处理
	d2, conn2 := newGitExtTestDispatcher(&gitExtTestBackend{})
	resp = callExtension(t, d2, conn2, `{"jsonrpc":"2.0","method":"git.stash.list","params":{"threadID":"`+gitExtTestThreadID+`"},"id":8}`)
	if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
		t.Fatalf("expected MethodNotFound, got: %+v", resp.Error)
	}
}

func TestGitExtPRMethods(t *testing.T) {
	updated := time.Date(2025, 4, 5, 6, 7, 8, 0, time.UTC)
	svc := &gitExtFakeService{
		prs: []git.PRSummary{{
			Number: 12, Title: "拆分 git 面板", State: "open", Author: "larch",
			HeadBranch: "feat/git", BaseBranch: "main", URL: "https://example.com/pr/12",
			UpdatedAt: updated, ReviewDecision: "APPROVED", ChecksState: "success",
		}},
		prDetail: git.PRDetail{
			PRSummary:    git.PRSummary{Number: 12, Title: "拆分 git 面板", Author: "larch"},
			Body:         "正文",
			Mergeable:    "CONFLICTING",
			Additions:    10,
			Deletions:    2,
			ChangedFiles: 3,
			Checks:       []git.PRCheck{{Name: "build", State: "success"}},
			Comments:     []git.PRComment{{Author: "bee", Body: "lgtm", CreatedAt: updated}},
		},
	}
	backend := &gitExtTestBackend{services: map[string]any{GitExtServiceName: svc}}
	d, conn := newGitExtTestDispatcher(backend)

	// 未指定 limit 时默认 20
	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.pr.list","params":{"threadID":"`+gitExtTestThreadID+`"},"id":1}`)
	var list wire.GitExtPRListResult
	gitExtResult(t, resp, &list)
	if svc.prLimit != gitExtPRListDefaultLimit {
		t.Fatalf("expected default limit %d, got %d", gitExtPRListDefaultLimit, svc.prLimit)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected 1 item, got %+v", list.Items)
	}
	if item := list.Items[0]; item.Number != 12 || item.HeadBranch != "feat/git" || item.URL != "https://example.com/pr/12" || item.UpdatedAt != updated.Format(time.RFC3339) {
		t.Fatalf("unexpected item: %+v", item)
	}

	// limit 超过上限时收敛到上限
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.pr.list","params":{"threadID":"`+gitExtTestThreadID+`","limit":500},"id":2}`)
	gitExtResult(t, resp, &list)
	if svc.prLimit != gitExtPRListMaxLimit {
		t.Fatalf("expected clamped limit %d, got %d", gitExtPRListMaxLimit, svc.prLimit)
	}

	// 详情
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.pr.detail","params":{"threadID":"`+gitExtTestThreadID+`","number":12},"id":3}`)
	var detail wire.GitExtPRDetail
	gitExtResult(t, resp, &detail)
	if svc.prNumber != 12 || detail.Title != "拆分 git 面板" || detail.Additions != 10 || detail.Mergeable != "CONFLICTING" {
		t.Fatalf("unexpected detail: %+v (service number=%d)", detail, svc.prNumber)
	}
	if len(detail.Checks) != 1 || detail.Checks[0].Name != "build" {
		t.Fatalf("unexpected checks: %+v", detail.Checks)
	}
	if len(detail.Comments) != 1 || detail.Comments[0].Body != "lgtm" {
		t.Fatalf("unexpected comments: %+v", detail.Comments)
	}

	// 编号必须为正
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.pr.detail","params":{"threadID":"`+gitExtTestThreadID+`","number":0},"id":4}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams for number 0, got: %+v", resp.Error)
	}

	// gh / glab 缺失时保留原始错误文案
	svc.err = errors.New("未找到 gh 命令，请先在 Mac 上安装 GitHub CLI")
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.pr.list","params":{"threadID":"`+gitExtTestThreadID+`"},"id":5}`)
	if resp.Error == nil || resp.Error.Code != wire.InternalError {
		t.Fatalf("expected InternalError, got: %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "gh") {
		t.Fatalf("original error text should be preserved, got %q", resp.Error.Message)
	}
}

func TestGitExtPRWriteMethods(t *testing.T) {
	for _, tc := range []struct {
		method, field string
		values        []string
	}{
		{"git.pr.merge", "method", []string{"squash", "merge", "rebase"}},
		{"git.pr.review", "event", []string{"approve", "request-changes", "comment"}},
	} {
		for _, value := range tc.values {
			t.Run(tc.method+"/"+value, func(t *testing.T) {
				svc := &gitExtFakeService{}
				backend := &gitExtTestBackend{services: map[string]any{GitExtServiceName: svc}}
				d, conn := newGitExtTestDispatcher(backend)
				params := map[string]any{"threadID": gitExtTestThreadID, "number": 12, tc.field: value}
				if tc.method == "git.pr.review" {
					params["body"] = "正文"
				}
				request, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": tc.method, "params": params})
				resp := callExtension(t, d, conn, string(request))
				var result wire.GitExtWriteActionResult
				gitExtResult(t, resp, &result)
				if !result.OK || svc.prCalls != 1 || svc.prThreadID != gitExtTestThreadID || svc.prNumber != 12 || !svc.prDeadline {
					t.Fatalf("调用不符: %+v %+v", result, svc)
				}
				if tc.field == "method" && svc.prMethod != value {
					t.Fatalf("合并方式 = %q", svc.prMethod)
				}
				if tc.field == "event" && (svc.prEvent != value || svc.prReviewBody != "正文") {
					t.Fatalf("审查参数不符: %+v", svc)
				}
				svc.err = errors.New("forge 写入失败")
				resp = callExtension(t, d, conn, string(request))
				if resp.Error == nil || resp.Error.Code != wire.InternalError {
					t.Fatalf("错误未返回: %+v", resp)
				}
				backend.services = nil
				resp = callExtension(t, d, conn, string(request))
				if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
					t.Fatalf("缺少服务错误不符: %+v", resp)
				}
			})
		}
	}
}

func TestGitExtPRSubmitReview(t *testing.T) {
	svc := &gitExtFakeService{}
	d, conn := newGitExtTestDispatcher(&gitExtTestBackend{services: map[string]any{GitExtServiceName: svc}})
	request := `{"jsonrpc":"2.0","id":1,"method":"git.pr.submitReview","params":{"threadID":"` + gitExtTestThreadID + `","number":4,"event":"request-changes","comments":[{"path":"a.go","line":7,"side":"RIGHT","body":"fix"}]}}`
	resp := callExtension(t, d, conn, request)
	var result wire.GitExtWriteActionResult
	gitExtResult(t, resp, &result)
	if !result.OK || svc.prCalls != 1 || svc.prNumber != 4 || len(svc.submitComments) != 1 || !svc.prDeadline {
		t.Fatalf("提交参数不符: %+v %+v", result, svc)
	}
}

func TestGitExtPRWriteInvalidParams(t *testing.T) {
	for _, tc := range []struct{ method, params string }{
		{"git.pr.merge", `{"number":12,"method":"squash"}`},
		{"git.pr.merge", `{"threadID":" ","number":12,"method":"squash"}`},
		{"git.pr.merge", `{"threadID":"` + gitExtTestThreadID + `","number":0,"method":"squash"}`},
		{"git.pr.merge", `{"threadID":"` + gitExtTestThreadID + `","number":-1,"method":"squash"}`},
		{"git.pr.merge", `{"threadID":"` + gitExtTestThreadID + `","number":12}`},
		{"git.pr.merge", `{"threadID":"` + gitExtTestThreadID + `","number":12,"method":"invalid"}`},
		{"git.pr.review", `{"number":12,"event":"approve"}`},
		{"git.pr.review", `{"threadID":"` + gitExtTestThreadID + `","number":0,"event":"approve"}`},
		{"git.pr.review", `{"threadID":"` + gitExtTestThreadID + `","number":12,"event":"invalid"}`},
		{"git.pr.review", `{"threadID":"` + gitExtTestThreadID + `","number":12,"event":"comment"}`},
		{"git.pr.review", `{"threadID":"` + gitExtTestThreadID + `","number":12,"event":"request-changes","body":"  "}`},
		{"git.pr.review", `{"threadID":"` + gitExtTestThreadID + `","number":"12","event":"approve"}`},
	} {
		t.Run(tc.method+tc.params, func(t *testing.T) {
			svc := &gitExtFakeService{}
			d, conn := newGitExtTestDispatcher(&gitExtTestBackend{services: map[string]any{GitExtServiceName: svc}})
			resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","id":1,"method":"`+tc.method+`","params":`+tc.params+`}`)
			if resp.Error == nil || resp.Error.Code != wire.InvalidParams || svc.prCalls != 0 {
				t.Fatalf("无效参数未拦截: %+v，调用 %d 次", resp, svc.prCalls)
			}
		})
	}
}

func TestGitExtPRAIReview(t *testing.T) {
	svc := &gitExtFakeService{aiComments: []git.PRReviewComment{{Path: "a.go", Line: 3, Side: "RIGHT", Body: "新增的函数没有判空"}}}
	d, conn := newGitExtTestDispatcher(&gitExtTestBackend{services: map[string]any{GitExtServiceName: svc}})
	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","id":1,"method":"git.pr.aiReview","params":{"threadID":"`+gitExtTestThreadID+`","number":7}}`)
	var res wire.GitExtPRAIReviewResult
	gitExtResult(t, resp, &res)
	if len(res.Comments) != 1 || res.Comments[0].Path != "a.go" || res.Comments[0].Line != 3 || res.Comments[0].Side != "RIGHT" || res.Comments[0].Body != "新增的函数没有判空" {
		t.Fatalf("评论结果不符: %+v", res.Comments)
	}
	if svc.prCalls != 1 || svc.prNumber != 7 || !svc.prDeadline {
		t.Fatalf("调用参数不符: calls=%d number=%d deadline=%v", svc.prCalls, svc.prNumber, svc.prDeadline)
	}

	// 没有意见时返回空数组，而不是省略或 null。
	empty := &gitExtFakeService{}
	d2, conn2 := newGitExtTestDispatcher(&gitExtTestBackend{services: map[string]any{GitExtServiceName: empty}})
	resp = callExtension(t, d2, conn2, `{"jsonrpc":"2.0","id":2,"method":"git.pr.aiReview","params":{"threadID":"`+gitExtTestThreadID+`","number":8}}`)
	res.Comments = nil
	gitExtResult(t, resp, &res)
	if res.Comments == nil || len(res.Comments) != 0 {
		t.Fatalf("空结果 = %+v, 期望空数组", res.Comments)
	}

	// 参数校验与 submitReview 一致。
	for _, params := range []string{
		`{"number":7}`,
		`{"threadID":" ","number":7}`,
		`{"threadID":"` + gitExtTestThreadID + `","number":0}`,
		`{"threadID":"` + gitExtTestThreadID + `","number":-1}`,
		`{"threadID":"` + gitExtTestThreadID + `","number":"7"}`,
		`{"threadID":"` + gitExtTestThreadID + `","number":7,"extra":1}`,
	} {
		t.Run(params, func(t *testing.T) {
			before := empty.prCalls
			resp := callExtension(t, d2, conn2, `{"jsonrpc":"2.0","id":3,"method":"git.pr.aiReview","params":`+params+`}`)
			if resp.Error == nil || resp.Error.Code != wire.InvalidParams || empty.prCalls != before {
				t.Fatalf("无效参数未拦截: %+v", resp.Error)
			}
		})
	}
}

func TestGitExtPRWriteSchema(t *testing.T) {
	data, err := os.ReadFile("../../docs/remote-control.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"RemoteGitPRMerge",
		"RemoteGitPRReview",
		"RemoteGitPRAIReview",
		"RemoteGitPRThreadState",
		"RemoteGitPRMergeFlow",
		"RemoteGitPRStartReview",
	} {
		for _, kind := range []string{"Request", "Response"} {
			if strings.Count(string(data), `"$ref": "#/$defs/`+name+kind+`"`) != 2 {
				t.Fatalf("%s%s 未加入顶层和批量 schema", name, kind)
			}
		}
		var request struct {
			Properties struct {
				Method struct {
					Const string `json:"const"`
				} `json:"method"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(doc.Defs[name+"Request"], &request); err != nil {
			t.Fatal(err)
		}
		registered := false
		for _, method := range ImplementedMethods() {
			if method == request.Properties.Method.Const {
				registered = true
				break
			}
		}
		if !registered {
			t.Fatalf("schema 方法未注册: %s", request.Properties.Method.Const)
		}
		for _, kind := range []string{"Params", "Request", "Response"} {
			if len(doc.Defs[name+kind]) == 0 {
				t.Fatalf("schema 缺少 %s%s", name, kind)
			}
		}
	}
}

func TestGitExtPRThreadState(t *testing.T) {
	svc := &gitExtFakeService{
		prThreadIsPR: true,
		prThreadState: git.PRThreadState{
			Number:       42,
			CrossRepo:    true,
			Dirty:        true,
			LocalCommits: 3,
		},
	}
	backend := &gitExtTestBackend{services: map[string]any{GitExtServiceName: svc}}
	d, conn := newGitExtTestDispatcher(backend)

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","id":1,"method":"git.pr.threadState","params":{"threadID":"`+gitExtTestThreadID+`"}}`)
	var res wire.GitExtPRThreadStateResult
	gitExtResult(t, resp, &res)
	if !res.IsPR || res.Number != 42 || !res.CrossRepo || !res.Dirty || res.LocalCommits != 3 || !svc.prDeadline {
		t.Fatalf("状态结果不符: %+v svc=%+v", res, svc)
	}

	// 非 PR 分支时返回 isPR=false 与零值字段。
	svc.prThreadIsPR = false
	svc.prThreadState = git.PRThreadState{}
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","id":2,"method":"git.pr.threadState","params":{"threadID":"`+gitExtTestThreadID+`"}}`)
	gitExtResult(t, resp, &res)
	if res.IsPR || res.Number != 0 || res.CrossRepo || res.Dirty || res.LocalCommits != 0 {
		t.Fatalf("非 PR 状态不符: %+v", res)
	}

	// 参数校验失败返回 InvalidParams。
	for _, params := range []string{`{}`, `{"threadID":"bad"}`, `{"threadID":"` + gitExtTestThreadID + `","extra":1}`} {
		resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","id":3,"method":"git.pr.threadState","params":`+params+`}`)
		if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
			t.Fatalf("无效参数未拦截 (%s): %+v", params, resp.Error)
		}
	}

	// 未注入服务返回 MethodNotFound。
	backend.services = nil
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","id":4,"method":"git.pr.threadState","params":{"threadID":"`+gitExtTestThreadID+`"}}`)
	if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
		t.Fatalf("缺少服务错误不符: %+v", resp.Error)
	}
}

func TestGitExtPRMergeFlow(t *testing.T) {
	svc := &gitExtFakeService{}
	backend := &gitExtTestBackend{services: map[string]any{GitExtServiceName: svc}}
	d, conn := newGitExtTestDispatcher(backend)

	// 正常路径（不传 from 与传入合法 from）。
	for _, tc := range []struct {
		method string
		from   string
	}{
		{"squash", ""},
		{"merge", "push"},
		{"rebase", "sync"},
	} {
		extra := ""
		if tc.from != "" {
			extra = `,"from":"` + tc.from + `"`
		}
		resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","id":1,"method":"git.pr.mergeFlow","params":{"threadID":"`+gitExtTestThreadID+`","number":9,"method":"`+tc.method+`"`+extra+`}}`)
		var res wire.GitExtPRMergeFlowResult
		gitExtResult(t, resp, &res)
		if !res.OK || res.FailedStep != "" || res.Message != "" || !svc.prDeadline {
			t.Fatalf("正常路径结果不符: %+v", res)
		}
		if svc.prMergeFlowMethod != tc.method || svc.prMergeFlowFrom != tc.from || svc.prNumber != 9 {
			t.Fatalf("调用参数不符: method=%q from=%q number=%d", svc.prMergeFlowMethod, svc.prMergeFlowFrom, svc.prNumber)
		}
	}

	// 流程失败不回 JSON-RPC 错误，而是 ok=false + failedStep + message。
	svc.prMergeFlowStep = "merge"
	svc.err = errors.New("gh merge failed")
	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","id":2,"method":"git.pr.mergeFlow","params":{"threadID":"`+gitExtTestThreadID+`","number":9,"method":"squash"}}`)
	var failRes wire.GitExtPRMergeFlowResult
	gitExtResult(t, resp, &failRes)
	if failRes.OK || failRes.FailedStep != "merge" || !strings.Contains(failRes.Message, "gh merge failed") {
		t.Fatalf("失败结果不符: %+v", failRes)
	}

	// 非法参数返回 InvalidParams 且不调用服务。
	for _, params := range []string{
		`{"number":9,"method":"squash"}`,
		`{"threadID":"bad","number":9,"method":"squash"}`,
		`{"threadID":"` + gitExtTestThreadID + `","number":0,"method":"squash"}`,
		`{"threadID":"` + gitExtTestThreadID + `","number":9,"method":"fast-forward"}`,
		`{"threadID":"` + gitExtTestThreadID + `","number":9,"method":"squash","from":"unknown"}`,
		`{"threadID":"` + gitExtTestThreadID + `","number":9,"method":"squash","extra":1}`,
	} {
		before := svc.prMergeFlowCalls
		resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","id":3,"method":"git.pr.mergeFlow","params":`+params+`}`)
		if resp.Error == nil || resp.Error.Code != wire.InvalidParams || svc.prMergeFlowCalls != before {
			t.Fatalf("无效参数未拦截 (%s): err=%+v calls=%d/%d", params, resp.Error, svc.prMergeFlowCalls, before)
		}
	}
}

func TestGitExtPRStartReview(t *testing.T) {
	newThreadID := "22222222-2222-2222-2222-222222222222"
	svc := &gitExtFakeService{prStartReviewID: newThreadID}
	backend := &gitExtTestBackend{services: map[string]any{GitExtServiceName: svc}}
	d, conn := newGitExtTestDispatcher(backend)

	// 正常路径。
	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","id":1,"method":"git.pr.startReview","params":{"threadID":"`+gitExtTestThreadID+`","number":15}}`)
	var res wire.GitExtPRStartReviewResult
	gitExtResult(t, resp, &res)
	if res.ThreadID != newThreadID || res.Message != "" || svc.prNumber != 15 || !svc.prDeadline {
		t.Fatalf("正常路径结果不符: %+v svc=%+v", res, svc)
	}

	// 会话已创建但发送失败：返回 threadID + message。
	svc.prStartReviewErr = errors.New("会话已创建，但首条消息发送失败：timeout")
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","id":2,"method":"git.pr.startReview","params":{"threadID":"`+gitExtTestThreadID+`","number":15}}`)
	res = wire.GitExtPRStartReviewResult{}
	gitExtResult(t, resp, &res)
	if res.ThreadID != newThreadID || !strings.Contains(res.Message, "首条消息发送失败") {
		t.Fatalf("部分失败结果不符: %+v", res)
	}

	// 创建前失败：返回 JSON-RPC 错误。
	svc.prStartReviewErr = nil
	svc.err = errors.New("checkout failed")
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","id":3,"method":"git.pr.startReview","params":{"threadID":"`+gitExtTestThreadID+`","number":15}}`)
	if resp.Error == nil || resp.Error.Code != wire.InternalError {
		t.Fatalf("创建前失败应返回错误: %+v", resp.Error)
	}

	// 非法参数返回 InvalidParams 且不调用服务。
	for _, params := range []string{
		`{"number":15}`,
		`{"threadID":"` + gitExtTestThreadID + `","number":0}`,
		`{"threadID":"` + gitExtTestThreadID + `","number":15,"extra":1}`,
	} {
		before := svc.prStartCalls
		resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","id":4,"method":"git.pr.startReview","params":`+params+`}`)
		if resp.Error == nil || resp.Error.Code != wire.InvalidParams || svc.prStartCalls != before {
			t.Fatalf("无效参数未拦截 (%s): err=%+v calls=%d/%d", params, resp.Error, svc.prStartCalls, before)
		}
	}
}
