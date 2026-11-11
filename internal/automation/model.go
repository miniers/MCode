package automation

import (
	"encoding/json"
	"errors"

	"mcode/internal/harness"
	"mcode/internal/store"
)

// 自动化状态常量
const (
	StatusActive    = "active"
	StatusPaused    = "paused"
	StatusFailed    = "failed"
	StatusCompleted = "completed"
)

// 运行记录状态常量
const (
	RunRunning   = "running"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
	RunStopped   = "stopped"
	// RunSkipped 表示运行没有真正启动：休眠错过 once 触发点且关闭补跑时记账用。
	RunSkipped = "skipped"
)

// 运行来源常量，对应 automation_runs.trigger 列。
const (
	// RunTriggerScheduled 表示按计划正常触发。
	RunTriggerScheduled = "scheduled"
	// RunTriggerCatchUp 表示休眠或进程停摆后补跑。
	RunTriggerCatchUp = "catch_up"
	// RunTriggerManual 表示用户通过 RunNow 手动触发。
	RunTriggerManual = "manual"
)

// 运行模式常量。
const (
	// RunModeStandalone 每次运行新建一个会话，最独立也最消耗资源，为默认模式。
	RunModeStandalone = "standalone"
	// RunModeDedicated 复用该自动化专属的同一个会话，首次运行时创建并固化。
	RunModeDedicated = "dedicated"
	// RunModeHeartbeat 把每轮运行投递回创建自动化时的来源会话。
	RunModeHeartbeat = "heartbeat"
)

var (
	// ErrAlreadyRunning 表示自动化任务已有运行正在执行中。
	ErrAlreadyRunning = errors.New("automation: run already in flight")
	// ErrInvalidInput 表示参数校验失败或非法输入。
	ErrInvalidInput = errors.New("automation: invalid input")
	// ErrRunNotFound 表示运行记录不存在或不属于指定自动化。
	ErrRunNotFound = errors.New("automation: run not found")
	// ErrRunAlreadySettled 表示运行记录已被结算。
	ErrRunAlreadySettled = errors.New("automation: run already settled")
	// ErrRunNotOwned 表示调用方线程不是该运行记录的所属线程。
	ErrRunNotOwned = errors.New("automation: run belongs to another thread")
	// ErrMaxRunsReached 表示自动化已达到运行次数上限，不再启动新的运行。
	ErrMaxRunsReached = errors.New("automation reached maximum run count")
)

// RunState 表示自动化任务运行时的累积状态。
type RunState struct {
	TotalRunCount       int               `json:"totalRunCount,omitempty"`
	ConsecutiveFailures int               `json:"consecutiveFailures,omitempty"`
	LastRunStatus       string            `json:"lastRunStatus,omitempty"`
	StopReason          string            `json:"stopReason,omitempty"`
	LastEvaluation      *EvaluationResult `json:"lastEvaluation,omitempty"`
}

// Spec 描述自动化的调度与执行策略，序列化后保存在 store.Automation.Schedule 列中。
type Spec struct {
	Schedule Schedule `json:"schedule"`
	StopWhen string   `json:"stopWhen,omitempty"`
	// Check 是定时触发前在项目目录执行的只读检查命令，退出码 0 才运行，空表示不检查。
	Check                  string  `json:"check,omitempty"`
	ConfidenceThreshold    float64 `json:"confidenceThreshold"`
	MaxConsecutiveFailures int     `json:"maxConsecutiveFailures"`
	// MaxIterations 已由 MaxRuns 取代，仅为旧数据与旧版本二进制的兼容镜像保留读取。
	// Deprecated: 使用 MaxRuns。
	MaxIterations int `json:"maxIterations,omitempty"`
	// MaxRuns 是运行次数上限，达到后自动停用；0 表示不限制。
	MaxRuns int `json:"maxRuns,omitempty"`
	// CatchUp 决定休眠错过触发点后是否补跑；nil 等价于 true，兼容没有该字段的旧数据。
	CatchUp *bool `json:"catchUp,omitempty"`
	// RunMode 为运行模式；空值按 standalone 处理，兼容没有该字段的旧数据。
	RunMode string `json:"runMode,omitempty"`
	// OriginThreadID 为 heartbeat 模式创建自动化时的来源线程，每轮运行投递回该线程。
	OriginThreadID string `json:"originThreadId,omitempty"`
	// CreatorThreadID 记录是哪个会话建的任务，用于每会话任务数上限；UI 手动创建时为空。
	CreatorThreadID string `json:"creatorThreadId,omitempty"`
	// DedicatedThreadID 为 dedicated 模式固化下来的专属线程，首次运行时写入。
	DedicatedThreadID string `json:"dedicatedThreadId,omitempty"`
	// Provider/Model/Effort 非空时覆盖本次运行新建会话的模型与推理强度；
	// 只对 standalone（每次新建会话）与 dedicated（首次新建专属会话）生效，
	// heartbeat 模式把每轮 prompt 投递回已有会话，这三项对它不生效。
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
	// RuntimeMode 非空时以该权限模式创建会话，取值同 ThreadCreateParams.RuntimeMode
	// （readonly/supervised/auto/fullAccess），空串表示跟随默认；
	// 与模型字段一样，heartbeat 模式不生效。
	RuntimeMode string   `json:"runtimeMode,omitempty"`
	State       RunState `json:"state"`
}

// NormalizedRunMode 返回归一化后的运行模式：空值与未知值都回退到 standalone。
func (s Spec) NormalizedRunMode() string {
	switch s.RunMode {
	case RunModeDedicated, RunModeHeartbeat:
		return s.RunMode
	default:
		return RunModeStandalone
	}
}

// EffectiveMaxRuns 返回生效的运行次数上限：MaxRuns 优先，缺失时回退到旧字段 MaxIterations。
func (s Spec) EffectiveMaxRuns() int {
	if s.MaxRuns > 0 {
		return s.MaxRuns
	}
	return s.MaxIterations
}

// CatchUpEnabled 返回是否允许补跑：未设置时按开启处理，兼容旧数据。
func (s Spec) CatchUpEnabled() bool {
	if s.CatchUp == nil {
		return true
	}
	return *s.CatchUp
}

// Definition 是带解析后 Spec 的自动化配置聚合体。
type Definition struct {
	store.Automation
	Spec Spec `json:"spec"`
}

// EncodeSpec 将 Spec 序列化为 JSON 字符串。
func EncodeSpec(s Spec) (string, error) {
	s.RuntimeMode = harness.NormalizeMode(s.RuntimeMode)
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// DecodeSpec 解析 JSON 字符串为 Spec。解析失败或未知调度类型时回退到默认的 3600 秒 interval 调度。
func DecodeSpec(raw string) Spec {
	var s Spec
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return Spec{
			Schedule:               Schedule{Type: "interval", Seconds: 3600},
			ConfidenceThreshold:    0.8,
			MaxConsecutiveFailures: 3,
			RunMode:                RunModeStandalone,
		}
	}
	s.RuntimeMode = harness.NormalizeMode(s.RuntimeMode)
	s.RunMode = s.NormalizedRunMode()
	switch s.Schedule.Type {
	case "interval", "once", "daily", "weekdays", "weekly", "cron":
	default:
		s.Schedule = Schedule{Type: "interval", Seconds: 3600}
	}
	if s.ConfidenceThreshold == 0 {
		s.ConfidenceThreshold = 0.8
	}
	if s.MaxConsecutiveFailures == 0 {
		s.MaxConsecutiveFailures = 3
	}
	return s
}
