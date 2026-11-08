package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

// Kind 表示 Agent 后端实现的种类标识。
type Kind string

// PermissionMode 表示运行时的权限模式。
type PermissionMode string

const (
	ModeReadOnly   PermissionMode = "readonly"
	ModeSupervised PermissionMode = "supervised"
	// ModeAutoAcceptEdits 已废弃：仅用于识别旧数据，解析时迁移为 ModeAuto。
	ModeAutoAcceptEdits PermissionMode = "autoAcceptEdits"
	ModeAuto            PermissionMode = "auto"
	ModeFullAccess      PermissionMode = "fullAccess"

	// ModeAgent 是 ModeAutoAcceptEdits 的兼容别名。
	// Deprecated: 请使用 ModeAuto。
	ModeAgent PermissionMode = ModeAutoAcceptEdits
)

// ParseMode 将字符串解析为对应的 PermissionMode。
// 接受 readonly、supervised、auto、fullAccess，以及旧值 autoAcceptEdits、agent 和 full_access。
func ParseMode(s string) (PermissionMode, error) {
	switch s {
	case string(ModeReadOnly):
		return ModeReadOnly, nil
	case string(ModeSupervised):
		return ModeSupervised, nil
	case string(ModeAutoAcceptEdits), "agent", string(ModeAuto):
		return ModeAuto, nil
	case string(ModeFullAccess), "full_access":
		return ModeFullAccess, nil
	case "":
		return "", fmt.Errorf("permission mode cannot be empty; valid modes: readonly, supervised, auto, fullAccess")
	default:
		return "", fmt.Errorf("unknown permission mode %q, valid modes: readonly, supervised, auto, fullAccess", s)
	}
}

// NormalizeMode 将已知权限模式转为规范值，未知值保持不变。
func NormalizeMode(s string) string {
	mode, err := ParseMode(s)
	if err != nil {
		return s
	}
	return string(mode)
}

// NonInteractiveEnv 返回注入给 agent 进程的环境变量：让它在非交互环境下不要因为等
// stdin、打开编辑器或停在分页器而卡住命令执行。
func NonInteractiveEnv() []string {
	return []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_EDITOR=true",
		"GIT_SEQUENCE_EDITOR=true",
		"EDITOR=true",
		"VISUAL=true",
		"GIT_PAGER=cat",
		"PAGER=cat",
		"CI=1",
		"DEBIAN_FRONTEND=noninteractive",
	}
}

// Turn 表示单轮对话交互的上下文及能力注入。
type Turn struct {
	ID       string
	ThreadID string // 所属会话 ID，用于内置 MCP 按会话路由；为空时退回共享端点
	Prompt   string
	WorkDir  string

	// AdditionalDirs 是工作目录之外额外允许访问的绝对目录（总管会话填已注册项目目录），
	// 普通会话为空：provider 把它映射成自己的附加目录参数，进程工作目录仍是 WorkDir。
	AdditionalDirs []string
	// ReadOnlyDirs 是只允许读取、不允许写入的额外目录（如技能目录）。
	ReadOnlyDirs []string

	SessionID string // 非空表示恢复会话
	Model     string
	Mode      PermissionMode
	Approver  Approver
	Sandbox   Sandbox
	Env       []string
	Images    []string // 本地图片绝对路径

	InteractionMode string         // "build" 或 "plan"，空等同 build
	PlanExitMode    PermissionMode // Plan 回合里计划获批后切到的运行模式（用户选的档位）；非 Plan 回合为空
	Effort          string         // low/medium/high，空表示供应商默认
	FastMode        bool           // 快速模式，按各供应商规则映射，false 表示供应商默认
	SpeedTier       string         // 快速模式下的速率档位 ID，空表示由供应商按模型默认快速档位

	// Transient 表示辅助会话（如 Swarm bee）：一般跑完就不再来回对话，驱动没必要为它保留常驻进程。
	Transient bool

	// Attach 表示这一轮不发新的用户帧，只接到该线程现有的常驻进程上，把它在没有轮次挂接时
	// 自发跑出的一段输出（例如后台任务结束后的续跑）读完并入本轮。进程不存在、已失效或没有
	// 待接的输出时，后端应直接返回 nil，不产生任何事件。
	Attach bool
}

