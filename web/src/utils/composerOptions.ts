import type { RemoteComposerOptions, RemoteThreadSummary } from '../types/contract';

export function runningThreadUpdateChanges(
  previous: RemoteComposerOptions,
  next: RemoteComposerOptions
): { model?: string; effort?: string } | null {
  const changes: { model?: string; effort?: string } = {};
  const modelChanged = previous.model !== next.model;
  if (modelChanged && next.model) changes.model = next.model;
  if (previous.effort !== next.effort && !(modelChanged && next.effort === undefined)) {
    changes.effort = next.effort ?? '';
  }
  return Object.keys(changes).length ? changes : null;
}

/** 会话当前的输入区选择，来自宿主下发的摘要（草稿页用 providers.list 的 draft）。 */
export function summaryComposerOptions(summary: RemoteThreadSummary): RemoteComposerOptions {
  return {
    provider: summary.provider || undefined,
    model: summary.model ?? undefined,
    effort: summary.effort ?? undefined,
    runtimeMode: summary.runtimeMode,
    interactionMode: summary.interactionMode,
    fastMode: summary.fastMode,
    workspaceMode: summary.worktreeRequested ? 'worktree' : 'local',
    swarmEnabled: summary.swarmEnabled,
    swarmPairID: summary.swarmPairID ?? undefined,
    swarmBeesProvider: summary.swarmBeesProvider ?? undefined,
    swarmBeesModel: summary.swarmBeesModel ?? undefined,
    swarmBeesEffort: summary.swarmBeesEffort ?? undefined
  };
}

/**
 * 只保留相对基线发生变化的选择：宿主会按 options 重新校验并重置 provider 会话，
 * 没改的字段不发。换了 provider/模型时两者一起发；档位只在用户显式改动时才带。
 */
export function changedComposerOptions(
  options: RemoteComposerOptions,
  base: RemoteComposerOptions
): RemoteComposerOptions {
  const changed: RemoteComposerOptions = {};
  const leavingPair = Boolean(base.swarmPairID) && !options.swarmPairID;
  if (options.provider !== base.provider || options.model !== base.model || leavingPair) {
    if (options.provider) changed.provider = options.provider;
    if (options.model) changed.model = options.model;
  }
  if (options.effort !== undefined && options.effort !== base.effort) changed.effort = options.effort;
  if (options.fastMode !== undefined && options.fastMode !== base.fastMode) changed.fastMode = options.fastMode;
  if (options.runtimeMode && options.runtimeMode !== base.runtimeMode) changed.runtimeMode = options.runtimeMode;
  if (options.interactionMode && options.interactionMode !== base.interactionMode) {
    changed.interactionMode = options.interactionMode;
  }
  if (options.workspaceMode && options.workspaceMode !== base.workspaceMode) changed.workspaceMode = options.workspaceMode;
  if (options.swarmPairID && options.swarmPairID !== base.swarmPairID) changed.swarmPairID = options.swarmPairID;
  const beesChanged =
    options.swarmBeesProvider !== base.swarmBeesProvider ||
    options.swarmBeesModel !== base.swarmBeesModel ||
    options.swarmBeesEffort !== base.swarmBeesEffort;
  if (!changed.swarmPairID && beesChanged && options.swarmBeesProvider) {
    // 子模型只在选了 provider 时才有意义；provider 没变就不发，不拿占位值兜底
    if (options.swarmBeesProvider !== base.swarmBeesProvider) {
      changed.swarmBeesProvider = options.swarmBeesProvider;
    }
    if (options.swarmBeesModel && options.swarmBeesModel !== base.swarmBeesModel) {
      changed.swarmBeesModel = options.swarmBeesModel;
    }
    if (options.swarmBeesEffort && options.swarmBeesEffort !== base.swarmBeesEffort) {
      changed.swarmBeesEffort = options.swarmBeesEffort;
    }
  }
  if (options.swarmEnabled !== undefined && options.swarmEnabled !== base.swarmEnabled) {
    changed.swarmEnabled = options.swarmEnabled;
  }
  return changed;
}

/** 记忆分组的可选项：结构上与宿主 providers.rememberSelection 的参数一致。 */
export interface ComposerSelectionMemoryParams {
  projectID?: string;
  provider?: string;
  model?: string;
  effort?: string;
  fastMode?: boolean;
  swarmEnabled?: boolean;
  swarmBeesProvider?: string;
  swarmBeesModel?: string;
  swarmBeesEffort?: string;
}

/**
 * 用户在本页动过的记忆分组：主导组（provider/model/effort/fastMode）、
 * 子代理组（swarmBees*）、编排开关（swarmEnabled）。一旦动过就一直保留，
 * 宿主下发的新默认值只回填没动过的组。
 */
export interface ComposerTouchedGroups {
  lead: boolean;
  bees: boolean;
  swarm: boolean;
}

const LEAD_FIELDS = ['provider', 'model', 'effort', 'fastMode'] as const;
const BEES_FIELDS = ['swarmBeesProvider', 'swarmBeesModel', 'swarmBeesEffort'] as const;

function leadChanged(previous: RemoteComposerOptions, next: RemoteComposerOptions): boolean {
  return LEAD_FIELDS.some((field) => previous[field] !== next[field]);
}

