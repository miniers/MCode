import React from 'react';
import { ChevronRightIcon, CodeBracketIcon } from '../Icons';
import { SwarmAvatar, pickByHash } from '../swarmAvatar';
import type { RemoteSwarmBee } from '../../types/swarm';
import type { useRemoteBackgroundTaskApi } from '../../context/remoteBackgroundTaskApi';

// 时间线末尾的后台任务聚合行，两种形态：
// - 会话在跑：显示「后台 · N 项任务 · M 个子智能体」，统计回合已结束但仍在跑的命令与运行中的 bee；
// - 会话空闲：显示「等待 N 个后台任务 · 任务名 · 1:05」，带秒级计时，告诉用户还在等什么。
// provider 托管的后台任务（toolStatus='background'）也在列，可查看日志并停止。
// 拿不到 swarm 状态时就只做命令部分，不为此改协议。

/** 一条仍在运行的后台命令（只保留短标题，完整命令在工具详情里）。 */
export interface BackgroundTaskCommand {
  id: string;
  /** 命令短标题：优先取工具输入的 description，否则取命令首段，已截断 */
  snippet: string;
  /** running：回合结束后仍在跑的本地命令；background：provider 托管、可查看日志并停止的后台任务 */
  status: 'running' | 'background';
  /** 命令开始的 ISO 时间，用于空闲时的等待计时 */
  startedAt?: string;
}

export interface BackgroundTasksRowProps {
  threadID?: string;
  taskApi?: ReturnType<typeof useRemoteBackgroundTaskApi>;
  commands: BackgroundTaskCommand[];
  bees: RemoteSwarmBee[];
  /** 会话已不在运行：标题改为等待文案，时钟图标加计时 */
  waiting?: boolean;
  /** 点子代理行打开既有的子代理浮窗；不传时 bee 行只读 */
  onOpenSwarmBee?: (threadID: string) => void;
}

/** 从工具输入里取命令短标题：description 优先，否则命令首行；截断到 max 个字符。 */
export function commandSnippet(toolName?: string, toolInput?: string, max = 24): string {
  const raw = (toolInput ?? '').trim();
  let text = '';
  if (raw !== '') {
    let record: Record<string, unknown> | null = null;
    try {
      const parsed: unknown = JSON.parse(raw);
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
        record = parsed as Record<string, unknown>;
      }
    } catch {
      record = null;
    }
    if (record) {
      const asText = (value: unknown): string => {
        if (typeof value === 'string') return value.trim();
        // 部分 CLI 把命令传成 argv 数组：拼回一行再截断
        if (Array.isArray(value)) {
          return value.filter((part): part is string => typeof part === 'string').join(' ').trim();
        }
        return '';
      };
      const description = asText(record.description);
      const command = asText(record.command) || asText(record.cmd);
      text = description !== '' ? description : command;
    } else {
      text = raw;
    }
  }
  // 命令可能带 heredoc 或多行脚本，组头只取首行
  text = text.split('\n')[0]?.trim() ?? '';
  if (text === '') return (toolName ?? '').trim() || '命令';
  const runes = Array.from(text);
  return runes.length > max ? `${runes.slice(0, max).join('')}…` : text;
}

/** 聚合行文案：某一支为 0 就省略该段；两支都为 0 返回空串（调用方不渲染）。 */
export function backgroundTasksLabel(commandCount: number, beeCount: number): string {
  const parts = ['后台'];
  if (commandCount > 0) parts.push(`${commandCount} 项任务`);
  if (beeCount > 0) parts.push(`${beeCount} 个子智能体`);
  return parts.length > 1 ? parts.join(' · ') : '';
}

/**
 * 会话空闲时的等待标题：「等待 N 个后台任务 · 任务名 · 1:05」。
 * 任务名为空时省略该段；计时为 null 时省略；不足一分钟按秒显示，否则按 m:ss。
 */
export function backgroundWaitTitle(
  total: number,
  leadName: string,
  elapsedSeconds: number | null
): string {
  const parts = [`等待 ${total} 个后台任务`];
  const name = leadName.trim();
  if (name !== '') parts.push(name);
  if (elapsedSeconds !== null) {
    const seconds = Math.max(0, Math.floor(elapsedSeconds));
    const clock =
      seconds < 60
        ? `${seconds}s`
        : `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`;
    parts.push(clock);
  }
  return parts.join(' · ');
}

