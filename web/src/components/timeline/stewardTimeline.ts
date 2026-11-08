// 时间线辅助判定：唤醒原因与思考摘要。

/** 唤醒正文里的事件行：由 internal/steward/wake.go 的 buildWakeMessage 生成，形如「1. 事件 ev-1（看板任务失败）」。 */
const WAKE_EVENT_LINE = /^\s*\d+\.\s*事件\s+\S+\s*（([^）]*)）/;
/** 唤醒正文里的事件标题行：「   标题：登录重构」。 */
const WAKE_TITLE_LINE = /^\s*标题：(.+)$/;
/** 唤醒正文的抬头：「有 3 条新情况，需要你先判断一下。」 */
const WAKE_TOTAL_LINE = /有\s*(\d+)\s*条新情况/;
/** 兜底巡检消息的开头，见 buildPatrolMessage。 */
const WAKE_PATROL_PREFIX = '兜底巡检';

/** 唤醒原因里引用的事件名长度上限。 */
const WAKE_REASON_MAX = 24;
/** 思考摘要的长度上限。 */
const HEADLINE_MAX = 60;

/** 按字符（而不是 UTF-16 码元）截断，超出补省略号。 */
function clip(text: string, max: number): string {
  const runes = Array.from(text);
  if (runes.length <= max) return text;
  return `${runes.slice(0, max).join('')}…`;
}

/**
 * 从一条总管唤醒消息的正文里提炼唤醒原因：「因「登录重构」被唤醒」「因「登录重构」等 3 件事被唤醒」。
 * 兜底巡检固定文案；解析不出事件时返回空串，调用方就不显示原因。
 */
export function stewardWakeReason(text: string): string {
  const body = (text ?? '').trim();
  if (body === '') return '';
  if (body.startsWith(WAKE_PATROL_PREFIX)) return '因兜底巡检被唤醒';

  const labels: string[] = [];
  let firstTitle = '';
  for (const line of body.split('\n')) {
    const event = WAKE_EVENT_LINE.exec(line);
    if (event) {
      labels.push((event[1] ?? '').trim());
      continue;
    }
    // 标题行只跟在事件行后面；第一条事件的标题才用来做原因
    if (labels.length === 1 && firstTitle === '') {
      const title = WAKE_TITLE_LINE.exec(line);
      if (title) firstTitle = (title[1] ?? '').trim();
    }
  }
  if (labels.length === 0) return '';

  const name = clip(firstTitle !== '' ? firstTitle : labels[0] ?? '', WAKE_REASON_MAX);
  if (name === '') return '';

  // 事件太多时正文只列前 maxBatchEvents 条，抬头里的总数更准，这里取两者较大值
  const totalMatch = WAKE_TOTAL_LINE.exec(body);
  const headerTotal = totalMatch ? Number.parseInt(totalMatch[1] ?? '', 10) : Number.NaN;
  const total = Number.isFinite(headerTotal) ? Math.max(labels.length, headerTotal) : labels.length;
  if (total <= 1) return `因「${name}」被唤醒`;
  return `因「${name}」等 ${total} 件事被唤醒`;
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

