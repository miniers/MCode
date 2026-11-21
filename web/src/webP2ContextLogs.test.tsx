// @ts-expect-error web/ 未安装 @types/node，测试运行时由 node 提供 node:fs / node:assert
import assert from 'node:assert/strict';
// @ts-expect-error web/ 未安装 @types/node，测试运行时由 node 提供 node:fs
import { readFileSync } from 'node:fs';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { LiveLogsPanel, isScrolledNearBottom } from './components/LiveLogsPanel';
import { ProjectContextSheet } from './components/ProjectContextSheet';
import { MenuPageChromeContext } from './components/thread/menu/menuPageChrome';
import type { RemoteProjectSummary } from './types/contract';
import type {
  RemoteContextListDocsResult,
  RemoteContextReadDocResult,
  RemoteLogsSubscribeResult
} from './types/generated';

function readText(relativePath: string): string {
  return readFileSync(new URL(relativePath, import.meta.url), 'utf8');
}

// ---------- 1. 静态源码与无障碍 / Token / 入口门控断言 ----------

const contextSheetTsx = readText('./components/ProjectContextSheet.tsx');
assert.ok(contextSheetTsx.includes('<SheetFrame'), 'ProjectContextSheet 应复用 SheetFrame');
assert.ok(contextSheetTsx.includes('size="panel"'), 'ProjectContextSheet 独立抽屉应使用 SheetFrame size="panel"');
assert.ok(contextSheetTsx.includes('<MenuSheetHeader'), 'ProjectContextSheet 应复用 MenuSheetHeader');
assert.ok(contextSheetTsx.includes('pickableProjects(projects)'), 'ProjectContextSheet 全部项目模式应使用 pickableProjects');
assert.ok(contextSheetTsx.includes('text-base'), 'ProjectContextSheet 搜索框应使用 text-base 避免 iOS 缩放');
assert.ok(contextSheetTsx.includes('mc-skeleton'), 'ProjectContextSheet 加载中应使用 mc-skeleton 骨架屏');
assert.ok(contextSheetTsx.includes('<MarkdownView'), 'ProjectContextSheet 正文应复用 MarkdownView');
assert.ok(contextSheetTsx.includes('内容已截断'), 'ProjectContextSheet 应包含截断提示');
assert.ok(!contextSheetTsx.includes('📚') && !contextSheetTsx.includes('📄'), 'ProjectContextSheet 不得包含原始 Emoji');

const liveLogsTsx = readText('./components/LiveLogsPanel.tsx');
assert.ok(liveLogsTsx.includes('<SheetFrame'), 'LiveLogsPanel 窄屏独立模式应复用 SheetFrame');
assert.ok(liveLogsTsx.includes('menu-side-panel'), 'LiveLogsPanel 宽屏独立模式应复用 .menu-side-panel');
assert.ok(liveLogsTsx.includes('font-mono text-mc-code select-text whitespace-pre-wrap break-all'), 'LiveLogsPanel 输出区应使用等宽语义样式');
assert.ok(liveLogsTsx.includes('回到底部'), 'LiveLogsPanel 应提供回到底部控制按钮');
assert.ok(liveLogsTsx.includes('role="log"'), 'LiveLogsPanel 输出区应具备 role="log"');
assert.ok(liveLogsTsx.includes('role="toolbar"'), 'LiveLogsPanel 流筛选栏应具备 role="toolbar"');
assert.ok(!liveLogsTsx.includes('⏳') && !liveLogsTsx.includes('❌'), 'LiveLogsPanel 不得包含原始 Emoji');

const threadListTsx = readText('./components/ThreadList.tsx');
assert.ok(
  threadListTsx.includes("supportsMethod('context.listDocs')") && threadListTsx.includes('项目文档'),
  'ThreadList.tsx HomeMoreMenu 应包含受 context.listDocs 门控的「项目文档」入口'
);
assert.ok(threadListTsx.includes('<ProjectContextSheet'), 'ThreadList.tsx 应挂载独立 ProjectContextSheet');

