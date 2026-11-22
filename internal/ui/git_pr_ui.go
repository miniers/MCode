package mcodeui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"mcode/internal/git"
)

// PR 只读查看弹窗：左列是当前仓库开放的 PR/MR 列表，右侧是选中项的详情。
// 浏览之外支持批准、要求修改和合并。

// modalDragGitPR 是「Pull Request 列表」弹窗的拖拽 key。
const modalDragGitPR = "modal-drag-git-pr"

const (
	// gitPRListLimit 是一次读取的 PR/MR 条数上限。
	gitPRListLimit = 30
	// gitPRDialogWidth 是弹窗卡片的宽度；窗口更窄时按窗口宽度收缩。
	gitPRDialogWidth float32 = 1040
	// gitPRListPaneWidth 是左侧列表列宽度。
	gitPRListPaneWidth float32 = 280
	// gitPRConflictFilesShown 是详情里最多展开的冲突文件数，其余折叠成一行。
	gitPRConflictFilesShown = 8
)

// gitPRClient 是 PR 只读弹窗需要的后端能力，由 app.Client 实现。
// 这里用小接口加类型断言接入，不往 ui/client.go 的大接口上加方法。
type gitPRClient interface {
	// GitPRList 列出会话所在仓库开放的 PR/MR。
	GitPRList(ctx context.Context, threadID string, limit int) ([]git.PRSummary, error)
	// GitPRDetail 读取单条 PR/MR 的详情。
	GitPRDetail(ctx context.Context, threadID string, number int) (git.PRDetail, error)
}

type gitPRActionClient interface {
	GitPRMerge(ctx context.Context, threadID string, number int, method string) error
}

// gitPRConflictClient 是预演冲突文件与创建冲突解决 worktree 需要的后端能力。
type gitPRConflictClient interface {
	GitPRConflicts(ctx context.Context, threadID string, number int, base string) ([]string, error)
	GitPRResolveWorktree(ctx context.Context, threadID string, number int, head, base string) (string, error)
	GitPRCleanupWorktree(ctx context.Context, threadID string, number int) (string, error)
}

// gitPRCheckoutClient 检出 PR 分支到独立 worktree。
type gitPRCheckoutClient interface {
	GitPRCheckoutWorktree(ctx context.Context, threadID string, number int, head string, crossRepo bool) (string, error)
}

// gitPRDialogState 是 PR 弹窗的状态，按 chatState 附加（与 chatFindStates 的做法一致）。
type gitPRDialogState struct {
	mu sync.Mutex

	open     bool
	threadID string // 列表所属会话，切换会话后弹窗作废

	listLoading bool
	listErr     string
	prs         []git.PRSummary
	query       string

	selected          int // 选中项编号，0 表示还没选
	tab               string // "" 表示概览，"changes" 表示改动
	detailLoading     bool
	detailErr         string
	detail            *git.PRDetail
	actionBusy        string
	actionErr         string
	mergeConfirm      string
	mergeMethod       string
	mergeMenuOpen     bool
	moreMenuOpen      bool
	statusDetailsOpen bool
	checksOpen        bool

	conflictNumber  int // 冲突结果对应的 PR 编号，0 表示没有结果
	conflictFiles   []string
	conflictLoading bool
	conflictErr     string

	listSeq     uint64 // 递增序号，丢弃过期的异步结果
	detailSeq   uint64
	conflictSeq uint64
}

func (st *gitPRDialogState) resetActions() {
	st.actionBusy, st.actionErr, st.mergeConfirm = "", "", ""
	st.mergeMenuOpen = false
	st.moreMenuOpen = false
}

// resetConflictsLocked 作废旧冲突结果；调用方需持有 st.mu。
func (st *gitPRDialogState) resetConflictsLocked() {
	st.conflictNumber = 0
	st.conflictFiles = nil
	st.conflictLoading = false
	st.conflictErr = ""
	st.conflictSeq++
}

var (
	gitPRDialogMu     sync.Mutex
	gitPRDialogStates = make(map[*chatState]*gitPRDialogState)
)

// getGitPRDialogState 取得或初始化指定会话的 PR 弹窗状态。
func getGitPRDialogState(s *chatState) *gitPRDialogState {
	gitPRDialogMu.Lock()
	defer gitPRDialogMu.Unlock()
	st, ok := gitPRDialogStates[s]
	if !ok {
		st = &gitPRDialogState{}
		gitPRDialogStates[s] = st
	}
	return st
}

// gitPRThreadID 返回当前选中的会话 ID。
func (s *chatState) gitPRThreadID() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selectedThreadID
}

// prClient 从当前会话客户端里取出 PR 读取能力，客户端不支持时返回 false。
func (s *chatState) prClient() (gitPRClient, bool) {
	if s == nil || s.client == nil {
		return nil, false
	}
	client, ok := s.client.(gitPRClient)
	return client, ok
}

// openGitPRDialog 打开弹窗并异步读取开放 PR 列表。
func (s *chatState) openGitPRDialog() {
	if s == nil {
		return
	}
	st := getGitPRDialogState(s)
	threadID := s.gitPRThreadID()
	st.mu.Lock()
	st.open = true
	st.threadID = threadID
	st.prs = nil
	st.query = ""
	st.selected = 0
	st.tab = ""
	st.detail = nil
	st.detailErr = ""
	st.detailLoading = false
	st.mergeMethod = "squash"
	st.statusDetailsOpen = false
	st.checksOpen = false
	st.resetActions()
	st.resetConflictsLocked()
	st.listErr = ""
	st.listSeq++
	st.detailSeq++
	st.mu.Unlock()
	s.invalidate()
	s.fetchGitPRList()
}

// closeGitPRDialog 关闭弹窗，并丢弃在途请求的结果。
func (s *chatState) closeGitPRDialog() {
	if s == nil {
		return
	}
	st := getGitPRDialogState(s)
	st.mu.Lock()
	st.open = false
	st.listLoading = false
	st.detailLoading = false
	st.listErr = ""
	st.detailErr = ""
	st.query = ""
	st.selected = 0
	st.tab = ""
	st.detail = nil
	st.statusDetailsOpen = false
	st.checksOpen = false
	st.resetActions()
	st.resetConflictsLocked()
	st.listSeq++
	st.detailSeq++
	st.mu.Unlock()
	modalDragReset(modalDragGitPR)
	s.invalidate()
}

// refreshGitPRList 重新读取列表；弹窗没打开时什么也不做。
func (s *chatState) refreshGitPRList() {
	st := getGitPRDialogState(s)
	st.mu.Lock()
	open := st.open
	st.mu.Unlock()
	if !open {
		return
	}
	s.fetchGitPRList()
}

// fetchGitPRList 在后台读取开放 PR 列表；只保留最后一次请求的结果。
func (s *chatState) fetchGitPRList() {
	st := getGitPRDialogState(s)
	client, ok := s.prClient()
	if !ok {
		st.mu.Lock()
		st.listLoading = false
		st.listErr = "当前客户端不支持读取 PR 列表"
		st.mu.Unlock()
		s.invalidate()
		return
	}

	threadID := s.gitPRThreadID()
	st.mu.Lock()
	st.listLoading = true
	st.listSeq++
	seq := st.listSeq
	st.mu.Unlock()
	s.invalidate()

	go func() {
		prs, err := client.GitPRList(context.Background(), threadID, gitPRListLimit)
		st.mu.Lock()
		if st.listSeq != seq {
			// 弹窗已关闭或有更新的请求：丢弃这次结果。
			st.mu.Unlock()
			return
		}
		st.listLoading = false
		autoSelect := 0
		if err != nil {
			st.listErr = "读取 PR 列表失败: " + err.Error()
		} else {
			st.prs = prs
			st.listErr = ""
			// 刷新后选中项已不在列表里，清掉右侧详情。
			found := false
			for _, pr := range prs {
				if pr.Number == st.selected {
					found = true
					break
				}
			}
			if !found {
				st.selected = 0
				st.tab = ""
				st.detail = nil
				st.detailErr = ""
				st.detailLoading = false
				st.statusDetailsOpen = false
				st.checksOpen = false
				st.detailSeq++
				st.resetActions()
				st.resetConflictsLocked()
			}
			// 列表加载完成时没有选中项就自动选中第一条，只在这里做一次。
			if st.selected == 0 && len(prs) > 0 {
				autoSelect = prs[0].Number
			}
		}
		st.mu.Unlock()
		if autoSelect > 0 {
			s.selectGitPR(autoSelect)
			return
		}
		s.invalidate()
	}()
}

// selectGitPR 选中一条 PR 并异步读取详情。
func (s *chatState) selectGitPR(number int) {
	if s == nil || number <= 0 {
		return
	}
	st := getGitPRDialogState(s)
	st.mu.Lock()
	if !st.open {
		st.mu.Unlock()
		return
	}
	st.selected = number
	st.tab = ""
	st.detail = nil
	st.detailErr = ""
	st.detailLoading = true
	st.statusDetailsOpen = false
	st.checksOpen = false
	st.resetActions()
	st.resetConflictsLocked()
	st.detailSeq++
	seq := st.detailSeq
	st.mu.Unlock()
	s.invalidate()
	s.fetchGitPRDetail(number, seq)
}

