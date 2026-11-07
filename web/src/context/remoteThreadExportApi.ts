// 会话导出用的历史全量拉取：宿主 threads.history 每次只给一页（默认 50 条，从最新往回翻），
// 这里循环翻页拼成按时间升序的完整数组；任何一页失败都整体 reject，不返回半份历史。
// 与 remoteMessageSearchApi 同构：纯函数接收 useRemoteCall 的 call，hook 只是薄包装。

import { useCallback } from 'react';
import type { RemoteThreadHistoryPage, RemoteThreadMessage } from '../types/generated';
import { useRemoteCall, type UseRemoteCallResult } from './useRemoteCall';

export const THREAD_HISTORY_METHOD = 'threads.history';

/** 单页请求条数：宿主默认 50，导出时取大一点减少往返。 */
const HISTORY_PAGE_LIMIT = 100;
/** 页数上限，防止宿主 nextCursor 异常时死循环（100 页 = 1 万条消息）。 */
const HISTORY_MAX_PAGES = 100;

const ascending = (messages: RemoteThreadMessage[]): RemoteThreadMessage[] =>
  messages
    .map((message, index) => ({ message, index, at: Date.parse(message.createdAt) }))
    .sort((a, b) => {
      const atA = Number.isNaN(a.at) ? 0 : a.at;
      const atB = Number.isNaN(b.at) ? 0 : b.at;
      // 时间相同保持宿主给的原始顺序（稳定排序）
      return atA === atB ? a.index - b.index : atA - atB;
    })
    .map((entry) => entry.message);

/**
 * 拉取会话的完整历史：先取最新一页，再按 nextCursor 往回翻，
 * 每页消息稳定升序拼在前一页之前，最后按 createdAt 稳定排序并去重。
 * cursor 不前进或 hasMore 为 false 时停止；页数超过上限按已取到的部分返回。
 */
export async function loadFullThreadHistory(
  call: UseRemoteCallResult['call'],
  threadID: string
): Promise<RemoteThreadMessage[]> {
  if (!threadID) throw new Error('缺少会话 ID，无法导出');
  let cursor: string | null = null;
  const collected: RemoteThreadMessage[] = [];
  for (let page = 0; page < HISTORY_MAX_PAGES; page += 1) {
    const params: { threadID: string; cursor?: string; limit: number } = {
      threadID,
      limit: HISTORY_PAGE_LIMIT
    };
    if (cursor) params.cursor = cursor;
    const result = await call<RemoteThreadHistoryPage>(THREAD_HISTORY_METHOD, params, 30000);
    if (!result || !Array.isArray(result.messages)) {
      throw new Error('读取会话历史失败：返回结构不正确');
    }
    // 页内本身就是升序，整页插到已取内容之前即可保持时间顺序
    collected.unshift(...result.messages);
    if (!result.hasMore || !result.nextCursor) break;
    if (result.nextCursor === cursor) break;
    cursor = result.nextCursor;
  }

  const seen = new Set<string>();
  return ascending(collected).filter((message) => {
    if (seen.has(message.id)) return false;
    seen.add(message.id);
    return true;
  });
}

/**
 * 导出入口用的 hook：宿主未声明 threads.history 时直接给中文报错，
 * 其余情况交给 loadFullThreadHistory 翻页。
 */
export function useThreadExportHistory(): {
  loadFullHistory: (threadID: string) => Promise<RemoteThreadMessage[]>;
} {
  const { call, supportsMethod } = useRemoteCall();
  const loadFullHistory = useCallback(
    async (threadID: string): Promise<RemoteThreadMessage[]> => {
      if (!supportsMethod(THREAD_HISTORY_METHOD)) {
        throw new Error(`当前宿主未声明 ${THREAD_HISTORY_METHOD} 能力，无法导出会话`);
      }
      return loadFullThreadHistory(call, threadID);
    },
    [call, supportsMethod]
  );
  return { loadFullHistory };
}
