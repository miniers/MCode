import { useEffect } from 'react';
import { useRemoteStatic } from '../context/RemoteContext';
import type { RemoteGitAutoFetchResult } from '../types/generated';

const GIT_AUTO_FETCH_INTERVAL = 5 * 60 * 1000;

export function useGitAutoFetch(threadID: string, enabled: boolean, onFetched?: () => void) {
  const { callRemote, capabilities, connectionState } = useRemoteStatic();
  const supported = capabilities?.methods.includes('git.autoFetch');
  useEffect(() => {
    if (!enabled || !supported || connectionState !== 'connected') return;
    let disposed = false;
    let pending = false;
    const fetch = async () => {
      if (pending || document.visibilityState !== 'visible' || !document.hasFocus()) return;
      pending = true;
      try {
        const result = await callRemote<RemoteGitAutoFetchResult>('git.autoFetch', { threadID }, 35000);
        if (!disposed && result.fetched) onFetched?.();
      } catch {
        // 与桌面后台抓取一致，手动抓取时展示远端错误。
      } finally {
        pending = false;
      }
    };
    const trigger = () => { void fetch(); };
    trigger();
    const timer = window.setInterval(trigger, GIT_AUTO_FETCH_INTERVAL);
    window.addEventListener('focus', trigger);
    document.addEventListener('visibilitychange', trigger);
    return () => {
      disposed = true;
      window.clearInterval(timer);
      window.removeEventListener('focus', trigger);
      document.removeEventListener('visibilitychange', trigger);
    };
  }, [callRemote, connectionState, enabled, onFetched, supported, threadID]);
}
