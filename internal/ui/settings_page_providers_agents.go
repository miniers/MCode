package mcodeui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mcode/internal/fsutil"
	"mcode/internal/providers/agy/accounts"
)

type providerListEntry struct {
	Kind, ID, Name, Status     string
	Enabled, Locked, CanToggle bool
	Toggle                     func(enabled bool)
}

type providerRowsState struct {
	currentProvider     string
	currentProviderKind string
	loaded              bool
	loading             bool
	recheckOnFocus      bool
	focusWindow         *mygo.Window
	available           map[string]bool
	versions            map[string]ProviderVersionInfo
	login               map[string]string // Provider 登录态，取值见 ProviderLoginState*（目前只有 claude 有值）
	versionErr          map[string]string
	updateBusy          map[string]bool
	updateMsg           map[string]string
	quota               map[string][]ProviderQuotaWindow
	// quotaErr 是最近一次额度刷新的错误（按 provider）；失败时 quota 保留上次结果。
	quotaErr map[string]string
	account  map[string]ProviderAccountInfo
	// loadErr 是最近一次刷新失败的原因（供应商列表或供应商设置读取失败）：
	// 失败时保留上一次的结果，并在区块顶部提示，不能让整页变成「未安装」。
	loadErr string

	// CLI harness 行状态
	settings map[string]ProviderSetting // Kind=="cli" 的 Provider 设置
	cliIDs   []string                   // CLI harness ID，按 ProviderSettings 返回顺序
	credits  map[string]ProviderCredits // 预付费余额，仅记录后端支持（无错误）的 Provider
	binDraft map[string]string          // 「可执行文件路径」输入框草稿
	saveErr  map[string]string          // 最近一次保存失败信息
	saving   map[string]bool            // 保存中（禁用该行控件）
	savedAt  map[string]time.Time
	// visionDraft 是 pi 「视觉模型」输入框草稿（其他 Provider 不用）。
	visionDraft map[string]string

	// showUninstalled 表示「未安装」分组是否展开；默认折叠成一行，搜索时强制展开。
	showUninstalled bool
}

// providerLoginStateReader 是 Provider 登录态的可选能力：Client 接口不强制要求，
// 实现方（*app.Client）提供后 claude 行才展示登录态；未实现时行摘要与现状一致。
// 方法会起子进程（claude auth status --json），只能在后台 goroutine 里调用。
type providerLoginStateReader interface {
	ProviderLoginState(ctx context.Context, provider string) string
}

// Provider 登录态的取值；CLI 型 Provider 已安装时据此在摘要里追加「已登录」/「未登录」。
// 由 internal/app 的 claudeLoginState 产生，经 providerLoginStateReader 送进 providerRowsState.login。
const (
	// ProviderLoginStateLoggedIn 表示本机已有可用的登录凭证。
	ProviderLoginStateLoggedIn = "logged_in"
	// ProviderLoginStateLoggedOut 表示已安装但本机未登录。
	ProviderLoginStateLoggedOut = "logged_out"
	// ProviderLoginStateUnknown 表示检测未上报（超时、命令不存在、输出非 JSON），摘要保持原样。
	ProviderLoginStateUnknown = "unknown"
	// ProviderLoginStateNotRequired 表示走 API key / 网关令牌 / Bedrock / Vertex，不需要 claude.ai 登录。
	ProviderLoginStateNotRequired = "not_required"
)

// cliProviderIDs 是 CLI harness 的静态 ID 列表，用于名称、图标与默认可执行文件名的回退。
var cliProviderIDs = []string{"copilot", "pi", "omp", "commandcode", "droid", "cline", "cursor", "opencode", "grok", "devin"}

// providerRowsLoadTimeout 是供应商行单次刷新的上限：超时后放弃等待并允许重新进入页面重试，
// 避免任一调用挂住时行永远停在「检查中」。
const providerRowsLoadTimeout = 15 * time.Second

// providerStatusCacheFileName 是模型供应商状态缓存的文件名，保存在 mcode 数据目录下
// （与 ui-state.json 同目录，见 ui_state.go 的 uiStatePath）。
const providerStatusCacheFileName = "provider-status-cache.json"

// providerStatusCacheVersion 是缓存文件的结构版本；读到其他版本时视为没有缓存。
const providerStatusCacheVersion = 2

// providerStatusCache 是模型供应商页最近一次成功加载的结果快照：页面打开时先同步读它立即渲染，
// 再在后台刷新并在完成后写回。嵌套结构复用 UI 侧的类型，字段名即 Go 字段名。
type providerStatusCache struct {
	Version    int                              `json:"version"`
	SavedAt    string                           `json:"savedAt"`
	Available  map[string]bool                  `json:"available,omitempty"`
	Versions   map[string]ProviderVersionInfo   `json:"versions,omitempty"`
	VersionErr map[string]string                `json:"versionErr,omitempty"`
	Account    map[string]ProviderAccountInfo   `json:"account,omitempty"`
	Quota      map[string][]ProviderQuotaWindow `json:"quota,omitempty"`
	Credits    map[string]ProviderCredits       `json:"credits,omitempty"`
	Settings   map[string]ProviderSetting       `json:"settings,omitempty"`
	CLIIDs     []string                         `json:"cliIDs,omitempty"`

	// agy（Antigravity）账号快照：账号列表、默认账号与「本机已登录」状态。
	AgyAccounts   []accounts.Account `json:"agyAccounts,omitempty"`
	AgyDefaultID  string             `json:"agyDefaultID,omitempty"`
	AgyLocalLogin bool               `json:"agyLocalLogin,omitempty"`
}

// providerStatusCachePath 返回缓存文件路径：优先用设置页的数据目录，缺失时沿用
// ui_state.go 的 resolveDataDirFallback（MCODE_DATA 或 ~/.mcode）；都取不到时返回空串。
func providerStatusCachePath(dataDir string) string {
	if dataDir == "" {
		dataDir = resolveDataDirFallback()
	}
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, providerStatusCacheFileName)
}

// loadProviderStatusCache 读取缓存；文件缺失、损坏或版本不符时返回 false。
func loadProviderStatusCache(dataDir string) (providerStatusCache, bool) {
	path := providerStatusCachePath(dataDir)
	if path == "" {
		return providerStatusCache{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return providerStatusCache{}, false
	}
	var cache providerStatusCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return providerStatusCache{}, false
	}
	if cache.Version != providerStatusCacheVersion {
		return providerStatusCache{}, false
	}
	return cache, true
}

// providerStatusCacheMu 串行化缓存文件的读改写，避免供应商行与 agy 账号两个刷新流程互相覆盖。
var providerStatusCacheMu sync.Mutex

// providerStatusCacheWriters 跟踪可能写供应商状态缓存的后台 goroutine，测试删除数据目录前据此等待写完。
var providerStatusCacheWriters sync.WaitGroup

// updateProviderStatusCache 读入现有缓存、交给 mutate 修改后原子写回；写盘失败静默。
func updateProviderStatusCache(dataDir string, mutate func(*providerStatusCache)) {
	providerStatusCacheMu.Lock()
	defer providerStatusCacheMu.Unlock()

	path := providerStatusCachePath(dataDir)
	if path == "" {
		return
	}
	cache, _ := loadProviderStatusCache(dataDir)
	cache.Version = providerStatusCacheVersion
	mutate(&cache)
	cache.SavedAt = time.Now().UTC().Format(time.RFC3339)
	_ = saveProviderStatusCache(path, cache)
}

// saveProviderStatusCache 原子写入缓存文件：先写同目录临时文件再重命名覆盖，
// 与 ui_state.go 的 saveUIStateFile 同一套落盘方式。
func saveProviderStatusCache(path string, cache providerStatusCache) error {
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, data, 0o600)
}

// applyProviderStatusCache 用缓存快照填充供应商行状态，供页面打开时立即渲染（调用方需持有 st.mu）。
func applyProviderStatusCache(st *SettingsState, cache providerStatusCache) {
	st.providerRows.loaded = true
	st.providerRows.available = cache.Available
	st.providerRows.versions = cache.Versions
	st.providerRows.versionErr = cache.VersionErr
	st.providerRows.account = cache.Account
	st.providerRows.quota = cache.Quota
	st.providerRows.credits = cache.Credits
	st.providerRows.settings = cache.Settings
	st.providerRows.cliIDs = cache.CLIIDs
	if st.providerRows.binDraft == nil {
		st.providerRows.binDraft = make(map[string]string)
	}
	for _, id := range cache.CLIIDs {
		st.providerRows.binDraft[id] = cache.Settings[id].Bin
	}
	if st.providerRows.visionDraft == nil {
		st.providerRows.visionDraft = make(map[string]string)
	}
	for _, id := range cache.CLIIDs {
		st.providerRows.visionDraft[id] = cache.Settings[id].PiVisionModel
	}
	if st.providerRows.saveErr == nil {
		st.providerRows.saveErr = make(map[string]string)
	}
	if st.providerRows.saving == nil {
		st.providerRows.saving = make(map[string]bool)
	}
	if st.providerRows.updateBusy == nil {
		st.providerRows.updateBusy = make(map[string]bool)
	}
	if st.providerRows.updateMsg == nil {
		st.providerRows.updateMsg = make(map[string]string)
	}
}

// providerVersionResolved 表示版本探测已经解析到可执行文件：即使 --version 执行失败
// （例如命令不支持该参数），路径解析成功也说明该 Provider 已安装。
func providerVersionResolved(v ProviderVersionInfo) bool {
	return strings.TrimSpace(v.Installed) != "" || strings.TrimSpace(v.ExecutablePath) != ""
}

// providerBinPlaceholder 返回「可执行文件路径」输入框的 placeholder：
// 优先展示自动检测到的路径，未检测到时回落到 PATH 查找提示。
func providerBinPlaceholder(v ProviderVersionInfo) string {
	if path := strings.TrimSpace(v.ExecutablePath); path != "" {
		return path
	}
	return "留空则从 PATH 查找"
}

// isCLIProviderID 判断 ID 是否为 CLI harness（ProviderSetting.Kind == "cli"）。
func isCLIProviderID(id string) bool {
	for _, cliID := range cliProviderIDs {
		if cliID == id {
			return true
		}
	}
	return false
}

