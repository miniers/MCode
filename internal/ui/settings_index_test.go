package mcodeui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"mcode/internal/agentconf"
	"mcode/internal/config"
	"mcode/internal/mcp"
)

func TestSettingsSearchAbout(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(&fakeUpdateClient{FakeClient: NewFakeClient()}))
	for _, query := range []string{"版本号", "检查更新"} {
		var found bool
		for _, hit := range matchSettingsItems(query) {
			if hit.Item.Title == query && hit.Item.Page == "about" {
				st.focusSettingsItem(hit)
				found = st.currentPage == "about"
				break
			}
		}
		if !found {
			t.Errorf("搜索 %q 未定位到关于页", query)
		}
	}
}

func TestMCP_LegacySearchTargets(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	server := agentconf.MCPServer{Agent: "custom", Scope: "project", Name: "example"}
	st.mcpServers = []agentconf.MCPServer{server}
	st.agentConfLoaded = true
	key := mcp.EndpointKey(server.Agent, server.Scope, server.Name)

	for _, query := range []string{"Claude", "Codex", "Antigravity", "Pi", "filesystem", "新会话默认"} {
		if hits := matchVisibleSettingsItemsIn(st, query, settingsItems()); len(hits) == 0 {
			t.Errorf("搜索 %q 没有命中", query)
		}
	}
	for _, id := range []string{"mcp.claude", "mcp.codex", "mcp.agy", "mcp.pi", "mcp.newThread", "block-mcp-claude", "block-mcp-codex", "block-mcp-agy", "block-mcp-pi", "block-mcp-newthread", "block-mcp-fold", "row-mcp-newthread-missing-0"} {
		st.focusSettingsItem(settingsItemHit{Item: settingsItem{ID: id, Page: "extensions"}})
		if st.jumpToItemID != "block-mcp-custom" {
			t.Errorf("%s 跳转到 %q", id, st.jumpToItemID)
		}
	}
	st.focusSettingsItem(settingsItemHit{Item: settingsItem{ID: "mcp.fold", Page: "extensions"}})
	if st.jumpToItemID != "row-mcp-fold-tools" {
		t.Errorf("折叠入口跳转到 %q", st.jumpToItemID)
	}
	for _, id := range []string{"mcp.catalog", "block-mcp-catalog"} {
		st.focusSettingsItem(settingsItemHit{Item: settingsItem{ID: id, Page: "extensions"}})
		if sheet, row := settingsTreeSheetState(st); sheet != "mcp-add" || row != "" {
			t.Errorf("%s 跳转到 %q %q", id, sheet, row)
		}
	}
	st.focusSettingsItem(settingsItemHit{Item: settingsItem{ID: "row-mcp-newthread-" + key + "-3", Page: "extensions"}})
	if sheet, row := settingsTreeSheetState(st); sheet != "mcp-detail" || row != "row-mcp-newthread-"+key+"-0" {
		t.Errorf("默认开关跳转到 %q %q", sheet, row)
	}
	if uiSt := getMCPUIState(st); uiSt.selectedServer != key {
		t.Errorf("选中的服务为 %q", uiSt.selectedServer)
	}
}

func TestSettingsIndex_HiddenRows(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	show := false
	page, sheet := settingsTreeFixture()
	page.Sections[0].Rows[0].Visible = func(*SettingsState) bool { return show }
	sheet.Sections[0].Rows[0].Visible = func(*SettingsState) bool { return show }
	oldPages, oldSheets := settingsTreePageDefs, settingsTreeSheetDefs
	settingsTreePageDefs = []settingsPage{page}
	settingsTreeSheetDefs = []settingsSheet{sheet}
	defer func() { settingsTreePageDefs, settingsTreeSheetDefs = oldPages, oldSheets }()
	items := []settingsItem{
		{ID: "row-mode", Page: page.ID, Title: "fixture-page"},
		{ID: "row-global-shortcut", Page: page.ID, Sheet: sheet.ID, Title: "fixture-sheet"},
	}
	if hits := matchVisibleSettingsItemsIn(st, "fixture", items); len(hits) != 0 {
		t.Fatalf("隐藏行不应命中: %#v", hits)
	}
	st.focusSettingsItem(settingsItemHit{Item: items[1]})
	if id, _ := settingsTreeSheetState(st); id != "" {
		t.Fatal("隐藏的面板行不应跳转")
	}
	show = true
	if hits := matchVisibleSettingsItemsIn(st, "fixture", items); len(hits) != 2 {
		t.Fatalf("显示后应命中两行: %#v", hits)
	}
	st.focusSettingsItem(settingsItemHit{Item: items[1]})
	if id, row := settingsTreeSheetState(st); id != sheet.ID || row != items[1].ID {
		t.Fatalf("面板跳转错误: %q %q", id, row)
	}
}

