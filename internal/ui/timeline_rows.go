package mcodeui

import (
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui/markdown"
)

var (
	userMsgExpandedMu sync.Mutex
	userMsgExpanded   = make(map[string]bool)

	// 复制反馈：按按钮 key 记录复制时刻，1.4 秒内按钮图标显示为绿色对勾。
	copyFeedbackMu sync.Mutex
	copyFeedbackAt = make(map[string]time.Time)

	// reasoningWasLive 记录曾经处于 live 状态的思考消息 ID，用于在 live 结束帧保护正在翻阅的用户不被突然收起。
	reasoningWasLiveMu sync.Mutex
	reasoningWasLive   = make(map[string]bool)
)

// copyFeedbackDuration 是复制按钮显示绿色对勾的时长。
const copyFeedbackDuration = 2500 * time.Millisecond

const (
	turnGap  float32 = 15 // token-exempt: 轮次间距采用设计稿尺寸。
	blockGap float32 = 10 // token-exempt: 轮内块间距采用设计稿尺寸。
)

// streamingCursor 是流式回复末尾的光标符号。
const streamingCursor = "▍"

// stewardSilentMarker 是总管被唤醒、但没有需要用户知道的事时整轮回复的唯一正文标记：
// 这一轮在时间线里整条隐藏。标记只在界面侧定义一份，后端提示词里写字面量，不往 internal/config 加符号。
const stewardSilentMarker = "[[静默]]"

// stewardWakeKind 是总管被宿主唤醒时落库的消息 Kind（取值与 internal/remote 的
// threadMessageKindStewardWake 一致）。这类消息是系统输入，不是用户自己说的话，
// 时间线里不产生行，也不渲染成「宿主转来」卡片。
const stewardWakeKind = "steward_wake"

const stewardReplyKind = "steward_reply"
const stewardDecisionKind = "steward_decision"
const stewardDecisionAnswerKind = "steward_decision_answer"
const stewardResultKind = "steward_result"

// hostEventKind 是宿主转来事件的 Kind：任务会话里仍然渲染成卡片，但同样不算用户开口。
const hostEventKind = "host_event"

// splitSessionKind 是分割会话的消息 Kind（取值与 internal/remote 一致）：
// 这条系统消息就是分割线本身，时间线里渲染成一条细线，分割线以上的历史照常显示。
const splitSessionKind = "split_session"

// contextCompactedKind 是「上下文已压缩」边界消息的 Kind（取值与 internal/remote 一致）：
// provider 压缩过后端落一条独立系统消息，时间线里渲染成一条细线，与分割线并列，不进回合折叠。
const contextCompactedKind = "context_compacted"

// contextCompactedNotice 是压缩边界行没有任何正文时的兑底文案。
const contextCompactedNotice = "已压缩上下文"

// isHiddenTriggerKind 判断 user 角色消息的 Kind 是否属于系统输入（宿主事件、总管唤醒）：
// 它们都不是用户自己说的话，不能算用户开口。
func isHiddenTriggerKind(kind string) bool {
	return kind == hostEventKind || kind == stewardWakeKind
}

// isHiddenTimelineMessage 判断消息是否不产生时间线行：总管唤醒这类系统输入，
// 以及整轮正文只有静默标记的助手回复。判断必须在构建 items 这一层做，
// 不能靠渲染返回空元素——每行外面还有一层带内边距的 Row，空元素会留下空白。
func isHiddenTimelineMessage(m Message) bool {
	if m.Kind == stewardDecisionAnswerKind {
		return true
	}
	if m.Role == "user" {
		return m.Kind == stewardWakeKind
	}
	if m.Role != "assistant" {
		return false
	}
	text := strings.TrimSpace(m.Text)
	if text == stewardSilentMarker {
		return true
	}
	// 流式输出时正文可能只是标记的前缀（如 "[[静"）：先不显示，避免闪现半截标记。
	return !m.IsFinal && text != "" && strings.HasPrefix(stewardSilentMarker, text)
}

