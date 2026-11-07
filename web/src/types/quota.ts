// mcode 远程控制输入框额度条的 composer.quota 契约类型

import type { RemoteQuotaWindow } from './generated';

/**
 * 额度窗口：直接用生成类型，字段与宿主一一对应
 * （label / account / active / disabled / source / plan / display / text / unlimited / balance / unit / readAt）。
 */
export type { RemoteQuotaWindow };

export interface RemoteQuotaBudget {
  provider: string;
  providerName: string;
  role: 'lead' | 'bees';
  profile: string | null;
  state: 'used' | 'stale' | 'credits' | 'loading' | 'attention' | 'unknown';
  windows: RemoteQuotaWindow[];
  amount: string | null;
  readAt: string | null;
  /** 该额度所属账号；同一 provider 多账号时每个账号一条 budget */
  account?: string | null;
  /** 是否该 provider 当前在用的账号 */
  active?: boolean | null;
}

export interface RemoteComposerQuotaResult {
  budgets: RemoteQuotaBudget[];
}

export interface ComposerQuotaScope {
  threadID?: string;
  projectID?: string;
}