// Approver 提供工具调用的审批接口。
type Approver interface {
	Approve(ctx context.Context, req ApprovalRequest) (Decision, error)
}

// RequestKind 表示交互请求的种类，取值与 wire 层 RemoteRequest.Kind 一致。
type RequestKind string

const (
	RequestKindCommand     RequestKind = "command"
	RequestKindFileChange  RequestKind = "fileChange"
	RequestKindTool        RequestKind = "tool"
	RequestKindPlan        RequestKind = "plan"
	RequestKindQuestion    RequestKind = "question"
	RequestKindCommit      RequestKind = "commit"
	RequestKindPermissions RequestKind = "permissions"
)

// QuestionChoice 表示提问的单个候选项。
type QuestionChoice struct {
	Label  string
	Detail string
}

// Question 表示提问类请求中的一道题目。
type Question struct {
	ID             string
	Header         string
	Prompt         string
	Choices        []QuestionChoice
	AllowsMultiple bool
	AllowsOther    bool
	IsSecret       bool
}

// ApprovalRequest 包含待审批工具调用的详情。
type ApprovalRequest struct {
	ToolID   string
	ToolName string
	Input    json.RawMessage
	Summary  string
	// Sensitive 非空表示凭证读取（credential）或控制面写入（controlPlane），只能逐次允许。
	Sensitive string
	// Kind 为空时由审批方按工具名推断。
	Kind      RequestKind
	Questions []Question
	// Plan 为计划类请求的 markdown 正文。
	Plan string
	// Diff 为文件改动的统一 diff 预览，取不到时为空。
	Diff string
}

// ApprovalResponse 是交互请求的完整答复。
type ApprovalResponse struct {
	Decision Decision
	// Answers 键为题目 ID，单选与填空均为单元素数组。
	Answers  map[string][]string
	Skipped  bool
	Feedback string
}

// Interactor 是 Approver 的可选扩展，支持提问与计划类请求的完整答复。
type Interactor interface {
	Interact(ctx context.Context, req ApprovalRequest) (ApprovalResponse, error)
}

// DeliveryAcker 由审批方（Approver）可选实现：Provider 把审批答复真正写入底层 CLI 管道（stdin、JSON-RPC 回复、Hook HTTP 响应等）之后，调用 AckDelivery 回报写入结果，err 为 nil 表示已送达。
type DeliveryAcker interface {
	AckDelivery(req ApprovalRequest, err error)
}

// DeliveryConfirmer 由 Harness 可选实现：返回 true 表示该 Provider 对每次 Approve/Interact 的答复都会调用 DeliveryAcker.AckDelivery，审批方可以据此等待送达确认。
type DeliveryConfirmer interface {
	ConfirmsApprovalDelivery() bool
}

// WorkDirProvider 表示能够提供当前工作目录的审批方或环境。
type WorkDirProvider interface {
	WorkDir() string
}

// AckDelivery 在 approver 实现了 DeliveryAcker 时调用其 AckDelivery 回报写入结果。
func AckDelivery(approver any, req ApprovalRequest, err error) {
	if acker, ok := approver.(DeliveryAcker); ok && acker != nil {
		acker.AckDelivery(req, err)
	}
}

// Sandbox 提供受限命令执行环境，返回清理函数以回收环境资源。
type Sandbox interface {
	Command(ctx context.Context, workDir string, mode PermissionMode, name string, args ...string) (*exec.Cmd, func(), error)
}

// Harness 是各 Agent 后端的统一抽象接口。
type Harness interface {
	Kind() Kind
	Run(ctx context.Context, turn Turn, emit func(Event)) error
}

// Emit 校验事件并在合法时派发给接收函数。
func Emit(emit func(Event), e Event) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if emit != nil {
		emit(e)
	}
	return nil
}

// ErrNotRunning 表示指定会话当前没有正在运行的常驻进程。
var ErrNotRunning = errors.New("harness: session is not running")

