package mcodeui

import (
	"strings"
	"sync"
	"time"

	"mcode/internal/agentconf"
)

// 本文件集中放置 MCP 设置页在逐帧渲染路径上用来「算一次、用很多帧」的缓存：
// 预设目录快照、搜索/安装判定计划、授权 ID 与登录状态。设置页是即时模式 UI，
// 每帧都会重建整棵子树，把这些要读写钥匙串、构造大量结构体或做哈希的数据降为读缓存。

// ---------- 预设目录快照 ----------

// mcpCatalogEntryView 是预设条目的渲染视图：key、检索小写字段与示例工具行都在构建时预计算，
// 避免每帧重复 ToLower / Join / 字符串拼接。
type mcpCatalogEntryView struct {
	entry      agentconf.MCPCatalogEntry
	key        string
	haystack   []string
	sampleLine string
}

type mcpCatalogSnapshot struct {
	categories []string
	entries    []mcpCatalogEntryView
}

var (
	mcpCatalogSnapshotOnce sync.Once
	mcpCatalogSnapshotVal  mcpCatalogSnapshot
)

// cachedMCPCatalog 返回缓存后的预设目录。agentconf.MCPCatalog 每次调用都会重新构造全部条目
// （含 map/slice 分配与主目录探测），此前每帧按分类重复调用多次，这里只在首次使用时构建一次。
func cachedMCPCatalog() mcpCatalogSnapshot {
	mcpCatalogSnapshotOnce.Do(func() {
		entries := agentconf.MCPCatalog()
		views := make([]mcpCatalogEntryView, len(entries))
		for i, entry := range entries {
			views[i] = mcpCatalogEntryView{
				entry:      entry,
				key:        "mcp-catalog-" + entry.ID,
				haystack:   mcpCatalogHaystack(entry),
				sampleLine: "示例工具：" + strings.Join(entry.SampleTools, "、"),
			}
		}
		mcpCatalogSnapshotVal = mcpCatalogSnapshot{
			categories: agentconf.MCPCatalogCategories(),
			entries:    views,
		}
	})
	return mcpCatalogSnapshotVal
}

// mcpCatalogHaystack 预先取出可检索字段并转成小写，供搜索时直接用 strings.Contains 比较。
func mcpCatalogHaystack(e agentconf.MCPCatalogEntry) []string {
	fields := make([]string, 0, 5)
	fields = append(fields, e.ID, e.Name, e.Vendor, e.Summary)
	if tools := strings.Join(e.SampleTools, " "); tools != "" {
		fields = append(fields, tools)
	}
	for i, f := range fields {
		fields[i] = strings.ToLower(f)
	}
	return fields
}

// matches 判断该条目是否命中已转小写的搜索词。
func (v mcpCatalogEntryView) matches(lowerQuery string) bool {
	if lowerQuery == "" {
		return true
	}
	for _, field := range v.haystack {
		if strings.Contains(field, lowerQuery) {
			return true
		}
	}
	return false
}

// ---------- 预设目录渲染计划 ----------

// mcpCatalogRow 是一条预设卡片的渲染数据：条目视图 + 已保存的同名服务（用于安装判定与状态显示）。
type mcpCatalogRow struct {
	view      mcpCatalogEntryView
	saved     agentconf.MCPServer
	installed bool
}

type mcpCatalogGroup struct {
	category string
	rows     []mcpCatalogRow
}

// mcpCatalogPlan 是「搜索词 + 已保存服务」组合下的完整渲染计划，只在两者之一变化时重建。
type mcpCatalogPlan struct {
	query      string
	serversKey string
	groups     []mcpCatalogGroup
}

// buildMCPCatalogPlan 过滤并按分类分组预设条目，同时判定每个条目是否已安装。
func buildMCPCatalogPlan(query string, servers []agentconf.MCPServer, serversKey string) mcpCatalogPlan {
	snap := cachedMCPCatalog()

	savedServers := make(map[string]agentconf.MCPServer, len(servers))
	for _, srv := range servers {
		if strings.EqualFold(srv.Agent, "custom") || strings.EqualFold(srv.Agent, "mcode") {
			savedServers[mcpCatalogNormalizeName(srv.Name)] = srv
		}
	}

	lowerQuery := strings.ToLower(strings.TrimSpace(query))
	var groups []mcpCatalogGroup
	for _, category := range snap.categories {
		var rows []mcpCatalogRow
		for _, view := range snap.entries {
			if view.entry.Category != category || !view.matches(lowerQuery) {
				continue
			}
			saved, installed := savedServers[mcpCatalogNormalizeName(view.entry.ID)]
			rows = append(rows, mcpCatalogRow{view: view, saved: saved, installed: installed})
		}
		if len(rows) == 0 {
			continue
		}
		groups = append(groups, mcpCatalogGroup{category: category, rows: rows})
	}

	return mcpCatalogPlan{query: query, serversKey: serversKey, groups: groups}
}

// planLocked 返回当前搜索词与服务列表对应的渲染计划，命中缓存时直接复用；调用方需持有锁。
func (s *mcpCatalogUIState) planLocked(query string, servers []agentconf.MCPServer) mcpCatalogPlan {
	key := mcpServersFingerprint(servers)
	if s.plan != nil && s.plan.query == query && s.plan.serversKey == key {
		return *s.plan
	}
	plan := buildMCPCatalogPlan(query, servers, key)
	s.plan = &plan
	return plan
}

