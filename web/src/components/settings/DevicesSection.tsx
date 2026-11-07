// 设备页的「已授权设备」区块：列出本机全部已配对设备，本机置顶并标注「本机」，
// 非本机设备可撤销（二次确认）。撤销成功后重新拉取列表，宿主广播 session.devices.changed
// 时也会自动刷新。宿主没同时提供 session.devices.list / session.devices.revoke 时整块不渲染。

import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  useDevicesChangeVersion,
  useRemoteDeviceApi,
  type SessionDeviceEntry
} from '../../context/remoteDeviceApi';
import { formatRelativeTime } from '../../utils/date';
import { ConfirmDialog } from '../ConfirmDialog';
import { SettingsRow, SettingsSection } from './SettingsLayout';

// 入口中文化，与桌面端 internal/ui/settings_remote.go:391 remoteEntryLabel 保持一致。
function entryLabel(entry: SessionDeviceEntry['entry']): string {
  switch (entry) {
    case 'tls':
      return '域名 HTTPS';
    case 'lan':
      return '局域网 HTTP';
    case 'tailscale':
      return 'Tailscale';
    default:
      return '';
  }
}

function deviceDisplayName(device: SessionDeviceEntry): string {
  const name = device.label?.trim();
  if (name) return name;
  return device.deviceID;
}

// 平台缺失时退回收窄的 userAgent，避免把整串 UA 塞进一行说明。
function platformText(device: SessionDeviceEntry): string {
  const platform = device.platform?.trim();
  if (platform) return platform;
  const ua = device.userAgent?.trim();
  if (!ua) return '';
  return ua.length > 40 ? `${ua.slice(0, 40)}…` : ua;
}

export const DevicesSection: React.FC = () => {
  const { supported, list, revoke } = useRemoteDeviceApi();
  const changeVersion = useDevicesChangeVersion(supported);

  const [devices, setDevices] = useState<SessionDeviceEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [actionError, setActionError] = useState('');
  const [busyID, setBusyID] = useState<string | null>(null);
  const [confirmID, setConfirmID] = useState<string | null>(null);
  const requestRef = useRef(0);

  const load = useCallback(async () => {
    if (!supported) return;
    const reqID = ++requestRef.current;
    setLoading(true);
    try {
      const next = await list();
      if (requestRef.current !== reqID) return;
      setDevices(next);
      setError('');
    } catch (err) {
      if (requestRef.current !== reqID) return;
      setError(err instanceof Error ? err.message : '获取已授权设备失败，请稍后重试');
    } finally {
      if (requestRef.current === reqID) setLoading(false);
    }
  }, [list, supported]);

  // 首次进入与收到 session.devices.changed 时都重新拉取
  useEffect(() => {
    void load();
  }, [load, changeVersion]);

  const orderedDevices = useMemo(() => {
    const sorted = [...devices];
    sorted.sort((a, b) => {
      if (a.current !== b.current) return a.current ? -1 : 1;
      const aPaired = Date.parse(a.pairedAt || '') || 0;
      const bPaired = Date.parse(b.pairedAt || '') || 0;
      return aPaired - bPaired;
    });
    return sorted;
  }, [devices]);

  const confirmDevice = useMemo(
    () => orderedDevices.find((device) => device.deviceID === confirmID) ?? null,
    [confirmID, orderedDevices]
  );

  const handleRevoke = useCallback(
    async (deviceID: string) => {
      if (busyID) return;
      setConfirmID(null);
      setBusyID(deviceID);
      setActionError('');
      try {
        await revoke(deviceID);
        await load();
      } catch (err) {
        setActionError(err instanceof Error ? err.message : '撤销失败，请稍后重试');
      } finally {
        setBusyID(null);
      }
    },
    [busyID, load, revoke]
  );

  if (!supported) return null;

  return (
    <SettingsSection title="已授权设备">
      {loading && orderedDevices.length === 0 && <SettingsRow desc="正在读取已授权设备…" />}

      {error && (
        <SettingsRow
          error={error}
          control={
            <button
              type="button"
              className="secondary-button settings-inline-button"
              onClick={() => void load()}
              disabled={loading}
            >
              <span>{loading ? '正在刷新' : '重试'}</span>
            </button>
          }
        />
      )}

      {actionError && <SettingsRow error={actionError} />}

      {!loading && !error && orderedDevices.length === 0 && (
        <SettingsRow desc="暂无已授权设备" />
      )}

      {orderedDevices.map((device) => {
        const name = deviceDisplayName(device);
        const platform = platformText(device);
        const entry = entryLabel(device.entry);
        const meta = [platform, entry, device.online ? '在线' : '离线'].filter(Boolean).join(' · ');
        const pairedAt = formatRelativeTime(device.pairedAt);
        const lastSeenAt = formatRelativeTime(device.lastSeenAt);
        const access = device.lastAddress ? `最后访问：${device.lastAddress}` : '';

        return (
          <SettingsRow
            key={device.deviceID}
            itemID={`web-device-${device.deviceID}`}
            title={
              <span className="inline-flex min-w-0 items-center gap-1.5">
                <span
                  aria-hidden="true"
                  className={`inline-block h-2 w-2 shrink-0 rounded-full ${
                    device.online ? 'bg-emerald-500' : 'bg-slate-300 dark:bg-zinc-600'
                  }`}
                />
                <span className="truncate">{name}</span>
                {device.current && (
                  <span className="shrink-0 rounded border border-[var(--border)] px-1 py-px text-mc-caption font-medium text-[var(--foreground-muted)]">
                    本机
                  </span>
                )}
              </span>
            }
            desc={
              <span className="block space-y-0.5">
                {meta && <span className="block">{meta}</span>}
                <span className="block">
                  {pairedAt ? `配对于 ${pairedAt}` : '配对时间未知'}
                  {lastSeenAt ? ` · 最后活跃 ${lastSeenAt}` : ' · 暂无活跃记录'}
                </span>
                {access && <span className="block">{access}</span>}
              </span>
            }
            control={
              device.current ? undefined : (
                <button
                  type="button"
                  className="settings-action settings-action--danger settings-inline-button"
                  onClick={() => setConfirmID(device.deviceID)}
                  disabled={busyID !== null}
                  aria-label={`撤销设备 ${name}`}
                >
                  <span>{busyID === device.deviceID ? '正在撤销…' : '撤销'}</span>
                </button>
              )
            }
          />
        );
      })}

      <SettingsRow
        desc="撤销后该设备需要重新扫码配对才能连接这台 Mac。本机的撤销入口在「此设备」里。"
      />

      <ConfirmDialog
        isOpen={confirmDevice !== null}
        title="撤销设备授权"
        message={
          confirmDevice
            ? `「${deviceDisplayName(confirmDevice)}」将立即失去连接权限，需要重新配对才能使用。`
            : ''
        }
        confirmLabel="撤销"
        confirmVariant="danger"
        isLoading={busyID !== null}
        onConfirm={() => {
          if (confirmDevice) void handleRevoke(confirmDevice.deviceID);
        }}
        onCancel={() => setConfirmID(null)}
      />
    </SettingsSection>
  );
};
