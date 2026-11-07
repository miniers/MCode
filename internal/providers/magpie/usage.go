package magpie

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"mcode/internal/providers/readhttp"
)

const (
	// usageTimeout 是读取用量记录的超时：远程网关要多走一跳，比额度读取宽松一点。
	usageTimeout = 15 * time.Second
)

// ErrUsageForbidden 表示这个 key 没有读取用量记录的权限。
var ErrUsageForbidden = errors.New("magpie: 该 key 没有读取用量记录的权限（Usage）")

// usageHTTPClient 是读取用量记录的专属客户端：这里可能连远程网关，必须走环境代理，
// 所以沿用默认 Transport，只换连接池与超时。
var usageHTTPClient = readhttp.NewClient(&http.Client{Timeout: usageTimeout})

// UsageRow 是一条用量记录里判断「谁答的」需要的部分（对照 magpie/internal/usage/usage.go
// 的 Record）。ProviderAccount 是实际应答的账号邮箱；key 应答时是一段掩码文本，可能为空。
type UsageRow struct {
	T               time.Time
	Agent           string
	Provider        string
	ProviderAccount string
	Model           string
	Session         string
	NativeSession   string
}

// usageRequestsResponse 是 GET /v1/magpie/usage/requests 的响应，只解析用到的字段；
// t 先按字符串收下，再自己按 RFC3339Nano 解析，解析不了时留零值。
type usageRequestsResponse struct {
	Rows []struct {
		T               string `json:"t"`
		Agent           string `json:"agent"`
		Provider        string `json:"provider"`
		ProviderAccount string `json:"providerAccount"`
		Model           string `json:"model"`
		Session         string `json:"session"`
		NativeSession   string `json:"native_session"`
	} `json:"rows"`
}

// ReadUsageRequests 读取网关的用量记录，按时间从新到旧排列。agent 非空时由服务端
// 过滤，limit 非正时用服务端默认值。baseURL 可以是 /v1 结尾的端点地址，也可以直接
// 是根地址。
func ReadUsageRequests(ctx context.Context, baseURL, apiKey, agent string, limit int) ([]UsageRow, error) {
	origin := strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(baseURL), "/"), "/v1")
	if origin == "" {
		return nil, errors.New("magpie: 未配置端点地址")
	}
	params := url.Values{}
	if limit > 0 {
		params.Set("limit", strconv.Itoa(limit))
	}
	if name := strings.TrimSpace(agent); name != "" {
		params.Set("agent", name)
	}
	target := origin + "/v1/magpie/usage/requests"
	if encoded := params.Encode(); encoded != "" {
		target += "?" + encoded
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("magpie: 创建用量请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if key := strings.TrimSpace(apiKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := usageHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("magpie: 请求用量失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("magpie: 读取用量响应失败: %w", err)
	}
	switch {
	case resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: %d %s", ErrUsageForbidden, resp.StatusCode, snippet(body))
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, fmt.Errorf("magpie: 用量返回 %d: %s", resp.StatusCode, snippet(body))
	}
	var payload usageRequestsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("magpie: 解析用量响应失败: %w", err)
	}
	rows := make([]UsageRow, 0, len(payload.Rows))
	for _, row := range payload.Rows {
		at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(row.T))
		if err != nil {
			at = time.Time{}
		}
		rows = append(rows, UsageRow{
			T:               at,
			Agent:           row.Agent,
			Provider:        row.Provider,
			ProviderAccount: row.ProviderAccount,
			Model:           row.Model,
			Session:         row.Session,
			NativeSession:   row.NativeSession,
		})
	}
	return rows, nil
}
