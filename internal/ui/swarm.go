package mcodeui

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"mcode/internal/config"
	"mcode/internal/git"
)

var (
	swarmPanelMu     sync.Mutex
	swarmPanelStates = make(map[*chatState]*swarmPanelState)
)

// modalDragSwarmDiscard 是「放弃这些改动？」二次确认框的拖拽 key。
const modalDragSwarmDiscard = "modal-drag-swarm-discard"

// swarmPanelState 维护当前会话的 Swarm 协作面板状态。
type swarmPanelState struct {
	open       bool
	loadedFor  string
	loading    bool
	err        string
	state      SwarmState
	watchStop  func()
	loadID     int
	refreshed  time.Time
	reportOpen map[string]bool
	// reportDismissed 记录汇报浮窗刚被 PopoverBase 关闭（点外部或 Esc）的消息，供 shouldKeepSwarmReportOpen 使用；
	// 与 reportOpen 一样只在 UI 线程读写。
	reportDismissed map[string]bool
	reviewOpen      bool
	reviewBeeID     string
	reviewLoading   bool
	reviewErr       string
	reviewFiles     []git.FileDiff
	reviewDiff      *diffPanelState
	reviewBusy      bool
	reviewLoadID    int
	reviewActionID  int

	// discardConfirmOpen/discardBeeID 是「放弃合并」二次确认框的状态：
	// 打开时记住待放弃的 bee，确认后才真正调用 DiscardSwarmMerge。
	discardConfirmOpen bool
	discardBeeID       string

	// offsetX/offsetY 是浮动面板相对默认停靠点（窗口右侧、会话头下方）的偏移，受 swarmPanelMu 保护。
	offsetX float32
	offsetY float32

	// 派生缓存与加载节流的记账，均由 swarmPanelMu 保护：
	//   - stateRev：state 每落地一次新内容递增，派生视图（swarmStateView）按它缓存；
	//   - view：state 的派生视图，见 viewLocked；
	//   - loadPending：一次加载在途期间又收到了变更事件，结束后补一次加载。
	stateRev    int
	view        *swarmStateView
	loadPending bool
}

func getSwarmPanelState(s *chatState) *swarmPanelState {
	if s == nil {
		return nil
	}
	swarmPanelMu.Lock()
	defer swarmPanelMu.Unlock()
	st, ok := swarmPanelStates[s]
	if !ok {
		st = &swarmPanelState{}
		swarmPanelStates[s] = st
	}
	return st
}

// setStateLocked 落地新的 Swarm 状态并让派生视图失效，调用方需持有 swarmPanelMu。
func (st *swarmPanelState) setStateLocked(next SwarmState) {
	st.state = next
	st.stateRev++
	st.view = nil
}

// swarmCapsuleRowData 是一条时间线胶囊行要展示的主会话 ID 与它的子线程列表。
type swarmCapsuleRowData struct {
	leadID string
	bees   []SwarmBee
}

// swarmStateView 是一份 SwarmState 的派生视图：有效子线程列表、运行中计数、是否还有子线程没结束，
// 以及按回合分组的胶囊行缓存。它在状态落地新版本时重建一次（见 swarmPanelState.stateRev），
// 渲染路径只读它，不再每帧拷贝整份 SwarmState 或反复全量遍历 bees。
// 视图一经建立就不再修改（胶囊缓存除外，它由 swarmPanelMu 保护），因此可以在解锁后继续读。
type swarmStateView struct {
	rev       int
	leadID    string
	isBee     bool
	available bool
	enabled   bool
	queued    int
	beeCount  int // 构建时的 len(state.Bees)，用于发现绕过 setStateLocked 的直接赋值

	active  []SwarmBee // 有效子线程，与 getActiveSwarmBees 一致
	running int        // state.Bees 里运行中的子线程数
	review  int        // state.Bees 里待审查（PendingMerge）的子线程数，仅主会话统计
	pending bool       // 与 swarmBeesPending 一致

	// statusByThread 是“子线程 ID -> bee 状态”，供侧边栏按会话查子代理状态时 O(1) 取值。
	statusByThread map[string]string
	// runningIDs 是仍在运行且带会话 ID 的子线程，侧边栏判定“还有子代理在跑”只需遍历它。
	runningIDs []string

	// capsules 缓存每条胶囊行的分组结果，键是回合区间：回合边界不会随时间移动，
	// 同一版本里每条行只需过滤一次。
	capsules map[swarmCapsuleTurnSpan]swarmCapsuleRowData
}

// swarmCapsuleCacheMax 是胶囊行缓存的最大条目数：回合行异常多时清空重来，
// 宁可多算一次也不让缓存无限增长。
const swarmCapsuleCacheMax = 256

// buildSwarmStateView 由一份 SwarmState 算出渲染所需的派生数据（只在状态变化时调用）。
func buildSwarmStateView(st SwarmState, rev int) *swarmStateView {
	v := &swarmStateView{
		rev:       rev,
		leadID:    st.LeadThreadID,
		isBee:     st.IsBee,
		available: st.Available,
		enabled:   st.Enabled,
		queued:    st.Queued,
		beeCount:  len(st.Bees),
		active:    getActiveSwarmBees(st),
	}
	for _, h := range st.Bees {
		if h.Status == "running" {
			v.running++
		}
		if id := strings.TrimSpace(h.ThreadID); id != "" {
			if v.statusByThread == nil {
				v.statusByThread = make(map[string]string, len(st.Bees))
			}
			v.statusByThread[id] = MapSwarmBeeStatus(h)
			if h.Status == "running" {
				v.runningIDs = append(v.runningIDs, id)
			}
		}
	}
	// active 已排除子代理会话，因此这里统计的只可能是主会话的子代理。
	for _, h := range v.active {
		if h.PendingMerge {
			v.review++
		}
	}
	v.pending = swarmBeesPending(st)
	return v
}

// viewLocked 返回与 hp.state 对应的派生视图，状态版本变化时重建，调用方需持有 swarmPanelMu。
func (st *swarmPanelState) viewLocked() *swarmStateView {
	if st.view != nil && st.view.rev == st.stateRev && st.view.beeCount == len(st.state.Bees) {
		return st.view
	}
	st.view = buildSwarmStateView(st.state, st.stateRev)
	return st.view
}

// swarmStateViewFor 返回当前会话 Swarm 状态的派生视图；没有面板状态时返回 nil。
// 每帧会被调用多次，因此只取一次锁、不拷贝 SwarmState。
func swarmStateViewFor(s *chatState) *swarmStateView {
	hp := getSwarmPanelState(s)
	if hp == nil {
		return nil
	}
	swarmPanelMu.Lock()
	view := hp.viewLocked()
	swarmPanelMu.Unlock()
	return view
}

// IsOpen 返回 Swarm 面板当前是否展开。
func (st *swarmPanelState) IsOpen() bool {
	if st == nil {
		return false
	}
	swarmPanelMu.Lock()
	defer swarmPanelMu.Unlock()
	return st.open
}

// toggleSwarmPanel 切换 Swarm 协作面板展开与折叠。
func (s *chatState) toggleSwarmPanel() {
	if s == nil {
		return
	}
	hp := getSwarmPanelState(s)
	if hp == nil {
		return
	}

	swarmPanelMu.Lock()
	hp.open = !hp.open
	isOpen := hp.open

	swarmPanelMu.Unlock()

	if isOpen {
		s.startSwarmWatch()
		s.refreshSwarm()
	}
	s.invalidate()
}

func (s *chatState) refreshSwarm() {
	s.mu.Lock()
	tid := s.selectedThreadID
	s.mu.Unlock()
	s.loadSwarm(tid)
}

func (s *chatState) startSwarmWatch() {
	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	watch := hp.watchStop == nil && s.client != nil
	swarmPanelMu.Unlock()
	if watch {
		stop := s.client.WatchSwarm(func(leadThreadID string) {
			swarmPanelMu.Lock()
			curLead := hp.state.LeadThreadID
			swarmPanelMu.Unlock()

			s.mu.Lock()
			selID := s.selectedThreadID
			s.mu.Unlock()

			if leadThreadID == curLead || leadThreadID == selID || leadThreadID == "" {
				s.loadSwarm(selID)
			}
		})
		swarmPanelMu.Lock()
		hp.watchStop = stop
		swarmPanelMu.Unlock()
	}
}

func (s *chatState) ensureSwarm(c *ui.Context) {
	s.startSwarmWatch()
	hp := getSwarmPanelState(s)
	s.mu.Lock()
	tid := s.selectedThreadID
	s.mu.Unlock()
	swarmPanelMu.Lock()
	load := hp.loadedFor != tid
	// running 直接读派生视图，不再每帧遍历 bees。
	running := hp.viewLocked().running > 0
	if running && !hp.loading && time.Since(hp.refreshed) >= 2*time.Second {
		load = true
	}
	swarmPanelMu.Unlock()

	if load {
		s.loadSwarm(tid)
	}
	if running {
		c.After(2 * time.Second)
	}
}

// loadSwarm 在后台异步加载指定会话的 Swarm 状态。
// 同一会话的加载在途时，后续调用只标记脏（loadPending）并立即返回：RPC 与转换不碰 UI 线程，
// 结束后再补一次，因此事件再多也只有“一个在途 + 最多一个补发”。
func (s *chatState) loadSwarm(threadID string) {
	if s == nil || s.client == nil {
		return
	}
	hp := getSwarmPanelState(s)
	if hp == nil {
		return
	}

	swarmPanelMu.Lock()
	if hp.loading && hp.loadedFor == threadID && threadID != "" {
		// 已经有同一个会话的加载在途：只记一笔脏，在途的那次结束后会补发。
		hp.loadPending = true
		swarmPanelMu.Unlock()
		return
	}
	hp.loadID++
	loadID := hp.loadID
	changed := hp.loadedFor != threadID
	hp.loadedFor = threadID
	if changed {
		hp.setStateLocked(SwarmState{})
		hp.reviewOpen = false
		hp.reviewBeeID = ""
		hp.reviewFiles = nil
		hp.reviewDiff = nil
		hp.reviewErr = ""
		hp.reviewLoading = false
		hp.reviewBusy = false
		hp.reviewLoadID++
		hp.reviewActionID++
		hp.discardConfirmOpen = false
		hp.discardBeeID = ""
	}
	if threadID == "" {
		hp.loading = false
		hp.loadPending = false
		hp.err = ""
		hp.setStateLocked(SwarmState{})
		swarmPanelMu.Unlock()
		s.invalidate()
		return
	}
	hp.loading = true
	hp.loadPending = false
	hp.err = ""
	swarmPanelMu.Unlock()
	s.invalidate()

	go func(tid string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		st, err := s.client.Swarm(ctx, tid)

		swarmPanelMu.Lock()
		if hp.loadedFor != tid || hp.loadID != loadID {
			// 已经被更新的一次加载取代：不落地任何状态，脏标记留给取代它的那次处理。
			swarmPanelMu.Unlock()
			return
		}
		hp.loading = false
		hp.refreshed = time.Now()
		if err != nil {
			// 只记错误，保留上次成功的编排状态：瞬时失败（如 5s 超时）不应清空
			// 子代理列表，也不应连带把 Swarm 开关与 lead 路由导向打回默认。
			hp.err = err.Error()
		} else {
			hp.err = ""
			hp.setStateLocked(st)
		}
		pending := hp.loadPending
		hp.loadPending = false
		swarmPanelMu.Unlock()
		s.invalidate()
		if pending {
			// 在途期间又有变更：补一次（此时已不在 loading 态，会真的发一次 RPC）。
			s.loadSwarm(tid)
		}
	}(threadID)
}

func (s *chatState) swarmTargetLeadID() string {
	s.mu.Lock()
	selected := s.selectedThreadID
	s.mu.Unlock()
	hp := getSwarmPanelState(s)
	if hp != nil {
		swarmPanelMu.Lock()
		lead := hp.state.LeadThreadID
		valid := hp.loadedFor == selected && !hp.loading
		swarmPanelMu.Unlock()
		if valid && lead != "" {
			return lead
		}
	}
	return selected
}

// setSwarmEnabled 设置是否开启 Swarm 多 bee 协作。
func (s *chatState) setSwarmEnabled(enabled bool) {
	if s == nil || s.client == nil {
		return
	}
	s.mu.Lock()
	if s.isPendingThreadLocked(s.selectedThreadID) {
		// 待创建会话：开关记在待创建选项里，创建时随会话一起下发。
		s.setPendingSwarmEnabledLocked(enabled)
		s.mu.Unlock()
		rememberSwarmEnabled(enabled)
		s.invalidate()
		return
	}
	s.mu.Unlock()
	lead := s.swarmTargetLeadID()
	if lead == "" {
		return
	}

	go func(lid string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, err := s.client.SetSwarmEnabled(ctx, lid, enabled)
		if err != nil {
			s.setToast("设置编排失败: " + err.Error())
		} else {
			rememberSwarmEnabled(enabled)
		}
		s.refreshSwarm()
	}(lead)
}

// setSwarmPlansOnly 设置是否开启 Swarm 只规划开关覆盖。
func (s *chatState) setSwarmPlansOnly(plansOnly bool) {
	if s == nil || s.client == nil {
		return
	}
	lead := s.swarmTargetLeadID()
	if lead == "" {
		return
	}

	go func(lid string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, err := s.client.SetSwarmPlansOnly(ctx, lid, plansOnly)
		if err != nil {
			s.setToast("设置主代理只规划失败: " + err.Error())
		}
		s.refreshSwarm()
	}(lead)
}

// stopSwarmBee 停止指定的 Swarm bee。
func (s *chatState) stopSwarmBee(beeID string) {
	if s == nil || s.client == nil {
		return
	}
	lead := s.swarmTargetLeadID()
	if lead == "" {
		return
	}

	go func(lid, hid string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		err := s.client.StopSwarmBee(ctx, lid, hid)
		if err != nil {
			s.setToast("停止子代理失败: " + err.Error())
		}
		s.refreshSwarm()
	}(lead, beeID)
}

// stopAllSwarmBees 停止所有运行中的 Swarm bee。
func (s *chatState) stopAllSwarmBees() {
	if s == nil || s.client == nil {
		return
	}
	lead := s.swarmTargetLeadID()
	if lead == "" {
		return
	}

	go func(lid string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		err := s.client.StopAllSwarmBees(ctx, lid)
		if err != nil {
			s.setToast("停止全部子代理失败: " + err.Error())
		}
		s.refreshSwarm()
	}(lead)
}

