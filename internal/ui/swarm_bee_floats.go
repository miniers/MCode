package mcodeui

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

// 子代理浮动面板。
// 默认停在聊天框右侧并从底部向上堆叠；“自动弹出子代理面板”关闭时所有子代理聚合在一个团队面板里，
// 开启时空间允许的子代理各有自己的面板；面板可拖动换角与排序，边与角可拖拽调整大小（双击角复位）。
// 底部输入框是嵌入的 composer（见 swarm_embedded.go），与主会话共用同一套输入逻辑。

// 面板内部用到的固定尺寸。
const (
	swarmGripThick    float32 = 6
	swarmGripCorner   float32 = 12
	swarmGripInset    float32 = 24
	swarmRestDuration         = 180 * time.Millisecond
	// swarmPulseDuration 是浮窗被打开/选中后的激活脉冲时长：这期间卡片边框用强调色。
	swarmPulseDuration = 1500 * time.Millisecond
)

// swarmLiveResize 是正在拖拽调整的面板尺寸：松手前只在内存里，松手后写入配置。
type swarmLiveResize struct {
	size  panelSize
	owner string
}

// 浮窗状态按主会话 ID 记录：关闭状态、选中的子代理、停靠位置与堆叠顺序、拖拽中的临时值。
var (
	swarmFloatMu             sync.Mutex
	swarmFloatPanelDismissed = make(map[string]bool)
	swarmFloatDismissedBees  = make(map[string]map[string]bool)
	swarmSelectedBeeID       = make(map[string]string)
	swarmTeamDock            = make(map[string]panelCorner)
	swarmAutoDocks           = make(map[string]map[string]panelCorner)
	swarmAutoHeld            = make(map[string]map[string]bool)
	swarmStackOrder          = make(map[string][]panelID)
	swarmDragPos             = make(map[string]panelPoint)
	swarmLiveSizes           = make(map[string]swarmLiveResize)
	swarmMenuOpen            = make(map[string]*bool)
	// swarmFloatForced 记录用户用编排按钮强制显示的浮窗；没有子代理时也会显示空团队面板。
	swarmFloatForced = make(map[string]bool)
	// swarmFloatForcedHadBees 记录强制显示期间是否出现过子代理；出现过且全部结束后自动关闭空面板。
	swarmFloatForcedHadBees = make(map[string]bool)
	// swarmAutoPopsOverride 是对“自动分散展示”的临时覆盖：键存在即覆盖，值为分散与否，不写配置。
	swarmAutoPopsOverride = make(map[string]bool)
	// swarmQuestionPopped 记录每个主会话下已经为「等用户回答」弹出过浮窗的子线程：
	// 新进入等待的弹一次，不再等待就删掉，下次再提问可以再弹。
	swarmQuestionPopped = make(map[string]map[string]bool)
	// swarmPulseTime 记录每个子代理浮窗的激活脉冲起点，键为「主会话|子线程」。
	// swarmPulseTimers 是脉冲到点的重绘唤醒，避免强调色边框要等下一帧事件才褪回。
	swarmPulseTime   = make(map[string]time.Time)
	swarmPulseTimers = make(map[string]*time.Timer)
)

// swarmBeePulsing 报告某个子代理浮窗是否还在激活脉冲期内（刚被打开或选中）。
// 脉冲到期时排一次重绘唤醒，让强调色边框自动褪回常规颜色。
func swarmBeePulsing(s *chatState, leadID, threadID string) bool {
	if leadID == "" || threadID == "" {
		return false
	}
	key := leadID + "|" + threadID
	now := time.Now()
	swarmFloatMu.Lock()
	since, ok := swarmPulseTime[key]
	if !ok {
		swarmFloatMu.Unlock()
		return false
	}
	left := swarmPulseDuration - now.Sub(since)
	if left <= 0 {
		delete(swarmPulseTime, key)
		swarmFloatMu.Unlock()
		return false
	}
	if _, pending := swarmPulseTimers[key]; !pending && s != nil {
		swarmPulseTimers[key] = time.AfterFunc(left, func() {
			swarmFloatMu.Lock()
			delete(swarmPulseTimers, key)
			swarmFloatMu.Unlock()
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if !closed {
				s.invalidate()
			}
		})
	}
	swarmFloatMu.Unlock()
	return true
}

// swarmMenuFlag 返回某个面板子代理菜单的开合状态指针。
func swarmMenuFlag(key string) *bool {
	swarmFloatMu.Lock()
	defer swarmFloatMu.Unlock()
	flag, ok := swarmMenuOpen[key]
	if !ok {
		flag = new(bool)
		swarmMenuOpen[key] = flag
	}
	return flag
}

// isSwarmFloatPanelDismissed 检查当前主会话的浮窗是否被用户主动关闭。
// 如果派出了新的 bee（此前不在已记录的关闭集合中），自动解除关闭状态并重新显示。
func isSwarmFloatPanelDismissed(leadID string, bees []SwarmBee) bool {
	swarmFloatMu.Lock()
	defer swarmFloatMu.Unlock()

	if !swarmFloatPanelDismissed[leadID] {
		return false
	}
	dismissedSet := swarmFloatDismissedBees[leadID]
	for _, h := range bees {
		if h.ThreadID != "" && (dismissedSet == nil || !dismissedSet[h.ThreadID]) {
			// 派出了新 bee！自动重新展示
			swarmFloatPanelDismissed[leadID] = false
			return false
		}
	}
	return true
}

// dismissSwarmFloatPanel 记录当前会话浮窗被用户关闭。
// 关闭集合按子线程累积，之前自动收起的子线程继续留在集合里，不会因用户关闭面板又冒出来。
func dismissSwarmFloatPanel(leadID string, bees []SwarmBee) {
	swarmFloatMu.Lock()
	defer swarmFloatMu.Unlock()

	delete(swarmFloatForced, leadID)
	delete(swarmFloatForcedHadBees, leadID)
	// 用户关掉浮窗即解除「明确点开」的保留，下次再打开时按普通子线程重新计时。
	delete(swarmBeeKeptOpen, leadID)
	swarmFloatPanelDismissed[leadID] = true
	set := swarmFloatDismissedBees[leadID]
	if set == nil {
		set = make(map[string]bool)
		swarmFloatDismissedBees[leadID] = set
	}
	for _, h := range bees {
		if h.ThreadID != "" {
			set[h.ThreadID] = true
		}
	}
}

// undismissSwarmFloatPanel 清除浮窗的关闭记录并标记为强制显示（仅内存，不写配置）。
// 没有子代理时它会渲染一个空的团队面板。
// 已经自动收起的子线程一并放回浮窗，并从打开这一刻重新计时：满 swarmAutoHideDelay 后照常收起，
// 不会一打开就消失。编排按钮要的正是「全部放回」；时间轴里点某个子线程走 openSwarmFloatForBee，
// 那条路径只放回被点的那一个，不经过这里。
func undismissSwarmFloatPanel(leadID string) {
	swarmFloatMu.Lock()
	defer swarmFloatMu.Unlock()

	delete(swarmFloatPanelDismissed, leadID)
	delete(swarmFloatDismissedBees, leadID)
	swarmFloatForced[leadID] = true
	// 重新手动打开时重新计数，这样之后真的有子代理全结束才自动关闭。
	delete(swarmFloatForcedHadBees, leadID)
	// 编排按钮是「全部放回」：一并解除时间轴里明确点开的保留，所有子线程都按 5 秒规则收起。
	delete(swarmBeeKeptOpen, leadID)
	now := time.Now()
	handled := swarmBeeAutoHideHandled[leadID]
	dones := swarmBeeDoneSince[leadID]
	for id := range dones {
		// 从打开这一刻重新计 5 秒，并撤掉「已处理」，下一帧按新时刻重新排定唤醒。
		dones[id] = now
		if handled != nil {
			delete(handled, id)
		}
		key := leadID + "|" + id
		if t, ok := swarmAutoHideTimers[key]; ok {
			t.Stop()
			delete(swarmAutoHideTimers, key)
		}
	}
}

// swarmFloatForcedFor 返回当前会话的浮窗是否被强制显示。
func swarmFloatForcedFor(leadID string) bool {
	swarmFloatMu.Lock()
	defer swarmFloatMu.Unlock()
	return swarmFloatForced[leadID]
}

// settleSwarmFloatForced 结算强制显示状态，返回本帧是否继续按强制显示处理。
// 强制显示期间出现过子代理、如今全部结束时清除标记，空白编排面板随之自动关闭；
// 用户手动打开且从未有过子代理时保留空面板。
func settleSwarmFloatForced(leadID string, beeCount int) bool {
	swarmFloatMu.Lock()
	defer swarmFloatMu.Unlock()
	if !swarmFloatForced[leadID] {
		return false
	}
	if beeCount > 0 {
		swarmFloatForcedHadBees[leadID] = true
		return true
	}
	if swarmFloatForcedHadBees[leadID] {
		delete(swarmFloatForced, leadID)
		delete(swarmFloatForcedHadBees, leadID)
		return false
	}
	return true
}

