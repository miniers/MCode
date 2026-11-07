// 「继续」相关语义的纯函数与模块级状态，逐条移植桌面端：
// internal/ui/timeline_error_retry.go（提示词、满载退避阶梯、次数上限）与
// internal/ui/composer_continue.go（继续胶囊的显示条件与文案）。
// 组件只做渲染，状态判断一律走这里的纯函数，便于将来补单测。

/** 「继续」发出的正文（桌面端 internal/ui/timeline_error_retry.go:137，逐字照抄）。 */
export const CONTINUATION_PROMPT = '继续从中断处往下做，不要从头重来。上一轮因为出错中断了。';

/** 模型满载/限流中断时用的正文，自动退避重试也发这一条（internal/ui/timeline_error_retry.go:140）。 */
export const CONTINUATION_PROMPT_CAPACITY = '继续从中断处往下做，不要从头重来。上一轮因为模型满载中断了。';

/**
 * 输入框上方继续胶囊发出的正文。桌面端胶囊走 continueLastTurn，
 * 发送的是 internal/ui/model.go:2538 的 continuePrompt，与错误卡片的续跑提示不是同一条。
 */
export const CONTINUE_PROMPT = 'Pick up from the point where you stopped and see the task through.';

/** 自动重试次数上限（internal/ui/timeline_error_retry.go:167）。 */
export const CAPACITY_RETRY_MAX = 5;

/** 退避阶梯有效期 5 分钟：期内出现的下一条满载错误接着往上走（internal/ui/timeline_error_retry.go:163）。 */
export const CAPACITY_LADDER_WINDOW_MS = 5 * 60 * 1000;

/** 第 attempt 次（从 0 开始）自动重试前的等待：5s、10s、20s、40s、80s（internal/ui/timeline_error_retry.go:170）。 */
export function nextCapacityRetryDelay(attempt: number): number {
  const lastAttempt = CAPACITY_RETRY_MAX - 1;
  let index = Number.isFinite(attempt) ? Math.floor(attempt) : 0;
  if (index < 0) index = 0;
  if (index > lastAttempt) index = lastAttempt;
  return 5000 * 2 ** index;
}

/** 把剩余等待换算成界面上显示的正整数秒（internal/ui/timeline_error_retry.go:301）。 */
export function capacityRetrySeconds(remainingMs: number): number {
  if (!Number.isFinite(remainingMs) || remainingMs <= 0) return 0;
  return Math.max(1, Math.ceil(remainingMs / 1000));
}

/** 续跑提示只需要这几个字段；ThreadMessage 结构上满足它。 */
export interface ContinuationMessage {
  id: string;
  text?: string;
  role?: string;
  kind?: string;
  errKind?: string | null;
}

/** 满载类中断的分类：只有限流与额度耗尽会自动退避重试。 */
export type CapacityNoticeKind = 'rateLimit' | 'quota';

// 与桌面端 providerNoticeKeywords 同序同词（internal/ui/timeline_banner.go:41）：先认限流再认额度。
const RATE_LIMIT_KEYWORDS = [
  'rate limit',
  'rate_limit',
  'ratelimit',
  'too many requests',
  'throttl',
  '请求过于频繁',
  '请求太频繁',
  '限流',
  '限速'
];

const QUOTA_KEYWORDS = [
  'quota',
  'usage limit',
  'usage_limit',
  'limit reached',
  'hit your limit',
  'insufficient',
  'out of credits',
  'credit balance',
  'spend limit',
  'exhausted your capacity',
  '额度',
  '余额不足',
  '配额'
];

/**
 * 判定一条消息是不是满载/限流类中断：先看后端上报的 errKind（Web 只有 rate_limited 这一类），
 * 未命中时按桌面端的关键词表扫原文。不是则返回 null。
 */
export function capacityNoticeKind(message: ContinuationMessage | null | undefined): CapacityNoticeKind | null {
  if (!message) return null;
  const kind = (message.errKind ?? '').trim().toLowerCase();
  if (kind === 'rate_limited' || kind === 'ratelimit' || kind === 'rate_limit') return 'rateLimit';
  if (kind === 'quota' || kind === 'quota_exhausted') return 'quota';
  const text = (message.text ?? '').toLowerCase();
  if (!text) return null;
  if (RATE_LIMIT_KEYWORDS.some((keyword) => text.includes(keyword))) return 'rateLimit';
  if (QUOTA_KEYWORDS.some((keyword) => text.includes(keyword))) return 'quota';
  return null;
}

/** 按中断原因选续跑提示的文案（internal/ui/timeline_error_retry.go:143）。 */
export function continuationPromptFor(message: ContinuationMessage | null | undefined): string {
  return capacityNoticeKind(message) ? CONTINUATION_PROMPT_CAPACITY : CONTINUATION_PROMPT;
}

