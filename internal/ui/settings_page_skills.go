package mcodeui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mcode/internal/agentconf"
	"mcode/internal/projctx"
)

type skillsPageState struct {
	busy        bool
	deleteSkill *agentconf.Skill
	deleteOpts  agentconf.Options
}

// 调用方持有 st.mu；状态跟随设置面板关闭释放。
func getSkillsPageLocked(st *SettingsState) *skillsPageState {
	if st.skillsPage == nil {
		st.skillsPage = &skillsPageState{}
	}
	return st.skillsPage
}

var pickSkillFolder = func() (string, bool, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Title: "选择技能文件夹", Directory: true,
	})
	if err != nil || len(paths) == 0 {
		return "", false, err
	}
	return paths[0], true, nil
}

func skillOperationBusy(st *SettingsState) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.agentConfLoading || (st.skillsPage != nil && st.skillsPage.busy)
}

func importSkillFolder(st *SettingsState) {
	opts := st.agentConfWriteOptions()
	pick := pickSkillFolder
	runSkillOperation(st, func() (bool, error) {
		path, ok, err := pick()
		if err != nil || !ok {
			return false, err
		}
		_, err = agentconf.ImportSkill(opts.Home, path)
		return err == nil, err
	})
}

func runSkillOperation(st *SettingsState, operation func() (bool, error)) {
	st.mu.Lock()
	page := getSkillsPageLocked(st)
	if page.busy || st.agentConfLoading {
		st.mu.Unlock()
		return
	}
	page.busy = true
	st.agentConfWriteErr = ""
	st.mu.Unlock()
	st.invalidate()
	go func() {
		changed, err := operation()
		st.mu.Lock()
		page.busy = false
		// 关闭后到达的回调不能重新挂载页面或启动扫描。
		if st.skillsPage != page {
			st.mu.Unlock()
			return
		}
		if err != nil {
			st.agentConfWriteErr = err.Error()
		}
		st.mu.Unlock()
		if changed {
			st.loadAgentConf(st.currentProjectDir())
			if err != nil {
				st.setAgentConfWriteErr(err.Error())
			}
		}
		st.invalidate()
	}()
}

func renderSkillDeleteDialog(c *ui.Context, st *SettingsState) {
	st.mu.Lock()
	page := st.skillsPage
	var skill *agentconf.Skill
	var opts agentconf.Options
	if page != nil {
		skill = page.deleteSkill
		opts = page.deleteOpts
	}
	st.mu.Unlock()
	if skill == nil {
		modalDragReset("skill-delete")
		return
	}
	closeModal := func() {
		st.mu.Lock()
		page.deleteSkill = nil
		st.mu.Unlock()
		st.invalidate()
	}
	RenderConfirmDialog(c, ConfirmDialog{
		Key: "skill-delete", Open: true, Tone: ConfirmDanger,
		Title: "删除技能", Body: fmt.Sprintf("将把 %s 移到废纸篓。", skill.Name),
		ConfirmLabel: "移到废纸篓", ConfirmA11y: "确认删除技能",
		CancelLabel: "取消", CancelA11y: "取消删除技能",
		OnConfirm: func() {
			closeModal()
			runSkillOperation(st, func() (bool, error) {
				return true, agentconf.DeleteSkill(opts, *skill)
			})
		},
		OnCancel: closeModal,
	})
}

// openItemInFinder 在系统访达中定位并高亮显示指定文件或目录。
func openItemInFinder(path string) {
	if path == "" {
		return
	}
	mygo.Shell.ShowItemInFolder(path)
}

// openDirectoryPath 在系统文件管理器中打开指定目录。
func openDirectoryPath(path string) {
	if path == "" {
		return
	}
	_ = mygo.Shell.OpenPath(path)
}

// getGlobalSkillsDir 返回全局统一 Skills 目录路径（若不存在则自动创建）。
func getGlobalSkillsDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	dir := filepath.Join(home, ".agents", "skills")
	_ = os.MkdirAll(dir, 0755)
	return dir
}