func gitPRActionsVisible(detail git.PRDetail) bool {
	state := strings.ToLower(strings.TrimSpace(detail.State))
	return state == "open" || state == "opened" || state == "draft"
}

func gitPRMergeBlocked(detail git.PRDetail) bool {
	return strings.EqualFold(strings.TrimSpace(detail.Mergeable), "CONFLICTING")
}

func gitPRSuccessToast(kind string) string {
	switch kind {
	case "approve":
		return "已批准"
	case "comment":
		return "已提交评论"
	case "request-changes":
		return "已要求修改"
	case "merge":
		return "已合并"
	default:
		return ""
	}
}

func (s *chatState) runGitPRAction(kind, method string) {
	client, ok := s.client.(gitPRActionClient)
	if !ok {
		return
	}
	st := getGitPRDialogState(s)
	st.mu.Lock()
	if !st.open || st.selected == 0 || st.detail == nil || st.actionBusy != "" || !gitPRActionsVisible(*st.detail) || kind != "merge" || method == "" || gitPRMergeBlocked(*st.detail) {
		st.mu.Unlock()
		return
	}
	number, threadID, seq := st.selected, st.threadID, st.detailSeq
	st.actionBusy, st.actionErr = kind, ""
	st.mu.Unlock()
	s.invalidate()
	go func() {
		var cleanupCh chan struct{}
		err := client.GitPRMerge(context.Background(), threadID, number, method)
		if cleanup, ok := s.client.(gitPRConflictClient); err == nil && ok {
			cleanupCh = make(chan struct{})
			go func() {
				<-cleanupCh
				reason, err := cleanup.GitPRCleanupWorktree(context.Background(), threadID, number)
				if err != nil {
					s.setToast(err.Error())
				} else if reason != "" {
					s.setToast(fmt.Sprintf("已保留 PR #%d 的工作区：%s", number, reason))
				}
			}()
		}
		st.mu.Lock()
		if !st.open || st.detailSeq != seq || st.selected != number {
			st.mu.Unlock()
			if cleanupCh != nil {
				close(cleanupCh)
			}
			return
		}
		st.actionBusy = ""
		if err != nil {
			st.actionErr = err.Error()
			st.mu.Unlock()
			if cleanupCh != nil {
				close(cleanupCh)
			}
			s.invalidate()
			return
		}
		st.resetActions()
		st.detailLoading = true
		st.detailSeq++
		seq = st.detailSeq
		st.mu.Unlock()
		if msg := gitPRSuccessToast(kind); msg != "" {
			s.setToast(msg)
		}
		if cleanupCh != nil {
			close(cleanupCh)
		}
		s.invalidate()
		s.fetchGitPRDetail(number, seq)
		s.fetchGitPRList()
		s.loadGitState(threadID)
	}()
}

// runGitPRResolve 在独立 worktree 里解决冲突，并切到新建的会话。
func (s *chatState) runGitPRResolve() {
	client, ok := s.client.(gitPRConflictClient)
	if !ok {
		return
	}
	st := getGitPRDialogState(s)
	st.mu.Lock()
	if !st.open || st.selected == 0 || st.detail == nil || st.actionBusy != "" || !gitPRActionsVisible(*st.detail) || !gitPRMergeBlocked(*st.detail) || st.detail.CrossRepo {
		st.mu.Unlock()
		return
	}
	detail := *st.detail
	number, threadID, seq := st.selected, st.threadID, st.detailSeq
	st.actionBusy, st.actionErr = "resolve", ""
	st.mu.Unlock()
	s.invalidate()
	go func() {
		path, err := client.GitPRResolveWorktree(context.Background(), threadID, number, detail.HeadBranch, detail.BaseBranch)
		newID := ""
		if err == nil {
			prompt := fmt.Sprintf("这个 worktree 正在把 origin/%s 合并进 PR #%d 的分支 %s，出现了冲突。请逐个解决冲突文件，保持双方意图，跑相关测试，然后完成合并提交。不要推送。", detail.BaseBranch, number, detail.HeadBranch)
			newID, err = s.createGitPRThread(threadID, number, path, fmt.Sprintf("解决 PR #%d 冲突", number), prompt)
		}
		s.finishGitPRThreadAction(st, seq, number, newID, err)
	}()
}

// runGitPRCheckout 把 PR 分支检出到独立 worktree，并切到新建的会话。
func (s *chatState) runGitPRCheckout() { s.runGitPRCheckoutThread("checkout") }

// gitPRAIReviewPrompt 是审查会话的首条消息，参数依次为 PR 编号、标题与三处 base 分支。
const gitPRAIReviewPrompt = `请审查 PR #%d「%s」。这个 worktree 已检出 PR 分支，base 是 origin/%s（需要时先 git fetch origin %s），用 git diff origin/%s...HEAD 查看改动，按需阅读相关代码。审查回答三件事：1）这个 PR 是否有必要（解决什么问题，是否已有实现或重复）；2）改动有什么真实问题（缺陷、安全、数据丢失、逻辑错误，不报风格）；3）能否合并。有问题时在 comment 与 fix 间二选一：涉及设计取舍或改动较大选 comment，问题小而明确、我方能直接修选 fix。不要修改文件、不要提交、不要推送，也不要用 gh 或 glab 发表评论。最后输出一个 ` + "```" + `pr-review 代码块，内容是 JSON 对象：{"verdict":"merge|comment|fix|close","summary":"一两句话","comments":[{"path":"仓库相对路径","line":新文件行号,"body":"问题、后果和建议改法"}]}。其中 merge=有必要且没有问题可以直接合并，comment=有问题评论给作者改进，fix=有必要但问题小而明确建议合并后我们自己修，close=没有必要不建议合并；line 必须落在 diff 的改动范围内，最多 10 条，没有问题时 comments 输出 []。`

// runGitPRAIReview 检出 PR 分支，在新会话里让 agent 直接开始审查。
func (s *chatState) runGitPRAIReview() { s.runGitPRCheckoutThread("ai-review") }

// runGitPRCheckoutThread 检出 PR 分支到独立 worktree 并切到新会话；ai-review 额外发送审查指令。
func (s *chatState) runGitPRCheckoutThread(kind string) {
	client, ok := s.client.(gitPRCheckoutClient)
	if !ok {
		return
	}
	st := getGitPRDialogState(s)
	st.mu.Lock()
	if !st.open || st.selected == 0 || st.detail == nil || st.actionBusy != "" || !gitPRActionsVisible(*st.detail) {
		st.mu.Unlock()
		return
	}
	detail := *st.detail
	number, threadID, seq := st.selected, st.threadID, st.detailSeq
	st.actionBusy, st.actionErr = kind, ""
	st.mu.Unlock()
	s.invalidate()
	go func() {
		path, err := client.GitPRCheckoutWorktree(context.Background(), threadID, number, detail.HeadBranch, detail.CrossRepo)
		newID := ""
		if err == nil {
			title, prompt := fmt.Sprintf("PR #%d", number), ""
			if kind == "ai-review" {
				title = fmt.Sprintf("审查 PR #%d", number)
				prompt = fmt.Sprintf(gitPRAIReviewPrompt, number, detail.Title, detail.BaseBranch, detail.BaseBranch, detail.BaseBranch)
			}
			newID, err = s.createGitPRThread(threadID, number, path, title, prompt)
		}
		if !s.finishGitPRThreadAction(st, seq, number, newID, err) {
			return
		}
		// AI 审查会话建好后直接停在该 PR 的改动视图，回合结束时自动导入审查意见。
		if kind == "ai-review" && s.gitPRThreadID() == newID {
			openReviewPR(s, number)
			if dp := getDiffPanelState(s); dp != nil {
				diffPanelMu.Lock()
				dp.prAIAuto = true
				diffPanelMu.Unlock()
			}
		}
	}()
}

// finishGitPRThreadAction 处理 PR 新建会话后的弹窗收尾；只要 newID 非空就会保留会话并切入，错误改以 Toast 提示。
func (s *chatState) finishGitPRThreadAction(st *gitPRDialogState, seq uint64, number int, newID string, err error) bool {
	st.mu.Lock()
	if !st.open || st.detailSeq != seq || st.selected != number {
		open := st.open
		st.mu.Unlock()
		if newID != "" {
			s.notifyStaleGitPRThread(open, number, newID)
			if err != nil {
				s.setToast(err.Error())
			}
		}
		return false
	}
	st.actionBusy = ""
	if err != nil && newID == "" {
		st.actionErr = err.Error()
		st.mu.Unlock()
		s.invalidate()
		return false
	}
	st.mu.Unlock()
	if newID != "" {
		s.closeGitPRDialog()
		s.selectThread(newID)
	}
	if err != nil {
		s.setToast(err.Error())
	}
	return newID != ""
}