// cliDefaultBin 返回 CLI harness 的默认可执行文件名。
func cliDefaultBin(id string) string {
	switch id {
	case "copilot":
		return "copilot"
	case "pi":
		return "pi"
	case "omp":
		return "omp"
	case "commandcode":
		// Command Code 的命令名是 cmd（command-code/commandcode 是别名）。
		return "cmd"
	case "droid":
		return "droid"
	case "cline":
		return "cline"
	case "cursor":
		// 对照 acp.DefaultBinary：Cursor 的可执行文件名为 cursor-agent。
		return "cursor-agent"
	case "opencode":
		return "opencode"
	case "grok":
		return "grok"
	case "devin":
		return "devin"
	default:
		return id
	}
}

// nativeProviderOrder 原生顺序：codex -> claude -> antigravity (agy)。
var nativeProviderOrder = []string{"codex", "claude", "agy"}

func providerDisplayName(id string) string {
	switch id {
	case "codex":
		return "Codex"
	case "claude":
		return "Claude"
	case "agy":
		return "Antigravity"
	case "anthropic":
		return "Anthropic"
	case "deepseek":
		return "DeepSeek"
	case "zai", "z.ai":
		return "Z.ai"
	case "meta":
		return "Meta"
	case "openai":
		return "OpenAI"
	case "copilot":
		return "GitHub Copilot"
	case "pi":
		return "Pi"
	case "omp":
		return "Omp"
	case "commandcode":
		return "Command Code"
	case "droid":
		return "Droid"
	case "cline":
		return "Cline"
	case "cursor":
		return "Cursor"
	case "opencode":
		return "OpenCode"
	case "grok":
		return "Grok"
	case "devin":
		return "Devin"
	default:
		return id
	}
}

// providerRowName 返回供应商行标题：CLI harness 优先用 ProviderSettings 中的名称，缺失时回退静态表。
func providerRowName(st *SettingsState, id string) string {
	st.mu.Lock()
	name := ""
	for _, endpoint := range st.endpoints.endpoints {
		if endpoint.ID == id {
			name = strings.TrimSpace(endpoint.Name)
			break
		}
	}
	if name != "" {
		st.mu.Unlock()
		return name
	}
	switch id {
	case "anthropic", "deepseek", "zai", "z.ai", "meta", "openai":
		st.mu.Unlock()
		return providerDisplayName(id)
	}
	if st.providerRows.settings != nil {
		name = strings.TrimSpace(st.providerRows.settings[id].Name)
	}
	st.mu.Unlock()
	if name != "" {
		return name
	}
	return providerDisplayName(id)
}

func (st *SettingsState) loadProviderRows() {
	client := st.agyClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	if st.providerRows.loading {
		st.mu.Unlock()
		return
	}
	// 页面打开时先同步读上一次成功加载的缓存立即渲染，随后在后台刷新；
	// 没有缓存时保持原有的加载中状态。
	if !st.providerRows.loaded {
		if cached, ok := loadProviderStatusCache(st.dataDir); ok {
			applyProviderStatusCache(st, cached)
		}
	}
	st.providerRows.loading = true
	st.mu.Unlock()
	st.invalidate()

	providerStatusCacheWriters.Add(1)
	go func() {
		defer providerStatusCacheWriters.Done()
		// 无论中途哪个调用失败，都要把 loading 复位，否则行会一直停在「检查中…」。
		defer func() {
			st.mu.Lock()
			st.providerRows.loading = false
			st.mu.Unlock()
			st.invalidate()
		}()

		ctx, cancel := context.WithTimeout(context.Background(), providerRowsLoadTimeout)
		defer cancel()
		provList, errProv := client.Providers(ctx)

		avail := make(map[string]bool)
		for _, p := range provList {
			avail[p.ID] = p.Available
		}

		// ProviderSettings 提供 CLI harness 的可执行文件路径、启用状态与更新监控开关。
		settingsList, errSettings := client.ProviderSettings(ctx)
		cliSettings := make(map[string]ProviderSetting)
		var cliIDs []string
		if errSettings == nil {
			for _, s := range settingsList {
				// 内置 mcode 引擎不再作为供应商出现，只为老 mcode 会话保留。
				if s.Kind != "cli" || s.ID == "mcode" {
					continue
				}
				cliSettings[s.ID] = s
				cliIDs = append(cliIDs, s.ID)
			}
		}

		vers := make(map[string]ProviderVersionInfo)
		vErr := make(map[string]string)
		accs := make(map[string]ProviderAccountInfo)
		quotas := make(map[string][]ProviderQuotaWindow)
		quotaErrs := make(map[string]string)
		credits := make(map[string]ProviderCredits)

		for _, id := range append([]string{"claude", "codex"}, cliIDs...) {
			// 已安装版本始终探测并展示（一次本地 --version）；更新监控只影响远程 latest 的
			// 检查，由 checkProviderUpdate 按需触发。
			ver, errVer := client.ProviderVersion(ctx, id)
			if errVer != nil {
				vErr[id] = errVer.Error()
				// 版本探测失败（例如命令不支持 --version）不代表未安装：
				// 只要解析到了可执行文件路径就保留，供「已安装」与路径 placeholder 使用。
				if strings.TrimSpace(ver.ExecutablePath) != "" {
					vers[id] = ver
				}
			} else {
				vers[id] = ver
			}

			acc, errAcc := client.ProviderAccount(ctx, id)
			if errAcc == nil {
				accs[id] = acc
			}

			q, errQ := client.ProviderQuota(ctx, id)
			if errQ == nil {
				quotas[id] = q
			} else {
				quotaErrs[id] = errQ.Error()
			}
		}

		// 登录态：与版本探测同一条后台路径，只对已安装的 claude 读一次
		// （internal/providers/claude 的 AuthChecker 自带 60 秒缓存与并发去重，不会卡住渲染线程）。
		// 后端没实现可选接口（如远程客户端）或 claude 未安装时 map 为空，行摘要与现状一致。
		loginStates := make(map[string]string)
		if reader, ok := client.(providerLoginStateReader); ok && providerVersionResolved(vers["claude"]) {
			if state := strings.TrimSpace(reader.ProviderLoginState(ctx, "claude")); state != "" {
				loginStates["claude"] = state
			}
		}

		// 余额：所有 Provider（含 CLI 与已启用的 API 预设）都尝试，未实现或不支持的错误忽略。
		creditIDs := append([]string{"claude", "codex", "agy"}, cliIDs...)
		for _, s := range settingsList {
			// API 预设（DeepSeek、Z.ai 等）只在启用后才会被调用，因此只对已启用的查余额。
			if s.Kind == "api" && s.Enabled {
				creditIDs = append(creditIDs, s.ID)
			}
		}
		seenCreditID := make(map[string]bool, len(creditIDs))
		for _, id := range creditIDs {
			if seenCreditID[id] {
				continue
			}
			seenCreditID[id] = true
			if c, errCredits := client.ProviderCredits(ctx, id); errCredits == nil {
				credits[id] = c
			}
		}

		st.mu.Lock()
		st.providerRows.loaded = true
		// 失败的那一项不覆盖上次的结果：空 map 会把所有行翻成「未安装」、让 CLI 行整块消失。
		loadErr := ""
		if errProv != nil {
			loadErr = "供应商列表加载失败：" + errProv.Error()
		} else {
			st.providerRows.available = avail
		}
		// 供应商列表都没拉到（通常是超时）时，逐项探测也一并失败；此时保留上次的版本与额度，
		// 不要用一个空快照覆盖掉它们。
		if errProv == nil || len(vers) > 0 {
			st.providerRows.versions = vers
			st.providerRows.login = loginStates
			st.providerRows.versionErr = vErr
			st.providerRows.account = accs
			// 额度刷新失败时保留上次的窗口，配合 quotaErr 展示错误与「数据过期」，而不是整块消失。
			for id := range quotaErrs {
				if prev := st.providerRows.quota[id]; len(prev) > 0 {
					quotas[id] = prev
				}
			}
			st.providerRows.quota = quotas
			st.providerRows.quotaErr = quotaErrs
			st.providerRows.credits = credits
		}
		if errSettings != nil {
			if loadErr != "" {
				loadErr += "；"
			}
			loadErr += "供应商设置加载失败：" + errSettings.Error()
		} else {
			st.providerRows.settings = cliSettings
			st.providerRows.cliIDs = cliIDs
			if st.providerRows.binDraft == nil {
				st.providerRows.binDraft = make(map[string]string)
			}
			for _, id := range cliIDs {
				st.providerRows.binDraft[id] = cliSettings[id].Bin
			}
			if st.providerRows.visionDraft == nil {
				st.providerRows.visionDraft = make(map[string]string)
			}
			for _, id := range cliIDs {
				st.providerRows.visionDraft[id] = cliSettings[id].PiVisionModel
			}
		}
		st.providerRows.loadErr = loadErr
		if st.providerRows.saveErr == nil {
			st.providerRows.saveErr = make(map[string]string)
		}
		if st.providerRows.saving == nil {
			st.providerRows.saving = make(map[string]bool)
		}
		if st.providerRows.updateBusy == nil {
			st.providerRows.updateBusy = make(map[string]bool)
		}
		if st.providerRows.updateMsg == nil {
			st.providerRows.updateMsg = make(map[string]string)
		}
		st.mu.Unlock()

		// 写回缓存，供下次打开页面时立即渲染（agy 账号快照由 loadAgy 写入，这里不动）。
		// 设置读取失败时保留上一次的快照，避免把一次失败的加载固化下来。
		if errSettings == nil {
			updateProviderStatusCache(st.dataDir, func(cache *providerStatusCache) {
				if errProv == nil {
					cache.Available = avail
				}
				if errProv == nil || len(vers) > 0 {
					cache.Versions = vers
					cache.VersionErr = vErr
					cache.Account = accs
					cache.Quota = quotas
					cache.Credits = credits
				}
				cache.Settings = cliSettings
				cache.CLIIDs = cliIDs
			})
		}

		st.invalidate()
	}()
}

