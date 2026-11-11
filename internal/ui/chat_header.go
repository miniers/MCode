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
)

var (
	chatHeaderMu     sync.Mutex
	chatHeaderStates = make(map[*chatState]*chatHeaderState)
)

// chatHeaderState 维护会话头部的标题编辑与 Git 胶囊状态，按 chatState 附加（同 diffPanelState）。
// editTitle、titleMenuOpen、branchMenuOpen、commitMenuOpen、permissionMenuOpen、openInMenuOpen、
// scriptsMenuOpen 绑定在控件上，只在 UI 线程读写；
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

	titleMenuOpen  bool
	branchMenuOpen bool
	commitMenuOpen bool
	closeMenus     bool

	// 标题栏项目动作
	permissionMenuOpen bool
	openInMenuOpen     bool
	scriptsMenuOpen    bool
	auxMenuOpen        bool

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

// renderChatHeaderTitle 渲染会话标题，单击进入编辑；回车或失焦提交，Esc 取消。
func renderChatHeaderTitle(c *ui.Context, s *chatState, th Thread) {
	hs := getChatHeaderState(s)

	chatHeaderMu.Lock()
	editing := hs.editing && hs.editThreadID == th.ID
	renameBusy := hs.renameBusy
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
		hs.editing = true
		hs.editThreadID = th.ID
		hs.editOrig = th.Title
		hs.editTitle = strings.TrimSpace(th.Title)
		hs.editHadFocus = false
		chatHeaderMu.Unlock()
		s.invalidate()
	}
	btn := ui.ButtonBase(c.Key("chat-header-title-btn")).
		Label("重命名会话").
		Disabled(renameBusy).
		Grow(1).
		Justify(ui.Start).
		Radius(RadiusSM).
		Padding(Space0, Space2)
	if btn.Hovered() {
		btn.Background(ThemeHover(c))
	}
	if btn.Clicked() {
		startEditing()
	}
	btn.ContextMenu(func(m *ui.Menu) {
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
	})
	btn.Children(func() {
		ui.Text(c, title).
			TextColor(textColor).
			FontSize(uiFont(TypeTitle)).
			FontWeight(600).
			SingleLine()
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
	if chip.Hovered() {
		chip.Background(ThemeAccent(c).Alpha(0.18))
	}
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
	if threadID == "" {
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
		} else {
			hs.git[threadID] = st
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

// renderChatHeaderGit 加载 Git 状态并渲染分支/Worktree 胶囊；提交按钮在右侧动作胶囊组里。
func renderChatHeaderGit(c *ui.Context, s *chatState, threadID string) {
	if threadID == "" {
		return
	}
	// PR 只读查看弹窗由 Git 菜单打开，每帧在这里渲染：与其它 Git 弹窗一样叠在界面上，
	// 关闭时自己从渲染树里消失。放在早期返回之前，保证分支胶囊不显示时弹窗仍能关闭。
	renderGitPRDialog(c, s)
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
		hs.permissionMenuOpen = false
		hs.openInMenuOpen = false
		hs.scriptsMenuOpen = false
		hs.pushConfirm = false
	}
	if hs.closeMenus {
		hs.closeMenus = false
		hs.titleMenuOpen = false
		hs.branchMenuOpen = false
		hs.commitMenuOpen = false
		hs.permissionMenuOpen = false
		hs.openInMenuOpen = false
		hs.scriptsMenuOpen = false
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
		return
	}

	renderGitBranchCapsule(c, s, hs, threadID, st)
}

// headerGitState 返回指定会话缓存的 Git 状态；尚未加载时返回零值。
func (s *chatState) headerGitState(threadID string) GitState {
	hs := getChatHeaderState(s)
	chatHeaderMu.Lock()
	defer chatHeaderMu.Unlock()
	return hs.git[threadID]
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

// renderChatHeaderActionGroup 渲染标题栏右侧的动作胶囊组，分为「打开方式」「执行命令」「Git 操作」
// 依次排列，相邻两组都非空时才在两组的交界处渲染一条分隔线。
func renderChatHeaderActionGroup(c *ui.Context, s *chatState, threadID string) {
	st := s.headerGitState(threadID)
	hasCommit := st.Branch != "" || st.Head != ""
	hasOpenIn := s.headerProjectDir(threadID) != ""
	hasScripts := s.headerScriptsButtonVisible(threadID)
	if !hasCommit && !hasOpenIn && !hasScripts {
		return
	}

	groups := []headerGroup{
		{hasOpenIn, func() { renderHeaderOpenInButton(c, s, threadID) }},
		{hasScripts, func() { renderHeaderScriptsMenuButton(c, s, threadID) }},
		{hasCommit, func() { renderGitCommitButton(c, s, getChatHeaderState(s), threadID, st) }},
	}

	ui.Row(c.Key("chat-header-actions-group")).
		Gap(Space0).
		Padding(headerCapsulePadding).
		Height(headerCapsuleHeight).
		Radius(CapsuleRadius).
		Background(ThemeOverlay(c, OverlaySubtle)).
		AlignItems(ui.Center).
		Children(func() {
			headerRenderDividerBetween(c, "chat-header-actions-divider", groups)
		})
}

// renderGitBranchCapsule 渲染分支胶囊及分支/Worktree 菜单：切换与新建分支在独立窗口里完成
// （见 git_form_windows_ui.go），Worktree 切换仍在菜单内直接执行。
func renderGitBranchCapsule(c *ui.Context, s *chatState, hs *chatHeaderState, threadID string, st GitState) {
	// 会话在自建 worktree 里干活时，改用该 worktree 的分支名并标注来源，Tooltip 带上完整路径。
	label := gitBranchLabel(st)
	aiWT := s.aiWorktree(threadID)
	if aiWT.Path != "" {
		name := aiWT.Branch
		if name == "" {
			name = filepath.Base(aiWT.Path)
		}
		label = name + " · AI Worktree"
	}

	trigger := Button(c.Key("chat-header-git-branch-btn"), ButtonOpts{
		Icon:  IconGitBranch(),
		Text:  label,
		Label: "Git 分支",
		OnClick: func() {
			hs.branchMenuOpen = !hs.branchMenuOpen
			hs.titleMenuOpen = false
			hs.commitMenuOpen = false
			chatHeaderMu.Lock()
			hs.branchErr = ""
			chatHeaderMu.Unlock()
			if hs.branchMenuOpen {
				s.loadGitState(threadID)
			}
		},
		Variant: ButtonSecondary,
		Size:    ControlSM,
	})
	if aiWT.Path != "" {
		trigger = trigger.Tooltip(label + "\n" + aiWT.Path)
	}

	chatHeaderMu.Lock()
	switching := hs.branchBusy
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
								ui.Text(c, "分支").
									FontSize(uiFont(TypeCaption)).
									Bold().
									TextColor(ThemeSecondaryText(c)).
									Padding(Space1, Space3, Space0, Space3)
								// 分支选择与新建都在独立窗口里完成，菜单里只留入口
								renderGitMenuOption(c, "chat-header-git-checkout-btn", "切换分支…", "切换分支…", "", false, busy, func() {
									hs.branchMenuOpen = false
									openGitForm(s, threadID, gitFormCheckout)
									s.invalidate()
								})
								renderGitMenuOption(c, "chat-header-git-new-branch-btn", "新建分支…", "新建分支…", "", false, busy, func() {
									hs.branchMenuOpen = false
									openGitForm(s, threadID, gitFormNewBranch)
									s.invalidate()
								})
								// 复制当前分支名（detached 时复制短哈希），方便在终端里直接使用
								copyBranch := st.Branch
								if copyBranch == "" {
									copyBranch = st.Head
								}
								if copyBranch != "" {
									renderGitMenuOption(c, "chat-header-git-copy-branch-btn", "复制分支名", "复制分支名", "", false, false, func() {
										hs.branchMenuOpen = false
										c.WriteClipboard(copyBranch)
										s.setToast("已复制分支名")
										s.invalidate()
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
										name := filepath.Base(wt.Path)
										renderGitMenuOption(c, "chat-header-git-wt-"+wt.Path, "切换到 worktree "+name, name, wt.Branch, wt.Current, busy, func() {
											run(func() error {
												return s.client.GitSwitchWorktree(context.Background(), threadID, wt.Path)
											})
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
										renderGitMenuOption(c, "chat-header-git-new-wt-btn", "为此会话新建 Worktree", "为此会话新建 Worktree", "", false, busy, func() {
											run(func() error {
												if err := s.client.MoveToWorktree(context.Background(), threadID, "", false); err != nil {
													return err
												}
												s.setToast("已迁入 Worktree")
												return nil
											})
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
														ui.Toggle(c.Key("chat-header-git-move-wt-transfer-check"), &hs.wtTransfer, "同时迁移未提交的改动")
														ui.Text(c, "同时迁移未提交的改动").
															FontSize(uiFont(TypeFootnote)).
															TextColor(ThemePrimaryText(c))
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
										renderGitMenuOption(c, "chat-header-git-move-wt-btn", "迁入 Worktree…", "迁入 Worktree…", "", false, busy, func() {
											chatHeaderMu.Lock()
											hs.wtMoving = true
											hs.wtBranch = ""
											hs.wtTransfer = true
											hs.branchErr = ""
											chatHeaderMu.Unlock()
											s.invalidate()
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

// renderGitMenuOption 渲染 Popover 中的一项，当前项打勾且不可点击。
func renderGitMenuOption(c *ui.Context, key, label, title, detail string, current, busy bool, onClick func()) {
	opt := ui.ButtonBase(c.Key(key)).
		Label(label).
		Padding(Space2, Space3).
		Radius(RadiusSM).
		FillWidth().
		Justify(ui.Start).
		AlignItems(ui.Center)
	if current {
		opt.Background(ThemeHover(c))
	} else if busy {
		opt.Disabled(true)
	} else {
		if opt.Hovered() {
			opt.Background(ThemeHover(c))
		}
		opt.OnClick(onClick)
	}
	opt.Children(func() {
		ui.Row(c.Key(key + "-row")).
			Gap(Space3).
			AlignItems(ui.Center).
			Grow(1).
			Children(func() {
				if current {
					ui.Icon(c, IconCheck()).Size(14, 14).TextColor(ThemeAccent(c))
				} else {
					ui.Box(c.Key(key+"-check-placeholder")).Size(14, 14)
				}
				ui.Text(c, title).
					FontSize(uiFont(TypeBody)).
					TextColor(ThemePrimaryText(c)).
					Grow(1).
					SingleLine()
				if detail != "" {
					ui.Text(c, detail).
						FontSize(uiFont(TypeCaption)).
						TextColor(ThemeSecondaryText(c)).
						MaxWidth(96).
						SingleLine()
				}
			})
	})
}

// renderGitCountBadge 渲染 Git 按钮右上角的改动数角标：用 Absolute 叠在按钮容器上，
// 不占布局宽度也不撑高胶囊；强调色底、白字，超过 99 显示 99+。
func renderGitCountBadge(c *ui.Context, key string, count int) {
	text := strconv.Itoa(count)
	if count > 99 {
		text = "99+"
	}
	ui.Row(c.Key(key)).
		Absolute().
		Top(-4).
		Right(-6).
		Height(IconSM).
		MinWidth(14).
		Padding(0, Space1).
		Radius(CapsuleRadius).
		Center().
		Background(ThemeAccent(c)).
		Children(func() {
			ui.Text(c, text).
				FontSize(uiFont(TypeCaption)).
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

// renderGitCommitButton 渲染提交与推送按钮及其 Popover。菜单结构：
// 标题「已更改 N 个文件」→ 提交…/推送/创建合并请求 → 抓取/拉取（快进）→ 合并…
// → 贮藏改动/恢复贮藏，各段之间用分隔线隔开。
func renderGitCommitButton(c *ui.Context, s *chatState, hs *chatHeaderState, threadID string, st GitState) {
	chatHeaderMu.Lock()
	busy := hs.commitBusy
	errMsg := hs.commitErr
	pushConfirm := hs.pushConfirm
	// 菜单未展开（关闭、切换会话或其他菜单顶掉）时不保留二次确认态
	if !hs.commitMenuOpen {
		hs.pushConfirm = false
		pushConfirm = false
	}
	chatHeaderMu.Unlock()

	// run 在后台执行提交、推送或同步类操作，name 用于显示进行中状态与失败提示；
	// 成功后关闭菜单，失败时把错误留在菜单里。
	run := func(name string, op func() error, onSuccess func()) {
		chatHeaderMu.Lock()
		if hs.commitBusy != "" || hs.branchBusy {
			chatHeaderMu.Unlock()
			return
		}
		hs.commitBusy = name
		hs.commitErr = ""
		generation := hs.gitGeneration
		chatHeaderMu.Unlock()
		s.invalidate()

		go func() {
			err := op()
			chatHeaderMu.Lock()
			if hs.gitGeneration == generation {
				hs.commitBusy = ""
				if err != nil {
					hs.commitErr = name + "失败: " + err.Error()
				} else {
					hs.closeMenus = true
					if onSuccess != nil {
						onSuccess()
					}
				}
			}
			chatHeaderMu.Unlock()
			s.loadGitState(threadID)
			s.invalidate()
		}()
	}

	changed := st.ChangedFiles
	if changed == 0 {
		changed = st.Changes
	}
	canRewrite := busy == "" && !s.isThreadRunning(threadID)
	others := gitOtherBranches(st)

	// 容器只包住按钮本身，改动数角标绝对定位叠在右上角，不参与布局。
	ui.Row(c.Key("chat-header-commit")).
		AlignItems(ui.Center).
		Children(func() {
			trigger := IconButton(c.Key("chat-header-commit-btn"), IconButtonProps{
				Icon:   IconArrowUpToLine(),
				Label:  "提交与推送",
				Active: hs.commitMenuOpen,
				OnClick: func() {
					hs.commitMenuOpen = !hs.commitMenuOpen
					hs.titleMenuOpen = false
					hs.branchMenuOpen = false
					chatHeaderMu.Lock()
					hs.commitErr = ""
					chatHeaderMu.Unlock()
					if hs.commitMenuOpen {
						s.loadGitState(threadID)
					}
				},
				Variant: ButtonGhost,
				Size:    ControlSM,
			})
			if st.Changes > 0 {
				renderGitCountBadge(c, "chat-header-commit-badge", st.Changes)
			}

			appPopover(c.Key("chat-header-commit-popover"), trigger, &hs.commitMenuOpen, func() {
				hs.commitMenuOpen = false
			}, func() {
				popoverCard(c, ui.Column(c.Key("chat-header-commit-menu"))).
					Gap(Space0).
					Padding(Space2).
					Width(gitMenuWidth).
					Children(func() {
						ui.Text(c, fmt.Sprintf("已更改 %d 个文件", changed)).
							FontSize(uiFont(TypeFootnote)).
							FontWeight(600).
							TextColor(ThemePrimaryText(c)).
							Padding(Space1, Space3)

						gitMenuDivider(c)

						renderGitMenuBranch1(c, s, hs, threadID, st, gitMenuRun{run: run, busy: busy})
						if pushConfirm {
							renderGitPushPRConfirm(c, s, st)
						}
						gitMenuDivider(c)
						renderGitMenuBranch2(c, s, hs, threadID, st, gitMenuRun{run: run, busy: busy, canRewrite: canRewrite, others: others})

						if busy != "" {
							ui.Row(c.Key("chat-header-commit-busy")).
								Gap(Space2).
								Padding(Space2, Space3).
								AlignItems(ui.Center).
								Children(func() {
									LoadingIndicator(c, nil, busy+"中…", IconSM)
								})
						}
						if errMsg != "" {
							renderGitError(c, "chat-header-commit-error", errMsg)
						}
					})
			})
		})
}

// renderGitMenuBranch1 渲染提交、推送与合并请求相关操作。
func renderGitMenuBranch1(c *ui.Context, s *chatState, hs *chatHeaderState, threadID string, st GitState, m gitMenuRun) {
	// 提交…：无改动时禁用，点击在独立窗口里填写提交信息
	renderGitMenuItem(c, "chat-header-git-commit-item", gitMenuItemProps{
		Label:    "提交…",
		Icon:     IconGitCommitVertical(),
		Disabled: m.busy != "" || st.Changes == 0,
		OnClick: func() {
			hs.commitMenuOpen = false
			openGitForm(s, threadID, gitFormCommit)
			s.invalidate()
		},
	})
	renderGitMenuItem(c, "chat-header-git-push-item", gitMenuItemProps{
		Label:    "推送",
		Icon:     IconArrowUpToLine(),
		Busy:     m.busy == "推送",
		Disabled: m.busy != "" || st.Branch == "",
		OnClick: func() {
			m.run("推送", func() error {
				return s.client.GitPush(context.Background(), threadID)
			}, nil)
		},
	})
	renderGitMenuItem(c, "chat-header-git-pr-item", gitMenuItemProps{
		Label:    "创建合并请求…",
		Icon:     IconGitPullRequestCreate(),
		Disabled: normalizeGitRemoteURL(st.RemoteURL) == "",
		OnClick: func() {
			hs.commitMenuOpen = false
			openGitForm(s, threadID, gitFormPullRequest)
			s.invalidate()
		},
	})
	chatHeaderMu.Lock()
	pushBusy := hs.pushBusy
	chatHeaderMu.Unlock()
	renderGitMenuItem(c, "chat-header-push-pr-btn", gitMenuItemProps{
		Label:    "推送并建 PR/MR",
		Icon:     IconGitPullRequestCreate(),
		Busy:     pushBusy,
		Disabled: m.busy != "" || pushBusy,
		OnClick:  s.requestPushPRConfirm,
	})
	// 查看 PR/MR 是只读操作：不限平台、也不看工作区是否有改动，
	// 没有远端或 CLI 不可用时由弹窗给出具体原因；放在写操作之后。
	renderGitMenuItem(c, "chat-header-git-pr-view-item", gitMenuItemProps{
		Label: "查看 Pull Request…",
		Icon:  IconExternalLink(),
		OnClick: func() {
			hs.commitMenuOpen = false
			s.openGitPRDialog()
		},
	})
	if repoURL := normalizeGitRemoteURL(st.RemoteURL); repoURL != "" {
		renderGitMenuItem(c, "chat-header-open-repo-btn", gitMenuItemProps{
			Label: "打开仓库",
			Icon:  IconExternalLink(),
			OnClick: func() {
				hs.commitMenuOpen = false
				c.OpenURL(repoURL)
				s.invalidate()
			},
		})
	}
}

// renderGitMenuBranch2 渲染抓取/拉取、快进合并/合并与贮藏两组操作及分支选择器。
func renderGitMenuBranch2(c *ui.Context, s *chatState, hs *chatHeaderState, threadID string, st GitState, m gitMenuRun) {
	renderGitMenuItem(c, "chat-header-git-fetch-item", gitMenuItemProps{
		Label:    "抓取",
		Icon:     IconRefreshCw(),
		Busy:     m.busy == "抓取",
		Disabled: m.busy != "",
		OnClick: func() {
			m.run("抓取", func() error {
				return s.client.GitFetch(context.Background(), threadID)
			}, func() { s.setToast("已抓取远程分支") })
		},
	})
	pullLabel := "拉取（快进）"
	if st.Behind > 0 {
		pullLabel = fmt.Sprintf("拉取（快进） ↓%d", st.Behind)
	}
	// 不依赖 CanFastForward：本地还没抓取时 AheadBehind 看不到落后，点击时仍会先 fetch 再快进
	renderGitMenuItem(c, "chat-header-git-pull-item", gitMenuItemProps{
		Label:    pullLabel,
		Icon:     IconArrowDownCircle(),
		Busy:     m.busy == "拉取",
		Disabled: m.busy != "" || !m.canRewrite || !st.HasUpstream,
		OnClick: func() {
			m.run("拉取", func() error {
				return s.client.GitPull(context.Background(), threadID)
			}, func() { s.setToast("已快进拉取") })
		},
	})

	gitMenuDivider(c)

	renderGitMenuItem(c, "chat-header-git-merge-item", gitMenuItemProps{
		Label:    "合并…",
		Icon:     IconMerge(),
		Disabled: m.busy != "" || len(m.others) == 0,
		OnClick: func() {
			hs.commitMenuOpen = false
			openGitForm(s, threadID, gitFormMerge)
			s.invalidate()
		},
	})

	gitMenuDivider(c)

	changed := st.ChangedFiles
	if changed == 0 {
		changed = st.Changes
	}
	renderGitMenuItem(c, "chat-header-git-stash-item", gitMenuItemProps{
		Label:    "贮藏改动",
		Icon:     IconPackage(),
		Busy:     m.busy == "贮藏改动",
		Disabled: !m.canRewrite || changed == 0,
		OnClick: func() {
			m.run("贮藏改动", func() error {
				return s.client.GitStash(context.Background(), threadID)
			}, func() { s.setToast("已贮藏改动") })
		},
	})
	renderGitMenuItem(c, "chat-header-git-stash-pop-item", gitMenuItemProps{
		Label:    "恢复贮藏",
		Icon:     IconPackage(),
		Busy:     m.busy == "恢复贮藏",
		Disabled: !m.canRewrite || !st.HasStash,
		OnClick: func() {
			m.run("恢复贮藏", func() error {
				return s.client.GitStashPop(context.Background(), threadID)
			}, func() { s.setToast("已恢复贮藏") })
		},
	})
}

// renderGitPushPRConfirm 渲染「推送并建 PR/MR」的二次确认区，确认后才真正推送。
func renderGitPushPRConfirm(c *ui.Context, s *chatState, st GitState) {
	ui.Column(c.Key("chat-header-push-pr-confirm")).
		FillWidth().
		Gap(Space2).
		Padding(Space0, Space3).
		Children(func() {
			ui.Text(c, fmt.Sprintf("将推送当前分支 %s 到远端，并用 gh/glab 创建 PR/MR。确认继续？", gitBranchLabel(st))).
				FontSize(uiFont(TypeFootnote)).
				TextColor(ThemeSecondaryText(c))
			ui.Row(c.Key("chat-header-push-pr-confirm-actions")).
				Gap(Space2).
				AlignItems(ui.Center).
				Children(func() {
					Button(c.Key("chat-header-push-pr-confirm-ok"), ButtonOpts{
						Text: "确认推送",
						OnClick: func() {
							s.cancelPushPRConfirm()
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
						Text: "取消",
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

// gitMenuItemHeight 是 Git 菜单单项的行高；gitMenuWidth 是菜单宽度。
const (
	gitMenuItemHeight = 26
	gitMenuWidth      = 248 // 菜单宽度：窄到 240~260，长文本靠截断显示
)

// gitMenuRun 汇总 Git 菜单渲染所需的运行状态，避免把大量参数逐个传递。
type gitMenuRun struct {
	run        func(name string, op func() error, onSuccess func()) // 后台执行操作
	busy       string                                               // 进行中的操作名
	canRewrite bool                                                 // 会话空闲且没有进行中的操作，允许改写工作区
	others     []string                                             // 除当前分支外的本地分支
}

// gitMenuItemProps 描述 Git 菜单中的单个操作项。
type gitMenuItemProps struct {
	Label    string
	Icon     *ui.SVG
	Busy     bool
	Disabled bool
	OnClick  func()
}

// renderGitMenuItem 渲染 Git 菜单中的一项：左侧 16pt 图标、26 行高，内容靠左，长文本截断，
// hover 高亮，禁用时 Opacity(0.4)。
func renderGitMenuItem(c *ui.Context, key string, p gitMenuItemProps) {
	item := ui.ButtonBase(c.Key(key)).
		Label(p.Label).
		Gap(Space3).
		Padding(0, Space3).
		Radius(RadiusSM).
		FillWidth().
		Justify(ui.Start).
		AlignItems(ui.Center).
		Height(gitMenuItemHeight)
	if p.Disabled || p.Busy {
		item.Disabled(true)
	} else {
		if item.Hovered() {
			item.Background(ThemeHover(c))
		}
		if p.OnClick != nil {
			item.OnClick(p.OnClick)
		}
	}
	if p.Disabled {
		item.Opacity(0.4)
	}
	item.Children(func() {
		if p.Busy {
			LoadingIndicator(c, nil, "", IconSM)
		} else if p.Icon != nil {
			ui.Icon(c, p.Icon).Size(16, 16).TextColor(ThemeSecondaryText(c))
		} else {
			ui.Box(c.Key(key+"-icon-placeholder")).Size(16, 16)
		}
		ui.Text(c, p.Label).
			FontSize(uiFont(TypeBody)).
			TextColor(ThemePrimaryText(c)).
			Grow(1).
			SingleLine()
	})
}

// gitMenuDivider 是 Git 菜单内的水平分隔线。
func gitMenuDivider(c *ui.Context) {
	ui.Box(c).
		Height(Hairline).
		FillWidth().
		Margin(4, 0, 4, 0).
		Background(ThemeBorder(c))
}

// gitOtherBranches 返回除当前分支外的所有本地分支，用作合并选择器的候选项。
func gitOtherBranches(st GitState) []string {
	out := make([]string, 0, len(st.Branches))
	for _, b := range st.Branches {
		if b == "" || b == st.Branch {
			continue
		}
		out = append(out, b)
	}
	return out
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
// 标题栏项目动作：权限圆钮、在其它应用中打开与脚本菜单
// ---------------------------------------------------------------------------

// headerGroup 描述标题栏胶囊组内的一个按钮分组：visible 为假的分组不渲染。
type headerGroup struct {
	visible bool
	render  func()
}

// headerRenderDividerBetween 按顺序渲染分组，只在两组都非空且相邻时插入一条分隔线，
// 不会出现在组的首尾，也不会紧挨着空组。
func headerRenderDividerBetween(c *ui.Context, dividerKey string, groups []headerGroup) {
	rendered := 0
	for _, g := range groups {
		if !g.visible {
			continue
		}
		if rendered > 0 {
			ui.Box(c.Key(dividerKey+"-"+strconv.Itoa(rendered))).
				Width(Hairline).
				Height(IconSM).
				Background(ThemeHairline(c)).
				Margin(0, 2, 0, 2)
		}
		g.render()
		rendered++
	}
}

// closeProjectActionMenus 关闭标题栏项目动作的三个弹层，keep 指定的那个保持原样（"" 表示全部关闭）。
func closeProjectActionMenus(hs *chatHeaderState, keep string) {
	if keep != "permission" {
		hs.permissionMenuOpen = false
	}
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
	// 待创建会话没有会话行：档位取自本地占位会话。
	if p := s.pendingThread; p != nil && threadID == "" && p.Thread.RuntimeMode != "" {
		return p.Thread.RuntimeMode
	}
	return "auto"
}

// threadProvider 返回指定会话的 provider ID，找不到时返回空字符串。
func (s *chatState) threadProvider(threadID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 待创建会话没有会话行：provider 取自本地占位会话。
	if p := s.pendingThread; p != nil && threadID == "" {
		return p.Thread.Provider
	}
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
	// 待创建会话没有会话行：模型取自本地占位会话。
	if p := s.pendingThread; p != nil && threadID == "" {
		return p.Thread.Model
	}
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
	// 待创建会话没有会话行：人设取自本地占位会话。
	if p := s.pendingThread; p != nil && threadID == "" {
		return p.Thread.Persona
	}
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

// permissionModeIcon 返回权限档位对应的图标（与设置页、composer 保持一致）。
func permissionModeIcon(mode string) *ui.SVG {
	switch mode {
	case "supervised":
		return IconUserRoundCheck()
	case "auto":
		return IconSparkles()
	case "fullAccess":
		return IconLockOpen()
	default:
		return IconSquarePen()
	}
}

// renderHeaderPermissionCapsule 渲染 32×32 权限圆钮与三档单行菜单；菜单底部是当前 provider/model 的人设分区。
func renderHeaderPermissionCapsule(c *ui.Context, s *chatState, threadID string) {
	mode := s.threadRuntimeMode(threadID)
	if mode == "autoAcceptEdits" || mode == "agent" {
		mode = "auto"
	}
	title := runtimeModeTitle(mode)
	summary := runtimeModeSummary(mode)
	provider := s.threadProvider(threadID)
	model := s.threadModel(threadID)
	hs := getChatHeaderState(s)
	// 首次渲染时补拉人设：已加载、正在加载或还在退避期时由 loadHeaderPersonas 自己跳过，不会按帧重复发起。
	s.loadHeaderPersonas(provider, model, false)

	label := title
	if summary != "" {
		label = title + " · " + summary
	}
	trigger := IconButton(c.Key("chat-header-permission-btn"), IconButtonProps{
		Icon:   permissionModeIcon(mode),
		Label:  label,
		Active: hs.permissionMenuOpen,
		OnClick: func() {
			hs.permissionMenuOpen = !hs.permissionMenuOpen
			closeProjectActionMenus(hs, "permission")
			hs.titleMenuOpen = false
			hs.branchMenuOpen = false
			hs.commitMenuOpen = false
			if hs.permissionMenuOpen {
				// 打开菜单时强制刷新一次人设，网关上新增的人设不必等下次重启就能看到。
				s.loadHeaderPersonas(provider, model, true)
			}
			s.invalidate()
		},
		Variant: ButtonSecondary,
		Size:    ControlSM,
	})

	appPopover(c.Key("chat-header-permission-popover"), trigger, &hs.permissionMenuOpen, func() {
		hs.permissionMenuOpen = false
	}, func() {
		personas := s.headerPersonas(provider, model)
		current := s.threadPersona(threadID)
		_, height := c.Size()
		popoverCard(c, ui.Column(c.Key("chat-header-permission-menu")).
			Padding(Space2).
			MinWidth(210).
			Children(func() {
				// 人设很多时菜单会顶出窗口：与 Git 菜单一样套一层滚动区，内容不超限时不影响视觉。
				ui.Scroll(c.Key("chat-header-permission-scroll")).
					MaxHeight(max(100, height-120)).
					Children(func() {
						ui.Column(c.Key("chat-header-permission-menu-list")).
							Gap(Space0).
							FillWidth().
							Children(func() {
								ui.Text(c, "权限").
									FontSize(uiFont(TypeCaption)).
									FontWeight(600).
									TextColor(ThemeSecondaryText(c)).
									Padding(Space2, Space3, Space1, Space3)
								for _, opt := range runtimeModeOptions {
									o := opt
									currentMode := o.mode == mode
									supported := headerRuntimeModeSupported(provider, o.mode)
									optBtn := ui.ButtonBase(c.Key("chat-header-permission-option-"+o.mode)).
										Label(o.title).
										Height(ControlMD).
										Padding(0, Space3).
										Radius(RadiusSM).
										FillWidth()
									if !supported {
										optBtn.Disabled(true)
										optBtn.Opacity(0.4)
									} else {
										if optBtn.Hovered() {
											optBtn.Background(ThemeHover(c))
										}
										optBtn.OnClick(func() {
											// 先关菜单再切换档位
											hs.permissionMenuOpen = false
											s.invalidate()
											s.setHeaderRuntimeMode(threadID, o.mode)
										})
									}
									optBtn.Children(func() {
										ui.Row(c.Key("chat-header-permission-row-" + o.mode)).
											Gap(Space3).
											AlignItems(ui.Center).
											Grow(1).
											Children(func() {
												if currentMode {
													ui.Icon(c, IconCheck()).Size(14, 14).TextColor(ThemeAccent(c))
												} else {
													ui.Box(c.Key("chat-header-permission-check-"+o.mode)).Size(14, 14)
												}
												ui.Icon(c, permissionModeIcon(o.mode)).Size(16, 16).TextColor(ThemeSecondaryText(c))
												ui.Text(c, o.title).
													FontSize(uiFont(TypeBody)).
													TextColor(ThemePrimaryText(c)).
													SingleLine()
											})
									})
								}
								renderHeaderPersonaSection(c, s, hs, threadID, current, personas)
							})
					})
			}),
		)
	})
}

// renderHeaderPersonaSection 渲染权限菜单底部的人设分区：分隔线、小标题、「无人设」与各人设项。
// 没有任何人设时整块（含分隔线与标题）都不渲染，菜单与加人设分区之前完全一致。
func renderHeaderPersonaSection(c *ui.Context, s *chatState, hs *chatHeaderState, threadID, current string, personas []MagpiePersona) {
	if len(personas) == 0 {
		return
	}
	gitMenuDivider(c)
	ui.Text(c, "人设").
		FontSize(uiFont(TypeCaption)).
		FontWeight(600).
		TextColor(ThemeSecondaryText(c)).
		Padding(Space2, Space3, Space1, Space3)
	renderHeaderPersonaOption(c, s, hs, threadID, current, personaNoneValue, "无人设", personas)
	for _, p := range personas {
		// 空 id 与保留字 none 不能当人设项：none 已占用「无人设」项的 key。
		if p.ID == "" || p.ID == personaNoneValue {
			continue
		}
		name := p.Name
		if name == "" {
			name = p.ID
		}
		renderHeaderPersonaOption(c, s, hs, threadID, current, p.ID, name, personas)
	}
}

// renderHeaderPersonaOption 渲染单个菜单项：写法与权限档位项一致，26 行高、左侧选中勾与未选中占位。
// id 传 personaNoneValue 表示「无人设」项；点击先关菜单，再按三态语义写入当前会话的人设。
func renderHeaderPersonaOption(c *ui.Context, s *chatState, hs *chatHeaderState, threadID, current, id, label string, personas []MagpiePersona) {
	key := "chat-header-persona-option-" + id
	checked := personaMenuChecked(current, id, personas)
	item := ui.ButtonBase(c.Key(key)).
		Label(label).
		Height(ControlMD).
		Padding(0, Space3).
		Radius(RadiusSM).
		FillWidth()
	if item.Hovered() {
		item.Background(ThemeHover(c))
	}
	item.OnClick(func() {
		// 先关菜单再写入
		hs.permissionMenuOpen = false
		s.invalidate()
		value, ok := personaMenuWriteValue(current, id)
		if !ok {
			return
		}
		s.updateComposerPrefs(threadID, func(prefs *ComposerPrefs) {
			prefs.Persona = value
		}, "切换人设失败")
	})
	item.Children(func() {
		ui.Row(c.Key(key + "-row")).
			Gap(Space3).
			AlignItems(ui.Center).
			Grow(1).
			Children(func() {
				if checked {
					ui.Icon(c, IconCheck()).Size(14, 14).TextColor(ThemeAccent(c))
				} else {
					ui.Box(c.Key(key+"-check")).Size(14, 14)
				}
				ui.Text(c, label).
					FontSize(uiFont(TypeBody)).
					TextColor(ThemePrimaryText(c)).
					SingleLine()
			})
	})
}

// setHeaderRuntimeMode 切换指定会话的权限档位并持久化，成功后更新本地会话列表。
func (s *chatState) setHeaderRuntimeMode(threadID, mode string) {
	if mode == "" {
		return
	}
	s.mu.Lock()
	pending := s.isPendingThreadLocked(threadID)
	if pending {
		// 待创建会话没有后端会话可写：先记下，创建后补写。
		s.setPendingRuntimeModeLocked(mode)
		s.mu.Unlock()
		s.invalidate()
		return
	}
	s.mu.Unlock()
	go func() {
		if err := s.client.SetThreadRuntimeMode(threadID, mode); err != nil {
			s.setToast("设置权限模式失败: " + err.Error())
			return
		}
		s.mu.Lock()
		for i := range s.threads {
			if s.threads[i].ID == threadID {
				s.threads[i].RuntimeMode = mode
				break
			}
		}
		for _, list := range s.threadsByProj {
			for i := range list {
				if list[i].ID == threadID {
					list[i].RuntimeMode = mode
					break
				}
			}
		}
		s.mu.Unlock()
		s.invalidate()
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
func openInWithApp(app openInApp, dir string) {
	if dir == "" {
		return
	}
	path := strings.TrimSpace(app.AppPath)
	if openInGOOS == "windows" {
		openInWithAppWindows(path, dir)
		return
	}
	bundle := strings.TrimSpace(app.BundleID)
	bundleFallback := bundle != "" && runtime.GOOS == "darwin"
	if path == "" {
		if bundleFallback {
			_ = openInRunner("-b", bundle, dir)
			return
		}
		_ = openInRunner(dir)
		return
	}
	if !openInStatFn(path) && bundleFallback {
		_ = openInRunner("-b", bundle, dir)
		return
	}
	_ = openInRunner("-a", path, dir)
}

// openInWithAppWindows 在 Windows 上用资源管理器或应用可执行文件打开目录。
// 可执行文件路径有效时直接启动，路径缺失或不是可执行文件时退回资源管理器。
func openInWithAppWindows(path, dir string) {
	if path != "" && (strings.EqualFold(filepath.Ext(path), ".exe") || openInStatFn(path)) {
		_ = openInLaunchRunner(path, dir)
		return
	}
	_ = openInLaunchRunner("explorer.exe", dir)
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
	for _, th := range s.threads {
		if th.ID == selectedID {
			selectedThread = th
			break
		}
	}
	s.mu.Unlock()

	ts := getChatToolbarState(s)
	header := ui.Row(c.Key("chat-header")).DragWindow().FillWidth().Height(HeaderHeight).
		Padding(Space3, chatHeaderBaseInset+titleBarTrailingInset(c), Space3, chatHeaderLeadingInsetFrom(titleBarLeadingInset(c), sidebarSlide)).
		BorderWidth(0, 0, 1, 0).BorderColor(ThemeBorder(c)).AlignItems(ui.Center).Gap(Space2)
	windowWidth, _ := c.Size()
	collapsed := headerCollapseLevel(headerCollapseWidth(windowWidth, sidebarWidth, sidebarSlide)) > 0
	header.Children(func() {
		// 左侧依次是父芯片、侧栏开关、会话列表（侧栏收起时）、新建会话、编排、权限与分支胶囊，
		// 之后是占满剩余宽度的标题；右侧只留动作胶囊组与辅助面板胶囊组。
		ui.Row(c.Key("chat-header-left")).Gap(Space4).AlignItems(ui.Center).Grow(1).Children(func() {
			renderChatHeaderParentChip(c, s)
			IconButton(c, IconButtonProps{Key: "chat-header-toggle-sidebar-btn", Icon: IconPanelLeft(), Size: ControlMD, Variant: ButtonGhost,
				Label: "显示/隐藏侧边栏 ⌘B", OnClick: func() { toggleSidebarPersisted(s) }})
			if sidebarCollapsed {
				threadsBtn := IconButton(c, IconButtonProps{Key: "chat-header-threads-btn", Icon: IconChevronDown(), Size: ControlMD, Variant: ButtonGhost,
					Label: "会话列表", Active: ts.threadsPopoverOpen, OnClick: func() { s.headerToggleThreadsPopover() }})
				PopoverCard(c, PopoverProps{Key: "chat-header-threads-popover", Anchor: threadsBtn, Open: &ts.threadsPopoverOpen, Width: chatThreadsPopoverWidth}, func() {
					renderChatThreadsPopover(c, s)
				})
			}
			IconButton(c, IconButtonProps{Key: "chat-header-new-thread-btn", Icon: IconMessageSquarePlus(), Size: ControlMD, Variant: ButtonGhost,
				Label: "新建会话 ⌘N", OnClick: s.headerNewThread})
			renderSwarmHeaderButton(c, s)
			renderHeaderPermissionCapsule(c, s, selectedID)
			renderChatHeaderGit(c, s, selectedID)
			renderChatHeaderTitle(c, s, selectedThread)
		})
		ui.Row(c.Key("chat-header-right")).Gap(Space2).AlignItems(ui.Center).Children(func() {
			if stewardSelected(s, selectedID) && !stewardBoardWide(chatAreaWidth(c, s)) {
				renderStewardBoardBadge(c, s)
			}
			renderChatHeaderActionGroup(c, s, selectedID)
			if collapsed {
				more := IconButton(c, IconButtonProps{Key: "chat-header-more-btn", Icon: IconMoreHorizontal(), Size: ControlMD, Variant: ButtonGhost,
					Label: "更多头部操作", Active: ts.morePopoverOpen, OnClick: func() { ts.morePopoverOpen = !ts.morePopoverOpen }})
				PopoverCard(c, PopoverProps{Key: "chat-header-more-popover", Anchor: more, Open: &ts.morePopoverOpen, Align: BelowEnd}, func() {
					renderChatHeaderAuxGroup(c, s)
				})
			} else {
				renderChatHeaderAuxGroup(c, s)
			}
			if stewardSelected(s, selectedID) {
				nickname, _ := s.stewardIdentity()
				IconButton(c, IconButtonProps{Key: "chat-header-steward-settings", Icon: IconSettings(), Size: ControlMD, Variant: ButtonGhost,
					Label: stewardName(nickname) + "设置", OnClick: func() { openSettingsWindow(s, "steward") }})
			}
		})
	})
}

const chatThreadsPopoverWidth float32 = 320 // token-exempt: 会话列表需要足够宽度展示长标题。

// chatToolbarState 维护会话头部工具栏的弹层状态，按 chatState 附加（同 chatHeaderState）。
// threadsPopoverOpen 绑定在会话列表 Popover 上，只在 UI 线程读写。
type chatToolbarState struct {
	threadsPopoverOpen bool
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

// headerToggleThreadsPopover 切换会话列表 Popover 的展开状态。
func (s *chatState) headerToggleThreadsPopover() {
	if s == nil {
		return
	}
	ts := getChatToolbarState(s)
	ts.threadsPopoverOpen = !ts.threadsPopoverOpen
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

// headerOpenKanban 切换到看板（任务）视图。
func (s *chatState) headerOpenKanban() {
	if s == nil {
		return
	}
	s.openTasksWindow("tasks")
}

// headerToggleDiff 切换改动面板。
func (s *chatState) headerToggleDiff() {
	if s == nil {
		return
	}
	s.toggleDiffPanel()
}

// renderChatHeaderAuxGroup 渲染终端、右栏和更多入口。
func renderChatHeaderAuxGroup(c *ui.Context, s *chatState) {
	hs := getChatHeaderState(s)
	pendingProposals := memoryPendingBadgeCount(s)
	s.refreshPendingMemoryCount(false)
	// 原生菜单在窗口创建前安装，那时还拿不到 client，这里补一次入口文案纠正。
	syncMemoryMenuLabel()

	terminalOpen := false
	if termSt := getTerminalState(s); termSt != nil {
		terminalMu.Lock()
		terminalOpen = termSt.visible
		terminalMu.Unlock()
	}

	inspector := inspectorSession(s)
	inspectorOpen := inspector != nil && inspector.Open

	// 与动作胶囊组同一套容器：内边距 2、固定高度 28，相邻按钮之间插发丝分隔线。
	groups := []headerGroup{
		{true, func() {
			IconButton(c, IconButtonProps{Key: "chat-header-terminal-btn", Icon: IconTerminal(), Size: headerCapsuleButtonSize, Variant: ButtonGhost, Label: "终端", Tooltip: shortcutTitleWithKey("toggle-terminal", "终端"), Active: terminalOpen, OnClick: s.headerToggleTerminal})
		}},
		{true, func() {
			IconButton(c, IconButtonProps{Key: "chat-header-inspector-btn", Icon: IconPanelRight(), Size: headerCapsuleButtonSize, Variant: ButtonGhost, Label: "右栏", Tooltip: shortcutTitleWithKey("toggle-inspector", "右栏"), Active: inspectorOpen, OnClick: func() { toggleInspector(s) }})
		}},
		{true, func() {
			ui.Row(c.Key("chat-header-aux-more-wrap")).Children(func() {
				trigger := IconButton(c, IconButtonProps{Key: "chat-header-aux-more-btn", Icon: IconMoreHorizontal(), Size: headerCapsuleButtonSize, Variant: ButtonGhost, Label: "更多工具", Tooltip: "更多", Active: hs.auxMenuOpen, OnClick: func() { hs.auxMenuOpen = !hs.auxMenuOpen }})
				if pendingProposals > 0 {
					renderGitCountBadge(c, "chat-header-memory-badge", pendingProposals)
				}
				appPopover(c.Key("chat-header-aux-more-popover"), trigger, &hs.auxMenuOpen, nil, func() {
					popoverCard(c, ui.Column(c.Key("chat-header-aux-more-menu")).Width(180).Padding(Space1).Children(func() {
						item := func(key, title, detail string, action func()) {
							renderGitMenuOption(c, key, title, title, detail, false, false, func() { hs.auxMenuOpen = false; action() })
						}
						item("chat-header-context-btn", "项目文档", "", s.headerToggleContextPanel)
						detail := ""
						if pendingProposals > 0 {
							detail = strconv.Itoa(pendingProposals)
						}
						item("chat-header-memory-btn", memoryEntryLabel(s.client), detail, s.headerToggleMemoryPanel)
						item("chat-header-kanban-btn", "任务", "", s.headerOpenKanban)
					}))
				})
			})
		}},
	}

	ui.Row(c.Key("chat-header-aux-group")).
		Gap(Space0).
		Padding(headerCapsulePadding).
		Height(headerCapsuleHeight).
		Radius(CapsuleRadius).
		Background(ThemeOverlay(c, OverlaySubtle)).
		AlignItems(ui.Center).
		Children(func() {
			headerRenderDividerBetween(c, "chat-header-aux-divider", groups)
		})
}

// renderChatThreadsPopover 渲染会话列表 Popover：按当前会话列表顺序列出标题，当前会话打勾。
func renderChatThreadsPopover(c *ui.Context, s *chatState) {
	s.mu.Lock()
	threads := make([]Thread, len(s.threads))
	copy(threads, s.threads)
	selectedID := s.selectedThreadID
	s.mu.Unlock()

	_, height := c.Size()
	ui.Scroll(c.Key("chat-header-threads-scroll")).MaxHeight(max(100, height-120)).Children(func() {
		ui.Column(c.Key("chat-header-threads-menu")).FillWidth().Children(func() {
			if len(threads) == 0 {
				ui.Text(c, "暂无会话").FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c)).Padding(Space2, Space3)
				return
			}
			for _, th := range threads {
				renderChatThreadMenuItem(c, s, th, th.ID == selectedID)
			}
		})
	})
}

// renderChatThreadMenuItem 渲染会话列表 Popover 的一项：当前会话打勾且不可点击，其余点击切换会话并关闭弹层。
func renderChatThreadMenuItem(c *ui.Context, s *chatState, th Thread, current bool) {
	title := strings.TrimSpace(th.Title)
	if title == "" {
		title = "未命名会话"
	}
	opt := ui.ButtonBase(c.Key("chat-header-threads-item-"+th.ID)).
		Label("切换到会话 "+title).
		Height(ControlSM).
		Padding(0, Space3).
		Radius(RadiusSM).
		FillWidth()
	if current {
		opt.Background(ThemeOverlaySelected(c))
	} else {
		if opt.Hovered() {
			opt.Background(ThemeHover(c))
		}
		if opt.Clicked() {
			s.headerSelectThread(th.ID)
		}
	}
	opt.Children(func() {
		ui.Row(c.Key("chat-header-threads-item-" + th.ID + "-row")).
			Gap(Space3).
			AlignItems(ui.Center).
			Grow(1).
			Children(func() {
				if current {
					ui.Icon(c, IconCheck()).Size(14, 14).TextColor(ThemeAccent(c))
				} else {
					ui.Box(c.Key("chat-header-threads-item-"+th.ID+"-check-placeholder")).Size(14, 14)
				}
				ui.Text(c, title).
					FontSize(uiFont(TypeBody)).
					TextColor(ThemePrimaryText(c)).
					SingleLine()
			})
	})
}
