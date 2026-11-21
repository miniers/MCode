package mcodeui

import (
	"context"
	"strings"
	"sync"
	"time"
)

// stewardDefaultName 是总管的默认名：客户端没给出昵称（或客户端还不支持身份接口）时用它。
const stewardDefaultName = "总管"

// stewardName 返回句子里点名用的名字：昵称为空（或只有空白）时用默认名「总管」，否则用昵称原文。
func stewardName(nickname string) string {
	if name := strings.TrimSpace(nickname); name != "" {
		return name
	}
	return stewardDefaultName
}

// stewardLabel 返回侧栏行上的标签：没有昵称时只显示「总管」，有昵称时显示「昵称 · 总管」。
func stewardLabel(nickname string) string {
	name := strings.TrimSpace(nickname)
	if name == "" {
		return stewardDefaultName
	}
	return name + " · 总管"
}

// StewardClient 是总管会话的可选客户端能力：界面用它判断当前客户端能否打开总管会话，
// 与 AttentionClient 一样通过类型断言取用，旧客户端和测试替身可以不实现。
type StewardClient interface {
	// StewardThreadID 返回当前总管会话 ID；还没有创建或未启用总管时返回空串。
	StewardThreadID() string
	// OpenStewardThread 确保总管会话存在并返回其 ID；未启用总管时返回错误。
	OpenStewardThread(ctx context.Context) (string, error)
	// ResetStewardThread 归档当前总管会话并创建新的主会话。
	ResetStewardThread(ctx context.Context) (string, error)
}

// stewardEnabledClient 是总管启用状态的可选客户端能力：客户端支持总管但总管还没启用时，
// 侧栏仍留一行置灰入口，点一下去设置里开启；旧客户端不实现时按「状态未知」处理。
type stewardEnabledClient interface {
	// StewardEnabled 返回配置里是否已启用总管。
	StewardEnabled() bool
}

// stewardDecisionCountClient 是总管未答决策卡数量的可选客户端能力。
// 旧客户端不实现时，侧栏不显示决策徽标。
type stewardDecisionCountClient interface {
	StewardOpenDecisionCount(ctx context.Context) (int, error)
}

type stewardDecisionAnswerClient interface {
	AnswerStewardDecision(context.Context, string, string) error
}

// StewardDecisionItem 是总管未答决策卡的界面条目。
type StewardDecisionItem struct {
	ID          string
	TaskID      string
	ThreadID    string
	Question    string
	Context     string
	Options     []string
	Recommended string
	Status      string
	CreatedAt   time.Time
}

type stewardDecisionListClient interface {
	StewardOpenDecisions(ctx context.Context) ([]StewardDecisionItem, error)
}

// stewardIdentityClient 是总管身份的可选客户端能力：昵称与头像字符都可改。
// 按契约它返回昵称原文（可为空）与头像原文（可为空），兜底由界面这里做。
type stewardIdentityClient interface {
	StewardIdentity() (nickname, avatar string)
}

// normalizeStewardIdentity 规范化总管昵称与头像字符，头像为空时取显示名首字。
func normalizeStewardIdentity(nickname, avatar string) (string, string) {
	nickname = strings.TrimSpace(nickname)
	avatar = strings.TrimSpace(avatar)
	if avatar == "" {
		if runes := []rune(stewardName(nickname)); len(runes) > 0 {
			avatar = string(runes[0])
		}
	}
	return nickname, avatar
}

