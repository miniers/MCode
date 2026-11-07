package remote

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"mcode/internal/remote/wire"
)

// newGitWriteTestDispatcher 构造一个已注入 git 扩展服务的 Dispatcher。
func newGitWriteTestDispatcher(svc *gitExtFakeService) (*Dispatcher, Conn) {
	return newGitExtTestDispatcher(&gitExtTestBackend{services: map[string]any{GitExtServiceName: svc}})
}

// gitWriteAction 发一条写操作请求并断言成功，返回解码后的结果。
func gitWriteAction(t *testing.T, d *Dispatcher, conn Conn, method, params string, id int) wire.GitExtWriteActionResult {
	t.Helper()
	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"`+method+`","params":`+params+`,"id":`+strconv.Itoa(id)+`}`)
	var res wire.GitExtWriteActionResult
	gitExtResult(t, resp, &res)
	if !res.OK {
		t.Fatalf("%s: expected ok result, got %+v", method, res)
	}
	return res
}

// TestGitWriteExtMethodsInCapabilities 确认新方法出现在 capabilities 的方法清单里。
func TestGitWriteExtMethodsInCapabilities(t *testing.T) {
	methods := make(map[string]bool, len(ImplementedMethods()))
	for _, m := range ImplementedMethods() {
		methods[m] = true
	}
	for _, want := range []string{
		"git.push", "git.pull", "git.fetch", "git.checkout", "git.createBranch",
		"git.merge", "git.generateCommitMessage", "git.pr.createUrl",
	} {
		if !methods[want] {
			t.Fatalf("capabilities should list %s, got %v", want, ImplementedMethods())
		}
	}
}

// TestGitWriteExtNetworkMethods 覆盖 push / pull / fetch 三个只带 threadID 的网络操作。
func TestGitWriteExtNetworkMethods(t *testing.T) {
	svc := &gitExtFakeService{}
	d, conn := newGitWriteTestDispatcher(svc)
	params := `{"threadID":"` + gitExtTestThreadID + `"}`

	gitWriteAction(t, d, conn, "git.push", params, 1)
	gitWriteAction(t, d, conn, "git.pull", params, 2)
	gitWriteAction(t, d, conn, "git.fetch", params, 3)
	if svc.pushes != 1 || svc.pulls != 1 || svc.fetches != 1 {
		t.Fatalf("expected one call each, got pushes=%d pulls=%d fetches=%d", svc.pushes, svc.pulls, svc.fetches)
	}

	// threadID 不合法
	for i, method := range []string{"git.push", "git.pull", "git.fetch"} {
		resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"`+method+`","params":{"threadID":"nope"},"id":`+strconv.Itoa(10+i)+`}`)
		if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
			t.Fatalf("%s: expected InvalidParams, got: %+v", method, resp.Error)
		}
	}

	// 未知字段被严格解码拒绝
	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.push","params":{"threadID":"`+gitExtTestThreadID+`","force":true},"id":20}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams for unknown field, got: %+v", resp.Error)
	}

	// 服务未注入时按未实现处理
	d2, conn2 := newGitExtTestDispatcher(&gitExtTestBackend{})
	resp = callExtension(t, d2, conn2, `{"jsonrpc":"2.0","method":"git.push","params":`+params+`,"id":21}`)
	if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
		t.Fatalf("expected MethodNotFound, got: %+v", resp.Error)
	}

	// 推送失败保留原始错误文案
	svc.err = errors.New("fatal: 远端拒绝推送（non-fast-forward）")
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.push","params":`+params+`,"id":22}`)
	if resp.Error == nil || resp.Error.Code != wire.InternalError {
		t.Fatalf("expected InternalError, got: %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "non-fast-forward") {
		t.Fatalf("original error text should be preserved, got %q", resp.Error.Message)
	}
}

