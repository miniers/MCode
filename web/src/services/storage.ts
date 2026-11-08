// 安全凭据存储与配对令牌提取服务
// 会话令牌仅写入用户选择的浏览器存储，严禁进入日志、URL 或 Service Worker Cache。

import { clearPushSession, savePushSession } from './pushSession';

const SESSION_TOKEN_KEY = 'mcode_remote_session_token';
const DEVICE_ID_KEY = 'mcode_remote_device_id';
const DEVICE_LABEL_KEY = 'mcode_remote_device_label';
const THEME_KEY = 'mcode_remote_theme_preference';
const HAPTICS_KEY = 'mcode_remote_haptics';
const AUTO_COLLAPSE_KEY = 'mcode_remote_auto_collapse_reasoning';
const ENVIRONMENT_STATE_KEY = 'mcode_remote_environment_state_v1';
const PAIR_TOKEN_PATTERN = /^(?:[0-9a-fA-F]{64}|[0-9]{6})$/;

export interface PairingInputResult {
  token: string | null;
  error: string | null;
}

export interface DeviceIdentity {
  deviceID: string;
  label: string;
}

export interface SessionCredentials {
  token: string;
  deviceIdentity: DeviceIdentity | null;
  rememberDevice: boolean;
}

export interface EnvironmentUIState {
  selectedProjectID: string | null;
}

const pairingError = (error: string): PairingInputResult => ({ token: null, error });

const validPairing = (token: string): PairingInputResult => ({
  token: token.toLowerCase(),
  error: null
});

function parsePairingURL(url: URL): PairingInputResult {
  if (typeof window === 'undefined') {
    return pairingError('当前页面无法验证配对链接');
  }

  if (url.origin !== window.location.origin) {
    return pairingError('配对链接不是当前 mcode 服务的地址，请从本机 Mac 设置复制链接');
  }

  if (url.username || url.password || url.search) {
    return pairingError('配对链接格式无效：配对码只能位于地址的 #pair= 片段中');
  }

  if (!url.hash.startsWith('#pair=')) {
    return pairingError('配对链接缺少 #pair= 配对码片段');
  }

  const params = new URLSearchParams(url.hash.slice(1));
  const entries = Array.from(params.entries());
  if (entries.length !== 1 || entries[0][0] !== 'pair') {
    return pairingError('配对链接格式无效：只支持 #pair= 配对码');
  }

  const token = entries[0][1]?.trim() ?? '';
  if (!PAIR_TOKEN_PATTERN.test(token)) {
    return pairingError('配对码无效：请输入 Mac 设置中显示的 6 位授权码或 64 位配对码');
  }

  return validPairing(token);
}

/**
 * 解析用户输入的原始配对码或完整配对链接，不把链接本身交给 RPC 服务端。
 */
export function parsePairingInput(value: string): PairingInputResult {
  const input = value.trim();
  if (!input) {
    return pairingError('请输入配对码或完整配对链接');
  }

  if (PAIR_TOKEN_PATTERN.test(input)) {
    return validPairing(input);
  }

  let url: URL;
  try {
    url = new URL(input);
  } catch {
    return pairingError('请输入 Mac 设置中显示的 6 位授权码、64 位配对码或完整配对链接');
  }

  return parsePairingURL(url);
}

/**
 * 从 URL Hash 中提取一次性配对令牌并立即从地址栏抹除。
 * 普通路由 hash 必须原样保留，只清除配对凭据。
 */
