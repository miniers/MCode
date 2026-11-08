package mcodeui

import (
	"math"
	"sort"
)

// 本文件是子代理浮动面板的几何计算（停靠、排序），只含纯计算，不依赖 UI。
// 面板停靠在聊天列旁的四个角，同一侧的多个面板按从底部向上的顺序堆叠；
// 主区域放不下“聊天框 + 面板”时，面板改为停在聊天框上方，互不遮挡。

const (
	// panelDefaultWidth 是面板默认宽度。
	panelDefaultWidth float32 = 400
	panelMinWidth     float32 = 300
	panelMinHeight    float32 = 220
	// panelGap 是面板与面板之间的上下间距。
	panelGap float32 = 12
	// panelSideGap 是面板与聊天列（正文、输入框）之间的水平间距；面板之间的上下间距仍用 panelGap。
	panelSideGap float32 = 24
	// panelSideMargin / panelBottomMargin 对齐聊天框自身的外边距，使停靠的面板与它对齐。
	panelSideMargin   float32 = 20
	panelBottomMargin float32 = 14
	// panelMinComposerWidth 是仍值得输入的最小聊天框宽度；低于它时面板改停到聊天框上方。
	panelMinComposerWidth float32 = 360
	// panelCornerSlack 是面板中心越过中线多远才换角，避免停在中线上的面板来回闪。
	panelCornerSlack float32 = 56
	// panelTopInset 使面板顶端与会话标题栏底边对齐。
	panelTopInset float32 = HeaderHeight
	// panelMaxStackSlots 是每侧最多可放的面板槽数。
	panelMaxStackSlots = 3
)

// panelSide 是面板相对聊天列所在的一侧。
type panelSide int

const (
	panelSideLeading panelSide = iota
	panelSideTrailing
)

// panelCorner 是面板停靠的角。默认 cornerBottomTrailing：右下角，向上堆叠。
type panelCorner int

const (
	cornerBottomTrailing panelCorner = iota
	cornerBottomLeading
	cornerTopTrailing
	cornerTopLeading
)

func (c panelCorner) side() panelSide {
	if c == cornerBottomLeading || c == cornerTopLeading {
		return panelSideLeading
	}
	return panelSideTrailing
}

func (c panelCorner) isTop() bool { return c == cornerTopLeading || c == cornerTopTrailing }

func makePanelCorner(side panelSide, isTop bool) panelCorner {
	switch {
	case side == panelSideLeading && isTop:
		return cornerTopLeading
	case side == panelSideTrailing && isTop:
		return cornerTopTrailing
	case side == panelSideLeading:
		return cornerBottomLeading
	default:
		return cornerBottomTrailing
	}
}

// across 返回聊天列另一侧、同一边缘（上或下）的角。
func (c panelCorner) across() panelCorner {
	return makePanelCorner(1-c.side(), c.isTop())
}

// flippedVertically 返回同一侧、另一边缘的角。
func (c panelCorner) flippedVertically() panelCorner {
	return makePanelCorner(c.side(), !c.isTop())
}

// panelPoint 与 panelSize 是面板几何用的二维值。
type panelPoint struct{ x, y float32 }
type panelSize struct{ w, h float32 }

// panelReserve 是停靠的面板从聊天列两侧各拿走的宽度；时间线与聊天框在剩余空间里居中。
type panelReserve struct{ leading, trailing float32 }

// panelLayout 由主区域尺寸算出面板的尺寸与停靠位置。
type panelLayout struct {
	paneW, paneH float32
	// composerAreaH 是聊天框连同标签与卡片的高度，面板停在它上方时据此避开。
	composerAreaH float32
	// contentMaxW 是时间线内容列的宽度上限（随「内容宽度」设置变化）；面板并排停靠时贴内容列外缘，
	// 而不贴输入框：输入框固定比内容列窄（宽档、按比例档尤甚），贴输入框会压住正文。
	// 0 表示未指定，按标准档 timelineContentMaxWidthStandard 处理。
	contentMaxW float32
	// stackDepth 是面板最多那一侧的面板数：它们平分标题栏到聊天框之间的高度。
	stackDepth int
	// prefW/prefH 是用户拖拽设定的尺寸，0 表示自动。
	prefW, prefH float32
}

func (l panelLayout) width() float32 {
	w := l.prefW
	if w <= 0 {
		w = panelDefaultWidth
	}
	return min(max(w, panelMinWidth), max(panelMinWidth, l.paneW-2*panelSideMargin))
}

