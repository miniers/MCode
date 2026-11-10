package mcodeui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

const (
	// nowledgeSpaceLoadTimeout 是「项目记忆空间」区块单次请求的上限：超时后显示错误并可重试。
	nowledgeSpaceLoadTimeout = 15 * time.Second
	// memorySpaceAutoChoice 是下拉框里「自动匹配」这一项的文案，也是它的取值。
	memorySpaceAutoChoice = "自动匹配"
)

func settingsMemoryBackend(st *SettingsState) string {
	st.mu.Lock()
	chat := st.chat
	st.mu.Unlock()
	if chat != nil && chat.client != nil {
		return chat.client.MemoryBackend()
	}
	return config.MemoryBackendNmem
}

// memoryBackendNmem 判断当前是否使用 Nowledge Mem 后端。本地文档后端下，nmem 专属的行仍然
// 渲染但禁用：逐项搜索照样能命中，点进去不会落到一个不存在的行上。
func memoryBackendNmem(st *SettingsState) bool {
	return settingsMemoryBackend(st) != config.MemoryBackendLocal
}

// 记忆设置和项目空间供记忆页组装。
var nowledgeSections = []settingsSection{
	{
		ID:    "nowledge.memory",
		Title: "记忆",
		Rows: []setting{
			{
				ID:      "row-memory-backend",
				Title:   "记忆后端",
				Aliases: []string{"后端", "本地", "Nowledge", "自动", "重启"},
				Control: choice(
					val(func(c *config.Config) *string { return &c.Memory.Backend }, config.MemoryBackendAuto),
					[]choiceOption{
						{Value: config.MemoryBackendAuto, Label: "自动"},
						{Value: config.MemoryBackendNmem, Label: "Nowledge Mem"},
						{Value: config.MemoryBackendLocal, Label: "本地文档"},
					},
				),
				// 选的是配置值，生效值要等重启；两者不同时把当前生效的后端写出来。
				Status: func(st *SettingsState, locked bool) string {
					active := "Nowledge Mem"
					if !memoryBackendNmem(st) {
						active = "本地文档"
					}
					return "当前生效：" + active
				},
			},
			{
				ID:      "row-nowledge-inject-working-memory",
				Title:   "新会话注入 Working Memory",
				Aliases: []string{"Working Memory", "记忆摘要", "首轮", "提示词"},
				Enabled: memoryBackendNmem,
				Control: toggle(ptr(func(c *config.Config) **bool { return &c.Memory.InjectWorkingMemory }, true)),
			},
			{
				ID:      "row-nowledge-bee-recall",
				Title:   "派发子代理时附带相关记忆",
				Aliases: []string{"bee", "子代理", "召回", "记忆检索"},
				Control: toggle(ptr(func(c *config.Config) **bool { return &c.Memory.BeeRecall }, true)),
			},
			{
				ID:      "row-nowledge-sync-threads",
				Title:   "同步会话到 Nowledge",
				Aliases: []string{"同步", "归档", "空闲", "自动同步", "记忆"},
				Enabled: memoryBackendNmem,
				Control: toggle(ptr(func(c *config.Config) **bool { return &c.Memory.SyncThreads }, true)),
			},
		},
	},
	{
		// 列表是运行时长出来的数据，只能用 custom 整块画。标题只写在节上，行里不再重复；
		// 没有行标题的 custom 行由控件铺满整行。
		ID:      "nowledge.spaces",
		Title:   "项目记忆空间",
		Visible: memoryBackendNmem,
		Rows: []setting{{
			ID:      "row-nowledge-project-spaces",
			Aliases: []string{"记忆空间", "项目"},
			Control: custom(renderNowledgeSpaceList),
		}},
	},
}

var pageNowledge = settingsPage{
	ID:       "nowledge",
	Title:    "记忆",
	Icon:     IconBrain(),
	Keywords: []string{"nowledge", "nmem", "memory", "working memory", "threads", "sync", "记忆", "上下文"},
}

func init() {
	pageNowledge.Sections = append(append([]settingsSection{}, nowledgeSections...), advancedMemory)
	settingsTreePageDefs = append(settingsTreePageDefs, pageNowledge)
}

