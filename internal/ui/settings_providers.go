package mcodeui

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/egoist/mygo/ui"
)

// customEndpointsPageState 维护自定义 OpenAI 兼容端点页面的交互状态。
type customEndpointsPageState struct {
	endpoints []CustomEndpoint
	loaded    bool
	loading   bool
	err       string

	// 表单状态
	editing     bool
	editScope   string // 当前展开的表单归属：custom / magpie；空值按 custom 处理
	editID      string // 为空表示新建，非空为正在编辑的端点 ID
	editName    string
	editBaseURL string
	editMagpie  bool
	editModel   string
	editAPIKey  string
	editEnabled bool
	editErr     string
	saving      bool

	// 人设来源草稿（只有 Magpie 端点用得上）；web key 明文只存在于 editWebKey，
	// 保存成功或取消编辑后立即清空，不写日志、不进任何结果文案。
	editPersonaWebURL string
	editWebKey        string
	editPersonasJSON  string
	editPersonaRules  bool
	editHasWebKey     bool // 编辑已有端点时带入 HasWebKey，仅用于提示是否已保存 web key
	editHasAPIKey     bool // 编辑已有端点时带入 HasAPIKey，仅用于提示是否已保存 API Key

	// Magpie 菜单栏额度供应商草稿
	editQuotaProviders     map[string]bool
	quotaProviderList      []string
	quotaProviderLoading   bool
	quotaProviderErr       string
	quotaProviderLoadedFor string

	// 测试状态
	testing    bool
	testResult *CustomEndpointTestResult
	testErr    string

	// 人设来源测试状态（与连通性测试互不影响）
	personaTestBusy   bool
	personaTestShown  bool
	personaTestErr    string
	personaTestResult PersonaSourceTestResult

	// 删除二次确认状态
	deleteConfirmID string
	deletingID      string
	toggling        bool
}

// 表单归属：自定义 Endpoints 与 Magpie 网关共用同一套表单草稿，用 editScope 区分。
const (
	endpointFormScopeCustom = "custom"
	endpointFormScopeMagpie = "magpie"
)

// clearCustomEndpointPersonaDrafts 清空人设来源与额度供应商草稿（含明文 web key）；调用方需持有 st.mu。
func clearCustomEndpointPersonaDrafts(st *SettingsState) {
	st.endpoints.editPersonaWebURL = ""
	st.endpoints.editWebKey = ""
	st.endpoints.editPersonasJSON = ""
	st.endpoints.editPersonaRules = false
	st.endpoints.editHasWebKey = false
	st.endpoints.editHasAPIKey = false
	st.endpoints.editQuotaProviders = nil
	st.endpoints.quotaProviderList = nil
	st.endpoints.quotaProviderLoading = false
	st.endpoints.quotaProviderErr = ""
	st.endpoints.quotaProviderLoadedFor = ""
}

// openCustomEndpointForm 打开端点表单并预填草稿；ep 为 nil 表示新建，scope 决定表单归属。
// Magpie 范围必须置 editMagpie，否则 customEndpointFormEndpoint 会清空人设字段。
// 调用方需持有 st.mu。
func openCustomEndpointForm(st *SettingsState, ep *CustomEndpoint, scope string) {
	clearCustomEndpointPersonaDrafts(st)
	st.endpoints.editing = true
	st.endpoints.editScope = scope
	st.endpoints.editID = ""
	st.endpoints.editName = ""
	st.endpoints.editBaseURL = ""
	st.endpoints.editMagpie = scope == endpointFormScopeMagpie
	st.endpoints.editModel = ""
	st.endpoints.editAPIKey = ""
	st.endpoints.editEnabled = true
	st.endpoints.editErr = ""
	st.endpoints.personaTestShown = false
	st.endpoints.personaTestErr = ""
	st.endpoints.testResult = nil
	st.endpoints.testErr = ""
	st.endpoints.deleteConfirmID = ""
	if ep == nil {
		return
	}
	st.endpoints.editID = ep.ID
	st.endpoints.editName = ep.Name
	st.endpoints.editBaseURL = ep.BaseURL
	// Magpie 标记只由范围决定，避免串到普通端点。
	st.endpoints.editMagpie = scope == endpointFormScopeMagpie
	st.endpoints.editModel = ep.Model
	// 人设来源预填已保存的值，明文 web key 永远留空
	st.endpoints.editPersonaWebURL = ep.PersonaWebURL
	st.endpoints.editPersonasJSON = ep.PersonasJSON
	st.endpoints.editPersonaRules = ep.PersonaRules
	st.endpoints.editHasWebKey = ep.HasWebKey
	st.endpoints.editHasAPIKey = ep.HasAPIKey
	st.endpoints.editQuotaProviders = resolveMagpieQuotaSelection(ep.QuotaProviders)
	st.endpoints.editEnabled = ep.Enabled
}

// closeCustomEndpointForm 收起端点表单并清空草稿（含明文 web key）；调用方需持有 st.mu。
func closeCustomEndpointForm(st *SettingsState) {
	st.endpoints.editing = false
	st.endpoints.editScope = ""
	st.endpoints.editID = ""
	st.endpoints.editName = ""
	st.endpoints.editBaseURL = ""
	st.endpoints.editMagpie = false
	st.endpoints.editModel = ""
	st.endpoints.editAPIKey = ""
	st.endpoints.editErr = ""
	st.endpoints.testResult = nil
	st.endpoints.testErr = ""
	clearCustomEndpointPersonaDrafts(st)
	st.endpoints.personaTestShown = false
	st.endpoints.personaTestErr = ""
}

// customEndpointFormEndpoint 用当前表单草稿组装自定义端点；调用方需持有 st.mu。
// 人设来源只有 Magpie 端点用得上，非 Magpie 端点一律留空（后端也会清掉）。
func customEndpointFormEndpoint(st *SettingsState) CustomEndpoint {
	ep := CustomEndpoint{
		ID:      st.endpoints.editID,
		Name:    strings.TrimSpace(st.endpoints.editName),
		BaseURL: strings.TrimSpace(st.endpoints.editBaseURL),
		Magpie:  st.endpoints.editMagpie,
		Model:   strings.TrimSpace(st.endpoints.editModel),
		Enabled: st.endpoints.editEnabled,
	}
	if ep.Magpie {
		ep.PersonaWebURL = strings.TrimSpace(st.endpoints.editPersonaWebURL)
		ep.PersonasJSON = strings.TrimSpace(st.endpoints.editPersonasJSON)
		ep.PersonaRules = st.endpoints.editPersonaRules
		ep.HasWebKey = st.endpoints.editHasWebKey

		// 先按 quotaProviderList 的顺序取勾选的 id，再追加不在列表里但仍勾选的 id。
		// 结果必须是非 nil 切片，一个都没勾时用空切片，表示一个都不展示。
		selected := make([]string, 0)
		seen := make(map[string]bool)
		for _, id := range st.endpoints.quotaProviderList {
			if st.endpoints.editQuotaProviders != nil && st.endpoints.editQuotaProviders[id] {
				selected = append(selected, id)
				seen[id] = true
			}
		}
		for id, checked := range st.endpoints.editQuotaProviders {
			if checked && !seen[id] {
				selected = append(selected, id)
			}
		}
		ep.QuotaProviders = selected
	}
	return ep
}

// personaWebURLNeedsHTTPSWarning 判断 web 管理地址是否会把 web key 明文发到网络上：
// 明文 http 且主机不是本机回环地址时需要提示改用 https 或 SSH 隧道。
func personaWebURLNeedsHTTPSWarning(raw string) bool {
	rest, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(raw)), "http://")
	if !ok {
		return false
	}
	// 去掉路径、查询与片段，只留 authority 部分
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	host := rest
	if h, _, err := net.SplitHostPort(rest); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	switch host {
	case "", "localhost", "127.0.0.1", "::1":
		return false
	}
	return true
}

func (st *SettingsState) customEndpointsClient() Client {
	st.mu.Lock()
	chat := st.chat
	st.mu.Unlock()
	if chat != nil {
		return chat.client
	}
	return nil
}