// contentMaxWidth 返回时间线内容列的宽度上限：未指定时按标准档 timelineContentMaxWidthStandard。
func (l panelLayout) contentMaxWidth() float32 {
	if l.contentMaxW <= 0 {
		return timelineContentMaxWidthStandard
	}
	return l.contentMaxW
}

// sitsBesideComposer 表示面板与聊天框能否并排。
func (l panelLayout) sitsBesideComposer() bool {
	return l.paneW-2*panelSideMargin-l.width()-panelSideGap >= panelMinComposerWidth
}

// fitsBothSides 表示两侧各放一个面板后聊天框是否仍够宽；放不下时所有面板只停在同一侧。
func (l panelLayout) fitsBothSides() bool {
	return l.paneW-2*panelSideMargin-2*(l.width()+panelSideGap) >= panelMinComposerWidth
}

// composerReserve 是并排时面板从聊天框所在行拿走的宽度。
func (l panelLayout) composerReserve() float32 {
	if l.sitsBesideComposer() {
		return l.width() + panelSideGap
	}
	return 0
}

// verticalRoom 是停靠面板从标题栏到聊天框（并排时到底边距）的可用高度。
func (l panelLayout) verticalRoom() float32 {
	below := panelBottomMargin
	if !l.sitsBesideComposer() {
		below = l.composerAreaH + panelGap
	}
	return l.paneH - panelTopInset - below
}

func (l panelLayout) height() float32 {
	var ideal float32
	switch {
	case l.prefH > 0:
		ideal = l.prefH
	default:
		ideal = min(520, max(300, l.paneH*0.5))
	}
	depth := float32(max(1, l.stackDepth))
	share := (l.verticalRoom() - (depth-1)*panelGap) / depth
	return max(panelMinHeight, min(ideal, share))
}

func (l panelLayout) size() panelSize { return panelSize{l.width(), l.height()} }

// fitted 把拖拽中的尺寸收进主区域能容纳的范围：不小于最小面板，不宽于边距允许，不高于该宽度下的可用高度。
func (l panelLayout) fitted(s panelSize) panelSize {
	trial := l
	trial.prefW = s.w
	trial.prefH = math.MaxFloat32 / 4
	return panelSize{trial.width(), max(panelMinHeight, min(s.h, trial.height()))}
}

// dockedOrigin 是某个角的停靠位置（面板左上角）。
func (l panelLayout) dockedOrigin(corner panelCorner, reserve panelReserve) panelPoint {
	var y float32
	if corner.isTop() {
		y = panelTopInset
	} else if l.sitsBesideComposer() {
		y = l.paneH - panelBottomMargin - l.height()
	} else {
		y = l.paneH - (l.composerAreaH + panelGap) - l.height()
	}
	// 可用高度（已扣掉底部控制台）容不下最低高度的面板时，宁可让面板顶到标题栏之下，也不让它上部跑出窗口。
	y = max(y, panelTopInset)
	w := l.width()
	if l.sitsBesideComposer() {
		row := l.paneW - reserve.leading - reserve.trailing
		columnW := min(l.contentMaxWidth(), row-2*panelSideMargin)
		columnLeft := reserve.leading + (row-columnW)/2
		if corner.side() == panelSideLeading {
			return panelPoint{columnLeft - panelSideGap - w, y}
		}
		return panelPoint{columnLeft + columnW + panelSideGap, y}
	}
	if corner.side() == panelSideLeading {
		return panelPoint{panelSideMargin, y}
	}
	return panelPoint{l.paneW - panelSideMargin - w, y}
}

// panelDocks 是当前面板尺寸下的四个停靠位置。
type panelDocks struct {
	topLeading, topTrailing, bottomLeading, bottomTrailing panelPoint
}

func newPanelDocks(l panelLayout, reserve panelReserve) panelDocks {
	return panelDocks{
		topLeading:     l.dockedOrigin(cornerTopLeading, reserve),
		topTrailing:    l.dockedOrigin(cornerTopTrailing, reserve),
		bottomLeading:  l.dockedOrigin(cornerBottomLeading, reserve),
		bottomTrailing: l.dockedOrigin(cornerBottomTrailing, reserve),
	}
}

func (d panelDocks) at(c panelCorner) panelPoint {
	switch c {
	case cornerTopLeading:
		return d.topLeading
	case cornerTopTrailing:
		return d.topTrailing
	case cornerBottomLeading:
		return d.bottomLeading
	default:
		return d.bottomTrailing
	}
}

