// 会话导出（Web 端）：把会话元数据与消息渲染成 Markdown、HTML 或 JSON。
// 结构、字段与预算降级规则照搬桌面端 internal/ui/thread_export.go：
// 正文超过预算（默认 150k 字符）时先截断工具输出，再整条丢弃终端输出，
// 然后把工具调用折叠成计数，最后把思考压成头尾；较早回合先降一级，
// 仍然超预算才硬截断。所有渲染函数都是纯函数，不碰 DOM 与网络。

import type { RemoteMediaRef, RemoteThreadMessage, RemoteThreadSummary } from '../types/contract';
import { isHiddenTimelineMessage, type ThreadMessage } from '../components/ThreadTimeline';

/** 与桌面端 threadExportBudgetChars 一致：Markdown / HTML / 剪贴板导出的字符上限。 */
export const THREAD_EXPORT_BUDGET_CHARS = 150_000;
/** 不额外降级的最近回合数，更老的回合先降一级。 */
const THREAD_EXPORT_RECENT_TURNS = 4;
/** 降级等级 1 时工具 / 推理正文的最大字符数。 */
const THREAD_EXPORT_TOOL_TEXT_CAP = 4000;
/** 降级等级 4 时思考正文头尾各保留的字符数。 */
const THREAD_EXPORT_THINKING_KEEP = 800;
/** 降级等级上限：0 原样、1 截断工具输出、2 丢弃终端输出、3 工具调用折叠成计数、4 思考只保留头尾。 */
const THREAD_EXPORT_LEVEL_MAX = 4;
/** 超预算导出时追加在元信息后的降级说明行。 */
const THREAD_EXPORT_DEGRADE_NOTE =
  '> 本会话过长，导出已自动降级：工具输出截断 → 丢弃终端输出 → 工具调用折叠成计数 → 思考只保留头尾，较早回合先降级。\n';

export type ThreadExportFormat = 'md' | 'html' | 'json';

export interface ThreadExportRenderResult {
  text: string;
  /** 是否发生了预算降级（用于提示用户内容已被裁剪） */
  degraded: boolean;
}

export interface ThreadExportFile extends ThreadExportRenderResult {
  fileName: string;
  mimeType: string;
}

/** 导出用的消息归一化结果：字段名与桌面端 threadExportMessage 对齐，正文已排除隐藏消息。 */
interface NormalizedMessage {
  id: string;
  role: string;
  kind: string;
  text: string;
  turnIndex: number;
  turnID: string;
  isFinal: boolean;
  toolName: string;
  media: RemoteMediaRef[];
}

const runeCount = (text: string): number => Array.from(text).length;

const runeHead = (text: string, keep: number): string => Array.from(text).slice(0, keep).join('');

const runeTail = (text: string, keep: number): string => {
  const all = Array.from(text);
  return all.slice(Math.max(0, all.length - keep)).join('');
};

/**
 * 把会话标题清洗成文件名：去掉 / \ : * ? " < > | 与换行等非法字符、去掉首尾空白，
 * 最长 80 个字符，清洗后为空时回退为 "mcode-会话"，最后拼上扩展名。
 * 规则与桌面端 threadExportFileName 逐字符一致（桌面端文件名不含日期）。
 */
export function threadExportFileName(title: string, ext: `.${ThreadExportFormat}`): string {
  let name = '';
  for (const ch of title) {
    if (ch === '/' || ch === '\\' || ch === ':' || ch === '*' || ch === '?' || ch === '"') continue;
    if (ch === '<' || ch === '>' || ch === '|' || ch === '\n' || ch === '\r') continue;
    name += ch;
  }
  name = name.trim();
  if (runeCount(name) > 80) name = runeHead(name, 80);
  name = name.trim();
  if (!name) name = 'mcode-会话';
  return name + ext;
}

const pad2 = (value: number): string => String(value).padStart(2, '0');

/** 与桌面端 threadExportTime 一致的本地时间格式 2006-01-02 15:04:05。 */
export function formatThreadExportTime(date: Date): string {
  if (Number.isNaN(date.getTime())) return '';
  return (
    `${date.getFullYear()}-${pad2(date.getMonth() + 1)}-${pad2(date.getDate())}` +
    ` ${pad2(date.getHours())}:${pad2(date.getMinutes())}:${pad2(date.getSeconds())}`
  );
}

/** RFC3339 秒级时间（秒以下清零，舍去 .000 毫秒），例如 2026-11-01T02:00:00Z。 */
function formatRFC3339(date: Date): string {
  if (Number.isNaN(date.getTime())) return '';
  const iso = new Date(date.getTime() - date.getMilliseconds()).toISOString();
  return iso.replace(/\.000Z$/, 'Z');
}

