package wire

// 本文件放扩展方法（internal/remote 下的 *_methods.go）专用的参数字与结果类型。
// 类型名统一加 SwarmExt / AlwaysAllowed 前缀；其中 retryBee 已并入 docs/remote-control.schema.json，
// 其余方法待 schema 补齐后再合并进 types.go，并保持字段名与 JSON 键一致。

// SwarmExtThreadParams 是只带 threadID 的编排扩展方法参数（中断查询、续跑、放弃续跑共用）。
type SwarmExtThreadParams struct {
	ThreadID string `json:"threadID"`
}

// SwarmExtInterruptedBee 描述一条因 MCode 重启被打断、等待用户决定的条目。
type SwarmExtInterruptedBee struct {
	ThreadID string `json:"threadID"`
	Name     string `json:"name"`
	Task     string `json:"task"`
}

// SwarmExtInterruptedResult 对应 threads.swarm.interrupted 成功结果。
// Count 与桌面端 SwarmState.Interrupted 同源：被打断的 bee 数加重启前未启动的排队条目数，
// 因此可能大于 Bees 的长度（排队条目没有对应的 bee 线程）。
type SwarmExtInterruptedResult struct {
	Count int                      `json:"count"`
	Bees  []SwarmExtInterruptedBee `json:"bees,omitempty"`
}

// SwarmExtResumeResult 对应 threads.swarm.resume 成功结果，Resumed 为重新派发或重新入队的条目数。
type SwarmExtResumeResult struct {
	Resumed int `json:"resumed"`
}

// SwarmExtDismissResult 对应 threads.swarm.dismissInterrupted 成功结果，Dismissed 为清掉的中断标记条目数。
type SwarmExtDismissResult struct {
	Dismissed int `json:"dismissed"`
}

// SwarmExtRetryBeeParams 对应 threads.swarm.retryBee 请求参数。
// ThreadID 既可以是主会话，也可以是 bee 自己的会话（实现侧按 BeeOwner 归一成主会话）。
type SwarmExtRetryBeeParams struct {
	ThreadID    string `json:"threadID"`
	BeeThreadID string `json:"beeThreadID"`
}

// SwarmExtRetryBeeResult 对应 threads.swarm.retryBee 成功结果，OK 固定为 true。
type SwarmExtRetryBeeResult struct {
	OK bool `json:"ok"`
}

// SwarmExtReportParams 对应 threads.swarm.report 请求参数：threadID 是主会话（也可传 bee 自己的会话，
// 实现侧按 BeeOwner 归一），beeID 是被截断汇报所属的 bee 线程 ID。
type SwarmExtReportParams struct {
	ThreadID string `json:"threadID"`
	BeeID    string `json:"beeID"`
}

// SwarmExtReportResult 对应 threads.swarm.report 成功结果：Text 是存档里的完整汇报，
// Name 是 bee 名字（存档读得到时非空），Truncated 读存档后恒为 false。
type SwarmExtReportResult struct {
	BeeID     string `json:"beeID"`
	Name      string `json:"name,omitempty"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

// AlwaysAllowedListParams 对应 requests.alwaysAllowed.list 请求参数；ProjectID 取 projects.list 里的 id。
type AlwaysAllowedListParams struct {
	ProjectID string `json:"projectID"`
}

// AlwaysAllowedListResult 对应 requests.alwaysAllowed.list 成功结果，Keys 为该项目下已持久化的记账键。
type AlwaysAllowedListResult struct {
	Keys []string `json:"keys"`
}

// AlwaysAllowedRevokeParams 对应 requests.alwaysAllowed.revoke 请求参数；Key 取列表里的原值。
type AlwaysAllowedRevokeParams struct {
	ProjectID string `json:"projectID"`
	Key       string `json:"key"`
}

// AlwaysAllowedRevokeResult 对应 requests.alwaysAllowed.revoke 成功结果，Keys 为撤销后剩余的记账键。
type AlwaysAllowedRevokeResult struct {
	Keys []string `json:"keys"`
}
