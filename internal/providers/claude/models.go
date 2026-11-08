package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
	"mcode/internal/providers/modelcache"
	"mcode/internal/startuptrace"
)

// Model 是 Claude CLI 在 initialize 握手中报告的模型目录里的一行。
type Model struct {
	ID                  string
	Name                string
	Description         string
	ResolvedModel       string
	Efforts             []string
	SupportsFast        bool
	SupportsAutoMode    *bool
	SupportsThinkingOff *bool
}

// modelsForLookup 是模型目录的读取入口；单独抽成变量便于测试注入，避免真的去起握手进程。
var modelsForLookup = CachedModels

// autoModeSupported 按模型 id 查目录里该模型的 supportsAutoMode。
// model 为空或 "default" 时按 value 为 "default" 的那条（默认模型）判断；其余先按 value、
// 再按 resolvedModel 匹配。目录未加载、找不到条目或条目没报告该字段时 known 为 false，
// 调用方据此保持原样，不冒险降级。
func autoModeSupported(model string) (supported bool, known bool) {
	models := modelsForLookup()
	if len(models) == 0 {
		return false, false
	}
	if model == "" || model == "default" {
		for _, entry := range models {
			if entry.ID == "default" {
				return entry.autoModeFlag()
			}
		}
		return false, false
	}
	for _, entry := range models {
		if entry.ID == model || (entry.ResolvedModel != "" && entry.ResolvedModel == model) {
			return entry.autoModeFlag()
		}
	}
	return false, false
}

// autoModeFlag 把目录条目的 supportsAutoMode 拆成取值与“目录是否给了该字段”：
// 指针为 nil 表示旧版 CLI 没报告，按未知处理。
func (m Model) autoModeFlag() (supported bool, known bool) {
	if m.SupportsAutoMode == nil {
		return false, false
	}
	return *m.SupportsAutoMode, true
}

// handshakeRequestID 是 initialize 控制请求的 id，回复里靠它认出握手结果。
const handshakeRequestID = "handshake"

// handshakeArgs 是一次性握手的启动参数：只跑 initialize，不写会话记录。
var handshakeArgs = []string{
	"-p",
	"--input-format", "stream-json",
	"--output-format", "stream-json",
	"--verbose",
	"--no-session-persistence",
	"--setting-sources", "user,project,local",
}

// handshakeRequest 是写进 stdin 的那一行 initialize 控制请求。
const handshakeRequest = `{"type":"control_request","request_id":"handshake","request":{"subtype":"initialize","hooks":null}}`

// handshakeTimeout 是一次握手的总超时。
const handshakeTimeout = 20 * time.Second

var (
	catalogMu     sync.RWMutex
	catalogBinary string
	modelCache    = &modelcache.Cache[Model]{}
)

// SetCatalogBinary 更新自动探测使用的 CLI，路径变化时丢弃旧目录与失败退避。
func SetCatalogBinary(binary string) {
	catalogMu.Lock()
	defer catalogMu.Unlock()
	if catalogBinary != binary {
		catalogBinary = binary
		modelCache = &modelcache.Cache[Model]{}
	}
}

// catalogFetch 是后台探测入口；单独抽成变量便于测试替换，避免真的启动本机 CLI。
var catalogFetch = func(ctx context.Context) ([]Model, error) {
	models, err := fetchModels(ctx)
	if err != nil {
		log.Printf("claude: 模型目录探测失败: %v", err)
	}
	return models, err
}

var catalogEnviron = loginenv.Environ
var catalogFetchWithBinary = fetchModelsWithBinary

// catalogSnapshot 是 CatalogNotice 读取探测状态的入口；单独抽成变量便于测试注入。
var catalogSnapshot = CachedCatalog

// loginPresent 报告本机是否有 Claude Code 登录凭据；单独抽成变量便于测试替换。
var loginPresent = cachedLoginPresent

var (
	loginPresentOnce  sync.Once
	loginPresentValue bool
)

// cachedLoginPresent 按进程缓存登录检测结果：readLogin 在 macOS 上可能拉起 security 子进程，
// 模型选择器反复扫描时不该每次重复启动它。
func cachedLoginPresent() bool {
	loginPresentOnce.Do(func() {
		_, err := readLogin()
		loginPresentValue = err == nil
	})
	return loginPresentValue
}

// CachedCatalog 返回模型目录与最近一次探测状态；尚未拿到时在后台启动一次握手。
func CachedCatalog() modelcache.Snapshot[Model] {
	catalogMu.RLock()
	snap := modelCache.Peek(catalogFetch)
	catalogMu.RUnlock()
	if len(snap.Models) > 0 {
		// 首次拿到目录时打点：启动路径只读缓存，握手在后台跑。
		startuptrace.Mark("providers: claude models ready")
	}
	return snap
}

// CachedModels 返回握手拿到的模型目录；尚未拿到时返回 nil，并在后台启动一次握手
// （同时只跑一个，找不到 CLI 或失败后 10 分钟内不再重试）。
func CachedModels() []Model {
	return CachedCatalog().Models
}

// ResetCatalogCacheForTest 把进程级模型目录缓存重置为「从未探测」，并把后台探测换成必然失败的桩，
// 返回的函数用于恢复原状态。仅供测试使用：同进程里其他用例可能已经写入缓存或触发真实探测，
// 不重置时读到的目录会随执行顺序变化。
func ResetCatalogCacheForTest() func() {
	catalogMu.Lock()
	prevCache, prevFetch, prevBinary := modelCache, catalogFetch, catalogBinary
	modelCache = &modelcache.Cache[Model]{}
	catalogFetch = func(context.Context) ([]Model, error) {
		return nil, errors.Join(modelcache.ErrNotInstalled, errors.New("测试环境已禁用模型探测"))
	}
	catalogMu.Unlock()
	return func() {
		catalogMu.Lock()
		modelCache, catalogFetch, catalogBinary = prevCache, prevFetch, prevBinary
		catalogMu.Unlock()
	}
}

