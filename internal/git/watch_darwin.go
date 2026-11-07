//go:build darwin

package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

// FSEventStreamCreateFlags。
const (
	fseNoDefer    = 0x00000002 // kFSEventStreamCreateFlagNoDefer
	fseFileEvents = 0x00000010 // kFSEventStreamCreateFlagFileEvents
)

// FSEventStreamEventFlags：表示事件可能已丢失的标记。
const (
	fseMustScanSubDirs = 0x00000001
	fseUserDropped     = 0x00000002
	fseKernelDropped   = 0x00000004
	fseEventIdsWrapped = 0x00000008
)

const (
	// fseLatency 是 FSEvents 的事件合并窗口（秒）。
	fseLatency = 0.05
	// fseUTF8 是 kCFStringEncodingUTF8。
	fseUTF8 = 0x08000100
	// fseCookieTimeout 是等待 cookie 事件的上限，超时即退回全量快照。
	fseCookieTimeout = 2 * time.Second
	// fseCookiePrefix 是写入工作区 git 目录的 cookie 文件名前缀。
	fseCookiePrefix = "mcode-watch-cookie-"

	fseCoreServices   = "/System/Library/Frameworks/CoreServices.framework/CoreServices"
	fseCoreFoundation = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
	fseLibSystem      = "/usr/lib/libSystem.B.dylib"
)

// FSEvents 与 CoreFoundation 的函数指针，进程内只绑定一次。
var (
	fseOnce    sync.Once
	fseLoadErr error

	fseStreamCreate     func(alloc, callback uintptr, ctx *fseContext, paths uintptr, sinceWhen uint64, latency float64, flags uint32) uintptr
	fseStreamStart      func(stream uintptr) uint8
	fseStreamStop       func(stream uintptr)
	fseStreamInvalidate func(stream uintptr)
	fseStreamRelease    func(stream uintptr)
	fseStreamSetQueue   func(stream, queue uintptr)

	cfStringCreateWithCString func(alloc uintptr, s *byte, encoding uint32) uintptr
	cfArrayCreate             func(alloc uintptr, values *uintptr, n int, callbacks uintptr) uintptr
	cfRelease                 func(obj uintptr)
	kCFTypeArrayCallBacks     uintptr

	fseQueueCreate func(label *byte, attr uintptr) uintptr

	// fseCallback 是全进程唯一一个 FSEventStreamCallback，通过 info 指针分发到具体 watcher；
	// NewCallback 数量有上限，绝不能按 watcher 创建。
	fseCallback uintptr
	// fseQueue 是所有流共用的串行派发队列。
	fseQueue uintptr
)

// loadFSEvents 只加载一次 FSEvents 相关符号，任何失败都返回 error，绝不 panic。
func loadFSEvents() error {
	fseOnce.Do(func() { fseLoadErr = loadFSEventsOnce() })
	return fseLoadErr
}

// loadFSEventsOnce 加载框架并绑定符号；失败原因全部包装为 error。
func loadFSEventsOnce() (err error) {
	// RegisterLibFunc 在符号缺失或类型不符时会 panic，这里统一兜底成 error
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("加载 FSEvents 失败: %v", r)
		}
	}()

	coreServices, derr := purego.Dlopen(fseCoreServices, purego.RTLD_GLOBAL|purego.RTLD_NOW)
	if derr != nil {
		return fmt.Errorf("加载 CoreServices 失败: %w", derr)
	}
	coreFoundation, derr := purego.Dlopen(fseCoreFoundation, purego.RTLD_GLOBAL|purego.RTLD_NOW)
	if derr != nil {
		return fmt.Errorf("加载 CoreFoundation 失败: %w", derr)
	}
	libSystem, derr := purego.Dlopen(fseLibSystem, purego.RTLD_GLOBAL|purego.RTLD_NOW)
	if derr != nil {
		return fmt.Errorf("加载 libSystem 失败: %w", derr)
	}

	bindings := []struct {
		handle uintptr
		name   string
		fptr   any
	}{
		{coreServices, "FSEventStreamCreate", &fseStreamCreate},
		{coreServices, "FSEventStreamStart", &fseStreamStart},
		{coreServices, "FSEventStreamStop", &fseStreamStop},
		{coreServices, "FSEventStreamInvalidate", &fseStreamInvalidate},
		{coreServices, "FSEventStreamRelease", &fseStreamRelease},
		{coreServices, "FSEventStreamSetDispatchQueue", &fseStreamSetQueue},
		{coreFoundation, "CFStringCreateWithCString", &cfStringCreateWithCString},
		{coreFoundation, "CFArrayCreate", &cfArrayCreate},
		{coreFoundation, "CFRelease", &cfRelease},
	}
	for _, b := range bindings {
		if berr := registerLibFunc(b.handle, b.name, b.fptr); berr != nil {
			return berr
		}
	}
	if berr := registerLibFunc(libSystem, "dispatch_queue_create", &fseQueueCreate); berr != nil {
		// 个别系统上 dispatch 只在进程默认命名空间可见
		if berr2 := registerLibFunc(purego.RTLD_DEFAULT, "dispatch_queue_create", &fseQueueCreate); berr2 != nil {
			return berr
		}
	}

	// kCFTypeArrayCallBacks 是结构体数据符号：符号地址即 CFArrayCreate 需要的 &kCFTypeArrayCallBacks
	callbacks, derr := purego.Dlsym(coreFoundation, "kCFTypeArrayCallBacks")
	if derr != nil {
		return fmt.Errorf("解析 kCFTypeArrayCallBacks 失败: %w", derr)
	}
	kCFTypeArrayCallBacks = callbacks

	fseCallback = purego.NewCallback(fseEvents)
	label := append([]byte("mcode.git.watch"), 0)
	fseQueue = fseQueueCreate(&label[0], 0)
	runtime.KeepAlive(label)
	if fseQueue == 0 {
		return errors.New("创建 FSEvents 派发队列失败")
	}
	return nil
}

