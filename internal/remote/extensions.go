package remote

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"mcode/internal/remote/wire"
)

// ExtensionHandler 是扩展方法的处理函数。
// d 是当前 Dispatcher（可取后端能力），c 是发起请求的连接，params 是原始参数（未解码）。
// 返回 nil 错误时按成功结果回复；返回 *ExtError 时按其中的错误码回复；其它错误归为内部错误。
type ExtensionHandler func(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error)

// ExtError 是扩展方法可显式控制错误码的错误类型。
type ExtError struct {
	Code    int
	Message string
}

// Error 实现 error 接口。
func (e *ExtError) Error() string {
	return e.Message
}

// ExtInvalidParams 构造参数错误（错误码 wire.InvalidParams）。
func ExtInvalidParams(msg string) *ExtError {
	return &ExtError{Code: wire.InvalidParams, Message: msg}
}

// ExtNotImplemented 构造未实现错误（错误码 wire.MethodNotFound）。
// 用于扩展方法在运行期判定某项能力不可用（如依赖的服务未注入）的场景。
func ExtNotImplemented() *ExtError {
	return &ExtError{Code: wire.MethodNotFound, Message: "Method not implemented"}
}

// 扩展方法注册表：包级，由 extMu 保护。约定只在 init() 中调用 RegisterMethod，
// 因此进程起来之后这张表是只读的。
var (
	extMu       sync.RWMutex
	extRegistry = make(map[string]ExtensionHandler)
)

// RegisterMethod 注册一个扩展方法。
// 方法名重复、为空或与内置方法重名时 panic：这些都属于编程错误，应在启动期暴露。
func RegisterMethod(name string, h ExtensionHandler) {
	name = strings.TrimSpace(name)
	if name == "" {
		panic("remote: 扩展方法名不能为空")
	}
	if h == nil {
		panic("remote: 扩展方法 handler 不能为空: " + name)
	}
	for _, builtin := range implementedMethods {
		if builtin == name {
			panic("remote: 扩展方法 " + name + " 与内置方法重名")
		}
	}
	extMu.Lock()
	defer extMu.Unlock()
	if _, dup := extRegistry[name]; dup {
		panic("remote: 扩展方法重复注册: " + name)
	}
	extRegistry[name] = h
}

// lookupExtension 查找扩展方法处理函数。
func lookupExtension(name string) (ExtensionHandler, bool) {
	extMu.RLock()
	defer extMu.RUnlock()
	h, ok := extRegistry[name]
	return h, ok
}

// extensionMethodNames 返回已注册的扩展方法名（升序，只读副本）。
func extensionMethodNames() []string {
	extMu.RLock()
	names := make([]string, 0, len(extRegistry))
	for name := range extRegistry {
		names = append(names, name)
	}
	extMu.RUnlock()
	sort.Strings(names)
	return names
}

// decodeExtParams 严格解码扩展方法参数（未知字段直接拒绝）。
// 解码失败统一转成 wire.InvalidParams，避免扩展各自造错误文案。
func decodeExtParams(raw json.RawMessage, v any) error {
	if err := decodeStrict(raw, v); err != nil {
		return ExtInvalidParams("Invalid params: " + err.Error())
	}
	return nil
}

// 服务注册表：以 *MemoryBackend 为键保存扩展注入的服务实例。
// 不往 MemoryBackend 结构体加字段，扩展无需改动后端定义。
var (
	backendServices   sync.Map // map[*MemoryBackend]map[string]any
	backendServicesMu sync.RWMutex
)

// SetService 向后端注入一个命名服务；svc 为 nil 时删除同名服务。
// 典型用法是在 app 层的 registerRemoteWiring 回调里调用，供远程扩展方法取用。
func (b *MemoryBackend) SetService(name string, svc any) {
	if b == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	backendServicesMu.Lock()
	defer backendServicesMu.Unlock()
	value, _ := backendServices.LoadOrStore(b, make(map[string]any))
	services := value.(map[string]any)
	if svc == nil {
		delete(services, name)
		return
	}
	services[name] = svc
}

// Service 读取通过 SetService 注入的命名服务，未注入时返回 nil。
func (b *MemoryBackend) Service(name string) any {
	if b == nil {
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	value, ok := backendServices.Load(b)
	if !ok {
		return nil
	}
	backendServicesMu.RLock()
	defer backendServicesMu.RUnlock()
	return value.(map[string]any)[name]
}

// backendService 从后端取出命名服务并断言为目标类型。
// 后端不支持服务注册表或服务类型不符时返回零值与 false。
func backendService[T any](backend Backend, name string) (T, bool) {
	var zero T
	if backend == nil {
		return zero, false
	}
	registry, ok := backend.(interface{ Service(string) any })
	if !ok {
		return zero, false
	}
	svc, ok := registry.Service(name).(T)
	if !ok {
		return zero, false
	}
	return svc, true
}
