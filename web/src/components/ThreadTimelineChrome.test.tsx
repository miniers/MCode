import { renderToStaticMarkup } from 'react-dom/server';
import { RemoteProvider } from '../context/RemoteContext';
import { ThreadTimeline, TimelineEntry, type ThreadMessage } from './ThreadTimeline';

const renderMessage = (message: ThreadMessage) => renderToStaticMarkup(
  <TimelineEntry
    activity={{}} threadID="thread" item={{ type: 'message', message }} expanded={false}
    copyState={null} minuteTick={0} onToggleExpanded={() => {}} onToggleWorkRecord={() => {}}
    segmentOpenState={{}} onToggleToolSegment={() => {}} autoCollapse={false}
    onCopyMessage={() => {}} canBranch={false} highlighted={false} decisionAnswers={{}}
  />
);
const base = { createdAt: '2026-11-06T08:00:00.000Z', turnIndex: 42, text: '正文' };
const assistant = renderMessage({ ...base, id: 'assistant', role: 'assistant', kind: 'assistant', isFinalResponse: true });
const user = renderMessage({ ...base, id: 'user', role: 'user', kind: 'user' });
const error = renderMessage({ ...base, id: 'error', role: 'system', kind: 'error', errKind: 'api_error' });
for (const html of [assistant, user, error]) {
  if (html.includes('助手 · 答复') || />\s*#42(?:\s|<|·)/.test(html)) throw new Error('消息不应显示类型标签或轮次编号');
  if (!html.includes('title=')) throw new Error('消息应通过 title 提供时间');
}
if (!error.includes('接口错误') || !error.includes('<svg')) throw new Error('错误标题与图标应保留');

const thinking = { ...base, id: 'thinking', role: 'assistant' as const, kind: 'thinking' as const };
const renderWork = (running: boolean) => renderToStaticMarkup(
  <RemoteProvider><TimelineEntry
    activity={{}} threadID="thread" item={{ type: 'workRecord', turnID: 'turn', turnIndex: 42, entries: [thinking] }} expanded
    copyState={null} minuteTick={0} onToggleExpanded={() => {}} onToggleWorkRecord={() => {}}
    segmentOpenState={{}} onToggleToolSegment={() => {}} autoCollapse={false}
    onCopyMessage={() => {}} canBranch={false} highlighted={false} decisionAnswers={{}}
    isRunning={running} lastMessageID="thinking"
  /></RemoteProvider>
);
if (!renderWork(true).includes('text-shimmer')) throw new Error('流式思考行应显示扫光');
if (renderWork(false).includes('text-shimmer')) throw new Error('结束的思考行不应显示扫光');

const timeline = renderToStaticMarkup(
  <RemoteProvider><ThreadTimeline
    threadID="thread"
    items={[
      { type: 'message', message: { ...base, id: 'first', role: 'user', kind: 'user' } },
      { type: 'message', message: { ...base, id: 'last', role: 'user', kind: 'user' } }
    ]}
    expandedIds={{}} copyState={null} onToggleExpanded={() => {}} onToggleWorkRecord={() => {}}
    onCopyMessage={() => {}}
  /></RemoteProvider>
);
const actionRows = [...timeline.matchAll(/<div class="([^"]*justify-end gap-2 mt-1 px-1 min-w-0[^"]*)"/g)].map((match) => match[1]);
if (actionRows.length !== 2 || !actionRows[0].includes('group-hover:opacity-100') || actionRows[1].includes('group-hover:opacity-100')) {
  throw new Error('仅较早的用户消息操作行应在悬停时显示');
}
