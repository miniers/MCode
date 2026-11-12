// 自动化（automations.*）的远程调用封装，对应宿主 internal/remote/automation_ext_methods.go；
// 类型来自 web/src/types/generated.ts（由 docs/remote-control.schema.json 生成），此处按原名再导出。
//
// 覆盖查看、手动运行、已读与归档，以及创建、修改、删除、暂停/恢复、重置（写操作）。

import { useCallback, useEffect, useMemo, useState } from 'react';
import type {
  RemoteAutomationExtArchiveRunResult,
  RemoteAutomationExtAutomation,
  RemoteAutomationExtAutomationResult,
  RemoteAutomationExtChanged,
  RemoteAutomationExtDeleteResult,
  RemoteAutomationExtInput,
  RemoteAutomationExtListResult,
  RemoteAutomationExtMarkReadResult,
  RemoteAutomationExtRun,
  RemoteAutomationExtRunResult,
  RemoteAutomationExtRunsResult,
  RemoteAutomationExtSchedule,
  RemoteAutomationExtPreviewResult
} from '../types/generated';
import { useRemoteCall, useRemoteNotification } from './useRemoteCall';

export type {
  RemoteAutomationExtArchiveRunResult,
  RemoteAutomationExtAutomation,
  RemoteAutomationExtAutomationResult,
  RemoteAutomationExtChanged,
  RemoteAutomationExtDeleteResult,
  RemoteAutomationExtInput,
  RemoteAutomationExtListResult,
  RemoteAutomationExtMarkReadResult,
  RemoteAutomationExtRun,
  RemoteAutomationExtRunResult,
  RemoteAutomationExtRunsResult,
  RemoteAutomationExtSchedule,
  RemoteAutomationExtPreviewResult
};

export const REMOTE_AUTOMATIONS_LIST_METHOD = 'automations.list';
export const REMOTE_AUTOMATIONS_RUNS_METHOD = 'automations.runs';
export const REMOTE_AUTOMATIONS_RUN_METHOD = 'automations.run';
export const REMOTE_AUTOMATIONS_MARK_READ_METHOD = 'automations.markRead';
export const REMOTE_AUTOMATIONS_ARCHIVE_RUN_METHOD = 'automations.archiveRun';
export const REMOTE_AUTOMATIONS_CREATE_METHOD = 'automations.create';
export const REMOTE_AUTOMATIONS_UPDATE_METHOD = 'automations.update';
export const REMOTE_AUTOMATIONS_DELETE_METHOD = 'automations.delete';
export const REMOTE_AUTOMATIONS_SET_ENABLED_METHOD = 'automations.setEnabled';
export const REMOTE_AUTOMATIONS_RESET_METHOD = 'automations.reset';
export const REMOTE_AUTOMATIONS_PREVIEW_METHOD = 'automations.preview';
export const REMOTE_AUTOMATIONS_CHANGED_METHOD = 'automations.changed';

/** 宿主返回的版本冲突错误码（wire.ErrCodeVersionConflict）。 */
export const AUTOMATION_VERSION_CONFLICT_CODE = -32009;

/** 版本冲突时展示的提示文案，与桌面端横幅一致。 */
export const AUTOMATION_VERSION_CONFLICT_MESSAGE = '自动化已被其他地方修改，请刷新后重试';

/** 把写操作失败转成可展示的中文文案：版本冲突单独提示，其它错误用宿主原文。 */
export function automationErrorMessage(err: unknown, fallback: string): string {
  if (err && typeof err === 'object' && 'code' in err) {
    const code = (err as { code?: unknown }).code;
    if (code === AUTOMATION_VERSION_CONFLICT_CODE) return AUTOMATION_VERSION_CONFLICT_MESSAGE;
  }
  if (err instanceof Error && err.message) return err.message;
  return fallback;
}

/** automations.runs 的选项。 */
export interface RemoteAutomationRunsOptions {
  /** 返回条数，宿主默认 30、上限 100。 */
  limit?: number;
  /** 是否一并返回已归档记录。 */
  includeArchived?: boolean;
}

