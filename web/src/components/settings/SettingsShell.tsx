// 设置外壳：宽屏是左右两栏（左侧分组导航），窄屏是全屏页面（顶部分类 tab）。
// 具体每页的内容由调用方通过 renderPage 提供，这里不认识任何设置项。

import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useSheetDrag } from '../../hooks/useSheetDrag';
import {
  ArchiveBoxIcon,
  ChevronRightIcon,
  ComputerDesktopIcon,
  DocumentTextIcon,
  LayoutIcon,
  MessageIcon,
  SearchIcon,
  SettingsIcon,
  SparklesIcon,
  StarIcon,
  SunIcon,
  XMarkIcon
} from '../Icons';
import {
  SETTINGS_GROUPS,
  SETTINGS_ITEMS,
  SETTINGS_PAGES,
  findSettingsPage,
  matchSettingsItems
} from './settingsIndex';
import type { SettingsPageIcon } from './settingsIndex';
import { HapticSection } from './HapticSection';

export interface SettingsAdvancedState {
  open: boolean;
  setOpen: (open: boolean) => void;
}

export interface SettingsShellProps {
  onClose: () => void;
  // 当前可见的页与设置项（旧宿主缺键、或宿主不支持远程设置时由调用方过滤）。
  visiblePageIDs: string[];
  visibleItemIDs: string[];
  renderPage: (pageID: string, advanced: SettingsAdvancedState) => React.ReactNode;
  // 受控页 id：不传时用内部状态（老宿主行为不变）；为空时窄屏回落到上次停留的页或第一页。
  pageID?: string | null;
  onPageChange?: (pageID: string | null) => void;
}

const FLASH_MS = 1500;
const LAST_PAGE_KEY = 'mcode.settings.lastPage';

// 页面图标只引用 Icons.tsx 里已有的图标，按语义挑；settingsIndex 里只存名字。
const PAGE_ICONS: Record<SettingsPageIcon, React.FC<{ className?: string }>> = {
  message: MessageIcon,
  sparkles: SparklesIcon,
  layout: LayoutIcon,
  document: DocumentTextIcon,
  settings: SettingsIcon,
  sun: SunIcon,
  archive: ArchiveBoxIcon,
  desktop: ComputerDesktopIcon,
  star: StarIcon
};

// 窄屏记住上次停留的页，下次打开设置直接回到它。
function readLastPage(): string | null {
  if (typeof window === 'undefined') return null;
  try {
    return window.sessionStorage.getItem(LAST_PAGE_KEY);
  } catch {
    return null;
  }
}

function writeLastPage(pageID: string) {
  try {
    window.sessionStorage.setItem(LAST_PAGE_KEY, pageID);
  } catch {
    // 隐私模式下 sessionStorage 不可写时忽略即可。
  }
}

// 宽屏（视口宽度不小于 900px）用左右两栏，窄屏是全屏页面加顶部分类 tab。
function useIsWideScreen(): boolean {
  const [wide, setWide] = useState<boolean>(() =>
    typeof window === 'undefined' ? false : window.matchMedia('(min-width: 900px)').matches
  );
  useEffect(() => {
    const query = window.matchMedia('(min-width: 900px)');
    const onChange = (event: MediaQueryListEvent) => setWide(event.matches);
    setWide(query.matches);
    query.addEventListener('change', onChange);
    return () => query.removeEventListener('change', onChange);
  }, []);
  return wide;
}

