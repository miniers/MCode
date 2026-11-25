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

type skillMemberLinkInfo struct {
	isSymlink bool
	realDir   string
}

type skillsPageState struct {
	busy              bool
	deleteSkill       *agentconf.Skill
	deleteOpts        agentconf.Options
	deleteBrokenLinks int
	// 浮层开合状态与筛选状态仅在 UI 线程读写（appPopover 关闭时会写回）。
	agentFilter     string
	agentFilterOpen bool
	topMenuOpen     bool
	rowMenuKey      string
	rowMenuOpen     bool
	rowMenuLinks    []skillMemberLinkInfo
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
	var brokenLinks int
	if page != nil {
		skill = page.deleteSkill
		opts = page.deleteOpts
		brokenLinks = page.deleteBrokenLinks
	}
	st.mu.Unlock()
	if skill == nil {
		modalDragReset("skill-delete")
		return
	}
	closeModal := func() {
		st.mu.Lock()
		page.deleteSkill = nil
		page.deleteBrokenLinks = 0
		st.mu.Unlock()
		st.invalidate()
	}
	body := fmt.Sprintf("将把 %s 移到废纸篓。", skill.Name)
	if brokenLinks > 0 {
		body += fmt.Sprintf("\n另有 %d 个 Agent 链接到此目录，删除后会失效。", brokenLinks)
	}
	RenderConfirmDialog(c, ConfirmDialog{
		Key: "skill-delete", Open: true, Tone: ConfirmDanger,
		Title: "删除技能", Body: body,
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

// skillEntry 表示按「小写名称 + 内容摘要」归并后的一条技能展示行。
type skillEntry struct {
	Name        string
	Description string
	Members     []agentconf.Skill
}

type skillAgentOption struct {
	agent string
	label string
}

// mergeSkills 按 strings.ToLower(Name)+"\x00"+Digest 将多 Agent 重复扫描的技能归并为一行，
// 保持首次出现顺序；同名不同内容分拆成多行时改用成员目录名区分（目录名也相同则照旧）。
func mergeSkills(list []agentconf.Skill) []skillEntry {
	var entries []skillEntry
	index := make(map[string]int, len(list))
	for _, sk := range list {
		key := strings.ToLower(sk.Name) + "\x00" + sk.Digest
		if idx, ok := index[key]; ok {
			entries[idx].Members = append(entries[idx].Members, sk)
			if entries[idx].Description == "" && sk.Description != "" {
				entries[idx].Description = sk.Description
			}
			continue
		}
		index[key] = len(entries)
		entries = append(entries, skillEntry{
			Name:        sk.Name,
			Description: sk.Description,
			Members:     []agentconf.Skill{sk},
		})
	}

	nameGroups := make(map[string][]int, len(entries))
	for i, e := range entries {
		k := strings.ToLower(e.Name)
		nameGroups[k] = append(nameGroups[k], i)
	}
	for _, idxs := range nameGroups {
		if len(idxs) <= 1 {
			continue
		}
		dirs := make([]string, len(idxs))
		unique := make(map[string]bool, len(idxs))
		allValid := true
		for j, idx := range idxs {
			p := entries[idx].Members[0].Path
			if p == "" {
				allValid = false
				break
			}
			d := filepath.Base(filepath.Dir(p))
			if d == "" || d == "." || d == string(filepath.Separator) {
				allValid = false
				break
			}
			dirs[j] = d
			unique[d] = true
		}
		if !allValid || len(unique) <= 1 {
			continue
		}
		for j, idx := range idxs {
			entries[idx].Name = dirs[j]
		}
	}
	return entries
}

func isSkillMemberEnabled(sk agentconf.Skill) bool {
	return sk.Enabled || !sk.Disabled
}

func skillMemberAgentLabel(sk agentconf.Skill) string {
	label := skillAgentLabel(sk.Agent)
	if sk.Plugin != "" {
		if label != "" {
			return label + " · 插件"
		}
		return "插件"
	}
	return label
}

// skillMatchesQuery 判断归并后的技能条目是否匹配搜索关键字：任一成员命中即可保留该行。
func skillMatchesQuery(entry skillEntry, query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return true
	}
	if strings.Contains(strings.ToLower(entry.Name), q) || strings.Contains(strings.ToLower(entry.Description), q) {
		return true
	}
	for _, m := range entry.Members {
		fields := []string{m.Name, m.Description, m.Scope, m.Path, m.Plugin, skillAgentLabel(m.Agent), skillMemberAgentLabel(m)}
		for _, f := range fields {
			if strings.Contains(strings.ToLower(f), q) {
				return true
			}
		}
	}
	return false
}

func entryMatchesAgent(entry skillEntry, agent string) bool {
	if agent == "" {
		return true
	}
	for _, m := range entry.Members {
		if strings.EqualFold(strings.TrimSpace(m.Agent), agent) {
			return true
		}
	}
	return false
}

func collectSkillAgentOptions(skills []agentconf.Skill) []skillAgentOption {
	opts := []skillAgentOption{{agent: "", label: "全部"}}
	seen := make(map[string]bool)
	for _, sk := range skills {
		a := strings.ToLower(strings.TrimSpace(sk.Agent))
		if a == "" || seen[a] {
			continue
		}
		label := skillAgentLabel(a)
		if label == "" {
			continue
		}
		seen[a] = true
		opts = append(opts, skillAgentOption{agent: a, label: label})
	}
	return opts
}

func collectGroupAgentMembers(entries []skillEntry, agent string) []agentconf.Skill {
	var members []agentconf.Skill
	for _, e := range entries {
		for _, m := range e.Members {
			if strings.EqualFold(strings.TrimSpace(m.Agent), agent) {
				members = append(members, m)
			}
		}
	}
	return members
}

func filterSkillEntries(entries []skillEntry, query, agent string) []skillEntry {
	var out []skillEntry
	for _, e := range entries {
		if skillMatchesQuery(e, query) && entryMatchesAgent(e, agent) {
			out = append(out, e)
		}
	}
	return out
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
	page := getSkillsPageLocked(st)
	loaded := st.agentConfLoaded
	loading := st.agentConfLoading
	errMsg := st.agentConfErr
	writeErr := st.agentConfWriteErr
	operating := page.busy
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

	var projectRaw, globalRaw []agentconf.Skill
	for _, sk := range skills {
		if strings.EqualFold(sk.Scope, "project") {
			projectRaw = append(projectRaw, sk)
		} else {
			globalRaw = append(globalRaw, sk)
		}
	}
	projectEntries := mergeSkills(projectRaw)
	globalEntries := mergeSkills(globalRaw)

	agentOptions := collectSkillAgentOptions(skills)
	if page.agentFilter != "" {
		found := false
		for _, opt := range agentOptions {
			if opt.agent == page.agentFilter {
				found = true
				break
			}
		}
		if !found {
			page.agentFilter = ""
		}
	}

	defer renderSkillDeleteDialog(c, st)
	renderSkillsActions(c, st, page, len(projectEntries)+len(globalEntries))

	// 按全字段过滤，空值显示全部；SearchField 带 Grow: true，放在横向 Row 里占满行宽。
	searchText := getSkillsSearchText(st)
	ui.Row(c.Key("skills-search-row")).
		FillWidth().
		AlignItems(ui.Center).
		Gap(Space2).
		Margin(Space0, Space0, Space4, Space0).
		Children(func() {
			renderSkillsSearchBox(c, st, searchText)
			renderSkillsAgentFilter(c, st, page, agentOptions)
		})
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

	filteredProject := filterSkillEntries(projectEntries, searchQuery, page.agentFilter)
	filteredGlobal := filterSkillEntries(globalEntries, searchQuery, page.agentFilter)

	if len(filteredProject) == 0 && len(filteredGlobal) == 0 && !loading {
		renderSkillsEmpty(c, searchQuery != "" || page.agentFilter != "")
		return
	}

	if len(filteredProject) > 0 {
		renderSkillGroupSection(c, st, page, "project",
			fmt.Sprintf("项目技能 (%d)", len(filteredProject)), filteredProject, page.agentFilter)
	}
	if len(filteredGlobal) > 0 {
		renderSkillGroupSection(c, st, page, "global",
			fmt.Sprintf("全局技能 (%d)", len(filteredGlobal)), filteredGlobal, page.agentFilter)
	}
}

// renderSkillsAgentFilter 在搜索框行右侧渲染 Agent 筛选下拉按钮。
func renderSkillsAgentFilter(c *ui.Context, st *SettingsState, page *skillsPageState, options []skillAgentOption) {
	currentLabel := "全部"
	for _, opt := range options {
		if opt.agent == page.agentFilter {
			currentLabel = opt.label
			break
		}
	}
	btn := Button(c.Key("btn-skills-agent-filter"), ButtonOpts{
		Text:    currentLabel + " ▾",
		Icon:    IconFilter(),
		Label:   "筛选 Agent",
		Tooltip: "按 Agent 筛选",
		Variant: ButtonSecondary,
		Size:    ControlMD,
		OnClick: func() {
			if togglePopoverOpen(&page.agentFilterOpen) {
				page.topMenuOpen = false
				page.rowMenuOpen = false
				page.rowMenuLinks = nil
			}
			st.invalidate()
		},
	})
	appPopover(c.Key("skills-agent-filter-popover"), btn, &page.agentFilterOpen, func() {
		st.invalidate()
	}, func() {
		popoverCard(c, ui.Column(c.Key("skills-agent-filter-menu"))).
			MinWidth(160).
			Padding(Space1).
			Gap(Space0).
			FocusGroup(ui.Vertical).
			Children(func() {
				for _, opt := range options {
					optVal := opt
					isCurrent := page.agentFilter == optVal.agent
					menuRow(c, menuRowProps{
						Key:         "skills-agent-opt-" + optVal.agent,
						Title:       optVal.label,
						Label:       "筛选：" + optVal.label,
						Checked:     isCurrent,
						Highlighted: isCurrent,
						OnClick: func() {
							page.agentFilter = optVal.agent
							page.agentFilterOpen = false
							st.invalidate()
						},
					})
				}
			})
	})
}

// renderSkillGroupTitle 渲染卡片内的来源标题。
func renderSkillGroupTitle(c *ui.Context, title string) {
	SectionHeading(c, nil, title).
		Margin(Space3, Space0, Space0, settingsRowPadX)
}

// renderSkillGroupSection 渲染分组标题（选中特定 Agent 时右侧带批量开关）和技能行。
func renderSkillGroupSection(c *ui.Context, st *SettingsState, page *skillsPageState, key, title string, entries []skillEntry, selectedAgent string) {
	ui.Column(c.Key("sec-skills-" + key)).Children(func() {
		ui.Row(c.Key("sec-skills-"+key+"-header")).
			FillWidth().
			AlignItems(ui.Center).
			Gap(Space3).
			Padding(Space3, settingsRowPadX, Space0, settingsRowPadX).
			Children(func() {
				SectionHeading(c, "sec-skills-"+key+"-title", title).SingleLine()

				if selectedAgent != "" {
					batchMembers := collectGroupAgentMembers(entries, selectedAgent)
					ui.Spacer(c)
					renderSkillBatchButton(c, st, "sec-skills-"+key+"-btn-enable-all", "全部开启", batchMembers, true)
					renderSkillBatchButton(c, st, "sec-skills-"+key+"-btn-disable-all", "全部禁用", batchMembers, false)
				}
			})

		renderSkillList(c, st, page, key, entries)
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

func entryInjected(projectDir, home string, members []agentconf.Skill) bool {
	for _, m := range members {
		if projctx.IsSkillInjectedPath(projectDir, home, m.Path) {
			return true
		}
	}
	return false
}

func skillDirSymlinkInfo(skillPath string) (isSymlink bool, realDir string) {
	if skillPath == "" {
		return false, ""
	}
	dir := filepath.Clean(filepath.Dir(skillPath))
	fi, err := os.Lstat(dir)
	if err != nil {
		return false, ""
	}
	isSymlink = fi.Mode()&os.ModeSymlink != 0
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		if isSymlink {
			return true, ""
		}
		return false, dir
	}
	return isSymlink, filepath.Clean(resolved)
}

func countSkillBrokenSymlinks(target agentconf.Skill, members []agentconf.Skill) int {
	if target.Path == "" {
		return 0
	}
	isLink, targetReal := skillDirSymlinkInfo(target.Path)
	if isLink || targetReal == "" {
		return 0
	}
	targetDir := filepath.Clean(filepath.Dir(target.Path))
	count := 0
	for _, other := range members {
		if sameSkill(other, target) || other.Path == "" {
			continue
		}
		if filepath.Clean(filepath.Dir(other.Path)) == targetDir {
			continue
		}
		_, otherReal := skillDirSymlinkInfo(other.Path)
		if otherReal != "" && (otherReal == targetReal || strings.HasPrefix(otherReal, targetReal+string(filepath.Separator))) {
			count++
		}
	}
	return count
}

// renderSkillAgentBadge 渲染成员的 Agent 行内徽标：启用为实色、停用为淡色，点击切换该成员启用状态。
func renderSkillAgentBadge(c *ui.Context, st *SettingsState, key, skillName string, sk agentconf.Skill) {
	agentText := skillMemberAgentLabel(sk)
	if agentText == "" {
		return
	}
	enabled := isSkillMemberEnabled(sk)
	stateText := "已停用"
	if enabled {
		stateText = "已启用"
	}
	a11yLabel := fmt.Sprintf("%s：%s「%s」", agentText, stateText, skillName)
	tooltip := agentText + " · " + stateText
	if sk.Plugin != "" {
		tooltip = fmt.Sprintf("%s（%s）· %s", agentText, sk.Plugin, stateText)
	}

	fg := ThemeTertiaryText(c)
	bg := ThemeOverlay(c, OverlayFaint)
	if enabled {
		fg = ThemePrimaryText(c)
		bg = ThemeOverlay(c, 0.09)
	}

	btn := ui.ButtonBase(c.Key(key)).
		Label(a11yLabel).
		Tooltip(tooltip).
		Shrink(0).
		Radius(RadiusXS).
		Padding(Space0, Space2).
		Background(bg).
		Transition(motionFeedback(c))
	if btn.Hovered() {
		btn.Background(ThemeHover(c))
	}
	if btn.Clicked() {
		applySkillEnabled(st, sk, !enabled)
	}
	btn.Children(func() {
		ui.Text(c, agentText).
			FontSize(uiFont(TypeCaption)).
			TextColor(fg).
			SingleLine().
			Transition(motionFeedback(c))
	})
}

// renderSkillList 渲染一组归并后的技能行（行间细分隔线）；项目与全局组共用。
func renderSkillList(c *ui.Context, st *SettingsState, page *skillsPageState, groupKey string, entries []skillEntry) {
	projectDir, home := st.injectedBase()
	for i, entry := range entries {
		if i > 0 {
			renderSettingsDivider(c, fmt.Sprintf("div-skill-%s-%d", groupKey, i))
		}
		entryVal := entry
		digest := ""
		if len(entryVal.Members) > 0 {
			digest = entryVal.Members[0].Digest
		}
		skillKey := fmt.Sprintf("skill-%s-%s-%s-%d", groupKey, entryVal.Name, digest, i)

		ui.Row(c.Key(skillKey)).
			FillWidth().
			MinHeight(settingsRowMinHeight).
			Padding(settingsRowPadY, settingsRowPadX).
			AlignItems(ui.Center).
			Gap(Space3).
			Children(func() {
				// 左侧只留名称与注入徽标，窄窗下自动折行且长名称截断。
				ui.Row(c.Key(skillKey + "-main")).
					Grow(1).
					Shrink(1).
					MinWidth(0).
					Wrap().
					AlignItems(ui.Center).
					Gap(Space2).
					Children(func() {
						ui.Text(c, entryVal.Name).
							FontSize(uiFont(TypeProse)).
							FontWeight(500).
							TextColor(ThemePrimaryText(c)).
							Shrink(1).
							MinWidth(0).
							SingleLine().
							Ellipsis("…").
							Tooltip(entryVal.Name)

						// 注入状态按成员路径判定：任一成员落在注入目录之下即为「mcode 注入」。
						if entryInjected(projectDir, home, entryVal.Members) {
							settingBadge(c, skillKey+"-badge-inject", "mcode 注入")
						} else {
							settingBadge(c, skillKey+"-badge-inject", "Agent 自行加载")
						}
					})

				settingRowRight(c, skillKey+"-actions", func() {
					ui.Row(c.Key(skillKey + "-agents")).
						MinWidth(0).
						Shrink(1).
						Wrap().
						Justify(ui.End).
						AlignItems(ui.Center).
						Gap(Space1).
						Children(func() {
							for j, m := range entryVal.Members {
								mVal := m
								badgeKey := fmt.Sprintf("%s-agent-%d-%s-%s-%s", skillKey, j, mVal.Agent, mVal.Scope, mVal.Plugin)
								renderSkillAgentBadge(c, st, badgeKey, entryVal.Name, mVal)
							}
						})
					ui.Row(c.Key(skillKey + "-controls")).
						Shrink(0).
						AlignItems(ui.Center).
						Gap(Space2).
						Children(func() {
							renderSkillRowMenu(c, st, page, skillKey, entryVal)
							renderSkillToggle(c, st, skillKey, entryVal.Name, entryVal.Members)
						})
				})
			})
	}
}

// renderSkillRowMenu 渲染技能行右侧 ··· 按钮及其浮层菜单（逐成员列出 Agent 来源、路径、真实目录、在访达中显示、移到废纸篓）。
func renderSkillRowMenu(c *ui.Context, st *SettingsState, page *skillsPageState, skillKey string, entryVal skillEntry) {
	moreBtn := IconButton(c, IconButtonProps{
		Key:     skillKey + "-btn-more",
		Icon:    IconMoreHorizontal(),
		Label:   "更多操作「" + entryVal.Name + "」",
		Tooltip: "更多操作",
		Variant: ButtonGhost,
		Size:    ControlSM,
		OnClick: func() {
			page.topMenuOpen = false
			page.agentFilterOpen = false
			if page.rowMenuKey == skillKey {
				togglePopoverOpen(&page.rowMenuOpen)
			} else {
				page.rowMenuKey = skillKey
				page.rowMenuOpen = true
			}
			if page.rowMenuOpen {
				links := make([]skillMemberLinkInfo, len(entryVal.Members))
				for j, m := range entryVal.Members {
					isLink, realDir := skillDirSymlinkInfo(m.Path)
					if isLink && realDir != "" {
						links[j] = skillMemberLinkInfo{isSymlink: true, realDir: realDir}
					}
				}
				page.rowMenuLinks = links
			} else {
				page.rowMenuLinks = nil
			}
			st.invalidate()
		},
	})
	if page.rowMenuKey != skillKey {
		return
	}
	appPopover(c.Key(skillKey+"-popover"), moreBtn, &page.rowMenuOpen, func() {
		page.rowMenuLinks = nil
		st.invalidate()
	}, func() {
		popoverCard(c, ui.Column(c.Key(skillKey+"-menu"))).
			MinWidth(240).
			MaxWidth(360).
			Padding(Space1).
			Gap(Space0).
			FocusGroup(ui.Vertical).
			Children(func() {
				hasMeta := false
				for _, m := range entryVal.Members {
					if skillMemberAgentLabel(m) != "" || m.Path != "" {
						hasMeta = true
						break
					}
				}
				if hasMeta {
					ui.Column(c.Key(skillKey + "-menu-meta")).
						FillWidth().
						Padding(Space1, Space3).
						Gap(Space2).
						Children(func() {
							for j, m := range entryVal.Members {
								agentText := skillMemberAgentLabel(m)
								hasLink := j < len(page.rowMenuLinks) && page.rowMenuLinks[j].isSymlink && page.rowMenuLinks[j].realDir != ""
								if agentText == "" && m.Path == "" && !hasLink {
									continue
								}
								ui.Column(c.Key(fmt.Sprintf("%s-menu-meta-%d", skillKey, j))).
									FillWidth().
									Gap(Space0).
									Children(func() {
										if agentText != "" || m.Plugin != "" {
											ui.Row(c.Key(fmt.Sprintf("%s-menu-agent-%d", skillKey, j))).
												AlignItems(ui.Center).
												Gap(Space1).
												Children(func() {
													if agentText != "" {
														settingBadge(c, fmt.Sprintf("%s-menu-badge-%d", skillKey, j), agentText)
													}
													if m.Plugin != "" {
														ui.Text(c, m.Plugin).
															FontSize(uiFont(TypeCaption)).
															TextColor(ThemeSecondaryText(c))
													}
												})
										}
										if m.Path != "" {
											ui.Text(c, m.Path).
												FontSize(uiFont(TypeCaption)).
												TextColor(ThemeTertiaryText(c)).
												FillWidth()
										}
										if hasLink {
											ui.Text(c, "真实目录："+page.rowMenuLinks[j].realDir).
												FontSize(uiFont(TypeCaption)).
												TextColor(ThemeTertiaryText(c)).
												FillWidth()
										}
									})
							}
						})
					ui.Box(c.Key(skillKey + "-menu-div")).
						FillWidth().
						Height(Hairline).
						Background(ThemeHairline(c)).
						Margin(Space1, Space0)
				}

				for j, m := range entryVal.Members {
					mVal := m
					agentText := skillMemberAgentLabel(mVal)
					revealTitle := revealInFileManagerLabel()
					if agentText != "" {
						revealTitle += " · " + agentText
					}
					menuRow(c, menuRowProps{
						Key:   fmt.Sprintf("%s-menu-reveal-%d", skillKey, j),
						Icon:  IconFolderOpen(),
						Title: revealTitle,
						OnClick: func() {
							page.rowMenuOpen = false
							page.rowMenuLinks = nil
							st.invalidate()
							openItemInFinder(mVal.Path)
						},
					})

					if mVal.Plugin == "" {
						deleteTitle := "移到废纸篓"
						if agentText != "" {
							deleteTitle += " · " + agentText
						}
						deleteLabel := "删除技能「" + entryVal.Name + "」"
						if len(entryVal.Members) > 1 && agentText != "" {
							deleteLabel += " · " + agentText
						}
						menuRow(c, menuRowProps{
							Key:      fmt.Sprintf("%s-menu-delete-%d", skillKey, j),
							Icon:     IconTrash2(),
							Title:    deleteTitle,
							Label:    deleteLabel,
							Danger:   true,
							Disabled: skillOperationBusy(st),
							OnClick: func() {
								page.rowMenuOpen = false
								page.rowMenuLinks = nil
								opts := st.agentConfWriteOptions()
								broken := countSkillBrokenSymlinks(mVal, entryVal.Members)
								st.mu.Lock()
								p := getSkillsPageLocked(st)
								p.deleteSkill = &mVal
								p.deleteOpts = opts
								p.deleteBrokenLinks = broken
								st.mu.Unlock()
								st.invalidate()
							},
						})
					}
				}
			})
	})
}

func renderSkillsActions(c *ui.Context, st *SettingsState, page *skillsPageState, count int) {
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

			moreBtn := IconButton(c, IconButtonProps{
				Key:     "btn-skills-more",
				Icon:    IconMoreHorizontal(),
				Label:   "技能更多操作",
				Tooltip: "更多操作",
				Variant: ButtonGhost,
				Size:    ControlSM,
				OnClick: func() {
					if togglePopoverOpen(&page.topMenuOpen) {
						page.rowMenuOpen = false
						page.rowMenuLinks = nil
						page.agentFilterOpen = false
					}
					st.invalidate()
				},
			})
			appPopover(c.Key("skills-top-more-popover"), moreBtn, &page.topMenuOpen, func() {
				st.invalidate()
			}, func() {
				popoverCard(c, ui.Column(c.Key("skills-top-more-menu"))).
					MinWidth(160).
					Padding(Space1).
					Gap(Space0).
					FocusGroup(ui.Vertical).
					Children(func() {
						menuRow(c, menuRowProps{
							Key:   "btn-skills-open-folder",
							Icon:  IconFolderOpen(),
							Title: "打开目录",
							OnClick: func() {
								page.topMenuOpen = false
								st.invalidate()
								openDirectoryPath(getGlobalSkillsDir())
							},
						})
						menuRow(c, menuRowProps{
							Key:      "btn-skills-rescan",
							Icon:     IconRefreshCw(),
							Title:    "重新扫描",
							Disabled: skillOperationBusy(st),
							OnClick: func() {
								page.topMenuOpen = false
								st.invalidate()
								st.loadAgentConf(st.currentProjectDir())
							},
						})
					})
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
