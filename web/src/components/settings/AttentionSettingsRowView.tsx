// 待处理设置行的展示部分单独成文件：保持纯粹的展示，不直接或间接引入任何 .css，
// 这样才能在没有测试运行器的 web/ 里用 react-dom/server 静态渲染断言。

import React from 'react';
import { SettingsRow, SettingsSection } from './SettingsLayout';

interface AttentionSettingsRowViewProps {
  unresolvedCount: number;
  onOpen: () => void;
}

// 说明文案与 settingsIndex.ts 里 web-attention-inbox 条目保持一致，避免搜索索引和设置页说法分叉。
const ATTENTION_ROW_DESC =
  '会话卡住、出错、额度用尽、等待审批，以及看板与自动化失败的汇总在这里逐条处理。';

// 待处理行的展示部分单独导出：走设置页统一的 SettingsSection / SettingsRow，
// 也便于不带 RemoteProvider 的静态渲染测试。
export const AttentionSettingsRowView: React.FC<AttentionSettingsRowViewProps> = ({
  unresolvedCount,
  onOpen
}) => (
  <SettingsSection title="待处理">
    <SettingsRow
      itemID="web-attention-inbox"
      title="待处理事项"
      desc={ATTENTION_ROW_DESC}
      control={
        <button type="button" className="secondary-button settings-inline-button" onClick={onOpen}>
          <span>打开</span>
          {unresolvedCount > 0 && (
            <span className="ml-auto shrink-0 min-w-[20px] px-1.5 py-0.5 rounded-full bg-[var(--danger)] text-center text-mc-caption font-semibold leading-[14px] text-white">
              {unresolvedCount > 99 ? '99+' : unresolvedCount}
            </span>
          )}
        </button>
      }
    />
  </SettingsSection>
);
