package openai

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/providers/readhttp"
	"mcode/internal/startuptrace"
)

var _ harness.Harness = (*Harness)(nil)

// Options 包含 OpenAI 兼容提供者的配置选项。
type Options struct {
	Kind           harness.Kind
	DisplayName    string
	BaseURL        string
	Model          string
	APIKey         string
	APIKeyAccount  string
	Secrets        config.SecretStore
	FallbackAPIKey string // keyring 无值时的回退，例如 OPENAI_API_KEY
	HTTPClient     *http.Client
	MaxToolRounds  int
	ExtraHeaders   map[string]string
	// BuiltinMCP 是本轮要注入的内置 MCP 端点（mcode），未配置时为空。
	BuiltinMCP harness.BuiltinMCP
	// ExternalMCP 按工作目录返回用户配置的外部 MCP 端点。
	ExternalMCP func(workDir, threadID string) []harness.MCPEndpoint
}

// Harness 实现了基于 OpenAI 兼容 Chat Completions SSE 协议的 harness.Harness 接口。
type Harness struct {
	opts     Options
	mu       *sync.Mutex
	sessions map[string][]chatMessage

	modelMu         sync.Mutex
	modelIDs        []string
	modelExpiry     time.Time
	modelGen        uint64
	modelRefreshing bool
	// modelHTTPClient 是读取模型目录的专属客户端：以 opts.HTTPClient 为配置
	// 克隆出独立连接池，连接中断时换池重试一次。聊天请求仍用 opts.HTTPClient。
	modelHTTPClient *http.Client

	// availMu 保护钥匙串可用性探测的缓存（见 Available）。
	availMu      sync.Mutex
	availResult  *availabilityResult
	availProbing bool

	// now 是 Run 的时钟，nil 时用 time.Now；测试通过它注入假时钟。
	now func() time.Time
}

// chatMessage 表示发送给 OpenAI API 的单条消息。
type chatMessage struct {
	Role       string             `json:"role"`
	Content    any                `json:"content,omitempty"`
	ToolCallID string             `json:"tool_call_id,omitempty"`
	ToolCalls  []chatOutgoingCall `json:"tool_calls,omitempty"`
}

type chatOutgoingCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatOutgoingFunc `json:"function"`
}

type chatOutgoingFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// New 创建一个新的 OpenAI Harness 适配器实例。
func New(opts Options) *Harness {
	if opts.Kind == "" {
		opts.Kind = harness.Kind("openai")
	}
	if opts.DisplayName == "" {
		opts.DisplayName = "OpenAI"
	}
	if opts.BaseURL == "" {
		opts.BaseURL = openAIDefaultBaseURL
	}
	opts.BaseURL = strings.TrimRight(opts.BaseURL, "/")

	// 默认工具循环轮数 200：未设置或小于等于 0 时用默认值
	if opts.MaxToolRounds <= 0 {
		opts.MaxToolRounds = 200
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 0}
	}

	h := &Harness{
		opts:            opts,
		modelHTTPClient: readhttp.NewClient(opts.HTTPClient),
		mu:              &sync.Mutex{},
		sessions:        make(map[string][]chatMessage),
	}
	// 启动预热：需要读钥匙串时先探一次，让 UI 首次拉取供应商列表时多半已有结论。
	// 判断条件与 Available 一致；加锁方式也保持一致，避免重复启动探测。
	if strings.TrimSpace(opts.APIKey) == "" && strings.TrimSpace(opts.FallbackAPIKey) == "" &&
		opts.Secrets != nil && strings.TrimSpace(opts.APIKeyAccount) != "" {
		h.availMu.Lock()
		h.availProbing = true
		h.availMu.Unlock()
		go h.probeAvailability()
	}
	return h
}

// PreserveSessionsFrom 更换端点设置时保留会话历史；进行中的旧实例也使用同一把锁。
func (h *Harness) PreserveSessionsFrom(previous harness.Harness) {
	if old, ok := previous.(*Harness); ok && old != h {
		h.mu = old.mu
		h.sessions = old.sessions
	}
}

// Kind 返回当前 Agent 后端的种类标识。
func (h *Harness) Kind() harness.Kind {
	return h.opts.Kind
}

// ConfirmsApprovalDelivery 返回 true，表示该 Provider 对每次 Approve/Interact 的答复都会调用 DeliveryAcker.AckDelivery。
func (h *Harness) ConfirmsApprovalDelivery() bool {
	return true
}