// swarmFloatVisible 判断浮窗此刻是否显示：被关闭时不显示；否则强制显示，或至少有子代理。
func swarmFloatVisible(leadID string, bees []SwarmBee) bool {
	if isSwarmFloatPanelDismissed(leadID, bees) {
		return false
	}
	return swarmFloatForcedFor(leadID) || len(bees) > 0
}

// swarmAutoHideDelay 是子线程各自进入终态后，把它从浮窗里收起的等待时长。
const swarmAutoHideDelay = 5 * time.Second

// 自动收起的记账状态（按子线程），均由 swarmFloatMu 保护：
//   - swarmBeeDoneSince 记录每个子线程进入终态的时刻（主会话 -> 子线程 -> 时刻），重新运行时清除；
//   - swarmAutoHideTimers 是已排定的到期唤醒，键为「主会话|子线程」，避免每帧重复排定；
//   - swarmBeeAutoHideHandled 记录已经自动收起过、或被用户明确点开的子线程，本帧不再收起；
//   - swarmBeeSeenActive 记录本次启动里是否见过某个子线程未处于终态（主会话 -> 子线程 -> bool），
//     用来区分「本进程看着它跑完」与「启动前就已经结束」：后者首次观察到即直接收起。
//   - swarmBeeKeptOpen 记录用户在时间轴里明确点开的子线程：它们一直留在浮窗里，不自动收起、
//     不重新计时（handled 会被可见性自愈清掉，这里才是持久的那份），直到用户关闭浮窗、
//     用编排按钮整体重开，或该子线程重新开始运行。
var (
	swarmBeeDoneSince       = make(map[string]map[string]time.Time)
	swarmAutoHideTimers     = make(map[string]*time.Timer)
	swarmBeeAutoHideHandled = make(map[string]map[string]bool)
	swarmBeeSeenActive      = make(map[string]map[string]bool)
	swarmBeeKeptOpen        = make(map[string]map[string]bool)
)

// swarmAutoHideWake 是一个待排定的自动收起到期唤醒。
type swarmAutoHideWake struct {
	key  string
	wait time.Duration
}

// swarmBeeDormant 判断单个子线程是否已进入终态：既没有在运行，也没有完成但仍在等人工合并审查。
// 只有 running 的 bee 才会因为 waiting（等用户回答提问或审批）而留在浮窗里；终态 bee 残留的
// waiting 无法再被回答，不应阻止收起。
func swarmBeeDormant(h SwarmBee, waiting bool) bool {
	return !(waiting && h.Status == "running") && !h.PendingMerge && h.Status != "running"
}

// swarmBeeShouldAutoHide 判定单个子线程此刻是否应该自动从浮窗里收起（纯函数，便于单测）：
// 该子线程已进入终态、自它进入终态起已满 swarmAutoHideDelay，且这批完成还没处理过
// （没自动收起过、用户也没手动打开过）。
func swarmBeeShouldAutoHide(h SwarmBee, waiting bool, doneSince, now time.Time, handled bool) bool {
	if handled || doneSince.IsZero() || !swarmBeeDormant(h, waiting) {
		return false
	}
	return !now.Before(doneSince.Add(swarmAutoHideDelay))
}

// swarmVisibleBees 去掉已经自动收起的子线程：自动分散展示时不再给它出面板，
// 聚合展示时它不再是当前显示项、也不出现在 bee 菜单里；
// 整体打开浮窗（undismissSwarmFloatPanel）会清掉这批关闭记录；时间轴里点某个子线程
// （openSwarmFloatForBee）只清它自己那一条，其它收起状态保留。
// 每帧要调用多次（让位宽度与面板渲染各一次），没有任何关闭记录时直接返回原切片；
// 有记录时先数一遍再按实际可见数分配，未全部收起时不为被收起的子线程白占容量。
func swarmVisibleBees(leadID string, bees []SwarmBee) []SwarmBee {
	swarmFloatMu.Lock()
	dismissed := swarmFloatDismissedBees[leadID]
	if len(dismissed) == 0 {
		swarmFloatMu.Unlock()
		return bees
	}
	keep := 0
	for _, h := range bees {
		if h.ThreadID == "" || !dismissed[h.ThreadID] {
			keep++
		}
	}
	if keep == 0 {
		swarmFloatMu.Unlock()
		return bees[:0]
	}
	visible := make([]SwarmBee, 0, keep)
	for _, h := range bees {
		if h.ThreadID == "" || !dismissed[h.ThreadID] {
			visible = append(visible, h)
		}
	}
	swarmFloatMu.Unlock()
	return visible
}

// swarmAutoDismissBees 把已到期的子线程记进关闭集合，并报告是否所有子线程都已关闭。
func swarmAutoDismissBees(leadID string, bees []SwarmBee, ids []string) bool {
	swarmFloatMu.Lock()
	defer swarmFloatMu.Unlock()

	set := swarmFloatDismissedBees[leadID]
	if set == nil {
		set = make(map[string]bool)
		swarmFloatDismissedBees[leadID] = set
	}
	handled := swarmBeeAutoHideHandled[leadID]
	if handled == nil {
		handled = make(map[string]bool)
		swarmBeeAutoHideHandled[leadID] = handled
	}
	for _, id := range ids {
		set[id] = true
		handled[id] = true
	}
	all := len(bees) > 0
	for _, h := range bees {
		if id := strings.TrimSpace(h.ThreadID); id != "" && !set[id] {
			all = false
			break
		}
	}
	return all
}

