// 长文本粘贴折叠成占位符的纯函数移植，规则与桌面端 internal/ui/composer_paste.go 一致：
// 超过行数或字数阈值的粘贴文本不在输入框里铺开，只留一个占位符，发送时再把占位符展开成原文，
// 作为内联文本发给模型（不是文件附件）。

/** 长文本粘贴的最小行数：达标即视为长文本（对应 internal/ui/composer_paste.go:13 的 pastedTextMinLines）。 */
export const PASTED_TEXT_MIN_LINES = 20;
/** 长文本粘贴的最小字数（按码点计，对应 internal/ui/composer_paste.go:14 的 pastedTextMinRunes 与 Go 的 utf8.RuneCountInString）。 */
export const PASTED_TEXT_MIN_RUNES = 1500;

/** 占位符 -> 原文 的映射，键是 pastedPlaceholderLabel 生成的整串占位符。 */
export type PastedTextMap = Readonly<Record<string, string>>;

/** runeLength 按码点计数，与 Go 的 utf8.RuneCountInString 一致（不是 UTF-16 长度）。 */
export function runeLength(text: string): number {
  return Array.from(text).length;
}

/** pastedTextLineCount 统计行数：换行数加一。 */
export function pastedTextLineCount(text: string): number {
  let lines = 1;
  for (const char of text) {
    if (char === '\n') lines += 1;
  }
  return lines;
}

/**
 * isLongPastedText 判断粘贴进来的文本是否算长文本：行数或字数任一达标即可。
 * 对应 internal/ui/composer_paste.go:18。
 */
export function isLongPastedText(text: string): boolean {
  if (text === '') return false;
  if (pastedTextLineCount(text) >= PASTED_TEXT_MIN_LINES) return true;
  return runeLength(text) >= PASTED_TEXT_MIN_RUNES;
}

/**
 * pastedPlaceholderLabel 生成占位符「[粘贴文本 #N · M 行]」，M 是行数（换行数加一）。
 * 文案必须与桌面端 internal/ui/composer_paste.go:29 完全一致。
 */
export function pastedPlaceholderLabel(seq: number, lines: number): string {
  return `[粘贴文本 #${seq} · ${lines} 行]`;
}

/**
 * nextPastedPlaceholder 登记一段长文本：序号从 1 开始自增，返回新的序号与占位符。
 * 对应 internal/ui/composer_paste.go:34 的 registerPastedPlaceholder。
 */
export function nextPastedPlaceholder(seq: number, text: string): { seq: number; label: string } {
  const nextSeq = seq + 1;
  return { seq: nextSeq, label: pastedPlaceholderLabel(nextSeq, pastedTextLineCount(text)) };
}

/**
 * expandPastedText 把文本里出现且已登记的占位符整串换回原文。
 * 只做纯查找替换、不动映射：发送可能被拒绝或后端失败，输入框里仍是占位符，
 * 用户重试时要能再展开一次，所以展开必须幂等。映射里没有的占位符保持原样。
 * 对应 internal/ui/composer_paste.go:52 的 expandPastedPlaceholders。
 */
export function expandPastedText(text: string, placeholders: PastedTextMap): string {
  if (text === '') return text;
  const labels = Object.keys(placeholders);
  if (labels.length === 0) return text;
  let expanded = text;
  for (const label of labels) {
    if (!expanded.includes(label)) continue;
    expanded = expanded.split(label).join(placeholders[label]);
  }
  return expanded;
}

/**
 * prunePastedText 丢弃文本里已经找不到的占位符条目：用户删掉占位符后原文不再需要。
 * 只清理确实不在文本里的键，仍留在文本里的条目保持不变。
 */
export function prunePastedText(text: string, placeholders: PastedTextMap): Record<string, string> {
  const kept: Record<string, string> = {};
  for (const [label, original] of Object.entries(placeholders)) {
    if (text.includes(label)) kept[label] = original;
  }
  return kept;
}

/**
 * insertPlaceholderAt 把占位符插到草稿的 [start, end) 区间（光标处即 start === end），
 * 返回新草稿和插入后光标应处的偏移（UTF-16 下标，可直接用于 textarea 的 setSelectionRange）。
 * 区间越界钳到文本长度内，end < start 时交换。
 * 对应 internal/ui/composer_paste.go:72 的 insertPastedPlaceholderAt。
 */
export function insertPlaceholderAt(
  draft: string,
  start: number,
  end: number,
  label: string
): { text: string; caret: number } {
  const length = draft.length;
  const from = Math.max(0, Math.min(start, length));
  const rawTo = Math.max(0, Math.min(end, length));
  const [begin, finish] = rawTo < from ? [rawTo, from] : [from, rawTo];
  return {
    text: draft.slice(0, begin) + label + draft.slice(finish),
    caret: begin + label.length
  };
}
