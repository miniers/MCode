// 待办清单的解析与进度统计：口径对齐桌面端 internal/ui/timeline_rows.go:3371 parseTodoItems
// 与 internal/ui/composer_plan_pill.go:52 latestTodoPlan —— 待办来自 TodoWrite / update_plan 这类
// 工具消息的输入 JSON（含 todos 或 plan 数组，每项有 content/step 与 status）。
// 解析只在这里做一份，时间线的待办摘要与输入框上方的进度胶囊共用。

export type TodoStatus = 'pending' | 'in_progress' | 'completed';

export interface TodoItem {
  text: string;
  status: TodoStatus;
}

/** 一份待办清单的进度：条目、完成数、总数与「进行中那一项」。 */
export interface TodoProgress {
  items: TodoItem[];
  done: number;
  total: number;
  /** 进行中那一项；没有进行中项时取第一项未完成的；全部完成时为空 */
  current: string;
}

/** 解析待办只需要这几个字段，不依赖时间线的消息类型（避免 utils 反向依赖组件）。 */
export interface TodoSourceMessage {
  role?: string;
  kind?: string;
  text?: string;
  toolInput?: string;
}

/** 待办项正文的候选字段名；plan 类工具用 step，todos 类用 content。 */
const TODO_TEXT_KEYS = ['content', 'step', 'text', 'task', 'title'] as const;

/** 与桌面端 todoInputLooksLikePlan 同口径的廉价预判：先挡掉绝大多数普通工具消息。 */
export function looksLikeTodoInput(raw: string | undefined | null): boolean {
  if (!raw) return false;
  if (!raw.includes('"status"')) return false;
  return raw.includes('"todos"') || raw.includes('"plan"');
}

function normalizeTodoStatus(status: unknown): TodoStatus {
  if (typeof status !== 'string') return 'pending';
  const key = status.trim().toLowerCase().replace(/[- ]/g, '_');
  if (key === 'completed' || key === 'complete' || key === 'done' || key === 'finished') return 'completed';
  if (key === 'in_progress' || key === 'inprogress' || key === 'active' || key === 'running' || key === 'doing') {
    return 'in_progress';
  }
  return 'pending';
}

function firstTodoText(entry: Record<string, unknown>): string {
  for (const key of TODO_TEXT_KEYS) {
    const value = entry[key];
    if (typeof value === 'string' && value.trim() !== '') return value;
  }
  return '';
}

/**
 * 从一段文本里解析待办清单：取第一个 `{` 起的 JSON（与桌面端用 json.Decoder 读子串一致），
 * 先看 todos 数组，再看 plan 数组；不是待办工具时返回 null。
 */
export function parseTodoItems(raw: string | undefined | null): TodoItem[] | null {
  if (!raw) return null;
  const start = raw.indexOf('{');
  if (start < 0) return null;
  const body = raw.slice(start);
  let payload: unknown = null;
  try {
    payload = JSON.parse(body);
  } catch {
    // 正文里常带着工具名前缀或截断标记：退一步只取到最后一个 `}`，再试一次
    const end = body.lastIndexOf('}');
    if (end <= 0) return null;
    try {
      payload = JSON.parse(body.slice(0, end + 1));
    } catch {
      return null;
    }
  }
  if (!payload || typeof payload !== 'object') return null;
  const record = payload as { todos?: unknown; plan?: unknown };
  const list = Array.isArray(record.todos) && record.todos.length > 0
    ? record.todos
    : Array.isArray(record.plan)
    ? record.plan
    : null;
  if (!list) return null;

  const items: TodoItem[] = [];
  for (const entry of list) {
    if (!entry || typeof entry !== 'object') continue;
    const text = firstTodoText(entry as Record<string, unknown>);
    if (text.trim() === '') continue;
    items.push({ text, status: normalizeTodoStatus((entry as Record<string, unknown>).status) });
  }
  return items.length > 0 ? items : null;
}

/**
 * 从一条消息里解析待办清单：优先结构化输入 ToolInput，拿不到（旧消息或输入被截断）时回退正文。
 * 只有工具消息才会带待办清单。
 */
export function todoItemsFromMessage(message: TodoSourceMessage | null | undefined): TodoItem[] | null {
  if (!message) return null;
  if (message.role !== 'tool' && message.kind !== 'tool') return null;
  const input = message.toolInput ?? '';
  if (looksLikeTodoInput(input)) {
    const items = parseTodoItems(input);
    if (items) return items;
  }
  return parseTodoItems(message.text ?? '');
}

/** 统计完成数与总数。 */
export function todoProgressOf(items: readonly TodoItem[]): TodoProgress {
  let done = 0;
  for (const item of items) {
    if (item.status === 'completed') done += 1;
  }
  return { items: [...items], done, total: items.length, current: todoCurrentText(items) };
}

/** 进行中那一项的正文；没有进行中项时取第一项未完成的；全部完成时为空。 */
export function todoCurrentText(items: readonly TodoItem[]): string {
  for (const item of items) {
    if (item.status === 'in_progress') return item.text;
  }
  for (const item of items) {
    if (item.status !== 'completed') return item.text;
  }
  return '';
}

/** 一条消息的待办进度；不是待办消息时返回 null。 */
export function todoProgressFromMessage(message: TodoSourceMessage | null | undefined): TodoProgress | null {
  const items = todoItemsFromMessage(message);
  return items ? todoProgressOf(items) : null;
}

function lastUserMessageIndex(messages: readonly TodoSourceMessage[]): number {
  for (let i = messages.length - 1; i >= 0; i -= 1) {
    if (messages[i]?.role === 'user') return i;
  }
  return -1;
}

/** 最新一份待办清单的进度及其消息下标；没有待办消息时返回 null。后到的待办整份覆盖先前的。 */
export function latestTodoProgress(
  messages: readonly TodoSourceMessage[]
): { progress: TodoProgress; index: number } | null {
  for (let i = messages.length - 1; i >= 0; i -= 1) {
    const progress = todoProgressFromMessage(messages[i]);
    if (progress) return { progress, index: i };
  }
  return null;
}

/**
 * 输入框上方进度胶囊要展示的内容，与桌面端 shouldShowPlanPill 同口径：
 * 有待办、未全部完成，而且会话正在运行，或最新待办出现在最后一条用户消息之后（这一轮的新进度）。
 * 全部完成、没有待办、或结束后仍是上一轮的旧清单时返回 null（不显示）。
 */
export function shouldShowTodoProgress(
  messages: readonly TodoSourceMessage[],
  isRunning: boolean
): TodoProgress | null {
  const latest = latestTodoProgress(messages);
  if (!latest) return null;
  if (latest.progress.total <= 0 || latest.progress.done >= latest.progress.total) return null;
  if (isRunning) return latest.progress;
  return latest.index > lastUserMessageIndex(messages) ? latest.progress : null;
}

/** 胶囊正文：「完成数/总数 · 进行中那一项」（正文按 maxRunes 截断）。 */
export function todoProgressText(progress: TodoProgress, maxRunes = 60): string {
  const head = `${progress.done}/${progress.total}`;
  const current = [...progress.current].slice(0, maxRunes).join('').replace(/\s+/g, ' ').trim();
  return current ? `${head} · ${current}` : head;
}
