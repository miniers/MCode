package mcodeui

import (
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"
)

// modelPickerItem 是通用模型选择浮层里的一行。Section 为分段名，可空；
// 未指定 ProviderID 时只有出现多个不同分段才渲染段头。Detail 只参与搜索匹配，不单独展示。
type modelPickerItem struct {
	Section string
	// ProviderID 非空时按供应商分组，组头显示供应商图标和 Section 文字，组内不再显示模型系列小标题。
	ProviderID string
	Key        string
	Name       string
	ID         string
	Detail     string
	Checked    bool
	Disabled   bool
}

// modelPickerGroup 是同一模型系列下的一组选择行，组内保持传入顺序。
type modelPickerGroup struct {
	title string
	items []modelPickerItem
}

// renderModelPickerAddButton 渲染模型区的「添加模型」按钮，返回按钮元素作为浮层锚点。
// label 是按钮的无障碍名称，可见文字仍是「添加模型」。
func renderModelPickerAddButton(c *ui.Context, st *SettingsState, ps *modelsPageState, popoverKey, label string) ui.Element {
	btn := ui.ButtonBase(c.Key(popoverKey+"-button")).
		Label(label).
		Radius(RadiusXS).
		Padding(Space0, Space2)
	btn.OnClick(func() {
		if ps.openPopoverKey == popoverKey {
			ps.openPopoverKey = ""
		} else {
			ps.openPopoverKey = popoverKey
		}
		st.invalidate()
	})
	btn.Children(func() {
		ui.Row(c.Key(popoverKey + "-button-inner")).
			Gap(Space1).
			AlignItems(ui.Center).
			Children(func() {
				ui.Icon(c, IconPlus()).
					Size(14, 14).
					TextColor(ThemeAccent(c))
				ui.Text(c, "添加模型").
					FontSize(TypeFootnote).
					TextColor(ThemeAccent(c))
			})
	})
	return btn
}

// modelPickerCustom 给「添加模型」浮层注入一条「添加自定义模型」入口；零值表示不显示。
type modelPickerCustom struct {
	Candidate func(query string) (id string, ok bool) // 返回 ok=false 时不渲染
	Add       func(id string)                         // 点击后的写入动作
}

// renderModelPickerPopover 渲染通用模型选择浮层：搜索、分段、按系列分组、勾选切换。
func renderModelPickerPopover(c *ui.Context, st *SettingsState, ps *modelsPageState, popoverKey string, anchor ui.Element, items []modelPickerItem, emptyHint string, onToggle func(item modelPickerItem, checked bool), custom modelPickerCustom) {
	renderModelPickerPopoverWithNotice(c, st, ps, popoverKey, anchor, items, emptyHint, "", onToggle, custom)
}

// renderModelPickerPopoverWithNotice 与 renderModelPickerPopover 相同，额外在列表上方
// 显示一条提示（例如模型数量已达上限）。
func renderModelPickerPopoverWithNotice(c *ui.Context, st *SettingsState, ps *modelsPageState, popoverKey string, anchor ui.Element, items []modelPickerItem, emptyHint, notice string, onToggle func(item modelPickerItem, checked bool), custom modelPickerCustom) {
	open := ps.openPopoverKey == popoverKey
	key := "popover-" + popoverKey
	host := c.Services()
	native := flyoutHostWindow(host) != nil
	background := ThemeSurfacePopover(c)
	closePicker := func() {
		ps.openPopoverKey = ""
		closeAppFlyoutPopover(host, key)
		st.invalidate()
	}
	appFlyoutPopover(c, key, anchor, &open, func() {
		if ps.openPopoverKey == popoverKey {
			ps.openPopoverKey = ""
		}
	}, appFlyoutProps{Width: 420, Background: &background}, func(c *ui.Context) {
		query := ps.providerSearchInput(popoverKey)
		card := ui.Column(c.Key("model-picker-" + popoverKey)).Width(420).Padding(Space3).Gap(Space3)
		if !native {
			card = popoverCard(c, card)
		}
		card.
			Children(func() {
				ui.Row(c.Key("model-picker-search-" + popoverKey)).
					FillWidth().
					Children(func() {
						renderProviderModelSearchBox(c, st, popoverKey, query).FillWidth()
					})
				if custom.Candidate != nil {
					if id, ok := custom.Candidate(strings.TrimSpace(*query)); ok {
						label := "添加自定义模型「" + id + "」"
						btn := ui.ButtonBase(c.Key("model-picker-custom-"+popoverKey)).
							Label(label).
							FillWidth().
							AlignItems(ui.Start).
							Padding(Space3, Space3).
							Radius(RadiusXS)
						if btn.Hovered() {
							btn.Background(ThemeHover(c))
						}
						btn.OnClick(func() {
							if custom.Add != nil {
								custom.Add(id)
							}
							closePicker()
						})
						btn.Children(func() {
							ui.Text(c, label).
								FontSize(TypeFootnote).
								TextColor(ThemeAccent(c))
						})
					}
				}
				if notice != "" {
					ui.Text(c, notice).
						FontSize(TypeFootnote).
						TextColor(ThemeSecondaryText(c)).
						Padding(0, Space1)
				}
				ui.Column(c.Key(popoverKey + "-list")).FillWidth().Children(func() {
					renderModelPickerSections(c, st, ps, popoverKey, items, strings.TrimSpace(*query), emptyHint, func(item modelPickerItem, checked bool) {
						if onToggle != nil {
							onToggle(item, checked)
						}
						closePicker()
					})
				})
			})
	})
}

