package mcodeui

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/agentconf"
	"mcode/internal/mcp"
)

// modalDragMCPDelete 是删除 MCP 服务确认框的拖拽 key（单列在这里，不去改 modal_drag.go 的常量表）。
const modalDragMCPDelete = "modal-drag-mcp-delete-server"

func probeMCPTools(ctx context.Context, srv agentconf.MCPServer) ([]agentconf.MCPToolInfo, error) {
	spec := mcp.SpecFromAgentconf("", srv)
	var tokens mcp.TokenSource
	if spec.OAuth {
		// OAuth 服务探活需带上已绑定的 Token，否则上游一律返回 401，
		// 设置页无法展示工具列表与在线状态。
		tokens = mcp.DefaultTokenSource()
	}
	res, err := mcp.Probe(ctx, spec, tokens)
	if err != nil {
		return nil, err
	}
	tools := make([]agentconf.MCPToolInfo, len(res.Tools))
	for i, t := range res.Tools {
		tools[i] = agentconf.MCPToolInfo{
			Name:        t.Name,
			Description: t.Description,
		}
	}
	return tools, nil
}

func init() {
	if agentconf.DefaultToolLister == nil {
		agentconf.DefaultToolLister = probeMCPTools
	}
}

// withProbeCredentials 给设置页列表里已脱敏的服务补回 Env 与 Headers。
// 列表扫描不带凭据，直接拿去探测会丢掉 Authorization 等请求头与 token 环境变量，上游一律返回 401；
// 只复制这两个字段，Untrusted 等判定仍以列表里的为准；重扫失败或找不到目标时原样返回。
func withProbeCredentials(opts agentconf.Options, srv agentconf.MCPServer) agentconf.MCPServer {
	opts.WithCredentials = true
	full, err := scanMCP(opts)
	if err != nil {
		return srv
	}
	for _, cand := range full {
		if cand.Agent == srv.Agent && cand.Scope == srv.Scope && cand.Name == srv.Name {
			srv.Env = cand.Env
			srv.Headers = cand.Headers
			break
		}
	}
	return srv
}

// startMCPToolProbe 在后台探测某服务的工具列表并写回页面状态；已有缓存或正在探测时直接返回，
// 保证每个服务每次打开设置页只自动探测一次。服务器行展开与预设卡片公用同一份状态。
func startMCPToolProbe(st *SettingsState, uiSt *mcpPageUIState, srv agentconf.MCPServer) {
	key := mcp.EndpointKey(srv.Agent, srv.Scope, srv.Name)
	uiSt.mu.Lock()
	if uiSt.toolsCache[key] != nil || uiSt.toolsLoading[key] {
		uiSt.mu.Unlock()
		return
	}
	uiSt.toolsLoading[key] = true
	delete(uiSt.toolsErr, key)
	uiSt.mu.Unlock()

	credOpts := st.agentConfWriteOptions()
	go func(s agentconf.MCPServer) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		toolList, err := agentconf.ListMCPTools(ctx, withProbeCredentials(credOpts, s))
		if toolList == nil {
			// 探测成功但无工具时也写入非 nil 空列表，避免被当成「尚未探测」而反复重试。
			toolList = []agentconf.MCPToolInfo{}
		}
		uiSt.mu.Lock()
		delete(uiSt.toolsLoading, key)
		if err == nil {
			uiSt.toolsCache[key] = toolList
			delete(uiSt.toolsErr, key)
		} else {
			uiSt.toolsErr[key] = err.Error()
		}
		uiSt.mu.Unlock()
		st.invalidate()
	}(srv)
}

// resetMCPToolProbe 清空某服务的工具缓存与探测错误，供「重新检测」按钮重新探测。
func resetMCPToolProbe(uiSt *mcpPageUIState, name string) {
	uiSt.mu.Lock()
	delete(uiSt.toolsCache, name)
	delete(uiSt.toolsErr, name)
	uiSt.mu.Unlock()
}

