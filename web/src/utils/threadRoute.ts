/**
 * 会话路由解析：#/t/<threadID> 之后允许带 ?m=<messageID> 与 &r=<requestID>，
 * 供系统推送点开时直达消息或审批卡。老链接不带查询串，行为不变。
 */
export interface ThreadRoute {
  threadID: string;
  messageID?: string;
  requestID?: string;
}

const THREAD_ROUTE = /^\/t\/([^/?#]+)(?:\?([^#]*))?$/;

/** parseThreadRoute 解析去掉 # 后的 hash；不是会话路由或编码非法时返回 null。 */
export function parseThreadRoute(hash: string): ThreadRoute | null {
  const value = (hash || '').replace(/^#/, '');
  const match = value.match(THREAD_ROUTE);
  if (!match) return null;
  let threadID: string;
  try {
    threadID = decodeURIComponent(match[1]);
  } catch {
    return null;
  }
  if (!threadID) return null;
  const params = new URLSearchParams(match[2] ?? '');
  const messageID = params.get('m') || '';
  const requestID = params.get('r') || '';
  return {
    threadID,
    ...(messageID ? { messageID } : {}),
    ...(requestID ? { requestID } : {})
  };
}
