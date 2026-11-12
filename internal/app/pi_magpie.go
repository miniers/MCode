package app

// 本文件把 mcode 设置里的 Magpie 端点接给 pi：勾选了模型的端点按 pi 的 models.json
// 形状经 MCODE_PI_MAGPIE 注入，桥扩展随 `-e` 加载；桥回传的实际路由账号用于在额度
// 窗口上标出当前在用的卡片。端点配置每次启动 pi / 取模型目录时重新读取，改完设置
// 下一次就生效。apiKey 只随环境变量走，不进命令行参数。

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"mcode/internal/config"
	"mcode/internal/magpiecfg"
	"mcode/internal/magpieinject"
	"mcode/internal/providers/magpie"
	"mcode/internal/providers/pi"
	"mcode/internal/remote"
	mcodeui "mcode/internal/ui"
)

// piMagpieFetchTimeout 是读取 Magpie 模型目录与额度的超时：这两处都在交互路径上
// （pi 启动、额度面板刷新），取不到就按勾选的模型补默认值，不长时间阻塞。
// 包级变量便于单元测试中缩短。
var piMagpieFetchTimeout = 5 * time.Second

// piMagpieDefaultModel 是网关没列出某个已勾选模型时用的默认元数据，
// 保证勾了就能在 pi 里选到（网关暂时不可用时也不至于少模型）。
var piMagpieDefaultModel = magpieinject.DefaultModel()

// piMagpieTarget 是一个启用的 Magpie 自定义端点及其 pi 注入所需的派生值。
type piMagpieTarget struct {
	endpoint   config.CustomEndpointConfig
	baseURL    string
	apiKey     string
	providerID string
	// models 是 pi 在该端点勾选的网关模型 ID。
	models []string
}

// injectable 表示该端点可以注入 pi：有 API Key 且勾选了模型。
// 没配 key 或没勾模型的端点仍分配 providerID，只是这一轮不注入。
func (t piMagpieTarget) injectable() bool {
	return t.apiKey != "" && len(t.models) > 0
}

// piMagpieTargets 按配置顺序列出 pi 勾选过的启用 Magpie 端点并分配 pi provider id。
// 端点枚举、providerID 分配与 base URL 归一化统一由 magpieinject 完成，
// 保证同一个端点在 pi、claude 里前缀一致；非 Magpie 与未启用的端点不参与注入。
func piMagpieTargets(cfg config.Config, secrets config.SecretStore) []piMagpieTarget {
	setting := config.MagpieAgentSetting(cfg, config.ProviderPi)
	return piMagpieTargetsWith(cfg, secrets, setting, setting.Selections)
}

// piMagpieAllTargets 列出全部启用的 Magpie 端点（不按勾选过滤），供设置页展示可选模型。
func piMagpieAllTargets(cfg config.Config, secrets config.SecretStore) []piMagpieTarget {
	setting := config.MagpieAgentSetting(cfg, config.ProviderPi)
	return piMagpieTargetsWith(cfg, secrets, setting, nil)
}

func piMagpieTargetsWith(cfg config.Config, secrets config.SecretStore, setting config.MagpieAgentConfig, selections []config.MagpieSelection) []piMagpieTarget {
	byID := make(map[string]config.CustomEndpointConfig, len(cfg.Providers.Custom))
	for _, ep := range cfg.Providers.Custom {
		byID[ep.ID] = ep
	}

	targets := magpieinject.Targets(cfg, secrets, selections)
	res := make([]piMagpieTarget, 0, len(targets))
	for _, target := range targets {
		ep := byID[target.Endpoint]
		models := magpieSelectionModels(setting, target.Endpoint)
		// 旧配置只有 PiModels、还没有 pi 勾选字段时沿用旧值，避免升级后一启动就丢模型。
		if len(models) == 0 {
			models = append([]string(nil), ep.PiModels...)
		}
		res = append(res, piMagpieTarget{
			endpoint:   ep,
			baseURL:    target.BaseURL,
			apiKey:     target.APIKey,
			providerID: target.ProviderID,
			models:     models,
		})
	}
	return res
}

