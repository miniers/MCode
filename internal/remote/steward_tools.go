package remote

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"mcode/internal/agentconf"
	"mcode/internal/store"
)

// 本文件实现总管（steward）会话与话题会话的审批豁免：两者都是常驻的只读会话，它们的
// 工作全部通过内置 mcode MCP 服务器上的工具完成（总管做分诊，话题会话写任务书、做验收），
// 而在只读档下这些工具本来会被一律拒绝或卡在人工审批上。
//
// 安全性由内置服务器自己保证：internal/mcp/server.go 的 handleToolCall 对总管会话与话题会话
// 各按一份白名单拦截（stewardScopedCallAllowed），越权工具按未知工具拒绝，所以这里只按
// 「会话身份 + 服务器名」放行，写文件、执行命令与第三方 MCP 服务器的工具都不在这条豁免里。

const (
	// stewardThreadSettingKey 是宿主保存总管的会话 ID 的设置键，
	// 与 internal/steward 的 settingThreadID 保持一致。
	stewardThreadSettingKey = "steward.thread_id"
	// stewardTopicsSettingKey 是宿主保存话题表的设置键，与 internal/steward 的 settingTopics 保持一致。
	stewardTopicsSettingKey = "steward.topics"
	// builtinMCPServerName 是内置 mcode MCP 服务器的名字，与 internal/mcp.ServerName 保持一致。
	builtinMCPServerName = "mcode"
	// stewardToolLookupTimeout 是读总管的会话 ID 的超时：审批链路上不能因为存储慢而卡住整轮对话。
	stewardToolLookupTimeout = 2 * time.Second
)

// stewardBuiltinMCPTool 报告 threadID 是总管的会话或某个话题的会话，且 toolName 落在内置 mcode MCP 服务器上。
// 两个条件同时成立才算豁免；工具名不是 mcp__mcode__<tool> 时连存储都不读，普通会话的
// 审批链路上不会多出一次查询。
func (b *MemoryBackend) stewardBuiltinMCPTool(ctx context.Context, threadID, toolName string) bool {
	if b == nil {
		return false
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return false
	}
	server, _, ok := agentconf.SplitMCPToolName(toolName)
	if !ok || server != builtinMCPServerName {
		return false
	}
	if b.stewardMainThread(ctx, threadID) {
		return true
	}

	// 存储读取不持 b.mu：调用点都在主锁之外，Store() 自己加锁取指针。
	st := b.Store()
	if st == nil {
		return false
	}

	lookupCtx, cancel := context.WithTimeout(ctx, stewardToolLookupTimeout)
	defer cancel()
	return stewardTopicThread(lookupCtx, st, threadID)
}

// stewardMainThread 报告 threadID 是不是总管的会话（即宿主记在 stewardThreadSettingKey 里的那个会话）。
// 这里只认总管自己的会话，不认它开出的话题会话：话题会话有各自的豁免范围。
// 与 stewardBuiltinMCPTool 一样，存储读取不持 b.mu，调用点必须在主锁之外。
func (b *MemoryBackend) stewardMainThread(ctx context.Context, threadID string) bool {
	if b == nil {
		return false
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return false
	}
	st := b.Store()
	if st == nil {
		return false
	}

	lookupCtx, cancel := context.WithTimeout(ctx, stewardToolLookupTimeout)
	defer cancel()
	stewardID, found, err := st.GetSetting(lookupCtx, stewardThreadSettingKey)
	return err == nil && found && strings.TrimSpace(stewardID) == threadID
}

// stewardTopicThread 报告 threadID 是不是话题表里的会话。话题会话是总管开的一次性会话，
// 写任务书、验收任务、开工全靠内置 MCP 工具，没有这条豁免它们会在只读档下被一律拒绝。
// 这里只认 ThreadID，与 internal/steward 的 Topic 结构保持同一个 json 字段名。
func stewardTopicThread(ctx context.Context, st *store.Store, threadID string) bool {
	raw, found, err := st.GetSetting(ctx, stewardTopicsSettingKey)
	if err != nil || !found {
		return false
	}
	var topics []struct {
		ThreadID string `json:"threadId"`
	}
	if err := json.Unmarshal([]byte(raw), &topics); err != nil {
		return false
	}
	for _, topic := range topics {
		if strings.TrimSpace(topic.ThreadID) == threadID {
			return true
		}
	}
	return false
}

// MCPToolAllowed 实现 harness.MCPToolAllower：总管与话题会话调用内置 mcode MCP 服务器上的工具时
// 在权限模式之外直接放行，安全性由内置服务器的白名单保证。其他会话与其他服务器的工具一律返回
// false，调用方继续走原来的权限判定。
func (a *threadApprover) MCPToolAllowed(ctx context.Context, toolName string) bool {
	if a == nil || a.b == nil {
		return false
	}
	return a.b.stewardBuiltinMCPTool(ctx, a.threadID, toolName)
}