// clearFinishedSwarmBees 清理已完成或已结束的 Swarm bee。
func (s *chatState) clearFinishedSwarmBees() {
	if s == nil || s.client == nil {
		return
	}
	lead := s.swarmTargetLeadID()
	if lead == "" {
		return
	}

	go func(lid string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		err := s.client.ClearFinishedSwarmBees(ctx, lid)
		if err != nil {
			s.setToast("清理已结束子代理失败: " + err.Error())
		}
		s.refreshSwarm()
	}(lead)
}

// popOutSwarmBee 把指定 bee 的会话弹出为系统级独立窗口。
func (s *chatState) popOutSwarmBee(beeID string) {
	if s == nil || beeID == "" {
		return
	}
	openBeeWindow(s, beeID)
}

// openSwarmReview 打开 Swarm 合并审查器并选中指定 bee。
func (s *chatState) openSwarmReview(beeID string) {
	if s == nil {
		return
	}
	hp := getSwarmPanelState(s)
	if hp == nil {
		return
	}
	swarmPanelMu.Lock()
	hp.reviewOpen = true
	if hp.reviewDiff == nil {
		hp.reviewDiff = &diffPanelState{
			collapsed: make(map[string]bool),
		}
	}
	swarmPanelMu.Unlock()
	s.selectSwarmReviewBee(beeID)
}

// selectSwarmReviewBee 选中指定 bee 并异步加载其合并差异。
func (s *chatState) selectSwarmReviewBee(beeID string) {
	if s == nil || s.client == nil {
		return
	}
	hp := getSwarmPanelState(s)
	if hp == nil {
		return
	}

	leadID := s.swarmTargetLeadID()
	if leadID == "" {
		return
	}

	swarmPanelMu.Lock()
	hp.reviewBeeID = beeID
	hp.reviewFiles = nil
	hp.reviewErr = ""
	hp.reviewLoading = true
	hp.reviewLoadID++
	loadID := hp.reviewLoadID
	hp.reviewDiff = &diffPanelState{collapsed: make(map[string]bool)}
	swarmPanelMu.Unlock()
	s.invalidate()

	go func(lid, hid string, curLoadID int) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		d, err := s.client.SwarmMergeDiff(ctx, lid, hid)

		swarmPanelMu.Lock()
		defer swarmPanelMu.Unlock()
		if hp.reviewLoadID != curLoadID {
			return
		}
		hp.reviewLoading = false
		if err != nil {
			hp.reviewErr = err.Error()
			hp.reviewFiles = nil
		} else {
			hp.reviewErr = ""
			files, parseErr := git.ParseUnifiedDiff([]byte(d.Diff))
			if parseErr != nil {
				hp.reviewErr = parseErr.Error()
				hp.reviewFiles = nil
			} else {
				hp.reviewFiles = files
			}
		}
		s.invalidate()
	}(leadID, beeID, loadID)
}

// approveSwarmMerge 批准指定 bee 的合并。
func (s *chatState) approveSwarmMerge(beeID string) {
	if s == nil || s.client == nil {
		return
	}
	hp := getSwarmPanelState(s)
	if hp == nil {
		return
	}
	leadID := s.swarmTargetLeadID()
	if leadID == "" {
		return
	}

	swarmPanelMu.Lock()
	if hp.reviewBusy {
		swarmPanelMu.Unlock()
		return
	}
	hp.reviewBusy = true
	hp.reviewActionID++
	actionID := hp.reviewActionID
	hp.reviewErr = ""
	swarmPanelMu.Unlock()
	s.invalidate()

	go func(lid, hid string) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		err := s.client.ApproveSwarmMerge(ctx, lid, hid)

		swarmPanelMu.Lock()
		if hp.reviewActionID == actionID {
			hp.reviewBusy = false
			if err != nil && hp.reviewBeeID == hid {
				hp.reviewErr = err.Error()
			}
		}
		swarmPanelMu.Unlock()

		s.refreshSwarm()
	}(leadID, beeID)
}

// discardSwarmMerge 放弃指定 bee 的合并。
func (s *chatState) discardSwarmMerge(beeID string) {
	if s == nil || s.client == nil {
		return
	}
	hp := getSwarmPanelState(s)
	if hp == nil {
		return
	}
	leadID := s.swarmTargetLeadID()
	if leadID == "" {
		return
	}

	swarmPanelMu.Lock()
	if hp.reviewBusy {
		swarmPanelMu.Unlock()
		return
	}
	hp.reviewBusy = true
	hp.reviewActionID++
	actionID := hp.reviewActionID
	hp.reviewErr = ""
	swarmPanelMu.Unlock()
	s.invalidate()

	go func(lid, hid string) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		err := s.client.DiscardSwarmMerge(ctx, lid, hid)

		swarmPanelMu.Lock()
		if hp.reviewActionID == actionID {
			hp.reviewBusy = false
			if err != nil && hp.reviewBeeID == hid {
				hp.reviewErr = err.Error()
			}
		}
		swarmPanelMu.Unlock()

		s.refreshSwarm()
	}(leadID, beeID)
}

// closeSwarmReview 关闭 Swarm 合并审查器。
func (s *chatState) closeSwarmReview() {
	if s == nil {
		return
	}
	hp := getSwarmPanelState(s)
	if hp == nil {
		return
	}
	swarmPanelMu.Lock()
	hp.reviewOpen = false
	hp.discardConfirmOpen = false
	hp.discardBeeID = ""
	swarmPanelMu.Unlock()
	s.invalidate()
}

// swarmBeePreview 计算 bee 的预览文字，截断至约 160 个字符并加省略号。
func swarmBeePreview(h SwarmBee) string {
	var text string
	if h.Status == "running" && strings.TrimSpace(h.Activity) != "" {
		text = h.Activity
	} else if strings.TrimSpace(h.Summary) != "" {
		text = h.Summary
	} else {
		text = h.Task
	}
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) > 160 {
		return string(runes[:160]) + "…"
	}
	return text
}

// swarmProgressText 生成如 "2/3 已结束，1 个失败" 格式的整体进度提示，无 bee 时返回空串。
// 非 running 都算已结束（completed、failed、stopped）；失败个数单独追加，便于一眼看出异常。
// 队列中还有待启动的 bee 时追加排队数，避免误判为全队已完成。
func swarmProgressText(st SwarmState) string {
	if len(st.Bees) == 0 {
		if st.Queued > 0 {
			return fmt.Sprintf("%d 个排队中", st.Queued)
		}
		return ""
	}
	done := 0
	failed := 0
	for _, h := range st.Bees {
		if h.Status == "running" {
			continue
		}
		done++
		if h.Status == "failed" {
			failed++
		}
	}
	text := fmt.Sprintf("%d/%d 已结束", done, len(st.Bees))
	if failed > 0 {
		text += fmt.Sprintf("，%d 个失败", failed)
	}
	if st.Queued > 0 {
		text += fmt.Sprintf("，%d 个排队中", st.Queued)
	}
	return text
}

// swarmHasConflicts 检查 bee 是否包含冲突文件。
func swarmHasConflicts(h SwarmBee) bool {
	return len(h.ConflictFiles) > 0
}

// 浮动面板的尺寸、边距与停靠参数。
const (
	swarmPanelWidth   float32 = 420 // 浮动面板宽度
	swarmPanelMargin  float32 = 8   // 面板与容器边缘的最小边距
	swarmPanelDockGap float32 = 24  // 距左右边缘小于该值时吸附停靠
	swarmPanelAnchorY float32 = 64  // 默认位置左上角 y（相对窗口内容区，位于会话头下方）
	swarmPanelRadius  float32 = 22  // 面板圆角：面板宽 420、内容成组，取比浮层常规更大的一档
)

// swarmHaloSpec 描述 Swarm 标题按钮光环的静态描边样式。
type swarmHaloSpec struct {
	Width float32 // 内层实色描边宽度（pt），0 表示不画光环
	Alpha float32 // 内层描边不透明度
	Glow  bool    // 是否叠加外层低 alpha 的宽描边模拟光晕（性能模式为 false）
}

// swarmHaloStyle 判定 Swarm 标题按钮光环的样式，纯函数：
//   - Swarm 关闭时不画光环；
//   - 开启但空闲时较暗（0.35），有 bee 运行时更亮（0.9）；
//   - 性能模式下只画单色细描边，不画外层柔光；
//   - 两种模式都不做旋转、呼吸或任何动画/定时重绘。
//
// 注：仓库目前没有性能模式设置（mcode 默认即性能模式
// 外观，不迁移 performanceMode 开关），调用点固定传 false，该参数为后续接入预留。
func swarmHaloStyle(enabled bool, runningBees int, performanceMode bool) swarmHaloSpec {
	if !enabled {
		return swarmHaloSpec{}
	}
	alpha := float32(0.35)
	if runningBees > 0 {
		alpha = 0.9
	}
	if performanceMode {
		return swarmHaloSpec{Width: 1, Alpha: alpha}
	}
	return swarmHaloSpec{Width: 2, Alpha: alpha, Glow: true}
}

// clampSwarmPanelPos 把浮动面板的左上角限制在容器内，四周至少留 swarmPanelMargin 边距。
func clampSwarmPanelPos(x, y, panelW, panelH, containerW, containerH float32) (float32, float32) {
	maxX := containerW - panelW - swarmPanelMargin
	if maxX < swarmPanelMargin {
		maxX = swarmPanelMargin
	}
	maxY := containerH - panelH - swarmPanelMargin
	if maxY < swarmPanelMargin {
		maxY = swarmPanelMargin
	}
	if x < swarmPanelMargin {
		x = swarmPanelMargin
	} else if x > maxX {
		x = maxX
	}
	if y < swarmPanelMargin {
		y = swarmPanelMargin
	} else if y > maxY {
		y = maxY
	}
	return x, y
}

// dockSwarmPanelPos 先 clamp，再在面板距离左右边缘不到 swarmPanelDockGap 时吸附到最近的一侧（贴边距）。
func dockSwarmPanelPos(x, y, panelW, panelH, containerW, containerH float32) (float32, float32) {
	x, y = clampSwarmPanelPos(x, y, panelW, panelH, containerW, containerH)
	leftDist := x - swarmPanelMargin
	rightDist := (containerW - panelW - swarmPanelMargin) - x
	switch {
	case leftDist < swarmPanelDockGap && leftDist <= rightDist:
		x = swarmPanelMargin
	case rightDist < swarmPanelDockGap:
		x = containerW - panelW - swarmPanelMargin
		if x < swarmPanelMargin {
			x = swarmPanelMargin
		}
	}
	return x, y
}

// swarmDragHandle 让面板顶部条充当拖拽手柄：按下拖动时按增量更新浮动面板偏移。
func swarmDragHandle(s *chatState, handle ui.Element) {
	if s == nil {
		return
	}
	hp := getSwarmPanelState(s)
	if hp == nil {
		return
	}
	if dx, dy, held := handle.Dragged(); held {
		swarmPanelMu.Lock()
		hp.offsetX += dx
		hp.offsetY += dy
		swarmPanelMu.Unlock()
		handle.Cursor(ui.CursorGrabbing)
	}
}

// swarmPopOutBeeID 返回标题栏弹出按钮的目标 bee：优先运行中的，其次第一个；没有 bee 时返回空串。
func swarmPopOutBeeID(st SwarmState) string {
	for _, h := range st.Bees {
		if h.Status == "running" {
			return h.ThreadID
		}
	}
	if len(st.Bees) > 0 {
		return st.Bees[0].ThreadID
	}
	return ""
}

// swarmToggleSize 是标题栏编排开关的绘制直径，与模型选择面板里的编排开关一致（ControlMD 28）。
const swarmToggleSize float32 = ControlMD

// swarmToggleHitSize 是编排开关的命中区：绘制仍是 28，外圈各留 2 便于点击。
const swarmToggleHitSize float32 = ControlLG

// swarmToggleInset 是命中区与绘制圆之间单侧的留白。
const swarmToggleInset float32 = (swarmToggleHitSize - swarmToggleSize) / 2

// 编排开关环内 accent 微光的不透明度：平时 0.16（与模型选择面板一致），
// 浮窗展开时加深到 0.30，保留原有“面板已打开”的反馈。
const (
	swarmToggleGlow     float32 = 0.16
	swarmToggleGlowOpen float32 = 0.30
)

// swarmTogglePaint 描述「编排」开关按钮的底，对应模型选择面板里的编排开关
// （effort_slider.go 的 renderProviderRainbowAvatar）的同一套外观。
type swarmTogglePaint struct {
	On      bool     // 开关态：开启时画名册色角向渐变环
	Hovered bool     // 悬停：关闭态底色从 6% 加深到 10%
	RingW   float32  // 环宽，也是镂空环内侧的内缩量；<=0 时按 2 处理
	Glow    float32  // 镂空环内侧 accent 微光的不透明度，0 表示不叠
	InnerBg ui.Color // 镂空环内侧的底色（用调用方所在视图的底，环心才能透出本视图底色）
}

// paintSwarmToggle 按统一外观绘制编排开关按钮的底：关闭时白 6%（悬停 10%）圆底；
// 开启时先用名册色角向渐变铺满圆，再用视图底色盖出镂空的环，最后叠一层 accent 微光。
func paintSwarmToggle(c *ui.Context, painter *ui.Painter, r ui.Rect, p swarmTogglePaint) {
	if !p.On {
		bg := ThemeOverlay(c, 0.06)
		if p.Hovered {
			bg = ThemeOverlay(c, 0.10)
		}
		painter.Fill(r, bg, r.W/2)
		return
	}
	ringW := p.RingW
	if ringW <= 0 {
		ringW = 2
	}
	stops := make([]ui.GradientStop, 0, len(swarmRosterColors)+1)
	for i, col := range swarmRosterColors {
		stops = append(stops, ui.GradientStop{Color: col, At: float32(i) / float32(len(swarmRosterColors))})
	}
	stops = append(stops, ui.GradientStop{Color: swarmRosterColors[0], At: 1})
	painter.FillConicGradient(r, ui.ConicGradient{Stops: stops}, r.W/2)
	inner := ui.Rect{X: r.X + ringW, Y: r.Y + ringW, W: r.W - 2*ringW, H: r.H - 2*ringW}
	painter.Fill(inner, p.InnerBg, inner.W/2)
	if p.Glow > 0 {
		painter.Fill(inner, ThemeAccent(c).Alpha(p.Glow), inner.W/2)
	}
}

