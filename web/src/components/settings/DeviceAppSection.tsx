// 设置 › 此设备里的常驻入口：添加到主屏幕、推送通知。
// 首页卡片关闭后，仍能从这里安装或开启通知。
import React from 'react';
import { SettingsRow, SettingsSection } from './SettingsLayout';
import { usePushSubscription } from '../../hooks/usePushSubscription';
import { getInstallEnvironment, installHintText } from '../../utils/installHint';
import { describePushRow } from '../../utils/pushRow';

// 与 settingsIndex.ts 里的搜索项 id 保持一致，搜索命中后才能滚到这里。
export const INSTALL_SETTINGS_ITEM_ID = 'row-web-install';
export const PUSH_SETTINGS_ITEM_ID = 'row-web-push';

export const DeviceAppSection: React.FC = () => {
  const env = React.useMemo(getInstallEnvironment, []);
  const { body: installBody } = installHintText(env);
  const { supported, subscribed, permission, working, errorMessage, enable } = usePushSubscription();

  const pushRow = describePushRow({
    supported,
    secure: env.secure,
    ios: env.ios,
    standalone: env.standalone,
    subscribed,
    permission
  });

  return (
    <SettingsSection title="主屏幕与通知">
      <SettingsRow
        itemID={INSTALL_SETTINGS_ITEM_ID}
        title="添加到主屏幕"
        desc={env.standalone ? '已从主屏幕打开' : installBody}
        control={env.standalone ? <span className="settings-row-desc">已添加</span> : undefined}
      />
      <SettingsRow
        itemID={PUSH_SETTINGS_ITEM_ID}
        title="推送通知"
        desc={pushRow.desc}
        error={errorMessage}
        control={
          pushRow.control === 'enabled' ? (
            <span className="settings-row-desc">已开启</span>
          ) : pushRow.control === 'button' ? (
            <button
              type="button"
              className="secondary-button settings-inline-button"
              disabled={working}
              onClick={() => void enable()}
            >
              {working ? '正在开启…' : '开启'}
            </button>
          ) : undefined
        }
      />
    </SettingsSection>
  );
};

export default DeviceAppSection;
