package mcodeui

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

func TestGitPRCreatePrefillsAndOpensURL(t *testing.T) {
	s, ps, client := gitPaneTestState(t)
	client.SetGenerateCommitMessageResult("feat: 登录\n\n修复登录流程", nil)
	ps.state = GitState{WorkDir: "/repo", RemoteURL: "git@github.com:owner/repo.git", Branch: "feature/login", HasUpstream: true}
	openGitPRCreateDialog(s, ps.threadID)
	d := ps.prCreate
	gitPaneWait(t, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return !d.generating })
	d.mu.Lock()
	title, body := d.title, d.body
	d.mu.Unlock()
	if title != "feat: 登录" || body != "修复登录流程" {
		t.Fatalf("预填内容 = %q/%q", title, body)
	}
	tt := ui.NewTester(func(c *ui.Context) { renderGitPRCreateDialog(c, d) }, 800, 700)
	tt.Advance(DurSheet)
	if err := tt.Click("创建"); err != nil {
		t.Fatal(err)
	}
	urls := tt.OpenedURLs()
	if len(urls) != 1 {
		t.Fatalf("创建地址 = %v", urls)
	}
	u, err := url.Parse(urls[0])
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "github.com" || u.Query().Get("title") != title || u.Query().Get("body") != body || !strings.Contains(u.Path, "feature/login") {
		t.Fatalf("预填 URL = %s", u)
	}
	d.mu.Lock()
	open := d.open
	d.mu.Unlock()
	if open {
		t.Fatal("打开网页后弹窗仍未关闭")
	}
}

func TestGitPRCreateErrorsAndEscape(t *testing.T) {
	s, ps, client := gitPaneTestState(t)
	client.SetGenerateCommitMessageResult("", errors.New("模型不可用"))
	ps.state = GitState{WorkDir: "/repo", Branch: "feature"}
	openGitPRCreateDialog(s, ps.threadID)
	d := ps.prCreate
	gitPaneWait(t, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return !d.generating })
	tt := ui.NewTester(func(c *ui.Context) { renderGitPRCreateDialog(c, d) }, 800, 700)
	tt.Advance(DurSheet)
	if !tt.HasText("AI 生成失败: 模型不可用") {
		t.Fatal("生成错误未显示")
	}
	if err := tt.Click("标题"); err != nil {
		t.Fatal(err)
	}
	tt.Type("feat: 测试")
	tt.Frame()
	if err := tt.Click("推送并创建"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("没有识别到可用的远端仓库地址") || len(tt.OpenedURLs()) != 0 {
		t.Fatal("无效远端应保留弹窗并显示错误")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	d.mu.Lock()
	open := d.open
	d.mu.Unlock()
	if open {
		t.Fatal("Escape 后弹窗仍然打开")
	}
}

type gitPRDelayedClient struct {
	*gitPaneFakeClient
	started  chan context.Context
	release  chan struct{}
	returned chan struct{}
}

func (f *gitPRDelayedClient) GeneratePRDescription(ctx context.Context, threadID string) (string, error) {
	f.started <- ctx
	<-f.release
	defer close(f.returned)
	return "旧生成标题\n旧生成正文", nil
}

func TestGitPRCreateLateGenerationPreservesDraftAndClose(t *testing.T) {
	for _, closeDialog := range []bool{false, true} {
		t.Run(map[bool]string{false: "保留编辑", true: "关闭丢弃"}[closeDialog], func(t *testing.T) {
			s, ps, base := gitPaneTestState(t)
			client := &gitPRDelayedClient{gitPaneFakeClient: base, started: make(chan context.Context, 1), release: make(chan struct{}), returned: make(chan struct{})}
			s.client = client
			ps.state = GitState{WorkDir: "/repo"}
			openGitPRCreateDialog(s, ps.threadID)
			d := ps.prCreate
			var ctx context.Context
			select {
			case ctx = <-client.started:
			case <-time.After(time.Second):
				t.Fatal("生成未开始")
			}
			if closeDialog {
				ps.dispose()
			} else {
				tt := ui.NewTester(func(c *ui.Context) { renderGitPRCreateDialog(c, d) }, 800, 700)
				tt.Advance(DurSheet)
				if err := tt.Click("标题"); err != nil {
					t.Fatal(err)
				}
				tt.Type("用户草稿")
				tt.Frame()
			}
			close(client.release)
			select {
			case <-client.returned:
			case <-time.After(time.Second):
				t.Fatal("生成没有退出")
			}
			gitPaneWait(t, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return !d.generating })
			d.mu.Lock()
			title, body, open := d.title, d.body, d.open
			d.mu.Unlock()
			if closeDialog {
				if ctx.Err() != context.Canceled || open || title != "" || body != "" {
					t.Fatalf("关闭后仍接受生成结果：%q/%q，%v", title, body, open)
				}
			} else if title != "用户草稿" || body != "" {
				t.Fatalf("生成覆盖了用户输入：%q/%q", title, body)
			}
		})
	}
}

