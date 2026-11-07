import type { ThreadMessage } from '../ThreadTimeline';
import { buildToolSegments } from './toolSegments';

function assertEqual<T>(actual: T, expected: T, label: string): void {
  if (actual !== expected) throw new Error(`${label}: 期望 ${String(expected)}，实际为 ${String(actual)}`);
}

const message = (id: string, role: string, at: string, extra: Partial<ThreadMessage> = {}): ThreadMessage => ({
  id, role, text: '', createdAt: at, turnIndex: 1, turnID: 'turn', ...extra
});

const tool = (id: string, at: string): ThreadMessage =>
  message(id, 'tool', at, { kind: 'tool', toolName: 'bash', toolStatus: 'completed' });
const thinking = (id: string, at: string): ThreadMessage => message(id, 'thinking', at, { kind: 'thinking' });
const assistant = (id: string, at: string): ThreadMessage => message(id, 'assistant', at, { kind: 'assistant', text: '正文' });

// (a) 只有一段连续活动行：不再折叠
const single = buildToolSegments([
  thinking('t1', '2026-11-06T08:00:01.000Z'),
  tool('c1', '2026-11-06T08:00:02.000Z'),
  tool('c2', '2026-11-06T08:00:03.000Z'),
]);
assertEqual(single.length, 1, '单段回合只返回一段');
assertEqual(single[0].collapsible, false, '单段回合不折叠');
assertEqual(single[0].entries.length, 3, '单段回合保留全部条目');
assertEqual(single[0].key, 't1', '单段回合的 key 不变');

// (b) 两段活动行中间夹一条非活动条目：两段都保持可折叠
const multi = buildToolSegments([
  tool('c1', '2026-11-06T08:00:01.000Z'),
  tool('c2', '2026-11-06T08:00:02.000Z'),
  assistant('a1', '2026-11-06T08:00:03.000Z'),
  tool('c3', '2026-11-06T08:00:04.000Z'),
  tool('c4', '2026-11-06T08:00:05.000Z'),
]);
assertEqual(multi.length, 3, '多段回合按连续活动行切分');
assertEqual(multi[0].collapsible, true, '第一段仍可折叠');
assertEqual(multi[1].collapsible, false, '中间正文条目单独展示');
assertEqual(multi[2].collapsible, true, '第二段仍可折叠');

// (c) 只有一条工具调用：原行为不变
const lone = buildToolSegments([tool('c1', '2026-11-06T08:00:01.000Z')]);
assertEqual(lone.length, 1, '单条工具调用只返回一段');
assertEqual(lone[0].collapsible, false, '单条工具调用不折叠');