/** 自动化的远程调用集合。 */
export interface RemoteAutomationApi {
  /** 宿主是否提供自动化列表；为假时整个入口隐藏。 */
  supportsList: boolean;
  supportsRuns: boolean;
  supportsRun: boolean;
  supportsMarkRead: boolean;
  supportsArchiveRun: boolean;
  supportsCreate: boolean;
  supportsUpdate: boolean;
  supportsDelete: boolean;
  supportsSetEnabled: boolean;
  supportsReset: boolean;
  supportsPreview: boolean;
  listAutomations: (projectID: string) => Promise<RemoteAutomationExtAutomation[]>;
  listRuns: (automationID: string, options?: RemoteAutomationRunsOptions) => Promise<RemoteAutomationExtRun[]>;
  runNow: (automationID: string) => Promise<RemoteAutomationExtRun>;
  markRunRead: (runID: string) => Promise<void>;
  archiveRun: (runID: string, archived: boolean) => Promise<void>;
  /** 新建自动化；input 见 RemoteAutomationExtInput。 */
  createAutomation: (projectID: string, input: RemoteAutomationExtInput) => Promise<RemoteAutomationExtAutomation>;
  /** 修改自动化；revision 省略时宿主不校验并发。 */
  updateAutomation: (
    automationID: string,
    input: RemoteAutomationExtInput,
    revision?: number
  ) => Promise<RemoteAutomationExtAutomation>;
  /** 删除自动化；宿主要求 confirm=true（二次确认）。 */
  deleteAutomation: (automationID: string) => Promise<void>;
  /** 暂停或恢复；恢复（enabled=true）同时清空连续失败计数。 */
  setAutomationEnabled: (
    automationID: string,
    enabled: boolean,
    revision?: number
  ) => Promise<RemoteAutomationExtAutomation>;
  /** 重置：等价于 setEnabled(true)，与桌面端「重置」语义一致。 */
  resetAutomation: (automationID: string, revision?: number) => Promise<RemoteAutomationExtAutomation>;
  previewAutomationSchedule: (schedule: RemoteAutomationExtSchedule) => Promise<RemoteAutomationExtPreviewResult>;
}

