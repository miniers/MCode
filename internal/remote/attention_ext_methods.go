package remote

import (
	"context"
	"encoding/json"
	"strings"

	"mcode/internal/remote/wire"
)

// 本文件把桌面端的待处理收件箱开放给远程客户端：
//   - attention.list：按项目列出待处理事件，默认只列未处理的，并带上未处理总数与审批建议；
//   - attention.resolve：手动消解一条事件；
//   - attention.confirmSuggestion：用户确认总管给的审批建议，后端按其答复请求。
//
// 事件新增、自动消解与手动消解统一由 app 层订阅 attention 服务后调 NotifyAttentionChanged
// 广播 attention.changed（见 internal/app/remote_service.go），handler 里不再重复广播。
// 桌面端同源逻辑见 internal/app/client_attention.go。

// attentionExtService 是待处理收件箱服务，由 app 层注入（见 internal/app/remote_attention.go）。
// 未注入时各 RPC 统一返回 MethodNotFound，前端据此隐藏「待处理」入口。
type attentionExtService interface {
	// List 按参数列出事件；Limit 已由调用方归一化，结果只需填 Items。
	List(ctx context.Context, params wire.AttentionExtListParams) (wire.AttentionExtListResult, error)
	// Resolve 手动消解一条事件；事件已消解或不存在时不报错。
	Resolve(ctx context.Context, id string) error
	// UnresolvedCount 返回全部未处理事件的条数。
	UnresolvedCount(ctx context.Context) (int, error)
	// ConfirmSuggestion 按总管的建议答复该请求；建议已失效或已变更时返回错误，不执行审批。
	ConfirmSuggestion(ctx context.Context, threadID, requestID, optionID string) error
}

const (
	// attentionExtServiceName 是待处理收件箱服务在服务注册表里的名字。
	attentionExtServiceName = "attention"
	// attentionExtChangedMethod 是待处理事件变化后的广播方法名。
	attentionExtChangedMethod = "attention.changed"
)

// attention.list 的条数限制。
const (
	attentionExtDefaultLimit = 100
	attentionExtMaxLimit     = 500
)

func init() {
	RegisterMethod("attention.list", handleAttentionList)
	RegisterMethod("attention.resolve", handleAttentionResolve)
	RegisterMethod("attention.confirmSuggestion", handleAttentionConfirmSuggestion)
}

// handleAttentionList 列出待处理事件并附上未处理总数。
func handleAttentionList(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.AttentionExtListParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	svc, ok := backendService[attentionExtService](d.backend, attentionExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	p.ProjectID = strings.TrimSpace(p.ProjectID)
	p.Limit = attentionExtLimit(p.Limit)
	result, err := svc.List(ctx, p)
	if err != nil {
		return nil, err
	}
	if result.Items == nil {
		// 统一输出空数组，别让客户端处理 null。
		result.Items = []wire.AttentionExtItem{}
	}
	count, err := svc.UnresolvedCount(ctx)
	if err != nil {
		return nil, err
	}
	result.UnresolvedCount = count
	return result, nil
}

// handleAttentionResolve 手动消解一条待处理事件；变更推送由订阅侧统一发出。
func handleAttentionResolve(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.AttentionExtResolveParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(p.ID)
	if id == "" {
		return nil, ExtInvalidParams("Invalid params: 'id' is required")
	}
	svc, ok := backendService[attentionExtService](d.backend, attentionExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	if err := svc.Resolve(ctx, id); err != nil {
		return nil, err
	}
	return wire.AttentionExtResolveResult{OK: true}, nil
}

// handleAttentionConfirmSuggestion 让用户确认总管的审批建议，后端再按建议答复请求。
// 建议是否仍然存在、选项是否已被更新由服务层判定；变更推送由订阅侧统一发出，这里不广播。
func handleAttentionConfirmSuggestion(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.AttentionExtConfirmSuggestionParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	threadID := strings.TrimSpace(p.ThreadID)
	requestID := strings.TrimSpace(p.RequestID)
	optionID := strings.TrimSpace(p.OptionID)
	if threadID == "" || requestID == "" || optionID == "" {
		return nil, ExtInvalidParams("Invalid params: 'threadID', 'requestID' and 'optionID' are required")
	}
	svc, ok := backendService[attentionExtService](d.backend, attentionExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	if err := svc.ConfirmSuggestion(ctx, threadID, requestID, optionID); err != nil {
		return nil, err
	}
	return wire.AttentionExtConfirmSuggestionResult{OK: true}, nil
}

// NotifyAttentionChanged 向所有已认证连接广播 attention.changed，载荷为当前未处理条数。
func (d *Dispatcher) NotifyAttentionChanged(unresolvedCount int) {
	d.NotifyAuthed(attentionExtChangedMethod, wire.AttentionExtChangedNotification{
		UnresolvedCount: unresolvedCount,
	})
}

// attentionExtLimit 归一化条数上限：缺省或为负取默认值，超过上限按上限截断。
func attentionExtLimit(limit int) int {
	if limit <= 0 {
		return attentionExtDefaultLimit
	}
	if limit > attentionExtMaxLimit {
		return attentionExtMaxLimit
	}
	return limit
}
