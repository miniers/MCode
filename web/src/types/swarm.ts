// mcode 远程控制编排（Swarm）模型契约与状态类型

export type RemoteSwarmBeeKind = 'native';
export type RemoteSwarmBeeOrigin = 'delegated' | 'queued' | 'sent';
export type RemoteSwarmBeeStatus = 'running' | 'completed' | 'failed' | 'stopped';

export interface RemoteSwarmBee {
  threadID: string;
  index: number;
  name: string;
  task: string;
  kind: RemoteSwarmBeeKind;
  origin: RemoteSwarmBeeOrigin;
  status: RemoteSwarmBeeStatus;
  summary?: string;
  activity?: string;
  toolCalls: number;
  tokens: number;
  startedAt: string;
  finishedAt?: string;
  provider?: string;
  model?: string;
  canOpen: boolean;
  canStop: boolean;
  /** 派发时选择的执行档位标识 */
  tier?: string;
  /** 只读档：改动不会落回项目 */
  readOnly?: boolean;
  /** 项目不是 Git 仓库，改动写在影子仓库里 */
  shadowRepo?: boolean;
  /** 共享目录且未启用 Git 隔离，子代理改动可能互相覆盖 */
  sharedNoGit?: boolean;
  /** 共享目录无隔离的原因说明 */
  sharedNoGitReason?: string;
  /** 主会话显式指定了模型 */
  directModel?: boolean;
  /** 改动合入状态 */
  mergeState?: 'pending' | 'merged' | 'discarded' | 'failed' | 'reviewing' | 'rejected';
  /** 合并前审查的进度；缺省表示未进入审查 */
  reviewState?: 'running' | 'approved' | 'rejected' | 'needs_human';
  /** 审查模型给出的结论说明 */
  reviewReason?: string;
  /** 审查会话的线程 ID */
  reviewThreadId?: string;
  /** 审查停在哪一层：L0 检查、L1 规则、L2 模型、L3 会话或人工 */
  reviewLayer?: 'l0' | 'l1' | 'l2' | 'l3' | 'human';
  /** 审查列出的疑点，通常是 path:line 形式的待确认项 */
  reviewConcerns?: string[];
  /** 本次审查消耗的 token 数；L0/L1 没有模型调用时不设置 */
  reviewTokens?: number;
  /** token 数是估算值而非模型返回值 */
  reviewTokensEstimated?: boolean;
  /** 落到同一文件、产生冲突的文件列表 */
  conflictFiles?: string[];
  /** 合入的文件列表 */
  changedFiles?: string[];
  /** 合并失败原因 */
  mergeError?: string;
  linesAdded?: number;
  linesDeleted?: number;
  /** 汇报中的可信度统计：已引用/已核实条目与未核实、未读清单 */
  credibility?: {
    cited: number;
    verified: number;
    unverified?: string[];
    notRead?: string[];
  };
  /** 子代理正在等待答复的提问 */
  pendingQuestion?: {
    requestID: string;
    prompt: string;
    forwarded: boolean;
    autoForwardAt?: string;
  };
}

export interface RemoteSwarmState {
  threadID: string;
  swarmAvailable: boolean; // Mac app-wide switch
  swarmEnabled: boolean;   // this chat
  isBee: boolean;
  leadThreadID?: string;
  bee?: RemoteSwarmBee;
  bees: RemoteSwarmBee[];
  /** 派发队列中等待启动的子代理数（尚未出现在 bees 中） */
  queued?: number;
  /** 本轮只出了方案、没有改动代码 */
  plansOnly?: boolean;
  /** 主会话显式覆盖了只出方案的限制 */
  plansOnlyOverridden?: boolean;
}

export type SwarmTimelineMessageKind = 'swarm_report' | 'swarm_task';