func providerConfigured(st *SettingsState, id string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if id == "agy" {
		return len(st.agy.accounts) > 0 || st.agy.localLogin
	}
	if st.providerRows.available != nil && st.providerRows.available[id] {
		return true
	}
	if st.providerRows.versions != nil && providerVersionResolved(st.providerRows.versions[id]) {
		return true
	}
	return false
}

func openProviderSheet(st *SettingsState, kind, id string) {
	st.mu.Lock()
	st.providerAdvancedOpen = false
	st.providerRows.currentProviderKind = kind
	st.providerRows.currentProvider = id
	st.mu.Unlock()
	if _, ok := settingsTreeSheetOf("provider"); ok {
		// 记下来源行，Esc 关闭面板后高亮回到这一行。
		openSettingsSheetFrom(st, "provider", "", "provider-row-"+kind+"-"+id)
	} else {
		st.invalidate()
	}
}

func currentProviderSheet(st *SettingsState) (kind, id string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.providerRows.currentProviderKind, st.providerRows.currentProvider
}

func cliProviderListEntries(st *SettingsState, lib *modelsLibraryView) []providerListEntry {
	var entries []providerListEntry
	seen := make(map[string]bool)
	for _, id := range orderedProviderIDs(st) {
		// 内置 mcode 引擎只为老 mcode 会话保留，不再作为供应商列出。
		if seen[id] || id == "mcode" {
			continue
		}
		seen[id] = true
		status := providerStatusFor(st, id)
		hasSwitch, enabled := providerRowSwitchState(st, id)
		if !hasSwitch {
			enabled = status.Ready
		}
		label := status.Summary
		switch {
		case strings.HasPrefix(label, "检查中"):
			label = "检查中…"
		case strings.Contains(label, "未登录"):
			label = "需登录"
		case strings.HasPrefix(label, "未安装"):
			label = "未安装"
		case status.Ready:
			label = "已连接"
		}
		st.mu.Lock()
		versionErr := st.providerRows.versionErr[id]
		st.mu.Unlock()
		if versionErr != "" && !status.Ready && label != "未安装" {
			label = "版本错误"
		}
		entry := providerListEntry{Kind: "cli", ID: id, Name: providerRowName(st, id), Status: label, Enabled: enabled, CanToggle: hasSwitch}
		if id == "claude" || id == "codex" {
			entry.Locked = st.isLocked(id + "-enabled")
			entry.Toggle = func(enabled bool) {
				st.mu.Lock()
				st.cfgMu.Lock()
				if id == "claude" {
					st.cfg.Providers.Claude.Enabled = enabled
				} else {
					st.cfg.Providers.Codex.Enabled = enabled
				}
				st.cfgMu.Unlock()
				st.mu.Unlock()
				st.save()
			}
		} else if hasSwitch {
			entry.Toggle = func(enabled bool) { saveProviderRowEnabled(st, id, enabled) }
		}
		entries = append(entries, entry)
	}
	return entries
}

func orderedProviderIDs(st *SettingsState) []string {
	st.mu.Lock()
	cliIDs := append([]string(nil), st.providerRows.cliIDs...)
	st.mu.Unlock()

	var configured []string
	var unconfigured []string
	for _, id := range nativeProviderOrder {
		if providerConfigured(st, id) {
			configured = append(configured, id)
		} else {
			unconfigured = append(unconfigured, id)
		}
	}

	// CLI harness 追加在原生 Provider 之后，保持 ProviderSettings 返回顺序，同样已配置居前。
	var cliConfigured []string
	var cliUnconfigured []string
	for _, id := range cliIDs {
		if providerConfigured(st, id) {
			cliConfigured = append(cliConfigured, id)
		} else {
			cliUnconfigured = append(cliUnconfigured, id)
		}
	}

	result := make([]string, 0, len(configured)+len(unconfigured)+len(cliIDs))
	result = append(result, configured...)
	result = append(result, unconfigured...)
	result = append(result, cliConfigured...)
	result = append(result, cliUnconfigured...)
	return result
}

// providerGroupInput 是 groupProviderIDs 的单个入参快照，避免纯函数直接读 SettingsState。
type providerGroupInput struct {
	HasSwitch    bool // 行头有启用开关
	Enabled      bool // 开关处于启用状态
	Ready        bool // providerStatusFor 判定可直接使用，用于没有开关的行
	NotInstalled bool // 探测结果已知且确认未安装；加载中或结果未知时为 false
}

// groupProviderIDs 把供应商行分成三组：已启用、已安装未启用、未安装，组内保持 ids 的顺序。
// 有开关的按开关状态分组；没有开关的（例如 agy）按是否就绪分组；确定未安装的单独成组。
func groupProviderIDs(ids []string, inputs map[string]providerGroupInput) (enabled, installedOff, uninstalled []string) {
	for _, id := range ids {
		in := inputs[id]
		switch {
		case in.HasSwitch && in.Enabled:
			enabled = append(enabled, id)
		case in.NotInstalled:
			uninstalled = append(uninstalled, id)
		case in.HasSwitch:
			installedOff = append(installedOff, id)
		case in.Ready:
			enabled = append(enabled, id)
		default:
			installedOff = append(installedOff, id)
		}
	}
	return enabled, installedOff, uninstalled
}

