// 设置 › 此设备 › 推送通知行的状态映射测试。和 useLongPress.test.ts 一样：
// 不依赖测试框架，断言失败直接抛错。
import { describePushRow, PUSH_READY_DESC, type PushRowState } from '../../utils/pushRow';

function assertEqual(label: string, actual: unknown, expected: unknown): void {
  if (actual !== expected) {
    throw new Error(`${label}: 期望 ${String(expected)}，实际 ${String(actual)}`);
  }
}

const base: PushRowState = {
  supported: true,
  secure: true,
  ios: false,
  standalone: false,
  subscribed: false,
  permission: 'default'
};

// 浏览器或 Mac 端不支持：不显示按钮。
const unsupported = describePushRow({ ...base, supported: false });
assertEqual('不支持说明', unsupported.desc, '当前浏览器或 Mac 端不支持推送通知');
assertEqual('不支持控件', unsupported.control, 'none');

// iOS 未添加到主屏幕：先安装再开通知。
const iosUninstalled = describePushRow({ ...base, supported: false, ios: true });
assertEqual(
  'iOS 未安装说明',
  iosUninstalled.desc,
  'iOS 需先添加到主屏幕，并从主屏幕打开后才能开启通知'
);
assertEqual('iOS 未安装控件', iosUninstalled.control, 'none');

// 权限被拒：提示去浏览器或系统设置放开。
const denied = describePushRow({ ...base, permission: 'denied' });
assertEqual(
  '权限被拒说明',
  denied.desc,
  '通知权限已被浏览器拒绝，请在浏览器或系统设置里允许本站通知后重试'
);
assertEqual('权限被拒控件', denied.control, 'none');

// 默认状态：显示开启按钮。
const notSubscribed = describePushRow(base);
assertEqual('未订阅说明', notSubscribed.desc, PUSH_READY_DESC);
assertEqual('未订阅控件', notSubscribed.control, 'button');

// 已订阅：显示「已开启」。
const subscribed = describePushRow({ ...base, permission: 'granted', subscribed: true });
assertEqual('已订阅说明', subscribed.desc, PUSH_READY_DESC);
assertEqual('已订阅控件', subscribed.control, 'enabled');
