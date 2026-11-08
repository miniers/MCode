package mcodeui

import "testing"

func testBees(n int) []SwarmBee {
	names := []string{"Aspen", "Birch", "Cove", "Dune"}
	bees := make([]SwarmBee, 0, n)
	for i := 0; i < n; i++ {
		bees = append(bees, SwarmBee{ThreadID: "h" + names[i], Name: names[i], Status: "running"})
	}
	return bees
}

func TestPanelLayoutWidthAndSides(t *testing.T) {
	wide := panelLayout{paneW: 1400, paneH: 900, composerAreaH: 100, stackDepth: 1}
	if got := wide.width(); got != panelDefaultWidth {
		t.Fatalf("width = %v, want %v", got, panelDefaultWidth)
	}
	if !wide.sitsBesideComposer() || !wide.fitsBothSides() {
		t.Fatalf("1400 宽主区域应能与聊天框并排，且两侧都放得下")
	}
	if got := wide.composerReserve(); got != panelDefaultWidth+panelSideGap {
		t.Fatalf("composerReserve = %v", got)
	}

	narrow := panelLayout{paneW: 700, paneH: 900, composerAreaH: 100, stackDepth: 1}
	if narrow.sitsBesideComposer() {
		t.Fatalf("700 宽主区域放不下并排的聊天框")
	}
	if narrow.composerReserve() != 0 {
		t.Fatalf("不并排时不应让出聊天框宽度")
	}

	// 用户拖出的宽度被收进主区域允许的范围。
	pref := panelLayout{paneW: 1400, paneH: 900, stackDepth: 1, prefW: 2000}
	if got := pref.width(); got != 1400-2*panelSideMargin {
		t.Fatalf("width 应被收进边距: %v", got)
	}
	pref.prefW = 100
	if got := pref.width(); got != panelMinWidth {
		t.Fatalf("width 不应小于最小宽度: %v", got)
	}
}

func TestPanelLayoutHeight(t *testing.T) {
	l := panelLayout{paneW: 1400, paneH: 900, composerAreaH: 100, stackDepth: 1}
	if got := l.height(); got != 450 {
		t.Fatalf("对话模式默认高度 = %v, want 450（主区域高度的一半）", got)
	}
	// 拖拽设定的高度生效，并受可用高度限制。
	l.prefH = 600
	if got := l.height(); got != 600 {
		t.Fatalf("height = %v, want 600", got)
	}
	l.prefH = 5000
	if got := l.height(); got != l.verticalRoom() {
		t.Fatalf("height 应被限制为可用高度 %v, got %v", l.verticalRoom(), got)
	}
	// 叠放两个面板时平分可用高度。
	l.prefH = 600
	l.stackDepth = 2
	want := (l.verticalRoom() - panelGap) / 2
	if got := l.height(); got != want {
		t.Fatalf("叠放深度 2 的高度 = %v, want %v", got, want)
	}
}

func TestPanelLayoutFitted(t *testing.T) {
	l := panelLayout{paneW: 1400, paneH: 900, composerAreaH: 100, stackDepth: 1}
	got := l.fitted(panelSize{w: 100, h: 50})
	if got.w != panelMinWidth || got.h != panelMinHeight {
		t.Fatalf("fitted 小尺寸 = %+v", got)
	}
	got = l.fitted(panelSize{w: 5000, h: 5000})
	// 宽到放不下聊天框并排时面板改停到聊天框上方，可用高度随之变矮。
	if got.w != 1400-2*panelSideMargin || got.h != 900-panelTopInset-(100+panelGap) {
		t.Fatalf("fitted 大尺寸 = %+v", got)
	}
}

func TestPanelDockedOriginBottomTrailing(t *testing.T) {
	l := panelLayout{paneW: 1400, paneH: 900, composerAreaH: 100, stackDepth: 1}
	reserve := panelReserve{trailing: l.composerReserve()}
	got := l.dockedOrigin(cornerBottomTrailing, reserve)
	// 内容列在 0..976 里居中，宽 768：左缘 104，面板贴在它右侧 panelSideGap。
	if got.x != 104+768+panelSideGap {
		t.Fatalf("x = %v", got.x)
	}
	if got.y != 900-panelBottomMargin-l.height() {
		t.Fatalf("y = %v，面板应与聊天框底边对齐", got.y)
	}
	top := l.dockedOrigin(cornerTopTrailing, reserve)
	if top.y != panelTopInset {
		t.Fatalf("上角 y = %v", top.y)
	}

	// 窄主区域：面板停在聊天框上方的同侧边缘。
	narrow := panelLayout{paneW: 700, paneH: 900, composerAreaH: 120, stackDepth: 1}
	n := narrow.dockedOrigin(cornerBottomTrailing, panelReserve{})
	if n.x != 700-panelSideMargin-panelDefaultWidth {
		t.Fatalf("窄屏 x = %v", n.x)
	}
	if n.y != 900-(120+panelGap)-narrow.height() {
		t.Fatalf("窄屏 y = %v，应停在聊天框上方", n.y)
	}
}

