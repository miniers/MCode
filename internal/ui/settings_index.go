package mcodeui

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// settingsItem 是设置面板里可被逐项搜索命中的一条设置项。
//
// ID 就是页面树行渲染器（renderSettingRow）用的控件 key，由 renderSettingsTreeSetting 传下去；
// Page 是新版设置页的 id（与 settingsPages() 一致）；Block 表示区块级条目（列表区而非单行控件），
// Dynamic 表示按数据循环生成的模板行，此时 ID 只登记固定前缀。
type settingsItem struct {
	ID       string
	Page     string
	Sheet    string
	Section  string
	Title    string
	Aliases  []string
	Advanced bool
	// NmemOnly 表示只在 Nowledge Mem 后端渲染的行：本地记忆后端下这类行不渲染，
	// 逐项搜索也必须过滤掉，否则点进去会落到一个不存在的行。
	NmemOnly bool
	Block    bool
	Dynamic  bool
}

// settingsItemHit 是一条匹配结果；rank 越小越靠前（标题前缀 0、标题包含 1、别名 2）。
type settingsItemHit struct {
	Item settingsItem
	rank int
}

// settingsItems 是设置项索引表，由两部分组成：手写表里尚未迁入页面树的条目，
// 以及页面树派生的条目。已迁入页面树的条目必须从 settingsLegacyItems 删除，
// settings_index_test.go 会对重复登记报错。
func settingsItems() []settingsItem {
	legacy := settingsLegacyItems()
	tree := settingsTreeItems()

	inTree := make(map[string]bool, len(tree))
	for _, item := range tree {
		inTree[item.ID] = true
	}

	items := make([]settingsItem, 0, len(legacy)+len(tree))
	for _, item := range legacy {
		if inTree[item.ID] {
			continue
		}
		items = append(items, item)
	}
	for _, item := range tree {
		items = append(items, settingsTreeItemToItem(item))
	}
	return items
}

