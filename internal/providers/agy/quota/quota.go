package quota

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	// ErrUnauthorized 表示 401 认证失败（Token 失效）。
	ErrUnauthorized = errors.New("agy: unauthorized")
	// ErrForbidden 表示 403 访问被拒绝或账号受限。
	ErrForbidden = errors.New("agy: forbidden")
)

// ForbiddenError 附带上游返回的具体封禁/受限原因文本。
type ForbiddenError struct {
	Reason string
}

func (e *ForbiddenError) Error() string {
	if e.Reason != "" {
		return "agy: forbidden: " + e.Reason
	}
	return "agy: forbidden"
}

// Is 支持 errors.Is(err, ErrForbidden) 成立。
func (e *ForbiddenError) Is(target error) bool {
	return target == ErrForbidden
}

// Bucket 描述单一配额窗口或具体模型的额度快照（accounts 无关）。
type Bucket struct {
	UsedPercent float64
	ResetAt     time.Time
}

// Result 包含额度拉取的最终综合结果。
type Result struct {
	ProjectID  string
	Tier       string
	Groups     map[string]Bucket
	Candidates []ModelCandidate
}

// DefaultHosts 是官方接口端点主机顺序（主用 + 备用测试主机）。
var DefaultHosts = []string{
	"https://cloudcode-pa.googleapis.com",
	"https://daily-cloudcode-pa.googleapis.com",
	"https://daily-cloudcode-pa.sandbox.googleapis.com",
}

// 可在单元测试中调小的重试间隔变量
var (
	backoffBaseInterval = 1 * time.Second
	tokenRetryInterval  = 500 * time.Millisecond
)

// Client 负责与 Google Code Assist 额度服务端点交互。
type Client struct {
	HTTPClient *http.Client
	Hosts      []string
}

type loadCodeAssistReq struct {
	Metadata map[string]string `json:"metadata"`
}

type loadCodeAssistResp struct {
	CloudaicompanionProject string           `json:"cloudaicompanionProject"`
	CloudaicompanionSnake   string           `json:"cloudaicompanion_project"`
	Project                 string           `json:"project"`
	ProjectID               string           `json:"projectId"`
	ProjectIDSnake          string           `json:"project_id"`
	CurrentTier             *tierInfo        `json:"currentTier"`
	CurrentTierSnake        *tierInfo        `json:"current_tier"`
	PaidTier                *tierInfo        `json:"paidTier"`
	PaidTierSnake           *tierInfo        `json:"paid_tier"`
	AllowedTiers            []tierInfo       `json:"allowedTiers"`
	AllowedTiersSnake       []tierInfo       `json:"allowed_tiers"`
	IneligibleTiers         []ineligibleTier `json:"ineligibleTiers"`
	IneligibleTiersSnake    []ineligibleTier `json:"ineligible_tiers"`
}

type tierInfo struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	IsDefault      *bool  `json:"isDefault"`
	IsDefaultSnake *bool  `json:"is_default"`
}

type ineligibleTier struct {
	ReasonCode      string `json:"reasonCode"`
	ReasonCodeSnake string `json:"reason_code"`
}

func (r *loadCodeAssistResp) resolveProjectID() string {
	if r.Project != "" {
		return r.Project
	}
	if r.ProjectID != "" {
		return r.ProjectID
	}
	if r.ProjectIDSnake != "" {
		return r.ProjectIDSnake
	}
	if r.CloudaicompanionProject != "" {
		return r.CloudaicompanionProject
	}
	return r.CloudaicompanionSnake
}

func (t *tierInfo) isDef() bool {
	if t == nil {
		return false
	}
	if t.IsDefault != nil {
		return *t.IsDefault
	}
	if t.IsDefaultSnake != nil {
		return *t.IsDefaultSnake
	}
	return false
}

func (t *tierInfo) label() string {
	if t == nil {
		return ""
	}
	if t.Name != "" {
		return t.Name
	}
	return t.ID
}

