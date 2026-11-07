package mcodeui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"
)

// TestClampTablePopupSize 校验浮窗尺寸夹取：默认比例、最小尺寸、最大尺寸与窗口过小的情况。
func TestClampTablePopupSize(t *testing.T) {
	tests := []struct {
		name             string
		w, h, winW, winH float32
		wantW, wantH     float32
	}{
		{name: "默认比例内不夹取", w: 700, h: 480, winW: 1000, winH: 800, wantW: 700, wantH: 480},
		{name: "小于最小尺寸取最小", w: 100, h: 80, winW: 1000, winH: 800, wantW: tablePopupMinWidth, wantH: tablePopupMinHeight},
		{name: "大于窗口留出间距", w: 2000, h: 2000, winW: 1000, winH: 800, wantW: 960, wantH: 760},
		{name: "窗口比最小尺寸还小以窗口为准", w: 700, h: 700, winW: 300, winH: 150, wantW: 260, wantH: 110},
		{name: "窗口过小时至少为 1", w: 700, h: 700, winW: 30, winH: 30, wantW: 1, wantH: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotW, gotH := clampTablePopupSize(tt.w, tt.h, tt.winW, tt.winH)
			if gotW != tt.wantW || gotH != tt.wantH {
				t.Fatalf("clampTablePopupSize(%v, %v, %v, %v) = (%v, %v)，期望 (%v, %v)",
					tt.w, tt.h, tt.winW, tt.winH, gotW, gotH, tt.wantW, tt.wantH)
			}
		})
	}
}

// TestDefaultTablePopupSize 校验默认尺寸是窗口的 70% 宽、60% 高，并且同样经过夹取。
func TestDefaultTablePopupSize(t *testing.T) {
	w, h := defaultTablePopupSize(1200, 900)
	if w != 840 || h != 540 {
		t.Fatalf("defaultTablePopupSize(1200, 900) = (%v, %v)，期望 (840, 540)", w, h)
	}
	// 小窗口下默认尺寸不能低于最小尺寸，也不能超出窗口。
	w, h = defaultTablePopupSize(500, 300)
	if w != tablePopupMinWidth || h != tablePopupMinHeight {
		t.Fatalf("defaultTablePopupSize(500, 300) = (%v, %v)，期望 (%v, %v)", w, h, tablePopupMinWidth, tablePopupMinHeight)
	}
}

// TestExpandableTableBlock 校验只接管写完整的表格块：段落不算，流式未写完（Open）的表格也不算。
func TestExpandableTableBlock(t *testing.T) {
	const src = "| 名称 | 说明 |\n| --- | --- |\n| 甲 | 乙 |\n\n段落\n"
	doc := parser.New(parser.Default)
	doc.Set(src)

	tableIdx := -1
	paraIdx := -1
	for i := 1; i < doc.Len(); i++ {
		switch doc.Block(i).Kind {
		case parser.Table:
			if tableIdx < 0 {
				tableIdx = i
			}
		case parser.Paragraph:
			if paraIdx < 0 {
				paraIdx = i
			}
		}
	}
	if tableIdx < 0 || paraIdx < 0 {
		t.Fatalf("测试源码应同时解析出表格与段落，得到 table=%d paragraph=%d", tableIdx, paraIdx)
	}
	if !expandableTableBlock(doc, tableIdx) {
		t.Fatal("写完整的表格块应当可以展开")
	}
	if expandableTableBlock(doc, paraIdx) {
		t.Fatal("段落不应被表格钩子接管")
	}
	if expandableTableBlock(doc, 0) {
		t.Fatal("根块不应被表格钩子接管")
	}
	if expandableTableBlock(nil, tableIdx) {
		t.Fatal("空文档不应被表格钩子接管")
	}

	// 取出的源码能重新解析出同一张表（表头 + 一行数据），而不是退回成段落。
	got := tableBlockSource(doc, tableIdx)
	if got == "" {
		t.Fatal("写完整的表格应能取到源码")
	}
	if !strings.HasPrefix(src, got) {
		t.Fatalf("表格源码应是原文的前缀，得到 %q", got)
	}
	again := parser.New(parser.Default)
	again.Set(got)
	againIdx := -1
	rows := 0
	for i := 1; i < again.Len(); i++ {
		switch again.Block(i).Kind {
		case parser.Table:
			againIdx = i
		case parser.TableRow:
			rows++
		}
	}
	if againIdx < 0 {
		t.Fatalf("取出的表格源码应能重新解析出表格，得到 %q", got)
	}
	if rows != 2 {
		t.Fatalf("取出的表格源码应含表头与一行数据，得到 %d 行：%q", rows, got)
	}

	// 流式还没写完的表格不接管，避免每来一段增量就重新解析导致抖动。
	doc.Block(tableIdx).Open = true
	if expandableTableBlock(doc, tableIdx) {
		t.Fatal("流式未写完的表格应改走默认渲染")
	}
	if src := tableBlockSource(doc, tableIdx); src != "" {
		t.Fatalf("未写完的表格不应提供源码，得到 %q", src)
	}

	// 表格在引用块或列表里时块起点前面还有 "> "、缩进等前缀，单取片段重新解析会还原不出表格：不接管。
	// 这里直接把块起点往后挪一字节，模拟“起点前面不是换行”的情况。
	doc.Block(tableIdx).Open = false
	start := doc.Block(tableIdx).Start
	doc.Block(tableIdx).Start = start + 1
	if expandableTableBlock(doc, tableIdx) {
		t.Fatal("块起点前面不是换行时应改走默认渲染")
	}
	doc.Block(tableIdx).Start = start
}

