package shell

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"

	"mcode/internal/harness"
)

const (
	// maxBackgroundJobs 是同时运行的后台任务上限。
	maxBackgroundJobs = 16
	// maxBackgroundOutput 是每个任务保留的输出上限（保留最后 1 MiB）。
	maxBackgroundOutput = 1024 * 1024
	// droppedNotice 标注超过上限被丢弃的早期输出。
	droppedNotice = "[earlier output dropped]\n"
)

// backgroundJob 保存后台任务的进程、输出和运行状态。
type backgroundJob struct {
	cmd   *exec.Cmd
	owner string
	// done 接收 cmd.Wait 的结果，供 stopProcess 复用。
	done chan error
	// killMu 保证同一任务不会并发终止。
	killMu sync.Mutex

	mu       sync.Mutex
	buf      []byte
	read     int64 // 已读取到的绝对偏移
	dropped  int64 // 已从缓冲区头部丢弃的字节数
	running  bool
	exitCode int
	finishedAt time.Time
	timedOut bool
	// reapable 在任务退出且 output 已读完全部剩余输出后置位。
	reapable bool
}

// Write 追加输出，只保留最后 maxBackgroundOutput 字节。
func (j *backgroundJob) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.buf = append(j.buf, p...)
	if excess := len(j.buf) - maxBackgroundOutput; excess > 0 {
		j.buf = j.buf[excess:]
		j.dropped += int64(excess)
	}
	return len(p), nil
}

// output 返回上次读取之后的新输出及当前状态。
func (j *backgroundJob) output() (string, bool, int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	start := j.read - j.dropped
	truncated := false
	if start < 0 {
		start = 0
		truncated = true
	}
	if start > int64(len(j.buf)) {
		start = int64(len(j.buf))
	}
	text := string(j.buf[start:])
	j.read = j.dropped + int64(len(j.buf))
	if !j.running && j.read == j.dropped+int64(len(j.buf)) {
		j.reapable = true
	}
	if truncated {
		text = droppedNotice + text
	}
	return text, j.running, j.exitCode
}

func (j *backgroundJob) isRunning() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.running
}

// isReapable 报告任务已退出且输出已读完，可以从任务表中移除。
func (j *backgroundJob) isReapable() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.reapable
}

var (
	backgroundMu   sync.Mutex
	backgroundJobs = map[string]*backgroundJob{}
	backgroundSeq  int
	backgroundMaxAge = 10 * time.Minute
)

// backgroundCount 返回正在运行的任务数；调用方持有 backgroundMu。
func backgroundCount() int {
	n := 0
	for id, job := range backgroundJobs {
		job.mu.Lock()
		expired := !job.running && !job.finishedAt.IsZero() && time.Since(job.finishedAt) >= backgroundMaxAge
		job.mu.Unlock()
		if expired {
			delete(backgroundJobs, id)
			continue
		}
		if job.isRunning() {
			n++
		}
	}
	return n
}

