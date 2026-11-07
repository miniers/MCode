package quota

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"mcode/internal/modelsdev"
)

var (
	// ErrModelRejected 表示模型明确被拒绝、下架或不支持生成。
	ErrModelRejected = errors.New("agy: model explicitly rejected")
	// ErrResultUncertain 表示网络超时或服务端异常，请求在服务端是否已执行未决。
	ErrResultUncertain = errors.New("agy: warmup result uncertain")
	// ErrExplicitFailure 表示请求明确失败且未执行（如 400/401/403/429 等）。
	ErrExplicitFailure = errors.New("agy: warmup explicitly failed")
)

// WarmupError 附带状态码与分类信息。
type WarmupError struct {
	Sentinel   error
	StatusCode int
	Message    string
	Underlying error
}

func (e *WarmupError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Underlying != nil {
		return e.Underlying.Error()
	}
	if e.Sentinel != nil {
		return e.Sentinel.Error()
	}
	return "agy: warmup error"
}

func (e *WarmupError) Unwrap() error {
	return e.Underlying
}

func (e *WarmupError) Is(target error) bool {
	return target == e.Sentinel || errors.Is(e.Underlying, target)
}

// ModelCandidate 描述经过正向过滤后的可用 Gemini 文本候选模型。
type ModelCandidate struct {
	ID          string    `json:"id"`
	DisplayName string    `json:"displayName,omitempty"`
	UsedPercent float64   `json:"usedPercent,omitempty"`
	ResetAt     time.Time `json:"resetAt,omitempty"`
	HasQuota    bool      `json:"hasQuota,omitempty"`
}

// IsEligibleGeminiTextModel 严格验证模型 ID 与展示名称是否为合格的 Gemini 文本模型。
// 排除 Claude、GPT、其他第三方模型，以及图像生成、音视频、embedding 等非文本模型。
func IsEligibleGeminiTextModel(id, displayName string) bool {
	lowerID := strings.ToLower(id)
	lowerName := strings.ToLower(displayName)

	// 1. 正向识别：必须包含 gemini 标识
	if !strings.Contains(lowerID, "gemini") && !strings.Contains(lowerName, "gemini") {
		return false
	}

	// 2. 排除第三方提供方模型
	if strings.Contains(lowerID, "claude") || strings.Contains(lowerName, "claude") ||
		strings.Contains(lowerID, "gpt") || strings.Contains(lowerName, "gpt") ||
		strings.Contains(lowerID, "anthropic") || strings.Contains(lowerName, "anthropic") ||
		strings.Contains(lowerID, "openai") || strings.Contains(lowerName, "openai") ||
		strings.Contains(lowerID, "llama") || strings.Contains(lowerName, "llama") ||
		strings.Contains(lowerID, "mistral") || strings.Contains(lowerName, "mistral") ||
		strings.Contains(lowerID, "deepseek") || strings.Contains(lowerName, "deepseek") ||
		strings.Contains(lowerID, "qwen") || strings.Contains(lowerName, "qwen") {
		return false
	}

	// 3. 排除非文本模型（图像生成、音视频、embedding 等）
	if strings.Contains(lowerID, "imagen") || strings.Contains(lowerName, "imagen") ||
		strings.Contains(lowerID, "image") || strings.Contains(lowerName, "image") ||
		strings.Contains(lowerID, "audio") || strings.Contains(lowerName, "audio") ||
		strings.Contains(lowerID, "video") || strings.Contains(lowerName, "video") ||
		strings.Contains(lowerID, "speech") || strings.Contains(lowerName, "speech") ||
		strings.Contains(lowerID, "tts") || strings.Contains(lowerName, "tts") ||
		strings.Contains(lowerID, "whisper") || strings.Contains(lowerName, "whisper") ||
		strings.Contains(lowerID, "live") || strings.Contains(lowerName, "live") ||
		strings.Contains(lowerID, "embedding") || strings.Contains(lowerName, "embedding") ||
		strings.Contains(lowerID, "embed") || strings.Contains(lowerName, "embed") {
		return false
	}

	// 4. 结合 models.dev 现有输入能力信息（若有且明确不支持文本输入，则排除）
	if devInfo, found := modelsdev.Default.Lookup("google", id); found {
		if len(devInfo.InputModalities) > 0 {
			hasText := false
			for _, m := range devInfo.InputModalities {
				if strings.EqualFold(m, "text") {
					hasText = true
					break
				}
			}
			if !hasText {
				return false
			}
		}
	}

	return true
}

