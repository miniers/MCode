package remote

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"mcode/internal/providers/acp"
	"mcode/internal/providers/copilot"
	"mcode/internal/providers/loginenv"
	"mcode/internal/providers/omp"
	"mcode/internal/providers/pi"
	"mcode/internal/remote/wire"
	"mcode/internal/startuptrace"
)

const (
	// cliCatalogFreshness 是 CLI 目录成功拉取后多久重新读取。
	cliCatalogFreshness = 6 * time.Hour
	// cliCatalogRetry 是拉取失败（找不到 CLI、未登录、超时）后再次尝试的间隔。
	cliCatalogRetry = 10 * time.Minute
)

// cliCatalog 缓存某个 CLI provider 的账号模型目录：读取时立即返回已缓存的目录，
// 缺失或过期时起一次后台探测；探测期间沿用旧目录，一行旧目录都没有时返回空，
// 由调用方给占位行，等目录落地后由上层再取一次。
type cliCatalog struct {
	mu        sync.Mutex
	models    []wire.ProviderModel
	fetchedAt time.Time
	failedAt  time.Time
	fetching  bool
	fetchGen  uint64
	// gen 是失效代数：invalidate 时加一，正在拉取的协程发现代数变了就丢弃这批结果
	// 重新拉取，避免“作废前发出的请求”把新目录顶掉、导致又要等一整个新鲜期。
	gen uint64
}

func (c *cliCatalog) get(fetch func(context.Context) ([]wire.ProviderModel, error)) []wire.ProviderModel {
	c.mu.Lock()
	stale := len(c.models) == 0 || time.Since(c.fetchedAt) >= cliCatalogFreshness
	retryOK := c.failedAt.IsZero() || time.Since(c.failedAt) >= cliCatalogRetry
	if stale && retryOK && !c.fetching {
		c.fetching = true
		c.fetchGen = c.gen
		go c.refresh(fetch, c.gen)
	}
	models := append([]wire.ProviderModel(nil), c.models...)
	c.mu.Unlock()
	return models
}

func (c *cliCatalog) snapshot() []wire.ProviderModel {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]wire.ProviderModel(nil), c.models...)
}

func (c *cliCatalog) refresh(fetch func(context.Context) ([]wire.ProviderModel, error), gen uint64) {
	models, err := safeCatalogFetch(context.Background(), fetch)
	c.mu.Lock()
	if c.gen != gen {
		// 拉取期间目录被作废（例如注入的 Magpie 模型变了）：这批结果来自旧配置，
		// 直接作废并立刻按当前代数重拉一次。
		start := c.fetchGen != c.gen
		if start {
			c.fetching, c.fetchGen = true, c.gen
		}
		next := c.gen
		c.mu.Unlock()
		if start {
			go c.refresh(fetch, next)
		}
		return
	}
	c.fetching = false
	if err != nil || len(models) == 0 {
		c.failedAt = time.Now()
	} else {
		c.models, c.fetchedAt, c.failedAt = models, time.Now(), time.Time{}
	}
	c.mu.Unlock()
}

func safeCatalogFetch(ctx context.Context, fetch func(context.Context) ([]wire.ProviderModel, error)) (models []wire.ProviderModel, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("获取模型目录 panic：%v", r)
			log.Print(err)
		}
	}()
	return fetch(ctx)
}

// invalidate 让下一次读取重新拉取：清掉时间戳、作废在途结果，并立即起一次后台拉取，
// 使新目录尽快落地（调用方之后的读取就能拿到）。fetch 由调用方给出，便于测试替换。
func (c *cliCatalog) invalidate(fetch func(context.Context) ([]wire.ProviderModel, error)) {
	c.mu.Lock()
	c.gen++
	c.fetchedAt = time.Time{}
	c.failedAt = time.Time{}
	start := !c.fetching
	if start {
		c.fetching = true
		c.fetchGen = c.gen
	}
	gen := c.gen
	c.mu.Unlock()
	if start {
		go c.refresh(fetch, gen)
	}
}

// reload 强制重拉目录，期间旧的后台请求不能覆盖结果。
func (c *cliCatalog) reload(ctx context.Context, fetch func(context.Context) ([]wire.ProviderModel, error)) error {
	c.mu.Lock()
	c.gen++
	gen := c.gen
	c.fetching, c.fetchGen = true, gen
	c.mu.Unlock()
	models, err := safeCatalogFetch(ctx, fetch)
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && len(models) == 0 {
		err = errors.New("模型目录为空")
	}
	c.mu.Lock()
	if c.gen != gen {
		start := c.fetchGen != c.gen
		if start {
			c.fetching, c.fetchGen = true, c.gen
		}
		next := c.gen
		c.mu.Unlock()
		if start {
			go c.refresh(fetch, next)
		}
		return errors.New("刷新期间模型目录已变更")
	}
	c.fetching = false
	if err != nil {
		c.failedAt = time.Now()
	} else {
		c.models, c.fetchedAt, c.failedAt = models, time.Now(), time.Time{}
	}
	c.mu.Unlock()
	return err
}

