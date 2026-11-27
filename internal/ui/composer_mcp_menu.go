package mcodeui

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/agentconf"
	"mcode/internal/mcp"
)

// mcpMenuCacheTTL 是单个项目目录 MCP 扫描结果的缓存有效期（与技能扫描一致，1 分钟）。
const mcpMenuCacheTTL = 60 * time.Second

// 附件入口浮层的 Lucide 图标（上传、剪贴板、MCP 服务器）。
var (
	iconAttachUpload    = parseLucideSVG(`<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" y1="3" x2="12" y2="15"/>`)
	iconAttachClipboard = parseLucideSVG(`<rect width="8" height="4" x="8" y="2" rx="1" ry="1"/><path d="M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2"/>`)
	iconAttachServer    = parseLucideSVG(`<rect x="2" y="2" width="20" height="8" rx="2"/><rect x="2" y="14" width="20" height="8" rx="2"/><line x1="6" y1="6" x2="6.01" y2="6"/><line x1="6" y1="18" x2="6.01" y2="18"/>`)
)

// mcpMenuCacheEntry 是单个项目目录的 MCP 扫描缓存。
type mcpMenuCacheEntry struct {
	servers  []agentconf.MCPServer
	loadedAt time.Time
	loading  bool
	err      error
}

// mcpMenuCache 按项目路径缓存 MCP 服务器列表。与技能缓存同构：磁盘扫描结果，与后端会话无关，
// 因此放包级缓存而不是 chatState 字段。
var mcpMenuCache = struct {
	mu      sync.Mutex
	entries map[string]*mcpMenuCacheEntry
}{entries: make(map[string]*mcpMenuCacheEntry)}

// scanMCPMenuServers 是 MCP 扫描入口，测试里替换成固定列表以隔离磁盘。
var scanMCPMenuServers = agentconf.ScanMCP

// resetMCPMenuCache 清空 MCP 菜单缓存，仅供测试在替换 scanMCPMenuServers 后隔离用例。
func resetMCPMenuCache() {
	mcpMenuCache.mu.Lock()
	mcpMenuCache.entries = make(map[string]*mcpMenuCacheEntry)
	mcpMenuCache.mu.Unlock()
}

// mcpMenuServersForProject 返回 projectPath 下缓存的 MCP 服务器列表，第二项表示缓存是否就绪。
// 未命中或已过期时启动一次后台扫描，扫描结束后 invalidate() 触发重绘。
func (s *chatState) mcpMenuServersForProject(projectPath string) ([]agentconf.MCPServer, bool) {
	now := time.Now()

	mcpMenuCache.mu.Lock()
	if mcpMenuCache.entries == nil {
		mcpMenuCache.entries = make(map[string]*mcpMenuCacheEntry)
	}
	if entry := mcpMenuCache.entries[projectPath]; entry != nil {
		if entry.loading || now.Sub(entry.loadedAt) < mcpMenuCacheTTL {
			servers, ready := entry.servers, !entry.loading
			mcpMenuCache.mu.Unlock()
			return servers, ready
		}
	}
	entry := &mcpMenuCacheEntry{loading: true}
	mcpMenuCache.entries[projectPath] = entry
	mcpMenuCache.mu.Unlock()

	opts := agentconf.Options{
		Home:       skillScanHome(),
		ProjectDir: projectPath,
		DataDir:    resolveDataDirFallback(),
	}

	go func() {
		servers, err := scanMCPMenuServers(opts)

		mcpMenuCache.mu.Lock()
		if cur := mcpMenuCache.entries[projectPath]; cur == entry {
			cur.loading = false
			cur.loadedAt = time.Now()
			cur.err = err
			if err == nil {
				cur.servers = servers
			}
		}
		mcpMenuCache.mu.Unlock()

		if s != nil {
			s.invalidate()
		}
	}()

	return nil, false
}

// threadMCPProviders 记录支持「按会话停用 MCP」的 provider（后端注入时会读 Thread.MCPDisabled 的那些）。
// 其余 provider 没有这条链路：菜单入口置灰并说明，不做看着生效、实际没生效的假开关。
var threadMCPProviders = map[string]bool{
	"acp":         true,
	"agy":         true,
	"claude":      true,
	"codex":       true,
	"commandcode": true,
	"copilot":     true,
	"mcode":       true,
	"openai":      true,
	"pi":          true,
}

