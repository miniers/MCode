import { useCallback, useEffect, useRef, useState } from 'react';
import { flushSync } from 'react-dom';
import { resolveNavDirection, shouldSkipNavTransition, startMobilePageTransition, type NavDirection } from '../hooks/mobileNavTransition';
import { parseThreadRoute } from '../utils/threadRoute';
import { usesSplitLayout } from '../utils/splitLayout';

export type Route =
  | { kind: 'home' }
  | { kind: 'board' }
  | { kind: 'automations'; automationID?: string }
  | { kind: 'pr'; number?: number }
  | { kind: 'settings'; page?: string }
  | { kind: 'draft'; projectID: string }
  | {
      kind: 'thread';
      threadID: string;
      subview?: 'agents' | 'review' | 'git' | 'files' | 'scripts' | 'context' | 'logs';
      filePath?: string;
      /** 推送点开时要定位的消息与审批请求，仅用于一次性跳转。 */
      messageID?: string;
      requestID?: string;
      /** 持续状态参数：打开的设计稿相对路径。 */
      design?: string;
    }
  | { kind: 'task'; taskID: string }
  | { kind: 'goal'; goalID: string };

export function parseRoute(hash: string): Route {
  const value = hash.replace(/^#/, '') || '/';
  if (value === '/board') return { kind: 'board' };
  if (value === '/automations') return { kind: 'automations' };
  const automationMatch = value.match(/^\/automations\/([^/]+)$/);
  if (automationMatch) {
    try {
      return { kind: 'automations', automationID: decodeURIComponent(automationMatch[1]) };
    } catch {
      return { kind: 'automations' };
    }
  }
  if (value === '/pr') return { kind: 'pr' };
  const prMatch = value.match(/^\/pr\/([1-9]\d*)$/);
  if (prMatch) {
    return { kind: 'pr', number: Number.parseInt(prMatch[1], 10) };
  }
  if (value === '/settings') return { kind: 'settings' };
  // #/settings/<页面 id>：直接打开某个 tab，tab 切换用 replace 写，不压浏览历史。
  const settingsPageMatch = value.match(/^\/settings\/([^/]+)$/);
  if (settingsPageMatch) {
    try {
      return { kind: 'settings', page: decodeURIComponent(settingsPageMatch[1]) };
    } catch {
      return { kind: 'settings' };
    }
  }
  const draftMatch = value.match(/^\/new\/([^/]+)$/);
  if (draftMatch) {
    try {
      return { kind: 'draft', projectID: decodeURIComponent(draftMatch[1]) };
    } catch {
      return { kind: 'home' };
    }
  }
  const taskMatch = value.match(/^\/task\/([^/]+)$/);
  if (taskMatch) {
    try {
      return { kind: 'task', taskID: decodeURIComponent(taskMatch[1]) };
    } catch {
      return { kind: 'home' };
    }
  }
  const goalMatch = value.match(/^\/goal\/([^/]+)$/);
  if (goalMatch) {
    try {
      return { kind: 'goal', goalID: decodeURIComponent(goalMatch[1]) };
    } catch {
      return { kind: 'home' };
    }
  }
  const agentsMatch = value.match(/^\/t\/([^/]+)\/agents$/);
  if (agentsMatch) {
    try {
      return { kind: 'thread', threadID: decodeURIComponent(agentsMatch[1]), subview: 'agents' };
    } catch {
      return { kind: 'home' };
    }
  }
  const reviewMatch = value.match(/^\/t\/([^/]+)\/review$/);
  if (reviewMatch) {
    try {
      return { kind: 'thread', threadID: decodeURIComponent(reviewMatch[1]), subview: 'review' };
    } catch {
      return { kind: 'home' };
    }
  }
  const workspaceMatch = value.match(/^\/t\/([^/]+)\/(git|files|scripts|context|logs)(?:\/([^/]+))?$/);
  if (workspaceMatch && (workspaceMatch[2] === 'files' || !workspaceMatch[3])) {
    try {
      return {
        kind: 'thread',
        threadID: decodeURIComponent(workspaceMatch[1]),
        subview: workspaceMatch[2] as 'git' | 'files' | 'scripts' | 'context' | 'logs',
        filePath: workspaceMatch[3] ? decodeURIComponent(workspaceMatch[3]) : undefined
      };
    } catch {
      return { kind: 'home' };
    }
  }
  // 推送点开：#/t/<id>?m=<messageID>&r=<requestID>，查询参数只负责定位。
  const threadRoute = parseThreadRoute(value);
  if (threadRoute) {
    return {
      kind: 'thread',
      threadID: threadRoute.threadID,
      messageID: threadRoute.messageID,
      requestID: threadRoute.requestID,
      design: threadRoute.design
    };
  }
  return { kind: 'home' };
}

function currentDepth(): number {
  const state = window.history.state;
  if (
    state &&
    typeof state === 'object' &&
    typeof state.mcodeDepth === 'number' &&
    Number.isInteger(state.mcodeDepth) &&
    state.mcodeDepth >= 0
  ) {
    return state.mcodeDepth;
  }
  return 0;
}

export function useHashRoute(): [
  Route,
  (hash: string, options?: { replace?: boolean }) => void,
  (fallback: string, options?: { skipTransition?: boolean }) => void,
  (hash: string) => void
] {
  const [route, setRoute] = useState<Route>(() => parseRoute(window.location.hash));
  const routeRef = useRef(route);
  const hashRef = useRef(window.location.hash);
  const skipTransitionRef = useRef(false);
  const transitionRef = useRef<ViewTransition | null>(null);

  const commitRoute = useCallback(
    (hash: string, direction: NavDirection, options?: { skipTransition?: boolean }) => {
      const next = parseRoute(hash);
      const prev = routeRef.current;
      const apply = () => {
        routeRef.current = next;
        hashRef.current = hash;
        setRoute(next);
      };
      // 同时只跑一次过场：上一段还没结束就直接换页，避免动画叠在一起。
      if (options?.skipTransition || transitionRef.current || shouldSkipNavTransition(prev, next) ||
          (window.matchMedia?.('(min-width: 900px)').matches && usesSplitLayout(true, prev.kind) && usesSplitLayout(true, next.kind))) {
        apply();
        return;
      }
      const transition = startMobilePageTransition(direction, () => flushSync(apply));
      if (!transition) return;
      transitionRef.current = transition;
      void transition.finished
        .catch(() => undefined)
        .finally(() => {
          if (transitionRef.current === transition) transitionRef.current = null;
        });
    },
    []
  );

  useEffect(() => {
    const sync = () => {
      const hash = window.location.hash;
      if (hash === hashRef.current) return;
      const direction = resolveNavDirection(routeRef.current, parseRoute(hash));
      const skipTransition = skipTransitionRef.current;
      skipTransitionRef.current = false;
      commitRoute(hash, direction, { skipTransition });
    };
    window.addEventListener('hashchange', sync);
    window.addEventListener('popstate', sync);
    return () => {
      window.removeEventListener('hashchange', sync);
      window.removeEventListener('popstate', sync);
    };
  }, [commitRoute]);

  const navigate = (hash: string, options?: { replace?: boolean }) => {
    if (window.location.hash === hash) {
      const next = parseRoute(hash);
      routeRef.current = next;
      hashRef.current = hash;
      setRoute(next);
      return;
    }
    const direction = resolveNavDirection(routeRef.current, parseRoute(hash), options?.replace);
    if (options?.replace) {
      window.history.replaceState({ mcodeDepth: currentDepth() }, '', hash);
    } else {
      window.history.pushState({ mcodeDepth: currentDepth() + 1 }, '', hash);
    }
    commitRoute(hash, direction);
  };

  const goBack = (fallback: string, options?: { skipTransition?: boolean }) => {
    if (currentDepth() > 0) {
      if (options?.skipTransition) {
        // 边缘滑回已经在做位移动画，popstate 到达时不要再叠一次 View Transition。
        skipTransitionRef.current = true;
        window.setTimeout(() => {
          skipTransitionRef.current = false;
        }, 600);
      }
      window.history.back();
      return;
    }
    if (window.location.hash === fallback) return;
    window.history.replaceState({ mcodeDepth: currentDepth() }, '', fallback);
    commitRoute(fallback, 'pop', { skipTransition: options?.skipTransition });
  };

  // 清掉 hash 上的查询参数（推送定位用完即弃）：不压历史、不触发 hashchange、不走过场。
  const replaceHash = useCallback((hash: string) => {
    if (window.location.hash === hash) return;
    window.history.replaceState({ mcodeDepth: currentDepth() }, '', hash);
    hashRef.current = hash;
  }, []);

  return [route, navigate, goBack, replaceHash];
}

