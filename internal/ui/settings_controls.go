package mcodeui

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// bind 把配置字段绑定成控件需要的读写能力：指针字段遇 nil 返回默认值，
// 值字段直接读写，inverted 只在读写时取反、reset 仍委托同一个字段。
type bind[T any] struct {
	field  func(*config.Config) **T // 指针字段（可为空）
	plain  func(*config.Config) *T  // 值字段（可为空）
	def    T                        // 字段的原始默认值
	invert bool                     // 反向命名字段：读写取反
}

// ptr 绑定指针字段，nil 视为 def。
func ptr[T any](field func(*config.Config) **T, def T) bind[T] {
	return bind[T]{field: field, def: def}
}

// val 绑定值字段。
func val[T any](field func(*config.Config) *T, def T) bind[T] {
	return bind[T]{plain: field, def: def}
}

// inverted 反转 bool 绑定：读到的值取反，写入的值取反，reset 仍写回字段默认值。
func inverted(b bind[bool]) bind[bool] {
	b.invert = !b.invert
	return b
}

// value 读取当前值：指针为 nil 时返回默认值。
func (b bind[T]) value(cfg *config.Config) T {
	var v T
	switch {
	case b.field != nil:
		if p := b.field(cfg); p != nil && *p != nil {
			v = **p
		} else {
			v = b.def
		}
	case b.plain != nil:
		v = *b.plain(cfg)
	default:
		v = b.def
	}
	if b.invert {
		if flag, ok := any(v).(bool); ok {
			return any(!flag).(T)
		}
	}
	return v
}

// write 写回字段：指针字段为 nil 时先分配。
func (b bind[T]) write(cfg *config.Config, v T) {
	if b.invert {
		if flag, ok := any(v).(bool); ok {
			v = any(!flag).(T)
		}
	}
	if b.field != nil {
		p := b.field(cfg)
		if *p == nil {
			*p = new(T)
		}
		**p = v
		return
	}
	if b.plain != nil {
		*b.plain(cfg) = v
	}
}

// reset 恢复默认值：指针字段写回 nil，值字段写回默认值。
func (b bind[T]) reset(cfg *config.Config) {
	if b.field != nil {
		*b.field(cfg) = nil
		return
	}
	if b.plain != nil {
		*b.plain(cfg) = b.def
	}
}

// commitSettingsControl 是控件写配置的统一出口：先持 mu → cfgMu 写 cfg，锁外落盘。
func commitSettingsControl(st *SettingsState, write func(cfg *config.Config)) {
	st.mu.Lock()
	st.cfgMu.Lock()
	write(&st.cfg)
	st.cfgMu.Unlock()
	st.mu.Unlock()
	st.save()
}

// ---------------------------------------------------------------------------
// 开关
// ---------------------------------------------------------------------------

type toggleControl struct {
	b bind[bool]
}

// toggle 声明一个开关行。
func toggle(b bind[bool]) toggleControl { return toggleControl{b: b} }

func (t toggleControl) render(c *ui.Context, st *SettingsState, s setting) {
	t.renderDisabled(c, st, s, false)
}

func (t toggleControl) renderDisabled(c *ui.Context, st *SettingsState, s setting, disabled bool) {
	val := t.b.value(&st.cfg)
	label := "切换" + s.Title
	if disabled {
		// 锁定或条件禁用时只画禁用态，不给回调，值不会被改。
		SwitchDisabled(c, s.ID+"-switch", val, label)
		return
	}
	Switch(c, s.ID+"-switch", &val, label, func(next bool) {
		commitSettingsControl(st, func(cfg *config.Config) { t.b.write(cfg, next) })
	})
}

func (t toggleControl) reset(cfg *config.Config) { t.b.reset(cfg) }

// ---------------------------------------------------------------------------
// 选项
// ---------------------------------------------------------------------------

type choiceOption struct {
	Value string
	Label string
}

type choiceControl struct {
	b    bind[string]
	opts []choiceOption
	// display 非空时覆盖显示的当前值：锁定档位由启动参数决定，选择器要显示真正生效的那一档。
	display func(st *SettingsState) string
}

