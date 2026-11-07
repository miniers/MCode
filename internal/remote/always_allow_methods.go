package remote

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"mcode/internal/remote/wire"
)

// 本文件是「始终允许」持久化名单的远程扩展方法：桌面端可以在审批设置里看到并撤销，
// 移动端此前没有入口。存储层按「项目键」记账（见 approvals.go 的 ListAlwaysAllowed /
// RevokeAlwaysAllowed），而 Web 端只拿得到 projects.list 里的项目 ID，
// 因此这里负责把 projectID 换算成存储层认的键：
//   1. 项目路径（projects.list 的 path）——会话有工作目录时存储层正是用它记账；
//   2. 项目 ID 本身——会话没有工作目录时存储层退回项目 ID，两处都要查。

// alwaysAllowBackend 是撤销入口需要的后端能力，MemoryBackend 直接实现。
type alwaysAllowBackend interface {
	ListAlwaysAllowed(project string) []string
	RevokeAlwaysAllowed(project, key string) error
}

func init() {
	RegisterMethod("requests.alwaysAllowed.list", handleRequestsAlwaysAllowedList)
	RegisterMethod("requests.alwaysAllowed.revoke", handleRequestsAlwaysAllowedRevoke)
}

// handleRequestsAlwaysAllowedList 列出指定项目下持久化的「始终允许」记账键。
func handleRequestsAlwaysAllowedList(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.AlwaysAllowedListParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(p.ProjectID)
	if !isValidUUID(projectID) {
		return nil, ExtInvalidParams("Invalid params: 'projectID' must be a valid UUID")
	}
	backend, ok := alwaysAllowBackendOf(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	return wire.AlwaysAllowedListResult{Keys: alwaysAllowListKeys(ctx, d, backend, projectID)}, nil
}

// handleRequestsAlwaysAllowedRevoke 撤销一条「始终允许」并返回该项目剩下的记账键。
func handleRequestsAlwaysAllowedRevoke(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.AlwaysAllowedRevokeParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(p.ProjectID)
	if !isValidUUID(projectID) {
		return nil, ExtInvalidParams("Invalid params: 'projectID' must be a valid UUID")
	}
	key := strings.TrimSpace(p.Key)
	if key == "" {
		return nil, ExtInvalidParams("Invalid params: 'key' is required")
	}
	backend, ok := alwaysAllowBackendOf(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	// 存储层对不存在的键幂等返回 nil，两种项目键都撤一遍即可，不必判断键到底记在哪一处。
	for _, project := range alwaysAllowProjectKeys(ctx, d, projectID) {
		if err := backend.RevokeAlwaysAllowed(project, key); err != nil {
			return nil, alwaysAllowWrapError(err)
		}
	}
	return wire.AlwaysAllowedRevokeResult{Keys: alwaysAllowListKeys(ctx, d, backend, projectID)}, nil
}

// alwaysAllowBackendOf 取出后端实现的撤销能力；后端不支持时返回 false。
func alwaysAllowBackendOf(d *Dispatcher) (alwaysAllowBackend, bool) {
	if d == nil || d.backend == nil {
		return nil, false
	}
	backend, ok := d.backend.(alwaysAllowBackend)
	if !ok || backend == nil {
		return nil, false
	}
	return backend, true
}

// alwaysAllowListKeys 汇总该项目在两种项目键下的记账键，去重后按字典序返回。
func alwaysAllowListKeys(ctx context.Context, d *Dispatcher, backend alwaysAllowBackend, projectID string) []string {
	seen := make(map[string]struct{})
	keys := make([]string, 0, 4)
	for _, project := range alwaysAllowProjectKeys(ctx, d, projectID) {
		for _, key := range backend.ListAlwaysAllowed(project) {
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// alwaysAllowProjectKeys 把项目 ID 换算成存储层认的项目键：项目 ID 本身，加上 projects.list 里同 ID 的路径。
// 项目列表取不到时只返回项目 ID，撤销仍能覆盖「没有工作目录」的那部分记录。
func alwaysAllowProjectKeys(ctx context.Context, d *Dispatcher, projectID string) []string {
	keys := []string{projectID}
	if d == nil || d.backend == nil {
		return keys
	}
	list, err := d.backend.ListProjects(ctx)
	if err != nil {
		return keys
	}
	for _, project := range list.Projects {
		if project.ID != projectID {
			continue
		}
		if path := strings.TrimSpace(project.Path); path != "" && path != projectID {
			keys = append(keys, path)
		}
		break
	}
	return keys
}

// alwaysAllowWrapError 把存储写入错误转成可读的拒绝错误，避免按内部错误脱敏后前端只看到笼统提示。
func alwaysAllowWrapError(err error) error {
	if err == nil {
		return nil
	}
	var extErr *ExtError
	if errors.As(err, &extErr) {
		return extErr
	}
	return &ExtError{Code: wire.ErrCodeOperationRejected, Message: err.Error()}
}
