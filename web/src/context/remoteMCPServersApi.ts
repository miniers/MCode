// MCP 服务器列表与逐会话启停的远程调用封装，对应宿主 internal/remote/mcp_ext_methods.go
// 与 threads.mcpDisabled.set（internal/remote/thread_ops.go）。
// servers 里的 key 就是 threads.mcpDisabled.set 使用的键（agent:scope:name），disabled 表示本会话禁用名单命中。
// 类型来自 web/src/types/generated.ts（由 docs/remote-control.schema.json 生成）。

import { useCallback, useMemo } from 'react';
import type {
  RemoteMCPExtListServersResult,
  RemoteMCPExtServer,
  RemoteThreadsMCPDisabledSetResult
} from '../types/generated';
import { makeActionID } from './remoteGuards';
import { useRemoteCall } from './useRemoteCall';

export type { RemoteMCPExtListServersResult, RemoteMCPExtServer };

export const REMOTE_MCP_SERVERS_LIST_METHOD = 'mcp.servers.list';
export const REMOTE_THREADS_MCP_DISABLED_SET_METHOD = 'threads.mcpDisabled.set';

/** 列表的查询目标：优先按会话（带出该会话的禁用状态），没有会话时退回按项目。 */
export interface MCPServersTarget {
  threadID?: string;
  projectID?: string;
}

/** 与桌面端 mcpDisabledKeys 一致：去重后升序，空数组表示全部启用。 */
const normalizeDisabledKeys = (keys: string[]): string[] => {
  const unique = new Set<string>();
  for (const key of keys) {
    const trimmed = key.trim();
    if (trimmed) unique.add(trimmed);
  }
  return Array.from(unique).sort();
};

/** MCP 服务器的远程调用集合：能力判断按方法逐个进行，宿主没声明的方法上层不显示入口。 */
export interface RemoteMCPServersApi {
  /** 宿主是否提供 mcp.servers.list。 */
  supportsList: boolean;
  /** 宿主是否提供 threads.mcpDisabled.set；为假时列表只能看不能改。 */
  supportsSetDisabled: boolean;
  /** 拉取服务器列表；threadID 优先，宿主按会话带出 disabled 状态。 */
  listServers: (target: MCPServersTarget) => Promise<RemoteMCPExtServer[]>;
  /** 整体替换会话的 MCP 禁用名单（宿主语义是覆盖，不是追加）。 */
  setDisabled: (threadID: string, keys: string[]) => Promise<string[]>;
}

/** 取 MCP 服务器的调用集合；能力未变化前返回同一对象。 */
export function useRemoteMCPServersApi(): RemoteMCPServersApi {
  const { call, supportsMethod } = useRemoteCall();
  const supportsList = supportsMethod(REMOTE_MCP_SERVERS_LIST_METHOD);
  const supportsSetDisabled = supportsMethod(REMOTE_THREADS_MCP_DISABLED_SET_METHOD);

  const listServers = useCallback(
    async (target: MCPServersTarget): Promise<RemoteMCPExtServer[]> => {
      const params: MCPServersTarget = {};
      if (target.threadID) params.threadID = target.threadID;
      else if (target.projectID) params.projectID = target.projectID;
      const result = await call<RemoteMCPExtListServersResult>(REMOTE_MCP_SERVERS_LIST_METHOD, params);
      return Array.isArray(result?.servers) ? result.servers : [];
    },
    [call]
  );

  const setDisabled = useCallback(
    async (threadID: string, keys: string[]): Promise<string[]> => {
      const result = await call<RemoteThreadsMCPDisabledSetResult>(REMOTE_THREADS_MCP_DISABLED_SET_METHOD, {
        threadID,
        keys: normalizeDisabledKeys(keys),
        actionID: makeActionID()
      });
      return Array.isArray(result?.keys) ? result.keys : [];
    },
    [call]
  );

  return useMemo<RemoteMCPServersApi>(
    () => ({ supportsList, supportsSetDisabled, listServers, setDisabled }),
    [listServers, setDisabled, supportsList, supportsSetDisabled]
  );
}