// mcpPageUIState 维护 MCP 设置页的客户端交互状态（添加/编辑表单、展开工具列表等）。
type mcpPageUIState struct {
	mu sync.Mutex

	isFormOpen     bool
	selectedServer string
	addTab         int
	editingServer  string
	// editingScope 记录正在编辑的服务原本所在的作用域（global/project），
	// 保存时沿用原作用域，否则编辑项目级服务会另存出一份全局记录。
	editingScope   string
	nameInput      string
	transportInput string // "stdio" 或 "http"
	commandInput   string
	argsInput      string
	urlInput       string
	envInput       string
	headersInput   string
	formError      string
	// 删除确认同时保存完整来源与扫描选项，避免同名服务或切项目后删错配置。
	// 删除会连凭据库条目一起清掉且无法撤销，点一下就删风险太大（S-076）。
	deleteConfirmTarget  agentconf.MCPServer
	deleteConfirmOptions agentconf.Options

	expandedServers map[string]bool
	toolsCache      map[string][]agentconf.MCPToolInfo
	toolsLoading    map[string]bool
	toolsErr        map[string]string

	// authIDs 缓存「名称+地址」到 AuthID 的映射，signedIn/signedInBusy 缓存登录状态与
	// 后台刷新标记（见 settings_page_mcp_cache.go）。渲染路径只读这些缓存，
	// 避免每帧做 sha256 与访问钥匙串。
	authIDs      map[string]string
	signedIn     map[string]mcpSignedInEntry
	signedInBusy map[string]bool

	// 工具级开关，键为 服务名 -> 工具名，持久化到 agentconf 的策略文件。
	disabledTools    map[string]map[string]bool
	autoApproveTools map[string]map[string]bool
	// foldThreshold 为工具折叠开关的持久化值：0 表示使用默认阈值（开启），负数表示关闭折叠。
	// 策略文件里没有这个字段时读到 0，正好与 "默认开启" 一致。
	foldThreshold int

	// policyPath 为策略文件路径，为空表示不持久化；policyError 为读写策略文件失败时的提示。
	policyPath  string
	policyError string

	// newThreadDisabled 缓存「新会话默认停用」的服务键集合（键为 agent:scope:name）。
	// 值存在即表示该服务在新建会话时默认关闭；渲染路径只读这份缓存，不逐帧读盘。
	newThreadDisabled map[string]bool
	// newThreadScanKey 记录缓存对应的扫描轮次（扫描用项目目录 + 服务条数），
	// MCP 列表重扫后键会变化，据此重新读取名单。
	newThreadScanKey string
	// newThreadDisabledErr 为保存「新会话默认停用」名单失败时的提示。
	newThreadDisabledErr string

	// authURLCopiedAt 供「复制链接」按钮短暂显示回执。
	authURLCopiedAt map[string]time.Time
}

// newMCPPageUIState 创建页面状态；首次创建时顺带读一次工具策略文件，
// 之后渲染路径只读写内存里的开关与折叠设置。
func newMCPPageUIState(dataDir string) *mcpPageUIState {
	uiSt := &mcpPageUIState{
		transportInput:  "stdio",
		expandedServers: make(map[string]bool),
		toolsCache:      make(map[string][]agentconf.MCPToolInfo),
		toolsLoading:    make(map[string]bool),
		toolsErr:        make(map[string]string),

		authIDs:      make(map[string]string),
		signedIn:     make(map[string]mcpSignedInEntry),
		signedInBusy: make(map[string]bool),

		disabledTools:    make(map[string]map[string]bool),
		autoApproveTools: make(map[string]map[string]bool),

		newThreadDisabled: make(map[string]bool),

		authURLCopiedAt: make(map[string]time.Time),
	}
	if dataDir != "" {
		uiSt.policyPath = agentconf.MCPToolPolicyPath(dataDir)
		policy, err := agentconf.LoadMCPToolPolicy(uiSt.policyPath)
		if err != nil {
			uiSt.policyError = fmt.Sprintf("读取 MCP 工具策略失败：%v", err)
		} else {
			// 仅在非 nil 时替换，保留已初始化的空 map，避免 setMCPToolFlag 写入 nil map。
			if policy.Disabled != nil {
				uiSt.disabledTools = policy.Disabled
			}
			if policy.AutoApprove != nil {
				uiSt.autoApproveTools = policy.AutoApprove
			}
			uiSt.foldThreshold = policy.FoldThreshold
		}
	}
	return uiSt
}

