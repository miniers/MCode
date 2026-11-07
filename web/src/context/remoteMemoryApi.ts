// 记忆提议收件箱（memory.proposals.*）的远程调用封装，对应宿主 internal/remote/memory_ext_methods.go。
// 字段与桌面端 mcodeui.MemoryProposal 一致：提议先落在 Mac 的本地收件箱，
// 用户接受后才写入 Nowledge Mem。类型来自 web/src/types/generated.ts，此处按原名再导出。

import { useCallback, useEffect, useRef, useState } from 'react';
import type {
  RemoteMemoryCreateParams,
  RemoteMemoryProposal,
  RemoteMemoryProposalChangedEvent,
  RemoteMemoryProposalListResult,
  RemoteMemoryProposalOKResult,
  RemoteMemoryProposalStatus,
  RemoteMemoryProposalsAcceptParams,
  RemoteMemorySearchEntry,
  RemoteMemorySearchParams,
  RemoteMemorySearchResult
} from '../types/generated';
import { useRemoteCall, useRemoteNotification } from './useRemoteCall';

export type {
  RemoteMemoryProposal,
  RemoteMemoryProposalChangedEvent,
  RemoteMemoryProposalListResult,
  RemoteMemoryProposalOKResult,
  RemoteMemoryProposalStatus,
  RemoteMemorySearchEntry
};

export const REMOTE_MEMORY_PROPOSALS_LIST_METHOD = 'memory.proposals.list';
export const REMOTE_MEMORY_PROPOSALS_ACCEPT_METHOD = 'memory.proposals.accept';
export const REMOTE_MEMORY_PROPOSALS_REJECT_METHOD = 'memory.proposals.reject';
export const REMOTE_MEMORY_PROPOSALS_CHANGED_METHOD = 'memory.proposals.changed';
export const REMOTE_MEMORY_CREATE_METHOD = 'memory.create';
export const REMOTE_MEMORY_SEARCH_METHOD = 'memory.search';

/** 记忆类型，与宿主缺省值一致。 */
export type RemoteMemoryUnitType = 'fact' | 'preference' | 'decision' | 'plan' | 'procedure' | 'learning' | 'context' | 'event';

/** 记忆作用域：project 只在本项目生效，global 跨项目。 */
export type RemoteMemoryScope = 'project' | 'global';

/** 接受提议时可选携带的编辑字段（对应桌面端「保存编辑，再点接受」）。 */
export interface MemoryProposalEdits {
  title?: string;
  content?: string;
  type?: string;
  importance?: number;
}

/** memory.create 的入参：新建的是待审提议，不是直接写入的长期记忆。 */
export interface MemoryCreateInput {
  title: string;
  content?: string;
  type?: RemoteMemoryUnitType;
  importance?: number;
  scope?: RemoteMemoryScope;
  threadID?: string;
}

/** memory.search 的入参：limit 缺省 20，宿主上限 50。 */
export interface MemorySearchInput {
  query: string;
  limit?: number;
}

export interface UseMemoryActionsResult {
  /** 新建一条待审提议，返回创建之后的提议。 */
  createMemory: (input: MemoryCreateInput) => Promise<RemoteMemoryProposal>;
  /** 搜索记忆，query 为空时直接返回空数组；projectID 为空表示全部项目。 */
  searchMemories: (input: MemorySearchInput) => Promise<RemoteMemorySearchEntry[]>;
  /** 接受一条提议，可同时带上编辑后的标题/内容/类型/重要性。 */
  acceptProposal: (id: string, edits?: MemoryProposalEdits) => Promise<RemoteMemoryProposal>;
  /** 宿主是否声明了 memory.create。 */
  createSupported: boolean;
  /** 宿主是否声明了 memory.search。 */
  searchSupported: boolean;
  /** 宿主是否声明了 memory.proposals.accept。 */
  acceptSupported: boolean;
}

const trimmed = (value?: string): string | undefined => {
  const text = value?.trim();
  return text ? text : undefined;
};

/** 把可选编辑字段收拾成 accept 参数；空值一律不传，避免把字段清空。 */
function acceptEdits(edits?: MemoryProposalEdits): Omit<RemoteMemoryProposalsAcceptParams, 'id'> {
  if (!edits) return {};
  const title = trimmed(edits.title);
  const content = edits.content;
  const type = trimmed(edits.type);
  return {
    ...(title ? { title } : {}),
    ...(content === undefined ? {} : { content }),
    ...(type ? { type } : {}),
    ...(edits.importance === undefined ? {} : { importance: edits.importance })
  };
}

/** 记忆提议收件箱的读取与审核动作。 */
export interface UseMemoryProposalsResult {
  proposals: RemoteMemoryProposal[];
  loading: boolean;
  error: string;
  /** 接受一条待审提议：写入 Nowledge Mem，失败时抛出宿主返回的错误；edits 非空时先按编辑后的内容更新再接受。 */
  accept: (id: string, edits?: MemoryProposalEdits) => Promise<void>;
  /** 驳回一条待审提议：只改 Mac 上的本地状态。 */
  reject: (id: string) => Promise<void>;
  reload: () => Promise<void>;
  /** 宿主是否提供记忆提议方法；为假时入口与列表都不显示。 */
  supported: boolean;
}

