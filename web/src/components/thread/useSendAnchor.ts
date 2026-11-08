// 发送后锚定：把刚发出的那条用户消息停在时间线视口顶部，回复在下方流入时视口不动。
// 与桌面端 internal/ui/chat_anchor.go 一致：
//   - 只在「刚发出 → 回复超过一屏」这段窗口保持锚定，期间不跟随到底；
//   - 锚定期间在时间线底部垫出一屏空白，锚点消息才停得到顶部（解除时撤掉）；
//   - 用户手动滚动、点「跳到最新」、切换会话、回复已超过一屏时立刻解除，回到贴底跟随；
//   - 后端的权威消息可能晚一帧才进列表：发送前先记下基线，等新用户消息出现再建锚点；
//   - 第一次校正用平滑滚动滚过去（尊重「减少动态效果」偏好），动画期间不动 scrollTop。

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';

/** 顶对齐的判定余量（px）：锚点消息顶部贴到滚动区顶部就算已经停稳 */
const ANCHOR_PIN_TOLERANCE = 2;
/** 一次锚定（或一次内容更新）最多下发几帧顶对齐滚动，顶不上去就交还给贴底跟随 */
const ANCHOR_PIN_TRIES = 30;
/** 已经停稳后复查解除条件的间隔（ms）：不必每帧量一次 */
const ANCHOR_SETTLE_INTERVAL = 120;
/** 平滑滚动动画的时间窗（ms）：窗口内只等动画自己滚，结束后回落到即时校正 */
const ANCHOR_SMOOTH_MS = 450;
/** 平滑滚动的起步余量（ms）：动画刚下发的头一两帧 scrollTop 还没变，先别判定成「已停」 */
const ANCHOR_SMOOTH_START_GRACE = 120;

/** 锚定只关心这几个字段，纯判定函数不依赖完整消息类型 */
export type AnchorMessage = { id: string; role?: string; kind?: string };

/** 一条消息是不是用户提示词（kind 优先，缺省看 role） */
export function isUserPromptMessage(message: AnchorMessage): boolean {
  return message.kind === 'user' || message.role === 'user';
}

/** 最后一条用户消息的 ID，没有时返回 null（对应桌面端 latestUserMessageID） */
export function latestUserMessageID(messages: readonly AnchorMessage[]): string | null {
  for (let i = messages.length - 1; i >= 0; i -= 1) {
    const message = messages[i];
    if (message.id && isUserPromptMessage(message)) return message.id;
  }
  return null;
}

/**
 * 识别锚点消息：发送前记下的最后一条用户消息 ID 是基线，之后列表里最后一条用户消息
 * 只要 ID 与基线不同，就是这次发出去的那条；还没出现时返回 null。
 */
export function findAnchorMessageID(
  messages: readonly AnchorMessage[],
  baselineID: string | null
): string | null {
  const latest = latestUserMessageID(messages);
  if (!latest || latest === baselineID) return null;
  return latest;
}

/** 锚点下方的内容是否已经超过一屏：超过就停在这里看不到新东西，应当解除锚定 */
export function anchorShouldRelease(replyHeight: number, viewportHeight: number): boolean {
  if (viewportHeight <= 0) return false;
  return replyHeight > viewportHeight;
}

/** 一次平滑滚动的观察状态：开始时间 + 上次看到的滚动位置 */
export interface AnchorSmoothState {
  startedAt: number;
  lastScrollTop: number;
}

/** 系统是否要求减少动态效果：没有 matchMedia 的环境按「不要求」处理 */
export function prefersReducedMotion(): boolean {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return false;
  return window.matchMedia('(prefers-reduced-motion: reduce)').matches;
}

/** 这次校正要不要走平滑滚动：用户要求减少动态效果时直接即时跳转 */
export function shouldSmoothAnchorScroll(reducedMotion: boolean): boolean {
  return !reducedMotion;
}

