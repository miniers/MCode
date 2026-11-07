package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"strings"
	"sync"
	"time"

	"mcode/internal/config"
	"mcode/internal/providers/agy/accounts"
	"mcode/internal/providers/agy/quota"
	"mcode/internal/providers/claude"
	"mcode/internal/providers/codex"
	"mcode/internal/providers/credits"
	"mcode/internal/providers/loginenv"
	"mcode/internal/remote/wire"
)

// codexQuotaHook 是 Codex 额度读取源，见 SetCodexQuotaReader。
var codexQuotaHook struct {
	sync.RWMutex
	read func(ctx context.Context, threadID string) (codex.RateLimits, error)
}

// SetCodexQuotaReader 设置 Codex 额度读取器（由 app 层在启动时调用），让远程端与桌面端
// 用同一份 Codex 配置（二进制、工作目录、CODEX_HOME）判定额度来源。传 nil 清除注入。
func SetCodexQuotaReader(read func(ctx context.Context, threadID string) (codex.RateLimits, error)) {
	codexQuotaHook.Lock()
	codexQuotaHook.read = read
	codexQuotaHook.Unlock()
}

// readCodexQuotaForThread 用注入的读取器读取额度，并把会话 ID 带过去定位会话工作目录。
// 没有注入时返回错误：此时无法确认运行时来源，不能回退到本机登录的额度。
func readCodexQuotaForThread(ctx context.Context, threadID string) (codex.RateLimits, error) {
	codexQuotaHook.RLock()
	read := codexQuotaHook.read
	codexQuotaHook.RUnlock()
	if read == nil {
		return codex.RateLimits{}, fmt.Errorf("codex 额度读取器未注入，无法判定额度来源")
	}
	return read(ctx, threadID)
}

// readClaudePlanLimits 可在单元测试中被替换为桩函数。
var readClaudePlanLimits = claude.ReadPlanLimits

// readProviderCredits 读取预付费余额，可在单元测试中被替换为桩函数。
var readProviderCredits = credits.Read

// readProviderPlanWindows 读取方案额度窗口，可在单元测试中被替换为桩函数。
var readProviderPlanWindows = credits.ReadPlanWindows

// lookupProviderAPIKey 按 provider 读取凭据库中的 API key；
// 凭据的统一注入尚未实现，这里直接按 "<provider>.apiKey" 取值。
var lookupProviderAPIKey = func(provider string) string {
	key, err := config.NewSecretStore("mcode").Get(provider + ".apiKey")
	if err != nil {
		return ""
	}
	return key
}

// providerCreditsOptions 构造一次 credits 读取所需的依赖：密钥来自凭据库，
// 环境变量取自登录 Shell 环境（GUI 进程自己的环境里缺 shell profile 导出的变量）与主目录取自当前进程。
func providerCreditsOptions(provider string) credits.Options {
	env := make(map[string]string)
	for _, kv := range loginenv.Environ() {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			env[kv[:i]] = kv[i+1:]
		}
	}
	home, _ := os.UserHomeDir()
	return credits.Options{
		APIKey:  lookupProviderAPIKey(provider),
		Env:     env,
		HomeDir: home,
	}
}

// providerLabel 返回 provider 的展示名。
func providerLabel(provider string) string {
	if provider == "" {
		return provider
	}
	return strings.ToUpper(provider[:1]) + provider[1:]
}

// quotaPeriodFromWindow 推断额度窗口的周期标记（5h、7d、1d、30d）：
// 先按窗口 ID 映射，ID 认不出时再按标题文本推断，都认不出返回 nil。
func quotaPeriodFromWindow(id, label string) *string {
	lowerID := strings.ToLower(strings.TrimSpace(id))
	switch lowerID {
	case "five_hour", "session":
		return stringPtr("5h")
	case "seven_day", "weekly_all":
		return stringPtr("7d")
	}
	if strings.HasPrefix(lowerID, "weekly") || strings.HasPrefix(lowerID, "seven_day") {
		return stringPtr("7d")
	}
	text := strings.ToLower(label)
	switch {
	case strings.Contains(text, "5-hour"), strings.Contains(text, "5h"), strings.Contains(text, "5 小时"):
		return stringPtr("5h")
	case strings.Contains(text, "weekly"), strings.Contains(text, "week"), strings.Contains(text, "7d"),
		strings.Contains(text, "7-day"), strings.Contains(text, "周"):
		return stringPtr("7d")
	case strings.Contains(text, "daily"), strings.Contains(text, "1d"), strings.Contains(text, "日"):
		return stringPtr("1d")
	case strings.Contains(text, "monthly"), strings.Contains(text, "30d"), strings.Contains(text, "月"):
		return stringPtr("30d")
	}
	return nil
}

