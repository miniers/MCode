// 输入区的 `/` 命令、`$技能` 与 `#模型` 补全辅助：纯函数，不依赖 React 与连接状态。

import type { RemoteCommand, RemoteFileEntry, RemoteProvider } from '../types/contract';
import type { RemoteArtifactType } from '../types/generated';

export const OPEN_ARTIFACT = 'open-artifact';
const artifactLabels: Record<string, string> = {
  data: '看板', design: '设计', 'design-system': '设计系统', docs: '文档',
  motion: '动画', page: '网页', sheets: '表格', slides: '幻灯片', whiteboard: '白板'
};
const localSlashCommands = new Set(['mode', 'model', 'effort', 'fast', 'pair', 'export', 'clear', 'copy', 'forward']);
const slashHints: Record<string, string> = {
  model: '打开模型选择', effort: 'low | medium | high', export: '导出为 Markdown/JSON',
  copy: '复制会话 Markdown', plan: '<任务描述>', compact: '[保留重点]',
  forward: '转发到新会话', [OPEN_ARTIFACT]: '<类型> <制作内容>'
};

export function artifactLabel(type: RemoteArtifactType): string {
  return Object.prototype.hasOwnProperty.call(artifactLabels, type.id) ? artifactLabels[type.id] : type.name || type.id;
}

export function artifactDraft(text: string, types: RemoteArtifactType[]) {
  const match = /^\/open-artifact ([^\s]+) /.exec(text);
  const type = match && types.find((item) => item.id === match[1]);
  return match && type ? { prefix: match[0], label: artifactLabel(type) } : null;
}

export function fileSearchScope(text: string): { query: string; path: string } {
  const slash = text.lastIndexOf('/');
  return { query: text.slice(slash + 1), path: text.slice(0, Math.max(slash, 0)) };
}

export interface CompletionToken {
  start: number;
  end: number;
}

export function rebaseCompletionTokens(previous: string, next: string, tokens: CompletionToken[]): CompletionToken[] {
  if (!tokens.length || previous === next) return tokens;
  let start = 0;
  let previousEnd = previous.length;
  let nextEnd = next.length;
  while (start < previousEnd && start < nextEnd && previous[start] === next[start]) start++;
  while (previousEnd > start && nextEnd > start && previous[previousEnd - 1] === next[nextEnd - 1]) {
    previousEnd--;
    nextEnd--;
  }
  const shift = nextEnd - previousEnd;
  return tokens.flatMap((token) => {
    if (token.end <= start) return [token];
    if (token.start >= previousEnd) return [{ start: token.start + shift, end: token.end + shift }];
    return [];
  });
}

export function deleteCompletionToken(text: string, caret: number, key: 'Backspace' | 'Delete', tokens: CompletionToken[]) {
  for (const { start, end } of tokens) {
    if (start > 0 && !/\s/.test(text[start - 1])) continue;
    if (end < text.length && !/\s/.test(text[end])) continue;
    const after = text[end] === ' ' ? end + 1 : end;
    const matches = key === 'Delete' ? caret === start : caret === end || caret === after;
    if (matches) return { text: text.slice(0, start) + text.slice(after), caret: start };
  }
  return null;
}

/** `threads.commands` 的取数范围：已有会话，或草稿阶段的 项目 + Provider。 */
export type CommandScope = { threadID: string } | { projectID: string; provider: string };

export function commandScopeKey(scope: CommandScope): string {
  return 'threadID' in scope ? `thread:${scope.threadID}` : `draft:${scope.projectID}:${scope.provider}`;
}

export type SuggestionTrigger = '@' | '/' | '$' | '#';

/** 补全弹层的统一条目；文件、命令、模型共用同一套键盘与点选交互。 */
export interface SuggestionItem {
  key: string;
  title: string;
  detail: string;
  icon: 'file' | 'folder' | 'link' | 'builtin' | 'command' | 'skill' | 'model';
  /** 右侧附注（文件大小等），可空。 */
  meta?: string;
  /** 替换触发片段的完整文本（含触发符与尾随空格）。 */
  insertText: string;
  kind: 'file' | 'command' | 'model';
  group?: string;
  hint?: string;
}

export interface TriggerQueryInfo {
  trigger: SuggestionTrigger;
  query: string;
  startIndex: number;
  endIndex: number;
}

/**
 * 侦测光标处是否正在输入某个触发符片段。
 * - `@`、`$`、`#`：位于行首或紧随空白，查询串内不含空白（与桌面端 detectCompletion 的 token 规则一致）。
 * - `/`：只有整条消息的第一个字符是 `/` 且光标仍在第一个词内才算（与 Mac 一致）。
 * `/`、`$`、`#` 替换时覆盖到当前词尾，避免光标落在词中间时留下残字。
 */
