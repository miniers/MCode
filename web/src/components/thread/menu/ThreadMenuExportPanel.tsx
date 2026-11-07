// 会话更多操作面板的「导出会话」子页：选格式下载整段会话历史，并说明超长内容的降级策略。
// 导出/复制的状态与回调留在 ThreadMenuSheet，因为「复制为 Markdown」在主页面分享组里。

import type { ThreadExportFormat } from '../../../utils/threadExport';

// 导出会话的格式选项：与桌面端 /export 命令的 md / html / json 三种格式对齐
const THREAD_EXPORT_FORMATS: Array<{ format: ThreadExportFormat; label: string }> = [
  { format: 'md', label: 'Markdown' },
  { format: 'html', label: 'HTML' },
  { format: 'json', label: 'JSON' }
];

export interface ThreadMenuExportPanelProps {
  isExporting: boolean;
  isCopyingMarkdown: boolean;
  handleExportThread: (format: ThreadExportFormat) => void | Promise<void>;
  /** 只有失败提示留在导出页，成功提示回主页面显示 */
  exportNotice: { kind: 'ok' | 'error'; text: string } | null;
}

export function ThreadMenuExportPanel({
  isExporting,
  isCopyingMarkdown,
  handleExportThread,
  exportNotice
}: ThreadMenuExportPanelProps) {
  const busy = isExporting || isCopyingMarkdown;
  const exportError = exportNotice?.kind === 'error' ? exportNotice.text : null;

  return (
    <div className="mt-4">
      <div className="grid grid-cols-3 gap-1.5">
        {THREAD_EXPORT_FORMATS.map((option) => (
          <button
            key={option.format}
            type="button"
            onClick={() => void handleExportThread(option.format)}
            disabled={busy}
            className="min-h-[44px] rounded-xl border border-[var(--input-border)] bg-[var(--screen)] text-xs font-semibold text-[var(--foreground)] disabled:opacity-40 transition active:scale-95"
          >
            {option.label}
          </button>
        ))}
      </div>
      <p className="mt-2 text-mc-caption leading-relaxed text-[var(--foreground-muted)]">
        {isExporting
          ? '正在拉取完整历史并生成文件…'
          : '会先拉取完整历史再下载；超长会话按 150k 字符预算自动降级。'}
      </p>
      {exportError && (
        <p className="mt-1.5 text-mc-caption text-[var(--danger-foreground)]" role="alert">
          {exportError}
        </p>
      )}
    </div>
  );
}