// refreshCustomEndpointProviders 同步会话选择器缓存，避免保存端点后必须重启应用。
func refreshCustomEndpointProviders(st *SettingsState, client Client) {
	providers, err := client.Providers(context.Background())
	if err != nil {
		return
	}
	st.mu.Lock()
	chat := st.chat
	st.mu.Unlock()
	if chat == nil {
		return
	}
	chat.mu.Lock()
	chat.providers = providers
	found := false
	for _, p := range providers {
		if p.ID == chat.newThreadProvider {
			found = true
			break
		}
	}
	if !found {
		chat.newThreadProvider = ""
		for _, p := range providers {
			if chat.newThreadProvider == "" || p.Available {
				chat.newThreadProvider = p.ID
			}
			if p.Available {
				break
			}
		}
	}
	chat.mu.Unlock()
}

// loadCustomEndpoints 异步加载自定义端点列表。
func loadCustomEndpoints(st *SettingsState) {
	client := st.customEndpointsClient()
	if client == nil {
		return
	}
	st.mu.Lock()
	if st.endpoints.loading {
		st.mu.Unlock()
		return
	}
	st.endpoints.loading = true
	st.endpoints.err = ""
	st.mu.Unlock()
	st.invalidate()

	go func() {
		list, err := client.CustomEndpoints(context.Background())
		st.mu.Lock()
		st.endpoints.loading = false
		st.endpoints.loaded = true
		if err != nil {
			st.endpoints.err = err.Error()
		} else {
			st.endpoints.err = ""
			st.endpoints.endpoints = list
		}
		st.mu.Unlock()
		st.invalidate()
	}()
}

// saveCustomEndpoint 异步保存当前编辑的端点（新建或更新）。
func saveCustomEndpoint(st *SettingsState) {
	client := st.customEndpointsClient()
	if client == nil {
		return
	}
	st.mu.Lock()
	if st.endpoints.saving || st.endpoints.testing || st.endpoints.toggling || st.endpoints.deletingID != "" {
		st.mu.Unlock()
		return
	}
	ep := customEndpointFormEndpoint(st)
	apiKey := strings.TrimSpace(st.endpoints.editAPIKey)
	// webKey 为空表示保留已保存的 web 管理密钥
	webKey := strings.TrimSpace(st.endpoints.editWebKey)
	st.endpoints.saving = true
	st.endpoints.editErr = ""
	st.mu.Unlock()
	st.invalidate()

	go func() {
		saved, err := client.SaveCustomEndpoint(context.Background(), ep, apiKey, webKey)
		if err != nil {
			st.mu.Lock()
			st.endpoints.saving = false
			st.endpoints.editErr = err.Error()
			st.mu.Unlock()
			st.invalidate()
			return
		}

		list, lErr := client.CustomEndpoints(context.Background())
		refreshCustomEndpointProviders(st, client)

		st.mu.Lock()
		st.endpoints.saving = false
		closeCustomEndpointForm(st)
		if lErr != nil {
			st.endpoints.err = lErr.Error()
		} else {
			st.endpoints.err = ""
			st.endpoints.endpoints = list
			st.endpoints.loaded = true
		}
		st.mu.Unlock()
		if saved.Magpie {
			st.loadPiMagpieEndpoints()
			markMagpieHubClaudeStale(st)
		}
		st.refreshModels()
		st.invalidate()
	}()
}

// deleteCustomEndpoint 异步删除指定端点。
func deleteCustomEndpoint(st *SettingsState, id string) {
	client := st.customEndpointsClient()
	if client == nil {
		return
	}
	st.mu.Lock()
	if st.endpoints.deletingID != "" || st.endpoints.saving || st.endpoints.testing || st.endpoints.toggling {
		st.mu.Unlock()
		return
	}
	st.endpoints.deletingID = id
	st.endpoints.err = ""
	wasMagpie := false
	for _, item := range st.endpoints.endpoints {
		if item.ID == id {
			wasMagpie = item.Magpie
			break
		}
	}
	st.mu.Unlock()
	st.invalidate()

	go func() {
		err := client.DeleteCustomEndpoint(context.Background(), id)
		if err != nil {
			st.mu.Lock()
			st.endpoints.deletingID = ""
			st.endpoints.err = err.Error()
			st.mu.Unlock()
			st.invalidate()
			return
		}

		list, lErr := client.CustomEndpoints(context.Background())
		refreshCustomEndpointProviders(st, client)

		st.mu.Lock()
		st.endpoints.deletingID = ""
		st.endpoints.deleteConfirmID = ""
		if lErr != nil {
			st.endpoints.err = lErr.Error()
		} else {
			st.endpoints.err = ""
			st.endpoints.endpoints = list
			st.endpoints.loaded = true
		}
		if st.endpoints.editID == id {
			closeCustomEndpointForm(st)
		}
		st.mu.Unlock()
		if wasMagpie {
			// 网关列表与 claude 快照都依赖端点集合，删除后重新读取。
			st.loadPiMagpieEndpoints()
			markMagpieHubClaudeStale(st)
		}
		st.invalidate()
	}()
}

// toggleCustomEndpointEnabled 异步切换端点启用状态。
func toggleCustomEndpointEnabled(st *SettingsState, ep CustomEndpoint, enabled bool) {
	client := st.customEndpointsClient()
	if client == nil {
		return
	}
	ep.Enabled = enabled
	st.mu.Lock()
	if st.endpoints.saving || st.endpoints.testing || st.endpoints.toggling || st.endpoints.deletingID != "" {
		st.mu.Unlock()
		return
	}
	st.endpoints.toggling = true
	st.endpoints.err = ""
	for i, item := range st.endpoints.endpoints {
		if item.ID == ep.ID {
			st.endpoints.endpoints[i].Enabled = enabled
			break
		}
	}
	st.mu.Unlock()
	st.invalidate()

	go func() {
		// ep 是列表里读回的完整端点，PersonaWebURL/PersonasJSON/PersonaRules/QuotaProviders 随原值写回；
		// 传空 webKey 表示保留已保存的 web 管理密钥。
		_, err := client.SaveCustomEndpoint(context.Background(), ep, "", "")
		if err != nil {
			st.mu.Lock()
			st.endpoints.toggling = false
			st.endpoints.err = err.Error()
			for i, item := range st.endpoints.endpoints {
				if item.ID == ep.ID {
					st.endpoints.endpoints[i].Enabled = !enabled
					break
				}
			}
			st.mu.Unlock()
			st.invalidate()
			return
		}

		list, lErr := client.CustomEndpoints(context.Background())
		refreshCustomEndpointProviders(st, client)

		st.mu.Lock()
		st.endpoints.toggling = false
		if lErr != nil {
			st.endpoints.err = lErr.Error()
		} else {
			st.endpoints.err = ""
			st.endpoints.endpoints = list
			st.endpoints.loaded = true
		}
		st.mu.Unlock()
		if ep.Magpie {
			// 启用状态决定 pi/claude 能不能用这个网关，重新读取两份快照。
			st.loadPiMagpieEndpoints()
			markMagpieHubClaudeStale(st)
		}
		st.invalidate()
	}()
}

// testCustomEndpoint 异步测试端点连通性。
func testCustomEndpoint(st *SettingsState) {
	client := st.customEndpointsClient()
	if client == nil {
		return
	}
	st.mu.Lock()
	if st.endpoints.testing || st.endpoints.saving || st.endpoints.toggling || st.endpoints.deletingID != "" {
		st.mu.Unlock()
		return
	}
	ep := customEndpointFormEndpoint(st)
	apiKey := strings.TrimSpace(st.endpoints.editAPIKey)
	st.endpoints.testing = true
	st.endpoints.testResult = nil
	st.endpoints.testErr = ""
	st.mu.Unlock()
	st.invalidate()

	go func() {
		res, err := client.TestCustomEndpoint(context.Background(), ep, apiKey)
		st.mu.Lock()
		st.endpoints.testing = false
		if err != nil {
			st.endpoints.testErr = err.Error()
			st.endpoints.testResult = nil
		} else {
			st.endpoints.testErr = ""
			st.endpoints.testResult = &res
		}
		st.mu.Unlock()
		st.invalidate()
	}()
}

// testMagpiePersonas 异步测试当前表单的 Magpie 人设来源，不保存配置。
// ep 用表单草稿组装，webKey 取编辑草稿（为空时后端回落已保存的 web 管理密钥）。
func testMagpiePersonas(st *SettingsState) {
	client := st.customEndpointsClient()
	if client == nil {
		return
	}
	st.mu.Lock()
	if st.endpoints.personaTestBusy || st.endpoints.testing || st.endpoints.saving || st.endpoints.toggling || st.endpoints.deletingID != "" {
		st.mu.Unlock()
		return
	}
	ep := customEndpointFormEndpoint(st)
	webKey := strings.TrimSpace(st.endpoints.editWebKey)
	st.endpoints.personaTestBusy = true
	st.endpoints.personaTestShown = false
	st.endpoints.personaTestErr = ""
	st.mu.Unlock()
	st.invalidate()

	go func() {
		res, err := client.TestMagpiePersonas(context.Background(), ep, webKey)
		st.mu.Lock()
		st.endpoints.personaTestBusy = false
		if err != nil {
			st.endpoints.personaTestErr = err.Error()
			st.endpoints.personaTestShown = false
		} else {
			st.endpoints.personaTestErr = ""
			st.endpoints.personaTestResult = res
			st.endpoints.personaTestShown = true
		}
		st.mu.Unlock()
		st.invalidate()
	}()
}

