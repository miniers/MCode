package mcodeui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestStripSwarmFence_Closed 测试闭合的 swarm 代码块被完整剥离，多余空行被规整。
func TestStripSwarmFence_Closed(t *testing.T) {
	input := "派出 3 个子代理：\n\n```swarm\n[{\"task\":\"调研\",\"bee\":\"Birch\"}]\n```\n\n请等待结果汇报。"
	want := "派出 3 个子代理：\n\n请等待结果汇报。"

	got := stripSwarmFence(input)
	if got != want {
		t.Fatalf("stripSwarmFence() =\n%q\nwant:\n%q", got, want)
	}
}

// TestStripSwarmFence_Unclosed 测试流式输出末尾尚未闭合的 swarm 代码块被截断移除。
func TestStripSwarmFence_Unclosed(t *testing.T) {
	input := "派出 3 个子代理：\n\n```swarm\n[{\"task\":\"调研\",\"bee\":\"Birch\","
	want := "派出 3 个子代理："

	got := stripSwarmFence(input)
	if got != want {
		t.Fatalf("stripSwarmFence() =\n%q\nwant:\n%q", got, want)
	}
}

// TestStripSwarmFence_Multiple 测试多个 swarm 代码块全部被剥离。
func TestStripSwarmFence_Multiple(t *testing.T) {
	input := "第一阶段：\n```swarm\n[{\"bee\":\"Aspen\"}]\n```\n第二阶段：\n```swarm\n[{\"bee\":\"Birch\"}]\n```\n完成。"
	want := "第一阶段：\n\n第二阶段：\n\n完成。"

	got := stripSwarmFence(input)
	if got != want {
		t.Fatalf("stripSwarmFence() =\n%q\nwant:\n%q", got, want)
	}
}

// TestStripSwarmFence_KeepNormalCodeBlocks 测试不含编排围栏的普通代码块必须完整保留。
func TestStripSwarmFence_KeepNormalCodeBlocks(t *testing.T) {
	input := "示例文本：\n```go\nfunc main() {\n    println(\"hello\")\n}\n```\n结束。"
	want := "示例文本：\n```go\nfunc main() {\n    println(\"hello\")\n}\n```\n结束。"

	got := stripSwarmFence(input)
	if got != want {
		t.Fatalf("stripSwarmFence() =\n%q\nwant:\n%q", got, want)
	}
}

// TestStripSwarmFence_MixedCodeBlocks 测试普通代码块与 swarm 代码块混合时，仅剥离 swarm 代码块。
func TestStripSwarmFence_MixedCodeBlocks(t *testing.T) {
	input := "配置如下：\n```json\n{\"enabled\": true}\n```\n\n派发协作：\n```swarm\n[{\"bee\":\"Cove\"}]\n```\n\n执行脚本：\n```bash\necho ok\n```"
	want := "配置如下：\n```json\n{\"enabled\": true}\n```\n\n派发协作：\n\n执行脚本：\n```bash\necho ok\n```"

	got := stripSwarmFence(input)
	if got != want {
		t.Fatalf("stripSwarmFence() =\n%q\nwant:\n%q", got, want)
	}
}

// TestStripSwarmFence_InsideNormalCodeBlock 测试普通代码块内部包含的 ```swarm 文本不被误判。
func TestStripSwarmFence_InsideNormalCodeBlock(t *testing.T) {
	input := "```markdown\n这里展示格式：\n```swarm\n[{\"bee\":\"Aspen\"}]\n```\n```"
	want := "```markdown\n这里展示格式：\n```swarm\n[{\"bee\":\"Aspen\"}]\n```\n```"

	got := stripSwarmFence(input)
	if got != want {
		t.Fatalf("stripSwarmFence() =\n%q\nwant:\n%q", got, want)
	}
}

// TestStripSwarmFence_OnlySwarm 测试纯 swarm 代码块的剥离结果为空字符串。
func TestStripSwarmFence_OnlySwarm(t *testing.T) {
	input := "```swarm\n[{\"bee\":\"Birch\"}]\n```"
	want := ""

	got := stripSwarmFence(input)
	if got != want {
		t.Fatalf("stripSwarmFence() =\n%q\nwant:\n%q", got, want)
	}
}

