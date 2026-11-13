package mcodeui

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"
	"github.com/egoist/mygo/ui/richtext"
	"mcode/internal/config"
	"mcode/internal/startuptrace"
)

// 行内小图标的边长：设计令牌最小的 IconSM 为 14，密集行里仍偏大，
// 这里集中定义更小的两档，避免各业务文件散落字面量。
const (
	inlineIconSize float32 = 11
	cardGlyphSize  float32 = 9
	microIconSize  float32 = 8
)

var initialHistoryOnce sync.Once

// UpsertMessages 根据 updates 更新现有消息列表，处理 removedIDs 删除，并按 TurnIndex 稳定排序。
// 当所有更新均为已有 ID 的原地替换或按顺序追加到末尾时走快速路径，不做排序、不整表重新分配；
// 出现乱序或需要删除时回退到稳定排序慢路径。
func UpsertMessages(existing []Message, updates []Message, removedIDs []string) []Message {
	if len(removedIDs) > 0 {
		return upsertMessagesFallback(existing, updates, removedIDs)
	}
	if len(updates) == 0 {
		return existing
	}

	// 快速路径：单条更新高频优化（流式打字与单消息追加占多数）
	if len(updates) == 1 {
		up := updates[0]
		if len(existing) == 0 {
			return []Message{up}
		}
		lastIdx := len(existing) - 1
		// 1. 最常见：末尾消息的原地内容/状态替换
		if existing[lastIdx].ID == up.ID {
			if lastIdx == 0 || up.TurnIndex >= existing[lastIdx-1].TurnIndex {
				existing[lastIdx] = up
				return existing
			}
		}
		// 2. 顺序追加新消息到末尾
		if up.TurnIndex >= existing[lastIdx].TurnIndex {
			existingIdx := -1
			for i := 0; i < lastIdx; i++ {
				if existing[i].ID == up.ID {
					existingIdx = i
					break
				}
			}
			if existingIdx == -1 {
				return append(existing, up)
			}
			// 已有非末尾 ID 的原地替换，检查局部顺序
			if (existingIdx == 0 || up.TurnIndex >= existing[existingIdx-1].TurnIndex) &&
				up.TurnIndex <= existing[existingIdx+1].TurnIndex {
				existing[existingIdx] = up
				return existing
			}
		}
	}

	// 检查多条 updates 是否全部满足原地替换或末尾顺序追加
	if canFastUpsert(existing, updates) {
		return fastUpsertMessages(existing, updates)
	}

	return upsertMessagesFallback(existing, updates, removedIDs)
}

// canFastUpsert 检查 updates 是否全部为保持顺序的原地替换或末尾顺序追加。
func canFastUpsert(existing []Message, updates []Message) bool {
	if len(existing) == 0 {
		for i := 1; i < len(updates); i++ {
			if updates[i].TurnIndex < updates[i-1].TurnIndex {
				return false
			}
		}
		return true
	}

	idToIndex := make(map[string]int, len(existing))
	for i, m := range existing {
		idToIndex[m.ID] = i
	}

	lastAppendTurn := existing[len(existing)-1].TurnIndex
	for _, up := range updates {
		if idx, exists := idToIndex[up.ID]; exists {
			// 已有 ID 原地替换：验证前后局部有序
			if idx > 0 && up.TurnIndex < existing[idx-1].TurnIndex {
				return false
			}
			if idx < len(existing)-1 && up.TurnIndex > existing[idx+1].TurnIndex {
				return false
			}
		} else {
			// 新 ID：必须在末尾按顺序追加
			if up.TurnIndex < lastAppendTurn {
				return false
			}
			lastAppendTurn = up.TurnIndex
		}
	}
	return true
}

// fastUpsertMessages 执行就地替换与顺序追加，不做排序、不整表重新分配。
func fastUpsertMessages(existing []Message, updates []Message) []Message {
	if len(existing) == 0 {
		res := make([]Message, len(updates))
		copy(res, updates)
		return res
	}

	idToIndex := make(map[string]int, len(existing))
	for i, m := range existing {
		idToIndex[m.ID] = i
	}

	for _, up := range updates {
		if idx, exists := idToIndex[up.ID]; exists {
			existing[idx] = up
		} else {
			idToIndex[up.ID] = len(existing)
			existing = append(existing, up)
		}
	}
	return existing
}

// upsertMessagesFallback 针对删除或乱序更新走全量重建与稳定排序的慢路径。
func upsertMessagesFallback(existing []Message, updates []Message, removedIDs []string) []Message {
	removeSet := make(map[string]struct{}, len(removedIDs))
	for _, id := range removedIDs {
		removeSet[id] = struct{}{}
	}

	result := make([]Message, 0, len(existing)+len(updates))
	idToIndex := make(map[string]int, len(existing)+len(updates))

	// 1. 保留未被删除的现有消息
	for _, m := range existing {
		if _, removed := removeSet[m.ID]; !removed {
			idToIndex[m.ID] = len(result)
			result = append(result, m)
		}
	}

	// 2. 处理 updates：相同 ID 替换，新 ID 追加
	for _, up := range updates {
		if _, removed := removeSet[up.ID]; removed {
			continue
		}
		if idx, exists := idToIndex[up.ID]; exists {
			result[idx] = up
		} else {
			idToIndex[up.ID] = len(result)
			result = append(result, up)
		}
	}

	// 3. 按 TurnIndex 升序做稳定排序
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].TurnIndex < result[j].TurnIndex
	})

	return result
}

// threadActivityTime 返回会话排序用的“有效活跃时间”：用户最后发消息时间为零值时回退到
// UpdatedAt。助手回复、改名、置顶等只改 UpdatedAt 的操作因此不会把会话顶到列表前面。
func threadActivityTime(t Thread) time.Time {
	if !t.LastUserMessageAt.IsZero() {
		return t.LastUserMessageAt
	}
	return t.UpdatedAt
}

// SortThreads 返回按置顶优先、有效活跃时间（threadActivityTime）倒序排列的会话列表副本。
func SortThreads(threads []Thread) []Thread {
	result := make([]Thread, len(threads))
	copy(result, threads)
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Pinned != result[j].Pinned {
			return result[i].Pinned && !result[j].Pinned
		}
		return threadActivityTime(result[i]).After(threadActivityTime(result[j]))
	})
	return result
}

// SortProjectsByActivity 返回按项目活跃时间（该项目内所有会话 threadActivityTime 的最大值）
// 倒序排列的项目列表副本。没有任何会话的项目排在最后，并保持传入顺序（稳定排序）。
// threadsByProj 为 nil 时原样复制，不会 panic。
func SortProjectsByActivity(projects []Project, threadsByProj map[string][]Thread) []Project {
	result := make([]Project, len(projects))
	copy(result, projects)
	activity := make(map[string]time.Time, len(projects))
	for _, p := range projects {
		var latest time.Time
		for _, th := range threadsByProj[p.ID] {
			if at := threadActivityTime(th); at.After(latest) {
				latest = at
			}
		}
		activity[p.ID] = latest
	}
	sort.SliceStable(result, func(i, j int) bool {
		ai, aj := activity[result[i].ID], activity[result[j].ID]
		if ai.IsZero() != aj.IsZero() {
			// 有活跃时间的项目始终排在空项目前面。
			return aj.IsZero()
		}
		return ai.After(aj)
	})
	return result
}

// pendingMenuAction 是菜单派发、等待在根视图下一帧执行的动作。
// 菜单点击发生在事件回调里，不能直接改界面状态，因此入队后由根视图按快捷键同一路径执行。
type pendingMenuAction struct {
	id    string
	index int
}

// chatState 维护整个 UI 层的本地状态。
// 根据 MyGo 规范，*ui.Context 和 ui.Element 不能存入结构体字段中。
type chatState struct {
	mu           sync.Mutex
	windowActive atomic.Bool
	inspector    inspectorState

	client Client
	// services 由 servicesMu 单独保护，不要直接读写，统一走 uiServices / setUIServices。
	// 单独一把锁是因为 invalidate 会从后台 goroutine 调用，而调用方可能已经持有 s.mu
	// （如 selectThread 起的 goroutine），不能在 invalidate 里再去取 s.mu。
	// servicesMu 允许在持有 s.mu 时获取；反过来持 servicesMu 时不得再取其他锁或调用外部代码。
	servicesMu sync.RWMutex
	services   ui.Services

	projects              []Project
	projectPathsChecked   map[string]string
	projectPathsMissing   map[string]bool
	projectPathCheckSeq   uint64
	projectPathsWasActive bool
	projectPathFocusHook  bool
	threadsByProj         map[string][]Thread // 每个项目的会话列表
	collapsedProj         map[string]bool     // 组折叠状态（保存在内存中，true 为折叠）
	threads               []Thread            // 全部会话的平铺视图，按时间倒序
	selectedThreadID      string
	stewardTaskThreadID   string
	stewardTask           stewardTaskSnapshot
	stewardTaskWatchStop  func()
	stewardTaskLoadSeq    uint64
	// beeBusyFrame 非 nil 时表示 renderSidebar 本帧已算好的「名下还有子代理在跑的主会话 ID」集合，
	// 侧栏每帧对每个会话多次判定忙碌，逐次全表扫描在几千个会话时是 O(N²)。只在 UI 线程读写。
	beeBusyFrame map[string]bool
	// forwardInflight 记录正在转发中的源会话 ID，由 s.mu 保护。
	// 按住快捷键或连点菜单时用来挡住第二次转发，避免同一会话建出多条新会话。
	forwardInflight             map[string]bool
	messages                    []Message
	messagesRev                 uint64 // 每次改写 s.messages 时递增，受 s.mu 保护
	timelineCacheRev            uint64
	timelineCacheMsgs           []Message
	timelineCacheItems          []timelineItem
	timelineCacheExpandedRev    uint64               // 展开状态缓存版本号
	timelineSnapshotThreadID    string               // 上次时间线构建所属会话 ID
	timelineSnapshotRunningTurn int                  // 上次构建的运行回合，空闲时为 -1
	timelineSnapshotMsgs        []messageFingerprint // 上次时间线消息变更指纹快照
	timelineSnapshotExpanded    map[string]bool      // 上次时间线构建时的展开状态快照
	timelineSnapshotGroupsOpen  bool                 // 上次时间线构建时工具组的默认展开值
	expandedRev                 uint64               // 展开状态变更序号（由 s.mu 保护）
	approvals                   []Approval
	// approvalsEvents 是 Watch 推送审批列表的次数，用来判断订阅后的补拉结果是否已被推送取代。
	approvalsEvents uint64
	threadUsage     ThreadUsage

	// maxTurnRev/maxTurnValue 缓存“消息里的最大回合号”：swarm 回合完成块的推迟判定每帧都会问，
	// 按 messagesRev 缓存，避免每帧重新扫一遍全部消息；maxTurnValid 区分“还没算过”。
	maxTurnRev   uint64
	maxTurnValue int
	maxTurnValid bool

	// swarmReportedRev/swarmReportedByName 缓存“已汇报的子代理名字 -> 汇报消息时间”，按 messagesRev 失效：
	// 时间线胶囊行每帧都要判定哪些子代理的汇报已经出现，不能逐帧重扫全部消息并解析汇报正文。
	swarmReportedRev    uint64
	swarmReportedByName map[string][]time.Time
	swarmReportedValid  bool

	// lastAsstRev/lastAsstByTurn 缓存“回合号 -> 该回合最后一条助手消息 ID”，按 messagesRev 失效：
	// 时间线每帧都要判定每个条目是否收尾了本回合，浮窗里的长会话逐帧重建整张表是纯浪费。
	lastAsstRev    uint64
	lastAsstByTurn map[int]string
	lastAsstValid  bool

	// 主时间线拼装缓存：把“会话条目 + 已裁决计划 + 派发占位行 + 每回合胶囊行”的拼装结果
	// 按会话 ID / 时间线条目快照版本（timelineCacheRev）/ 展开状态版本 / 计划指纹缓存，
	// 静止帧直接复用，不再每帧复制整表。
	// timelineAssembledRev 记的是拼装所用 timelineCacheItems 的版本（timelineCacheRev），不是 messagesRev：
	// 两者抢跑时 base 可能比 messagesRev 旧，按 messagesRev 记账会让旧结果一直命中。
	timelineAssembledSel    string
	timelineAssembledRev    uint64
	timelineAssembledExpand uint64
	timelineAssembledPlans  string
	timelineAssembledItems  []timelineItem
	timelineAssembledValid  bool

	// swarmMentionView/swarmMentionCache 缓存“消息正文里提到了哪些子代理”：
	// 条目按 Swarm 派生视图区分，视图一换就整表作废（子代理名单只在状态变更时改动）。
	swarmMentionView  *swarmStateView
	swarmMentionCache map[string][]SwarmMentionMatch
	// swarmFenceCache 缓存“消息正文去掉 swarm 派单代码块后的结果”，键为消息 ID；
	// 正文没变（绝大多数帧）时直接命中，不再逐帧切行、正则替换与拼接。
	swarmFenceCache map[string]swarmFenceEntry
	// composerHistoryRev/composerHistoryEntries 缓存输入框方向键历史（按 messagesRev）：
	// 主会话与每个浮窗面板各有自己的 composer，每帧都要重扫一遍全部消息。
	composerHistoryRev     uint64
	composerHistoryEntries []string
	composerHistoryValid   bool

	// threadsRuntimeRev 在会话运行态/待处理请求可能变化时递增（Watch 摘要、会话列表重载）；
	// pendingReqIDs 是按它缓存的“有待处理请求”会话 ID 集合，pendingReqIDsRev 是建立时的版本号。
	threadsRuntimeRev uint64
	pendingReqIDs     map[string]bool
	pendingReqIDsRev  uint64

	pickFolder func() (string, bool) // 自定义文件夹选择对话框逻辑

	window               func() *mygo.Window
	openThreadWindow     func(threadID string)
	openTasksWindowFn    func(view string)
	revealThreadInMainFn func(threadID string)
	tasksWindowView      string
	initialThreadID      string

	initialized          bool
	closed               bool
	watchSeq             uint64
	respondingApproval   int                           // 在途审批响应数，归零才解锁批量/单卡按钮
	questionDrafts       map[string]*questionDraft     // 提问卡片草稿，以 RequestID 为键
	composerFocused      bool                          // composer 输入框是否聚焦，由 composer 渲染时写入，供 ⌘Return 判定
	composerFocusRequest bool                          // 下一帧主动给 composer 输入框要一次焦点（待创建会话页用）
	questionOtherFocused bool                          // 提问卡片“其他”文本框是否聚焦
	resolvedPlans        map[string][]resolvedPlan     // 按 threadID 记录已裁决的计划历史态
	answeredQuestions    map[string][]answeredQuestion // 按 threadID 记录已回答的提问历史态
	planAnchorSeen       map[string]bool               // 同一请求只定位一次
	collapsedPlans       map[string]bool               // 计划历史态卡片正文折叠状态（key 为 resolvedPlan.ID）
	stopping             bool
	stoppingSince        time.Time // 进入停止态的时刻，用于兜底超时判定
	watchStop            func()

	// 队列与中断状态
	queue        map[string][]QueuedFollowUp // 按会话 ID 存排队消息
	queueChanges map[string]uint64           // 防止初次加载覆盖监听期间的新快照
	interrupted  map[string]bool             // 成功中断后可继续标记（按会话 ID 存）
	// continueDismissed 记录「继续」胶囊被忽略的异常结束（会话 ID -> 忽略时最后一条消息 ID）：
	// 最后一条消息 ID 变化后该记录自动失效，胶囊重新出现；随 ui-state.json 持久化。
	continueDismissed map[string]string
	stopQueueWatch    func()
	stopBeesWatch     func()
	// stopSummaryWatch 注销全局会话运行态监听（见 startThreadSummaryWatch）。
	stopSummaryWatch func()
	// summaryRefreshSeen 记录已因推送摘要触发过列表刷新的键（见 threadListStaleLocked）：
	// 流式期间同一会话会连推很多条摘要，靠它只重拉一次列表。
	summaryRefreshSeen map[string]bool

	// 乐观运行中标记（按会话 ID 存）：发送成功后立即置位，让界面在当帧就显示“回复中”；
	// 后端 Watch 增量给出权威摘要或发送失败时清除。
	optimisticRunning map[string]bool
	// composerDockPromo 是「发送首条消息」的透视标记，让输入框在消息推回前就切到停靠布局，
	// 见 composer_dock.go。自带锁，锁序为 s.mu → composerDockPromo.mu。
	composerDockPromo composerDockPromoState

	// turnFinished 标记「这一轮已跑完、用户还没打开看」的会话（按会话 ID 存角标种类）：
	// 侧栏据此在会话图标上亮小圆点并把标题同色，正常的完成用强调色，只有报错的回合用红色，
	// 打开该会话即清除。
	turnFinished map[string]string

	// 运行中排队配置缓存（由 s.mu 保护）
	queueWhileRunningCache    bool
	queueWhileRunningCachedAt time.Time

	// 运行中的消息插入当前回合配置缓存（由 s.mu 保护）
	insertIntoRunningTurnCache    bool
	insertIntoRunningTurnCachedAt time.Time
	quotaProvider                 string
	quotaLoadedAt                 time.Time
	quotaLoading                  bool
	quotaWindows                  []ProviderQuotaWindow
	quotaErr                      string
	// 按 provider 的额度缓存：窗口、错误、加载中与上次加载时间（由 s.mu 保护）。
	quotaWindowsByProvider map[string][]ProviderQuotaWindow
	quotaErrs              map[string]string
	quotaLoadingProviders  map[string]bool
	quotaLoadedProviders   map[string]time.Time
	// quotaRefreshingProviders 按 provider 记录手动刷新是否进行中；quotaRefreshErrs 保存
	// 最近一次手动刷新的错误，避免随后的额度读取把它吞掉（由 s.mu 保护）。
	quotaRefreshingProviders map[string]bool
	quotaRefreshErrs         map[string]string

	// 自动休眠扫描节流时间（由 s.mu 保护）
	autoDormantScannedAt time.Time

	draft         string
	draftText     richtext.Text
	isComposing   bool
	composerView  composerViewState // composer 视图态，仅 UI 线程读写，类型定义在 composer.go
	composerAreaH float32           // 主 composer 区域上一帧的高度（含标签与卡片），供浮动面板在窄屏停到它上方时避开；受 s.mu 保护
	// chatPaneWidthBits 是聊天区最近一帧的可用宽度（float32 位模式，见 chatPaneWidth / setChatPaneWidth）：
	// 按比例档的内容宽度每帧按它换算，时间线逐行渲染时用原子量读取，不抢 s.mu。
	chatPaneWidthBits       atomic.Uint32
	stewardBoard            stewardBoardState
	embeddedBee             bool              // 为浮动面板里某个子代理嵌入的会话状态：复用 composer（含额度条），但改动标签与 Hero 布局不同
	embeddedPicker          *ModelPickerState // 嵌入 composer 自己的模型选择展开状态，避免与主 composer 的全局状态联动
	pendingDraftFill        *string
	pendingDraftFillIfEmpty bool

	// 长文本粘贴卡片（定义见 composer_paste.go，由 s.mu 保护）：
	// snippets 是输入框附件行里待发送的长文本，发送时接在正文后面。
	snippets []composerSnippet
	// snippetSeq 是卡片自增序号，从 1 开始。
	snippetSeq int
	// snippetPreviewID 是正在预览的卡片 ID，0 表示没有打开预览。
	snippetPreviewID int

	editingFollowUpID     string
	editingFollowUpThread string
	editStashDraft        string
	editStashAttachments  []composerAttachment
	editStashSnippets     []composerSnippet
	drafts                map[string]composerDraft // 各会话草稿（文本 + 仅内存保留的已上传附件）
	draftSaveTimer        *time.Timer              // 草稿落盘防抖定时器
	editGeneration        uint64
	expandedReasoning     map[string]bool
	expandedTool          map[string]bool
	// 助手正文是否已完全显示的登记表（由 s.mu 保护）：revealedMsgs 按消息 ID 记录本帧
	// markdown.Revealed 的结果；streamedRevealMsgs 记录本会话里以流式状态渲染过的消息，
	// 只有它们才等正文显示完再露出时间与操作栏，打开历史会话时已有的消息不受影响。
	revealedMsgs       map[string]bool
	streamedRevealMsgs map[string]struct{}
	// 消息入场动画的登记表（由 s.mu 保护）：seenMsgIDs 记录已判定过的消息，避免重绘时反复播放；
	// streamedMsgIDs 记录流式过程中出现过的消息，等它进入完成态再补播一次。
	seenMsgIDs     map[string]struct{}
	streamedMsgIDs map[string]struct{}
	toolDiff       toolDiffState // 文件修改工具步骤的内嵌 diff 展开状态与缓存
	toastMessage   string

	// 跳转落点高亮：flashMessageID 是当前要高亮的消息，flashSeq 是跳转序号（每次跳转加一）。
	// 淡出结束后按序号比对再清空，避免把新一次跳转的高亮提前抹掉。两者都由 s.mu 保护。
	flashMessageID string
	flashSeq       uint64

	// 运行中命令的观测量：toolRunIdle 按消息 ID 记录输出长度与最后一次变化时刻，用于判定
	// 「长时间无输出」，消息结束后删除对应项避免泄漏；killingTools 记录正在请求终止的消息 ID。
	// 两者都由 s.mu 保护。
	toolRunIdle               map[string]toolRunIdleState
	commandActivity           map[string]*commandActivityState
	commandActivityPending    map[string]bool
	commandActivityNext       map[string]time.Time
	commandActivityGeneration map[string]uint64
	killingTools              map[string]bool

	// 补全状态
	completionKind         completionKind
	completionStart        int
	completionQuery        string
	completionItems        []completionItem
	completionSelected     int
	completionDismissedFor string
	// completionSnap 是补全层最近一次可见的候选，关闭动画期间继续渲染它。
	completionSnap []completionItem
	// completionCaretToEnd 表示补全刚改写了草稿，下一帧需把光标放到文末；TextKey 无法移动光标，只能在构建视图时用 SetTextSelection。
	completionCaretToEnd bool
	completionThreadID   string
	completionSuspended  bool
	completionLoading    bool
	completionLoaded     bool
	commandsCache        map[string][]SlashCommand
	mentionSeq           uint64
	// draftThreadMentions 是草稿上方的会话引用胶囊，与正文分离；发送时按 ThreadID 展开为引用块。
	// 由 s.mu 保护。
	draftThreadMentions []ThreadMention

	// 附件状态
	attachments []composerAttachment
	sending     bool
	sentDraft   *string

	providers           []ProviderInfo
	catalogRequestSeq   uint64
	catalogAppliedSeq   uint64
	catalogRefreshing   bool
	newThreadProvider   string
	pendingThread       *pendingThreadState // 待创建会话（点新建后、首个输入前），nil 表示没有；受 s.mu 保护
	selectedProviderIdx int
	composerPrefsSaving map[string]bool
	modelPrefCache      map[string]ModelPreference // 模型偏好缓存（key: pin.Key()）
	modelPrefLoading    map[string]bool            // 正在加载偏好的 pin
	modelPrefWrites     map[string]chan struct{}   // 同一模型的偏好写入按点击顺序执行

	threadListState ui.ListState
	chatListState   ui.ListState
	mdOpts          markdown.Options
	mdStaticOpts    markdown.Options
	imagePreview    *imagePreviewState // 正在放大的本地图片，nil 表示没有大图浮层
	lastLiveID      string

	// toolOutputScroll 按工具消息 ID 记录详情正文滚动区的位置（仅 UI 线程读写）；
	// toolOutputDialog 是「显示完整输出」弹窗的快照，nil 表示未打开（由 s.mu 保护）。
	toolOutputScroll map[string]*ui.ScrollState
	toolOutputDialog *toolOutputDialogState
	// revealProgress 按消息 ID 记录本窗口的逐字揭示进度：打开会话时已存在的正文记为
	// “已完整显示”；只有打开之后新到达的增量才逐字展示，避免重开会话把整条回答重播一遍。
	revealProgress map[string]*messageReveal
	scrollToEnd    bool // 切换会话或发送消息后，下一帧把时间线滚到底并恢复跟随
	// timelineUnread 按会话记录离开时间线底部那一刻的未读基线，
	// 供“回到底部”按钮显示未读条数（受 s.mu 保护）。
	timelineUnread timelineUnreadState

	sidebarWidth     float32 // 侧边栏宽度
	sidebarCollapsed bool    // ⌘B 折叠侧边栏

	// 模态框状态
	renameModalOpen   bool
	renameThreadID    string
	renameThreadTitle string

	deleteModalOpen   bool
	deleteThreadID    string
	deleteThreadTitle string

	removeProjModalOpen bool
	removeProjID        string
	removeProjName      string

	// 侧边栏整理、批量操作、休眠折叠组与搜索高亮状态
	bulkPopoverProjectID string          // 当前打开整理浮窗的项目 ID
	bulkRangeDays        int             // 整理时间范围天数（1, 3, 7, 30 或 0 表示全部，默认 7）
	bulkIncludePinned    bool            // 整理浮窗是否包含置顶会话
	bulkIncludeDormant   bool            // 整理浮窗是否包含已休眠会话
	lastBulk             *lastBulkInfo   // 上一次批量操作记录，用于撤销条与右键菜单
	expandedDormantProj  map[string]bool // 已休眠会话折叠组展开状态（按 projectID 记录，默认折叠）
	visibleThreadsProj   map[string]int  // 当前展示的活跃会话条数（按 projectID 记录，缺省或 0 表示默认 5 条，受 s.mu 保护）
	searchHighlightIndex int             // 搜索结果键盘高亮项索引
	searchFocusInput     bool            // 回车选择后是否将焦点放回输入框

	// pendingActions 是菜单派发的待执行动作队列，由根视图下一帧出队执行（受 s.mu 保护）。
	pendingActions []pendingMenuAction

	// attentionInbox 是侧栏「待处理」收件箱的数量、列表与订阅状态，懒创建（受 s.mu 保护）。
	attentionInbox *attentionInboxState
}

