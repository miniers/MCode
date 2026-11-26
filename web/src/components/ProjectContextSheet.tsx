// 只读项目知识库文档面板：支持独立抽屉（SheetFrame size="panel"）与嵌入 ThreadMenuSheet 栈内页两种形态；
// 支持全部项目切换、标准/项目文档分组、搜索过滤、Markdown 渲染与复制。

import React, { useCallback, useEffect, useImperativeHandle, useMemo, useRef, useState } from 'react';
import '../styles/skeleton.css';
import { useRemoteStatic } from '../context/RemoteContext';
import { useProjectContextDocs } from '../hooks/useProjectContextDocs';
import type { RemoteContextDocSummary } from '../types/generated';
import { pickableProjects } from '../utils/builtinProjects';
import {
  ArrowPathIcon,
  BookOpenIcon,
  CheckCircleIcon,
  ChevronRightIcon,
  DocumentTextIcon,
  SearchIcon,
  XMarkIcon
} from './Icons';
import { MarkdownView } from './MarkdownView';
import { SheetFrame } from './sheet/SheetFrame';
import { MenuSheetHeader } from './thread/menu/MenuSheetHeader';
import { useMenuPageChrome } from './thread/menu/menuPageChrome';
import type { ThreadMenuPageHandle } from './thread/menu/menuPages';
import { useIsWideSheet } from './thread/menu/useIsWideSheet';

export interface ProjectContextSheetProps {
  projectID: string | null;
  embedded?: boolean;
  isOpen?: boolean;
  onClose?: () => void;
}

function matchesDocQuery(doc: RemoteContextDocSummary, needle: string): boolean {
  if (!needle) return true;
  const lower = needle.toLowerCase();
  return (
    doc.title.toLowerCase().includes(lower) ||
    doc.name.toLowerCase().includes(lower)
  );
}

function ContextDocSkeleton({ lines = 5 }: { lines?: number }) {
  return (
    <div className="mc-skeleton gap-mc-3 py-mc-2" role="status" aria-live="polite" aria-busy="true">
      <span className="mc-skeleton-label">正在加载文档…</span>
      {Array.from({ length: lines }, (_, index) => (
        <div
          key={index}
          className="mc-skeleton-block mc-skeleton-line"
          style={{ width: index % 2 === 0 ? '76%' : '52%' }}
        />
      ))}
    </div>
  );
}