// notifyStaleGitPRThread 处理新建 PR 会话完成时弹窗已关闭或切到别的 PR 的情况。
func (s *chatState) notifyStaleGitPRThread(open bool, number int, newID string) {
	if newID == "" {
		return
	}
	if !open {
		s.selectThread(newID)
		return
	}
	s.setToast(fmt.Sprintf("PR #%d 的会话已创建", number))
}

// createGitPRThread 在 PR 所属项目新建普通会话，命名后切到 worktree，并按需发送首条消息。
func (s *chatState) createGitPRThread(threadID string, number int, path, title, prompt string) (string, error) {
	s.mu.Lock()
	th, ok := s.threadSnapshotLocked(threadID)
	s.mu.Unlock()
	if !ok || th.ProjectID == "" {
		return "", fmt.Errorf("找不到 PR 所属项目")
	}
	opts := s.newThreadOptions(th.ProjectID)
	opts.Provisional = false
	id, err := s.client.CreateThreadWithOptions(context.Background(), th.ProjectID, opts)
	if err != nil {
		return "", err
	}
	if err := s.client.RenameThread(id, title); err != nil {
		_ = s.client.DeleteThread(id)
		return "", err
	}
	if err := s.client.GitSwitchWorktree(context.Background(), id, path); err != nil {
		_ = s.client.DeleteThread(id)
		return "", err
	}
	if prompt != "" {
		if _, err := s.client.Send(context.Background(), id, prompt); err != nil {
			// 会话已切入 PR worktree，删除会连带清理可能复用的 PR worktree 及其本地改动。
			_ = s.refreshProjectThreads(context.Background(), th.ProjectID)
			return id, fmt.Errorf("会话已创建，但首条消息发送失败：%w", err)
		}
	}
	// 让新会话出现在侧栏；列表刷新失败不影响已经切过去的会话。
	_ = s.refreshProjectThreads(context.Background(), th.ProjectID)
	return id, nil
}

// fetchGitPRDetail 在后台读取详情；序号变化后旧结果直接丢弃。
func (s *chatState) fetchGitPRDetail(number int, seq uint64) {
	st := getGitPRDialogState(s)
	client, ok := s.prClient()
	if !ok {
		st.mu.Lock()
		if st.detailSeq == seq {
			st.detailLoading = false
			st.detailErr = "当前客户端不支持读取 PR 详情"
		}
		st.mu.Unlock()
		s.invalidate()
		return
	}

	threadID := s.gitPRThreadID()
	go func() {
		detail, err := client.GitPRDetail(context.Background(), threadID, number)
		st.mu.Lock()
		if st.detailSeq != seq {
			st.mu.Unlock()
			return
		}
		st.detailLoading = false
		needConflicts := false
		conflictBase := ""
		if err != nil {
			st.detailErr = "读取 PR 详情失败: " + err.Error()
			st.resetConflictsLocked()
		} else {
			loaded := detail
			st.detail = &loaded
			st.detailErr = ""
			st.resetConflictsLocked()
			if gitPRMergeBlocked(loaded) {
				needConflicts = true
				conflictBase = loaded.BaseBranch
			}
		}
		st.mu.Unlock()
		s.invalidate()
		if needConflicts {
			s.fetchGitPRConflicts(number, conflictBase, seq)
		}
	}()
}

// fetchGitPRConflicts 在后台预演合并冲突；编号或序号变化后旧结果直接丢弃。
func (s *chatState) fetchGitPRConflicts(number int, base string, detailSeq uint64) {
	st := getGitPRDialogState(s)
	client, ok := s.client.(gitPRConflictClient)
	if !ok {
		st.mu.Lock()
		if st.detailSeq == detailSeq && st.selected == number {
			st.conflictNumber = number
			st.conflictLoading = false
			st.conflictErr = "当前客户端不支持读取冲突文件"
		}
		st.mu.Unlock()
		s.invalidate()
		return
	}

	threadID := s.gitPRThreadID()
	st.mu.Lock()
	if st.detailSeq != detailSeq || st.selected != number {
		st.mu.Unlock()
		return
	}
	st.conflictNumber = number
	st.conflictLoading = true
	st.conflictErr = ""
	st.conflictFiles = nil
	st.conflictSeq++
	seq := st.conflictSeq
	st.mu.Unlock()
	s.invalidate()

	go func() {
		files, err := client.GitPRConflicts(context.Background(), threadID, number, base)
		st.mu.Lock()
		if st.conflictSeq != seq || st.detailSeq != detailSeq || st.selected != number || st.conflictNumber != number {
			st.mu.Unlock()
			return
		}
		st.conflictLoading = false
		if err != nil {
			st.conflictErr = err.Error()
		} else {
			st.conflictFiles = files
		}
		st.mu.Unlock()
		s.invalidate()
	}()
}

// renderGitPRDialog 渲染 PR 只读弹窗，由会话头部每帧调用。
func renderGitPRDialog(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	st := getGitPRDialogState(s)
	st.mu.Lock()
	open := st.open
	threadID := st.threadID
	listLoading := st.listLoading
	listErr := st.listErr
	prs := append([]git.PRSummary(nil), st.prs...)
	selected := st.selected
	detailLoading := st.detailLoading
	detailErr := st.detailErr
	var detail *git.PRDetail
	if st.detail != nil {
		loaded := *st.detail
		detail = &loaded
	}
	st.mu.Unlock()

	if !open {
		modalDragReset(modalDragGitPR)
		return
	}
	// 切换会话后旧列表没有意义，直接关掉。
	if threadID != "" && threadID != s.gitPRThreadID() {
		s.closeGitPRDialog()
		return
	}

	winW, winH := c.Size()
	empty := !listLoading && listErr == "" && len(prs) == 0
	cardW, cardH := modalResizeSize(modalDragGitPR, gitPRDialogWidth, max(400, winH-120), 360, 240, winW, winH)
	contentH := max(0, cardH-72)

	modalDragModal(c, modalDragGitPR, &open, func() {
		if c.Shortcut(0, ui.KeyEscape) {
			s.closeGitPRDialog()
			return
		}

		ui.Column(c.Key("modal-git-pr-card")).
			Label("PR 弹窗卡片").
			Width(cardW).
			Height(cardH).
			Background(ThemeSurface(c)).
			BorderColor(ThemeBorder(c)).
			BorderWidth(Hairline).
			Radius(RadiusLG).
			Padding(Space5).
			Gap(Space4).
			Transition(motionDialog(c)).
			Children(func() {
				// 标题行同时是弹窗的拖拽把手。
				modalDragHandle(c, modalDragGitPR, ui.Row(c.Key("modal-git-pr-title-row")).
					FillWidth().
					AlignItems(ui.Center).
					Gap(Space2).
					Children(func() {
						ui.Text(c, "Pull Request").
							FontSize(uiFont(TypeTitle)).
							FontWeight(600).
							TextColor(ThemePrimaryText(c)).
							SingleLine()
						ui.Spacer(c)
						IconButton(c, IconButtonProps{
							Key:     "modal-git-pr-refresh",
							Icon:    IconRefreshCw(),
							Label:   "刷新",
							Variant: ButtonGhost,
							Loading: listLoading,
							OnClick: func() { s.refreshGitPRList() },
						})
						IconButton(c, IconButtonProps{
							Key:     "modal-git-pr-close",
							Icon:    IconX(),
							Label:   "关闭",
							Variant: ButtonGhost,
							OnClick: func() { s.closeGitPRDialog() },
						})
					}))

				if empty {
					renderGitPREmptyState(c, s, threadID)
				} else {
					ui.Row(c.Key("modal-git-pr-body")).
						FillWidth().
						Gap(Space4).
						AlignItems(ui.Start).
						Children(func() {
							renderGitPRListPane(c, s, prs, selected, listLoading, listErr, contentH)
							ui.Column(c.Key("modal-git-pr-divider")).
								Width(Hairline).
								Height(contentH).
								Shrink(0).
								Background(ThemeHairline(c))
							renderGitPRDetailPane(c, s, detail, selected, detailLoading, detailErr, contentH)
						})
				}

				modalResizeGrips(c, modalDragGitPR, cardW, cardH)
			})
	})
	// DialogBase 已经把遮罩点击与 Esc 写回 open，这里据此关闭。
	if !open {
		s.closeGitPRDialog()
	}
}

// renderGitPREmptyState 渲染没有开放 PR 时的空状态：图标、标题与创建入口。
func renderGitPREmptyState(c *ui.Context, s *chatState, threadID string) {
	repoURL := gitPRRemoteURL(s, threadID)
	tip := ""
	if repoURL == "" {
		tip = "没有可用的远端仓库地址"
	}
	ui.Column(c.Key("modal-git-pr-empty")).FillWidth().Grow(1).Center().Children(func() {
		EmptyState(c, EmptyStateProps{
			Icon:  IconGitPullRequestCreate(),
			Title: "没有开放的 PR",
			Action: func() {
				Button(c, ButtonOpts{
					Key:      "modal-git-pr-empty-create",
					Text:     "创建 PR",
					Variant:  ButtonPrimary,
					Disabled: repoURL == "",
					Tooltip:  tip,
					OnClick: func() {
						s.closeGitPRDialog()
						openGitPRCreateDialog(s, threadID)
					},
				})
			},
		})
	})
}

