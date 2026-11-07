package app

// 本文件把桌面端的人设目录接到远程扩展方法上：
// providers.personas 读 Magpie 人设清单，threads.setPersona 由 internal/remote/persona_methods.go
// 负责，它只依赖这里注入的 "personas" 服务。

import (
	"context"

	"mcode/internal/remote/wire"
)

// remotePersonaService 是注入给远程扩展方法的人设服务，实现 remote.personaLister。
type remotePersonaService struct {
	client *Client
}

// Personas 返回 provider/model 下的人设列表；不是 Magpie 端点或读不到来源时是空列表。
// force 忽略：桌面端的 MagpiePersonas 没有强制刷新入口（loadHeaderPersonas 的 force 只绕过
// 界面自己的记忆），magpie 的 web 结果自带缓存，每次打开菜单都强行联网只会拖慢界面。
func (s remotePersonaService) Personas(ctx context.Context, provider, model string, force bool) ([]wire.Persona, error) {
	if s.client == nil {
		return nil, nil
	}
	personas, err := s.client.MagpiePersonas(ctx, provider, model)
	if err != nil {
		return nil, err
	}
	res := make([]wire.Persona, 0, len(personas))
	for _, p := range personas {
		res = append(res, wire.Persona{ID: p.ID, Name: p.Name, Description: p.Description})
	}
	return res, nil
}

func init() {
	registerRemoteWiring(func(a *App) {
		if a == nil || a.backend == nil {
			return
		}
		a.backend.SetService("personas", remotePersonaService{client: a.remoteClient()})
	})
}