// TestPanelDockedOriginClearsContentColumn 验证并排停靠的面板贴的是时间线内容列的外缘，
// 而不是固定 820 的输入框：内容列随「内容宽度」设置变宽时，面板要跟着往外让开正文。
func TestPanelDockedOriginClearsContentColumn(t *testing.T) {
	const (
		paneW    float32 = 1600
		reserveW float32 = 424
	)
	// contentColumn 按 dockedOrigin 的口径算出内容列左缘与宽度，用来检查面板没压到正文。
	contentColumn := func(l panelLayout, reserve panelReserve) (left, width float32) {
		row := l.paneW - reserve.leading - reserve.trailing
		width = min(l.contentMaxWidth(), row-2*panelSideMargin)
		return reserve.leading + (row-width)/2, width
	}

	cases := []struct {
		name        string
		contentMaxW float32
		wantX       float32
	}{
		{name: "未指定按标准档", contentMaxW: 0, wantX: 996},
		{name: "标准档", contentMaxW: 768, wantX: 996},
		{name: "宽档", contentMaxW: 1120, wantX: 1172},
		{name: "按比例档宽于聊天列", contentMaxW: 1400, wantX: 1180},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := panelLayout{paneW: paneW, paneH: 900, composerAreaH: 100, stackDepth: 1, contentMaxW: tc.contentMaxW}
			reserve := panelReserve{trailing: reserveW}
			got := l.dockedOrigin(cornerBottomTrailing, reserve)
			if got.x != tc.wantX {
				t.Fatalf("x = %v, want %v", got.x, tc.wantX)
			}
			left, width := contentColumn(l, reserve)
			if got.x < left+width {
				t.Fatalf("面板左缘 %v 压住了内容列右缘 %v", got.x, left+width)
			}
			if right := got.x + panelDefaultWidth; right > paneW-panelSideMargin {
				t.Fatalf("面板右缘 %v 超出主区域边距 %v", right, paneW-panelSideMargin)
			}
		})
	}

	// 对称：面板停在左侧时同样贴内容列左缘，不压正文。
	l := panelLayout{paneW: paneW, paneH: 900, composerAreaH: 100, stackDepth: 1, contentMaxW: 1120}
	reserve := panelReserve{leading: reserveW}
	got := l.dockedOrigin(cornerBottomLeading, reserve)
	if got.x != 28 {
		t.Fatalf("左侧 x = %v, want 28", got.x)
	}
	left, _ := contentColumn(l, reserve)
	if right := got.x + panelDefaultWidth; right > left {
		t.Fatalf("面板右缘 %v 压住了内容列左缘 %v", right, left)
	}
}

func TestComputeSwarmSceneSingleTeamPanelDocksBottomTrailing(t *testing.T) {
	sc := computeSwarmScene(sceneInput{
		paneW: 1400, paneH: 900, composerAreaH: 100,
		bees: testBees(2), showsTeam: true, teamDock: cornerBottomTrailing,
	})
	slot, ok := sc.slots[teamPanelID]
	if !ok {
		t.Fatal("应有团队面板")
	}
	if slot.corner != cornerBottomTrailing || slot.below != 0 {
		t.Fatalf("团队面板默认应在右下角，got %+v", slot)
	}
	if len(sc.teamBees) != 2 || len(sc.autoBees) != 0 {
		t.Fatalf("聚合模式下所有子代理都在团队面板里: team=%d auto=%d", len(sc.teamBees), len(sc.autoBees))
	}
	if sc.reserve.leading != 0 || sc.reserve.trailing != panelDefaultWidth+panelSideGap {
		t.Fatalf("reserve = %+v，只应从右侧让出面板宽度", sc.reserve)
	}
}