// settingsLegacyItems 是尚未迁入页面树的手写索引表：声明式登记，新增设置行时补一行即可被搜索命中。
// settings_index_test.go 会解析源码里的行 key 与本表做双向比对，保证不漏不残。
func settingsLegacyItems() []settingsItem {
	return []settingsItem{
		// ---- 工作台：打开方式（按应用循环的模板行） ----
		{ID: "row-openin-", Page: "workbench", Sheet: "open-in", Section: "打开方式", Title: "打开方式", Aliases: []string{"应用", "外部编辑器"}, Dynamic: true},
		// ---- 工作台：快捷回复（列表区） ----
		{ID: "block-quick-replies", Page: "conversation", Sheet: "quick-replies", Section: "快捷回复", Title: "快捷回复", Aliases: []string{"快捷回复", "模板", "输入框"}, Block: true},

		// ---- 编排：子代理完成时（其余编排行已迁入页面树） ----
		{ID: "row-swarm-review-bees", Page: "swarm", Section: "子代理完成时", Title: "合并前审查", Aliases: []string{"审查", "AI 审查", "合入", "改动"}},
		{ID: "row-swarm-review-model", Page: "swarm", Section: "子代理完成时", Title: "审查模型", Aliases: []string{"审查模型", "模型", "自动"}},
		{ID: "row-swarm-review-check-command", Page: "swarm", Section: "子代理完成时", Title: "检查命令（本项目）", Aliases: []string{"检查命令", "构建检查", "驳回", "工作副本"}},
		// ---- 编排：存储 ----
		// ---- 编排：档位（列表区） ----

		// ---- Magpie：网关面板 ----
		{ID: "row-magpie-gateway-enabled", Page: "magpie", Sheet: "magpie-gateway", Section: "网关", Title: "启用", Aliases: []string{"magpie", "网关"}},
		{ID: "row-magpie-gateway-operations", Page: "magpie", Sheet: "magpie-gateway", Section: "网关", Title: "操作", Aliases: []string{"magpie", "网关"}},
		{ID: "row-magpie-gateway-name", Page: "magpie", Sheet: "magpie-gateway", Section: "网关", Title: "名称", Aliases: []string{"magpie", "网关"}},
		{ID: "row-magpie-gateway-baseurl", Page: "magpie", Sheet: "magpie-gateway", Section: "网关", Title: "接口地址（BaseURL）", Aliases: []string{"magpie", "网关", "地址"}},
		{ID: "row-magpie-gateway-model", Page: "magpie", Sheet: "magpie-gateway", Section: "网关", Title: "默认模型", Aliases: []string{"magpie", "网关", "模型"}},
		{ID: "row-magpie-gateway-apikey", Page: "magpie", Sheet: "magpie-gateway", Section: "网关", Title: "密钥（API Key）", Aliases: []string{"magpie", "网关", "密钥"}},
		{ID: "row-magpie-gateway-quota", Page: "magpie", Sheet: "magpie-gateway", Section: "网关", Title: "菜单栏额度", Aliases: []string{"magpie", "网关", "额度"}},
		// ---- Magpie：人设面板 ----
		{ID: "row-magpie-persona-weburl", Page: "magpie", Sheet: "magpie-personas", Section: "人设", Title: "人设管理地址", Aliases: []string{"magpie", "人设", "地址"}},
		{ID: "row-magpie-persona-webkey", Page: "magpie", Sheet: "magpie-personas", Section: "人设", Title: "人设管理密钥", Aliases: []string{"magpie", "人设", "密钥"}},
		{ID: "row-magpie-persona-json", Page: "magpie", Sheet: "magpie-personas", Section: "人设", Title: "手动人设列表", Aliases: []string{"magpie", "人设", "json"}},
		{ID: "row-magpie-persona-rules", Page: "magpie", Sheet: "magpie-personas", Section: "人设", Title: "人设规则", Aliases: []string{"magpie", "人设", "规则"}},
		{ID: "row-magpie-persona-test", Page: "magpie", Sheet: "magpie-personas", Section: "人设", Title: "测试人设", Aliases: []string{"magpie", "人设", "测试"}},

		// ---- 模型与供应商：供应商面板 ----
		{ID: "row-provider-balance", Page: "models", Sheet: "provider", Section: "供应商", Title: "余额", Aliases: []string{"供应商", "额度"}},
		{ID: "row-provider-bin", Page: "models", Sheet: "provider", Section: "供应商", Title: "可执行文件路径", Aliases: []string{"供应商", "路径"}},
		{ID: "row-provider-update-monitor", Page: "models", Sheet: "provider", Section: "供应商", Title: "监控更新", Aliases: []string{"供应商", "更新"}},
		{ID: "row-provider-pi-cache", Page: "models", Sheet: "provider", Section: "供应商", Title: "长缓存", Aliases: []string{"pi", "缓存", "PI_CACHE_RETENTION"}},
		{ID: "row-provider-pi-vision", Page: "models", Sheet: "provider", Section: "供应商", Title: "视觉模型", Aliases: []string{"pi", "图片"}},
		{ID: "row-provider-version", Page: "models", Sheet: "provider", Section: "供应商", Title: "版本信息", Aliases: []string{"供应商", "版本"}},
		{ID: "row-provider-auth", Page: "models", Sheet: "provider", Section: "供应商", Title: "认证方式", Aliases: []string{"供应商", "登录"}},
		{ID: "row-provider-magpie", Page: "models", Sheet: "provider", Section: "模型", Title: "Magpie 接入", Aliases: []string{"供应商", "Magpie", "路由"}},
		{ID: "row-provider-balance-breakdown", Page: "models", Sheet: "provider", Section: "供应商", Title: "明细", Aliases: []string{"供应商", "余额", "额度"}},
		{ID: "provider-model-", Page: "models", Sheet: "provider", Section: "供应商", Title: "供应商模型", Aliases: []string{"供应商", "模型"}, Dynamic: true},
		{ID: "provider-models-empty", Page: "models", Sheet: "provider", Section: "模型", Title: "暂无模型"},
		// ---- 模型与供应商：添加供应商面板 ----
		{ID: "row-provider-add-type", Page: "models", Sheet: "provider-add", Section: "添加供应商", Title: "类型", Aliases: []string{"供应商", "类型"}},
		{ID: "row-provider-add-apikey", Page: "models", Sheet: "provider-add", Section: "添加供应商", Title: "密钥（API Key）", Aliases: []string{"供应商", "密钥"}},
		{ID: "row-provider-add-actions", Page: "models", Sheet: "provider-add", Section: "添加供应商", Title: "供应商操作", Aliases: []string{"供应商", "操作"}},
		{ID: "row-provider-add-name", Page: "models", Sheet: "provider-add", Section: "添加供应商", Title: "名称", Aliases: []string{"供应商", "名称"}},
		{ID: "row-provider-add-baseurl", Page: "models", Sheet: "provider-add", Section: "添加供应商", Title: "接口地址（BaseURL）", Aliases: []string{"供应商", "地址"}},
		{ID: "row-provider-add-model", Page: "models", Sheet: "provider-add", Section: "添加供应商", Title: "默认模型", Aliases: []string{"供应商", "模型"}},
		{ID: "row-provider-add-cli-", Page: "models", Sheet: "provider-add", Section: "添加供应商", Title: "命令行供应商", Aliases: []string{"供应商", "cli"}, Dynamic: true},
		{ID: "row-provider-add-preset-", Page: "models", Sheet: "provider-add", Section: "添加供应商", Title: "预设供应商", Aliases: []string{"供应商", "预设"}, Dynamic: true},
		{ID: "row-provider-add-custom", Page: "models", Sheet: "provider-add", Section: "添加供应商", Title: "自定义端点", Aliases: []string{"供应商", "自定义", "端点"}},

		// ---- 模型与供应商：你的模型 ----
		{ID: "row-models-none", Page: "models", Section: "你的模型", Title: "尚未选择模型", Aliases: []string{"模型", "模型选择器"}},
		// ---- 模型与供应商：供应商账号（按供应商循环的模板行） ----
		{ID: "row-provider-copilot-account", Page: "models", Section: "供应商账号", Title: "GitHub Copilot 登录", Aliases: []string{"供应商", "登录"}},
		{ID: "row-provider-copilot-login", Page: "models", Section: "供应商账号", Title: "GitHub Copilot 登录", Aliases: []string{"供应商", "登录"}},
		// ---- 模型与供应商：供应商账号（其他登录方式） ----
		{ID: "row-claude-bin", Page: "models", Sheet: "provider", Section: "命令行", Title: "Claude 可执行文件路径", Aliases: []string{"可执行文件路径", "供应商"}},
		{ID: "row-codex-bin", Page: "models", Sheet: "provider", Section: "命令行", Title: "Codex 可执行文件路径", Aliases: []string{"可执行文件路径", "供应商"}},
		// ---- 模型与供应商：自定义 Endpoints / 本地网关 ----
		{ID: "row-ep-form-name", Page: "models", Section: "自定义 Endpoints / 本地网关", Title: "名称", Aliases: []string{"网关", "endpoint", "名字"}},
		{ID: "row-ep-form-baseurl", Page: "models", Section: "自定义 Endpoints / 本地网关", Title: "接口地址（BaseURL）", Aliases: []string{"网关", "endpoint", "host"}},
		{ID: "row-ep-form-model", Page: "models", Section: "自定义 Endpoints / 本地网关", Title: "默认模型", Aliases: []string{"网关", "透传", "model"}},
		{ID: "row-ep-form-apikey", Page: "models", Section: "自定义 Endpoints / 本地网关", Title: "密钥（API Key）", Aliases: []string{"网关", "密钥"}},
		// ---- 模型与供应商：API 供应商 ----
		{ID: "row-preset-form-baseurl", Page: "models", Section: "API 供应商", Title: "接口地址（BaseURL）", Aliases: []string{"供应商", "默认值"}},
		{ID: "row-preset-form-model", Page: "models", Section: "API 供应商", Title: "默认模型", Aliases: []string{"供应商", "默认值"}},
		{ID: "row-preset-form-apikey", Page: "models", Section: "API 供应商", Title: "密钥（API Key）", Aliases: []string{"供应商", "密钥", "key"}},

		// ---- 快捷键：各功能快捷键（按功能循环的模板行） ----
		{ID: "row-shortcut-", Page: "general", Sheet: "shortcuts", Section: "各功能快捷键", Title: "功能快捷键", Aliases: []string{"快捷键", "热键", "按键"}, Dynamic: true},

		// ---- 归档：自动休眠 ----
		// ---- 归档：已归档会话（按会话循环的模板行） ----
		{ID: "row-archive-", Page: "conversation", Sheet: "archive", Section: "已归档会话", Title: "已归档会话", Aliases: []string{"归档"}, Dynamic: true},
		// ---- 归档：Worktree 清理（列表区） ----
		{ID: "block-archive-worktrees", Page: "conversation", Sheet: "archive", Section: "Worktree 清理", Title: "工作树清理", Aliases: []string{"工作树", "清理", "worktree", "分支"}, Block: true},

		// ---- 关于 ----
		{ID: "row-about-version", Page: "about", Section: "关于 MCode", Title: "版本号", Aliases: []string{"构建"}},
		{ID: "row-about-datadir", Page: "about", Section: "关于 MCode", Title: "数据目录", Aliases: []string{"目录", "路径", "存储"}},
		{ID: "row-about-update", Page: "about", Section: "关于 MCode", Title: "检查更新", Aliases: []string{"更新", "新版本"}},
		{ID: "row-about-auto-update", Page: "about", Section: "关于 MCode", Title: "自动检查更新", Aliases: []string{"更新", "新版本"}},
		{ID: "row-about-diag", Page: "about", Section: "关于 MCode", Title: "导出诊断日志", Aliases: []string{"日志", "诊断", "反馈"}},

		// ---- MCP 旧搜索入口 ----
		{ID: "block-mcp-catalog", Page: "extensions", Section: "MCP 服务", Title: "MCP 预设", Aliases: mcpCatalogSearchAliases(), Block: true},
		// ---- MCP：新会话默认（按服务循环的模板行） ----
		{ID: "row-mcp-newthread-", Page: "extensions", Section: "新会话默认", Title: "新会话默认启用的 MCP 服务", Aliases: []string{"mcp", "新会话", "默认", "新建会话"}, Dynamic: true},
	}
}

