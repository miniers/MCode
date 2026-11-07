// 输入框上方的待办进度胶囊：最新一份待办清单（TodoWrite / update_plan 工具）还没做完时，
// 常驻一枚小胶囊显示「完成数/总数 · 进行中那一项」，点开展开完整清单（三种状态各有样式）。
// 显示条件与文案照桌面端 internal/ui/composer_plan_pill.go:144 renderComposerPlanPill；
// 解析与进度统计在 utils/todoProgress.ts，时间线的待办摘要与这里共用同一份口径。

import React from 'react';
import type { TodoItem, TodoSourceMessage, TodoStatus } from '../../utils/todoProgress';
import { shouldShowTodoProgress, todoProgressText } from '../../utils/todoProgress';

export interface TodoProgressPillProps {
  /** 当前已加载的消息：取最新一条待办消息，口径与桌面端 latestTodoPlan 一致 */
  messages: readonly TodoSourceMessage[];
  /** 会话是否正在运行：决定隐藏条件与进度环是否呼吸 */
  isRunning: boolean;
}

const RING_SIZE = 16;
const RING_STROKE = 2;
const RING_RADIUS = (RING_SIZE - RING_STROKE) / 2;
const RING_CIRCUMFERENCE = 2 * Math.PI * RING_RADIUS;

const STATUS_LABEL: Record<TodoStatus, string> = {
  completed: '已完成',
  in_progress: '进行中',
  pending: '待办'
};

const CheckIcon: React.FC = () => (
  <svg aria-hidden="true" viewBox="0 0 24 24" className="h-3 w-3" fill="none" strokeWidth="2.6" stroke="currentColor">
    <path strokeLinecap="round" strokeLinejoin="round" d="m5 12.5 4.2 4.2L19 7" />
  </svg>
);

/** 三种状态的图标：已完成打勾、进行中空心中点（运行中呼吸）、待办空心圈。 */
const StatusIcon: React.FC<{ status: TodoStatus; running: boolean }> = ({ status, running }) => {
  if (status === 'completed') {
    return (
      <span className="mt-[2px] grid h-3.5 w-3.5 shrink-0 place-items-center rounded-full bg-primary text-white">
        <CheckIcon />
      </span>
    );
  }
  if (status === 'in_progress') {
    return (
      <span
        className={`mt-[2px] grid h-3.5 w-3.5 shrink-0 place-items-center rounded-full border-2 border-primary ${
          running ? 'animate-pulse' : ''
        }`}
      >
        <span className="h-1.5 w-1.5 rounded-full bg-primary" />
      </span>
    );
  }
  return <span className="mt-[2px] grid h-3.5 w-3.5 shrink-0 place-items-center rounded-full border-2 border-muted opacity-70" />;
};

const TodoRow: React.FC<{ item: TodoItem; index: number; running: boolean }> = ({ item, index, running }) => {
  const completed = item.status === 'completed';
  const inProgress = item.status === 'in_progress';
  return (
    <li
      data-todo-index={index}
      data-todo-status={item.status}
      className={`flex items-start gap-2 text-caption ${
        completed ? 'text-muted' : inProgress ? 'font-medium text-foreground' : 'text-foreground'
      }`}
    >
      <StatusIcon status={item.status} running={running} />
      <span className={`min-w-0 break-words ${completed ? 'line-through opacity-75' : ''}`}>{item.text}</span>
      <span className="sr-only">{STATUS_LABEL[item.status]}</span>
    </li>
  );
};

export const TodoProgressPill: React.FC<TodoProgressPillProps> = ({ messages, isRunning }) => {
  const progress = React.useMemo(() => shouldShowTodoProgress(messages, isRunning), [messages, isRunning]);
  const [expanded, setExpanded] = React.useState(false);

  // 胶囊消失时顺带收起清单，避免下次出现时突然弹开
  React.useEffect(() => {
    if (!progress) setExpanded(false);
  }, [progress]);

  if (!progress) return null;

  const fraction = progress.total > 0 ? progress.done / progress.total : 0;
  const ringOffset = RING_CIRCUMFERENCE * (1 - fraction);

  return (
    <div className="relative mx-auto flex w-full max-w-[880px] items-center px-4 pb-1">
      <button
        type="button"
        onClick={() => setExpanded((open) => !open)}
        aria-expanded={expanded}
        aria-label={`待办进度 ${progress.done}/${progress.total}`}
        title={expanded ? '收起待办清单' : '查看待办清单'}
        className="inline-flex min-h-[26px] max-w-[min(420px,100%)] items-center gap-1.5 rounded-full border border-border bg-card px-2.5 py-[3px] text-caption text-foreground transition hover:bg-user active:scale-95"
      >
        <svg
          aria-hidden="true"
          viewBox={`0 0 ${RING_SIZE} ${RING_SIZE}`}
          className={`h-4 w-4 shrink-0 -rotate-90 ${isRunning ? 'animate-pulse' : 'opacity-90'}`}
        >
          <circle cx={RING_SIZE / 2} cy={RING_SIZE / 2} r={RING_RADIUS} fill="none" stroke="var(--border)" strokeWidth={RING_STROKE} />
          <circle
            cx={RING_SIZE / 2}
            cy={RING_SIZE / 2}
            r={RING_RADIUS}
            fill="none"
            stroke="var(--primary)"
            strokeWidth={RING_STROKE}
            strokeLinecap="round"
            strokeDasharray={RING_CIRCUMFERENCE}
            strokeDashoffset={ringOffset}
          />
        </svg>
        <span className="min-w-0 truncate">{todoProgressText(progress)}</span>
      </button>

      {expanded && (
        <div
          role="dialog"
          aria-label="待办清单"
          className="absolute bottom-full left-0 z-30 mb-2 w-[min(360px,calc(100vw-32px))] overflow-hidden rounded-2xl border border-border bg-card shadow-xl"
        >
          <div className="flex items-center justify-between gap-2 border-b border-border px-3 py-2 text-caption text-muted">
            <span className="font-medium">待办清单</span>
            <span>
              {progress.done}/{progress.total}
            </span>
          </div>
          <ul className="max-h-[240px] space-y-1.5 overflow-y-auto px-3 py-2">
            {progress.items.map((item, index) => (
              <TodoRow key={`${index}-${item.text}`} item={item} index={index} running={isRunning} />
            ))}
          </ul>
        </div>
      )}
    </div>
  );
};

export default TodoProgressPill;
