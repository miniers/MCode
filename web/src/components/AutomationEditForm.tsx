// 自动化新建/编辑表单：字段、默认值与校验对齐桌面端 internal/ui/automation.go 的编辑表单
// （name/prompt/scheduleType/interval/timeOfDay/dayOfWeek/cron/once/stopWhen/enabled/失败上限/最大运行次数，
// 以及模型供应商/模型/推理强度/权限模式）。
// 校验写成导出的纯函数，Web 端没有测试框架，这里只保证调用方能直接复用。

import React, { useCallback, useMemo, useState } from 'react';
import { useRemote } from '../context/RemoteContext';
import type {
  RemoteAutomationExtAutomation,
  RemoteAutomationExtInput,
  RemoteAutomationExtSchedule
} from '../context/remoteAutomationApi';

/** 调度类型选项，文案与桌面端 scheduleTypeOptions 一致（internal/ui/automation.go:860）。 */
export const SCHEDULE_TYPE_OPTIONS: ReadonlyArray<{ key: string; label: string }> = [
  { key: 'interval', label: '每隔N分钟' },
  { key: 'daily', label: '每天固定时间' },
  { key: 'weekdays', label: '工作日固定时间' },
  { key: 'weekly', label: '每周指定日' },
  { key: 'cron', label: 'Cron表达式' },
  { key: 'once', label: '单次定时运行' }
];

/** 星期几选项（周日开始），与桌面端 dayOfWeekOptions 一致。 */
export const DAY_OF_WEEK_OPTIONS: ReadonlyArray<{ day: number; label: string }> = [
  { day: 0, label: '周日' },
  { day: 1, label: '周一' },
  { day: 2, label: '周二' },
  { day: 3, label: '周三' },
  { day: 4, label: '周四' },
  { day: 5, label: '周五' },
  { day: 6, label: '周六' }
];

/** 间隔分钟档位：与桌面端 intervalMinuteOptions 相同的八档。 */
export const INTERVAL_MINUTE_OPTIONS: readonly number[] = [5, 15, 30, 60, 120, 240, 720, 1440];

/** 连续失败上限的上下限，与桌面端步进器一致（1–10）。 */
export const MAX_CONSECUTIVE_FAILURES_MIN = 1;
export const MAX_CONSECUTIVE_FAILURES_MAX = 10;

/**
 * 权限模式选项，文案与桌面端 automationRuntimeModeOptions 一致
 * （internal/ui/automation.go:780）；空串表示跟随默认。
 */
export const AUTOMATION_RUNTIME_MODES: ReadonlyArray<{ value: string; label: string }> = [
  { value: '', label: '跟随默认' },
  { value: 'readonly', label: '只读' },
  { value: 'supervised', label: '需审批' },
  { value: 'autoAcceptEdits', label: '自动接受编辑' },
  { value: 'auto', label: '自动' },
  { value: 'fullAccess', label: '完全访问' }
];

/** 推理强度中文标签，与桌面端 effortLabel 一致（internal/ui/effort_slider.go:104）。 */
const EFFORT_LABELS: Record<string, string | undefined> = {
  none: '无',
  minimal: '最低',
  low: '低',
  medium: '中',
  high: '高',
  xhigh: '超高',
  'extra-high': '超高',
  extra_high: '超高',
  'extra high': '超高',
  max: '最大',
  ultra: '最大'
};

export function effortLabel(raw: string): string {
  return EFFORT_LABELS[raw.trim().toLowerCase()] ?? raw;
}

/** 表单值：全部以字符串保存，便于直接绑定输入框；提交前统一转成入参。 */
export interface AutomationFormValues {
  projectID: string;
  name: string;
  prompt: string;
  stopWhen: string;
  scheduleType: string;
  /** 运行间隔（分钟），允许小数，如 1.5 表示 90 秒。 */
  intervalMinutes: string;
  /** 执行时刻，必须是两位时两位分的 HH:MM。 */
  timeOfDay: string;
  dayOfWeek: number;
  cronExpression: string;
  /** 单次执行时间，桌面端格式 YYYY-MM-DD HH:MM（宿主也接受 RFC3339）。 */
  onceDate: string;
  /** 时区：表单不编辑，原样回传，避免保存一次就丢。 */
  timezone: string;
  enabled: boolean;
  maxConsecutiveFailures: string;
  maxIterations: string;
  /** 模型与权限：留空（空串）表示跟随默认，宿主 update 会把它恢复成默认值。 */
  provider: string;
  model: string;
  effort: string;
  runtimeMode: string;
}