func renderPageNowledge(c *ui.Context, st *SettingsState) {
	renderSettingsTreePage(c, st, pageNowledge)
}

// advancedMemory 是记忆页的高级设置。
var advancedMemory = settingsSection{
	ID:    "advanced.memory",
	Title: "高级",
	Rows: []setting{
		{
			ID:      "row-nowledge-inject-memory-tools-note",
			Title:   "向新会话说明记忆工具用法",
			Aliases: []string{"记忆工具", "工具用法", "提示词", "首轮"},
			Control: toggle(ptr(func(c *config.Config) **bool { return &c.Memory.InjectMemoryToolsNote }, true)),
		},
		{
			ID:      "row-nowledge-working-memory-budget",
			Title:   "工作记忆预算",
			Aliases: []string{"字符上限", "上限", "记忆预算", "截断", "8000"},
			Control: number(
				val(func(c *config.Config) *int { return &c.Memory.WorkingMemoryMaxCharacters }, config.DefaultWorkingMemoryMaxCharacters),
				"字符",
				config.MinWorkingMemoryMaxCharacters,
				config.MaxWorkingMemoryMaxCharacters,
			),
		},
		{
			ID:      "row-nowledge-sync-helper-threads",
			Title:   "同步编排子代理会话",
			Aliases: []string{"子代理", "bee", "辅助线程", "同步"},
			Enabled: memoryBackendNmem,
			Control: toggle(ptr(func(c *config.Config) **bool { return &c.Memory.SyncHelperThreads }, false)),
		},
		{
			ID:      "row-nowledge-sync-automation-threads",
			Title:   "同步自动化运行会话",
			Aliases: []string{"自动化", "运行会话", "同步"},
			Enabled: memoryBackendNmem,
			Control: toggle(ptr(func(c *config.Config) **bool { return &c.Memory.SyncAutomationThreads }, false)),
		},
	},
}

// nowledgeSpaceLoadResult 是一次后台加载的结果：空间列表与各项目的解析结果。
type nowledgeSpaceLoadResult struct {
	spaces      []MemorySpace
	resolutions map[string]MemorySpaceResolution
	resolveErrs map[string]string
	err         error
}

// nowledgeSpacePageState 是「项目记忆空间」区块的页面状态，只在 UI 线程读写；
// 后台 goroutine 只往 load 通道写结果。
type nowledgeSpacePageState struct {
	spaces      []MemorySpace
	resolutions map[string]MemorySpaceResolution
	resolveErrs map[string]string
	loaded      bool
	load        chan nowledgeSpaceLoadResult
	loadSince   time.Time
	loadErr     string
}

func newNowledgeSpacePageState() nowledgeSpacePageState {
	return nowledgeSpacePageState{
		resolutions: map[string]MemorySpaceResolution{},
		resolveErrs: map[string]string{},
	}
}

// memorySpaceSubtitle 返回一行项目的记忆空间解析结果文案。
func memorySpaceSubtitle(res MemorySpaceResolution) string {
	switch {
	case res.FromOverride:
		return "手动指定：" + res.Space
	case res.Hit:
		return "自动匹配：" + res.Space
	default:
		return "未匹配，使用默认空间"
	}
}

// applySpaceOverride 把下拉框的选择写回 space_overrides：选「自动匹配」删除该项目的键，
// 选具体空间名时写入该名字（map 为 nil 时先建）。返回写好的 map（可能新建）。
func applySpaceOverride(overrides map[string]string, path, choice string) map[string]string {
	if strings.TrimSpace(choice) == "" || choice == memorySpaceAutoChoice {
		// nil map 上 delete 是安全的，保持「没配置过」的原样。
		delete(overrides, path)
		return overrides
	}
	if overrides == nil {
		overrides = map[string]string{}
	}
	overrides[path] = choice
	return overrides
}

