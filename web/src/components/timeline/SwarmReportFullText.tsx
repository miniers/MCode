// 被截断的子代理汇报：消息里只带总结的开头一段和一行截断提示，这里给「展开全文」按需从宿主存档读取全文。
// 截断判定与桌面端同源：internal/swarm/report_archive.go 在总结前加 ID 字段、在末尾加截断提示行，
// internal/ui/swarm.go 解析时把 ID 记进 BeeID、把提示行去掉并置 Truncated；
// 网页端拿不到结构化字段，只能按同样的前缀在汇报正文里识别。

import React from 'react';
import type { RemoteSwarmReportApi } from '../../context/remoteSwarmReportApi';
import {
  getCachedSwarmReportFull,
  setCachedSwarmReportFull,
  useRemoteSwarmReportApi
} from '../../context/remoteSwarmReportApi';
import { RpcError } from '../../services/rpc';
import type { SwarmReportSection } from '../../utils/markdown';
import { MarkdownView } from '../MarkdownView';

/** 与 internal/swarm/report_archive.go:18 的 ReportIDPrefix 一致：截断时随消息下发的 bee ID 字段 */
const SWARM_REPORT_ID_PREFIX = '- ID：';
const SWARM_REPORT_ID_PREFIX_ASCII = '- ID:';
/** 与 internal/swarm/report_archive.go:20 的 ReportTruncatedPrefix 一致：截断提示行的固定开头 */
const SWARM_REPORT_TRUNCATED_PREFIX = '（汇报总结已截断：';
/** 与 web/src/utils/markdown.ts 的汇报切段规则一致：行首最多 3 个空格的 `## ` 标题 */
const SWARM_REPORT_HEADING = /^ {0,3}##[ \t]+/;

/** 汇报里一段对应的存档字段：只有被截断的汇报才带 bee ID。 */
export interface SwarmReportArchiveFields {
  beeID: string;
  truncated: boolean;
}

/**
 * 按 `## ` 标题切段，逐段取 bee ID 与截断标记；结果顺序与 parseSwarmReport 的返回一一对应，
 * 调用方可以用同一下标把两者对上。
 */
export function parseSwarmReportArchiveFields(text: string): SwarmReportArchiveFields[] {
  const sections: SwarmReportArchiveFields[] = [];
  let current: SwarmReportArchiveFields | null = null;

  for (const line of String(text ?? '').split(/\r?\n/)) {
    if (SWARM_REPORT_HEADING.test(line)) {
      if (current) sections.push(current);
      current = { beeID: '', truncated: false };
      continue;
    }
    if (!current) continue;
    const trimmed = line.trim();
    if (current.beeID === '') {
      const idPrefix = trimmed.startsWith(SWARM_REPORT_ID_PREFIX)
        ? SWARM_REPORT_ID_PREFIX
        : trimmed.startsWith(SWARM_REPORT_ID_PREFIX_ASCII)
        ? SWARM_REPORT_ID_PREFIX_ASCII
        : null;
      if (idPrefix) current.beeID = trimmed.slice(idPrefix.length).trim();
      continue;
    }
    // 截断提示行只出现在带 ID 字段的条目里，和桌面端的判定一致
    if (trimmed.startsWith(SWARM_REPORT_TRUNCATED_PREFIX)) current.truncated = true;
  }
  if (current) sections.push(current);

  return sections;
}

/** 时间线上的一段汇报，带上从正文里识别出的存档字段。 */
export interface SwarmReportFullItem {
  report: SwarmReportSection;
  beeID: string;
  truncated: boolean;
}

/** 读取失败时的提示：存档里没有就直接显示宿主给的原文，其余按异常消息或通用文案兜底。 */
function describeReportFullError(err: unknown): string {
  // -32002 = 宿主找不到这份汇报的存档（internal/remote/wire/jsonrpc.go 的 ErrCodeResourceNotFound）
  if (err instanceof RpcError && err.code === -32002) {
    return err.message || '找不到这条子代理的完整汇报';
  }
  if (err instanceof Error && err.message) return err.message;
  return '读取汇报全文失败，请稍后重试';
}

interface SwarmReportFullRowProps {
  threadID: string;
  beeID: string;
  name: string;
  fetchReportFull: RemoteSwarmReportApi['fetchReportFull'];
}