export function extractPairingTokenFromURL(): PairingInputResult {
  if (typeof window === 'undefined') return { token: null, error: null };

  try {
    const rawHash = window.location.hash;
    const rawSearch = window.location.search;
    const hasHashCredential = /^#(?:pair|token|session)=/.test(rawHash);

    let cleanSearch = rawSearch;
    let queryCredentialError: string | null = null;
    if (rawSearch) {
      const searchParams = new URLSearchParams(rawSearch);
      if (searchParams.has('pair') || searchParams.has('token') || searchParams.has('session')) {
        searchParams.delete('pair');
        searchParams.delete('token');
        searchParams.delete('session');
        const remaining = searchParams.toString();
        cleanSearch = remaining ? `?${remaining}` : '';
        queryCredentialError = '配对链接格式无效：配对凭据严禁置于 URL 查询参数中，请使用 #pair= 片段';
      }
    }

    const pairingResult = hasHashCredential
      ? parsePairingInput(window.location.href)
      : null;

    // 只抹除配对凭据；普通路由 hash（例如 #/t/<id>）必须留在地址栏和历史记录中。
    if (hasHashCredential || cleanSearch !== rawSearch) {
      const cleanHash = hasHashCredential ? '' : rawHash;
      const cleanUrl = window.location.pathname + cleanSearch + cleanHash;
      window.history.replaceState(null, '', cleanUrl);
    }

    if (queryCredentialError) {
      return pairingError(queryCredentialError);
    }

    return pairingResult ?? { token: null, error: null };
  } catch {
    return pairingError('配对链接格式无效');
  }
}

// 旧调用方保留原名称；新入口名明确表达只清除配对凭据。
export const extractAndClearPairToken = extractPairingTokenFromURL;

export function getEnvironmentID(): string {
  if (typeof window === 'undefined') return 'default';
  return window.location.origin;
}

export function readEnvironmentUIState(environmentID: string): EnvironmentUIState {
  const fallback: EnvironmentUIState = { selectedProjectID: null };
  if (typeof window === 'undefined') return fallback;
  try {
    const raw = window.localStorage.getItem(ENVIRONMENT_STATE_KEY);
    if (!raw) return fallback;
    const all = JSON.parse(raw) as Record<string, Partial<EnvironmentUIState>>;
    const state = all[environmentID];
    return {
      selectedProjectID: typeof state?.selectedProjectID === 'string' ? state.selectedProjectID : null
    };
  } catch {
    return fallback;
  }
}

export function writeEnvironmentUIState(environmentID: string, state: EnvironmentUIState): void {
  if (typeof window === 'undefined') return;
  try {
    const raw = window.localStorage.getItem(ENVIRONMENT_STATE_KEY);
    const all = raw ? (JSON.parse(raw) as Record<string, EnvironmentUIState>) : {};
    all[environmentID] = state;
    window.localStorage.setItem(ENVIRONMENT_STATE_KEY, JSON.stringify(all));
  } catch {
    // UI 状态不是连接凭据，存储失败不应阻断页面。
  }
}

export function readSessionCredentials(): { credentials: SessionCredentials | null; notice: string | null } {
  if (typeof window === 'undefined') return { credentials: null, notice: null };
  let readFailed = false;
  const read = (rememberDevice: boolean): SessionCredentials | null => {
    let storage: Storage;
    let token: string | null;
    try {
      storage = rememberDevice ? window.localStorage : window.sessionStorage;
      token = storage.getItem(SESSION_TOKEN_KEY);
    } catch {
      readFailed = true;
      return null;
    }
    if (!token) return null;
    let deviceIdentity: DeviceIdentity | null = null;
    try {
      const deviceID = storage.getItem(DEVICE_ID_KEY);
      const label = storage.getItem(DEVICE_LABEL_KEY);
      if (deviceID && label) deviceIdentity = { deviceID, label };
    } catch {
      readFailed = true;
    }
    return { token, deviceIdentity, rememberDevice };
  };
  // 旧版 sessionStorage 凭据继续按临时设备恢复，不自动改成持久保存。
  const temporary = read(false);
  const remembered = read(true);
  return {
    credentials: temporary ?? remembered,
    notice: readFailed ? '浏览器无法读取部分配对凭据；未能确认此浏览器保存的设备状态。' : null
  };
}

function removeCredentials(rememberDevice: boolean): boolean {
  let storage: Storage;
  try {
    storage = rememberDevice ? window.localStorage : window.sessionStorage;
  } catch {
    return false;
  }
  let removed = true;
  for (const key of [SESSION_TOKEN_KEY, DEVICE_ID_KEY, DEVICE_LABEL_KEY]) {
    try {
      storage.removeItem(key);
    } catch {
      removed = false;
    }
  }
  return removed;
}

