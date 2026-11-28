package mcodeui

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math"
	"mcode/internal/fsutil"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mcode/internal/config"
)

// uiStateFileName 是界面状态文件名，保存在 mcode 数据目录（默认 ~/.mcode）下。
const uiStateFileName = "ui-state.json"

var uiStateDataDir struct {
	sync.RWMutex
	dir string
}

// SetUIStateDataDir 设置界面状态使用的数据目录；空值恢复默认路径。
func SetUIStateDataDir(dir string) {
	uiStateDataDir.Lock()
	uiStateDataDir.dir = dir
	uiStateDataDir.Unlock()
}

// UIState 保存需要在重启后恢复的界面状态。
// SidePaneWidths 是 map，因此结构体不可比较；需要比较时用 reflect.DeepEqual 或逐字段比较。
type UIState struct {
	SidebarWidth  float32 `json:"sidebar_width"`
	SidebarHidden bool    `json:"sidebar_hidden"`
	// TerminalDockHeight 是会话底部终端 dock 的高度（pt）；0 表示没有记录，按默认高度。
	TerminalDockHeight float32 `json:"terminal_dock_height"`
	// SidePaneWidths 是主区右侧分栏面板的宽度（面板 ID -> 宽度，pt）；
	// 没有记录的面板首次创建时用默认宽度。
	SidePaneWidths    map[string]float32             `json:"side_pane_widths,omitempty"`
	InspectorSessions map[string]InspectorPreference `json:"inspector_sessions,omitempty"`
	// LastThreadID 是主窗口上次选中的会话，重启后据此回到退出前的会话。
	LastThreadID string `json:"last_thread,omitempty"`
}

// lastThreadMu 保护最近一次记录的会话 ID，以及尚未写进 ui-state.json 的待写会话。
// 会话 ID 是独立键：合并写盘时只按它覆盖磁盘上的 last_thread，不牵动侧栏字段。
var (
	lastThreadMu      sync.Mutex
	lastThreadID      string
	lastThreadPending bool
)

// rememberSelectedThread 记录主窗口选中的会话；threadID 为空表示回到新会话首页，清掉记录。
func rememberSelectedThread(threadID string) {
	lastThreadMu.Lock()
	lastThreadID = threadID
	lastThreadPending = true
	lastThreadMu.Unlock()
	// 写盘期间的记录由这一轮写者自行补写，否则它要靠下一次切换才会落盘。
	lastModelSelectionMu.Lock()
	lastSelectionWritesPending++
	lastModelSelectionMu.Unlock()
	writeLastSelections(nil)
}

// lastSelectedThread 返回上次记录的会话 ID。
func lastSelectedThread() string {
	lastThreadMu.Lock()
	defer lastThreadMu.Unlock()
	return lastThreadID
}

// loadLastThread 读取 ui-state.json 中保存的上次会话。
func loadLastThread() string {
	file, ok := loadUIStateFile()
	if !ok {
		return ""
	}
	return strings.TrimSpace(file.LastThreadID)
}

type InspectorPreference struct {
	Open   bool    `json:"open"`
	Active string  `json:"active"`
	Width  float32 `json:"width"`

	// 文件树定位：展开的目录、活动文件与滚动锚点都只存工作区相对路径，不存像素或文件正文。
	FileExpanded []string `json:"files_expanded,omitempty"`
	FileActive   string   `json:"file_active,omitempty"`
	FileAnchor   string   `json:"file_anchor,omitempty"`

	// 文件树排序偏好：零值为名称升序、目录置顶。
	FileSort      string `json:"file_sort,omitempty"`
	FileSortDesc  bool   `json:"file_sort_desc,omitempty"`
	FileDirsMixed bool   `json:"file_dirs_mixed,omitempty"`
	// 文件导航栏宽度与忽略文件过滤：0 表示默认宽度，false 表示显示忽略文件。
	FileNavWidth    float32 `json:"file_nav_width,omitempty"`
	FileHideIgnored bool    `json:"file_hide_ignored,omitempty"`
}

// legacySidePaneDiffID 是合并进 Git Tab 前旧版「改动」面板的持久化 ID，只在读盘兼容时映射为 sidePaneGitID。
const legacySidePaneDiffID = "diff"

func normalizeInspectorPreference(pref InspectorPreference) InspectorPreference {
	switch pref.Active {
	case sidePaneFileID, sidePaneGitID, sidePanePreviewID:
	case legacySidePaneDiffID:
		pref.Active = sidePaneGitID
	default:
		pref.Active = sidePaneFileID
	}
	if pref.Width <= 0 || math.IsNaN(float64(pref.Width)) || math.IsInf(float64(pref.Width), 0) {
		pref.Width = inspectorDefaultWidth
	} else {
		pref.Width = max(pref.Width, inspectorMinWidth)
	}
	pref.FileExpanded = normalizeInspectorPaths(pref.FileExpanded)
	pref.FileActive = normalizeInspectorPath(pref.FileActive)
	pref.FileAnchor = normalizeInspectorPath(pref.FileAnchor)
	pref.FileSort = string(normalizeFileSortField(pref.FileSort))
	if pref.FileNavWidth < 0 || math.IsNaN(float64(pref.FileNavWidth)) || math.IsInf(float64(pref.FileNavWidth), 0) {
		pref.FileNavWidth = 0
	}
	return pref
}

