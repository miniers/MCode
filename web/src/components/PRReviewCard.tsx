// PR 审查报告卡片（Scheme A 单栏决策流 + 手风琴）与纯解析函数 parsePRReview。

import React, { useState } from 'react';
import { ChevronRightIcon } from './Icons';

export type PRReviewVerdict = 'merge' | 'fix' | 'comment' | 'close';
export type PRReviewSeverity = 'blocking' | 'suggestion';

export interface PRReviewPatchLine {
  type: 'ctx' | 'add' | 'del';
  text: string;
}

export interface PRReviewComment {
  path: string;
  line?: number;
  severity: PRReviewSeverity;
  headline: string;
  impact?: string;
  fix?: string;
  body?: string;
  patchLines: PRReviewPatchLine[];
}

export interface ParsedPRReview {
  verdict: PRReviewVerdict;
  verdictLabel: string;
  reason: string;
  /** 按分组顺序排列：阻塞项在前，建议项在后。 */
  comments: PRReviewComment[];
  blocking: PRReviewComment[];
  suggestions: PRReviewComment[];
}

const VERDICT_LABELS: Record<PRReviewVerdict, string> = {
  merge: '可以合并',
  fix: '修复后合并',
  comment: '需作者修改',
  close: '不建议合并'
};

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function firstNonEmptyLine(text: string): string {
  for (const line of text.split(/\r?\n/)) {
    const trimmed = line.trim();
    if (trimmed) return trimmed;
  }
  return '';
}

function classifyPatchLine(text: string, hintType?: string): PRReviewPatchLine {
  if (hintType === 'add' || hintType === 'del' || hintType === 'ctx') {
    return { type: hintType, text };
  }
  if (text.startsWith('+') && !text.startsWith('+++')) {
    return { type: 'add', text };
  }
  if (text.startsWith('-') && !text.startsWith('---')) {
    return { type: 'del', text };
  }
  return { type: 'ctx', text };
}

function parsePatchLines(rawPatch: unknown): PRReviewPatchLine[] {
  if (typeof rawPatch === 'string') {
    const normalized = rawPatch.replace(/\r\n/g, '\n').replace(/\r/g, '\n');
    if (!normalized.trim()) return [];
    const lines = normalized.split('\n');
    if (lines.length > 0 && lines[lines.length - 1] === '') lines.pop();
    return lines.map((line) => classifyPatchLine(line));
  }
  if (Array.isArray(rawPatch)) {
    const result: PRReviewPatchLine[] = [];
    for (const item of rawPatch) {
      if (typeof item === 'string') {
        result.push(classifyPatchLine(item));
      } else if (isRecord(item) && typeof item.text === 'string') {
        const hint = typeof item.type === 'string' ? item.type : undefined;
        result.push(classifyPatchLine(item.text, hint));
      }
    }
    return result;
  }
  return [];
}

function parseCommentItem(rawItem: unknown): PRReviewComment | null {
  if (!isRecord(rawItem)) return null;
  const path = typeof rawItem.path === 'string' ? rawItem.path.trim() : '';
  const rawLine = typeof rawItem.line === 'number' ? rawItem.line : Number.NaN;
  const line = Number.isFinite(rawLine) && rawLine > 0 ? Math.floor(rawLine) : undefined;
  const rawSeverity = typeof rawItem.severity === 'string' ? rawItem.severity.trim().toLowerCase() : '';
  const severity: PRReviewSeverity = rawSeverity === 'blocking' ? 'blocking' : 'suggestion';

  const body = typeof rawItem.body === 'string' ? rawItem.body.trim() : '';
  const explicitHeadline = typeof rawItem.headline === 'string' ? rawItem.headline.trim() : '';
  const headline = explicitHeadline || firstNonEmptyLine(body) || path || '审查意见';

  const impact = typeof rawItem.impact === 'string' ? rawItem.impact.trim() : '';
  const fix = typeof rawItem.fix === 'string' ? rawItem.fix.trim() : '';
  const patchLines = parsePatchLines(rawItem.patch);

  return {
    path,
    ...(line !== undefined ? { line } : {}),
    severity,
    headline,
    ...(impact ? { impact } : {}),
    ...(fix ? { fix } : {}),
    ...(body ? { body } : {}),
    patchLines
  };
}

