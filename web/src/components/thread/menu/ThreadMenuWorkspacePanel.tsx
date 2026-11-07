// 会话更多操作面板的「工作区」子页：本地/Worktree 模式切换、移到 worktree 入口与执行结果提示。
// worktreeOpen / worktreeNotice 由 ThreadMenuSheet 持有，因为 WorktreeMoveSheet 挂在外壳里。

import React from 'react';
import type { RemoteComposerOptions, RemoteThreadSummary } from '../../../types/contract';
import { GitBranchIcon } from '../../Icons';
import { MenuRow } from './MenuRow';

export interface ThreadMenuWorkspacePanelProps {
  latestSummary: RemoteThreadSummary;
  composerOptions: RemoteComposerOptions;
  composerOptionsSupported: boolean;
  updateComposerOptions: (
    next: RemoteComposerOptions | ((previous: RemoteComposerOptions) => RemoteComposerOptions)
  ) => void;
  isUpdatingThread: boolean;
  shareActionsBlocked: boolean;
  worktreeSupported: boolean;
  setWorktreeOpen: React.Dispatch<React.SetStateAction<boolean>>;
  worktreeNotice: string | null;
  setWorktreeNotice: React.Dispatch<React.SetStateAction<string | null>>;
}

export function ThreadMenuWorkspacePanel({
  latestSummary,
  composerOptions,
  composerOptionsSupported,
  updateComposerOptions,
  isUpdatingThread,
  shareActionsBlocked,
  worktreeSupported,
  setWorktreeOpen,
  worktreeNotice,
  setWorktreeNotice
}: ThreadMenuWorkspacePanelProps) {
  // 工作区模式只在首条消息发出前可改，已开始的会话只能看当前模式
  const currentMode =
    composerOptions.workspaceMode ?? (latestSummary.worktreeRequested ? 'worktree' : 'local');

  return (
    <div className="mt-4">
      {latestSummary.lastUserMessageAt ? (
        <p className="text-mc-caption text-[var(--foreground-muted)]">
          已开始的会话不能切换工作区（当前：{latestSummary.worktreeRequested ? 'Worktree' : '本地'}）
        </p>
      ) : (
        <div className="grid grid-cols-2 gap-1.5 p-1 rounded-xl bg-[var(--user-bubble)] border border-[var(--border)]">
          {(['local', 'worktree'] as const).map((mode) => {
            const active = currentMode === mode;
            return (
              <button
                key={mode}
                type="button"
                disabled={!composerOptionsSupported}
                onClick={() => updateComposerOptions((prev) => ({ ...prev, workspaceMode: mode }))}
                className={`min-h-[34px] rounded-lg text-xs font-medium transition ${
                  active
                    ? 'bg-[var(--card)] text-[var(--foreground)] shadow-sm font-semibold'
                    : 'text-[var(--foreground-muted)] hover:text-[var(--foreground)]'
                }`}
              >
                {mode === 'local' ? '本地' : 'Worktree'}
              </button>
            );
          })}
        </div>
      )}

      {worktreeSupported && !latestSummary.isArchived && (
        <div className="sheet-actions !mt-2">
          <MenuRow
            icon={<GitBranchIcon className="h-4 w-4" />}
            label="移到 worktree"
            onClick={() => {
              setWorktreeNotice(null);
              setWorktreeOpen(true);
            }}
            disabled={isUpdatingThread || shareActionsBlocked}
          />
        </div>
      )}

      {worktreeNotice && (
        <p className="mt-1.5 text-mc-caption text-emerald-600 dark:text-emerald-400" role="status">
          {worktreeNotice}
        </p>
      )}
    </div>
  );
}
