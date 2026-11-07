package mcodeui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// archivePageState 维护归档页的运行时会话缓存与弹窗交互状态。
type archivePageState struct {
	mu             sync.Mutex
	loaded         bool
	loading        bool
	attempted      bool   // 自动加载已尝试过：失败后不再逐帧重试，交给用户点「重试」
	loadErr        string // 最近一次加载失败的原因
	pendingReload  bool   // 加载中收到的刷新请求，本轮结束后补拉一次（S-112）
	threads        []Thread
	projects       map[string]string // projectID -> projectName
	deleteTarget   *Thread
	deleteModal    bool
	deleteAllModal bool

	// opErr 保存单条恢复/删除失败的原因，在「已归档会话」区块内展示（S-109）。
	opErr string
	// deletingAll 表示「全部删除」正在执行，期间禁用按钮，避免重复连点并发删除（S-111）。
	deletingAll bool
	// deleteAllResult/deleteAllDeleted/deleteAllFailed 是「全部删除」的结果汇总与逐条失败原因（S-111）。
	deleteAllResult  string
	deleteAllDeleted int
	deleteAllFailed  []string

	// query 是归档列表的搜索词（匹配标题与项目名，不区分大小写）。
	query string
	// picked 是多选状态：会话 ID → 是否选中；批量操作失败项会保持选中。
	picked map[string]bool
	// bulkRunning 表示批量恢复/删除正在执行，期间禁用批量按钮。
	bulkRunning bool
	// bulkResult/bulkFailed 是批量操作的结果汇总与逐条失败原因。
	bulkResult string
	bulkFailed []string
	// bulkDeleteConfirm 表示「批量删除」的确认弹窗已打开。
	bulkDeleteConfirm bool
}

var globalArchiveState archivePageState

// resetArchivePageStateForTest 重置归档页状态，供单元测试隔离使用。
func resetArchivePageStateForTest() {
	globalArchiveState.mu.Lock()
	defer globalArchiveState.mu.Unlock()
	globalArchiveState.loaded = false
	globalArchiveState.loading = false
	globalArchiveState.attempted = false
	globalArchiveState.loadErr = ""
	globalArchiveState.pendingReload = false
	globalArchiveState.threads = nil
	globalArchiveState.projects = nil
	globalArchiveState.deleteTarget = nil
	globalArchiveState.deleteModal = false
	globalArchiveState.deleteAllModal = false
	globalArchiveState.opErr = ""
	globalArchiveState.deletingAll = false
	globalArchiveState.deleteAllResult = ""
	globalArchiveState.deleteAllDeleted = 0
	globalArchiveState.deleteAllFailed = nil
	globalArchiveState.query = ""
	globalArchiveState.picked = nil
	globalArchiveState.bulkRunning = false
	globalArchiveState.bulkResult = ""
	globalArchiveState.bulkFailed = nil
	globalArchiveState.bulkDeleteConfirm = false
	resetArchiveWorktreeStateForTest()
}

// archiveClient 获取设置面板关联的 Client 实例。
func archiveClient(st *SettingsState) Client {
	if st == nil {
		return nil
	}
	st.mu.Lock()
	chat := st.chat
	st.mu.Unlock()
	if chat != nil {
		return chat.client
	}
	return nil
}

// beginArchiveLoad 在归档状态上标记「正在加载」。已有一轮在跑时记下待补拉的刷新请求并返回 false（S-112）。
func beginArchiveLoad() bool {
	globalArchiveState.mu.Lock()
	defer globalArchiveState.mu.Unlock()
	if globalArchiveState.loading {
		// 加载窗口内来的刷新请求记下来，本轮结束后补拉一次，
		// 否则删除/恢复落在加载期间时，界面会一直停在旧列表。
		globalArchiveState.pendingReload = true
		return false
	}
	globalArchiveState.loading = true
	return true
}

// loadArchivedThreads 异步或同步拉取已归档会话与项目信息。
func loadArchivedThreads(st *SettingsState) {
	client := archiveClient(st)
	if client == nil {
		return
	}
	if !beginArchiveLoad() {
		return
	}

	go fetchArchivedThreadsWorker(st, client)
}

// loadArchivedThreadsSync 同步拉取归档会话，便于单元测试直接触发。
func loadArchivedThreadsSync(st *SettingsState) {
	client := archiveClient(st)
	if client == nil {
		return
	}
	beginArchiveLoad()

	fetchArchivedThreadsWorker(st, client)
}

// archiveThreadsFetch 是归档页的数据源：项目名表与已归档会话。测试可替换它统计加载次数。
var archiveThreadsFetch = func(ctx context.Context, client Client) ([]Thread, map[string]string, error) {
	projs, err := client.Projects(ctx)
	if err != nil {
		return nil, nil, err
	}
	pMap := make(map[string]string)
	for _, p := range projs {
		pMap[p.ID] = p.Name
	}
	threads, err := client.ArchivedThreads(ctx)
	if err != nil {
		return nil, nil, err
	}
	return threads, pMap, nil
}

