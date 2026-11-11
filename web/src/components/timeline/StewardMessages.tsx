import React from 'react';
import type { ThreadMessage } from '../ThreadTimeline';
import type { TaskItem } from '../../types/contract';
import { StewardTaskCard } from './StewardTaskCard';

export interface StewardDecisionOption {
  key: string;
  text: string;
}

export interface StewardDecision {
  id: string;
  task?: string;
  task_id?: string;
  thread?: string;
  question: string;
  options: StewardDecisionOption[];
  recommended?: string;
}

export interface StewardResult {
  task?: string;
  thread?: string;
  title: string;
  summary: string;
  status: 'done' | 'failed';
}

function objectValue(text: string): Record<string, unknown> | null {
  try {
    const value: unknown = JSON.parse(text);
    if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
    return value as Record<string, unknown>;
  } catch {
    return null;
  }
}

function optionalString(value: unknown): string | undefined {
  return typeof value === 'string' ? value : undefined;
}

export function parseStewardDecision(text: string): StewardDecision | null {
  const value = objectValue(text);
  if (!value || typeof value.id !== 'string' || value.id.trim() === '' ||
      typeof value.question !== 'string' || value.question.trim() === '') {
    return null;
  }
  if (value.options !== undefined && !Array.isArray(value.options)) return null;
  const options: StewardDecisionOption[] = [];
  for (const option of (value.options ?? []) as unknown[]) {
    if (!option || typeof option !== 'object' || Array.isArray(option)) return null;
    const record = option as Record<string, unknown>;
    if (typeof record.key !== 'string' || typeof record.text !== 'string') return null;
    options.push({ key: record.key, text: record.text });
  }
  if (value.recommended !== undefined && typeof value.recommended !== 'string') return null;
  for (const key of ['task', 'task_id', 'thread'] as const) {
    if (value[key] !== undefined && typeof value[key] !== 'string') return null;
  }
  return {
    id: value.id,
    task: optionalString(value.task),
    task_id: optionalString(value.task_id),
    thread: optionalString(value.thread),
    question: value.question,
    options,
    recommended: optionalString(value.recommended)
  };
}

export function parseStewardResult(text: string): StewardResult | null {
  const value = objectValue(text);
  if (!value || typeof value.title !== 'string' || typeof value.summary !== 'string' ||
      (value.status !== 'done' && value.status !== 'failed')) {
    return null;
  }
  for (const key of ['task', 'thread'] as const) {
    if (value[key] !== undefined && typeof value[key] !== 'string') return null;
  }
  return {
    task: optionalString(value.task),
    thread: optionalString(value.thread),
    title: value.title,
    summary: value.summary,
    status: value.status
  };
}

/** 取同一决策编号最后一条有效回答；后端允许追加改答记录。 */
export function latestDecisionAnswers(messages: ReadonlyArray<Pick<ThreadMessage, 'kind' | 'role' | 'text'>>): Record<string, string> {
  const answers: Record<string, string> = {};
  for (const message of messages) {
    if (message.kind !== 'steward_decision_answer' || (message.role !== 'system' && message.role !== 'user')) continue;
    const value = objectValue(message.text);
    if (!value || typeof value.id !== 'string' || value.id.trim() === '' || typeof value.answer !== 'string') continue;
    answers[value.id] = value.answer;
  }
  return answers;
}

export function decisionReplyText(id: string, key: string): string {
  return `${id} ${key}`;
}

export function sendDecisionAnswer(
  id: string,
  answer: string,
  onAnswer?: (id: string, answer: string) => void | Promise<unknown>,
  onSend?: (text: string) => void | Promise<unknown>
): void | Promise<unknown> {
  if (onAnswer) return onAnswer(id, answer);
  return onSend?.(decisionReplyText(id, answer));
}

export function submitDecisionAnswer(
  submitting: { current: boolean },
  answered: boolean,
  send: () => void | Promise<unknown>,
  onSuccess: () => void,
  onError: (error: unknown) => void,
  onFinally: () => void
): Promise<void> | undefined {
  if (submitting.current || answered) return;
  submitting.current = true;
  return Promise.resolve().then(send).then(onSuccess).catch((error: unknown) => {
    submitting.current = false;
    onError(error);
  }).finally(onFinally);
}

export interface StewardReplyProps {
  message: Pick<ThreadMessage, 'text'>;
}

