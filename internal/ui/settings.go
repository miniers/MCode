package mcodeui

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mcode/internal/agentconf"
	"mcode/internal/config"
	"mcode/internal/mcp"
)

// 设置页局部尺寸：以下取值不在 tokens.go 的刻度上，单独命名以保持现有布局与对齐。
const (
	settingsFieldTrailingPad float32 = 34 // 搜索框右侧给清除按钮留出的内边距
	settingsTinyIcon         float32 = 11 // 22px 圆形小按钮内的图标
	settingsSwatchIcon       float32 = 18 // 外观预览色块内的图标
	settingsSelectedBorder   float32 = 2  // 外观预览选中态的描边粗细
)

// 设置页局部控件尺寸与面板宽度：一次性布局尺寸，不进全局令牌。
const (
	settingsModelRowHeight     float32 = 58  // 模型行卡片高度
	settingsModelPickerWidth   float32 = 72  // 模型选择按钮宽度
	settingsQRCodeSize         float32 = 200 // 远程配对二维码边长
	settingsPreviewTileW       float32 = 64  // 外观预览色块宽度
	settingsPreviewTileH       float32 = 44  // 外观预览色块高度
	settingsFieldNarrowWidth   float32 = 90  // 天数、默认档等窄输入框宽度
	settingsFieldWidth         float32 = 120 // 自动休眠天数输入框宽度
	settingsFieldMediumWidth   float32 = 160 // 会话过滤输入框宽度
	settingsSidePanelWidth     float32 = 220 // 工作台侧栏宽度
	settingsModelsPanelWidth   float32 = 330 // 模型页自定义接口面板宽度
	settingsEditorPopoverWidth float32 = 360 // Swarm 编辑浮层宽度
	settingsPresetPopoverWidth float32 = 480 // 预设编辑浮层宽度
)

// 设置页浮层高度上限。
const (
	settingsEditorPopoverMaxHeight float32 = 650 // Swarm 编辑浮层可滚动高度
	settingsPresetPopoverMaxHeight float32 = 520 // 预设编辑浮层可滚动高度
)

// settingsRemoteHeadlineFont 是远程访问页配对码标题字号。
const settingsRemoteHeadlineFont float32 = 24

var (
	settingsMu   sync.Mutex
	settingsList = make(map[*chatState]*SettingsState)
)

func getChatSettings(s *chatState) *SettingsState {
	if s == nil {
		return nil
	}
	settingsMu.Lock()
	defer settingsMu.Unlock()
	return settingsList[s]
}

func setChatSettings(s *chatState, st *SettingsState) {
	if s == nil {
		return
	}
	settingsMu.Lock()
	defer settingsMu.Unlock()
	settingsList[s] = st
}

// SettingsState 维护桌面端设置面板的交互状态。
type SettingsState struct {
	updateDotVisible bool
	// modelsPage 在各模型页面节之间共享状态。
	modelsPage *modelsPageState
	// magpieMatrixSearch 是 Magpie 页模型矩阵的搜索词，切换页面时保留。
	magpieMatrixSearch string
	mu                 sync.Mutex
	// cfgMu 单独保护 cfg / base：渲染路径大量无锁直读 st.cfg，save 也可能被后台 goroutine
	// 调用（如 loadProjects → rememberUsedProvider），两边的快照与回写都靠它排序；
	// 不复用 mu，是因为设置页多处「改完就地保存」是在 mu 里调 save 的，重入会自锁。
	// 加锁顺序固定为 mu → cfgMu，不得反过来。
	cfgMu sync.Mutex

	dataDir    string
	lockedKeys map[string]bool
	chat       *chatState

	open        bool
	currentPage string
	navSearch   string
	// pendingPage 是在渲染期请求切到的页。渲染中不能直接切页（selectSettingsPage 要加锁、
	// 补拉目标页数据并请求重绘，中途改页会重入渲染），所以先记在这里，由下一帧的
	// renderSettingsDetail 在渲染前走同一条切页入口执行。
	pendingPage string

	// resetPendingPage 是已点「恢复本页默认」、等待二次确认的页面 id；空串表示没有。
	// 和 advancedOpen 一样只在界面线程读写。
	resetPendingPage string

	// openSheet 是当前打开的面板 id（空表示无），sheetJumpRow 是面板内要跳到的那一行。
	openSheet    string
	sheetJumpRow string
	// sheetSourceRow 是打开面板的来源行 key，关闭面板时把高亮交还给这一行；空串表示不回焦。
	sheetSourceRow string
	// providerAdvancedOpen 记录「高级」折叠区是否展开，providerAdvancedKey 记录它所属的面板
	// （kind + "/" + id）。切换供应商时收起；渲染期读、锁内写。
	providerAdvancedOpen bool
	providerAdvancedKey  string

	// advancedOpen 记录各页底部「高级」折叠区是否展开（键为页 id），默认收起。
	advancedOpen map[string]bool

	// jumpToItemID 是逐项搜索点了某条命中后要滚动到可视区的那一行 key，渲染到该行后立即清空。
	jumpToItemID string
	// highlightItemID 是同上要高亮的那一行 key，从 highlightStartedAt 起保持 settingsItemHighlightDuration。
	highlightItemID    string
	highlightStartedAt time.Time

	cfg config.Config
	// base 是设置页上次与磁盘同步时的配置快照，save 时用它判断本页到底改过哪些字段。
	base            config.Config
	remoteHostsText string
	errorMessage    string
	// configWarning 记录读盘时被 config 逐字段重置的非法值说明：它不是错误，只提示用户
	// 磁盘上有笔误，保存照常进行（E-07）。
	configWarning string
	// launchMode 是 --mode / MCODE_MODE 指定的、当前实际生效的权限档位。
	// Open() 会重新从磁盘 Load，光看配置会把锁定态显示成磁盘上的旧档（E-03）。
	launchMode string

	skills           []agentconf.Skill
	mcpServers       []agentconf.MCPServer
	agentConfLoading bool
	agentConfErr     string
	agentConfLoaded  bool
	// agentConfProjectDir 是技能/MCP 列表扫描时用的项目目录：页面展示的条目与写盘目标都以它为准，
	// 免得切换项目后还在改上一个项目的 .mcode 配置（J-07）。
	agentConfProjectDir string
	// agentConfWriteErr 单独保存技能启停写盘失败的原因（S-113），与扫描错误分开显示。
	agentConfWriteErr string

	// 设置页的多行文本输入走防抖落盘：textSavePending 表示还有改过但未落盘的文本，
	// textSaveDue 是允许落盘的时间点（输入期间只改内存）。
	textSavePending bool
	textSaveDue     time.Time
	// globalShortcutDraft 是全局快捷键输入框的草稿指针（由 cfgMu 保护）：控件只改这里，
	// 改动再在锁内写回 cfg，落盘交给设置页统一的文本防抖（E-06）。
	// 用指针而不是 string，是为了让输入框每帧拿到的地址固定（textInput 按指针回写）。
	globalShortcutDraft *string

	// 快捷键输入框同样先写草稿再落盘：shortcutDirtyIDs 是防抖窗口内改过的快捷键 id，
	// shortcutSaveDue 是这次防抖的到期时间（与文本输入一样由界面线程到期后落盘），
	// shortcutInputFocus 记录上一帧哪些输入框有焦点，用于识别失焦（三项都由 mu 保护）。
	shortcutDirtyIDs   map[string]bool
	shortcutSaveDue    time.Time
	shortcutInputFocus map[string]bool

	mcpAuthBusy map[string]bool
	mcpAuthErr  map[string]string
	// mcpStaticAuth 记录用静态请求头鉴权的远程服务键（agent:scope:name），
	// 这些服务不需要 OAuth 授权，设置页不显示登录按钮。
	mcpStaticAuth map[string]bool

	agy          agyPageState
	remote       remotePageState
	endpoints    customEndpointsPageState
	presets      presetProvidersState
	providerRows providerRowsState
	piMagpie     piMagpieState
	magpieHub    magpieHubState
	// agentMagpie 按 agent id 保存各自的 Magpie 小节状态，claude 与 codex 互不覆盖。
	agentMagpie map[string]*agentMagpieState
	// catalogStale 表示供应商目录刚被作废（pi 的 Magpie 模型勾选变了），
	// 模型库页下次渲染时丢弃自己的供应商快照重新拉取，与其它选择器看到同一份目录。
	catalogStale bool
	// modelCatalogVersion 在手动刷新完成后递增，通知模型页更新目录快照。
	modelCatalogVersion uint64
	modelsRefreshing    bool
	modelsRefreshDone   bool
	modelsRefreshErr    string

	// reviewCheckLoaded 表示当前项目的检查命令已从项目配置读入内存。
	reviewCheckLoaded bool
	// reviewCheckProjectDir 是上次加载检查命令的项目目录，切换项目时据此重载。
	reviewCheckProjectDir string
	// reviewCheckCommand 是本项目检查命令的编辑值，保存后写入 review-checks/<hash>.json。
	reviewCheckCommand string
	// reviewCheckErr 是读写本项目检查命令失败的原因，显示在输入框下方。
	reviewCheckErr string
	// reviewCheckSaved 表示本项目检查命令最近一次保存成功，用于显示已保存提示。
	reviewCheckSaved bool

	// 下面八个字段是各设置页自己的界面状态，替代原来按 *SettingsState 索引的包级 map：
	// 状态跟着面板走，面板关掉后不再由全局 map 长期持有。
	// 读写都在 s.mu 锁内，各页 getter 在锁内懒初始化；Close 时清空。
	mcpPage          *mcpPageUIState
	mcpCatalogPage   *mcpCatalogUIState
	swarmTiersPage   *swarmTiersUIState
	swarmPage        *swarmPairsUIState
	skillsSearch     *string
	openInPage       *openInPageState
	quickRepliesPage *quickRepliesPageState
	nowledgePage     *nowledgeSpacePageState
}