/** 取远程扩展调用集合；能力未变化前返回同一对象，可直接作为 effect 依赖。 */
export function useRemoteAutomationApi(): RemoteAutomationApi {
  const { call, supportsMethod } = useRemoteCall();
  const supportsList = supportsMethod(REMOTE_AUTOMATIONS_LIST_METHOD);
  const supportsRuns = supportsMethod(REMOTE_AUTOMATIONS_RUNS_METHOD);
  const supportsRun = supportsMethod(REMOTE_AUTOMATIONS_RUN_METHOD);
  const supportsMarkRead = supportsMethod(REMOTE_AUTOMATIONS_MARK_READ_METHOD);
  const supportsArchiveRun = supportsMethod(REMOTE_AUTOMATIONS_ARCHIVE_RUN_METHOD);
  const supportsCreate = supportsMethod(REMOTE_AUTOMATIONS_CREATE_METHOD);
  const supportsUpdate = supportsMethod(REMOTE_AUTOMATIONS_UPDATE_METHOD);
  const supportsDelete = supportsMethod(REMOTE_AUTOMATIONS_DELETE_METHOD);
  const supportsSetEnabled = supportsMethod(REMOTE_AUTOMATIONS_SET_ENABLED_METHOD);
  const supportsReset = supportsMethod(REMOTE_AUTOMATIONS_RESET_METHOD);
  const supportsPreview = supportsMethod(REMOTE_AUTOMATIONS_PREVIEW_METHOD);

  const listAutomations = useCallback(
    async (projectID: string): Promise<RemoteAutomationExtAutomation[]> => {
      const result = await call<RemoteAutomationExtListResult>(REMOTE_AUTOMATIONS_LIST_METHOD, { projectID });
      return parseAutomations(result?.automations);
    },
    [call]
  );

  const listRuns = useCallback(
    async (automationID: string, options?: RemoteAutomationRunsOptions): Promise<RemoteAutomationExtRun[]> => {
      const params: { automationID: string; limit?: number; includeArchived?: boolean } = { automationID };
      if (typeof options?.limit === 'number' && Number.isFinite(options.limit) && options.limit > 0) {
        params.limit = Math.min(Math.floor(options.limit), 100);
      }
      if (options?.includeArchived) params.includeArchived = true;
      const result = await call<RemoteAutomationExtRunsResult>(REMOTE_AUTOMATIONS_RUNS_METHOD, params);
      return parseRuns(result?.runs);
    },
    [call]
  );

  const runNow = useCallback(
    async (automationID: string): Promise<RemoteAutomationExtRun> => {
      const result = await call<RemoteAutomationExtRunResult>(REMOTE_AUTOMATIONS_RUN_METHOD, { automationID });
      return parseRun(result?.run);
    },
    [call]
  );

  const markRunRead = useCallback(
    async (runID: string): Promise<void> => {
      await call<RemoteAutomationExtMarkReadResult>(REMOTE_AUTOMATIONS_MARK_READ_METHOD, { runID });
    },
    [call]
  );

  const archiveRun = useCallback(
    async (runID: string, archived: boolean): Promise<void> => {
      await call<RemoteAutomationExtArchiveRunResult>(REMOTE_AUTOMATIONS_ARCHIVE_RUN_METHOD, { runID, archived });
    },
    [call]
  );

  const createAutomation = useCallback(
    async (projectID: string, input: RemoteAutomationExtInput): Promise<RemoteAutomationExtAutomation> => {
      try {
        const result = await call<RemoteAutomationExtAutomationResult>(REMOTE_AUTOMATIONS_CREATE_METHOD, {
          projectID,
          input
        });
        return parseAutomationResult(result?.automation);
      } catch (err: unknown) {
        throw new Error(automationErrorMessage(err, '新建自动化失败'));
      }
    },
    [call]
  );

  const updateAutomation = useCallback(
    async (
      automationID: string,
      input: RemoteAutomationExtInput,
      revision?: number
    ): Promise<RemoteAutomationExtAutomation> => {
      const params: { automationID: string; input: RemoteAutomationExtInput; revision?: number } = {
        automationID,
        input
      };
      if (typeof revision === 'number' && Number.isFinite(revision)) params.revision = revision;
      try {
        const result = await call<RemoteAutomationExtAutomationResult>(REMOTE_AUTOMATIONS_UPDATE_METHOD, params);
        return parseAutomationResult(result?.automation);
      } catch (err: unknown) {
        throw new Error(automationErrorMessage(err, '保存自动化失败'));
      }
    },
    [call]
  );

  const deleteAutomation = useCallback(
    async (automationID: string): Promise<void> => {
      try {
        await call<RemoteAutomationExtDeleteResult>(REMOTE_AUTOMATIONS_DELETE_METHOD, {
          automationID,
          confirm: true
        });
      } catch (err: unknown) {
        throw new Error(automationErrorMessage(err, '删除自动化失败'));
      }
    },
    [call]
  );

  const setAutomationEnabled = useCallback(
    async (automationID: string, enabled: boolean, revision?: number): Promise<RemoteAutomationExtAutomation> => {
      const params: { automationID: string; enabled: boolean; revision?: number } = { automationID, enabled };
      if (typeof revision === 'number' && Number.isFinite(revision)) params.revision = revision;
      try {
        const result = await call<RemoteAutomationExtAutomationResult>(
          REMOTE_AUTOMATIONS_SET_ENABLED_METHOD,
          params
        );
        return parseAutomationResult(result?.automation);
      } catch (err: unknown) {
        throw new Error(automationErrorMessage(err, enabled ? '恢复自动化失败' : '暂停自动化失败'));
      }
    },
    [call]
  );

  const resetAutomation = useCallback(
    async (automationID: string, revision?: number): Promise<RemoteAutomationExtAutomation> => {
      const params: { automationID: string; revision?: number } = { automationID };
      if (typeof revision === 'number' && Number.isFinite(revision)) params.revision = revision;
      try {
        const result = await call<RemoteAutomationExtAutomationResult>(REMOTE_AUTOMATIONS_RESET_METHOD, params);
        return parseAutomationResult(result?.automation);
      } catch (err: unknown) {
        throw new Error(automationErrorMessage(err, '重置自动化失败'));
      }
    },
    [call]
  );

  const previewAutomationSchedule = useCallback(
    (schedule: RemoteAutomationExtSchedule): Promise<RemoteAutomationExtPreviewResult> =>
      call<RemoteAutomationExtPreviewResult>(REMOTE_AUTOMATIONS_PREVIEW_METHOD, { schedule, count: 3 }),
    [call]
  );

  return useMemo<RemoteAutomationApi>(
    () => ({
      supportsList,
      supportsRuns,
      supportsRun,
      supportsMarkRead,
      supportsArchiveRun,
      supportsCreate,
      supportsUpdate,
      supportsDelete,
      supportsSetEnabled,
      supportsReset,
      supportsPreview,
      listAutomations,
      listRuns,
      runNow,
      markRunRead,
      archiveRun,
      createAutomation,
      updateAutomation,
      deleteAutomation,
      setAutomationEnabled,
      resetAutomation,
      previewAutomationSchedule
    }),
    [
      supportsList,
      supportsRuns,
      supportsRun,
      supportsMarkRead,
      supportsArchiveRun,
      supportsCreate,
      supportsUpdate,
      supportsDelete,
      supportsSetEnabled,
      supportsReset,
      supportsPreview,
      listAutomations,
      listRuns,
      runNow,
      markRunRead,
      archiveRun,
      createAutomation,
      updateAutomation,
      deleteAutomation,
      setAutomationEnabled,
      resetAutomation,
      previewAutomationSchedule
    ]
  );
}

