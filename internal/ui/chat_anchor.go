package mcodeui

import (
	"sync"

	"github.com/egoist/mygo/ui"
)

// 发送后锚定：把刚发出的那条用户消息停在时间线视口顶部，回复在下方流入时视口不动。
// mygo 的列表只按行滚动（没有像素级滚动，见 timeline_minimap.go 的说明），所以“顶对齐”
// 在这里就是把锚点行滚到列表顶部，并在列表底部垫出一屏空白，让它停得住。
//
// 这块底部空白同时就是动画量：额外空白从 0 逐帧长到 anchorPadTarget，期间每帧保持
// FollowEnd + ScrollToEnd，mygo 的 follow-end 排版（最后一行放在“视口高 - 下内距 - 行高”）
// 就会一路把锚点行从底部顶到视口顶部，于是得到平滑滑动的观感。动画期间绝不下发
// ScrollTo(row, …)：那会走到“按整行重新对齐”的分支，一帧就跳到位。动画停稳的那一帧把
// 垫高换成 chatAnchorBottomPad，并用一次 ScrollTo(row, ui.Start) 精确收口，两件事同帧
// 发生，画面不动。
//
// 锚定只在“刚发出 → 回复到来”这段窗口里保持：期间关掉 FollowEnd，不再跟着最新一行跑。
// 用户手动滚动、点回到底部、切换会话，或者回复已经超过一屏时立刻解除，回到跟随到底。

// chatAnchorBottomPad 是锚定期间加在时间线列表底部的空白（点）。
// 列表只按行滚动：锚点行就是最后一行时，下方没有空白就滚不到顶部，列表会把整段内容
// 推到底部，所以先垫出一屏。解除锚定时这块空白一并撤掉。
const chatAnchorBottomPad float32 = 1200

// anchorPadKey 是底部垫高动画在宿主元素（时间线的 chatBody）上的通道名。
const anchorPadKey = "chat-anchor-pad"

// anchorGlideWaitFrames 是“等几何量”最多等几帧：锚点行被虚拟化到视口外时拿不到它的
// 上一帧位置，等这么多帧还拿不到就退化成改动前的即时跳转，别让视口一直悬着。
const anchorGlideWaitFrames = 3

// anchorGeometry 是 applyChatAnchor 需要的上一帧几何量（DIP，窗口坐标）：锚点行顶边、
// 列表顶边，以及列表的上内距。用位置而不是行高，是因为锚点行下方往往还有别的行
// （运行指示行、会话用量行、占位行），按“锚点行高 + 上下内距”算出来的距离在那些情况下是错的。
type anchorGeometry struct {
	rowTop, listTop, padTop float32
	reduceMotion            bool
}

// anchorPinTries 是顶对齐最多下发几次滚动。列表在布局阶段才消费滚动请求，而且一帧里
// 可能构建多次（每次构建都会走一遍 applyChatAnchor），所以这里按“调用次数”给足余量：
// 只要有一帧真的把锚点行放到顶部，下一次调用就会确认 pinned；都顶不上去时
// （内容确实不够一屏，列表会把整段内容贴底）再把控制权交还给跟随到底。
const anchorPinTries = 12

// anchorVisibleRowsFallback 是列表还没排过版时估算的可视行数（下一页判定用）。
const anchorVisibleRowsFallback = 12

// anchorPinTolerance 是“贴顶”的判定余量：列表把刚好压到上沿的那一行也算在可见范围内，
// 行距比列表上内距小的时候锚点行上面那一行会当首行报出来，这时仍算已经贴顶。
const anchorPinTolerance = 1

