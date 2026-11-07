package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
)

// Codex 额度的实际来源。
const (
	// QuotaSourceOpenAI 是直连 OpenAI 的 ChatGPT 登录，额度由 account/rateLimits/read 给出。
	QuotaSourceOpenAI = "openai"
	// QuotaSourceMagpie 是 Magpie 网关，额度由网关缓存的账号卡片给出。
	QuotaSourceMagpie = "magpie"
)

// magpieCardsPath 是 Magpie 的账号额度卡片接口（相对网关根地址）。
const magpieCardsPath = "/v1/magpie/quotas/cards"

// quotaFetchTimeout 是一次额度读取（含启动 App Server）的整体超时。
const quotaFetchTimeout = 15 * time.Second

// quotaCacheTTL 是同一运行时额度结果的内存缓存时长。
const quotaCacheTTL = 60 * time.Second

// AccountQuota 是一张 Magpie Codex 账号卡片的额度。
type AccountQuota struct {
	// Account 是账号标识：优先 Magpie 里起的别名，其次账号（邮箱），都没有时用序号；
	// 来自远程 Magpie 的卡片带「来源 · 」前缀，避免与本机卡片重名。
	Account  string
	PlanType string
	// Provider 是 Magpie 的 provider id（Codex 卡片为 codex）。
	Provider string
	// User 是卡片的账号标识（邮箱或 login），不含「来源 · 」前缀。
	User string
	// Kind 是卡片种类：subscription、plan 或 balance。
	Kind string
	// Last 表示该账号是最近一次经网关服务的账号。
	Last bool
	// LastServedAt 是该账号最近一次经网关服务的时间，未知时为零值。
	LastServedAt time.Time
	// ReadAt 是卡片在来源端的原始读取时间，未知时为零值。
	ReadAt  time.Time
	Windows []RateLimitWindow
}

// QuotaOptions 指定读取额度时使用的 Codex 运行时，应与实际会话保持一致。
type QuotaOptions struct {
	// Binary 是 Codex 可执行文件；为空时按 PATH 查找 codex。
	Binary string
	// WorkDir 是会话工作目录（Codex 按 cwd 解析项目配置）；为空时用临时目录。
	WorkDir string
	// CodexHome 是追加给子进程的 CODEX_HOME；为空时继承登录环境。
	CodexHome string
}

type cachedQuotaLimits struct {
	limits RateLimits
	at     time.Time
}

var (
	rateLimitsMu    sync.Mutex
	rateLimitsCache = map[string]cachedQuotaLimits{}
)

// ReadRateLimits 按默认运行时（PATH 中的 codex 与继承的登录环境）读取额度。
func ReadRateLimits(ctx context.Context) (RateLimits, error) {
	return ReadQuotaLimits(ctx, QuotaOptions{})
}

// ResetRateLimitsCache 清除额度内存缓存，供单元测试使用。
func ResetRateLimitsCache() {
	rateLimitsMu.Lock()
	rateLimitsCache = map[string]cachedQuotaLimits{}
	rateLimitsMu.Unlock()
}

// ReadQuotaLimits 判定 Codex 实际使用的额度来源并读取：直连 OpenAI 的 ChatGPT 登录走
// account/rateLimits/read，Magpie 网关读该网关的账号卡片，其他网关、认证方式或读不到的
// 配置一律返回错误（调用方据此显示不可用，不能回退到本机登录的额度）。
// 结果按运行时（可执行文件、工作目录、CODEX_HOME）缓存 60 秒。
func ReadQuotaLimits(ctx context.Context, opts QuotaOptions) (RateLimits, error) {
	binPath, err := resolveQuotaBinary(opts.Binary)
	if err != nil {
		return RateLimits{}, err
	}
	key := quotaCacheKey(opts, binPath)

	rateLimitsMu.Lock()
	defer rateLimitsMu.Unlock()
	if entry, ok := rateLimitsCache[key]; ok && time.Since(entry.at) < quotaCacheTTL {
		return cloneRateLimits(entry.limits), nil
	}

	limits, err := readQuotaLimitsUncached(ctx, binPath, opts)
	if err != nil {
		return RateLimits{}, err
	}
	rateLimitsCache[key] = cachedQuotaLimits{limits: limits, at: time.Now()}
	return cloneRateLimits(limits), nil
}

