package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/egoist/mygo/ui"
	"mcode/internal/providers/agy/quota"
)

var usagePanelNow = time.Now

// usageStripSmallSize 是额度条里状态图标与加载转圈的边长，比 IconSM 更小以免撑高单行。
const usageStripSmallSize float32 = 10

// composerCreditsTTL 是 Composer 余额缓存的过期时间：过期后下一帧再在后台刷新。
const composerCreditsTTL = time.Minute

// composerCreditsEntry 是某个 provider 的一次余额读取结果；ok 为假表示后端不支持或还没读到数据。
type composerCreditsEntry struct {
	credits  ProviderCredits
	ok       bool
	loadedAt time.Time
}

// composerCreditsByChat 按 chatState 旁挂缓存 Composer 展示的提供方余额（chatState 字段不在本文件，
// 用旁挂表保存）。键是 chatState 指针，随会话对象存活；读写统一由 composerCreditsMu 保护。
var (
	composerCreditsMu     sync.Mutex
	composerCreditsByChat = make(map[*chatState]map[string]composerCreditsEntry)
)

// SetChatCreditsForProvider 为指定 chatState 与 provider 注入余额缓存（测试或外部注入用）。
func SetChatCreditsForProvider(s *chatState, provider string, credits ProviderCredits) {
	provider = strings.TrimSpace(provider)
	if s == nil || provider == "" {
		return
	}
	composerCreditsMu.Lock()
	if composerCreditsByChat[s] == nil {
		composerCreditsByChat[s] = make(map[string]composerCreditsEntry)
	}
	composerCreditsByChat[s][provider] = composerCreditsEntry{credits: credits, ok: true, loadedAt: time.Now()}
	composerCreditsMu.Unlock()
}

// getChatCreditsForProvider 读取 provider 的余额缓存；ok 为假表示没有可展示的余额。
func getChatCreditsForProvider(s *chatState, provider string) (ProviderCredits, bool) {
	provider = strings.TrimSpace(provider)
	if s == nil || provider == "" {
		return ProviderCredits{}, false
	}
	composerCreditsMu.Lock()
	defer composerCreditsMu.Unlock()
	entry, ok := composerCreditsByChat[s][provider]
	if !ok || !entry.ok {
		return ProviderCredits{}, false
	}
	return entry.credits, true
}

// loadComposerCreditsForProvider 后台加载 provider 的余额：同一 provider 一分钟内不重复请求。
// 后端未实现或不支持余额时不写入数据，因此这类 provider 不会出现在面板上。
func (s *chatState) loadComposerCreditsForProvider(provider string, force bool) {
	provider = strings.TrimSpace(provider)
	if s == nil || provider == "" || s.client == nil {
		return
	}
	now := time.Now()
	composerCreditsMu.Lock()
	entries := composerCreditsByChat[s]
	if entries == nil {
		entries = make(map[string]composerCreditsEntry)
		composerCreditsByChat[s] = entries
	}
	prev := entries[provider]
	if !force && !prev.loadedAt.IsZero() && now.Sub(prev.loadedAt) < composerCreditsTTL {
		composerCreditsMu.Unlock()
		return
	}
	// 先写入读取时间作为占位，避免同一 TTL 窗口内并发渲染重复发起请求。
	entries[provider] = composerCreditsEntry{credits: prev.credits, ok: prev.ok, loadedAt: now}
	composerCreditsMu.Unlock()

	go func() {
		credits, err := s.client.ProviderCredits(context.Background(), provider)
		composerCreditsMu.Lock()
		entries := composerCreditsByChat[s]
		if entries == nil {
			entries = make(map[string]composerCreditsEntry)
			composerCreditsByChat[s] = entries
		}
		entry := entries[provider]
		switch {
		case err == nil:
			entry = composerCreditsEntry{credits: credits, ok: true, loadedAt: time.Now()}
		case errors.Is(err, ErrNotImplemented):
			// 该 provider 没有余额接口：不展示，TTL 内不再请求。
			entry = composerCreditsEntry{loadedAt: time.Now()}
		default:
			// 读取失败：保留上一次的结果，TTL 过后再重试。
			entry.loadedAt = time.Now()
		}
		entries[provider] = entry
		composerCreditsMu.Unlock()
		s.invalidate()
	}()
}

// SetChatQuotaWindows 为指定 chatState 设置提供方额度窗口数据（支持外部或测试注入）。
func SetChatQuotaWindows(s *chatState, windows []ProviderQuotaWindow) {
	SetChatQuotaWindowsForProvider(s, "", windows)
}

// SetChatQuotaWindowsForProvider 为指定 provider 设置额度窗口缓存；provider 为空时
// 兼容旧行为，直接覆盖当前会话 provider 的缓存。
func SetChatQuotaWindowsForProvider(s *chatState, provider string, windows []ProviderQuotaWindow) {
	s.mu.Lock()
	provider = chatQuotaCacheKey(s, provider)
	if s.quotaWindowsByProvider == nil {
		s.quotaWindowsByProvider = make(map[string][]ProviderQuotaWindow)
	}
	if s.quotaErrs == nil {
		s.quotaErrs = make(map[string]string)
	}
	s.quotaWindowsByProvider[provider] = append([]ProviderQuotaWindow(nil), windows...)
	delete(s.quotaErrs, provider)
	if s.quotaLoadingProviders != nil {
		delete(s.quotaLoadingProviders, provider)
	}
	// 手动注入的数据视为已加载，渲染时不再触发请求、也不显示加载中。
	if provider != "" {
		if s.quotaLoadedProviders == nil {
			s.quotaLoadedProviders = make(map[string]time.Time)
		}
		s.quotaLoadedProviders[provider] = time.Now()
	}
	s.quotaProvider = provider
	s.quotaWindows = append([]ProviderQuotaWindow(nil), windows...)
	s.quotaErr = ""
	s.quotaLoading = false
	s.quotaLoadedAt = time.Now()
	s.mu.Unlock()
}

// chatQuotaCacheKey 把 provider 归一化为额度缓存键：为空时回退到当前会话 provider。
func chatQuotaCacheKey(s *chatState, provider string) string {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		provider = strings.TrimSpace(s.quotaProvider)
	}
	return provider
}

// threadQuotaProvider 返回会话对应的额度 key：
// pi 线程的模型 ID 形如「magpie-jp/auto/deepseek-v4.1-flash」（第一段是 pi provider id），
// 额度按 magpie 端点区分，所以取第一段并加「pi:」前缀；其余情况沿用会话 provider。
func threadQuotaProvider(th *Thread) string {
	if th == nil {
		return ""
	}
	if th.Provider == "pi" {
		if model := strings.TrimSpace(th.Model); model != "" {
			if head, _, ok := strings.Cut(model, "/"); ok && head != "" {
				return "pi:" + head
			}
		}
	}
	return th.Provider
}

// getChatQuotaWindows 获取指定 chatState 关联的提供方额度窗口列表。
func getChatQuotaWindows(s *chatState, th *Thread) []ProviderQuotaWindow {
	if s == nil || th == nil {
		return nil
	}
	return getChatQuotaWindowsForProvider(s, threadQuotaProvider(th))
}

// getChatQuotaWindowsForProvider 读取指定 provider 的额度窗口缓存。
func getChatQuotaWindowsForProvider(s *chatState, provider string) []ProviderQuotaWindow {
	if s == nil || strings.TrimSpace(provider) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ProviderQuotaWindow(nil), s.quotaWindowsByProvider[provider]...)
}

// composerQuotaLoadTimeout 是一次额度请求的最长等待时间：超时按失败/空态处理，
// 停止加载动画并允许重新发起请求。
const composerQuotaLoadTimeout = 15 * time.Second

// quotaLoadingKey 是额度请求发起时间旁挂表的键：会话对象 + provider。
type quotaLoadingKey struct {
	chat     *chatState
	provider string
}

// quotaLoadingSince 旁挂记录各 provider 额度请求的发起时间（chatState 字段不在本文件，
// 用旁挂表保存），用于判定请求是否已卡死。读写统一由 quotaLoadingSinceMu 保护。
var (
	quotaLoadingSinceMu sync.Mutex
	quotaLoadingSince   = make(map[quotaLoadingKey]time.Time)
)

// markQuotaLoadingSince 记录一次额度请求的发起时间。
func markQuotaLoadingSince(s *chatState, provider string) {
	quotaLoadingSinceMu.Lock()
	defer quotaLoadingSinceMu.Unlock()
	quotaLoadingSince[quotaLoadingKey{chat: s, provider: provider}] = time.Now()
}

// clearQuotaLoadingSince 清除额度请求的发起时间记录。
func clearQuotaLoadingSince(s *chatState, provider string) {
	quotaLoadingSinceMu.Lock()
	defer quotaLoadingSinceMu.Unlock()
	delete(quotaLoadingSince, quotaLoadingKey{chat: s, provider: provider})
}

// quotaLoadingExpired 返回某 provider 的额度请求是否已超过 composerQuotaLoadTimeout。
func quotaLoadingExpired(s *chatState, provider string) bool {
	quotaLoadingSinceMu.Lock()
	defer quotaLoadingSinceMu.Unlock()
	since, ok := quotaLoadingSince[quotaLoadingKey{chat: s, provider: provider}]
	return ok && time.Since(since) >= composerQuotaLoadTimeout
}

// quotaLoadedThread 记录各 provider 上次额度请求对应的会话：切换会话后「在用」账号
// 可能变了（多账号路由按会话绑定），缓存必须跟着会话失效，否则弹窗会沿用上一个会话的标记。
// 与 quotaLoadingSince 共用同一把锁与键类型。
var quotaLoadedThread = make(map[quotaLoadingKey]string)

// quotaLoadedThreadFor 返回某 provider 上次额度请求对应的会话 ID。
func quotaLoadedThreadFor(s *chatState, provider string) string {
	quotaLoadingSinceMu.Lock()
	defer quotaLoadingSinceMu.Unlock()
	return quotaLoadedThread[quotaLoadingKey{chat: s, provider: provider}]
}

// markQuotaLoadedThread 记录某 provider 本次额度请求对应的会话 ID。
func markQuotaLoadedThread(s *chatState, provider, threadID string) {
	quotaLoadingSinceMu.Lock()
	defer quotaLoadingSinceMu.Unlock()
	quotaLoadedThread[quotaLoadingKey{chat: s, provider: provider}] = threadID
}

// composerQuotaStatus 返回指定 provider 的加载中与错误状态。
func composerQuotaStatus(s *chatState, provider string) (loading bool, errText string) {
	s.mu.Lock()
	loading = s.quotaLoadingProviders[provider]
	errText = s.quotaErrs[provider]
	if errText == "" {
		// 手动刷新失败也要能在额度区域看到，不能被随后成功的读取掩盖。
		errText = s.quotaRefreshErrs[provider]
	}
	s.mu.Unlock()
	// 请求超过 15 秒仍未返回：不再显示加载动画，当作失败/空态渲染。
	if loading && quotaLoadingExpired(s, provider) {
		loading = false
	}
	return loading, errText
}

// loadComposerQuota 加载当前会话 provider 的额度窗口。
func (s *chatState) loadComposerQuota(th *Thread, force bool) {
	if th == nil {
		return
	}
	s.loadComposerQuotaForProvider(threadQuotaProvider(th), force)
}