// getNowledgeSpacePageState 返回「项目记忆空间」区块的页面状态：首次访问时在 st.mu 锁内创建，
// SettingsState.Close 收尾时清空。后台协程只写自己拿到的 load 通道，不回头读 st.nowledgePage，
// 所以关面板时字段被清空也不会 panic。调用方不要持有 st.mu。
func getNowledgeSpacePageState(st *SettingsState) *nowledgeSpacePageState {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.nowledgePage == nil {
		ps := newNowledgeSpacePageState()
		st.nowledgePage = &ps
	}
	return st.nowledgePage
}

// renderNowledgeSpaceList 画「项目记忆空间」行的内容：每个项目一行，显示解析到的空间并可改。
// 首次渲染触发一次加载，加载结果通过通道回到 UI 线程；失败时只显示一行灰字，不弹窗。
func renderNowledgeSpaceList(c *ui.Context, st *SettingsState) {
	ps := getNowledgeSpacePageState(st)

	st.mu.Lock()
	chat := st.chat
	overrides := make(map[string]string, len(st.cfg.Memory.SpaceOverrides))
	for path, space := range st.cfg.Memory.SpaceOverrides {
		overrides[path] = space
	}
	st.mu.Unlock()

	var projects []Project
	var client Client
	if chat != nil {
		chat.mu.Lock()
		projects = append([]Project(nil), chat.projects...)
		client = chat.client
		chat.mu.Unlock()
	}

	if !ps.loaded && ps.load == nil {
		startNowledgeSpaceLoad(st, ps, client, projects, overrides)
	}
	// 请求迟迟不回时放弃等待，转成可重试的错误，避免区块一直空着等加载。
	if ps.load != nil && time.Since(ps.loadSince) > nowledgeSpaceLoadTimeout {
		ps.load = nil
		ps.loaded = true
		ps.loadErr = "读取记忆空间超时，请重试"
	}
	if ps.load != nil {
		select {
		case r := <-ps.load:
			ps.load = nil
			ps.loaded = true
			if r.err != nil {
				ps.loadErr = "读取记忆空间失败：" + r.err.Error()
			} else {
				ps.loadErr = ""
				ps.spaces = r.spaces
				ps.resolutions = r.resolutions
				ps.resolveErrs = r.resolveErrs
			}
		default:
		}
	}

	ui.Column(c.Key("nowledge-space-list")).Children(func() {
		if ps.loadErr != "" {
			ui.Row(c.Key("row-nowledge-space-error")).
				Padding(settingsRowPadY, settingsRowPadX).
				Gap(Space3).
				AlignItems(ui.Center).
				Children(func() {
					ui.Text(c, ps.loadErr).FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c)).Grow(1)
					ui.ButtonBase(c.Key("btn-nowledge-space-retry")).Label("重试").Padding(Space2, settingsCompactPadX).Radius(RadiusSM).OnClick(func() {
						ps.loaded = false
						ps.loadErr = ""
						ps.load = nil
						st.invalidate()
					})
				})
			return
		}
		if !ps.loaded {
			// 加载中用转圈代替说明句；结果回来前不列项目行，免得先闪出错的解析结果。
			LoadingIndicator(c, nil, "", IconSM, ThemeSecondaryText(c)).Margin(settingsRowPadY, settingsRowPadX)
			return
		}
		if len(projects) == 0 {
			ui.Text(c, "还没有项目").FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c)).Margin(settingsRowPadY, settingsRowPadX)
			return
		}
		for i, p := range projects {
			renderNowledgeSpaceRow(c, st, ps, i, p, overrides)
			if i != len(projects)-1 {
				renderSettingsDivider(c, fmt.Sprintf("div-rgfwhs-tmoxl-%d", i))
			}
		}
	})
}

// setProjectSpaceOverride 把某个项目的记忆空间选择写回配置并落盘；choice 为「自动匹配」时
// 删除该项，选具体空间名时写入该名字。
func (st *SettingsState) setProjectSpaceOverride(path, choice string) {
	// 统一走 updateAndSave：在 mu → cfgMu 内改配置、锁外落盘，不由调用方拼锁。
	st.updateAndSave(func(cfg *config.Config) {
		cfg.Memory.SpaceOverrides = applySpaceOverride(cfg.Memory.SpaceOverrides, path, choice)
	})
}