// loadMagpieQuotaProviders 异步读取 Magpie 端点卡片上出现的供应商列表。
func loadMagpieQuotaProviders(st *SettingsState, endpointID string) {
	client := st.customEndpointsClient()
	if client == nil || strings.TrimSpace(endpointID) == "" {
		return
	}
	st.mu.Lock()
	if st.endpoints.quotaProviderLoading && st.endpoints.quotaProviderLoadedFor == endpointID {
		st.mu.Unlock()
		return
	}
	st.endpoints.quotaProviderLoading = true
	st.endpoints.quotaProviderErr = ""
	st.endpoints.quotaProviderLoadedFor = endpointID
	st.mu.Unlock()
	st.invalidate()

	go func() {
		list, err := client.MagpieQuotaProviders(context.Background(), endpointID)
		st.mu.Lock()
		if st.endpoints.quotaProviderLoadedFor != endpointID {
			st.mu.Unlock()
			return
		}
		st.endpoints.quotaProviderLoading = false
		if err != nil {
			st.endpoints.quotaProviderErr = err.Error()
			st.endpoints.quotaProviderList = nil
		} else {
			st.endpoints.quotaProviderErr = ""
			st.endpoints.quotaProviderList = list
		}
		st.mu.Unlock()
		st.invalidate()
	}()
}

// renderPageCustomEndpoints 渲染自定义 OpenAI 兼容端点设置区域。
func renderPageCustomEndpoints(c *ui.Context, st *SettingsState) {
	st.mu.Lock()
	loaded := st.endpoints.loaded
	loading := st.endpoints.loading
	eps := make([]CustomEndpoint, len(st.endpoints.endpoints))
	copy(eps, st.endpoints.endpoints)
	errText := st.endpoints.err
	// Magpie 网关由顶部 hub 管理，这里只列普通端点。
	kept := eps[:0]
	for _, ep := range eps {
		if !ep.Magpie {
			kept = append(kept, ep)
		}
	}
	eps = kept
	// 只在当前表单归属本区时展开；空归属按 custom 处理。
	editing := st.endpoints.editing && st.endpoints.editScope != endpointFormScopeMagpie
	editID := st.endpoints.editID
	editErr := st.endpoints.editErr
	saving := st.endpoints.saving
	testing := st.endpoints.testing
	var testResult *CustomEndpointTestResult
	if st.endpoints.testResult != nil {
		r := *st.endpoints.testResult
		testResult = &r
	}
	testErr := st.endpoints.testErr
	deleteConfirmID := st.endpoints.deleteConfirmID
	// API Key 草稿在后台保存成功的协程里会被清空，输入框不能绑裸指针，
	// 否则 UI 线程与协程会同时读写同一个字符串（数据竞争）。
	keyDraft := st.endpoints.editAPIKey
	nameDraft, baseDraft, modelDraft := st.endpoints.editName, st.endpoints.editBaseURL, st.endpoints.editModel
	personaTestBusy := st.endpoints.personaTestBusy
	busy := saving || testing || personaTestBusy || st.endpoints.toggling || st.endpoints.deletingID != ""
	st.mu.Unlock()

	// 首次渲染且未加载时触发加载
	if !loaded && !loading {
		loadCustomEndpoints(st)
	}

	renderSettingsSection(c, "sec-custom-endpoints", "自定义 Endpoints / 本地网关", func() {
		// 客户端全局错误显示在区块内
		if errText != "" {
			ui.Row(c.Key("custom-endpoints-err-row")).
				Padding(Space4, Space5).
				Children(func() {
					ui.Text(c, errText).
						FontSize(TypeBody).
						TextColor(ThemeDanger(c))
				})
			renderSettingsDivider(c, "div-custom-endpoints-err")
		}

		// 空列表提示
		if len(eps) == 0 {
			ui.Row(c.Key("custom-endpoints-empty-row")).
				Padding(Space4, Space5).
				Children(func() {
					text := "尚未添加自定义端点"
					if loading || !loaded {
						text = "正在加载端点…"
					}
					ui.Text(c, text).
						FontSize(TypeBody).
						TextColor(ThemeSecondaryText(c))
				})
		} else {
			// 端点列表
			for i, ep := range eps {
				if i > 0 {
					renderSettingsDivider(c, fmt.Sprintf("div-custom-ep-%d", i))
				}
				epVal := ep

				renderCustomEndpointRow(c, "row-custom-ep-"+epVal.ID, epVal.ID, epVal.Name, epVal, busy,
					func(enabled bool) { toggleCustomEndpointEnabled(st, epVal, enabled) },
					func() {
						// 编辑按钮
						editBtn := ui.ButtonBase(c.Key("btn-edit-ep-"+epVal.ID)).
							Label("编辑 "+epVal.Name).
							Disabled(busy).
							Radius(RadiusSM).
							Padding(Space1, Space4).
							Background(ThemeHover(c))
						if editBtn.Hovered() {
							editBtn.Background(ThemeOverlay(c, 0.15))
						}
						if editBtn.Clicked() {
							epCopy := epVal
							st.mu.Lock()
							openCustomEndpointForm(st, &epCopy, endpointFormScopeCustom)
							st.mu.Unlock()
							st.invalidate()
						}
						editBtn.Children(func() {
							ui.Text(c, "编辑").
								FontSize(TypeFootnote).
								TextColor(ThemeAccent(c))
						})

						// 删除按钮（需二次确认）
						isConfirm := (deleteConfirmID == epVal.ID)
						delLabel := "删除"
						if isConfirm {
							delLabel = "确认删除"
						}
						delBtn := ui.ButtonBase(c.Key("btn-del-ep-"+epVal.ID)).
							Label(delLabel+" "+epVal.Name).
							Disabled(busy).
							Radius(RadiusSM).
							Padding(Space1, Space4)
						if isConfirm {
							delBtn.Background(ThemeDanger(c).Alpha(0.12))
						} else {
							delBtn.Background(ThemeHover(c))
						}
						if delBtn.Hovered() {
							if isConfirm {
								delBtn.Background(ThemeDanger(c).Alpha(0.2))
							} else {
								delBtn.Background(ThemeOverlay(c, 0.15))
							}
						}
						if delBtn.Clicked() {
							if isConfirm {
								deleteCustomEndpoint(st, epVal.ID)
							} else {
								st.mu.Lock()
								st.endpoints.deleteConfirmID = epVal.ID
								st.mu.Unlock()
								st.invalidate()
							}
						}
						delBtn.Children(func() {
							delColor := ThemeSecondaryText(c)
							if isConfirm {
								delColor = ThemeDanger(c)
							}
							ui.Text(c, delLabel).
								FontSize(TypeFootnote).
								TextColor(delColor)
						})
					})
			}
		}

		// “添加端点”按钮（未在编辑表单时展示）
		if !editing {
			renderSettingsDivider(c, "div-custom-ep-add")
			ui.Row(c.Key("row-custom-ep-add")).
				Padding(Space4, Space5).
				Children(func() {
					addBtn := ui.ButtonBase(c.Key("btn-add-custom-ep")).
						Label("添加端点").
						Disabled(busy).
						Radius(RadiusSM).
						Padding(Space2, Space4).
						Background(ThemeHover(c))
					if addBtn.Hovered() {
						addBtn.Background(ThemeOverlay(c, 0.15))
					}
					if addBtn.Clicked() {
						st.mu.Lock()
						openCustomEndpointForm(st, nil, endpointFormScopeCustom)
						st.mu.Unlock()
						st.invalidate()
					}
					addBtn.Children(func() {
						ui.Text(c, "添加端点").
							FontSize(TypeBody).
							TextColor(ThemeAccent(c))
					})
				})
		} else {
			// 编辑表单展开
			renderSettingsDivider(c, "div-custom-ep-form-start")

			formTitle := "添加自定义端点"
			if editID != "" {
				formTitle = "编辑自定义端点"
			}
			ui.Row(c.Key("row-ep-form-title")).
				Padding(Space4, Space5, Space1, Space5).
				Children(func() {
					ui.Text(c, formTitle).
						FontSize(TypeProse).
						FontWeight(600).
						TextColor(ThemePrimaryText(c))
				})

			// 1. 名称
			renderSettingRow(c, "row-ep-form-name", "名称", func() {
				providerDraftInput(c, st, "input-ep-name", "端点名称", "端点名称", nameDraft, false, func(v string) { st.endpoints.editName = v })
			})

			renderSettingsDivider(c, "div-ep-form-1")

			// 2. BaseURL
			renderSettingRow(c, "row-ep-form-baseurl", "接口地址（BaseURL）", func() {
				providerDraftInput(c, st, "input-ep-baseurl", "端点 BaseURL", "https://host/v1", baseDraft, false, func(v string) { st.endpoints.editBaseURL = v })
			})

			renderSettingsDivider(c, "div-ep-form-2")

			// 3. 默认模型（可选，留空则由端点自动选择）
			renderSettingRow(c, "row-ep-form-model", "默认模型", func() {
				providerDraftInput(c, st, "input-ep-model", "端点默认模型", "留空则按请求里的 model 透传", modelDraft, false, func(v string) { st.endpoints.editModel = v })
			})

			renderSettingsDivider(c, "div-ep-form-3")

			// 4. API Key（编辑已有端点时 Key 框留空并提示“留空则保留已保存的 Key”）
			keyPlaceholder := "API Key"
			if editID != "" {
				keyPlaceholder = "留空则保留已保存的 Key"
			}
			renderSettingRow(c, "row-ep-form-apikey", "密钥（API Key）", func() {
				providerDraftInput(c, st, "input-ep-apikey", "端点 API Key", keyPlaceholder, keyDraft, true, func(v string) { st.endpoints.editAPIKey = v })
			})

			// 测试结果展示
			if testErr != "" {
				renderSettingsDivider(c, "div-ep-form-test-err")
				ui.Row(c.Key("row-ep-form-test-err")).
					Padding(Space3, Space5).
					Children(func() {
						ui.Text(c, testErr).
							FontSize(TypeFootnote).
							TextColor(ThemeDanger(c))
					})
			} else if testResult != nil {
				renderSettingsDivider(c, "div-ep-form-test-res")
				ui.Row(c.Key("row-ep-form-test-res")).
					Padding(Space3, Space5).
					Children(func() {
						if testResult.OK {
							msg := fmt.Sprintf("连接成功 (%dms, %d 个模型)", testResult.Latency.Milliseconds(), len(testResult.Models))
							ui.Text(c, msg).
								FontSize(TypeFootnote).
								TextColor(ThemeSuccess(c))
						} else {
							msg := testResult.Message
							if msg == "" {
								msg = "连接失败"
							}
							ui.Text(c, msg).
								FontSize(TypeFootnote).
								TextColor(ThemeDanger(c))
						}
					})
			}

			// 保存表单错误
			if editErr != "" {
				renderSettingsDivider(c, "div-ep-form-edit-err")
				ui.Row(c.Key("row-ep-form-edit-err")).
					Padding(Space3, Space5).
					Children(func() {
						ui.Text(c, editErr).
							FontSize(TypeFootnote).
							TextColor(ThemeDanger(c))
					})
			}

			renderSettingsDivider(c, "div-ep-form-actions")

			// 操作按钮栏：“测试连接”“保存”“取消”
			ui.Row(c.Key("row-ep-form-buttons")).
				Padding(Space4, Space5).
				Gap(Space4).
				Children(func() {
					// 测试连接
					testBtnText := "测试连接"
					if testing {
						testBtnText = "测试中…"
					}
					testBtn := ui.ButtonBase(c.Key("btn-ep-test")).
						Label(testBtnText).
						Radius(RadiusSM).
						Padding(Space2, Space4).
						Background(ThemeHover(c)).
						Disabled(busy)
					if !testing && testBtn.Hovered() {
						testBtn.Background(ThemeOverlay(c, 0.15))
					}
					if !testing && testBtn.Clicked() {
						testCustomEndpoint(st)
					}
					testBtn.Children(func() {
						ui.Text(c, testBtnText).
							FontSize(TypeBody).
							TextColor(ThemePrimaryText(c))
					})

					// 保存按钮
					saveBtnText := "保存"
					if saving {
						saveBtnText = "保存中…"
					}
					saveBtn := ui.ButtonBase(c.Key("btn-ep-save")).
						Label(saveBtnText).
						Radius(RadiusSM).
						Padding(Space2, Space4).
						Background(ThemeAccent(c)).
						Disabled(busy)
					if !saving && saveBtn.Hovered() {
						saveBtn.Background(ThemeAccent(c).Alpha(0.85))
					}
					if !saving && saveBtn.Clicked() {
						saveCustomEndpoint(st)
					}
					saveBtn.Children(func() {
						ui.Text(c, saveBtnText).
							FontSize(TypeBody).
							TextColor(ThemeOnAccent(c))
					})

					// 取消按钮
					cancelBtn := ui.ButtonBase(c.Key("btn-ep-cancel")).
						Label("取消").
						Radius(RadiusSM).
						Padding(Space2, Space4).
						Background(ThemeHover(c)).
						Disabled(busy)
					if cancelBtn.Hovered() {
						cancelBtn.Background(ThemeOverlay(c, 0.15))
					}
					if cancelBtn.Clicked() {
						st.mu.Lock()
						closeCustomEndpointForm(st)
						st.mu.Unlock()
						st.invalidate()
					}
					cancelBtn.Children(func() {
						ui.Text(c, "取消").
							FontSize(TypeBody).
							TextColor(ThemeSecondaryText(c))
					})
				})
		}
	})
}