const parseThreadDate = (raw: string | null | undefined): Date => {
  if (!raw) return new Date(Number.NaN);
  const parsed = new Date(raw);
  return parsed;
};

const roleHeading = (role: string): string => {
  switch (role) {
    case 'user':
      return '## 用户';
    case 'assistant':
      return '## 助手';
    default:
      return `## ${role}`;
  }
};

/** 工具类或推理类消息在导出时折叠。 */
const shouldCollapse = (m: NormalizedMessage): boolean =>
  m.kind === 'tool' || m.kind === 'reasoning' || m.role === 'tool' || m.role === 'reasoning';

const isReasoning = (m: NormalizedMessage): boolean => m.kind === 'reasoning' || m.role === 'reasoning';

/** 终端命令输出体量最大、可读价值最低，降级等级 2 时整条丢弃。 */
const isTerminalOutput = (m: NormalizedMessage): boolean => {
  if (!shouldCollapse(m)) return false;
  const toolName = m.toolName.trim().toLowerCase();
  if (
    toolName === 'bash' ||
    toolName === 'shell' ||
    toolName === 'sh' ||
    toolName === 'zsh' ||
    toolName === 'terminal' ||
    toolName === 'exec' ||
    toolName === 'command' ||
    toolName === 'run_command'
  ) {
    return true;
  }
  return m.kind.toLowerCase() === 'terminal' || m.role.toLowerCase() === 'terminal';
};

/** 包裹正文所需的反引号围栏：最长连续反引号数 +1，至少 3 个。 */
const fenceFor = (text: string): string => {
  let longest = 0;
  let run = 0;
  for (const ch of text) {
    if (ch === '`') {
      run += 1;
      if (run > longest) longest = run;
    } else {
      run = 0;
    }
  }
  return '`'.repeat(Math.max(3, longest + 1));
};

const detailsBlock = (text: string): string => {
  const fence = fenceFor(text);
  const body = text.endsWith('\n') ? text : `${text}\n`;
  return `<details><summary>工具/推理</summary>\n\n${fence}\n${body}${fence}\n\n</details>`;
};

const mediaLabel = (media: RemoteMediaRef): string => {
  if (media.name) return media.name;
  if (media.mimeType) return media.mimeType;
  return '未命名附件';
};

const messageSection = (m: NormalizedMessage): string => {
  const parts: string[] = [roleHeading(m.role)];
  if (m.text) parts.push(shouldCollapse(m) ? detailsBlock(m.text) : m.text);
  if (m.media.length > 0) parts.push(m.media.map((item) => `- 附件：${mediaLabel(item)}`).join('\n'));
  return parts.join('\n\n');
};

const truncateText = (text: string, limit: number): string => {
  if (limit <= 0) return text;
  if (runeCount(text) <= limit) return text;
  return `${runeHead(text, limit)}\n…（已截断）`;
};

const keepHeadAndTail = (text: string, keep: number): string => {
  if (keep <= 0) return text;
  if (runeCount(text) <= 2 * keep) return text;
  return `${runeHead(text, keep)}\n…（已省略中间内容）…\n${runeTail(text, keep)}`;
};

/**
 * 把 Web 消息归一化成导出结构：剔除隐藏消息（总管唤醒输入与静默回复）、
 * 补齐缺失字段，正文与附件保持原样。
 */
const normalizeMessages = (messages: RemoteThreadMessage[]): NormalizedMessage[] =>
  messages
    .filter((message) => !isHiddenTimelineMessage(message as ThreadMessage))
    .map((message) => ({
      id: message.id,
      role: message.role,
      kind: message.kind ?? '',
      text: message.text ?? '',
      turnIndex: message.turnIndex,
      turnID: message.turnID ?? '',
      isFinal: message.isFinalResponse ?? false,
      toolName: message.toolName ?? '',
      media: Array.isArray(message.media) ? message.media : []
    }));

const isQuestionAnswer = (m: NormalizedMessage): boolean => m.kind === 'question_answer';

const isEmptyMessage = (m: NormalizedMessage): boolean => m.text === '' && m.media.length === 0;

/** 导出开头的标题与元信息，完整导出与预算版导出共用。 */
const header = (thread: RemoteThreadSummary, exportedAt: Date): string => {
  const meta = [
    `- 会话 ID：${thread.id}`,
    `- Provider：${thread.provider}`,
    `- Model：${thread.model ?? ''}`,
    `- 更新时间：${formatThreadExportTime(parseThreadDate(thread.updatedAt))}`,
    `- 导出时间：${formatThreadExportTime(exportedAt)}`
  ];
  return `# ${thread.title}\n\n${meta.join('\n')}\n`;
};

