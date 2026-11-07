package remote

import (
	"context"
	"encoding/json"
	"strings"

	"mcode/internal/remote/wire"
)

// 本文件注册会话人设相关的扩展方法：
//   - providers.personas：取某个 provider/model 可用的人设列表（Magpie 侧目录）；
//   - threads.setPersona：改会话人设，其余会话偏好保持不动。
//
// 桌面端同源逻辑见 internal/ui/chat_header.go 的 renderHeaderPersonaSection 与
// internal/ui/composer.go 的 composerPrefsOf：只有 Persona 一个字段变化，其它 prefs 原样回写。

// personaLister 是人设目录服务，由 app 层注入（见 internal/app/remote_persona.go）。
type personaLister interface {
	Personas(ctx context.Context, provider, model string, force bool) ([]wire.Persona, error)
}

// composerPrefsWriter 是后端可选的会话偏好写入口。
// Backend 接口本身只保证读路径，写路径由 MemoryBackend 提供，扩展方法按需断言。
type composerPrefsWriter interface {
	SetThreadComposerPrefs(ctx context.Context, threadID string, p ThreadComposerPrefs) (wire.ThreadSummary, error)
}

const (
	// personaServiceName 是人设服务在服务注册表里的名字。
	personaServiceName = "personas"
	// personaNoneID 与桌面端 personaNoneValue 一致：表示显式选择「不使用人设」。
	personaNoneID = "none"
)

func init() {
	RegisterMethod("providers.personas", handleProvidersPersonas)
	RegisterMethod("threads.setPersona", handleThreadsSetPersona)
}

// handleProvidersPersonas 返回 provider/model 下的人设列表。
// 服务未注入时返回 MethodNotFound，让客户端按「宿主不支持」隐藏人设入口。
func handleProvidersPersonas(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.PersonasListParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	svc, ok := backendService[personaLister](d.backend, personaServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	personas, err := svc.Personas(ctx, strings.TrimSpace(p.Provider), strings.TrimSpace(p.Model), p.Force)
	if err != nil {
		return nil, err
	}
	if personas == nil {
		// 统一输出空数组，别让客户端处理 null。
		personas = []wire.Persona{}
	}
	return wire.PersonasListResult{Personas: personas}, nil
}

// handleThreadsSetPersona 改会话人设并返回新的会话摘要。
// Persona 为空串表示清除；非空时必须是会话人设目录里真实存在的人设（参见 ensurePersonaKnown）。
func handleThreadsSetPersona(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.ThreadsSetPersonaParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	threadID := strings.TrimSpace(p.ThreadID)
	if !isValidUUID(threadID) {
		return nil, ExtInvalidParams("Invalid params: threadID must be a UUID")
	}
	persona := strings.TrimSpace(p.Persona)
	if !isValidPersona(persona) {
		return nil, ExtInvalidParams("Invalid params: persona is invalid")
	}
	writer, ok := d.backend.(composerPrefsWriter)
	if !ok {
		return nil, ExtNotImplemented()
	}
	current, err := d.backend.GetThread(ctx, threadID)
	if err != nil {
		return nil, err
	}
	// 会话未设置过人设（空串）或显式无人设（none）时不需要校验目录：清空和保持无人设都合法。
	if persona != "" && persona != personaNoneID {
		svc, ok := backendService[personaLister](d.backend, personaServiceName)
		if !ok {
			return nil, ExtNotImplemented()
		}
		if err := ensurePersonaKnown(ctx, svc, current, strings.TrimSpace(p.Provider), strings.TrimSpace(p.Model), persona); err != nil {
			return nil, err
		}
	}
	// 只替换 Persona，其余字段照抄会话当前值（与桌面端 composerPrefsOf 的组装方式一致）。
	next := ThreadComposerPrefs{
		InteractionMode: current.InteractionMode,
		Effort:          derefString(current.Effort),
		BeesProvider:    derefString(current.SwarmBeesProvider),
		BeesModel:       derefString(current.SwarmBeesModel),
		BeesEffort:      derefString(current.SwarmBeesEffort),
		Persona:         persona,
	}
	summary, err := writer.SetThreadComposerPrefs(ctx, threadID, next)
	if err != nil {
		return nil, err
	}
	// MemoryBackend.SetThreadComposerPrefs 只做 hub.Publish，不触发 threadsChangedHook，
	// 这里补一次广播，客户端才能收到 threads.changed 并刷新人设标签。
	d.broadcastThreadsChanged(wire.ThreadsChanged{Threads: []wire.ThreadSummary{summary}})
	return summary, nil
}

// ensurePersonaKnown 确认 persona 在候选目录里存在。
// 候选的 provider/model 先是客户端拉列表时用的那一组（见 wire.ThreadsSetPersonaParams）：
// 弹窗里的模型选择要等下一次发送才写回会话，只按会话自己的值校验会把刚在列表里看过的人设
// 判成未知；会话自己那一组再补查一次，两边指向同一端点时就是一次调用。
// 所有候选都读不到目录（不是 Magpie 端点、网络或鉴权失败）时放行，与桌面端一致：
// 桌面端只按当前列表渲染，点选后直接写回，不做事后校验。
func ensurePersonaKnown(ctx context.Context, svc personaLister, current wire.ThreadSummary, provider, model, persona string) error {
	if provider == "" {
		provider, model = current.Provider, derefString(current.Model)
	}
	pairs := [][2]string{{provider, model}}
	if ownProvider, ownModel := current.Provider, derefString(current.Model); provider != ownProvider || model != ownModel {
		pairs = append(pairs, [2]string{ownProvider, ownModel})
	}
	readable := false
	for _, pair := range pairs {
		personas, err := svc.Personas(ctx, pair[0], pair[1], false)
		if err != nil {
			return err
		}
		if len(personas) == 0 {
			continue
		}
		readable = true
		if hasPersona(personas, persona) {
			return nil
		}
	}
	if !readable {
		return nil
	}
	return ExtInvalidParams("Invalid params: unknown persona " + persona)
}

// hasPersona 判断人设 id 是否在列表里。
func hasPersona(personas []wire.Persona, id string) bool {
	for _, item := range personas {
		if item.ID == id {
			return true
		}
	}
	return false
}
