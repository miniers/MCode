import type {
  RemoteFilesCreateParams, RemoteFilesCreateResult,
  RemoteFilesRenameParams, RemoteFilesRenameResult,
  RemoteFilesTrashParams, RemoteFilesTrashResult,
  RemoteFilesDuplicateParams, RemoteFilesDuplicateResult
} from '../types/generated';
import { useRemoteStatic } from './RemoteContext';
import { useRemoteCall } from './useRemoteCall';

export function filesOpsSupport(methods: readonly string[] | undefined) {
  return {
    create: Boolean(methods?.includes('files.create')),
    rename: Boolean(methods?.includes('files.rename')),
    trash: Boolean(methods?.includes('files.trash')),
    duplicate: Boolean(methods?.includes('files.duplicate'))
  };
}

type Call = <T>(method: string, params?: unknown) => Promise<T>;

export function createRemoteFilesOpsApi(call: Call, methods: readonly string[] | undefined) {
  const supported = filesOpsSupport(methods);
  const invoke = <T>(method: keyof typeof supported, params: unknown): Promise<T> => {
    if (!supported[method]) return Promise.reject(new Error(`宿主未声明 files.${method} 能力`));
    return call<T>(`files.${method}`, params);
  };
  return {
    supported,
    create: (params: RemoteFilesCreateParams) => invoke<RemoteFilesCreateResult>('create', params),
    rename: (params: RemoteFilesRenameParams) => invoke<RemoteFilesRenameResult>('rename', params),
    trash: (params: RemoteFilesTrashParams) => invoke<RemoteFilesTrashResult>('trash', params),
    duplicate: (params: RemoteFilesDuplicateParams) => invoke<RemoteFilesDuplicateResult>('duplicate', params)
  };
}

export function useRemoteFilesOpsApi() {
  const { capabilities } = useRemoteStatic();
  const { call } = useRemoteCall();
  return createRemoteFilesOpsApi(call, capabilities?.methods);
}