// DisplayName 返回当前提供方展示名称。
func (h *Harness) DisplayName() string {
	return h.opts.DisplayName
}

const (
	// modelListCacheTTL 是成功拉取模型列表后的缓存时长。
	modelListCacheTTL = 5 * time.Minute
	// modelListFailureTTL 是拉取失败后的缓存时长，避免频繁重试。
	modelListFailureTTL = 30 * time.Second
	// modelListRequestTimeout 是拉取 /models 的超时时间。
	modelListRequestTimeout = 5 * time.Second
	// availabilityCacheTTL 是钥匙串可用性探测结果的缓存时长。
	// macOS 下读一次密钥要起一个 `security` 子进程，启动路径（providers.list）不能每次等它。
	availabilityCacheTTL = 60 * time.Second
)

// availabilityResult 是一次 API 密钥探测的结果（ok=false 时 reason 可直接展示）。
type availabilityResult struct {
	ok     bool
	reason string
	at     time.Time
}

// ModelIDs 返回配置的模型与端点 /models 返回的模型合并去重后的列表，
// 配置的模型始终排在第一位。结果按 harness 实例缓存：成功 5 分钟，失败 30 秒。
// 缓存缺失或过期时先返回手上已有的目录（首次可能只有配置的模型），再在后台拉端点 /models：
// 供应商列表在桌面启动路径上，不能为一次网络请求等待。
func (h *Harness) ModelIDs() []string {
	h.modelMu.Lock()
	ids := append([]string(nil), h.modelIDs...)
	fresh := !h.modelExpiry.IsZero() && time.Now().Before(h.modelExpiry)
	startRefresh := !fresh && !h.modelRefreshing
	if startRefresh {
		h.modelRefreshing = true
	}
	h.modelMu.Unlock()

	if startRefresh {
		go h.refreshModelsInBackground()
	}
	if len(ids) > 0 {
		return ids
	}
	// 端点目录还没到：先用配置的模型占位，后台拉取完成后由下一次读取补上。
	return mergeModelIDs(strings.TrimSpace(h.opts.Model), nil)
}

// refreshModelsInBackground 在后台拉取端点目录。缓存语义（成功 5 分钟、失败 30 秒
// 且保留旧目录）由 RefreshModels 负责，这里只补失败日志与启动打点。
func (h *Harness) refreshModelsInBackground() {
	ctx, cancel := context.WithTimeout(context.Background(), modelListRequestTimeout)
	defer cancel()
	if err := h.RefreshModels(ctx); err != nil {
		log.Printf("openai: 后台拉取模型目录失败: %v", err)
		startuptrace.Mark("providers: " + h.markKind() + " models failed")
		return
	}
	startuptrace.Mark("providers: " + h.markKind() + " models ready")
}

// cachedModelIDs 只读当前缓存，不触发任何拉取。
func (h *Harness) cachedModelIDs() []string {
	h.modelMu.Lock()
	defer h.modelMu.Unlock()
	return append([]string(nil), h.modelIDs...)
}

// modelIDsForRun 返回本轮请求可用的模型目录：缓存为空时同步拉一次端点目录。
// 发消息由用户触发，这里可以等网络；拉取失败就退回配置的模型。
func (h *Harness) modelIDsForRun(ctx context.Context) []string {
	if ids := h.cachedModelIDs(); len(ids) > 0 {
		return ids
	}
	_ = h.RefreshModels(ctx)
	return h.cachedModelIDs()
}

// markKind 返回启动打点用的供应商名（自定义端点会带来自己的 kind）。
func (h *Harness) markKind() string {
	if kind := strings.TrimSpace(string(h.opts.Kind)); kind != "" {
		return kind
	}
	return "openai"
}

// RefreshModels 强制刷新端点目录，失败时保留已有模型。
func (h *Harness) RefreshModels(ctx context.Context) error {
	h.modelMu.Lock()
	h.modelGen++
	gen := h.modelGen
	h.modelRefreshing = true
	h.modelMu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, modelListRequestTimeout)
	defer cancel()
	apiKey, _ := h.getAPIKey()
	fetched, err := ListModels(ctx, h.modelHTTPClient, h.opts.BaseURL, apiKey)
	h.modelMu.Lock()
	defer h.modelMu.Unlock()
	if gen != h.modelGen {
		return errors.New("刷新期间模型目录已变更")
	}
	h.modelRefreshing = false
	if err != nil {
		if len(h.modelIDs) == 0 {
			h.modelIDs = mergeModelIDs(strings.TrimSpace(h.opts.Model), nil)
		}
		h.modelExpiry = time.Now().Add(modelListFailureTTL)
		return err
	}
	h.modelIDs = mergeModelIDs(strings.TrimSpace(h.opts.Model), fetched)
	h.modelExpiry = time.Now().Add(modelListCacheTTL)
	return nil
}

