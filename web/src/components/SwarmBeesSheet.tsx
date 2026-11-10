import React, { forwardRef, useEffect, useImperativeHandle, useMemo, useState } from 'react';
import type { RemoteSwarmMergeDiffResult, RemoteProvider } from '../types/generated';
import { modelDisplayName } from '../utils/modelLabel';
import type { RemoteSwarmBee, RemoteSwarmState } from '../types/swarm';
import { ConfirmDialog } from './ConfirmDialog';
import { beeDurationMs } from './SwarmCapsules';
import { SwarmInterruptedBanner } from './SwarmInterruptedBanner';
import { ChevronRightIcon, LockIcon } from './Icons';
import { MarkdownContent } from './ThreadTimeline';
import { LazyBoundary } from './LazyFallback';
import type { ThreadMenuPageHandle } from './thread/menu/menuPages';

// 合并审查抽屉带 diff 渲染，只在点开审查时才用到，做成懒加载。
const SwarmMergeReviewSheet = React.lazy(() =>
  import('./SwarmMergeReviewSheet').then((module) => ({ default: module.SwarmMergeReviewSheet }))
);

interface SwarmBeesSheetProps {
  isOpen: boolean;
  onClose: () => void;
  state: RemoteSwarmState | null;
  onStopBee: (beeThreadID: string) => Promise<void>;
  /** 宿主未声明批量方法时不传，对应按钮不显示。 */
  onStopAll?: () => Promise<void>;
  onClearFinished?: () => Promise<void>;
  /** 以下四项在宿主未声明对应合并/转交方法时不传，相关按钮不显示。 */
  onForwardQuestion?: (beeThreadID: string, requestID: string) => Promise<void>;
  getMergeDiff?: (beeThreadID: string) => Promise<RemoteSwarmMergeDiffResult>;
  onApproveMerge?: (beeThreadID: string) => Promise<void>;
  onDiscardMerge?: (beeThreadID: string) => Promise<void>;
  onOpenThread: (threadID: string) => void;
  /** 模型 ID → 显示名用；拿不到时原样显示 ID。 */
  providers?: RemoteProvider[];
}

/** 内容体 props：去掉 isOpen，入栈时挂载、出栈即卸载。 */
export type SwarmBeesSheetBodyProps = Omit<SwarmBeesSheetProps, 'isOpen'>;

/** 合并状态标签；无合并状态时返回 null。 */
function getMergeStateBadge(mergeState: RemoteSwarmBee['mergeState']): {
  label: string;
  className: string;
} | null {
  switch (mergeState) {
    case 'reviewing':
      return { label: '审查中', className: 'bg-sky-100 dark:bg-sky-950/40 text-sky-700 dark:text-sky-400' };
    case 'rejected':
      return { label: '审查未通过', className: 'bg-rose-100 dark:bg-rose-950/40 text-rose-700 dark:text-rose-400' };
    case 'pending':
      return { label: '待审查', className: 'bg-amber-100 dark:bg-amber-950/40 text-amber-700 dark:text-amber-400' };
    case 'merged':
      return { label: '已合并', className: 'bg-emerald-100 dark:bg-emerald-950/40 text-emerald-700 dark:text-emerald-400' };
    case 'discarded':
      return { label: '已放弃', className: 'bg-slate-100 dark:bg-zinc-800 text-slate-500 dark:text-zinc-400' };
    case 'failed':
      return { label: '合并失败', className: 'bg-rose-100 dark:bg-rose-950/40 text-rose-700 dark:text-rose-400' };
    default:
      return null;
  }
}

/** 审查决定层徽标；旧数据没有 reviewLayer 时返回 null。 */
function getReviewLayerBadge(reviewLayer: RemoteSwarmBee['reviewLayer']): {
  label: string;
  className: string;
} | null {
  switch (reviewLayer) {
    case 'l0':
      return { label: 'L0 检查', className: 'bg-slate-100 dark:bg-zinc-800 text-slate-600 dark:text-zinc-300' };
    case 'l1':
      return { label: 'L1 规则', className: 'bg-slate-100 dark:bg-zinc-800 text-slate-600 dark:text-zinc-300' };
    case 'l2':
      return { label: 'L2 模型', className: 'bg-sky-100 dark:bg-sky-950/40 text-sky-700 dark:text-sky-400' };
    case 'l3':
      return { label: 'L3 会话', className: 'bg-amber-100 dark:bg-amber-950/40 text-amber-700 dark:text-amber-400' };
    case 'human':
      return { label: '人工', className: 'bg-slate-100 dark:bg-zinc-800 text-slate-500 dark:text-zinc-400' };
    default:
      return null;
  }
}

