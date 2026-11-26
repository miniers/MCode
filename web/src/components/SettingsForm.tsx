// 移动端设置表单：按页渲染，每页按小节分组；Web 版只保留远程端需要的设置。
// 对应宿主 internal/remote/settings_ext_methods.go（白名单与类型）与
// internal/app/remote_settings.go（取值校验）。
// 控件类型跟着校验走：布尔用开关、枚举用下拉、时刻用 HH:MM。
// 改一项就立刻 settings.set 只提交这一键；失败时回滚并显示服务端错误。

import React, { useCallback, useEffect, useState } from 'react';
import type { RemoteSettingsApi } from '../context/remoteSettingsApi';
import type { RemoteSettingsValue, RemoteSettingsValues } from '../types/generated';
import {
  SettingsRow,
  SettingsSection,
  SettingsStackedRow,
  SettingsSwitch
} from './settings/SettingsLayout';

export type SettingsFormPage = 'conversation' | 'swarm' | 'steward' | 'device';

interface SelectOption {
  value: string;
  label: string;
}

const PERMISSION_MODES: SelectOption[] = [
  { value: 'readonly', label: '只读' },
  { value: 'supervised', label: '监督' },
  { value: 'auto', label: '自动' },
  { value: 'fullAccess', label: '完全访问' }
];

const AUTO_DORMANT_DAYS: SelectOption[] = [
  { value: '0', label: '关闭 (0)' },
  { value: '1', label: '1 天' },
  { value: '3', label: '3 天' },
  { value: '7', label: '7 天' },
  { value: '14', label: '14 天' },
  { value: '30', label: '30 天' }
];

const MAX_BEES_DEFAULT = 8;
const MAX_BEES_OPTIONS: SelectOption[] = [
  { value: '0', label: '自动 (8)' },
  ...[1, 2, 3, 4, 5, 6, 7, 8].map((count) => ({ value: String(count), label: String(count) }))
];

// 合并前审查三档，与 config.ReviewMode* 一致。
const REVIEW_MODE_OPTIONS: SelectOption[] = [
  { value: 'off', label: '关闭' },
  { value: 'ai', label: 'AI 审查' },
  { value: 'human', label: '人工审查' }
];

const asString = (value: RemoteSettingsValue | undefined, fallback = ''): string =>
  typeof value === 'string' ? value : fallback;

const asBool = (value: RemoteSettingsValue | undefined, fallback = false): boolean =>
  typeof value === 'boolean' ? value : fallback;

const asNumber = (value: RemoteSettingsValue | undefined, fallback = 0): number =>
  typeof value === 'number' && Number.isFinite(value) ? value : fallback;

const compactSettings = [
  { key: 'session.compact_suggest_percent', id: 'row-compact-suggest-percent', label: '建议压缩比例（%）', min: 10, max: 95 },
  { key: 'session.compact_suggest_max_k', id: 'row-compact-suggest-max', label: '建议压缩上限（k tokens）', min: 10, max: 1000 }
] as const;

function WebhookSettings({ save, busy }: { save: RemoteSettingsApi['setValue']; busy: boolean }) {
  const [value, setValue] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [status, setStatus] = useState('');
  const secure = typeof window !== 'undefined' && window.location.protocol === 'https:';
  const commit = async (next: string) => {
    setStatus('');
    const result = await save('notifications.webhook_url', next);
    setError(result);
    if (!result) {
      setValue('');
      setStatus(next ? 'Webhook 已保存' : 'Webhook 已关闭');
    }
  };
  return (
    <SettingsStackedRow itemID="row-webhook-url" title="Webhook" error={error}>
      {secure ? (
        <form className="flex flex-wrap items-center gap-2" onSubmit={(event) => { event.preventDefault(); if (value.trim()) void commit(value.trim()); }}>
          <input type="password" autoComplete="new-password" aria-label="Webhook 地址" className="settings-input min-w-0 flex-1" value={value} onChange={(event) => setValue(event.target.value)} disabled={busy} />
          <button type="submit" disabled={busy || !value.trim()} className="primary-button settings-inline-button">保存</button>
          <button type="button" disabled={busy} className="secondary-button settings-inline-button" onClick={() => void commit('')}>关闭</button>
          {status && <span role="status" className="basis-full text-mc-footnote text-[var(--foreground-muted)]">{status}</span>}
        </form>
      ) : <p className="text-mc-footnote text-[var(--foreground-muted)]">通过 HTTPS 配置 Webhook。</p>}
    </SettingsStackedRow>
  );
}

