// 重启中断续跑横幅：桌面端在编排区域给出「继续运行 / 放弃」，Web 端此前看不到这类中断。
// 计数不在 threads.swarm.get 的载荷里（wire.SwarmState 没有 interrupted 字段），
// 所以这里单独调 threads.swarm.interrupted，并在收到 threads.swarm.changed 后重新拉取。

import React, { useCallback, useEffect, useState } from 'react';
import { useRemoteNotification } from '../context/useRemoteCall';
import type { RemoteSwarmExtInterruptedBee } from '../context/remoteSwarmExtApi';
import { useRemoteSwarmExtApi } from '../context/remoteSwarmExtApi';

const SWARM_CHANGED_METHOD = 'threads.swarm.changed';

/** 横幅上最多并列几个子代理名字，多了只显示计数。 */
const MAX_VISIBLE_NAMES = 3;

interface SwarmInterruptedBannerProps {
  /** 主会话线程 ID；传 bee 线程也可以，宿主会归一成所属主会话。 */
  threadID: string;
}

export const SwarmInterruptedBanner: React.FC<SwarmInterruptedBannerProps> = ({ threadID }) => {
  const api = useRemoteSwarmExtApi();
  const [count, setCount] = useState(0);
  const [bees, setBees] = useState<RemoteSwarmExtInterruptedBee[]>([]);
  const [pending, setPending] = useState<'resume' | 'dismiss' | null>(null);
  const [error, setError] = useState('');

  const enabled = api.supportsInterrupted && Boolean(threadID);

  const refresh = useCallback(async () => {
    if (!enabled) return;
    try {
      const result = await api.fetchInterrupted(threadID);
      setCount(typeof result.count === 'number' && result.count > 0 ? result.count : 0);
      setBees(result.bees ?? []);
    } catch {
      // 查询失败不在面板里报错：横幅只是附加提示，bee 列表照常可用。
    }
  }, [api, enabled, threadID]);

  // 面板一打开就拉一次
  useEffect(() => {
    void refresh();
  }, [refresh]);

  // 宿主在续跑、放弃或其它端操作后会推 threads.swarm.changed，据此刷新计数
  useRemoteNotification(SWARM_CHANGED_METHOD, () => void refresh(), enabled);

  if (!enabled || count <= 0) return null;

  const names = bees
    .slice(0, MAX_VISIBLE_NAMES)
    .map((bee) => bee.name || bee.task)
    .filter(Boolean)
    .join('、');

  const runAction = async (kind: 'resume' | 'dismiss') => {
    if (pending) return;
    setPending(kind);
    setError('');
    try {
      if (kind === 'resume') {
        await api.resumeInterrupted(threadID);
      } else {
        await api.dismissInterrupted(threadID);
      }
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : '操作失败，请稍后重试');
    } finally {
      setPending(null);
    }
  };

  return (
    <div
      role="status"
      className="mb-3 rounded-xl border border-amber-200 dark:border-amber-900/40 bg-amber-50 dark:bg-amber-950/20 px-3 py-2.5 space-y-2"
    >
      <p className="text-xs font-medium text-amber-700 dark:text-amber-300">
        {`${count} 个子代理在重启时中断`}
      </p>
      {names && (
        <p className="truncate text-mc-caption text-[var(--foreground-muted)]">{names}</p>
      )}
      <div className="flex flex-wrap items-center gap-2">
        {api.supportsResume && (
          <button
            type="button"
            onClick={() => void runAction('resume')}
            disabled={pending !== null}
            className="min-h-[32px] px-3 rounded-xl text-xs font-medium border border-[var(--border)] bg-[var(--card)] text-[var(--foreground)] hover:bg-[var(--border)] disabled:opacity-45 transition"
          >
            {pending === 'resume' ? '正在继续…' : '继续运行'}
          </button>
        )}
        {api.supportsDismissInterrupted && (
          <button
            type="button"
            onClick={() => void runAction('dismiss')}
            disabled={pending !== null}
            className="min-h-[32px] px-3 rounded-xl text-xs font-medium border border-[var(--border)] bg-transparent text-[var(--foreground-muted)] hover:bg-[var(--border)] disabled:opacity-45 transition"
          >
            {pending === 'dismiss' ? '正在忽略…' : '忽略'}
          </button>
        )}
      </div>
      {error && (
        <p className="text-mc-caption text-rose-600 dark:text-rose-400" role="alert">
          {error}
        </p>
      )}
    </div>
  );
};