// thinkPulseAlpha 返回「思考中…」呼吸标识的透明度：1400ms 一个周期的三角波
// （0ms 为 0.45、700ms 为 1、1400ms 回到 0.45，与原关键帧一致），相位按 8 档量化、
// 每档约 175ms，配合调用方 c.After(175ms) 请求下一帧即可，无需逐帧关键帧动画。
func thinkPulseAlpha(now time.Time) float64 {
	const (
		period = int64(1400) // 一个呼吸周期的毫秒数
		steps  = int64(8)    // 相位量化档数
		stepMs = period / steps
	)
	phase := now.UnixMilli() % period
	if phase < 0 {
		phase += period
	}
	// 四舍五入到最近一档，避免同一档内每次调用都取到不同值
	step := (phase + stepMs/2) / stepMs
	half := steps / 2
	if step > half { // 后半程透明度回落
		step = steps - step
	}
	return 0.45 + 0.55*float64(step)/float64(half)
}

// copyFeedbackActive 返回 key 对应的复制反馈是否仍在显示窗口（1.4 秒）内，供按钮决定显示对勾还是复制图标。
func copyFeedbackActive(key string, now time.Time) bool {
	if key == "" {
		return false
	}
	copyFeedbackMu.Lock()
	at, ok := copyFeedbackAt[key]
	copyFeedbackMu.Unlock()
	return ok && now.Sub(at) < copyFeedbackDuration
}

// markCopyFeedback 记录一次复制成功，并触发重绘；反馈窗口结束后再触发一次重绘让图标恢复，
// 顺手清掉过期的记录（仿 composer_attach.go 的 setAttachmentNotice）。
func markCopyFeedback(s *chatState, key string) {
	if key == "" {
		return
	}
	copyFeedbackMu.Lock()
	copyFeedbackAt[key] = time.Now()
	copyFeedbackMu.Unlock()
	if s == nil {
		return
	}
	s.invalidate()
	time.AfterFunc(copyFeedbackDuration, func() {
		copyFeedbackMu.Lock()
		if at, ok := copyFeedbackAt[key]; ok && time.Now().Sub(at) >= copyFeedbackDuration {
			delete(copyFeedbackAt, key)
		}
		copyFeedbackMu.Unlock()
		s.invalidate()
	})
}

// 用户长消息的折叠阈值：普通会话 8 行 / 600 字；子代理嵌入会话 5 行 / 400 字，
// 因为那边的首条消息是派发提示词（机器写给自己看的），只需要露出开头几行。
const (
	userCollapseLines         = 8
	userCollapseRunes         = 600
	userCollapseEmbeddedLines = 5
	userCollapseEmbeddedRunes = 400
)

// userFadeHeight 是折叠态正文底部渐隐层的高度（pt）。
const userFadeHeight float32 = 28

// 消息入场动画：新到达的消息向上落位并淡入，时长只取 motion.go 的 token。
const (
	// messageEnterWindow 是入场动画的时间窗：只有这段时间内新到达的消息才播，
	// 加载历史时出现的旧消息一律不播。
	messageEnterWindow = 1500 * time.Millisecond
	// messageEnterShift 是入场动画的起始下移距离（pt）。
	messageEnterShift float32 = 8
	// messageEnterCap 是入场登记表的上限，超过就整表清空重建，避免长会话里无限增长。
	messageEnterCap = 2000
)

type timelineItemKind int