/**
 * 平滑动画是否仍独占滚动位置：时间窗内，并且滚动位置自上次观察以来还在变化
 *（刚起步的几帧用起步余量兜住）。为真时 tick 只等动画，不改写 scrollTop、不计入 tries。
 */
export function anchorSmoothStillRunning(
  state: AnchorSmoothState | null,
  now: number,
  scrollTop: number
): boolean {
  if (!state) return false;
  const elapsed = now - state.startedAt;
  if (elapsed >= ANCHOR_SMOOTH_MS) return false;
  if (scrollTop !== state.lastScrollTop) return true;
  return elapsed < ANCHOR_SMOOTH_START_GRACE;
}

/** 在滚动容器里找锚点条目的 DOM 节点：虚拟列表用 data-item-key，普通列表用 data-timeline-key */
function findAnchorNode(container: HTMLElement, messageID: string): HTMLElement | null {
  const escaped = messageID.replace(/["\\]/g, '\\$&');
  return container.querySelector<HTMLElement>(
    `[data-item-key="${escaped}"], [data-timeline-key="${escaped}"]`
  );
}

export interface SendAnchorOptions {
  threadID: string;
  messages: readonly AnchorMessage[];
  scrollRef: { current: HTMLDivElement | null };
  /** 结束进入会话时的贴底跟随（锚定期间不再跟到底） */
  endPin: () => void;
  /** 恢复贴底跟随并贴到底（点「跳到最新」、解除锚定回到底部时调用） */
  resumePin: () => void;
  /** 锚定期间置为 true，交给 useInitialPinToLatest 暂停自动贴底 */
  suspendedRef: { current: boolean };
  /** 是否启用发送后置顶（对应 session.pin_sent_message，默认开启） */
  enabled?: boolean;
  onAnchorMissing?: (messageID: string) => void;
}

export interface SendAnchorControls {
  /** 发送前调用：记下当前最后一条用户消息，之后出现的新用户消息即为锚点 */
  arm: () => void;
  /** 手动解除；toEnd 为真时顺手贴回底部（点「跳到最新」、回复已满屏时走这条） */
  release: (toEnd?: boolean) => void;
  /** 是否正在保持锚定（锚定期间时间线底部要垫一屏空白） */
  holding: boolean;
  /** 锚定期间的底部垫高（px），未锚定时为 0 */
  bottomPadding: number;
}

export function useSendAnchor({
  threadID,
  messages,
  scrollRef,
  endPin,
  resumePin,
  suspendedRef,
  onAnchorMissing,
  enabled = true
}: SendAnchorOptions): SendAnchorControls {
  const [holding, setHolding] = useState(false);
  const [bottomPadding, setBottomPadding] = useState(0);

  // 已发出、还在等新用户消息进列表（对应桌面端 sendWindow）
  const pendingRef = useRef(false);
  const baselineRef = useRef<string | null>(null);
  const anchorIDRef = useRef<string | null>(null);
  const paddingRef = useRef(0);
  const triesRef = useRef(0);
  const tickRef = useRef(0);
  const threadRef = useRef(threadID);
  const messagesRef = useRef<readonly AnchorMessage[]>(messages);
  const endPinRef = useRef(endPin);
  const resumePinRef = useRef(resumePin);
  const onAnchorMissingRef = useRef(onAnchorMissing);
  const missingRequestedIDRef = useRef<string | null>(null);
  const scrollRefStable = scrollRef;
  // 这次锚定是否已经用平滑滚动做过第一次校正（每次锚定只用一次，之后即时校正兜底）
  const smoothUsedRef = useRef(false);
  // 正在进行的平滑动画状态；为 null 表示没有动画独占滚动位置
  const smoothRef = useRef<AnchorSmoothState | null>(null);
  const enabledRef = useRef(enabled);
  endPinRef.current = endPin;
  resumePinRef.current = resumePin;
  onAnchorMissingRef.current = onAnchorMissing;
  enabledRef.current = enabled;

  const cancelTick = useCallback(() => {
    if (tickRef.current) {
      // rAF 与 timeout 共用这一个句柄位，两个都清一遍避免残留
      window.cancelAnimationFrame(tickRef.current);
      window.clearTimeout(tickRef.current);
      tickRef.current = 0;
    }
  }, []);

  const releaseRef = useRef<(toEnd?: boolean) => void>(() => {});

  const release = useCallback(
    (toEnd = false) => {
      cancelTick();
      pendingRef.current = false;
      baselineRef.current = null;
      anchorIDRef.current = null;
      missingRequestedIDRef.current = null;
      triesRef.current = 0;
      paddingRef.current = 0;
      smoothRef.current = null;
      smoothUsedRef.current = false;
      // 先放开暂停标记再恢复贴底，否则 pin 会直接跳过
      suspendedRef.current = false;
      setHolding(false);
      setBottomPadding(0);
      if (toEnd) resumePinRef.current();
    },
    [cancelTick, suspendedRef]
  );
  releaseRef.current = release;

  // 逐帧把锚点消息顶到滚动区顶部，直到停稳；停稳后按较低频率复查解除条件
  const pump = useCallback(() => {
    if (!suspendedRef.current || !enabledRef.current) return;
    if (tickRef.current) return; // 已经在跑

    const tick = () => {
      tickRef.current = 0;
      const container = scrollRefStable.current;
      const anchorID = anchorIDRef.current;
      if (!suspendedRef.current || !container || !anchorID) return;
      const node = findAnchorNode(container, anchorID);
      if (!node) {
        if (missingRequestedIDRef.current !== anchorID) {
          missingRequestedIDRef.current = anchorID;
          onAnchorMissingRef.current?.(anchorID);
        }
        // 条目还没渲染出来（虚拟列表尚未绘制，或这条消息不产生时间线行）：下一帧再试
        if (triesRef.current >= ANCHOR_PIN_TRIES) {
          releaseRef.current(true);
          return;
        }
        triesRef.current += 1;
        tickRef.current = window.requestAnimationFrame(tick);
        return;
      }
      const containerTop = container.getBoundingClientRect().top;
      const nodeRect = node.getBoundingClientRect();
      const anchorTop = nodeRect.top - containerTop + container.scrollTop;
      // 锚点下方真正的内容高度：要减掉锚定期垫出的那屏空白
      const replyHeight = container.scrollHeight - paddingRef.current - anchorTop - nodeRect.height;
      if (anchorShouldRelease(replyHeight, container.clientHeight)) {
        // 回复已经超过一屏：停在这里看不到新内容，回到底部跟随
        releaseRef.current(true);
        return;
      }
      const delta = nodeRect.top - containerTop;
      if (Math.abs(delta) <= ANCHOR_PIN_TOLERANCE) {
        // 已经停在锚点消息顶部：不再干预滚动，只隔一会儿复查一次解除条件
        smoothRef.current = null;
        triesRef.current = 0;
        tickRef.current = window.setTimeout(tick, ANCHOR_SETTLE_INTERVAL);
        return;
      }
      // 平滑动画进行中：不改写 scrollTop、不计 tries，让浏览器自己滚
      const smooth = smoothRef.current;
      if (smooth && anchorSmoothStillRunning(smooth, Date.now(), container.scrollTop)) {
        smoothRef.current = { startedAt: smooth.startedAt, lastScrollTop: container.scrollTop };
        tickRef.current = window.requestAnimationFrame(tick);
        return;
      }
      // 时间窗已过（动画被打断或提前结束）：后面的帧回到即时校正兜底
      smoothRef.current = null;
      if (triesRef.current >= ANCHOR_PIN_TRIES) {
        // 顶不上去（内容确实不够一屏）：交还给贴底跟随
        releaseRef.current(true);
        return;
      }
      triesRef.current += 1;
      if (!smoothUsedRef.current && shouldSmoothAnchorScroll(prefersReducedMotion())) {
        // 本次锚定的第一次校正走平滑动画，之后不再打断它
        smoothUsedRef.current = true;
        smoothRef.current = { startedAt: Date.now(), lastScrollTop: container.scrollTop };
        container.scrollTo({ top: container.scrollTop + delta, behavior: 'smooth' });
      } else {
        // 普通列表和虚拟列表都按真实位置校正：虚拟列表的条目也是真实 DOM，滚动后同步重算
        container.scrollTop += delta;
      }
      tickRef.current = window.requestAnimationFrame(tick);
    };
    triesRef.current = 0;
    tick();
  }, [scrollRefStable, suspendedRef]);

  const startHold = useCallback(
    (anchorID: string) => {
      const container = scrollRefStable.current;
      // 先同步置位暂停标记，再结束贴底：中间任何一帧都不该被贴底 pin 抢走位置
      suspendedRef.current = true;
      endPinRef.current();
      anchorIDRef.current = anchorID;
      missingRequestedIDRef.current = null;
      pendingRef.current = false;
      triesRef.current = 0;
      smoothRef.current = null;
      smoothUsedRef.current = false;
      // 底部垫一屏空白：没有这块空白，锚点消息根本滚不到顶部
      const padding = container ? container.clientHeight : 0;
      paddingRef.current = padding;
      setBottomPadding(padding);
      setHolding(true);
      pump();
    },
    [pump, scrollRefStable, suspendedRef]
  );

  const arm = useCallback(() => {
    // 开关关闭：不锚定，始终贴底跟随
    if (!enabledRef.current) return;
    // 发送请求可能比渲染帧还快：先记下基线，等新用户消息落进列表再建锚点
    cancelTick();
    pendingRef.current = true;
    baselineRef.current = latestUserMessageID(messagesRef.current);
    anchorIDRef.current = null;
    triesRef.current = 0;
    smoothRef.current = null;
    smoothUsedRef.current = false;
  }, [cancelTick]);

  // 开关被关掉：正在锚定就解除，回到普通的贴底跟随
  useEffect(() => {
    if (enabled) return;
    release(false);
  }, [enabled, release]);

  // 消息列表变化：先等锚点消息出现，再维护锚点身份与解除条件
  useLayoutEffect(() => {
    messagesRef.current = messages;
    if (!pendingRef.current) {
      const anchorID = anchorIDRef.current;
      if (!anchorID) return;
      const latest = latestUserMessageID(messages);
      if (latest === anchorID) {
        pump();
        return;
      }
      if (latest && !messages.some((message) => message.id === anchorID)) {
        // 乐观消息被权威消息替换：ID 变了但仍是同一条，锚点跟着换，别误判成新消息
        anchorIDRef.current = latest;
        pump();
        return;
      }
      // 锚点已不再是最后一条用户消息（消息被重载）：保持没有意义了
      release();
      return;
    }
    const found = findAnchorMessageID(messages, baselineRef.current);
    if (!found) return;
    startHold(found);
  }, [messages, pump, release, startHold]);

  // 切换会话：上一个会话的锚定直接作废，并恢复贴底跟随（新会话进入时仍然贴底）
  useLayoutEffect(() => {
    if (threadRef.current === threadID) return;
    threadRef.current = threadID;
    release(true);
  }, [threadID, release]);

  // 用户自己滚走（滚轮、触摸拖动）：解除锚定，但不把视口拽回底部。
  // 只认 wheel / touchmove，程序化滚动（含平滑动画）不派发这两个事件，不会误触发。
  useEffect(() => {
    if (!holding) return;
    const container = scrollRefStable.current;
    if (!container) return;
    const onUserScroll = () => release();
    container.addEventListener('wheel', onUserScroll, { passive: true });
    container.addEventListener('touchmove', onUserScroll, { passive: true });
    return () => {
      container.removeEventListener('wheel', onUserScroll);
      container.removeEventListener('touchmove', onUserScroll);
    };
  }, [holding, release, scrollRefStable]);

  useEffect(() => cancelTick, [cancelTick]);

  return { arm, release, holding, bottomPadding };
}
