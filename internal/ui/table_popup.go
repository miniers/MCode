package mcodeui

import (
	"fmt"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"
)

// 聊天消息里的表格在窄消息列里会被横向滚动裁切（markdown 包用 ui.ScrollHorizontal 渲染表格）。
// 这里给每张写完的表格在右上角叠一个「展开」按钮，点击后用根层浮窗完整展示这张表：
//   - 块钩子只接管 parser.Table 块，流式还没写完的表格（b.Open）仍走默认渲染，避免重新解析导致抖动；
//   - 表格本体用一份不带本钩子的 Markdown 选项重新渲染，外观与默认表格一致，也不会递归触发钩子；
//   - 浮窗挂在根视图（与图片大图浮层同层），不受消息列表裁切，标题栏可拖动、三处抓手可缩放。

// tablePopupMinWidth、tablePopupMinHeight 是面板的最小尺寸，tablePopupInset 是面板离窗口边缘
// 至少保留的间距；其余依次是卡片圆角、标题行高度、缩放抓手的粗细与留白、右下角抓手与
// 「展开」按钮的边长及其悬停提示。
const (
	tablePopupMinWidth      = 360
	tablePopupMinHeight     = 200
	tablePopupInset         = 20
	tablePopupRadius        = 10
	tablePopupHeaderHeight  = 34
	tablePopupExpandSize    = 22
	tablePopupExpandTooltip = "在浮窗中查看表格"
)

// tablePopupState 是正在浮窗中展示的表格：实例 key、表格源码、渲染表格本体用的选项，
// 以及打开时重建好的 GFM 表格文本（供「复制为 Markdown」，避免渲染期重新解析文档）。
type tablePopupState struct {
	key      string            // 浮窗实例 key，同时用作尺寸表的 key
	src      string            // 表格的 Markdown 源码
	opts     *markdown.Options // 不带本钩子的 Markdown 选项，避免浮窗内再次展开
	markdown string            // 由单元格重建的 GFM 表格，复制时原样写入剪贴板
}

var (
	// tablePopupMu 保护下面两项浮窗状态。
	tablePopupMu sync.Mutex
	// tablePopupOpen 是当前展开的表格，同一时刻最多一个浮窗。
	tablePopupOpen *tablePopupState
)

// tableExpandHooksInstalled 记录已装过表格展开钩子的 Markdown 选项，避免重复包装 Block。
var tableExpandHooksInstalled sync.Map // *markdown.Options → struct{}

// tableBodyOptionsCache 缓存「不带表格展开钩子」的 Markdown 选项副本，按原选项区分。
var tableBodyOptionsCache sync.Map // *markdown.Options → *markdown.Options

// installTableExpandHook 给 Markdown 选项装上表格展开钩子：每张写完的表格右上角多一个「展开」按钮，
// 点击后在可缩放的浮窗里完整展示这张表。原有 Block 钩子被包一层并优先调用，每个选项只安装一次。
func installTableExpandHook(o *markdown.Options, s *chatState) {
	if o == nil || s == nil {
		return
	}
	if _, done := tableExpandHooksInstalled.LoadOrStore(o, struct{}{}); done {
		return
	}
	o.Block = tableExpandBlockHook(o, s, o.Block)
}

// tableExpandBlockHook 把原 Block 钩子包一层：只有写完整的表格块才交给自己渲染，
// 其余块（含图片段落）先交给原钩子，仍不接管时返回 false 交给默认渲染。
func tableExpandBlockHook(o *markdown.Options, s *chatState, prev func(c *ui.Context, d *parser.Document, i int) bool) func(c *ui.Context, d *parser.Document, i int) bool {
	return func(c *ui.Context, d *parser.Document, i int) bool {
		if prev != nil && prev(c, d, i) {
			return true
		}
		if !expandableTableBlock(d, i) {
			return false
		}
		renderExpandableTable(c, s, o, d, i)
		return true
	}
}

// expandableTableBlock 报告第 i 块是不是可以展开的表格。
// 只接管完整的表格块：流式还没写完的表格（Open）走默认渲染，否则每来一段增量就重新解析会抖动。
// 也只接管从行首开始的表格：表格在引用块或列表里时块起点前面还有 "> "、缩进等前缀，
// 单取源码片段重新解析会还原不出表格，这类表格仍走默认渲染。
func expandableTableBlock(d *parser.Document, i int) bool {
	if d == nil || i < 0 || i >= d.Len() {
		return false
	}
	b := d.Block(i)
	if b.Kind != parser.Table || b.Open {
		return false
	}
	src := d.Source()
	if b.Start < 0 || b.End <= b.Start || b.End > len(src) {
		return false
	}
	return b.Start == 0 || src[b.Start-1] == '\n'
}

