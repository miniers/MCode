// 顶层 PR 视图：宽屏左栏列表 + 右栏详情，窄屏（<768px）列表与详情栈式切换并带返回按钮。

import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useRemote } from '../context/RemoteContext';
import {
  GIT_EXT_METHODS,
  fetchGitPRDetail,
  fetchGitPRList,
  startGitPRReview,
  type RemoteGitPRDetail,
  type RemoteGitPRSummary
} from '../context/remoteGitApi';
import { useRemoteCall } from '../context/useRemoteCall';
import { moveTargetProjects } from '../utils/builtinProjects';
import { GitPRDetailView, GitPRListRow } from './GitPRPanel';
import { ArrowPathIcon, ExclamationTriangleIcon, GitPullRequestIcon } from './Icons';

const PR_WIDE_QUERY = '(min-width: 768px)';

function readIsWidePR(): boolean {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return false;
  return window.matchMedia(PR_WIDE_QUERY).matches;
}

function useIsWidePR(): boolean {
  const [wide, setWide] = useState(readIsWidePR);
  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return;
    const query = window.matchMedia(PR_WIDE_QUERY);
    const handleChange = (event: MediaQueryListEvent) => setWide(event.matches);
    setWide(query.matches);
    query.addEventListener('change', handleChange);
    return () => query.removeEventListener('change', handleChange);
  }, []);
  return wide;
}

export interface PRViewProps {
  initialNumber?: number | null;
  onSelectPR?: (number: number | null, options?: { replace?: boolean }) => void;
  onOpenThread?: (threadID: string, message?: string) => void;
}