func TestComputeSwarmSceneNoPanelWhenDismissedOrNoBees(t *testing.T) {
	sc := computeSwarmScene(sceneInput{paneW: 1400, paneH: 900, bees: testBees(2), teamDock: cornerBottomTrailing})
	if sc.has() || sc.reserve != (panelReserve{}) {
		t.Fatalf("团队面板被关闭时不应有面板或让出空间: %+v", sc)
	}
	sc = computeSwarmScene(sceneInput{paneW: 1400, paneH: 900, showsTeam: true, teamDock: cornerBottomTrailing})
	if sc.has() {
		t.Fatalf("没有子代理时不应有面板")
	}
}

func TestComputeSwarmSceneAutoPopsEachBeeGetsPanel(t *testing.T) {
	bees := testBees(3)
	sc := computeSwarmScene(sceneInput{
		paneW: 1400, paneH: 900, composerAreaH: 100,
		bees: bees, autoPops: true, showsTeam: true, teamDock: cornerBottomTrailing,
	})
	if len(sc.teamBees) != 1 || sc.teamBees[0].ThreadID != bees[0].ThreadID {
		t.Fatalf("团队面板只留第一个子代理: %+v", sc.teamBees)
	}
	if len(sc.autoBees) != 2 {
		t.Fatalf("其余两个子代理应各有面板: %d", len(sc.autoBees))
	}
	for _, h := range bees[1:] {
		slot, ok := sc.slots[autoPanelID(h.ThreadID)]
		if !ok {
			t.Fatalf("%s 缺少面板", h.Name)
		}
		if slot.corner.side() != panelSideLeading || slot.corner.isTop() {
			t.Fatalf("%s 应先填团队面板对侧的下角: %+v", h.Name, slot)
		}
	}
	// 两侧都让出空间，聊天列夹在中间。
	if sc.reserve.leading == 0 || sc.reserve.trailing == 0 {
		t.Fatalf("两侧都有面板，reserve = %+v", sc.reserve)
	}
	// 同一侧的两个面板从底部向上排：第一个在下角槽位，第二个在上一个槽位。
	first, _ := sc.restOrigin(autoPanelID(bees[1].ThreadID))
	second, _ := sc.restOrigin(autoPanelID(bees[2].ThreadID))
	if !(second.y < first.y) {
		t.Fatalf("第二个面板应在第一个之上: first=%v second=%v", first, second)
	}
	if first.y+sc.layout.height() > sc.layout.paneH {
		t.Fatalf("面板不应超出底边: %v", first)
	}
	if second.y+sc.layout.height() > first.y {
		t.Fatalf("两个面板不应重叠: first=%v second=%v h=%v", first, second, sc.layout.height())
	}
}

// TestComputeSwarmSceneFourthPanelGoesToTeamSideTop 验证第四个面板不再在左侧叠第三个，
// 而是放到团队这一侧的对角（团队在右下时就是右上）。
func TestComputeSwarmSceneFourthPanelGoesToTeamSideTop(t *testing.T) {
	bees := testBees(4)
	// paneH 700 时每侧自然槽位是 2：左侧放两个，第四个只能放团队这一侧。
	sc := computeSwarmScene(sceneInput{
		paneW: 1400, paneH: 700, composerAreaH: 100,
		bees: bees, autoPops: true, showsTeam: true, teamDock: cornerBottomTrailing,
	})
	for _, h := range bees[1:3] {
		slot, ok := sc.slots[autoPanelID(h.ThreadID)]
		if !ok {
			t.Fatalf("%s 缺少面板", h.Name)
		}
		if slot.corner.side() != panelSideLeading {
			t.Fatalf("%s 应在团队面板对侧: %+v", h.Name, slot)
		}
	}
	fourth, ok := sc.slots[autoPanelID(bees[3].ThreadID)]
	if !ok {
		t.Fatalf("%s 缺少面板", bees[3].Name)
	}
	if fourth.corner != cornerTopTrailing {
		t.Fatalf("第四个面板应放到团队这一侧的顶部: %+v", fourth)
	}
	autoOrigin, _ := sc.restOrigin(autoPanelID(bees[3].ThreadID))
	teamOrigin, _ := sc.restOrigin(teamPanelID)
	if !(autoOrigin.y < teamOrigin.y) {
		t.Fatalf("上面的面板应在团队面板之上: auto=%v team=%v", autoOrigin, teamOrigin)
	}
	if autoOrigin.y+sc.layout.height() > teamOrigin.y {
		t.Fatalf("两个面板不应重叠: auto=%v team=%v h=%v", autoOrigin, teamOrigin, sc.layout.height())
	}
}