// ensureArchiveLoaded 在数据为空且尚未自动尝试过时发起一次加载；失败后等用户点「重试」。
func ensureArchiveLoaded(st *SettingsState) {
	globalArchiveState.mu.Lock()
	if globalArchiveState.loaded || globalArchiveState.loading || globalArchiveState.attempted {
		globalArchiveState.mu.Unlock()
		return
	}
	globalArchiveState.attempted = true
	globalArchiveState.mu.Unlock()

	loadArchivedThreads(st)
}

// retryArchiveLoad 清掉上一次的失败记录并重新加载，由「重试」按钮触发。
func retryArchiveLoad(st *SettingsState) {
	globalArchiveState.mu.Lock()
	globalArchiveState.attempted = false
	globalArchiveState.loadErr = ""
	globalArchiveState.mu.Unlock()
	ensureArchiveLoaded(st)
}

func fetchArchivedThreadsWorker(st *SettingsState, client Client) {
	for {
		threads, projects, err := archiveThreadsFetch(context.Background(), client)

		globalArchiveState.mu.Lock()
		globalArchiveState.loading = false
		if err != nil {
			// 失败时保留上一次成功的列表，只记下原因；重试交给用户。
			globalArchiveState.loadErr = err.Error()
			globalArchiveState.pendingReload = false
			globalArchiveState.mu.Unlock()
			if st != nil {
				st.invalidate()
			}
			return
		}
		globalArchiveState.threads = threads
		globalArchiveState.projects = projects
		globalArchiveState.loaded = true
		globalArchiveState.loadErr = ""
		pending := globalArchiveState.pendingReload
		globalArchiveState.pendingReload = false
		globalArchiveState.mu.Unlock()

		if st != nil {
			st.invalidate()
		}
		if !pending {
			return
		}
	}
}

// setArchiveOpErr 记录或清除归档页写操作的失败原因，并让界面重绘（S-109）。
func setArchiveOpErr(st *SettingsState, msg string) {
	globalArchiveState.mu.Lock()
	globalArchiveState.opErr = msg
	globalArchiveState.mu.Unlock()
	if st != nil {
		st.invalidate()
	}
}

// archiveConfirmDelete 报告归档页删除是否需要二次确认（General.ConfirmBeforeDeleting）；
// 未关联设置时按开启处理，保持原有行为。
func archiveConfirmDelete(st *SettingsState) bool {
	if st == nil {
		return true
	}
	return st.cfg.Archive.ConfirmBeforeDeleting
}

// deleteArchivedThread 永久删除单个已归档会话，成功后刷新列表。
func deleteArchivedThread(st *SettingsState, target *Thread) {
	if target == nil {
		return
	}
	targetID := target.ID
	client := archiveClient(st)
	if client == nil {
		return
	}
	go func() {
		if err := client.DeleteThread(targetID); err != nil {
			setArchiveOpErr(st, "删除失败："+err.Error())
			return
		}
		setArchiveOpErr(st, "")
		loadArchivedThreads(st)
	}()
}

// startArchiveDeleteAll 立即清空全部已归档会话（「删除前确认」关闭时跳过弹窗）。
func startArchiveDeleteAll(st *SettingsState) {
	// archiveClient 会拿 st.mu，必须先在归档状态锁之外取，避免锁序倒置。
	client := archiveClient(st)
	globalArchiveState.mu.Lock()
	if client == nil || globalArchiveState.deletingAll {
		globalArchiveState.mu.Unlock()
		return
	}
	globalArchiveState.deleteAllModal = false
	globalArchiveState.deletingAll = true
	globalArchiveState.deleteAllResult = ""
	globalArchiveState.deleteAllDeleted = 0
	globalArchiveState.deleteAllFailed = nil
	toDelete := make([]Thread, len(globalArchiveState.threads))
	copy(toDelete, globalArchiveState.threads)
	globalArchiveState.mu.Unlock()
	if st != nil {
		st.invalidate()
	}
	go deleteAllArchivedThreads(st, client, toDelete)
}

// deleteAllArchivedThreads 逐条删除全部已归档会话，完成后汇总「已删 X、失败 Y」并刷新列表（S-111）。
// 单条失败不影响其余条目，失败原因逐条列在区块内，便于用户重试。
func deleteAllArchivedThreads(st *SettingsState, client Client, targets []Thread) {
	deleted := 0
	var failed []string
	if client != nil {
		for _, th := range targets {
			if err := client.DeleteThread(th.ID); err != nil {
				failed = append(failed, fmt.Sprintf("%s：%s", archiveThreadLabel(th), err.Error()))
				continue
			}
			deleted++
		}
	}

	globalArchiveState.mu.Lock()
	globalArchiveState.deletingAll = false
	globalArchiveState.deleteAllDeleted = deleted
	globalArchiveState.deleteAllFailed = failed
	globalArchiveState.deleteAllResult = archiveDeleteAllResultText(deleted, failed)
	globalArchiveState.mu.Unlock()
	if st != nil {
		st.invalidate()
	}

	loadArchivedThreads(st)
}

// archiveThreadLabel 返回会话在失败提示里的可读名称：优先标题，没标题时用 ID。
func archiveThreadLabel(th Thread) string {
	if title := strings.TrimSpace(th.Title); title != "" {
		return title
	}
	return th.ID
}

