// 本文件包含 MemoryBackend 的Swarm 状态与配置方法。
package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"mcode/internal/remote/wire"
	"mcode/internal/swarm"
	"strings"
)

// SetSwarm 设置 Swarm 能力可用性及会话启用判断函数。
func (b *MemoryBackend) SetSwarm(available bool, enabledFor func(threadID string) bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.swarmAvailable = available
	b.swarmEnabledFor = enabledFor
}

// SetRevertBusyCheck 设置撤销前的额外占用检查：返回非空字符串表示该工作区正被占用，
// 内容作为拒绝撤销的原因展示给用户。未注入时撤销只检查会话自身的运行状态。
func (b *MemoryBackend) SetRevertBusyCheck(fn func(threadID, root string) string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.revertBusyCheck = fn
}

// SetSwarmLeadPlansOnlyFunc 设置主代理只规划约束的判断函数。
func (b *MemoryBackend) SetSwarmLeadPlansOnlyFunc(fn func(threadID string) bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.swarmLeadPlansOnlyFor = fn
}

// SetSwarmEnabler 设置新建会话时立即开启编排的回调（与 threads.swarm.set 同一后端实现）。
// 未注入时新建会话只把编排开关记在摘要里，等客户端自行调用 threads.swarm.set。
func (b *MemoryBackend) SetSwarmEnabler(fn func(threadID string, enabled bool)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.swarmEnablerFunc = fn
}

// SetSwarmInheritor 设置转发会话时继承编排状态的回调：新会话从源会话继承编排开关与只规划覆盖值。
// 未注入时转发出的新会话不继承编排状态。
func (b *MemoryBackend) SetSwarmInheritor(fn func(fromThreadID, toThreadID string)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.swarmInheritFunc = fn
}

// SetSwarmBusy 设置 Swarm 忙闲判定函数：返回 true 表示该主会话名下仍有
// 未结束的子线程，或仍有待投递给它的子线程回报。
func (b *MemoryBackend) SetSwarmBusy(busy func(threadID string) bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.swarmBusyFor = busy
}

// SetSwarmQueueHooks 设置 Swarm 队列决策钩子。
func (b *MemoryBackend) SetSwarmQueueHooks(hasWorkingBees func(threadID string) bool, sendToBee func(ctx context.Context, threadID, text string) (string, error)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.swarmHasWorkingBees = hasWorkingBees
	b.swarmSendToBee = sendToBee
}

func (b *MemoryBackend) hasWorkingSwarmBees(threadID string) bool {
	b.mu.Lock()
	fn := b.swarmHasWorkingBees
	b.mu.Unlock()
	return fn != nil && fn(threadID)
}

// SetThreadSwarmMode 更新会话级 Swarm 工作模式并写穿到 store。仅供桌面端直接调用，不注册为 RPC。
// 空值或未知模式归一为 inherited（跟随全局设置）。
func (b *MemoryBackend) SetThreadSwarmMode(ctx context.Context, threadID, mode string) (wire.ThreadSummary, error) {
	mode = swarm.NormalizeMode(mode)

	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return wire.ThreadSummary{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}
	// 先落盘再更新内存，保存失败不得向界面报告成功；锁内写入保证并发更新顺序一致。
	if b.store != nil {
		if err := b.store.UpdateThreadSwarmMode(ctx, threadID, mode); err != nil {
			b.mu.Unlock()
			return wire.ThreadSummary{}, err
		}
	}
	td.summary.SwarmMode = mode
	currentSummary := b.summaryForPublishLocked(td)
	b.mu.Unlock()

	b.hub.Publish(threadID, nil, nil, currentSummary)

	return currentSummary, nil
}