// loadComposerQuotaForProvider 按 provider 加载额度窗口：同一 provider 一分钟内不重复请求。
// 没有 client（例如尚未接上后端的嵌入状态）时安全返回，额度条随之不渲染。
func (s *chatState) loadComposerQuotaForProvider(provider string, force bool) {
	provider = strings.TrimSpace(provider)
	if provider == "" || s == nil || s.client == nil {
		return
	}
	s.mu.Lock()
	if s.quotaLoadingProviders == nil {
		s.quotaLoadingProviders = make(map[string]bool)
	}
	if s.quotaLoadedProviders == nil {
		s.quotaLoadedProviders = make(map[string]time.Time)
	}
	if s.quotaErrs == nil {
		s.quotaErrs = make(map[string]string)
	}
	if s.quotaWindowsByProvider == nil {
		s.quotaWindowsByProvider = make(map[string][]ProviderQuotaWindow)
	}
	loadedAt := s.quotaLoadedProviders[provider]
	// 会话切走后「在用」账号可能变了：会话变了就当作缓存过期，重新拉一次。
	threadID := s.selectedThreadID
	if s.quotaLoadingProviders[provider] {
		// 超过 15 秒仍在加载视为卡死：清掉状态并允许重新发起请求。
		if !quotaLoadingExpired(s, provider) {
			s.mu.Unlock()
			return
		}
		delete(s.quotaLoadingProviders, provider)
		clearQuotaLoadingSince(s, provider)
	} else if !force && !loadedAt.IsZero() && time.Since(loadedAt) < time.Minute && quotaLoadedThreadFor(s, provider) == threadID {
		s.mu.Unlock()
		return
	}
	s.quotaLoadingProviders[provider] = true
	markQuotaLoadingSince(s, provider)
	markQuotaLoadedThread(s, provider, threadID)
	s.quotaProvider = provider
	s.quotaWindows = append([]ProviderQuotaWindow(nil), s.quotaWindowsByProvider[provider]...)
	s.quotaErr = ""
	s.quotaLoading = true
	s.mu.Unlock()
	go func() {
		// 15 秒超时：RPC 无论如何返回（成功/失败/超时）都会清掉加载状态，不会无限转圈。
		ctx, cancel := context.WithTimeout(context.Background(), composerQuotaLoadTimeout)
		defer cancel()
		// 带上会话 ID：agy 多账号按会话绑定账号，「在用」要标在当前会话真正用的那个号上。
		windows, err := s.client.ProviderQuota(WithQuotaThread(ctx, threadID), provider)
		s.mu.Lock()
		s.quotaWindowsByProvider[provider] = windows
		delete(s.quotaErrs, provider)
		if err != nil && !errors.Is(err, ErrNotImplemented) {
			s.quotaErrs[provider] = err.Error()
		}
		delete(s.quotaLoadingProviders, provider)
		clearQuotaLoadingSince(s, provider)
		s.quotaLoadedProviders[provider] = time.Now()
		s.quotaProvider = provider
		s.quotaWindows = append([]ProviderQuotaWindow(nil), windows...)
		s.quotaErr = s.quotaErrs[provider]
		s.quotaLoading = false
		s.quotaLoadedAt = time.Now()
		s.mu.Unlock()
		s.invalidate()
	}()
}

// composerQuotaRefreshTimeout 是手动刷新额度的超时：网关要重新向厂商读取，比普通读取慢得多。
const composerQuotaRefreshTimeout = 30 * time.Second

// composerQuotaRefreshing 返回某 provider 是否正在让网关重读额度。
func composerQuotaRefreshing(s *chatState, provider string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.quotaRefreshingProviders[provider]
}

// composerQuotaRefreshErr 返回某 provider 最近一次手动刷新的错误。
func composerQuotaRefreshErr(s *chatState, provider string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.quotaRefreshErrs[provider]
}

// refreshComposerQuotaForProvider 手动刷新某 provider 的额度：先让 Magpie 网关重新向厂商
// 读取（直连 provider 是空操作），再重新拉额度与余额。刷新期间按钮显示「刷新中…」并忽略
// 重复点击；刷新失败写进额度错误提示，但不阻止随后的读取。
func (s *chatState) refreshComposerQuotaForProvider(provider string) {
	provider = strings.TrimSpace(provider)
	if provider == "" || s == nil || s.client == nil {
		return
	}
	s.mu.Lock()
	if s.quotaRefreshingProviders == nil {
		s.quotaRefreshingProviders = make(map[string]bool)
	}
	if s.quotaRefreshingProviders[provider] {
		s.mu.Unlock()
		return
	}
	s.quotaRefreshingProviders[provider] = true
	if s.quotaRefreshErrs == nil {
		s.quotaRefreshErrs = make(map[string]string)
	}
	delete(s.quotaRefreshErrs, provider)
	s.mu.Unlock()
	s.invalidate()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), composerQuotaRefreshTimeout)
		defer cancel()
		err := s.client.RefreshProviderQuota(ctx, provider)
		s.mu.Lock()
		delete(s.quotaRefreshingProviders, provider)
		if err != nil && !errors.Is(err, ErrNotImplemented) {
			s.quotaRefreshErrs[provider] = "刷新额度失败: " + err.Error()
		}
		s.mu.Unlock()
		// 无论刷新成败都重新读取：成功时拿到网关刚写的卡片，失败时至少刷新一次缓存。
		s.loadComposerQuotaForProvider(provider, true)
		s.loadComposerCreditsForProvider(provider, true)
		s.invalidate()
	}()
}

// quotaUsageSnapshot 记录某个账户上一次观察到的已用百分比。
type quotaUsageSnapshot struct {
	used map[string]float64
}

// quotaAccountGroup 是某个账户下的一组额度窗口（窗口名已去掉账户前缀）。
type quotaAccountGroup struct {
	account  string
	active   bool
	disabled bool
	windows  []ProviderQuotaWindow
}

// quotaWindowAccount 返回窗口所属账户与去掉账户前缀后的窗口名：
// 优先采用后端给出的 Account 字段，否则按「 · 」切分 Label，首段含 @ 时视为账户。
func quotaWindowAccount(w ProviderQuotaWindow) (account, name string) {
	label := strings.TrimSpace(w.Label)
	if account = strings.TrimSpace(w.Account); account != "" {
		name = strings.TrimSpace(strings.TrimPrefix(label, account+" · "))
		return account, name
	}
	if key, rest, ok := strings.Cut(label, " · "); ok && strings.Contains(key, "@") {
		return strings.TrimSpace(key), strings.TrimSpace(rest)
	}
	return "", label
}

// groupQuotaWindowsByAccount 按账户分组（保持窗口原有顺序），并汇总后端的 Active / Disabled 标记。
func groupQuotaWindowsByAccount(windows []ProviderQuotaWindow) []quotaAccountGroup {
	var groups []quotaAccountGroup
	index := make(map[string]int)
	for _, w := range windows {
		account, name := quotaWindowAccount(w)
		i, ok := index[account]
		if !ok {
			i = len(groups)
			index[account] = i
			// 先假定整组停用，只要有一个窗口没被停用就改回启用。
			groups = append(groups, quotaAccountGroup{account: account, disabled: true})
		}
		// 整份复制再覆盖账号与窗口名，避免 ProviderQuotaWindow 新增字段时被漏掉。
		cp := w
		cp.Label = name
		cp.Account = account
		groups[i].windows = append(groups[i].windows, cp)
		if w.Active {
			groups[i].active = true
		}
		if !w.Disabled {
			groups[i].disabled = false
		}
	}
	return groups
}

// quotaSoleAccount 返回唯一未停用账户组的账号：只有一个账号时不存在「在用哪个」的猜测，
// 额度条与弹层都可以直接把它当作在用账号。账号名可能为空串（后端没给 Account、
// Label 里也没有邮箱的窗口组，如 workbuddy 的 Credits）；没有任何未停用组或有两组
// 以上时 ok 为 false，交给在用判定与最近应答线索。
func quotaSoleAccount(groups []quotaAccountGroup) (account string, ok bool) {
	for _, g := range groups {
		if g.disabled {
			continue
		}
		if ok {
			return "", false
		}
		account, ok = g.account, true
	}
	return account, ok
}

// observeQuotaAccounts 观察一次额度结果：与「上次看到」的快照比对，已用百分比有变化就刷新
// 该账户的额度变动时间（按 provider+账户记录）；同时记录后端的 Active 标记。
func (s *chatState) observeQuotaAccounts(provider string, windows []ProviderQuotaWindow) {
	if len(windows) == 0 {
		return
	}
	now := time.Now()
	used := make(map[string]map[string]float64)
	var activeAccount string
	for _, w := range windows {
		account, name := quotaWindowAccount(w)
		label := strings.TrimSpace(w.Label)
		if account == "" {
			account, name = label, label
		}
		if used[account] == nil {
			used[account] = make(map[string]float64)
		}
		used[account][name] = w.UsedPercent
		if w.Active && activeAccount == "" {
			activeAccount = account
		}
	}
	cv := &s.composerView
	if cv.quotaLastSeen == nil {
		cv.quotaLastSeen = make(map[string]quotaUsageSnapshot)
	}
	if cv.quotaChangedAt == nil {
		cv.quotaChangedAt = make(map[string]time.Time)
	}
	if cv.activeQuotaAccount == nil {
		cv.activeQuotaAccount = make(map[string]string)
	}
	for account, values := range used {
		key := provider + "\x00" + account
		prev, seen := cv.quotaLastSeen[key]
		if !seen || !maps.Equal(prev.used, values) {
			cv.quotaChangedAt[key] = now
		}
		cv.quotaLastSeen[key] = quotaUsageSnapshot{used: values}
	}
	if activeAccount != "" {
		cv.activeQuotaAccount[provider] = activeAccount
	}
}

// quotaInUseAccount 判定 provider 的在用账户：只有后端给出的唯一 Active 账户才算在用。
// 多个 Active 通常是旧后端只到 provider 粒度的标记，无法区分真正在用的那一张卡，视为不可信；
// Magpie 来源没有 Active 时返回空串，由展示层改用 quotaRecentAccount 标注「最近使用」或显示未知，
// 不按模型名、返回顺序或额度变动猜测。非 Magpie 来源（直连本机登录）保留原来的回退链：
// 观察到的 Active、额度最近变动时间；已停用（Disabled）的账户不参与判定。
func (s *chatState) quotaInUseAccount(provider string, windows []ProviderQuotaWindow) string {
	groups := groupQuotaWindowsByAccount(windows)
	var active []string
	for _, g := range groups {
		if g.account == "" || g.disabled || !g.active {
			continue
		}
		if !slices.Contains(active, g.account) {
			active = append(active, g.account)
		}
	}
	switch {
	case len(active) == 1:
		return active[0]
	case len(active) > 1:
		return ""
	}
	// Magpie 卡片只能证明网关里有这些账号，证明不了当前会话在用哪个。
	if s == nil || quotaWindowsFromMagpie(windows) {
		return ""
	}
	if active := s.composerView.activeQuotaAccount[provider]; active != "" {
		for _, g := range groups {
			if g.account == active && !g.disabled {
				return active
			}
		}
	}
	best := ""
	var bestAt time.Time
	for _, g := range groups {
		if g.account == "" || g.disabled {
			continue
		}
		at := s.composerView.quotaChangedAt[provider+"\x00"+g.account]
		if at.IsZero() {
			continue
		}
		if best == "" || at.After(bestAt) {
			best, bestAt = g.account, at
		}
	}
	return best
}

// quotaRecentAccount 返回 Magpie 记录的最近应答账户，作为「在用」未知时的展示回退：
// 优先 Last 为 true 的账户组（多组时取 LastServedAt 最新的一组），没有 Last 时取
// LastServedAt 最新的一组；完全没有应答时间线索时返回空串。已停用的账户不参与。
func quotaRecentAccount(windows []ProviderQuotaWindow) string {
	lastAccount, anyAccount := "", ""
	var lastAt, anyAt time.Time
	for _, g := range groupQuotaWindowsByAccount(windows) {
		if g.account == "" || g.disabled {
			continue
		}
		groupLast := false
		var groupAt time.Time
		for _, w := range g.windows {
			if w.Last {
				groupLast = true
			}
			if w.LastServedAt.After(groupAt) {
				groupAt = w.LastServedAt
			}
		}
		if groupLast && (lastAccount == "" || groupAt.After(lastAt)) {
			lastAccount, lastAt = g.account, groupAt
		}
		if !groupAt.IsZero() && (anyAccount == "" || groupAt.After(anyAt)) {
			anyAccount, anyAt = g.account, groupAt
		}
	}
	if lastAccount != "" {
		return lastAccount
	}
	return anyAccount
}

// formatTokenCount 将 Token 数量格式化为紧凑字符串（如 '76k', '1.2M', '950'）。
func formatTokenCount(n int64) string {
	if n < 0 {
		return "-" + formatTokenCount(-n)
	}
	compact := func(val float64, suffix string) string {
		s := fmt.Sprintf("%.1f", val)
		s = strings.TrimSuffix(s, ".0")
		return s + suffix
	}
	if n >= 1_000_000_000 {
		return compact(float64(n)/1_000_000_000.0, "B")
	}
	if n >= 1_000_000 {
		return compact(float64(n)/1_000_000.0, "M")
	}
	if n >= 1_000 {
		return compact(float64(n)/1_000.0, "k")
	}
	return strconv.FormatInt(n, 10)
}

// quotaGroupPlan 返回账户组里第一个非空的套餐名。
func quotaGroupPlan(windows []ProviderQuotaWindow) string {
	for _, w := range windows {
		if plan := strings.TrimSpace(w.Plan); plan != "" {
			return plan
		}
	}
	return ""
}

// quotaGroupReadAt 返回账户组里第一个已知的数据读取时间；全部未知时为零值。
func quotaGroupReadAt(windows []ProviderQuotaWindow) time.Time {
	for _, w := range windows {
		if !w.ReadAt.IsZero() {
			return w.ReadAt
		}
	}
	return time.Time{}
}