// FilterGeminiTextCandidates 正向识别并提取 Gemini 文本模型候选。
func FilterGeminiTextCandidates(models map[string]rawModelInfo) []ModelCandidate {
	var candidates []ModelCandidate
	if len(models) == 0 {
		return candidates
	}

	for id, info := range models {
		if !IsEligibleGeminiTextModel(id, info.DisplayName) {
			continue
		}

		var usedPercent float64
		var resetAt time.Time
		hasQuota := false
		qInfo := info.QuotaInfo
		if qInfo == nil {
			qInfo = info.QuotaSnake
		}
		if qInfo != nil && (qInfo.RemainingFraction != nil || qInfo.RemainingSnake != nil) {
			hasQuota = true
			usedPercent = calcUsedPercent(qInfo.fraction())
			if rStr := qInfo.reset(); rStr != "" {
				if t, err := time.Parse(time.RFC3339, rStr); err == nil {
					resetAt = t
				}
			}
		}

		candidates = append(candidates, ModelCandidate{
			ID:          id,
			DisplayName: info.DisplayName,
			UsedPercent: usedPercent,
			ResetAt:     resetAt,
			HasQuota:    hasQuota,
		})
	}

	return candidates
}

// candidateMeta 排序辅助元数据
type candidateMeta struct {
	cand          ModelCandidate
	hasCost       bool
	estimatedCost float64
	isLastSuccess bool
	isLight       bool
}

// RankGeminiCandidates 结合 modelsdev 参考价格与启发式对候选模型排序。
// 固定短提示(2 tokens)与1个输出token计算预估成本，已知成本低者优先；
// 缺失 quota 不当已确认可用；未知价格不当零；相同成本或缺价时，上次成功模型优先，随后 flash/lite 启发式优先，最后按字母序稳定排序。
func RankGeminiCandidates(candidates []ModelCandidate, lastSuccessModel string) []ModelCandidate {
	if len(candidates) <= 1 {
		return candidates
	}

	metas := make([]candidateMeta, len(candidates))
	for i, c := range candidates {
		meta := candidateMeta{
			cand:          c,
			isLastSuccess: (lastSuccessModel != "" && c.ID == lastSuccessModel),
		}

		lowerID := strings.ToLower(c.ID)
		lowerName := strings.ToLower(c.DisplayName)
		if strings.Contains(lowerID, "flash") || strings.Contains(lowerID, "lite") ||
			strings.Contains(lowerName, "flash") || strings.Contains(lowerName, "lite") {
			meta.isLight = true
		}

		if devInfo, ok := modelsdev.Default.Lookup("google", c.ID); ok && devInfo.Cost != nil {
			meta.hasCost = true
			// 短提示约 2 tokens 输入，1 token 输出。单位为美元/百万 token。
			meta.estimatedCost = (2.0*devInfo.Cost.Input + 1.0*devInfo.Cost.Output) / 1000000.0
		}

		metas[i] = meta
	}

	sort.SliceStable(metas, func(i, j int) bool {
		a, b := metas[i], metas[j]

		// 过滤与选择时不能把缺失 quota 当作已确认可用
		if a.cand.HasQuota != b.cand.HasQuota {
			return a.cand.HasQuota
		}

		// 1. 已知价格对比：两者都有价格且成本不同时，成本低者优先；未知价格不当零（有已知价格的排在缺价前面）
		if a.hasCost && b.hasCost {
			diff := a.estimatedCost - b.estimatedCost
			if diff > 1e-12 {
				return false
			}
			if diff < -1e-12 {
				return true
			}
		} else if a.hasCost != b.hasCost {
			return a.hasCost
		}

		// 2. 成本相同或均缺价：上次成功候选优先
		if a.isLastSuccess != b.isLastSuccess {
			return a.isLastSuccess
		}

		// 3. flash / lite 轻量名称启发式优先
		if a.isLight != b.isLight {
			return a.isLight
		}

		// 4. 稳定排序：按 ID 字母序
		return strings.ToLower(a.cand.ID) < strings.ToLower(b.cand.ID)
	})

	out := make([]ModelCandidate, len(metas))
	for i, m := range metas {
		out[i] = m.cand
	}
	return out
}