export function extractTriggerQuery(
  text: string,
  cursorPosition: number,
  trigger: SuggestionTrigger
): TriggerQueryInfo | null {
  if (cursorPosition <= 0) return null;
  const textBefore = text.slice(0, cursorPosition);
  const startIndex = trigger === '/' ? (text.startsWith('/') ? 0 : -1) : textBefore.lastIndexOf(trigger);
  if (startIndex === -1) return null;

  if (trigger !== '/' && startIndex > 0 && !/\s/.test(textBefore[startIndex - 1])) {
    return null;
  }

  const query = textBefore.slice(startIndex + 1);
  if (/\s/.test(query)) return null;

  let endIndex = cursorPosition;
  if (trigger !== '@') {
    const rest = text.slice(cursorPosition);
    const wordEnd = rest.search(/\s/);
    endIndex = wordEnd === -1 ? text.length : cursorPosition + wordEnd;
  }
  return { trigger, query, startIndex, endIndex };
}

/**
 * 匹配规则：名称包含查询（大小写不敏感）；
 * 前缀匹配优先；其次按 recent（越靠前越优先）；再按名称字典序；同名只保留第一条。
 */
export function filterSlashCommands(
  commands: RemoteCommand[],
  query: string,
  recent: string[],
  grouped = true
): RemoteCommand[] {
  const needle = query.toLowerCase();
  const ranks = new Map<string, number>();
  recent.forEach((name, index) => {
    if (!ranks.has(name)) ranks.set(name, index);
  });
  const seen = new Set<string>();
  const matched: Array<{ command: RemoteCommand; name: string; prefixed: boolean; rank: number }> = [];
  for (const command of commands) {
    if (seen.has(command.name)) continue;
    seen.add(command.name);
    const name = command.name.toLowerCase();
    const artifactAlias = grouped && name === OPEN_ARTIFACT && ['artifact', '交付物', '创建', '做', '生成'].some((alias) => alias.includes(needle));
    if (needle.length > 0 && !name.includes(needle) && !artifactAlias) continue;
    matched.push({
      command,
      name,
      prefixed: name.startsWith(needle),
      rank: ranks.get(command.name) ?? Number.MAX_SAFE_INTEGER
    });
  }
  matched.sort((lhs, rhs) => {
    if (grouped && needle === '') {
      const group = (name: string) => name === OPEN_ARTIFACT ? 0 : localSlashCommands.has(name) ? 1 : 2;
      if (group(lhs.name) !== group(rhs.name)) return group(lhs.name) - group(rhs.name);
    }
    if (lhs.prefixed !== rhs.prefixed) return lhs.prefixed ? -1 : 1;
    if (lhs.rank !== rhs.rank) return lhs.rank - rhs.rank;
    return lhs.name < rhs.name ? -1 : lhs.name > rhs.name ? 1 : 0;
  });
  return matched.map((entry) => entry.command);
}

