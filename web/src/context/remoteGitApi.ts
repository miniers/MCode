// 扩展方法 git.*（分文件提交、贮藏、PR/MR 查看、写操作）的调用封装，对应 Go 端
// internal/remote/wire/git_ext_types.go；参数与结果类型来自 web/src/types/generated.ts
// （由 docs/remote-control.schema.json 生成），此处按原名再导出。

import type { RemoteCapabilityDeclaration } from '../types/contract';
import type {
  RemoteGitChangedFile,
  RemoteGitChangedFilesResult,
  RemoteGitCommitSelectedResult,
  RemoteGitGenerateCommitMessageResult,
  RemoteGitPRCheck,
  RemoteGitPRComment,
  RemoteGitPRCreateURLResult,
  RemoteGitPRDetail,
  RemoteGitPRListResult,
  RemoteGitPRSummary,
  RemoteGitStashActionResult,
  RemoteGitStashEntry,
  RemoteGitStashListResult,
  RemoteGitWriteActionResult
} from '../types/generated';

export type {
  RemoteGitChangedFile,
  RemoteGitChangedFilesResult,
  RemoteGitCommitSelectedResult,
  RemoteGitGenerateCommitMessageResult,
  RemoteGitPRCheck,
  RemoteGitPRComment,
  RemoteGitPRCreateURLResult,
  RemoteGitPRDetail,
  RemoteGitPRListResult,
  RemoteGitPRSummary,
  RemoteGitStashActionResult,
  RemoteGitStashEntry,
  RemoteGitStashListResult,
  RemoteGitWriteActionResult
};

/** 远程调用器：与 useRemoteCall().call 同签名，便于在测试或非组件代码里注入。 */
export type GitExtCaller = <T>(method: string, params?: unknown, timeoutMs?: number) => Promise<T>;

/** 扩展方法名，集中一处避免各处手写字符串。 */
export const GIT_EXT_METHODS = {
  changedFiles: 'git.changedFiles',
  commitSelected: 'git.commitSelected',
  stashList: 'git.stash.list',
  stashPush: 'git.stash.push',
  stashPop: 'git.stash.pop',
  stashApply: 'git.stash.apply',
  stashDrop: 'git.stash.drop',
  prList: 'git.pr.list',
  prDetail: 'git.pr.detail',
  push: 'git.push',
  pull: 'git.pull',
  fetch: 'git.fetch',
  checkout: 'git.checkout',
  createBranch: 'git.createBranch',
  merge: 'git.merge',
  generateCommitMessage: 'git.generateCommitMessage',
  prCreateUrl: 'git.pr.createUrl'
} as const;

/** 写操作依赖的方法名清单，用于一次性判断宿主是否支持整套 Git 写能力。 */
export const GIT_WRITE_METHODS = [
  GIT_EXT_METHODS.push,
  GIT_EXT_METHODS.pull,
  GIT_EXT_METHODS.fetch,
  GIT_EXT_METHODS.checkout,
  GIT_EXT_METHODS.createBranch,
  GIT_EXT_METHODS.merge,
  GIT_EXT_METHODS.generateCommitMessage,
  GIT_EXT_METHODS.prCreateUrl
] as const;

/**
 * 能力判断：capabilities 的 methods 里有该方法即为支持。
 * 拿不到 capabilities（未连接）时返回 false，调用方据此隐藏入口或提前报错。
 */
export function gitExtMethodSupported(
  capabilities: RemoteCapabilityDeclaration | null | undefined,
  method: string
): boolean {
  if (!capabilities) return false;
  return capabilities.methods.includes(method);
}

/** 能力判断：GIT_WRITE_METHODS 里的方法全部支持时才算支持 Git 写操作。 */
export function gitWriteSupported(
  capabilities: RemoteCapabilityDeclaration | null | undefined
): boolean {
  return GIT_WRITE_METHODS.every((method) => gitExtMethodSupported(capabilities, method));
}

/** 列出工作区改动文件，供分文件提交时逐项勾选。 */
export async function fetchGitChangedFiles(
  call: GitExtCaller,
  threadID: string
): Promise<RemoteGitChangedFilesResult> {
  return call<RemoteGitChangedFilesResult>(GIT_EXT_METHODS.changedFiles, { threadID });
}

/**
 * 只提交 paths 指定的改动，message 不能为空（首尾空白会被服务端去掉）。
 * pushAfter 为 true 时提交成功后继续推送；推送失败不会抛错，而是返回 ok=true 加 pushError。
 */
export async function commitGitSelected(
  call: GitExtCaller,
  threadID: string,
  paths: string[],
  message: string,
  pushAfter?: boolean
): Promise<RemoteGitCommitSelectedResult> {
  return call<RemoteGitCommitSelectedResult>(GIT_EXT_METHODS.commitSelected, {
    threadID,
    paths,
    message,
    ...(pushAfter ? { pushAfter } : {})
  });
}

/** 列出贮藏记录。 */
export async function fetchGitStashList(
  call: GitExtCaller,
  threadID: string
): Promise<RemoteGitStashListResult> {
  return call<RemoteGitStashListResult>(GIT_EXT_METHODS.stashList, { threadID });
}

/** 贮藏当前工作区改动，message 可选。 */
export async function pushGitStash(
  call: GitExtCaller,
  threadID: string,
  message?: string
): Promise<RemoteGitStashActionResult> {
  return call<RemoteGitStashActionResult>(GIT_EXT_METHODS.stashPush, { threadID, message });
}