// applyThemeSource 在测试中会替换为空操作，因为 headless 下 SetSource 会阻塞。
var applyThemeSource = func(src mygo.ThemeSource) {
	mygo.Theme.SetSource(src)
}

// NewSettingsState 创建并初始化设置状态。
func NewSettingsState(dataDir string, lockedKeys map[string]bool, chat *chatState) *SettingsState {
	cfg, warnings, err := config.LoadWithWarnings(dataDir)
	loadErr := ""
	if err != nil {
		// 读不出配置时先用默认值把界面撑起来，但把原因留给页面顶部的错误条：
		// 否则用户只看到「设置像没生效」，分不清是没改到还是根本没读出来。
		cfg = config.Defaults()
		loadErr = "读取设置失败，当前显示默认值：" + err.Error()
	}

	st := &SettingsState{
		dataDir:         dataDir,
		lockedKeys:      lockedKeys,
		chat:            chat,
		open:            false,
		currentPage:     "general",
		navSearch:       "",
		cfg:             cfg,
		base:            cloneConfig(cfg),
		remoteHostsText: strings.Join(cfg.Remote.ExtraHosts, ", "),
		errorMessage:    loadErr,
		configWarning:   configWarningsText(warnings),
		mcpAuthBusy:     make(map[string]bool),
		mcpAuthErr:      make(map[string]string),
		mcpStaticAuth:   make(map[string]bool),
	}

	// 草稿单独一份，不指向 cfg，避免输入框不经锁直接写 cfg 字段；
	// 用稳定指针是为了让 textInput 每帧拿到同一个地址。
	shortcutDraft := cfg.Shortcuts.GlobalShortcut
	st.globalShortcutDraft = &shortcutDraft

	// 订阅配置变更：Web 端 settings.set 写完后要让这里跟着刷新，否则不重启看不到新值。
	st.watchConfigChanges(chat)

	return st
}

// watchConfigChanges 订阅客户端的配置变更通知。
// chat.client 是 mcodeui.Client 接口，只有真实的 app.Client 实现了 WatchConfig，
// 测试里的替身没有这个方法，断言失败就跳过（不为了一个订阅去改公共接口）。
func (s *SettingsState) watchConfigChanges(chat *chatState) {
	if s == nil || chat == nil || chat.client == nil {
		return
	}
	watcher, ok := chat.client.(interface {
		WatchConfig(onChange func()) (stop func())
	})
	if !ok {
		return
	}
	// 不注销：SettingsState 的生命周期与 chatState 一致，进程内不会积累订阅。
	watcher.WatchConfig(s.handleConfigChanged)
}

// handleConfigChanged 在配置文件被别处改写后刷新内存态。
func (s *SettingsState) handleConfigChanged() {
	if s == nil {
		return
	}
	// 读配置的短期缓存（运行中排队、插入当前回合）也要一起失效，否则新值最多晚 2 秒才生效。
	chat := s.chat
	if chat != nil {
		chat.invalidateConfigCache()
	}
	s.refreshFromDisk()
}

// refreshFromDisk 重新读盘并刷新设置页的配置快照。
// 有未落盘的文本改动时跳过：那份内存态更新，重读会把用户刚输入的内容盖回去；
// 保存（或下次打开面板）时本来就会重新读一次。
func (s *SettingsState) refreshFromDisk() {
	s.mu.Lock()
	pending := s.textSavePending
	s.mu.Unlock()
	if pending {
		return
	}
	cfg, warnings, err := config.LoadWithWarnings(s.dataDir)
	if err != nil {
		return
	}
	s.cfgMu.Lock()
	s.cfg = cfg
	s.base = cloneConfig(cfg)
	s.remoteHostsText = strings.Join(cfg.Remote.ExtraHosts, ", ")
	// 重读配置后草稿要跟着换，否则输入框还显示上一份配置里的旧快捷键。
	if s.globalShortcutDraft != nil {
		*s.globalShortcutDraft = cfg.Shortcuts.GlobalShortcut
	}
	s.configWarning = configWarningsText(warnings)
	s.cfgMu.Unlock()

	s.invalidate()
}

// WithSettings 注入设置数据目录与只读锁定的启动参数项。
// 没传此 Option 或 dataDir 为空时不展示设置入口。
func WithSettings(dataDir string, lockedKeys map[string]bool) Option {
	return func(s *chatState) {
		if dataDir == "" {
			return
		}
		st := NewSettingsState(dataDir, lockedKeys, s)
		setChatSettings(s, st)
		// 启动即应用持久化的快捷键覆盖（此处是 UI 启动时唯一加载 config.Config 的位置），
		// 避免必须先打开一次设置面板才生效。
		setShortcutOverrides(st.cfg.Shortcuts.ShortcutOverrides)
		// 文字大小同样在这一处进入 UI：没打开过设置页也要按配置生效。
		setTextScale(st.cfg.Appearance.TextScale)
	}
}

// WithLaunchMode 注入由 --mode / MCODE_MODE 指定、当前实际生效的权限档位。
// 设置面板的权限档位被启动参数锁定时用它渲染选中项，否则显示的是磁盘上的旧档（E-03）。
// 必须跟在 WithSettings 之后传入。
func WithLaunchMode(mode string) Option {
	return func(s *chatState) {
		st := getChatSettings(s)
		if st == nil {
			return
		}
		st.mu.Lock()
		st.launchMode = mode
		st.mu.Unlock()
	}
}

// launchPermissionMode 返回启动参数指定的权限档位；未通过启动参数指定时为空。
func (s *SettingsState) launchPermissionMode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.launchMode
}

// configWarningsText 把 LoadWithWarnings 的逐字段说明压成一行提示：
// 只列字段路径（每条说明都以字段路径开头），完整原因由 config 包写进日志。
func configWarningsText(warnings []string) string {
	if len(warnings) == 0 {
		return ""
	}
	const maxFields = 5
	fields := make([]string, 0, len(warnings))
	for _, w := range warnings {
		name := w
		if parts := strings.Fields(w); len(parts) > 0 {
			name = parts[0]
		}
		fields = append(fields, name)
	}
	text := strings.Join(fields, "、")
	if len(fields) > maxFields {
		text = strings.Join(fields[:maxFields], "、") + " 等"
	}
	return fmt.Sprintf("设置文件中有 %d 项非法值已重置为默认：%s", len(warnings), text)
}

