package mcodeui

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
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
	if err := tt.Click("创建"); err != nil {
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
			if err := tt.Click("创建"); err != nil {
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
