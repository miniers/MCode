package mcodeui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
	"mcode/internal/testenv"
)

func TestGitPRActionsVisible(t *testing.T) {
	for _, tc := range []struct {
		state       string
		draft, want bool
	}{{"open", false, true}, {"open", true, true}, {"draft", true, true}, {"merged", false, false}, {"closed", false, false}} {
		if got := gitPRActionsVisible(git.PRDetail{PRSummary: git.PRSummary{State: tc.state, IsDraft: tc.draft}}); got != tc.want {
			t.Errorf("状态 %q 草稿 %v: 得到 %v", tc.state, tc.draft, got)
		}
	}
}

func TestGitPRMergeBlocked(t *testing.T) {
	for _, tc := range []struct {
		state string
		want  bool
	}{{"CONFLICTING", true}, {"MERGEABLE", false}, {"UNKNOWN", false}, {"", false}} {
		if got := gitPRMergeBlocked(git.PRDetail{Mergeable: tc.state}); got != tc.want {
			t.Errorf("合并状态 %q: 得到 %v", tc.state, got)
		}
	}
}

type gitPRActionCall struct {
	threadID    string
	number      int
	kind, value string
}
type gitPRActionFake struct {
	*FakeClient
	calls chan gitPRActionCall
	err   error
}

func (f *gitPRActionFake) GitPRList(context.Context, string, int) ([]git.PRSummary, error) {
	return []git.PRSummary{{Number: 42, State: "open"}}, nil
}
func (f *gitPRActionFake) GitPRDetail(context.Context, string, int) (git.PRDetail, error) {
	return git.PRDetail{PRSummary: git.PRSummary{Number: 42, State: "open"}}, nil
}
func (f *gitPRActionFake) GitPRReview(_ context.Context, threadID string, number int, event, body string) error {
	f.calls <- gitPRActionCall{threadID, number, event, body}
	return f.err
}
func (f *gitPRActionFake) GitPRMerge(_ context.Context, threadID string, number int, method string) error {
	f.calls <- gitPRActionCall{threadID, number, "merge", method}
	return f.err
}
func (f *gitPRActionFake) GitPRAIReview(context.Context, string, int) ([]git.PRReviewComment, error) {
	return nil, f.err
}
func (f *gitPRActionFake) GitPRCheckoutWorktree(context.Context, string, int, string, bool) (string, error) {
	return "", f.err
}

func TestGitPRRunAction(t *testing.T) {
	for _, tc := range []struct {
		kind, method, value string
		fail                bool
	}{
		{"merge", "rebase", "rebase", false},
		{"merge", "squash", "squash", true},
	} {
		t.Run(tc.kind+tc.method, func(t *testing.T) {
			f := &gitPRActionFake{FakeClient: NewFakeClient(), calls: make(chan gitPRActionCall, 1)}
			if tc.fail {
				f.err = errors.New("操作失败")
			}
			s := newChatState(f)
			s.selectedThreadID = "thread-1"
			st := getGitPRDialogState(s)
			st.mu.Lock()
			st.open, st.threadID, st.selected = true, "thread-1", 42
			st.detail = &git.PRDetail{PRSummary: git.PRSummary{Number: 42, State: "open"}}
			st.mu.Unlock()
			s.runGitPRAction(tc.kind, tc.method)
			select {
			case got := <-f.calls:
				if want := (gitPRActionCall{"thread-1", 42, tc.kind, tc.value}); got != want {
					t.Errorf("调用 = %+v，期望 %+v", got, want)
				}
			case <-time.After(time.Second):
				t.Fatal("操作未执行")
			}
			deadline := time.Now().Add(time.Second)
			for {
				st.mu.Lock()
				busy, errText := st.actionBusy, st.actionErr
				st.mu.Unlock()
				if busy == "" {
					if tc.fail && errText == "" {
						t.Error("缺少错误信息")
					}
					if !tc.fail && errText != "" {
						t.Errorf("错误 %q", errText)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("操作未完成")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

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

// gitPRDialogFake 提供可控的 PR 列表、详情与冲突解决调用记录。
type gitPRDialogFake struct {
	*FakeClient
	prs     []git.PRSummary
	listErr error
	mu      sync.Mutex
	details []int

	detail      git.PRDetail
	conflicts   []string
	conflictErr error

	conflictCalls []gitPRConflictCall
	resolvePath   string
	resolveErr    error
	resolveCalls  []gitPRResolveCall
	switchCalls   []gitPRSwitchCall
	cleanupCalls  []gitPRActionCall
	cleanupReason string
	cleanupErr    error
	cleanupWait   <-chan struct{}
	mergeErr      error
	mergeStarted  chan struct{}
	mergeWait     <-chan struct{}
	mergeCalls    []gitPRActionCall
	reviewCalls   []gitPRActionCall

	checkoutCalls []gitPRCheckoutCall
	checkoutPath  string
	checkoutErr   error
	reviewGetPR   func(context.Context, string, int, string) (ReviewSnapshot, error)
}

type gitPRConflictCall struct {
	number int
	base   string
}

type gitPRResolveCall struct {
	number     int
	head, base string
}

type gitPRSwitchCall struct {
	threadID, path string
}

type gitPRCheckoutCall struct {
	number    int
	head      string
	crossRepo bool
}

func (f *gitPRDialogFake) GitPRList(context.Context, string, int) ([]git.PRSummary, error) {
	return f.prs, f.listErr
}

func (f *gitPRDialogFake) GitPRDetail(_ context.Context, _ string, number int) (git.PRDetail, error) {
	f.mu.Lock()
	f.details = append(f.details, number)
	detail := f.detail
	f.mu.Unlock()
	if detail.Number == 0 {
		detail = git.PRDetail{PRSummary: git.PRSummary{Number: number, Title: "详情标题", State: "open"}}
	}
	return detail, nil
}

func (f *gitPRDialogFake) GitPRConflicts(_ context.Context, _ string, number int, base string) ([]string, error) {
	f.mu.Lock()
	f.conflictCalls = append(f.conflictCalls, gitPRConflictCall{number: number, base: base})
	files, err := append([]string(nil), f.conflicts...), f.conflictErr
	f.mu.Unlock()
	return files, err
}

func (f *gitPRDialogFake) GitPRResolveWorktree(_ context.Context, _ string, number int, head, base string) (string, error) {
	f.mu.Lock()
	f.resolveCalls = append(f.resolveCalls, gitPRResolveCall{number: number, head: head, base: base})
	path, err := f.resolvePath, f.resolveErr
	f.mu.Unlock()
	return path, err
}

func (f *gitPRDialogFake) GitSwitchWorktree(ctx context.Context, threadID, path string) error {
	f.mu.Lock()
	f.switchCalls = append(f.switchCalls, gitPRSwitchCall{threadID: threadID, path: path})
	f.mu.Unlock()
	return f.FakeClient.GitSwitchWorktree(ctx, threadID, path)
}

func (f *gitPRDialogFake) GitPRCleanupWorktree(_ context.Context, threadID string, number int) (string, error) {
	f.mu.Lock()
	f.cleanupCalls = append(f.cleanupCalls, gitPRActionCall{threadID: threadID, number: number})
	reason, err, wait := f.cleanupReason, f.cleanupErr, f.cleanupWait
	f.mu.Unlock()
	if wait != nil {
		<-wait
	}
	return reason, err
}

func (f *gitPRDialogFake) GitPRMerge(_ context.Context, threadID string, number int, method string) error {
	f.mu.Lock()
	f.mergeCalls = append(f.mergeCalls, gitPRActionCall{threadID: threadID, number: number, kind: "merge", value: method})
	f.mu.Unlock()
	if f.mergeStarted != nil {
		close(f.mergeStarted)
		<-f.mergeWait
	}
	return f.mergeErr
}

func (f *gitPRDialogFake) GitPRReview(_ context.Context, threadID string, number int, event, body string) error {
	f.mu.Lock()
	f.reviewCalls = append(f.reviewCalls, gitPRActionCall{threadID: threadID, number: number, kind: event, value: body})
	f.mu.Unlock()
	return nil
}

func (f *gitPRDialogFake) mergeCallsSnapshot() []gitPRActionCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]gitPRActionCall(nil), f.mergeCalls...)
}

func (f *gitPRDialogFake) reviewCallsSnapshot() []gitPRActionCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]gitPRActionCall(nil), f.reviewCalls...)
}

// ReviewGetPR 让 AI 审查后的改动视图能读到 PR 数据，满足 reviewPRClient。
func (f *gitPRDialogFake) ReviewGetPR(ctx context.Context, tid string, number int, path string) (ReviewSnapshot, error) {
	if f.reviewGetPR != nil {
		return f.reviewGetPR(ctx, tid, number, path)
	}
	return ReviewSnapshot{Scope: "pr"}, nil
}

// GitPRSubmitReview 满足 reviewPRClient；本文件的测试不提交评论。
func (f *gitPRDialogFake) GitPRSubmitReview(context.Context, string, int, string, string, []git.PRReviewComment) error {
	return nil
}

func (f *gitPRDialogFake) GitPRCheckoutWorktree(_ context.Context, _ string, number int, head string, crossRepo bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkoutCalls = append(f.checkoutCalls, gitPRCheckoutCall{number: number, head: head, crossRepo: crossRepo})
	return f.checkoutPath, f.checkoutErr
}

func (f *gitPRDialogFake) checkoutCallsSnapshot() []gitPRCheckoutCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]gitPRCheckoutCall(nil), f.checkoutCalls...)
}

