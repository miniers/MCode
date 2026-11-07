// 被截断子代理汇报的全文读取（threads.swarm.report），对应宿主 internal/app/remote_swarm_ext.go 的 ReportFull，
// 与桌面端展开卡片走同一条存档读取路径（Client.SwarmReportSummary）。
// 类型来自 web/src/types/generated.ts（由 docs/remote-control.schema.json 生成）。
// 全文按 threadID + beeID 缓存在模块级 Map 里：同一条汇报只拉一次，切会话回来也不重复请求。

import { useCallback, useMemo } from 'react';
import type {
  RemoteThreadsSwarmReportParams,
  RemoteThreadsSwarmReportResult
} from '../types/generated';
import { useRemoteCall } from './useRemoteCall';

export type { RemoteThreadsSwarmReportParams, RemoteThreadsSwarmReportResult };

export const REMOTE_SWARM_REPORT_METHOD = 'threads.swarm.report';

/** 全文缓存的最大条目数：超过后整表清空重来，长时间开着窗口也不会无限增长。 */
const SWARM_REPORT_FULL_MAX_CACHE = 256;

const swarmReportFullCache = new Map<string, string>();

function swarmReportFullKey(threadID: string, beeID: string): string {
  return `${threadID}\u0000${beeID}`;
}

/** 取已经拉到内存的汇报全文；没拉到过返回 null。 */
export function getCachedSwarmReportFull(threadID: string, beeID: string): string | null {
  if (!threadID || !beeID) return null;
  return swarmReportFullCache.get(swarmReportFullKey(threadID, beeID)) ?? null;
}

/** 记住一份汇报全文；空字符串不缓存，读不到时下次还能再试。 */
export function setCachedSwarmReportFull(threadID: string, beeID: string, text: string): void {
  if (!threadID || !beeID || !text) return;
  const key = swarmReportFullKey(threadID, beeID);
  if (swarmReportFullCache.size >= SWARM_REPORT_FULL_MAX_CACHE && !swarmReportFullCache.has(key)) {
    swarmReportFullCache.clear();
  }
  swarmReportFullCache.set(key, text);
}

/** 汇报全文读取的远程调用集合。 */
export interface RemoteSwarmReportApi {
  /** 宿主是否提供汇报全文读取；为假时不显示「展开全文」按钮。 */
  supportsReportFull: boolean;
  /** 读取指定 bee 的完整汇报；存档缺失时宿主返回 -32002 与可读原文。 */
  fetchReportFull: (params: RemoteThreadsSwarmReportParams) => Promise<RemoteThreadsSwarmReportResult>;
}

/** 取汇报全文读取的调用集合；能力与 call 不变时返回同一对象。 */
export function useRemoteSwarmReportApi(): RemoteSwarmReportApi {
  const { call, supportsMethod } = useRemoteCall();
  const supportsReportFull = supportsMethod(REMOTE_SWARM_REPORT_METHOD);

  const fetchReportFull = useCallback(
    (params: RemoteThreadsSwarmReportParams) =>
      call<RemoteThreadsSwarmReportResult>(REMOTE_SWARM_REPORT_METHOD, params),
    [call]
  );

  return useMemo<RemoteSwarmReportApi>(
    () => ({ supportsReportFull, fetchReportFull }),
    [supportsReportFull, fetchReportFull]
  );
}
