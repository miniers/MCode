// 目标产物弹层：按目标汇总任务的标题、状态与完成说明，复用任务详情 sheet 查看单个任务。
// 算法与桌面端 internal/ui/kanban.go 的 goalOutputs 一致，只取看板快照，不新增 RPC。

import React, { useEffect, useMemo } from 'react';
import type { Goal, KanbanDisplayColumn, TaskItem } from '../types/contract';
import { XMarkIcon } from './Icons';

// 产物里的单条任务汇总：summary 优先取完成说明（评审总结），退化为 Notes。
export interface GoalOutputTask {
  task: TaskItem;
  summary: string;
  hasDiff: boolean;
}

// 产物排序用的列顺序，与桌面端 kanbanColumnDefs 一致（internal/ui/kanban.go:961）。
const GOAL_OUTPUT_COLUMN_RANK: KanbanDisplayColumn[] = [
  'todo',
  'in_progress',
  'needs_you',
  'review',
  'done'
];

// goalOutputColumnRank 返回任务所在列的展示顺序，未知列排在最后。
export function goalOutputColumnRank(column: string): number {
  const index = GOAL_OUTPUT_COLUMN_RANK.indexOf(column as KanbanDisplayColumn);
  return index === -1 ? GOAL_OUTPUT_COLUMN_RANK.length : index;
}

// goalOutputSummary 提取任务的完成说明/Notes 摘要，优先完成说明（评审总结）。
export function goalOutputSummary(task: TaskItem): string {
  const reviewSummary = (task.reviewSummary ?? '').trim();
  if (reviewSummary !== '') return task.reviewSummary ?? '';
  return task.notes;
}

// goalOutputStatusLabel 将任务状态映射为界面文案，未知状态原样返回。
export function goalOutputStatusLabel(status: string): string {
  switch (status) {
    case 'todo':
      return '待办';
    case 'in_progress':
      return '进行中';
    case 'needs_approval':
      return '待审批';
    case 'review':
      return '审查';
    case 'done':
      return '完成';
    case 'rejected':
      return '已驳回';
    default:
      return status;
  }
}

// goalOutputs 从看板任务按目标汇总任务产物：只保留 goalID 匹配的任务，
// 按看板列顺序（待办→进行中→待你处理→审查→完成）与标题稳定排序。
export function goalOutputs(tasks: TaskItem[], goalID: string): GoalOutputTask[] {
  const normalized = goalID.trim();
  if (normalized === '') return [];
  const out: GoalOutputTask[] = [];
  for (const task of tasks) {
    if (task.goalID !== normalized) continue;
    out.push({
      task,
      summary: goalOutputSummary(task),
      hasDiff: task.threadID !== null && task.threadID !== ''
    });
  }
  out.sort((first, second) => {
    const firstRank = goalOutputColumnRank(first.task.displayColumn);
    const secondRank = goalOutputColumnRank(second.task.displayColumn);
    if (firstRank !== secondRank) return firstRank - secondRank;
    if (first.task.title < second.task.title) return -1;
    if (first.task.title > second.task.title) return 1;
    return 0;
  });
  return out;
}

interface GoalOutputsSheetProps {
  goal: Goal;
  tasks: TaskItem[];
  onOpenTask: (task: TaskItem) => void;
  onClose: () => void;
}

export const GoalOutputsSheet: React.FC<GoalOutputsSheetProps> = ({
  goal,
  tasks,
  onOpenTask,
  onClose
}) => {
  const outputs = useMemo(() => goalOutputs(tasks, goal.id), [tasks, goal.id]);
  const doneCount = useMemo(() => outputs.filter((out) => out.task.status === 'done').length, [outputs]);
  const title = goal.title || goal.prompt;

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        onClose();
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [onClose]);

  return (
    <div className="sheet-layer" role="presentation" onMouseDown={onClose}>
      <section
        role="dialog"
        aria-modal="true"
        aria-label="目标产物"
        className="settings-sheet flex flex-col outline-none w-full max-w-full min-w-0 overflow-x-hidden"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="sheet-handle shrink-0" aria-hidden="true" />
        <div className="flex shrink-0 items-start justify-between gap-3 min-w-0 w-full">
          <div className="min-w-0 flex-1">
            <p className="eyebrow truncate">目标产物</p>
            {title && (
              <h2 className="mt-1 break-words [overflow-wrap:anywhere] text-headline font-bold text-foreground">
                {title}
              </h2>
            )}
            {goal.resultSummary && (
              <p className="mt-1 whitespace-pre-wrap break-words [overflow-wrap:anywhere] text-sm text-muted min-w-0">
                {goal.resultSummary}
              </p>
            )}
          </div>
          <button type="button" onClick={onClose} aria-label="关闭" title="关闭" className="icon-button shrink-0">
            <XMarkIcon className="h-4 w-4" />
          </button>
        </div>

        <div className="mt-4 min-h-0 flex-1 space-y-2 overflow-y-auto overflow-x-hidden min-w-0 max-w-full w-full">
          {outputs.length === 0 ? (
            <p className="py-6 text-center text-sm text-muted">该目标暂无任务产出</p>
          ) : (
            outputs.map((out) => (
              <article
                key={out.task.id}
                className="rounded-xl border border-border-subtle bg-card-alt px-3 py-2"
              >
                <div className="flex min-w-0 items-center gap-2">
                  <button
                    type="button"
                    className="min-w-0 flex-1 text-left"
                    onClick={() => onOpenTask(out.task)}
                  >
                    <h3 className="truncate text-sm font-medium text-foreground">
                      {out.task.title || '未命名任务'}
                    </h3>
                  </button>
                  <span className="shrink-0 rounded bg-border-subtle px-1.5 py-0.5 text-mc-caption text-muted">
                    {goalOutputStatusLabel(out.task.status)}
                  </span>
                  <button
                    type="button"
                    className="shrink-0 text-mc-caption text-primary hover:underline"
                    onClick={() => onOpenTask(out.task)}
                    aria-label={`打开任务 ${out.task.title || '未命名任务'}`}
                  >
                    {out.hasDiff ? '查看 Diff' : '查看'}
                  </button>
                </div>
                {out.summary !== '' && (
                  <p className="mt-1 truncate text-mc-caption text-muted">{out.summary}</p>
                )}
              </article>
            ))
          )}
        </div>

        <div className="mt-3 flex shrink-0 items-center gap-2">
          <p className="min-w-0 flex-1 text-mc-caption text-muted">
            {outputs.length} 项任务 · 已完成 {doneCount} 项
          </p>
          <button type="button" className="secondary-button shrink-0 px-3 text-xs" onClick={onClose}>
            关闭
          </button>
        </div>
      </section>
    </div>
  );
};
