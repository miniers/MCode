package mcode

import (
	"context"

	"mcode/internal/agent/session"
	"mcode/internal/harness"
)

// DisplayName 返回 provider 展示名称；未配置时回落到 Kind。
func (h *Harness) DisplayName() string {
	if h.opts.DisplayName != "" {
		return h.opts.DisplayName
	}
	if h.opts.Kind != "" {
		return string(h.opts.Kind)
	}
	return "mcode"
}

// ModelIDs 返回该来源当前可选的模型；未配置回调时返回 nil。
func (h *Harness) ModelIDs() []string {
	if h.opts.ModelIDs == nil {
		return nil
	}
	return h.opts.ModelIDs()
}

// RefreshModels 刷新模型目录；未配置回调时不做任何事。
func (h *Harness) RefreshModels(ctx context.Context) error {
	if h.opts.RefreshModels == nil {
		return nil
	}
	return h.opts.RefreshModels(ctx)
}

// Available 报告该来源是否可用；未配置回调时视为始终可用。
func (h *Harness) Available() (bool, string) {
	if h.opts.Available == nil {
		return true, ""
	}
	return h.opts.Available()
}

// ConfirmsApprovalDelivery 返回 true：引擎在工具执行前同步等待 Approve/Interact 的答复，
// 拿到答复后经 harness.AckDelivery 回报送达结果，因此审批方可以等待送达确认。
func (h *Harness) ConfirmsApprovalDelivery() bool { return true }

// SessionExists 判断某个会话的上下文是否还在。会话目录未配置时无法判断，按仍在处理。
func (h *Harness) SessionExists(sessionID string) bool {
	if sessionID == "" || h.opts.StoreDir == "" {
		return true
	}
	return session.NewStore(h.opts.StoreDir).Exists(sessionID)
}

var _ harness.SessionChecker = (*Harness)(nil)
