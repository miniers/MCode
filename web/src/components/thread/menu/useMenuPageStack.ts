// 菜单页面栈：纯函数负责 push/pop，hook 负责方向、重置与栈底判定。
// id 用模块内自增计数器，保证 React key 稳定且便于测试。

import { useCallback, useEffect, useRef, useState } from 'react';
import type { ThreadMenuPage, ThreadMenuPageSpec } from './menuPages';

export type ThreadMenuDirection = 'forward' | 'back';

let nextPageId = 0;

function allocatePageId(): string {
  nextPageId += 1;
  return `page-${nextPageId}`;
}

export function createBasePage(spec: ThreadMenuPageSpec): ThreadMenuPage {
  const page: ThreadMenuPage = { id: allocatePageId(), kind: spec.kind };
  if (spec.filePath !== undefined) page.filePath = spec.filePath;
  return page;
}

export function pushMenuPage(stack: ThreadMenuPage[], spec: ThreadMenuPageSpec): ThreadMenuPage[] {
  return [...stack, createBasePage(spec)];
}

export function popMenuPage(stack: ThreadMenuPage[]): ThreadMenuPage[] {
  if (stack.length <= 1) return stack;
  return stack.slice(0, -1);
}

/** 重置为单个栈底页，并生成新的 id。 */
export function resetMenuPage(spec: ThreadMenuPageSpec): ThreadMenuPage[] {
  return [createBasePage(spec)];
}

export interface UseMenuPageStackOptions {
  /** 栈底页；由路由 subview 决定 */
  base: ThreadMenuPageSpec;
  /** 变化时重置栈；会话切换用 thread.id */
  resetKey: string;
}

export interface UseMenuPageStackResult {
  stack: ThreadMenuPage[];
  direction: ThreadMenuDirection;
  isAtBase: boolean;
  push: (spec: ThreadMenuPageSpec) => void;
  /** 栈底时返回 false 且不修改栈 */
  pop: () => boolean;
  reset: (spec?: ThreadMenuPageSpec) => void;
}

export function useMenuPageStack({ base, resetKey }: UseMenuPageStackOptions): UseMenuPageStackResult {
  const [stack, setStack] = useState<ThreadMenuPage[]>(() => [createBasePage(base)]);
  const [direction, setDirection] = useState<ThreadMenuDirection>('forward');
  const stackRef = useRef(stack);
  stackRef.current = stack;
  const baseRef = useRef(base);
  baseRef.current = base;
  const lastResetKeyRef = useRef(resetKey);

  const apply = useCallback((next: ThreadMenuPage[], nextDirection: ThreadMenuDirection) => {
    stackRef.current = next;
    setStack(next);
    setDirection(nextDirection);
  }, []);

  const push = useCallback(
    (spec: ThreadMenuPageSpec) => {
      apply(pushMenuPage(stackRef.current, spec), 'forward');
    },
    [apply]
  );

  const pop = useCallback((): boolean => {
    if (stackRef.current.length <= 1) return false;
    apply(popMenuPage(stackRef.current), 'back');
    return true;
  }, [apply]);

  const reset = useCallback(
    (spec?: ThreadMenuPageSpec) => {
      apply(resetMenuPage(spec ?? baseRef.current), 'forward');
    },
    [apply]
  );

  useEffect(() => {
    if (lastResetKeyRef.current === resetKey) return;
    lastResetKeyRef.current = resetKey;
    reset();
  }, [resetKey, reset]);

  return {
    stack,
    direction,
    isAtBase: stack.length <= 1,
    push,
    pop,
    reset
  };
}