// providerSupportsThreadMCP 判断 provider 是否支持按会话启停外部 MCP；pi 的 "pi:" 端点前缀先归一。
func providerSupportsThreadMCP(provider string) bool {
	if provider == "" {
		return false
	}
	if base, ok := strings.CutPrefix(provider, "pi:"); ok && base != "" {
		provider = "pi"
	}
	return threadMCPProviders[provider]
}

// threadSupportsThreadMCP 判断当前会话的 Agent 是否支持按会话启停外部 MCP。
func (s *chatState) threadSupportsThreadMCP(threadID string) bool {
	s.mu.Lock()
	provider := ""
	th, ok := s.threadSnapshotLocked(threadID)
	if ok {
		provider = th.Provider
	}
	s.mu.Unlock()
	return providerSupportsThreadMCP(provider)
}

// openThreadMCPMenu 进入 MCP 二级页：先把会话当前的禁用集合拷进待提交缓冲区，
// 用户勾选后点「应用」才真正下发，避免一次点击就改掉会话配置。
func (s *chatState) openThreadMCPMenu(threadID string) {
	if !s.threadSupportsThreadMCP(threadID) {
		s.setToast("当前 Agent 不支持外部 MCP，仅内置端点生效")
		return
	}
	s.mu.Lock()
	th, ok := s.threadSnapshotLocked(threadID)
	s.mu.Unlock()
	pending := make(map[string]bool)
	if ok {
		for _, key := range th.MCPDisabled {
			pending[key] = true
		}
	}
	s.composerView.mcpPendingDisabled = pending
	setMCPMenuVisited(s, true)
	s.composerView.mcpMenuOpen = true
	s.invalidate()
}

// applyThreadMCPDisabled 下发会话级的 MCP 禁用集合；成功后同步本地会话记录并提示，
// 失败保留待提交状态以便重试（不假装已经生效）。
func (s *chatState) applyThreadMCPDisabled(threadID string, keys []string) {
	if threadID == "" {
		return
	}
	s.mu.Lock()
	if s.composerView.mcpSaving {
		s.mu.Unlock()
		return
	}
	s.composerView.mcpSaving = true
	s.mu.Unlock()
	s.invalidate()

	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)

	go func() {
		err := s.client.SetThreadMCPDisabled(context.Background(), threadID, sorted)
		s.mu.Lock()
		s.composerView.mcpSaving = false
		if err == nil {
			for i := range s.threads {
				if s.threads[i].ID == threadID {
					s.threads[i].MCPDisabled = append([]string(nil), sorted...)
				}
			}
			for proj := range s.threadsByProj {
				list := s.threadsByProj[proj]
				for i := range list {
					if list[i].ID == threadID {
						list[i].MCPDisabled = append([]string(nil), sorted...)
					}
				}
			}
		}
		s.mu.Unlock()
		if err != nil {
			s.setToast("保存会话 MCP 设置失败: " + err.Error())
		} else {
			s.setToast("已保存会话的 MCP 设置")
		}
		s.invalidate()
	}()
}

// mcpMenuSaving 返回会话 MCP 设置是否正在下发（受 s.mu 保护，供渲染路径读取）。
func (s *chatState) mcpMenuSaving() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.composerView.mcpSaving
}