interface ToggleFieldProps {
  itemID?: string;
  label: string;
  checked: boolean;
  disabled?: boolean;
  error?: string | null;
  onChange: (next: boolean) => void;
}

const ToggleField: React.FC<ToggleFieldProps> = ({
  itemID,
  label,
  checked,
  disabled,
  error,
  onChange
}) => (
  <SettingsRow
    itemID={itemID}
    title={label}
    error={error}
    control={
      <SettingsSwitch
        checked={checked}
        disabled={disabled}
        label={label}
        onChange={onChange}
      />
    }
  />
);

interface ChoiceFieldProps {
  itemID?: string;
  label: string;
  value: string;
  options: SelectOption[];
  disabled?: boolean;
  error?: string | null;
  onChange: (next: string) => void;
}

const ChoiceField: React.FC<ChoiceFieldProps> = ({
  itemID,
  label,
  value,
  options,
  disabled,
  error,
  onChange
}) => {
  // 当前值不在选项里时补一项，避免下拉静默显示成第一项。
  const merged = options.some((option) => option.value === value)
    ? options
    : [...options, { value, label: value || '未设置' }];
  return (
    <SettingsRow
      itemID={itemID}
      title={label}
      error={error}
      control={
        <select
          className="settings-select"
          value={value}
          disabled={disabled}
          aria-label={label}
          onChange={(event) => onChange(event.target.value)}
        >
          {merged.map((option) => (
            <option key={option.value || 'empty'} value={option.value}>
              {option.label}
            </option>
          ))}
        </select>
      }
    />
  );
};

interface DraftFieldProps {
  itemID?: string;
  label: string;
  value: string;
  type?: 'text' | 'time' | 'number';
  placeholder?: string;
  min?: number;
  max?: number;
  required?: boolean;
  disabled?: boolean;
  error?: string | null;
  onCommit: (next: string) => void;
}

// 文本/数字/时刻这类需要先编辑再提交的项：失焦或回车才写入，避免每敲一个字都打一次 RPC。
const DraftField: React.FC<DraftFieldProps> = ({
  itemID,
  label,
  value,
  type = 'text',
  placeholder,
  min,
  max,
  required,
  disabled,
  error,
  onCommit
}) => {
  const [draft, setDraft] = useState(value);
  const [localError, setLocalError] = useState<string | null>(null);

  useEffect(() => {
    setDraft(value);
  }, [value]);

  const commit = () => {
    const trimmed = draft.trim();
    if (required && !trimmed) {
      setLocalError('不能为空');
      return;
    }
    if (type === 'number' && trimmed) {
      if (!/^-?\d+$/.test(trimmed)) {
        setLocalError('请输入整数');
        return;
      }
      const number = Number(trimmed);
      if (min !== undefined && number < min) {
        setLocalError(`不能小于 ${min}`);
        return;
      }
      if (max !== undefined && number > max) {
        setLocalError(`不能大于 ${max}`);
        return;
      }
    }
    setLocalError(null);
    if (trimmed !== value) onCommit(trimmed);
  };

  return (
    <SettingsStackedRow itemID={itemID} title={label} error={localError || error}>
      <input
        className="settings-input"
        type={type}
        value={draft}
        placeholder={placeholder}
        min={min}
        max={max}
        disabled={disabled}
        aria-label={label}
        onChange={(event) => setDraft(event.target.value)}
        onBlur={commit}
        onKeyDown={(event) => {
          if (event.key === 'Enter') {
            event.preventDefault();
            event.currentTarget.blur();
          }
        }}
      />
    </SettingsStackedRow>
  );
};

export interface SettingsFormProps {
  api: RemoteSettingsApi;
  page: SettingsFormPage;
  // 「子代理档位」入口由 Header 传入（它持有打开档位弹层的状态）。
  swarmTiersRow?: React.ReactNode;
}