// splitQuotaAccount 把窗口拆成「所属账号」与「去掉账号前缀的窗口名」：
// 优先采用数据源给出的账号，否则按「 · 」切分标题，首段含 @ 时视为账号。
// 语义与 internal/ui/composer_usage_panel.go 的 quotaWindowAccount 一致。
func splitQuotaAccount(account, label string) (acct, title string) {
	label = strings.TrimSpace(label)
	if acct = strings.TrimSpace(account); acct != "" {
		title = strings.TrimSpace(strings.TrimPrefix(label, acct+" · "))
		return acct, title
	}
	if key, rest, ok := strings.Cut(label, " · "); ok && strings.Contains(key, "@") {
		return strings.TrimSpace(key), strings.TrimSpace(rest)
	}
	return "", label
}

// quotaWindowInput 是分账号归组前的单个额度窗口。
type quotaWindowInput struct {
	Account     string
	ID          string
	Label       string
	Period      *string
	UsedPercent float64
	ResetsAt    *string
	Active      bool
	Disabled    bool
	Source      string
	Plan        string
	Display     string
	Text        bool
	Unlimited   bool
	Balance     *float64
	Unit        string
	ReadAt      *string
}

// quotaBudgetsByAccount 按账号把窗口拆成多条 budget，账号与窗口都保持首次出现的顺序；
// 只有一个账号（或没有账号）时只产出一条与 base 相同的 budget。
func quotaBudgetsByAccount(base wire.RemoteQuotaBudget, windows []quotaWindowInput) []wire.RemoteQuotaBudget {
	budgets := make([]wire.RemoteQuotaBudget, 0, 1)
	index := make(map[string]int, 1)
	for _, w := range windows {
		acct, title := splitQuotaAccount(w.Account, w.Label)
		i, ok := index[acct]
		if !ok {
			i = len(budgets)
			index[acct] = i
			b := base
			b.Windows = make([]wire.RemoteQuotaWindow, 0, len(windows))
			if acct != "" {
				b.Account = &acct
			}
			budgets = append(budgets, b)
		}
		window := wire.RemoteQuotaWindow{
			Period:      w.Period,
			Title:       title,
			Label:       title,
			UsedPercent: w.UsedPercent,
			ResetsAt:    w.ResetsAt,
			Account:     acct,
			Active:      w.Active,
			Disabled:    w.Disabled,
			Source:      w.Source,
			Plan:        w.Plan,
			Display:     w.Display,
			Text:        w.Text,
			Unlimited:   w.Unlimited,
			Balance:     w.Balance,
			Unit:        w.Unit,
			ReadAt:      w.ReadAt,
		}
		if w.ID != "" {
			window.ID = stringPtr(w.ID)
		}
		budgets[i].Windows = append(budgets[i].Windows, window)
	}
	return budgets
}

// quotaAccountTracker 记录每个 provider+账号最近一次看到的「窗口标题 → 已用百分比」快照
// 和额度变动时间，用于在同 provider 有多个账号、数据源又不给在用标记时推断在用账号。
// 语义与桌面端 internal/ui/composer_usage_panel.go 的 observeQuotaAccounts 保持一致。
type quotaAccountTracker struct {
	mu      sync.Mutex
	seen    map[string]map[string]float64 // key -> 窗口标题 -> 已用百分比
	changed map[string]time.Time          // key -> 额度变动时间
	// now 可在测试中替换，默认 time.Now。
	now func() time.Time
}

// quotaTrackers 按 MemoryBackend 存跟踪器，避免改动 MemoryBackend 结构体。
var quotaTrackers sync.Map // map[*MemoryBackend]*quotaAccountTracker

// quotaTracker 取（或首次创建）该 backend 的额度账号跟踪器。
func (b *MemoryBackend) quotaTracker() *quotaAccountTracker {
	if v, ok := quotaTrackers.Load(b); ok {
		return v.(*quotaAccountTracker)
	}
	v, _ := quotaTrackers.LoadOrStore(b, &quotaAccountTracker{
		seen:    make(map[string]map[string]float64),
		changed: make(map[string]time.Time),
		now:     time.Now,
	})
	return v.(*quotaAccountTracker)
}