// getMCPUIState 返回 SettingsState 自己的 MCP 页面状态，首次调用时懒初始化。
// 调用方不得持有 st.mu：本函数在 st.mu 内创建状态，首次创建还会读一次策略文件。
func getMCPUIState(st *SettingsState) *mcpPageUIState {
	if st == nil {
		return newMCPPageUIState("")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.mcpPage == nil {
		st.mcpPage = newMCPPageUIState(st.dataDir)
	}
	return st.mcpPage
}

var mcpSections = []settingsSection{
	{ID: "mcp.custom", Title: "MCP 服务", Rows: []setting{
		{ID: "block-mcp-custom", Aliases: []string{"mcp", "服务器", "Claude", "Codex", "Agy", "Pi", "添加", "编辑", "删除"}, Control: customFullWidth(renderMCPCustomBody)},
		{ID: "row-mcp-fold-tools", Title: "折叠大量工具", Aliases: []string{"mcp", "工具", "折叠", "40", "call_tool"}, Control: toggleFunc(mcpFoldEnabled, setMCPFoldEnabled)},
	}},
}

func init() {
	settingsTreeSheetDefs = append(settingsTreeSheetDefs,
		settingsSheet{ID: "mcp-detail", Title: "MCP 服务", Width: settingsSheetWideWidth, Body: renderMCPDetailSheet},
		settingsSheet{ID: "mcp-add", Title: "添加服务", Width: settingsSheetWideWidth, Body: renderMCPAddSheet},
	)
}

type mcpSnapshot struct {
	servers           []agentconf.MCPServer
	client            Client
	authBusy          map[string]bool
	authErr           map[string]string
	staticAuth        map[string]bool
	loading           bool
	errMsg, policyErr string
	uiSt              *mcpPageUIState
}

func mcpPageSnapshot(st *SettingsState) mcpSnapshot {
	st.mu.Lock()
	loaded := st.agentConfLoaded
	loading := st.agentConfLoading
	errMsg := st.agentConfErr
	servers := make([]agentconf.MCPServer, len(st.mcpServers))
	copy(servers, st.mcpServers)
	chat := st.chat
	authBusy := make(map[string]bool, len(st.mcpAuthBusy))
	for k, v := range st.mcpAuthBusy {
		authBusy[k] = v
	}
	authErr := make(map[string]string, len(st.mcpAuthErr))
	for k, v := range st.mcpAuthErr {
		authErr[k] = v
	}
	staticAuthMap := make(map[string]bool, len(st.mcpStaticAuth))
	for k, v := range st.mcpStaticAuth {
		staticAuthMap[k] = v
	}
	// 列表扫描时记下的项目目录，用于刷新「新会话默认停用」缓存。
	scannedDir := st.agentConfProjectDir
	st.mu.Unlock()

	var client Client
	if chat != nil {
		client = chat.client
	}

	uiSt := mcpPageLoad(st, loaded, loading, scannedDir, servers, client)
	uiSt.mu.Lock()
	policyErr := uiSt.policyError
	uiSt.mu.Unlock()

	return mcpSnapshot{servers: servers, client: client, authBusy: authBusy, authErr: authErr, staticAuth: staticAuthMap, loading: loading, errMsg: errMsg, policyErr: policyErr, uiSt: uiSt}
}

// mcpPageLoad 是 MCP 页的加载入口：补齐列表扫描、刷新「新会话默认停用」缓存，
// 并把远程服务的授权 ID 与登录状态预读进缓存，让各分区的渲染只读内存快照。
func mcpPageLoad(st *SettingsState, loaded, loading bool, scannedDir string, servers []agentconf.MCPServer, client Client) *mcpPageUIState {
	if !loaded && !loading {
		st.loadAgentConf(st.currentProjectDir())
	}

	uiSt := getMCPUIState(st)
	if loaded {
		// MCP 列表扫描完成后同步一次「新会话默认停用」名单，其余帧只读缓存，不逐帧读盘。
		uiSt.syncNewThreadDisabled(st, scannedDir+"|"+strconv.Itoa(len(servers)))
	}
	mcpPrefetchAuthState(st, uiSt, servers, client)
	return uiSt
}

// mcpScopeLabel 把作用域转成界面上的中文名。
func mcpScopeLabel(scope string) string {
	if strings.EqualFold(scope, "project") {
		return "项目"
	}
	return "全局"
}

// renderMCPServerDeleteModal 渲染删除确认框，外部服务写明将改写的来源文件。
func renderMCPServerDeleteModal(c *ui.Context, st *SettingsState, uiSt *mcpPageUIState) {
	uiSt.mu.Lock()
	srv := uiSt.deleteConfirmTarget
	opts := uiSt.deleteConfirmOptions
	name, scope := srv.Name, srv.Scope
	uiSt.mu.Unlock()

	if name == "" {
		modalDragReset(modalDragMCPDelete)
		return
	}

	closeModal := func() {
		uiSt.mu.Lock()
		uiSt.deleteConfirmTarget = agentconf.MCPServer{}
		uiSt.deleteConfirmOptions = agentconf.Options{}
		uiSt.mu.Unlock()
		st.invalidate()
	}

	body := fmt.Sprintf("将删除 %s（%s）的配置与已保存的凭据，删除后无法恢复。", name, mcpScopeLabel(scope))
	if agentconf.CanDeleteExternalMCPServer(opts, srv) {
		source := srv.Source
		if rel, err := filepath.Rel(opts.Home, source); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			source = filepath.Join("~", rel)
		}
		body = fmt.Sprintf("将从 %s 删除 %s，删除后无法恢复。", source, name)
	}
	RenderConfirmDialog(c, ConfirmDialog{
		Key:          modalDragMCPDelete,
		Open:         true,
		Tone:         ConfirmDanger,
		Title:        "删除 MCP 服务",
		Body:         body,
		ConfirmLabel: "删除",
		ConfirmA11y:  "确认删除 MCP 服务",
		CancelLabel:  "取消",
		CancelA11y:   "取消删除 MCP 服务",
		OnConfirm: func() {
			closeModal()
			confirmMCPServerDelete(st, opts, srv)
		},
		// 点遮罩或按 Esc 关闭时同样要清掉待删目标，否则下一帧又会打开。
		OnCancel: closeModal,
	})
}

