// web/ 目前没有测试运行器（package.json 里没有 vitest/jest，scripts/check.sh 只跑 tsc --noEmit），
// 所以这里不引入任何测试框架与 node 内置模块：用 react-dom/server 把列表渲染成字符串，
// 断言直接抛 Error。接入运行器后本文件即可直接执行，类型检查也不会牵连缺失的 @types/node。
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import type { RemoteProviderListWithCatalog } from '../context/remoteTypes';
import type { RemoteProvider, RemoteProviderModel } from '../types/contract';
import { buildPickerSections } from '../utils/modelIndex';
import { EffortTrack, ModelPickerPanel, ModelSummaryRow } from './ModelPicker';

function assertIncludes(haystack: string, needle: string): void {
  if (!haystack.includes(needle)) {
    throw new Error(`期望渲染结果包含 “${needle}”，实际为：${haystack}`);
  }
}

function assertExcludes(haystack: string, needle: string): void {
  if (haystack.includes(needle)) {
    throw new Error(`期望渲染结果不含 “${needle}”，实际为：${haystack}`);
  }
}

function assertEqual<T>(actual: T, expected: T, what: string): void {
  if (actual !== expected) {
    throw new Error(`期望${what}为 ${String(expected)}，实际为 ${String(actual)}`);
  }
}

// 没有 DOM 也没有运行器：直接调用无 hook 的组件拿到元素树，再按顺序取按钮的 onClick 触发。
function collectButtons(
  node: React.ReactNode,
  out: Array<React.ReactElement<{ onClick?: () => void }>> = []
): Array<React.ReactElement<{ onClick?: () => void }>> {
  if (Array.isArray(node)) {
    node.forEach((child) => collectButtons(child, out));
    return out;
  }
  if (!React.isValidElement(node)) return out;
  const element = node as React.ReactElement<{ children?: React.ReactNode }>;
  if (element.type === 'button') out.push(element as React.ReactElement<{ onClick?: () => void }>);
  collectButtons(element.props.children, out);
  return out;
}

function provider(overrides: Partial<RemoteProvider>): RemoteProvider {
  return {
    id: 'claude',
    name: 'Claude',
    auth: 'unknown',
    models: [],
    supportsPlanMode: false,
    supportsImages: false,
    supportsFast: false,
    available: true,
    unavailableReason: null,
    ...overrides
  };
}

const claudeNotInstalled = provider({ id: 'claude', name: 'Claude', unavailableReason: '未安装 Claude Code CLI' });
const codexSilentEmpty = provider({ id: 'codex', name: 'Codex' });

// ---- ModelPickerPanel ----

function model(overrides: Partial<RemoteProviderModel>): RemoteProviderModel {
  return {
    id: 'model',
    name: 'Model',
    detail: null,
    efforts: [],
    defaultEffort: null,
    isDefault: false,
    supportsFast: false,
    preferredEffort: null,
    ...overrides
  };
}

function providerList(list: RemoteProvider[]): RemoteProviderListWithCatalog {
  return [...list] as RemoteProviderListWithCatalog;
}

const opusAuto = model({ id: 'auto/claude-opus-5-5', name: 'Opus 5.5', contextLimit: 200000 });
const opusGroup = model({ id: 'group/claude-opus-5-5', name: 'Opus 5.5', contextLimit: 1000000 });
const claudePicker = provider({ id: 'claude', name: 'Claude', models: [opusAuto, opusGroup] });
const pickerSections = buildPickerSections({
  providers: providerList([claudePicker]),
  query: '',
  isProviderVisible: () => true
});

const panelHtml = renderToStaticMarkup(
  <ModelPickerPanel
    sections={pickerSections}
    query=""
    onQueryChange={() => {}}
    selectedKey={null}
    onSelect={() => {}}
  />
);

// 同名模型的副标题用模型 ID 区分；上下文窗口按千/百万取整。
assertIncludes(panelHtml, '>auto/claude-opus-5-5</span>');
assertIncludes(panelHtml, '>group/claude-opus-5-5</span>');
assertIncludes(panelHtml, '200K');
assertIncludes(panelHtml, '1M');

// selectedKey 命中的行带勾选图标（外层 aria-label 标出已选）。
const selectedHtml = renderToStaticMarkup(
  <ModelPickerPanel
    sections={pickerSections}
    query=""
    onQueryChange={() => {}}
    selectedKey="claude/group/claude-opus-5-5"
    onSelect={() => {}}
  />
);
assertIncludes(selectedHtml, 'aria-label="已选"');

// 搜索无命中：显示空结果文案。
const emptySections = buildPickerSections({
  providers: providerList([claudePicker]),
  query: '不存在的模型',
  isProviderVisible: () => true
});
const emptyHtml = renderToStaticMarkup(
  <ModelPickerPanel
    sections={emptySections}
    query="不存在的模型"
    onQueryChange={() => {}}
    selectedKey={null}
    onSelect={() => {}}
  />
);
assertIncludes(emptyHtml, '没有匹配的模型');

// 目录为空且有原因的 Agent：保留分组标题并显示原因；目录为空且无原因的整组不渲染。
const reasonSections = buildPickerSections({
  providers: providerList([claudeNotInstalled, codexSilentEmpty]),
  query: '',
  isProviderVisible: () => true
});
const reasonHtml = renderToStaticMarkup(
  <ModelPickerPanel
    sections={reasonSections}
    query=""
    onQueryChange={() => {}}
    selectedKey={null}
    onSelect={() => {}}
  />
);
assertIncludes(reasonHtml, 'Claude');
assertIncludes(reasonHtml, '未安装 Claude Code CLI');
assertExcludes(reasonHtml, 'Codex');

// EffortTrack 传 defaultLabel 时，最前面多一个“默认”档位。
const effortHtml = renderToStaticMarkup(
  <EffortTrack efforts={['low', 'high']} value="" onChange={() => {}} defaultLabel="默认" />
);
assertIncludes(effortHtml, 'aria-label="思考档位: 默认"');

// ---- ModelSummaryRow：摘要页的入口行 ----
const summaryRowHtml = renderToStaticMarkup(
  <ModelSummaryRow label="主代理" value="Opus 5.5" detail="high" onClick={() => {}} />
);
assertIncludes(summaryRowHtml, '>主代理<');
assertIncludes(summaryRowHtml, '>Opus 5.5<');
assertIncludes(summaryRowHtml, '>high<');
assertIncludes(summaryRowHtml, 'min-h-[44px]');

let summaryRowClicks = 0;
const summaryRowButtons = collectButtons(
  ModelSummaryRow({ label: '主代理', value: 'Opus 5.5', onClick: () => { summaryRowClicks += 1; } })
);
assertEqual(summaryRowButtons.length, 1, '入口行按钮数量');
summaryRowButtons[0]?.props.onClick?.();
assertEqual(summaryRowClicks, 1, '入口行点击次数');