// IsOpen 返回当前是否正展开设置面板。
func (s *SettingsState) IsOpen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.open
}

// Open 打开设置面板并重新从磁盘同步最新配置。
func (s *SettingsState) Open() {
	s.mu.Lock()
	s.open = true
	s.endpoints.failedAt = time.Time{}
	s.currentPage = normalizeSettingsPageID(s.currentPage)
	s.navSearch = ""
	s.jumpToItemID = ""
	s.highlightItemID = ""
	cfg, warnings, err := config.LoadWithWarnings(s.dataDir)
	s.cfgMu.Lock()
	if err != nil {
		// 读不出来就保留上次的内存态，并把原因写进错误条，而不是静默沿用旧值。
		s.errorMessage = "读取设置失败，显示的是上次读取到的设置：" + err.Error()
	} else {
		s.cfg = cfg
		s.base = cloneConfig(cfg)
		s.remoteHostsText = strings.Join(cfg.Remote.ExtraHosts, ", ")
		s.errorMessage = ""
		s.configWarning = configWarningsText(warnings)
	}
	s.cfgMu.Unlock()
	if err == nil {
		setShortcutOverrides(cfg.Shortcuts.ShortcutOverrides)
		setTextScale(cfg.Appearance.TextScale)
	}
	s.remote.statusLoaded = false
	// 设备列表也每次都重读：web 端可能已通过 session.renameDevice 改过名称。
	s.remote.devicesLoaded = false
	loadMagpieProviders := s.currentPage == "magpie"
	s.mu.Unlock()

	if loadMagpieProviders {
		s.loadProviderRows()
	}
	s.invalidate()
}

// Close 关闭设置面板。
func (s *SettingsState) Close() {
	// 关面板前把还在防抖窗口里的文本与快捷键改动落盘，否则最后一次输入会丢。
	s.flushTextSave(true)
	s.flushShortcutDrafts(true)

	s.mu.Lock()
	s.open = false
	s.resetPendingPage = ""
	s.mcpPage = nil
	s.mcpCatalogPage = nil
	s.swarmTiersPage = nil
	s.swarmPage = nil
	s.skillsSearch = nil
	s.openInPage = nil
	s.quickRepliesPage = nil
	s.nowledgePage = nil
	s.mu.Unlock()
	closeSettingsSheet(s)

	s.invalidate()
}

func (s *SettingsState) invalidate() {
	s.mu.Lock()
	chat := s.chat
	s.mu.Unlock()
	if chat != nil {
		chat.invalidate()
	}
	invalidateSettingsWindow(s)
}

func (s *SettingsState) isLocked(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lockedKeys != nil && s.lockedKeys[key]
}

// settingsSaveWriters 跟踪正在进行的设置写盘，供测试在删除临时数据目录前等待后台写盘结束。
// save 本身是同步的，但后台 goroutine（如 loadProjects → rememberUsedProvider）也会触发它；
// 测试结束时那次调用可能仍在写 config.json，删目录时会报 "directory not empty"。
var settingsSaveWriters sync.WaitGroup

// settingsSaveHook 仅供测试统计 save 调用次数（默认 nil）。包内测试不并行，直接赋值即可。
var settingsSaveHook func()

// save 校验配置并原子写入磁盘。若校验失败则记录错误并保留内存修改，不写文件。
// 写入时只把本页改过的字段合并进磁盘上的最新配置：供应商页等入口通过 Client 直接写盘，
// 用整份内存快照覆盖会静默回退它们的修改，多窗口各持一份快照时也会互相抹掉。
// 线程约定：cfg / base 只通过 cfgMu 持有或在锁内整体替换；save 的磁盘 IO 不持任何锁，
// 回写只覆盖真正变了的字段。后台 goroutine（如 loadProjects → rememberUsedProvider）也会调
// 这里，整份换掉 cfg 会和渲染路径的无锁读撞车。
func (s *SettingsState) save() bool {
	if settingsSaveHook != nil {
		settingsSaveHook()
	}
	// 让测试能等到这次写盘结束再删临时数据目录。
	settingsSaveWriters.Add(1)
	defer settingsSaveWriters.Done()
	// 先在本 goroutine 里按 cfgMu 拍下深拷贝快照：后续校验与写盘只读这份快照，
	// 不会再碰会被并发修改的 s.cfg / s.base 字段。
	s.cfgMu.Lock()
	cfg := cloneConfig(s.cfg)
	base := cloneConfig(s.base)
	s.cfgMu.Unlock()
	return s.saveSnapshot(cfg, base)
}

// updateAndSave 给后台 goroutine 用：在锁内改配置再落盘。调用方（如记「上次使用的 Agent」）
// 不要自己 st.mu.Lock 后改 s.cfg，统一走这里，免得改动和保存之间被渲染看到半截状态。
// 改 cfg 要同时持有 mu 与 cfgMu（顺序固定 mu → cfgMu）：渲染路径在 mu 下读 cfg，
// save 与 saveSnapshot 在 cfgMu 下读写。因此这里不能先拿 cfgMu 再补 mu，必须按顺序来；
// 调用方也都不能持有 s.mu，否则这里加 mu 会自锁。save 在两把锁之外调。
func (s *SettingsState) updateAndSave(mutate func(cfg *config.Config)) {
	s.mu.Lock()
	s.cfgMu.Lock()
	if mutate != nil {
		mutate(&s.cfg)
	}
	s.cfgMu.Unlock()
	s.mu.Unlock()
	s.save()
}

// saveSnapshot 用锁内拍下的快照完成校验、合并写盘与内存回写。
// 不碰 s.mu：设置页多处是在持 s.mu 的临界区里改完就 save 的，重入会自锁。
func (s *SettingsState) saveSnapshot(cfg, base config.Config) bool {
	if err := config.Validate(cfg); err != nil {
		s.noteError(err.Error())
		return false
	}
	// merged 保存合并后的结果，落盘成功后用它刷新内存快照，界面上就能看到别处写入的最新值。
	var merged config.Config
	if err := config.Update(s.dataDir, func(latest *config.Config) error {
		mergeConfigChanges(reflect.ValueOf(latest).Elem(), reflect.ValueOf(base), reflect.ValueOf(cfg))
		if err := config.Validate(*latest); err != nil {
			return err
		}
		merged = cloneConfig(*latest)
		return nil
	}); err != nil {
		s.noteError(err.Error())
		return false
	}

	// 写盘期间用户可能又改过配置：merged 只覆盖和快照一致（也就是没再动过）的字段，
	// 新改动留在内存里，下次保存再落盘，不会被旧快照盖回去。
	s.cfgMu.Lock()
	next := cloneConfig(merged)
	mergeConfigChanges(reflect.ValueOf(&next).Elem(), reflect.ValueOf(cfg), reflect.ValueOf(s.cfg))
	applyConfigDiff(reflect.ValueOf(&s.cfg).Elem(), reflect.ValueOf(next))
	// base 记的是「磁盘上已确认的值」，所以取 merged 而不是 next：写盘期间的新改动仍是待保存状态。
	s.base = cloneConfig(merged)
	textScale := s.cfg.Appearance.TextScale
	s.noteErrorLocked("")
	s.cfgMu.Unlock()

	// 设置改动后同步进程内的文字大小，侧栏与时间线下一帧即按新档位渲染。
	setTextScale(textScale)
	return true
}

// noteError 记错误条内容；值没变就不写，少给渲染路径制造无意义的写入。
// 错误条与 cfg 共用 cfgMu：后台保存也会走到这里，要和 Open 的写入排同一个序。
func (s *SettingsState) noteError(msg string) {
	s.cfgMu.Lock()
	s.noteErrorLocked(msg)
	s.cfgMu.Unlock()
}

func (s *SettingsState) noteErrorLocked(msg string) {
	if s.errorMessage != msg {
		s.errorMessage = msg
	}
}