// mergeModelIDs 把配置的模型放在首位，追加拉取到的模型并去重。
func mergeModelIDs(configured string, fetched []string) []string {
	var ids []string
	seen := make(map[string]struct{}, len(fetched)+1)
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	add(configured)
	for _, id := range fetched {
		add(id)
	}
	return ids
}

// isOfficialOpenAI 判断当前端点是否为 OpenAI 官方端点。
func (h *Harness) isOfficialOpenAI() bool {
	if strings.EqualFold(string(h.opts.Kind), "openai") {
		return true
	}
	u, err := url.Parse(strings.TrimSpace(h.opts.BaseURL))
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), "api.openai.com")
}

// Available 检查当前环境是否可获取到 API 密钥。
// 显式 APIKey / FallbackAPIKey 直接命中；需要读钥匙串时用带缓存的探测结果，
// 缓存过期时先返回旧结论并后台重探（stale-while-revalidate），
// 只有完全没有缓存时才按「暂时可用」返回，避免启动路径（providers.list）
// 为一次 `security` 子进程等待。
func (h *Harness) Available() (bool, string) {
	if strings.TrimSpace(h.opts.APIKey) != "" || strings.TrimSpace(h.opts.FallbackAPIKey) != "" {
		return true, ""
	}
	if h.opts.Secrets == nil || strings.TrimSpace(h.opts.APIKeyAccount) == "" {
		return false, "API key is required"
	}

	h.availMu.Lock()
	if res := h.availResult; res != nil {
		// 缓存有效直接复用；过期时先返回旧结论，再在后台重探纠正，
		// 否则已知没有密钥的供应商会在 TTL 过期后又闪成可用。
		ok, reason := res.ok, res.reason
		stale := time.Since(res.at) >= availabilityCacheTTL
		probe := stale && !h.availProbing
		if probe {
			h.availProbing = true
		}
		h.availMu.Unlock()
		if probe {
			go h.probeAvailability()
		}
		return ok, reason
	}
	probe := !h.availProbing
	if probe {
		h.availProbing = true
	}
	h.availMu.Unlock()

	if probe {
		go h.probeAvailability()
	}
	// 完全没有缓存（首次读取）：先按可用处理，探测完成后由下一次读取纠正。
	return true, ""
}

// probeAvailability 在后台确认钥匙串里是否已有该账号的密钥，并写回缓存。
func (h *Harness) probeAvailability() {
	ok, reason := h.readSecretAvailability()

	h.availMu.Lock()
	h.availResult = &availabilityResult{ok: ok, reason: reason, at: time.Now()}
	h.availProbing = false
	h.availMu.Unlock()

	name := "providers: " + h.markKind() + " api key "
	if ok {
		startuptrace.Mark(name + "ready")
		return
	}
	startuptrace.Mark(name + "missing")
}

// readSecretAvailability 读一次 Secrets 判断密钥是否存在，返回的文案与 Available 一致。
// 调用方需保证 Secrets 非空、APIKeyAccount 非空。
func (h *Harness) readSecretAvailability() (bool, string) {
	val, err := h.opts.Secrets.Get(h.opts.APIKeyAccount)
	if err == nil && strings.TrimSpace(val) != "" {
		return true, ""
	}
	if err != nil {
		if errors.Is(err, config.ErrSecretNotFound) {
			return false, fmt.Sprintf("API key account %q not found in secrets", h.opts.APIKeyAccount)
		}
		return false, fmt.Sprintf("failed to get API key from secrets: %v", err)
	}
	return false, "API key is required"
}

// getAPIKey 尝试从显式配置或 Secrets 存储中检索 API 密钥，若无值则回退使用 FallbackAPIKey。
func (h *Harness) getAPIKey() (string, error) {
	if k := strings.TrimSpace(h.opts.APIKey); k != "" {
		return k, nil
	}
	if h.opts.Secrets != nil && strings.TrimSpace(h.opts.APIKeyAccount) != "" {
		val, err := h.opts.Secrets.Get(h.opts.APIKeyAccount)
		if err == nil && strings.TrimSpace(val) != "" {
			return strings.TrimSpace(val), nil
		}
	}
	if fb := strings.TrimSpace(h.opts.FallbackAPIKey); fb != "" {
		return fb, nil
	}
	return "", errors.New("openai: API key is required but not provided")
}

