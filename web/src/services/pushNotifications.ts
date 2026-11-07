// 页面侧推送通知清理与角标：能力缺失或调用失败一律静默跳过。

/**
 * 关闭某条待处理请求的系统通知；页面作答、其它设备作答后都经由这里清理。
 */
export function closeRequestNotification(requestID: string): void {
  if (!requestID || typeof navigator === 'undefined' || !('serviceWorker' in navigator)) return;

  try {
    navigator.serviceWorker.controller?.postMessage({ type: 'mcode-request-settled', requestID });
  } catch {
    // 首屏尚未被 SW 接管时没有 controller，交给下面的 getNotifications 兜底。
  }

  navigator.serviceWorker.ready
    .then((registration) => registration.getNotifications())
    .then((notifications) => {
      for (const notification of notifications) {
        if (notification.tag === requestID || notification.data?.requestID === requestID) {
          notification.close();
        }
      }
    })
    .catch(() => {
      // 未授权通知或环境不支持通知查询时忽略。
    });
}

/**
 * 待处理请求数写入应用角标；不支持 App Badge API 的环境静默跳过。
 */
export function setPendingBadge(count: number): void {
  if (typeof navigator === 'undefined') return;
  try {
    if (count > 0) {
      void navigator.setAppBadge?.(count);
    } else {
      void navigator.clearAppBadge?.();
    }
  } catch {
    // 桌面 Firefox 等环境没有角标 API。
  }
}
