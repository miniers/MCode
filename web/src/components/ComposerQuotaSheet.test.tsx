// 同 ComposerQuotaBar.test.tsx：web/ 没有测试运行器，顶层直接断言，失败时 throw。
// 额度配色按用量分档：<50 绿、<80 黄、<95 橙、其余红；百分比文字在低用量时用弱化色。
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import type { RemoteQuotaBudget } from '../types/quota';
import {
  formatCompactReset,
  quotaWindowCompactStatus,
  quotaWindowHoverDetail
} from '../utils/quotaPace';
import {
  ComposerQuotaSheet,
  ComposerQuotaSheetContent,
  agyAccountChoices,
  agyAccountSelectable,
  agyLeadAccountLocked,
  usageColor,
  usagePercentColor
} from './ComposerQuotaSheet';

function assertEqual<T>(actual: T, expected: T, label: string): void {
  if (actual !== expected) {
    throw new Error(`${label} 期望 “${String(expected)}”，实际为 “${String(actual)}”`);
  }
}

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

function collectButtons(
  node: React.ReactNode,
  out: Array<React.ReactElement<React.ButtonHTMLAttributes<HTMLButtonElement> & { children?: React.ReactNode }>> = []
): Array<React.ReactElement<React.ButtonHTMLAttributes<HTMLButtonElement> & { children?: React.ReactNode }>> {
  if (Array.isArray(node)) {
    node.forEach((child) => collectButtons(child, out));
    return out;
  }
  if (!React.isValidElement(node)) return out;
  const element = node as React.ReactElement<{ children?: React.ReactNode }>;
  if (element.type === 'button') {
    out.push(element as React.ReactElement<React.ButtonHTMLAttributes<HTMLButtonElement> & { children?: React.ReactNode }>);
  }
  collectButtons(element.props.children, out);
  return out;
}

function buttonText(node: React.ReactNode): string {
  if (typeof node === 'string' || typeof node === 'number') return String(node);
  if (Array.isArray(node)) return node.map(buttonText).join('');
  if (React.isValidElement(node)) {
    const el = node as React.ReactElement<{ children?: React.ReactNode }>;
    return buttonText(el.props.children);
  }
  return '';
}

assertEqual(usageColor(0), 'var(--quota-low)', 'usageColor(0)');
assertEqual(usageColor(49.9), 'var(--quota-low)', 'usageColor(49.9)');
assertEqual(usageColor(50), 'var(--quota-mid)', 'usageColor(50)');
assertEqual(usageColor(80), 'var(--quota-high)', 'usageColor(80)');
assertEqual(usageColor(95), 'var(--quota-critical)', 'usageColor(95)');
assertEqual(usageColor(100), 'var(--quota-critical)', 'usageColor(100)');

assertEqual(usagePercentColor(30), 'var(--foreground-muted)', 'usagePercentColor(30)');
assertEqual(usagePercentColor(74), 'var(--quota-mid)', 'usagePercentColor(74)');

// ---- 短词格式：XhYm 重置、周X重置、M/D 重置、用尽日、完整详情 ----
const MINUTE_MS = 60_000;
const HOUR_MS = 60 * MINUTE_MS;
const DAY_MS = 24 * HOUR_MS;
// 2026-12-07 10:00:00 本地时间（周一）
const LOCAL_NOW = new Date(2026, 11, 7, 10, 0, 0, 0).getTime();

assertEqual(
  formatCompactReset(new Date(LOCAL_NOW + 1 * HOUR_MS + 12 * MINUTE_MS).toISOString(), LOCAL_NOW),
  '1h12m 重置',
  'formatCompactReset(1h12m)'
);
assertEqual(
  formatCompactReset(new Date(LOCAL_NOW + 2 * HOUR_MS).toISOString(), LOCAL_NOW),
  '2h 重置',
  'formatCompactReset(2h)'
);
assertEqual(
  formatCompactReset(new Date(LOCAL_NOW + 45 * MINUTE_MS).toISOString(), LOCAL_NOW),
  '45m 重置',
  'formatCompactReset(45m)'
);
assertEqual(
  formatCompactReset(new Date(LOCAL_NOW + 11 * DAY_MS).toISOString(), LOCAL_NOW),
  '12/18 重置',
  'formatCompactReset(12/18)'
);
assertEqual(
  formatCompactReset(new Date(LOCAL_NOW + 3 * DAY_MS).toISOString(), LOCAL_NOW),
  '周四重置',
  'formatCompactReset(周四)'
);

