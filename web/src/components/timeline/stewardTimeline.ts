// 时间线思考摘要。
/** 思考摘要的长度上限。 */
const HEADLINE_MAX = 60;

/** 按字符（而不是 UTF-16 码元）截断，超出补省略号。 */
function clip(text: string, max: number): string {
  const runes = Array.from(text);
  if (runes.length <= max) return text;
  return `${runes.slice(0, max).join('')}…`;
}

/**
 * 一段思考正文的标题行：去行首空白与 # > - * 标记、去掉强调符号与反引号、去掉有序列表前缀后
 * 取第一行（latest 为真取最后一行）。没有内容时返回空串，调用方保持原有标题。
 */
export function reasoningHeadline(text: string, latest: boolean): string {
  const lines = (text ?? '')
    .split('\n')
    .map((line) =>
      line
        .replace(/\*\*/g, '')
        .replace(/`/g, '')
        .replace(/^[\s#>\-*]+/, '')
        .replace(/^\d+[.)]\s*/, '')
        .trim()
    )
    .filter((line) => line !== '');
  if (lines.length === 0) return '';
  return clip(latest ? lines[lines.length - 1] : lines[0], HEADLINE_MAX);
}

/**
 * 思考行的标题：结束后取原文第一行的要旨，流式中取最新一句。
 * 空文本只返回标签本身，不留下「思考过程 · 」这样的尾巴。
 */
export function reasoningHeaderTitle(text: string, streaming: boolean): string {
  const label = streaming ? '思考中' : '思考过程';
  const headline = reasoningHeadline(text, streaming);
  return headline === '' ? label : `${label} · ${headline}`;
}

