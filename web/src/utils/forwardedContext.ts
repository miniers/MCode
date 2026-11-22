// 转发会话带入的历史上下文解析与分段：与桌面端 internal/ui/timeline_rows_context.go 保持一致。

export interface ForwardedTurn {
  role: 'user' | 'assistant' | 'omitted';
  text: string;
}

/**
 * 将转发会话 continuation 正文拆分为按角色排列的历史轮次。
 * 规则：空行后的 `User:` / `Assistant:` 切换角色，`[… ... …]` 识别为省略标记，
 * 文本末尾的 `[trimmed]` 统一替换为 `…`；未匹配到任何标记时作为单条助手内容回退展示。
 */
export function parseForwardedTurns(content: string): ForwardedTurn[] {
  const turns: ForwardedTurn[] = [];
  let role: 'user' | 'assistant' | '' = '';
  let lines: string[] = [];

  const flush = () => {
    if (!role) return;
    let text = lines.join('\n').trim();
    if (text === '[trimmed]') {
      text = '…';
    } else if (text.endsWith('\n[trimmed]')) {
      text = `${text.slice(0, -'[trimmed]'.length)}…`;
    }
    turns.push({ role, text });
    lines = [];
  };

  let previousBlank = true;
  for (const rawLine of content.split('\n')) {
    const line = rawLine.endsWith('\r') ? rawLine.slice(0, -1) : rawLine;
    let newRole: 'user' | 'assistant' | 'omitted' | '' = '';
    if (previousBlank && line === 'User:') {
      newRole = 'user';
    } else if (previousBlank && line === 'Assistant:') {
      newRole = 'assistant';
    } else if (line.startsWith('[…') && line.endsWith('…]')) {
      newRole = 'omitted';
    }
    if (newRole) {
      flush();
      if (newRole === 'omitted') {
        turns.push({ role: 'omitted', text: line });
        role = '';
      } else {
        role = newRole;
      }
    } else if (role) {
      lines.push(line);
    }
    previousBlank = line.trim() === '';
  }
  flush();

  if (turns.length === 0) {
    const trimmed = content.trim();
    if (!trimmed) return [];
    return [{ role: 'assistant', text: trimmed }];
  }
  return turns;
}

/**
 * 切分历史轮次：默认展开最后一条 user 及其之后的内容（tail），更早的轮次（earlier）折叠收纳。
 */
export function splitForwardedTail(turns: ForwardedTurn[]): {
  earlier: ForwardedTurn[];
  tail: ForwardedTurn[];
  earlierCount: number;
} {
  if (turns.length === 0) {
    return { earlier: [], tail: [], earlierCount: 0 };
  }
  let start = turns.length - 1;
  for (let i = turns.length - 1; i >= 0; i--) {
    if (turns[i].role === 'user') {
      start = i;
      break;
    }
  }
  const earlier = turns.slice(0, start);
  const tail = turns.slice(start);
  const earlierCount = earlier.filter((turn) => turn.role !== 'omitted').length;
  return { earlier, tail, earlierCount };
}