// apiProviderListEntries 返回预置和普通自定义供应商的快照。
func apiProviderListEntries(st *SettingsState) []providerListEntry {
	st.mu.Lock()
	presets := append([]ProviderSetting(nil), st.presets.items...)
	endpoints := append([]CustomEndpoint(nil), st.endpoints.endpoints...)
	loadPresets := !st.presets.loaded && !st.presets.loading
	loadEndpoints := !st.endpoints.loaded && !st.endpoints.loading
	presetBusy := st.presets.saving
	endpointBusy := st.endpoints.saving || st.endpoints.testing || st.endpoints.toggling || st.endpoints.deletingID != ""
	st.mu.Unlock()
	if loadPresets {
		loadPresetProviders(st)
	}
	if loadEndpoints {
		loadCustomEndpoints(st)
	}
	status := func(enabled, hasKey bool) string {
		if !enabled {
			return "已停用"
		}
		if !hasKey {
			return "未配置密钥"
		}
		return "已配置"
	}
	entries := make([]providerListEntry, 0, len(presets)+len(endpoints))
	for _, p := range presets {
		if p.Kind != "api" {
			continue
		}
		p := p
		entries = append(entries, providerListEntry{Kind: "preset", ID: p.ID, Name: p.Name, Status: status(p.Enabled, p.HasAPIKey), Enabled: p.Enabled, CanToggle: !presetBusy, Toggle: func(enabled bool) { togglePresetProviderEnabled(st, p, enabled) }})
	}
	for _, ep := range endpoints {
		if ep.Magpie {
			continue
		}
		ep := ep
		entries = append(entries, providerListEntry{Kind: "custom", ID: ep.ID, Name: ep.Name, Status: status(ep.Enabled, ep.HasAPIKey), Enabled: ep.Enabled, CanToggle: !endpointBusy, Toggle: func(enabled bool) { toggleCustomEndpointEnabled(st, ep, enabled) }})
	}
	return entries
}

// providerDraftInput 只把本帧快照交给控件，避免与后台保存协程竞争。
func providerDraftInput(c *ui.Context, st *SettingsState, key, label, placeholder string, value string, password bool, set func(string)) {
	ctl := textFunc(
		func(*SettingsState) (string, bool) { return value, false },
		func(_ *SettingsState, v string) {
			st.mu.Lock()
			set(v)
			st.mu.Unlock()
		},
		placeholder,
	)
	if password {
		ctl = ctl.withSecret()
	}
	ctl.render(c, st, setting{ID: key, Title: label})
}