func TestGitPullRequestFormURLPreservesProviderFields(t *testing.T) {
	for _, repo := range []string{"https://github.com/owner/repo", "https://gitlab.com/group/repo"} {
		target := gitPullRequestFormURL(repo, "feature/a", " 标题 & 引号 ", " 正文\n下一行 ")
		u, err := url.Parse(target)
		if err != nil {
			t.Fatal(err)
		}
		titleKey, bodyKey := "title", "body"
		if strings.Contains(repo, "gitlab") {
			titleKey, bodyKey = "merge_request[title]", "merge_request[description]"
		}
		if u.Query().Get(titleKey) != "标题 & 引号" || u.Query().Get(bodyKey) != "正文\n下一行" {
			t.Fatalf("URL 字段丢失：%s", target)
		}
		if got := GitPullRequestCreateURL(repo+".git", "feature/a"); got != gitPullRequestURL(repo, "feature/a") {
			t.Fatalf("远程协议 URL 改变：%s", got)
		}
	}
	if got := GitPullRequestCreateURL("", "feature"); got != "" {
		t.Fatalf("空远端生成了 URL：%s", got)
	}
}

type gitPRPushClient struct {
	*gitPaneFakeClient
	started chan struct{}
	release chan struct{}
	pushErr error
}

func (f *gitPRPushClient) GitPush(ctx context.Context, _ string) error {
	close(f.started)
	select {
	case <-f.release:
		return f.pushErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestGitPRCreatePushBeforeOpen(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "成功", true: "失败"}[fail], func(t *testing.T) {
			s, ps, base := gitPaneTestState(t)
			base.SetGenerateCommitMessageResult("feat: 推送创建\n\n详细说明", nil)
			client := &gitPRPushClient{gitPaneFakeClient: base, started: make(chan struct{}), release: make(chan struct{})}
			if fail {
				client.pushErr = errors.New("push failed")
			}
			s.client = client
			ps.state = GitState{WorkDir: "/repo", RemoteURL: "git@github.com:owner/repo.git", Branch: "feature"}
			openGitPRCreateDialog(s, ps.threadID)
			d := ps.prCreate
			gitPaneWait(t, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return !d.generating })
			tt := ui.NewTester(func(c *ui.Context) { renderGitPRCreateDialog(c, d) }, 800, 700)
			tt.Advance(DurSheet)
			if err := tt.Click("推送并创建"); err != nil {
				t.Fatal(err)
			}
			select {
			case <-client.started:
			case <-time.After(time.Second):
				t.Fatal("推送未开始")
			}
			if len(tt.OpenedURLs()) != 0 {
				t.Fatal("推送前打开网页")
			}
			close(client.release)
			gitPaneWait(t, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return !d.pushing })
			tt.Frame()
			if fail {
				if len(tt.OpenedURLs()) != 0 {
					t.Fatal("推送失败后打开网页")
				}
				d.mu.Lock()
				errText := d.err
				d.mu.Unlock()
				if errText == "" {
					t.Fatal("推送错误未显示")
				}
			} else if len(tt.OpenedURLs()) != 1 {
				t.Fatal("推送成功后未打开网页")
			}
		})
	}
}