// SwarmThreadContext 在持锁状态下由新到旧扫描会话消息，寻找首条文本不是编排通知且 Trim 后不以 / 开头的 user 角色消息；
// 返回其文本内容、会话生效的配对 ID（显式选择优先，否则按模型推断）、会话存储的档位覆盖 JSON，
// 以及会话里选的子线程默认服务商、模型与推理强度。
func (b *MemoryBackend) SwarmThreadContext(threadID string) (userMessage, pairID, beeTiers, beeProvider, beeModel, beeEffort string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	td, ok := b.threads[threadID]
	if !ok {
		return "", "", "", "", "", ""
	}
	_ = b.ensureMessagesLocked(td, threadID)
	b.ensureMessagesLoadedLocked(td)

	beeTiers = td.summary.SwarmBeeTiers
	if td.summary.SwarmBeesProvider != nil {
		beeProvider = *td.summary.SwarmBeesProvider
	}
	if td.summary.SwarmBeesModel != nil {
		beeModel = *td.summary.SwarmBeesModel
	}
	beeEffort = derefString(td.summary.SwarmBeesEffort)
	// 生效配对在读取时计算，不回写数据库：历史会话没绑配对时也能按模型套用配对里的档位映射。
	swarmCfg := b.swarmConfig()
	pairID = effectivePairID(swarmPairsFromConfig(swarmCfg.Pairs), derefString(td.summary.SwarmPairID),
		td.summary.Provider, derefString(td.summary.Model),
		trimOr(beeProvider, swarmCfg.DefaultProvider), trimOr(beeModel, swarmCfg.DefaultModel))

	for i := len(td.messages) - 1; i >= 0; i-- {
		m := td.messages[i]
		if m.Role != "user" {
			continue
		}
		text := m.Text
		if swarm.IsNotice(text) || strings.HasPrefix(strings.TrimSpace(text), "/") {
			continue
		}
		userMessage = text
		break
	}
	return userMessage, pairID, beeTiers, beeProvider, beeModel, beeEffort
}

// SwarmProviderEnabled 判断指定 Provider 是否已启用且可用，判定依据与 ListProviders 保持一致。
func (b *MemoryBackend) SwarmProviderEnabled(providerID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	h, ok := b.harnesses[providerID]
	if !ok {
		return false
	}
	if rep, ok := h.(availabilityReporter); ok {
		available, _ := rep.Available()
		return available
	}
	return true
}

// SetThreadBeeTiers 校验 beeTiers（必须为空或合法 JSON 对象），通过 store 的 SetThreadBeeTiers 持久化，
// 并更新内存中的会话摘要及向前端广播 threads.changed 通知。
func (b *MemoryBackend) SetThreadBeeTiers(ctx context.Context, threadID, beeTiers string) error {
	trimmed := strings.TrimSpace(beeTiers)
	if trimmed != "" {
		var obj map[string]any
		if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
			return &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: "beeTiers must be empty or a valid JSON object",
			}
		}
	} else {
		trimmed = ""
	}

	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}

	if b.store != nil {
		if err := b.store.SetThreadBeeTiers(ctx, threadID, trimmed); err != nil {
			b.mu.Unlock()
			return err
		}
	}

	td.summary.SwarmBeeTiers = trimmed
	currentSummary := b.summaryForPublishLocked(td)
	hook := b.threadsChangedHook
	b.mu.Unlock()

	b.hub.Publish(threadID, nil, nil, currentSummary)
	if hook != nil {
		hook(wire.ThreadsChanged{Threads: []wire.ThreadSummary{currentSummary}})
	}

	return nil
}

// SetThreadSwarmPairID 更新会话的 Swarm 配对 ID。若配对发生实质变更，清空会话的档位覆盖配置。
func (b *MemoryBackend) SetThreadSwarmPairID(ctx context.Context, threadID, pairID string) error {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}

	oldPairID := derefString(td.summary.SwarmPairID)
	pairChanged := (oldPairID != pairID)
	td.summary.SwarmPairID = optionalString(pairID)
	if b.store != nil {
		if err := b.store.SetThreadSwarmPairID(ctx, threadID, pairID); err != nil {
			b.mu.Unlock()
			return err
		}
	}
	if pairChanged {
		td.summary.SwarmBeeTiers = ""
		if b.store != nil {
			if err := b.store.SetThreadBeeTiers(ctx, threadID, ""); err != nil {
				b.mu.Unlock()
				return err
			}
		}
	}
	currentSummary := b.summaryForPublishLocked(td)
	hook := b.threadsChangedHook
	b.mu.Unlock()

	b.hub.Publish(threadID, nil, nil, currentSummary)
	if hook != nil {
		hook(wire.ThreadsChanged{Threads: []wire.ThreadSummary{currentSummary}})
	}

	return nil
}