// formatQuotaReadAge 把数据在来源端的读取时间格式化为相对时间（如「3 分钟前」）。
func formatQuotaReadAge(now, readAt time.Time) string {
	if readAt.IsZero() {
		return ""
	}
	if now.Before(readAt) {
		return "刚刚"
	}
	age := now.Sub(readAt)
	switch {
	case age < time.Minute:
		return "刚刚"
	case age < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(age.Hours()))
	default:
		return readAt.Local().Format("1月2日 15:04")
	}
}

// formatQuotaResetCountdown 格式化额度重置倒计时（如 '2 时 30 分后重置'、'45 分后重置'、'即将重置'）。
func formatQuotaResetCountdown(now, resetsAt time.Time) string {
	if resetsAt.IsZero() {
		return ""
	}
	if !resetsAt.After(now) {
		return "即将重置"
	}

	loc := now.Location()
	if loc == nil {
		loc = time.Local
	}
	rAt := resetsAt.In(loc)
	diff := rAt.Sub(now)

	if diff < 24*time.Hour {
		totalSeconds := int64(diff.Seconds())
		hours := int(totalSeconds / 3600)
		minutes := int((totalSeconds % 3600) / 60)
		if hours > 0 && minutes > 0 {
			return fmt.Sprintf("%d 时 %d 分后重置", hours, minutes)
		}
		if hours > 0 {
			return fmt.Sprintf("%d 小时后重置", hours)
		}
		if minutes < 1 {
			minutes = 1
		}
		return fmt.Sprintf("%d 分后重置", minutes)
	}

	if diff < 7*24*time.Hour {
		weekdays := [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}
		return fmt.Sprintf("%s %s 重置", weekdays[rAt.Weekday()], rAt.Format("15:04"))
	}

	return rAt.Format("1月2日") + " 重置"
}

// creditsAmountColor 返回余额金额文本颜色：余额不足（Depleted）时用错误色，否则主文本色。
func creditsAmountColor(c *ui.Context, credits ProviderCredits) ui.Color {
	if credits.Depleted() {
		return ThemeDanger(c)
	}
	return ThemePrimaryText(c)
}

// renderComposerCreditsRow 渲染一行预付费余额：金额与设置页余额面板同格式（AmountText 优先，
// 缺失时用「金额 + 币种」），余额不足时金额标红并跟一个「余额不足」标记。
func renderComposerCreditsRow(c *ui.Context, key string, credits ProviderCredits) {
	amount := strings.TrimSpace(credits.AmountText)
	if amount == "" {
		amount = strings.TrimSpace(fmt.Sprintf("%.2f %s", credits.Total, credits.Currency))
	}
	depleted := credits.Depleted()
	ui.Row(c.Key(key)).
		FillWidth().
		AlignItems(ui.Center).
		Gap(Space3).
		Children(func() {
			ui.Text(c, "余额").
				FontSize(TypeFootnote).
				TextColor(ThemeSecondaryText(c))
			ui.Box(c.Key(key + "-spacer")).Grow(1)
			ui.Text(c, amount).
				FontSize(TypeFootnote).
				FontWeight(500).
				TextColor(creditsAmountColor(c, credits)).
				SingleLine()
			if depleted {
				ui.Text(c, "余额不足").
					FontSize(TypeCaption).
					TextColor(ThemeDanger(c)).
					SingleLine()
			}
		})
}

// formatTurnCostUSD 把单轮费用格式化成面板里的一行文本，金额保留 4 位小数；
// 金额不大于 0（provider 未上报）时返回空串，面板不显示费用行。
func formatTurnCostUSD(cost float64) string {
	if cost <= 0 {
		return ""
	}
	return fmt.Sprintf("本轮费用 $%.4f", cost)
}

// renderUsagePanel 渲染上下文用量与配额分析浮窗内容（卡片外观由 PopoverBase 的外层面板提供）。
func renderUsagePanel(c *ui.Context, s *chatState, th *Thread) {
	s.loadComposerQuota(th, false)
	threadProvider := ""
	quotaProvider := ""
	if th != nil {
		threadProvider = th.Provider
		quotaProvider = threadQuotaProvider(th)
	}
	popoverCardShadow(ui.Column(c.Key("composer-usage-panel")).
		Width(360).
		Padding(Space4, Space5, Space4, Space5).
		Gap(Space4).
		Radius(RadiusMD).
		Background(ThemeSurface(c)).
		Border(Hairline, ThemeBorder(c)).
		Children(func() {
			// 1. 上下文窗口区域
			used, limit, ok := currentThreadContextTokens(s, th)
			hasContextData := ok && limit > 0
			s.mu.Lock()
			total := s.threadUsage.Total
			latestTurn, hasLatestTurn := latestTurnUsageLocked(s.threadUsage)
			s.mu.Unlock()
			// 本轮费用取最新一轮的 CostUSD；provider 未上报时保持 0，面板不显示费用行。
			costUSD := 0.0
			if hasLatestTurn {
				costUSD = latestTurn.CostUSD
			}

			ui.Column(c.Key("composer-usage-context-section")).
				FillWidth().
				Gap(Space3).
				Children(func() {
					// 标题行与数字
					ui.Row(c.Key("composer-usage-context-header")).
						FillWidth().
						AlignItems(ui.Center).
						Children(func() {
							ui.Text(c, "上下文窗口").
								FontSize(TypeBody).
								FontWeight(600).
								TextColor(ThemePrimaryText(c))

							ui.Box(c.Key("composer-usage-context-spacer")).Grow(1)

							if hasContextData {
								pct := int(math.Round(contextUsagePercent(used, limit)))
								txt := fmt.Sprintf("%s / %s (%d%%)", formatTokenCount(used), formatTokenCount(limit), pct)
								ui.Text(c, txt).
									FontFeatures("tnum").
									FontSize(TypeFootnote).
									TextColor(ThemeSecondaryText(c))
							}
						})

					if hasContextData {
						fraction := contextUsageFraction(used, limit)
						clamped := math.Max(0, math.Min(1.0, fraction))
						ratio := float32(clamped)
						barColor := ThemeAccent(c)
						if fraction > 0.85 {
							barColor = ThemeWarning(c)
						}
						if fraction > 0.95 {
							barColor = ThemeDanger(c)
						}

						ui.Row(c.Key("composer-usage-context-bar")).
							FillWidth().
							Height(Space2).
							Radius(RadiusPill).
							Background(ThemeOverlay(c, 0.1)).
							Clip().
							Children(func() {
								if ratio > 0 {
									ui.Box(c.Key("composer-usage-context-bar-fill")).
										FillHeight().
										Width(0).
										Grow(ratio).
										Radius(RadiusPill).
										Background(barColor)
								}
								if ratio < 1 {
									ui.Box(c.Key("composer-usage-context-bar-empty")).
										Width(0).
										Grow(1 - ratio)
								}
							})
					} else {
						ui.Text(c, "暂无上下文窗口数据").
							FontSize(TypeFootnote).
							TextColor(ThemeSecondaryText(c))
					}
					if total.Input > 0 || total.Output > 0 || total.Cached > 0 {
						ui.Text(c, fmt.Sprintf("输入 %s · 输出 %s · 缓存命中 %s",
							formatTokenCount(total.Input), formatTokenCount(total.Output), formatTokenCount(total.Cached))).
							FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
					}
					if costLine := formatTurnCostUSD(costUSD); costLine != "" {
						ui.Text(c, costLine).
							FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
					}
				})

			if view := splitSessionViewFor(s, th); view.Show {
				color := ThemePrimaryText(c)
				button := ui.ButtonBase(c.Key("composer-usage-split")).
					Label("拆分会话").
					FillWidth().
					Padding(Space2, Space3).
					Radius(RadiusSM).
					Justify(ui.Start).
					AlignItems(ui.Center)
				if view.Disabled {
					color = ThemeSecondaryText(c)
					button.Disabled(true).Tooltip("等这一轮结束再拆分")
				} else {
					if button.Hovered() {
						button.Background(ThemeHover(c))
					}
					button.OnClick(func() {
						s.composerView.usageOpen = false
						splitStewardSession(s, th.ID)
					})
				}
				button.Children(func() {
					ui.Row(c.Key("composer-usage-split-row")).Gap(Space3).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, IconColumns3()).Size(IconSM, IconSM).TextColor(color)
						ui.Column(c.Key("composer-usage-split-text")).Gap(Space1).Children(func() {
							ui.Text(c, "拆分会话").FontSize(TypeBody).TextColor(color)
							ui.Text(c, "把当前会话拆成新会话，释放上下文").FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
						})
					})
				})
			}

			// 预付费余额：仅当 provider 支持余额接口时出现（如 DeepSeek、Z.ai）。
			s.loadComposerCreditsForProvider(threadProvider, false)
			if credits, ok := getChatCreditsForProvider(s, threadProvider); ok {
				ui.Row(c.Key("composer-usage-credits-divider")).
					FillWidth().
					Height(Hairline).
					Background(ThemeDivider(c))
				renderComposerCreditsRow(c, "composer-usage-credits", credits)
			}

			// 额度状态：加载中显示进度，否则按语义状态展示（未登录/不支持灰色说明，出错警告色并保留原错误，
			// 数据过期只在行尾加提示）。
			quotaLoading, _ := composerQuotaStatus(s, quotaProvider)
			if quotaLoading {
				ui.Text(c, "正在加载额度…").FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
			} else {
				renderUsageStatusLine(c, chatUsageStatus(s, quotaProvider, usagePanelNow()), TypeFootnote)
			}

			quotaWindows := getChatQuotaWindows(s, th)
			if len(quotaWindows) > 0 {
				ui.Row(c.Key("composer-usage-quota-divider")).
					FillWidth().
					Height(Hairline).
					Background(ThemeDivider(c))

				renderComposerQuotaSection(c, s, quotaProvider, quotaWindows)
			}
		}))
}

// magpieQuotaProviderName 返回 Magpie 供应商展示名：
// claude→Claude、codex→Codex、cline→Cline、zai→Z.ai、droid→Droid、gemini→Gemini，
// workbuddy→WorkBuddy，commandcode 与以 commandcode- 开头的 id→Command Code，
// 其余非空 provider 首字母大写。
func magpieQuotaProviderName(provider string) string {
	lower := strings.ToLower(strings.TrimSpace(provider))
	switch {
	case lower == "claude":
		return "Claude"
	case lower == "codex":
		return "Codex"
	case lower == "cline":
		return "Cline"
	case lower == "zai":
		return "Z.ai"
	case lower == "droid":
		return "Droid"
	case lower == "gemini":
		return "Gemini"
	case lower == "workbuddy":
		return "WorkBuddy"
	case lower == "commandcode" || strings.HasPrefix(lower, "commandcode-"):
		return "Command Code"
	default:
		p := strings.TrimSpace(provider)
		if p == "" {
			return ""
		}
		runes := []rune(p)
		runes[0] = unicode.ToUpper(runes[0])
		return string(runes)
	}
}

// quotaAccountDisplayName 去掉账号开头的 provider + " · " 前缀（大小写不敏感），
// 去掉后为空时返回原值。
func quotaAccountDisplayName(account, provider string) string {
	raw := strings.TrimSpace(account)
	p := strings.TrimSpace(provider)
	if p != "" {
		prefix := p + " · "
		if len(raw) >= len(prefix) && strings.EqualFold(raw[:len(prefix)], prefix) {
			rest := strings.TrimSpace(raw[len(prefix):])
			if rest != "" {
				return rest
			}
			return account
		}
	}
	return account
}

// quotaProviderGroup 表示按供应商分组后的额度窗口列表。
type quotaProviderGroup struct {
	provider string
	windows  []ProviderQuotaWindow
}

// groupQuotaWindowsByProvider 按供应商对额度窗口分组：保持 Provider 首次出现的顺序及组内窗口原顺序。
// 只有当 windows 中至少一个窗口 Source == "magpie" 且 Provider 非空时才按供应商分组；
// 否则返回 nil（调用方走单供应商/直连默认逻辑）。
func groupQuotaWindowsByProvider(windows []ProviderQuotaWindow) []quotaProviderGroup {
	hasMagpie := false
	for _, w := range windows {
		if w.Source == "magpie" && strings.TrimSpace(w.Provider) != "" {
			hasMagpie = true
			break
		}
	}
	if !hasMagpie {
		return nil
	}

	var groups []quotaProviderGroup
	index := make(map[string]int)
	for _, w := range windows {
		p := w.Provider
		i, ok := index[p]
		if !ok {
			i = len(groups)
			index[p] = i
			groups = append(groups, quotaProviderGroup{provider: p})
		}
		groups[i].windows = append(groups[i].windows, w)
	}
	return groups
}