// providerDefaultPlaceholder 用供应商默认值当占位符；没有默认值时说明可以留空。
func providerDefaultPlaceholder(defaultValue string) string {
	if strings.TrimSpace(defaultValue) == "" {
		return "留空则用供应商默认值"
	}
	return defaultValue
}

func providerSheetButton(c *ui.Context, key, label string, disabled bool, action func()) {
	providerSheetButtonWithColor(c, key, label, disabled, ThemePrimaryText(c), action)
}

func providerSheetButtonWithColor(c *ui.Context, key, label string, disabled bool, textColor ui.Color, action func()) {
	b := ui.ButtonBase(c.Key(key)).Label(label).Disabled(disabled).Radius(RadiusSM).Padding(Space2, Space4).Background(ThemeHover(c))
	if b.Clicked() {
		action()
	}
	b.Children(func() { ui.Text(c, label).FontSize(TypeBody).TextColor(textColor) })
}

func providerSheetPrimaryButton(c *ui.Context, key, label string, disabled bool, action func()) {
	b := ui.ButtonBase(c.Key(key)).Label(label).Disabled(disabled).Radius(RadiusSM).Padding(Space2, Space4).Background(ThemeAccent(c))
	if !disabled && b.Hovered() {
		b.Background(ThemeAccent(c).Alpha(0.85))
	}
	if b.Clicked() {
		action()
	}
	b.Children(func() { ui.Text(c, label).FontSize(TypeBody).TextColor(ThemeOnAccent(c)) })
}

// apiProviderSheetState 是 API 供应商面板一帧的状态快照；草稿字段来自编辑态，避免与后台保存竞争。
type apiProviderSheetState struct {
	provider ProviderSetting
	endpoint CustomEndpoint
	name     string
	base     string
	model    string
	key      string
	busy     bool
	errText  string
	testErr  string
	testing  bool
	confirm  bool
	result   *CustomEndpointTestResult
	credits  ProviderCredits
}

// apiProviderSheetStateFor 取供应商快照并初始化草稿。副作用幂等：每帧调用不会覆盖用户正在编辑的草稿。
func apiProviderSheetStateFor(st *SettingsState, kind, id string) (apiProviderSheetState, bool) {
	var state apiProviderSheetState
	st.mu.Lock()
	found := false
	if kind == "preset" {
		for _, item := range st.presets.items {
			if item.ID == id && item.Kind == "api" {
				state.provider = item
				found = true
				break
			}
		}
		if found && st.presets.editingID != id {
			st.presets.editingID, st.presets.baseURLDraft, st.presets.modelDraft = id, state.provider.BaseURL, state.provider.Model
			st.presets.apiKeyDraft, st.presets.saveErr = "", ""
		}
	} else if kind == "custom" {
		for _, item := range st.endpoints.endpoints {
			if item.ID == id && !item.Magpie {
				state.endpoint = item
				found = true
				break
			}
		}
		if found && (!st.endpoints.editing || st.endpoints.editScope != endpointFormScopeCustom || st.endpoints.editID != id) {
			openCustomEndpointForm(st, &state.endpoint, endpointFormScopeCustom)
		}
	}
	if !found {
		st.mu.Unlock()
		return apiProviderSheetState{}, false
	}
	state.credits = st.providerRows.credits[id]
	state.base, state.model, state.key, state.name = st.presets.baseURLDraft, st.presets.modelDraft, st.presets.apiKeyDraft, ""
	state.busy, state.errText = st.presets.saving, st.presets.saveErr
	if kind == "custom" {
		state.base, state.model, state.key, state.name = st.endpoints.editBaseURL, st.endpoints.editModel, st.endpoints.editAPIKey, st.endpoints.editName
		state.busy = st.endpoints.saving || st.endpoints.testing || st.endpoints.toggling || st.endpoints.deletingID != "" || st.endpoints.personaTestBusy
		state.errText, state.testErr, state.testing = st.endpoints.editErr, st.endpoints.testErr, st.endpoints.testing
		if state.errText == "" {
			state.errText = st.endpoints.err
		}
		state.confirm = st.endpoints.deleteConfirmID == id
		if st.endpoints.testResult != nil {
			r := *st.endpoints.testResult
			state.result = &r
		}
	}
	st.mu.Unlock()
	return state, true
}

// apiProviderSheetSections 返回 API 供应商面板的两节；渲染时重新取快照，保证与底栏一致。
func apiProviderSheetSections(st *SettingsState, kind, id string) []providerSheetSection {
	return []providerSheetSection{
		{
			ID:    "sec-provider-account",
			Title: "账号",
			Render: func(c *ui.Context, st *SettingsState) {
				state, ok := apiProviderSheetStateFor(st, kind, id)
				if !ok {
					return
				}
				renderSettingsSection(c, "sec-provider-account", "", func() {
					if kind == "custom" {
						renderSettingRow(c, "row-ep-form-name", "名称", func() {
							providerDraftInput(c, st, "input-ep-sheet-name", "端点名称", "端点名称", state.name, false, func(v string) { st.endpoints.editName = v })
						})
						renderSettingRow(c, "row-ep-form-baseurl", "接口地址（BaseURL）", func() {
							providerDraftInput(c, st, "input-ep-sheet-baseurl", "端点 BaseURL", "https://host/v1", state.base, false, func(v string) { st.endpoints.editBaseURL = v })
						})
						renderSettingRow(c, "row-ep-form-apikey", "密钥（API Key）", func() {
							providerDraftInput(c, st, "input-ep-sheet-apikey", "端点 API Key", "留空则保留已保存的 Key", state.key, true, func(v string) { st.endpoints.editAPIKey = v })
						})
					} else {
						renderSettingRow(c, "row-preset-form-baseurl", "接口地址（BaseURL）", func() {
							providerDraftInput(c, st, "input-preset-sheet-baseurl", "预设 BaseURL", providerDefaultPlaceholder(state.provider.DefaultBaseURL), state.base, false, func(v string) { st.presets.baseURLDraft = v })
						})
						renderSettingRow(c, "row-preset-form-apikey", "密钥（API Key）", func() {
							providerDraftInput(c, st, "input-preset-sheet-apikey", "预设 API Key", "留空则保留已保存的 Key", state.key, true, func(v string) { st.presets.apiKeyDraft = v })
						})
						renderProviderBalancePanel(c, st, id)
						if state.credits.BreakdownText != "" {
							renderSettingRow(c, "row-provider-balance-breakdown", "明细", func() {
								ui.Text(c, state.credits.BreakdownText).FontSize(TypeBody).TextColor(ThemeSecondaryText(c))
							})
						}
					}
				})
			},
		},
		{
			ID:    "sec-provider-models",
			Title: "模型",
			Render: func(c *ui.Context, st *SettingsState) {
				state, ok := apiProviderSheetStateFor(st, kind, id)
				if !ok {
					return
				}
				renderSettingsSection(c, "sec-provider-models", "", func() {
					if kind == "custom" {
						renderSettingRow(c, "row-ep-form-model", "默认模型", func() {
							providerDraftInput(c, st, "input-ep-sheet-model", "端点默认模型", "默认模型", state.model, false, func(v string) { st.endpoints.editModel = v })
						})
					} else {
						renderSettingRow(c, "row-preset-form-model", "默认模型", func() {
							providerDraftInput(c, st, "input-preset-sheet-model", "预设默认模型", providerDefaultPlaceholder(state.provider.DefaultModel), state.model, false, func(v string) { st.presets.modelDraft = v })
						})
					}
				})
			},
		},
	}
}