// CatalogNotice 返回模型目录为空时的原因文案；目录可用时返回空串。
func CatalogNotice() string {
	snap := catalogSnapshot()
	if len(snap.Models) > 0 {
		return ""
	}
	if snap.Probing {
		return "正在探测 Claude Code 模型…"
	}
	if errors.Is(snap.Err, modelcache.ErrNotInstalled) {
		return "未安装 Claude Code CLI"
	}
	if snap.Err != nil && !UsesThirdPartyAPI(catalogEnviron()) && !loginPresent() {
		return "未登录 Claude Code"
	}
	return "探测 Claude Code 模型失败"
}

// RefreshModels 强制重新读取 Claude CLI 的模型目录。
func RefreshModels(ctx context.Context, binary string) error {
	catalogMu.RLock()
	cache := modelCache
	catalogMu.RUnlock()
	return cache.Refresh(ctx, func(ctx context.Context) ([]Model, error) {
		return fetchModelsWithBinary(ctx, binary)
	})
}

// fetchModels 起一个一次性 CLI 进程做 initialize 握手，读出它报告的模型目录。
func fetchModels(ctx context.Context) ([]Model, error) {
	catalogMu.RLock()
	binary := catalogBinary
	catalogMu.RUnlock()
	if binary == "" {
		binary = DefaultBinary
	}
	return catalogFetchWithBinary(ctx, binary)
}

func fetchModelsWithBinary(ctx context.Context, binary string) ([]Model, error) {
	if binary == "" {
		binary = DefaultBinary
	}
	bin, err := loginenv.LookPath(binary)
	if err != nil {
		return nil, fmt.Errorf("claude: 未找到 %s: %w", binary, errors.Join(modelcache.ErrNotInstalled, err))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("claude: 获取用户主目录失败: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()

	cmd := procattr.HideWindow(exec.CommandContext(ctx, bin, handshakeArgs...))
	// 与 runOnce 一致：独立进程组、超时按组强杀、WaitDelay 兜底未退出的子进程。
	setProcessGroup(cmd)
	cmd.Cancel = func() error {
		return killProcessGroup(cmd, syscall.SIGTERM)
	}
	cmd.WaitDelay = 3 * time.Second
	cmd.Dir = home
	// 与 runOnce 一致：从登录环境构造，并注入指向工作目录的 PWD。
	cmd.Env = buildEnv(loginenv.Environ(), home, nil)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("claude: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("claude: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("claude: start: %w", err)
	}
	// 拿到结果、失败或超时都要杀掉进程并等它退出，避免握手进程残留在后台。
	defer func() {
		_ = killProcessGroup(cmd, syscall.SIGKILL)
		_ = cmd.Wait()
	}()

	// 只发 initialize，不关闭 stdin：CLI 收到 initialize 后就会回 control_response。
	if _, err := stdin.Write([]byte(handshakeRequest + "\n")); err != nil {
		return nil, fmt.Errorf("claude: write handshake: %w", err)
	}

	reader := bufio.NewReader(stdout)
	for {
		line, readErr := reader.ReadBytes('\n')
		if models, ok := parseHandshakeModels(line); ok {
			return models, nil
		}
		if readErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, fmt.Errorf("claude: 握手超时: %w", ctxErr)
			}
			return nil, fmt.Errorf("claude: 握手未收到模型目录: %w", readErr)
		}
	}
}

// parseHandshakeModels 解析一行 stream-json 输出：只认 request_id 为 handshake 的
// control_response，返回它的模型目录以及是否命中。拆成纯函数便于单元测试。
func parseHandshakeModels(line []byte) ([]Model, bool) {
	var msg struct {
		Type     string `json:"type"`
		Response *struct {
			RequestID string `json:"request_id"`
			Response  struct {
				Models []struct {
					Value                 string   `json:"value"`
					ResolvedModel         string   `json:"resolvedModel"`
					DisplayName           string   `json:"displayName"`
					Description           string   `json:"description"`
					SupportedEffortLevels []string `json:"supportedEffortLevels"`
					SupportsFastMode      bool     `json:"supportsFastMode"`
					SupportsAutoMode      *bool    `json:"supportsAutoMode"`
					SupportsThinkingOff   *bool    `json:"supportsThinkingOff"`
				} `json:"models"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(line), &msg); err != nil {
		return nil, false
	}
	if msg.Type != "control_response" || msg.Response == nil || msg.Response.RequestID != handshakeRequestID {
		return nil, false
	}

	var models []Model
	for _, entry := range msg.Response.Response.Models {
		// 没有 value 的行无法作为模型 id 使用，跳过。
		if entry.Value == "" {
			continue
		}
		name := entry.DisplayName
		if name == "" {
			name = entry.Value
		}
		models = append(models, Model{
			ID:                  entry.Value,
			Name:                name,
			Description:         entry.Description,
			ResolvedModel:       entry.ResolvedModel,
			Efforts:             entry.SupportedEffortLevels,
			SupportsFast:        entry.SupportsFastMode,
			SupportsAutoMode:    entry.SupportsAutoMode,
			SupportsThinkingOff: entry.SupportsThinkingOff,
		})
	}
	return models, true
}
