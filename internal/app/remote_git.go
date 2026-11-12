package app

import (
	"context"
	"errors"

	"mcode/internal/git"
	"mcode/internal/remote"
	mcodeui "mcode/internal/ui"
)

// remoteGitService 把桌面端 Client 的 git 能力适配为远程 git 扩展方法的服务。
// 这些方法依赖 Client 自持的 git 执行器与仓库根目录定位（gitStashTarget），
// 因此在 app 层包一层并以 remote.GitExtServiceName 为名注入；
// 直接落在 MemoryBackend 上的方法（changedFiles / commitSelected / stash.pop）不经过这里。

// remoteGitService 实现 remote 包定义的 git 扩展服务接口。
type remoteGitService struct {
	client *Client
	*Client
}

func init() {
	registerRemoteWiring(func(a *App) {
		svc := newRemoteGitService(a.remoteClient())
		if svc == nil {
			// 没有 Client 时不注入：避免向注册表塞入 typed nil 导致取到半可用服务。
			return
		}
		a.backend.SetService(remote.GitExtServiceName, svc)
	})
}

// newRemoteGitService 构造 git 扩展服务；client 为 nil 时返回 nil。
func newRemoteGitService(client *Client) *remoteGitService {
	if client == nil {
		return nil
	}
	return &remoteGitService{client: client, Client: client}
}

// GitStashList 列出会话所在仓库的贮藏记录，最新一条在最前。
func (s *remoteGitService) GitStashList(ctx context.Context, threadID string) ([]git.StashEntry, error) {
	return s.client.GitStashList(ctx, threadID)
}

// GitStashPush 把会话所在仓库的工作区改动贮藏起来，message 非空时作为贮藏说明。
func (s *remoteGitService) GitStashPush(ctx context.Context, threadID, message string) error {
	runner, root, err := s.client.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	return runner.StashPush(ctx, root, message)
}

// GitStashApply 应用指定序号的贮藏，记录保留。
func (s *remoteGitService) GitStashApply(ctx context.Context, threadID string, index int) error {
	return s.client.GitStashApply(ctx, threadID, index)
}

// GitStashDrop 删除指定序号的贮藏记录，不影响工作区。
func (s *remoteGitService) GitStashDrop(ctx context.Context, threadID string, index int) error {
	return s.client.GitStashDrop(ctx, threadID, index)
}

// GitPRList 列出会话所在仓库开放的 PR/MR，limit<=0 时由 git 层取默认条数。
func (s *remoteGitService) GitPRList(ctx context.Context, threadID string, limit int) ([]git.PRSummary, error) {
	return s.client.GitPRList(ctx, threadID, limit)
}

// GitPRDetail 读取单条 PR/MR 的详情。
func (s *remoteGitService) GitPRDetail(ctx context.Context, threadID string, number int) (git.PRDetail, error) {
	return s.client.GitPRDetail(ctx, threadID, number)
}

// GitPush 推送会话所在仓库的当前分支到远程。
func (s *remoteGitService) GitPush(ctx context.Context, threadID string) error {
	return s.client.GitPush(ctx, threadID)
}

// GitPull 快进拉取当前分支的上游；不能快进时由 git 层报错，不改写工作区。
func (s *remoteGitService) GitPull(ctx context.Context, threadID string) error {
	return s.client.GitPull(ctx, threadID)
}

// GitFetch 抓取远程分支，不改动工作区与本地分支。
func (s *remoteGitService) GitFetch(ctx context.Context, threadID string) error {
	return s.client.GitFetch(ctx, threadID)
}

// GitCheckout 切换到本地已有分支。
func (s *remoteGitService) GitCheckout(ctx context.Context, threadID, branch string) error {
	return s.client.GitCheckout(ctx, threadID, branch)
}

// GitCreateBranch 从当前分支新建并切换到 name。
func (s *remoteGitService) GitCreateBranch(ctx context.Context, threadID, name string) error {
	return s.client.GitCreateBranch(ctx, threadID, name)
}

// GitMergeFrom 把 branch 合并进当前分支。
func (s *remoteGitService) GitMergeFrom(ctx context.Context, threadID, branch string) error {
	return s.client.GitMergeFrom(ctx, threadID, branch)
}

// GitGenerateCommitMessage 为未提交改动生成提交信息；未开启该功能或没有改动时返回原始错误。
func (s *remoteGitService) GitGenerateCommitMessage(ctx context.Context, threadID string) (string, error) {
	return s.client.GenerateCommitMessage(ctx, threadID)
}

// GitPullRequestCreateURL 取会话所在仓库的远端地址与当前分支，按桌面端同一套规则生成
// 创建 PR/MR 的网页地址。标题与正文沿用桌面表单的默认值（均为空），因此不预填。
// 没有远端、识别不到托管平台或处于游离 HEAD 时返回错误，让前端给出明确提示。
func (s *remoteGitService) GitPullRequestCreateURL(ctx context.Context, threadID string) (string, string, string, error) {
	st, err := s.client.GitState(ctx, threadID)
	if err != nil {
		return "", "", "", err
	}
	if st.Branch == "" {
		return "", "", "", errors.New("当前处于游离 HEAD，无法生成创建合并请求的链接")
	}
	target := mcodeui.GitPullRequestCreateURL(st.RemoteURL, st.Branch)
	if target == "" {
		return "", "", "", errors.New("没有识别到可用的远端仓库地址，无法生成创建合并请求的链接")
	}
	return target, "", "", nil
}