// archiveDeleteAllResultText 汇总批量删除的结果文案。
func archiveDeleteAllResultText(deleted int, failed []string) string {
	switch {
	case len(failed) == 0:
		return fmt.Sprintf("已删除全部 %d 个已归档会话", deleted)
	case deleted == 0:
		return fmt.Sprintf("删除失败：%d 个已归档会话都未被删除", len(failed))
	default:
		return fmt.Sprintf("已删除 %d 个已归档会话，%d 个失败", deleted, len(failed))
	}
}

// renderArchiveOpError 在「已归档会话」区块内展示单条写操作的失败原因（S-109）。
func renderArchiveOpError(c *ui.Context, msg string) {
	ui.Row(c.Key("row-archive-op-error")).
		Padding(Space3, Space5).
		Gap(Space2).
		AlignItems(ui.Center).
		Children(func() {
			ui.Icon(c, IconOctagonAlert()).Size(IconSM, IconSM).TextColor(ThemeDanger(c))
			ui.Text(c, msg).
				FontSize(TypeFootnote).
				TextColor(ThemeDanger(c)).
				Grow(1)
		})
}

// renderArchiveDeleteAllResult 渲染「全部删除」的结果汇总与逐条失败原因。
func renderArchiveDeleteAllResult(c *ui.Context, text string, deleted int, failed []string) {
	color := ThemeSuccess(c)
	switch {
	case deleted == 0 && len(failed) > 0:
		color = ThemeDanger(c)
	case len(failed) > 0:
		color = ThemeWarning(c)
	}
	ui.Column(c.Key("row-archive-delete-all-result")).
		Padding(Space3, Space5).
		Gap(Space1).
		Children(func() {
			ui.Text(c, text).
				FontSize(TypeFootnote).
				FontWeight(500).
				TextColor(color)
			for _, line := range failed {
				ui.Text(c, line).
					FontSize(TypeCaption).
					TextColor(ThemeDanger(c))
			}
		})
}

// archiveCountLabel 读取归档数量；首次展示入口时触发一次加载，失败后不再逐帧重试。
func archiveCountLabel(st *SettingsState) string {
	ensureArchiveLoaded(st)
	globalArchiveState.mu.Lock()
	loaded, loadErr, count := globalArchiveState.loaded, globalArchiveState.loadErr, len(globalArchiveState.threads)
	globalArchiveState.mu.Unlock()
	switch {
	case !loaded && loadErr != "":
		return "加载失败"
	case !loaded:
		return "加载中"
	}
	return fmt.Sprintf("%d 个", count)
}

// archivePageView 是归档页一帧所需的全部数据快照，渲染只读这些字段。
type archivePageView struct {
	client Client

	loaded  bool
	loadErr string

	threads  []Thread
	projects map[string]string
	visible  []Thread
	groups   []archiveProjectGroup
	picked   map[string]bool

	query       string
	pickedCount int

	deleteTarget       *Thread
	deleteModalOpen    bool
	deleteAllModalOpen bool
	opErr              string
	deletingAll        bool
	deleteAllResult    string
	deleteAllDeleted   int
	deleteAllFailed    []string
	bulkRunning        bool
	bulkResult         string
	bulkFailed         []string
	bulkDeleteConfirm  bool
}

// archivePageData 是归档页唯一的数据入口：先确保自动加载只发起一次，
// 再一次性加锁复制状态并算好可见列表、分组与选中数；之后渲染不再取数。
func archivePageData(st *SettingsState) archivePageView {
	ensureArchiveLoaded(st)

	// archiveClient 会拿 st.mu，必须先在归档状态锁之外取，避免锁序倒置。
	var view archivePageView
	view.client = archiveClient(st)

	globalArchiveState.mu.Lock()
	view.loaded = globalArchiveState.loaded
	view.loadErr = globalArchiveState.loadErr
	view.threads = append([]Thread(nil), globalArchiveState.threads...)
	view.projects = make(map[string]string, len(globalArchiveState.projects))
	for k, v := range globalArchiveState.projects {
		view.projects[k] = v
	}
	view.deleteTarget = globalArchiveState.deleteTarget
	view.deleteModalOpen = globalArchiveState.deleteModal
	view.deleteAllModalOpen = globalArchiveState.deleteAllModal
	view.opErr = globalArchiveState.opErr
	view.deletingAll = globalArchiveState.deletingAll
	view.deleteAllResult = globalArchiveState.deleteAllResult
	view.deleteAllDeleted = globalArchiveState.deleteAllDeleted
	view.deleteAllFailed = append([]string(nil), globalArchiveState.deleteAllFailed...)
	view.query = globalArchiveState.query
	view.picked = make(map[string]bool, len(globalArchiveState.picked))
	for k, v := range globalArchiveState.picked {
		view.picked[k] = v
	}
	view.bulkRunning = globalArchiveState.bulkRunning
	view.bulkResult = globalArchiveState.bulkResult
	view.bulkFailed = append([]string(nil), globalArchiveState.bulkFailed...)
	view.bulkDeleteConfirm = globalArchiveState.bulkDeleteConfirm
	globalArchiveState.mu.Unlock()

	view.visible = archiveFilterThreads(view.threads, view.projects, view.query)
	view.groups = archiveGroupByProject(view.visible, view.projects)
	view.pickedCount = len(archivePickedThreads(view.threads, view.picked))
	return view
}