// stacked 是面板在某侧 slots 个槽位里的位置：槽位在上下两个停靠位置之间均分；
// 上角里 below=b 的面板落在第 b 槽，下角里的落在第 slots-1-b 槽，即从底部向上堆叠。
func (d panelDocks) stacked(corner panelCorner, below, slots int) panelPoint {
	var step float32
	if slots > 1 {
		step = (d.bottomLeading.y - d.topLeading.y) / float32(slots-1)
	}
	index := slots - 1 - below
	if corner.isTop() {
		index = below
	}
	return panelPoint{d.at(corner).x, d.topLeading.y + float32(max(0, index))*step}
}

// panelCornerForSlot 是某侧第 index 个槽位所属的角与其中的堆叠深度：上半在上角自上而下数，下半在下角自下而上数。
func panelCornerForSlot(index, slots int, side panelSide) (panelCorner, int) {
	if index <= (slots-1)/2 {
		return makePanelCorner(side, true), index
	}
	return makePanelCorner(side, false), slots - 1 - index
}

// dockCorner 返回 origin 处的面板应归属的角：中心所在的一侧与较近的上下位置；
// 当前所在的角会保持到中心越过中线 panelCornerSlack，停在线上的面板因此不会来回闪。
func (l panelLayout) dockCorner(origin panelPoint, keeping panelCorner, docks panelDocks) panelCorner {
	size := l.size()
	cx, cy := origin.x+size.w/2, origin.y+size.h/2
	mx := l.paneW / 2
	my := (docks.topLeading.y + docks.bottomLeading.y + size.h) / 2
	side := keeping.side()
	if dx := cx - mx; dx < -panelCornerSlack {
		side = panelSideLeading
	} else if dx >= panelCornerSlack {
		side = panelSideTrailing
	}
	isTop := keeping.isTop()
	if dy := cy - my; dy < -panelCornerSlack {
		isTop = true
	} else if dy >= panelCornerSlack {
		isTop = false
	}
	return makePanelCorner(side, isTop)
}

// slotIndex 是 origin 处的面板越过的槽位：y 最近的那个槽。
func (l panelLayout) slotIndex(origin panelPoint, slots int, docks panelDocks) int {
	if slots <= 1 {
		return 0
	}
	step := (docks.bottomLeading.y - docks.topLeading.y) / float32(slots-1)
	if step <= 0 {
		return 0
	}
	idx := int(math.Round(float64((origin.y - docks.topLeading.y) / step)))
	return min(slots-1, max(0, idx))
}

// clamped 把拖动中的面板限制在主区域内。
func (l panelLayout) clamped(p panelPoint) panelPoint {
	const inset float32 = 8
	size := l.size()
	return panelPoint{
		x: min(max(p.x, inset), max(inset, l.paneW-size.w-inset)),
		y: min(max(p.y, inset), max(inset, l.paneH-size.h-inset)),
	}
}

// panelID 标识一个浮动面板：团队面板，或某个子代理自己的面板。
type panelID string

const teamPanelID panelID = "team"

func autoPanelID(bee string) panelID { return panelID("auto:" + bee) }

func (id panelID) beeID() (string, bool) {
	const prefix = "auto:"
	if len(id) > len(prefix) && string(id[:len(prefix)]) == prefix {
		return string(id[len(prefix):]), true
	}
	return "", false
}

// panelSlot 是面板所在的角，以及同一角里排在它下面的面板数。
type panelSlot struct {
	corner panelCorner
	below  int
	// fromTop 是同一侧面板按槽位从上到下排的名次，sideCount 是该侧的面板数：
	// 两者让变高的面板在同一侧紧挨排列，彼此不重叠。
	fromTop, sideCount int
}

// slotTop 是槽位在这一侧从上往下数的位置。
func slotTop(slot panelSlot, slots int) int {
	if slot.corner.isTop() {
		return slot.below
	}
	return slots - 1 - slot.below
}