// 7 天窗口：已用 38%，已过 1.75 天（25%），按配速约 2.86 天后（周四）用尽，周六重置
const earlyWeekResetsAt = new Date(LOCAL_NOW + 5.25 * DAY_MS).toISOString();
const earlyWeekWin = {
  period: '7d',
  title: '周',
  usedPercent: 38,
  resetsAt: earlyWeekResetsAt
};
const earlyCompact = quotaWindowCompactStatus(earlyWeekWin, '周', 7 * 24 * 60, LOCAL_NOW);
assertEqual(earlyCompact.text, '周四用尽', 'quotaWindowCompactStatus(早用尽周窗口)');
assertEqual(earlyCompact.tone, 'warn', 'quotaWindowCompactStatus(早用尽色调)');

const earlyDetail = quotaWindowHoverDetail(earlyWeekWin, '周', 7 * 24 * 60, LOCAL_NOW);
assertEqual(earlyDetail.left, '周 38%', 'quotaWindowHoverDetail.left');
assertIncludes(earlyDetail.right, '周四用尽 · ');
assertIncludes(earlyDetail.right, '重置');
assertEqual(earlyDetail.tone, 'warn', 'quotaWindowHoverDetail.tone');

// 已用尽 + 1h12m 重置
const exhaustedStatus = quotaWindowCompactStatus(
  { period: '5h', title: '5h', usedPercent: 100, resetsAt: new Date(LOCAL_NOW + 72 * MINUTE_MS).toISOString() },
  '5h',
  5 * 60,
  LOCAL_NOW
);
assertEqual(exhaustedStatus.text, '1h12m 重置', '已用尽显示重置短词');
assertEqual(exhaustedStatus.tone, 'danger', '已用尽色调为 danger');

function makeAgyBudget(
  accountID: string,
  account: string,
  windows: RemoteQuotaBudget['windows'],
  overrides: Partial<RemoteQuotaBudget> = {}
): RemoteQuotaBudget {
  return {
    provider: 'agy',
    providerName: 'Antigravity',
    role: 'lead',
    profile: null,
    state: 'used',
    windows,
    amount: null,
    readAt: null,
    account,
    accountID,
    ...overrides
  };
}

// ---- agyAccountChoices：过滤、按 accountID 去重保序、全部 disabled / 5h 满禁用 ----
const choiceBudgets: RemoteQuotaBudget[] = [
  {
    provider: 'codex',
    providerName: 'Codex',
    role: 'lead',
    profile: null,
    state: 'used',
    windows: [{ period: '5h', title: '5h', usedPercent: 10, resetsAt: null }],
    amount: null,
    readAt: null,
    account: 'codex@a.com',
    accountID: 'codex-1'
  },
  makeAgyBudget('', '无 ID 账号', [{ period: '5h', title: '5h', usedPercent: 10, resetsAt: null }]),
  makeAgyBudget('acc-1', '个人账号', [{ id: 'Gemini', period: '5h', title: 'Gemini', usedPercent: 25, resetsAt: null }], { active: true }),
  makeAgyBudget('acc-2', '工作账号', [{ id: '5h', period: null, title: '5h', usedPercent: 100, resetsAt: null }]),
  makeAgyBudget('acc-1', '个人账号重复', [{ id: 'Claude 7d', period: '7d', title: '7d', usedPercent: 100, resetsAt: null }]),
  makeAgyBudget('acc-3', '已停用账号', [
    { id: 'Gemini', period: '5h', title: '5h', usedPercent: 15, resetsAt: null, disabled: true },
    { id: 'Claude 7d', period: '7d', title: '7d', usedPercent: 20, resetsAt: null, disabled: true }
  ])
];

const choices = agyAccountChoices(choiceBudgets);
assertEqual(choices.length, 3, 'agyAccountChoices 去重数量');
assertEqual(choices[0].accountID, 'acc-1', 'choices[0].accountID');
assertEqual(choices[0].account, '个人账号', 'choices[0].account');
assertEqual(choices[0].disabled, false, 'choices[0].disabled（仅 7d 满不禁用）');
assertEqual(choices[0].anyWindowFull, true, 'choices[0].anyWindowFull（7d 满为 true）');
assertEqual(choices[1].accountID, 'acc-2', 'choices[1].accountID');
assertEqual(choices[1].disabled, true, 'choices[1].disabled（5h 满禁用）');
assertEqual(choices[1].anyWindowFull, true, 'choices[1].anyWindowFull（5h 满为 true）');
assertEqual(choices[2].accountID, 'acc-3', 'choices[2].accountID');
assertEqual(choices[2].disabled, true, 'choices[2].disabled（窗口全部 disabled）');
assertEqual(choices[2].anyWindowFull, false, 'choices[2].anyWindowFull（未满为 false）');

