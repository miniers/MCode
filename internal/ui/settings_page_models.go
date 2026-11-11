package mcodeui

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// modelsPageLoadTimeout 是模型页单次加载的上限：超时后放弃等待并允许重试，
// 避免某个调用挂住时页面永远停在加载中，也不让「重新加载」无从出现。
const modelsPageLoadTimeout = 15 * time.Second
const modelsRefreshTimeout = 60 * time.Second

var (
	iconGrip        = parseLucideSVG(`<line x1="4" x2="20" y1="6" y2="6"/><line x1="4" x2="20" y1="12" y2="12"/><line x1="4" x2="20" y1="18" y2="18"/>`)
	iconMinusCircle = parseLucideSVG(`<circle cx="12" cy="12" r="10"/><path d="M8 12h8"/>`)
)

type modelsPageLoadResult struct {
	pins             []ModelPin
	limit            int
	preferences      map[string]ModelPreference
	providers        []ProviderInfo
	providerSettings []ProviderSetting
	err              error
	// catalogErr 是供应商目录（Providers）读取失败：它只影响模型库，不影响「你的模型」。
	catalogErr error
}

type modelsPageState struct {
	pins      []ModelPin
	limit     int
	loaded    bool
	load      chan modelsPageLoadResult
	loadSince time.Time // 本次加载的发起时间，用于超时放弃
	// loadErr 是加载失败（固定模型列表/偏好读取不出来），会隐藏「你的模型」；
	// catalogErr 是供应商目录读取失败，只提示不隐藏列表；opErr 是添加/排序/移除失败。
	loadErr     string
	catalogErr  string
	opErr       string
	searchQuery string
	// providerSearch 按供应商 ID 保存局部搜索词；用指针是因为 ui.TextInput 需要可寻址的值。
	providerSearch map[string]*string
	// magpieListScroll 按滚动容器的 key 保存 Magpie 端点模型列表的滚动位置（懒初始化），
	// 让 containScroll 知道列表是否已经滚到顶或底。
	magpieListScroll   map[string]*ui.ScrollState
	providers          []ProviderInfo
	catalogVersion     uint64
	loadCatalogVersion uint64
	providerSettings   []ProviderSetting
	dragFrom           int
	dragDy             float32
	openPopoverKey     string
	preferences        map[string]ModelPreference
	// seeded 表示首帧已用配置里的固定模型填过「你的模型」，避免加载期间区块空白。
	seeded bool
	// loadCatalogOnly 表示本次加载只刷新供应商目录：固定模型、偏好与上限保持原样。
	loadCatalogOnly bool
}

// modelsPageStateHook 在每帧渲染时把页面状态交给测试观察，正式运行始终为 nil。
var modelsPageStateHook func(*modelsPageState)

// canEdit 判断页面当前能否编辑：完整加载结束后允许；只刷新供应商目录的重载期间
// ps.loaded 会是 false，此时同样允许，避免模型库的添加按钮短暂变灰。
func (ps *modelsPageState) canEdit() bool {
	return (ps.loaded || ps.loadCatalogOnly) && (ps.load == nil || ps.loadCatalogOnly)
}

// refreshModels 在后台更新供应商目录，保留固定模型与偏好。
func (st *SettingsState) refreshModels() {
	st.mu.Lock()
	if st.modelsRefreshing || st.chat == nil || st.chat.client == nil {
		st.mu.Unlock()
		return
	}
	chat := st.chat
	st.modelsRefreshing = true
	st.modelsRefreshDone = false
	st.modelsRefreshErr = ""
	st.mu.Unlock()
	st.invalidate()
	seq := chat.nextProviderCatalogRequest()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), modelsRefreshTimeout)
		defer cancel()
		providers, err := chat.client.RefreshProviders(ctx)
		if providers != nil {
			chat.applyProviderCatalog(seq, providers, st)
		}
		st.mu.Lock()
		st.modelsRefreshing = false
		st.modelsRefreshDone = true
		if err != nil {
			st.modelsRefreshErr = err.Error()
		}
		st.mu.Unlock()
		st.invalidate()
	}()
}

func newModelsPageState() modelsPageState {
	return modelsPageState{
		limit:          config.MaxModelListLimit,
		dragFrom:       -1,
		providerSearch: make(map[string]*string),
		preferences:    make(map[string]ModelPreference),
	}
}

// providerSearchInput 返回指定供应商的局部搜索词指针，不存在时先创建。
func (ps *modelsPageState) providerSearchInput(providerID string) *string {
	if ps.providerSearch == nil {
		ps.providerSearch = make(map[string]*string)
	}
	if q, ok := ps.providerSearch[providerID]; ok {
		return q
	}
	q := new(string)
	ps.providerSearch[providerID] = q
	return q
}

// magpieListScrollState 返回 Magpie 端点模型列表的滚动状态，首次使用时创建。
func (ps *modelsPageState) magpieListScrollState(key string) *ui.ScrollState {
	if ps.magpieListScroll == nil {
		ps.magpieListScroll = make(map[string]*ui.ScrollState)
	}
	if s, ok := ps.magpieListScroll[key]; ok {
		return s
	}
	s := &ui.ScrollState{}
	ps.magpieListScroll[key] = s
	return s
}

