import React, { useEffect, useRef, useState } from 'react';
import type {
  RemoteRequest,
  RemoteRequestsRespondParams
} from '../types/contract';
import { ChevronDownIcon } from './Icons';
import { DiffViewer } from './DiffViewer';
import { MarkdownView } from './MarkdownView';
import { PlanReaderSheet } from './PlanReaderSheet';
import { haptic } from '../utils/haptic';

interface RemoteRequestPanelProps {
  threadID: string;
  requests: RemoteRequest[];
  isConnected: boolean;
  /** 宿主声明 requests.skip 才显示问题卡的“跳过”。 */
  skipSupported?: boolean;
  onRespond: (params: RemoteRequestsRespondParams) => Promise<void>;
  onRefresh: () => Promise<void>;
}

const approvalKindLabels: Record<string, string> = {
  command: '需要授权',
  fileChange: '需要授权',
  tool: '需要授权',
  permissions: '需要授权',
  plan: '计划确认',
  commit: '提交确认'
};

// 百分比会按包裹层（即卡片自身）的高度解析，不能用作视口高度限制。
export function requestPanelMaxHeight(keyboardOpen: boolean): string {
  return keyboardOpen ? 'calc(var(--app-height, 100dvh) * 0.45)' : 'calc(var(--app-height, 100dvh) * 0.6)';
}

function requestLabel(request: RemoteRequest): string {
  if (request.reason === 'credential') return '读取凭证';
  if (request.reason === 'controlPlane') return '改动 mcode 数据';
  return request.kind === 'question' ? '需要回答' : approvalKindLabels[request.kind] ?? '需要授权';
}

/** 上次答复未送达提示；没有送达错误时返回空串，与桌面端 approvalDeliveryNotice 保持一致 */
function deliveryNotice(deliveryError?: string | null): string {
  const reason = (deliveryError ?? '').trim();
  if (!reason) return '';
  return `上次答复未送达：${reason}，可重新选择`;
}

function errorMessage(error: unknown): string {
  if (error && typeof error === 'object' && 'code' in error && (error as { code?: unknown }).code === -32010) {
    return '请求已处理，正在刷新当前状态。';
  }
  return error instanceof Error ? error.message : '请求答复失败，请刷新后重试';
}

export function planFeedbackPayload(options: RemoteRequest['options'], text: string): { optionID: string; answers: { feedback: string[] } } | null {
  const feedback = text.trim();
  if (!feedback) return null;
  const decline = options.find((option) => option.role === 'decline') ?? options.find((option) => option.title.includes('拒绝')) ?? options[options.length - 1];
  return decline ? { optionID: decline.id, answers: { feedback: [feedback] } } : null;
}

/**
 * 多条待处理请求时只展开一条：记住的当前项还在列表里就保持，
 * 否则回到第一条；列表为空返回 null。
 */
export function pickActiveRequestId(
  requests: readonly Pick<RemoteRequest, 'requestID'>[],
  currentID: string | null
): string | null {
  if (currentID && requests.some((request) => request.requestID === currentID)) return currentID;
  return requests.length > 0 ? requests[0].requestID : null;
}

export function approvalShortcut(
  event: Pick<KeyboardEvent, 'key' | 'defaultPrevented' | 'repeat' | 'metaKey' | 'ctrlKey' | 'altKey' | 'shiftKey' | 'isComposing' | 'keyCode'>,
  targetTag: string,
  isContentEditable: boolean,
  hasOtherDialog: boolean,
  isInteractiveTarget: boolean
): 'allow' | 'reject' | null {
  if (event.defaultPrevented || event.repeat || event.metaKey || event.ctrlKey || event.altKey || event.shiftKey ||
      event.isComposing || event.keyCode === 229 || isContentEditable || hasOtherDialog || isInteractiveTarget ||
      ['input', 'textarea', 'select'].includes(targetTag.toLowerCase())) return null;
  if (event.key === 'Enter') return 'allow';
  if (event.key === 'Escape') return 'reject';
  return null;
}