/** 可信度徽章文案与展开清单；无统计数据时返回 null。 */
function getCredibilitySummary(bee: RemoteSwarmBee): {
  label: string;
  hasGaps: boolean;
  unverified: string[];
  notRead: string[];
} | null {
  const credibility = bee.credibility;
  if (!credibility) return null;
  const unverified = credibility.unverified ?? [];
  const notRead = credibility.notRead ?? [];
  return {
    label: `引用 ${credibility.cited} · 核实 ${credibility.verified}`,
    hasGaps: unverified.length > 0 || notRead.length > 0,
    unverified,
    notRead
  };
}

/** 距离自动转交的剩余时间文案；无 autoForwardAt 或解析失败时返回 null。 */
function getAutoForwardLabel(autoForwardAt: string | undefined, nowMs: number): string | null {
  if (!autoForwardAt) return null;
  const target = new Date(autoForwardAt).getTime();
  if (Number.isNaN(target)) return null;
  const seconds = Math.max(0, Math.ceil((target - nowMs) / 1000));
  return seconds > 0 ? `${seconds} 秒后自动转交` : '即将自动转交';
}

function formatDurationSeconds(seconds: number): string {
  if (seconds < 60) return `${seconds}秒`;
  const mins = Math.floor(seconds / 60);
  const remainingSecs = seconds % 60;
  if (mins < 60) return `${mins}分${remainingSecs}秒`;
  const hours = Math.floor(mins / 60);
  const remainingMins = mins % 60;
  return `${hours}小时${remainingMins}分`;
}

function getBeeDuration(bee: RemoteSwarmBee, nowMs: number): string {
  const durationMs = beeDurationMs(bee, nowMs);
  if (durationMs === null) return '';
  const prefix = bee.status === 'running' ? '已耗时' : '耗时';
  return `${prefix} ${formatDurationSeconds(Math.floor(durationMs / 1000))}`;
}

function getStatusBadge(bee: RemoteSwarmBee, nowMs: number): {
  label: string;
  className: string;
} {
  const durationText = getBeeDuration(bee, nowMs);
  const withDuration = (text: string) => (durationText ? `${text} · ${durationText}` : text);

  switch (bee.status) {
    case 'running':
      return {
        label: withDuration('运行中'),
        className: 'text-sky-600 dark:text-sky-400 font-medium'
      };
    case 'failed':
      return {
        label: withDuration('失败'),
        className: 'text-rose-600 dark:text-rose-400 font-medium'
      };
    case 'stopped':
      return {
        label: withDuration('已停止'),
        className: 'text-slate-400 dark:text-zinc-500'
      };
    case 'completed':
      return {
        label: withDuration('已完成'),
        className: 'text-slate-500 dark:text-zinc-400'
      };
    default:
      return {
        label: bee.status,
        className: 'text-slate-400 dark:text-zinc-500'
      };
  }
}

