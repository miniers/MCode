// 撤销某一轮文件改动的远程调用集合（threads.revert.preview / threads.revert.apply），
// 对应桌面端 internal/ui/client.go 的 RevertPreview（内部调用 Client.PreviewRevert / ExecuteRevert，
// 后端实现见 internal/remote/memory_backend.go 的 PreviewTurnRevert / ExecuteTurnRevert）。
// 类型来自 web/src/types/generated.ts（由 docs/remote-control.schema.json 生成）。
// 只有预览与执行两个方法都在 capabilities.methods 里时才认为宿主支持撤销。
// 本模块只提供数据调用，不渲染任何 UI。

import { useCallback, useMemo } from 'react';
import type {
  RemoteThreadsRevertApplyResult,
  RemoteThreadsRevertPreviewResult
} from '../types/generated';
import { useRemoteCall } from './useRemoteCall';

export type { RemoteThreadsRevertApplyResult, RemoteThreadsRevertPreviewResult };

/** 预览一轮改动的回退范围，不落盘。 */
export const REMOTE_REVERT_PREVIEW_METHOD = 'threads.revert.preview';
/** 真正执行回退。 */
export const REMOTE_REVERT_APPLY_METHOD = 'threads.revert.apply';

/** 一轮改动回退的远程调用集合。 */
export interface RemoteRevertApi {
  /** 宿主是否同时提供预览与执行；为假时不显示撤销入口。 */
  supported: boolean;
  /** 预览该轮会改动哪些文件；会话运行中宿主返回错误，文案可直接展示。 */
  preview: (threadID: string, turnID: string) => Promise<RemoteThreadsRevertPreviewResult>;
  /** 执行回退；成功时 ok 为真，kept/files 与预览一致便于直接刷新界面。 */
  apply: (threadID: string, turnID: string) => Promise<RemoteThreadsRevertApplyResult>;
}

/** 取回退调用集合；能力与 call 不变时返回同一对象。 */
export function useRemoteRevertApi(): RemoteRevertApi {
  const { call, supportsMethod } = useRemoteCall();
  const supported =
    supportsMethod(REMOTE_REVERT_PREVIEW_METHOD) && supportsMethod(REMOTE_REVERT_APPLY_METHOD);

  const preview = useCallback(
    (threadID: string, turnID: string) =>
      call<RemoteThreadsRevertPreviewResult>(REMOTE_REVERT_PREVIEW_METHOD, { threadID, turnID }),
    [call]
  );

  const apply = useCallback(
    (threadID: string, turnID: string) =>
      call<RemoteThreadsRevertApplyResult>(REMOTE_REVERT_APPLY_METHOD, { threadID, turnID }),
    [call]
  );

  return useMemo<RemoteRevertApi>(() => ({ supported, preview, apply }), [supported, preview, apply]);
}