func newChatState(client Client) *chatState {
	s := &chatState{
		client:              client,
		threadsByProj:       make(map[string][]Thread),
		collapsedProj:       make(map[string]bool),
		expandedDormantProj: make(map[string]bool),
		visibleThreadsProj:  make(map[string]int),
		bulkRangeDays:       7,
		expandedReasoning:   make(map[string]bool),
		expandedTool:        make(map[string]bool),
		revealedMsgs:        make(map[string]bool),
		streamedRevealMsgs:  make(map[string]struct{}),
		seenMsgIDs:          make(map[string]struct{}),
		streamedMsgIDs:      make(map[string]struct{}),
		timelineUnread:      timelineUnreadState{anchors: make(map[string]timelineUnreadAnchor)},
		threadUsage:         ThreadUsage{Turns: make(map[int]TokenUsage)},
		sidebarWidth:        SidebarDefaultWidth,
		commandsCache:       make(map[string][]SlashCommand),
		queue:               make(map[string][]QueuedFollowUp),
		interrupted:         make(map[string]bool),
		continueDismissed:   make(map[string]string),
		optimisticRunning:   make(map[string]bool),
		turnFinished:        make(map[string]string),
		drafts:              make(map[string]composerDraft),
		modelPrefCache:      make(map[string]ModelPreference),
		modelPrefLoading:    make(map[string]bool),
		resolvedPlans:       make(map[string][]resolvedPlan),
		answeredQuestions:   make(map[string][]answeredQuestion),
		collapsedPlans:      make(map[string]bool),
	}
	s.windowActive.Store(true)
	s.inspector.Persist = func(tid string, st *inspectorSessionState) { inspectorPersistSession(s, tid, st) }
	htmlCodeRenderer := func(c *ui.Context, b *markdown.CodeBlockView) {
		ui.Column(c).AlignSelf(ui.Stretch).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Justify(ui.End).Padding(0, 0, Space1, 0).Children(func() {
				Button(c, ButtonOpts{
					Text:    "预览",
					Label:   "预览 HTML",
					Size:    ControlXS,
					Variant: ButtonSecondary,
					OnClick: func() {
						openPreviewHTML(s, b.Code())
					},
				})
			})
			markdown.DefaultCodeBlock(c, b)
		})
	}
	s.mdOpts.Code = map[string]func(c *ui.Context, b *markdown.CodeBlockView){
		"html": htmlCodeRenderer,
	}
	s.mdStaticOpts.Code = map[string]func(c *ui.Context, b *markdown.CodeBlockView){
		"html": htmlCodeRenderer,
	}
	// 选择与右键菜单交给消息容器，避免 Markdown 的内置菜单拦截消息操作。
	s.mdOpts.ShareSelection = true
	s.mdStaticOpts.ShareSelection = true
	// 项目文档预览使用静态 markdown：一次渲染、无打字动画。
	s.mdStaticOpts.Reveal = &markdown.Reveal{Unit: markdown.AtOnce, Fade: -1}
	// 公式语法：默认扩展只认 $$…$$，再加上单美元行内公式 $x^2$。
	extensions := parser.Default | parser.ExtDollarMath
	s.mdOpts.Extensions = extensions
	s.mdStaticOpts.Extensions = extensions
	// 本地图片由 loadLocalImage 加载；点击图片看大图由 Block 钩子接管整段图片段落。
	installImagePreviewHooks(&s.mdOpts, s)
	installImagePreviewHooks(&s.mdStaticOpts, s)
	// 表格太宽会被消息列横向裁切：右上角加「展开」按钮，点击后在可缩放的浮窗里完整查看。
	installTableExpandHook(&s.mdOpts, s)
	installTableExpandHook(&s.mdStaticOpts, s)
	// 公式：行内公式与正文基线对齐，块级公式单独居中；排不出的公式退回原文。
	installMathHooks(&s.mdOpts)
	installMathHooks(&s.mdStaticOpts)
	// 点击链接：本地文件用默认程序打开，网页交给浏览器。
	installLocalFileLinkHook(&s.mdOpts, s)
	installLocalFileLinkHook(&s.mdStaticOpts, s)
	// 只配置一次：用户上滚后 List 自行停止跟随，回到底部再恢复
	s.chatListState.FollowEnd = true
	return s
}

// init 保证仅在初次进入 Build Pass 时初始化 services 与触发项目数据加载及消息队列监听。
func (s *chatState) init(c *ui.Context) {
	s.mu.Lock()
	if s.initialized || s.closed {
		s.mu.Unlock()
		return
	}
	s.initialized = true
	s.setUIServices(c.Services())
	s.mu.Unlock()

	s.startQueueWatch()
	s.startSidebarBeesWatch()
	s.startThreadSummaryWatch()
	s.loadProjects()
}

// startQueueWatch 订阅消息队列变更，在回调中加锁更新 s.queue 并触发重绘。
func (s *chatState) startQueueWatch() {
	s.mu.Lock()
	if s.stopQueueWatch != nil || s.client == nil || s.closed {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	stop := s.client.WatchQueue(func(threadID string, items []QueuedFollowUp) {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		if s.queue == nil {
			s.queue = make(map[string][]QueuedFollowUp)
		}
		if s.queueChanges == nil {
			s.queueChanges = make(map[string]uint64)
		}
		s.queueChanges[threadID]++
		s.queue[threadID] = items
		if s.editingFollowUpThread == threadID && s.editingFollowUpID != "" {
			found := false
			for _, item := range items {
				if item.ID == s.editingFollowUpID {
					found = true
					break
				}
			}
			if !found {
				stash := s.editStashDraft
				s.pendingDraftFill = &stash
				s.pendingDraftFillIfEmpty = false
				s.clearEditingStateLocked()
			}
		}
		s.mu.Unlock()
		s.invalidate()
	})

	s.mu.Lock()
	if s.closed {
		if stop != nil {
			stop()
		}
	} else {
		s.stopQueueWatch = stop
	}
	s.mu.Unlock()
}

// invalidate 通知 MyGo 主循环调度重新绘制。
// 可能在已持有 s.mu 时被调用，所以只能碰 servicesMu 保护的那份服务句柄。
func (s *chatState) invalidate() {
	s.uiServices().Invalidate()
}

// uiServices 返回当前渲染服务句柄；调用方不得持有 servicesMu。
func (s *chatState) uiServices() ui.Services {
	s.servicesMu.RLock()
	services := s.services
	s.servicesMu.RUnlock()
	return services
}

// setUIServices 首次进入 Build Pass（或子窗口绑定）时登记渲染服务句柄。
// 允许在持有 s.mu 时调用；内部不调用任何外部代码。
func (s *chatState) setUIServices(services ui.Services) {
	s.servicesMu.Lock()
	s.services = services
	s.servicesMu.Unlock()
}

// enqueueAction 把菜单派发的动作追加到待执行队列，并请求重绘让根视图出队执行。
func (s *chatState) enqueueAction(id string, index int) {
	s.mu.Lock()
	s.pendingActions = append(s.pendingActions, pendingMenuAction{id: id, index: index})
	s.mu.Unlock()
	s.invalidate()
}

// popPendingAction 取出队首待执行动作；队列为空时返回 false。
func (s *chatState) popPendingAction() (string, int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pendingActions) == 0 {
		return "", 0, false
	}
	action := s.pendingActions[0]
	s.pendingActions = s.pendingActions[1:]
	return action.id, action.index, true
}

// setToast 设置错误提示文本并触发重绘以弹出 Toast。
func (s *chatState) setToast(msg string) {
	owner := toastOwner(s)
	owner.mu.Lock()
	owner.toastMessage = msg
	owner.mu.Unlock()
	owner.invalidate()
}

// providerCatalogRetryDelays 是供应商目录仍是占位行时的补拉时间点（相对初次拉取）。
// CLI 供应商（pi / omp / copilot / droid）的模型目录在后台拉取，pi 最长可达 30 秒，
// 启动快照里只有占位行；改成包级变量便于测试缩短。
var providerCatalogRetryDelays = []time.Duration{5 * time.Second, 15 * time.Second, 40 * time.Second}

// providerAvailabilityRecheckDelay 是首次拉取供应商列表后补拉一次的延迟。
// provider 的可用性（例如钥匙串密钥探测）可能在首次拉取之后才确定，这里补拉一次纠正。
// 改成包级变量便于测试调整（例如改成 0 或调大）。
var providerAvailabilityRecheckDelay = 2 * time.Second

// refreshProviders 只在用户主动操作时强制刷新，重复点击不会启动多个刷新。
func (s *chatState) refreshProviders() {
	s.mu.Lock()
	if s.closed || s.catalogRefreshing {
		s.mu.Unlock()
		return
	}
	s.catalogRefreshing = true
	s.catalogRequestSeq++
	seq := s.catalogRequestSeq
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			s.catalogRefreshing = false
			s.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), modelsRefreshTimeout)
		defer cancel()
		providers, err := s.client.RefreshProviders(ctx)
		if err == nil || providers != nil {
			s.applyProviderCatalog(seq, providers, getChatSettings(s))
		}
	}()
}

func (s *chatState) nextProviderCatalogRequest() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.catalogRequestSeq++
	return s.catalogRequestSeq
}

// applyProviderCatalog 忽略比已应用结果更早发起的请求，并同步作废设置页快照。
// 两把锁不嵌套持有。
func (s *chatState) applyProviderCatalog(seq uint64, providers []ProviderInfo, st *SettingsState) bool {
	s.mu.Lock()
	if s.closed || seq < s.catalogAppliedSeq {
		s.mu.Unlock()
		return false
	}
	s.catalogAppliedSeq = seq
	s.providers = providers
	s.mu.Unlock()
	if st != nil {
		st.mu.Lock()
		st.modelCatalogVersion++
		st.mu.Unlock()
	}
	s.invalidate()
	return true
}

// reloadProviderCatalog 读取缓存，供后台补拉使用。
func (s *chatState) reloadProviderCatalog() ([]ProviderInfo, bool) {
	s.mu.Lock()
	if s.catalogRefreshing {
		s.mu.Unlock()
		return nil, false
	}
	s.catalogRequestSeq++
	seq := s.catalogRequestSeq
	s.mu.Unlock()
	providers, err := s.client.Providers(context.Background())
	if err != nil || !s.applyProviderCatalog(seq, providers, getChatSettings(s)) {
		return nil, false
	}
	return providers, true
}

