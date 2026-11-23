package mcodeui

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

type gitPRBranchClient interface {
	GitPRForBranch(context.Context, string) (*git.PRSummary, error)
}

var (
	chatHeaderMu     sync.Mutex
	chatHeaderStates = make(map[*chatState]*chatHeaderState)
)

// chatHeaderState 维护会话头部的标题编辑与 Git 胶囊状态，按 chatState 附加（同 diffPanelState）。
// editTitle、titleMenuOpen、branchMenuOpen、commitMenuOpen、openInMenuOpen、
// scriptsMenuOpen、auxMenuOpen、moreSubmenu 绑定在控件上，只在 UI 线程读写；
// 后台 goroutine 通过 closeMenus 请求下一帧关闭弹层，其余字段受 chatHeaderMu 保护。
// 提交、新建分支、切换分支与创建合并请求的输入表单在独立窗口里完成（见 git_form_windows_ui.go）。
type chatHeaderState struct {
	// 标题内联编辑
	editing      bool
	editThreadID string
	editOrig     string
	editTitle    string
	editHadFocus bool
	renameBusy   bool

	// Git 状态缓存（按 threadID），出错或不是 Git 仓库时没有条目
	gitThreadID   string
	gitGeneration uint64
	git           map[string]GitState
	gitSeq        map[string]uint64
	pr            map[string]*git.PRSummary

	titleMenuOpen  bool
	branchMenuOpen bool
	commitMenuOpen bool
	closeMenus     bool

	// 标题栏项目动作
	openInMenuOpen  bool
	scriptsMenuOpen bool
	auxMenuOpen     bool
	moreSubmenu     string

	// 脚本缓存（按 threadID），出错或没有脚本时没有条目
	scripts    map[string][]Script
	scriptsSeq map[string]uint64
	// 脚本拉取失败后的退避：连续失败次数与下次允许自动重试的时间。
	// 拉取失败时列表不缓存，渲染路径每帧都会补拉，没有退避就会变成按帧重试的自激循环。
	scriptsFailures map[string]int
	scriptsRetryAt  map[string]time.Time

	// 人设缓存（按 provider|model）：空列表也算已加载，避免渲染路径每帧重复拉取；
	// 失败不缓存，与脚本一样按退避控制自动重试。
	personas         map[string][]MagpiePersona
	personasLoaded   map[string]bool
	personasLoading  map[string]bool
	personasFailures map[string]int
	personasRetryAt  map[string]time.Time

	pushBusy    bool // 「推送并建 PR/MR」进行中
	pushConfirm bool // 「推送并建 PR/MR」二次确认区是否展开

	branchBusy bool
	branchErr  string
	commitBusy string // 进行中的操作："推送"、"抓取" 等，空表示空闲
	commitErr  string

	wtMoving   bool // 「迁入 Worktree…」表单是否展开
	wtBranch   string
	wtTransfer bool
}

func getChatHeaderState(s *chatState) *chatHeaderState {
	chatHeaderMu.Lock()
	defer chatHeaderMu.Unlock()
	st, ok := chatHeaderStates[s]
	if !ok {
		st = &chatHeaderState{
			git:              make(map[string]GitState),
			gitSeq:           make(map[string]uint64),
			pr:               make(map[string]*git.PRSummary),
			scripts:          make(map[string][]Script),
			scriptsSeq:       make(map[string]uint64),
			scriptsFailures:  make(map[string]int),
			scriptsRetryAt:   make(map[string]time.Time),
			personas:         make(map[string][]MagpiePersona),
			personasLoaded:   make(map[string]bool),
			personasLoading:  make(map[string]bool),
			personasFailures: make(map[string]int),
			personasRetryAt:  make(map[string]time.Time),
		}
		chatHeaderStates[s] = st
	}
	return st
}

// releaseChatHeaderState 在窗口关闭时丢弃该会话的头部状态，避免包级 map 只增不删。
func releaseChatHeaderState(s *chatState) {
	chatHeaderMu.Lock()
	defer chatHeaderMu.Unlock()
	delete(chatHeaderStates, s)
}

// renderChatHeaderTitle 渲染可拖动的会话标题；右键重命名后，回车或失焦提交，Esc 取消。
func renderChatHeaderTitle(c *ui.Context, s *chatState, th Thread) {
	hs := getChatHeaderState(s)

	chatHeaderMu.Lock()
	editing := hs.editing && hs.editThreadID == th.ID
	switched := hs.editing && !editing
	chatHeaderMu.Unlock()
	if switched {
		s.commitHeaderTitle(hs)
	}

	if editing {
		in := ui.TextInput(c.Key("chat-header-title-input"), &hs.editTitle).
			Label("会话标题").
			Placeholder("输入会话标题...").
			FontSize(uiFont(TypeTitle)).
			TextColor(ThemePrimaryText(c)).
			Background(ThemeHover(c).Alpha(0.25)).
			BorderColor(ThemeAccent(c)).
			BorderWidth(1).
			Radius(RadiusSM).
			Padding(Space0, Space3).
			Grow(1).
			Shrink(1).
			MinWidth(0).
			AutoFocus()

		in.OnTextKey(func(k ui.TextKey) bool {
			if k.Key == ui.KeyEnter && k.Mods == 0 {
				hs.editTitle = k.Text()
				s.commitHeaderTitle(hs)
				return true
			}
			if k.Key == ui.KeyEscape {
				s.cancelHeaderTitle(hs)
				return true
			}
			return false
		})

		// 失焦提交：获得过焦点后又失去才算失焦，避免 AutoFocus 生效前的首帧误提交
		chatHeaderMu.Lock()
		stillEditing := hs.editing
		focused := in.Focused()
		blurred := stillEditing && hs.editHadFocus && !focused
		if focused {
			if !hs.editHadFocus {
				in.SetTextSelection(0, len([]rune(hs.editTitle)))
			}
			hs.editHadFocus = true
		}
		chatHeaderMu.Unlock()
		if blurred {
			s.commitHeaderTitle(hs)
		}
		return
	}

	title := strings.TrimSpace(th.Title)
	textColor := ThemePrimaryText(c)
	if title == "" {
		title = "未命名会话"
		textColor = ThemeSecondaryText(c)
	}

	startEditing := func() {
		chatHeaderMu.Lock()
		if hs.renameBusy {
			chatHeaderMu.Unlock()
			return
		}
		hs.editing = true
		hs.editThreadID = th.ID
		hs.editOrig = th.Title
		hs.editTitle = strings.TrimSpace(th.Title)
		hs.editHadFocus = false
		chatHeaderMu.Unlock()
		s.invalidate()
	}
	s.mu.Lock()
	hasMessages := len(s.messages) > 0 && s.selectedThreadID == th.ID
	forwarding := s.forwardInflight[th.ID]
	s.mu.Unlock()

	titleArea := ui.Row(c.Key("chat-header-title-btn")).
		Label("会话标题拖动区").
		Grow(1).
		Shrink(1).
		MinWidth(0).
		ClipX().
		Justify(ui.Start).
		Padding(Space0, Space2).
		DragWindow()
	titleArea.ContextMenu(func(m *ui.Menu) {
		if m.Item("复制名称").Chosen() {
			c.WriteClipboard(th.Title)
			s.setToast("已复制会话名称")
		}
		if m.Item("在侧栏定位").Chosen() {
			s.mu.Lock()
			collapsed := s.sidebarCollapsed
			s.mu.Unlock()
			if collapsed {
				toggleSidebarPersisted(s)
			}
			s.selectThread(th.ID)
		}
		if m.Item("重命名").Chosen() {
			startEditing()
		}
		if th.ID != "" && !stewardSelected(s, th.ID) && hasMessages {
			m.Separator()
			if m.Item("转发到新会话").
				Trailing(shortcutKeysDisplay("forward-thread", "⇧⌘D")).
				Disabled(forwarding).
				Chosen() {
				s.forwardThread(th.ProjectID, th.ID)
			}
		}
	})
	titleArea.Children(func() {
		ui.Text(c, title).
			TextColor(textColor).
			FontSize(uiFont(TypeTitle)).
			FontWeight(600).
			Shrink(1).
			MinWidth(0).
			SingleLine().
			Ellipsis("…")
	})
}

// headerParentThread 返回当前会话的父会话（Swarm lead）：Swarm 面板状态已为当前选中会话
// 加载完成、lead 非空且不等于选中会话时返回 true，并从本地会话列表取 lead 对应的 Thread；
// 列表中没有该会话时用标题「主会话」兜底。面板未加载或 lead 就是当前会话时返回 false。
func (s *chatState) headerParentThread() (Thread, bool) {
	lead := s.swarmTargetLeadID()
	if lead == "" {
		return Thread{}, false
	}
	s.mu.Lock()
	selected := s.selectedThreadID
	var found Thread
	ok := false
	for _, th := range s.threads {
		if th.ID == lead {
			found = th
			ok = true
			break
		}
	}
	s.mu.Unlock()
	if lead == selected {
		return Thread{}, false
	}
	if !ok {
		found = Thread{ID: lead, Title: "主会话"}
	}
	return found, true
}