// settingsTextSaveDebounce 是设置页文本输入的落盘防抖：输入期间只改内存，停手这么久之后
// 才由界面线程写一次盘，避免每按一个键就在 UI 线程做一次「读文件 → 校验 → fsync → rename」（E-06）。
const settingsTextSaveDebounce = 500 * time.Millisecond

// settingsTextSaveGrace 是重绘定时器相对防抖时间的余量，避免它恰好早于到期时间触发。
const settingsTextSaveGrace = 100 * time.Millisecond

// settingsItemHighlightDuration 是逐项搜索跳转后目标行的高亮时长：两轮 650ms 的描边脉冲。
const settingsItemHighlightDuration = 1300 * time.Millisecond

// settingsNavHitLimit 是导航栏每个页面下最多列出的命中设置项数。
const settingsNavHitLimit = 5

// noteTextEdit 记录一次文本输入：只标记待保存并预约一次重绘，真正的落盘在界面线程的
// flushTextSave 里执行（save 会与后台保存争同一份 cfg 快照，每次都当场跑会把 fsync 挤到输入路径上）。
func (s *SettingsState) noteTextEdit() {
	s.mu.Lock()
	s.textSaveDue = time.Now().Add(settingsTextSaveDebounce)
	s.textSavePending = true
	s.mu.Unlock()

	time.AfterFunc(settingsTextSaveDebounce+settingsTextSaveGrace, s.invalidate)
}

// flushTextSave 在界面线程把到期的文本改动落盘；force 忽略剩余防抖时间（关闭面板前调用）。
func (s *SettingsState) flushTextSave(force bool) bool {
	s.mu.Lock()
	pending, due := s.textSavePending, s.textSaveDue
	if !pending || (!force && time.Now().Before(due)) {
		s.mu.Unlock()
		return true
	}
	s.mu.Unlock()

	if !s.save() {
		return false
	}
	s.mu.Lock()
	if s.textSaveDue == due {
		s.textSavePending = false
	}
	s.mu.Unlock()
	return true
}

func (s *SettingsState) prepareWindowClose() bool {
	if !s.flushTextSave(true) || !s.flushShortcutDrafts(true) {
		s.invalidate()
		return false
	}
	return true
}

// cloneConfig 深拷贝整份配置。Config 里含 slice 与 map，直接赋值会让两份快照共享底层存储，
// 一侧改了另一侧跟着变，「本页有没有改过」就判断不出来了，所以统一走 JSON 往返拷贝。
func cloneConfig(cfg config.Config) config.Config {
	return cloneValue(reflect.ValueOf(cfg)).Interface().(config.Config)
}

// cloneValue 用 JSON 往返深拷贝单个值（基本类型、slice、map、指针都可以）。
// 拷贝失败（类型不支持 JSON）时退回原值，调用方至少不会拿到错误的零值。
func cloneValue(v reflect.Value) reflect.Value {
	data, err := json.Marshal(v.Interface())
	if err != nil {
		return v
	}
	out := reflect.New(v.Type())
	if err := json.Unmarshal(data, out.Interface()); err != nil {
		return v
	}
	return out.Elem()
}

// isMergeLeaf 判断字段是否作为整体比较：基本类型、slice、map、指针、time.Time 都是叶子，
// 只有普通 struct 继续往下一层递归。
func isMergeLeaf(t reflect.Type) bool {
	if t.Kind() != reflect.Struct {
		return true
	}
	return t == reflect.TypeOf(time.Time{})
}

// mergeConfigChanges 把设置页改过的字段三方合并进 latest：
// edited 与 base 不同的字段说明本页改过，取 edited 的值；相同的字段保留 latest，
// 因为别处（供应商页、预设供应商页等）可能刚写过磁盘。
func mergeConfigChanges(latest, base, edited reflect.Value) {
	if latest.Kind() != reflect.Struct || latest.Type() != base.Type() || latest.Type() != edited.Type() {
		// 类型对不上就没法逐字段对齐，整体按「改过就取 edited」处理。
		if !reflect.DeepEqual(edited.Interface(), base.Interface()) && latest.CanSet() {
			latest.Set(cloneValue(edited))
		}
		return
	}

	t := latest.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" {
			// 未导出字段既读不出也写不进，跳过。
			continue
		}
		l, b, e := latest.Field(i), base.Field(i), edited.Field(i)
		if isMergeLeaf(field.Type) {
			if reflect.DeepEqual(e.Interface(), b.Interface()) {
				continue
			}
			// 叶子整体替换，并且用深拷贝，避免 latest 与设置页快照共享 slice/map/指针。
			if l.CanSet() {
				l.Set(cloneValue(e))
			}
			continue
		}
		mergeConfigChanges(l, b, e)
	}
}

// applyConfigDiff 把 next 里与 dst 不同的字段逐个写回 dst。写回只动真正变了的字段：
// save 可能在后台 goroutine 里跑，整份赋值会让渲染路径正在读的 s.cfg 整块换掉。
func applyConfigDiff(dst, next reflect.Value) {
	if dst.Kind() != reflect.Struct || dst.Type() != next.Type() {
		if !reflect.DeepEqual(dst.Interface(), next.Interface()) && dst.CanSet() {
			dst.Set(cloneValue(next))
		}
		return
	}

	t := dst.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" {
			continue
		}
		d, n := dst.Field(i), next.Field(i)
		if isMergeLeaf(field.Type) {
			if !reflect.DeepEqual(d.Interface(), n.Interface()) && d.CanSet() {
				d.Set(cloneValue(n))
			}
			continue
		}
		applyConfigDiff(d, n)
	}
}

func latestUpdateStatus(client any) (AppUpdateStatus, bool) {
	latest, ok := client.(appUpdateLatestClient)
	if !ok {
		return AppUpdateStatus{}, false
	}
	return latest.LatestUpdate(), true
}

type settingsNavDef struct {
	id       string
	title    string
	group    string
	icon     *ui.SVG
	keywords []string
}