// confirmMCPServerDelete 按服务所在作用域删除配置与凭据，失败原因必须上屏。
func confirmMCPServerDelete(st *SettingsState, opts agentconf.Options, srv agentconf.MCPServer) {
	var err error
	if strings.EqualFold(srv.Agent, "custom") || strings.EqualFold(srv.Agent, "mcode") {
		err = agentconf.DeleteMCPServerInScope(opts, srv.Scope, srv.Name)
	} else {
		err = agentconf.DeleteExternalMCPServer(opts, srv)
		if err == nil {
			uiSt := getMCPUIState(st)
			uiSt.mu.Lock()
			policy, policyErr := agentconf.LoadMCPToolPolicy(uiSt.policyPath)
			if policyErr != nil {
				uiSt.policyError = fmt.Sprintf("读取 MCP 工具策略失败：%v", policyErr)
			} else {
				uiSt.disabledTools = policy.Disabled
				uiSt.autoApproveTools = policy.AutoApprove
				uiSt.policyError = ""
			}
			uiSt.newThreadScanKey = ""
			uiSt.mu.Unlock()
		}
	}
	if err != nil {
		st.mu.Lock()
		st.errorMessage = fmt.Sprintf("删除 MCP 服务 %s 失败：%v", srv.Name, err)
		st.mu.Unlock()
		st.invalidate()
		return
	}
	st.mu.Lock()
	st.errorMessage = ""
	st.mu.Unlock()
	st.loadAgentConf(st.currentProjectDir())
}