func renderAPIProviderSheetFooter(c *ui.Context, st *SettingsState, kind, id string) {
	state, ok := apiProviderSheetStateFor(st, kind, id)
	if !ok {
		return
	}
	ui.Column(c.Key("provider-sheet-status")).
		Shrink(1).
		Children(func() {
			switch {
			case state.errText != "":
				ui.Text(c, state.errText).FontSize(TypeCaption).TextColor(ThemeDanger(c)).SingleLine()
			case state.testErr != "":
				ui.Text(c, state.testErr).FontSize(TypeCaption).TextColor(ThemeDanger(c)).SingleLine()
			case state.result != nil && state.result.OK:
				ui.Row(c.Key("provider-sheet-status-ok")).
					AlignItems(ui.Center).
					Gap(Space2).
					Children(func() {
						ui.Icon(c, IconCheck()).Size(IconSM, IconSM).TextColor(ThemeSuccess(c))
						ui.Text(c, fmt.Sprintf("%d ms · %d 个模型", state.result.Latency.Milliseconds(), len(state.result.Models))).
							FontSize(TypeCaption).
							TextColor(ThemeSecondaryText(c))
					})
			case state.result != nil:
				msg := state.result.Message
				if msg == "" {
					msg = "连接失败"
				}
				ui.Text(c, msg).FontSize(TypeCaption).TextColor(ThemeDanger(c)).SingleLine()
			}
		})
	ui.Row(c.Key("provider-sheet-actions")).
		Grow(1).
		AlignItems(ui.Center).
		Gap(Space4).
		Children(func() {
			if kind == "custom" {
				label := "删除"
				if state.confirm {
					label = "确认删除"
				}
				providerSheetButtonWithColor(c, "provider-sheet-delete", label, state.busy, ThemeDanger(c), func() {
					if state.confirm {
						deleteCustomEndpoint(st, id)
						return
					}
					st.mu.Lock()
					st.endpoints.deleteConfirmID = id
					st.mu.Unlock()
					st.invalidate()
				})
			}
			ui.Box(c.Key("provider-sheet-footer-spacer")).Grow(1)
			providerSheetButton(c, "provider-sheet-cancel", "取消", state.busy, func() {
				st.mu.Lock()
				if kind == "custom" {
					closeCustomEndpointForm(st)
					st.endpoints.deleteConfirmID = ""
				} else {
					st.presets.editingID = ""
					st.presets.apiKeyDraft = ""
				}
				st.mu.Unlock()
				closeSettingsSheet(st)
			})
			if kind == "custom" {
				label := "测试连接"
				if state.testing {
					label = "测试中…"
				}
				providerSheetButton(c, "provider-sheet-test", label, state.busy, func() { testCustomEndpoint(st) })
			}
			label := "保存"
			if state.busy && !state.testing {
				label = "保存中…"
			}
			providerSheetPrimaryButton(c, "provider-sheet-save", label, state.busy, func() {
				if kind == "custom" {
					saveCustomEndpoint(st)
				} else {
					savePresetProvider(st, state.provider, state.key)
				}
			})
		})
}

// renderAPIProviderSheetSections 整块堆叠，仅供测试与调试；生产路径由 providerSheet 外壳按节渲染。
func renderAPIProviderSheetSections(c *ui.Context, st *SettingsState, kind, id string) {
	if _, ok := apiProviderSheetStateFor(st, kind, id); !ok {
		return
	}
	for _, def := range apiProviderSheetSections(st, kind, id) {
		def.Render(c, st)
	}
	renderAPIProviderSheetFooter(c, st, kind, id)
}

func renderProviderAddSheetBody(c *ui.Context, st *SettingsState, cliCandidates []providerListEntry) {
	st.mu.Lock()
	kind := st.presets.addType
	selected := st.presets.addPresetID
	presets := append([]ProviderSetting(nil), st.presets.items...)
	load := !st.presets.loaded && !st.presets.loading
	name, base, model, key := st.endpoints.editName, st.endpoints.editBaseURL, st.endpoints.editModel, st.endpoints.editAPIKey
	presetKey := st.presets.apiKeyDraft
	busy, errText := st.endpoints.saving, st.endpoints.editErr
	if kind == 1 {
		busy, errText = st.presets.saving, st.presets.saveErr
	}
	st.mu.Unlock()
	if load {
		loadPresetProviders(st)
	}
	renderSettingsSection(c, "sec-provider-add-type", "类型", func() {
		renderSettingRow(c, "row-provider-add-type", "类型", func() {
			choiceFunc(
				[]string{"CLI 智能体", "API 预置", "自定义"},
				func(*SettingsState) (int, bool) { return kind, false },
				func(st *SettingsState, index int) {
					st.mu.Lock()
					st.presets.addType = index
					st.presets.addPresetID = ""
					st.presets.apiKeyDraft = ""
					if index == 2 {
						openCustomEndpointForm(st, nil, endpointFormScopeCustom)
					}
					st.mu.Unlock()
					st.invalidate()
				},
			).render(c, st, setting{ID: "provider-add-type", Title: "类型"})
		})
	})
	hasCredentials := kind == 2 || errText != ""
	if kind == 0 {
		for _, entry := range cliCandidates {
			if !entry.Enabled {
				hasCredentials = true
				break
			}
		}
	} else if kind == 1 {
		for _, p := range presets {
			if p.Kind == "api" && !p.HasAPIKey {
				hasCredentials = true
				break
			}
		}
	}
	if !hasCredentials {
		return
	}
	renderSettingsSection(c, "sec-provider-add-credentials", "凭据", func() {
		switch kind {
		case 0:
			for _, entry := range cliCandidates {
				if entry.Enabled {
					continue
				}
				e := entry
				renderSettingRow(c, "row-provider-add-cli-"+e.ID, e.Name, func() {
					providerSheetButton(c, "provider-add-cli-"+e.ID, "启用 "+e.Name, e.Locked || !e.CanToggle || e.Toggle == nil, func() { e.Toggle(true) })
				})
			}
		case 1:
			var chosen *ProviderSetting
			for _, item := range presets {
				if item.Kind != "api" || item.HasAPIKey {
					continue
				}
				p := item
				if p.ID == selected {
					chosen = &p
				}
				renderSettingRow(c, "row-provider-add-preset-"+p.ID, p.Name, func() {
					providerSheetButton(c, "provider-add-preset-"+p.ID, "选择 "+p.Name, busy, func() {
						st.mu.Lock()
						st.presets.addPresetID = p.ID
						st.presets.baseURLDraft = p.BaseURL
						st.presets.modelDraft = p.Model
						st.presets.apiKeyDraft = ""
						st.mu.Unlock()
						st.invalidate()
					})
				})
			}
			if chosen != nil {
				p := *chosen
				renderSettingRow(c, "row-provider-add-apikey", "密钥（API Key）", func() {
					providerDraftInput(c, st, "provider-add-preset-key", "预设 API Key", "API Key", presetKey, true, func(v string) { st.presets.apiKeyDraft = v })
				})
				renderSettingRow(c, "row-provider-add-actions", "", func() {
					providerSheetButton(c, "provider-add-preset-save", "添加", busy || strings.TrimSpace(presetKey) == "", func() { p.Enabled = true; savePresetProvider(st, p, presetKey) })
				})
			}
		case 2:
			renderSettingRow(c, "row-provider-add-name", "名称", func() {
				providerDraftInput(c, st, "provider-add-name", "端点名称", "端点名称", name, false, func(v string) { st.endpoints.editName = v })
			})
			renderSettingRow(c, "row-provider-add-baseurl", "接口地址（BaseURL）", func() {
				providerDraftInput(c, st, "provider-add-baseurl", "端点 BaseURL", "https://api.example.com/v1", base, false, func(v string) { st.endpoints.editBaseURL = v })
			})
			renderSettingRow(c, "row-provider-add-model", "默认模型", func() {
				providerDraftInput(c, st, "provider-add-model", "端点默认模型", "默认模型", model, false, func(v string) { st.endpoints.editModel = v })
			})
			renderSettingRow(c, "row-provider-add-apikey", "密钥（API Key）", func() {
				providerDraftInput(c, st, "provider-add-key", "端点 API Key", "API Key", key, true, func(v string) { st.endpoints.editAPIKey = v })
			})
			renderSettingRow(c, "row-provider-add-actions", "", func() {
				providerSheetButton(c, "provider-add-custom-save", "添加", busy, func() { saveCustomEndpoint(st) })
			})
		}
		if errText != "" {
			ui.Text(c, errText).TextColor(ThemeDanger(c))
		}
	})
}

// presetProvidersState 维护「API 供应商」预设区域的交互状态。
type presetProvidersState struct {
	addType     int
	addPresetID string
	loaded      bool
	loading     bool
	items       []ProviderSetting
	err         string

	// 表单状态
	editingID    string
	baseURLDraft string
	modelDraft   string
	apiKeyDraft  string
	saving       bool
	saveErr      string
}

// presetProviderItems 过滤出 Kind == "api" 的预设供应商，保持后端返回顺序。
func presetProviderItems(list []ProviderSetting) []ProviderSetting {
	items := make([]ProviderSetting, 0, len(list))
	for _, s := range list {
		if s.Kind == "api" {
			items = append(items, s)
		}
	}
	return items
}