// renderAttachMenuPanel 渲染附件按钮浮层的内容：
// 一级页从上到下依次为「选择文件或文件夹…」「粘贴剪贴板内容」「最近下载」（可折叠，默认展开）与「MCP 服务器」入口；
// 点「MCP 服务器」后在同一浮层切到二级页，点返回回到一级页。
func renderAttachMenuPanel(c *ui.Context, s *chatState, threadID string, onClose func()) {
	if s.composerView.mcpMenuOpen {
		renderMCPSelectionPanel(c, s, threadID, func() {
			s.composerView.mcpMenuOpen = false
			s.invalidate()
		})
		return
	}

	panel := ui.Column(c.Key("composer-attach-menu")).
		Width(360).
		Padding(Space3).
		Gap(Space1).
		OnUnmount(func() {
			if !s.composerView.mcpMenuOpen {
				setMCPMenuVisited(s, false)
			}
		})

	if mcpMenuVisited(s) {
		panel.Transition(motionTransition(c, ui.ElementTransition{
			Duration: DurSnap,
			Ease:     EaseSnap,
			Colors:   true,
			Enter:    &ui.Motion{X: -motionRiseOffset * 2, Opacity: 0},
		}))
	}

	if !isAppFlyoutServices(c.Services()) {
		panel = popoverCard(c, panel)
	}

	panel.Children(func() {
		// 主操作：选择文件或文件夹…
		pickBtn := ui.ButtonBase(c.Key("composer-attach-pick-files")).
			Label("选择文件或文件夹…").
			FillWidth().
			Height(ControlLG).
			Radius(RadiusSM).
			Padding(0, Space3).
			Justify(ui.Start).
			Background(ui.Transparent).
			OnClick(func() {
				promptChooseFiles(s)
				if onClose != nil {
					onClose()
				}
			})
		composerHoverBg(c, pickBtn, pickBtn.Hovered())
		focusRing(c, pickBtn)
		pickBtn.Children(func() {
			ui.Row(c.Key("composer-attach-pick-files-row")).
				FillWidth().
				AlignItems(ui.Center).
				Gap(Space3).
				Children(func() {
					ui.Icon(c, iconAttachUpload).
						Size(IconMD, IconMD).
						TextColor(ThemeAccent(c)).
						Shrink(0)
					ui.Text(c, "选择文件或文件夹…").
						FontSize(uiFont(TypeBody)).
						FontWeight(weightFor(c, 500)).
						TextColor(ThemePrimaryText(c)).
						Grow(1).
						SingleLine()
				})
		})

		// 次操作：粘贴剪贴板内容（与 ⌘V 走相同的剪贴板附件入口）
		pasteBtn := ui.ButtonBase(c.Key("composer-attach-paste-clipboard")).
			Label("粘贴剪贴板内容").
			FillWidth().
			Height(ControlLG).
			Radius(RadiusSM).
			Padding(0, Space3).
			Justify(ui.Start).
			Background(ui.Transparent).
			OnClick(func() {
				if s != nil {
					if !s.tryPasteImage() {
						s.setToast("剪贴板中没有图片或文件")
					}
				}
				if onClose != nil {
					onClose()
				}
			})
		composerHoverBg(c, pasteBtn, pasteBtn.Hovered())
		focusRing(c, pasteBtn)
		pasteBtn.Children(func() {
			ui.Row(c.Key("composer-attach-paste-clipboard-row")).
				FillWidth().
				AlignItems(ui.Center).
				Gap(Space3).
				Children(func() {
					ui.Icon(c, iconAttachClipboard).
						Size(IconMD, IconMD).
						TextColor(ThemeSecondaryText(c)).
						Shrink(0)
					ui.Text(c, "粘贴剪贴板内容").
						FontSize(uiFont(TypeBody)).
						TextColor(ThemePrimaryText(c)).
						Grow(1).
						SingleLine()
					ui.Text(c, shortcutDisplay("⌘V")).
						FontSize(uiFont(TypeCaption)).
						TextColor(ThemeTertiaryText(c)).
						Shrink(0)
				})
		})

		// 分隔线
		ui.Row(c.Key("composer-attach-divider-top")).
			FillWidth().
			Height(Hairline).
			Background(ThemeDivider(c)).
			Margin(Space1, 0, Space1, 0)

		// 可折叠的「最近下载」分组
		renderDownloadsPanel(c, s, onClose)

		// 分隔线
		ui.Row(c.Key("composer-attach-divider-bottom")).
			FillWidth().
			Height(Hairline).
			Background(ThemeDivider(c)).
			Margin(Space1, 0, Space1, 0)

		// 底部：MCP 服务器二级页入口
		renderMCPMenuEntry(c, s, threadID)
	})
}