export function saveSessionCredentials(credentials: SessionCredentials): string | null {
  if (typeof window === 'undefined') return '当前页面无法保存配对凭据。';
  let saved = true;
  try {
    const storage = credentials.rememberDevice ? window.localStorage : window.sessionStorage;
    if (credentials.deviceIdentity) {
      storage.setItem(DEVICE_ID_KEY, credentials.deviceIdentity.deviceID);
      storage.setItem(DEVICE_LABEL_KEY, credentials.deviceIdentity.label);
    } else {
      storage.removeItem(DEVICE_ID_KEY);
      storage.removeItem(DEVICE_LABEL_KEY);
    }
    storage.setItem(SESSION_TOKEN_KEY, credentials.token);
  } catch {
    saved = false;
    removeCredentials(credentials.rememberDevice);
  }
  const otherRemoved = removeCredentials(!credentials.rememberDevice);
  // Service Worker 只能从 IndexedDB 取令牌：仅「记住此设备」且凭据写入成功时才复制，其余情况一律清除。
  if (saved && credentials.rememberDevice) {
    void savePushSession({
      origin: window.location.origin,
      token: credentials.token,
      deviceID: credentials.deviceIdentity?.deviceID ?? '',
      updatedAt: Date.now()
    });
  } else {
    void clearPushSession();
  }
  const notices: string[] = [];
  if (!saved) notices.push('浏览器无法保存配对凭据；当前页面仍可使用，但无法确认刷新或关闭页面后的配对状态。');
  if (!otherRemoved) notices.push('浏览器未能清理另一种存储中的配对凭据；请到 Mac 设置 → 远程控制撤销此设备以确保失效。');
  return notices.length ? notices.join(' ') : null;
}

export function clearSessionCredentials(): string | null {
  if (typeof window === 'undefined') return null;
  void clearPushSession();
  const temporaryRemoved = removeCredentials(false);
  const rememberedRemoved = removeCredentials(true);
  return temporaryRemoved && rememberedRemoved
    ? null
    : '已退出当前页面，但浏览器未能完全清理配对凭据；请到 Mac 设置 → 远程控制撤销此设备，并清除此站点的浏览器数据。';
}

/**
 * 主题设置存取 (普通用户界面偏好)
 */
export function getStoredTheme(): 'system' | 'light' | 'dark' {
  if (typeof window === 'undefined') return 'system';
  try {
    const val = window.localStorage.getItem(THEME_KEY);
    if (val === 'light' || val === 'dark' || val === 'system') return val;
  } catch {
    // 忽略
  }
  return 'system';
}

export function setStoredTheme(theme: 'system' | 'light' | 'dark'): void {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(THEME_KEY, theme);
  } catch {
    // 忽略
  }
}

/**
 * 触感反馈开关（W-18）：默认开启，只对支持 Vibration API 的 Android 生效。
 */
export function getStoredHapticsEnabled(): boolean {
  if (typeof window === 'undefined') return true;
  try {
    const val = window.localStorage.getItem(HAPTICS_KEY);
    if (val === 'true') return true;
    if (val === 'false') return false;
  } catch {
    // 忽略
  }
  return true;
}

export function setStoredHapticsEnabled(enabled: boolean): void {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(HAPTICS_KEY, enabled ? 'true' : 'false');
  } catch {
    // 忽略
  }
}

/**
 * 「自动折叠思考过程」偏好：默认开启（已结束回合默认收起思考过程）。
 * 写入后用 window 自定义事件通知订阅方，时间线无需重载页面即可刷新。
 */
export const AUTO_COLLAPSE_CHANGED_EVENT = 'mcode:auto-collapse-changed';

export function getStoredAutoCollapse(): boolean {
  if (typeof window === 'undefined') return true;
  try {
    const val = window.localStorage.getItem(AUTO_COLLAPSE_KEY);
    if (val === 'true') return true;
    if (val === 'false') return false;
  } catch {
    // 忽略
  }
  return true;
}

export function setStoredAutoCollapse(enabled: boolean): void {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(AUTO_COLLAPSE_KEY, enabled ? 'true' : 'false');
  } catch {
    // 忽略
  }
  window.dispatchEvent(new CustomEvent<boolean>(AUTO_COLLAPSE_CHANGED_EVENT, { detail: enabled }));
}