// swarmWaitingBeeIDs 按 bees 顺序返回正在等用户回答提问或审批的子线程 ID：
// 这类子线程的会话状态带待处理请求。待处理请求集合由 chatState 按会话运行态版本缓存，
// 这里只按 bees 做一次查表，不再每帧重扫全部会话列表。
func swarmWaitingBeeIDs(s *chatState, bees []SwarmBee) []string {
	if s == nil || len(bees) == 0 {
		return nil
	}
	pending := s.pendingRequestThreadIDs()
	if len(pending) == 0 {
		return nil
	}
	var ids []string
	var seen map[string]bool
	for _, h := range bees {
		id := strings.TrimSpace(h.ThreadID)
		if id == "" || !pending[id] {
			continue
		}
		if seen == nil {
			seen = make(map[string]bool, 2)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}

// swarmNewlyWaiting 由上一帧的等待集合算出本帧新进入等待的子线程（保持 waiting 顺序），
// 以及本帧的等待集合（纯函数，便于单测）。
func swarmNewlyWaiting(prev map[string]bool, waiting []string) (newIDs []string, next map[string]bool) {
	next = make(map[string]bool, len(waiting))
	for _, id := range waiting {
		if id == "" {
			continue
		}
		next[id] = true
		if !prev[id] {
			newIDs = append(newIDs, id)
		}
	}
	return newIDs, next
}

// trackSwarmQuestionPop 每帧检查有没有子线程刚进入「等用户回答」状态：
// 新进入的立即弹出浮窗并选中该子线程，属于强提醒——既不受「自动弹出子代理面板」开关影响，
// 也不管用户是否手动关过浮窗；它走 revealSwarmFloatForBee，是程序自动弹出而非用户手动打开，
// 所以不设强制显示、也不把其它已完成的子线程记成已处理，那些子线程仍按各自的 5 秒规则收起。
// 不再等待的从集合里删掉，下次再提问可以再弹一次。
// precomputedWait 是调用方已经算好的等待集合（同一帧只算一次），没传时自己算。
func trackSwarmQuestionPop(s *chatState, leadID string, bees []SwarmBee, precomputedWait ...[]string) {
	if s == nil || leadID == "" {
		return
	}
	var waiting []string
	if len(precomputedWait) > 0 {
		waiting = precomputedWait[0]
	} else {
		waiting = swarmWaitingBeeIDs(s, bees)
	}
	swarmFloatMu.Lock()
	newIDs, next := swarmNewlyWaiting(swarmQuestionPopped[leadID], waiting)
	if len(next) == 0 {
		delete(swarmQuestionPopped, leadID)
	} else {
		swarmQuestionPopped[leadID] = next
	}
	swarmFloatMu.Unlock()
	if len(newIDs) == 0 {
		return
	}
	for _, id := range newIDs {
		revealSwarmFloatForBee(leadID, id)
	}
	s.invalidate()
}

// trackSwarmAutoHide 每帧按子线程检查一次自动收起：某个子线程进入终态的那一帧记下时刻，
// 满 swarmAutoHideDelay 就把它从浮窗里收起（记进 swarmFloatDismissedBees），其余子线程照常显示；
// 最后一个子线程也收起时整个浮窗隐藏。重开应用时最先看到的一批子线程往往早就结束了（终态记账是
// 进程内状态，重启即清空），它们第一次被观察就已经是终态、也从没被看到在跑，这一帧直接收起，
// 不再重新弹出等满 5 秒。还没到点的按子线程排一个到期唤醒，
// 让浮窗在没有其它事件时也能收到下一帧。定时器只负责唤醒、不改状态，
// 避免跨帧失效的会话状态被误用。
func trackSwarmAutoHide(s *chatState, leadID string, bees []SwarmBee, precomputedWait ...[]string) {
	if s == nil || leadID == "" || len(bees) == 0 {
		return
	}
	waiting := make(map[string]bool)
	if len(precomputedWait) > 0 {
		for _, id := range precomputedWait[0] {
			waiting[id] = true
		}
	} else {
		for _, id := range swarmWaitingBeeIDs(s, bees) {
			waiting[id] = true
		}
	}

	now := time.Now()
	var hideNow []string
	var wakes []swarmAutoHideWake

	swarmFloatMu.Lock()
	dones := swarmBeeDoneSince[leadID]
	handled := swarmBeeAutoHideHandled[leadID]
	seenActive := swarmBeeSeenActive[leadID]
	keptOpen := swarmBeeKeptOpen[leadID]
	for _, h := range bees {
		id := strings.TrimSpace(h.ThreadID)
		if id == "" {
			continue
		}
		key := leadID + "|" + id
		if !swarmBeeDormant(h, waiting[id]) {
			// 又跑起来了，或在等用户回答/审批：这份计时与「已处理」标记一并作废，下次结束重新计时；
			// 被自动收起过的子线程重新运行时也放回浮窗。
			// 用户明确点开的保留同样到期：重新开始运行后按新的完成时刻重新计时。
			if keptOpen != nil {
				delete(keptOpen, id)
			}
			if seenActive == nil {
				seenActive = make(map[string]bool)
				swarmBeeSeenActive[leadID] = seenActive
			}
			seenActive[id] = true
			if handled[id] {
				delete(handled, id)
				delete(swarmFloatDismissedBees[leadID], id)
			}
			delete(dones, id)
			if t, ok := swarmAutoHideTimers[key]; ok {
				t.Stop()
				delete(swarmAutoHideTimers, key)
			}
			continue
		}
		if keptOpen[id] {
			// 用户明确点开的子线程一直留在浮窗里：不收起、不重新计时，也不排到期唤醒；
			// 只有用户关闭浮窗、编排按钮整体重开，或它重新开始运行才会解除。
			if t, ok := swarmAutoHideTimers[key]; ok {
				t.Stop()
				delete(swarmAutoHideTimers, key)
			}
			continue
		}
		if dones == nil {
			dones = make(map[string]time.Time)
			swarmBeeDoneSince[leadID] = dones
		}
		since, tracked := dones[id]
		if !tracked {
			since = now
			dones[id] = now
		}
		if handled[id] {
			if swarmFloatDismissedBees[leadID][id] {
				continue
			}
			// 自愈：handled 只应在子线程已被自动收起后保留。浮窗里还能看到它，说明标记
			// 残留（例如收起记录被别的路径清掉了），清掉标记并重新计时；最坏晚 5 秒收起，
			// 不会让已经结束的子线程永久占住浮窗。
			delete(handled, id)
			tracked = true
			dones[id] = now
			since = now
		}
		// 本次启动第一次看到它、却已经是终态，而且从头到尾没见它在跑：它在启动前就结束了，
		// 不该再弹出来等 5 秒。用户手动打开过的（自愈时已把 since 重置为 now）与强制显示的
		// 浮窗不受影响，仍然按原来的 5 秒规则处理，不会被立刻关掉。
		if !tracked && !seenActive[id] && !swarmFloatForced[leadID] {
			hideNow = append(hideNow, id)
			continue
		}
		if swarmBeeShouldAutoHide(h, waiting[id], since, now, handled[id]) {
			hideNow = append(hideNow, id)
			continue
		}
		if _, pending := swarmAutoHideTimers[key]; pending {
			continue
		}
		wakes = append(wakes, swarmAutoHideWake{key: key, wait: since.Add(swarmAutoHideDelay).Sub(now)})
	}
	swarmFloatMu.Unlock()

	if len(hideNow) > 0 {
		if swarmAutoDismissBees(leadID, bees, hideNow) {
			// 最后一个子线程也收起了：整个浮窗隐藏，同时清掉强制显示。
			dismissSwarmFloatPanel(leadID, bees)
		}
		s.invalidate()
	}

	for _, w := range wakes {
		wait := max(w.wait, 0)
		swarmFloatMu.Lock()
		if _, pending := swarmAutoHideTimers[w.key]; !pending {
			swarmAutoHideTimers[w.key] = time.AfterFunc(wait, func() {
				swarmFloatMu.Lock()
				delete(swarmAutoHideTimers, w.key)
				swarmFloatMu.Unlock()
				s.mu.Lock()
				closed := s.closed
				s.mu.Unlock()
				if closed {
					return
				}
				// 到期只唤醒一帧：是否收起由这一帧按当时的子线程状态重新判定。
				s.invalidate()
			})
		}
		swarmFloatMu.Unlock()
	}
}

// openSwarmFloatForBee 只放回被点击的那一个子线程并选中它（时间轴胶囊的「打开浮窗」）。
// 它清掉浮窗本身的关闭记录、标记强制显示，但只把这一条子线程从关闭集合里摘掉，
// 其它已经收起的子线程保持收起，也不重排它们的计时；被点开的那一个记为已处理并放进
// swarmBeeKeptOpen，之后不再自动收起，直到用户关闭浮窗或它重新开始运行，
// 并加一次激活脉冲高亮提示点击已生效。
// 没有具体子线程时退回整体打开（undismissSwarmFloatPanel），保持旧行为。
func openSwarmFloatForBee(leadID, threadID string) {
	if leadID == "" {
		return
	}
	id := strings.TrimSpace(threadID)
	if id == "" {
		undismissSwarmFloatPanel(leadID)
		return
	}
	swarmFloatMu.Lock()
	delete(swarmFloatPanelDismissed, leadID)
	swarmFloatForced[leadID] = true
	// 重新手动打开时重新计数，这样之后真的有子代理全结束才自动关闭。
	delete(swarmFloatForcedHadBees, leadID)
	if set := swarmFloatDismissedBees[leadID]; set != nil {
		delete(set, id)
	}
	handled := swarmBeeAutoHideHandled[leadID]
	if handled == nil {
		handled = make(map[string]bool)
		swarmBeeAutoHideHandled[leadID] = handled
	}
	handled[id] = true
	// handled 只是本帧的「不收起」标记，可能被可见性自愈清掉；keptOpen 才是持久的用户意图。
	kept := swarmBeeKeptOpen[leadID]
	if kept == nil {
		kept = make(map[string]bool)
		swarmBeeKeptOpen[leadID] = kept
	}
	kept[id] = true
	swarmSelectedBeeID[leadID] = id
	// 记录激活脉冲：选中后的 1.5 秒内，该子线程的卡片边框用强调色提示点击已生效。
	swarmPulseTime[leadID+"|"+id] = time.Now()
	swarmFloatMu.Unlock()
}

// revealSwarmFloatForBee 由程序自动弹出浮窗并选中指定子线程（子线程新进入等用户回答或审批）。
// 它不等于用户手动打开（openSwarmFloatForBee）：不设强制显示，也不动其它子线程的关闭记录与「已处理」标记，
// 只把这一个子线程单独放回浮窗。这样刚弹出的请求一定看得见，而其它已经结束的子线程仍按各自 5 秒规则自动收起。
func revealSwarmFloatForBee(leadID, threadID string) {
	if leadID == "" {
		return
	}
	id := strings.TrimSpace(threadID)
	if id == "" {
		return
	}
	swarmFloatMu.Lock()
	delete(swarmFloatPanelDismissed, leadID)
	if set := swarmFloatDismissedBees[leadID]; set != nil {
		delete(set, id)
	}
	if handled := swarmBeeAutoHideHandled[leadID]; handled != nil {
		delete(handled, id)
	}
	swarmSelectedBeeID[leadID] = id
	// 记录激活脉冲：选中后的 1.5 秒内，该子线程的卡片边框用强调色提示点击已生效。
	swarmPulseTime[leadID+"|"+id] = time.Now()
	swarmFloatMu.Unlock()
}

// swarmAutoPopsFor 返回当前生效的“自动分散展示”状态：内存覆盖优先，其次读设置。
func swarmAutoPopsFor(s *chatState, leadID string) bool {
	autoPops, _, _ := swarmFloatPrefs(s)
	swarmFloatMu.Lock()
	override, ok := swarmAutoPopsOverride[leadID]
	swarmFloatMu.Unlock()
	if ok {
		return override
	}
	return autoPops
}

// toggleSwarmAutoPops 在内存里切换聚合与分散展示，不改设置。
func toggleSwarmAutoPops(s *chatState, leadID string) {
	next := !swarmAutoPopsFor(s, leadID)
	swarmFloatMu.Lock()
	swarmAutoPopsOverride[leadID] = next
	swarmFloatMu.Unlock()
	s.invalidate()
}

// getActiveSwarmBees 获取当前会话中所有的有效 Swarm bee。
func getActiveSwarmBees(st SwarmState) []SwarmBee {
	if !st.Available || st.IsBee || len(st.Bees) == 0 {
		return nil
	}
	var res []SwarmBee
	for _, h := range st.Bees {
		if strings.TrimSpace(h.ThreadID) == "" && strings.TrimSpace(h.Name) == "" {
			continue
		}
		res = append(res, h)
	}
	return res
}

// getSelectedSwarmBee 决定团队面板应当呈现的 bee。
// 优先级：用户手动切换选中的 bee -> 最新正在工作的 bee -> 最新的 bee。
func getSelectedSwarmBee(leadID string, bees []SwarmBee) *SwarmBee {
	if len(bees) == 0 {
		return nil
	}
	swarmFloatMu.Lock()
	selID := swarmSelectedBeeID[leadID]
	swarmFloatMu.Unlock()

	if selID != "" {
		for i := range bees {
			if bees[i].ThreadID == selID {
				return &bees[i]
			}
		}
	}
	for i := len(bees) - 1; i >= 0; i-- {
		if bees[i].Status == "running" {
			return &bees[i]
		}
	}
	return &bees[len(bees)-1]
}

// swarmFloatFrame 是一帧里的面板场景：聊天列让出的宽度与每个面板的位置。
type swarmFloatFrame struct {
	leadID     string
	bees       []SwarmBee
	mainLeft   float32
	scene      swarmScene
	stackOrder []panelID
}

// swarmFloatPrefs 读取自动弹出设置和拖拽设定的尺寸。
func swarmFloatPrefs(s *chatState) (autoPops bool, prefW, prefH float32) {
	if st := getChatSettings(s); st != nil {
		h := st.cfg.Orchestration
		return h.AutoPopsBees, h.PanelWidth, h.PanelHeight
	}
	return false, 0, 0
}

// swarmFloatFrameFor 由窗口尺寸、侧边栏与右侧分栏状态、面板状态算出本帧的面板场景；
// 主区宽度与聊天区共用 chatAreaWidth 的口径（左右分栏都扣掉），
// 主聊天区与面板渲染各调用一次，结果只取决于这些输入，两处自然一致。
func swarmFloatFrameFor(c *ui.Context, s *chatState) swarmFloatFrame {
	var f swarmFloatFrame
	if s == nil {
		return f
	}
	s.mu.Lock()
	selectedID := s.selectedThreadID
	sidebarW := s.sidebarWidth
	sidebarHidden := s.sidebarCollapsed
	composerH := s.composerAreaH
	s.mu.Unlock()
	if selectedID == "" {
		return f
	}
	view := swarmStateViewFor(s)
	if view == nil {
		return f
	}
	leadID := view.leadID
	// 已经自动收起的子线程不再出现在浮窗里；全部收起时按“没有子代理”处理，浮窗随之隐藏。
	// 子线程列表直接读派生视图，不再每帧拷贝整份 SwarmState 再重新过滤一遍。
	bees := swarmVisibleBees(leadID, view.active)
	forced := settleSwarmFloatForced(leadID, len(bees))
	if len(bees) == 0 && !forced {
		return f
	}

	f.leadID = leadID
	f.bees = bees
	showsTeam := forced || !isSwarmFloatPanelDismissed(f.leadID, bees)
	autoPops, prefW, prefH := swarmFloatPrefs(s)
	swarmFloatMu.Lock()
	if override, ok := swarmAutoPopsOverride[f.leadID]; ok {
		autoPops = override
	}
	swarmFloatMu.Unlock()
	_, winH := c.Size()
	// 底部控制台占住窗口底部一段高度，浮窗的停靠区要让开它：拖拽、缩放、吸角都基于这份可用高度。
	paneH := max(0, winH-terminalDockOccupiedHeight(s, winH))

	swarmFloatMu.Lock()
	if live, ok := swarmLiveSizes[f.leadID]; ok {
		prefW, prefH = live.size.w, live.size.h
	}
	teamDock := swarmTeamDock[f.leadID]
	autoDocks := maps.Clone(swarmAutoDocks[f.leadID])
	autoHeld := maps.Clone(swarmAutoHeld[f.leadID])
	f.stackOrder = slices.Clone(swarmStackOrder[f.leadID])
	swarmFloatMu.Unlock()

	if !sidebarHidden {
		f.mainLeft = sidebarW + 1
	}
	f.scene = computeSwarmScene(sceneInput{
		paneW:         chatAreaWidth(c, s),
		paneH:         paneH,
		composerAreaH: composerH,
		contentMaxW:   timelineContentMaxWidth(s),
		prefW:         prefW,
		prefH:         prefH,
		bees:          bees,
		autoPops:      autoPops,
		showsTeam:     showsTeam,
		forcesTeam:    forced,
		teamDock:      teamDock,
		autoDocks:     autoDocks,
		autoHeld:      autoHeld,
		stackOrder:    f.stackOrder,
	})
	return f
}

// swarmFloatReserve 是面板从聊天列两侧让出的宽度。
func swarmFloatReserve(c *ui.Context, s *chatState) panelReserve {
	return swarmFloatFrameFor(c, s).scene.reserve
}

// renderSwarmBeeFloats 渲染子代理面板：团队面板与（开启自动弹出时）各子代理自己的面板。
func renderSwarmBeeFloats(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	hp := getSwarmPanelState(s)
	if hp == nil {
		return
	}
	swarmPanelMu.Lock()
	panelOpen := hp.open
	swarmPanelMu.Unlock()

	// 子线程列表与运行状态都读派生视图（状态变化时才算一次），不再每帧拷贝整份 SwarmState。
	view := swarmStateViewFor(s)
	leadID := ""
	var activeBees []SwarmBee
	if view != nil {
		leadID = view.leadID
		activeBees = view.active
	}
	// 等待集合同一帧只算一次：自动收起与提问弹出共用。
	waiting := swarmWaitingBeeIDs(s, activeBees)
	// 每个子线程各自进入终态满 5 秒后从浮窗里收起（每帧检查一次，到点后重绘一次）。
	trackSwarmAutoHide(s, leadID, activeBees, waiting)
	// 子线程开始提问时立即弹出浮窗：放在渲染判定之前，浮窗被关掉时也要能弹回来。
	trackSwarmQuestionPop(s, leadID, activeBees, waiting)

	f := swarmFloatFrameFor(c, s)
	if f.scene.has() {
		ui.Overlay(c, func() {
			if f.scene.showsTeam {
				renderSwarmPanelCard(c, s, f, teamPanelID, f.scene.teamBees)
			}
			for _, h := range f.scene.autoBees {
				renderSwarmPanelCard(c, s, f, autoPanelID(h.ThreadID), []SwarmBee{h})
			}
		})
	}
	sweepEmbeddedBees(s)

	// 若用户手动点击 SwarmHeaderButton 打开了汇总面板，在浮窗之外同时展示汇总面板
	if panelOpen {
		renderSwarmPanel(c, s)
	}
}

// renderSwarmPanelCard 渲染一个面板：顶栏、对话、嵌入的输入框与缩放抓手。
// 没有子代理时只可能是用户强制显示的空团队面板：顶栏写“编排”，主体是居中的空态，没有输入框。
func renderSwarmPanelCard(c *ui.Context, s *chatState, f swarmFloatFrame, id panelID, bees []SwarmBee) {
	slot, ok := f.scene.slots[id]
	if !ok {
		return
	}
	if len(bees) == 0 && id != teamPanelID {
		return
	}
	rest, _ := f.scene.restOrigin(id)
	size := f.scene.layout.size()
	key := f.leadID + "|" + string(id)

	swarmFloatMu.Lock()
	dragAt, dragging := swarmDragPos[key]
	swarmFloatMu.Unlock()
	pos := rest
	if dragging {
		pos = dragAt
	}

	// 当前展示的子代理：团队面板取选中的，独立面板只有自己；空团队面板没有当前子代理。
	var cur *SwarmBee
	if len(bees) > 0 {
		cur = &bees[0]
		if id == teamPanelID {
			cur = getSelectedSwarmBee(f.leadID, bees)
		}
	}
	// 已结束（完成/失败/已停止）的子代理：卡片换成该子线程人设色的外发光 + 同色描边，
	// 让用户一眼看出哪个子线程结束了；运行中的卡片维持原样。
	curName := ""
	done := false
	if cur != nil {
		curName = cur.Name
		done = swarmBeeDone(*cur, swarmBeeWaiting(s, *cur))
	}

	// 刚被打开或选中的子代理在脉冲期内用强调色边框，给点击一个直接的可视反馈。
	borderColor := ThemeBorder(c)
	borderWidth := float32(1)
	if done {
		borderColor = swarmDoneGlowColor(curName, 0.8)
		borderWidth = 1.5
	}
	if cur != nil && swarmBeePulsing(s, f.leadID, cur.ThreadID) {
		borderColor = ThemeAccent(c)
	}

	card := applyElevation(c, ui.Column(c.Key("swarm-panel-"+string(id))).
		Absolute().
		Width(size.w).
		Height(size.h).
		Radius(RadiusLG).
		Background(ThemeSurface(c)).
		BorderWidth(borderWidth).
		BorderColor(borderColor), ElevFloat)
	if done {
		// mygo 的 Shadow 可以叠加：保留原有投影，再补一圈人设色发光。
		card.Shadow(0, 0, 22, 2, swarmDoneGlowColor(curName, 0.6))
	}
	card.Children(func() {
		if len(bees) == 0 || cur == nil {
			renderSwarmEmptyTeamPanel(c, s, f)
			renderSwarmGrips(c, s, f, key, slot, size)
			return
		}
		child := beeChatState(s, *cur)
		renderSwarmPanelStrip(c, s, f, id, key, rest, *cur, bees, child)
		// 以子代理 ID 为键的容器：切换所选子代理时，输入框与列表的内部状态不串到另一个子代理。
		ui.Column(c.Key("swarm-bee-" + cur.ThreadID)).Grow(1).MinHeight(0).FillWidth().Children(func() {
			ui.Column(c.Key("swarm-body")).Grow(1).MinHeight(0).FillWidth().Clip().Children(func() {
				renderBeeTimeline(c, child, *cur)
			})
			// 子线程的提问与审批卡片停靠在嵌入输入框的正上方。
			renderApproval(c, child)
			renderComposer(c, child)
		})
		renderSwarmGrips(c, s, f, key, slot, size)
	})

	// 拖动中的面板紧跟指针；松手后以短动画滑向停靠位置。
	dur := swarmRestDuration
	if dragging {
		dur = time.Millisecond
	}
	x := card.AnimateWith("swarm-panel-x", f.mainLeft+pos.x, animateDuration(c, dur), ui.EaseOut)
	y := card.AnimateWith("swarm-panel-y", pos.y, animateDuration(c, dur), ui.EaseOut)
	card.Left(x).Top(y)
}

// renderSwarmEmptyTeamPanel 渲染强制显示时的空团队面板：顶栏写“编排”，主体居中提示暂无子代理。
func renderSwarmEmptyTeamPanel(c *ui.Context, s *chatState, f swarmFloatFrame) {
	ui.Row(c.Key("swarm-strip")).FillWidth().AlignItems(ui.Center).Padding(Space3, Space4).Gap(Space3).Children(func() {
		ui.Icon(c, IconSwarm()).Size(IconSM, IconSM).TextColor(ThemePrimaryText(c).Alpha(0.85))
		ui.Text(c, "编排").FontSize(TypeFootnote).FontWeight(600).TextColor(ThemePrimaryText(c)).Grow(1)
		closeBtn := ui.ButtonBase(c.Key("swarm-strip-close")).
			Size(ControlSM, ControlSM).
			Radius(RadiusPill).
			Background(ThemeHover(c)).
			BorderWidth(1).
			BorderColor(ThemeBorder(c)).
			Justify(ui.Center).
			AlignItems(ui.Center).
			Label("关闭编排面板").
			Role(ui.RoleButton).
			Tooltip("关闭面板")
		if closeBtn.Hovered() {
			closeBtn.Background(ThemeHover(c).Alpha(0.85))
		}
		if closeBtn.Clicked() {
			dismissSwarmFloatPanel(f.leadID, f.bees)
			s.invalidate()
		}
		closeBtn.Children(func() {
			ui.Icon(c, IconX()).Size(11, 11).TextColor(ThemeSecondaryText(c))
		})
	})
	ui.Column(c.Key("swarm-empty-body")).Grow(1).FillWidth().Justify(ui.Center).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "暂无子代理").FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
	})
}

