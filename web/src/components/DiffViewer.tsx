// Git 差异阅读组件，支持按文件组织、等宽行号、折叠、换行开关、行内评论与截断提示

import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useRemoteStatic } from '../context/RemoteContext';
import {
  parseFilesFromUnifiedDiff,
  type ParsedDiffLine,
  type ParsedFileDiff,
  type DiffComment
} from '../utils/diff';
import {
  ArrowPathIcon,
  ChevronDownIcon,
  ChevronRightIcon,
  ExclamationTriangleIcon
} from './Icons';

export interface DiffViewerProps {
  taskID?: string;
  diffText?: string;
  truncated?: boolean;
  metadataTruncated?: boolean;
  filesChanged?: string[];
  failureReason?: string;
  onRetry?: () => void;
  comments?: Record<string, DiffComment>;
  onAddComment?: (line: ParsedDiffLine, file: ParsedFileDiff, content: string) => void;
  onDeleteComment?: (commentId: string) => void;
  onRevertHunk?: (file: ParsedFileDiff, startLine: number, lineCount: number) => void;
  canRevertHunk?: (file: ParsedFileDiff) => boolean;
  readOnly?: boolean;
  /** 改动行数超过该值的文件初始折叠（默认 400；<= 0 表示不做阈值折叠） */
  defaultCollapsedWhenOver?: number;
  /** 初始折叠全部文件 */
  initialCollapseAll?: boolean;
}

// 单个 hunk 块首次展开时最多渲染的行数，超出部分由「展开本块」按钮补足
const BLOCK_PREVIEW_LINES = 40;

// 行级类名预先拼好，避免每行做字符串累加
const ROW_BASE_CLASS =
  'flex items-start px-1.5 py-0.5 rounded leading-relaxed min-w-0 max-w-full w-full select-text group relative';
const ROW_TYPE_CLASS: Record<ParsedDiffLine['type'], string> = {
  add: ' bg-emerald-950/60 text-emerald-300',
  del: ' bg-rose-950/60 text-rose-300',
  hunk: ' bg-sky-950/50 text-sky-300 font-semibold',
  header: ' text-slate-500 font-bold',
  context: ' text-slate-300'
};
const LINE_CONTENT_WRAP_CLASS =
  'flex-1 min-w-0 font-mono text-mc-caption whitespace-pre-wrap break-all [overflow-wrap:anywhere]';
const LINE_CONTENT_NOWRAP_CLASS = 'flex-1 min-w-0 font-mono text-mc-caption whitespace-pre overflow-x-auto';

interface DiffBlock {
  key: string;
  lines: ParsedDiffLine[];
}

export interface RevertHunkRange {
  hunkLineId: string;
  startLine: number;
  lineCount: number;
}

export function getRevertHunkRanges(lines: ParsedDiffLine[]): RevertHunkRange[] {
  const ranges: RevertHunkRange[] = [];
  let hunkLineId = '';
  let newLine = 0;
  let active: RevertHunkRange | null = null;
  const finish = () => {
    if (active) ranges.push(active);
    active = null;
  };

  for (const line of lines) {
    if (line.type === 'hunk') {
      finish();
      hunkLineId = line.id;
      const match = line.text.match(/^@@\s+-\d+(?:,\d+)?\s+\+(\d+)(?:,\d+)?\s+@@/);
      newLine = match ? Number(match[1]) : 0;
      continue;
    }
    if (line.type === 'add' || line.type === 'del') {
      if (!active) active = { hunkLineId, startLine: newLine || 1, lineCount: 0 };
      if (line.type === 'add') {
        newLine++;
        active.lineCount++;
      }
      continue;
    }
    if (line.type === 'context') {
      finish();
      if (line.newLineNumber != null) newLine = line.newLineNumber + 1;
      continue;
    }
    finish();
  }
  finish();
  return ranges;
}

interface CommentEditorProps {
  initialContent: string;
  lineLabel: string;
  onCancel: () => void;
  onSave: (content: string) => void;
}