// renderMCPMenuEntry 渲染附件浮层里的「MCP 服务器」入口行。
// 当前 Agent 不支持按会话启停时整行置灰并在行尾写明原因，点击只给提示。
func renderMCPMenuEntry(c *ui.Context, s *chatState, threadID string) {
	supported := s.threadSupportsThreadMCP(threadID)
	tooltip := "选择本会话启用的 MCP 服务器"
	trailing := "›"
	if !supported {
		tooltip = "当前 Agent 不支持外部 MCP，仅内置端点生效"
		trailing = "不支持"
	}
	menuRow(c, menuRowProps{
		Key:      "composer-mcp-menu-entry",
		Label:    "MCP 服务器",
		Icon:     iconAttachServer,
		Title:    "MCP 服务器",
		Trailing: trailing,
		Disabled: !supported,
		OnClick: func() {
			s.openThreadMCPMenu(threadID)
		},
	}).Tooltip(tooltip)
}

// renderMCPSelectionPanel 渲染 MCP 逐会话选择页：顶部返回、中间服务器开关列表、底部「应用」。
// 只改本地待提交集合，点「应用」才调 SetThreadMCPDisabled。
func renderMCPSelectionPanel(c *ui.Context, s *chatState, threadID string, onBack func()) {
	supported := s.threadSupportsThreadMCP(threadID)
	s.mu.Lock()
	projectPath := s.projectPathForThreadLocked(threadID)
	s.mu.Unlock()
	var servers []agentconf.MCPServer
	ready := false
	if supported {
		servers, ready = s.mcpMenuServersForProject(projectPath)
	}
	saving := s.mcpMenuSaving()

	panel := ui.Column(c.Key("composer-mcp-panel")).
		Width(360).
		Padding(Space4).
		Gap(Space3).
		Transition(motionTransition(c, ui.ElementTransition{
			Duration: DurSnap,
			Ease:     EaseSnap,
			Colors:   true,
			Enter:    &ui.Motion{X: motionRiseOffset * 2, Opacity: 0},
		}))

	panel.Children(func() {
		// 标题栏：返回按钮 + 标题
		ui.Row(c.Key("composer-mcp-header")).
			FillWidth().
			Height(ControlSM).
			AlignItems(ui.Center).
			Gap(Space2).
			Children(func() {
				pressButton0 := ui.ButtonBase(c.Key("composer-mcp-back")).
					Label("返回").
					Tooltip("返回").
					Height(ControlXS).
					Radius(RadiusPill).
					Padding(0, Space3).
					Background(ui.Transparent).
					OnClick(onBack)
				pressScale0 := motionPressScale(c, pressButton0, "button-press-scale", true && pressButton0.Pressed())
				pressButton0.Scale(pressScale0, pressScale0)
				pressButton0.Children(func() {
					ui.Text(c, "‹ 返回").
						FontSize(uiFont(TypeFootnote)).
						TextColor(ThemeSecondaryText(c))
				})
				ui.Text(c, "MCP 服务器").
					FontSize(uiFont(TypeBody)).
					FontWeight(600).
					TextColor(ThemePrimaryText(c))
			})

		if !supported {
			ui.Text(c, "当前 Agent 不支持外部 MCP，仅内置端点生效。").
				FontSize(uiFont(TypeFootnote)).
				TextColor(ThemeSecondaryText(c)).
				Padding(Space1, 0, Space1, 0)
			return
		}

		switch {
		case !ready:
			ui.Text(c, "正在读取 MCP 服务器…").
				FontSize(uiFont(TypeFootnote)).
				TextColor(ThemeSecondaryText(c)).
				Padding(Space3, 0, Space3, 0)
		case len(servers) == 0:
			ui.Text(c, "没有发现 MCP 服务器").
				FontSize(uiFont(TypeFootnote)).
				TextColor(ThemeSecondaryText(c)).
				Padding(Space3, 0, Space3, 0)
		default:
			ui.Scroll(c.Key("composer-mcp-list")).
				MaxHeight(240).
				Children(func() {
					ui.Column(c.Key("composer-mcp-rows")).
						FillWidth().
						Gap(2).
						FocusGroup(ui.Vertical).
						Children(func() {
							for i := range servers {
								renderMCPSelectionRow(c, s, servers[i])
							}
						})
				})
		}

		// 底部应用条：下发中置灰，避免重复提交。
		ui.Row(c.Key("composer-mcp-footer")).
			FillWidth().
			Justify(ui.End).
			AlignItems(ui.Center).
			Children(func() {
				applyBtn := ui.ButtonBase(c.Key("composer-mcp-apply")).
					Label("应用").
					Tooltip("保存当前会话的 MCP 选择").
					Height(ControlSM).
					Radius(RadiusLG).
					Padding(0, Space4).
					Background(ThemeAccent(c)).
					OnClick(func() {
						s.applyThreadMCPDisabled(threadID, mcpDisabledKeys(s.composerView.mcpPendingDisabled))
					})
				applyBtn.Disabled(saving)
				scale := motionPressScale(c, applyBtn, "button-press-scale", true && applyBtn.Pressed())
				applyBtn.Scale(scale, scale)
				applyBtn.Children(func() {
					ui.Text(c, "应用").
						FontSize(uiFont(TypeFootnote)).
						TextColor(ThemeOnAccent(c))
				})
			})
	})

	popoverCard(c, panel)
}

