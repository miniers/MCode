// 展开的工作记录与右下角胶囊的联动：只有展开的记录跨过滚动区底边时，「收起」才有意义。
// 取不到 DOM 的纯函数便于单测，ThreadView 只负责把 rect 读出来。
export type ExpandedWorkRecordRect = {
  turnID: string;
  top: number;
  bottom: number;
};

// 模块级展开状态的上限：长期使用不无限增长，超出后丢最旧的键。
export const WORK_RECORD_EXPANDED_LIMIT = 500;

// 显式收起：不看当前值，一律置为收起（默认展开时「收起」也必须真的收起）。
export function collapseWorkRecordState(
  map: Map<string, boolean>,
  key: string,
  limit: number = WORK_RECORD_EXPANDED_LIMIT
): void {
  map.set(key, false);
  while (map.size > limit) {
    const oldest = map.keys().next().value;
    if (oldest === undefined) break;
    map.delete(oldest);
  }
}

// 命中条件：顶部在视口底边之上（留 8px 余量），底部在视口底边之下，即记录跨过可视区底边。
export function pickExpandedWorkRecordTurnID(
  records: ExpandedWorkRecordRect[],
  viewportBottom: number
): string | null {
  let hit: string | null = null;
  for (const record of records) {
    if (!record.turnID) continue;
    if (record.top < viewportBottom - 8 && record.bottom > viewportBottom) hit = record.turnID;
  }
  return hit;
}
