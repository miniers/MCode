import React from 'react';
import type { RemoteSwarmBee, RemoteSwarmState } from '../types/swarm';
import type { SwarmEntryParsed, SwarmReportSection } from '../utils/markdown';
import { MarkdownView } from './MarkdownView';
import { SwarmAvatar, pickByHash } from './swarmAvatar';

/** 档位固定配色，其余档位按哈希取头像候选色 */
const TIER_COLORS: Record<string, string> = {
  read: '#30B0C7',
  edit: 'var(--primary)',
  deep: '#AF52DE'
};

const TASK_PREVIEW_LENGTH = 20;

function tierColor(tier: string): string {
  const key = tier.trim().toLowerCase();
  return TIER_COLORS[key] ?? pickByHash(key);
}

function truncateTask(task: string): string {
  const chars = Array.from(task.trim());
  return chars.length > TASK_PREVIEW_LENGTH ? `${chars.slice(0, TASK_PREVIEW_LENGTH).join('')}…` : chars.join('');
}

function formatDuration(ms: number): string {
  const totalSeconds = Math.max(0, Math.floor(ms / 1000));
  const seconds = totalSeconds % 60;
  const totalMinutes = Math.floor(totalSeconds / 60);
  if (totalMinutes >= 60) {
    return `${Math.floor(totalMinutes / 60)}小时${totalMinutes % 60}分`;
  }
  if (totalMinutes > 0) return `${totalMinutes}分${seconds}秒`;
  return `${seconds}秒`;
}

export function beeDurationMs(bee: RemoteSwarmBee, nowMs: number): number | null {
  const startedMs = Date.parse(bee.startedAt);
  if (Number.isNaN(startedMs)) return null;
  const finishedMs = bee.finishedAt ? Date.parse(bee.finishedAt) : Number.NaN;
  if (Number.isNaN(finishedMs) && bee.status !== 'running') return null;
  const endMs = Number.isNaN(finishedMs) ? nowMs : finishedMs;
  return Math.max(0, endMs - startedMs);
}