// gitPRRemoteURL 返回创建 PR 可用的远端仓库地址；优先用 Git 面板缓存的状态。
func gitPRRemoteURL(s *chatState, threadID string) string {
	if s == nil || threadID == "" {
		return ""
	}
	state := s.headerGitState(threadID)
	if ps := getGitPaneState(s); ps != nil && ps.threadID == threadID {
		ps.mu.Lock()
		if ps.state.WorkDir != "" {
			state = ps.state
		}
		ps.mu.Unlock()
	}
	return normalizeGitRemoteURL(state.RemoteURL)
}

// filterGitPRs 按标题、#号、作者、分支对 PR 列表做不区分大小写的子串过滤。
func filterGitPRs(prs []git.PRSummary, query string) []git.PRSummary {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return prs
	}
	var out []git.PRSummary
	for _, pr := range prs {
		if strings.Contains(strings.ToLower(pr.Title), q) ||
			strings.Contains(fmt.Sprintf("#%d", pr.Number), q) ||
			strings.Contains(strings.ToLower(pr.Author), q) ||
			strings.Contains(strings.ToLower(pr.HeadBranch), q) ||
			strings.Contains(strings.ToLower(pr.BaseBranch), q) {
			out = append(out, pr)
		}
	}
	return out
}

// renderGitPRListPane 渲染左列：搜索与新建工具栏、加载骨架/出错提示与 PR 列表。
func renderGitPRListPane(c *ui.Context, s *chatState, prs []git.PRSummary, selected int, loading bool, errText string, contentH float32) {
	query, threadID := "", ""
	if s != nil {
		st := getGitPRDialogState(s)
		st.mu.Lock()
		query = st.query
		threadID = st.threadID
		st.mu.Unlock()
	}
	listH := max(0, contentH-ControlMD-Space2)
	if errText != "" && len(prs) > 0 {
		// 底部留出错误行的高度，避免撑出卡片。
		listH = max(0, listH-ControlSM-Space2)
	}
	ui.Column(c.Key("modal-git-pr-list-pane")).
		Width(gitPRListPaneWidth).
		Shrink(0).
		Gap(Space2).
		Children(func() {
			ui.Row(c.Key("modal-git-pr-list-toolbar")).
				FillWidth().
				Gap(Space2).
				AlignItems(ui.Center).
				Children(func() {
					SearchField(c, TextFieldProps{
						Key:         "modal-git-pr-search",
						Value:       &query,
						Placeholder: "搜索",
						Label:       "搜索",
						Grow:        true,
						Clearable:   true,
						Disabled:    s == nil,
						OnChange: func(v string) {
							if s == nil {
								return
							}
							st := getGitPRDialogState(s)
							st.mu.Lock()
							st.query = v
							st.mu.Unlock()
							s.invalidate()
						},
					})
					repoURL := gitPRRemoteURL(s, threadID)
					tip := ""
					if s != nil && repoURL == "" {
						tip = "没有可用的远端仓库地址"
					}
					IconButton(c, IconButtonProps{
						Key:      "modal-git-pr-list-create",
						Icon:     IconPlus(),
						Label:    "创建 PR",
						Variant:  ButtonGhost,
						Size:     ControlMD,
						Disabled: s == nil || repoURL == "",
						Tooltip:  tip,
						OnClick: func() {
							if s == nil {
								return
							}
							s.closeGitPRDialog()
							openGitPRCreateDialog(s, threadID)
						},
					})
				})

			switch {
			case loading && len(prs) == 0:
				ui.Column(c.Key("modal-git-pr-list-loading")).FillWidth().Label("正在读取 PR 列表…").Children(func() {
					renderSkeletonRows(c, "modal-git-pr-list-skeleton", 6, uiFont(TypeBody)+Space2, Space2)
				})
			case errText != "" && len(prs) == 0:
				renderGitPRListError(c, s, errText)
			default:
				filtered := filterGitPRs(prs, query)
				ui.Scroll(c.Key("modal-git-pr-list-scroll")).
					FillWidth().
					MaxHeight(listH).
					Children(func() {
						ui.Column(c.Key("modal-git-pr-list")).
							FillWidth().
							Gap(Space1).
							Children(func() {
								for _, pr := range filtered {
									renderGitPRListRow(c, s, pr, pr.Number == selected)
								}
							})
					})
			}

			// 列表已有内容时，错误只作为底部提示，不顶掉列表。
			if errText != "" && len(prs) > 0 {
				renderGitPRListError(c, s, errText)
			}
		})
}

// renderGitPRListError 渲染列表错误：一行灰字加「重试」。
func renderGitPRListError(c *ui.Context, s *chatState, errText string) {
	ui.Row(c.Key("modal-git-pr-list-error")).
		FillWidth().
		Gap(Space2).
		AlignItems(ui.Center).
		Children(func() {
			ui.Text(c, errText).
				FontSize(uiFont(TypeFootnote)).
				TextColor(ThemeSecondaryText(c)).
				SingleLine().
				Tooltip(errText).
				Grow(1)
			Button(c, ButtonOpts{
				Key:     "modal-git-pr-list-retry",
				Text:    "重试",
				Variant: ButtonGhost,
				Size:    ControlXS,
				OnClick: func() { s.refreshGitPRList() },
			})
		})
}

func gitPRCIIcon(c *ui.Context, checksState string) (*ui.SVG, ui.Color, string) {
	switch gitPRCheckState(checksState) {
	case "success":
		return IconCircleCheck(), ThemeSuccess(c), "检查通过"
	case "failure":
		return IconCircleX(), ThemeDanger(c), "检查失败"
	case "pending":
		return IconClock(), ThemeWarning(c), "检查进行中"
	default:
		return nil, ui.Transparent, ""
	}
}

func gitPRListSecondLine(pr git.PRSummary) string {
	parts := []string{fmt.Sprintf("#%d", pr.Number)}
	if author := strings.TrimSpace(pr.Author); author != "" {
		parts = append(parts, author)
	}
	if rel := gitPRUpdatedLabel(pr.UpdatedAt); rel != "" {
		parts = append(parts, rel)
	}
	return strings.Join(parts, " · ")
}

// renderGitPRListRow 渲染列表中的一条：第一行状态点+标题+CI图标，第二行#号·作者·相对时间+评审图标。
func renderGitPRListRow(c *ui.Context, s *chatState, pr git.PRSummary, active bool) {
	key := fmt.Sprintf("modal-git-pr-row-%d", pr.Number)
	row := ui.ButtonBase(c.Key(key)).
		Label(fmt.Sprintf("查看 PR #%d %s", pr.Number, pr.Title)).
		Padding(Space2, Space3).
		Radius(RadiusMD).
		FillWidth().
		Justify(ui.Start).
		AlignItems(ui.Start).
		OnClick(func() {
			s.selectGitPR(pr.Number)
		})
	scale := motionPressScale(c, row, "button-press-scale", row.Pressed())
	row.Scale(scale, scale)
	if active {
		row.Background(ThemeSelected(c))
	} else if row.Hovered() {
		row.Background(ThemeHover(c))
	}

	dotColor := ThemeSuccess(c)
	stateLower := strings.ToLower(strings.TrimSpace(pr.State))
	if stateLower == "merged" {
		dotColor = ThemePlan(c)
	} else if pr.IsDraft || stateLower == "closed" {
		dotColor = ThemeSecondaryText(c)
	}

	row.Children(func() {
		ui.Column(c.Key(key + "-content")).
			FillWidth().
			Gap(Space1).
			Children(func() {
				ui.Row(c.Key(key + "-line1")).
					FillWidth().
					Gap(Space2).
					AlignItems(ui.Center).
					Children(func() {
						ui.Box(c.Key(key + "-dot")).
							Size(Space2, Space2).
							Radius(RadiusPill).
							Shrink(0).
							Background(dotColor)
						ui.Text(c, pr.Title).
							FontSize(uiFont(TypeBody)).
							FontWeight(500).
							TextColor(ThemePrimaryText(c)).
							MaxLines(1).
							Ellipsis("…").
							Grow(1).
							Shrink(1).
							MinWidth(0)
						if icon, color, tip := gitPRCIIcon(c, pr.ChecksState); icon != nil {
							ui.Icon(c, icon).Size(IconSM, IconSM).TextColor(color).Tooltip(tip)
						}
					})
				ui.Row(c.Key(key + "-line2")).
					FillWidth().
					Gap(Space2).
					Padding(0, 0, 0, Space4).
					AlignItems(ui.Center).
					Children(func() {
						ui.Text(c, gitPRListSecondLine(pr)).
							FontSize(uiFont(TypeCaption)).
							TextColor(ThemeSecondaryText(c)).
							SingleLine().
							Ellipsis("…").
							Grow(1).
							Shrink(1).
							MinWidth(0)
						switch strings.ToUpper(strings.TrimSpace(pr.ReviewDecision)) {
						case "APPROVED":
							ui.Icon(c, IconUserRoundCheck()).Size(IconSM, IconSM).TextColor(ThemeSuccess(c)).Tooltip("已批准")
						case "CHANGES_REQUESTED":
							ui.Icon(c, IconPencilLine()).Size(IconSM, IconSM).TextColor(ThemeWarning(c)).Tooltip("需要修改")
						}
					})
			})
	})
}

