import React, { useCallback, useRef, useState } from 'react';
import type { PickerRow, PickerSections } from '../utils/modelIndex';
import { isImeComposing } from '../utils/ime';
import { CheckCircleIcon, ChevronRightIcon, SearchIcon } from './Icons';

export interface EffortTrackProps {
  efforts: string[];
  value: string;
  onChange: (effort: string) => void;
  /** 头部右侧展示值，缺省为 `value || '默认（CLI）'` */
  displayValue?: string;
  /** 传入时在最前面加一个“默认”档位，空字符串表示选中它 */
  defaultLabel?: string;
}

/** 思考档位轨道卡片，无档位时给出占位文案。 */
export const EffortTrack: React.FC<EffortTrackProps> = ({ efforts, value, onChange, displayValue, defaultLabel }) => {
  // 档位节点：defaultLabel 存在时先放一个空字符串档位（交给 CLI 默认值）。
  const stops: Array<{ key: string; label: string; selected: boolean; select: () => void }> = [];
  if (defaultLabel) {
    stops.push({ key: '__default__', label: defaultLabel, selected: value === '', select: () => onChange('') });
  }
  for (const eff of efforts) {
    stops.push({ key: eff, label: eff, selected: value === eff, select: () => onChange(eff) });
  }

  return (
    <div className="rounded-2xl border border-[var(--border)] bg-[var(--card)] p-4">
      <div className="flex items-center justify-between mb-4">
        <span className="text-xs font-semibold text-[var(--foreground)]">思考档位</span>
        <span className="text-xs font-bold text-[var(--primary)]">{displayValue ?? (value || '默认（CLI）')}</span>
      </div>
      {stops.length > 0 ? (
        <div className="relative flex items-center justify-between w-full px-4 py-3">
          {/* Track line behind dots */}
          <div className="absolute left-6 right-6 top-[21px] h-0.5 bg-[var(--border)] -z-0" />
          {stops.map((stop) => {
            const isSelected = stop.selected;
            return (
              <button
                key={stop.key}
                type="button"
                onClick={stop.select}
                className="relative z-10 flex flex-col items-center group cursor-pointer focus:outline-none"
                aria-label={`思考档位: ${stop.label}`}
              >
                <div
                  className={`w-5 h-5 rounded-full flex items-center justify-center transition-all ${
                    isSelected
                      ? 'bg-[var(--primary)] shadow-sm ring-4 ring-[var(--primary)]/20 scale-110'
                      : 'bg-[var(--card)] border-2 border-[var(--border)] group-hover:border-[var(--primary)]'
                  }`}
                >
                  {isSelected && <span className="w-1.5 h-1.5 rounded-full bg-white" />}
                </div>
                <span
                  className={`mt-2 text-xs transition-colors ${
                    isSelected
                      ? 'font-bold text-[var(--primary)]'
                      : 'font-medium text-[var(--foreground-muted)] group-hover:text-[var(--foreground)]'
                  }`}
                >
                  {stop.label}
                </span>
              </button>
            );
          })}
        </div>
      ) : (
        <div className="py-4 text-center text-xs text-[var(--foreground-muted)]">该模型无思考档位选项</div>
      )}
    </div>
  );
};

/** 上下文窗口徽标：百万以上按百万取整，其余按千取整。 */
function formatContextLimit(limit: number | undefined): string | null {
  if (!limit || limit <= 0) return null;
  if (limit >= 1000000) return `${Math.round(limit / 1000000)}M`;
  return `${Math.round(limit / 1000)}K`;
}

export interface ModelPickerPanelProps {
  /** 由 buildPickerSections 算好的分区 */
  sections: PickerSections;
  /** 受控搜索词 */
  query: string;
  onQueryChange: (query: string) => void;
  /** provider.id + '/' + model.id；命中的行显示勾选 */
  selectedKey: string | null;
  onSelect: (providerID: string, modelID: string) => void;
  /** “当前”之后、“收藏”之前的插槽，搜索时不渲染 */
  header?: React.ReactNode;
  /** 搜索框上方的提示 */
  notice?: React.ReactNode;
  /** 打开面板时自动聚焦搜索框 */
  autoFocusSearch?: boolean;
  allowedProviderID?: string;
}

