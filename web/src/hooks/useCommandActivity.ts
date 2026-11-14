import { useEffect, useState } from 'react';
import { useRemoteCommandActivityApi } from '../context/remoteCommandActivityApi';
import { nextCommandActivity, type CommandActivityState } from '../utils/commandActivity';

export function useCommandActivity(threadID: string, enabled: boolean): Record<string, CommandActivityState> {
  const { supported, activity } = useRemoteCommandActivityApi();
  const [states, setStates] = useState<Record<string, CommandActivityState>>({});
  useEffect(() => { setStates({}); }, [threadID]);
  useEffect(() => {
    if (!enabled || !supported) return;
    let active = true, pending = false;
    const poll = async () => {
      if (pending || document.visibilityState !== 'visible') return;
      pending = true;
      try {
        const result = await activity(threadID);
        const now = Date.now();
        if (active && document.visibilityState === 'visible') setStates((previous) => Object.fromEntries(result.commands.map((command) => [command.messageID, nextCommandActivity(previous[command.messageID], command.processes, now)])));
      } catch { /* 本次采样失败时保留已有结果 */ }
      finally { pending = false; }
    };
    const visible = () => { if (document.visibilityState === 'visible') void poll(); };
    document.addEventListener('visibilitychange', visible);
    void poll();
    const timer = window.setInterval(() => void poll(), 1000);
    return () => { active = false; window.clearInterval(timer); document.removeEventListener('visibilitychange', visible); };
  }, [threadID, enabled, supported, activity]);
  return states;
}