// renderChatHeaderParentChip 渲染 Swarm bee 返回父会话（lead）的胶囊芯片，放在标题栏行首。
func renderChatHeaderParentChip(c *ui.Context, s *chatState) {
	parent, ok := s.headerParentThread()
	if !ok {
		return
	}
	chip := ui.ButtonBase(c.Key("chat-header-parent-chip")).
		Label("返回主会话").
		Tooltip("返回主会话").
		Gap(Space1).
		Padding(Space1, Space3).
		Radius(RadiusXL).
		MaxWidth(180).
		Background(ThemeAccent(c).Alpha(0.12))
	{
		scale := motionPressScale(c, chip, "button-press-scale", chip.Pressed())
		chip.Scale(scale, scale)
	}
	if chip.Hovered() {
		chip.Background(ThemeAccent(c).Alpha(0.18))
	}
	focusRing(c, chip)
	if chip.Clicked() {
		s.selectThread(parent.ID)
	}
	chip.Children(func() {
		ui.Icon(c, IconArrowLeft()).Size(10, 10).TextColor(ThemeAccent(c))
		ui.Text(c, truncateParentThreadTitle(parent.Title)).
			FontSize(uiFont(TypeCaption)).
			FontWeight(500).
			TextColor(ThemePrimaryText(c)).
			SingleLine()
	})
}

// truncateParentThreadTitle 规范父会话芯片的标题：空标题兜底为「主会话」，超过 16 个字符截断并加省略号。
func truncateParentThreadTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "主会话"
	}
	runes := []rune(title)
	if len(runes) > 16 {
		return string(runes[:16]) + "…"
	}
	return title
}

// cancelHeaderTitle 放弃标题编辑。
func (s *chatState) cancelHeaderTitle(hs *chatHeaderState) {
	chatHeaderMu.Lock()
	hs.editing = false
	chatHeaderMu.Unlock()
	s.invalidate()
}

// commitHeaderTitle 提交标题编辑：去掉首尾空白后非空且有变化才重命名，先乐观更新，失败时恢复原标题。
func (s *chatState) commitHeaderTitle(hs *chatHeaderState) {
	chatHeaderMu.Lock()
	if !hs.editing {
		chatHeaderMu.Unlock()
		return
	}
	hs.editing = false
	id := hs.editThreadID
	orig := hs.editOrig
	title := strings.TrimSpace(hs.editTitle)
	chatHeaderMu.Unlock()

	if id == "" || title == "" || title == strings.TrimSpace(orig) {
		s.invalidate()
		return
	}
	chatHeaderMu.Lock()
	hs.renameBusy = true
	chatHeaderMu.Unlock()
	s.setThreadTitle(id, title)
	s.invalidate()

	go func() {
		if err := s.client.RenameThread(id, title); err != nil {
			s.setThreadTitle(id, orig)
			s.setToast("重命名失败: " + err.Error())
		}
		chatHeaderMu.Lock()
		hs.renameBusy = false
		chatHeaderMu.Unlock()
		s.invalidate()
	}()
}

// setThreadTitle 更新本地会话列表中指定会话的标题。
func (s *chatState) setThreadTitle(id, title string) {
	s.mu.Lock()
	for _, list := range s.threadsByProj {
		for i := range list {
			if list[i].ID == id {
				list[i].Title = title
			}
		}
	}
	for i := range s.threads {
		if s.threads[i].ID == id {
			s.threads[i].Title = title
		}
	}
	s.mu.Unlock()
	s.invalidate()
}

