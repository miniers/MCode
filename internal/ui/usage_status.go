package mcodeui

import (
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// usageState 是用量数据的展示状态：正常、未登录、不支持、出错、数据过期。
type usageState int

const (
	// usageStateOK 表示数据正常，或既没有数据也没有错误。
	usageStateOK usageState = iota
	// usageStateNeedsAuth 表示需要先登录该 Agent 账号。
	usageStateNeedsAuth
	// usageStateUnsupported 表示该 Agent 不提供额度接口。
	usageStateUnsupported
	// usageStateError 表示读取额度失败。
	usageStateError
	// usageStateStale 表示数据仍在，但已超过 usageStaleAfter 没有成功刷新。
	usageStateStale
)

// usageStaleAfter 是用量数据被视为过期的阈值：超过 15 分钟没成功刷新就打上过期标记。
const usageStaleAfter = 15 * time.Minute

const (
	// usageAuthHint 是未登录时给出的可执行指引。
	usageAuthHint = "请先登录该 Agent 账号：设置 → 模型与供应商"
	// usageUnsupportedHint 是不支持额度查询时的说明。
	usageUnsupportedHint = "该 Agent 不支持额度查询"
	// usageStaleSuffix 是追加在数据行尾的过期提示。
	usageStaleSuffix = "(数据可能已过期)"
)

// usageStatus 是一次用量状态判定结果。
type usageStatus struct {
	// State 是判定出的展示状态。
	State usageState
	// Detail 是后端原始错误文本，仅 usageStateError 时保留。
	Detail string
	// Message 是可展示的中文说明；正常与过期状态下为空（过期只在行尾加 usageStaleSuffix）。
	Message string
}

// Normal 表示状态不需要额外说明行（正常或过期）。
func (st usageStatus) Normal() bool {
	return st.State == usageStateOK || st.State == usageStateStale
}

// Stale 表示数据已过期，需要在数据行尾追加 usageStaleSuffix。
func (st usageStatus) Stale() bool {
	return st.State == usageStateStale
}

// usageStatusFor 判定用量数据状态（纯函数）：先按错误文本分类未登录/不支持/出错，
// 无错误时再按最近成功刷新时间判断是否过期。refreshedAt 为零值表示时间未知，不判过期。
func usageStatusFor(errText string, windows []ProviderQuotaWindow, refreshedAt, now time.Time) usageStatus {
	st := usageStatusForData(errText, len(windows) > 0, refreshedAt, now)
	// Magpie 卡片带来源端的读取时间：数据本身超过阈值没更新时同样算过期，
	// 不能因为刚重读过缓存就当成新鲜数据。
	if st.State == usageStateOK && quotaWindowsReadAtStale(windows, now) {
		return usageStatus{State: usageStateStale}
	}
	return st
}

// quotaWindowsReadAtStale 判断窗口来源端的读取时间是否已超过过期阈值。
// 只在所有窗口都带来源读取时间（Magpie 卡片）时判断；读取时间未知时不判过期。
func quotaWindowsReadAtStale(windows []ProviderQuotaWindow, now time.Time) bool {
	if len(windows) == 0 || now.IsZero() {
		return false
	}
	newest := time.Time{}
	for _, w := range windows {
		if w.ReadAt.IsZero() {
			return false
		}
		if w.ReadAt.After(newest) {
			newest = w.ReadAt
		}
	}
	return now.Sub(newest) > usageStaleAfter
}

// quotaWindowsFromMagpie 判断一组额度窗口是否来自 Magpie 网关缓存。
func quotaWindowsFromMagpie(windows []ProviderQuotaWindow) bool {
	for _, w := range windows {
		if w.Source == "magpie" {
			return true
		}
	}
	return false
}

// usageStatusForData 与 usageStatusFor 相同，但用 hasData 表示是否已有可展示的数据，
// 供没有窗口列表的调用方（如设置页方案限额条）复用同一套判定。
func usageStatusForData(errText string, hasData bool, refreshedAt, now time.Time) usageStatus {
	if text := strings.TrimSpace(errText); text != "" {
		switch {
		case isUsageAuthError(text):
			return usageStatus{State: usageStateNeedsAuth, Detail: text, Message: usageAuthHint}
		case isUsageUnsupportedError(text):
			return usageStatus{State: usageStateUnsupported, Detail: text, Message: usageUnsupportedHint}
		default:
			return usageStatus{State: usageStateError, Detail: text, Message: "加载额度失败: " + text}
		}
	}
	if hasData && !refreshedAt.IsZero() && !now.IsZero() && now.Sub(refreshedAt) > usageStaleAfter {
		return usageStatus{State: usageStateStale}
	}
	return usageStatus{State: usageStateOK}
}

// usageAuthErrorNeedles 是判定「未登录/需要授权」的错误关键字（小写比较）。
var usageAuthErrorNeedles = []string{
	"401",
	"unauthorized",
	"unauthenticated",
	"needs-auth",
	"needs auth",
	"authentication",
	"not logged in",
	"not login",
	"login required",
	"please log in",
	"credential",
	"token expired",
	"token 过期",
	"invalid token",
	"invalid_grant",
	"auth",
	"未登录",
	"登录",
	"授权",
	"凭证",
}

// usageUnsupportedErrorNeedles 是判定「不支持额度查询」的错误关键字（小写比较）。
var usageUnsupportedErrorNeedles = []string{
	"not implemented",
	"unsupported",
	"not supported",
	"does not support",
	"不支持",
	"未实现",
}

// isUsageAuthError 判断错误文本是否属于未登录/需要授权。
func isUsageAuthError(text string) bool {
	return containsAnyNeedle(text, usageAuthErrorNeedles)
}

// isUsageUnsupportedError 判断错误文本是否属于不支持额度查询。
func isUsageUnsupportedError(text string) bool {
	return containsAnyNeedle(text, usageUnsupportedErrorNeedles)
}

// containsAnyNeedle 判断文本（忽略大小写）是否包含任一关键字。
func containsAnyNeedle(text string, needles []string) bool {
	lower := strings.ToLower(text)
	for _, needle := range needles {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

// chatUsageStatus 读取 chatState 的额度缓存并判定某个 provider 的展示状态。
// 正在加载时不判过期，加载完成时间取 quotaLoadedProviders 里记录的最近一次读取时间。
func chatUsageStatus(s *chatState, provider string, now time.Time) usageStatus {
	if s == nil {
		return usageStatus{State: usageStateOK}
	}
	provider = strings.TrimSpace(provider)
	s.mu.Lock()
	loading := s.quotaLoadingProviders[provider]
	errText := s.quotaErrs[provider]
	windows := s.quotaWindowsByProvider[provider]
	refreshedAt := s.quotaLoadedProviders[provider]
	s.mu.Unlock()
	if loading {
		return usageStatus{State: usageStateOK}
	}
	return usageStatusFor(errText, windows, refreshedAt, now)
}

// renderUsageStatusLine 渲染状态说明行：未登录/不支持用次要灰，出错用警告色并保留原错误原文；
// 正常与过期状态不渲染说明行（过期由调用方在数据行尾追加 usageStaleSuffix）。
func renderUsageStatusLine(c *ui.Context, st usageStatus, fontSize float32) {
	message := strings.TrimSpace(st.Message)
	if message == "" {
		return
	}
	switch st.State {
	case usageStateNeedsAuth, usageStateUnsupported:
		ui.Text(c, message).
			FontSize(fontSize).
			TextColor(ThemeSecondaryText(c))
	case usageStateError:
		ui.Text(c, message).
			FontSize(fontSize).
			TextColor(ThemeWarning(c))
	}
}

// usageStatusStripLabel 返回额度条上表示状态的整体短标签；正常与过期返回空串。
func usageStatusStripLabel(st usageStatus) string {
	switch st.State {
	case usageStateNeedsAuth:
		return "需登录"
	case usageStateUnsupported:
		return "不支持额度"
	case usageStateError:
		return "额度不可用"
	default:
		return ""
	}
}

// usageStatusColor 返回状态短标签的颜色：出错用警告色，未登录/不支持用次要灰。
func usageStatusColor(c *ui.Context, st usageStatus) ui.Color {
	if st.State == usageStateError {
		return ThemeWarning(c)
	}
	return ThemeSecondaryText(c)
}