// chatAnchor 维护一个会话的锚定状态。
type chatAnchor struct {
	threadID  string // 所属会话
	messageID string // 锚定的用户消息 ID；解除后保留，避免同一帧内反复重建
	row       int    // 锚点行的下标，-1 表示还没算出来
	// lead 是锚点行正上方连续的占位行数（子线程胶囊行、派发占位行，没有子线程时高度为 0），
	// 贴顶判定要把它们一并当作余量：这些行看不见，列表把它们算进可见范围也不算漏出内容。
	lead    int
	holding bool // 是否正在保持锚定
	pending bool // 已确定锚点消息，但还没出现在时间线条目里
	pinned  bool // 是否已经停在锚点行顶部
	tries   int  // 已经下发过几次顶对齐滚动
	// sendWindow 记下“刚发出消息”的窗口是否还在：后端的权威摘要会先撤掉乐观运行标记，
	// 用户消息往往在其后的一帧才进列表，只看当帧的 sending/乐观标记会错过建锚时机。
	sendWindow bool

	// 底部垫高动画：垫出来的那块空白既是“让锚点行停得住”的空白，也是把锚点行
	// 从底部平滑顶到顶部的动画量。
	padAnimating bool    // 动画正在推进
	padJumped    bool    // 本段动画已经把通道起点写回 0（首帧瞬跳防护）
	padHeld      bool    // 动画已到位，垫高按 chatAnchorBottomPad 常驻到解除锚定
	padTarget    float32 // 本段动画的垫高目标（点）
	padItems     int     // 本段动画开始时的条目数，变了就说明列表结构动了，要提前收口
}

// release 解除锚定；保留 messageID，这样同一条消息不会在下一帧又被锚定一次。
func (a *chatAnchor) release() {
	a.row = -1
	a.lead = 0
	a.holding = false
	a.pending = false
	a.pinned = false
	a.tries = 0
	a.sendWindow = false
	a.resetPad()
}

// resetPad 清掉底部垫高动画的状态，让下一段锚定从零重新滑起。
// 留着 padHeld 会让下一段锚定一上来就“已到位”，动画不会启动。
func (a *chatAnchor) resetPad() {
	a.padAnimating = false
	a.padJumped = false
	a.padHeld = false
	a.padTarget = 0
	a.padItems = 0
}

var (
	chatAnchorMu     sync.Mutex
	chatAnchorStates = make(map[*chatState]*chatAnchor)
)

// getChatAnchorState 获取或初始化会话的锚定状态，调用方需持有 chatAnchorMu。
func getChatAnchorState(s *chatState) *chatAnchor {
	a, ok := chatAnchorStates[s]
	if !ok {
		a = &chatAnchor{}
		chatAnchorStates[s] = a
	}
	return a
}

// anchorArmed 判断这一帧是否处在“刚发出消息”的窗口里：
// 请求还在发送中，或者乐观运行标记还在（消息常常先于 sending 复位到达）。
func anchorArmed(sending, optimisticRunning bool) bool {
	return sending || optimisticRunning
}

// armChatAnchor 在真正发出消息之前直接记下“刚发出”的窗口，调用方无需持有 chatAnchorMu。
// 只靠渲染帧探测 sending/乐观运行标记并不可靠：发送请求可能在渲染出一帧之前就返回，
// 后端的权威摘要又会撤掉乐观运行标记，这个窗口就再也建不起来了（锁顺序与
// syncChatAnchorLocked 一致，允许在持有 s.mu 时调用）。
func (s *chatState) armChatAnchor(selectedThreadID string) {
	if s == nil || selectedThreadID == "" {
		return
	}
	chatAnchorMu.Lock()
	defer chatAnchorMu.Unlock()
	a := getChatAnchorState(s)
	if a.threadID != selectedThreadID {
		// 切了会话：上一个会话的锚定直接作废，只留下这次的发送窗口。
		*a = chatAnchor{threadID: selectedThreadID}
	}
	a.sendWindow = true
}

// latestUserMessageID 取消息列表里最后一条用户消息的 ID，没有时返回空串。
func latestUserMessageID(msgs []Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && msgs[i].ID != "" {
			return msgs[i].ID
		}
	}
	return ""
}

// anchorRowIndex 找出这条消息对应的时间线条目下标，找不到返回 -1。
func anchorRowIndex(items []timelineItem, messageID string) int {
	if messageID == "" {
		return -1
	}
	for i, item := range items {
		if item.id == messageID || item.msg.ID == messageID {
			return i
		}
	}
	return -1
}

