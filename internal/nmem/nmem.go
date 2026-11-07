// Package nmem 是 Nowledge Mem 记忆系统的 REST 客户端。
//
// 只用标准库（net/http + encoding/json），不引入第三方依赖。
// 服务端是远端 Mem 后端（也可指向本机 127.0.0.1:14242）；鉴权用 API key
// （nmem_ 前缀），每个请求同时带 Authorization: Bearer <key> 与
// X-NMEM-API-Key: <key>。GET /health 不需要鉴权。
//
// 连接配置按优先级解析：环境变量 NMEM_API_URL / NMEM_API_KEY →
// ~/.nowledge-mem/config.json（字段 apiUrl、apiKey）→ 本机默认地址。
// 必须配置 API key：没有 key 时不创建客户端，地址的回落只对已配置 key 的情况生效。
package nmem

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	envAPIURL = "NMEM_API_URL"
	envAPIKey = "NMEM_API_KEY"

	// DefaultBaseURL 是本机 Mem 后端的默认地址。
	DefaultBaseURL = "http://127.0.0.1:14242"

	// DefaultSpace 是未指定空间时使用的空间 ID。
	DefaultSpace = "default"

	// GlobalSpace 是跨项目通用记忆的空间 ID：项目空间以 shared 模式共享它，检索时一并带出。
	GlobalSpace = "global"

	configDirName  = ".nowledge-mem"
	configFileName = "config.json"

	requestTimeout = 10 * time.Second
	maxErrorBody   = 512
)

// UnitTypes 是服务端认可的记忆单元类型（全量 8 种）。
var UnitTypes = []string{
	"fact",
	"preference",
	"decision",
	"plan",
	"procedure",
	"learning",
	"context",
	"event",
}

var (
	// ErrNotConfigured 表示客户端缺少 API key（或 API 地址），请求未发出。
	// Default 在环境变量与配置文件里都没有 key 时直接返回该错误。
	ErrNotConfigured = errors.New("nmem: 未配置 API 地址或密钥")

	// ErrUnsupported 表示服务端没有提供该能力（例如蒸馏预览端点返回 404）。
	ErrUnsupported = errors.New("nmem: 服务端不支持该操作")
)

// Client 是 Nowledge Mem REST 客户端。
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// New 用给定地址与密钥创建客户端；baseURL 末尾的斜杠会被去掉。
func New(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:  strings.TrimSpace(apiKey),
		http:    &http.Client{Timeout: requestTimeout},
	}
}

// fileConfig 是 ~/.nowledge-mem/config.json 的字段子集。
type fileConfig struct {
	APIURL string `json:"apiUrl"`
	APIKey string `json:"apiKey"`
}

// Default 按「环境变量 → 配置文件 → 本机默认地址」的顺序生成客户端。
//
// 必须配置 API key：两处都没有 key 时返回 nil, ErrNotConfigured，不再生成指向本机
// 地址的空密钥客户端。key 已配置而地址缺失时仍回落到 DefaultBaseURL。
// 配置文件损坏时返回错误；配置文件不存在不是读取错误，只是因为没有 key 而返回
// ErrNotConfigured。
func Default() (*Client, error) {
	baseURL := strings.TrimSpace(os.Getenv(envAPIURL))
	apiKey := strings.TrimSpace(os.Getenv(envAPIKey))

	if baseURL == "" || apiKey == "" {
		cfg, err := readFileConfig()
		if err != nil {
			return nil, err
		}
		if baseURL == "" {
			baseURL = strings.TrimSpace(cfg.APIURL)
		}
		if apiKey == "" {
			apiKey = strings.TrimSpace(cfg.APIKey)
		}
	}
	if apiKey == "" {
		return nil, ErrNotConfigured
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return New(baseURL, apiKey), nil
}

func readFileConfig() (fileConfig, error) {
	var cfg fileConfig
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return cfg, nil
	}
	path := filepath.Join(home, configDirName, configFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("nmem: 读取 %s 失败: %w", path, err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fileConfig{}, fmt.Errorf("nmem: 解析 %s 失败: %w", path, err)
	}
	return cfg, nil
}

// APIError 表示服务端返回的非 2xx 响应；Body 已截断到 512 字节。
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("nmem: HTTP %d", e.Status)
	}
	return fmt.Sprintf("nmem: HTTP %d: %s", e.Status, e.Body)
}

