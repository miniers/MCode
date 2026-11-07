package projctx

import (
	"fmt"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// DebounceInterval 默认的文件变更防抖时间间隔。
const DebounceInterval = 300 * time.Millisecond

// Watcher 监听项目上下文目录的文件变更。
type Watcher struct {
	fsw       *fsnotify.Watcher
	debounce  time.Duration
	onChange  func()
	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
	done      chan struct{}
	timer     *time.Timer
}

// Watch 创建并启动针对上下文目录、docs 及 inbox 目录的文件变更监听器。
func (s *Service) Watch(onChange func()) (*Watcher, error) {
	if err := s.Ensure(); err != nil {
		return nil, err
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("projctx: new watcher: %w", err)
	}

	if err := fsw.Add(s.ContextDir()); err != nil {
		_ = fsw.Close()
		return nil, fmt.Errorf("projctx: watch context dir: %w", err)
	}
	if err := fsw.Add(s.DocsPath()); err != nil {
		_ = fsw.Close()
		return nil, fmt.Errorf("projctx: watch docs dir: %w", err)
	}
	if err := fsw.Add(s.InboxPath()); err != nil {
		_ = fsw.Close()
		return nil, fmt.Errorf("projctx: watch inbox dir: %w", err)
	}

	w := &Watcher{
		fsw:      fsw,
		debounce: DebounceInterval,
		onChange: onChange,
		done:     make(chan struct{}),
	}

	go w.loop()

	return w, nil
}

func (w *Watcher) loop() {
	defer close(w.done)
	for {
		select {
		case _, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			w.mu.Lock()
			if !w.closed {
				if w.timer != nil {
					w.timer.Stop()
				}
				d := w.debounce
				w.timer = time.AfterFunc(d, func() {
					// 锁内只取快照，回调在锁外调用：回调里再调 Close 不会重入这把锁。
					w.mu.Lock()
					closed, onChange := w.closed, w.onChange
					w.mu.Unlock()
					if !closed && onChange != nil {
						onChange()
					}
				})
			}
			w.mu.Unlock()
		case _, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			// Errors 直接忽略
		}
	}
}

// Close 关闭监听器，停止计时器并等待后台 goroutine 退出。可安全地重复调用。
func (w *Watcher) Close() error {
	var err error
	w.closeOnce.Do(func() {
		w.mu.Lock()
		w.closed = true
		if w.timer != nil {
			w.timer.Stop()
		}
		w.mu.Unlock()

		err = w.fsw.Close()
		<-w.done
	})
	return err
}