func TestComputeSwarmSceneAutoHeldBeeStaysInTeamPanel(t *testing.T) {
	bees := testBees(2)
	sc := computeSwarmScene(sceneInput{
		paneW: 1400, paneH: 900, composerAreaH: 100,
		bees: bees, autoPops: true, showsTeam: true, teamDock: cornerBottomTrailing,
		autoHeld: map[string]bool{bees[1].ThreadID: true},
	})
	if len(sc.autoBees) != 0 || len(sc.teamBees) != 2 {
		t.Fatalf("被送回团队面板的子代理不再单独弹出: team=%d auto=%d", len(sc.teamBees), len(sc.autoBees))
	}
}

func TestComputeSwarmSceneNarrowPaneKeepsEverythingOnOneSide(t *testing.T) {
	bees := testBees(3)
	sc := computeSwarmScene(sceneInput{
		paneW: 800, paneH: 900, composerAreaH: 100,
		bees: bees, autoPops: true, showsTeam: true, teamDock: cornerBottomTrailing,
	})
	if sc.reserve != (panelReserve{}) {
		t.Fatalf("窄屏时面板停在聊天框上方，不应让出两侧宽度: %+v", sc.reserve)
	}
	for id, slot := range sc.slots {
		if slot.corner.side() != panelSideTrailing {
			t.Fatalf("窄屏下 %s 应与团队面板同侧: %+v", id, slot)
		}
	}
}

func TestPanelDockCornerKeepsWithinSlack(t *testing.T) {
	sc := computeSwarmScene(sceneInput{
		paneW: 1400, paneH: 900, composerAreaH: 100,
		bees: testBees(1), showsTeam: true, teamDock: cornerBottomTrailing,
	})
	// 把面板拖到左上方：换到左上角。
	got := sc.layout.dockCorner(panelPoint{0, 0}, cornerBottomTrailing, sc.docks)
	if got != cornerTopLeading {
		t.Fatalf("拖到左上应换到左上角，got %v", got)
	}
	// 中心恰在中线附近：保持原角。
	size := sc.layout.size()
	mid := panelPoint{sc.layout.paneW/2 - size.w/2 + 10, (sc.docks.topLeading.y+sc.docks.bottomLeading.y+size.h)/2 - size.h/2}
	if got := sc.layout.dockCorner(mid, cornerBottomTrailing, sc.docks); got.side() != panelSideTrailing {
		t.Fatalf("中线附近应保持原来所在侧，got %v", got)
	}
}

func TestReorderedPanelsSwapsOnFullSide(t *testing.T) {
	bees := testBees(3)
	in := sceneInput{
		paneW: 1400, paneH: 700, composerAreaH: 100,
		bees: bees, autoPops: true, showsTeam: true, teamDock: cornerBottomTrailing,
	}
	sc := computeSwarmScene(in)
	a, b := autoPanelID(bees[1].ThreadID), autoPanelID(bees[2].ThreadID)
	// 左侧共两个槽，a 在底部（槽 1）、b 在顶部（槽 0）。把 a 拖到顶部槽：与 b 互换。
	corners, order := reorderedPanels(sc, a, panelPoint{x: sc.docks.topLeading.x, y: sc.docks.topLeading.y}, cornerBottomLeading, nil)
	if !corners[a].isTop() || corners[b].isTop() {
		t.Fatalf("互换后 a 在上角、b 在下角: a=%v b=%v", corners[a], corners[b])
	}
	if len(order) != 2 || order[0] != a || order[1] != b {
		t.Fatalf("堆叠顺序应是自上而下 [a b]: %v", order)
	}
}

// testSceneWithAutoPanels 造一个把子代理分摊到两侧面板上的场景：第一个留在团队面板，其余自动弹出。
func testSceneWithAutoPanels(bees int, in sceneInput) swarmScene {
	in.bees = testBees(bees)
	in.autoPops = true
	in.showsTeam = true
	in.teamDock = cornerBottomTrailing
	return computeSwarmScene(in)
}

