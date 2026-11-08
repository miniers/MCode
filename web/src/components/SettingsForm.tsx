// 移动端设置表单：按页渲染，每页按小节分组。
// 对应宿主 internal/remote/settings_ext_methods.go（白名单与类型）与
// internal/app/remote_settings.go（取值校验）；页、小节与文案照抄桌面版
// internal/ui/settings_index.go、internal/ui/settings.go 与 internal/ui/settings_page_*.go。
// 控件类型跟着校验走：布尔用开关、枚举用下拉、数字限定范围、时刻用 HH:MM。
// 改一项就立刻 settings.set 只提交这一键；失败时回滚并显示服务端错误。

import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useRemote } from '../context/RemoteContext';
import type { RemoteSettingsApi } from '../context/remoteSettingsApi';
import type { RemoteSettingsValue, RemoteSettingsValues } from '../types/generated';
import {
  SettingsAdvancedArea,
  SettingsRow,
  SettingsSection,
  SettingsStackedRow,
  SettingsSwitch
} from './settings/SettingsLayout';

export type SettingsFormPage =
  | 'conversation'
  | 'swarm'
  | 'steward'
  | 'general'
  | 'notifications'
  | 'archive';

interface SelectOption {
  value: string;
  label: string;
}

// 权限档位与桌面 permissions.mode 选项一致（internal/ui/settings_page_general.go:13）。
const PERMISSION_MODES: SelectOption[] = [
  { value: 'readonly', label: '只读' },
  { value: 'supervised', label: '监督' },
  { value: 'auto', label: '自动' },
  { value: 'fullAccess', label: '完全访问' }
];

// 自动休眠天数选项照抄桌面端（internal/ui/settings_page_archive.go:395）。
const AUTO_DORMANT_DAYS: SelectOption[] = [
  { value: '0', label: '关闭 (0)' },
  { value: '1', label: '1 天' },
  { value: '3', label: '3 天' },
  { value: '7', label: '7 天' },
  { value: '14', label: '14 天' },
  { value: '30', label: '30 天' }
];

const MAX_BEES_OPTIONS: SelectOption[] = [
  { value: '0', label: '自动 (8)' },
  ...[1, 2, 3, 4, 5, 6, 7, 8].map((count) => ({ value: String(count), label: String(count) }))
];

const MAX_BEES_MIN = 0;
const MAX_BEES_MAX = 8;
// 派发轮次、提级秒数与总管头像上限照 config（internal/config/config.go）。
const MAX_ROUNDS_MIN = 0;
const MAX_ROUNDS_MAX = 100;
const STEWARD_AVATAR_MAX = 2;
const STEWARD_NAME_MAX = 24;

// 工作空间模式选项与桌面选择器一致（internal/ui/settings_page_swarm.go）。
const WORKSPACE_MODE_OPTIONS: SelectOption[] = [
  { value: 'worktree', label: '独立副本 (Worktree)' },
  { value: 'shared', label: '共享工作区 (Shared)' }
];

// 合并前审查三档，与 config.ReviewMode* 一致。
const REVIEW_MODE_OPTIONS: SelectOption[] = [
  { value: 'off', label: '关闭' },
  { value: 'ai', label: 'AI 审查' },
  { value: 'human', label: '人工审查' }
];

// 审查模型的可选思考档位，与子代理档位抽屉一致；空值表示跟随主代理。
const REVIEW_EFFORT_OPTIONS: SelectOption[] = [
  { value: '', label: '跟随主代理' },
  { value: 'low', label: 'low' },
  { value: 'medium', label: 'medium' },
  { value: 'high', label: 'high' },
  { value: 'xhigh', label: 'xhigh' },
  { value: 'max', label: 'max' }
];

// 无人回答时转给主代理的等待时长选项，与桌面选择器一致（0 表示关闭）。
const FORWARD_QUESTION_OPTIONS: SelectOption[] = [
  { value: '0', label: '关闭' },
  { value: '60', label: '1 分钟' },
  { value: '180', label: '3 分钟' },
  { value: '300', label: '5 分钟' },
  { value: '600', label: '10 分钟' }
];