// sceneInput 是面板场景的全部输入。
type sceneInput struct {
	paneW, paneH  float32
	composerAreaH float32
	// contentMaxW 是时间线内容列的宽度上限（随「内容宽度」设置变化）；0 表示按标准档。
	contentMaxW float32
	// 用户拖拽设定的面板尺寸，0 表示自动。
	prefW, prefH float32
	// bees 是主会话当前所有子代理，按派出顺序。
	bees []SwarmBee
	// autoPops 为 true 时，第一个之后的每个子代理在空间允许时有自己的面板。
	autoPops bool
	// showsTeam 为 false 表示团队面板被用户关闭。
	showsTeam bool
	// forcesTeam 表示用户强制显示浮窗：没有子代理时也渲染一个空的团队面板。
	forcesTeam bool
	teamDock   panelCorner
	autoDocks  map[string]panelCorner
	// autoHeld 是被送回团队面板、不再单独弹出的子代理。
	autoHeld   map[string]bool
	stackOrder []panelID
}

// swarmScene 是一帧里所有面板的位置与聊天列让出的空间。
type swarmScene struct {
	layout panelLayout
	// teamBees 是团队面板里的子代理；autoBees 是有自己面板的子代理。
	teamBees  []SwarmBee
	autoBees  []SwarmBee
	showsTeam bool
	slots     map[panelID]panelSlot
	// slotsOnSide 是每侧的槽位数：面板可以放下的位置。
	slotsOnSide map[panelSide]int
	reserve     panelReserve
	docks       panelDocks
	isMeasured  bool
}

// has 表示场景里是否有任何面板。
func (sc swarmScene) has() bool { return len(sc.slots) > 0 }

// lockedSide 返回主区域放不下两侧面板时所有面板必须停靠的那一侧：团队面板所在侧，否则较满的一侧；
// 两侧都放得下或没有别的面板时返回 false。
func (sc swarmScene) lockedSide(excluding panelID) (panelSide, bool) {
	if sc.layout.fitsBothSides() {
		return 0, false
	}
	others := 0
	leading := 0
	for id, slot := range sc.slots {
		if id == excluding {
			continue
		}
		others++
		if slot.corner.side() == panelSideLeading {
			leading++
		}
	}
	if others == 0 {
		return 0, false
	}
	if team, ok := sc.slots[teamPanelID]; ok && excluding != teamPanelID {
		return team.corner.side(), true
	}
	if leading >= others-leading {
		return panelSideLeading, true
	}
	return panelSideTrailing, true
}

