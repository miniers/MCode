// Web 版设置索引：分组、页面与设置项的登记表，外加逐项搜索的纯函数。
// Web 版只保留远程端需要的设置。
// 这里只放纯数据与纯函数，不依赖 SettingsForm，保证设置表单仍然可以懒加载。

export type SettingsGroupID = 'ai' | 'app';

export interface SettingsGroupDef {
  id: SettingsGroupID;
  title: string;
}

export const SETTINGS_GROUPS: SettingsGroupDef[] = [
  { id: 'ai', title: '智能体' },
  { id: 'app', title: '应用' }
];

// 导航图标：这里只存语义名，真实图标在 SettingsShell 里按名字映射到 Icons.tsx，
// 保证本文件仍然只有纯数据与纯函数（不引入 React 组件）。
export type SettingsPageIcon =
  | 'message'
  | 'layout'
  | 'document'
  | 'sun'
  | 'desktop'
  | 'star';

export interface SettingsPageDef {
  id: string;
  title: string;
  group: SettingsGroupID;
  // 导航项左侧图标；缺省时不显示图标。
  icon?: SettingsPageIcon;
  keywords: string[];
}

export const SETTINGS_PAGES: SettingsPageDef[] = [
  {
    id: 'conversation',
    title: '会话',
    group: 'ai',
    icon: 'message',
    keywords: [
      'conversation', 'session', 'chat', 'thread', 'reasoning', 'thinking',
      'compact', 'concise', 'concise-replies', 'verbose', 'comments', 'replies',
      'queue', 'insert', 'finish', 'finished', 'done', 'dormant',
      'permissions', 'permission', 'mode', 'approval', 'archived', 'restore',
      '会话', '对话', '思考', '排队', '压缩', '限额', '完成',
      '权限', '档位', '审批', '归档', '恢复', '休眠'
    ]
  },
  {
    id: 'swarm',
    title: '编排',
    group: 'ai',
    icon: 'layout',
    keywords: [
      'swarm', 'hydra', 'bees', 'heads', 'subagents', 'sub-agents', 'agents', 'team',
      'orchestrator', 'worker', 'pair', 'pairs', 'parallel', 'delegate', 'queue',
      '规划', '并行', '编排', '主代理', '子代理'
    ]
  },
  {
    id: 'steward',
    title: '总管',
    group: 'ai',
    icon: 'star',
    keywords: [
      'steward', 'watchdog', 'triage', 'escalate', 'dispatcher', 'follow-up',
      'stalled', 'pending', 'quiet',
      '默默', '总管', '值守', '待处理', '分诊', '跟进者', '总调度', '安静时段', '免打扰'
    ]
  },
  {
    id: 'nowledge',
    title: '记忆',
    group: 'ai',
    icon: 'document',
    keywords: ['nowledge', 'nmem', 'memory', 'working memory', 'threads', 'sync', '记忆', '上下文']
  },
  {
    id: 'skills',
    title: '技能',
    group: 'ai',
    icon: 'document',
    keywords: ['skills', 'agent', '技能', '扩展']
  },
  {
    id: 'appearance',
    title: '外观',
    group: 'app',
    icon: 'sun',
    keywords: [
      'appearance', 'theme', 'transparency', 'transparent', 'opacity', 'glass',
      'dark', 'light', 'accent', 'tint', 'catppuccin', 'dracula', 'tokyo', 'nord',
      'gruvbox', 'solarized', 'github', 'claude', 'codex', 'cursor', 'matrix',
      'text', 'size', 'font', 'zoom', 'wallpaper', 'sidebar', 'performance', 'gpu',
      '主题', '深色', '浅色', '文字大小', '字号'
    ]
  },
  {
    id: 'device',
    title: '此设备',
    group: 'app',
    icon: 'desktop',
    keywords: [
      'device', 'devices', 'pair', 'pairing', 'remote', 'entry', 'lan',
      '设备', '配对', '连接', '入口', '二维码', '重命名', '撤销'
    ]
  },
  {
    id: 'about',
    title: '关于',
    group: 'app',
    icon: 'document',
    keywords: [
      'about', 'version', 'build', 'schema', 'appversion', 'webbuildid',
      '关于', '版本', '版本号', '构建', '构建号', '协议', '协议版本'
    ]
  }
];

export interface SettingsItemDef {
  id: string;
  page: string;
  section: string;
  title: string;
  aliases?: string[];
  // 对应宿主 settings.set 的键；宿主没给这个键时该项隐藏。
  settingsKey?: string;
  // Web 独有的设置项（桌面版索引里没有对应行）。
  webOnly?: boolean;
}

