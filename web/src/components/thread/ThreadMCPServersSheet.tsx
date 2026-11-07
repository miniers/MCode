// MCP 逐会话启停抽屉：列出项目里发现的 MCP 服务器，按会话下发禁用名单。
// 开关即生效（不做桌面端那种「应用」二次确认），乐观更新、失败回滚；
// 被全局设置锁住的服务（只读/待信任/全局已停用）不可切换，行内直接写明原因。

import React, { useCallback, useEffect, useImperativeHandle, useRef, useState } from 'react';
import type { RemoteMCPExtServer } from '../../types/generated';
import { useRemoteMCPServersApi } from '../../context/remoteMCPServersApi';
import { SheetFrame } from '../sheet/SheetFrame';
import { MenuSheetHeader } from './menu/MenuSheetHeader';
import type { ThreadMenuPageHandle } from './menu/menuPages';
import { ExclamationTriangleIcon, LockIcon } from '../Icons';

export interface ThreadMCPServersSheetProps {
  isOpen: boolean;
  threadID: string;
  /** 没有会话时按项目列清单；只是兜底，禁用名单仍按会话保存。 */
  projectID?: string;
  threadTitle?: string;
  onClose: () => void;
}

/** 桌面端 mcpServerRowLabel 的等价实现：名称缺失时退回 URL，再补来源 Agent。 */
const serverLabel = (server: RemoteMCPExtServer): string => {
  const name = server.name || server.url || server.key;
  return server.agent ? `${name} · ${server.agent}` : name;
};

/** 桌面端 mcpServerRowTooltip 的等价实现：来源与地址/命令，锁住时补一句原因。 */
const serverDetail = (server: RemoteMCPExtServer): string => {
  const parts: string[] = [];
  if (server.source) parts.push(server.source);
  if (server.url) parts.push(server.url);
  else if (server.command) parts.push(server.command);
  return parts.join(' · ');
};

const lockReasonText = (server: RemoteMCPExtServer): string =>
  server.lockReason || '需先在 MCP 设置里处理';

/** 手柄开关：样式与 WorktreeMoveSheet 的 role="switch" 一致。 */
const Switch: React.FC<{
  checked: boolean;
  disabled: boolean;
  label: string;
  onChange: () => void;
}> = ({ checked, disabled, label, onChange }) => (
  <button
    type="button"
    role="switch"
    aria-checked={checked}
    aria-label={label}
    disabled={disabled}
    onClick={onChange}
    className={`relative h-6 w-11 shrink-0 rounded-full transition disabled:opacity-40 ${
      checked ? 'bg-[var(--primary)]' : 'bg-[var(--border)]'
    }`}
  >
    <span
      className={`absolute left-0.5 top-0.5 h-5 w-5 rounded-full bg-white transition-transform ${
        checked ? 'translate-x-5' : ''
      }`}
    />
  </button>
);

/** 内容体：不含遮罩与标题栏，挂载即拉列表；保存中拦截关闭。 */
export const ThreadMCPServersSheetBody = React.forwardRef<
  ThreadMenuPageHandle,
  Omit<ThreadMCPServersSheetProps, 'isOpen'>