// settingsPages 定义设置面板的导航项。
func settingsPages() []settingsNavDef {
	return []settingsNavDef{
		{
			id:    "magpie",
			title: "Magpie",
			icon:  pageMagpie.Icon,
			keywords: []string{
				"magpie", "gateway", "网关", "persona", "人设", "route", "路由", "inject", "注入", "经 Magpie", "直连",
			},
		},
		{
			id:    "general",
			title: "通用",
			group: "应用",
			icon:  IconSettings(),
			keywords: []string{
				"permissions", "notifications", "startup", "tray", "quit", "token", "tokens",
				"activity", "usage", "panel", "limits", "credits", "heatmap", "daily", "weekly",
				"cumulative", "sound", "chime", "icon", "icons", "emoji", "symbol", "symbols",
				"project mark", "权限", "通知", "提示音", "托盘", "启动", "退出",
				"keyboard", "keys", "快捷键", "按键",
			},
		},
		{
			id:    "appearance",
			title: "外观",
			group: "应用",
			icon:  IconPalette(),
			keywords: []string{
				"appearance", "theme", "transparency", "transparent", "opacity", "glass",
				"dark", "light", "accent", "tint", "catppuccin", "dracula", "tokyo", "nord",
				"gruvbox", "solarized", "github", "claude", "codex", "cursor", "matrix",
				"text", "size", "font", "zoom", "wallpaper", "sidebar", "performance", "gpu",
				"主题", "深色", "浅色", "文字大小", "字号",
			},
		},
		{
			id:    "remote",
			title: "远程控制",
			group: "应用",
			icon:  IconSmartphone(),
			keywords: []string{
				"remote", "mobile", "phone", "kanban", "pair", "qr", "qrcode", "lan",
				"device", "devices", "rename", "revoke", "restart", "port", "access",
				"tls", "https", "cert", "certificate", "domain", "cloudflare", "token",
				"远程", "手机", "看板", "移动端", "配对", "二维码", "局域网",
				"设备", "重命名", "撤销", "重启", "端口", "入口", "证书", "域名", "令牌", "安全",
			},
		},
		{
			id:    "models",
			title: "模型与供应商",
			group: "代理",
			icon:  IconSlidersHorizontal(),
			keywords: []string{
				"model", "effort", "reasoning", "fast", "slider", "picker", "providers", "provider",
				"codex", "claude", "cursor", "opencode", "opencode zen", "opencode go", "zen",
				"grok", "deepseek", "meta", "muse", "spark", "zai", "z.ai", "glm",
				"coding plan", "devin", "cognition", "antigravity", "agy", "google", "gemini",
				"copilot", "github", "command code", "commandcode", "cmd", "pi", "omp", "oh-my-pi",
				"binary", "path", "sign in", "login", "api key", "usage", "limits", "limit",
				"plan", "quota", "credits", "balance", "供应商", "模型", "预算", "温度",
			},
		},
		{
			id:    "conversation",
			title: "会话",
			group: "代理",
			icon:  IconMessageSquarePlus(),
			keywords: []string{
				"conversation", "session", "chat", "thread", "reasoning", "thinking",
				"compact", "concise", "concise-replies", "verbose", "comments", "replies",
				"queue", "insert", "finish", "finished", "done", "dormant",
				"confirm", "会话", "对话", "思考", "排队", "压缩", "限额", "完成", "删除",
				"quick", "reply", "replies", "canned", "snippet", "template", "快捷回复", "常用语", "短语",
				"archived", "restore", "归档", "恢复",
			},
		},
		{
			id:    "swarm",
			title: "编排",
			group: "代理",
			icon:  IconSwarm(),
			keywords: []string{
				"swarm", "hydra", "bees", "heads", "subagents", "sub-agents", "agents", "team",
				"orchestrator", "worker", "pair", "pairs", "parallel", "delegate", "queue",
				"steward", "watchdog", "triage", "escalate", "dispatcher", "follow-up",
				"stalled", "pending",
				"规划", "并行", "编排", "主代理", "子代理",
				"默默", "总管", "昵称", "跟进者", "总调度", "待处理", "分诊", "值守",
			},
		},
		{
			id:    "extensions",
			title: "扩展",
			group: "代理",
			icon:  IconSparkles(),
			keywords: []string{
				"skills", "skill.md", "prompts", "rules", "instructions", "custom skills", "workflows", "技能",
				"mcp", "model context protocol", "servers", "tools", "connect", "integrations",
				"github", "playwright", "notion", "linear", "slack", "figma", "supabase", "stripe",
				"resend", "email", "context7", "sentry", "postgres", "browser", "netlify", "deploy", "hosting",
				"nowledge", "nmem", "memory", "working memory", "threads", "sync",
				"记忆", "上下文",
			},
		},
		{id: "nowledge", title: "记忆", group: "代理", icon: pageNowledge.Icon, keywords: pageNowledge.Keywords},
		{
			id:    "workbench",
			title: "工作台",
			group: "工作区",
			icon:  IconLayoutDashboard(),
			keywords: []string{
				"workbench", "kanban", "board", "task", "tasks", "editor", "browser",
				"preview", "console", "context", "knowledge", "inject", "injection",
				"font", "size", "工作台", "看板", "任务",
				"open in", "editors", "apps", "applications", "finder", "vscode",
				"cursor", "zed", "xcode", "iterm", "terminal", "shell", "qspace", "打开方式", "编辑器", "应用",
				"quick", "reply", "replies", "canned", "snippet", "template",
				"快捷回复", "常用语", "短语", "文件树",
				"git", "commit", "pull request", "titles", "text generation", "github", "clone",
				"repository", "repositories", "repo", "sign in", "token", "connect", "account",
				"版本控制", "仓库", "分支",
			},
		},
		{id: "sourceControl", title: "版本控制", group: "工作区", icon: pageSourceControl.Icon, keywords: pageSourceControl.Keywords},
		{id: "about", title: "关于", icon: IconInfo(), keywords: []string{"version", "license", "关于", "版本", "update", "changelog", "更新", "更新日志", "检查更新", "诊断", "数据目录"}},
	}
}

// normalizeSettingsPageID 将旧页面 ID 映射到当前页面。
func normalizeSettingsPageID(id string) string {
	switch id {
	case "providers":
		return "models"
	case "steward":
		return "swarm"
	case "openIn":
		return "workbench"
	case "quickReplies", "archive":
		return "conversation"
	case "shortcuts", "advanced":
		return "general"
	case "skills", "mcp":
		return "extensions"
	}
	return id
}

func settingsLegacySheet(id string) string {
	switch id {
	case "quickReplies":
		return "quick-replies"
	case "archive":
		return "archive"
	case "shortcuts":
		return "shortcuts"
	case "openIn":
		return "open-in"
	case "steward":
		return "steward"
	}
	return ""
}

// OpenPage 打开设置并定位到旧页面对应的面板。
func (st *SettingsState) OpenPage(page string) {
	if st == nil {
		return
	}
	st.Open()
	if page != "" {
		st.selectSettingsPage(page)
		st.openLegacySettingsSheet(page)
	}
}

func (s *SettingsState) openLegacySettingsSheet(page string) {
	sheet := settingsLegacySheet(page)
	if _, ok := settingsTreeSheetOf(sheet); ok {
		openSettingsSheet(s, sheet, "")
	}
}

// requestSettingsPageSwitch 在渲染期请求切页：只记下目标页并请求下一帧重绘，
// 真正的切页由 applyPendingPageSwitch 在下一帧渲染详情区之前执行。
// 渲染中直接调 selectSettingsPage 会重入渲染：它会拿 st.mu、触发目标页的数据懒加载并在
// 渲染途中 invalidate，而当前帧的详情区还在按旧页面继续渲染。
func (s *SettingsState) requestSettingsPageSwitch(id string) {
	target := id
	s.mu.Lock()
	changed := s.pendingPage != target
	s.pendingPage = target
	s.mu.Unlock()
	if changed {
		// 保证确实还有下一帧：只有 navSearch 变化触发的那次重绘时，本帧详情区
		// 已在按旧页渲染，没有新事件就不会再重绘，待切页会一直挂着。
		s.invalidate()
	}
}

// applyPendingPageSwitch 在每帧渲染详情区之前执行渲染期攒下的切页请求，
// 让目标页走 selectSettingsPage，懒加载钩子（如模型页重拉供应商）照常触发。
func (s *SettingsState) applyPendingPageSwitch() {
	s.mu.Lock()
	target := s.pendingPage
	s.pendingPage = ""
	s.mu.Unlock()
	if target == "" {
		return
	}
	s.selectSettingsPage(target)
	s.openLegacySettingsSheet(target)
}

// selectSettingsPage 切到目标页，并按需重置该页的延迟加载状态。直接点页名会清掉逐项跳转与高亮。
func (s *SettingsState) selectSettingsPage(id string) {
	s.mu.Lock()
	loadProviders := s.switchSettingsPageLocked(id)
	s.jumpToItemID = ""
	s.highlightItemID = ""
	s.mu.Unlock()
	s.loadSettingsPageData(loadProviders)
	s.invalidate()
}

// switchSettingsPageLocked 把当前页切到目标页（调用方已持有 s.mu），并按需重置目标页的延迟加载状态。
// 返回 true 表示调用方需要在解锁后重新拉取供应商与模型目录。
func (s *SettingsState) switchSettingsPageLocked(id string) bool {
	target := normalizeSettingsPageID(id)
	if s.currentPage != target {
		s.endpoints.failedAt = time.Time{}
	}
	loadProviders := false
	if s.currentPage != target && target == "remote" {
		s.remote.statusLoaded = false
		s.remote.devicesLoaded = false
	}
	if s.currentPage != target && target == "models" {
		s.endpoints.loaded = false
		loadProviders = true
	}
	if s.currentPage != target && target == "magpie" {
		loadProviders = true
	}
	if s.currentPage != target {
		// 面板属于打开它的页面：换页就收起，免得同一组行同时出现在页面和面板里。
		s.openSheet = ""
		s.sheetJumpRow = ""
	}
	s.currentPage = target
	// 任何显式切页都作废渲染期攒下的请求，免得下一帧的 applyPendingPageSwitch
	// 把用户刚选的页又改回导航搜索算出的目标页。
	s.pendingPage = ""
	return loadProviders
}