// skillAgentLabel 返回 Agent 徽标的可读名称；未知 Agent 回退为首字母大写。
func skillAgentLabel(agent string) string {
	switch strings.ToLower(agent) {
	case "":
		return ""
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	case "cursor":
		return "Cursor"
	case "factory":
		return "Factory"
	case "gemini":
		return "Gemini"
	case "antigravity":
		return "Antigravity"
	case "pi":
		return "Pi"
	default:
		// 按 rune 取首字再大写：按字节切 agent[:1] 会把中文或 emoji 的 UTF-8 序列切成破字节。
		return capitalizeFirstRune(agent)
	}
}

// capitalizeFirstRune 把首个 rune 转成大写再拼上剩余部分，空串直接返回空串。
// 不能按字节切 agent[:1]：中文首字等多字节字符会被切碎成乱码。
func capitalizeFirstRune(s string) string {
	if s == "" {
		return ""
	}
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError && size == 1 {
		// 非法 UTF-8：没有可转的 rune，原样返回，避免把坏字节换成替换字符。
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}

// skillMatchesQuery 判断技能是否匹配搜索关键字：名称、描述、作用域、Agent 标签与路径均可命中。
func skillMatchesQuery(sk agentconf.Skill, query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return true
	}
	fields := []string{sk.Name, sk.Description, sk.Scope, sk.Path, skillAgentLabel(sk.Agent)}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), q) {
			return true
		}
	}
	return false
}

// skillTitleMatchesQuery 判断技能标题是否匹配搜索关键字：只按标题做不区分大小写的包含匹配，
// 空关键字视为全部命中（技能页顶部搜索框的过滤规则）。
func skillTitleMatchesQuery(sk agentconf.Skill, query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return true
	}
	return strings.Contains(strings.ToLower(sk.Name), q)
}

// getSkillsSearchText 返回技能页搜索框的文本指针：首次访问时在 st.mu 锁内创建，
// SettingsState.Close 收尾时清空，页面状态不再常驻进程。调用方不要持有 st.mu。
func getSkillsSearchText(st *SettingsState) *string {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.skillsSearch == nil {
		st.skillsSearch = new(string)
	}
	return st.skillsSearch
}

var sectionSkills = settingsSection{
	ID:    "skills.list",
	Title: "技能",
	Rows: []setting{{
		ID:      "block-skills",
		Aliases: []string{"skill", "启用", "停用", "项目", "全局", "外部", "插件", "SKILL.md", "领域规则", "工作流", "提示词", "斜杠命令", "Claude", "Codex", "Cursor", "Antigravity", "自动发现", "搜索", "目录"},
		Control: custom(renderSkillsContent),
	}},
}

