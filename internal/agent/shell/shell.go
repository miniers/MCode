package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mcode/internal/harness"
)

// Options 描述 shell 命令的执行环境。
type Options struct {
	Command string
	WorkDir string
	Env     []string
	Timeout time.Duration
	Sandbox harness.Sandbox
	Mode    harness.PermissionMode
}

// Result 保存命令输出及退出状态。
type Result struct {
	Output         string
	ExitCode       int
	TimedOut       bool
	Cancelled      bool
	FullOutputPath string
}

const maxOutputBytes = 50 * 1024
const maxOutputLines = 2000

// shellTempDir 是临时文件根目录，测试可替换。
var shellTempDir = os.TempDir

// shellLogMaxAge 是清理旧 shell 日志的阈值。
var shellLogMaxAge = 24 * time.Hour

var shellLogCleanupOnce sync.Once

var secretValues = struct {
	sync.RWMutex
	values map[string]struct{}
}{}

// SetSecretValues 更新需要从子进程环境中过滤的密钥值。
func SetSecretValues(values []string) {
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		if len(value) >= 8 {
			unique[value] = struct{}{}
		}
	}
	secretValues.Lock()
	secretValues.values = unique
	secretValues.Unlock()
}

func childEnv(base []string) []string {
	secretValues.RLock()
	defer secretValues.RUnlock()
	env := make([]string, 0, len(base))
	for _, entry := range base {
		name, value, _ := strings.Cut(entry, "=")
		if name == "ANTHROPIC_API_KEY" || name == "ANTHROPIC_AUTH_TOKEN" || name == "OPENAI_API_KEY" ||
			strings.HasPrefix(name, "MCODE_") && (strings.Contains(name, "KEY") || strings.Contains(name, "TOKEN")) {
			continue
		}
		if _, found := secretValues.values[value]; found {
			continue
		}
		env = append(env, entry)
	}
	return env
}

// shellLogDir 返回 shell 日志专用子目录。
func shellLogDir() string {
	return filepath.Join(shellTempDir(), "mcode-shell")
}

// cleanupOldShellLogs 删除超过 shellLogMaxAge 的 shell 日志。
func cleanupOldShellLogs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-shellLogMaxAge)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		matched, err := filepath.Match("mcode-shell-*.log", entry.Name())
		if err != nil || !matched {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, entry.Name()))
	}
}

// processTracker 保存调用方关心的子进程生命周期回调。
type processTracker struct {
	onStart func(pid int)
	onExit  func(pid int)
}

type processTrackerKey struct{}
type ownerKey struct{}

// WithOwner 将后台任务拥有者传入上下文。
func WithOwner(ctx context.Context, owner string) context.Context {
	return context.WithValue(ctx, ownerKey{}, owner)
}

// OwnerFrom 返回上下文中的后台任务拥有者。
func OwnerFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	owner, _ := ctx.Value(ownerKey{}).(string)
	return owner
}

// callbackError 将回调 panic 转换为可报告的错误。
func callbackError(fn func()) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("shell 回调 panic: %v", v)
		}
	}()
	fn()
	return nil
}

func waitCommand(cmd *exec.Cmd) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("shell 等待进程 panic: %v", v)
		}
	}()
	return cmd.Wait()
}

// WithProcessTracker 返回携带进程回调的 ctx：Run 在子进程启动成功后调用 onStart，
// 子进程退出后调用 onExit；ctx 未携带回调时 Run 不做任何通知。
func WithProcessTracker(ctx context.Context, onStart func(pid int), onExit func(pid int)) context.Context {
	if ctx == nil || (onStart == nil && onExit == nil) {
		return ctx
	}
	return context.WithValue(ctx, processTrackerKey{}, &processTracker{onStart: onStart, onExit: onExit})
}

// trackerFrom 取出 ctx 携带的进程回调，没有时返回 nil。
func trackerFrom(ctx context.Context) *processTracker {
	if ctx == nil {
		return nil
	}
	tracker, _ := ctx.Value(processTrackerKey{}).(*processTracker)
	return tracker
}

type outputWriter struct {
	tail     []byte
	file     *os.File
	err      error
	onOutput func(string)
}