// loadGitState 在后台拉取会话的 Git 状态并按 threadID 缓存；出错时移除缓存，不渲染胶囊。
func (s *chatState) loadGitState(threadID string) {
	if s == nil || s.client == nil || threadID == "" {
		return
	}
	hs := getChatHeaderState(s)
	chatHeaderMu.Lock()
	hs.gitSeq[threadID]++
	seq := hs.gitSeq[threadID]
	chatHeaderMu.Unlock()

	go func() {
		st, err := s.client.GitState(context.Background(), threadID)
		chatHeaderMu.Lock()
		if hs.gitSeq[threadID] != seq {
			chatHeaderMu.Unlock()
			return
		}
		if err != nil {
			delete(hs.git, threadID)
			delete(hs.pr, threadID)
			chatHeaderMu.Unlock()
			s.invalidate()
			return
		}
		prev, hadPrev := hs.git[threadID]
		branch := strings.TrimSpace(st.Branch)
		remote := strings.TrimSpace(st.RemoteURL)
		hs.git[threadID] = st
		if !hadPrev || strings.TrimSpace(prev.Branch) != branch || st.Detached || branch == "" || remote == "" {
			delete(hs.pr, threadID)
		}
		prClient, hasPRClient := s.client.(gitPRBranchClient)
		if !hasPRClient {
			delete(hs.pr, threadID)
		}
		chatHeaderMu.Unlock()
		s.invalidate()

		if !hasPRClient || st.Detached || branch == "" || remote == "" {
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		pr, prErr := prClient.GitPRForBranch(ctx, threadID)
		chatHeaderMu.Lock()
		cur, ok := hs.git[threadID]
		if hs.gitSeq[threadID] != seq || !ok || cur.Detached || strings.TrimSpace(cur.Branch) != branch || strings.TrimSpace(cur.RemoteURL) == "" {
			chatHeaderMu.Unlock()
			return
		}
		if prErr != nil {
			delete(hs.pr, threadID)
		} else {
			hs.pr[threadID] = pr
		}
		chatHeaderMu.Unlock()
		s.invalidate()
	}()
}

// headerScriptsRetryBase / headerScriptsRetryMax 是脚本列表拉取失败后的退避区间：
// 首次失败等 2 秒，之后每次翻倍，最长 60 秒。
const (
	headerScriptsRetryBase = 2 * time.Second
	headerScriptsRetryMax  = 60 * time.Second
)

// headerScriptsRetryDelay 返回第 failures 次连续失败后到下次自动重试的等待时间（指数退避，封顶 60 秒）。
func headerScriptsRetryDelay(failures int) time.Duration {
	delay := headerScriptsRetryBase
	for i := 1; i < failures && delay < headerScriptsRetryMax; i++ {
		delay *= 2
	}
	if delay > headerScriptsRetryMax {
		delay = headerScriptsRetryMax
	}
	return delay
}

// loadHeaderScripts 在后台拉取会话的脚本列表并按 threadID 缓存；出错时移除缓存，不渲染脚本菜单。
// 失败后按指数退避（2s 起、最长 60s）暂时不再自动重试：失败时没有缓存条目，渲染路径每帧都会补拉，
// 若无退避，Scripts() 持续失败就会形成按帧重试的自激循环。
func (s *chatState) loadHeaderScripts(threadID string) {
	if threadID == "" {
		return
	}
	hs := getChatHeaderState(s)
	chatHeaderMu.Lock()
	if retryAt, ok := hs.scriptsRetryAt[threadID]; ok && time.Now().Before(retryAt) {
		chatHeaderMu.Unlock()
		return
	}
	hs.scriptsSeq[threadID]++
	seq := hs.scriptsSeq[threadID]
	chatHeaderMu.Unlock()

	go func() {
		scripts, err := s.client.Scripts(context.Background(), threadID)
		chatHeaderMu.Lock()
		if hs.scriptsSeq[threadID] != seq {
			chatHeaderMu.Unlock()
			return
		}
		if err != nil {
			delete(hs.scripts, threadID)
			hs.scriptsFailures[threadID]++
			hs.scriptsRetryAt[threadID] = time.Now().Add(headerScriptsRetryDelay(hs.scriptsFailures[threadID]))
		} else {
			hs.scripts[threadID] = scripts
			delete(hs.scriptsFailures, threadID)
			delete(hs.scriptsRetryAt, threadID)
		}
		chatHeaderMu.Unlock()
		s.invalidate()
	}()
}

// headerPersonaKey 把人设缓存的键拼起来：人设列表跟着 provider 与模型走。
func headerPersonaKey(provider, model string) string {
	return provider + "|" + model
}

// loadHeaderPersonas 在后台拉取指定 provider/model 的 magpie 人设并按 provider|model 缓存。
// 空列表也算已加载：否则渲染路径每帧都会补拉；失败不缓存，按跟脚本列表相同的指数退避偶尔重试。
// force 为真时忽略已加载与退避，用于点击打开权限菜单时的强制刷新；同一 key 正在加载时不重复发起。
func (s *chatState) loadHeaderPersonas(provider, model string, force bool) {
	if s.client == nil {
		return
	}
	key := headerPersonaKey(provider, model)
	hs := getChatHeaderState(s)
	chatHeaderMu.Lock()
	if hs.personasLoading[key] {
		chatHeaderMu.Unlock()
		return
	}
	if !force {
		if hs.personasLoaded[key] {
			chatHeaderMu.Unlock()
			return
		}
		if retryAt, ok := hs.personasRetryAt[key]; ok && time.Now().Before(retryAt) {
			chatHeaderMu.Unlock()
			return
		}
	}
	hs.personasLoading[key] = true
	chatHeaderMu.Unlock()

	go func() {
		list, err := s.client.MagpiePersonas(context.Background(), provider, model)
		chatHeaderMu.Lock()
		delete(hs.personasLoading, key)
		if err != nil {
			hs.personasFailures[key]++
			hs.personasRetryAt[key] = time.Now().Add(headerScriptsRetryDelay(hs.personasFailures[key]))
		} else {
			hs.personas[key] = list
			hs.personasLoaded[key] = true
			delete(hs.personasFailures, key)
			delete(hs.personasRetryAt, key)
		}
		chatHeaderMu.Unlock()
		s.invalidate()
	}()
}

// headerPersonas 返回指定 provider/model 已缓存的人设列表；尚未加载或拉取失败时返回空列表。
func (s *chatState) headerPersonas(provider, model string) []MagpiePersona {
	hs := getChatHeaderState(s)
	chatHeaderMu.Lock()
	defer chatHeaderMu.Unlock()
	return hs.personas[headerPersonaKey(provider, model)]
}

// gitBranchLabel 返回胶囊文字：当前分支（detached 时为 HEAD），不在主工作树时附上 worktree 目录名。
func gitBranchLabel(st GitState) string {
	label := st.Branch
	if st.Detached || label == "" {
		label = st.Head
	}
	for i, wt := range st.Worktrees {
		if wt.Current && i > 0 {
			label += " · " + filepath.Base(wt.Path)
			break
		}
	}
	return label
}

// syncChatHeaderGit 检查 PR 独立窗口所属会话并按会话变更拉取 Git 状态与头部脚本；
// 若当前为有效 Git 仓库且已加载状态，返回缓存状态以供渲染分支胶囊。
func syncChatHeaderGit(c *ui.Context, s *chatState, threadID string) (*chatHeaderState, GitState, bool) {
	if threadID == "" {
		return nil, GitState{}, false
	}
	// PR 独立窗口打开时若主窗口切换了会话，PR 窗口此时不一定重绘；
	// 在这里只加锁检查一次状态，会话变化时直接关掉 PR 窗口（不做 IO）。
	if s != nil {
		st := getGitPRDialogState(s)
		st.mu.Lock()
		prOpen := st.open
		prThreadID := st.threadID
		st.mu.Unlock()
		if prOpen && prThreadID != "" && prThreadID != s.gitPRThreadID() {
			s.closeGitPRDialog()
		}
	}
	hs := getChatHeaderState(s)

	chatHeaderMu.Lock()
	needLoad := hs.gitThreadID != threadID
	if needLoad {
		hs.gitThreadID = threadID
		hs.gitGeneration++
		hs.titleMenuOpen = false
		hs.branchMenuOpen = false
		hs.commitMenuOpen = false
		hs.branchBusy = false
		hs.commitBusy = ""
		hs.closeMenus = false
		hs.branchErr = ""
		hs.commitErr = ""
		hs.wtMoving = false
		hs.wtBranch = ""
		hs.openInMenuOpen = false
		hs.scriptsMenuOpen = false
		hs.auxMenuOpen = false
		hs.moreSubmenu = ""
		hs.pushConfirm = false
	}
	if hs.closeMenus {
		hs.closeMenus = false
		hs.titleMenuOpen = false
		hs.branchMenuOpen = false
		hs.commitMenuOpen = false
		hs.openInMenuOpen = false
		hs.scriptsMenuOpen = false
		hs.auxMenuOpen = false
		hs.moreSubmenu = ""
		hs.wtMoving = false
		hs.wtBranch = ""
		hs.pushConfirm = false
	}
	st, ok := hs.git[threadID]
	chatHeaderMu.Unlock()

	if needLoad {
		s.loadGitState(threadID)
		s.loadHeaderScripts(threadID)
	}
	// 分支与 HEAD 都为空视为不是 Git 仓库：只加载状态，不渲染分支胶囊
	if !ok || (st.Branch == "" && st.Head == "") {
		return hs, st, false
	}
	return hs, st, true
}

// renderChatHeaderGit 加载 Git 状态并渲染分支/Worktree 胶囊；提交按钮在右侧动作胶囊组里。
func renderChatHeaderGit(c *ui.Context, s *chatState, threadID string) {
	hs, st, ok := syncChatHeaderGit(c, s, threadID)
	if !ok {
		return
	}
	renderGitBranchCapsule(c, s, hs, threadID, st)
	renderGitPRHeaderChip(c, s, hs, threadID, st)
}

// headerGitState 返回指定会话缓存的 Git 状态；尚未加载时返回零值。
func (s *chatState) headerGitState(threadID string) GitState {
	hs := getChatHeaderState(s)
	chatHeaderMu.Lock()
	defer chatHeaderMu.Unlock()
	return hs.git[threadID]
}

// headerGitPR 返回指定会话缓存的当前分支 PR；loaded 为 false 表示尚未加载或加载出错。
func (s *chatState) headerGitPR(threadID string) (*git.PRSummary, bool) {
	hs := getChatHeaderState(s)
	chatHeaderMu.Lock()
	defer chatHeaderMu.Unlock()
	pr, ok := hs.pr[threadID]
	return pr, ok
}

// headerProjectDir 返回指定会话的项目工作目录：优先用 Git 状态里的 WorkDir（Git 会话切换
// worktree 后以它为准），为空时回退到会话的实际工作目录（非 Git 项目只有后者可用）。
func (s *chatState) headerProjectDir(threadID string) string {
	if threadID == "" {
		return ""
	}
	if dir := strings.TrimSpace(s.headerGitState(threadID).WorkDir); dir != "" {
		return dir
	}
	if s.client == nil {
		return ""
	}
	dir, ok := s.client.ThreadWorkDir(threadID)
	if !ok {
		return ""
	}
	return strings.TrimSpace(dir)
}

// headerScriptsButtonVisible 决定执行命令按钮是否显示：只要有项目工作目录就显示
// （无脚本时菜单内给出空态与「添加脚本…」入口），没有工作目录时隐藏。
func (s *chatState) headerScriptsButtonVisible(threadID string) bool {
	return threadID != "" && s.headerProjectDir(threadID) != ""
}

// renderChatHeaderActionGroup 渲染标题栏右侧的动作按钮组（「打开方式」「执行命令」）。
func renderChatHeaderActionGroup(c *ui.Context, s *chatState, threadID string) {
	hasOpenIn := s.headerProjectDir(threadID) != ""
	hasScripts := s.headerScriptsButtonVisible(threadID)
	if !hasOpenIn && !hasScripts {
		return
	}

	ui.Row(c.Key("chat-header-actions-group")).
		Gap(Space1).
		AlignItems(ui.Center).
		Children(func() {
			if hasOpenIn {
				renderHeaderOpenInButton(c, s, threadID)
			}
			if hasScripts {
				renderHeaderScriptsMenuButton(c, s, threadID)
			}
		})
}

func visibleHeaderWorktree(branch string) bool {
	return !strings.HasPrefix(branch, "mcode/swarm/")
}

// renderGitBranchCapsule 渲染分支胶囊及分支/Worktree 菜单：切换与新建分支在独立窗口里完成
// （见 git_form_windows_ui.go），Worktree 切换仍在菜单内直接执行。
func renderGitBranchCapsule(c *ui.Context, s *chatState, hs *chatHeaderState, threadID string, st GitState) {
	// 会话在自建 worktree 里干活时，改用该 worktree 的分支名，Tooltip 带上完整路径。
	label := gitBranchLabel(st)
	aiWT := s.aiWorktree(threadID)
	if aiWT.Path != "" {
		name := aiWT.Branch
		if name == "" {
			name = filepath.Base(aiWT.Path)
		}
		label = name
	}

	trigger := ui.ButtonBase(c.Key("chat-header-git-branch-btn")).
		Label("Git 分支").
		Height(ControlMD).
		Padding(0, Space2).
		Radius(RadiusMD).
		Gap(Space2).
		AlignItems(ui.Center).
		MaxWidth(150).
		MinWidth(0).
		Shrink(1).
		Transition(motionFeedback(c)).
		OnClick(func() {
			opened := togglePopoverOpen(&hs.branchMenuOpen)
			hs.titleMenuOpen = false
			hs.commitMenuOpen = false
			chatHeaderMu.Lock()
			hs.branchErr = ""
			chatHeaderMu.Unlock()
			if opened {
				s.loadGitState(threadID)
				openBranchPopover(s, threadID)
			}
		})
	scale := motionPressScale(c, trigger, "button-press-scale", trigger.Pressed())
	trigger.Scale(scale, scale)
	chatHeaderMu.Lock()
	switching := hs.branchBusy
	chatHeaderMu.Unlock()
	fg := ThemeSecondaryText(c)
	if hs.branchMenuOpen {
		trigger.Background(ThemeOverlaySelected(c))
		fg = ThemePrimaryText(c)
	} else if trigger.Hovered() {
		trigger.Background(ThemeHover(c))
		fg = ThemePrimaryText(c)
	}
	focusRing(c, trigger)
	trigger.Children(func() {
		if switching {
			LoadingIndicator(c, nil, "", IconSM)
		} else {
			ui.Icon(c, IconGitBranch()).Size(IconMD, IconMD).TextColor(fg)
		}
		ui.Text(c, label).FontSize(uiFont(TypeFootnote)).FontWeight(500).TextColor(fg).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
		if st.Ahead > 0 {
			ui.Text(c, fmt.Sprintf("↑%d", st.Ahead)).FontSize(uiFont(TypeCaption)).TextColor(ThemeSuccess(c)).Shrink(0)
		}
		if st.Behind > 0 {
			ui.Text(c, fmt.Sprintf("↓%d", st.Behind)).FontSize(uiFont(TypeCaption)).TextColor(ThemeWarning(c)).Shrink(0)
		}
	})
	if aiWT.Path != "" {
		trigger = trigger.Tooltip(label + "\n" + aiWT.Path)
	} else {
		trigger = trigger.Tooltip(label)
	}

	chatHeaderMu.Lock()
	busy := switching || hs.commitBusy != ""
	errMsg := hs.branchErr
	chatHeaderMu.Unlock()
	s.mu.Lock()
	for _, th := range s.threads {
		if th.ID == threadID && (th.IsRunning || th.SwarmBusy) {
			busy = true
			break
		}
	}
	s.mu.Unlock()

	// run 在后台执行切换操作，成功后关闭弹层并刷新 Git 状态
	run := func(op func() error) {
		chatHeaderMu.Lock()
		if hs.branchBusy || hs.commitBusy != "" || busy {
			chatHeaderMu.Unlock()
			return
		}
		hs.branchBusy = true
		hs.branchErr = ""
		generation := hs.gitGeneration
		chatHeaderMu.Unlock()
		s.invalidate()

		go func() {
			err := op()
			chatHeaderMu.Lock()
			if hs.gitGeneration == generation {
				hs.branchBusy = false
				if err != nil {
					hs.branchErr = err.Error()
				} else {
					hs.closeMenus = true
				}
			}
			chatHeaderMu.Unlock()
			s.loadGitState(threadID)
			s.invalidate()
		}()
	}

	appPopover(c.Key("chat-header-git-popover"), trigger, &hs.branchMenuOpen, func() {
		hs.branchMenuOpen = false
	}, func() {
		_, height := c.Size()
		popoverCard(c, ui.Column(c.Key("chat-header-git-menu")).
			Padding(Space1).
			Width(gitMenuWidth).
			Children(func() {
				ui.Scroll(c.Key("chat-header-git-scroll")).
					MaxHeight(max(100, height-120)).
					Children(func() {
						ui.Column(c.Key("chat-header-git-menu-list")).
							Gap(Space0).
							FillWidth().
							Children(func() {
								renderGitBranchPopoverContent(c, s, threadID, 360, &hs.branchMenuOpen)
								if canShowGitPRCreate(s, threadID, st) {
									menuRow(c, menuRowProps{
										Key:   "chat-header-pr-create-btn",
										Label: "创建 PR",
										Icon:  IconGitPullRequestCreate(),
										Title: "创建 PR",
										OnClick: func() {
											hs.branchMenuOpen = false
											openGitPRCreateDialog(s, threadID)
											s.invalidate()
										},
									})
								}
								gitMenuDivider(c)
								// 复制当前分支名（detached 时复制短哈希），方便在终端里直接使用
								copyBranch := st.Branch
								if copyBranch == "" {
									copyBranch = st.Head
								}
								if copyBranch != "" {
									menuRow(c, menuRowProps{
										Key:   "chat-header-git-copy-branch-btn",
										Label: "复制分支名",
										Title: "复制分支名",
										OnClick: func() {
											hs.branchMenuOpen = false
											c.WriteClipboard(copyBranch)
											s.setToast("已复制分支名")
											s.invalidate()
										},
									})
								}

								if len(st.Worktrees) > 0 {
									ui.Text(c, "Worktree").
										FontSize(uiFont(TypeCaption)).
										Bold().
										TextColor(ThemeSecondaryText(c)).
										Padding(Space3, Space3, Space0, Space3)
									for _, w := range st.Worktrees {
										wt := w
										if !visibleHeaderWorktree(wt.Branch) {
											continue
										}
										name := filepath.Base(wt.Path)
										var onClick func()
										if !wt.Current {
											onClick = func() {
												run(func() error {
													return s.client.GitSwitchWorktree(context.Background(), threadID, wt.Path)
												})
											}
										}
										menuRow(c, menuRowProps{
											Key:         "chat-header-git-wt-" + wt.Path,
											Label:       "切换到 worktree " + name,
											Title:       name,
											Trailing:    truncateRunes(wt.Branch, 16),
											Checked:     wt.Current,
											Highlighted: wt.Current,
											Disabled:    busy,
											OnClick:     onClick,
										})
									}
								}

								// 非主 worktree 时不提供新建/迁入 Worktree
								inMainWorktree := true
								for i, wt := range st.Worktrees {
									if wt.Current && i > 0 {
										inMainWorktree = false
										break
									}
								}
								if inMainWorktree {
									s.mu.Lock()
									emptyThread := s.selectedThreadID == threadID && len(s.messages) == 0
									s.mu.Unlock()
									if emptyThread {
										menuRow(c, menuRowProps{
											Key:      "chat-header-git-new-wt-btn",
											Label:    "为此会话新建 Worktree",
											Title:    "为此会话新建 Worktree",
											Disabled: busy,
											OnClick: func() {
												run(func() error {
													if err := s.client.MoveToWorktree(context.Background(), threadID, "", false); err != nil {
														return err
													}
													s.setToast("已迁入 Worktree")
													return nil
												})
											},
										})
									} else if hs.wtMoving {
										ui.Column(c.Key("chat-header-git-move-wt-form")).
											FillWidth().
											Padding(Space0, Space1).
											Gap(Space2).
											Children(func() {
												in := ui.TextInput(c.Key("chat-header-git-move-wt-input"), &hs.wtBranch).
													Label("Worktree 分支名").
													Placeholder("留空自动生成").
													FontSize(uiFont(TypeFootnote)).
													TextColor(ThemePrimaryText(c)).
													Background(ThemeHover(c).Alpha(0.25)).
													BorderColor(ThemeAccent(c)).
													BorderWidth(1).
													Radius(RadiusSM).
													Padding(Space1, Space3).
													FillWidth().
													AutoFocus()
												in.OnTextKey(func(k ui.TextKey) bool {
													if k.Key == ui.KeyEscape {
														chatHeaderMu.Lock()
														hs.wtMoving = false
														hs.wtBranch = ""
														chatHeaderMu.Unlock()
														s.invalidate()
														return true
													}
													return false
												})
												ui.Row(c.Key("chat-header-git-move-wt-transfer")).
													Gap(Space2).
													AlignItems(ui.Center).
													Children(func() {
														if cb := ui.Checkbox(c.Key("chat-header-git-move-wt-transfer-check"), &hs.wtTransfer, "同时迁移未提交的改动"); cb.Changed() {
															s.invalidate()
														}
													})
												Button(c.Key("chat-header-git-move-wt-confirm"), ButtonOpts{
													Text:     "迁入",
													Disabled: busy,
													OnClick: func() {
														branch := strings.TrimSpace(hs.wtBranch)
														transfer := hs.wtTransfer
														run(func() error {
															if err := s.client.MoveToWorktree(context.Background(), threadID, branch, transfer); err != nil {
																return err
															}
															s.setToast("已迁入 Worktree")
															return nil
														})
													},
													Variant: ButtonPrimary,
												})
											})
									} else {
										menuRow(c, menuRowProps{
											Key:      "chat-header-git-move-wt-btn",
											Label:    "迁入 Worktree…",
											Title:    "迁入 Worktree…",
											Disabled: busy,
											OnClick: func() {
												chatHeaderMu.Lock()
												hs.wtMoving = true
												hs.wtBranch = ""
												hs.wtTransfer = true
												hs.branchErr = ""
												chatHeaderMu.Unlock()
												s.invalidate()
											},
										})
									}
								}

								if switching {
									ui.Row(c.Key("chat-header-git-busy")).
										Gap(Space2).
										Padding(Space2, Space3).
										AlignItems(ui.Center).
										Children(func() {
											LoadingIndicator(c, nil, "正在切换…", IconSM)
										})
								}
								if errMsg != "" {
									renderGitError(c, "chat-header-git-error", errMsg)
								}
							})
					})
			}))
	})
}

// canShowGitPRCreate 判断当前分支菜单是否应显示「创建 PR」入口：
// 非主分支、有 remote、已查过 PR 状态且当前分支尚无 PR。
func canShowGitPRCreate(s *chatState, threadID string, st GitState) bool {
	if s == nil || s.client == nil || threadID == "" {
		return false
	}
	if _, ok := s.client.(gitPRBranchClient); !ok {
		return false
	}
	branch := strings.TrimSpace(st.Branch)
	if st.Detached || branch == "" || strings.TrimSpace(st.RemoteURL) == "" {
		return false
	}
	pr, loaded := s.headerGitPR(threadID)
	if !loaded || pr != nil {
		return false
	}
	// shortcut: GitState 没有默认分支字段，按常见名判断；有该字段后改用它
	return branch != "main" && branch != "master"
}

// renderGitPRHeaderChip 在分支胶囊右侧渲染当前分支的 PR 状态标签。
func renderGitPRHeaderChip(c *ui.Context, s *chatState, hs *chatHeaderState, threadID string, st GitState) {
	if s == nil || s.client == nil || threadID == "" {
		return
	}
	if _, ok := s.client.(gitPRBranchClient); !ok {
		return
	}
	branch := strings.TrimSpace(st.Branch)
	if st.Detached || branch == "" || strings.TrimSpace(st.RemoteURL) == "" {
		return
	}
	pr, loaded := s.headerGitPR(threadID)
	if !loaded || pr == nil {
		return
	}

	numText := fmt.Sprintf("#%d", pr.Number)
	chip := ui.ButtonBase(c.Key("chat-header-pr-chip")).
		Label("PR "+numText).
		Height(ControlSM).
		Padding(0, Space3).
		Radius(RadiusPill).
		Gap(Space2).
		AlignItems(ui.Center).
		Border(Hairline, ThemeBorder(c)).
		Background(ThemeOverlay(c, OverlaySubtle)).
		Transition(motionFeedback(c)).
		OnClick(func() {
			hs.titleMenuOpen = false
			hs.branchMenuOpen = false
			hs.commitMenuOpen = false
			if strings.EqualFold(strings.TrimSpace(pr.State), "open") {
				s.openGitPRDialog()
				s.selectGitPR(pr.Number)
				return
			}
			if u := strings.TrimSpace(pr.URL); u != "" {
				c.OpenURL(u)
			}
		})
	if title := strings.TrimSpace(pr.Title); title != "" {
		chip.Tooltip(title)
	}
	scale := motionPressScale(c, chip, "button-press-scale", chip.Pressed())
	chip.Scale(scale, scale)
	if chip.Hovered() {
		chip.Background(ThemeHover(c))
	}
	focusRing(c, chip)

	state := strings.ToLower(strings.TrimSpace(pr.State))
	dotColor := ThemeSecondaryText(c)
	switch {
	case state == "merged":
		dotColor = ThemePlan(c)
	case pr.IsDraft || state == "closed" || state == "locked":
		dotColor = ThemeSecondaryText(c)
	case state == "open" || state == "opened":
		dotColor = ThemeSuccess(c)
	}

	chip.Children(func() {
		ui.Box(c.Key("chat-header-pr-dot")).
			Size(Space2, Space2).
			Radius(RadiusPill).
			Background(dotColor).
			Shrink(0)
		ui.Text(c, numText).
			FontSize(uiFont(TypeFootnote)).
			FontWeight(500).
			TextColor(ThemePrimaryText(c)).
			SingleLine()
		switch strings.ToLower(strings.TrimSpace(pr.ChecksState)) {
		case "success":
			ui.Icon(c, IconCircleCheck()).Size(IconSM, IconSM).TextColor(ThemeSuccess(c))
		case "failure":
			ui.Icon(c, IconCircleX()).Size(IconSM, IconSM).TextColor(ThemeDanger(c))
		case "pending":
			ui.Icon(c, IconClock()).Size(IconSM, IconSM).TextColor(ThemeWarning(c))
		}
	})
}

// renderGitCountBadge 渲染图标按钮右上角的数字角标：用 Absolute 叠在按钮容器上，
// 不占布局宽度也不撑高按钮；强调色底、白字，超过 99 显示 99+。
func renderGitCountBadge(c *ui.Context, key string, count int) {
	text := strconv.Itoa(count)
	if count > 99 {
		text = "99+"
	}
	ui.Row(c.Key(key)).
		Absolute().
		Top(-2).
		Right(-4).
		Height(IconSM).
		MinWidth(14).
		Padding(0, Space1).
		Radius(RadiusPill).
		Center().
		Background(ThemeAccent(c)).
		Children(func() {
			ui.Text(c, text).
				FontSize(uiFont(TypeMicro)).
				FontWeight(600).
				TextColor(ThemeOnAccent(c)).
				SingleLine().
				Transition(motionFeedback(c))
		})
}

// renderGitError 渲染 Popover 内的错误提示。
func renderGitError(c *ui.Context, key, msg string) {
	ui.Row(c.Key(key)).
		Padding(Space2, Space3).
		Radius(RadiusSM).
		MaxWidth(320).
		Background(ThemeDanger(c).Alpha(0.12)).
		Children(func() {
			ui.Text(c, msg).
				FontSize(uiFont(TypeFootnote)).
				TextColor(ThemeDanger(c))
		})
}

// gitChangeBadgeVisible 返回 Git 按钮是否叠改动数角标：设置页关掉后不画。
// 设置尚未注册时按默认显示，免得启动早期工具栏少一块。
func gitChangeBadgeVisible(s *chatState) bool {
	cs := getChatSettings(s)
	if cs == nil {
		return true
	}
	return cs.cfg.SourceControl.ShowChangeBadgeEnabled()
}

// renderGitPushPRConfirm 渲染「推送并建 PR/MR」的二次确认区，确认后才真正推送。
func renderGitPushPRConfirm(c *ui.Context, s *chatState, st GitState) {
	hs := getChatHeaderState(s)
	chatHeaderMu.Lock()
	pushBusy := hs.pushBusy
	chatHeaderMu.Unlock()
	ui.Column(c.Key("chat-header-push-pr-confirm")).
		FillWidth().
		Gap(Space2).
		Padding(Space0, Space3).
		Children(func() {
			ui.Text(c, "合并子代理改动（PR）？").
				FontSize(uiFont(TypeFootnote)).
				TextColor(ThemeSecondaryText(c))
			ui.Row(c.Key("chat-header-push-pr-confirm-actions")).
				Gap(Space2).
				AlignItems(ui.Center).
				Children(func() {
					Button(c.Key("chat-header-push-pr-confirm-ok"), ButtonOpts{
						Text:    "确认合并",
						Loading: pushBusy,
						OnClick: func() {
							// bee 会话回到所属 lead，普通会话即当前会话
							lead := s.selectedThreadID
							if target := s.swarmTargetLeadID(); target != "" {
								lead = target
							}
							s.triggerManualPushPR(lead)
						},
						Variant: ButtonPrimary,
					})
					Button(c.Key("chat-header-push-pr-confirm-cancel"), ButtonOpts{
						Text:     "取消",
						Disabled: pushBusy,
						OnClick: func() {
							s.cancelPushPRConfirm()
						},
						Variant: ButtonSecondary,
					})
				})
		})
}

// isThreadRunning 返回指定会话当前是否还在运行（含名下子线程未结束）；拉取、合并与贮藏会改写工作区，运行中需要禁用。
func (s *chatState) isThreadRunning(threadID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, th := range s.threads {
		if th.ID == threadID {
			return th.IsRunning || th.SwarmBusy
		}
	}
	return false
}

// gitMenuWidth 是分支菜单宽度。
const gitMenuWidth = 248

// gitMenuDivider 是 Git 菜单内的水平分隔线。
func gitMenuDivider(c *ui.Context) {
	ui.Box(c).
		Height(Hairline).
		FillWidth().
		Margin(4, 0, 4, 0).
		Background(ThemeBorder(c))
}

// normalizeGitRemoteURL 将 ssh 或 https 格式的 Git remote URL 规范化为 https 网页仓库地址。
// 支持格式：
//   - git@host:owner/repo.git -> https://host/owner/repo
//   - git@gitlab.com:group/subgroup/repo.git -> https://gitlab.com/group/subgroup/repo
//   - https://host/owner/repo.git -> https://host/owner/repo
//   - http://host/owner/repo.git -> https://host/owner/repo
//   - ssh://git@host[:port]/owner/repo.git -> https://host/owner/repo
func normalizeGitRemoteURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	var host, path string

	if strings.HasPrefix(raw, "ssh://") {
		trimmed := strings.TrimPrefix(raw, "ssh://")
		if idx := strings.Index(trimmed, "@"); idx != -1 {
			trimmed = trimmed[idx+1:]
		}
		slashIdx := strings.Index(trimmed, "/")
		if slashIdx == -1 {
			return ""
		}
		hostPart := trimmed[:slashIdx]
		path = trimmed[slashIdx+1:]

		// 若 hostPart 带有 ssh 端口（如 host:2222），在 Web 链接中去除该端口
		if colonIdx := strings.Index(hostPart, ":"); colonIdx != -1 {
			host = hostPart[:colonIdx]
		} else {
			host = hostPart
		}
	} else if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		trimmed := raw
		if strings.HasPrefix(raw, "http://") {
			trimmed = strings.TrimPrefix(raw, "http://")
		} else {
			trimmed = strings.TrimPrefix(raw, "https://")
		}
		slashIdx := strings.Index(trimmed, "/")
		if slashIdx == -1 {
			return ""
		}
		hostPart := trimmed[:slashIdx]
		path = trimmed[slashIdx+1:]

		// 去除可能的用户信息 user:pass@
		if atIdx := strings.Index(hostPart, "@"); atIdx != -1 {
			hostPart = hostPart[atIdx+1:]
		}
		host = hostPart
	} else if strings.Contains(raw, "@") && strings.Contains(raw, ":") {
		// SCP 格式：git@host:owner/repo.git
		atIdx := strings.Index(raw, "@")
		colonIdx := strings.Index(raw, ":")
		if colonIdx > atIdx {
			host = raw[atIdx+1 : colonIdx]
			path = raw[colonIdx+1:]
		} else {
			return ""
		}
	} else {
		return ""
	}

	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.Trim(path, "/")

	if host == "" || path == "" {
		return ""
	}

	return "https://" + host + "/" + path
}

