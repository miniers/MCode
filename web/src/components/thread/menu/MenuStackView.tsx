// 页面层：所有页面常驻 DOM，用 data-relation 控制滑动方向与可见性；
// 非活动页设 inert/aria-hidden。入栈页先按 after 渲染两帧再切 active 触发滑入；
// 出栈页复用同一个 DOM 节点滑出，220ms 后再卸载；尺寸切换时先量当前高度，再过渡到目标高度；
// menu 尺寸内翻页时先锁住旧高度，入场结束后再过渡到新页内容高度。

import React, { useEffect, useLayoutEffect, useRef, useState } from 'react';

export interface MenuStackPage {
  id: string;
  node: React.ReactNode;
}

export interface MenuStackViewProps {
  pages: MenuStackPage[];
  activeId: string;
  /** 父层用 pageSizeFor(栈顶 kind) 计算：compact 页 menu，详情页 panel */
  size: 'menu' | 'panel';
  className?: string;
  /** 宽屏侧栏等由父容器撑满的场景：跳过本文的 inline 高度逻辑 */
  fillParent?: boolean;
}

// SSR 下 useLayoutEffect 会告警；服务端退化为 useEffect（不会执行）。
const useIsoLayoutEffect = typeof window === 'undefined' ? useEffect : useLayoutEffect;

/** 出栈页滑出时长，与 index.css 里 .menu-page 的 transition（0.2s）对齐。 */
const EXIT_ANIMATION_MS = 220;
/** 翻页时等入场的双 rAF 结束，新页才真正撑开内容。 */
const MENU_HEIGHT_ENTER_FRAMES = 2;
/** 高度过渡的兜底时长：transitionend 漏发时也要把高度交回给内容撑开。 */
const MENU_HEIGHT_FALLBACK_MS = 250;

/**
 * panel 尺寸下栈容器的固定高度：先按卡片自身的上限收窄视口，再扣掉父卡片的
 * padding/border，避免卡片整体超出视口。
 */
export function computePanelStackHeight(appHeight: number, chrome: number): number {
  return Math.max(0, Math.min(appHeight * 0.92, appHeight - 20) - chrome);
}

/**
 * menu 尺寸翻页过渡的目标高度：不超过 panel 的固定高度上限，
 * 避免一页很长的内容把底部抽屉顶出视口。
 */
export function clampMenuHeight(contentHeight: number, appHeight: number, chrome: number): number {
  return Math.min(contentHeight, computePanelStackHeight(appHeight, chrome));
}

/**
 * 量容器的内容自然高度：临时把 inline height 交给内容，读一次高度再还原原值。
 * 同一帧内同步完成，浏览器不会画出中间态；还原后再由调用方写入目标高度。
 * 结构类型让测试可以直接传假对象。
 */
export function measureNaturalHeight(container: {
  style: { height: string };
  getBoundingClientRect: () => { height: number };
}): number {
  const previousHeight = container.style.height;
  container.style.height = 'auto';
  const measured = container.getBoundingClientRect().height;
  container.style.height = previousHeight;
  if (!Number.isFinite(measured) || measured <= 0) return 0;
  return measured;
}

function readAppHeight(): number {
  if (typeof window === 'undefined') return 0;
  const raw = window.getComputedStyle(document.documentElement).getPropertyValue('--app-height').trim();
  const parsed = Number.parseFloat(raw);
  // --app-height 可能是 100dvh 之类的非像素值，只有 px 才可直接使用。
  if (raw.endsWith('px') && Number.isFinite(parsed) && parsed > 0) return parsed;
  return window.innerHeight;
}