function formatBytes(bytes: number): string {
  if (bytes <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB'];
  const i = Math.floor(Math.log(bytes) / Math.log(1024));
  const val = bytes / Math.pow(1024, i);
  return `${val.toFixed(i === 0 ? 0 : 1)} ${units[i] || 'B'}`;
}

/** `@` 文件补全条目：与命令、模型共用同一套弹层。 */
export function fileEntryToSuggestion(entry: RemoteFileEntry): SuggestionItem {
  return {
    key: `file:${entry.path}`,
    title: entry.name,
    detail: entry.path,
    icon: entry.kind === 'directory' ? 'folder' : entry.kind === 'symlink' ? 'link' : 'file',
    meta: entry.ignored ? '已忽略' : entry.size > 0 && entry.kind !== 'directory' ? formatBytes(entry.size) : undefined,
    insertText: `@${entry.path} `,
    kind: 'file'
  };
}

/**
 * `$技能` 单一触发符的纯函数：从 `threads.commands` 结果里只取 kind === 'skill' 的项，
 * 过滤规则沿用 `/` 命令（名称包含、前缀优先、同名只留第一条）。
 */
export function filterSkillCommands(commands: RemoteCommand[], query: string): RemoteCommand[] {
  return filterSlashCommands(
    commands.filter((command) => command.kind === 'skill'),
    query,
    [],
    false
  );
}

/**
 * 技能候选：插入文本与桌面端一致——`$` + 技能名（名称里的空格换成 `-`）后再补一个空格，
 * 见 internal/ui/composer_complete.go:376 与 internal/ui/composer_complete.go:649。
 */
export function skillCommandToSuggestion(command: RemoteCommand): SuggestionItem {
  return {
    key: `skill:${command.name}`,
    title: `$${command.name}`,
    detail: command.detail,
    icon: 'skill',
    insertText: `$${command.name.replace(/ /g, '-')} `,
    kind: 'command'
  };
}

export function commandToSuggestion(command: RemoteCommand, grouped = false): SuggestionItem {
  return {
    key: `command:${command.name}`,
    title: `/${command.name}`,
    detail: command.detail,
    group: grouped && command.name !== OPEN_ARTIFACT
      ? localSlashCommands.has(command.name) ? '本地操作' : '会话与 Provider'
      : undefined,
    hint: Object.prototype.hasOwnProperty.call(slashHints, command.name) ? slashHints[command.name] : undefined,
    icon: command.kind === 'skill' ? 'skill' : command.kind === 'builtin' || command.isBuiltIn ? 'builtin' : 'command',
    insertText: `/${command.name} `,
    kind: 'command'
  };
}

/** 去除首尾空白后精确等于 `/plan`、`/compact` 才是本地命令（`/plan 正文` 不算）。 */
export function localCommandName(text: string): 'plan' | 'compact' | null {
  const trimmed = text.trim();
  if (trimmed === '/plan') return 'plan';
  if (trimmed === '/compact') return 'compact';
  return null;
}

/** 去除首尾空白后精确等于 `/forward` 或其别名（`/handoff`、`/fw`）时视为本地转发命令。 */
export function isForwardSlashCommand(text: string): boolean {
  const trimmed = text.trim().toLowerCase();
  return trimmed === '/forward' || trimmed === '/handoff' || trimmed === '/fw';
}

/**
 * `#模型` 候选：Mac 输入框的模型列表顺序（catalog），没有 catalog 时按各 Provider 的模型顺序；
 * 只列 available 的 Provider，编排组合不在其中。
 */
export function modelSuggestions(
  providers: RemoteProvider[] & { catalog?: Array<{ provider: string; modelID: string }> },
  query: string,
  limit = 30
): SuggestionItem[] {
  // 只列 available 且未在桌面设置里关掉的 Provider，编排组合不在其中。
  const usable = providers.filter((provider) => provider.available && provider.enabled !== false);
  const pairs: Array<{ provider: RemoteProvider; modelID: string; modelName: string }> = [];
  const seen = new Set<string>();
  const push = (provider: RemoteProvider, modelID: string, modelName: string) => {
    const id = `${provider.id}/${modelID}`;
    if (seen.has(id)) return;
    seen.add(id);
    pairs.push({ provider, modelID, modelName });
  };
  if (providers.catalog && providers.catalog.length > 0) {
    for (const entry of providers.catalog) {
      const provider = usable.find((candidate) => candidate.id === entry.provider);
      const model = provider?.models.find((candidate) => candidate.id === entry.modelID);
      if (provider && model) push(provider, model.id, model.name);
    }
  } else {
    for (const provider of usable) {
      for (const model of provider.models) push(provider, model.id, model.name);
    }
  }

  const needle = query.toLowerCase();
  const ranked: Array<{ pair: (typeof pairs)[number]; prefixed: boolean; order: number }> = [];
  pairs.forEach((pair, order) => {
    const id = `${pair.provider.id}/${pair.modelID}`.toLowerCase();
    const haystacks = [id, pair.modelName.toLowerCase(), pair.provider.name.toLowerCase()];
    if (needle.length > 0 && !haystacks.some((value) => value.includes(needle))) return;
    ranked.push({ pair, prefixed: id.startsWith(needle), order });
  });
  ranked.sort((lhs, rhs) => {
    if (lhs.prefixed !== rhs.prefixed) return lhs.prefixed ? -1 : 1;
    return lhs.order - rhs.order;
  });
  return ranked.slice(0, limit).map(({ pair }) => ({
    key: `model:${pair.provider.id}/${pair.modelID}`,
    title: `${pair.provider.name} · ${pair.modelName}`,
    detail: `#${pair.provider.id}/${pair.modelID}`,
    icon: 'model',
    insertText: `#${pair.provider.id}/${pair.modelID} `,
    kind: 'model'
  }));
}

/**
 * 委派判定 `#<provider>/<modelID> 正文`：Provider 已启用且可用、模型存在、正文非空。
 * 裸 `#claude`、未知模型或没有正文都按普通文本，返回 null。
 */
export function parseDelegation(
  text: string,
  providers: RemoteProvider[]
): { providerID: string; modelID: string; prompt: string } | null {
  const trimmed = text.trim();
  if (!trimmed.startsWith('#')) return null;
  const withoutHash = trimmed.slice(1);
  const spaceIndex = withoutHash.search(/\s/);
  if (spaceIndex === -1) return null;
  const tag = withoutHash.slice(0, spaceIndex);
  const prompt = withoutHash.slice(spaceIndex).trim();
  const slash = tag.indexOf('/');
  if (!prompt || slash === -1) return null;
  const providerTag = tag.slice(0, slash).toLowerCase();
  const modelID = tag.slice(slash + 1);
  if (!modelID) return null;
  const provider = providers.find(
    (candidate) => candidate.available && candidate.enabled !== false && candidate.id.toLowerCase() === providerTag
  );
  if (!provider || !provider.models.some((model) => model.id === modelID)) return null;
  return { providerID: provider.id, modelID, prompt };
}
