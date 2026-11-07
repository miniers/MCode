package mcodeui

import (
	"reflect"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// settingsResetTarget 描述一个支持「恢复本页默认」的页面：确认框里的标题与分节。
// 重置范围完全由页面树派生：只有实现了 settingResettable 的行才算可重置，
// 账号、密钥、收藏、网关路由等没有 reset 的控件因此天然被排除。
type settingsResetTarget struct {
	title    string   // 页面标题，用于确认框标题
	sections []string // 重置覆盖的分节，用于确认框正文
}

// settingsResetKeepFields 是 resetPreferenceFields 一律保留的字段，键为「类型名.字段名」。
// 只放兜底重置会破坏语义或丢用户数据的字段：值里写原因，改动前先读它。
var settingsResetKeepFields = map[string]string{
	"StewardConfig.EnabledChoice":              "迁移标记，不是设置项：清掉后后续迁移会把用户亲手关掉的总管重新打开",
	"StewardConfig.HomeDir":                    "目录路径：用户指定的总管主目录，重置会让已有记忆与状态文件失去归属",
	"SourceControlConfig.CloneIgnoredPatterns": "目录名单：用户自己维护的写时复制条目名模式，属列表类用户数据",
}

// resetPreferenceFields 把 dst 结构体的偏好字段改回 def 里的默认值。
// 逐字段处理：标量与指针（含 *bool、*int 这类三态字段）用默认值覆盖；切片与 map 保留用户原值
// （快捷回复、模型组合、档位、打开方式清单等都存在这类字段里）；嵌套结构体递归处理。
// dst 与 def 必须是同一类型的结构体，其他情况什么都不做。
func resetPreferenceFields(dst, def reflect.Value) {
	if dst.Kind() != reflect.Struct || def.Kind() != reflect.Struct || dst.Type() != def.Type() {
		return
	}

	t := dst.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" {
			// 未导出字段既读不出也写不进，跳过。
			continue
		}
		d, df := dst.Field(i), def.Field(i)
		if !d.CanSet() {
			continue
		}
		if _, keep := settingsResetKeepFields[t.Name()+"."+field.Name]; keep {
			continue
		}
		if !isMergeLeaf(field.Type) {
			// 嵌套结构体（值类型，且不是 time.Time）逐层递归，例如里面的总管配置。
			resetPreferenceFields(d, df)
			continue
		}
		switch field.Type.Kind() {
		case reflect.Slice, reflect.Map:
			// 列表类数据属于用户内容，重置只改偏好，不动它们。
			continue
		}
		// 用克隆值而不是直接赋值：def 里的指针字段不能和默认值共享同一份内存。
		d.Set(cloneValue(df))
	}
}

// settingsResetTargetFor 返回某页的重置目标：只从页面树汇总，
// 逐节收集实现了 settingResettable 且未锁定的行，确认框只列出真正会变的节。
// 没有任何可重置行的页面（如模型、Magpie）返回 false，不显示重置入口。
func settingsResetTargetFor(page string, st *SettingsState) (settingsResetTarget, bool) {
	p, ok := settingsTreePageOf(page)
	if !ok {
		return settingsResetTarget{}, false
	}
	sections := make([]string, 0, len(p.Sections))
	hasResettable := false
	for _, section := range p.Sections {
		if section.Visible != nil && st != nil && !section.Visible(st) {
			continue
		}
		sectionHasResettable := false
		for _, s := range section.Rows {
			if _, ok := s.Control.(settingResettable); !ok {
				continue
			}
			if settingRowLocked(st, s) {
				continue
			}
			sectionHasResettable = true
			break
		}
		if !sectionHasResettable {
			continue
		}
		hasResettable = true
		// 无标题分节（如编排页的「启用编排」）不进重置范围文案，避免出现空的顿号项。
		if section.Title != "" {
			sections = append(sections, section.Title)
		}
	}
	if !hasResettable {
		return settingsResetTarget{}, false
	}
	return settingsResetTarget{title: p.Title, sections: sections}, true
}

// renderSettingsResetArea 在支持重置的页面底部（高级区之后）渲染「恢复本页默认」按钮，
// 点击后弹出二次确认，确认时才真正重置本页对应的配置段。
func renderSettingsResetArea(c *ui.Context, st *SettingsState, page string) {
	if st == nil {
		return
	}
	if _, ok := settingsResetTargetFor(page, st); !ok {
		return
	}
	// 切到别的页时把残留的待确认状态清掉，否则回到原页会突然弹出旧确认框。
	if st.resetPendingPage != "" && st.resetPendingPage != page {
		st.resetPendingPage = ""
	}

	ui.Row(c.Key("settings-reset-row-" + page)).
		FillWidth().
		AlignItems(ui.Center).
		Gap(Space4).
		Children(func() {
			ui.Text(c, "把本页设置改回默认值；快捷回复、模型组合、档位等列表数据不受影响。").
				FontSize(TypeFootnote).
				TextColor(ThemeSecondaryText(c)).
				Grow(1)
			Button(c.Key("btn-settings-reset-"+page), ButtonOpts{
				Text:    "恢复本页默认",
				Variant: ButtonSecondary,
				Size:    ControlMD,
				OnClick: func() {
					st.resetPendingPage = page
					st.invalidate()
				},
			})
		})

}

// renderSettingsResetConfirm 把确认框放在设置卡片及二级面板之上。
func renderSettingsResetConfirm(c *ui.Context, st *SettingsState) {
	if st == nil || st.resetPendingPage == "" {
		return
	}
	page := st.resetPendingPage
	target, ok := settingsResetTargetFor(page, st)
	if !ok {
		return
	}
	RenderConfirmDialog(c, ConfirmDialog{
		Key:   "settings-reset-" + page,
		Open:  true,
		Tone:  ConfirmDanger,
		Title: "恢复「" + target.title + "」页的默认设置？",
		Body:  "只把本页对应的设置改回默认值，其他页不受影响。",
		Detail: []string{
			"重置范围：" + strings.Join(target.sections, "、"),
			"快捷回复、模型组合、档位等列表类数据保留。",
		},
		ConfirmLabel: "恢复默认",
		ConfirmA11y:  "确认恢复本页默认",
		CancelLabel:  "取消",
		OnConfirm:    func() { applySettingsPageReset(st, page) },
		OnCancel: func() {
			st.resetPendingPage = ""
			st.invalidate()
		},
	})
}

// applySettingsPageReset 把某一页的设置改回默认值并落盘。
// 重置范围由页面树派生：只调用各行实现的 reset，锁定行跳过，整页只落盘一次。
func applySettingsPageReset(st *SettingsState, page string) {
	if st == nil {
		return
	}
	st.resetPendingPage = ""
	p, ok := settingsTreePageOf(page)
	if !ok {
		return
	}
	resetSettingsTreePage(st, p)

	// save 只同步文字大小；外观页重置后主题也要立刻生效，否则要重启才换回默认主题。
	if page == "appearance" {
		st.mu.Lock()
		theme := st.cfg.Appearance.Theme
		st.mu.Unlock()
		if theme != "" {
			appearanceThemeApplied = theme
			applyThemeSource(mygo.ThemeSource(theme))
		}
	}
}