// anchorPlaceholderItem 判断这个时间线条目是不是高度为 0 的占位行：子线程胶囊行
// （没有子线程时不渲染）和派发占位行（派单块写完、子代理还没出现时也不渲染）。
func anchorPlaceholderItem(item timelineItem) bool {
	return item.kind == timelineItemSwarmCapsules || item.kind == timelineItemSwarmDispatch
}

// anchorLeadPlaceholders 从 row-1 往上数连续的占位行，遇到第一个非占位行就停。
func anchorLeadPlaceholders(items []timelineItem, row int) int {
	lead := 0
	for i := row - 1; i >= 0 && i < len(items); i-- {
		if !anchorPlaceholderItem(items[i]) {
			break
		}
		lead++
	}
	return lead
}

// anchorGeometryReady 判断上一帧的几何量是否齐备：锚点行和列表的顶边都要真的排过版。
// 窗口坐标里的顶边不可能小于等于 0（时间线列表上方永远有会话头部），
// 所以拿不到时 chat.go 留下的 0 就是“缺失”。
func anchorGeometryReady(geom anchorGeometry) bool {
	return geom.rowTop > 0 && geom.listTop > 0
}

// anchorPadTarget 返回锚定期间要额外垫在列表底部的空白。跟随到底时最后一行顶边落在
// 列表高 - (上内距 + extra) - 行高，要它落到上内距，就要 extra 消除“锚点行顶边比列表顶边
// 低出的那一段”；取位置差而不是行高，锚点行下方有其它行时距离也正确。
// 结果不大于 0（锚点行已在顶上，或几何量缺失）时返回 0，超过一个屏幕垫高时封顶。
func anchorPadTarget(geom anchorGeometry) float32 {
	if !anchorGeometryReady(geom) {
		return 0
	}
	target := geom.rowTop - geom.listTop - geom.padTop
	if target <= 0 {
		return 0
	}
	if target > chatAnchorBottomPad {
		target = chatAnchorBottomPad
	}
	return target
}

// anchorGlideAction 是“还没贴顶”那一帧该怎么走。
type anchorGlideAction int

const (
	anchorGlideWait    anchorGlideAction = iota // 几何量还没到，等下一帧
	anchorGlideStart                            // 这一帧启动垫高动画
	anchorGlideHold                             // 动画推进中：保持 follow-end，不下发 ScrollTo
	anchorGlideSettle                           // 已到位：切回贴顶并精确收口
	anchorGlideInstant                          // 退化：沿用改动前的即时 ScrollTo(row, ui.Start)
)

// anchorGlideActionFor 只看锚定状态与几何量，决定“还没贴顶”这一帧怎么走。
// sameList 表示列表条目数没变且仍贴着末尾（用户在动画中滚走、或回复行已经进列表时为假）：
// 这两种情况垫高已经推不动锚点行，要立刻收口。
func anchorGlideActionFor(a *chatAnchor, geom anchorGeometry, sameList bool) anchorGlideAction {
	switch {
	case geom.reduceMotion:
		// 减少动态效果：不启动动画，行为与改动前的即时跳转一致。
		return anchorGlideInstant
	case a.padHeld:
		// 动画已经到位：每帧只做一次精确收口，直到列表确认贴顶。
		return anchorGlideSettle
	case a.padAnimating:
		if sameList {
			return anchorGlideHold
		}
		return anchorGlideSettle
	case !anchorGeometryReady(geom):
		if a.tries >= anchorGlideWaitFrames {
			return anchorGlideInstant
		}
		return anchorGlideWait
	case anchorPadTarget(geom) <= 0:
		// 锚点行已经在视口顶部附近，或者内容不足一屏顶不上去：照旧直接跳到位。
		return anchorGlideInstant
	default:
		return anchorGlideStart
	}
}

// anchorShouldRelease 判断锚点是否已经离末尾太远：下方内容超过一屏后停在这里就看不到新东西了，
// 这时应当解除锚定并回到底部跟随。
func anchorShouldRelease(anchorRow, lastItem, visibleRows int) bool {
	if anchorRow < 0 || lastItem < 0 {
		return false
	}
	if visibleRows <= 0 {
		visibleRows = anchorVisibleRowsFallback
	}
	return lastItem-anchorRow > visibleRows
}