func (w *outputWriter) Write(p []byte) (int, error) {
	if w.onOutput != nil {
		if err := callbackError(func() { w.onOutput(string(p)) }); err != nil {
			w.err = err
			return 0, err
		}
	}
	if w.file != nil {
		if _, err := w.file.Write(p); err != nil {
			w.err = err
			return 0, err
		}
	}
	w.tail = append(w.tail, p...)
	if w.file == nil && (len(w.tail) > maxOutputBytes || lineCount(w.tail) > maxOutputLines) {
		dir := shellLogDir()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			w.err = err
			return 0, err
		}
		shellLogCleanupOnce.Do(func() {
			go func() {
				if err := callbackError(func() { cleanupOldShellLogs(dir) }); err != nil {
					log.Print(err)
				}
			}()
		})
		f, err := os.CreateTemp(dir, "mcode-shell-*.log")
		if err != nil {
			w.err = err
			return 0, err
		}
		w.file = f
		if _, err = f.Write(w.tail); err != nil {
			w.err = err
			return 0, err
		}
	}
	if len(w.tail) > maxOutputBytes {
		w.tail = w.tail[len(w.tail)-maxOutputBytes:]
	}
	for lineCount(w.tail) > maxOutputLines {
		i := bytes.IndexByte(w.tail, '\n')
		if i < 0 {
			break
		}
		w.tail = w.tail[i+1:]
	}
	return len(p), nil
}

func lineCount(b []byte) int {
	n := bytes.Count(b, []byte{'\n'})
	if len(b) > 0 && b[len(b)-1] != '\n' {
		n++
	}
	return n
}

// Run 执行命令，并在超时或取消时终止进程组。
func Run(ctx context.Context, opts Options, onOutput func(chunk string)) (Result, error) {
	result := Result{ExitCode: -1}
	if err := ctx.Err(); err != nil {
		result.Cancelled = true
		return result, nil
	}
	name, args := shellCommand(opts.Command)
	var cmd *exec.Cmd
	if opts.Sandbox != nil {
		var cleanup func()
		var err error
		cmd, cleanup, err = opts.Sandbox.Command(context.WithoutCancel(ctx), opts.WorkDir, opts.Mode, name, args...)
		if cleanup != nil {
			defer func() {
				if err := callbackError(cleanup); err != nil {
					log.Print(err)
				}
			}()
		}
		if err != nil {
			return result, err
		}
		if cmd == nil {
			return result, errors.New("sandbox returned a nil command")
		}
	} else {
		cmd = exec.Command(name, args...)
		cmd.Dir = opts.WorkDir
	}
	// 命令退出后，若脱离进程组的子进程仍占用管道，最多再等待 3 秒。
	cmd.WaitDelay = 3 * time.Second
	cmd.Env = append(append(childEnv(os.Environ()), opts.Env...), harness.NonInteractiveEnv()...)
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		return result, err
	}
	defer stdin.Close()
	cmd.Stdin = stdin
	output := &outputWriter{onOutput: onOutput}
	cmd.Stdout, cmd.Stderr = output, output
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return result, err
	}
	if tracker := trackerFrom(ctx); tracker != nil {
		pid := cmd.Process.Pid
		if tracker.onStart != nil {
			tracker.onStart(pid)
		}
		if tracker.onExit != nil {
			defer tracker.onExit(pid)
		}
	}
	done := make(chan error, 1)
	go func() { done <- waitCommand(cmd) }()
	var timeout <-chan time.Time
	var timer *time.Timer
	if opts.Timeout > 0 {
		timer = time.NewTimer(opts.Timeout)
		timeout = timer.C
		defer timer.Stop()
	}
	select {
	case err = <-done:
	case <-ctx.Done():
		result.Cancelled = true
		err = stopProcess(cmd, done)
	case <-timeout:
		result.TimedOut = true
		err = stopProcess(cmd, done)
	}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if output.file != nil {
		result.FullOutputPath = output.file.Name()
		if closeErr := output.file.Close(); output.err == nil {
			output.err = closeErr
		}
		result.Output = fmt.Sprintf("输出已截断，完整输出：%s\n%s", result.FullOutputPath, output.tail)
	} else {
		result.Output = string(output.tail)
	}
	if output.err != nil {
		return result, output.err
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return result, err
	}
	return result, nil
}