export const SETTINGS_ITEMS: SettingsItemDef[] = [
  { id: 'row-compact-suggest-percent', page: 'conversation', section: '上下文压缩', title: '建议压缩比例', settingsKey: 'session.compact_suggest_percent' },
  { id: 'row-compact-suggest-max', page: 'conversation', section: '上下文压缩', title: '建议压缩上限', settingsKey: 'session.compact_suggest_max_k' },
  { id: 'row-git-auto-fetch', page: 'conversation', section: '版本控制', title: '自动抓取', aliases: ['git', 'fetch'], settingsKey: 'source_control.auto_fetch' },
  { id: 'row-webhook-url', page: 'device', section: '通知', title: 'Webhook', settingsKey: 'notifications.webhook_url' },
  { id: 'skills-list', page: 'skills', section: '技能', title: '已安装技能', aliases: ['Agent', '摘要', '扩展'], webOnly: true },
  // ---- 会话：回复 ----
  {
    id: 'row-auto-collapse-reasoning',
    page: 'conversation',
    section: '回复',
    title: '自动折叠思考过程',
    aliases: ['折叠', '思考'],
    settingsKey: 'session.auto_collapse_reasoning'
  },
  {
    id: 'row-pin-sent-message',
    page: 'conversation',
    section: '回复',
    title: '发送后置顶消息',
    aliases: ['置顶', '锚定', '滚动'],
    settingsKey: 'session.pin_sent_message'
  },
  {
    id: 'row-concise-replies',
    page: 'conversation',
    section: '回复',
    title: '简洁 AI 回复',
    settingsKey: 'session.concise_replies_enabled'
  },
  // ---- 会话：权限 ----
  {
    id: 'row-mode',
    page: 'conversation',
    section: '权限',
    title: '新会话默认权限档位',
    aliases: ['权限', '只读', '审批'],
    settingsKey: 'permissions.mode'
  },
  {
    id: 'block-always-allowed',
    page: 'conversation',
    section: '权限',
    title: '始终允许的操作',
    aliases: ['权限', '允许'],
    webOnly: true
  },
  // ---- 会话：归档 ----
  {
    id: 'row-auto-dormant-days',
    page: 'conversation',
    section: '归档',
    title: '无消息多少天后自动休眠',
    aliases: ['休眠', '归档'],
    settingsKey: 'archive.sidebar_auto_dormant_days'
  },

  // ---- 编排：编排 ----
  {
    id: 'row-swarm-enabled',
    page: 'swarm',
    section: '编排',
    title: '启用编排',
    aliases: ['子代理', 'swarm', '编排'],
    settingsKey: 'orchestration.enabled'
  },
  {
    id: 'row-swarm-max-bees',
    page: 'swarm',
    section: '编排',
    title: '并发上限',
    aliases: ['子代理', '并发'],
    settingsKey: 'orchestration.max_bees'
  },
  {
    id: 'row-swarm-auto-delegate',
    page: 'swarm',
    section: '编排',
    title: '自动委托',
    aliases: ['委托', '分派'],
    settingsKey: 'orchestration.auto_delegate'
  },
  {
    id: 'row-swarm-lead-plans-only',
    page: 'swarm',
    section: '编排',
    title: '主代理只规划',
    aliases: ['主代理'],
    settingsKey: 'orchestration.lead_plans_only'
  },
  {
    id: 'block-swarm-tiers',
    page: 'swarm',
    section: '编排',
    title: '子代理档位',
    aliases: ['档位', '只读模式', '白名单'],
    webOnly: true
  },
  // ---- 编排：完成后 ----
  {
    id: 'row-swarm-review-bees',
    page: 'swarm',
    section: '完成后',
    title: '合并前审查',
    aliases: ['审查', 'AI 审查'],
    settingsKey: 'orchestration.review_mode'
  },
  {
    id: 'row-swarm-auto-merge',
    page: 'swarm',
    section: '完成后',
    title: '自动合并',
    aliases: ['合并'],
    settingsKey: 'orchestration.auto_merge'
  },
  {
    id: 'row-swarm-auto-push',
    page: 'swarm',
    section: '完成后',
    title: '自动推送',
    aliases: ['推送'],
    settingsKey: 'orchestration.auto_push'
  },

  // ---- 总管 ----
  {
    id: 'row-steward-enabled',
    page: 'steward',
    section: '总管',
    title: '启用总管',
    aliases: ['总管', '默默'],
    settingsKey: 'steward.enabled'
  },
  // ---- 总管：安静时段 ----
  {
    id: 'web-steward-quiet-start',
    page: 'steward',
    section: '安静时段',
    title: '安静时段开始',
    aliases: ['总管', '免打扰'],
    settingsKey: 'steward.quiet_start',
    webOnly: true
  },
  {
    id: 'web-steward-quiet-end',
    page: 'steward',
    section: '安静时段',
    title: '安静时段结束',
    aliases: ['总管', '免打扰'],
    settingsKey: 'steward.quiet_end',
    webOnly: true
  },
  // ---- 总管：待处理入口（Web 独有） ----
  {
    id: 'web-attention-inbox',
    page: 'steward',
    section: '待处理',
    title: '待处理事项',
    aliases: ['待处理', '介入'],
    webOnly: true
  },

  // ---- 外观：主题 ----
  {
    id: 'row-theme',
    page: 'appearance',
    section: '主题',
    title: '主题',
    aliases: ['深色', '浅色']
  },
  {
    id: 'row-web-haptics',
    page: 'appearance',
    section: '反馈',
    title: '触感反馈',
    aliases: ['振动', '触感', 'haptic'],
    webOnly: true
  },

  // ---- 记忆：记忆提议入口（Web 独有） ----
  {
    id: 'web-memory-proposals',
    page: 'nowledge',
    section: '记忆',
    title: '记忆提议',
    aliases: ['记忆', '提案', 'Nowledge'],
    webOnly: true
  },

  // ---- 此设备：连接 ----
  {
    id: 'web-connection-refresh',
    page: 'device',
    section: '连接',
    title: '刷新会话与看板',
    aliases: ['刷新', '连接'],
    webOnly: true
  },
  {
    id: 'web-connection-entry',
    page: 'device',
    section: '连接',
    title: '当前入口',
    aliases: ['入口', '连接', 'https'],
    webOnly: true
  },
  {
    id: 'web-home-pairing',
    page: 'device',
    section: '连接',
    title: '主屏幕配对码',
    aliases: ['配对', '主屏幕'],
    webOnly: true
  },
  // ---- 此设备：主屏幕与通知（Web 独有，常驻入口） ----
  {
    id: 'row-web-install',
    page: 'device',
    section: '主屏幕与通知',
    title: '添加到主屏幕',
    aliases: ['安装', '主屏幕', 'PWA', 'install'],
    webOnly: true
  },
  {
    id: 'row-web-push',
    page: 'device',
    section: '主屏幕与通知',
    title: '推送通知',
    aliases: ['通知', '推送', 'push', 'notification'],
    webOnly: true
  },
  // ---- 此设备：此设备 ----
  {
    id: 'web-device-name',
    page: 'device',
    section: '此设备',
    title: '设备名称',
    aliases: ['设备', '改名'],
    webOnly: true
  },
  {
    id: 'web-device-remember',
    page: 'device',
    section: '此设备',
    title: '记住此设备',
    aliases: ['设备', '配对'],
    webOnly: true
  },
  {
    id: 'web-device-forget',
    page: 'device',
    section: '此设备',
    title: '忘记此设备',
    aliases: ['设备', '撤销'],
    webOnly: true
  },

  // ---- 关于：版本信息 ----
  {
    id: 'web-about-app-version',
    page: 'about',
    section: '关于 MCode',
    title: '版本号',
    aliases: ['版本', '版本号', 'version', '关于'],
    webOnly: true
  },
  {
    id: 'web-about-web-build',
    page: 'about',
    section: '关于 MCode',
    title: 'Web 端构建号',
    aliases: ['构建', '构建号', 'build', 'web'],
    webOnly: true
  },
  {
    id: 'web-about-schema-version',
    page: 'about',
    section: '关于 MCode',
    title: '协议版本',
    aliases: ['协议', '协议版本', 'schema', 'version'],
    webOnly: true
  }
];