// gitPullRequestURL 根据规范化后的仓库地址与分支名生成创建 Pull Request / Merge Request 的 URL。
// 对 GitHub 用 /compare/<branch>?expand=1，对 GitLab 用 /-/merge_requests/new?merge_request[source_branch]=<branch>。
func gitPullRequestURL(repoURL, branch string) string {
	repoURL = strings.TrimSuffix(strings.TrimSpace(repoURL), "/")
	branch = strings.TrimSpace(branch)
	if repoURL == "" || branch == "" {
		return ""
	}

	isGitLab := strings.Contains(strings.ToLower(repoURL), "gitlab")
	if isGitLab {
		return repoURL + "/-/merge_requests/new?merge_request[source_branch]=" + url.QueryEscape(branch)
	}
	return repoURL + "/compare/" + branch + "?expand=1"
}

// isGenerateCommitMessageEnabled 检查当前配置中是否开启了 AI 生成提交信息功能。
func isGenerateCommitMessageEnabled(s *chatState) bool {
	if st := getChatSettings(s); st != nil {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.cfg.SourceControl.GenerateCommitMessageEnabled()
	}
	return true
}

// ---------------------------------------------------------------------------
// 标题栏项目动作：在其它应用中打开与脚本菜单
// ---------------------------------------------------------------------------

