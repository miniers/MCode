// Web 版设置索引：分组、页面与设置项的登记表，外加逐项搜索的纯函数。
// 分组与页面照抄桌面版 internal/ui/settings.go（settingsNavGroups / settingsPages），
// 每个设置项的 Title、Desc、Aliases 与 Advanced 照抄 internal/ui/settings_index.go。
// 这里只放纯数据与纯函数，不依赖 SettingsForm，保证设置表单仍然可以懒加载。

export type SettingsGroupID = 'ai' | 'ui' | 'data';

export interface SettingsGroupDef {
  id: SettingsGroupID;
  title: string;
}

// 分组标题与桌面版 settingsNavGroups 一致。
export const SETTINGS_GROUPS: SettingsGroupDef[] = [
  { id: 'ai', title: 'AI 与智能体' },
  { id: 'ui', title: '交互与界面' },
  { id: 'data', title: '连接与数据' }
];

// 导航图标：这里只存语义名，真实图标在 SettingsShell 里按名字映射到 Icons.tsx，
// 保证本文件仍然只有纯数据与纯函数（不引入 React 组件）。
// 对应桌面版 settingsPages() 里每页的 icon。
export type SettingsPageIcon =
  | 'message'
  | 'sparkles'
  | 'layout'
  | 'document'
  | 'settings'
  | 'sun'
  | 'archive'
  | 'desktop'
  | 'star';

export interface SettingsPageDef {
  id: string;
  title: string;
  group: SettingsGroupID;
  // 导航项左侧图标；缺省时不显示图标。
  icon?: SettingsPageIcon;
  // 搜索关键词照抄桌面版对应页的 keywords。
  keywords: string[];
}

// 只列 Web 上确实有内容的页；顺序与桌面版导航一致。
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
      '会话', '对话', '思考', '排队', '压缩', '限额', '完成'
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
      'escalate',
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
      '默默', '总管', '昵称', '名字', '值守', '待处理', '分诊', '跟进者', '总调度', '安静时段', '免打扰'
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
    id: 'general',
    title: '通用',
    group: 'ui',
    icon: 'settings',
    keywords: ['permissions', 'permission', 'mode', 'approval', '权限', '档位', '审批']
  },
  {
    id: 'notifications',
    title: '通知',
    group: 'ui',
    icon: 'message',
    keywords: [
      'notification', 'notifications', 'notify', 'sound', 'chime', 'alert', 'stalled',
      'finish', 'approval', 'attention', 'dormant', 'tray', 'quota',
      '通知', '提醒', '提示音', '响铃', '完成通知', '审批', '卡住', '休眠'
    ]
  },
  {
    id: 'appearance',
    title: '外观',
    group: 'ui',
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
    id: 'archive',
    title: '归档',
    group: 'data',
    icon: 'archive',
    keywords: ['archived', 'restore', '归档', '恢复']
  },
  {
    id: 'device',
    title: '此设备',
    group: 'data',
    icon: 'desktop',
    keywords: [
      'device', 'devices', 'pair', 'pairing', 'remote', 'entry', 'lan',
      '设备', '配对', '连接', '入口', '二维码', '重命名', '撤销'
    ]
  },
  {
    id: 'about',
    title: '关于',
    group: 'data',
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
  desc?: string;
  aliases?: string[];
  advanced?: boolean;
  // 对应宿主 settings.set 的键；宿主没给这个键时该项隐藏。
  settingsKey?: string;
  // Web 独有的设置项（桌面版索引里没有对应行）。
  webOnly?: boolean;
}