// renderSwarmPanelStrip 渲染面板顶栏：子代理计数菜单（团队面板）、可拖动的当前子代理胶囊、弹出与关闭。
func renderSwarmPanelStrip(c *ui.Context, s *chatState, f swarmFloatFrame, id panelID, key string, rest panelPoint, cur SwarmBee, bees []SwarmBee, child *chatState) {
	isTeam := id == teamPanelID
	running := 0
	for _, h := range bees {
		if h.Status == "running" {
			running++
		}
	}

	ui.Row(c.Key("swarm-strip")).FillWidth().AlignItems(ui.Center).Padding(Space3, Space4).Gap(Space3).Children(func() {
		if isTeam {
			open := swarmMenuFlag(key)
			btn := ui.ButtonBase(c.Key("swarm-strip-count")).
				Radius(RadiusXL).
				Padding(Space1, Space3).
				Background(ThemeHover(c)).
				BorderWidth(1).
				BorderColor(ThemeBorder(c)).
				Label("子代理列表，共 " + strconv.Itoa(len(bees)) + " 个").
				Role(ui.RoleMenuButton).
				Tooltip("切换正在查看的子代理")
			if btn.Hovered() {
				btn.Background(ThemeHover(c).Alpha(0.85))
			}
			// 菜单打开时点按钮由 PopoverBase 负责关闭，这里不能再取反。
			if btn.Clicked() && !*open {
				*open = true
				s.invalidate()
			}
			btn.Children(func() {
				ui.Row(c.Key("swarm-strip-count-row")).AlignItems(ui.Center).Gap(Space1).Children(func() {
					ui.Icon(c, IconSwarm()).Size(IconSM, IconSM).TextColor(ThemePrimaryText(c).Alpha(0.85))
					ui.Text(c, strconv.Itoa(len(bees))).FontSize(TypeFootnote).FontWeight(600).TextColor(ThemePrimaryText(c))
					if running > 0 {
						renderRunningGlyph(c, "bee-float-run-strip-"+key, 9, ThemeAccent(c))
					}
					ui.Icon(c, IconChevronDown()).Size(10, 10).TextColor(ThemeSecondaryText(c))
				})
			})
			anchoredMenu(c, "swarm-bees-menu-"+key, btn, open, nil, 240, 320, 360, func() {
				renderBeesMenu(c, s, f.leadID, bees, cur.ThreadID, running, open)
			})
		}

		persona := findSwarmPersona(cur.Name)
		childRunning := beeWindowChildRunning(child, cur.ThreadID)
		background := beeBackgroundTaskCount(child, cur.ThreadID)
		statusText := beeStatusText(cur.Status, childRunning, background)
		statusColor := ThemeSecondaryText(c)
		// 等待后台时用强调色，和仍在工作的 bee 区分开。
		if beeWaitingBackground(cur.Status, childRunning, background) {
			statusColor = ThemeAccent(c)
		}
		handle := ui.Row(c.Key("swarm-strip-handle")).
			Grow(1).
			AlignItems(ui.Center).
			Gap(Space2).
			Padding(Space1, Space3).
			Radius(RadiusXL).
			Background(ThemeHover(c)).
			BorderWidth(1).
			BorderColor(ThemeBorder(c)).
			Cursor(ui.CursorGrab).
			Label(cur.Name + " 面板，可拖动").
			Role(ui.RoleButton)
		// 已结束的子代理在顶栏胶囊上补一圈同色细描边与小发光，与卡片的外发光呼应。
		if swarmBeeDone(cur, swarmBeeWaiting(s, cur)) {
			glow := swarmDoneGlowColor(cur.Name, 0.5)
			handle.BorderColor(swarmDoneGlowColor(cur.Name, 0.7)).Shadow(0, 0, 10, 0, glow)
		}
		handle.Children(func() {
			renderSwarmGlyph(c, "strip-"+cur.ThreadID, cur, 18)
			ui.Text(c, cur.Name).FontSize(TypeFootnote).FontWeight(600).TextColor(persona.Color)
			// 顶栏胶囊补上档位徽章，与团队卡片里的档位展示保持一致。
			if cur.Tier != "" {
				renderSwarmTierChip(c, "strip-tier-"+cur.ThreadID, swarmTierName(s, cur.Tier), cur.Tier, cur.ReadOnly)
			}
			ui.Text(c, statusText).FontSize(TypeCaption).FontWeight(500).TextColor(statusColor)
			if d := formatSwarmDuration(cur.StartedAt, cur.FinishedAt); d != "" {
				ui.Text(c, d).FontSize(TypeCaption).TextColor(ThemeSecondaryText(c).Alpha(0.8))
			}
		})
		trackSwarmPanelDrag(s, f, id, key, rest, handle)

		pop := ui.ButtonBase(c.Key("swarm-strip-pop")).Radius(RadiusXS).Padding(Space1).Label("在独立窗口打开 " + cur.Name).Role(ui.RoleButton).Tooltip("在独立会话窗口打开此子代理")
		if pop.Hovered() {
			pop.Background(ThemeHover(c))
		}
		if pop.Clicked() {
			s.popOutSwarmBee(cur.ThreadID)
		}
		pop.Children(func() {
			ui.Icon(c, IconSquareArrowOutUpRight()).Size(12, 12).TextColor(ThemeSecondaryText(c))
		})

		tip := "关闭面板（有新子代理派发时自动重新出现）"
		label := "关闭面板"
		icon := IconX()
		if !isTeam {
			tip = "放回团队面板"
			label = "把 " + cur.Name + " 放回团队面板"
			icon = IconArrowLeft()
		}
		closeBtn := ui.ButtonBase(c.Key("swarm-strip-close")).
			Size(ControlSM, ControlSM).
			Radius(RadiusPill).
			Background(ThemeHover(c)).
			BorderWidth(1).
			BorderColor(ThemeBorder(c)).
			Justify(ui.Center).
			AlignItems(ui.Center).
			Label(label).
			Role(ui.RoleButton).
			Tooltip(tip)
		if closeBtn.Hovered() {
			closeBtn.Background(ThemeHover(c).Alpha(0.85))
		}
		if closeBtn.Clicked() {
			if isTeam {
				dismissSwarmFloatPanel(f.leadID, f.bees)
			} else {
				swarmFloatMu.Lock()
				held := swarmAutoHeld[f.leadID]
				if held == nil {
					held = make(map[string]bool)
					swarmAutoHeld[f.leadID] = held
				}
				held[cur.ThreadID] = true
				swarmSelectedBeeID[f.leadID] = cur.ThreadID
				swarmFloatMu.Unlock()
			}
			s.invalidate()
		}
		closeBtn.Children(func() {
			ui.Icon(c, icon).Size(11, 11).TextColor(ThemeSecondaryText(c))
		})
	})
}