// gitPRListMeta 拼出列表第二行：作者与分支。
func gitPRListMeta(pr git.PRSummary) string {
	var parts []string
	if author := strings.TrimSpace(pr.Author); author != "" {
		parts = append(parts, author)
	}
	head := strings.TrimSpace(pr.HeadBranch)
	base := strings.TrimSpace(pr.BaseBranch)
	switch {
	case head != "" && base != "":
		parts = append(parts, head+" → "+base)
	case head != "":
		parts = append(parts, head)
	case base != "":
		parts = append(parts, "→ "+base)
	}
	return strings.Join(parts, " · ")
}

// renderGitPRTag 渲染一个小号状态标签。
func renderGitPRTag(c *ui.Context, key, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	Badge(c, key, text, ToneNeutral)
}

// renderGitPRDetailPane 渲染右列：加载骨架/出错提示，或选中项的详情。
func renderGitPRDetailPane(c *ui.Context, s *chatState, detail *git.PRDetail, selected int, loading bool, errText string, contentH float32) {
	detailH := contentH
	if errText != "" && detail != nil {
		// 底部留出错误行的高度，避免撑出卡片。
		detailH = max(0, contentH-ControlSM-Space3)
	}
	ui.Column(c.Key("modal-git-pr-detail-pane")).
		Grow(1).
		MinWidth(0).
		Gap(Space3).
		Children(func() {
			switch {
			case selected == 0:
				// 列表加载完成后会自动选中第一条；这里只在极短的过渡帧里留空。
			case loading && detail == nil:
				ui.Column(c.Key("modal-git-pr-detail-loading")).FillWidth().Label("正在读取 PR 详情…").Children(func() {
					renderSkeletonRows(c, "modal-git-pr-detail-skeleton", 5, uiFont(TypeBody)+Space2, Space3)
				})
			case errText != "" && detail == nil:
				ui.Text(c, errText).
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemeDanger(c)).
					MaxLines(4)
			case detail == nil:
				// 没有可显示的详情：保持空白，不占用位文字。
			default:
				renderGitPRDetail(c, s, *detail, detailH)
			}

			if errText != "" && detail != nil {
				ui.Text(c, errText).
					FontSize(uiFont(TypeCaption)).
					TextColor(ThemeDanger(c)).
					SingleLine().
					Tooltip(errText)
			}
		})
}

func gitPRCheckState(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "success":
		return "success"
	case "failure":
		return "failure"
	case "pending":
		return "pending"
	case "none":
		return "none"
	default:
		return ""
	}
}

func gitPRStatusKind(detail git.PRDetail, conflictFiles []string) (kind, tone, text string) {
	state := strings.ToLower(strings.TrimSpace(detail.State))
	switch {
	case state == "merged":
		base := strings.TrimSpace(detail.BaseBranch)
		if base == "" {
			return "merged", "plan", "已合并"
		}
		return "merged", "plan", "已合并到 " + base
	case state == "closed":
		return "closed", "neutral", "已关闭"
	case detail.IsDraft:
		return "draft", "neutral", "草稿"
	case gitPRMergeBlocked(detail) || len(conflictFiles) > 0:
		if len(conflictFiles) > 0 {
			return "conflict", "danger", fmt.Sprintf("%d 个文件冲突", len(conflictFiles))
		}
		return "conflict", "danger", "有冲突"
	}

	failedCount, pendingCount := 0, 0
	for _, ch := range detail.Checks {
		switch gitPRCheckState(ch.State) {
		case "failure":
			failedCount++
		case "pending":
			pendingCount++
		}
	}
	if failedCount > 0 {
		return "checks-failure", "danger", fmt.Sprintf("%d 项检查失败", failedCount)
	}
	if pendingCount > 0 {
		return "checks-pending", "neutral", fmt.Sprintf("%d 项检查进行中", pendingCount)
	}

	switch strings.ToUpper(strings.TrimSpace(detail.ReviewDecision)) {
	case "CHANGES_REQUESTED":
		return "changes-requested", "danger", "需要修改"
	case "REVIEW_REQUIRED":
		return "review-required", "neutral", "等待评审"
	default:
		return "ready", "success", "可以合并"
	}
}

// gitPRStatusStrip 根据详情与冲突文件判定状态条语气与文案。
func gitPRStatusStrip(detail git.PRDetail, conflictFiles []string) (tone, text string) {
	_, tone, text = gitPRStatusKind(detail, conflictFiles)
	return tone, text
}

func gitPRStatusColor(c *ui.Context, tone string) ui.Color {
	switch tone {
	case "success":
		return ThemeSuccess(c)
	case "danger":
		return ThemeDanger(c)
	case "plan":
		return ThemePlan(c)
	default:
		return ThemeSecondaryText(c)
	}
}

func gitPRCurrentMergeMethod(method string) string {
	switch method {
	case "squash", "merge", "rebase":
		return method
	default:
		return "squash"
	}
}

func gitPRMergeMethodShortLabel(method string) string {
	switch gitPRCurrentMergeMethod(method) {
	case "merge":
		return "合并提交"
	case "rebase":
		return "变基合并"
	default:
		return "Squash 合并"
	}
}

