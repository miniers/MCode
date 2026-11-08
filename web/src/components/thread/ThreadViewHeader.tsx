// 会话头部：返回键、标题、副标题（项目 · 分支/工作区）、搜索与合并后的 ⋯ 菜单入口

import React from 'react';
import type { ConnectionState, RemoteThreadSummary } from '../../types/contract';
import type { RemoteSwarmState } from '../../types/swarm';
import { ChevronRightIcon, EllipsisHorizontalIcon, SearchIcon } from '../Icons';
import { isThreadBusy } from '../../context/remoteGuards';
import { backgroundWaitCount, backgroundWaitStatusText } from '../../utils/backgroundWait';
import { ThreadChangeStats } from './ThreadChangeStats';

export interface ThreadViewHeaderProps {
  onBack?: () => void;
  latestSummary: RemoteThreadSummary;
  subtitle: string;
  showFloatingStatus: boolean;
  isConnected: boolean;
  connectionState: ConnectionState;
  reconnect: () => void;
  turnElapsedSeconds: number;
  swarmState: RemoteSwarmState | null;
  /** 子代理页头部状态：文字与配色由调用方用 getHelperStatusDisplay 生成 */
  beeStatusLabel?: string;
  beeStatusClassName?: string;
  beeStatusDotClassName?: string;
  /** 子代理运行中的已耗时（秒），头部按 m:ss 显示 */
  beeElapsedSeconds?: number;
  beeMenuOpen?: boolean;
  setBeeMenuOpen?: React.Dispatch<React.SetStateAction<boolean>>;
  setIsRenaming: React.Dispatch<React.SetStateAction<boolean>>;
  setRenameDraft: React.Dispatch<React.SetStateAction<string>>;
  setMenuSheetOpen: React.Dispatch<React.SetStateAction<boolean>>;
  /** ⋯ 按钮锚点：宽屏菜单用它把 popover 定位到按钮下方；Bee 会话不挂 */
  menuButtonRef?: React.RefObject<HTMLButtonElement>;
  /** 会话内查找条是否展开：展开时搜索按钮高亮 */
  findOpen: boolean;
  onToggleFind: () => void;
  /** 会话 ID：改动统计由 ThreadChangeStats 内部自行拉取，避免统计数字进入本组件的 memo props。 */
  threadID: string;
  isRunning: boolean;
  /** 点击改动统计胶囊：打开审阅/改动面板，不传时胶囊只做展示 */
  onOpenChanges?: () => void;
}