// settingsRowCall 是源码里一次行渲染调用的静态信息。
// 行渲染原语（renderSettingRow / renderStackedSettingRow）前三个参数都是 c、key、title，扫描时按位置取用。
type settingsRowCall struct {
	file  string
	line  int
	key   string
	title string
	// keyConcat / titleConcat 表示对应参数是拼接表达式，此时 key 只记最左侧的字符串字面量前缀。
	keyConcat   bool
	titleConcat bool
	// keyLiteral / titleLiteral 表示对应参数本身就是一个字符串字面量。
	keyLiteral   bool
	titleLiteral bool
	// composite 表示这条来自 setting{ID: ..., Title: ...} 声明而不是行渲染调用。
	// 声明里的 ID 可能只是内部控件 key（如 provider-add-type），所以只参与覆盖比对。
	composite bool
}

// settingsRowRendererName 报告函数名是否是行渲染原语。
func settingsRowRendererName(name string) bool {
	switch name {
	case "renderSettingRow", "renderStackedSettingRow":
		return true
	}
	return false
}

// collectSettingsRowCalls 用 go/parser 扫描 internal/ui 下的非测试文件，收集行渲染调用的 key 与标题。
// key 由变量拼出（如 s.ID）时静态比不了，跳过：这类行由页面树与各页自己的测试覆盖。
func collectSettingsRowCalls(t *testing.T) []settingsRowCall {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读取 internal/ui 目录失败: %v", err)
	}

	fset := token.NewFileSet()
	var calls []settingsRowCall
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if name == "settings_tree.go" || name == "settings_controls.go" {
			// 通用渲染器：行 key 由页面树提供，由页面树测试覆盖。
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if lit, ok := n.(*ast.CompositeLit); ok {
				for _, declLit := range settingsSettingLiterals(lit) {
					decl, ok := settingsSettingLiteralCall(declLit)
					if !ok {
						continue
					}
					decl.file = name
					decl.line = fset.Position(declLit.Pos()).Line
					decl.composite = true
					calls = append(calls, decl)
				}
				return true
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fn, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			if !settingsRowRendererName(fn.Name) {
				return true
			}
			if len(call.Args) < 3 {
				return true
			}
			key, keyLiteral, keyConcat := settingsStringPrefix(call.Args[1])
			if key == "" {
				return true
			}
			title, titleLiteral, titleConcat := settingsStringPrefix(call.Args[2])
			calls = append(calls, settingsRowCall{
				file:         name,
				line:         fset.Position(call.Pos()).Line,
				key:          key,
				title:        title,
				keyLiteral:   keyLiteral,
				keyConcat:    keyConcat,
				titleLiteral: titleLiteral,
				titleConcat:  titleConcat,
			})
			return true
		})
	}
	return calls
}