// TestMatchSwarmMentions_LongestFirstAndNumbered 测试“Aspen 2”与“Aspen”并存时的最长优先匹配。
func TestMatchSwarmMentions_LongestFirstAndNumbered(t *testing.T) {
	bees := []SwarmBee{
		{Name: "Aspen", ThreadID: "tid-aspen-1"},
		{Name: "Aspen 2", ThreadID: "tid-aspen-2"},
	}

	text := "Aspen 2 修 bug，Aspen 做测试"
	matches := matchSwarmMentions(text, bees)

	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %d: %+v", len(matches), matches)
	}

	if matches[0].Bee.Name != "Aspen 2" || matches[0].Start != 0 || matches[0].End != 7 {
		t.Fatalf("first match expected 'Aspen 2' [0, 7], got %+v", matches[0])
	}

	aspenIndex := len("Aspen 2 修 bug，")
	if matches[1].Bee.Name != "Aspen" || matches[1].Start != aspenIndex {
		t.Fatalf("second match expected 'Aspen' at start %d, got %+v", aspenIndex, matches[1])
	}
}

// TestMatchSwarmMentions_WordBoundaries 测试词边界与数字后缀判断，防止误匹配更长英文单词。
func TestMatchSwarmMentions_WordBoundaries(t *testing.T) {
	bees := []SwarmBee{
		{Name: "Aspen", ThreadID: "tid-aspen"},
		{Name: "Birch", ThreadID: "tid-birch"},
	}

	text := "Thanks to Aspen, Aspen2 is skipped, Birch is valid, Birch9 is skipped."
	matches := matchSwarmMentions(text, bees)

	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %d: %+v", len(matches), matches)
	}

	if matches[0].Bee.Name != "Aspen" {
		t.Fatalf("expected Aspen, got %s", matches[0].Bee.Name)
	}
	if matches[1].Bee.Name != "Birch" {
		t.Fatalf("expected Birch, got %s", matches[1].Bee.Name)
	}
}

// TestMatchSwarmMentions_ChineseContext 测试中文标点与汉字紧邻环境下的匹配。
func TestMatchSwarmMentions_ChineseContext(t *testing.T) {
	bees := []SwarmBee{
		{Name: "Aspen", ThreadID: "h1"},
		{Name: "Birch", ThreadID: "w1"},
		{Name: "Cove", ThreadID: "a1"},
	}

	text := "派出 3 个子代理：Aspen 修 bee 汇报的捕获和父子关联，Birch 做时间线两侧的 bee 浮窗，Cove 修模型胶囊的提供者图标和发送按钮图标。"
	matches := matchSwarmMentions(text, bees)

	if len(matches) != 3 {
		t.Fatalf("expected 3 matches, got %d", len(matches))
	}
	names := []string{matches[0].Bee.Name, matches[1].Bee.Name, matches[2].Bee.Name}
	wantNames := []string{"Aspen", "Birch", "Cove"}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("matched names = %+v, want %+v", names, wantNames)
	}

	// 测试紧凑中文排版（无空格）
	compactText := "让Aspen和Birch分别处理"
	compactMatches := matchSwarmMentions(compactText, bees)
	if len(compactMatches) != 2 {
		t.Fatalf("compact text expected 2 matches, got %d", len(compactMatches))
	}
	if compactMatches[0].Bee.Name != "Aspen" || compactMatches[1].Bee.Name != "Birch" {
		t.Fatalf("compact matches unexpected: %+v", compactMatches)
	}
}

// TestMatchSwarmMentions_Empty 测试空参数与无匹配情况。
func TestMatchSwarmMentions_Empty(t *testing.T) {
	if matches := matchSwarmMentions("", nil); len(matches) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(matches))
	}

	bees := []SwarmBee{{Name: "Aspen"}}
	if matches := matchSwarmMentions("普通消息没有任何 bee 提及", bees); len(matches) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(matches))
	}
}