// TestInstallTableExpandHookOnce 校验每个 Markdown 选项只装一次钩子，且派生选项不带钩子（否则递归）。
func TestInstallTableExpandHookOnce(t *testing.T) {
	o := &markdown.Options{}
	s := &chatState{}

	installTableExpandHook(o, s)
	if o.Block == nil {
		t.Fatal("安装后应当有 Block 钩子")
	}
	first := reflect.ValueOf(o.Block).Pointer()

	installTableExpandHook(o, s)
	if got := reflect.ValueOf(o.Block).Pointer(); got != first {
		t.Fatal("重复安装不应再包一层 Block 钩子")
	}

	// nil 选项或空状态直接跳过，不 panic。
	installTableExpandHook(nil, s)
	installTableExpandHook(&markdown.Options{}, nil)

	// 渲染表格本体的选项必须与原选项区分，并且不带 Block 钩子。
	body := tableBodyOptions(o)
	if body == o {
		t.Fatal("表格本体应使用独立的 Markdown 选项，避免整体拷贝内部的 sync.Map")
	}
	if body.Block != nil {
		t.Fatal("表格本体选项不能带 Block 钩子，否则会递归")
	}
	theme := &markdown.Theme{}
	o.Theme = theme
	called := false
	o.LinkMenu = func(*ui.Menu, string) { called = true }
	if got := tableBodyOptions(o); got != body {
		t.Fatal("同一份选项应复用同一份表格本体选项")
	}
	if body.Theme != theme {
		t.Fatal("表格本体选项应跟随原选项的主题")
	}
	if body.LinkMenu == nil {
		t.Fatal("表格本体选项应同步链接菜单钩子")
	}
	body.LinkMenu(nil, "")
	if !called {
		t.Fatal("表格本体选项应使用原链接菜单钩子")
	}
	if tableBodyOptions(nil) != nil {
		t.Fatal("空选项应返回 nil")
	}
}

// TestTablePopupKey 校验浮窗实例 key 按选项与源码偏移区分。
func TestTablePopupKey(t *testing.T) {
	a := &markdown.Options{}
	b := &markdown.Options{}
	if tablePopupKey(a, 12) == tablePopupKey(b, 12) {
		t.Fatal("不同选项的表格不应共用浮窗尺寸")
	}
	if tablePopupKey(a, 12) == tablePopupKey(a, 40) {
		t.Fatal("同一选项里不同位置的表格不应共用浮窗尺寸")
	}
}