// FetchRateLimits 以 binary 启动 App Server 读取一次额度，不走缓存。
func FetchRateLimits(ctx context.Context, binary string) (RateLimits, error) {
	binPath, err := resolveQuotaBinary(binary)
	if err != nil {
		return RateLimits{}, err
	}
	return readQuotaLimitsUncached(ctx, binPath, QuotaOptions{Binary: binary})
}

// quotaCacheKey 让不同来源（可执行文件）、工作目录与 CODEX_HOME 的额度互不串用。
func quotaCacheKey(opts QuotaOptions, binPath string) string {
	return strings.Join([]string{binPath, strings.TrimSpace(opts.WorkDir), strings.TrimSpace(opts.CodexHome)}, "\x00")
}

// resolveQuotaBinary 解析 Codex 可执行文件：先查登录环境的 PATH，再查本进程 PATH。
func resolveQuotaBinary(binary string) (string, error) {
	binary = strings.TrimSpace(binary)
	if binary == "" {
		binary = "codex"
	}
	if path, err := loginenv.LookPath(binary); err == nil {
		return path, nil
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		return "", fmt.Errorf("codex: binary %q not found: %w", binary, err)
	}
	return path, nil
}

// readQuotaLimitsUncached 启动一次 App Server 判定来源并读取额度。
func readQuotaLimitsUncached(ctx context.Context, binPath string, opts QuotaOptions) (RateLimits, error) {
	ctx, cancel := context.WithTimeout(ctx, quotaFetchTimeout)
	defer cancel()

	session, err := startQuotaSession(ctx, binPath, opts)
	if err != nil {
		return RateLimits{}, err
	}
	defer session.stop()

	route, err := resolveQuotaRoute(ctx, session.client)
	if err != nil {
		return RateLimits{}, err
	}
	if route.Source == QuotaSourceMagpie {
		return readMagpieQuota(ctx, route)
	}
	return readOpenAIQuota(ctx, session.client)
}

// codexAppServerSession 是一次额度读取用的 App Server 会话。
type codexAppServerSession struct {
	client *appClient
	stop   func()
}

// startQuotaSession 以与实际会话一致的二进制、工作目录、CODEX_HOME 与环境启动
// codex app-server，并完成 initialize 握手。
func startQuotaSession(ctx context.Context, binPath string, opts QuotaOptions) (*codexAppServerSession, error) {
	env, workDir := quotaProcessEnv(opts)

	cmd := procattr.HideWindow(exec.CommandContext(ctx, binPath, "app-server", "--stdio"))
	setProcessGroup(cmd)
	cmd.Cancel = func() error {
		return killProcessGroup(cmd, syscall.SIGTERM)
	}
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = workDir
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("codex: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("codex: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("codex: start %s: %w", binPath, err)
	}

	stop := func() {
		_ = stdin.Close()
		waitDone := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(waitDone)
		}()
		select {
		case <-waitDone:
		case <-time.After(500 * time.Millisecond):
			_ = killProcessGroup(cmd, syscall.SIGKILL)
			<-waitDone
		}
	}

	client := newAppClient(stdin)
	go readAppServerFrames(client, stdout)

	initParams := map[string]any{
		"clientInfo": map[string]any{
			"name":    "mcode",
			"version": "0.1.0",
		},
		"capabilities": map[string]any{
			"experimentalApi": true,
		},
	}
	if _, err := client.call(ctx, "initialize", initParams); err != nil {
		stop()
		return nil, fmt.Errorf("codex: initialize failed: %w", err)
	}
	_ = client.notify("initialized", nil)
	return &codexAppServerSession{client: client, stop: stop}, nil
}

// readAppServerFrames 逐行读取 App Server 输出：响应交给等待中的调用，服务端请求一律拒绝。
func readAppServerFrames(client *appClient, stdout io.Reader) {
	defer close(client.readerDoneCh)
	reader := bufio.NewReader(stdout)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimSpace(line)
			if len(line) > 0 {
				var rawMsg JSONRPCMessage
				if err := json.Unmarshal(line, &rawMsg); err == nil {
					switch {
					case rawMsg.IsResponse():
						client.mu.Lock()
						ch := client.pending[rawMsg.RawID()]
						client.mu.Unlock()
						if ch != nil {
							ch <- &rawMsg
						}
					case rawMsg.IsRequest():
						go func(reqMsg JSONRPCMessage) {
							reply := &JSONRPCMessage{
								JSONRPC: "2.0",
								ID:      reqMsg.ID,
								Result:  json.RawMessage(`{"decision":"decline"}`),
							}
							_ = client.send(reply)
						}(rawMsg)
					}
				}
			}
		}
		if readErr != nil {
			break
		}
	}

	client.mu.Lock()
	for idStr, ch := range client.pending {
		close(ch)
		delete(client.pending, idStr)
	}
	client.mu.Unlock()
}