// closeProjectActionMenus 关闭标题栏项目动作弹层，keep 指定的那个保持原样（"" 表示全部关闭）。
func closeProjectActionMenus(hs *chatHeaderState, keep string) {
	if keep != "openin" {
		hs.openInMenuOpen = false
	}
	if keep != "scripts" {
		hs.scriptsMenuOpen = false
	}
}

// threadRuntimeMode 返回指定会话当前的权限档位，未设置或找不到时按 composer 的默认档位处理。
func (s *chatState) threadRuntimeMode(threadID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, th := range s.threads {
		if th.ID == threadID {
			if th.RuntimeMode != "" {
				return th.RuntimeMode
			}
			break
		}
	}
	return "auto"
}

// threadProvider 返回指定会话的 provider ID，找不到时返回空字符串。
func (s *chatState) threadProvider(threadID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, th := range s.threads {
		if th.ID == threadID {
			return th.Provider
		}
	}
	return ""
}

// threadModel 返回指定会话的模型 ID，找不到时返回空字符串。
func (s *chatState) threadModel(threadID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, th := range s.threads {
		if th.ID == threadID {
			return th.Model
		}
	}
	return ""
}

// threadPersona 返回指定会话当前的人设值：空串表示从未设置，"none" 表示用户选了无人设。
func (s *chatState) threadPersona(threadID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, th := range s.threads {
		if th.ID == threadID {
			return th.Persona
		}
	}
	return ""
}