// mcpServersFingerprint 生成服务列表的内容指纹，用于判断渲染计划是否过期。
func mcpServersFingerprint(servers []agentconf.MCPServer) string {
	var b strings.Builder
	b.Grow(len(servers) * 32)
	for _, srv := range servers {
		b.WriteString(srv.Agent)
		b.WriteByte('|')
		b.WriteString(srv.Scope)
		b.WriteByte('|')
		b.WriteString(srv.Name)
		b.WriteByte('|')
		b.WriteString(srv.URL)
		b.WriteByte('\n')
	}
	return b.String()
}

// ---------- 授权 ID 与登录状态缓存 ----------

// mcpSignedInTTL 是登录状态缓存的保鲜期。过期后由后台刷新并触发重绘，渲染路径不直接访问钥匙串；
// 授权/退出动作会立即清除对应缓存，因此用户操作后的状态是即时的。
const mcpSignedInTTL = 5 * time.Second

type mcpSignedInEntry struct {
	signedIn bool
	at       time.Time
}

// mcpCachedAuthID 返回服务授权 ID。AuthID 每次调用都会做一次 sha256 与格式化，
// 逐帧渲染时按「名称 + 地址」缓存结果。
func mcpCachedAuthID(uiSt *mcpPageUIState, srv agentconf.MCPServer) string {
	cacheKey := srv.Name + "\x00" + srv.URL
	uiSt.mu.Lock()
	id, ok := uiSt.authIDs[cacheKey]
	uiSt.mu.Unlock()
	if ok {
		return id
	}

	id = srv.AuthID()
	uiSt.mu.Lock()
	if uiSt.authIDs == nil {
		uiSt.authIDs = make(map[string]string)
	}
	uiSt.authIDs[cacheKey] = id
	uiSt.mu.Unlock()
	return id
}

// mcpServerSignedIn 读取远程服务的登录状态。缓存缺失时同步取一次（每个服务仅首帧付一次钥匙串开销），
// 命中但已过期时改为后台刷新，保证渲染路径不会阻塞在钥匙串上。
func mcpServerSignedIn(st *SettingsState, uiSt *mcpPageUIState, srv agentconf.MCPServer, client Client) bool {
	if client == nil || srv.URL == "" {
		return false
	}
	authID := mcpCachedAuthID(uiSt, srv)

	uiSt.mu.Lock()
	entry, cached := uiSt.signedIn[authID]
	busy := uiSt.signedInBusy[authID]
	uiSt.mu.Unlock()

	if !cached {
		signedIn := client.MCPIsSignedIn(authID)
		uiSt.mu.Lock()
		if uiSt.signedIn == nil {
			uiSt.signedIn = make(map[string]mcpSignedInEntry)
		}
		uiSt.signedIn[authID] = mcpSignedInEntry{signedIn: signedIn, at: time.Now()}
		uiSt.mu.Unlock()
		return signedIn
	}

	if !busy && time.Since(entry.at) >= mcpSignedInTTL {
		refreshMCPSignedInAsync(st, uiSt, authID, client)
	}
	return entry.signedIn
}

// refreshMCPSignedInAsync 在后台重新读取一次登录状态，完成后触发重绘；同一服务同时只允许一个刷新。
func refreshMCPSignedInAsync(st *SettingsState, uiSt *mcpPageUIState, authID string, client Client) {
	uiSt.mu.Lock()
	if uiSt.signedInBusy[authID] {
		uiSt.mu.Unlock()
		return
	}
	if uiSt.signedInBusy == nil {
		uiSt.signedInBusy = make(map[string]bool)
	}
	uiSt.signedInBusy[authID] = true
	uiSt.mu.Unlock()

	go func() {
		signedIn := client.MCPIsSignedIn(authID)

		uiSt.mu.Lock()
		delete(uiSt.signedInBusy, authID)
		if uiSt.signedIn == nil {
			uiSt.signedIn = make(map[string]mcpSignedInEntry)
		}
		uiSt.signedIn[authID] = mcpSignedInEntry{signedIn: signedIn, at: time.Now()}
		uiSt.mu.Unlock()

		if st != nil {
			st.invalidate()
		}
	}()
}

// invalidateMCPSignedIn 清除指定服务的登录状态缓存，下一帧会重新读取（授权前后调用）。
func invalidateMCPSignedIn(uiSt *mcpPageUIState, authID string) {
	if uiSt == nil || authID == "" {
		return
	}
	uiSt.mu.Lock()
	delete(uiSt.signedIn, authID)
	uiSt.mu.Unlock()
}

// mcpPrefetchAuthState 在页面加载入口预热远程服务的授权 ID 与登录状态，
// 让各分区的逐帧渲染只读缓存；缓存缺失时 mcpServerSignedIn 仍会补一次同步读取。
func mcpPrefetchAuthState(st *SettingsState, uiSt *mcpPageUIState, servers []agentconf.MCPServer, client Client) {
	if uiSt == nil || client == nil {
		return
	}
	for _, srv := range servers {
		if srv.URL == "" {
			continue
		}
		mcpServerSignedIn(st, uiSt, srv, client)
	}
}