// composerQuotaGroupModels 将浮窗额度转换为托盘共用的展示模型。
func composerQuotaGroupModels(provider, title string, windows []ProviderQuotaWindow, activeAccount, recentAccount string, now time.Time) []trayQuotaGroupModel {
	sole, soleKnown := quotaSoleAccount(groupQuotaWindowsByAccount(windows))
	if soleKnown {
		activeAccount, recentAccount = sole, ""
	}
	if activeAccount == recentAccount {
		recentAccount = ""
	}
	providerGroups := groupQuotaWindowsByProvider(windows)
	var groups []TrayQuotaGroup
	if len(providerGroups) == 0 {
		groups = append(groups, TrayQuotaGroup{Provider: provider, Title: title, Windows: windows})
	} else {
		for _, group := range providerGroups {
			groups = append(groups, TrayQuotaGroup{Provider: "magpie:" + group.provider, Title: magpieQuotaProviderName(group.provider), Windows: group.windows})
		}
	}
	models := buildTrayQuotaModel(groups, time.Time{}, now, false).Groups
	for i := range models {
		for j := range models[i].Accounts {
			account := &models[i].Accounts[j]
			account.DisplayName = strings.TrimSuffix(account.DisplayName, " · 最近使用")
			account.DisplayName = quotaAccountDisplayName(account.DisplayName, strings.TrimPrefix(models[i].Provider, "magpie:"))
			for _, window := range groups[i].Windows {
				if window.Account == account.Account && window.Provider != "" {
					account.DisplayName = quotaAccountDisplayName(account.DisplayName, window.Provider)
				}
			}
			if len(providerGroups) == 0 && strings.Contains(strings.ToLower(provider), "magpie") {
				if prefix, _, ok := strings.Cut(account.Account, " · "); ok {
					account.DisplayName = quotaAccountDisplayName(account.DisplayName, prefix)
				}
			}
			account.IsActive = !account.IsDisabled && account.Account == activeAccount && (activeAccount != "" || soleKnown)
			account.IsRecent = !account.IsDisabled && recentAccount != "" && account.Account == recentAccount
			if account.IsRecent {
				account.DisplayName += " · 最近使用"
			}
		}
		slices.SortStableFunc(models[i].Accounts, func(a, b trayQuotaAccountModel) int {
			rank := func(account trayQuotaAccountModel) int {
				if account.IsActive {
					return 0
				}
				if account.IsRecent {
					return 1
				}
				return 2
			}
			return rank(a) - rank(b)
		})
	}
	slices.SortStableFunc(models, func(a, b trayQuotaGroupModel) int {
		rank := func(group trayQuotaGroupModel) int {
			result := 2
			for _, account := range group.Accounts {
				if account.IsActive {
					return 0
				}
				if account.IsRecent {
					result = 1
				}
			}
			return result
		}
		return rank(a) - rank(b)
	})
	return models
}

// 额度弹层的列宽：窗口名与百分比定宽，倒计时占剩余宽度，进度条定宽。
// 列宽按弹层的固定字号量出，四列在每一行（含第二个账号）都从同一位置开始，
// 倒计时与百分比因此不会被挤成省略号。
const (
	quotaPopoverWidth        float32 = 440
	quotaPopoverNameWidth    float32 = 88
	quotaPopoverBarWidth     float32 = 80
	quotaPopoverPercentWidth float32 = 80
)

// renderComposerQuotaSection 渲染额度窗口列表：按账户分组，组内每个窗口一行：
// 窗口名（定宽单行截断）、重置倒计时（紧贴进度条右侧）、定宽进度条、已用/剩余百分比（可点击切换）。
// 组内各窗口的倒计时文案一致时只在该组标题行右侧显示一次；没有账号组标题时显示在区标题行右侧。
// 唯一在用的账户组置顶并标「在用」（只有一个未停用账户组时直接算在用）；
// 没有在用账户时最近应答的账户组置顶并标「最近使用」；两者都没有时在顶部提示账号未知，随后列出全部账户。
// 若包含 Magpie 多供应商窗口，则先按供应商分组（彩色图标 + 供应商名），组内账号与窗口缩进展示。
func renderComposerQuotaSection(c *ui.Context, s *chatState, provider string, quotaWindows []ProviderQuotaWindow) {
	activeAccount, recentAccount := "", ""
	if s != nil && len(quotaWindows) > 0 {
		activeAccount = s.quotaInUseAccount(provider, quotaWindows)
		recentAccount = quotaRecentAccount(quotaWindows)
	}
	allGroups := groupQuotaWindowsByAccount(quotaWindows)
	// 只有一个未停用账户组时不存在「在用哪个」的猜测：单账号 provider 与只有一张卡、
	// 后端没给 Active 的 Magpie 卡片都直接视为在用（账号名可能为空串）。
	soleAccount, soleKnown := quotaSoleAccount(allGroups)
	if soleKnown {
		activeAccount, recentAccount = soleAccount, ""
	}
	if recentAccount == activeAccount {
		recentAccount = ""
	}

	now := usagePanelNow()
	stale := chatUsageStatus(s, provider, now).Stale()
	// 没有账号组标题时（单账号 provider 且账号名为空），共同倒计时显示在区标题行右侧。
	sectionShared := ""
	if len(allGroups) == 1 && allGroups[0].account == "" {
		sectionShared = quotaGroupSharedCountdown(allGroups[0].windows, now)
	}

	providerGroups := groupQuotaWindowsByProvider(quotaWindows)
	if len(providerGroups) > 0 {
		// 含 activeAccount 的供应商组排第一，含 recentAccount 的排第二，其余保持原顺序。
		groupHasAccount := func(pg quotaProviderGroup, target string) bool {
			if target == "" {
				return false
			}
			for _, w := range pg.windows {
				acc, _ := quotaWindowAccount(w)
				if acc == target {
					return true
				}
			}
			return false
		}
		slices.SortStableFunc(providerGroups, func(a, b quotaProviderGroup) int {
			rank := func(pg quotaProviderGroup) int {
				switch {
				case activeAccount != "" && groupHasAccount(pg, activeAccount):
					return 0
				case recentAccount != "" && groupHasAccount(pg, recentAccount):
					return 1
				default:
					return 2
				}
			}
			return rank(a) - rank(b)
		})
	}

	ui.Column(c.Key("composer-usage-quota-section")).
		FillWidth().
		Gap(Space2).
		Children(func() {
			ui.Row(c.Key("composer-usage-quota-header")).
				FillWidth().
				AlignItems(ui.Center).
				Gap(Space2).
				Children(func() {
					ui.Text(c, "额度").
						FontSize(FontSizeProjectName).
						FontWeight(500).
						TextColor(ThemeSecondaryText(c))
					ui.Spacer(c)
					if text := quotaCountdownHeading(sectionShared, stale); text != "" {
						ui.Text(c, text).
							FontSize(FontSizeProjectName).
							FontFeatures("tnum").
							TextColor(ThemeSecondaryText(c)).
							SingleLine()
					}
				})

			if !soleKnown && activeAccount == "" && recentAccount == "" && quotaSectionHasNamedAccount(allGroups) {
				ui.Text(c, "本会话账号未知，以下为全部账号").
					FontSize(FontSizeProjectName).
					TextColor(ThemeSecondaryText(c)).
					SingleLine()
			}

			idx := 0
			renderAccountGroups := func(pKey, prov string, wins []ProviderQuotaWindow, indent float32, defaultShared string) {
				groups := groupQuotaWindowsByAccount(wins)
				slices.SortStableFunc(groups, func(a, b quotaAccountGroup) int {
					rank := func(account string) int {
						switch {
						case activeAccount != "" && account == activeAccount:
							return 0
						case recentAccount != "" && account == recentAccount:
							return 1
						default:
							return 2
						}
					}
					return rank(a.account) - rank(b.account)
				})

				groupIdx := 0
				for _, group := range groups {
					shared := defaultShared
					if group.account != "" {
						if groupIdx > 0 {
							// 账户组之间留白，组内靠外层 Gap 自然分隔。
							gapKey := fmt.Sprintf("composer-usage-quota-group-gap-%d", groupIdx)
							if pKey != "" {
								gapKey = fmt.Sprintf("composer-usage-quota-group-gap-%s-%d", pKey, groupIdx)
							}
							ui.Box(c.Key(gapKey)).Height(Space3)
						}
						groupIdx++
						shared = quotaGroupSharedCountdown(group.windows, now)
						title := quotaAccountDisplayName(group.account, prov)
						// 组标题带上该账号的套餐与数据时效：Magpie 卡片自带的套餐不能与本机登录套餐混用。
						if plan := quotaGroupPlan(group.windows); plan != "" {
							title += " · " + plan
						}
						switch {
						case activeAccount != "" && group.account == activeAccount:
							title += " · 在用"
						case recentAccount != "" && group.account == recentAccount:
							title += " · 最近使用"
						case group.disabled:
							title += " · 已停止"
						}
						// 组标题一行：左侧账号、套餐与数据时效（时效改入悬停提示），右侧该组共同的倒计时。
						headerKey := fmt.Sprintf("composer-usage-quota-group-%d-header", groupIdx)
						if pKey != "" {
							headerKey = fmt.Sprintf("composer-usage-quota-group-%s-%d-header", pKey, groupIdx)
						}
						headerRow := ui.Row(c.Key(headerKey)).
							FillWidth().
							AlignItems(ui.Center).
							Gap(Space3)
						if indent > 0 {
							headerRow.Padding(0, 0, 0, indent)
						}
						headerRow.Children(func() {
							titleText := ui.Text(c, title).
								FontSize(FontSizeCallout).
								TextColor(ThemeSecondaryText(c)).
								SingleLine().
								Grow(1)
							if readAt := quotaGroupReadAt(group.windows); !readAt.IsZero() {
								titleText.Tooltip("读取于 " + formatQuotaReadAge(now, readAt))
							}
							if text := quotaCountdownHeading(shared, stale); text != "" {
								ui.Text(c, text).
									FontSize(FontSizeProjectName).
									FontFeatures("tnum").
									TextColor(ThemeSecondaryText(c)).
									SingleLine()
							}
						})
					}
					for _, win := range group.windows {
						renderComposerQuotaWindowRow(c, s, idx, win, now, stale, shared, indent)
						idx++
					}
				}
			}

			if len(providerGroups) > 0 {
				for pIdx, pg := range providerGroups {
					if pIdx > 0 {
						ui.Box(c.Key(fmt.Sprintf("composer-usage-quota-provider-gap-%d", pIdx))).Height(Space4)
					}
					ui.Row(c.Key(fmt.Sprintf("composer-usage-quota-provider-%d-header", pIdx))).
						FillWidth().
						AlignItems(ui.Center).
						Gap(Space2).
						Children(func() {
							renderProviderIconColor(c, pg.provider, IconMD, ThemeSecondaryText(c))
							ui.Text(c, magpieQuotaProviderName(pg.provider)).
								FontSize(FontSizeCallout).
								FontWeight(600).
								TextColor(ThemePrimaryText(c))
						})
					renderAccountGroups(fmt.Sprintf("%d", pIdx), pg.provider, pg.windows, 24, "")
				}
			} else {
				renderAccountGroups("", provider, quotaWindows, 0, sectionShared)
			}
		})
}

// quotaSectionHasNamedAccount 判断分组里是否有带名字的账户；全都没有名字时
// 「以下为全部账号」这样的提示没有意义。
func quotaSectionHasNamedAccount(groups []quotaAccountGroup) bool {
	for _, g := range groups {
		if g.account != "" {
			return true
		}
	}
	return false
}

// quotaGroupSharedCountdown 返回账户组内各窗口共同的倒计时文案：组内所有带重置时间的窗口
// 文案都相同时返回该文案；文案不同或没有任何窗口带重置时间时返回空串（由各窗口行自己显示）。
func quotaGroupSharedCountdown(windows []ProviderQuotaWindow, now time.Time) string {
	shared := ""
	for _, w := range windows {
		if w.ResetsAt.IsZero() {
			continue
		}
		text := formatQuotaResetCountdown(now, w.ResetsAt)
		if shared == "" {
			shared = text
			continue
		}
		if text != shared {
			return ""
		}
	}
	return shared
}

// quotaCountdownHeading 返回标题行右侧的倒计时文案：只有组内倒计时一致时才显示，
// 数据过期时追加过期提示；没有共同倒计时时返回空串（过期提示仍留在各窗口行）。
func quotaCountdownHeading(shared string, stale bool) string {
	if shared == "" {
		return ""
	}
	if stale {
		return shared + " " + usageStaleSuffix
	}
	return shared
}

// quotaWindowCountdown 返回窗口行内要显示的倒计时：组标题已经显示过共同倒计时时不重复，
// stale 为真时在倒计时后追加过期提示（没有倒计时时只显示过期提示）。
func quotaWindowCountdown(now time.Time, win ProviderQuotaWindow, stale bool, shared string) string {
	countdown := formatQuotaResetCountdown(now, win.ResetsAt)
	if shared != "" && countdown == shared {
		return ""
	}
	if !stale {
		return countdown
	}
	if countdown == "" {
		return usageStaleSuffix
	}
	return countdown + " " + usageStaleSuffix
}