// TestTableCellsMarkdown 校验表格复制成 GFM Markdown：竖线转义、换行折成空格、短行补齐到表头列数。
func TestTableCellsMarkdown(t *testing.T) {
	tests := []struct {
		name string
		rows [][]string
		want string
	}{
		{
			name: "空表格返回空串",
			rows: nil,
			want: "",
		},
		{
			name: "单列表格",
			rows: [][]string{{"名称"}, {"甲"}},
			want: "| 名称 |\n| --- |\n| 甲 |\n",
		},
		{
			name: "多列表格",
			rows: [][]string{{"名称", "说明"}, {"甲", "乙"}},
			want: "| 名称 | 说明 |\n| --- | --- |\n| 甲 | 乙 |\n",
		},
		{
			name: "短行补齐到最大列数",
			rows: [][]string{{"a", "b", "c"}, {"1"}},
			want: "| a | b | c |\n| --- | --- | --- |\n| 1 |  |  |\n",
		},
		{
			name: "单元格竖线转义",
			rows: [][]string{{"a|b", "c"}, {"d", "e|f"}},
			want: "| a\\|b | c |\n| --- | --- |\n| d | e\\|f |\n",
		},
		{
			name: "已转义的竖线再加一层反斜杠",
			rows: [][]string{{"a\\|b"}},
			want: "| a\\\\|b |\n| --- |\n",
		},
		{
			name: "单元格换行折成空格",
			rows: [][]string{{"第一行\n第二行", "多行\r\ntext"}},
			want: "| 第一行 第二行 | 多行 text |\n| --- | --- |\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tableCellsMarkdown(tt.rows); got != tt.want {
				t.Fatalf("tableCellsMarkdown(%v) = %q，期望 %q", tt.rows, got, tt.want)
			}
		})
	}
}

// TestTableCellsFromDocument 校验从解析结果重建单元格：表头与数据行都在，转义竖线还原成原文。
func TestTableCellsFromDocument(t *testing.T) {
	const src = `| 名称 | 说明 |
| --- | --- |
| 甲 | a\|b |
| 乙 |
`
	doc := parser.New(parser.Default)
	doc.Set(src)

	tableIdx := -1
	for i := 1; i < doc.Len(); i++ {
		if doc.Block(i).Kind == parser.Table {
			tableIdx = i
			break
		}
	}
	if tableIdx < 0 {
		t.Fatal("测试源码应解析出表格块")
	}

	rows := tableCellsFromDocument(doc, tableIdx)
	want := [][]string{{"名称", "说明"}, {"甲", "a|b"}, {"乙", ""}}
	if len(rows) != len(want) {
		t.Fatalf("tableCellsFromDocument 得到 %d 行，期望 %d 行：%v", len(rows), len(want), rows)
	}
	for i := range want {
		if len(rows[i]) != len(want[i]) {
			t.Fatalf("第 %d 行有 %d 个单元格，期望 %d：%v", i, len(rows[i]), len(want[i]), rows[i])
		}
		for j := range want[i] {
			if rows[i][j] != want[i][j] {
				t.Fatalf("第 %d 行第 %d 个单元格 = %q，期望 %q", i, j, rows[i][j], want[i][j])
			}
		}
	}

	// 重建回去时竖线要重新转义，才能仍然是一张合法的表格。
	md := tableCellsMarkdown(rows)
	if !strings.Contains(md, `| 甲 | a\|b |`) {
		t.Fatalf("重建的 Markdown 应保留转义竖线，得到：\n%s", md)
	}

	// 非表格块与空文档不产生单元格。
	paragraphIdx := -1
	for i := 1; i < doc.Len(); i++ {
		if doc.Block(i).Kind == parser.Paragraph {
			paragraphIdx = i
			break
		}
	}
	if paragraphIdx >= 0 && tableCellsFromDocument(doc, paragraphIdx) != nil {
		t.Fatal("段落块不应产生表格单元格")
	}
	if tableCellsFromDocument(nil, 0) != nil {
		t.Fatal("空文档不应产生表格单元格")
	}
}
