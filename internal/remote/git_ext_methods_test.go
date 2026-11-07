package remote

import (
	"context"
	"encoding/json"
	"errors"
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
	stashEntries  []git.StashEntry
	stashMessages []string
	stashApplied  []int
	stashDropped  []int
	prs           []git.PRSummary
	prLimit       int
	prDetail      git.PRDetail
	prNumber      int
	err           error

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
	if svc.prNumber != 12 || detail.Title != "拆分 git 面板" || detail.Additions != 10 {
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