// trackSwarmPanelDrag 让顶栏胶囊充当拖拽手柄：拖动时面板跟随指针，松手后换到最近的角并排序。
func trackSwarmPanelDrag(s *chatState, f swarmFloatFrame, id panelID, key string, rest panelPoint, handle ui.Element) {
	dx, dy, held := handle.Dragged()
	swarmFloatMu.Lock()
	at, dragging := swarmDragPos[key]
	swarmFloatMu.Unlock()

	if held && (dragging || dx != 0 || dy != 0) {
		if !dragging {
			at = rest
		}
		at = f.scene.layout.clamped(panelPoint{at.x + dx, at.y + dy})
		swarmFloatMu.Lock()
		swarmDragPos[key] = at
		swarmFloatMu.Unlock()
		handle.Cursor(ui.CursorGrabbing)
		s.invalidate()
		return
	}
	if held || !dragging {
		return
	}

	// 松手：定位到最近的角，并让出/占用该侧的槽位。
	slot := f.scene.slots[id]
	corner := f.scene.layout.dockCorner(at, slot.corner, f.scene.docks)
	corners, order := reorderedPanels(f.scene, id, at, corner, f.stackOrder)
	swarmFloatMu.Lock()
	delete(swarmDragPos, key)
	for pid, cn := range corners {
		if pid == teamPanelID {
			swarmTeamDock[f.leadID] = cn
		} else if beeID, ok := pid.beeID(); ok {
			docks := swarmAutoDocks[f.leadID]
			if docks == nil {
				docks = make(map[string]panelCorner)
				swarmAutoDocks[f.leadID] = docks
			}
			docks[beeID] = cn
		}
	}
	swarmStackOrder[f.leadID] = order
	swarmFloatMu.Unlock()
	s.invalidate()
}

