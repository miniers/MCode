package app

import (
	"strings"

	"mcode/internal/config"
)

// piTuningEnv 把 pi 的两个专有设置转换成 pi 子进程的环境变量，供 pi.New 的 Options.Env 使用：
//   - PiCacheLong 开启时给 PI_CACHE_RETENTION=long，让支持的服务商使用更长的 prompt 缓存；
//   - PiVisionModel 去掉首尾空白后非空时给 MCODE_PI_VISION_MODEL=<provider/model>，
//     当前模型看不了图片时由该模型描述图片。
//
// 两项都没设置时返回 nil，调用方可以直接把 nil 拼进已有的环境变量列表。
func piTuningEnv(p config.ProviderConfig) []string {
	var env []string
	if p.PiCacheLong {
		env = append(env, "PI_CACHE_RETENTION=long")
	}
	if model := strings.TrimSpace(p.PiVisionModel); model != "" {
		env = append(env, "MCODE_PI_VISION_MODEL="+model)
	}
	return env
}