func mcpCatalogSearchAliases() []string {
	aliases := []string{"mcp", "预设", "添加"}
	for _, entry := range cachedMCPCatalog().entries {
		aliases = append(aliases, entry.haystack...)
	}
	return aliases
}

// settingsTreeItemToItem 把页面树派生的条目转成索引条目：Page、Title、Aliases 照抄，
// Section 换成 section 标题（面板行取打开该面板的 link 所在页），list 前缀条目标为动态。
func settingsTreeItemToItem(item settingsTreeItem) settingsItem {
	section, page, dynamic := settingsTreeItemMeta(item)
	if item.ID == "block-mcp-custom" {
		item.Aliases = append(append([]string(nil), item.Aliases...), "Antigravity")
	}
	if item.ID == "row-nowledge-sync-helper-threads" {
		item.Aliases = append(append([]string(nil), item.Aliases...), "空闲")
	}
	return settingsItem{
		ID:      item.ID,
		Page:    page,
		Sheet:   item.Sheet,
		Section: section,
		Title:   item.Title,
		Aliases: item.Aliases,
		Dynamic: dynamic,
	}
}

// settingsTreeItemMeta 在页面树里按 section ID 查标题与所属页。
// 面板行取打开该面板的 link 所在页；实在查不到 section 时用面板标题兜底、Page 留空，
// 这样索引里不会出现页面集合之外的 id。
func settingsTreeItemMeta(item settingsTreeItem) (string, string, bool) {
	for _, p := range settingsTreePages() {
		for _, sec := range p.Sections {
			if sec.ID != item.Section {
				continue
			}
			return sec.Title, p.ID, settingsTreeSectionHasListPrefix(sec, item.ID)
		}
	}
	for _, sh := range settingsTreeSheets() {
		for _, sec := range sh.Sections {
			if sec.ID != item.Section {
				continue
			}
			page := settingsTreePageOpeningSheet(sh.ID)
			if page == "" {
				page = settingsSheetPageFallback(sh.ID)
			}
			return sec.Title, page, settingsTreeSectionHasListPrefix(sec, item.ID)
		}
	}
	if sh, ok := settingsTreeSheetOf(item.Sheet); ok {
		return sh.Title, settingsSheetPageFallback(sh.ID), false
	}
	return item.Section, "", false
}