// renderSwarmGrips 渲染缩放抓手：面向聊天列的侧边、面向窗口中线的上/下边，以及它们相交的角。
// 面板的停靠位置每帧由新尺寸重算，所以只要改尺寸，它就从所在的角向外长。
func renderSwarmGrips(c *ui.Context, s *chatState, f swarmFloatFrame, key string, slot panelSlot, size panelSize) {
	trailing := slot.corner.side() == panelSideTrailing
	bottom := !slot.corner.isTop()
	signW, signH := float32(1), float32(1)
	if trailing {
		signW = -1
	}
	if bottom {
		signH = -1
	}

	sideX := size.w - swarmGripThick
	if trailing {
		sideX = 0
	}
	vertY := size.h - swarmGripThick
	if bottom {
		vertY = 0
	}
	cornerX := size.w - swarmGripCorner
	if trailing {
		cornerX = 0
	}
	cornerY := size.h - swarmGripCorner
	if bottom {
		cornerY = 0
	}
	diagonal := ui.CursorResizeNESW
	if trailing == bottom {
		diagonal = ui.CursorResizeNWSE
	}

	side := ui.Box(c.Key("swarm-grip-side")).Absolute().Left(sideX).Top(swarmGripInset).Width(swarmGripThick).Height(max(0, size.h-2*swarmGripInset)).Cursor(ui.CursorResizeEW)
	trackSwarmResize(s, f, key+"|side", side, signW, 0, size)
	vert := ui.Box(c.Key("swarm-grip-vert")).Absolute().Left(swarmGripInset).Top(vertY).Width(max(0, size.w-2*swarmGripInset)).Height(swarmGripThick).Cursor(ui.CursorResizeNS)
	trackSwarmResize(s, f, key+"|vert", vert, 0, signH, size)
	corner := ui.Box(c.Key("swarm-grip-corner")).Absolute().Left(cornerX).Top(cornerY).Width(swarmGripCorner).Height(swarmGripCorner).Cursor(diagonal)
	trackSwarmResize(s, f, key+"|corner", corner, signW, signH, size)
	if corner.DoubleClicked() {
		persistSwarmPanelSize(s, f.leadID, panelSize{})
	}
}

// trackSwarmResize 把抓手上的拖动转成面板尺寸：拖动中只改内存里的临时尺寸，松手才写入配置。
func trackSwarmResize(s *chatState, f swarmFloatFrame, owner string, grip ui.Element, signW, signH float32, size panelSize) {
	dx, dy, held := grip.Dragged()
	if held && (dx != 0 || dy != 0) {
		swarmFloatMu.Lock()
		base := size
		if live, ok := swarmLiveSizes[f.leadID]; ok {
			base = live.size
		}
		next := f.scene.layout.fitted(panelSize{base.w + dx*signW, base.h + dy*signH})
		swarmLiveSizes[f.leadID] = swarmLiveResize{size: next, owner: owner}
		swarmFloatMu.Unlock()
		s.invalidate()
		return
	}
	if held {
		return
	}
	swarmFloatMu.Lock()
	live, ok := swarmLiveSizes[f.leadID]
	swarmFloatMu.Unlock()
	if ok && live.owner == owner {
		persistSwarmPanelSize(s, f.leadID, live.size)
	}
}

// persistSwarmPanelSize 把拖拽设定的面板尺寸写入配置；零尺寸表示复位为自动。
// 没有设置状态（如未装配设置的窗口）时临时尺寸继续留在内存里生效。
func persistSwarmPanelSize(s *chatState, leadID string, size panelSize) {
	st := getChatSettings(s)
	if st == nil {
		if size == (panelSize{}) {
			swarmFloatMu.Lock()
			delete(swarmLiveSizes, leadID)
			swarmFloatMu.Unlock()
		}
		return
	}
	// 改 cfg 要同时持有 mu 与 cfgMu（顺序 mu → cfgMu），save 与 invalidate 在两把锁之外调。
	st.mu.Lock()
	st.cfgMu.Lock()
	st.cfg.Orchestration.PanelWidth = size.w
	st.cfg.Orchestration.PanelHeight = size.h
	st.cfgMu.Unlock()
	st.mu.Unlock()
	st.save()
	swarmFloatMu.Lock()
	delete(swarmLiveSizes, leadID)
	swarmFloatMu.Unlock()
	s.invalidate()
}

