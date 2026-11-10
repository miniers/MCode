package pi

// 本文件实现一个 `pi --mode rpc` 子进程的托管：唯一的 stdout 读取协程把应答按 id 交给
// 等待它的 call，其余事件交给当前轮（sink）。进程可以只服务一轮（Options.IdleTTL 为 0），
// 也可以在线程空闲时驻留，下一轮直接复用（pool 逻辑在 pi.go 的 Run / acquire 里）。

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"mcode/internal/procutil"
)

var setProcessGroup = procutil.SetProcessGroup
var killProcessGroup = procutil.KillProcessGroup

const (
	// maxRPCLineBytes 是 stdout 单行 RPC 消息的上限（16MB），防止脏流或超大帧耗尽内存。
	maxRPCLineBytes = 16 << 20
	// sinkBuffer 是当轮事件通道的缓冲：Run 阻塞在审批上时，读取协程还能再收这么多行。
	sinkBuffer = 256
	// killGrace 是 SIGTERM 之后等进程自行退出的时间，超时按进程组 SIGKILL。
	killGrace = 3 * time.Second
	// abortGrace 是取消一轮时等 pi 回到空闲的时间，超时就放弃这个进程。
	abortGrace = 3 * time.Second
)

// errProcDead 表示进程已退出，等待中的请求不会再有应答。
var errProcDead = errors.New("pi: process exited")

// turnSink 接收当轮的事件；ch 在进程退出时被读取协程关闭，quit 在 detach 时关闭，
// 让读取协程不会卡在一个已经没人读的通道上。
type turnSink struct {
	ch   chan *rpcEvent
	quit chan struct{}
}

// proc 是一个运行中的 pi 子进程。
type proc struct {
	key      string
	threadID string
	cmd      *exec.Cmd
	writer   *procutil.LineWriter
	stderr   *procutil.TailBuffer
	// gatePath 是门禁运行模式文件；复用进程时每轮重写。
	gatePath string
	cleanups []func()

	// exited 在读取协程收尾（含 Wait 与清理）之后关闭，waitErr 此后可读。
	exited  chan struct{}
	waitErr error

	closeOnce sync.Once

	// 以下字段由 mu 保护。
	mu        sync.Mutex
	dead      bool
	sink      *turnSink
	pending   map[string]chan *rpcEvent
	sessionID string
	// model / effort 是进程当前生效的模型与思考档位（turn.Model / turn.Effort 的原值），
	// 复用时据此决定要不要发 set_model / set_thinking_level。
	model    string
	effort   string
	busy     bool
	lastUsed time.Time
	idle     *time.Timer
}