const (
	timelineItemSingle timelineItemKind = iota
	timelineItemToolGroup
	timelineItemSwarmReport   // 汇报行：相邻汇报消息合并成一行，行内胶囊横向流式排列
	timelineItemSwarmCapsules // 子线程胶囊行：每个 Swarm 子线程一枚，内容每帧读当前状态
	timelineItemSwarmDispatch // 派发占位行：派单块正在写、或已写完但子代理还没出现时占位，不可点击
	timelineItemResolvedPlan
	timelineItemPendingPlan
	timelineItemAnsweredQuestion // 已答复提问的历史态卡片：提问卡片消灭后在时间线里留一条问答记录
	timelineItemRunning
	timelineItemSessionUsage
	// timelineItemForkOrigin 是时间线最顶部的 fork 来源条：只在当前会话带来源字段时出现，
	// 内容每帧按会话列表现查源会话标题（见 renderForkOriginRow）。
	timelineItemForkOrigin
	timelineItemForwardedContext
	// timelineItemToolBeat 是展开后的工具组内的一个“节拍”：一条思考消息加上它后面紧跟的连续工具调用，
	// 渲染成一行思考叙事加一行可换行的动作胶囊（行级虚拟化单项）。
	timelineItemToolBeat
	// timelineItemTurnWork 是已结束回合的折叠项：把用户消息与最终答复之间的思考、工具调用与中途正文
	// 收进一行组头（用时 Xs），展开后才显示里面的条目。
	timelineItemTurnWork
	// timelineItemBackgroundWait 是时间线末尾的等待条：回合已结束但仍有后台任务在跑时出现，
	// 用一行扫光文字提示会话还在等它们，点击打开输入区胶囊上已有的后台任务浮层。
	timelineItemBackgroundWait
	// 总管会话里每个回合开头的日期时间分隔行。
	timelineItemTurnTime
	timelineItemStewardTask
	timelineItemStewardAmend
	timelineItemStewardTaskChips
)

// timelineItem 表示时间线中的一个渲染项（单条消息、连续工具合并组或组内的一个节拍）。
type timelineItem struct {
	kind         timelineItemKind
	stewardTask  KanbanTask
	stewardTasks []KanbanTask
	msg          Message
	tools        []Message // 组头项与组内各节拍共用的整组消息
	reportMsgs   []Message // 合并后的连续 Swarm 汇报消息（汇报行专有，首条与 msg 相同）
	beatTools    []Message // 节拍内由这条思考触发的连续工具调用（没有前置思考的节拍只带工具）
	id           string
	threadID     string // 该行所属会话 ID（会话统计行用它定位数据），其它行可为空
	turnIndex    int
	resolvedPlan resolvedPlan
	// answeredQuestion 是已答复提问的历史态（只有 timelineItemAnsweredQuestion 用它）。
	answeredQuestion answeredQuestion
	// 该项消息的完成时间，由消息 CreatedAt 解析得到，拿不到时为零值。
	finishedAt time.Time
	// 时间分隔行要显示的时刻，只有 timelineItemTurnTime 使用。
	stampAt time.Time
	// groupID 是所属工具组 ID（组头项与组内各节拍共用）。
	groupID string
	// groupExpanded 标记工具组处于展开状态：展开时组内各节拍紧随组头项生成。
	groupExpanded bool
	// workItems 是回合折叠项展开后要渲染的条目：思考、工具组、工具节拍与中途正文，按原顺序排列。
	workItems []timelineItem
	// workElapsed 是回合折叠项组头显示的耗时短文本（如 "3.2s"），拿不到时间戳时为空。
	workElapsed string
	// workAttention 标记被折叠的回合里有工具执行失败，组头据此显示警示图标。
	workAttention bool
	// workRunning 标记这是运行中回合的临时折叠头：只显示实时计时，回合结束后会被摘掉重折。
	workRunning bool
	// workStartedAt 是运行中回合的起始时刻（本回合用户消息的发送时间），拿不到时为零值。
	workStartedAt time.Time
	// workHeaderID 打在折叠项之后那条负责收尾的答复上：非空表示回合完成信息已由折叠头承载，
	// 这条消息不再单独画完成行，只保留改动文件卡片。
	workHeaderID string
	// msgLastIndex 是该项覆盖的最后一条消息下标（回合折叠项覆盖被收起的整段，组头项覆盖整组）；
	// 未设置（0）时按 itemLastMsgIndex 回退到 msgIndex。
	msgLastIndex int
	// isFirst、isLast 标记该节拍是否为组内首个、最后一个（节拍分界见 groupSegments）。
	isFirst bool
	isLast  bool
	// msgIndex 是该项对应消息在原始 messages 切片中的起始下标。
	msgIndex int
	// capsuleSpan 是子线程胶囊行的回合区间：渲染时按它把该回合的子线程分到这一行（其它行为零值）。
	capsuleSpan swarmCapsuleTurnSpan
	// forkFromThreadID 是 fork 来源条指向的源会话 ID（只有来源条用它）。
	forkFromThreadID string
}