// loadSettingsPageData 在切页后补拉该页依赖的数据；必须在释放 s.mu 后调用。
func (s *SettingsState) loadSettingsPageData(loadProviders bool) {
	if !loadProviders {
		return
	}
	s.loadAgy(true)
	s.loadProviderRows()
}

// settingsAdvancedOpen 返回某页底部「高级」区是否展开，默认收起。
func (s *SettingsState) settingsAdvancedOpen(page string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.advancedOpen[normalizeSettingsPageID(page)]
}

// setSettingsAdvancedOpen 记录某页底部「高级」区的展开状态。
func (s *SettingsState) setSettingsAdvancedOpen(page string, open bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setSettingsAdvancedOpenLocked(normalizeSettingsPageID(page), open)
}

// setSettingsAdvancedOpenLocked 与 setSettingsAdvancedOpen 等价，调用方已持有 s.mu。
func (s *SettingsState) setSettingsAdvancedOpenLocked(page string, open bool) {
	if s.advancedOpen == nil {
		s.advancedOpen = map[string]bool{}
	}
	if open {
		s.advancedOpen[page] = true
		return
	}
	delete(s.advancedOpen, page)
}

// focusSettingsItem 处理逐项搜索里点开一条命中：切到它所在的页，把该行记为待滚动与待高亮，
// 并预约一次重绘把高亮过期清掉。
func (s *SettingsState) focusSettingsItem(hit settingsItemHit) {
	id := hit.Item.ID
	if strings.HasPrefix(id, "row-mcp-newthread-") {
		key := strings.TrimPrefix(id, "row-mcp-newthread-")
		s.mu.Lock()
		servers := append([]agentconf.MCPServer(nil), s.mcpServers...)
		s.mu.Unlock()
		for _, server := range servers {
			serverKey := mcp.EndpointKey(server.Agent, server.Scope, server.Name)
			if !server.ReadOnly && (key == serverKey || strings.HasPrefix(key, serverKey+"-")) {
				uiSt := getMCPUIState(s)
				uiSt.mu.Lock()
				uiSt.selectedServer = serverKey
				uiSt.expandedServers[serverKey] = true
				uiSt.isFormOpen = false
				uiSt.mu.Unlock()
				hit.Item = settingsItem{ID: "row-mcp-newthread-" + serverKey + "-0", Page: "extensions", Sheet: "mcp-detail"}
				goto resolved
			}
		}
		hit.Item = settingsItem{ID: "block-mcp-custom", Page: "extensions"}
	} else {
		switch id {
		case "mcp.catalog", "block-mcp-catalog":
			uiSt := getMCPUIState(s)
			uiSt.mu.Lock()
			uiSt.addTab = 0
			uiSt.isFormOpen = true
			uiSt.editingServer = ""
			uiSt.editingScope = ""
			uiSt.mu.Unlock()
			hit.Item = settingsItem{Page: "extensions", Sheet: "mcp-add"}
			goto resolved
		case "mcp.fold":
			hit.Item.ID = "row-mcp-fold-tools"
		case "mcp.claude", "mcp.codex", "mcp.agy", "mcp.pi", "mcp.newThread", "block-mcp-claude", "block-mcp-codex", "block-mcp-agy", "block-mcp-pi", "block-mcp-newthread", "block-mcp-fold":
			hit.Item.ID = "block-mcp-custom"
		}
	}
	if treeItem, ok := settingsTreeItemOf(hit.Item.ID); ok {
		hit.Item = settingsTreeItemToItem(treeItem)
	} else {
		for _, treeItem := range settingsTreeItems() {
			item := settingsTreeItemToItem(treeItem)
			if item.Dynamic && strings.HasPrefix(hit.Item.ID, item.ID) {
				item.ID = hit.Item.ID
				hit.Item = item
				break
			}
		}
	}
resolved:
	if !settingsItemVisible(s, hit.Item) {
		return
	}
	s.mu.Lock()
	loadProviders := s.switchSettingsPageLocked(hit.Item.Page)
	s.jumpToItemID = hit.Item.ID
	s.highlightItemID = hit.Item.ID
	s.highlightStartedAt = time.Now()
	// 命中项被收在页底高级区里时先展开，否则跳过去也看不到那一行。
	if hit.Item.Advanced {
		s.setSettingsAdvancedOpenLocked(normalizeSettingsPageID(hit.Item.Page), true)
	}
	providerID := settingsItemProviderSheet(hit.Item.ID)
	if providerID != "" {
		s.providerRows.currentProvider = providerID
		s.providerRows.currentProviderKind = "cli"
		if providerID == "agy" {
			s.agy.dispatchOpen = true
		}
		// 命中高级节时先展开，避免目标行尚未渲染就消费滚动标记。
		switch providerSheetSectionForRow("cli", providerID, hit.Item.ID) {
		case "sec-provider-cli", "sec-agy-routing", "sec-agy-advanced-login":
			setProviderSheetAdvancedOpenLocked(s, "cli", providerID, true)
		}
	}
	s.mu.Unlock()
	s.loadSettingsPageData(loadProviders)
	// 命中项在面板里时先打开面板，滚动与高亮才能落在面板的行上。
	if providerID != "" {
		openSettingsSheet(s, "provider", hit.Item.ID)
	} else {
		s.focusSettingsItemSheet(hit.Item)
	}
	if providerID == "agy" && !loadProviders {
		s.loadAgy(true)
	}
	// 同一行再次命中也要重播描边脉冲：换一个新 key 让 Keyframes 从 0 重新开始。
	settingsRowFocus.pulseSeq++
	time.AfterFunc(settingsItemHighlightDuration, s.invalidate)
	s.invalidate()
}

func (s *SettingsState) focusSettingsItemSheet(item settingsItem) {
	sheet := item.Sheet
	if sheet == "" {
		if treeItem, ok := settingsTreeItemOf(item.ID); ok {
			sheet = treeItem.Sheet
		}
	}
	if sheet != "" {
		openSettingsSheet(s, sheet, item.ID)
	}
}

// syncSettingsRowFocus 把逐项搜索的定位与高亮状态写进括号级的 settingsRowFocus，
// 供行渲染函数读取；高亮超过 settingsItemHighlightDuration 后在这里清掉。
func (s *SettingsState) syncSettingsRowFocus() {
	s.mu.Lock()
	highlightID := s.highlightItemID
	if highlightID != "" && !s.highlightStartedAt.IsZero() && time.Since(s.highlightStartedAt) >= settingsItemHighlightDuration {
		s.highlightItemID = ""
		highlightID = ""
	}
	jumpID := s.jumpToItemID
	s.mu.Unlock()

	settingsRowFocus.jumpID = jumpID
	// 高亮项变了才换脉冲序号：同一项再次命中要重播，停留在同一项时保持当前进度。
	if settingsRowFocus.highlightID != highlightID {
		settingsRowFocus.pulseSeq++
	}
	settingsRowFocus.highlightID = highlightID
	// 行渲染器完成一次性滚动后回调，把待跳转标记清掉，避免每帧都把用户拉回那一行。
	settingsRowFocus.onJump = func() {
		s.mu.Lock()
		s.jumpToItemID = ""
		s.mu.Unlock()
	}
}

