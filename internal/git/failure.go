package git

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"mcode/internal/redact"
)

type FailureKind string

const (
	FailureAuthentication FailureKind = "authentication"
	FailureHostKey        FailureKind = "host_key"
	FailureNetwork        FailureKind = "network"
	FailureRejected       FailureKind = "rejected"
	FailureDiverged       FailureKind = "diverged"
	FailureLease          FailureKind = "lease"
	FailureProtected      FailureKind = "protected"
	FailureNoUpstream     FailureKind = "no_upstream"
	FailureDetachedHead   FailureKind = "detached_head"
	FailureHook           FailureKind = "hook"
	FailureIndexLock      FailureKind = "index_lock"
	FailureConflict       FailureKind = "conflict"
	FailureWorktree       FailureKind = "worktree"
	FailureCanceled       FailureKind = "canceled"
	FailureOther          FailureKind = "other"
)

type Failure struct {
	Kind                FailureKind
	Title, Hint, Detail string
}

var gitErrorPrefix = regexp.MustCompile(`(?s)^.*?git [^:]+: exit status \d+:\s*`)

func ClassifyError(err error) Failure {
	if err == nil {
		return Failure{}
	}
	detail := redact.Secrets(gitErrorPrefix.ReplaceAllString(err.Error(), ""))
	s := strings.ToLower(detail)
	has := func(parts ...string) bool {
		for _, p := range parts {
			if strings.Contains(s, p) {
				return true
			}
		}
		return false
	}
	f := Failure{Kind: FailureOther, Title: "Git 操作失败", Hint: "查看错误详情后重试", Detail: detail}
	switch {
	case errors.Is(err, context.Canceled):
		f.Kind, f.Title, f.Hint = FailureCanceled, "操作已取消", "需要时重新操作"
	case has("stale info"):
		f.Kind, f.Title, f.Hint = FailureLease, "远端分支已更新", "先获取远端更新后再检查推送内容"
	case has("protected branch", "protected branch hook declined", "gh006", "gh013"):
		f.Kind, f.Title, f.Hint = FailureProtected, "分支受到保护", "按仓库规则提交更改"
	case has("host key verification failed", "remote host identification has changed", "host key is unknown", "no matching host key"):
		f.Kind, f.Title, f.Hint = FailureHostKey, "SSH 主机密钥验证失败", "核对远端主机密钥后重试"
	case has("authentication failed", "permission denied (publickey)", "could not read username", "invalid username or password", "access denied"):
		f.Kind, f.Title, f.Hint = FailureAuthentication, "身份验证失败", "检查凭据和访问权限"
	case has("could not resolve host", "network is unreachable", "connection timed out", "connection refused", "unable to access", "operation timed out"):
		f.Kind, f.Title, f.Hint = FailureNetwork, "无法连接远端", "检查网络连接后重试"
	case has("no upstream", "no tracking information", "没有配置上游", "has no upstream branch"):
		f.Kind, f.Title, f.Hint = FailureNoUpstream, "分支没有上游", "设置上游分支后重试"
	case has("detached head", "分离 head"):
		f.Kind, f.Title, f.Hint = FailureDetachedHead, "当前处于分离 HEAD 状态", "切换到分支后重试"
	case has("index.lock", "another git process seems to be running"):
		f.Kind, f.Title, f.Hint = FailureIndexLock, "Git 索引被占用", "等待其他 Git 操作完成后重试"
	case has("unmerged files", "unmerged paths", "resolve your current index first", "未解决的冲突"):
		f.Kind, f.Title, f.Hint = FailureConflict, "存在未解决的冲突", "解决冲突后重试"
	case has("would be overwritten", "would be lost", "your local changes to the following files"):
		f.Kind, f.Title, f.Hint = FailureWorktree, "工作区改动会被覆盖", "先提交或贮藏改动"
	case has("pre-commit", "pre-push", "commit-msg", "hook declined", "hook failed"):
		f.Kind, f.Title, f.Hint = FailureHook, "Git 钩子拒绝了操作", "检查钩子的错误信息"
	case has("non-fast-forward", "fetch first", "[rejected]", "failed to push some refs"):
		f.Kind, f.Title, f.Hint = FailureRejected, "远端有新的提交", "先拉取再推送"
	case has("not possible to fast-forward", "divergent branches", "have diverged"):
		f.Kind, f.Title, f.Hint = FailureDiverged, "本地与远端已分叉", "选择变基或合并来拉取"
	}
	return f
}