export interface SettingsItemHit {
  item: SettingsItemDef;
  rank: number;
}

export function findSettingsPage(pageID: string): SettingsPageDef | undefined {
  return SETTINGS_PAGES.find((page) => page.id === pageID);
}

// 匹配一条设置项，rank 越小越靠前：标题前缀 0、标题包含 1、别名 2、小节/页名/页关键词 3。
function matchRank(item: SettingsItemDef, query: string): number | null {
  const title = item.title.toLowerCase();
  if (title.startsWith(query)) return 0;
  if (title.includes(query)) return 1;
  if ((item.aliases ?? []).some((alias) => alias.toLowerCase().includes(query))) return 2;
  const page = findSettingsPage(item.page);
  const rest: Array<string | undefined> = [item.section, page?.title, ...(page?.keywords ?? [])];
  if (rest.some((value) => typeof value === 'string' && value.toLowerCase().includes(query))) return 3;
  return null;
}

// matchSettingsItems 按桌面版 matchSettingsItems 的规则做不区分大小写的包含匹配，
// 同分按页顺序、再按登记表顺序排列（稳定排序）。
export function matchSettingsItems(
  query: string,
  items: readonly SettingsItemDef[] = SETTINGS_ITEMS
): SettingsItemHit[] {
  const q = query.trim().toLowerCase();
  if (!q) return [];
  const pageOrder = new Map(SETTINGS_PAGES.map((page, index) => [page.id, index]));
  const ranked: Array<{ hit: SettingsItemHit; order: number }> = [];
  items.forEach((item, order) => {
    const rank = matchRank(item, q);
    if (rank === null) return;
    ranked.push({ hit: { item, rank }, order });
  });
  ranked.sort((a, b) => {
    if (a.hit.rank !== b.hit.rank) return a.hit.rank - b.hit.rank;
    const pageA = pageOrder.get(a.hit.item.page) ?? Number.MAX_SAFE_INTEGER;
    const pageB = pageOrder.get(b.hit.item.page) ?? Number.MAX_SAFE_INTEGER;
    if (pageA !== pageB) return pageA - pageB;
    return a.order - b.order;
  });
  return ranked.map((entry) => entry.hit);
}