// settingsTreeSectionHasListPrefix 判断条目是否由 section 里的 list 控件前缀派生。
func settingsTreeSectionHasListPrefix(sec settingsSection, id string) bool {
	for _, s := range sec.Rows {
		if lc, ok := s.Control.(listControl); ok && lc.prefix == id {
			return true
		}
	}
	return false
}

// settingsTreePageOpeningSheet 返回用 link 打开该面板的页；没有入口行时返回空串。
func settingsTreePageOpeningSheet(sheet string) string {
	for _, p := range settingsTreePages() {
		for _, sec := range p.Sections {
			for _, s := range sec.Rows {
				if lc, ok := s.Control.(linkControl); ok && lc.sheet == sheet {
					return p.ID
				}
			}
		}
	}
	return ""
}

// matchSettingsItems 在索引里按标题前缀、标题包含、别名包含的顺序匹配设置项，
// 同分按页顺序、再按表内顺序排列。查询少于 2 个字符（按 rune 计）时返回空。
func matchSettingsItems(query string) []settingsItemHit {
	return matchSettingsItemsIn(query, settingsItems())
}

// settingsItemVisible 保留手写索引，并按声明树检查页面及面板行的当前可见性。
func settingsItemVisible(st *SettingsState, item settingsItem) bool {
	check := func(sections []settingsSection, inSheet bool) (bool, bool) {
		for _, section := range sections {
			for _, row := range section.Rows {
				id := row.ID
				if lc, ok := row.Control.(listControl); ok {
					id = lc.prefix
				}
				if id != item.ID {
					continue
				}
				if inSheet {
					if _, isLink := row.Control.(linkControl); isLink {
						return true, false
					}
				}
				return true, settingsSectionVisible(st, section) && settingVisible(st, row)
			}
		}
		return false, false
	}
	if item.Sheet != "" {
		if sheet, ok := settingsTreeSheetOf(item.Sheet); ok {
			if found, visible := check(sheet.Sections, true); found {
				return visible
			}
		}
		return true
	}
	if page, ok := settingsTreePageOf(item.Page); ok {
		if found, visible := check(page.Sections, false); found {
			return visible
		}
	}
	return true
}

