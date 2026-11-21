// 项目知识库只读文档 Hook：拉取文档列表（context.listDocs）与按需读取正文（context.readDoc），
// 按 `${projectID}:${name}` 缓存已读结果并包含项目切换与关闭时的竞态保护。

import { useCallback, useEffect, useRef, useState } from 'react';
import { useRemoteCall } from '../context/useRemoteCall';
import type {
  RemoteContextDocSummary,
  RemoteContextListDocsParams,
  RemoteContextListDocsResult,
  RemoteContextReadDocParams,
  RemoteContextReadDocResult
} from '../types/generated';

export interface UseProjectContextDocsResult {
  docs: RemoteContextDocSummary[];
  selectedName: string | null;
  doc: RemoteContextReadDocResult | null;
  loadingList: boolean;
  loadingDoc: boolean;
  error: string | null;
  supported: boolean;
  selectDoc: (name: string) => void;
  refresh: () => void;
}

export function contextDocCacheKey(projectID: string, name: string): string {
  return `${projectID}:${name}`;
}

export function useProjectContextDocs(
  projectID: string | null,
  enabled: boolean
): UseProjectContextDocsResult {
  const { call, supportsMethod } = useRemoteCall();
  const listSupported = supportsMethod('context.listDocs');
  const readSupported = supportsMethod('context.readDoc');
  const supported = listSupported && readSupported;

  const [docs, setDocs] = useState<RemoteContextDocSummary[]>([]);
  const [selectedName, setSelectedName] = useState<string | null>(null);
  const [docCache, setDocCache] = useState<Record<string, RemoteContextReadDocResult>>({});
  const [loadingList, setLoadingList] = useState(false);
  const [loadingDoc, setLoadingDoc] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const docCacheRef = useRef(docCache);
  docCacheRef.current = docCache;
  const selectedNameRef = useRef(selectedName);
  selectedNameRef.current = selectedName;
  const scopeRef = useRef({ projectID, enabled, supported });
  scopeRef.current = { projectID, enabled, supported };

  const listReqRef = useRef(0);
  const docReqRef = useRef(0);

  const loadList = useCallback(
    async (targetProjectID: string) => {
      const reqID = ++listReqRef.current;
      setLoadingList(true);
      setError(null);
      try {
        const params: RemoteContextListDocsParams = { projectID: targetProjectID };
        const result = await call<RemoteContextListDocsResult>('context.listDocs', params);
        const scope = scopeRef.current;
        if (
          reqID !== listReqRef.current ||
          !scope.enabled ||
          !scope.supported ||
          scope.projectID !== targetProjectID
        ) {
          return;
        }
        setDocs(Array.isArray(result?.docs) ? result.docs : []);
      } catch (err) {
        const scope = scopeRef.current;
        if (
          reqID !== listReqRef.current ||
          !scope.enabled ||
          !scope.supported ||
          scope.projectID !== targetProjectID
        ) {
          return;
        }
        setError(err instanceof Error ? err.message : '加载项目文档列表失败');
      } finally {
        if (reqID === listReqRef.current) {
          setLoadingList(false);
        }
      }
    },
    [call]
  );

  const loadDoc = useCallback(
    async (targetProjectID: string, name: string) => {
      const reqID = ++docReqRef.current;
      setLoadingDoc(true);
      setError(null);
      try {
        const params: RemoteContextReadDocParams = { projectID: targetProjectID, name };
        const result = await call<RemoteContextReadDocResult>('context.readDoc', params);
        const scope = scopeRef.current;
        if (
          reqID !== docReqRef.current ||
          !scope.enabled ||
          !scope.supported ||
          scope.projectID !== targetProjectID ||
          selectedNameRef.current !== name
        ) {
          return;
        }
        if (result) {
          const key = contextDocCacheKey(targetProjectID, name);
          docCacheRef.current = { ...docCacheRef.current, [key]: result };
          setDocCache(docCacheRef.current);
        }
      } catch (err) {
        const scope = scopeRef.current;
        if (
          reqID !== docReqRef.current ||
          !scope.enabled ||
          !scope.supported ||
          scope.projectID !== targetProjectID ||
          selectedNameRef.current !== name
        ) {
          return;
        }
        setError(err instanceof Error ? err.message : '读取项目文档失败');
      } finally {
        if (reqID === docReqRef.current) {
          setLoadingDoc(false);
        }
      }
    },
    [call]
  );

  // 切换项目时重置当前列表与选中文档；禁用或切走时作废在途请求。
  useEffect(() => {
    setDocs([]);
    setSelectedName(null);
    selectedNameRef.current = null;
    setError(null);
    setLoadingList(false);
    setLoadingDoc(false);
    return () => {
      listReqRef.current += 1;
      docReqRef.current += 1;
    };
  }, [projectID]);

  useEffect(() => {
    if (!enabled || !projectID || !supported) {
      listReqRef.current += 1;
      docReqRef.current += 1;
      setLoadingList(false);
      setLoadingDoc(false);
      return;
    }
    void loadList(projectID);
    return () => {
      listReqRef.current += 1;
      docReqRef.current += 1;
    };
  }, [enabled, loadList, projectID, supported]);

  const selectDoc = useCallback(
    (name: string) => {
      setSelectedName(name);
      selectedNameRef.current = name;
      setError(null);
      if (!enabled || !projectID || !supported) return;
      const key = contextDocCacheKey(projectID, name);
      if (docCacheRef.current[key]) {
        docReqRef.current += 1;
        setLoadingDoc(false);
        return;
      }
      void loadDoc(projectID, name);
    },
    [enabled, loadDoc, projectID, supported]
  );

  const refresh = useCallback(() => {
    if (!enabled || !projectID || !supported) return;
    void loadList(projectID);
    const activeName = selectedNameRef.current;
    if (activeName) {
      void loadDoc(projectID, activeName);
    }
  }, [enabled, loadDoc, loadList, projectID, supported]);

  const doc =
    projectID && selectedName
      ? docCache[contextDocCacheKey(projectID, selectedName)] ?? null
      : null;

  return {
    docs,
    selectedName,
    doc,
    loadingList,
    loadingDoc,
    error,
    supported,
    selectDoc,
    refresh
  };
}