// 行内评论输入框：草稿状态收敛在这里，打字只重渲染输入框本身
function CommentEditor({ initialContent, lineLabel, onCancel, onSave }: CommentEditorProps) {
  const [draft, setDraft] = useState(initialContent);
  const trimmed = draft.trim();

  return (
    <div className="ml-16 sm:ml-20 my-2 p-2.5 rounded-xl bg-slate-800 border border-slate-700 shadow-md space-y-2">
      <div className="text-mc-caption text-slate-300 font-medium">
        添加审阅意见 ·{' '}
        {lineLabel}
      </div>
      <textarea
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        placeholder="输入针对此代码行的审阅建议..."
        rows={2}
        className="w-full p-2 text-xs rounded-lg border border-slate-600 bg-slate-900 text-slate-200 outline-none focus:border-sky-500 resize-y"
      />
      <div className="flex items-center justify-end gap-2">
        <button
          type="button"
          onClick={onCancel}
          className="min-h-[28px] px-2.5 rounded-lg text-xs text-slate-400 hover:text-slate-200"
        >
          取消
        </button>
        <button
          type="button"
          disabled={!trimmed}
          onClick={() => {
            if (trimmed) onSave(trimmed);
          }}
          className="min-h-[28px] px-3 rounded-lg text-xs font-semibold bg-sky-600 text-white hover:bg-sky-500 disabled:opacity-40"
        >
          保存评论
        </button>
      </div>
    </div>
  );
}

interface DiffRowProps {
  line: ParsedDiffLine;
  file: ParsedFileDiff;
  wrapLines: boolean;
  canComment: boolean;
  isReadOnly: boolean;
  comment?: DiffComment;
  isCommenting: boolean;
  onToggleComment: (lineId: string) => void;
  onCancelComment: () => void;
  onSaveComment: (file: ParsedFileDiff, line: ParsedDiffLine, content: string) => void;
  onDeleteComment?: (commentId: string) => void;
  revertHunks?: RevertHunkRange[];
  onRevertHunk?: (file: ParsedFileDiff, startLine: number, lineCount: number) => void;
  canRevertHunk?: (file: ParsedFileDiff) => boolean;
}

// 单行渲染：memo 化后，父级或其它行的状态变化不会重建这一行
const DiffRow = React.memo(function DiffRow({
  line,
  file,
  wrapLines,
  canComment,
  isReadOnly,
  comment,
  isCommenting,
  onToggleComment,
  onCancelComment,
  onSaveComment,
  onDeleteComment,
  revertHunks = [],
  onRevertHunk,
  canRevertHunk
}: DiffRowProps) {
  const rowClass = ROW_BASE_CLASS + (ROW_TYPE_CLASS[line.type] ?? ROW_TYPE_CLASS.context);
  const oldLineLabel = line.oldLineNumber != null ? String(line.oldLineNumber) : '';
  const newLineLabel = line.newLineNumber != null ? String(line.newLineNumber) : '';
  const contentClass = wrapLines ? LINE_CONTENT_WRAP_CLASS : LINE_CONTENT_NOWRAP_CLASS;
  const positionLabel = line.newLineNumber
    ? `第 ${line.newLineNumber} 行`
    : `旧第 ${line.oldLineNumber} 行`;

  return (
    <>
      <div className={rowClass}>
        {/* 旧行号 */}
        <span
          className="w-8 sm:w-10 text-right pr-2 text-slate-500 font-mono text-mc-caption shrink-0 select-none"
          aria-hidden="true"
        >
          {oldLineLabel}
        </span>

        {/* 新行号 */}
        <span
          className="w-8 sm:w-10 text-right pr-2 text-slate-500 font-mono text-mc-caption shrink-0 select-none border-r border-slate-700/60 mr-2"
          aria-hidden="true"
        >
          {newLineLabel}
        </span>

        {/* 行内容 */}
        <div className={contentClass}>{line.text || ' '}</div>
        {line.type === 'hunk' && onRevertHunk && canRevertHunk?.(file) !== false && revertHunks.map((hunk) => (
          <button
            key={`${hunk.startLine}:${hunk.lineCount}`}
            type="button"
            title="撤销此块"
            aria-label="撤销此块"
            onClick={(event) => {
              event.stopPropagation();
              onRevertHunk(file, hunk.startLine, hunk.lineCount);
            }}
            className="ml-2 shrink-0 rounded px-1.5 py-0.5 text-mc-caption text-slate-300 hover:bg-slate-700 hover:text-white"
          >
            撤销此块
          </button>
        ))}

        {/* 行内评论触发按钮 */}
        {canComment && line.isCommentable && (
          <button
            type="button"
            onClick={() => onToggleComment(line.id)}
            title="对此行发表评论"
            aria-label="添加评论"
            className="ml-2 shrink-0 opacity-40 hover:opacity-100 group-hover:opacity-100 text-slate-400 hover:text-sky-300 px-1 py-0.5 rounded text-mc-caption transition"
          >
            {comment ? '💬' : '＋'}
          </button>
        )}
      </div>

      {/* 已存在评论卡片 */}
      {comment && (
        <div className="ml-16 sm:ml-20 my-1.5 p-2.5 rounded-lg bg-amber-500/10 border border-amber-500/30 text-slate-200 text-xs space-y-1">
          <div className="flex items-center justify-between text-mc-caption text-amber-400 font-medium">
            <span>💬 评论 · {positionLabel}</span>
            {!isReadOnly && onDeleteComment && (
              <button
                type="button"
                onClick={() => onDeleteComment(comment.id)}
                className="text-rose-400 hover:underline text-mc-caption"
              >
                删除
              </button>
            )}
          </div>
          <p className="whitespace-pre-wrap break-words">{comment.content}</p>
        </div>
      )}

      {/* 行内添加/编辑评论输入框 */}
      {isCommenting && canComment && (
        <CommentEditor
          initialContent={comment?.content || ''}
          lineLabel={positionLabel}
          onCancel={onCancelComment}
          onSave={(content) => onSaveComment(file, line, content)}
        />
      )}
    </>
  );
});

