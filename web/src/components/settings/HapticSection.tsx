// 外观页里的「触感反馈」开关（W-18）：只对支持 Vibration API 的 Android 生效。
import React from 'react';
import { SettingsRow, SettingsSection, SettingsSwitch } from './SettingsLayout';
import { getStoredHapticsEnabled, setStoredHapticsEnabled } from '../../services/storage';
import { haptic } from '../../utils/haptic';

// 与 settingsIndex.ts 里的搜索项 id 保持一致，搜索命中后才能滚到这里。
export const HAPTIC_SETTINGS_ITEM_ID = 'row-web-haptics';

export const HapticSection: React.FC = () => {
  const [enabled, setEnabled] = React.useState(getStoredHapticsEnabled);

  const toggle = (next: boolean) => {
    setEnabled(next);
    setStoredHapticsEnabled(next);
    // 打开时立即给一次反馈，让用户知道手感是什么。
    if (next) haptic('light');
  };

  return (
    <SettingsSection title="反馈">
      <SettingsRow
        itemID={HAPTIC_SETTINGS_ITEM_ID}
        title="触感反馈"
        desc="左滑、长按与审批提交时轻微振动；仅 Android 支持"
        control={<SettingsSwitch checked={enabled} onChange={toggle} label="触感反馈" />}
      />
    </SettingsSection>
  );
};