// renderSwarmHeaderButton 渲染会话头部的 Swarm 切换按钮：外观与模型选择面板里的编排开关一致
// （开启时外圈为名册色渐变环），右键弹出会话专属开关菜单。
func renderSwarmHeaderButton(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	s.ensureSwarm(c)
	s.mu.Lock()
	selected := s.selectedThreadID
	s.mu.Unlock()
	if selected == "" {
		return
	}
	hp := getSwarmPanelState(s)
	if hp == nil {
		return
	}
	// 运行中计数、排队数、待审查数与有效子线程列表都读派生视图，不再每帧拷贝整份 SwarmState。
	view := swarmStateViewFor(s)
	var runningCount, reviewCount int
	var activeBees []SwarmBee
	var enabled, isBee bool
	var leadID string
	var queued int
	if view != nil {
		runningCount = view.running
		reviewCount = view.review
		activeBees = view.active
		enabled, isBee = view.enabled, view.isBee
		leadID, queued = view.leadID, view.queued
	}
	// 角标数字只统计正在运行中的子代理，加上派发队列里排队等待启动的子代理。
	queuedCount := queued
	if queuedCount < 0 {
		queuedCount = 0
	}
	pendingCount := runningCount + queuedCount
	floatShown := swarmFloatVisible(leadID, activeBees)
	// 等待用户回答的子线程：按钮右上角叠角标，tooltip 说明，点击直接打开对应子线程的浮窗。
	waitingBees := swarmWaitingBeeIDs(s, activeBees)
	tooltip := "编排：主代理派发多个子代理并行执行（右键切换工作模式）"
	if n := len(waitingBees); n > 0 {
		tooltip = fmt.Sprintf("%d 个子线程在等你回答", n)
	} else if !isBee && len(activeBees) > 0 {
		// 无待回答提问时，角标改为显示当前会话的子任务数量，tooltip 同步说明。
		tooltip = fmt.Sprintf("编排：%d 个子任务（右键切换工作模式）", len(activeBees))
	}

	// 仓库尚无性能模式设置，固定按普通模式渲染。
	haloSpec := swarmHaloStyle(enabled, runningCount, false)
	on := haloSpec.Width > 0

	// 容器包住待审查角标与按钮本身；等待/数量角标绝对定位叠在按钮右上角，不参与布局。
	ui.Row(c.Key("chat-header-swarm-btn-wrap")).
		AlignItems(ui.Center).
		Children(func() {
			// 待审查角标放在按钮左侧：数字角标按容器右边缘绝对定位，放在按钮后面会被挤离按钮。
			if !isBee && reviewCount > 0 {
				renderSwarmReviewBadge(c, reviewCount)
			}

			// 外观与模型选择面板里的编排开关完全一致：绘制 28 圆形，命中区 32；关闭时是白 6% 圆底
			// （悬停 10%），开启时是名册色角向渐变环 + 内衬 accent 微光。
			btn := ui.ButtonBase(c.Key("chat-header-swarm-btn")).
				Label("编排").
				Value(map[bool]string{true: "开启", false: "关闭"}[on]).
				Tooltip(tooltip).
				Size(swarmToggleHitSize, swarmToggleHitSize).
				Radius(swarmToggleHitSize / 2).
				Center()

			hovered := btn.Hovered()
			glow := swarmToggleGlow
			if floatShown {
				// 保留原来「浮窗已展开」的反馈：环内微光加深。
				glow = swarmToggleGlowOpen
			}
			btn.Draw(func(painter *ui.Painter, r ui.Rect) {
				paintSwarmToggle(c, painter, ui.Rect{
					X: r.X + swarmToggleInset,
					Y: r.Y + swarmToggleInset,
					W: swarmToggleSize,
					H: swarmToggleSize,
				}, swarmTogglePaint{
					On:      on,
					Hovered: hovered,
					RingW:   haloSpec.Width,
					Glow:    glow,
					InnerBg: ThemeWindowBg(c),
				})
			})

			if btn.Clicked() && leadID != "" && !isBee {
				// 有待回答的提问时，左键直接打开第一个等待的子线程；否则只切换浮窗：
				// 显示中则隐藏，隐藏中则清除关闭记录并强制显示（没有 bee 时是空团队面板）。
				if len(waitingBees) > 0 {
					openSwarmFloatForBee(leadID, waitingBees[0])
				} else if floatShown {
					dismissSwarmFloatPanel(leadID, activeBees)
				} else {
					undismissSwarmFloatPanel(leadID)
				}
				s.invalidate()
			}
			// 右键菜单直接切换会话级工作模式；子线程（isBee）本身不承载主导角色，不挂菜单。
			if !isBee {
				btn.ContextMenu(func(m *ui.Menu) {
					current := swarmModeOptionFor(s.threadSwarmMode(selected))
					for _, opt := range swarmModeOptions {
						o := opt
						if m.Item(o.Label).Checked(o.Mode == current.Mode).Chosen() && o.Mode != current.Mode {
							s.setThreadSwarmMode(selected, o.Mode)
						}
					}
				})
			}

			btn.Children(func() {
				// 图标两态取色与开关一致：关闭 60% 主文本色，开启或悬停为实色。
				alpha := float32(0.6)
				if on || hovered {
					alpha = 1
				}
				ui.Row(c.Key("chat-header-swarm-btn-content")).
					Gap(Space1).
					AlignItems(ui.Center).
					Children(func() {
						ui.Icon(c, IconSwarm()).Size(IconMD, IconMD).TextColor(ThemePrimaryText(c).Alpha(alpha))
					})
			})

			// 有待回答提问时优先显示危险色等待角标；否则在主导会话上显示运行中与排队中的子代理数量角标。
			if len(waitingBees) > 0 {
				renderSwarmHeaderBadge(c, "chat-header-swarm-waiting-badge", len(waitingBees), ThemeDanger(c))
			} else if !isBee && pendingCount > 0 {
				renderSwarmHeaderBadge(c, "chat-header-swarm-count-badge", pendingCount, ThemeAccent(c))
			}
		})
}

// renderSwarmHeaderBadge 渲染编排按钮右上角的数字角标：用 Absolute 叠在按钮容器上，
// 不占布局宽度也不撑高按钮；背景色由调用方指定（等待回答用危险色、子任务数量用强调色），
// 白字，超过 99 显示 99+。
func renderSwarmHeaderBadge(c *ui.Context, key string, count int, bg ui.Color) {
	text := strconv.Itoa(count)
	if count > 99 {
		text = "99+"
	}
	ui.Row(c.Key(key)).
		Absolute().
		Top(-4).
		Right(-6).
		Height(IconSM).
		MinWidth(14).
		Padding(0, Space1).
		Radius(CapsuleRadius).
		Center().
		Background(bg).
		Children(func() {
			ui.Text(c, text).
				FontSize(TypeCaption).
				FontWeight(600).
				TextColor(ThemeOnAccent(c)).
				SingleLine()
		})
}

// renderSwarmReviewBadge 在编排按钮左侧显示「待审查 N」：配色与 bee 行内的「待审查」标签一致，
// 只提示待审查数量，不改变运行中/排队中数字角标的语义。
func renderSwarmReviewBadge(c *ui.Context, count int) {
	ui.Row(c.Key("chat-header-swarm-review-badge")).
		Height(IconMD).
		Padding(0, Space1).
		Margin(0, Space1, 0, 0).
		Radius(CapsuleRadius).
		Center().
		Background(ThemeAccent(c).Alpha(0.15)).
		Children(func() {
			ui.Text(c, fmt.Sprintf("待审查 %d", count)).
				FontSize(TypeCaption).
				FontWeight(600).
				TextColor(ThemeAccent(c)).
				SingleLine()
		})
}

// reviewBeeStatusTag 计算 bee 在审查器中的状态标签文本与语义颜色。
func reviewBeeStatusTag(c *ui.Context, h SwarmBee) (string, ui.Color) {
	if len(h.ConflictFiles) > 0 {
		return "有冲突", ThemeDanger(c)
	}
	switch h.ReviewState {
	case "running":
		return "审查中", ThemeAccent(c)
	case "rejected":
		return "审查未通过", ThemeDanger(c)
	case "needs_human":
		return "待审查", ThemeAccent(c)
	}
	if h.PendingMerge {
		return "待审查", ThemeAccent(c)
	}
	if h.MergeError != "" {
		return "合并失败", ThemeDanger(c)
	}
	if h.Merged {
		return "已合并", ThemeSuccess(c)
	}
	if h.MergeDiscarded {
		return "已放弃", ThemeSecondaryText(c)
	}
	return "已完成", ThemeSuccess(c)
}

// reviewLayerLabel 把审查决定层标识转换为界面文案，未知取值原样展示。
func reviewLayerLabel(layer string) string {
	switch layer {
	case "l0":
		return "L0 确定性检查"
	case "l1":
		return "L1 规则放行"
	case "l2":
		return "L2 模型审查"
	case "l3":
		return "L3 审查会话"
	case "human":
		return "人工审查"
	}
	return layer
}