var (
	piCatalog        cliCatalog
	ompCatalog       cliCatalog
	copilotCatalog   cliCatalog
	droidCatalog     cliCatalog
	otherACPCatalogs = map[string]*cliCatalog{
		"cursor": {}, "opencode": {}, "grok": {}, "devin": {},
	}
)

// piMagpieInjection 是取 pi 模型目录时的 Magpie 注入源，由 app 层在启动时设置：
// 返回 Magpie 桥扩展路径与 MCODE_PI_MAGPIE 取值，任一为空表示按 pi 自己的配置取目录。
// 写入只在启动时发生一次，读取发生在后台拉取协程里，用读写锁隔开。
var (
	piMagpieHookMu    sync.RWMutex
	piMagpieInjection func() (bridge, env string)
)

// piStaleModel 判断 pi 线程保存的模型是否已因直连失效：模型属于 mcode 注入的 Magpie provider，
// 但本轮没有注入，pi 不认识它。由 app 层在启动时设置，读写锁隔开。
var (
	piStaleModelMu sync.RWMutex
	piStaleModel   func(model string) bool
)

// SetPiStaleModel 设置 pi 直连时失效模型的判定函数（由 app 层在启动时调用）。
func SetPiStaleModel(fn func(model string) bool) {
	piStaleModelMu.Lock()
	piStaleModel = fn
	piStaleModelMu.Unlock()
}

func isPiStaleModel(model string) bool {
	piStaleModelMu.RLock()
	hook := piStaleModel
	piStaleModelMu.RUnlock()
	return hook != nil && hook(model)
}

var (
	magpieCatalogMu  sync.RWMutex
	magpieCatalog    func(string) ([]wire.ProviderModel, bool)
	piInjectedRowsMu sync.RWMutex
	piInjectedRows   []wire.ProviderModel
)

// SetMagpieCatalog 设置按 agent 提供 Magpie 目录的钩子。
func SetMagpieCatalog(fn func(string) ([]wire.ProviderModel, bool)) {
	magpieCatalogMu.Lock()
	magpieCatalog = fn
	magpieCatalogMu.Unlock()
}

func magpieCatalogRows(agent string) ([]wire.ProviderModel, bool) {
	magpieCatalogMu.RLock()
	hook := magpieCatalog
	magpieCatalogMu.RUnlock()
	if hook == nil {
		return nil, false
	}
	return hook(agent)
}

// SetPiMagpieInjection 设置 pi 模型目录要用的 Magpie 注入源（由 app 层在启动时调用）。
// 注入源就绪前可能已经按 pi 自己的配置拉过目录（启动早期的读取会触发后台拉取），
// 因此这里同时作废一次 pi 目录，保证目录按注入后的配置重新读取。
func SetPiMagpieInjection(fn func() (bridge, env string)) {
	piMagpieHookMu.Lock()
	piMagpieInjection = fn
	piMagpieHookMu.Unlock()
	InvalidatePiCatalog()
}

// InvalidatePiCatalog 作废 pi 模型目录并立即后台重拉（用户在设置里改了注入 pi 的模型后调用，
// 注入源变更时也调用）：下一次读取就能拿注入后的目录，不用再等一次冷启动。
func InvalidatePiCatalog() {
	piCatalog.invalidate(fetchPiModels)
}

// piMagpieCatalogInjection 返回本次取 pi 模型目录要用的桥路径与端点清单，都做过去空白。
func piMagpieCatalogInjection() (bridge, env string) {
	piMagpieHookMu.RLock()
	hook := piMagpieInjection
	piMagpieHookMu.RUnlock()
	if hook == nil {
		return "", ""
	}
	bridge, env = hook()
	return strings.TrimSpace(bridge), strings.TrimSpace(env)
}

// cliCatalogModels 返回 pi / omp / copilot / droid 的账号模型目录；尚未取到时返回 nil。
func cliCatalogModels(provider string) []wire.ProviderModel {
	switch provider {
	case "pi":
		return piCatalog.get(fetchPiModels)
	case "omp":
		return ompCatalog.get(fetchOmpModels)
	case "copilot":
		return copilotCatalog.get(fetchCopilotModels)
	case "droid":
		return droidCatalog.get(fetchDroidModels)
	}
	return nil
}

func fetchPiModels(ctx context.Context) ([]wire.ProviderModel, error) {
	return fetchPiModelsWithBinary(ctx, "")
}