type gitPRCreateCall struct {
	threadID, title, body, base string
}

type gitPRCreateFakeClient struct {
	*gitPaneFakeClient
	mu            sync.Mutex
	branches      []git.BranchInfo
	pushStarted   chan struct{}
	pushRelease   chan struct{}
	pushErr       error
	createStarted chan struct{}
	createRelease chan struct{}
	createURL     string
	createErr     error
	createCalls   []gitPRCreateCall
	order         []string
	prList        []git.PRSummary
	prDetail      git.PRDetail
}

func (f *gitPRCreateFakeClient) GitBranches(context.Context, string) ([]git.BranchInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]git.BranchInfo(nil), f.branches...), nil
}

func (f *gitPRCreateFakeClient) GitPush(ctx context.Context, _ string) error {
	f.mu.Lock()
	f.order = append(f.order, "push")
	started, release, err := f.pushStarted, f.pushRelease, f.pushErr
	f.mu.Unlock()
	if started != nil {
		select {
		case <-started:
		default:
			close(started)
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (f *gitPRCreateFakeClient) GitPRCreate(ctx context.Context, threadID, title, body, base string) (string, error) {
	f.mu.Lock()
	f.order = append(f.order, "create")
	f.createCalls = append(f.createCalls, gitPRCreateCall{threadID: threadID, title: title, body: body, base: base})
	started, release, urlStr, err := f.createStarted, f.createRelease, f.createURL, f.createErr
	f.mu.Unlock()
	if started != nil {
		select {
		case <-started:
		default:
			close(started)
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return urlStr, err
}

func (f *gitPRCreateFakeClient) GitPRList(context.Context, string, int) ([]git.PRSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]git.PRSummary(nil), f.prList...), nil
}

func (f *gitPRCreateFakeClient) GitPRDetail(context.Context, string, int) (git.PRDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prDetail, nil
}

func TestGitPRCreateDialogInAppPushedCreatesAndOpensPR(t *testing.T) {
	s, ps, base := gitPaneTestState(t)
	base.SetGenerateCommitMessageResult("feat: 登录\n\n修复登录流程", nil)
	client := &gitPRCreateFakeClient{
		gitPaneFakeClient: base,
		createURL:         "https://github.com/owner/repo/pull/129",
		prList:            []git.PRSummary{{Number: 129, Title: "feat: 登录", State: "open"}},
		prDetail:          git.PRDetail{PRSummary: git.PRSummary{Number: 129, Title: "feat: 登录", State: "open"}},
	}
	s.client = client
	ps.state = GitState{
		WorkDir:     "/repo",
		RemoteURL:   "git@github.com:owner/repo.git",
		Branch:      "feature/login",
		Branches:    []string{"feature/login", "develop", "main"},
		HasUpstream: true,
	}
	openGitPRCreateDialog(s, ps.threadID)
	d := ps.prCreate
	gitPaneWait(t, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return !d.generating })

	tt := ui.NewTester(func(c *ui.Context) { renderGitPRCreateDialog(c, d) }, 800, 700)
	tt.Advance(DurSheet)
	if !tt.HasText("创建") || tt.HasText("推送并创建") {
		t.Fatal("已推送时主按钮应为「创建」")
	}
	if err := tt.Click("创建"); err != nil {
		t.Fatal(err)
	}
	gitPaneWait(t, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return !d.creating && d.createdNumber == 129 })
	tt.Advance(DurSlow)
	if tt.FramePending() {
		t.Fatal("成功态切换动画结束后不应继续排帧")
	}

	client.mu.Lock()
	order := slices.Clone(client.order)
	calls := slices.Clone(client.createCalls)
	client.mu.Unlock()
	if !slices.Equal(order, []string{"create"}) {
		t.Fatalf("调用顺序 = %v, 期望 [create]", order)
	}
	if len(calls) != 1 || calls[0] != (gitPRCreateCall{threadID: ps.threadID, title: "feat: 登录", body: "修复登录流程", base: "main"}) {
		t.Fatalf("GitPRCreate 参数 = %+v", calls)
	}
	if len(tt.OpenedURLs()) != 0 {
		t.Fatalf("应用内创建不应打开浏览器: %v", tt.OpenedURLs())
	}
	if !tt.HasText("#129 已创建") {
		t.Fatal("未显示「#129 已创建」")
	}

	if err := tt.Click("查看"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	d.mu.Lock()
	createOpen := d.open
	d.mu.Unlock()
	if createOpen {
		t.Fatal("点击查看后创建弹窗应关闭")
	}
	prState := getGitPRDialogState(s)
	prState.mu.Lock()
	prOpen, selected := prState.open, prState.selected
	prState.mu.Unlock()
	if !prOpen || selected != 129 {
		t.Fatalf("PR 弹窗状态 = (open=%v, selected=%d), 期望 (true, 129)", prOpen, selected)
	}
}

func TestGitPRCreateDialogInAppUnpushedPushesThenCreates(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state GitState
	}{
		{"无上游分支", GitState{WorkDir: "/repo", RemoteURL: "git@github.com:owner/repo.git", Branch: "feature/push", Branches: []string{"main", "feature/push"}}},
		{"领先上游提交", GitState{WorkDir: "/repo", RemoteURL: "git@github.com:owner/repo.git", Branch: "feature/push", Branches: []string{"main", "feature/push"}, HasUpstream: true, Ahead: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, ps, base := gitPaneTestState(t)
			base.SetGenerateCommitMessageResult("feat: 推送再创建\n\n正文", nil)
			client := &gitPRCreateFakeClient{
				gitPaneFakeClient: base,
				pushStarted:       make(chan struct{}),
				pushRelease:       make(chan struct{}),
				createURL:         "https://github.com/owner/repo/pull/88",
			}
			s.client = client
			ps.state = tc.state
			openGitPRCreateDialog(s, ps.threadID)
			d := ps.prCreate
			gitPaneWait(t, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return !d.generating })

			tt := ui.NewTester(func(c *ui.Context) { renderGitPRCreateDialog(c, d) }, 800, 700)
			tt.Advance(DurSheet)
			if err := tt.Click("推送并创建"); err != nil {
				t.Fatal(err)
			}
			select {
			case <-client.pushStarted:
			case <-time.After(time.Second):
				t.Fatal("推送未开始")
			}
			close(client.pushRelease)
			gitPaneWait(t, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return !d.pushing && !d.creating && d.createdNumber == 88 })
			tt.Advance(DurSlow)

			client.mu.Lock()
			order := slices.Clone(client.order)
			calls := slices.Clone(client.createCalls)
			client.mu.Unlock()
			if !slices.Equal(order, []string{"push", "create"}) {
				t.Fatalf("调用顺序 = %v, 期望 [push create]", order)
			}
			if len(calls) != 1 || calls[0].base != "main" {
				t.Fatalf("GitPRCreate 参数 = %+v", calls)
			}
			if !tt.HasText("#88 已创建") || len(tt.OpenedURLs()) != 0 {
				t.Fatal("未进入成功态或误开浏览器")
			}
		})
	}
}