// magpieSelectionModels 返回某 agent 在某端点勾选的模型副本。
func magpieSelectionModels(setting config.MagpieAgentConfig, endpointID string) []string {
	for _, selection := range setting.Selections {
		if selection.EndpointID == endpointID {
			return append([]string(nil), selection.Models...)
		}
	}
	return nil
}

// buildPiMagpieEnv 生成这一轮要注入 pi 的 MCODE_PI_MAGPIE 取值（[]PiProvider 的 JSON）。
// 只有 inject 模式才注入：global 由 pi 自己的 models.json 提供模型，off 是直连。
// 只有可注入（有 key 且勾了模型）的端点参与；一个都没有时返回空串，表示本轮不注入、
// pi 完全用它自己的配置。
func buildPiMagpieEnv(ctx context.Context, cfg config.Config, secrets config.SecretStore) string {
	routing := ResolveAgentRouting(cfg, secrets, config.ProviderPi)
	if routing.Mode != config.MagpieModeInject || !routing.Active() {
		return ""
	}
	providers := make([]magpie.PiProvider, 0, len(routing.Targets))
	for _, target := range routing.Targets {
		var ids []string
		for _, model := range routing.Models {
			if model.EndpointID == target.Endpoint {
				ids = append(ids, model.ModelID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		fetchCtx, cancel := context.WithTimeout(ctx, piMagpieFetchTimeout)
		models, err := magpie.FetchModels(fetchCtx, target.BaseURL, target.APIKey)
		cancel()
		if err != nil {
			log.Printf("pi magpie: 读取端点 %q 的模型失败，按勾选的模型补默认值: %v", target.Endpoint, err)
		}
		providers = append(providers, magpie.PiProvider{
			Provider: target.ProviderID,
			// Endpoint 由桥原样回传，这里放端点 ID，便于对回成员记录。
			Endpoint: target.Endpoint,
			BaseURL:  target.BaseURL,
			APIKey:   target.APIKey,
			Models:   selectedPiModels(ids, models),
		})
	}
	if len(providers) == 0 {
		return ""
	}
	value, err := magpie.PiEnvJSON(providers)
	if err != nil {
		log.Printf("pi magpie: 生成注入配置失败，本轮不注入: %v", err)
		return ""
	}
	return value
}

// selectedPiModels 按勾选顺序取出模型：网关没取到的 ID（或整次拉取失败）用默认元数据补上，
// 每个模型的 Name 换成界面显示用的 ShortName。
func selectedPiModels(ids []string, fetched []magpie.Model) []magpie.Model {
	return magpieinject.Selected(ids, fetched)
}

// piMagpieMember 是桥最近一次回传的实际路由：上游 provider、模型与记录时间。
type piMagpieMember struct {
	Provider string
	Model    string
	At       time.Time
}

// piMagpieMembers 记录各端点最近一次的实际路由，键是端点 ID（桥原样回传 Endpoint 字段）。
var piMagpieMembers = struct {
	sync.Mutex
	byEndpoint map[string]piMagpieMember
}{byEndpoint: map[string]piMagpieMember{}}

// recordPiMagpieMember 记下桥回传的实际路由账号，供额度窗口标出当前在用的卡片。
// 端点或 provider 为空（桥还没路由过）时忽略。
func recordPiMagpieMember(endpoint, provider, model string) {
	id := strings.TrimSpace(endpoint)
	provider = strings.TrimSpace(provider)
	if id == "" || provider == "" {
		return
	}
	piMagpieMembers.Lock()
	defer piMagpieMembers.Unlock()
	if piMagpieMembers.byEndpoint == nil {
		piMagpieMembers.byEndpoint = map[string]piMagpieMember{}
	}
	piMagpieMembers.byEndpoint[id] = piMagpieMember{
		Provider: provider,
		Model:    strings.TrimSpace(model),
		At:       time.Now(),
	}
}

// piMagpieBridge 缓存 Magpie 桥扩展的落盘路径：一个进程写一份临时目录。
var piMagpieBridge struct {
	once sync.Once
	path string
}

// installPiMagpieBridge 返回 Magpie 桥扩展路径；写出失败只记日志并返回空串，
// pi 照常启动（与没有桥时一样）。
func installPiMagpieBridge() string {
	piMagpieBridge.once.Do(func() {
		dir, err := os.MkdirTemp("", "mcode-pi-magpie-")
		if err != nil {
			log.Printf("failed to create pi magpie bridge directory: %v", err)
			return
		}
		path, err := pi.InstallMagpieBridge(dir)
		if err != nil {
			log.Printf("failed to install pi magpie bridge: %v", err)
			return
		}
		piMagpieBridge.path = path
	})
	return piMagpieBridge.path
}

// piMagpieRuntime 持有启动时的数据目录与凭据存储。端点配置不在这里快照：每次要用时
// 按最新配置重新生成，用户在设置里改了勾选模型后下一次 pi 启动即生效。
var piMagpieRuntime struct {
	mu      sync.Mutex
	dataDir string
	secrets config.SecretStore
}

// setPiMagpieRuntime 记录 pi Magpie 注入要用的数据目录与凭据存储（启动时调用一次）。
func setPiMagpieRuntime(dataDir string, secrets config.SecretStore) {
	piMagpieRuntime.mu.Lock()
	defer piMagpieRuntime.mu.Unlock()
	piMagpieRuntime.dataDir = dataDir
	piMagpieRuntime.secrets = secrets
}

// piMagpieRuntimeState 返回已记录的数据目录与凭据存储。
func piMagpieRuntimeState() (string, config.SecretStore) {
	piMagpieRuntime.mu.Lock()
	defer piMagpieRuntime.mu.Unlock()
	return piMagpieRuntime.dataDir, piMagpieRuntime.secrets
}

// currentPiMagpieEnv 按最新配置生成这一轮要注入 pi 的端点清单；读不到配置时返回空串。
func currentPiMagpieEnv() string {
	dataDir, secrets := piMagpieRuntimeState()
	if dataDir == "" {
		return ""
	}
	cfg, err := config.Load(dataDir)
	if err != nil {
		log.Printf("pi magpie: 读取配置失败，本轮不注入: %v", err)
		return ""
	}
	return buildPiMagpieEnv(context.Background(), cfg, secrets)
}

// piInjectedModel 判断模型 ID（provider/model 形式）是否属于某个 Magpie 端点的 pi provider。
func piInjectedModel(model string) bool {
	dataDir, secrets := piMagpieRuntimeState()
	if dataDir == "" {
		return false
	}
	cfg, err := config.Load(dataDir)
	if err != nil {
		return false
	}
	provider, _, ok := strings.Cut(model, "/")
	if !ok {
		return false
	}
	for _, target := range piMagpieAllTargets(cfg, secrets) {
		if target.providerID == provider {
			return true
		}
	}
	return false
}

// piGlobalApplied 判断 pi 的 Magpie 设置是否为 global，且端点已写进 pi 自己的 models.json。
func piGlobalApplied(cfg config.Config, dataDir string) bool {
	if dataDir == "" {
		return false
	}
	if config.MagpieAgentSetting(cfg, config.ProviderPi).Mode != config.MagpieModeGlobal {
		return false
	}
	applied, err := magpiecfg.Applied(dataDir, config.ProviderPi)
	return err == nil && applied
}

// piUnregisteredModel 判断模型是否属于 Magpie provider、但 pi 自己的 models.json 里没有该
// provider（未被 global 接管）：直连时 pi 会报 Unknown provider，需要降级为默认模型。
func piUnregisteredModel(model string) bool {
	if !piInjectedModel(model) {
		return false
	}
	dataDir, _ := piMagpieRuntimeState()
	cfg, err := config.Load(dataDir)
	if err != nil {
		return false
	}
	return !piGlobalApplied(cfg, dataDir)
}

// piStaleModel 判断线程里保存的 pi 模型是否已因直连失效：属于某个 Magpie 端点的 provider，
// 而本轮不会注入（设置为直连，或没有可注入的端点）。global 模式且该 agent 已接管全局
// 配置时模型由 pi 自己的 models.json 提供，不算失效。不拉取网关，可在每轮启动时调用。
func piStaleModel(model string) bool {
	if !piInjectedModel(model) {
		return false
	}
	dataDir, secrets := piMagpieRuntimeState()
	cfg, err := config.Load(dataDir)
	if err != nil {
		return false
	}
	mode := config.MagpieAgentSetting(cfg, config.ProviderPi).Mode
	if mode == config.MagpieModeOff {
		return true
	}
	if piGlobalApplied(cfg, dataDir) {
		return false
	}
	for _, target := range piMagpieTargets(cfg, secrets) {
		if target.injectable() {
			return false
		}
	}
	return true
}

// piMagpieInjection 是 remote 包取 pi 模型目录时的注入源：桥扩展路径与端点清单。
// 任一为空时 remote 退回 pi 自己的配置。
func piMagpieInjection() (string, string) {
	return installPiMagpieBridge(), currentPiMagpieEnv()
}

// piMagpieQuotaReading 是一次 pi 额度读取的结果：端点展示名、窗口列表与是否映射到端点。
type piMagpieQuotaReading struct {
	// Name 是端点名，界面用作 provider 展示名。
	Name string
	// Windows 是各卡片的额度窗口。
	Windows []mcodeui.ProviderQuotaWindow
	// Found 为 false 表示 providerID 映射不到有 key 的 Magpie 端点（线程用的是 pi 自带的 provider）。
	Found bool
}

// piMagpieTargetFor 按 pi provider id 找到有 key 的 Magpie 端点；找不到返回 false。
func (c *Client) piMagpieTargetFor(providerID string) (piMagpieTarget, bool) {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return piMagpieTarget{}, false
	}
	cfg, ok := c.loadConfig()
	if !ok {
		return piMagpieTarget{}, false
	}
	c.providersMu.Lock()
	secrets := c.secrets
	c.providersMu.Unlock()
	for _, candidate := range piMagpieTargets(cfg, secrets) {
		if candidate.providerID == providerID && candidate.apiKey != "" {
			return candidate, true
		}
	}
	return piMagpieTarget{}, false
}

// piMagpieQuotaReadingFor 读取某个 Magpie 端点的展示名与额度窗口。providerID 是
// pi:<pi provider id> 里的那一段：映射不到有 key 的 Magpie 端点时 Found 为 false。
func (c *Client) piMagpieQuotaReadingFor(ctx context.Context, providerID string) (piMagpieQuotaReading, error) {
	target, found := c.piMagpieTargetFor(providerID)
	if !found {
		return piMagpieQuotaReading{}, nil
	}

	// 会话落盘的 provider 会话 ID 由装配层注入的解析函数给出。
	sessionID := ""
	if threadID := mcodeui.QuotaThread(ctx); threadID != "" && c.agySessionForThread != nil {
		sessionID = c.agySessionForThread(ctx, threadID)
	}
	model := c.magpieQuotaModel(ctx, "pi", target.endpoint.ID, target.providerID)
	windows, err := c.readMagpieQuotaWindows(ctx, target.baseURL, target.apiKey, "pi", sessionID, model)
	if err != nil {
		return piMagpieQuotaReading{}, err
	}
	return piMagpieQuotaReading{Name: target.endpoint.Name, Windows: windows, Found: true}, nil
}

// piMagpieQuota 读取某个 Magpie 端点的额度窗口。providerID 是 pi:<pi provider id> 里的
// 那一段：映射不到有 key 的 Magpie 端点（线程用的是 pi 自带的 provider）时按未实现处理，
// 界面据此不显示额度。
func (c *Client) piMagpieQuota(ctx context.Context, providerID string) ([]mcodeui.ProviderQuotaWindow, error) {
	reading, err := c.piMagpieQuotaReadingFor(ctx, providerID)
	if err != nil {
		return nil, err
	}
	if !reading.Found {
		return nil, fmt.Errorf("ProviderQuota: %w", mcodeui.ErrNotImplemented)
	}
	return reading.Windows, nil
}

// PiMagpieEndpoints 列出当前配置里启用的 Magpie 端点、各端点可选的模型与已勾选模型。
// 拉取失败的端点把错误写进 Err、Models 留空；没配 key 的端点不请求网关。
func (c *Client) PiMagpieEndpoints(ctx context.Context) ([]mcodeui.PiMagpieEndpoint, error) {
	return c.piMagpieEndpoints(ctx, magpie.FetchModels)
}

// RefreshPiMagpieEndpoints 与 PiMagpieEndpoints 相同，但绕过模型缓存重新向各端点读取；读取失败的端点同样把错误写进 Err。
func (c *Client) RefreshPiMagpieEndpoints(ctx context.Context) ([]mcodeui.PiMagpieEndpoint, error) {
	return c.piMagpieEndpoints(ctx, magpie.RefreshModels)
}

func (c *Client) piMagpieEndpoints(ctx context.Context, fetch func(context.Context, string, string) ([]magpie.Model, error)) ([]mcodeui.PiMagpieEndpoint, error) {
	cfg, ok := c.loadConfig()
	if !ok {
		return nil, errors.New("dataDir 未初始化")
	}
	c.providersMu.Lock()
	secrets := c.secrets
	c.providersMu.Unlock()

	// 设置页要看到全部启用端点以便挑选模型，因此这里不按 pi 勾选过滤。
	targets := piMagpieAllTargets(cfg, secrets)
	res := make([]mcodeui.PiMagpieEndpoint, 0, len(targets))
	for _, target := range targets {
		ep := mcodeui.PiMagpieEndpoint{
			ID:         target.endpoint.ID,
			Name:       target.endpoint.Name,
			ProviderID: target.providerID,
			Selected:   append([]string(nil), target.models...),
		}
		if target.apiKey == "" {
			ep.Err = "未设置 API Key"
			res = append(res, ep)
			continue
		}
		fetchCtx, cancel := context.WithTimeout(ctx, piMagpieFetchTimeout)
		models, err := fetch(fetchCtx, target.baseURL, target.apiKey)
		cancel()
		if err != nil {
			ep.Err = err.Error()
			res = append(res, ep)
			continue
		}
		ep.Models = make([]mcodeui.PiMagpieModel, 0, len(models))
		for _, m := range models {
			ep.Models = append(ep.Models, mcodeui.PiMagpieModel{ID: m.ID, Name: magpie.ShortName(m)})
		}
		res = append(res, ep)
	}
	return res, nil
}

// SetPiMagpieModels 保存某个 Magpie 端点勾选注入 pi 的模型 ID（去空、去重由配置层在写盘时完成），
// 随后让 pi 模型目录下次重新拉取，使新增的端点模型立刻出现在模型列表里。global
// 已接管时与其它 agent 一样重写全局配置，勾选变化立刻生效。
func (c *Client) SetPiMagpieModels(endpointID string, modelIDs []string) error {
	c.providersMu.Lock()
	defer c.providersMu.Unlock()

	id := strings.TrimSpace(endpointID)
	if id == "" {
		return errors.New("端点 ID 不能为空")
	}
	dataDir := c.effectiveDataDir()
	if dataDir == "" {
		return errors.New("dataDir 未初始化")
	}

	if err := config.Update(dataDir, func(latest *config.Config) error {
		for _, ep := range latest.Providers.Custom {
			if ep.ID != id {
				continue
			}
			if !ep.Magpie {
				return fmt.Errorf("端点 %q 不是 Magpie 端点", id)
			}
			// 走统一的勾选写入：只写 PiModels 时，Normalize 会把旧勾选并回来，
			// 导致取消勾选的模型重新出现。
			config.SetMagpieAgentModels(latest, config.ProviderPi, id, modelIDs)
			return nil
		}
		return fmt.Errorf("自定义端点 %q 不存在", id)
	}); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	if err := c.agentMagpieReapplyGlobal(context.Background(), dataDir, config.ProviderPi, "勾选已保存", c.secrets); err != nil {
		return err
	}
	remote.InvalidatePiCatalog()
	return nil
}