const HH_MM_PATTERN = /^([01]\d|2[0-3]):[0-5]\d$/;
const DATE_TIME_PATTERN = /^(\d{4})-(\d{2})-(\d{2}) (\d{2}):(\d{2})$/;

/** 把时间格式化成桌面端表单使用的本地时间文本 YYYY-MM-DD HH:MM。 */
export function formatLocalDateTime(date: Date): string {
  const pad = (value: number) => String(value).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(
    date.getHours()
  )}:${pad(date.getMinutes())}`;
}

/** 把宿主的 schedule.date（RFC3339 或本地文本）转成表单可编辑的本地时间文本。 */
export function toFormDateTimeText(raw: string | undefined, fallback: Date): string {
  const text = (raw ?? '').trim();
  if (!text) return formatLocalDateTime(fallback);
  if (DATE_TIME_PATTERN.test(text)) return text;
  const parsed = new Date(text);
  if (Number.isNaN(parsed.getTime())) return formatLocalDateTime(fallback);
  return formatLocalDateTime(parsed);
}

/** 新建时的默认表单值，默认值对齐桌面端 resetToNewAutomation。 */
export function defaultAutomationFormValues(projectID: string): AutomationFormValues {
  return {
    projectID,
    name: '',
    prompt: '',
    stopWhen: '',
    scheduleType: 'interval',
    intervalMinutes: '60',
    timeOfDay: '09:00',
    dayOfWeek: 1,
    cronExpression: '*/15 * * * *',
    onceDate: formatLocalDateTime(new Date(Date.now() + 24 * 60 * 60 * 1000)),
    timezone: '',
    enabled: true,
    maxConsecutiveFailures: '3',
    maxIterations: '0',
    provider: '',
    model: '',
    effort: '',
    runtimeMode: ''
  };
}

/** 秒数转间隔分钟文本，与桌面端 formatIntervalMinutesText 一致（1.5 分钟保持原样）。 */
export function intervalMinutesText(seconds: number | undefined): string {
  if (!seconds || seconds <= 0) return '60';
  return String(seconds / 60);
}

/** 编辑已有自动化时，用其现有字段填充表单。 */
export function automationFormValues(
  projectID: string,
  automation: RemoteAutomationExtAutomation | null
): AutomationFormValues {
  if (!automation) return defaultAutomationFormValues(projectID);
  const schedule = automation.schedule;
  const scheduleType = schedule.type === '' ? 'interval' : schedule.type;
  return {
    // update 不能改项目，这里用自动化自己的项目，避免显示成别的项目。
    projectID: automation.projectID ?? projectID,
    name: automation.name,
    prompt: automation.prompt,
    stopWhen: automation.stopWhen ?? '',
    scheduleType,
    intervalMinutes: intervalMinutesText(schedule.seconds),
    timeOfDay: schedule.timeOfDay && schedule.timeOfDay !== '' ? schedule.timeOfDay : '09:00',
    dayOfWeek: typeof schedule.dayOfWeek === 'number' ? schedule.dayOfWeek : 1,
    cronExpression: schedule.expression && schedule.expression !== '' ? schedule.expression : '*/15 * * * *',
    onceDate: toFormDateTimeText(schedule.date, new Date(Date.now() + 24 * 60 * 60 * 1000)),
    timezone: schedule.timezone ?? '',
    enabled: automation.enabled,
    maxConsecutiveFailures: String(
      automation.maxConsecutiveFailures > 0 ? automation.maxConsecutiveFailures : 3
    ),
    maxIterations: String(automation.maxIterations),
    // 编辑时原值回显；留空（或改成「跟随默认」）提交空串，宿主会恢复默认模型与权限。
    provider: (automation.provider ?? '').trim(),
    model: (automation.model ?? '').trim(),
    effort: (automation.effort ?? '').trim(),
    runtimeMode: (automation.runtimeMode ?? '').trim()
  };
}

/** 解析整数输入；空串或非法值返回 null。 */
function parseIntStrict(raw: string): number | null {
  const text = raw.trim();
  if (!/^-?\d+$/.test(text)) return null;
  const value = Number.parseInt(text, 10);
  return Number.isFinite(value) ? value : null;
}

/** 校验单次执行时间文本：格式必须是 YYYY-MM-DD HH:MM，且是真实存在的时刻。 */
export function isValidDateTimeText(raw: string): boolean {
  const match = DATE_TIME_PATTERN.exec(raw.trim());
  if (!match) return false;
  const [, year, month, day, hour, minute] = match;
  const parsed = new Date(
    Number(year),
    Number(month) - 1,
    Number(day),
    Number(hour),
    Number(minute),
    0,
    0
  );
  return (
    parsed.getFullYear() === Number(year) &&
    parsed.getMonth() === Number(month) - 1 &&
    parsed.getDate() === Number(day) &&
    parsed.getHours() === Number(hour) &&
    parsed.getMinutes() === Number(minute)
  );
}

/**
 * 校验表单，返回第一条错误文案；全部通过返回 null。
 * 规则与桌面端 saveAutomation 及宿主 automationExtNormalizeInput 一致：
 * timeOfDay 必须是两位小时的 HH:MM（宿主会拒绝 9:00），cron 必须五个字段。
 */
export function validateAutomationForm(values: AutomationFormValues): string | null {
  if (values.projectID.trim() === '') return '请选择项目';
  if (values.name.trim() === '') return '自动化名称不能为空';
  if (values.prompt.trim() === '') return 'Prompt 不能为空';

  const failures = parseIntStrict(values.maxConsecutiveFailures);
  if (
    failures === null ||
    failures < MAX_CONSECUTIVE_FAILURES_MIN ||
    failures > MAX_CONSECUTIVE_FAILURES_MAX
  ) {
    return `最多连续失败次数须为 ${MAX_CONSECUTIVE_FAILURES_MIN} 到 ${MAX_CONSECUTIVE_FAILURES_MAX} 的整数`;
  }
  const iterations = values.maxIterations.trim() === '' ? 0 : parseIntStrict(values.maxIterations);
  if (iterations === null || iterations < 0) return '最大运行次数须为非负整数';

  switch (values.scheduleType) {
    case 'interval': {
      const minutes = Number(values.intervalMinutes.trim());
      if (!Number.isFinite(minutes) || minutes <= 0) return '运行间隔必须大于 0 分钟';
      const seconds = Math.round(minutes * 60);
      if (seconds < 1) return '运行间隔必须大于 0 分钟';
      return null;
    }
    case 'daily':
    case 'weekdays':
      if (!HH_MM_PATTERN.test(values.timeOfDay.trim())) return '时间格式必须为两位小时的 HH:MM，例如 09:30';
      return null;
    case 'weekly':
      if (!HH_MM_PATTERN.test(values.timeOfDay.trim())) return '时间格式必须为两位小时的 HH:MM，例如 09:30';
      if (values.dayOfWeek < 0 || values.dayOfWeek > 6) return '请选择星期几';
      return null;
    case 'cron':
      if (values.cronExpression.trim().split(/\s+/).filter(Boolean).length !== 5) {
        return 'cron 表达式必须包含五个字段，例如 */15 * * * *';
      }
      return null;
    case 'once':
      if (!isValidDateTimeText(values.onceDate)) return '单次时间格式必须为 YYYY-MM-DD HH:MM，例如 2026-10-20 14:00';
      return null;
    default:
      return '请选择有效的调度类型';
  }
}

/** 用表单值组装 RemoteAutomationExtInput；调用前应先通过 validateAutomationForm。 */
export function buildAutomationInput(values: AutomationFormValues): RemoteAutomationExtInput {
  const schedule: RemoteAutomationExtSchedule = { type: values.scheduleType };
  if (values.timezone.trim() !== '') schedule.timezone = values.timezone.trim();
  switch (values.scheduleType) {
    case 'interval':
      // 与桌面端一致：分钟换算成秒，非预设值（如 1.5 分钟）也能原样提交。
      schedule.seconds = Math.max(1, Math.round(Number(values.intervalMinutes.trim()) * 60));
      break;
    case 'daily':
    case 'weekdays':
      schedule.timeOfDay = values.timeOfDay.trim();
      break;
    case 'weekly':
      schedule.timeOfDay = values.timeOfDay.trim();
      schedule.dayOfWeek = values.dayOfWeek;
      break;
    case 'cron':
      schedule.expression = values.cronExpression.trim();
      break;
    case 'once':
      schedule.date = values.onceDate.trim();
      break;
    default:
      break;
  }

  const iterations = values.maxIterations.trim() === '' ? 0 : Number.parseInt(values.maxIterations.trim(), 10);
  const input: RemoteAutomationExtInput = {
    name: values.name.trim(),
    prompt: values.prompt.trim(),
    schedule,
    enabled: values.enabled,
    maxConsecutiveFailures: Number.parseInt(values.maxConsecutiveFailures.trim(), 10),
    maxIterations: Number.isFinite(iterations) ? iterations : 0
  };
  const stopWhen = values.stopWhen.trim();
  if (stopWhen !== '') input.stopWhen = stopWhen;
  // 模型与权限始终带上：空串=恢复默认，非空=显式指定（宿主用指针区分缺省与空串，见 internal/app/remote_automation.go:141）。
  const provider = values.provider.trim();
  // 没选供应商时不能带模型，没选模型时不能带强度，避免落下半截配置。
  const model = provider === '' ? '' : values.model.trim();
  const effort = model === '' ? '' : values.effort.trim();
  input.provider = provider;
  input.model = model;
  input.effort = effort;
  const runtimeMode = values.runtimeMode.trim();
  input.runtimeMode = AUTOMATION_RUNTIME_MODES.some((mode) => mode.value === runtimeMode)
    ? (runtimeMode as RemoteAutomationExtInput['runtimeMode'])
    : '';
  return input;
}

const INPUT_CLASS = 'pairing-input w-full min-w-0 text-sm';
const TEXTAREA_CLASS = 'pairing-input min-h-24 w-full min-w-0 resize-y py-2 text-sm';
const LABEL_CLASS = 'text-mc-caption font-medium text-[var(--foreground-muted)]';
const CHIP_CLASS =
  'min-h-[32px] rounded-xl border border-[var(--border)] bg-[var(--card)] px-2.5 text-mc-caption font-medium ' +
  'text-[var(--foreground)] transition disabled:opacity-45';

export interface AutomationEditFormProps {
  /** 为空表示新建。 */
  automation: RemoteAutomationExtAutomation | null;
  /** 可选项目，新建时展示选择器；编辑时项目不可改（宿主 update 不接受 projectID）。 */
  projects: ReadonlyArray<{ id: string; name: string }>;
  /** 新建时的默认项目，通常是当前项目。 */
  defaultProjectID: string;
  saving: boolean;
  /** 后端返回的错误，由调用方展示。 */
  error: string | null;
  onCancel: () => void;
  onSubmit: (projectID: string, input: RemoteAutomationExtInput) => void;
}

/** 自动化新建/编辑表单；适配手机宽度，所有控件占满一行、按钮保持触控高度。 */
export const AutomationEditForm: React.FC<AutomationEditFormProps> = ({
  automation,
  projects,
  defaultProjectID,
  saving,
  error,
  onCancel,
  onSubmit
}) => {
  const isNew = automation === null;
  const [values, setValues] = useState<AutomationFormValues>(() =>
    automationFormValues(defaultProjectID, automation)
  );
  const [formError, setFormError] = useState<string | null>(null);

  const patch = useCallback((partial: Partial<AutomationFormValues>) => {
    setValues((prev) => ({ ...prev, ...partial }));
    setFormError(null);
  }, []);

  const intervalOptions = useMemo(() => {
    const presets = INTERVAL_MINUTE_OPTIONS.map((minutes) => String(minutes));
    const current = values.intervalMinutes.trim();
    // 原始间隔不在八档内（如 1.5 分钟）时补一个选项，避免打开表单就被吸附成预设值。
    if (current !== '' && !presets.includes(current)) return [...presets, current];
    return presets;
  }, [values.intervalMinutes]);

  // 供应商与模型列表来自 Web 端已有的 providers（与设置页的模型下拉同源）；
  // 旧值不在当前列表里时补一项，避免打开编辑表单就显示成第一项。
  const { providers } = useRemote();
  const providerOptions = useMemo(() => {
    const list = providers.map((provider) => ({
      value: provider.id,
      label: provider.name || provider.id
    }));
    const current = values.provider.trim();
    if (current !== '' && !list.some((option) => option.value === current)) {
      list.push({ value: current, label: current });
    }
    return [{ value: '', label: '跟随默认' }, ...list];
  }, [providers, values.provider]);

  const selectedProvider = providers.find((provider) => provider.id === values.provider.trim());
  const modelOptions = useMemo(() => {
    const list = (selectedProvider?.models ?? []).map((model) => ({
      value: model.id,
      label: model.name || model.id
    }));
    const current = values.model.trim();
    if (current !== '' && !list.some((option) => option.value === current)) {
      list.push({ value: current, label: current });
    }
    return [{ value: '', label: '跟随默认模型' }, ...list];
  }, [selectedProvider, values.model]);

  const selectedModel = selectedProvider?.models.find((model) => model.id === values.model.trim());
  // 推理强度只给所选模型声明的档位（RemoteProviderModel.efforts）。
  const effortOptions = useMemo(() => {
    const list = (selectedModel?.efforts ?? []).map((effort) => ({
      value: effort,
      label: effortLabel(effort)
    }));
    const current = values.effort.trim();
    if (current !== '' && !list.some((option) => option.value === current)) {
      list.push({ value: current, label: effortLabel(current) });
    }
    return [{ value: '', label: '跟随默认' }, ...list];
  }, [selectedModel, values.effort]);

  const canSubmit = !saving && values.name.trim() !== '' && values.prompt.trim() !== '';

  const handleSubmit = useCallback(() => {
    const message = validateAutomationForm(values);
    if (message) {
      setFormError(message);
      return;
    }
    onSubmit(values.projectID.trim(), buildAutomationInput(values));
  }, [onSubmit, values]);

  const timeLabel = values.scheduleType === 'weekly' ? '时间 (HH:MM)' : '运行时间 (HH:MM)';

  return (
    <div className="space-y-3 pb-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-xs font-semibold text-[var(--foreground)]">
          {isNew ? '新建自动化' : '编辑自动化'}
        </p>
        <div className="flex items-center gap-2">
          <button type="button" className={CHIP_CLASS} onClick={onCancel} disabled={saving}>
            取消
          </button>
          <button
            type="button"
            className="min-h-[32px] rounded-xl bg-[var(--primary)] px-3 text-mc-caption font-semibold text-white transition disabled:opacity-45"
            onClick={handleSubmit}
            disabled={!canSubmit}
          >
            {saving ? '保存中…' : '保存'}
          </button>
        </div>
      </div>

      {(formError || error) && (
        <div className="inline-alert inline-alert--danger" role="alert">
          <span className="min-w-0 select-text break-words">{formError ?? error}</span>
        </div>
      )}

      {isNew && (
        <div className="space-y-1">
          <label className={LABEL_CLASS} htmlFor="automation-form-project">
            项目
          </label>
          <select
            id="automation-form-project"
            className={INPUT_CLASS}
            value={values.projectID}
            onChange={(event) => patch({ projectID: event.target.value })}
          >
            {projects.length === 0 && <option value={values.projectID}>{values.projectID || '当前项目'}</option>}
            {projects.map((project) => (
              <option key={project.id} value={project.id}>
                {project.name}
              </option>
            ))}
          </select>
        </div>
      )}

      <div className="space-y-1">
        <label className={LABEL_CLASS} htmlFor="automation-form-name">
          名称
        </label>
        <input
          id="automation-form-name"
          className={INPUT_CLASS}
          value={values.name}
          placeholder="如：每周安全审计"
          onChange={(event) => patch({ name: event.target.value })}
        />
      </div>

      <div className="space-y-1">
        <label className={LABEL_CLASS} htmlFor="automation-form-prompt">
          Prompt 提示词
        </label>
        <textarea
          id="automation-form-prompt"
          className={TEXTAREA_CLASS}
          value={values.prompt}
          placeholder="输入要执行的任务指令..."
          onChange={(event) => patch({ prompt: event.target.value })}
        />
      </div>

      <div className="space-y-2 rounded-xl bg-[color-mix(in_srgb,var(--border)_28%,transparent)] p-2.5">
        <p className="text-xs font-semibold text-[var(--foreground)]">模型与权限</p>

        <div className="space-y-1">
          <label className={LABEL_CLASS} htmlFor="automation-form-provider">
            模型供应商
          </label>
          <select
            id="automation-form-provider"
            className={INPUT_CLASS}
            value={values.provider}
            onChange={(event) =>
              // 换供应商后原来的模型与强度不再适用，一并清回「跟随默认」。
              patch({ provider: event.target.value, model: '', effort: '' })
            }
          >
            {providerOptions.map((option) => (
              <option key={option.value || 'empty'} value={option.value}>
                {option.label}
              </option>
            ))}
          </select>
        </div>

        <div className="space-y-1">
          <label className={LABEL_CLASS} htmlFor="automation-form-model">
            模型
          </label>
          <select
            id="automation-form-model"
            className={INPUT_CLASS}
            value={values.model}
            disabled={!selectedProvider}
            onChange={(event) => patch({ model: event.target.value, effort: '' })}
          >
            {modelOptions.map((option) => (
              <option key={option.value || 'empty'} value={option.value}>
                {option.label}
              </option>
            ))}
          </select>
        </div>

        <div className="space-y-1">
          <label className={LABEL_CLASS} htmlFor="automation-form-effort">
            推理强度
          </label>
          <select
            id="automation-form-effort"
            className={INPUT_CLASS}
            value={values.effort}
            disabled={!selectedModel}
            onChange={(event) => patch({ effort: event.target.value })}
          >
            {effortOptions.map((option) => (
              <option key={option.value || 'empty'} value={option.value}>
                {option.label}
              </option>
            ))}
          </select>
          <p className="text-mc-caption text-[var(--foreground-muted)]">
            可选档位跟着所选模型走；未选模型时沿用模型的默认强度。
          </p>
        </div>

        <div className="space-y-1">
          <label className={LABEL_CLASS} htmlFor="automation-form-runtime-mode">
            权限模式
          </label>
          <select
            id="automation-form-runtime-mode"
            className={INPUT_CLASS}
            value={values.runtimeMode}
            onChange={(event) => patch({ runtimeMode: event.target.value })}
          >
            {AUTOMATION_RUNTIME_MODES.map((mode) => (
              <option key={mode.value || 'empty'} value={mode.value}>
                {mode.label}
              </option>
            ))}
          </select>
          <p className="text-mc-caption text-[var(--foreground-muted)]">
            新会话按这一档执行权限限制；跟随默认表示沿用 Mac 上的默认档位。
          </p>
        </div>
      </div>

      <div className="space-y-1">
        <label className={LABEL_CLASS} htmlFor="automation-form-schedule-type">
          调度类型
        </label>
        <select
          id="automation-form-schedule-type"
          className={INPUT_CLASS}
          value={values.scheduleType}
          onChange={(event) => patch({ scheduleType: event.target.value })}
        >
          {SCHEDULE_TYPE_OPTIONS.map((option) => (
            <option key={option.key} value={option.key}>
              {option.label}
            </option>
          ))}
        </select>
      </div>

      <div className="space-y-2 rounded-xl bg-[color-mix(in_srgb,var(--border)_28%,transparent)] p-2.5">
        {values.scheduleType === 'interval' && (
          <div className="space-y-1">
            <label className={LABEL_CLASS} htmlFor="automation-form-interval">
              运行间隔
            </label>
            <select
              id="automation-form-interval"
              className={INPUT_CLASS}
              value={values.intervalMinutes}
              onChange={(event) => patch({ intervalMinutes: event.target.value })}
            >
              {intervalOptions.map((minutes) => (
                <option key={minutes} value={minutes}>
                  {`${minutes} 分钟`}
                </option>
              ))}
            </select>
          </div>
        )}

        {(values.scheduleType === 'daily' ||
          values.scheduleType === 'weekdays' ||
          values.scheduleType === 'weekly') && (
          <div className="space-y-1">
            <label className={LABEL_CLASS} htmlFor="automation-form-time">
              {timeLabel}
            </label>
            <input
              id="automation-form-time"
              className={INPUT_CLASS}
              inputMode="numeric"
              value={values.timeOfDay}
              placeholder="如 09:30"
              onChange={(event) => patch({ timeOfDay: event.target.value })}
            />
            <p className="text-mc-caption text-[var(--foreground-muted)]">
              必须两位小时，如 09:00；9:00 会被宿主拒绝。
            </p>
          </div>
        )}

        {values.scheduleType === 'weekly' && (
          <div className="space-y-1">
            <span className={LABEL_CLASS}>星期几</span>
            <div className="flex flex-wrap gap-1.5">
              {DAY_OF_WEEK_OPTIONS.map((option) => {
                const selected = values.dayOfWeek === option.day;
                return (
                  <button
                    key={option.day}
                    type="button"
                    aria-pressed={selected}
                    onClick={() => patch({ dayOfWeek: option.day })}
                    className={
                      selected
                        ? 'min-h-[32px] rounded-xl bg-[var(--primary)] px-2.5 text-mc-caption font-semibold text-white'
                        : CHIP_CLASS
                    }
                  >
                    {option.label}
                  </button>
                );
              })}
            </div>
          </div>
        )}

        {values.scheduleType === 'cron' && (
          <div className="space-y-1">
            <label className={LABEL_CLASS} htmlFor="automation-form-cron">
              Cron 表达式（五个字段）
            </label>
            <input
              id="automation-form-cron"
              className={INPUT_CLASS}
              value={values.cronExpression}
              placeholder="如 */15 * * * *"
              onChange={(event) => patch({ cronExpression: event.target.value })}
            />
          </div>
        )}

        {values.scheduleType === 'once' && (
          <div className="space-y-1">
            <label className={LABEL_CLASS} htmlFor="automation-form-once">
              执行时间 (YYYY-MM-DD HH:MM)
            </label>
            <input
              id="automation-form-once"
              className={INPUT_CLASS}
              value={values.onceDate}
              placeholder="如 2026-10-20 14:00"
              onChange={(event) => patch({ onceDate: event.target.value })}
            />
          </div>
        )}
      </div>

      <div className="space-y-1">
        <label className={LABEL_CLASS} htmlFor="automation-form-stop-when">
          停止条件 (StopWhen)
        </label>
        <input
          id="automation-form-stop-when"
          className={INPUT_CLASS}
          value={values.stopWhen}
          placeholder="如：所有单元测试均通过，或任务目标达成"
          onChange={(event) => patch({ stopWhen: event.target.value })}
        />
      </div>

      <div className="flex flex-wrap items-center gap-x-4 gap-y-3">
        <label className="flex items-center gap-2 text-xs text-[var(--foreground)]">
          <input
            type="checkbox"
            className="h-4 w-4"
            checked={values.enabled}
            onChange={(event) => patch({ enabled: event.target.checked })}
          />
          启用该自动化
        </label>

        <div className="flex items-center gap-2">
          <label className={`${LABEL_CLASS} whitespace-nowrap`} htmlFor="automation-form-max-failures">
            最多连续失败
          </label>
          <input
            id="automation-form-max-failures"
            className="pairing-input w-16 min-w-0 text-center text-sm"
            type="number"
            min={MAX_CONSECUTIVE_FAILURES_MIN}
            max={MAX_CONSECUTIVE_FAILURES_MAX}
            step={1}
            value={values.maxConsecutiveFailures}
            onChange={(event) => patch({ maxConsecutiveFailures: event.target.value })}
          />
        </div>

        <div className="flex items-center gap-2">
          <label className={`${LABEL_CLASS} whitespace-nowrap`} htmlFor="automation-form-max-iterations">
            最大运行次数（0 = 不限制）
          </label>
          <input
            id="automation-form-max-iterations"
            className="pairing-input w-20 min-w-0 text-center text-sm"
            type="number"
            min={0}
            step={1}
            value={values.maxIterations}
            onChange={(event) => patch({ maxIterations: event.target.value })}
          />
        </div>
      </div>
    </div>
  );
};