func TestGitPRCreateDialogInAppSwitchBaseBranch(t *testing.T) {
	s, ps, base := gitPaneTestState(t)
	base.SetGenerateCommitMessageResult("feat: 切换目标分支\n\n正文内容", nil)
	client := &gitPRCreateFakeClient{
		gitPaneFakeClient: base,
		branches: []git.BranchInfo{
			{Name: "feature/login", Head: true},
			{Name: "main"},
			{Name: "develop"},
			{Name: "origin/develop", Remote: true},
		},
		createURL: "https://github.com/owner/repo/pull/9",
	}
	s.client = client
	ps.state = GitState{
		WorkDir:     "/repo",
		RemoteURL:   "git@github.com:owner/repo.git",
		Branch:      "feature/login",
		HasUpstream: true,
	}
	openGitPRCreateDialog(s, ps.threadID)
	d := ps.prCreate
	gitPaneWait(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return !d.generating && len(d.branches) == 2 && d.base == "main"
	})

	tt := ui.NewTester(func(c *ui.Context) { renderGitPRCreateDialog(c, d) }, 800, 700)
	tt.Advance(DurSheet)
	if err := tt.Click("目标分支"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Access("origin/develop"); ok {
		t.Fatal("基分支列表不应包含远端跟踪分支")
	}
	if err := tt.Click("develop"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("创建"); err != nil {
		t.Fatal(err)
	}
	gitPaneWait(t, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return !d.creating && d.createdNumber == 9 })

	client.mu.Lock()
	calls := slices.Clone(client.createCalls)
	client.mu.Unlock()
	if len(calls) != 1 || calls[0].base != "develop" {
		t.Fatalf("切换 base 后参数 = %+v, 期望 base=develop", calls)
	}
}