// formatReviewTokenCount 格式化审查消耗的 token 数，上千时以 k 为单位保留一位小数。
func formatReviewTokenCount(n int, estimated bool) string {
	count := strconv.Itoa(n)
	if n >= 1000 {
		count = fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	if estimated {
		return "约 " + count + " tokens"
	}
	return count + " tokens"
}

// renderSwarmMergeReview 渲染 Swarm 多 Bee 代码合并审查器。
func renderSwarmMergeReview(c *ui.Context, s *chatState, st SwarmState) {
	hp := getSwarmPanelState(s)
	if hp == nil {
		return
	}

	swarmPanelMu.Lock()
	selectedBeeID := hp.reviewBeeID
	reviewLoading := hp.reviewLoading
	reviewErr := hp.reviewErr
	reviewBusy := hp.reviewBusy
	reviewDiff := hp.reviewDiff
	reviewFiles := make([]git.FileDiff, len(hp.reviewFiles))
	copy(reviewFiles, hp.reviewFiles)
	swarmPanelMu.Unlock()

	// 筛选出可供审查的 bee：完成且处于待审查/已合并/已放弃状态（或含冲突），
	// 以及审查进行中或审查未通过的 bee。
	var reviewBees []SwarmBee
	for _, h := range st.Bees {
		if (h.Status == "completed" || len(h.ConflictFiles) > 0) && (h.PendingMerge || h.Merged || h.MergeDiscarded || len(h.ConflictFiles) > 0 || h.ReviewState == "running" || h.ReviewState == "rejected") {
			reviewBees = append(reviewBees, h)
		}
	}

	// 确定当前选中的 bee
	var curBee *SwarmBee
	for i := range reviewBees {
		if reviewBees[i].ThreadID == selectedBeeID {
			curBee = &reviewBees[i]
			break
		}
	}
	if curBee == nil && len(reviewBees) > 0 {
		curBee = &reviewBees[0]
		selectedBeeID = curBee.ThreadID
		s.selectSwarmReviewBee(selectedBeeID)
		reviewFiles = nil
		reviewErr = ""
		reviewLoading = true
	}

	// 汇总增删行数
	var adds, dels int
	for _, f := range reviewFiles {
		if !f.Binary {
			for _, hunk := range f.Hunks {
				for _, l := range hunk.Lines {
					switch l.Kind {
					case '+':
						adds++
					case '-':
						dels++
					}
				}
			}
		}
	}

	ui.Column(c.Key("swarm-review-root")).
		FillWidth().
		Gap(Space3).
		Children(func() {
			// 顶部标题栏与关闭按钮；整行兼作浮动面板的拖拽手柄
			reviewHeader := ui.Row(c.Key("swarm-review-header")).
				FillWidth().
				AlignItems(ui.Center).
				Margin(0, 0, Space1, 0).
				Cursor(ui.CursorGrab).
				Children(func() {
					ui.Text(c, "合并审查").
						FontSize(TypeProse).
						FontWeight(600).
						TextColor(ThemePrimaryText(c))

					ui.Spacer(c)

					closeBtn := ui.ButtonBase(c.Key("swarm-review-close-btn")).
						Label("关闭").
						Tooltip("关闭审查器").
						Radius(RadiusSM).
						Padding(Space1, Space3)
					if closeBtn.Hovered() {
						closeBtn.Background(ThemeHover(c))
					}
					if closeBtn.Clicked() {
						s.closeSwarmReview()
					}
					closeBtn.Children(func() {
						ui.Row(c.Key("swarm-review-close-btn-inner")).
							Gap(Space1).
							AlignItems(ui.Center).
							Children(func() {
								ui.Icon(c, IconX()).Size(Space4, Space4).TextColor(ThemeSecondaryText(c))
								ui.Text(c, "关闭").
									FontSize(TypeFootnote).
									TextColor(ThemeSecondaryText(c))
							})
					})
				})

			swarmDragHandle(s, reviewHeader)

			if len(reviewBees) == 0 {
				ui.Row(c.Key("swarm-review-no-bees")).
					Padding(Space3, 0).
					Children(func() {
						ui.Text(c, "暂无可合并审查的子代理").
							FontSize(TypeBody).
							TextColor(ThemeSecondaryText(c))
					})
				return
			}

			// bee 切换条
			ui.Row(c.Key("swarm-review-tabs")).
				FillWidth().
				Gap(Space2).
				AlignItems(ui.Center).
				Children(func() {
					for _, h := range reviewBees {
						beeItem := h
						isSelected := beeItem.ThreadID == selectedBeeID
						tagText, tagColor := reviewBeeStatusTag(c, beeItem)

						tabBtn := ui.ButtonBase(c.Key("swarm-review-tab-"+beeItem.ThreadID)).
							Label(beeItem.Name).
							Radius(RadiusSM).
							Padding(Space1, Space3)
						if isSelected {
							tabBtn.Background(ThemeSelected(c))
						} else if tabBtn.Hovered() {
							tabBtn.Background(ThemeHover(c))
						}
						if tabBtn.Clicked() {
							s.selectSwarmReviewBee(beeItem.ThreadID)
						}
						tabBtn.Children(func() {
							ui.Row(c.Key("swarm-review-tab-inner-" + beeItem.ThreadID)).
								Gap(Space1).
								AlignItems(ui.Center).
								Children(func() {
									titleColor := ThemePrimaryText(c)
									if isSelected {
										titleColor = ThemeAccent(c)
									}
									displayName := beeItem.Name
									if displayName == "" {
										displayName = fmt.Sprintf("子代理 #%d", beeItem.Index)
									}
									ui.Text(c, displayName).
										FontSize(TypeFootnote).
										FontWeight(500).
										TextColor(titleColor)

									ui.Text(c, tagText).
										FontSize(TypeCaption).
										TextColor(tagColor)
								})
						})
					}
				})

			// 选中 bee 的元信息与操作栏
			if curBee != nil {
				ui.Column(c.Key("swarm-review-bee-info")).
					FillWidth().
					Padding(Space3, Space3).
					Radius(RadiusSM).
					Background(ThemeSurface(c)).
					BorderWidth(1).
					BorderColor(ThemeBorder(c)).
					Gap(Space1).
					Children(func() {
						// 改动统计与操作按钮
						ui.Row(c.Key("swarm-review-action-row")).
							FillWidth().
							AlignItems(ui.Center).
							Gap(Space3).
							Children(func() {
								fileCountText := fmt.Sprintf("%d 个改动文件", len(reviewFiles))
								ui.Text(c, fileCountText).
									FontSize(TypeBody).
									FontWeight(500).
									TextColor(ThemePrimaryText(c))

								if adds > 0 {
									ui.Text(c, fmt.Sprintf("+%d", adds)).
										FontSize(TypeFootnote).
										FontWeight(500).
										TextColor(ThemeSuccess(c))
								}
								if dels > 0 {
									ui.Text(c, fmt.Sprintf("-%d", dels)).
										FontSize(TypeFootnote).
										FontWeight(500).
										TextColor(ThemeDanger(c))
								}

								ui.Spacer(c)

								if curBee.ReviewState == "running" {
									ui.Text(c, "审查模型正在审查…").
										FontSize(TypeFootnote).
										TextColor(ThemeAccent(c))
								} else if curBee.ReviewState == "rejected" {
									ui.Text(c, "改动未合入，补丁已保留，可让主代理派子代理按审查意见返工").
										FontSize(TypeFootnote).
										TextColor(ThemeDanger(c))
								} else if curBee.PendingMerge {
									if reviewBusy {
										ui.Row(c.Key("swarm-review-busy-row")).
											Gap(Space1).
											AlignItems(ui.Center).
											Children(func() {
												renderRunningGlyph(c, "swarm-panel-run-merge-busy-"+curBee.ThreadID, 12, ThemeAccent(c))
												ui.Text(c, "处理中…").
													FontSize(TypeFootnote).
													TextColor(ThemeSecondaryText(c))
											})
									} else {
										approveBtn := ui.ButtonBase(c.Key("swarm-review-approve-btn")).
											Label("批准合并").
											Disabled(reviewLoading || len(reviewFiles) == 0).
											Radius(RadiusXS).
											Padding(Space1, Space3).
											Background(ThemeAccent(c))
										if approveBtn.Hovered() {
											approveBtn.Background(ThemeAccent(c).Alpha(0.85))
										}
										if approveBtn.Clicked() {
											s.approveSwarmMerge(curBee.ThreadID)
										}
										approveBtn.Children(func() {
											ui.Text(c, "批准合并").
												FontSize(TypeFootnote).
												FontWeight(500).
												TextColor(ThemeOnAccent(c))
										})

										discardBtn := ui.ButtonBase(c.Key("swarm-review-discard-btn")).
											Label("放弃").
											Radius(RadiusXS).
											Padding(Space1, Space3)
										if discardBtn.Hovered() {
											discardBtn.Background(ThemeHover(c))
										}
										if discardBtn.Clicked() {
											swarmPanelMu.Lock()
											hp.discardBeeID = curBee.ThreadID
											hp.discardConfirmOpen = true
											swarmPanelMu.Unlock()
											s.invalidate()
										}
										discardBtn.Children(func() {
											ui.Text(c, "放弃").
												FontSize(TypeFootnote).
												TextColor(ThemeDanger(c))
										})
									}
								} else if curBee.Merged {
									ui.Text(c, "已合并").
										FontSize(TypeFootnote).
										FontWeight(500).
										TextColor(ThemeSuccess(c))
								} else if curBee.MergeDiscarded {
									ui.Text(c, "已放弃合并").
										FontSize(TypeFootnote).
										TextColor(ThemeSecondaryText(c))
								} else {
									_, statusColor := swarmStatusTone(c, curBee.Status)
									ui.Text(c, swarmStatusLabel(curBee.Status)).
										FontSize(TypeFootnote).
										TextColor(statusColor)
								}

								// 把当前选中的 bee 弹出为独立窗口
								popOutBtn := ui.ButtonBase(c.Key("swarm-review-pop-out-btn")).
									Label("在独立窗口中打开").
									Tooltip("在独立窗口中打开").
									Radius(RadiusXS).
									Padding(Space1, Space2)
								if popOutBtn.Hovered() {
									popOutBtn.Background(ThemeHover(c))
								}
								if popOutBtn.Clicked() {
									s.popOutSwarmBee(curBee.ThreadID)
								}
								popOutBtn.Children(func() {
									ui.Icon(c, IconSquareArrowOutUpRight()).Size(Space4, Space4).TextColor(ThemeSecondaryText(c))
								})
							})

						// 审查决定层与 token 消耗
						if curBee.ReviewLayer != "" {
							layerText := reviewLayerLabel(curBee.ReviewLayer)
							if curBee.ReviewTokens > 0 {
								layerText += " · " + formatReviewTokenCount(curBee.ReviewTokens, curBee.ReviewTokensEstimated)
							}
							ui.Text(c, layerText).
								FontSize(TypeCaption).
								TextColor(ThemeSecondaryText(c))
						}

						// 审查意见：审查模型给出结论时展示原因
						if curBee.ReviewReason != "" {
							reasonLabel := "审查模型意见："
							if curBee.ReviewState == "rejected" {
								reasonLabel = "审查意见："
							}
							ui.Text(c, reasonLabel+curBee.ReviewReason).
								FontSize(TypeFootnote).
								TextColor(ThemeSecondaryText(c))
						}

						// 审查疑点：最多展示 10 条，避免撑爆面板
						if len(curBee.ReviewConcerns) > 0 {
							ui.Text(c, "审查疑点").
								FontSize(TypeCaption).
								TextColor(ThemeSecondaryText(c))
							for i, concern := range curBee.ReviewConcerns {
								if i >= 10 {
									break
								}
								ui.Text(c, "· "+concern).
									FontSize(TypeCaption).
									TextColor(ThemeSecondaryText(c))
							}
						}

						// 有 AI 审查会话时，提供跳转到审查会话的入口
						if curBee.ReviewThreadID != "" {
							reviewThreadBtn := ui.ButtonBase(c.Key("swarm-review-open-review-thread-btn")).
								Label("查看审查会话").
								Tooltip("查看审查会话").
								Radius(RadiusXS).
								Padding(Space1, Space2)
							if reviewThreadBtn.Hovered() {
								reviewThreadBtn.Background(ThemeHover(c))
							}
							if reviewThreadBtn.Clicked() {
								reviewThreadID := curBee.ReviewThreadID
								s.closeSwarmReview()
								s.selectThread(reviewThreadID)
							}
							reviewThreadBtn.Children(func() {
								ui.Row(c.Key("swarm-review-open-review-thread-btn-inner")).
									Gap(Space1).
									AlignItems(ui.Center).
									Children(func() {
										ui.Icon(c, IconEye()).Size(Space4, Space4).TextColor(ThemeSecondaryText(c))
										ui.Text(c, "查看审查会话").
											FontSize(TypeFootnote).
											TextColor(ThemeSecondaryText(c))
									})
							})
						}

						// 冲突文件列表
						if len(curBee.ConflictFiles) > 0 {
							conflictText := "冲突文件: " + strings.Join(curBee.ConflictFiles, ", ")
							ui.Text(c, conflictText).
								FontSize(TypeFootnote).
								TextColor(ThemeDanger(c))
						}

						// MergeError
						if curBee.MergeError != "" {
							ui.Text(c, "合并错误: "+curBee.MergeError).
								FontSize(TypeFootnote).
								TextColor(ThemeDanger(c))
						}
					})
			}

			// 下方差异文件列表
			if reviewLoading {
				ui.Row(c.Key("swarm-review-loading")).
					Gap(Space3).
					AlignItems(ui.Center).
					Padding(Space4, 0).
					Children(func() {
						renderRunningGlyph(c, "swarm-panel-run-review-loading", 14, ThemeAccent(c))
						ui.Text(c, "加载中…").
							FontSize(TypeBody).
							TextColor(ThemeSecondaryText(c))
					})
			} else if reviewErr != "" {
				ui.Row(c.Key("swarm-review-err")).
					Padding(Space3, Space3).
					Radius(RadiusSM).
					Background(ThemeDanger(c).Alpha(0.12)).
					Children(func() {
						ui.Text(c, reviewErr).
							FontSize(TypeFootnote).
							TextColor(ThemeDanger(c))
					})
			} else if len(reviewFiles) == 0 {
				ui.Row(c.Key("swarm-review-empty")).
					Padding(Space4, 0).
					Children(func() {
						ui.Text(c, "无改动").
							FontSize(TypeBody).
							TextColor(ThemeSecondaryText(c))
					})
			} else {
				renderDiffFileList(c, s, reviewDiff, reviewFiles)
			}
		})
}

// renderSwarmPanel 渲染 Swarm 浮动协作面板：不透明大圆角卡片，顶部条可拖拽，靠近左右边缘时吸附停靠。
func renderSwarmPanel(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	hp := getSwarmPanelState(s)
	if hp == nil {
		return
	}

	swarmPanelMu.Lock()
	open := hp.open
	loading := hp.loading
	swarmPanelMu.Unlock()

	if !open {
		return
	}

	s.mu.Lock()
	selectedID := s.selectedThreadID
	s.mu.Unlock()

	if selectedID == "" {
		return
	}

	swarmPanelMu.Lock()
	loading = hp.loading
	errMsg := hp.err
	st := hp.state
	reviewOpen := hp.reviewOpen
	swarmPanelMu.Unlock()

	panelMaxH := float32(280)
	if reviewOpen {
		panelMaxH = 420
	}

	winW, winH := c.Size()

	ui.Overlay(c, func() {
		card := ui.Column(c.Key("swarm-panel-root")).
			Width(swarmPanelWidth).
			MaxHeight(panelMaxH).
			Radius(swarmPanelRadius).
			Background(ThemeSurface(c)).
			BorderWidth(1).
			BorderColor(ThemeBorder(c)).
			Clip().
			Children(func() {
				ui.Scroll(c.Key("swarm-panel-scroll")).
					FillWidth().
					MaxHeight(panelMaxH).
					Padding(Space3, Space5).
					Children(func() {
						if loading && st.LeadThreadID == "" {
							ui.Row(c.Key("swarm-loading-row")).
								Gap(Space3).
								AlignItems(ui.Center).
								Margin(Space1, 0).
								Children(func() {
									renderRunningGlyph(c, "swarm-panel-run-loading", 14, ThemeAccent(c))
									ui.Text(c, "正在加载…").
										FontSize(TypeBody).
										TextColor(ThemeSecondaryText(c))
								})
							return
						}

						if errMsg != "" {
							ui.Row(c.Key("swarm-error-row")).
								Gap(Space3).
								AlignItems(ui.Center).
								Padding(Space2, Space3).
								Radius(RadiusSM).
								Background(ThemeDanger(c).Alpha(0.12)).
								Children(func() {
									ui.Text(c, errMsg).
										FontSize(TypeBody).
										TextColor(ThemeDanger(c))
									retryBtn := ui.ButtonBase(c.Key("swarm-retry-btn")).
										Label("重试").
										Radius(RadiusXS).
										Padding(Space1, Space2)
									if retryBtn.Hovered() {
										retryBtn.Background(ThemeHover(c))
									}
									if retryBtn.Clicked() {
										s.loadSwarm(selectedID)
									}
									retryBtn.Children(func() {
										ui.Text(c, "重试").
											FontSize(TypeFootnote).
											TextColor(ThemeAccent(c))
									})
								})
							return
						}

						if !st.Available {
							ui.Row(c.Key("swarm-unavailable-row")).
								Padding(Space2, Space3).
								Radius(RadiusSM).
								Background(ThemeHover(c)).
								Children(func() {
									ui.Text(c, "编排不可用：需要安装 git").
										FontSize(TypeBody).
										TextColor(ThemeSecondaryText(c))
								})
							return
						}

						if st.IsBee {
							leadShort := st.LeadThreadID
							if len(leadShort) > 8 {
								leadShort = leadShort[:8]
							}
							desc := "这是子代理会话，由主代理统一管理"
							if leadShort != "" {
								desc += " (" + leadShort + ")"
							}
							ui.Row(c.Key("swarm-is-bee-row")).
								Padding(Space2, Space3).
								Radius(RadiusSM).
								Background(ThemeHover(c)).
								Children(func() {
									ui.Text(c, desc).
										FontSize(TypeBody).
										TextColor(ThemeSecondaryText(c))
								})
							return
						}

						if reviewOpen {
							renderSwarmMergeReview(c, s, st)
							return
						}

						renderSwarmNormal(c, s, st)
					})
			})
		// 面板阴影统一取 ElevDialog，不再写死一档 rgba 阴影。
		card = applyElevation(c, card, ElevDialog)

		// 位置：默认贴窗口右侧、会话头下方，叠加用户拖拽的偏移；先限制在容器内，再按左右边缘吸附停靠。
		// 写回偏移后容器尺寸变化时只需重新 clamp，面板始终可见。
		baseX := winW - swarmPanelWidth - swarmPanelMargin
		swarmPanelMu.Lock()
		ox, oy := hp.offsetX, hp.offsetY
		swarmPanelMu.Unlock()
		x, y := dockSwarmPanelPos(baseX+ox, swarmPanelAnchorY+oy, swarmPanelWidth, panelMaxH, winW, winH)
		swarmPanelMu.Lock()
		hp.offsetX, hp.offsetY = x-baseX, y-swarmPanelAnchorY
		swarmPanelMu.Unlock()
		card.Absolute().Left(x).Top(y)
	})

	// 放弃合并的二次确认挂在根层、且在浮动面板之后渲染：嵌在面板卡片里会被裁剪，
	// 后渲染的弹窗同时能优先拿到 Esc。
	swarmPanelMu.Lock()
	discardOpen := hp.discardConfirmOpen
	discardBeeID := hp.discardBeeID
	swarmPanelMu.Unlock()
	RenderConfirmDialog(c, ConfirmDialog{
		Key:          modalDragSwarmDiscard,
		Open:         discardOpen,
		Tone:         ConfirmDanger,
		Title:        "放弃这些改动？",
		Body:         "子代理的改动不会合入工作区，此操作不可撤销",
		ConfirmLabel: "放弃改动",
		CancelLabel:  "取消",
		OnConfirm: func() {
			swarmPanelMu.Lock()
			hp.discardConfirmOpen = false
			hp.discardBeeID = ""
			swarmPanelMu.Unlock()
			s.discardSwarmMerge(discardBeeID)
		},
		OnCancel: func() {
			swarmPanelMu.Lock()
			hp.discardConfirmOpen = false
			hp.discardBeeID = ""
			swarmPanelMu.Unlock()
			s.invalidate()
		},
	})
}

// renderSwarmNormal 渲染 normal 状态下的 Swarm 面板。
func renderSwarmNormal(c *ui.Context, s *chatState, st SwarmState) {
	var hasRunning, hasFinished bool
	for _, h := range st.Bees {
		if h.Status == "running" {
			hasRunning = true
		} else {
			hasFinished = true
		}
	}
	// 只有排队条目时也要能停下来，否则它们仍会被后续 processQueue 启动。
	if st.Queued > 0 {
		hasRunning = true
	}

	// 顶部控制行：标题、进度、开启/关闭、停止全部、清理已结束；整行兼作浮动面板的拖拽手柄
	topRow := ui.Row(c.Key("swarm-top-row")).
		FillWidth().
		AlignItems(ui.Center).
		Gap(Space3).
		Margin(0, 0, Space3, 0).
		Cursor(ui.CursorGrab).
		Children(func() {
			ui.Text(c, "编排").
				FontSize(TypeProse).
				FontWeight(600).
				TextColor(ThemePrimaryText(c))

			progress := swarmProgressText(st)
			if progress != "" {
				ui.Text(c, progress).
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c))
			}

			ui.Spacer(c)

			if st.Enabled {
				plansOnlyLabel := "只规划"
				plansOnlyBtn := ui.ButtonBase(c.Key("swarm-plans-only-btn")).
					Label(plansOnlyLabel).
					Radius(RadiusSM).
					Padding(Space1, Space3)
				if st.PlansOnly {
					plansOnlyBtn.Background(ThemeAccent(c).Alpha(0.12)).
						Border(1, ThemeAccent(c).Alpha(0.4))
				} else if plansOnlyBtn.Hovered() {
					plansOnlyBtn.Background(ThemeHover(c))
				}
				if plansOnlyBtn.Clicked() {
					s.setSwarmPlansOnly(!st.PlansOnly)
				}
				plansOnlyBtn.Children(func() {
					txt := ui.Text(c, plansOnlyLabel).
						FontSize(TypeFootnote)
					if st.PlansOnly {
						txt.TextColor(ThemeAccent(c)).FontWeight(600)
					} else {
						txt.TextColor(ThemeSecondaryText(c))
					}
				})
			}

			toggleLabel := "开启"
			if st.Enabled {
				toggleLabel = "关闭"
			}
			toggleBtn := ui.ButtonBase(c.Key("swarm-toggle-btn")).
				Label(toggleLabel).
				Radius(RadiusSM).
				Padding(Space1, Space3)
			if toggleBtn.Hovered() {
				toggleBtn.Background(ThemeHover(c))
			}
			if toggleBtn.Clicked() {
				s.setSwarmEnabled(!st.Enabled)
			}
			toggleBtn.Children(func() {
				ui.Text(c, toggleLabel).
					FontSize(TypeFootnote).
					TextColor(ThemePrimaryText(c))
			})

			if hasRunning {
				stopAllBtn := ui.ButtonBase(c.Key("swarm-stop-all-btn")).
					Label("停止全部").
					Radius(RadiusSM).
					Padding(Space1, Space3)
				if stopAllBtn.Hovered() {
					stopAllBtn.Background(ThemeHover(c))
				}
				if stopAllBtn.Clicked() {
					s.stopAllSwarmBees()
				}
				stopAllBtn.Children(func() {
					ui.Text(c, "停止全部").
						FontSize(TypeFootnote).
						TextColor(ThemeDanger(c))
				})
			}

			if hasFinished {
				clearBtn := ui.ButtonBase(c.Key("swarm-clear-finished-btn")).
					Label("清理已结束").
					Radius(RadiusSM).
					Padding(Space1, Space3)
				if clearBtn.Hovered() {
					clearBtn.Background(ThemeHover(c))
				}
				if clearBtn.Clicked() {
					s.clearFinishedSwarmBees()
				}
				clearBtn.Children(func() {
					ui.Text(c, "清理已结束").
						FontSize(TypeFootnote).
						TextColor(ThemeSecondaryText(c))
				})
			}

			// 把标题栏当前的 bee 弹出为独立窗口
			if beeID := swarmPopOutBeeID(st); beeID != "" {
				popOutBtn := ui.ButtonBase(c.Key("swarm-pop-out-btn")).
					Label("在独立窗口中打开").
					Tooltip("在独立窗口中打开").
					Radius(RadiusSM).
					Padding(Space1, Space2)
				if popOutBtn.Hovered() {
					popOutBtn.Background(ThemeHover(c))
				}
				if popOutBtn.Clicked() {
					s.popOutSwarmBee(beeID)
				}
				popOutBtn.Children(func() {
					ui.Icon(c, IconSquareArrowOutUpRight()).Size(13, 13).TextColor(ThemeSecondaryText(c))
				})
			}
		})

	swarmDragHandle(s, topRow)

	// 重启后被中断的子代理/排队条目：在 bee 列表上方给出一条续跑横幅；
	// 子代理自己的会话视图由 renderSwarmPanel 提前返回，这里再兜一层 IsBee 判断。
	if !st.IsBee {
		leadID := strings.TrimSpace(st.LeadThreadID)
		if leadID == "" {
			leadID = s.swarmTargetLeadID()
		}
		renderSwarmInterruptedBanner(c, s, leadID, st.Interrupted)
	}

	if len(st.Bees) == 0 {
		ui.Row(c.Key("swarm-empty-bees")).
			Padding(Space3, 0).
			Children(func() {
				ui.Text(c, "暂无子代理；开启后主代理派发的子代理会显示在这里").
					FontSize(TypeBody).
					TextColor(ThemeSecondaryText(c))
			})
		return
	}

	ui.Column(c.Key("swarm-bees-list")).
		FillWidth().
		Gap(Space2).
		Children(func() {
			for i, h := range st.Bees {
				renderSwarmBeeRow(c, s, h, i)
			}
		})
}