// timelineGroupSegment 是展开后的工具组内的一个节拍：段首与段尾（不含）在 run 内的下标。
// 一条思考消息加上它后面紧跟的所有连续工具调用构成一段；组开头没有前置思考的工具单独成一段。
type timelineGroupSegment struct {
	start int
	end   int
}

// turnWorkFoldPrefix 是回合折叠项的稳定 ID 前缀：完整 ID 为 "turnwork-" + 该回合用户消息 ID，
// 同一回合反复重建时 ID 不变，展开状态与展开动画都能延续。
const turnWorkFoldPrefix = "turnwork-"

// turnHeaderShimmerLabel 是运行中回合组头扫光条的 a11y Label，用来在测试里确认页面上只有一处运行中扫光。
const turnHeaderShimmerLabel = "运行中的回合头扫光"

// timelineTurnWorkFold 描述一个回合里可以收进折叠项的那一段：foldStart 之前（用户消息）保持可见，
// [foldStart, answerIdx) 收进折叠项，answerIdx 起是最终答复及其后的条目。
type timelineTurnWorkFold struct {
	foldStart int
	answerIdx int
}

// isPinSentMessageEnabled 检查当前配置中是否开启了发送后置顶消息（取不到配置时视为关闭）。
// 它会锁设置状态的锁，必须在 s.mu 之外调用。
func isPinSentMessageEnabled(s *chatState) bool {
	if st := getChatSettings(s); st != nil {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.cfg.Session.PinSentMessageEnabled()
	}
	return false
}

// timelineItemTopGap 返回时间线第 i 项上方的间距，保留组头和等待行的特殊布局。
func timelineItemTopGap(items []timelineItem, i int, _ float32) float32 {
	if i <= 0 || i >= len(items) {
		return 0
	}
	if items[i].turnIndex != items[i-1].turnIndex {
		return turnGap
	}
	if items[i].kind == timelineItemToolBeat {
		if items[i].isFirst {
			return 4
		}
		return blockGap
	}
	// 等待后台任务条紧贴最后一条回复，不按列表间距拉开。
	if items[i].kind == timelineItemBackgroundWait {
		return 8
	}
	return blockGap
}

// continuationStartPrefixes 是转发会话（Continued）与分支会话（Branch）标题的前缀及对应的来源提示语。
// 标题前缀只用于兼容旧会话，新会话按来源字段判定。
var continuationStartPrefixes = []struct {
	prefix string
	label  string
}{
	{"Continued: ", "继续自"},
	{"Branch: ", "分叉自"},
}

// continuationStartInfo 描述空转发/分叉会话的来源引导卡片内容。
type continuationStartInfo struct {
	Label          string // 来源提示语："继续自" 或 "分叉自"
	SourceTitle    string // 去掉前缀后的来源会话标题（完整原文，用于匹配来源会话）
	DisplayTitle   string // 卡片上展示的标题：过长时已截断并以 "..." 结尾
	TargetThreadID string // 同一项目内标题匹配来源会话的会话 ID，找不到时为空
}

// continuationSourceTitleMaxRunes 限制引导卡片上来源标题的最大字符数：超过时保留前 33 个字符
// 再加 "..."（合计 36 个字符），避免长标题在窄屏下把卡片撑变形。
const continuationSourceTitleMaxRunes = 36

// forwardedContextState 是按会话缓存转发上下文的小状态：内容不可变，每个会话只拉取一次，
// 并记住用户点击后的展开状态（键存在即覆盖模式默认值）。
type forwardedContextState struct {
	mu       sync.Mutex
	contexts map[string]ForwardedContext
	loaded   map[string]bool // 已尝试加载（含失败），不再自动重试
	inflight map[string]bool
	expanded map[string]bool
}

