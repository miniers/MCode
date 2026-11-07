// 待处理收件箱（attention.*）的远程调用封装，对应宿主 internal/remote/attention_ext_methods.go。
// 宿主把「会话卡住 / 出错 / 额度用尽 / 等待审批 / 看板与自动化失败」等需要用户处理的事项
// 汇总成统一列表，这里只负责拉取、标记已处理，并在 attention.changed 推送后重新拉取。
// 类型来自 web/src/types/generated.ts，此处按原名再导出。

import { useCallback, useEffect, useRef, useState } from 'react';
import type {
  RemoteAttentionChangedParams,
  RemoteAttentionConfirmSuggestionResult,
  RemoteAttentionItem,
  RemoteAttentionListResult,
  RemoteAttentionResolveResult,
  RemoteAttentionSuggestion
} from '../types/generated';
import { useRemoteCall, useRemoteNotification } from './useRemoteCall';

export type {
  RemoteAttentionChangedParams,
  RemoteAttentionConfirmSuggestionResult,
  RemoteAttentionItem,
  RemoteAttentionListResult,
  RemoteAttentionResolveResult,
  RemoteAttentionSuggestion
};

export const REMOTE_ATTENTION_LIST_METHOD = 'attention.list';
export const REMOTE_ATTENTION_RESOLVE_METHOD = 'attention.resolve';
export const REMOTE_ATTENTION_CONFIRM_SUGGESTION_METHOD = 'attention.confirmSuggestion';
export const REMOTE_ATTENTION_CHANGED_METHOD = 'attention.changed';

/** 单次拉取的条数上限，与宿主默认值一致（宿主上限 500）。 */
export const ATTENTION_LIST_LIMIT = 100;

/** 待处理收件箱的读取与处理动作。 */
export interface UseAttentionInboxResult {
  /** 宿主是否提供 attention.list；为假时入口与列表都不显示。 */
  supported: boolean;
  /** 未处理事项，按宿主返回顺序排列。 */
  items: RemoteAttentionItem[];
  /** 未处理事项总数，来自宿主；不受 limit 截断影响。 */
  unresolvedCount: number;
  loading: boolean;
  error: string;
  refresh: () => Promise<void>;
  /** 标记一条事项已处理：成功后乐观地从列表移除并递减计数。 */
  resolve: (id: string) => Promise<void>;
  /** 宿主是否提供 attention.confirmSuggestion；为假时建议块不显示确认按钮。 */
  canConfirmSuggestion: boolean;
  /**
   * 按总管的建议回复审批：optionID 必须传用户看见的那个。
   * 成功后从该事项的 suggestions 里摘掉这条；失败时不改列表，
   * 先刷新拿最新建议再把错误抛给调用方。
   */
  confirmSuggestion: (threadID: string, suggestion: RemoteAttentionSuggestion) => Promise<void>;
}

/**
 * 拉取待处理事项。
 * projectID 为空表示全部项目；收到 attention.changed 时自动重新拉取，
 * 多端同时处理也能同步角标与列表。
 */
export function useAttentionInbox(projectID: string | null = null): UseAttentionInboxResult {
  const { call, supportsMethod } = useRemoteCall();
  const supported = supportsMethod(REMOTE_ATTENTION_LIST_METHOD);
  const canConfirmSuggestion = supportsMethod(REMOTE_ATTENTION_CONFIRM_SUGGESTION_METHOD);
  const [items, setItems] = useState<RemoteAttentionItem[]>([]);
  const [unresolvedCount, setUnresolvedCount] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  // 请求代际：切项目后旧请求回来时直接丢弃，避免列表回踩。
  const seqRef = useRef(0);

  const refresh = useCallback(async () => {
    if (!supported) {
      seqRef.current += 1;
      setItems([]);
      setUnresolvedCount(0);
      setLoading(false);
      return;
    }
    const seq = seqRef.current + 1;
    seqRef.current = seq;
    setLoading(true);
    try {
      const result = await call<RemoteAttentionListResult>(REMOTE_ATTENTION_LIST_METHOD, {
        ...(projectID ? { projectID } : {}),
        limit: ATTENTION_LIST_LIMIT
      });
      if (seq !== seqRef.current) return;
      const nextItems = result.items ?? [];
      // 宿主只统计未处理项，缺省时按列表长度兜底。
      const nextCount = typeof result.unresolvedCount === 'number' ? result.unresolvedCount : nextItems.length;
      setItems(nextItems);
      setUnresolvedCount(nextCount);
      setError('');
    } catch (err) {
      if (seq !== seqRef.current) return;
      setItems([]);
      setUnresolvedCount(0);
      setError(err instanceof Error ? err.message : '读取待处理事项失败，请稍后重试');
    } finally {
      if (seq === seqRef.current) setLoading(false);
    }
  }, [call, projectID, supported]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // 推送只带总数：先更新角标，再重新拉列表。
  useRemoteNotification(
    REMOTE_ATTENTION_CHANGED_METHOD,
    (params) => {
      const next = (params as Partial<RemoteAttentionChangedParams> | null)?.unresolvedCount;
      if (typeof next === 'number') setUnresolvedCount(next);
      void refresh();
    },
    supported
  );

  const resolve = useCallback(
    async (id: string) => {
      await call<RemoteAttentionResolveResult>(REMOTE_ATTENTION_RESOLVE_METHOD, { id });
      // 乐观更新：宿主已确认，先把这张卡片摘掉，避免整表闪一下。
      setItems((prev) => prev.filter((item) => item.id !== id));
      setUnresolvedCount((prev) => Math.max(0, prev - 1));
    },
    [call]
  );

  const confirmSuggestion = useCallback(
    async (threadID: string, suggestion: RemoteAttentionSuggestion) => {
      const { requestID, optionID } = suggestion;
      try {
        await call<RemoteAttentionConfirmSuggestionResult>(REMOTE_ATTENTION_CONFIRM_SUGGESTION_METHOD, {
          threadID,
          requestID,
          optionID
        });
      } catch (err) {
        // 失败时不改列表：先刷新让界面拿到最新建议（建议可能已被总管改掉），再把错误抛给调用方。
        await refresh();
        throw err;
      }
      // 宿主已按这条建议答复，只摘掉这条建议块，事项本身保持不动。
      setItems((prev) =>
        prev.map((item) =>
          item.threadID === threadID
            ? {
                ...item,
                suggestions: (item.suggestions ?? []).filter((entry) => entry.requestID !== requestID)
              }
            : item
        )
      );
    },
    [call, refresh]
  );

  return { supported, items, unresolvedCount, loading, error, refresh, resolve, canConfirmSuggestion, confirmSuggestion };
}