/** 键盘弹起判定：全站唯一来源是 main.tsx 写在 documentElement 上的 data-keyboard-open */
function useKeyboardOpen(): boolean {
  const [keyboardOpen, setKeyboardOpen] = useState(
    () => typeof document !== 'undefined' && document.documentElement.hasAttribute('data-keyboard-open')
  );

  useEffect(() => {
    const root = document.documentElement;
    const update = (): void => setKeyboardOpen(root.hasAttribute('data-keyboard-open'));
    update();
    const observer = new MutationObserver(update);
    observer.observe(root, { attributes: true, attributeFilter: ['data-keyboard-open'] });
    return () => observer.disconnect();
  }, []);

  return keyboardOpen;
}

/** 选项按钮组：审批卡与计划阅读弹层共用同一份样式与回调 */
const OptionButtonRow: React.FC<{
  options: RemoteRequest['options'];
  busy: boolean;
  disabled: boolean;
  onSelect: (optionID: string) => void;
}> = ({ options, busy, disabled, onSelect }) => (
  <div className="flex flex-wrap gap-2.5 pt-1">
    {options.map((option) => {
      const isApprove = option.role === 'approve';
      const isDanger = option.role === 'decline' || option.role === 'cancel';

      return (
        <button
          key={option.id}
          type="button"
          disabled={disabled}
          onClick={() => onSelect(option.id)}
          className={`items-center justify-center rounded-mc-lg px-3.5 py-3 text-sm transition active:opacity-70 disabled:opacity-50 ${
            isApprove
              ? 'bg-[var(--primary)] text-white font-extrabold'
              : isDanger
              ? 'bg-[var(--subtle-strong,var(--user-bubble))] text-[var(--danger-foreground)] font-bold'
              : 'bg-[var(--subtle-strong,var(--user-bubble))] text-[var(--foreground)] font-bold'
          }`}
        >
          {busy ? '处理中…' : option.title}
        </button>
      );
    })}
  </div>
);