export const StewardReply: React.FC<StewardReplyProps> = ({ message }) => (
  <p className="mb-4 min-w-0 max-w-full overflow-hidden text-ellipsis whitespace-nowrap text-sm text-[var(--foreground-muted)]">
    {message.text}
  </p>
);

export interface StewardDecisionCardProps {
  message: Pick<ThreadMessage, 'text'>;
  answer?: string;
  onSend?: (text: string) => void | Promise<unknown>;
  onAnswer?: (id: string, answer: string) => void | Promise<unknown>;
}

export const StewardDecisionCard: React.FC<StewardDecisionCardProps> = ({ message, answer, onSend, onAnswer }) => {
  const [error, setError] = React.useState('');
  const [submitting, setSubmitting] = React.useState(false);
  const [submitted, setSubmitted] = React.useState<string>();
  const submittingRef = React.useRef(false);
  const decision = parseStewardDecision(message.text);
  if (!decision) return null;
  const selected = answer ?? submitted;
  const answered = selected !== undefined;
  const selectedOption = decision.options.find((option) => option.key === selected);
  const send = (key: string) => {
    if (submittingRef.current || answered || (!onSend && !onAnswer)) return;
    setSubmitting(true);
    setError('');
    void submitDecisionAnswer(submittingRef, answered,
      () => sendDecisionAnswer(decision.id, key, onAnswer, onSend),
      () => setSubmitted(key),
      (err) => setError(err instanceof Error ? err.message : '回答失败'),
      () => setSubmitting(false));
  };

  return (
    <section className="mb-4 min-w-0 max-w-full rounded-2xl border border-[var(--border)] bg-[var(--card)] p-3.5 text-[var(--foreground)]">
      <div className="mb-3 flex min-w-0 items-start gap-2">
        <span className="shrink-0 text-sm font-semibold text-[var(--foreground-muted)]">{decision.id}</span>
        <p className="min-w-0 flex-1 text-sm leading-relaxed">{decision.question}</p>
      </div>
      {answered ? (
        <p className="text-sm text-[var(--foreground-muted)]">
          {selected === '忽略' ? '已忽略' : `已选 ${selected}${selectedOption ? ` · ${selectedOption.text}` : ''}`}
        </p>
      ) : (
        <div className="flex min-w-0 flex-col gap-2">
          {decision.options.map((option) => (
            <button
              key={option.key}
              type="button"
              disabled={submitting || (!onSend && !onAnswer)}
              onClick={() => send(option.key)}
              className="flex min-h-[40px] min-w-0 items-center gap-2 rounded-xl border border-[var(--border)] bg-[var(--card)] px-3 text-left text-sm text-[var(--foreground)] transition motion-reduce:transition-none hover:bg-[var(--card-alt,var(--card))] active:scale-[.99] disabled:cursor-not-allowed disabled:opacity-60"
            >
              <span className="min-w-0 flex-1 [overflow-wrap:anywhere]">{option.key} {option.text}</span>
              {option.key === decision.recommended && (
                <span className="shrink-0 text-xs text-[var(--foreground-muted)]">推荐</span>
              )}
            </button>
          ))}
          <button
            type="button"
            disabled={submitting || (!onSend && !onAnswer)}
            onClick={() => send('忽略')}
            className="mt-1 min-h-[32px] self-start rounded-lg px-2.5 text-sm text-[var(--foreground-muted)] transition motion-reduce:transition-none hover:bg-[var(--card-alt,var(--card))] disabled:cursor-not-allowed disabled:opacity-60"
          >
            忽略
          </button>
          {error && <p role="alert" className="text-sm text-[var(--foreground-muted)]">{error}</p>}
        </div>
      )}
    </section>
  );
};

export interface StewardResultMessageProps {
  message: Pick<ThreadMessage, 'text'>;
  onOpenThread?: (threadID: string) => void;
  task?: TaskItem;
}

export const StewardResultMessage: React.FC<StewardResultMessageProps> = ({ message, onOpenThread, task }) => {
  const result = parseStewardResult(message.text);
  if (!result) return null;
  const threadID = task?.threadID || result.thread?.trim();
  return (
    <div className="mb-4 min-w-0 max-w-full text-sm leading-relaxed text-[var(--foreground)]">
      <p>
        {result.title}{result.status === 'failed' ? ' 没能完成：' : '：'}{result.summary}
      </p>
      {threadID && (
        <div className="mt-1">
          <StewardTaskCard compact task={task ? { ...task, threadID } : { title: result.task || result.title, status: result.status, threadID }} onOpenThread={onOpenThread} />
        </div>
      )}
    </div>
  );
};