// renderModelPickerSections 按供应商或分段和模型系列渲染选择行；搜索词为空时展示全部。
func renderModelPickerSections(c *ui.Context, st *SettingsState, ps *modelsPageState, popoverKey string, items []modelPickerItem, query, emptyHint string, onToggle func(item modelPickerItem, checked bool)) {
	byProvider := false
	for _, item := range items {
		if item.ProviderID != "" {
			byProvider = true
			break
		}
	}
	var sections []string
	bySection := make(map[string][]modelPickerItem, 4)
	sectionNames := make(map[string]string, 4)
	for _, item := range items {
		section := item.Section
		if byProvider {
			section = item.ProviderID
			if _, ok := sectionNames[section]; !ok {
				name := item.Section
				if name == "" {
					name = section
				}
				sectionNames[section] = name
				sections = append(sections, section)
			}
		}
		if query != "" && !modelMatchesQuery(item.Name, item.ID, item.Detail, query) {
			continue
		}
		if !byProvider {
			if _, ok := bySection[section]; !ok {
				sections = append(sections, section)
			}
		}
		bySection[section] = append(bySection[section], item)
	}

	multi := len(sections) > 1
	rendered := false
	for _, section := range sections {
		list := bySection[section]
		if len(list) == 0 {
			continue
		}
		if byProvider {
			if rendered {
				renderSettingsDivider(c, "model-picker-provider-div-"+section)
			}
			ui.Row(c.Key("model-picker-provider-"+section)).
				AlignItems(ui.Center).
				Gap(Space2).
				Padding(Space2, Space1, Space1, Space1).
				Children(func() {
					ui.Box(c).Width(IconSM).Height(IconSM).Shrink(0).Children(func() {
						renderProviderRowIcon(c, section, IconSM)
					})
					ui.Text(c, sectionNames[section]).
						FontSize(TypeCaption).
						FontWeight(600).
						TextColor(ThemeSecondaryText(c)).
						SingleLine()
				})
		}
		rendered = true
		if !byProvider && multi && section != "" {
			ui.Text(c, section).
				FontSize(TypeCaption).
				FontWeight(600).
				TextColor(ThemeSecondaryText(c)).
				Padding(Space1, Space1, Space0, Space1)
		}
		// 段内按模型系列归组，组的排序沿用 Magpie 面板的规则。
		var groups []*modelPickerGroup
		byTitle := make(map[string]*modelPickerGroup, 8)
		for _, item := range list {
			title := magpieModelFamily(item.ID)
			group := byTitle[title]
			if group == nil {
				group = &modelPickerGroup{title: title}
				byTitle[title] = group
				groups = append(groups, group)
			}
			group.items = append(group.items, item)
		}
		sort.SliceStable(groups, func(i, j int) bool {
			ri, rj := magpieModelFamilyRank(groups[i].title), magpieModelFamilyRank(groups[j].title)
			if ri != rj {
				return ri < rj
			}
			return groups[i].title < groups[j].title
		})
		for _, group := range groups {
			if !byProvider {
				ui.Row(c).
					AlignItems(ui.Center).
					Gap(Space2).
					Padding(Space1, Space1, Space0, Space1).
					Children(func() {
						if key := modelFamilyIconKey(group.title); key != "" {
							ui.Box(c).Width(IconSM).Height(IconSM).Shrink(0).Children(func() {
								renderProviderRowIcon(c, key, IconSM)
							})
						}
						ui.Text(c, group.title).
							FontSize(TypeFootnote).
							FontWeight(600).
							TextColor(ThemeSecondaryText(c))
					})
			}
			for _, item := range group.items {
				renderModelPickerRow(c, st, ps, popoverKey, item, onToggle)
			}
		}
	}
	if rendered {
		return
	}
	text := "无匹配模型"
	if query == "" {
		text = emptyHint
	}
	ui.Text(c, text).
		FontSize(TypeFootnote).
		TextColor(ThemeSecondaryText(c)).
		Padding(Space2, Space1)
}

func modelFamilyIconKey(title string) string {
	switch title {
	case "Claude":
		return "claude"
	case "GPT":
		return "codex"
	case "GLM":
		return "zai"
	case "DeepSeek":
		return "deepseek"
	case "Grok":
		return "grok"
	default:
		return ""
	}
}

// renderModelPickerRow 渲染单个可勾选的模型行。
func renderModelPickerRow(c *ui.Context, st *SettingsState, ps *modelsPageState, popoverKey string, item modelPickerItem, onToggle func(item modelPickerItem, checked bool)) {
	name := strings.TrimSpace(item.Name)
	if name == "" {
		name = item.ID
	}
	checked := item.Checked
	ui.Row(c.Key("row-"+item.Key)).
		FillWidth().
		AlignItems(ui.Center).
		Gap(Space2).
		Padding(Space0, Space1).
		Children(func() {
			cb := ui.Checkbox(c.Key(item.Key), &checked, name)
			cb.Disabled(item.Disabled)
			if cb.Changed() {
				if onToggle != nil {
					onToggle(item, checked)
				}
				st.invalidate()
			}
			if strings.TrimSpace(item.ID) != "" && item.ID != name {
				ui.Text(c, item.ID).
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c)).
					SingleLine().
					Ellipsis("…").
					Shrink(1).
					MinWidth(0)
			}
		})
}
