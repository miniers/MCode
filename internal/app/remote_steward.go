package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"mcode/internal/remote"
	mcodeui "mcode/internal/ui"
)

// 总管（steward）的远程服务：把网页端的按钮动作接到桌面端同一套 Client 方法上，
// 只读的话题列表在 internal/remote/steward_topics.go，这里只做动作转发、错误码映射与变更推送。

// stewardExtClient 是本文件依赖的 Client 能力子集，抽成接口是为了让测试能注入假实现。
type stewardExtClient interface {
	OpenStewardThread(ctx context.Context) (string, error)
	StartStewardTopic(ctx context.Context, projectID, request string) (string, error)
	ReplanStewardTopic(ctx context.Context, threadID string) error
	ConfirmStewardTopic(ctx context.Context, threadID string) error
	CloseStewardTopic(ctx context.Context, threadID string) error
	DeleteStewardTopic(ctx context.Context, threadID string) error
	ReviewTask(ctx context.Context, taskID, decision, feedback string, revision int) (mcodeui.KanbanTask, error)
}

// remoteStewardService 实现 remote 层注入的总管服务接口（steward.open 与话题类方法）。
type remoteStewardService struct {
	client stewardExtClient
}

// errStewardServiceUnavailable 表示远程服务没拿到客户端，动作无法执行。
var errStewardServiceUnavailable = errors.New("总管服务不可用")

// Open 返回总管主会话，对应桌面端「找总管」。
func (s remoteStewardService) Open(ctx context.Context, projectID string) (string, error) {
	if s.client == nil {
		return "", errStewardServiceUnavailable
	}
	return s.client.OpenStewardThread(ctx)
}

// StartTopic 用一句需求开新话题。threadID 是提出需求的会话，远程层已校验它是该项目的话题；
// Client.StartStewardTopic 只按项目与需求开话题，所以这里不再使用它。
func (s remoteStewardService) StartTopic(ctx context.Context, projectID, threadID, request string) (string, error) {
	if s.client == nil {
		return "", errStewardServiceUnavailable
	}
	return s.client.StartStewardTopic(ctx, projectID, request)
}

// ReplanTopic 让规划失败的话题重新发起一轮规划。
func (s remoteStewardService) ReplanTopic(ctx context.Context, threadID string) error {
	if s.client == nil {
		return errStewardServiceUnavailable
	}
	return stewardConflictErr(s.client.ReplanStewardTopic(ctx, threadID))
}

// ConfirmTopic 确认任务书并开工。
func (s remoteStewardService) ConfirmTopic(ctx context.Context, threadID string) error {
	if s.client == nil {
		return errStewardServiceUnavailable
	}
	return stewardConflictErr(s.client.ConfirmStewardTopic(ctx, threadID))
}

// CloseTopic 结项。
func (s remoteStewardService) CloseTopic(ctx context.Context, threadID string) error {
	if s.client == nil {
		return errStewardServiceUnavailable
	}
	return stewardConflictErr(s.client.CloseStewardTopic(ctx, threadID))
}

// DeleteTopic 删除话题（顺带取消目标、中断在跑的任务）。
func (s remoteStewardService) DeleteTopic(ctx context.Context, threadID string) error {
	if s.client == nil {
		return errStewardServiceUnavailable
	}
	return stewardConflictErr(s.client.DeleteStewardTopic(ctx, threadID))
}

// ReviewTask 验收通过或打回话题下的任务；threadID 只用于远程层做话题归属校验，任务本身按 taskID 定位。
func (s remoteStewardService) ReviewTask(ctx context.Context, threadID, taskID, decision, feedback string, revision int) error {
	if s.client == nil {
		return errStewardServiceUnavailable
	}
	if _, err := s.client.ReviewTask(ctx, taskID, decision, feedback, revision); err != nil {
		return stewardConflictErr(err)
	}
	return nil
}

// stewardConflictErr 把桌面端的修订号冲突映射成远程层可识别的哨兵错误，其它错误原样返回；
// 远程层据此回复版本冲突错误码（wire.ErrCodeVersionConflict），web 端可提示刷新后重试。
func stewardConflictErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, mcodeui.ErrRevisionConflict) {
		return fmt.Errorf("%w: %v", remote.ErrStewardRevisionConflict, err)
	}
	return err
}

// stewardChangedDebounce 是 steward.changed 的合并窗口：话题与看板变化经常成串到达，
// 200ms 内的多次变化只推一条通知。
const stewardChangedDebounce = 200 * time.Millisecond

// stewardChangeDebouncer 把短时间内多次变化合并成一次通知。
// trigger 可被任意 goroutine 反复调用，stop 之后不再发出通知。
type stewardChangeDebouncer struct {
	delay  time.Duration
	notify func()

	mu      sync.Mutex
	timer   *time.Timer
	stopped bool
}

// newStewardChangeDebouncer 构造合并器；notify 为 nil 时 trigger 什么也不做。
func newStewardChangeDebouncer(delay time.Duration, notify func()) *stewardChangeDebouncer {
	return &stewardChangeDebouncer{delay: delay, notify: notify}
}

// trigger 重排定时器：距最后一次变化 delay 之后通知一次。
func (d *stewardChangeDebouncer) trigger() {
	if d == nil || d.notify == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return
	}
	if d.timer != nil {
		d.timer.Stop()
	}
	d.timer = time.AfterFunc(d.delay, d.fire)
}

// fire 在定时器到点时发出通知；已被 stop 时什么也不做。
func (d *stewardChangeDebouncer) fire() {
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return
	}
	d.timer = nil
	notify := d.notify
	d.mu.Unlock()
	if notify != nil {
		notify()
	}
}

// stop 取消还没发出的通知，并让后续 trigger 失效。
func (d *stewardChangeDebouncer) stop() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopped = true
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	d.notify = nil
}

func init() {
	registerRemoteWiring(func(a *App) {
		if a == nil || a.backend == nil {
			return
		}
		client := a.remoteClient()
		if client == nil {
			return
		}
		// 服务名与 remote 层的 stewardExtServiceName 一致，远程方法才能取到实现。
		a.backend.SetService("steward", remoteStewardService{client: client})
		if a.remote == nil {
			return
		}
		// 桌面端的话题与看板变化经 Client.WatchStewardTopics 汇到一处，由 RemoteService 合并后推送。
		a.remote.SetStewardWatcher(client.WatchStewardTopics)
	})
}
