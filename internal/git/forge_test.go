package git

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"mcode/internal/testenv"
)

// forgeCall 记录一次被假执行器拦截的命令调用。
type forgeCall struct {
	dir  string
	name string
	args []string
}

// fakeForgeExecutor 是测试用的 CommandExecutor：记录调用并按 handler 返回结果。
type fakeForgeExecutor struct {
	calls   []forgeCall
	handler func(name string, args []string) ([]byte, error)
}

func (f *fakeForgeExecutor) run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, forgeCall{dir: dir, name: name, args: append([]string(nil), args...)})
	if f.handler != nil {
		return f.handler(name, args)
	}
	return []byte{}, nil
}

func (f *fakeForgeExecutor) client() *ForgeClient {
	return NewForgeClient(f.run)
}

func TestDetectForge(t *testing.T) {
	testenv.TempHome(t)

	cases := []struct {
		name string
		url  string
		want Forge
	}{
		{"https github", "https://github.com/owner/repo.git", ForgeGitHub},
		{"https github 无 .git", "https://github.com/owner/repo", ForgeGitHub},
		{"scp github", "git@github.com:owner/repo.git", ForgeGitHub},
		{"ssh 协议 github 带端口", "ssh://git@github.com:22/owner/repo.git", ForgeGitHub},
		{"https gitlab", "https://gitlab.com/owner/repo.git", ForgeGitLab},
		{"自建 gitlab 主机", "git@gitlab.example.com:group/repo.git", ForgeGitLab},
		{"gitlab 带端口的 https", "https://gitlab.example.com:8443/group/repo.git", ForgeGitLab},
		{"bitbucket 不支持", "git@bitbucket.org:owner/repo.git", ForgeUnknown},
		{"本地路径", "/tmp/local/repo.git", ForgeUnknown},
		{"空字符串", "", ForgeUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectForge(tc.url); got != tc.want {
				t.Errorf("DetectForge(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

func TestForgeClient_DefaultExecutorUsesOsExec(t *testing.T) {
	testenv.TempHome(t)
	client := NewForgeClient(nil)
	out, err := client.Exec(context.Background(), t.TempDir(), "echo", "hello")
	if err != nil {
		t.Fatalf("默认执行器调用失败: %v", err)
	}
	if !strings.Contains(string(out), "hello") {
		t.Errorf("默认执行器输出不包含 hello: %q", string(out))
	}
}

func TestForgeClient_GitHubCreatePullRequest(t *testing.T) {
	testenv.TempHome(t)
	exec := &fakeForgeExecutor{handler: func(name string, args []string) ([]byte, error) {
		return []byte("Creating pull request for feat into main in owner/repo\nhttps://github.com/owner/repo/pull/1\n"), nil
	}}
	client := exec.client()

	url, err := client.CreatePullRequest(context.Background(), "/work", ForgeGitHub, PullRequestSpec{
		Title: "T", Body: "B", Head: "feat", Base: "main",
	})
	if err != nil {
		t.Fatalf("CreatePullRequest failed: %v", err)
	}
	if url != "https://github.com/owner/repo/pull/1" {
		t.Errorf("URL = %q", url)
	}
	if len(exec.calls) != 1 {
		t.Fatalf("期望 1 次调用，实际 %d", len(exec.calls))
	}
	call := exec.calls[0]
	if call.name != "gh" || call.dir != "/work" {
		t.Errorf("调用 name=%q dir=%q", call.name, call.dir)
	}
	wantArgs := []string{"pr", "create", "--title", "T", "--body", "B", "--head", "feat", "--base", "main"}
	if !reflect.DeepEqual(call.args, wantArgs) {
		t.Errorf("argv = %v, want %v", call.args, wantArgs)
	}
}

func TestForgeClient_CreateTakesLastURL(t *testing.T) {
	testenv.TempHome(t)
	exec := &fakeForgeExecutor{handler: func(name string, args []string) ([]byte, error) {
		return []byte("see https://github.com/owner/repo/pull/1 and\nhttps://github.com/owner/repo/pull/2\n"), nil
	}}
	url, err := exec.client().CreatePullRequest(context.Background(), "/work", ForgeGitHub, PullRequestSpec{
		Title: "T", Body: "B", Head: "feat", Base: "main",
	})
	if err != nil {
		t.Fatalf("CreatePullRequest failed: %v", err)
	}
	if url != "https://github.com/owner/repo/pull/2" {
		t.Errorf("应取输出中最后一个 URL，实际 %q", url)
	}
}

func TestForgeClient_GitHubCreateFallsBackToViewWhenExists(t *testing.T) {
	testenv.TempHome(t)
	exec := &fakeForgeExecutor{handler: func(name string, args []string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "pr" && args[1] == "create" {
			return []byte("a pull request for branch \"feat\" into branch \"main\" already exists:\nhttps://github.com/owner/repo/pull/3\n"),
				errors.New("exit status 1")
		}
		if len(args) >= 2 && args[0] == "pr" && args[1] == "view" {
			return []byte("https://github.com/owner/repo/pull/3\n"), nil
		}
		return nil, fmt.Errorf("unexpected call: %v", args)
	}}
	url, err := exec.client().CreatePullRequest(context.Background(), "/work", ForgeGitHub, PullRequestSpec{
		Title: "T", Body: "B", Head: "feat", Base: "main",
	})
	if err != nil {
		t.Fatalf("CreatePullRequest failed: %v", err)
	}
	if url != "https://github.com/owner/repo/pull/3" {
		t.Errorf("URL = %q", url)
	}
	if len(exec.calls) != 2 {
		t.Fatalf("期望 2 次调用（create + view），实际 %d", len(exec.calls))
	}
	wantView := []string{"pr", "view", "feat", "--json", "url", "-q", ".url"}
	if !reflect.DeepEqual(exec.calls[1].args, wantView) {
		t.Errorf("view argv = %v, want %v", exec.calls[1].args, wantView)
	}
}

func TestForgeClient_GitLabCreatePullRequest(t *testing.T) {
	testenv.TempHome(t)
	exec := &fakeForgeExecutor{handler: func(name string, args []string) ([]byte, error) {
		return []byte("https://gitlab.com/owner/repo/-/merge_requests/5\n"), nil
	}}
	url, err := exec.client().CreatePullRequest(context.Background(), "/work", ForgeGitLab, PullRequestSpec{
		Title: "T", Body: "B", Head: "feat", Base: "main",
	})
	if err != nil {
		t.Fatalf("CreatePullRequest failed: %v", err)
	}
	if url != "https://gitlab.com/owner/repo/-/merge_requests/5" {
		t.Errorf("URL = %q", url)
	}
	wantArgs := []string{"mr", "create", "--title", "T", "--description", "B", "--source-branch", "feat", "--target-branch", "main", "--yes"}
	if len(exec.calls) != 1 || !reflect.DeepEqual(exec.calls[0].args, wantArgs) {
		t.Fatalf("argv = %v, want %v", exec.calls, wantArgs)
	}
}

func TestForgeClient_GitLabCreateReusesURLWhenExists(t *testing.T) {
	testenv.TempHome(t)
	exec := &fakeForgeExecutor{handler: func(name string, args []string) ([]byte, error) {
		return []byte("! MR already exists: https://gitlab.com/owner/repo/-/merge_requests/5\n"),
			errors.New("exit status 1")
	}}
	url, err := exec.client().CreatePullRequest(context.Background(), "/work", ForgeGitLab, PullRequestSpec{
		Title: "T", Body: "B", Head: "feat", Base: "main",
	})
	if err != nil {
		t.Fatalf("CreatePullRequest failed: %v", err)
	}
	if url != "https://gitlab.com/owner/repo/-/merge_requests/5" {
		t.Errorf("URL = %q", url)
	}
	if len(exec.calls) != 1 {
		t.Errorf("已存在时不应额外调用 CLI，实际 %d 次", len(exec.calls))
	}
}

// glab 失败时即使输出里带了 MR 地址（鉴权/网络失败常见），也不能当成功复用。
func TestForgeClient_GitLabCreateErrorDoesNotReuseURL(t *testing.T) {
	testenv.TempHome(t)
	exec := &fakeForgeExecutor{handler: func(name string, args []string) ([]byte, error) {
		return []byte("glab: 401 Unauthorized, see https://gitlab.com/owner/repo/-/merge_requests/5\n"),
			errors.New("exit status 1")
	}}
	url, err := exec.client().CreatePullRequest(context.Background(), "/work", ForgeGitLab, PullRequestSpec{
		Title: "T", Body: "B", Head: "feat", Base: "main",
	})
	if err == nil {
		t.Fatalf("鉴权失败时应返回错误，实际 url=%q", url)
	}
	if url != "" {
		t.Errorf("失败时不应返回 URL: %q", url)
	}
	if !strings.Contains(err.Error(), "Unauthorized") {
		t.Errorf("错误应保留命令输出，实际 %v", err)
	}
}

func TestForgeClient_MergePullRequest(t *testing.T) {
	testenv.TempHome(t)

	githubExec := &fakeForgeExecutor{}
	if err := githubExec.client().MergePullRequest(context.Background(), "/work", ForgeGitHub, "42"); err != nil {
		t.Fatalf("gh merge failed: %v", err)
	}
	wantGitHub := []string{"pr", "merge", "42", "--squash"}
	if len(githubExec.calls) != 1 || githubExec.calls[0].name != "gh" || !reflect.DeepEqual(githubExec.calls[0].args, wantGitHub) {
		t.Errorf("gh merge 调用 = %v, want %v", githubExec.calls, wantGitHub)
	}

	gitlabExec := &fakeForgeExecutor{}
	if err := gitlabExec.client().MergePullRequest(context.Background(), "/work", ForgeGitLab, "7"); err != nil {
		t.Fatalf("glab merge failed: %v", err)
	}
	wantGitLab := []string{"mr", "merge", "7", "--yes", "--squash", "--remove-source-branch"}
	if len(gitlabExec.calls) != 1 || gitlabExec.calls[0].name != "glab" || !reflect.DeepEqual(gitlabExec.calls[0].args, wantGitLab) {
		t.Errorf("glab merge 调用 = %v, want %v", gitlabExec.calls, wantGitLab)
	}
}

func TestForgeClient_UnsupportedForge(t *testing.T) {
	testenv.TempHome(t)
	exec := &fakeForgeExecutor{}
	client := exec.client()

	if _, err := client.CreatePullRequest(context.Background(), "/work", ForgeUnknown, PullRequestSpec{}); !errors.Is(err, ErrForgeUnsupported) {
		t.Errorf("未知平台创建应返回 ErrForgeUnsupported，实际 %v", err)
	}
	if err := client.MergePullRequest(context.Background(), "/work", ForgeUnknown, "1"); !errors.Is(err, ErrForgeUnsupported) {
		t.Errorf("未知平台合入应返回 ErrForgeUnsupported，实际 %v", err)
	}
	if len(exec.calls) != 0 {
		t.Errorf("未知平台不应调用任何 CLI，实际 %d 次", len(exec.calls))
	}
}

func TestForgeClient_ErrorWrapsOutput(t *testing.T) {
	testenv.TempHome(t)
	exec := &fakeForgeExecutor{handler: func(name string, args []string) ([]byte, error) {
		return []byte("GraphQL: 405 Checking in progress"), errors.New("exit status 1")
	}}
	_, err := exec.client().CreatePullRequest(context.Background(), "/work", ForgeGitHub, PullRequestSpec{
		Title: "T", Body: "B", Head: "feat", Base: "main",
	})
	if err == nil {
		t.Fatal("期望创建失败")
	}
	if !strings.Contains(err.Error(), "Checking") {
		t.Errorf("错误应保留命令输出，实际 %v", err)
	}
	if !IsRetryableForgeError(err) {
		t.Errorf("405/Checking 应判定为可重试，实际 %v", err)
	}
}

func TestForgeClient_CreateWithoutURL(t *testing.T) {
	testenv.TempHome(t)
	exec := &fakeForgeExecutor{handler: func(name string, args []string) ([]byte, error) {
		return []byte("created, but no link printed\n"), nil
	}}
	if _, err := exec.client().CreatePullRequest(context.Background(), "/work", ForgeGitHub, PullRequestSpec{
		Title: "T", Body: "B", Head: "feat", Base: "main",
	}); err == nil {
		t.Error("输出中没有 URL 时应返回错误")
	}
}

func TestIsRetryableForgeError(t *testing.T) {
	testenv.TempHome(t)

	retryable := []error{
		errors.New("glab mr create: exit status 1: 405 Not Allowed"),
		errors.New("gh pr merge: exit status 1: Checking in progress on request"),
		errors.New("gh pr merge: exit status 1: Pull request is not mergeable"),
		errors.New("gh pr create: exit status 1: request timeout after 30s"),
		errors.New("git push: connection reset by peer"),
		errors.New("fatal: could not resolve host: github.com"),
		fmt.Errorf("wrapped: %w", context.DeadlineExceeded),
	}
	for _, err := range retryable {
		if !IsRetryableForgeError(err) {
			t.Errorf("%v 应判定为可重试", err)
		}
	}

	notRetryable := []error{
		nil,
		ErrForgeUnsupported,
		errors.New("gh pr create: exit status 1: bad credentials"),
		errors.New("glab: authentication required, run glab auth login"),
	}
	for _, err := range notRetryable {
		if IsRetryableForgeError(err) {
			t.Errorf("%v 不应判定为可重试", err)
		}
	}
}