// settingsSettingLiterals 找出一个复合字面量里声明的 setting：裸的 setting{...},
// 以及 []setting{{...}} 里省略了类型名的行声明。
func settingsSettingLiterals(lit *ast.CompositeLit) []*ast.CompositeLit {
	switch typ := lit.Type.(type) {
	case *ast.Ident:
		if typ.Name == "setting" {
			return []*ast.CompositeLit{lit}
		}
	case *ast.ArrayType:
		elt, ok := typ.Elt.(*ast.Ident)
		if !ok || elt.Name != "setting" {
			return nil
		}
		var out []*ast.CompositeLit
		for _, e := range lit.Elts {
			inner, ok := e.(*ast.CompositeLit)
			if !ok || inner.Type != nil {
				continue
			}
			out = append(out, inner)
		}
		return out
	}
	return nil
}

// settingsSettingLiteralCall 解析 setting{ID: ..., Title: ...} 声明：ID 取字面量或拼接前缀，
// Title 同样按字符串前缀取；ID 静态不可解析（如循环变量）时跳过。
// 入参已经由 settingsSettingLiterals 筛成 setting 字面量，其中 []setting{{...}} 的内层
// 字面量 Type 为 nil，所以这里不再校验类型名。
func settingsSettingLiteralCall(lit *ast.CompositeLit) (settingsRowCall, bool) {
	var call settingsRowCall
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		field, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch field.Name {
		case "ID":
			call.key, call.keyLiteral, call.keyConcat = settingsStringPrefix(kv.Value)
		case "Title":
			call.title, call.titleLiteral, call.titleConcat = settingsStringPrefix(kv.Value)
		}
	}
	return call, call.key != ""
}

// settingsStringPrefix 取表达式的字符串前缀：字面量取整体（literal=true），拼接表达式取最左侧的
// 字符串字面量（literal=false, concat=true），其他表达式返回空且两者都为 false。
func settingsStringPrefix(expr ast.Expr) (prefix string, literal bool, concat bool) {
	switch v := expr.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false, false
		}
		s, err := strconv.Unquote(v.Value)
		if err != nil {
			return "", false, false
		}
		return s, true, false
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", false, false
		}
		for cur := ast.Expr(v); ; {
			bin, ok := cur.(*ast.BinaryExpr)
			if !ok || bin.Op != token.ADD {
				lit, isLit, _ := settingsStringPrefix(cur)
				return lit, false, isLit
			}
			cur = bin.X
		}
	}
	return "", false, false
}

// settingsTreeIDSet 收集页面树里已有的条目 ID。
func settingsTreeIDSet() map[string]bool {
	ids := map[string]bool{}
	for _, item := range settingsTreeItems() {
		ids[item.ID] = true
	}
	return ids
}