// choice 声明一个选项行：不超过 3 项用分段，超过用下拉。
func choice(b bind[string], opts []choiceOption) choiceControl {
	return choiceControl{b: b, opts: opts}
}

// withDisplay 覆盖显示的选中值，返回空串时仍用绑定值。
func (ch choiceControl) withDisplay(fn func(st *SettingsState) string) choiceControl {
	ch.display = fn
	return ch
}

// current 读显示的选中值。
func (ch choiceControl) current(st *SettingsState) string {
	if ch.display != nil {
		if v := ch.display(st); v != "" {
			return v
		}
	}
	return ch.b.value(&st.cfg)
}

func (ch choiceControl) render(c *ui.Context, st *SettingsState, s setting) {
	ch.renderDisabled(c, st, s, false)
}

func (ch choiceControl) renderDisabled(c *ui.Context, st *SettingsState, s setting, disabled bool) {
	if len(ch.opts) == 0 {
		return
	}
	cur := ch.current(st)
	idx := 0
	labels := make([]string, len(ch.opts))
	for i, opt := range ch.opts {
		labels[i] = opt.Label
		if opt.Value == cur {
			idx = i
		}
	}

	if len(ch.opts) <= 3 {
		segments := make([]Segment, len(ch.opts))
		for i, opt := range ch.opts {
			segments[i] = Segment{Label: opt.Label, Disabled: disabled}
		}
		Segmented(c, s.ID+"-seg", &idx, segments, func(next int) {
			if next < 0 || next >= len(ch.opts) {
				return
			}
			value := ch.opts[next].Value
			commitSettingsControl(st, func(cfg *config.Config) { ch.b.write(cfg, value) })
		}).Label(s.Title)
		return
	}

	sel := labels[idx]
	if cur != "" && cur != ch.opts[idx].Value {
		// 配置里是不认识的档位：不假装选中第一项，把原值写出来，状态行负责解释。
		sel = cur
	}
	oldSel := sel
	pick := ui.Select(c.Key(s.ID+"-select"), &sel, labels).
		Label(s.Title).
		Width(160).
		Height(RowHeight).
		Disabled(disabled)
	if disabled || (sel == oldSel && !pick.Changed()) {
		return
	}
	for _, opt := range ch.opts {
		if opt.Label == sel {
			next := opt.Value
			commitSettingsControl(st, func(cfg *config.Config) { ch.b.write(cfg, next) })
			return
		}
	}
}

func (ch choiceControl) reset(cfg *config.Config) { ch.b.reset(cfg) }

// ---------------------------------------------------------------------------
// 数值
// ---------------------------------------------------------------------------

func settingsSlider(c *ui.Context, key string, value *float64, min, max float64, label string) bool {
	return ui.Slider(c.Key(key), value, min, max).Step(1).Width(settingsPreviewTileW * 2).Label(label).Changed()
}

type numberControl struct {
	b        bind[int]
	unit     string
	min, max int
}

// number 声明一个数值行：输入框加单位后缀，默认值作占位，提交时 clamp。
func number(b bind[int], unit string, minV, maxV int) numberControl {
	return numberControl{b: b, unit: unit, min: minV, max: maxV}
}

func (n numberControl) render(c *ui.Context, st *SettingsState, s setting) {
	n.renderDisabled(c, st, s, false)
}

func (n numberControl) renderDisabled(c *ui.Context, st *SettingsState, s setting, disabled bool) {
	// 越界的存量值（例如 0 表示未设置）按默认值显示，默认值同时是输入框的占位。
	cur := n.b.value(&st.cfg)
	if cur < n.min || cur > n.max {
		cur = n.b.def
	}
	txt := strconv.Itoa(cur)
	oldTxt := txt
	ui.Row(c.Key(s.ID + "-value")).AlignItems(ui.Center).Gap(Space2).Children(func() {
		TextField(c, TextFieldProps{
			Key:         s.ID + "-input",
			Value:       &txt,
			Placeholder: strconv.Itoa(n.b.def),
			Width:       90,
			Disabled:    disabled,
		})
		if n.unit != "" {
			ui.Text(c, n.unit).FontSize(uiFont(TypeBody)).TextColor(ThemeSecondaryText(c))
		}
	})
	if disabled || txt == oldTxt {
		return
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(txt))
	if err != nil {
		return
	}
	parsed = min(max(parsed, n.min), n.max)
	if parsed == cur {
		return
	}
	commitSettingsControl(st, func(cfg *config.Config) { n.b.write(cfg, parsed) })
}