/**
 * 拉取并审核记忆提议。
 * projectID 为空表示全部项目；status 决定列表过滤的状态。
 * 收到 memory.proposals.changed 时自动重新拉取，多处同时审核也能同步。
 */
export function useMemoryProposals(
  projectID: string | null,
  status: RemoteMemoryProposalStatus
): UseMemoryProposalsResult {
  const { call, supportsMethod } = useRemoteCall();
  const supported = supportsMethod(REMOTE_MEMORY_PROPOSALS_LIST_METHOD);
  const [proposals, setProposals] = useState<RemoteMemoryProposal[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  // 请求代际：切项目/切状态后旧请求回来时直接丢弃，避免列表回踩。
  const seqRef = useRef(0);

  const reload = useCallback(async () => {
    if (!supported) {
      seqRef.current += 1;
      setProposals([]);
      setLoading(false);
      return;
    }
    const seq = seqRef.current + 1;
    seqRef.current = seq;
    setLoading(true);
    try {
      const result = await call<RemoteMemoryProposalListResult>(REMOTE_MEMORY_PROPOSALS_LIST_METHOD, {
        ...(projectID ? { projectID } : {}),
        ...(status ? { status } : {})
      });
      if (seq !== seqRef.current) return;
      setProposals(result.proposals ?? []);
      setError('');
    } catch (err) {
      if (seq !== seqRef.current) return;
      setProposals([]);
      setError(err instanceof Error ? err.message : '读取记忆提议失败，请稍后重试');
    } finally {
      if (seq === seqRef.current) setLoading(false);
    }
  }, [call, projectID, status, supported]);

  useEffect(() => {
    void reload();
  }, [reload]);

  useRemoteNotification(
    REMOTE_MEMORY_PROPOSALS_CHANGED_METHOD,
    () => {
      void reload();
    },
    supported
  );

  const accept = useCallback(
    async (id: string, edits?: MemoryProposalEdits) => {
      // 失败时由宿主返回原始错误（如 Nowledge 未配置），交给调用方显示。
      const params: RemoteMemoryProposalsAcceptParams = { id, ...acceptEdits(edits) };
      await call<RemoteMemoryProposal>(REMOTE_MEMORY_PROPOSALS_ACCEPT_METHOD, params);
      await reload();
    },
    [call, reload]
  );

  const reject = useCallback(
    async (id: string) => {
      await call<RemoteMemoryProposalOKResult>(REMOTE_MEMORY_PROPOSALS_REJECT_METHOD, { id });
      await reload();
    },
    [call, reload]
  );

  return { proposals, loading, error, accept, reject, reload, supported };
}

/**
 * 记忆的新建与搜索：对应宿主 memory.create 与 memory.search（两者都走 Nowledge Mem 后端）。
 * projectID 为空时新建会报错，搜索则跨全部项目。
 */
export function useMemoryActions(projectID: string | null): UseMemoryActionsResult {
  const { call, supportsMethod } = useRemoteCall();
  const createSupported = supportsMethod(REMOTE_MEMORY_CREATE_METHOD);
  const searchSupported = supportsMethod(REMOTE_MEMORY_SEARCH_METHOD);
  const acceptSupported = supportsMethod(REMOTE_MEMORY_PROPOSALS_ACCEPT_METHOD);

  const createMemory = useCallback(
    async (input: MemoryCreateInput): Promise<RemoteMemoryProposal> => {
      const title = trimmed(input.title);
      if (!title) throw new Error('请先填写记忆标题');
      if (!projectID) throw new Error('请先选择项目，再新建记忆');
      const content = input.content;
      const params: RemoteMemoryCreateParams = {
        projectID,
        title,
        ...(content === undefined ? {} : { content }),
        ...(input.type ? { type: input.type } : {}),
        ...(input.importance === undefined ? {} : { importance: input.importance }),
        ...(input.scope ? { scope: input.scope } : {}),
        ...(input.threadID ? { threadID: input.threadID } : {})
      };
      return call<RemoteMemoryProposal>(REMOTE_MEMORY_CREATE_METHOD, params);
    },
    [call, projectID]
  );

  const searchMemories = useCallback(
    async (input: MemorySearchInput): Promise<RemoteMemorySearchEntry[]> => {
      const query = trimmed(input.query);
      if (!query) return [];
      const params: RemoteMemorySearchParams = {
        query,
        ...(projectID ? { projectID } : {}),
        ...(input.limit === undefined ? {} : { limit: input.limit })
      };
      const result = await call<RemoteMemorySearchResult>(REMOTE_MEMORY_SEARCH_METHOD, params);
      return result?.results ?? [];
    },
    [call, projectID]
  );

  const acceptProposal = useCallback(
    async (id: string, edits?: MemoryProposalEdits): Promise<RemoteMemoryProposal> => {
      const params: RemoteMemoryProposalsAcceptParams = { id, ...acceptEdits(edits) };
      return call<RemoteMemoryProposal>(REMOTE_MEMORY_PROPOSALS_ACCEPT_METHOD, params);
    },
    [call]
  );

  return { createMemory, searchMemories, acceptProposal, createSupported, searchSupported, acceptSupported };
}