// catalogPending 判断供应商快照里是否还有没拉到目录的可用供应商：只有一行占位“默认”模型，
// 或者目录仍为空（CLI 目录在后台探测）。补拉次数由 providerCatalogRetryDelays 限定，不会无限轮询。
func catalogPending(providers []ProviderInfo) bool {
	for _, p := range providers {
		if !p.Available {
			continue
		}
		if len(p.Models) == 0 {
			return true
		}
		if len(p.Models) == 1 && p.Models[0].ID == "default" {
			return true
		}
	}
	return false
}

// refreshProviderCatalogUntilReady 在后台按 providerCatalogRetryDelays 补拉供应商列表，
// 直到没有占位目录（一次性按时间点重试，不留常驻 ticker）。
func (s *chatState) refreshProviderCatalogUntilReady(providers []ProviderInfo) {
	go func() {
		start := time.Now()
		for _, delay := range providerCatalogRetryDelays {
			if !catalogPending(providers) {
				return
			}
			if wait := delay - time.Since(start); wait > 0 {
				time.Sleep(wait)
			}
			latest, ok := s.reloadProviderCatalog()
			if !ok {
				s.mu.Lock()
				refreshing := s.catalogRefreshing
				s.mu.Unlock()
				if refreshing {
					continue
				}
				return
			}
			providers = latest
		}
	}()
}

// recordedProviderPreference 返回持久化的“上次使用的 Agent”。
// recorded 为 false 表示还没有使用记录（首次启动），调用方应改用第一个可用的 Agent。
// 没有设置状态或 provider 被启动参数锁定时同样返回 false：此时后端的默认 provider
// 已经由启动参数决定，新会话跟着供应商列表的顺序取即可。
func (s *chatState) recordedProviderPreference() (string, bool) {
	st := getChatSettings(s)
	if st == nil || st.isLocked("provider") {
		return "", false
	}
	// 读 cfg 只持 cfgMu 就够：写入方（rememberUsedProvider）改 cfg 时同时持有 st.mu 与
	// st.cfgMu，读者持其中任意一把都能与写互斥。
	st.cfgMu.Lock()
	provider := strings.TrimSpace(st.cfg.Models.Provider)
	selected := st.cfg.Models.ProviderSelected
	st.cfgMu.Unlock()
	if !selected || provider == "" {
		return "", false
	}
	return provider, true
}

// rememberUsedProvider 把用户刚用过的 Agent 记为“上次使用”：写回配置并持久化，
// 同时更新进程内的新会话默认值，后续新建会话无需重启就沿用。
// provider 被启动参数或环境变量锁定时只更新进程内默认值，不覆盖写回配置。
func (s *chatState) rememberUsedProvider(providerID string) {
	providerID = strings.TrimSpace(providerID)
	if s == nil || providerID == "" || providerID == config.ProviderMcode || s.embeddedBee {
		// 子代理输入框里的模型切换不代表用户的默认 Agent 选择。
		return
	}
	s.mu.Lock()
	s.newThreadProvider = providerID
	s.mu.Unlock()

	st := getChatSettings(s)
	if st == nil || st.isLocked("provider") {
		return
	}
	// 改 cfg 要同时持有 st.mu 和 st.cfgMu，顺序固定为 mu → cfgMu：渲染路径（如
	// isAutoCollapseReasoningEnabled）在 st.mu 下读 st.cfg，save 与 saveSnapshot 在
	// st.cfgMu 下读写，只持其中一把都会和另一类读者竞争，go test -race 会报出来。
	st.mu.Lock()
	st.cfgMu.Lock()
	changed := !st.cfg.Models.ProviderSelected || st.cfg.Models.Provider != providerID
	st.cfg.Models.Provider = providerID
	st.cfg.Models.ProviderSelected = true
	st.cfgMu.Unlock()
	st.mu.Unlock()
	if changed {
		st.save()
	}
}

// initNewThreadProvider 设置新会话默认使用的 Agent：有使用记录时沿用上次使用的 Agent，
// 首次启动（还没有使用记录）时按供应商列表顺序取第一个可用的 Agent，并记为“上次使用”。
func (s *chatState) initNewThreadProvider(providers []ProviderInfo) {
	preferred, _ := s.recordedProviderPreference()
	s.mu.Lock()
	ctx := newThreadProviderContext{threads: append([]Thread(nil), s.threads...), stewardThreadID: s.stewardThreadID(), preferred: preferred}
	s.mu.Unlock()
	id := defaultNewThreadProvider(providers, ctx)
	if id != "" {
		s.rememberUsedProvider(id)
	}
}

// loadProjects 在后台 goroutine 中拉取所有项目及其会话列表，并在启动时选中全部会话中最新的一条。
func (s *chatState) loadProjects() {
	go func() {
		ctx := context.Background()
		threadsLoaded := make(chan struct{})

		// providers 只影响默认 Agent 与模型列表，项目/会话列表并不依赖它，
		// 所以放到独立 goroutine 里与后面的拉取并行，避免侧边栏首帧多等几百毫秒。
		seq := s.nextProviderCatalogRequest()
		go func() {
			providers, pErr := s.client.Providers(ctx)
			<-threadsLoaded
			s.mu.Lock()
			needProvider := s.newThreadProvider == ""
			s.mu.Unlock()
			if pErr == nil && s.applyProviderCatalog(seq, providers, getChatSettings(s)) {
				var available []ProviderInfo
				for _, p := range providers {
					if p.Available {
						available = append(available, p)
					}
				}
				if needProvider {
					// 选默认 Agent 要读配置（首次启动还会写回），放在锁外避免嵌套加锁。
					s.initNewThreadProvider(providers)
				}
				s.mu.Lock()
				if len(available) > 0 {
					idx := 0
					for i, p := range available {
						if p.ID == s.newThreadProvider {
							idx = i
							break
						}
					}
					s.selectedProviderIdx = idx
				}
				s.mu.Unlock()

				// CLI 目录还在后台拉取时快照里只有占位行，后台补拉后再刷新模型列表。
				if catalogPending(providers) {
					s.refreshProviderCatalogUntilReady(providers)
				}
				// provider 的可用性（例如钥匙串密钥探测）可能在首次拉取之后才确定，
				// UI 不会自动重拉，这里隔一会儿补拉一次纠正。
				go func() {
					time.Sleep(providerAvailabilityRecheckDelay)
					_, _ = s.reloadProviderCatalog()
				}()
			}
			startuptrace.Mark("providers loaded")
			// 拆开后台后，这次刷新不再由侧边栏数据加载完的 invalidate 顺带完成。
			s.invalidate()
		}()

		projects, err := s.client.Projects(ctx)
		if err != nil {
			close(threadsLoaded)
			s.setToast("获取项目失败: " + err.Error())
			return
		}

		s.mu.Lock()
		s.projects = projects
		s.mu.Unlock()

		// 对每个项目拉取会话列表
		type projThreads struct {
			projID  string
			threads []Thread
		}
		var wg sync.WaitGroup
		resCh := make(chan projThreads, len(projects))

		for _, p := range projects {
			wg.Add(1)
			go func(pid string) {
				defer wg.Done()
				ths, err := s.client.Threads(ctx, pid)
				if err == nil {
					resCh <- projThreads{projID: pid, threads: SortThreads(ths)}
				}
			}(p.ID)
		}

		wg.Wait()
		close(resCh)

		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			close(threadsLoaded)
			return
		}
		var allThreads []Thread
		loadedProjects := 0
		for pt := range resCh {
			loadedProjects++
			s.threadsByProj[pt.projID] = pt.threads
			allThreads = append(allThreads, pt.threads...)
		}
		s.threads = SortThreads(allThreads)
		// 整表重载可能带来新的待处理请求/运行标记，让待处理请求集合失效。
		s.bumpThreadsRuntimeLocked()

		var targetThreadID string
		if s.initialThreadID != "" {
			targetThreadID = s.initialThreadID
			s.initialThreadID = ""
		} else if len(s.threads) > 0 && s.pendingThread != nil {
			// 正在编辑待创建会话：不自动选中任何会话，避免把用户从新会话页拽走。
		} else if len(s.threads) > 0 {
			if s.selectedThreadID == "" {
				for _, th := range s.threads {
					if !th.Dormant {
						targetThreadID = th.ID
						break
					}
				}
				if targetThreadID == "" {
					targetThreadID = s.threads[0].ID
				}
			} else {
				exists := false
				for _, th := range s.threads {
					if th.ID == s.selectedThreadID {
						exists = true
						break
					}
				}
				if !exists {
					for _, th := range s.threads {
						if !th.Dormant {
							targetThreadID = th.ID
							break
						}
					}
					if targetThreadID == "" {
						targetThreadID = s.threads[0].ID
					}
				} else if d := s.takeDraftLocked(s.selectedThreadID); d.Text != "" {
					// 会话已选中且仍存在，不会走 selectThread，这里直接回填它的草稿。
					s.pendingDraftFill = &d.Text
					s.pendingDraftFillIfEmpty = false
				}
			}
		}
		s.mu.Unlock()
		if loadedProjects == len(projects) {
			inspectorPrunePreferences(s)
		}
		close(threadsLoaded)
		startuptrace.Mark("sidebar data loaded")
		s.invalidate()

		// 会话列表加载后按配置的天数自动休眠超期会话（内部按小时节流）。
		s.maybeAutoDormant(time.Now(), s.autoDormantDays())

		if targetThreadID != "" {
			s.selectThread(targetThreadID)
		}
	}()
}

// loadThreads 在后台 goroutine 中拉取指定项目的会话列表。autoSelect 为 true 时自动选中最新的会话。
func (s *chatState) loadThreads(projectID string, autoSelect bool) {
	go func() {
		ctx := context.Background()
		threads, err := s.client.Threads(ctx, projectID)
		if err != nil {
			s.setToast("获取会话列表失败: " + err.Error())
			return
		}

		sorted := SortThreads(threads)

		s.mu.Lock()
		s.threadsByProj[projectID] = sorted
		var all []Thread
		for _, list := range s.threadsByProj {
			all = append(all, list...)
		}
		s.threads = SortThreads(all)
		s.bumpThreadsRuntimeLocked()

		var targetThreadID string
		if s.initialThreadID != "" {
			targetThreadID = s.initialThreadID
			s.initialThreadID = ""
		} else if autoSelect && len(sorted) > 0 && s.pendingThread == nil {
			for _, th := range sorted {
				if !th.Dormant {
					targetThreadID = th.ID
					break
				}
			}
			if targetThreadID == "" {
				targetThreadID = sorted[0].ID
			}
		}
		s.mu.Unlock()

		s.invalidate()

		// 会话列表加载后按配置的天数自动休眠超期会话（内部按小时节流）。
		s.maybeAutoDormant(time.Now(), s.autoDormantDays())

		if targetThreadID != "" {
			s.selectThread(targetThreadID)
		}
	}()
}

// maxMessagesTurn 返回当前会话消息里的最大回合号，按 messagesRev 缓存：
// Swarm 回合完成块的推迟判定对每个回合完成块都会问一次，每帧重扫全部消息是纯浪费。
func (s *chatState) maxMessagesTurn() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.maxTurnValid && s.maxTurnRev == s.messagesRev {
		return s.maxTurnValue
	}
	maxTurn := 0
	for _, msg := range s.messages {
		if msg.TurnIndex > maxTurn {
			maxTurn = msg.TurnIndex
		}
	}
	s.maxTurnRev = s.messagesRev
	s.maxTurnValue = maxTurn
	s.maxTurnValid = true
	return maxTurn
}

// runningTurn 返回该会话是否仍在跑这一轮，以及消息里的最新回合号。
// 回合进行中每次模型调用结束都会先写一次该回合用量（后端 EventUsage 分支），只看用量会在步骤之间的
// 间隙提前冒出「已完成」，所以完成块要按运行状态判断。刚发出新消息、后端还没回推这一轮时是本地乐观
// 运行中，此时消息里最大的回合仍是上一轮、且它已经结束：按「没在跑」处理，免得上一轮的完成块闪一下再回来。
func (s *chatState) runningTurn(threadID string) (bool, int) {
	if s == nil || threadID == "" {
		return false, 0
	}
	s.mu.Lock()
	running := false
	for _, th := range s.threads {
		if th.ID == threadID {
			running = th.IsRunning || th.SwarmBusy
			break
		}
	}
	optimistic := s.optimisticRunning[threadID]
	s.mu.Unlock()
	if !running || optimistic {
		return false, 0
	}
	return true, s.maxMessagesTurn()
}

// lastAssistantPerTurn 返回“回合号 -> 该回合最后一条助手消息 ID”，按 messagesRev 缓存：
// 时间线每帧都要为每个条目问“它是不是本回合最后一条助手消息”，
// 子代理浮窗的 200 条消息 × 4 个面板逐帧重建这张表是纯浪费。
// 返回值只读，调用方不得修改。
func (s *chatState) lastAssistantPerTurn() map[int]string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastAsstValid && s.lastAsstRev == s.messagesRev {
		return s.lastAsstByTurn
	}
	last := make(map[int]string)
	for _, m := range s.messages {
		if m.Role == "assistant" {
			last[m.TurnIndex] = m.ID
		}
	}
	s.lastAsstRev = s.messagesRev
	s.lastAsstByTurn = last
	s.lastAsstValid = true
	return last
}

// pendingRequestThreadIDs 返回当前会话列表里“有待处理请求”的会话 ID 集合，
// 按 threadsRuntimeRev 缓存：子代理浮窗、标题按钮与自动收起每帧都要问“哪个子线程在等用户”，
// 每帧重扫全部会话列表是纯浪费。返回的 map 只读，调用方不得修改。
func (s *chatState) pendingRequestThreadIDs() map[string]bool {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingReqIDs != nil && s.pendingReqIDsRev == s.threadsRuntimeRev {
		return s.pendingReqIDs
	}
	ids := make(map[string]bool, 4)
	for _, th := range s.threads {
		if th.HasPendingRequest {
			ids[th.ID] = true
		}
	}
	s.pendingReqIDs = ids
	s.pendingReqIDsRev = s.threadsRuntimeRev
	return ids
}

// bumpThreadsRuntimeLocked 递增会话运行态版本号，调用方需持有 s.mu。
// 任何可能改变 HasPendingRequest / IsRunning 的地方都要调它，否则待处理请求集合会停留在旧值。
func (s *chatState) bumpThreadsRuntimeLocked() {
	s.threadsRuntimeRev++
}

// fetchUsage 在后台拉取指定会话的 token 用量统计，存入 chatState 并通知界面重绘。
func (s *chatState) fetchUsage(threadID string) {
	if threadID == "" {
		return
	}
	go func(tid string) {
		ctx := context.Background()
		u, err := s.client.Usage(ctx, tid)
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.selectedThreadID == tid {
			s.threadUsage = u
		}
		s.mu.Unlock()
		s.invalidate()
	}(threadID)
}

