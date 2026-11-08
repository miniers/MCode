// 设置「关于」页：只读展示版本信息（Mac 端 mcode 版本、Web 端构建号、协议版本）。
// 对应桌面关于页 internal/ui/settings_page_about.go 的「版本号 / 当前构建版本」，
// Web 端支持检查更新，不支持导出诊断。

import React, { useEffect, useState } from 'react';
import { useRemote } from '../../context/RemoteContext';
import { isAppUpdateMethodNotFound, useRemoteAppUpdateApi, type RemoteAppUpdateApi } from '../../context/remoteAppUpdateApi';
import type { AppUpdateStatus } from '../../types/generated';
import { MarkdownView } from '../MarkdownView';
import { SettingsRow, SettingsSection } from './SettingsLayout';

/** 页面自身的构建号，写法与 web/src/App.tsx 的 getPageBuildID 一致。 */
function pageBuildID(): string {
  return document.querySelector('meta[name="mcode-build-id"]')?.getAttribute('content') || 'dev';
}

/** 只读的值：放在行尾的控件位，长文本允许换行。 */
const valueClass = 'text-sm font-medium break-all';

export const AboutSection: React.FC = () => {
  const { capabilities } = useRemote();
  const api = useRemoteAppUpdateApi();
  const appVersion = capabilities?.appVersion?.trim() ?? '';
  // 宿主没带 appVersion 时说明对面版本过旧，直接说明而不是留空。
  const appVersionText = appVersion || '未知（宿主版本过旧）';
  const webBuildID = capabilities?.webBuildID?.trim() || pageBuildID();
  const schemaVersion =
    typeof capabilities?.schemaVersion === 'number' ? String(capabilities.schemaVersion) : '';

  return <AboutSectionView api={api} appVersionText={appVersionText} webBuildID={webBuildID} schemaVersion={schemaVersion} />;
};

export const AboutSectionView: React.FC<{
  api: RemoteAppUpdateApi;
  appVersionText: string;
  webBuildID: string;
  schemaVersion: string;
}> = ({ api, appVersionText, webBuildID, schemaVersion }) => {
  const [status, setStatus] = useState<AppUpdateStatus | null>(null);
  const [unsupported, setUnsupported] = useState(false);
  const [checked, setChecked] = useState(false);
  const [busy, setBusy] = useState(false);
  const [installing, setInstalling] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    let cancelled = false;
    setUnsupported(false);
    setStatus(null);
    setChecked(false);
    setError('');
    api.get().then((result) => {
      if (!cancelled) setStatus(result);
    }).catch((err: unknown) => {
      if (cancelled) return;
      if (isAppUpdateMethodNotFound(err)) setUnsupported(true);
      else setError('读取更新状态失败');
    });
    return () => { cancelled = true; };
  }, [api]);

  const handleAction = async () => {
    if (busy || installing) return;
    if (status?.available && !status.installable) {
      if (status.url) window.open(status.url, '_blank');
      return;
    }
    setBusy(true);
    setError('');
    try {
      if (status?.available && status.installable) {
        const result = await api.install();
        if (result.started) setInstalling(true);
        else setError('更新启动失败');
      } else {
        setStatus(await api.check());
        setChecked(true);
      }
    } catch (err) {
      if (isAppUpdateMethodNotFound(err)) setUnsupported(true);
      else setError(status?.available ? '更新失败' : '检查失败');
    } finally {
      setBusy(false);
    }
  };

  return (
    <SettingsSection title="关于 MCode">
      <SettingsRow
        itemID="web-about-app-version"
        title="版本号"
        desc="Mac 端 mcode 的当前构建版本"
        control={<span className={valueClass}>{appVersionText}</span>}
      />
      {!unsupported && (
        <>
          <SettingsRow
            itemID="web-about-update"
            title="检查更新"
            control={
              <div className="flex items-center gap-2">
                <span className={valueClass} role={error ? 'alert' : undefined}>
                  {error || (busy && !status?.available ? '检查中…' : status?.available ? status.latest : checked ? '已是最新' : '')}
                </span>
                <button
                  type="button"
                  className="secondary-button settings-inline-button"
                  onClick={() => void handleAction()}
                  disabled={busy || installing || Boolean(status?.available && !status.installable && !status.url)}
                >
                  {installing ? '正在更新…' : status?.available ? (status.installable ? '更新' : '下载') : '检查'}
                </button>
              </div>
            }
          />
          {status?.available && status.notes?.trim() && (
            <div className="settings-row animate-fadeIn motion-reduce:animate-none">
              <MarkdownView text={status.notes} />
            </div>
          )}
        </>
      )}
      <SettingsRow
        itemID="web-about-web-build"
        title="Web 端构建号"
        desc="当前页面资源对应的构建标识"
        control={<span className={valueClass}>{webBuildID}</span>}
      />
      <SettingsRow
        itemID="web-about-schema-version"
        title="协议版本"
        desc="远程控制协议的 schemaVersion，两端不一致时需要一起更新"
        control={<span className={valueClass}>{schemaVersion || '未知'}</span>}
      />
    </SettingsSection>
  );
};