export const RemoteRequestPanel: React.FC<RemoteRequestPanelProps> = ({
  threadID,
  requests,
  isConnected,
  skipSupported = false,
  onRespond,
  onRefresh
}) => {
  const [busyRequestID, setBusyRequestID] = useState<string | null>(null);
  const [error, setError] = useState<{ requestID: string; message: string } | null>(null);
  const [answers, setAnswers] = useState<Record<string, Record<string, string[]>>>({});
  const [otherAnswers, setOtherAnswers] = useState<Record<string, Record<string, string>>>({});
  const [planFeedback, setPlanFeedback] = useState<Record<string, string>>({});
  const [staleRequestIDs, setStaleRequestIDs] = useState<Set<string>>(() => new Set());
  const [handledRequestIDs, setHandledRequestIDs] = useState<Set<string>>(() => new Set());
  const [expandedCommandIDs, setExpandedCommandIDs] = useState<Set<string>>(() => new Set());
  // 计划全文阅读弹层：只存 requestID，请求被答复/移除后弹层自然关闭
  const [planReaderRequestID, setPlanReaderRequestID] = useState<string | null>(null);
  // 每张问题卡一次只展开一道；默认展开第一道未答的，其余折叠成一行。
  const [openQuestionKeys, setOpenQuestionKeys] = useState<Record<string, string>>(() =>
    Object.fromEntries(requests.map((request) => [request.requestID, request.questions.find((question) => question.id)?.id ?? '']))
  );

  // 多条审批折叠成 1/N：只记 id，不记下标；当前项消失后由 pickActiveRequestId 回落到第一条
  const [selectedRequestID, setSelectedRequestID] = useState<string | null>(null);
  const activeRequestID = pickActiveRequestId(requests, selectedRequestID);
  const activeRequestIndex = requests.findIndex((request) => request.requestID === activeRequestID);
  const keyboardOpen = useKeyboardOpen();
  const scrollAreaRef = useRef<HTMLElement | null>(null);

  useEffect(() => {
    // 切换当前请求后把新的卡滚回可见区域（卡只有一张，回到滚动区顶部即可）
    if (requests.length < 2) return;
    scrollAreaRef.current?.scrollTo({ top: 0 });
  }, [activeRequestID, requests.length]);

  useEffect(() => {
    const validIDs = new Set(requests.map((request) => request.requestID));
    setAnswers((previous) => Object.fromEntries(Object.entries(previous).filter(([id]) => validIDs.has(id))));
    setOtherAnswers((previous) => Object.fromEntries(Object.entries(previous).filter(([id]) => validIDs.has(id))));
    setError((previous) => previous && validIDs.has(previous.requestID) ? previous : null);
    setStaleRequestIDs((previous) => new Set([...previous].filter((id) => validIDs.has(id))));
    setHandledRequestIDs((previous) => new Set([...previous].filter((id) => validIDs.has(id))));
    setOpenQuestionKeys((previous) => Object.fromEntries(requests.map((request) => {
      const stored = previous[request.requestID];
      if (stored !== undefined && (stored === '' || request.questions.some((question) => question.id === stored))) {
        return [request.requestID, stored];
      }
      return [request.requestID, request.questions.find((question) => question.id)?.id ?? ''];
    })));
  }, [requests]);

  const toggleCommandExpand = (requestID: string) => {
    setExpandedCommandIDs((prev) => {
      const next = new Set(prev);
      if (next.has(requestID)) next.delete(requestID);
      else next.add(requestID);
      return next;
    });
  };

  const submit = async (request: RemoteRequest, payload: Omit<RemoteRequestsRespondParams, 'threadID' | 'requestID' | 'turnID' | 'runID'>): Promise<void> => {
    if (!isConnected || busyRequestID || !request.turnID || staleRequestIDs.has(request.requestID) || handledRequestIDs.has(request.requestID)) return;
    setBusyRequestID(request.requestID);
    setError(null);
    try {
      await onRespond({
        threadID,
        requestID: request.requestID,
        turnID: request.turnID,
        runID: request.runID,
        ...payload
      });
      haptic('light');
      setHandledRequestIDs((previous) => new Set(previous).add(request.requestID));
      await onRefresh();
    } catch (err) {
      if (err && typeof err === 'object' && 'code' in err && (err as { code?: unknown }).code === -32010) {
        setStaleRequestIDs((previous) => new Set(previous).add(request.requestID));
        setError({ requestID: request.requestID, message: '请求已过期或已被其他设备处理，请在 Mac 端确认。' });
        await onRefresh().catch(() => {});
      } else {
        setError({ requestID: request.requestID, message: errorMessage(err) });
      }
    } finally {
      setBusyRequestID(null);
    }
  };

  useEffect(() => {
    const request = requests.find((candidate) => candidate.requestID === activeRequestID);
    if (!request || (request.kind !== 'command' && request.kind !== 'fileChange')) return;
    const allow = request.options.filter((option) => option.role === 'approve');
    const reject = request.options.filter((option) => option.role === 'decline');
    if (allow.length !== 1 || reject.length !== 1) return;

    const onKeyDown = (event: KeyboardEvent): void => {
      const target = event.target instanceof Element ? event.target : null;
      const editable = target instanceof HTMLElement && target.isContentEditable;
      const control = target?.closest('input, textarea, select, button, a[href], summary, [role="button"], [role="menuitem"], [role="option"], [role="tab"], [role="checkbox"], [role="radio"], [role="switch"]');
      const panel = scrollAreaRef.current;
      const hasOtherDialog = Array.from(document.querySelectorAll('[role="dialog"]')).some(
        (dialog) => !panel?.contains(dialog) && !dialog.contains(panel)
      );
      const action = approvalShortcut(event, control?.tagName ?? target?.tagName ?? '', editable, hasOtherDialog, !!control);
      if (!action) return;
      event.preventDefault();
      void submit(request, { optionID: action === 'allow' ? allow[0].id : reject[0].id });
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  });

  const toggleQuestion = (requestID: string, questionID: string): void => {
    setOpenQuestionKeys((previous) => ({ ...previous, [requestID]: previous[requestID] === questionID ? '' : questionID }));
  };

  const setChoice = (requestID: string, questionID: string, label: string, allowsMultiple: boolean): void => {
    let next: string[] = [];
    setAnswers((previous) => {
      const requestAnswers = previous[requestID] ?? {};
      const selected = requestAnswers[questionID] ?? [];
      next = allowsMultiple
        ? selected.includes(label) ? selected.filter((value) => value !== label) : [...selected, label]
        : [label];
      return { ...previous, [requestID]: { ...requestAnswers, [questionID]: next } };
    });
    // 单选即选定：折叠当前这道，展开下一道未答的，问题多时不用来回滚。
    if (!allowsMultiple && next.length > 0) {
      setOpenQuestionKeys((previous) => {
        const request = requests.find((candidate) => candidate.requestID === requestID);
        if (!request || request.questions.length < 2) return previous;
        const currentAnswers = answers[requestID] ?? {};
        const nextUnanswered = request.questions.find((question) => {
          if (question.id === questionID) return false;
          if ((currentAnswers[question.id] ?? []).length > 0) return false;
          return !(otherAnswers[requestID]?.[question.id]?.trim());
        });
        return { ...previous, [requestID]: nextUnanswered?.id ?? '' };
      });
    }
  };

  const setOther = (requestID: string, questionID: string, value: string): void => {
    setOtherAnswers((previous) => ({
      ...previous,
      [requestID]: { ...(previous[requestID] ?? {}), [questionID]: value }
    }));
  };

  const questionAnswerMap = (request: RemoteRequest): Record<string, string[]> => {
    const requestAnswers = answers[request.requestID] ?? {};
    const requestOther = otherAnswers[request.requestID] ?? {};
    return Object.fromEntries(request.questions.map((question) => {
      const selected = requestAnswers[question.id] ?? [];
      const other = requestOther[question.id]?.trim() ?? '';
      const values = question.allowsOther && other
        ? question.allowsMultiple ? [...selected, other] : [other]
        : selected;
      return [question.id, values];
    }));
  };

  const canSubmitQuestion = (request: RemoteRequest): boolean => {
    const map = questionAnswerMap(request);
    return request.questions.every((question) => (map[question.id]?.length ?? 0) > 0);
  };

  if (requests.length === 0) return null;

  const planReaderRequest = planReaderRequestID
    ? requests.find((request) => request.requestID === planReaderRequestID) ?? null
    : null;

  return (
    <>
      <section
        ref={scrollAreaRef}
        className="overflow-y-auto overscroll-contain px-3 sm:px-4 py-2 space-y-3"
        style={{ maxHeight: requestPanelMaxHeight(keyboardOpen) }}
        aria-label="待处理请求"
      >
      {requests.filter((request) => request.requestID === activeRequestID).map((request) => {
        const busy = busyRequestID === request.requestID;
        const requestError = error?.requestID === request.requestID ? error.message : null;
        const stale = staleRequestIDs.has(request.requestID) || handledRequestIDs.has(request.requestID) || !request.turnID;
        const secretOverHTTP = typeof window !== 'undefined' && window.location.protocol !== 'https:' &&
          request.kind === 'question' && request.questions.some((question) => question.isSecret);
        const isCommandExpanded = expandedCommandIDs.has(request.requestID);
        const isDetailLong = (request.detail?.length ?? 0) > 120;
        const deliveryNoticeText = deliveryNotice(request.deliveryError);
        const reason = request.reason === 'credential' || request.reason === 'controlPlane' ? '' : (request.reason ?? '').trim();
        const feedback = planFeedbackPayload(request.options, planFeedback[request.requestID] ?? '');

        return (
          <article
            key={request.requestID}
            className="rounded-mc-xl border border-[var(--border)] bg-[var(--card-alt,var(--card))] p-4 shadow-sm space-y-3"
          >
            <div className="flex items-start justify-between gap-2">
              <div className="text-mc-caption font-bold uppercase tracking-[1.1px] text-[var(--foreground-muted)]">
                {requestLabel(request)}
              </div>
              {requests.length > 1 && (
                <span className="shrink-0 rounded-full border border-[var(--border)] bg-[var(--user-bubble)] px-2 py-0.5 text-mc-caption font-bold tabular-nums text-[var(--foreground-muted)]">
                  {activeRequestIndex + 1}/{requests.length}
                </span>
              )}
            </div>

            {request.title && (
              <h2 className="text-lg font-bold text-[var(--foreground)] break-words leading-snug">
                {request.title}
              </h2>
            )}

            {/* fileChange 带 diff 时直接渲染差异，不再把 detail 当纯文本铺开 */}
            {request.kind === 'fileChange' && request.diff?.trim() ? (
              <div className="max-h-80 overflow-y-auto overflow-x-auto rounded-xl border border-[var(--border)] bg-[var(--card)] p-3">
                <DiffViewer diffText={request.diff} readOnly defaultCollapsedWhenOver={200} />
              </div>
            ) : request.kind === 'plan' && request.detail ? (
              /* 计划正文往往很长，卡片只给 Markdown 摘要 + 渐隐，完整计划放到阅读弹层里看 */
              <div className="space-y-2">
                <div className="relative max-h-[276px] overflow-hidden rounded-xl border border-[var(--border)] bg-[var(--card)] p-3">
                  <MarkdownView text={request.detail} />
                  <div
                    className="pointer-events-none absolute inset-x-0 bottom-0 h-16 bg-gradient-to-t from-[var(--card)] to-transparent"
                    aria-hidden="true"
                  />
                </div>
                <button
                  type="button"
                  onClick={() => setPlanReaderRequestID(request.requestID)}
                  className="inline-flex min-h-[36px] items-center gap-1.5 rounded-mc-lg border border-[var(--border)] bg-[var(--subtle-strong,var(--user-bubble))] px-3 text-xs font-semibold text-[var(--foreground)] transition active:opacity-70"
                >
                  <span>阅读完整计划</span>
                  <ChevronDownIcon className="h-3.5 w-3.5" />
                </button>
              </div>
            ) : request.detail && (
              <div className="space-y-1.5">
                <div
                  className={`font-mono text-xs p-3 rounded-xl bg-[var(--user-bubble)] border border-[var(--border)] text-[var(--foreground)] select-text break-all whitespace-pre-wrap overflow-x-auto ${
                    isDetailLong && !isCommandExpanded ? 'max-h-24 overflow-hidden' : 'max-h-60'
                  }`}
                >
                  {request.detail}
                </div>
                {isDetailLong && (
                  <button
                    type="button"
                    onClick={() => toggleCommandExpand(request.requestID)}
                    className="text-xs text-[var(--primary)] hover:underline"
                  >
                    {isCommandExpanded ? '收起详情' : '展开完整命令/详情'}
                  </button>
                )}
              </div>
            )}

            {reason && (
              <p className="whitespace-pre-wrap break-words text-sm text-[var(--foreground-muted)] leading-relaxed">
                {reason}
              </p>
            )}

            {deliveryNoticeText && (
              <p role="status" className="text-xs text-[var(--danger-foreground)] break-words">
                {deliveryNoticeText}
              </p>
            )}

            {request.kind === 'plan' && <textarea value={planFeedback[request.requestID] ?? ''} onChange={(event) => setPlanFeedback((previous) => ({ ...previous, [request.requestID]: event.target.value }))} placeholder="写修改意见…" disabled={busy || stale || !isConnected} className="min-h-[44px] w-full rounded-mc-lg border border-[var(--input-border)] bg-[var(--card)] p-3 text-sm text-[var(--foreground)] placeholder:text-[var(--foreground-muted)]" />}
            {request.kind === 'plan' && feedback ? (
              <button type="button" disabled={busy || stale || !isConnected} onClick={() => void submit(request, feedback)} className="min-h-[44px] w-full rounded-mc-lg bg-[var(--primary)] px-4 text-sm font-bold text-white disabled:opacity-50">{busy ? '提交中…' : '提交修改意见'}</button>
            ) : request.kind !== 'question' ? (
              <OptionButtonRow
                options={request.options}
                busy={busy}
                disabled={busy || stale || !isConnected}
                onSelect={(optionID) => void submit(request, { optionID })}
              />
            ) : (
              <div className="space-y-4 pt-1">
                {secretOverHTTP && (
                  <p className="text-xs text-[var(--warning-foreground)]">
                    此问题包含 secret，HTTP 页面不会提交，请在 Mac 端回答。
                  </p>
                )}
                <div className="space-y-2">
                  {request.questions.map((question) => {
                    const selected = answers[request.requestID]?.[question.id] ?? [];
                    const other = otherAnswers[request.requestID]?.[question.id] ?? '';
                    const answered = selected.length > 0 || other.trim().length > 0;
                    const isOpen = openQuestionKeys[request.requestID] === question.id;
                    const answerSummary = question.isSecret ? '已填写'
                      : selected.length > 0 ? selected.join('、')
                      : other.trim() ? '其他' : '';
                    return (
                      <div
                        key={question.id}
                        className={`rounded-mc-lg border transition ${
                          isOpen ? 'border-[var(--border)] bg-[var(--card)]' : 'border-[var(--border-subtle,var(--border))] bg-[var(--subtle-strong,var(--user-bubble))]'
                        }`}
                      >
                        <button
                          type="button"
                          onClick={() => toggleQuestion(request.requestID, question.id)}
                          aria-expanded={isOpen}
                          aria-controls={`${request.requestID}-${question.id}-body`}
                          className="flex w-full items-center gap-2.5 px-3 py-2.5 text-left"
                        >
                          <span className="min-w-0 flex-1">
                            <span className={`block truncate text-sm ${answered ? 'text-[var(--foreground-muted)]' : 'font-semibold text-[var(--foreground)]'}`}>
                              {question.header || question.prompt}
                            </span>
                            {!isOpen && answered && (
                              <span className="mt-0.5 flex items-center gap-1.5 text-xs text-[var(--primary)]">
                                <span className="shrink-0">✓</span>
                                {answerSummary && <span className="min-w-0 truncate">{answerSummary}</span>}
                              </span>
                            )}
                          </span>
                          <ChevronDownIcon className={`h-4 w-4 shrink-0 text-[var(--foreground-muted)] transition-transform ${isOpen ? 'rotate-180' : ''}`} />
                        </button>

                        {isOpen && (
                          <div id={`${request.requestID}-${question.id}-body`} className="space-y-2.5 border-t border-[var(--border)] px-3 pb-3 pt-3">
                            {question.prompt && question.header && (
                              <p className="text-xs text-[var(--foreground-muted)] break-words">{question.prompt}</p>
                            )}
                            <div className="space-y-2">
                              {question.choices.map((choice) => {
                                const checked = selected.includes(choice.label);
                                return (
                                  <label
                                    key={choice.label}
                                    className={`flex cursor-pointer items-start gap-3 rounded-mc-lg border p-3 transition ${
                                      checked
                                        ? 'border-[var(--primary)] bg-[var(--user-bubble)]'
                                        : 'border-[var(--border)] bg-[var(--card)] hover:bg-[var(--card-alt,var(--card))]'
                                    }`}
                                  >
                                    <input
                                      type={question.allowsMultiple ? 'checkbox' : 'radio'}
                                      name={`${request.requestID}-${question.id}`}
                                      checked={checked}
                                      disabled={busy || stale || secretOverHTTP || !isConnected}
                                      onChange={(event) => {
                                        setChoice(request.requestID, question.id, choice.label, question.allowsMultiple);
                                        if (!question.allowsMultiple) event.currentTarget.blur();
                                      }}
                                      className="mt-0.5 accent-[var(--primary)]"
                                    />
                                    <div className="min-w-0 flex-1">
                                      <span className="block text-sm font-medium text-[var(--foreground)] break-words">
                                        {choice.label}
                                      </span>
                                      {choice.detail && (
                                        <span className="mt-0.5 block text-xs text-[var(--foreground-muted)] break-words">
                                          {choice.detail}
                                        </span>
                                      )}
                                    </div>
                                  </label>
                                );
                              })}
                            </div>
                            {question.allowsOther && (
                              <input
                                type={question.isSecret ? 'password' : 'text'}
                                value={other}
                                disabled={busy || stale || secretOverHTTP || !isConnected}
                                onChange={(event) => setOther(request.requestID, question.id, event.target.value)}
                                placeholder="其他"
                                className="min-h-[44px] w-full rounded-mc-lg border border-[var(--input-border)] bg-[var(--card)] px-3 text-sm text-[var(--foreground)] outline-none focus:border-[var(--primary)] placeholder:text-[var(--foreground-muted)]"
                              />
                            )}
                          </div>
                        )}
                      </div>
                    );
                  })}
                </div>
                <button
                  type="button"
                  disabled={busy || stale || secretOverHTTP || !isConnected || !canSubmitQuestion(request)}
                  onClick={() => void submit(request, { answers: questionAnswerMap(request) })}
                  className="min-h-[44px] w-full rounded-mc-lg bg-[var(--primary)] px-4 py-2 text-sm font-extrabold text-white transition active:opacity-70 disabled:opacity-50"
                >
                  {busy ? '提交中…' : '提交回答'}
                </button>
                {/* 跳过：不答直接放行；secret 问题也可跳过（不涉及任何输入内容） */}
                {skipSupported && (
                  <div className="space-y-1">
                    <button
                      type="button"
                      disabled={busy || stale || !isConnected}
                      onClick={() => void submit(request, { skip: true })}
                      className="min-h-[40px] w-full rounded-mc-lg border border-[var(--border)] bg-[var(--subtle-strong,var(--user-bubble))] px-4 py-2 text-sm font-semibold text-[var(--foreground)] transition active:opacity-70 disabled:opacity-50"
                    >
                      {busy ? '处理中…' : '跳过'}
                    </button>
                    <p className="text-center text-mc-caption text-[var(--foreground-muted)]">
                      跳过后 agent 将在没有该回答的情况下继续
                    </p>
                  </div>
                )}
              </div>
            )}

            {!request.turnID && (
              <p className="text-xs text-[var(--danger-foreground)]">
                请求缺少有效轮次，已无法答复，请刷新状态。
              </p>
            )}
            {handledRequestIDs.has(request.requestID) && (
              <p className="text-xs text-[var(--primary)]">
                已提交，正在刷新当前状态。
              </p>
            )}
            {staleRequestIDs.has(request.requestID) && (
              <p className="text-xs text-[var(--warning-foreground)]">
                请求已过期或已被其他设备抢答，按钮已禁用。
              </p>
            )}
            {requestError && (
              <p role="alert" className="text-xs text-[var(--danger-foreground)]">
                {requestError}
              </p>
            )}
          </article>
        );
      })}
      </section>

      {requests.length > 1 && (
        <button
          type="button"
          onClick={() => {
            const next = requests[(activeRequestIndex + 1) % requests.length];
            if (next) setSelectedRequestID(next.requestID);
          }}
          className="z-10 mx-3 shrink-0 rounded-mc-lg border border-[var(--border)] bg-[var(--subtle-strong,var(--user-bubble))] px-3 py-2.5 text-xs font-semibold text-[var(--foreground)] transition active:opacity-70 sm:mx-4"
        >
          还有 {requests.length - 1} 条待处理
        </button>
      )}

      {planReaderRequest && planReaderRequest.detail ? (
        <PlanReaderSheet
          title={planReaderRequest.title ?? '完整计划'}
          text={planReaderRequest.detail}
          onClose={() => setPlanReaderRequestID(null)}
          actions={
            <OptionButtonRow
              options={planReaderRequest.options}
              busy={busyRequestID === planReaderRequest.requestID}
              disabled={
                busyRequestID === planReaderRequest.requestID ||
                staleRequestIDs.has(planReaderRequest.requestID) ||
                handledRequestIDs.has(planReaderRequest.requestID) ||
                !planReaderRequest.turnID ||
                !isConnected
              }
              onSelect={(optionID) => void submit(planReaderRequest, { optionID })}
            />
          }
        />
      ) : null}
    </>
  );
};
