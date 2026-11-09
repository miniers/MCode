package remote

import (
	"sync"

	"mcode/internal/remote/wire"
)

var apiCatalogs = struct {
	sync.RWMutex
	callbacks map[string]func() []wire.ProviderModel
}{callbacks: make(map[string]func() []wire.ProviderModel)}

// SetAPICatalog 注册 API 来源的模型目录；传入 nil 时清除注册。
func SetAPICatalog(kind string, fn func() []wire.ProviderModel) {
	apiCatalogs.Lock()
	defer apiCatalogs.Unlock()
	if fn == nil {
		delete(apiCatalogs.callbacks, kind)
	} else {
		apiCatalogs.callbacks[kind] = fn
	}
}

func apiCatalog(kind string) ([]wire.ProviderModel, bool) {
	apiCatalogs.RLock()
	fn, registered := apiCatalogs.callbacks[kind]
	apiCatalogs.RUnlock()
	if !registered {
		return nil, false
	}
	rows := fn()
	if rows == nil {
		return nil, true
	}
	result := make([]wire.ProviderModel, len(rows))
	copy(result, rows)
	for i := range result {
		row := &result[i]
		row.Efforts = append([]string(nil), row.Efforts...)
		if row.Detail != nil {
			v := *row.Detail
			row.Detail = &v
		}
		if row.DefaultEffort != nil {
			v := *row.DefaultEffort
			row.DefaultEffort = &v
		}
		if row.PreferredEffort != nil {
			v := *row.PreferredEffort
			row.PreferredEffort = &v
		}
		if row.Cost != nil {
			v := *row.Cost
			row.Cost = &v
		}
	}
	return result, true
}

func hasAPICatalog(kind string) bool {
	apiCatalogs.RLock()
	_, ok := apiCatalogs.callbacks[kind]
	apiCatalogs.RUnlock()
	return ok
}