// restoreArchivedThread 恢复单条已归档会话，成功后刷新列表。
func restoreArchivedThread(st *SettingsState, client Client, target Thread) {
	if client == nil {
		return
	}
	targetID := target.ID
	go func() {
		if err := client.SetThreadArchived(targetID, false); err != nil {
			setArchiveOpErr(st, "恢复失败："+err.Error())
			return
		}
		setArchiveOpErr(st, "")
		loadArchivedThreads(st)
	}()
}

// requestArchiveDelete 处理删除按钮：需要确认时开弹窗，关时直接删除。
func requestArchiveDelete(st *SettingsState, target Thread) {
	if archiveConfirmDelete(st) {
		targetCopy := target
		globalArchiveState.mu.Lock()
		globalArchiveState.deleteTarget = &targetCopy
		globalArchiveState.deleteModal = true
		globalArchiveState.mu.Unlock()
		if st != nil {
			st.invalidate()
		}
		return
	}
	deleteArchivedThread(st, &target)
}

// requestArchiveDeleteAll 处理「全部删除」：需要确认时开弹窗，关时直接清空。
func requestArchiveDeleteAll(st *SettingsState) {
	if !archiveConfirmDelete(st) {
		startArchiveDeleteAll(st)
		return
	}
	globalArchiveState.mu.Lock()
	globalArchiveState.deleteAllModal = true
	globalArchiveState.mu.Unlock()
	if st != nil {
		st.invalidate()
	}
}

// requestArchiveBulkDelete 打开批量删除的二次确认弹窗。
func requestArchiveBulkDelete(st *SettingsState) {
	globalArchiveState.mu.Lock()
	globalArchiveState.bulkDeleteConfirm = true
	globalArchiveState.mu.Unlock()
	if st != nil {
		st.invalidate()
	}
}

// renderArchiveThreadRow 渲染一条归档会话：小号灰字归档时间 + 多选 + 恢复 + 删除。
func renderArchiveThreadRow(c *ui.Context, st *SettingsState, client Client, th Thread, picked map[string]bool) {
	ui.Row(c.Key("row-archive-actions-" + th.ID)).Gap(Space3).AlignItems(ui.Center).Children(func() {
		ui.Text(c, th.UpdatedAt.Local().Format("2006-01-02 15:04")).
			FontSize(TypeCaption).
			TextColor(ThemeSecondaryText(c)).
			SingleLine()

		// 行首复选框：勾选状态存在 picked 里，批量操作失败项会保持勾选。
		checked := picked[th.ID]
		cb := ui.Checkbox(c.Key("archive-check-"+th.ID), &checked, "").
			Label("archive-check-" + th.ID)
		if cb.Changed() {
			setArchivePicked(st, th.ID, checked)
		}

		Button(c.Key("btn-restore-"+th.ID), ButtonOpts{
			Text:    "恢复",
			Label:   "恢复会话 " + th.Title,
			Variant: ButtonSecondary,
			Size:    ControlMD,
			OnClick: func() { restoreArchivedThread(st, client, th) },
		})

		Button(c.Key("btn-delete-"+th.ID), ButtonOpts{
			Text:    "删除",
			Label:   "删除会话 " + th.Title,
			Variant: ButtonDanger,
			Size:    ControlMD,
			OnClick: func() { requestArchiveDelete(st, th) },
		})
	})
}