// quotaPopoverWindowName 返回弹窗行里的窗口名：能认出周期的账户级窗口用周期短名（5h、周、日、月），
// 认不出周期或只限某个模型的窗口（如 Weekly · Opus）沿用原始名称，避免两个周窗口都显示成「周」。
func quotaPopoverWindowName(win ProviderQuotaWindow) string {
	key, _, ok := quotaStripWindowKey(win)
	if ok && quotaWindowScopeRank(win.Label) == 0 {
		return key
	}
	return win.Label
}

// renderComposerQuotaWindowRow 渲染弹窗内单个额度窗口行：窗口名用周期短名（认不出周期或
// 只限模型的窗口保留原名）单行截断。行内是固定列：左边窗口名列定宽，中间弹簧把倒计时
// （或配速提示，太长时省略）贴到进度条左侧，右边进度条与已用/剩余百分比各占定宽列，因此
// 每一行（含第二个账号）的进度条与百分比都从同一位置开始、在同一位置结束。
// 百分比默认显示已用额度，点击在「已用 / 剩余」之间切换（所有窗口共用
// s.composerView.quotaShowRemaining），颜色按已用百分比分档。
// sharedCountdown 是该组标题行已经显示过的共同倒计时，与之相同时行内不再重复。
// stale 为真时在倒计时后追加过期提示；按当前速度会提前用尽时在倒计时旁显示橙色小字提示。
// 走供应商分组时带 indent 缩进，与组头供应商名左边缘对齐。
func renderComposerQuotaWindowRow(c *ui.Context, s *chatState, idx int, win ProviderQuotaWindow, now time.Time, stale bool, sharedCountdown string, indent float32) {
	remaining := math.Max(0, math.Min(100, 100.0-win.UsedPercent))
	used := math.Max(0, math.Min(100, win.UsedPercent))
	countdown := quotaWindowCountdown(now, win, stale, sharedCountdown)
	percentText := fmt.Sprintf("已用 %d%%", int(math.Round(used)))
	if s != nil && s.composerView.quotaShowRemaining {
		percentText = fmt.Sprintf("剩余 %d%%", int(math.Round(remaining)))
	}

	row := ui.Row(c.Key(fmt.Sprintf("composer-usage-quota-win-%d", idx))).
		FillWidth().
		Height(ControlXS).
		AlignItems(ui.Center).
		Gap(Space2)
	if indent > 0 {
		row.Padding(0, 0, 0, indent)
	}
	// 悬停整行窗口元素时显示按当前配速预测的虚影。
	projected := row.Hovered()
	row.Children(func() {
		// 窗口名列定宽，保证各行（含第二个账号）的倒计时、进度条与百分比列左边缘对齐。
		name := quotaPopoverWindowName(win)
		nameText := ui.Text(c, name).
			FontSize(FontSizeCallout).
			TextColor(ThemeSecondaryText(c)).
			SingleLine().
			Width(quotaPopoverNameWidth)
		if name != win.Label {
			nameText.Tooltip(win.Label)
		}

		// 中间是弹簧 + 倒计时（或配速提示）：倒计时紧贴进度条左侧、宽度按内容自适应，空间不够时
		// 先压缩它；没有倒计时时弹簧占满中间列，保证各行进度条与百分比列对齐。
		ui.Spacer(c)
		paceText := ""
		if !stale {
			state, runOutAt := quotaPace(now, win)
			paceText = paceRunOutText(now, runOutAt, state)
		}
		if paceText != "" {
			t := ui.Text(c, paceText).
				FontSize(FontSizeProjectName).
				TextColor(ThemeWarning(c)).
				SingleLine().
				Shrink(1)
			if countdown != "" {
				t.Tooltip(countdown)
			}
		} else if countdown != "" {
			ui.Text(c, countdown).
				FontSize(FontSizeProjectName).
				TextColor(ThemeTertiaryText(c)).
				SingleLine().
				Shrink(1)
		}

		// 额度进度条：定宽列，与左边的窗口名、倒计时和右边的百分比列在不同行之间对齐。
		clamped := math.Max(0, math.Min(100, win.UsedPercent))
		ratio := float32(clamped / 100.0)
		elapsedRatio, hasElapsed := quotaElapsedRatio(now, win)
		paceStateNow, _ := quotaPace(now, win)
		barColor := quotaLevelColor(c, win.UsedPercent)
		ui.Row(c.Key(fmt.Sprintf("composer-usage-quota-win-%d-bar", idx))).
			Label(fmt.Sprintf("composer-usage-quota-win-%d-bar", idx)).
			Width(quotaPopoverBarWidth).
			Height(quotaBarHeight).
			Radius(quotaBarRadius).
			Background(quotaBarTrackColor(c)).
			Clip().
			Children(func() {
				if !hasElapsed {
					// 没有重置时间时保持旧渲染：按已用比例填充，整条按配额分档取色。
					if ratio > 0 {
						ui.Box(c.Key(fmt.Sprintf("composer-usage-quota-win-%d-fill", idx))).
							FillHeight().
							Width(0).
							Grow(ratio).
							Radius(quotaBarRadius).
							Background(barColor)
					}
					if ratio < 1 {
						ui.Box(c.Key(fmt.Sprintf("composer-usage-quota-win-%d-empty", idx))).
							Width(0).
							Grow(1 - ratio)
					}
					return
				}
				// 有时间信息时按分段渲染：竖线落在时间进度上，整条用同一分档色，悬停时叠加同色虚影。
				segs := quotaBarSegments(float64(clamped)/100, elapsedRatio, paceStateNow != paceStateUnknown, projected)
				renderQuotaBarSegments(c, fmt.Sprintf("composer-usage-quota-win-%d", idx), segs, quotaLevelColor(c, clamped), "quota-pace-marker")
			})

		// 百分比是透明背景的按钮：定宽右对齐、悬停高亮，点击切换已用 / 剩余。
		percent := ui.ButtonBase(c.Key(fmt.Sprintf("composer-usage-quota-win-%d-percent", idx))).
			Label(percentText).
			Tooltip("点击切换已用/剩余").
			Width(quotaPopoverPercentWidth).
			Height(ControlXS).
			Justify(ui.End).
			Radius(RadiusXS).
			Background(ui.Transparent).
			OnClick(func() {
				if s == nil {
					return
				}
				s.composerView.quotaShowRemaining = !s.composerView.quotaShowRemaining
				s.invalidate()
			})
		if percent.Hovered() {
			percent.Background(ThemeHover(c))
		}
		percent.Children(func() {
			ui.Text(c, percentText).
				FontFeatures("tnum").
				FontSize(FontSizeCallout).
				FontWeight(500).
				TextColor(quotaPercentColor(c, win.UsedPercent)).
				SingleLine()
		})
	})
}

// quotaStripGroup 是额度条里的一组读数：一个供应商对应一组。
type quotaStripGroup struct {
	provider string
}

// quotaStripSwarmOn 判断会话的子代理（Swarm）是否开启：待创建会话取待创建选项，
// 其余取后端 Swarm 面板状态。
func quotaStripSwarmOn(s *chatState, th *Thread) bool {
	if th.ID == "" {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.pendingThread != nil && s.pendingThread.Options.SwarmEnabled
	}
	panel := getSwarmPanelState(s)
	if panel == nil {
		return false
	}
	swarmPanelMu.Lock()
	defer swarmPanelMu.Unlock()
	return panel.state.Enabled
}

// quotaStripProviders 返回额度条要展示的组：当前会话排第一；子代理开启且会话的
// Swarm bee 子会话用了其他供应商时，也各列一组（子代理关闭则只展示主代理额度）。
// 嵌入状态（浮动面板里的子代理输入框）只展示该子代理会话自己的供应商。
// 同一供应商下不同订阅账户的去重留给渲染阶段。
func quotaStripProviders(s *chatState, th *Thread) []quotaStripGroup {
	if s == nil || th == nil {
		return nil
	}
	groups := []quotaStripGroup{{provider: threadQuotaProvider(th)}}
	if !s.embeddedBee && !quotaStripSwarmOn(s, th) {
		return groups
	}

	// Swarm bee 的供应商：Lead 会话摘要里显式配置的 BeesProvider，或 Swarm 面板里
	// 挂在该 Lead 下的 bee 所用的供应商。额度按 threadQuotaProvider 算 key，
	// 所以 pi bee 也要分成到它自己的 magpie 端点。
	if hp := threadQuotaProvider(&Thread{Provider: th.BeesProvider, Model: th.BeesModel}); hp != "" {
		groups = append(groups, quotaStripGroup{provider: hp})
	}
	if s.embeddedBee {
		return groups
	}
	if panel := getSwarmPanelState(s); panel != nil {
		swarmPanelMu.Lock()
		state := panel.state
		swarmPanelMu.Unlock()
		for _, h := range state.Bees {
			provider := threadQuotaProvider(&Thread{Provider: h.Provider, Model: h.Model})
			if provider == "" {
				continue
			}
			groups = append(groups, quotaStripGroup{provider: provider})
		}
	}
	return groups
}

// quotaStripAccount 选额度条某个组要展示的订阅账户：只有一个未停用账户组时直接用它
// （账号名可能为空串，如没有账号名的 Credits 窗口）；否则优先本会话确认在用的账户，
// 没有时用 Magpie 记录的最近应答账户并返回 recent=true，让调用方标注「最近使用」；
// 两者都没有时返回空串，不再从模型名或返回顺序推断账户。
func quotaStripAccount(s *chatState, provider string, windows []ProviderQuotaWindow) (account string, recent bool) {
	// 只有一个账号时不存在猜测，单账号 provider 与没有账号名的分组都直接展示它的读数。
	if account, ok := quotaSoleAccount(groupQuotaWindowsByAccount(windows)); ok {
		return account, false
	}
	if s != nil {
		if account := s.quotaInUseAccount(provider, windows); account != "" {
			return account, false
		}
	}
	if account := quotaRecentAccount(windows); account != "" {
		return account, true
	}
	return "", false
}

// quotaStripPlan 是额度条里一组已确定订阅账户与元素 key 的渲染计划。
type quotaStripPlan struct {
	group   quotaStripGroup
	windows []ProviderQuotaWindow
	// account 是这组用的订阅账户（可能为空，表示本会话账号未知）。
	account string
	// recent 为 true 表示 account 只是 Magpie 记录的最近应答账号，不是本会话确认在用。
	recent bool
	// key 是这组元素 key 的后缀：默认就是 provider，同一 provider 有多组时带上账户避免冲突。
	key string
}

// quotaAccountWindows 取某个账户下的窗口，包括账号为空串的无名窗口组（如 Credits）；
// 空串只有在整份数据仅有一个未停用组、且这组没有账号名时才代表当前在用的那份额度。
// 账户不在列表里时返回 nil，不再借用第一组窗口冒充这个账号。
func quotaAccountWindows(account string, windows []ProviderQuotaWindow) []ProviderQuotaWindow {
	groups := groupQuotaWindowsByAccount(windows)
	if account == "" {
		if sole, ok := quotaSoleAccount(groups); !ok || sole != "" {
			return nil
		}
	}
	for _, g := range groups {
		if g.account == account {
			return g.windows
		}
	}
	return nil
}

// quotaStripPlans 把额度条的分组整理成渲染计划：没有额度数据的组跳过，按 provider+选中账户去重，
// 同一 provider 有多组（lead 与 bee 用不同订阅）时给元素 key 带上账户。
func quotaStripPlans(s *chatState, groups []quotaStripGroup) []quotaStripPlan {
	seen := make(map[string]bool, len(groups))
	counts := make(map[string]int, len(groups))
	plans := make([]quotaStripPlan, 0, len(groups))
	for _, g := range groups {
		windows := getChatQuotaWindowsForProvider(s, g.provider)
		if len(windows) == 0 {
			continue
		}
		account, recent := quotaStripAccount(s, g.provider, windows)
		dedupe := g.provider + "\x00" + account
		if seen[dedupe] {
			continue
		}
		seen[dedupe] = true
		counts[g.provider]++
		plans = append(plans, quotaStripPlan{
			group:   g,
			windows: quotaAccountWindows(account, windows),
			account: account,
			recent:  recent,
		})
	}
	for i := range plans {
		key := plans[i].group.provider
		if counts[key] > 1 && plans[i].account != "" {
			// 同一 provider 的账户可能带「 · 标识」（多个账号），空格不进元素 key。
			key += "-" + strings.ReplaceAll(plans[i].account, " ", "-")
		}
		plans[i].key = key
	}
	return plans
}