// Client 独立写配置，设置页的预算等控件仍通过 st.save 写入，必须同步快照。
// 写 cfg 要同时持有 mu 与 cfgMu（顺序 mu → cfgMu），渲染路径在 mu 下读 cfg；
// 本函数不调 save/invalidate，避免在持锁时重入 st.mu。
func (ps *modelsPageState) syncSettings(st *SettingsState) {
	st.mu.Lock()
	st.cfgMu.Lock()
	st.cfg.Models.Pins = append([]ModelPin(nil), ps.pins...)
	for key, pref := range ps.preferences {
		provider, model, ok := strings.Cut(key, "/")
		if ok {
			st.cfg.Models.SetModelPreference(ModelPin{Provider: provider, ModelID: model}, pref)
		}
	}
	st.cfgMu.Unlock()
	st.mu.Unlock()
}

func (ps *modelsPageState) refreshPins(st *SettingsState, client Client) {
	pins, limit, err := client.ModelList(context.Background())
	if err != nil {
		ps.loadErr = "加载模型失败：" + err.Error()
		return
	}
	ps.pins = pins
	ps.limit = config.MaxModelListLimit
	if limit > 0 && limit < ps.limit {
		ps.limit = limit
	}
	ps.loadErr = ""
	ps.syncSettings(st)
}

// removePin 从「你的模型」移除一个模型，成功后同步配置与列表，失败时提示。
func (ps *modelsPageState) removePin(st *SettingsState, client Client, pin ModelPin) {
	if client == nil {
		return
	}
	removed, err := client.RemoveModelFromList(context.Background(), pin)
	if err != nil {
		ps.opErr = "移除模型失败：" + err.Error()
		st.invalidate()
		return
	}
	if removed {
		var kept []ModelPin
		for _, p := range ps.pins {
			if p != pin {
				kept = append(kept, p)
			}
		}
		ps.pins = kept
		ps.opErr = ""
		ps.syncSettings(st)
	}
	ps.refreshPins(st, client)
	ps.openPopoverKey = ""
	st.invalidate()
}

func (ps *modelsPageState) savePreference(st *SettingsState, client Client, pin ModelPin, pref ModelPreference) {
	if client == nil {
		return
	}
	if err := client.UpdateModelPreference(context.Background(), pin, pref); err != nil {
		ps.opErr = "保存模型偏好失败：" + err.Error()
	} else {
		ps.preferences[pin.Key()] = pref
		ps.opErr = ""
		ps.syncSettings(st)
	}
	st.invalidate()
}

// reorderPins 纯函数：将 pins 中的元素从 from 移动至 to 位置，返回新切片。越界时原样返回。
func reorderPins(pins []ModelPin, from, to int) []ModelPin {
	n := len(pins)
	if from < 0 || from >= n || to < 0 || to >= n || from == to {
		res := make([]ModelPin, len(pins))
		copy(res, pins)
		return res
	}
	item := pins[from]
	remaining := make([]ModelPin, 0, n-1)
	for i, p := range pins {
		if i != from {
			remaining = append(remaining, p)
		}
	}
	res := make([]ModelPin, 0, n)
	for i := 0; i <= len(remaining); i++ {
		if i == to {
			res = append(res, item)
		}
		if i < len(remaining) {
			res = append(res, remaining[i])
		}
	}
	return res
}

// modelDragTargetIndex 纯函数：按起始下标、垂直偏移和行高换算出目标下标。
func modelDragTargetIndex(from int, dy, rowHeight float32, n int) int {
	if n <= 0 {
		return 0
	}
	if rowHeight <= 0 {
		rowHeight = 50
	}
	steps := int(math.Round(float64(dy / rowHeight)))
	target := from + steps
	if target < 0 {
		return 0
	}
	if target >= n {
		return n - 1
	}
	return target
}

// commitModelReorder 将 pins 列表重排并在 client 非空时持久化保存新顺序。
func commitModelReorder(client Client, pins []ModelPin, from, to int) ([]ModelPin, error) {
	newPins := reorderPins(pins, from, to)
	if client != nil {
		if err := client.ReorderModelList(context.Background(), newPins); err != nil {
			return pins, err
		}
	}
	return newPins, nil
}

// canAddModel 纯函数：判断指定 pin 是否允许添加到列表（未达上限且不重复）。
func canAddModel(pins []ModelPin, limit int, pin ModelPin) bool {
	if limit <= 0 || limit > config.MaxModelListLimit {
		limit = config.MaxModelListLimit
	}
	if pin.Provider == "" || pin.ModelID == "" || len(pins) >= limit {
		return false
	}
	for _, p := range pins {
		if p.Key() == pin.Key() {
			return false
		}
	}
	return true
}

// modelMatchesQuery 纯函数：判断模型是否命中搜索词。搜索词按空白切成多个词，
// 大小写不敏感，每个词都必须出现在名称、ID 或说明之一；搜索词为空时全部命中。
func modelMatchesQuery(name, id, detail, query string) bool {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return true
	}
	name, id, detail = strings.ToLower(name), strings.ToLower(id), strings.ToLower(detail)
	for _, word := range strings.Fields(strings.ToLower(trimmed)) {
		if !strings.Contains(name, word) && !strings.Contains(id, word) && !strings.Contains(detail, word) {
			return false
		}
	}
	return true
}

// modelPinPresent 判断固定模型列表里是否已经有该 pin。
func modelPinPresent(pins []ModelPin, pin ModelPin) bool {
	for _, p := range pins {
		if p.Key() == pin.Key() {
			return true
		}
	}
	return false
}