// stewardIdentity 返回总管的昵称原文（去首尾空白，可为空）与头像字符，供侧栏行、提示文案与胶囊使用。
// 渲染路径只读缓存并在缓存缺失或过期时异步刷新，不在渲染线程同步读取配置；
// 从未取过或客户端不支持身份接口时昵称为空，头像回落为 stewardName(nickname) 的首个 rune。
func (s *chatState) stewardIdentity() (string, string) {
	if s == nil || s.client == nil {
		return normalizeStewardIdentity("", "")
	}
	c, ok := s.client.(stewardIdentityClient)
	if !ok {
		return normalizeStewardIdentity("", "")
	}
	st := getStewardOpenState(s)
	if st == nil {
		return normalizeStewardIdentity("", "")
	}

	st.mu.Lock()
	nickname, avatar := st.nickname, st.avatar
	needRefresh := st.identityAt.IsZero() || time.Since(st.identityAt) >= stewardEnabledCacheTTL
	if needRefresh && !st.identityLoading {
		st.identityLoading = true
		st.mu.Unlock()
		go func() {
			nick, av := c.StewardIdentity()
			st.mu.Lock()
			st.nickname = nick
			st.avatar = av
			st.identityAt = time.Now()
			st.identityLoading = false
			st.mu.Unlock()
			s.invalidate()
		}()
	} else {
		st.mu.Unlock()
	}
	return normalizeStewardIdentity(nickname, avatar)
}

// stewardEnabledCacheTTL 是启用状态与身份的缓存时长：侧栏每帧都会问一次，
// 而客户端的实现可能要去磁盘读配置，不能每帧都读一次。
const stewardEnabledCacheTTL = 2 * time.Second

// stewardDecisionCountRefresh 是总管决策数的最小刷新间隔，避免渲染帧重复查库。
const stewardDecisionCountRefresh = time.Second

// stewardOpenState 是一个 chatState 上的总管会话开合状态。
// creating 防止重复点击并发创建多个会话；pending 是后台创建成功后等待 UI 线程消费的会话 ID。
type stewardOpenState struct {
	mu       sync.Mutex
	creating bool
	pending  string
	// 启用状态的短缓存：enabledAt 为零值表示还没取过；enabledLoading 防止并发重复刷新。
	enabledAt      time.Time
	enabled        bool
	enabledKnown   bool
	enabledLoading bool
	enabledGen     uint64
	// 身份（昵称与头像）的短缓存：identityAt 为零值表示还没取过；identityLoading 防止并发重复刷新。
	identityAt      time.Time
	nickname        string
	avatar          string
	identityLoading bool

	decisionCount        int
	decisionCountAt      time.Time
	decisionCountLoading bool
	decisionErrors       map[string]string
	openDecisions        []StewardDecisionItem
	decisionsLoading     bool
}

var (
	stewardOpenMu     sync.Mutex
	stewardOpenStates = make(map[*chatState]*stewardOpenState)
)

// getStewardOpenState 返回 chatState 对应的总管状态，首次访问时创建。
// 与 workspaceState 一样按 chatState 指针存放，不需要在 chatState 结构里加字段。
func getStewardOpenState(s *chatState) *stewardOpenState {
	if s == nil {
		return nil
	}
	stewardOpenMu.Lock()
	defer stewardOpenMu.Unlock()
	st, ok := stewardOpenStates[s]
	if !ok {
		st = &stewardOpenState{}
		stewardOpenStates[s] = st
	}
	return st
}

// stewardClient 返回实现了总管接口的客户端；旧客户端或测试替身返回 false。
func (s *chatState) stewardClient() (StewardClient, bool) {
	if s == nil || s.client == nil {
		return nil, false
	}
	c, ok := s.client.(StewardClient)
	return c, ok
}