// pinnedAtTop 判断锚点行是否已经停在视口顶部（允许上面漏出 anchorPinTolerance 行），
// lastVisible 要盖住锚点行。锚点行上方那串占位行高度为 0，允许它们一起当余量：
// 以 row-a.lead 为基准，可见首行落在它附近就算已经贴顶。
func (a *chatAnchor) pinnedAtTop(firstVisible, lastVisible, row int) bool {
	base := row - a.lead
	return firstVisible >= base-anchorPinTolerance && firstVisible <= row && lastVisible >= row
}

// scrolledByUser 判断用户是否自己滚走了：停稳之后可见首行离开了锚点行
// （锚点行上方的占位行同样算作余量）。
func (a *chatAnchor) scrolledByUser(firstVisible int) bool {
	base := a.row - a.lead
	return firstVisible < base-anchorPinTolerance || firstVisible > a.row+anchorPinTolerance
}

// syncChatAnchorLocked 在渲染前更新锚定状态，返回这一帧是否保持锚定（保持时列表底部要垫空白）。
// 调用方必须持有 s.mu：这里读 sending、乐观运行标记与消息列表。enabled 为假（设置里关掉
// 「发送后置顶消息」）时解除锚定并把 messageID 记成当前最新用户消息，之后重新打开开关
// 不会把旧消息补锚定一次。
func (s *chatState) syncChatAnchorLocked(selectedThreadID string, enabled bool) bool {
	if s == nil {
		return false
	}
	chatAnchorMu.Lock()
	defer chatAnchorMu.Unlock()
	a := getChatAnchorState(s)
	if a.threadID != selectedThreadID {
		// 切了会话：上一个会话的锚定直接作废。
		*a = chatAnchor{threadID: selectedThreadID}
	}
	if !enabled {
		// 关掉开关：撤掉锚定并吞掉发送窗口，把 messageID 推进到最新一条用户消息，
		// 这样重新打开开关也不会把已经发出去的旧消息补锚定到顶部。
		a.release()
		a.messageID = latestUserMessageID(s.messages)
		return false
	}
	armed := anchorArmed(s.sending, s.optimisticRunning[selectedThreadID])
	if armed {
		// 发送窗口一旦出现就记住：onWatchUpdate 收到后端摘要时会撤掉乐观运行标记，
		// sending 也在发送请求返回后立刻复位，回推的用户消息通常晚于这两者到列表。
		a.sendWindow = true
	}
	latest := latestUserMessageID(s.messages)
	// 只锁“刚发出去、还没被回复顶下去”的那一条：发送请求还在路上时列表末尾可能仍是
	// 上一条回复，这时锚定最新一条用户消息会把视口往上拽，得等乐观消息落到末尾再说。
	lastID := ""
	if n := len(s.messages); n > 0 {
		lastID = s.messages[n-1].ID
	}
	if a.sendWindow && latest != "" && latest == lastID && latest != a.messageID {
		// 新的用户消息：重新建锚点，顶对齐在 applyChatAnchor 里下发。
		a.messageID = latest
		a.row = -1
		a.holding = true
		a.pending = true
		a.pinned = false
		a.tries = 0
		a.sendWindow = false
		// 上一段锚定可能还留着垫高：清掉之后这一帧列表底部没有空白，
		// 新消息先停在底部，下一帧再从底部平滑滑上去。
		a.resetPad()
	}
	if a.holding && a.pending && latest == a.messageID {
		// 消息已经落到本地列表里，可以锚定了。
		a.pending = false
	}
	if a.holding && !a.pending && latest != a.messageID {
		// 锚点已经不是最新一条用户消息（消息被重新加载、或者用户又发了一条还没到）：
		// 保持就没有意义了，交还给跟随到底。
		a.release()
	}
	return a.holding
}