// ---- agyAccountSelectable：需 hasThread 且至少 2 个窗口未全部 disabled 的不同账号 ----
assertEqual(agyAccountSelectable(choiceBudgets, false), false, '无 thread 时不可选');
assertEqual(agyAccountSelectable(choiceBudgets, true), true, 'acc-1 与 acc-2 窗口未全 disabled 时可选');
assertEqual(
  agyAccountSelectable(
    [
      makeAgyBudget('acc-1', '个人账号', [{ period: '5h', title: '5h', usedPercent: 10, resetsAt: null }]),
      makeAgyBudget('acc-3', '已停用账号', [{ period: '5h', title: '5h', usedPercent: 10, resetsAt: null, disabled: true }])
    ],
    true
  ),
  false,
  '仅 1 个未全 disabled 账号时不可选'
);

// ---- agyLeadAccountLocked ----
const healthyChoice = agyAccountChoices([
  makeAgyBudget('acc-ok', '正常账号', [
    { id: 'Gemini', period: '5h', title: '5h', usedPercent: 20, resetsAt: null },
    { id: 'Claude 7d', period: '7d', title: '7d', usedPercent: 40, resetsAt: null }
  ])
]);
const lockChoices = [...choices, ...healthyChoice];

assertEqual(
  agyLeadAccountLocked({ isRunning: true, hasMessages: false, currentAccountID: '', choices: lockChoices }),
  true,
  '运行中且无消息锁定'
);
assertEqual(
  agyLeadAccountLocked({ isRunning: true, hasMessages: true, currentAccountID: 'acc-2', choices: lockChoices }),
  true,
  '运行中且指定账号 5h 满额仍锁定'
);
assertEqual(
  agyLeadAccountLocked({ isRunning: false, hasMessages: false, currentAccountID: 'acc-ok', choices: lockChoices }),
  false,
  '无消息且未运行解锁'
);
assertEqual(
  agyLeadAccountLocked({ isRunning: false, hasMessages: true, currentAccountID: '', choices: lockChoices }),
  true,
  '有消息且自动模式锁定'
);
assertEqual(
  agyLeadAccountLocked({ isRunning: false, hasMessages: true, currentAccountID: 'acc-ok', choices: lockChoices }),
  true,
  '有消息且指定账号可用时锁定'
);
assertEqual(
  agyLeadAccountLocked({ isRunning: false, hasMessages: true, currentAccountID: 'missing', choices: lockChoices }),
  false,
  '有消息且指定账号不在列表时解锁'
);
assertEqual(
  agyLeadAccountLocked({ isRunning: false, hasMessages: true, currentAccountID: 'acc-3', choices: lockChoices }),
  false,
  '有消息且指定账号全停用时解锁'
);
assertEqual(
  agyLeadAccountLocked({ isRunning: false, hasMessages: true, currentAccountID: 'acc-2', choices: lockChoices }),
  false,
  '有消息且指定账号 5h 满额时解锁'
);
assertEqual(
  agyLeadAccountLocked({ isRunning: false, hasMessages: true, currentAccountID: 'acc-1', choices: lockChoices }),
  false,
  '有消息且指定账号仅 7d 满额时解锁'
);

// ---- 顶栏「自动选号 | 指定」、图标刷新按钮、草稿只读、禁止嵌套 button ----
const selectableHtml = renderToStaticMarkup(
  <ComposerQuotaSheet
    isOpen
    onClose={() => {}}
    provider="agy"
    role="lead"
    budgets={choiceBudgets}
    hasThread
    selectedAccountID=""
    isRunning={false}
    hasMessages={false}
    onRefresh={() => {}}
  />
);
assertIncludes(selectableHtml, 'role="radiogroup"');
assertIncludes(selectableHtml, 'role="radio"');
assertIncludes(selectableHtml, '>自动选号<');
assertIncludes(selectableHtml, '>指定<');
assertIncludes(selectableHtml, '>在用<');
assertIncludes(selectableHtml, 'aria-label="刷新额度"');
assertExcludes(selectableHtml, '>刷新<');
assertExcludes(selectableHtml, 'animate-spin');
assertExcludes(selectableHtml, '点击切换已用/剩余');
assertEqual(/<button\b(?:(?!(<\/button>)).)*<button\b/.test(selectableHtml), false, '不得嵌套 button');