var (
	forwardedContextStates  = make(map[*chatState]*forwardedContextState)
	forwardedContextStateMu sync.Mutex
)

type forwardedTurn struct {
	Role string
	Text string
}

// continuationStartCardMaxWidth 是来源引导卡片的最大宽度：窄屏跟随容器宽度，宽屏不超过 520
// 并居中。
const continuationStartCardMaxWidth = 520

// swarmFenceEntry 是一条消息去掉 swarm 派单代码块后的正文缓存：src 用来判断正文是否又改过。
type swarmFenceEntry struct {
	src string
	out string
}

// swarmFenceCacheMax 是派单块正文缓存的最大条目数。
const swarmFenceCacheMax = 64

// codeBlockHeaderHeight 是代码块标题栏高度：控件高 ControlLG 加一格内边距。
const codeBlockHeaderHeight float32 = ControlLG + Space1

// reasoningMarkdownKey 是思考正文 Markdown 配置的缓存键：会话加基准字号。
type reasoningMarkdownKey struct {
	state *chatState
	size  float32
}

// reasoningMDOpts 保存每个 chatState 的思考正文 Markdown 配置（与助手正文分开，避免字号与颜色互相影响）。
// 按字号分开缓存：独立的思考行用 12，工具组节拍里的思考叙事用 13，两者可能在同一帧里同时存在。
var (
	reasoningMDOptsMu sync.Mutex
	reasoningMDOpts   = make(map[reasoningMarkdownKey]*markdown.Options)
)

// commandToolNames 为算作命令行执行的工具名（已转小写并去掉 _ 与 -）。
var commandToolNames = map[string]bool{
	"bash":         true,
	"shell":        true,
	"exec_command": true,
	"execcommand":  true,
	"command":      true,
	"runcommand":   true,
	"run_command":  true,
	"terminal":     true,
	"localshell":   true,
	"local_shell":  true,
	"exec":         true,
	"execute":      true,
}

var (
	toolCommandKeyRe      = regexp.MustCompile(`"(?:command|cmd|CommandLine|command_line|commandline|input|script|code)"\s*:\s*"((?:[^"\\]|\\.)*)"`)
	toolCommandArgsRe     = regexp.MustCompile(`"(?:args|argv)"\s*:\s*\[([^\]]+)\]`)
	toolExitCodeJSONRe    = regexp.MustCompile(`"(?:exitCode|exit_code|exitStatus|exit_status|status_code|exit)"\s*:\s*(-?\d+)`)
	toolExitCodeTextRe    = regexp.MustCompile(`(?i)(?:exit\s+code\s*[:=]?\s*|exit\s+status\s*[:=]?\s*|\bexit\s*[:=]?\s*|\[exit\s*[:=]?\s*|\(exit\s*[:=]?\s*)(-?\d+)`)
	toolDurationMsJSONRe  = regexp.MustCompile(`"(?:duration_ms|durationMs|elapsed_ms|elapsedMs)"\s*:\s*([0-9]+(?:\.[0-9]+)?)`)
	toolDurationSecJSONRe = regexp.MustCompile(`"(?:duration|elapsed|duration_seconds|durationSeconds)"\s*:\s*([0-9]+(?:\.[0-9]+)?)`)
	toolDurationStrJSONRe = regexp.MustCompile(`"(?:duration|elapsed)"\s*:\s*"([0-9]+(?:\.[0-9]+)?\s*(?:s|ms))"`)
	toolDurationTextRe    = regexp.MustCompile(`(?i)\b(?:in|took|duration:?|elapsed:?)\s*([0-9]+(?:\.[0-9]+)?\s*(?:s|ms))\b`)
	toolCommandArgItemRe  = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
)

