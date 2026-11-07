// 「跳到最新」按钮的未读计数：口径与桌面端 internal/ui/timeline_unread.go 对齐。
// 以离开底部那一刻最后一条消息的 ID 作锚，锚之后新到达的助手正文逐条计数；
// 向上翻历史分页会把旧消息插到锚之前，因此不会被算成新消息。

/** 只有渲染未读需要的字段，避免让 utils 反向依赖时间线组件的类型。 */
export interface UnreadMessage {
  id: string;
  role?: string;
  kind?: string;
}

/** 某个会话离开底部时的基线：最后一条消息 ID + 当时已计数条数。 */
export interface UnreadAnchor {
  threadID: string;
  lastMessageID: string;
  countedMessageCount: number;
}

/** 桌面端未读胶囊的最大数字，超过后显示「99+」。 */
export const UNREAD_LABEL_MAX = 99;

/** 计入未读的消息：助手正文各计 1 条；工具调用与思考过程不计，用户自己的消息也不计。 */
function isUnreadMessage(message: UnreadMessage): boolean {
  if (message.role !== 'assistant') return false;
  const kind = message.kind ?? '';
  return kind !== 'tool' && kind !== 'thinking';
}

/** 以当前消息列表生成未读基线。 */
export function createUnreadAnchor(threadID: string, messages: UnreadMessage[]): UnreadAnchor {
  let countedMessageCount = 0;
  for (const message of messages) {
    if (isUnreadMessage(message)) countedMessageCount += 1;
  }
  const lastMessage = messages.length > 0 ? messages[messages.length - 1] : null;
  return {
    threadID,
    lastMessageID: lastMessage?.id ?? '',
    countedMessageCount
  };
}

/**
 * 统计基线之后新增的未读条数。
 * 锚点消息还在列表里时只数它后面的消息；锚点消息已被替换或截断（乐观消息换 ID、
 * 会话被改写）时退回按计数条数差估算，避免把整段历史都算成未读。
 */
export function countUnreadMessages(messages: UnreadMessage[], anchor: UnreadAnchor): number {
  let start = -1;
  if (anchor.lastMessageID !== '') {
    for (let index = messages.length - 1; index >= 0; index -= 1) {
      if (messages[index].id === anchor.lastMessageID) {
        start = index + 1;
        break;
      }
    }
  }

  if (start < 0) {
    let counted = 0;
    for (const message of messages) {
      if (isUnreadMessage(message)) counted += 1;
    }
    return Math.max(0, counted - anchor.countedMessageCount);
  }

  let unread = 0;
  for (const message of messages.slice(start)) {
    if (isUnreadMessage(message)) unread += 1;
  }
  return unread;
}

/** 生成回到底部胶囊的未读文案；超过 99 条显示「99+」，非正数返回空串。 */
export function formatUnreadCount(count: number): string {
  if (!Number.isFinite(count) || count <= 0) return '';
  const rounded = Math.floor(count);
  if (rounded <= 0) return '';
  if (rounded > UNREAD_LABEL_MAX) return `${UNREAD_LABEL_MAX}+ 条新消息`;
  return `${rounded} 条新消息`;
}
