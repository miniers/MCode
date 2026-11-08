// 设置「关于」页：只读展示版本信息（Mac 端 mcode 版本、Web 端构建号、协议版本）。
// 对应桌面关于页 internal/ui/settings_page_about.go 的「版本号 / 当前构建版本」，
// Web 端支持检查更新，不支持导出诊断。

import React, { useCallback, useEffect, useState } from 'react';
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

/** 更新区的全部状态：抽成纯数据，展示与交互都能脱离组件单独验证。 */
export type AboutUpdateState = {
  status: AppUpdateStatus | null;
  unsupported: boolean;
  checked: boolean;
  busy: boolean;
  installing: boolean;
  error: string;
};

export const initialAboutUpdateState: AboutUpdateState = {
  status: null,
  unsupported: false,
  checked: false,
  busy: false,
  installing: false,
  error: ''
};

/** 状态写入器；与 useState 的函数式更新语义一致（在旧值上打补丁）。 */
export type AboutUpdatePatch = (patch: Partial<AboutUpdateState>) => void;

/** 读取宿主的更新状态；cancelled 用于组件卸载后丢弃结果。 */
export async function loadAboutUpdate(
  api: RemoteAppUpdateApi,
  update: AboutUpdatePatch,
  cancelled: () => boolean = () => false
): Promise<void> {
  update({ unsupported: false, status: null, checked: false, error: '' });
  try {
    const result = await api.get();
    if (!cancelled()) update({ status: result });
  } catch (err) {
    if (cancelled()) return;
    if (isAppUpdateMethodNotFound(err)) update({ unsupported: true });
    else update({ error: '读取更新状态失败' });
  }
}

/** 检查 / 更新 / 下载的唯一入口：只读写传入的状态，不依赖 React。 */
export async function runAboutUpdateAction(
  api: RemoteAppUpdateApi,
  state: AboutUpdateState,
  update: AboutUpdatePatch
): Promise<void> {
  if (state.busy || state.installing) return;
  if (state.status?.available && !state.status.installable) {
    if (state.status.url) window.open(state.status.url, '_blank');
    return;
  }
  update({ busy: true, error: '' });
  try {
    if (state.status?.available && state.status.installable) {
      const result = await api.install();
      if (result.started) update({ installing: true });
      else update({ error: '更新启动失败' });
    } else {
      const next = await api.check();
      update({ status: next, checked: true });
    }
  } catch (err) {
    if (isAppUpdateMethodNotFound(err)) update({ unsupported: true });
    else update({ error: state.status?.available ? '更新失败' : '检查失败' });
  } finally {
    update({ busy: false });
  }
}

/** 纯展示层：只按传入的状态渲染。 */
export const AboutSectionContent: React.FC<{
  state: AboutUpdateState;
  appVersionText: string;
  webBuildID: string;
  schemaVersion: string;
  onAction: () => void;
}> = ({ state, appVersionText, webBuildID, schemaVersion, onAction }) => {
  const { status, unsupported, checked, busy, installing, error } = state;

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
                  onClick={onAction}
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

export const AboutSectionView: React.FC<{
  api: RemoteAppUpdateApi;
  appVersionText: string;
  webBuildID: string;
  schemaVersion: string;
}> = ({ api, appVersionText, webBuildID, schemaVersion }) => {
  const [state, setState] = useState<AboutUpdateState>(initialAboutUpdateState);
  const update = useCallback<AboutUpdatePatch>((patch) => {
    setState((prev) => ({ ...prev, ...patch }));
  }, []);

  useEffect(() => {
    let cancelled = false;
    void loadAboutUpdate(api, update, () => cancelled);
    return () => { cancelled = true; };
  }, [api, update]);

  return (
    <AboutSectionContent
      state={state}
      appVersionText={appVersionText}
      webBuildID={webBuildID}
      schemaVersion={schemaVersion}
      onAction={() => void runAboutUpdateAction(api, state, update)}
    />
  );
};