func (n numberControl) reset(cfg *config.Config) { n.b.reset(cfg) }

// ---------------------------------------------------------------------------
// 文本
// ---------------------------------------------------------------------------

type textControl struct {
	b           bind[string]
	placeholder string
}

// text 声明一个文本行，持久化走 noteTextEdit 的防抖。
func text(b bind[string], placeholder string) textControl {
	return textControl{b: b, placeholder: placeholder}
}

func (t textControl) render(c *ui.Context, st *SettingsState, s setting) {
	t.renderDisabled(c, st, s, false)
}

func (t textControl) renderDisabled(c *ui.Context, st *SettingsState, s setting, disabled bool) {
	val := t.b.value(&st.cfg)
	TextField(c, TextFieldProps{
		Key:         s.ID + "-input",
		Value:       &val,
		Placeholder: t.placeholder,
		Width:       settingsTextFieldWidth,
		Disabled:    disabled,
		OnChange: func(text string) {
			st.mu.Lock()
			st.cfgMu.Lock()
			t.b.write(&st.cfg, text)
			st.cfgMu.Unlock()
			st.mu.Unlock()
			st.noteTextEdit()
		},
		// 回车立即落盘，不用等文本防抖到期。
		OnSubmit: func(string) {
			st.flushTextSave(true)
		},
	})
}

func (t textControl) reset(cfg *config.Config) { t.b.reset(cfg) }

// ---------------------------------------------------------------------------
// 回调控件
// ---------------------------------------------------------------------------

// toggleFuncControl 用于值不在 config 里、或写入是异步且期间要显示忙碌的开关：
// 控件只调用回调，读值、写值与 I/O 都由调用方负责；不实现 settingResettable。
type toggleFuncControl struct {
	get      func(st *SettingsState) (on bool, busy bool)
	set      func(st *SettingsState, on bool)
	disabled func(st *SettingsState) bool
}

// toggleFunc 声明一个回调式开关。
func toggleFunc(get func(st *SettingsState) (on bool, busy bool), set func(st *SettingsState, on bool)) toggleFuncControl {
	return toggleFuncControl{get: get, set: set}
}

// withDisabled 叠加额外的禁用条件；锁定仍由渲染器统一传 disabled。
func (t toggleFuncControl) withDisabled(fn func(st *SettingsState) bool) toggleFuncControl {
	t.disabled = fn
	return t
}

func (t toggleFuncControl) render(c *ui.Context, st *SettingsState, s setting) {
	t.renderDisabled(c, st, s, false)
}

func (t toggleFuncControl) renderDisabled(c *ui.Context, st *SettingsState, s setting, disabled bool) {
	if t.get == nil {
		return
	}
	on, busy := t.get(st)
	val := on
	off := disabled || busy || (t.disabled != nil && t.disabled(st))
	label := "切换" + s.Title
	if off {
		SwitchDisabled(c, s.ID+"-switch", on, label)
		return
	}
	Switch(c, s.ID+"-switch", &val, label, func(next bool) {
		if t.set != nil {
			t.set(st, next)
		}
	})
}

// choiceFuncControl 用于值不在 config 里、或写入是异步且期间要显示忙碌的选项：
// 2 到 3 项用分段，更多项用下拉，与 choiceControl 保持一致；不实现 settingResettable。
type choiceFuncControl struct {
	options      []string
	get          func(st *SettingsState) (idx int, busy bool)
	set          func(st *SettingsState, idx int)
	disabled     func(st *SettingsState) bool
	itemDisabled func(st *SettingsState, idx int) bool
	fill         bool
}