// computeSwarmScene 由输入算出面板场景：谁有面板、停在哪个角、堆叠顺序、聊天列让出多少宽度。
func computeSwarmScene(in sceneInput) swarmScene {
	measured := in.paneW > 0 && in.paneH > 0
	showsTeam := in.showsTeam && (len(in.bees) > 0 || in.forcesTeam) && measured
	single := panelLayout{paneW: in.paneW, paneH: in.paneH, composerAreaH: in.composerAreaH, contentMaxW: in.contentMaxW, stackDepth: 1, prefW: in.prefW, prefH: in.prefH}
	fitsBoth := single.fitsBothSides()

	// 每侧最多放几个面板：按它们缩到最小时的高度计，至多三个。
	smallest := panelMinHeight
	if in.prefH > 0 {
		smallest = min(smallest, max(panelMinHeight, in.prefH))
	}
	natural := min(panelMaxStackSlots, max(1, int((single.verticalRoom()+panelGap)/(smallest+panelGap))))

	type placement struct {
		id     panelID
		corner panelCorner
	}
	var placed []placement
	var autoBees []SwarmBee
	if showsTeam {
		placed = append(placed, placement{teamPanelID, in.teamDock})
	}
	if in.autoPops && showsTeam && len(in.bees) > 1 {
		var candidates []SwarmBee
		for _, h := range in.bees[1:] {
			if !in.autoHeld[h.ThreadID] {
				candidates = append(candidates, h)
			}
		}
		// 已有停靠记录的子代理保持原位。
		var rest []SwarmBee
		for _, h := range candidates {
			if corner, ok := in.autoDocks[h.ThreadID]; ok {
				placed = append(placed, placement{autoPanelID(h.ThreadID), corner})
				autoBees = append(autoBees, h)
			} else {
				rest = append(rest, h)
			}
		}
		// 其余的在两侧之间均衡：哪个角的面板少就放哪一侧，数量相等时先放团队面板对侧；
		// 团队这一侧从团队面板的对角（顶部）开始放，两侧都满了就留在团队面板里。
		// 窄到放不下两侧时只用团队面板这一侧，从底部往上排。
		if !fitsBoth {
			corner := in.teamDock
			other := corner.flippedVertically()
			used := 0
			for _, p := range placed {
				if p.corner == corner || p.corner == other {
					used++
				}
			}
			free := max(0, natural-used)
			for i := 0; i < free && len(rest) > 0; i++ {
				h := rest[0]
				rest = rest[1:]
				placed = append(placed, placement{autoPanelID(h.ThreadID), corner})
				autoBees = append(autoBees, h)
			}
		} else {
			across := in.teamDock.across()
			home := in.teamDock.flippedVertically()
			away, onHome := 0, 0
			for _, p := range placed {
				switch p.corner.side() {
				case across.side():
					away++
				case in.teamDock.side():
					onHome++
				}
			}
			for len(rest) > 0 {
				if away >= natural && onHome >= natural {
					break
				}
				var corner panelCorner
				switch {
				case onHome >= natural:
					corner = across
					away++
				case away >= natural || onHome < away:
					corner = home
					onHome++
				default:
					corner = across
					away++
				}
				h := rest[0]
				rest = rest[1:]
				placed = append(placed, placement{autoPanelID(h.ThreadID), corner})
				autoBees = append(autoBees, h)
			}
		}
	}

	// 放不下两侧：所有面板去团队面板所在侧，各自保持上或下。
	if !fitsBoth {
		sides := map[panelSide]int{}
		for _, p := range placed {
			sides[p.corner.side()]++
		}
		if len(sides) > 1 {
			home := panelSideTrailing
			if showsTeam {
				home = in.teamDock.side()
			} else if sides[panelSideLeading] >= sides[panelSideTrailing] {
				home = panelSideLeading
			}
			for i := range placed {
				placed[i].corner = makePanelCorner(home, placed[i].corner.isTop())
			}
		}
	}

	rank := map[panelID]int{}
	for _, id := range in.stackOrder {
		if _, ok := rank[id]; !ok {
			rank[id] = len(rank)
		}
	}
	slots := map[panelID]panelSlot{}
	byCorner := map[panelCorner][]int{}
	for i, p := range placed {
		byCorner[p.corner] = append(byCorner[p.corner], i)
	}
	for corner, idxs := range byCorner {
		sort.SliceStable(idxs, func(a, b int) bool {
			ra, oka := rank[placed[idxs[a]].id]
			rb, okb := rank[placed[idxs[b]].id]
			if !oka {
				ra = math.MaxInt32
			}
			if !okb {
				rb = math.MaxInt32
			}
			if ra != rb {
				return ra < rb
			}
			return idxs[a] < idxs[b]
		})
		for below, i := range idxs {
			slots[placed[i].id] = panelSlot{corner: corner, below: below}
		}
	}

	sidesUsed := map[panelSide]bool{}
	for _, slot := range slots {
		sidesUsed[slot.corner.side()] = true
	}
	// 槽位数是面板可以放下的位置；实际放下的面板数决定它们平分可用高度。
	slotsOnSide := map[panelSide]int{}
	stackDepth := 1
	for _, side := range []panelSide{panelSideLeading, panelSideTrailing} {
		count := 0
		for _, slot := range slots {
			if slot.corner.side() == side {
				count++
			}
		}
		slotsOnSide[side] = max(natural, count)
		stackDepth = max(stackDepth, count)
	}

	// 同一侧的面板按槽位从上到下排名次：均分槽位得到的位置据此收紧成紧挨排列。
	for _, side := range []panelSide{panelSideLeading, panelSideTrailing} {
		var ids []panelID
		for id, slot := range slots {
			if slot.corner.side() == side {
				ids = append(ids, id)
			}
		}
		n := slotsOnSide[side]
		sort.Slice(ids, func(a, b int) bool { return slotTop(slots[ids[a]], n) < slotTop(slots[ids[b]], n) })
		for fromTop, id := range ids {
			slot := slots[id]
			slot.fromTop, slot.sideCount = fromTop, len(ids)
			slots[id] = slot
		}
	}

	layout := panelLayout{paneW: in.paneW, paneH: in.paneH, composerAreaH: in.composerAreaH, contentMaxW: in.contentMaxW, stackDepth: stackDepth, prefW: in.prefW, prefH: in.prefH}
	var reserve panelReserve
	if sidesUsed[panelSideLeading] {
		reserve.leading = layout.composerReserve()
	}
	if sidesUsed[panelSideTrailing] {
		reserve.trailing = layout.composerReserve()
	}

	autoSet := map[string]bool{}
	for _, h := range autoBees {
		autoSet[h.ThreadID] = true
	}
	var teamBees []SwarmBee
	if showsTeam {
		for _, h := range in.bees {
			if !autoSet[h.ThreadID] {
				teamBees = append(teamBees, h)
			}
		}
	}
	return swarmScene{
		layout:      layout,
		teamBees:    teamBees,
		autoBees:    autoBees,
		showsTeam:   showsTeam,
		slots:       slots,
		slotsOnSide: slotsOnSide,
		reserve:     reserve,
		docks:       newPanelDocks(layout, reserve),
		isMeasured:  measured,
	}
}