func fetchPiModelsWithBinary(ctx context.Context, binary string) ([]wire.ProviderModel, error) {
	if binary == "" {
		binary = pi.DefaultBinary
	}
	bin, err := loginenv.LookPath(binary)
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for _, kv := range loginenv.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	// 原生目录单独缓存，避免切换到 off 时沿用旧的注入模型。
	models, err := pi.ListModels(ctx, bin, env, loginenv.HomeDirectory())
	bridge, magpieEnv := piMagpieCatalogInjection()
	if bridge != "" && magpieEnv != "" {
		injected, injectErr := pi.ListModelsWith(ctx, bin, env, loginenv.HomeDirectory(), bridge, magpieEnv)
		if injectErr == nil {
			piInjectedRowsMu.Lock()
			piInjectedRows = piModelRows(injected)
			piInjectedRowsMu.Unlock()
		}
	}
	if err != nil {
		return nil, err
	}
	rows := withDefaultRow(piModelRows(models))
	startuptrace.Mark("providers: pi models ready")
	return rows, nil
}

// piModelRows 把 pi 的模型目录转成协议行：档位原样带过去（Efforts），
// 默认档位只在 pi 给出非空值时下发，供界面渲染推理强度卡片；
// 上下文窗口已知时带上，界面据此画上下文占用圆环。
func piModelRows(models []pi.Model) []wire.ProviderModel {
	out := make([]wire.ProviderModel, 0, len(models))
	for _, m := range models {
		row := wire.ProviderModel{ID: m.ID, Name: m.Name, Detail: optionalString(m.Provider), Efforts: m.ThinkingLevels, IsDefault: m.IsDefault}
		if m.ContextWindow > 0 {
			row.ContextLimit = m.ContextWindow
		}
		if m.DefaultThinking != "" {
			row.DefaultEffort = stringPtr(m.DefaultThinking)
		}
		out = append(out, row)
	}
	return out
}

func fetchOmpModels(ctx context.Context) ([]wire.ProviderModel, error) {
	return fetchOmpModelsWithBinary(ctx, "")
}

func fetchOmpModelsWithBinary(ctx context.Context, binary string) ([]wire.ProviderModel, error) {
	if binary == "" {
		binary = omp.DefaultBinary
	}
	bin, err := loginenv.LookPath(binary)
	if err != nil {
		return nil, err
	}
	models, err := omp.ListModels(ctx, bin)
	if err != nil {
		return nil, err
	}
	out := make([]wire.ProviderModel, 0, len(models))
	for _, m := range models {
		row := wire.ProviderModel{ID: m.ID, Name: m.Name, Detail: optionalString(m.Provider), Efforts: m.Efforts}
		if m.DefaultEffort != "" {
			row.DefaultEffort = stringPtr(m.DefaultEffort)
		}
		out = append(out, row)
	}
	startuptrace.Mark("providers: omp models ready")
	return withDefaultRow(out), nil
}

func fetchCopilotModels(ctx context.Context) ([]wire.ProviderModel, error) {
	return fetchCopilotModelsWithBinary(ctx, "")
}

func fetchCopilotModelsWithBinary(ctx context.Context, binary string) ([]wire.ProviderModel, error) {
	if binary == "" {
		binary = copilot.DefaultBinary()
	}
	bin, err := loginenv.LookPath(binary)
	if err != nil {
		return nil, err
	}
	models, err := copilot.ListModels(ctx, bin, loginenv.Environ())
	if err != nil {
		return nil, err
	}
	out := make([]wire.ProviderModel, 0, len(models))
	for _, m := range models {
		row := wire.ProviderModel{ID: m.ID, Name: m.Name, Detail: optionalString(m.Detail), Efforts: m.Efforts, IsDefault: m.ID == copilot.AutoModelID}
		if m.DefaultEffort != "" {
			row.DefaultEffort = stringPtr(m.DefaultEffort)
		}
		out = append(out, row)
	}
	startuptrace.Mark("providers: copilot models ready")
	return out, nil
}

func fetchDroidModels(ctx context.Context) ([]wire.ProviderModel, error) {
	return fetchDroidModelsWithBinary(ctx, "")
}

func fetchDroidModelsWithBinary(ctx context.Context, binary string) ([]wire.ProviderModel, error) {
	if binary == "" {
		binary = acp.DefaultBinary(acp.AgentDroid)
	}
	bin, err := loginenv.LookPath(binary)
	if err != nil {
		return nil, err
	}
	models, err := acp.ListDroidModels(ctx, bin)
	if err != nil {
		return nil, err
	}
	out := make([]wire.ProviderModel, 0, len(models))
	for _, m := range models {
		row := wire.ProviderModel{ID: m.ID, Name: m.Name, Detail: optionalString(m.Provider), Efforts: m.Efforts, IsDefault: m.Default}
		if m.DefaultEffort != "" {
			row.DefaultEffort = stringPtr(m.DefaultEffort)
		}
		out = append(out, row)
	}
	startuptrace.Mark("providers: droid models ready")
	return withDefaultRow(out), nil
}

// withDefaultRow 保证目录里有且只有一行默认：CLI 没标出当前模型时，在最前面补“默认”占位行
// （选它不传 --model，由 CLI 用自己的默认模型）。
func withDefaultRow(models []wire.ProviderModel) []wire.ProviderModel {
	for _, m := range models {
		if m.IsDefault {
			return models
		}
	}
	return append([]wire.ProviderModel{placeholderModel()}, models...)
}