// choiceFunc 声明一个回调式选项行。
func choiceFunc(options []string, get func(st *SettingsState) (idx int, busy bool), set func(st *SettingsState, idx int)) choiceFuncControl {
	return choiceFuncControl{options: options, get: get, set: set}
}

// withItemDisabled 让单个选项不可选（例如依赖别处勾选的路由），其余选项照常可点。
// 只对分段生效，下拉不支持单项禁用。
func (ch choiceFuncControl) withItemDisabled(fn func(st *SettingsState, idx int) bool) choiceFuncControl {
	ch.itemDisabled = fn
	return ch
}

// itemOff 报告单个选项是否不可选。
func (ch choiceFuncControl) itemOff(st *SettingsState, idx int) bool {
	return ch.itemDisabled != nil && ch.itemDisabled(st, idx)
}

func (ch choiceFuncControl) render(c *ui.Context, st *SettingsState, s setting) {
	ch.renderDisabled(c, st, s, false)
}

func (ch choiceFuncControl) renderDisabled(c *ui.Context, st *SettingsState, s setting, disabled bool) {
	if len(ch.options) == 0 || ch.get == nil {
		return
	}
	idx, busy := ch.get(st)
	if idx < 0 || idx >= len(ch.options) {
		idx = 0
	}
	off := disabled || busy || (ch.disabled != nil && ch.disabled(st))

	if len(ch.options) <= 3 {
		segments := make([]Segment, len(ch.options))
		for i, opt := range ch.options {
			segments[i] = Segment{Label: opt, Disabled: off || ch.itemOff(st, i)}
		}
		apply := func(next int) {
			if ch.set != nil && next >= 0 && next < len(ch.options) {
				ch.set(st, next)
			}
		}
		if ch.fill {
			SegmentedFill(c, s.ID+"-seg", &idx, segments, apply).Label(s.Title)
			return
		}
		Segmented(c, s.ID+"-seg", &idx, segments, apply).Label(s.Title)
		return
	}

	sel := ch.options[idx]
	oldSel := sel
	pick := ui.Select(c.Key(s.ID+"-select"), &sel, ch.options).
		Label(s.Title).
		Width(160).
		Height(RowHeight).
		Disabled(off)
	if off || (sel == oldSel && !pick.Changed()) {
		return
	}
	for i, opt := range ch.options {
		if opt == sel {
			if ch.set != nil {
				ch.set(st, i)
			}
			return
		}
	}
}

// textFuncControl 用于值不在 config 里、或保存是异步且期间要显示忙碌的文本行：
// 内容变化时只调用 set，不写配置也不落盘；不实现 settingResettable。
type textFuncControl struct {
	get         func(st *SettingsState) (text string, busy bool)
	set         func(st *SettingsState, text string)
	submit      func(st *SettingsState, text string)
	placeholder string
	width       float32
	secret      bool
	disabled    func(st *SettingsState) bool
}

// settingsTextFieldWidth 是回调式文本行的默认宽度。
const settingsTextFieldWidth float32 = 220

// textFunc 声明一个回调式文本行。
func textFunc(get func(st *SettingsState) (text string, busy bool), set func(st *SettingsState, text string), placeholder string) textFuncControl {
	return textFuncControl{get: get, set: set, placeholder: placeholder}
}

// withWidth 覆盖输入框宽度。
func (t textFuncControl) withWidth(width float32) textFuncControl {
	t.width = width
	return t
}

// withSubmit 覆盖回车提交的动作；默认与 set 相同。
func (t textFuncControl) withSubmit(fn func(st *SettingsState, text string)) textFuncControl {
	t.submit = fn
	return t
}

// withSecret 把输入框改为密码样式。
func (t textFuncControl) withSecret() textFuncControl {
	t.secret = true
	return t
}

// withDisabled 叠加额外的禁用条件；锁定仍由渲染器统一传 disabled。
func (t textFuncControl) withDisabled(fn func(st *SettingsState) bool) textFuncControl {
	t.disabled = fn
	return t
}