// quotaProcessEnv 构造与 Codex 会话一致的环境：登录 Shell 环境 + 工作目录 + 显式 CODEX_HOME。
func quotaProcessEnv(opts QuotaOptions) (env []string, workDir string) {
	workDir = strings.TrimSpace(opts.WorkDir)
	if workDir == "" {
		workDir = os.TempDir()
	}
	var extra []string
	if home := strings.TrimSpace(opts.CodexHome); home != "" {
		extra = append(extra, "CODEX_HOME="+home)
	}
	return buildEnv(loginenv.Environ(), workDir, extra), workDir
}

// codexConfigReadResult 是 config/read 中判定额度来源需要的字段。
type codexConfigReadResult struct {
	Config struct {
		ModelProvider  string                        `json:"model_provider"`
		OpenAIBaseURL  string                        `json:"openai_base_url"`
		ModelProviders map[string]codexProviderEntry `json:"model_providers"`
	} `json:"config"`
}

// codexProviderEntry 是 config.model_providers 里的一项。
type codexProviderEntry struct {
	Name                    string `json:"name"`
	BaseURL                 string `json:"base_url"`
	EnvKey                  string `json:"env_key"`
	ExperimentalBearerToken string `json:"experimental_bearer_token"`
}

// quotaRoute 是一次读取确认后的额度来源。
type quotaRoute struct {
	Source string
	// BaseURL 是 Magpie 网关根地址（不含 /v1），Token 只用于该地址，不打印、不持久化。
	BaseURL string
	Token   string
}

// resolveQuotaRoute 用 Codex 的有效配置判定额度来源：真正直连 OpenAI 时返回 openai，
// 自定义网关只有经网关自身响应确认是 Magpie 时才返回 magpie，其余一律报错。
func resolveQuotaRoute(ctx context.Context, client *appClient) (quotaRoute, error) {
	raw, err := client.call(ctx, "config/read", map[string]any{})
	if err != nil {
		return quotaRoute{}, fmt.Errorf("codex: 读取配置失败: %w", err)
	}
	var res codexConfigReadResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return quotaRoute{}, fmt.Errorf("codex: 解析配置失败: %w", err)
	}

	cfg := res.Config
	providerID := strings.TrimSpace(cfg.ModelProvider)
	if providerID == "" {
		// Codex 未配置 model_provider 时用内置的 openai。
		providerID = "openai"
	}
	entry := cfg.ModelProviders[providerID]
	baseURL := strings.TrimSpace(entry.BaseURL)
	if strings.EqualFold(providerID, "openai") && baseURL == "" {
		baseURL = strings.TrimSpace(cfg.OpenAIBaseURL)
	}
	if baseURL == "" || isOpenAIHost(baseURL) {
		return quotaRoute{Source: QuotaSourceOpenAI}, nil
	}

	token := strings.TrimSpace(entry.ExperimentalBearerToken)
	if token == "" && strings.TrimSpace(entry.EnvKey) != "" {
		token = quotaEnvValue(entry.EnvKey)
	}
	root := gatewayRoot(baseURL)
	if root == "" || token == "" {
		return quotaRoute{}, fmt.Errorf("codex: 无法确认额度来源：provider %q 是自定义网关，但没有可用的网关密钥", providerID)
	}
	if !isMagpieGateway(ctx, root, token) {
		return quotaRoute{}, fmt.Errorf("codex: 无法确认额度来源：provider %q 的自定义网关不是 Magpie", providerID)
	}
	return quotaRoute{Source: QuotaSourceMagpie, BaseURL: root, Token: token}, nil
}

// isOpenAIHost 判断 base_url 是否指向 OpenAI 官方（直连），自定义主机一律不算。
func isOpenAIHost(baseURL string) bool {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return false
	}
	return host == "openai.com" || strings.HasSuffix(host, ".openai.com") ||
		host == "chatgpt.com" || strings.HasSuffix(host, ".chatgpt.com")
}

// gatewayRoot 由 Codex 的 base_url（带 /v1）推出网关根地址。
func gatewayRoot(baseURL string) string {
	root := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	root = strings.TrimSuffix(root, "/v1")
	return strings.TrimRight(root, "/")
}