export const ProjectContextSheet = React.forwardRef<ThreadMenuPageHandle, ProjectContextSheetProps>(
  function ProjectContextSheet({ projectID, embedded = false, isOpen = true, onClose }, ref) {
    const { projects, connectionState } = useRemoteStatic();
    const chrome = useMenuPageChrome();
    const wide = useIsWideSheet();

    useImperativeHandle(ref, () => ({ requestClose: () => true }), []);

    const active = embedded ? isOpen : isOpen !== false;
    const isConnected = connectionState === 'connected';
    const splitPane = wide && !embedded;

    const availableProjects = useMemo(() => {
      const picked = pickableProjects(projects);
      return picked.length > 0 ? picked : projects;
    }, [projects]);

    const [fallbackProjectID, setFallbackProjectID] = useState<string | null>(null);
    const effectiveProjectID = useMemo(() => {
      if (projectID) return projectID;
      if (fallbackProjectID && availableProjects.some((item) => item.id === fallbackProjectID)) {
        return fallbackProjectID;
      }
      return availableProjects[0]?.id ?? null;
    }, [availableProjects, fallbackProjectID, projectID]);

    const activeProject = useMemo(
      () => availableProjects.find((item) => item.id === effectiveProjectID) ?? null,
      [availableProjects, effectiveProjectID]
    );

    const {
      docs,
      selectedName,
      doc,
      loadingList,
      loadingDoc,
      error,
      supported,
      selectDoc,
      refresh
    } = useProjectContextDocs(effectiveProjectID, active && isConnected);

    const [searchQuery, setSearchQuery] = useState('');
    const readerRef = useRef<HTMLDivElement>(null);
    const [headings, setHeadings] = useState<HTMLHeadingElement[]>([]);
    const [mobileViewingDoc, setMobileViewingDoc] = useState(false);
    const [copied, setCopied] = useState(false);
    const copyTimerRef = useRef<number | null>(null);

    useEffect(
      () => () => {
        if (copyTimerRef.current !== null) window.clearTimeout(copyTimerRef.current);
      },
      []
    );

    useEffect(() => {
      setSearchQuery('');
      setMobileViewingDoc(false);
      setCopied(false);
    }, [effectiveProjectID]);

    // 宽屏分栏模式下自动选中首篇已存在文档（若都未创建则选第一篇），避免右侧空白。
    useEffect(() => {
      if (!splitPane || selectedName || docs.length === 0) return;
      const preferred = docs.find((item) => item.exists) ?? docs[0];
      if (preferred) selectDoc(preferred.name);
    }, [docs, selectDoc, selectedName, splitPane]);

    const handleSelectDoc = useCallback(
      (name: string) => {
        setCopied(false);
        selectDoc(name);
        if (!splitPane) {
          setMobileViewingDoc(true);
        }
      },
      [selectDoc, splitPane]
    );

    const handleCopyDoc = useCallback(async () => {
      if (!doc?.exists || !doc.content || !navigator.clipboard?.writeText) return;
      try {
        await navigator.clipboard.writeText(doc.content);
        setCopied(true);
        if (copyTimerRef.current !== null) window.clearTimeout(copyTimerRef.current);
        copyTimerRef.current = window.setTimeout(() => {
          copyTimerRef.current = null;
          setCopied(false);
        }, 1500);
      } catch {
        // 剪贴板写入失败时保持原按钮状态
      }
    }, [doc]);

    const refreshButton = useMemo(
      () => (
        <button
          type="button"
          className="icon-button"
          onClick={refresh}
          disabled={!effectiveProjectID || !supported || loadingList || loadingDoc}
          aria-label="刷新项目文档"
          title="刷新"
        >
          <ArrowPathIcon className={`h-4 w-4 ${loadingList || loadingDoc ? 'animate-spin' : ''}`} />
        </button>
      ),
      [effectiveProjectID, loadingDoc, loadingList, refresh, supported]
    );

    // 嵌入 ThreadMenuSheet 时，把刷新按钮挂载到共享标题栏右侧，避免重复标题栏。
    const setChromeTrailing = chrome.setTrailing;
    const hasOuterMenuHeader = embedded && (chrome.leading === 'back' || chrome.leading === 'close');
    useEffect(() => {
      if (!hasOuterMenuHeader || !setChromeTrailing) return;
      setChromeTrailing(refreshButton);
      return () => setChromeTrailing(null);
    }, [hasOuterMenuHeader, refreshButton, setChromeTrailing]);

    const needle = searchQuery.trim();
    const filteredDocs = useMemo(
      () => docs.filter((item) => matchesDocQuery(item, needle)),
      [docs, needle]
    );
    const standardDocs = useMemo(
      () => filteredDocs.filter((item) => item.standard),
      [filteredDocs]
    );
    const projectDocs = useMemo(
      () => filteredDocs.filter((item) => !item.standard),
      [filteredDocs]
    );

    const selectedSummary = useMemo(
      () => docs.find((item) => item.name === selectedName) ?? null,
      [docs, selectedName]
    );

    useEffect(() => {
      const reader = readerRef.current;
      setHeadings(reader ? Array.from(reader.querySelectorAll('h2[data-context-heading]')) : []);
      if (reader) reader.scrollTop = 0;
    }, [active, doc, loadingDoc, mobileViewingDoc, splitPane]);

    if (!active) return null;

    const projectSwitcher =
      projectID === null && availableProjects.length > 0 ? (
        <div className="filter-row !mt-0 py-1 shrink-0" role="toolbar" aria-label="选择项目查看文档">
          {availableProjects.map((project) => {
            const selected = effectiveProjectID === project.id;
            return (
              <button
                key={project.id}
                type="button"
                className={`filter-chip touch-hit-44 ${selected ? 'filter-chip--active' : ''}`}
                aria-pressed={selected}
                onClick={() => {
                  setFallbackProjectID(project.id);
                  setMobileViewingDoc(false);
                }}
              >
                <span>{project.name}</span>
              </button>
            );
          })}
        </div>
      ) : null;

    const renderDocRow = (item: RemoteContextDocSummary) => {
      const selected = selectedName === item.name;
      return (
        <button
          key={item.name}
          type="button"
          onClick={() => handleSelectDoc(item.name)}
          aria-current={selected ? 'true' : undefined}
          className={`flex w-full min-h-[44px] items-center justify-between gap-2 rounded-mc-lg px-3 py-2 text-left transition ${
            selected
              ? 'bg-[var(--mc-overlay-selected)] text-[var(--mc-text-primary)]'
              : 'hover:bg-[var(--mc-overlay-hover)] text-[var(--mc-text-primary)]'
          }`}
        >
          <span className="flex min-w-0 flex-1 items-center gap-2">
            <DocumentTextIcon className="h-4 w-4 shrink-0 text-[var(--mc-text-secondary)]" aria-hidden="true" />
            <span className="min-w-0 flex-1">
              <span className="block truncate text-mc-body font-medium">
                {item.title || item.name}
              </span>
              {item.title && item.title !== item.name && (
                <span className="block truncate text-mc-caption text-[var(--mc-text-tertiary)] font-mono">
                  {item.name}
                </span>
              )}
            </span>
          </span>
          <span className="flex shrink-0 items-center gap-1.5">
            {!item.exists && (
              <span className="rounded-mc-sm bg-[var(--mc-overlay-hover)] px-1.5 py-0.5 text-mc-caption text-[var(--mc-text-secondary)]">
                未创建
              </span>
            )}
            <ChevronRightIcon className="h-3.5 w-3.5 text-[var(--mc-text-tertiary)]" aria-hidden="true" />
          </span>
        </button>
      );
    };

    const listPane = (
      <div className="flex min-h-0 flex-1 flex-col gap-mc-3">
        <div className="search-field shrink-0">
          <SearchIcon className="h-4 w-4 shrink-0 text-[var(--mc-text-secondary)]" aria-hidden="true" />
          <input
            type="search"
            value={searchQuery}
            onChange={(event) => setSearchQuery(event.target.value)}
            placeholder="搜索文档"
            aria-label="搜索项目文档"
            className="text-base"
            autoComplete="off"
          />
          {searchQuery && (
            <button
              type="button"
              className="search-field__clear touch-hit-44"
              aria-label="清除文档搜索"
              onClick={() => setSearchQuery('')}
            >
              <XMarkIcon className="h-3.5 w-3.5" />
            </button>
          )}
        </div>

        <div className="flex-1 min-h-0 overflow-y-auto space-y-4 pr-0.5">
          {loadingList && docs.length === 0 ? (
            <ContextDocSkeleton lines={4} />
          ) : filteredDocs.length === 0 ? (
            <div className="empty-state">
              <div className="empty-state__icon">
                <BookOpenIcon className="h-6 w-6" aria-hidden="true" />
              </div>
              <h2>{needle ? '没有匹配的文档' : '暂无项目文档'}</h2>
            </div>
          ) : (
            <>
              {standardDocs.length > 0 && (
                <section aria-label="标准文档" className="space-y-1">
                  <p className="px-1 text-mc-caption font-semibold uppercase tracking-wide text-[var(--mc-text-tertiary)]">
                    标准文档
                  </p>
                  <div className="space-y-0.5">
                    {standardDocs.map(renderDocRow)}
                  </div>
                </section>
              )}
              {projectDocs.length > 0 && (
                <section aria-label="项目文档" className="space-y-1">
                  <p className="px-1 text-mc-caption font-semibold uppercase tracking-wide text-[var(--mc-text-tertiary)]">
                    项目文档
                  </p>
                  <div className="space-y-0.5">
                    {projectDocs.map(renderDocRow)}
                  </div>
                </section>
              )}
            </>
          )}
        </div>
      </div>
    );

    const activeTitle = doc?.title || selectedSummary?.title || selectedName || '';
    const canCopyDoc = Boolean(doc?.exists && doc.content);

    const readerPane = (
      <div className="flex min-h-0 flex-1 flex-col gap-mc-3">
        <div className="flex shrink-0 items-center justify-between gap-2 border-b border-[var(--mc-hairline)] pb-2">
          <div className="flex min-w-0 items-center gap-2">
            {!splitPane && (
              <button
                type="button"
                onClick={() => setMobileViewingDoc(false)}
                className="secondary-button min-h-[36px] touch-hit-44 px-2.5 text-mc-footnote shrink-0"
                aria-label="返回文档列表"
              >
                <ChevronRightIcon className="h-4 w-4 rotate-180" aria-hidden="true" />
                <span>列表</span>
              </button>
            )}
            <h3 className="truncate text-mc-title font-semibold text-[var(--mc-text-primary)]">
              {activeTitle || '选择文档'}
            </h3>
          </div>
          {canCopyDoc && (
            <button
              type="button"
              onClick={() => void handleCopyDoc()}
              className="secondary-button min-h-[36px] touch-hit-44 px-3 text-mc-footnote shrink-0"
              aria-label={copied ? '已复制文档' : '复制文档内容'}
            >
              {copied ? (
                <>
                  <CheckCircleIcon className="h-3.5 w-3.5 text-[var(--mc-success)]" aria-hidden="true" />
                  <span>已复制</span>
                </>
              ) : (
                <span>复制</span>
              )}
            </button>
          )}
        </div>

        {headings.length > 0 && (
          <select
            value=""
            className="settings-input shrink-0"
            aria-label="跳转到章节"
            onChange={(event) => {
              if (!event.target.value) return;
              const heading = headings[Number(event.target.value)];
              heading?.focus({ preventScroll: true });
              heading?.scrollIntoView({ block: 'start' });
            }}
          >
            <option value="" disabled>跳转到章节</option>
            {headings.map((heading, index) => <option key={index} value={index}>{heading.textContent}</option>)}
          </select>
        )}

        {doc?.truncated && (
          <div
            role="status"
            className="shrink-0 rounded-mc-md border border-[var(--mc-border)] bg-[var(--mc-surface-card)] px-3 py-1.5 text-mc-caption text-[var(--mc-warning)]"
          >
            内容已截断
          </div>
        )}

        <div ref={readerRef} className="flex-1 min-h-0 overflow-y-auto overscroll-contain pr-1 select-text">
          {!selectedName ? (
            <div className="empty-state">
              <div className="empty-state__icon">
                <BookOpenIcon className="h-6 w-6" aria-hidden="true" />
              </div>
              <h2>选择文档开始阅读</h2>
            </div>
          ) : loadingDoc && !doc ? (
            <ContextDocSkeleton lines={6} />
          ) : doc && !doc.exists ? (
            <div className="empty-state">
              <div className="empty-state__icon">
                <DocumentTextIcon className="h-6 w-6" aria-hidden="true" />
              </div>
              <h2>尚未创建</h2>
              <p>{doc.name}</p>
            </div>
          ) : selectedSummary && !selectedSummary.exists && !doc ? (
            <div className="empty-state">
              <div className="empty-state__icon">
                <DocumentTextIcon className="h-6 w-6" aria-hidden="true" />
              </div>
              <h2>尚未创建</h2>
              <p>{selectedSummary.name}</p>
            </div>
          ) : doc ? (
            <MarkdownView text={doc.content} contextHeadings />
          ) : null}
        </div>
      </div>
    );

    const content = (
      <div className="flex min-h-0 flex-1 flex-col gap-mc-3">
        {!hasOuterMenuHeader && (
          <MenuSheetHeader
            eyebrow={activeProject?.name ?? '项目知识库'}
            title="项目文档"
            labelledBy="project-context-title"
            leading={onClose ? 'close' : 'none'}
            onLeading={() => onClose?.()}
            trailing={refreshButton}
            showHandle={!embedded && !wide}
            closeLabel="关闭项目文档"
          />
        )}

        {projectSwitcher}

        {error && (
          <div className="inline-alert inline-alert--danger !my-0 shrink-0" role="alert">
            <span className="min-w-0 select-text break-words">{error}</span>
            <button type="button" className="inline-alert__action touch-hit-44" onClick={refresh}>
              重试
            </button>
          </div>
        )}

        {!supported ? (
          <div className="empty-state">
            <div className="empty-state__icon">
              <BookOpenIcon className="h-6 w-6" aria-hidden="true" />
            </div>
            <h2>当前 Mac 暂不支持项目文档</h2>
          </div>
        ) : !effectiveProjectID ? (
          <div className="empty-state">
            <div className="empty-state__icon">
              <BookOpenIcon className="h-6 w-6" aria-hidden="true" />
            </div>
            <h2>暂无可用项目</h2>
          </div>
        ) : splitPane ? (
          <div className="grid min-h-0 flex-1 grid-cols-[240px_minmax(0,1fr)] gap-4 pt-1">
            <div className="flex min-h-0 flex-col border-r border-[var(--mc-hairline)] pr-3">
              {listPane}
            </div>
            <div className="flex min-h-0 flex-col">
              {readerPane}
            </div>
          </div>
        ) : mobileViewingDoc && selectedName ? (
          readerPane
        ) : (
          listPane
        )}
      </div>
    );

    if (embedded) {
      return content;
    }

    return (
      <SheetFrame
        onRequestClose={() => onClose?.()}
        size="panel"
        cardClassName="context-sheet-panel"
        labelledBy="project-context-title"
      >
        {content}
      </SheetFrame>
    );
  }
);