// providerRowSwitchState 返回某行启用开关是否存在、当前是否启用：claude/codex 读固定配置，
// CLI harness 读 ProviderSettings.Enabled；其余 Provider（例如 agy）没有启用开关。
func providerRowSwitchState(st *SettingsState, id string) (hasSwitch bool, enabled bool) {
	if id != "claude" && id != "codex" && !isCLIProviderID(id) {
		return false, false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	switch id {
	case "claude":
		return true, st.cfg.Providers.Claude.Enabled
	case "codex":
		return true, st.cfg.Providers.Codex.Enabled
	}
	setting, ok := st.providerRows.settings[id]
	if !ok {
		// 设置还没拉到时先不展示开关，与行渲染一致。
		return false, false
	}
	return true, setting.Enabled
}

// providerRowNotInstalled 判断探测结果已确认某个 Provider 未安装：只有拿到供应商列表（或上次结果）、
// 且版本也没解析出来时才成立。加载中或结果未知时一律返回 false，避免首帧把所有行闪进「未安装」分组。
func providerRowNotInstalled(st *SettingsState, id string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.providerRows.loading && !st.providerRows.loaded {
		return false
	}
	if st.providerRows.versions != nil {
		if providerVersionResolved(st.providerRows.versions[id]) {
			return false
		}
	}
	if st.providerRows.available == nil {
		return false
	}
	avail, ok := st.providerRows.available[id]
	return ok && !avail
}

const (
	// Provider 行右侧操作区的固定列宽：快捷按钮、启用开关、展开箭头。
	// 某个控件不存在时保留等宽占位，使各行的状态与操作列保持对齐。
	providerActionSlotWidth float32 = 52
	providerSwitchSlotWidth float32 = 36
	providerArrowSlotWidth  float32 = 14
)

// providerKind 区分三类 Provider 的状态推导方式：CLI 型（codex/claude 与 CLI harness）
// 看安装与版本，登录型（agy）看账号，API Key 型看是否已保存 Key。
type providerKind int

const (
	providerKindCLI providerKind = iota
	providerKindLogin
	providerKindAPIKey
)

// providerRowStatus 是 Provider 行的一行状态摘要与下一步动作。
type providerRowStatus struct {
	// Ready 表示该 Provider 可直接使用。
	Ready bool
	// Summary 是一行摘要，例如「已安装 v1.2 · 已登录 2 个账号」。加载中时为「检查中…」。
	Summary string
	// NextStep 在不可用时给出下一步（例如「点击安装」），可用时为空。
	NextStep string
}

// providerStatusInput 是 providerStatusSummary 的入参快照，避免纯函数直接读 SettingsState。
type providerStatusInput struct {
	Kind           providerKind
	Loading        bool // 尚无任何结果（首次加载中）
	Available      bool
	AvailableKnown bool // 已拿到可执行文件探测结果；未知时不判「未安装」
	Version        ProviderVersionInfo
	AccountCount   int
	LocalLogin     bool // 登录型 Provider 在本机已有系统凭证
	HasAPIKey      bool
	LoginState     string // CLI 型 Provider 的登录态，取值见 ProviderLoginState*
}

// providerStatusSummary 统一推导 Provider 行状态，供 CLI 行与 API Key 行共用同一套展示。
func providerStatusSummary(in providerStatusInput) providerRowStatus {
	if in.Loading {
		return providerRowStatus{Summary: "检查中…"}
	}
	switch in.Kind {
	case providerKindLogin:
		if in.AvailableKnown && !in.Available {
			return providerRowStatus{Summary: "未安装", NextStep: "点击安装"}
		}
		if in.AccountCount > 0 {
			return providerRowStatus{Ready: true, Summary: fmt.Sprintf("已登录 %d 个账号", in.AccountCount)}
		}
		// 账号管理器未接线（例如以 memory provider 启动）时，仍按本机凭证报告已登录。
		if in.LocalLogin {
			return providerRowStatus{Ready: true, Summary: "已登录（系统账号）"}
		}
		return providerRowStatus{Summary: "未登录", NextStep: "需要登录"}
	case providerKindAPIKey:
		if in.HasAPIKey {
			return providerRowStatus{Ready: true, Summary: "已配置"}
		}
		return providerRowStatus{Summary: "未配置", NextStep: "填写 API Key"}
	}

	installed := strings.TrimSpace(in.Version.Installed)
	resolved := providerVersionResolved(in.Version)
	isInstalled := in.Available || resolved
	loginSuffix := providerLoginSuffix(in.LoginState)
	// claude 已安装但本机未登录时用不了：摘要追加「未登录（请在终端运行 claude /login）」，
	// 并按其他 Provider 行的未就绪样式（警告色）渲染。
	loggedOut := strings.TrimSpace(in.LoginState) == ProviderLoginStateLoggedOut
	if !isInstalled {
		return providerRowStatus{Summary: "未安装", NextStep: "点击安装"}
	}

	summary := "已安装"
	if installed != "" {
		summary += " · v" + installed
	}
	summary += loginSuffix
	if !in.Version.UpdateAvailable {
		return providerRowStatus{Ready: !loggedOut, Summary: summary}
	}
	latest := strings.TrimSpace(in.Version.Latest)
	if latest != "" {
		summary += " · 有新版本 " + latest
	} else {
		summary += " · 有新版本"
	}
	// 已安装可继续使用，只是建议升级；更新入口由行内的「更新」按钮负责，不再给出下一步文案。
	return providerRowStatus{Ready: !loggedOut, Summary: summary}
}

// providerLoginSuffix 把登录态拼成摘要后缀；未上报或不需要登录时返回空串，摘要保持原样。
func providerLoginSuffix(state string) string {
	switch strings.TrimSpace(state) {
	case ProviderLoginStateLoggedIn:
		return " · 已登录"
	case ProviderLoginStateLoggedOut:
		return " · 未登录（请在终端运行 claude /login）"
	}
	return ""
}

// providerStatusFor 从 SettingsState 收集状态快照并推导摘要。
func providerStatusFor(st *SettingsState, id string) providerRowStatus {
	st.mu.Lock()
	defer st.mu.Unlock()
	in := providerStatusInput{
		Kind:    providerKindCLI,
		Loading: st.providerRows.loading && !st.providerRows.loaded,
	}
	if id == "agy" {
		in.Kind = providerKindLogin
		// 账号列表里固定带一个系统账号，但它只有在本机确实存在系统凭证时才算已登录账号；
		// 否则没装 agy 也没登录的机器会显示「已登录 1 个账号」。
		for _, acc := range st.agy.accounts {
			if acc.ID == accounts.SystemAccountID && !st.agy.localLogin {
				continue
			}
			in.AccountCount++
		}
		in.LocalLogin = st.agy.localLogin
		// agy 的可执行文件探测结果和其他 Provider 一样来自供应商列表；
		// 列表还没拉到时（或压根没上报 agy）不能当成未安装。
		if avail, ok := st.providerRows.available[id]; ok {
			in.AvailableKnown = true
			in.Available = avail
		}
		return providerStatusSummary(in)
	}
	if st.providerRows.available != nil {
		in.Available = st.providerRows.available[id]
	}
	if st.providerRows.versions != nil {
		in.Version = st.providerRows.versions[id]
	}
	if st.providerRows.login != nil {
		in.LoginState = st.providerRows.login[id]
	}
	return providerStatusSummary(in)
}

// providerRowSubtitle 返回 Provider 行下方的一行状态文案；不可用时追加下一步动作。
func providerRowSubtitle(st *SettingsState, id string) string {
	status := providerStatusFor(st, id)
	return providerSubtitleText(status)
}

// providerSubtitleText 拼出状态摘要；不可用时在摘要后追加「 · <NextStep>」。
func providerSubtitleText(status providerRowStatus) string {
	if status.NextStep == "" {
		return status.Summary
	}
	if status.Summary == "" {
		return status.NextStep
	}
	return status.Summary + " · " + status.NextStep
}

func quoteShellArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// providerRowBin 返回 CLI harness 当前生效的可执行文件：优先 settings 中的 Bin，留空时用默认命令名。
func providerRowBin(st *SettingsState, id string) string {
	st.mu.Lock()
	bin := ""
	if st.providerRows.settings != nil {
		bin = strings.TrimSpace(st.providerRows.settings[id].Bin)
	}
	st.mu.Unlock()
	if bin == "" {
		bin = cliDefaultBin(id)
	}
	return bin
}

// providerTerminalCommand 返回供应商终端动作对应的命令；不支持的动作返回 ok=false。
// 更新优先用后端按安装渠道动态生成的命令（ProviderVersionInfo.UpdateCommand），
// 它跟着二进制的真实安装方式走，不只限 claude/codex/copilot。
func providerTerminalCommand(st *SettingsState, id, action string) (string, bool) {
	if action == "update" {
		if cmd := providerUpdateCommand(st, id); cmd != "" {
			return cmd, true
		}
	}
	switch id {
	case "claude":
		if action == "install" {
			if providerTerminalGOOS == "windows" {
				return "irm https://claude.ai/install.ps1 | iex", true
			}
			return "npm install -g @anthropic-ai/claude-code", true
		}
		bin := st.cfg.Providers.Claude.Bin
		if bin == "" {
			bin = "claude"
		}
		return quoteShellArg(bin) + " update", true
	case "codex":
		if action == "install" {
			return "npm install -g @openai/codex", true
		}
		bin := st.cfg.Providers.Codex.Bin
		if bin == "" {
			bin = "codex"
		}
		return quoteShellArg(bin) + " update", true
	case "pi":
		if action == "install" {
			return "npm install -g @earendil-works/pi-coding-agent", true
		}
		return "", false
	case "agy":
		if action == "install" {
			if providerTerminalGOOS == "windows" {
				return "irm https://antigravity.google/cli/install.ps1 | iex", true
			}
			return "curl -fsSL https://antigravity.google/cli/install.sh | bash", true
		}
		if action == "login" {
			return quoteShellArg(providerRowBin(st, "agy")), true
		}
		return "", false
	case "copilot":
		// Copilot 在独立脚本终端中完成登录/更新
		bin := providerRowBin(st, "copilot")
		if action == "login" {
			return quoteShellArg(bin) + " login", true
		}
		if action == "update" {
			return quoteShellArg(bin) + " update", true
		}
		return "", false
	default:
		return "", false
	}
}

// providerSheetPrimaryAction 返回供应商面板身份区的主操作：未安装给「安装」，有新版本给「更新」；
// 没有对应命令时 ok=false，身份区不画按钮。
func providerSheetPrimaryAction(st *SettingsState, id string) (label, action string, ok bool) {
	status := providerStatusFor(st, id)
	st.mu.Lock()
	version := st.providerRows.versions[id]
	st.mu.Unlock()
	if !status.Ready && strings.HasPrefix(status.Summary, "未安装") {
		if _, ok := providerTerminalCommand(st, id, "install"); ok {
			return "安装", "install", true
		}
		return "", "", false
	}
	if version.UpdateAvailable {
		if _, ok := providerTerminalCommand(st, id, "update"); ok {
			return "更新", "update", true
		}
	}
	return "", "", false
}

// providerUpdateCommand 返回后端为这个 Provider 生成的升级命令（跟着二进制的安装渠道走，
// 覆盖 pi、agy、cursor、grok、omp、cline 等）；渠道认不出来且没自带更新器时为空。
func providerUpdateCommand(st *SettingsState, id string) string {
	st.mu.Lock()
	version := ProviderVersionInfo{}
	if st.providerRows.versions != nil {
		version = st.providerRows.versions[id]
	}
	st.mu.Unlock()
	command := strings.TrimSpace(version.UpdateCommand)
	if command == "" {
		return ""
	}
	// 自带更新器的命令以 CLI 自己的命令名开头；用户改过可执行文件路径时换成配置的那个。
	name, args, _ := strings.Cut(command, " ")
	if name != cliDefaultBin(id) {
		return command
	}
	bin := providerConfiguredBin(st, id)
	if bin == "" || bin == name {
		return command
	}
	return strings.TrimSpace(quoteShellArg(bin) + " " + args)
}

// providerConfiguredBin 返回设置里指定的可执行文件：claude/codex 用固定配置，其余用 CLI harness 设置。
func providerConfiguredBin(st *SettingsState, id string) string {
	switch id {
	case "claude":
		return strings.TrimSpace(st.cfg.Providers.Claude.Bin)
	case "codex":
		return strings.TrimSpace(st.cfg.Providers.Codex.Bin)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.providerRows.settings != nil {
		return strings.TrimSpace(st.providerRows.settings[id].Bin)
	}
	return ""
}

func (st *SettingsState) checkProviderUpdate(id string) {
	client := st.agyClient()
	if client == nil {
		return
	}
	st.mu.Lock()
	if st.providerRows.updateBusy == nil {
		st.providerRows.updateBusy = make(map[string]bool)
	}
	if st.providerRows.updateBusy[id] {
		st.mu.Unlock()
		return
	}
	st.providerRows.updateBusy[id] = true
	if st.providerRows.updateMsg == nil {
		st.providerRows.updateMsg = make(map[string]string)
	}
	st.providerRows.updateMsg[id] = ""
	st.mu.Unlock()
	st.invalidate()

	go func() {
		ver, err := client.CheckProviderUpdate(context.Background(), id)
		st.mu.Lock()
		st.providerRows.updateBusy[id] = false
		if err != nil {
			if errors.Is(err, ErrNotImplemented) {
				st.providerRows.updateMsg[id] = "暂不支持检查更新"
			} else {
				st.providerRows.updateMsg[id] = err.Error()
			}
		} else {
			if st.providerRows.versions == nil {
				st.providerRows.versions = make(map[string]ProviderVersionInfo)
			}
			st.providerRows.versions[id] = ver
			if ver.UpdateAvailable {
				st.providerRows.updateMsg[id] = "发现新版本: " + ver.Latest
			} else {
				st.providerRows.updateMsg[id] = "已是最新版本"
			}
		}
		st.mu.Unlock()
		st.invalidate()
	}()
}

// saveProviderRowSetting 异步保存单个 CLI harness 设置（apiKey 传空表示保留已存 Key）。
// mutate 在加锁状态下修改设置副本，保存成功后写回 settings，失败写入 saveErr。
func saveProviderRowSetting(st *SettingsState, id string, mutate func(s *ProviderSetting)) {
	saveProviderRowSettingWithKey(st, id, "", mutate)
}

// saveProviderRowSettingWithKey 与 saveProviderRowSetting 相同，并同时保存非空的 API Key。
func saveProviderRowSettingWithKey(st *SettingsState, id, apiKey string, mutate func(s *ProviderSetting)) {
	saveProviderRowSettingMarked(st, id, apiKey, "", mutate)
}

func saveProviderRowSettingMarked(st *SettingsState, id, apiKey, marker string, mutate func(s *ProviderSetting)) {
	client := st.agyClient()
	if client == nil {
		return
	}

	st.mu.Lock()
	s, ok := st.providerRows.settings[id]
	if !ok {
		st.mu.Unlock()
		return
	}
	if st.providerRows.saving == nil {
		st.providerRows.saving = make(map[string]bool)
	}
	if st.providerRows.saving[id] {
		st.mu.Unlock()
		return
	}
	st.providerRows.saving[id] = true
	if st.providerRows.saveErr == nil {
		st.providerRows.saveErr = make(map[string]string)
	}
	st.providerRows.saveErr[id] = ""
	previousEnabled := s.Enabled
	mutate(&s)
	enabledChanged := s.Enabled != previousEnabled
	st.mu.Unlock()
	st.invalidate()

	go func() {
		saved, err := client.SaveProviderSetting(context.Background(), s, apiKey)
		st.mu.Lock()
		st.providerRows.saving[id] = false
		if err != nil {
			st.providerRows.saveErr[id] = err.Error()
		} else {
			st.providerRows.settings[id] = saved
			if marker != "" {
				if st.providerRows.savedAt == nil {
					st.providerRows.savedAt = make(map[string]time.Time)
				}
				st.providerRows.savedAt[id+":"+marker] = time.Now()
				time.AfterFunc(1500*time.Millisecond, st.invalidate)
			}
			// 草稿未被继续编辑时同步为保存结果
			if st.providerRows.binDraft != nil && strings.TrimSpace(st.providerRows.binDraft[id]) == strings.TrimSpace(s.Bin) {
				st.providerRows.binDraft[id] = saved.Bin
			}
			if st.providerRows.visionDraft != nil && strings.TrimSpace(st.providerRows.visionDraft[id]) == strings.TrimSpace(s.PiVisionModel) {
				st.providerRows.visionDraft[id] = saved.PiVisionModel
			}
		}
		st.mu.Unlock()
		if err == nil && enabledChanged {
			// 启用状态变化会影响选择器里的供应商与“我的模型”条目，同步会话缓存。
			refreshCustomEndpointProviders(st, client)
		}
		st.invalidate()
	}()
}

// saveProviderRowEnabled 异步切换 CLI harness 行的启用状态（apiKey 传空表示保留已存 Key）。
func saveProviderRowEnabled(st *SettingsState, id string, enabled bool) {
	saveProviderRowSetting(st, id, func(s *ProviderSetting) { s.Enabled = enabled })
}

// saveProviderRowBin 提交「可执行文件路径」草稿。
func saveProviderRowBin(st *SettingsState, id string) {
	st.mu.Lock()
	if st.providerRows.binDraft == nil {
		st.providerRows.binDraft = make(map[string]string)
	}
	draft := strings.TrimSpace(st.providerRows.binDraft[id])
	st.providerRows.binDraft[id] = draft
	st.mu.Unlock()

	saveProviderRowSettingMarked(st, id, "", "bin", func(s *ProviderSetting) { s.Bin = draft })
}

// saveProviderRowVision 提交 pi 「视觉模型」草稿（其他 Provider 不用）。
func saveProviderRowVision(st *SettingsState, id string) {
	st.mu.Lock()
	if st.providerRows.visionDraft == nil {
		st.providerRows.visionDraft = make(map[string]string)
	}
	draft := strings.TrimSpace(st.providerRows.visionDraft[id])
	st.providerRows.visionDraft[id] = draft
	st.mu.Unlock()

	saveProviderRowSettingMarked(st, id, "", "vision", func(s *ProviderSetting) { s.PiVisionModel = draft })
}

// providerAuthIndex 把认证方式映射为分段控件下标：browser（网页登录）为 0，apiKey 为 1。
func providerAuthIndex(auth string) int {
	if auth == "apiKey" {
		return 1
	}
	return 0
}

// providerAuthFromIndex 把分段控件下标映射回认证方式取值。
func providerAuthFromIndex(index int) string {
	if index == 1 {
		return "apiKey"
	}
	return "browser"
}

// renderProviderRowIcon 渲染供应商行首彩色图标；无彩色资源时回落到单色 ProviderIcon。
func renderProviderRowIcon(c *ui.Context, id string, size float32) {
	renderProviderIconColor(c, id, size, ThemeSecondaryText(c))
}

// renderProviderBalancePanel 渲染余额面板：预付费余额与额度窗口。
// claude/codex/agy 的额度窗口已在各自展开区渲染，这里不重复展示；两者都没有时不渲染。
func renderProviderBalancePanel(c *ui.Context, st *SettingsState, id string) {
	st.mu.Lock()
	var credits ProviderCredits
	hasCredits := false
	if st.providerRows.credits != nil {
		credits, hasCredits = st.providerRows.credits[id]
	}
	var quotaWindows []ProviderQuotaWindow
	if st.providerRows.quota != nil {
		quotaWindows = st.providerRows.quota[id]
	}
	quotaErr := st.providerRows.quotaErr[id]
	plan := ""
	if st.providerRows.account != nil {
		plan = st.providerRows.account[id].Plan
	}
	loading := st.providerRows.loading
	st.mu.Unlock()

	showQuota := len(quotaWindows) > 0 && id != "claude" && id != "codex" && id != "agy"
	if !hasCredits && !showQuota {
		return
	}

	if hasCredits {
		amount := strings.TrimSpace(credits.AmountText)
		if amount == "" {
			amount = strings.TrimSpace(fmt.Sprintf("%.2f %s", credits.Total, credits.Currency))
		}
		depleted := credits.Depleted()
		renderSettingRow(c, "row-provider-balance", "余额", func() {
			ui.Row(c.Key("balance-" + id)).
				AlignItems(ui.Center).
				Gap(Space3).
				Children(func() {
					amountColor := ThemePrimaryText(c)
					if depleted {
						amountColor = ThemeWarning(c)
					}
					ui.Text(c, amount).
						FontSize(uiFont(TypeBody)).
						FontWeight(500).
						TextColor(amountColor)
					if depleted {
						ui.Row(c.Key("badge-balance-depleted-"+id)).
							Padding(Space0, Space2).
							Radius(RadiusXS).
							Background(ThemeWarning(c).Alpha(0.12)).
							Children(func() {
								ui.Text(c, "余额不足").
									FontSize(uiFont(TypeCaption)).
									FontWeight(500).
									TextColor(ThemeWarning(c))
							})
					}
				})
		})
	}

	if showQuota {
		limits := make([]planLimit, 0, len(quotaWindows))
		for _, w := range quotaWindows {
			limits = append(limits, planLimit{
				Label:       w.Label,
				UsedPercent: w.UsedPercent,
				ResetsAt:    w.ResetsAt,
				RefreshedAt: w.ReadAt,
			})
		}
		withPlanLimitsErr(limits, quotaErr)
		renderPlanLimitsView(c, "balance-limits-"+id, plan, limits, 0, loading)
	}
}

// renderProviderCLIExpanded 渲染 CLI harness 展开区：启用开关、可执行文件路径、
// 更新监控、版本信息、droid 认证方式与 Copilot 登录引导；pi 额外有长缓存与视觉模型两项。
// locked 为真时写配置的控件一律禁用，只读展示照常渲染。
func renderProviderCLIExpanded(c *ui.Context, st *SettingsState, id string, accountSection bool, locked bool) {
	st.mu.Lock()
	var provSetting ProviderSetting
	if st.providerRows.settings != nil {
		provSetting = st.providerRows.settings[id]
	}
	binValue := ""
	if st.providerRows.binDraft != nil {
		binValue = st.providerRows.binDraft[id]
	}
	visionValue := ""
	if st.providerRows.visionDraft != nil {
		visionValue = st.providerRows.visionDraft[id]
	}
	saving := st.providerRows.saving != nil && st.providerRows.saving[id]
	binSaved := time.Since(st.providerRows.savedAt[id+":bin"]) < 1500*time.Millisecond
	visionSaved := time.Since(st.providerRows.savedAt[id+":vision"]) < 1500*time.Millisecond
	saveErr := ""
	if st.providerRows.saveErr != nil {
		saveErr = st.providerRows.saveErr[id]
	}
	var ver ProviderVersionInfo
	if st.providerRows.versions != nil {
		ver = st.providerRows.versions[id]
	}
	updateBusy := st.providerRows.updateBusy != nil && st.providerRows.updateBusy[id]
	updateMsg := ""
	if st.providerRows.updateMsg != nil {
		updateMsg = st.providerRows.updateMsg[id]
	}
	var acc ProviderAccountInfo
	hasAccount := false
	if st.providerRows.account != nil {
		acc, hasAccount = st.providerRows.account[id]
	}
	st.mu.Unlock()

	name := providerRowName(st, id)

	if !accountSection {
		// ACP 类 Provider（droid/cline/cursor/opencode/grok/devin）的配置改动需要重启生效
		if id == "droid" || id == "cline" || id == "cursor" || id == "opencode" || id == "grok" || id == "devin" {
			ui.Column(c.Key("provider-restart-hint-"+id)).FillWidth().Padding(settingsRowPadY, settingsRowPadX).Children(func() {
				ui.Text(c, "修改后需重启 MCode 生效").
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemeSecondaryText(c))
			})
		}

		// (b) 可执行文件路径：placeholder 显示自动检测到的路径，
		// 输入值仍以用户填写为准，留空表示按检测链自动解析。
		renderSettingRow(c, "row-provider-bin", "可执行文件路径", func() {
			ui.Row(c.Key("provider-bin-controls-" + id)).
				AlignItems(ui.Center).
				Gap(Space3).
				Children(func() {
					TextField(c, TextFieldProps{
						Key:         "input-provider-bin-" + id,
						Value:       &binValue,
						Label:       "可执行文件路径 " + name,
						Placeholder: providerBinPlaceholder(ver),
						Width:       280,
						Disabled:    locked,
						ReadOnly:    locked,
						OnChange: func(text string) {
							st.mu.Lock()
							if st.providerRows.binDraft == nil {
								st.providerRows.binDraft = make(map[string]string)
							}
							st.providerRows.binDraft[id] = text
							st.mu.Unlock()
						},
					})

					saveBtn := ui.ButtonBase(c.Key("btn-provider-bin-save-"+id)).
						Label("保存").
						Disabled(saving || locked).
						Radius(RadiusSM).
						Padding(settingsPillPadX, Space4).
						Background(ThemeHover(c))
					scale := motionPressScale(c, saveBtn, "button-press-scale", !saving && !locked && saveBtn.Pressed())
					saveBtn.Scale(scale, scale)
					if saveBtn.Hovered() {
						saveBtn.Background(ThemeOverlay(c, 0.15))
					}
					if saveBtn.Clicked() && !saving && !locked {
						saveProviderRowBin(st, id)
					}
					saveBtn.Children(func() {
						label := "保存"
						if binSaved {
							label = "已保存"
						}
						ui.Text(c, label).
							FontSize(uiFont(TypeBody)).
							TextColor(ThemeAccent(c))
					})
				})
		})

		// (c1) 仅 pi：长缓存开关与视觉模型输入框，保存后走同一个 SaveProviderSetting 流程
		// （这个流程会热重建 pi 的 harness）。
		if id == "pi" {
			renderSettingsDivider(c, "div-provider-pi-cache-"+id)

			renderSettingsTreeSetting(c, st, setting{
				ID:      "row-provider-pi-cache",
				Title:   "长缓存",
				Enabled: func(*SettingsState) bool { return !locked },
				Control: toggleFunc(
					func(*SettingsState) (bool, bool) { return provSetting.PiCacheLong, saving },
					func(_ *SettingsState, on bool) {
						saveProviderRowSetting(st, id, func(s *ProviderSetting) { s.PiCacheLong = on })
					},
				),
			})

			renderSettingsDivider(c, "div-provider-pi-vision-"+id)

			renderSettingRow(c, "row-provider-pi-vision", "视觉模型", func() {
				ui.Row(c.Key("provider-vision-controls-" + id)).
					AlignItems(ui.Center).
					Gap(Space3).
					Children(func() {
						TextField(c, TextFieldProps{
							Key:         "input-provider-vision-" + id,
							Value:       &visionValue,
							Label:       "视觉模型 " + name,
							Placeholder: "provider/model",
							Width:       280,
							Disabled:    locked,
							ReadOnly:    locked,
							OnChange: func(text string) {
								st.mu.Lock()
								if st.providerRows.visionDraft == nil {
									st.providerRows.visionDraft = make(map[string]string)
								}
								st.providerRows.visionDraft[id] = text
								st.mu.Unlock()
							},
						})

						saveBtn := ui.ButtonBase(c.Key("btn-provider-vision-save-"+id)).
							Label("保存视觉模型").
							Disabled(saving || locked).
							Radius(RadiusSM).
							Padding(settingsPillPadX, Space4).
							Background(ThemeHover(c))
						scale := motionPressScale(c, saveBtn, "button-press-scale", !saving && !locked && saveBtn.Pressed())
						saveBtn.Scale(scale, scale)
						if saveBtn.Hovered() {
							saveBtn.Background(ThemeOverlay(c, 0.15))
						}
						if saveBtn.Clicked() && !saving && !locked {
							saveProviderRowVision(st, id)
						}
						saveBtn.Children(func() {
							label := "保存"
							if visionSaved {
								label = "已保存"
							}
							ui.Text(c, label).
								FontSize(uiFont(TypeBody)).
								TextColor(ThemeAccent(c))
						})
					})
			})
		}

		// (d) 版本信息
		binPath := strings.TrimSpace(provSetting.Bin)
		if binPath == "" {
			binPath = strings.TrimSpace(ver.ExecutablePath)
		}
		if binPath == "" {
			binPath = cliDefaultBin(id)
		}
		installed := ver.Installed
		if installed == "" {
			installed = "未安装"
		}
		latest := ver.Latest
		if latest == "" {
			latest = "未知"
		}
		renderStackedSettingRow(c, "row-provider-version", "版本信息", func() {
			ui.Column(c.Key("provider-version-info-" + id)).
				Gap(Space1).
				Children(func() {
					settingStatus(c, "provider-version-status-"+id, fmt.Sprintf("已安装版本 %s · 最新版本 %s · 可执行文件 %s", installed, latest, binPath))
					ui.Row(c.Key("provider-version-actions-" + id)).
						AlignItems(ui.Center).
						Gap(Space3).
						Children(func() {
							checkBtn := ui.ButtonBase(c.Key("btn-provider-check-update-"+id)).
								Label("检查更新").
								Radius(RadiusSM).
								Padding(settingsPillPadX, Space4).
								Background(ThemeHover(c))
							if updateBusy {
								checkBtn.Disabled(true)
							} else if checkBtn.Hovered() {
								checkBtn.Background(ThemeOverlay(c, 0.12))
							}
							if checkBtn.Clicked() && !updateBusy {
								st.checkProviderUpdate(id)
							}
							checkBtn.Children(func() {
								label := "检查更新"
								if updateBusy {
									label = "正在检查更新…"
								}
								ui.Text(c, label).
									FontSize(uiFont(TypeBody)).
									TextColor(ThemePrimaryText(c))
							})

							if updateMsg != "" {
								msgColor := ThemeSecondaryText(c)
								if strings.Contains(updateMsg, "失败") || strings.Contains(updateMsg, "error") {
									msgColor = ThemeDanger(c)
								} else if strings.Contains(updateMsg, "新版本") {
									msgColor = ThemeAccent(c)
								}
								ui.Text(c, updateMsg).
									FontSize(uiFont(TypeFootnote)).
									TextColor(msgColor)
							}
						})
				})
		})
	}
	if accountSection {
		// (e) droid 认证方式（网页登录 / API Key）
		if id == "droid" {
			renderSettingsTreeSetting(c, st, setting{
				ID:      "row-provider-auth",
				Title:   "认证方式",
				Enabled: func(*SettingsState) bool { return !locked },
				Control: choiceFunc([]string{"网页登录", "API Key"},
					func(*SettingsState) (int, bool) {
						return providerAuthIndex(provSetting.Auth), false
					},
					func(_ *SettingsState, idx int) {
						auth := providerAuthFromIndex(idx)
						saveProviderRowSetting(st, id, func(s *ProviderSetting) { s.Auth = auth })
					},
				),
			})
		}

		// (f) Copilot 登录状态与引导：已登录时显示「已登录：<邮箱>」，
		// 只有拿不到邮箱（未登录）时才显示引导行与「在终端登录」按钮。
		if id == "copilot" {
			if hasAccount && acc.Email != "" {
				renderStackedSettingRow(c, "row-provider-copilot-account", "GitHub Copilot 登录", func() {
					ui.Column(c.Key("provider-copilot-account-" + id)).
						Gap(Space0).
						Children(func() {
							settingStatus(c, "provider-copilot-account-status-"+id, "已登录："+acc.Email)
							if acc.Plan != "" {
								ui.Text(c, acc.Plan).
									FontSize(uiFont(TypeFootnote)).
									TextColor(ThemeSecondaryText(c))
							}
						})
				})
			} else {
				renderStackedSettingRow(c, "row-provider-copilot-login", "GitHub Copilot 登录", func() {
					ui.Column(c.Key("provider-copilot-login-" + id)).
						Gap(Space1).
						Children(func() {
							settingStatus(c, "provider-copilot-login-status-"+id, "尚未登录 GitHub Copilot")
							loginBtn := ui.ButtonBase(c.Key("btn-provider-copilot-login")).
								Label("在终端登录").
								Radius(RadiusSM).
								Padding(settingsPillPadX, Space4).
								Background(ThemeHover(c))
							if loginBtn.Hovered() {
								loginBtn.Background(ThemeOverlay(c, 0.15))
							}
							if loginBtn.Clicked() {
								launchProviderTerminalAction(st, "copilot", "login")
							}
							loginBtn.Children(func() {
								ui.Text(c, "在终端登录").
									FontSize(uiFont(TypeBody)).
									TextColor(ThemeAccent(c))
							})
						})
				})
			}
		}

		// (g) 已连接账号
		renderProviderAccountsSection(c, st, id)
	}

	if !accountSection && saveErr != "" {
		ui.Row(c.Key("row-provider-save-err-"+id)).
			Padding(0, settingsRowPadX).
			Children(func() {
				ui.Text(c, saveErr).
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemeDanger(c))
			})
	}
}

