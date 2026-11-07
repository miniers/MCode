import React from 'react';
import { CheckCircleIcon, EllipsisHorizontalIcon } from '../Icons';
import type { QuestionAnswerRecord } from '../../utils/questionAnswer';

// 已答复提问卡片：与桌面端 internal/ui/timeline_answered_question.go 的紧凑卡片对齐。
// 每题一行问题、一行答案；整次跳过时只显示一行「已跳过」。

interface QuestionAnswerCardProps {
  record: QuestionAnswerRecord;
}

export const QuestionAnswerCard: React.FC<QuestionAnswerCardProps> = ({ record }) => {
  return (
    <div className="mb-4 min-w-0 space-y-2.5 rounded-2xl border border-[var(--border)] bg-[var(--card)] p-3.5 text-[var(--foreground)]">
      {record.skipped ? (
        <div className="flex items-center gap-2 text-mc-body text-[var(--foreground-muted)]">
          <EllipsisHorizontalIcon className="h-3.5 w-3.5 shrink-0" />
          <span>已跳过</span>
        </div>
      ) : (
        <>
          <div className="flex items-center gap-2">
            <CheckCircleIcon className="h-3.5 w-3.5 shrink-0 text-[var(--primary)]" />
            <span className="text-mc-body font-semibold">提问</span>
          </div>
          {record.items.map((item, index) => (
            <div key={`${index}-${item.question}`} className="min-w-0 space-y-1">
              <div className="flex items-start gap-2 text-mc-body">
                <span className="shrink-0 font-semibold text-[var(--foreground-muted)]">问</span>
                <span className="min-w-0 break-words text-[var(--foreground-muted)]">
                  {item.question || '（无题目）'}
                </span>
              </div>
              <div className="flex items-start gap-2 text-mc-body">
                <span className="shrink-0 font-semibold text-[var(--primary)]">答</span>
                {item.answers.length === 0 ? (
                  <span className="min-w-0 italic text-[var(--foreground-muted)]">未回答</span>
                ) : item.answers.length === 1 ? (
                  <span className="min-w-0 break-words">{item.answers[0]}</span>
                ) : (
                  <ul className="min-w-0 list-disc space-y-0.5 pl-4">
                    {item.answers.map((answer, answerIndex) => (
                      <li key={`${answerIndex}-${answer}`} className="break-words">
                        {answer}
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            </div>
          ))}
        </>
      )}
    </div>
  );
};