func (r *loadCodeAssistResp) resolveTier() string {
	paid := r.PaidTier
	if paid == nil {
		paid = r.PaidTierSnake
	}
	if paid != nil && paid.label() != "" {
		return paid.label()
	}

	ineligible := r.IneligibleTiers
	if len(ineligible) == 0 {
		ineligible = r.IneligibleTiersSnake
	}
	isIneligible := len(ineligible) > 0

	curr := r.CurrentTier
	if curr == nil {
		curr = r.CurrentTierSnake
	}
	if !isIneligible && curr != nil && curr.label() != "" {
		return curr.label()
	}

	allowed := r.AllowedTiers
	if len(allowed) == 0 {
		allowed = r.AllowedTiersSnake
	}
	if isIneligible {
		for _, t := range allowed {
			if t.isDef() && t.label() != "" {
				return t.label() + " (Restricted)"
			}
		}
	}
	if curr != nil && curr.label() != "" {
		return curr.label()
	}
	return ""
}

type fetchModelsReq struct {
	Project string `json:"project,omitempty"`
}

type fetchModelsResp struct {
	Models map[string]rawModelInfo `json:"models"`
}

type rawModelInfo struct {
	DisplayName string        `json:"displayName"`
	QuotaInfo   *rawQuotaInfo `json:"quotaInfo"`
	QuotaSnake  *rawQuotaInfo `json:"quota_info"`
}

type rawQuotaInfo struct {
	RemainingFraction *float64 `json:"remainingFraction"`
	RemainingSnake    *float64 `json:"remaining_fraction"`
	ResetTime         string   `json:"resetTime"`
	ResetTimeSnake    string   `json:"reset_time"`
}

func (q *rawQuotaInfo) fraction() float64 {
	if q == nil {
		return 0
	}
	if q.RemainingFraction != nil {
		return *q.RemainingFraction
	}
	if q.RemainingSnake != nil {
		return *q.RemainingSnake
	}
	return 0
}

func (q *rawQuotaInfo) reset() string {
	if q == nil {
		return ""
	}
	if q.ResetTime != "" {
		return q.ResetTime
	}
	return q.ResetTimeSnake
}

type summaryResp struct {
	Groups []rawSummaryGroup `json:"groups"`
}

type rawSummaryGroup struct {
	DisplayName string             `json:"displayName"`
	Buckets     []rawSummaryBucket `json:"buckets"`
}

type rawSummaryBucket struct {
	BucketID          string   `json:"bucketId"`
	BucketIDSnake     string   `json:"bucket_id"`
	Window            string   `json:"window"`
	RemainingFraction *float64 `json:"remainingFraction"`
	RemainingSnake    *float64 `json:"remaining_fraction"`
	ResetTime         string   `json:"resetTime"`
	ResetTimeSnake    string   `json:"reset_time"`
}

func extractForbiddenReason(body string) string {
	if strings.Contains(body, "USER_INELIGIBLE") {
		return "用户不符合使用条件 (USER_INELIGIBLE)"
	}
	if strings.Contains(body, "TERMS_NOT_ACCEPTED") {
		return "未同意服务条款 (TERMS_NOT_ACCEPTED)"
	}
	if strings.Contains(body, "PERMISSION_DENIED") {
		return "权限被拒绝 (PERMISSION_DENIED)"
	}
	if strings.Contains(body, "RESOURCE_EXHAUSTED") {
		return "资源已耗尽 (RESOURCE_EXHAUSTED)"
	}
	return "账号已被封禁或受限 (403)"
}

func calcUsedPercent(fraction float64) float64 {
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	return (1.0 - fraction) * 100.0
}

func parseResetTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	return time.Time{}
}

func (c *Client) postWithHostFallback(ctx context.Context, path string, token string, body []byte) (*http.Response, []byte, error) {
	hosts := c.Hosts
	if len(hosts) == 0 {
		hosts = DefaultHosts
	}

	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	var lastErr error

	for _, host := range hosts {
		u := strings.TrimRight(host, "/") + path
		maxRetries := 3
		var resp *http.Response
		var respBody []byte
		hostFailed := false

		for attempt := 0; attempt <= maxRetries; attempt++ {
			if attempt > 0 {
				backoff := backoffBaseInterval * time.Duration(1<<(attempt-1))
				select {
				case <-ctx.Done():
					return nil, nil, ctx.Err()
				case <-time.After(backoff):
				}
			}

			req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
			if err != nil {
				return nil, nil, err
			}
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", "vscode/1.X.X (Antigravity/4.3.0)")
			req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")

			resp, err = client.Do(req)
			if err != nil {
				lastErr = err
				hostFailed = true
				break // 网络错误切下一主机
			}

			respBody, err = io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				lastErr = err
				hostFailed = true
				break
			}

			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
				lastErr = fmt.Errorf("server error %d: %s", resp.StatusCode, string(respBody))
				if attempt < maxRetries {
					continue
				}
				hostFailed = true
				break
			}

			return resp, respBody, nil
		}

		if !hostFailed {
			return resp, respBody, nil
		}
	}

	if lastErr != nil {
		return nil, nil, lastErr
	}
	return nil, nil, errors.New("agy: all hosts failed")
}

