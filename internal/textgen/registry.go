package textgen

import (
	"context"
	"sort"
	"sync"
)

// GenerateFunc 是外部注入的一次性文本生成函数。
// textgen 不能 import internal/app，内置 provider（如 mcode）通过 RegisterGenerator
// 注入实现，swarm 审查等调用方无需依赖 app 包。
type GenerateFunc func(ctx context.Context, model, effort, prompt string) (string, error)

var generators = struct {
	sync.RWMutex
	fns map[string]GenerateFunc
}{fns: make(map[string]GenerateFunc)}

// RegisterGenerator 注册 provider 的生成函数；fn 为 nil 时注销。
// provider 统一按 normalizeProvider 归一化，空名忽略。
func RegisterGenerator(provider string, fn GenerateFunc) {
	name := normalizeProvider(provider)
	if name == "" {
		return
	}
	generators.Lock()
	defer generators.Unlock()
	if fn == nil {
		delete(generators.fns, name)
		return
	}
	generators.fns[name] = fn
}

// registeredGenerator 返回已注册的生成函数。
func registeredGenerator(provider string) (GenerateFunc, bool) {
	name := normalizeProvider(provider)
	if name == "" {
		return nil, false
	}
	generators.RLock()
	defer generators.RUnlock()
	fn, ok := generators.fns[name]
	return fn, ok
}

// registeredProviders 返回已注册的 provider 名，按字典序排序以保证回退顺序稳定。
func registeredProviders() []string {
	generators.RLock()
	names := make([]string, 0, len(generators.fns))
	for name := range generators.fns {
		names = append(names, name)
	}
	generators.RUnlock()
	sort.Strings(names)
	return names
}