// do 发出一个 JSON 请求；非 2xx 响应返回 *APIError。
// auth 为 true 表示该端点需要密钥，缺密钥时直接返回 ErrNotConfigured。
func (c *Client) do(ctx context.Context, method, path string, query url.Values, in, out any, auth bool) error {
	if c == nil || c.baseURL == "" {
		return ErrNotConfigured
	}
	if auth && c.apiKey == "" {
		return ErrNotConfigured
	}

	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	var body io.Reader
	if in != nil {
		buf, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("nmem: 编码请求失败: %w", err)
		}
		body = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return fmt.Errorf("nmem: 构造请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// 两种鉴权头都带：远端 Mem 后端认 Bearer，本地网关也认 X-NMEM-API-Key。
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("X-NMEM-API-Key", c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("nmem: 请求 %s %s 失败: %w", method, path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("nmem: 读取 %s %s 响应失败: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Status: resp.StatusCode, Body: c.scrubBody(data)}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("nmem: 解析 %s %s 响应失败: %w", method, path, err)
		}
	}
	return nil
}

// scrubBody 截断错误响应体，并抹掉可能被回显的密钥。
func (c *Client) scrubBody(data []byte) string {
	if len(data) > maxErrorBody {
		data = data[:maxErrorBody]
	}
	s := strings.TrimSpace(string(data))
	if c != nil && c.apiKey != "" {
		s = strings.ReplaceAll(s, c.apiKey, "[redacted]")
	}
	return s
}

// Health 检查后端可达性（GET /health，不需要鉴权）。
func (c *Client) Health(ctx context.Context) error {
	var res struct {
		Status string `json:"status"`
	}
	return c.do(ctx, http.MethodGet, "/health", nil, nil, &res, false)
}

// Space 是记忆空间（GET /spaces 的条目）。
type Space struct {
	ID      string
	Name    string
	Key     string
	Aliases []string
}

// Spaces 列出所有记忆空间（GET /spaces）。
func (c *Client) Spaces(ctx context.Context) ([]Space, error) {
	var res struct {
		Spaces []struct {
			ID      string   `json:"id"`
			Name    string   `json:"name"`
			Key     *string  `json:"key"`
			Aliases []string `json:"aliases"`
		} `json:"spaces"`
	}
	if err := c.do(ctx, http.MethodGet, "/spaces", nil, nil, &res, true); err != nil {
		return nil, err
	}
	out := make([]Space, 0, len(res.Spaces))
	for _, s := range res.Spaces {
		space := Space{ID: s.ID, Name: s.Name, Aliases: s.Aliases}
		if s.Key != nil {
			space.Key = *s.Key
		}
		out = append(out, space)
	}
	return out, nil
}

// ResolveSpace 不区分大小写地按 ID、Name、Key、Aliases 的先后顺序匹配空间名。
func ResolveSpace(spaces []Space, name string) (Space, bool) {
	want := strings.ToLower(strings.TrimSpace(name))
	if want == "" {
		return Space{}, false
	}
	for _, s := range spaces {
		if strings.ToLower(s.ID) == want {
			return s, true
		}
	}
	for _, s := range spaces {
		if strings.ToLower(s.Name) == want {
			return s, true
		}
	}
	for _, s := range spaces {
		if strings.ToLower(s.Key) == want {
			return s, true
		}
	}
	for _, s := range spaces {
		for _, alias := range s.Aliases {
			if strings.ToLower(alias) == want {
				return s, true
			}
		}
	}
	return Space{}, false
}

// WorkingMemory 读取空间的今日工作记忆 Markdown 正文（GET /agent/working-memory）。
// space 为空时用 DefaultSpace；服务端没有该空间的记忆时返回空字符串。
func (c *Client) WorkingMemory(ctx context.Context, space string) (string, error) {
	if strings.TrimSpace(space) == "" {
		space = DefaultSpace
	}
	query := url.Values{"space_id": {space}}
	var res struct {
		Content string `json:"content"`
		Exists  bool   `json:"exists"`
	}
	if err := c.do(ctx, http.MethodGet, "/agent/working-memory", query, nil, &res, true); err != nil {
		return "", err
	}
	return res.Content, nil
}

// Message 是导入对话中的一条消息。
type Message struct {
	Role      string
	Content   string
	Timestamp time.Time
}

// Thread 是要导入的对话。
type Thread struct {
	ID       string
	Title    string
	Source   string
	Space    string
	Messages []Message
	Metadata map[string]any
}

// threadImportMessage 对应 openapi 的 ThreadImportMessage。
type threadImportMessage struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp,omitempty"`
}

// threadImportRequest 对应 openapi 的 ThreadImportRequest（单条模式）。
type threadImportRequest struct {
	ThreadID string                `json:"thread_id,omitempty"`
	Title    string                `json:"title,omitempty"`
	Source   string                `json:"source,omitempty"`
	SpaceID  string                `json:"space_id,omitempty"`
	Messages []threadImportMessage `json:"messages,omitempty"`
	Metadata map[string]any        `json:"metadata,omitempty"`
}

// threadImportResponse 对应 openapi 的 ThreadImportResponse。
type threadImportResponse struct {
	Success     bool `json:"success"`
	FailedCount int  `json:"failed_count"`
	Results     []struct {
		Success       bool   `json:"success"`
		ThreadID      string `json:"thread_id"`
		AlreadyExists bool   `json:"already_existed"`
		MessagesAdded int    `json:"messages_added"`
		Error         string `json:"error"`
	} `json:"results"`
}

// ImportThread 幂等导入一条对话（POST /threads/import）。
//
// 同一个 thread_id 重复导入不会覆盖已有内容，也不会重复插入：服务端按消息
// 前缀去重，完全相同的重放返回 already_existed，后续新增的导出只追加缺失消息。
func (c *Client) ImportThread(ctx context.Context, t Thread) error {
	req := threadImportRequest{
		ThreadID: strings.TrimSpace(t.ID),
		Title:    t.Title,
		Source:   t.Source,
		SpaceID:  strings.TrimSpace(t.Space),
		Metadata: t.Metadata,
	}
	if n := len(t.Messages); n > 0 {
		msgs := make([]threadImportMessage, 0, n)
		for _, m := range t.Messages {
			msg := threadImportMessage{Role: m.Role, Content: m.Content}
			if !m.Timestamp.IsZero() {
				msg.Timestamp = m.Timestamp.UTC().Format(time.RFC3339Nano)
			}
			msgs = append(msgs, msg)
		}
		req.Messages = msgs
	}

	var res threadImportResponse
	if err := c.do(ctx, http.MethodPost, "/threads/import", nil, req, &res, true); err != nil {
		return err
	}
	if res.Success && res.FailedCount == 0 {
		return nil
	}
	detail := ""
	for _, r := range res.Results {
		if !r.Success || r.Error != "" {
			detail = r.Error
			break
		}
	}
	if detail == "" {
		detail = fmt.Sprintf("失败 %d 条", res.FailedCount)
	}
	return fmt.Errorf("nmem: 导入对话失败: %s", detail)
}

// Memory 是记忆条目。
type Memory struct {
	ID         string
	Title      string
	Content    string
	UnitType   string
	Importance float64
	Labels     []string
	CreatedAt  time.Time
	// UpdatedAt 是记忆最后更新时间；服务端没返回时为零值。
	UpdatedAt time.Time
	// Space 是记忆所在的空间 ID；服务端没返回时为空。
	Space string
	// Score 是检索时的相似度，只在检索结果里有值。
	Score float64
}

// MemoryInput 是要写入或预览生成的记忆。
type MemoryInput struct {
	Title      string
	Content    string
	UnitType   string
	Importance float64
	Labels     []string
	Source     string
	Metadata   map[string]any
}

// memoryNode 对应 openapi 的 MemoryNode（搜索结果为 MemorySearchResult.memory）。
type memoryNode struct {
	ID         string         `json:"id"`
	Title      string         `json:"title"`
	Content    string         `json:"content"`
	UnitType   string         `json:"unit_type"`
	Source     string         `json:"source"`
	SpaceID    string         `json:"space_id"`
	CreatedAt  string         `json:"created_at"`
	UpdatedAt  string         `json:"updated_at"`
	Importance *float64       `json:"importance"`
	Metadata   map[string]any `json:"metadata"`
	Labels     []string       `json:"labels"`
}

// toMemory 把服务端记忆节点映射成 Memory；labels 为空时回落到 metadata 里的同名字段。
func (n memoryNode) toMemory(labels []string) Memory {
	if len(labels) == 0 {
		labels = n.Labels
	}
	if len(labels) == 0 {
		labels = stringsFromAny(n.Metadata["labels"])
	}
	m := Memory{
		ID:        n.ID,
		Title:     n.Title,
		Content:   n.Content,
		UnitType:  n.UnitType,
		CreatedAt: parseTime(n.CreatedAt),
		UpdatedAt: parseTime(n.UpdatedAt),
		Labels:    labels,
		Space:     n.SpaceID,
	}
	if n.Importance != nil {
		m.Importance = *n.Importance
	}
	if len(m.Labels) == 0 {
		m.Labels = nil
	}
	return m
}

// SearchQuery 是语义检索的入参；除 Query 外都可留空。
type SearchQuery struct {
	Query string
	// Limit <= 0 时用服务端默认值（10）。
	Limit int
	// UnitType 只返回该类型的记忆（UnitTypes 之一）。
	UnitType string
	// Labels 只返回带这些标签的记忆。
	Labels []string
	// Mode 为 "deep"（服务端默认）或 "fast"（只走 BM25 + 向量）。
	Mode string
}

// SearchMemories 语义检索记忆（POST /memories/search）。
// space 为空时由服务端使用默认空间；limit <= 0 时用服务端默认值（10）。
func (c *Client) SearchMemories(ctx context.Context, space, query string, limit int) ([]Memory, error) {
	return c.Search(ctx, space, SearchQuery{Query: query, Limit: limit})
}

// Search 按 SearchQuery 语义检索记忆（POST /memories/search）。
// 服务端会按空间的检索模式带上共享空间（shared 模式）的记忆。
func (c *Client) Search(ctx context.Context, space string, q SearchQuery) ([]Memory, error) {
	req := struct {
		Query        string   `json:"query"`
		Limit        int      `json:"limit,omitempty"`
		SpaceID      string   `json:"space_id,omitempty"`
		UnitType     string   `json:"unit_type,omitempty"`
		FilterLabels []string `json:"filter_labels,omitempty"`
		Mode         string   `json:"mode,omitempty"`
	}{
		Query:        q.Query,
		SpaceID:      strings.TrimSpace(space),
		UnitType:     strings.TrimSpace(q.UnitType),
		FilterLabels: q.Labels,
		Mode:         strings.TrimSpace(q.Mode),
	}
	if q.Limit > 0 {
		req.Limit = q.Limit
	}

	var res []struct {
		Memory          *memoryNode `json:"memory"`
		SimilarityScore float64     `json:"similarity_score"`
	}
	if err := c.do(ctx, http.MethodPost, "/memories/search", nil, req, &res, true); err != nil {
		return nil, err
	}
	out := make([]Memory, 0, len(res))
	for _, r := range res {
		if r.Memory == nil {
			continue
		}
		m := r.Memory.toMemory(nil)
		m.Score = r.SimilarityScore
		out = append(out, m)
	}
	return out, nil
}

// memoryCreateRequest 对应 openapi 的 MemoryCreateRequest。
type memoryCreateRequest struct {
	Title      string         `json:"title,omitempty"`
	Content    string         `json:"content"`
	UnitType   string         `json:"unit_type,omitempty"`
	Importance *float64       `json:"importance,omitempty"`
	Labels     []string       `json:"labels,omitempty"`
	Source     string         `json:"source,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	SpaceID    string         `json:"space_id,omitempty"`
}

// AddMemory 新增一条记忆（POST /memories）。
// Importance <= 0 时不显式发送，由服务端应用默认值 0.5。
func (c *Client) AddMemory(ctx context.Context, space string, in MemoryInput) (Memory, error) {
	req := memoryCreateRequest{
		Title:    in.Title,
		Content:  in.Content,
		UnitType: in.UnitType,
		Labels:   in.Labels,
		Source:   in.Source,
		Metadata: in.Metadata,
		SpaceID:  strings.TrimSpace(space),
	}
	if in.Importance > 0 {
		importance := in.Importance
		req.Importance = &importance
	}

	var res struct {
		Memory         memoryNode `json:"memory"`
		AssignedLabels []string   `json:"assigned_labels"`
	}
	if err := c.do(ctx, http.MethodPost, "/memories", nil, req, &res, true); err != nil {
		return Memory{}, err
	}
	m := res.Memory.toMemory(res.AssignedLabels)
	if m.Importance == 0 {
		m.Importance = in.Importance
	}
	return m, nil
}

// GetMemory 按 ID 读取一条记忆（GET /memories/{id}）。
// space 是服务端的空间守卫：记忆不在该空间时返回 HTTP 404（*APIError），不会跨空间读到。
func (c *Client) GetMemory(ctx context.Context, space, id string) (Memory, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Memory{}, errors.New("nmem: 记忆 ID 为空")
	}
	var query url.Values
	if s := strings.TrimSpace(space); s != "" {
		query = url.Values{"space_id": {s}}
	}
	var res memoryNode
	if err := c.do(ctx, http.MethodGet, "/memories/"+url.PathEscape(id), query, nil, &res, true); err != nil {
		return Memory{}, err
	}
	return res.toMemory(nil), nil
}

// UpdateMemory 改写一条已有记忆的标题、内容与重要度（PATCH /memories/{id}）。
// Importance <= 0 时不发送，保留原值；UnitType、Labels 与 Source 不改。
func (c *Client) UpdateMemory(ctx context.Context, id string, in MemoryInput) (Memory, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Memory{}, errors.New("nmem: 记忆 ID 为空")
	}
	req := map[string]any{"title": in.Title, "content": in.Content}
	if in.Importance > 0 {
		req["importance"] = in.Importance
	}
	var res memoryNode
	if err := c.do(ctx, http.MethodPatch, "/memories/"+url.PathEscape(id), nil, req, &res, true); err != nil {
		return Memory{}, err
	}
	return res.toMemory(nil), nil
}

// DeleteMemory 删除一条记忆（DELETE /memories/{id}）。
// 服务端不支持删除或记忆不存在时返回 *APIError，调用方可用 errors.As 取状态码。
func (c *Client) DeleteMemory(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("nmem: 记忆 ID 为空")
	}
	return c.do(ctx, http.MethodDelete, "/memories/"+url.PathEscape(id), nil, nil, nil, true)
}

// DeprecatedTitlePrefix 是服务端不支持删除时给记忆标题打上的废弃标记前缀。
const DeprecatedTitlePrefix = "[已废弃]"

// IsDeprecated 报告一条记忆是否已被标记为废弃（标题以 DeprecatedTitlePrefix 开头）。
func IsDeprecated(m Memory) bool {
	return strings.HasPrefix(strings.TrimSpace(m.Title), DeprecatedTitlePrefix)
}

// ForgetMemory 忘掉一条记忆：优先真删除；服务端不支持删除（404/405/501）时改用
// 在标题前加 DeprecatedTitlePrefix 标记废弃，内容与重要度不变，此时 deleted 为 false。
// 其它错误直接返回；GET 或 PATCH 失败时返回该错误。
func (c *Client) ForgetMemory(ctx context.Context, space, id string) (deleted bool, err error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, errors.New("nmem: 记忆 ID 为空")
	}
	if err := c.DeleteMemory(ctx, id); err == nil {
		return true, nil
	} else if !isUnsupportedDelete(err) {
		return false, err
	}

	m, err := c.GetMemory(ctx, space, id)
	if err != nil {
		return false, err
	}
	title := strings.TrimSpace(m.Title)
	if title == "" {
		title = "未命名记忆"
	}
	if !strings.HasPrefix(title, DeprecatedTitlePrefix) {
		title = DeprecatedTitlePrefix + " " + title
	}
	if _, err := c.UpdateMemory(ctx, id, MemoryInput{Title: title, Content: m.Content, Importance: m.Importance}); err != nil {
		return false, err
	}
	return false, nil
}

// isUnsupportedDelete 判断删除失败是否因为服务端不支持删除：404、405 与 501。
func isUnsupportedDelete(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Status {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	}
	return false
}

// ListMemories 分页列出一个空间里的记忆（GET /memories）。
// space 为空时由服务端使用默认空间；limit <= 0 时用服务端默认值，offset 从 0 开始。
// 响应解析兼容 {"memories": [...]} 与裸数组两种形状。
func (c *Client) ListMemories(ctx context.Context, space string, limit, offset int) ([]Memory, error) {
	q := url.Values{}
	if s := strings.TrimSpace(space); s != "" {
		q.Set("space_id", s)
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	if offset > 0 {
		q.Set("offset", fmt.Sprint(offset))
	}
	var raw json.RawMessage
	if err := c.do(ctx, http.MethodGet, "/memories", q, nil, &raw, true); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var res struct {
		Memories []memoryNode `json:"memories"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		var nodes []memoryNode
		if err := json.Unmarshal(raw, &nodes); err != nil {
			return nil, fmt.Errorf("nmem: 解析 GET /memories 响应失败: %w", err)
		}
		res.Memories = nodes
	}
	out := make([]Memory, 0, len(res.Memories))
	for _, n := range res.Memories {
		out = append(out, n.toMemory(nil))
	}
	return out, nil
}