// chatAnchorRowID 返回当前正在锚定的消息 ID，供列表构建时定位锚点行、抽它的上一帧位置。
// 没在锚定（或锚点消息还没落到列表里）时返回空串。
func (s *chatState) chatAnchorRowID() string {
	if s == nil {
		return ""
	}
	chatAnchorMu.Lock()
	defer chatAnchorMu.Unlock()
	a := getChatAnchorState(s)
	if !a.holding || a.pending {
		return ""
	}
	return a.messageID
}

// chatAnchorPadExtra 返回这一帧要额外垫在时间线列表底部的空白，并推进垫高动画。
// 必须在构建列表之前调用（列表的 Padding 只能用当前帧就绪的值），host 用时间线的 chatBody。
// 通道第一次建立时会直接停在目标值上，所以首帧先用 Jump 把起点写回 0，动画才能从底部滑起。
func (s *chatState) chatAnchorPadExtra(c *ui.Context, host ui.Element) float32 {
	if s == nil {
		return 0
	}
	chatAnchorMu.Lock()
	defer chatAnchorMu.Unlock()
	a := getChatAnchorState(s)
	if a.padHeld {
		// 动画已到位：垫高常驻到解除锚定，否则列表底部没有空白，锚点行就停不住。
		return chatAnchorBottomPad
	}
	if !a.padAnimating {
		return 0
	}
	if !a.padJumped {
		host.Jump(anchorPadKey, 0)
		a.padJumped = true
	}
	var v float32
	if reduceMotion(c) {
		// 减少动效：垫高直接到位，不走弹簧。
		v = host.Jump(anchorPadKey, a.padTarget)
	} else {
		v = host.AnimateTo(anchorPadKey, a.padTarget, ui.Smooth)
	}
	// Settled 对未知 key 返回 true，兼做宿主元素没有状态时的兜底。
	if host.Arrived(anchorPadKey) || host.Settled(anchorPadKey) {
		a.padAnimating = false
		a.padHeld = true
		return chatAnchorBottomPad
	}
	return v
}