/** 错误卡片里的「继续」只在最新一条且没在跑时出现（internal/ui/timeline_error_row.go:54）。 */
export function errorRetryVisible(isLast: boolean, isRunning: boolean): boolean {
  return isLast && !isRunning;
}

/** 消息是不是时间线里的错误卡片（internal/ui/composer_continue.go:103）。 */
export function isErrorMessage(message: ContinuationMessage | null | undefined): boolean {
  if (!message) return false;
  const role = message.role ?? '';
  return role === 'error' || message.kind === 'error' || role.startsWith('error');
}

export interface CapacityRetryState {
  threadID: string;
  messageID: string;
  /** 已经自动重试过几次 */
  attempts: number;
  /** 下一次重试的时间点（毫秒时间戳）；null 表示还没开始计时 */
  nextAt: number | null;
  /** 退避阶梯的有效期：期内出现的下一条满载错误接着往上走 */
  ladderUntil: number;
  /** 用户点了取消，这条错误不再自动重试 */
  cancelled: boolean;
}

export interface CapacityRetryTick {
  /** 距下一次自动重试还有多久（毫秒），未武装时为 0 */
  remainingMs: number;
  /** 已经自动重试过几次 */
  attempts: number;
  /** 是否正在等待自动重试 */
  armed: boolean;
  /** 本次调用是否已到点：调用方此时发一条 continuationPromptFor(message) */
  shouldSend: boolean;
}

// 组件重挂载不能重置退避进度，所以状态放在模块级 Map 里，按 threadID + messageID 记录。
const capacityRetryStates = new Map<string, CapacityRetryState>();
// 阶梯按线程记录：上一条满载错误退避还没过期时，新错误沿用已重试次数，5s→10s→20s 不会被打断。
const capacityLadders = new Map<string, { attempts: number; ladderUntil: number }>();

function capacityRetryKey(threadID: string, messageID: string): string {
  return `${threadID}\u0000${messageID}`;
}

/** 读一读当前退避进度（只读，供渲染与调试）。 */
export function readCapacityRetryState(
  threadID: string,
  messageID: string
): CapacityRetryState | undefined {
  return capacityRetryStates.get(capacityRetryKey(threadID, messageID));
}

/**
 * 驱动满载/限流错误的自动重试，与 internal/ui/timeline_error_retry.go:216 的 tickCapacityRetry 等价：
 * 返回还要等多久、已经重试几次、是否正在等待、是否该发了。到次数上限或已被取消就不再重试。
 */
export function tickCapacityRetry(params: {
  threadID: string;
  message: ContinuationMessage;
  isLast: boolean;
  running: boolean;
  now?: number;
}): CapacityRetryTick {
  const { threadID, message, isLast, running } = params;
  const now = params.now ?? Date.now();
  const messageID = message.id ?? '';
  const idle: CapacityRetryTick = { remainingMs: 0, attempts: 0, armed: false, shouldSend: false };
  if (!messageID) return idle;

  const key = capacityRetryKey(threadID, messageID);
  let state = capacityRetryStates.get(key);
  if (!state) {
    // 换了一条错误（含切会话）：取消只对原来那条有效；上一轮退避还在阶梯期内时沿用已重试次数。
    const ladder = capacityLadders.get(threadID);
    const carry = ladder && ladder.attempts > 0 && now < ladder.ladderUntil ? ladder.attempts : 0;
    state = {
      threadID,
      messageID,
      attempts: carry,
      nextAt: null,
      ladderUntil: ladder?.ladderUntil ?? 0,
      cancelled: false
    };
    capacityRetryStates.set(key, state);
  }

  const eligible =
    isLast && !running && Boolean(capacityNoticeKind(message)) && messageID !== '';
  if (!eligible) {
    // 不再是最后一条，或者会话又在跑：停止计时，等下次重新判断。
    state.nextAt = null;
    return { remainingMs: 0, attempts: state.attempts, armed: false, shouldSend: false };
  }
  if (state.cancelled || state.attempts >= CAPACITY_RETRY_MAX) {
    return { remainingMs: 0, attempts: state.attempts, armed: false, shouldSend: false };
  }
  let nextAt = state.nextAt;
  if (nextAt === null) {
    // 第一次看到这条满载错误：等一个 5 秒起步的间隔再自动重试。
    nextAt = now + nextCapacityRetryDelay(state.attempts);
    state.nextAt = nextAt;
    if (!state.ladderUntil) state.ladderUntil = now + CAPACITY_LADDER_WINDOW_MS;
    capacityLadders.set(threadID, { attempts: state.attempts, ladderUntil: state.ladderUntil });
  }
  if (now < nextAt) {
    return {
      remainingMs: nextAt - now,
      attempts: state.attempts,
      armed: true,
      shouldSend: false
    };
  }

  state.attempts += 1;
  if (state.attempts >= CAPACITY_RETRY_MAX) {
    state.nextAt = null;
    capacityLadders.delete(threadID);
  } else {
    state.nextAt = now + nextCapacityRetryDelay(state.attempts);
    capacityLadders.set(threadID, { attempts: state.attempts, ladderUntil: state.ladderUntil });
  }
  return { remainingMs: 0, attempts: state.attempts, armed: false, shouldSend: true };
}

