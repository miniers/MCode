import { useCallback, useEffect, useRef, useState } from 'react';
import type { ThreadMessage } from '../components/ThreadTimeline';
import { isEndedStatus } from '../components/ThreadTimeline';

export interface UseTurnElapsedSecondsParams {
  threadID: string;
  isRunning: boolean;
  lastUserMessageAt?: string | null;
  messages: ThreadMessage[];
  pendingTurnID?: string | null;
}

export interface TurnIdentity {
  key: string;
  hostStartTimestamp: number | null;
  source: number;
}

function isValidHostTimestamp(t: number): boolean {
  return Number.isFinite(t) && t > 0 && t <= Date.now() + 60_000;
}

export function resolveCurrentTurnIdentity(
  threadID: string,
  messages: ThreadMessage[],
  lastUserMessageAt?: string | null,
  pendingTurnID?: string | null
): TurnIdentity {
  const endedTurnIDs = new Set<string>();
  let latestEndedTimestamp = 0;

  for (let i = 0; i < messages.length; i++) {
    const msg = messages[i];
    if (msg.turnID && (isEndedStatus(msg.turnStatus) || isValidHostTimestamp(Date.parse(msg.turnCompletedAt ?? '')))) {
      endedTurnIDs.add(msg.turnID);
    }
    if (isEndedStatus(msg.turnStatus) || isValidHostTimestamp(Date.parse(msg.turnCompletedAt ?? ''))) {
      const t = Date.parse(msg.turnCompletedAt ?? msg.createdAt);
      if (isValidHostTimestamp(t) && t > latestEndedTimestamp) {
        latestEndedTimestamp = t;
      }
    }
  }

  let activeTurnID: string | null = pendingTurnID && !endedTurnIDs.has(pendingTurnID) ? pendingTurnID : null;
  for (let i = messages.length - 1; !activeTurnID && i >= 0; i--) {
    const msg = messages[i];
    if (msg.turnID) {
      if (!endedTurnIDs.has(msg.turnID)) {
        activeTurnID = msg.turnID;
      }
      break;
    }
  }

  if (activeTurnID) {
    const turnMsgs = messages.filter((m) => m.turnID === activeTurnID);
    let hostTime: number | null = null;
    let source = 0;

    for (const m of turnMsgs) {
      const t = Date.parse(m.turnStartedAt ?? '');
      if (isValidHostTimestamp(t) && (hostTime === null || t < hostTime)) hostTime = t;
    }
    if (hostTime !== null) source = 2;

    const userMsgs = turnMsgs.filter((m) => m.role === 'user' || m.kind === 'user');
    for (const m of hostTime === null ? userMsgs : []) {
      const t = Date.parse(m.createdAt);
      if (isValidHostTimestamp(t) && (hostTime === null || t < hostTime)) {
        hostTime = t;
      }
    }

    if (hostTime !== null && source === 0) source = 1;

    if (hostTime === null && turnMsgs.length > 0) {
      for (const m of turnMsgs) {
        const t = Date.parse(m.createdAt);
        if (isValidHostTimestamp(t) && (hostTime === null || t < hostTime)) {
          hostTime = t;
        }
      }
    }

    if (hostTime === null && lastUserMessageAt) {
      const t = Date.parse(lastUserMessageAt);
      if (isValidHostTimestamp(t) && t >= latestEndedTimestamp) {
        hostTime = t;
      }
    }

    if (hostTime !== null && source === 0) source = 1;

    return {
      key: `turn:${activeTurnID}`,
      hostStartTimestamp: hostTime,
      source
    };
  }

  const latestUserMsg = [...messages].reverse().find((m) => m.role === 'user' || m.kind === 'user');
  if (latestUserMsg) {
    const t = Date.parse(latestUserMsg.createdAt);
    if (isValidHostTimestamp(t) && t >= latestEndedTimestamp) {
      return {
        key: `msg:${latestUserMsg.id}`,
        hostStartTimestamp: t,
        source: 1
      };
    }
  }

  if (lastUserMessageAt) {
    const t = Date.parse(lastUserMessageAt);
    if (isValidHostTimestamp(t) && t >= latestEndedTimestamp) {
      return {
        key: `lum:${lastUserMessageAt}`,
        hostStartTimestamp: t,
        source: 1
      };
    }
  }

  return {
    key: `thread:${threadID}:running-fallback`,
    hostStartTimestamp: null,
    source: 0
  };
}