// TestPanelStackHeightSplitsBySideCount 验证面板最高只能到“该侧可用高度按实际面板数平分”，
// 而不是按槽位间距推算出的叠放深度。
func TestPanelStackHeightSplitsBySideCount(t *testing.T) {
	// 宽主区域每侧 3 个槽位：团队面板占右下，两个子代理面板占上方相邻的两个槽位。
	wide := sceneInput{paneW: 1400, paneH: 1000, composerAreaH: 100}
	// 窄主区域只能放在同一侧：3 个面板排满该侧。
	narrow := sceneInput{paneW: 800, paneH: 1200, composerAreaH: 100}
	cases := []struct {
		name  string
		in    sceneInput
		bees  int
		depth int
	}{
		{"单面板", wide, 1, 1},
		{"同侧2面板占相邻槽位", wide, 3, 2},
		{"同侧3面板", narrow, 3, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sc := testSceneWithAutoPanels(c.bees, c.in)
			if sc.layout.stackDepth != c.depth {
				t.Fatalf("stackDepth = %d, want %d", sc.layout.stackDepth, c.depth)
			}
			if c.depth == 1 {
				// 单面板：静止位置仍是原来的下角停靠点。
				if rest, _ := sc.restOrigin(teamPanelID); rest != sc.docks.bottomTrailing {
					t.Fatalf("单面板位置 = %v, want %v", rest, sc.docks.bottomTrailing)
				}
			}
			// 拖到最高：高度等于可用高度按面板数平分，拖拽上限也是同一个值。
			maxed := testSceneWithAutoPanels(c.bees, withPrefHeight(c.in, 5000))
			want := (maxed.layout.verticalRoom() - float32(c.depth-1)*panelGap) / float32(c.depth)
			if got := maxed.layout.height(); got != want {
				t.Fatalf("最大高度 = %v, want %v", got, want)
			}
			if got := maxed.layout.fitted(panelSize{w: maxed.layout.width(), h: 5000}).h; got != want {
				t.Fatalf("拖拽上限 = %v, want %v", got, want)
			}
		})
	}
}

// withPrefHeight 返回把拖拽设定高度换成 h 的场景输入。
func withPrefHeight(in sceneInput, h float32) sceneInput {
	in.prefH = h
	return in
}

// TestPanelStackStaysAdjacentAtMaxHeight 验证同侧两个面板拖到最高时首尾相接、互不重叠，
// 且都留在停靠范围内。
func TestPanelStackStaysAdjacentAtMaxHeight(t *testing.T) {
	sc := testSceneWithAutoPanels(3, withPrefHeight(sceneInput{paneW: 1400, paneH: 1000, composerAreaH: 100}, 5000))
	h := sc.layout.height()
	top, bottom := sc.docks.topLeading.y, sc.docks.bottomLeading.y
	// 左侧两个面板：名次 0 在上、1 在下。
	var origins [2]panelPoint
	for id, slot := range sc.slots {
		if slot.corner.side() != panelSideLeading {
			continue
		}
		if slot.sideCount != 2 || slot.fromTop < 0 || slot.fromTop > 1 {
			t.Fatalf("%s 的名次不对: %+v", id, slot)
		}
		origins[slot.fromTop], _ = sc.restOrigin(id)
	}
	if origins[0].y >= origins[1].y {
		t.Fatalf("面板应按名次从上到下排列: %v", origins)
	}
	if origins[1].y != origins[0].y+h+panelGap {
		t.Fatalf("两个面板应紧挨排列: %v h=%v", origins, h)
	}
	for _, p := range origins {
		if p.y < top || p.y+h > bottom+h {
			t.Fatalf("面板 %v 应落在停靠范围 [%v, %v] 内: h=%v", p, top, bottom+h, h)
		}
	}
}

// paneH 是扣掉底部控制台后剩下的可用高度：停靠在底角的面板底边不能越过 paneH - panelBottomMargin，
// 否则浮窗会压在控制台上。
func TestComputeSwarmSceneBottomDockStaysAboveReservedHeight(t *testing.T) {
	paneH := float32(520)
	sc := computeSwarmScene(sceneInput{
		paneW: 1400, paneH: paneH, composerAreaH: 100,
		bees: testBees(1), showsTeam: true, teamDock: cornerBottomTrailing,
	})
	origin, ok := sc.restOrigin(teamPanelID)
	if !ok {
		t.Fatalf("团队面板应有停靠位置")
	}
	if bottom := origin.y + sc.layout.height(); bottom > paneH-panelBottomMargin {
		t.Fatalf("停靠面板底边 %v 越过可用高度 %v 的底边距 %v", bottom, paneH, panelBottomMargin)
	}
}