function normalizeVerdict(rawVerdict: unknown, blockingCount: number, totalCount: number): PRReviewVerdict {
  if (typeof rawVerdict === 'string') {
    const normalized = rawVerdict.trim().toLowerCase();
    if (normalized === 'merge' || normalized === 'fix' || normalized === 'comment' || normalized === 'close') {
      return normalized;
    }
  }
  if (blockingCount > 0) return 'fix';
  if (totalCount > 0) return 'comment';
  return 'merge';
}

function groupComments(items: unknown[]): {
  blocking: PRReviewComment[];
  suggestions: PRReviewComment[];
  comments: PRReviewComment[];
} {
  const blocking: PRReviewComment[] = [];
  const suggestions: PRReviewComment[] = [];
  for (const item of items) {
    const parsed = parseCommentItem(item);
    if (!parsed) continue;
    if (parsed.severity === 'blocking') {
      blocking.push(parsed);
    } else {
      suggestions.push(parsed);
    }
  }
  return {
    blocking,
    suggestions,
    comments: [...blocking, ...suggestions]
  };
}

/**
 * 解析 pr-review 代码块中的 JSON（兼容新格式、带 summary/body 的旧对象格式、以及旧条目数组格式）。
 * 非法 JSON 或无法识别的结构返回 null。
 */
export function parsePRReview(raw: string): ParsedPRReview | null {
  const trimmed = raw.trim();
  if (!trimmed) return null;

  let data: unknown;
  try {
    data = JSON.parse(trimmed);
  } catch {
    return null;
  }

  if (Array.isArray(data)) {
    const { blocking, suggestions, comments } = groupComments(data);
    if (data.length > 0 && comments.length === 0) return null;
    const verdict = normalizeVerdict(undefined, blocking.length, comments.length);
    return {
      verdict,
      verdictLabel: VERDICT_LABELS[verdict],
      reason: '',
      comments,
      blocking,
      suggestions
    };
  }

  if (!isRecord(data)) return null;

  const hasKnownKey =
    'verdict' in data || 'reason' in data || 'summary' in data || 'comments' in data;
  if (!hasKnownKey) return null;

  const rawComments = Array.isArray(data.comments) ? data.comments : [];
  const { blocking, suggestions, comments } = groupComments(rawComments);
  const reason =
    typeof data.reason === 'string' && data.reason.trim()
      ? data.reason.trim()
      : typeof data.summary === 'string'
        ? data.summary.trim()
        : '';
  const verdict = normalizeVerdict(data.verdict, blocking.length, comments.length);

  return {
    verdict,
    verdictLabel: VERDICT_LABELS[verdict],
    reason,
    comments,
    blocking,
    suggestions
  };
}

function verdictPillStyle(verdict: PRReviewVerdict): React.CSSProperties {
  if (verdict === 'merge') {
    return {
      backgroundColor: 'color-mix(in srgb, var(--mc-success) 12%, transparent)',
      color: 'var(--mc-success)',
      border: '1px solid color-mix(in srgb, var(--mc-success) 30%, transparent)'
    };
  }
  if (verdict === 'fix') {
    return {
      backgroundColor: 'color-mix(in srgb, var(--mc-warning) 12%, transparent)',
      color: 'var(--mc-warning)',
      border: '1px solid color-mix(in srgb, var(--mc-warning) 30%, transparent)'
    };
  }
  return {
    backgroundColor: 'color-mix(in srgb, var(--mc-danger) 12%, transparent)',
    color: 'var(--mc-danger)',
    border: '1px solid color-mix(in srgb, var(--mc-danger) 30%, transparent)'
  };
}

function verdictBarBackground(verdict: PRReviewVerdict): string {
  if (verdict === 'merge') {
    return 'linear-gradient(180deg, color-mix(in srgb, var(--mc-success) 12%, transparent) 0%, transparent 100%)';
  }
  if (verdict === 'fix') {
    return 'linear-gradient(180deg, color-mix(in srgb, var(--mc-warning) 12%, transparent) 0%, transparent 100%)';
  }
  return 'linear-gradient(180deg, color-mix(in srgb, var(--mc-danger) 12%, transparent) 0%, transparent 100%)';
}

