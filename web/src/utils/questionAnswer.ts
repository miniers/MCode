// question_answer 系统消息的正文解析：与后端 internal/remote/question_answer.go 的
// questionAnswerRecord / questionAnswerItem 共用一份 JSON 结构。
// 解析失败返回 null，调用方退回普通系统消息渲染，不能让一条脏消息影响整条时间线。

export interface QuestionAnswerItem {
  question: string;
  answers: string[];
}

export interface QuestionAnswerRecord {
  requestID: string;
  skipped: boolean;
  items: QuestionAnswerItem[];
}

function asRecord(value: unknown): Record<string, unknown> | null {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return null;
  return value as Record<string, unknown>;
}

function asString(value: unknown): string {
  return typeof value === 'string' ? value : '';
}

function asAnswers(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return value
    .map((entry) => asString(entry).trim())
    .filter((entry) => entry !== '');
}

function asItem(value: unknown): QuestionAnswerItem | null {
  const raw = asRecord(value);
  if (!raw) return null;
  const item: QuestionAnswerItem = {
    question: asString(raw.question).trim(),
    answers: asAnswers(raw.answers)
  };
  // 题目与答案都为空：跳过空条目，避免卡片里多出一行空白。
  if (item.question === '' && item.answers.length === 0) return null;
  return item;
}

/** 解析问答记录正文；正文不是合法 JSON 或没有任何可用信息时返回 null。 */
export function parseQuestionAnswer(text: string): QuestionAnswerRecord | null {
  if (typeof text !== 'string' || text.trim() === '') return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return null;
  }
  const raw = asRecord(parsed);
  if (!raw) return null;

  // 后端字段是 requestID；兼容小驼峰 requestId，不让大小写差异把卡片退回成原文。
  const requestID = asString(raw.requestID).trim() || asString(raw.requestId).trim();
  const skipped = raw.skipped === true;
  const items = (Array.isArray(raw.items) ? raw.items : [])
    .map(asItem)
    .filter((item): item is QuestionAnswerItem => item !== null);

  // 没有任何可用信息（例如正文是 null、{} 或 []）：当成非法记录，退回原文渲染。
  if (!skipped && requestID === '' && items.length === 0) return null;

  return { requestID, skipped, items };
}