// renderPageArchive 渲染归档面板正文，只读 archivePageData 的快照。
func renderPageArchive(c *ui.Context, st *SettingsState) {
	view := archivePageData(st)

	// 页头与操作栏
	ui.Row(c.Key("archive-header-row")).
		Justify(ui.SpaceBetween).
		AlignItems(ui.Center).
		Margin(0, 0, Space3, 0).
		Children(func() {
			query := view.query
			// 搜索只过滤本地列表，不发请求；输入即过滤（不区分大小写）。
			SearchField(c, TextFieldProps{
				Key:         "archive-search",
				Value:       &query,
				Placeholder: "搜索已归档会话…",
				Label:       "搜索已归档会话",
				Clearable:   true,
				ClearLabel:  "清除搜索",
				OnChange: func(val string) {
					globalArchiveState.mu.Lock()
					globalArchiveState.query = val
					globalArchiveState.mu.Unlock()
					if st != nil {
						st.invalidate()
					}
				},
			})

			if len(view.threads) > 0 {
				Button(c.Key("btn-archive-delete-all"), ButtonOpts{
					Icon:     IconTrash2(),
					Text:     "全部删除",
					Variant:  ButtonDanger,
					Size:     ControlMD,
					Disabled: view.deletingAll,
					OnClick:  func() { requestArchiveDeleteAll(st) },
				})
			}
		})

	renderSettingsSection(c, "sec-archive-list", "已归档会话", func() {
		if view.deletingAll {
			ui.Row(c.Key("row-archive-deleting-all")).
				Padding(settingsCompactPadX, Space5).
				Gap(Space2).
				AlignItems(ui.Center).
				Children(func() {
					ui.Spinner(c).Size(IconSM, IconSM).TextColor(ThemeAccent(c))
					ui.Text(c, "正在删除全部已归档会话…").
						FontSize(TypeFootnote).
						TextColor(ThemeSecondaryText(c))
				})
		}

		if view.opErr != "" {
			renderArchiveOpError(c, view.opErr)
		}

		if view.deleteAllResult != "" {
			renderArchiveDeleteAllResult(c, view.deleteAllResult, view.deleteAllDeleted, view.deleteAllFailed)
		}

		if view.bulkResult != "" {
			renderArchiveBulkResult(c, view.bulkResult, view.bulkFailed)
		}

		if !view.loaded {
			if view.loadErr == "" {
				ui.Row(c.Key("row-archive-loading")).
					Padding(Space7, Space5).
					Gap(Space3).
					AlignItems(ui.Center).
					Justify(ui.Center).
					Children(func() {
						ui.Spinner(c).Size(IconSM, IconSM).TextColor(ThemeAccent(c))
						ui.Text(c, "加载中…").FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
					})
			} else {
				ui.Row(c.Key("row-archive-load-error")).
					Padding(Space7, Space5).
					Gap(settingsCompactPadX).
					AlignItems(ui.Center).
					Justify(ui.Center).
					Children(func() {
						ui.Text(c, "加载失败："+view.loadErr).
							FontSize(TypeFootnote).
							TextColor(ThemeDanger(c)).
							SingleLine()
						Button(c.Key("btn-archive-retry"), ButtonOpts{
							Text:    "重试",
							Label:   "重新加载已归档会话",
							Variant: ButtonSecondary,
							Size:    ControlMD,
							OnClick: func() { retryArchiveLoad(st) },
						})
					})
			}
			return
		}

		if len(view.threads) == 0 {
			ui.Column(c.Key("row-archive-empty")).
				Padding(Space7, Space5).
				Gap(Space3).
				AlignItems(ui.Center).
				Children(func() {
					ui.Icon(c, IconArchive()).
						Size(ControlMD, ControlMD).
						TextColor(ThemeSecondaryText(c))

					ui.Text(c, "暂无已归档会话").
						FontSize(TypeProse).
						FontWeight(500).
						TextColor(ThemeSecondaryText(c)).
						SingleLine()

				})
			return
		}

		// 头部批量条：无论是否搜索都渲染，避免过滤后无法取消选择；无选中项时批量按钮禁用。
		renderArchiveBulkBar(c, st, view.threads, view.pickedCount, view.bulkRunning, view.client)
		if len(view.visible) == 0 {
			renderArchiveNoMatch(c)
			return
		}

		for _, group := range view.groups {
			renderArchiveProjectGroupHeader(c, group.projectID, group.name, len(group.threads))
			for idx, th := range group.threads {
				if idx > 0 {
					renderSettingsDivider(c, fmt.Sprintf("div-archive-%s", th.ID))
				}

				thVal := th
				renderSettingsTreeSetting(c, st, setting{
					ID:    "row-archive-" + thVal.ID,
					Title: thVal.Title,
					Control: custom(func(c *ui.Context, st *SettingsState) {
						renderArchiveThreadRow(c, st, view.client, thVal, view.picked)
					}),
				})
			}
		}
	})

	// 弹窗确认
	if view.deleteModalOpen && view.deleteTarget != nil {
		renderArchiveDeleteModal(c, st, view.deleteTarget, &globalArchiveState.deleteModal)
	}
	if view.deleteAllModalOpen {
		renderArchiveDeleteAllModal(c, st, &globalArchiveState.deleteAllModal, len(view.threads))
	}
	if view.bulkDeleteConfirm {
		renderArchiveBulkDeleteModal(c, st, view.threads, view.picked)
	}
}

type autoDormantControl struct{}

func (autoDormantControl) render(c *ui.Context, st *SettingsState, _ setting) {
	renderAutoDormantControl(c, st)
}

func (autoDormantControl) reset(cfg *config.Config) {
	cfg.Archive.SidebarAutoDormantDays = nil
}

func renderAutoDormantControl(c *ui.Context, st *SettingsState) {
	days := st.cfg.Archive.AutoDormantDays()
	options := []string{"关闭 (0)", "1 天", "3 天", "7 天", "14 天", "30 天"}
	curOpt := "关闭 (0)"
	if days > 0 {
		curOpt = fmt.Sprintf("%d 天", days)
		known := false
		for _, opt := range options {
			if opt == curOpt {
				known = true
				break
			}
		}
		if !known {
			options = append(options, curOpt)
		}
	}
	sel := ui.Select(c.Key("sel-auto-dormant-days"), &curOpt, options).
		Label("自动休眠天数").Width(settingsFieldWidth)
	if sel.Changed() {
		val := 0
		if curOpt != "关闭 (0)" {
			fmt.Sscanf(curOpt, "%d", &val)
		}
		if val != days {
			st.cfg.Archive.SidebarAutoDormantDays = &val
			st.save()
		}
	}
}