// renderNowledgeSpaceRow 渲染一个项目一行：左侧项目名与解析结果，右侧空间下拉框。
// 行 key 用 row-memory-space-<序号>，该行直接自绘、不走设置行渲染器。
func renderNowledgeSpaceRow(c *ui.Context, st *SettingsState, ps *nowledgeSpacePageState, index int, p Project, overrides map[string]string) {
	key := fmt.Sprintf("row-memory-space-%d", index)
	override := strings.TrimSpace(overrides[p.Path])

	res, ok := ps.resolutions[p.Path]
	if !ok {
		res = MemorySpaceResolution{Space: override, FromOverride: override != ""}
	}
	subtitle := memorySpaceSubtitle(res)
	if msg := ps.resolveErrs[p.Path]; msg != "" {
		subtitle = "解析失败：" + msg
	}

	current := memorySpaceAutoChoice
	if override != "" {
		current = override
	}
	options := make([]string, 0, len(ps.spaces)+1)
	options = append(options, memorySpaceAutoChoice)
	for _, s := range ps.spaces {
		options = append(options, s.Name)
	}
	// 手动指定的空间已被删除时仍要能显示当前值，否则下拉框会悄悄跳回「自动匹配」。
	if override != "" && !containsString(options, override) {
		options = append(options, override)
	}

	ui.Row(c.Key(key)).
		Padding(settingsRowPadY, settingsRowPadX).
		// Label 让 ui.Tester.Find 能按文字定位到这一行（Tester 按 Label 匹配，不按 key）。
		Label(key).
		Gap(Space5).
		AlignItems(ui.Center).
		Children(func() {
			ui.Column(c.Key(key + "-left")).
				Grow(1).
				MinWidth(settingsFieldMediumWidth).
				Gap(settingsBadgePadY).
				Children(func() {
					ui.Text(c, p.Name).
						FontSize(TypeProse).
						FontWeight(500).
						TextColor(ThemePrimaryText(c)).
						SingleLine()
					ui.Text(c, subtitle).
						FontSize(TypeFootnote).
						TextColor(ThemeSecondaryText(c))
				})
			ui.Row(c.Key(key + "-right")).
				AlignItems(ui.Center).
				Children(func() {
					initial := current
					sel := ui.Select(c.Key(fmt.Sprintf("sel-rgfwhs-tmoxl-%d", index)), &current, options).
						Label(p.Name).
						Width(settingsFieldMediumWidth)
					if sel.Changed() && current != initial {
						st.setProjectSpaceOverride(p.Path, current)
						// 解析结果要按新的覆盖重算：清掉加载标记，下一帧重新拉空间与解析结果。
						ps.loaded = false
						ps.load = nil
						st.invalidate()
					}
				})
		})
}

// startNowledgeSpaceLoad 在后台拉空间列表并逐个解析项目；只写结果通道，页面状态由 UI 线程更新。
func startNowledgeSpaceLoad(st *SettingsState, ps *nowledgeSpacePageState, client Client, projects []Project, overrides map[string]string) {
	ps.load = make(chan nowledgeSpaceLoadResult, 1)
	ps.loadSince = time.Now()
	results := ps.load
	go func() {
		r := nowledgeSpaceLoadResult{
			resolutions: make(map[string]MemorySpaceResolution, len(projects)),
			resolveErrs: make(map[string]string),
		}
		if client != nil {
			ctx, cancel := context.WithTimeout(context.Background(), nowledgeSpaceLoadTimeout)
			spaces, err := client.MemorySpaces(ctx)
			cancel()
			if err != nil {
				r.err = err
			} else {
				r.spaces = spaces
				for _, p := range projects {
					callCtx, cancelCall := context.WithTimeout(context.Background(), nowledgeSpaceLoadTimeout)
					res, err := client.ResolveMemorySpace(callCtx, p.Path, overrides[p.Path])
					cancelCall()
					if err != nil {
						r.resolveErrs[p.Path] = err.Error()
						continue
					}
					r.resolutions[p.Path] = res
				}
			}
		}
		results <- r
		st.invalidate()
	}()
}

// containsString 判断列表里是否已有该值。
func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