const refreshingHtml = renderToStaticMarkup(
  <ComposerQuotaSheet
    isOpen
    onClose={() => {}}
    provider="agy"
    role="lead"
    budgets={choiceBudgets}
    hasThread
    refreshing
    onRefresh={() => {}}
  />
);
assertIncludes(refreshingHtml, 'animate-spin');
assertIncludes(refreshingHtml, 'motion-reduce:animate-none');

const draftHtml = renderToStaticMarkup(
  <ComposerQuotaSheet
    isOpen
    onClose={() => {}}
    provider="agy"
    role="lead"
    budgets={choiceBudgets}
    hasThread={false}
    onRefresh={() => {}}
  />
);
assertExcludes(draftHtml, 'role="radiogroup"');
assertExcludes(draftHtml, '自动选号');

const radioButtons = (html: string): string[] => html.match(/<button[^>]*role="radio"[^>]*>/g) ?? [];

// lead 运行中：全部 radio 按钮与分段按钮均禁用
const leadRunningHtml = renderToStaticMarkup(
  <ComposerQuotaSheet
    isOpen
    onClose={() => {}}
    provider="agy"
    role="lead"
    budgets={choiceBudgets}
    hasThread
    selectedAccountID=""
    isRunning
    hasMessages={false}
    onRefresh={() => {}}
  />
);
const leadRunningRadios = radioButtons(leadRunningHtml);
assertEqual(leadRunningRadios.length, 3, 'lead 运行中 radio 数量');
for (const button of leadRunningRadios) {
  assertIncludes(button, 'disabled=""');
}

// lead 未运行、有消息、自动模式：全部 radio 禁用，「自动选号」保持 aria-pressed="true"
const leadAutoHasMessagesHtml = renderToStaticMarkup(
  <ComposerQuotaSheet
    isOpen
    onClose={() => {}}
    provider="agy"
    role="lead"
    budgets={choiceBudgets}
    hasThread
    selectedAccountID=""
    isRunning={false}
    hasMessages
    onRefresh={() => {}}
  />
);
const leadAutoHasMessagesRadios = radioButtons(leadAutoHasMessagesHtml);
assertEqual(leadAutoHasMessagesRadios.length, 3, 'lead 有消息自动模式 radio 数量');
for (const button of leadAutoHasMessagesRadios) {
  assertIncludes(button, 'disabled=""');
}
assertIncludes(leadAutoHasMessagesHtml, 'aria-pressed="true"');

// bees 运行中且有消息：可用账号 radio 未禁用，5h 满额/停用账号仍禁用
const beesBudgets: RemoteQuotaBudget[] = [
  makeAgyBudget('acc-ok', '正常账号', [{ id: 'Gemini', period: '5h', title: '5h', usedPercent: 20, resetsAt: null }], { role: 'bees' }),
  makeAgyBudget('acc-2', '工作账号', [{ id: '5h', period: null, title: '5h', usedPercent: 100, resetsAt: null }], { role: 'bees' }),
  makeAgyBudget(
    'acc-3',
    '已停用账号',
    [{ id: 'Gemini', period: '5h', title: '5h', usedPercent: 15, resetsAt: null, disabled: true }],
    { role: 'bees' }
  )
];
const beesRunningHtml = renderToStaticMarkup(
  <ComposerQuotaSheet
    isOpen
    onClose={() => {}}
    provider="agy"
    role="bees"
    budgets={beesBudgets}
    hasThread
    selectedAccountID=""
    isRunning
    hasMessages
    onRefresh={() => {}}
  />
);
const beesRunningRadios = radioButtons(beesRunningHtml);
assertEqual(beesRunningRadios.length, 3, 'bees radio 数量');
assertExcludes(beesRunningRadios[0], 'disabled=""');
assertIncludes(beesRunningRadios[1], 'disabled=""');
assertIncludes(beesRunningRadios[2], 'disabled=""');

// ---- 「自动选号 | 指定」回调参数校验 ----
const switchBudgets: RemoteQuotaBudget[] = [
  makeAgyBudget('acc-idle', '空闲账号', [{ period: '5h', title: '5h', usedPercent: 10, resetsAt: null }], { active: false }),
  makeAgyBudget('acc-active', '在用账号', [{ period: '5h', title: '5h', usedPercent: 20, resetsAt: null }], { active: true })
];