// Steerer 是 Harness 的可选扩展接口，支持在轮次执行期间实时注入用户指引。
// 调用方通过类型断言判断当前后端是否支持 steer。
type Steerer interface {
	Steer(ctx context.Context, sessionID string, text string) error
}

// TurnStopper 仅强停指定轮次，不能影响同一 Provider 的其他会话。
type TurnStopper interface {
	ForceStopTurn(turnID string)
}

// ProcessRooter 是 Harness 的可选扩展接口：按会话返回正在运行的 agent 进程 PID，
// 调用方据此定位该会话里正在跑的那条命令的子进程树。
// 未实现或会话没有运行进程时返回空切片。
type ProcessRooter interface {
	ProcessRootPIDs(threadID string) []int
}

// ThreadEvictor 是 Harness 的可选扩展接口：释放指定 thread 持有的空闲常驻进程，
// 供会话或项目被删除时立刻回收内存；正在执行的轮次不受影响。
// 不支持常驻的后端无需实现，调用方通过类型断言探测。
type ThreadEvictor interface {
	EvictThread(threadID string)
}

// HostToolsReporter 是 Harness 的可选扩展接口：报告该后端的会话是否接入了 mcode 的 MCP 工具。
// 接入后才能调用 automation_report_result、kanban_submit_result 等回执工具；
// 不接入的后端（例如 omp）返回 false，上层据此不追问回执。
// 不实现该接口的后端按已接入处理，调用方通过类型断言探测。
type HostToolsReporter interface {
	HostToolsAvailable() bool
}

// UnsolicitedTurnNotifier 是 Harness 的可选扩展接口：线程的常驻进程在没有轮次挂接时自己跑出了
// 一段输出（例如后台任务结束后模型继续处理），后端通过回调把线程 ID 告诉上层，上层据此用
// Turn.Attach 补一轮把这段输出接住。回调可能在任意协程上、进程的锁之外触发，实现方不能持锁调用。
// 不支持常驻的后端无需实现，调用方通过类型断言探测。
type UnsolicitedTurnNotifier interface {
	SetUnsolicitedTurnHandler(fn func(threadID string))
}

// ProcessExitNotifier 是 Harness 的可选扩展：常驻进程退出、之后不会再为该会话产出事件时通知上层。
// before 为零值时，表示该会话已没有存活进程，所有后台任务都随退出的进程中断；
// before 非零时，表示已有替代进程在跑，只有创建时间早于 before 的后台任务属于退出的进程。
type ProcessExitNotifier interface {
	SetProcessExitHandler(fn func(threadID string, before time.Time))
}

// ProcessRoots 按会话记录 agent 进程 PID，供各 Provider 廉价地实现 ProcessRooter。
// 同一会话可同时登记多个 PID（例如常驻进程与兜底的单轮进程并存）。
type ProcessRoots struct {
	mu   sync.Mutex
	pids map[string][]int
}

// Set 登记指定会话的一个 agent 进程 PID；重复登记同一 PID 无副作用。
func (p *ProcessRoots) Set(threadID string, pid int) {
	if p == nil || threadID == "" || pid <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pids == nil {
		p.pids = make(map[string][]int)
	}
	for _, cur := range p.pids[threadID] {
		if cur == pid {
			return
		}
	}
	p.pids[threadID] = append(p.pids[threadID], pid)
}

// Clear 在指定会话的 agent 进程结束后清理记录，只移除该 PID，避免误删同一会话的其他进程；
// pid <= 0 时清空该会话的全部记录。
func (p *ProcessRoots) Clear(threadID string, pid int) {
	if p == nil || threadID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if pid <= 0 {
		delete(p.pids, threadID)
		return
	}
	cur := p.pids[threadID]
	for i, v := range cur {
		if v != pid {
			continue
		}
		if len(cur) == 1 {
			delete(p.pids, threadID)
		} else {
			p.pids[threadID] = append(cur[:i:i], cur[i+1:]...)
		}
		return
	}
}

// PIDs 返回指定会话当前记录的 agent 进程 PID。
func (p *ProcessRoots) PIDs(threadID string) []int {
	if p == nil || threadID == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	cur := p.pids[threadID]
	if len(cur) == 0 {
		return nil
	}
	return append([]int(nil), cur...)
}