// renderComposerQuotaStrip 渲染 Composer 左下角的供应商额度条：每个供应商一组、各自可点击，
// 点击在某组下方锚定打开只含该 provider 的额度弹层。每组只显示一个账户的 5h/周窗口：
// 有唯一在用账户时显示它，否则显示 Magpie 记录的最近应答账户（悬停注明最近使用）；
// 两者都没有时这组显示「额度未知」，不借用其他账户的数字。
// 嵌入状态（浮动面板里的子代理输入框）同样渲染，用的是该子代理会话的供应商。
func renderComposerQuotaStrip(c *ui.Context, s *chatState, th *Thread, tiers ...composerChinSize) {
	tier := composerChinFull
	if len(tiers) > 0 {
		tier = tiers[0]
	}
	if s == nil || th == nil || strings.TrimSpace(th.Provider) == "" {
		return
	}

	groups := quotaStripProviders(s, th)
	for _, g := range groups {
		s.loadComposerQuotaForProvider(g.provider, false)
	}

	anyWindows := false
	for _, g := range groups {
		if windows := getChatQuotaWindowsForProvider(s, g.provider); len(windows) > 0 {
			anyWindows = true
			s.observeQuotaAccounts(g.provider, windows)
		}
	}
	// 只有全部组都没有额度数据时，才由主 provider 的状态决定整体显示加载中或不可用；
	// 任一组已有数据时按组渲染，仍在加载或无数据的附加组直接跳过。
	loading, quotaErr := composerQuotaStatus(s, threadQuotaProvider(th))
	stripStatus := chatUsageStatus(s, threadQuotaProvider(th), usagePanelNow())
	if stripStatus.State == usageStateOK && quotaErr != "" {
		// 加载中与状态读取之间可能刚好翻转，这里按错误文本兑一个可用状态，避免短标签为空。
		stripStatus = usageStatusFor(quotaErr, nil, time.Time{}, usagePanelNow())
	}
	if !anyWindows && quotaErr == "" && !loading {
		return
	}

	viewport := ui.Row(c.Key("composer-quota-chip")).
		Label("供应商额度").
		Gap(0).
		AlignItems(ui.Center)

	viewport.Children(func() {
		ui.Row(c.Key("composer-quota-chip-content")).
			Gap(0).
			AlignItems(ui.Center).
			Children(func() {
				switch {
				case !anyWindows && quotaErr != "":
					// 没有任何一组数据时才落在整体状态上：图标 + 状态短标签，不可点击。
					ui.Row(c.Key("composer-quota-status")).
						Gap(Space2).
						AlignItems(ui.Center).
						Children(func() {
							ui.Icon(c, IconOctagonAlert()).
								Size(usageStripSmallSize, usageStripSmallSize).
								TextColor(usageStatusColor(c, stripStatus))
							ui.Text(c, usageStatusStripLabel(stripStatus)).
								FontSize(FontSizeProjectName).
								FontWeight(500).
								TextColor(usageStatusColor(c, stripStatus))
						})
				case !anyWindows && loading:
					ui.Row(c.Key("composer-quota-status")).
						Gap(Space2).
						AlignItems(ui.Center).
						Children(func() {
							if tier >= composerChinIcons {
								renderComposerQuotaRing(c, "loading", "额度", &quotaRingWindow{}, nil)
								return
							}
							renderRunningGlyph(c, "quota-loading", usageStripSmallSize, ThemeSecondaryText(c))
							ui.Text(c, "加载中…").
								FontSize(FontSizeProjectName).
								FontWeight(500).
								TextColor(ThemeSecondaryText(c))
						})
				default:
					// 最窄档只保留使用率最高的供应商组。
					plans := quotaStripPlans(s, groups)
					if tier == composerChinNarrow && len(plans) > 1 {
						best, maxUsed := -1, -1.0
						for i, plan := range plans {
							for _, w := range plan.windows {
								if !quotaWindowIsAuxiliary(w) && w.UsedPercent > maxUsed {
									if plan.group.provider == "agy" {
										if _, ok := agyQuotaStripWindowKey(w.Label); !ok {
											continue
										}
									}
									best, maxUsed = i, w.UsedPercent
								}
							}
						}
						if best >= 0 {
							plans = plans[best : best+1]
						} else {
							plans = plans[:1]
						}
					}
					rendered := false
					for _, plan := range plans {
						if !renderComposerQuotaProviderGroup(c, s, th, plan, rendered, tier, len(plans) > 1) {
							continue
						}
						rendered = true
					}
					if !rendered {
						// 有窗口但都无法映射成短标签时，不能再显示「加载中…」。
						ui.Text(c, "暂无额度数据").
							FontSize(FontSizeProjectName).
							FontWeight(500).
							TextColor(ThemeSecondaryText(c))
					}
				}
			})
	})
}

// quotaStripGroupTooltip 拼接额度条一个分组的悬停提示。一组只展示一个账号的额度：
// 注明是哪个账号，避免看成本机或其他账号；只有最近应答线索时明确说明它不是本会话确认的
// 在用账号。无名分组（如 workbuddy 的 Credits）读数照常展示，只有窗口也为空时才提示账号未知。
func quotaStripGroupTooltip(name string, plan quotaStripPlan) string {
	tooltip := name + " 额度"
	switch {
	case plan.account != "" && plan.recent:
		tooltip += " · " + plan.account + " · 最近使用，非本会话确认"
	case plan.account != "":
		tooltip += " · " + plan.account
	case len(plan.windows) == 0:
		tooltip += " · 本会话账号未知"
	}
	return tooltip
}

// renderComposerQuotaProviderGroup 渲染额度条里一个供应商的读数（点击组打开该 provider 的额度弹层），
// 以及组间的 secondary 色竖线（竖线属于组外，不可点击）。不是第一组时前置竖线。
// 组内顺序：供应商图标，然后每个窗口「短标签 + 迷你进度条 + 已用百分比」。
// 没有可展示的窗口时返回 false。
func renderComposerQuotaProviderGroup(c *ui.Context, s *chatState, th *Thread, plan quotaStripPlan, separated bool, tier composerChinSize, showProvider bool) bool {
	windows := plan.windows
	// 账户未知时 windows 为空（quotaAccountWindows 不再回退第一组），这组仍要渲染，
	// 只是把读数换成「额度未知」，不能整组消失。
	unknownAccount := len(windows) == 0
	provider := plan.group.provider
	keySuffix := plan.key

	type readout struct {
		label    string
		used     float64
		minutes  int // 推断出的周期时长，未知为 0
		resetsAt time.Time
		scope    int // 0 = 账户/全模型窗口，1 = 按模型限定的窗口
		order    int
	}
	var candidates []readout
	appendReadout := func(label string, used float64, minutes, scope int, resetsAt time.Time) {
		candidates = append(candidates, readout{
			label:    label,
			used:     math.Max(0, math.Min(100, used)),
			minutes:  minutes,
			resetsAt: resetsAt,
			scope:    scope,
			order:    len(candidates),
		})
	}
	for _, w := range windows {
		// MCP tools、附加用量等辅助窗口不是订阅额度，不能混进主额度条。
		if quotaWindowIsAuxiliary(w) {
			continue
		}
		if provider == "agy" {
			key, ok := agyQuotaStripWindowKey(w.Label)
			if !ok {
				continue
			}
			// agy 的窗口名只区分 Gemini 的 5h / 7d 两档，排序用的时长由短标签本身决定。
			minutes := 0
			switch key {
			case "5h":
				minutes = 5 * quotaHourMinutes
			case "周":
				minutes = quotaWeekMinutes
			}
			appendReadout(key, w.UsedPercent, minutes, 0, w.ResetsAt)
			continue
		}
		key, minutes, ok := quotaStripWindowKey(w)
		if !ok {
			continue
		}
		appendReadout(key, w.UsedPercent, minutes, quotaWindowScopeRank(w.Label), w.ResetsAt)
	}
	// 周期短的排前面；同一周期优先账户/全模型窗口，其次保持接口返回顺序。
	slices.SortStableFunc(candidates, func(a, b readout) int {
		if a.minutes != b.minutes {
			return a.minutes - b.minutes
		}
		if a.scope != b.scope {
			return a.scope - b.scope
		}
		return a.order - b.order
	})
	seen := make(map[string]bool, 2)
	var readouts []readout
	for _, r := range candidates {
		if seen[r.label] {
			continue
		}
		seen[r.label] = true
		readouts = append(readouts, r)
	}
	if len(readouts) == 0 && provider != "agy" {
		// 周期全都推断不出来时兜底显示前两条数据，避免拿到配额却显示「暂无额度数据」。
		// agy 只展示 Gemini 系列，这里不兜底，以免把 Claude/GPT 分组带进额度条。
		for i, w := range windows {
			if i >= 2 {
				break
			}
			label := "额度"
			if i > 0 {
				label = fmt.Sprintf("额度%d", i+1)
			}
			readouts = append(readouts, readout{label: label, used: math.Max(0, math.Min(100, w.UsedPercent)), resetsAt: w.ResetsAt, order: i})
		}
	}
	if len(readouts) == 0 && !unknownAccount {
		return false
	}
	if tier == composerChinNarrow && len(readouts) > 1 {
		best := 0
		for i := 1; i < len(readouts); i++ {
			if readouts[i].used > readouts[best].used {
				best = i
			}
		}
		readouts = readouts[best : best+1]
	}

	if separated {
		// 竖线两侧各留 Space2，与组内更紧的 Gap(0) 区分开分组边界。
		ui.Row(c.Key("composer-quota-separator-"+keySuffix)).
			Gap(0).
			Margin(0, Space2).
			AlignItems(ui.Center).
			Children(func() {
				ui.Text(c, "｜").
					FontSize(FontSizeProjectName).
					TextColor(ThemeTertiaryText(c)).
					SingleLine()
			})
	}

	groupOpen := s.composerView.quotaOpen && s.composerView.quotaPopoverProvider == provider
	groupTooltip := quotaStripGroupTooltip(s.providerDisplayName(provider), plan)
	outer, inner := quotaRingWindows(provider, windows)
	if tier >= composerChinIcons && outer != nil {
		parts := []string{fmt.Sprintf("%s %d%%", outer.label, int(math.Round(outer.used)))}
		if inner != nil {
			parts = append(parts, fmt.Sprintf("%s %d%%", inner.label, int(math.Round(inner.used))))
		}
		groupTooltip = strings.Join(parts, " · ")
	}
	group := ui.ButtonBase(c.Key("composer-quota-provider-"+keySuffix)).
		Label("额度 "+provider).
		Tooltip(groupTooltip).
		// 纵向与横向都取半格间距：额度条在输入框上方的行高里只占一行，保持紧凑。
		Padding(Space0, Space0, Space0, Space0).
		Radius(RadiusXS).
		Background(ui.Transparent).
		OnClick(func() {
			if s.composerView.quotaOpen && s.composerView.quotaPopoverProvider == provider {
				s.composerView.quotaOpen = false
				s.composerView.quotaPopoverProvider = ""
				return
			}
			s.composerView.quotaOpen = true
			s.composerView.quotaPopoverProvider = provider
			s.loadComposerQuotaForProvider(provider, false)
		})
	if groupOpen || group.Hovered() {
		group.Background(ThemeHover(c))
	}

	group.Children(func() {
		ui.Row(c.Key("composer-quota-provider-content-" + keySuffix)).
			Gap(Space2).
			AlignItems(ui.Center).
			Children(func() {
				if showProvider {
					ui.Box(c.Key("composer-quota-provider-icon-" + keySuffix)).
						Label(s.providerDisplayName(provider)).
						Children(func() { renderProviderIconColor(c, provider, IconSM, ThemeSecondaryText(c)) })
				}
				if unknownAccount {
					ui.Text(c, "额度未知").
						FontSize(FontSizeProjectName).
						FontWeight(500).
						TextColor(ThemeSecondaryText(c)).
						SingleLine()
					return
				}
				if tier >= composerChinIcons && outer != nil {
					renderComposerQuotaRing(c, keySuffix, groupTooltip, outer, inner)
					if tier == composerChinIcons {
						used := outer.used
						if inner != nil && inner.used > used {
							used = inner.used
						}
						ui.Text(c, fmt.Sprintf("%d%%", int(math.Round(used)))).
							FontFeatures("tnum").FontSize(FontSizeProjectName).FontWeight(500).
							TextColor(ThemeSecondaryText(c)).SingleLine()
					}
					return
				}
				for _, r := range readouts {
					// 每个窗口自己一组：短标签、迷你进度条、已用百分比，窗口之间靠外层 Gap 留白。
					ui.Row(c.Key(fmt.Sprintf("composer-quota-provider-window-%s-%s", keySuffix, r.label))).
						Gap(Space0).
						AlignItems(ui.Center).
						Children(func() {
							if tier != composerChinNarrow {
								ui.Text(c, r.label).
									FontSize(FontSizeProjectName).
									FontWeight(500).
									TextColor(ThemeSecondaryText(c)).
									SingleLine()
								if tier < composerChinIcons {
									renderComposerQuotaMiniGauge(c, keySuffix+"-"+r.label, r.used, r.resetsAt, r.minutes)
								}
							}
							ui.Text(c, fmt.Sprintf("%d%%", int(math.Round(r.used)))).
								FontFeatures("tnum").
								FontSize(FontSizeProjectName).
								FontWeight(500).
								TextColor(ThemeSecondaryText(c)).
								SingleLine()
						})
				}
			})
	})

	// 弹层锚定在被点击的组上。点击打开时由 OnClick 写入状态，这里不能再把标记取反；
	// 各组用各自的局部开关，避免共享 quotaOpen 导致点一组时其他组的弹层也打开；
	// 点外部或 Esc 时 PopoverCard 会把局部开关置回 false，仅当本组原本打开时才据此清空全局状态。
	// 卡片贴在锚点下方左对齐，与原来 PopoverBase 的默认位置一致，圆角、边框、Popover 阴影与入场动画由 PopoverCard 统一负责。
	open := groupOpen
	popoverMaxHeight := composerQuotaPopoverMaxHeight(c, group)
	// PopoverCard 的卡片自带 Space2 内边距，内容可用高度要扣掉它，否则内容会顶出卡片。
	contentMaxHeight := popoverMaxHeight
	if contentMaxHeight > Space2*2 {
		contentMaxHeight -= Space2 * 2
	}
	popover := PopoverCard(c, PopoverProps{
		Key:       "composer-quota-popover-" + keySuffix,
		Anchor:    group,
		Open:      &open,
		Width:     quotaPopoverWidth,
		MaxHeight: popoverMaxHeight,
	}, func() {
		renderComposerQuotaPopover(c, s, th, provider, contentMaxHeight)
	})
	popover.Margin(Space1, 0, 0, 0)
	if groupOpen && popover.Dismissed() {
		s.composerView.quotaOpen = false
		s.composerView.quotaPopoverProvider = ""
	}
	return true
}

