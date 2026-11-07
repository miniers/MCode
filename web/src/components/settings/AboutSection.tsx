// 设置「关于」页：只读展示版本信息（Mac 端 mcode 版本、Web 端构建号、协议版本）。
// 对应桌面关于页 internal/ui/settings_page_about.go 的「版本号 / 当前构建版本」，
// Web 端不做检查更新与导出诊断。

import React from 'react';
import { useRemote } from '../../context/RemoteContext';
import { SettingsRow, SettingsSection } from './SettingsLayout';

/** 页面自身的构建号，写法与 web/src/App.tsx 的 getPageBuildID 一致。 */
function pageBuildID(): string {
  return document.querySelector('meta[name="mcode-build-id"]')?.getAttribute('content') || 'dev';
}

/** 只读的值：放在行尾的控件位，长文本允许换行。 */
const valueClass = 'text-sm font-medium break-all';

export const AboutSection: React.FC = () => {
  const { capabilities } = useRemote();
  const appVersion = capabilities?.appVersion?.trim() ?? '';
  // 宿主没带 appVersion 时说明对面版本过旧，直接说明而不是留空。
  const appVersionText = appVersion || '未知（宿主版本过旧）';
  const webBuildID = capabilities?.webBuildID?.trim() || pageBuildID();
  const schemaVersion =
    typeof capabilities?.schemaVersion === 'number' ? String(capabilities.schemaVersion) : '';

  return (
    <SettingsSection title="关于 MCode">
      <SettingsRow
        itemID="web-about-app-version"
        title="版本号"
        desc="Mac 端 mcode 的当前构建版本"
        control={<span className={valueClass}>{appVersionText}</span>}
      />
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