const pickedIDs: string[] = [];
const autoModeButtons = collectButtons(
  ComposerQuotaSheetContent({
    onClose: () => {},
    provider: 'agy',
    role: 'lead',
    budgets: switchBudgets,
    hasThread: true,
    selectedAccountID: '',
    isRunning: false,
    hasMessages: false,
    onSelectAccount: (id) => pickedIDs.push(id),
    onRefresh: () => {},
    now: LOCAL_NOW,
    expandedRowKey: null,
    onToggleRow: () => {},
    noReadingsOpen: false,
    onToggleNoReadings: () => {}
  })
);
const manualBtn = autoModeButtons.find((btn) => buttonText(btn.props.children) === '指定');
if (!manualBtn) throw new Error('未找到「指定」分段按钮');
manualBtn.props.onClick?.({} as React.MouseEvent<HTMLButtonElement>);
assertEqual(pickedIDs.join(','), 'acc-active', '自动切指定时应选中当前在用账号');

// 无在用账号时，点「指定」选中第一个可选账号
const noActiveBudgets: RemoteQuotaBudget[] = [
  makeAgyBudget('acc-full', '满额账号', [{ period: '5h', title: '5h', usedPercent: 100, resetsAt: null }]),
  makeAgyBudget('acc-first-ok', '可用账号', [{ period: '5h', title: '5h', usedPercent: 12, resetsAt: null }])
];
const noActivePicked: string[] = [];
const noActiveButtons = collectButtons(
  ComposerQuotaSheetContent({
    onClose: () => {},
    provider: 'agy',
    role: 'lead',
    budgets: noActiveBudgets,
    hasThread: true,
    selectedAccountID: '',
    isRunning: false,
    hasMessages: false,
    onSelectAccount: (id) => noActivePicked.push(id),
    onRefresh: () => {},
    now: LOCAL_NOW,
    expandedRowKey: null,
    onToggleRow: () => {},
    noReadingsOpen: false,
    onToggleNoReadings: () => {}
  })
);
const noActiveManualBtn = noActiveButtons.find((btn) => buttonText(btn.props.children) === '指定');
noActiveManualBtn?.props.onClick?.({} as React.MouseEvent<HTMLButtonElement>);
assertEqual(noActivePicked.join(','), 'acc-first-ok', '无在用账号时指定应选中第一个可选账号');

// 指定切自动：传空串
const manualToAutoPicked: string[] = [];
const manualModeButtons = collectButtons(
  ComposerQuotaSheetContent({
    onClose: () => {},
    provider: 'agy',
    role: 'lead',
    budgets: switchBudgets,
    hasThread: true,
    selectedAccountID: 'acc-active',
    isRunning: false,
    hasMessages: false,
    onSelectAccount: (id) => manualToAutoPicked.push(id),
    onRefresh: () => {},
    now: LOCAL_NOW,
    expandedRowKey: null,
    onToggleRow: () => {},
    noReadingsOpen: false,
    onToggleNoReadings: () => {}
  })
);
const autoBtn = manualModeButtons.find((btn) => buttonText(btn.props.children) === '自动选号');
autoBtn?.props.onClick?.({} as React.MouseEvent<HTMLButtonElement>);
assertEqual(manualToAutoPicked.length, 1, '点自动选号回调次数');
assertEqual(manualToAutoPicked[0], '', '点自动选号应传入空串');

// ---- 单账号省略账号行 + 「N 项无读数」折叠 + 点按行展开详情 + 余额/不限量 ----
const codexSingleBudgets: RemoteQuotaBudget[] = [
  {
    provider: 'codex',
    providerName: 'Codex',
    role: 'lead',
    profile: null,
    state: 'used',
    account: 'miniersliu@gmail.com',
    accountID: 'codex-single',
    windows: [
      {
        period: '5h',
        title: '5h',
        usedPercent: 18,
        resetsAt: new Date(LOCAL_NOW + 3 * HOUR_MS + 36 * MINUTE_MS).toISOString(),
        plan: 'plus'
      },
      earlyWeekWin,
      {
        period: null,
        title: 'Image generation',
        label: 'Image generation',
        usedPercent: 0,
        resetsAt: null,
        text: true,
        display: 'Unknown'
      },
      {
        period: null,
        title: 'Credits',
        label: 'Credits',
        usedPercent: 0,
        resetsAt: null,
        balance: 12.5,
        unit: 'credits'
      },
      {
        period: null,
        title: 'Unlimited',
        label: '无限',
        usedPercent: 0,
        resetsAt: null,
        unlimited: true
      }
    ],
    amount: null,
    readAt: null
  }
];

