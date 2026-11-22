package mcodeui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
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
	gitPRDialogWidth float32 = 860
	// gitPRListPaneWidth 是左侧列表列宽度。
	gitPRListPaneWidth float32 = 250
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
	GitPRReview(ctx context.Context, threadID string, number int, event, body string) error
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

	selected      int // 选中项编号，0 表示还没选
	detailLoading bool
	detailErr     string
	detail        *git.PRDetail
	reviewBody    string
	actionBusy     string
	actionErr      string
	approveConfirm bool
	mergeConfirm   string
	mergeMenuOpen  bool

	conflictNumber  int // 冲突结果对应的 PR 编号，0 表示没有结果
	conflictFiles   []string
	conflictLoading bool
	conflictErr     string

	listSeq     uint64 // 递增序号，丢弃过期的异步结果
	detailSeq   uint64
	conflictSeq uint64
}

func (st *gitPRDialogState) resetActions() {
	st.reviewBody, st.actionBusy, st.actionErr, st.mergeConfirm = "", "", "", ""
	st.approveConfirm = false
	st.mergeMenuOpen = false
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
	st.selected = 0
	st.detail = nil
	st.detailErr = ""
	st.detailLoading = false
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
	st.selected = 0
	st.detail = nil
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
				st.detail = nil
				st.detailErr = ""
				st.detailLoading = false
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
	st.detail = nil
	st.detailErr = ""
	st.detailLoading = true
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
	if !st.open || st.selected == 0 || st.detail == nil || st.actionBusy != "" || !gitPRActionsVisible(*st.detail) || ((kind == "request-changes" || kind == "comment") && strings.TrimSpace(st.reviewBody) == "") || (kind == "merge" && (method == "" || gitPRMergeBlocked(*st.detail))) || (kind != "approve" && kind != "comment" && kind != "request-changes" && kind != "merge") {
		st.mu.Unlock()
		return
	}
	number, threadID, body, seq := st.selected, st.threadID, st.reviewBody, st.detailSeq
	st.actionBusy, st.actionErr = kind, ""
	st.mu.Unlock()
	s.invalidate()
	go func() {
		var err error
		var cleanupCh chan struct{}
		if kind == "merge" {
			err = client.GitPRMerge(context.Background(), threadID, number, method)
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
		} else {
			err = client.GitPRReview(context.Background(), threadID, number, kind, body)
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
const gitPRAIReviewPrompt = `请审查 PR #%d「%s」。这个 worktree 已检出 PR 分支，base 是 origin/%s（需要时先 git fetch origin %s），用 git diff origin/%s...HEAD 查看改动，按需阅读相关代码。只报告真实的缺陷、安全问题、数据丢失风险和明显的逻辑错误，不报风格问题。不要修改文件、不要提交、不要推送，也不要用 gh 或 glab 发表评论。最后输出一个 ` + "```" + `pr-review 代码块，内容是 JSON 数组，每项 {"path":"仓库相对路径","line":新文件行号,"body":"问题和后果"}，line 必须落在 diff 的改动范围内，最多 10 条；没有问题时输出 []。`

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
	// 没有开放的 PR 时收成一张紧凑的空状态卡片；有 PR 时保留用户调整过的尺寸。
	empty := !listLoading && listErr == "" && len(prs) == 0
	var cardW, cardH float32
	if empty {
		cardW = min(gitPRDialogWidth, winW-modalDragPanelInset)
		cardH = max(200, min(260, winH-modalDragPanelInset))
	} else {
		cardW, cardH = modalResizeSize(modalDragGitPR, gitPRDialogWidth, max(400, winH-120), 360, 240, winW, winH)
	}
	contentH := max(0, cardH-72)

	modalDragModal(c, modalDragGitPR, &open, func() {
		if c.Shortcut(0, ui.KeyEscape) {
			s.closeGitPRDialog()
			return
		}

		ui.Column(c.Key("modal-git-pr-card")).
			Width(cardW).
			Height(cardH).
			Background(ThemeSurface(c)).
			BorderColor(ThemeBorder(c)).
			BorderWidth(Hairline).
			Radius(RadiusLG).
			Padding(Space5).
			Gap(Space4).
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
					return
				}

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
	ui.Column(c.Key("modal-git-pr-empty")).Fill().Center().Children(func() {
		EmptyState(c, EmptyStateProps{
			Icon:  IconGitPullRequestCreate(),
			Title: "没有开放的 PR",
			Action: func() {
				Button(c, ButtonOpts{
					Key:      "modal-git-pr-empty-create",
					Text:     "创建 PR…",
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

// renderGitPRListPane 渲染左列：加载/出错提示与 PR 列表。
func renderGitPRListPane(c *ui.Context, s *chatState, prs []git.PRSummary, selected int, loading bool, errText string, contentH float32) {
	listH := contentH
	if errText != "" && len(prs) > 0 {
		// 底部留出错误行的高度，避免撑出卡片。
		listH = max(0, contentH-ControlSM-Space2)
	}
	ui.Column(c.Key("modal-git-pr-list-pane")).
		Width(gitPRListPaneWidth).
		Shrink(0).
		Gap(Space2).
		Children(func() {
			switch {
			case loading && len(prs) == 0:
				ui.Row(c.Key("modal-git-pr-list-loading")).
					FillWidth().
					Justify(ui.Start).
					Children(func() {
						LoadingIndicator(c, nil, "正在读取 PR 列表…", IconSM)
					})
			case errText != "" && len(prs) == 0:
				renderGitPRListError(c, s, errText)
			default:
				ui.Scroll(c.Key("modal-git-pr-list-scroll")).
					FillWidth().
					MaxHeight(listH).
					Children(func() {
						ui.Column(c.Key("modal-git-pr-list")).
							FillWidth().
							Gap(Space1).
							Children(func() {
								for _, pr := range prs {
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

// renderGitPRListRow 渲染列表中的一条：#编号与标题在上，作者与分支在下。
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
	row.Children(func() {
		ui.Column(c.Key(key + "-text")).
			FillWidth().
			Gap(Space0).
			Children(func() {
				ui.Text(c, fmt.Sprintf("#%d %s", pr.Number, pr.Title)).
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemePrimaryText(c)).
					SingleLine().
					FillWidth()
				if meta := gitPRListMeta(pr); meta != "" {
					ui.Text(c, meta).
						FontSize(uiFont(TypeCaption)).
						TextColor(ThemeSecondaryText(c)).
						SingleLine().
						FillWidth()
				}
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

// renderGitPRDetailPane 渲染右列：加载/出错提示，或选中项的详情。
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
				ui.Row(c.Key("modal-git-pr-detail-loading")).
					Gap(Space2).
					AlignItems(ui.Center).
					Children(func() {
						LoadingIndicator(c, nil, "正在读取 PR 详情…", IconSM)
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

// renderGitPRDetail 渲染详情：标题、元信息、状态标签、改动统计、检查、正文与评论。
func renderGitPRDetail(c *ui.Context, s *chatState, detail git.PRDetail, contentH float32) {
	ui.Scroll(c.Key("modal-git-pr-detail-scroll")).
		FillWidth().
		MaxHeight(contentH).
		Children(func() {
			ui.Column(c.Key("modal-git-pr-detail")).
				FillWidth().
				Gap(Space3).
				Children(func() {
					ui.Row(c.Key("modal-git-pr-detail-title-row")).
						FillWidth().
						Gap(Space2).
						AlignItems(ui.Start).
						Children(func() {
							ui.Text(c, fmt.Sprintf("#%d %s", detail.Number, detail.Title)).
								FontSize(uiFont(TypeProse)).
								FontWeight(600).
								TextColor(ThemePrimaryText(c)).
								Grow(1)
							if detail.URL != "" {
								IconButton(c, IconButtonProps{
									Key:     "modal-git-pr-open-browser",
									Icon:    IconExternalLink(),
									Label:   "在浏览器打开",
									Variant: ButtonGhost,
									Size:    ControlSM,
									OnClick: func() { c.OpenURL(detail.URL) },
								})
							}
						})

					if meta := gitPRMetaLine(detail.PRSummary); meta != "" {
						ui.Text(c, meta).
							FontSize(uiFont(TypeCaption)).
							TextColor(ThemeSecondaryText(c))
					}

					ui.Row(c.Key("modal-git-pr-detail-tags")).
						FillWidth().
						Gap(Space1).
						AlignItems(ui.Center).
						Children(func() {
							renderGitPRTag(c, "modal-git-pr-detail-tag-state", gitPRStateLabel(detail.State, detail.IsDraft))
							renderGitPRTag(c, "modal-git-pr-detail-tag-review", gitPRReviewLabel(detail.ReviewDecision))
							renderGitPRTag(c, "modal-git-pr-detail-tag-checks", gitPRChecksLabel(detail.ChecksState))
							if gitPRMergeBlocked(detail) {
								renderGitPRTag(c, "modal-git-pr-detail-tag-conflict", "冲突")
							}
							stats := gitPRChangeStats(detail.Additions, detail.Deletions, detail.ChangedFiles)
							if _, ok := s.client.(reviewPRClient); ok && detail.ChangedFiles > 0 {
								Button(c, ButtonOpts{Key: "modal-git-pr-open-diff", Text: stats, Variant: ButtonGhost, Size: ControlXS, OnClick: func() { openReviewPR(s, detail.Number) }})
							} else {
								ui.Text(c, stats).FontSize(uiFont(TypeCaption)).TextColor(ThemeSecondaryText(c)).SingleLine()
							}
						})

					if gitPRMergeBlocked(detail) {
						renderGitPRConflicts(c, s, detail)
					}

					if len(detail.Checks) > 0 {
						renderGitPRSectionTitle(c, "CI 检查")
						ui.Column(c.Key("modal-git-pr-detail-checks")).
							FillWidth().
							Gap(Space0).
							Children(func() {
								for i, check := range detail.Checks {
									renderGitPRCheckRow(c, fmt.Sprintf("modal-git-pr-detail-check-%d", i), check)
								}
							})
					}

					renderGitPRSectionTitle(c, "正文")
					if detail.Body == "" {
						ui.Text(c, "没有填写正文").
							FontSize(uiFont(TypeFootnote)).
							TextColor(ThemeSecondaryText(c))
					} else {
						ui.Text(c, detail.Body).
							FontSize(uiFont(TypeFootnote)).
							TextColor(ThemePrimaryText(c))
					}

					renderGitPRSectionTitle(c, fmt.Sprintf("评论（%d）", len(detail.Comments)))
					if len(detail.Comments) == 0 {
						ui.Text(c, "还没有评论").
							FontSize(uiFont(TypeFootnote)).
							TextColor(ThemeSecondaryText(c))
					} else {
						ui.Column(c.Key("modal-git-pr-detail-comments")).
							FillWidth().
							Gap(Space2).
							Children(func() {
								for i, comment := range detail.Comments {
									renderGitPRCommentRow(c, fmt.Sprintf("modal-git-pr-detail-comment-%d", i), comment)
								}
							})
					}
					if _, ok := s.client.(gitPRActionClient); ok && gitPRActionsVisible(detail) {
						renderGitPRActions(c, s, detail)
					}
				})
		})
}

func openReviewPR(s *chatState, number int) {
	if _, ok := s.client.(reviewPRClient); !ok {
		return
	}
	s.closeGitPRDialog()
	inspectorSelect(s, sidePaneDiffID, false)
	dp := getDiffPanelState(s)
	if dp == nil {
		return
	}
	tid := s.gitPRThreadID()
	inspectorFollowPause(s, tid)
	diffPanelMu.Lock()
	dp.scope, dp.prNumber = "pr", number
	dp.selectedPath, dp.pendingPath = "", ""
	diffPanelMu.Unlock()
	s.loadDiff(tid, "")
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

func renderGitPRActions(c *ui.Context, s *chatState, detail git.PRDetail) {
	st := getGitPRDialogState(s)
	st.mu.Lock()
	body, busy, errText, approveConfirm, confirm, menuOpen := st.reviewBody, st.actionBusy, st.actionErr, st.approveConfirm, st.mergeConfirm, st.mergeMenuOpen
	st.mu.Unlock()
	area := ui.TextArea(c.Key("modal-git-pr-review-body"), &body).Placeholder("评论").Lines(4, 10).FontSize(uiFont(TypeBody)).TextColor(ThemePrimaryText(c)).Background(ThemeHover(c)).Border(Hairline, ThemeBorder(c)).Radius(RadiusSM).Padding(Space2, Space3).FillWidth()
	area.OnChange(func() { st.mu.Lock(); st.reviewBody = body; st.mu.Unlock() })
	_, canCheckout := s.client.(gitPRCheckoutClient)
	commentEmpty := strings.TrimSpace(body) == ""
	commentTip := ""
	if commentEmpty {
		commentTip = "先在上方输入评论内容"
	}
	ui.Row(c.Key("modal-git-pr-review-actions")).FillWidth().Gap(Space2).AlignItems(ui.Center).Children(func() {
		if confirm != "" {
			ui.Text(c, fmt.Sprintf("使用 %s 合并 #%d？", gitPRMergeMethodLabel(confirm), detail.Number)).FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
			ui.Spacer(c)
			Button(c, ButtonOpts{Key: "modal-git-pr-merge-confirm", Text: "确认", Variant: ButtonPrimary, Loading: busy == "merge", Disabled: busy != "" && busy != "merge", Tooltip: gitPRActionTip(busy, "merge", ""), OnClick: func() { s.runGitPRAction("merge", confirm) }})
			Button(c, ButtonOpts{Key: "modal-git-pr-merge-cancel", Text: "取消", Variant: ButtonSecondary, Disabled: busy != "", Tooltip: gitPRActionTip(busy, "", ""), OnClick: func() { st.mu.Lock(); st.mergeConfirm = ""; st.mu.Unlock(); s.invalidate() }})
			return
		}
		if approveConfirm {
			ui.Text(c, fmt.Sprintf("批准 #%d？", detail.Number)).FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
			ui.Spacer(c)
			Button(c, ButtonOpts{Key: "modal-git-pr-approve-confirm", Text: "确认批准", Variant: ButtonPrimary, Loading: busy == "approve", Disabled: busy != "" && busy != "approve", Tooltip: gitPRActionTip(busy, "approve", ""), OnClick: func() { s.runGitPRAction("approve", "") }})
			Button(c, ButtonOpts{Key: "modal-git-pr-approve-cancel", Text: "取消", Variant: ButtonSecondary, Disabled: busy != "", Tooltip: gitPRActionTip(busy, "", ""), OnClick: func() { st.mu.Lock(); st.approveConfirm = false; st.mu.Unlock(); s.invalidate() }})
			return
		}
		if canCheckout {
			Button(c, ButtonOpts{Key: "modal-git-pr-ai-review", Text: "AI 审查", Variant: ButtonSecondary, Loading: busy == "ai-review", Disabled: busy != "" && busy != "ai-review", Tooltip: gitPRActionTip(busy, "ai-review", ""), OnClick: func() { s.runGitPRAIReview() }})
		}
		Button(c, ButtonOpts{Key: "modal-git-pr-approve", Text: "批准", Variant: ButtonSecondary, Loading: busy == "approve", Disabled: busy != "" && busy != "approve", Tooltip: gitPRActionTip(busy, "approve", ""), OnClick: func() { st.mu.Lock(); st.approveConfirm = true; st.mu.Unlock(); s.invalidate() }})
		Button(c, ButtonOpts{Key: "modal-git-pr-comment", Text: "评论", Variant: ButtonSecondary, Loading: busy == "comment", Disabled: (busy != "" && busy != "comment") || commentEmpty, Tooltip: gitPRActionTip(busy, "comment", commentTip), OnClick: func() { s.runGitPRAction("comment", "") }})
		Button(c, ButtonOpts{Key: "modal-git-pr-request-changes", Text: "要求修改", Variant: ButtonSecondary, Loading: busy == "request-changes", Disabled: (busy != "" && busy != "request-changes") || commentEmpty, Tooltip: gitPRActionTip(busy, "request-changes", commentTip), OnClick: func() { s.runGitPRAction("request-changes", "") }})
		if canCheckout {
			Button(c, ButtonOpts{Key: "modal-git-pr-checkout", Text: "检出", Variant: ButtonSecondary, Loading: busy == "checkout", Disabled: busy != "" && busy != "checkout", Tooltip: gitPRActionTip(busy, "checkout", ""), OnClick: func() { s.runGitPRCheckout() }})
		}
		ui.Spacer(c)
		if gitPRMergeBlocked(detail) {
			resolveTip := ""
			if detail.CrossRepo {
				resolveTip = "来自 fork 的 PR 无法推回，请在网页上解决"
			}
			Button(c, ButtonOpts{Key: "modal-git-pr-resolve", Text: "解决冲突", Variant: ButtonPrimary, Loading: busy == "resolve", Disabled: (busy != "" && busy != "resolve") || detail.CrossRepo, Tooltip: gitPRActionTip(busy, "resolve", resolveTip), OnClick: func() { s.runGitPRResolve() }})
		} else {
			trigger := Button(c, ButtonOpts{Key: "modal-git-pr-merge", Text: "合并 ▾", Variant: ButtonPrimary, Disabled: busy != "", Tooltip: gitPRActionTip(busy, "", ""), OnClick: func() { st.mu.Lock(); st.mergeMenuOpen = true; st.mu.Unlock(); s.invalidate() }})
			appPopover(c.Key("modal-git-pr-merge-popover"), trigger, &menuOpen, func() { st.mu.Lock(); st.mergeMenuOpen = false; st.mu.Unlock(); s.invalidate() }, func() {
				popoverCard(c, ui.Column(c.Key("modal-git-pr-merge-menu"))).Padding(Space1).Gap(Space0).Children(func() {
					for _, item := range []struct{ label, method string }{{"Squash 合并", "squash"}, {"合并提交", "merge"}, {"变基合并", "rebase"}} {
						Button(c, ButtonOpts{Key: "modal-git-pr-merge-" + item.method, Text: item.label, Variant: ButtonGhost, OnClick: func() {
							st.mu.Lock()
							st.mergeMenuOpen = false
							st.mergeConfirm = item.method
							st.mu.Unlock()
							s.invalidate()
						}})
					}
				})
			})
		}
	})
	if errText != "" {
		ui.Text(c, errText).FontSize(uiFont(TypeCaption)).TextColor(ThemeDanger(c)).MaxLines(3)
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

// renderGitPRCommentRow 渲染一条评论：作者与时间在上，正文在下。
func renderGitPRCommentRow(c *ui.Context, key string, comment git.PRComment) {
	ui.Column(c.Key(key)).
		FillWidth().
		Padding(Space2, Space3).
		Radius(RadiusSM).
		Background(ThemeHover(c).Alpha(0.4)).
		Gap(Space0).
		Children(func() {
			author := strings.TrimSpace(comment.Author)
			if author == "" {
				author = "匿名"
			}
			ui.Row(c.Key(key + "-head")).
				FillWidth().
				Gap(Space2).
				AlignItems(ui.Center).
				Children(func() {
					ui.Text(c, author).
						FontSize(uiFont(TypeCaption)).
						FontWeight(600).
						TextColor(ThemePrimaryText(c)).
						SingleLine()
					if rel := gitPRUpdatedLabel(comment.CreatedAt); rel != "" {
						ui.Text(c, rel).
							FontSize(uiFont(TypeCaption)).
							TextColor(ThemeSecondaryText(c)).
							SingleLine()
					}
				})
			ui.Text(c, comment.Body).
				FontSize(uiFont(TypeFootnote)).
				TextColor(ThemePrimaryText(c))
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