// renderGitPRDetail 渲染详情：头部、状态条、标签页与概览正文/检查/评论/评审操作。
func renderGitPRDetail(c *ui.Context, s *chatState, detail git.PRDetail, contentH float32) {
	if s != nil {
		ensureChatMarkdown(c, s)
	}
	st := getGitPRDialogState(s)
	st.mu.Lock()
	tab := st.tab
	busy := st.actionBusy
	actionErr := st.actionErr
	moreOpen := st.moreMenuOpen
	mergeOpen := st.mergeMenuOpen
	mergeConfirm := st.mergeConfirm
	mergeMethod := gitPRCurrentMergeMethod(st.mergeMethod)
	statusDetailsOpen := st.statusDetailsOpen
	checksOpen := st.checksOpen
	var conflictFiles []string
	if st.conflictNumber == detail.Number {
		conflictFiles = append([]string(nil), st.conflictFiles...)
	}
	st.mu.Unlock()

	_, canCheckout := s.client.(gitPRCheckoutClient)
	_, canAction := s.client.(gitPRActionClient)
	_, canReview := s.client.(reviewPRClient)
	_, canResolve := s.client.(gitPRConflictClient)

	title := strings.TrimSpace(detail.Title)
	if title == "" {
		title = fmt.Sprintf("#%d", detail.Number)
	}

	ui.Column(c.Key("modal-git-pr-detail")).
		FillWidth().
		Height(contentH).
		Gap(Space3).
		Children(func() {
			// 头部：标题、元信息与右上操作。
			ui.Column(c.Key("modal-git-pr-detail-head")).
				FillWidth().
				Shrink(0).
				Gap(Space2).
				Children(func() {
					ui.Row(c.Key("modal-git-pr-detail-title-row")).
						FillWidth().
						Gap(Space3).
						AlignItems(ui.Start).
						Children(func() {
							ui.Text(c, title).
								FontSize(uiFont(TypePageTitle)).
								FontWeight(600).
								TextColor(ThemePrimaryText(c)).
								Wrap().
								Grow(1).
								Shrink(1).
								MinWidth(0)
							ui.Row(c.Key("modal-git-pr-head-actions")).
								Gap(Space1).
								Shrink(0).
								AlignItems(ui.Center).
								Children(func() {
									IconButton(c, IconButtonProps{
										Key:      "modal-git-pr-open-browser",
										Icon:     IconExternalLink(),
										Label:    "在浏览器打开",
										Tooltip:  "在浏览器打开",
										Variant:  ButtonGhost,
										Size:     ControlMD,
										Disabled: detail.URL == "",
										OnClick:  func() { c.OpenURL(detail.URL) },
									})
									moreTrigger := IconButton(c, IconButtonProps{
										Key:     "modal-git-pr-more",
										Icon:    IconMoreHorizontal(),
										Label:   "更多操作",
										Tooltip: "更多操作",
										Variant: ButtonGhost,
										Size:    ControlMD,
										Active:  moreOpen,
										OnClick: func() {
											st.mu.Lock()
											togglePopoverOpen(&st.moreMenuOpen)
											st.mergeMenuOpen = false
											st.mu.Unlock()
											s.invalidate()
										},
									})
									appPopover(c.Key("modal-git-pr-more-popover"), moreTrigger, &moreOpen, func() {
										st.mu.Lock()
										st.moreMenuOpen = false
										st.mu.Unlock()
										s.invalidate()
									}, func() {
										popoverCard(c, ui.Column(c.Key("modal-git-pr-more-menu")).Width(176)).Padding(Space1).Gap(Space0).Children(func() {
											checkoutDisabled := busy != "" || !canCheckout || !gitPRActionsVisible(detail)
											checkoutTip := gitPRActionTip(busy, "checkout", "")
											elCheckout := menuRow(c, menuRowProps{
												Key:      "modal-git-pr-more-checkout",
												Icon:     IconGitBranch(),
												Title:    "检出到 worktree",
												Disabled: checkoutDisabled,
												OnClick: func() {
													st.mu.Lock()
													st.moreMenuOpen = false
													st.mu.Unlock()
													s.runGitPRCheckout()
												},
											})
											if checkoutTip != "" {
												elCheckout.Tooltip(checkoutTip)
											}

											aiDisabled := busy != "" || !canCheckout || !gitPRActionsVisible(detail)
											aiTip := gitPRActionTip(busy, "ai-review", "")
											elAI := menuRow(c, menuRowProps{
												Key:      "modal-git-pr-more-ai-review",
												Icon:     IconSparkles(),
												Title:    "AI 审查",
												Disabled: aiDisabled,
												OnClick: func() {
													st.mu.Lock()
													st.moreMenuOpen = false
													st.mu.Unlock()
													s.runGitPRAIReview()
												},
											})
											if aiTip != "" {
												elAI.Tooltip(aiTip)
											}

											menuRow(c, menuRowProps{
												Key:      "modal-git-pr-more-copy",
												Icon:     IconCopy(),
												Title:    "复制链接",
												Disabled: detail.URL == "",
												OnClick: func() {
													st.mu.Lock()
													st.moreMenuOpen = false
													st.mu.Unlock()
													c.WriteClipboard(detail.URL)
													s.setToast("已复制链接")
													s.invalidate()
												},
											})
										})
									})
								})
						})

					renderGitPRDetailMetaRow(c, detail)

					renderGitPRStatusStrip(c, s, st, detail, conflictFiles, busy, mergeMethod, mergeConfirm, mergeOpen, statusDetailsOpen, canAction, canReview, canResolve)
					if actionErr != "" {
						ui.Text(c, actionErr).
							FontSize(uiFont(TypeCaption)).
							TextColor(ThemeDanger(c)).
							MaxLines(3)
					}
				})

			// Tab 栏：概览与改动。
			changesLabel := "改动"
			if detail.ChangedFiles > 0 {
				changesLabel = fmt.Sprintf("改动 %d", detail.ChangedFiles)
			}
			ui.Row(c.Key("modal-git-pr-tabs")).
				FillWidth().
				Shrink(0).
				Gap(Space3).
				Padding(0, 0, Space2, 0).
				BorderWidth(0, 0, Hairline, 0).
				BorderColor(ThemeHairline(c)).
				AlignItems(ui.Center).
				Children(func() {
					TabButton(c, TabButtonProps{
						Key:    "modal-git-pr-tab-overview",
						Label:  "概览",
						Active: tab == "",
					}, nil).OnClick(func() {
						st.mu.Lock()
						st.tab = ""
						st.mu.Unlock()
						s.invalidate()
					})
					TabButton(c, TabButtonProps{
						Key:    "modal-git-pr-tab-changes",
						Label:  changesLabel,
						Active: tab == "changes",
					}, nil).OnClick(func() {
						st.mu.Lock()
						st.tab = "changes"
						st.mu.Unlock()
						preparePRReview(s, detail.Number)
						s.invalidate()
					})
				})

			if tab == "changes" {
				dp := getDiffPanelState(s)
				tid := s.gitPRThreadID()
				changesPane := ui.Column(c.Key("modal-git-pr-changes")).
					FillWidth().
					Grow(1).
					MinHeight(0).
					Gap(Space3)
				changesPane.Draw(func(_ *ui.Painter, r ui.Rect) {
					if updateReviewPanelWidth(dp, r.W) {
						c.Invalidate()
					}
				})
				changesPane.Children(func() {
					if dp == nil {
						return
					}
					renderReviewBody(c, s, dp, changesPane, tid, reviewHost{dialog: true})
					ui.Column(c.Key("modal-git-pr-review-bar")).
						FillWidth().
						Shrink(0).
						Padding(Space3, 0, 0, 0).
						BorderWidth(Hairline, 0, 0, 0).
						BorderColor(ThemeHairline(c)).
						Children(func() {
							renderReviewPRAction(c, s, dp, tid)
						})
				})
				return
			}

			// 概览滚动正文。
			ui.Scroll(c.Key("modal-git-pr-detail-scroll")).
				FillWidth().
				Grow(1).
				MinHeight(0).
				Children(func() {
					ui.Column(c.Key("modal-git-pr-overview")).
						FillWidth().
						Gap(Space4).
						Children(func() {
							if strings.TrimSpace(detail.Body) != "" {
								markdown.View(c.Key(fmt.Sprintf("modal-git-pr-body-%d", detail.Number)), detail.Body, &s.mdStaticOpts)
							}

							if len(detail.Checks) > 0 {
								renderGitPRChecksCard(c, s, st, detail, checksOpen)
							}

							if len(detail.Comments) > 0 {
								ui.Column(c.Key("modal-git-pr-detail-comments")).
									FillWidth().
									Gap(Space3).
									Children(func() {
										for i, comment := range detail.Comments {
											renderGitPRCommentRow(c, s, fmt.Sprintf("modal-git-pr-detail-comment-%d", i), comment)
										}
									})
							}
						})
				})
		})
}

func renderGitPRDetailMetaRow(c *ui.Context, detail git.PRDetail) {
	ui.Row(c.Key("modal-git-pr-detail-meta")).
		FillWidth().
		Wrap().
		Gap(Space2).
		AlignItems(ui.Center).
		Children(func() {
			hasPrev := false
			if author := strings.TrimSpace(detail.Author); author != "" {
				ui.Text(c, author).
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemeSecondaryText(c)).
					SingleLine()
				hasPrev = true
			}
			head := strings.TrimSpace(detail.HeadBranch)
			base := strings.TrimSpace(detail.BaseBranch)
			if head != "" || base != "" {
				if hasPrev {
					ui.Text(c, "·").FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c)).SingleLine()
				}
				if head != "" {
					ui.Text(c, head).
						Font("monospace").
						FontSize(uiFont(TypeCode)).
						TextColor(ThemePrimaryText(c)).
						Background(ThemeHover(c)).
						Padding(0, Space1).
						Radius(RadiusXS).
						SingleLine()
				}
				ui.Text(c, "→").FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c)).SingleLine()
				if base != "" {
					ui.Text(c, base).
						Font("monospace").
						FontSize(uiFont(TypeCode)).
						TextColor(ThemePrimaryText(c)).
						Background(ThemeHover(c)).
						Padding(0, Space1).
						Radius(RadiusXS).
						SingleLine()
				}
				hasPrev = true
			}
			if hasPrev {
				ui.Text(c, "·").FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c)).SingleLine()
			}
			ui.Text(c, fmt.Sprintf("+%d", detail.Additions)).
				Font("monospace").
				FontSize(uiFont(TypeCode)).
				TextColor(ThemeSuccess(c)).
				SingleLine()
			ui.Text(c, fmt.Sprintf("−%d", detail.Deletions)).
				Font("monospace").
				FontSize(uiFont(TypeCode)).
				TextColor(ThemeDanger(c)).
				SingleLine()
		})
}

