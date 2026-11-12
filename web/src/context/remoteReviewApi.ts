import { useCallback, useMemo } from 'react';
import type {
  RemoteReviewScope,
  RemoteThreadsReviewGetParams,
  RemoteThreadsReviewGetResult,
  RemoteThreadsReviewRevertHunkParams,
  RemoteThreadsReviewRevertHunkResult
} from '../types/generated';
import { useRemoteCall } from './useRemoteCall';

export const REMOTE_REVIEW_GET_METHOD = 'threads.review.get';
export const REMOTE_REVIEW_REVERT_HUNK_METHOD = 'threads.review.revertHunk';

export interface RemoteReviewApi {
  getSupported: boolean;
  revertHunkSupported: boolean;
  get: (params: RemoteThreadsReviewGetParams) => Promise<RemoteThreadsReviewGetResult>;
  revertHunk: (params: RemoteThreadsReviewRevertHunkParams) => Promise<RemoteThreadsReviewRevertHunkResult>;
}

export function useRemoteReviewApi(): RemoteReviewApi {
  const { call, supportsMethod } = useRemoteCall();
  const getSupported = supportsMethod(REMOTE_REVIEW_GET_METHOD);
  const revertHunkSupported = supportsMethod(REMOTE_REVIEW_REVERT_HUNK_METHOD);
  const get = useCallback(
    (params: RemoteThreadsReviewGetParams) => {
      if (!supportsMethod(REMOTE_REVIEW_GET_METHOD)) throw new Error('当前宿主未声明 threads.review.get');
      return call<RemoteThreadsReviewGetResult>(REMOTE_REVIEW_GET_METHOD, params);
    },
    [call, supportsMethod]
  );
  const revertHunk = useCallback(
    (params: RemoteThreadsReviewRevertHunkParams) => {
      if (!supportsMethod(REMOTE_REVIEW_REVERT_HUNK_METHOD)) throw new Error('当前宿主未声明 threads.review.revertHunk');
      return call<RemoteThreadsReviewRevertHunkResult>(REMOTE_REVIEW_REVERT_HUNK_METHOD, params);
    },
    [call, supportsMethod]
  );
  return useMemo(() => ({ getSupported, revertHunkSupported, get, revertHunk }), [getSupported, revertHunkSupported, get, revertHunk]);
}

export async function reviewContentHash(text: string): Promise<string> {
  return sha256(new TextEncoder().encode(text));
}

export async function reviewHunkHash(text: string, startLine: number, lineCount: number): Promise<string> {
  const lines = splitReviewLines(text);
  if (startLine < 1 || lineCount < 0 || startLine - 1 > lines.length || startLine - 1 + lineCount > lines.length) {
    throw new Error('无效的审阅块');
  }
  return sha256(new TextEncoder().encode(lines.slice(startLine - 1, startLine - 1 + lineCount).join('')));
}

function splitReviewLines(text: string): string[] {
  if (!text) return [];
  const final = text.endsWith('\n');
  const lines = text.split('\n');
  if (final) lines.pop();
  const result = lines.map((line) => `${line}\n`);
  if (!final && result.length > 0) result[result.length - 1] = result[result.length - 1].slice(0, -1);
  return result;
}

async function sha256(data: Uint8Array): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', data.slice().buffer as ArrayBuffer);
  return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, '0')).join('');
}

export type ReviewScope = RemoteReviewScope;