/** 取消这条错误的自动重试（internal/ui/timeline_error_retry.go:283）：取消后不再自动重发。 */
export function cancelCapacityRetry(threadID: string, messageID: string): void {
  if (!messageID) return;
  const key = capacityRetryKey(threadID, messageID);
  const state = capacityRetryStates.get(key);
  if (state) {
    state.cancelled = true;
    state.nextAt = null;
    state.ladderUntil = 0;
  } else {
    capacityRetryStates.set(key, {
      threadID,
      messageID,
      attempts: 0,
      nextAt: null,
      ladderUntil: 0,
      cancelled: true
    });
  }
  capacityLadders.delete(threadID);
}

/** 继续胶囊的文案种类（internal/ui/composer_continue.go:6）。 */
export type ContinuePillKind = 'none' | 'interrupted' | 'failed';

/** 判定继续胶囊所需的纯数据快照（internal/ui/composer_continue.go:19）。 */
export interface ContinuePillState {
  /** 本次运行中用户主动停止过 */
  locallyInterrupted: boolean;
  /** 后端给出的线程最近一轮结束状态 */
  lastStatus?: string | null;
  /** 线程本体或名下子线程还在运行 */
  isRunning: boolean;
  /** 这一次异常结束已经被用户忽略过 */
  dismissed: boolean;
  /** 最后一条消息的 ID */
  lastMessageID?: string;
  /** 最后一条消息是错误卡片 */
  lastMessageIsError: boolean;
}

/** 把线程最近一轮终态映射成胶囊文案种类（internal/ui/composer_continue.go:35）。 */
export function continuePillStatusKind(lastStatus?: string | null): ContinuePillKind {
  switch ((lastStatus ?? '').trim().toLowerCase()) {
    case 'interrupted':
    case 'cancelled':
    case 'canceled':
    case 'stopped':
      return 'interrupted';
    case 'failed':
      return 'failed';
    default:
      return 'none';
  }
}

/** 纯函数：是否显示继续胶囊，以及显示哪一种文案（internal/ui/composer_continue.go:47）。 */
export function shouldShowContinuePill(state: ContinuePillState): {
  show: boolean;
  kind: ContinuePillKind;
} {
  if (state.isRunning) return { show: false, kind: 'none' };
  if (state.locallyInterrupted) return { show: true, kind: 'interrupted' };
  const kind = continuePillStatusKind(state.lastStatus);
  if (kind === 'none') return { show: false, kind: 'none' };
  // 失败且时间线末尾就是错误卡片时不再提供胶囊：错误卡片自己已经带「继续」入口。
  if (kind === 'failed' && state.lastMessageIsError) return { show: false, kind: 'none' };
  // 这一次异常结束被忽略过就不再显示；出现新一轮（最后一条消息 ID 变化）后自动恢复。
  if (state.dismissed) return { show: false, kind: 'none' };
  return { show: true, kind };
}

/** 胶囊的状态文案与操作提示；按钮文字统一为「继续」（internal/ui/composer_continue.go:72）。 */
export function continuePillLabels(kind: ContinuePillKind): { status: string; tooltip: string } {
  if (kind === 'failed') return { status: '上一轮失败', tooltip: '从失败处继续' };
  return { status: '上一轮已中断', tooltip: '继续上一轮' };
}

// 忽略记录按「线程 ID -> 当时最后一条消息 ID」保存：最后一条消息变了说明开了新一轮，记录自动失效。
const continueDismissed = new Map<string, string>();

/** 这一次异常结束是否已被忽略（internal/ui/composer_continue.go:121）。 */
export function isContinueDismissed(threadID: string, lastMessageID?: string): boolean {
  if (!threadID) return false;
  return continueDismissed.get(threadID) === (lastMessageID ?? '');
}

/** 收起继续胶囊：记下这一次异常结束已被忽略，直到最后一条消息变化（internal/ui/composer_continue.go:128）。 */
export function dismissContinuePill(threadID: string, lastMessageID?: string): void {
  if (!threadID) return;
  continueDismissed.set(threadID, lastMessageID ?? '');
}