// TestStripSwarmFence_LegacyFence 验证旧围栏 ```hydra 仍被识别并剥离（新围栏是 ```swarm）。
func TestStripSwarmFence_LegacyFence(t *testing.T) {
	input := "派出 2 个子代理：\n\n```hydra\n[{\"task\":\"调研\",\"bee\":\"Birch\"}]\n```\n\n请等待结果汇报。"
	want := "派出 2 个子代理：\n\n请等待结果汇报。"

	if got := stripSwarmFence(input); got != want {
		t.Fatalf("stripSwarmFence() =\n%q\nwant:\n%q", got, want)
	}
}

// TestSwarmFenceDispatchInfo_LegacyAndNewFence 验证新旧围栏都能被派单块计数识别。
func TestSwarmFenceDispatchInfo_LegacyAndNewFence(t *testing.T) {
	closedNew := "计划：\n```swarm\n[{\"task\":\"A\",\"prompt\":\"a\"},{\"task\":\"B\",\"prompt\":\"b\"}]\n```\n"
	if open, closed, count := swarmFenceDispatchInfo(closedNew); open || !closed || count != 2 {
		t.Fatalf("swarm 围栏闭合计数错误: open=%v closed=%v count=%d", open, closed, count)
	}

	closedLegacy := "计划：\n```hydra\n[{\"task\":\"A\",\"prompt\":\"a\"}]\n```\n"
	if open, closed, count := swarmFenceDispatchInfo(closedLegacy); open || !closed || count != 1 {
		t.Fatalf("swarm 围栏闭合计数错误: open=%v closed=%v count=%d", open, closed, count)
	}

	// 旧围栏的答案块同样属于编排围栏，不能被当成普通代码块。
	answer := "```hydra-answer\n{\"bee\":\"h1\",\"request\":\"r1\",\"skip\":true}\n```\n"
	if got := stripSwarmFence(answer); got != "" {
		t.Fatalf("旧围栏答案块应被剥离, got %q", got)
	}
}

// TestMentionTableMarkdown 验证表格和前后正文各自的渲染方式。
func TestMentionTableMarkdown(t *testing.T) {
	bees := []SwarmBee{{Name: "Aspen", ThreadID: "tid-aspen"}}
	tests := []struct {
		name  string
		input string
		want  []mentionBlock
	}{
		{
			name:  "带前导竖线的表格",
			input: "| 名称 | 说明 |\n| --- | --- |\n| Aspen | 完成 |",
			want:  []mentionBlock{{kind: mentionBlockTable, text: "| 名称 | 说明 |\n| --- | --- |\n| Aspen | 完成 |", hasBee: true}},
		},
		{
			name:  "无前导竖线的表格和前后正文",
			input: "Aspen 开始。\n名称 | 说明\n--- | ---\nAspen | 完成\n\nAspen 结束。",
			want: []mentionBlock{
				{kind: mentionBlockProse, text: "Aspen 开始。", hasBee: true},
				{kind: mentionBlockTable, text: "名称 | 说明\n--- | ---\nAspen | 完成", hasBee: true},
				{kind: mentionBlockProse, text: "Aspen 结束。", hasBee: true},
			},
		},
		{
			name:  "含竖线的普通正文",
			input: "Aspen 选择 A | B。\n继续处理。",
			want:  []mentionBlock{{kind: mentionBlockProse, text: "Aspen 选择 A | B。\n继续处理。", hasBee: true}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitMentionBlocks(tt.input, bees)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("splitMentionBlocks() = %+v, want %+v", got, tt.want)
			}
			for _, block := range got {
				if markdown := mentionBlockUsesMarkdown(block); markdown != (block.kind == mentionBlockTable) {
					t.Errorf("mentionBlockUsesMarkdown(%+v) = %v", block, markdown)
				}
			}
		})
	}
}