// selectThread 切换会话：先注销前一个 Watch，再加载 History、Approvals 和 Usage，最后 Watch 新会话。
// 切换前把当前会话草稿（含排队项编辑暂存）写回草稿表，切换后回填目标会话的草稿。
func (s *chatState) selectThread(threadID string) {
	inspectorRemember(s)
	stewardID := s.stewardThreadID()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	// 先读出旧会话的草稿：正在编辑排队项时，输入框里是排队项文本，用户草稿在暂存字段里。
	oldThreadID := s.selectedThreadID
	// 切走时清掉「首条消息已发出」的透视标记，避免它在别的会话里再次触发停靠动画。
	s.disarmComposerDockPromote(oldThreadID)
	oldText := s.draft
	oldAtts := s.attachments
	oldSnippets := s.snippets
	editingQueued := s.editingFollowUpID != ""
	if editingQueued {
		oldText = s.editStashDraft
		oldAtts = s.editStashAttachments
		oldSnippets = s.editStashSnippets
	}
	if oldThreadID != threadID {
		s.stashDraftLocked(oldThreadID, oldText, oldAtts, oldSnippets)
		stopStewardTaskWatchLocked(s)
	}
	s.watchSeq++
	seq := s.watchSeq
	if s.watchStop != nil {
		s.watchStop()
		s.watchStop = nil
	}
	s.selectedThreadID = threadID
	if threadID != s.stewardTaskThreadID {
		s.stewardTaskThreadID = ""
		s.stewardTask = stewardTaskSnapshot{}
	}
	// 打开会话即视为已查看：撤掉侧栏上的完成小红点与红色标题。
	s.clearTurnFinishedLocked(threadID)
	readProjectID := ""
	// 主会话的消息只是结果通知，打开即算看过。
	if threadID != "" && threadID == stewardID {
		for i := range s.threads {
			if s.threads[i].ID == threadID && s.threads[i].HasUnread {
				readProjectID = s.threads[i].ProjectID
				s.threads[i].HasUnread = false
			}
		}
		for _, list := range s.threadsByProj {
			for i := range list {
				if list[i].ID == threadID && list[i].HasUnread {
					if readProjectID == "" {
						readProjectID = list[i].ProjectID
					}
					list[i].HasUnread = false
				}
			}
		}
	}
	if threadID != "" {
		// 切到真实会话即丢弃待创建会话，不在后端留下空会话。
		s.pendingThread = nil
	}
	s.pendingDraftFill = nil
	s.pendingDraftFillIfEmpty = false
	s.clearEditingStateLocked()
	if oldThreadID != threadID {
		d := s.takeDraftLocked(threadID)
		// 草稿为空也要回填，以清空上一个会话残留在输入框里的文本。
		s.pendingDraftFill = &d.Text
		s.pendingDraftFillIfEmpty = false
		s.attachments = d.Attachments
		s.snippets = d.Snippets
		s.snippetPreviewID = 0
	} else if editingQueued {
		// 同一会话重新选中：排队编辑暂存的草稿仍归它自己，保持原来的回填行为。
		stash := oldText
		s.pendingDraftFill = &stash
		s.pendingDraftFillIfEmpty = false
	}
	s.completionKind = completionNone
	s.completionItems = nil
	s.completionDismissedFor = ""
	s.mentionSeq++
	s.messages = nil
	s.messagesRev++
	// 切换会话时清空入场登记：新会话的消息要重新按时间窗判定，旧会话的流式记录也不再有用。
	s.seenMsgIDs = make(map[string]struct{})
	s.streamedMsgIDs = make(map[string]struct{})
	s.revealProgress = nil
	s.revealedMsgs = make(map[string]bool)
	s.streamedRevealMsgs = make(map[string]struct{})
	s.toolDiff = toolDiffState{generation: s.toolDiff.generation + 1}
	s.approvals = nil
	s.threadUsage = ThreadUsage{}
	s.lastLiveID = ""
	s.stopping = false
	s.stoppingSince = time.Time{}
	s.scrollToEnd = true
	s.mu.Unlock()
	if oldThreadID != threadID {
		inspectorSwitchThread(s, oldThreadID)
	}
	if readProjectID != "" {
		go func() {
			if err := s.client.BulkMarkRead(context.Background(), readProjectID, []string{threadID}); err != nil {
				s.setToast("标记已读失败: " + err.Error())
			}
		}()
	}
	if threadID != "" && threadID == stewardID {
		time.AfterFunc(stewardDecisionCountRefresh, s.refreshStewardDecisionCount)
	}
	s.invalidate()
	if oldThreadID != threadID {
		// 草稿表发生变化，后台补一次落盘（内部自行加锁）。
		s.persistDraftsAsync()
	}

	// 新会话页（包括待创建会话）没有后端会话，不拉取任何会话数据。
	if threadID == "" {
		return
	}
	if threadID != s.stewardThreadID() {
		loadStewardTaskForThread(s, threadID)
	}

	go func(tid string) {
		ctx := context.Background()

		historyStart := time.Now()
		history, hErr := s.client.History(ctx, tid)
		initialHistoryOnce.Do(func() {
			ms := time.Since(historyStart).Milliseconds()
			startuptrace.Mark(fmt.Sprintf("initial history loaded n=%d took=%dms", len(history), ms))
		})
		if hErr != nil {
			s.setToast("加载历史消息失败: " + hErr.Error())
		}

		approvals, aErr := s.client.Approvals(ctx, tid)
		if aErr != nil {
			s.setToast("加载审批请求失败: " + aErr.Error())
		}

		usage, uErr := s.client.Usage(ctx, tid)

		s.mu.Lock()
		queueChange := s.queueChanges[tid]
		s.mu.Unlock()
		queueItems, qErr := s.client.Queue(ctx, tid)

		s.mu.Lock()
		if s.closed || s.watchSeq != seq || s.selectedThreadID != tid {
			s.mu.Unlock()
			return
		}
		if hErr == nil {
			s.messages = UpsertMessages(nil, history, nil)
			s.messagesRev++
			// 打开会话时已有的正文立即完整显示，只有之后新流入的增量才逐字揭示。
			s.markRevealBaselineLocked(s.messages)
		}
		if aErr == nil {
			s.approvals = approvals
			s.pruneApprovalDraftsLocked()
		}
		if uErr == nil {
			s.threadUsage = usage
		}
		if qErr == nil && s.queueChanges[tid] == queueChange {
			if s.queue == nil {
				s.queue = make(map[string][]QueuedFollowUp)
			}
			s.queue[tid] = queueItems
		}
		s.mu.Unlock()
		s.invalidate()

		stop := s.client.Watch(tid, func(update ThreadUpdate) {
			s.onWatchUpdate(update)
		}, func(approvals []Approval) {
			s.onWatchApprovals(tid, approvals)
		})

		s.mu.Lock()
		watching := !s.closed && s.watchSeq == seq && s.selectedThreadID == tid
		if watching {
			s.watchStop = stop
		} else {
			if stop != nil {
				stop()
			}
		}
		eventsBefore := s.approvalsEvents
		s.mu.Unlock()
		if !watching {
			return
		}

		// 上面「先拉审批、再订阅」之间有空窗：这期间新增的审批既不在拉取结果里，也不会再推送，
		// agent 会一直等审批而界面不显示。订阅建立后补拉一次；补拉期间若已收到推送（推送是完整列表且不早于订阅），
		// 以推送为准，不用补拉结果覆盖。
		if refetched, err := s.client.Approvals(ctx, tid); err == nil {
			s.mu.Lock()
			if !s.closed && s.watchSeq == seq && s.selectedThreadID == tid && s.approvalsEvents == eventsBefore {
				s.approvals = refetched
				s.pruneApprovalDraftsLocked()
			}
			s.mu.Unlock()
			s.invalidate()
		}
	}(threadID)
}

// messageReveal 记录一条助手消息在本窗口的逐字揭示进度。
type messageReveal struct {
	// shown 是已经一次性完整显示过的正文字节数（即该帧 m.Text 的长度）。
	shown int
	// streaming 为真表示已进入本窗口的逐字揭示阶段，之后的增量逐字显示。
	streaming bool
}

// markRevealBaselineLocked 把会话原有的消息记为“已完整显示”。打开会话时调用：
// 这些正文早已存在，不应再以打字机效果从头重播。调用方需持有 s.mu。
func (s *chatState) markRevealBaselineLocked(messages []Message) {
	s.revealProgress = make(map[string]*messageReveal, len(messages))
	for _, m := range messages {
		if m.ID == "" {
			continue
		}
		text := m.Text
		if m.Role == "assistant" && !isReasoningMessage(m) {
			text = stripSwarmFence(text)
		}
		s.revealProgress[m.ID] = &messageReveal{shown: len(text)}
	}
}

// resetRevealBaseline 把会话的揭示基线重置为“当前正文已完整显示”。
// 视图（子代理浮窗、独立窗口）被销毁重建后，Markdown 视图里的揭示进度随元素树一起丢失，
// 此时必须重置基线：否则会话状态仍记着“正在逐字揭示”，重建后的视图会从头重播一遍。
func (s *chatState) resetRevealBaseline() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markRevealBaselineLocked(s.messages)
}

// takeStreamReveal 判断一条正在流式输出的消息是否走逐字揭示，并推进揭示基线。
// 打开会话时已存在的正文立即完整显示；打开后第一次看到的增量先整段铺满，
// 此后的增量才逐字揭示，避免把历史内容从头播一遍。
func (s *chatState) takeStreamReveal(m Message) bool {
	if m.ID == "" {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revealProgress == nil {
		s.revealProgress = make(map[string]*messageReveal)
	}
	st, ok := s.revealProgress[m.ID]
	if !ok {
		// 打开会话之后才出现的消息：本来就是本窗口流式生成的，逐字展示。
		s.revealProgress[m.ID] = &messageReveal{shown: len(m.Text), streaming: true}
		return true
	}
	if st.streaming {
		return true
	}
	if len(m.Text) <= st.shown {
		return false
	}
	// 打开之后新到达的增量：本帧把当前正文一次铺满，下一帧起逐字揭示新增部分。
	st.shown = len(m.Text)
	st.streaming = true
	return false
}

// turnQuotaRefreshTimeout 是回合结束后按会话刷新额度的等待上限：超时就放弃刷新，
// 但无论如何都重新读一次面板数据，不阻塞后续回合。
const turnQuotaRefreshTimeout = 35 * time.Second

// turnEndQuotaKey 判断这一轮的额度 key 是否需要按会话让 Magpie 重读：pi 的端点 key
// 与经网关的 claude、codex 都要重读本会话应答的账号，直连提供方没有可刷新的账号。
func turnEndQuotaKey(key string) bool {
	if strings.HasPrefix(key, "pi:") {
		return true
	}
	return key == "claude" || key == "codex"
}

// onWatchUpdate 在收到 Watch 增量更新时安全更新本地消息与会话状态，并在从运行中变为空闲时拉取最新用量。
func (s *chatState) onWatchUpdate(update ThreadUpdate) {
	browserServerObserveToolOutput(s, update)
	inspectorFollowObserve(s, update)
	stewardID := s.stewardThreadID()
	readProjectID := ""
	markRead := false
	refreshDecision := false
	var shouldFetchUsage bool
	// endedQuotaKey 记录刚结束的这一轮所属会话的额度 key（pi 线程带 magpie 端点前缀，claude/codex 用提供方名）。
	var endedQuotaKey string
	// resyncThreadID 非空时说明这一帧是后端因帧体超限改发的重新同步帧：帧里不带消息，
	// 需要按该会话重拉全量消息，否则这一批消息就永久缺失。
	var resyncThreadID string

	s.mu.Lock()
	// 后端已给出该会话的权威摘要（运行中、已结束或出错）：撤销本地乐观标记，状态以后端为准。
	delete(s.optimisticRunning, update.ThreadID)
	for _, th := range s.threads {
		if th.ID == update.ThreadID {
			// 停止请求成功后仍可能收到同一轮的运行中增量，只有新一轮开始才清除标记。
			if !th.IsRunning && update.Thread.IsRunning {
				delete(s.interrupted, update.ThreadID)
			}
			break
		}
	}
	if s.selectedThreadID == update.ThreadID {
		if update.ThreadID == stewardID && update.Thread.HasUnread {
			markRead = true
			update.Thread.HasUnread = false
		}
		if update.ThreadID == stewardID {
			for _, msg := range update.Messages {
				if msg.Kind == stewardDecisionAnswerKind {
					refreshDecision = true
					break
				}
			}
		}
		var wasRunning bool
		for _, th := range s.threads {
			if th.ID == update.ThreadID {
				wasRunning = th.IsRunning
				break
			}
		}
		if wasRunning && !update.Thread.IsRunning {
			shouldFetchUsage = true
			// 增量里没带模型时回退到本地会话摘要，确保 pi 线程拿得到端点 key。
			endedQuotaKey = threadQuotaProvider(&update.Thread)
			if !strings.HasPrefix(endedQuotaKey, "pi:") {
				for _, th := range s.threads {
					if th.ID == update.ThreadID {
						endedQuotaKey = threadQuotaProvider(&th)
						break
					}
				}
			}
		} else if len(update.Messages) == 0 && len(update.RemovedIDs) == 0 {
			// 后端发布空增量时也刷新用量
			shouldFetchUsage = true
		}
		s.messages = UpsertMessages(s.messages, update.Messages, update.RemovedIDs)
		s.messagesRev++
		if update.Resync {
			resyncThreadID = update.ThreadID
		}
		refreshDiff := shouldFetchUsage || len(update.RemovedIDs) > 0
		for _, msg := range update.Messages {
			refreshDiff = refreshDiff || msg.Role == "tool" || msg.IsFinal
		}
		if refreshDiff {
			s.toolDiff.entries = nil
			s.toolDiff.turnKey = nil
			s.toolDiff.generation++
		}
		if !update.Thread.IsRunning && !update.Thread.SwarmBusy {
			// 只有主会话与名下子线程都空下来，这一轮才算真正结束。
			s.stopping = false
		}
	}

	// 1. 按 ProjectID 更新对应的分组
	pid := update.Thread.ProjectID
	if pid == "" {
		for _, th := range s.threads {
			if th.ID == update.ThreadID {
				pid = th.ProjectID
				break
			}
		}
	}
	if markRead {
		readProjectID = pid
	}
	if pid != "" {
		list := s.threadsByProj[pid]
		found := false
		for i, th := range list {
			if th.ID == update.ThreadID {
				list[i] = update.Thread
				found = true
				break
			}
		}
		if !found {
			list = append(list, update.Thread)
		}
		s.threadsByProj[pid] = SortThreads(list)
	}

	// 2. 更新全局 threads 列表：未知会话只在“运行中”时补进平铺列表，
	// 否则后端推送的运行状态会被丢弃，标题栏与输入框看不到“回复中”；
	// 非运行中的未知会话交给会话列表刷新处理，避免把已归档/删除的会话塞回侧边栏。
	foundGlobal := false
	for i, th := range s.threads {
		if th.ID == update.ThreadID {
			s.threads[i] = update.Thread
			foundGlobal = true
			break
		}
	}
	if !foundGlobal && update.Thread.IsRunning {
		s.threads = append(s.threads, update.Thread)
	}
	s.threads = SortThreads(s.threads)
	// 推送里的会话摘要带着权威的 HasPendingRequest / IsRunning，待处理请求集合必须失效。
	s.bumpThreadsRuntimeLocked()
	s.mu.Unlock()
	if readProjectID != "" {
		go func() {
			if err := s.client.BulkMarkRead(context.Background(), readProjectID, []string{update.ThreadID}); err != nil {
				s.setToast("标记已读失败: " + err.Error())
			}
		}()
	}
	if refreshDecision {
		time.AfterFunc(stewardDecisionCountRefresh, s.refreshStewardDecisionCount)
	}
	s.invalidate()

	if resyncThreadID != "" {
		s.reloadMessagesAfterResync(resyncThreadID)
	}

	if shouldFetchUsage {
		// 回合结束后本会话的 magpie 额度已变化（pi、claude、codex）：先让网关只重读
		// 本会话实际应答的账号，再绕过一分钟节流重新取面板数据。刷新在后台进行，失败只记日志。
		if turnEndQuotaKey(endedQuotaKey) && s.client != nil {
			go func(threadID, provider string) {
				ctx, cancel := context.WithTimeout(context.Background(), turnQuotaRefreshTimeout)
				defer cancel()
				if err := s.client.RefreshThreadQuota(ctx, threadID, provider); err != nil {
					log.Printf("刷新会话额度失败: %v", err)
				}
				s.loadComposerQuotaForProvider(provider, "", "", true)
				s.invalidate()
			}(update.ThreadID, endedQuotaKey)
		}
		s.fetchUsage(update.ThreadID)
		s.loadGitState(update.ThreadID)
	}
}

// reloadMessagesAfterResync 在收到 resync 帧后按会话重拉全量消息并替换本地副本。
// 帧本身不带消息（后端因帧体超过上限只重发摘要），不重拉就看不到这一批消息。
func (s *chatState) reloadMessagesAfterResync(threadID string) {
	if threadID == "" {
		return
	}
	go func(tid string) {
		ctx := context.Background()
		history, err := s.client.History(ctx, tid)
		if err != nil {
			s.setToast("重新同步消息失败: " + err.Error())
			return
		}
		s.mu.Lock()
		// 拉取期间用户可能已切到别的会话，那边的消息不能被子线程覆盖。
		if s.closed || s.selectedThreadID != tid {
			s.mu.Unlock()
			return
		}
		s.messages = UpsertMessages(nil, history, nil)
		s.messagesRev++
		// 重新同步拿到的是已存在的正文，直接完整显示，不再走打字机效果。
		s.markRevealBaselineLocked(s.messages)
		s.toolDiff = toolDiffState{generation: s.toolDiff.generation + 1}
		s.mu.Unlock()
		s.invalidate()
	}(threadID)
}

// onWatchApprovals 在收到审批变化时安全更新本地审批列表。
func (s *chatState) onWatchApprovals(threadID string, approvals []Approval) {
	s.mu.Lock()
	s.approvalsEvents++
	if s.selectedThreadID == threadID {
		s.approvals = approvals
		s.pruneApprovalDraftsLocked()
	}
	s.mu.Unlock()
	s.invalidate()
}

// newThreadOptionsForProject 构造新建会话选项。优先级：
// 1. 项目记忆（含完整 provider+model）> 上次在模型选择器里确认的选择（provider 仍启用、模型仍在目录里时沿用，含推理强度与快速模式）；
// 2. provider 模型列表的第一个模型。
// 这是只读路径：过滤掉非法值但绝不回写记忆。
// enabled 为可选的 provider 启用表（见 newThreadOptions），未传时所有 provider 视为启用。
func newThreadOptionsForProject(projectID string, provider string, providers []ProviderInfo, pins []ModelPin, enabled ...map[string]bool) NewThreadOptions {
	opts := NewThreadOptions{Provider: provider}
	var providerEnabled map[string]bool
	if len(enabled) > 0 {
		providerEnabled = enabled[0]
	}
	// 记忆里的 provider 已在设置里停用时整条跳过。
	// 项目记忆优先于全局「上次确认的选择」；项目里没有完整记录时 lastSelectionForProject 自己退回全局。
	var last ModelSelection
	var hasLast bool
	if strings.TrimSpace(projectID) != "" {
		last, hasLast = lastSelectionForProject(projectID)
	} else {
		last, hasLast = loadLastModelSelection()
	}
	if hasLast && !providerDisabled(providerEnabled, last.Provider) {
		if len(providers) == 0 {
			opts.Provider = last.Provider
			opts.Model = last.Model
			opts.Effort = last.Effort
			opts.Fast = last.Fast
		} else if prov, found := findProviderInfo(providers, last.Provider); found && modelUsable(prov, last.Model, pins) {
			opts.Provider = last.Provider
			opts.Model = last.Model
			opts.Effort = effortSupportedByModels(prov.Models, last.Model, last.Effort)
			opts.Fast = last.Fast
		}
	}
	// 上次的 Swarm 开关与执行侧选择跨项目沿用；执行侧模型已不可用时保持跟随主导。
	swarm := loadLastSwarmSelection()
	if h := swarm.Bees; h != nil {
		followsLead := h.Provider == "" && h.Model == ""
		switch {
		case followsLead:
			// 跟随主导：不关心 provider 启用状态，强度按最终主导模型校验。
			opts.BeesProvider, opts.BeesModel = h.Provider, h.Model
			opts.BeesEffort = h.Effort
			if len(providers) > 0 {
				if prov, found := findProviderInfo(providers, opts.Provider); found {
					opts.BeesEffort = effortSupportedByModels(prov.Models, opts.Model, h.Effort)
				}
			}
		case providerDisabled(providerEnabled, h.Provider):
			// 记忆里的执行侧 provider 已停用：跳过，保持跟随主导。
		case len(providers) == 0:
			opts.BeesProvider, opts.BeesModel, opts.BeesEffort = h.Provider, h.Model, h.Effort
		default:
			if prov, found := findProviderInfo(providers, h.Provider); found && modelUsable(prov, h.Model, pins) {
				opts.BeesProvider, opts.BeesModel = h.Provider, h.Model
				opts.BeesEffort = effortSupportedByModels(prov.Models, h.Model, h.Effort)
			}
		}
	}
	if swarm.Enabled != nil {
		opts.SwarmEnabled = *swarm.Enabled
	}
	if opts.Model == "" {
		if prov, found := findProviderInfo(providers, opts.Provider); found && len(prov.Models) > 0 {
			opts.Model = prov.Models[0].ID
		}
	}
	return opts
}

// effortSupportedByModels 按目录能力校验记忆里的推理强度：模型没有声明强度列表
// （如 CLI 占位目录）时无法判断，保留原值；声明了但不含该强度时按空字符串处理。
// 只用于过滤读取结果，不回写记忆。
func effortSupportedByModels(models []ModelInfo, modelID, effort string) string {
	if effort == "" {
		return ""
	}
	m, found := findModelInfo(models, modelID)
	if !found || len(m.Efforts) == 0 {
		return effort
	}
	if !modelSupportsEffort(m, effort) {
		return ""
	}
	return effort
}

// newThreadOptions 计算新建会话的初始选项（上次确认的模型/强度与 Swarm 开关）。
// 项目 ID 可选：给定项目时按项目记忆恢复模型（见 newThreadOptionsForProject），
// 不传时只用全局记忆，行为与旧版一致。
func (s *chatState) newThreadOptions(projectID ...string) NewThreadOptions {
	s.mu.Lock()
	provider := s.newThreadProvider
	providers := append([]ProviderInfo(nil), s.providers...)
	s.mu.Unlock()

	project := ""
	if len(projectID) > 0 {
		project = projectID[0]
	}
	opts := NewThreadOptions{Provider: provider}
	if st := getChatSettings(s); st != nil {
		st.mu.Lock()
		swarmGlobalOn := st.cfg.Orchestration.IsEnabled()
		enabled := ProviderEnabledMap(st.cfg)
		st.mu.Unlock()
		opts = newThreadOptionsForProject(project, provider, providers, pinnedModelPins(s), enabled)
		// 全局总开关已关时不替用户重新打开 Swarm。
		opts.SwarmEnabled = opts.SwarmEnabled && swarmGlobalOn
	}
	return opts
}

// createThread 进入指定项目的待创建会话：输入或发送时才真正建会话，空会话不进侧栏。
func (s *chatState) createThread(projectID string) {
	s.beginPendingThread(projectID, s.newThreadOptions(projectID), false)
}

// createNewThread 在当前选中会话所属的项目里新建（没有选中会话时用第一个项目）。
func (s *chatState) createNewThread() {
	s.mu.Lock()
	var targetProjectID string
	if s.selectedThreadID != "" {
		for _, th := range s.threads {
			if th.ID == s.selectedThreadID && th.ProjectID != "" {
				targetProjectID = th.ProjectID
				break
			}
		}
	}
	if targetProjectID == "" && s.pendingThread != nil {
		// 已在待创建会话里再次新建：沿用它的项目。
		targetProjectID = s.pendingThread.ProjectID
	}
	if targetProjectID == "" && len(s.projects) > 0 {
		targetProjectID = s.projects[0].ID
	}
	s.mu.Unlock()

	if targetProjectID == "" {
		s.setToast("新建会话失败: 尚未加载到有效项目")
		return
	}

	s.createThread(targetProjectID)
}

// defaultPickFolder 调用系统对话框让用户选取文件夹。
func defaultPickFolder() (string, bool, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Title:             "选择项目目录",
		Directory:         true,
		CreateDirectories: true, // 选择框里带「新建文件夹」，可直接把新建的空目录当项目
	})
	if err != nil {
		return "", false, err
	}
	if len(paths) == 0 {
		return "", false, nil
	}
	return paths[0], true, nil
}