// syncNewThreadDisabled 在 MCP 列表扫描完成后刷新「新会话默认停用」缓存。
// scanKey 由扫描用项目目录与服务条数组成：只有重扫（切项目、增删服务）导致键变化时才读盘，
// 渲染路径其余时间只读缓存。
func (uiSt *mcpPageUIState) syncNewThreadDisabled(st *SettingsState, scanKey string) {
	uiSt.mu.Lock()
	if uiSt.newThreadScanKey == scanKey {
		uiSt.mu.Unlock()
		return
	}
	// 先占住键再读盘：读盘期间渲染路径命中同一键时不会重复读取。
	uiSt.newThreadScanKey = scanKey
	uiSt.mu.Unlock()

	// 写配置的 Options 与列表扫描同源，切项目后也读同一份全局配置（A3）。
	keys := agentconf.NewThreadDisabledServers(st.agentConfWriteOptions())

	uiSt.mu.Lock()
	disabled := make(map[string]bool, len(keys))
	for _, k := range keys {
		disabled[k] = true
	}
	uiSt.newThreadDisabled = disabled
	uiSt.mu.Unlock()
}

// renderCustomMCPSection 渲染自定义服务配置区（包含添加表单与自定义服务列表）。
func renderMCPCustomBody(c *ui.Context, st *SettingsState) {
	snap := mcpPageSnapshot(st)
	uiSt, allServers := snap.uiSt, snap.servers
	if snap.loading {
		ui.Text(c, "正在扫描 MCP 服务…").FontSize(uiFont(TypeBody)).TextColor(ThemeSecondaryText(c)).Margin(Space2, settingsRowPadX, Space1, settingsRowPadX)
	}
	if snap.errMsg != "" {
		ui.Text(c, snap.errMsg).FontSize(uiFont(TypeBody)).TextColor(ThemeDanger(c)).Margin(Space2, settingsRowPadX, Space1, settingsRowPadX)
	}
	if snap.policyErr != "" {
		ui.Text(c, snap.policyErr).FontSize(uiFont(TypeBody)).TextColor(ThemeDanger(c)).Margin(Space2, settingsRowPadX, Space1, settingsRowPadX)
	}
	customServers := allServers

	func() {
		uiSt.mu.Lock()
		isFormOpen := uiSt.isFormOpen
		uiSt.mu.Unlock()

		// 顶栏："+ 添加服务" 按钮
		ui.Row(c.Key("row-mcp-custom-header")).
			Padding(settingsRowPadY, settingsRowPadX).
			AlignItems(ui.Center).
			Children(func() {
				ui.Spacer(c)

				addBtn := ui.ButtonBase(c.Key("btn-mcp-add-server")).
					Radius(RadiusSM).
					Padding(Space1, Space4).
					Background(ThemeOverlay(c, 0.08))
				{
					scale := motionPressScale(c, addBtn, "button-press-scale", addBtn.Pressed())
					addBtn.Scale(scale, scale)
				}
				if addBtn.Hovered() {
					addBtn.Background(ThemeHover(c))
				}
				if addBtn.Clicked() {
					uiSt.mu.Lock()
					uiSt.isFormOpen = true
					uiSt.editingServer = ""
					uiSt.editingScope = ""
					uiSt.mu.Unlock()
					openSettingsSheet(st, "mcp-add", "")
					st.invalidate()
				}
				addBtn.Children(func() {
					ui.Text(c, "+ 添加服务").
						FontSize(uiFont(TypeBody)).
						FontWeight(500).
						TextColor(ThemeAccent(c))
				})
			})

		_ = isFormOpen

		if len(customServers) == 0 && !isFormOpen {
			renderSettingsDivider(c, "div-mcp-custom-empty-sep")
			ui.Row(c.Key("mcp-custom-empty")).
				Padding(settingsRowPadY, settingsRowPadX).
				Children(func() {
					ui.Text(c, "暂无自定义服务").
						FontSize(uiFont(TypeBody)).
						TextColor(ThemeSecondaryText(c))
				})
			return
		}

		for i, srv := range customServers {
			renderSettingsDivider(c, "div-mcp-custom-"+strconv.Itoa(i))
			key := mcp.EndpointKey(srv.Agent, srv.Scope, srv.Name)
			row := ui.ButtonBase(c.Key("mcp-list-"+key)).Label("打开 MCP 服务 "+key).FillWidth().Padding(settingsRowPadY, settingsRowPadX)
			if row.Clicked() {
				uiSt.mu.Lock()
				uiSt.selectedServer = key
				uiSt.expandedServers[key] = true
				uiSt.isFormOpen = false
				uiSt.mu.Unlock()
				openSettingsSheet(st, "mcp-detail", "")
				startMCPToolProbe(st, uiSt, srv)
			}
			row.Children(func() {
				ui.Row(c.Key("mcp-list-content-" + key)).FillWidth().Gap(Space3).AlignItems(ui.Center).Children(func() {
					ui.Text(c, srv.Name).FontSize(uiFont(TypeBody)).TextColor(ThemePrimaryText(c))
					Badge(c, "mcp-list-source-"+key, mcpSourceLabel(srv.Agent), ToneNeutral)
					if srv.ReadOnly {
						Badge(c, "mcp-list-readonly-"+key, "只读", ToneNeutral)
					}
				})
			})
		}
	}()
}