// Start 在后台启动命令，返回任务 ID 和进程号。
// 任务不绑定 ctx 取消：本轮结束后进程继续运行，可用 Kill 停止。
func Start(ctx context.Context, opts Options) (string, int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	name, args := shellCommand(opts.Command)
	var cmd *exec.Cmd
	var cleanup func()
	if opts.Sandbox != nil {
		var err error
		cmd, cleanup, err = opts.Sandbox.Command(context.WithoutCancel(ctx), opts.WorkDir, opts.Mode, name, args...)
		if err != nil {
			return "", 0, err
		}
		if cmd == nil {
			return "", 0, errors.New("sandbox returned a nil command")
		}
	} else {
		cmd = exec.Command(name, args...)
		cmd.Dir = opts.WorkDir
	}
	// 命令退出后，若脱离进程组的子进程仍占用管道，最多再等待 3 秒。
	cmd.WaitDelay = 3 * time.Second
	cmd.Env = append(append(os.Environ(), opts.Env...), harness.NonInteractiveEnv()...)
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		if cleanup != nil {
			cleanup()
		}
		return "", 0, err
	}
	cmd.Stdin = stdin
	job := &backgroundJob{cmd: cmd, owner: OwnerFrom(ctx), done: make(chan error, 1), exitCode: -1, running: true}
	cmd.Stdout, cmd.Stderr = job, job
	setProcessGroup(cmd)

	backgroundMu.Lock()
	for id, job := range backgroundJobs {
		if job.isReapable() {
			delete(backgroundJobs, id)
		}
	}
	if backgroundCount() >= maxBackgroundJobs {
		backgroundMu.Unlock()
		_ = stdin.Close()
		if cleanup != nil {
			cleanup()
		}
		return "", 0, fmt.Errorf("too many background jobs (max %d); use kill tool to stop old jobs", maxBackgroundJobs)
	}
	if err := cmd.Start(); err != nil {
		backgroundMu.Unlock()
		_ = stdin.Close()
		if cleanup != nil {
			cleanup()
		}
		return "", 0, err
	}
	_ = stdin.Close()
	backgroundSeq++
	id := fmt.Sprintf("bg%d", backgroundSeq)
	pid := cmd.Process.Pid
	backgroundJobs[id] = job
	backgroundMu.Unlock()

	if tracker := trackerFrom(ctx); tracker != nil && tracker.onStart != nil {
		tracker.onStart(pid)
	}
	finished := make(chan struct{})
	go func() {
		var err error
		exitCode := -1
		defer func() {
			if v := recover(); v != nil {
				err = fmt.Errorf("后台任务 panic: %v", v)
				log.Print(err)
				_, _ = job.Write([]byte(err.Error() + "\n"))
			}
			if tracker := trackerFrom(ctx); tracker != nil && tracker.onExit != nil {
				if callbackErr := callbackError(func() { tracker.onExit(pid) }); callbackErr != nil {
					log.Print(callbackErr)
					_, _ = job.Write([]byte(callbackErr.Error() + "\n"))
					exitCode = -1
				}
			}
			if cleanup != nil {
				if callbackErr := callbackError(cleanup); callbackErr != nil {
					log.Print(callbackErr)
					_, _ = job.Write([]byte(callbackErr.Error() + "\n"))
					exitCode = -1
				}
			}
			job.mu.Lock()
			job.running = false
			job.exitCode = exitCode
			job.finishedAt = time.Now()
			job.mu.Unlock()
			job.done <- err
			close(finished)
		}()
		err = waitCommand(cmd)
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
	}()
	if opts.Timeout > 0 {
		go func() {
			timer := time.NewTimer(opts.Timeout)
			defer timer.Stop()
			select {
			case <-finished:
				return
			case <-timer.C:
			}
			job.killMu.Lock()
			defer job.killMu.Unlock()
			job.mu.Lock()
			if !job.running {
				job.mu.Unlock()
				return
			}
			job.timedOut = true
			job.mu.Unlock()
			_, _ = job.Write([]byte("后台任务超时\n"))
			if err := stopProcess(cmd, job.done); err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					log.Print(err)
				}
			}
		}()
	}
	return id, pid, nil
}

// Output 返回任务自上次读取之后的新输出、是否仍在运行以及退出码。
func Output(id string) (string, bool, int, error) {
	return output(id, "", false)
}

// OutputOwner 只读取指定拥有者的任务。
func OutputOwner(owner, id string) (string, bool, int, error) {
	return output(id, owner, true)
}

func output(id, owner string, checkOwner bool) (string, bool, int, error) {
	backgroundMu.Lock()
	job := backgroundJobs[id]
	backgroundMu.Unlock()
	if job == nil || checkOwner && job.owner != owner {
		return "", false, -1, fmt.Errorf("unknown background job %q", id)
	}
	text, running, exitCode := job.output()
	return text, running, exitCode, nil
}

// Kill 终止任务所在的进程组：先 SIGTERM，2 秒后 SIGKILL。
func Kill(id string) error {
	return kill(id, "", false)
}

// KillOwned 只停止指定拥有者的任务。
func KillOwned(owner, id string) error {
	return kill(id, owner, true)
}

func kill(id, owner string, checkOwner bool) error {
	backgroundMu.Lock()
	job := backgroundJobs[id]
	backgroundMu.Unlock()
	if job == nil || checkOwner && job.owner != owner {
		return fmt.Errorf("unknown background job %q", id)
	}
	job.killMu.Lock()
	defer job.killMu.Unlock()
	if !job.isRunning() {
		return nil
	}
	err := stopProcess(job.cmd, job.done)
	// 被信号终止的进程会以 ExitError 结束，这只表示终止成功。
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return err
	}
	return nil
}

// KillOwner 停止拥有者的全部后台任务；空拥有者不触发批量清理。
func KillOwner(owner string) {
	if owner == "" {
		return
	}
	backgroundMu.Lock()
	var ids []string
	for id, job := range backgroundJobs {
		if job.owner == owner {
			ids = append(ids, id)
		}
	}
	backgroundMu.Unlock()
	for _, id := range ids {
		if err := KillOwned(owner, id); err != nil {
			log.Print(err)
		}
	}
}