// stewardEnabledState 返回客户端报告的启用状态；客户端不支持该接口或尚未完成首次异步拉取时 known 为 false，
// 调用方据此保持原来的渲染行为，不凭空下结论。
// 结果按 stewardEnabledCacheTTL 缓存：渲染路径只读缓存并在过期或首次访问时异步刷新，不在渲染线程同步读配置。
func (s *chatState) stewardEnabledState() (enabled, known bool) {
	if s == nil || s.client == nil {
		return false, false
	}
	c, ok := s.client.(stewardEnabledClient)
	if !ok {
		return false, false
	}
	st := getStewardOpenState(s)
	if st == nil {
		return false, false
	}

	st.mu.Lock()
	enabled, known = st.enabled, st.enabledKnown
	needRefresh := st.enabledAt.IsZero() || time.Since(st.enabledAt) >= stewardEnabledCacheTTL
	gen := st.enabledGen
	if needRefresh && !st.enabledLoading {
		st.enabledLoading = true
		st.mu.Unlock()
		go func() {
			en := c.StewardEnabled()
			st.mu.Lock()
			if st.enabledGen == gen {
				st.enabled = en
				st.enabledKnown = true
				st.enabledAt = time.Now()
			}
			st.enabledLoading = false
			st.mu.Unlock()
			s.invalidate()
		}()
	} else {
		st.mu.Unlock()
	}
	return enabled, known
}

// invalidateStewardEnabledState 丢掉某会话的启用状态缓存：设置页刚改过开关时调用，
// 否则侧栏要等缓存过期才会换掉「未启用」那一行。只清掉时间戳以触发下一次异步刷新，不清掉旧值。
// 调用方不得持有 stewardOpenState.mu。
func invalidateStewardEnabledState(s *chatState) {
	st := getStewardOpenState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	st.enabledAt = time.Time{}
	st.enabledGen++
	st.mu.Unlock()
}

// stewardThreadID 返回当前总管会话 ID，客户端不支持时返回空串。
// 侧栏每一行都会调用它，因此只做类型断言加一次接口调用，不碰 s.mu，避免渲染路径互相等待。
func (s *chatState) stewardThreadID() string {
	c, ok := s.stewardClient()
	if !ok {
		return ""
	}
	return c.StewardThreadID()
}

// refreshStewardDecisionCount 异步刷新未答决策卡数。渲染路径只负责按间隔发起一次查询，
// 查询完成后让界面重绘，因此不会在每帧同步访问存储。
func (s *chatState) refreshStewardDecisionCount() {
	if s == nil || s.client == nil {
		return
	}
	c, ok := s.client.(stewardDecisionCountClient)
	if !ok {
		return
	}
	st := getStewardOpenState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	if st.decisionCountLoading || (!st.decisionCountAt.IsZero() && time.Since(st.decisionCountAt) < stewardDecisionCountRefresh) {
		st.mu.Unlock()
		return
	}
	st.decisionCountLoading = true
	st.mu.Unlock()

	go func() {
		count, err := c.StewardOpenDecisionCount(context.Background())
		st.mu.Lock()
		st.decisionCountLoading = false
		st.decisionCountAt = time.Now()
		if err == nil {
			if count < 0 {
				count = 0
			}
			st.decisionCount = count
		}
		st.mu.Unlock()
		s.invalidate()
	}()
}

// stewardDecisionCount 返回缓存的未答决策卡数与接口支持状态。
func (s *chatState) stewardDecisionCount() (int, bool) {
	if s == nil || s.client == nil {
		return 0, false
	}
	if _, ok := s.client.(stewardDecisionCountClient); !ok {
		return 0, false
	}
	st := getStewardOpenState(s)
	if st == nil {
		return 0, true
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.decisionCount, true
}

// refreshStewardOpenDecisions 异步拉取未答总管决策卡列表并同步更新缓存与决策计数。
func refreshStewardOpenDecisions(s *chatState) {
	if s == nil || s.client == nil {
		return
	}
	c, ok := s.client.(stewardDecisionListClient)
	if !ok {
		return
	}
	st := getStewardOpenState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	if st.decisionsLoading {
		st.mu.Unlock()
		return
	}
	st.decisionsLoading = true
	st.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		items, err := c.StewardOpenDecisions(ctx)
		cancel()
		st.mu.Lock()
		st.decisionsLoading = false
		if err == nil {
			st.openDecisions = append([]StewardDecisionItem(nil), items...)
			st.decisionCount = len(items)
			st.decisionCountAt = time.Now()
		}
		st.mu.Unlock()
		s.invalidate()
	}()
}

