// 实时终端/命令日志订阅流 Hook：先注册 logs.event 通知再调用 logs.subscribe 拉取快照，
// 按 chunk.seq 去重增量追加（客户端最多保留 1000 条），支持断线重连自动拉齐与卸载退订。

import { useCallback, useEffect, useRef, useState } from 'react';
import { useRemoteMeta } from '../context/RemoteContext';
import { useRemoteCall } from '../context/useRemoteCall';
import type {
  RemoteLogChunk,
  RemoteLogStreamInfo,
  RemoteLogsEventNotificationParams,
  RemoteLogsSubscribeParams,
  RemoteLogsSubscribeResult,
  RemoteLogsUnsubscribeParams
} from '../types/generated';

export const MAX_LOG_CHUNKS = 1000;

export interface UseLiveLogStreamResult {
  streams: RemoteLogStreamInfo[];
  chunks: RemoteLogChunk[];
  truncated: boolean;
  loading: boolean;
  error: string | null;
  supported: boolean;
  clearLocal: () => void;
  resubscribe: () => void;
}

export function mergeLogStream(
  streams: readonly RemoteLogStreamInfo[],
  incoming: RemoteLogStreamInfo
): RemoteLogStreamInfo[] {
  const index = streams.findIndex((item) => item.streamID === incoming.streamID);
  if (index < 0) return [...streams, incoming];
  const next = streams.slice();
  next[index] = incoming;
  return next;
}

export function appendBoundedLogChunks(
  existing: readonly RemoteLogChunk[],
  incoming: readonly RemoteLogChunk[],
  lastSeq: number,
  maxChunks = MAX_LOG_CHUNKS
): { chunks: RemoteLogChunk[]; lastSeq: number; trimmed: boolean } {
  let nextLastSeq = lastSeq;
  const fresh: RemoteLogChunk[] = [];
  for (const chunk of incoming) {
    if (!chunk || typeof chunk.seq !== 'number') continue;
    if (chunk.seq > nextLastSeq) {
      nextLastSeq = chunk.seq;
      fresh.push(chunk);
    }
  }
  if (fresh.length === 0) {
    return { chunks: existing as RemoteLogChunk[], lastSeq: nextLastSeq, trimmed: false };
  }
  const merged = [...existing, ...fresh];
  if (merged.length > maxChunks) {
    return {
      chunks: merged.slice(merged.length - maxChunks),
      lastSeq: nextLastSeq,
      trimmed: true
    };
  }
  return { chunks: merged, lastSeq: nextLastSeq, trimmed: false };
}

function clampLogChunks(
  chunks: readonly RemoteLogChunk[],
  maxChunks = MAX_LOG_CHUNKS
): { chunks: RemoteLogChunk[]; trimmed: boolean } {
  if (chunks.length <= maxChunks) {
    return { chunks: chunks.slice(), trimmed: false };
  }
  return { chunks: chunks.slice(chunks.length - maxChunks), trimmed: true };
}