// filterProviderModels 纯函数：按 query 实时过滤供应商及其模型列表。
// 没有模型的供应商（未启用、未登录或没配 API Key）默认不进入模型库；
// keepEmpty 为真时，空搜索下保留 Available 为真的空目录供应商。
func filterProviderModels(providers []ProviderInfo, query string, keepEmpty ...bool) []ProviderInfo {
	keep := len(keepEmpty) > 0 && keepEmpty[0]
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		var res []ProviderInfo
		for _, p := range providers {
			if len(p.Models) > 0 || (keep && p.Available) {
				res = append(res, p)
			}
		}
		return res
	}
	q := strings.ToLower(trimmed)
	var res []ProviderInfo
	for _, p := range providers {
		var matched []ModelInfo
		for _, m := range p.Models {
			fields := []string{m.Name, m.ID, m.Detail, p.Name, p.ID}
			matchedModel := false
			for _, f := range fields {
				if f != "" && strings.Contains(strings.ToLower(f), q) {
					matchedModel = true
					break
				}
			}
			if matchedModel {
				matched = append(matched, m)
			}
		}
		if len(matched) > 0 {
			pCopy := p
			pCopy.Models = matched
			res = append(res, pCopy)
		}
	}
	return res
}

// providerEnabledMap 汇总各供应商当前的启用状态，供渲染时跳过已关闭供应商的模型。
// 取值优先级（后者覆盖前者）：页面加载时读到的 ProviderSettings、供应商页已加载的行设置、
// 配置里直接控制的启用开关（claude/codex/agy 与自定义端点）。只读取，不写回任何数据。
func providerEnabledMap(cfg config.Config, rows map[string]ProviderSetting, settings []ProviderSetting) map[string]bool {
	enabled := make(map[string]bool, len(settings)+len(rows)+3)
	for _, s := range settings {
		enabled[s.ID] = s.Enabled
	}
	for id, row := range rows {
		enabled[id] = row.Enabled
	}
	enabled[config.ProviderClaude] = cfg.Providers.Claude.Enabled
	enabled[config.ProviderCodex] = cfg.Providers.Codex.Enabled
	enabled[config.ProviderAgy] = cfg.Providers.Agy.Enabled
	for _, ep := range cfg.Providers.Custom {
		enabled[config.CustomEndpointProviderKind(ep)] = ep.Enabled
	}
	return enabled
}

// providerDisabled 判断供应商是否已关闭；没有启用状态信息的供应商不视为关闭。
func providerDisabled(enabled map[string]bool, provider string) bool {
	val, known := enabled[provider]
	return known && !val
}

// filterPinsByEnabledProviders 纯函数：跳过所属供应商已关闭的固定模型。
// 「我的模型」的持久化数据保持不变，供应商重新启用后条目会自动重新出现。
func filterPinsByEnabledProviders(pins []ModelPin, enabled map[string]bool) []ModelPin {
	visible := make([]ModelPin, 0, len(pins))
	for _, pin := range pins {
		if providerDisabled(enabled, pin.Provider) {
			continue
		}
		visible = append(visible, pin)
	}
	return visible
}

// filterProvidersByEnabled 纯函数：跳过已关闭的供应商，模型库不再列出它的模型。
func filterProvidersByEnabled(providers []ProviderInfo, enabled map[string]bool) []ProviderInfo {
	res := make([]ProviderInfo, 0, len(providers))
	for _, p := range providers {
		if providerDisabled(enabled, p.ID) {
			continue
		}
		res = append(res, p)
	}
	return res
}

func resolveModelName(providers []ProviderInfo, pin ModelPin) string {
	for _, p := range providers {
		if p.ID == pin.Provider {
			for _, m := range p.Models {
				if m.ID == pin.ModelID {
					if m.Name != "" {
						return m.Name
					}
					return m.ID
				}
			}
		}
	}
	return pin.ModelID
}

// pinPickerProviders 返回只包含该固定模型所属 provider 的目录（推理强度卡片只针对该模型）。
func pinPickerProviders(providers []ProviderInfo, pin ModelPin) []ProviderInfo {
	if prov, ok := findProviderInfo(providers, pin.Provider); ok {
		return []ProviderInfo{prov}
	}
	return nil
}

func modelPinSummary(providers []ProviderInfo, pin ModelPin, pref ModelPreference) string {
	parts := []string{modelPinAgentName(providers, pin)}
	if pref.Effort != nil && *pref.Effort != "" {
		parts = append(parts, "推理强度 "+effortLabel(*pref.Effort))
	}
	if pref.FastMode {
		parts = append(parts, "快速模式")
	}
	return strings.Join(parts, " · ")
}

// modelPinAgentName 返回固定模型所属 agent 的展示名：目录里的名称优先，
// 缺失或与 provider ID 相同时用内置显示名，仍为空才退回 provider ID。
func modelPinAgentName(providers []ProviderInfo, pin ModelPin) string {
	name := ""
	for _, p := range providers {
		if p.ID != pin.Provider {
			continue
		}
		name = strings.TrimSpace(p.Name)
		break
	}
	if name == "" || strings.EqualFold(name, pin.Provider) {
		name = strings.TrimSpace(providerDisplayName(pin.Provider))
	}
	if name == "" {
		name = pin.Provider
	}
	return name
}

// modelIDAbbreviations 是模型 ID 里保持整词大写的缩写。
var modelIDAbbreviations = map[string]bool{"gpt": true, "glm": true}