// openStewardThread 打开总管会话：会话已存在时直接跳过去；还没有时先在后台创建，
// 创建完成后由 consumeStewardPendingThread 在渲染线程跳转（selectThread 只能在 UI 线程调用）。
// 已知总管未启用时打开设置；连续点击只会发起一次创建，其余点击直接忽略。
func (s *chatState) openStewardThread() {
	if _, ok := s.stewardClient(); !ok {
		return
	}
	enabled, known := s.stewardEnabledState()
	if !known {
		// 冷启动尚未完成异步预热时（如侧栏折叠或未渲染前直接触发动作），按需同步读取一次以保证跳转准确。
		if ec, ok := s.client.(stewardEnabledClient); ok {
			enabled = ec.StewardEnabled()
			known = true
			if st := getStewardOpenState(s); st != nil {
				st.mu.Lock()
				st.enabled = enabled
				st.enabledKnown = true
				st.enabledAt = time.Now()
				st.enabledGen++
				st.mu.Unlock()
			}
		}
	}
	if known && !enabled {
		openStewardSettings(s)
		return
	}
	if id := s.stewardThreadID(); id != "" {
		s.revealThread(id)
		s.invalidate()
		return
	}

	st := getStewardOpenState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	if st.creating {
		st.mu.Unlock()
		return
	}
	st.creating = true
	st.mu.Unlock()

	// 首次创建可能要几十秒，先给一句提示，免得看起来像没反应。
	nickname, _ := s.stewardIdentity()
	s.setToast("正在准备" + stewardName(nickname) + "会话…")

	go func() {
		client, ok := s.stewardClient()
		if !ok {
			st.mu.Lock()
			st.creating = false
			st.mu.Unlock()
			return
		}
		id, err := client.OpenStewardThread(context.Background())

		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()

		st.mu.Lock()
		st.creating = false
		if err == nil && id != "" && !closed {
			st.pending = id
		}
		st.mu.Unlock()

		if err != nil {
			if !closed {
				s.setToast(err.Error())
			}
			return
		}
		s.invalidate()
	}()
}

// resetStewardThread 在后台归档当前总管会话并创建新会话，完成后沿用 pending 机制跳转。
func (s *chatState) resetStewardThread() {
	client, ok := s.stewardClient()
	if !ok {
		return
	}
	st := getStewardOpenState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	if st.creating {
		st.mu.Unlock()
		return
	}
	st.creating = true
	st.mu.Unlock()

	nickname, _ := s.stewardIdentity()
	s.setToast("正在重开" + stewardName(nickname) + "会话…")
	go func() {
		id, err := client.ResetStewardThread(context.Background())
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()

		st.mu.Lock()
		st.creating = false
		if err == nil && id != "" && !closed {
			st.pending = id
		}
		st.mu.Unlock()

		if err != nil {
			if !closed {
				s.setToast(err.Error())
			}
			return
		}
		s.invalidate()
	}()
}

// takeStewardPendingThread 取出后台创建完成后待跳转的会话 ID，取出即清空。
// 只由 consumeStewardPendingThread 在渲染线程调用，跳转逻辑因此留在 UI 线程里。
func takeStewardPendingThread(s *chatState) string {
	st := getStewardOpenState(s)
	if st == nil {
		return ""
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	id := st.pending
	st.pending = ""
	return id
}

// consumeStewardPendingThread 消费后台创建好的总管会话并跳转过去。
// 每帧在渲染线程调用一次，不受侧栏折叠影响：侧栏折叠时 renderSidebarStewardSection 不执行，
// 把取出逻辑放在那里会让跳转一直推迟到侧栏展开之后。
func consumeStewardPendingThread(s *chatState) {
	id := takeStewardPendingThread(s)
	if id == "" {
		return
	}
	s.revealThread(id)
	s.invalidate()
}
