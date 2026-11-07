// 线程草稿持久化：只存文本（不存附件），按「环境 + 线程」隔离，与配对令牌无关，重新配对后草稿仍在。
// 单条上限 32K 字符、最多 50 条、TTL 14 天；localStorage 不可用或写入失败时静默退化为只用内存。
// 草稿可能含敏感文本：忘记设备/退出时由 clearAllDrafts 一并清除（含项目草稿的 key）。

import { getEnvironmentID } from './storage';

/** 所有草稿 key 的公共前缀（含旧的项目草稿 `mcode.remote.draft.<projectID>`）。 */
const DRAFT_PREFIX = 'mcode.remote.draft.';
const THREAD_PREFIX = `${DRAFT_PREFIX}v1.thread.`;
const MAX_TEXT_LENGTH = 32 * 1024;
const MAX_ENTRIES = 50;
const TTL_MS = 14 * 24 * 60 * 60 * 1000;
/** 配额写入失败时一次淘汰的最旧条数。 */
const QUOTA_EVICT_COUNT = 10;

interface StoredThreadDraft {
  v: 1;
  text: string;
  updatedAt: number;
}

/** threadID -> updatedAt，当前环境的草稿索引；首次使用时扫描 localStorage 建立。 */
const index = new Map<string, number>();
/** localStorage 不可用时的内存退化存储。 */
const memory = new Map<string, StoredThreadDraft>();
let initialized = false;

function getStorage(): Storage | null {
  try {
    return typeof window === 'undefined' ? null : window.localStorage;
  } catch {
    return null;
  }
}

function threadKey(threadID: string): string {
  return `${THREAD_PREFIX}${getEnvironmentID()}.${threadID}`;
}

function parseDraft(raw: string | null): StoredThreadDraft | null {
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw) as Partial<StoredThreadDraft>;
    if (parsed?.v !== 1 || typeof parsed.text !== 'string' || typeof parsed.updatedAt !== 'number') return null;
    return { v: 1, text: parsed.text, updatedAt: parsed.updatedAt };
  } catch {
    return null;
  }
}

function threadKeys(storage: Storage): string[] {
  const keys: string[] = [];
  for (let i = 0; i < storage.length; i++) {
    const key = storage.key(i);
    if (key && key.startsWith(THREAD_PREFIX)) keys.push(key);
  }
  return keys;
}

function removeKey(storage: Storage, key: string): void {
  try {
    storage.removeItem(key);
  } catch {
    // 忽略
  }
}

/** 淘汰当前环境里最旧的若干条（不含 exceptID）。 */
function evictOldest(count: number, exceptID?: string): void {
  const storage = getStorage();
  const oldest = [...index.entries()]
    .filter(([id]) => id !== exceptID)
    .sort((a, b) => a[1] - b[1])
    .slice(0, count);
  for (const [id] of oldest) {
    index.delete(id);
    if (storage) removeKey(storage, threadKey(id));
  }
}

/** 清理过期、损坏的草稿并按数量上限淘汰；同时重建当前环境的索引。 */
function sweep(): void {
  const storage = getStorage();
  if (!storage) return;
  const now = Date.now();
  const envPrefix = `${THREAD_PREFIX}${getEnvironmentID()}.`;
  const live: Array<[string, number]> = [];
  try {
    for (const key of threadKeys(storage)) {
      const draft = parseDraft(storage.getItem(key));
      if (!draft || now - draft.updatedAt > TTL_MS) {
        removeKey(storage, key);
        continue;
      }
      if (key.startsWith(envPrefix)) live.push([key.slice(envPrefix.length), draft.updatedAt]);
    }
  } catch {
    return;
  }
  index.clear();
  for (const [id, updatedAt] of live) index.set(id, updatedAt);
  if (index.size > MAX_ENTRIES) evictOldest(index.size - MAX_ENTRIES);
}

function ensureInitialized(): void {
  if (initialized) return;
  initialized = true;
  sweep();
}

/** 读取线程草稿；不存在、过期或不可读时返回 null。 */
export function loadThreadDraft(threadID: string): string | null {
  ensureInitialized();
  const storage = getStorage();
  if (!storage) return memory.get(threadID)?.text ?? null;
  try {
    const key = threadKey(threadID);
    const draft = parseDraft(storage.getItem(key));
    if (!draft) return null;
    if (Date.now() - draft.updatedAt > TTL_MS) {
      removeKey(storage, key);
      index.delete(threadID);
      return null;
    }
    return draft.text || null;
  } catch {
    return null;
  }
}

/** 保存线程草稿；空文本等同删除。超长文本截断到上限。 */
export function saveThreadDraft(threadID: string, text: string): void {
  if (!text) {
    clearThreadDraft(threadID);
    return;
  }
  ensureInitialized();
  let value = text;
  if (value.length > MAX_TEXT_LENGTH) {
    value = value.slice(0, MAX_TEXT_LENGTH);
    // 不要把代理对截成半个字符
    const last = value.charCodeAt(value.length - 1);
    if (last >= 0xd800 && last <= 0xdbff) value = value.slice(0, -1);
  }
  const draft: StoredThreadDraft = { v: 1, text: value, updatedAt: Date.now() };
  const storage = getStorage();
  if (!storage) {
    memory.set(threadID, draft);
    return;
  }
  const key = threadKey(threadID);
  const payload = JSON.stringify(draft);
  try {
    storage.setItem(key, payload);
  } catch {
    // 配额：先淘汰最旧的草稿再重试一次，仍失败就放弃（内存缓存仍在）。
    evictOldest(QUOTA_EVICT_COUNT, threadID);
    try {
      storage.setItem(key, payload);
    } catch {
      return;
    }
  }
  index.set(threadID, draft.updatedAt);
  if (index.size > MAX_ENTRIES) evictOldest(index.size - MAX_ENTRIES, threadID);
}

/** 删除单个线程草稿。 */
export function clearThreadDraft(threadID: string): void {
  memory.delete(threadID);
  index.delete(threadID);
  const storage = getStorage();
  if (storage) removeKey(storage, threadKey(threadID));
}

/** 清除所有草稿（线程草稿与项目草稿，含旧 key）；忘记设备/退出时调用。 */
export function clearAllDrafts(): void {
  memory.clear();
  index.clear();
  const storage = getStorage();
  if (!storage) return;
  try {
    const keys: string[] = [];
    for (let i = 0; i < storage.length; i++) {
      const key = storage.key(i);
      if (key && key.startsWith(DRAFT_PREFIX)) keys.push(key);
    }
    for (const key of keys) removeKey(storage, key);
  } catch {
    // 忽略
  }
}