// registerLibFunc 先确认符号存在再绑定：RegisterLibFunc 在符号缺失时会 panic，
// 这里统一转成 error，保证监听初始化失败只退化、不崩溃。
func registerLibFunc(handle uintptr, name string, fptr any) error {
	if _, err := purego.Dlsym(handle, name); err != nil {
		return fmt.Errorf("解析符号 %s 失败: %w", name, err)
	}
	purego.RegisterLibFunc(fptr, handle, name)
	return nil
}

// fseContext 对应 FSEventStreamContext：FSEvents 会复制它，并把 info 原样回传给回调。
type fseContext struct {
	version         uintptr
	info            uintptr
	retain          uintptr
	release         uintptr
	copyDescription uintptr
}

// fseWatches 用 info 指针把全进程唯一的回调分发到具体 watcher。
var fseWatches = struct {
	sync.Mutex
	m map[uintptr]*fseventsWatcher
}{m: map[uintptr]*fseventsWatcher{}}

// fseEvents 是全进程唯一的 FSEventStreamCallback；
// 未设置 UseCFTypes 时 paths 是 C 字符串数组（char **）。
func fseEvents(stream, info, numEvents, paths, flags, ids uintptr) uintptr {
	if info == 0 {
		return 0
	}
	fseWatches.Lock()
	w := fseWatches.m[info]
	fseWatches.Unlock()
	if w == nil {
		return 0
	}
	eventPaths, eventFlags := ptrOf(paths), ptrOf(flags)
	var batch []string
	var batchFlags []uint32
	for i := uintptr(0); i < numEvents; i++ {
		p := *(*uintptr)(unsafe.Add(eventPaths, i*unsafe.Sizeof(uintptr(0))))
		if p == 0 {
			continue
		}
		batch = append(batch, cString(ptrOf(p)))
		batchFlags = append(batchFlags, *(*uint32)(unsafe.Add(eventFlags, i*4)))
	}
	w.note(batch, batchFlags)
	return 0
}

// ptrOf 把 C 地址转回 unsafe.Pointer：地址来自 FSEvents 而非 Go，不能直接做 uintptr→指针转换。
func ptrOf(p uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&p)) }

// cString 复制 p 处 NUL 结尾的 C 字符串。
func cString(p unsafe.Pointer) string {
	n := 0
	for *(*byte)(unsafe.Add(p, n)) != 0 {
		n++
	}
	return string(unsafe.Slice((*byte)(p), n))
}

// fseventsWatcher 是基于 FSEvents 的工作区监听器：每个工作区一条流，
// 事件路径不在工作区 git 目录下时 generation 加一；
// cookie 文件用来确认此前发生的写入都已被计入，超时即退回全量快照。
// gitDir 是工作区真正使用的 git 目录：普通仓库为 <root>/.git，
// git worktree 下为 <root>/.git 指向的目录，通常落在工作区之外。
type fseventsWatcher struct {
	root         string // realpath 解析后的工作区根目录
	gitDir       string
	gitPrefix    string // gitDir + "/"
	cookiePrefix string // gitDir + "/mcode-watch-cookie-"

	gen atomic.Uint64

	mu      sync.Mutex
	stream  uintptr
	info    uintptr
	closed  bool
	next    uint64
	cookies map[uint64]chan struct{}
}