// applyChatAnchor 在时间线列表构建之后处理锚定：未顶对齐时先启动/推进底部垫高动画，
// 让 follow-end 排版把锚点行平滑顶到视口顶部，动画停稳后再用一次 ScrollTo(row, ui.Start)
// 精确收口；顶上去之后只观察是否需要解除，不再干预用户的滚动。
func (s *chatState) applyChatAnchor(selectedThreadID string, items []timelineItem, geom anchorGeometry) {
	if s == nil || selectedThreadID == "" {
		return
	}
	chatAnchorMu.Lock()
	defer chatAnchorMu.Unlock()
	a := getChatAnchorState(s)
	if a.threadID != selectedThreadID || !a.holding || a.pending || a.messageID == "" {
		return
	}
	row := anchorRowIndex(items, a.messageID)
	if row < 0 {
		return
	}
	first, last := s.chatListState.Visible()
	visibleRows := anchorVisibleRowsFallback
	if last >= first {
		visibleRows = last - first + 1
	}
	if anchorShouldRelease(row, len(items)-1, visibleRows) {
		// 回复已经超过一屏：停在这里看不到新内容，直接回到底部跟随。
		a.release()
		s.chatListState.FollowEnd = true
		s.chatListState.ScrollToEnd()
		return
	}
	// 锚点行上方可能有连续的空占位行（子线程胶囊行、派发占位行）：它们高度为 0，
	// 却会把可见首行顶到更上面，先数出来交给贴顶判定当余量。
	a.lead = anchorLeadPlaceholders(items, row)
	switch {
	case a.pinnedAtTop(first, last, row):
		// 已经停在锚点行顶部：记下行号，之后只等用户滚动或回复满屏。
		a.pinned = true
		a.row = row
		a.tries = 0
	case a.pinned && a.scrolledByUser(first):
		// 停稳过又离开锚点行：用户自己滚走了，解除锚定但不再强行回到底部
		// （这里不能 ScrollToEnd，否则会把用户刚滚到的位置又拽回末尾）。
		a.release()
		s.chatListState.FollowEnd = true
	case a.tries >= anchorPinTries:
		// 试了这么多次还顶不到（内容不足一屏时列表会把整段内容贴底）：交还给跟随到底。
		a.release()
		s.chatListState.FollowEnd = true
		s.chatListState.ScrollToEnd()
	default:
		// 还没顶到：几何量齐了就启动/推进底部垫高动画，让 follow-end 排版把锚点行
		// 一路推到视口顶部；动画期间绝不下发 ScrollTo(row, …)，否则会瞬跳到位。
		sameList := len(items) == a.padItems && s.chatListState.AtEnd()
		switch anchorGlideActionFor(a, geom, sameList) {
		case anchorGlideWait:
			// 上一帧没排出锚点行的位置（虚拟化到视口外）：只记账，保持现有滚动位置。
			a.row = row
			a.tries++
		case anchorGlideStart:
			a.row = row
			a.padTarget = anchorPadTarget(geom)
			a.padAnimating = true
			a.padJumped = false
			a.padHeld = false
			a.padItems = len(items)
			// 动画靠“最后一行”就是锚点行：动画期间必须每帧跟随到底，否则垫高推不动它。
			s.chatListState.FollowEnd = true
			s.chatListState.ScrollToEnd()
		case anchorGlideHold:
			a.row = row
			s.chatListState.FollowEnd = true
			s.chatListState.ScrollToEnd()
		case anchorGlideSettle:
			// 垫高已到位，或者动画被打断（用户滚走、回复行已进列表）：先让垫高停在
			// chatAnchorBottomPad 常驻，再把锚点行精确对到视口顶部。同帧下发 req.set，
			// 所以换垫高不会带来位移；也不能清 padHeld，撤掉的话下一帧底部没有空白，
			// 锚点行就停不住了。
			a.row = row
			a.tries++
			a.padAnimating = false
			a.padHeld = true
			s.chatListState.FollowEnd = false
			s.chatListState.ScrollTo(row, ui.Start)
		default: // anchorGlideInstant
			// 退化路径：与改动前的即时跳转一致，垫高直接给足（本帧已算过，下一帧生效），
			// 之后由 padHeld 每帧走一次收口，直到列表确认贴顶。
			a.row = row
			a.tries++
			a.padAnimating = false
			a.padHeld = true
			s.chatListState.FollowEnd = false
			s.chatListState.ScrollTo(row, ui.Start)
		}
	}
}

// chatAnchorHolding 判断这个会话当前是否处于锚定状态。
func (s *chatState) chatAnchorHolding(selectedThreadID string) bool {
	if s == nil {
		return false
	}
	chatAnchorMu.Lock()
	defer chatAnchorMu.Unlock()
	a := getChatAnchorState(s)
	return a.threadID == selectedThreadID && a.holding
}

// chatSelectedThreadID 读当前选中的会话 ID。时间线渲染期调用：这里短暂持锁，
// 避免和发送协程并发读写同一个字段。
func (s *chatState) chatSelectedThreadID() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selectedThreadID
}

// jumpChatToLatest 回到时间线最新处：滚到底的同时清空未读基线，不必等下一帧 AtEnd 变为 true。
// 「跳到最新」按钮与同名快捷键共用这一条路径。
func (s *chatState) jumpChatToLatest(selectedThreadID string) {
	if s == nil || selectedThreadID == "" {
		return
	}
	s.clearTimelineUnread(selectedThreadID)
	s.releaseChatAnchor(selectedThreadID, true)
}

// releaseChatAnchor 解除锚定并恢复跟随；toEnd 为真时顺手滚到底（点「跳到最新」走这条）。
func (s *chatState) releaseChatAnchor(selectedThreadID string, toEnd bool) {
	if s == nil {
		return
	}
	chatAnchorMu.Lock()
	a := getChatAnchorState(s)
	if a.threadID == selectedThreadID {
		a.release()
	}
	chatAnchorMu.Unlock()
	s.chatListState.FollowEnd = true
	if toEnd {
		s.chatListState.ScrollToEnd()
	}
}
