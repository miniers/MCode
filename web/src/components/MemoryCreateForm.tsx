// 新建记忆分页：手动记录一条记忆，来源记为「我」，提交后先落在 Mac 的待审收件箱。
// 字段照桌面版「新建记忆」对话框（internal/ui/memory_panel_views.go 的标题 / 内容 / 类型），
// 并按 memory.create 的入参补上重要度（0~1）与写入空间（当前项目 / 全局）。

import React, { useState } from 'react';
import {
  useMemoryActions,
  type RemoteMemoryScope,
  type RemoteMemoryUnitType
} from '../context/remoteMemoryApi';
import { PlusIcon } from './Icons';

/** 记忆类型选项：顺序与桌面版 memoryUnitTypeOptions 一致。 */
export const MEMORY_UNIT_TYPE_OPTIONS: RemoteMemoryUnitType[] = [
  'fact',
  'preference',
  'decision',
  'plan',
  'procedure',
  'learning',
  'context',
  'event'
];

/** 类型的中文标签；桌面端还认 insight / digest，这里一并保留。 */
const UNIT_TYPE_LABELS: Record<string, string> = {
  fact: '事实',
  insight: '洞见',
  preference: '偏好',
  decision: '决策',
  plan: '计划',
  procedure: '流程',
  learning: '经验',
  context: '背景',
  event: '事件',
  digest: '汇总'
};

/** 类型标签：搜索页与提议编辑区共用同一份映射。 */
export function memoryUnitTypeLabel(unitType?: string): string {
  const key = unitType?.trim() ?? '';
  if (!key) return '未分类';
  return UNIT_TYPE_LABELS[key] ?? key;
}

/** 写入空间选项与文案，与桌面端新建对话框一致。 */
const SCOPE_OPTIONS: { value: RemoteMemoryScope; label: string }[] = [
  { value: 'project', label: '当前项目' },
  { value: 'global', label: '全局' }
];

/** 重要度默认值，与桌面端新建对话框的占位 0.5 一致。 */
const DEFAULT_MEMORY_IMPORTANCE = 0.5;

/** 正文输入框的行内样式：settings-input 的上下内边距为 0，多行框要单独补。 */
const MEMORY_TEXTAREA_STYLE: React.CSSProperties = {
  minHeight: 132,
  paddingTop: 8,
  paddingBottom: 8,
  lineHeight: 1.6
};

export interface MemoryCreateFormProps {
  /** 当前选中项目；为空（全部项目）时宿主不知道记到哪个项目，需要先选项目。 */
  projectID: string | null;
  /** 项目名，仅用于说明当前记到哪个项目。 */
  projectName?: string;
  /** 创建成功后把提示文案交给调用方，切回提议页时显示。 */
  onCreated: (notice: string) => void;
}

export const MemoryCreateForm: React.FC<MemoryCreateFormProps> = ({
  projectID,
  projectName,
  onCreated
}) => {
  const { createMemory, createSupported } = useMemoryActions(projectID);
  const [title, setTitle] = useState('');
  const [content, setContent] = useState('');
  const [unitType, setUnitType] = useState<RemoteMemoryUnitType>('fact');
  const [importance, setImportance] = useState(DEFAULT_MEMORY_IMPORTANCE);
  const [scope, setScope] = useState<RemoteMemoryScope>('project');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  if (!createSupported) return null;

  const handleSubmit = async () => {
    if (busy) return;
    if (!projectID) {
      setError('请先在顶部选择项目，再新建记忆');
      return;
    }
    if (!title.trim()) {
      setError('记忆标题不能为空');
      return;
    }
    setBusy(true);
    setError('');
    try {
      await createMemory({ title: title.trim(), content, type: unitType, importance, scope });
      setTitle('');
      setContent('');
      onCreated('已加入待审提议');
    } catch (err) {
      setError(err instanceof Error ? err.message : '新建记忆失败，请稍后重试');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-3 pb-2">
      <p className="text-xs text-[var(--foreground-muted)]">
        手动记录一条记忆，来源记为「我」，加入待审列表后再确认写入。
      </p>

      {error && (
        <p className="settings-error" role="alert">
          {error}
        </p>
      )}

      <div>
        <span className="settings-label">记忆标题</span>
        <input
          className="settings-input"
          type="text"
          value={title}
          maxLength={200}
          placeholder="例如：发布前先跑 web 的类型检查"
          aria-label="记忆标题"
          onChange={(event) => setTitle(event.target.value)}
        />
      </div>

      <div>
        <span className="settings-label">正文</span>
        <textarea
          className="settings-input"
          style={MEMORY_TEXTAREA_STYLE}
          value={content}
          rows={6}
          placeholder="写下结论、原因与适用场景…"
          aria-label="记忆正文"
          onChange={(event) => setContent(event.target.value)}
        />
      </div>

      <div>
        <span className="settings-label">类型</span>
        <div className="filter-row" role="group" aria-label="记忆类型">
          {MEMORY_UNIT_TYPE_OPTIONS.map((value) => (
            <button
              key={value}
              type="button"
              aria-pressed={unitType === value}
              onClick={() => setUnitType(value)}
              className={`filter-chip ${unitType === value ? 'filter-chip--active' : ''}`}
            >
              {memoryUnitTypeLabel(value)}
            </button>
          ))}
        </div>
      </div>

      <div>
        <div className="flex items-center justify-between gap-2">
          <span className="settings-label">重要度</span>
          <span className="text-xs text-[var(--foreground-muted)]">{importance.toFixed(1)}</span>
        </div>
        <input
          type="range"
          min={0}
          max={1}
          step={0.1}
          value={importance}
          aria-label="重要度"
          className="w-full"
          style={{ accentColor: 'var(--primary)' }}
          onChange={(event) => setImportance(Number(event.target.value))}
        />
      </div>

      <div>
        <span className="settings-label">写入空间</span>
        <div className="filter-row" role="group" aria-label="写入空间">
          {SCOPE_OPTIONS.map((option) => (
            <button
              key={option.value}
              type="button"
              aria-pressed={scope === option.value}
              onClick={() => setScope(option.value)}
              className={`filter-chip ${scope === option.value ? 'filter-chip--active' : ''}`}
            >
              {option.label}
            </button>
          ))}
        </div>
        <p className="mt-2 text-mc-caption text-[var(--foreground-muted)]">
          {projectName ? `当前项目：${projectName}` : '未选择项目（全部项目视图）'}
        </p>
      </div>

      <div className="flex items-center justify-end gap-2 pt-1">
        <button
          type="button"
          onClick={() => void handleSubmit()}
          disabled={busy || !projectID}
          className="primary-button settings-inline-button shrink-0"
        >
          <PlusIcon className="h-3.5 w-3.5" />
          {busy ? '提交中…' : '加入待审'}
        </button>
      </div>
    </div>
  );
};