func renderGitPRStatusStrip(
	c *ui.Context,
	s *chatState,
	st *gitPRDialogState,
	detail git.PRDetail,
	conflictFiles []string,
	busy, mergeMethod, mergeConfirm string,
	mergeOpen, statusDetailsOpen bool,
	canAction, canReview, canResolve bool,
) {
	kind, tone, text := gitPRStatusKind(detail, conflictFiles)
	fg := gitPRStatusColor(c, tone)
	bg := fg.Alpha(0.12)
	expandable := kind == "conflict" || kind == "checks-failure"

	var statusIcon *ui.SVG
	switch kind {
	case "ready", "merged":
		statusIcon = IconCircleCheck()
	case "checks-pending":
		statusIcon = IconClock()
	case "conflict":
		statusIcon = IconAlertTriangle()
	case "checks-failure", "changes-requested", "closed":
		statusIcon = IconCircleX()
	case "review-required":
		statusIcon = IconPencilLine()
	}

	ui.Column(c.Key("modal-git-pr-status-bar")).
		FillWidth().
		Padding(Space2, Space3).
		Radius(RadiusMD).
		Background(bg).
		Transition(motionFeedback(c)).
		Children(func() {
			ui.Row(c.Key("modal-git-pr-status-main")).
				FillWidth().
				MinHeight(ControlMD).
				Gap(Space3).
				Justify(ui.SpaceBetween).
				AlignItems(ui.Center).
				Children(func() {
					renderLabel := func() {
						if statusIcon != nil {
							ui.Icon(c, statusIcon).Size(IconMD, IconMD).TextColor(fg)
						} else {
							ui.Box(c.Key("modal-git-pr-status-dot")).Size(Space3, Space3).Radius(RadiusPill).Shrink(0).Background(fg)
						}
						ui.Text(c, text).
							FontSize(uiFont(TypeBody)).
							FontWeight(500).
							TextColor(fg).
							SingleLine()
						if expandable {
							chev := IconChevronRight()
							if statusDetailsOpen {
								chev = IconChevronDown()
							}
							ui.Icon(c, chev).Size(IconSM, IconSM).TextColor(fg)
						}
					}

					if expandable {
						ui.ButtonBase(c.Key("modal-git-pr-status-toggle")).
							Label(text).
							Padding(Space0, Space1).
							Radius(RadiusXS).
							Gap(Space2).
							AlignItems(ui.Center).
							OnClick(func() {
								st.mu.Lock()
								st.statusDetailsOpen = !st.statusDetailsOpen
								st.mu.Unlock()
								s.invalidate()
							}).
							Children(renderLabel)
					} else {
						ui.Row(c.Key("modal-git-pr-status-msg")).
							Gap(Space2).
							AlignItems(ui.Center).
							Children(renderLabel)
					}

					if gitPRActionsVisible(detail) && !detail.IsDraft {
						ui.Row(c.Key("modal-git-pr-status-actions")).
							Gap(Space2).
							Shrink(0).
							AlignItems(ui.Center).
							Children(func() {
								switch kind {
								case "ready", "checks-pending", "checks-failure", "changes-requested":
									if !canAction {
										return
									}
									if mergeConfirm != "" {
										Button(c, ButtonOpts{
											Key:      "modal-git-pr-merge-confirm",
											Text:     "确认合并",
											Variant:  ButtonPrimary,
											Size:     ControlSM,
											Loading:  busy == "merge",
											Disabled: busy != "" && busy != "merge",
											Tooltip:  gitPRActionTip(busy, "merge", ""),
											OnClick:  func() { s.runGitPRAction("merge", mergeConfirm) },
										})
										Button(c, ButtonOpts{
											Key:      "modal-git-pr-merge-cancel",
											Text:     "取消",
											Variant:  ButtonSecondary,
											Size:     ControlSM,
											Disabled: busy != "",
											Tooltip:  gitPRActionTip(busy, "", ""),
											OnClick: func() {
												st.mu.Lock()
												st.mergeConfirm = ""
												st.mu.Unlock()
												s.invalidate()
											},
										})
										return
									}
									mergeVariant := ButtonSecondary
									if kind == "ready" {
										mergeVariant = ButtonPrimary
									}
									pendingChecks := kind == "checks-pending"
									pendingTip := ""
									if pendingChecks {
										pendingTip = "检查进行中"
									}
									ui.Row(c.Key("modal-git-pr-merge-split")).AlignItems(ui.Center).Gap(1).Children(func() {
										Button(c, ButtonOpts{
											Key:      "modal-git-pr-merge",
											Text:     gitPRMergeMethodShortLabel(mergeMethod),
											Variant:  mergeVariant,
											Size:     ControlSM,
											Disabled: busy != "" || pendingChecks,
											Tooltip:  gitPRActionTip(busy, "", pendingTip),
											OnClick: func() {
												st.mu.Lock()
												st.mergeMenuOpen = false
												st.mergeConfirm = mergeMethod
												st.mu.Unlock()
												s.invalidate()
											},
										})
										caret := IconButton(c, IconButtonProps{
											Key:      "modal-git-pr-merge-caret",
											Icon:     IconChevronDown(),
											Label:    "选择合并方式",
											Tooltip:  "选择合并方式",
											Variant:  mergeVariant,
											Size:     ControlSM,
											Active:   mergeOpen,
											Disabled: busy != "",
											OnClick: func() {
												st.mu.Lock()
												togglePopoverOpen(&st.mergeMenuOpen)
												st.moreMenuOpen = false
												st.mu.Unlock()
												s.invalidate()
											},
										})
										appPopover(c.Key("modal-git-pr-merge-popover"), caret, &mergeOpen, func() {
											st.mu.Lock()
											st.mergeMenuOpen = false
											st.mu.Unlock()
											s.invalidate()
										}, func() {
											popoverCard(c, ui.Column(c.Key("modal-git-pr-merge-menu")).Width(156)).Padding(Space1).Gap(Space0).Children(func() {
												for _, item := range []struct{ label, method string }{{"Squash 合并", "squash"}, {"合并提交", "merge"}, {"变基合并", "rebase"}} {
													methodVal := item.method
													menuRow(c, menuRowProps{
														Key:     "modal-git-pr-merge-" + methodVal,
														Title:   item.label,
														Checked: mergeMethod == methodVal,
														OnClick: func() {
															st.mu.Lock()
															st.mergeMethod = methodVal
															st.mergeMenuOpen = false
															st.mu.Unlock()
															s.invalidate()
														},
													})
												}
											})
										})
									})
								case "review-required":
									if !canReview {
										return
									}
									Button(c, ButtonOpts{
										Key:      "modal-git-pr-start-review",
										Text:     "开始评审",
										Variant:  ButtonPrimary,
										Size:     ControlSM,
										Disabled: busy != "",
										Tooltip:  gitPRActionTip(busy, "", ""),
										OnClick: func() {
											st.mu.Lock()
											st.tab = "changes"
											st.mu.Unlock()
											preparePRReview(s, detail.Number)
											s.invalidate()
										},
									})
								case "conflict":
									if !canResolve {
										return
									}
									resolveTip := ""
									if detail.CrossRepo {
										resolveTip = "来自 fork 的 PR 无法推回，请在网页上解决"
									}
									Button(c, ButtonOpts{
										Key:      "modal-git-pr-resolve",
										Text:     "解决冲突",
										Variant:  ButtonPrimary,
										Size:     ControlSM,
										Loading:  busy == "resolve",
										Disabled: (busy != "" && busy != "resolve") || detail.CrossRepo,
										Tooltip:  gitPRActionTip(busy, "resolve", resolveTip),
										OnClick:  func() { s.runGitPRResolve() },
									})
								}
							})
					}
				})

			if expandable {
				motionUnfoldBody(c, "modal-git-pr-status-unfold", statusDetailsOpen, func() {
					ui.Column(c.Key("modal-git-pr-status-expand")).FillWidth().Padding(Space2, 0, 0, 0).Gap(Space1).Children(func() {
						if kind == "conflict" {
							renderGitPRConflicts(c, s, detail)
							return
						}
						for i, check := range detail.Checks {
							if gitPRCheckState(check.State) == "failure" {
								renderGitPRCheckRow(c, fmt.Sprintf("modal-git-pr-status-failed-check-%d", i), check)
							}
						}
					})
				})
			}
		})
}

func renderGitPRChecksCard(c *ui.Context, s *chatState, st *gitPRDialogState, detail git.PRDetail, checksOpen bool) {
	passed := 0
	for _, check := range detail.Checks {
		if gitPRCheckState(check.State) == "success" {
			passed++
		}
	}
	chev := IconChevronRight()
	if checksOpen {
		chev = IconChevronDown()
	}
	summaryText := fmt.Sprintf("检查 %d/%d", passed, len(detail.Checks))
	ui.Column(c.Key("modal-git-pr-checks-card")).
		FillWidth().
		Radius(RadiusMD).
		Border(Hairline, ThemeHairline(c)).
		Children(func() {
			ui.ButtonBase(c.Key("modal-git-pr-checks-toggle")).
				Label(summaryText).
				FillWidth().
				Padding(Space3, Space4).
				Justify(ui.SpaceBetween).
				AlignItems(ui.Center).
				OnClick(func() {
					st.mu.Lock()
					st.checksOpen = !st.checksOpen
					st.mu.Unlock()
					s.invalidate()
				}).
				Children(func() {
					ui.Row(c.Key("modal-git-pr-checks-summary-left")).Gap(Space2).AlignItems(ui.Center).Children(func() {
						if icon, color, _ := gitPRCIIcon(c, detail.ChecksState); icon != nil {
							ui.Icon(c, icon).Size(IconSM, IconSM).TextColor(color)
						}
						ui.Text(c, summaryText).
							FontSize(uiFont(TypeBody)).
							FontWeight(500).
							TextColor(ThemePrimaryText(c)).
							SingleLine()
					})
					ui.Icon(c, chev).Size(IconSM, IconSM).TextColor(ThemeTertiaryText(c))
				})

			motionUnfoldBody(c, "modal-git-pr-checks-unfold", checksOpen, func() {
				ui.Column(c.Key("modal-git-pr-detail-checks")).
					FillWidth().
					Padding(Space2, Space3, Space3, Space3).
					Gap(Space0).
					BorderWidth(Hairline, 0, 0, 0).
					BorderColor(ThemeHairline(c)).
					Children(func() {
						for i, check := range detail.Checks {
							renderGitPRCheckRow(c, fmt.Sprintf("modal-git-pr-detail-check-%d", i), check)
						}
					})
			})
		})
}