// TestGitWriteExtBranchMethods 覆盖 checkout / createBranch / merge 三个带分支名的操作。
func TestGitWriteExtBranchMethods(t *testing.T) {
	svc := &gitExtFakeService{}
	d, conn := newGitWriteTestDispatcher(svc)

	// 分支名去掉首尾空白后传给服务
	gitWriteAction(t, d, conn, "git.checkout", `{"threadID":"`+gitExtTestThreadID+`","branch":"  feat/git  "}`, 1)
	gitWriteAction(t, d, conn, "git.createBranch", `{"threadID":"`+gitExtTestThreadID+`","name":"feat/write"}`, 2)
	gitWriteAction(t, d, conn, "git.merge", `{"threadID":"`+gitExtTestThreadID+`","branch":"main"}`, 3)
	if len(svc.checkouts) != 1 || svc.checkouts[0] != "feat/git" {
		t.Fatalf("unexpected checkouts: %+v", svc.checkouts)
	}
	if len(svc.createdBranch) != 1 || svc.createdBranch[0] != "feat/write" {
		t.Fatalf("unexpected created branches: %+v", svc.createdBranch)
	}
	if len(svc.merged) != 1 || svc.merged[0] != "main" {
		t.Fatalf("unexpected merges: %+v", svc.merged)
	}

	// 分支名非法：空、全空白、以 '-' 开头、含空白或 git 拒绝的符号
	badNames := []string{"", "   ", "-x", "feat git", "a..b", "a~b", "a:b", "a?", "a*", "a[b", `a\b`, "a/", "a.lock", "a@{b}"}
	for i, name := range badNames {
		for j, method := range []string{"git.checkout", "git.merge"} {
			body := `{"threadID":"` + gitExtTestThreadID + `","branch":` + jsonString(name) + `}`
			resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"`+method+`","params":`+body+`,"id":`+strconv.Itoa(10+i*2+j)+`}`)
			if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
				t.Fatalf("%s(%q): expected InvalidParams, got: %+v", method, name, resp.Error)
			}
		}
		body := `{"threadID":"` + gitExtTestThreadID + `","name":` + jsonString(name) + `}`
		resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.createBranch","params":`+body+`,"id":`+strconv.Itoa(40+i)+`}`)
		if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
			t.Fatalf("git.createBranch(%q): expected InvalidParams, got: %+v", name, resp.Error)
		}
	}

	// 字段名写错会被严格解码拒绝
	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.checkout","params":{"threadID":"`+gitExtTestThreadID+`","name":"main"},"id":60}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams for wrong field, got: %+v", resp.Error)
	}

	// 服务未注入
	d2, conn2 := newGitExtTestDispatcher(&gitExtTestBackend{})
	resp = callExtension(t, d2, conn2, `{"jsonrpc":"2.0","method":"git.checkout","params":{"threadID":"`+gitExtTestThreadID+`","branch":"main"},"id":61}`)
	if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
		t.Fatalf("expected MethodNotFound, got: %+v", resp.Error)
	}

	// git 侧错误（例如有未提交改动导致无法切换）原样回传
	svc.err = errors.New("error: Your local changes to the following files would be overwritten by checkout")
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.checkout","params":{"threadID":"`+gitExtTestThreadID+`","branch":"main"},"id":62}`)
	if resp.Error == nil || resp.Error.Code != wire.InternalError {
		t.Fatalf("expected InternalError, got: %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "would be overwritten") {
		t.Fatalf("original error text should be preserved, got %q", resp.Error.Message)
	}
}

// TestGitWriteExtGenerateCommitMessage 覆盖 AI 生成提交信息。
func TestGitWriteExtGenerateCommitMessage(t *testing.T) {
	svc := &gitExtFakeService{commitMsg: "  feat: 分文件提交  \n"}
	d, conn := newGitWriteTestDispatcher(svc)

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.generateCommitMessage","params":{"threadID":"`+gitExtTestThreadID+`"},"id":1}`)
	var res wire.GitExtGenerateCommitMessageResult
	gitExtResult(t, resp, &res)
	if res.Message != "feat: 分文件提交" {
		t.Fatalf("expected trimmed message, got %q", res.Message)
	}

	// threadID 不合法
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.generateCommitMessage","params":{"threadID":"nope"},"id":2}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams, got: %+v", resp.Error)
	}

	// 功能未开启或没有改动时保留原始文案
	svc.err = errors.New("没有检测到未提交的代码改动")
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.generateCommitMessage","params":{"threadID":"`+gitExtTestThreadID+`"},"id":3}`)
	if resp.Error == nil || resp.Error.Code != wire.InternalError {
		t.Fatalf("expected InternalError, got: %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "没有检测到未提交的代码改动") {
		t.Fatalf("original error text should be preserved, got %q", resp.Error.Message)
	}

	// 服务未注入
	d2, conn2 := newGitExtTestDispatcher(&gitExtTestBackend{})
	resp = callExtension(t, d2, conn2, `{"jsonrpc":"2.0","method":"git.generateCommitMessage","params":{"threadID":"`+gitExtTestThreadID+`"},"id":4}`)
	if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
		t.Fatalf("expected MethodNotFound, got: %+v", resp.Error)
	}
}

