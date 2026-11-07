package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"mcode/internal/config"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
)

// remoteSwarmExtras 把桌面端 Client 的 Swarm 能力适配为远程 Dispatcher 的扩展接口。
// 单独成类型而不是直接给 *Client 加方法：Client.SwarmMergeDiff 返回 UI 层结构，签名与协议层不同。
type remoteSwarmExtras struct {
	client *Client
}

var _ remote.SwarmRemoteExtras = remoteSwarmExtras{}

// PendingBeeQuestion 返回 bee 线程当前待回答的提问；没有提问时返回 nil。
func (x remoteSwarmExtras) PendingBeeQuestion(ctx context.Context, beeThreadID string) *wire.SwarmBeeQuestion {
	q := x.client.pendingBeeQuestion(ctx, beeThreadID)
	if q == nil {
		return nil
	}

	res := &wire.SwarmBeeQuestion{
		RequestID: q.RequestID,
		Forwarded: q.Forwarded,
	}
	// 协议只需要第一道题面，多题交给桌面端处理。
	for _, question := range q.Questions {
		if prompt := strings.TrimSpace(question.Prompt); prompt != "" {
			res.Prompt = prompt
			break
		}
	}
	if !q.AutoForwardAt.IsZero() {
		res.AutoForwardAt = q.AutoForwardAt.UTC().Format(time.RFC3339)
	}
	return res
}

// ForwardBeeQuestion 把 bee 的提问转交主线程回答。
func (x remoteSwarmExtras) ForwardBeeQuestion(ctx context.Context, leadThreadID, beeThreadID, requestID string) error {
	return x.client.ForwardBeeQuestion(ctx, leadThreadID, beeThreadID, requestID)
}

// SwarmMergeDiff 返回 bee 待审查的补丁、是否被截断与改动文件列表。
func (x remoteSwarmExtras) SwarmMergeDiff(ctx context.Context, leadThreadID, beeThreadID string) (string, bool, []string, error) {
	diff, err := x.client.SwarmMergeDiff(ctx, leadThreadID, beeThreadID)
	if err != nil {
		return "", false, nil, err
	}
	return diff.Diff, diff.Truncated, diff.FilesChanged, nil
}

// ApproveSwarmMerge 批准并应用 bee 的改动。
func (x remoteSwarmExtras) ApproveSwarmMerge(ctx context.Context, leadThreadID, beeThreadID string) error {
	return x.client.ApproveSwarmMerge(ctx, leadThreadID, beeThreadID)
}

// DiscardSwarmMerge 放弃 bee 的改动。
func (x remoteSwarmExtras) DiscardSwarmMerge(ctx context.Context, leadThreadID, beeThreadID string) error {
	return x.client.DiscardSwarmMerge(ctx, leadThreadID, beeThreadID)
}

// SetSwarmPlansOnly 设置主会话的「只规划」覆盖；v 为 nil 表示清除覆盖、回到全局设置。
func (x remoteSwarmExtras) SetSwarmPlansOnly(ctx context.Context, threadID string, v *bool) error {
	c := x.client
	c.kanbanMu.Lock()
	m := c.swarm
	c.kanbanMu.Unlock()
	if m == nil {
		return errors.New("编排服务不可用")
	}

	leadID := threadID
	if lead, ok := m.BeeOwner(threadID); ok {
		leadID = lead
	}
	m.SetLeadPlansOnly(leadID, v)
	return nil
}

// SwarmPlansOnly 返回主会话当前生效的「只规划」取值，以及该值是否来自会话级覆盖。
func (x remoteSwarmExtras) SwarmPlansOnly(leadThreadID string) (bool, bool) {
	c := x.client
	c.kanbanMu.Lock()
	m := c.swarm
	c.kanbanMu.Unlock()

	if m != nil {
		if po := m.State(leadThreadID).PlansOnly; po != nil {
			return *po, true
		}
	}
	return c.globalLeadPlansOnly(), false
}

// SwarmTiers 返回配置里的 Bee 档位列表，与桌面设置页读到的是同一份配置。
func (x remoteSwarmExtras) SwarmTiers() []wire.SwarmTier {
	return swarmTiersToWire(x.client.swarmConfig().Orchestration.Tiers)
}

// SaveSwarmTiers 整表替换档位配置，校验规则与桌面端设置页一致：先在副本上 config.Validate，
// 失败直接返回错误且不落盘；成功则有同一套 Normalize/Validate 经 config.Update 原子写入。
func (x remoteSwarmExtras) SaveSwarmTiers(ctx context.Context, tiers []wire.SwarmTier) ([]wire.SwarmTier, error) {
	dir := x.client.configDir()
	if dir == "" {
		return nil, errors.New("配置目录不可用")
	}

	next := swarmTiersFromWire(tiers)
	candidate := x.client.swarmConfig()
	candidate.Orchestration.Tiers = next
	if err := config.Validate(candidate); err != nil {
		return nil, err
	}
	if err := config.Update(dir, func(latest *config.Config) error {
		latest.Orchestration.Tiers = next
		return nil
	}); err != nil {
		return nil, err
	}

	// 返回落盘并归一化后的档位，调用方拿到的就是桌面端接下来会读到的配置。
	return swarmTiersToWire(x.client.swarmConfig().Orchestration.Tiers), nil
}

// swarmConfig 读取数据目录里的当前配置并应用环境变量覆盖，与桌面端运行期读到的一致。
func (c *Client) swarmConfig() config.Config {
	if dir := c.configDir(); dir != "" {
		if cfg, err := config.Load(dir); err == nil {
			config.ApplyEnv(&cfg, os.LookupEnv)
			return cfg
		}
	}
	cfg := config.Defaults()
	config.ApplyEnv(&cfg, os.LookupEnv)
	return cfg
}

// configDir 返回 Client 实际使用的数据目录，未注入时回退到远程服务的数据目录。
func (c *Client) configDir() string {
	if c.dataDir != "" {
		return c.dataDir
	}
	if c.remote != nil {
		return c.remote.dataDir
	}
	return ""
}

// swarmTiersToWire 把配置里的档位转换为协议结构。
func swarmTiersToWire(tiers []config.SwarmTier) []wire.SwarmTier {
	out := make([]wire.SwarmTier, 0, len(tiers))
	for _, t := range tiers {
		out = append(out, wire.SwarmTier{
			ID:           t.ID,
			Name:         t.Name,
			Description:  t.Description,
			ReadOnly:     t.ReadOnly,
			WritePaths:   append([]string(nil), t.WritePaths...),
			Provider:     t.Provider,
			Model:        t.Model,
			Effort:       t.Effort,
			FastMode:     t.FastMode,
			SystemPrompt: t.SystemPrompt,
		})
	}
	return out
}

// swarmTiersFromWire 把协议结构转换为配置结构。
func swarmTiersFromWire(tiers []wire.SwarmTier) []config.SwarmTier {
	out := make([]config.SwarmTier, 0, len(tiers))
	for _, t := range tiers {
		out = append(out, config.SwarmTier{
			ID:           t.ID,
			Name:         t.Name,
			Description:  t.Description,
			ReadOnly:     t.ReadOnly,
			WritePaths:   append([]string(nil), t.WritePaths...),
			Provider:     t.Provider,
			Model:        t.Model,
			Effort:       t.Effort,
			FastMode:     t.FastMode,
			SystemPrompt: t.SystemPrompt,
		})
	}
	return out
}
