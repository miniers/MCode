package automation

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"time"

	"mcode/internal/filelock"
)

// SchedulerLockState 描述当前进程在跨进程调度锁中的角色。
type SchedulerLockState string

const (
	// SchedulerLockActive 表示本进程持有排他锁，负责 Tick 触发。
	SchedulerLockActive SchedulerLockState = "active"
	// SchedulerLockPassiveHeld 表示锁被其他进程持有，本进程不触发任何任务。
	SchedulerLockPassiveHeld SchedulerLockState = "passive_held"
	// SchedulerLockPassiveError 表示锁不可用，本进程不触发任何任务。
	SchedulerLockPassiveError SchedulerLockState = "passive_error"
)

// SchedulerLockStatus 是调度锁状态的只读快照；Error 仅在 passive_error 时非空。
type SchedulerLockStatus struct {
	State SchedulerLockState
	Error string
}

// SchedulerLockInfo 是写入锁文件的诊断信息，仅用于排障，不参与任何加锁判定。
type SchedulerLockInfo struct {
	PID   int       `json:"pid"`
	Host  string    `json:"host"`
	Role  string    `json:"role"`
	Since time.Time `json:"since"`
}

// LockHandle 是调度锁的最小句柄；生产实现为 *filelock.Lock。
type LockHandle interface {
	Close() error
	WriteInfo(v any) error
}

// LockAcquirer 允许测试注入假的非阻塞加锁实现；生产默认使用 filelock.TryAcquire。
type LockAcquirer func(path string) (LockHandle, error)

var (
	// ErrSchedulerPassive 表示本进程未持有调度锁，不能触发或手动运行任务。
	ErrSchedulerPassive = errors.New("another mcode instance is scheduling automations")
	// ErrSchedulerNoDataDir 表示未配置数据目录，无法定位锁文件。
	ErrSchedulerNoDataDir = errors.New("automation: data directory is not configured")
	// ErrSchedulerNotStarted 是调度器尚未取锁时的初始状态文本。
	ErrSchedulerNotStarted = errors.New("automation: scheduler not started")
	// ErrSchedulerStopped 是调度器停止后的状态文本。
	ErrSchedulerStopped = errors.New("automation: scheduler stopped")
)

// lockLogThrottle 是同一错误文本重复记日志的最小间隔，避免网络盘上每 10 秒一条日志。
const lockLogThrottle = 10 * time.Minute

// initSchedulerLock 初始化调度锁的路径、加锁实现与初始状态。
func (s *Service) initSchedulerLock(opts Options) {
	s.acquireLock = opts.AcquireLock
	if s.acquireLock == nil {
		s.acquireLock = func(path string) (LockHandle, error) {
			return filelock.TryAcquire(path)
		}
	}
	s.logf = opts.Logf
	if s.logf == nil {
		s.logf = log.Printf
	}
	if opts.DataDir == "" {
		// 未配置数据目录时只用于测试注入与防御性降级：不尝试加锁，也绝不触发任务。
		s.lockPath = ""
		s.lockStatus = SchedulerLockStatus{
			State: SchedulerLockPassiveError,
			Error: ErrSchedulerNoDataDir.Error(),
		}
		return
	}
	s.lockPath = filepath.Join(opts.DataDir, "automation", "scheduler.lock")
	s.lockStatus = SchedulerLockStatus{
		State: SchedulerLockPassiveError,
		Error: ErrSchedulerNotStarted.Error(),
	}
}

// SchedulerLockState 返回调度锁状态快照，供 UI 与测试读取。
func (s *Service) SchedulerLockState() SchedulerLockStatus {
	s.lockMu.Lock()
	defer s.lockMu.Unlock()
	return s.lockStatus
}

// ensureSchedulerLock 在未持锁时尝试非阻塞获取调度锁。
// 返回 false 表示本进程当前为 passive，调用方不得触发任何任务。
func (s *Service) ensureSchedulerLock() bool {
	s.lockMu.Lock()
	defer s.lockMu.Unlock()

	if s.lock != nil {
		return true
	}
	if s.lockPath == "" {
		err := ErrSchedulerNoDataDir
		s.setLockStateLocked(SchedulerLockStatus{State: SchedulerLockPassiveError, Error: err.Error()})
		s.logLockFailureLocked(err)
		return false
	}
	if err := os.MkdirAll(filepath.Dir(s.lockPath), 0o700); err != nil {
		s.setLockStateLocked(SchedulerLockStatus{State: SchedulerLockPassiveError, Error: err.Error()})
		s.logLockFailureLocked(err)
		return false
	}

	l, err := s.acquireLock(s.lockPath)
	if err == nil && l != nil {
		s.lock = l
		s.setLockStateLocked(SchedulerLockStatus{State: SchedulerLockActive})
		s.resetLockLogLocked()
		host, _ := os.Hostname()
		// 诊断信息 best-effort：写失败不影响已持有的锁。
		_ = l.WriteInfo(SchedulerLockInfo{
			PID:   os.Getpid(),
			Host:  host,
			Role:  string(SchedulerLockActive),
			Since: s.now(),
		})
		return true
	}
	if err == nil {
		// 加锁实现返回 (nil, nil) 属于实现错误，绝不能当成拿到锁。
		err = errors.New("filelock: nil lock without error")
	}

	s.lock = nil
	if errors.Is(err, filelock.ErrBusy) {
		s.setLockStateLocked(SchedulerLockStatus{State: SchedulerLockPassiveHeld})
	} else {
		s.setLockStateLocked(SchedulerLockStatus{State: SchedulerLockPassiveError, Error: err.Error()})
	}
	s.logLockFailureLocked(err)
	return false
}

// releaseSchedulerLock 关闭锁描述符（不删除锁文件），并把状态置为停止。
// Stop 无论是否启动过调度协程都会调用，避免只调用过 Tick/RunNow 的实例泄漏描述符。
func (s *Service) releaseSchedulerLock() {
	s.lockMu.Lock()
	defer s.lockMu.Unlock()

	if s.lock != nil {
		_ = s.lock.Close()
		s.lock = nil
	}
	s.setLockStateLocked(SchedulerLockStatus{
		State: SchedulerLockPassiveError,
		Error: ErrSchedulerStopped.Error(),
	})
}

// setLockStateLocked 更新状态快照；调用方必须持有 lockMu。
func (s *Service) setLockStateLocked(status SchedulerLockStatus) {
	s.lockStatus = status
}

// logLockFailureLocked 在首次失败或错误文本变化时立即记一条 warn；
// 同一文本此后每 lockLogThrottle 最多一条。调用方必须持有 lockMu。
func (s *Service) logLockFailureLocked(err error) {
	if err == nil {
		return
	}
	msg := err.Error()
	now := s.now()
	if s.lastLockLogErr == msg && !s.lastLockLogAt.IsZero() && now.Sub(s.lastLockLogAt) < lockLogThrottle {
		return
	}
	s.lastLockLogErr = msg
	s.lastLockLogAt = now
	s.logf("automation: scheduler lock unavailable: %s", msg)
}

// resetLockLogLocked 在成功取锁后清空日志限流状态。调用方必须持有 lockMu。
func (s *Service) resetLockLogLocked() {
	s.lastLockLogErr = ""
	s.lastLockLogAt = time.Time{}
}