func renderMCPDetailSheet(c *ui.Context, st *SettingsState) {
	snap := mcpPageSnapshot(st)
	snap.uiSt.mu.Lock()
	selected := snap.uiSt.selectedServer
	formOpen := snap.uiSt.isFormOpen
	snap.uiSt.mu.Unlock()
	for i, srv := range snap.servers {
		if mcp.EndpointKey(srv.Agent, srv.Scope, srv.Name) != selected {
			continue
		}
		renderSettingsSection(c, "sec-mcp-detail", "", func() {
			row := ui.Column(c.Key("mcp-detail-server"))
			for j, other := range snap.servers {
				if j != i && other.Name == srv.Name {
					row.Tooltip("同名服务共用工具开关")
					break
				}
			}
			row.Children(func() {
				renderMCPServerRow(c, st, snap.uiSt, srv, i, snap.client, snap.authBusy, snap.authErr, snap.staticAuth, strings.EqualFold(srv.Agent, "custom") || strings.EqualFold(srv.Agent, "mcode"))
			})
		})
		if !srv.ReadOnly {
			renderSettingsSection(c, "sec-mcp-newthread", "", func() { renderMCPNewThreadDefaultsBody(c, st) })
			if formOpen {
				renderSettingsSection(c, "sec-mcp-edit", "", func() { renderMCPCustomForm(c, st, snap.uiSt) })
			}
		}
		// 确认框只在详情中渲染一次，避免重复叠加。
		renderMCPServerDeleteModal(c, st, snap.uiSt)
		return
	}
}

func renderMCPAddSheet(c *ui.Context, st *SettingsState) {
	uiSt := getMCPUIState(st)
	uiSt.mu.Lock()
	tab := uiSt.addTab
	uiSt.mu.Unlock()
	Segmented(c, "mcp-add-mode", &tab, []Segment{{Label: "从预设添加"}, {Label: "手动填写"}}, func(next int) {
		uiSt.mu.Lock()
		uiSt.addTab = next
		uiSt.mu.Unlock()
		st.invalidate()
	})
	renderSettingsSection(c, "sec-mcp-add", "", func() {
		if tab == 0 {
			renderMCPCatalogBody(c, st)
		} else {
			renderMCPCustomForm(c, st, uiSt)
		}
	})
}