// ThreadHit 是线程检索的一条结果。
type ThreadHit struct {
	ThreadID     string
	Title        string
	Source       string
	MessageCount int
	LastActivity time.Time
	Matches      []ThreadMatch
}

// ThreadMatch 是线程里命中检索词的一条消息摘录。
type ThreadMatch struct {
	Index   int
	Role    string
	Snippet string
}

// SearchThreads 全文检索已同步的对话（GET /threads/search）。
// space 限定检索空间；limit <= 0 时用服务端默认值（20）。
func (c *Client) SearchThreads(ctx context.Context, space, query string, limit int) ([]ThreadHit, error) {
	q := url.Values{"query": {query}}
	if s := strings.TrimSpace(space); s != "" {
		q.Set("space_id", s)
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	var res struct {
		Threads []struct {
			ThreadID        string `json:"thread_id"`
			Title           string `json:"title"`
			Source          string `json:"source"`
			MessageCount    int    `json:"message_count"`
			LastActivity    string `json:"last_activity"`
			MatchedMessages []struct {
				Index   int    `json:"message_index"`
				Role    string `json:"role"`
				Snippet string `json:"snippet"`
			} `json:"matched_messages"`
		} `json:"threads"`
	}
	if err := c.do(ctx, http.MethodGet, "/threads/search", q, nil, &res, true); err != nil {
		return nil, err
	}
	out := make([]ThreadHit, 0, len(res.Threads))
	for _, t := range res.Threads {
		hit := ThreadHit{
			ThreadID:     t.ThreadID,
			Title:        t.Title,
			Source:       t.Source,
			MessageCount: t.MessageCount,
			LastActivity: parseTime(t.LastActivity),
		}
		for _, m := range t.MatchedMessages {
			hit.Matches = append(hit.Matches, ThreadMatch{Index: m.Index, Role: m.Role, Snippet: m.Snippet})
		}
		out = append(out, hit)
	}
	return out, nil
}

// ThreadPage 是一段对话消息（GET /threads/{id} 的分页结果）。
type ThreadPage struct {
	ThreadID string
	Title    string
	// Total 是对话的消息总数，Offset 是 Messages 第一条的序号（从 0 开始）。
	Total    int
	Offset   int
	Messages []Message
}

// FetchThread 分页读取一条对话的消息（GET /threads/{id}）。
// space 是服务端的空间守卫，对话不在该空间时返回 HTTP 404；limit <= 0 时不限条数。
func (c *Client) FetchThread(ctx context.Context, space, threadID string, offset, limit int) (ThreadPage, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return ThreadPage{}, errors.New("nmem: 对话 ID 为空")
	}
	q := url.Values{}
	if s := strings.TrimSpace(space); s != "" {
		q.Set("space_id", s)
	}
	if offset > 0 {
		q.Set("offset", fmt.Sprint(offset))
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	var res struct {
		Thread struct {
			ThreadID     string `json:"thread_id"`
			Title        string `json:"title"`
			MessageCount int    `json:"message_count"`
		} `json:"thread"`
		Messages []struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			CreatedAt string `json:"created_at"`
			Timestamp string `json:"timestamp"`
		} `json:"messages"`
		TotalMessages int `json:"total_messages"`
	}
	if err := c.do(ctx, http.MethodGet, "/threads/"+url.PathEscape(threadID), q, nil, &res, true); err != nil {
		return ThreadPage{}, err
	}
	page := ThreadPage{
		ThreadID: res.Thread.ThreadID,
		Title:    res.Thread.Title,
		Total:    res.TotalMessages,
		Offset:   offset,
	}
	if page.ThreadID == "" {
		page.ThreadID = threadID
	}
	if page.Total == 0 {
		page.Total = res.Thread.MessageCount
	}
	for _, m := range res.Messages {
		ts := m.Timestamp
		if ts == "" {
			ts = m.CreatedAt
		}
		page.Messages = append(page.Messages, Message{Role: m.Role, Content: m.Content, Timestamp: parseTime(ts)})
	}
	return page, nil
}