// pickFolderDialog 内部文件夹选择对话框实现变量，默认指向 defaultPickFolder。
var pickFolderDialog func() (string, bool, error) = defaultPickFolder

// promptAddProject 弹出选择文件夹对话框并在后台调用 client.AddProject 添加项目。
func (s *chatState) promptAddProject() {
	go func() {
		var pickFn func() (string, bool)
		s.mu.Lock()
		pickFn = s.pickFolder
		s.mu.Unlock()

		var (
			path string
			ok   bool
			err  error
		)

		if pickFn != nil {
			path, ok = pickFn()
		} else if pickFolderDialog != nil {
			path, ok, err = pickFolderDialog()
		} else {
			path, ok, err = defaultPickFolder()
		}

		if err != nil {
			s.setToast("打开目录选择框失败: " + err.Error())
			return
		}
		if !ok || path == "" {
			return
		}

		s.addProject(path)
	}()
}

// addProject 在后台调用 client.AddProject 添加项目，成功后刷新项目列表、展开新项目，
// 并直接在右侧进入这个新项目的待创建会话，用户选完目录就能接着提问。
func (s *chatState) addProject(path string) {
	go func() {
		ctx := context.Background()
		proj, err := s.client.AddProject(ctx, path)
		if err != nil {
			s.setToast("添加项目失败: " + err.Error())
			return
		}

		s.mu.Lock()
		if s.collapsedProj == nil {
			s.collapsedProj = make(map[string]bool)
		}
		s.collapsedProj[proj.ID] = false // 展开新项目
		s.mu.Unlock()

		// 先进入新项目的待创建会话再刷新列表：刷新时已有 pendingThread，
		// 不会把选中态挪到别的会话上。
		s.createThread(proj.ID)
		s.loadProjects()
	}()
}

// prepareOutgoingMessageLocked 校验发送/入队/转向消息的前置条件并收集就绪附件，调用方需持有 s.mu。
func (s *chatState) prepareOutgoingMessageLocked(text string) (threadID string, attachmentIDs []string, ok bool) {
	tid := s.selectedThreadID
	if s.sending || s.sentDraft != nil || tid == "" || s.composerPrefsSaving[tid] {
		return "", nil, false
	}
	for _, att := range s.attachments {
		if att.Uploading || att.Err != "" || att.ID == "" {
			return "", nil, false
		}
		attachmentIDs = append(attachmentIDs, att.ID)
	}
	if strings.TrimSpace(text) == "" && len(attachmentIDs) == 0 {
		return "", nil, false
	}
	s.sending = true
	s.scrollToEnd = true
	return tid, attachmentIDs, true
}

// markThreadUserMessageLocked 记录用户刚发出的一条消息时间并立即重排会话列表，
// 调用方需持有 s.mu。这样侧边栏在发送当帧就按“用户最后发消息时间”把会话提到前面，
// 不必等后端摘要通过 Watch 回推。
func (s *chatState) markThreadUserMessageLocked(threadID string, at time.Time) {
	if threadID == "" {
		return
	}
	apply := func(th *Thread) {
		if th.ID != threadID {
			return
		}
		th.LastUserMessageAt = at
		// 与后端保持一致：发消息同样刷新 UpdatedAt。
		if th.UpdatedAt.Before(at) {
			th.UpdatedAt = at
		}
	}
	for i := range s.threads {
		apply(&s.threads[i])
	}
	for pid, list := range s.threadsByProj {
		for i := range list {
			apply(&list[i])
		}
		s.threadsByProj[pid] = SortThreads(list)
	}
	s.threads = SortThreads(s.threads)
}

// markThreadRunningOptimisticallyLocked 在发送成功后把会话乐观置为运行中，调用方需持有 s.mu。
// 界面据此在发送当帧就显示“回复中”状态；后端 Watch 增量到达后以权威摘要覆盖，
// 发送失败则用 clearThreadRunningOptimisticallyLocked 回滚。
func (s *chatState) markThreadRunningOptimisticallyLocked(threadID string) {
	if threadID == "" {
		return
	}
	if s.optimisticRunning == nil {
		s.optimisticRunning = make(map[string]bool)
	}
	s.optimisticRunning[threadID] = true
	// 新一轮已经开始，撤掉“继续执行”入口：后端随后推送运行中增量时，本地已处于运行中，
	// 不会再触发 onWatchUpdate 里的清除分支（该分支只认本地由停止态转为运行态的跳变）。
	delete(s.interrupted, threadID)
	for i := range s.threads {
		if s.threads[i].ID == threadID {
			s.threads[i].IsRunning = true
		}
	}
	for _, list := range s.threadsByProj {
		for i := range list {
			if list[i].ID == threadID {
				list[i].IsRunning = true
			}
		}
	}
}

// clearThreadRunningOptimisticallyLocked 清除会话的乐观运行中标记，调用方需持有 s.mu。
// revert 为 true 时把本地 IsRunning 一并回滚为 false：仅用于发送失败、且后端尚未给出权威状态的场景
// （标记存在说明后端还没有推送过该会话的运行状态）。
func (s *chatState) clearThreadRunningOptimisticallyLocked(threadID string, revert bool) {
	if threadID == "" || !s.optimisticRunning[threadID] {
		return
	}
	delete(s.optimisticRunning, threadID)
	if !revert {
		return
	}
	s.markThreadIdleLocked(threadID)
}

// markThreadIdleLocked 把会话本地状态置为空闲并撤销乐观运行中标记，调用方需持有 s.mu。
// 用于后端确认该会话没有在运行的轮次时纠正本地状态。
func (s *chatState) markThreadIdleLocked(threadID string) {
	if threadID == "" {
		return
	}
	delete(s.optimisticRunning, threadID)
	for i := range s.threads {
		if s.threads[i].ID == threadID {
			s.threads[i].IsRunning = false
		}
	}
	for _, list := range s.threadsByProj {
		for i := range list {
			if list[i].ID == threadID {
				list[i].IsRunning = false
			}
		}
	}
}

// finishOutgoingMessageLocked 在消息发送、入队或转向完成后更新本地状态与草稿/附件，调用方需持有 s.mu。
// delegated 为 true 表示消息被交给子线程处理，本会话不会开始新一轮，因此不做乐观运行中标记。
func (s *chatState) finishOutgoingMessageLocked(tid, text string, attachmentIDs []string, delegated bool, err error, failPrefix string) {
	s.sending = false
	if err == nil {
		delete(s.interrupted, tid)
		// 用户消息已送达：本地立刻记下发送时间并重排，侧边栏顺序不等后端回推就更新。
		s.markThreadUserMessageLocked(tid, time.Now())
		// 发送成功：先乐观置为运行中，界面立即显示“回复中”，直到后端报告 turn 结束或出错。
		if !delegated {
			s.markThreadRunningOptimisticallyLocked(tid)
		}
		// 草稿绑定只能在 UI 帧内更新，失败时保留原文及附件供重试。
		if s.selectedThreadID == tid {
			s.sentDraft = &text
		}
		// 发送成功后清掉该会话草稿，重启不再回填已发送的内容。
		s.clearDraftLocked(tid)
		for _, id := range attachmentIDs {
			for i, att := range s.attachments {
				if att.ID == id {
					s.attachments = append(s.attachments[:i], s.attachments[i+1:]...)
					break
				}
			}
		}
	} else {
		// 发送失败：回滚乐观运行中标记，避免界面一直停在“回复中”。
		s.clearThreadRunningOptimisticallyLocked(tid, true)
		// 首条消息没发出去：撤掉透视标记，布局退回居中的 Hero（反向翻转为瞬时，不播动画）。
		s.disarmComposerDockPromote(tid)
		s.toastMessage = failPrefix + ": " + err.Error()
	}
}

// sendMessage 在后台 goroutine 中向当前会话发送文本与已上传附件。
func (s *chatState) sendMessage(text string) {
	s.mu.Lock()
	sendFromPending := s.isPendingThreadLocked(s.selectedThreadID)
	s.mu.Unlock()
	if sendFromPending {
		// 待创建会话还没有后端会话：先建会话，成功后接着把这条消息发出去。
		s.createPendingThread(func(string) { s.sendMessage(text) })
		return
	}

	s.mu.Lock()
	// 粘贴卡片接在正文后面一起发出；text 仍是输入框原文，供发送后按 sentDraft 清空输入框。
	outgoing, snippetIDs := s.outgoingTextLocked(text)
	tid, attachmentIDs, ok := s.prepareOutgoingMessageLocked(outgoing)
	mentions := append([]ThreadMention(nil), s.draftThreadMentions...)
	if ok {
		// 引用胶囊只消费一次；发送被拒绝时保留，供下次重试使用。
		s.draftThreadMentions = nil
		// 发出前先记下“刚发出”的窗口：发送请求可能比渲染帧还快（见 chat_anchor.go）。
		s.armChatAnchor(tid)
		// 首条消息刚发出：记下这次 Hero→Docked，让输入框从中间滑到底部。
		// 只在发送当帧会话为空时打标：切到别的有消息会话、恢复会话都不会走到这里。
		if composerThreadEmpty(len(s.messages), len(s.resolvedPlans[tid]), len(s.answeredQuestions[tid])) {
			s.armComposerDockPromote(tid)
		}
	}
	s.mu.Unlock()
	if !ok {
		return
	}

	go func() {
		ctx := context.Background()
		sendText := outgoing
		if len(mentions) > 0 {
			// 被引用会话的 <referenced_chat> 资料块连同前言与结束标记一并置于用户当前输入之前。
			sendText = expandDraftThreadMentions(outgoing, mentions, s.threadTitleByID, func(id string) ([]Message, error) {
				return s.client.History(ctx, id)
			})
		}
		var err error
		delegated := false
		if len(attachmentIDs) > 0 {
			delegated, err = s.client.SendWithAttachments(ctx, tid, sendText, attachmentIDs)
		} else {
			delegated, err = s.client.Send(ctx, tid, sendText)
		}
		s.mu.Lock()
		s.finishOutgoingMessageLocked(tid, text, attachmentIDs, delegated, err, "发送消息失败")
		if err == nil {
			s.dropSnippetsLocked(snippetIDs)
		}
		s.mu.Unlock()
		if err == nil {
			// 发送成功后草稿已清空，后台补一次落盘（内部自行加锁）。
			s.persistDraftsAsync()
		}
		s.invalidate()
	}()
}

// sendTextOnly 在后台 goroutine 中向当前会话发送纯文本，不读取也不清空输入框草稿与附件。
// 决策卡的回答走这条路径：它不应消耗用户正在编辑的草稿，也不应带上待发送的附件。
func (s *chatState) sendTextOnly(text string) {
	s.mu.Lock()
	if s.isPendingThreadLocked(s.selectedThreadID) {
		s.mu.Unlock()
		// 待创建会话还没有后端会话：先建会话，成功后接着把这条消息发出去。
		s.createPendingThread(func(string) { s.sendTextOnly(text) })
		return
	}
	tid := s.selectedThreadID
	if s.sending || s.sentDraft != nil || tid == "" || s.composerPrefsSaving[tid] {
		s.mu.Unlock()
		return
	}
	if strings.TrimSpace(text) == "" {
		s.mu.Unlock()
		return
	}
	s.sending = true
	s.scrollToEnd = true
	s.armChatAnchor(tid)
	s.mu.Unlock()

	go func() {
		ctx := context.Background()
		delegated, err := s.client.Send(ctx, tid, text)
		s.mu.Lock()
		s.sending = false
		if err == nil {
			delete(s.interrupted, tid)
			// 消息已送达：本地立即记下发送时间并重排侧边栏。
			s.markThreadUserMessageLocked(tid, time.Now())
			if !delegated {
				s.markThreadRunningOptimisticallyLocked(tid)
			}
		} else {
			// 发送失败：回滚乐观运行中标记，避免界面一直停在“回复中”。
			s.clearThreadRunningOptimisticallyLocked(tid, true)
			s.disarmComposerDockPromote(tid)
			s.toastMessage = "发送消息失败: " + err.Error()
		}
		s.mu.Unlock()
		s.invalidate()
	}()
}

// threadTitleByID 返回会话标题，会话不存在时返回空字符串；expandThreadMentions 会回退到引用文本本身。
func (s *chatState) threadTitleByID(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.threads {
		if s.threads[i].ID == id {
			return s.threads[i].Title
		}
	}
	return ""
}

// resolvedPlan 记录已完成审批（批准或驳回）的计划历史态。
type resolvedPlan struct {
	ID        string    // 审批请求 ID
	Plan      string    // 计划正文
	Feedback  string    // 驳回意见原文
	Approved  bool      // true 表示 Approved，false 表示 Dismissed
	Time      time.Time // 裁决时间
	TurnIndex int       // 所属 turn 序号
	TurnID    string    // 所属 turn ID
}

// recordResolvedPlan 在内存中记录已批准或驳回的计划历史态；需在持锁状态下调用。
func (s *chatState) recordResolvedPlan(a Approval, approved bool, feedback ...string) {
	comment := strings.Join(feedback, "\n")
	threadID := a.ThreadID
	if threadID == "" {
		threadID = s.selectedThreadID
	}
	if threadID == "" {
		return
	}

	turnIndex := -1
	if a.TurnID != "" {
		for _, m := range s.messages {
			if m.TurnID == a.TurnID {
				turnIndex = m.TurnIndex
				break
			}
		}
	}
	if turnIndex == -1 && len(s.messages) > 0 {
		turnIndex = s.messages[len(s.messages)-1].TurnIndex
	}

	if s.resolvedPlans == nil {
		s.resolvedPlans = make(map[string][]resolvedPlan)
	}
	for i, rp := range s.resolvedPlans[threadID] {
		// 只按请求去重，相同正文的新版计划仍须保留独立裁决。
		sameID := rp.ID != "" && rp.ID == a.RequestID
		if !sameID {
			continue
		}
		s.resolvedPlans[threadID][i].Approved = approved
		s.resolvedPlans[threadID][i].Feedback = comment
		s.resolvedPlans[threadID][i].Time = time.Now()
		if s.resolvedPlans[threadID][i].ID == "" {
			s.resolvedPlans[threadID][i].ID = a.RequestID
		}
		return
	}
	newID := a.RequestID
	if newID == "" {
		newID = fmt.Sprintf("plan-%d", time.Now().UnixNano())
	}
	// 新条目默认折叠，只露头部一行；用户手动展开后不再改写。
	if s.collapsedPlans == nil {
		s.collapsedPlans = make(map[string]bool)
	}
	s.collapsedPlans[newID] = true
	s.resolvedPlans[threadID] = append(s.resolvedPlans[threadID], resolvedPlan{
		ID:        newID,
		Plan:      a.Detail,
		Approved:  approved,
		Feedback:  comment,
		Time:      time.Now(),
		TurnIndex: turnIndex,
		TurnID:    a.TurnID,
	})
}

// recordAnsweredQuestion 在内存中记录一次已答复的提问历史态；需在持锁状态下调用。
// 只记提问类审批：命令、文件改动、计划等其它审批不产生问答记录。
// 与 resolvedPlan 一样记录只存在内存里，重启后丢失。
func (s *chatState) recordAnsweredQuestion(a Approval, answers map[string][]string, skip bool) {
	if a.Kind != ApprovalKindQuestion {
		return
	}
	threadID := a.ThreadID
	if threadID == "" {
		threadID = s.selectedThreadID
	}
	if threadID == "" {
		return
	}

	turnIndex := -1
	if a.TurnID != "" {
		for _, m := range s.messages {
			if m.TurnID == a.TurnID {
				turnIndex = m.TurnIndex
				break
			}
		}
	}
	if turnIndex == -1 && len(s.messages) > 0 {
		turnIndex = s.messages[len(s.messages)-1].TurnIndex
	}

	if s.answeredQuestions == nil {
		s.answeredQuestions = make(map[string][]answeredQuestion)
	}
	record := newAnsweredQuestion(a, answers, skip)
	record.TurnIndex = turnIndex
	if record.ID == "" {
		record.ID = fmt.Sprintf("question-%d", time.Now().UnixNano())
	}
	for i, old := range s.answeredQuestions[threadID] {
		// 同一 RequestID 重复答复（其他端先处理、本地补记）只覆盖原条目，不追加第二张卡。
		if old.ID == record.ID {
			s.answeredQuestions[threadID][i] = record
			return
		}
	}
	s.answeredQuestions[threadID] = append(s.answeredQuestions[threadID], record)
}