// loadPresetProviders 异步加载 API 预设供应商列表。
func loadPresetProviders(st *SettingsState) {
	client := st.customEndpointsClient()
	if client == nil {
		return
	}
	st.mu.Lock()
	if st.presets.loading {
		st.mu.Unlock()
		return
	}
	st.presets.loading = true
	st.presets.err = ""
	st.mu.Unlock()
	st.invalidate()

	go func() {
		list, err := client.ProviderSettings(context.Background())
		st.mu.Lock()
		st.presets.loading = false
		st.presets.loaded = true
		if err != nil {
			st.presets.err = err.Error()
		} else {
			st.presets.err = ""
			st.presets.items = presetProviderItems(list)
		}
		st.mu.Unlock()
		st.invalidate()
	}()
}

// savePresetProvider 异步保存预设供应商的 BaseURL、模型与可选的新 API Key；
// BaseURL 与模型取表单草稿（留空表示用默认），apiKey 为空表示保留已保存的 Key。
func savePresetProvider(st *SettingsState, s ProviderSetting, apiKey string) {
	client := st.customEndpointsClient()
	if client == nil {
		return
	}
	st.mu.Lock()
	if st.presets.saving {
		st.mu.Unlock()
		return
	}
	s.BaseURL = strings.TrimSpace(st.presets.baseURLDraft)
	s.Model = strings.TrimSpace(st.presets.modelDraft)
	apiKey = strings.TrimSpace(apiKey)
	st.presets.saving = true
	st.presets.saveErr = ""
	st.mu.Unlock()
	st.invalidate()

	go func() {
		_, err := client.SaveProviderSetting(context.Background(), s, apiKey)
		if err != nil {
			st.mu.Lock()
			st.presets.saving = false
			st.presets.saveErr = err.Error()
			st.mu.Unlock()
			st.invalidate()
			return
		}

		list, lErr := client.ProviderSettings(context.Background())
		refreshCustomEndpointProviders(st, client)

		st.mu.Lock()
		st.presets.saving = false
		st.presets.editingID = ""
		st.presets.baseURLDraft = ""
		st.presets.modelDraft = ""
		st.presets.apiKeyDraft = ""
		st.presets.saveErr = ""
		if lErr != nil {
			st.presets.err = lErr.Error()
		} else {
			st.presets.err = ""
			st.presets.items = presetProviderItems(list)
			st.presets.loaded = true
		}
		st.mu.Unlock()
		st.refreshModels()
		st.invalidate()
	}()
}

// togglePresetProviderEnabled 异步切换预设供应商启用状态（apiKey 传空表示保留已保存的 Key）。
func togglePresetProviderEnabled(st *SettingsState, s ProviderSetting, enabled bool) {
	client := st.customEndpointsClient()
	if client == nil {
		return
	}
	s.Enabled = enabled
	st.mu.Lock()
	if st.presets.saving {
		st.mu.Unlock()
		return
	}
	st.presets.err = ""
	for i, item := range st.presets.items {
		if item.ID == s.ID {
			st.presets.items[i].Enabled = enabled
			break
		}
	}
	st.mu.Unlock()
	st.invalidate()

	go func() {
		_, err := client.SaveProviderSetting(context.Background(), s, "")
		if err != nil {
			st.mu.Lock()
			st.presets.err = err.Error()
			for i, item := range st.presets.items {
				if item.ID == s.ID {
					st.presets.items[i].Enabled = !enabled
					break
				}
			}
			st.mu.Unlock()
			st.invalidate()
			return
		}

		list, lErr := client.ProviderSettings(context.Background())
		refreshCustomEndpointProviders(st, client)

		st.mu.Lock()
		if lErr != nil {
			st.presets.err = lErr.Error()
		} else {
			st.presets.err = ""
			st.presets.items = presetProviderItems(list)
			st.presets.loaded = true
		}
		st.mu.Unlock()
		st.invalidate()
	}()
}

// renderPresetProviders 渲染「API 供应商」预设区域：每行一个预设供应商，可编辑 BaseURL、模型与 API Key。
func renderPresetProviders(c *ui.Context, st *SettingsState) {
	st.mu.Lock()
	loaded := st.presets.loaded
	loading := st.presets.loading
	items := make([]ProviderSetting, len(st.presets.items))
	copy(items, st.presets.items)
	errText := st.presets.err
	editingID := st.presets.editingID
	saving := st.presets.saving
	saveErr := st.presets.saveErr
	st.mu.Unlock()

	// 首次渲染且未加载时触发加载
	if !loaded && !loading {
		loadPresetProviders(st)
	}

	renderSettingsSection(c, "sec-preset-providers", "API 供应商", func() {
		// 客户端全局错误显示在区块内
		if errText != "" {
			ui.Row(c.Key("preset-providers-err-row")).
				Padding(Space4, Space5).
				Children(func() {
					ui.Text(c, errText).
						FontSize(TypeBody).
						TextColor(ThemeDanger(c))
				})
			renderSettingsDivider(c, "div-preset-providers-err")
		}

		if len(items) == 0 {
			ui.Row(c.Key("preset-providers-empty-row")).
				Padding(Space4, Space5).
				Children(func() {
					text := "暂无预设供应商"
					if loading || !loaded {
						text = "正在加载供应商…"
					}
					ui.Text(c, text).
						FontSize(TypeBody).
						TextColor(ThemeSecondaryText(c))
				})
			return
		}

		for i, item := range items {
			if i > 0 {
				renderSettingsDivider(c, fmt.Sprintf("div-preset-provider-%d", i))
			}
			renderPresetProviderRow(c, st, item, editingID == item.ID, saving, saveErr)
		}
	})
}

// renderProviderAPIKeyRow 渲染 API Key 型 Provider 的行头部：左侧图标 + 名称 + 一行状态摘要，
// 右侧固定列宽的启用开关与操作区（编辑按钮等由 controls 提供），与 CLI 型行保持列对齐。
func renderProviderAPIKeyRow(c *ui.Context, key, id, name string, pEnabled, hasAPIKey, disabled bool, toggle func(enabled bool), controls func()) {
	status := providerStatusSummary(providerStatusInput{Kind: providerKindAPIKey, HasAPIKey: hasAPIKey})
	summaryColor := ThemeSecondaryText(c)
	if !status.Ready {
		summaryColor = ThemeWarning(c)
	}
	ui.Row(c.Key(key)).
		Padding(Space4, Space5).
		Gap(Space5).
		AlignItems(ui.Center).
		Children(func() {
			ui.Row(c.Key(key + "-info")).
				Grow(1).
				MinWidth(160).
				Gap(Space4).
				AlignItems(ui.Center).
				Children(func() {
					ui.Icon(c, ProviderIcon(id)).
						Size(IconMD, IconMD).
						TextColor(ThemeSecondaryText(c))
					ui.Column(c.Key(key + "-texts")).
						Gap(Space1).
						Children(func() {
							ui.Text(c, name).
								FontSize(TypeProse).
								FontWeight(500).
								TextColor(ThemePrimaryText(c)).
								SingleLine()
							ui.Text(c, providerSubtitleText(status)).
								FontSize(TypeFootnote).
								TextColor(summaryColor).
								SingleLine()
						})
				})

			ui.Box(c.Key(key + "-switch-slot")).
				Width(providerSwitchSlotWidth).
				AlignItems(ui.Center).
				Children(func() {
					toggleFunc(
						func(*SettingsState) (bool, bool) { return pEnabled, false },
						func(_ *SettingsState, on bool) { toggle(on) },
					).withDisabled(func(*SettingsState) bool { return disabled }).render(c, nil, setting{ID: key, Title: name})
				})

			ui.Row(c.Key(key + "-actions")).
				AlignItems(ui.Center).
				Gap(Space4).
				Children(controls)
		})
}