/** 不做预算降级的完整 Markdown（正文为空且没有附件的消息、问答记录消息会被跳过）。 */
function buildThreadMarkdownFull(
  thread: RemoteThreadSummary,
  messages: RemoteThreadMessage[],
  exportedAt: Date
): { text: string; messages: NormalizedMessage[] } {
  const normalized = normalizeMessages(messages);
  let text = header(thread, exportedAt);
  for (const message of normalized) {
    if (isEmptyMessage(message)) continue;
    // 问答记录消息的正文是前端用的 JSON，Markdown 里没有可读内容，完整记录仍保留在 JSON 导出里。
    if (isQuestionAnswer(message)) continue;
    text += `\n${messageSection(message)}\n`;
  }
  return { text, messages: normalized };
}

/** 两条消息是否属于同一回合；缺少回合信息时按消息各自成回合。 */
const sameTurn = (a: NormalizedMessage, b: NormalizedMessage): boolean => {
  if (a.turnID || b.turnID) return a.turnID === b.turnID;
  return a.turnIndex === b.turnIndex;
};

/** 按降级等级渲染单条消息小节；返回空串表示该条在该等级下被整条丢弃。 */
const messageSectionAt = (m: NormalizedMessage, level: number): string => {
  if (level <= 0 || !shouldCollapse(m) || m.text === '') return messageSection(m);
  // 工具调用塌缩成计数：由调用方统计连续条数并写一行汇总，这里不单独输出小节。
  if (level >= 3 && !isReasoning(m)) return '';
  if (level >= 2 && isTerminalOutput(m)) return '';
  const text =
    level >= THREAD_EXPORT_LEVEL_MAX && isReasoning(m)
      ? keepHeadAndTail(m.text, THREAD_EXPORT_THINKING_KEEP)
      : truncateText(m.text, THREAD_EXPORT_TOOL_TEXT_CAP);
  return messageSection({ ...m, text });
};

/**
 * 按长度预算给出每条消息的降级等级（budget <= 0 表示不降级）：
 * 先按回合统计正文长度选择全局等级，再让最近 4 个回合用全局等级、更老的回合高一级，
 * 预算紧张时总是老回合先降级。
 */
const threadExportLevels = (messages: NormalizedMessage[], budget: number): number[] => {
  const levels = new Array<number>(messages.length).fill(0);
  if (budget <= 0 || messages.length === 0) return levels;

  const turnOf = new Array<number>(messages.length).fill(0);
  let turn = 0;
  for (let i = 0; i < messages.length; i += 1) {
    if (i > 0 && !sameTurn(messages[i - 1], messages[i])) turn += 1;
    turnOf[i] = turn;
  }
  const recentFrom = Math.max(0, turn + 1 - THREAD_EXPORT_RECENT_TURNS);

  let plain = 0;
  for (const message of messages) plain += runeCount(messageSectionAt(message, 0));
  // 预算充足：全部不降级，输出与完整导出逐字符一致。
  if (plain <= budget) return levels;

  const total = (global: number): number => {
    let sum = 0;
    for (let i = 0; i < messages.length; i += 1) {
      const level = turnOf[i] < recentFrom ? Math.min(global + 1, THREAD_EXPORT_LEVEL_MAX) : global;
      sum += runeCount(messageSectionAt(messages[i], level));
    }
    return sum;
  };

  let global = 0;
  while (global < THREAD_EXPORT_LEVEL_MAX && total(global) > budget) global += 1;
  for (let i = 0; i < levels.length; i += 1) {
    levels[i] = turnOf[i] < recentFrom ? Math.min(global + 1, THREAD_EXPORT_LEVEL_MAX) : global;
  }
  return levels;
}

/**
 * 按长度预算渲染 Markdown：没超预算时与完整导出一致，
 * 超预算时逐条降级、追加降级说明并统计被折叠的工具调用条数，
 * 仍然超预算就在末尾硬截断（截断标记也算在预算里）。
 * 用户与助手的正文永远保留原文。
 */