interface PanelRowProps {
  row: PickerRow;
  selected: boolean;
  allowedProviderID?: string;
  onSelect: (providerID: string, modelID: string) => void;
  onKeyDown: (event: React.KeyboardEvent<HTMLButtonElement>) => void;
}

/** 选择器单行：整行可点，右侧显示上下文窗口与选中标记。 */
const PanelRow: React.FC<PanelRowProps> = ({ row, selected, allowedProviderID, onSelect, onKeyDown }) => {
  const context = formatContextLimit(row.model.contextLimit);
  const meta = [row.subtitle, row.reason].filter((part): part is string => Boolean(part)).join(' · ');

  return (
    <button
      type="button"
      data-picker-row="true"
      data-picker-provider={row.provider.id}
      data-picker-model={row.model.id}
      disabled={row.disabled || Boolean(allowedProviderID && row.provider.id !== allowedProviderID)}
      onClick={() => onSelect(row.provider.id, row.model.id)}
      onKeyDown={onKeyDown}
      className="flex w-full items-center justify-between gap-2 rounded-xl border border-[var(--border)] bg-[var(--card)] px-3 py-2 text-left transition-colors enabled:hover:bg-[var(--user-bubble)] focus:outline-none focus-visible:ring-2 focus-visible:ring-[var(--primary)]/40 disabled:opacity-40"
    >
      <span className="min-w-0 flex-1">
        <span className="block truncate text-sm font-semibold text-[var(--foreground)]">{row.model.name}</span>
        {meta && <span className="mt-0.5 block truncate text-xs text-[var(--foreground-muted)]">{meta}</span>}
      </span>
      <span className="flex shrink-0 items-center gap-1.5">
        {context && (
          <span className="rounded-md bg-[var(--user-bubble)] px-1.5 py-0.5 text-[10px] font-semibold text-[var(--foreground-muted)]">
            {context}
          </span>
        )}
        {selected && (
          <span role="img" aria-label="已选">
            <CheckCircleIcon className="w-4 h-4 text-[var(--primary)]" />
          </span>
        )}
      </span>
    </button>
  );
};

const PanelHeading: React.FC<{ children: React.ReactNode }> = ({ children }) => (
  <div className="px-1 text-mc-caption font-semibold tracking-wide text-[var(--foreground-muted)]">{children}</div>
);

export interface ModelSummaryRowProps {
  /** 左侧小号标题，例如「主代理」 */
  label: string;
  /** 当前值，例如模型短名 */
  value: string;
  /** 悬停展示的全名：摘要行只放短名，品牌与附加说明走 title */
  title?: string;
  /** 右侧小号补充，例如档位或 provider 名 */
  detail?: string;
  onClick: () => void;
}

/** 摘要页的入口行：整行可点，右侧箭头提示会进入下一级。 */
export const ModelSummaryRow: React.FC<ModelSummaryRowProps> = ({ label, value, title, detail, onClick }) => (
  <button
    type="button"
    onClick={onClick}
    title={title}
    className="flex w-full min-h-[44px] items-center justify-between gap-2 rounded-xl border border-[var(--border)] bg-[var(--card)] px-3 py-2 text-left transition-colors hover:bg-[var(--user-bubble)] focus:outline-none focus-visible:ring-2 focus-visible:ring-[var(--primary)]/40"
  >
    <span className="min-w-0 flex-1">
      <span className="block text-mc-caption font-semibold text-[var(--foreground-muted)]">{label}</span>
      <span className="block truncate text-sm font-semibold text-[var(--foreground)]">{value}</span>
    </span>
    <span className="flex shrink-0 items-center gap-1.5">
      {detail && <span className="text-xs text-[var(--foreground-muted)]">{detail}</span>}
      <ChevronRightIcon className="w-4 h-4 text-[var(--foreground-muted)]" />
    </span>
  </button>
);