// renderProvidersCard 渲染全部模型供应商单卡片折叠行列表；lib 用于在供应商行里展示各自的模型。
func renderProvidersCard(c *ui.Context, st *SettingsState, lib *modelsLibraryView) {
	settingsWindowMu.Lock()
	var win *mygo.Window
	if settingsWindow != nil && settingsWindow.state == st {
		win = settingsWindow.win
	}
	settingsWindowMu.Unlock()
	if win != nil {
		st.mu.Lock()
		install := st.providerRows.focusWindow != win
		st.providerRows.focusWindow = win
		st.mu.Unlock()
		if install {
			win.OnFocus(st.recheckProvidersOnFocus)
		}
	}
	st.mu.Lock()
	load := !st.providerRows.loaded && !st.providerRows.loading
	loadErr := st.providerRows.loadErr
	st.mu.Unlock()
	if load {
		st.loadProviderRows()
	}
	agents := cliProviderListEntries(st, lib)
	api := apiProviderListEntries(st)
	st.mu.Lock()
	presetErr := st.presets.saveErr
	st.mu.Unlock()
	for i := range api {
		if api[i].Kind == "preset" {
			switch api[i].ID {
			case "anthropic", "deepseek", "zai", "z.ai", "meta", "openai":
				api[i].Name = providerRowName(st, api[i].ID)
			}
		}
	}
	for _, entries := range [][]providerListEntry{agents, api} {
		sort.SliceStable(entries, func(i, j int) bool {
			if entries[i].Enabled != entries[j].Enabled {
				return entries[i].Enabled
			}
			return entries[i].Name < entries[j].Name
		})
	}
	if loadErr != "" {
		ui.Text(c, loadErr).FontSize(uiFont(TypeFootnote)).TextColor(ThemeDanger(c)).Margin(Space2, settingsRowPadX)
	}
	for groupIndex, group := range []struct {
		title   string
		entries []providerListEntry
	}{{"智能体", agents}, {"API Key", api}} {
		renderSkillGroupTitle(c, group.title)
		if groupIndex == 1 && presetErr != "" {
			ui.Text(c, presetErr).FontSize(uiFont(TypeFootnote)).TextColor(ThemeDanger(c)).Margin(Space2, settingsRowPadX)
		}
		for i, entry := range group.entries {
			if i > 0 {
				renderSettingsDivider(c, fmt.Sprintf("div-provider-list-%d-%d", groupIndex, i))
			}
			e := entry
			rowKey := "provider-row-" + e.Kind + "-" + e.ID
			row := applySettingsRowFocus(c, ui.Row(c.Key(rowKey)).
				Label(e.Name).Padding(Space3, settingsRowPadX).MinHeight(settingsRowMinHeight).AlignItems(ui.Center).Gap(Space4).FillWidth(), rowKey)
			if row.Hovered() {
				row.Background(ThemeHover(c))
			}
			childClicked := false
			row.Children(func() {
				renderProviderRowIcon(c, e.ID, 16)
				ui.Text(c, e.Name).FontSize(uiFont(TypeBody)).FontWeight(500).TextColor(ThemePrimaryText(c))
				ui.Box(c.Key("provider-list-spacer-" + e.Kind + "-" + e.ID)).Grow(1)
				ui.Text(c, e.Status).FontSize(uiFont(TypeCaption)).TextColor(ThemeSecondaryText(c))
				if e.Kind == "cli" {
					if route, known := magpieAgentRouteValue(st, e.ID); known && (route == "仅 mcode" || route == "全局") {
						ui.Text(c, "路由 · Magpie").FontSize(uiFont(TypeCaption)).TextColor(ThemeAccent(c))
					}
				}
				if e.Locked {
					ui.Text(c, "锁定").FontSize(uiFont(TypeCaption)).TextColor(ThemeSecondaryText(c))
				}
				if e.CanToggle {
					value := e.Enabled
					if e.Locked {
						SwitchDisabled(c, "sw-provider-list-"+e.Kind+"-"+e.ID, value, "启用 "+e.Name)
					} else {
						Switch(c, "sw-provider-list-"+e.Kind+"-"+e.ID, &value, "启用 "+e.Name, func(on bool) {
							childClicked = true
							if e.Toggle != nil {
								e.Toggle(on)
							}
						})
					}
				}
				ui.Icon(c, IconChevronRight()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
			})
			if row.Clicked() && !childClicked {
				openProviderSheet(st, e.Kind, e.ID)
			}
		}
	}
	if len(agents)+len(api) > 0 || loadErr != "" {
		renderSettingsDivider(c, "div-provider-add")
	}
	row := ui.Row(c.Key("row-provider-add")).Label("添加供应商").Padding(Space3, settingsRowPadX).MinHeight(settingsRowMinHeight).AlignItems(ui.Center).FillWidth()
	if row.Hovered() {
		row.Background(ThemeHover(c))
	}
	row.Children(func() {
		ui.Text(c, "添加供应商").FontSize(uiFont(TypeBody)).TextColor(ThemePrimaryText(c))
		ui.Box(c.Key("provider-add-spacer")).Grow(1)
		ui.Icon(c, IconChevronRight()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
	})
	if row.Clicked() {
		openSettingsSheet(st, "provider-add", "")
	}
}

func (st *SettingsState) recheckProvidersOnFocus() {
	st.mu.Lock()
	recheck := st.providerRows.recheckOnFocus
	if recheck {
		st.providerRows.recheckOnFocus = false
		if !st.providerRows.loading {
			st.providerRows.loaded = false
		}
	}
	st.mu.Unlock()
	if recheck {
		st.invalidate()
	}
}

// providerDetailState 是 renderProviderSingleRow 已经取好的行状态快照，供详情页签渲染使用。
type providerDetailState struct {
	models     ProviderInfo
	hasModels  bool
	quota      []ProviderQuotaWindow
	quotaErr   string
	loading    bool
	updateBusy bool
	updateMsg  string
}

// cliProviderSheetSections 返回 CLI 供应商面板的分节定义：按供应商实际存在的节生成，
// 顺序固定为账号、模型、命令行。锁状态与详情快照在这里算好，由各节闭包捕获。
func cliProviderSheetSections(st *SettingsState, id string, lib *modelsLibraryView) []providerSheetSection {
	// 供应商被锁定时，面板内所有会写配置的控件统一禁用；只读展示照常渲染。
	locked := st.isLocked(id + "-enabled")
	st.mu.Lock()
	view := providerDetailState{quota: st.providerRows.quota[id], quotaErr: st.providerRows.quotaErr[id], loading: st.providerRows.loading, updateBusy: st.providerRows.updateBusy[id], updateMsg: st.providerRows.updateMsg[id]}
	st.mu.Unlock()
	view.models, view.hasModels = providerModelsFor(lib, id)
	if id == "agy" {
		return agyProviderSheetSections(st)
	}

	sections := make([]providerSheetSection, 0, 3)
	if providerHasAccountTab(st, id, view) || id == "droid" || id == "copilot" {
		sections = append(sections, providerSheetSection{
			ID:    "sec-provider-account",
			Title: "账号",
			Render: func(c *ui.Context, st *SettingsState) {
				renderSettingsSection(c, "sec-provider-account", "", func() {
					renderProviderAccountTab(c, st, id, view, locked)
					if isCLIProviderID(id) {
						renderProviderCLIExpanded(c, st, id, true, locked)
					}
				})
			},
		})
	}
	routeAgent := false
	switch id {
	case "claude", "codex", "pi":
		connected, _ := magpieConnected(st)
		// 已连接网关，或该 agent 的 Magpie 设置里已有端点、已写全局配置时
		// 才有路由可管：未连接、未登录且目录为空的供应商不新增空的模型节。
		routeAgent = connected || agentMagpieRouteConfigured(st, id)
	}
	if view.hasModels || routeAgent {
		sections = append(sections, providerSheetSection{
			ID:    "sec-provider-models",
			Title: "模型",
			Render: func(c *ui.Context, st *SettingsState) {
				renderSettingsSection(c, "sec-provider-models", "", func() {
					if routeAgent {
						value, known := magpieAgentRouteValue(st, id)
						if !known {
							value = "…"
						}
						renderSettingRow(c, "row-provider-magpie", "Magpie 接入", func() {
							btn := ui.ButtonBase(c.Key("row-provider-magpie-open")).
								Label("Magpie 接入").
								Padding(Space1, Space2).
								Radius(RadiusSM)
							if btn.Hovered() {
								btn.Background(ThemeHover(c))
							} else {
								btn.Background(ui.Transparent)
							}
							if btn.Clicked() {
								openMagpieSettingsPage(st)
							}
							btn.Children(func() {
								ui.Row(c.Key("row-provider-magpie-open-body")).
									AlignItems(ui.Center).
									Gap(Space1).
									Children(func() {
										ui.Text(c, value).FontSize(uiFont(TypeBody)).TextColor(ThemeSecondaryText(c))
										ui.Icon(c, IconChevronRight()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
									})
							})
						})
					}
					if len(view.models.Models) == 0 {
						renderSettingRow(c, "provider-models-empty", "暂无模型", func() {})
						return
					}
					name := view.models.Name
					if name == "" {
						name = providerDisplayName(id)
					}
					ui.Text(c, "来自 "+name+" 的模型目录，只读；常用模型请在「收藏模型」里添加").
						FontSize(uiFont(TypeFootnote)).
						TextColor(ThemeSecondaryText(c)).
						Padding(settingsRowPadY, settingsRowPadX)
					renderSettingsDivider(c, "div-provider-models-note")
					for i, model := range view.models.Models {
						if i > 0 {
							renderSettingsDivider(c, fmt.Sprintf("div-provider-model-%d", i))
						}
						title := model.Name
						if strings.TrimSpace(title) == "" {
							title = model.ID
						}
						renderSettingRow(c, "provider-model-"+model.ID, title, func() {
							if strings.TrimSpace(model.ID) != strings.TrimSpace(title) {
								ui.Text(c, model.ID).FontSize(uiFont(TypeCaption)).TextColor(ThemeTertiaryText(c)).SingleLine()
							}
						})
					}
				})
			},
		})
	}
	if id != "agy" {
		sections = append(sections, providerSheetSection{
			ID:       "sec-provider-cli",
			Title:    "命令行",
			Advanced: true,
			Render: func(c *ui.Context, st *SettingsState) {
				renderSettingsSection(c, "sec-provider-cli", "", func() {
					renderProviderAdvancedTab(c, st, id, view, locked)
				})
			},
		})
	}
	return sections
}

func openMagpieSettingsPage(st *SettingsState) {
	st.mu.Lock()
	loadProviders, disposeEditor := st.switchSettingsPageLocked("magpie")
	st.mu.Unlock()
	if disposeEditor != nil {
		disposeEditor()
	}
	st.loadSettingsPageData(loadProviders)
}

// renderCLIProviderSheetSections 把各节整块堆叠，仅供测试与调试；生产路径走 renderProviderSheetBody。
func renderCLIProviderSheetSections(c *ui.Context, st *SettingsState, id string, lib *modelsLibraryView) {
	for _, def := range cliProviderSheetSections(st, id, lib) {
		def.Render(c, st)
	}
}

// providerHasTrayQuotaSwitch 判断该 Provider 是否有「菜单栏额度」开关（与菜单栏额度面板支持的直连供应商一致）。
func providerHasTrayQuotaSwitch(id string) bool {
	switch id {
	case "claude", "agy", "zai", "cline", "droid":
		return true
	}
	return false
}

// providerHasAccountTab 判断「账号与额度」页签是否有内容：原生 Provider 始终有账号区或说明行；
// CLI harness 只有额度、余额或菜单栏额度开关之一时才显示。
func providerHasAccountTab(st *SettingsState, id string, view providerDetailState) bool {
	if !isCLIProviderID(id) {
		return true
	}
	if providerHasTrayQuotaSwitch(id) || len(view.quota) > 0 || view.quotaErr != "" {
		return true
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.providerRows.credits == nil {
		return false
	}
	_, ok := st.providerRows.credits[id]
	return ok
}

// renderProviderTrayQuotaRow 渲染「菜单栏额度」行：左侧文字、右侧开关；没有该开关的 Provider 不渲染。
func renderProviderTrayQuotaRow(c *ui.Context, st *SettingsState, id string) {
	renderProviderTrayQuotaRowLocked(c, st, id, st.isLocked(id+"-enabled"))
}

// renderProviderTrayQuotaRowLocked 按调用方算好的锁定状态渲染「菜单栏额度」行。
func renderProviderTrayQuotaRowLocked(c *ui.Context, st *SettingsState, id string, locked bool) {
	if !providerHasTrayQuotaSwitch(id) {
		return
	}
	st.mu.Lock()
	shown := trayQuotaShown(st.cfg, id)
	st.mu.Unlock()
	renderSettingRow(c, "row-provider-tray-quota", "菜单栏额度", func() {
		if locked {
			SwitchDisabled(c, "sw-tray-quota-"+id, shown, "在菜单栏显示额度").Shrink(0)
		} else {
			Switch(c, "sw-tray-quota-"+id, &shown, "在菜单栏显示额度", func(on bool) {
				setTrayQuotaShown(st, id, on)
			}).Shrink(0)
		}
	})
}

// renderProviderQuotaPanel 渲染 claude/codex 的限额面板（Codex 经 Magpie 时按账号分组）。
func renderProviderQuotaPanel(c *ui.Context, st *SettingsState, id string, quotaWindows []ProviderQuotaWindow, quotaErr string, loading bool) {
	if len(quotaWindows) == 0 {
		return
	}
	st.mu.Lock()
	localPlan := ""
	if st.providerRows.account != nil {
		localPlan = st.providerRows.account[id].Plan
	}
	st.mu.Unlock()
	if id == "codex" && quotaWindowsFromMagpie(quotaWindows) {
		// Codex 经 Magpie 时额度属于网关里的账号：按账号分组展示，每组用卡片自带
		// 的套餐与读取时间，不能拼本机登录的套餐。
		for i, group := range groupQuotaWindowsByAccount(quotaWindows) {
			limits := make([]planLimit, 0, len(group.windows))
			for _, w := range group.windows {
				limits = append(limits, planLimit{
					Label:       w.Label,
					UsedPercent: w.UsedPercent,
					ResetsAt:    w.ResetsAt,
					RefreshedAt: w.ReadAt,
				})
			}
			planName := group.account
			if p := quotaGroupPlan(group.windows); p != "" {
				planName += " · " + p
			}
			withPlanLimitsErr(limits, quotaErr)
			renderPlanLimitsView(c, fmt.Sprintf("limits-%s-%d", id, i), planName, limits, 0, loading)
		}
		return
	}
	limits := make([]planLimit, 0, len(quotaWindows))
	for _, w := range quotaWindows {
		limits = append(limits, planLimit{
			Label:       w.Label,
			UsedPercent: w.UsedPercent,
			ResetsAt:    w.ResetsAt,
			RefreshedAt: w.ReadAt,
		})
	}
	withPlanLimitsErr(limits, quotaErr)
	renderPlanLimitsView(c, "limits-"+id, localPlan, limits, 0, loading)
}

// renderProviderCheckUpdateRow 渲染 claude/codex 的「检查更新」行与结果文案。
func renderProviderCheckUpdateRow(c *ui.Context, st *SettingsState, id string, updateBusy bool, updateMsg string) {
	ui.Row(c.Key("row-check-update-"+id)).
		Padding(settingsRowPadY, settingsRowPadX).
		AlignItems(ui.Center).
		Gap(Space4).
		Children(func() {
			checkBtn := ui.ButtonBase(c.Key("btn-check-update-"+id)).
				Label("检查更新").
				Radius(RadiusSM).
				Padding(settingsPillPadX, Space4).
				Background(ThemeHover(c))
			if updateBusy {
				checkBtn.Disabled(true)
			} else if checkBtn.Hovered() {
				checkBtn.Background(ThemeOverlay(c, 0.12))
			}
			if checkBtn.Clicked() && !updateBusy {
				st.checkProviderUpdate(id)
			}
			checkBtn.Children(func() {
				btnLabel := "检查更新"
				if updateBusy {
					btnLabel = "正在检查更新…"
				}
				ui.Text(c, btnLabel).
					FontSize(uiFont(TypeBody)).
					TextColor(ThemePrimaryText(c))
			})

			if updateMsg != "" {
				msgColor := ThemeSecondaryText(c)
				if updateMsg == "暂不支持检查更新" {
					msgColor = ThemeSecondaryText(c)
				} else if strings.Contains(updateMsg, "失败") || strings.Contains(updateMsg, "error") {
					msgColor = ThemeDanger(c)
				} else if strings.Contains(updateMsg, "新版本") {
					msgColor = ThemeAccent(c)
				}
				ui.Text(c, updateMsg).
					FontSize(uiFont(TypeFootnote)).
					TextColor(msgColor)
			}
		})
}

// renderProviderAccountTab 渲染「账号」节：额度、账号、菜单栏额度开关与余额面板；
// locked 为真时写配置的开关禁用，只读展示照常渲染。
func renderProviderAccountTab(c *ui.Context, st *SettingsState, id string, view providerDetailState, locked bool) {
	if id == "agy" {
		// (d) agy 刷新额度行
		renderAgyRefreshQuota(c, st)
		// (c) 多账号区
		renderProviderAccountsSection(c, st, id)
		renderProviderTrayQuotaRowLocked(c, st, id, locked)
		return
	}
	if len(view.quota) == 0 && view.quotaErr != "" {
		ui.Column(c.Key("provider-quota-status-"+id)).FillWidth().Padding(settingsRowPadY, settingsRowPadX).Children(func() {
			renderUsageStatusLine(c, usageStatusForData(view.quotaErr, false, time.Time{}, time.Now()), 11)
		})
	}
	if !isCLIProviderID(id) {
		// (a) 限额面板
		renderProviderQuotaPanel(c, st, id, view.quota, view.quotaErr, view.loading)
		// (c) 多账号区
		renderProviderAccountsSection(c, st, id)
	}
	renderProviderTrayQuotaRowLocked(c, st, id, locked)
	// 余额面板只在「账号与额度」页签出现一次。
	renderProviderBalancePanel(c, st, id)
}

// renderProviderAdvancedTab 渲染「高级」页签：检查更新、可执行文件路径与 Magpie 全局配置。
func renderProviderAdvancedTab(c *ui.Context, st *SettingsState, id string, view providerDetailState, locked bool) {
	if id == "agy" {
		// (e) 默认收起的「账号调度」子区
		renderAgyRowAdvancedSections(c, st)
		return
	}
	if isCLIProviderID(id) {
		// CLI harness 的路径、版本与认证方式。
		renderProviderCLIExpanded(c, st, id, false, locked)
		return
	}
	// (e) 检查更新按钮
	renderProviderCheckUpdateRow(c, st, id, view.updateBusy, view.updateMsg)
	// (f) 可执行文件路径
	renderNativeProviderBinRow(c, st, id)
}

// renderProviderAccountsSection 渲染指定供应商的账号小节。
func renderProviderAccountsSection(c *ui.Context, st *SettingsState, id string) {
	if id == "agy" {
		renderAgyAccountsSection(c, st)
		return
	}

	st.mu.Lock()
	var acc ProviderAccountInfo
	hasAcc := false
	if st.providerRows.account != nil {
		acc, hasAcc = st.providerRows.account[id]
	}
	st.mu.Unlock()

	if !hasAcc || (acc.Email == "" && acc.Plan == "") {
		return
	}

	ui.Column(c.Key("sec-accounts-" + id)).Children(func() {
		ui.Row(c.Key("row-acc-"+id)).
			Padding(Space4, settingsRowPadX).
			AlignItems(ui.Center).
			Gap(Space4).
			Children(func() {
				ui.Column(c.Key("col-acc-info-" + id)).
					Grow(1).
					Gap(Space0).
					Children(func() {
						email := acc.Email
						if email == "" {
							email = "已连接"
						}
						ui.Text(c, email).
							FontSize(uiFont(TypeBody)).
							FontWeight(500).
							TextColor(ThemePrimaryText(c)).
							SingleLine()
						if acc.Plan != "" {
							ui.Text(c, acc.Plan).
								FontSize(uiFont(TypeCaption)).
								TextColor(ThemeSecondaryText(c)).
								SingleLine()
						}
					})

				// 本机登录标签：这里的账号/套餐来自本机 Codex/Claude 登录，
				// 与 Magpie 网关额度里的账号无关，不能看成同一个账号。
				ui.Row(c.Key("badge-current-"+id)).
					Padding(Space0, Space2).
					Radius(RadiusXS).
					Background(ThemeAccent(c).Alpha(0.12)).
					Children(func() {
						ui.Text(c, "本机登录").
							FontSize(uiFont(TypeCaption)).
							FontWeight(500).
							TextColor(ThemeAccent(c))
					})
			})
	})
}

// renderNativeProviderBinRow 渲染内置 Claude / Codex 供应商展开区里的「可执行文件路径」行：
// row key、输入框 key 与 Label 保持不变。
func renderNativeProviderBinRow(c *ui.Context, st *SettingsState, id string) {
	locked := st.isLocked(id + "-bin")
	switch id {
	case "claude":
		renderSettingRow(c, "row-claude-bin", "Claude 可执行文件路径", func() {
			renderNativeProviderBinInput(c, st, &st.cfg.Providers.Claude.Bin, "input-claude-bin", "可执行文件路径 Claude", locked)
		})
	case "codex":
		renderSettingRow(c, "row-codex-bin", "Codex 可执行文件路径", func() {
			renderNativeProviderBinInput(c, st, &st.cfg.Providers.Codex.Bin, "input-codex-bin", "可执行文件路径 Codex", locked)
		})
	}
}

func renderNativeProviderBinInput(c *ui.Context, st *SettingsState, bin *string, key, label string, locked bool) {
	TextField(c, TextFieldProps{
		Key:         key,
		Value:       bin,
		Label:       label,
		Placeholder: "留空表示从 PATH 查找",
		Width:       280,
		Disabled:    locked,
		ReadOnly:    locked,
		OnChange: func(string) {
			st.save()
		},
	})
}
