import React from 'react';
import type { TodoStatus } from '../../utils/todoProgress';
import { CheckCircleIcon, ChevronRightIcon, CircleIcon } from '../Icons';
import type { TurnChecklistData } from './projection';
import { RunningGlyph } from './RunningGlyph';

const CHECKLIST_TRANSITION_STYLES = `
.mc-turn-checklist-collapse {
  display: grid;
  grid-template-rows: 0fr;
  opacity: 0;
  visibility: hidden;
  transition:
    grid-template-rows var(--mc-dur-base) cubic-bezier(.2, .8, .2, 1),
    opacity var(--mc-dur-snap) ease-out,
    visibility 0s linear var(--mc-dur-base);
}
.mc-turn-checklist-collapse.is-open {
  grid-template-rows: 1fr;
  opacity: 1;
  visibility: visible;
  transition:
    grid-template-rows var(--mc-dur-base) cubic-bezier(.2, .8, .2, 1),
    opacity var(--mc-dur-snap) ease-out,
    visibility 0s;
}
.mc-turn-checklist-collapse__inner {
  overflow: hidden;
  min-height: 0;
}
.mc-turn-checklist-chevron {
  transition: transform var(--mc-dur-snap) cubic-bezier(.2, .8, .2, 1);
}
.mc-turn-checklist-chevron.is-open {
  transform: rotate(90deg);
}
@media (prefers-reduced-motion: reduce) {
  .mc-turn-checklist-collapse,
  .mc-turn-checklist-collapse.is-open,
  .mc-turn-checklist-chevron {
    transition: none !important;
  }
}
`;

export interface TurnChecklistTriggerProps {
  checklist: TurnChecklistData;
  open: boolean;
  onToggle: () => void;
}

/** 回合头步骤入口段：`› ○ 12/12 步`，点击只开合清单卡片，不触发回合折叠体。 */
export const TurnChecklistTrigger: React.FC<TurnChecklistTriggerProps> = ({
  checklist,
  open,
  onToggle
}) => {
  const allCompleted = checklist.total > 0 && checklist.done >= checklist.total;

  return (
    <button
      type="button"
      data-turn-checklist-trigger="true"
      aria-expanded={open}
      aria-label={
        open
          ? `收起步骤清单（已完成 ${checklist.done}/${checklist.total} 步）`
          : `展开步骤清单（已完成 ${checklist.done}/${checklist.total} 步）`
      }
      onClick={(event) => {
        event.stopPropagation();
        onToggle();
      }}
      className="inline-flex shrink-0 items-center gap-[var(--mc-space-1)] rounded-[var(--mc-radius-sm)] px-[var(--mc-space-2)] py-[2px] text-mc-body font-medium text-[var(--mc-text-secondary)] hover:bg-[var(--mc-overlay-hover)] hover:text-[var(--mc-text-primary)] active:bg-[var(--mc-overlay-pressed)] transition-colors duration-mc-fast motion-reduce:transition-none"
    >
      <span
        aria-hidden="true"
        className={`inline-flex shrink-0 text-[var(--mc-text-tertiary)] mc-turn-checklist-chevron${
          open ? ' is-open' : ''
        }`}
      >
        <ChevronRightIcon className="h-[var(--mc-icon-sm)] w-[var(--mc-icon-sm)]" />
      </span>
      <span aria-hidden="true" className="inline-flex shrink-0">
        {allCompleted ? (
          <CheckCircleIcon className="h-[var(--mc-icon-sm)] w-[var(--mc-icon-sm)] text-[var(--mc-success)]" />
        ) : (
          <CircleIcon className="h-[var(--mc-icon-sm)] w-[var(--mc-icon-sm)] text-[var(--mc-text-tertiary)]" />
        )}
      </span>
      <span className="tabular-nums whitespace-nowrap">
        {checklist.done}/{checklist.total} 步
      </span>
    </button>
  );
};

TurnChecklistTrigger.displayName = 'TurnChecklistTrigger';

const ChecklistStepIcon: React.FC<{ status: TodoStatus; running: boolean }> = ({
  status,
  running
}) => {
  if (status === 'completed') {
    return (
      <CheckCircleIcon className="h-[var(--mc-icon-sm)] w-[var(--mc-icon-sm)] text-[var(--mc-success)]" />
    );
  }
  if (status === 'in_progress') {
    if (running) {
      return <RunningGlyph size={12} label="进行中" />;
    }
    return (
      <span
        className="block rounded-full bg-[var(--mc-accent)]"
        style={{ width: 'var(--mc-space-2)', height: 'var(--mc-space-2)' }}
      />
    );
  }
  return (
    <CircleIcon className="h-[var(--mc-icon-sm)] w-[var(--mc-icon-sm)] text-[var(--mc-border)]" />
  );
};

export interface TurnChecklistCardProps {
  checklist: TurnChecklistData;
  open: boolean;
  running: boolean;
}

/** 回合头正下方的逐行打勾清单卡片；独立于回合「用时」折叠体。 */
export const TurnChecklistCard: React.FC<TurnChecklistCardProps> = ({
  checklist,
  open,
  running
}) => (
  <div
    data-turn-checklist-card="true"
    data-checklist-open={open ? 'true' : 'false'}
    aria-hidden={!open}
    className={`mc-turn-checklist-collapse${open ? ' is-open' : ''}`}
  >
    <style>{CHECKLIST_TRANSITION_STYLES}</style>
    <div className="mc-turn-checklist-collapse__inner">
      <div className="pt-[var(--mc-space-2)]">
        <ul
          aria-label="回合步骤清单"
          className="space-y-[var(--mc-space-1)] rounded-[var(--mc-radius-lg)] border border-[var(--mc-hairline)] bg-[var(--mc-surface-main)] px-[var(--mc-space-4)] py-[var(--mc-space-3)]"
        >
          {checklist.items.map((item, index) => (
            <li
              key={`${index}-${item.text}`}
              data-checklist-status={item.status}
              className="flex min-w-0 items-start gap-[var(--mc-space-2)] text-mc-body leading-[20px]"
            >
              <span
                aria-hidden="true"
                className="inline-flex shrink-0 items-center justify-center"
                style={{ width: 'var(--mc-icon-sm)', height: '20px' }}
              >
                <ChecklistStepIcon status={item.status} running={running} />
              </span>
              <span
                className={`min-w-0 break-words ${
                  item.status === 'completed'
                    ? 'text-[var(--mc-text-secondary)]'
                    : 'text-[var(--mc-text-primary)]'
                }`}
              >
                {item.text}
              </span>
            </li>
          ))}
        </ul>
      </div>
    </div>
  </div>
);

TurnChecklistCard.displayName = 'TurnChecklistCard';
