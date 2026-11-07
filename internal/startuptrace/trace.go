// Package startuptrace 记录启动各阶段的耗时。
//
// 默认不写任何日志。需要调试启动耗时的时候，设置环境变量 MCODE_STARTUP_TRACE=1 开启：
//
//	open --env MCODE_STARTUP_TRACE=1 -a MCode
//
// 或者直接运行应用包里的可执行文件：
//
//	MCODE_STARTUP_TRACE=1 /Applications/MCode.app/Contents/MacOS/MCode
//
// 从 Finder 启动的 .app 拿不到 shell 里的环境变量，所以要按上面的方式启动。
// 日志写在数据目录下的 logs/mcode.log（默认 ~/.mcode/logs/mcode.log），搜索 startup: 即可。
package startuptrace

import (
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

type point struct {
	name    string
	totalMs int64
	deltaMs int64
}

var (
	enabled    bool
	mu         sync.Mutex
	flushed    bool
	seen       = make(map[string]bool)
	points     []point
	startTime  time.Time
	lastMarkAt time.Time
)

func init() {
	env := strings.TrimSpace(os.Getenv("MCODE_STARTUP_TRACE"))
	enabled = env != "" && env != "0"
	if !enabled {
		return
	}

	startTime = processStartTime()
	if startTime.IsZero() {
		startTime = time.Now()
	}
	lastMarkAt = startTime
}

// resetForTesting 重置内部状态，仅供单元测试使用。
func resetForTesting(customStart time.Time) {
	mu.Lock()
	defer mu.Unlock()

	enabled = true
	flushed = false
	seen = make(map[string]bool)
	points = nil
	if customStart.IsZero() {
		startTime = time.Now()
	} else {
		startTime = customStart
	}
	lastMarkAt = startTime
}

// Mark 记录一个计时点。同名只记第一次，后面再调用直接忽略。
// 每个点记录距进程启动的毫秒数，以及距上一个计时点的毫秒数。
// 未设置 MCODE_STARTUP_TRACE=1 时直接返回，不做任何记录。
func Mark(name string) {
	if !enabled {
		return
	}

	mu.Lock()
	defer mu.Unlock()

	if seen[name] {
		return
	}
	seen[name] = true

	now := time.Now()
	totalMs := now.Sub(startTime).Milliseconds()
	if totalMs < 0 {
		totalMs = 0
	}
	deltaMs := now.Sub(lastMarkAt).Milliseconds()
	if deltaMs < 0 {
		deltaMs = 0
	}
	lastMarkAt = now

	pt := point{
		name:    name,
		totalMs: totalMs,
		deltaMs: deltaMs,
	}

	if flushed {
		log.Printf("startup: %s +%dms (Δ%dms)", pt.name, pt.totalMs, pt.deltaMs)
	} else {
		points = append(points, pt)
	}
}

// Flush 从这一刻起使 Mark 直接写 log.Printf。
// 调用前积累的计时点在 Flush 时按顺序一次性补写。在 Flush 之前，Mark 只存在内存里。
// 未设置 MCODE_STARTUP_TRACE=1 时直接返回。
func Flush() {
	if !enabled {
		return
	}

	mu.Lock()
	defer mu.Unlock()

	if flushed {
		return
	}
	flushed = true

	for _, pt := range points {
		log.Printf("startup: %s +%dms (Δ%dms)", pt.name, pt.totalMs, pt.deltaMs)
	}
	points = nil
}