// parseMCPHeaderLines 解析请求头输入：兼容 KEY: VALUE 与 KEY=VALUE，逗号、分号、换行均可分隔，与 env 输入一致。
func parseMCPHeaderLines(raw string) map[string]string {
	headers := make(map[string]string)
	tokens := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == ';'
	})
	for _, t := range tokens {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		k, v := t, ""
		if idx := strings.IndexAny(t, ":="); idx >= 0 {
			k = strings.TrimSpace(t[:idx])
			v = strings.TrimSpace(t[idx+1:])
		}
		if k != "" {
			headers[k] = v
		}
	}
	return headers
}

func setMCPToolFlag(uiSt *mcpPageUIState, flags map[string]map[string]bool, server, tool string, on bool) {
	uiSt.mu.Lock()
	defer uiSt.mu.Unlock()
	setMCPToolFlagsLocked(uiSt, flags, server, []string{tool}, on)
	saveMCPToolPolicyLocked(uiSt)
}

// setMCPToolFlags 批量更新某服务下一组工具的开关状态，只持久化一次整份策略。
func setMCPToolFlags(uiSt *mcpPageUIState, flags map[string]map[string]bool, server string, toolNames []string, on bool) {
	uiSt.mu.Lock()
	defer uiSt.mu.Unlock()
	setMCPToolFlagsLocked(uiSt, flags, server, toolNames, on)
	saveMCPToolPolicyLocked(uiSt)
}

// setMCPToolFlagsLocked 在已持锁的前提下写入开关状态，不触碰锁与持久化。
func setMCPToolFlagsLocked(uiSt *mcpPageUIState, flags map[string]map[string]bool, server string, toolNames []string, on bool) {
	if flags[server] == nil {
		flags[server] = make(map[string]bool)
	}
	for _, tool := range toolNames {
		if on {
			flags[server][tool] = true
		} else {
			delete(flags[server], tool)
		}
	}
}

// saveMCPToolPolicyLocked 在已持锁的前提下把整份工具策略写回文件，并记录失败提示。
func saveMCPToolPolicyLocked(uiSt *mcpPageUIState) {
	if uiSt.policyPath == "" {
		return
	}
	policy := agentconf.MCPToolPolicy{
		Disabled:    uiSt.disabledTools,
		AutoApprove: uiSt.autoApproveTools,
		// 必须一并回写，否则任意一次工具开关保存都会把折叠设置重置为默认值。
		FoldThreshold: uiSt.foldThreshold,
	}
	if err := agentconf.SaveMCPToolPolicy(uiSt.policyPath, policy); err != nil {
		uiSt.policyError = fmt.Sprintf("保存 MCP 工具策略失败：%v", err)
		return
	}
	uiSt.policyError = ""
}

// saveMCPNewThreadDisabledLocked 在已持锁的前提下把「新会话默认停用」名单写回全局 MCP 配置，
// 并记录失败提示；opts 由调用方在锁外构造（构造时要读 SettingsState 的锁）。
func saveMCPNewThreadDisabledLocked(uiSt *mcpPageUIState, opts agentconf.Options) {
	keys := make([]string, 0, len(uiSt.newThreadDisabled))
	for k, off := range uiSt.newThreadDisabled {
		if off {
			keys = append(keys, k)
		}
	}
	if err := agentconf.SetNewThreadDisabledServers(opts, keys); err != nil {
		uiSt.newThreadDisabledErr = fmt.Sprintf("保存新会话默认停用名单失败：%v", err)
		return
	}
	uiSt.newThreadDisabledErr = ""
}