// TestGitWriteExtPRCreateURL 覆盖创建 PR/MR 链接。
func TestGitWriteExtPRCreateURL(t *testing.T) {
	svc := &gitExtFakeService{prURL: "https://github.com/acme/repo/compare/feat%2Fgit?expand=1"}
	d, conn := newGitWriteTestDispatcher(svc)

	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.pr.createUrl","params":{"threadID":"`+gitExtTestThreadID+`"},"id":1}`)
	var res wire.GitExtPRCreateURLResult
	gitExtResult(t, resp, &res)
	if res.URL != svc.prURL {
		t.Fatalf("unexpected url: %q", res.URL)
	}
	if res.Title != "" || res.Body != "" {
		t.Fatalf("expected empty prefill, got title=%q body=%q", res.Title, res.Body)
	}

	// 宿主识别不到托管平台时返回明确的参数错误
	svc.prURL = ""
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.pr.createUrl","params":{"threadID":"`+gitExtTestThreadID+`"},"id":2}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams, got: %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "remote") {
		t.Fatalf("unexpected message: %q", resp.Error.Message)
	}

	// threadID 不合法
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.pr.createUrl","params":{"threadID":"nope"},"id":3}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams, got: %+v", resp.Error)
	}

	// 服务未注入
	d2, conn2 := newGitExtTestDispatcher(&gitExtTestBackend{})
	resp = callExtension(t, d2, conn2, `{"jsonrpc":"2.0","method":"git.pr.createUrl","params":{"threadID":"`+gitExtTestThreadID+`"},"id":4}`)
	if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
		t.Fatalf("expected MethodNotFound, got: %+v", resp.Error)
	}
}

// TestGitExtCommitSelectedPushAfter 覆盖提交后推送：推送成功、推送失败与未要求推送三种情形。
func TestGitExtCommitSelectedPushAfter(t *testing.T) {
	svc := &gitExtFakeService{}
	backend := &gitExtTestBackend{services: map[string]any{GitExtServiceName: svc}}
	d, conn := newGitExtTestDispatcher(backend)
	params := func(pushAfter string) string {
		return `{"threadID":"` + gitExtTestThreadID + `","paths":["a.go"],"message":"m"` + pushAfter + `}`
	}

	// 不带 pushAfter：不推送
	resp := callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.commitSelected","params":`+params("")+`,"id":1}`)
	var res wire.GitExtCommitSelectedResult
	gitExtResult(t, resp, &res)
	if !res.OK || res.PushError != "" {
		t.Fatalf("unexpected result without pushAfter: %+v", res)
	}
	if svc.pushes != 0 {
		t.Fatalf("push should not run without pushAfter, got %d", svc.pushes)
	}

	// pushAfter 为真：提交后推送
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.commitSelected","params":`+params(`,"pushAfter":true`)+`,"id":2}`)
	gitExtResult(t, resp, &res)
	if !res.OK || res.PushError != "" {
		t.Fatalf("unexpected result with pushAfter: %+v", res)
	}
	if svc.pushes != 1 {
		t.Fatalf("expected one push, got %d", svc.pushes)
	}
	if len(backend.commits) != 2 {
		t.Fatalf("expected two commits, got %+v", backend.commits)
	}

	// 推送失败：提交仍算成功，失败原因放在 pushError 里，凭证被脱敏
	const token = "ghp_abcdefghijklmnopqrstuvwx"
	svc.pushErr = errors.New("fatal: could not read Password for 'https://acme:" + token + "@github.com/acme/repo.git'")
	resp = callExtension(t, d, conn, `{"jsonrpc":"2.0","method":"git.commitSelected","params":`+params(`,"pushAfter":true`)+`,"id":3}`)
	gitExtResult(t, resp, &res)
	if !res.OK {
		t.Fatalf("commit should still be reported as done, got %+v", res)
	}
	if !strings.Contains(res.PushError, "could not read Password") {
		t.Fatalf("pushError should carry the failure text, got %q", res.PushError)
	}
	if strings.Contains(res.PushError, token) {
		t.Fatalf("pushError should redact credentials, got %q", res.PushError)
	}

	// pushAfter 为真但服务未注入：按未实现处理，不静默忽略
	d2, conn2 := newGitExtTestDispatcher(&gitExtTestBackend{})
	resp = callExtension(t, d2, conn2, `{"jsonrpc":"2.0","method":"git.commitSelected","params":`+params(`,"pushAfter":true`)+`,"id":4}`)
	if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
		t.Fatalf("expected MethodNotFound, got: %+v", resp.Error)
	}
}

// jsonString 把字符串编码成 JSON 字符串字面量（只用于测试里的分支名）。
func jsonString(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(raw)
}
