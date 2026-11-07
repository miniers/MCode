// 进入会话时贴底跟随：内容高度变化保持底边，用户上滑离底暂停，滚回底部恢复。

import { useCallback, useEffect, useLayoutEffect, useRef } from 'react';

// 贴底参数：用户上滑离底超过 PIN_USER_SCROLL_DISTANCE 即暂停跟随；滚回距底 PIN_RESUME_DISTANCE 以内则恢复。
const PIN_USER_SCROLL_DISTANCE = 80;
const PIN_RESUME_DISTANCE = 24;

export function useInitialPinToLatest(
  threadID: string,
  scrollRef: { current: HTMLDivElement | null },
  contentRef: { current: HTMLDivElement | null },
  contentVersion: unknown,
  // 发送后锚定期间置为 true：暂停贴底跟随，也不允许被滚动事件自动恢复，否则会和锚定抢位置
  suspendedRef?: { current: boolean }
): { end: () => void; resume: () => void } {
  const activeRef = useRef(false);
  const lastScrollTopRef = useRef(0);
  const observerRef = useRef<ResizeObserver | null>(null);

  const end = useCallback(() => {
    activeRef.current = false;
    observerRef.current?.disconnect();
    observerRef.current = null;
  }, []);

  const pin = useCallback(() => {
    if (!activeRef.current) return;
    if (suspendedRef?.current) return;
    const element = scrollRef.current;
    if (!element) return;
    if (element.scrollHeight > element.clientHeight) {
      element.scrollTop = element.scrollHeight;
    }
    lastScrollTopRef.current = element.scrollTop;
  }, [scrollRef, suspendedRef]);

  const arm = useCallback(() => {
    activeRef.current = true;
    lastScrollTopRef.current = scrollRef.current?.scrollTop ?? 0;
    if (typeof ResizeObserver !== 'undefined' && !observerRef.current) {
      const observer = new ResizeObserver(pin);
      if (contentRef.current) observer.observe(contentRef.current);
      if (scrollRef.current) observer.observe(scrollRef.current);
      observerRef.current = observer;
    }
    pin();
  }, [contentRef, pin, scrollRef]);

  // 进入会话（更换 threadID）时开始贴底；异步 Markdown/图片高度变化通过 ResizeObserver 保持底边。
  // 用户主动上滑离底超过 80px 暂停跟随，滚回底部（含点击「跳到最新」）后恢复，流式输出继续跟随。
  useLayoutEffect(() => {
    arm();
    return end;
  }, [threadID, arm, end]);

  useLayoutEffect(() => {
    pin();
  }, [contentVersion, pin]);

  useEffect(() => {
    const element = scrollRef.current;
    if (!element) return;
    const onScroll = () => {
      if (suspendedRef?.current) return;
      const currentScrollTop = element.scrollTop;
      const distanceFromBottom = element.scrollHeight - currentScrollTop - element.clientHeight;
      if (!activeRef.current) {
        if (distanceFromBottom <= PIN_RESUME_DISTANCE) arm();
        return;
      }
      const previousScrollTop = lastScrollTopRef.current;
      lastScrollTopRef.current = currentScrollTop;
      if (currentScrollTop < previousScrollTop && distanceFromBottom > PIN_USER_SCROLL_DISTANCE) {
        end();
      }
    };
    element.addEventListener('scroll', onScroll, { passive: true });
    return () => {
      element.removeEventListener('scroll', onScroll);
      end();
    };
  }, [arm, end, scrollRef, suspendedRef]);

  return { end, resume: arm };
}