// TestSplitMentionBlocks 表驱动验证正文分块：围栏代码块内部空行不拆、连续表格行不拆、
// 其余按空行分块，并判定每块是否含已派出的 bee 名字。
func TestSplitMentionBlocks(t *testing.T) {
	bees := []SwarmBee{{Name: "Aspen"}, {Name: "Heath"}}

	tests := []struct {
		name  string
		input string
		want  []mentionBlock
	}{
		{
			name:  "空行分块并判定 bee 提及",
			input: "先说明现状。\n\n## 核验率变化\n\nAspen 修好了捕获。",
			want: []mentionBlock{
				{kind: mentionBlockProse, text: "先说明现状。", hasBee: false},
				{kind: mentionBlockProse, text: "## 核验率变化", hasBee: false},
				{kind: mentionBlockProse, text: "Aspen 修好了捕获。", hasBee: true},
			},
		},
		{
			name:  "围栏代码块内部空行不拆",
			input: "示例：\n\n```go\nfunc a() {}\n\nfunc b() {}\n```\n\nAspen 完成。",
			want: []mentionBlock{
				{kind: mentionBlockProse, text: "示例：", hasBee: false},
				{kind: mentionBlockCode, text: "```go\nfunc a() {}\n\nfunc b() {}\n```", hasBee: false},
				{kind: mentionBlockProse, text: "Aspen 完成。", hasBee: true},
			},
		},
		{
			name:  "未闭合的围栏代码块（流式）保持成块",
			input: "说明：\n\n```go\nfunc a() {\n\n",
			want: []mentionBlock{
				{kind: mentionBlockProse, text: "说明：", hasBee: false},
				{kind: mentionBlockCode, text: "```go\nfunc a() {", hasBee: false},
			},
		},
		{
			name:  "连续表格行不拆",
			input: "| bee | 状态 |\n| --- | --- |\n| Aspen | 完成 |\n\n结尾说明。",
			want: []mentionBlock{
				{kind: mentionBlockTable, text: "| bee | 状态 |\n| --- | --- |\n| Aspen | 完成 |", hasBee: true},
				{kind: mentionBlockProse, text: "结尾说明。", hasBee: false},
			},
		},
		{
			name:  "表格行之间的空行不打断表格",
			input: "| a | b |\n\n| c | d |",
			want: []mentionBlock{
				{kind: mentionBlockTable, text: "| a | b |\n| c | d |", hasBee: false},
			},
		},
		{
			name:  "多行正文按空行分块",
			input: "## 标题\n\n- 第一项\n- 第二项",
			want: []mentionBlock{
				{kind: mentionBlockProse, text: "## 标题", hasBee: false},
				{kind: mentionBlockProse, text: "- 第一项\n- 第二项", hasBee: false},
			},
		},
		{
			name:  "纯空白正文没有块",
			input: "\n\n  \n",
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitMentionBlocks(tt.input, bees)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("splitMentionBlocks() =\n%+v\nwant:\n%+v", got, tt.want)
			}
		})
	}
}

// mentionIconPlaceholder 是 bee 头像在图里占位的行内对象字符（等宽 12pt），段落文字里会带上它。
const mentionIconPlaceholder = "\uFFFC"

// mentionParagraphLabel 返回加了头像占位之后的段落文字：每个 bee 名字前都有一个行内对象占位，
// 所以段落的文字是原文在名字前插入 U+FFFC。
func mentionParagraphLabel(line string, bees []SwarmBee) string {
	var b strings.Builder
	last := 0
	for _, m := range matchSwarmMentions(line, bees) {
		b.WriteString(line[last:m.Start])
		b.WriteString(mentionIconPlaceholder)
		last = m.Start
	}
	b.WriteString(line[last:])
	return b.String()
}