// chatCompletionsURL 构造调用 Chat Completions 的目标 URL。
func (h *Harness) chatCompletionsURL() string {
	base := strings.TrimRight(h.opts.BaseURL, "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	return base + "/chat/completions"
}

// openAIDefaultBaseURL 是 OpenAI 官方端点，其它 BaseURL（含自定义端点）视为自定义端点。
const openAIDefaultBaseURL = "https://api.openai.com/v1"

// reasoningEffort 映射推理强度：
// 自定义端点原样透传；官方端点仅对推理模型生效（low/medium/high 原样，max/xhigh/extra-high 归一到 high），
// 非推理模型或不支持的取值不下发（返回空串）。
func (h *Harness) reasoningEffort(effort, model string) string {
	if effort == "" {
		return ""
	}
	// deepseek-reasoner 不接受 reasoning_effort，注入会被拒绝，故按模型名排除。
	if strings.EqualFold(model, "deepseek-reasoner") {
		return ""
	}
	if h.opts.BaseURL != openAIDefaultBaseURL {
		return effort
	}
	lower := strings.ToLower(model)
	isReasoning := strings.HasPrefix(lower, "o1") || strings.HasPrefix(lower, "o3") ||
		strings.Contains(lower, "reasoner") || strings.Contains(lower, "r1") ||
		strings.Contains(lower, "thinking") || strings.Contains(lower, "qwq")
	if !isReasoning {
		return ""
	}
	switch effort {
	case "low", "medium", "high":
		return effort
	case "max", "xhigh", "extra-high":
		return "high"
	default:
		return ""
	}
}

// nowTime 返回当前时间；测试可以通过 now 字段注入时钟。
func (h *Harness) nowTime() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// Run 执行一轮或多轮 OpenAI 兼容交互。
func (h *Harness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	apiKey, err := h.getAPIKey()
	if err != nil {
		return err
	}

	model := strings.TrimSpace(turn.Model)
	if model == "" {
		model = strings.TrimSpace(h.opts.Model)
	}
	if model == "" {
		if h.isOfficialOpenAI() {
			model = "gpt-4o"
		} else {
			ids := h.modelIDsForRun(ctx)
			if len(ids) == 0 {
				return errors.New("未配置模型，且端点未返回模型列表")
			}
			model = ids[0]
		}
	}

	sessionID := turn.SessionID
	if sessionID == "" {
		sessionID = generateSessionID()
		_ = harness.Emit(emit, harness.Event{
			Type:      harness.EventSession,
			SessionID: sessionID,
		})
	}

	h.mu.Lock()
	history := h.sessions[sessionID]
	h.mu.Unlock()

	// 历史记录为空时注入系统提示词
	if len(history) == 0 {
		history = append(history, chatMessage{
			Role:    "system",
			Content: buildSystemPrompt(h.opts.DisplayName, model, turn),
		})
	}

	// 构造用户消息（包含文本与多模态图片）
	userMsg, err := buildUserMessage(turn)
	if err != nil {
		return err
	}
	history = append(history, userMsg)

	// 每轮开始时发现 MCP 工具：端点失败只跳过该端点，工具列表缓存范围限于本轮。
	mcpTools := h.mcpToolsForTurn(ctx, turn)

	var (
		totalUsage     harness.Usage
		lastStopReason string
		roundCount     int
		// lastContextTokens 是本轮最后一次请求实际带入的上下文大小（prompt + completion），
		// 只用于「上下文窗口占用」口径；totalUsage.Input/OutputTokens 仍是多轮累加的计费用量。
		lastContextTokens int64
		// 每个工具轮（一次模型请求）单独计时，样本随本轮创建、随本轮丢弃。
		requestClock  harness.RequestClock
		requestSpeeds []harness.RequestSpeed
	)

	chatURL := h.chatCompletionsURL()

	for roundCount < h.opts.MaxToolRounds {
		if ctx.Err() != nil {
			h.saveSessionHistory(sessionID, history)
			return ctx.Err()
		}

		payload := map[string]any{
			"model":          model,
			"messages":       history,
			"stream":         true,
			"stream_options": map[string]any{"include_usage": true},
			"tools":          toolsForRequest(mcpTools),
			"tool_choice":    "auto",
		}
		// 推理强度：非空时按供应商规则映射后写入 reasoning_effort。
		if mapped := h.reasoningEffort(turn.Effort, model); mapped != "" {
			payload["reasoning_effort"] = mapped
		}

		bodyBytes, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("openai: marshal request: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatURL, bytes.NewReader(bodyBytes))
		if err != nil {
			return fmt.Errorf("openai: create request: %w", err)
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		for k, v := range h.opts.ExtraHeaders {
			req.Header.Set(k, v)
		}

		resp, err := h.opts.HTTPClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				h.saveSessionHistory(sessionID, history)
				return ctx.Err()
			}
			_ = harness.Emit(emit, harness.Event{
				Type: harness.EventError,
				Err:  err.Error(),
			})
			return fmt.Errorf("openai: request failed: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()

			var errResp struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
				Message string `json:"message"`
			}
			errMsg := string(body)
			if json.Unmarshal(body, &errResp) == nil {
				if errResp.Error.Message != "" {
					errMsg = errResp.Error.Message
				} else if errResp.Message != "" {
					errMsg = errResp.Message
				}
			}
			errMsg = strings.TrimSpace(errMsg)
			if errMsg == "" {
				errMsg = fmt.Sprintf("HTTP %d", resp.StatusCode)
			}

			_ = harness.Emit(emit, harness.Event{
				Type: harness.EventError,
				Err:  errMsg,
			})
			h.saveSessionHistory(sessionID, history)
			return fmt.Errorf("openai: HTTP %d: %s", resp.StatusCode, errMsg)
		}

		// 读取 SSE 数据流。部分网关忽略 stream:true，直接返回整段 JSON（Content-Type
		// 为 application/json）；这种响应交给 SSE 解析器只会读成空流，用户看到空回答，
		// 所以非 event-stream 响应先尝试按普通 JSON 归一化。
		bodyReader := io.Reader(resp.Body)
		if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
			raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxNonStreamBody))
			resp.Body.Close()
			if readErr != nil {
				_ = harness.Emit(emit, harness.Event{
					Type: harness.EventError,
					Err:  readErr.Error(),
				})
				h.saveSessionHistory(sessionID, history)
				return fmt.Errorf("openai: read response: %w", readErr)
			}
			data, normErr := normalizeNonStreamResponse(resp.Header.Get("Content-Type"), raw)
			switch {
			case normErr == nil:
				bodyReader = strings.NewReader("data: " + data + "\n\n")
			case looksLikeSSE(raw):
				// 少数服务端不设置 Content-Type 却仍在流式输出，按 SSE 处理。
				bodyReader = bytes.NewReader(raw)
			default:
				_ = harness.Emit(emit, harness.Event{
					Type: harness.EventError,
					Err:  normErr.Error(),
				})
				h.saveSessionHistory(sessionID, history)
				return normErr
			}
		}
		sse := newSSEReader(bodyReader)
		var (
			contentBuilder strings.Builder
			accMap         = make(map[int]*toolCallAccumulator)
			roundFinish    string
			streamErr      error
		)

		watchDone := make(chan struct{})
		defer close(watchDone)
		go func() {
			select {
			case <-ctx.Done():
				_ = resp.Body.Close()
			case <-watchDone:
			}
		}()

		for {
			if ctx.Err() != nil {
				resp.Body.Close()
				h.saveSessionHistory(sessionID, history)
				return ctx.Err()
			}

			ev, sErr := sse.readEvent()
			if sErr != nil {
				if ctx.Err() != nil {
					resp.Body.Close()
					h.saveSessionHistory(sessionID, history)
					return ctx.Err()
				}
				if !errors.Is(sErr, io.EOF) {
					streamErr = sErr
				}
				break
			}

			if isDoneChunk(ev.Data) {
				break
			}

			chunk, pErr := parseChatChunk(ev.Data)
			if pErr != nil {
				continue
			}

			if chunk.Error != nil && chunk.Error.Message != "" {
				streamErr = errors.New(chunk.Error.Message)
				break
			}

			// 统计 usage 信息
			if chunk.Usage != nil {
				// 上下文占用取最后一次请求：多轮工具调用时每轮 prompt 已包含历史，必须覆盖而非累加。
				if sum := chunk.Usage.PromptTokens + chunk.Usage.CompletionTokens; sum > 0 {
					lastContextTokens = sum
				}
				// 这条 usage 是本次请求（一个工具轮）的用量帧，用它结算该请求的速度样本。
				if speed, ok := requestClock.Finish(h.nowTime(), chunk.Usage.CompletionTokens); ok {
					requestSpeeds = append(requestSpeeds, speed)
				}
				if chunk.Usage.PromptTokens > 0 {
					totalUsage.InputTokens += chunk.Usage.PromptTokens
				}
				if chunk.Usage.CompletionTokens > 0 {
					totalUsage.OutputTokens += chunk.Usage.CompletionTokens
				}
				if chunk.Usage.PromptTokensDetails != nil && chunk.Usage.PromptTokensDetails.CachedTokens > 0 {
					totalUsage.CachedTokens += chunk.Usage.PromptTokensDetails.CachedTokens
				}
			}

			// 本次请求的第一帧内容（正文、推理或工具调用）到达时开始计时。
			// 放在 usage 之前处理：非流式网关会把它合成同一帧，此时不该留下未结算的计时。
			for _, choice := range chunk.Choices {
				if choice.Delta.Content != "" || choice.Delta.ReasoningContent != "" ||
					choice.Delta.Reasoning != "" || len(choice.Delta.ToolCalls) > 0 {
					requestClock.Begin(h.nowTime())
					break
				}
			}

			for _, choice := range chunk.Choices {
				if choice.FinishReason != nil && *choice.FinishReason != "" {
					roundFinish = *choice.FinishReason
				}

				if choice.Delta.Content != "" {
					contentBuilder.WriteString(choice.Delta.Content)
					_ = harness.Emit(emit, harness.Event{
						Type: harness.EventText,
						Text: choice.Delta.Content,
					})
				}

				think := choice.Delta.ReasoningContent
				if think == "" {
					think = choice.Delta.Reasoning
				}
				if think != "" {
					_ = harness.Emit(emit, harness.Event{
						Type: harness.EventReasoning,
						Text: think,
					})
				}

				if len(choice.Delta.ToolCalls) > 0 {
					feedToolCalls(accMap, choice.Delta.ToolCalls)
				}
			}
		}

		resp.Body.Close()

		if streamErr != nil {
			if ctx.Err() != nil {
				h.saveSessionHistory(sessionID, history)
				return ctx.Err()
			}
			_ = harness.Emit(emit, harness.Event{
				Type: harness.EventError,
				Err:  streamErr.Error(),
			})
			h.saveSessionHistory(sessionID, history)
			return fmt.Errorf("openai stream error: %w", streamErr)
		}

		lastStopReason = roundFinish
		if lastStopReason == "" {
			lastStopReason = "stop"
		}

		completedCalls := sortedToolCalls(accMap)

		// 检查本轮是否产生了工具调用
		if len(completedCalls) > 0 {
			var outgoing []chatOutgoingCall
			for _, tc := range completedCalls {
				outgoing = append(outgoing, chatOutgoingCall{
					ID:   tc.ID,
					Type: "function",
					Function: chatOutgoingFunc{
						Name:      tc.Name,
						Arguments: tc.Arguments,
					},
				})
			}

			// 追加 assistant 角色消息
			history = append(history, chatMessage{
				Role:      "assistant",
				Content:   contentBuilder.String(),
				ToolCalls: outgoing,
			})

			if roundFinish == "length" {
				const lengthErrMsg = "error: the response hit the output token limit before the tool arguments finished; re-issue the call in full"
				for _, tc := range completedCalls {
					if ctx.Err() != nil {
						h.saveSessionHistory(sessionID, history)
						return ctx.Err()
					}

					_ = harness.Emit(emit, harness.Event{
						Type:     harness.EventToolStart,
						ToolID:   tc.ID,
						ToolName: tc.Name,
						Input:    json.RawMessage(tc.Arguments),
					})

					_ = harness.Emit(emit, harness.Event{
						Type:    harness.EventToolEnd,
						ToolID:  tc.ID,
						Output:  lengthErrMsg,
						IsError: true,
					})

					history = append(history, chatMessage{
						Role:       "tool",
						ToolCallID: tc.ID,
						Content:    lengthErrMsg,
					})
				}
			} else {
				// 依次执行每个工具调用并记录结果
				for _, tc := range completedCalls {
					if ctx.Err() != nil {
						h.saveSessionHistory(sessionID, history)
						return ctx.Err()
					}

					_ = harness.Emit(emit, harness.Event{
						Type:     harness.EventToolStart,
						ToolID:   tc.ID,
						ToolName: tc.Name,
						Input:    json.RawMessage(tc.Arguments),
					})

					output, isErr := executeToolCallWithMCP(ctx, turn, tc, emit, mcpTools)

					_ = harness.Emit(emit, harness.Event{
						Type:    harness.EventToolEnd,
						ToolID:  tc.ID,
						Output:  output,
						IsError: isErr,
					})

					history = append(history, chatMessage{
						Role:       "tool",
						ToolCallID: tc.ID,
						Content:    output,
					})
				}
			}

			roundCount++
			if roundCount >= h.opts.MaxToolRounds {
				_ = harness.Emit(emit, harness.Event{
					Type: harness.EventError,
					Err:  fmt.Sprintf("reached maximum tool rounds limit (%d)", h.opts.MaxToolRounds),
				})
				break
			}
			continue
		}

		// 本轮无工具调用，追加 assistant 消息后结束循环
		history = append(history, chatMessage{
			Role:    "assistant",
			Content: contentBuilder.String(),
		})
		break
	}

	totalUsage.ContextTokens = lastContextTokens
	if len(requestSpeeds) > 0 {
		totalUsage.Requests = append([]harness.RequestSpeed(nil), requestSpeeds...)
	}

	if totalUsage.InputTokens > 0 || totalUsage.OutputTokens > 0 || totalUsage.CachedTokens > 0 {
		_ = harness.Emit(emit, harness.Event{
			Type:  harness.EventUsage,
			Usage: &totalUsage,
		})
	}

	_ = harness.Emit(emit, harness.Event{
		Type:       harness.EventDone,
		StopReason: lastStopReason,
	})

	h.saveSessionHistory(sessionID, history)
	return nil
}