// currentTime 取一次跟踪器的时钟读数；优先用测试替换的 now，否则用 time.Now。
// 同一次读取中所有账号必须共用同一个读数，否则后观察的账号会稍晚，首次读取会误判为
// 不同的变动时间。
func (t *quotaAccountTracker) currentTime() time.Time {
	t.mu.Lock()
	now := t.now
	t.mu.Unlock()
	if now == nil {
		now = time.Now
	}
	return now()
}

// observe 用一次结果更新某个 provider+账号的快照：第一次看到、或任一窗口的已用百分比
// 与上次不同，就把变动时间记为 at（同一次读取的所有账号共用同一个 at）；没变化则保留
// 原变动时间。
func (t *quotaAccountTracker) observe(provider, account string, used map[string]float64, at time.Time) {
	key := provider + "\x00" + account
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.seen == nil {
		t.seen = make(map[string]map[string]float64)
	}
	if t.changed == nil {
		t.changed = make(map[string]time.Time)
	}
	prev, seen := t.seen[key]
	if !seen || !maps.Equal(prev, used) {
		t.changed[key] = at
	}
	t.seen[key] = used
}

// changedAt 返回某账号额度的最近变动时间；从未变动过时为零值时间。
func (t *quotaAccountTracker) changedAt(provider, account string) time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.changed[provider+"\x00"+account]
}

// markActiveQuotaAccounts 按 provider 分组处理带账号的 budget：先更新各账号的额度快照；
// 若该 provider 有至少两个账号且没有任何 budget 已带 Active 标记，就推断出在用账号并只给它
// 打上 Active。规则与桌面端 quotaInUseAccount 一致：取额度最近有变动的账号，变动时间相同
// （含首次读取）时取列表中的第一个；只有一个账号时不设置 Active。
func (b *MemoryBackend) markActiveQuotaAccounts(budgets []wire.RemoteQuotaBudget) {
	if len(budgets) == 0 {
		return
	}
	tracker := b.quotaTracker()
	// 同一次读取只取一次时间，所有账号共用，首次读取（并列）时才会取列表第一个。
	at := tracker.currentTime()
	providers := make([]string, 0, 1)
	byProvider := make(map[string][]int)
	marked := make(map[string]bool)
	for i := range budgets {
		budget := &budgets[i]
		if budget.Account == nil || *budget.Account == "" {
			continue
		}
		provider := budget.Provider
		if _, ok := byProvider[provider]; !ok {
			providers = append(providers, provider)
		}
		byProvider[provider] = append(byProvider[provider], i)
		if budget.Active != nil && *budget.Active {
			marked[provider] = true
		}
		if len(budget.Windows) == 0 {
			continue
		}
		used := make(map[string]float64, len(budget.Windows))
		for _, w := range budget.Windows {
			used[w.Title] = w.UsedPercent
		}
		tracker.observe(provider, *budget.Account, used, at)
	}
	for _, provider := range providers {
		indexes := byProvider[provider]
		if len(indexes) < 2 || marked[provider] {
			continue
		}
		chosen := -1
		var chosenAt time.Time
		for _, i := range indexes {
			seenAt := tracker.changedAt(provider, *budgets[i].Account)
			if seenAt.IsZero() {
				continue
			}
			if chosen < 0 || seenAt.After(chosenAt) {
				chosen, chosenAt = i, seenAt
			}
		}
		if chosen < 0 {
			chosen = indexes[0]
		}
		active := true
		budgets[chosen].Active = &active
		// 窗口级的 Active 与 budget 保持一致，供只读窗口列表的客户端使用。
		for j := range budgets[chosen].Windows {
			budgets[chosen].Windows[j].Active = true
		}
	}
}

func (b *MemoryBackend) SetAgyAccounts(m *accounts.Manager) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.agyAccounts = m
}

// ComposerQuota 读取当前账号的本地额度缓存，不让上游网络故障阻塞会话。
// 同 provider 有多个账号时，推断出在用账号并给它打上 Active 标记。
func (b *MemoryBackend) ComposerQuota(ctx context.Context, p wire.RemoteComposerQuotaParams) (wire.RemoteComposerQuotaResult, error) {
	result, err := b.readComposerQuota(ctx, p)
	if err != nil {
		return result, err
	}
	// Magpie 的 Codex 卡片证明不了当前会话用哪个账号，不能按额度变动推断「在用」。
	if !allCodexBudgets(result.Budgets) {
		b.markActiveQuotaAccounts(result.Budgets)
	}
	return result, nil
}

