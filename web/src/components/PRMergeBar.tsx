// 会话内 PR 合并条：贴合输入框上沿展示 PR 编号、本地提交数、Fork 标记及合并操作，
// 宽屏使用锁定在主按钮上的弹出层，窄屏（<768px）使用底部抽屉；合并成功后替换输入区为归档状态条。

import React, { useCallback, useEffect, useRef, useState } from 'react';
import { useRemote } from '../context/RemoteContext';
import {
  GIT_EXT_METHODS,
  fetchGitPRThreadState,
  mergeGitPRFlow,
  type RemoteGitPRMergeFlowParams,
  type RemoteGitPRMergeFlowResult,
  type RemoteGitPRThreadStateResult
} from '../context/remoteGitApi';
import { useRemoteCall } from '../context/useRemoteCall';
import { useSheetDrag } from '../hooks/useSheetDrag';
import {
  CircleCheckIcon,
  CircleIcon,
  CircleXIcon,
  ExclamationTriangleIcon,
  GitPullRequestIcon,
  HalfRingIcon,
  PlusIcon,
  XMarkIcon
} from './Icons';
import { SHEET_LAYER_CLASS, SheetFrame } from './sheet/SheetFrame';

export const PR_MERGED_TITLE_PREFIX = '已合并 · ';
const PR_WIDE_QUERY = '(min-width: 768px)';

export type PRMergeMethod = RemoteGitPRMergeFlowParams['method'];
export type PRMergeFromStep = NonNullable<RemoteGitPRMergeFlowParams['from']>;

export interface PRMergeButtonState {
  label: string;
  disabled: boolean;
}

export type MergeFlowStepKey = 'push' | 'merge' | 'sync' | 'cleanup';
export type MergeStepStatus = 'pending' | 'running' | 'done' | 'failed';

export interface MergeStepItem {
  key: MergeFlowStepKey;
  label: string;
  status: MergeStepStatus;
}

export const MERGE_FLOW_STEPS: ReadonlyArray<{ key: MergeFlowStepKey; label: string }> = [
  { key: 'push', label: '推送' },
  { key: 'merge', label: '合并 PR' },
  { key: 'sync', label: '更新本地 main' },
  { key: 'cleanup', label: '清理' }
];

const MERGE_METHOD_OPTIONS: ReadonlyArray<{ value: PRMergeMethod; label: string }> = [
  { value: 'squash', label: 'Squash' },
  { value: 'merge', label: '合并提交' },
  { value: 'rebase', label: '变基' }
];

/** 按会话标题判断是否已是归档合并态。 */
export function isMergedThreadTitle(title?: string | null): boolean {
  return typeof title === 'string' && title.startsWith(PR_MERGED_TITLE_PREFIX);
}