func renderSkillsContent(c *ui.Context, st *SettingsState) {
	st.ensureAgentConfCurrent()
	st.mu.Lock()
	loaded := st.agentConfLoaded
	loading := st.agentConfLoading
	errMsg := st.agentConfErr
	writeErr := st.agentConfWriteErr
	operating := st.skillsPage != nil && st.skillsPage.busy
	skills := make([]agentconf.Skill, len(st.skills))
	copy(skills, st.skills)
	st.mu.Unlock()

	if !loaded && !loading {
		st.loadAgentConf(st.currentProjectDir())
		// 起扫后重读一次，免得本帧先闪出「未发现技能」的空态。
		st.mu.Lock()
		loading = st.agentConfLoading
		st.mu.Unlock()
	}

	defer renderSkillDeleteDialog(c, st)
	renderSkillsActions(c, st, len(skills))

	// 按技能标题过滤，空值显示全部。
	searchText := getSkillsSearchText(st)
	renderSkillsSearchBox(c, st, searchText)
	searchQuery := strings.TrimSpace(*searchText)

	if loading || operating {
		// 扫描中用转圈代替说明句。
		LoadingIndicator(c, "skills-loading", "", IconSM).Margin(Space3, settingsRowPadX, Space1, settingsRowPadX)
	}
	if errMsg != "" {
		ui.Text(c, errMsg).
			FontSize(uiFont(TypeBody)).
			TextColor(ThemeDanger(c)).
			Margin(Space3, settingsRowPadX, Space1, settingsRowPadX)
	}
	if writeErr != "" {
		ui.Text(c, writeErr).
			FontSize(uiFont(TypeBody)).
			TextColor(ThemeDanger(c)).
			Margin(Space3, settingsRowPadX, Space1, settingsRowPadX)
	}

	// 3. 搜索过滤
	var filtered []agentconf.Skill
	for _, sk := range skills {
		if skillTitleMatchesQuery(sk, searchQuery) {
			filtered = append(filtered, sk)
		}
	}

	// 4. 空态展示
	if len(filtered) == 0 && !loading {
		renderSkillsEmpty(c, searchQuery != "")
		return
	}

	// 5. 分组展示：项目与外部按作用域分组；全局技能按供应商（Agent）分组，
	// 每组标题右侧提供「全部开启」「全部禁用」批量开关；过滤后为空的组不渲染。
	var projectSkills, externalSkills, globalSkills []agentconf.Skill
	for _, sk := range filtered {
		switch strings.ToLower(sk.Scope) {
		case "project":
			projectSkills = append(projectSkills, sk)
		case "global":
			globalSkills = append(globalSkills, sk)
		default:
			externalSkills = append(externalSkills, sk)
		}
	}

	if len(projectSkills) > 0 {
		renderSkillGroupTitle(c, fmt.Sprintf("项目技能 (%d)", len(projectSkills)))
		renderSkillList(c, st, "project", projectSkills)
	}

	for _, grp := range groupGlobalSkillsByProvider(globalSkills) {
		grpVal := grp
		renderSkillProviderSection(c, st, "sec-skills-"+grpVal.id,
			fmt.Sprintf("%s 全局技能 (%d)", grpVal.label, len(grpVal.list)), grpVal.list)
	}

	if len(externalSkills) > 0 {
		renderSkillGroupTitle(c, fmt.Sprintf("外部与插件技能 (%d)", len(externalSkills)))
		renderSkillList(c, st, "external", externalSkills)
	}
}

// skillGroup 是技能页的一个展示分组：id 用于组件 key，label 是供应商可读名称。
type skillGroup struct {
	id    string
	label string
	list  []agentconf.Skill
}

// groupGlobalSkillsByProvider 按供应商（Skill.Agent）对全局技能分组，保持传入顺序；
// 空 Agent 归入「其他」，未知供应商沿用 skillAgentLabel 的首字母大写回退。
func groupGlobalSkillsByProvider(skills []agentconf.Skill) []skillGroup {
	var groups []skillGroup
	index := make(map[string]int, len(skills))
	for _, sk := range skills {
		agent := strings.ToLower(strings.TrimSpace(sk.Agent))
		idx, ok := index[agent]
		if !ok {
			label := skillAgentLabel(agent)
			if label == "" {
				label = "其他"
			}
			idx = len(groups)
			index[agent] = idx
			groups = append(groups, skillGroup{id: "global-" + agent, label: label})
		}
		groups[idx].list = append(groups[idx].list, sk)
	}
	return groups
}

// renderSkillGroupTitle 渲染卡片内的来源标题。
func renderSkillGroupTitle(c *ui.Context, title string) {
	SectionHeading(c, nil, title).
		Margin(Space3, 0, 0, settingsRowPadX)
}

// renderSkillProviderSection 渲染供应商标题、批量按钮和技能行。
func renderSkillProviderSection(c *ui.Context, st *SettingsState, key, title string, list []agentconf.Skill) {
	ui.Column(c.Key(key)).Children(func() {
		ui.Row(c.Key(key+"-header")).
			AlignItems(ui.Center).
			Gap(Space3).
			Padding(Space3, settingsRowPadX, 0, settingsRowPadX).
			Children(func() {
				SectionHeading(c, key+"-title", title).SingleLine()

				ui.Spacer(c)

				renderSkillBatchButton(c, st, key+"-btn-enable-all", "全部开启", list, true)
				renderSkillBatchButton(c, st, key+"-btn-disable-all", "全部禁用", list, false)
			})

		renderSkillList(c, st, key, list)
	})
}