// renderSettingsNavHits 在页名下方缩进列出命中项（最多 settingsNavHitLimit 条），
// 标题后附小号灰字的分节名，超出部分给一行「还有 N 项」。
func renderSettingsNavHits(c *ui.Context, st *SettingsState, hits []settingsItemHit) {
	if len(hits) == 0 {
		return
	}
	shown := hits
	if len(shown) > settingsNavHitLimit {
		shown = shown[:settingsNavHitLimit]
	}
	for _, hit := range shown {
		hitRow := ui.Row(c.Key("settings-nav-hit-"+hit.Item.ID)).
			Height(ControlSM).
			Padding(0, Space3).
			Margin(0, 0, 0, 0).
			Radius(RowCornerRadius).
			Gap(Space3).
			AlignItems(ui.Center).
			Label("settings-nav-hit-" + hit.Item.ID)

		if hitRow.Hovered() {
			hitRow.Background(ThemeHover(c))
		}
		if hitRow.Clicked() {
			st.focusSettingsItem(hit)
		}

		hitRow.Children(func() {
			ui.Box(c.Key("settings-nav-hit-indent-" + hit.Item.ID)).Width(Space6)
			ui.Text(c, hit.Item.Title).
				FontSize(TypeFootnote).
				SingleLine().
				TextColor(ThemeSecondaryText(c)).
				Grow(1)
			if hit.Item.Section != "" {
				ui.Text(c, hit.Item.Section).
					FontSize(TypeCaption).
					SingleLine().
					TextColor(ThemeTertiaryText(c))
			}
		})
	}
	if more := len(hits) - len(shown); more > 0 {
		ui.Text(c, fmt.Sprintf("还有 %d 项", more)).
			FontSize(TypeCaption).
			TextColor(ThemeSecondaryText(c)).
			Padding(Space1, settingsFieldTrailingPad, Space2, Space3)
	}
}

// renderSettingsNav 渲染设置面板左侧搜索与导航。
func renderSettingsNav(c *ui.Context, st *SettingsState) {
	pages := settingsPages()

	q := strings.TrimSpace(strings.ToLower(st.navSearch))

	// 逐项命中：查询不足 2 个字符时只按页名过滤（matchSettingsItems 自身也会拦一道）。
	// 本地记忆后端下部分行不渲染，搜索结果也要滤掉，否则点进去没有对应行。
	localBackend := settingsMemoryBackend(st) == config.MemoryBackendLocal
	hitsByPage := map[string][]settingsItemHit{}
	items := settingsItems()
	for _, item := range items {
		if item.ID != "row-openin-" || !item.Dynamic {
			continue
		}
		for _, app := range st.openInAppsDraft() {
			row := item
			row.ID += app.Name
			row.Title = app.Name
			row.Dynamic = false
			items = append(items, row)
		}
	}
	for _, h := range matchVisibleSettingsItemsIn(st, st.navSearch, items) {
		if localBackend && h.Item.NmemOnly {
			continue
		}
		hitsByPage[h.Item.Page] = append(hitsByPage[h.Item.Page], h)
	}

	var visible []settingsNavDef
	for _, p := range pages {
		matched := q == "" || strings.Contains(strings.ToLower(p.title), q) || strings.Contains(strings.ToLower(p.id), q)
		if !matched {
			for _, kw := range p.keywords {
				if strings.Contains(strings.ToLower(kw), q) {
					matched = true
					break
				}
			}
		}
		// 页级命中与条目命中只要有一方成立，该页就显示。
		if !matched && len(hitsByPage[p.id]) > 0 {
			matched = true
		}
		if matched {
			visible = append(visible, p)
		}
	}

	hasCurrent := false
	for _, p := range visible {
		if p.id == st.currentPage {
			hasCurrent = true
			break
		}
	}
	if !hasCurrent && len(visible) > 0 {
		// 搜索把当前页滤掉时自动切到第一个可见页。渲染期不能直接改 currentPage：
		// 那会绕过 selectSettingsPage，目标页的懒加载（供应商目录、远程设备等）永远不触发；
		// 而在渲染中直接调 selectSettingsPage 又会重入（加锁、懒加载并 invalidate）。
		// 所以这里只登记目标页，由 applyPendingPageSwitch 在下一帧渲染详情区之前执行。
		// 置顶的 Magpie 入口是账号入口，不是列表页：优先切到第一个可见的普通页，
		// 只有 Magpie 是唯一可见项时才切过去。
		target := visible[0].id
		for _, p := range visible {
			if p.id != "magpie" {
				target = p.id
				break
			}
		}
		st.requestSettingsPageSwitch(target)
	}

	ui.Column(c.Key("settings-nav-col")).Fill().Background(ThemeSidebarBg(c)).Children(func() {
		ui.Row(c.Key("settings-nav-drag-handle")).Height(Space5).Shrink(0).DragWindow()
		// 按页面名和设置项过滤导航。
		ui.Row(c.Key("settings-nav-search-wrap")).
			Padding(0, ListInset, Space4, ListInset).
			Children(func() {
				SearchField(c, TextFieldProps{
					Key:         "settings-nav-search-input",
					Value:       &st.navSearch,
					Label:       "搜索设置",
					Placeholder: "搜索",
					Grow:        true,
					OnChange: func(string) {
						st.invalidate()
					},
				}).Label("搜索设置外框")
			})

		// 导航列表纵向滚动，命中项在页名下缩进列出。
		ui.Scroll(c.Key("settings-nav-scroll")).Grow(1).Padding(settingsVerticalPadding, ListInset).Children(func() {
			previousGroup := ""
			for i, item := range visible {
				if item.group != previousGroup || (item.id == "about" && i > 0) {
					if i > 0 {
						ui.Box(c.Key("settings-nav-" + "group-gap-" + item.id)).Height(Space5)
					}
					if item.group != "" {
						ui.Row(c.Key("settings-nav-group-" + item.group)).Height(ControlSM).AlignItems(ui.Center).PaddingX(Space3).Children(func() {
							ui.Text(c, item.group).FontSize(TypeCaption).FontWeight(500).TextColor(ThemeTertiaryText(c)).SingleLine()
						})
					}
				}
				previousGroup = item.group
				renderSettingsNavItem(c, st, item, hitsByPage[item.id])
			}
			if len(visible) == 0 {
				ui.Text(c, "无匹配结果").FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c)).Padding(Space4, Space3)
			}
		})
		if st.chat != nil {
			Button(c, ButtonOpts{Key: "settings-onboarding", Text: "新用户引导", Variant: ButtonGhost, OnClick: func() {
				if closeSettingsWindowForOwner(st.chat) {
					openOnboarding(st.chat)
				}
			}})
		}
	})
}

const settingsNavTitleFontSize = TypeBody

// renderSettingsNavItem 渲染一个导航项：图标、名称与选中底色，命中项缩进列在页名下。
func renderSettingsNavItem(c *ui.Context, st *SettingsState, item settingsNavDef, hits []settingsItemHit) {
	isSelected := st.currentPage == item.id
	// 行高 RowHeight 加上下各 1 的外边距：10 项加三处组间空隙约 400pt，700 高的窗口里最后一项仍完整可见可点。
	row := ui.Row(c.Key("settings-nav-item-"+item.id)).
		Height(RowHeight).
		Padding(0, Space3).
		Margin(Hairline, 0, Hairline, 0).
		Radius(RowCornerRadius).
		Gap(Space3).
		AlignItems(ui.Center).
		Label(item.title)

	hoverTarget, selTarget := float32(0), float32(0)
	if isSelected {
		selTarget = 1
	} else if row.Hovered() {
		hoverTarget = 1
	}
	hover := row.AnimateTo("settings-nav-hover", hoverTarget, ui.Animation{Duration: animateDuration(c, DurSnap), Ease: EaseSnap})
	sel := row.AnimateTo("settings-nav-sel", selTarget, ui.Animation{Duration: animateDuration(c, DurFast), Ease: EaseSnap})
	if isSelected {
		hover = 0
	}
	if hover > 0 || sel > 0 {
		bg := ThemeHover(c)
		bg.A = uint8(float32(bg.A)*hover + 0.5)
		bg = bg.Mix(ThemeSelected(c), sel)
		row.Background(bg)
	}

	if row.Clicked() {
		st.selectSettingsPage(item.id)
	}

	row.Children(func() {
		iconColor := ThemeTertiaryText(c)
		if isSelected {
			iconColor = ThemeAccent(c)
		}
		ui.Icon(c, item.icon).Size(IconMD, IconMD).TextColor(iconColor)

		title := item.title
		connected := false
		if item.id == "magpie" {
			st.mu.Lock()
			loadFailed := st.endpoints.loaded && st.endpoints.err != ""
			st.mu.Unlock()
			if !loadFailed {
				title, connected = magpieNavStatus(st)
			}
		}
		tElem := ui.Text(c, title).
			FontSize(settingsNavTitleFontSize).
			SingleLine().
			Grow(1)
		if isSelected {
			tElem.FontWeight(500).TextColor(ThemePrimaryText(c))
		} else {
			tElem.TextColor(ThemeSecondaryText(c))
		}
		if connected {
			ui.Box(c.Key("settings-nav-magpie-dot")).Size(Space2, Space2).Radius(Space2 / 2).Background(ThemeSuccess(c)).Label("已连接").Shrink(0)
		}
		if item.id == "about" {
			available := updateAvailable(archiveClient(st))
			if available {
				renderUpdateDot(c, "settings-nav-update-dot", !st.updateDotVisible)
			}
			st.updateDotVisible = available
		}
	})

	// 页名下缩进列出命中项（最多 5 条），标题后附小号灰字的分节名。
	renderSettingsNavHits(c, st, hits)
}