// renderArchiveDeleteModal 渲染单个会话永久删除二次确认弹窗（统一确认组件，危险态）。
func renderArchiveDeleteModal(c *ui.Context, st *SettingsState, target *Thread, open *bool) {
	if target == nil || open == nil {
		RenderConfirmDialog(c, ConfirmDialog{Key: modalDragArchiveDelete, Open: false})
		return
	}
	desc := "确定要永久删除此会话吗？"
	if title := target.Title; title != "" {
		desc = fmt.Sprintf("确定要永久删除会话 %q 吗？", title)
	}
	closeModal := func() {
		globalArchiveState.mu.Lock()
		globalArchiveState.deleteModal = false
		globalArchiveState.mu.Unlock()
		if st != nil {
			st.invalidate()
		}
	}
	deleteTarget := target
	RenderConfirmDialog(c, ConfirmDialog{
		Key:          modalDragArchiveDelete,
		Open:         *open,
		Tone:         ConfirmDanger,
		Title:        "删除已归档会话",
		Body:         desc,
		ConfirmLabel: "删除",
		ConfirmA11y:  "确认删除单个已归档会话",
		OnConfirm: func() {
			closeModal()
			deleteArchivedThread(st, deleteTarget)
		},
		OnCancel: closeModal,
	})
}

// renderArchiveDeleteAllModal 渲染批量清空全部已归档会话二次确认弹窗（统一确认组件，危险态）。
func renderArchiveDeleteAllModal(c *ui.Context, st *SettingsState, open *bool, count int) {
	if open == nil {
		RenderConfirmDialog(c, ConfirmDialog{Key: modalDragArchiveDeleteAll, Open: false})
		return
	}
	closeModal := func() {
		globalArchiveState.mu.Lock()
		globalArchiveState.deleteAllModal = false
		globalArchiveState.mu.Unlock()
		if st != nil {
			st.invalidate()
		}
	}
	RenderConfirmDialog(c, ConfirmDialog{
		Key:          modalDragArchiveDeleteAll,
		Open:         *open,
		Tone:         ConfirmDanger,
		Title:        "清空所有已归档会话",
		Body:         fmt.Sprintf("确定要永久删除全部 %d 个已归档会话吗？", count),
		ConfirmLabel: "全部删除",
		// startArchiveDeleteAll 内部会复位 deleteAllModal 并起 goroutine。
		OnConfirm: func() { startArchiveDeleteAll(st) },
		OnCancel:  closeModal,
	})
}

// renderArchiveBulkDeleteModal 渲染批量删除的确认弹窗：标题写明条数，正文说明不可恢复。
func renderArchiveBulkDeleteModal(c *ui.Context, st *SettingsState, threads []Thread, picked map[string]bool) {
	count := len(archivePickedThreads(threads, picked))
	globalArchiveState.mu.Lock()
	open := globalArchiveState.bulkDeleteConfirm
	globalArchiveState.mu.Unlock()

	closeModal := func() {
		globalArchiveState.mu.Lock()
		globalArchiveState.bulkDeleteConfirm = false
		globalArchiveState.mu.Unlock()
		if st != nil {
			st.invalidate()
		}
	}
	RenderConfirmDialog(c, ConfirmDialog{
		Key:          modalDragArchiveBulkDelete,
		Open:         open,
		Tone:         ConfirmDanger,
		Title:        fmt.Sprintf("删除 %d 个已归档会话", count),
		Body:         "确定要永久删除选中的会话吗？",
		ConfirmLabel: "删除",
		ConfirmA11y:  "确认删除已归档会话",
		CancelA11y:   "取消删除已归档会话",
		OnConfirm: func() {
			closeModal()
			runArchiveBulk(st, archiveClient(st), archiveBulkDelete)
		},
		OnCancel: closeModal,
	})
}

// archiveUnknownProjectName 是缺少项目信息时的分组名。
const archiveUnknownProjectName = "未关联项目"

// 批量操作的动作名。
const (
	archiveBulkRestore = "restore"
	archiveBulkDelete  = "delete"
)

// modalDragArchiveBulkDelete 是批量删除确认弹窗的拖拽 key（本文件自用，不改 modal_drag.go）。
const modalDragArchiveBulkDelete = "modal-drag-archive-bulk-delete"

// archiveBulkTimeout 是批量操作单条的超时时间：一条卡住不拖住整批。
const archiveBulkTimeout = 20 * time.Second

// archiveProjectGroup 是按项目分组的归档会话。
type archiveProjectGroup struct {
	projectID string
	name      string
	threads   []Thread
}

// archiveFilterThreads 按搜索词过滤归档会话：标题或项目名包含关键字即命中（不区分大小写），
// 搜索词为空时原样返回。
func archiveFilterThreads(threads []Thread, projects map[string]string, query string) []Thread {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return threads
	}
	out := make([]Thread, 0, len(threads))
	for _, th := range threads {
		name := projects[th.ProjectID]
		if name == "" {
			name = archiveUnknownProjectName
		}
		if strings.Contains(strings.ToLower(th.Title), q) || strings.Contains(strings.ToLower(name), q) {
			out = append(out, th)
		}
	}
	return out
}