// renderSkillBatchButton 渲染分组标题右侧的批量开关按钮，一次切换该组所有技能的启用状态。
func renderSkillBatchButton(c *ui.Context, st *SettingsState, key, label string, list []agentconf.Skill, enabled bool) {
	btn := ui.ButtonBase(c.Key(key)).
		Label(label).
		Tooltip(label).
		Radius(RadiusSM).
		Padding(settingsBadgePadY, Space3).
		Background(ThemeOverlay(c, 0.06))
	{
		scale := motionPressScale(c, btn, "button-press-scale", btn.Pressed())
		btn.Scale(scale, scale)
	}
	if btn.Hovered() {
		btn.Background(ThemeHover(c))
	}
	if btn.Clicked() {
		setSkillsEnabled(st, list, enabled)
	}
	btn.Children(func() {
		ui.Text(c, label).
			FontSize(uiFont(TypeFootnote)).
			TextColor(ThemeSecondaryText(c))
	})
}

// setSkillsEnabled 批量设置一组技能的启用状态：逐个复用单项开关的 SetSkillEnabled 持久化，
// 只把写盘成功的条目同步进内存列表；有失败时保留原状态并把原因汇总到页面错误区（S-113）。
func setSkillsEnabled(st *SettingsState, list []agentconf.Skill, enabled bool) {
	opts := st.agentConfWriteOptions()
	changed := make([]agentconf.Skill, 0, len(list))
	failed := 0
	var firstErr error
	for _, sk := range list {
		if err := agentconf.SetSkillEnabled(opts, sk, enabled); err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		changed = append(changed, sk)
	}

	if len(changed) > 0 {
		st.mu.Lock()
		for _, sk := range changed {
			for idx := range st.skills {
				if sameSkill(st.skills[idx], sk) {
					st.skills[idx].Enabled = enabled
					st.skills[idx].Disabled = !enabled
				}
			}
		}
		st.mu.Unlock()
	}

	if failed > 0 {
		st.setAgentConfWriteErr(fmt.Sprintf("%d 个技能切换失败：%v", failed, firstErr))
	} else {
		st.setAgentConfWriteErr("")
	}
}

// applySkillEnabled 单个技能启停：写盘成功后才改内存列表，失败保留原状态并把原因写进页面错误区；
// 开关的显示值每帧由 st.skills 重建，不写内存即等于回滚（S-113）。
func applySkillEnabled(st *SettingsState, sk agentconf.Skill, enabled bool) {
	if err := agentconf.SetSkillEnabled(st.agentConfWriteOptions(), sk, enabled); err != nil {
		st.setAgentConfWriteErr("切换技能「" + sk.Name + "」失败：" + err.Error())
		return
	}

	st.mu.Lock()
	for idx := range st.skills {
		if sameSkill(st.skills[idx], sk) {
			st.skills[idx].Enabled = enabled
			st.skills[idx].Disabled = !enabled
		}
	}
	st.mu.Unlock()

	st.setAgentConfWriteErr("")
}

// sameSkill 判断内存里的两条记录是否指向同一条技能：停用键按 Agent + 作用域 + 插件 + 名字生成，
// 乐观更新与失败回滚必须按同一粒度匹配，不能只用名字，否则同名跨 Agent（或跨插件）的技能会被一起改掉（S-114）。
func sameSkill(a, b agentconf.Skill) bool {
	return strings.EqualFold(strings.TrimSpace(a.Agent), strings.TrimSpace(b.Agent)) &&
		strings.EqualFold(strings.TrimSpace(a.Scope), strings.TrimSpace(b.Scope)) &&
		strings.EqualFold(strings.TrimSpace(a.Plugin), strings.TrimSpace(b.Plugin)) &&
		a.ID() == b.ID()
}

