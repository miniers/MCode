import React from 'react';
import { ChevronDownIcon, ChevronRightIcon, DocumentTextIcon } from '../Icons';
import type { ThreadMessage } from '../ThreadTimeline';
import { ToolEntryDiff, isFileEditToolName, parseToolEditDiff } from './ToolEntryDiff';
import type { ToolDiffFile, ToolDiffLine } from './ToolEntryDiff';

// 回合改动文件卡片：回合结束行下面列出本回合改过的文件（对齐桌面端 internal/ui/turn_files_card.go 的 renderTurnFilesCard）。
// 数据来源是本回合工具调用里的文件编辑输入（复用 ToolEntryDiff 的解析），不额外请求 diff 接口；
// 点击文件优先打开工作区预览，打不开时行内展开该文件本回合的差量。

/** 收起时最多显示的文件数，多出来的折成「还有 N 个」（对齐桌面端 turnFilesDisplayLimit）。 */
export const TURN_FILES_DISPLAY_LIMIT = 5;

/** 本回合改动的一个文件。 */
export interface TurnEditedFile {
  /** 工具里记录的原始路径（相对会话工作目录或绝对路径） */
  path: string;
  /** 展示用的文件名 */
  name: string;
  /** 展示用的目录：路径去掉文件名后的部分，没有则为空串 */
  dir: string;
  additions: number;
  deletions: number;
  /** 该文件本回合的差量，没有可用差量时为 null */
  diff: ToolDiffFile | null;
}

// 与桌面端 turnToolSucceeded 一致：这些状态下的工具调用不算改成功，不计入卡片。
const UNSUCCESSFUL_TOOL_STATUSES = new Set([
  'failed',
  'error',
  'blocked',
  'cancelled',
  'canceled',
  'running',
  'pending'
]);

function isToolEntryMessage(entry: ThreadMessage): boolean {
  if (entry.kind) return entry.kind === 'tool';
  return entry.role === 'tool';
}

function toolSucceeded(entry: ThreadMessage): boolean {
  return !UNSUCCESSFUL_TOOL_STATUSES.has((entry.toolStatus ?? '').trim().toLowerCase());
}

function splitPath(path: string): { name: string; dir: string } {
  const trimmed = path.replace(/\\/g, '/').replace(/\/+$/, '');
  const index = trimmed.lastIndexOf('/');
  if (index < 0) return { name: trimmed, dir: '' };
  return { name: trimmed.slice(index + 1), dir: trimmed.slice(0, index) };
}

interface CollectedFile {
  path: string;
  name: string;
  dir: string;
  additions: number;
  deletions: number;
  lines: ToolDiffLine[];
  truncated: boolean;
  edits: number;
}

/**
 * 从本回合的工具消息里收集被修改的文件，按路径去重并累加增删行数。
 * 同一文件在本回合被改多次时，差量按 `@@ 第 N 次改动` 分段拼接。
 */
export function collectTurnEditedFiles(entries: ThreadMessage[]): TurnEditedFile[] {
  const collected = new Map<string, CollectedFile>();
  for (const entry of entries) {
    if (!isToolEntryMessage(entry) || !toolSucceeded(entry)) continue;
    if (!isFileEditToolName(entry.toolName)) continue;
    const files = parseToolEditDiff(entry.toolName, entry.toolInput);
    if (!files) continue;
    for (const file of files) {
      const path = (file.path ?? '').trim();
      // buildFile 在没有路径时会填占位文案，这种条目不是真实文件，跳过
      if (!path || path === '（未提供文件路径）') continue;
      const existing = collected.get(path);
      if (!existing) {
        const { name, dir } = splitPath(path);
        collected.set(path, {
          path,
          name,
          dir,
          additions: file.additions,
          deletions: file.deletions,
          lines: [...file.lines],
          truncated: file.truncated,
          edits: 1
        });
        continue;
      }
      existing.edits += 1;
      existing.additions += file.additions;
      existing.deletions += file.deletions;
      existing.truncated = existing.truncated || file.truncated;
      if (file.lines.length > 0) {
        existing.lines.push({ type: 'hunk', text: `@@ 第 ${existing.edits} 次改动` });
        existing.lines.push(...file.lines);
      }
    }
  }

  return [...collected.values()].map((file) => ({
    path: file.path,
    name: file.name,
    dir: file.dir,
    additions: file.additions,
    deletions: file.deletions,
    diff:
      file.lines.length > 0
        ? {
            id: file.path,
            path: file.path,
            additions: file.additions,
            deletions: file.deletions,
            lines: file.lines,
            truncated: file.truncated
          }
        : null
  }));
}