// TestSettingsIndex_CoversEveryRow 校验索引与源码里的行 key 双向一致：源码里出现的 key 必须登记在
// 索引里，索引里的 key 也必须在源码里能找到。拼接表达式（如按供应商循环的模板行）用前缀比对。
// 已迁入页面树的条目不在源码扫描范围内，改为报重复登记。
func TestSettingsIndex_CoversEveryRow(t *testing.T) {
	calls := collectSettingsRowCalls(t)
	if len(calls) == 0 {
		t.Fatalf("没有收集到任何行渲染调用，解析逻辑可能失效")
	}

	byID := map[string]settingsItem{}
	for _, it := range settingsItems() {
		if _, dup := byID[it.ID]; dup {
			t.Fatalf("索引里出现重复 ID: %q", it.ID)
		}
		byID[it.ID] = it
	}

	// 1. 源码 → 索引：字面量 key 必须精确命中，拼接 key 必须有条目以该前缀开头。
	// setting 声明里的内部控件 key 不在索引范围内，只看覆盖方向。
	for _, call := range calls {
		if call.composite {
			continue
		}
		// 旧收藏模型渲染函数不再接入页面树，空状态不应作为搜索入口。
		if call.key == "row-models-none" {
			continue
		}
		if !call.keyConcat {
			if _, ok := byID[call.key]; !ok {
				t.Errorf("%s:%d 的行 key %q 未登记在 settingsItems()", call.file, call.line, call.key)
			}
			continue
		}
		found := false
		for id := range byID {
			if strings.HasPrefix(id, call.key) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s:%d 的模板行前缀 %q 没有任何索引条目", call.file, call.line, call.key)
		}
	}

	// 2. 索引 → 源码：每个非区块条目都要有对应的行渲染调用；页面树里的条目除外。
	treeIDs := settingsTreeIDSet()
	literalKeys := map[string]bool{}
	var concatPrefixes []string
	for _, call := range calls {
		if call.keyConcat {
			concatPrefixes = append(concatPrefixes, call.key)
			continue
		}
		literalKeys[call.key] = true
	}
	var missing []string
	for _, it := range settingsItems() {
		if it.Block || treeIDs[it.ID] {
			continue
		}
		if literalKeys[it.ID] {
			continue
		}
		covered := false
		for _, prefix := range concatPrefixes {
			if strings.HasPrefix(it.ID, prefix) {
				covered = true
				break
			}
		}
		if !covered {
			missing = append(missing, it.ID)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("索引里有 %d 条条目在源码中找不到行渲染: %v", len(missing), missing)
	}

	// 3. 已迁入页面树的条目必须从手写表删除，否则会留下两份定义。
	for _, it := range settingsLegacyItems() {
		if treeIDs[it.ID] {
			t.Errorf("%s 已迁入页面树，请从 settingsLegacyItems 删除", it.ID)
		}
	}
}

// TestSettingsIndex_TitlesMatchSource 校验手写表里未迁移条目的标题与源码里写死的标题一致。
func TestSettingsIndex_TitlesMatchSource(t *testing.T) {
	treeIDs := settingsTreeIDSet()
	byID := map[string]settingsItem{}
	for _, it := range settingsLegacyItems() {
		if treeIDs[it.ID] {
			continue
		}
		byID[it.ID] = it
	}

	checked := 0
	for _, call := range collectSettingsRowCalls(t) {
		if !call.keyLiteral || !call.titleLiteral || call.title == "" {
			continue
		}
		it, ok := byID[call.key]
		if !ok {
			continue
		}
		if it.Dynamic {
			continue
		}
		if it.Title != call.title {
			t.Errorf("%s:%d 的标题是 %q，索引里写的是 %q", call.file, call.line, call.title, it.Title)
		}
		checked++
	}
	if checked == 0 {
		t.Fatalf("没有比对到任何标题，解析逻辑可能失效")
	}
}

// TestSettingsIndex_Match 校验逐项匹配的命中、排序与最短查询长度。
func TestSettingsIndex_Match(t *testing.T) {
	hits := matchSettingsItems("折叠")
	found := false
	for _, h := range hits {
		if h.Item.ID == "row-auto-collapse-reasoning" && h.Item.Page == "conversation" {
			found = true
		}
	}
	if !found {
		t.Fatalf("搜「折叠」未命中会话页的自动折叠思考: %+v", hits)
	}

	found = false
	for _, h := range matchSettingsItems("排队") {
		if h.Item.ID == "row-queue-while-running" {
			found = true
		}
	}
	if !found {
		t.Fatalf("搜「排队」未命中 row-queue-while-running")
	}

	found = false
	for _, h := range matchSettingsItems("工作副本") {
		if h.Item.ID == "row-swarm-review-check-command" {
			found = true
		}
	}
	if !found {
		t.Fatalf("搜「工作副本」未命中 row-swarm-review-check-command：这个词原来只写在说明里，现已并入别名")
	}

	if hits := matchSettingsItems("排"); len(hits) != 0 {
		t.Fatalf("单个字符不应返回命中，实际 %d 条", len(hits))
	}
	if hits := matchSettingsItems(""); len(hits) != 0 {
		t.Fatalf("空查询不应返回命中，实际 %d 条", len(hits))
	}
}

// TestSettingsIndex_ItemPages 校验手写表里未迁移条目的页面都在 settingsPages() 的页面集合里。
func TestSettingsIndex_ItemPages(t *testing.T) {
	pages := map[string]bool{}
	for _, p := range settingsPages() {
		pages[p.id] = true
	}
	treeIDs := settingsTreeIDSet()
	var items []settingsItem
	for _, it := range settingsLegacyItems() {
		if !treeIDs[it.ID] {
			items = append(items, it)
		}
	}
	if len(items) == 0 {
		t.Fatalf("索引为空")
	}
	for _, it := range items {
		if it.Page == "" {
			t.Errorf("条目 %q 缺少页面", it.ID)
			continue
		}
		if !pages[it.Page] {
			t.Errorf("条目 %q 的页面 %q 不在 settingsPages() 里", it.ID, it.Page)
		}
	}
}

// TestSettingsItems_EmptyTreeKeepsLegacy 校验页面树为空时索引与手写表逐条一致。
func TestSettingsItems_EmptyTreeKeepsLegacy(t *testing.T) {
	prevPages, prevSheets := settingsTreePageDefs, settingsTreeSheetDefs
	settingsTreePageDefs, settingsTreeSheetDefs = nil, nil
	t.Cleanup(func() {
		settingsTreePageDefs, settingsTreeSheetDefs = prevPages, prevSheets
	})

	items := settingsItems()
	legacy := settingsLegacyItems()
	if len(items) != len(legacy) {
		t.Fatalf("页面树为空时索引 %d 条，手写表 %d 条", len(items), len(legacy))
	}
	for i := range legacy {
		if items[i].ID != legacy[i].ID {
			t.Fatalf("第 %d 条 = %q，手写表是 %q", i, items[i].ID, legacy[i].ID)
		}
	}
}

// TestSettingsItems_IncludesTree 校验页面树派生的条目并入索引：与手写表重复的 ID 只保留一条、
// Section 用 section 标题、Desc 为空，并且能按别名搜索到；list 前缀条目标为动态。
func TestSettingsItems_IncludesTree(t *testing.T) {
	prevPages := settingsTreePageDefs
	settingsTreePageDefs = []settingsPage{{
		ID:    "general",
		Title: "通用",
		Sections: []settingsSection{{
			ID:    "general.fixture",
			Title: "测试区",
			Rows: []setting{
				{ID: "row-notify", Title: "完成后通知", Aliases: []string{"fixture-notify"},
					Control: toggle(val(func(c *config.Config) *bool { return &c.Notifications.NotifyWhenFinished }, true))},
				{ID: "row-fixture-", Title: "模板行",
					Control: list("row-fixture-", func(st *SettingsState) []settingListRow { return nil })},
			},
		}},
	}}
	t.Cleanup(func() { settingsTreePageDefs = prevPages })

	var matches []settingsItem
	dynamic := false
	for _, item := range settingsItems() {
		if item.ID == "row-notify" {
			matches = append(matches, item)
		}
		if item.ID == "row-fixture-" && item.Dynamic {
			dynamic = true
		}
	}
	if len(matches) != 1 {
		t.Fatalf("row-notify 应只出现一次，实际 %d 次", len(matches))
	}
	got := matches[0]
	if got.Section != "测试区" {
		t.Errorf("row-notify 的 Section = %q，想要 fixture 的 section 标题「测试区」", got.Section)
	}
	if got.Page != "general" {
		t.Errorf("row-notify 的 Page = %q，想要 general", got.Page)
	}
	if !dynamic {
		t.Errorf("list 前缀条目 row-fixture- 应标为动态")
	}

	hit := false
	for _, h := range matchSettingsItems("fixture-notify") {
		if h.Item.ID == "row-notify" {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("按别名 fixture-notify 搜索应命中 row-notify")
	}
}

// TestSettingsNav_ItemJumpAndHighlight 校验导航里点开命中项会切页并高亮，点页名会清掉跳转状态。
func TestSettingsNav_ItemJumpAndHighlight(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.open = true
	st.currentPage = "conversation"

	tt := ui.NewTester(func(c *ui.Context) { renderSettingsNav(c, st) }, 900, 1400)
	tt.Frame()

	st.navSearch = "排队"
	tt.Frame()
	if !tt.HasText("settings-nav-hit-row-queue-while-running") {
		t.Fatalf("导航里没有列出命中项「运行中排队消息」: %v", tt.Texts())
	}

	if err := tt.Click("settings-nav-hit-row-queue-while-running"); err != nil {
		t.Fatalf("点击命中项失败: %v", err)
	}
	tt.Frame()

	st.mu.Lock()
	page, jump, highlight := st.currentPage, st.jumpToItemID, st.highlightItemID
	st.mu.Unlock()
	if page != "conversation" {
		t.Fatalf("点命中项后应切到会话页，实际 %q", page)
	}
	if highlight != "row-queue-while-running" {
		t.Fatalf("点命中项后应高亮 row-queue-while-running，实际 %q", highlight)
	}
	if jump != "row-queue-while-running" {
		t.Fatalf("点命中项后应定位到 row-queue-while-running，实际 %q", jump)
	}

	// 清空搜索后点页名「通用」，跳转与高亮都要被清掉。
	st.navSearch = ""
	tt.Frame()
	if err := tt.Click("通用"); err != nil {
		t.Fatalf("点击页名「通用」失败: %v", err)
	}
	tt.Frame()

	st.mu.Lock()
	page, jump, highlight = st.currentPage, st.jumpToItemID, st.highlightItemID
	st.mu.Unlock()
	if page != "general" {
		t.Fatalf("点页名后应切到通用页，实际 %q", page)
	}
	if jump != "" || highlight != "" {
		t.Fatalf("点页名后应清空跳转与高亮，实际 jump=%q highlight=%q", jump, highlight)
	}
}

// TestSettingsIndex_QueryMinRunes 校验最短查询长度按 rune 计算。
func TestSettingsIndex_QueryMinRunes(t *testing.T) {
	if utf8.RuneCountInString("排队") != 2 {
		t.Fatalf("测试前提不成立")
	}
	if hits := matchSettingsItems(" 排 "); len(hits) != 0 {
		t.Fatalf("去空白后只剩一个字符，不应命中，实际 %d 条", len(hits))
	}
}

// TestSettingsNav_SearchSwitchUsesLazyLoad 校验导航搜索把当前页过滤掉后，切到第一个可见页
// 走的是统一切页入口：目标页的懒加载状态被重置并重新拉取（旧实现渲染期直写 currentPage，
// 目标页的懒加载钩子不会被触发）。
func TestSettingsNav_SearchSwitchUsesLazyLoad(t *testing.T) {
	// 不传 chat：各页的加载函数都因 client 为 nil 早退，切页重置的懒加载标志不会被后台
	// 拉取改回去，断言因此是确定的。
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.open = true
	st.currentPage = "general"
	// 预置模型页已加载：走切页入口时它必须被重置为待加载。
	st.mu.Lock()
	st.endpoints.loaded = true
	st.mu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) {
		// 与设置面板真实布局一致：左导航、右详情，导航的切页请求下一帧由详情区执行。
		ui.Row(c.Key("test-settings-split")).Fill().Children(func() {
			ui.Column(c.Key("test-settings-nav")).Width(300).FillHeight().Children(func() {
				renderSettingsNav(c, st)
			})
			ui.Column(c.Key("test-settings-main")).Grow(1).Children(func() {
				renderSettingsDetail(c, st)
			})
		})
	}, 1200, 1600)
	tt.Frame()

	st.navSearch = "模型"
	tt.Frame() // 导航把当前页（关于）过滤掉，请求切到第一个可见页
	tt.Frame() // 详情区渲染前执行切页
	st.mu.Lock()
	page, loaded := st.currentPage, st.endpoints.loaded
	st.mu.Unlock()
	if page != "models" {
		t.Fatalf("搜索应切到第一个可见页 models，实际 %q", page)
	}
	if loaded {
		t.Fatal("切到模型页时应重置懒加载状态（走 selectSettingsPage），实际仍是已加载")
	}
	if st.navSearch != "模型" {
		t.Fatalf("切页不应清掉搜索词，实际 %q", st.navSearch)
	}
}