// renderCustomEndpointRow 渲染单个自定义端点的行头部：左侧图标 + 名称 + 一行状态摘要
// （与 API Key 型 Provider 同一套文案），右侧固定列宽的启用开关与操作区。
// extra 为调用方追加的状态行（如 Magpie 网关的连通状态）。
func renderCustomEndpointRow(c *ui.Context, key, id, name string, ep CustomEndpoint, disabled bool, toggle func(enabled bool), controls func(), extra ...string) {
	status := providerStatusSummary(providerStatusInput{Kind: providerKindAPIKey, HasAPIKey: ep.HasAPIKey})
	summaryColor := ThemeSecondaryText(c)
	if !status.Ready {
		summaryColor = ThemeWarning(c)
	}
	// 端点地址与模型名单独占一行，便于识别当前指向哪个服务。
	endpointText := ep.BaseURL
	if model := strings.TrimSpace(ep.Model); model != "" {
		endpointText += " · " + model
	}
	ui.Row(c.Key(key)).
		Padding(Space4, Space5).
		Gap(Space5).
		AlignItems(ui.Center).
		Children(func() {
			ui.Row(c.Key(key + "-info")).
				Grow(1).
				MinWidth(160).
				Gap(Space4).
				AlignItems(ui.Center).
				Children(func() {
					ui.Icon(c, ProviderIcon(id)).
						Size(IconMD, IconMD).
						TextColor(ThemeSecondaryText(c))
					ui.Column(c.Key(key + "-texts")).
						Gap(Space1).
						Children(func() {
							ui.Text(c, name).
								FontSize(TypeProse).
								FontWeight(500).
								TextColor(ThemePrimaryText(c)).
								SingleLine()
							ui.Text(c, providerSubtitleText(status)).
								FontSize(TypeFootnote).
								TextColor(summaryColor).
								SingleLine()
							if endpointText != "" {
								ui.Text(c, endpointText).
									FontSize(TypeFootnote).
									TextColor(ThemeSecondaryText(c)).
									SingleLine()
							}
							for _, line := range extra {
								if strings.TrimSpace(line) == "" {
									continue
								}
								ui.Text(c, line).
									FontSize(TypeFootnote).
									TextColor(ThemeSecondaryText(c)).
									SingleLine()
							}
						})
				})

			ui.Box(c.Key(key + "-switch-slot")).
				Width(providerSwitchSlotWidth).
				AlignItems(ui.Center).
				Children(func() {
					toggleFunc(
						func(*SettingsState) (bool, bool) { return ep.Enabled, false },
						func(_ *SettingsState, on bool) { toggle(on) },
					).withDisabled(func(*SettingsState) bool { return disabled }).render(c, nil, setting{ID: key, Title: name})
				})

			ui.Row(c.Key(key + "-actions")).
				AlignItems(ui.Center).
				Gap(Space4).
				Children(controls)
		})
}

// renderPresetProviderRow 渲染单个预设供应商行；editing 为真时在该行下展开编辑表单。
func renderPresetProviderRow(c *ui.Context, st *SettingsState, p ProviderSetting, editing, saving bool, saveErr string) {
	pVal := p
	// API Key 草稿同自定义端点：保存成功的协程会清空它，输入框绑局部变量再写回。
	st.mu.Lock()
	keyDraft := st.presets.apiKeyDraft
	st.mu.Unlock()
	renderProviderAPIKeyRow(c, "row-preset-provider-"+pVal.ID, pVal.ID, pVal.Name, pVal.Enabled, pVal.HasAPIKey, saving,
		func(enabled bool) { togglePresetProviderEnabled(st, pVal, enabled) },
		func() {
			if pVal.ID == "zai" {
				ui.Row(c.Key("provider-tray-quota-zai")).
					AlignItems(ui.Center).
					Gap(Space1).
					Children(func() {
						ui.Text(c, "菜单栏额度").
							FontSize(TypeCaption).
							TextColor(ThemeSecondaryText(c))
						toggleFunc(
							func(*SettingsState) (bool, bool) { return trayQuotaShown(st.cfg, "zai"), false },
							func(st *SettingsState, shown bool) { setTrayQuotaShown(st, "zai", shown) },
						).render(c, st, setting{ID: "tray-quota-zai", Title: "菜单栏额度"})
					})
			}

			// 编辑按钮：展开行内表单并预填当前 BaseURL 与模型
			editBtn := ui.ButtonBase(c.Key("btn-edit-preset-"+pVal.ID)).
				Label("编辑 "+pVal.Name).
				Disabled(saving).
				Radius(RadiusSM).
				Padding(Space1, Space4).
				Background(ThemeHover(c))
			if editBtn.Hovered() {
				editBtn.Background(ThemeOverlay(c, 0.15))
			}
			if editBtn.Clicked() {
				st.mu.Lock()
				st.presets.editingID = pVal.ID
				st.presets.baseURLDraft = pVal.BaseURL
				st.presets.modelDraft = pVal.Model
				st.presets.apiKeyDraft = ""
				st.presets.saveErr = ""
				st.mu.Unlock()
				st.invalidate()
			}
			editBtn.Children(func() {
				ui.Text(c, "编辑").
					FontSize(TypeFootnote).
					TextColor(ThemeAccent(c))
			})
		})

	// 余额：后端支持预付费余额的预设供应商（如 DeepSeek、Z.ai）在行下方展示余额与额度窗口。
	renderProviderBalancePanel(c, st, pVal.ID)

	if !editing {
		return
	}

	renderSettingsDivider(c, "div-preset-provider-form-"+p.ID)

	ui.Row(c.Key("row-preset-form-title")).
		Padding(Space4, Space5, Space1, Space5).
		Children(func() {
			ui.Text(c, "编辑 "+p.Name).
				FontSize(TypeProse).
				FontWeight(600).
				TextColor(ThemePrimaryText(c))
		})

	// 1. BaseURL（占位符为默认值，留空表示用默认）
	renderSettingRow(c, "row-preset-form-baseurl", "接口地址（BaseURL）", func() {
		providerDraftInput(c, st, "input-preset-baseurl", "预设 BaseURL", providerDefaultPlaceholder(p.DefaultBaseURL), st.presets.baseURLDraft, false, func(v string) { st.presets.baseURLDraft = v })
	})

	renderSettingsDivider(c, "div-preset-form-1")

	// 2. 默认模型（占位符为默认值，留空表示用默认）
	renderSettingRow(c, "row-preset-form-model", "默认模型", func() {
		providerDraftInput(c, st, "input-preset-model", "预设默认模型", providerDefaultPlaceholder(p.DefaultModel), st.presets.modelDraft, false, func(v string) { st.presets.modelDraft = v })
	})

	renderSettingsDivider(c, "div-preset-form-2")

	// 3. API Key（留空则保留已保存的 Key）
	renderSettingRow(c, "row-preset-form-apikey", "密钥（API Key）", func() {
		providerDraftInput(c, st, "input-preset-apikey", "预设 API Key", "留空则保留已保存的 Key", keyDraft, true, func(v string) { st.presets.apiKeyDraft = v })
	})

	// 保存错误
	if saveErr != "" {
		renderSettingsDivider(c, "div-preset-form-save-err")
		ui.Row(c.Key("row-preset-form-save-err")).
			Padding(Space3, Space5).
			Children(func() {
				ui.Text(c, saveErr).
					FontSize(TypeFootnote).
					TextColor(ThemeDanger(c))
			})
	}

	renderSettingsDivider(c, "div-preset-form-actions")

	// 操作按钮栏：“保存”“取消”
	ui.Row(c.Key("row-preset-form-buttons")).
		Padding(Space4, Space5).
		Gap(Space4).
		Children(func() {
			// 保存按钮
			saveBtnText := "保存"
			if saving {
				saveBtnText = "保存中…"
			}
			saveBtn := ui.ButtonBase(c.Key("btn-preset-save")).
				Label(saveBtnText).
				Radius(RadiusSM).
				Padding(Space2, Space4).
				Background(ThemeAccent(c)).
				Disabled(saving)
			if !saving && saveBtn.Hovered() {
				saveBtn.Background(ThemeAccent(c).Alpha(0.85))
			}
			if !saving && saveBtn.Clicked() {
				st.mu.Lock()
				apiKey := st.presets.apiKeyDraft
				st.mu.Unlock()
				savePresetProvider(st, p, apiKey)
			}
			saveBtn.Children(func() {
				ui.Text(c, saveBtnText).
					FontSize(TypeBody).
					TextColor(ThemeOnAccent(c))
			})

			// 取消按钮
			cancelBtn := ui.ButtonBase(c.Key("btn-preset-cancel")).
				Label("取消").
				Radius(RadiusSM).
				Padding(Space2, Space4).
				Background(ThemeHover(c)).
				Disabled(saving)
			if cancelBtn.Hovered() {
				cancelBtn.Background(ThemeOverlay(c, 0.15))
			}
			if cancelBtn.Clicked() {
				st.mu.Lock()
				st.presets.editingID = ""
				st.presets.baseURLDraft = ""
				st.presets.modelDraft = ""
				st.presets.apiKeyDraft = ""
				st.presets.saveErr = ""
				st.mu.Unlock()
				st.invalidate()
			}
			cancelBtn.Children(func() {
				ui.Text(c, "取消").
					FontSize(TypeBody).
					TextColor(ThemeSecondaryText(c))
			})
		})
}
