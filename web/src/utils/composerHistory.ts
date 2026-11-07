// 输入框方向键历史导航的纯函数移植，规则与桌面端 internal/ui/composer_history.go 一致：
// 历史条目来自会话内 role 为 user 的消息文本，过滤空文本与编排通知，相邻重复去重；
// index 为 0 表示未在导航（零值即可用），1..n 表示正在浏览「从最新往旧数」第 index 条；
// 进入导航前把当时的草稿存进 stash，翻过最新一条或按 Esc 时恢复它。

/**
 * 编排通知前缀，与 internal/ui/swarm.go:1995 的 swarmNoticePrefix、legacySwarmNoticePrefix 一致。
 * 子代理汇报以 user 消息形式落入会话，但不是用户输入，不应进入方向键历史。
 */
const SWARM_NOTICE_PREFIXES = ['[编排] ', '[Hydra] '];

/** 历史导航只关心角色与文本，RemoteThreadMessage 可直接传入。 */
export interface ComposerHistoryMessage {
  role?: string;
  text?: string;
}

/** 方向键历史导航状态：index 为 0 表示未在导航。 */
export interface ComposerHistoryState {
  index: number;
  /** 进入导航前的草稿，翻过最新一条或 Esc 退出时恢复 */
  stash: string;
  /** 最近一次回填的文本，用于识别用户改动并退出导航 */
  recalled: string;
}

/** 未在导航时的状态（不可变，可直接复用）。 */
export const emptyComposerHistoryState: ComposerHistoryState = { index: 0, stash: '', recalled: '' };

/** isSwarmNoticeText 判断文本是否为编排通知（含子代理汇报）。 */
export function isSwarmNoticeText(text: string): boolean {
  const trimmed = text.trim();
  return SWARM_NOTICE_PREFIXES.some((prefix) => trimmed.startsWith(prefix.trim()));
}

/**
 * composerHistoryEntries 按时间顺序收集会话内用户消息的文本，供方向键历史导航使用。
 * 对应 internal/ui/composer_history.go:15 的 composerHistoryEntries。
 */
export function composerHistoryEntries(messages: ReadonlyArray<ComposerHistoryMessage>): string[] {
  const entries: string[] = [];
  for (const message of messages) {
    if (message.role !== 'user') continue;
    const text = String(message.text ?? '').trim();
    if (text === '') continue;
    if (isSwarmNoticeText(text)) continue;
    if (entries.length > 0 && entries[entries.length - 1] === text) continue;
    entries.push(text);
  }
  return entries;
}

/**
 * caretOnFirstLine 判断光标之前是否没有换行符，即光标是否位于首行。
 * caret 用 textarea 的 selectionStart（UTF-16 偏移）；text 为原文，对应 internal/ui/composer_history.go:56。
 */
export function caretOnFirstLine(text: string, caret: number): boolean {
  const end = Math.max(0, Math.min(caret, text.length));
  return !text.slice(0, end).includes('\n');
}

/**
 * caretOnLastLine 判断光标之后是否没有换行符，即光标是否位于末行。
 * 对应 internal/ui/composer_history.go:71。
 */
export function caretOnLastLine(text: string, caret: number): boolean {
  const start = Math.max(0, Math.min(caret, text.length));
  return !text.slice(start).includes('\n');
}

/**
 * composerHistoryPrev 返回上一条（更旧）历史文本；首次翻动时把 current 存进 stash。
 * 返回 null 表示没有历史或已翻到最旧一条，状态保持不变。
 * 对应 internal/ui/composer_history.go:84 的 (*composerHistoryState).prev。
 */
export function composerHistoryPrev(
  state: ComposerHistoryState,
  entries: ReadonlyArray<string>,
  current: string
): { state: ComposerHistoryState; text: string } | null {
  if (entries.length === 0) return null;
  if (state.index >= entries.length) return null;
  const index = state.index + 1;
  const recalled = entries[entries.length - index];
  return {
    state: { index, stash: state.index === 0 ? current : state.stash, recalled },
    text: recalled
  };
}

/**
 * composerHistoryNext 返回下一条（更新）历史文本；翻过最新一条后恢复 stash 并退出导航。
 * 返回 null 表示未在导航。
 * 对应 internal/ui/composer_history.go:101 的 (*composerHistoryState).next。
 */
export function composerHistoryNext(
  state: ComposerHistoryState,
  entries: ReadonlyArray<string>
): { state: ComposerHistoryState; text: string } | null {
  if (state.index === 0) return null;
  if (state.index > 1 && state.index <= entries.length) {
    const index = state.index - 1;
    return {
      state: { index, stash: state.stash, recalled: entries[entries.length - index] },
      text: entries[entries.length - index]
    };
  }
  // 翻过最新一条：把进入导航前的草稿还给用户，并退出导航。
  return { state: emptyComposerHistoryState, text: state.stash };
}

/**
 * composerHistoryRestore 退出导航并返回进入导航前的草稿。
 * 返回 null 表示未在导航。对应 internal/ui/composer_history.go:126 的 restore。
 */
export function composerHistoryRestore(
  state: ComposerHistoryState
): { state: ComposerHistoryState; text: string } | null {
  if (state.index === 0) return null;
  return { state: emptyComposerHistoryState, text: state.stash };
}

/**
 * composerHistoryReset 退出导航并丢弃暂存草稿，由调用方保留输入框当前文本。
 * 对应 internal/ui/composer_history.go:116 的 reset。
 */
export function composerHistoryReset(): ComposerHistoryState {
  return emptyComposerHistoryState;
}
