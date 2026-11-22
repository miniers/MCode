import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { ForwardedContextSection } from './ForwardedContextSection';

function assertIncludes(haystack: string, needle: string, label: string): void {
  if (!haystack.includes(needle)) {
    throw new Error(`${label}: expected HTML to include "${needle}", got: ${haystack}`);
  }
}

function assertExcludes(haystack: string, needle: string, label: string): void {
  if (haystack.includes(needle)) {
    throw new Error(`${label}: expected HTML not to include "${needle}", got: ${haystack}`);
  }
}

const sampleContent = [
  'User:',
  '第一轮提问（应折叠）',
  '',
  'Assistant:',
  '第一轮回答（应折叠）',
  '',
  'User:',
  '最后一条提问（默认展开）',
  '',
  'Assistant:',
  '最后一条回复（默认展开）'
].join('\n');

// 1. 转发会话到达开头：默认展示最后一轮，折叠更早 2 条，并显示来源分隔线
const fullHtml = renderToStaticMarkup(
  React.createElement(ForwardedContextSection, {
    threadID: 'th-new',
    forwardedContext: {
      sourceTitle: '原会话标题',
      content: sampleContent,
      truncated: false
    },
    forkedFromThreadID: 'th-origin',
    forkedFromTitle: '原会话标题',
    isContinuation: true,
    showHistory: true,
    onOpenOrigin: () => undefined
  })
);
assertIncludes(fullHtml, '更早 2 条', '折叠按钮显示更早条数');
assertExcludes(fullHtml, '第一轮提问（应折叠）', '默认折叠更早轮次');
assertIncludes(fullHtml, '最后一条提问（默认展开）', '默认展开最后一条 user');
assertIncludes(fullHtml, '最后一条回复（默认展开）', '默认展开最后一条 assistant');
assertIncludes(fullHtml, '继续自「原会话标题」', '底部渲染来源分隔线');

// 2. 原会话不在当前列表时，回退使用 forwardedContext.sourceTitle
const fallbackTitleHtml = renderToStaticMarkup(
  React.createElement(ForwardedContextSection, {
    threadID: 'th-new-2',
    forwardedContext: {
      sourceTitle: '历史来源标题',
      content: 'User:\n继续做完测试\n\nAssistant:\n好的。',
      truncated: false
    },
    forkedFromThreadID: 'th-deleted',
    forkedFromTitle: '',
    isContinuation: true,
    showHistory: true
  })
);
assertIncludes(fallbackTitleHtml, '继续自「历史来源标题」', '回退使用 sourceTitle');
assertExcludes(fallbackTitleHtml, '更早', '无更早轮次时不显示折叠按钮');

// 3. 尚未翻到会话开头（showHistory=false）时不渲染历史正文
const hiddenHistoryHtml = renderToStaticMarkup(
  React.createElement(ForwardedContextSection, {
    threadID: 'th-new-3',
    forwardedContext: {
      sourceTitle: '原会话标题',
      content: sampleContent,
      truncated: false
    },
    forkedFromThreadID: 'th-origin',
    forkedFromTitle: '原会话标题',
    isContinuation: true,
    showHistory: false
  })
);
assertExcludes(hiddenHistoryHtml, '最后一条提问（默认展开）', 'hasMore 时不渲染历史正文');
assertIncludes(hiddenHistoryHtml, '继续自「原会话标题」', '仍保留来源分隔线');