// resolveGitDir 返回工作区真正使用的 git 目录：普通仓库是 <root>/.git；
// git worktree 下 <root>/.git 是内容为「gitdir: <path>」的普通文件，
// 这里解析出它指向的目录（相对路径相对 <root> 解析），
// 让 cookie 文件写进真实的 git 目录，并继续用它过滤 git 自身的写入。
func resolveGitDir(root string) (string, error) {
	gitPath := filepath.Join(root, ".git")
	info, err := os.Stat(gitPath)
	if err == nil && info.IsDir() {
		return gitPath, nil
	}
	if err != nil {
		return "", fmt.Errorf("工作区缺少 .git 目录: %s", gitPath)
	}

	data, rerr := os.ReadFile(gitPath)
	if rerr != nil {
		return "", fmt.Errorf("读取 .git 文件失败: %w", rerr)
	}
	line := strings.TrimSpace(strings.SplitN(string(data), "\n", 2)[0])
	const gitdirPrefix = "gitdir:"
	if !strings.HasPrefix(line, gitdirPrefix) {
		return "", fmt.Errorf("无法识别的 .git 文件: %s", gitPath)
	}
	dir := strings.TrimSpace(strings.TrimPrefix(line, gitdirPrefix))
	if dir == "" {
		return "", fmt.Errorf("无法识别的 .git 文件: %s", gitPath)
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	dir = filepath.Clean(dir)
	// 事件路径是磁盘上的真实路径，先解析软链再比对前缀
	if resolved, serr := filepath.EvalSymlinks(dir); serr == nil {
		dir = resolved
	}
	if dirInfo, derr := os.Stat(dir); derr != nil || !dirInfo.IsDir() {
		return "", fmt.Errorf("worktree 的 git 目录不可用: %s", dir)
	}
	return dir, nil
}

// newPlatformWatcher 创建基于 FSEvents 的监听器；加载或创建失败时返回 error（调用方退回轮询实现）。
func newPlatformWatcher(root string) (w Watcher, err error) {
	defer func() {
		if r := recover(); r != nil {
			w, err = nil, fmt.Errorf("创建 FSEvents 监听失败: %v", r)
		}
	}()
	if lerr := loadFSEvents(); lerr != nil {
		return nil, lerr
	}

	// 事件路径是磁盘上的真实路径（/var 是 /private/var 的软链），先解析再比对
	real, rerr := filepath.EvalSymlinks(root)
	if rerr != nil {
		return nil, fmt.Errorf("解析工作区路径失败: %w", rerr)
	}
	real, rerr = filepath.Abs(real)
	if rerr != nil {
		return nil, rerr
	}
	info, serr := os.Stat(real)
	if serr != nil {
		return nil, serr
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("工作区路径不是目录: %s", real)
	}
	gitDir, gerr := resolveGitDir(real)
	if gerr != nil {
		return nil, gerr
	}

	watch := &fseventsWatcher{
		root:         real,
		gitDir:       gitDir,
		gitPrefix:    gitDir + string(filepath.Separator),
		cookiePrefix: filepath.Join(gitDir, fseCookiePrefix),
		cookies:      map[uint64]chan struct{}{},
	}
	watch.gen.Store(1)

	// 监听路径先放工作区根目录；worktree 的 git 目录不在根目录下时再补一条，
	// 否则写进 git 目录的 cookie 不会产生事件，每次 Generation 都会等到超时。
	watchPaths := []string{real}
	if gitDir != filepath.Join(real, ".git") {
		watchPaths = append(watchPaths, gitDir)
	}
	cfStrings := make([]uintptr, 0, len(watchPaths))
	pathBufs := make([][]byte, 0, len(watchPaths))
	for _, p := range watchPaths {
		buf := append([]byte(p), 0)
		cf := cfStringCreateWithCString(0, &buf[0], fseUTF8)
		if cf == 0 {
			for _, created := range cfStrings {
				cfRelease(created)
			}
			return nil, fmt.Errorf("无法创建监听路径的 CFString: %s", p)
		}
		cfStrings = append(cfStrings, cf)
		pathBufs = append(pathBufs, buf)
	}
	cfPaths := cfArrayCreate(0, &cfStrings[0], len(cfStrings), kCFTypeArrayCallBacks)
	runtime.KeepAlive(pathBufs)
	// CFArray 已经 retain 每个字符串，这里只丢弃本地引用
	for _, cf := range cfStrings {
		cfRelease(cf)
	}
	if cfPaths == 0 {
		return nil, errors.New("无法创建监听路径的 CFArray")
	}
	watch.info = uintptr(unsafe.Pointer(watch))
	fseCtx := fseContext{info: watch.info}
	// kFSEventStreamEventIdSinceNow：只关心从现在起的事件
	stream := fseStreamCreate(0, fseCallback, &fseCtx, cfPaths, ^uint64(0), fseLatency, fseNoDefer|fseFileEvents)
	cfRelease(cfPaths)
	if stream == 0 {
		return nil, errors.New("无法创建 FSEventStream")
	}

	fseWatches.Lock()
	fseWatches.m[watch.info] = watch
	fseWatches.Unlock()

	fseStreamSetQueue(stream, fseQueue)
	if fseStreamStart(stream) == 0 {
		fseWatches.Lock()
		delete(fseWatches.m, watch.info)
		fseWatches.Unlock()
		fseStreamInvalidate(stream)
		fseStreamRelease(stream)
		return nil, errors.New("无法启动 FSEventStream")
	}
	watch.stream = stream
	return watch, nil
}

// Generation 写入 cookie 文件并等待它的事件回调，确认此前发生的写入都已被计入后返回当前代次；
// 超时返回错误，调用方会退回全量快照。
func (w *fseventsWatcher) Generation(ctx context.Context) (uint64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return 0, errors.New("watcher 已关闭")
	}
	w.next++
	n := w.next
	done := make(chan struct{})
	w.cookies[n] = done
	w.mu.Unlock()

	cookiePath := w.cookiePrefix + strconv.FormatUint(n, 10)
	if werr := os.WriteFile(cookiePath, nil, 0644); werr != nil {
		w.dropCookie(n)
		return 0, fmt.Errorf("写入监听 cookie 失败: %w", werr)
	}

	timer := time.NewTimer(fseCookieTimeout)
	defer timer.Stop()
	select {
	case <-done:
		_ = os.Remove(cookiePath)
		return w.gen.Load(), nil
	case <-timer.C:
		w.dropCookie(n)
		_ = os.Remove(cookiePath)
		return 0, errors.New("等待 FSEvents cookie 事件超时")
	case <-ctx.Done():
		w.dropCookie(n)
		_ = os.Remove(cookiePath)
		return 0, ctx.Err()
	}
}

