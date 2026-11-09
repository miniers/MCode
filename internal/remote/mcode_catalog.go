package remote

import "mcode/internal/remote/wire"

// SetMcodeModels 设置内置 mcode 的动态模型目录。
func SetMcodeModels(fn func() []wire.ProviderModel) {
	SetAPICatalog("mcode", fn)
}