export const SettingsShell: React.FC<SettingsShellProps> = ({
  onClose,
  visiblePageIDs,
  visibleItemIDs,
  renderPage,
  pageID: controlledPageID,
  onPageChange
}) => {
  const isWide = useIsWideScreen();
  // 受控时页 id 由宿主（#/settings/<id>）决定，非受控时留在组件内部。
  const [internalPageID, setInternalPageID] = useState<string | null>(null);
  const pageID = controlledPageID === undefined ? internalPageID : controlledPageID;
  const [lastPageID, setLastPageID] = useState<string | null>(readLastPage);
  const [query, setQuery] = useState('');
  const [searchOpen, setSearchOpen] = useState(false);
  const [advancedOpen, setAdvancedOpen] = useState<Record<string, boolean>>({});
  const [pending, setPending] = useState<{ pageID: string; itemID: string } | null>(null);
  const [scrolled, setScrolled] = useState(false);
  const contentRef = useRef<HTMLDivElement | null>(null);
  const searchInputRef = useRef<HTMLInputElement | null>(null);
  // 窄屏设置是底部上滑的全屏 sheet：拖把手可以关闭；宽屏两栏弹窗不接。
  const shellRef = useRef<HTMLElement | null>(null);
  const shellHandleRef = useRef<HTMLDivElement | null>(null);
  useSheetDrag(shellRef, shellHandleRef, onClose, !isWide);
  const tabRefs = useRef<Record<string, HTMLButtonElement | null>>({});
  const flashTimer = useRef<number | null>(null);
  const focusTimer = useRef<number | null>(null);
  const previousViewKey = useRef<string | null>(null);

  const pages = useMemo(
    () => SETTINGS_PAGES.filter((page) => visiblePageIDs.includes(page.id)),
    [visiblePageIDs]
  );
  const items = useMemo(
    () => SETTINGS_ITEMS.filter((item) => visibleItemIDs.includes(item.id)),
    [visibleItemIDs]
  );
  const isVisiblePage = useCallback(
    (id: string | null): id is string => Boolean(id) && pages.some((entry) => entry.id === id),
    [pages]
  );
  // 不存在或当前不可见的页视为未指定：宽屏回到第一页，窄屏回到上次停留的页再回落到第一页。
  const requestedPageID = isVisiblePage(pageID) ? pageID : null;
  const fallbackPageID =
    (!isWide && isVisiblePage(lastPageID) ? lastPageID : null) ?? pages[0]?.id ?? null;
  const effectivePageID = requestedPageID ?? fallbackPageID;
  const page = effectivePageID ? findSettingsPage(effectivePageID) : undefined;

  const hits = useMemo(() => matchSettingsItems(query, items), [query, items]);
  const searching = query.trim().length > 0;

  // 窄屏记住当前页，下次打开设置直接回到这里。
  useEffect(() => {
    if (isWide || !effectivePageID || effectivePageID === lastPageID) return;
    setLastPageID(effectivePageID);
    writeLastPage(effectivePageID);
  }, [isWide, effectivePageID, lastPageID]);

  const setAdvanced = useCallback((targetPage: string, open: boolean) => {
    setAdvancedOpen((current) => ({ ...current, [targetPage]: open }));
  }, []);

  // 切换 tab 不压浏览历史：宿主用 replace 写 #/settings/<id>，系统返回手势直接关闭设置。
  const setPage = useCallback(
    (nextPageID: string | null) => {
      if (controlledPageID === undefined) {
        setInternalPageID(nextPageID);
        return;
      }
      onPageChange?.(nextPageID);
    },
    [controlledPageID, onPageChange]
  );

  const openPage = useCallback(
    (nextPageID: string) => {
      setQuery('');
      setSearchOpen(false);
      setPage(nextPageID);
    },
    [setPage]
  );

  // 跳到某条设置：切页、高级项先展开、滚动到该行并短暂高亮。
  const jumpToItem = useCallback(
    (itemID: string, targetPage: string, advanced: boolean) => {
      setQuery('');
      setSearchOpen(false);
      setPage(targetPage);
      if (advanced) setAdvanced(targetPage, true);
      setPending({ pageID: targetPage, itemID });
    },
    [setAdvanced, setPage]
  );

  // 切 tab 或进出搜索时内容回到顶部。
  const viewKey = searching ? 'search' : effectivePageID ?? 'none';
  useEffect(() => {
    if (previousViewKey.current === viewKey) return;
    previousViewKey.current = viewKey;
    const content = contentRef.current;
    if (!content) return;
    content.scrollTop = 0;
    setScrolled(false);
  }, [viewKey]);

  // 先归零再定位：上面这个 effect 与下面这个 effect 同批提交，顺序保证定位不被归零覆盖。
  useEffect(() => {
    if (!pending) return;
    if (effectivePageID !== pending.pageID) return;
    const row = contentRef.current?.querySelector<HTMLElement>(
      `[data-settings-item="${pending.itemID}"]`
    );
    setPending(null);
    if (!row) return;
    row.scrollIntoView({ block: 'center', behavior: 'smooth' });
    row.classList.add('settings-row--flash');
    if (flashTimer.current !== null) window.clearTimeout(flashTimer.current);
    flashTimer.current = window.setTimeout(() => {
      row.classList.remove('settings-row--flash');
      flashTimer.current = null;
    }, FLASH_MS);
  }, [pending, effectivePageID, advancedOpen]);

  // 选中 tab 滚到可见范围：只影响 tab 条自己的横向滚动。
  useEffect(() => {
    if (isWide || !effectivePageID) return;
    tabRefs.current[effectivePageID]?.scrollIntoView({ inline: 'nearest', block: 'nearest' });
  }, [isWide, effectivePageID]);

  // 点搜索按钮后输入框立刻聚焦。
  useEffect(() => {
    if (!searchOpen) return;
    searchInputRef.current?.focus();
  }, [searchOpen]);

  useEffect(
    () => () => {
      if (flashTimer.current !== null) window.clearTimeout(flashTimer.current);
      if (focusTimer.current !== null) window.clearTimeout(focusTimer.current);
    },
    []
  );

  // 软键盘弹出会缩小 --app-height：等布局稳定后再把聚焦的输入框滚进可视区。
  const handleFocus = (event: React.FocusEvent<HTMLElement>) => {
    if (isWide) return;
    const target = event.target as HTMLElement;
    if (!target.matches('input, textarea')) return;
    if (focusTimer.current !== null) window.clearTimeout(focusTimer.current);
    focusTimer.current = window.setTimeout(() => {
      focusTimer.current = null;
      target.scrollIntoView({ block: 'nearest' });
    }, 260);
  };

  const advanced: SettingsAdvancedState = {
    open: effectivePageID ? Boolean(advancedOpen[effectivePageID]) : false,
    setOpen: (open: boolean) => {
      if (effectivePageID) setAdvanced(effectivePageID, open);
    }
  };

  const nav = (
    <nav className="settings-nav" aria-label="设置分类">
      {SETTINGS_GROUPS.map((group) => {
        const groupPages = pages.filter((entry) => entry.group === group.id);
        if (groupPages.length === 0) return null;
        return (
          <div key={group.id} className="settings-nav-group">
            <p className="settings-nav-group__title">{group.title}</p>
            <div className="settings-nav-list">
              {groupPages.map((entry) => {
                const Icon = entry.icon ? PAGE_ICONS[entry.icon] : null;
                return (
                  <button
                    key={entry.id}
                    type="button"
                    className={`settings-nav-item${entry.id === effectivePageID ? ' is-active' : ''}`}
                    onClick={() => openPage(entry.id)}
                    aria-current={entry.id === effectivePageID ? 'page' : undefined}
                  >
                    <span className="settings-nav-item__label">
                      {Icon ? <Icon className="w-4 h-4 settings-nav-icon" /> : null}
                      <span>{entry.title}</span>
                    </span>
                    <ChevronRightIcon className="w-4 h-4 settings-nav-chevron" />
                  </button>
                );
              })}
            </div>
          </div>
        );
      })}
    </nav>
  );

  // 窄屏分类 tab：按页面顺序排列，不显示分组标题，只有它自己能横向滑动。
  const tabs = (
    <div className="settings-tabs" role="tablist" aria-label="设置分类">
      {pages.map((entry) => {
        const Icon = entry.icon ? PAGE_ICONS[entry.icon] : null;
        const active = entry.id === effectivePageID;
        return (
          <button
            key={entry.id}
            ref={(element) => {
              tabRefs.current[entry.id] = element;
            }}
            type="button"
            role="tab"
            aria-selected={active}
            className={`settings-tab${active ? ' is-active' : ''}`}
            onClick={() => openPage(entry.id)}
          >
            {Icon ? <Icon className="w-4 h-4 settings-tab__icon" /> : null}
            <span className="settings-tab__label">{entry.title}</span>
          </button>
        );
      })}
    </div>
  );

  const results = searching ? (
    hits.length === 0 ? (
      <p className="settings-empty">没有匹配的设置</p>
    ) : (
      <div className="settings-results">
        {hits.map(({ item, rank }) => (
          <button
            key={`${item.id}-${rank}`}
            type="button"
            className="settings-result"
            onClick={() => jumpToItem(item.id, item.page, Boolean(item.advanced))}
          >
            <span className="settings-result__title">{item.title}</span>
            <span className="settings-result__meta">
              {findSettingsPage(item.page)?.title ?? item.page} · {item.section}
            </span>
          </button>
        ))}
      </div>
    )
  ) : null;

  // 窄屏页标题由 tab 表明，滚动区里不再重复一遍。
  const pageView = page ? (
    <div className="settings-page">
      {isWide ? <h3 className="settings-page-title">{page.title}</h3> : null}
      {renderPage(page.id, advanced)}
      {page.id === 'appearance' ? <HapticSection /> : null}
    </div>
  ) : null;

  const closeButton = (
    <button
      type="button"
      className="icon-button settings-topbar-icon"
      onClick={onClose}
      aria-label="关闭设置"
      title="关闭"
    >
      <XMarkIcon className="h-4 w-4" />
    </button>
  );

  const searchField = (
    <label className="settings-search">
      <SearchIcon className="w-4 h-4 settings-search-icon" />
      <input
        type="search"
        value={query}
        onChange={(event) => setQuery(event.target.value)}
        placeholder="搜索设置"
        aria-label="搜索设置"
      />
    </label>
  );

  const cancelSearch = () => {
    setQuery('');
    setSearchOpen(false);
  };

  const head = isWide ? (
    <>
      <div className="flex items-center justify-between gap-3">
        <div>
          <p className="eyebrow">mcode</p>
          <h2 className="sheet-title">设置</h2>
        </div>
        {closeButton}
      </div>
      {searchField}
    </>
  ) : (
    <>
      <div className="settings-topbar">
        {searchOpen ? (
          <>
            <label className="settings-search settings-search--topbar">
              <SearchIcon className="w-4 h-4 settings-search-icon" />
              <input
                ref={searchInputRef}
                type="search"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="搜索设置"
                aria-label="搜索设置"
              />
            </label>
            <button type="button" className="settings-topbar-cancel" onClick={cancelSearch}>
              取消
            </button>
          </>
        ) : (
          <>
            <h2 className="settings-topbar-title">设置</h2>
            <button
              type="button"
              className="icon-button settings-topbar-icon"
              onClick={() => setSearchOpen(true)}
              aria-label="搜索设置"
              title="搜索"
            >
              <SearchIcon className="h-5 w-5" />
            </button>
            {closeButton}
          </>
        )}
      </div>
      {tabs}
    </>
  );

  return (
    <div className="sheet-layer" role="presentation" onMouseDown={onClose}>
      <section
        ref={shellRef}
        className={`settings-sheet settings-shell${isWide ? ' settings-shell--wide' : ' mobile-page'}`}
        role="dialog"
        aria-modal="true"
        aria-label="设置"
        onMouseDown={(event) => event.stopPropagation()}
        onFocus={handleFocus}
      >
        <div ref={shellHandleRef} className={`settings-shell-head${scrolled ? ' is-scrolled' : ''}`}>{head}</div>

        <div className="settings-shell-body">
          {isWide ? nav : null}
          <div
            className="settings-shell-content"
            ref={contentRef}
            onScroll={(event) => setScrolled(event.currentTarget.scrollTop > 0)}
          >
            {searching ? results : pageView}
            {!searching && !pageView ? (
              <p className="settings-empty">这个 Mac 端暂时没有可调整的设置。</p>
            ) : null}
          </div>
        </div>
      </section>
    </div>
  );
};
