// 设置 › 此设备 › 推送通知行的状态映射，抽成纯函数便于单独测试。
import type { PushPermission } from '../hooks/usePushSubscription';

export type PushRowControl = 'none' | 'enabled' | 'button';

export interface PushRowState {
  supported: boolean;
  secure: boolean;
  ios: boolean;
  standalone: boolean;
  subscribed: boolean;
  permission: PushPermission;
}

export interface PushRowView {
  desc: string;
  control: PushRowControl;
}

export const PUSH_READY_DESC = '会话等待你处理或运行结束时，在这台设备上收到通知';

export function describePushRow(state: PushRowState): PushRowView {
  if (!state.supported) {
    if (!state.secure) {
      return { desc: '通知需要 HTTPS 入口', control: 'none' };
    }
    if (state.ios && !state.standalone) {
      return { desc: 'iOS 需先添加到主屏幕，并从主屏幕打开后才能开启通知', control: 'none' };
    }
    return { desc: '当前浏览器或 Mac 端不支持推送通知', control: 'none' };
  }

  if (state.subscribed) {
    return { desc: PUSH_READY_DESC, control: 'enabled' };
  }

  if (state.permission === 'denied') {
    return {
      desc: '通知权限已被浏览器拒绝，请在浏览器或系统设置里允许本站通知后重试',
      control: 'none'
    };
  }

  return { desc: PUSH_READY_DESC, control: 'button' };
}
