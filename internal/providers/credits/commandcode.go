package credits

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// commandCodeDefaultBaseURL 是 Command Code API 的默认 baseURL。
const commandCodeDefaultBaseURL = "https://api.commandcode.ai"

// readCommandCode 读 Command Code 的预付费余额（GET /alpha/billing/credits）。
// 密钥来源与 CLI 一致：APIKey 参数、COMMAND_CODE_API_KEY 环境变量、最后是
// `~/.commandcode/auth.json`（`cmd login` 写入的 apiKey）。
func readCommandCode(ctx context.Context, opts Options) (Credits, error) {
	key, err := commandCodeAPIKey(opts)
	if err != nil {
		return Credits{}, err
	}
	url := opts.baseURL(commandCodeDefaultBaseURL) + "/alpha/billing/credits"
	status, body, err := getJSON(ctx, opts.client(), url, key)
	if err != nil {
		return Credits{}, err
	}
	switch {
	case status == 401 || status == 403:
		return Credits{}, fmt.Errorf("commandcode: 余额接口拒绝了密钥（HTTP %d）", status)
	case status != 200:
		return Credits{}, fmt.Errorf("commandcode: 余额接口返回 HTTP %d", status)
	}
	return parseCommandCodeCredits(body, opts.now())
}

// commandCodeAPIKey 依次从 Options.APIKey、环境变量、本地配置文件里解析密钥。
func commandCodeAPIKey(opts Options) (string, error) {
	if key := strings.TrimSpace(opts.APIKey); key != "" {
		return key, nil
	}
	if key := strings.TrimSpace(opts.envValue("COMMAND_CODE_API_KEY")); key != "" {
		return key, nil
	}
	if key := commandCodeStoredKey(opts); key != "" {
		return key, nil
	}
	return "", fmt.Errorf("%w: commandcode 需要 API key", ErrMissingKey)
}

// commandCodeStoredKey 读 `~/.commandcode/auth.json` 里的 apiKey。
func commandCodeStoredKey(opts Options) string {
	home := opts.home()
	if home == "" {
		return ""
	}
	file := filepath.Join(home, ".commandcode", "auth.json")
	data, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return ""
	}
	return strings.TrimSpace(jsonText(root, "apiKey"))
}

// parseCommandCodeCredits 解析余额响应：
// {"credits": {"monthlyCredits": N, "purchasedCredits": N, "freeCredits": N, "belowThreshold": bool}}
// 三部分都是美元；below_threshold 为 true 表示账户已不能再被扣费。
func parseCommandCodeCredits(raw []byte, now time.Time) (Credits, error) {
	if len(raw) == 0 {
		return Credits{}, fmt.Errorf("commandcode: 解析余额: 响应为空")
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return Credits{}, fmt.Errorf("commandcode: 解析余额: %w", err)
	}
	credits := jsonRawObject(root["credits"])
	if credits == nil {
		return Credits{}, fmt.Errorf("commandcode: 解析余额: 响应缺少 credits")
	}
	monthly := floatOrZero(jsonFloat(credits, "monthlyCredits"))
	purchased := floatOrZero(jsonFloat(credits, "purchasedCredits"))
	free := floatOrZero(jsonFloat(credits, "freeCredits"))
	belowThreshold := jsonBool(credits, "belowThreshold")
	return Credits{
		Provider: ProviderCommandCode,
		Total:    monthly + purchased + free,
		Currency: "USD",
		ToppedUp: &purchased,
		Granted:  &free,
		Monthly:  &monthly,
		Usable:   belowThreshold == nil || !*belowThreshold,
		ReadAt:   now,
	}, nil
}

func floatOrZero(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}