func TestGitPRCreateDialogInAppUnsupportedFallsBackToBrowser(t *testing.T) {
	s, ps, base := gitPaneTestState(t)
	base.SetGenerateCommitMessageResult("feat: 退回浏览器\n\n正文", nil)
	client := &gitPRCreateFakeClient{
		gitPaneFakeClient: base,
		createErr:         git.ErrForgeUnsupported,
	}
	s.client = client
	ps.state = GitState{
		WorkDir:     "/repo",
		RemoteURL:   "git@github.com:owner/repo.git",
		Branch:      "feature/fallback",
		Branches:    []string{"main", "feature/fallback"},
		HasUpstream: true,
	}
	openGitPRCreateDialog(s, ps.threadID)
	d := ps.prCreate
	gitPaneWait(t, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return !d.generating })

	tt := ui.NewTester(func(c *ui.Context) { renderGitPRCreateDialog(c, d) }, 800, 700)
	tt.Advance(DurSheet)
	if err := tt.Click("创建"); err != nil {
		t.Fatal(err)
	}
	gitPaneWait(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return !d.creating && (!d.open || d.readyURL != "")
	})
	tt.Frame()
	if len(tt.OpenedURLs()) != 1 {
		t.Fatalf("ErrForgeUnsupported 应退回浏览器打开创建页，实际 %v", tt.OpenedURLs())
	}
	d.mu.Lock()
	open := d.open
	d.mu.Unlock()
	if open {
		t.Fatal("退回浏览器打开后弹窗应关闭")
	}
}

func TestGitPRCreateDialogInAppErrorKeepsDialogAndInput(t *testing.T) {
	s, ps, base := gitPaneTestState(t)
	base.SetGenerateCommitMessageResult("feat: 保留输入\n\n原有正文", nil)
	client := &gitPRCreateFakeClient{
		gitPaneFakeClient: base,
		createErr:         errors.New("当前分支已存在开放 PR"),
	}
	s.client = client
	ps.state = GitState{
		WorkDir:     "/repo",
		RemoteURL:   "git@github.com:owner/repo.git",
		Branch:      "feature/err",
		Branches:    []string{"main", "feature/err"},
		HasUpstream: true,
	}
	openGitPRCreateDialog(s, ps.threadID)
	d := ps.prCreate
	gitPaneWait(t, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return !d.generating })

	tt := ui.NewTester(func(c *ui.Context) { renderGitPRCreateDialog(c, d) }, 800, 700)
	tt.Advance(DurSheet)
	if err := tt.Click("创建"); err != nil {
		t.Fatal(err)
	}
	gitPaneWait(t, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return !d.creating && d.err != "" })
	tt.Frame()
	if !tt.HasText("当前分支已存在开放 PR") {
		t.Fatal("弹窗内未显示创建错误")
	}
	if len(tt.OpenedURLs()) != 0 {
		t.Fatalf("失败时不应打开浏览器: %v", tt.OpenedURLs())
	}
	d.mu.Lock()
	open, title, body := d.open, d.title, d.body
	d.mu.Unlock()
	if !open || title != "feat: 保留输入" || body != "原有正文" {
		t.Fatalf("失败后状态 = (open=%v, title=%q, body=%q)", open, title, body)
	}
}