// readBoundedLine 从 r 中读取一行直至 '\n'，最多保留 limit 字节。
// 超过 limit 的行不再保存多余内容，并继续丢弃剩余字节直至 '\n'，返回 tooLong=true。
// EOF 前没有换行的最后一段原样返回并带上 io.EOF，保持 ReadBytes 原来的语义。
func readBoundedLine(r *bufio.Reader, limit int) (line []byte, tooLong bool, err error) {
	for {
		var chunk []byte
		chunk, err = r.ReadSlice('\n')
		if !tooLong {
			line = append(line, chunk...)
			if len(line) > limit {
				tooLong = true
			}
		}
		if err == nil {
			return line, tooLong, nil
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return line, tooLong, err
	}
}

// readLoop 是进程 stdout 的唯一读取者；EOF 之后回收进程、跑清理并关闭 exited。
// onExit 在最后调用，用来把进程从池里摘掉。
func (p *proc) readLoop(stdout io.Reader, onExit func(*proc)) {
	reader := bufio.NewReader(stdout)
	for {
		raw, tooLong, err := readBoundedLine(reader, maxRPCLineBytes)
		if tooLong {
			log.Printf("pi: dropped oversized rpc line (%d bytes)", len(raw))
			if err != nil {
				break
			}
			continue
		}
		if ev, parseErr := parseEvent(raw); parseErr == nil && ev != nil {
			p.dispatch(ev)
		}
		if err != nil {
			break
		}
	}
	p.waitErr = p.cmd.Wait()

	p.mu.Lock()
	p.dead = true
	sink, pending := p.sink, p.pending
	p.sink, p.pending = nil, nil
	p.mu.Unlock()
	for _, ch := range pending {
		close(ch)
	}
	if sink != nil {
		close(sink.ch)
	}
	for i := len(p.cleanups) - 1; i >= 0; i-- {
		p.cleanups[i]()
	}
	close(p.exited)
	if onExit != nil {
		onExit(p)
	}
}

// dispatch 把一条事件交给等待它的 call，或交给当前轮；没有人接收时丢弃。
func (p *proc) dispatch(ev *rpcEvent) {
	p.mu.Lock()
	var waiter chan *rpcEvent
	if ev.Type == "response" && ev.ID != "" {
		waiter = p.pending[ev.ID]
		delete(p.pending, ev.ID)
	}
	sink := p.sink
	p.mu.Unlock()

	if waiter != nil {
		waiter <- ev
		return
	}
	if sink == nil {
		return
	}
	select {
	case sink.ch <- ev:
	case <-sink.quit:
	}
}

// attach 把当轮登记为事件接收方；调用方用 detach 注销。
func (p *proc) attach() *turnSink {
	sink := &turnSink{ch: make(chan *rpcEvent, sinkBuffer), quit: make(chan struct{})}
	p.mu.Lock()
	if p.dead {
		close(sink.ch)
	} else {
		p.sink = sink
	}
	p.mu.Unlock()
	return sink
}

// detach 注销当轮的事件接收方，之后到达的事件被丢弃。
func (p *proc) detach(sink *turnSink) {
	p.mu.Lock()
	if p.sink == sink {
		p.sink = nil
	}
	p.mu.Unlock()
	close(sink.quit)
}

// call 写一条带 id 的命令并等它的应答；success=false 的应答转成错误。
func (p *proc) call(ctx context.Context, req rpcRequest) (*rpcEvent, error) {
	if req.ID == "" {
		return nil, fmt.Errorf("pi: call %s without id", req.Type)
	}
	waiter := make(chan *rpcEvent, 1)
	p.mu.Lock()
	if p.dead {
		p.mu.Unlock()
		return nil, errProcDead
	}
	if p.pending == nil {
		p.pending = make(map[string]chan *rpcEvent)
	}
	p.pending[req.ID] = waiter
	p.mu.Unlock()

	forget := func() {
		p.mu.Lock()
		delete(p.pending, req.ID)
		p.mu.Unlock()
	}
	line, err := requestLine(req)
	if err != nil {
		forget()
		return nil, err
	}
	if err := p.writer.WriteLine(line); err != nil {
		forget()
		return nil, fmt.Errorf("%w: write %s: %v", errProcDead, req.Type, err)
	}
	select {
	case ev, ok := <-waiter:
		if !ok {
			return nil, errProcDead
		}
		if ev.Success != nil && !*ev.Success {
			return ev, fmt.Errorf("pi: %s: %s", req.Type, firstNonEmpty(ev.Error, "request failed"))
		}
		return ev, nil
	case <-ctx.Done():
		forget()
		return nil, ctx.Err()
	}
}

// setSession 记录 pi 最近一次回报的会话文件，复用判断以它为准。
func (p *proc) setSession(sessionID string) {
	p.mu.Lock()
	p.sessionID = sessionID
	p.mu.Unlock()
}

// close 请求进程退出：先 SIGTERM 进程组（超过 killGrace 仍未退出就 SIGKILL），再关 stdin；
// 关 stdin 交给独立协程，不会卡住调用方。不等待。
func (p *proc) close() {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		if p.idle != nil {
			p.idle.Stop()
			p.idle = nil
		}
		p.mu.Unlock()
		// 先杀进程组再关 stdin：子进程不再读 stdin 而管道写满时，writer.close() 会卡在 l.mu 上，
		// 先关 writer 会让 SIGTERM/SIGKILL 永远等不到执行。
		_ = killProcessGroup(p.cmd, syscall.SIGTERM)
		go func() {
			select {
			case <-p.exited:
			case <-time.After(killGrace):
				_ = killProcessGroup(p.cmd, syscall.SIGKILL)
			}
		}()
		// 写端可能正握着锁卡在管道 Write 上；进程退出后那次 Write 返回错误并放锁，这里不占用调用方。
		go func() { _ = p.writer.Close() }()
	})
}

// closeAndWait 先 close，再等进程回收完成（含清理）。
func (p *proc) closeAndWait() {
	p.close()
	<-p.exited
}
