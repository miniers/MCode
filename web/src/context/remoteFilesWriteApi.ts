// 覆盖保存工作区文件（files.write），对应桌面端内置编辑器的保存动作，
// 宿主实现见 internal/remote/files_write_ext_methods.go，参数与结果类型见 internal/remote/wire/files_write_ext_types.go。
// 类型来自 web/src/types/generated.ts（由 docs/remote-control.schema.json 生成）。
// 只允许覆盖工作区内已存在的文本文件：不创建新文件、不写二进制，内容上限 512 KiB。
// expectedHash 取自 files.read 结果的 hash（整文件读取时才返回），用于避免覆盖别人刚写过的改动。
// 本模块只提供数据调用，不渲染任何 UI。

import { useCallback, useMemo } from 'react';
import type { RemoteFilesWriteParams, RemoteFilesWriteResult } from '../types/generated';
import { RpcError } from '../services/rpc';
import { useRemoteCall } from './useRemoteCall';

export type { RemoteFilesWriteParams, RemoteFilesWriteResult };

/** 覆盖保存工作区文件。 */
export const REMOTE_FILES_WRITE_METHOD = 'files.write';

/** 错误码与 internal/remote/wire/jsonrpc.go 保持一致。 */
/** 版本冲突：expectedHash 与磁盘当前内容不一致，或其它 CAS 冲突。 */
export const REMOTE_FILE_WRITE_CONFLICT_CODE = -32009;
/** 文件不存在或已被删除。 */
export const REMOTE_FILE_WRITE_NOT_FOUND_CODE = -32004;
/** 业务拒绝：二进制文件、受保护文件或写入失败。 */
export const REMOTE_FILE_WRITE_REJECTED_CODE = -32000;

/** 覆盖保存的远程调用集合。 */
export interface RemoteFilesWriteApi {
  /** 宿主是否提供 files.write；为假时不显示保存入口。 */
  supported: boolean;
  /** 整体覆盖文件内容，返回写入后的 hash 与字节数；失败时 reject。 */
  write: (params: RemoteFilesWriteParams) => Promise<RemoteFilesWriteResult>;
}

/** 判断是否为哈希冲突（文件已被别处改过，应先重新读取再保存）。 */
export function isFileWriteConflict(error: unknown): boolean {
  return error instanceof RpcError && error.code === REMOTE_FILE_WRITE_CONFLICT_CODE;
}

/** 把写入失败转成给用户看的一句话；冲突、文件不存在与其它拒绝各有提示。 */
export function filesWriteErrorText(error: unknown, fallback = '保存失败'): string {
  if (error instanceof RpcError) {
    if (error.code === REMOTE_FILE_WRITE_CONFLICT_CODE) return '文件已被别处改过，请重新读取后再保存';
    if (error.code === REMOTE_FILE_WRITE_NOT_FOUND_CODE) return '文件不存在或已被删除';
    if (error.code === REMOTE_FILE_WRITE_REJECTED_CODE) return '这个文件不能远程保存（二进制或写入被拒绝）';
    if (error.message) return error.message;
  }
  if (error instanceof Error && error.message) return error.message;
  return fallback;
}

/** 取覆盖保存的调用集合；能力与 call 不变时返回同一对象。 */
export function useRemoteFilesWriteApi(): RemoteFilesWriteApi {
  const { call, supportsMethod } = useRemoteCall();
  const supported = supportsMethod(REMOTE_FILES_WRITE_METHOD);

  const write = useCallback(
    (params: RemoteFilesWriteParams) =>
      call<RemoteFilesWriteResult>(REMOTE_FILES_WRITE_METHOD, params),
    [call]
  );

  return useMemo<RemoteFilesWriteApi>(() => ({ supported, write }), [supported, write]);
}