// isApprovalAlreadyHandledErr 判断错误是否属于审批已在其他端完成或不存在的误报情况。
func isApprovalAlreadyHandledErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "already handled") || strings.Contains(msg, "request not found or already handled")
}

// removeApprovalLocked 从本地审批列表中移除指定请求并清理对应草稿；调用方需持有 s.mu。
func (s *chatState) removeApprovalLocked(requestID string) {
	filtered := make([]Approval, 0, len(s.approvals))
	for _, item := range s.approvals {
		if item.RequestID != requestID {
			filtered = append(filtered, item)
		}
	}
	s.approvals = filtered
	s.pruneApprovalDraftsLocked()
}

// respondApproval 在后台 goroutine 中响应审批请求。
// respondingApproval 计的是在途响应数：批量“全部允许/全部拒绝”会并发发出多个请求，
// 必须等全部返回才解锁按钮，否则首个响应回来就能重复提交同一批审批。
func (s *chatState) respondApproval(a Approval, optionID string) {
	s.mu.Lock()
	s.respondingApproval++
	s.mu.Unlock()
	s.invalidate()

	go func() {
		ctx := context.Background()
		err := s.client.Respond(ctx, a, optionID)

		s.mu.Lock()
		s.respondingApproval--
		if err != nil {
			if isApprovalAlreadyHandledErr(err) {
				s.removeApprovalLocked(a.RequestID)
				if a.Kind == ApprovalKindPlan {
					s.recordResolvedPlan(a, optionID == "allow" || optionID == "approve")
				}
			} else {
				s.toastMessage = "审批操作失败: " + err.Error()
			}
		} else {
			s.removeApprovalLocked(a.RequestID)
			if a.Kind == ApprovalKindPlan {
				s.recordResolvedPlan(a, optionID == "allow" || optionID == "approve")
			}
		}
		s.mu.Unlock()
		s.invalidate()
	}()
}

// respondAnswers 在后台 goroutine 中携带答案响应提问或计划驳回。
func (s *chatState) respondAnswers(a Approval, optionID string, answers map[string][]string, skip bool) {
	s.mu.Lock()
	if a.Kind == ApprovalKindPlan {
		pending := false
		for _, current := range s.approvals {
			if current.RequestID == a.RequestID {
				pending = true
				break
			}
		}
		if !pending || s.respondingApproval > 0 {
			s.mu.Unlock()
			return
		}
		// 只清正文，附件、粘贴卡片与会话引用仍留在输入框。
		s.draft = ""
	}
	s.respondingApproval++
	s.mu.Unlock()
	s.invalidate()

	go func() {
		ctx := context.Background()
		err := s.client.RespondAnswers(ctx, a, optionID, answers, skip)

		s.mu.Lock()
		s.respondingApproval--
		if err != nil {
			if isApprovalAlreadyHandledErr(err) {
				s.removeApprovalLocked(a.RequestID)
				if a.Kind == ApprovalKindPlan {
					s.recordResolvedPlan(a, false, answers["feedback"]...)
				}
				s.recordAnsweredQuestion(a, answers, skip)
			} else {
				s.toastMessage = "审批操作失败: " + err.Error()
			}
		} else {
			s.removeApprovalLocked(a.RequestID)
			if a.Kind == ApprovalKindPlan {
				s.recordResolvedPlan(a, false, answers["feedback"]...)
			}
			s.recordAnsweredQuestion(a, answers, skip)
		}
		s.mu.Unlock()
		s.invalidate()
	}()
}

// questionDraft 记录提问卡片的草稿：每题选中的选项下标（升序）、填空文本与当前题号。
type questionDraft struct {
	selected [][]int
	others   []string
	current  int
}

// questionDraftFor 返回指定提问请求的草稿，首次访问时按 recommended 预选初始化；调用方需持有 s.mu。
func (s *chatState) questionDraftFor(a Approval) *questionDraft {
	if s.questionDrafts == nil {
		s.questionDrafts = make(map[string]*questionDraft)
	}
	d := s.questionDrafts[a.RequestID]
	if d == nil || len(d.selected) != len(a.Questions) {
		d = newQuestionDraft(a.Questions)
		s.questionDrafts[a.RequestID] = d
	}
	return d
}

// pruneApprovalDraftsLocked 清掉审批列表中已不存在的请求对应的卡片草稿；调用方需持有 s.mu。
func (s *chatState) pruneApprovalDraftsLocked() {
	alive := make(map[string]bool, len(s.approvals))
	for _, a := range s.approvals {
		alive[a.RequestID] = true
	}
	for id := range s.questionDrafts {
		if !alive[id] {
			delete(s.questionDrafts, id)
		}
	}
}

func (s *chatState) toggleReasoning(msgID string) {
	s.mu.Lock()
	s.expandedReasoning[msgID] = !s.expandedReasoning[msgID]
	s.mu.Unlock()
	s.holdChatPlace()
	s.invalidate()
}

// holdChatPlace 让下一帧列表保持原位、不再跟随末尾：展开或收起最后一条消息里的折叠时，
// 被点的那一行留在指针下面（FollowEnd 不变，列表回到末尾后照旧跟随）。
func (s *chatState) holdChatPlace() {
	if s == nil {
		return
	}
	s.chatListState.HoldPlace()
}

// markAssistantRevealed 记录助手正文这一帧是否已完全显示；streaming 为真时记下这条消息
// 曾在流式状态下渲染过。标记从 false 变成 true 时请求重绘，让时间与操作栏补上来。
func (s *chatState) markAssistantRevealed(msgID string, streaming, revealed bool) {
	if s == nil || msgID == "" {
		return
	}
	s.mu.Lock()
	if streaming {
		if len(s.streamedRevealMsgs) >= messageEnterCap {
			s.streamedRevealMsgs = make(map[string]struct{})
		}
		s.streamedRevealMsgs[msgID] = struct{}{}
	}
	previous := s.revealedMsgs[msgID]
	s.revealedMsgs[msgID] = revealed
	s.mu.Unlock()
	if revealed && !previous {
		s.invalidate()
	}
}

// assistantRevealed 返回助手正文是否已完全显示：没在本会话里流式输出过的消息一律视为
// 已显示，打开历史会话时时间和标题照旧第一帧就出现。
func (s *chatState) assistantRevealed(msgID string) bool {
	if s == nil || msgID == "" {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, streamed := s.streamedRevealMsgs[msgID]; !streamed {
		return true
	}
	return s.revealedMsgs[msgID]
}

func (s *chatState) toggleTool(msgID string) {
	s.mu.Lock()
	s.expandedTool[msgID] = !s.expandedTool[msgID]
	s.expandedRev++
	s.mu.Unlock()
	s.invalidate()
}

func (s *chatState) interruptCurrent() {
	s.stopCurrent(false)
}

// stopOrRecall 优先撤回尚未产生回复的轮次，失败时中断同一个会话。
func (s *chatState) stopOrRecall() {
	s.stopCurrent(true)
}

func (s *chatState) stopCurrent(tryRecall bool) {
	s.mu.Lock()
	if s.stopping || s.selectedThreadID == "" {
		s.mu.Unlock()
		return
	}
	s.stopping = true
	s.stoppingSince = time.Now()
	tid := s.selectedThreadID
	swarmBusy := false
	for _, th := range s.threads {
		if th.ID == tid {
			swarmBusy = th.SwarmBusy
			break
		}
	}
	seq := s.watchSeq
	recall := tryRecall && canRecallTurn(s.messages)
	s.scheduleStoppingTimeoutLocked(s.stoppingSince)
	s.mu.Unlock()
	s.invalidate()

	go func() {
		if swarmBusy {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := s.client.StopAllSwarmBees(ctx, tid)
			cancel()
			if err != nil {
				s.mu.Lock()
				s.toastMessage = "停止子代理失败: " + err.Error()
				s.mu.Unlock()
			} else {
				s.refreshSwarm()
			}
		}
		ctx := context.Background()
		var prompt string
		var err error
		retracted := false
		alreadyStopped := false
		if recall {
			prompt, err = s.client.RecallTurn(ctx, tid)
			retracted = err == nil
		}
		if !retracted {
			alreadyStopped, err = s.client.Interrupt(ctx, tid)
		}
		s.mu.Lock()
		if s.selectedThreadID == tid && s.watchSeq == seq {
			if err != nil || retracted || alreadyStopped {
				s.stopping = false
				s.stoppingSince = time.Time{}
			}
			if err != nil {
				s.toastMessage = "停止失败: " + err.Error()
			} else if retracted {
				delete(s.interrupted, tid)
				s.pendingDraftFill = &prompt
				s.pendingDraftFillIfEmpty = true
			} else if alreadyStopped {
				// 后端确认没有在运行的轮次：本地撤销运行中，避免按钮继续停在“停止”。
				s.markThreadIdleLocked(tid)
			}
		}
		if err == nil && !retracted && !alreadyStopped {
			if s.interrupted == nil {
				s.interrupted = make(map[string]bool)
			}
			s.interrupted[tid] = true
		}
		s.mu.Unlock()
		s.invalidate()
	}()
}

// stoppingTimeout 是停止态的兜底时长：后端既不回调也不推送运行状态时，到期自行解除停止态。
const stoppingTimeout = 8 * time.Second

// killCommandTimeout 是「终止此命令」请求的超时：后端需要找到并杀死子进程，给宽一点。
const killCommandTimeout = 10 * time.Second

// killRunningCommand 只终止这条工具消息对应的命令子进程，不中断本轮；
// 请求进行中按钮禁用并显示「正在终止…」，结束后用 toast 汇报结果。
func (s *chatState) killRunningCommand(messageID string) {
	if messageID == "" {
		return
	}
	s.mu.Lock()
	if s.killingTools[messageID] || s.client == nil {
		s.mu.Unlock()
		return
	}
	tid := s.selectedThreadID
	if tid == "" {
		s.mu.Unlock()
		return
	}
	if s.killingTools == nil {
		s.killingTools = make(map[string]bool)
	}
	s.killingTools[messageID] = true
	seq := s.watchSeq
	s.mu.Unlock()
	s.invalidate()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), killCommandTimeout)
		defer cancel()
		killed, err := s.client.KillRunningCommand(ctx, tid, messageID)

		var toast string
		s.mu.Lock()
		delete(s.killingTools, messageID)
		// 期间切了会话（或重开了一轮监听）就不再弹提示，避免张冠李戴。
		if s.selectedThreadID == tid && s.watchSeq == seq {
			switch {
			case err != nil:
				toast = "终止命令失败: " + err.Error()
			case killed > 0:
				toast = fmt.Sprintf("已终止 %d 个进程，AI 会收到命令失败的结果", killed)
			default:
				toast = "没有找到正在运行的命令进程"
			}
		}
		s.mu.Unlock()
		if toast != "" {
			s.setToast(toast)
		} else {
			s.invalidate()
		}
	}()
}

// scheduleStoppingTimeoutLocked 在进入停止态后安排一次兜底唤醒，调用方需持有 s.mu。
// 到期仍是同一次停止请求就清除 stopping 并重绘，避免按钮一直卡在“停止”与转圈状态。
func (s *chatState) scheduleStoppingTimeoutLocked(started time.Time) {
	time.AfterFunc(stoppingTimeout, func() {
		s.mu.Lock()
		expired := !s.closed && s.stopping && s.stoppingSince.Equal(started)
		if expired {
			s.stopping = false
			s.stoppingSince = time.Time{}
		}
		s.mu.Unlock()
		if expired {
			s.invalidate()
		}
	})
}

func resolveDataDirFallback() string {
	if env := os.Getenv("MCODE_DATA"); env != "" {
		return env
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".mcode")
	}
	return ""
}

// invalidateConfigCache 清空读配置的短期缓存：配置文件被别处改过（如 Web 端 settings.set）后，
// 下一次读取立刻重新读盘，而不是继续用最多 2 秒前的旧值。
func (s *chatState) invalidateConfigCache() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.queueWhileRunningCachedAt = time.Time{}
	s.insertIntoRunningTurnCachedAt = time.Time{}
	s.mu.Unlock()
}

// queueWhileRunningEnabled 读取全局设置中的运行中排队开关。
// 当关联的 SettingsState 存在时直接读取内存配置；
// 若 getChatSettings(s) 为 nil，回退读取持久化配置 config.Load(dataDir)，在 chatState 上缓存 2 秒。
func (s *chatState) queueWhileRunningEnabled() bool {
	if s == nil {
		return false
	}
	if st := getChatSettings(s); st != nil {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.cfg.Session.QueueWhileRunning
	}

	s.mu.Lock()
	if !s.queueWhileRunningCachedAt.IsZero() && time.Since(s.queueWhileRunningCachedAt) < 2*time.Second {
		cached := s.queueWhileRunningCache
		s.mu.Unlock()
		return cached
	}
	s.queueWhileRunningCachedAt = time.Now()
	s.mu.Unlock()

	dataDir := resolveDataDirFallback()
	if dataDir == "" {
		s.mu.Lock()
		s.queueWhileRunningCache = true
		s.queueWhileRunningCachedAt = time.Now()
		s.mu.Unlock()
		return true
	}

	cfg, err := config.Load(dataDir)
	enabled := true
	if err == nil {
		enabled = cfg.Session.QueueWhileRunning
	}

	s.mu.Lock()
	s.queueWhileRunningCache = enabled
	s.queueWhileRunningCachedAt = time.Now()
	s.mu.Unlock()
	return enabled
}

// insertIntoRunningTurnEnabled 读取全局设置中的「运行中的消息插入当前回合」开关。
// 与 queueWhileRunningEnabled 相同的读取策略：优先读内存中的设置面板配置，
// 缺失时回退读取持久化配置 config.Load(dataDir)，在 chatState 上缓存 2 秒。
func (s *chatState) insertIntoRunningTurnEnabled() bool {
	if s == nil {
		return false
	}
	if st := getChatSettings(s); st != nil {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.cfg.Session.InsertIntoRunningTurn
	}

	s.mu.Lock()
	if !s.insertIntoRunningTurnCachedAt.IsZero() && time.Since(s.insertIntoRunningTurnCachedAt) < 2*time.Second {
		cached := s.insertIntoRunningTurnCache
		s.mu.Unlock()
		return cached
	}
	s.insertIntoRunningTurnCachedAt = time.Now()
	s.mu.Unlock()

	dataDir := resolveDataDirFallback()
	if dataDir == "" {
		s.mu.Lock()
		s.insertIntoRunningTurnCache = true
		s.insertIntoRunningTurnCachedAt = time.Now()
		s.mu.Unlock()
		return true
	}

	cfg, err := config.Load(dataDir)
	enabled := true
	if err == nil {
		enabled = cfg.Session.InsertIntoRunningTurn
	}

	s.mu.Lock()
	s.insertIntoRunningTurnCache = enabled
	s.insertIntoRunningTurnCachedAt = time.Now()
	s.mu.Unlock()
	return enabled
}

// steerMessage 在会话运行中发送转向纠偏消息。
func (s *chatState) steerMessage(text string) {
	s.mu.Lock()
	sender, ok := s.client.(steerSender)
	if !ok {
		s.mu.Unlock()
		s.setToast("当前客户端不支持转向消息")
		return
	}
	outgoing, snippetIDs := s.outgoingTextLocked(text)
	tid, attachmentIDs, ok := s.prepareOutgoingMessageLocked(outgoing)
	s.mu.Unlock()
	if !ok {
		return
	}

	go func() {
		ctx := context.Background()
		err := sender.InterruptAndSend(ctx, tid, outgoing, attachmentIDs)
		s.mu.Lock()
		s.finishOutgoingMessageLocked(tid, text, attachmentIDs, false, err, "转向消息失败")
		if err == nil {
			s.dropSnippetsLocked(snippetIDs)
		}
		s.mu.Unlock()
		if err == nil {
			// 发送成功后草稿已清空，后台补一次落盘（内部自行加锁）。
			s.persistDraftsAsync()
		}
		s.invalidate()
	}()
}

const continuePrompt = "Pick up from the point where you stopped and see the task through."

// enqueueMessage 在后台 goroutine 中向当前会话排队发送文本与已上传附件。
// 前置检查与 sendMessage 相同；调用 client.Enqueue 成功后清空草稿和附件，复用 sentDraft 机制。
func (s *chatState) enqueueMessage(text string) {
	s.mu.Lock()
	outgoing, snippetIDs := s.outgoingTextLocked(text)
	tid, attachmentIDs, ok := s.prepareOutgoingMessageLocked(outgoing)
	s.mu.Unlock()
	if !ok {
		return
	}

	go func() {
		ctx := context.Background()
		err := s.client.Enqueue(ctx, tid, outgoing, attachmentIDs)
		s.mu.Lock()
		s.finishOutgoingMessageLocked(tid, text, attachmentIDs, false, err, "加入队列失败")
		if err == nil {
			s.dropSnippetsLocked(snippetIDs)
		}
		s.mu.Unlock()
		if err == nil {
			// 入队成功后草稿已清空，后台补一次落盘（内部自行加锁）。
			s.persistDraftsAsync()
		}
		s.invalidate()
	}()
}

