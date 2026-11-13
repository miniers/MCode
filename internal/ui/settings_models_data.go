package mcodeui

import (
	"context"
	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
	"time"
)

// renderPageModels 渲染模型设置页：已启用模型列表、搜索过滤及采样参数，并把过滤后的供应商目录交给模型供应商卡片渲染。
func modelsPageLibrary(c *ui.Context, st *SettingsState) *modelsLibraryView {
	ps := modelsPageStateOf(st)
	ps.applyOperations()
	if modelsPageStateHook != nil {
		modelsPageStateHook(ps)
	}

	st.mu.Lock()
	chat := st.chat
	cfg := st.cfg
	providerRows := make(map[string]ProviderSetting, len(st.providerRows.settings))
	for id, row := range st.providerRows.settings {
		providerRows[id] = row
	}
	// 供应商目录刚被作废（pi 注入的 Magpie 模型变了）：重新拉目录但不清空旧快照，
	// 避免模型库与“我的模型”里的选择器先闪成空列表再突然长回来。
	catalogStale := st.catalogStale
	catalogVersion := st.modelCatalogVersion
	if catalogStale {
		st.catalogStale = false
	}
	st.mu.Unlock()
	// 首帧先用配置里的固定模型把「你的模型」画出来，后台加载完成后再用真实结果覆盖，
	// 避免刚进页面时区块空白、列表随后突然把页面撑开。
	if !ps.seeded {
		ps.seeded = true
		if len(ps.pins) == 0 && len(cfg.Models.Pins) > 0 {
			ps.pins = append([]ModelPin(nil), cfg.Models.Pins...)
			for _, pin := range ps.pins {
				ps.preferences[pin.Key()] = cfg.Models.ModelPreferenceFor(pin)
			}
		}
	}
	// 目录作废只重载目录：保留「你的模型」和旧目录，等新目录到达再替换。
	// 首次加载还没结束时保持原来的完整加载流程，同样不再清空 ps.providers。
	if catalogStale && ps.loaded && ps.load == nil {
		ps.loaded = false
		ps.loadCatalogOnly = true
	}

	var client Client
	if chat != nil {
		client = chat.client
	}

	var providers []ProviderInfo
	if chat != nil {
		chat.mu.Lock()
		providers = append([]ProviderInfo(nil), chat.providers...)
		chat.mu.Unlock()
	}
	if ps.catalogVersion != catalogVersion {
		ps.catalogVersion = catalogVersion
		ps.providers = append([]ProviderInfo(nil), providers...)
	}
	// 优先使用页面向 client 刷新到的供应商列表：chat.providers 是启动时的快照，
	// 刚在设置页启用/停用的 provider 不会反映在里面。
	if len(ps.providers) > 0 {
		providers = append([]ProviderInfo(nil), ps.providers...)
	}
	if len(providers) == 0 {
		providers = []ProviderInfo{
			{ID: config.ProviderClaude, Name: "Claude"},
			{ID: config.ProviderCodex, Name: "Codex"},
			{ID: config.ProviderAgy, Name: "agy"},
		}
	}

	// 后台只写结果通道，所有页面状态均由 UI 线程更新。
	if !ps.loaded && ps.load == nil {
		ps.loadCatalogVersion = catalogVersion
		ps.loadPinsVersion = ps.pinsVersion
		ps.load = make(chan modelsPageLoadResult, 1)
		ps.loadSince = time.Now()
		results := ps.load
		// 在启动 goroutine 前拷贝标记：它是页面状态，只允许 UI 线程修改。
		catalogOnly := ps.loadCatalogOnly
		go func() {
			r := modelsPageLoadResult{limit: config.MaxModelListLimit, preferences: make(map[string]ModelPreference)}
			if client != nil {
				ctx, cancel := context.WithTimeout(context.Background(), modelsPageLoadTimeout)
				defer cancel()
				// 只刷新目录时跳过固定模型与偏好读取，避免旧结果覆盖「你的模型」。
				if !catalogOnly {
					r.pins, r.limit, r.err = client.ModelList(ctx)
					if r.err == nil {
						for _, pin := range r.pins {
							pref, err := client.ModelPreferences(ctx, pin)
							if err != nil {
								r.err = err
								break
							}
							r.preferences[pin.Key()] = pref
						}
					}
				}
				// ListProviders 只返回已启用的 provider，正好用作模型库列表。
				if provs, err := client.Providers(ctx); err == nil {
					r.providers = provs
				} else {
					r.catalogErr = err
				}
				// ProviderSettings 给出 CLI harness 与 API 预设的启用状态，用于隐藏已关闭供应商的模型。
				if settings, err := client.ProviderSettings(ctx); err == nil {
					r.providerSettings = settings
				} else if r.catalogErr == nil {
					r.catalogErr = err
				}
			}
			results <- r
			st.invalidate()
		}()
	}
	// 结果迟迟不到（调用了不理会 ctx 的路径）时放弃等待，转成可重试的错误。
	if ps.load != nil && time.Since(ps.loadSince) > modelsPageLoadTimeout {
		catalogOnly := ps.loadCatalogOnly
		ps.load = nil
		ps.loaded = true
		ps.loadCatalogOnly = false
		if catalogOnly {
			// 目录超时只影响模型库，不能把「你的模型」也藏起来。
			ps.catalogErr = "模型目录加载超时，请重新加载"
		} else {
			ps.loadErr = "加载模型超时，请重新加载"
		}
	}
	if ps.load != nil {
		select {
		case r := <-ps.load:
			catalogOnly := ps.loadCatalogOnly
			ps.load = nil
			ps.loaded = true
			ps.loadCatalogOnly = false
			if ps.loadCatalogVersion == catalogVersion && len(r.providers) > 0 {
				ps.providers = r.providers
			}
			ps.providerSettings = r.providerSettings
			if r.catalogErr != nil {
				ps.catalogErr = "模型目录加载失败：" + r.catalogErr.Error()
			} else {
				ps.catalogErr = ""
			}
			// 只刷新目录时不动固定模型、偏好和上限，也不回写配置。
			if !catalogOnly && ps.loadPinsVersion == ps.pinsVersion {
				if r.err != nil {
					ps.loadErr = "加载模型失败：" + r.err.Error()
				} else {
					ps.loadErr = ""
					ps.pins, ps.preferences = r.pins, r.preferences
					ps.limit = config.MaxModelListLimit
					if r.limit > 0 && r.limit < ps.limit {
						ps.limit = r.limit
					}
					ps.syncSettings(st)
				}
			}
		default:
		}
	}
	enabledProviders := providerEnabledMap(cfg, providerRows, ps.providerSettings)
	return &modelsLibraryView{ps: ps, client: client, providers: filterProviderModels(filterProvidersByEnabled(providers, enabledProviders), "", true)}
}