// cancelledToolCallContent 是取消路径补写的工具结果内容。
const cancelledToolCallContent = "cancelled: the user interrupted this tool call before it finished"

// closeDanglingToolCalls 为取消路径补全缺失的工具结果消息。
// 带 tool_calls 的 assistant 消息先写入了历史，若取消发生在工具执行中途，
// 对应的 role:"tool" 结果就缺失了，OpenAI 兼容端点会在之后每一轮都返回 400。
func closeDanglingToolCalls(history []chatMessage) []chatMessage {
	lastAssistant := -1
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "assistant" && len(history[i].ToolCalls) > 0 {
			lastAssistant = i
			break
		}
	}
	if lastAssistant < 0 {
		return history
	}

	answered := make(map[string]bool, len(history)-lastAssistant-1)
	for _, msg := range history[lastAssistant+1:] {
		if msg.Role == "tool" && msg.ToolCallID != "" {
			answered[msg.ToolCallID] = true
		}
	}

	for _, tc := range history[lastAssistant].ToolCalls {
		if answered[tc.ID] {
			continue
		}
		history = append(history, chatMessage{
			Role:       "tool",
			ToolCallID: tc.ID,
			Content:    cancelledToolCallContent,
		})
	}
	return history
}

// saveSessionHistory 安全地将更新后的对话历史保存到内存映射中。
func (h *Harness) saveSessionHistory(sessionID string, history []chatMessage) {
	history = closeDanglingToolCalls(history)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sessions[sessionID] = history
}