// IsModelNotFoundError 严格检查响应是否包含模型下架、不存在或不支持文本生成的明确证据。
// 必须严格限制为明确模型相关的 400 或 404；503/429/401/403 等即便 body 含 model 也不得视为模型不存在；
// 泛化 NOT_FOUND 或不支持某个 generationConfig 参数不能当模型下架。
func IsModelNotFoundError(statusCode int, body string) bool {
	if statusCode != http.StatusBadRequest && statusCode != http.StatusNotFound {
		return false
	}

	lowerBody := strings.ToLower(body)

	// 不支持的 generationConfig 参数不能当模型下架
	if strings.Contains(lowerBody, "generationconfig") || strings.Contains(lowerBody, "generation_config") {
		return false
	}

	// 必须具备模型相关上下文
	hasModelContext := strings.Contains(lowerBody, "model") ||
		strings.Contains(lowerBody, "models/")

	if !hasModelContext {
		return false
	}

	evidencePhrases := []string{
		"not found",
		"does not exist",
		"unknown model",
		"unsupported model",
		"is not supported",
		"not supported for generatecontent",
		"does not support",
		"invalid model",
		"not available",
		"unrecognized model",
	}

	for _, phrase := range evidencePhrases {
		if strings.Contains(lowerBody, phrase) {
			return true
		}
	}

	return false
}

// sanitizeErrorBody 用简明状态与错误类别替代上游原文凭证字段，防止泄露。
func sanitizeErrorBody(statusCode int, isModelErr bool) string {
	switch {
	case statusCode == http.StatusBadRequest:
		if isModelErr {
			return "模型参数不支持或已失效 (400)"
		}
		return "请求参数无效 (400)"
	case statusCode == http.StatusUnauthorized:
		return "认证失效 (401)"
	case statusCode == http.StatusForbidden:
		return "无权访问或受限 (403)"
	case statusCode == http.StatusNotFound:
		if isModelErr {
			return "模型不存在或已下架 (404)"
		}
		return "服务端端点不存在 (404)"
	case statusCode == http.StatusTooManyRequests:
		return "请求频控限制 (429)"
	case statusCode >= 500:
		return fmt.Sprintf("服务端异常 (%d)", statusCode)
	default:
		return fmt.Sprintf("请求失败 (%d)", statusCode)
	}
}