// 菜单栏额度面板能隐藏的直连供应商，与宿主 settingsTrayQuotaProviders 一致
// （internal/app/remote_settings.go:38）；勾选 = 写进 startup.tray_quota_hidden。
const TRAY_QUOTA_PROVIDERS: Array<{ id: string; label: string }> = [
  { id: 'claude', label: 'Claude' },
  { id: 'agy', label: 'Antigravity' },
  { id: 'zai', label: 'ZAI' },
  { id: 'cline', label: 'Cline' },
  { id: 'droid', label: 'Droid' }
];

// 小改动自动通过的行数上限，与 config.ReviewMaxChangedLines 一致（0 关闭）。
const REVIEW_MAX_CHANGED_LINES_MIN = 0;
const REVIEW_MAX_CHANGED_LINES_MAX = 500;

// 卡住提醒阈值：宿主只接受 0..1440 分钟（internal/app/remote_settings.go:32），
// 0 表示回到默认的 10 分钟。桌面端步进器更窄（1..120），这里按宿主范围放开。
const STALLED_ALERT_MINUTES_MIN = 0;
const STALLED_ALERT_MINUTES_MAX = 1440;

const asString = (value: RemoteSettingsValue | undefined, fallback = ''): string =>
  typeof value === 'string' ? value : fallback;

const asBool = (value: RemoteSettingsValue | undefined, fallback = false): boolean =>
  typeof value === 'boolean' ? value : fallback;

const asNumber = (value: RemoteSettingsValue | undefined, fallback = 0): number =>
  typeof value === 'number' && Number.isFinite(value) ? value : fallback;

// 字符串数组键（startup.tray_quota_hidden）读出来的是 string[]；其它形状一律当空。
const asStringList = (value: RemoteSettingsValue | undefined): string[] =>
  Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string') : [];

interface ToggleFieldProps {
  itemID?: string;
  label: string;
  note?: string;
  checked: boolean;
  disabled?: boolean;
  error?: string | null;
  onChange: (next: boolean) => void;
}

const ToggleField: React.FC<ToggleFieldProps> = ({
  itemID,
  label,
  note,
  checked,
  disabled,
  error,
  onChange
}) => (
  <SettingsRow
    itemID={itemID}
    title={label}
    desc={note}
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
  note?: string;
  value: string;
  options: SelectOption[];
  disabled?: boolean;
  error?: string | null;
  onChange: (next: string) => void;
}

