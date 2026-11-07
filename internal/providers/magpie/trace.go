package magpie

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// traceTimeout 是读取 trace 的超时：网关长轮询最多等 25 秒，客户端留一点余量。
	traceTimeout = 30 * time.Second
)

// ErrTraceForbidden 表示这个 key 没有读取 trace 的权限：magpie 只把路由告诉本机，
// 来自别的机器或受限 key 的请求一律被拒。
var ErrTraceForbidden = errors.New("magpie: 该 key 没有读取 trace 的权限（Trace）")

// traceHTTPClient 是读取 trace 的专属客户端：回环地址不能走代理，否则 magpie 的
// local(r) 会把请求当成别的机器发来的而拒绝。测试里会替换成 httptest 的客户端。
var traceHTTPClient = &http.Client{Timeout: traceTimeout, Transport: newTraceTransport()}

// newTraceTransport 返回克隆自默认 Transport 的 Transport，只把 Proxy 置空：
// 回环请求必须直连，其余拨号参数保持默认。
func newTraceTransport() *http.Transport {
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		tr := base.Clone()
		tr.Proxy = nil
		return tr
	}
	return &http.Transport{Proxy: nil}
}

// TraceSnapshot 是一次 GET /v1/magpie/trace 的结果，只保留判断「谁答的」需要的部分
// （对照 magpie/internal/gateway/trace.go 的 TraceState）。
type TraceSnapshot struct {
	Seq    int64        `json:"seq"`
	Routes []TraceRoute `json:"routes"`
}

// TraceRoute 是网关处理过的一条请求及它的路由结果，字段对照 magpie 的 Route，
// 只解析判断「谁答的」用到的部分。
type TraceRoute struct {
	ID       int64          `json:"id"`
	Session  string         `json:"session"`
	Agent    string         `json:"agent"`
	Model    string         `json:"model"`
	Done     bool           `json:"done"`
	Order    []TraceWeighed `json:"order"`
	Left     []TraceWeighed `json:"left"`
	Tries    []TraceTry     `json:"tries"`
	Affinity *TraceAffinity `json:"affinity"`
}

// TraceWeighed 是路由权衡过的一个账号、key 或 provider（magpie 的 Weighed）。
type TraceWeighed struct {
	// ID 是判定成败后留下的标识，与 Try.ID 对应。
	ID string `json:"id"`
	// Provider 是它所属的 provider id（如 claude）。
	Provider string `json:"provider"`
	// Name 是 provider 的显示名。
	Name string `json:"name"`
	// Who 是账号（邮箱、login），key 用 key 名或掩码，provider 没有 who。
	Who string `json:"who"`
	// Kind 是 "account"、"key" 或 "provider"。
	Kind string `json:"kind"`
	// Plan 是账号的方案名，没有时为空。
	Plan string `json:"plan"`
}

// TraceTry 是一次尝试（magpie 的 Try）：Done 与 Status 用来判断它是否成功。
type TraceTry struct {
	ID     string `json:"id"`
	Done   bool   `json:"done"`
	Status int    `json:"status"`
}

// TraceAffinity 是会话粘性（magpie 的 Affinity）：Last 是上一次应答这个会话的
// 账号标识，与 Weighed.ID 对应；为空表示该 provider 没开粘性。
type TraceAffinity struct {
	Mode string `json:"mode"`
	Last string `json:"last"`
}

// ReadTrace 读取网关的路由记录：after 之后的变化，wait 为真时网关会等到有变化或
// 最多 25 秒。baseURL 可以是 /v1 结尾的端点地址，也可以直接是根地址。
func ReadTrace(ctx context.Context, baseURL, apiKey string, after int64, wait bool) (TraceSnapshot, error) {
	origin := strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(baseURL), "/"), "/v1")
	if origin == "" {
		return TraceSnapshot{}, errors.New("magpie: 未配置端点地址")
	}
	target := origin + "/v1/magpie/trace?after=" + strconv.FormatInt(after, 10)
	if wait {
		target += "&wait=1"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return TraceSnapshot{}, fmt.Errorf("magpie: 创建 trace 请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if key := strings.TrimSpace(apiKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := traceHTTPClient.Do(req)
	if err != nil {
		return TraceSnapshot{}, fmt.Errorf("magpie: 请求 trace 失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return TraceSnapshot{}, fmt.Errorf("magpie: 读取 trace 响应失败: %w", err)
	}
	switch {
	case resp.StatusCode == http.StatusForbidden:
		return TraceSnapshot{}, fmt.Errorf("%w: %d %s", ErrTraceForbidden, resp.StatusCode, snippet(body))
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return TraceSnapshot{}, fmt.Errorf("magpie: trace 返回 %d: %s", resp.StatusCode, snippet(body))
	}
	var snapshot TraceSnapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		return TraceSnapshot{}, fmt.Errorf("magpie: 解析 trace 响应失败: %w", err)
	}
	return snapshot, nil
}

// ServedAccount 取出一条路由实际应答的账号。顺序同方案 2.1 节：
//
//  1. 取最后一条成功的 try（done 且 2xx）；流式进行中尚无 done 时，退而取最后
//     一条 2xx 的 try。
//  2. 用 try 的 id 在 order 里找 weighed.id，找不到再找 left。
//  3. 查到后返回它的 provider、who、kind。
//  4. 都没有时用 affinity.last 匹配 order/left，再取那一条。
//
// 判断不了时返回 ok=false，不猜。
func ServedAccount(r TraceRoute) (provider, who, kind string, ok bool) {
	p, w, k, found := servedByID(r, succeededTry(r.Tries))
	if found {
		return p, w, k, true
	}
	if r.Affinity != nil {
		return servedByID(r, r.Affinity.Last)
	}
	return "", "", "", false
}

// servedByID 在 order 里找 id 对应的条目，找不到再找 left；条目没有 provider 时
// 当作判断不了。
func servedByID(r TraceRoute, id string) (provider, who, kind string, ok bool) {
	if w, found := weighedByID(r.Order, id); found && w.Provider != "" {
		return w.Provider, w.Who, w.Kind, true
	}
	if w, found := weighedByID(r.Left, id); found && w.Provider != "" {
		return w.Provider, w.Who, w.Kind, true
	}
	return "", "", "", false
}

// succeededTry 返回实际应答的那次尝试的 id：最后一条完成的 2xx try；没有时取最后
// 一条 2xx 的 try（流式进行中还没有 done）；都没有时为空串。
func succeededTry(tries []TraceTry) string {
	for i := len(tries) - 1; i >= 0; i-- {
		if t := tries[i]; t.Done && t.Status >= 200 && t.Status < 300 {
			return t.ID
		}
	}
	for i := len(tries) - 1; i >= 0; i-- {
		if t := tries[i]; t.Status >= 200 && t.Status < 300 {
			return t.ID
		}
	}
	return ""
}

// weighedByID 在 ws 里找 id 相同的条目；id 为空或没有时返回 false。
func weighedByID(ws []TraceWeighed, id string) (TraceWeighed, bool) {
	if id == "" {
		return TraceWeighed{}, false
	}
	for _, w := range ws {
		if w.ID == id {
			return w, true
		}
	}
	return TraceWeighed{}, false
}