// dropCookie 移除等待中的 cookie；事件回调已消费时什么都不做。
func (w *fseventsWatcher) dropCookie(n uint64) {
	w.mu.Lock()
	delete(w.cookies, n)
	w.mu.Unlock()
}

// note 处理一批 FSEvents 事件：cookie 事件唤醒等待者，.git 下的事件忽略，其余让 generation 加一。
func (w *fseventsWatcher) note(paths []string, flags []uint32) {
	var ready []chan struct{}
	changed := false
	w.mu.Lock()
	for i, p := range paths {
		if i < len(flags) && flags[i]&(fseMustScanSubDirs|fseUserDropped|fseKernelDropped|fseEventIdsWrapped) != 0 {
			// 事件可能已丢失：按工作区已变化处理
			changed = true
			continue
		}
		if strings.HasPrefix(p, w.cookiePrefix) {
			if n, perr := strconv.ParseUint(strings.TrimPrefix(p, w.cookiePrefix), 10, 64); perr == nil {
				if done := w.cookies[n]; done != nil {
					delete(w.cookies, n)
					ready = append(ready, done)
				}
			}
			continue
		}
		// git 自身的写入（对象、引用、索引）不改变工作区文件
		if p == w.gitDir || strings.HasPrefix(p, w.gitPrefix) {
			continue
		}
		changed = true
	}
	w.mu.Unlock()

	if changed {
		w.gen.Add(1)
	}
	for _, done := range ready {
		close(done)
	}
}

// Close 停止并释放 FSEventStream；可重复调用。
func (w *fseventsWatcher) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	stream := w.stream
	w.stream = 0
	w.mu.Unlock()

	// 先摘掉分发映射：在途回调找不到 watcher 会直接返回
	fseWatches.Lock()
	delete(fseWatches.m, w.info)
	fseWatches.Unlock()

	if stream != 0 {
		fseStreamStop(stream)
		fseStreamInvalidate(stream)
		fseStreamRelease(stream)
	}
	return nil
}