export const SettingsForm: React.FC<SettingsFormProps> = ({
  api,
  page,
  swarmTiersRow
}) => {
  const { snapshot, loading, loadError, savingKey, reload, setValue } = api;
  // values 先跟着快照走，提交时先改本地再等结果，失败按服务端错误回滚。
  const [values, setValues] = useState<RemoteSettingsValues>({});
  const [errors, setErrors] = useState<Record<string, string | null>>({});

  useEffect(() => {
    if (!snapshot) return;
    setValues((current) => ({ ...current, ...snapshot.values }));
  }, [snapshot]);

  const commit = useCallback(
    async (key: string, next: RemoteSettingsValue) => {
      const previous = values[key];
      setValues((current) => ({ ...current, [key]: next }));
      setErrors((current) => ({ ...current, [key]: null }));
      const error = await setValue(key, next);
      if (!error) return;
      setValues((current) => ({ ...current, [key]: previous }));
      setErrors((current) => ({ ...current, [key]: error }));
    },
    [setValue, values]
  );

  if (loading && !snapshot) {
    return (
      <SettingsSection title="偏好">
        <div className="settings-row">
          <div className="settings-row-main">
            <div className="settings-row-desc">正在读取 Mac 上的设置…</div>
          </div>
        </div>
      </SettingsSection>
    );
  }

  if (!snapshot) {
    return (
      <SettingsSection title="偏好">
        <div className="settings-row">
          <div className="settings-row-main">
            <div className="settings-row-error" role="alert">
              {loadError || '暂时读不到 Mac 上的设置'}
            </div>
            <button type="button" className="settings-action" onClick={reload}>
              <span>重新读取</span>
            </button>
          </div>
        </div>
      </SettingsSection>
    );
  }

  const busy = savingKey !== null;
  // 旧宿主没有这些键时 writableKeys 里也不包含，对应控件直接不渲染。
  const hasKey = (key: string) => snapshot.writableKeys.includes(key);

  const dormantDaysValue = String(asNumber(values['archive.sidebar_auto_dormant_days'], 7));
  const dormantOptions = AUTO_DORMANT_DAYS.some((option) => option.value === dormantDaysValue)
    ? AUTO_DORMANT_DAYS
    : [...AUTO_DORMANT_DAYS, { value: dormantDaysValue, label: `${dormantDaysValue} 天` }];

  // ---- 会话页 ----
  const conversationPage = (
    <>
      <SettingsSection title="回复">
        <ToggleField
          itemID="row-auto-collapse-reasoning"
          label="自动折叠思考过程"
          checked={asBool(values['session.auto_collapse_reasoning'], true)}
          disabled={busy}
          error={errors['session.auto_collapse_reasoning']}
          onChange={(next) => void commit('session.auto_collapse_reasoning', next)}
        />
        <ToggleField
          itemID="row-pin-sent-message"
          label="发送后置顶消息"
          checked={asBool(values['session.pin_sent_message'], true)}
          disabled={busy}
          error={errors['session.pin_sent_message']}
          onChange={(next) => void commit('session.pin_sent_message', next)}
        />
        <ToggleField
          itemID="row-concise-replies"
          label="简洁 AI 回复"
          checked={asBool(values['session.concise_replies_enabled'])}
          disabled={busy}
          error={errors['session.concise_replies_enabled']}
          onChange={(next) => void commit('session.concise_replies_enabled', next)}
        />
      </SettingsSection>
      {compactSettings.some(({ key }) => hasKey(key)) && (
        <SettingsSection title="上下文压缩">
          {compactSettings.filter(({ key }) => hasKey(key)).map(({ key, id, label, min, max }) => (
            <DraftField key={key} itemID={id} label={label} min={min} max={max} required
              type="number" value={String(asNumber(values[key]))} disabled={busy} error={errors[key]}
              onCommit={(next) => void commit(key, Number(next))} />
          ))}
        </SettingsSection>
      )}
      {hasKey('source_control.auto_fetch') && (
        <SettingsSection title="版本控制">
          <ToggleField itemID="row-git-auto-fetch" label="自动抓取" checked={asBool(values['source_control.auto_fetch'], true)}
            disabled={busy} error={errors['source_control.auto_fetch']}
            onChange={(next) => void commit('source_control.auto_fetch', next)} />
        </SettingsSection>
      )}
      <SettingsSection title="权限">
        <ChoiceField
          itemID="row-mode"
          label="新会话默认权限档位"
          value={values['permissions.mode'] === 'autoAcceptEdits' ? 'auto' : asString(values['permissions.mode'], 'supervised')}
          options={PERMISSION_MODES}
          disabled={busy}
          error={errors['permissions.mode']}
          onChange={(next) => void commit('permissions.mode', next)}
        />
      </SettingsSection>
      <SettingsSection title="归档">
        <ChoiceField
          itemID="row-auto-dormant-days"
          label="无消息多少天后自动休眠"
          value={dormantDaysValue}
          options={dormantOptions}
          disabled={busy}
          error={errors['archive.sidebar_auto_dormant_days']}
          onChange={(next) => void commit('archive.sidebar_auto_dormant_days', Number(next))}
        />
      </SettingsSection>
    </>
  );

  // ---- 编排页 ----
  const showAfterComplete =
    hasKey('orchestration.review_mode') ||
    hasKey('orchestration.auto_merge') ||
    hasKey('orchestration.auto_push');

  const swarmPage = (
    <>
      <SettingsSection title="编排">
        <ToggleField
          itemID="row-swarm-enabled"
          label="启用编排"
          checked={asBool(values['orchestration.enabled'], true)}
          disabled={busy}
          error={errors['orchestration.enabled']}
          onChange={(next) => void commit('orchestration.enabled', next)}
        />
        <ChoiceField
          itemID="row-swarm-max-bees"
          label="并发上限"
          value={String(asNumber(values['orchestration.max_bees'], MAX_BEES_DEFAULT))}
          options={MAX_BEES_OPTIONS}
          disabled={busy}
          error={errors['orchestration.max_bees']}
          onChange={(next) => void commit('orchestration.max_bees', Number(next))}
        />
        <ToggleField
          itemID="row-swarm-auto-delegate"
          label="自动委托"
          checked={asBool(values['orchestration.auto_delegate'], true)}
          disabled={busy}
          error={errors['orchestration.auto_delegate']}
          onChange={(next) => void commit('orchestration.auto_delegate', next)}
        />
        <ToggleField
          itemID="row-swarm-lead-plans-only"
          label="主代理只规划"
          checked={asBool(values['orchestration.lead_plans_only'])}
          disabled={busy}
          error={errors['orchestration.lead_plans_only']}
          onChange={(next) => void commit('orchestration.lead_plans_only', next)}
        />
        {swarmTiersRow}
      </SettingsSection>
      {showAfterComplete && (
        <SettingsSection title="完成后">
          {hasKey('orchestration.review_mode') && (
            <ChoiceField
              itemID="row-swarm-review-bees"
              label="合并前审查"
              value={asString(values['orchestration.review_mode'], 'off')}
              options={REVIEW_MODE_OPTIONS}
              disabled={busy}
              error={errors['orchestration.review_mode']}
              onChange={(next) => void commit('orchestration.review_mode', next)}
            />
          )}
          {hasKey('orchestration.auto_merge') && (
            <ToggleField
              itemID="row-swarm-auto-merge"
              label="自动合并"
              checked={asBool(values['orchestration.auto_merge'])}
              disabled={busy}
              error={errors['orchestration.auto_merge']}
              onChange={(next) => void commit('orchestration.auto_merge', next)}
            />
          )}
          {hasKey('orchestration.auto_push') && (
            <ToggleField
              itemID="row-swarm-auto-push"
              label="自动推送"
              checked={asBool(values['orchestration.auto_push'])}
              disabled={busy}
              error={errors['orchestration.auto_push']}
              onChange={(next) => void commit('orchestration.auto_push', next)}
            />
          )}
        </SettingsSection>
      )}
    </>
  );

  // ---- 总管页 ----
  const stewardPage = (
    <>
      <SettingsSection title="总管">
        <ToggleField
          itemID="row-steward-enabled"
          label="启用总管"
          checked={asBool(values['steward.enabled'])}
          disabled={busy}
          error={errors['steward.enabled']}
          onChange={(next) => void commit('steward.enabled', next)}
        />
      </SettingsSection>
      <SettingsSection title="安静时段">
        <DraftField
          itemID="web-steward-quiet-start"
          label="安静时段开始"
          value={asString(values['steward.quiet_start'])}
          type="time"
          disabled={busy}
          error={errors['steward.quiet_start']}
          onCommit={(next) => void commit('steward.quiet_start', next)}
        />
        <DraftField
          itemID="web-steward-quiet-end"
          label="安静时段结束"
          value={asString(values['steward.quiet_end'])}
          type="time"
          disabled={busy}
          error={errors['steward.quiet_end']}
          onCommit={(next) => void commit('steward.quiet_end', next)}
        />
      </SettingsSection>
    </>
  );

  switch (page) {
    case 'conversation':
      return conversationPage;
    case 'swarm':
      return swarmPage;
    case 'steward':
      return stewardPage;
    case 'device':
      return hasKey('notifications.webhook_url') ? (
        <SettingsSection title="通知">
          <WebhookSettings save={setValue} busy={busy} />
        </SettingsSection>
      ) : null;
  }
};