func (t textFuncControl) render(c *ui.Context, st *SettingsState, s setting) {
	t.renderDisabled(c, st, s, false)
}

func (t textFuncControl) renderDisabled(c *ui.Context, st *SettingsState, s setting, disabled bool) {
	if t.get == nil {
		return
	}
	val, busy := t.get(st)
	off := disabled || busy || (t.disabled != nil && t.disabled(st))
	width := t.width
	if width <= 0 {
		width = settingsTextFieldWidth
	}
	TextField(c, TextFieldProps{
		Key:         s.ID + "-input",
		Value:       &val,
		Placeholder: t.placeholder,
		Width:       width,
		Label:       "编辑" + s.Title,
		Password:    t.secret,
		Disabled:    off,
		OnChange: func(text string) {
			if t.set != nil {
				t.set(st, text)
			}
		},
		// 回车提交：默认与每次改动相同，调用方可用 withSubmit 换成立即生效的动作。
		OnSubmit: func(text string) {
			submit := t.submit
			if submit == nil {
				submit = t.set
			}
			if submit != nil {
				submit(st, text)
			}
		},
	})
}

// ---------------------------------------------------------------------------
// 入口
// ---------------------------------------------------------------------------

type linkControl struct {
	value func(st *SettingsState) string
	sheet string
}

// link 声明一个入口行：显示当前值加箭头，点击打开面板。
func link(value func(st *SettingsState) string, sheet string) linkControl {
	return linkControl{value: value, sheet: sheet}
}

func (l linkControl) render(c *ui.Context, st *SettingsState, s setting) {
	ui.Row(c.Key(s.ID + "-link-body")).AlignItems(ui.Center).Gap(Space1).Children(func() {
		ui.Text(c, l.value(st)).FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c)).SingleLine()
		ui.Icon(c, IconChevronRight()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
	})
}

// ---------------------------------------------------------------------------
// 列表
// ---------------------------------------------------------------------------

// settingListRow 是列表里的一行：ID 拼在列表前缀之后，Title 原样显示。
type settingListRow struct {
	ID    string
	Title string
}

type listControl struct {
	prefix string
	rows   func(st *SettingsState) []settingListRow
}

// list 声明一个动态列表：条目由 rows 提供，搜索索引只认前缀一条。
func list(prefix string, rows func(st *SettingsState) []settingListRow) listControl {
	return listControl{prefix: prefix, rows: rows}
}

func (l listControl) render(c *ui.Context, st *SettingsState, s setting) {
	ui.Column(c.Key(s.ID + "-list")).Gap(Space0).Children(func() {
		for _, r := range l.rows(st) {
			renderSettingRow(c, l.prefix+r.ID, r.Title, func() {})
		}
	})
}

// ---------------------------------------------------------------------------
// 自定义
// ---------------------------------------------------------------------------

type customControl struct {
	renderFn  func(c *ui.Context, st *SettingsState)
	fullWidth bool
}

// custom 声明一行完全自定义的内容，由调用方负责绘制。
func custom(renderFn func(c *ui.Context, st *SettingsState)) customControl {
	return customControl{renderFn: renderFn}
}

func customFullWidth(renderFn func(c *ui.Context, st *SettingsState)) customControl {
	return customControl{renderFn: renderFn, fullWidth: true}
}

func (cc customControl) render(c *ui.Context, st *SettingsState, s setting) {
	if s.Title == "" {
		// 没有行标题时内容占满整行，不留一列空白去挤压右侧内容。
		row := ui.Column(c.Key(s.ID))
		if !cc.fullWidth {
			row.Padding(settingsRowPadY, settingsRowPadX)
		}
		applySettingsRowFocus(c, row, s.ID).Children(func() {
			cc.renderFn(c, st)
		})
		return
	}
	if cc.fullWidth {
		// 整宽的自定义内容把标题放到上方，避免和内容挤在同一行。
		renderStackedSettingRow(c, s.ID, s.Title, func() { cc.renderFn(c, st) })
		return
	}
	renderSettingRow(c, s.ID, s.Title, func() { cc.renderFn(c, st) })
}
