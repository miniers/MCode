package app

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
)

const (
	// messageRetentionFirstDelay 是启动后到第一次执行 bee 消息保留任务的等待时长，避开启动高峰。
	messageRetentionFirstDelay = 2 * time.Minute
	// messageRetentionInterval 是两次执行之间的间隔。
	messageRetentionInterval = 24 * time.Hour
)

// messageRetentionScheduler 是 bee 消息保留任务的后台调度器，与 App 解耦以便测试。
// 启动后等待 firstDelay 执行第一次，之后每隔 interval 执行一次；
// 每次执行前现读开关，关闭时只跳过这一轮，不退出循环，用户重新打开后会自动恢复。
type messageRetentionScheduler struct {
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
}

// startMessageRetentionScheduler 启动后台调度 goroutine；run 为 nil 时返回 nil。
func startMessageRetentionScheduler(run func(context.Context) error, enabled func() bool, firstDelay, interval time.Duration) *messageRetentionScheduler {
	if run == nil {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &messageRetentionScheduler{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		timer := time.NewTimer(firstDelay)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			// 同步执行：Stop 必须等正在跑的这轮结束，避免关库时还有事务在跑。
			runMessageRetentionOnce(ctx, run, enabled)
			timer.Reset(interval)
		}
	}()
	return s
}

// runMessageRetentionOnce 执行一轮任务：开关关闭或已取消时跳过。
// ctx 取消引起的错误不记录日志，其余错误记录后继续下一轮。
func runMessageRetentionOnce(ctx context.Context, run func(context.Context) error, enabled func() bool) {
	if ctx.Err() != nil {
		return
	}
	if enabled != nil && !enabled() {
		return
	}
	err := run(ctx)
	if err == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return
	}
	log.Printf("bee 消息保留任务失败: %v", err)
}

// Stop 取消调度并等待 goroutine 退出；s 为 nil 或重复调用都安全。
func (s *messageRetentionScheduler) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		if s.done != nil {
			<-s.done
		}
	})
}