// personaNoneValue 是 ComposerPrefs.Persona 里表示「用户明确选了无人设」的字面量。
const personaNoneValue = "none"

// personaMenuChecked 判断人设菜单项是否显示选中勾：当前人设等于该项 id 时勾该项；
// 当前人设为空串、none 或已不在列表里时，勾在「无人设」项（id 传 personaNoneValue）上。
func personaMenuChecked(current, id string, personas []MagpiePersona) bool {
	if id == personaNoneValue {
		if current == "" || current == personaNoneValue {
			return true
		}
		for _, p := range personas {
			if p.ID == current {
				return false
			}
		}
		return true
	}
	return current == id
}

// personaMenuWriteValue 返回点击菜单项时要写入的 Persona 值。
// 返回 ok 为假表示无需写入：点「无人设」且当前从未设置过，或写入值与当前值相同。
func personaMenuWriteValue(current, id string) (string, bool) {
	if id == personaNoneValue && current == "" {
		return "", false
	}
	if id == current {
		return "", false
	}
	return id, true
}

// runtimeModeSummary 返回权限档位的说明文字，未知档位返回空字符串。
func runtimeModeSummary(mode string) string {
	for _, opt := range runtimeModeOptions {
		if opt.mode == mode {
			return opt.summary
		}
	}
	return ""
}

// headerRuntimeModeSupported 判断 provider 是否支持该档位。
func headerRuntimeModeSupported(provider, mode string) bool {
	return true
}

// setHeaderRuntimeMode 切换指定会话的权限档位并持久化。
func (s *chatState) setHeaderRuntimeMode(threadID, mode string) {
	if mode == "" {
		return
	}
	s.mu.Lock()
	var oldMode string
	for i := range s.threads {
		if s.threads[i].ID == threadID {
			oldMode = s.threads[i].RuntimeMode
			s.threads[i].RuntimeMode = mode
			break
		}
	}
	for _, list := range s.threadsByProj {
		for i := range list {
			if list[i].ID == threadID {
				list[i].RuntimeMode = mode
			}
		}
	}
	s.mu.Unlock()
	s.invalidate()
	go func() {
		if err := s.client.SetThreadRuntimeMode(threadID, mode); err != nil {
			s.mu.Lock()
			for i := range s.threads {
				if s.threads[i].ID == threadID && s.threads[i].RuntimeMode == mode {
					s.threads[i].RuntimeMode = oldMode
				}
			}
			for _, list := range s.threadsByProj {
				for i := range list {
					if list[i].ID == threadID && list[i].RuntimeMode == mode {
						list[i].RuntimeMode = oldMode
					}
				}
			}
			s.mu.Unlock()
			s.setToast("设置权限模式失败: " + err.Error())
			s.invalidate()
		}
	}()
}

// openInApp 描述「在其它应用中打开」菜单中的一项；AppPath 为空表示系统文件管理器（访达/资源管理器）。
// BundleID 用于 macOS 上应用路径不存在时改用 open -b 唤起。
type openInApp struct {
	Name     string
	AppPath  string
	BundleID string
}

// openInGOOS 是打开方式分支使用的平台标识，测试中可替换。
var openInGOOS = runtime.GOOS

// openInFileManagerName 返回系统文件管理器在打开方式菜单中的名称。
func openInFileManagerName() string {
	if openInGOOS == "windows" {
		return "资源管理器"
	}
	return "访达"
}

// revealInFileManagerLabel 返回「在系统文件管理器中定位」动作在当前平台的文案。
func revealInFileManagerLabel() string {
	return "在" + openInFileManagerName() + "中显示"
}

