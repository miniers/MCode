package wire

// PersonasListParams 对应 providers.personas 请求参数。
type PersonasListParams struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// Force 为真表示希望绕过宿主侧缓存重读列表。当前宿主实现忽略该字段：
	// 桌面端 MagpiePersonas 没有强制刷新入口，Magpie 的目录结果自带缓存。
	Force bool `json:"force,omitempty"`
}

// Persona 是某个 provider/model 下可选的人设条目，字段对齐 Magpie 的 MagpiePersona。
type Persona struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// PersonasListResult 对应 providers.personas 成功结果。
type PersonasListResult struct {
	Personas []Persona `json:"personas"`
}

// ThreadsSetPersonaParams 对应 threads.setPersona 请求参数。
// Persona 为空串表示清除会话人设（回到「默认，不使用人设」），其它字段的会话偏好保持原样。
// Provider/Model 是客户端拉人设列表时用的那组 provider/model：弹窗里的模型选择要等下一次
// 发送才写回会话，此时它与会话已保存的值不同，校验跟着列表走才不会把刚看过的人设判成未知。
// Provider 为空时回落到会话自己的 provider/model。
type ThreadsSetPersonaParams struct {
	ThreadID string `json:"threadID"`
	Persona  string `json:"persona"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}