func (c *Client) postJSON(ctx context.Context, path string, token string, reqBody any) (*http.Response, []byte, error) {
	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, nil, err
	}
	return c.postWithHostFallback(ctx, path, token, data)
}

// Fetch 依次调用 loadCodeAssist、fetchAvailableModels 与 retrieveUserQuotaSummary 拉取账号配额。
func (c *Client) Fetch(ctx context.Context, accessToken, projectID string) (Result, error) {
	// 1. loadCodeAssist
	loadReq := loadCodeAssistReq{Metadata: map[string]string{"ideType": "ANTIGRAVITY"}}
	resp, body, err := c.postJSON(ctx, "/v1internal:loadCodeAssist", accessToken, loadReq)
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return Result{}, ErrUnauthorized
	}
	if resp.StatusCode == http.StatusForbidden {
		return Result{}, forbiddenResponse(body)
	}
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("loadCodeAssist status %d: %s", resp.StatusCode, string(body))
	}

	var loadResp loadCodeAssistResp
	if err := json.Unmarshal(body, &loadResp); err != nil {
		return Result{}, fmt.Errorf("unmarshal loadCodeAssist: %w", err)
	}
	resolvedProject := loadResp.resolveProjectID()
	if resolvedProject != "" {
		projectID = resolvedProject
	}
	tier := loadResp.resolveTier()

	// 2. fetchAvailableModels
	modelsReq := fetchModelsReq{}
	if projectID != "" {
		modelsReq.Project = projectID
	}
	resp, body, err = c.postJSON(ctx, "/v1internal:fetchAvailableModels", accessToken, modelsReq)
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return Result{}, ErrUnauthorized
	}
	if resp.StatusCode == http.StatusForbidden {
		if projectID != "" {
			// 403 剥离 project 用空体重试一次
			retryReq := fetchModelsReq{}
			resp, body, err = c.postJSON(ctx, "/v1internal:fetchAvailableModels", accessToken, retryReq)
			if err != nil {
				return Result{}, err
			}
			if resp.StatusCode == http.StatusUnauthorized {
				return Result{}, ErrUnauthorized
			}
			if resp.StatusCode == http.StatusForbidden {
				return Result{}, forbiddenResponse(body)
			}
		} else {
			return Result{}, forbiddenResponse(body)
		}
	}
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("fetchAvailableModels status %d: %s", resp.StatusCode, string(body))
	}

	var modelsResp fetchModelsResp
	if err := json.Unmarshal(body, &modelsResp); err != nil {
		return Result{}, fmt.Errorf("unmarshal fetchAvailableModels: %w", err)
	}

	type rawModelParsed struct {
		usedPercent float64
		resetAt     time.Time
	}
	modelsParsed := make(map[string]rawModelParsed)
	for name, info := range modelsResp.Models {
		lower := strings.ToLower(name)
		if !strings.HasPrefix(lower, "gemini") &&
			!strings.HasPrefix(lower, "claude") &&
			!strings.HasPrefix(lower, "gpt") &&
			!strings.HasPrefix(lower, "image") {
			continue
		}
		qInfo := info.QuotaInfo
		if qInfo == nil {
			qInfo = info.QuotaSnake
		}
		if qInfo == nil || (qInfo.RemainingFraction == nil && qInfo.RemainingSnake == nil) {
			continue
		}
		frac := qInfo.fraction()
		resetStr := qInfo.reset()
		modelsParsed[name] = rawModelParsed{
			usedPercent: calcUsedPercent(frac),
			resetAt:     parseResetTime(resetStr),
		}
	}

	// 3. retrieveUserQuotaSummary
	summaryReq := fetchModelsReq{}
	if projectID != "" {
		summaryReq.Project = projectID
	}
	resp, body, err = c.postJSON(ctx, "/v1internal:retrieveUserQuotaSummary", accessToken, summaryReq)
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return Result{}, ErrUnauthorized
	}
	if resp.StatusCode == http.StatusForbidden {
		if projectID != "" {
			retryReq := fetchModelsReq{}
			resp, body, err = c.postJSON(ctx, "/v1internal:retrieveUserQuotaSummary", accessToken, retryReq)
			if err != nil {
				return Result{}, err
			}
			if resp.StatusCode == http.StatusUnauthorized {
				return Result{}, ErrUnauthorized
			}
			if resp.StatusCode == http.StatusForbidden {
				return Result{}, forbiddenResponse(body)
			}
		} else {
			return Result{}, forbiddenResponse(body)
		}
	}
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("retrieveUserQuotaSummary status %d: %s", resp.StatusCode, string(body))
	}

	var sumResp summaryResp
	if err := json.Unmarshal(body, &sumResp); err != nil {
		return Result{}, fmt.Errorf("unmarshal retrieveUserQuotaSummary: %w", err)
	}

	// 4. 配额融合与 Groups 构建
	groups := make(map[string]Bucket)

	for _, g := range sumResp.Groups {
		groupName := g.DisplayName
		if groupName == "" {
			continue
		}
		for _, b := range g.Buckets {
			if b.RemainingFraction == nil && b.RemainingSnake == nil {
				continue
			}
			bid := strings.ToLower(b.BucketID)
			if bid == "" {
				bid = strings.ToLower(b.BucketIDSnake)
			}
			win := strings.ToLower(b.Window)
			frac := 0.0
			if b.RemainingFraction != nil {
				frac = *b.RemainingFraction
			} else if b.RemainingSnake != nil {
				frac = *b.RemainingSnake
			}
			resetStr := b.ResetTime
			if resetStr == "" {
				resetStr = b.ResetTimeSnake
			}
			resetAt := parseResetTime(resetStr)
			used := calcUsedPercent(frac)

			if strings.Contains(win, "7d") || strings.Contains(bid, "7d") || strings.Contains(win, "week") || strings.Contains(bid, "week") {
				groups[groupName+" 7d"] = Bucket{
					UsedPercent: used,
					ResetAt:     resetAt,
				}
			} else if strings.Contains(win, "5h") || strings.Contains(bid, "5h") || strings.Contains(win, "hour") {
				groups[groupName] = Bucket{
					UsedPercent: used,
					ResetAt:     resetAt,
				}
			}
		}
	}

	// 若 summary 缺失某分组 5h 数据，用该组内模型的最大 UsedPercent 作为分组值
	type groupModelStat struct {
		maxUsed float64
		resetAt time.Time
		found   bool
	}
	groupStats := make(map[string]*groupModelStat)
	for modelName, mp := range modelsParsed {
		gName := ModelGroup(modelName)
		stat, ok := groupStats[gName]
		if !ok {
			stat = &groupModelStat{}
			groupStats[gName] = stat
		}
		if !stat.found || mp.usedPercent > stat.maxUsed {
			stat.maxUsed = mp.usedPercent
			stat.resetAt = mp.resetAt
			stat.found = true
		}
	}

	for gName, stat := range groupStats {
		if _, has5h := groups[gName]; !has5h && stat.found {
			groups[gName] = Bucket{
				UsedPercent: stat.maxUsed,
				ResetAt:     stat.resetAt,
			}
		}
	}

	// 具体模型名 = 模型额度，模型额度与其所属分组（用 ModelGroup 归组）的 5h 额度融合，UsedPercent 取两者较大值。
	for modelName, mp := range modelsParsed {
		gName := ModelGroup(modelName)
		finalUsed := mp.usedPercent
		finalReset := mp.resetAt

		if g5h, ok := groups[gName]; ok {
			if g5h.UsedPercent > finalUsed {
				finalUsed = g5h.UsedPercent
			}
			if finalReset.IsZero() && !g5h.ResetAt.IsZero() {
				finalReset = g5h.ResetAt
			}
		}

		groups[modelName] = Bucket{
			UsedPercent: finalUsed,
			ResetAt:     finalReset,
		}
	}

	geminiCandidates := FilterGeminiTextCandidates(modelsResp.Models)

	return Result{
		ProjectID:  projectID,
		Tier:       tier,
		Groups:     groups,
		Candidates: geminiCandidates,
	}, nil
}