// CommandToolInfo 保存命令工具执行行的解析信息。
type CommandToolInfo struct {
	IsCommand bool   // 是否属于命令执行类工具
	Command   string // 命令正文（单行，超长截断）
	ExitCode  *int   // 退出码（0 为中性色，非 0 为错误色；拿不到为 nil）
	Duration  string // 耗时格式化文本（如「1.2s」；拿不到为空）
	Failed    bool   // 是否执行失败（m.ToolStatus == "failed" 或包含 [failed]）
}

// todoItem 表示待办清单中的一项。
type todoItem struct {
	Text   string // 待办正文
	Status string // pending / in_progress / completed
}

// todoTextKeys 是待办项正文的候选字段名；plan 类工具用 step，todos 类用 content。
var todoTextKeys = []string{"content", "step", "text", "task", "title"}

// todoProgressBarHeight 是待办进度条的轨道高度（pt），圆角取完整胶囊。
const todoProgressBarHeight float32 = 2

// toolRunStallAfter 是「输出多久没变化算空闲」的阈值。
const toolRunStallAfter = 30 * time.Second

// toolRunStallMinRun 是提示「无输出」所需的最短运行时长：短命命令很快就结束，不必惊扰用户。
const toolRunStallMinRun = 60 * time.Second

// toolRunIdleState 是运行中命令工具的输出观测值：输出长度与最后一次变化的时刻。
type toolRunIdleState struct {
	textLen   int
	changedAt time.Time
}

// toolChipHeight 是动作胶囊的高度，圆角取其一半。
const toolChipHeight float32 = 22

// toolBeatPadLeft 是节拍胶囊、折叠预览和详情区相对组头的缩进距离。
const toolBeatPadLeft float32 = 12

const toolPendingBorderOpacity float32 = 0.10

type toolMarkerShape int

const (
	toolMarkerHollow toolMarkerShape = iota
	toolMarkerSpinner
	toolMarkerCheck
	toolMarkerAlert
)

// toolVerbShimmerWidth 是动词扫光条自身的宽度（DIP）。
const toolVerbShimmerWidth float32 = 24

// toolShimmerBandLabel 是扫光条的无障碍名，仅用于测试与调试定位这条装饰层。
const toolShimmerBandLabel = "运行中的工具扫光"

const foldRunThreshold = 300

// toolOutputFadeHeight 是工具输出折叠遮罩高度。 // token-exempt: 渐隐覆盖固定的预览边缘
const toolOutputFadeHeight float32 = 28

// toolCallOldTextKeys 与 toolCallNewTextKeys 是编辑类工具输入里旧/新正文的候选字段名。
var (
	toolCallOldTextKeys = []string{
		"old_string", "oldstring", "oldtext", "old_text", "old_str", "oldstr", "old",
		"original", "target_content", "targetcontent",
	}
	toolCallNewTextKeys = []string{
		"new_string", "newstring", "newtext", "new_text", "new_str", "newstr", "new",
		"replacement", "replacementcontent", "replacement_content", "replacement_text",
		"new_content", "newcontent", "content", "file_text",
	}
)

// toolCategory 是工具组摘要中的工具类别。
type toolCategory int

const (
	toolCategoryOther toolCategory = iota
	toolCategoryCommand
	toolCategoryEdit
	toolCategoryRead
	toolCategorySearch
)

// readToolNames 与 searchToolNames 为读取与搜索类工具名（已转小写并去掉 _ 与 -）。
var readToolNames = map[string]bool{
	"read":              true,
	"readfile":          true,
	"fileread":          true,
	"readfiles":         true,
	"readtextfile":      true,
	"readmultiplefiles": true,
	"readfilecontent":   true,
	"getfilecontents":   true,
	"getfilecontent":    true,
	"view":              true,
	"viewfile":          true,
	"openfile":          true,
	"cat":               true,
}

var searchToolNames = map[string]bool{
	"grep":           true,
	"rg":             true,
	"ripgrep":        true,
	"grepsearch":     true,
	"search":         true,
	"searchfiles":    true,
	"filesearch":     true,
	"find":           true,
	"findfiles":      true,
	"glob":           true,
	"codebasesearch": true,
}