// Warmup 使用动态选择的 Gemini 文本模型执行微量预热生成（至多 1 个输出 token）。
// 必须验证候选为合格 Gemini 文本模型，直接传入第三方候选不产生任何请求；
// 过滤时明确余量耗尽且 reset 未到的候选不应选；
// 仅在明确模型失效时刷新一次当前账号模型列表，排除已尝试 ID，重新排名，最多再请求一个不同 Gemini 模型；
// 网络、401/403 账号问题、429、5xx 绝不触发候选刷新或换模型；保留同模型 403 剥离 project 的一次兼容请求。
func (c *Client) Warmup(ctx context.Context, accessToken, projectID string, candidates []ModelCandidate, lastSuccessModel string) (string, error) {
	if accessToken == "" {
		return "", errors.New("agy: empty access token for warmup")
	}

	now := time.Now()
	// 验证并过滤合格候选
	var eligible []ModelCandidate
	for _, cand := range candidates {
		if !IsEligibleGeminiTextModel(cand.ID, cand.DisplayName) {
			continue
		}
		// 明确余量耗尽且 reset 未到的候选不选；reset 到期可按现有额度语义判断
		if cand.HasQuota && cand.UsedPercent >= 100 && !cand.ResetAt.IsZero() && cand.ResetAt.After(now) {
			continue
		}
		eligible = append(eligible, cand)
	}

	if len(eligible) == 0 {
		return "", errors.New("agy: no available Gemini models for warmup")
	}

	// 动态排序候选模型
	ranked := RankGeminiCandidates(eligible, lastSuccessModel)
	if len(ranked) == 0 {
		return "", errors.New("agy: no available Gemini models for warmup")
	}

	hosts := c.Hosts
	if len(hosts) == 0 {
		hosts = DefaultHosts
	}
	hc := c.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}

	// 尝试向指定模型发送微型生成请求
	tryModel := func(modelID string) error {
		payload := map[string]any{
			"model": modelID,
			"request": map[string]any{
				"contents": []any{
					map[string]any{
						"role":  "user",
						"parts": []any{map[string]string{"text": "Say hi"}},
					},
				},
				"generationConfig": map[string]any{
					"maxOutputTokens": 1,
					"temperature":     0,
				},
			},
		}
		if projectID != "" {
			payload["project"] = projectID
		}

		for attempt := 0; attempt < 2; attempt++ {
			bodyBytes, err := json.Marshal(payload)
			if err != nil {
				return &WarmupError{Sentinel: ErrExplicitFailure, Message: err.Error(), Underlying: err}
			}

			reqURL := strings.TrimRight(hosts[0], "/") + "/v1internal:generateContent"
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(bodyBytes))
			if err != nil {
				return &WarmupError{Sentinel: ErrExplicitFailure, Message: err.Error(), Underlying: err}
			}
			req.Header.Set("Authorization", "Bearer "+accessToken)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", "vscode/1.X.X (Antigravity/4.3.0)")

			resp, err := hc.Do(req)
			if err != nil {
				// 网络错误、超时等为结果未决
				return &WarmupError{Sentinel: ErrResultUncertain, Message: "网络连接失败: " + err.Error(), Underlying: err}
			}

			// 读取至多 4KB 响应体供错误排查，避免溢出
			bodyData, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				return nil
			}

			// 403 且带 project 时剥离 project 重试一次
			if resp.StatusCode == http.StatusForbidden && attempt == 0 && projectID != "" {
				delete(payload, "project")
				continue
			}

			bodyText := string(bodyData)
			isModelErr := IsModelNotFoundError(resp.StatusCode, bodyText)
			cleanMsg := sanitizeErrorBody(resp.StatusCode, isModelErr)

			if isModelErr {
				return &WarmupError{Sentinel: ErrModelRejected, StatusCode: resp.StatusCode, Message: cleanMsg}
			}
			if resp.StatusCode >= 500 {
				return &WarmupError{Sentinel: ErrResultUncertain, StatusCode: resp.StatusCode, Message: cleanMsg}
			}
			return &WarmupError{Sentinel: ErrExplicitFailure, StatusCode: resp.StatusCode, Message: cleanMsg}
		}

		return &WarmupError{Sentinel: ErrExplicitFailure, Message: "agy: warmup generation failed"}
	}

	// 1. 发起首选模型请求
	primary := ranked[0]
	err := tryModel(primary.ID)
	if err == nil {
		return primary.ID, nil
	}

	// 2. 仅在明确模型失效时刷新一次当前账号模型列表，排除已尝试 ID，重新排名，最多再请求一个不同 Gemini 模型
	if errors.Is(err, ErrModelRejected) {
		refreshed, refErr := c.FetchGeminiCandidates(ctx, accessToken, projectID)
		if refErr != nil {
			return "", fmt.Errorf("warmup primary %s delisted, refresh models failed: %w", primary.ID, refErr)
		}

		var newEligible []ModelCandidate
		for _, cand := range refreshed {
			if cand.ID == primary.ID {
				continue
			}
			if !IsEligibleGeminiTextModel(cand.ID, cand.DisplayName) {
				continue
			}
			if cand.HasQuota && cand.UsedPercent >= 100 && !cand.ResetAt.IsZero() && cand.ResetAt.After(now) {
				continue
			}
			newEligible = append(newEligible, cand)
		}

		if len(newEligible) == 0 {
			return "", fmt.Errorf("warmup primary %s delisted, no alternative candidate available", primary.ID)
		}

		newRanked := RankGeminiCandidates(newEligible, "")
		backup := newRanked[0]
		backupErr := tryModel(backup.ID)
		if backupErr == nil {
			return backup.ID, nil
		}
		return "", fmt.Errorf("warmup primary %s delisted, backup %s failed: %w", primary.ID, backup.ID, backupErr)
	}

	return "", err
}