const threadMenuTsx = readText('./components/thread/ThreadMenuSheet.tsx');
assert.ok(
  threadMenuTsx.includes("supportsMethod('context.listDocs') && Boolean(latestSummary.projectID)"),
  'ThreadMenuSheet.tsx 应以 context.listDocs 与 projectID 门控项目文档入口'
);
assert.ok(
  threadMenuTsx.includes("supportsMethod('logs.subscribe')"),
  'ThreadMenuSheet.tsx 应以 logs.subscribe 门控实时日志入口'
);
assert.ok(
  threadMenuTsx.includes("pushPage({ kind: 'context' })") && threadMenuTsx.includes("pushPage({ kind: 'logs' })"),
  'ThreadMenuSheet.tsx 应支持入栈 context 与 logs 页面'
);
assert.ok(
  threadMenuTsx.includes('<ProjectContextSheet') && threadMenuTsx.includes('<LiveLogsPanel'),
  'ThreadMenuSheet.tsx 栈内应渲染 ProjectContextSheet 与 LiveLogsPanel'
);

const threadViewTsx = readText('./components/ThreadView.tsx');
assert.ok(
  threadViewTsx.includes('contextSupported') && threadViewTsx.includes('logsSupported'),
  'ThreadView.tsx 应将 contextSupported 与 logsSupported 传入 menuBaseForRoute'
);

// ---------- 2. 智能滚底阈值纯函数校验 ----------

assert.equal(isScrolledNearBottom(1000, 700, 300), true, '贴底时应允许自动滚底');
assert.equal(isScrolledNearBottom(1000, 680, 300), true, '离底 <= 24px 时应继续自动滚底');
assert.equal(isScrolledNearBottom(1000, 670, 300), false, '离底 > 24px 时应暂停自动滚底');

// ---------- 3. renderToStaticMarkup 组件渲染与交互状态断言 ----------

type HookHarness = {
  reset: () => void;
  setContext: (next: Record<string, unknown>) => void;
  renderMarkup: (element: React.ReactElement) => string;
  flush: () => Promise<void>;
  restore: () => void;
};

function createHookHarness(): HookHarness {
  const originalUseState = React.useState;
  const originalUseRef = React.useRef;
  const originalUseCallback = React.useCallback;
  const originalUseMemo = React.useMemo;
  const originalUseEffect = React.useEffect;
  const originalUseContext = React.useContext;

  let stateSlots: unknown[] = [];
  let refSlots: { current: unknown }[] = [];
  let callbackSlots: { fn: unknown; deps?: readonly unknown[] }[] = [];
  let memoSlots: { value: unknown; deps?: readonly unknown[] }[] = [];
  let effectSlots: { deps?: readonly unknown[]; cleanup?: () => void }[] = [];
  let stateIndex = 0;
  let refIndex = 0;
  let callbackIndex = 0;
  let memoIndex = 0;
  let effectIndex = 0;
  let fakeContextValue: Record<string, unknown> = {};

  const depsChanged = (prev: readonly unknown[], next: readonly unknown[]) =>
    prev.length !== next.length || prev.some((value, idx) => !Object.is(value, next[idx]));

  Object.assign(React, {
    useState: <T,>(initial: T | (() => T)) => {
      const idx = stateIndex++;
      if (!(idx in stateSlots)) {
        stateSlots[idx] = typeof initial === 'function' ? (initial as () => T)() : initial;
      }
      const setState = (next: T | ((prev: T) => T)) => {
        const prev = stateSlots[idx] as T;
        stateSlots[idx] = typeof next === 'function' ? (next as (p: T) => T)(prev) : next;
      };
      return [stateSlots[idx] as T, setState];
    },
    useRef: <T,>(initial: T) => {
      const idx = refIndex++;
      if (!(idx in refSlots)) {
        refSlots[idx] = { current: initial };
      }
      return refSlots[idx] as { current: T };
    },
    useCallback: <T,>(fn: T, deps?: readonly unknown[]) => {
      const idx = callbackIndex++;
      const slot = callbackSlots[idx];
      if (slot && deps && slot.deps && !depsChanged(slot.deps, deps)) {
        return slot.fn as T;
      }
      callbackSlots[idx] = { fn, deps };
      return fn;
    },
    useMemo: <T,>(factory: () => T, deps?: readonly unknown[]) => {
      const idx = memoIndex++;
      const slot = memoSlots[idx];
      if (slot && deps && slot.deps && !depsChanged(slot.deps, deps)) {
        return slot.value as T;
      }
      const value = factory();
      memoSlots[idx] = { value, deps };
      return value;
    },
    useEffect: (callback: () => void | (() => void), deps?: readonly unknown[]) => {
      const idx = effectIndex++;
      const slot = effectSlots[idx] ?? (effectSlots[idx] = {});
      if (deps && slot.deps && !depsChanged(slot.deps, deps)) return;
      slot.cleanup?.();
      const cleanup = callback();
      slot.cleanup = typeof cleanup === 'function' ? cleanup : undefined;
      slot.deps = deps;
    },
    useContext: (context: React.Context<unknown>) => {
      if (context === MenuPageChromeContext) {
        return { leading: 'none', onLeading: () => {} };
      }
      return fakeContextValue;
    }
  });

  return {
    reset() {
      for (const slot of effectSlots) slot.cleanup?.();
      stateSlots = [];
      refSlots = [];
      callbackSlots = [];
      memoSlots = [];
      effectSlots = [];
    },
    setContext(next) {
      fakeContextValue = next;
    },
    renderMarkup(element) {
      stateIndex = 0;
      refIndex = 0;
      callbackIndex = 0;
      memoIndex = 0;
      effectIndex = 0;
      return renderToStaticMarkup(element);
    },
    async flush() {
      for (let i = 0; i < 6; i += 1) await Promise.resolve();
    },
    restore() {
      for (const slot of effectSlots) slot.cleanup?.();
      Object.assign(React, {
        useState: originalUseState,
        useRef: originalUseRef,
        useCallback: originalUseCallback,
        useMemo: originalUseMemo,
        useEffect: originalUseEffect,
        useContext: originalUseContext
      });
    }
  };
}