export function requestBackgroundTaskLog(threadID: string, messageID: string, log: (threadID: string, messageID: string) => Promise<{ text: string; available: boolean }>) {
  return log(threadID, messageID);
}

export function stopBackgroundTask(threadID: string, messageID: string, stop: (threadID: string, messageID: string) => Promise<unknown>) {
  return stop(threadID, messageID);
}

export function backgroundTaskStopLabel(status: BackgroundTaskCommand['status'], supported: boolean): string | null {
  return supported && status === 'background' ? '停止' : null;
}

/** 最早启动的那条命令：优先按 startedAt，缺时间戳时退回第一条命令。 */
function leadCommand(commands: BackgroundTaskCommand[]): BackgroundTaskCommand | undefined {
  let earliest: BackgroundTaskCommand | undefined;
  let earliestAt = Number.POSITIVE_INFINITY;
  for (const command of commands) {
    if (!command.startedAt) continue;
    const parsed = Date.parse(command.startedAt);
    if (Number.isNaN(parsed)) continue;
    if (parsed < earliestAt) {
      earliestAt = parsed;
      earliest = command;
    }
  }
  return earliest ?? commands[0];
}

export const BackgroundTasksRow: React.FC<BackgroundTasksRowProps> = ({
  threadID,
  taskApi,
  commands,
  bees,
  waiting = false,
  onOpenSwarmBee
}) => {
  const [expanded, setExpanded] = React.useState(false);
  const [expandedTaskID, setExpandedTaskID] = React.useState<string | null>(null);
  const [logText, setLogText] = React.useState('');
  const [logAvailable, setLogAvailable] = React.useState(false);
  const [stopping, setStopping] = React.useState<string | null>(null);
  const [stopFailure, setStopFailure] = React.useState<string | null>(null);
  const pendingLog = React.useRef(false);
  const lead = leadCommand(commands);
  const startedAtMs = lead && lead.startedAt ? Date.parse(lead.startedAt) : Number.NaN;
  const hasStartedAt = !Number.isNaN(startedAtMs);
  const expandedCommandPresent = commands.some((item) => item.id === expandedTaskID);

  // 只在等待且拿得到开始时间时每秒刷新，卸载或切回运行态就停
  const [now, setNow] = React.useState(() => Date.now());
  React.useEffect(() => {
    if (!waiting || !hasStartedAt) return;
    setNow(Date.now());
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [waiting, hasStartedAt, startedAtMs]);

  React.useEffect(() => {
    const command = commands.find((item) => item.id === expandedTaskID);
    if (!expanded || !command || !threadID || !taskApi?.supported) return;
    let active = true;
    const poll = async () => {
      if (pendingLog.current) return;
      pendingLog.current = true;
      try {
        const result = await requestBackgroundTaskLog(threadID, command.id, taskApi.log);
        if (active) {
          setLogText(result.text);
          setLogAvailable(result.available);
        }
      } catch { /* 日志暂不可用时保留已有内容 */ }
      finally { pendingLog.current = false; }
    };
    void poll();
    const timer = window.setInterval(() => void poll(), 1000);
    return () => { active = false; window.clearInterval(timer); };
  }, [expanded, expandedTaskID, threadID, taskApi, expandedCommandPresent]);

  const count = commands.length + bees.length;
  if (count === 0) return null;

  const leadName = lead?.snippet ?? bees[0]?.name ?? '';
  const elapsedSeconds =
    waiting && hasStartedAt ? Math.max(0, Math.floor((now - startedAtMs) / 1000)) : null;
  const label = waiting
    ? backgroundWaitTitle(count, leadName, elapsedSeconds)
    : backgroundTasksLabel(commands.length, bees.length);

  return (
    <div className="mb-4 min-w-0">
      <button
        type="button"
        onClick={() => setExpanded((current) => !current)}
        aria-expanded={expanded}
        aria-label={expanded ? `收起${label}` : `展开${label}`}
        className={`flex w-full min-h-[34px] items-center justify-between gap-2 rounded-xl border bg-[var(--card)] px-2.5 py-1.5 text-left text-mc-footnote text-[var(--foreground-muted)] transition hover:bg-[var(--card-alt,var(--card))] ${
          waiting ? 'border-[var(--primary)]/40' : 'border-[var(--border)]'
        }`}
      >
        <span className="flex min-w-0 items-center gap-1.5">
          {waiting ? (
            // 等后台任务时用静止的小时钟，比脉动圆点更能表达「在等」
            <svg
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth={2}
              strokeLinecap="round"
              strokeLinejoin="round"
              className="h-3.5 w-3.5 shrink-0 text-[var(--primary)]"
              aria-hidden="true"
            >
              <circle cx="12" cy="12" r="9" />
              <path d="M12 7v5l3 2" />
            </svg>
          ) : (
            // 有东西在跑才渲染这一行，脉动动画也就只在运行中启用
            <span
              className="h-1.5 w-1.5 shrink-0 rounded-full bg-[var(--primary)] animate-pulse motion-reduce:animate-none"
              aria-hidden="true"
            />
          )}
          <span className="truncate font-medium">{label}</span>
        </span>
        <ChevronRightIcon
          className={`h-3.5 w-3.5 shrink-0 transition-transform duration-200 ${
            expanded ? 'rotate-90' : ''
          }`}
        />
      </button>

      {expanded && (
        <ul className="mt-1.5 min-w-0 space-y-0.5 rounded-xl border border-[var(--border)] bg-[var(--card)] px-2 py-1.5 text-mc-caption text-[var(--foreground-muted)]">
          {commands.map((command) => (
            <React.Fragment key={command.id}>
            <li className="flex min-w-0 items-center gap-1.5 px-1 py-0.5" title={command.snippet}>
              <button type="button" aria-expanded={expandedTaskID === command.id} onClick={() => { setExpandedTaskID((current) => current === command.id ? null : command.id); setLogAvailable(false); setLogText(''); }} className="flex min-w-0 flex-1 items-center gap-1.5 text-left">
                <CodeBracketIcon className="h-3.5 w-3.5 shrink-0" />
                <span className="min-w-0 flex-1 truncate font-mono text-[var(--foreground)]">{command.snippet}</span>
                <span className="shrink-0">{command.status === 'background' ? '后台' : '运行中'}</span>
              </button>
              {taskApi?.supported && backgroundTaskStopLabel(command.status, taskApi.supported) && threadID && (
                <>
                  <button
                    type="button"
                    disabled={stopping === command.id}
                    onClick={() => {
                      setStopping(command.id);
                      setStopFailure(null);
                      void stopBackgroundTask(threadID, command.id, taskApi.stop)
                        .then((result) => {
                          const stopped = Boolean(result && typeof result === 'object' && 'stopped' in result && (result as { stopped?: unknown }).stopped === true);
                          if (stopped) {
                            setExpanded(false);
                            setExpandedTaskID(null);
                          } else {
                            setStopFailure(command.id);
                          }
                        })
                        .catch(() => setStopFailure(command.id))
                        .finally(() => setStopping(null));
                    }}
                    className="shrink-0 text-[var(--danger-foreground)] disabled:opacity-50"
                  >{stopping === command.id ? '停止中…' : backgroundTaskStopLabel(command.status, taskApi.supported)}</button>
                  {stopFailure === command.id && <span className="shrink-0 text-[var(--danger-foreground)]">未能停止</span>}
                </>
              )}
            </li>
            {expandedTaskID === command.id && logAvailable && <li className="ml-5 max-h-48 overflow-auto whitespace-pre-wrap break-words font-mono text-[var(--foreground)]">{logText}</li>}
            </React.Fragment>
          ))}
          {bees.map((bee) => {
            // 头像用与派单胶囊同一套名字与配色，方便把两处的 bee 对上
            const beeContent = (
              <>
                <SwarmAvatar name={bee.name} tint={pickByHash(bee.name)} />
                <span className="shrink-0 font-medium text-[var(--foreground)]">{bee.name}</span>
                <span className="min-w-0 flex-1 truncate">
                  {(bee.activity ?? '').trim() || '运行中'}
                </span>
              </>
            );
            return (
              <li key={bee.threadID} className="min-w-0">
                {onOpenSwarmBee ? (
                  <button
                    type="button"
                    onClick={() => onOpenSwarmBee(bee.threadID)}
                    aria-label={`打开子智能体 ${bee.name}`}
                    className="flex w-full min-w-0 items-center gap-1.5 rounded-lg px-1 py-0.5 text-left transition hover:bg-[var(--card-alt,var(--card))]"
                  >
                    {beeContent}
                  </button>
                ) : (
                  <div className="flex min-w-0 items-center gap-1.5 px-1 py-0.5">{beeContent}</div>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
};

BackgroundTasksRow.displayName = 'BackgroundTasksRow';