// swarmTierName 返回指定档位 ID 在全局设置中配置的展示名称；未配置或未找到时回退为 tierID 本身。
func swarmTierName(s *chatState, tierID string) string {
	st := getChatSettings(s)
	if st != nil {
		id := strings.TrimSpace(tierID)
		for _, t := range st.cfg.Orchestration.Tiers {
			if strings.TrimSpace(t.ID) == id {
				if strings.TrimSpace(t.Name) != "" {
					return t.Name
				}
				return t.ID
			}
		}
	}
	return tierID
}

// renderSwarmBeeRow 渲染单个 bee 卡片。
func renderSwarmBeeRow(c *ui.Context, s *chatState, h SwarmBee, idx int) {
	key := fmt.Sprintf("swarm-bee-%d-%s", h.Index, h.ThreadID)
	ui.Column(c.Key(key)).
		FillWidth().
		Padding(Space3, Space3).
		Radius(RadiusSM).
		Background(ThemeSurface(c)).
		BorderWidth(1).
		BorderColor(ThemeBorder(c)).
		Gap(Space1).
		Children(func() {
			ui.Row(c.Key(key + "-meta")).
				FillWidth().
				AlignItems(ui.Center).
				Gap(Space3).
				Children(func() {
					// #Index Name
					title := fmt.Sprintf("#%d %s", h.Index, h.Name)
					ui.Text(c, title).
						FontSize(TypeBody).
						FontWeight(600).
						TextColor(ThemePrimaryText(c))

					if h.Tier != "" {
						renderSwarmTierChip(c, "bee-tier-"+h.ThreadID, swarmTierName(s, h.Tier), h.Tier, h.ReadOnly)
					}

					// status label in status color
					_, statusColor := swarmStatusTone(c, h.Status)
					ui.Text(c, swarmStatusLabel(h.Status)).
						FontSize(TypeFootnote).
						FontWeight(500).
						TextColor(statusColor)

					// provider/model small (show short model name: last path segment of Model)
					provModel := strings.TrimSpace(h.Provider)
					shortModel := strings.TrimSpace(h.Model)
					if idx := strings.LastIndex(shortModel, "/"); idx != -1 {
						shortModel = shortModel[idx+1:]
					}
					if shortModel != "" {
						if provModel != "" {
							provModel += " / " + shortModel
						} else {
							provModel = shortModel
						}
					}
					if provModel != "" {
						ui.Text(c, provModel).
							FontSize(TypeCaption).
							TextColor(ThemeSecondaryText(c))
					}

					if h.DirectModel {
						ui.Row(c.Key(key+"-direct-model-tag")).
							BorderWidth(1).
							BorderColor(ThemeAccent(c).Alpha(0.5)).
							Radius(RadiusXS).
							Padding(Space0, Space1).
							Tooltip("用户在消息里点名了这个模型").
							Children(func() {
								ui.Text(c, "指定模型").
									FontSize(TypeCaption).
									TextColor(ThemeAccent(c))
							})
					}

					// tokens & toolCalls
					usageText := fmt.Sprintf("%s tokens · %d 工具调用", formatTokens(int64(h.Tokens)), h.ToolCalls)
					ui.Text(c, usageText).
						FontSize(TypeCaption).
						TextColor(ThemeSecondaryText(c))

					// 越权写入 tag when ReadOnlyViolation
					if h.ReadOnlyViolation {
						ui.Row(c.Key(key+"-read-only-violation-tag")).
							Padding(Space0, Space1).
							Radius(RadiusXS).
							Background(ThemeDanger(c).Alpha(0.15)).
							Tooltip("只读档子代理改动了文件，改动已丢弃，未合并").
							Children(func() {
								ui.Text(c, "越权写入").
									FontSize(TypeCaption).
									FontWeight(600).
									TextColor(ThemeDanger(c))
							})
					} else if h.Credibility != nil && h.Credibility.Cited > 0 {
						badgeColor := ThemeSuccess(c)
						badgeIcon := IconCircleCheck()
						if h.Credibility.Verified != h.Credibility.Cited ||
							strings.Contains(h.Credibility.Summary, "未读却引用") ||
							strings.Contains(h.Credibility.Summary, "不可核实") {
							badgeColor = ThemeWarning(c)
							badgeIcon = IconOctagonAlert()
						}
						ui.Row(c.Key(key+"-credibility-badge")).
							Padding(Space0, Space2).
							Radius(RadiusXS).
							Gap(Space1).
							AlignItems(ui.Center).
							Background(badgeColor.Alpha(0.15)).
							Tooltip("可信度：" + h.Credibility.Summary).
							Children(func() {
								ui.Icon(c, badgeIcon).
									Size(10, 10).
									TextColor(badgeColor)
								ui.Text(c, fmt.Sprintf("%d/%d", h.Credibility.Verified, h.Credibility.Cited)).
									FontSize(TypeCaption).
									FontWeight(600).
									TextColor(badgeColor)
							})
					}

					// 审查状态 tag：审查中、审查未通过或待审查
					if h.ReviewState == "running" {
						ui.Row(c.Key(key+"-review-running-tag")).
							Padding(Space0, Space1).
							Radius(RadiusXS).
							Background(ThemeAccent(c).Alpha(0.15)).
							Children(func() {
								ui.Text(c, "审查中").
									FontSize(TypeCaption).
									TextColor(ThemeAccent(c))
							})
					} else if h.ReviewState == "rejected" {
						ui.Row(c.Key(key+"-review-rejected-tag")).
							Padding(Space0, Space1).
							Radius(RadiusXS).
							Background(ThemeDanger(c).Alpha(0.15)).
							Children(func() {
								ui.Text(c, "审查未通过").
									FontSize(TypeCaption).
									TextColor(ThemeDanger(c))
							})
					} else if h.PendingMerge {
						ui.Row(c.Key(key+"-pending-merge-tag")).
							Padding(Space0, Space1).
							Radius(RadiusXS).
							Background(ThemeAccent(c).Alpha(0.15)).
							Children(func() {
								ui.Text(c, "待审查").
									FontSize(TypeCaption).
									TextColor(ThemeAccent(c))
							})
					}

					// 已合并 tag when Merged
					if h.Merged {
						ui.Row(c.Key(key+"-merged-tag")).
							Padding(Space0, Space1).
							Radius(RadiusXS).
							Background(ThemeSuccess(c).Alpha(0.15)).
							Children(func() {
								ui.Text(c, "已合并").
									FontSize(TypeCaption).
									TextColor(ThemeSuccess(c))
							})
					}

					// 已放弃合并 tag when MergeDiscarded
					if h.MergeDiscarded {
						ui.Row(c.Key(key+"-discarded-tag")).
							Padding(Space0, Space1).
							Radius(RadiusXS).
							Background(ThemeSecondaryText(c).Alpha(0.15)).
							Children(func() {
								ui.Text(c, "已放弃合并").
									FontSize(TypeCaption).
									TextColor(ThemeSecondaryText(c))
							})
					}

					ui.Spacer(c)

					// 审查按钮 (PendingMerge)
					if h.PendingMerge {
						reviewBtn := ui.ButtonBase(c.Key(key+"-review-btn")).
							Label("审查").
							Radius(RadiusXS).
							Padding(Space1, Space2).
							Background(ThemeAccent(c))
						if reviewBtn.Hovered() {
							reviewBtn.Background(ThemeAccent(c).Alpha(0.85))
						}
						if reviewBtn.Clicked() {
							s.openSwarmReview(h.ThreadID)
						}
						reviewBtn.Children(func() {
							ui.Text(c, "审查").
								FontSize(TypeCaption).
								TextColor(ThemeOnAccent(c))
						})
					} else if (h.Merged || len(h.ConflictFiles) > 0) && (h.Status == "completed" || h.Status == "failed") {
						viewBtn := ui.ButtonBase(c.Key(key+"-view-diff-btn")).
							Label("查看改动").
							Radius(RadiusXS).
							Padding(Space1, Space2)
						if viewBtn.Hovered() {
							viewBtn.Background(ThemeHover(c))
						}
						if viewBtn.Clicked() {
							s.openSwarmReview(h.ThreadID)
						}
						viewBtn.Children(func() {
							ui.Text(c, "查看改动").
								FontSize(TypeCaption).
								TextColor(ThemeSecondaryText(c))
						})
					}

					// 停止 button only when status is running
					if h.Status == "running" {
						stopBtn := ui.ButtonBase(c.Key(key+"-stop-btn")).
							Label("停止").
							Radius(RadiusXS).
							Padding(Space1, Space2)
						if stopBtn.Hovered() {
							stopBtn.Background(ThemeHover(c))
						}
						if stopBtn.Clicked() {
							s.stopSwarmBee(h.ThreadID)
						}
						stopBtn.Children(func() {
							ui.Text(c, "停止").
								FontSize(TypeCaption).
								TextColor(ThemeDanger(c))
						})
					}
				})

			// preview text 2 lines max in secondary color
			preview := swarmBeePreview(h)
			if preview != "" {
				ui.Text(c, preview).
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c))
			}

			// conflict files line in error color
			if len(h.ConflictFiles) > 0 {
				conflictText := "冲突文件: " + strings.Join(h.ConflictFiles, ", ")
				ui.Text(c, conflictText).
					FontSize(TypeFootnote).
					TextColor(ThemeDanger(c))
			}

			// MergeError in error color
			if h.MergeError != "" {
				ui.Text(c, "合并错误: "+h.MergeError).
					FontSize(TypeFootnote).
					TextColor(ThemeDanger(c))
			}
		})
}