// toolNameKeyStripper 是 toolNameKey 的固定替换器：每帧要为每条工具消息归类，
// 现造一个 strings.Replacer 会连索引一起重建一次。
var toolNameKeyStripper = strings.NewReplacer("_", "", "-", "")

// toolGroupCounts 是工具组摘要中各类别的出现次数，供组头的图标计数与文字摘要共用。
type toolGroupCounts struct {
	thoughts, commands, edits, reads, searches, others, total int
}

// toolCallPathKeys 是工具输入中文件路径字段的候选名（统一做不区分大小写的匹配）。
var toolCallPathKeys = []string{
	"path", "file_path", "filepath", "target_file", "notebook_path", "absolutepath", "file",
}

// toolCallPatternKeys / toolCallSearchPathKeys 是搜索类工具的搜索词与搜索范围字段。
var (
	toolCallPatternKeys    = []string{"pattern", "query", "regex", "glob", "search", "search_term"}
	toolCallSearchPathKeys = []string{"path", "dir", "directory", "root", "cwd", "folder"}
)

// createToolNames 为「新建文件」类工具名（已转小写并去掉 _ 与 -），其余修改类工具算「编辑」。
var createToolNames = map[string]bool{
	"write":            true,
	"writefile":        true,
	"createfile":       true,
	"writetofile":      true,
	"newfile":          true,
	"create":           true,
	"addfile":          true,
	"writefiletext":    true,
	"writefilecontent": true,
}

// 工具补丁正文里的三种路径行：unified diff 的 +++ b/<path> 与 --- a/<path>、
// diff --git a/<p> b/<p>、apply_patch 的 *** Update/Add/Delete File: <path>。
var (
	toolPatchFileLineRe = regexp.MustCompile(`(?m)^(?:\+\+\+ b/|--- a/)([^\t\r\n]+)`)
	toolPatchGitLineRe  = regexp.MustCompile(`(?m)^diff --git a/\S+ b/([^\t\r\n]+)`)
	toolPatchHdrLineRe  = regexp.MustCompile(`(?m)^\*\*\* (?:Update|Add|Delete) File: ([^\r\n]+)`)
)

// toolGroupPreviewCount 是运行中的工具组折叠时预览的工具行条数。
const toolGroupPreviewCount = 3

// noticeKind 系统通知的类型，决定通知行的图标与配色。
type noticeKind int

const (
	noticeInfo     noticeKind = iota // 普通信息
	noticeWarning                    // 警告：重试、超时、额度、版本过旧等
	noticeError                      // 错误：失败、异常、无法完成
	noticeWorktree                   // 工作树 / 分支切换
	noticeModel                      // 模型 / 供应商切换
	noticeCompact                    // 上下文压缩
)

// noticeClassifyRules 按顺序判定通知类型的关键词表（按小写文本匹配）：
// 先错误，再警告，然后按主题分类，都没命中则为普通信息。
// 关键词对齐 mcode 后端实际写入的系统消息文本
// （见 internal/remote/memory_backend.go 与 internal/app/swarm_wiring.go 的 AppendNotice 调用）。
var noticeClassifyRules = []struct {
	kind     noticeKind
	keywords []string
}{
	{noticeError, []string{"错误", "失败", "异常", "无法", "未能", "error", "failed"}},
	{noticeWarning, []string{
		"重试", "重新发送", "超时", "额度", "中断", "警告", "过旧", "升级", "失效", "意外退出", "强制结束",
		"retry", "timeout", "quota", "interrupt", "warning",
	}},
	{noticeCompact, []string{"压缩", "compact"}},
	{noticeWorktree, []string{"工作树", "worktree", "分支", "branch", "checkout", "已检出"}},
	{noticeModel, []string{"模型", "model", "供应商", "provider"}},
}

// requestSpeedStats 是一轮里多次模型请求的输出速度统计（t/s）：min/max 是各样本的极值，
// avg 按 token 加权，n 是有效样本数。
type requestSpeedStats struct {
	min, max, avg float64
	n             int
}