// restOrigin 是面板在场景里的静止位置（左上角）。
func (sc swarmScene) restOrigin(id panelID) (panelPoint, bool) {
	slot, ok := sc.slots[id]
	if !ok {
		return panelPoint{}, false
	}
	p := sc.docks.stacked(slot.corner, slot.below, sc.slotsOnSide[slot.corner.side()])
	// 均分到的位置再收紧：空间够时保持原槽位，面板变高后同侧面板紧挨排列、互不压住。
	if slot.sideCount > 0 {
		step := sc.layout.height() + panelGap
		top, bottom := sc.docks.topLeading.y, sc.docks.bottomLeading.y
		p.y = max(p.y, top+float32(slot.fromTop)*step)
		p.y = min(p.y, bottom-float32(slot.sideCount-1-slot.fromTop)*step)
	}
	return p, true
}

// reorderedPanels 让被拖动的面板占据它所在一侧的当前槽位，并返回改写后的角与堆叠顺序：
// 空槽直接占用；满侧里被占的槽与面板原来的槽互换；否则占用者及其后的面板依次让一格。
func reorderedPanels(sc swarmScene, id panelID, at panelPoint, corner panelCorner, stackOrder []panelID) (map[panelID]panelCorner, []panelID) {
	side := corner.side()
	others := map[panelID]panelSlot{}
	for pid, slot := range sc.slots {
		if pid != id && slot.corner.side() == side {
			others[pid] = slot
		}
	}
	n := max(sc.slotsOnSide[side], len(others)+1)
	if n < 1 {
		n = 1
	}
	target := sc.layout.slotIndex(at, n, sc.docks)
	slotOf := func(slot panelSlot) int { return slotTop(slot, n) }
	occupancy := map[int]panelID{}
	for pid, slot := range others {
		occupancy[slotOf(slot)] = pid
	}
	previous := -1
	if cur, ok := sc.slots[id]; ok && cur.corner.side() == side {
		previous = slotOf(cur)
	}
	full := !(len(others)+1 > sc.slotsOnSide[side])
	if other, taken := occupancy[target]; taken && previous >= 0 && full {
		occupancy[previous] = other
		occupancy[target] = id
	} else if taken {
		delete(occupancy, target)
		occupancy[target] = id
		forward := previous < 0 || previous < target
		displaced := other
		next := target - 1
		if forward {
			next = target + 1
		}
		for {
			if next < 0 || next >= n {
				free := -1
				if forward {
					for i := 0; i < n; i++ {
						if _, used := occupancy[i]; !used {
							free = i
							break
						}
					}
				} else {
					for i := n - 1; i >= 0; i-- {
						if _, used := occupancy[i]; !used {
							free = i
							break
						}
					}
				}
				if free >= 0 {
					occupancy[free] = displaced
				}
				break
			}
			occupant, used := occupancy[next]
			if !used {
				occupancy[next] = displaced
				break
			}
			occupancy[next] = displaced
			displaced = occupant
			if forward {
				next++
			} else {
				next--
			}
		}
	} else {
		occupancy[target] = id
	}

	corners := map[panelID]panelCorner{}
	var top, bottom []int
	for index, pid := range occupancy {
		c, _ := panelCornerForSlot(index, n, side)
		corners[pid] = c
		if c.isTop() {
			top = append(top, index)
		} else {
			bottom = append(bottom, index)
		}
	}
	sort.Ints(top)
	sort.Sort(sort.Reverse(sort.IntSlice(bottom)))
	var sideOrder []panelID
	for _, index := range top {
		sideOrder = append(sideOrder, occupancy[index])
	}
	for _, index := range bottom {
		sideOrder = append(sideOrder, occupancy[index])
	}
	onSide := map[panelID]bool{}
	for _, pid := range occupancy {
		onSide[pid] = true
	}
	next := make([]panelID, 0, len(stackOrder)+len(sideOrder))
	for _, pid := range stackOrder {
		if !onSide[pid] && pid != id {
			next = append(next, pid)
		}
	}
	next = append(next, sideOrder...)
	return corners, next
}