// injectedBase 返回本页列表对应的项目目录与家目录，用于判断某条技能是否落在 mcode 注入提示词的
// 根目录之下。两个值取自与扫描同源的 agentConfWriteOptions，避免切项目后徽章与列表错位。
func (s *SettingsState) injectedBase() (projectDir, home string) {
	opts := s.agentConfWriteOptions()
	return opts.ProjectDir, opts.Home
}

// renderSkillList 渲染一组技能行（行间细分隔线）；项目、全局供应商与外部组共用。
func renderSkillList(c *ui.Context, st *SettingsState, groupKey string, list []agentconf.Skill) {
	projectDir, home := st.injectedBase()
	for i, sk := range list {
		if i > 0 {
			renderSettingsDivider(c, fmt.Sprintf("div-skill-%s-%d", groupKey, i))
		}
		skVal := sk
		skillKey := fmt.Sprintf("skill-%s-%s-%s-%s-%d", skVal.Agent, skVal.Scope, skVal.Plugin, skVal.Name, i)

		ui.Column(c.Key(skillKey)).
			Padding(settingsRowPadY, settingsRowPadX).
			Gap(Space1).
			Children(func() {
				// 顶部行：名称、作用域标签、Agent 标签、在访达中显示按钮、启用开关
				ui.Row(c.Key(skillKey + "-header")).
					AlignItems(ui.Center).
					Gap(Space3).
					Children(func() {
						ui.Text(c, skVal.Name).
							FontSize(uiFont(TypeProse)).
							FontWeight(500).
							TextColor(ThemePrimaryText(c)).
							SingleLine()

						scopeLabel := "全局"
						if strings.EqualFold(skVal.Scope, "project") {
							scopeLabel = "项目"
						} else if !strings.EqualFold(skVal.Scope, "global") {
							scopeLabel = "外部"
						}
						settingBadge(c, skillKey+"-badge-scope", scopeLabel)

						// 注入状态按路径判定：Agent 标签与是否注入并不等价（~/.agents/skills 被标成 codex，
						// 却是注入的主力目录）。
						if projctx.IsSkillInjectedPath(projectDir, home, skVal.Path) {
							settingBadge(c, skillKey+"-badge-inject", "mcode 注入")
						} else {
							settingBadge(c, skillKey+"-badge-inject", "Agent 自行加载")
						}

						if skVal.Agent != "" {
							settingBadge(c, skillKey+"-badge-agent", skillAgentLabel(skVal.Agent))
						}

						if skVal.Plugin != "" {
							settingBadge(c, skillKey+"-badge-plugin", "插件 · "+skVal.Plugin)
						}

						// 弹性占位推至右侧
						ui.Spacer(c)

						settingRowRight(c, skillKey+"-actions", func() {
							// 在访达中显示按钮
							revealBtn := ui.ButtonBase(c.Key(skillKey+"-btn-reveal")).
								Radius(RadiusSM).
								Padding(settingsBadgePadY, Space3)
							{
								scale := motionPressScale(c, revealBtn, "button-press-scale", revealBtn.Pressed())
								revealBtn.Scale(scale, scale)
							}
							if revealBtn.Hovered() {
								revealBtn.Background(ThemeHover(c))
							}
							if revealBtn.Clicked() {
								openItemInFinder(skVal.Path)
							}
							revealBtn.Children(func() {
								ui.Text(c, revealInFileManagerLabel()).
									FontSize(uiFont(TypeFootnote)).
									TextColor(ThemeSecondaryText(c))
							})

							if skVal.Plugin == "" {
								deleteBtn := ui.ButtonBase(c.Key(skillKey+"-btn-delete")).
									Label("删除技能「"+skVal.Name+"」").
									Tooltip("移到废纸篓").
									Disabled(skillOperationBusy(st)).
									Radius(RadiusSM).
									Padding(settingsBadgePadY, Space3)
								{
									scale := motionPressScale(c, deleteBtn, "button-press-scale", deleteBtn.Pressed())
									deleteBtn.Scale(scale, scale)
								}
								if deleteBtn.Hovered() {
									deleteBtn.Background(ThemeHover(c))
								}
								if deleteBtn.Clicked() {
									opts := st.agentConfWriteOptions()
									st.mu.Lock()
									page := getSkillsPageLocked(st)
									page.deleteSkill = &skVal
									page.deleteOpts = opts
									st.mu.Unlock()
									st.invalidate()
								}
								deleteBtn.Children(func() {
									ui.Icon(c, IconTrash2()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
								})
							}

							renderSkillToggle(c, st, skillKey, skVal)
						})
					})
			})
	}
}

func renderSkillsActions(c *ui.Context, st *SettingsState, count int) {
	ui.Row(c.Key("skills-lead-actions")).
		AlignItems(ui.Center).
		Gap(settingsCompactPadX).
		Padding(settingsRowPadY, settingsRowPadX).
		Children(func() {
			ui.Text(c, fmt.Sprintf("%d 个技能", count)).FontSize(uiFont(TypeBody)).TextColor(ThemePrimaryText(c))
			ui.Spacer(c)

			importBtn := ui.ButtonBase(c.Key("btn-skills-import")).
				Label("导入").
				Disabled(skillOperationBusy(st)).
				Radius(RadiusSM).
				Padding(settingsPillPadX, Space4).
				Background(ThemeOverlay(c, 0.06))
			{
				scale := motionPressScale(c, importBtn, "button-press-scale", importBtn.Pressed())
				importBtn.Scale(scale, scale)
			}
			if importBtn.Hovered() {
				importBtn.Background(ThemeHover(c))
			}
			if importBtn.Clicked() {
				importSkillFolder(st)
			}
			importBtn.Children(func() {
				ui.Text(c, "导入").FontSize(uiFont(TypeBody)).TextColor(ThemePrimaryText(c))
			})

			openBtn := ui.ButtonBase(c.Key("btn-skills-open-folder")).
				Radius(RadiusSM).
				Padding(settingsPillPadX, Space4).
				Background(ThemeOverlay(c, 0.06))
			{
				scale := motionPressScale(c, openBtn, "button-press-scale", openBtn.Pressed())
				openBtn.Scale(scale, scale)
			}
			if openBtn.Hovered() {
				openBtn.Background(ThemeHover(c))
			}
			if openBtn.Clicked() {
				openDirectoryPath(getGlobalSkillsDir())
			}
			openBtn.Children(func() {
				ui.Text(c, "打开目录").
					FontSize(uiFont(TypeBody)).
					TextColor(ThemePrimaryText(c))
			})

			rescanBtn := ui.ButtonBase(c.Key("btn-skills-rescan")).
				Disabled(skillOperationBusy(st)).
				Radius(RadiusSM).
				Padding(settingsPillPadX, Space4).
				Background(ThemeOverlay(c, 0.06))
			{
				scale := motionPressScale(c, rescanBtn, "button-press-scale", rescanBtn.Pressed())
				rescanBtn.Scale(scale, scale)
			}
			if rescanBtn.Hovered() {
				rescanBtn.Background(ThemeHover(c))
			}
			if rescanBtn.Clicked() {
				st.loadAgentConf(st.currentProjectDir())
			}
			rescanBtn.Children(func() {
				ui.Text(c, "重新扫描").
					FontSize(uiFont(TypeBody)).
					TextColor(ThemePrimaryText(c))
			})
		})
}

func renderSkillsEmpty(c *ui.Context, isFiltered bool) {
	title := "未发现技能"
	if isFiltered {
		title = "无匹配的技能"
	}
	EmptyState(c, EmptyStateProps{Key: "skills-empty", Title: title, Compact: true}).Margin(settingsRowPadY, settingsRowPadX)
}