// React.memo：会话页流式输出期间，这些 props 的引用不随增量变化，头部不必跟着重渲染。
export const ThreadViewHeader = React.memo(function ThreadViewHeader({
  onBack,
  latestSummary,
  subtitle,
  showFloatingStatus,
  isConnected,
  connectionState,
  reconnect,
  turnElapsedSeconds,
  swarmState,
  beeStatusLabel,
  beeStatusClassName,
  beeStatusDotClassName,
  beeElapsedSeconds = 0,
  beeMenuOpen,
  setBeeMenuOpen,
  setIsRenaming,
  setRenameDraft,
  setMenuSheetOpen,
  menuButtonRef,
  findOpen,
  onToggleFind,
  threadID,
  isRunning,
  onOpenChanges
}: ThreadViewHeaderProps) {
  // 排队中的子代理也算未结束，⋯ 上的指示点与角标不能漏掉它们
  const runningBeeCount = swarmState?.bees?.filter((h) => h.status === 'running').length ?? 0;
  const activeBeeCount = runningBeeCount + (swarmState?.queued ?? 0);
  // 空闲但仍在等后台任务：也给状态占位，否则顶栏看着像已经彻底结束。
  const backgroundWait = backgroundWaitCount(latestSummary);
  return (
    <div
      // iOS 26 起，系统只在顶部有不透明 fixed/sticky 元素时才不画顶部模糊，
      // 所以顶栏保持不透明背景并吸顶，不用 backdrop 模糊。
      className="sticky top-0 z-20 shrink-0 flex items-center gap-2 border-b border-[var(--border)] bg-[var(--screen)]"
      style={{
        paddingTop: 'calc(var(--sat) + 0.5rem)',
        paddingBottom: '0.5rem',
        paddingLeft: 'max(0.75rem, calc(0.75rem + var(--sal)))',
        paddingRight: 'max(0.75rem, calc(0.75rem + var(--sar)))'
      }}
    >
      {onBack && (
        <button
          type="button"
          onClick={onBack}
          aria-label="返回会话列表"
          className="min-h-[44px] min-w-[44px] inline-flex items-center justify-center gap-0.5 -ml-1.5 px-2 rounded-xl text-xs font-medium text-[var(--foreground)] hover:bg-[var(--user-bubble)] transition active:scale-95"
        >
          <ChevronRightIcon className="w-4 h-4 rotate-180" />
          <span>会话</span>
        </button>
      )}
      <div className="min-w-0 flex-1">
        <div className="text-[16px] sm:text-mc-page-title font-semibold text-[var(--foreground)] truncate leading-tight">
          {latestSummary.title || '未命名会话'}
        </div>
        <div className="min-w-0 flex items-center gap-2 text-mc-footnote text-[var(--foreground-muted)] leading-normal">
          <span className="flex-1 min-w-0 truncate">{subtitle}</span>
          {/* 子代理页永远显示自己的状态；主会话页保持原先的按需显示 */}
          {(swarmState?.isBee || showFloatingStatus || backgroundWait > 0) && (
            !isConnected ? (
              <button
                type="button"
                onClick={() => void reconnect()}
                aria-label={connectionState === 'reconnecting' ? '正在重连' : '未连接，点击重连'}
                className="shrink-0 inline-flex items-center gap-1.5 text-mc-caption sm:text-mc-footnote text-[var(--danger-foreground)] hover:underline active:opacity-75 -my-0.5 py-0.5 px-1 -mx-1 rounded"
              >
                <span
                  className={`w-2 h-2 rounded-full shrink-0 ${
                    connectionState === 'reconnecting'
                      ? 'bg-[var(--warning-foreground)]'
                      : 'bg-[var(--danger-foreground)]'
                  }`}
                />
                <span className="tabular-nums">
                  {connectionState === 'reconnecting' ? '正在重连…' : '未连接 · 点击重连'}
                </span>
              </button>
            ) : swarmState?.isBee ? (
              <span
                role="status"
                aria-live="off"
                className={`shrink-0 inline-flex items-center gap-1.5 text-mc-caption sm:text-mc-footnote tabular-nums ${beeStatusClassName ?? ''}`}
              >
                <span
                  className={`w-2 h-2 rounded-full shrink-0 ${beeStatusDotClassName ?? 'bg-[var(--foreground-muted)]'}`}
                />
                <span>
                  {swarmState.bee?.status === 'running'
                    ? `${beeStatusLabel ?? ''} ${Math.floor(beeElapsedSeconds / 60)}:${(beeElapsedSeconds % 60)
                        .toString()
                        .padStart(2, '0')}`
                    : beeStatusLabel}
                </span>
              </span>
            ) : (
              <span
                role="status"
                aria-live="off"
                className="shrink-0 inline-flex items-center gap-1.5 text-mc-caption sm:text-mc-footnote tabular-nums"
              >
                <span
                  className={`w-2 h-2 rounded-full shrink-0 ${
                    isThreadBusy(latestSummary)
                      ? 'bg-[var(--primary)] animate-pulse'
                      : backgroundWait > 0
                        ? 'bg-[var(--primary)] animate-pulse motion-reduce:animate-none'
                        : 'bg-[var(--warning-foreground)]'
                  }`}
                />
                <span>
                  {isThreadBusy(latestSummary)
                    ? latestSummary.isRunning
                      ? `运行中 ${Math.floor(turnElapsedSeconds / 60)}:${(turnElapsedSeconds % 60).toString().padStart(2, '0')}`
                      : '运行中'
                    : backgroundWait > 0
                      ? backgroundWaitStatusText(backgroundWait)
                      : '正在同步…'}
                </span>
              </span>
            )
          )}
        </div>
      </div>
      {/* 会话改动统计：零改动时不渲染，点击打开改动面板 */}
      <ThreadChangeStats threadID={threadID} isRunning={isRunning} onOpen={onOpenChanges} />
      <button
        type="button"
        onClick={onToggleFind}
        aria-label="在会话内查找"
        aria-expanded={findOpen}
        className={`min-h-[44px] min-w-[44px] inline-flex items-center justify-center rounded-xl transition active:scale-95 ${
          findOpen
            ? 'bg-[var(--user-bubble)] text-[var(--primary)]'
            : 'text-[var(--foreground-muted)] hover:bg-[var(--user-bubble)] hover:text-[var(--foreground)]'
        }`}
      >
        <SearchIcon className="w-4 h-4" />
      </button>
      <button
        ref={swarmState?.isBee ? undefined : menuButtonRef}
        type="button"
        onClick={() => {
          // 子代理页只开精简菜单
          if (swarmState?.isBee) {
            setBeeMenuOpen?.((open) => !open);
            return;
          }
          setIsRenaming(false);
          setRenameDraft(latestSummary.title || '');
          setMenuSheetOpen(true);
        }}
        aria-label={
          swarmState?.isBee
            ? '子代理操作'
            : activeBeeCount > 0
              ? `会话操作，${activeBeeCount} 个子代理进行中`
              : '会话操作'
        }
        aria-expanded={swarmState?.isBee ? Boolean(beeMenuOpen) : undefined}
        className="relative min-h-[44px] min-w-[44px] inline-flex items-center justify-center rounded-xl text-[var(--foreground-muted)] hover:bg-[var(--user-bubble)] hover:text-[var(--foreground)] transition active:scale-95"
      >
        <EllipsisHorizontalIcon className="w-5 h-5" />
        {/* 原「工作区」按钮上的脉冲点与角标合并到这里：数字=运行中+排队，脉冲=有运行中的子代理 */}
        {!swarmState?.isBee && activeBeeCount > 0 && (
          <span
            className={`absolute right-0.5 top-0.5 inline-flex h-4 min-w-[16px] items-center justify-center rounded-full bg-[var(--primary)] px-1 text-mc-caption font-semibold leading-none text-white ${
              runningBeeCount > 0 ? 'animate-pulse' : ''
            }`}
          >
            {activeBeeCount}
          </span>
        )}
      </button>
    </div>
  );
});
