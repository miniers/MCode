package app

import (
	"context"
	"errors"

	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
	"mcode/internal/swarm"
)

// 本文件把桌面端的「重启中断续跑」与失败子代理「沿用改动续做」能力注入远程扩展方法
// （internal/remote/swarm_ext_methods.go）。桌面端横幅读的是 Client 状态里的 Interrupted 计数
// （internal/ui/client.go），移动端拿不到这个字段，所以这里按 Manager 的快照算出同样的计数与 bee 明细。

// remoteSwarmExt 把 *Client 的续跑能力适配为远程协议接口。
type remoteSwarmExt struct {
	client *Client
}

var _ remote.SwarmExtService = remoteSwarmExt{}

func init() {
	registerRemoteWiring(func(a *App) {
		if a == nil || a.backend == nil {
			return
		}
		client := a.remoteClient()
		if client == nil {
			return
		}
		a.backend.SetService("swarmExt", remoteSwarmExt{client: client})
	})
}

// Interrupted 返回主会话下因重启中断的条目总数与其中的 bee 明细。
// 计数与桌面端一致（bee 中断数 + 重启前的排队条目数），因此可能大于 Bees 的长度。
func (x remoteSwarmExt) Interrupted(ctx context.Context, leadThreadID string) ([]wire.SwarmExtInterruptedBee, int, error) {
	m, err := x.manager()
	if err != nil {
		return nil, 0, err
	}
	st := m.State(x.leadID(m, leadThreadID))
	bees := make([]wire.SwarmExtInterruptedBee, 0, len(st.Bees))
	for _, bee := range st.Bees {
		if !bee.Interrupted || bee.Status != swarm.StatusStopped {
			continue
		}
		bees = append(bees, wire.SwarmExtInterruptedBee{
			ThreadID: bee.ThreadID,
			Name:     bee.Name,
			Task:     bee.Task,
		})
	}
	return bees, st.Interrupted, nil
}

// ResumeInterrupted 续跑被打断的子代理并把重启前的排队条目重新入队。
func (x remoteSwarmExt) ResumeInterrupted(ctx context.Context, leadThreadID string) (int, error) {
	m, err := x.manager()
	if err != nil {
		return 0, err
	}
	return x.client.ResumeInterruptedSwarm(ctx, x.leadID(m, leadThreadID))
}

// DismissInterrupted 放弃续跑：只清中断标记与排队条目，不删除任何文件。
func (x remoteSwarmExt) DismissInterrupted(ctx context.Context, leadThreadID string) (int, error) {
	m, err := x.manager()
	if err != nil {
		return 0, err
	}
	return x.client.DismissInterruptedSwarm(ctx, x.leadID(m, leadThreadID))
}

// RetryBee 沿用指定失败或已停止子代理的原 worktree 与半成品改动重新派发一个子会话。
// threadID 可能是 bee 自己的会话，先按 BeeOwner 归一成所属主会话再调用。
func (x remoteSwarmExt) RetryBee(ctx context.Context, threadID, beeThreadID string) error {
	m, err := x.manager()
	if err != nil {
		return err
	}
	return x.client.RetrySwarmBee(ctx, x.leadID(m, threadID), beeThreadID)
}

// ReportFull 读取被截断汇报的全文，读取路径与桌面端展开卡片完全一致（Client.SwarmReportSummary）。
// 归属先按存档里记录的 lead 校验，再用主会话的 bee 列表兜底：归属不合法与存档缺失返回两种不同的错误码，
// 另从主会话的 bee 列表里取 bee 名字。
func (x remoteSwarmExt) ReportFull(ctx context.Context, threadID, beeID string) (wire.SwarmExtReportResult, error) {
	m, err := x.manager()
	if err != nil {
		return wire.SwarmExtReportResult{}, err
	}
	lead := x.leadID(m, threadID)
	name, belongs := x.beeInfo(m, lead, beeID)
	rec, archived, err := x.reportArchive(ctx, beeID)
	if err != nil {
		return wire.SwarmExtReportResult{}, err
	}
	// 先做归属校验：存档记的是别的主会话，或既不在 bee 列表里也没有存档，都按参数错误拒绝。
	if (archived && rec.LeadThreadID != lead) || (!archived && !belongs) {
		return wire.SwarmExtReportResult{}, remote.ExtInvalidParams("Invalid params: 'beeID' does not belong to 'threadID'")
	}
	text, err := x.client.SwarmReportSummary(ctx, lead, beeID)
	if err != nil {
		// 归属已经校验通过，剩下的只可能是存档读不到。
		return wire.SwarmExtReportResult{}, &remote.ExtError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: "找不到这条子代理的完整汇报",
		}
	}
	if name == "" {
		name = rec.Name
	}
	return wire.SwarmExtReportResult{BeeID: beeID, Name: name, Text: text, Truncated: false}, nil
}

// reportArchive 读取 bee 的汇报存档用于在读全文前校验归属；没有存档时 archived 为 false。
func (x remoteSwarmExt) reportArchive(ctx context.Context, beeID string) (store.SwarmReport, bool, error) {
	if x.client == nil || x.client.backend == nil {
		return store.SwarmReport{}, false, nil
	}
	st := x.client.backend.Store()
	if st == nil {
		return store.SwarmReport{}, false, nil
	}
	rec, err := st.GetSwarmReport(ctx, beeID)
	if errors.Is(err, store.ErrNotFound) {
		return store.SwarmReport{}, false, nil
	}
	if err != nil {
		return store.SwarmReport{}, false, err
	}
	return rec, true, nil
}

// beeInfo 在主会话的 bee 列表里查 bee 名字，并返回它是否属于该主会话。
func (x remoteSwarmExt) beeInfo(m *swarm.Manager, leadThreadID, beeID string) (string, bool) {
	for _, bee := range m.State(leadThreadID).Bees {
		if bee.ThreadID == beeID {
			return bee.Name, true
		}
	}
	return "", false
}

// manager 取出当前的编排管理器；客户端未就绪时返回错误，由 RPC 层转成可读的拒绝错误。
func (x remoteSwarmExt) manager() (*swarm.Manager, error) {
	if x.client == nil {
		return nil, errors.New("编排服务不可用")
	}
	x.client.kanbanMu.Lock()
	m := x.client.swarm
	x.client.kanbanMu.Unlock()
	if m == nil {
		return nil, errors.New("编排服务不可用")
	}
	return m, nil
}

// leadID 把 bee 线程归一成所属主会话：移动端可能从子代理会话里发起续跑。
func (x remoteSwarmExt) leadID(m *swarm.Manager, threadID string) string {
	if lead, ok := m.BeeOwner(threadID); ok && lead != "" {
		return lead
	}
	return threadID
}