/**
 * 拉取某个项目的自动化列表，收到 automations.changed 后自动重拉。
 * projectID 为空或宿主不支持 automations.list 时不发请求。
 */
export function useProjectAutomations(projectID: string | null): {
  automations: RemoteAutomationExtAutomation[];
  unreadTotal: number;
  loading: boolean;
  error: string | null;
  reload: () => void;
} {
  const { listAutomations, supportsList } = useRemoteAutomationApi();
  const [automations, setAutomations] = useState<RemoteAutomationExtAutomation[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [reloadToken, setReloadToken] = useState(0);

  useEffect(() => {
    if (!projectID || !supportsList) {
      setAutomations([]);
      setLoading(false);
      setError(null);
      return;
    }
    let cancelled = false;
    setLoading(true);
    void listAutomations(projectID)
      .then((list) => {
        if (cancelled) return;
        setAutomations(list);
        setError(null);
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        setAutomations([]);
        setError(err instanceof Error ? err.message : '加载自动化失败');
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [listAutomations, projectID, reloadToken, supportsList]);

  const reload = useCallback(() => setReloadToken((token) => token + 1), []);
  // 变更通知不筛 automationID：运行级的已读/归档只带 runID，统一重拉最省事。
  useRemoteNotification(REMOTE_AUTOMATIONS_CHANGED_METHOD, () => reload(), supportsList);

  const unreadTotal = automations.reduce((sum, item) => sum + (item.unreadCount || 0), 0);
  return { automations, unreadTotal, loading, error, reload };
}

/**
 * 拉取某个自动化的运行记录，收到 automations.changed 后自动重拉。
 * automationID 为空或宿主不支持 automations.runs 时不发请求。
 */
export function useAutomationRuns(
  automationID: string | null,
  includeArchived: boolean
): {
  runs: RemoteAutomationExtRun[];
  loading: boolean;
  error: string | null;
  reload: () => void;
} {
  const { listRuns, supportsRuns } = useRemoteAutomationApi();
  const [runs, setRuns] = useState<RemoteAutomationExtRun[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [reloadToken, setReloadToken] = useState(0);

  useEffect(() => {
    if (!automationID || !supportsRuns) {
      setRuns([]);
      setLoading(false);
      setError(null);
      return;
    }
    let cancelled = false;
    setLoading(true);
    void listRuns(automationID, { includeArchived })
      .then((list) => {
        if (cancelled) return;
        setRuns(list);
        setError(null);
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        setRuns([]);
        setError(err instanceof Error ? err.message : '加载运行记录失败');
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [listRuns, automationID, includeArchived, reloadToken, supportsRuns]);

  const reload = useCallback(() => setReloadToken((token) => token + 1), []);
  useRemoteNotification(REMOTE_AUTOMATIONS_CHANGED_METHOD, () => reload(), supportsRuns);

  return { runs, loading, error, reload };
}

/** 稳妥解析列表项：形状不对的条目直接跳过，字符串缺省补空串、数字缺省补 0。 */
function parseAutomations(raw: unknown): RemoteAutomationExtAutomation[] {
  if (!Array.isArray(raw)) return [];
  const out: RemoteAutomationExtAutomation[] = [];
  for (const entry of raw) {
    if (!entry || typeof entry !== 'object') continue;
    const item = entry as Partial<RemoteAutomationExtAutomation>;
    if (typeof item.id !== 'string' || typeof item.name !== 'string') continue;
    out.push(parseAutomation(item));
  }
  return out;
}

/** 解析单个自动化；写操作返回的对象走同一套字段兜底。 */
function parseAutomation(raw: Partial<RemoteAutomationExtAutomation> | undefined): RemoteAutomationExtAutomation {
  const item = raw ?? {};
  return {
    id: typeof item.id === 'string' ? item.id : '',
    projectID: typeof item.projectID === 'string' ? item.projectID : undefined,
    name: typeof item.name === 'string' ? item.name : '',
    prompt: typeof item.prompt === 'string' ? item.prompt : '',
    stopWhen: typeof item.stopWhen === 'string' ? item.stopWhen : undefined,
    schedule: parseSchedule(item.schedule),
    scheduleText: typeof item.scheduleText === 'string' ? item.scheduleText : '',
    enabled: Boolean(item.enabled),
    status: typeof item.status === 'string' ? item.status : undefined,
    lastRunAt: typeof item.lastRunAt === 'string' ? item.lastRunAt : undefined,
    lastRunStatus: typeof item.lastRunStatus === 'string' ? item.lastRunStatus : undefined,
    nextRunAt: typeof item.nextRunAt === 'string' ? item.nextRunAt : undefined,
    unreadCount: typeof item.unreadCount === 'number' ? item.unreadCount : 0,
    consecutiveFailures: typeof item.consecutiveFailures === 'number' ? item.consecutiveFailures : 0,
    maxConsecutiveFailures: typeof item.maxConsecutiveFailures === 'number' ? item.maxConsecutiveFailures : 0,
    maxIterations: typeof item.maxIterations === 'number' ? item.maxIterations : 0,
    revision: typeof item.revision === 'number' ? item.revision : 0
  };
}

/** 解析写操作返回的 automation 字段。 */
function parseAutomationResult(raw: unknown): RemoteAutomationExtAutomation {
  if (!raw || typeof raw !== 'object') return parseAutomation(undefined);
  return parseAutomation(raw as Partial<RemoteAutomationExtAutomation>);
}

function parseSchedule(raw: unknown): RemoteAutomationExtSchedule {
  if (!raw || typeof raw !== 'object') return { type: '' };
  const sch = raw as Partial<RemoteAutomationExtSchedule>;
  return {
    type: typeof sch.type === 'string' ? sch.type : '',
    seconds: typeof sch.seconds === 'number' ? sch.seconds : undefined,
    date: typeof sch.date === 'string' ? sch.date : undefined,
    timeOfDay: typeof sch.timeOfDay === 'string' ? sch.timeOfDay : undefined,
    timezone: typeof sch.timezone === 'string' ? sch.timezone : undefined,
    dayOfWeek: typeof sch.dayOfWeek === 'number' ? sch.dayOfWeek : undefined,
    expression: typeof sch.expression === 'string' ? sch.expression : undefined
  };
}

function parseRuns(raw: unknown): RemoteAutomationExtRun[] {
  if (!Array.isArray(raw)) return [];
  const out: RemoteAutomationExtRun[] = [];
  for (const entry of raw) {
    if (!entry || typeof entry !== 'object') continue;
    const run = parseRun(entry);
    if (run) out.push(run);
  }
  return out;
}

function parseRun(raw: unknown): RemoteAutomationExtRun {
  const run = raw && typeof raw === 'object' ? (raw as Partial<RemoteAutomationExtRun>) : undefined;
  if (!run || typeof run.id !== 'string') {
    // 调用方（runNow）拿不到 id 说明返回值形状不对，按空运行记录处理，界面会显示「未知」。
    return { id: '', automationID: '', status: '', startedAt: '', read: false, archived: false };
  }
  return {
    id: run.id,
    automationID: typeof run.automationID === 'string' ? run.automationID : '',
    status: typeof run.status === 'string' ? run.status : '',
    startedAt: typeof run.startedAt === 'string' ? run.startedAt : '',
    finishedAt: typeof run.finishedAt === 'string' ? run.finishedAt : undefined,
    threadID: typeof run.threadID === 'string' && run.threadID ? run.threadID : undefined,
    read: Boolean(run.read),
    archived: Boolean(run.archived),
    summary: typeof run.summary === 'string' ? run.summary : undefined,
    error: typeof run.error === 'string' ? run.error : undefined
  };
}