// injectMessage 在会话运行中把消息插入当前回合：
// 走普通发送通道，由后端在服务商支持中途注入（harness.Steerer）时交给正在执行的轮次，
// 服务商不支持或消息带附件时退回排队，因此不会中断当前轮次。
// 前置检查与 sendMessage 相同；发送成功后清空草稿和附件，复用 sentDraft 机制。
func (s *chatState) injectMessage(text string) {
	s.mu.Lock()
	outgoing, snippetIDs := s.outgoingTextLocked(text)
	tid, attachmentIDs, ok := s.prepareOutgoingMessageLocked(outgoing)
	s.mu.Unlock()
	if !ok {
		return
	}

	go func() {
		ctx := context.Background()
		var err error
		delegated := false
		if len(attachmentIDs) > 0 {
			delegated, err = s.client.SendWithAttachments(ctx, tid, outgoing, attachmentIDs)
		} else {
			delegated, err = s.client.Send(ctx, tid, outgoing)
		}
		s.mu.Lock()
		s.finishOutgoingMessageLocked(tid, text, attachmentIDs, delegated, err, "插入当前回合失败")
		if err == nil {
			s.dropSnippetsLocked(snippetIDs)
		}
		s.mu.Unlock()
		if err == nil {
			// 发送成功后草稿已清空，后台补一次落盘（内部自行加锁）。
			s.persistDraftsAsync()
		}
		s.invalidate()
	}()
}

// beginEditQueued 开始编辑指定排队跟进项，暂存当前草稿并回填排队项文本到输入框。
func (s *chatState) beginEditQueued(followUpID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tid := s.selectedThreadID
	if tid == "" || followUpID == "" || s.sending {
		return
	}
	for _, att := range s.attachments {
		if att.Uploading {
			s.toastMessage = "请等待附件上传完成后再编辑排队消息"
			s.invalidate()
			return
		}
	}
	var target *QueuedFollowUp
	for i := range s.queue[tid] {
		if s.queue[tid][i].ID == followUpID {
			target = &s.queue[tid][i]
			break
		}
	}
	if target == nil {
		return
	}
	editor, supportsAttachments := s.client.(queueAttachmentEditor)
	if target.AttachmentCount > 0 && !supportsAttachments {
		s.toastMessage = "当前客户端不支持编辑带附件的排队消息"
		s.invalidate()
		return
	}
	if s.editingFollowUpID == "" {
		s.editStashDraft = s.draft
		s.editStashAttachments = append([]composerAttachment(nil), s.attachments...)
		s.editStashSnippets = append([]composerSnippet(nil), s.snippets...)
	}
	s.attachments = nil
	s.snippets = nil
	s.snippetPreviewID = 0
	s.editGeneration++
	s.editingFollowUpID = followUpID
	s.editingFollowUpThread = tid
	text := target.Text
	s.pendingDraftFill = &text
	s.pendingDraftFillIfEmpty = false
	s.invalidate()
	if target.AttachmentCount > 0 {
		generation := s.editGeneration
		s.sending = true
		go func() {
			attachments, err := editor.QueuedAttachments(context.Background(), tid, followUpID)
			s.mu.Lock()
			s.sending = false
			if s.editGeneration == generation && s.selectedThreadID == tid {
				if err != nil {
					stash := s.editStashDraft
					s.clearEditingStateLocked()
					s.pendingDraftFill = &stash
					s.toastMessage = "加载排队附件失败: " + err.Error()
				} else {
					for _, att := range attachments {
						s.attachments = append(s.attachments, composerAttachment{
							LocalID: nextLocalAttachmentID(), ID: att.ID, Name: att.Name,
							MimeType: att.MimeType, Size: att.ByteCount,
						})
					}
				}
			}
			s.mu.Unlock()
			s.invalidate()
		}()
	}
}

// editingQueuedID 返回当前会话正在编辑的排队消息 ID；若未在当前会话编辑则返回空字符串。
func (s *chatState) editingQueuedID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.editingFollowUpThread == s.selectedThreadID {
		return s.editingFollowUpID
	}
	return ""
}

// cancelQueuedEdit 取消排队消息编辑，恢复暂存的草稿并清空编辑态。
func (s *chatState) cancelQueuedEdit() {
	s.mu.Lock()
	defer s.mu.Unlock()
	stash := s.editStashDraft
	s.clearEditingStateLocked()
	s.pendingDraftFill = &stash
	s.pendingDraftFillIfEmpty = false
	s.invalidate()
}

// saveQueuedEdit 原地更新排队消息；不支持更新时保留原项，避免降级删除造成数据丢失。
func (s *chatState) saveQueuedEdit(text string) {
	s.mu.Lock()
	// 编辑排队项时粘贴的长文本卡片并入排队项；保存成功后卡片随编辑态一起换回用户草稿的卡片。
	text, _ = s.outgoingTextLocked(text)
	tid := s.selectedThreadID
	followUpID := s.editingFollowUpID
	stash := s.editStashDraft
	if tid == "" || followUpID == "" || s.editingFollowUpThread != tid || s.sending {
		s.mu.Unlock()
		return
	}
	updater, ok := s.client.(queueUpdater)
	if !ok {
		s.toastMessage = "当前客户端不支持编辑排队消息"
		s.mu.Unlock()
		s.invalidate()
		return
	}
	var attachmentIDs []string
	for _, att := range s.attachments {
		if att.Uploading || att.Err != "" || att.ID == "" {
			s.mu.Unlock()
			return
		}
		attachmentIDs = append(attachmentIDs, att.ID)
	}
	editor, supportsAttachments := s.client.(queueAttachmentEditor)
	if len(attachmentIDs) > 0 && !supportsAttachments {
		s.toastMessage = "当前客户端不支持保存排队附件"
		s.mu.Unlock()
		s.invalidate()
		return
	}
	generation := s.editGeneration
	s.sending = true
	s.mu.Unlock()

	go func() {
		ctx := context.Background()
		var err error
		if supportsAttachments {
			err = editor.UpdateQueuedWithAttachments(ctx, tid, followUpID, text, attachmentIDs)
		} else {
			err = updater.UpdateQueued(ctx, tid, followUpID, text)
		}

		s.mu.Lock()
		s.sending = false
		if s.editGeneration != generation || s.selectedThreadID != tid {
			s.mu.Unlock()
			s.invalidate()
			return
		}
		if err == nil {
			s.clearEditingStateLocked()
			p := stash
			s.pendingDraftFill = &p
			s.pendingDraftFillIfEmpty = false
		} else {
			s.toastMessage = "保存排队消息失败: " + err.Error()
		}
		s.mu.Unlock()
		s.invalidate()
	}()
}

// clearEditingStateLocked 清空排队编辑状态，调用方需持有 s.mu。
func (s *chatState) clearEditingStateLocked() {
	s.editGeneration++
	if s.editingFollowUpID != "" {
		s.attachments = s.editStashAttachments
		s.snippets = s.editStashSnippets
		s.snippetPreviewID = 0
	}
	s.editStashAttachments = nil
	s.editStashSnippets = nil
	s.editingFollowUpID = ""
	s.editingFollowUpThread = ""
	s.editStashDraft = ""
}

// consumePendingDraftFillLocked 读取并清空待回填的草稿文本，调用方需持有 s.mu。
func (s *chatState) consumePendingDraftFillLocked() (string, bool) {
	if s.pendingDraftFill == nil {
		return "", false
	}
	text := *s.pendingDraftFill
	s.pendingDraftFill = nil
	onlyEmpty := s.pendingDraftFillIfEmpty
	s.pendingDraftFillIfEmpty = false
	if onlyEmpty && s.draft != "" {
		s.toastMessage = "已撤回，原输入框内容保留"
		return "", false
	}
	return text, true
}

// clearQueue 移除当前会话的所有排队消息。
func (s *chatState) clearQueue() {
	s.mu.Lock()
	tid := s.selectedThreadID
	if tid == "" {
		s.mu.Unlock()
		return
	}
	var ids []string
	skippedSystem := 0
	for _, item := range s.queue[tid] {
		// 系统消息后端不允许撤回，清空时跳过并提示。
		if item.System {
			skippedSystem++
			continue
		}
		ids = append(ids, item.ID)
	}
	s.mu.Unlock()

	if len(ids) == 0 {
		if skippedSystem > 0 {
			s.setToast("系统消息不可撤回，已保留")
		}
		return
	}

	go func() {
		ctx := context.Background()
		for _, id := range ids {
			if err := s.client.RemoveQueued(ctx, tid, id); err != nil {
				s.setToast("清空排队消息失败: " + err.Error())
				break
			}
		}
		s.invalidate()
	}()
}

// recallAllQueued 把当前会话队列里的普通消息按顺序退回输入框。
// 带附件的条目先取回附件再从队列移除；任一步失败就中止，草稿只累积已成功退回的条目。
func (s *chatState) recallAllQueued() {
	// editingQueuedID 会自己加锁，必须在持有 s.mu 之前调用。
	if s.editingQueuedID() != "" {
		s.setToast("正在编辑排队消息，请先取消编辑再全部退回")
		return
	}

	s.mu.Lock()
	tid := s.selectedThreadID
	items := append([]QueuedFollowUp(nil), s.queue[tid]...)
	currentAttachments := len(s.attachments)
	editor, supportsAttachments := s.client.(queueAttachmentEditor)
	s.mu.Unlock()
	if tid == "" {
		return
	}
	limit := s.attachmentLimit()
	if reason := queuedRecallBlockedReason(items, currentAttachments, supportsAttachments, limit); reason != "" {
		s.setToast(reason)
		return
	}

	recallable := make([]QueuedFollowUp, 0, len(items))
	skippedSystem := 0
	for _, item := range items {
		if item.System {
			skippedSystem++
			continue
		}
		recallable = append(recallable, item)
	}

	go func() {
		if recallAllQueuedDoneHook != nil {
			defer recallAllQueuedDoneHook()
		}
		ctx := context.Background()
		texts := make([]string, 0, len(recallable))
		recalledAttachments := make([]composerAttachment, 0)
		failed := false
		for _, item := range recallable {
			var itemAttachments []composerAttachment
			if item.AttachmentCount > 0 {
				attachments, err := editor.QueuedAttachments(ctx, tid, item.ID)
				if err != nil {
					s.setToast("退回排队消息失败: " + err.Error())
					failed = true
					break
				}
				for _, att := range attachments {
					itemAttachments = append(itemAttachments, composerAttachment{
						LocalID:  nextLocalAttachmentID(),
						ID:       att.ID,
						Name:     att.Name,
						MimeType: att.MimeType,
						Size:     att.ByteCount,
					})
				}
			}
			// 先确认附件放得下，再从队列移除，避免删了消息却无法呈现附件。
			s.mu.Lock()
			fits := len(s.attachments)+len(recalledAttachments)+len(itemAttachments) <= limit
			s.mu.Unlock()
			if !fits {
				s.setToast(fmt.Sprintf("退回后附件将超过 %d 个上限", limit))
				failed = true
				break
			}
			if err := s.client.RemoveQueued(ctx, tid, item.ID); err != nil {
				s.setToast("退回排队消息失败: " + err.Error())
				failed = true
				break
			}
			texts = append(texts, item.Text)
			recalledAttachments = append(recalledAttachments, itemAttachments...)
		}

		s.mu.Lock()
		sameThread := s.selectedThreadID == tid
		if sameThread {
			if len(texts) > 0 {
				s.draft = mergeQueuedDraftText(s.draft, texts)
			}
			s.attachments = append(s.attachments, recalledAttachments...)
		}
		draft := s.draft
		s.mu.Unlock()
		if sameThread && len(texts) > 0 {
			s.syncDraftTags()
			s.noteComposerDraft(tid, draft)
		}
		s.invalidate()
		if failed {
			return
		}
		message := fmt.Sprintf("已退回 %d 条排队消息", len(texts))
		if skippedSystem > 0 {
			message += "，系统消息已保留"
		}
		s.setToast(message)
	}()
}

// continueLastTurn 仅在当前会话显示继续入口、未运行、草稿为空时生效。
// 判定与胶囊共用 shouldShowContinuePill：本地中断标记、后端异常结束状态与忽略记录一起看。
// 调用 client.Send 发送 continuePrompt，失败时保留继续入口以便重试。
func (s *chatState) continueLastTurn() {
	s.mu.Lock()
	tid := s.selectedThreadID
	if tid == "" || s.sending || s.composerPrefsSaving[tid] {
		s.mu.Unlock()
		return
	}
	// 判定所需数据都在 s.mu 保护下读取：这里不能调用 isThreadRunning 之类会再次加锁的函数。
	canContinue, _ := shouldShowContinuePill(s.continuePillSnapshotLocked(tid))
	if !canContinue || strings.TrimSpace(s.draft) != "" || len(s.attachments) > 0 {
		s.mu.Unlock()
		return
	}
	s.sending = true
	s.scrollToEnd = true
	s.mu.Unlock()
	s.invalidate()

	go func() {
		ctx := context.Background()
		delegated, err := s.client.Send(ctx, tid, continuePrompt)
		s.mu.Lock()
		s.sending = false
		if err != nil {
			s.toastMessage = "继续执行失败: " + err.Error()
		} else {
			delete(s.interrupted, tid)
			// 发送成功：乐观置为运行中，界面立即显示“回复中”；消息交给子线程时不标记。
			if !delegated {
				s.markThreadRunningOptimisticallyLocked(tid)
			}
		}
		s.mu.Unlock()
		s.invalidate()
	}()
}

// removeQueued 调用客户端从指定会话的排队列表中移除消息。
func (s *chatState) removeQueued(id string) {
	s.mu.Lock()
	tid := s.selectedThreadID
	s.mu.Unlock()
	if tid == "" || id == "" {
		return
	}

	go func() {
		ctx := context.Background()
		if err := s.client.RemoveQueued(ctx, tid, id); err != nil {
			s.setToast("删除排队消息失败: " + err.Error())
		}
	}()
}

// sendQueuedNow 调用客户端立即发送当前会话的指定排队消息。
func (s *chatState) sendQueuedNow(id string) {
	s.mu.Lock()
	tid := s.selectedThreadID
	s.mu.Unlock()
	if tid == "" || id == "" {
		return
	}

	go func() {
		ctx := context.Background()
		if err := s.client.SendQueuedNow(ctx, tid, id); err != nil {
			s.setToast("立即发送失败: " + err.Error())
			return
		}
		// 立即发送会让空闲会话重新进入运行，这里乐观置位，界面立即显示“回复中”。
		s.mu.Lock()
		s.markThreadRunningOptimisticallyLocked(tid)
		s.mu.Unlock()
		s.invalidate()
	}()
}

// providerDisplayName 根据 ID 查找 Provider 的展示名称，找不到时返回 ID，为空时返回空字符串。
// providerDisplayName 返回 provider 的展示名；pi 线程的额度 key 带「pi:端点」前缀，
// 展示时只取端点 id，查得到 provider 时仍用它的名字。
func (s *chatState) providerDisplayName(providerID string) string {
	if providerID == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.providers {
		if p.ID == providerID && p.Name != "" {
			return p.Name
		}
	}
	if endpoint, ok := strings.CutPrefix(providerID, "pi:"); ok && endpoint != "" {
		for _, p := range s.providers {
			if p.ID == endpoint && p.Name != "" {
				return p.Name
			}
		}
		return endpoint
	}
	return providerID
}

// toggleSidebar 切换侧边栏的折叠与展开状态。
func (s *chatState) toggleSidebar() {
	s.mu.Lock()
	s.sidebarCollapsed = !s.sidebarCollapsed
	s.mu.Unlock()
	s.invalidate()
}

// openRenameModal 打开会话重命名弹窗并预填标题。
func (s *chatState) openRenameModal(id, currentTitle string) {
	s.mu.Lock()
	s.renameModalOpen = true
	s.renameThreadID = id
	s.renameThreadTitle = currentTitle
	s.mu.Unlock()
	s.invalidate()
}

// closeRenameModal 关闭会话重命名弹窗。
func (s *chatState) closeRenameModal() {
	s.mu.Lock()
	s.renameModalOpen = false
	s.renameThreadID = ""
	s.renameThreadTitle = ""
	s.mu.Unlock()
	s.invalidate()
}

// submitRename 提交会话重命名。空标题不能保存。
func (s *chatState) submitRename() {
	s.mu.Lock()
	id := s.renameThreadID
	title := strings.TrimSpace(s.renameThreadTitle)
	s.mu.Unlock()
	if id == "" || title == "" {
		return
	}
	s.closeRenameModal()

	go func() {
		if err := s.client.RenameThread(id, title); err != nil {
			s.setToast("重命名失败: " + err.Error())
			return
		}
		s.mu.Lock()
		for _, list := range s.threadsByProj {
			for i := range list {
				if list[i].ID == id {
					list[i].Title = title
				}
			}
		}
		for i := range s.threads {
			if s.threads[i].ID == id {
				s.threads[i].Title = title
			}
		}
		s.mu.Unlock()
		s.invalidate()
	}()
}

// setThreadPinned 设置指定会话的置顶状态，并更新排序。
func (s *chatState) setThreadPinned(id string, pinned bool) {
	go func() {
		if err := s.client.SetThreadPinned(id, pinned); err != nil {
			s.setToast("设置置顶失败: " + err.Error())
			return
		}
		s.mu.Lock()
		for pid, list := range s.threadsByProj {
			for i := range list {
				if list[i].ID == id {
					list[i].Pinned = pinned
				}
			}
			s.threadsByProj[pid] = SortThreads(list)
		}
		for i := range s.threads {
			if s.threads[i].ID == id {
				s.threads[i].Pinned = pinned
			}
		}
		s.threads = SortThreads(s.threads)
		s.mu.Unlock()
		s.invalidate()
	}()
}

// archiveThread 归档指定会话，使其从侧边栏消失；若当前选中则清空选中状态。
func (s *chatState) archiveThread(id string) {
	go func() {
		if err := s.client.SetThreadArchived(id, true); err != nil {
			s.setToast("归档失败: " + err.Error())
			return
		}
		s.mu.Lock()
		if s.selectedThreadID == id {
			s.selectedThreadID = ""
			stopStewardTaskWatchLocked(s)
			s.messages = nil
			s.messagesRev++
			s.approvals = nil
			s.threadUsage = ThreadUsage{Turns: make(map[int]TokenUsage)}
			if s.watchStop != nil {
				s.watchStop()
				s.watchStop = nil
			}
		}
		for pid, list := range s.threadsByProj {
			var filtered []Thread
			for _, th := range list {
				if th.ID != id {
					filtered = append(filtered, th)
				}
			}
			s.threadsByProj[pid] = filtered
		}
		var filtered []Thread
		for _, th := range s.threads {
			if th.ID != id {
				filtered = append(filtered, th)
			}
		}
		s.threads = filtered
		// 删掉了会话：待处理请求集合必须重算。
		s.bumpThreadsRuntimeLocked()
		s.mu.Unlock()
		s.invalidate()
	}()
}

