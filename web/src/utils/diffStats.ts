// 会话改动统计：与桌面端 internal/git.ParseUnifiedDiff 相同的计数口径
// （internal/ui/composer_changes_tab.go 里 ensureThreadChangeStats 的统计方式）。

export interface UnifiedDiffStats {
  additions: number;
  deletions: number;
  files: number;
}

const EMPTY_STATS: UnifiedDiffStats = { additions: 0, deletions: 0, files: 0 };

/**
 * 按行拆分统一 diff，行为与 internal/git/diff.go 的 splitLines 一致：
 * 按 `\n` 切分、去掉行尾 `\r`，末尾换行不会产生额外空行。
 */
function splitDiffLines(diff: string): string[] {
  const lines: string[] = [];
  let start = 0;
  for (let i = 0; i < diff.length; i += 1) {
    if (diff.charCodeAt(i) !== 10) continue;
    let end = i;
    if (end > start && diff.charCodeAt(end - 1) === 13) end -= 1;
    lines.push(diff.slice(start, end));
    start = i + 1;
  }
  if (start < diff.length) {
    let end = diff.length;
    if (end > start && diff.charCodeAt(end - 1) === 13) end -= 1;
    lines.push(diff.slice(start, end));
  }
  return lines;
}

/**
 * 解析整段统一 diff，统计新增行、删除行与变更文件数。
 *
 * 口径对齐 internal/ui/composer_changes_tab.go 中「ParseUnifiedDiff + 逐 hunk 逐行计数」：
 * - 文件数 = `diff --git ` 头出现的次数（二进制、纯重命名、纯权限变更都算一个文件）；
 * - 只有进入 `@@` hunk 之后的 `+`/`-` 前缀行才计数，`+++ b/x`、`--- a/x` 这类文件头不计；
 * - `\ No newline at end of file` 这类注记以 `\` 开头，不计数；
 * - 没有 `diff --git ` 头之前的行不归属任何文件，全部忽略。
 */
export function parseUnifiedDiffStats(diff: string): UnifiedDiffStats {
  if (!diff) return { ...EMPTY_STATS };

  let additions = 0;
  let deletions = 0;
  let files = 0;
  let inHunk = false;

  for (const line of splitDiffLines(diff)) {
    if (line.startsWith('diff --git ')) {
      files += 1;
      inHunk = false;
      continue;
    }
    if (files === 0) continue;
    if (line.startsWith('@@')) {
      inHunk = true;
      continue;
    }
    if (!inHunk || line.length === 0) continue;
    const prefix = line[0];
    if (prefix === '+') additions += 1;
    else if (prefix === '-') deletions += 1;
  }

  return { additions, deletions, files };
}

/** 「N 个文件」，与桌面端 `fmt.Sprintf("%d 个文件", files)` 一致。 */
export function formatChangeStatsFileCount(files: number): string {
  return `${files} 个文件`;
}

/** 「+N」；桌面端仅在 add > 0 时显示，零值返回空串表示不渲染。 */
export function formatChangeStatsAdditions(additions: number): string {
  return additions > 0 ? `+${additions}` : '';
}

/** 「-N」；桌面端仅在 del > 0 时显示，零值返回空串表示不渲染。 */
export function formatChangeStatsDeletions(deletions: number): string {
  return deletions > 0 ? `-${deletions}` : '';
}

/** 合并文案，如「3 个文件 +12 -4」；零改动返回空串。 */
export function formatChangeStatsLabel(stats: UnifiedDiffStats): string {
  if (stats.files <= 0) return '';
  return [
    formatChangeStatsFileCount(stats.files),
    formatChangeStatsAdditions(stats.additions),
    formatChangeStatsDeletions(stats.deletions)
  ]
    .filter((part) => part.length > 0)
    .join(' ');
}

/**
 * 是否展示统计胶囊：桌面端只在 files > 0 时渲染，
 * 重命名、纯权限变更等只有文件数没有增删行的情况也照常展示。
 */
export function hasChangeStats(stats: UnifiedDiffStats): boolean {
  return stats.files > 0;
}