/** 单屏模型面板：搜索、当前、收藏与按 Agent 分组的全部模型。 */
export const ModelPickerPanel: React.FC<ModelPickerPanelProps> = ({
  sections,
  query,
  onQueryChange,
  selectedKey,
  onSelect,
  header,
  notice,
  allowedProviderID,
  autoFocusSearch = false
}) => {
  const panelRef = useRef<HTMLDivElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  // 全部模型默认收起（仅在存在收藏时），展开后保持；搜索时强制展开。
  const [allExpanded, setAllExpanded] = useState(false);
  // 分组与 secondary 的展开状态按 provider.id 记录。
  const [collapsedGroups, setCollapsedGroups] = useState<Record<string, boolean>>({});
  const [expandedSecondary, setExpandedSecondary] = useState<Record<string, boolean>>({});

  const enabledRows = useCallback((): HTMLButtonElement[] => {
    const root = panelRef.current;
    if (!root) return [];
    return Array.from(root.querySelectorAll<HTMLButtonElement>('button[data-picker-row]')).filter(
      (button) => !button.disabled
    );
  }, []);

  const handleRowKeyDown = useCallback(
    (event: React.KeyboardEvent<HTMLButtonElement>) => {
      if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return;
      event.preventDefault();
      const rows = enabledRows();
      const index = rows.indexOf(event.currentTarget);
      // 第一行再往上回到搜索框。
      if (event.key === 'ArrowUp' && index <= 0) {
        searchRef.current?.focus();
        return;
      }
      const next = rows[index + (event.key === 'ArrowDown' ? 1 : -1)];
      if (next) next.focus();
    },
    [enabledRows]
  );

  const handleSearchKeyDown = (event: React.KeyboardEvent<HTMLInputElement>) => {
    // 中文输入法组字时方向键/回车交给输入法，不抢键盘。
    if (isImeComposing(event)) return;
    if (event.key === 'ArrowDown') {
      event.preventDefault();
      enabledRows()[0]?.focus();
      return;
    }
    if (event.key === 'Enter') {
      // 回车选中当前可见的第一行（禁用行已排除）。
      const first = enabledRows()[0];
      const providerID = first?.dataset.pickerProvider;
      const modelID = first?.dataset.pickerModel;
      if (!providerID || !modelID) return;
      event.preventDefault();
      onSelect(providerID, modelID);
    }
  };

  const toggleGroup = (providerID: string) => {
    setCollapsedGroups((current) => ({ ...current, [providerID]: !current[providerID] }));
  };

  const toggleSecondary = (providerID: string) => {
    setExpandedSecondary((current) => ({ ...current, [providerID]: !current[providerID] }));
  };

  const showAllToggle = sections.favorites.length > 0 && !sections.searching;
  const allVisible = !showAllToggle || allExpanded;

  return (
    <div ref={panelRef} className="flex flex-col gap-3">
      {notice}

      {/* 搜索框：向下箭头进入列表，回车选中第一行 */}
      <label className="flex min-h-[40px] items-center gap-2 rounded-xl border border-[var(--border)] bg-[var(--card)] px-3">
        <SearchIcon className="w-4 h-4 shrink-0 text-[var(--foreground-muted)]" />
        <input
          ref={searchRef}
          type="search"
          value={query}
          onChange={(event) => onQueryChange(event.target.value)}
          onKeyDown={handleSearchKeyDown}
          placeholder="搜索模型或 Agent"
          aria-label="搜索模型或 Agent"
          autoFocus={autoFocusSearch}
          autoComplete="off"
          enterKeyHint="search"
          className="min-w-0 flex-1 bg-transparent text-sm text-[var(--foreground)] outline-none"
        />
      </label>

      {!sections.searching && sections.current && (
        <div className="space-y-1">
          <PanelHeading>当前</PanelHeading>
          <PanelRow
            row={sections.current}
            allowedProviderID={allowedProviderID}
            selected={sections.current.key === selectedKey}
            onSelect={onSelect}
            onKeyDown={handleRowKeyDown}
          />
        </div>
      )}

      {!sections.searching && header}

      {sections.favorites.length > 0 && (
        <div className="space-y-1">
          <PanelHeading>收藏</PanelHeading>
          {sections.favorites.map((row) => (
            <PanelRow
              key={row.key}
              row={row}
              allowedProviderID={allowedProviderID}
              selected={row.key === selectedKey}
              onSelect={onSelect}
              onKeyDown={handleRowKeyDown}
            />
          ))}
        </div>
      )}

      <div className="space-y-1">
        {showAllToggle ? (
          <button
            type="button"
            onClick={() => setAllExpanded((value) => !value)}
            aria-expanded={allExpanded}
            className="flex w-full items-center justify-between rounded-lg px-1 py-1.5 text-left"
          >
            <span className="text-mc-caption font-semibold tracking-wide text-[var(--foreground-muted)]">全部模型</span>
            <span className="flex shrink-0 items-center gap-1 text-xs text-[var(--foreground-muted)]">
              <span>{sections.agents.length}</span>
              <ChevronRightIcon
                className={`w-3.5 h-3.5 transition-transform ${allExpanded ? 'rotate-90' : ''}`}
              />
            </span>
          </button>
        ) : (
          <PanelHeading>全部模型</PanelHeading>
        )}

        {allVisible && (
          <div className="space-y-3">
            {sections.agents.map((section) => {
              const collapsible = section.primary.length > 0 || section.secondary.length > 0;
              // 包含当前选中的组不能折叠（保证选中行可见）；搜索时忽略折叠状态。
              const hasSelection =
                selectedKey !== null &&
                [...section.primary, ...section.secondary].some((row) => row.key === selectedKey);
              const canToggle = collapsible && !sections.searching && !hasSelection;
              const collapsed = canToggle && collapsedGroups[section.provider.id] === true;
              const showSecondary = expandedSecondary[section.provider.id] === true;

              return (
                <div key={section.provider.id} className="space-y-1">
                  {canToggle ? (
                    <button
                      type="button"
                      onClick={() => toggleGroup(section.provider.id)}
                      aria-expanded={!collapsed}
                      className="flex w-full items-center justify-between rounded-lg px-1 py-1 text-left"
                    >
                      <span className="text-xs font-semibold text-[var(--foreground-muted)]">{section.provider.name}</span>
                      <ChevronRightIcon
                        className={`w-3.5 h-3.5 text-[var(--foreground-muted)] transition-transform ${
                          collapsed ? '' : 'rotate-90'
                        }`}
                      />
                    </button>
                  ) : (
                    <div className="px-1 py-1 text-xs font-semibold text-[var(--foreground-muted)]">
                      {section.provider.name}
                    </div>
                  )}

                  {!collapsed &&
                    section.primary.map((row) => (
                      <PanelRow
                        key={row.key}
                        row={row}
                        allowedProviderID={allowedProviderID}
                        selected={row.key === selectedKey}
                        onSelect={onSelect}
                        onKeyDown={handleRowKeyDown}
                      />
                    ))}

                  {!collapsed &&
                    showSecondary &&
                    section.secondary.map((row) => (
                      <PanelRow
                        key={row.key}
                        row={row}
                        allowedProviderID={allowedProviderID}
                        selected={row.key === selectedKey}
                        onSelect={onSelect}
                        onKeyDown={handleRowKeyDown}
                      />
                    ))}

                  {/* 只有原因的空分组：显示一行原因。 */}
                  {!collapsed && !collapsible && section.reason && (
                    <div className="px-1 text-xs text-[var(--foreground-muted)]">{section.reason}</div>
                  )}

                  {!collapsed && collapsible && section.secondary.length > 0 && (
                    <button
                      type="button"
                      onClick={() => toggleSecondary(section.provider.id)}
                      aria-expanded={showSecondary}
                      className="w-full rounded-lg px-1 py-1 text-left text-xs font-semibold text-[var(--primary)] hover:underline"
                    >
                      {showSecondary ? '收起' : `更多（${section.secondary.length}）`}
                    </button>
                  )}
                </div>
              );
            })}

            {sections.searching && sections.agents.length === 0 && (
              <div className="px-1 py-3 text-xs text-[var(--foreground-muted)]">没有匹配的模型</div>
            )}
          </div>
        )}
      </div>
    </div>
  );
};