// tableBlockSource 返回表格块的 Markdown 源码（End 含整张表的所有行），越界时返回空串。
func tableBlockSource(d *parser.Document, i int) string {
	if !expandableTableBlock(d, i) {
		return ""
	}
	b := d.Block(i)
	return d.Source()[b.Start:b.End]
}

// tablePopupKey 生成浮窗实例 key：同一份选项里的表格按源码偏移区分，不同选项互不影响。
func tablePopupKey(o *markdown.Options, start int) string {
	return fmt.Sprintf("%p:%d", o, start)
}

// tableBodyOptions 返回渲染表格本体的 Markdown 选项：主题、链接、图片、代码块等与原选项保持一致，
// 但不带 Block 钩子，重新渲染表格时不会再进本钩子（否则无限递归）。
// markdown.Options 内含 sync.Map，不能整体拷贝，只能新建后逐字段赋值；每个原选项只建一份并复用，
// 逐帧把可变字段同步回来，保证主题与回调改了立刻生效。
func tableBodyOptions(o *markdown.Options) *markdown.Options {
	if o == nil {
		return nil
	}
	derived := &markdown.Options{}
	actual, _ := tableBodyOptionsCache.LoadOrStore(o, derived)
	out := actual.(*markdown.Options)
	out.Theme = o.Theme
	out.Extensions = o.Extensions
	out.OnLink = o.OnLink
	out.LoadImage = o.LoadImage
	out.CodeBlock = o.CodeBlock
	out.Code = o.Code
	out.WrapCode = o.WrapCode
	out.Math = o.Math
	out.InlineMath = o.InlineMath
	out.HTML = o.HTML
	out.Reveal = o.Reveal
	out.ShareSelection = o.ShareSelection
	// Block 保持为空：表格本体走 markdown 的默认表格渲染。
	return out
}

// tableCellSource 取出单元格的原文：Block.Start/End 已由解析器裁掉两侧竖线与空白，
// 没有可用区间时返回空串；原文里的 `\|` 是转义后的竖线，这里先还原成单个 `|`。
func tableCellSource(src string, b *parser.Block) string {
	if b == nil || b.Start < 0 || b.End <= b.Start || b.End > len(src) {
		return ""
	}
	return strings.ReplaceAll(strings.TrimSpace(src[b.Start:b.End]), `\|`, "|")
}

// tableCellsFromDocument 取出第 i 块表格的行与单元格文本：按 Table → TableRow → TableCell
// 逐层遍历，行的列数以表格列数为准，缺失的单元格补空串。不是表格块时返回 nil。
func tableCellsFromDocument(d *parser.Document, i int) [][]string {
	if d == nil || i < 0 || i >= d.Len() {
		return nil
	}
	if d.Block(i).Kind != parser.Table {
		return nil
	}
	src := d.Source()
	var rows [][]string
	for rowIdx := range d.Children(i) {
		row := d.Block(rowIdx)
		if row.Kind != parser.TableRow {
			continue
		}
		var cells []string
		for cellIdx := range d.Children(rowIdx) {
			cell := d.Block(cellIdx)
			if cell.Kind != parser.TableCell {
				continue
			}
			cells = append(cells, tableCellSource(src, cell))
		}
		// 解析器会为表头与每行补齐到列数（缺失单元格为空区间），这里仍过滤空行，
		// 避免分隔行被当成数据行重复输出。
		if len(cells) > 0 {
			rows = append(rows, cells)
		}
	}
	return rows
}

// tableCellMarkdown 清洗单个单元格：单元格里不能有换行，折成空格；
// 竖线是 GFM 表格的列分隔符，必须转义成 `\|`。
func tableCellMarkdown(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(strings.TrimSpace(s), "|", `\|`)
}