/** 恢复最近一条贮藏并从贮藏列表删除。 */
export async function popGitStash(
  call: GitExtCaller,
  threadID: string
): Promise<RemoteGitStashActionResult> {
  return call<RemoteGitStashActionResult>(GIT_EXT_METHODS.stashPop, { threadID });
}

/** 应用指定序号的贮藏，贮藏记录保留。 */
export async function applyGitStash(
  call: GitExtCaller,
  threadID: string,
  index: number
): Promise<RemoteGitStashActionResult> {
  return call<RemoteGitStashActionResult>(GIT_EXT_METHODS.stashApply, { threadID, index });
}

/** 删除指定序号的贮藏记录，不影响工作区。 */
export async function dropGitStash(
  call: GitExtCaller,
  threadID: string,
  index: number
): Promise<RemoteGitStashActionResult> {
  return call<RemoteGitStashActionResult>(GIT_EXT_METHODS.stashDrop, { threadID, index });
}

/** 列出仓库的 PR/MR，limit 缺省时服务端取 20。 */
export async function fetchGitPRList(
  call: GitExtCaller,
  threadID: string,
  limit?: number
): Promise<RemoteGitPRListResult> {
  return call<RemoteGitPRListResult>(GIT_EXT_METHODS.prList, { threadID, limit });
}

/** 读取单条 PR/MR 的详情。 */
export async function fetchGitPRDetail(
  call: GitExtCaller,
  threadID: string,
  number: number
): Promise<RemoteGitPRDetail> {
  return call<RemoteGitPRDetail>(GIT_EXT_METHODS.prDetail, { threadID, number });
}

// 网络操作（推送、拉取、抓取、生成提交信息）在宿主侧有 5 分钟上限，本地操作 2 分钟，
// 这里给前端请求留同样的余量，避免宿主还在跑时前端先超时。
const GIT_NETWORK_TIMEOUT_MS = 5 * 60 * 1000;
const GIT_LOCAL_TIMEOUT_MS = 2 * 60 * 1000;

/** 推送当前分支到远程；失败由宿主回 InternalError，文案是 git 原始错误。 */
export async function pushGitBranch(
  call: GitExtCaller,
  threadID: string
): Promise<RemoteGitWriteActionResult> {
  return call<RemoteGitWriteActionResult>(GIT_EXT_METHODS.push, { threadID }, GIT_NETWORK_TIMEOUT_MS);
}

/** 快进拉取上游到当前分支；无法快进时由宿主报错。 */
export async function pullGitBranch(
  call: GitExtCaller,
  threadID: string
): Promise<RemoteGitWriteActionResult> {
  return call<RemoteGitWriteActionResult>(GIT_EXT_METHODS.pull, { threadID }, GIT_NETWORK_TIMEOUT_MS);
}

/** 抓取远程分支，不改动工作区。 */
export async function fetchGitRemote(
  call: GitExtCaller,
  threadID: string
): Promise<RemoteGitWriteActionResult> {
  return call<RemoteGitWriteActionResult>(GIT_EXT_METHODS.fetch, { threadID }, GIT_NETWORK_TIMEOUT_MS);
}

/** 切换到本地已有分支。 */
export async function checkoutGitBranch(
  call: GitExtCaller,
  threadID: string,
  branch: string
): Promise<RemoteGitWriteActionResult> {
  return call<RemoteGitWriteActionResult>(
    GIT_EXT_METHODS.checkout,
    { threadID, branch },
    GIT_LOCAL_TIMEOUT_MS
  );
}

/** 从当前分支新建并切换到 name；分支名非法时宿主回 InvalidParams。 */
export async function createGitBranch(
  call: GitExtCaller,
  threadID: string,
  name: string
): Promise<RemoteGitWriteActionResult> {
  return call<RemoteGitWriteActionResult>(
    GIT_EXT_METHODS.createBranch,
    { threadID, name },
    GIT_LOCAL_TIMEOUT_MS
  );
}

/** 把 branch 合并进当前分支；冲突等失败由宿主回 InternalError。 */
export async function mergeGitBranch(
  call: GitExtCaller,
  threadID: string,
  branch: string
): Promise<RemoteGitWriteActionResult> {
  return call<RemoteGitWriteActionResult>(
    GIT_EXT_METHODS.merge,
    { threadID, branch },
    GIT_LOCAL_TIMEOUT_MS
  );
}

/** 让宿主按当前未提交改动生成一条提交信息；未开启功能或没有改动时回 InternalError。 */
export async function generateGitCommitMessage(
  call: GitExtCaller,
  threadID: string
): Promise<RemoteGitGenerateCommitMessageResult> {
  return call<RemoteGitGenerateCommitMessageResult>(
    GIT_EXT_METHODS.generateCommitMessage,
    { threadID },
    GIT_NETWORK_TIMEOUT_MS
  );
}

/**
 * 取创建 PR/MR 的网页地址（宿主按远端地址与当前分支拼好，规则与桌面端一致）。
 * title/body 是宿主预填的表单字段，没有预填时省略；无远端或识别不到平台时回 InvalidParams。
 */
export async function fetchGitPRCreateURL(
  call: GitExtCaller,
  threadID: string
): Promise<RemoteGitPRCreateURLResult> {
  return call<RemoteGitPRCreateURLResult>(
    GIT_EXT_METHODS.prCreateUrl,
    { threadID },
    GIT_LOCAL_TIMEOUT_MS
  );
}