export const SwarmBeesSheetBody = forwardRef<ThreadMenuPageHandle, SwarmBeesSheetBodyProps>(
  function SwarmBeesSheetBody(
    {
      onClose,
      state,
      onStopBee,
      onStopAll,
      onClearFinished,
      onForwardQuestion,
      getMergeDiff,
      onApproveMerge,
      onDiscardMerge,
      onOpenThread,
      providers
    },
    ref
  ) {
    const [nowMs, setNowMs] = useState(() => Date.now());
    const [expandedTasks, setExpandedTasks] = useState<Record<string, boolean>>({});
    const [expandedSummaries, setExpandedSummaries] = useState<Record<string, boolean>>({});
    const [expandedCredibility, setExpandedCredibility] = useState<Record<string, boolean>>({});
    // 单个子代理的操作错误（转交提问）按 threadID 存放
    const [beeActionErrors, setBeeActionErrors] = useState<Record<string, string>>({});
    const [forwardingBeeID, setForwardingBeeID] = useState<string | null>(null);
    // 合并审查抽屉：存 beeThreadID，bee 变化（合并完成）时跟最新状态
    const [reviewBeeThreadID, setReviewBeeThreadID] = useState<string | null>(null);
    const [stoppingBee, setStoppingBee] = useState<RemoteSwarmBee | null>(null);
    const [isStopping, setIsStopping] = useState(false);
    // 批量操作：二次确认后再执行
    const [batchConfirm, setBatchConfirm] = useState<'stopAll' | 'clearFinished' | null>(null);
    const [isBatchRunning, setIsBatchRunning] = useState(false);
    const [batchError, setBatchError] = useState<string | null>(null);

    // 定时刷新运行中的已耗时；内容体只在入栈时挂载，等价于原来的打开态
    useEffect(() => {
      const interval = setInterval(() => setNowMs(Date.now()), 1000);
      return () => clearInterval(interval);
    }, []);

    // 无关闭守卫：本页没有未完成输入，直接允许出栈
    useImperativeHandle(ref, () => ({ requestClose: () => true }), []);

    const sortedBees = useMemo(() => {
      if (!state?.bees) return [];
      return [...state.bees].sort((a, b) => a.index - b.index);
    }, [state?.bees]);

    const reviewBee = useMemo(
      () => (reviewBeeThreadID ? sortedBees.find((item) => item.threadID === reviewBeeThreadID) ?? null : null),
      [reviewBeeThreadID, sortedBees]
    );

    // 未转交的提问才需要主线程来处理
    const awaitingAnswerCount = useMemo(
      () => sortedBees.filter((bee) => bee.pendingQuestion && !bee.pendingQuestion.forwarded).length,
      [sortedBees]
    );
    const mergeReviewSupported = Boolean(getMergeDiff && onApproveMerge && onDiscardMerge);

    // 运行中 + 排队 > 1 才有“停止全部”的意义（单个子代理用行内“停止”）；已结束 = 非 running
    const runningCount = sortedBees.filter((bee) => bee.status === 'running').length;
    const finishedCount = sortedBees.length - runningCount;
    const completedCount = sortedBees.filter((bee) => bee.status === 'completed').length;
    // 排队中的子代理还没启动，但属于未结束的工作：算进“进行中”，停止全部也能一并清空队列
    const queuedCount = state?.queued ?? 0;
    // 中断续跑挂在主会话上：bee 视角的状态里 leadThreadID 指向所属主会话
    const interruptedLeadID = state?.leadThreadID || state?.threadID || '';
    const showStopAll = Boolean(onStopAll) && runningCount + queuedCount > 1;
    const showClearFinished = Boolean(onClearFinished) && finishedCount > 0;

    const toggleTaskExpanded = (threadID: string) => {
      setExpandedTasks((prev) => ({ ...prev, [threadID]: !prev[threadID] }));
    };

    const toggleSummaryExpanded = (threadID: string) => {
      setExpandedSummaries((prev) => ({ ...prev, [threadID]: !prev[threadID] }));
    };

    const handleConfirmStop = async () => {
      if (!stoppingBee) return;
      setIsStopping(true);
      try {
        await onStopBee(stoppingBee.threadID);
        setStoppingBee(null);
      } finally {
        setIsStopping(false);
      }
    };

    const handleConfirmBatch = async () => {
      const action = batchConfirm === 'stopAll' ? onStopAll : batchConfirm === 'clearFinished' ? onClearFinished : undefined;
      if (!batchConfirm || !action) return;
      setIsBatchRunning(true);
      setBatchError(null);
      try {
        await action();
        setBatchConfirm(null);
      } catch (error) {
        setBatchConfirm(null);
        setBatchError(error instanceof Error ? error.message : '批量操作失败，请稍后重试');
      } finally {
        setIsBatchRunning(false);
      }
    };

    const handleOpenThread = (threadID: string) => {
      onOpenThread(threadID);
      onClose();
    };

    // 把子代理提问转交主线程回答；失败时把错误挂在该 bee 卡片上
    const handleForwardQuestion = async (bee: RemoteSwarmBee) => {
      const question = bee.pendingQuestion;
      if (!question || !onForwardQuestion || forwardingBeeID) return;
      setForwardingBeeID(bee.threadID);
      setBeeActionErrors((prev) => ({ ...prev, [bee.threadID]: '' }));
      try {
        await onForwardQuestion(bee.threadID, question.requestID);
      } catch (error) {
        setBeeActionErrors((prev) => ({
          ...prev,
          [bee.threadID]: error instanceof Error ? error.message : '转交失败，请稍后重试'
        }));
      } finally {
        setForwardingBeeID(null);
      }
    };

    return (
      <div className="flex flex-col flex-1 min-h-0 min-w-0">
        <div className="flex-1 min-h-0 overflow-y-auto">
          {/* 重启中断续跑：计数不在 swarm 状态载荷里，横幅自己查询并在状态变更后刷新 */}
          {interruptedLeadID && <SwarmInterruptedBanner threadID={interruptedLeadID} />}

          {/* 进度总览：已完成数与排队数；只看 bees 会把纯排队误显示成已经结束 */}
          {(sortedBees.length > 0 || queuedCount > 0) && (
            <p className="mb-3 text-xs text-[var(--foreground-muted)]">
              {`已完成 ${completedCount}/${sortedBees.length}${queuedCount > 0 ? `，${queuedCount} 个排队中` : ''}`}
            </p>
          )}

          {/* 未转交的提问：主会话需要人手回复 */}
          {awaitingAnswerCount > 0 && (
            <p className="mb-3 flex items-center gap-1.5 text-xs font-medium text-amber-600 dark:text-amber-400">
              <span className="grid h-4 w-4 place-items-center rounded-full bg-amber-500 text-mc-caption font-bold text-white shrink-0">?</span>
              <span>{`${awaitingAnswerCount} 个子代理在等你回答`}</span>
            </p>
          )}

          {/* 批量操作：停止全部运行中的子代理 / 清除已结束的子代理 */}
          {(showStopAll || showClearFinished) && (
            <div className="flex flex-wrap items-center justify-end gap-2 mb-3">
              {showStopAll && (
                <button
                  type="button"
                  onClick={() => {
                    setBatchError(null);
                    setBatchConfirm('stopAll');
                  }}
                  disabled={isBatchRunning}
                  className="min-h-[36px] px-3 rounded-xl text-xs font-medium border border-rose-200 dark:border-rose-900/40 bg-rose-50 dark:bg-rose-950/20 hover:bg-rose-100 text-rose-600 dark:text-rose-400 disabled:opacity-45 transition"
                >
                  停止全部
                </button>
              )}
              {showClearFinished && (
                <button
                  type="button"
                  onClick={() => {
                    setBatchError(null);
                    setBatchConfirm('clearFinished');
                  }}
                  disabled={isBatchRunning}
                  className="min-h-[36px] px-3 rounded-xl text-xs font-medium border border-slate-200 dark:border-zinc-700 bg-white dark:bg-zinc-800 hover:bg-slate-50 dark:hover:bg-zinc-700 text-slate-700 dark:text-slate-200 disabled:opacity-45 transition"
                >
                  清除已完成
                </button>
              )}
            </div>
          )}
          {batchError && (
            <p role="alert" className="mb-3 text-xs text-[var(--danger-foreground)] break-words">
              {batchError}
            </p>
          )}

          {/* 列表内容 */}
          {sortedBees.length === 0 ? (
            <div className="py-12 text-center text-xs text-slate-400 dark:text-zinc-500 space-y-2">
              <div className="text-2xl">🌱</div>
              <p>{queuedCount > 0 ? `${queuedCount} 个排队中，等待启动` : '暂无子代理记录'}</p>
            </div>
          ) : (
            <div className="space-y-3 pb-2">
              {sortedBees.map((bee) => {
                const statusInfo = getStatusBadge(bee, nowMs);
                const isTaskExpanded = Boolean(expandedTasks[bee.threadID]);
                const isSummaryExpanded = Boolean(expandedSummaries[bee.threadID]);
                const isCredibilityExpanded = Boolean(expandedCredibility[bee.threadID]);
                const mergeStateInfo = getMergeStateBadge(bee.mergeState);
                const reviewLayerInfo = getReviewLayerBadge(bee.reviewLayer);
                const credibilityInfo = getCredibilitySummary(bee);
                const question = bee.pendingQuestion;
                const autoForwardText = getAutoForwardLabel(question?.autoForwardAt, nowMs);
                const conflictFiles = bee.conflictFiles ?? [];
                const changedFileCount = bee.changedFiles?.length ?? 0;

                return (
                  <article
                    key={bee.threadID}
                    className="p-3.5 rounded-2xl border border-slate-200/80 dark:border-zinc-800 bg-white/60 dark:bg-zinc-900/40 space-y-2.5 transition"
                  >
                    {/* 头部：名称与状态 */}
                    <div className="flex items-center justify-between gap-2 text-xs">
                      <div
                        onClick={bee.canOpen ? () => handleOpenThread(bee.threadID) : undefined}
                        className={`flex items-center gap-1.5 min-w-0 font-semibold text-slate-800 dark:text-slate-100 ${
                          bee.canOpen ? 'cursor-pointer hover:underline' : ''
                        }`}
                        title={bee.canOpen ? '点击打开会话时间线' : undefined}
                      >
                        <span className="w-2 h-2 rounded-full shrink-0 bg-sky-500" />
                        <span className="truncate">{bee.name || `子代理 #${bee.index}`}</span>
                        {bee.model && (
                          <span className="px-1.5 py-0.5 rounded text-mc-caption font-normal bg-slate-100 dark:bg-zinc-800 text-slate-500 dark:text-zinc-400 shrink-0">
                            {modelDisplayName(providers, bee.provider, bee.model)}
                          </span>
                        )}
                      </div>
                      <span className={`text-mc-caption shrink-0 ${statusInfo.className}`}>
                        {statusInfo.label}
                      </span>
                    </div>

                    {/* 档位 / 只读锁定 / 合并状态 / 改动规模 */}
                    <div className="flex flex-wrap items-center gap-1.5 text-mc-caption min-w-0">
                      {bee.tier && (
                        <span className="px-1.5 py-0.5 rounded bg-slate-100 dark:bg-zinc-800 text-slate-600 dark:text-zinc-300 shrink-0">
                          {bee.tier}
                        </span>
                      )}
                      {bee.readOnly && (
                        <span
                          className="inline-flex items-center gap-0.5 px-1.5 py-0.5 rounded bg-slate-100 dark:bg-zinc-800 text-slate-600 dark:text-zinc-300 shrink-0"
                          title="只读档：改动不会落回项目"
                        >
                          <LockIcon className="h-3 w-3" />
                          只读
                        </span>
                      )}
                      {bee.shadowRepo && (
                        <span
                          className="inline-flex items-center gap-0.5 px-1.5 py-0.5 rounded bg-slate-100 dark:bg-zinc-800 text-slate-600 dark:text-zinc-300 shrink-0"
                          title="项目不是 Git 仓库，改动保存在影子仓库中，合入时直接写成文件"
                        >
                          影子仓库 · 非 Git 项目
                        </span>
                      )}
                      {bee.sharedNoGit && (
                        <span
                          className="inline-flex items-center gap-0.5 px-1.5 py-0.5 rounded bg-slate-100 dark:bg-zinc-800 text-slate-600 dark:text-zinc-300 shrink-0"
                          title={bee.sharedNoGitReason || '共享目录且无法隔离：子代理改动可能互相覆盖'}
                        >
                          共享目录 · 无隔离
                        </span>
                      )}
                      {mergeStateInfo && (
                        <span className={`px-1.5 py-0.5 rounded font-medium shrink-0 ${mergeStateInfo.className}`}>
                          {mergeStateInfo.label}
                        </span>
                      )}
                      {changedFileCount > 0 && (
                        <span className="text-slate-500 dark:text-zinc-400 shrink-0">
                          {`${changedFileCount} 个文件 `}
                          <span className="text-emerald-600 dark:text-emerald-400">{`+${bee.linesAdded ?? 0}`}</span>
                          <span className="text-rose-600 dark:text-rose-400">{` −${bee.linesDeleted ?? 0}`}</span>
                        </span>
                      )}
                    </div>

                    {/* 冲突文件与合并错误（从独立字段读，不靠 summary 文字） */}
                    {conflictFiles.length > 0 && (
                      <p className="text-mc-caption text-rose-600 dark:text-rose-400 break-words [overflow-wrap:anywhere]">
                        <span>冲突文件：</span>
                        <span>{conflictFiles.slice(0, 5).join('、')}</span>
                        {conflictFiles.length > 5 && <span>{` 等 ${conflictFiles.length} 个`}</span>}
                      </p>
                    )}
                    {bee.mergeError && (
                      <p className="text-mc-caption text-rose-600 dark:text-rose-400 break-words [overflow-wrap:anywhere]">
                        {bee.mergeError}
                      </p>
                    )}

                    {/* 任务描述（可展开） */}
                    <div className="text-xs text-slate-700 dark:text-slate-300">
                      <p
                        onClick={() => toggleTaskExpanded(bee.threadID)}
                        className={`cursor-pointer ${isTaskExpanded ? '' : 'line-clamp-2'} leading-relaxed`}
                        title={isTaskExpanded ? '点击收起任务描述' : '点击展开全部任务描述'}
                      >
                        {bee.task || '（无任务说明）'}
                      </p>
                    </div>

                    {/* 运行中的实时动态 */}
                    {bee.status === 'running' && bee.activity && (
                      <div className="flex items-center gap-1.5 text-mc-caption text-sky-600 dark:text-sky-400 bg-sky-50/70 dark:bg-sky-950/30 px-2 py-1 rounded-lg">
                        <span className="w-1.5 h-1.5 rounded-full bg-sky-500 animate-pulse shrink-0" />
                        <span className="truncate">{bee.activity}</span>
                      </div>
                    )}

                    {/* 元数据行：工具调用次数 / Token 数 */}
                    <div className="flex flex-wrap items-center gap-3 text-mc-caption text-slate-400 dark:text-zinc-500">
                      <span>{bee.toolCalls} 次工具调用</span>
                      {bee.tokens > 0 && <span>{bee.tokens.toLocaleString()} tokens</span>}
                      {credibilityInfo && (
                        <button
                          type="button"
                          onClick={() =>
                            setExpandedCredibility((prev) => ({ ...prev, [bee.threadID]: !prev[bee.threadID] }))
                          }
                          className={`px-1.5 py-0.5 rounded border font-medium transition ${
                            credibilityInfo.hasGaps
                              ? 'border-amber-300/70 dark:border-amber-900/50 bg-amber-50 dark:bg-amber-950/30 text-amber-700 dark:text-amber-400'
                              : 'border-slate-200 dark:border-zinc-700 bg-slate-50 dark:bg-zinc-800 text-slate-600 dark:text-zinc-300'
                          }`}
                          title={credibilityInfo.hasGaps ? '存在未核实或未读条目，点击展开' : '可信度统计'}
                        >
                          {credibilityInfo.label}
                        </button>
                      )}
                    </div>

                    {/* 可信度清单（未核实 / 未读） */}
                    {credibilityInfo && isCredibilityExpanded && (
                      <div className="space-y-1.5 p-2.5 rounded-xl bg-amber-50/70 dark:bg-amber-950/20 text-mc-caption text-slate-700 dark:text-slate-300">
                        {credibilityInfo.unverified.length > 0 && (
                          <div className="space-y-0.5">
                            <p className="font-medium text-amber-700 dark:text-amber-400">
                              {`未核实 ${credibilityInfo.unverified.length} 条`}
                            </p>
                            <ul className="list-disc pl-4 space-y-0.5 break-words [overflow-wrap:anywhere]">
                              {credibilityInfo.unverified.map((item, index) => (
                                <li key={`unverified-${index}`}>{item}</li>
                              ))}
                            </ul>
                          </div>
                        )}
                        {credibilityInfo.notRead.length > 0 && (
                          <div className="space-y-0.5">
                            <p className="font-medium text-amber-700 dark:text-amber-400">
                              {`未读 ${credibilityInfo.notRead.length} 条`}
                            </p>
                            <ul className="list-disc pl-4 space-y-0.5 break-words [overflow-wrap:anywhere]">
                              {credibilityInfo.notRead.map((item, index) => (
                                <li key={`not-read-${index}`}>{item}</li>
                              ))}
                            </ul>
                          </div>
                        )}
                        {!credibilityInfo.hasGaps && (
                          <p className="text-slate-500 dark:text-zinc-400">没有未核实或未读条目。</p>
                        )}
                      </div>
                    )}

                    {/* 子代理提问：可去回答，也可转交主线程 */}
                    {question && (
                      <div className="space-y-1.5 p-2.5 rounded-xl border border-amber-200 dark:border-amber-900/40 bg-amber-50/70 dark:bg-amber-950/20 text-xs">
                        <p className="text-mc-caption font-semibold text-amber-700 dark:text-amber-400">
                          {question.forwarded ? '提问已转交主线程' : '子代理在等你回答'}
                        </p>
                        <p className="text-slate-700 dark:text-slate-200 leading-relaxed whitespace-pre-wrap break-words [overflow-wrap:anywhere]">
                          {question.prompt}
                        </p>
                        {autoForwardText && (
                          <p className="text-mc-caption text-amber-700 dark:text-amber-400">{autoForwardText}</p>
                        )}
                        {beeActionErrors[bee.threadID] && (
                          <p role="alert" className="text-mc-caption text-[var(--danger-foreground)] break-words">
                            {beeActionErrors[bee.threadID]}
                          </p>
                        )}
                        <div className="flex flex-wrap items-center justify-end gap-2">
                          <button
                            type="button"
                            onClick={() => handleOpenThread(bee.threadID)}
                            className="min-h-[32px] px-3 rounded-xl text-xs font-medium bg-[var(--primary)] text-white active:scale-95 transition"
                          >
                            去回答
                          </button>
                          {question.forwarded ? (
                            <span className="min-h-[32px] inline-flex items-center px-3 rounded-xl border border-slate-200 dark:border-zinc-700 text-mc-caption font-medium text-slate-500 dark:text-zinc-400">
                              已转交
                            </span>
                          ) : onForwardQuestion ? (
                            <button
                              type="button"
                              onClick={() => void handleForwardQuestion(bee)}
                              disabled={forwardingBeeID === bee.threadID}
                              className="min-h-[32px] px-3 rounded-xl text-xs font-medium border border-slate-200 dark:border-zinc-700 bg-white dark:bg-zinc-800 hover:bg-slate-50 dark:hover:bg-zinc-700 text-slate-700 dark:text-slate-200 disabled:opacity-45 transition"
                            >
                              {forwardingBeeID === bee.threadID ? '转交中…' : '交给主线程回答'}
                            </button>
                          ) : null}
                        </div>
                      </div>
                    )}

                    {/* 总结（Markdown 渲染，可折叠） */}
                    {bee.summary && (
                      <div className="pt-1 border-t border-slate-100 dark:border-zinc-800/80">
                        <button
                          type="button"
                          onClick={() => toggleSummaryExpanded(bee.threadID)}
                          className="flex items-center gap-1 text-mc-caption font-medium text-slate-600 dark:text-slate-300 hover:text-sky-500 dark:hover:text-sky-400 transition"
                        >
                          <ChevronRightIcon
                            className={`w-3.5 h-3.5 transition-transform duration-200 ${
                              isSummaryExpanded ? 'rotate-90' : ''
                            }`}
                          />
                          <span>{isSummaryExpanded ? '收起总结' : '查看总结'}</span>
                        </button>
                        {isSummaryExpanded && (
                          <div className="mt-2 p-2.5 rounded-xl bg-slate-50 dark:bg-zinc-800/60 text-xs">
                            <MarkdownContent text={bee.summary} prefix={`bee-summary-${bee.threadID}`} />
                          </div>
                        )}
                      </div>
                    )}

                    {/* 行动作栏 */}
                    <div className="flex items-center justify-end gap-2 pt-1 border-t border-slate-100 dark:border-zinc-800/60">
                      {bee.canOpen && (
                        <button
                          type="button"
                          onClick={() => handleOpenThread(bee.threadID)}
                          className="min-h-[36px] px-3 rounded-xl text-xs font-medium border border-slate-200 dark:border-zinc-700 bg-white dark:bg-zinc-800 hover:bg-slate-50 dark:hover:bg-zinc-700 text-slate-700 dark:text-slate-200 transition"
                        >
                          打开会话
                        </button>
                      )}
                      {bee.canStop && (
                        <button
                          type="button"
                          onClick={() => setStoppingBee(bee)}
                          className="min-h-[36px] px-3 rounded-xl text-xs font-medium border border-rose-200 dark:border-rose-900/40 bg-rose-50 dark:bg-rose-950/20 hover:bg-rose-100 text-rose-600 dark:text-rose-400 transition"
                        >
                          停止
                        </button>
                      )}
                      {reviewLayerInfo && (
                        <span
                          className={`px-1.5 py-0.5 rounded text-mc-caption font-medium shrink-0 ${reviewLayerInfo.className}`}
                          title="审查停在这一层"
                        >
                          {reviewLayerInfo.label}
                        </span>
                      )}
                      {(bee.mergeState === 'pending' || bee.mergeState === 'rejected') && mergeReviewSupported && (
                        <button
                          type="button"
                          onClick={() => setReviewBeeThreadID(bee.threadID)}
                          className="min-h-[36px] px-3 rounded-xl text-xs font-medium border border-amber-200 dark:border-amber-900/40 bg-amber-50 dark:bg-amber-950/20 hover:bg-amber-100 text-amber-700 dark:text-amber-400 transition"
                        >
                          {bee.mergeState === 'rejected' ? '查看审查意见' : '审查改动'}
                        </button>
                      )}
                    </div>
                  </article>
                );
              })}
            </div>
          )}
        </div>

        {/* 合并审查抽屉 */}
        {reviewBee && getMergeDiff && onApproveMerge && onDiscardMerge && (
          <LazyBoundary label="正在加载合并审查…">
            <SwarmMergeReviewSheet
              bee={reviewBee}
              onClose={() => setReviewBeeThreadID(null)}
              getMergeDiff={getMergeDiff}
              onApproveMerge={onApproveMerge}
              onDiscardMerge={onDiscardMerge}
              onOpenReviewThread={handleOpenThread}
              reviewLayer={reviewBee.reviewLayer}
              reviewConcerns={reviewBee.reviewConcerns}
              reviewTokens={reviewBee.reviewTokens}
              reviewTokensEstimated={reviewBee.reviewTokensEstimated}
            />
          </LazyBoundary>
        )}

        {/* 停止确认弹窗 */}
        <ConfirmDialog
          isOpen={Boolean(stoppingBee)}
          title="停止子代理"
          message={`确定要停止“${stoppingBee?.name || '当前子代理'}”的执行吗？已完成的工作将保留。`}
          confirmLabel="停止"
          confirmVariant="danger"
          isLoading={isStopping}
          onConfirm={() => void handleConfirmStop()}
          onCancel={() => {
            if (!isStopping) setStoppingBee(null);
          }}
        />

        {/* 批量操作确认弹窗 */}
        <ConfirmDialog
          isOpen={batchConfirm !== null}
          title={batchConfirm === 'stopAll' ? '停止全部子代理' : '清除已完成的子代理'}
          message={
            batchConfirm === 'stopAll'
              ? `确定要停止全部 ${runningCount + queuedCount} 个子代理吗？已完成的工作将保留。`
              : `确定要把 ${finishedCount} 个已结束的子代理移出面板吗？它们的工作副本会被归还，运行中的子代理不受影响。`
          }
          confirmLabel={batchConfirm === 'stopAll' ? '全部停止' : '清除'}
          confirmVariant={batchConfirm === 'stopAll' ? 'danger' : 'primary'}
          isLoading={isBatchRunning}
          onConfirm={() => void handleConfirmBatch()}
          onCancel={() => {
            if (!isBatchRunning) setBatchConfirm(null);
          }}
        />
      </div>
    );
  }
);

