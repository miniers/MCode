// Service Worker 推送凭据：只有「记住此设备」才把会话令牌写入 IndexedDB。
// 与 localStorage 同源可读，仍严禁进入日志、URL 或 Service Worker Cache。

const DB_NAME = 'mcode-sw-auth';
const DB_VERSION = 1;
const STORE_NAME = 'session';
const SESSION_KEY = 'current';

export interface PushSessionRecord {
  origin: string;
  token: string;
  deviceID: string;
  updatedAt: number;
}

function openPushDatabase(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    if (typeof indexedDB === 'undefined') {
      reject(new Error('IndexedDB 不可用'));
      return;
    }
    const request = indexedDB.open(DB_NAME, DB_VERSION);
    request.onupgradeneeded = () => {
      const db = request.result;
      if (!db.objectStoreNames.contains(STORE_NAME)) db.createObjectStore(STORE_NAME);
    };
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error ?? new Error('IndexedDB 打开失败'));
    request.onblocked = () => reject(new Error('IndexedDB 被旧连接占用'));
  });
}

function writePushSession(db: IDBDatabase, run: (store: IDBObjectStore) => IDBRequest | void): Promise<void> {
  return new Promise((resolve, reject) => {
    const transaction = db.transaction(STORE_NAME, 'readwrite');
    run(transaction.objectStore(STORE_NAME));
    transaction.oncomplete = () => resolve();
    transaction.onerror = () => reject(transaction.error ?? new Error('IndexedDB 写入失败'));
    transaction.onabort = () => reject(transaction.error ?? new Error('IndexedDB 写入被中止'));
  });
}

/**
 * 写入 Service Worker 使用的会话凭据；任何失败都静默忽略，不影响正常配对。
 */
export async function savePushSession(value: PushSessionRecord): Promise<void> {
  try {
    const db = await openPushDatabase();
    try {
      await writePushSession(db, (store) => {
        store.put(value, SESSION_KEY);
      });
    } finally {
      db.close();
    }
  } catch {
    // 隐私模式、配额满或旧浏览器：SW 拿不到令牌时通知只保留「打开」。
  }
}

/**
 * 清除 Service Worker 副本；退出登录、忘记设备、认证失败都会走到这里。
 */
export async function clearPushSession(): Promise<void> {
  try {
    const db = await openPushDatabase();
    try {
      await writePushSession(db, (store) => {
        store.delete(SESSION_KEY);
      });
    } finally {
      db.close();
    }
  } catch {
    // 清除失败不影响页面侧凭据状态，撤销设备仍是最终兜底。
  }
}
