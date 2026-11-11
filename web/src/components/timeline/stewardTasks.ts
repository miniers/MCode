import type { TaskItem } from '../../types/contract';
import type { TimelineItem } from '../ThreadTimeline';

export interface StewardTurnTasks {
  created: TaskItem[];
  amended: TaskItem[];
}

export function stewardTasksByTurnEnd(items: TimelineItem[], tasks: TaskItem[]): Map<number, StewardTurnTasks> {
  const turns = new Map<string, number>();
  let userID: string | undefined;
  for (let index = 0; index < items.length; index += 1) {
    const item = items[index];
    if (item.type === 'message' && item.message.role === 'user') userID = item.message.id;
    if (userID) turns.set(userID, index);
  }
  const result = new Map<number, StewardTurnTasks>();
  const add = (messageID: string | undefined, task: TaskItem, kind: keyof StewardTurnTasks) => {
    if (!messageID) return;
    const index = turns.get(messageID);
    if (index === undefined) return;
    const group = result.get(index) ?? { created: [], amended: [] };
    group[kind].push(task);
    result.set(index, group);
  };
  for (const task of tasks) {
    if (task.origin !== 'owner' || task.parentID) continue;
    add(task.sourceMessageID, task, 'created');
    for (const messageID of new Set(task.amendMessageIDs ?? [])) add(messageID, task, 'amended');
  }
  return result;
}
