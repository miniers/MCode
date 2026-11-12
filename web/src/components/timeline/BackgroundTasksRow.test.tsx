// 同 LazyFallback.test.tsx：web/ 没有测试运行器，用 react-dom/server 渲染成字符串后顶层断言。
// 这里校验等待标题的格式、空闲/运行两种形态的文案，以及后台任务只读（不出现停止入口）。
import { renderToStaticMarkup } from 'react-dom/server';
import { pickByHash, SwarmAvatar } from '../swarmAvatar';
import {
  BackgroundTasksRow,
  backgroundTasksLabel,
  backgroundWaitTitle,
  requestBackgroundTaskLog,
  stopBackgroundTask,
  type BackgroundTaskCommand
} from './BackgroundTasksRow';

function assertEqual<T>(actual: T, expected: T, what: string): void {
  if (actual !== expected) {
    throw new Error(`期望${what}为 ${String(expected)}，实际为 ${String(actual)}`);
  }
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

// 等待标题：任务名与计时按需拼接，计时不足一分钟按秒、否则按 m:ss。
assertEqual(
  backgroundWaitTitle(2, 'npm run dev', 65),
  '等待 2 个后台任务 · npm run dev · 1:05',
  '带任务名与计时的等待标题'
);
assertEqual(backgroundWaitTitle(1, '', 36), '等待 1 个后台任务 · 36s', '省略任务名的等待标题');
assertEqual(backgroundWaitTitle(1, 'npm run dev', null), '等待 1 个后台任务 · npm run dev', '省略计时的等待标题');
assertEqual(backgroundWaitTitle(3, '  ', 5), '等待 3 个后台任务 · 5s', '任务名只有空白时省略该段');

// 运行态聚合文案：命令口径改成「项任务」。
assertEqual(backgroundTasksLabel(2, 0), '后台 · 2 项任务', '运行态文案');
assertEqual(backgroundTasksLabel(0, 1), '后台 · 1 个子智能体', '只有子智能体时的文案');

const backgroundCommand: BackgroundTaskCommand = {
  id: 'cmd-1',
  snippet: 'npm run dev',
  status: 'background',
  startedAt: new Date().toISOString()
};

// 空闲：等待文案 + 命令名 + 时钟图标，外框用主色描边。
const waitingHtml = renderToStaticMarkup(
  <BackgroundTasksRow commands={[backgroundCommand]} bees={[]} waiting />
);
assertIncludes(waitingHtml, '等待 1 个后台任务');
assertIncludes(waitingHtml, 'npm run dev');
assertIncludes(waitingHtml, 'border-[var(--primary)]/40');
assertIncludes(waitingHtml, '<circle');

// 缺开始时间时只省略计时段，不退回成「后台 · N 项任务」。
const noStartedAtHtml = renderToStaticMarkup(
  <BackgroundTasksRow
    commands={[{ id: 'cmd-2', snippet: 'tail -f app.log', status: 'background' }]}
    bees={[]}
    waiting
  />
);
assertIncludes(noStartedAtHtml, '等待 1 个后台任务');
assertIncludes(noStartedAtHtml, 'tail -f app.log');

// 运行中：保持原有文案，不出现等待专用样式。
const runningHtml = renderToStaticMarkup(
  <BackgroundTasksRow commands={[{ id: 'cmd-3', snippet: 'go test ./...', status: 'running' }]} bees={[]} />
);
assertIncludes(runningHtml, '后台 · 1 项任务');
assertExcludes(runningHtml, '等待 1 个后台任务');

// 后台任务只读：任何形态都不出现停止入口。
assertExcludes(waitingHtml, '停止');
assertExcludes(runningHtml, '停止');

const calls: string[] = [];
const logResult = await requestBackgroundTaskLog('thread-1', 'cmd-1', async (threadID, messageID) => {
  calls.push(`log:${threadID}:${messageID}`);
  return { text: 'process output', available: true };
});
assertEqual(logResult.text, 'process output', '后台日志');
await stopBackgroundTask('thread-1', 'cmd-1', async (threadID, messageID) => {
  calls.push(`stop:${threadID}:${messageID}`);
  return { stopped: true };
});
assertEqual(calls.join(','), 'log:thread-1:cmd-1,stop:thread-1:cmd-1', '日志与停止远程调用');

// 子智能体行改用与派单胶囊同一套头像：名字首字母 + 按名字哈希的底色。
// 展开列表要到点击后才渲染，静态渲染拿不到，这里直接断言行内使用的头像组件。
const bee = {
  threadID: 'bee-1',
  index: 0,
  name: 'Aspen',
  task: '调研时间线渲染',
  kind: 'native' as const,
  origin: 'delegated' as const,
  status: 'running' as const,
  toolCalls: 0,
  tokens: 0,
  startedAt: new Date().toISOString(),
  canOpen: true,
  canStop: true
};
const avatarHtml = renderToStaticMarkup(<SwarmAvatar name={bee.name} tint={pickByHash(bee.name)} />);
assertIncludes(avatarHtml, '>A<');

// bee 计入聚合文案，说明行内确实拿到了子智能体。
const beeRowHtml = renderToStaticMarkup(<BackgroundTasksRow commands={[]} bees={[bee]} />);
assertIncludes(beeRowHtml, '后台 · 1 个子智能体');
