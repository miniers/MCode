import React from 'react';
import { MarkdownView } from '../MarkdownView';

export interface PlanDecision { requestID: string; approved: boolean; plan: string; feedback: string }

export function parsePlanDecision(text: string): PlanDecision | null {
  try {
    const decision = JSON.parse(text) as { requestID?: unknown; approved?: unknown; plan?: unknown; feedback?: unknown };
    if (!decision || typeof decision.requestID !== 'string' || !decision.requestID.trim() || typeof decision.approved !== 'boolean') return null;
    if (decision.plan != null && typeof decision.plan !== 'string') return null;
    if (decision.feedback != null && typeof decision.feedback !== 'string') return null;
    return { requestID: decision.requestID, approved: decision.approved, plan: typeof decision.plan === 'string' ? decision.plan : '', feedback: typeof decision.feedback === 'string' ? decision.feedback : '' };
  } catch { return null; }
}

export const PlanDecisionMessage: React.FC<{ text: string }> = ({ text }) => {
  const decision = parsePlanDecision(text);
  if (!decision) return null;
  return (
    <article className="mb-4 rounded-2xl border border-[var(--border)] bg-[var(--card)] p-3.5 text-[var(--foreground)]">
      <details>
        <summary className="cursor-pointer font-semibold">计划 · {decision.approved ? '已批准' : '已驳回'}</summary>
        <div className="mt-3"><MarkdownView text={decision.plan} /></div>
      </details>
      {!decision.approved && decision.feedback && <p className="mt-2 whitespace-pre-wrap text-[var(--foreground-muted)]">{decision.feedback}</p>}
    </article>
  );
};