export function useLiveLogStream(
  threadID: string | null,
  enabled: boolean
): UseLiveLogStreamResult {
  const { call, supportsMethod, onNotification } = useRemoteCall();
  const { connectionState } = useRemoteMeta();
  const supported = supportsMethod('logs.subscribe');
  const isReady = connectionState === 'connected';

  const [streams, setStreams] = useState<RemoteLogStreamInfo[]>([]);
  const [chunks, setChunks] = useState<RemoteLogChunk[]>([]);
  const [truncated, setTruncated] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [resubscribeToken, setResubscribeToken] = useState(0);

  const lastSeqRef = useRef(-1);
  const activeThreadRef = useRef<string | null>(threadID);

  // 切换会话时清空旧流与日志块。
  useEffect(() => {
    activeThreadRef.current = threadID;
    lastSeqRef.current = -1;
    setStreams([]);
    setChunks([]);
    setTruncated(false);
    setError(null);
    setLoading(false);
  }, [threadID]);

  useEffect(() => {
    if (!enabled || !isReady || !threadID || !supported) {
      setLoading(false);
      return;
    }

    let cancelled = false;
    let snapshotReady = false;
    const pendingEvents: RemoteLogsEventNotificationParams[] = [];

    const applyEvent = (params: RemoteLogsEventNotificationParams) => {
      if (params.stream) {
        const nextStream = params.stream;
        setStreams((prev) => mergeLogStream(prev, nextStream));
      }
      if (Array.isArray(params.chunks) && params.chunks.length > 0) {
        let trimmedNow = false;
        setChunks((prev) => {
          const applied = appendBoundedLogChunks(prev, params.chunks, lastSeqRef.current);
          lastSeqRef.current = applied.lastSeq;
          trimmedNow = applied.trimmed;
          return applied.chunks;
        });
        if (trimmedNow) {
          setTruncated(true);
        }
      }
    };

    const detachNotification = onNotification('logs.event', (raw) => {
      if (cancelled) return;
      if (!raw || typeof raw !== 'object') return;
      const params = raw as RemoteLogsEventNotificationParams;
      if (params.threadID !== threadID || activeThreadRef.current !== threadID) return;
      if (!snapshotReady) {
        pendingEvents.push(params);
        return;
      }
      applyEvent(params);
    });

    setLoading(true);
    setError(null);

    const subscribeParams: RemoteLogsSubscribeParams = { threadID };
    void call<RemoteLogsSubscribeResult>('logs.subscribe', subscribeParams)
      .then((result) => {
        if (cancelled || activeThreadRef.current !== threadID) return;
        snapshotReady = true;
        const snapshotStreams = Array.isArray(result?.streams) ? result.streams : [];
        const snapshotChunks = Array.isArray(result?.chunks) ? result.chunks : [];
        let maxSeq = typeof result?.sequence === 'number' ? result.sequence : -1;
        for (const chunk of snapshotChunks) {
          if (chunk && typeof chunk.seq === 'number' && chunk.seq > maxSeq) {
            maxSeq = chunk.seq;
          }
        }
        lastSeqRef.current = maxSeq;

        const clamped = clampLogChunks(snapshotChunks);
        let nextStreams = snapshotStreams;
        let nextChunks = clamped.chunks;
        let nextTruncated = Boolean(result?.truncated) || clamped.trimmed;

        for (const pending of pendingEvents) {
          if (pending.stream) {
            nextStreams = mergeLogStream(nextStreams, pending.stream);
          }
          if (Array.isArray(pending.chunks) && pending.chunks.length > 0) {
            const applied = appendBoundedLogChunks(nextChunks, pending.chunks, lastSeqRef.current);
            lastSeqRef.current = applied.lastSeq;
            nextChunks = applied.chunks;
            if (applied.trimmed) nextTruncated = true;
          }
        }
        pendingEvents.length = 0;

        setStreams(nextStreams);
        setChunks(nextChunks);
        setTruncated(nextTruncated);
        setError(null);
      })
      .catch((err) => {
        if (cancelled || activeThreadRef.current !== threadID) return;
        setError(err instanceof Error ? err.message : '订阅实时日志失败');
      })
      .finally(() => {
        if (!cancelled && activeThreadRef.current === threadID) {
          setLoading(false);
        }
      });

    return () => {
      cancelled = true;
      detachNotification();
      const unsubscribeParams: RemoteLogsUnsubscribeParams = { threadID };
      void call('logs.unsubscribe', unsubscribeParams).catch(() => {});
    };
  }, [call, enabled, isReady, onNotification, resubscribeToken, supported, threadID]);

  const clearLocal = useCallback(() => {
    setChunks([]);
  }, []);

  const resubscribe = useCallback(() => {
    setResubscribeToken((token) => token + 1);
  }, []);

  return {
    streams,
    chunks,
    truncated,
    loading,
    error,
    supported,
    clearLocal,
    resubscribe
  };
}
