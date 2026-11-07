import { useCallback, useEffect, useRef, useState } from 'react';
import type { McodeRpcClient } from '../services/rpc';
import type { RemoteCapabilityDeclaration, RemoteThreadsCommandsResult } from '../types/contract';
import { commandScopeKey } from '../utils/slashCommands';
import type { CommandScope } from '../utils/slashCommands';

interface Ref<T> {
  current: T;
}

// 宿主在后台拉取命令表时 loading 为 true；稍后重新请求，最多重试几次。
const COMMANDS_RETRY_DELAY_MS = 1500;
const COMMANDS_MAX_RETRIES = 4;
// 命令表（skill 等）变化不频繁，进入输入区时已缓存且较新的结果直接复用。
const COMMANDS_FRESH_MS = 5 * 60 * 1000;

interface UseRemoteCommandsOptions {
  clientRef: Ref<McodeRpcClient | null>;
  capabilities: RemoteCapabilityDeclaration | null;
}

/**
 * `/` 斜杠命令清单：按 scope（threadID 或 projectID+provider）缓存，
 * 进入输入区时拉一次；宿主仍在后台拉取（loading）时重试；换连接时作废。
 */
export function useRemoteCommands({ clientRef, capabilities }: UseRemoteCommandsOptions) {
  const [commandSnapshots, setCommandSnapshots] = useState<Record<string, RemoteThreadsCommandsResult>>({});
  const snapshotsRef = useRef<Record<string, RemoteThreadsCommandsResult>>({});
  const fetchedAtRef = useRef(new Map<string, number>());
  const inflightRef = useRef(new Map<string, Promise<RemoteThreadsCommandsResult>>());
  const retryTimersRef = useRef(new Map<string, number>());
  const generationRef = useRef(0);
  const supportedRef = useRef(false);
  supportedRef.current = Boolean(
    capabilities?.features?.includes('threads.commands') &&
    capabilities.methods.includes('threads.commands')
  );

  const clearTimers = useCallback(() => {
    for (const timer of retryTimersRef.current.values()) window.clearTimeout(timer);
    retryTimersRef.current.clear();
  }, []);

  const reset = useCallback(() => {
    generationRef.current++;
    clearTimers();
    inflightRef.current.clear();
    fetchedAtRef.current.clear();
    snapshotsRef.current = {};
    setCommandSnapshots({});
  }, [clearTimers]);

  useEffect(() => clearTimers, [clearTimers]);

  const fetchCommands = useCallback(
    (scope: CommandScope, attempt: number): Promise<RemoteThreadsCommandsResult> => {
      const key = commandScopeKey(scope);
      const client = clientRef.current;
      if (!client) return Promise.reject(new Error('未连接到 Mac 端，无法读取命令列表'));
      const generation = generationRef.current;
      const task = (async () => {
        const result = await client.listThreadCommands(scope);
        if (clientRef.current !== client || generationRef.current !== generation) {
          throw new Error('连接已更换，请重新读取命令列表');
        }
        snapshotsRef.current = { ...snapshotsRef.current, [key]: result };
        fetchedAtRef.current.set(key, Date.now());
        setCommandSnapshots(snapshotsRef.current);
        if (result.loading && attempt < COMMANDS_MAX_RETRIES) {
          const previous = retryTimersRef.current.get(key);
          if (previous !== undefined) window.clearTimeout(previous);
          const timer = window.setTimeout(() => {
            retryTimersRef.current.delete(key);
            if (generationRef.current !== generation || clientRef.current !== client) return;
            void fetchCommands(scope, attempt + 1).catch(() => undefined);
          }, COMMANDS_RETRY_DELAY_MS);
          retryTimersRef.current.set(key, timer);
        }
        return result;
      })();
      inflightRef.current.set(key, task);
      const clearInflight = () => {
        if (inflightRef.current.get(key) === task) inflightRef.current.delete(key);
      };
      task.then(clearInflight, clearInflight);
      return task;
    },
    [clientRef]
  );

  const loadThreadCommands = useCallback(
    async (scope: CommandScope, force = false): Promise<RemoteThreadsCommandsResult> => {
      if (!supportedRef.current) throw new Error('当前宿主未声明 threads.commands 能力');
      const key = commandScopeKey(scope);
      const cached = snapshotsRef.current[key];
      const fetchedAt = fetchedAtRef.current.get(key) ?? 0;
      if (
        cached && !force && !cached.loading && !cached.error &&
        Date.now() - fetchedAt < COMMANDS_FRESH_MS
      ) {
        return cached;
      }
      const inflight = inflightRef.current.get(key);
      if (inflight) return inflight;
      return fetchCommands(scope, 0);
    },
    [fetchCommands]
  );

  return { commandSnapshots, loadThreadCommands, reset };
}