// startMCPAuth 串行化同一远程服务的授权操作，避免重复行同时触发登录和退出。
func (st *SettingsState) startMCPAuth(serverID string, action func() error) {
	st.mu.Lock()
	if st.mcpAuthBusy[serverID] {
		st.mu.Unlock()
		return
	}
	// 授权会改变登录状态：动作开始前作废缓存，结束时再作废一次，下一帧重新读取。
	if st.mcpAuthBusy == nil {
		st.mcpAuthBusy = make(map[string]bool)
	}
	if st.mcpAuthErr == nil {
		st.mcpAuthErr = make(map[string]string)
	}
	st.mcpAuthBusy[serverID] = true
	delete(st.mcpAuthErr, serverID)
	st.mu.Unlock()
	// 放在锁外执行：可能首次创建页面状态并读取策略文件。
	uiSt := getMCPUIState(st)
	invalidateMCPSignedIn(uiSt, serverID)
	st.invalidate()
	go func() {
		err := action()
		invalidateMCPSignedIn(uiSt, serverID)
		uiSt.mu.Lock()
		delete(uiSt.authURLCopiedAt, serverID)
		uiSt.mu.Unlock()
		st.mu.Lock()
		delete(st.mcpAuthBusy, serverID)
		if err != nil {
			st.mcpAuthErr[serverID] = err.Error()
		} else {
			delete(st.mcpAuthErr, serverID)
		}
		st.mu.Unlock()
		st.invalidate()
	}()
}

// mcpAuthInspector 是授权过程的可选查看/取消能力：Client 接口不强制要求，
// 实现方（*app.Client）提供后设置页才会显示授权链接、设备码与取消按钮。
// 方法必须在渲染线程上可快速返回：只读已生成的状态，不做网络请求。
type mcpAuthInspector interface {
	MCPAuthURL(serverID string) string
	// MCPDeviceCode 返回设备流进行中的验证码与验证地址，没有设备流会话时 ok 为 false。
	MCPDeviceCode(serverID string) (userCode, verificationURI string, ok bool)
	MCPCancelSignIn(serverID string)
}

// mcpAuthLinkMaxLen 是界面上展示授权链接的最大字符数，超出部分用省略号收尾。
const mcpAuthLinkMaxLen = 80

// mcpAuthLinkDisplay 把过长的授权链接裁到 80 个字符加省略号；复制按钮仍拷完整链接。
func mcpAuthLinkDisplay(link string) string {
	runes := []rune(link)
	if len(runes) <= mcpAuthLinkMaxLen {
		return link
	}
	return string(runes[:mcpAuthLinkMaxLen]) + "…"
}

// markMCPAuthURLCopied 记录「复制链接」的回执时间，供按钮短暂显示「已复制」。
func markMCPAuthURLCopied(st *SettingsState, uiSt *mcpPageUIState, serverID string, at time.Time) {
	uiSt.mu.Lock()
	if uiSt.authURLCopiedAt == nil {
		uiSt.authURLCopiedAt = make(map[string]time.Time)
	}
	uiSt.authURLCopiedAt[serverID] = at
	uiSt.mu.Unlock()
	if st != nil {
		st.invalidate()
	}
}

// mcpAuthURLCopyLabel 在复制成功后的 1.5 秒内返回完成文案，并安排一次重绘让文案复原。
func mcpAuthURLCopyLabel(c *ui.Context, uiSt *mcpPageUIState, serverID, idle, copied string) string {
	uiSt.mu.Lock()
	at := uiSt.authURLCopiedAt[serverID]
	uiSt.mu.Unlock()
	if at.IsZero() {
		return idle
	}
	remaining := 1500*time.Millisecond - c.Now().Sub(at)
	if remaining <= 0 {
		return idle
	}
	c.After(remaining)
	return copied
}

func mcpSourceLabel(agent string) string {
	if strings.EqualFold(agent, "custom") || strings.EqualFold(agent, "mcode") {
		return "mcode"
	}
	return agent
}
