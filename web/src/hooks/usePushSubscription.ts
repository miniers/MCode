// 推送订阅逻辑：首页提示卡片与设置 › 此设备 › 推送通知共用。
import { useEffect, useRef, useState } from 'react';
import { useRemoteStatic } from '../context/RemoteContext';

export type PushPermission = 'granted' | 'denied' | 'default' | 'unsupported';

// 任一处开启成功后派发，另一处据此立即显示「已开启」。
export const PUSH_SUBSCRIBED_EVENT = 'mcode:push-subscribed';

interface UsePushSubscriptionOptions {
  /** true：挂载时把已有订阅重新上报给服务端；false：只做只读检查。 */
  syncExisting?: boolean;
}

function urlBase64ToUint8Array(base64String: string): Uint8Array<ArrayBuffer> {
  const padding = '='.repeat((4 - (base64String.length % 4)) % 4);
  const base64 = (base64String + padding)
    .replace(/-/g, '+')
    .replace(/_/g, '/');
  const rawData = window.atob(base64);
  const outputArray = new Uint8Array(rawData.length);
  for (let i = 0; i < rawData.length; ++i) {
    outputArray[i] = rawData.charCodeAt(i);
  }
  return outputArray;
}

// 订阅用的公钥是否就是服务端当前这把：逐字节比较，不同则说明服务端换了 key。
function applicationServerKeyMatches(
  subscription: PushSubscription,
  applicationServerKey: Uint8Array<ArrayBuffer>
): boolean {
  const current = subscription.options.applicationServerKey;
  if (!current) return false;
  const currentBytes = new Uint8Array(current);
  if (currentBytes.byteLength !== applicationServerKey.byteLength) return false;
  for (let i = 0; i < currentBytes.byteLength; i++) {
    if (currentBytes[i] !== applicationServerKey[i]) return false;
  }
  return true;
}

function readPermission(): PushPermission {
  if (typeof Notification === 'undefined') return 'unsupported';
  return Notification.permission;
}

export function usePushSubscription(options?: UsePushSubscriptionOptions) {
  const syncExisting = options?.syncExisting ?? false;
  const { capabilities, connectionState, getPushPublicKey, subscribePush } = useRemoteStatic();

  const supported = Boolean(
    typeof window !== 'undefined' &&
    window.isSecureContext &&
    'serviceWorker' in navigator &&
    'PushManager' in window &&
    'Notification' in window &&
    capabilities?.methods.includes('push.subscribe')
  );

  const [subscribed, setSubscribed] = useState(false);
  const [permission, setPermission] = useState<PushPermission>(readPermission);
  const [working, setWorking] = useState(false);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  const hasReRegisteredRef = useRef(false);

  // 别处（首页卡片或设置行）开启成功后同步状态。
  useEffect(() => {
    if (typeof window === 'undefined') return;
    const handleSubscribed = () => setSubscribed(true);
    window.addEventListener(PUSH_SUBSCRIBED_EVENT, handleSubscribed);
    return () => window.removeEventListener(PUSH_SUBSCRIBED_EVENT, handleSubscribed);
  }, []);

  useEffect(() => {
    if (!supported || !syncExisting) return;

    let cancelled = false;

    const syncExistingSubscription = async () => {
      try {
        const registration = await navigator.serviceWorker.ready;
        const subscription = await registration.pushManager.getSubscription();
        if (cancelled) return;

        if (subscription && typeof Notification !== 'undefined' && Notification.permission === 'granted') {
          setSubscribed(true);

          if (!hasReRegisteredRef.current && connectionState === 'connected') {
            const { publicKey } = await getPushPublicKey();
            const applicationServerKey = urlBase64ToUint8Array(publicKey);
            if (cancelled) return;

            // 服务端换了公钥时旧订阅推不动：退掉再按当前公钥重新订阅。
            let active = subscription;
            if (!applicationServerKeyMatches(subscription, applicationServerKey)) {
              await subscription.unsubscribe();
              active = await registration.pushManager.subscribe({
                userVisibleOnly: true,
                applicationServerKey
              });
            }

            const subJson = active.toJSON();
            if (subJson.endpoint && subJson.keys?.p256dh && subJson.keys?.auth) {
              await subscribePush({
                endpoint: subJson.endpoint,
                p256dh: subJson.keys.p256dh,
                auth: subJson.keys.auth
              });
              // 只有上报成功才记下，失败时保持 false，下次连接变化还能重试。
              hasReRegisteredRef.current = true;
            }
          }
        }
      } catch {
        // 忽略自动重新注册时的异常
      }
    };

    void syncExistingSubscription();

    return () => {
      cancelled = true;
    };
  }, [supported, syncExisting, connectionState, subscribePush]);

  // 设置页只读：不重新上报，避免与首页卡片重复调用。
  useEffect(() => {
    if (!supported || syncExisting) return;

    let cancelled = false;

    const checkExistingSubscription = async () => {
      try {
        const registration = await navigator.serviceWorker.ready;
        const subscription = await registration.pushManager.getSubscription();
        if (cancelled) return;
        if (subscription && typeof Notification !== 'undefined' && Notification.permission === 'granted') {
          setSubscribed(true);
        }
      } catch {
        // 只读检查失败时保持未订阅
      }
    };

    void checkExistingSubscription();

    return () => {
      cancelled = true;
    };
  }, [supported, syncExisting]);

  const enable = async () => {
    if (working) return;
    setWorking(true);
    setErrorMessage(null);

    try {
      const result = await Notification.requestPermission();
      setPermission(result);
      if (result !== 'granted') {
        setErrorMessage('通知权限未开启');
        return;
      }

      const { publicKey } = await getPushPublicKey();
      const applicationServerKey = urlBase64ToUint8Array(publicKey);

      const registration = await navigator.serviceWorker.ready;
      const subscription = await registration.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey
      });

      const json = subscription.toJSON();
      const endpoint = json.endpoint;
      const p256dh = json.keys?.p256dh;
      const auth = json.keys?.auth;

      if (!endpoint || !p256dh || !auth) {
        setErrorMessage('订阅信息不完整');
        return;
      }

      await subscribePush({ endpoint, p256dh, auth });
      setSubscribed(true);
      if (typeof window !== 'undefined') {
        window.dispatchEvent(new Event(PUSH_SUBSCRIBED_EVENT));
      }
    } catch (error) {
      setErrorMessage(error instanceof Error ? error.message : '开启通知失败');
    } finally {
      setWorking(false);
    }
  };

  return { supported, subscribed, permission, working, errorMessage, enable };
}