// tableCellsMarkdown 把单元格重建为 GFM 表格文本：第一行是表头，第二行是分隔行，
// 每行按表头列数补齐（GFM 要求每行列数一致），行尾不留空白。rows 为空时返回空串。
func tableCellsMarkdown(rows [][]string) string {
	cols := 0
	for _, row := range rows {
		if len(row) > cols {
			cols = len(row)
		}
	}
	if cols == 0 {
		return ""
	}
	var b strings.Builder
	for i, row := range rows {
		b.WriteString("|")
		for j := 0; j < cols; j++ {
			cell := ""
			if j < len(row) {
				cell = tableCellMarkdown(row[j])
			}
			b.WriteString(" " + cell + " |")
		}
		b.WriteString("\n")
		if i == 0 {
			// 对齐信息不参与复制：统一用无对齐的 `---`，避免把单元格文本误当成对齐标记。
			b.WriteString("|")
			for j := 0; j < cols; j++ {
				b.WriteString(" --- |")
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// renderExpandableTable 渲染表格块：先按默认外观渲染表格本体，再在右上角叠一个「展开」按钮。
func renderExpandableTable(c *ui.Context, s *chatState, o *markdown.Options, d *parser.Document, i int) {
	src := tableBlockSource(d, i)
	if src == "" {
		return
	}
	opts := tableBodyOptions(o)
	key := tablePopupKey(o, d.Block(i).Start)
	// 单元格只在渲染时取一次：浮窗里的复制按钮直接用这份结果，避免渲染期重新解析文档。
	rows := tableCellsFromDocument(d, i)
	ui.Column(c).AlignSelf(ui.Stretch).Children(func() {
		markdown.View(c, src, opts)
		renderTableExpandButton(c, s, key, src, opts, rows)
	})
}

// renderTableExpandButton 渲染表格右上角的「展开」按钮：绝对定位、不占表格布局，
// 常驻但低对比度，悬停时才变亮，避免遮住表格内容。
func renderTableExpandButton(c *ui.Context, s *chatState, key, src string, opts *markdown.Options, rows [][]string) {
	btn := ui.ButtonBase(c.Key("md-table-expand-" + key)).
		Absolute().
		Right(4).
		Top(4).
		Width(tablePopupExpandSize).
		Height(tablePopupExpandSize).
		Radius(RadiusXS).
		AlignItems(ui.Center).
		Justify(ui.Center).
		Cursor(ui.CursorPointer).
		Label("展开表格").
		Tooltip(tablePopupExpandTooltip)
	if btn.Hovered() {
		btn.Background(ThemeHover(c))
	} else {
		btn.Background(ThemeWindowBg(c).Alpha(0.55))
	}
	btn.Children(func() {
		ui.Icon(c, IconSquareArrowOutUpRight()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
	})
	btn.OnClick(func() {
		openTablePopup(s, key, src, opts, rows)
	})
}

// openTablePopup 记下要展开的表格（含重建好的 GFM 表格文本）并重绘；浮窗由根视图统一渲染，不受消息列表裁切。
func openTablePopup(s *chatState, key, src string, opts *markdown.Options, rows [][]string) {
	tablePopupMu.Lock()
	tablePopupOpen = &tablePopupState{key: key, src: src, opts: opts, markdown: tableCellsMarkdown(rows)}
	tablePopupMu.Unlock()
	if s != nil {
		s.invalidate()
	}
}

// closeTablePopup 关闭浮窗，并清掉这张表的尺寸与拖拽偏移，下次打开回到默认大小与位置。
func closeTablePopup(s *chatState, key string) {
	tablePopupMu.Lock()
	tablePopupOpen = nil
	tablePopupMu.Unlock()
	modalDragReset(modalDragTablePopup)
	if s != nil {
		s.invalidate()
	}
}

// defaultTablePopupSize 是浮窗首次打开时的尺寸：窗口的 70% 宽、60% 高，再夹到允许范围内。
func defaultTablePopupSize(winW, winH float32) (float32, float32) {
	return clampTablePopupSize(winW*0.7, winH*0.6, winW, winH)
}

// clampTablePopupSize 把期望尺寸夹到 [最小尺寸, 窗口尺寸-2*tablePopupInset] 之间。
func clampTablePopupSize(w, h, winW, winH float32) (float32, float32) {
	return clampTablePopupAxis(w, winW, tablePopupMinWidth), clampTablePopupAxis(h, winH, tablePopupMinHeight)
}

// clampTablePopupAxis 夹取单轴尺寸：窗口比最小尺寸还小时以窗口为准（至少留 1），保证浮窗不出窗口。
func clampTablePopupAxis(v, win, minSide float32) float32 {
	limit := win - 2*tablePopupInset
	if limit < minSide {
		return max(limit, 1)
	}
	return min(max(v, minSide), limit)
}

// renderTablePopupOverlay 渲染表格浮窗：半透明遮罩 + 自绘圆角卡片（不用 ui.Modal，避免卡片外多一圈黑边）。
// 点击遮罩或按 Esc 关闭，标题栏可拖动，右边、下边、右下角三处抓手可缩放。
func renderTablePopupOverlay(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	tablePopupMu.Lock()
	st := tablePopupOpen
	tablePopupMu.Unlock()
	if st == nil || st.src == "" {
		return
	}

	winW, winH := c.Size()
	defaultW, defaultH := defaultTablePopupSize(winW, winH)
	minW := min(float32(tablePopupMinWidth), max(1, winW-2*tablePopupInset))
	minH := min(float32(tablePopupMinHeight), max(1, winH-2*tablePopupInset))
	width, height := modalResizeSize(modalDragTablePopup, defaultW, defaultH, minW, minH, winW, winH)

	open := true
	ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {
		// 半透明遮罩铺满窗口；点击遮罩与 Esc 由 DialogBase 写回 open。
		backdrop.
			Absolute().
			Left(0).
			Top(0).
			Right(0).
			Bottom(0).
			Center().
			Background(ui.Color{}.Alpha(ScrimOpacity))

		// panel 本身不加外观，尺寸与外观都交给内层唯一的圆角卡片。
		panel.Key("table-popup-panel").
			Label("table-popup-panel").
			Width(width).
			Height(height).
			Padding(0).
			Gap(0)
		modalDragPanel(c, modalDragTablePopup, panel)

		card := ui.Column(c.Key("table-popup-card")).
			Label("table-popup-card").
			Fill().
			Radius(tablePopupRadius).
			Background(ThemeWindowBg(c)).
			BorderWidth(Hairline).
			BorderColor(ThemeBorder(c))
		applyElevation(c, card, ElevDialog).
			Gap(0).
			Children(func() {
				renderTablePopupHeader(c, s, st)
				ui.Scroll(c.Key("table-popup-body")).
					FillWidth().
					Grow(1).
					Padding(Space2, Space3).
					Children(func() {
						// 表格内部已有横向滚动，外层只补纵向滚动；选择交给这一层容器。
						ui.Column(c.Key("table-popup-body-content")).
							FillWidth().
							Selectable().
							Children(func() {
								markdown.View(c, st.src, st.opts)
							})
					})
				modalResizeGripsMin(c, modalDragTablePopup, width, height, minW, minH)
			})
	})
	// DialogBase 已经把遮罩点击与 Esc 写回 open，这里据此关闭。
	if !open {
		closeTablePopup(s, st.key)
	}
}

// renderTablePopupHeader 渲染浮窗标题行：「表格」标题、关闭按钮，整行可拖动移动浮窗。
func renderTablePopupHeader(c *ui.Context, s *chatState, st *tablePopupState) {
	header := ui.Row(c.Key("table-popup-header")).
		FillWidth().
		Height(tablePopupHeaderHeight).
		Padding(0, Space3).
		AlignItems(ui.Center).
		Gap(Space3).
		BorderWidth(0, 0, Hairline, 0).
		BorderColor(ThemeBorder(c))
	header.Children(func() {
		ui.Icon(c, IconSquareArrowOutUpRight()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
		ui.Text(c, "表格").
			FontSize(TypeBody).
			FontWeight(600).
			TextColor(ThemePrimaryText(c))
		ui.Box(c).Grow(1)
		// 复制按钮：把重建好的 GFM 表格写进剪贴板，单元格里的竖线已转义。
		Button(c.Key("table-popup-copy"), ButtonOpts{
			Icon:     IconCopy(),
			Text:     "复制为 Markdown",
			Variant:  ButtonSecondary,
			Label:    "复制为 Markdown",
			Size:     ControlSM,
			Disabled: st.markdown == "",
			OnClick: func() {
				if st.markdown == "" {
					return
				}
				c.WriteClipboard(st.markdown)
				if s != nil {
					s.setToast("已复制表格 Markdown")
				}
			},
		})
		closeBtn := ui.ButtonBase(c.Key("table-popup-close")).
			Width(ControlXS).
			Height(ControlXS).
			Radius(RadiusXS).
			AlignItems(ui.Center).
			Justify(ui.Center).
			Cursor(ui.CursorPointer).
			Label("关闭").
			Tooltip("关闭")
		if closeBtn.Hovered() {
			closeBtn.Background(ThemeHover(c))
		}
		closeBtn.Children(func() {
			ui.Icon(c, IconX()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
		})
		closeBtn.OnClick(func() {
			closeTablePopup(s, st.key)
		})
	})
	// 标题行当拖拽把手：按在关闭按钮上的指针由按钮接管，不会拖动窗口。
	modalDragHandle(c, modalDragTablePopup, header)
}