// randomHex 生成 n 字节的随机十六进制字符串。
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// generateSessionID 生成随机唯一的会话标识符。
func generateSessionID() string {
	return randomHex(16)
}

// buildSystemPrompt 构建 system 提示词：身份与工作目录、当前权限模式下能做什么、
// 通用工作规则，以及工作目录下的项目规则文档。
func buildSystemPrompt(displayName, model string, turn harness.Turn) string {
	var modeLine string
	switch turn.Mode {
	case harness.ModeReadOnly:
		modeLine = "Permissions: planning only. Investigate the project by reading, listing and searching, then lay out a numbered plan. Do not edit files or run anything that changes state."
	case harness.ModeSupervised:
		modeLine = "Permissions: supervised. Every tool call waits for the user to approve it in mcode."
	case harness.ModeFullAccess:
		modeLine = "Permissions: full access. Tools run without asking the user."
	default: // ModeAutoAcceptEdits、ModeAuto 与未知模式
		modeLine = "Permissions: file reads and edits run directly; shell commands wait for the user to approve them in mcode."
	}

	today := time.Now().UTC().Format("2006-01-02")

	guidance := loadProjectRules(turn.WorkDir)

	prompt := fmt.Sprintf(`You are %s (%s), running as a coding agent inside mcode on macOS. The project is at %s.
%s
How to work:
- Use the tools yourself rather than asking the user to run things, and look at a file before you change it.
- Refer to files by paths relative to the project root; stay inside the project.
- When you finish, say what you did and anything that still falls short.
- Keep each paragraph of prose on a single line instead of wrapping it at a fixed width, so it pastes cleanly anywhere; code blocks keep their own line breaks.
- When a tool result says the user rejected an action, don't try it again; ask the user what they want instead.
- The current date is %s.`, displayName, model, turn.WorkDir, modeLine, today)

	if guidance != "" {
		prompt += "\n\n" + guidance
	}

	return prompt
}