const singleCollapsedHtml = renderToStaticMarkup(
  <ComposerQuotaSheet
    isOpen
    onClose={() => {}}
    provider="codex"
    role="lead"
    budgets={codexSingleBudgets}
    onRefresh={() => {}}
    now={LOCAL_NOW}
  />
);
assertIncludes(singleCollapsedHtml, '· plus');
assertExcludes(singleCollapsedHtml, 'miniersliu@gmail.com');
assertExcludes(singleCollapsedHtml, 'role="radiogroup"');
assertExcludes(singleCollapsedHtml, '>账号<');
assertIncludes(singleCollapsedHtml, '18%');
assertIncludes(singleCollapsedHtml, '3h36m 重置');
assertIncludes(singleCollapsedHtml, '38%');
assertIncludes(singleCollapsedHtml, '周四用尽');
assertIncludes(singleCollapsedHtml, '剩余 12.5 credits');
assertIncludes(singleCollapsedHtml, '不限量');
assertIncludes(singleCollapsedHtml, '1 项无读数');
assertIncludes(singleCollapsedHtml, 'aria-expanded="false"');

// 点按行展开详情 + 点按「1 项无读数」
const toggledRows: string[] = [];
let noReadingsToggles = 0;
const singleButtons = collectButtons(
  ComposerQuotaSheetContent({
    onClose: () => {},
    provider: 'codex',
    role: 'lead',
    budgets: codexSingleBudgets,
    onRefresh: () => {},
    now: LOCAL_NOW,
    expandedRowKey: null,
    onToggleRow: (key) => toggledRows.push(key),
    noReadingsOpen: false,
    onToggleNoReadings: () => {
      noReadingsToggles += 1;
    }
  })
);
const weekRowBtn = singleButtons.find((btn) => buttonText(btn.props.children).includes('周四用尽'));
if (!weekRowBtn) throw new Error('未找到单账号周窗口行按钮');
weekRowBtn.props.onClick?.({} as React.MouseEvent<HTMLButtonElement>);
assertEqual(toggledRows.length, 1, '点按窗口行触发 onToggleRow');

const nrBtn = singleButtons.find((btn) => buttonText(btn.props.children).includes('1 项无读数'));
if (!nrBtn) throw new Error('未找到 1 项无读数折叠按钮');
nrBtn.props.onClick?.({} as React.MouseEvent<HTMLButtonElement>);
assertEqual(noReadingsToggles, 1, '点按无读数按钮触发 onToggleNoReadings');

const singleExpandedHtml = renderToStaticMarkup(
  <ComposerQuotaSheetContent
    onClose={() => {}}
    provider="codex"
    role="lead"
    budgets={codexSingleBudgets}
    onRefresh={() => {}}
    now={LOCAL_NOW}
    expandedRowKey={toggledRows[0]}
    onToggleRow={() => {}}
    noReadingsOpen
    onToggleNoReadings={() => {}}
  />
);
assertIncludes(singleExpandedHtml, 'aria-expanded="true"');
assertIncludes(singleExpandedHtml, 'grid-rows-[1fr] opacity-100');
assertIncludes(singleExpandedHtml, 'Image generation');
assertIncludes(singleExpandedHtml, 'Unknown');

// 多账号行展开详情
const multiRowToggled: string[] = [];
const multiButtons = collectButtons(
  ComposerQuotaSheetContent({
    onClose: () => {},
    provider: 'agy',
    role: 'lead',
    budgets: switchBudgets,
    hasThread: true,
    selectedAccountID: '',
    onRefresh: () => {},
    now: LOCAL_NOW,
    expandedRowKey: null,
    onToggleRow: (key) => multiRowToggled.push(key),
    noReadingsOpen: false,
    onToggleNoReadings: () => {}
  })
);
const activeAccountRowBtn = multiButtons.find(
  (btn) => btn.props.role !== 'radio' && buttonText(btn.props.children).includes('在用账号')
);
if (!activeAccountRowBtn) throw new Error('未找到多账号行展开按钮');
activeAccountRowBtn.props.onClick?.({} as React.MouseEvent<HTMLButtonElement>);
assertEqual(multiRowToggled[0], 'acc-active', '点按多账号行应以 accountID 为 key 展开');