func matchVisibleSettingsItemsIn(st *SettingsState, query string, items []settingsItem) []settingsItemHit {
	var visible []settingsItem
	for _, item := range items {
		if settingsItemVisible(st, item) {
			visible = append(visible, item)
		}
	}
	return matchSettingsItemsIn(query, visible)
}

func matchSettingsItemsIn(query string, items []settingsItem) []settingsItemHit {
	q := strings.ToLower(strings.TrimSpace(query))
	if utf8.RuneCountInString(q) < 2 {
		return nil
	}

	pageOrder := map[string]int{}
	for i, p := range settingsPages() {
		pageOrder[p.id] = i
	}

	type ranked struct {
		hit   settingsItemHit
		order int
	}
	var rankedHits []ranked
	for i, it := range items {
		rank := -1
		title := strings.ToLower(it.Title)
		switch {
		case strings.HasPrefix(title, q):
			rank = 0
		case strings.Contains(title, q):
			rank = 1
		case settingsItemAliasContains(it, q):
			rank = 2
		}
		if rank < 0 {
			continue
		}
		rankedHits = append(rankedHits, ranked{hit: settingsItemHit{Item: it, rank: rank}, order: i})
	}

	sort.SliceStable(rankedHits, func(a, b int) bool {
		x, y := rankedHits[a], rankedHits[b]
		if x.hit.rank != y.hit.rank {
			return x.hit.rank < y.hit.rank
		}
		px, py := pageOrder[x.hit.Item.Page], pageOrder[y.hit.Item.Page]
		if px != py {
			return px < py
		}
		return x.order < y.order
	})

	hits := make([]settingsItemHit, 0, len(rankedHits))
	for _, r := range rankedHits {
		hits = append(hits, r.hit)
	}
	return hits
}

// settingsItemAliasContains 判断查询是否命中某条目的别名。
func settingsItemAliasContains(it settingsItem, q string) bool {
	for _, a := range it.Aliases {
		if strings.Contains(strings.ToLower(a), q) {
			return true
		}
	}
	return false
}

// settingsSheetPageFallback 补上「用按钮而不是 link 行打开」的面板所属页：这类面板没有
// link 行可查，条目 Page 会留空，命中后切页会落到空页。
func settingsSheetPageFallback(sheet string) string {
	switch sheet {
	case "provider":
		return "models"
	}
	return ""
}

// settingsItemProviderSheet 返回设置项所属供应商面板的 ID。
func settingsItemProviderSheet(id string) string {
	switch {
	case id == "row-agy-browser-login", id == "row-agy-terminal-login", id == "sec-agy-auto-warmup", strings.HasPrefix(id, "sec-agy-routing-"):
		return "agy"
	case id == "row-claude-bin":
		return "claude"
	case id == "row-codex-bin":
		return "codex"
	}
	return ""
}
