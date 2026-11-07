package credits

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// deepSeekDefaultBaseURL 是 DeepSeek API 的 baseURL。
const deepSeekDefaultBaseURL = "https://api.deepseek.com"

// deepSeekBalanceResponse 是 GET /user/balance 的响应：
// {"is_available": true, "balance_infos": [{"currency": "USD", "total_balance": "12.34",
// "granted_balance": "2.34", "topped_up_balance": "10.00"}]}
// 金额是字符串，所以按字符串解析而不是当数字解码。
type deepSeekBalanceResponse struct {
	IsAvailable  *bool                 `json:"is_available"`
	BalanceInfos []deepSeekBalanceInfo `json:"balance_infos"`
}

type deepSeekBalanceInfo struct {
	Currency        string          `json:"currency"`
	TotalBalance    json.RawMessage `json:"total_balance"`
	ToppedUpBalance json.RawMessage `json:"topped_up_balance"`
	GrantedBalance  json.RawMessage `json:"granted_balance"`
}

// readDeepSeek 读 DeepSeek 的预付费余额。
func readDeepSeek(ctx context.Context, opts Options) (Credits, error) {
	key := strings.TrimSpace(opts.APIKey)
	if key == "" {
		return Credits{}, fmt.Errorf("%w: deepseek 需要 API key", ErrMissingKey)
	}
	url := opts.baseURL(deepSeekDefaultBaseURL) + "/user/balance"
	status, body, err := getJSON(ctx, opts.client(), url, key)
	if err != nil {
		return Credits{}, err
	}
	switch {
	case status == 401 || status == 403:
		return Credits{}, fmt.Errorf("deepseek: 余额接口拒绝了 API key（HTTP %d）", status)
	case status != 200:
		return Credits{}, fmt.Errorf("deepseek: 余额接口返回 HTTP %d", status)
	}
	return parseDeepSeekBalance(body, opts.now())
}

// parseDeepSeekBalance 解析余额响应。
// 金额与币种来自 balance_infos；一个账户按币种分行，通常只有一条，
// 多条时优先取 USD、其次 CNY，都没有才退回第一条。is_available 缺失时按可用处理。
func parseDeepSeekBalance(raw []byte, now time.Time) (Credits, error) {
	if len(raw) == 0 {
		return Credits{}, fmt.Errorf("deepseek: 解析余额: 响应为空")
	}
	var resp deepSeekBalanceResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return Credits{}, fmt.Errorf("deepseek: 解析余额: %w", err)
	}
	info, ok := pickDeepSeekBalance(resp.BalanceInfos)
	if !ok {
		return Credits{}, fmt.Errorf("deepseek: 解析余额: 响应缺少 balance_infos")
	}
	total := parseFloatRaw(info.TotalBalance)
	if total == nil {
		return Credits{}, fmt.Errorf("deepseek: 解析余额: total_balance 非法")
	}
	usable := true
	if resp.IsAvailable != nil {
		usable = *resp.IsAvailable
	}
	return Credits{
		Provider: ProviderDeepSeek,
		Total:    *total,
		Currency: info.Currency,
		ToppedUp: parseFloatRaw(info.ToppedUpBalance),
		Granted:  parseFloatRaw(info.GrantedBalance),
		Usable:   usable,
		ReadAt:   now,
	}, nil
}

// pickDeepSeekBalance 挑出要展示的那一行余额。
func pickDeepSeekBalance(infos []deepSeekBalanceInfo) (deepSeekBalanceInfo, bool) {
	if len(infos) == 0 {
		return deepSeekBalanceInfo{}, false
	}
	for _, want := range []string{"USD", "CNY"} {
		for _, info := range infos {
			if strings.EqualFold(strings.TrimSpace(info.Currency), want) {
				return info, true
			}
		}
	}
	return infos[0], true
}
