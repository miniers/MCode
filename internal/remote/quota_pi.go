package remote

// 本文件把 pi 线程的额度接到 Magpie 端点上：pi 线程的 provider 是 "pi"，模型 ID 形如
// 「magpie-jp/auto/deepseek-v4.1-flash」，第一段是 pi provider id，额度按端点区分。
// 端点地址与 key 只有 app 层读得到（设置里的自定义端点 + 凭据库），所以这里只留一个
// 读取钩子由 app 层注入；没注入时 pi 线程不产出额度，与桌面端映射不到端点时的表现一致。

import (
	"context"
	"strings"
	"sync"
	"time"

	"mcode/internal/remote/wire"
)

// PiQuotaWindow 是 Magpie 某张卡片的一个额度窗口，由 app 层从 magpie.ReadQuotas 的结果
// 转换而来，字段与桌面端的 ui.ProviderQuotaWindow 对齐。
type PiQuotaWindow struct {
	// Account 是卡片所属账号：通常是上游 provider id，同一 provider 有多张卡片时
	// 带「 · 标识」（如「claude · 家里的号」）。
	Account string
	// ID 是 "账号/窗口名" 形式的窗口标识。
	ID          string
	Label       string
	Period      string
	UsedPercent float64
	// ResetsAt 为零值表示数据源没给出重置时间。
	ResetsAt time.Time
	// Active 表示这张卡是当前在用的账号（桥回传的实际路由命中它）。
	Active bool
	// Plan 是该窗口所属账号的套餐，为空表示未知。
	Plan string
	// Display 是 Magpie 给的展示文字，例如 $12.30 / $40。
	Display string
	// Text 为 true 表示只有文字、没有占比的窗口，UsedPercent 不可用。
	Text bool
	// Unlimited 为 true 表示不限量。
	Unlimited bool
	// Balance 是剩余数量，nil 表示未提供。
	Balance *float64
	// Unit 是额度单位（如 "credits"）。
	Unit string
	// Disabled 表示账号已停用（路由覆盖关了它）。
	Disabled bool
	// Source 是额度数据来源：magpie 表示来自 Magpie 网关缓存，空表示直连。
	Source string
	// ReadAt 是数据在来源端的读取时间，零值表示未知。
	ReadAt time.Time
}

// PiQuotaSource 是一次 pi 额度读取的结果：端点展示名与窗口列表。
type PiQuotaSource struct {
	Name    string
	Windows []PiQuotaWindow
}

// piQuotaHook 是 pi 额度的读取源，见 SetPiQuotaReader。
var piQuotaHook struct {
	sync.RWMutex
	read func(ctx context.Context, providerID string) (PiQuotaSource, bool)
}

// SetPiQuotaReader 设置 pi 线程的额度读取器（由 app 层在启动时调用）：
// 按 pi provider id 读对应 Magpie 端点的额度，第二个返回值为 false 表示映射不到
// 有 key 的端点（线程用的是 pi 自带的 provider）。传 nil 表示恢复成不产出额度。
func SetPiQuotaReader(read func(ctx context.Context, providerID string) (PiQuotaSource, bool)) {
	piQuotaHook.Lock()
	piQuotaHook.read = read
	piQuotaHook.Unlock()
}

// piQuotaReader 取当前注入的读取器，未注入时返回 nil。
func piQuotaReader() func(ctx context.Context, providerID string) (PiQuotaSource, bool) {
	piQuotaHook.RLock()
	defer piQuotaHook.RUnlock()
	return piQuotaHook.read
}

// piThreadProviderID 取 pi 模型 ID 里的 provider 段：模型形如
// 「magpie-jp/auto/deepseek-v4.1-flash」，第一段就是 pi provider id。
// 没有斜杠或第一段为空时返回空串。语义与桌面端 threadQuotaProvider 一致。
func piThreadProviderID(model string) string {
	head, _, ok := strings.Cut(strings.TrimSpace(model), "/")
	if !ok {
		return ""
	}
	return strings.TrimSpace(head)
}

// piQuotaBudgets 读取 pi 线程所属 Magpie 端点的额度并组装成 budgets。
// 与 claude 一致，端点多张卡片（多个上游账号）时按账号拆成多条 budget；
// 数据源给出在用标记时给对应账号打上 Active。映射不到端点或没有窗口时返回 false。
func piQuotaBudgets(ctx context.Context, model string) ([]wire.RemoteQuotaBudget, bool) {
	read := piQuotaReader()
	if read == nil {
		return nil, false
	}
	providerID := piThreadProviderID(model)
	if providerID == "" {
		return nil, false
	}
	source, ok := read(ctx, providerID)
	if !ok || len(source.Windows) == 0 {
		return nil, false
	}

	name := strings.TrimSpace(source.Name)
	if name == "" {
		name = "Pi"
	}
	readAt := time.Now().UTC().Format(time.RFC3339)
	base := wire.RemoteQuotaBudget{
		Provider:     "pi",
		ProviderName: name,
		Role:         "lead",
		State:        "used",
		ReadAt:       &readAt,
	}

	activeAccounts := make(map[string]bool, 1)
	entries := make([]quotaWindowInput, 0, len(source.Windows))
	for _, w := range source.Windows {
		var reset *string
		if !w.ResetsAt.IsZero() {
			reset = stringPtr(w.ResetsAt.UTC().Format(time.RFC3339))
		}
		var readAt *string
		if !w.ReadAt.IsZero() {
			readAt = stringPtr(w.ReadAt.UTC().Format(time.RFC3339))
		}
		// 数据源已给出周期时以它为准，否则按窗口 ID 或标题推断。
		period := quotaPeriodFromWindow(w.ID, w.Label)
		if given := strings.TrimSpace(w.Period); given != "" {
			period = stringPtr(given)
		}
		account := strings.TrimSpace(w.Account)
		entries = append(entries, quotaWindowInput{
			Account:     account,
			ID:          w.ID,
			Label:       w.Label,
			Period:      period,
			UsedPercent: w.UsedPercent,
			ResetsAt:    reset,
			Active:      w.Active,
			Disabled:    w.Disabled,
			Source:      w.Source,
			Plan:        w.Plan,
			Display:     w.Display,
			Text:        w.Text,
			Unlimited:   w.Unlimited,
			Balance:     w.Balance,
			Unit:        w.Unit,
			ReadAt:      readAt,
		})
		if w.Active {
			activeAccounts[account] = true
		}
	}

	budgets := quotaBudgetsByAccount(base, entries)
	for i := range budgets {
		account := ""
		if budgets[i].Account != nil {
			account = strings.TrimSpace(*budgets[i].Account)
		}
		if !activeAccounts[account] {
			continue
		}
		active := true
		budgets[i].Active = &active
		for j := range budgets[i].Windows {
			budgets[i].Windows[j].Active = true
		}
	}
	return budgets, true
}
