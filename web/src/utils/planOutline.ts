// 计划消息的大纲提取与预览截断：对齐桌面端 internal/ui/plan_reader.go 的 planOutline / planPreviewLines。
// 时间线里的长计划只显示大纲 + 少量正文预览，全文交给「阅读全文」打开的 PlanReaderSheet，
// 避免长计划把时间线撑成一整屏 Markdown。

/** 计划正文里提取出来的一条标题。 */
export interface PlanHeading {
  /** 标题层级 1~6，对应 # 的个数 */
  level: number;
  /** 去掉 # 后的标题文本 */
  text: string;
}

/** 摘要卡大纲最多展示的标题条数（对齐桌面端 planOutlineMaxItems）。 */
export const PLAN_OUTLINE_MAX_ITEMS = 8;
/** 摘要卡正文预览的最大行数（对齐桌面端 planPreviewMaxLines）。 */
export const PLAN_PREVIEW_MAX_LINES = 6;

/** 代码块围栏行的标记（``` 或 ~~~），不是围栏行时返回空串。 */
function fenceMarker(trimmed: string): string {
  for (const marker of ['```', '~~~']) {
    if (trimmed.startsWith(marker)) return marker;
  }
  return '';
}

/** 解析一行 ATX 标题：行首（允许缩进）1~6 个 # 后跟空格或行尾。 */
function parsePlanHeading(line: string): PlanHeading | null {
  const trimmed = line.replace(/^[ \t]+/, '');
  let level = 0;
  while (level < trimmed.length && trimmed[level] === '#') level += 1;
  if (level === 0 || level > 6) return null;
  const rest = trimmed.slice(level);
  if (rest !== '' && rest[0] !== ' ' && rest[0] !== '\t') return null;
  const text = rest.trim();
  if (!text) return null;
  return { level, text };
}

/**
 * 从计划正文里提取 Markdown 标题，最多返回 limit 条。
 * 只认行首（允许缩进）连续 1~6 个 # 加空格的 ATX 标题行；围栏代码块（``` / ~~~）内部的 # 行忽略。
 */
export function planOutline(plan: string, limit = PLAN_OUTLINE_MAX_ITEMS): PlanHeading[] {
  if (limit <= 0) return [];
  const out: PlanHeading[] = [];
  let fence = ''; // 当前所处围栏代码块的标记，空串表示不在代码块里
  for (const line of plan.split('\n')) {
    const marker = fenceMarker(line.trim());
    if (marker) {
      if (!fence) fence = marker;
      else if (fence === marker) fence = '';
      continue;
    }
    if (fence) continue;
    const heading = parsePlanHeading(line);
    if (!heading) continue;
    out.push(heading);
    if (out.length >= limit) break;
  }
  return out;
}

/** 返回计划正文的前 n 行，不足 n 行时原样返回。 */
export function planPreviewLines(plan: string, n = PLAN_PREVIEW_MAX_LINES): string {
  if (n <= 0) return '';
  const lines = plan.split('\n');
  if (lines.length <= n) return plan;
  return lines.slice(0, n).join('\n');
}

/** 计划是否需要「阅读全文」：正文超过预览行数才截断。 */
export function planNeedsReader(plan: string): boolean {
  return plan.split('\n').length > PLAN_PREVIEW_MAX_LINES;
}