// archiveGroupByProject 把归档会话按项目名分组：组按项目名升序，组内按归档时间（UpdatedAt）降序；
// 缺少项目信息的会话归入「未关联项目」一组。
func archiveGroupByProject(threads []Thread, projects map[string]string) []archiveProjectGroup {
	byID := make(map[string]*archiveProjectGroup, len(threads))
	ids := make([]string, 0, len(threads))
	for _, th := range threads {
		id := th.ProjectID
		group, ok := byID[id]
		if !ok {
			name := projects[id]
			if name == "" {
				name = archiveUnknownProjectName
			}
			group = &archiveProjectGroup{projectID: id, name: name}
			byID[id] = group
			ids = append(ids, id)
		}
		group.threads = append(group.threads, th)
	}
	groups := make([]archiveProjectGroup, 0, len(ids))
	for _, id := range ids {
		group := byID[id]
		sort.SliceStable(group.threads, func(i, j int) bool {
			return group.threads[i].UpdatedAt.After(group.threads[j].UpdatedAt)
		})
		groups = append(groups, *group)
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].name < groups[j].name })
	return groups
}

// renderArchiveProjectGroupHeader 渲染项目分组标题行：项目名 + 会话数量（12 号次要色）。
func renderArchiveProjectGroupHeader(c *ui.Context, projectID, name string, count int) {
	ui.Row(c.Key("archive-group-"+projectID)).
		Label("archive-group-"+projectID).
		Padding(settingsCompactPadX, Space5).
		Gap(Space3).
		AlignItems(ui.Center).
		Children(func() {
			ui.Text(c, name).
				FontSize(TypeFootnote).
				FontWeight(600).
				TextColor(ThemeSecondaryText(c)).
				SingleLine()
			ui.Text(c, fmt.Sprintf("%d 个会话", count)).
				FontSize(TypeFootnote).
				TextColor(ThemeSecondaryText(c)).
				SingleLine()
		})
}

// renderArchiveNoMatch 渲染「搜索无匹配」提示，避免过滤后整块空白让人以为列表丢了。
func renderArchiveNoMatch(c *ui.Context) {
	ui.Row(c.Key("row-archive-no-match")).
		Label("row-archive-no-match").
		Padding(Space7, Space5).
		AlignItems(ui.Center).
		Children(func() {
			ui.Text(c, "没有匹配的已归档会话").
				FontSize(TypeFootnote).
				TextColor(ThemeSecondaryText(c))
		})
}

// archivePickedThreads 返回当前选中的归档会话（保持传入列表顺序）。
func archivePickedThreads(threads []Thread, picked map[string]bool) []Thread {
	out := make([]Thread, 0, len(threads))
	for _, th := range threads {
		if picked[th.ID] {
			out = append(out, th)
		}
	}
	return out
}

// setArchivePicked 写回某一行的勾选状态并触发重绘。
func setArchivePicked(st *SettingsState, threadID string, picked bool) {
	if threadID == "" {
		return
	}
	globalArchiveState.mu.Lock()
	if globalArchiveState.picked == nil {
		globalArchiveState.picked = make(map[string]bool)
	}
	if picked {
		globalArchiveState.picked[threadID] = true
	} else {
		delete(globalArchiveState.picked, threadID)
	}
	globalArchiveState.mu.Unlock()
	if st != nil {
		st.invalidate()
	}
}

// setArchivePickedAll 对给定列表全选或清空选择。
func setArchivePickedAll(st *SettingsState, threads []Thread, selected bool) {
	globalArchiveState.mu.Lock()
	if globalArchiveState.picked == nil {
		globalArchiveState.picked = make(map[string]bool)
	}
	for _, th := range threads {
		if selected {
			globalArchiveState.picked[th.ID] = true
		} else {
			delete(globalArchiveState.picked, th.ID)
		}
	}
	globalArchiveState.mu.Unlock()
	if st != nil {
		st.invalidate()
	}
}

// renderArchiveBulkBar 渲染头部批量条：「已选 N 项」+ 全选 / 取消选择 / 恢复 / 删除。
// 没有选中项或正在批量执行时，恢复与删除禁用。
func renderArchiveBulkBar(c *ui.Context, st *SettingsState, threads []Thread, count int, running bool, client Client) {
	disabled := count == 0 || running

	ui.Row(c.Key("archive-bulk-bar")).
		Label("archive-bulk-bar").
		Padding(Space3, Space5).
		Gap(Space3).
		AlignItems(ui.Center).
		Children(func() {
			ui.Text(c, fmt.Sprintf("已选 %d 项", count)).
				FontSize(TypeFootnote).
				TextColor(ThemeSecondaryText(c)).
				Grow(1)

			Button(c.Key("btn-archive-bulk-select-all"), ButtonOpts{
				Text:     "全选",
				Label:    "批量全选",
				Variant:  ButtonSecondary,
				Size:     ControlMD,
				Disabled: running,
				OnClick: func() {
					setArchivePickedAll(st, threads, true)
				},
			})
			Button(c.Key("btn-archive-bulk-clear"), ButtonOpts{
				Text:     "取消选择",
				Label:    "批量取消选择",
				Variant:  ButtonSecondary,
				Size:     ControlMD,
				Disabled: disabled,
				OnClick: func() {
					setArchivePickedAll(st, threads, false)
				},
			})
			Button(c.Key("btn-archive-bulk-restore"), ButtonOpts{
				Text:     "恢复",
				Label:    "批量恢复",
				Variant:  ButtonPrimary,
				Size:     ControlMD,
				Disabled: disabled,
				OnClick: func() {
					runArchiveBulk(st, client, archiveBulkRestore)
				},
			})
			Button(c.Key("btn-archive-bulk-delete"), ButtonOpts{
				Text:     "删除",
				Label:    "批量删除",
				Variant:  ButtonDanger,
				Size:     ControlMD,
				Disabled: disabled,
				OnClick: func() {
					requestArchiveBulkDelete(st)
				},
			})
		})
}

