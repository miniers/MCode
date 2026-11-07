// Markdown 图片渲染的会话上下文：本地图片必须借会话工作区才能经 files.read 读取，
// 不在会话里的 Markdown（其他面板）没有 threadID，只能回退成链接。

import React, { createContext, useContext, useMemo } from 'react';

export interface MarkdownImageContextValue {
  /** 当前渲染所处的会话 ID；不在会话里时为 null */
  threadID: string | null;
}

const DEFAULT_VALUE: MarkdownImageContextValue = { threadID: null };

const MarkdownImageContext = createContext<MarkdownImageContextValue>(DEFAULT_VALUE);

export const MarkdownImageProvider: React.FC<{
  threadID: string | null;
  children: React.ReactNode;
}> = ({ threadID, children }) => {
  const value = useMemo<MarkdownImageContextValue>(() => ({ threadID }), [threadID]);
  return <MarkdownImageContext.Provider value={value}>{children}</MarkdownImageContext.Provider>;
};

export function useMarkdownImageThreadID(): string | null {
  return useContext(MarkdownImageContext).threadID;
}

export default MarkdownImageContext;