// FetchGeminiCandidates 向 /v1internal:fetchAvailableModels 请求新鲜模型列表并返回合格的 Gemini 文本候选。
func (c *Client) FetchGeminiCandidates(ctx context.Context, accessToken, projectID string) ([]ModelCandidate, error) {
	modelsReq := fetchModelsReq{}
	if projectID != "" {
		modelsReq.Project = projectID
	}
	resp, body, err := c.postJSON(ctx, "/v1internal:fetchAvailableModels", accessToken, modelsReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrUnauthorized
	}
	if resp.StatusCode == http.StatusForbidden {
		if projectID != "" {
			retryReq := fetchModelsReq{}
			resp, body, err = c.postJSON(ctx, "/v1internal:fetchAvailableModels", accessToken, retryReq)
			if err != nil {
				return nil, err
			}
			if resp.StatusCode == http.StatusUnauthorized {
				return nil, ErrUnauthorized
			}
			if resp.StatusCode == http.StatusForbidden {
				return nil, forbiddenResponse(body)
			}
		} else {
			return nil, forbiddenResponse(body)
		}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetchAvailableModels status %d: %s", resp.StatusCode, string(body))
	}

	var modelsResp fetchModelsResp
	if err := json.Unmarshal(body, &modelsResp); err != nil {
		return nil, fmt.Errorf("unmarshal fetchAvailableModels: %w", err)
	}

	return FilterGeminiTextCandidates(modelsResp.Models), nil
}