const projects: RemoteProjectSummary[] = [
  { id: 'proj-chats', name: 'Chats', path: '', kind: 'chats', isExpanded: false, threadCount: 1 },
  { id: 'proj-1', name: 'mcode', path: '/repo/mcode', isExpanded: true, threadCount: 3 }
];

const harness = createHookHarness();

try {
  // 3a. ProjectContextSheet：全部项目切换条 + 标准文档（含未创建）+ 项目文档 + 独立抽屉 SheetFrame
  const listResult: RemoteContextListDocsResult = {
    projectID: 'proj-chats',
    docs: [
      { name: 'overview', title: '项目概览', standard: true, exists: true, size: 120 },
      { name: 'decisions', title: '架构决策', standard: true, exists: false, size: 0 },
      { name: 'docs/guide.md', title: '开发指南', standard: false, exists: true, size: 80 }
    ]
  };
  const readResult: RemoteContextReadDocResult = {
    projectID: 'proj-chats',
    name: 'overview',
    title: '项目概览',
    standard: true,
    exists: true,
    content: '# 项目概览\n\n已加载正文内容',
    size: 4096,
    truncated: true
  };

  harness.reset();
  harness.setContext({
    projects,
    connectionState: 'connected',
    callRemote: async (method: string) => {
      if (method === 'context.listDocs') return listResult;
      if (method === 'context.readDoc') return readResult;
      return {};
    },
    supportsMethod: (method: string) => method === 'context.listDocs' || method === 'context.readDoc',
    onRemoteNotification: () => () => {}
  });

  harness.renderMarkup(<ProjectContextSheet projectID={null} isOpen onClose={() => {}} />);
  await harness.flush();
  const listHtml = harness.renderMarkup(<ProjectContextSheet projectID={null} isOpen onClose={() => {}} />);

  assert.ok(listHtml.includes('sheet-layer'), '独立 ProjectContextSheet 应渲染 SheetFrame 遮罩');
  assert.ok(listHtml.includes('role="toolbar"') && listHtml.includes('aria-label="选择项目查看文档"'), '全部项目应渲染项目切换工具栏');
  assert.ok(listHtml.includes('aria-pressed="true"') && listHtml.includes('Chats'), '默认应选中首个可选项目');
  assert.ok(listHtml.includes('标准文档') && listHtml.includes('项目文档'), '应区分标准文档与项目文档分组');
  assert.ok(listHtml.includes('未创建'), '缺失的标准文档应展示「未创建」标签');
  assert.ok(listHtml.includes('aria-label="搜索项目文档"') && listHtml.includes('text-base'), '应包含 16px 搜索输入框');
  assert.ok(listHtml.includes('aria-label="刷新项目文档"'), '应包含刷新按钮');

  // 3b. ProjectContextSheet 空态：无项目时显示 .empty-state
  harness.reset();
  harness.setContext({
    projects: [],
    connectionState: 'connected',
    callRemote: async () => ({ projectID: '', docs: [] }),
    supportsMethod: () => true,
    onRemoteNotification: () => () => {}
  });
  const noProjectHtml = harness.renderMarkup(<ProjectContextSheet projectID={null} embedded />);
  assert.ok(noProjectHtml.includes('empty-state') && noProjectHtml.includes('暂无可用项目'), '无项目时应展示 empty-state');
  assert.ok(!noProjectHtml.includes('sheet-layer'), 'embedded 模式不应包裹外层 sheet-layer');

  // 3c. LiveLogsPanel：流筛选胶囊、流标题分隔、等宽输出、截断提示与无障碍属性
  const logsSnapshot: RemoteLogsSubscribeResult = {
    threadID: 'thread-1',
    sequence: 2,
    streams: [
      {
        streamID: 's-cmd',
        source: 'tool',
        title: 'go test ./...',
        status: 'running',
        startedAt: '2026-11-21T00:00:00Z'
      },
      {
        streamID: 's-script',
        source: 'script',
        title: 'npm run build',
        status: 'done',
        startedAt: '2026-11-21T00:00:01Z'
      }
    ],
    chunks: [
      { seq: 1, streamID: 's-cmd', source: 'tool', text: 'ok internal/ui\n', ts: '2026-11-21T00:00:00Z' },
      { seq: 2, streamID: 's-script', source: 'script', text: 'built dist/\n', ts: '2026-11-21T00:00:02Z' }
    ],
    truncated: true
  };

  harness.reset();
  harness.setContext({
    connectionState: 'connected',
    callRemote: async (method: string) => {
      if (method === 'logs.subscribe') return logsSnapshot;
      return { unsubscribed: true };
    },
    supportsMethod: (method: string) => method === 'logs.subscribe' || method === 'logs.unsubscribe',
    onRemoteNotification: () => () => {}
  });

  harness.renderMarkup(<LiveLogsPanel threadID="thread-1" embedded />);
  await harness.flush();
  const logsHtml = harness.renderMarkup(<LiveLogsPanel threadID="thread-1" embedded />);

  assert.ok(logsHtml.includes('role="toolbar"') && logsHtml.includes('aria-label="日志流筛选"'), 'LiveLogsPanel 应渲染日志流筛选工具栏');
  assert.ok(logsHtml.includes('全部') && logsHtml.includes('go test ./...') && logsHtml.includes('npm run build'), 'LiveLogsPanel 应渲染「全部」及各流胶囊');
  assert.ok(logsHtml.includes('命令') && logsHtml.includes('脚本'), 'LiveLogsPanel 应标注命令与脚本来源');
  assert.ok(logsHtml.includes('运行中') && logsHtml.includes('已完成'), 'LiveLogsPanel 应渲染流状态');
  assert.ok(logsHtml.includes('ok internal/ui') && logsHtml.includes('built dist/'), 'LiveLogsPanel 应渲染终端输出文本');
  assert.ok(logsHtml.includes('role="log"') && logsHtml.includes('font-mono text-mc-code'), 'LiveLogsPanel 输出区应带 role="log" 与等宽类名');
  assert.ok(logsHtml.includes('早期日志已截断'), 'LiveLogsPanel truncated=true 时应展示截断提示');
  assert.ok(logsHtml.includes('aria-label="复制当前日志"') && logsHtml.includes('aria-label="清空日志视图"'), 'LiveLogsPanel 应包含复制与清空按钮');

  // 3d. LiveLogsPanel 空态与不支持能力降级
  harness.reset();
  harness.setContext({
    connectionState: 'connected',
    callRemote: async () => ({ threadID: 'thread-2', sequence: 0, streams: [], chunks: [], truncated: false }),
    supportsMethod: () => false,
    onRemoteNotification: () => () => {}
  });
  const unsupportedLogsHtml = harness.renderMarkup(<LiveLogsPanel threadID="thread-2" embedded />);
  assert.ok(unsupportedLogsHtml.includes('empty-state') && unsupportedLogsHtml.includes('当前 Mac 暂不支持实时日志'), '能力不支持时 LiveLogsPanel 应安全降级提示');
} finally {
  harness.restore();
}