// quotaEnvValue 从 Codex 子进程实际继承的登录环境里取值，取不到时退回本进程环境。
func quotaEnvValue(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	prefix := key + "="
	for _, kv := range loginenv.Environ() {
		if strings.HasPrefix(kv, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(kv, prefix))
		}
	}
	return strings.TrimSpace(os.Getenv(key))
}

// quotaHTTPClient 是读取网关额度的客户端：密钥只发给配置里的地址，重定向不跟随，
// 避免把密钥发给别的主机。
var quotaHTTPClient = &http.Client{
	Timeout: 10 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// quotaGet 发起一次带 Bearer key 的 GET 并读回响应体。
func quotaGet(ctx context.Context, target, token string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := quotaHTTPClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return body, resp.StatusCode, err
}

// isMagpieGateway 通过根地址的 {"name":"magpie"} 核验网关，不只看 provider 名字。
func isMagpieGateway(ctx context.Context, root, token string) bool {
	body, status, err := quotaGet(ctx, strings.TrimRight(root, "/")+"/", token)
	if err != nil || status != http.StatusOK {
		return false
	}
	var info struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(info.Name), "magpie")
}

// magpieCardsResponse 对应 /v1/magpie/quotas/cards 的返回：每个账号一张卡片。
type magpieCardsResponse struct {
	Data []magpieQuotaCard `json:"data"`
}

type magpieQuotaCard struct {
	Provider string `json:"provider"`
	Plan     string `json:"plan"`
	User     string `json:"user"`
	Alias    string `json:"alias"`
	// Kind 是卡片种类：subscription、plan 或 balance。
	Kind string `json:"kind"`
	// Last 表示该账号是最近一次经网关服务的账号。
	Last bool `json:"last"`
	// LastServedAt 是该账号最近一次经网关服务的时间，缺失时为空串。
	LastServedAt string `json:"lastServedAt"`
	// From 是远程 Magpie 的名字，本机卡片为空。
	From    string              `json:"from"`
	ReadAt  string              `json:"readAt"`
	Error   string              `json:"error"`
	Windows []magpieQuotaWindow `json:"windows"`
}

type magpieQuotaWindow struct {
	Name string `json:"name"`
	// Used 用指针区分「缺失」与「0%」：缺失的窗口不能当成 0%。
	Used      *float64 `json:"used"`
	ResetsAt  string   `json:"resetsAt"`
	Unlimited bool     `json:"unlimited"`
}

// readOpenAIQuota 直连 OpenAI：确认是 ChatGPT 登录后读 account/rateLimits/read。
func readOpenAIQuota(ctx context.Context, client *appClient) (RateLimits, error) {
	accountRaw, err := client.call(ctx, "account/read", map[string]any{})
	if err != nil {
		return RateLimits{}, fmt.Errorf("codex: account/read: %w", err)
	}
	planType, ok := chatGPTAccountPlan(accountRaw)
	if !ok {
		return RateLimits{}, fmt.Errorf("codex: 直连 OpenAI 但没有可读取额度的 ChatGPT 登录")
	}

	resRaw, err := client.call(ctx, "account/rateLimits/read", nil)
	if err != nil {
		return RateLimits{}, fmt.Errorf("codex: account/rateLimits/read: %w", err)
	}
	limits, err := ParseRateLimitsResponse(resRaw)
	if err != nil {
		return RateLimits{}, err
	}
	limits.Source = QuotaSourceOpenAI
	if limits.PlanType == "" {
		limits.PlanType = planType
	}
	// 直连的额度是本次实时读取，读取时间就是现在。
	limits.ReadAt = time.Now().UTC()
	return limits, nil
}

// chatGPTAccountPlan 判断 account/read 的账号是不是 ChatGPT 登录，并取出套餐标识。
func chatGPTAccountPlan(raw json.RawMessage) (string, bool) {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return "", false
	}
	account, ok := root["account"].(map[string]any)
	if !ok {
		return "", false
	}
	if !strings.EqualFold(strings.TrimSpace(getStringField(account, "type")), "chatgpt") {
		return "", false
	}
	return FormatCodexPlanName(getStringField(account, "planType", "plan_type")), true
}

