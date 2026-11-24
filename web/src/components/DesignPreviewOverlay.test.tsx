// 使用 react-dom/server 校验看稿层在 SSR 下的核心契约，不引入额外测试框架。
import { renderToStaticMarkup } from 'react-dom/server';
import type { RemoteSwarmBee } from '../types/swarm';
import { DesignPreviewOverlay } from './DesignPreviewOverlay';

function assertIncludes(haystack: string, needle: string, label: string): void {
  if (!haystack.includes(needle)) {
    throw new Error(`${label}：期望包含 ${needle}，实际为 ${haystack}`);
  }
}

function assertExcludes(haystack: string, needle: string, label: string): void {
  if (haystack.includes(needle)) {
    throw new Error(`${label}：期望不包含 ${needle}，实际为 ${haystack}`);
  }
}

const baseBee: RemoteSwarmBee = {
  threadID: 'bee-1',
  index: 0,
  name: 'Design Bee',
  task: '设计页面',
  kind: 'native',
  origin: 'delegated',
  status: 'completed',
  toolCalls: 3,
  tokens: 1200,
  startedAt: '2026-01-01T00:00:00Z',
  canOpen: true,
  canStop: false,
  designFiles: ['.mcode/design/home.html']
};

const noopAsync = async () => {};
const noopPreview = async () => '/p/token/home.html';

// 1. iframe sandbox 必须正好是 allow-scripts，不含 allow-same-origin；非 awaitingFinalize 时不出现定稿按钮
const normalHtml = renderToStaticMarkup(
  <DesignPreviewOverlay
    threadID="bee-1"
    bee={{ ...baseBee, awaitingFinalize: false }}
    activePath=".mcode/design/home.html"
    onSelectFile={() => {}}
    onClose={() => {}}
    openThreadPreview={noopPreview}
    onSendFeedback={noopAsync}
    onFinalizeDesign={noopAsync}
  />
);

assertIncludes(normalHtml, 'sandbox="allow-scripts"', 'iframe sandbox 属性');
assertExcludes(normalHtml, 'allow-same-origin', '禁止 allow-same-origin');
assertIncludes(normalHtml, 'home', '去掉目录与 .html 的标题');
assertExcludes(normalHtml, '>定稿<', '非待定稿态不渲染定稿按钮');
assertExcludes(normalHtml, 'role="tablist"', '单份稿件不渲染切换标签');

// 2. awaitingFinalize 为真时显示定稿按钮
const awaitingHtml = renderToStaticMarkup(
  <DesignPreviewOverlay
    threadID="bee-1"
    bee={{ ...baseBee, awaitingFinalize: true }}
    activePath=".mcode/design/home.html"
    onSelectFile={() => {}}
    onClose={() => {}}
    openThreadPreview={noopPreview}
    onSendFeedback={noopAsync}
    onFinalizeDesign={noopAsync}
  />
);

assertIncludes(awaitingHtml, '>定稿<', '待定稿态渲染定稿按钮');

// 3. 多份稿子时出现切换标签
const multiHtml = renderToStaticMarkup(
  <DesignPreviewOverlay
    threadID="bee-1"
    bee={{ ...baseBee, designFiles: ['.mcode/design/home.html', '.mcode/design/detail.html'] }}
    activePath=".mcode/design/home.html"
    onSelectFile={() => {}}
    onClose={() => {}}
    openThreadPreview={noopPreview}
    onSendFeedback={noopAsync}
    onFinalizeDesign={noopAsync}
  />
);

assertIncludes(multiHtml, 'role="tablist"', '多份稿件出现 tablist');
assertIncludes(multiHtml, 'role="tab"', '多份稿件出现 tab');
assertIncludes(multiHtml, 'detail', '多份稿件显示第二个文件名');
