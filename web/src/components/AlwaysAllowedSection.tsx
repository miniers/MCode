// 「始终允许」名单：桌面端可以在审批设置里查看并撤销，Web 端此前只能看到弹窗里的选项。
// 存储层按项目路径（没有工作目录时退回项目 ID）记账，宿主负责把项目 ID 换算过去，
// 这里只管把当前选中项目的条目列出来并提供撤销。

import React, { useCallback, useEffect, useState } from 'react';
import { useRemoteSwarmExtApi } from '../context/remoteSwarmExtApi';

interface AlwaysAllowedSectionProps {
  /** 当前选中项目；为空（全部项目）时整块隐藏。 */
  projectID: string | null;
  /** 项目名，仅用于说明当前列的是哪个项目。 */
  projectName?: string;
}

export const AlwaysAllowedSection: React.FC<AlwaysAllowedSectionProps> = ({ projectID, projectName }) => {
  const api = useRemoteSwarmExtApi();
  const [keys, setKeys] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [revoking, setRevoking] = useState<string | null>(null);
  const [error, setError] = useState('');

  const enabled = api.supportsAlwaysAllowed && Boolean(projectID);

  const refresh = useCallback(async () => {
    if (!enabled || !projectID) return;
    setLoading(true);
    try {
      const result = await api.listAlwaysAllowed(projectID);
      setKeys(result.keys ?? []);
      setError('');
    } catch (err) {
      setError(err instanceof Error ? err.message : '读取「始终允许」失败，请稍后重试');
    } finally {
      setLoading(false);
    }
  }, [api, enabled, projectID]);

  // 切换项目后重新拉取
  useEffect(() => {
    void refresh();
  }, [refresh]);

  if (!enabled) return null;

  const revoke = async (key: string) => {
    if (!projectID || revoking) return;
    setRevoking(key);
    setError('');
    try {
      const result = await api.revokeAlwaysAllowed(projectID, key);
      setKeys(result.keys ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : '撤销失败，请稍后重试');
    } finally {
      setRevoking(null);
    }
  };

  return (
    <div className="settings-section">
      <p className="settings-label">始终允许的操作</p>
      {loading && keys.length === 0 && <p className="settings-note">加载中…</p>}
      {!loading && keys.length === 0 && <p className="settings-note">暂无</p>}
      {keys.length > 0 && (
        <ul className="space-y-2">
          {keys.map((key) => (
            <li
              key={key}
              className="flex items-center gap-2 rounded-xl border border-[var(--border)] bg-[var(--card)] px-3 py-2"
            >
              <span className="min-w-0 flex-1 break-all font-mono text-mc-footnote leading-5 text-[var(--foreground)]">
                {key.includes('\0prefix\0') ? key.replace('\0prefix\0', ' · ') + ' …' : key}
              </span>
              <button
                type="button"
                onClick={() => void revoke(key)}
                disabled={revoking !== null}
                className="shrink-0 min-h-[30px] px-2.5 rounded-lg text-xs font-medium border border-[var(--border)] bg-transparent text-rose-600 dark:text-rose-400 hover:bg-[var(--border)] disabled:opacity-45 transition"
              >
                {revoking === key ? '撤销中…' : '撤销'}
              </button>
            </li>
          ))}
        </ul>
      )}
      {error && <p className="settings-error" role="alert">{error}</p>}
      <p className="settings-note">
        {projectName ? `当前项目：${projectName}。` : ''}
        这些操作此前在审批时被选过「始终允许」，撤销后同一条操作会重新弹出确认。
      </p>
    </div>
  );
};