// openDeleteModal 打开删除会话确认弹窗。
func (s *chatState) openDeleteModal(id, title string) {
	s.mu.Lock()
	s.deleteModalOpen = true
	s.deleteThreadID = id
	s.deleteThreadTitle = title
	s.mu.Unlock()
	s.invalidate()
}

// closeDeleteModal 关闭删除会话确认弹窗。
func (s *chatState) closeDeleteModal() {
	s.mu.Lock()
	s.deleteModalOpen = false
	s.deleteThreadID = ""
	s.deleteThreadTitle = ""
	s.mu.Unlock()
	s.invalidate()
}

// confirmDelete 确认删除会话；若当前选中则清空选中状态。
func (s *chatState) confirmDelete() {
	s.mu.Lock()
	id := s.deleteThreadID
	s.mu.Unlock()
	s.closeDeleteModal()
	if id == "" {
		return
	}

	go func() {
		if err := s.client.DeleteThread(id); err != nil {
			s.setToast("删除失败: " + err.Error())
			return
		}
		s.forgetDeletedThread(id)
	}()
}

// forgetDeletedThread 清掉被删会话在本地的痕迹：它是当前会话时退回空状态、
// 停掉订阅，再从项目树与会话列表里移除它。
// 自己拿 s.mu，调用时不要持有它。
func (s *chatState) forgetDeletedThread(id string) {
	s.mu.Lock()
	if s.selectedThreadID == id {
		s.selectedThreadID = ""
		stopStewardTaskWatchLocked(s)
		s.messages = nil
		s.messagesRev++
		s.approvals = nil
		s.threadUsage = ThreadUsage{Turns: make(map[int]TokenUsage)}
		if s.watchStop != nil {
			s.watchStop()
			s.watchStop = nil
		}
	}
	for pid, list := range s.threadsByProj {
		var filtered []Thread
		for _, th := range list {
			if th.ID != id {
				filtered = append(filtered, th)
			}
		}
		s.threadsByProj[pid] = filtered
	}
	var filtered []Thread
	for _, th := range s.threads {
		if th.ID != id {
			filtered = append(filtered, th)
		}
	}
	s.threads = filtered
	// 删掉了会话：待处理请求集合必须重算。
	s.bumpThreadsRuntimeLocked()
	s.mu.Unlock()
	inspectorDeleteSession(s, id)
	s.invalidate()
}

// openRemoveProjectModal 打开从侧边栏移除项目确认弹窗。
func (s *chatState) openRemoveProjectModal(id, name string) {
	s.mu.Lock()
	s.removeProjModalOpen = true
	s.removeProjID = id
	s.removeProjName = name
	s.mu.Unlock()
	s.invalidate()
}

// closeRemoveProjectModal 关闭移除项目确认弹窗。
func (s *chatState) closeRemoveProjectModal() {
	s.mu.Lock()
	s.removeProjModalOpen = false
	s.removeProjID = ""
	s.removeProjName = ""
	s.mu.Unlock()
	s.invalidate()
}

// confirmRemoveProject 确认从侧边栏移除项目。
func (s *chatState) confirmRemoveProject() {
	s.mu.Lock()
	pid := s.removeProjID
	s.mu.Unlock()
	s.closeRemoveProjectModal()
	if pid == "" {
		return
	}

	go func() {
		if err := s.client.RemoveProject(pid); err != nil {
			s.setToast("移除项目失败: " + err.Error())
			return
		}
		s.mu.Lock()
		var filteredProjs []Project
		for _, p := range s.projects {
			if p.ID != pid {
				filteredProjs = append(filteredProjs, p)
			}
		}
		s.projects = filteredProjs
		if threads, ok := s.threadsByProj[pid]; ok {
			for _, th := range threads {
				if th.ID == s.selectedThreadID {
					s.selectedThreadID = ""
					stopStewardTaskWatchLocked(s)
					s.messages = nil
					s.messagesRev++
					s.approvals = nil
					s.threadUsage = ThreadUsage{Turns: make(map[int]TokenUsage)}
					if s.watchStop != nil {
						s.watchStop()
						s.watchStop = nil
					}
					break
				}
			}
			delete(s.threadsByProj, pid)
		}
		delete(s.collapsedProj, pid)
		delete(s.visibleThreadsProj, pid)
		var filteredThreads []Thread
		for _, th := range s.threads {
			if th.ProjectID != pid {
				filteredThreads = append(filteredThreads, th)
			}
		}
		s.threads = filteredThreads
		// 整个项目的会话都被移除：待处理请求集合必须重算。
		s.bumpThreadsRuntimeLocked()
		s.mu.Unlock()
		s.invalidate()
	}()
}

// loadModelPreference 在缓存未命中时异步拉取固定模型偏好并写入缓存，拉取完成后刷新视图。
func (s *chatState) loadModelPreference(pin ModelPin) {
	if pin.Provider == "" || pin.ModelID == "" || s.client == nil {
		return
	}
	key := pin.Key()
	s.mu.Lock()
	if s.modelPrefCache == nil {
		s.modelPrefCache = make(map[string]ModelPreference)
	}
	if _, exists := s.modelPrefCache[key]; exists {
		s.mu.Unlock()
		return
	}
	if s.modelPrefLoading == nil {
		s.modelPrefLoading = make(map[string]bool)
	}
	if s.modelPrefLoading[key] {
		s.mu.Unlock()
		return
	}
	s.modelPrefLoading[key] = true
	s.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		pref, err := s.client.ModelPreferences(ctx, pin)
		s.mu.Lock()
		delete(s.modelPrefLoading, key)
		if _, exists := s.modelPrefCache[key]; !exists && s.modelPrefWrites[key] == nil {
			s.modelPrefCache[key] = pref
		}
		s.mu.Unlock()
		if err != nil {
			s.setToast("读取模型偏好失败: " + err.Error())
		}
		s.invalidate()
	}()
}

// modelPreference 读取内存缓存中的模型偏好设置；未命中时返回零值。
func (s *chatState) modelPreference(pin ModelPin) ModelPreference {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.modelPrefCache == nil {
		return ModelPreference{}
	}
	return s.modelPrefCache[pin.Key()]
}

// effortBackendAllowed 判断推理强度能否写进会话：后端只接受这份白名单里的档位，
// 模型目录里还存在 off/minimal 等档位，直接下发会被判为参数非法。
// UI 层不 import internal/remote，因此在这里用本地集合复刻同一份白名单；空串表示跟随模型默认，总是允许。
func effortBackendAllowed(effort string) bool {
	switch effort {
	case "", "low", "medium", "high", "xhigh", "max":
		return true
	}
	return false
}

// defaultEffortForModel 计算切到该模型后应当套用的默认推理强度，供切换模型时写入会话：
// 优先级依次为用户在设置里为该模型配置的默认强度、模型自身默认档、空串（表示没有可用默认值）。
// 结果还要同时落在模型支持的档位列表与后端白名单内，任一不满足都按空串处理，
// 让调用方退回“旧强度能支持就保留”的原有逻辑。
// 偏好先读设置页内存里的配置副本（用户刚改完立刻生效），未命中再回退偏好缓存；
// 顺带触发一次异步加载，让缓存后续跟上。
// 调用方不得持有 s.mu：内部会读偏好缓存与登记异步加载；
// 也不得持有 st.cfgMu：设置页配置副本由本函数自己按需加锁读取，重入会自锁。
func (s *chatState) defaultEffortForModel(provider string, m ModelInfo) string {
	pin := modelPreferencePin(provider, m.ID)
	effort := ""
	if st := getChatSettings(s); st != nil {
		// cfg 由 cfgMu 保护（见 SettingsState 注释），锁只包住读副本这段，
		// 后面的偏好缓存与异步加载各用自己的锁，不并进本次临界区。
		st.cfgMu.Lock()
		if pref := st.cfg.Models.ModelPreferenceFor(pin); pref.Effort != nil {
			effort = *pref.Effort
		}
		st.cfgMu.Unlock()
	}
	if effort == "" {
		if pref := s.modelPreference(pin); pref.Effort != nil {
			effort = *pref.Effort
		}
	}
	if effort == "" {
		effort = m.DefaultEffort
	}
	s.loadModelPreference(pin)
	if effort == "" || !modelSupportsEffort(m, effort) || !effortBackendAllowed(effort) {
		return ""
	}
	return effort
}

// updateModelPreference 串行读改写，避免首次加载或连续操作覆盖已有偏好。
func (s *chatState) updateModelPreference(pin ModelPin, mutate func(*ModelPreference)) {
	if pin.Provider == "" || pin.ModelID == "" || s.client == nil {
		return
	}
	key := pin.Key()
	s.mu.Lock()
	if s.modelPrefWrites == nil {
		s.modelPrefWrites = make(map[string]chan struct{})
	}
	prev := s.modelPrefWrites[key]
	done := make(chan struct{})
	s.modelPrefWrites[key] = done
	s.mu.Unlock()
	s.writeModelPreference(pin, prev, done, mutate)
}

// writeModelPreference 在后台按登记顺序执行一次写入：先读服务端最新偏好，再用 apply 算出新值并落盘。
// 只有本次写入仍是最新登记（s.modelPrefWrites[key] == done）时才把结果回写缓存，
// 否则更早的在途写入会把它之后发生的同步翻转成果盖回旧值。
// 写入成功后还要把结果镜像回设置页的配置副本（st.cfg）：defaultEffortForModel 取推理强度时优先读这份副本，
// 而滑杆、重置、快速模式只经由本函数写后端与偏好缓存，不镜像的话先在设置页配一档、再在输入框改成另一档，
// 切到别的模型再切回来就会读到设置页那份过期值。
// 镜像放在 s.mu 之外：本仓固定 st.mu -> st.cfgMu 的锁序，镜像要按这个顺序取 st.mu 再取 st.cfgMu；
// 镜像代码只碰这两把锁（s.modelPrefCache 已在上面 s.mu 的临界区里更新完），不回调任何取 s.mu 的函数。
// 设置页副本走的是 SetModelPreference，它在 effort 为 nil 且非快速模式时删键，
// 所以重置产生的零值偏好会顺手清掉设置页里该模型的旧档位，不需要额外的删除分支。
// 调用方必须在锁内登记 done 后再调用。
func (s *chatState) writeModelPreference(pin ModelPin, prev, done chan struct{}, apply func(*ModelPreference)) {
	key := pin.Key()
	s.invalidate()

	go func() {
		if prev != nil {
			<-prev
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		next, err := s.client.ModelPreferences(ctx, pin)
		if err == nil {
			apply(&next)
			err = s.client.UpdateModelPreference(ctx, pin, next)
		}
		s.mu.Lock()
		latest := s.modelPrefWrites[key] == done
		mirror := err == nil && latest
		if mirror {
			if s.modelPrefCache == nil {
				s.modelPrefCache = make(map[string]ModelPreference)
			}
			s.modelPrefCache[key] = next
		}
		if latest {
			delete(s.modelPrefWrites, key)
		}
		close(done)
		s.mu.Unlock()
		if mirror {
			if st := getChatSettings(s); st != nil {
				// 写 cfg 要同时持有 st.mu 与 st.cfgMu（顺序 mu → cfgMu），渲染路径在 st.mu 下读。
				// 这里已经在 s.mu 之外，取 st.mu 不会与 s.mu 形成环。
				st.mu.Lock()
				st.cfgMu.Lock()
				st.cfg.Models.SetModelPreference(pin, next)
				st.cfgMu.Unlock()
				st.mu.Unlock()
			}
		}
		if err != nil {
			s.setToast("更新模型偏好失败: " + err.Error())
		}
		s.invalidate()
	}()
}

func (s *chatState) modelPreferenceBusy(pin ModelPin) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.modelPrefLoading[pin.Key()] || s.modelPrefWrites[pin.Key()] != nil
}

func (s *chatState) toggleModelFastMode(pin ModelPin) {
	s.updateModelPreference(pin, func(pref *ModelPreference) {
		pref.FastMode = !pref.FastMode
		next := pref.FastMode
		s.syncSelectedThreadFastMode(pin, next)
	})
}

// toggleModelFastModeNow 同步翻转一次快速模式，返回翻转后的最终值。
// 缓存翻转与写入登记在同一个临界区内完成：调用返回时缓存已是最终值，后台落盘写入的也是同一个值，
// 因此既不会出现“落盘时按服务端旧状态又翻一次”，也不会被更早的在途写入把旧值盖回来。
// 没有客户端时只改内存缓存，本地命令仍然生效。
func (s *chatState) toggleModelFastModeNow(pin ModelPin) bool {
	key := pin.Key()
	persist := pin.Provider != "" && pin.ModelID != "" && s.client != nil
	s.mu.Lock()
	if s.modelPrefCache == nil {
		s.modelPrefCache = make(map[string]ModelPreference)
	}
	pref := s.modelPrefCache[key]
	pref.FastMode = !pref.FastMode
	fast := pref.FastMode
	s.modelPrefCache[key] = pref
	var prev chan struct{}
	var done chan struct{}
	if persist {
		if s.modelPrefWrites == nil {
			s.modelPrefWrites = make(map[string]chan struct{})
		}
		prev = s.modelPrefWrites[key]
		done = make(chan struct{})
		s.modelPrefWrites[key] = done
	}
	s.mu.Unlock()
	if !persist {
		s.invalidate()
		return fast
	}
	s.writeModelPreference(pin, prev, done, func(pref *ModelPreference) {
		// 落盘写的是同一个绝对值，而不是按服务端状态再翻一次。
		pref.FastMode = fast
		s.syncSelectedThreadFastMode(pin, fast)
	})
	return fast
}

// applyThreadFastModeLocked 把快速模式与速率档位写回本地会话列表（平铺视图与按项目分组视图），调用方需持有 s.mu。
func (s *chatState) applyThreadFastModeLocked(threadID string, fast bool, tier string) {
	apply := func(th *Thread) {
		th.FastMode = fast
		th.SpeedTier = tier
	}
	for i := range s.threads {
		if s.threads[i].ID == threadID {
			apply(&s.threads[i])
			break
		}
	}
	for _, list := range s.threadsByProj {
		for i := range list {
			if list[i].ID == threadID {
				apply(&list[i])
				break
			}
		}
	}
}

// syncSelectedThreadFastMode 在选中会话的 Lead 模型与 pin 匹配时，把快速模式写穿到该会话。
// 会话未选中、不在本地列表或 Lead 模型与 pin 不一致时不做任何事。
func (s *chatState) syncSelectedThreadFastMode(pin ModelPin, fast bool) {
	if s.client == nil {
		return
	}
	s.mu.Lock()
	threadID := s.selectedThreadID
	if s.isPendingThreadLocked(threadID) {
		// 待创建会话：快速模式只记进待创建选项，创建时随选项下发。
		s.setPendingFastLocked(fast)
		s.mu.Unlock()
		s.invalidate()
		return
	}
	matched := false
	if threadID != "" {
		for i := range s.threads {
			if s.threads[i].ID != threadID {
				continue
			}
			provider := s.threads[i].Provider
			if provider == "" {
				provider = "claude"
			}
			matched = modelPreferencePin(provider, s.threads[i].Model).Key() == pin.Key()
			break
		}
	}
	s.mu.Unlock()
	if !matched {
		return
	}
	if err := s.client.SetThreadFastMode(threadID, fast, ""); err != nil {
		s.setToast("设置会话快速模式失败: " + err.Error())
		return
	}
	s.mu.Lock()
	s.applyThreadFastModeLocked(threadID, fast, "")
	s.mu.Unlock()
	s.invalidate()
}

// toggleThreadFastMode 按会话翻转快速模式：已有会话直接写后端并回填本地列表，
// 待创建会话只记进待创建选项（创建时随选项下发）。cur 为界面当前展示的会话值。
// 模型偏好只作为新建会话的默认值，这里不改偏好。
func (s *chatState) toggleThreadFastMode(threadID string, cur bool) {
	if s.client == nil {
		return
	}
	next := !cur
	if threadID == "" {
		s.mu.Lock()
		s.setPendingFastLocked(next)
		s.mu.Unlock()
		s.invalidate()
		return
	}
	if err := s.client.SetThreadFastMode(threadID, next, ""); err != nil {
		s.setToast("设置会话快速模式失败: " + err.Error())
		return
	}
	s.mu.Lock()
	s.applyThreadFastModeLocked(threadID, next, "")
	s.mu.Unlock()
	s.invalidate()
}

func (s *chatState) setModelEffortPreference(pin ModelPin, effort string) {
	s.updateModelPreference(pin, func(pref *ModelPreference) {
		pref.Effort = nil
		if effort != "" {
			pref.Effort = &effort
		}
	})
}

// resetThreadFastMode 关闭指定会话的快速模式并清空速率档位：
// 已有会话写回后端并同步本地列表，待创建会话只记进待创建选项（创建时随选项下发）。
func (s *chatState) resetThreadFastMode(threadID string) {
	if s.client == nil {
		return
	}
	if threadID == "" {
		s.mu.Lock()
		s.setPendingFastLocked(false)
		s.mu.Unlock()
		s.invalidate()
		return
	}
	if err := s.client.SetThreadFastMode(threadID, false, ""); err != nil {
		s.setToast("重置快速模式失败: " + err.Error())
		return
	}
	s.mu.Lock()
	s.applyThreadFastModeLocked(threadID, false, "")
	s.mu.Unlock()
	s.invalidate()
}

// resetModelPreference 重置指定模型的偏好为空，并通过 updateComposerPrefs 清空当前会话对应 tab 的 Effort。
func (s *chatState) resetModelPreference(threadID, tab string, pin ModelPin) {
	if threadID != "" {
		s.updateComposerPrefs(threadID, func(p *ComposerPrefs) {
			if tab == "bees" {
				p.BeesEffort = ""
			} else {
				p.Effort = ""
			}
		}, "重置偏好设置失败")
	}

	// 会话的快速模式与速率档位不在 composer 偏好里，必须单独写回后端并同步本地列表，
	// 否则重置后闪电开关仍然亮着；Bees 侧的快速模式只存在模型偏好里，由下面整体清空覆盖。
	if tab != "bees" {
		s.resetThreadFastMode(threadID)
	}

	s.updateModelPreference(pin, func(pref *ModelPreference) {
		*pref = ModelPreference{}
	})
}
