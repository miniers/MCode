// 侧栏话题行：一行显示标题、阶段、任务进度与待处理数，右侧「⋯」打开话题操作面板。
// 呈现口径对齐桌面端 internal/ui/steward_topics.go:539 的行结构与 internal/app/client_steward.go:223 的待办计数。
// 计数、分组、按钮出现条件都写成导出的纯函数，方便上层复用与断言。

import React from 'react';
import { EllipsisHorizontalIcon } from './Icons';
import type { RemoteStewardTopic } from '../types/generated';

/** 话题上的动作标识，与宿主 steward.topics.* 方法一一对应。 */
export type StewardTopicActionID = 'confirm' | 'replan' | 'close' | 'delete';

/** 宿主是否提供了对应动作的远程方法；没提供就不显示按钮。 */
export interface StewardTopicActionCaps {
  confirm: boolean;
  replan: boolean;
  close: boolean;
  delete: boolean;
}

export interface StewardTopicAction {
  id: StewardTopicActionID;
  label: string;
  /** 二次确认文案；为空表示点击即执行。 */
  confirmText?: string;
  danger?: boolean;
}

/**
 * 数一件话题里有几件事等用户拍板。
 * 桌面端口径见 internal/app/client_steward.go:223 的 stewardTopicPendingUser，其中「待验收任务数」依赖看板数据、
 * 未上线到话题协议，这里用 tasksTotal - tasksDone 近似；closed 的话题不再计入。
 */
export function stewardTopicPendingCount(topic: RemoteStewardTopic): number {
  if (topic.closed) return 0;
  const remaining = Math.max(0, topic.tasksTotal - topic.tasksDone);
  switch (topic.phase) {
    case 'drafting':
      // 任务书已写好、没有待回答的追问，就等用户确认开工。
      return remaining + (topic.openQuestions === 0 && topic.brief.trim() !== '' ? 1 : 0);
    case 'plan_failed':
    case 'completed':
      // 规划失败要等用户点「重新规划」，已完成要等用户确认结项。
      return remaining + 1;
    default:
      return remaining;
  }
}

/** 话题分组：在办话题按最后更新倒序，已结束的收进历史（对齐桌面 splitStewardTopics / sortStewardTopics）。 */
export function splitStewardTopics(topics: RemoteStewardTopic[]): {
  active: RemoteStewardTopic[];
  history: RemoteStewardTopic[];
} {
  const sorted = [...topics].sort((first, second) => {
    const firstTime = Date.parse(first.updatedAt ?? '') || 0;
    const secondTime = Date.parse(second.updatedAt ?? '') || 0;
    return secondTime - firstTime;
  });
  return {
    active: sorted.filter((topic) => !topic.closed),
    history: sorted.filter((topic) => topic.closed)
  };
}

/** 总管固定行上的待处理徽标：在办话题的待办数之和（历史话题不计）。 */
export function stewardPendingTotal(topics: RemoteStewardTopic[]): number {
  return splitStewardTopics(topics).active.reduce((sum, topic) => sum + stewardTopicPendingCount(topic), 0);
}

/**
 * 话题操作菜单的按钮，出现条件照抄桌面端的 stewardTopicPrimaryActionFor
 * （internal/ui/steward_topic_card.go:232）与右键菜单的「删除话题…」（internal/ui/steward_topics.go:893）。
 * plan_review 阶段的「直接开工」在桌面端走 StartStewardTopicGoal，宿主没有开放对应的远程方法，因此 Web 侧不显示。
 */
export function stewardTopicActions(topic: RemoteStewardTopic, caps: StewardTopicActionCaps): StewardTopicAction[] {
  const actions: StewardTopicAction[] = [];
  if (caps.confirm && topic.phase === 'drafting' && topic.brief.trim() !== '') {
    actions.push({ id: 'confirm', label: '直接开工' });
  }
  if (caps.replan && topic.phase === 'plan_failed') {
    actions.push({ id: 'replan', label: '重新规划' });
  }
  if (caps.close && topic.phase === 'completed' && !topic.closed) {
    actions.push({ id: 'close', label: '确认结项' });
  }
  if (caps.delete) {
    actions.push({
      id: 'delete',
      label: '删除话题',
      confirmText: '删除后话题、目标与在跑的任务会一起消失，且无法撤销。',
      danger: true
    });
  }
  return actions;
}

/** 任务进度百分比，没有任务时返回 0。 */
export function stewardTopicProgressPercent(topic: RemoteStewardTopic): number {
  if (topic.tasksTotal <= 0) return 0;
  const done = Math.min(Math.max(topic.tasksDone, 0), topic.tasksTotal);
  return Math.round((done / topic.tasksTotal) * 100);
}

export interface StewardTopicRowProps {
  topic: RemoteStewardTopic;
  /** 阶段文案由上层用既有的 stewardTopicPhaseLabel 算好传进来。 */
  phaseLabel: string;
  /** 全部项目视图下显示归属项目名。 */
  projectName?: string;
  onOpen: (topic: RemoteStewardTopic) => void;
  onMore: (topic: RemoteStewardTopic) => void;
}

export const StewardTopicRow: React.FC<StewardTopicRowProps> = ({
  topic,
  phaseLabel,
  projectName,
  onOpen,
  onMore
}) => {
  const pending = stewardTopicPendingCount(topic);
  const percent = stewardTopicProgressPercent(topic);
  const meta = [projectName, phaseLabel].filter(Boolean).join(' · ');

  return (
    <div className="gutter-x flex w-full min-w-0 items-center gap-1">
      <button
        type="button"
        onClick={() => onOpen(topic)}
        className="flex min-h-[44px] min-w-0 flex-1 flex-col justify-center gap-0.5 py-1 text-left transition-colors hover:bg-row-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sky-500 rounded-lg"
        title={topic.title || '未命名话题'}
      >
        <span className="flex w-full min-w-0 items-center gap-1.5">
          <span className="min-w-0 flex-1 truncate text-footnote">{topic.title || '未命名话题'}</span>
          {pending > 0 && (
            <span
              className="shrink-0 rounded-full bg-danger px-1.5 text-mc-caption font-semibold leading-[18px] text-white"
              aria-label={`${pending} 项待处理`}
            >
              {pending}
            </span>
          )}
        </span>
        <span className="flex w-full min-w-0 items-center gap-1.5 text-mc-caption text-muted">
          {meta && <span className="shrink-0 truncate max-w-[60%]">{meta}</span>}
          {topic.tasksTotal > 0 && (
            <>
              <span className="h-1 min-w-[36px] flex-1 overflow-hidden rounded-full bg-border" aria-hidden="true">
                <span className="block h-full rounded-full bg-primary" style={{ width: `${percent}%` }} />
              </span>
              <span className="shrink-0 tabular-nums">
                {topic.tasksDone}/{topic.tasksTotal}
              </span>
            </>
          )}
        </span>
      </button>
      <button
        type="button"
        className="icon-button min-h-[44px] min-w-[44px] shrink-0"
        onClick={() => onMore(topic)}
        aria-label={`打开话题 ${topic.title || '未命名话题'} 的操作菜单`}
        title="更多操作"
      >
        <EllipsisHorizontalIcon className="h-4 w-4" />
      </button>
    </div>
  );
};