export function useTurnElapsedSeconds({
  threadID,
  isRunning,
  lastUserMessageAt,
  messages,
  pendingTurnID
}: UseTurnElapsedSecondsParams): number {
  const [elapsedSeconds, setElapsedSeconds] = useState(0);

  const trackerRef = useRef<{
    threadID: string;
    turnKey: string | null;
    startTimestamp: number | null;
    source: number;
  }>({
    threadID,
    turnKey: null,
    startTimestamp: null,
    source: 0
  });

  const tick = useCallback(() => {
    const start = trackerRef.current.startTimestamp;
    if (!start) {
      setElapsedSeconds(0);
      return;
    }
    const sec = Math.max(0, Math.floor((Date.now() - start) / 1000));
    setElapsedSeconds(sec);
  }, []);

  useEffect(() => {
    if (trackerRef.current.threadID !== threadID) {
      trackerRef.current = {
        threadID,
        turnKey: null,
        startTimestamp: null,
        source: 0
      };
      setElapsedSeconds(0);
    }

    if (!isRunning) {
      if (trackerRef.current.turnKey !== null || trackerRef.current.startTimestamp !== null) {
        trackerRef.current.turnKey = null;
        trackerRef.current.startTimestamp = null;
        trackerRef.current.source = 0;
        setElapsedSeconds(0);
      }
      return;
    }

    const identity = resolveCurrentTurnIdentity(threadID, messages, lastUserMessageAt, pendingTurnID);
    const prev = trackerRef.current;
    let anchorChanged = false;

    if (prev.turnKey === null) {
      trackerRef.current = {
        threadID,
        turnKey: identity.key,
        startTimestamp: identity.hostStartTimestamp ?? Date.now(),
        source: identity.source
      };
      anchorChanged = true;
    } else if (prev.turnKey === identity.key) {
      if (identity.source > prev.source && identity.hostStartTimestamp !== null) {
        trackerRef.current.startTimestamp = identity.hostStartTimestamp;
        trackerRef.current.source = identity.source;
        anchorChanged = true;
      }
    } else {
      const isUpgradeFromFallback = prev.turnKey === `thread:${threadID}:running-fallback`;
      if (isUpgradeFromFallback) {
        trackerRef.current.turnKey = identity.key;
        if (identity.hostStartTimestamp !== null) {
          trackerRef.current.startTimestamp = identity.hostStartTimestamp;
          trackerRef.current.source = identity.source;
          anchorChanged = true;
        }
      } else {
        trackerRef.current = {
          threadID,
          turnKey: identity.key,
          startTimestamp: identity.hostStartTimestamp ?? Date.now(),
          source: identity.source
        };
        anchorChanged = true;
      }
    }

    if (anchorChanged) {
      tick();
    }
  }, [threadID, isRunning, lastUserMessageAt, messages, pendingTurnID, tick]);

  useEffect(() => {
    if (!isRunning) return;

    tick();
    const interval = window.setInterval(tick, 1000);

    const handleVisibilityChange = () => {
      if (document.visibilityState === 'visible') tick();
    };
    const handlePageShow = () => tick();
    const handleFocus = () => tick();

    document.addEventListener('visibilitychange', handleVisibilityChange);
    window.addEventListener('pageshow', handlePageShow);
    window.addEventListener('focus', handleFocus);

    return () => {
      window.clearInterval(interval);
      document.removeEventListener('visibilitychange', handleVisibilityChange);
      window.removeEventListener('pageshow', handlePageShow);
      window.removeEventListener('focus', handleFocus);
    };
  }, [isRunning, tick]);

  return isRunning ? elapsedSeconds : 0;
}
