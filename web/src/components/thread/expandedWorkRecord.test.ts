// web/ 没有测试运行器，沿用同目录测试的写法：直接调用纯函数后断言。
import { collapseWorkRecordState, pickExpandedWorkRecordTurnID } from './expandedWorkRecord';

function assertEqual<T>(actual: T, expected: T, what: string): void {
  if (actual !== expected) {
    throw new Error(`期望${what}为 ${String(expected)}，实际为 ${String(actual)}`);
  }
}

const viewportBottom = 600;

assertEqual(
  pickExpandedWorkRecordTurnID([{ turnID: 't1', top: 400, bottom: 900 }], viewportBottom),
  't1',
  '跨过底边的展开记录'
);
assertEqual(
  pickExpandedWorkRecordTurnID([{ turnID: 't1', top: 100, bottom: 300 }], viewportBottom),
  null,
  '完全在视口内的展开记录'
);
assertEqual(
  pickExpandedWorkRecordTurnID([{ turnID: 't1', top: 700, bottom: 900 }], viewportBottom),
  null,
  '完全在视口下方的展开记录'
);
assertEqual(
  pickExpandedWorkRecordTurnID([{ turnID: 't1', top: 100, bottom: 592 }], viewportBottom),
  null,
  '底边未越过 8px 余量'
);
assertEqual(
  pickExpandedWorkRecordTurnID(
    [
      { turnID: 't1', top: 100, bottom: 300 },
      { turnID: 't2', top: 350, bottom: 800 }
    ],
    viewportBottom
  ),
  't2',
  '多条跨边时取最后一条'
);
assertEqual(pickExpandedWorkRecordTurnID([], viewportBottom), null, '没有展开记录');

// 显式收起：不看当前值，一律置为收起。
const unsetMap = new Map<string, boolean>();
collapseWorkRecordState(unsetMap, 't1');
assertEqual(unsetMap.get('t1'), false, '未设置过的键收起后的值');

const expandedMap = new Map<string, boolean>([['t1', true]]);
collapseWorkRecordState(expandedMap, 't1');
assertEqual(expandedMap.get('t1'), false, '已展开的键收起后的值');

const collapsedMap = new Map<string, boolean>([['t1', false]]);
collapseWorkRecordState(collapsedMap, 't1');
assertEqual(collapsedMap.get('t1'), false, '已收起的键再收起后的值');

// 超出上限时丢最旧的键，保留刚收起的键。
const cappedMap = new Map<string, boolean>();
for (let i = 0; i < 500; i += 1) cappedMap.set(`old${i}`, true);
collapseWorkRecordState(cappedMap, 'new');
assertEqual(cappedMap.size, 500, '裁剪后的键数量');
assertEqual(cappedMap.has('old0'), false, '最旧键是否被丢弃');
assertEqual(cappedMap.get('new'), false, '新键是否保留为收起');