// 只有明确的账号限制才持久化为 forbidden，普通权限错误不应永久停用账号。
func forbiddenResponse(body []byte) error {
	text := string(body)
	if strings.Contains(text, "USER_INELIGIBLE") || strings.Contains(text, "TERMS_NOT_ACCEPTED") {
		return &ForbiddenError{Reason: extractForbiddenReason(text)}
	}
	return errors.New("agy: quota request rejected (403)")
}

// ActivateWeekly 消耗至多 1 个输出 token 激活新周期；动态获取可用候选后执行预热，不重试不确定的网络失败。
func (c *Client) ActivateWeekly(ctx context.Context, accessToken, projectID string) error {
	res, err := c.Fetch(ctx, accessToken, projectID)
	if err != nil {
		return err
	}
	_, err = c.Warmup(ctx, accessToken, projectID, res.Candidates, "")
	return err
}

// RefreshAccessToken 调用官方 OAuth 端点刷新 access token，遇到网络错误或 invalid_grant 进行 500ms 短退避重试。
func RefreshAccessToken(ctx context.Context, hc *http.Client, tokenURL, clientID, clientSecret, refreshToken string) (accessToken string, expiry time.Time, err error) {
	if refreshToken == "" {
		return "", time.Time{}, errors.New("agy: empty refresh token")
	}
	if hc == nil {
		hc = http.DefaultClient
	}

	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("refresh_token", refreshToken)
	form.Set("grant_type", "refresh_token")

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", time.Time{}, ctx.Err()
			case <-time.After(tokenRetryInterval):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
		if err != nil {
			return "", time.Time{}, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("User-Agent", "vscode/1.X.X (Antigravity/4.3.0)")

		resp, err := hc.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode != http.StatusOK {
			bodyStr := string(body)
			lastErr = fmt.Errorf("token refresh failed (%d): %s", resp.StatusCode, bodyStr)
			if strings.Contains(bodyStr, "invalid_grant") && attempt == 0 {
				continue
			}
			continue
		}

		var tr struct {
			AccessToken string `json:"access_token"`
			ExpiresIn   int64  `json:"expires_in"`
		}
		if err := json.Unmarshal(body, &tr); err != nil {
			return "", time.Time{}, fmt.Errorf("decode token refresh response: %w", err)
		}
		if tr.AccessToken == "" {
			return "", time.Time{}, errors.New("agy: empty access token returned")
		}

		exp := time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
		return tr.AccessToken, exp, nil
	}

	return "", time.Time{}, lastErr
}
