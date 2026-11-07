import React from 'react';
import type { SuggestionItem, SuggestionTrigger } from '../utils/slashCommands';
import { CodeBracketIcon, DocumentTextIcon } from './Icons';

export interface CommandSuggestionsProps {
  trigger: SuggestionTrigger;
  suggestions: SuggestionItem[];
  selectedIndex: number;
  onSelect: (item: SuggestionItem) => void;
  onClose: () => void;
  isLoading?: boolean;
  /** 底部补充提示（例如宿主仍在后台拉取命令表）。 */
  footnote?: string | null;
}

const HEADINGS: Record<SuggestionTrigger, { title: string; loading: string; empty: string; label: string }> = {
  '@': { title: '工作区文件建议 (@)', loading: '搜索中…', empty: '未找到匹配的工作区文件', label: '工作区文件建议' },
  '/': { title: '命令 (/)', loading: '加载中…', empty: '没有匹配的命令', label: '斜杠命令建议' },
  '$': { title: '技能 ($)', loading: '加载中…', empty: '没有匹配的技能', label: '技能建议' },
  '#': { title: '委派给其他模型 (#)', loading: '加载中…', empty: '没有匹配的模型', label: '模型委派建议' }
};

type IconProps = { className?: string };

const FolderIcon: React.FC<IconProps> = ({ className = 'w-3.5 h-3.5' }) => (
  <svg className={className} fill="none" viewBox="0 0 24 24" strokeWidth="1.8" stroke="currentColor">
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="M2.25 12.75V12A2.25 2.25 0 0 1 4.5 9.75h3.379c.456 0 .895-.138 1.266-.395l1.01-1.01A2.25 2.25 0 0 1 11.75 7.5H19.5A2.25 2.25 0 0 1 21.75 9.75v3m-19.5 0A2.25 2.25 0 0 0 4.5 15h15a2.25 2.25 0 0 0 2.25-2.25m-19.5 0v5.25A2.25 2.25 0 0 0 4.5 20.25h15a2.25 2.25 0 0 0 2.25-2.25V12.75"
    />
  </svg>
);

const LinkIcon: React.FC<IconProps> = ({ className = 'w-3.5 h-3.5' }) => (
  <svg className={className} fill="none" viewBox="0 0 24 24" strokeWidth="1.8" stroke="currentColor">
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="M13.19 8.688a4.5 4.5 0 0 1 1.242 7.244l-4.5 4.5a4.5 4.5 0 0 1-6.364-6.364l1.757-1.757m13.35-3.636 1.757-1.757a4.5 4.5 0 0 0-6.364-6.364l-4.5 4.5a4.5 4.5 0 0 0 1.242 7.244"
    />
  </svg>
);

// 内置命令（plan、compact）：闪电
const BoltIcon: React.FC<IconProps> = ({ className = 'w-3.5 h-3.5' }) => (
  <svg className={className} fill="none" viewBox="0 0 24 24" strokeWidth="1.8" stroke="currentColor">
    <path strokeLinecap="round" strokeLinejoin="round" d="m3.75 13.5 10.5-11.25L12 10.5h8.25L9.75 21.75 12 13.5H3.75Z" />
  </svg>
);

// skill：星光
const SparklesIcon: React.FC<IconProps> = ({ className = 'w-3.5 h-3.5' }) => (
  <svg className={className} fill="none" viewBox="0 0 24 24" strokeWidth="1.8" stroke="currentColor">
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="M9.813 15.904 9 18.75l-.813-2.846a4.5 4.5 0 0 0-3.09-3.09L2.25 12l2.846-.813a4.5 4.5 0 0 0 3.09-3.09L9 5.25l.813 2.846a4.5 4.5 0 0 0 3.09 3.09L15.75 12l-2.846.813a4.5 4.5 0 0 0-3.09 3.09ZM18.259 8.715 18 9.75l-.259-1.035a3.375 3.375 0 0 0-2.455-2.456L14.25 6l1.036-.259a3.375 3.375 0 0 0 2.455-2.456L18 2.25l.259 1.035a3.375 3.375 0 0 0 2.456 2.456L21.75 6l-1.035.259a3.375 3.375 0 0 0-2.456 2.456Z"
    />
  </svg>
);