func preparePRReview(s *chatState, number int) {
	if s == nil {
		return
	}
	if _, ok := s.client.(reviewPRClient); !ok {
		return
	}
	dp := getDiffPanelState(s)
	if dp == nil {
		return
	}
	tid := s.gitPRThreadID()
	diffPanelMu.Lock()
	changed := dp.scope != "pr" || dp.prNumber != number
	dp.scope, dp.prNumber = "pr", number
	dp.selectedPath, dp.pendingPath = "", ""
	if changed {
		dp.snapshot = ReviewSnapshot{}
		dp.prReviewBody = ""
		dp.prReviewErr = ""
		dp.prApproveConfirm = 0
	}
	diffPanelMu.Unlock()
	s.loadDiff(tid, "")
}

func openReviewPR(s *chatState, number int) {
	if s == nil {
		return
	}
	if _, ok := s.client.(reviewPRClient); !ok {
		return
	}
	s.closeGitPRDialog()
	inspectorSelect(s, sidePaneDiffID, false)
	inspectorFollowPause(s, s.gitPRThreadID())
	preparePRReview(s, number)
}

func gitPRActionTip(busy, self, reason string) string {
	if busy != "" && busy != self {
		return "正在执行其他操作，请稍候"
	}
	return reason
}

func gitPRMergeMethodLabel(method string) string {
	switch method {
	case "squash":
		return "Squash 合并（squash）"
	case "merge":
		return "合并提交（merge）"
	case "rebase":
		return "变基合并（rebase）"
	default:
		return method
	}
}

// renderGitPRConflicts 渲染冲突文件数量与路径列表。
func renderGitPRConflicts(c *ui.Context, s *chatState, detail git.PRDetail) {
	st := getGitPRDialogState(s)
	st.mu.Lock()
	number := st.conflictNumber
	loading := st.conflictLoading
	errText := st.conflictErr
	files := append([]string(nil), st.conflictFiles...)
	st.mu.Unlock()
	if number != detail.Number {
		loading = true
	}
	ui.Column(c.Key("modal-git-pr-detail-conflicts")).
		FillWidth().
		Gap(Space1).
		Children(func() {
			switch {
			case loading:
				LoadingIndicator(c, nil, "", IconSM)
			case errText != "":
				ui.Text(c, errText).
					FontSize(uiFont(TypeCaption)).
					TextColor(ThemeDanger(c)).
					MaxLines(3)
			default:
				ui.Text(c, fmt.Sprintf("%d 个文件冲突", len(files))).
					FontSize(uiFont(TypeCaption)).
					FontWeight(600).
					TextColor(ThemeSecondaryText(c)).
					SingleLine()
				shown := files
				if len(shown) > gitPRConflictFilesShown {
					shown = shown[:gitPRConflictFilesShown]
				}
				for _, path := range shown {
					ui.Text(c, path).
						Font("monospace").
						FontSize(uiFont(TypeCaption)).
						TextColor(ThemePrimaryText(c)).
						SingleLine().
						Tooltip(path)
				}
				if rest := len(files) - len(shown); rest > 0 {
					ui.Text(c, fmt.Sprintf("还有 %d 个", rest)).
						FontSize(uiFont(TypeCaption)).
						TextColor(ThemeSecondaryText(c)).
						SingleLine()
				}
			}
		})
}

// renderGitPRSectionTitle 渲染小节标题。
func renderGitPRSectionTitle(c *ui.Context, text string) {
	ui.Text(c, text).
		FontSize(uiFont(TypeCaption)).
		FontWeight(600).
		TextColor(ThemeSecondaryText(c)).
		SingleLine()
}

// gitPRCheckURLSafe 判断 CI 检查链接是否允许在浏览器中打开。
func gitPRCheckURLSafe(raw string) (string, bool) {
	url := strings.TrimSpace(raw)
	if url == "" || !ui.SafeToOpen(url) {
		return "", false
	}
	return url, true
}

// renderGitPRCheckRow 渲染一条 CI 检查：左侧名称，右侧状态文案；有安全链接时整行可点。
func renderGitPRCheckRow(c *ui.Context, key string, check git.PRCheck) {
	name := strings.TrimSpace(check.Name)
	if name == "" {
		name = "未命名检查"
	}
	row := ui.Row(c.Key(key)).
		FillWidth().
		Padding(Space0, Space2).
		Radius(RadiusXS).
		Background(ThemeHover(c).Alpha(0.4)).
		Gap(Space3).
		AlignItems(ui.Center)
	if url, ok := gitPRCheckURLSafe(check.URL); ok {
		row.Label("打开检查 " + name)
		if row.Hovered() {
			row.Background(ThemeHover(c))
		}
		if row.Clicked() {
			c.OpenURL(url)
		}
	}
	row.Children(func() {
		ui.Text(c, name).
			FontSize(uiFont(TypeFootnote)).
			TextColor(ThemePrimaryText(c)).
			Grow(1).
			SingleLine()
		ui.Text(c, gitPRChecksLabel(check.State)).
			FontSize(uiFont(TypeCaption)).
			TextColor(ThemeSecondaryText(c)).
			SingleLine()
	})
}

// renderGitPRCommentRow 渲染一条评论：时间线头像、作者、相对时间与 Markdown 正文。
func renderGitPRCommentRow(c *ui.Context, s *chatState, key string, comment git.PRComment) {
	author := strings.TrimSpace(comment.Author)
	if author == "" {
		author = "匿名"
	}
	ui.Row(c.Key(key)).
		FillWidth().
		Gap(Space3).
		AlignItems(ui.Start).
		Children(func() {
			Avatar(c, AvatarProps{
				Key:        key + "-avatar",
				Name:       author,
				Size:       ControlSM,
				FontSize:   uiFont(TypeCaption),
				Foreground: ThemeSecondaryText(c),
				Background: ThemeSelected(c),
			})
			ui.Column(c.Key(key + "-content")).
				Grow(1).
				MinWidth(0).
				Gap(Space1).
				Children(func() {
					ui.Row(c.Key(key + "-head")).
						FillWidth().
						Gap(Space2).
						AlignItems(ui.Center).
						Children(func() {
							ui.Text(c, author).
								FontSize(uiFont(TypeFootnote)).
								FontWeight(600).
								TextColor(ThemePrimaryText(c)).
								SingleLine()
							if rel := gitPRUpdatedLabel(comment.CreatedAt); rel != "" {
								ui.Text(c, rel).
									FontSize(uiFont(TypeCaption)).
									TextColor(ThemeTertiaryText(c)).
									SingleLine()
							}
						})
					if s != nil {
						markdown.View(c.Key(key+"-body"), comment.Body, &s.mdStaticOpts)
					} else {
						ui.Text(c, comment.Body).
							FontSize(uiFont(TypeBody)).
							TextColor(ThemePrimaryText(c))
					}
				})
		})
}

// gitPRStateLabel 返回状态标签文案：草稿优先，其次按归一化状态显示中文。
func gitPRStateLabel(state string, draft bool) string {
	if draft {
		return "草稿"
	}
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "open":
		return "开放"
	case "closed":
		return "已关闭"
	case "merged":
		return "已合并"
	case "locked":
		return "已锁定"
	case "":
		return "未知状态"
	}
	return strings.TrimSpace(state)
}

// gitPRChecksLabel 返回 CI 汇总状态的文案。
func gitPRChecksLabel(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "success":
		return "检查通过"
	case "failure":
		return "检查失败"
	case "pending":
		return "检查进行中"
	case "none":
		return "无检查"
	}
	return "检查未知"
}

// gitPRReviewLabel 返回评审结论的文案。
func gitPRReviewLabel(decision string) string {
	switch strings.ToUpper(strings.TrimSpace(decision)) {
	case "APPROVED":
		return "评审通过"
	case "CHANGES_REQUESTED":
		return "评审要求修改"
	case "REVIEW_REQUIRED":
		return "等待评审"
	case "":
		return "暂无评审"
	}
	return "评审未定"
}

// gitPRChangeStats 拼出改动统计：「+120 · -8 · 5 个文件」。
func gitPRChangeStats(additions, deletions, changedFiles int) string {
	parts := []string{fmt.Sprintf("+%d", additions), fmt.Sprintf("-%d", deletions)}
	if changedFiles > 0 {
		parts = append(parts, fmt.Sprintf("%d 个文件", changedFiles))
	}
	return strings.Join(parts, " · ")
}

// gitPRUpdatedLabel 把时间格式化成中文短格式；零值返回空字符串。
func gitPRUpdatedLabel(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return formatRelativeTime(t.Local())
}

// gitPRMetaLine 拼出详情里的元信息行：作者、分支、更新时间。
func gitPRMetaLine(summary git.PRSummary) string {
	var parts []string
	if author := strings.TrimSpace(summary.Author); author != "" {
		parts = append(parts, "作者 "+author)
	}
	head := strings.TrimSpace(summary.HeadBranch)
	base := strings.TrimSpace(summary.BaseBranch)
	switch {
	case head != "" && base != "":
		parts = append(parts, head+" → "+base)
	case head != "":
		parts = append(parts, head)
	case base != "":
		parts = append(parts, "→ "+base)
	}
	if rel := gitPRUpdatedLabel(summary.UpdatedAt); rel != "" {
		parts = append(parts, "更新于 "+rel)
	}
	return strings.Join(parts, " · ")
}