const ChoiceField: React.FC<ChoiceFieldProps> = ({
  itemID,
  label,
  note,
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
      desc={note}
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
  note?: string;
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
  note,
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
    <SettingsStackedRow itemID={itemID} title={label} desc={note} error={localError || error}>
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
  advancedOpen: boolean;
  onAdvancedToggle: () => void;
  // 「子代理档位」入口由 Header 传入（它持有打开档位弹层的状态）。
  swarmTiersRow?: React.ReactNode;
}

export const SettingsForm: React.FC<SettingsFormProps> = ({
  api,
  page,
  advancedOpen,
  onAdvancedToggle,
  swarmTiersRow
}) => {
  const { providers } = useRemote();
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

  const failLocally = useCallback((key: string, message: string) => {
    setErrors((current) => ({ ...current, [key]: message }));
  }, []);

  const providerOptions = useMemo<SelectOption[]>(
    () => providers.map((provider) => ({ value: provider.id, label: provider.name || provider.id })),
    [providers]
  );

  const roleOptions = useCallback(
    (providerID: string): SelectOption[] => {
      const provider = providers.find((item) => item.id === providerID);
      if (!provider) return [{ value: '', label: '默认模型' }];
      return [
        { value: '', label: '默认模型' },
        ...provider.models.map((model) => ({ value: model.id, label: model.name || model.id }))
      ];
    },
    [providers]
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

  const commitStewardName = (next: string) => {
    const trimmed = next.trim();
    // 允许留空：留空时界面显示「总管」。
    if (Array.from(trimmed).length > STEWARD_NAME_MAX) {
      failLocally('steward.name', `最多 ${STEWARD_NAME_MAX} 个字`);
      return;
    }
    void commit('steward.name', trimmed);
  };

  const commitStewardAvatar = (next: string) => {
    // 与宿主校验一致：一个 emoji 或一个字（组合 emoji 最多两个 rune），留空用名字的第一个字。
    const trimmed = next.trim();
    if (Array.from(trimmed).length > STEWARD_AVATAR_MAX) {
      failLocally('steward.avatar', `最多 ${STEWARD_AVATAR_MAX} 个字（一个 emoji 或一个字）`);
      return;
    }
    void commit('steward.avatar', trimmed);
  };

  // ---- 会话页 ----
  const conversationPage = (
    <>
      <SettingsSection title="回复与思考">
        <ToggleField
          itemID="row-auto-collapse-reasoning"
          label="自动折叠思考过程"
          note="思考结束后自动收起，关闭则保持展开"
          checked={asBool(values['session.auto_collapse_reasoning'], true)}
          disabled={busy}
          error={errors['session.auto_collapse_reasoning']}
          onChange={(next) => void commit('session.auto_collapse_reasoning', next)}
        />
        <ToggleField
          itemID="row-pin-sent-message"
          label="发送后置顶消息"
          note="发送后把这条消息滚到时间线顶部，回复在下方展开；关闭则始终跟随到底部"
          checked={asBool(values['session.pin_sent_message'], true)}
          disabled={busy}
          error={errors['session.pin_sent_message']}
          onChange={(next) => void commit('session.pin_sent_message', next)}
        />
        <ToggleField
          itemID="row-concise-replies"
          label="简洁 AI 回复"
          note="内置提示词引导精简回复与代码注释"
          checked={asBool(values['session.concise_replies_enabled'])}
          disabled={busy}
          error={errors['session.concise_replies_enabled']}
          onChange={(next) => void commit('session.concise_replies_enabled', next)}
        />
      </SettingsSection>
    </>
  );

  // ---- 编排页 ----
  const swarmPage = (
    <>
      <SettingsSection title="总开关">
        <ToggleField
          itemID="row-swarm-enabled"
          label="启用编排"
          note="由主代理拆分任务，派给多个子代理并行执行，完成后统一审查合并"
          checked={asBool(values['orchestration.enabled'], true)}
          disabled={busy}
          error={errors['orchestration.enabled']}
          onChange={(next) => void commit('orchestration.enabled', next)}
        />
      </SettingsSection>
      <SettingsSection title="子代理">
        {hasKey('orchestration.workspace_mode') && (
          <ChoiceField
            itemID="row-swarm-workspace-mode"
            label="工作空间模式"
            note="每个子代理在独立工作树里改代码，互不干扰"
            value={asString(values['orchestration.workspace_mode'], 'worktree')}
            options={WORKSPACE_MODE_OPTIONS}
            disabled={busy}
            error={errors['orchestration.workspace_mode']}
            onChange={(next) => void commit('orchestration.workspace_mode', next)}
          />
        )}
        <ChoiceField
          itemID="row-swarm-max-bees"
          label="并发上限"
          note={`默认 8；超出的子代理排队等待（${MAX_BEES_MIN}..${MAX_BEES_MAX}）`}
          value={String(asNumber(values['orchestration.max_bees'], MAX_BEES_MAX))}
          options={MAX_BEES_OPTIONS}
          disabled={busy}
          error={errors['orchestration.max_bees']}
          onChange={(next) => void commit('orchestration.max_bees', Number(next))}
        />
        {hasKey('orchestration.max_rounds') && (
          <DraftField
            itemID="row-swarm-max-rounds"
            label="最多派发轮次"
            note={`主代理最多派发几轮子代理，0 为不限（${MAX_ROUNDS_MIN}..${MAX_ROUNDS_MAX}）`}
            value={String(asNumber(values['orchestration.max_rounds']))}
            type="number"
            min={MAX_ROUNDS_MIN}
            max={MAX_ROUNDS_MAX}
            disabled={busy}
            error={errors['orchestration.max_rounds']}
            onCommit={(next) => void commit('orchestration.max_rounds', Number(next))}
          />
        )}
        {hasKey('orchestration.retry_failed') && (
          <ToggleField
            itemID="row-swarm-retry-failed"
            label="失败时自动重试"
            note="子代理遇到异常失败时自动重试一次"
            checked={asBool(values['orchestration.retry_failed'])}
            disabled={busy}
            error={errors['orchestration.retry_failed']}
            onChange={(next) => void commit('orchestration.retry_failed', next)}
          />
        )}
        {hasKey('orchestration.auto_clear_finished') && (
          <ToggleField
            itemID="row-swarm-auto-clear-finished"
            label="自动清理已完成子代理"
            note="面板只留进行中的子代理"
            checked={asBool(values['orchestration.auto_clear_finished'])}
            disabled={busy}
            error={errors['orchestration.auto_clear_finished']}
            onChange={(next) => void commit('orchestration.auto_clear_finished', next)}
          />
        )}
      </SettingsSection>
      <SettingsSection title="消息流向">
        <ToggleField
          itemID="row-swarm-auto-delegate"
          label="自动委托"
          note="需要拆分任务时由主代理自动派给子代理"
          checked={asBool(values['orchestration.auto_delegate'], true)}
          disabled={busy}
          error={errors['orchestration.auto_delegate']}
          onChange={(next) => void commit('orchestration.auto_delegate', next)}
        />
        <ToggleField
          itemID="row-swarm-lead-plans-only"
          label="主代理只规划"
          note="开启后主代理只负责规划和审查，所有改动都派给子代理"
          checked={asBool(values['orchestration.lead_plans_only'])}
          disabled={busy}
          error={errors['orchestration.lead_plans_only']}
          onChange={(next) => void commit('orchestration.lead_plans_only', next)}
        />
        <ToggleField
          itemID="row-swarm-send-to-bees"
          label="消息直接交给子代理"
          note="开启后发送的消息直接交给子代理处理"
          checked={asBool(values['orchestration.always_bees'])}
          disabled={busy}
          error={errors['orchestration.always_bees']}
          onChange={(next) => void commit('orchestration.always_bees', next)}
        />
      </SettingsSection>
      <SettingsSection title="子代理完成时">
        {hasKey('orchestration.review_mode') && (
          <>
            <ChoiceField
              itemID="row-swarm-review-bees"
              label="合并前审查"
              note="AI 审查由审查模型自动判断，人工审查等你批准；两种都在审查有结论后才汇报给主代理"
              value={asString(values['orchestration.review_mode'], 'off')}
              options={REVIEW_MODE_OPTIONS}
              disabled={busy}
              error={errors['orchestration.review_mode']}
              onChange={(next) => void commit('orchestration.review_mode', next)}
            />
            {asString(values['orchestration.review_mode'], 'off') === 'ai' && (
              <SettingsRow
                itemID="row-swarm-review-model"
                title="审查模型"
                desc="留空自动选择最便宜的可用审查模型"
                control={
                  <div className="settings-role-controls">
                    <select
                      className="settings-select"
                      value={asString(values['orchestration.review_provider'])}
                      disabled={busy}
                      aria-label="审查模型 provider"
                      onChange={(event) => {
                        const next = event.target.value;
                        const prev = asString(values['orchestration.review_provider']);
                        void (async () => {
                          await commit('orchestration.review_provider', next);
                          // 切换 provider 后旧模型不属于新 provider，清空回该 provider 的默认模型。
                          if (next !== prev) await commit('orchestration.review_model', '');
                        })();
                      }}
                    >
                      {[{ value: '', label: '跟随主代理' }, ...providerOptions].map((option) => (
                        <option key={option.value || 'empty'} value={option.value}>
                          {option.label}
                        </option>
                      ))}
                    </select>
                    <select
                      className="settings-select"
                      value={asString(values['orchestration.review_model'])}
                      disabled={busy || !asString(values['orchestration.review_provider'])}
                      aria-label="审查模型 model"
                      onChange={(event) => void commit('orchestration.review_model', event.target.value)}
                    >
                      {roleOptions(asString(values['orchestration.review_provider'])).map((option) => (
                        <option key={option.value || 'empty'} value={option.value}>
                          {option.value === '' ? '跟随主代理' : option.label}
                        </option>
                      ))}
                    </select>
                    <select
                      className="settings-select"
                      value={asString(values['orchestration.review_effort'])}
                      disabled={busy}
                      aria-label="审查模型思考档位"
                      onChange={(event) => void commit('orchestration.review_effort', event.target.value)}
                    >
                      {REVIEW_EFFORT_OPTIONS.map((option) => (
                        <option key={option.value || 'empty'} value={option.value}>
                          {option.label}
                        </option>
                      ))}
                    </select>
                    <button
                      type="button"
                      className="settings-action"
                      disabled={busy}
                      onClick={() => {
                        void (async () => {
                          await commit('orchestration.review_provider', '');
                          await commit('orchestration.review_model', '');
                          await commit('orchestration.review_effort', '');
                        })();
                      }}
                    >
                      <span>跟随主代理</span>
                    </button>
                  </div>
                }
              />
            )}
            {asString(values['orchestration.review_mode'], 'off') === 'ai' &&
              hasKey('orchestration.review_check_command') && (
                <DraftField
                  itemID="row-swarm-review-check-command"
                  label="检查命令"
                  note="全局默认；桌面端可按项目覆盖"
                  placeholder="留空自动识别（Go 项目跑 go build 和 go vet）"
                  value={asString(values['orchestration.review_check_command'])}
                  disabled={busy}
                  error={errors['orchestration.review_check_command']}
                  onCommit={(next) => void commit('orchestration.review_check_command', next)}
                />
              )}
            {asString(values['orchestration.review_mode'], 'off') === 'ai' &&
              hasKey('orchestration.review_max_changed_lines') && (
                <DraftField
                  itemID="row-swarm-review-max-changed-lines"
                  label="自动通过行数上限"
                  note="改动行数不超过该值且未越界时不调用模型；0 表示关闭"
                  value={String(asNumber(values['orchestration.review_max_changed_lines'], 40))}
                  type="number"
                  min={REVIEW_MAX_CHANGED_LINES_MIN}
                  max={REVIEW_MAX_CHANGED_LINES_MAX}
                  disabled={busy}
                  error={errors['orchestration.review_max_changed_lines']}
                  onCommit={(next) => void commit('orchestration.review_max_changed_lines', Number(next))}
                />
              )}
          </>
        )}
        {hasKey('orchestration.auto_merge') && (
          <ToggleField
            itemID="row-swarm-auto-merge"
            label="自动合并"
            note="默认关闭；全部完成后合并到当前分支并清理副本"
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
            note="默认关闭；开启后自动推送分支并创建 PR"
            checked={asBool(values['orchestration.auto_push'])}
            disabled={busy}
            error={errors['orchestration.auto_push']}
            onChange={(next) => void commit('orchestration.auto_push', next)}
          />
        )}
      </SettingsSection>
      {swarmTiersRow && (
        <SettingsSection title="消息与提级">
          {swarmTiersRow}
        </SettingsSection>
      )}
      <SettingsAdvancedArea
        count={hasKey('orchestration.auto_forward_question_seconds') ? 1 : 0}
        open={advancedOpen}
        onToggle={onAdvancedToggle}
      >
        <SettingsSection title="消息与提级">
          <ChoiceField
            itemID="row-swarm-auto-forward-question"
            label="无人回答时转给主代理"
            note="超时后由主代理先答，拿不准再交给你"
            value={String(asNumber(values['orchestration.auto_forward_question_seconds']))}
            options={FORWARD_QUESTION_OPTIONS}
            disabled={busy}
            error={errors['orchestration.auto_forward_question_seconds']}
            onChange={(next) => void commit('orchestration.auto_forward_question_seconds', Number(next))}
          />
        </SettingsSection>
      </SettingsAdvancedArea>
    </>
  );

  // ---- 总管页：总管与安静时段（与编排分开，手机上不用滚很长） ----
  const stewardPage = (
    <>
      <SettingsSection title="总管">
        <ToggleField
          itemID="row-steward-enabled"
          label="启用总管"
          note="开启后由它在后台盯会话和看板"
          checked={asBool(values['steward.enabled'])}
          disabled={busy}
          error={errors['steward.enabled']}
          onChange={(next) => void commit('steward.enabled', next)}
        />
        {hasKey('steward.cli_fallback') && (
          <ToggleField
            itemID="row-steward-cli-fallback"
            label="没有 API Key 时用本机 CLI"
            checked={asBool(values['steward.cli_fallback'])}
            disabled={busy}
            error={errors['steward.cli_fallback']}
            onChange={(next) => void commit('steward.cli_fallback', next)}
          />
        )}
        <DraftField
          itemID="row-steward-name"
          label="昵称"
          note={`最多 ${STEWARD_NAME_MAX} 个字，留空时显示「总管」`}
          value={asString(values['steward.name'])}
          disabled={busy}
          error={errors['steward.name']}
          onCommit={commitStewardName}
        />
        {hasKey('steward.avatar') && (
          <DraftField
            itemID="row-steward-avatar"
            label="头像"
            note="填一个 emoji 或一个字，留空用名字的第一个字"
            value={asString(values['steward.avatar'])}
            placeholder="🙂"
            disabled={busy}
            error={errors['steward.avatar']}
            onCommit={commitStewardAvatar}
          />
        )}
      </SettingsSection>
      <SettingsSection title="安静时段">
        <DraftField
          itemID="web-steward-quiet-start"
          label="安静时段开始"
          note="本地时间 HH:MM，留空表示不设开始时刻"
          value={asString(values['steward.quiet_start'])}
          type="time"
          disabled={busy}
          error={errors['steward.quiet_start']}
          onCommit={(next) => void commit('steward.quiet_start', next)}
        />
        <DraftField
          itemID="web-steward-quiet-end"
          label="安静时段结束"
          note="本地时间 HH:MM，可以跨午夜；留空表示不设结束时刻"
          value={asString(values['steward.quiet_end'])}
          type="time"
          disabled={busy}
          error={errors['steward.quiet_end']}
          onCommit={(next) => void commit('steward.quiet_end', next)}
        />
      </SettingsSection>
    </>
  );

  // ---- 通用页 ----
  // 菜单栏额度面板的隐藏列表是字符串数组，逐项勾选后整组写回。
  const trayHidden = asStringList(values['startup.tray_quota_hidden']);
  const toggleTrayQuotaHidden = (id: string, hidden: boolean) => {
    const next = new Set(trayHidden);
    if (hidden) next.add(id);
    else next.delete(id);
    // 按宿主允许的顺序提交，避免同一组勾选因点击顺序不同产生不同入参。
    const ordered = TRAY_QUOTA_PROVIDERS.map((item) => item.id).filter((itemID) => next.has(itemID));
    void commit('startup.tray_quota_hidden', ordered);
  };

  const generalPage = (
    <>
      <SettingsSection title="权限">
        <ChoiceField
          itemID="row-mode"
          label="新会话默认权限档位"
          note="新会话按这一档执行权限限制"
          value={values['permissions.mode'] === 'autoAcceptEdits' ? 'auto' : asString(values['permissions.mode'], 'supervised')}
          options={PERMISSION_MODES}
          disabled={busy}
          error={errors['permissions.mode']}
          onChange={(next) => void commit('permissions.mode', next)}
        />
      </SettingsSection>
      {hasKey('startup.tray_quota_hidden') && (
        <SettingsSection title="启动与托盘">
          <SettingsStackedRow
            itemID="row-tray-quota-hidden"
            title="菜单栏额度面板中隐藏"
            desc="勾选的直连供应商不在 Mac 菜单栏的额度面板中显示，取消勾选即恢复显示"
            error={errors['startup.tray_quota_hidden']}
          >
            <div className="flex flex-wrap gap-x-4 gap-y-2 text-xs text-[var(--foreground)]">
              {TRAY_QUOTA_PROVIDERS.map((provider) => (
                <label key={provider.id} className="flex items-center gap-1.5">
                  <input
                    type="checkbox"
                    className="h-4 w-4"
                    checked={trayHidden.includes(provider.id)}
                    disabled={busy}
                    onChange={(event) => toggleTrayQuotaHidden(provider.id, event.target.checked)}
                  />
                  {provider.label}
                </label>
              ))}
            </div>
          </SettingsStackedRow>
        </SettingsSection>
      )}
    </>
  );

  // ---- 通知页 ----
  // 这些开关都改 Mac 端的系统通知与提示音，Web 端只是远程改配置。
  const stalledEnabled = asBool(values['notifications.notify_when_stalled'], true);
  const notificationsPage = (
    <SettingsSection title="通知与提示音">
      <SettingsRow
        title="以下通知在 Mac 上生效"
        desc="开关改的是 Mac 上的系统通知与提示音，手机不会收到这些提醒"
      />
      <ToggleField
        itemID="row-notify-finished"
        label="完成后通知"
        note="会话轮次结束时发送系统通知"
        checked={asBool(values['notifications.notify_when_finished'])}
        disabled={busy}
        error={errors['notifications.notify_when_finished']}
        onChange={(next) => void commit('notifications.notify_when_finished', next)}
      />
      <ToggleField
        itemID="row-notify-foreground"
        label="前台也发送通知"
        note="应用在前台时也发系统通知"
        checked={asBool(values['notifications.notify_when_foreground'])}
        disabled={busy}
        error={errors['notifications.notify_when_foreground']}
        onChange={(next) => void commit('notifications.notify_when_foreground', next)}
      />
      <ToggleField
        itemID="row-notify-approval"
        label="待审批时通知"
        note="有新的审批请求时发送系统通知"
        checked={asBool(values['notifications.notify_on_approval'])}
        disabled={busy}
        error={errors['notifications.notify_on_approval']}
        onChange={(next) => void commit('notifications.notify_on_approval', next)}
      />
      <ToggleField
        itemID="row-finish-sound"
        label="完成提示音"
        note="会话轮次结束时播放轻提示音"
        checked={asBool(values['notifications.play_finish_sound'])}
        disabled={busy}
        error={errors['notifications.play_finish_sound']}
        onChange={(next) => void commit('notifications.play_finish_sound', next)}
      />
      <ToggleField
        itemID="row-chime-attention"
        label="需要介入时提示音"
        note="Agent 提问或等待用户决策时播放专属提示音"
        checked={asBool(values['notifications.chime_when_attention_needed'], true)}
        disabled={busy}
        error={errors['notifications.chime_when_attention_needed']}
        onChange={(next) => void commit('notifications.chime_when_attention_needed', next)}
      />
      <ToggleField
        itemID="row-dormant-sound"
        label="会话休眠提示音"
        note="会话休眠时播放轻提示音"
        checked={asBool(values['notifications.dormant_sound'], true)}
        disabled={busy}
        error={errors['notifications.dormant_sound']}
        onChange={(next) => void commit('notifications.dormant_sound', next)}
      />
      <ToggleField
        itemID="row-notify-stalled"
        label="会话卡住时提醒"
        note="会话长时间没有进展时发送系统通知"
        checked={stalledEnabled}
        disabled={busy}
        error={errors['notifications.notify_when_stalled']}
        onChange={(next) => void commit('notifications.notify_when_stalled', next)}
      />
      <DraftField
        itemID="row-stalled-minutes"
        label="无进展多久算卡住（分钟）"
        note="默认 10 分钟；多少分钟没输出就提醒你，0 表示用默认值；关闭上面的提醒后不可编辑"
        type="number"
        min={STALLED_ALERT_MINUTES_MIN}
        max={STALLED_ALERT_MINUTES_MAX}
        required
        value={String(asNumber(values['notifications.stalled_alert_minutes'], 10))}
        disabled={busy || !stalledEnabled}
        error={errors['notifications.stalled_alert_minutes']}
        onCommit={(next) => void commit('notifications.stalled_alert_minutes', Number(next))}
      />
    </SettingsSection>
  );

  // ---- 归档页 ----
  const archivePage = (
    <>
      <SettingsSection title="自动休眠">
        <ChoiceField
          itemID="row-auto-dormant-days"
          label="无消息多少天后自动休眠"
          note="默认 7 天，0 为不自动休眠"
          value={dormantDaysValue}
          options={dormantOptions}
          disabled={busy}
          error={errors['archive.sidebar_auto_dormant_days']}
          onChange={(next) => void commit('archive.sidebar_auto_dormant_days', Number(next))}
        />
      </SettingsSection>
      {hasKey('archive.confirm_before_deleting') && (
        <SettingsSection title="会话维护">
          <ToggleField
            itemID="row-confirm-delete"
            label="删除会话前确认"
            note="删除会话前弹窗确认一次"
            checked={asBool(values['archive.confirm_before_deleting'])}
            disabled={busy}
            error={errors['archive.confirm_before_deleting']}
            onChange={(next) => void commit('archive.confirm_before_deleting', next)}
          />
        </SettingsSection>
      )}
    </>
  );

  switch (page) {
    case 'conversation':
      return conversationPage;
    case 'swarm':
      return swarmPage;
    case 'steward':
      return stewardPage;
    case 'general':
      return generalPage;
    case 'notifications':
      return notificationsPage;
    case 'archive':
      return archivePage;
  }
};