// 模型：芯片
const ModelIcon: React.FC<IconProps> = ({ className = 'w-3.5 h-3.5' }) => (
  <svg className={className} fill="none" viewBox="0 0 24 24" strokeWidth="1.8" stroke="currentColor">
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="M8.25 3v1.5M4.5 8.25H3m18 0h-1.5M4.5 12H3m18 0h-1.5m-15 3.75H3m18 0h-1.5M8.25 19.5V21M12 3v1.5m0 15V21m3.75-18v1.5m0 15V21m-9-1.5h10.5a2.25 2.25 0 0 0 2.25-2.25V6.75a2.25 2.25 0 0 0-2.25-2.25H6.75A2.25 2.25 0 0 0 4.5 6.75v10.5a2.25 2.25 0 0 0 2.25 2.25Zm.75-12h9v9h-9v-9Z"
    />
  </svg>
);

function renderIcon(icon: SuggestionItem['icon'], isSelected: boolean): React.ReactNode {
  const base = 'w-3.5 h-3.5 shrink-0';
  switch (icon) {
    case 'folder':
      return <FolderIcon className={`${base} ${isSelected ? 'text-white' : 'text-amber-500'}`} />;
    case 'link':
      return <LinkIcon className={`${base} ${isSelected ? 'text-white' : 'text-sky-500'}`} />;
    case 'builtin':
      return <BoltIcon className={`${base} ${isSelected ? 'text-white' : 'text-amber-500'}`} />;
    case 'skill':
      return <SparklesIcon className={`${base} ${isSelected ? 'text-white' : 'text-violet-500'}`} />;
    case 'command':
      return <CodeBracketIcon className={`${base} ${isSelected ? 'text-white' : 'text-[var(--foreground-muted)]'}`} />;
    case 'model':
      return <ModelIcon className={`${base} ${isSelected ? 'text-white' : 'text-sky-500'}`} />;
    default:
      return <DocumentTextIcon className={`${base} ${isSelected ? 'text-white' : 'text-[var(--foreground-muted)]'}`} />;
  }
}

/** `@` 文件、`/` 命令、`$` 技能、`#` 模型共用的补全弹层：定位与点选交互与原文件补全一致。 */
export const CommandSuggestions: React.FC<CommandSuggestionsProps> = ({
  trigger,
  suggestions,
  selectedIndex,
  onSelect,
  onClose,
  isLoading = false,
  footnote
}) => {
  const heading = HEADINGS[trigger];
  return (
    <div
      className="absolute bottom-full left-0 right-0 mb-2 max-h-[min(14rem,35dvh)] overflow-y-auto rounded-2xl bg-[var(--card)] border border-[var(--border)] shadow-xl z-30 p-1.5 space-y-0.5"
      role="listbox"
      aria-label={heading.label}
    >
      <div className="flex items-center justify-between px-2.5 py-1 text-mc-caption font-medium text-[var(--foreground-muted)] border-b border-[var(--border)]">
        <span>{heading.title}</span>
        <div className="flex items-center gap-2">
          {isLoading && <span>{heading.loading}</span>}
          <button
            type="button"
            onClick={onClose}
            className="text-[var(--foreground-muted)] hover:text-[var(--foreground)] text-xs"
            aria-label="关闭建议"
          >
            Esc
          </button>
        </div>
      </div>

      {suggestions.length === 0 && !isLoading ? (
        <div className="px-3 py-2 text-xs text-[var(--foreground-muted)] text-center">{heading.empty}</div>
      ) : (
        suggestions.map((item, index) => {
          const isSelected = index === selectedIndex;
          return (
            <button
              key={item.key}
              type="button"
              role="option"
              aria-selected={isSelected}
              onMouseDown={(e) => {
                // 阻止输入框失焦
                e.preventDefault();
                onSelect(item);
              }}
              className={`w-full flex items-center justify-between gap-2 px-2.5 py-1.5 rounded-xl text-left text-xs transition ${
                isSelected
                  ? 'bg-[var(--primary)] text-white'
                  : 'text-[var(--foreground)] hover:bg-[var(--card-alt,var(--card))]'
              }`}
            >
              <div className="flex items-center gap-1.5 min-w-0 flex-1">
                {renderIcon(item.icon, isSelected)}
                <span className={`font-medium truncate ${trigger === '#' ? '' : 'font-mono'}`}>{item.title}</span>
                {item.detail && (
                  <span className={`text-mc-caption truncate ${isSelected ? 'text-white/80' : 'text-[var(--foreground-muted)]'}`}>
                    {item.detail}
                  </span>
                )}
              </div>
              {item.meta && (
                <span className={`text-mc-caption shrink-0 font-mono ${isSelected ? 'text-white/80' : 'text-[var(--foreground-muted)]'}`}>
                  {item.meta}
                </span>
              )}
            </button>
          );
        })
      )}
      {footnote && (
        <div className="px-2.5 py-1 text-mc-caption text-[var(--foreground-muted)]">{footnote}</div>
      )}
    </div>
  );
};
