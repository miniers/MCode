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

// mcpMenuSlideDuration 是 MCP 二级页滑入的时长；只取时长常量，不引用 motion.go 里的曲线变量。
const mcpMenuSlideDuration = 150 * time.Millisecond

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
	} else if s.pendingThread != nil {
		provider = s.pendingThread.Thread.Provider
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

// renderAttachMenuPanel 渲染附件按钮浮层的内容：一级页是「最近下载」面板加一条「MCP 服务器」入口，
// 点进入口后在同一浮层里切到 MCP 逐会话选择页（二级页），点返回回到一级页。
func renderAttachMenuPanel(c *ui.Context, s *chatState, threadID string, onClose func()) {
	if s.composerView.mcpMenuOpen {
		renderMCPSelectionPanel(c, s, threadID, func() {
			s.composerView.mcpMenuOpen = false
		})
		return
	}
	ui.Column(c.Key("composer-attach-menu")).
		Width(360).
		Gap(Space3).
		Children(func() {
			renderDownloadsPanel(c, s, onClose)
			renderMCPMenuEntry(c, s, threadID)
		})
}

// renderMCPMenuEntry 渲染附件浮层里的「MCP 服务器」入口行。
// 当前 Agent 不支持按会话启停时整行置灰并在行尾写明原因，点击只给提示。
func renderMCPMenuEntry(c *ui.Context, s *chatState, threadID string) {
	supported := s.threadSupportsThreadMCP(threadID)
	tooltip := "选择本会话启用的 MCP 服务器"
	if !supported {
		tooltip = "当前 Agent 不支持外部 MCP，仅内置端点生效"
	}
	row := ui.ButtonBase(c.Key("composer-mcp-menu-entry")).
		Label("MCP 服务器").
		Tooltip(tooltip).
		FillWidth().
		Height(ControlLG).
		Radius(RadiusMD).
		Padding(0, Space3).
		Background(ThemeSurface(c)).
		Border(1, ThemeBorder(c)).
		Disabled(!supported).
		OnClick(func() {
			s.openThreadMCPMenu(threadID)
		})
	row.Children(func() {
		ui.Row(c.Key("composer-mcp-menu-entry-content")).
			FillWidth().
			AlignItems(ui.Center).
			Gap(Space3).
			Children(func() {
				ui.Text(c, "MCP 服务器").
					FontSize(TypeFootnote).
					TextColor(mcpMenuTextColor(c, supported))
				ui.Spacer(c)
				if !supported {
					ui.Text(c, "不支持").
						FontSize(TypeCaption).
						TextColor(ThemeSecondaryText(c))
				} else {
					ui.Text(c, "›").
						FontSize(TypeFootnote).
						TextColor(ThemeSecondaryText(c))
				}
			})
	})
}

// mcpMenuTextColor 返回入口/行的文字颜色：不可用时用次要色，避免看起来可点。
func mcpMenuTextColor(c *ui.Context, enabled bool) ui.Color {
	if enabled {
		return ThemePrimaryText(c)
	}
	return ThemeSecondaryText(c)
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
			Duration: mcpMenuSlideDuration,
			Colors:   true,
			Enter:    &ui.Motion{X: 8, Opacity: 0},
		}))

	panel.Children(func() {
		// 标题栏：返回按钮 + 标题
		ui.Row(c.Key("composer-mcp-header")).
			FillWidth().
			Height(ControlSM).
			AlignItems(ui.Center).
			Gap(Space2).
			Children(func() {
				ui.ButtonBase(c.Key("composer-mcp-back")).
					Label("返回").
					Tooltip("返回").
					Height(ControlXS).
					Radius(RadiusPill).
					Padding(0, Space3).
					Background(ui.Transparent).
					OnClick(onBack).
					Children(func() {
						ui.Text(c, "‹ 返回").
							FontSize(TypeFootnote).
							TextColor(ThemeSecondaryText(c))
					})
				ui.Text(c, "MCP 服务器").
					FontSize(TypeBody).
					FontWeight(600).
					TextColor(ThemePrimaryText(c))
			})

		if !supported {
			ui.Text(c, "当前 Agent 不支持外部 MCP，仅内置端点生效。").
				FontSize(TypeFootnote).
				TextColor(ThemeSecondaryText(c)).
				Padding(Space1, 0, Space1, 0)
			return
		}

		ui.Text(c, "勾选仅对当前会话生效，取消勾选表示本会话不注入该服务。").
			FontSize(TypeCaption).
			TextColor(ThemeSecondaryText(c))

		switch {
		case !ready:
			ui.Text(c, "正在读取 MCP 服务器…").
				FontSize(TypeFootnote).
				TextColor(ThemeSecondaryText(c)).
				Padding(Space3, 0, Space3, 0)
		case len(servers) == 0:
			ui.Text(c, "没有发现 MCP 服务器").
				FontSize(TypeFootnote).
				TextColor(ThemeSecondaryText(c)).
				Padding(Space3, 0, Space3, 0)
		default:
			ui.Scroll(c.Key("composer-mcp-list")).
				MaxHeight(240).
				Children(func() {
					ui.Column(c.Key("composer-mcp-rows")).
						FillWidth().
						Gap(2).
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
				applyBtn.Children(func() {
					ui.Text(c, "应用").
						FontSize(TypeFootnote).
						TextColor(ThemeOnAccent(c))
				})
			})
	})

	popoverCardShadow(popoverCard(c, panel))
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

	row := ui.ButtonBase(c.Key("composer-mcp-row-"+key)).
		Label(mcpServerRowLabel(srv)).
		Tooltip(mcpServerRowTooltip(srv, reason)).
		FillWidth().
		Height(ControlLG).
		Radius(RadiusSM).
		Padding(0, Space3).
		Background(ui.Transparent).
		Disabled(locked).
		OnClick(func() {
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
		})

	row.Children(func() {
		ui.Row(c.Key("composer-mcp-row-content-" + key)).
			FillWidth().
			AlignItems(ui.Center).
			Gap(Space3).
			Children(func() {
				// 勾选框：选中用强调色实心，未选中只留边框。
				box := ui.Box(c.Key("composer-mcp-check-"+key)).Size(IconSM, IconSM).Radius(RadiusXS).Border(1, ThemeBorder(c))
				if enabled {
					box.Background(ThemeAccent(c))
				}
				if enabled {
					ui.Text(c, "✓").
						FontSize(TypeCaption).
						TextColor(ThemeOnAccent(c))
				}
				ui.Text(c, mcpServerRowLabel(srv)).
					FontSize(TypeFootnote).
					TextColor(mcpMenuTextColor(c, !locked)).
					Grow(1).
					MaxLines(1)
				if locked {
					ui.Text(c, reason).
						FontSize(TypeCaption).
						TextColor(ThemeSecondaryText(c)).
						MaxLines(1)
				}
			})
	})
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