// modelsPageStateOf 为同一个设置页复用模型页状态。
func modelsPageStateOf(st *SettingsState) *modelsPageState {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.modelsPage == nil {
		state := newModelsPageState()
		st.modelsPage = &state
	}
	return st.modelsPage
}

// pinBusy 与完成队列共用锁，不能在此锁内调用 RPC、回调或 invalidate。
func (ps *modelsPageState) pinBusy(pin ModelPin) bool {
	ps.opMu.Lock()
	defer ps.opMu.Unlock()
	return ps.busy[pin.Key()]
}

// runOperation 的 work 只读捕获的快照，返回的回调在下一帧由 UI 线程执行。
// 队列不依赖窗口存活，也不会在窗口关闭后阻塞后台请求。
func (ps *modelsPageState) runOperation(st *SettingsState, key string, work func(context.Context) func()) bool {
	ps.opMu.Lock()
	if key != "" {
		if ps.busy[key] {
			ps.opMu.Unlock()
			return false
		}
		if ps.busy == nil {
			ps.busy = make(map[string]bool)
		}
		ps.busy[key] = true
	}
	ps.opMu.Unlock()
	st.invalidate()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), modelsPageLoadTimeout)
		defer cancel()
		apply := work(ctx)
		ps.opMu.Lock()
		ps.completed = append(ps.completed, func() {
			apply()
			ps.opMu.Lock()
			delete(ps.busy, key)
			ps.opMu.Unlock()
		})
		ps.opMu.Unlock()
		st.invalidate()
	}()
	return true
}

func (ps *modelsPageState) applyOperations() {
	ps.opMu.Lock()
	completed := ps.completed
	ps.completed = nil
	ps.opMu.Unlock()
	for _, apply := range completed {
		apply()
	}
}

// reorder 立即更新本地顺序，只有最新排序请求的完成结果可以更新页面。
func (ps *modelsPageState) reorder(st *SettingsState, client Client, from, to int) {
	ps.pins = reorderPins(ps.pins, from, to)
	ps.pinsVersion++
	ps.reorderSeq++
	seq := ps.reorderSeq
	previous := ps.reorderTail
	done := make(chan struct{})
	ps.reorderTail = done
	pins := append([]ModelPin(nil), ps.pins...)
	ps.opErr = ""
	ps.syncSettings(st)
	ps.runOperation(st, "", func(ctx context.Context) func() {
		defer close(done)
		var err error
		// 按拖拽顺序落盘，避免较慢的旧请求最后写入配置。
		if previous != nil {
			select {
			case <-previous:
			case <-ctx.Done():
				err = ctx.Err()
			}
		}
		if err == nil && client != nil {
			err = client.ReorderModelList(ctx, pins)
		}
		return func() {
			if seq != ps.reorderSeq {
				return
			}
			if err != nil {
				ps.opErr = "保存模型顺序失败：" + err.Error()
				ps.loaded = false
				ps.load = nil
				ps.loadCatalogOnly = false
			}
		}
	})
}