// TestRenderMentionParagraphInline 验证 bee 名字是段落里的行内文字：名字出现在长句中间时，
// 名字与前后文字属于同一段落、从同一行开始，而不是自成一行、把后面的正文整块挤到下一行行首。
// 名字前的头像是一个 12pt 的行内对象占位，紧贴在名字左侧、与名字同处一行，并落在段落范围内。
func TestRenderMentionParagraphInline(t *testing.T) {
	bees := []SwarmBee{
		{Name: "Aspen", ThreadID: "tid-aspen"},
		{Name: "Heath", ThreadID: "tid-heath"},
	}
	line := "先把 Aspen 修好的捕获逻辑合进来，再让 Heath 复核时间线两侧的浮窗，最后统一收口。"
	paraText := mentionParagraphLabel(line, bees)
	iconSize := swarmMentionIconSize()
	// 与 renderMentionLine 一致：一行只建一个段落，普通文本与 bee 名字都作为它的行内子元素。
	view := func(c *ui.Context) {
		ui.Column(c.Key("mention-inline-shell")).Fill().Padding(10).Children(func() {
			pieces := splitLineByMentions(line, matchSwarmMentions(line, bees))
			renderMentionParagraph(c, nil, "mention-inline", pieces, scaledFont(13), 0)
		})
	}

	// 窗口够宽，即使「文字大小」调到最大档也不折行。
	wide := ui.NewTester(view, 1600, 200)
	if !wide.HasText(paraText) {
		t.Fatalf("名字与前后文字应属于同一段落, texts = %q", wide.Texts())
	}
	para, ok := wide.Find(paraText)
	if !ok {
		t.Fatalf("找不到段落, texts = %q", wide.Texts())
	}
	name, ok := wide.Find("Aspen")
	if !ok {
		t.Fatalf("找不到行内的 bee 名字, texts = %q", wide.Texts())
	}
	icon, ok := wide.Find(mentionIconPlaceholder)
	if !ok {
		t.Fatalf("找不到名字前的头像占位, texts = %q", wide.Texts())
	}
	// 宽窗口下整行不折行：段落单行高、名字的盒子在段落里且只占自己的字宽。
	if para.H >= name.H*2 {
		t.Fatalf("段落高 %v，不是单行（名字高 %v）", para.H, name.H)
	}
	if name.X <= para.X || name.W <= 0 || name.X+name.W >= para.X+para.W {
		t.Fatalf("名字不在行内: 名字 %+v, 段落 %+v", name, para)
	}
	if name.Y < para.Y || name.Y+name.H > para.Y+para.H {
		t.Fatalf("名字不在段落内: 名字 %+v, 段落 %+v", name, para)
	}
	// 头像占位是 12pt 的行内对象，紧贴在名字左侧、与名字同一行。
	if icon.W < iconSize-0.5 || icon.W > iconSize+0.5 {
		t.Fatalf("头像占位宽 %v，应为 %v", icon.W, iconSize)
	}
	if icon.X+icon.W > name.X+0.5 {
		t.Fatalf("头像不在名字左侧: 头像 %+v, 名字 %+v", icon, name)
	}
	if icon.Y < para.Y || icon.Y+icon.H > para.Y+para.H {
		t.Fatalf("头像占位不在段落内: 头像 %+v, 段落 %+v", icon, para)
	}
	if d := icon.Y - name.Y; d > 0.5 || d < -0.5 {
		t.Fatalf("头像与名字不在同一行: 头像 %+v, 名字 %+v", icon, name)
	}

	// 窄窗口下整段一起折行：仍是同一个段落，名字与头像仍落在段落范围内、占位宽度不变。
	narrow := ui.NewTester(view, 160, 400)
	if !narrow.HasText(paraText) {
		t.Fatalf("折行后仍应是同一段落, texts = %q", narrow.Texts())
	}
	narrowPara, ok := narrow.Find(paraText)
	if !ok {
		t.Fatalf("窄窗口下找不到段落, texts = %q", narrow.Texts())
	}
	heath, ok := narrow.Find("Heath")
	if !ok {
		t.Fatalf("窄窗口下找不到行内的 bee 名字, texts = %q", narrow.Texts())
	}
	if heath.X < narrowPara.X || heath.X+heath.W > narrowPara.X+narrowPara.W+1 || heath.Y > narrowPara.Y+narrowPara.H {
		t.Fatalf("折行后名字越出段落: 名字 %+v, 段落 %+v", heath, narrowPara)
	}
	narrowIcon, ok := narrow.Find(mentionIconPlaceholder)
	if !ok {
		t.Fatalf("窄窗口下找不到头像占位, texts = %q", narrow.Texts())
	}
	if narrowIcon.W < iconSize-0.5 || narrowIcon.W > iconSize+0.5 {
		t.Fatalf("折行后头像占位宽 %v，应为 %v", narrowIcon.W, iconSize)
	}
	if narrowIcon.X < narrowPara.X || narrowIcon.X+narrowIcon.W > narrowPara.X+narrowPara.W+1 {
		t.Fatalf("折行后头像占位越出段落: 头像 %+v, 段落 %+v", narrowIcon, narrowPara)
	}
	if narrowIcon.Y < narrowPara.Y || narrowIcon.Y+narrowIcon.H > narrowPara.Y+narrowPara.H {
		t.Fatalf("折行后头像占位不在段落内: 头像 %+v, 段落 %+v", narrowIcon, narrowPara)
	}
}