// renderMCPSelectionRow 渲染单个 MCP 服务器的勾选行。
// 全局停用、未信任或只读的服务不可在这里改，行内直接写明原因。
func renderMCPSelectionRow(c *ui.Context, s *chatState, srv agentconf.MCPServer) {
	key := mcp.EndpointKey(srv.Agent, srv.Scope, srv.Name)
	locked, reason := mcpServerLockReason(srv)
	enabled := !s.composerView.mcpPendingDisabled[key]
	if locked {
		enabled = false
	}

	label := mcpServerRowLabel(srv)
	menuRow(c, menuRowProps{
		Key:      "composer-mcp-row-" + key,
		Label:    label,
		Title:    label,
		Trailing: reason,
		Checked:  enabled,
		Disabled: locked,
		OnClick: func() {
			if locked {
				s.setToast(reason)
				return
			}
			if s.composerView.mcpPendingDisabled == nil {
				s.composerView.mcpPendingDisabled = make(map[string]bool)
			}
			if enabled {
				s.composerView.mcpPendingDisabled[key] = true
			} else {
				delete(s.composerView.mcpPendingDisabled, key)
			}
			s.invalidate()
		},
	}).Tooltip(mcpServerRowTooltip(srv, reason))
}

// mcpServerLockReason 判断服务是否被全局设置锁住，并给出可读原因。
func mcpServerLockReason(srv agentconf.MCPServer) (bool, string) {
	switch {
	case srv.ReadOnly:
		return true, "只读"
	case srv.Untrusted:
		return true, "待信任"
	case srv.Disabled:
		return true, "全局已停用"
	}
	return false, ""
}

// mcpServerRowLabel 是列表行的显示名：名称 + 来源 Agent，避免同名服务分不清。
func mcpServerRowLabel(srv agentconf.MCPServer) string {
	name := srv.Name
	if name == "" {
		name = srv.URL
	}
	if srv.Agent == "" {
		return name
	}
	return name + " · " + srv.Agent
}

// mcpServerRowTooltip 是列表行的悬停说明：带来源与地址，锁住时补一句原因。
func mcpServerRowTooltip(srv agentconf.MCPServer, reason string) string {
	text := mcpServerRowLabel(srv)
	if srv.Source != "" {
		text += "\n来源: " + srv.Source
	}
	if srv.URL != "" {
		text += "\n" + srv.URL
	} else if srv.Command != "" {
		text += "\n" + srv.Command
	}
	if reason != "" {
		text += "\n" + reason + "：需先在 MCP 设置里处理"
	}
	return text
}

// mcpDisabledKeys 把待提交的禁用集合展开成排序后的键列表（nil 表示全部启用）。
func mcpDisabledKeys(pending map[string]bool) []string {
	if len(pending) == 0 {
		return nil
	}
	keys := make([]string, 0, len(pending))
	for key, disabled := range pending {
		if disabled {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}
