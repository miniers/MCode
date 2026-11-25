// 同 SwarmCapsules.test.tsx：web/ 没有测试运行器，用 react-dom/server 渲染成字符串后顶层断言。
// 校验设计稿待确认横幅的过滤条件与渲染内容。
import { renderToStaticMarkup } from 'react-dom/server';
import type { RemoteSwarmBee } from '../../types/swarm';
import { SwarmDesignBanner } from './SwarmDesignBanner';

function assertEqual(actual: string, expected: string): void {
  if (actual !== expected) {
    throw new Error(`期望 ${JSON.stringify(expected)}，实际为 ${JSON.stringify(actual)}`);
  }
}

function assertIncludes(haystack: string, needle: string): void {
  if (!haystack.includes(needle)) {
    throw new Error(`期望渲染结果包含 “${needle}”，实际为：${haystack}`);
  }
}

const baseBee: RemoteSwarmBee = {
  threadID: 'bee-design-1',
  index: 0,
  name: 'Luna',
  task: '输出设置页设计稿',
  kind: 'native',
  origin: 'delegated',
  status: 'completed',
  toolCalls: 3,
  tokens: 1200,
  startedAt: '2026-01-01T00:00:00Z',
  canOpen: true,
  canStop: false
};

const noopOpenDesign = () => {};

// (a) 有 awaitingFinalize 且 designFiles 非空的 bee 时，展示标题、bee 名和「看稿」按钮。
const activeHtml = renderToStaticMarkup(
  <SwarmDesignBanner
    bees={[{ ...baseBee, awaitingFinalize: true, designFiles: ['docs/design/settings.html'] }]}
    onOpenDesign={noopOpenDesign}
  />
);
assertIncludes(activeHtml, '设计稿待你确认');
assertIncludes(activeHtml, 'Luna');
assertIncludes(activeHtml, '输出设置页设计稿');
assertIncludes(activeHtml, '看稿');

// (b) awaitingFinalize 为 false 或 designFiles 为空时，不渲染任何内容。
const notAwaitingHtml = renderToStaticMarkup(
  <SwarmDesignBanner
    bees={[{ ...baseBee, awaitingFinalize: false, designFiles: ['docs/design/settings.html'] }]}
    onOpenDesign={noopOpenDesign}
  />
);
assertEqual(notAwaitingHtml, '');

const emptyFilesHtml = renderToStaticMarkup(
  <SwarmDesignBanner
    bees={[{ ...baseBee, awaitingFinalize: true, designFiles: [] }]}
    onOpenDesign={noopOpenDesign}
  />
);
assertEqual(emptyFilesHtml, '');

const missingFilesHtml = renderToStaticMarkup(
  <SwarmDesignBanner
    bees={[{ ...baseBee, awaitingFinalize: true }]}
    onOpenDesign={noopOpenDesign}
  />
);
assertEqual(missingFilesHtml, '');