function formatLocation(comment: PRReviewComment): string {
  if (!comment.path) return '';
  return comment.line !== undefined ? `${comment.path}:${comment.line}` : comment.path;
}

function issueCountText(review: ParsedPRReview): string {
  if (review.comments.length === 0) return '0 个问题';
  if (review.blocking.length > 0 && review.suggestions.length > 0) {
    return `${review.blocking.length} 阻塞 · ${review.suggestions.length} 建议`;
  }
  if (review.blocking.length > 0) {
    return `${review.blocking.length} 个阻塞问题`;
  }
  return `${review.suggestions.length} 条建议`;
}

const PRReviewIssueRow: React.FC<{
  comment: PRReviewComment;
  defaultOpen?: boolean;
}> = ({ comment, defaultOpen = false }) => {
  const [open, setOpen] = useState(defaultOpen);
  const [diffOpen, setDiffOpen] = useState(false);

  const location = formatLocation(comment);
  const showFallbackBody = !comment.impact && !comment.fix && Boolean(comment.body);
  const hasDetails = Boolean(
    comment.impact || comment.fix || showFallbackBody || comment.patchLines.length > 0
  );

  return (
    <div className="border-t border-mc-hairline first:border-t-0">
      <button
        type="button"
        onClick={() => hasDetails && setOpen((prev) => !prev)}
        aria-expanded={hasDetails ? open : undefined}
        className="w-full min-h-[44px] px-4 py-3 flex items-start gap-2.5 text-left hover:bg-mc-overlay-hover transition-colors motion-reduce:transition-none"
      >
        {hasDetails ? (
          <span
            className={`pr-chevron-rot mt-0.5 text-mc-text-tertiary shrink-0${open ? ' is-open' : ''}`}
          >
            <ChevronRightIcon className="w-3.5 h-3.5" />
          </span>
        ) : (
          <span className="w-3.5 shrink-0" aria-hidden="true" />
        )}
        <span className="flex-1 min-w-0 flex flex-col gap-1">
          {location && (
            <span className="inline-flex items-center self-start max-w-full px-1.5 py-0.5 rounded-mc-xs bg-mc-overlay-hover font-mono text-mc-caption text-mc-text-secondary truncate">
              {location}
            </span>
          )}
          <span className="text-mc-body font-medium text-mc-text-primary leading-snug select-text break-words">
            {comment.headline}
          </span>
        </span>
      </button>

      {hasDetails && (
        <div
          className={`pr-checks-collapse${open ? ' is-open' : ''}`}
          aria-hidden={!open}
        >
          <div className="pr-checks-collapse__inner">
            <div className="px-4 pb-3.5 pl-10 space-y-2.5">
              {(comment.impact || comment.fix) && (
                <div className="grid grid-cols-[44px_1fr] gap-x-3 gap-y-1.5 text-mc-body">
                  {comment.impact && (
                    <>
                      <span className="text-mc-footnote font-semibold text-mc-text-tertiary">影响</span>
                      <span className="text-mc-text-secondary leading-relaxed select-text break-words">
                        {comment.impact}
                      </span>
                    </>
                  )}
                  {comment.fix && (
                    <>
                      <span className="text-mc-footnote font-semibold text-mc-text-tertiary">建议</span>
                      <span className="text-mc-text-secondary leading-relaxed select-text break-words">
                        {comment.fix}
                      </span>
                    </>
                  )}
                </div>
              )}

              {showFallbackBody && (
                <p className="text-mc-footnote text-mc-text-secondary leading-relaxed whitespace-pre-wrap select-text break-words">
                  {comment.body}
                </p>
              )}

              {comment.patchLines.length > 0 && (
                <div className="rounded-mc-md border border-mc-border bg-mc-surface-main overflow-hidden">
                  <button
                    type="button"
                    onClick={() => setDiffOpen((prev) => !prev)}
                    aria-expanded={diffOpen}
                    tabIndex={open ? undefined : -1}
                    className="w-full min-h-[36px] touch-hit-44 px-3 py-1.5 flex items-center justify-between text-mc-caption text-mc-text-secondary hover:bg-mc-overlay-hover hover:text-mc-text-primary transition-colors motion-reduce:transition-none"
                  >
                    <span>参考改动片段</span>
                    <span>{diffOpen ? '收起' : '展开'}</span>
                  </button>
                  <div
                    className={`pr-checks-collapse${diffOpen ? ' is-open border-t border-mc-hairline' : ''}`}
                    aria-hidden={!diffOpen}
                  >
                    <div className="pr-checks-collapse__inner">
                      <div className="font-mono text-mc-code leading-relaxed overflow-x-auto select-text py-1">
                        {comment.patchLines.map((line, idx) => {
                          const style: React.CSSProperties | undefined =
                            line.type === 'add'
                              ? {
                                  backgroundColor: 'color-mix(in srgb, var(--mc-success) 14%, transparent)',
                                  color: 'var(--mc-success)'
                                }
                              : line.type === 'del'
                                ? {
                                    backgroundColor: 'color-mix(in srgb, var(--mc-danger) 14%, transparent)',
                                    color: 'var(--mc-danger)'
                                  }
                                : { color: 'var(--mc-text-tertiary)' };
                          return (
                            <div key={idx} className="px-3 py-0.5 whitespace-pre" style={style}>
                              {line.text || ' '}
                            </div>
                          );
                        })}
                      </div>
                    </div>
                  </div>
                </div>
              )}
            </div>
          </div>
        </div>
      )}
    </div>
  );
};