export const PRView: React.FC<PRViewProps> = ({ initialNumber = null, onSelectPR, onOpenThread }) => {
  const { projects, selectedProjectID, threads, currentThread, connectionState, loadThreads } = useRemote();
  const { call, supportsMethod } = useRemoteCall();
  const isWide = useIsWidePR();

  const canPR = supportsMethod('git.pr.list');
  const canPRDetail = supportsMethod('git.pr.detail');
  const canStartReview = supportsMethod(GIT_EXT_METHODS.prStartReview);

  const availableProjects = useMemo(() => {
    const regular = moveTargetProjects(projects);
    return regular.length > 0 ? regular : projects;
  }, [projects]);

  const [fallbackProjectID, setFallbackProjectID] = useState<string | null>(null);
  const effectiveProjectID = useMemo(() => {
    if (selectedProjectID) return selectedProjectID;
    if (fallbackProjectID && availableProjects.some((p) => p.id === fallbackProjectID)) {
      return fallbackProjectID;
    }
    const withThreads = availableProjects.find((p) =>
      threads.some((t) => t.projectID === p.id)
    );
    return (withThreads ?? availableProjects[0])?.id ?? null;
  }, [availableProjects, fallbackProjectID, selectedProjectID, threads]);

  const targetThreadID = useMemo(() => {
    if (!effectiveProjectID) return null;
    if (currentThread && currentThread.projectID === effectiveProjectID) {
      return currentThread.id;
    }
    const active = threads.find((t) => t.projectID === effectiveProjectID && !t.isArchived);
    return (active ?? threads.find((t) => t.projectID === effectiveProjectID))?.id ?? null;
  }, [currentThread, effectiveProjectID, threads]);

  const [items, setItems] = useState<RemoteGitPRSummary[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [selectedNumber, setSelectedNumber] = useState<number | null>(() => initialNumber ?? null);
  const [details, setDetails] = useState<Record<number, RemoteGitPRDetail>>({});
  const [detailLoading, setDetailLoading] = useState<number | null>(null);
  const [detailError, setDetailError] = useState<string | null>(null);
  const [startingReviewNumber, setStartingReviewNumber] = useState<number | null>(null);
  const [reviewError, setReviewError] = useState<string | null>(null);
  const [reloadToken, setReloadToken] = useState(0);

  const reqIdRef = useRef(0);
  const detailReqIdRef = useRef(0);
  const reviewReqIdRef = useRef(0);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      reqIdRef.current += 1;
      detailReqIdRef.current += 1;
      reviewReqIdRef.current += 1;
    };
  }, []);

  useEffect(() => {
    setSelectedNumber(initialNumber ?? null);
  }, [initialNumber]);

  // 切换项目时重置当前列表与选中态
  const prevProjectRef = useRef(effectiveProjectID);
  useEffect(() => {
    if (prevProjectRef.current === effectiveProjectID) return;
    prevProjectRef.current = effectiveProjectID;
    setSelectedNumber(null);
    setDetails({});
    setDetailError(null);
    setReviewError(null);
    onSelectPR?.(null, { replace: true });
  }, [effectiveProjectID, onSelectPR]);

  useEffect(() => {
    if (!canPR || connectionState !== 'connected' || !targetThreadID) {
      setLoading(false);
      setLoadError(null);
      if (!targetThreadID) setItems([]);
      return;
    }

    const reqId = ++reqIdRef.current;
    setLoading(true);
    setLoadError(null);

    void fetchGitPRList(call, targetThreadID)
      .then((result) => {
        if (!mountedRef.current || reqId !== reqIdRef.current) return;
        setItems(result.items);
        setDetails({});
        setDetailError(null);
      })
      .catch((err: unknown) => {
        if (!mountedRef.current || reqId !== reqIdRef.current) return;
        setItems([]);
        setDetails({});
        setLoadError(err instanceof Error ? err.message : '读取 PR/MR 列表失败');
      })
      .finally(() => {
        if (mountedRef.current && reqId === reqIdRef.current) {
          setLoading(false);
        }
      });
  }, [call, canPR, connectionState, reloadToken, targetThreadID]);

  // 宽屏下默认选中第一条 PR，方便右侧直接展示详情
  useEffect(() => {
    if (!isWide || items.length === 0) return;
    if (selectedNumber !== null && items.some((item) => item.number === selectedNumber)) {
      return;
    }
    setSelectedNumber(items[0].number);
  }, [isWide, items, selectedNumber]);

  const loadDetail = useCallback(
    async (prNumber: number) => {
      if (!targetThreadID || !canPRDetail || connectionState !== 'connected') return;
      const reqId = ++detailReqIdRef.current;
      setDetailError(null);
      setDetailLoading(prNumber);
      try {
        const detail = await fetchGitPRDetail(call, targetThreadID, prNumber);
        if (!mountedRef.current || reqId !== detailReqIdRef.current) return;
        setDetails((prev) => ({ ...prev, [prNumber]: detail }));
      } catch (err: unknown) {
        if (!mountedRef.current || reqId !== detailReqIdRef.current) return;
        setDetailError(err instanceof Error ? err.message : '读取 PR/MR 详情失败');
      } finally {
        if (mountedRef.current && reqId === detailReqIdRef.current) {
          setDetailLoading((prev) => (prev === prNumber ? null : prev));
        }
      }
    },
    [call, canPRDetail, connectionState, targetThreadID]
  );

  useEffect(() => {
    if (selectedNumber === null || !targetThreadID || !canPRDetail || connectionState !== 'connected') {
      return;
    }
    if (details[selectedNumber]) return;
    void loadDetail(selectedNumber);
  }, [canPRDetail, connectionState, details, loadDetail, selectedNumber, targetThreadID]);

  const handleSelectItem = useCallback(
    (item: RemoteGitPRSummary) => {
      setDetailError(null);
      setReviewError(null);
      setSelectedNumber(item.number);
      onSelectPR?.(item.number, { replace: isWide });
    },
    [isWide, onSelectPR]
  );

  const handleBack = useCallback(() => {
    setSelectedNumber(null);
    setDetailError(null);
    setReviewError(null);
    onSelectPR?.(null, { replace: true });
  }, [onSelectPR]);

  const handleStartReview = useCallback(async () => {
    if (selectedNumber === null || !targetThreadID || !canStartReview || connectionState !== 'connected') {
      return;
    }
    const prNumber = selectedNumber;
    const reqId = ++reviewReqIdRef.current;
    setReviewError(null);
    setStartingReviewNumber(prNumber);
    try {
      const result = await startGitPRReview(call, targetThreadID, prNumber);
      if (!mountedRef.current || reqId !== reviewReqIdRef.current) return;
      void loadThreads(effectiveProjectID);
      if (onOpenThread) {
        onOpenThread(result.threadID, result.message);
      } else if (typeof window !== 'undefined') {
        window.location.hash = `#/t/${encodeURIComponent(result.threadID)}`;
      }
    } catch (err: unknown) {
      if (!mountedRef.current || reqId !== reviewReqIdRef.current) return;
      setReviewError(err instanceof Error ? err.message : '发起 AI 审查失败');
    } finally {
      if (mountedRef.current && reqId === reviewReqIdRef.current) {
        setStartingReviewNumber((prev) => (prev === prNumber ? null : prev));
      }
    }
  }, [
    call,
    canStartReview,
    connectionState,
    effectiveProjectID,
    loadThreads,
    onOpenThread,
    selectedNumber,
    targetThreadID
  ]);

  const selectedSummary =
    selectedNumber !== null ? items.find((item) => item.number === selectedNumber) ?? null : null;
  const activeDetail = selectedNumber !== null ? details[selectedNumber] ?? null : null;

  if (!canPR) {
    return (
      <div className="empty-state">
        <div className="empty-state__icon">
          <GitPullRequestIcon className="h-6 w-6" aria-hidden="true" />
        </div>
        <h2>当前 Mac 暂不支持 PR 查看</h2>
      </div>
    );
  }

  const projectSwitcher =
    selectedProjectID === null && availableProjects.length > 1 ? (
      <div className="filter-row !mt-0 px-3 py-2 border-b border-mc-hairline" role="toolbar" aria-label="选择项目查看 PR">
        {availableProjects.map((project) => {
          const selected = effectiveProjectID === project.id;
          return (
            <button
              key={project.id}
              type="button"
              className={`filter-chip touch-hit-44 ${selected ? 'filter-chip--active' : ''}`}
              aria-pressed={selected}
              onClick={() => setFallbackProjectID(project.id)}
            >
              <span>{project.name}</span>
            </button>
          );
        })}
      </div>
    ) : null;

  const listPane = (
    <div className="flex flex-col min-h-0 flex-1 overflow-hidden">
      {projectSwitcher}
      <div className="flex items-center justify-between gap-2 px-3 py-2 border-b border-mc-hairline shrink-0">
        <span className="text-mc-footnote text-mc-text-secondary">
          {loading ? '正在读取...' : `共 ${items.length} 条`}
        </span>
        <button
          type="button"
          onClick={() => setReloadToken((prev) => prev + 1)}
          disabled={loading || !targetThreadID || connectionState !== 'connected'}
          aria-label="刷新 PR 列表"
          title="刷新"
          className="inline-flex items-center justify-center w-11 h-11 rounded-lg text-mc-text-secondary hover:bg-mc-overlay-hover hover:text-mc-text-primary active:bg-mc-overlay-pressed disabled:opacity-40 transition-colors motion-reduce:transition-none"
        >
          <ArrowPathIcon className="w-4 h-4" />
        </button>
      </div>

      <div className="flex-1 min-h-0 overflow-y-auto overscroll-contain p-2 space-y-1">
        {loadError && (
          <div className="inline-alert inline-alert--danger" role="alert">
            <div className="flex items-start gap-2 min-w-0 flex-1">
              <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-mc-danger" />
              <span className="break-words select-text">{loadError}</span>
            </div>
            <button
              type="button"
              onClick={() => setReloadToken((prev) => prev + 1)}
              disabled={loading}
              className="inline-alert__action min-h-[44px] px-2 flex items-center"
            >
              重试
            </button>
          </div>
        )}

        {!targetThreadID ? (
          <div className="empty-state">
            <div className="empty-state__icon">
              <GitPullRequestIcon className="h-6 w-6" aria-hidden="true" />
            </div>
            <h2>暂无可用会话</h2>
          </div>
        ) : loading && items.length === 0 ? (
          <div className="empty-state">
            <span className="loading-spinner mb-1" />
            <h2>正在读取 PR/MR</h2>
          </div>
        ) : !loadError && items.length === 0 ? (
          <div className="empty-state">
            <h2>没有开放的 PR/MR</h2>
          </div>
        ) : (
          <ul className="space-y-1">
            {items.map((item) => (
              <li key={item.number}>
                <GitPRListRow
                  item={item}
                  selected={isWide && item.number === selectedNumber}
                  onSelect={handleSelectItem}
                />
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );

  if (isWide) {
    return (
      <div className="flex flex-1 min-h-0 overflow-hidden bg-screen">
        <aside className="w-[320px] lg:w-[360px] shrink-0 border-r border-mc-hairline flex flex-col min-h-0 bg-mc-surface-main">
          {listPane}
        </aside>
        <section className="flex-1 min-w-0 min-h-0 overflow-y-auto overscroll-contain px-6 py-5">
          {selectedNumber !== null ? (
            <div className="max-w-3xl">
              <GitPRDetailView
                number={selectedNumber}
                summary={selectedSummary}
                detail={activeDetail}
                loading={detailLoading === selectedNumber}
                error={detailError}
                onRetry={() => void loadDetail(selectedNumber)}
                canStartReview={canStartReview}
                startingReview={startingReviewNumber === selectedNumber}
                reviewDisabled={!targetThreadID || connectionState !== 'connected'}
                reviewError={reviewError}
                onStartReview={() => void handleStartReview()}
              />
            </div>
          ) : (
            <div className="empty-state h-full">
              <div className="empty-state__icon">
                <GitPullRequestIcon className="h-6 w-6" aria-hidden="true" />
              </div>
              <h2>{items.length > 0 ? '选择左侧 PR 查看详情' : '没有开放的 PR/MR'}</h2>
            </div>
          )}
        </section>
      </div>
    );
  }

  if (selectedNumber !== null) {
    return (
      <div className="flex-1 min-h-0 overflow-y-auto overscroll-contain px-4 py-3 pb-[calc(20px+var(--sab))]">
        <GitPRDetailView
          number={selectedNumber}
          summary={selectedSummary}
          detail={activeDetail}
          loading={detailLoading === selectedNumber}
          error={detailError}
          onRetry={() => void loadDetail(selectedNumber)}
          onBack={handleBack}
          canStartReview={canStartReview}
          startingReview={startingReviewNumber === selectedNumber}
          reviewDisabled={!targetThreadID || connectionState !== 'connected'}
          reviewError={reviewError}
          onStartReview={() => void handleStartReview()}
        />
      </div>
    );
  }

  return (
    <div key="pr-list" className="pr-view-enter flex flex-1 min-h-0 flex-col overflow-hidden">
      {listPane}
    </div>
  );
};
