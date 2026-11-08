package remote

import (
	"sync"

	"mcode/internal/remote/wire"
)

var mcodeModelsHook = struct {
	sync.RWMutex
	fn func() []wire.ProviderModel
}{}

// SetMcodeModels 设置内置 mcode 的动态模型目录。
func SetMcodeModels(fn func() []wire.ProviderModel) {
	mcodeModelsHook.Lock()
	mcodeModelsHook.fn = fn
	mcodeModelsHook.Unlock()
}

func mcodeModels() []wire.ProviderModel {
	mcodeModelsHook.RLock()
	fn := mcodeModelsHook.fn
	mcodeModelsHook.RUnlock()
	if fn == nil {
		return nil
	}
	return fn()
}