export interface TurnFilesCardProps {
  /** 本回合改动的文件，空数组时不渲染 */
  files: TurnEditedFile[];
  /**
   * 打开文件的工作区预览：返回 true 表示已打开，返回 false 或未提供时点击行改为展开行内差量。
   * 绝对路径落在项目目录外时调用方可以返回 false。
   */
  onOpenFile?: (path: string) => boolean;
}

export const TurnFilesCard: React.FC<TurnFilesCardProps> = ({ files, onOpenFile }) => {
  const [expanded, setExpanded] = React.useState(false);
  const [openPath, setOpenPath] = React.useState<string | null>(null);
  if (files.length === 0) return null;

  const visible = expanded ? files : files.slice(0, TURN_FILES_DISPLAY_LIMIT);
  const hiddenCount = files.length - visible.length;
  const collapsible = files.length > TURN_FILES_DISPLAY_LIMIT;
  // 每个文件的增删数都已知（不是 0/0 的未知项）时才在标题给总计，避免把残缺小计当成全量
  const totalsKnown = files.every((file) => file.additions > 0 || file.deletions > 0);
  const totalAdditions = files.reduce((sum, file) => sum + file.additions, 0);
  const totalDeletions = files.reduce((sum, file) => sum + file.deletions, 0);

  const handleFileClick = (file: TurnEditedFile) => {
    if (onOpenFile && onOpenFile(file.path)) {
      setOpenPath(null);
      return;
    }
    setOpenPath((current) => (current === file.path ? null : file.path));
  };

  return (
    <div className="mb-4 min-w-0 rounded-xl border border-[var(--border)] bg-[var(--card)] px-2.5 py-2 select-text">
      <div className="flex min-w-0 items-baseline justify-between gap-2">
        <p className="text-mc-caption font-medium text-[var(--foreground-muted)]">
          本回合改动 {files.length} 个文件
        </p>
        {totalsKnown && (
          <span
            className="shrink-0 font-mono text-mc-caption"
            title={`本回合共新增 ${totalAdditions} 行、删除 ${totalDeletions} 行`}
          >
            <span className="text-emerald-600 dark:text-emerald-400">+{totalAdditions}</span>
            <span className="mx-1 opacity-50">/</span>
            <span className="text-rose-600 dark:text-rose-400">-{totalDeletions}</span>
          </span>
        )}
      </div>
      <ul className="mt-1 space-y-0.5">
        {visible.map((file) => {
          const isOpen = openPath === file.path;
          const hasDelta = file.additions > 0 || file.deletions > 0;
          return (
            <li key={file.path} className="min-w-0">
              <button
                type="button"
                onClick={() => handleFileClick(file)}
                aria-expanded={isOpen}
                aria-label={`打开改动文件 ${file.path}`}
                title={file.path}
                className="flex w-full min-w-0 items-center gap-1.5 rounded-lg px-1.5 py-1 text-left text-xs transition hover:bg-[var(--card-alt,var(--card))] active:scale-[0.99]"
              >
                <DocumentTextIcon className="h-3.5 w-3.5 shrink-0 text-[var(--foreground-muted)]" />
                <span className="shrink-0 font-mono font-medium text-[var(--foreground)]">{file.name}</span>
                {file.dir && (
                  <span className="min-w-0 flex-1 truncate font-mono text-mc-caption text-[var(--foreground-muted)]">
                    {file.dir}
                  </span>
                )}
                {!file.dir && <span className="min-w-0 flex-1" />}
                {hasDelta && (
                  <span className="shrink-0 font-mono text-mc-caption">
                    <span className="text-emerald-600 dark:text-emerald-400">+{file.additions}</span>
                    <span className="mx-1 opacity-50">/</span>
                    <span className="text-rose-600 dark:text-rose-400">-{file.deletions}</span>
                  </span>
                )}
                {file.diff && (
                  <ChevronRightIcon
                    className={`h-3 w-3 shrink-0 text-[var(--foreground-muted)] transition-transform ${isOpen ? 'rotate-90' : ''}`}
                  />
                )}
              </button>
              {isOpen && file.diff && (
                <div className="mt-1.5">
                  <ToolEntryDiff files={[file.diff]} />
                </div>
              )}
            </li>
          );
        })}
      </ul>
      {collapsible && (
        <button
          type="button"
          onClick={() => setExpanded((current) => !current)}
          aria-expanded={expanded}
          className="mt-1.5 flex min-h-[28px] items-center gap-1 rounded-lg px-1.5 text-mc-caption text-[var(--foreground-muted)] transition hover:bg-[var(--card-alt,var(--card))]"
        >
          <ChevronDownIcon
            className={`h-3 w-3 shrink-0 transition-transform ${expanded ? 'rotate-180' : ''}`}
          />
          {expanded ? '收起' : `还有 ${hiddenCount} 个`}
        </button>
      )}
    </div>
  );
};

TurnFilesCard.displayName = 'TurnFilesCard';