const (
	// projectRuleMaxFileBytes 是单个规则文档允许注入的字节上限。
	projectRuleMaxFileBytes = 16 * 1024
	// projectRuleMaxTotalBytes 是所有规则文档合计的字节上限。
	projectRuleMaxTotalBytes = 24 * 1024
	// projectRuleOversizeNotice 在规则文档超出预算时替代其内容注入。
	projectRuleOversizeNotice = "文件过大，请在会话中按需读取"
)

// loadProjectRules 加载工作目录下的规则文档：AGENTS.md 与 CLAUDE.md 同时存在时按
// AGENTS.md 在前、CLAUDE.md 在后的顺序拼接注入。单个文件超过 16KB 或注入总量超过
// 24KB 时只注入「文件过大，请在会话中按需读取」提示，避免规则文档撑爆上下文；
// 解析后逃出工作区的符号链接会被忽略。
func loadProjectRules(workDir string) string {
	if workDir == "" {
		return ""
	}
	root, err := filepath.Abs(workDir)
	if err != nil {
		return ""
	}
	rootReal := root
	if resolved, evalErr := filepath.EvalSymlinks(root); evalErr == nil {
		rootReal = resolved
	}

	var sections []string
	total := 0
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		data, ok := readRuleFile(root, rootReal, name)
		if !ok {
			continue
		}
		header := "Project rules from " + name + ":"
		if len(data) > projectRuleMaxFileBytes || total+len(data) > projectRuleMaxTotalBytes {
			sections = append(sections, header+"\n"+projectRuleOversizeNotice)
			continue
		}
		total += len(data)
		sections = append(sections, header+"\n"+string(data))
	}
	return strings.Join(sections, "\n\n")
}