// renderComposerQuotaMiniGauge 渲染额度条里的迷你进度条：宽 18 高 3、圆角 1.5，底色 ThemeBorder。
// 有重置时间与周期时按时间进度分段渲染（竖线 1px，整条同色，虚影为同色 35% 透明）；
// 没有时间信息时按已用比例填充，颜色取配额分档色。
func renderComposerQuotaMiniGauge(c *ui.Context, key string, used float64, resetsAt time.Time, minutes int) {
	const width, height = 18.0, 3.0
	ratio := float32(1)
	if used < 100 {
		ratio = float32(used / 100)
	}
	fill := quotaLevelColor(c, used)
	elapsed, hasElapsed := quotaElapsedRatioAt(usagePanelNow(), resetsAt, minutes)
	// 竖线固定 1px，从总宽里扣掉，其余段按比例分配，保证整条仍是 18px。
	const markerWidth = 1.0
	contentWidth := float32(width - markerWidth)
	ui.Row(c.Key("composer-quota-gauge-"+key)).
		Label("composer-quota-gauge-"+key).
		Size(width, height).
		Radius(RadiusPill).
		Background(ThemeBorder(c)).
		Clip().
		AlignItems(ui.Center).
		Children(func() {
			if !hasElapsed {
				// 零填充也保留一个等高的空盒，保证进度条元素始终存在。
				ui.Box(c.Key("composer-quota-gauge-"+key+"-fill")).
					Size(width*ratio, height).
					Background(fill)
				return
			}
			usedRatio := float64(used) / 100
			paceKnown := used >= paceMinUsedPercent && elapsed >= paceMinElapsedRatio
			segs := quotaBarSegments(usedRatio, elapsed, paceKnown, false)
			for i, seg := range segs {
				segKey := fmt.Sprintf("composer-quota-gauge-%s-seg-%d", key, i)
				if seg.kind == quotaBarSegmentMarker {
					ui.Box(c.Key(segKey)).
						Label("composer-quota-gauge-"+key+"-marker").
						Size(markerWidth, height).
						Background(ThemePrimaryText(c))
					continue
				}
				box := ui.Box(c.Key(segKey)).
					Size(contentWidth*seg.grow, height)
				switch seg.kind {
				case quotaBarSegmentNormal, quotaBarSegmentOver:
					box.Background(fill)
				case quotaBarSegmentGhost, quotaBarSegmentGhostOver:
					box.Background(fill.Alpha(0.35))
				}
			}
		})
}

// agyQuotaStripWindowKey 提取 agy 额度条短标签：agy 额度条只展示 Gemini 系列。
func agyQuotaStripWindowKey(label string) (string, bool) {
	group := label
	if idx := strings.LastIndex(label, " · "); idx >= 0 {
		group = label[idx+len(" · "):]
	}
	group = strings.TrimSpace(group)
	switch group {
	case quota.GroupGemini:
		return "5h", true
	case quota.GroupGemini + " 7d":
		return "周", true
	default:
		return "", false
	}
}

// quotaStripWindowKey 把窗口映射为额度条短标签：后端给了 Period（5h、1d、7d、30d）就直接用它，
// 否则从窗口名推断周期；两种都认不出时不映射（额度条另有兜底显示）。
// 短标签都以 ASCII 字母或中文字符结尾，后面紧跟「 · 百分比」时不会与数字粘连。
func quotaStripWindowKey(w ProviderQuotaWindow) (string, int, bool) {
	minutes, ok := quotaPayloadPeriodMinutes(w.Period)
	if !ok {
		minutes, ok = quotaWindowDurationMinutes(w.Label)
	}
	if !ok {
		return "", 0, false
	}
	return quotaPeriodLabel(minutes), minutes, true
}

// quotaPayloadPeriodMinutes 把后端给出的周期标记（5h、1d、7d、30d…）换算成分钟；
// 认不出（空串或非周期文本）时返回 false，交回标题推断。
func quotaPayloadPeriodMinutes(period string) (int, bool) {
	text := strings.ToLower(strings.TrimSpace(period))
	if text == "" {
		return 0, false
	}
	return quotaDurationInText(text)
}

// quotaWindowIsAuxiliary 判断窗口是附加用量而非订阅额度：
// 预付费余额占位（ai-credits）、按需附加用量（…extra-usage）与 MCP tools 配额（tools-*）都不进主额度条。
// 后端没给 ID 时才退回窗口名判断。
func quotaWindowIsAuxiliary(w ProviderQuotaWindow) bool {
	if id := strings.ToLower(strings.TrimSpace(w.ID)); id != "" {
		return id == "ai-credits" || strings.Contains(id, "extra-usage") || strings.HasPrefix(id, "tools-")
	}
	label := strings.ToLower(strings.TrimSpace(w.Label))
	return strings.Contains(label, "mcp tools") || strings.Contains(label, "extra usage")
}

// 常用周期统一换算成分钟，便于跨单位比较：30m < 5h < 1d < 1w < 1mo。
const (
	quotaHourMinutes  = 60
	quotaDayMinutes   = 24 * quotaHourMinutes
	quotaWeekMinutes  = 7 * quotaDayMinutes
	quotaMonthMinutes = 30 * quotaDayMinutes
)

var (
	// quotaDurationPattern 匹配「数字 + 时长单位」；同组单位按长到短排列，才能命中完整单位。
	quotaDurationPattern = regexp.MustCompile(`(\d+)\s*-?\s*(分钟|分|小时|时|minutes|minute|mins|min|hours|hour|hrs|hr|h|days|day|d|weeks|week|wks|wk|w|months|month|mos|mo|m)`)
	// quotaClockPattern 匹配时钟（12:00、18:30、12 点）：这类文本描述的是重置时间点。
	quotaClockPattern = regexp.MustCompile(`\d{1,2}\s*[:：]\s*\d{2}|\d{1,2}\s*点`)
	// quotaCalendarDatePattern 匹配日历日期（1 月 1 日、每月 1 日、第 1 日）。
	quotaCalendarDatePattern = regexp.MustCompile(`\d+\s*月\s*\d+\s*[日号]|每\s*月?\s*\d+\s*[日号]|第\s*\d+\s*[日号]`)
	// quotaDayOfMonthPattern 匹配「月」后面的「N 日 / N 号」：前面的月是日历，不是周期。
	quotaDayOfMonthPattern = regexp.MustCompile(`^\s*\d+\s*[日号]`)
	// quotaEnglishDayPattern 匹配裸词 day（没有数字时也算一天周期）。
	quotaEnglishDayPattern = regexp.MustCompile(`\bday\b`)
)

// quotaDurationUnits 把时长单位换算成分钟；未登记的单位返回 0。
// 裸写「m」按 30m 这类分钟缩写处理，月份只认 月/mo/month。
var quotaDurationUnits = map[string]int{
	"分钟": 1, "分": 1,
	"min": 1, "mins": 1, "minute": 1, "minutes": 1, "m": 1,
	"小时": quotaHourMinutes, "时": quotaHourMinutes,
	"h": quotaHourMinutes, "hr": quotaHourMinutes, "hrs": quotaHourMinutes,
	"hour": quotaHourMinutes, "hours": quotaHourMinutes,
	"天": quotaDayMinutes, "日": quotaDayMinutes,
	"d": quotaDayMinutes, "day": quotaDayMinutes, "days": quotaDayMinutes,
	"周": quotaWeekMinutes,
	"w": quotaWeekMinutes, "wk": quotaWeekMinutes, "wks": quotaWeekMinutes,
	"week": quotaWeekMinutes, "weeks": quotaWeekMinutes,
	"月":  quotaMonthMinutes,
	"mo": quotaMonthMinutes, "mos": quotaMonthMinutes,
	"month": quotaMonthMinutes, "months": quotaMonthMinutes,
}

// quotaDurationAliases 把无数字的周期别名映射时长：fiveHour、weekly、每天、每月…
var quotaDurationAliases = []struct {
	needle  string
	minutes int
}{
	{"fivehour", 5 * quotaHourMinutes},
	{"five_hour", 5 * quotaHourMinutes},
	{"five-hour", 5 * quotaHourMinutes},
	{"hourly", quotaHourMinutes},
	{"每小时", quotaHourMinutes},
	{"seven_day", quotaWeekMinutes},
	{"sevenday", quotaWeekMinutes},
	{"seven-day", quotaWeekMinutes},
	{"daily", quotaDayMinutes},
	{"每日", quotaDayMinutes},
	{"每天", quotaDayMinutes},
	{"weekly", quotaWeekMinutes},
	{"每周", quotaWeekMinutes},
	{"每星期", quotaWeekMinutes},
	{"monthly", quotaMonthMinutes},
	{"每月", quotaMonthMinutes},
}

// quotaClockTails 出现在「时」后面就说明它是钟点而不是时长（「12 时重置」）。
var quotaClockTails = []string{"重置", "刷新", "恢复", "过期", "到期", "开始", "结束", "点", "整", "半",
	"reset", "refresh", "recover", "restore", "expir"}

// quotaResetTails 是重置/到期类措辞，跟在一个日期后面说明那是日期而不是天数。
var quotaResetTails = []string{"重置", "刷新", "恢复", "过期", "到期", "开始", "结束",
	"reset", "refresh", "recover", "restore", "expir"}

// quotaWindowDurationMinutes 从窗口名推断周期时长（分钟）；推断不出时返回 false。
// 只看窗口名本身的 id/title 证据，不看重置时间。
func quotaWindowDurationMinutes(label string) (int, bool) {
	return quotaDurationInText(strings.ToLower(strings.TrimSpace(label)))
}

// quotaDurationInText 从一段已小写的窗口名里取最短的周期时长（分钟）。
// 显式「数字 + 单位」优先；没有显式时长时，重置时间点文本（「每天 12:00 重置」
// 「每月 1 日」）一律不猜周期，只有真正写周期词（每天/每周/weekly）的才回退。
func quotaDurationInText(text string) (int, bool) {
	found := quotaExplicitMinutes(text)
	if len(found) == 0 {
		if quotaClockPattern.MatchString(text) || quotaCalendarDatePattern.MatchString(text) {
			return 0, false
		}
		found = quotaAliasMinutes(text)
	}
	if len(found) == 0 {
		found = quotaBareUnitMinutes(text)
	}
	if len(found) == 0 {
		return 0, false
	}
	return slices.Min(found), true
}