// swarmNoticePrefix 与 swarmReportPrefix 必须与 internal/swarm 的 NoticePrefix、ReportPrefix 一致，
// 因为通知与汇报以普通 user 消息下发、没有结构化字段，只能靠前缀识别。
// legacy 前缀是改名前写入历史会话的版本，识别时需兼容。
const (
	swarmNoticePrefix       = "[编排] "
	swarmReportPrefix       = swarmNoticePrefix + "子代理汇报："
	legacySwarmNoticePrefix = "[Hydra] "
	legacySwarmReportPrefix = legacySwarmNoticePrefix + "协作者汇报："
	// swarmReportIDPrefix 与 swarmReportTruncatedPrefix 必须与 internal/swarm 的 ReportIDPrefix、
	// ReportTruncatedPrefix 一致：汇报总结超长被截断时，后端在总结前加 ID 字段、在总结末尾加提示行。
	swarmReportIDPrefix        = "- ID："
	swarmReportTruncatedPrefix = "（汇报总结已截断："
)

// isSwarmNoticeText 判断文本是否为 mcode 下发的编排通知（含汇报）。
func isSwarmNoticeText(text string) bool {
	return strings.HasPrefix(text, strings.TrimSpace(swarmNoticePrefix)) || strings.HasPrefix(text, strings.TrimSpace(legacySwarmNoticePrefix))
}

// swarmStripNoticePrefix 去掉正文开头的编排通知前缀，时间线上不显示「[编排] 」「[Hydra] 」这类内部标记。
func swarmStripNoticePrefix(text string) string {
	trimmed := strings.TrimPrefix(text, swarmNoticePrefix)
	trimmed = strings.TrimPrefix(trimmed, legacySwarmNoticePrefix)
	return trimmed
}

// isSwarmReportMessage 判断某条消息是否为子代理汇报消息。
func isSwarmReportMessage(m Message) bool {
	if m.Role != "user" {
		return false
	}
	text := strings.TrimSpace(m.Text)
	return strings.HasPrefix(text, swarmReportPrefix) || strings.HasPrefix(text, legacySwarmReportPrefix)
}

// swarmReportEntry 记录从汇报文本中解析出的单个 Bee 汇报项。
type swarmReportEntry struct {
	Name          string
	Task          string
	Status        string
	MergeResult   string
	Summary       string
	ChangedFiles  []string
	ConflictFiles []string
	filesRecorded bool

	// 统计行由后端追加在文件列表之后，字段都带 omitempty，整行可能缺失。
	DurationMs    int64
	ToolCalls     int
	LinesAdded    int
	LinesDeleted  int
	statsRecorded bool

	Tier        string
	Model       string
	DirectModel bool
	Violation   string
	Credibility string

	// BeeID 只在汇报总结被截断时随汇报下发，展开卡片时凭它从存档读全文；
	// Truncated 表示 Summary 只是开头一段，截断提示行已从 Summary 里去掉。
	BeeID     string
	Truncated bool
}

// swarmReportParse 是缓存下来的一次汇报解析结果。
type swarmReportParse struct {
	size    int    // 解析时的正文长度
	sum     uint64 // 解析时的正文哈希，和长度一起盖住正文内容
	title   string
	entries []swarmReportEntry
}

// swarmReportParseKey 是汇报解析缓存的键：消息 ID 不足以区分不同会话/不同窗口里同名的消息，
// 正文长度也不足以区分长度相同的两份不同正文，两者必须同时参与。
type swarmReportParseKey struct {
	id  string
	sum uint64
}

// swarmReportTextHash 返回正文的 FNV-1a 64 位哈希，用纯字节循环实现，不为哈希另建分配。
func swarmReportTextHash(text string) uint64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	for i := 0; i < len(text); i++ {
		h ^= uint64(text[i])
		h *= prime64
	}
	return h
}

// swarmReportParseCache 按消息 ID + 正文内容哈希缓存汇报解析结果：汇报消息是已经定稿的文字（可达几十 KB），
// 时间线每帧都重新 Split + 逐行扫有真实开销，这里按“消息 ID + 正文长度与哈希”缓存，正文不变就只取结果。
// 只按消息 ID + 长度缓存会让不同会话里 ID、长度相同而正文不同的消息串数据（测试与多窗口会撞上），
// 所以哈希也必须参与。全局锁只在渲染汇报胶囊时短暂持有。
var (
	swarmReportParseMu    sync.Mutex
	swarmReportParseCache = make(map[swarmReportParseKey]swarmReportParse)
)

// swarmReportParseMaxCache 是汇报解析缓存的最大条目数，超过后整表清空重来，避免长期运行时无限增长。
const swarmReportParseMaxCache = 512

// parseSwarmReportCached 按消息 ID + 正文内容缓存 parseSwarmReport 的结果。
// 返回的 entries 切片与缓存共用，调用方只能读。
func parseSwarmReportCached(id, text string) (title string, entries []swarmReportEntry) {
	if id == "" {
		return parseSwarmReport(text)
	}
	size, sum := len(text), swarmReportTextHash(text)
	key := swarmReportParseKey{id: id, sum: sum}
	swarmReportParseMu.Lock()
	if cached, ok := swarmReportParseCache[key]; ok && cached.size == size && cached.sum == sum {
		swarmReportParseMu.Unlock()
		return cached.title, cached.entries
	}
	swarmReportParseMu.Unlock()

	title, entries = parseSwarmReport(text)

	swarmReportParseMu.Lock()
	if len(swarmReportParseCache) >= swarmReportParseMaxCache {
		swarmReportParseCache = make(map[swarmReportParseKey]swarmReportParse)
	}
	swarmReportParseCache[key] = swarmReportParse{size: size, sum: sum, title: title, entries: entries}
	swarmReportParseMu.Unlock()
	return title, entries
}

// parseSwarmReport 解析 Swarm 汇报纯文本，提取标题与每个 Bee 的结构化汇报信息。
func parseSwarmReport(text string) (title string, entries []swarmReportEntry) {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 {
		return "", nil
	}

	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed != "" {
			title = strings.TrimPrefix(strings.TrimPrefix(trimmed, swarmNoticePrefix), legacySwarmNoticePrefix)
			break
		}
	}

	var curEntry *swarmReportEntry
	var inSummary bool
	var summaryLines []string
	var fence byte
	var fenceLength int
	appendSummary := func(line string) {
		summaryLines = append(summaryLines, line)
		trimmed := strings.TrimSpace(line)
		if len(trimmed) < 3 || trimmed[0] != '`' && trimmed[0] != '~' {
			return
		}
		n := 0
		for n < len(trimmed) && trimmed[n] == trimmed[0] {
			n++
		}
		if n < 3 {
			return
		}
		if fence == 0 {
			fence, fenceLength = trimmed[0], n
		} else if trimmed[0] == fence && n >= fenceLength && strings.TrimSpace(trimmed[n:]) == "" {
			fence, fenceLength = 0, 0
		}
	}

	flushEntry := func() {
		if curEntry != nil {
			if len(summaryLines) > 0 {
				curEntry.Summary = strings.TrimSpace(strings.Join(summaryLines, "\n"))
			}
			entries = append(entries, *curEntry)
			curEntry = nil
			summaryLines = nil
			inSummary = false
			fence, fenceLength = 0, 0
		}
	}

	for i, rawLine := range lines {
		line := strings.TrimRight(rawLine, "\r")
		trimmed := strings.TrimSpace(line)
		nextIsMeta := false
		if i+1 < len(lines) {
			nxt := strings.TrimSpace(lines[i+1])
			nextIsMeta = strings.HasPrefix(nxt, "- 档位：") || strings.HasPrefix(nxt, "- 档位:") ||
				strings.HasPrefix(nxt, "- 越权写入：") || strings.HasPrefix(nxt, "- 越权写入:") ||
				strings.HasPrefix(nxt, "- 可信度：") || strings.HasPrefix(nxt, "- 可信度:") ||
				strings.HasPrefix(nxt, "- 合并结果：") || strings.HasPrefix(nxt, "- 合并结果:") ||
				strings.HasPrefix(nxt, "- 改动文件：")
		}
		// 新汇报附带文件元数据，可在上一个 bee 的代码块被截断时恢复分段。
		hasFileSnapshot := false
		for k := i + 1; k < len(lines) && k <= i+5; k++ {
			if strings.HasPrefix(strings.TrimSpace(lines[k]), "- 改动文件：") {
				hasFileSnapshot = true
				break
			}
		}
		if (fence == 0 || hasFileSnapshot) && strings.HasPrefix(trimmed, "## ") && nextIsMeta && strings.HasSuffix(trimmed, ")") {
			entryContent := strings.TrimSpace(strings.TrimPrefix(trimmed, "## "))
			name, taskWithStatus, found := strings.Cut(entryContent, "：")
			if !found {
				name, taskWithStatus, _ = strings.Cut(entryContent, ":")
			}
			task := strings.TrimSpace(taskWithStatus)
			status := ""
			if strings.HasSuffix(task, ")") {
				if idx := strings.LastIndex(task, " ("); idx != -1 {
					status = task[idx+2 : len(task)-1]
					task = strings.TrimSpace(task[:idx])
				}
			}
			if strings.TrimSpace(name) != "" && (status == "已完成" || status == "失败" || status == "已停止" || status == "运行中") {
				flushEntry()
				curEntry = &swarmReportEntry{
					Name:   strings.TrimSpace(name),
					Task:   task,
					Status: status,
				}
				continue
			}
		}

		if curEntry == nil {
			continue
		}

		if inSummary {
			// 截断提示行只出现在带 ID 字段的条目里，界面不显示这一行。
			if curEntry.BeeID != "" && strings.HasPrefix(trimmed, swarmReportTruncatedPrefix) {
				curEntry.Truncated = true
				continue
			}
			appendSummary(line)
			continue
		}

		if id, ok := strings.CutPrefix(trimmed, swarmReportIDPrefix); ok {
			curEntry.BeeID = strings.TrimSpace(id)
			continue
		}

		if strings.HasPrefix(trimmed, "- 档位：") || strings.HasPrefix(trimmed, "- 档位:") {
			tierRest := strings.TrimPrefix(trimmed, "- 档位：")
			tierRest = strings.TrimPrefix(tierRest, "- 档位:")
			if strings.Contains(tierRest, "用户指定模型") {
				curEntry.DirectModel = true
				tierRest = strings.ReplaceAll(tierRest, "· 用户指定模型", "")
				tierRest = strings.ReplaceAll(tierRest, "用户指定模型", "")
			}
			if idx := strings.Index(tierRest, "模型："); idx != -1 {
				curEntry.Model = strings.Trim(strings.TrimSpace(tierRest[idx+len("模型："):]), " ·")
				curEntry.Tier = strings.Trim(strings.TrimSpace(tierRest[:idx]), " ·")
			} else if idx := strings.Index(tierRest, "模型:"); idx != -1 {
				curEntry.Model = strings.Trim(strings.TrimSpace(tierRest[idx+len("模型:"):]), " ·")
				curEntry.Tier = strings.Trim(strings.TrimSpace(tierRest[:idx]), " ·")
			} else {
				curEntry.Tier = strings.Trim(strings.TrimSpace(tierRest), " ·")
			}
			inSummary = false
			continue
		}

		if strings.HasPrefix(trimmed, "- 越权写入：") || strings.HasPrefix(trimmed, "- 越权写入:") {
			v := strings.TrimPrefix(trimmed, "- 越权写入：")
			v = strings.TrimPrefix(v, "- 越权写入:")
			curEntry.Violation = strings.TrimSpace(v)
			inSummary = false
			continue
		}

		if strings.HasPrefix(trimmed, "- 可信度：") || strings.HasPrefix(trimmed, "- 可信度:") {
			cred := strings.TrimPrefix(trimmed, "- 可信度：")
			cred = strings.TrimPrefix(cred, "- 可信度:")
			curEntry.Credibility = strings.TrimSpace(cred)
			inSummary = false
			continue
		}

		if strings.HasPrefix(trimmed, "- 合并结果：") || strings.HasPrefix(trimmed, "- 合并结果:") {
			res := strings.TrimPrefix(trimmed, "- 合并结果：")
			res = strings.TrimPrefix(res, "- 合并结果:")
			curEntry.MergeResult = strings.TrimSpace(res)
			inSummary = false
			continue
		}

		if strings.HasPrefix(trimmed, "- 汇报总结：") || strings.HasPrefix(trimmed, "- 汇报总结:") {
			sumLine := strings.TrimPrefix(trimmed, "- 汇报总结：")
			sumLine = strings.TrimPrefix(sumLine, "- 汇报总结:")
			appendSummary(sumLine)
			inSummary = true
			continue
		}

		if files, ok := strings.CutPrefix(trimmed, "- 改动文件："); ok {
			curEntry.filesRecorded = json.Unmarshal([]byte(files), &curEntry.ChangedFiles) == nil
		}
		if files, ok := strings.CutPrefix(trimmed, "- 冲突文件："); ok {
			_ = json.Unmarshal([]byte(files), &curEntry.ConflictFiles)
		}
		if stats, ok := strings.CutPrefix(trimmed, "- 统计："); ok {
			var parsed struct {
				DurationMs   int64 `json:"durationMs"`
				ToolCalls    int   `json:"toolCalls"`
				LinesAdded   int   `json:"linesAdded"`
				LinesDeleted int   `json:"linesDeleted"`
			}
			if json.Unmarshal([]byte(stats), &parsed) == nil {
				curEntry.DurationMs = parsed.DurationMs
				curEntry.ToolCalls = parsed.ToolCalls
				curEntry.LinesAdded = parsed.LinesAdded
				curEntry.LinesDeleted = parsed.LinesDeleted
				curEntry.statsRecorded = true
			}
		}
	}
	flushEntry()
	return title, entries
}