// openInRunner 执行 open 命令，测试中可替换。
var openInRunner = func(args ...string) error {
	cmd := exec.Command("open", args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// openInLaunchRunner 在 Windows 上直接启动可执行文件，测试中可替换。
var openInLaunchRunner = func(exePath string, args ...string) error {
	cmd := exec.Command(exePath, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// detectOpenInApps 返回可用的打开方式：系统文件管理器始终第一项，其余按固定顺序只保留已安装的应用。
func detectOpenInApps(statFn func(string) bool) []openInApp {
	apps := []openInApp{{Name: openInFileManagerName()}}
	var candidates []openInApp
	if openInGOOS == "windows" {
		candidates = windowsOpenInAppCandidates()
	} else {
		candidates = []openInApp{
			{Name: "Visual Studio Code", AppPath: "/Applications/Visual Studio Code.app"},
			{Name: "Cursor", AppPath: "/Applications/Cursor.app"},
			{Name: "Zed", AppPath: "/Applications/Zed.app"},
			{Name: "Xcode", AppPath: "/Applications/Xcode.app"},
			{Name: "Windsurf", AppPath: "/Applications/Windsurf.app"},
			{Name: "Sublime Text", AppPath: "/Applications/Sublime Text.app"},
		}
	}
	// Windows 上 Visual Studio Code 有两个常见安装位置，同一应用只列一次。
	seen := make(map[string]bool, len(candidates))
	for _, app := range candidates {
		if seen[app.Name] {
			continue
		}
		if statFn != nil && statFn(app.AppPath) {
			seen[app.Name] = true
			apps = append(apps, app)
		}
	}
	return apps
}

// windowsOpenInAppCandidates 返回 Windows 上默认的打开方式候选，路径来自环境变量，按可执行文件检测。
func windowsOpenInAppCandidates() []openInApp {
	localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	programFiles := strings.TrimSpace(os.Getenv("ProgramFiles"))
	apps := make([]openInApp, 0, 6)
	add := func(name, base string, rel ...string) {
		if base == "" {
			return
		}
		apps = append(apps, openInApp{Name: name, AppPath: filepath.Join(append([]string{base}, rel...)...)})
	}
	add("Visual Studio Code", localAppData, "Programs", "Microsoft VS Code", "Code.exe")
	add("Visual Studio Code", programFiles, "Microsoft VS Code", "Code.exe")
	add("Cursor", localAppData, "Programs", "cursor", "Cursor.exe")
	add("Zed", localAppData, "Programs", "Zed", "Zed.exe")
	add("Windsurf", localAppData, "Programs", "Windsurf", "Windsurf.exe")
	add("Sublime Text", programFiles, "Sublime Text", "sublime_text.exe")
	return apps
}

// openInWithApp 在指定应用中打开目录；AppPath 与 BundleID 都为空表示系统文件管理器，直接打开目录本身。
// 应用路径不存在（或未配置）但 macOS 上有 BundleID 时，改用 open -b <bundleID> <dir> 唤起。
func openInWithApp(app openInApp, dir string) error {
	if dir == "" {
		return nil
	}
	path := strings.TrimSpace(app.AppPath)
	if openInGOOS == "windows" {
		return openInWithAppWindows(path, dir)
	}
	bundle := strings.TrimSpace(app.BundleID)
	bundleFallback := bundle != "" && runtime.GOOS == "darwin"
	if path == "" {
		if bundleFallback {
			return openInRunner("-b", bundle, dir)
		}
		return openInRunner(dir)
	}
	if !openInStatFn(path) && bundleFallback {
		return openInRunner("-b", bundle, dir)
	}
	return openInRunner("-a", path, dir)
}

// openInWithAppWindows 在 Windows 上用资源管理器或应用可执行文件打开目录。
func openInWithAppWindows(path, dir string) error {
	if path != "" && (strings.EqualFold(filepath.Ext(path), ".exe") || openInStatFn(path)) {
		return openInLaunchRunner(path, dir)
	}
	return openInLaunchRunner("explorer.exe", dir)
}

// defaultOpenInAppStat 检查应用路径是否存在；Windows 上还要求是普通文件（可执行文件）。
func defaultOpenInAppStat(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	if openInGOOS == "windows" {
		return fi.Mode().IsRegular()
	}
	return true
}

// openInStatFn 检查应用是否已安装，测试中可替换。
var openInStatFn = defaultOpenInAppStat

// requestPushPRConfirm 进入「推送并建 PR/MR」的二次确认态；确认前不触发任何推送。
func (s *chatState) requestPushPRConfirm() {
	if s == nil {
		return
	}
	hs := getChatHeaderState(s)
	chatHeaderMu.Lock()
	hs.pushConfirm = true
	chatHeaderMu.Unlock()
	s.invalidate()
}

// cancelPushPRConfirm 退出「推送并建 PR/MR」的二次确认态，不触发推送。
func (s *chatState) cancelPushPRConfirm() {
	if s == nil {
		return
	}
	hs := getChatHeaderState(s)
	chatHeaderMu.Lock()
	hs.pushConfirm = false
	chatHeaderMu.Unlock()
	s.invalidate()
}

// triggerManualPushPR 异步触发当前分支的推送与 PR/MR 创建；执行中置 pushBusy 防止重复点击。
func (s *chatState) triggerManualPushPR(leadThreadID string) {
	if s == nil || s.client == nil || leadThreadID == "" {
		return
	}
	hs := getChatHeaderState(s)
	chatHeaderMu.Lock()
	if hs.pushBusy {
		chatHeaderMu.Unlock()
		return
	}
	hs.pushBusy = true
	chatHeaderMu.Unlock()
	s.invalidate()

	go func() {
		err := s.client.TriggerAutoMerge(context.Background(), leadThreadID)
		chatHeaderMu.Lock()
		hs.pushBusy = false
		hs.pushConfirm = false
		chatHeaderMu.Unlock()
		if err != nil {
			s.setToast("推送并创建 PR/MR 失败: " + err.Error())
		} else {
			s.setToast("已推送并创建 PR/MR")
		}
		s.invalidate()
	}()
}

// renderChatHeader 渲染会话头部栏。
func renderChatHeader(c *ui.Context, s *chatState, selectedID string, _ ThreadUsage, sidebarSlide float32) {
	var selectedThread Thread
	s.mu.Lock()
	sidebarCollapsed := s.sidebarCollapsed
	sidebarWidth := s.sidebarWidth
	inspector := inspectorExistingSessionLocked(s, s.selectedThreadID)
	inspectorOpen := inspector != nil && inspector.Open
	for _, th := range s.threads {
		if th.ID == selectedID {
			selectedThread = th
			break
		}
	}
	s.mu.Unlock()

	rightInset := chatHeaderBaseInset
	if !inspectorOpen {
		rightInset += titleBarTrailingInset(c)
	}
	ts := getChatToolbarState(s)
	hs := getChatHeaderState(s)
	header := ui.Row(c.Key("chat-header")).DragWindow().FillWidth().MinWidth(0).Height(HeaderHeight).
		Padding(Space3, rightInset, Space3, chatHeaderLeadingInsetFrom(titleBarLeadingInset(c), sidebarSlide)).
		BorderWidth(0, 0, 1, 0).BorderColor(ThemeBorder(c)).AlignItems(ui.Center).Gap(Space2)
	windowWidth, _ := c.Size()
	headerWidth := headerCollapseWidth(windowWidth, sidebarWidth, sidebarSlide)
	collapseLevel := headerCollapseLevel(headerWidth)
	if windowWidth > 0 && headerWidth <= 0 {
		collapseLevel = 2
	}
	collapsed := collapseLevel > 0
	collapseLeft := collapseLevel >= 2
	header.Children(func() {
		// 左侧依次是父芯片、侧栏开关、会话列表与新建会话（仅侧栏收起时）、编排与分支胶囊，
		// 之后是占满剩余宽度的标题；右侧宽屏为动作与辅助图标按钮，窄屏收成单一「…」。
		var gitHS *chatHeaderState
		var gitST GitState
		var gitOK bool
		ui.Row(c.Key("chat-header-left")).Gap(Space1).AlignItems(ui.Center).Grow(1).Shrink(1).MinWidth(0).ClipX().Children(func() {
			renderChatHeaderParentChip(c, s)
			IconButton(c, IconButtonProps{Key: "chat-header-toggle-sidebar-btn", Icon: IconPanelLeft(), Size: ControlMD, Variant: ButtonGhost,
				Label: "显示/隐藏侧边栏 " + shortcutDisplay("⌘B"), OnClick: func() { toggleSidebarPersisted(s) }})
			if sidebarCollapsed {
				threadsBtn := IconButton(c, IconButtonProps{Key: "chat-header-threads-btn", Icon: IconChevronDown(), Size: ControlMD, Variant: ButtonGhost,
					Label: "会话列表", Active: ts.threadsPopoverOpen, OnClick: func() { s.headerToggleThreadsPopover() }})
				PopoverCard(c, threadsBtn, PopoverProps{Key: "chat-header-threads-popover", Open: &ts.threadsPopoverOpen, Width: chatThreadsPopoverWidth}, func() {
					renderChatThreadsPopover(c, s)
				})
				IconButton(c, IconButtonProps{Key: "chat-header-new-thread-btn", Icon: IconMessageSquarePlus(), Size: ControlMD, Variant: ButtonGhost,
					Label: "新建会话 " + shortcutDisplay("⌘N"), OnClick: s.headerNewThread})
			}
			renderSwarmHeaderButton(c, s)
			if collapseLeft {
				gitHS, gitST, gitOK = syncChatHeaderGit(c, s, selectedID)
			} else {
				renderChatHeaderGit(c, s, selectedID)
			}
			renderChatHeaderTitle(c, s, selectedThread)
		})
		ui.Row(c.Key("chat-header-right")).Gap(Space1).AlignItems(ui.Center).Shrink(0).Children(func() {
			if stewardSelected(s, selectedID) && !stewardBoardWide(chatAreaWidth(c, s)) {
				renderStewardBoardBadge(c, s)
			}
			if collapsed {
				renderScriptsSheet(c, s)
				changes := 0
				if selectedID != "" {
					changes = s.headerGitState(selectedID).Changes
				}
				pendingProposals := memoryPendingBadgeCount(s)
				s.refreshPendingMemoryCount(false)
				syncMemoryMenuLabel()
				ui.Row(c.Key("chat-header-more-wrap")).AlignItems(ui.Center).Children(func() {
					more := IconButton(c, IconButtonProps{
						Key:     "chat-header-more-btn",
						Icon:    IconMoreHorizontal(),
						Size:    ControlMD,
						Variant: ButtonGhost,
						Label:   "更多头部操作",
						Tooltip: "更多",
						Active:  ts.morePopoverOpen,
						OnClick: func() {
							if togglePopoverOpen(&ts.morePopoverOpen) {
								hs.moreSubmenu = ""
							}
						},
					})
					if changes > 0 && gitChangeBadgeVisible(s) {
						renderGitCountBadge(c, "chat-header-more-badge", changes)
					}
					PopoverCard(c, more, PopoverProps{
						Key:       "chat-header-more-popover",
						Open:      &ts.morePopoverOpen,
						Align:     BelowEnd,
						OnDismiss: func() { hs.moreSubmenu = "" },
					}, func() {
						renderChatHeaderMorePopoverContent(c, s, hs, ts, selectedID, collapseLeft, gitHS, gitST, gitOK, changes, pendingProposals)
					})
				})
			} else {
				renderChatHeaderActionGroup(c, s, selectedID)
				renderChatHeaderAuxGroup(c, s)
			}
			if stewardSelected(s, selectedID) {
				nickname, _ := s.stewardIdentity()
				IconButton(c, IconButtonProps{Key: "chat-header-steward-settings", Icon: IconSettings(), Size: ControlMD, Variant: ButtonGhost,
					Label: stewardName(nickname) + "设置", OnClick: func() { openSettingsWindow(s, settingsPageSteward) }})
			}
		})
	})
}

// renderChatHeaderMorePopoverContent 渲染窄屏下唯一「…」按钮的扁平弹层内容，
// 依次平铺（<560 时的 Git 分支）、外部打开、运行、终端、右栏（带改动角标）、分隔线、项目文档与记忆。
func renderChatHeaderMorePopoverContent(
	c *ui.Context,
	s *chatState,
	hs *chatHeaderState,
	ts *chatToolbarState,
	selectedID string,
	collapseLeft bool,
	gitHS *chatHeaderState,
	gitST GitState,
	gitOK bool,
	changes int,
	pendingProposals int,
) {
	dir := s.headerProjectDir(selectedID)
	hasScripts := s.headerScriptsButtonVisible(selectedID)

	ui.Column(c.Key("chat-header-more-content")).MinWidth(204).Gap(Space0).FocusGroup(ui.Vertical).Children(func() {
		if hs.moreSubmenu == "openin" && dir != "" {
			menuRow(c, menuRowProps{
				Key:   "chat-header-more-back",
				Icon:  IconArrowLeft(),
				Title: "外部打开",
				OnClick: func() {
					hs.moreSubmenu = ""
					s.invalidate()
				},
			})
			gitMenuDivider(c)
			renderHeaderOpenInMenuRows(c, s, dir, func() {
				ts.morePopoverOpen = false
				hs.moreSubmenu = ""
			})
			return
		}
		if hs.moreSubmenu == "scripts" && hasScripts {
			menuRow(c, menuRowProps{
				Key:   "chat-header-more-back",
				Icon:  IconArrowLeft(),
				Title: "运行",
				OnClick: func() {
					hs.moreSubmenu = ""
					s.invalidate()
				},
			})
			gitMenuDivider(c)
			renderHeaderScriptsMenuRows(c, s, selectedID, func() {
				ts.morePopoverOpen = false
				hs.moreSubmenu = ""
			})
			return
		}

		if collapseLeft && gitOK {
			ui.Row(c.Key("chat-header-more-left-group")).Gap(Space2).AlignItems(ui.Center).Children(func() {
				renderGitBranchCapsule(c, s, gitHS, selectedID, gitST)
				renderGitPRHeaderChip(c, s, gitHS, selectedID, gitST)
			})
			gitMenuDivider(c)
		}
		if dir != "" {
			menuRow(c, menuRowProps{
				Key:   "chat-header-more-open-in",
				Icon:  IconSquareArrowOutUpRight(),
				Title: "外部打开",
				OnClick: func() {
					hs.moreSubmenu = "openin"
					s.invalidate()
				},
			})
		}
		if hasScripts {
			menuRow(c, menuRowProps{
				Key:   "chat-header-more-scripts",
				Icon:  IconPlay(),
				Title: "运行",
				OnClick: func() {
					hs.moreSubmenu = "scripts"
					s.invalidate()
				},
			})
		}

		terminalOpen := false
		if termSt := getTerminalState(s); termSt != nil {
			terminalMu.Lock()
			terminalOpen = termSt.visible
			terminalMu.Unlock()
		}
		inspector := inspectorSession(s)
		inspectorOpen := inspector != nil && inspector.Open

		menuRow(c, menuRowProps{
			Key:     "chat-header-more-terminal",
			Icon:    IconTerminal(),
			Title:   "终端",
			Checked: terminalOpen,
			OnClick: func() {
				ts.morePopoverOpen = false
				s.headerToggleTerminal()
			},
		})
		badgeDetail := ""
		if changes > 0 && gitChangeBadgeVisible(s) {
			if changes > 99 {
				badgeDetail = "99+"
			} else {
				badgeDetail = strconv.Itoa(changes)
			}
		}
		menuRow(c, menuRowProps{
			Key:      "chat-header-more-inspector",
			Icon:     IconPanelRight(),
			Title:    "右栏",
			Trailing: badgeDetail,
			Checked:  inspectorOpen,
			OnClick: func() {
				ts.morePopoverOpen = false
				toggleInspector(s)
			},
		})

		gitMenuDivider(c)
		menuRow(c, menuRowProps{
			Key:   "chat-header-more-context",
			Title: "项目文档",
			OnClick: func() {
				ts.morePopoverOpen = false
				s.headerToggleContextPanel()
			},
		})
		memDetail := ""
		if pendingProposals > 0 {
			memDetail = strconv.Itoa(pendingProposals)
		}
		menuRow(c, menuRowProps{
			Key:      "chat-header-more-memory",
			Title:    memoryEntryLabel(s.client),
			Trailing: memDetail,
			OnClick: func() {
				ts.morePopoverOpen = false
				s.headerToggleMemoryPanel()
			},
		})
	})
}

const chatThreadsPopoverWidth float32 = 320 // token-exempt: 会话列表需要足够宽度展示长标题。

// chatToolbarState 维护会话头部工具栏的弹层状态，按 chatState 附加（同 chatHeaderState）。
// threadsPopoverOpen 与 threadsList 绑定在会话列表 Popover 上，只在 UI 线程读写。
type chatToolbarState struct {
	threadsPopoverOpen bool
	threadsList        searchListState
	morePopoverOpen    bool
}

var (
	chatToolbarMu     sync.Mutex
	chatToolbarStates = make(map[*chatState]*chatToolbarState)
)

func getChatToolbarState(s *chatState) *chatToolbarState {
	chatToolbarMu.Lock()
	defer chatToolbarMu.Unlock()
	st, ok := chatToolbarStates[s]
	if !ok {
		st = &chatToolbarState{}
		chatToolbarStates[s] = st
	}
	return st
}

// 以下小方法把头部工具栏各按钮的动作集中到一处，便于直接调用与测试。

// headerNewThread 新建会话。
func (s *chatState) headerNewThread() {
	if s == nil {
		return
	}
	s.createNewThread()
}

// headerToggleThreadsPopover 切换会话列表 Popover 的展开状态；打开时重置搜索与键盘选中状态。
func (s *chatState) headerToggleThreadsPopover() {
	if s == nil {
		return
	}
	ts := getChatToolbarState(s)
	ts.threadsPopoverOpen = !ts.threadsPopoverOpen
	if ts.threadsPopoverOpen {
		ts.threadsList.reset()
	}
	s.invalidate()
}

// headerSelectThread 在会话列表 Popover 里选中某个会话并关闭弹层。
func (s *chatState) headerSelectThread(threadID string) {
	if s == nil {
		return
	}
	ts := getChatToolbarState(s)
	ts.threadsPopoverOpen = false
	s.selectThread(threadID)
}

// headerToggleContextPanel 切换项目文档面板。
func (s *chatState) headerToggleContextPanel() {
	if s == nil {
		return
	}
	s.toggleContextPanel()
}

// headerToggleMemoryPanel 切换记忆面板。
func (s *chatState) headerToggleMemoryPanel() {
	if s == nil {
		return
	}
	s.toggleMemoryPanel()
}

// headerToggleTerminal 切换终端面板。
func (s *chatState) headerToggleTerminal() {
	toggleTerminal(s)
}

// headerTogglePreview 切换浏览器预览面板。
func (s *chatState) headerTogglePreview() {
	togglePreview(s)
}

// headerToggleDiff 切换改动面板。
func (s *chatState) headerToggleDiff() {
	if s == nil {
		return
	}
	s.toggleDiffPanel()
}

// renderChatHeaderAuxGroup 渲染终端、右栏（带改动数角标）和更多入口。
func renderChatHeaderAuxGroup(c *ui.Context, s *chatState) {
	hs := getChatHeaderState(s)
	pendingProposals := memoryPendingBadgeCount(s)
	s.refreshPendingMemoryCount(false)
	// 原生菜单在窗口创建前安装，那时还拿不到 client，这里补一次入口文案纠正。
	syncMemoryMenuLabel()

	s.mu.Lock()
	selectedID := s.selectedThreadID
	s.mu.Unlock()
	changes := 0
	if selectedID != "" {
		changes = s.headerGitState(selectedID).Changes
	}

	terminalOpen := false
	if termSt := getTerminalState(s); termSt != nil {
		terminalMu.Lock()
		terminalOpen = termSt.visible
		terminalMu.Unlock()
	}

	inspector := inspectorSession(s)
	inspectorOpen := inspector != nil && inspector.Open

	ui.Row(c.Key("chat-header-aux-group")).
		Gap(Space1).
		AlignItems(ui.Center).
		Children(func() {
			IconButton(c, IconButtonProps{
				Key:     "chat-header-terminal-btn",
				Icon:    IconTerminal(),
				Size:    ControlMD,
				Variant: ButtonGhost,
				Label:   "终端",
				Tooltip: shortcutTitleWithKey("toggle-terminal", "终端"),
				Active:  terminalOpen,
				OnClick: s.headerToggleTerminal,
			})
			ui.Row(c.Key("chat-header-inspector-wrap")).AlignItems(ui.Center).Children(func() {
				IconButton(c, IconButtonProps{
					Key:     "chat-header-inspector-btn",
					Icon:    IconPanelRight(),
					Size:    ControlMD,
					Variant: ButtonGhost,
					Label:   "右栏",
					Tooltip: shortcutTitleWithKey("toggle-inspector", "右栏"),
					Active:  inspectorOpen,
					OnClick: func() { toggleInspector(s) },
				})
				if changes > 0 && gitChangeBadgeVisible(s) {
					renderGitCountBadge(c, "chat-header-inspector-badge", changes)
				}
			})
			ui.Row(c.Key("chat-header-aux-more-wrap")).AlignItems(ui.Center).Children(func() {
				trigger := IconButton(c, IconButtonProps{
					Key:     "chat-header-aux-more-btn",
					Icon:    IconMoreHorizontal(),
					Size:    ControlMD,
					Variant: ButtonGhost,
					Label:   "更多工具",
					Tooltip: "更多",
					Active:  hs.auxMenuOpen,
					OnClick: func() { togglePopoverOpen(&hs.auxMenuOpen) },
				})
				if pendingProposals > 0 {
					renderGitCountBadge(c, "chat-header-memory-badge", pendingProposals)
				}
				appPopover(c.Key("chat-header-aux-more-popover"), trigger, &hs.auxMenuOpen, nil, func() {
					popoverCard(c, ui.Column(c.Key("chat-header-aux-more-menu")).Width(180).Padding(Space1).FocusGroup(ui.Vertical).Children(func() {
						item := func(key, title, detail string, action func()) {
							menuRow(c, menuRowProps{
								Key:      key,
								Title:    title,
								Trailing: detail,
								OnClick:  func() { hs.auxMenuOpen = false; action() },
							})
						}
						item("chat-header-context-btn", "项目文档", "", s.headerToggleContextPanel)
						detail := ""
						if pendingProposals > 0 {
							detail = strconv.Itoa(pendingProposals)
						}
						item("chat-header-memory-btn", memoryEntryLabel(s.client), detail, s.headerToggleMemoryPanel)
					}))
				})
			})
		})
}

// renderChatThreadsPopover 渲染会话列表 Popover：用 searchList 列出非临时会话并支持搜索与键盘选择。
func renderChatThreadsPopover(c *ui.Context, s *chatState) {
	s.mu.Lock()
	threads := withoutProvisional(s.threads)
	selectedID := s.selectedThreadID
	s.mu.Unlock()

	items := make([]searchListItem, 0, len(threads))
	for _, th := range threads {
		title := strings.TrimSpace(th.Title)
		if title == "" {
			title = "未命名会话"
		}
		items = append(items, searchListItem{
			ID:       th.ID,
			Title:    title,
			Trailing: formatRelativeTime(threadActivityTime(th)),
			Checked:  th.ID == selectedID,
		})
	}

	ts := getChatToolbarState(s)
	_, height := c.Size()
	ui.Column(c.Key("chat-header-threads-menu")).FillWidth().Gap(Space1).Children(func() {
		searchList(c, searchListProps{
			Key:         "chat-header-threads",
			Label:       "搜索会话",
			Placeholder: "搜索会话…",
			Empty:       "无匹配会话",
			Items:       items,
			State:       &ts.threadsList,
			MaxHeight:   max(100, height-120),
			OnSelect: func(id string) {
				s.mu.Lock()
				current := s.selectedThreadID
				s.mu.Unlock()
				if id == current {
					ts.threadsPopoverOpen = false
					s.invalidate()
					return
				}
				s.headerSelectThread(id)
			},
			OnDismiss: func() {
				ts.threadsPopoverOpen = false
				s.invalidate()
			},
			Invalidate: s.invalidate,
		})
	})
}
