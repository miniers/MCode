/**
 * @ 会话引用的候选列表与发送前展开。
 *
 * 候选来自 RemoteContext 已同步的会话列表；展开发生在客户端（宿主不做展开），
 * 与桌面端一致：先按触发器替换链接，再拼上被引用会话的背景资料块。
 */
import { useCallback, useMemo } from 'react';
import { useRemote } from '../context/RemoteContext';
import type { RemoteThreadHistoryPage, RemoteThreadMessage, RemoteThreadSummary } from '../types/generated';
import {
  expandDraftThreadMentions,
  recentThreadMentionItems,
  type DraftThreadMention,
  type ThreadMentionCandidate,
  type ThreadMentionHistoryLoader,
  type ThreadMentionHistoryMessage
} from '../utils/threadMention';

/** 每页拉取的历史条数（与 ThreadView 的分页上限同量级）。 */
export const THREAD_MENTION_HISTORY_PAGE_SIZE = 100;
/** 每个被引用会话最多翻多少页，避免一个引用拖住发送。 */
export const THREAD_MENTION_HISTORY_MAX_PAGES = 5;

/** 拉取会话历史用的 RPC 形状（RemoteContext.getThreadHistory / rpc.ts 的同名方法）。 */
export type ThreadHistoryFetcher = (
  threadID: string,
  cursor?: string | null,
  limit?: number
) => Promise<RemoteThreadHistoryPage>;

export interface UseThreadMentionCandidatesOptions {
  /** 用户输入的触发词（补全面板里 @ 之后的文本）。 */
  query: string;
  /** 候选条数上限，默认 5（桌面端 recentThreadMentionItems 的默认值）。 */
  limit?: number;
  /** 要排除的会话（当前会话）；不传时用 currentThread.id。 */
  excludeThreadID?: string | null;
  /** 所属项目；不传时依次回退 currentThread.projectID、selectedProjectID。 */
  projectID?: string | null;
  /** 相对时间的基准时刻，便于测试；默认取当前时间。 */
  now?: number;
}

/** 输入框 @ 补全用的会话候选。 */
export function useThreadMentionCandidates({
  query,
  limit,
  excludeThreadID,
  projectID,
  now
}: UseThreadMentionCandidatesOptions): ThreadMentionCandidate[] {
  const { threads, currentThread, selectedProjectID } = useRemote();
  const resolvedProjectID = projectID ?? currentThread?.projectID ?? selectedProjectID ?? '';
  const resolvedExcludeID = excludeThreadID ?? currentThread?.id ?? '';

  return useMemo(
    () =>
      recentThreadMentionItems(
        threads,
        resolvedProjectID,
        resolvedExcludeID,
        query,
        now ?? Date.now(),
        limit
      ),
    [threads, resolvedProjectID, resolvedExcludeID, query, now, limit]
  );
}

/** 拉取单个被引用会话的历史：失败返回 null（跳过该引用块，正文照常发送）。 */
async function fetchThreadMentionHistory(
  threadID: string,
  fetchHistory: ThreadHistoryFetcher,
  maxPages: number
): Promise<ThreadMentionHistoryMessage[] | null> {
  try {
    const pages: RemoteThreadMessage[][] = [];
    let cursor: string | null = null;
    for (let page = 0; page < maxPages; page += 1) {
      const result = await fetchHistory(threadID, cursor, THREAD_MENTION_HISTORY_PAGE_SIZE);
      pages.push(result.messages);
      if (!result.hasMore || !result.nextCursor) break;
      cursor = result.nextCursor;
    }
    // 分页是从最新一页往前翻的，倒序拼回来才是时间正序。
    return pages.reverse().flat();
  } catch {
    return null;
  }
}

/**
 * 先并行拉取所有被引用会话的历史，再返回一个同步 loader 给展开函数用。
 * 拿不到历史的会话返回 null，展开时会跳过它的资料块。
 */
export async function createThreadMentionHistoryLoader(
  threadIDs: ReadonlyArray<string>,
  fetchHistory: ThreadHistoryFetcher,
  maxPages: number = THREAD_MENTION_HISTORY_MAX_PAGES
): Promise<ThreadMentionHistoryLoader> {
  const unique = Array.from(new Set(threadIDs.filter((threadID) => threadID !== '')));
  const cache = new Map<string, ThreadMentionHistoryMessage[] | null>();
  await Promise.all(
    unique.map(async (threadID) => {
      cache.set(threadID, await fetchThreadMentionHistory(threadID, fetchHistory, maxPages));
    })
  );
  return (threadID: string) => cache.get(threadID) ?? null;
}

export interface BuildThreadMentionSendTextParams {
  /** 用户输入框里的草稿正文。 */
  text: string;
  /** 草稿上方的引用胶囊。 */
  mentions: ReadonlyArray<DraftThreadMention>;
  /** 用于取引用标题的会话列表；找不到时退回触发词（与桌面端一致）。 */
  threads?: ReadonlyArray<RemoteThreadSummary>;
  fetchHistory: ThreadHistoryFetcher;
  maxPages?: number;
}

/**
 * 发送前的最终文本：拉取被引用会话历史 → 替换链接 → 拼接资料块。
 * 没有引用时原样返回正文。
 */
export async function buildThreadMentionSendText({
  text,
  mentions,
  threads,
  fetchHistory,
  maxPages
}: BuildThreadMentionSendTextParams): Promise<string> {
  if (mentions.length === 0) return text;

  const loader = await createThreadMentionHistoryLoader(
    mentions.map((mention) => mention.threadID),
    fetchHistory,
    maxPages
  );
  const titleOf = (threadID: string) => threads?.find((thread) => thread.id === threadID)?.title ?? '';
  return expandDraftThreadMentions(text, mentions, titleOf, loader);
}

export type ThreadMentionSendTextBuilder = (
  text: string,
  mentions: ReadonlyArray<DraftThreadMention>
) => Promise<string>;

/** 已接好 RemoteContext 的发送文本构造函数，供输入框直接调用。 */
export function useThreadMentionSendText(): ThreadMentionSendTextBuilder {
  const { getThreadHistory, threads } = useRemote();
  return useCallback(
    (text: string, mentions: ReadonlyArray<DraftThreadMention>) =>
      buildThreadMentionSendText({ text, mentions, threads, fetchHistory: getThreadHistory }),
    [getThreadHistory, threads]
  );
}