/** 父卡片自身的上下 padding 与边框：inline 高度必须扣掉，否则整卡撑出视口。 */
function readParentChrome(container: HTMLDivElement | null): number {
  const parent = container?.parentElement;
  if (!parent || typeof window === 'undefined') return 0;
  const style = window.getComputedStyle(parent);
  const toPx = (value: string) => {
    const parsed = Number.parseFloat(value);
    return Number.isFinite(parsed) ? parsed : 0;
  };
  return (
    toPx(style.paddingTop) +
    toPx(style.paddingBottom) +
    toPx(style.borderTopWidth) +
    toPx(style.borderBottomWidth)
  );
}

export function MenuStackView({
  pages,
  activeId,
  size,
  className,
  fillParent = false
}: MenuStackViewProps) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const [inlineHeight, setInlineHeight] = useState<number | undefined>(undefined);
  const [dataDirection, setDataDirection] = useState<'forward' | 'back'>('forward');
  // 新入栈的活动页：先渲染在右侧，下一帧再切 active
  const [enteringIds, setEnteringIds] = useState<string[]>([]);
  // 已出栈但还在滑出的页：保留最后一次渲染的 node，220ms 后卸载
  const [exitingPages, setExitingPages] = useState<MenuStackPage[]>([]);

  const activeIndex = Math.max(0, pages.findIndex((page) => page.id === activeId));
  const pagesKey = pages.map((page) => page.id).join('|');

  const prevActiveIndexRef = useRef(activeIndex);
  const prevSizeRef = useRef<'menu' | 'panel' | null>(null);
  const prevFillParentRef = useRef(fillParent);
  const seenIdsRef = useRef<Set<string>>(new Set());
  const firstMountRef = useRef(true);
  const previousPagesRef = useRef<MenuStackPage[]>(pages);
  const exitTimersRef = useRef(new Map<string, number>());
  // 翻页高度锁：上一次 commit 后容器的真实高度
  const lastCommitHeightRef = useRef(0);
  // 进行中的翻页高度会话（锁旧高度 → 过渡到新页内容高度）
  const menuHeightSessionRef = useRef<{ cancel: () => void } | null>(null);
  const prevHeightActiveIdRef = useRef(activeId);
  const prevHeightSizeRef = useRef<'menu' | 'panel' | null>(null);
  // 双 rAF 与 transitionend 回调里读最新的入场集合，避免把等待写进依赖
  const enteringIdsRef = useRef(enteringIds);
  enteringIdsRef.current = enteringIds;

  // 只用于观测的 data-direction：活动下标变大记为 forward，变小记为 back。
  useEffect(() => {
    const previous = prevActiveIndexRef.current;
    if (previous === activeIndex) return;
    prevActiveIndexRef.current = activeIndex;
    setDataDirection(activeIndex > previous ? 'forward' : 'back');
  }, [activeIndex]);

  // 非活动页不可聚焦、不响应点击；SSR 无 DOM 时保留 aria-hidden 作为降级。
  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;
    const sections = container.querySelectorAll<HTMLElement>('section[data-relation]');
    sections.forEach((section) => {
      (section as HTMLElement & { inert: boolean }).inert = section.dataset.relation !== 'active';
    });
  });

  // 入栈/出栈的动画状态：只看页面 id 集合的变化。
  useIsoLayoutEffect(() => {
    const currentIds = pages.map((page) => page.id);
    const currentSet = new Set(currentIds);
    const previousPages = previousPagesRef.current;
    previousPagesRef.current = pages;
    const wasFirstMount = firstMountRef.current;
    firstMountRef.current = false;

    // 出栈页保留最后一份 node：滑出动画结束再移除
    const removedPages = previousPages.filter((page) => !currentSet.has(page.id));
    if (removedPages.length > 0) {
      for (const page of removedPages) {
        const existingTimer = exitTimersRef.current.get(page.id);
        if (existingTimer !== undefined) window.clearTimeout(existingTimer);
        exitTimersRef.current.set(
          page.id,
          window.setTimeout(() => {
            exitTimersRef.current.delete(page.id);
            setExitingPages((prev) => prev.filter((item) => item.id !== page.id));
          }, EXIT_ANIMATION_MS)
        );
      }
      setExitingPages((prev) => [
        ...prev.filter((page) => !removedPages.some((item) => item.id === page.id)),
        ...removedPages
      ]);
    }

    // 出栈页又回来了：取消卸载并移出保留列表，避免同一页出现两份
    for (const id of currentIds) {
      const timer = exitTimersRef.current.get(id);
      if (timer === undefined) continue;
      window.clearTimeout(timer);
      exitTimersRef.current.delete(id);
    }
    setExitingPages((prev) => {
      const next = prev.filter((page) => !currentSet.has(page.id));
      return next.length === prev.length ? prev : next;
    });

    // 首次出现的活动页：第一帧按 after 渲染，下一帧再切 active 让 transition 生效
    const activePage = pages.find((page) => page.id === activeId);
    if (activePage && !seenIdsRef.current.has(activePage.id)) {
      // 首次挂载且栈里只有一页：没有可滑动的起点，直接 active
      const skipEnter = wasFirstMount && pages.length === 1;
      if (!skipEnter) {
        setEnteringIds((prev) =>
          prev.includes(activePage.id) ? prev : [...prev, activePage.id]
        );
      }
    }
    for (const id of currentIds) seenIdsRef.current.add(id);
  }, [pagesKey, activeId]);

  // 页面内容变化（同 id 新 node）时刷新快照，出栈时用的才是最后一份
  useEffect(() => {
    previousPagesRef.current = pages;
  });

  // 双 rAF：等浏览器算完 after 那一帧的样式，再切 active 才有过渡。
  useEffect(() => {
    if (enteringIds.length === 0) return;
    const pending = new Set(enteringIds);
    let second = 0;
    const first = window.requestAnimationFrame(() => {
      second = window.requestAnimationFrame(() => {
        setEnteringIds((prev) => prev.filter((id) => !pending.has(id)));
      });
    });
    return () => {
      window.cancelAnimationFrame(first);
      window.cancelAnimationFrame(second);
    };
  }, [enteringIds]);

  // 高度：panel 固定为 computePanelStackHeight，menu 由内容撑开。
  useIsoLayoutEffect(() => {
    if (typeof window === 'undefined') return;
    const container = containerRef.current;
    const previousSize = prevSizeRef.current;
    const previousFillParent = prevFillParentRef.current;
    prevSizeRef.current = size;
    prevFillParentRef.current = fillParent;
    if (!container) return;

    if (fillParent) {
      setInlineHeight(undefined);
      return;
    }

    const panelHeight = computePanelStackHeight(readAppHeight(), readParentChrome(container));

    if (size === 'panel') {
      // 已经是 panel：保留 inline height，不做无谓的重算与过渡
      if (previousSize === 'panel' && previousFillParent === fillParent) return;
      // 深链接直达、从侧栏切回：直接写入，不做过渡
      if (previousSize !== 'menu') {
        setInlineHeight(panelHeight);
        return;
      }
      const measured = container.getBoundingClientRect().height;
      if (!Number.isFinite(measured) || measured <= 0) {
        setInlineHeight(panelHeight);
        return;
      }
      // menu → panel：从当前高度过渡到固定高度，结束后保留 inline height
      setInlineHeight(measured);
      const growRaf = window.requestAnimationFrame(() => setInlineHeight(panelHeight));
      return () => window.cancelAnimationFrame(growRaf);
    }

    // menu：只有从 panel 切回来才需要把固定高度收回去
    if (previousSize !== 'panel') return;
    const measured = container.getBoundingClientRect().height;

    let timer = 0;
    const finish = () => {
      window.clearTimeout(timer);
      container.removeEventListener('transitionend', handleTransitionEnd);
      // 翻页高度会话接管时不要清 inline height，交给它自己收尾
      if (menuHeightSessionRef.current) return;
      setInlineHeight(undefined);
    };
    const handleTransitionEnd = (event: TransitionEvent) => {
      if (event.target === container && event.propertyName === 'height') finish();
    };

    setInlineHeight(measured);
    const shrinkRaf = window.requestAnimationFrame(() => {
      // 容器此刻被锁在 measured：直接量 scrollHeight 读到的是锁定高度，过渡不会动。
      // 先同步把 inline height 交给内容量出自然高度（同帧内还原），再写目标高度。
      const contentHeight = measureNaturalHeight(container);
      const targetHeight =
        contentHeight > 0
          ? Math.min(panelHeight, contentHeight)
          : Math.min(panelHeight, measured);
      setInlineHeight(targetHeight);
      container.addEventListener('transitionend', handleTransitionEnd);
      timer = window.setTimeout(finish, MENU_HEIGHT_FALLBACK_MS);
    });
    return () => {
      window.cancelAnimationFrame(shrinkRaf);
      window.clearTimeout(timer);
      container.removeEventListener('transitionend', handleTransitionEnd);
    };
  }, [size, fillParent]);

  // menu 尺寸内翻页：绘制前先把容器锁在切换前的高度。
  // 入场那两帧里旧页变 before、新页是 absolute 的 after，两页都不撑容器，
  // 不锁高度就会塌成 0、画面闪一下；size 变化仍交给上面的固定高度逻辑。
  useIsoLayoutEffect(() => {
    if (typeof window === 'undefined') return;
    const container = containerRef.current;
    const previousSize = prevHeightSizeRef.current;
    const previousActiveId = prevHeightActiveIdRef.current;
    prevHeightSizeRef.current = size;
    prevHeightActiveIdRef.current = activeId;
    if (!container) return;

    if (size !== 'menu' || fillParent || previousSize !== 'menu') {
      // 退出 menu 或改由父容器撑满：只中断未完成的翻页会话，不写高度
      if (previousSize !== null && (size !== 'menu' || fillParent)) {
        menuHeightSessionRef.current?.cancel();
        menuHeightSessionRef.current = null;
      }
      return;
    }
    if (previousActiveId === activeId) return;

    // 上一轮还在锁/过渡中：先取消它的 rAF、计时器和监听器
    const previousSession = menuHeightSessionRef.current;
    previousSession?.cancel();
    menuHeightSessionRef.current = null;

    // 有未完成的过渡时 inline height 还在容器上，现量得到的就是当前视觉高度；
    // 否则用上次 commit 记录的高度，避免读到本帧新内容已经撑开的高度。
    const measured = previousSession ? container.getBoundingClientRect().height : 0;
    const lockedHeight = measured > 0 ? measured : lastCommitHeightRef.current;
    const sessionActiveId = activeId;
    let frame = 0;
    let timer = 0;
    let cancelled = false;

    const removeTransitionListener = () => {
      container.removeEventListener('transitionend', handleTransitionEnd);
    };
    const cancel = () => {
      cancelled = true;
      window.cancelAnimationFrame(frame);
      window.clearTimeout(timer);
      removeTransitionListener();
    };
    const finish = () => {
      if (cancelled) return;
      window.clearTimeout(timer);
      removeTransitionListener();
      menuHeightSessionRef.current = null;
      // 清掉 inline height，高度重新交给内容撑开
      setInlineHeight(undefined);
    };
    const handleTransitionEnd = (event: TransitionEvent) => {
      if (event.target === container && event.propertyName === 'height') finish();
    };

    menuHeightSessionRef.current = { cancel };

    // layout 阶段写入，浏览器不会画出容器塌陷的那一帧
    if (Number.isFinite(lockedHeight) && lockedHeight > 0) setInlineHeight(lockedHeight);

    const startTransition = () => {
      if (cancelled) return;
      const contentHeight = measureNaturalHeight(container);
      const targetHeight = clampMenuHeight(
        contentHeight > 0 ? contentHeight : container.getBoundingClientRect().height,
        readAppHeight(),
        readParentChrome(container)
      );
      if (!Number.isFinite(targetHeight) || targetHeight <= 0) {
        menuHeightSessionRef.current = null;
        setInlineHeight(undefined);
        return;
      }
      frame = window.requestAnimationFrame(() => {
        if (cancelled) return;
        setInlineHeight(targetHeight);
        container.addEventListener('transitionend', handleTransitionEnd);
        timer = window.setTimeout(finish, MENU_HEIGHT_FALLBACK_MS);
      });
    };

    // 入场期间新页还可能停在 after（不撑容器）：等 enteringIds 里没有它了再量
    const waitForEnter = (framesLeft: number) => {
      frame = window.requestAnimationFrame(() => {
        if (cancelled) return;
        if (framesLeft > 0) {
          waitForEnter(framesLeft - 1);
          return;
        }
        if (enteringIdsRef.current.includes(sessionActiveId)) {
          waitForEnter(0);
          return;
        }
        startTransition();
      });
    };
    waitForEnter(MENU_HEIGHT_ENTER_FRAMES);

    return cancel;
  }, [activeId, size, fillParent]);

  // 记录每次 commit 后容器的真实高度：下次翻页锁高度时用它作为切换前的高度。
  // 过渡进行中不记录，否则会把动画中间值当成稳定高度。
  useIsoLayoutEffect(() => {
    if (typeof window === 'undefined') return;
    const container = containerRef.current;
    if (!container || menuHeightSessionRef.current) return;
    const measured = container.getBoundingClientRect().height;
    if (Number.isFinite(measured) && measured > 0) lastCommitHeightRef.current = measured;
  });

  // panel 的固定高度随窗口尺寸重算；fillParent 时交给父容器。
  useEffect(() => {
    if (typeof window === 'undefined' || fillParent || size !== 'panel') return;
    const handleResize = () => {
      const container = containerRef.current;
      if (!container) return;
      setInlineHeight(computePanelStackHeight(readAppHeight(), readParentChrome(container)));
    };
    window.addEventListener('resize', handleResize);
    return () => window.removeEventListener('resize', handleResize);
  }, [size, fillParent]);

  // 卸载时清掉还没触发的出栈计时器
  useEffect(() => {
    const timers = exitTimersRef.current;
    return () => {
      timers.forEach((timer) => window.clearTimeout(timer));
      timers.clear();
    };
  }, []);

  const containerClassName = ['menu-stack', className].filter(Boolean).join(' ');
  const currentIds = new Set(pages.map((page) => page.id));
  // 出栈首帧：上一帧 pages 里刚消失的页必须继续渲染，否则同一个 DOM 节点会被卸载、滑出动画失效
  const justExitedPages = previousPagesRef.current.filter((page) => !currentIds.has(page.id));
  const exitingForRender = [
    ...justExitedPages,
    ...exitingPages.filter(
      (page) => !currentIds.has(page.id) && !justExitedPages.some((item) => item.id === page.id)
    )
  ];

  return (
    <div
      ref={containerRef}
      className={containerClassName}
      data-size={size}
      data-direction={dataDirection}
      style={inlineHeight === undefined ? undefined : { height: `${inlineHeight}px` }}
    >
      {pages.map((page, index) => {
        const relation = enteringIds.includes(page.id)
          ? 'after'
          : index < activeIndex
            ? 'before'
            : index === activeIndex
              ? 'active'
              : 'after';
        return (
          <section
            key={page.id}
            className="menu-page"
            data-relation={relation}
            aria-hidden={relation !== 'active'}
          >
            {page.node}
          </section>
        );
      })}
      {/* 出栈页沿用原 key，复用同一个 DOM 节点，transform 才能过渡出去 */}
      {exitingForRender.map((page) => (
        <section
          key={page.id}
          className="menu-page"
          data-relation="after"
          aria-hidden="true"
          data-exiting="true"
        >
          {page.node}
        </section>
      ))}
    </div>
  );
}