// normalizeInspectorPath 只保留工作区相对路径；绝对路径与含 .. 的路径直接丢弃。
func normalizeInspectorPath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "./")
	if path == "" || strings.HasPrefix(path, "/") || strings.Contains(path, `\`) {
		return ""
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return ""
		}
	}
	return path
}

func normalizeInspectorPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(paths))
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		path = normalizeInspectorPath(path)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		result = append(result, path)
	}
	if len(result) == 0 {
		return nil
	}
	sort.Strings(result)
	return result
}

// nil 和空表都只表示没有更新，删除必须提交显式操作。
func mergeInspectorPreferences(base, updates map[string]InspectorPreference) map[string]InspectorPreference {
	var result map[string]InspectorPreference
	for _, source := range []map[string]InspectorPreference{base, updates} {
		for tid, pref := range source {
			if tid == "" {
				continue
			}
			if result == nil {
				result = make(map[string]InspectorPreference)
			}
			result[tid] = normalizeInspectorPreference(pref)
		}
	}
	return result
}

type pendingInspectorPreference struct {
	Preference InspectorPreference
	Delete     bool
	Generation uint64
}

var (
	inspectorPreferencePendingPath string
	inspectorPreferencePending     map[string]pendingInspectorPreference
	inspectorPreferenceGeneration  uint64
)

// 与其他界面字段共用串行写者；锁内只登记更新，不读盘。
func persistInspectorPreference(tid string, change pendingInspectorPreference) {
	if tid == "" {
		return
	}
	lastModelSelectionMu.Lock()
	path := uiStatePath()
	if inspectorPreferencePendingPath != path {
		inspectorPreferencePendingPath = path
		inspectorPreferencePending = nil
	}
	if inspectorPreferencePending == nil {
		inspectorPreferencePending = make(map[string]pendingInspectorPreference)
	}
	inspectorPreferenceGeneration++
	change.Generation = inspectorPreferenceGeneration
	inspectorPreferencePending[tid] = change
	lastSelectionWritesPending++
	lastModelSelectionMu.Unlock()
	writeLastSelections(nil)
}

type reviewPreference struct {
	SelectedPath string   `json:"selected_path,omitempty"`
	ReadKeys     []string `json:"read_keys,omitempty"`
}

type pendingReviewPreference struct {
	preference reviewPreference
	generation uint64
}

var (
	reviewPreferencePendingPath string
	reviewPreferencePending     map[string]pendingReviewPreference
	reviewPreferenceGeneration  uint64
)

func normalizeReviewPreference(pref reviewPreference) reviewPreference {
	pref.SelectedPath = normalizeInspectorPath(pref.SelectedPath)
	keys := make([]string, 0, min(len(pref.ReadKeys), reviewReadMarkerLimit))
	seen := make(map[string]bool)
	for i := len(pref.ReadKeys) - 1; i >= 0 && len(keys) < reviewReadMarkerLimit; i-- {
		key := pref.ReadKeys[i]
		if len(key) != sha256.Size*2 || strings.Trim(key, "0123456789abcdef") != "" || seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}
	slices.Reverse(keys)
	pref.ReadKeys = keys
	return pref
}

func loadReviewPreference(s *chatState, dp *diffPanelState, tid string) {
	diffPanelMu.Lock()
	loaded := dp.prefLoaded
	dp.prefLoaded = true
	diffPanelMu.Unlock()
	if loaded || tid == "" || !uiStatePersistenceEnabled(s) {
		return
	}
	file, _ := loadUIStateFile()
	pref := normalizeReviewPreference(file.ReviewSessions[tid])
	diffPanelMu.Lock()
	dp.readKeys = pref.ReadKeys
	if dp.scope != "pr" && dp.selectedPath == "" && dp.pendingPath == "" {
		dp.selectedPath = pref.SelectedPath
	}
	diffPanelMu.Unlock()
}

func rememberReviewPreference(s *chatState, dp *diffPanelState, tid string) {
	if tid == "" || !uiStatePersistenceEnabled(s) {
		return
	}
	diffPanelMu.Lock()
	if dp.scope == "pr" {
		diffPanelMu.Unlock()
		return
	}
	pref := normalizeReviewPreference(reviewPreference{SelectedPath: dp.selectedPath, ReadKeys: dp.readKeys})
	diffPanelMu.Unlock()
	lastModelSelectionMu.Lock()
	path := uiStatePath()
	if reviewPreferencePendingPath != path {
		reviewPreferencePendingPath, reviewPreferencePending = path, nil
	}
	if reviewPreferencePending == nil {
		reviewPreferencePending = make(map[string]pendingReviewPreference)
	}
	reviewPreferenceGeneration++
	reviewPreferencePending[tid] = pendingReviewPreference{preference: pref, generation: reviewPreferenceGeneration}
	lastSelectionWritesPending++
	lastModelSelectionMu.Unlock()
	writeLastSelections(nil)
}

// uiStateFile 是 ui-state.json 的完整文件结构：在 UIState 之外承载带切片字段的持久化数据，
// 与 UIState 读写同一文件、同一套落盘逻辑。
type uiStateFile struct {
	UIState
	ReviewSessions   map[string]reviewPreference `json:"review_sessions,omitempty"`
	TasksDetailWidth float32                     `json:"tasks_detail_width,omitempty"`
	// ProjectOrder 是侧边栏拖拽重排后的项目顺序（项目 ID 列表）；空表示按项目默认顺序。
	ProjectOrder []string `json:"project_order,omitempty"`
	// ProjectActivation 是项目激活状态的覆盖表（项目 ID -> 是否激活）：键为项目 ID，false 表示停用；
	// 没有条目的项目跟随全局默认（mcode 无全局开关，默认视为激活）。
	ProjectActivation map[string]bool `json:"project_activation,omitempty"`
	// ContinueDismissed 记录被忽略的继续胶囊（会话 ID -> 忽略时最后一条消息 ID）：
	// 最后一条消息 ID 变化后该记录自动失效，胶囊重新出现。
	ContinueDismissed map[string]string `json:"continue_dismissed,omitempty"`
	// SwarmFailedSeen 记录已查看的失败子代理（主会话 ID -> 子代理 ID -> 查看时的 FinishedAt）：
	// FinishedAt 变化后该记录自动失效，编排按钮的失败角标重新计数。
	SwarmFailedSeen map[string]map[string]string `json:"swarm_failed_seen,omitempty"`
	// PreviewDesignBees 记录本地设计稿路径 -> 设计子代理会话 ID，重启后据此恢复定稿入口。
	PreviewDesignBees map[string]string `json:"preview_design_bees,omitempty"`
	Onboarding        string            `json:"onboarding,omitempty"`
	// LastModel 是项目场景下最近一次在模型选择组件中确认的模型选择（provider + model + 推理强度 + 快速模式），
	// 新建项目会话或组件初始化时优先使用该记录；零值表示还没有记录。
	LastModel ModelSelection `json:"last_model,omitempty"`
	// LastModelQuick 是快速对话（projectID 为空）最近一次确认的模型选择；nil 表示还没有记录。
	LastModelQuick *ModelSelection `json:"last_model_quick,omitempty"`
	// LastSwarm 是项目场景下最近一次确认的编排开关与执行侧选择，新建项目会话时沿用；nil 表示还没有记录。
	// 落盘键名是 last_swarm，每次保存只写新键。
	LastSwarm *LastSwarmSelection `json:"last_swarm,omitempty"`
	// LastSwarmQuick 是快速对话（projectID 为空）最近一次确认的编排开关与执行侧选择；nil 表示还没有记录。
	LastSwarmQuick *LastSwarmSelection `json:"last_swarm_quick,omitempty"`
	// LegacyLastSwarm 是改名前的旧键 last_hydra，只在读取旧文件做迁移时使用；保存时始终写回 nil。
	LegacyLastSwarm *LastSwarmSelection `json:"last_hydra,omitempty"`
	// ProjectModels 是按项目记住的模型与交互模式选择（项目键 -> 选择）：键为项目 ID，
	// 没有项目归属的新会话用 projectSelectionNoneKey。新建会话时优先按所属项目恢复。
	ProjectModels map[string]ProjectModelSelection `json:"project_models,omitempty"`
	// LastCloneDir 是「从 GitHub 克隆」面板上次选择的父目录。
	LastCloneDir string `json:"last_clone_dir,omitempty"`
}

// ProjectModelSelection 是某个项目下记住的模型选择。
// Provider 与 Model 都为空表示该项目还没有记录过模型，新建会话时退回全局记忆。
type ProjectModelSelection struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
	Fast     bool   `json:"fast,omitempty"`
}

// LastSwarmSelection 记录 Swarm 开关与执行侧选择；两项分别记录，nil 表示该项从未设置过（沿用默认）。
type LastSwarmSelection struct {
	Enabled *bool           `json:"enabled,omitempty"`
	Bees    *ModelSelection `json:"heads,omitempty"` // Provider 与 Model 都为空表示跟随主导；Fast 不用，按模型偏好走；落盘键名沿用旧名 heads
}

// WithUIStatePersistence 开启界面状态持久化：启动时恢复上次保存的侧边栏宽度与显隐，
// 拖拽分栏或切换侧边栏后写回 ui-state.json。默认关闭，只有真实应用的主窗口显式开启，
// 避免多个窗口与测试之间通过共享数据目录互相污染。
func WithUIStatePersistence() Option {
	return func(s *chatState) {
		if s == nil {
			return
		}
		// 选项可能在 chatUIState 首次注册之前应用，这里取不到就创建。
		st := getChatUIState(s)
		if st == nil {
			return
		}
		chatUIMu.Lock()
		st.persist = true
		chatUIMu.Unlock()
	}
}

// clampSidebarWidth 把侧边栏宽度夹取到 [SidebarMinWidth, SidebarMaxWidth]。
// 非正值（配置文件缺字段）回落到默认宽度。
func clampSidebarWidth(width float32) float32 {
	if width <= 0 {
		return SidebarDefaultWidth
	}
	if width < SidebarMinWidth {
		return SidebarMinWidth
	}
	if width > SidebarMaxWidth {
		return SidebarMaxWidth
	}
	return width
}

// mainAreaWidth 计算主工作区可用宽度：侧边栏隐藏时占满窗口，显示时减去侧边栏与 1pt 分隔线。
func mainAreaWidth(windowWidth, sidebarWidth float32, sidebarHidden bool) float32 {
	if windowWidth < 0 {
		windowWidth = 0
	}
	if sidebarHidden {
		return windowWidth
	}
	return max(windowWidth-sidebarWidth-1, 0)
}

// uiStatePath 返回界面状态文件路径，沿用 model.go 中 resolveDataDirFallback 的取值逻辑（MCODE_DATA 或 ~/.mcode）。
func uiStatePath() string {
	uiStateDataDir.RLock()
	dir := uiStateDataDir.dir
	uiStateDataDir.RUnlock()
	if dir == "" {
		dir = resolveDataDirFallback()
	}
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, uiStateFileName)
}

// loadUIStateFile 读取 ui-state.json 的完整内容；文件缺失或损坏时返回 false。
func loadUIStateFile() (uiStateFile, bool) {
	parsed, ok, _ := loadUIStateFileStrict()
	return parsed, ok
}

// loadUIStateFileStrict 与 loadUIStateFile 相同，但能区分“文件不存在”与“文件存在但解析失败”：
// exists 为文件是否存在，err 为解析错误。写盘前用它判断能否安全地以磁盘内容为底做合并，
// 避免把损坏文件整体覆盖成零值。
func loadUIStateFileStrict() (uiStateFile, bool, error) {
	path := uiStatePath()
	if path == "" {
		return uiStateFile{}, false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return uiStateFile{}, false, nil
		}
		return uiStateFile{}, true, err
	}
	var parsed uiStateFile
	if err := json.Unmarshal(data, &parsed); err != nil {
		return uiStateFile{}, true, err
	}
	return parsed, true, nil
}

// loadUIState 读取界面状态文件；文件缺失或损坏时返回默认状态。
func loadUIState() UIState {
	st := UIState{SidebarWidth: SidebarDefaultWidth}
	file, ok := loadUIStateFile()
	if !ok {
		return st
	}
	st.SidebarWidth = clampSidebarWidth(file.SidebarWidth)
	st.SidebarHidden = file.SidebarHidden
	st.TerminalDockHeight = file.TerminalDockHeight
	st.SidePaneWidths = file.SidePaneWidths
	st.InspectorSessions = mergeInspectorPreferences(nil, file.InspectorSessions)
	st.LastThreadID = strings.TrimSpace(file.LastThreadID)
	return st
}

// loadProjectOrder 读取 ui-state.json 中保存的项目顺序。
func loadProjectOrder() []string {
	file, ok := loadUIStateFile()
	if !ok {
		return nil
	}
	return file.ProjectOrder
}

// loadProjectActivation 读取 ui-state.json 中保存的项目激活覆盖表。
func loadProjectActivation() map[string]bool {
	file, ok := loadUIStateFile()
	if !ok {
		return nil
	}
	return file.ProjectActivation
}

// loadContinueDismissed 读取 ui-state.json 中保存的继续胶囊忽略记录（会话 ID -> 忽略时最后一条消息 ID）。
func loadContinueDismissed() map[string]string {
	file, ok := loadUIStateFile()
	if !ok {
		return nil
	}
	return file.ContinueDismissed
}

// saveContinueDismissed 保存继续胶囊的忽略记录：先写内存缓存并标脏，再交给统一的写者落盘。
// 写盘时以磁盘内容为底只覆盖该字段，不会冲掉模型记忆、项目顺序等其它字段。
func saveContinueDismissed(records map[string]string) {
	lastModelSelectionMu.Lock()
	ensureLastSelectionLoadedLocked()
	continueDismissedCache = copyStringMap(records)
	continueDismissedDirty = true
	continueDismissedGeneration++
	lastSelectionWritesPending++
	lastModelSelectionMu.Unlock()
	writeLastSelections(nil)
}

// persistContinueDismissed 在开启界面持久化的窗口里保存继续胶囊的忽略记录；测试与浮动面板窗口只改内存。
func persistContinueDismissed(s *chatState, records map[string]string) {
	if s == nil || !uiStatePersistenceEnabled(s) {
		return
	}
	saveContinueDismissed(records)
}

// loadLastCloneDir 读取 ui-state.json 中保存的上次克隆父目录。
func loadLastCloneDir() string {
	file, ok := loadUIStateFile()
	if !ok {
		return ""
	}
	return strings.TrimSpace(file.LastCloneDir)
}

// saveLastCloneDir 保存克隆父目录：先写内存缓存并标脏，再交给统一的写者落盘。
// 写盘时以磁盘内容为底只覆盖该字段，不会冲掉模型记忆、项目顺序等其它字段。
func saveLastCloneDir(dir string) {
	dir = strings.TrimSpace(dir)
	lastModelSelectionMu.Lock()
	ensureLastSelectionLoadedLocked()
	lastCloneDirCache = dir
	lastCloneDirDirty = true
	lastCloneDirGeneration++
	lastSelectionWritesPending++
	lastModelSelectionMu.Unlock()
	writeLastSelections(nil)
}

// persistLastCloneDir 在开启界面持久化的窗口里保存克隆父目录；测试与浮动面板窗口只改内存。
func persistLastCloneDir(s *chatState, dir string) {
	if s == nil || !uiStatePersistenceEnabled(s) {
		return
	}
	saveLastCloneDir(dir)
}

// loadSwarmFailedSeen 读取 ui-state.json 中保存的已查看失败子代理记录（主会话 ID -> 子代理 ID -> FinishedAt）。
func loadSwarmFailedSeen() map[string]map[string]string {
	file, ok := loadUIStateFile()
	if !ok {
		return nil
	}
	return copyNestedStringMap(file.SwarmFailedSeen)
}

// saveSwarmFailedSeen 保存已查看的失败子代理记录：先写内存缓存并标脏，再交给统一的写者落盘。
// 写盘时以磁盘内容为底只覆盖该字段，不会冲掉其它字段。
func saveSwarmFailedSeen(records map[string]map[string]string) {
	lastModelSelectionMu.Lock()
	ensureLastSelectionLoadedLocked()
	swarmFailedSeenCache = copyNestedStringMap(records)
	swarmFailedSeenDirty = true
	swarmFailedSeenGeneration++
	lastSelectionWritesPending++
	lastModelSelectionMu.Unlock()
	writeLastSelections(nil)
}

// loadPreviewDesignBees 读取 ui-state.json 中保存的本地设计稿与设计子代理映射。
func loadPreviewDesignBees() map[string]string {
	file, ok := loadUIStateFile()
	if !ok {
		return nil
	}
	return copyStringMap(file.PreviewDesignBees)
}

// savePreviewDesignBees 保存本地设计稿与设计子代理映射：先写内存缓存并标脏，再交给统一的写者落盘。
// 写盘时以磁盘内容为底只覆盖该字段，不会冲掉其它字段。
func savePreviewDesignBees(records map[string]string) {
	lastModelSelectionMu.Lock()
	ensureLastSelectionLoadedLocked()
	previewDesignBeesCache = copyStringMap(records)
	previewDesignBeesDirty = true
	previewDesignBeesGeneration++
	lastSelectionWritesPending++
	lastModelSelectionMu.Unlock()
	writeLastSelections(nil)
}

// 最近一次模型选择的内存缓存：避免每帧读盘，并在 persistUIState 覆盖写时保留该字段。
// 缓存按数据目录（ui-state.json 路径）区分，数据目录变化时重新读盘，避免读到别的目录的记录。
// Swarm 记录与模型选择共用同一缓存与锁。
var (
	lastModelSelectionMu       sync.Mutex
	lastModelSelectionPath     string
	lastModelSelectionLoaded   bool
	lastModelSelectionCache    *ModelSelection
	lastModelQuickCache        *ModelSelection
	tasksDetailWidthCache      float32
	tasksDetailWidthDirty      bool
	tasksDetailWidthGeneration uint64
	lastSwarmSelectionCache    *LastSwarmSelection
	lastSwarmQuickCache        *LastSwarmSelection
	// lastModelSelectionModTime 记录上次从磁盘加载或本进程写盘完成时 ui-state.json 的修改时间，
	// 用于发现其它进程对该文件的改动（为零值时表示上次加载时文件还不存在）。
	lastModelSelectionModTime time.Time
	// lastModelSelectionCheckedAt 记录上次对 ui-state.json 做 Stat 的时刻，用于限频：
	// 距上次 Stat 不足 lastSelectionStatInterval 时直接沿用缓存，不再每帧 Stat。
	lastModelSelectionCheckedAt time.Time
	// lastSelectionWritesPending 是已更新内存缓存、尚未写完盘的写入次数。
	// 读盘时据此判断内存里是否有比磁盘更新的内容。
	lastSelectionWritesPending int
	// lastSelectionWriteMu 串行化写盘，写盘时读取最新缓存，避免两次写入乱序后旧值覆盖新值。
	lastSelectionWriteMu sync.Mutex
	// lastSelectionWriteWG 追踪尚未完成的后台写盘（writeLastSelections 与 persistUIState），
	// 供测试在清理临时数据目录前等待写盘完成。
	lastSelectionWriteWG sync.WaitGroup

	// 以下脏标记记录本进程改过、但还没落盘的字段：写盘时只把这些字段覆盖到磁盘内容上，
	// 其它字段仍以磁盘为准，避免把别的写者（外部进程、手改文件）改过的字段冲掉。
	lastModelDirty             bool
	lastModelQuickDirty        bool
	lastSwarmEnabledDirty      bool
	lastSwarmBeesDirty         bool
	lastSwarmQuickEnabledDirty bool
	lastSwarmQuickBeesDirty    bool
	// continueDismissedCache/continueDismissedDirty 是继续胶囊忽略记录的内存缓存与脏标记，
	// 与模型记忆共用同一把锁与同一个写者。
	continueDismissedCache map[string]string
	continueDismissedDirty bool
	// lastCloneDirCache/lastCloneDirDirty 是上次克隆父目录的内存缓存与脏标记，
	// 与模型记忆共用同一把锁与同一个写者。
	lastCloneDirCache string
	lastCloneDirDirty bool
	// swarmFailedSeenCache/swarmFailedSeenDirty 是已查看失败子代理记录的持久化缓存与脏标记，
	// 与模型记忆共用同一把锁与同一个写者。
	swarmFailedSeenCache map[string]map[string]string
	swarmFailedSeenDirty bool
	// previewDesignBeesCache/previewDesignBeesDirty 是本地设计稿与设计子代理映射的持久化缓存与脏标记，
	// 与模型记忆共用同一把锁与同一个写者。
	previewDesignBeesCache map[string]string
	previewDesignBeesDirty bool
	// projectModelCache 是按项目记住的模型选择（项目键 -> 选择）；projectModelDirty 按项目键
	// 记录本进程改过、还没落盘的条目，projectModelGeneration 用来在写盘期间又有新写入时放弃清脏。
	// 脏标记按项目键粒度记录，写盘时只覆盖这些键，不会冲掉其它项目的外部改动。
	projectModelCache      map[string]ProjectModelSelection
	projectModelDirty      map[string]bool
	projectModelGeneration map[string]uint64

	// 以下 generation 对每个脏字段与待写侧栏状态各记一个单调递增计数，每次 save / remember /
	// persistUIState 都加一。写者在锁内取快照后要放开锁写盘，这期间可能有新的写入进来；
	// 写盘完成后只有 generation 没变的字段才可以清脏、才可以用合并结果回填缓存，
	// 否则新值会被这次写盘清脏并覆盖掉，后续写者再把磁盘旧值写回缓存。
	lastModelGeneration             uint64
	lastModelQuickGeneration        uint64
	lastSwarmEnabledGeneration      uint64
	lastSwarmBeesGeneration         uint64
	lastSwarmQuickEnabledGeneration uint64
	lastSwarmQuickBeesGeneration    uint64
	lastSidebarGeneration           uint64
	continueDismissedGeneration     uint64
	lastCloneDirGeneration          uint64
	swarmFailedSeenGeneration       uint64
	previewDesignBeesGeneration     uint64
)

// uiStateBeforeWriteHook 仅供测试注入：在 flushUIStateLocked 于锁内取完快照、释放锁、
// 尚未写盘之前调用，用来模拟“写盘期间又来了新的保存”。生产代码里始终为 nil。
var uiStateBeforeWriteHook func()

// pendingUISidebarState 是最近一次 persistUIState 待写的侧栏相关字段，受 lastModelSelectionMu 保护。
// 侧栏拖动、切换与项目重排与模型记忆共用同一个写者，避免在 goroutine 外面快照的旧 last_* 覆盖新记忆。
type pendingUISidebarState struct {
	set               bool
	ui                UIState
	projectOrder      []string
	projectActivation map[string]bool
}

var lastSelectionPendingSidebar pendingUISidebarState

// lastSelectionStatInterval 是两次 Stat ui-state.json 之间的最小间隔。
// 外部改写最多延迟这么久才被发现，这是为了避免每帧都去 Stat（帧级 profile 里 Stat 是热点）。
// 测试里设为 0，让写文件后立刻读取仍能发现改动。
var lastSelectionStatInterval = time.Second

// lastSelectionCheckInFlight 保证后台检查 ui-state.json 变更同时只有一个协程在飞。
var lastSelectionCheckInFlight atomic.Bool

// errUIStateFileCorrupt 表示 ui-state.json 存在但无法解析；此时写盘会放弃本次写入，绝不写零值。
var errUIStateFileCorrupt = errors.New("ui-state.json 内容无法解析")

// ensureLastSelectionLoadedLocked 在缓存未加载、数据目录变化或 ui-state.json 被外部改写时
// 从磁盘读取；调用方持有 lastModelSelectionMu。
func ensureLastSelectionLoadedLocked() {
	path := uiStatePath()
	if lastModelSelectionLoaded && lastModelSelectionPath == path {
		// 距上次 Stat 不足最小间隔时直接返回缓存，避免每帧 Stat。
		if time.Since(lastModelSelectionCheckedAt) < lastSelectionStatInterval {
			return
		}
		lastModelSelectionCheckedAt = time.Now()
		if !lastSelectionFileChangedLocked(path) {
			return
		}
	}
	reloadLastSelectionLocked(path)
}

// ensureLastSelectionLoadedAsyncLocked 供渲染路径使用：绝不阻塞在 os.Stat 或读盘上。
// 缓存过期或路径变化时最多启动一个后台协程检查并在必要时重载，重载完成后调用 onReload 请求重绘。
// 调用方持有 lastModelSelectionMu。
func ensureLastSelectionLoadedAsyncLocked(onReload func()) {
	path := uiStatePath()
	if path == "" {
		return
	}
	if lastModelSelectionLoaded && lastModelSelectionPath == path &&
		time.Since(lastModelSelectionCheckedAt) < lastSelectionStatInterval {
		return
	}
	if !lastSelectionCheckInFlight.CompareAndSwap(false, true) {
		return
	}
	lastSelectionWriteWG.Add(1)
	go func(targetPath string, reloadNotify func()) {
		defer lastSelectionWriteWG.Done()
		defer lastSelectionCheckInFlight.Store(false)

		info, statErr := os.Stat(targetPath)
		lastModelSelectionMu.Lock()
		if uiStatePath() != targetPath {
			lastModelSelectionMu.Unlock()
			return
		}
		needReload := !lastModelSelectionLoaded || lastModelSelectionPath != targetPath
		if !needReload {
			lastModelSelectionCheckedAt = time.Now()
			if statErr != nil {
				lastModelSelectionModTime = time.Time{}
			} else if !info.ModTime().Equal(lastModelSelectionModTime) {
				needReload = true
			}
		}
		if needReload {
			reloadLastSelectionLocked(targetPath)
		}
		lastModelSelectionMu.Unlock()
		if needReload && reloadNotify != nil {
			reloadNotify()
		}
	}(path, onReload)
}

// lastSelectionFileChangedLocked 判断 ui-state.json 的修改时间是否与记录的不同（即被外部改写）。
// 文件不存在时把记录的修改时间清零并返回 false：没有可读的新内容，同时让文件之后被创建时能触发重新加载。
func lastSelectionFileChangedLocked(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		lastModelSelectionModTime = time.Time{}
		return false
	}
	return !info.ModTime().Equal(lastModelSelectionModTime)
}

// reloadLastSelectionLocked 重新读盘并刷新最近选择缓存，同时记录文件当前的修改时间。
// 读盘结果与本进程尚标脏的字段按字段合并，外部改动不会把本进程未落盘的记忆冲掉。
func reloadLastSelectionLocked(path string) {
	if lastModelSelectionPath != path {
		tasksDetailWidthDirty = false
		tasksDetailWidthCache = 0
		tasksDetailWidthGeneration++
	}
	lastModelSelectionPath = path
	lastModelSelectionLoaded = true
	lastModelSelectionCheckedAt = time.Now()
	lastModelSelectionModTime = time.Time{}
	if info, err := os.Stat(path); err == nil {
		lastModelSelectionModTime = info.ModTime()
	}
	file, ok := loadUIStateFile()
	if !ok {
		// 文件缺失或损坏：磁盘上没有可用的底稿，只保留本进程标脏的字段。
		file = uiStateFile{}
	}
	// 先合并（会读取旧缓存里的脏字段）再覆写缓存。
	merged := mergeLastSelectionsLocked(file)
	if !tasksDetailWidthDirty {
		tasksDetailWidthCache = file.TasksDetailWidth
	}
	lastModelSelectionCache = nil
	if merged.model.Provider != "" || merged.model.Model != "" {
		sel := merged.model
		lastModelSelectionCache = &sel
	}
	lastModelQuickCache = nil
	if merged.modelQuick != nil && (merged.modelQuick.Provider != "" || merged.modelQuick.Model != "") {
		sel := *merged.modelQuick
		lastModelQuickCache = &sel
	}
	lastSwarmSelectionCache = merged.swarm
	lastSwarmQuickCache = merged.swarmQuick
	continueDismissedCache = copyStringMap(merged.continueDismissed)
	lastCloneDirCache = merged.lastCloneDir
	swarmFailedSeenCache = copyNestedStringMap(merged.swarmFailedSeen)
	previewDesignBeesCache = copyStringMap(merged.previewDesignBees)
	projectModelCache = copyProjectSelections(merged.projects)
}

func clampTasksDetailWidth(width float32) float32 {
	if width <= 0 || math.IsNaN(float64(width)) || math.IsInf(float64(width), 0) {
		return KanbanDetailDefaultWidth
	}
	return min(max(width, KanbanDetailMinWidth), KanbanDetailMaxWidth)
}

func loadTasksDetailWidth() float32 {
	lastModelSelectionMu.Lock()
	defer lastModelSelectionMu.Unlock()
	ensureLastSelectionLoadedLocked()
	return clampTasksDetailWidth(tasksDetailWidthCache)
}

func rememberTasksDetailWidth(ks *kanbanState, width float32) {
	if ks == nil || !ks.persistTasksDetailWidth {
		return
	}
	lastModelSelectionMu.Lock()
	ensureLastSelectionLoadedLocked()
	tasksDetailWidthCache = clampTasksDetailWidth(width)
	tasksDetailWidthDirty = true
	tasksDetailWidthGeneration++
	lastSelectionWritesPending++
	lastModelSelectionMu.Unlock()
	writeLastSelections(nil)
}

// lastSelectionMerge 是一次按字段合并的结果：swarm 为新键 last_swarm 的值（无记录时为 nil），
// *Applied 标记该字段来自本进程标脏的缓存（写盘成功后据此清除脏标记）。
type lastSelectionMerge struct {
	model                    ModelSelection
	modelQuick               *ModelSelection
	swarm                    *LastSwarmSelection
	swarmQuick               *LastSwarmSelection
	modelApplied             bool
	modelQuickApplied        bool
	swarmEnabledApplied      bool
	swarmBeesApplied         bool
	swarmQuickEnabledApplied bool
	swarmQuickBeesApplied    bool
	// continueDismissed 是合并后的继续胶囊忽略记录，continueDismissedApplied 标记它来自本进程的脏缓存。
	continueDismissed        map[string]string
	continueDismissedApplied bool
	// lastCloneDir 是合并后的上次克隆父目录，lastCloneDirApplied 标记它来自本进程的脏缓存。
	lastCloneDir        string
	lastCloneDirApplied bool
	// swarmFailedSeen 是合并后的已查看失败子代理记录，swarmFailedSeenApplied 标记它来自本进程的脏缓存。
	swarmFailedSeen        map[string]map[string]string
	swarmFailedSeenApplied bool
	// previewDesignBees 是合并后的本地设计稿与设计子代理映射，previewDesignBeesApplied 标记它来自本进程的脏缓存。
	previewDesignBees        map[string]string
	previewDesignBeesApplied bool
	// projects 是合并后的按项目模型选择，projectApplied 记录其中来自本进程脏缓存的键。
	projects       map[string]ProjectModelSelection
	projectApplied []string
}

// mergeLastSelectionsLocked 以磁盘上的 file.LastModel / file.LastSwarm（缺失时回退 last_hydra）为底，
// 只用本进程标脏的字段覆盖上去：Enabled 与 Bees 分别判断。调用方持有 lastModelSelectionMu。
func mergeLastSelectionsLocked(file uiStateFile) lastSelectionMerge {
	merged := lastSelectionMerge{
		model:                    file.LastModel,
		modelApplied:             lastModelDirty,
		modelQuickApplied:        lastModelQuickDirty,
		continueDismissed:        copyStringMap(file.ContinueDismissed),
		continueDismissedApplied: continueDismissedDirty,
		lastCloneDir:             strings.TrimSpace(file.LastCloneDir),
		lastCloneDirApplied:      lastCloneDirDirty,
		swarmFailedSeen:          copyNestedStringMap(file.SwarmFailedSeen),
		swarmFailedSeenApplied:   swarmFailedSeenDirty,
		previewDesignBees:        copyStringMap(file.PreviewDesignBees),
		previewDesignBeesApplied: previewDesignBeesDirty,
		swarmEnabledApplied:      lastSwarmEnabledDirty,
		swarmBeesApplied:         lastSwarmBeesDirty,
		swarmQuickEnabledApplied: lastSwarmQuickEnabledDirty,
		swarmQuickBeesApplied:    lastSwarmQuickBeesDirty,
	}
	if continueDismissedDirty {
		// 本进程标脏时以内存缓存为准，忽略记录整体覆盖，不做逐条合并。
		merged.continueDismissed = copyStringMap(continueDismissedCache)
	}
	if lastCloneDirDirty {
		// 本进程标脏时以内存缓存为准，覆盖磁盘上的上次克隆父目录。
		merged.lastCloneDir = lastCloneDirCache
	}
	if swarmFailedSeenDirty {
		// 本进程标脏时以内存缓存为准，已查看失败记录整体覆盖，不做逐条合并。
		merged.swarmFailedSeen = copyNestedStringMap(swarmFailedSeenCache)
	}
	if previewDesignBeesDirty {
		// 本进程标脏时以内存缓存为准，本地设计稿映射整体覆盖，不做逐条合并。
		merged.previewDesignBees = copyStringMap(previewDesignBeesCache)
	}
	// 按项目模型选择同样以磁盘为底，只覆盖本进程标脏的项目键。
	merged.projects = copyProjectSelections(file.ProjectModels)
	for key := range projectModelDirty {
		merged.projectApplied = append(merged.projectApplied, key)
		if sel, ok := projectModelCache[key]; ok {
			if merged.projects == nil {
				merged.projects = make(map[string]ProjectModelSelection, len(projectModelDirty))
			}
			merged.projects[key] = sel
			continue
		}
		delete(merged.projects, key)
	}
	if lastModelDirty && lastModelSelectionCache != nil {
		merged.model = *lastModelSelectionCache
	}
	if file.LastModelQuick != nil && (file.LastModelQuick.Provider != "" || file.LastModelQuick.Model != "") {
		sel := *file.LastModelQuick
		merged.modelQuick = &sel
	}
	if lastModelQuickDirty && lastModelQuickCache != nil {
		sel := *lastModelQuickCache
		merged.modelQuick = &sel
	}
	// 新键优先；旧文件只有 last_hydra 时以旧值为底，写回时迁移成新键。
	base := file.LastSwarm
	if base == nil {
		base = file.LegacyLastSwarm
	}
	merged.swarm = mergeSwarmSelection(base, lastSwarmSelectionCache, merged.swarmEnabledApplied, merged.swarmBeesApplied)
	merged.swarmQuick = mergeSwarmSelection(file.LastSwarmQuick, lastSwarmQuickCache, merged.swarmQuickEnabledApplied, merged.swarmQuickBeesApplied)
	return merged
}

// mergeSwarmSelection 以磁盘上的 Swarm 记录为底，按字段只覆盖本进程标脏的开关或执行侧。
func mergeSwarmSelection(disk, cache *LastSwarmSelection, enabledDirty, beesDirty bool) *LastSwarmSelection {
	swarm := LastSwarmSelection{}
	if disk != nil {
		swarm = *disk
	}
	if cache != nil {
		if enabledDirty {
			swarm.Enabled = cache.Enabled
		}
		if beesDirty {
			swarm.Bees = cache.Bees
		}
	}
	if swarm.Enabled != nil || swarm.Bees != nil {
		return &swarm
	}
	return nil
}

// finishLastSelectionWrite 在写盘结束后调用：减少待写计数；写成功时记录本次写入的修改时间
// （来自临时文件 rename 前的 Stat），这样本进程刚写下的内容不会被下次读取当成外部改动。
func finishLastSelectionWrite(modTime time.Time, writeErr error) {
	lastModelSelectionMu.Lock()
	defer lastModelSelectionMu.Unlock()
	if lastSelectionWritesPending > 0 {
		lastSelectionWritesPending--
	}
	if writeErr != nil {
		return
	}
	lastModelSelectionModTime = modTime
}

// loadLastModelSelectionCached 供渲染路径只读获取最近一次模型选择，并在后台异步检查磁盘变更。
func loadLastModelSelectionCached(onReload func()) (ModelSelection, bool) {
	lastModelSelectionMu.Lock()
	defer lastModelSelectionMu.Unlock()
	ensureLastSelectionLoadedAsyncLocked(onReload)
	if !lastModelSelectionLoaded || lastModelSelectionPath != uiStatePath() || lastModelSelectionCache == nil {
		return ModelSelection{}, false
	}
	sel := *lastModelSelectionCache
	if sel.Provider == "" && sel.Model == "" {
		return ModelSelection{}, false
	}
	return sel, true
}

// loadLastModelSelection 读取项目场景最近一次模型选择：优先使用进程内缓存，未命中时从 ui-state.json 读取；
// 没有记录时返回零值与 false。
func loadLastModelSelection() (ModelSelection, bool) {
	return loadCachedModelSelection(&lastModelSelectionCache)
}

// loadLastModelQuickSelection 读取快速对话（projectID 为空）最近一次模型选择；没有记录时返回零值与 false。
func loadLastModelQuickSelection() (ModelSelection, bool) {
	return loadCachedModelSelection(&lastModelQuickCache)
}

func loadCachedModelSelection(cache **ModelSelection) (ModelSelection, bool) {
	lastModelSelectionMu.Lock()
	defer lastModelSelectionMu.Unlock()
	ensureLastSelectionLoadedLocked()
	if *cache == nil {
		return ModelSelection{}, false
	}
	sel := **cache
	if sel.Provider == "" && sel.Model == "" {
		return ModelSelection{}, false
	}
	return sel, true
}

// saveLastModelSelection 按场景记录最近一次模型选择并写入 ui-state.json；写盘放在后台 goroutine，失败静默。
// projectID 为空表示快速对话（写入 last_model_quick），非空表示项目场景（写入 last_model）。
// 主导记忆要求 provider 与 model 都非空：模型选择组件在 provider 不在当前目录里时解析结果只有
// provider（见 resolveModelPickerSelection），把这种半成品写进“上次使用”会让新建会话拿到空模型。
// 这里统一忽略，不标脏、不触发写盘。
func saveLastModelSelection(projectID string, sel ModelSelection) {
	if sel.Provider == "" || sel.Model == "" {
		return
	}
	quick := strings.TrimSpace(projectID) == ""
	lastModelSelectionMu.Lock()
	ensureLastSelectionLoadedLocked()
	if quick {
		lastModelQuickCache = &sel
		lastModelQuickDirty = true
		lastModelQuickGeneration++
	} else {
		lastModelSelectionCache = &sel
		lastModelDirty = true
		lastModelGeneration++
	}
	lastSelectionWritesPending++
	lastModelSelectionMu.Unlock()
	writeLastSelections(nil)
}

// projectSelectionNoneKey 是没有项目归属时（例如尚未选择项目的新会话）使用的项目桶键。
const projectSelectionNoneKey = "none"

// projectSelectionKey 把项目 ID 归一成项目桶键；空 ID 统一落到 projectSelectionNoneKey。
func projectSelectionKey(projectID string) string {
	if id := strings.TrimSpace(projectID); id != "" {
		return id
	}
	return projectSelectionNoneKey
}

// loadProjectModelSelectionCached 供渲染路径只读获取项目记住的模型选择，并在后台异步检查磁盘变更。
func loadProjectModelSelectionCached(projectID string, onReload func()) (ProjectModelSelection, bool) {
	lastModelSelectionMu.Lock()
	defer lastModelSelectionMu.Unlock()
	ensureLastSelectionLoadedAsyncLocked(onReload)
	if !lastModelSelectionLoaded || lastModelSelectionPath != uiStatePath() {
		return ProjectModelSelection{}, false
	}
	sel, ok := projectModelCache[projectSelectionKey(projectID)]
	if !ok || (sel.Provider == "" && sel.Model == "") {
		return ProjectModelSelection{}, false
	}
	return sel, true
}

// loadProjectModelSelection 读取某个项目记住的模型选择；没有记录时返回零值与 false。
// 调用方拿不到记录（或记录的 provider/model 都为空）时退回全局 loadLastModelSelection。
func loadProjectModelSelection(projectID string) (ProjectModelSelection, bool) {
	lastModelSelectionMu.Lock()
	defer lastModelSelectionMu.Unlock()
	ensureLastSelectionLoadedLocked()
	sel, ok := projectModelCache[projectSelectionKey(projectID)]
	if !ok {
		return ProjectModelSelection{}, false
	}
	if sel.Provider == "" && sel.Model == "" {
		return ProjectModelSelection{}, false
	}
	return sel, true
}

// saveProjectModelSelection 记录项目下确认的模型选择并写入 ui-state.json。
// 与全局记忆一致，provider 与 model 都为空时直接忽略，避免半成品覆盖已有的好记录。
func saveProjectModelSelection(projectID string, sel ModelSelection) {
	if sel.Provider == "" || sel.Model == "" {
		return
	}
	updateProjectModelSelection(projectID, func(p *ProjectModelSelection) {
		p.Provider, p.Model, p.Effort, p.Fast = sel.Provider, sel.Model, sel.Effort, sel.Fast
	})
}

// updateProjectModelSelection 在锁内按项目桶更新选择并标脏，再交给统一的写者落盘。
// 与全局记忆不同的是，脏标记与 generation 都按项目键粒度记录：
// 写盘时只会覆盖这次改过的项目，其它项目的记录仍以磁盘为准，不会被本进程的旧快照冲掉。
func updateProjectModelSelection(projectID string, mutate func(p *ProjectModelSelection)) {
	if mutate == nil {
		return
	}
	key := projectSelectionKey(projectID)
	lastModelSelectionMu.Lock()
	ensureLastSelectionLoadedLocked()
	if projectModelCache == nil {
		projectModelCache = make(map[string]ProjectModelSelection)
	}
	sel := projectModelCache[key]
	mutate(&sel)
	projectModelCache[key] = sel
	if projectModelDirty == nil {
		projectModelDirty = make(map[string]bool)
	}
	projectModelDirty[key] = true
	if projectModelGeneration == nil {
		projectModelGeneration = make(map[string]uint64)
	}
	projectModelGeneration[key]++
	lastSelectionWritesPending++
	lastModelSelectionMu.Unlock()
	writeLastSelections(nil)
}

// copyProjectSelections 复制按项目模型选择表（nil 保持 nil）。
func copyProjectSelections(m map[string]ProjectModelSelection) map[string]ProjectModelSelection {
	if len(m) == 0 {
		return nil
	}
	cp := make(map[string]ProjectModelSelection, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}

// loadLastSwarmSelection 返回项目场景最近一次 Swarm 记录的副本；没有记录时返回零值。
func loadLastSwarmSelection() LastSwarmSelection {
	return loadLastSwarmSelectionForScene(false)
}

// loadLastSwarmQuickSelection 返回快速对话（projectID 为空）最近一次 Swarm 记录的副本；没有记录时返回零值。
func loadLastSwarmQuickSelection() LastSwarmSelection {
	return loadLastSwarmSelectionForScene(true)
}

func loadLastSwarmSelectionForScene(quick bool) LastSwarmSelection {
	lastModelSelectionMu.Lock()
	defer lastModelSelectionMu.Unlock()
	ensureLastSelectionLoadedLocked()
	src := lastSwarmSelectionCache
	if quick {
		src = lastSwarmQuickCache
	}
	if src == nil {
		return LastSwarmSelection{}
	}
	return *src
}

// rememberSwarmEnabled 按场景记录最近一次 Swarm 开关选择（projectID 为空表示快速对话）。
func rememberSwarmEnabled(projectID string, on bool) {
	updateLastSwarmSelection(strings.TrimSpace(projectID) == "", true, func(h *LastSwarmSelection) { h.Enabled = &on })
}

// rememberSwarmBees 按场景记录最近一次执行侧选择（projectID 为空表示快速对话；provider/model 为空表示跟随主导）。
func rememberSwarmBees(projectID, provider, model, effort string) {
	updateLastSwarmSelection(strings.TrimSpace(projectID) == "", false, func(h *LastSwarmSelection) {
		h.Bees = &ModelSelection{Provider: provider, Model: model, Effort: effort}
	})
}

// updateLastSwarmSelection 更新内存中的 Swarm 记录并按场景与字段标脏：quick 区分快速对话与项目场景，
// enabled 为 true 表示这次只改开关，否则只改执行侧（Bees），这样写盘时不会顺带覆盖另一项的外部改动。
func updateLastSwarmSelection(quick, enabled bool, mutate func(h *LastSwarmSelection)) {
	lastModelSelectionMu.Lock()
	ensureLastSelectionLoadedLocked()
	cache := &lastSwarmSelectionCache
	dirty := &lastSwarmBeesDirty
	gen := &lastSwarmBeesGeneration
	if enabled {
		dirty = &lastSwarmEnabledDirty
		gen = &lastSwarmEnabledGeneration
	}
	if quick {
		cache = &lastSwarmQuickCache
		dirty = &lastSwarmQuickBeesDirty
		gen = &lastSwarmQuickBeesGeneration
		if enabled {
			dirty = &lastSwarmQuickEnabledDirty
			gen = &lastSwarmQuickEnabledGeneration
		}
	}
	next := LastSwarmSelection{}
	if *cache != nil {
		next = **cache
	}
	mutate(&next)
	*cache = &next
	*dirty = true
	*gen++
	lastSelectionWritesPending++
	lastModelSelectionMu.Unlock()
	writeLastSelections(nil)
}

// writeLastSelections 是唯一的后台写盘入口：把本次标脏的记忆与待写的侧栏状态合并进 ui-state.json。
// 调用方必须已在持有 lastModelSelectionMu 时把 lastSelectionWritesPending 加一，
// 写盘结束（无论成败）由 finishLastSelectionWrite 负责减一。
// onErr 非 nil 时接收写盘错误（persistUIState 用它弹 toast）；文件损坏属于放弃写入，不回调。
func writeLastSelections(onErr func(error)) {
	lastSelectionWriteWG.Add(1)
	go func() {
		defer lastSelectionWriteWG.Done()
		lastSelectionWriteMu.Lock()
		defer lastSelectionWriteMu.Unlock()
		modTime, err := flushUIStateLocked(nil)
		finishLastSelectionWrite(modTime, err)
		if err != nil && !errors.Is(err, errUIStateFileCorrupt) && onErr != nil {
			onErr(err)
		}
	}()
}

// flushUIStateLocked 是 ui-state.json 唯一的写盘实现，调用方持有 lastSelectionWriteMu。
// 顺序：读盘 -> 按字段合并本进程标脏的记忆与待写侧栏状态 -> 写盘 -> 回填缓存并清脏。
// uiOverride 非 nil 时用它覆盖侧栏宽度与显隐（saveUIState 的全量写用）。
// 返回临时文件 rename 时的修改时间与错误；放弃写入（文件损坏）时两者分别为零值与 errUIStateFileCorrupt。
func flushUIStateLocked(uiOverride *UIState) (time.Time, error) {
	if uiStatePath() == "" {
		return time.Time{}, errors.New("未找到 mcode 数据目录")
	}
	file, exists, parseErr := loadUIStateFileStrict()
	if exists && parseErr != nil {
		// 文件存在但解析失败：这次放弃写盘，绝不把零值整体写回去覆盖用户的文件。
		return time.Time{}, errUIStateFileCorrupt
	}

	lastModelSelectionMu.Lock()
	merged := mergeLastSelectionsLocked(file)
	// 记下这次快照对应的 generation：写盘期间若有新的 save / remember / persistUIState 进来，
	// 对应字段的 generation 会变大，写完后就不能清脏、不能回填旧快照。
	// 继续胶囊忽略记录与已查看失败子代理记录同样只在本进程标脏时覆盖磁盘内容。
	file.ContinueDismissed = merged.continueDismissed
	file.LastCloneDir = merged.lastCloneDir
	file.SwarmFailedSeen = merged.swarmFailedSeen
	file.PreviewDesignBees = merged.previewDesignBees
	modelGeneration := lastModelGeneration
	modelQuickGeneration := lastModelQuickGeneration
	swarmEnabledGeneration := lastSwarmEnabledGeneration
	swarmBeesGeneration := lastSwarmBeesGeneration
	swarmQuickEnabledGeneration := lastSwarmQuickEnabledGeneration
	swarmQuickBeesGeneration := lastSwarmQuickBeesGeneration
	sidebarGeneration := lastSidebarGeneration
	// 上次会话以磁盘为底，只有本进程待写时才覆盖，避免侧栏写盘把磁盘记录冲掉。
	lastThreadMu.Lock()
	threadSnapshot := lastThreadID
	threadApplied := lastThreadPending
	lastThreadMu.Unlock()
	continueGeneration := continueDismissedGeneration
	lastCloneDirGen := lastCloneDirGeneration
	swarmFailedSeenGen := swarmFailedSeenGeneration
	previewDesignBeesGen := previewDesignBeesGeneration
	tasksGeneration := tasksDetailWidthGeneration
	tasksApplied := tasksDetailWidthDirty
	if tasksApplied {
		file.TasksDetailWidth = tasksDetailWidthCache
	}
	// 按项目选择按项目键记 generation：写盘期间该项目又有新写入时不清脏，交给后面的写者。
	projectGenerations := make(map[string]uint64, len(merged.projectApplied))
	for _, key := range merged.projectApplied {
		projectGenerations[key] = projectModelGeneration[key]
	}
	file.LastModel = merged.model
	file.LastModelQuick = merged.modelQuick
	file.LastSwarm = merged.swarm
	file.LastSwarmQuick = merged.swarmQuick
	file.ProjectModels = merged.projects
	// 只写新键，读到旧文件时顺手把 last_hydra 迁移掉。
	file.LegacyLastSwarm = nil
	diskThread := file.LastThreadID
	sidebarConsumed := false
	switch {
	case uiOverride != nil:
		// 调用方没有带侧栏宽度时保留磁盘上的旧值（侧栏宽度与显隐仍按 override 覆盖）。
		ui := *uiOverride
		ui.InspectorSessions = mergeInspectorPreferences(file.InspectorSessions, ui.InspectorSessions)
		if ui.SidePaneWidths == nil {
			ui.SidePaneWidths = file.SidePaneWidths
		}
		file.UIState = ui
	case lastSelectionPendingSidebar.set:
		// 没有待写侧栏状态时保留磁盘上的原值。
		pending := lastSelectionPendingSidebar
		pending.ui.InspectorSessions = mergeInspectorPreferences(file.InspectorSessions, pending.ui.InspectorSessions)
		// 本窗口从未创建过侧栏面板状态时不会带 side_pane_widths，
		// 这里用磁盘上的旧值补上，避免把另一个进程写入的宽度冲掉。
		if pending.ui.SidePaneWidths == nil {
			pending.ui.SidePaneWidths = file.SidePaneWidths
		}
		file.UIState = pending.ui
		file.ProjectOrder = append([]string(nil), pending.projectOrder...)
		file.ProjectActivation = copyBoolMap(pending.projectActivation)
		sidebarConsumed = true
	}
	// UIState 整块赋值会顺带覆盖内嵌的 LastThreadID，在 switch 之后单独落定。
	if threadApplied {
		file.LastThreadID = threadSnapshot
	} else {
		file.LastThreadID = diskThread
	}
	inspectorApplied := make(map[string]pendingInspectorPreference)
	if inspectorPreferencePendingPath == uiStatePath() {
		file.InspectorSessions = mergeInspectorPreferences(nil, file.InspectorSessions)
		for tid, change := range inspectorPreferencePending {
			inspectorApplied[tid] = change
			if change.Delete {
				delete(file.InspectorSessions, tid)
			} else {
				if file.InspectorSessions == nil {
					file.InspectorSessions = make(map[string]InspectorPreference)
				}
				file.InspectorSessions[tid] = normalizeInspectorPreference(change.Preference)
			}
		}
	}
	reviewApplied := make(map[string]pendingReviewPreference)
	if reviewPreferencePendingPath == uiStatePath() {
		for tid, change := range reviewPreferencePending {
			if file.ReviewSessions == nil {
				file.ReviewSessions = make(map[string]reviewPreference)
			}
			file.ReviewSessions[tid] = change.preference
			reviewApplied[tid] = change
		}
	}
	lastModelSelectionMu.Unlock()

	if hook := uiStateBeforeWriteHook; hook != nil {
		hook()
	}

	modTime, err := writeUIStateFile(file)
	if err != nil {
		// 写盘失败：脏标记与待写侧栏状态都保留，等下一次写盘重试。
		return time.Time{}, err
	}

	lastModelSelectionMu.Lock()
	for tid, change := range reviewApplied {
		if current := reviewPreferencePending[tid]; current.generation == change.generation {
			delete(reviewPreferencePending, tid)
		}
	}
	for tid, change := range inspectorApplied {
		if inspectorPreferencePending[tid].Generation == change.Generation {
			delete(inspectorPreferencePending, tid)
		}
	}
	lastThreadMu.Lock()
	// 写盘期间又换了会话时保持待写标记，并补排一轮写盘：这次切换本身已经排队，
	// 那轮可能正卡在本轮后面还没来得及取快照，不补就可能丢掉最新值。
	threadDirty := threadApplied && lastThreadID != threadSnapshot
	if threadApplied && !threadDirty {
		lastThreadPending = false
	}
	lastThreadMu.Unlock()
	if threadDirty {
		lastSelectionWritesPending++
		go writeLastSelections(nil)
	}
	if tasksGeneration == tasksDetailWidthGeneration {
		if tasksApplied {
			tasksDetailWidthDirty = false
		}
		tasksDetailWidthCache = file.TasksDetailWidth
	}
	// 只有 generation 没变的字段才可以清脏并回填合并结果：generation 变了说明写盘期间又来了
	// 新的写入，缓存里已经是更新的值，这里保持脏标记与缓存不动，交给排在后面的写者写出。
	// 未变更字段的合并结果包含磁盘上的外部改动，回填缓存正好把外部改动也同步进来。
	if modelGeneration == lastModelGeneration {
		if merged.model.Provider != "" || merged.model.Model != "" {
			sel := merged.model
			lastModelSelectionCache = &sel
		}
		if merged.modelApplied {
			lastModelDirty = false
		}
	}
	if modelQuickGeneration == lastModelQuickGeneration {
		if merged.modelQuick != nil && (merged.modelQuick.Provider != "" || merged.modelQuick.Model != "") {
			sel := *merged.modelQuick
			lastModelQuickCache = &sel
		} else {
			lastModelQuickCache = nil
		}
		if merged.modelQuickApplied {
			lastModelQuickDirty = false
		}
	}
	if swarmEnabledGeneration == lastSwarmEnabledGeneration && merged.swarmEnabledApplied {
		lastSwarmEnabledDirty = false
	}
	if swarmBeesGeneration == lastSwarmBeesGeneration && merged.swarmBeesApplied {
		lastSwarmBeesDirty = false
	}
	if swarmEnabledGeneration == lastSwarmEnabledGeneration && swarmBeesGeneration == lastSwarmBeesGeneration {
		lastSwarmSelectionCache = merged.swarm
	}
	if swarmQuickEnabledGeneration == lastSwarmQuickEnabledGeneration && merged.swarmQuickEnabledApplied {
		lastSwarmQuickEnabledDirty = false
	}
	if swarmQuickBeesGeneration == lastSwarmQuickBeesGeneration && merged.swarmQuickBeesApplied {
		lastSwarmQuickBeesDirty = false
	}
	if swarmQuickEnabledGeneration == lastSwarmQuickEnabledGeneration && swarmQuickBeesGeneration == lastSwarmQuickBeesGeneration {
		lastSwarmQuickCache = merged.swarmQuick
	}
	if sidebarConsumed && sidebarGeneration == lastSidebarGeneration {
		lastSelectionPendingSidebar = pendingUISidebarState{}
	}
	if continueGeneration == continueDismissedGeneration {
		if merged.continueDismissedApplied {
			continueDismissedDirty = false
		}
		// 合并结果包含磁盘上的外部改动，回填缓存正好把忽略记录的外部改动也同步进来。
		continueDismissedCache = copyStringMap(merged.continueDismissed)
	}
	if lastCloneDirGen == lastCloneDirGeneration {
		if merged.lastCloneDirApplied {
			lastCloneDirDirty = false
		}
		lastCloneDirCache = merged.lastCloneDir
	}
	if swarmFailedSeenGen == swarmFailedSeenGeneration {
		if merged.swarmFailedSeenApplied {
			swarmFailedSeenDirty = false
		}
		swarmFailedSeenCache = copyNestedStringMap(merged.swarmFailedSeen)
	}
	if previewDesignBeesGen == previewDesignBeesGeneration {
		if merged.previewDesignBeesApplied {
			previewDesignBeesDirty = false
		}
		previewDesignBeesCache = copyStringMap(merged.previewDesignBees)
	}
	for _, key := range merged.projectApplied {
		if projectGenerations[key] != projectModelGeneration[key] {
			continue
		}
		delete(projectModelDirty, key)
	}
	lastModelSelectionMu.Unlock()
	return modTime, nil
}

// copyBoolMap 复制项目激活覆盖表（nil 保持 nil）。
func copyBoolMap(m map[string]bool) map[string]bool {
	if len(m) == 0 {
		return nil
	}
	cp := make(map[string]bool, len(m))
	for id, active := range m {
		cp[id] = active
	}
	return cp
}

// copyStringMap 复制字符串映射（nil 保持 nil）。
func copyStringMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	cp := make(map[string]string, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}

// copyNestedStringMap 深拷贝二级字符串映射（nil 保持 nil）。
func copyNestedStringMap(m map[string]map[string]string) map[string]map[string]string {
	if len(m) == 0 {
		return nil
	}
	cp := make(map[string]map[string]string, len(m))
	for outer, inner := range m {
		if copied := copyStringMap(inner); len(copied) > 0 {
			cp[outer] = copied
		}
	}
	if len(cp) == 0 {
		return nil
	}
	return cp
}

// waitUIStateWrites 等待后台写 ui-state.json 的 goroutine 全部结束。
// 测试把 MCODE_DATA 指向 t.TempDir() 时，须在清理临时目录前调用它（例如注册 t.Cleanup），
// 否则后台写盘会在目录被删除后又写进文件，导致 RemoveAll 报 “directory not empty”。
func waitUIStateWrites() {
	lastSelectionWriteWG.Wait()
}

// saveUIState 写入界面状态文件（不含项目顺序，供按值调用方与测试使用）。
// 走与模型记忆相同的写者（同步执行，便于调用方立刻读到结果），不会清空 last_* 记录。
func saveUIState(st UIState) error {
	lastSelectionWriteMu.Lock()
	defer lastSelectionWriteMu.Unlock()
	modTime, err := flushUIStateLocked(&st)
	if err == nil {
		// 这次写入不是由 writeLastSelections 提交的，不动待写计数，只记录写入的修改时间。
		lastModelSelectionMu.Lock()
		lastModelSelectionModTime = modTime
		lastModelSelectionMu.Unlock()
	}
	return err
}

// saveUIStateFile 写入界面状态文件：先写同目录临时文件，再重命名覆盖，避免半截文件。
func saveUIStateFile(file uiStateFile) error {
	_, err := writeUIStateFile(file)
	return err
}

// writeUIStateFile 落盘并返回本次写入的修改时间。
// 临时文件先 Sync 再 Close（避免 rename 后掉电丢内容）；mtime 取 rename 之前临时文件的 Stat
// （rename 会保留 mtime），这样调用方记录的就是本次写入的时间，而不是可能已被别人改写的目标文件时间。
func writeUIStateFile(file uiStateFile) (time.Time, error) {
	path := uiStatePath()
	if path == "" {
		return time.Time{}, errors.New("未找到 mcode 数据目录")
	}
	file.SidebarWidth = clampSidebarWidth(file.SidebarWidth)
	file.InspectorSessions = mergeInspectorPreferences(nil, file.InspectorSessions)
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return time.Time{}, err
	}
	return writeUIStateBytes(path, data)
}

func writeUIStateBytes(path string, data []byte) (time.Time, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return time.Time{}, err
	}
	tmp, err := os.CreateTemp(dir, uiStateFileName+".tmp-*")
	if err != nil {
		return time.Time{}, err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return time.Time{}, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return time.Time{}, err
	}
	var modTime time.Time
	if info, err := tmp.Stat(); err == nil {
		modTime = info.ModTime()
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return time.Time{}, err
	}
	if err := fsutil.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return time.Time{}, err
	}
	return modTime, nil
}

// applyUIState 把持久化的界面状态应用到 chatState，在窗口构建时调用一次。
// 项目顺序与侧边栏状态保存在同一文件，启动时一并恢复。
func applyUIState(s *chatState, st UIState) {
	if s == nil {
		return
	}
	// 先读盘再持锁，避免在 s.mu 里做文件 I/O；恢复继续胶囊的忽略记录，
	// 最后一条消息 ID 变化后该记录自动失效；一并恢复已查看的失败子代理记录。
	continueDismissed := loadContinueDismissed()
	swarmFailedSeenRecords := loadSwarmFailedSeen()
	previewDesignBees := loadPreviewDesignBees()
	s.mu.Lock()
	s.sidebarWidth = clampSidebarWidth(st.SidebarWidth)
	s.sidebarCollapsed = st.SidebarHidden
	s.inspector.Preferences = mergeInspectorPreferences(nil, st.InspectorSessions)
	if len(continueDismissed) > 0 {
		if s.continueDismissed == nil {
			s.continueDismissed = make(map[string]string, len(continueDismissed))
		}
		for threadID, messageID := range continueDismissed {
			s.continueDismissed[threadID] = messageID
		}
	}
	s.mu.Unlock()
	mergeSwarmFailedSeenMemory(swarmFailedSeenRecords)
	mergePreviewDesignBeesMemory(previewDesignBees)
	// 恢复终端 dock 高度偏好（0 表示没有记录，首次打开终端时用默认高度）。
	setTerminalDockHeightPreference(s, st.TerminalDockHeight)
	// 恢复右侧分栏面板的宽度偏好（面板状态首次创建时生效）。
	setSidePaneWidthPreferences(s, st.SidePaneWidths)
	storeSidebarProjectOrder(s, loadProjectOrder())
	storeSidebarProjectActivation(s, loadProjectActivation())
	// 恢复上次退出的会话：只交给启动时的会话列表选中，这里不直接选中（列表还没加载）。
	lastThreadMu.Lock()
	lastThreadID = strings.TrimSpace(st.LastThreadID)
	lastThreadPending = false
	lastThreadMu.Unlock()
}

// persistUIState 把当前侧边栏宽度、显隐状态、项目顺序与项目激活覆盖写入 ui-state.json。
// 由拖拽分栏结束（Split 的 OnChange）、切换侧边栏与项目重排时调用。
// 这里只登记待写字段并交给模型记忆的同一个写者：写者在锁内读盘后按字段合并，
// 因此不会用在 goroutine 外面快照的旧 last_* 覆盖刚保存的新记忆。
// 未开启 WithUIStatePersistence 的窗口（独立会话窗口、测试）跳过写盘。
func persistUIState(s *chatState) {
	if s == nil || !uiStatePersistenceEnabled(s) {
		return
	}
	s.mu.Lock()
	st := UIState{
		SidebarWidth:       s.sidebarWidth,
		SidebarHidden:      s.sidebarCollapsed,
		TerminalDockHeight: terminalDockHeightForPersist(s),
	}
	s.mu.Unlock()
	order := sidebarProjectOrder(s)
	activation := sidebarProjectActivation(s)

	lastModelSelectionMu.Lock()
	lastSelectionPendingSidebar = pendingUISidebarState{
		set:               true,
		ui:                st,
		projectOrder:      order,
		projectActivation: activation,
	}
	lastSidebarGeneration++
	lastSelectionWritesPending++
	lastModelSelectionMu.Unlock()

	writeLastSelections(func(err error) {
		s.setToast("保存界面状态失败: " + err.Error())
	})
}

// toggleSidebarPersisted 切换侧边栏显示状态，并在开启持久化时写入状态。
// model.go 的 toggleSidebar 保持不变，入口统一走这里。
func toggleSidebarPersisted(s *chatState) {
	if s == nil {
		return
	}
	s.toggleSidebar()
	persistUIState(s)
}

// chatUIState 保存仅 UI 线程读写的界面状态，按 chatState 附加（同 chatToolbarState 模式），
// 避免改动 model.go 中的 chatState 结构。
type chatUIState struct {
	// persist 记录该窗口是否开启界面状态持久化（WithUIStatePersistence），默认关闭。
	persist bool
	// sidebarSlide 是侧边栏显示进度（0 隐藏，1 显示），root.go 每帧写入，chat.go 读取用于头部左边距。
	sidebarSlide float32
	// projectPopoverOpen 是新会话空态项目切换浮层的展开状态。
	projectPopoverOpen bool
	// projectPopoverList 保存新会话空态项目切换浮层的搜索与选择状态。
	projectPopoverList searchListState
	// compactDismissed 记录已忽略压缩建议的会话，本会话不再提示。
	compactDismissed map[string]bool
	// forwardSuggestDismissed 记录已忽略转发到新会话建议的会话，本会话不再提示。
	forwardSuggestDismissed map[string]bool
	// projectOrder 是侧边栏项目拖拽重排后的顺序（项目 ID 列表），随 ui-state.json 持久化。
	projectOrder []string
	// projectActivation 是项目激活状态的覆盖表（项目 ID -> 是否激活），随 ui-state.json 持久化。
	projectActivation map[string]bool
	// clone 是 GitHub 克隆面板的状态。
	clone githubCloneState
}

var (
	chatUIMu     sync.Mutex
	chatUIStates = make(map[*chatState]*chatUIState)
)

// getChatUIState 返回 chatState 对应的界面状态，首次访问时创建。
func getChatUIState(s *chatState) *chatUIState {
	if s == nil {
		return nil
	}
	chatUIMu.Lock()
	defer chatUIMu.Unlock()
	st, ok := chatUIStates[s]
	if !ok {
		st = &chatUIState{
			compactDismissed:        make(map[string]bool),
			forwardSuggestDismissed: make(map[string]bool),
			clone:                   githubCloneState{results: make(chan githubCloneResult, 4)},
		}
		chatUIStates[s] = st
	}
	return st
}

// releaseChatUIState 在窗口关闭时清理界面状态。
func releaseChatUIState(s *chatState) {
	if s == nil {
		return
	}
	chatUIMu.Lock()
	delete(chatUIStates, s)
	chatUIMu.Unlock()
	releaseTerminalDockPreference(s)
	releaseSidePanePreferences(s)
}

// uiStatePersistenceEnabled 返回该窗口是否开启了界面状态持久化。
func uiStatePersistenceEnabled(s *chatState) bool {
	if s == nil {
		return false
	}
	chatUIMu.Lock()
	defer chatUIMu.Unlock()
	st := chatUIStates[s]
	return st != nil && st.persist
}

// setSidebarVisibleProgress 记录当前帧的侧边栏显示进度（0 隐藏，1 显示）。
func setSidebarVisibleProgress(s *chatState, progress float32) {
	if s == nil {
		return
	}
	if progress < 0 {
		progress = 0
	} else if progress > 1 {
		progress = 1
	}
	st := getChatUIState(s)
	if st == nil {
		return
	}
	st.sidebarSlide = progress
}

// sidebarVisibleProgress 返回最近一帧的侧边栏显示进度；无记录时按当前折叠状态推断。
func sidebarVisibleProgress(s *chatState) float32 {
	if s == nil {
		return 1
	}
	chatUIMu.Lock()
	st := chatUIStates[s]
	chatUIMu.Unlock()
	if st == nil {
		s.mu.Lock()
		hidden := s.sidebarCollapsed
		s.mu.Unlock()
		if hidden {
			return 0
		}
		return 1
	}
	return st.sidebarSlide
}

// dismissCompactSuggestion 记录指定会话已忽略压缩建议，本会话不再提示。
func dismissCompactSuggestion(s *chatState, threadID string) {
	if s == nil || threadID == "" {
		return
	}
	st := getChatUIState(s)
	if st == nil {
		return
	}
	chatUIMu.Lock()
	st.compactDismissed[threadID] = true
	chatUIMu.Unlock()
}

// compactSuggestionDismissed 返回指定会话是否已忽略压缩建议。
func compactSuggestionDismissed(s *chatState, threadID string) bool {
	if s == nil || threadID == "" {
		return false
	}
	chatUIMu.Lock()
	defer chatUIMu.Unlock()
	st := chatUIStates[s]
	return st != nil && st.compactDismissed[threadID]
}

// dismissForwardSuggestion 记录指定会话已忽略转发建议，本会话不再提示。
func dismissForwardSuggestion(s *chatState, threadID string) {
	if s == nil || threadID == "" {
		return
	}
	st := getChatUIState(s)
	if st == nil {
		return
	}
	chatUIMu.Lock()
	st.forwardSuggestDismissed[threadID] = true
	chatUIMu.Unlock()
}

// forwardSuggestionDismissed 返回指定会话是否已忽略转发建议。
func forwardSuggestionDismissed(s *chatState, threadID string) bool {
	if s == nil || threadID == "" {
		return false
	}
	chatUIMu.Lock()
	defer chatUIMu.Unlock()
	st := chatUIStates[s]
	return st != nil && st.forwardSuggestDismissed[threadID]
}

func compactSuggestDisabledSession() config.SessionConfig {
	disabled := false
	return config.SessionConfig{AskCompactBeforeResume: &disabled}
}

func copySessionConfigForCompact(s config.SessionConfig) config.SessionConfig {
	enabled := s.AskCompactOnResume()
	s.AskCompactBeforeResume = &enabled
	return s
}

const fallbackConfigCacheTTL = 2 * time.Second

type fallbackConfigSnapshot struct {
	dataDir  string
	cfg      config.Config
	loaded   bool
	cachedAt time.Time
}

var (
	fallbackConfigCache    atomic.Pointer[fallbackConfigSnapshot]
	fallbackConfigInFlight atomic.Bool
	// loadFallbackConfigFunc 供测试替换持久化配置加载，默认调用 config.Load。
	loadFallbackConfigFunc = config.Load
	// fallbackConfigNow 供测试推进异步配置缓存时钟，默认返回 time.Now。
	fallbackConfigNow = time.Now
)

// cachedFallbackConfig 在 getChatSettings(s) 为 nil 时只读进程内缓存的持久化配置；
// 缓存缺失或超过 2 秒时最多启动一个后台协程加载，加载完成后写入缓存并触发重绘。
func cachedFallbackConfig(s *chatState) (config.Config, bool) {
	dataDir := resolveDataDirFallback()
	snap := fallbackConfigCache.Load()
	sameDir := snap != nil && snap.dataDir == dataDir
	fresh := sameDir && !snap.cachedAt.IsZero() && fallbackConfigNow().Sub(snap.cachedAt) < fallbackConfigCacheTTL
	if !fresh && dataDir != "" {
		startFallbackConfigLoad(s, dataDir)
	}
	if sameDir && snap.loaded {
		return snap.cfg, true
	}
	return config.Config{}, false
}

func startFallbackConfigLoad(s *chatState, dataDir string) {
	if dataDir == "" || !fallbackConfigInFlight.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer fallbackConfigInFlight.Store(false)
		cfg, err := loadFallbackConfigFunc(dataDir)
		next := &fallbackConfigSnapshot{
			dataDir:  dataDir,
			cachedAt: fallbackConfigNow(),
		}
		if err == nil {
			next.cfg = cfg
			next.loaded = true
		}
		fallbackConfigCache.Store(next)
		if s != nil {
			s.invalidate()
		}
	}()
}

// compactSuggestSession 读取压缩建议所需的会话配置。
// 与 composerSendKey 一致：优先读内存中的设置面板配置，
// 缺失时通过后台缓存读取持久化配置，缓存未就绪时返回禁用默认配置。
func (s *chatState) compactSuggestSession() config.SessionConfig {
	if s == nil {
		return compactSuggestDisabledSession()
	}
	if st := getChatSettings(s); st != nil {
		st.mu.Lock()
		defer st.mu.Unlock()
		return copySessionConfigForCompact(st.cfg.Session)
	}
	if cfg, ok := cachedFallbackConfig(s); ok {
		return copySessionConfigForCompact(cfg.Session)
	}
	return compactSuggestDisabledSession()
}

// sidebarProjectOrder 返回当前窗口保存的项目顺序副本。
func sidebarProjectOrder(s *chatState) []string {
	if s == nil {
		return nil
	}
	st := getChatUIState(s)
	if st == nil {
		return nil
	}
	chatUIMu.Lock()
	defer chatUIMu.Unlock()
	return append([]string(nil), st.projectOrder...)
}

// storeSidebarProjectOrder 写入内存中的项目顺序，不落盘（启动恢复时用）。
func storeSidebarProjectOrder(s *chatState, order []string) {
	if s == nil {
		return
	}
	st := getChatUIState(s)
	if st == nil {
		return
	}
	chatUIMu.Lock()
	st.projectOrder = append([]string(nil), order...)
	chatUIMu.Unlock()
}

// sidebarProjectActivation 返回当前窗口保存的项目激活覆盖表副本；无覆盖时返回 nil。
func sidebarProjectActivation(s *chatState) map[string]bool {
	if s == nil {
		return nil
	}
	st := getChatUIState(s)
	if st == nil {
		return nil
	}
	chatUIMu.Lock()
	defer chatUIMu.Unlock()
	if len(st.projectActivation) == 0 {
		return nil
	}
	cp := make(map[string]bool, len(st.projectActivation))
	for id, active := range st.projectActivation {
		cp[id] = active
	}
	return cp
}

// storeSidebarProjectActivation 写入内存中的项目激活覆盖表，不落盘（启动恢复时用）。
func storeSidebarProjectActivation(s *chatState, m map[string]bool) {
	if s == nil {
		return
	}
	st := getChatUIState(s)
	if st == nil {
		return
	}
	var cp map[string]bool
	if len(m) > 0 {
		cp = make(map[string]bool, len(m))
		for id, active := range m {
			cp[id] = active
		}
	}
	chatUIMu.Lock()
	st.projectActivation = cp
	chatUIMu.Unlock()
}

// applyProjectOrder 纯函数：按保存的顺序重排项目列表。
// 未出现在 order 中的项目按原顺序追加到末尾；假设项目 ID 唯一。
func applyProjectOrder(projects []Project, order []string) []Project {
	if len(projects) == 0 || len(order) == 0 {
		return projects
	}
	byID := make(map[string]Project, len(projects))
	for _, p := range projects {
		byID[p.ID] = p
	}
	result := make([]Project, 0, len(projects))
	seen := make(map[string]bool, len(projects))
	for _, id := range order {
		p, ok := byID[id]
		if !ok || seen[id] {
			continue
		}
		result = append(result, p)
		seen[id] = true
	}
	for _, p := range projects {
		if seen[p.ID] {
			continue
		}
		result = append(result, p)
		seen[p.ID] = true
	}
	return result
}

// --- 模型记忆的跨包入口（供 internal/app 注入远程控制后端使用）---
// 以下包装函数只转发原有实现，不改变读写行为：远程端新建会话时据此读取与写回与桌面同一份
// ui-state.json 记忆，并复用同一份进程内缓存，避免远端写回后桌面读到旧值。

// LoadLastModelSelection 返回最近一次确认的模型选择（provider/model/effort/fast），没有记录时 ok 为 false。
func LoadLastModelSelection() (ModelSelection, bool) { return loadLastModelSelection() }

// LoadLastSwarmSelection 返回最近一次确认的编排开关与执行侧选择。
func LoadLastSwarmSelection() LastSwarmSelection { return loadLastSwarmSelection() }

// SaveLastModelSelection 按场景记录最近一次模型选择并写回 ui-state.json（projectID 为空表示快速对话）。
func SaveLastModelSelection(projectID string, sel ModelSelection) {
	saveLastModelSelection(projectID, sel)
}

// RememberSwarmEnabled 按场景记录最近一次编排总开关选择（projectID 为空表示快速对话）。
func RememberSwarmEnabled(projectID string, on bool) { rememberSwarmEnabled(projectID, on) }

// RememberSwarmBees 按场景记录最近一次执行侧选择（projectID 为空表示快速对话；provider/model 为空表示跟随主导）。
func RememberSwarmBees(projectID, provider, model, effort string) {
	rememberSwarmBees(projectID, provider, model, effort)
}

// ProviderEnabledMap 返回配置里各 provider 的启用状态（键为 provider kind，false 表示已关闭）。
// 与桌面模型选择器使用的同一张表，供远程端过滤已关闭的 provider。
func ProviderEnabledMap(cfg config.Config) map[string]bool { return providerEnabledMap(cfg, nil, nil) }
