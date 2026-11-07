// 远程添加项目：对应宿主 internal/remote/project_ext_methods.go 的
// projects.add（登记目录）与 projects.validatePath（只读校验，供输入时实时提示）。
// 类型来自 web/src/types/generated.ts（由 docs/remote-control.schema.json 生成），此处按原名再导出。

import type {
  RemoteProjectExtAddParams,
  RemoteProjectExtAddResult,
  RemoteProjectExtCloneParams,
  RemoteProjectExtListDirParams,
  RemoteProjectExtListDirResult,
  RemoteProjectExtValidateParams,
  RemoteProjectExtValidateResult,
  RemoteProjectSummary
} from '../types/generated';
import type { UseRemoteCallResult } from './useRemoteCall';

export type {
  RemoteProjectExtAddParams,
  RemoteProjectExtAddResult,
  RemoteProjectExtCloneParams,
  RemoteProjectExtListDirParams,
  RemoteProjectExtListDirResult,
  RemoteProjectExtValidateParams,
  RemoteProjectExtValidateResult
};

/** projects.listDir 返回的单个子目录。 */
export type RemoteProjectDirEntry = RemoteProjectExtListDirResult['entries'][number];

/** 项目摘要：与 projects.list 共用的 RemoteProjectSummary 同名类型，换个名字导出。 */
export type RemoteProjectExtSummary = RemoteProjectSummary;

/** projects.add：把 Mac 上的目录登记为项目。 */
export const PROJECTS_ADD_METHOD = 'projects.add';
/** projects.validatePath：只校验路径并返回规范化结果，不写数据。 */
export const PROJECTS_VALIDATE_PATH_METHOD = 'projects.validatePath';
/** projects.clone：在宿主上 git clone 并登记成项目。 */
export const PROJECTS_CLONE_METHOD = 'projects.clone';
/** projects.listDir：浏览宿主子目录，供选择项目目录。 */
export const PROJECTS_LIST_DIR_METHOD = 'projects.listDir';
/** projects.changed：宿主添加项目后的广播，参数为 { projectID }。 */
export const PROJECTS_CHANGED_METHOD = 'projects.changed';

/** 克隆在宿主侧同步执行，宿主上限 10 分钟，客户端超时据此放宽。 */
const CLONE_TIMEOUT_MS = 10 * 60 * 1000;

/** 调用宿主用的远程调用函数，来自 useRemoteCall().call。 */
type RemoteCaller = UseRemoteCallResult['call'];

const toError = (err: unknown): Error => (err instanceof Error ? err : new Error(String(err)));

/**
 * 添加项目；返回宿主登记后的项目摘要（重复添加同一路径时返回已有项目）。
 * 参数不合法时宿主回 InvalidParams，错误信息是中文原因，可直接展示给用户。
 */
export async function addProject(call: RemoteCaller, path: string): Promise<RemoteProjectExtSummary> {
  const params: RemoteProjectExtAddParams = { path };
  const result = await call<RemoteProjectExtAddResult>(PROJECTS_ADD_METHOD, params).catch((err: unknown) => {
    throw toError(err);
  });
  const project = result?.project;
  if (!project || typeof project.id !== 'string' || project.id === '') {
    throw new Error('添加项目失败：宿主未返回项目信息');
  }
  return project;
}

/**
 * 校验路径，供输入框做防抖提示。
 * 返回 { ok, normalizedPath, reason }；结果形状不对时抛错，由调用方提示。
 */
export async function validateProjectPath(
  call: RemoteCaller,
  path: string
): Promise<RemoteProjectExtValidateResult> {
  const params: RemoteProjectExtValidateParams = { path };
  const result = await call<RemoteProjectExtValidateResult>(
    PROJECTS_VALIDATE_PATH_METHOD,
    params
  ).catch((err: unknown) => {
    throw toError(err);
  });
  if (!result || typeof result.ok !== 'boolean') {
    throw new Error('校验路径失败：宿主返回格式不正确');
  }
  return {
    ok: result.ok,
    normalizedPath: typeof result.normalizedPath === 'string' ? result.normalizedPath : '',
    reason: typeof result.reason === 'string' ? result.reason : ''
  };
}

/**
 * 在宿主上 git clone 一个仓库并登记成项目，返回登记后的项目摘要。
 * 宿主同步执行克隆，这里把 RPC 超时放宽到 10 分钟；
 * 目标目录已存在或克隆失败时宿主回错误，文案是中文原因，可直接展示给用户。
 */
export async function cloneProject(
  call: RemoteCaller,
  params: RemoteProjectExtCloneParams
): Promise<RemoteProjectExtSummary> {
  const request: RemoteProjectExtCloneParams = {
    url: params.url.trim(),
    ...(params.parentPath ? { parentPath: params.parentPath } : {}),
    ...(params.name?.trim() ? { name: params.name.trim() } : {})
  };
  const result = await call<RemoteProjectExtAddResult>(
    PROJECTS_CLONE_METHOD,
    request,
    CLONE_TIMEOUT_MS
  ).catch((err: unknown) => {
    throw toError(err);
  });
  const project = result?.project;
  if (!project || typeof project.id !== 'string' || project.id === '') {
    throw new Error('克隆项目失败：宿主未返回项目信息');
  }
  return project;
}

/**
 * 浏览宿主上的子目录，供远程选择项目目录。
 * path 缺省为用户主目录；只列目录、不列文件，超过 500 项时 truncated 为真。
 * 路径不在用户主目录内时宿主回 InvalidParams，此处抛错由调用方提示。
 */
export async function listDir(
  call: RemoteCaller,
  params: RemoteProjectExtListDirParams = {}
): Promise<RemoteProjectExtListDirResult> {
  const request: RemoteProjectExtListDirParams = {
    ...(params.path ? { path: params.path } : {}),
    ...(params.showHidden ? { showHidden: params.showHidden } : {})
  };
  const result = await call<RemoteProjectExtListDirResult>(PROJECTS_LIST_DIR_METHOD, request).catch(
    (err: unknown) => {
      throw toError(err);
    }
  );
  if (!result || typeof result.path !== 'string' || !Array.isArray(result.entries)) {
    throw new Error('读取目录失败：宿主返回格式不正确');
  }
  return {
    path: result.path,
    ...(typeof result.parent === 'string' && result.parent !== '' ? { parent: result.parent } : {}),
    entries: result.entries.filter(
      (entry) => entry && typeof entry.name === 'string' && typeof entry.path === 'string'
    ).map((entry) => ({
      name: entry.name,
      path: entry.path,
      isGitRepo: entry.isGitRepo === true
    })),
    truncated: result.truncated === true
  };
}