// TestParseMentionLineStyle 表驱动验证行首 Markdown 标记（标题井号、无序列表圆点、表格行）的剥离。
func TestParseMentionLineStyle(t *testing.T) {
	tests := []struct {
		name string
		line string
		want mentionLineStyle
	}{
		{name: "二级标题", line: "## 核验率变化", want: mentionLineStyle{heading: 2, content: "核验率变化"}},
		{name: "一级标题", line: "# Aspen 汇报", want: mentionLineStyle{heading: 1, content: "Aspen 汇报"}},
		{name: "缩进的标题", line: "   ### Heath", want: mentionLineStyle{heading: 3, content: "Heath"}},
		{name: "井号后无空格不算标题", line: "#hashtag 不算标题", want: mentionLineStyle{content: "#hashtag 不算标题"}},
		{name: "列表项", line: "- **Aspen 修好了**", want: mentionLineStyle{list: true, content: "**Aspen 修好了**"}},
		{name: "星号列表项", line: "* Heath 待复核", want: mentionLineStyle{list: true, content: "Heath 待复核"}},
		{name: "加号列表项", line: "+ Heath 已合并", want: mentionLineStyle{list: true, content: "Heath 已合并"}},
		{name: "粗体段落不算列表", line: "**Aspen** 已通过", want: mentionLineStyle{content: "**Aspen** 已通过"}},
		{name: "表格行保持原样", line: "| Aspen | 完成 |", want: mentionLineStyle{content: "| Aspen | 完成 |"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseMentionLineStyle(tt.line); got != tt.want {
				t.Fatalf("parseMentionLineStyle(%q) = %+v, want %+v", tt.line, got, tt.want)
			}
		})
	}
}

// TestMatchSwarmMentions_ChineseNameBoundary 测试汉字名字的词边界：
// 不能嵌在更长的汉字词组里，但紧邻标点、空格或句首句尾时仍算提及。
func TestMatchSwarmMentions_ChineseNameBoundary(t *testing.T) {
	bees := []SwarmBee{{Name: "测试", ThreadID: "tid-test"}}

	for _, text := range []string{"补测试用例", "重试测试"} {
		if matches := matchSwarmMentions(text, bees); len(matches) != 0 {
			t.Fatalf("%q expected 0 matches, got %+v", text, matches)
		}
	}

	for _, text := range []string{"测试：已完成", "派 测试 去跑", "（测试）"} {
		matches := matchSwarmMentions(text, bees)
		if len(matches) != 1 || matches[0].Bee.Name != "测试" {
			t.Fatalf("%q expected 1 match for 测试, got %+v", text, matches)
		}
	}
}

// TestMatchSwarmMentions_AspenBoundary 测试 ASCII 名字在中文正文中的边界不受汉字规则影响。
func TestMatchSwarmMentions_AspenBoundary(t *testing.T) {
	bees := []SwarmBee{
		{Name: "Aspen", ThreadID: "tid-aspen"},
		{Name: "Aspen 2", ThreadID: "tid-aspen-2"},
	}

	for _, text := range []string{"Aspen 已汇报", "派Aspen去"} {
		matches := matchSwarmMentions(text, bees)
		if len(matches) != 1 || matches[0].Bee.Name != "Aspen" {
			t.Fatalf("%q expected 1 Aspen match, got %+v", text, matches)
		}
	}

	if matches := matchSwarmMentions("Aspens", bees); len(matches) != 0 {
		t.Fatalf("Aspens expected 0 matches, got %+v", matches)
	}

	if matches := matchSwarmMentions("派 Aspen 2 去", bees); len(matches) != 1 || matches[0].Bee.Name != "Aspen 2" {
		t.Fatalf("expected Aspen 2 to win, got %+v", matches)
	}
}
