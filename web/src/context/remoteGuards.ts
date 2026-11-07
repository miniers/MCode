import type {
  RemoteThreadStreamSnapshot,
  RemoteThreadSummary
} from '../types/contract';
import type { RemoteThreadAction } from './remoteTypes';

export const isUnresolvedThreadAction = (action: RemoteThreadAction): boolean =>
  action.state === 'submitting' || action.state === 'pending' || action.state === 'unknown';

export const MAX_THREAD_EVENT_BUFFER = 128;

// 主线程本轮或子代理仍在运行时，整个会话即视为忙碌。
export const isThreadBusy = (
  thread: Pick<RemoteThreadSummary, 'isRunning' | 'swarmBusy'>
): boolean => thread.isRunning || Boolean(thread.swarmBusy);

export const makeActionID = (): string => {
  const cryptoAPI = globalThis.crypto as (Crypto & { randomUUID?: () => string }) | undefined;
  if (cryptoAPI && typeof cryptoAPI.randomUUID === 'function') {
    try {
      return cryptoAPI.randomUUID();
    } catch {
      // HTTP 局域网中 randomUUID 可能存在但不可调用，继续使用 getRandomValues。
    }
  }
  if (!cryptoAPI || typeof cryptoAPI.getRandomValues !== 'function') {
    throw new Error('当前浏览器不支持生成远程动作标识');
  }
  const bytes = new Uint8Array(16);
  cryptoAPI.getRandomValues(bytes);
  bytes[6] = (bytes[6] & 0x0f) | 0x40;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
};
export const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null;

export const isThreadSummary = (value: unknown): value is RemoteThreadSummary => {
  if (!isRecord(value)) return false;
  return (
    typeof value.id === 'string' &&
    typeof value.projectID === 'string' &&
    typeof value.title === 'string' &&
    typeof value.provider === 'string' &&
    (value.model === null || typeof value.model === 'string') &&
    (value.effort === null || typeof value.effort === 'string') &&
    typeof value.isRunning === 'boolean' &&
    typeof value.isArchived === 'boolean' &&
    typeof value.isPinned === 'boolean' &&
    typeof value.isDormant === 'boolean' &&
    typeof value.hasUnread === 'boolean' &&
    typeof value.attention === 'string' &&
    (value.lastActivityText === null || typeof value.lastActivityText === 'string') &&
    typeof value.section === 'string' &&
    typeof value.orderKey === 'string' &&
    typeof value.updatedAt === 'string' &&
    (value.lastUserMessageAt === undefined || typeof value.lastUserMessageAt === 'string') &&
    (value.lastStatus === null || typeof value.lastStatus === 'string') &&
    typeof value.hasPendingRequest === 'boolean'
  );
};

export const isThreadsChangedParams = (value: unknown): value is import('../types/contract').ThreadsChangedParams => {
  if (!isRecord(value) || typeof value.revision !== 'number' || !Number.isSafeInteger(value.revision)) return false;
  if (!Array.isArray(value.threads) || !value.threads.every(isThreadSummary)) return false;
  return Array.isArray(value.removedThreadIDs) && value.removedThreadIDs.every((id) => typeof id === 'string');
};

/**
 * 把推送来的摘要合并进列表行。keepListOrder 为 true 时保留该行在当前列表里的 section/orderKey：
 * 订阅快照、事件等摘要带的是不含列表上下文的默认排序键，直接覆盖会让该行跳位甚至沉到最底部。
 * 合并后没有任何字段变化时返回原对象，避免无意义的重渲染。
 */
export function mergeThreadSummary(
  existing: RemoteThreadSummary,
  summary: RemoteThreadSummary,
  keepListOrder: boolean
): RemoteThreadSummary {
  const merged: RemoteThreadSummary = keepListOrder
    ? { ...existing, ...summary, section: existing.section, orderKey: existing.orderKey }
    : { ...existing, ...summary };
  const keys = Object.keys(merged) as Array<keyof RemoteThreadSummary>;
  return keys.every((key) => existing[key] === merged[key]) ? existing : merged;
}

export const isValidStreamSnapshot = (value: unknown): value is RemoteThreadStreamSnapshot => {
  if (!isRecord(value) || !isThreadSummary(value.thread)) return false;
  if (!isRecord(value.history)) return false;
  const history = value.history;
  if (
    typeof value.threadID !== 'string' ||
    value.threadID !== value.thread.id ||
    typeof value.runID !== 'string' ||
    value.runID.length === 0 ||
    typeof value.sequence !== 'number' ||
    !Number.isSafeInteger(value.sequence) ||
    value.sequence < 0 ||
    history.threadID !== value.threadID ||
    !Array.isArray(history.messages) ||
    (history.nextCursor !== undefined && typeof history.nextCursor !== 'string' && history.nextCursor !== null) ||
    typeof history.hasMore !== 'boolean' ||
    typeof history.truncated !== 'boolean'
  ) {
    return false;
  }
  return history.messages.every((message) => {
    if (!isRecord(message)) return false;
    return (
      typeof message.id === 'string' &&
      typeof message.role === 'string' &&
      typeof message.text === 'string' &&
      typeof message.createdAt === 'string' &&
      Number.isSafeInteger(message.turnIndex)
    );
  });
};