export const SETTINGS_ITEMS: SettingsItemDef[] = [
  // ---- 会话：回复与思考 ----
  {
    id: 'row-auto-collapse-reasoning',
    page: 'conversation',
    section: '回复与思考',
    title: '自动折叠思考过程',
    desc: '思考结束后自动收起，关闭则保持展开',
    aliases: ['折叠', '思考'],
    settingsKey: 'session.auto_collapse_reasoning'
  },
  {
    id: 'row-pin-sent-message',
    page: 'conversation',
    section: '回复与思考',
    title: '发送后置顶消息',
    desc: '发送后把这条消息滚到时间线顶部，回复在下方展开；关闭则始终跟随到底部',
    aliases: ['置顶', '锚定', '滚动'],
    settingsKey: 'session.pin_sent_message'
  },
  {
    id: 'row-concise-replies',
    page: 'conversation',
    section: '回复与思考',
    title: '简洁 AI 回复',
    desc: '内置提示词引导精简回复与代码注释',
    settingsKey: 'session.concise_replies_enabled'
  },
  // ---- 归档：自动休眠 ----
  {
    id: 'row-auto-dormant-days',
    page: 'archive',
    section: '自动休眠',
    title: '无消息多少天后自动休眠',
    desc: '默认 7 天，0 为不自动休眠',
    aliases: ['休眠', '归档'],
    settingsKey: 'archive.sidebar_auto_dormant_days'
  },

  // ---- 编排：总开关 ----
  {
    id: 'row-swarm-enabled',
    page: 'swarm',
    section: '总开关',
    title: '启用编排',
    aliases: ['子代理', 'swarm', '编排'],
    settingsKey: 'orchestration.enabled'
  },
  // ---- 编排：子代理 ----
  {
    id: 'row-swarm-workspace-mode',
    page: 'swarm',
    section: '子代理',
    title: '工作空间模式',
    aliases: ['子代理', '工作树'],
    settingsKey: 'orchestration.workspace_mode'
  },
  {
    id: 'row-swarm-max-bees',
    page: 'swarm',
    section: '子代理',
    title: '并发上限',
    aliases: ['子代理', '并发'],
    settingsKey: 'orchestration.max_bees'
  },
  {
    id: 'row-swarm-max-rounds',
    page: 'swarm',
    section: '子代理',
    title: '最多派发轮次',
    desc: '主代理最多派发几轮子代理，0 为不限',
    aliases: ['子代理'],
    settingsKey: 'orchestration.max_rounds'
  },
  {
    id: 'row-swarm-retry-failed',
    page: 'swarm',
    section: '子代理',
    title: '失败时自动重试',
    aliases: ['子代理', '重试'],
    settingsKey: 'orchestration.retry_failed'
  },
  {
    id: 'row-swarm-auto-clear-finished',
    page: 'swarm',
    section: '子代理',
    title: '自动清理已完成子代理',
    desc: '面板只留进行中的子代理',
    aliases: ['子代理'],
    settingsKey: 'orchestration.auto_clear_finished'
  },
  // ---- 编排：消息流向 ----
  {
    id: 'row-swarm-auto-delegate',
    page: 'swarm',
    section: '消息流向',
    title: '自动委托',
    desc: '需要拆分任务时由主代理自动派给子代理',
    aliases: ['委托', '分派'],
    settingsKey: 'orchestration.auto_delegate'
  },
  {
    id: 'row-swarm-lead-plans-only',
    page: 'swarm',
    section: '消息流向',
    title: '主代理只规划',
    aliases: ['主代理'],
    settingsKey: 'orchestration.lead_plans_only'
  },
  {
    id: 'row-swarm-send-to-bees',
    page: 'swarm',
    section: '消息流向',
    title: '消息直接交给子代理',
    aliases: ['子代理'],
    settingsKey: 'orchestration.always_bees'
  },
  // ---- 编排：子代理完成时 ----
  {
    id: 'row-swarm-review-bees',
    page: 'swarm',
    section: '子代理完成时',
    title: '合并前审查',
    desc: 'AI 审查或人工确认后再合入改动',
    aliases: ['审查', 'AI 审查'],
    settingsKey: 'orchestration.review_mode'
  },
  {
    id: 'row-swarm-auto-merge',
    page: 'swarm',
    section: '子代理完成时',
    title: '自动合并',
    desc: '默认关闭；全部完成后合并到当前分支并清理副本',
    aliases: ['合并'],
    settingsKey: 'orchestration.auto_merge'
  },
  {
    id: 'row-swarm-auto-push',
    page: 'swarm',
    section: '子代理完成时',
    title: '自动推送',
    desc: '默认关闭；开启后自动推送分支并创建 PR',
    aliases: ['推送'],
    settingsKey: 'orchestration.auto_push'
  },
  // ---- 编排：消息与提级 ----
  {
    id: 'block-swarm-tiers',
    page: 'swarm',
    section: '消息与提级',
    title: '子代理档位',
    desc: '按档位配置子代理的模型与写入白名单',
    aliases: ['档位', '只读模式', '白名单'],
    webOnly: true
  },
  {
    id: 'row-swarm-auto-forward-question',
    page: 'swarm',
    section: '消息与提级',
    title: '无人回答时转给主代理',
    desc: '超时后由主代理先答，拿不准再交给你',
    aliases: ['提级'],
    advanced: true,
    settingsKey: 'orchestration.auto_forward_question_seconds'
  },
  // ---- 总管：启用、昵称与头像 ----
  {
    id: 'row-steward-enabled',
    page: 'steward',
    section: '总管',
    title: '启用总管',
    aliases: ['总管', '默默'],
    settingsKey: 'steward.enabled'
  },
  {
    id: 'row-steward-cli-fallback',
    page: 'steward',
    section: '总管',
    title: '没有 API Key 时用本机 CLI',
    settingsKey: 'steward.cli_fallback'
  },
  {
    id: 'row-steward-name',
    page: 'steward',
    section: '总管',
    title: '昵称',
    desc: '留空时显示「总管」',
    aliases: ['总管', '默默', '昵称', '名字'],
    settingsKey: 'steward.name'
  },
  {
    id: 'row-steward-avatar',
    page: 'steward',
    section: '总管',
    title: '头像',
    desc: '填一个 emoji 或一个字，留空用名字的第一个字',
    aliases: ['总管'],
    settingsKey: 'steward.avatar'
  },
  // ---- 总管：安静时段 ----
  {
    id: 'web-steward-quiet-start',
    page: 'steward',
    section: '安静时段',
    title: '安静时段开始',
    desc: '本地时间 HH:MM，留空表示不设开始时刻',
    aliases: ['总管', '免打扰'],
    settingsKey: 'steward.quiet_start',
    webOnly: true
  },
  {
    id: 'web-steward-quiet-end',
    page: 'steward',
    section: '安静时段',
    title: '安静时段结束',
    desc: '本地时间 HH:MM，可以跨午夜；留空表示不设结束时刻',
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
    desc: '会话卡住、出错、额度用尽、等待审批，以及看板与自动化失败的汇总在这里逐条处理。',
    aliases: ['待处理', '介入'],
    webOnly: true
  },

  // ---- 通用：权限 ----
  {
    id: 'row-mode',
    page: 'general',
    section: '权限',
    title: '新会话默认权限档位',
    aliases: ['权限', '只读', '审批'],
    settingsKey: 'permissions.mode'
  },
  {
    id: 'block-always-allowed',
    page: 'general',
    section: '权限',
    title: '始终允许的操作',
    desc: '此前在审批时被选过「始终允许」的操作，撤销后同一条操作会重新弹出确认。',
    aliases: ['权限', '允许'],
    webOnly: true
  },

  {
    id: 'row-tray-quota-hidden',
    page: 'general',
    section: '启动与托盘',
    title: '菜单栏额度面板中隐藏',
    desc: '勾选的直连供应商不在 Mac 菜单栏的额度面板中显示，取消勾选即恢复显示',
    aliases: ['托盘', '额度', '菜单栏', '隐藏'],
    settingsKey: 'startup.tray_quota_hidden'
  },

  // ---- 通知：通知与提示音（都在 Mac 上生效） ----
  {
    id: 'row-notify-finished',
    page: 'notifications',
    section: '通知与提示音',
    title: '完成后通知',
    desc: '会话轮次结束时发送系统通知',
    aliases: ['通知', '完成'],
    settingsKey: 'notifications.notify_when_finished'
  },
  {
    id: 'row-notify-foreground',
    page: 'notifications',
    section: '通知与提示音',
    title: '前台也发送通知',
    desc: '应用在前台时也发系统通知',
    aliases: ['通知', '前台'],
    settingsKey: 'notifications.notify_when_foreground'
  },
  {
    id: 'row-notify-approval',
    page: 'notifications',
    section: '通知与提示音',
    title: '待审批时通知',
    desc: '有新的审批请求时发送系统通知',
    aliases: ['通知', '审批'],
    settingsKey: 'notifications.notify_on_approval'
  },
  {
    id: 'row-finish-sound',
    page: 'notifications',
    section: '通知与提示音',
    title: '完成提示音',
    desc: '会话轮次结束时播放轻提示音',
    aliases: ['提示音', '声音', '完成'],
    settingsKey: 'notifications.play_finish_sound'
  },
  {
    id: 'row-chime-attention',
    page: 'notifications',
    section: '通知与提示音',
    title: '需要介入时提示音',
    desc: 'Agent 提问或等待用户决策时播放专属提示音',
    aliases: ['提示音', '声音', '介入', '提问'],
    settingsKey: 'notifications.chime_when_attention_needed'
  },
  {
    id: 'row-dormant-sound',
    page: 'notifications',
    section: '通知与提示音',
    title: '会话休眠提示音',
    desc: '会话休眠时播放轻提示音',
    aliases: ['提示音', '声音', '休眠'],
    settingsKey: 'notifications.dormant_sound'
  },
  {
    id: 'row-notify-stalled',
    page: 'notifications',
    section: '通知与提示音',
    title: '会话卡住时提醒',
    desc: '会话长时间没有进展时发送系统通知',
    aliases: ['通知', '卡住', '停滞'],
    settingsKey: 'notifications.notify_when_stalled'
  },
  {
    id: 'row-stalled-minutes',
    page: 'notifications',
    section: '通知与提示音',
    title: '无进展多久算卡住',
    desc: '默认 10 分钟；多少分钟没输出就提醒你，0 表示用默认值',
    aliases: ['卡住', '分钟', '阈值'],
    settingsKey: 'notifications.stalled_alert_minutes'
  },

  // ---- 归档：会话维护 ----
  {
    id: 'row-confirm-delete',
    page: 'archive',
    section: '会话维护',
    title: '删除会话前确认',
    desc: '删除会话前弹窗确认一次',
    aliases: ['删除', '确认'],
    settingsKey: 'archive.confirm_before_deleting'
  },

  // ---- 外观：主题与字体 ----
  {
    id: 'row-theme',
    page: 'appearance',
    section: '主题与字体',
    title: '主题',
    desc: '跟随系统、浅色或深色',
    aliases: ['深色', '浅色']
  },
  {
    id: 'row-web-haptics',
    page: 'appearance',
    section: '反馈',
    title: '触感反馈',
    desc: '左滑、长按与审批提交时轻微振动；仅 Android 支持',
    aliases: ['振动', '触感', 'haptic'],
    webOnly: true
  },

  // ---- 记忆：记忆提议入口（Web 独有） ----
  {
    id: 'web-memory-proposals',
    page: 'nowledge',
    section: '记忆',
    title: '记忆提议',
    desc: '待审的会话记忆先落本地收件箱，接受后写入 Nowledge Mem，驳回只改本地状态；也可搜索记忆或手动新建一条待审记忆。',
    aliases: ['记忆', '提案', 'Nowledge'],
    webOnly: true
  },

  // ---- 此设备：连接 ----
  {
    id: 'web-connection-refresh',
    page: 'device',
    section: '连接',
    title: '刷新会话与看板',
    desc: '重新拉取 Mac 上的会话、看板与项目列表',
    aliases: ['刷新', '连接'],
    webOnly: true
  },
  {
    id: 'web-connection-entry',
    page: 'device',
    section: '连接',
    title: '当前入口',
    desc: '显示当前使用的 HTTPS 或局域网 HTTP 入口，以及主屏幕安装说明',
    aliases: ['入口', '连接', 'https'],
    webOnly: true
  },
  {
    id: 'web-home-pairing',
    page: 'device',
    section: '连接',
    title: '主屏幕配对码',
    desc: '为主屏幕应用生成一次 64 位配对码',
    aliases: ['配对', '主屏幕'],
    webOnly: true
  },
  // ---- 此设备：主屏幕与通知（Web 独有，常驻入口） ----
  {
    id: 'row-web-install',
    page: 'device',
    section: '主屏幕与通知',
    title: '添加到主屏幕',
    desc: '安装到主屏幕，像应用一样打开',
    aliases: ['安装', '主屏幕', 'PWA', 'install'],
    webOnly: true
  },
  {
    id: 'row-web-push',
    page: 'device',
    section: '主屏幕与通知',
    title: '推送通知',
    desc: '会话等待你处理或运行结束时在这台设备上收到通知',
    aliases: ['通知', '推送', 'push', 'notification'],
    webOnly: true
  },
  // ---- 此设备：此设备 ----
  {
    id: 'web-device-name',
    page: 'device',
    section: '此设备',
    title: '设备名称',
    desc: '修改这台设备在 Mac 上显示的名字',
    aliases: ['设备', '改名'],
    webOnly: true
  },
  {
    id: 'web-device-remember',
    page: 'device',
    section: '此设备',
    title: '记住此设备',
    desc: '关闭页面后保留配对信息',
    aliases: ['设备', '配对'],
    webOnly: true
  },
  {
    id: 'web-device-forget',
    page: 'device',
    section: '此设备',
    title: '忘记此设备',
    desc: '撤销 Mac 上的此设备并清除本地配对信息',
    aliases: ['设备', '撤销'],
    webOnly: true
  },

  // ---- 关于：版本信息 ----
  {
    id: 'web-about-app-version',
    page: 'about',
    section: '关于 MCode',
    title: '版本号',
    desc: 'Mac 端 mcode 的当前构建版本；宿主版本过旧时显示「未知（宿主版本过旧）」',
    aliases: ['版本', '版本号', 'version', '关于'],
    webOnly: true
  },
  {
    id: 'web-about-web-build',
    page: 'about',
    section: '关于 MCode',
    title: 'Web 端构建号',
    desc: '当前页面资源对应的构建标识',
    aliases: ['构建', '构建号', 'build', 'web'],
    webOnly: true
  },
  {
    id: 'web-about-schema-version',
    page: 'about',
    section: '关于 MCode',
    title: '协议版本',
    desc: '远程控制协议的 schemaVersion，两端不一致时需要一起更新',
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

// 匹配一条设置项，rank 越小越靠前：标题前缀 0、标题包含 1、别名 2、说明/小节/页名/页关键词 3。
function matchRank(item: SettingsItemDef, query: string): number | null {
  const title = item.title.toLowerCase();
  if (title.startsWith(query)) return 0;
  if (title.includes(query)) return 1;
  if ((item.aliases ?? []).some((alias) => alias.toLowerCase().includes(query))) return 2;
  const page = findSettingsPage(item.page);
  const rest: Array<string | undefined> = [item.desc, item.section, page?.title, ...(page?.keywords ?? [])];
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
