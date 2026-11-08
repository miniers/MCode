package remote

import (
	"context"
	"encoding/json"
	"strings"

	"mcode/internal/agent/gate"
	"mcode/internal/harness"
)

// hardLockApprover 给看板任务执行会话的审批方加一层危险命令硬锁。
// 命中时直接拒绝并在线程里留一条系统消息；其余请求原样交给内层审批方。
type hardLockApprover struct {
	inner      harness.Approver
	b          *MemoryBackend
	threadID   string
	hardLocked bool
	override   string
}

// Approve 实现 harness.Approver。
func (a *hardLockApprover) Approve(ctx context.Context, req harness.ApprovalRequest) (harness.Decision, error) {
	if reason, denied := a.hardLockDenial(req); denied {
		a.notice(reason)
		return harness.DecisionDeny, nil
	}
	return a.inner.Approve(ctx, req)
}

// Interact 实现 harness.Interactor，并在内层不支持交互时退回 Approve。
func (a *hardLockApprover) Interact(ctx context.Context, req harness.ApprovalRequest) (harness.ApprovalResponse, error) {
	if reason, denied := a.hardLockDenial(req); denied {
		a.notice(reason)
		return harness.ApprovalResponse{Decision: harness.DecisionDeny}, nil
	}
	if interactor, ok := a.inner.(harness.Interactor); ok && interactor != nil {
		return interactor.Interact(ctx, req)
	}
	decision, err := a.inner.Approve(ctx, req)
	return harness.ApprovalResponse{Decision: decision}, err
}

// WorkDir 转发内层的工作目录信息。
func (a *hardLockApprover) WorkDir() string {
	if provider, ok := a.inner.(harness.WorkDirProvider); ok && provider != nil {
		return provider.WorkDir()
	}
	return ""
}

// AckDelivery 转发内层的送达确认。
func (a *hardLockApprover) AckDelivery(req harness.ApprovalRequest, err error) {
	if acker, ok := a.inner.(harness.DeliveryAcker); ok && acker != nil {
		acker.AckDelivery(req, err)
	}
}

// MCPToolAllowed 转发内层对内置 MCP 工具的放行判断。
func (a *hardLockApprover) MCPToolAllowed(ctx context.Context, toolName string) bool {
	if allower, ok := a.inner.(harness.MCPToolAllower); ok && allower != nil {
		return allower.MCPToolAllowed(ctx, toolName)
	}
	return false
}

// hardLockDenial 判断请求是否命中硬锁；override 非空时本次运行放行。
func (a *hardLockApprover) hardLockDenial(req harness.ApprovalRequest) (string, bool) {
	if !a.hardLocked || strings.TrimSpace(a.override) != "" {
		return "", false
	}
	kind := req.Kind
	if kind == "" {
		kind = inferRequestKind(req.ToolName)
	}
	if kind != harness.RequestKindCommand {
		return "", false
	}
	lock, hit := gate.CheckHardLock(approvalCommand(req), a.override)
	if !hit {
		return "", false
	}
	return "危险操作已锁定，需要用户答决策卡后解锁：" + lock.Reason, true
}

// notice 把拒绝理由追加到线程时间线。
func (a *hardLockApprover) notice(reason string) {
	if a.b == nil || a.threadID == "" {
		return
	}
	_ = a.b.appendSystemMessage(a.threadID, "info", reason)
}

// taskGateOverride 只解析任务 meta 里的 gate_override，避免依赖 internal/kanban 的完整结构。
func taskGateOverride(meta string) string {
	var parsed struct {
		GateOverride string `json:"gate_override"`
	}
	if err := json.Unmarshal([]byte(meta), &parsed); err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.GateOverride)
}