// swarmReportStatusSummary 按状态计数生成如 "2 已完成 · 1 失败" 的汇总文本。
// 顺序依次为已完成、失败、已停止、运行中，计数为 0 的跳过。
func swarmReportStatusSummary(entries []swarmReportEntry) string {
	counts := make(map[string]int)
	for _, e := range entries {
		counts[e.Status]++
	}

	order := []string{"已完成", "失败", "已停止", "运行中"}
	var parts []string
	for _, status := range order {
		if c := counts[status]; c > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c, status))
		}
	}
	return strings.Join(parts, " · ")
}

// swarmReportCaptionParts 汇总表头状态说明需要的数字：耗时、工具数、文件数与增删行数。
// 汇报里的统计行优先，缺失时退回内存中的 bee 信息；增删行数只在统计行里有。
func swarmReportCaptionParts(e swarmReportEntry, h *SwarmBee) (duration string, tools, files, added, deleted int) {
	switch {
	case e.statsRecorded && e.DurationMs > 0:
		end := time.Unix(0, 0)
		duration = formatSwarmDuration(end.Add(-time.Duration(e.DurationMs)*time.Millisecond), &end)
	case h != nil && h.FinishedAt != nil && !h.StartedAt.IsZero():
		duration = formatSwarmDuration(h.StartedAt, h.FinishedAt)
	}

	if e.statsRecorded {
		tools = e.ToolCalls
	} else if h != nil {
		tools = h.ToolCalls
	}

	if e.filesRecorded {
		files = len(e.ChangedFiles)
	} else if h != nil {
		files = len(h.ChangedFiles)
	}

	added = e.LinesAdded
	deleted = e.LinesDeleted
	return duration, tools, files, added, deleted
}

// swarmAvatarColors 是名册头像按名字哈希取用的固定八色，依次为蓝、绿、紫、橙、靛蓝、粉红、蓝绿、棕；
// 取自 identityPalette 的人设色，与浮卡外发光、名字同一套色，同一名字颜色恒定。
var swarmAvatarColors = []ui.Color{
	ThemeIdentity("isle"),
	ThemeIdentity("yarrow"),
	ThemeIdentity("sage"),
	ThemeIdentity("tide"),
	ThemeIdentity("zephyr"),
	ThemeIdentity("jade"),
	ThemeIdentity("onyx"),
	ThemeIdentity("wren"),
}

// swarmAvatarColor 根据名字哈希返回一组固定的主题色，保证同一名字的颜色稳定。
func swarmAvatarColor(name string) ui.Color {
	if len(swarmAvatarColors) == 0 {
		return ThemeIdentity("isle")
	}
	var h uint32
	for _, b := range []byte(name) {
		h = h*31 + uint32(b)
	}
	return swarmAvatarColors[int(h)%len(swarmAvatarColors)]
}

func swarmStatusFromChinese(status string) string {
	switch status {
	case "已完成":
		return "completed"
	case "失败":
		return "failed"
	case "已停止":
		return "stopped"
	case "运行中":
		return "running"
	default:
		return status
	}
}

// renderSwarmAvatarStack 渲染重叠头像组，每个 entry 一个直径约 22 的圆形，圆内显示名字首字符（按 rune 取）。
// 最多显示 4 个，超出部分用一个 +N 圆表示；后续头像以负外边距重叠 4px。
func renderSwarmAvatarStack(c *ui.Context, keyPrefix string, entries []swarmReportEntry) {
	const maxAvatars = 4
	total := len(entries)
	showCount := total
	hasMore := false
	moreCount := 0
	if total > maxAvatars {
		showCount = maxAvatars
		hasMore = true
		moreCount = total - maxAvatars
	}

	ui.Row(c.Key(keyPrefix + "-avatars")).
		AlignItems(ui.Center).
		Gap(0).
		Children(func() {
			for i := 0; i < showCount; i++ {
				entry := entries[i]
				avatarKey := fmt.Sprintf("%s-avatar-%d", keyPrefix, i)
				margin := float32(0)
				if i > 0 {
					margin = -4
				}
				Avatar(c, AvatarProps{
					Key: avatarKey, Name: entry.Name, Size: 22, FontSize: TypeCaption,
					Foreground: ThemeOnAccent(c), Background: swarmAvatarColor(entry.Name), Border: ThemeSurface(c),
				}).Margin(0, 0, 0, margin)
			}
			if hasMore {
				moreKey := fmt.Sprintf("%s-avatar-more", keyPrefix)
				Avatar(c, AvatarProps{
					Key: moreKey, Text: fmt.Sprintf("+%d", moreCount), Size: 22, FontSize: TypeCaption,
					Foreground: ThemeSecondaryText(c), Background: ThemeSecondaryText(c).Alpha(0.18), Border: ThemeSurface(c),
				}).Margin(0, 0, 0, -4)
			}
		})
}

// shouldKeepSwarmReportOpen 决定点击汇报按钮后浮窗的下一状态。
// 浮窗打开时点按钮，PopoverBase 已先把状态置为 false 并把这次按下传给按钮；
// 此时按钮的 Clicked 不能再把它打开，否则会抵消关闭（表现为点浮窗外部也关不掉）。
// opened 是本帧按钮读取到的状态，dismissed 表示浮窗刚被 PopoverBase 关闭。
func shouldKeepSwarmReportOpen(opened, dismissed bool) bool {
	return !opened && !dismissed
}

// renderSwarmReportCapsule 渲染汇报行中的一枚胶囊及其详情浮窗。
// key 同时用作控件 key 后缀与 reportOpen/reportDismissed 的 map 键，
// view 描述胶囊内容，body 渲染浮窗里的内容（maxH 是浮窗在当前窗口里可用的最大高度）。
func renderSwarmReportCapsule(c *ui.Context, s *chatState, hp *swarmPanelState, key string, view swarmCapsuleView, body func(maxH float32)) {
	swarmPanelMu.Lock()
	if hp != nil && hp.reportOpen == nil {
		hp.reportOpen = make(map[string]bool)
	}
	if hp != nil && hp.reportDismissed == nil {
		hp.reportDismissed = make(map[string]bool)
	}
	opened := false
	dismissed := false
	if hp != nil {
		opened = hp.reportOpen[key]
		dismissed = hp.reportDismissed[key]
	}
	swarmPanelMu.Unlock()

	// 胶囊外观由 renderSwarmCapsuleButton 统一负责，与运行中的子线程胶囊完全一致。
	open, wasDismissed := renderSwarmCapsuleButton(c, key, view, opened, dismissed, func(maxH float32) {
		if s != nil {
			s.ensureSwarm(c)
		}
		// 表头固定，滚动交给正文自己处理，所以这里不再套 Scroll。
		body(maxH)
	})

	swarmPanelMu.Lock()
	if hp != nil {
		hp.reportOpen[key] = open
		// PopoverBase 在点外部或 Esc 时关闭浮窗；同一轮里按钮也可能收到 Clicked，
		// 记录 dismissed 让这一步的 Clicked 不再把它重新打开。
		hp.reportDismissed[key] = wasDismissed
	}
	swarmPanelMu.Unlock()
}

// swarmReportStatusColor 返回汇报条目状态点的颜色，与详情卡片里的状态色保持一致。
func swarmReportStatusColor(c *ui.Context, status string) ui.Color {
	switch status {
	case "已完成":
		return ThemeSuccess(c)
	case "失败":
		return ThemeDanger(c)
	case "已停止":
		return ThemeWarning(c)
	default:
		return ThemeSecondaryText(c)
	}
}

// swarmReportPillText 生成汇报胶囊的文字：名字加结果后缀，名字为空时用“子代理”。
func swarmReportPillText(e swarmReportEntry) string {
	name := strings.TrimSpace(e.Name)
	if name == "" {
		name = "子代理"
	}
	switch e.Status {
	case "失败":
		return name + " 失败"
	case "已停止":
		return name + " 已停止"
	default:
		return name + " 已完成工作"
	}
}

// renderSwarmReportAvatar 渲染汇报里的单人头像：直径 size 的圆形，圆内是名字首字符。
func renderSwarmReportAvatar(c *ui.Context, key, name string, size, fontSize float32) {
	Avatar(c, AvatarProps{
		Key: key, Name: name, Size: size, FontSize: fontSize,
		Foreground: ThemeOnAccent(c), Background: swarmAvatarColor(name),
	})
}

// renderSwarmReportCopyButton 渲染表头右侧的汇总复制按钮：只显示图标，复制后短暂换成对勾。
func renderSwarmReportCopyButton(c *ui.Context, s *chatState, key, text string) {
	copyKey := key + "-copy"
	copied := copyFeedbackActive(copyKey, time.Now())
	CopyButton(c, CopyButtonProps{
		Key: copyKey, Label: "复制汇报", Tooltip: "复制汇报", Mode: CopyIcon, Copied: copied,
		Value: func() string { return text }, OnCopy: func() { markCopyFeedback(s, copyKey) },
	})
}

// renderSwarmReportRow 渲染时间线中的 Swarm 汇报行：相邻的汇报消息合并成一行，
// 每个 bee 一枚胶囊，胶囊横向流式排列、放不下时自动换行；点开各自弹出详情卡片。
func renderSwarmReportRow(c *ui.Context, s *chatState, msgs ...Message) ui.Element {
	if len(msgs) == 0 {
		return ui.Element{}
	}
	hp := getSwarmPanelState(s)
	if swarmReportMergedIntoLeadCapsule(s, hp) {
		// 总管会话里这些汇报都已经有原地更新的胶囊：整行都无需显示时直接不渲染，不再多一条汇报行。
		merged := true
		for _, m := range msgs {
			if !swarmReportMessageMerged(hp, m) {
				merged = false
				break
			}
		}
		if merged {
			return ui.Element{}
		}
	}
	return renderSwarmCapsuleFlow(c, "msg-swarm-report-"+msgs[0].ID, func() {
		for _, m := range msgs {
			renderSwarmReportMessageCapsules(c, s, hp, m)
		}
	})
}

// swarmReportMergedIntoLeadCapsule 判断汇报是否已由时间线上的子线程胶囊展示：总管主会话作为 Lead 时，
// 它派出的子代理在时间线上已有一枚从「运行中」原地变成「已完成」的胶囊，点开就是汇报，
// 同一份汇报不再另起一行；普通会话保持原有的汇报行。
func swarmReportMergedIntoLeadCapsule(s *chatState, hp *swarmPanelState) bool {
	if s == nil || hp == nil {
		return false
	}
	stewardID := s.stewardThreadID()
	if stewardID == "" {
		return false
	}
	// stewardThreadID 内部不加锁，先取到再进临界区，避免与渲染路径互相等待。
	swarmPanelMu.Lock()
	lead, isBee := hp.state.LeadThreadID, hp.state.IsBee
	swarmPanelMu.Unlock()
	return !isBee && lead == stewardID
}

// swarmCapsuleHasBee 判断时间线胶囊行是否已经在展示同名子代理：一个 Lead 内的子代理名字唯一，
// 按名字即可判定这份汇报是否已有对应的胶囊。
func swarmCapsuleHasBee(hp *swarmPanelState, name string) bool {
	name = strings.TrimSpace(name)
	if hp == nil || name == "" {
		return false
	}
	swarmPanelMu.Lock()
	defer swarmPanelMu.Unlock()
	for _, h := range hp.state.Bees {
		if strings.TrimSpace(h.Name) == name {
			return true
		}
	}
	return false
}

// swarmReportMessageMerged 判断一条汇报消息里的每个条目是否都已由胶囊行展示。
func swarmReportMessageMerged(hp *swarmPanelState, m Message) bool {
	_, entries := parseSwarmReportCached(m.ID, m.Text)
	if len(entries) == 0 {
		return false
	}
	for _, e := range entries {
		if !swarmCapsuleHasBee(hp, e.Name) {
			return false
		}
	}
	return true
}

// renderSwarmReportMessageCapsules 渲染一条汇报消息里的所有胶囊：每个 bee 一枚胶囊，
// 解析不出条目时整条消息渲染成一枚兜底胶囊。
func renderSwarmReportMessageCapsules(c *ui.Context, s *chatState, hp *swarmPanelState, m Message) {
	// 汇报正文不再每帧解析：按消息 ID + 长度缓存。
	title, entries := parseSwarmReportCached(m.ID, m.Text)
	if len(entries) == 0 {
		label := title
		if label == "" {
			label = "子代理汇报"
		}
		renderSwarmReportCapsule(c, s, hp, m.ID, swarmCapsuleView{
			Label:       "子代理汇报详情",
			Name:        label,
			Text:        label,
			StatusColor: ThemeSecondaryText(c),
		}, func(maxH float32) {
			ui.Scroll(c.Key("swarm-report-scroll-" + m.ID)).
				FillWidth().
				MaxHeight(maxH).
				Children(func() {
					ui.Column(c.Key("swarm-report-fallback-"+m.ID)).
						Padding(Space4, Space5).
						Children(func() {
							ui.Text(c, swarmStripNoticePrefix(m.Text)).Selectable().TextColor(ThemePrimaryText(c))
						})
				})
		})
		return
	}
	merged := swarmReportMergedIntoLeadCapsule(s, hp)
	for idx, e := range entries {
		// 总管会话里已有同名胶囊在展示这条汇报时跳过，避免同一份汇报出现两枚胶囊。
		if merged && swarmCapsuleHasBee(hp, e.Name) {
			continue
		}
		key := fmt.Sprintf("%s#%d", m.ID, idx)
		entryIdx, entry := idx, e
		// 档位文本与详情卡片保持一致：条目自带档位优先，缺失时用匹配到的 bee 的档位回退。
		tier := entry.Tier
		if tier == "" {
			if bee := matchSwarmReportBee(s, hp, entry); bee != nil {
				tier = bee.Tier
			}
		}
		chipColor, chipName, chipReadOnly := resolveSwarmReportTier(c, s, tier)
		renderSwarmReportCapsule(c, s, hp, key, swarmCapsuleView{
			Label:        "子代理汇报详情",
			Name:         entry.Name,
			Text:         swarmReportPillText(entry),
			StatusColor:  swarmReportStatusColor(c, entry.Status),
			TierName:     chipName,
			TierColor:    chipColor,
			TierReadOnly: chipReadOnly,
		}, func(maxH float32) {
			renderSwarmReportEntryCard(c, s, hp, m.ID, entryIdx, entry, maxH)
		})
	}
}