// 按阈值与 initialCollapseAll 计算文件的初始折叠状态
function buildInitialCollapse(
  parsedFiles: ParsedFileDiff[],
  threshold: number,
  collapseAll: boolean
): Record<string, boolean> {
  const next: Record<string, boolean> = {};
  if (collapseAll) {
    parsedFiles.forEach((file) => {
      next[file.id] = true;
    });
    return next;
  }
  if (threshold <= 0) return next;
  parsedFiles.forEach((file) => {
    if (file.lines.length > threshold) {
      next[file.id] = true;
    }
  });
  return next;
}

// memo 化后，会话输入框打字 / 流式增量不会再重建整棵 diff 行树
export const DiffViewer = React.memo(function DiffViewer({
  taskID,
  diffText: propDiffText,
  truncated: propTruncated,
  metadataTruncated: propMetadataTruncated,
  filesChanged: propFilesChanged,
  failureReason: propFailureReason,
  onRetry,
  comments,
  onAddComment,
  onDeleteComment,
  onRevertHunk,
  canRevertHunk,
  readOnly = false,
  defaultCollapsedWhenOver = 400,
  initialCollapseAll = false
}: DiffViewerProps) {
  const { getDiff } = useRemoteStatic();
  const [loading, setLoading] = useState<boolean>(Boolean(taskID));
  const [error, setError] = useState<string | null>(propFailureReason ?? null);
  const [taskDiffText, setTaskDiffText] = useState<string>('');
  const [taskTruncated, setTaskTruncated] = useState<boolean>(false);

  // 视图控制状态
  const [wrapLines, setWrapLines] = useState<boolean>(true);
  const [activeCommentLineId, setActiveCommentLineId] = useState<string | null>(null);
  const [expandedBlocks, setExpandedBlocks] = useState<Record<string, boolean>>({});

  const isTaskMode = Boolean(taskID);
  const effectiveDiffText = isTaskMode ? taskDiffText : propDiffText || '';
  const effectiveTruncated = isTaskMode ? taskTruncated : Boolean(propTruncated);
  const effectiveMetadataTruncated = !isTaskMode && Boolean(propMetadataTruncated);

  const fetchTaskDiff = async () => {
    if (!taskID) return;
    setLoading(true);
    setError(null);
    try {
      const res = await getDiff(taskID);
      setTaskDiffText(res.diff || '');
      setTaskTruncated(Boolean(res.truncated));
    } catch (err) {
      setError(err instanceof Error ? err.message : '获取代码差异失败');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    if (taskID) {
      void fetchTaskDiff();
    }
  }, [taskID]);

  useEffect(() => {
    if (propFailureReason) {
      setError(propFailureReason);
    }
  }, [propFailureReason]);

  const parsedFiles = useMemo(() => {
    return parseFilesFromUnifiedDiff(effectiveDiffText, propFilesChanged);
  }, [effectiveDiffText, propFilesChanged]);

  // 文件级折叠状态：首帧就按阈值折叠，避免先全量渲染再收缩
  const [collapsedFiles, setCollapsedFiles] = useState<Record<string, boolean>>(() =>
    buildInitialCollapse(parsedFiles, defaultCollapsedWhenOver, initialCollapseAll)
  );
  const initializedFilesRef = useRef<ParsedFileDiff[]>(parsedFiles);

  // diff 内容变化（例如任务模式异步加载完成）时重新套用默认折叠，不覆盖用户后续操作
  useEffect(() => {
    if (initializedFilesRef.current === parsedFiles) return;
    initializedFilesRef.current = parsedFiles;
    setCollapsedFiles(
      buildInitialCollapse(parsedFiles, defaultCollapsedWhenOver, initialCollapseAll)
    );
    setExpandedBlocks({});
  }, [parsedFiles, defaultCollapsedWhenOver, initialCollapseAll]);

  // 把单个文件的行按 hunk 边界切成块，hunk 头留在块首
  const diffBlocks = useMemo(() => {
    const map = new Map<string, DiffBlock[]>();
    parsedFiles.forEach((file) => {
      const blocks: DiffBlock[] = [];
      let current: ParsedDiffLine[] = [];
      file.lines.forEach((line) => {
        if (line.type === 'hunk' && current.length > 0) {
          blocks.push({ key: `${file.id}#${blocks.length}`, lines: current });
          current = [];
        }
        current.push(line);
      });
      if (current.length > 0) {
        blocks.push({ key: `${file.id}#${blocks.length}`, lines: current });
      }
      map.set(file.id, blocks);
    });
    return map;
  }, [parsedFiles]);

  const totalAdditions = useMemo(
    () => parsedFiles.reduce((sum, f) => sum + f.additionsCount, 0),
    [parsedFiles]
  );
  const totalDeletions = useMemo(
    () => parsedFiles.reduce((sum, f) => sum + f.deletionsCount, 0),
    [parsedFiles]
  );

  // onAddComment 在调用方可能随轮次变化，用 ref 读取最新值，保持下方回调身份稳定
  const onAddCommentRef = useRef(onAddComment);
  useEffect(() => {
    onAddCommentRef.current = onAddComment;
  });

  const toggleFileCollapse = useCallback((fileId: string) => {
    setCollapsedFiles((prev) => {
      const next = { ...prev };
      if (next[fileId]) {
        delete next[fileId];
      } else {
        next[fileId] = true;
      }
      return next;
    });
  }, []);

  const handleCollapseAll = useCallback(() => {
    const allCollapsed: Record<string, boolean> = {};
    parsedFiles.forEach((f) => {
      allCollapsed[f.id] = true;
    });
    setCollapsedFiles(allCollapsed);
    setExpandedBlocks({});
  }, [parsedFiles]);

  const handleExpandAll = useCallback(() => {
    setCollapsedFiles({});
    const nextBlocks: Record<string, boolean> = {};
    diffBlocks.forEach((blocks) => {
      blocks.forEach((block) => {
        nextBlocks[block.key] = true;
      });
    });
    setExpandedBlocks(nextBlocks);
  }, [diffBlocks]);

  const expandBlock = useCallback((blockKey: string) => {
    setExpandedBlocks((prev) => (prev[blockKey] ? prev : { ...prev, [blockKey]: true }));
  }, []);

  const handleToggleComment = useCallback((lineId: string) => {
    setActiveCommentLineId((prev) => (prev === lineId ? null : lineId));
  }, []);

  const handleCancelComment = useCallback(() => {
    setActiveCommentLineId(null);
  }, []);

  const handleSaveComment = useCallback(
    (file: ParsedFileDiff, line: ParsedDiffLine, content: string) => {
      onAddCommentRef.current?.(line, file, content);
      setActiveCommentLineId(null);
    },
    []
  );

  const scrollToElement = useCallback((id: string) => {
    const el = document.getElementById(id);
    if (el) {
      el.scrollIntoView({ behavior: 'smooth', block: 'start' });
    }
  }, []);

  // 顶部文件胶囊跳转：目标文件若已折叠，先展开再滚动到位
  const jumpToFile = useCallback(
    (fileId: string) => {
      setCollapsedFiles((prev) => {
        if (!prev[fileId]) return prev;
        const next = { ...prev };
        delete next[fileId];
        return next;
      });
      scrollToElement(fileId);
    },
    [scrollToElement]
  );

  const hasCollapsedFiles = Object.keys(collapsedFiles).length > 0;
  const canComment = !readOnly && Boolean(onAddComment);

  if (loading) {
    return (
      <div className="flex flex-col items-center justify-center p-8 space-y-3 bg-slate-50 dark:bg-zinc-900/40 rounded-xl border border-slate-200 dark:border-zinc-800">
        <span className="w-6 h-6 border-2 border-sky-500/30 border-t-sky-500 rounded-full animate-spin" />
        <p className="text-xs text-slate-500 dark:text-slate-400">正在获取代码差异 (Diff)...</p>
      </div>
    );
  }

  if (error) {
    return (
      <div className="p-4 bg-rose-50 dark:bg-rose-950/30 border border-rose-200 dark:border-rose-900/40 rounded-xl space-y-3">
        <div className="flex items-center gap-2 text-rose-600 dark:text-rose-400 text-sm font-medium">
          <ExclamationTriangleIcon className="w-4 h-4 shrink-0" />
          <span>无法加载变更内容</span>
        </div>
        <p className="text-xs text-rose-600/80 dark:text-rose-400/80">{error}</p>
        <button
          type="button"
          onClick={isTaskMode ? fetchTaskDiff : onRetry}
          className="min-h-[44px] px-3 py-1.5 text-xs font-medium text-rose-700 dark:text-rose-300 bg-rose-100 dark:bg-rose-900/50 hover:bg-rose-200 rounded-lg inline-flex items-center gap-1.5 transition"
        >
          <ArrowPathIcon className="w-3.5 h-3.5" />
          <span>重新获取</span>
        </button>
      </div>
    );
  }

  if (!effectiveDiffText.trim() && parsedFiles.length === 0) {
    return (
      <div className="space-y-3 rounded-xl border border-slate-200 bg-slate-50 p-4 text-center text-xs text-slate-500 dark:border-zinc-800 dark:bg-zinc-900/40 dark:text-slate-400">
        {effectiveTruncated || effectiveMetadataTruncated ? (
          <>
            <div className="flex items-center justify-center gap-2 text-amber-700 dark:text-amber-400">
              <ExclamationTriangleIcon className="h-4 w-4" />
              <span>服务端只返回了截断标记，当前内容不完整，不能作为完整审查依据。</span>
            </div>
            <button
              type="button"
              onClick={isTaskMode ? fetchTaskDiff : onRetry}
              className="secondary-button mx-auto"
              data-host-actionable="false"
            >
              <ArrowPathIcon className="h-3.5 w-3.5" />重新获取
            </button>
          </>
        ) : (
          '当前范围暂无代码变更差异 (Diff)'
        )}
      </div>
    );
  }

  return (
    <div className="space-y-3 w-full min-w-0 max-w-full overflow-hidden text-xs">
      {/* 截断警告横幅 */}
      {(effectiveTruncated || effectiveMetadataTruncated) && (
        <div className="flex items-start gap-2.5 p-3 bg-amber-500/10 border border-amber-500/30 text-amber-800 dark:text-amber-300 rounded-xl min-w-0 break-words">
          <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5" />
          <div className="min-w-0 flex-1 space-y-1">
            <div className="font-semibold text-xs">
              内容不完整，不能作为完整审查依据
            </div>
            <div className="text-mc-caption opacity-90 leading-relaxed">
              {effectiveTruncated && '差异内容超出单次传输限制 (512 KiB)，服务端已截断显示前部变更。完整内容请在 Mac 桌面端审查。'}
              {effectiveMetadataTruncated && ' 文件列表或轮多元数据存在截断。'}
            </div>
          </div>
        </div>
      )}

      {/* 顶部工具栏与统计 */}
      <div className="flex flex-wrap items-center justify-between gap-2 p-2.5 rounded-xl border border-slate-200 dark:border-zinc-800 bg-slate-100/70 dark:bg-zinc-900/70 text-slate-700 dark:text-slate-300">
        <div className="flex items-center gap-2 font-medium">
          <span>{parsedFiles.length} 个文件</span>
          <span className="text-emerald-600 dark:text-emerald-400">+{totalAdditions}</span>
          <span className="text-rose-600 dark:text-rose-400">-{totalDeletions}</span>
        </div>

        <div className="flex items-center gap-1.5 shrink-0">
          <button
            type="button"
            onClick={() => setWrapLines((w) => !w)}
            className="min-h-[32px] px-2.5 py-1 rounded-lg text-xs font-medium border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-800 hover:bg-slate-50 dark:hover:bg-zinc-700 transition"
          >
            {wrapLines ? '自动换行: 开' : '自动换行: 关'}
          </button>
          <button
            type="button"
            onClick={hasCollapsedFiles ? handleExpandAll : handleCollapseAll}
            className="min-h-[32px] px-2.5 py-1 rounded-lg text-xs font-medium border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-800 hover:bg-slate-50 dark:hover:bg-zinc-700 transition"
          >
            {hasCollapsedFiles ? '全部展开' : '全部折叠'}
          </button>
        </div>
      </div>

      {/* 多文件导航快速跳转胶囊 */}
      {parsedFiles.length > 1 && (
        <div className="flex items-center gap-1.5 overflow-x-auto pb-1 pt-0.5 no-scrollbar">
          {parsedFiles.map((file) => (
            <button
              key={file.id}
              type="button"
              onClick={() => jumpToFile(file.id)}
              className="min-h-[28px] max-w-[200px] truncate px-2.5 py-1 rounded-full text-mc-caption font-mono border border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-800/80 hover:bg-slate-200 dark:hover:bg-zinc-700 transition shrink-0"
            >
              <span className="truncate">{file.displayPath.split('/').pop()}</span>
              {file.hasHunks && (
                <span className="ml-1 opacity-70">
                  +{file.additionsCount}/-{file.deletionsCount}
                </span>
              )}
            </button>
          ))}
        </div>
      )}

      {/* 按文件渲染 Diff 容器 */}
      <div className="space-y-3 w-full min-w-0">
        {parsedFiles.map((file) => {
          const isCollapsed = Boolean(collapsedFiles[file.id]);
          const blocks = diffBlocks.get(file.id) ?? [];
          const revertHunks = getRevertHunkRanges(file.lines);

          return (
            <div
              key={file.id}
              id={file.id}
              className="rounded-xl border border-slate-200 dark:border-zinc-800 overflow-hidden bg-slate-900 text-slate-200 font-mono shadow-inner w-full min-w-0 max-w-full"
            >
              {/* 文件标题栏（可点击折叠/展开） */}
              <div
                onClick={() => toggleFileCollapse(file.id)}
                className="flex items-center justify-between px-3 py-2 bg-slate-800/90 border-b border-slate-700/60 cursor-pointer select-none hover:bg-slate-800 transition"
              >
                <div className="flex items-center gap-2 min-w-0 flex-1">
                  <span className="text-slate-400 shrink-0">
                    {isCollapsed ? (
                      <ChevronRightIcon className="w-3.5 h-3.5" />
                    ) : (
                      <ChevronDownIcon className="w-3.5 h-3.5" />
                    )}
                  </span>
                  <span className="font-semibold text-xs text-slate-200 truncate" title={file.displayPath}>
                    {file.displayPath}
                  </span>
                  {file.status === 'added' && (
                    <span className="px-1.5 py-0.5 rounded text-mc-caption bg-emerald-950 text-emerald-400 shrink-0">
                      新增
                    </span>
                  )}
                  {file.status === 'deleted' && (
                    <span className="px-1.5 py-0.5 rounded text-mc-caption bg-rose-950 text-rose-400 shrink-0">
                      删除
                    </span>
                  )}
                  {file.status === 'renamed' && (
                    <span className="px-1.5 py-0.5 rounded text-mc-caption bg-sky-950 text-sky-400 shrink-0">
                      重命名
                    </span>
                  )}
                  {file.status === 'binary' && (
                    <span className="px-1.5 py-0.5 rounded text-mc-caption bg-amber-950 text-amber-400 shrink-0">
                      二进制
                    </span>
                  )}
                </div>

                <div className="flex items-center gap-2 text-mc-caption shrink-0 text-slate-400">
                  {file.hasHunks && (
                    <>
                      <span className="text-emerald-400">+{file.additionsCount}</span>
                      <span className="text-rose-400">-{file.deletionsCount}</span>
                    </>
                  )}
                  <span>{isCollapsed ? '展开' : '折叠'}</span>
                </div>
              </div>

              {/* 文件内容展示（仅展开时挂载与渲染） */}
              {!isCollapsed && (
                <div className="p-2 space-y-0.5">
                  {file.isBinary ? (
                    <div className="py-6 px-4 text-center text-xs text-slate-400 bg-slate-900/60 rounded">
                      二进制文件差异，不提供文本行对比
                    </div>
                  ) : file.status === 'renamed' && !file.hasHunks ? (
                    <div className="py-6 px-4 text-center text-xs text-slate-400 bg-slate-900/60 rounded">
                      文件已重命名，内容无改动
                    </div>
                  ) : file.status === 'mode_change' && !file.hasHunks ? (
                    <div className="py-6 px-4 text-center text-xs text-slate-400 bg-slate-900/60 rounded">
                      文件权限/模式变更，无内容改动
                    </div>
                  ) : !file.hasHunks ? (
                    <div className="py-6 px-4 text-center text-xs text-slate-400 bg-slate-900/60 rounded">
                      无具体代码行变更
                    </div>
                  ) : (
                    <div className="divide-y divide-slate-800/40">
                      {blocks.map((block) => {
                        const isBlockExpanded = Boolean(expandedBlocks[block.key]);
                        const hiddenCount = isBlockExpanded
                          ? 0
                          : Math.max(0, block.lines.length - BLOCK_PREVIEW_LINES);
                        const visibleLines = hiddenCount > 0
                          ? block.lines.slice(0, BLOCK_PREVIEW_LINES)
                          : block.lines;

                        return (
                          <div key={block.key}>
                            <div className="divide-y divide-slate-800/40">
                              {visibleLines.map((line) => {
                                // 仅在存在评论时才拼接行级 key，省掉无谓的字符串拼接
                                const comment = comments
                                  ? comments[`${file.displayPath}:${line.id}`]
                                  : undefined;

                                return (
                                  <DiffRow
                                    key={line.id}
                                    line={line}
                                    file={file}
                                    wrapLines={wrapLines}
                                    canComment={canComment}
                                    isReadOnly={readOnly}
                                    comment={comment}
                                    isCommenting={activeCommentLineId === line.id}
                                    onToggleComment={handleToggleComment}
                                    onCancelComment={handleCancelComment}
                                    onSaveComment={handleSaveComment}
                                    onDeleteComment={onDeleteComment}
                                    revertHunks={line.type === 'hunk' ? revertHunks.filter((hunk) => hunk.hunkLineId === line.id) : undefined}
                                    onRevertHunk={onRevertHunk}
                                    canRevertHunk={canRevertHunk}
                                  />
                                );
                              })}
                            </div>
                            {hiddenCount > 0 && (
                              <button
                                type="button"
                                onClick={() => expandBlock(block.key)}
                                className="w-full min-h-[32px] border-t border-slate-800/40 bg-slate-800/40 py-1.5 text-center text-mc-caption text-sky-300 hover:bg-slate-800/70 transition"
                              >
                                {`展开本块（共 ${block.lines.length} 行，已显示前 ${BLOCK_PREVIEW_LINES} 行）`}
                              </button>
                            )}
                          </div>
                        );
                      })}
                    </div>
                  )}
                </div>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
});