>(function ThreadMCPServersSheetBody({ threadID, projectID, threadTitle, onClose }, ref) {
  const { supportsList, supportsSetDisabled, listServers, setDisabled } = useRemoteMCPServersApi();

  const [servers, setServers] = useState<RemoteMCPExtServer[]>([]);
  // 待提交的禁用名单：宿主语义是整体替换，这里始终保存完整集合
  const [disabledKeys, setDisabledKeys] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const requestRef = useRef(0);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      requestRef.current += 1;
    };
  }, []);

  const loadServers = useCallback(async () => {
    if (!threadID && !projectID) {
      setError('缺少会话 ID，无法读取 MCP 服务器');
      return;
    }
    if (!supportsList) {
      setError('当前宿主未声明 mcp.servers.list 能力，无法读取 MCP 服务器');
      return;
    }
    const requestID = ++requestRef.current;
    setLoading(true);
    setError(null);
    setNotice(null);
    try {
      const list = await listServers(threadID ? { threadID } : { projectID });
      if (!mountedRef.current || requestID !== requestRef.current) return;
      setServers(list);
      setDisabledKeys(list.filter((server) => server.disabled).map((server) => server.key));
    } catch (err) {
      if (!mountedRef.current || requestID !== requestRef.current) return;
      setServers([]);
      setDisabledKeys([]);
      setError(err instanceof Error ? err.message : '读取 MCP 服务器失败');
    } finally {
      if (mountedRef.current && requestID === requestRef.current) setLoading(false);
    }
  }, [listServers, projectID, supportsList, threadID]);

  // 每次挂载（即每次入栈）拉一次列表；卸载时由 mountedRef 清理作废在途请求
  useEffect(() => {
    void loadServers();
  }, [loadServers]);

  const handleToggle = useCallback(
    async (server: RemoteMCPExtServer) => {
      if (saving || server.locked) return;
      const previous = disabledKeys;
      const next = previous.includes(server.key)
        ? previous.filter((key) => key !== server.key)
        : [...previous, server.key];
      // 乐观更新：先把开关拨过去，失败再回滚
      setDisabledKeys(next);
      setError(null);
      setNotice(null);
      setSaving(true);
      try {
        const saved = await setDisabled(threadID, next);
        if (!mountedRef.current) return;
        setDisabledKeys(saved);
        setNotice('已保存会话的 MCP 设置');
      } catch (err) {
        if (!mountedRef.current) return;
        setDisabledKeys(previous);
        setError(err instanceof Error ? err.message : '保存会话 MCP 设置失败');
      } finally {
        if (mountedRef.current) setSaving(false);
      }
    },
    [disabledKeys, saving, setDisabled, threadID]
  );

  const requestClose = useCallback(() => (saving ? false : true), [saving]);

  useImperativeHandle(ref, () => ({ requestClose }), [requestClose]);

  const handleClose = useCallback(() => {
    if (!requestClose()) return;
    onClose();
  }, [onClose, requestClose]);

  return (
    <div className="flex flex-col flex-1 min-h-0 min-w-0">
      <p className="mb-3 shrink-0 text-xs text-[var(--foreground-muted)] truncate">
        {threadTitle || '未命名会话'}
      </p>

      <div className="flex-1 min-h-0 overflow-y-auto overflow-x-hidden min-w-0 max-w-full w-full pr-0.5 space-y-3">
          {/* 与桌面端 renderMCPSelectionPanel 的说明一致：只影响当前会话 */}
          <p className="text-mc-caption text-[var(--foreground-muted)] leading-relaxed">
            开启表示本会话注入该服务，关闭表示本会话不注入；设置只对当前会话生效。
          </p>

          {loading && (
            <div className="empty-state">
              <span className="loading-spinner mb-1" />
              <h2>正在读取 MCP 服务器</h2>
              <p className="text-xs text-[var(--foreground-muted)]">正在扫描项目与用户级 MCP 配置…</p>
            </div>
          )}

          {!loading && !error && servers.length === 0 && (
            <p className="text-xs text-[var(--foreground-muted)]">这个项目没有配置 MCP 服务器</p>
          )}

          {!loading && servers.length > 0 && (
            <ul className="space-y-1">
              {servers.map((server) => {
                const locked = Boolean(server.locked);
                const enabled = !locked && !disabledKeys.includes(server.key);
                const detail = serverDetail(server);
                return (
                  <li
                    key={server.key}
                    className="flex items-center gap-3 p-2.5 rounded-xl border border-[var(--border)] bg-[var(--card)] min-w-0"
                  >
                    <div className="min-w-0 flex-1">
                      <p className="text-xs font-semibold text-[var(--foreground)] truncate">
                        {serverLabel(server)}
                      </p>
                      {detail && (
                        <p className="text-mc-caption text-[var(--foreground-muted)] break-all leading-relaxed">
                          {detail}
                        </p>
                      )}
                      {locked && (
                        <p className="mt-0.5 flex items-center gap-1 text-mc-caption text-[var(--foreground-muted)]">
                          <LockIcon className="h-3 w-3 shrink-0" />
                          <span className="truncate">{lockReasonText(server)}</span>
                        </p>
                      )}
                    </div>
                    <Switch
                      checked={enabled}
                      disabled={locked || saving || !supportsSetDisabled || !threadID}
                      label={locked ? `${serverLabel(server)}：${lockReasonText(server)}` : `切换 ${serverLabel(server)}`}
                      onChange={() => void handleToggle(server)}
                    />
                  </li>
                );
              })}
            </ul>
          )}

          {!loading && servers.length > 0 && !supportsSetDisabled && (
            <p className="text-mc-caption text-[var(--foreground-muted)]">
              当前宿主未声明 threads.mcpDisabled.set 能力，只能查看列表。
            </p>
          )}

          {notice && (
            <p className="text-mc-caption text-emerald-600 dark:text-emerald-400" role="status">
              {notice}
            </p>
          )}

          {error && (
            <div className="inline-alert inline-alert--danger" role="alert">
              <div className="flex items-start gap-2 min-w-0 flex-1">
                <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-[var(--danger-foreground)]" />
                <span className="break-words select-text">{error}</span>
              </div>
              <button
                type="button"
                onClick={() => void loadServers()}
                disabled={loading}
                className="inline-alert__action min-h-[44px] px-2 flex items-center"
              >
                重试
              </button>
            </div>
          )}
      </div>

      <div className="mt-3 shrink-0 border-t border-[var(--border)] pt-3 min-w-0 max-w-full w-full flex items-center justify-end gap-2">
        <button
          type="button"
          onClick={handleClose}
          disabled={saving}
          className="min-h-[44px] px-3 rounded-xl text-xs font-medium border border-[var(--border)] bg-[var(--card)] text-[var(--foreground)] disabled:opacity-50 transition"
        >
          {saving ? '保存中…' : '完成'}
        </button>
      </div>
    </div>
  );
});

/** 兼容外壳：保留原 props，内部用 SheetFrame + MenuSheetHeader 包住内容体。 */
export const ThreadMCPServersSheet: React.FC<ThreadMCPServersSheetProps> = ({
  isOpen,
  threadID,
  projectID,
  threadTitle,
  onClose
}) => {
  const bodyRef = useRef<ThreadMenuPageHandle>(null);

  const requestClose = useCallback(() => {
    if (bodyRef.current?.requestClose() === false) return;
    onClose();
  }, [onClose]);

  if (!isOpen) return null;

  return (
    <SheetFrame
      size="panel"
      labelledBy="thread-mcp-sheet-title"
      onRequestClose={requestClose}
      closeOnEscape
    >
      <MenuSheetHeader
        eyebrow="会话与工作区"
        title="MCP 服务器"
        labelledBy="thread-mcp-sheet-title"
        leading="close"
        onLeading={requestClose}
      />
      <ThreadMCPServersSheetBody
        ref={bodyRef}
        threadID={threadID}
        projectID={projectID}
        threadTitle={threadTitle}
        onClose={onClose}
      />
    </SheetFrame>
  );
};