/** 单条被截断汇报：未展开时是一个「展开全文」按钮，展开后正文整段换成存档里的全文。 */
const SwarmReportFullRow: React.FC<SwarmReportFullRowProps> = ({
  threadID,
  beeID,
  name,
  fetchReportFull
}) => {
  const [text, setText] = React.useState<string>(() => getCachedSwarmReportFull(threadID, beeID) ?? '');
  const [loading, setLoading] = React.useState(false);
  const [notice, setNotice] = React.useState('');
  const mountedRef = React.useRef(true);
  React.useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const load = React.useCallback(() => {
    setLoading(true);
    setNotice('');
    void Promise.resolve(fetchReportFull({ threadID, beeID }))
      .then((result) => {
        const full = typeof result?.text === 'string' ? result.text.trim() : '';
        if (!full) throw new Error('存档里没有这份汇报的全文');
        setCachedSwarmReportFull(threadID, beeID, full);
        if (!mountedRef.current) return;
        setText(full);
        if (result.truncated) setNotice('全文超过了单次返回上限，这里只显示前一部分');
      })
      .catch((err: unknown) => {
        if (!mountedRef.current) return;
        setNotice(describeReportFullError(err));
      })
      .finally(() => {
        if (mountedRef.current) setLoading(false);
      });
  }, [beeID, fetchReportFull, threadID]);

  const collapse = React.useCallback(() => {
    setText('');
    setNotice('');
  }, []);

  if (text) {
    return (
      <div className="min-w-0 max-w-full overflow-hidden rounded-2xl border border-[var(--border)] bg-[var(--card)] p-3 text-mc-body text-[var(--foreground)]">
        <div className="mb-1.5 flex items-center justify-between gap-2">
          <span className="text-mc-caption font-semibold text-[var(--foreground-muted)]">
            {`${name} 的完整汇报`}
          </span>
          <button
            type="button"
            onClick={collapse}
            className="min-h-[28px] shrink-0 rounded-full border border-[var(--border)] bg-[var(--card)] px-2.5 text-mc-caption font-medium text-[var(--foreground)] transition hover:bg-[var(--user-bubble)] active:scale-95"
          >
            收起全文
          </button>
        </div>
        <MarkdownView text={text} />
        {notice && <p className="mt-1.5 text-mc-caption text-[var(--foreground-muted)]">{notice}</p>}
      </div>
    );
  }

  return (
    <div className="flex min-w-0 flex-col gap-1">
      <div className="flex flex-wrap items-center gap-2">
        <button
          type="button"
          onClick={load}
          disabled={loading}
          title="从宿主存档读取这份汇报的全文"
          className="inline-flex min-h-[28px] shrink-0 items-center rounded-full border border-[var(--border)] bg-[var(--card)] px-2.5 text-mc-caption font-semibold text-[var(--foreground)] transition hover:bg-[var(--user-bubble)] active:scale-95 disabled:opacity-40"
        >
          {loading ? '读取中…' : '展开全文'}
        </button>
        <span className="text-mc-caption text-[var(--foreground-muted)]">{`${name} 的汇报被截断，仅显示开头一段`}</span>
      </div>
      {notice && <p className="text-mc-caption text-[var(--danger-foreground)]">{notice}</p>}
    </div>
  );
};

export interface SwarmReportFullTextProps {
  threadID: string;
  /** 时间线上这段汇报消息里的各段；只有被截断且带 bee ID 的才显示入口 */
  items: SwarmReportFullItem[];
}

/**
 * 汇报消息下方的全文入口：宿主没声明 threads.swarm.report 时整块不渲染，
 * 没有截断的汇报也不渲染，所以不传新能力时行为与旧版完全一致。
 */
export const SwarmReportFullText: React.FC<SwarmReportFullTextProps> = ({ threadID, items }) => {
  const { supportsReportFull, fetchReportFull } = useRemoteSwarmReportApi();
  if (!supportsReportFull) return null;
  const truncated = items.filter((item) => item.truncated && item.beeID);
  if (truncated.length === 0) return null;

  return (
    <div className="flex w-full min-w-0 flex-col gap-1.5">
      {truncated.map((item) => (
        <SwarmReportFullRow
          key={`${item.beeID}:${item.report.name}`}
          threadID={threadID}
          beeID={item.beeID}
          name={item.report.name?.trim() || '子代理'}
          fetchReportFull={fetchReportFull}
        />
      ))}
    </div>
  );
};

export default SwarmReportFullText;