// renderBeesMenu 渲染团队面板的子代理菜单：切换查看对象、逐个停止与打开独立窗口，
// 以及聚合与分散的临时切换和清理已结束。
// 面板自身不透明（与 Git 下拉菜单同一套圆角、描边与阴影），不依赖 PopoverBase 的透明底。
func renderBeesMenu(c *ui.Context, s *chatState, leadID string, bees []SwarmBee, currentID string, running int, open *bool) {
	card := popoverCardShadow(popoverCard(c, ui.Column(c.Key("swarm-bees-menu-col")).FillWidth().Padding(Space2).Gap(Space1)))
	card.Children(func() {
		for _, h := range bees {
			bee := h
			row := ui.ButtonBase(c.Key("swarm-menu-row-"+bee.ThreadID)).FillWidth().Radius(RadiusSM).Padding(Space1, Space3).
				Label("查看 " + bee.Name + "，状态：" + swarmStatusLabel(bee.Status)).
				Role(ui.RoleButton)
			if bee.ThreadID == currentID {
				row.Background(ThemeSelected(c))
			} else if row.Hovered() {
				row.Background(ThemeHover(c))
			}
			if row.Clicked() {
				swarmFloatMu.Lock()
				swarmSelectedBeeID[leadID] = bee.ThreadID
				swarmFloatMu.Unlock()
				*open = false
				s.invalidate()
			}
			row.Children(func() {
				ui.Row(c.Key("swarm-menu-inner-" + bee.ThreadID)).FillWidth().AlignItems(ui.Center).Gap(Space3).Children(func() {
					renderSwarmGlyph(c, "menu-"+bee.ThreadID, bee, IconLG)
					ui.Column(c.Key("swarm-menu-meta-" + bee.ThreadID)).Grow(1).Gap(Space0).Children(func() {
						ui.Row(c.Key("swarm-menu-title-" + bee.ThreadID)).AlignItems(ui.Center).Gap(Space2).Children(func() {
							ui.Text(c, bee.Name).FontSize(TypeFootnote).FontWeight(600).TextColor(ThemePrimaryText(c))
							// 菜单每行同样带档位徽章，key 带 ThreadID 保证各子代理之间唯一。
							if bee.Tier != "" {
								renderSwarmTierChip(c, "menu-tier-"+bee.ThreadID, swarmTierName(s, bee.Tier), bee.Tier, bee.ReadOnly)
							}
							ui.Text(c, swarmStatusLabel(bee.Status)).FontSize(TypeCaption).TextColor(swarmStatusColor(c, bee.Status))
						})
						if bee.Task != "" {
							ui.Text(c, bee.Task).FontSize(TypeCaption).TextColor(ThemeSecondaryText(c))
						}
					})
					if bee.Status == "running" {
						stop := ui.ButtonBase(c.Key("swarm-menu-stop-"+bee.ThreadID)).Radius(RadiusXS).Padding(Space0, Space2).
							Label("停止 " + bee.Name).
							Role(ui.RoleButton).
							Tooltip("停止此子代理")
						if stop.Clicked() {
							s.stopSwarmBee(bee.ThreadID)
						}
						stop.Children(func() {
							ui.Icon(c, IconSquare()).Size(8, 8).TextColor(ThemeDanger(c))
						})
					}
					pop := ui.ButtonBase(c.Key("swarm-menu-pop-"+bee.ThreadID)).Radius(RadiusXS).Padding(Space0, Space1).
						Label("在独立窗口打开 " + bee.Name).
						Role(ui.RoleButton).
						Tooltip("在独立窗口中打开")
					if pop.Hovered() {
						pop.Background(ThemeHover(c))
					}
					if pop.Clicked() {
						s.popOutSwarmBee(bee.ThreadID)
						*open = false
						s.invalidate()
					}
					pop.Children(func() {
						ui.Icon(c, IconSquareArrowOutUpRight()).Size(11, 11).TextColor(ThemeSecondaryText(c))
					})
				})
			})
		}

		ui.Box(c.Key("swarm-menu-divider")).FillWidth().Height(Hairline).Background(ThemeBorder(c)).Margin(Space0, 0)
		ui.Row(c.Key("swarm-menu-actions")).FillWidth().AlignItems(ui.Center).Gap(Space2).Children(func() {
			if running > 1 {
				all := ui.ButtonBase(c.Key("swarm-menu-stop-all")).Radius(RadiusXS).Padding(Space1, Space2).
					Label("停止全部子代理").
					Role(ui.RoleButton)
				if all.Clicked() {
					s.stopAllSwarmBees()
					*open = false
				}
				all.Children(func() {
					ui.Text(c, "停止全部").FontSize(TypeCaption).TextColor(ThemeDanger(c))
				})
			}
			ui.Spacer(c)
			clear := ui.ButtonBase(c.Key("swarm-menu-clear")).Radius(RadiusXS).Padding(Space1, Space2).
				Label("清理已结束的子代理").
				Role(ui.RoleButton)
			if clear.Clicked() {
				s.clearFinishedSwarmBees()
				*open = false
			}
			clear.Children(func() {
				ui.Text(c, "清理已结束").FontSize(TypeCaption).TextColor(ThemeSecondaryText(c))
			})
		})

		// 聚合与分散的临时切换：只改内存里的覆盖，不写配置。
		ui.Box(c.Key("swarm-menu-divider-layout")).FillWidth().Height(Hairline).Background(ThemeBorder(c)).Margin(Space0, 0)
		scatterLabel := "全部分散展示"
		if swarmAutoPopsFor(s, leadID) {
			scatterLabel = "全部聚合"
		}
		layoutToggle := ui.ButtonBase(c.Key("swarm-menu-toggle-scatter")).FillWidth().Radius(RadiusSM).Padding(Space1, Space3).
			Label(scatterLabel).
			Role(ui.RoleButton)
		if layoutToggle.Hovered() {
			layoutToggle.Background(ThemeHover(c))
		}
		if layoutToggle.Clicked() {
			toggleSwarmAutoPops(s, leadID)
			*open = false
			s.invalidate()
		}
		layoutToggle.Children(func() {
			ui.Text(c, scatterLabel).FontSize(TypeCaption).TextColor(ThemePrimaryText(c))
		})
	})
}

// beeTimelineViewKey 标识子代理时间线视图的本地状态：视图被销毁重建后它会回到初始值。
type beeTimelineViewKey struct{}

// beeViewGeneration 递增并返回本次渲染所在的视图代数：视图被销毁重建后回到 1。
// 元素状态随元素树一起生死，代数因此能反映“视图是不是刚重建”。
func beeViewGeneration(c *ui.Context, view ui.Element) int {
	gen := ui.Local(view, beeTimelineViewKey{}, func() int { return 0 })
	*gen++
	return *gen
}

// renderBeeTimeline 渲染面板的对话视图，复用主会话的时间线行渲染。
func renderBeeTimeline(c *ui.Context, s *chatState, bee SwarmBee) {
	ensureChatMarkdown(c, s)

	// 切换「自动折叠思考内容」后工具组与回合折叠的默认开合要跟着变，即使没有新消息也需重建。
	groupsOpenChanged := timelineSettingsChanged(s)

	s.mu.Lock()
	selectedID := s.selectedThreadID
	expandedRev := s.expandedRev
	needRebuild := s.messagesRev != s.timelineCacheRev || s.timelineCacheMsgs == nil || expandedRev != s.timelineCacheExpandedRev || selectedID != s.timelineSnapshotThreadID || groupsOpenChanged
	rev := s.messagesRev
	var messages []Message
	if needRebuild {
		messages = slices.Clone(s.messages)
	}
	curUsage := s.threadUsage
	if s.scrollToEnd && len(s.messages) > 0 {
		s.scrollToEnd = false
		s.chatListState.ScrollToEnd()
	}
	expanded := cloneBoolMap(s.expandedTool)
	s.mu.Unlock()

	if needRebuild {
		items, fps := s.rebuildTimelineIncremental(selectedID, messages, expanded, liveToolGroupID(messages, bee.Status == "running"))
		s.mu.Lock()
		s.timelineCacheRev = rev
		s.timelineCacheMsgs = messages
		s.timelineCacheItems = items
		s.timelineCacheExpandedRev = expandedRev
		s.timelineSnapshotThreadID = selectedID
		s.timelineSnapshotMsgs = fps
		s.timelineSnapshotExpanded = cloneBoolMap(expanded)
		s.mu.Unlock()
	} else {
		messages = s.timelineCacheMsgs
	}

	if len(messages) == 0 {
		ui.Column(c.Key("swarm-timeline-empty")).Grow(1).FillWidth().Justify(ui.Center).AlignItems(ui.Center).Gap(Space2).Children(func() {
			if bee.Status == "running" {
				renderRunningGlyph(c, "bee-float-run-timeline-empty-"+bee.ThreadID, 14, ThemeAccent(c))
				ui.Text(c, "正在执行任务…").FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
			} else {
				ui.Text(c, "暂无记录").FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
			}
		})
		return
	}

	var liveID string
	if bee.Status == "running" {
		if last := messages[len(messages)-1]; last.Role == "assistant" || last.Kind == "reasoning" {
			liveID = last.ID
		}
	}
	if liveID != "" {
		s.mu.Lock()
		s.lastLiveID = liveID
		s.mu.Unlock()
	}
	s.mu.Lock()
	lastLiveID := s.lastLiveID
	s.mu.Unlock()

	// 时间线条目只在需要补“回复中”指示时才复制：缓存项本身只读，
	// 200 条消息的浮窗每帧整表复制一遍是最大的一笔无谓分配。
	items := s.timelineCacheItems
	if bee.Status == "running" && liveID == "" {
		items = append(slices.Clone(items), runningIndicatorItem(selectedID))
	}
	s.chatListState.Key = func(i int) any {
		if i >= 0 && i < len(items) {
			return items[i].id
		}
		return i
	}
	lastAssistantPerTurn := s.lastAssistantPerTurn()
	// 子线程仍在跑时，正在进行中的那一轮还没结束：与主会话一致，压掉它的完成块；
	// 之前的回合照常显示（bee 自己的运行状态就是这条子线程的状态）。
	turnRunning := bee.Status == "running"
	latestTurn := -1
	if turnRunning {
		latestTurn = s.maxMessagesTurn()
	}
	// 浮窗关闭重开、切换子代理时，视图会被销毁重建，元素状态随之丢失：
	// Markdown 的揭示进度也没了，而会话状态仍记着“正在逐字揭示”，
	// 重建后的视图就会把已显示过的正文从头重播一遍。代数回到 1 说明视图刚重建，
	// 此时把揭示基线重置为“已完整显示”，旧正文直接整段出现。
	root := ui.Column(c.Key("swarm-timeline-root")).Grow(1).MinHeight(0).FillWidth()
	if beeViewGeneration(c, root) == 1 {
		s.resetRevealBaseline()
	}
	root.Children(func() {
		ui.List(c.Key("swarm-timeline"), &s.chatListState, len(items), func(i int) {
			item := items[i]
			turnUsage, hasUsage := curUsage.Turns[item.turnIndex]
			isLastAssistant := itemHoldsTurnEnd(item, lastAssistantPerTurn) &&
				!turnRunSuppressesEnd(turnRunning, latestTurn, item.turnIndex)
			renderTimelineItem(c, s, item, liveID, lastLiveID, isLastAssistant, hasUsage, turnUsage, timelineItemTopGap(items, i, 12))
		}).Grow(1).MinHeight(0).Padding(Space3, Space4)
	})
}

