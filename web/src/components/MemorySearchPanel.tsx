// 记忆搜索分页：输入关键词回车搜索 Nowledge Mem 里的记忆，结果只读（不改状态、不写入）。
// 对应桌面端记忆面板的搜索页（internal/ui/memory_panel_views.go 的 renderMemorySearchTab）。

import React, { useRef, useState } from 'react';
import { useMemoryActions, type RemoteMemorySearchEntry } from '../context/remoteMemoryApi';
import { memoryUnitTypeLabel } from './MemoryCreateForm';
import { SearchIcon } from './Icons';

/** 每次搜索的条数上限，与宿主 memory.search 的默认值一致（宿主上限 50）。 */
const RESULT_LIMIT = 20;

export interface MemorySearchPanelProps {
  /** 当前选中项目；为空时只能跨全部项目搜索。 */
  projectID: string | null;
  /** 项目名，仅用于说明限定的是哪个项目。 */
  projectName?: string;
}

function formatTime(value: string): string {
  if (!value) return '时间未知';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit'
  });
}

export const MemorySearchPanel: React.FC<MemorySearchPanelProps> = ({
  projectID,
  projectName
}) => {
  // 默认只在当前项目里搜；勾掉后跨全部项目（宿主 projectID 为空即全局搜索）。
  const [onlyProject, setOnlyProject] = useState(true);
  const scopeProjectID = onlyProject ? projectID : null;
  const { searchMemories, searchSupported } = useMemoryActions(scopeProjectID);
  const [query, setQuery] = useState('');
  const [results, setResults] = useState<RemoteMemorySearchEntry[] | null>(null);
  const [searched, setSearched] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  // 请求代际：连续回车时丢掉旧请求的结果，避免列表回踩。
  const seqRef = useRef(0);

  if (!searchSupported) return null;

  const toggleExpanded = (id: string) => {
    setExpanded((prev) => ({ ...prev, [id]: !prev[id] }));
  };

  const runSearch = async () => {
    const text = query.trim();
    if (!text) {
      setError('请输入要搜索的关键词');
      setResults(null);
      return;
    }
    seqRef.current += 1;
    const seq = seqRef.current;
    setLoading(true);
    setError('');
    try {
      const items = await searchMemories({ query: text, limit: RESULT_LIMIT });
      if (seq !== seqRef.current) return;
      setResults(items);
      setSearched(text);
      setExpanded({});
    } catch (err) {
      if (seq !== seqRef.current) return;
      setResults(null);
      setError(err instanceof Error ? err.message : '搜索记忆失败，请稍后重试');
    } finally {
      if (seq === seqRef.current) setLoading(false);
    }
  };

  const scopeHint = onlyProject && projectName ? `只搜当前项目（${projectName}）` : '搜索全部项目';

  return (
    <div className="space-y-3 pb-2">
      <form
        className="flex items-center gap-2"
        onSubmit={(event) => {
          event.preventDefault();
          void runSearch();
        }}
      >
        <input
          className="settings-input flex-1"
          type="text"
          value={query}
          placeholder="搜索记忆标题与正文，回车搜索"
          aria-label="搜索记忆"
          onChange={(event) => setQuery(event.target.value)}
        />
        <button
          type="submit"
          disabled={loading}
          className="primary-button settings-inline-button shrink-0"
        >
          <SearchIcon className="h-3.5 w-3.5" />
          {loading ? '搜索中…' : '搜索'}
        </button>
      </form>

      {projectID ? (
        <label className="flex items-center gap-2 text-xs text-[var(--foreground-muted)]">
          <input
            type="checkbox"
            className="h-3.5 w-3.5"
            checked={onlyProject}
            onChange={(event) => setOnlyProject(event.target.checked)}
          />
          <span>{scopeHint}</span>
        </label>
      ) : (
        <p className="text-mc-caption text-[var(--foreground-muted)]">{scopeHint}</p>
      )}

      {error && (
        <p className="settings-error" role="alert">
          {error}
        </p>
      )}

      {results !== null && !error && (
        <p className="text-mc-caption text-[var(--foreground-muted)]" role="status">
          {results.length === 0
            ? `没有找到与「${searched}」相关的记忆`
            : `找到 ${results.length} 条记忆`}
        </p>
      )}

      <div className="space-y-3">
        {(results ?? []).map((entry) => {
          const isExpanded = Boolean(expanded[entry.id]);
          const labels = entry.labels ?? [];
          return (
            <article
              key={entry.id}
              className="p-3.5 rounded-2xl border border-slate-200/80 dark:border-zinc-800 bg-white/60 dark:bg-zinc-900/40 space-y-2.5"
            >
              <div className="flex items-start justify-between gap-2">
                <h3 className="min-w-0 text-sm font-semibold text-slate-800 dark:text-slate-100 break-words">
                  {entry.title || '（无标题）'}
                </h3>
                <span className="shrink-0 px-1.5 py-0.5 rounded text-mc-caption bg-slate-100 dark:bg-zinc-800 text-slate-600 dark:text-zinc-300">
                  {memoryUnitTypeLabel(entry.unitType)}
                </span>
              </div>

              {/* 正文摘要，点一下展开全文 */}
              <p
                onClick={() => toggleExpanded(entry.id)}
                className={`cursor-pointer whitespace-pre-wrap break-words text-xs leading-relaxed text-slate-700 dark:text-slate-300 ${
                  isExpanded ? '' : 'line-clamp-3'
                }`}
                title={isExpanded ? '点击收起正文' : '点击展开全文'}
              >
                {entry.content || '（无正文）'}
              </p>

              {labels.length > 0 && (
                <div className="flex flex-wrap gap-1.5">
                  {labels.map((label) => (
                    <span
                      key={label}
                      className="px-1.5 py-0.5 rounded text-mc-caption bg-slate-100 dark:bg-zinc-800 text-slate-600 dark:text-zinc-300 break-all"
                    >
                      {label}
                    </span>
                  ))}
                </div>
              )}

              <div className="text-mc-caption text-slate-500 dark:text-zinc-400">
                {formatTime(entry.createdAt)}
              </div>
            </article>
          );
        })}
      </div>
    </div>
  );
};
