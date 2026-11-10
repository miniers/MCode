// 同 LazyFallback.test.tsx：web/ 没有测试运行器，用 react-dom/server 渲染成字符串后顶层断言。
// 这里校验耗时计算，以及派单胶囊的取名（无 name 时用匹配到的 bee 真名）与完成状态标记。
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import type { RemoteSwarmBee, RemoteSwarmState } from '../types/swarm';
import type { SwarmEntryParsed, SwarmReportSection } from '../utils/markdown';
import { SwarmDispatchCapsules, SwarmReportCapsules, beeDurationMs } from './SwarmCapsules';

function assertEqual(actual: number | null, expected: number | null): void {
  if (actual !== expected) throw new Error(`期望 ${String(expected)}，实际为 ${String(actual)}`);
}

function assertIncludes(haystack: string, needle: string): void {
  if (!haystack.includes(needle)) {
    throw new Error(`期望渲染结果包含 “${needle}”，实际为：${haystack}`);
  }
}

function assertExcludes(haystack: string, needle: string): void {
  if (haystack.includes(needle)) {
    throw new Error(`期望渲染结果不包含 “${needle}”，实际为：${haystack}`);
  }
}

const bee: RemoteSwarmBee = {
  threadID: 'bee-1',
  index: 0,
  name: 'bee',
  task: 'task',
  kind: 'native',
  origin: 'delegated',
  status: 'running',
  toolCalls: 0,
  tokens: 0,
  startedAt: '2026-01-01T00:00:00Z',
  canOpen: true,
  canStop: true
};
const nowMs = Date.parse('2026-01-01T00:00:10Z');

assertEqual(beeDurationMs({ ...bee, finishedAt: '2026-01-01T00:00:05Z' }, nowMs), 5000);
assertEqual(beeDurationMs({ ...bee, status: 'completed' }, nowMs), null);
assertEqual(beeDurationMs(bee, nowMs), 10000);

function swarmStateWith(bees: RemoteSwarmBee[]): RemoteSwarmState {
  return {
    threadID: 'thread-1',
    swarmAvailable: true,
    swarmEnabled: true,
    isBee: false,
    bees
  };
}

// 派单 entry 常不写 name：已按任务匹配到 bee 时显示 bee 真名，与后台任务行对得上。
const unnamedEntry: SwarmEntryParsed = { task: '调研时间线渲染' };
const aspenBee: RemoteSwarmBee = {
  ...bee,
  threadID: 'bee-aspen',
  name: 'Aspen',
  task: '调研时间线渲染',
  status: 'completed',
  finishedAt: '2026-01-01T00:00:05Z'
};
const dispatchHtml = renderToStaticMarkup(
  <SwarmDispatchCapsules entries={[unnamedEntry]} swarmState={swarmStateWith([aspenBee])} />
);
assertIncludes(dispatchHtml, 'Aspen');
assertExcludes(dispatchHtml, '子代理');
// 完成状态渲染对勾标记，便于一眼看出已结束。
assertIncludes(dispatchHtml, 'data-status="completed"');
assertIncludes(dispatchHtml, '5秒');
// 完成的胶囊背景是半透明淡绿、边框带一点绿，与运行中的胶囊区分。
assertIncludes(dispatchHtml, '#34C759 12%');
assertIncludes(dispatchHtml, '#34C759 30%');

// 运行中的 bee 仍然是脉动蓝点，不出现完成标记。
const runningHtml = renderToStaticMarkup(
  <SwarmDispatchCapsules
    entries={[unnamedEntry]}
    swarmState={swarmStateWith([{ ...aspenBee, status: 'running', finishedAt: undefined }])}
  />
);
assertExcludes(runningHtml, 'data-status="completed"');
assertExcludes(runningHtml, '#34C759 12%');
assertIncludes(runningHtml, 'animate-pulse');

// 模拟点击并重排报告，确认展开状态跟随原报告而不是位置。
const reports: SwarmReportSection[] = [
  { name: 'Aspen', task: '任务一', status: '已完成', body: '报告一' },
  { name: 'Aspen', task: '任务二', status: '已完成', body: '报告二' }
];
type ReportButton = React.ReactElement<{ onClick: () => void; 'aria-expanded': boolean }>;
type ReportRow = React.ReactElement<{ children: [ReportButton, React.ReactNode] }>;
type ReportList = React.ReactElement<{ children: ReportRow[] }>;
const internals = (React as unknown as {
  __SECRET_INTERNALS_DO_NOT_USE_OR_YOU_WILL_BE_FIRED: { ReactCurrentDispatcher: { current: unknown } };
}).__SECRET_INTERNALS_DO_NOT_USE_OR_YOU_WILL_BE_FIRED;
const originalDispatcher = internals.ReactCurrentDispatcher.current;
let expanded: Record<string, boolean> = {};
internals.ReactCurrentDispatcher.current = {
  useState: () => [expanded, (update: (previous: Record<string, boolean>) => Record<string, boolean>) => {
    expanded = update(expanded);
  }],
  useMemo: (create: () => unknown) => create()
};
try {
  const initial = SwarmReportCapsules({ reports }) as ReportList;
  initial.props.children[0].props.children[0].props.onClick();
  const reordered = SwarmReportCapsules({ reports: [...reports].reverse() }) as ReportList;
  const [first, second] = reordered.props.children;
  if (first.props.children[0].props['aria-expanded'] || !second.props.children[0].props['aria-expanded']) {
    throw new Error('重排后展开状态未跟随原报告');
  }
  if (first.key === second.key) throw new Error('同名不同任务的报告不应使用相同的 key');
} finally {
  internals.ReactCurrentDispatcher.current = originalDispatcher;
}