func (f *gitPRDialogFake) detailCalls() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.details...)
}

func (f *gitPRDialogFake) conflictCallsSnapshot() []gitPRConflictCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]gitPRConflictCall(nil), f.conflictCalls...)
}

func (f *gitPRDialogFake) resolveCallsSnapshot() []gitPRResolveCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]gitPRResolveCall(nil), f.resolveCalls...)
}

func (f *gitPRDialogFake) switchCallsSnapshot() []gitPRSwitchCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]gitPRSwitchCall(nil), f.switchCalls...)
}

func (f *gitPRDialogFake) renameCallsSnapshot() []renameCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]renameCall(nil), f.renameCalls...)
}

func newGitPRDialogTestState(t *testing.T, client *gitPRDialogFake) *chatState {
	t.Helper()
	testenv.TempHome(t)
	s := newChatState(client)
	s.mu.Lock()
	s.selectedThreadID = "thread-1"
	s.threads = []Thread{{ID: "thread-1", ProjectID: "p1"}}
	s.mu.Unlock()
	t.Cleanup(func() {
		s.close()
		modalDragReset(modalDragGitPR)
	})
	return s
}

func waitGitPRDialog(t *testing.T, s *chatState, check func(*gitPRDialogState) bool) {
	t.Helper()
	st := getGitPRDialogState(s)
	deadline := time.Now().Add(3 * time.Second)
	for {
		st.mu.Lock()
		ok := check(st)
		st.mu.Unlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("等待 PR 弹窗状态超时")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestGitPRMergeCleansWorktree(t *testing.T) {
	for _, tc := range []struct {
		name, reason, toast string
		err, mergeErr       error
		closeDialog         bool
	}{
		{name: "清理成功", toast: "已合并"},
		{name: "保留工作区", reason: "有未推送的提交", toast: "已保留 PR #42 的工作区：有未推送的提交"},
		{name: "清理失败", err: errors.New("清理失败原文"), toast: "清理失败原文"},
		{name: "合并失败", mergeErr: errors.New("合并失败原文")},
		{name: "合并期间关闭弹窗", closeDialog: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			done := make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(done) }) }
			t.Cleanup(release)
			f := &gitPRDialogFake{FakeClient: NewFakeClient(), cleanupReason: tc.reason, cleanupErr: tc.err, cleanupWait: done, mergeErr: tc.mergeErr}
			mergeGate := make(chan struct{})
			if tc.closeDialog {
				f.mergeStarted, f.mergeWait = make(chan struct{}), mergeGate
			}
			s := newGitPRDialogTestState(t, f)
			st := getGitPRDialogState(s)
			st.mu.Lock()
			st.open, st.threadID, st.selected = true, "thread-1", 42
			st.detail = &git.PRDetail{PRSummary: git.PRSummary{Number: 42, State: "open"}}
			st.mu.Unlock()
			s.runGitPRAction("merge", "squash")
			if tc.closeDialog {
				select {
				case <-f.mergeStarted:
				case <-time.After(3 * time.Second):
					close(mergeGate)
					t.Fatal("合并未开始")
				}
				st.mu.Lock()
				st.open = false
				st.detailSeq++
				st.mu.Unlock()
				close(mergeGate)
			} else {
				waitGitPRDialog(t, s, func(st *gitPRDialogState) bool { return st.actionBusy == "" && !st.detailLoading && !st.listLoading })
			}
			if tc.mergeErr == nil && !tc.closeDialog {
				// 清理仍阻塞时，合并成功后的详情刷新必须已执行。
				if len(f.detailCalls()) != 1 {
					t.Error("清理阻塞了合并后的详情刷新")
				}
			}
			release()
			deadline := time.Now().Add(3 * time.Second)
			for {
				f.mu.Lock()
				calls := append([]gitPRActionCall(nil), f.cleanupCalls...)
				f.mu.Unlock()
				s.mu.Lock()
				toast := s.toastMessage
				s.mu.Unlock()
				if tc.mergeErr != nil {
					if len(calls) != 0 {
						t.Fatal("合并失败后不应清理")
					}
					break
				}
				if len(calls) == 1 && toast == tc.toast {
					if calls[0].number != 42 || calls[0].threadID != "thread-1" {
						t.Fatalf("清理调用 = %+v", calls)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("清理调用 = %+v，toast = %q", calls, toast)
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

// TestGitPRDialogEmptyStateOffersCreate 覆盖没有开放 PR 时的空状态。
func TestGitPRDialogEmptyStateOffersCreate(t *testing.T) {
	s := newGitPRDialogTestState(t, &gitPRDialogFake{FakeClient: NewFakeClient()})
	s.openGitPRDialog()
	waitGitPRDialog(t, s, func(st *gitPRDialogState) bool { return !st.listLoading && st.listErr == "" })

	tt := ui.NewTester(func(c *ui.Context) { renderGitPRDialog(c, s) }, 1200, 800)
	tt.Advance(time.Second)
	for _, text := range []string{"开放的 PR/MR", "从左侧选择一条 PR 查看详情", "Esc 关闭"} {
		if tt.HasText(text) {
			t.Errorf("空状态不应显示 %q", text)
		}
	}
	for _, text := range []string{"没有开放的 PR", "创建 PR"} {
		if !tt.HasText(text) {
			t.Errorf("空状态缺少 %q", text)
		}
	}
	if tt.FramePending() {
		t.Error("空状态弹窗动画结束后仍在请求下一帧")
	}
}

// TestGitPRDialogAutoSelectsFirst 覆盖列表加载完成后自动选中第一条并请求详情。
func TestGitPRDialogAutoSelectsFirst(t *testing.T) {
	f := &gitPRDialogFake{FakeClient: NewFakeClient(), prs: []git.PRSummary{
		{Number: 7, Title: "第一条", State: "open"},
		{Number: 9, Title: "第二条", State: "open"},
	}}
	s := newGitPRDialogTestState(t, f)
	s.openGitPRDialog()
	waitGitPRDialog(t, s, func(st *gitPRDialogState) bool {
		return st.selected == 7 && st.detail != nil && !st.detailLoading
	})
	if calls := f.detailCalls(); len(calls) != 1 || calls[0] != 7 {
		t.Fatalf("详情请求 = %v，期望只请求第一条 #7", calls)
	}

	tt := ui.NewTester(func(c *ui.Context) { renderGitPRDialog(c, s) }, 1200, 800)
	tt.Advance(time.Second)
	if !tt.HasText("第一条") {
		t.Error("左列没有显示第一条 PR")
	}
}

// TestGitPRDialogConflictList 覆盖冲突时点击状态条文字展开冲突文件路径。
func TestGitPRDialogConflictList(t *testing.T) {
	f := &gitPRDialogFake{
		FakeClient: NewFakeClient(),
		prs:        []git.PRSummary{{Number: 42, Title: "冲突", State: "open"}},
		detail: git.PRDetail{
			PRSummary: git.PRSummary{Number: 42, Title: "冲突", State: "open", HeadBranch: "feat/x", BaseBranch: "main"},
			Mergeable: "CONFLICTING",
		},
		conflicts: []string{"src/a.go", "src/b.go"},
	}
	s := newGitPRDialogTestState(t, f)
	s.openGitPRDialog()
	waitGitPRDialog(t, s, func(st *gitPRDialogState) bool {
		return st.selected == 42 && st.detail != nil && !st.detailLoading && !st.conflictLoading && st.conflictNumber == 42
	})
	calls := f.conflictCallsSnapshot()
	if len(calls) != 1 || calls[0].number != 42 || calls[0].base != "main" {
		t.Fatalf("冲突预演调用 = %+v，期望 #42 base main", calls)
	}

	tt := ui.NewTester(func(c *ui.Context) { renderGitPRDialog(c, s) }, 1200, 800)
	tt.Advance(time.Second)
	if !tt.HasText("2 个文件冲突") {
		t.Fatal("状态条缺少冲突文件数量")
	}
	if tt.HasText("src/a.go") {
		t.Fatal("冲突文件列表默认应折叠")
	}
	if err := tt.Click("2 个文件冲突"); err != nil {
		t.Fatalf("点击展开冲突列表: %v", err)
	}
	tt.Advance(time.Second)
	for _, text := range []string{"src/a.go", "src/b.go"} {
		if !tt.HasText(text) {
			t.Errorf("展开后缺少 %q", text)
		}
	}
	if tt.FramePending() {
		t.Error("冲突列表加载并展开完成后仍在请求下一帧")
	}
}

// TestGitPRDialogConflictResolve 覆盖「解决冲突」的完整调用顺序。
func TestGitPRDialogConflictResolve(t *testing.T) {
	f := &gitPRDialogFake{
		FakeClient: NewFakeClient(),
		prs:        []git.PRSummary{{Number: 42, Title: "冲突", State: "open"}},
		detail: git.PRDetail{
			PRSummary: git.PRSummary{Number: 42, Title: "冲突", State: "open", HeadBranch: "feat/x", BaseBranch: "main"},
			Mergeable: "CONFLICTING",
		},
		conflicts:   []string{"src/a.go"},
		resolvePath: "/tmp/mcode-pr-42",
	}
	s := newGitPRDialogTestState(t, f)
	s.openGitPRDialog()
	waitGitPRDialog(t, s, func(st *gitPRDialogState) bool {
		return st.selected == 42 && st.detail != nil && !st.detailLoading && !st.conflictLoading
	})

	tt := ui.NewTester(func(c *ui.Context) { renderGitPRDialog(c, s) }, 1200, 800)
	tt.Advance(time.Second)
	if err := tt.Click("解决冲突"); err != nil {
		t.Fatalf("点击解决冲突: %v", err)
	}
	waitGitPRDialog(t, s, func(st *gitPRDialogState) bool { return !st.open })

	if calls := f.resolveCallsSnapshot(); len(calls) != 1 || calls[0] != (gitPRResolveCall{number: 42, head: "feat/x", base: "main"}) {
		t.Fatalf("冲突解决调用 = %+v", calls)
	}
	if got := f.LastCreatedProjectID(); got != "p1" {
		t.Errorf("新建会话项目 = %q，期望 p1", got)
	}
	f.mu.Lock()
	opts := f.lastNewThreadOpts
	f.mu.Unlock()
	if opts.Provisional {
		t.Error("冲突解决会话不应是临时会话")
	}
	renames := f.renameCallsSnapshot()
	if len(renames) != 1 || renames[0].Title != "解决 PR #42 冲突" {
		t.Fatalf("重命名调用 = %+v", renames)
	}
	newID := renames[0].ThreadID
	switches := f.switchCallsSnapshot()
	if len(switches) != 1 || switches[0] != (gitPRSwitchCall{threadID: newID, path: "/tmp/mcode-pr-42"}) {
		t.Fatalf("切换 worktree 调用 = %+v，期望会话 %q", switches, newID)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		s.mu.Lock()
		selected := s.selectedThreadID
		s.mu.Unlock()
		if selected == newID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("选中会话 = %q，期望 %q", selected, newID)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestGitPRAIReviewThread 覆盖从「⋯」菜单发起 AI 审查：检出 PR、新建审查会话并发送审查指令。
func TestGitPRAIReviewThread(t *testing.T) {
	f := &gitPRDialogFake{
		FakeClient: NewFakeClient(),
		prs:        []git.PRSummary{{Number: 42, Title: "审查", State: "open"}},
		detail: git.PRDetail{
			PRSummary: git.PRSummary{Number: 42, Title: "按钮归类", State: "open", HeadBranch: "feat/x", BaseBranch: "main"},
		},
		checkoutPath: "/tmp/mcode-pr-42-review",
	}
	s := newGitPRDialogTestState(t, f)
	s.openGitPRDialog()
	waitGitPRDialog(t, s, func(st *gitPRDialogState) bool {
		return st.selected == 42 && st.detail != nil && !st.detailLoading
	})

	tt := ui.NewTester(func(c *ui.Context) { renderGitPRDialog(c, s) }, 1200, 800)
	tt.Advance(time.Second)
	if err := tt.Click("更多操作"); err != nil {
		t.Fatalf("打开更多操作菜单: %v", err)
	}
	tt.Advance(time.Second)
	// 拿不到改动文件数的客户端（如 GitLab）也要显示这个菜单项。
	if !tt.HasText("AI 审查") {
		t.Fatal("缺少 AI 审查菜单项")
	}
	if err := tt.Click("AI 审查"); err != nil {
		t.Fatalf("点击 AI 审查: %v", err)
	}
	waitGitPRDialog(t, s, func(st *gitPRDialogState) bool { return !st.open })

	calls := f.checkoutCallsSnapshot()
	if len(calls) != 1 || calls[0] != (gitPRCheckoutCall{number: 42, head: "feat/x"}) {
		t.Fatalf("检出调用 = %+v", calls)
	}
	renames := f.renameCallsSnapshot()
	if len(renames) != 1 || renames[0].Title != "审查 PR #42" {
		t.Fatalf("重命名调用 = %+v", renames)
	}
	newID := renames[0].ThreadID
	switches := f.switchCallsSnapshot()
	if len(switches) != 1 || switches[0] != (gitPRSwitchCall{threadID: newID, path: "/tmp/mcode-pr-42-review"}) {
		t.Fatalf("切换 worktree 调用 = %+v", switches)
	}
	f.FakeClient.mu.Lock()
	sends := append([]sentCall(nil), f.FakeClient.sendCalls...)
	f.FakeClient.mu.Unlock()
	if len(sends) != 1 || sends[0].ThreadID != newID {
		t.Fatalf("发送调用 = %+v，期望会话 %q", sends, newID)
	}
	for _, want := range []string{"```pr-review", "origin/main", "按钮归类"} {
		if !strings.Contains(sends[0].Text, want) {
			t.Fatalf("审查提示缺少 %q: %q", want, sends[0].Text)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.mu.Lock()
		selected := s.selectedThreadID
		s.mu.Unlock()
		if selected == newID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("选中会话 = %q，期望 %q", selected, newID)
		}
		time.Sleep(time.Millisecond)
	}
	// 审查会话建好后改动面板应直接停在 PR 视图并标记为自动导入 AI 审查。
	dp := getDiffPanelState(s)
	deadline = time.Now().Add(3 * time.Second)
	for {
		diffPanelMu.Lock()
		scope, number, auto := dp.scope, dp.prNumber, dp.prAIAuto
		diffPanelMu.Unlock()
		if scope == "pr" && number == 42 && auto {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("改动面板 = scope %q PR #%d prAIAuto=%v，期望 PR #42 且 prAIAuto=true", scope, number, auto)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestGitPRAIReviewCrossRepo 覆盖 fork 来的 PR 也能通过「⋯」菜单发起 AI 审查与检出，并把 crossRepo=true 传给后端。
func TestGitPRAIReviewCrossRepo(t *testing.T) {
	for _, tc := range []struct {
		button    string
		wantTitle string
	}{
		{"AI 审查", "审查 PR #42"},
		{"检出到 worktree", "PR #42"},
	} {
		t.Run(tc.button, func(t *testing.T) {
			f := &gitPRDialogFake{
				FakeClient: NewFakeClient(),
				prs:        []git.PRSummary{{Number: 42, Title: "审查", State: "open"}},
				detail: git.PRDetail{
					PRSummary: git.PRSummary{Number: 42, Title: "审查", State: "open", HeadBranch: "feat/x", BaseBranch: "main"},
					CrossRepo: true,
				},
				checkoutPath: "/tmp/mcode-pr-42-review",
			}
			s := newGitPRDialogTestState(t, f)
			s.openGitPRDialog()
			waitGitPRDialog(t, s, func(st *gitPRDialogState) bool {
				return st.selected == 42 && st.detail != nil && !st.detailLoading
			})

			tt := ui.NewTester(func(c *ui.Context) { renderGitPRDialog(c, s) }, 1200, 800)
			tt.Advance(time.Second)
			if err := tt.Click("更多操作"); err != nil {
				t.Fatalf("打开更多操作菜单: %v", err)
			}
			tt.Advance(time.Second)
			if !tt.HasText(tc.button) {
				t.Fatalf("缺少 %s 菜单项", tc.button)
			}
			if err := tt.Click(tc.button); err != nil {
				t.Fatalf("点击 %s: %v", tc.button, err)
			}
			waitGitPRDialog(t, s, func(st *gitPRDialogState) bool { return !st.open })

			calls := f.checkoutCallsSnapshot()
			if len(calls) != 1 || calls[0] != (gitPRCheckoutCall{number: 42, head: "feat/x", crossRepo: true}) {
				t.Fatalf("检出调用 = %+v", calls)
			}
			renames := f.renameCallsSnapshot()
			if len(renames) != 1 || renames[0].Title != tc.wantTitle {
				t.Fatalf("重命名调用 = %+v", renames)
			}
		})
	}
}

// TestGitPRDialogCrossRepoBlocksResolve 覆盖 fork 来的 PR 禁止解决冲突。
func TestGitPRDialogCrossRepoBlocksResolve(t *testing.T) {
	f := &gitPRDialogFake{
		FakeClient: NewFakeClient(),
		prs:        []git.PRSummary{{Number: 42, Title: "冲突", State: "open"}},
		detail: git.PRDetail{
			PRSummary: git.PRSummary{Number: 42, Title: "冲突", State: "open", HeadBranch: "feat/x", BaseBranch: "main"},
			Mergeable: "CONFLICTING",
			CrossRepo: true,
		},
		resolvePath: "/tmp/mcode-pr-42",
	}
	s := newGitPRDialogTestState(t, f)
	s.openGitPRDialog()
	waitGitPRDialog(t, s, func(st *gitPRDialogState) bool {
		return st.selected == 42 && st.detail != nil && !st.detailLoading
	})

	tt := ui.NewTester(func(c *ui.Context) { renderGitPRDialog(c, s) }, 1200, 800)
	tt.Advance(time.Second)
	if !tt.HasText("解决冲突") {
		t.Fatal("缺少解决冲突按钮")
	}
	_ = tt.Click("解决冲突")
	time.Sleep(50 * time.Millisecond)
	if calls := f.resolveCallsSnapshot(); len(calls) != 0 {
		t.Fatalf("fork PR 不应创建冲突 worktree，调用 = %+v", calls)
	}
	st := getGitPRDialogState(s)
	st.mu.Lock()
	open, busy := st.open, st.actionBusy
	st.mu.Unlock()
	if !open || busy != "" {
		t.Errorf("弹窗状态 open=%v busy=%q", open, busy)
	}
}

// TestGitPRCheckRowURL 覆盖 CI 检查行有链接时可点击、无链接时保持原样。
func TestGitPRCheckRowURL(t *testing.T) {
	for _, tc := range []struct {
		name, url string
		wantOpen  bool
	}{{"有链接", "https://ci.example.com/run/1", true}, {"无链接", "", false}} {
		t.Run(tc.name, func(t *testing.T) {
			tt := ui.NewTester(func(c *ui.Context) {
				renderGitPRCheckRow(c, "modal-git-pr-detail-check-0", git.PRCheck{Name: "build", State: "success", URL: tc.url})
			}, 600, 100)
			tt.Frame()
			if !tt.HasText("build") || !tt.HasText("检查通过") {
				t.Fatal("检查行缺少名称或状态")
			}
			if tc.wantOpen {
				if err := tt.Click("build"); err != nil {
					t.Fatalf("点击检查行: %v", err)
				}
				tt.Frame()
			}
			opened := tt.OpenedURLs()
			if tc.wantOpen {
				if len(opened) != 1 || opened[0] != tc.url {
					t.Fatalf("打开的链接 = %v，期望 %q", opened, tc.url)
				}
			} else if len(opened) != 0 {
				t.Fatalf("无链接的检查行打开了 %v", opened)
			}
		})
	}
}

// TestGitPRResolveSendsPrompt 覆盖解决冲突后把合并提示发给新会话。
func TestGitPRResolveSendsPrompt(t *testing.T) {
	f := &gitPRDialogFake{
		FakeClient: NewFakeClient(),
		prs:        []git.PRSummary{{Number: 42, Title: "冲突", State: "open"}},
		detail: git.PRDetail{
			PRSummary: git.PRSummary{Number: 42, Title: "冲突", State: "open", HeadBranch: "feat/x", BaseBranch: "main"},
			Mergeable: "CONFLICTING",
		},
		conflicts:   []string{"src/a.go"},
		resolvePath: "/tmp/mcode-pr-42",
	}
	s := newGitPRDialogTestState(t, f)
	s.openGitPRDialog()
	waitGitPRDialog(t, s, func(st *gitPRDialogState) bool {
		return st.selected == 42 && st.detail != nil && !st.detailLoading && !st.conflictLoading
	})

	tt := ui.NewTester(func(c *ui.Context) { renderGitPRDialog(c, s) }, 1200, 800)
	tt.Advance(time.Second)
	if err := tt.Click("解决冲突"); err != nil {
		t.Fatalf("点击解决冲突: %v", err)
	}
	waitGitPRDialog(t, s, func(st *gitPRDialogState) bool { return !st.open })

	renames := f.renameCallsSnapshot()
	if len(renames) != 1 {
		t.Fatalf("重命名调用 = %+v", renames)
	}
	newID := renames[0].ThreadID
	f.FakeClient.mu.Lock()
	sends := append([]sentCall(nil), f.FakeClient.sendCalls...)
	f.FakeClient.mu.Unlock()
	if len(sends) != 1 || sends[0].ThreadID != newID || !strings.Contains(sends[0].Text, "PR #42") {
		t.Fatalf("发送调用 = %+v，期望会话 %q 包含 PR #42", sends, newID)
	}
	if !strings.Contains(sends[0].Text, "origin/main") {
		t.Fatalf("提示缺少目标分支: %q", sends[0].Text)
	}
}

// TestGitPRCheckout 覆盖通过「⋯」菜单检出后切到新会话，不发送消息。
func TestGitPRCheckout(t *testing.T) {
	f := &gitPRDialogFake{
		FakeClient: NewFakeClient(),
		prs:        []git.PRSummary{{Number: 42, Title: "检出", State: "open"}},
		detail: git.PRDetail{
			PRSummary: git.PRSummary{Number: 42, Title: "检出", State: "open", HeadBranch: "feat/x", BaseBranch: "main"},
		},
		checkoutPath: "/tmp/mcode-pr-42-checkout",
	}
	s := newGitPRDialogTestState(t, f)
	s.openGitPRDialog()
	waitGitPRDialog(t, s, func(st *gitPRDialogState) bool {
		return st.selected == 42 && st.detail != nil && !st.detailLoading
	})

	tt := ui.NewTester(func(c *ui.Context) { renderGitPRDialog(c, s) }, 1200, 800)
	tt.Advance(time.Second)
	if err := tt.Click("更多操作"); err != nil {
		t.Fatalf("打开更多操作菜单: %v", err)
	}
	tt.Advance(time.Second)
	if !tt.HasText("检出到 worktree") {
		t.Fatal("缺少检出到 worktree 菜单项")
	}
	if err := tt.Click("检出到 worktree"); err != nil {
		t.Fatalf("点击检出到 worktree: %v", err)
	}
	waitGitPRDialog(t, s, func(st *gitPRDialogState) bool { return !st.open })

	calls := f.checkoutCallsSnapshot()
	if len(calls) != 1 || calls[0] != (gitPRCheckoutCall{number: 42, head: "feat/x"}) {
		t.Fatalf("检出调用 = %+v", calls)
	}
	renames := f.renameCallsSnapshot()
	if len(renames) != 1 || renames[0].Title != "PR #42" {
		t.Fatalf("重命名调用 = %+v", renames)
	}
	newID := renames[0].ThreadID
	switches := f.switchCallsSnapshot()
	if len(switches) != 1 || switches[0] != (gitPRSwitchCall{threadID: newID, path: "/tmp/mcode-pr-42-checkout"}) {
		t.Fatalf("切换 worktree 调用 = %+v", switches)
	}
	if f.SendCalls() != 0 {
		t.Fatalf("检出不应发送消息，调用了 %d 次", f.SendCalls())
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.mu.Lock()
		selected := s.selectedThreadID
		s.mu.Unlock()
		if selected == newID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("选中会话 = %q，期望 %q", selected, newID)
		}
		time.Sleep(time.Millisecond)
	}
	// 普通检出只切会话，不改动面板范围。
	dp := getDiffPanelState(s)
	diffPanelMu.Lock()
	scope, number := dp.scope, dp.prNumber
	diffPanelMu.Unlock()
	if scope == "pr" || number != 0 || inspectorPaneOpen(s, sidePaneDiffID) {
		t.Fatalf("检出不应打开 PR 改动视图：open=%v scope=%q number=%d", inspectorPaneOpen(s, sidePaneDiffID), scope, number)
	}
}

// TestCreateGitPRThreadKeepsThreadOnSendFailure 覆盖首条消息发送失败时保留已切入 worktree 的会话，不调用 DeleteThread。
func TestCreateGitPRThreadKeepsThreadOnSendFailure(t *testing.T) {
	f := NewFakeClient()
	f.mu.Lock()
	f.sendErr = errors.New("send failed")
	f.mu.Unlock()

	s := newGitPRDialogTestState(t, &gitPRDialogFake{FakeClient: f})
	newID, err := s.createGitPRThread("thread-1", 42, "/tmp/mcode-pr-42", "解决 PR #42 冲突", "请处理冲突")
	if err == nil {
		t.Fatalf("Send 失败时应返回错误，got newID=%q", newID)
	}
	if !strings.Contains(err.Error(), "会话已创建，但首条消息发送失败：") {
		t.Fatalf("错误信息 = %q，期望包含保留会话提示", err.Error())
	}
	if newID == "" {
		t.Fatalf("Send 失败时应保留并返回新会话 ID")
	}

	f.mu.Lock()
	deletes := append([]string(nil), f.deleteCalls...)
	threads := append([]Thread(nil), f.threads["p1"]...)
	f.mu.Unlock()

	if len(deletes) != 0 {
		t.Fatalf("Send 失败时不应调用 DeleteThread，实际 deleteCalls=%v", deletes)
	}
	found := false
	for _, th := range threads {
		if th.ID == newID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("保留的会话 %q 未出现在项目列表中: %+v", newID, threads)
	}
}

// TestCreateGitPRThreadRollsBackOnRenameFailure 覆盖尚未切入 worktree 时 RenameThread 失败仍回滚删掉新建会话。
func TestCreateGitPRThreadRollsBackOnRenameFailure(t *testing.T) {
	f := NewFakeClient()
	f.mu.Lock()
	f.renameErr = errors.New("rename failed")
	f.mu.Unlock()

	s := newGitPRDialogTestState(t, &gitPRDialogFake{FakeClient: f})
	newID, err := s.createGitPRThread("thread-1", 42, "/tmp/mcode-pr-42", "解决 PR #42 冲突", "请处理冲突")
	if err == nil {
		t.Fatalf("RenameThread 失败时应返回错误，got newID=%q", newID)
	}
	if newID != "" {
		t.Fatalf("RenameThread 失败时不应返回新会话 ID: %q", newID)
	}

	f.mu.Lock()
	deletes := append([]string(nil), f.deleteCalls...)
	threads := append([]Thread(nil), f.threads["p1"]...)
	f.mu.Unlock()

	if len(deletes) != 1 || deletes[0] == "" {
		t.Fatalf("期望 DeleteThread 被调用一次，实际 deleteCalls=%v", deletes)
	}
	for _, th := range threads {
		if th.ID == deletes[0] {
			t.Fatalf("回滚后项目仍残留孤儿会话 %q", th.ID)
		}
	}
}

// TestGitPRResolveSendFailureSelectsThreadAndToasts 覆盖调用方在首条消息发送失败时关闭弹窗、切入保留会话并以 Toast 报错。
func TestGitPRResolveSendFailureSelectsThreadAndToasts(t *testing.T) {
	base := NewFakeClient()
	base.mu.Lock()
	base.sendErr = errors.New("send failed")
	base.mu.Unlock()
	f := &gitPRDialogFake{
		FakeClient: base,
		prs:        []git.PRSummary{{Number: 42, Title: "冲突", State: "open"}},
		detail: git.PRDetail{
			PRSummary: git.PRSummary{Number: 42, Title: "冲突", State: "open", HeadBranch: "feat/x", BaseBranch: "main"},
			Mergeable: "CONFLICTING",
		},
		conflicts:   []string{"src/a.go"},
		resolvePath: "/tmp/mcode-pr-42",
	}
	s := newGitPRDialogTestState(t, f)
	s.openGitPRDialog()
	waitGitPRDialog(t, s, func(st *gitPRDialogState) bool {
		return st.selected == 42 && st.detail != nil && !st.detailLoading
	})

	tt := ui.NewTester(func(c *ui.Context) { renderGitPRDialog(c, s) }, 1200, 800)
	tt.Advance(time.Second)
	if err := tt.Click("解决冲突"); err != nil {
		t.Fatalf("点击解决冲突: %v", err)
	}
	waitGitPRDialog(t, s, func(st *gitPRDialogState) bool { return !st.open })

	renames := f.renameCallsSnapshot()
	if len(renames) != 1 {
		t.Fatalf("重命名调用 = %+v", renames)
	}
	newID := renames[0].ThreadID

	deadline := time.Now().Add(3 * time.Second)
	for {
		s.mu.Lock()
		selected := s.selectedThreadID
		toast := s.toastMessage
		s.mu.Unlock()
		if selected == newID && strings.Contains(toast, "会话已创建，但首条消息发送失败：") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("selected=%q (want %q), toast=%q", selected, newID, toast)
		}
		time.Sleep(time.Millisecond)
	}

	base.mu.Lock()
	deletes := append([]string(nil), base.deleteCalls...)
	base.mu.Unlock()
	if len(deletes) != 0 {
		t.Fatalf("Send 失败时不应调用 DeleteThread，实际 %v", deletes)
	}
}

// TestGitPRCheckRowRejectsUnsafeURL 覆盖 CI 检查行拒绝不安全 scheme，仅允许安全链接点击打开。
func TestGitPRCheckRowRejectsUnsafeURL(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want bool
	}{
		{"https 允许", "https://ci.example.com/run/1", true},
		{"javascript 拒绝", "javascript:alert(1)", false},
		{"file 拒绝", "file:///etc/passwd", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotURL, ok := gitPRCheckURLSafe(tc.url)
			if ok != tc.want {
				t.Fatalf("gitPRCheckURLSafe(%q) = (%q, %v), want ok=%v", tc.url, gotURL, ok, tc.want)
			}

			tt := ui.NewTester(func(c *ui.Context) {
				renderGitPRCheckRow(c, "modal-git-pr-detail-check-0", git.PRCheck{Name: "build", State: "success", URL: tc.url})
			}, 600, 100)
			tt.Frame()
			_ = tt.Click("build")
			tt.Frame()
			opened := tt.OpenedURLs()
			if tc.want {
				if len(opened) != 1 || opened[0] != tc.url {
					t.Fatalf("安全链接点击结果 = %v，期望 [%q]", opened, tc.url)
				}
			} else if len(opened) != 0 {
				t.Fatalf("不安全链接 %q 不应触发打开，实际 %v", tc.url, opened)
			}
		})
	}
}

// TestGitPRStaleThreadCallback 覆盖创建 PR 会话回调在弹窗关闭或切换 PR 后的处理。
func TestGitPRStaleThreadCallback(t *testing.T) {
	t.Run("弹窗已关切入新会话", func(t *testing.T) {
		f := &gitPRDialogFake{FakeClient: NewFakeClient()}
		s := newGitPRDialogTestState(t, f)
		s.notifyStaleGitPRThread(false, 42, "t-new-1")
		s.mu.Lock()
		selected := s.selectedThreadID
		toast := s.toastMessage
		s.mu.Unlock()
		if selected != "t-new-1" {
			t.Fatalf("selectedThreadID = %q, want t-new-1", selected)
		}
		if toast != "" {
			t.Fatalf("弹窗已关直接切换时不应弹 toast，got %q", toast)
		}
	})

	t.Run("切到别的PR只弹提示不跳转", func(t *testing.T) {
		f := &gitPRDialogFake{FakeClient: NewFakeClient()}
		s := newGitPRDialogTestState(t, f)
		s.notifyStaleGitPRThread(true, 42, "t-new-2")
		s.mu.Lock()
		selected := s.selectedThreadID
		toast := s.toastMessage
		s.mu.Unlock()
		if selected != "thread-1" {
			t.Fatalf("切到别的 PR 时不应跳转会话，selectedThreadID = %q", selected)
		}
		if toast != "PR #42 的会话已创建" {
			t.Fatalf("toast = %q, want %q", toast, "PR #42 的会话已创建")
		}
	})
}

func TestGitPRActionTip(t *testing.T) {
	cases := []struct {
		name               string
		busy, self, reason string
		want               string
	}{
		{"busy为其他操作", "approve", "merge", "原因", "正在执行其他操作，请稍候"},
		{"busy为其他操作且self为空", "approve", "", "", "正在执行其他操作，请稍候"},
		{"busy为自己且无原因", "merge", "merge", "", ""},
		{"busy为自己且保留原因", "comment", "comment", "先在上方输入评论内容", "先在上方输入评论内容"},
		{"不busy且有原因", "", "comment", "先在上方输入评论内容", "先在上方输入评论内容"},
		{"不busy且无原因", "", "approve", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gitPRActionTip(tc.busy, tc.self, tc.reason); got != tc.want {
				t.Errorf("gitPRActionTip(%q, %q, %q) = %q, want %q", tc.busy, tc.self, tc.reason, got, tc.want)
			}
		})
	}
}

// TestGitPRStatusStrip 覆盖状态条各分支与优先级判定。
func TestGitPRStatusStrip(t *testing.T) {
	cases := []struct {
		name          string
		detail        git.PRDetail
		conflictFiles []string
		wantTone      string
		wantText      string
	}{
		{
			name: "已合并优先于冲突与检查失败",
			detail: git.PRDetail{
				PRSummary: git.PRSummary{State: "merged", IsDraft: true, BaseBranch: "main", ReviewDecision: "CHANGES_REQUESTED"},
				Mergeable: "CONFLICTING",
				Checks:    []git.PRCheck{{Name: "ci", State: "failure"}},
			},
			conflictFiles: []string{"a.go"},
			wantTone:      "plan",
			wantText:      "已合并到 main",
		},
		{
			name: "已合并无目标分支",
			detail: git.PRDetail{
				PRSummary: git.PRSummary{State: "merged"},
			},
			wantTone: "plan",
			wantText: "已合并",
		},
		{
			name: "已关闭优先于草稿与冲突",
			detail: git.PRDetail{
				PRSummary: git.PRSummary{State: "closed", IsDraft: true},
				Mergeable: "CONFLICTING",
			},
			wantTone: "neutral",
			wantText: "已关闭",
		},
		{
			name: "草稿优先于冲突与检查失败",
			detail: git.PRDetail{
				PRSummary: git.PRSummary{State: "open", IsDraft: true},
				Mergeable: "CONFLICTING",
				Checks:    []git.PRCheck{{Name: "ci", State: "failure"}},
			},
			conflictFiles: []string{"a.go"},
			wantTone:      "neutral",
			wantText:      "草稿",
		},
		{
			name: "冲突且有文件列表优先于检查失败",
			detail: git.PRDetail{
				PRSummary: git.PRSummary{State: "open", ReviewDecision: "CHANGES_REQUESTED"},
				Mergeable: "CONFLICTING",
				Checks:    []git.PRCheck{{Name: "ci", State: "failure"}},
			},
			conflictFiles: []string{"a.go", "b.go"},
			wantTone:      "danger",
			wantText:      "2 个文件冲突",
		},
		{
			name: "冲突无文件列表",
			detail: git.PRDetail{
				PRSummary: git.PRSummary{State: "open"},
				Mergeable: "CONFLICTING",
			},
			wantTone: "danger",
			wantText: "有冲突",
		},
		{
			name: "仅conflictFiles非空也判为冲突",
			detail: git.PRDetail{
				PRSummary: git.PRSummary{State: "open"},
				Mergeable: "MERGEABLE",
			},
			conflictFiles: []string{"c.go"},
			wantTone:      "danger",
			wantText:      "1 个文件冲突",
		},
		{
			name: "检查失败优先于检查进行中与评审",
			detail: git.PRDetail{
				PRSummary: git.PRSummary{State: "open", ReviewDecision: "REVIEW_REQUIRED"},
				Checks: []git.PRCheck{
					{Name: "a", State: "failure"},
					{Name: "b", State: "FAILURE"},
					{Name: "c", State: "pending"},
					{Name: "d", State: "success"},
				},
			},
			wantTone: "danger",
			wantText: "2 项检查失败",
		},
		{
			name: "检查进行中优先于评审",
			detail: git.PRDetail{
				PRSummary: git.PRSummary{State: "open", ReviewDecision: "CHANGES_REQUESTED"},
				Checks: []git.PRCheck{
					{Name: "a", State: "pending"},
					{Name: "b", State: "pending"},
					{Name: "c", State: "success"},
				},
			},
			wantTone: "neutral",
			wantText: "2 项检查进行中",
		},
		{
			name: "需要修改",
			detail: git.PRDetail{
				PRSummary: git.PRSummary{State: "open", ReviewDecision: "CHANGES_REQUESTED"},
				Checks:    []git.PRCheck{{Name: "a", State: "success"}},
			},
			wantTone: "danger",
			wantText: "需要修改",
		},
		{
			name: "等待评审",
			detail: git.PRDetail{
				PRSummary: git.PRSummary{State: "open", ReviewDecision: "REVIEW_REQUIRED"},
			},
			wantTone: "neutral",
			wantText: "等待评审",
		},
		{
			name: "可以合并",
			detail: git.PRDetail{
				PRSummary: git.PRSummary{State: "open", ReviewDecision: "APPROVED"},
				Mergeable: "MERGEABLE",
				Checks:    []git.PRCheck{{Name: "a", State: "success"}},
			},
			wantTone: "success",
			wantText: "可以合并",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tone, text := gitPRStatusStrip(tc.detail, tc.conflictFiles)
			if tone != tc.wantTone || text != tc.wantText {
				t.Errorf("gitPRStatusStrip() = (%q, %q), want (%q, %q)", tone, text, tc.wantTone, tc.wantText)
			}
		})
	}
}

// TestGitPRDialogEmptySameSize 覆盖空态与非空弹窗卡片尺寸一致，且入场动画完成后不再续帧。
func TestGitPRDialogEmptySameSize(t *testing.T) {
	emptyState := newGitPRDialogTestState(t, &gitPRDialogFake{FakeClient: NewFakeClient()})
	emptyState.openGitPRDialog()
	waitGitPRDialog(t, emptyState, func(st *gitPRDialogState) bool { return !st.listLoading })

	emptyTT := ui.NewTester(func(c *ui.Context) { renderGitPRDialog(c, emptyState) }, 1200, 800)
	emptyTT.Advance(time.Second)
	emptyBox, ok := emptyTT.Find("PR 弹窗卡片")
	if !ok {
		t.Fatal("找不到空态 PR 弹窗卡片")
	}
	if emptyTT.FramePending() {
		t.Fatal("空态弹窗入场动画结束后仍在请求下一帧")
	}

	nonEmptyState := newGitPRDialogTestState(t, &gitPRDialogFake{
		FakeClient: NewFakeClient(),
		prs:        []git.PRSummary{{Number: 7, Title: "第一条", State: "open"}},
	})
	nonEmptyState.openGitPRDialog()
	waitGitPRDialog(t, nonEmptyState, func(st *gitPRDialogState) bool {
		return st.selected == 7 && st.detail != nil && !st.detailLoading
	})

	nonEmptyTT := ui.NewTester(func(c *ui.Context) { renderGitPRDialog(c, nonEmptyState) }, 1200, 800)
	nonEmptyTT.Advance(time.Second)
	nonEmptyBox, ok := nonEmptyTT.Find("PR 弹窗卡片")
	if !ok {
		t.Fatal("找不到非空 PR 弹窗卡片")
	}
	if nonEmptyTT.FramePending() {
		t.Fatal("非空弹窗入场动画结束后仍在请求下一帧")
	}

	if emptyBox.W != nonEmptyBox.W || emptyBox.H != nonEmptyBox.H {
		t.Fatalf("空态尺寸 (%v×%v) != 非空尺寸 (%v×%v)", emptyBox.W, emptyBox.H, nonEmptyBox.W, nonEmptyBox.H)
	}
	if emptyBox.W != gitPRDialogWidth {
		t.Fatalf("弹窗宽度 = %v，期望 %v", emptyBox.W, gitPRDialogWidth)
	}
}

// TestGitPRListSearch 覆盖列表搜索按标题与 #号不区分大小写过滤。
func TestGitPRListSearch(t *testing.T) {
	f := &gitPRDialogFake{
		FakeClient: NewFakeClient(),
		prs: []git.PRSummary{
			{Number: 101, Title: "Fix auth bug", Author: "alice", HeadBranch: "fix/auth", State: "open"},
			{Number: 102, Title: "Add dark mode", Author: "bob", HeadBranch: "feat/dark", State: "open"},
			{Number: 103, Title: "Refactor parser", Author: "carol", HeadBranch: "chore/parser", State: "open"},
		},
	}
	s := newGitPRDialogTestState(t, f)
	s.openGitPRDialog()
	waitGitPRDialog(t, s, func(st *gitPRDialogState) bool {
		return st.selected == 101 && st.detail != nil && !st.detailLoading
	})

	tt := ui.NewTester(func(c *ui.Context) { renderGitPRDialog(c, s) }, 1200, 800)
	tt.Advance(time.Second)
	for _, title := range []string{"Fix auth bug", "Add dark mode", "Refactor parser"} {
		if !tt.HasText(title) {
			t.Fatalf("初始列表缺少 %q", title)
		}
	}

	if err := tt.Click("搜索"); err != nil {
		t.Fatalf("聚焦搜索框: %v", err)
	}
	tt.Type("DARK")
	tt.Frame()
	if !tt.HasText("Add dark mode") || tt.HasText("Fix auth bug") || tt.HasText("Refactor parser") {
		t.Fatalf("按标题过滤失败，当前文本: %v", tt.Texts())
	}

	if err := tt.Click("清空"); err != nil {
		t.Fatalf("清空搜索框: %v", err)
	}
	tt.Frame()
	if err := tt.Click("搜索"); err != nil {
		t.Fatalf("重新聚焦搜索框: %v", err)
	}
	tt.Type("#103")
	tt.Frame()
	if !tt.HasText("Refactor parser") || tt.HasText("Fix auth bug") || tt.HasText("Add dark mode") {
		t.Fatalf("按 #号过滤失败，当前文本: %v", tt.Texts())
	}
}

// TestGitPRDialogStatusActionsAndBrowser 覆盖合并二次确认、批准确认、在浏览器打开与「⋯」复制链接。
func TestGitPRDialogStatusActionsAndBrowser(t *testing.T) {
	f := &gitPRDialogFake{
		FakeClient: NewFakeClient(),
		prs:        []git.PRSummary{{Number: 42, Title: "状态条与菜单", State: "open", URL: "https://example.com/pr/42"}},
		detail: git.PRDetail{
			PRSummary: git.PRSummary{
				Number:     42,
				Title:      "状态条与菜单",
				State:      "open",
				Author:     "liukai",
				HeadBranch: "feat/ui",
				BaseBranch: "main",
				URL:        "https://example.com/pr/42",
			},
			Mergeable: "MERGEABLE",
		},
	}
	s := newGitPRDialogTestState(t, f)
	s.openGitPRDialog()
	waitGitPRDialog(t, s, func(st *gitPRDialogState) bool {
		return st.selected == 42 && st.detail != nil && !st.detailLoading
	})

	tt := ui.NewTester(func(c *ui.Context) { renderGitPRDialog(c, s) }, 1200, 800)
	tt.Advance(time.Second)
	if tt.FramePending() {
		t.Fatal("弹窗静止后仍在请求下一帧")
	}

	// 打开浏览器。
	if err := tt.Click("在浏览器打开"); err != nil {
		t.Fatalf("点击在浏览器打开: %v", err)
	}
	tt.Frame()
	if opened := tt.OpenedURLs(); len(opened) != 1 || opened[0] != "https://example.com/pr/42" {
		t.Fatalf("打开浏览器 URL = %v", opened)
	}

	// 「⋯」菜单复制链接。
	if err := tt.Click("更多操作"); err != nil {
		t.Fatalf("打开更多操作菜单: %v", err)
	}
	tt.Advance(time.Second)
	if err := tt.Click("复制链接"); err != nil {
		t.Fatalf("点击复制链接: %v", err)
	}
	tt.Advance(time.Second)
	s.mu.Lock()
	toast := s.toastMessage
	s.mu.Unlock()
	if toast != "已复制链接" {
		t.Fatalf("复制链接 toast = %q，期望 已复制链接", toast)
	}

	// 切换合并方式并执行二次确认。
	if err := tt.Click("选择合并方式"); err != nil {
		t.Fatalf("打开合并方式菜单: %v", err)
	}
	tt.Advance(time.Second)
	if err := tt.Click("变基合并"); err != nil {
		t.Fatalf("选择变基合并: %v", err)
	}
	tt.Advance(time.Second)
	if err := tt.Click("变基合并"); err != nil {
		t.Fatalf("点击合并主按钮: %v", err)
	}
	tt.Frame()
	if !tt.HasText("确认合并") || !tt.HasText("取消") {
		t.Fatal("点击合并后未进入二次确认")
	}
	if len(f.mergeCallsSnapshot()) != 0 {
		t.Fatal("未确认前不应调用 GitPRMerge")
	}
	if err := tt.Click("确认合并"); err != nil {
		t.Fatalf("点击确认合并: %v", err)
	}
	waitGitPRDialog(t, s, func(st *gitPRDialogState) bool {
		return st.actionBusy == "" && !st.detailLoading && !st.listLoading
	})
	merges := f.mergeCallsSnapshot()
	if len(merges) != 1 || merges[0] != (gitPRActionCall{threadID: "thread-1", number: 42, kind: "merge", value: "rebase"}) {
		t.Fatalf("合并调用 = %+v，期望 rebase", merges)
	}
}

// TestGitPRDialogChangesTab 覆盖弹窗内「改动」Tab 与「开始评审」的切换、文件卡片与评审栏渲染、不改侧栏与动画收束。
func TestGitPRDialogChangesTab(t *testing.T) {
	prFake := &reviewPRFake{FakeClient: NewFakeClient()}
	f := &gitPRDialogFake{
		FakeClient: NewFakeClient(),
		prs:        []git.PRSummary{{Number: 42, Title: "弹窗改动", State: "open", ReviewDecision: "REVIEW_REQUIRED"}},
		detail: git.PRDetail{
			PRSummary:    git.PRSummary{Number: 42, Title: "弹窗改动", State: "open", ReviewDecision: "REVIEW_REQUIRED"},
			ChangedFiles: 2,
		},
		reviewGetPR: prFake.ReviewGetPR,
	}
	s := newGitPRDialogTestState(t, f)
	inspectorSelect(s, sidePaneFileID, false)

	s.openGitPRDialog()
	waitGitPRDialog(t, s, func(st *gitPRDialogState) bool {
		return st.selected == 42 && st.detail != nil && !st.detailLoading
	})

	tt := ui.NewTester(func(c *ui.Context) { renderGitPRDialog(c, s) }, 1200, 800)
	tt.Advance(time.Second)

	if err := tt.Click("改动 2"); err != nil {
		t.Fatalf("点击改动 Tab: %v", err)
	}
	dp := getDiffPanelState(s)
	waitReviewComments(t, func() bool {
		diffPanelMu.Lock()
		defer diffPanelMu.Unlock()
		return !dp.loading && len(dp.snapshot.Files) == 2
	})
	tt.Advance(time.Second)

	st := getGitPRDialogState(s)
	st.mu.Lock()
	open, tab := st.open, st.tab
	st.mu.Unlock()
	if !open || tab != "changes" {
		t.Fatalf("点击改动后状态 open=%v tab=%q，期望 open=true tab=changes", open, tab)
	}
	diffPanelMu.Lock()
	scope, prNum, reviewW := dp.scope, dp.prNumber, dp.reviewWidth
	diffPanelMu.Unlock()
	if scope != "pr" || prNum != 42 {
		t.Fatalf("diffPanelState scope=%q prNumber=%d，期望 pr/42", scope, prNum)
	}
	if reviewW <= 0 {
		t.Fatalf("弹窗未更新 dp.reviewWidth: %v", reviewW)
	}
	if !tt.HasText("a.go") || !tt.HasText("b.go") {
		t.Fatalf("弹窗未渲染文件卡片，文本: %v", tt.Texts())
	}
	if _, ok := tt.Access("总评"); !ok {
		t.Fatal("弹窗未渲染「总评」输入框")
	}
	if !inspectorPaneOpen(s, sidePaneFileID) || inspectorPaneOpen(s, sidePaneDiffID) {
		t.Fatal("切到弹窗改动 Tab 不应改变侧栏选中项")
	}

	// 点文件卡片不会改变侧栏选中项或暂停跟随，且展开后动画会收敛。
	if err := tt.Click("a.go"); err != nil {
		t.Fatalf("点击文件卡片 a.go: %v", err)
	}
	tt.Frame()
	list, _ := reviewFixture()
	list.Scope, list.Number, list.TurnID = "pr", 42, ""
	keyA := reviewKey(s, "thread-1", list, "a.go")
	waitReviewComments(t, func() bool {
		diffPanelMu.Lock()
		defer diffPanelMu.Unlock()
		entry := dp.contents[keyA]
		return entry != nil && !entry.loading
	})
	tt.Advance(time.Second)
	if !inspectorPaneOpen(s, sidePaneFileID) || inspectorFollowPaused(s) {
		t.Fatal("弹窗内点击文件卡片改变了侧栏或跟随状态")
	}
	if tt.FramePending() {
		t.Fatal("弹窗改动 Tab 动画推进结束后仍在请求下一帧")
	}

	// 切回概览后，「等待评审」状态点「开始评审」同样切到 changes 且不关弹窗、不切侧栏。
	if err := tt.Click("概览"); err != nil {
		t.Fatalf("切回概览 Tab: %v", err)
	}
	tt.Advance(time.Second)
	if err := tt.Click("开始评审"); err != nil {
		t.Fatalf("点击开始评审: %v", err)
	}
	waitReviewComments(t, func() bool {
		diffPanelMu.Lock()
		defer diffPanelMu.Unlock()
		return !dp.loading && len(dp.snapshot.Files) == 2
	})
	tt.Advance(time.Second)

	st.mu.Lock()
	open, tab = st.open, st.tab
	st.mu.Unlock()
	if !open || tab != "changes" {
		t.Fatalf("点击开始评审后状态 open=%v tab=%q，期望 open=true tab=changes", open, tab)
	}
	if !inspectorPaneOpen(s, sidePaneFileID) || inspectorPaneOpen(s, sidePaneDiffID) {
		t.Fatal("点击开始评审不应改变侧栏选中项")
	}
	if tt.FramePending() {
		t.Fatal("开始评审动画推进结束后仍在请求下一帧")
	}
}