export const PRReviewCard: React.FC<{ review: ParsedPRReview }> = ({ review }) => {
  const renderGroup = (
    title: string,
    dotColorClass: string,
    items: PRReviewComment[],
    defaultFirstOpen: boolean
  ) => {
    if (items.length === 0) return null;
    return (
      <div className="border-t border-mc-hairline">
        <div className="flex items-center justify-between px-4 py-2 bg-mc-overlay-hover text-mc-caption font-semibold text-mc-text-secondary">
          <span className="inline-flex items-center gap-2">
            <span className={`w-1.5 h-1.5 rounded-full shrink-0 ${dotColorClass}`} />
            <span>{title}</span>
          </span>
          <span className="tabular-nums">{items.length}</span>
        </div>
        <div>
          {items.map((comment, idx) => (
            <PRReviewIssueRow
              key={`${comment.path}-${comment.line ?? 0}-${idx}`}
              comment={comment}
              defaultOpen={defaultFirstOpen && idx === 0}
            />
          ))}
        </div>
      </div>
    );
  };

  return (
    <div
      className="my-2 min-w-0 max-w-full rounded-mc-lg border border-mc-border bg-mc-surface-card overflow-hidden"
      data-pr-review-verdict={review.verdict}
    >
      <div
        className="px-4 py-3.5 space-y-1.5"
        style={{ background: verdictBarBackground(review.verdict) }}
      >
        <div className="flex flex-wrap items-center gap-2">
          <span
            className="inline-flex items-center h-mc-control-xs px-2 rounded-mc-pill text-mc-caption font-semibold"
            style={verdictPillStyle(review.verdict)}
          >
            {review.verdictLabel}
          </span>
          <span className="text-mc-caption text-mc-text-secondary tabular-nums">
            {issueCountText(review)}
          </span>
        </div>
        {review.reason && (
          <p className="text-mc-body font-medium text-mc-text-primary leading-relaxed select-text break-words">
            {review.reason}
          </p>
        )}
      </div>

      {review.comments.length === 0 ? (
        <div className="border-t border-mc-hairline px-4 py-3 text-mc-footnote text-mc-text-secondary">
          无阻塞项与修改建议
        </div>
      ) : (
        <>
          {renderGroup('阻塞合并', 'bg-mc-danger', review.blocking, true)}
          {renderGroup('建议修复', 'bg-mc-warning', review.suggestions, review.blocking.length === 0)}
        </>
      )}
    </div>
  );
};