/** running 时每秒刷新一次，其他状态返回固定值，避免无意义定时器 */
function useNowMs(active: boolean): number {
  const [nowMs, setNowMs] = React.useState(() => Date.now());
  React.useEffect(() => {
    if (!active) return;
    setNowMs(Date.now());
    const timer = window.setInterval(() => setNowMs(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [active]);
  return nowMs;
}

/**
 * 状态标识：运行中是脉动蓝点；完成/失败是带白色对勾或叉的圆底标记，首次出现时缩放弹入一次；
 * 其余状态是灰点。弹入只走一次 transition，不做常驻循环动画，减少动效时直接落位。
 */
const StatusMark: React.FC<{ status?: string }> = ({ status }) => {
  const marked = status === 'completed' || status === 'failed';
  const [settled, setSettled] = React.useState(false);
  React.useEffect(() => {
    if (!marked) return;
    setSettled(false);
    const frame = window.requestAnimationFrame(() => setSettled(true));
    return () => window.cancelAnimationFrame(frame);
  }, [marked, status]);

  if (marked) {
    const completed = status === 'completed';
    return (
      <span
        className="flex h-3 w-3 shrink-0 items-center justify-center rounded-full transition-transform ease-out motion-reduce:transition-none"
        style={{
          backgroundColor: completed ? '#34C759' : '#FF3B30',
          transitionDuration: 'var(--mc-dur-snap)',
          transform: settled ? 'scale(1)' : 'scale(0.6)'
        }}
        aria-hidden="true"
      >
        <svg
          data-status={status}
          viewBox="0 0 12 12"
          className="h-2 w-2"
          fill="none"
          stroke="#FFFFFF"
          strokeWidth={2}
          strokeLinecap="round"
          strokeLinejoin="round"
        >
          {completed ? <path d="M2.6 6.2 4.9 8.5 9.4 3.9" /> : <path d="M3.6 3.6 8.4 8.4M8.4 3.6 3.6 8.4" />}
        </svg>
      </span>
    );
  }
  if (status === 'running') {
    return (
      <span
        className="h-1.5 w-1.5 shrink-0 rounded-full bg-[var(--primary)] animate-pulse motion-reduce:animate-none"
        aria-hidden="true"
      />
    );
  }
  return (
    <span
      className="h-1.5 w-1.5 shrink-0 rounded-full bg-[var(--foreground-muted)] opacity-60"
      aria-hidden="true"
    />
  );
};

/** 汇报标题里的中文状态文字映射到状态标识 */
function statusKeyOfReport(status: string): RemoteSwarmBee['status'] | undefined {
  switch (status.trim()) {
    case '运行中':
      return 'running';
    case '已完成':
      return 'completed';
    case '失败':
      return 'failed';
    case '已停止':
      return 'stopped';
    default:
      return undefined;
  }
}

/** 发起胶囊在映射里的 key：来源消息 id + 该消息内第几条派单 */
export function dispatchEntryKey(messageID: string, index: number): string {
  return `dispatch:${messageID}#${index}`;
}

/** 汇报段在映射里的 key：来源消息 id + 该消息内第几段汇报 */
export function reportSectionKey(messageID: string, index: number): string {
  return `report:${messageID}#${index}`;
}

/**
 * 时间线层预先算好的「发起胶囊 → 汇报」映射：由 ThreadTimeline 用 useMemo 构建一次，按引用传给各枚胶囊，
 * 避免每枚胶囊各自去扫整条时间线。
 */
export interface SwarmDispatchReportIndex {
  /** 发起胶囊 key → 已并入的汇报段，按时间线顺序 */
  reportsByEntry: Record<string, SwarmReportSection[]>;
  /** 已并入发起胶囊的汇报段 key：单独渲染汇报消息时跳过，避免同一个 bee 出现两行 */
  consumedReports: Record<string, boolean>;
}

/** 汇报胶囊的标题文字：`## 名字：任务 (状态)` 里的名字与状态组合成一句中文 */
function reportLabel(report: SwarmReportSection): string {
  const name = report.name?.trim() || '子代理';
  switch (report.status.trim()) {
    case '已完成':
      return `${name} 已完成工作`;
    case '失败':
      return `${name} 失败`;
    case '已停止':
      return `${name} 已停止`;
    case '运行中':
      return `${name} 运行中`;
    default:
      return report.task ? `${name} ${report.task}` : name;
  }
}

/**
 * 名字优先，其次任务文本（精确匹配，再做包含匹配）：在候选里找与 entry 对应的一项。
 * 派单胶囊找 bee、时间线把汇报段配到发起胶囊，走的都是这一套规则。
 */
export function matchCandidate<T extends { name?: string; task: string }>(
  entry: { name?: string; task?: string },
  candidates: readonly T[]
): T | null {
  const name = entry.name?.trim();
  if (name) {
    const byName = candidates.find((candidate) => candidate.name?.trim() === name);
    if (byName) return byName;
  }
  const task = entry.task?.trim();
  if (!task) return null;
  const exact = candidates.find((candidate) => candidate.task.trim() === task);
  if (exact) return exact;
  return (
    candidates.find((candidate) => {
      const candidateTask = candidate.task.trim();
      return candidateTask.length > 0 && (candidateTask.includes(task) || task.includes(candidateTask));
    }) ?? null
  );
}

/** 名字优先，其次任务文本（精确匹配，再做包含匹配） */
function matchBee(entry: { name?: string; task: string }, bees: RemoteSwarmBee[]): RemoteSwarmBee | null {
  return matchCandidate(entry, bees);
}

const capsuleClass =
  'inline-flex max-w-full items-center gap-1.5 rounded-full border border-[var(--border)] bg-[var(--card)] py-1 pl-1 pr-2 text-mc-caption leading-[16px] text-[var(--foreground)] transition hover:bg-[var(--card-alt,var(--card))] active:scale-[0.98]';

const tierTagClass = 'shrink-0 rounded-full px-1.5 py-[1px] text-mc-caption font-semibold';

export interface SwarmDispatchCapsulesProps {
  entries: SwarmEntryParsed[];
  /** 派单块仍在流式输出中 */
  pending?: boolean;
  /** 流式中已出现几条派单 */
  pendingCount?: number;
  /** 来源消息 id：与 reportIndex 一起定位并入本消息派单的汇报 */
  messageID?: string;
  /** 时间线层预算的发起 → 汇报映射；缺省时胶囊不并入汇报 */
  reportIndex?: SwarmDispatchReportIndex;
  swarmState?: RemoteSwarmState | null;
  onOpenBee?: (threadID: string) => void;
  onOpenBees?: () => void;
}

/**
 * 助手回复末尾的派单胶囊：每条派单一枚，状态、档位和耗时来自 swarmState.bees。
 * 已经有了汇报的 bee 把汇报并进发起胶囊：状态点按汇报状态兜底，点胶囊在原位展开汇报正文，
 * 时间线上不再单独多出一行汇报胶囊。
 * 匹配不到 bee 时（刚开始派发或已清理）仍然显示胶囊，点击改为打开子代理面板。
 */
export const SwarmDispatchCapsules: React.FC<SwarmDispatchCapsulesProps> = ({
  entries,
  pending = false,
  pendingCount = 0,
  messageID,
  reportIndex,
  swarmState = null,
  onOpenBee,
  onOpenBees
}) => {
  const bees = React.useMemo(() => swarmState?.bees ?? [], [swarmState]);
  const queued = swarmState?.queued ?? 0;
  const [expandedReports, setExpandedReports] = React.useState<Record<number, boolean>>({});
  const rows = React.useMemo(
    () =>
      entries.map((entry, index) => ({
        entry,
        bee: matchBee(entry, bees),
        reports:
          messageID && reportIndex ? reportIndex.reportsByEntry[dispatchEntryKey(messageID, index)] ?? [] : []
      })),
    [entries, bees, messageID, reportIndex]
  );
  const hasRunning = rows.some((row) => row.bee?.status === 'running');
  const nowMs = useNowMs(hasRunning);

  if (rows.length === 0 && !pending) return null;

  return (
    <div className="flex min-w-0 flex-wrap items-start gap-1.5">
      {rows.map(({ entry, bee, reports }, index) => {
        // 派单 entry 常不写 name，但已按任务匹配到 bee：优先用 bee 真名，与后台任务行对得上
        const name = bee?.name?.trim() || entry.name?.trim() || '';
        const displayName = name || '子代理';
        // 头像与着色种子：有真名用真名，否则退回任务文本，同一 bee 两处颜色与首字一致
        const tintSeed = name || entry.task?.trim() || '子代理';
        const tint = pickByHash(tintSeed);
        const tier = entry.tier?.trim() ?? '';
        const durationMs = bee ? beeDurationMs(bee, nowMs) : null;
        const duration = durationMs === null ? '' : formatDuration(durationMs);
        const waiting = !bee && queued > 0;
        const latestReport = reports.length > 0 ? reports[reports.length - 1] : null;
        const expandable = latestReport !== null;
        const isOpen = expandable && Boolean(expandedReports[index]);
        // 有了汇报就以汇报里的终态为准（bee 可能已被清理），没汇报时看 bee 当前状态
        const dotStatus = (latestReport ? statusKeyOfReport(latestReport.status) : undefined) ?? bee?.status;
        // 没有对应 bee 也没有排队：胶囊置灰但保留，方便用户知道派了什么；已完成的不再置灰
        const dimmed = !bee && queued === 0 && dotStatus !== 'completed';
        // 完成的胶囊用半透明淡绿底 + 浅绿边，一眼和运行中区分；内联 style 同时压住 hover 的灰底
        const completed = dotStatus === 'completed';
        const completedStyle = completed
          ? {
              backgroundColor: 'color-mix(in srgb, #34C759 12%, transparent)',
              borderColor: 'color-mix(in srgb, #34C759 30%, transparent)',
              transitionDuration: 'var(--mc-dur-base)'
            }
          : undefined;
        const openBee = () => {
          if (bee) onOpenBee?.(bee.threadID);
          else onOpenBees?.();
        };
        const toggleReports = () => setExpandedReports((prev) => ({ ...prev, [index]: !prev[index] }));

        return (
          // 外层包一层：展开的汇报正文跟胶囊同列，未展开时仍是一行胶囊
          <div
            key={`${entry.name ?? 'bee'}-${index}`}
            className={`min-w-0 max-w-full ${isOpen ? 'w-full' : ''}`}
          >
            <button
              type="button"
              onClick={expandable ? toggleReports : openBee}
              title={entry.task}
              aria-expanded={expandable ? isOpen : undefined}
              aria-label={`${displayName}：${entry.task}${duration ? ` · ${duration}` : ''}${
                expandable ? (isOpen ? ' · 收起汇报' : ' · 展开汇报') : ''
              }`}
              className={`${capsuleClass} ${dimmed ? 'opacity-60 grayscale' : ''}${
                completed ? ' motion-reduce:transition-none' : ''
              }`}
              style={completedStyle}
            >
              <SwarmAvatar name={tintSeed} tint={tint} />
              <span className="shrink-0 font-medium">{displayName}</span>
              <StatusMark status={dotStatus} />
              <span className="min-w-0 truncate text-[var(--foreground-muted)]">{truncateTask(entry.task)}</span>
              {waiting && <span className="shrink-0 text-mc-caption text-[var(--foreground-muted)]">排队中</span>}
              {tier && (
                <span
                  className={tierTagClass}
                  style={{
                    color: tierColor(tier),
                    backgroundColor: `color-mix(in srgb, ${tierColor(tier)} 15%, transparent)`
                  }}
                >
                  {tier}
                </span>
              )}
              {entry.readOnly && (
                <span className="shrink-0 text-mc-caption" title="只读档：改动不落回项目" aria-label="只读档">
                  🔒
                </span>
              )}
              {duration && (
                <span className="shrink-0 text-mc-caption tabular-nums text-[var(--foreground-muted)]">{duration}</span>
              )}
              {expandable && (
                <span className="shrink-0 text-mc-caption text-[var(--foreground-muted)]">{isOpen ? '收起' : '展开'}</span>
              )}
            </button>

            {isOpen && (
              <div className="mt-1.5 min-w-0 max-w-full overflow-hidden rounded-2xl border border-[var(--border)] bg-[var(--card)] p-3 text-mc-body text-[var(--foreground)]">
                {reports.map((report, sectionIndex) => (
                  <div key={`${report.name}-${sectionIndex}`} className={sectionIndex > 0 ? 'mt-2' : ''}>
                    <p className="text-mc-caption font-medium text-[var(--foreground-muted)]">{reportLabel(report)}</p>
                    {report.body.trim() ? (
                      <MarkdownView text={report.body} />
                    ) : (
                      <p className="text-[var(--foreground-muted)]">（无报告内容）</p>
                    )}
                  </div>
                ))}
                {bee && (
                  <button
                    type="button"
                    onClick={() => onOpenBee?.(bee.threadID)}
                    className="mt-2 min-h-[32px] rounded-lg border border-[var(--border)] bg-[var(--card)] px-2.5 text-xs font-medium text-[var(--foreground)] transition hover:bg-[var(--card-alt,var(--card))] active:scale-95"
                  >
                    打开子线程
                  </button>
                )}
              </div>
            )}
          </div>
        );
      })}

      {pending && (
        <span
          className="inline-flex items-center gap-1.5 rounded-full border border-dashed border-[var(--border)] bg-[var(--user-bubble)] px-2.5 py-1 text-mc-caption leading-[16px] text-[var(--foreground-muted)]"
          role="status"
        >
          <span className="h-3 w-3 shrink-0 animate-spin rounded-full border-2 border-[var(--foreground-muted)] border-t-transparent" />
          <span>{pendingCount > 0 ? `正在派发 ${pendingCount} 个子代理…` : '正在派发…'}</span>
        </span>
      )}
    </div>
  );
};

export interface SwarmReportCapsulesProps {
  reports: SwarmReportSection[];
  swarmState?: RemoteSwarmState | null;
  onOpenBee?: (threadID: string) => void;
}

export function swarmReportKey(report: SwarmReportSection, index: number): string {
  return report.name || report.task ? JSON.stringify([report.name, report.task]) : `index:${index}`;
}

/**
 * 子代理汇报胶囊：每个 bee 一枚，点击在下方展开该段汇报正文。
 * 汇报消息以用户消息形式写入，这里替掉默认的用户气泡。
 */
export const SwarmReportCapsules: React.FC<SwarmReportCapsulesProps> = ({
  reports,
  swarmState = null,
  onOpenBee
}) => {
  const [expanded, setExpanded] = React.useState<Record<string, boolean>>({});
  const bees = React.useMemo(() => swarmState?.bees ?? [], [swarmState]);
  const rows = React.useMemo(
    () => reports.map((report) => ({ report, bee: matchBee({ name: report.name, task: report.task }, bees) })),
    [reports, bees]
  );

  if (rows.length === 0) return null;

  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      {rows.map(({ report, bee }, index) => {
        const key = swarmReportKey(report, index);
        const isOpen = Boolean(expanded[key]);
        const displayName = report.name?.trim() || '子代理';
        const dotStatus = statusKeyOfReport(report.status) ?? bee?.status;

        return (
          <div key={key} className="min-w-0">
            <button
              type="button"
              onClick={() => setExpanded((prev) => ({ ...prev, [key]: !prev[key] }))}
              aria-expanded={isOpen}
              title={report.task}
              className={`${capsuleClass} ${bee ? '' : 'opacity-90'}`}
            >
              <SwarmAvatar name={displayName} tint={pickByHash(displayName)} />
              <StatusMark status={dotStatus} />
              <span className="min-w-0 truncate font-medium">{reportLabel(report)}</span>
              <span className="shrink-0 text-mc-caption text-[var(--foreground-muted)]">{isOpen ? '收起' : '展开'}</span>
            </button>

            {isOpen && (
              <div className="mt-1.5 min-w-0 max-w-full overflow-hidden rounded-2xl border border-[var(--border)] bg-[var(--card)] p-3 text-mc-body text-[var(--foreground)]">
                {report.body.trim() ? (
                  <MarkdownView text={report.body} />
                ) : (
                  <p className="text-[var(--foreground-muted)]">（无报告内容）</p>
                )}
                {bee && (
                  <button
                    type="button"
                    onClick={() => onOpenBee?.(bee.threadID)}
                    className="mt-2 min-h-[32px] rounded-lg border border-[var(--border)] bg-[var(--card)] px-2.5 text-xs font-medium text-[var(--foreground)] transition hover:bg-[var(--card-alt,var(--card))] active:scale-95"
                  >
                    打开子线程
                  </button>
                )}
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
};

export default SwarmDispatchCapsules;