export function buildThreadMarkdown(
  thread: RemoteThreadSummary,
  messages: RemoteThreadMessage[],
  exportedAt: Date,
  budget: number = THREAD_EXPORT_BUDGET_CHARS
): ThreadExportRenderResult {
  const full = buildThreadMarkdownFull(thread, messages, exportedAt);
  if (budget <= 0 || runeCount(full.text) <= budget) {
    return { text: full.text, degraded: false };
  }

  const normalized = full.messages;
  const levels = threadExportLevels(normalized, budget);
  let out = header(thread, exportedAt) + THREAD_EXPORT_DEGRADE_NOTE;

  let omitted = 0;
  const flushOmitted = (): void => {
    if (omitted === 0) return;
    out += `\n> 已省略 ${omitted} 条工具调用记录\n`;
    omitted = 0;
  };
  for (let i = 0; i < normalized.length; i += 1) {
    const message = normalized[i];
    if (isEmptyMessage(message)) continue;
    if (isQuestionAnswer(message)) continue;
    if (levels[i] >= 3 && shouldCollapse(message) && !isReasoning(message)) {
      omitted += 1;
      continue;
    }
    const section = messageSectionAt(message, levels[i]);
    if (section === '') continue;
    flushOmitted();
    out += `\n${section}\n`;
  }
  flushOmitted();

  if (runeCount(out) > budget) {
    // 极端情况下（正文本身就超预算）只能硬截断；预算太小时干脆不留标记。
    let marker = '\n…（已按长度预算截断）\n';
    let keep = budget - runeCount(marker);
    if (keep < 0) {
      keep = 0;
      marker = '';
    }
    out = runeHead(out, keep) + marker;
  }
  return { text: out, degraded: true };
}

const escapeHTML = (text: string): string =>
  text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&#34;')
    .replace(/'/g, '&#39;');

/**
 * 最小 HTML 文档：正文沿用受预算约束的 Markdown 文本，整体做 HTML 转义后放进 <pre>，
 * 没有外链样式与脚本，浏览器打开即可阅读（与桌面端 buildThreadExportHTML 一致）。
 */
export function buildThreadHTML(
  thread: RemoteThreadSummary,
  messages: RemoteThreadMessage[],
  exportedAt: Date,
  budget: number = THREAD_EXPORT_BUDGET_CHARS
): ThreadExportRenderResult {
  const { text, degraded } = buildThreadMarkdown(thread, messages, exportedAt, budget);
  const doc =
    '<!DOCTYPE html>\n<html lang="zh-CN">\n<head>\n<meta charset="utf-8">\n<title>' +
    `${escapeHTML(thread.title)}</title>\n</head>\n<body>\n<pre>` +
    `${escapeHTML(text)}</pre>\n</body>\n</html>\n`;
  return { text: doc, degraded };
}

/**
 * JSON 导出：两空格缩进，version 1，时间用 RFC3339。
 * 与桌面端一致，JSON 保留全部原始消息（含工具输出与问答记录），不做预算降级。
 */
export function buildThreadJSON(
  thread: RemoteThreadSummary,
  messages: RemoteThreadMessage[],
  exportedAt: Date
): string {
  const doc = {
    version: 1,
    exportedAt: formatRFC3339(exportedAt),
    thread: {
      id: thread.id,
      projectId: thread.projectID,
      title: thread.title,
      provider: thread.provider,
      model: thread.model ?? '',
      updatedAt: formatRFC3339(parseThreadDate(thread.updatedAt))
    },
    messages: messages.map((message) => ({
      id: message.id,
      role: message.role,
      kind: message.kind ?? '',
      text: message.text ?? '',
      turnIndex: message.turnIndex,
      turnId: message.turnID ?? '',
      isFinal: message.isFinalResponse ?? false,
      media: (Array.isArray(message.media) ? message.media : []).map((item) => ({
        id: item.id,
        name: item.name,
        mimeType: item.mimeType,
        width: item.width ?? 0,
        height: item.height ?? 0
      }))
    }))
  };
  return JSON.stringify(doc, null, 2);
}

const MIME_TYPES: Record<ThreadExportFormat, string> = {
  md: 'text/markdown;charset=utf-8',
  html: 'text/html;charset=utf-8',
  json: 'application/json;charset=utf-8'
};

/** 按格式渲染导出文件：文件名、MIME 与是否降级一次给全，供菜单直接下载或复制。 */
export function buildThreadExport(
  thread: RemoteThreadSummary,
  messages: RemoteThreadMessage[],
  format: ThreadExportFormat,
  exportedAt: Date = new Date()
): ThreadExportFile {
  const ext: `.${ThreadExportFormat}` = `.${format}`;
  const fileName = threadExportFileName(thread.title, ext);
  const mimeType = MIME_TYPES[format];
  switch (format) {
    case 'html': {
      const { text, degraded } = buildThreadHTML(thread, messages, exportedAt);
      return { fileName, mimeType, text, degraded };
    }
    case 'json':
      return { fileName, mimeType, text: buildThreadJSON(thread, messages, exportedAt), degraded: false };
    default: {
      const { text, degraded } = buildThreadMarkdown(thread, messages, exportedAt);
      return { fileName, mimeType, text, degraded };
    }
  }
}
