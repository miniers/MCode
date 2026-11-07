// 会话菜单页面栈的页面模型：种类、路由映射、标题与尺寸分类。
// 路由只覆盖五个详情页（agents/review/git/scripts/files），其余页面不改 hash。

export type ThreadMenuPageKind =
  | 'main' | 'swarm' | 'workspace' | 'export'
  | 'agents' | 'review' | 'git' | 'scripts' | 'files'
  | 'mcp' | 'worktree' | 'save-task' | 'forward';

export type ThreadSubview = 'agents' | 'review' | 'git' | 'scripts' | 'files';

export interface ThreadMenuPageSpec {
  kind: ThreadMenuPageKind;
  /** 仅 files 栈底使用：进入文件页时的初始路径 */
  filePath?: string;
}

export interface ThreadMenuPage extends ThreadMenuPageSpec {
  /** 自增，用于 React key 与动画定位 */
  id: string;
}

export interface ThreadMenuPageHandle {
  /** true 表示可以关闭/出栈；false 表示被页面守卫拦下 */
  requestClose: () => boolean;
}

/** 菜单形态的紧凑页：内容高度自适应；详情页则使用固定高度的面板。 */
const COMPACT_PAGE_KINDS: ReadonlySet<ThreadMenuPageKind> = new Set<ThreadMenuPageKind>([
  'main',
  'swarm',
  'workspace',
  'export',
  'save-task',
  'forward'
]);

export function isCompactPageKind(kind: ThreadMenuPageKind): boolean {
  return COMPACT_PAGE_KINDS.has(kind);
}

/** 页面尺寸：compact 页用 menu（内容高度），详情页用 panel（固定高度）。 */
export function pageSizeFor(kind: ThreadMenuPageKind): 'menu' | 'panel' {
  return isCompactPageKind(kind) ? 'menu' : 'panel';
}

export const MENU_PAGE_TITLES: Record<ThreadMenuPageKind, string> = {
  main: '会话菜单',
  swarm: '编排',
  workspace: '工作区',
  export: '导出会话',
  agents: '子代理进度与汇报',
  review: '代码变更与审阅',
  git: 'Git 工作区',
  scripts: '运行脚本',
  files: '工作区文件',
  mcp: 'MCP 服务器',
  worktree: '移到 worktree',
  'save-task': '存为看板任务',
  forward: '转发到新会话'
};

/** 可路由页 → hash 后缀；不可路由页返回 null。 */
export function routeForPage(page: ThreadMenuPageSpec): string | null {
  switch (page.kind) {
    case 'agents':
      return '/agents';
    case 'review':
      return '/review';
    case 'git':
      return '/git';
    case 'scripts':
      return '/scripts';
    case 'files':
      return page.filePath ? `/files/${encodeURIComponent(page.filePath)}` : '/files';
    default:
      return null;
  }
}

/** 路由解析结果 → 栈底页；无 subview 时是菜单主页。 */
export function pageFromRoute(subview?: ThreadSubview | null, filePath?: string): ThreadMenuPageSpec {
  if (!subview) return { kind: 'main' };
  if (subview === 'files') {
    return filePath ? { kind: 'files', filePath } : { kind: 'files' };
  }
  return { kind: subview };
}

export function isRoutablePage(kind: ThreadMenuPageKind): boolean {
  return routeForPage({ kind }) !== null;
}