// DistillPreview 预览从对话中会提炼出哪些记忆（POST /memories/distill/preview）。
//
// 该端点只做预览、不落库（落库要用 POST /memories/distill）。服务端没有这个
// 端点（HTTP 404）时返回 ErrUnsupported。
func (c *Client) DistillPreview(ctx context.Context, space, threadID string) ([]MemoryInput, error) {
	req := struct {
		ThreadID string `json:"thread_id"`
		SpaceID  string `json:"space_id,omitempty"`
	}{
		ThreadID: strings.TrimSpace(threadID),
		SpaceID:  strings.TrimSpace(space),
	}

	var res struct {
		Success  bool             `json:"success"`
		Memories []map[string]any `json:"memories"`
		Error    string           `json:"error"`
	}
	err := c.do(ctx, http.MethodPost, "/memories/distill/preview", nil, req, &res, true)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return nil, ErrUnsupported
		}
		return nil, err
	}

	out := make([]MemoryInput, 0, len(res.Memories))
	for _, item := range res.Memories {
		in := MemoryInput{
			Title:      firstString(item, "title", "name"),
			Content:    firstString(item, "content", "text", "body"),
			UnitType:   normalizeUnitType(firstString(item, "unit_type", "unitType")),
			Importance: firstFloat(item, "importance", "score"),
			Labels:     stringsFromAny(item["labels"]),
			Source:     firstString(item, "source"),
			Metadata:   anyMap(item["metadata"]),
		}
		if in.Title == "" && in.Content == "" {
			continue
		}
		out = append(out, in)
	}
	if len(out) == 0 && !res.Success {
		if res.Error != "" {
			return nil, fmt.Errorf("nmem: 蒸馏预览失败: %s", res.Error)
		}
		return nil, errors.New("nmem: 蒸馏预览未返回任何记忆")
	}
	return out, nil
}

// normalizeUnitType 校验并归一化单元类型；未知类型返回空串。
func normalizeUnitType(s string) string {
	v := strings.ToLower(strings.TrimSpace(s))
	for _, t := range UnitTypes {
		if t == v {
			return v
		}
	}
	return ""
}

// firstString 依次尝试多个键，返回第一个非空字符串值。
func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
	}
	return ""
}

// firstFloat 依次尝试多个键，返回第一个数值。
func firstFloat(m map[string]any, keys ...string) float64 {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if f, ok := v.(float64); ok {
				return f
			}
		}
	}
	return 0
}

// anyMap 断言 map[string]any，失败返回 nil。
func anyMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// stringsFromAny 把 []any / []string 转成 []string，失败返回 nil。
func stringsFromAny(v any) []string {
	switch vv := v.(type) {
	case []string:
		return vv
	case []any:
		out := make([]string, 0, len(vv))
		for _, item := range vv {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return nil
	}
}

// parseTime 尽量解析服务端时间串（带时区或纯本地时间），失败返回零值。
func parseTime(s string) time.Time {
	if strings.TrimSpace(s) == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