// renderArchiveBulkResult 渲染批量恢复/删除的结果汇总与逐条失败原因。
func renderArchiveBulkResult(c *ui.Context, text string, failed []string) {
	color := ThemeSuccess(c)
	if len(failed) > 0 {
		color = ThemeWarning(c)
	}
	ui.Column(c.Key("row-archive-bulk-result")).
		Label("row-archive-bulk-result").
		Padding(Space3, Space5).
		Gap(Space1).
		Children(func() {
			ui.Text(c, text).
				FontSize(TypeFootnote).
				FontWeight(500).
				TextColor(color)
			for _, line := range failed {
				ui.Text(c, line).
					FontSize(TypeCaption).
					TextColor(ThemeDanger(c))
			}
		})
}

// archiveBulkResultText 汇总批量恢复/删除的结果文案，风格与「全部删除」保持一致。
func archiveBulkResultText(action string, done int, failed []string) string {
	verb := "恢复"
	if action == archiveBulkDelete {
		verb = "删除"
	}
	switch {
	case len(failed) == 0:
		return fmt.Sprintf("已%s %d 个已归档会话", verb, done)
	case done == 0:
		return fmt.Sprintf("%s失败：%d 个已归档会话都未%s", verb, len(failed), verb)
	default:
		return fmt.Sprintf("已%s %d 个已归档会话，%d 个失败", verb, done, len(failed))
	}
}

// runArchiveBulk 逐条执行批量恢复或删除：恢复调 SetThreadArchived(id, false)，删除调 DeleteThread(id)。
// 单条失败不影响其余条目；失败项保持选中并在 bulkFailed 里记下原因，完成后刷新列表。
func runArchiveBulk(st *SettingsState, client Client, action string) {
	if client == nil {
		return
	}
	globalArchiveState.mu.Lock()
	if globalArchiveState.bulkRunning {
		globalArchiveState.mu.Unlock()
		return
	}
	var targets []Thread
	for _, th := range globalArchiveState.threads {
		if globalArchiveState.picked[th.ID] {
			targets = append(targets, th)
		}
	}
	if len(targets) == 0 {
		globalArchiveState.mu.Unlock()
		return
	}
	globalArchiveState.bulkRunning = true
	globalArchiveState.bulkResult = ""
	globalArchiveState.bulkFailed = nil
	globalArchiveState.bulkDeleteConfirm = false
	globalArchiveState.mu.Unlock()
	if st != nil {
		st.invalidate()
	}

	go func() {
		done := 0
		var failed []string
		failedIDs := make(map[string]bool, len(targets))
		for _, th := range targets {
			if err := runArchiveBulkItem(client, action, th.ID); err != nil {
				failed = append(failed, fmt.Sprintf("%s：%s", archiveThreadLabel(th), err.Error()))
				failedIDs[th.ID] = true
				continue
			}
			done++
		}

		globalArchiveState.mu.Lock()
		globalArchiveState.bulkRunning = false
		globalArchiveState.bulkResult = archiveBulkResultText(action, done, failed)
		globalArchiveState.bulkFailed = failed
		if globalArchiveState.picked == nil {
			globalArchiveState.picked = make(map[string]bool)
		}
		for _, th := range targets {
			if failedIDs[th.ID] {
				// 失败项保持选中，便于用户重试。
				globalArchiveState.picked[th.ID] = true
				continue
			}
			delete(globalArchiveState.picked, th.ID)
		}
		globalArchiveState.mu.Unlock()
		if st != nil {
			st.invalidate()
		}

		loadArchivedThreads(st)
	}()
}

// runArchiveBulkItem 执行单条批量操作，最多等 archiveBulkTimeout。
// 客户端方法不带 context，这里用 goroutine + 超时兜底，避免一条卡死拖住整批。
func runArchiveBulkItem(client Client, action, threadID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), archiveBulkTimeout)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		if action == archiveBulkDelete {
			result <- client.DeleteThread(threadID)
			return
		}
		result <- client.SetThreadArchived(threadID, false)
	}()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return fmt.Errorf("等待超过 %s", archiveBulkTimeout)
	}
}