// resolveSwarmReportTier 解析汇报中记录的档位文本，返回对应的视觉颜色、展示名与只读标记。
// 若匹配配置中（或默认内置）档位的 Name 或 ID，返回档位视觉强调色；否则返回次要文本色。
func resolveSwarmReportTier(c *ui.Context, s *chatState, tierStr string) (chipColor ui.Color, displayName string, isReadOnly bool) {
	trimmed := strings.TrimSpace(tierStr)
	if trimmed == "" {
		return ThemeSecondaryText(c), "", false
	}
	var tiers []config.SwarmTier
	if st := getChatSettings(s); st != nil && len(st.cfg.Orchestration.Tiers) > 0 {
		tiers = st.cfg.Orchestration.Tiers
	} else {
		tiers = config.DefaultSwarmTiers()
	}
	for _, t := range tiers {
		if strings.EqualFold(strings.TrimSpace(t.ID), trimmed) || (t.Name != "" && strings.TrimSpace(t.Name) == trimmed) {
			disp := t.Name
			if disp == "" {
				disp = t.ID
			}
			return swarmTierColor(c, t.ID), disp, t.ReadOnly
		}
	}
	return ThemeSecondaryText(c), trimmed, false
}

// matchSwarmReportBee 为一条汇报条目找出归属的 bee，用作档位/模型/文件的回退来源。
// 旧汇报没有文件快照，必须核对会话与完整任务内容，不能仅凭可复用的名字匹配；
// 有文件快照的条目直接构造一个只带文件列表的 bee。找不到归属时返回 nil。
func matchSwarmReportBee(s *chatState, hp *swarmPanelState, e swarmReportEntry) *SwarmBee {
	if e.filesRecorded {
		return &SwarmBee{ChangedFiles: e.ChangedFiles, ConflictFiles: e.ConflictFiles}
	}
	if hp == nil || s == nil {
		return nil
	}
	s.mu.Lock()
	selected := s.selectedThreadID
	s.mu.Unlock()
	swarmPanelMu.Lock()
	defer swarmPanelMu.Unlock()
	for i := range hp.state.Bees {
		h := hp.state.Bees[i]
		if hp.loadedFor == selected && h.Name == e.Name && h.Task == e.Task &&
			swarmReportStatusText(h.Status) == e.Status && strings.TrimSpace(h.Summary) == e.Summary {
			cp := hp.state.Bees[i]
			return &cp
		}
	}
	return nil
}

// renderSwarmReportEntryCard 渲染单个 bee 的汇报详情：表头固定，正文在自己的滚动区里。
// maxH 是浮窗可用的最大高度，正文用它减去表头高度后自行滚动。
func renderSwarmReportEntryCard(c *ui.Context, s *chatState, hp *swarmPanelState, msgID string, idx int, e swarmReportEntry, maxH float32) {
	cardKey := fmt.Sprintf("swarm-report-card-%s-%d", msgID, idx)

	matchedBee := matchSwarmReportBee(s, hp, e)

	tier := e.Tier
	if tier == "" && matchedBee != nil {
		tier = matchedBee.Tier
	}
	model := e.Model
	if model == "" && matchedBee != nil && matchedBee.Model != "" {
		if matchedBee.Provider != "" {
			model = matchedBee.Provider + "/" + matchedBee.Model
		} else {
			model = matchedBee.Model
		}
	}
	isDirectModel := e.DirectModel || (matchedBee != nil && matchedBee.DirectModel)
	violation := e.Violation
	if violation == "" && matchedBee != nil && matchedBee.ReadOnlyViolation {
		violation = "只读档子代理改动了文件，改动已丢弃，未合并"
	}

	statusColor := ThemeSecondaryText(c)
	switch e.Status {
	case "失败":
		statusColor = ThemeDanger(c)
	case "已停止":
		statusColor = ThemeWarning(c)
	}
	duration, toolCount, fileCount, linesAdded, linesDeleted := swarmReportCaptionParts(e, matchedBee)

	// 表头是两行信息（名字与状态 / 档位与模型）加任务标题，扣掉表头和分隔线的高度再交给正文滚动。
	bodyMaxH := maxH - 96
	if bodyMaxH < 150 {
		bodyMaxH = 150
	}

	ui.Column(c.Key(cardKey)).
		FillWidth().
		Children(func() {
			// 固定表头：头像、名字与状态、档位与模型标签、任务标题，右侧是复制按钮。
			ui.Row(c.Key(cardKey+"-header")).
				FillWidth().
				AlignItems(ui.Start).
				Gap(Space4).
				Padding(Space4, Space5).
				Background(ThemePrimaryText(c).Alpha(0.035)).
				Children(func() {
					renderSwarmReportAvatar(c, cardKey+"-avatar", e.Name, 28, 13)

					ui.Column(c.Key(cardKey + "-header-text")).
						Grow(1).
						MinWidth(0).
						Gap(Space2).
						Children(func() {
							// 第一行只放名字和状态说明：两者都不压缩，后面的说明在末尾截断。
							ui.Row(c.Key(cardKey + "-title")).
								AlignItems(ui.Center).
								Gap(Space3).
								MinWidth(0).
								ClipX().
								Children(func() {
									ui.Text(c, e.Name).
										FontSize(TypeProse).
										FontWeight(600).
										TextColor(ThemePrimaryText(c)).
										SingleLine().
										Shrink(0)

									// 名字紧跟状态说明：状态、耗时、工具数、文件数、增删行数，整体不换行。
									if e.Status != "" || duration != "" || toolCount > 0 || fileCount > 0 || linesAdded+linesDeleted > 0 {
										ui.Row(c.Key(cardKey + "-caption")).
											AlignItems(ui.Center).
											Gap(0).
											Shrink(1).
											MinWidth(0).
											ClipX().
											Children(func() {
												// 状态与耗时保持完整，排在后面的说明在空间不足时从末尾截断。
												captionText := func(text string, color ui.Color) {
													ui.Text(c, text).
														FontSize(TypeFootnote).
														TextColor(color).
														SingleLine().
														Shrink(0)
												}
												captionTail := func(text string, color ui.Color) {
													ui.Text(c, text).
														FontSize(TypeFootnote).
														TextColor(color).
														SingleLine().
														Ellipsis("…").
														Shrink(1).
														MinWidth(0)
												}
												separator := func() {
													captionText(" · ", ThemeSecondaryText(c))
												}

												if e.Status != "" {
													ui.Text(c, e.Status).
														FontSize(TypeFootnote).
														FontWeight(500).
														TextColor(statusColor).
														SingleLine().
														Shrink(0)
												}
												if duration != "" {
													if e.Status != "" {
														separator()
													}
													captionText(duration, ThemeSecondaryText(c))
												}
												if toolCount > 0 {
													separator()
													label := fmt.Sprintf("%d tools", toolCount)
													if toolCount == 1 {
														label = "1 tool"
													}
													captionTail(label, ThemeSecondaryText(c))
												}
												if fileCount > 0 {
													separator()
													label := fmt.Sprintf("%d files", fileCount)
													if fileCount == 1 {
														label = "1 file"
													}
													captionTail(label, ThemeSecondaryText(c))
												}
												if linesAdded+linesDeleted > 0 {
													separator()
													captionTail(fmt.Sprintf("+%d", linesAdded), ThemeSuccess(c))
													captionTail(" ", ThemeSecondaryText(c))
													captionTail(fmt.Sprintf("−%d", linesDeleted), ThemeDanger(c))
												}
											})
									}
								})

							// 第二行：档位、模型与标签。模型名放不下时自己截断，档位和标签保持完整宽度。
							if tier != "" || model != "" || isDirectModel || violation != "" {
								ui.Row(c.Key(cardKey + "-meta")).
									AlignItems(ui.Center).
									Gap(Space3).
									MinWidth(0).
									ClipX().
									Children(func() {
										if tier != "" {
											chipColor, chipName, isReadOnly := resolveSwarmReportTier(c, s, tier)
											ui.Row(c.Key(cardKey+"-tier-chip")).
												Padding(Space0, Space2).
												Radius(RadiusMD).
												Background(chipColor.Alpha(0.15)).
												AlignItems(ui.Center).
												Gap(Space1).
												Shrink(0).
												Children(func() {
													if isReadOnly {
														ui.Icon(c, IconLock()).
															Size(10, 10).
															TextColor(chipColor)
													}
													ui.Text(c, chipName).
														FontSize(TypeCaption).
														FontWeight(600).
														TextColor(chipColor).
														SingleLine().
														Shrink(0)
												})
										}

										if model != "" {
											ui.Text(c, model).
												FontSize(TypeCaption).
												TextColor(ThemeSecondaryText(c)).
												SingleLine().
												Ellipsis("…").
												Shrink(1).
												MinWidth(0)
										}

										if isDirectModel {
											ui.Row(c.Key(cardKey+"-direct-model-tag")).
												BorderWidth(1).
												BorderColor(ThemeAccent(c).Alpha(0.5)).
												Radius(RadiusXS).
												Padding(Space0, Space1).
												Shrink(0).
												Tooltip("用户在消息里点名了这个模型").
												Children(func() {
													ui.Text(c, "指定模型").
														FontSize(TypeCaption).
														TextColor(ThemeAccent(c)).
														SingleLine().
														Shrink(0)
												})
										}

										if violation != "" {
											ui.Row(c.Key(cardKey+"-violation-tag")).
												Padding(Space0, Space1).
												Radius(RadiusXS).
												Background(ThemeDanger(c).Alpha(0.15)).
												Shrink(0).
												Tooltip(violation).
												Children(func() {
													ui.Text(c, "越权写入").
														FontSize(TypeCaption).
														FontWeight(600).
														TextColor(ThemeDanger(c)).
														SingleLine().
														Shrink(0)
												})
										}
									})
							}

							if e.Task != "" {
								ui.Text(c, e.Task).
									FontSize(TypeBody).
									TextColor(ThemePrimaryText(c).Alpha(0.85))
							}
						})

					if strings.TrimSpace(e.Summary) != "" {
						renderSwarmReportCopyButton(c, s, cardKey, swarmReportDisplaySummary(s, e))
					}
				})

			// 表头与正文之间的分隔线。
			ui.Row(c.Key(cardKey + "-divider")).
				FillWidth().
				Height(Hairline).
				Background(ThemeBorder(c))

			ui.Scroll(c.Key(cardKey + "-scroll")).
				FillWidth().
				MaxHeight(bodyMaxH).
				Children(func() {
					ui.Column(c.Key(cardKey+"-body")).
						FillWidth().
						Padding(Space4, Space5).
						Gap(Space3).
						Children(func() {
							// 可信度（没有“不可核实 / 未读却引用”时用成功色，否则警告色）
							if e.Credibility != "" {
								credColor := ThemeSuccess(c)
								if strings.Contains(e.Credibility, "不可核实") || strings.Contains(e.Credibility, "未读却引用") {
									credColor = ThemeWarning(c)
								}
								credText := e.Credibility
								if !strings.HasPrefix(credText, "可信度") {
									credText = "可信度: " + credText
								}
								ui.Text(c, credText).
									FontSize(TypeCaption).
									TextColor(credColor)
							}

							// 合并结果
							if e.MergeResult != "" {
								ui.Text(c, "合并结果: "+e.MergeResult).
									FontSize(TypeFootnote).
									TextColor(ThemeSecondaryText(c))
							}

							// 改动文件与冲突文件
							if matchedBee != nil {
								if len(matchedBee.ChangedFiles) > 0 {
									ui.Column(c.Key(cardKey + "-changed-files")).
										Gap(Space1).
										Children(func() {
											ui.Text(c, fmt.Sprintf("改动文件 (%d):", len(matchedBee.ChangedFiles))).
												FontSize(TypeCaption).
												FontWeight(600).
												TextColor(ThemeSecondaryText(c))
											for _, f := range matchedBee.ChangedFiles {
												ui.Text(c, f).
													FontSize(TypeCaption).
													TextColor(ThemeSecondaryText(c))
											}
										})
								}

								if len(matchedBee.ConflictFiles) > 0 {
									ui.Column(c.Key(cardKey + "-conflict-files")).
										Gap(Space1).
										Children(func() {
											ui.Text(c, fmt.Sprintf("冲突文件 (%d):", len(matchedBee.ConflictFiles))).
												FontSize(TypeCaption).
												FontWeight(600).
												TextColor(ThemeDanger(c))
											for _, f := range matchedBee.ConflictFiles {
												ui.Text(c, f).
													FontSize(TypeCaption).
													TextColor(ThemeDanger(c))
											}
										})
								}
							}

							// 汇报总结全文沿用聊天区的 Markdown 样式；消息里被截断的总结从存档读全文。
							if summary := swarmReportDisplaySummary(s, e); summary != "" {
								ui.Column(c.Key(cardKey + "-summary")).
									FillWidth().
									Selectable().
									Children(func() {
										if s != nil {
											markdown.View(c, summary, &s.mdStaticOpts)
										} else {
											ui.Text(c, summary).
												Selectable().
												FontSize(TypeFootnote).
												TextColor(ThemePrimaryText(c))
										}
									})
							} else {
								ui.Text(c, "还没有汇报。").
									FontSize(TypeFootnote).
									TextColor(ThemeSecondaryText(c))
							}
						})
				})
		})
}