// readRuleFile 读取单个规则文档；文件不存在、为空，或符号链接逃出工作区时返回 false。
func readRuleFile(root, rootReal, name string) ([]byte, bool) {
	p := filepath.Join(root, name)
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, false
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		realTarget, evalErr := filepath.EvalSymlinks(p)
		if evalErr != nil || !isWithinDir(rootReal, realTarget) {
			return nil, false
		}
	}
	data, err := os.ReadFile(p)
	if err != nil || len(data) == 0 {
		return nil, false
	}
	return data, true
}

// isWithinDir 报告 target 是否位于 dir 之下。
func isWithinDir(dir, target string) bool {
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// buildUserMessage 根据 Turn 的 Prompt 与 Images 组装用户消息（支持多模态 base64 data URL）。
func buildUserMessage(turn harness.Turn) (chatMessage, error) {
	if len(turn.Images) == 0 {
		return chatMessage{
			Role:    "user",
			Content: turn.Prompt,
		}, nil
	}

	var parts []any
	if turn.Prompt != "" {
		parts = append(parts, map[string]any{
			"type": "text",
			"text": turn.Prompt,
		})
	}

	images := 0
	for _, imgPath := range turn.Images {
		// 非图片附件（PDF、docx 等）不当图片下发，其路径已由上层写进 prompt。
		// 图片类型由 harness.ImageMediaType 判定：扩展名优先，扩展名不可靠时按文件内容嗅探。
		mimeType, ok := harness.ImageMediaType(imgPath)
		if !ok {
			log.Printf("openai: skip image %s: unsupported image type %q", imgPath, filepath.Ext(imgPath))
			continue
		}
		data, err := os.ReadFile(imgPath)
		if err != nil {
			return chatMessage{}, fmt.Errorf("read image %s: %w", imgPath, err)
		}
		encoded := base64.StdEncoding.EncodeToString(data)
		dataURL := fmt.Sprintf("data:%s;base64,%s", mimeType, encoded)

		parts = append(parts, map[string]any{
			"type": "image_url",
			"image_url": map[string]string{
				"url": dataURL,
			},
		})
		images++
	}

	// 附件全部不是图片时退化成纯文本 content，与没有图片的轮次保持一致。
	if images == 0 {
		return chatMessage{
			Role:    "user",
			Content: turn.Prompt,
		}, nil
	}

	return chatMessage{
		Role:    "user",
		Content: parts,
	}, nil
}
