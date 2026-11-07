// 「发送后置顶消息」偏好：对应宿主设置键 session.pin_sent_message（bool，默认开启）。
// 宿主不支持 settings.get、快照还没读到、或没有这个键时，一律按开启处理（与桌面端一致）。

import { useRemoteSettingsApi } from '../../context/remoteSettingsApi';

/** 设置键名，与宿主 internal/app/remote_settings.go 一致 */
export const PIN_SENT_MESSAGE_KEY = 'session.pin_sent_message';

/** 默认值：快照里没有这个值或宿主不支持 settings.get 时视为开启 */
export const PIN_SENT_MESSAGE_DEFAULT = true;

export function usePinSentMessage(): boolean {
  const { snapshot } = useRemoteSettingsApi();
  const value = snapshot?.values[PIN_SENT_MESSAGE_KEY];
  return typeof value === 'boolean' ? value : PIN_SENT_MESSAGE_DEFAULT;
}
