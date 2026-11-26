// 同 LazyFallback.test.tsx：web/ 没有测试运行器，用 react-dom/server 渲染成字符串后顶层断言。
// 这里校验耗时计算，以及派单胶囊的取名（无 name 时用匹配到的 bee 真名）与完成状态标记。
import { renderToStaticMarkup } from 'react-dom/server';
import type { RemoteSwarmBee, RemoteSwarmState } from '../types/swarm';
import type { SwarmEntryParsed, SwarmReportSection } from '../utils/markdown';
import { parseSwarmReport, swarmReportHeadline } from '../utils/markdown';
import {
  SwarmDispatchCapsules,
  SwarmReportCapsules,
  beeDurationMs,
  dispatchEntryKey,
  swarmReportKey
} from './SwarmCapsules';

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

// 报告键区分同名任务，并在重排时保持稳定。
const reports = [
  { name: 'Aspen', task: '任务一', status: '已完成', body: '报告一' },
  { name: 'Aspen', task: '任务二', status: '已完成', body: '报告二' }
];
if (swarmReportKey(reports[0], 0) === swarmReportKey(reports[1], 0)) {
  throw new Error('同名不同任务的报告不应使用相同的 key');
}
if (swarmReportKey(reports[0], 0) !== swarmReportKey(reports[0], 1)) {
  throw new Error('同一份报告的 key 不应随位置变化');
}
if (swarmReportKey({ name: '', task: '', status: '', body: '' }, 1) !== 'index:1') {
  throw new Error('无名称和任务的报告应使用位置作为 key');
}

function assertHeadline(body: string, expected: string): void {
  const actual = swarmReportHeadline(body);
  if (actual !== expected) {
    throw new Error(`期望结论首行为 “${expected}”，实际为 “${actual}”`);
  }
}

// swarmReportHeadline 从 `- 汇报总结：` 后提取 `## 结论` 首行，并反复去掉 Markdown 前缀。
assertHeadline(
  '- 合并结果：已合入\n- 改动文件：[]\n- 汇报总结：## 结论\n已完成调研：通知轮次仅促派发未要求转述。\n最小改动落在 ...\n## 详情\n详细说明',
  '已完成调研：通知轮次仅促派发未要求转述。'
);
assertHeadline(
  '- 汇报总结：\r\n  ## 结论  \r\n1. ### 修复了 `splitSwarmReport` 的 **边界**\r\n\r\n## 详情\r\n详细内容',
  '修复了 splitSwarmReport 的 边界'
);
assertHeadline('- 汇报总结：## 结论\n\n- **x**\n第二行', 'x');
assertHeadline('- 汇报总结: 普通总结，无结论段落', '');
assertHeadline('- 汇报总结：## 结论\n\n## 详情\n只有详情', '');

const reportWithConclusion: SwarmReportSection = {
  name: 'Aria',
  task: '调研通知轮次',
  status: '已完成',
  body: '- 合并结果：已合入\n- 改动文件：[]\n- 汇报总结：## 结论\n已完成调研：通知轮次仅促派发未要求转述。\n## 详情\n详情正文'
};
const reportWithoutConclusion: SwarmReportSection = {
  name: 'Aria',
  task: '调研通知轮次',
  status: '已完成',
  body: '- 合并结果：已合入\n- 汇报总结：已核对完成。'
};
const failedReport: SwarmReportSection = {
  name: 'Aria',
  task: '调研通知轮次',
  status: '失败',
  body: '- 汇报总结：## 结论\n失败时的结论不应覆盖状态文案'
};

// (a) 汇报有 `## 结论` 时，折叠态文案是结论首行。
const reportWithHeadlineHtml = renderToStaticMarkup(<SwarmReportCapsules reports={[reportWithConclusion]} />);
assertIncludes(reportWithHeadlineHtml, '已完成调研：通知轮次仅促派发未要求转述。');
assertExcludes(reportWithHeadlineHtml, 'Aria 已完成工作');

// (b) 已完成但没有结论段时，文案是「名字 已完成工作」。
const reportNoHeadlineHtml = renderToStaticMarkup(<SwarmReportCapsules reports={[reportWithoutConclusion]} />);
assertIncludes(reportNoHeadlineHtml, 'Aria 已完成工作');