// renderSettingsDetail 渲染设置面板右侧详情展示区，内容列最大宽度 720、窗口更窄时自适应。
func renderSettingsDetail(c *ui.Context, st *SettingsState) {
	// 文本输入的防抖落盘必须在界面线程做，这里每帧检查一次到期时间。
	st.flushTextSave(false)
	// 快捷键输入同理：定时器只负责叫醒界面线程，真正的落盘在这里。
	st.flushShortcutDrafts(false)

	// 导航搜索把当前页过滤掉时，上一帧的导航会请求切到第一个可见页；渲染期不能直接切，
	// 在这里先执行掉，后面的标题与页面内容都按新页渲染，懒加载也走同一个入口。
	st.applyPendingPageSwitch()
	syncAppearanceTheme(st)

	// 每帧渲染详情区之前把逐项搜索的定位与高亮状态交给行渲染器；高亮过期就地清掉。
	st.syncSettingsRowFocus()

	// 技能/MCP 列表与扫描时的项目目录绑定，切项目后重扫，避免写盘落到别的项目（J-07）。
	if st.currentPage == "extensions" {
		st.ensureAgentConfCurrent()
	}

	ui.Scroll(c.Key("settings-detail-scroll")).Fill().Background(ThemeWindowBg(c)).Children(func() {
		ui.Row(c.Key("settings-detail-outer")).FillWidth().Justify(ui.Center).Padding(0, Space7).Children(func() {
			ui.Column(c.Key("settings-detail-container")).
				Label("设置详情列").
				MaxWidth(settingsContentMaxWidth).
				FillWidth().
				Padding(Space5, Space6, Space8, Space6).
				Gap(settingsSectionGap).
				Children(func() {
					title := "通用"
					for _, p := range settingsPages() {
						if p.id == normalizeSettingsPageID(st.currentPage) {
							title = p.title
							break
						}
					}

					// 顶部标题及支持刷新的页面操作。
					ui.Row(c.Key("settings-detail-title-row")).
						AlignItems(ui.Center).
						Children(func() {
							ui.Text(c, title).
								FontSize(TypePageTitle).
								FontWeight(500).
								TextColor(ThemePrimaryText(c)).
								Grow(1)

							if st.currentPage == "extensions" || st.currentPage == "models" {
								st.mu.Lock()
								refreshing := st.currentPage == "models" && st.modelsRefreshing
								st.mu.Unlock()
								refreshLabel := "刷新"
								if refreshing {
									refreshLabel = "刷新中…"
								}
								refreshBtn := ui.ButtonBase(c.Key("settings-refresh-btn")).
									Label(refreshLabel).
									Disabled(refreshing).
									Radius(RadiusSM).
									Padding(Space1, Space3)
								if refreshBtn.Hovered() {
									refreshBtn.Background(ThemeHover(c))
								}
								if refreshBtn.Clicked() {
									if st.currentPage == "models" {
										// 合并页同时刷新模型库与供应商目录。
										st.refreshModels()
										st.loadAgy(true)
										st.loadProviderRows()
										loadCustomEndpoints(st)
										st.loadPiMagpieEndpoints()
									} else {
										st.loadAgentConf(st.currentProjectDir())
									}
								}
								refreshBtn.Children(func() {
									ui.Text(c, refreshLabel).
										FontSize(TypeBody).
										TextColor(ThemeAccent(c))
								})
							}
							closeBtn := ui.ButtonBase(c.Key("settings-dialog-close")).
								Label("关闭设置").Tooltip("关闭设置").Radius(RadiusSM).Padding(Space1, Space2).Shrink(0)
							if closeBtn.Hovered() {
								closeBtn.Background(ThemeHover(c))
							}
							if closeBtn.Clicked() {
								closeSettingsWindowForOwner(st.chat)
							}
							closeBtn.Children(func() {
								ui.Icon(c, IconX()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
							})
						})

					if st.errorMessage != "" {
						ui.Row(c.Key("settings-error-banner")).
							Padding(Space3, Space4).
							Radius(RadiusMD).
							Background(ThemeDanger(c).Alpha(0.15)).
							Children(func() {
								ui.Text(c, st.errorMessage).
									TextColor(ThemeDanger(c)).
									FontSize(TypeBody)
							})
					}

					// 非法值已被重置，不是错误：只在错误条下面补一条黄色提示，保存照常进行（E-07）。
					if st.configWarning != "" {
						ui.Row(c.Key("settings-warning-banner")).
							Padding(Space3, Space4).
							Radius(RadiusMD).
							Background(ThemeWarning(c).Alpha(0.15)).
							Children(func() {
								ui.Text(c, st.configWarning).
									TextColor(ThemeWarning(c)).
									FontSize(TypeBody)
							})
					}

					// 页面树里的页面由声明式渲染器画，重置也按行派生；其余页面走原有实现。
					page := normalizeSettingsPageID(st.currentPage)
					if treePage, ok := settingsTreePageOf(page); ok {
						renderSettingsTreePage(c, st, treePage)
						renderSettingsResetArea(c, st, treePage.ID)
					}
				})
		})
	})

}

const settingsVerticalPadding = 0

// renderSettingsWindow 绘制设置窗口及其子弹窗。
func renderSettingsWindow(c *ui.Context, st *SettingsState) {
	if st == nil || !st.IsOpen() {
		return
	}
	openSheet, _ := settingsTreeSheetState(st)
	if openSheet == "" && st.resetPendingPage == "" && c.Shortcut(0, ui.KeyEscape) {
		closeSettingsWindowForOwner(st.chat)
	}
	width, _ := c.Size()
	ui.Column(c.Key("settings-window-root")).Label("设置").Fill().Background(ThemeWindowBg(c)).Children(func() {
		ui.Row(c.Key("settings-window-titlebar")).FillWidth().Height(HeaderHeight).DragWindow().
			Padding(settingsVerticalPadding, titleBarTrailingInset(c), settingsVerticalPadding, titleBarLeadingInset(c))
		ui.Row(c.Key("settings-dialog-body")).FillWidth().Grow(1).MinHeight(0).Children(func() {
			ui.Column(c.Key("settings-dialog-nav")).Width(min(float32(220), width*0.35)).FillHeight().Children(func() {
				renderSettingsNav(c, st)
			})
			ui.Column(c.Key("settings-dialog-detail")).Grow(1).MinWidth(0).FillHeight().Children(func() {
				renderSettingsDetail(c, st)
			})
		})
	})

	if openSheet, _ := settingsTreeSheetState(st); openSheet != "" {
		if sheet, ok := settingsTreeSheetOf(openSheet); ok {
			renderSettingsTreeSheet(c, st, sheet)
		}
	}

	renderSettingsResetConfirm(c, st)

	// 跳转目标行如果本帧没被任何行消费（条件行、模板行未渲染），清掉它，
	// 否则下一帧还会把用户拉回同一个位置。
	if settingsRowFocus.jumpID != "" {
		settingsRowFocus.jumpID = ""
		st.mu.Lock()
		st.jumpToItemID = ""
		st.mu.Unlock()
	}
}