function beesChanged(previous: RemoteComposerOptions, next: RemoteComposerOptions): boolean {
  return BEES_FIELDS.some((field) => previous[field] !== next[field]);
}

function swarmChanged(previous: RemoteComposerOptions, next: RemoteComposerOptions): boolean {
  return previous.swarmEnabled !== next.swarmEnabled;
}

/** 累积 touched 标记：这次的变化涉及的组记为已动过。 */
export function mergeTouchedGroups(
  previous: RemoteComposerOptions,
  next: RemoteComposerOptions,
  touched: ComposerTouchedGroups
): ComposerTouchedGroups {
  return {
    lead: touched.lead || leadChanged(previous, next),
    bees: touched.bees || beesChanged(previous, next),
    swarm: touched.swarm || swarmChanged(previous, next)
  };
}

/**
 * 用户改动选择时要写入记忆的参数：某项发生变化就把它所在的整组当前值一起带上。
 * 主导组需要 provider + model 同时有值才算完整；子代理组未设置用空字符串表示“跟随/默认档”。
 * 什么都没变（如只改了工作区或执行模式）返回 null，调用方不写记忆。
 */
export function rememberParamsForChange(
  previous: RemoteComposerOptions,
  next: RemoteComposerOptions,
  projectID?: string
): ComposerSelectionMemoryParams | null {
  const params: ComposerSelectionMemoryParams = {};
  let changed = false;
  if (leadChanged(previous, next)) {
    changed = true;
    if (next.provider) params.provider = next.provider;
    if (next.model) params.model = next.model;
    params.effort = next.effort ?? '';
    params.fastMode = Boolean(next.fastMode);
  }
  if (beesChanged(previous, next)) {
    changed = true;
    params.swarmBeesProvider = next.swarmBeesProvider ?? '';
    params.swarmBeesModel = next.swarmBeesModel ?? '';
    params.swarmBeesEffort = next.swarmBeesEffort ?? '';
  }
  if (swarmChanged(previous, next)) {
    changed = true;
    params.swarmEnabled = Boolean(next.swarmEnabled);
  }
  if (!changed) return null;
  if (projectID) params.projectID = projectID;
  return params;
}

/**
 * 把宿主下发的新默认值合并进当前选择：没动过的记忆组跟随新默认值，动过的保留用户选择。
 * 非记忆字段（执行模式、权限、工作区）仍按老规则：用户改过就保留本页选择，否则用默认值。
 */
export function mergeComposerDefaults(
  current: RemoteComposerOptions,
  defaults: RemoteComposerOptions,
  touched: ComposerTouchedGroups,
  keepNonMemoryFromCurrent: boolean
): RemoteComposerOptions {
  const merged: RemoteComposerOptions = { ...defaults };
  if (keepNonMemoryFromCurrent) {
    if (current.runtimeMode !== undefined) merged.runtimeMode = current.runtimeMode;
    if (current.interactionMode !== undefined) merged.interactionMode = current.interactionMode;
    if (current.workspaceMode !== undefined) merged.workspaceMode = current.workspaceMode;
  }
  if (touched.lead) {
    merged.provider = current.provider;
    merged.model = current.model;
    merged.effort = current.effort;
    merged.fastMode = current.fastMode;
  }
  if (touched.bees) {
    merged.swarmBeesProvider = current.swarmBeesProvider;
    merged.swarmBeesModel = current.swarmBeesModel;
    merged.swarmBeesEffort = current.swarmBeesEffort;
  }
  if (touched.swarm) merged.swarmEnabled = current.swarmEnabled;
  // 编排组合不属于记忆，跟着动过的那几组走
  if (touched.lead || touched.bees || touched.swarm) merged.swarmPairID = current.swarmPairID;
  return merged;
}

/**
 * 发送时下发的选择：只发用户动过的记忆组（整组下发），没动过的组交给服务端按最新记忆补齐。
 * 非记忆字段与宿主默认不同才发。
 */
export function touchedComposerOptions(
  options: RemoteComposerOptions,
  touched: ComposerTouchedGroups,
  base: RemoteComposerOptions | null
): RemoteComposerOptions {
  const payload: RemoteComposerOptions = {};
  if (touched.lead) {
    if (options.provider) payload.provider = options.provider;
    if (options.model) payload.model = options.model;
    if (options.effort !== undefined) payload.effort = options.effort;
    if (options.fastMode !== undefined) payload.fastMode = options.fastMode;
    if (options.swarmPairID) payload.swarmPairID = options.swarmPairID;
  }
  if (touched.bees) {
    payload.swarmBeesProvider = options.swarmBeesProvider ?? '';
    payload.swarmBeesModel = options.swarmBeesModel ?? '';
    payload.swarmBeesEffort = options.swarmBeesEffort ?? '';
  }
  if (touched.swarm) payload.swarmEnabled = Boolean(options.swarmEnabled);
  if (options.runtimeMode !== undefined && options.runtimeMode !== base?.runtimeMode) {
    payload.runtimeMode = options.runtimeMode;
  }
  if (options.interactionMode !== undefined && options.interactionMode !== base?.interactionMode) {
    payload.interactionMode = options.interactionMode;
  }
  if (options.workspaceMode !== undefined && options.workspaceMode !== base?.workspaceMode) {
    payload.workspaceMode = options.workspaceMode;
  }
  return payload;
}