// allCodexBudgets 判断 budgets 是否全是 Codex 的：这类多账号卡片不带可信的在用标记。
func allCodexBudgets(budgets []wire.RemoteQuotaBudget) bool {
	if len(budgets) == 0 {
		return false
	}
	for _, budget := range budgets {
		if budget.Provider != "codex" {
			return false
		}
	}
	return true
}

// unknownQuotaResult 返回一条「额度不可用」的 budget：界面据此显示不可用，不回退到本机额度。
func unknownQuotaResult(provider, providerName string) wire.RemoteComposerQuotaResult {
	return wire.RemoteComposerQuotaResult{Budgets: []wire.RemoteQuotaBudget{{
		Provider:     provider,
		ProviderName: providerName,
		Role:         "lead",
		State:        "unknown",
		Windows:      make([]wire.RemoteQuotaWindow, 0),
	}}}
}

// remoteQuotaWindow 把 codex 的额度窗口转成 wire 结构。account/plan/source 是窗口
// 所属账号的套餐与来源，readAt 是来源端的读取时间（零值表示未知），与桌面端
// internal/app/client.go 的 codexQuotaWindows 保持同一组字段。
func remoteQuotaWindow(w codex.RateLimitWindow, account, plan, source string, readAt time.Time) wire.RemoteQuotaWindow {
	var period *string
	if w.Period != "" {
		p := w.Period
		period = &p
	}
	var reset *string
	if w.ResetsAt != nil {
		r := w.ResetsAt.UTC().Format(time.RFC3339)
		reset = &r
	}
	window := wire.RemoteQuotaWindow{
		Period:      period,
		Title:       w.Title,
		Label:       w.Title,
		UsedPercent: w.UsedPercent,
		ResetsAt:    reset,
		Account:     account,
		Source:      source,
		Plan:        plan,
	}
	if w.ID != "" {
		window.ID = stringPtr(w.ID)
	}
	if !readAt.IsZero() {
		window.ReadAt = stringPtr(readAt.UTC().Format(time.RFC3339))
	}
	return window
}

