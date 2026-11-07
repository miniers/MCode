// 同 LazyFallback.test.tsx：web/ 没有测试运行器，用 react-dom/server 渲染成字符串后直接断言，
// 断言失败直接抛错。这里只渲染记忆提议行的展示部分，不挂 RemoteProvider、不触发远程调用。
import { renderToStaticMarkup } from 'react-dom/server';
import { MemoryProposalSettingsRowView } from './MemoryProposalSettingsRowView';
import { SETTINGS_ITEMS } from './settingsIndex';

function assertIncludes(haystack: string, needle: string): void {
  if (!haystack.includes(needle)) {
    throw new Error(`期望渲染结果包含 “${needle}”，实际为：${haystack}`);
  }
}

function assertNotIncludes(haystack: string, needle: string): void {
  if (haystack.includes(needle)) {
    throw new Error(`期望渲染结果不含 “${needle}”，实际为：${haystack}`);
  }
}

// 搜索索引里必须存在这一行，id 也要和行上的 data-settings-item 对得上。
const entry = SETTINGS_ITEMS.find((item) => item.id === 'web-memory-proposals');
const entryDesc = entry?.desc;
if (!entry || !entryDesc) throw new Error('设置搜索索引缺少 web-memory-proposals 条目或说明');

const noop = () => {};
const pendingHtml = renderToStaticMarkup(
  <MemoryProposalSettingsRowView pendingCount={3} onOpen={noop} />
);
const emptyHtml = renderToStaticMarkup(
  <MemoryProposalSettingsRowView pendingCount={0} onOpen={noop} />
);
const overflowHtml = renderToStaticMarkup(
  <MemoryProposalSettingsRowView pendingCount={120} onOpen={noop} />
);

// 结构：复用设置页的小节卡片与统一行，不再是旧的 settings-section/settings-action。
assertIncludes(pendingHtml, 'settings-section-block');
assertIncludes(pendingHtml, 'settings-section-title');
assertIncludes(pendingHtml, 'settings-card');
assertIncludes(pendingHtml, 'settings-row');
assertIncludes(pendingHtml, 'settings-row-title');
assertIncludes(pendingHtml, 'settings-row-desc');
assertIncludes(pendingHtml, 'settings-row-control');
assertIncludes(pendingHtml, 'data-settings-item="web-memory-proposals"');
assertNotIncludes(pendingHtml, 'settings-action');
assertNotIncludes(pendingHtml, 'settings-label');
assertNotIncludes(pendingHtml, 'settings-note');

// 文案：小节标题、行标题、说明与搜索索引一致。
assertIncludes(pendingHtml, '记忆');
assertIncludes(pendingHtml, '记忆提议');
assertIncludes(pendingHtml, '打开');
assertIncludes(pendingHtml, 'secondary-button settings-inline-button');
assertIncludes(pendingHtml, entryDesc);

// 角标：0 不显示，超过 99 显示 99+，其余显示原始数量。
assertNotIncludes(emptyHtml, 'rounded-full');
assertIncludes(pendingHtml, 'rounded-full');
assertIncludes(pendingHtml, '>3</span>');
assertIncludes(overflowHtml, '99+');
