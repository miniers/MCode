// @ts-expect-error web/ 未安装 @types/node，测试运行时由 node 提供 node:fs / node:assert
import assert from 'node:assert/strict';
// @ts-expect-error web/ 未安装 @types/node，测试运行时由 node 提供 node:fs
import { readFileSync } from 'node:fs';
import { parseRoute } from './app/route';
import { usesSplitLayout } from './utils/splitLayout';
import { routeDepth } from './hooks/mobileNavTransition';

function readText(relativePath: string): string {
  return readFileSync(new URL(relativePath, import.meta.url), 'utf8');
}

// 1. #/automations 路由解析与全主区布局（不走分栏侧边栏）
assert.deepEqual(parseRoute('#/automations'), { kind: 'automations' });
assert.equal(usesSplitLayout(true, 'automations'), false, '宽屏下 automations 应走全主区布局而非分栏侧边栏');
assert.equal(usesSplitLayout(false, 'automations'), false, '窄屏下 automations 不走分栏布局');
assert.equal(routeDepth({ kind: 'automations' }), 1, 'automations 路由层级应与 board 一致');

// 2. Header.tsx：顶栏一级「待处理」直达按钮（连接就绪挂载 useAttentionInbox、计数角标 99+）与「自动化」一级 Tab
const headerTsx = readText('./components/Header.tsx');
assert.match(headerTsx, /export\s+type\s+HomeView\s*=\s*['"]sessions['"]\s*\|\s*['"]board['"]\s*\|\s*['"]automations['"]/);
assert.ok(headerTsx.includes('aria-label="待处理事项"'), 'Header 顶栏应包含 aria-label="待处理事项" 一级按钮');
assert.ok(headerTsx.includes('setAttentionOpen(true)'), 'Header 顶栏「待处理」按钮应直接打开 AttentionSheet');
assert.ok(headerTsx.includes('useAttentionInbox(projectID)'), 'Header 应在连接就绪监测组件里挂载 useAttentionInbox');
assert.ok(headerTsx.includes("connectionState === 'connected' && isAttentionSupported"), 'Header 应在连接就绪时挂载待处理计数同步');
assert.ok(headerTsx.includes("attentionCount > 99 ? '99+' : attentionCount"), 'Header 顶栏待处理角标超过 99 应显示 99+');
assert.ok(headerTsx.includes('<AttentionSettingsRow unresolvedCount={attentionCount}'), '设置 → 总管页应复用顶栏已拉取的待处理计数');
assert.ok(headerTsx.includes("activeView === 'automations'") && headerTsx.includes('aria-label="自动化"'), 'Header 应包含一级「自动化」Tab');
assert.ok(headerTsx.includes('useProjectAutomations('), 'Header 应获取选中项目的自动化未读数');

// 3. App.tsx 与 ThreadList.tsx：#/automations 路由视图接入与重复状态清理
const appTsx = readText('./App.tsx');
assert.ok(appTsx.includes("route.kind === 'automations'"), 'App.tsx 应处理 automations 路由');
assert.ok(appTsx.includes("'#/automations'"), 'App.tsx 应支持切至 #/automations');
assert.ok(appTsx.includes('<AutomationPanel'), 'App.tsx 应在主区渲染内联自动化面板');

const threadListTsx = readText('./components/ThreadList.tsx');
assert.ok(!threadListTsx.includes('automationOpen'), 'ThreadList.tsx 不应再维护独立的 automationOpen 弹窗状态');

// 4. AutomationSheet.tsx：支持 inline 内联模式、selectedProjectID === null 时项目切换条，且不硬编码 85dvh 或原始 Emoji
const automationSheetTsx = readText('./components/AutomationSheet.tsx');
assert.ok(automationSheetTsx.includes('projectID: string | null'), 'AutomationSheet 应接受 projectID: string | null');
assert.ok(automationSheetTsx.includes('inline?: boolean'), 'AutomationSheet 应支持 inline 内联渲染模式');
assert.ok(automationSheetTsx.includes('export const AutomationPanel'), 'AutomationSheet 应导出内联包装组件 AutomationPanel');
assert.ok(automationSheetTsx.includes('projectID === null && availableProjects.length > 0'), 'AutomationSheet 应在全部项目下展示项目切换条');
assert.ok(automationSheetTsx.includes('暂无可用项目'), 'AutomationSheet 应在无项目时展示清晰空态引导');
assert.ok(automationSheetTsx.includes('--app-height'), 'AutomationSheet 弹窗模式应使用 --app-height');
assert.ok(!automationSheetTsx.includes('max-h-[85dvh]'), 'AutomationSheet 不应再硬编码 max-h-[85dvh]');
assert.ok(!automationSheetTsx.includes('🕒') && !automationSheetTsx.includes('📄'), 'AutomationSheet 不应包含原始 Emoji');

const attentionSheetTsx = readText('./components/AttentionSheet.tsx');
assert.ok(attentionSheetTsx.includes('--app-height'), 'AttentionSheet 应使用 --app-height');
assert.ok(!attentionSheetTsx.includes('max-h-[85dvh]'), 'AttentionSheet 不应再硬编码 max-h-[85dvh]');

// 5. KanbanBoard.tsx 与 index.css：宽屏 5 列多列网格、列头吸顶与列内独立滚动，窄屏保留单列 + 顶栏 375px 自适应
const kanbanTsx = readText('./components/KanbanBoard.tsx');
assert.ok(kanbanTsx.includes('home-list kanban-shell'), 'KanbanBoard 应包含 .home-list.kanban-shell');
assert.ok(kanbanTsx.includes('home-list__scroll kanban-scroll'), 'KanbanBoard 应包含 .home-list__scroll.kanban-scroll');
assert.ok(kanbanTsx.includes('kanban-columns'), 'KanbanBoard 应包含 .kanban-columns');
assert.ok(kanbanTsx.includes('kanban-column__header') && kanbanTsx.includes('kanban-column__body'), 'KanbanBoard 每列应拆分 .kanban-column__header 与 .kanban-column__body');

const indexCss = readText('./index.css');
assert.ok(indexCss.includes('--surface-subtle:'), 'index.css 应定义 --surface-subtle 语义变量');
const wideBlockMatch = /@media\s*\(min-width:\s*900px\)\s*\{(?:(?!@media)[\s\S])*?\.kanban-columns(?:(?!@media)[\s\S])*?\n\}/m.exec(indexCss);
assert.ok(wideBlockMatch !== null, 'index.css 应在 @media (min-width: 900px) 下定义宽屏看板样式');
const wideSection = wideBlockMatch?.[0] ?? '';
assert.ok(wideSection.includes('grid-template-columns: repeat(5, minmax(248px, 1fr))'), '宽屏看板应定义 5 列横向网格');
assert.ok(wideSection.includes('overflow-x: auto; overflow-y: hidden'), '.kanban-scroll 在宽屏应开启横向滚动并限制纵向整体滚动');
assert.ok(wideSection.includes('overflow-y: auto') && wideSection.includes('.kanban-column__body'), '.kanban-column__body 在宽屏应独立纵向滚动');
assert.ok(indexCss.includes('@media (max-width: 640px)') && indexCss.includes('minmax(0, 1fr) auto auto'), 'index.css 顶栏窄屏应使用 minmax(0, 1fr) auto auto 防止挤压');
assert.ok(indexCss.includes('@media (max-width: 480px)') && indexCss.includes('.view-switch--header .view-switch__item'), 'index.css 应在 <= 480px 将 3 个视图 Tab 折叠为纯图标');
