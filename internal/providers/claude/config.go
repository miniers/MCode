package claude

import (
	"log"
	"time"

	"mcode/internal/config"
)

const (
	// residentMaxTurns 与 residentMaxAge 是应用层给常驻进程的长寿上限：CLI 进程内存随会话增长，
	// 服务满这么多轮或这么久后下一轮用 --resume 换一个新进程。
	residentMaxTurns = 200
	residentMaxAge   = 6 * time.Hour
)

// WithResident 按用户配置补全常驻进程相关选项：空闲回收时长与驻留上限来自配置
// （providers.claude.residentIdleMinutes / residentMax），轮数与存活时间上限用应用默认值，
// 并把常驻进程的关键事件写进日志。
func WithResident(opts Options, cfg config.ProviderConfig) Options {
	opts.IdleTTL = cfg.ResidentIdleTTL()
	opts.MaxResident = cfg.ResidentMax
	opts.MaxTurns = residentMaxTurns
	opts.MaxAge = residentMaxAge
	opts.Logf = log.Printf
	return opts
}