// prettyModelID 把模型 ID 整理成可读名称：去掉 provider 前缀，连字符换成空格，
// 连续的纯数字用「.」连接（5-5 → 5.5），已知缩写整词大写。
func prettyModelID(id string) string {
	if slash := strings.LastIndex(id, "/"); slash >= 0 {
		id = id[slash+1:]
	}
	if id == "" {
		return ""
	}
	tokens := make([]string, 0, 4)
	var numbers []string
	flushNumbers := func() {
		if len(numbers) == 0 {
			return
		}
		tokens = append(tokens, strings.Join(numbers, "."))
		numbers = numbers[:0]
	}
	for _, token := range strings.Split(id, "-") {
		if token == "" {
			continue
		}
		if allDigits(token) {
			numbers = append(numbers, token)
			continue
		}
		flushNumbers()
		if modelIDAbbreviations[strings.ToLower(token)] {
			tokens = append(tokens, strings.ToUpper(token))
			continue
		}
		tokens = append(tokens, capitalizeFirstRune(token))
	}
	flushNumbers()
	return strings.Join(tokens, " ")
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// modelsLibraryView 是模型库的渲染结果：供应商行的展开区需要它来画自家模型列表。
type modelsLibraryView struct {
	ps        *modelsPageState
	client    Client
	providers []ProviderInfo // 已按启用状态与搜索词过滤后的供应商目录
	search    string         // 去掉首尾空格的搜索词
}

var pageModels = settingsPage{
	ID: "models", Title: "模型与供应商", Icon: IconSlidersHorizontal(),
	Sections: []settingsSection{
		{ID: "sec-models-pinned", Title: "收藏模型", Rows: []setting{
			{ID: "row-models-pinned", Aliases: []string{"收藏", "模型", "model", "pinned"}, Control: customFullWidth(renderPinnedModels)},
			{ID: "row-models-add", Aliases: []string{"添加收藏", "添加模型", "add model"}, Control: custom(renderAddModelPin)},
		}},
		{ID: "sec-providers", Title: "供应商", Rows: []setting{
			{ID: "row-providers-list", Aliases: []string{"供应商", "provider", "模型"}, Control: customFullWidth(func(c *ui.Context, st *SettingsState) {
				renderProvidersCard(c, st, modelsPageLibrary(c, st))
			})},
		}},
	},
}

func init() { settingsTreePageDefs = append(settingsTreePageDefs, pageModels) }

func renderAddModelPin(c *ui.Context, st *SettingsState) {
	lib := modelsPageLibrary(c, st)
	ps, client := lib.ps, lib.client
	const key = "models-add-pin"
	// 外层自定义行已经提供 settingsRowPadX，按钮不再叠加水平内边距。
	btn := ui.ButtonBase(c.Key(key+"-button")).Label("添加收藏").Padding(Space4, 0)
	btn.OnClick(func() {
		if ps.openPopoverKey == key {
			ps.openPopoverKey = ""
		} else {
			ps.openPopoverKey = key
		}
		st.invalidate()
	})
	btn.Children(func() { ui.Text(c, "+ 添加收藏").FontSize(uiFont(TypeFootnote)).TextColor(ThemeAccent(c)) })
	st.mu.Lock()
	cfg := st.cfg
	providerRows := make(map[string]ProviderSetting, len(st.providerRows.settings))
	for id, row := range st.providerRows.settings {
		providerRows[id] = row
	}
	st.mu.Unlock()
	// 浮层与模型库共用同一个启用判定，关闭的供应商整组隐藏。
	enabled := providerEnabledMap(cfg, providerRows, nil)
	// 与聊天模型选择器共用同一份 Magpie 过滤规则，关闭时整组不列出。
	magpieFilter := newModelPickerMagpieFilter(cfg)
	providers := filterModelPickerMagpie(lib.providers, magpieFilter)
	items := []modelPickerItem{}
	pins := map[string]ModelPin{}
	for _, provider := range providers {
		if providerDisabled(enabled, provider.ID) || len(provider.Models) == 0 {
			continue
		}
		for _, item := range providerModelPickerItems(ps, client, provider) {
			item.ProviderID = provider.ID
			item.Section = provider.Name
			if item.Section == "" {
				item.Section = provider.ID
			}
			pins[item.Key] = ModelPin{Provider: provider.ID, ModelID: item.ID}
			items = append(items, item)
		}
	}
	renderModelPickerPopoverWithNotice(c, st, ps, key, btn, items, "暂无可添加的模型", providerModelLimitHint(ps), func(item modelPickerItem, checked bool) {
		pin := pins[item.Key]
		if checked {
			magpieAddModelPin(st, ps, client, pin)
		} else {
			ps.removePin(st, client, pin)
		}
	}, modelPickerCustom{})
}

func renderPinnedModels(c *ui.Context, st *SettingsState) {
	lib := modelsPageLibrary(c, st)
	ps, client, providers := lib.ps, lib.client, lib.providers
	st.mu.Lock()
	cfg := st.cfg
	providerRows := make(map[string]ProviderSetting, len(st.providerRows.settings))
	for id, row := range st.providerRows.settings {
		providerRows[id] = row
	}
	refreshDone, refreshErr := st.modelsRefreshDone, st.modelsRefreshErr
	st.mu.Unlock()
	// 加载失败与目录失败共用一条错误行；写操作失败单独一行，不隐藏「你的模型」。
	loadErrText := ps.loadErr
	if loadErrText == "" {
		loadErrText = ps.catalogErr
	}
	if loadErrText != "" || ps.opErr != "" || refreshDone {
		status := ui.Column(c.Key("models-pinned-status")).Padding(settingsRowPadY, settingsRowPadX)
		status.Children(func() {
			if loadErrText != "" {
				ui.Text(c, loadErrText).FontSize(uiFont(TypeFootnote)).TextColor(ThemeDanger(c))
				ui.Button(c.Key("models-retry"), "重新加载").OnClick(func() {
					ps.loaded = false
					ps.loadErr = ""
					ps.catalogErr = ""
					st.invalidate()
				})
			}
			if ps.opErr != "" {
				ui.Text(c, ps.opErr).FontSize(uiFont(TypeFootnote)).TextColor(ThemeDanger(c))
			}
			if refreshDone {
				if refreshErr != "" {
					ui.Text(c, refreshErr).FontSize(uiFont(TypeFootnote)).TextColor(ThemeDanger(c))
				} else {
					ui.Text(c, "模型列表已刷新").FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
				}
			}
		})
	}

	// 1. 区块“收藏模型”
	// 已关闭供应商的固定模型只在渲染时跳过，不从配置里删除。
	enabledProviders := providerEnabledMap(cfg, providerRows, ps.providerSettings)
	visiblePins := filterPinsByEnabledProviders(ps.pins, enabledProviders)
	if ps.loadErr != "" {
		return
	}
	if len(ps.pins) == 0 {
		renderSettingRow(c, "row-models-none", "尚未选择模型", func() {})
	} else {
		for i, pin := range visiblePins {
			if i > 0 {
				renderSettingsDivider(c, fmt.Sprintf("div-models-pin-%d", i))
			}
			renderPinnedModelRow(c, st, ps, client, providers, visiblePins, i, pin)
		}
	}

	if hint := providerModelLimitHint(ps); hint != "" {
		ui.Text(c, hint).FontSize(uiFont(TypeFootnote)).TextColor(ThemeDanger(c)).Padding(0, settingsRowPadX)
	}
}

// providerModelsFor 在模型库里查找指定供应商，找不到（或没有模型库）时返回 false。
func providerModelsFor(lib *modelsLibraryView, id string) (ProviderInfo, bool) {
	if lib == nil {
		return ProviderInfo{}, false
	}
	for _, p := range lib.providers {
		if p.ID == id {
			return p, true
		}
	}
	return ProviderInfo{}, false
}

// leftoverProviderModels 返回没有对应供应商行的供应商（例如 API 预设的模型），保持原顺序。
func leftoverProviderModels(providers []ProviderInfo, rowIDs []string) []ProviderInfo {
	hasRow := make(map[string]bool, len(rowIDs))
	for _, id := range rowIDs {
		hasRow[id] = true
	}
	var left []ProviderInfo
	for _, p := range providers {
		if hasRow[p.ID] {
			continue
		}
		left = append(left, p)
	}
	return left
}

func renderPinnedModelRow(c *ui.Context, st *SettingsState, ps *modelsPageState, client Client, providers []ProviderInfo, visiblePins []ModelPin, idx int, pin ModelPin) {
	pinKey := pin.Key()
	modelName := resolveModelName(providers, pin)
	if modelName == pin.ModelID {
		modelName = prettyModelID(pin.ModelID)
	}
	summary := modelPinSummary(providers, pin, ps.preferences[pinKey])
	accessibleName := modelPinAgentName(providers, pin) + " " + modelName
	rowKey := "row-models-pin-" + pinKey

	row := ui.Row(c.Key(rowKey)).
		Height(settingsModelRowHeight).
		Padding(Space4, Space5).
		Gap(Space4).
		AlignItems(ui.Center).
		Children(func() {
			handle := ui.Row(c.Key("handle-"+pinKey)).
				Label("拖动排序 "+accessibleName).
				Tooltip("拖拽排序").
				Size(ControlSM, ControlSM).
				AlignItems(ui.Center).
				Justify(ui.Center).
				Cursor(ui.CursorGrab)
			if _, dy, held := handle.Dragged(); held {
				if ps.dragFrom < 0 {
					ps.dragFrom = idx
				}
				ps.dragDy += dy
				handle.Cursor(ui.CursorGrabbing)
			}
			handle.Children(func() {
				opacity := float32(0.7)
				if handle.Hovered() {
					opacity = 1
				}
				ui.Icon(c, iconGrip).Size(IconMD, IconMD).TextColor(ThemeSecondaryText(c)).Opacity(opacity)
			})

			// agent 图标：固定 16×16 槽位，没有原色图标的 provider 用通用图标保持对齐。
			ui.Row(c.Key("icon-"+pinKey)).
				Size(IconMD, IconMD).
				AlignItems(ui.Center).
				Justify(ui.Center).
				Children(func() {
					renderProviderRowIcon(c, pin.Provider, 16)
				})

			if _, _, ended := handle.DragEnded(); ended && ps.dragFrom == idx {
				target := modelDragTargetIndex(idx, ps.dragDy, 59, len(visiblePins))
				from, to := -1, -1
				for i, p := range ps.pins {
					if p == pin {
						from = i
					}
					if p == visiblePins[target] {
						to = i
					}
				}
				pins, err := commitModelReorder(client, ps.pins, from, to)
				if err != nil {
					ps.opErr = "保存模型顺序失败：" + err.Error()
				} else {
					ps.pins = pins
					ps.opErr = ""
					ps.syncSettings(st)
				}
				ps.dragFrom, ps.dragDy = -1, 0
				st.invalidate()
			}

			// 2. 行主体（点击打开 Popover）
			rowBody := ui.ButtonBase(c.Key("body-"+pinKey)).
				Label(accessibleName).
				Grow(1).
				Justify(ui.Start).
				AlignItems(ui.Center).
				Padding(Space1, Space2).
				Radius(RadiusSM)

			if rowBody.Hovered() {
				rowBody.Background(ThemeHover(c))
			} else {
				rowBody.Background(ui.Transparent)
			}

			rowBody.OnClick(func() {
				if ps.openPopoverKey == pinKey {
					ps.openPopoverKey = ""
				} else {
					ps.openPopoverKey = pinKey
				}
				st.invalidate()
			})

			rowBody.Children(func() {
				ui.Column(c.Key("col-" + pinKey)).
					Gap(Space0).
					Children(func() {
						ui.Row(c.Key("pin-name-" + pinKey)).Gap(Space2).AlignItems(ui.Center).Children(func() {
							ui.Text(c, modelName).Tooltip(pin.ModelID).FontSize(uiFont(TypeBody)).FontWeight(500).TextColor(ThemePrimaryText(c))
							if _, ok := magpieModelSource(st, pin.Provider, pin.ModelID); ok {
								ui.Text(c, "Magpie").FontSize(uiFont(TypeCaption)).TextColor(ThemeSecondaryText(c))
							}
						})
						ui.Text(c, summary).
							FontSize(uiFont(TypeCaption)).
							TextColor(ThemeSecondaryText(c))
					})
			})

			isPopOpen := (ps.openPopoverKey == pinKey)
			appPopover(c.Key("popover-"+pinKey), rowBody, &isPopOpen, func() {
				if ps.openPopoverKey == pinKey {
					ps.openPopoverKey = ""
				}
			}, func() {
				popover := ui.Column(c.Key("popover-content-"+pinKey)).
					Width(settingsModelsPanelWidth).
					Padding(Space2).
					Radius(RadiusLG).
					Background(ThemeSurface(c)).
					Border(Hairline, ThemeBorder(c))
				applyElevation(c, popover, ElevPopover).Children(func() {
					pref := ps.preferences[pinKey]
					var curEffort string
					if pref.Effort != nil {
						curEffort = *pref.Effort
					}
					// 固定模型的推理强度卡片：复用公共模型选择组件的 Simple 模式（Inline 只渲染卡片）。
					renderModelPicker(c, ModelPickerProps{
						Mode:      ModelPickerSimple,
						Inline:    true,
						KeyPrefix: "model-pref-" + pinKey + "-",
						ScopeKey:  "model-pref-" + pinKey,
						Providers: pinPickerProviders(providers, pin),
						Selection: ModelSelection{Provider: pin.Provider, Model: pin.ModelID, Effort: curEffort, Fast: pref.FastMode},
						Disabled:  client == nil,
						OnEffort: func(_ ModelPickerSide, eff string) {
							newPref := ps.preferences[pinKey]
							newPref.Effort = &eff
							ps.savePreference(st, client, pin, newPref)
						},
						OnToggleFast: func(ModelPickerSide) {
							newPref := ps.preferences[pinKey]
							newPref.FastMode = !newPref.FastMode
							ps.savePreference(st, client, pin, newPref)
						},
						OnReset: func(ModelPickerSide) {
							newPref := ModelPreference{Effort: nil, FastMode: false}
							ps.savePreference(st, client, pin, newPref)
						},
						Invalidate: st.invalidate,
					})
				})
			})
			if !isPopOpen && ps.openPopoverKey == pinKey {
				ps.openPopoverKey = ""
			}

			// 3. 减号移除按钮
			btnRemove := ui.ButtonBase(c.Key("models-pin-remove-"+pinKey)).
				Label("移除收藏 "+accessibleName).
				Size(ControlSM, ControlSM).
				AlignItems(ui.Center).
				Justify(ui.Center).
				Radius(RadiusXS)

			if btnRemove.Hovered() {
				btnRemove.Background(ThemeHover(c))
			} else {
				btnRemove.Background(ui.Transparent)
			}

			btnRemove.Disabled(client == nil)
			btnRemove.OnClick(func() {
				ps.removePin(st, client, pin)
			})

			btnRemove.Children(func() {
				ui.Icon(c, iconMinusCircle).
					Size(IconMD, IconMD).
					TextColor(ThemeSecondaryText(c))
			})
		})
	offset := float32(0)
	if ps.dragFrom >= 0 {
		target := modelDragTargetIndex(ps.dragFrom, ps.dragDy, 59, len(visiblePins))
		if idx == ps.dragFrom {
			offset = ps.dragDy
			row.Background(ThemeSurface(c)).Radius(RadiusMD)
		} else if idx > ps.dragFrom && idx <= target {
			offset = -59
		} else if idx < ps.dragFrom && idx >= target {
			offset = 59
		}
	}
	if ps.dragFrom < 0 {
		row.Jump("models-pin-offset", 0)
		offset = 0
	} else if idx == ps.dragFrom {
		row.Jump("models-pin-offset", offset)
	} else {
		offset = row.AnimateTo("models-pin-offset", offset, springSnappy)
	}
	row.Translate(0, offset)
}

// renderProviderModelSearchBox 渲染某个供应商模型列表顶部的局部搜索框：左侧放大镜图标，
// 右侧在搜索词非空时显示清除按钮。返回节点，便于调用方按需控制宽度（如 Grow）。
func renderProviderModelSearchBox(c *ui.Context, st *SettingsState, id string, query *string) ui.Element {
	field := SearchField(c, TextFieldProps{
		Key:         "input-provider-models-search-" + id,
		Value:       query,
		Label:       "input-provider-models-search-" + id,
		Placeholder: "搜索模型名称或 ID",
		Clearable:   true,
		ClearLabel:  "clear-provider-models-search-" + id,
		Grow:        true,
		OnChange: func(string) {
			st.invalidate()
		},
	})
	return field.Margin(0, 0, Space3, 0)
}

// providerModelsListOptions 是 renderProviderModelsListExt 的渲染选项。
type providerModelsListOptions struct {
	// ExcludePin 返回 true 的 pin 不计入「已添加」，也不在已添加列表里显示；nil 表示不排除。
	ExcludePin func(ModelPin) bool
}

// renderProviderModelsList 渲染某个供应商的模型区：页面只列出已添加的模型，
// 其余模型收进「添加模型」浮层；设置页全局搜索非空时仍平铺命中的模型。
func renderProviderModelsList(c *ui.Context, st *SettingsState, ps *modelsPageState, client Client, p ProviderInfo) {
	if strings.TrimSpace(ps.searchQuery) != "" {
		// 全局搜索时按供应商平铺命中模型，保持跨供应商浏览的习惯。
		for i, m := range p.Models {
			renderSettingsDivider(c, fmt.Sprintf("div-provider-%s-%d", p.ID, i))
			renderProviderModelRow(c, st, ps, client, p, m)
		}
		return
	}
	renderProviderModelsListExt(c, st, ps, client, p, providerModelsListOptions{})
}

// renderProviderModelsListExt 渲染「收藏（N）+ 添加模型」布局；
// opts.ExcludePin 可以把某些 pin 排除在「收藏」之外，供 Magpie 面板复用。
func renderProviderModelsListExt(c *ui.Context, st *SettingsState, ps *modelsPageState, client Client, p ProviderInfo, opts providerModelsListOptions) {
	for _, model := range p.Models {
		name := strings.TrimSpace(model.Name)
		if name == "" {
			name = model.ID
		}
		ui.Text(c, name).FontSize(uiFont(TypeBody)).TextColor(ThemePrimaryText(c)).Padding(Space2, Space5)
	}
}

// providerModelPickerItems 把供应商模型转换成浮层选项：主模型在前、次要模型在后，
// 已加入的显示为勾选；不能编辑、加载失败或达到数量上限时禁用未加入的选项。
func providerModelPickerItems(ps *modelsPageState, client Client, p ProviderInfo) []modelPickerItem {
	// 加载失败时不知道当前固定模型有哪些，沿用旧「+」按钮的规则禁止添加，避免覆盖列表。
	editable := ps.canEdit() && ps.loadErr == "" && client != nil
	hasSecondary := false
	for _, m := range p.Models {
		if m.Secondary {
			hasSecondary = true
			break
		}
	}
	items := make([]modelPickerItem, 0, len(p.Models))
	for pass := 0; pass < 2; pass++ {
		for _, m := range p.Models {
			if (pass == 1) != m.Secondary {
				continue
			}
			name := strings.TrimSpace(m.Name)
			if name == "" {
				name = m.ID
			}
			pin := ModelPin{Provider: p.ID, ModelID: m.ID}
			checked := modelPinPresent(ps.pins, pin)
			section := ""
			if hasSecondary {
				section = "主模型"
				if m.Secondary {
					section = "次要模型"
				}
			}
			items = append(items, modelPickerItem{
				Section:  section,
				Key:      "provider-pick-" + pin.Key(),
				Name:     name,
				ID:       m.ID,
				Detail:   m.Detail,
				Checked:  checked,
				Disabled: !editable || (!checked && !canAddModel(ps.pins, ps.limit, pin)),
			})
		}
	}
	return items
}

// providerModelLimitHint 返回模型数量已达上限时的提示，未达上限返回空串。
func providerModelLimitHint(ps *modelsPageState) string {
	if ps.limit <= 0 || len(ps.pins) < ps.limit {
		return ""
	}
	return fmt.Sprintf("收藏模型已达上限（%d 个），先移除一些再添加", ps.limit)
}

// formatModelCost 将 API 参考价格式化为「API 参考价 $输入/$输出 每百万 token」，c 为 nil 时返回空串。
func formatModelCost(c *ModelCost) string {
	if c == nil {
		return ""
	}
	trim := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	return "API 参考价 $" + trim(c.Input) + "/$" + trim(c.Output) + " 每百万 token"
}

// providerModelRowExtras 是模型行的可选附加行为：leading 渲染在行首（名称列之前），
// beforeAdd 在点「+」时、canAddModel 判断之后最先同步调用，返回错误则不再加入列表。
type providerModelRowExtras struct {
	leading   func()
	beforeAdd func() error
	// inset 是行的左右内边距；0 表示沿用默认的 16。
	inset int
	// addTooltip 是「+」按钮的悬停提示，空串表示不显示。
	addTooltip string
}

// providerModelRowInset 返回模型行的左右内边距，inset 为 0 时用默认值。
func providerModelRowInset(inset int) float32 {
	if inset <= 0 {
		return 16
	}
	return float32(inset)
}

func renderProviderModelRow(c *ui.Context, st *SettingsState, ps *modelsPageState, client Client, p ProviderInfo, m ModelInfo) {
	renderProviderModelRowExt(c, st, ps, client, p, m, providerModelRowExtras{})
}

func renderProviderModelRowExt(c *ui.Context, st *SettingsState, ps *modelsPageState, client Client, p ProviderInfo, m ModelInfo, extras providerModelRowExtras) {
	pin := ModelPin{Provider: p.ID, ModelID: m.ID}
	pinKey := pin.Key()
	dispName := m.Name
	if dispName == "" {
		dispName = m.ID
	}
	var descParts []string
	if m.Detail != "" {
		descParts = append(descParts, m.Detail)
	}
	if cost := formatModelCost(m.Cost); cost != "" {
		descParts = append(descParts, cost)
	}
	desc := strings.Join(descParts, " · ")

	isAdded := false
	for _, pPin := range ps.pins {
		if pPin.Key() == pinKey {
			isAdded = true
			break
		}
	}
	canAdd := ps.canEdit() && ps.loadErr == "" && client != nil && canAddModel(ps.pins, ps.limit, pin)

	ui.Row(c.Key("row-provider-model-"+pinKey)).
		Padding(Space4, providerModelRowInset(extras.inset)).
		Gap(Space4).
		AlignItems(ui.Center).
		Children(func() {
			if extras.leading != nil {
				extras.leading()
			}
			ui.Column(c.Key("col-provider-model-" + pinKey)).
				Grow(1).
				Gap(Space0).
				Children(func() {
					ui.Text(c, dispName).
						FontSize(uiFont(TypeBody)).
						FontWeight(500).
						TextColor(ThemePrimaryText(c))
					if desc != "" {
						ui.Text(c, desc).
							FontSize(uiFont(TypeCaption)).
							TextColor(ThemeSecondaryText(c))
					}
				})

			if isAdded {
				// 已添加的模型可以在这里直接移除：高度固定 24 与「+」按钮一致，行高不变。
				// 宽度也固定 72 并右对齐内容：悬停时文字从「已收藏」换成「移除」，若宽度随内容收缩，
				// 按钮左边缘会移动，指针停在左侧那一段时会在悬停/非悬停之间来回切换而闪烁。
				canRemove := ps.canEdit() && ps.loadErr == "" && client != nil
				btnAdded := ui.ButtonBase(c.Key("models-added-"+pinKey)).
					Label("models-added-"+pinKey).
					Size(settingsModelPickerWidth, ControlSM).
					Padding(0, Space2).
					AlignItems(ui.Center).
					Justify(ui.End).
					Gap(Space1).
					Radius(RadiusXS).
					Disabled(!canRemove)

				if btnAdded.Hovered() && canRemove {
					btnAdded.Background(ThemeHover(c))
				} else {
					btnAdded.Background(ui.Transparent)
				}

				if canRemove {
					btnAdded.OnClick(func() {
						ps.removePin(st, client, pin)
					})
				}

				btnAdded.Children(func() {
					// 悬停且可移除时显示危险色的「移除」，其余时候保持静态的「已收藏」。
					if canRemove && btnAdded.Hovered() {
						ui.Icon(c, iconMinusCircle).
							Size(IconSM, IconSM).
							TextColor(ThemeDanger(c))
						ui.Text(c, "移除").
							FontSize(uiFont(TypeFootnote)).
							TextColor(ThemeDanger(c))
						return
					}
					ui.Icon(c, IconCheck()).
						Size(IconSM, IconSM).
						TextColor(ThemeSecondaryText(c))
					ui.Text(c, "已收藏").
						FontSize(uiFont(TypeFootnote)).
						TextColor(ThemeSecondaryText(c))
				})
			} else {
				btnAdd := ui.ButtonBase(c.Key("models-add-"+pinKey)).
					Label("models-add-"+pinKey).
					Size(ControlSM, ControlSM).
					AlignItems(ui.Center).
					Justify(ui.Center).
					Disabled(!canAdd).
					Radius(RadiusXS)
				if extras.addTooltip != "" {
					btnAdd.Tooltip(extras.addTooltip)
				}

				if btnAdd.Hovered() && canAdd {
					btnAdd.Background(ThemeHover(c))
				} else {
					btnAdd.Background(ui.Transparent)
				}

				if canAdd {
					btnAdd.OnClick(func() {
						if !canAddModel(ps.pins, ps.limit, pin) {
							return
						}
						if extras.beforeAdd != nil {
							if err := extras.beforeAdd(); err != nil {
								ps.opErr = "添加模型失败：" + err.Error()
								st.invalidate()
								return
							}
						}
						pref, err := client.ModelPreferences(context.Background(), pin)
						if err == nil {
							var added bool
							added, err = client.AddModelToList(context.Background(), pin)
							if err == nil && added {
								ps.pins = append(ps.pins, pin)
								ps.preferences[pinKey] = pref
								ps.syncSettings(st)
							}
						}
						if err != nil {
							ps.opErr = "添加模型失败：" + err.Error()
						} else {
							ps.opErr = ""
							ps.refreshPins(st, client)
						}
						st.invalidate()
					})
				}

				btnAdd.Children(func() {
					col := ThemeAccent(c)
					op := float32(1.0)
					if !canAdd {
						col = ThemeSecondaryText(c)
						op = 0.35
					}
					ui.Icon(c, IconPlus()).
						Size(IconMD, IconMD).
						TextColor(col).
						Opacity(op)
				})
			}
		})
}
