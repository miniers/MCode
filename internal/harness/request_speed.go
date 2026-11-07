package harness

import "time"

// RequestSpeed 是一次模型请求的输出速度样本：该请求产出的 tokens 与其生成时长（毫秒）。
// 时长从首个流式内容帧算到该请求的用量帧，不含首 token 延迟，也不含工具执行等请求外的时间。
type RequestSpeed struct {
	OutputTokens int64 `json:"output_tokens"`
	DurationMs   int64 `json:"duration_ms"`
}

// RequestClock 为一个模型请求计时，供 provider 采集每个请求的生成时长。
// 零值可用；非并发安全，由持有它的 provider 顺序调用。
type RequestClock struct {
	first time.Time
}

// Begin 记录请求的第一个流式内容帧到达时间；已经记过就保持不变，重复调用只认第一次。
func (c *RequestClock) Begin(now time.Time) {
	if c.first.IsZero() {
		c.first = now
	}
}

// Reset 清零计时，准备下一个请求。
func (c *RequestClock) Reset() {
	c.first = time.Time{}
}

// Finish 在请求的用量帧到达时结算时长：返回该请求的用量与生成时长。
// 无论是否产出样本都会先清零计时。没有 Begin、输出为 0 或时长不足 100 毫秒时返回 false，
// 避免极短请求把速度算成没有意义的数值。
func (c *RequestClock) Finish(now time.Time, outputTokens int64) (RequestSpeed, bool) {
	first := c.first
	c.first = time.Time{}
	if first.IsZero() || outputTokens <= 0 {
		return RequestSpeed{}, false
	}
	elapsed := now.Sub(first)
	if elapsed < 100*time.Millisecond {
		return RequestSpeed{}, false
	}
	return RequestSpeed{OutputTokens: outputTokens, DurationMs: elapsed.Milliseconds()}, true
}