/** 从 threadState 或会话标题提取 PR 编号；拿不到时返回 null。 */
export function resolveMergedPRNumber(
  title?: string | null,
  stateNumber?: number | null
): number | null {
  if (typeof stateNumber === 'number' && Number.isFinite(stateNumber) && stateNumber > 0) {
    return stateNumber;
  }
  if (!title) return null;
  const match = title.match(/#([1-9]\d*)/);
  return match ? Number.parseInt(match[1], 10) : null;
}

/** 根据工作区与 PR 状态推导主按钮文案与禁用态。 */
export function mergeButtonState(
  state: Pick<RemoteGitPRThreadStateResult, 'dirty' | 'localCommits' | 'crossRepo'>
): PRMergeButtonState {
  if (state.dirty) {
    return { label: '有未提交的改动', disabled: true };
  }
  if (state.localCommits > 0 && !state.crossRepo) {
    return { label: '推送并合并', disabled: false };
  }
  return { label: '合并', disabled: false };
}

/** 把 mergeFlow 结果（或运行中态）映射为四步状态列表。 */
export function stepStatuses(
  result: RemoteGitPRMergeFlowResult | null | undefined,
  isRunning = false
): MergeStepItem[] {
  if (isRunning || !result) {
    const status: MergeStepStatus = isRunning ? 'running' : 'pending';
    return MERGE_FLOW_STEPS.map((step) => ({ ...step, status }));
  }
  if (result.ok) {
    return MERGE_FLOW_STEPS.map((step) => ({ ...step, status: 'done' }));
  }
  const failedIndex = MERGE_FLOW_STEPS.findIndex((step) => step.key === result.failedStep);
  return MERGE_FLOW_STEPS.map((step, index) => {
    if (failedIndex < 0) {
      return { ...step, status: index === 0 ? 'failed' : 'pending' };
    }
    if (index < failedIndex) return { ...step, status: 'done' };
    if (index === failedIndex) return { ...step, status: 'failed' };
    return { ...step, status: 'pending' };
  });
}

/** 把失败步骤名校验并收窄为合法的重试起点参数。 */
export function retryFromStep(failedStep?: string): PRMergeFromStep | undefined {
  if (failedStep === 'push' || failedStep === 'merge' || failedStep === 'sync' || failedStep === 'cleanup') {
    return failedStep;
  }
  return undefined;
}

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

interface PopoverAnchor {
  left: number;
  bottom: number;
  maxHeight: number;
  width: number;
}

export interface PRMergeBarProps {
  threadID: string;
  threadTitle?: string | null;
  projectID?: string | null;
  isRunning: boolean;
  /** 外层合并归档标志（例如会话标题带前缀或本次合并已成功）。 */
  merged?: boolean;
  onMerged?: () => void;
  onNewThread?: (projectID?: string | null) => void;
}

export const PRMergeBar: React.FC<PRMergeBarProps> = ({
  threadID,
  threadTitle = null,
  projectID = null,
  isRunning,
  merged = false,
  onMerged,
  onNewThread
}) => {
  const { connectionState } = useRemote();
  const { call, supportsMethod } = useRemoteCall();
  const isWide = useIsWidePR();

  const threadStateSupported = supportsMethod(GIT_EXT_METHODS.prThreadState);
  const mergeFlowSupported = supportsMethod(GIT_EXT_METHODS.prMergeFlow);
  const isConnected = connectionState === 'connected';

  const [stateEntry, setStateEntry] = useState<{
    threadID: string;
    data: RemoteGitPRThreadStateResult | null;
  }>({ threadID: '', data: null });
  const threadState = stateEntry.threadID === threadID ? stateEntry.data : null;

  const [confirmOpen, setConfirmOpen] = useState(false);
  const [mergeMethod, setMergeMethod] = useState<PRMergeMethod>('squash');
  const [phase, setPhase] = useState<'confirm' | 'running' | 'result'>('confirm');
  const [mergeResult, setMergeResult] = useState<RemoteGitPRMergeFlowResult | null>(null);
  const [mergeError, setMergeError] = useState<string | null>(null);
  const [localMergedThreadID, setLocalMergedThreadID] = useState<string | null>(null);
  const [popoverAnchor, setPopoverAnchor] = useState<PopoverAnchor | null>(null);

  const triggerBtnRef = useRef<HTMLButtonElement | null>(null);
  const sheetRef = useRef<HTMLElement | null>(null);
  const sheetHandleRef = useRef<HTMLDivElement | null>(null);
  const stateReqIdRef = useRef(0);
  const mergeReqIdRef = useRef(0);
  const mountedRef = useRef(true);
  const activeThreadIdRef = useRef(threadID);
  activeThreadIdRef.current = threadID;

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      stateReqIdRef.current += 1;
      mergeReqIdRef.current += 1;
    };
  }, []);

  // 切换会话时重置弹层与执行状态，避免上一个会话的确认框或结果残留
  useEffect(() => {
    mergeReqIdRef.current += 1;
    setConfirmOpen(false);
    setMergeMethod('squash');
    setPhase('confirm');
    setMergeResult(null);
    setMergeError(null);
    setPopoverAnchor(null);
  }, [threadID]);

  const loadThreadState = useCallback(async () => {
    if (!threadStateSupported || !isConnected || !threadID) return;
    const reqId = ++stateReqIdRef.current;
    const targetThreadID = threadID;
    try {
      const next = await fetchGitPRThreadState(call, targetThreadID);
      if (!mountedRef.current || reqId !== stateReqIdRef.current || activeThreadIdRef.current !== targetThreadID) {
        return;
      }
      setStateEntry({ threadID: targetThreadID, data: next });
    } catch {
      // 查询失败保持静默，不阻断正常聊天输入
    }
  }, [call, isConnected, threadID, threadStateSupported]);

  // 切换会话时查询一次
  useEffect(() => {
    void loadThreadState();
  }, [loadThreadState]);

  // 会话一个回合结束（运行态从运行变为空闲）时再查询一次
  const prevTurnRef = useRef<{ threadID: string; isRunning: boolean }>({
    threadID,
    isRunning
  });
  useEffect(() => {
    const prev = prevTurnRef.current;
    prevTurnRef.current = { threadID, isRunning };
    if (prev.threadID === threadID && prev.isRunning && !isRunning) {
      void loadThreadState();
    }
  }, [isRunning, loadThreadState, threadID]);

  const isMerging = phase === 'running';
  const isMerged =
    merged ||
    localMergedThreadID === threadID ||
    isMergedThreadTitle(threadTitle);

  const computePopoverAnchor = useCallback((): PopoverAnchor | null => {
    if (!isWide) return null;
    const button = triggerBtnRef.current;
    if (!button || typeof window === 'undefined') return null;
    const rect = button.getBoundingClientRect();
    const width = Math.min(380, window.innerWidth - 32);
    const rawLeft = rect.right - width;
    const left = Math.min(Math.max(rawLeft, 16), window.innerWidth - width - 16);
    return {
      left,
      bottom: Math.max(16, window.innerHeight - rect.top + 8),
      maxHeight: Math.max(200, rect.top - 16),
      width
    };
  }, [isWide]);

  useEffect(() => {
    if (!confirmOpen) return;
    setPopoverAnchor(computePopoverAnchor());
    const handleResize = () => setPopoverAnchor(computePopoverAnchor());
    window.addEventListener('resize', handleResize);
    return () => window.removeEventListener('resize', handleResize);
  }, [computePopoverAnchor, confirmOpen]);

  const closeConfirm = useCallback(() => {
    if (isMerging) return;
    setConfirmOpen(false);
  }, [isMerging]);

  const usePopoverMode = isWide && popoverAnchor !== null;
  useSheetDrag(
    sheetRef,
    sheetHandleRef,
    closeConfirm,
    confirmOpen && !usePopoverMode && !isMerging
  );

  const openConfirm = useCallback(() => {
    if (!threadState || isMerging || isMerged) return;
    setPhase('confirm');
    setMergeResult(null);
    setMergeError(null);
    setPopoverAnchor(computePopoverAnchor());
    setConfirmOpen(true);
  }, [computePopoverAnchor, isMerged, isMerging, threadState]);

  const executeMerge = useCallback(
    async (from?: PRMergeFromStep) => {
      if (!threadState || isMerging || !mergeFlowSupported || !isConnected) return;
      const reqId = ++mergeReqIdRef.current;
      const targetThreadID = threadID;
      setPhase('running');
      setMergeResult(null);
      setMergeError(null);
      try {
        const result = await mergeGitPRFlow(call, targetThreadID, threadState.number, mergeMethod, from);
        if (!mountedRef.current || reqId !== mergeReqIdRef.current || activeThreadIdRef.current !== targetThreadID) {
          return;
        }
        setMergeResult(result);
        setPhase('result');
        if (result.ok) {
          setLocalMergedThreadID(targetThreadID);
          onMerged?.();
        } else {
          void loadThreadState();
        }
      } catch (err: unknown) {
        if (!mountedRef.current || reqId !== mergeReqIdRef.current || activeThreadIdRef.current !== targetThreadID) {
          return;
        }
        setMergeError(err instanceof Error ? err.message : '合并请求失败');
        setPhase('result');
      }
    },
    [call, isConnected, isMerging, loadThreadState, mergeFlowSupported, mergeMethod, onMerged, threadID, threadState]
  );

  const handleNewThread = useCallback(() => {
    if (onNewThread) {
      onNewThread(projectID);
      return;
    }
    if (typeof window !== 'undefined') {
      window.location.hash = projectID ? `#/new/${encodeURIComponent(projectID)}` : '#/';
    }
  }, [onNewThread, projectID]);

  if (!threadStateSupported) return null;
  if (!isMerged && !threadState?.isPR) return null;

  const prNumber = resolveMergedPRNumber(threadTitle, threadState?.number);
  const steps = stepStatuses(mergeResult, isMerging);
  const failedStepFrom = retryFromStep(mergeResult?.failedStep);
  const failureMessage =
    mergeError || (mergeResult && !mergeResult.ok ? mergeResult.message || '合并失败' : null);

  const dialogBody =
    phase === 'confirm' && threadState ? (
      <div className="space-y-3">
        <div className="flex items-center justify-between gap-2">
          <h3 className="text-mc-title font-semibold text-mc-text-primary">
            合并 PR #{threadState.number}
          </h3>
          <button
            type="button"
            onClick={closeConfirm}
            aria-label="关闭"
            className="touch-hit-44 inline-flex items-center justify-center w-8 h-8 rounded-mc-sm text-mc-text-secondary hover:bg-mc-overlay-hover hover:text-mc-text-primary transition-colors motion-reduce:transition-none"
          >
            <XMarkIcon className="w-4 h-4" />
          </button>
        </div>

        <div
          className="grid grid-cols-3 gap-0.5 p-0.5 rounded-mc-sm bg-mc-overlay-hover"
          role="radiogroup"
          aria-label="合并方式"
        >
          {MERGE_METHOD_OPTIONS.map((option) => {
            const active = mergeMethod === option.value;
            return (
              <button
                key={option.value}
                type="button"
                role="radio"
                aria-checked={active}
                onClick={() => setMergeMethod(option.value)}
                className={`touch-hit-44 h-9 rounded-mc-xs text-mc-footnote font-medium transition-colors motion-reduce:transition-none ${
                  active
                    ? 'bg-mc-surface-card text-mc-text-primary border border-mc-hairline'
                    : 'text-mc-text-secondary hover:text-mc-text-primary'
                }`}
              >
                {option.label}
              </button>
            );
          })}
        </div>

        <div className="flex items-center justify-between gap-2 px-3 py-2 rounded-mc-md border border-mc-hairline bg-mc-surface-main text-mc-footnote">
          <span className="text-mc-text-secondary">将推送的本地提交</span>
          <span className="flex items-center gap-1.5 font-mono text-mc-code text-mc-text-primary">
            <span>{threadState.crossRepo ? 0 : threadState.localCommits}</span>
            {threadState.crossRepo && (
              <span className="px-1.5 py-0.5 rounded-mc-pill bg-mc-warning/15 text-mc-warning font-sans text-mc-caption">
                Fork
              </span>
            )}
          </span>
        </div>

        <div className="flex items-center justify-end gap-2 pt-1">
          <button
            type="button"
            onClick={closeConfirm}
            className="touch-hit-44 min-h-[44px] px-3 rounded-mc-sm text-mc-footnote font-medium text-mc-text-secondary hover:bg-mc-overlay-hover hover:text-mc-text-primary transition-colors motion-reduce:transition-none"
          >
            取消
          </button>
          <button
            type="button"
            onClick={() => void executeMerge()}
            disabled={!mergeFlowSupported || !isConnected}
            className="touch-hit-44 min-h-[44px] px-4 rounded-mc-sm bg-mc-accent text-mc-on-accent text-mc-footnote font-medium hover:bg-mc-accent-hover active:bg-mc-accent-pressed disabled:opacity-45 transition-colors motion-reduce:transition-none"
          >
            {threadState.localCommits > 0 && !threadState.crossRepo ? '确认推送并合并' : '确认合并'}
          </button>
        </div>
      </div>
    ) : (
      <div className="space-y-3">
        <div className="flex items-center justify-between gap-2">
          <div className="flex items-center gap-2 min-w-0">
            <h3 className="text-mc-title font-semibold text-mc-text-primary truncate">
              PR #{prNumber ?? threadState?.number ?? ''}
            </h3>
            {isMerging && (
              <span className="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-mc-pill bg-mc-accent-subtle text-mc-accent text-mc-caption font-medium">
                <span className="loading-spinner loading-spinner--small shrink-0 motion-reduce:animate-none" />
                <span>进行中</span>
              </span>
            )}
          </div>
          <button
            type="button"
            onClick={closeConfirm}
            disabled={isMerging}
            aria-label="关闭"
            className="touch-hit-44 inline-flex items-center justify-center w-8 h-8 rounded-mc-sm text-mc-text-secondary hover:bg-mc-overlay-hover hover:text-mc-text-primary disabled:opacity-35 transition-colors motion-reduce:transition-none"
          >
            <XMarkIcon className="w-4 h-4" />
          </button>
        </div>

        <ul className="space-y-1.5">
          {steps.map((step) => {
            const rowStyle =
              step.status === 'running'
                ? 'border-mc-accent/35 bg-mc-accent-subtle'
                : step.status === 'failed'
                ? 'border-mc-danger/35 bg-mc-danger/10'
                : step.status === 'done'
                ? 'border-mc-hairline bg-mc-surface-main'
                : 'border-transparent bg-mc-surface-main';
            return (
              <li
                key={step.key}
                data-step-key={step.key}
                data-step-status={step.status}
                className={`pr-merge-step flex items-center gap-2.5 px-3 py-2 rounded-mc-md border text-mc-footnote ${rowStyle}`}
              >
                {step.status === 'done' ? (
                  <CircleCheckIcon className="w-4 h-4 text-mc-success shrink-0" />
                ) : step.status === 'failed' ? (
                  <CircleXIcon className="w-4 h-4 text-mc-danger shrink-0" />
                ) : step.status === 'running' ? (
                  <HalfRingIcon className="w-4 h-4 text-mc-accent shrink-0" />
                ) : (
                  <CircleIcon className="w-4 h-4 text-mc-text-tertiary shrink-0" />
                )}
                <span className="font-medium text-mc-text-primary">{step.label}</span>
              </li>
            );
          })}
        </ul>

        {failureMessage && (
          <div className="inline-alert inline-alert--danger !my-0" role="alert">
            <div className="flex items-start gap-2 min-w-0 flex-1">
              <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-mc-danger" />
              <span className="break-words select-text">{failureMessage}</span>
            </div>
          </div>
        )}

        <div className="flex items-center justify-end gap-2 pt-1">
          {failureMessage ? (
            <>
              <button
                type="button"
                onClick={closeConfirm}
                className="touch-hit-44 min-h-[44px] px-3 rounded-mc-sm text-mc-footnote font-medium text-mc-text-secondary hover:bg-mc-overlay-hover hover:text-mc-text-primary transition-colors motion-reduce:transition-none"
              >
                关闭
              </button>
              <button
                type="button"
                onClick={() => void executeMerge(failedStepFrom)}
                disabled={isMerging || !isConnected}
                className="touch-hit-44 min-h-[44px] px-4 rounded-mc-sm bg-mc-accent text-mc-on-accent text-mc-footnote font-medium hover:bg-mc-accent-hover active:bg-mc-accent-pressed disabled:opacity-45 transition-colors motion-reduce:transition-none"
              >
                重试
              </button>
            </>
          ) : !isMerging ? (
            <>
              <button
                type="button"
                onClick={closeConfirm}
                className="touch-hit-44 min-h-[44px] px-3 rounded-mc-sm text-mc-footnote font-medium text-mc-text-secondary hover:bg-mc-overlay-hover hover:text-mc-text-primary transition-colors motion-reduce:transition-none"
              >
                关闭
              </button>
              <button
                type="button"
                onClick={() => {
                  setConfirmOpen(false);
                  handleNewThread();
                }}
                className="touch-hit-44 min-h-[44px] px-4 rounded-mc-sm bg-mc-accent text-mc-on-accent text-mc-footnote font-medium hover:bg-mc-accent-hover active:bg-mc-accent-pressed transition-colors motion-reduce:transition-none inline-flex items-center gap-1.5"
              >
                <PlusIcon className="w-3.5 h-3.5 shrink-0" />
                <span>新建会话</span>
              </button>
            </>
          ) : null}
        </div>
      </div>
    );

  return (
    <>
      {isMerged ? (
        <div
          className="px-3 sm:px-4 pt-1"
          style={{ paddingBottom: 'var(--composer-bottom-inset, max(16px, env(safe-area-inset-bottom, 0px)))' }}
        >
          <div
            role="status"
            className="pr-merge-bar-enter flex items-center justify-between gap-3 px-3 py-2 rounded-mc-lg border border-mc-border bg-mc-surface-card"
            style={{
              backgroundColor: 'color-mix(in srgb, var(--mc-plan) 12%, var(--mc-surface-card))',
              borderColor: 'color-mix(in srgb, var(--mc-plan) 30%, var(--mc-border))'
            }}
          >
            <span className="flex items-center gap-2 min-w-0 text-mc-body font-medium text-mc-plan">
              <GitPullRequestIcon className="w-4 h-4 shrink-0" />
              <span className="truncate">{prNumber !== null ? `已合并 #${prNumber}` : '已合并'}</span>
            </span>
            <button
              type="button"
              onClick={handleNewThread}
              className="touch-hit-44 min-h-[36px] px-3 rounded-mc-sm border border-mc-border bg-mc-surface-raised text-mc-footnote font-medium text-mc-text-primary hover:bg-mc-overlay-hover active:bg-mc-overlay-pressed transition-colors motion-reduce:transition-none inline-flex items-center gap-1.5 shrink-0"
            >
              <PlusIcon className="w-3.5 h-3.5 shrink-0" />
              <span>新建会话</span>
            </button>
          </div>
        </div>
      ) : threadState ? (
        (() => {
          const btn = mergeButtonState(threadState);
          const actionDisabled = btn.disabled || isMerging || !mergeFlowSupported || !isConnected;
          return (
            <div className="px-3 sm:px-4 pt-1">
              <div className="pr-merge-bar-enter flex items-center justify-between gap-2 px-3 py-1.5 rounded-mc-lg border border-mc-border bg-mc-surface-card">
                <div className="flex items-center gap-2 min-w-0 flex-wrap">
                  <span className="inline-flex items-center gap-1 text-mc-footnote font-medium text-mc-text-primary font-mono">
                    <GitPullRequestIcon className="w-3.5 h-3.5 text-mc-accent shrink-0" />
                    <span>#{threadState.number}</span>
                  </span>
                  {threadState.localCommits > 0 && (
                    <span className="inline-flex items-center h-5 px-2 rounded-mc-pill bg-mc-accent-subtle text-mc-accent text-mc-caption font-medium tabular-nums">
                      +{threadState.localCommits} 提交
                    </span>
                  )}
                  {threadState.crossRepo && (
                    <span className="inline-flex items-center h-5 px-2 rounded-mc-pill bg-mc-warning/15 text-mc-warning text-mc-caption font-medium">
                      Fork
                    </span>
                  )}
                </div>
                <button
                  ref={triggerBtnRef}
                  type="button"
                  onClick={openConfirm}
                  disabled={actionDisabled}
                  aria-haspopup="dialog"
                  aria-expanded={confirmOpen}
                  className={`touch-hit-44 inline-flex items-center justify-center gap-1.5 h-8 px-3 rounded-mc-sm text-mc-footnote font-medium shrink-0 transition-colors motion-reduce:transition-none ${
                    btn.disabled
                      ? 'border border-mc-border bg-mc-surface-raised text-mc-text-secondary opacity-60'
                      : 'bg-mc-accent text-mc-on-accent hover:bg-mc-accent-hover active:bg-mc-accent-pressed disabled:opacity-45'
                  }`}
                >
                  {isMerging && (
                    <span className="loading-spinner loading-spinner--small shrink-0 motion-reduce:animate-none" />
                  )}
                  <span>{isMerging ? '合并中…' : btn.label}</span>
                </button>
              </div>
            </div>
          );
        })()
      ) : null}

      {confirmOpen && (
        <SheetFrame
          onRequestClose={closeConfirm}
          closeOnEscape={!isMerging}
          size="bare"
          cardRef={usePopoverMode ? undefined : sheetRef}
          ariaLabel={`合并 PR #${prNumber ?? threadState?.number ?? ''}`}
          layerClassName={usePopoverMode ? `${SHEET_LAYER_CLASS}--popover` : undefined}
          cardClassName={
            usePopoverMode
              ? 'settings-sheet model-popover pr-merge-popover !p-4'
              : 'settings-sheet pr-merge-sheet'
          }
          cardStyle={
            usePopoverMode && popoverAnchor
              ? {
                  left: popoverAnchor.left,
                  bottom: popoverAnchor.bottom,
                  maxHeight: popoverAnchor.maxHeight,
                  width: popoverAnchor.width,
                  top: 'auto',
                  background: 'var(--mc-surface-popover)',
                  boxShadow: 'var(--mc-elev-popover)'
                }
              : undefined
          }
        >
          {!usePopoverMode && <div ref={sheetHandleRef} className="sheet-handle" aria-hidden="true" />}
          {dialogBody}
        </SheetFrame>
      )}
    </>
  );
};