// (c) 失败汇报文案保持「名字 失败」。
const failedReportHtml = renderToStaticMarkup(<SwarmReportCapsules reports={[failedReport]} />);
assertIncludes(failedReportHtml, 'Aria 失败');
assertExcludes(failedReportHtml, '失败时的结论不应覆盖状态文案');

// (d) SwarmDispatchCapsules 有已完成且带结论的汇报时显示结论首行，而不是任务预览。
const longTaskEntry: SwarmEntryParsed = {
  name: 'Aria',
  task: '这是一段超过二十个字符的派单任务描述用于测试预览'
};
const dispatchWithHeadlineHtml = renderToStaticMarkup(
  <SwarmDispatchCapsules
    entries={[longTaskEntry]}
    messageID="msg-1"
    reportIndex={{
      reportsByEntry: { [dispatchEntryKey('msg-1', 0)]: [reportWithConclusion] },
      consumedReports: {}
    }}
  />
);
assertIncludes(dispatchWithHeadlineHtml, '已完成调研：通知轮次仅促派发未要求转述。');
assertExcludes(dispatchWithHeadlineHtml, '这是一段超过二十个字符的派单任务描述用于…');

// 未带结论或非已完成状态时，SwarmDispatchCapsules 仍显示任务预览。
const dispatchFallbackHtml = renderToStaticMarkup(
  <SwarmDispatchCapsules
    entries={[longTaskEntry]}
    messageID="msg-1"
    reportIndex={{
      reportsByEntry: { [dispatchEntryKey('msg-1', 0)]: [failedReport] },
      consumedReports: {}
    }}
  />
);
assertIncludes(dispatchFallbackHtml, '这是一段超过二十个字符的派单任务描述用于…');
assertExcludes(dispatchFallbackHtml, '失败时的结论不应覆盖状态文案');

// parseSwarmReport 只按带元数据行的 bee 标题切段，保留正文里的 `## 详情` 等二级标题。
const modernReport = [
  '[编排] 子代理汇报：Aria, Bram 已结束工作。',
  '',
  '## Aria：调研 xxx (已完成)',
  '- 合并结果：已合入',
  '- 改动文件：[]',
  '- 汇报总结：## 结论',
  '结论第一行',
  '',
  '## 详情',
  '1. 第一条详情',
  '',
  '## Bram：调研 yyy (已完成)',
  '- 合并结果：已合入',
  '- 汇报总结：## 结论',
  '第二位子代理结论',
  '',
  '## 详情',
  '1. 第二位详情'
].join('\n');
const parsedModern = parseSwarmReport(modernReport);
if (!parsedModern || parsedModern.length !== 2) {
  throw new Error(`期望解析出 2 段，实际为 ${parsedModern ? parsedModern.length : 'null'}`);
}
if (parsedModern[0].name !== 'Aria' || parsedModern[1].name !== 'Bram') {
  throw new Error(`期望段名为 Aria 和 Bram，实际为 ${parsedModern.map((item) => item.name).join(', ')}`);
}
assertIncludes(parsedModern[0].body, '## 详情');
assertHeadline(parsedModern[0].body, '结论第一行');

// 无任何元数据行的旧格式汇报，仍按所有 `## ` 标题切段。
const legacyReport = [
  '[Hydra] 汇报',
  '',
  '## Aria：旧任务一 (已完成)',
  '旧格式正文一',
  '',
  '## Bram：旧任务二 (已完成)',
  '旧格式正文二'
].join('\n');
const parsedLegacy = parseSwarmReport(legacyReport);
if (!parsedLegacy || parsedLegacy.length !== 2) {
  throw new Error(`期望旧格式解析出 2 段，实际为 ${parsedLegacy ? parsedLegacy.length : 'null'}`);
}
if (parsedLegacy[0].name !== 'Aria' || parsedLegacy[1].name !== 'Bram') {
  throw new Error(`期望旧格式段名为 Aria 和 Bram，实际为 ${parsedLegacy.map((item) => item.name).join(', ')}`);
}