// readMagpieQuota 读取 Magpie 网关缓存的账号卡片：普通刷新只重读缓存，不触发上游刷新。
func readMagpieQuota(ctx context.Context, route quotaRoute) (RateLimits, error) {
	body, status, err := quotaGet(ctx, route.BaseURL+magpieCardsPath, route.Token)
	if err != nil {
		return RateLimits{}, fmt.Errorf("codex: 读取 Magpie 额度失败: %w", err)
	}
	switch {
	case status == http.StatusForbidden:
		return RateLimits{}, fmt.Errorf("codex: 该 Magpie key 没有读取额度的权限（Quotas）")
	case status < 200 || status >= 300:
		return RateLimits{}, fmt.Errorf("codex: Magpie 额度返回 %d: %s", status, quotaSnippet(body))
	}
	var payload magpieCardsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return RateLimits{}, fmt.Errorf("codex: 解析 Magpie 额度失败: %w", err)
	}
	accounts := parseMagpieCodexCards(payload.Data)
	if len(accounts) == 0 {
		return RateLimits{}, fmt.Errorf("codex: Magpie 网关没有返回 Codex 额度卡片")
	}
	return RateLimits{
		Source:         QuotaSourceMagpie,
		Accounts:       accounts,
		GatewayBaseURL: route.BaseURL,
		GatewayToken:   route.Token,
	}, nil
}

// parseMagpieCodexCards 只保留 Codex 卡片，按别名、账号标识、序号的顺序区分账号：
// 别名与账号都缺失或重复时补序号，避免两张卡片被合并成同一个账号；
// 远程 Magpie 的卡片带上「来源 · 」前缀，避免与本机卡片重名。
func parseMagpieCodexCards(cards []magpieQuotaCard) []AccountQuota {
	var out []AccountQuota
	taken := make(map[string]bool, len(cards))
	for i, card := range cards {
		if !strings.EqualFold(strings.TrimSpace(card.Provider), "codex") {
			continue
		}
		if len(card.Windows) == 0 {
			continue
		}
		id := firstNonEmptyTrim(card.Alias, card.User)
		if id == "" {
			id = fmt.Sprintf("账号 %d", i+1)
		}
		if from := strings.TrimSpace(card.From); from != "" {
			id = from + " · " + id
		}
		account := id
		for n := 2; taken[account]; n++ {
			account = fmt.Sprintf("%s #%d", id, n)
		}
		taken[account] = true

		acct := AccountQuota{
			Account:      account,
			PlanType:     FormatCodexPlanName(card.Plan),
			Provider:     strings.TrimSpace(card.Provider),
			User:         strings.TrimSpace(card.User),
			Kind:         strings.TrimSpace(card.Kind),
			Last:         card.Last,
			LastServedAt: parseCardReadAt(card.LastServedAt),
			ReadAt:       parseCardReadAt(card.ReadAt),
		}
		for _, w := range card.Windows {
			name := strings.TrimSpace(w.Name)
			// 缺失用量或窗口名无法展示的窗口跳过，不能当成 0%。
			if name == "" || w.Used == nil {
				continue
			}
			acct.Windows = append(acct.Windows, RateLimitWindow{
				ID:                 account + "/" + name,
				Title:              name,
				Period:             magpieWindowPeriod(name),
				UsedPercent:        clampPercent(*w.Used),
				WindowDurationMins: magpieWindowMins(name),
				ResetsAt:           parseResetTime(strings.TrimSpace(w.ResetsAt)),
			})
		}
		if len(acct.Windows) == 0 {
			continue
		}
		out = append(out, acct)
	}
	return out
}

// magpieWindowMins 由 Magpie 的窗口名（5 hours、7 days、30 days…）推出窗口时长。
func magpieWindowMins(name string) int {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.Contains(lower, "5 hour"):
		return 300
	case strings.Contains(lower, "7 day") || strings.Contains(lower, "week"):
		return 10080
	case strings.Contains(lower, "30 day") || strings.Contains(lower, "month"):
		return 43200
	case strings.Contains(lower, "1 day") || lower == "day":
		return 1440
	}
	return 0
}

// magpieWindowPeriod 由 Magpie 的窗口名推出周期标记，认不出时返回空串。
func magpieWindowPeriod(name string) string {
	return formatWindowPeriod(magpieWindowMins(name))
}

// parseCardReadAt 解析卡片的原始读取时间；缺失或无法解析时返回零值，不能伪装成当前时间。
func parseCardReadAt(raw string) time.Time {
	if t := parseResetTime(strings.TrimSpace(raw)); t != nil {
		return t.UTC()
	}
	return time.Time{}
}

// clampPercent 把百分比夹在 0..100。
func clampPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// firstNonEmptyTrim 返回第一个去空白后非空的值。
func firstNonEmptyTrim(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// quotaSnippet 截断响应体，避免把整页 HTML 写进错误信息。
func quotaSnippet(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	return text
}