func TestGitPRCreateDialogInAppAIGenerateEmptyDisableAndReduceMotion(t *testing.T) {
	s, ps, base := gitPaneTestState(t)
	base.SetGenerateCommitMessageResult(" ", nil)
	client := &gitPRCreateFakeClient{
		gitPaneFakeClient: base,
		createURL:         "https://example.com/pulls/new",
	}
	s.client = client
	ps.state = GitState{
		WorkDir:     "/repo",
		RemoteURL:   "git@github.com:owner/repo.git",
		Branch:      "feature/ai",
		Branches:    []string{"master", "release"},
		HasUpstream: true,
	}
	openGitPRCreateDialog(s, ps.threadID)
	d := ps.prCreate
	gitPaneWait(t, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return !d.generating })

	tt := ui.NewTester(func(c *ui.Context) { renderGitPRCreateDialog(c, d) }, 800, 700)
	setAppReduceMotion(t, true)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Advance(DurSheet)

	if node, ok := tt.Access("创建"); !ok || !node.Disabled {
		t.Fatalf("标题为空时创建按钮应禁用: %+v, ok=%v", node, ok)
	}
	if !tt.HasText("master") {
		t.Fatal("无 main 时默认基分支应为 master")
	}

	base.SetGenerateCommitMessageResult("feat: AI 重生成\n\n重生成正文", nil)
	if err := tt.Click("AI 生成"); err != nil {
		t.Fatal(err)
	}
	gitPaneWait(t, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return !d.generating && d.title == "feat: AI 重生成" })
	tt.Frame()
	if node, ok := tt.Access("创建"); !ok || node.Disabled {
		t.Fatalf("生成标题后创建按钮应启用: %+v, ok=%v", node, ok)
	}
	if err := tt.Click("创建"); err != nil {
		t.Fatal(err)
	}
	gitPaneWait(t, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return !d.creating && d.createdURL != "" })
	tt.Frame()
	if tt.FramePending() {
		t.Fatal("ReduceMotion 开启时成功态切换不应继续排帧")
	}
	if !tt.HasText("已创建") || tt.HasText("#") {
		t.Fatal("无法解析编号时应只显示「已创建」")
	}
	if err := tt.Click("查看"); err != nil {
		t.Fatal(err)
	}
	if urls := tt.OpenedURLs(); len(urls) != 1 || urls[0] != "https://example.com/pulls/new" {
		t.Fatalf("无编号 URL 点击查看应打开原地址，实际 %v", urls)
	}
}

func TestGitPRCreateDialogInAppCloseCancelsAndDropsLateResult(t *testing.T) {
	s, ps, base := gitPaneTestState(t)
	base.SetGenerateCommitMessageResult("feat: 迟到结果\n\n正文", nil)
	client := &gitPRCreateFakeClient{
		gitPaneFakeClient: base,
		createStarted:     make(chan struct{}),
		createRelease:     make(chan struct{}),
		createURL:         "https://github.com/owner/repo/pull/55",
	}
	s.client = client
	ps.state = GitState{
		WorkDir:     "/repo",
		RemoteURL:   "git@github.com:owner/repo.git",
		Branch:      "feature/cancel",
		Branches:    []string{"develop"},
		HasUpstream: true,
	}
	openGitPRCreateDialog(s, ps.threadID)
	d := ps.prCreate
	gitPaneWait(t, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return !d.generating })

	tt := ui.NewTester(func(c *ui.Context) { renderGitPRCreateDialog(c, d) }, 800, 700)
	tt.Advance(DurSheet)
	if err := tt.Click("创建"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-client.createStarted:
	case <-time.After(time.Second):
		t.Fatal("创建未开始")
	}
	d.close()
	close(client.createRelease)
	time.Sleep(20 * time.Millisecond)
	d.mu.Lock()
	open, createdURL, createdNumber := d.open, d.createdURL, d.createdNumber
	d.mu.Unlock()
	if open || createdURL != "" || createdNumber != 0 {
		t.Fatalf("关闭后迟到结果未被丢弃: open=%v url=%q num=%d", open, createdURL, createdNumber)
	}
}