// quotaExplicitMinutes 收集所有「数字 + 单位」的时长，跳过时间点等假阳性。
func quotaExplicitMinutes(text string) []int {
	var found []int
	for _, m := range quotaDurationPattern.FindAllStringSubmatchIndex(text, -1) {
		unit := text[m[4]:m[5]]
		factor := quotaDurationUnits[unit]
		if factor == 0 {
			continue
		}
		head, tail := text[:m[0]], text[m[1]:]
		if !quotaDurationMatchCounts(unit, head, tail) {
			continue
		}
		if minutes, ok := quotaScaledMinutes(text[m[2]:m[3]], factor); ok {
			found = append(found, minutes)
		}
	}
	return found
}

// quotaDurationMatchCounts 判断一次「数字 + 单位」匹配是时长还是钟点/日期。
func quotaDurationMatchCounts(unit, head, tail string) bool {
	// 单位后面紧跟拉丁字母说明匹配到了更长的单词（"5hx"），不是时长。
	if len(tail) > 0 && (tail[0] >= 'a' && tail[0] <= 'z' || tail[0] >= 'A' && tail[0] <= 'Z') {
		return false
	}
	switch unit {
	case "时":
		return !quotaStartsWithAny(tail, quotaClockTails)
	case "日":
		trimmed := strings.TrimSpace(head)
		// 「每月 1 日」「第 1 日」「1 月 1 日」都是日历日期。
		if strings.HasSuffix(trimmed, "每") || strings.HasSuffix(trimmed, "第") || strings.HasSuffix(trimmed, "月") {
			return false
		}
		return !quotaStartsWithAny(tail, quotaResetTails)
	case "月":
		// 「1 月 1 日」里的月是日期的一部分，不是一个月周期。
		return !quotaDayOfMonthPattern.MatchString(tail)
	}
	return true
}

// quotaScaledMinutes 把数字乘以单位分钟数；非正数或过大（防溢出）返回 false。
func quotaScaledMinutes(digits string, factor int) (int, bool) {
	value, err := strconv.Atoi(digits)
	if err != nil || value <= 0 || value > 100_000 {
		return 0, false
	}
	return value * factor, true
}

// quotaAliasMinutes 收集无数字的周期别名（daily/weekly/每天/每周…）。
func quotaAliasMinutes(text string) []int {
	var found []int
	for _, alias := range quotaDurationAliases {
		if strings.Contains(text, alias.needle) {
			found = append(found, alias.minutes)
		}
	}
	return found
}

// quotaBareUnitMinutes 是最后的兜底：只有单位没有数字的窗口名（「周限额」「本月额度」）。
// 周期（billing cycle）与「周一…周日」不是周窗口，今天/明天等相对日期不是一天周期。
func quotaBareUnitMinutes(text string) []int {
	var found []int
	if !strings.Contains(text, "周期") && quotaHasWeekUnit(text) {
		found = append(found, quotaWeekMinutes)
	}
	if strings.Contains(text, "月") {
		found = append(found, quotaMonthMinutes)
	}
	stripped := text
	for _, word := range []string{"今天", "明天", "昨天", "前天", "后天"} {
		stripped = strings.ReplaceAll(stripped, word, "")
	}
	if strings.Contains(stripped, "天") {
		found = append(found, quotaDayMinutes)
	}
	if quotaEnglishDayPattern.MatchString(text) {
		found = append(found, quotaDayMinutes)
	}
	return found
}

// quotaHasWeekUnit 判断「周」是否指一周：后面跟星期几（「周一」…「周日」）时不算。
func quotaHasWeekUnit(text string) bool {
	runes := []rune(text)
	for i, r := range runes {
		if r != '周' {
			continue
		}
		if i+1 >= len(runes) || !strings.ContainsRune("一二三四五六日天", runes[i+1]) {
			return true
		}
	}
	return false
}

// quotaStartsWithAny 判断文本trim后是否以候选词之一开头。
func quotaStartsWithAny(text string, words []string) bool {
	trimmed := strings.TrimSpace(text)
	for _, word := range words {
		if strings.HasPrefix(trimmed, word) {
			return true
		}
	}
	return false
}

// quotaPeriodLabel 把周期时长映射成额度条短标签：30m、5h、日、周、月，其余用 Nd/Nh/Nm。
func quotaPeriodLabel(minutes int) string {
	switch minutes {
	case quotaDayMinutes:
		return "日"
	case quotaWeekMinutes:
		return "周"
	case quotaMonthMinutes:
		return "月"
	}
	if minutes > 0 && minutes%quotaDayMinutes == 0 {
		return fmt.Sprintf("%dd", minutes/quotaDayMinutes)
	}
	if minutes > 0 && minutes%quotaHourMinutes == 0 {
		return fmt.Sprintf("%dh", minutes/quotaHourMinutes)
	}
	return fmt.Sprintf("%dm", minutes)
}

// quotaWindowScopeRank 给同周期的窗口排序用：0 是账户窗口或「all models」全模型窗口，
// 1 是按模型限定的窗口（「Weekly · Opus」），额度条优先展示前者。
func quotaWindowScopeRank(label string) int {
	lower := strings.ToLower(label)
	if strings.Contains(lower, "all models") || strings.Contains(label, "全部模型") || strings.Contains(label, "全模型") {
		return 0
	}
	if _, rest, ok := strings.Cut(label, " · "); ok && strings.TrimSpace(rest) != "" {
		return 1
	}
	return 0
}

// composerQuotaRefreshLabel 返回刷新按钮的文字：刷新进行中显示「刷新中…」并提示进度。
func composerQuotaRefreshLabel(s *chatState, provider string) string {
	if composerQuotaRefreshing(s, provider) {
		return "刷新中…"
	}
	return "刷新"
}

// composerQuotaPopoverMaxHeight 返回额度弹层的最大高度：弹层在额度条上方展开，优先用
// 锚点上方到窗口顶部的空间（留 16 像素呼吸空间），拿不到锚点位置时退回窗口高度的 70%。
func composerQuotaPopoverMaxHeight(c *ui.Context, anchor ui.Element) float32 {
	const margin = 8
	_, winH := c.Size()
	if winH <= 0 {
		return 0
	}
	limit := winH - margin*2
	if ab := anchor.Bounds(); ab.H > 0 && ab.Y > 0 {
		if above := ab.Y - 16; above > 0 && above < limit {
			limit = above
		}
	} else if capped := winH * 0.7; capped < limit {
		limit = capped
	}
	return max(limit, 0)
}

// renderComposerQuotaPopover 渲染额度详情浮窗：标题行固定，下面是可滚动的按账户分组的
// 额度列表。弹层由额度条里被点击的 provider 组锚定，因此只展示这一个 provider 的额度。
// maxHeight 大于 0 时面板与列表都不超过它，供应商再多也不会长出窗口。
func renderComposerQuotaPopover(c *ui.Context, s *chatState, th *Thread, provider string, maxHeight float32) {
	// 卡片外观（圆角、边框、Popover 阴影、背景）由外层 PopoverCard 统一提供，这里只排内容。
	// 面板保留上下内边距，水平内边距由各行和列表提供；字号不随全局字号缩放。
	panel := ui.Column(c.Key("composer-quota-popover-panel")).
		Label("额度详情").
		FillWidth().
		Padding(Space4, 0).
		Gap(Space4)
	if maxHeight > 0 {
		panel.MaxHeight(maxHeight)
	}
	windows := getChatQuotaWindowsForProvider(s, provider)
	title := s.providerDisplayName(provider)
	var groups []trayQuotaGroupModel
	if len(windows) > 0 {
		groups = composerQuotaGroupModels(provider, title, windows, s.quotaInUseAccount(provider, windows), quotaRecentAccount(windows), usagePanelNow())
		if len(groups) == 1 && len(groupQuotaWindowsByProvider(windows)) == 1 {
			title += " · " + groups[0].Title
		}
	}
	panel.Children(func() {
		ui.Row(c.Key("composer-quota-popover-header")).
			FillWidth().
			Padding(0, Space4).
			Shrink(0).
			AlignItems(ui.Center).
			Gap(Space2).
			Children(func() {
				renderProviderIconColor(c, provider, IconMD, ThemeSecondaryText(c))
				ui.Text(c, title).
					FontSize(FontSizeTitle3).
					FontWeight(600).
					TextColor(ThemePrimaryText(c)).
					SingleLine()

				if len(groups) == 1 && groups[0].Plan != "" {
					ui.Text(c, "· "+groups[0].Plan).
						FontSize(TypeFootnote).
						TextColor(ThemeSecondaryText(c)).
						SingleLine()
				}
				ui.Spacer(c)
				if len(groups) == 1 && groups[0].HeadText != "" {
					textColor := ThemeSecondaryText(c)
					if groups[0].HeadWarn {
						textColor = ThemeWarning(c)
					}
					ui.Text(c, groups[0].HeadText).FontSize(TypeFootnote).TextColor(textColor).SingleLine()
				}

				refresh := ui.ButtonBase(c.Key("composer-quota-refresh")).
					Label(composerQuotaRefreshLabel(s, provider)).
					Tooltip("刷新额度").
					Height(ControlSM).
					Radius(RadiusPill).
					Padding(0, Space3).
					Background(ThemeSurface(c)).
					OnClick(func() {
						// 先让网关重新向厂商读取，结束后无论成败都会重新拉额度与余额。
						s.refreshComposerQuotaForProvider(provider)
					})
				if refresh.Hovered() {
					refresh.Background(ThemeHover(c))
				}
				refresh.Children(func() {
					ui.Text(c, composerQuotaRefreshLabel(s, provider)).
						FontSize(FontSizeProjectName).
						TextColor(ThemePrimaryText(c))
				})
			})

		// 预付费余额：仅当 provider 支持余额接口时出现，与额度列表并列。
		s.loadComposerCreditsForProvider(provider, false)
		credits, hasCredits := getChatCreditsForProvider(s, provider)
		if hasCredits {
			ui.Row(c.Key("composer-quota-credits-wrapper")).FillWidth().Padding(0, Space4).Children(func() {
				renderComposerCreditsRow(c, "composer-quota-credits", credits)
			})
		}

		if len(windows) > 0 {
			errText := composerQuotaRefreshErr(s, provider)
			if errText != "" {
				// 有旧数据时列表照常展示，但刷新失败要看得见。
				ui.Row(c.Key("composer-quota-popover-refresh-error")).FillWidth().Padding(0, Space4).Children(func() {
					renderUsageStatusLine(c, usageStatusFor(errText, nil, time.Time{}, usagePanelNow()), FontSizeCallout)
				})
			}
			// 标题行固定在面板顶部，账户列表放进滚动容器：窗口矮或供应商多时只滚列
			// 表，不把弹层顶出屏幕。列表高度扣掉面板内边距、标题行与间距后不超过上限。
			listMax := maxHeight
			if listMax > 0 {
				// 面板上下内边距 Space4*2、标题行 ControlSM、列表上方间距 Space4；余额行约 ControlXS 再加同样的间距。
				listMax -= Space4*2 + ControlSM + Space4
				if hasCredits {
					listMax -= ControlXS + Space4
				}
				if errText != "" {
					listMax -= ControlXS + Space4
				}
				listMax = max(listMax, 40)
			}
			list := ui.Scroll(c.Key("composer-quota-popover-scroll")).
				Label("额度列表").
				FillWidth().
				Grow(1).
				Shrink(1).
				ScrollbarInsets(4, 0, 4, 0)
			if listMax > 0 {
				list.MaxHeight(listMax)
			}
			list.Children(func() {
				renderQuotaGroupList(c, "composer-quota-", groups, quotaGroupListOptions{HideSingleGroupTitle: true})
			})
			return
		}

		loading, quotaErr := composerQuotaStatus(s, provider)
		status := chatUsageStatus(s, provider, usagePanelNow())
		switch {
		case quotaErr != "":
			if status.State == usageStateOK {
				status = usageStatusFor(quotaErr, nil, time.Time{}, usagePanelNow())
			}
			ui.Row(c.Key("composer-quota-popover-error")).FillWidth().Padding(0, Space4).Children(func() {
				renderUsageStatusLine(c, status, FontSizeCallout)
			})
		case loading:
			ui.Row(c.Key("composer-quota-popover-loading")).
				FillWidth().
				Padding(0, Space4).
				Gap(Space2).
				AlignItems(ui.Center).
				Children(func() {
					renderRunningGlyph(c, "quota-popover-loading", IconSM, ThemeSecondaryText(c))
					ui.Text(c, "正在加载额度…").
						FontSize(FontSizeCallout).
						TextColor(ThemeSecondaryText(c))
				})
		default:
			ui.Row(c.Key("composer-quota-popover-empty")).FillWidth().Padding(0, Space4).Children(func() {
				ui.Text(c, "暂无额度数据").
					FontSize(FontSizeCallout).
					TextColor(ThemeSecondaryText(c))
			})
		}
	})
}