// swarmPersonaInfo 记录 bee 的预设人设、图标与配色。
type swarmPersonaInfo struct {
	Name  string
	Asset string
	Color ui.Color
}

// swarmRosterPersonas 是 mcode 的 25 位预设 Swarm 协作者名单与配色。
var swarmRosterPersonas = []swarmPersonaInfo{
	{"Aspen", "swarm-bee-01.svg", ThemeIdentity("Aspen")},
	{"Birch", "swarm-bee-02.svg", ThemeIdentity("Birch")},
	{"Cove", "swarm-bee-03.svg", ThemeIdentity("Cove")},
	{"Dune", "swarm-bee-04.svg", ThemeIdentity("Dune")},
	{"Ember", "swarm-bee-05.svg", ThemeIdentity("Ember")},
	{"Flint", "swarm-bee-06.svg", ThemeIdentity("Flint")},
	{"Grove", "swarm-bee-07.svg", ThemeIdentity("Grove")},
	{"Heath", "swarm-bee-08.svg", ThemeIdentity("Heath")},
	{"Isle", "swarm-bee-09.svg", ThemeIdentity("Isle")},
	{"Jade", "swarm-bee-10.svg", ThemeIdentity("Jade")},
	{"Kestrel", "swarm-bee-11.svg", ThemeIdentity("Kestrel")},
	{"Larch", "swarm-bee-12.svg", ThemeIdentity("Larch")},
	{"Moss", "swarm-bee-13.svg", ThemeIdentity("Moss")},
	{"Nimbus", "swarm-bee-14.svg", ThemeIdentity("Nimbus")},
	{"Onyx", "swarm-bee-15.svg", ThemeIdentity("Onyx")},
	{"Pike", "swarm-bee-16.svg", ThemeIdentity("Pike")},
	{"Quill", "swarm-bee-17.svg", ThemeIdentity("Quill")},
	{"Reed", "swarm-bee-18.svg", ThemeIdentity("Reed")},
	{"Sage", "swarm-bee-19.svg", ThemeIdentity("Sage")},
	{"Tide", "swarm-bee-20.svg", ThemeIdentity("Tide")},
	{"Umber", "swarm-bee-21.svg", ThemeIdentity("Umber")},
	{"Vale", "swarm-bee-22.svg", ThemeIdentity("Vale")},
	{"Wren", "swarm-bee-23.svg", ThemeIdentity("Wren")},
	{"Yarrow", "swarm-bee-24.svg", ThemeIdentity("Yarrow")},
	{"Zephyr", "swarm-bee-25.svg", ThemeIdentity("Zephyr")},
}

// findSwarmPersona 根据 bee 名称匹配对应的人设，带轮次后缀（如 "Birch 2"）时归一化匹配。
func findSwarmPersona(name string) swarmPersonaInfo {
	trimmed := strings.TrimSpace(name)
	bare := trimmed
	if idx := strings.LastIndex(bare, " "); idx != -1 {
		if _, err := strconv.Atoi(bare[idx+1:]); err == nil {
			bare = strings.TrimSpace(bare[:idx])
		}
	}
	for _, p := range swarmRosterPersonas {
		if strings.EqualFold(p.Name, bare) {
			return p
		}
	}
	color := swarmAvatarColor(name)
	var h uint32
	for _, b := range []byte(name) {
		h = h*31 + uint32(b)
	}
	assetIdx := (int(h) % len(swarmRosterPersonas)) + 1
	asset := fmt.Sprintf("swarm-bee-%02d.svg", assetIdx)
	return swarmPersonaInfo{
		Name:  name,
		Asset: asset,
		Color: color,
	}
}

var (
	beeIconCacheMu sync.Mutex
	beeIconCache   = make(map[string]*ui.SVG)
)

// safePersonaIcon 安全解析并缓存 icons_assets 下的 SVG 图标，资源缺失时不 panic。
func safePersonaIcon(name string) *ui.SVG {
	beeIconCacheMu.Lock()
	defer beeIconCacheMu.Unlock()
	if icon, ok := beeIconCache[name]; ok {
		return icon
	}
	data, err := bundledAssets.ReadFile("icons_assets/" + name)
	if err != nil {
		return nil
	}
	icon, err := ui.ParseSVG(data)
	if err != nil {
		return nil
	}
	beeIconCache[name] = icon
	return icon
}

// formatSwarmDuration 格式化耗时（如“7分钟46秒”）。
func formatSwarmDuration(started time.Time, finished *time.Time) string {
	if started.IsZero() {
		return ""
	}
	end := time.Now()
	if finished != nil && !finished.IsZero() {
		end = *finished
	}
	dur := end.Sub(started)
	if dur < 0 {
		dur = 0
	}
	mins := int(dur.Minutes())
	secs := int(dur.Seconds()) % 60
	if mins > 0 {
		return fmt.Sprintf("%d分钟%d秒", mins, secs)
	}
	return fmt.Sprintf("%d秒", secs)
}

// formatSwarmShortDuration 格式化短耗时（如“7m 45s”）。
func formatSwarmShortDuration(started time.Time, finished *time.Time) string {
	if started.IsZero() {
		return ""
	}
	end := time.Now()
	if finished != nil && !finished.IsZero() {
		end = *finished
	}
	dur := end.Sub(started)
	if dur < 0 {
		dur = 0
	}
	mins := int(dur.Minutes())
	secs := int(dur.Seconds()) % 60
	if mins > 0 {
		return fmt.Sprintf("%dm %ds", mins, secs)
	}
	return fmt.Sprintf("%ds", secs)
}

// formatBeeToolSummary 归一化工具调用摘要（生成“Use Read file”“Use Edit file”）。
func formatBeeToolSummary(text string) string {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return "Use Tool"
	}
	lower := strings.ToLower(raw)
	if strings.Contains(lower, "read_file") || strings.Contains(lower, "read file") {
		return "Use Read file"
	}
	if strings.Contains(lower, "edit_file") || strings.Contains(lower, "edit file") || strings.Contains(lower, "apply_patch") {
		return "Use Edit file"
	}
	if strings.Contains(lower, "run_command") || strings.Contains(lower, "command") {
		return "Use Run command"
	}
	if strings.Contains(lower, "search_web") || strings.Contains(lower, "search") {
		return "Use Search"
	}
	if strings.HasPrefix(raw, "Use ") {
		return raw
	}
	return "Use " + toolSummary(raw)
}

// renderSwarmGlyph 渲染 bee 的龙形标识或首字圆形头像。
func renderSwarmGlyph(c *ui.Context, keyPrefix string, h SwarmBee, size float32) {
	persona := findSwarmPersona(h.Name)
	icon := safePersonaIcon(persona.Asset)

	ui.Box(c.Key(keyPrefix+"-glyph-container")).
		Size(size, size).
		Radius(size / 2).
		Background(persona.Color.Alpha(0.18)).
		BorderWidth(1.2).
		BorderColor(persona.Color.Alpha(0.55)).
		Justify(ui.Center).
		AlignItems(ui.Center).
		Children(func() {
			if icon != nil {
				ui.Icon(c, icon).
					Size(size*0.72, size*0.72).
					TextColor(persona.Color)
			} else {
				runes := []rune(strings.TrimSpace(h.Name))
				char := ""
				if len(runes) > 0 {
					char = string(runes[0])
				}
				ui.Text(c, char).
					FontSize(size * 0.45).
					FontWeight(600).
					TextColor(persona.Color)
			}
		})
}