// readComposerQuota 组装 composer.quota 的 budgets，不处理在用账号标记。
func (b *MemoryBackend) readComposerQuota(ctx context.Context, p wire.RemoteComposerQuotaParams) (wire.RemoteComposerQuotaResult, error) {
	b.mu.Lock()
	mgr := b.agyAccounts
	provider := b.defaultProvider
	conversationID := ""
	// model 是会话当前模型，pi 线程要靠它认出额度属于哪个 Magpie 端点。
	model := ""

	if p.ThreadID != "" {
		td, ok := b.threads[p.ThreadID]
		if !ok {
			b.mu.Unlock()
			return wire.RemoteComposerQuotaResult{}, &wire.RPCError{
				Code:    wire.ErrCodeResourceNotFound,
				Message: fmt.Sprintf("thread %s not found", p.ThreadID),
			}
		}
		provider = td.summary.Provider
		if provider == "" {
			provider = b.defaultProvider
		}
		conversationID = td.sessionID
		if td.summary.Model != nil {
			model = *td.summary.Model
		}
	} else if p.ProjectID != "" {
		if _, ok := b.projects[p.ProjectID]; !ok {
			b.mu.Unlock()
			return wire.RemoteComposerQuotaResult{}, &wire.RPCError{
				Code:    wire.ErrCodeResourceNotFound,
				Message: fmt.Sprintf("project %s not found", p.ProjectID),
			}
		}
	}
	b.mu.Unlock()

	result := wire.RemoteComposerQuotaResult{
		Budgets: make([]wire.RemoteQuotaBudget, 0),
	}
	if provider == "codex" {
		limits, err := readCodexQuotaForThread(ctx, p.ThreadID)
		if err != nil {
			return unknownQuotaResult("codex", "Codex"), nil
		}
		if limits.Source == codex.QuotaSourceMagpie {
			// Magpie 每张卡片一个账号：账号、套餐与卡片读取时间各自保留，不用本机登录信息补。
			for _, acct := range limits.Accounts {
				budget := wire.RemoteQuotaBudget{
					Provider:     "codex",
					ProviderName: "Codex",
					Role:         "lead",
					State:        "used",
					Windows:      make([]wire.RemoteQuotaWindow, 0, len(acct.Windows)),
				}
				if acct.Account != "" {
					account := acct.Account
					budget.Account = &account
				}
				if acct.PlanType != "" {
					plan := acct.PlanType
					budget.Profile = &plan
				}
				if !acct.ReadAt.IsZero() {
					readAt := acct.ReadAt.UTC().Format(time.RFC3339)
					budget.ReadAt = &readAt
				}
				for _, w := range acct.Windows {
					budget.Windows = append(budget.Windows, remoteQuotaWindow(w, acct.Account, acct.PlanType, codex.QuotaSourceMagpie, acct.ReadAt))
				}
				result.Budgets = append(result.Budgets, budget)
			}
			if len(result.Budgets) == 0 {
				return unknownQuotaResult("codex", "Codex"), nil
			}
			return result, nil
		}
		if len(limits.Windows) == 0 {
			return unknownQuotaResult("codex", "Codex"), nil
		}
		budget := wire.RemoteQuotaBudget{
			Provider:     "codex",
			ProviderName: "Codex",
			Role:         "lead",
			State:        "used",
			Windows:      make([]wire.RemoteQuotaWindow, 0, len(limits.Windows)),
		}
		if limits.PlanType != "" {
			plan := limits.PlanType
			budget.Profile = &plan
		}
		// 直连的读取时间由 codex 包给出，缺失时不伪造为当前时间。
		if !limits.ReadAt.IsZero() {
			readAt := limits.ReadAt.UTC().Format(time.RFC3339)
			budget.ReadAt = &readAt
		}
		for _, w := range limits.Windows {
			budget.Windows = append(budget.Windows, remoteQuotaWindow(w, "", limits.PlanType, codex.QuotaSourceOpenAI, limits.ReadAt))
		}
		result.Budgets = append(result.Budgets, budget)
		return result, nil
	}
	if provider == "claude" {
		limits, err := readClaudePlanLimits(ctx)
		if err != nil || len(limits.Windows) == 0 {
			return result, nil
		}
		budget := wire.RemoteQuotaBudget{
			Provider:     "claude",
			ProviderName: "Claude",
			Role:         "lead",
			State:        "used",
		}
		if limits.PlanType != "" {
			budget.Profile = stringPtr(limits.PlanType)
		}
		readAt := time.Now().UTC().Format(time.RFC3339)
		budget.ReadAt = &readAt
		// Claude 网关多卡片时同一个响应里会有多个账号的窗口：按账号拆成多条 budget，
		// 窗口标题去掉「账号 · 」前缀，周期按 ID 或标题推断（PlanLimitWindow 没有在用标记，
		// Active 交给 ComposerQuota 按额度变动推断）。
		entries := make([]quotaWindowInput, 0, len(limits.Windows))
		for _, w := range limits.Windows {
			var reset *string
			if w.ResetsAt != nil {
				reset = stringPtr(w.ResetsAt.UTC().Format(time.RFC3339))
			}
			entries = append(entries, quotaWindowInput{
				Account:     w.Account,
				ID:          w.ID,
				Label:       w.Label,
				Period:      quotaPeriodFromWindow(w.ID, w.Label),
				UsedPercent: w.UsedPercent,
				ResetsAt:    reset,
			})
		}
		result.Budgets = append(result.Budgets, quotaBudgetsByAccount(budget, entries)...)
		return result, nil
	}
	// pi 线程的额度来自 Magpie 端点：模型 ID 的第一段是 pi provider id，端点地址与 key
	// 只有 app 层读得到，通过注入的读取器取（见 quota_pi.go）。
	if provider == "pi" {
		if budgets, ok := piQuotaBudgets(ctx, model); ok {
			result.Budgets = append(result.Budgets, budgets...)
		}
		return result, nil
	}
	// zai / cline / droid 通过 credits 包读取方案额度窗口；读取失败时只标 unknown，
	// 不返回错误，与文件开头「不让上游网络故障阻塞会话」的约定一致。
	switch provider {
	case "zai", "cline", "droid":
		windows, err := readProviderPlanWindows(ctx, provider, providerCreditsOptions(provider))
		readAt := time.Now().UTC().Format(time.RFC3339)
		budget := wire.RemoteQuotaBudget{
			Provider:     provider,
			ProviderName: providerLabel(provider),
			Role:         "lead",
			ReadAt:       &readAt,
		}
		if err != nil {
			budget.State = "unknown"
			budget.Windows = make([]wire.RemoteQuotaWindow, 0)
			result.Budgets = append(result.Budgets, budget)
		} else {
			budget.State = "used"
			entries := make([]quotaWindowInput, 0, len(windows))
			for _, w := range windows {
				var reset *string
				if !w.ResetsAt.IsZero() {
					reset = stringPtr(w.ResetsAt.UTC().Format(time.RFC3339))
				}
				// 数据源已给出周期时以它为准，否则按 ID 或标题推断。
				period := quotaPeriodFromWindow(w.ID, w.Label)
				if w.Period != "" {
					period = stringPtr(w.Period)
				}
				entries = append(entries, quotaWindowInput{
					ID:          w.ID,
					Label:       w.Label,
					Period:      period,
					UsedPercent: w.UsedPercent,
					ResetsAt:    reset,
				})
			}
			result.Budgets = append(result.Budgets, quotaBudgetsByAccount(budget, entries)...)
		}
	}
	// deepseek / cline / commandcode 报预付费余额：若上面已为同一 provider 追加过
	// budget 就补写金额，否则新建一条；读取失败时不追加也不报错。
	if credits.ExposesCredits(provider) {
		c, err := readProviderCredits(ctx, provider, providerCreditsOptions(provider))
		if err == nil {
			amount := c.AmountText()
			state := "used"
			if c.Depleted() {
				state = "attention"
			}
			index := -1
			for i := range result.Budgets {
				if result.Budgets[i].Provider == provider {
					index = i
					break
				}
			}
			if index >= 0 {
				result.Budgets[index].Amount = &amount
				result.Budgets[index].State = state
			} else {
				readAt := time.Now().UTC().Format(time.RFC3339)
				result.Budgets = append(result.Budgets, wire.RemoteQuotaBudget{
					Provider:     provider,
					ProviderName: providerLabel(provider),
					Role:         "lead",
					State:        state,
					Windows:      make([]wire.RemoteQuotaWindow, 0),
					Amount:       &amount,
					ReadAt:       &readAt,
				})
			}
		}
	}
	if provider != "agy" || mgr == nil {
		return result, nil
	}
	acc, ok := mgr.AccountForConversation(conversationID)
	if !ok {
		return result, nil
	}
	budget := wire.RemoteQuotaBudget{
		Provider: "agy", ProviderName: "Antigravity", Role: "lead",
		State: "unknown", Windows: make([]wire.RemoteQuotaWindow, 0),
	}
	profile := acc.Label
	if profile == "" {
		profile = acc.Email
	}
	if profile != "" {
		budget.Profile = &profile
		// 对话已绑定到该账户，即它是当前在用账户。
		active := true
		budget.Account = &profile
		budget.Active = &active
	}
	if acc.QuotaReadAt != "" {
		budget.ReadAt = &acc.QuotaReadAt
		budget.State = "used"
	}
	if acc.QuotaStale {
		budget.State = "stale"
	}
	if acc.Status != accounts.StatusActive {
		budget.State = "attention"
	}
	// 额度条默认只展示 Gemini 系列，Claude/GPT 等三方分组只在原生浮窗中展示
	group := quota.GroupGemini
	for _, period := range []string{"5h", "7d"} {
		key := group
		if period == "7d" {
			key += " 7d"
		}
		q, exists := acc.Quota[key]
		if !exists {
			continue
		}
		q = accounts.EffectiveQuota(q, acc.QuotaReadAt, time.Now())
		window := wire.RemoteQuotaWindow{
			ID: stringPtr(key), Period: &period, Title: group + " " + period, Label: group + " " + period,
			UsedPercent: q.UsedPercent, Account: profile, Disabled: !acc.On,
		}
		if budget.Active != nil {
			window.Active = *budget.Active
		}
		if q.ResetAt != "" {
			reset := q.ResetAt
			window.ResetsAt = &reset
		}
		budget.Windows = append(budget.Windows, window)
	}
	result.Budgets = append(result.Budgets, budget)
	return result, nil
}

// handleComposerQuota 校验参数并分发 composer.quota 请求。
func (d *Dispatcher) handleComposerQuota(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.RemoteComposerQuotaParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}

	hasThread := strings.TrimSpace(p.ThreadID) != ""
	hasProj := strings.TrimSpace(p.ProjectID) != ""
	if (hasThread && hasProj) || (!hasThread && !hasProj) {
		return replyErr(wire.InvalidParams, "Invalid params: 'composer.quota' requires exactly one of threadID or projectID UUID string")
	}

	if hasThread && !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}
	if hasProj && !isValidUUID(p.ProjectID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'projectID' must be a valid UUID")
	}

	mb, ok := d.memoryBackend()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "quota operations unavailable")
	}
	res, err := mb.ComposerQuota(ctx, p)
	if err != nil {
		return replyWithBackendErr(err)
	}
	return replySuccess(res)
}
