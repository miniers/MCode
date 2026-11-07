import React, { useState } from 'react';
import { XMarkIcon } from './Icons';
import { usePushSubscription } from '../hooks/usePushSubscription';

export const PushEnableHint: React.FC = () => {
  const { supported, subscribed, permission, working, errorMessage, enable } = usePushSubscription({
    syncExisting: true
  });

  const storageKey = 'mcode.hint.push.dismissed';

  const [dismissed, setDismissed] = useState<boolean>(() => {
    if (typeof window === 'undefined') return false;
    try {
      return window.localStorage.getItem(storageKey) === '1';
    } catch {
      return false;
    }
  });

  const isDenied = permission === 'denied';

  if (!supported || subscribed || isDenied || dismissed) {
    return null;
  }

  const handleDismiss = () => {
    try {
      window.localStorage.setItem(storageKey, '1');
    } catch {
      // 写入失败时仅在本次会话内隐藏
    }
    setDismissed(true);
  };

  return (
    <aside className="entry-hint" role="note">
      <div className="entry-hint-content min-w-0 flex-1">
        <strong className="entry-hint-title">开启通知</strong>
        <p className="entry-hint-body">会话等待你处理或运行结束时，在这台设备上收到通知。</p>
        {errorMessage && <p className="entry-hint-error">{errorMessage}</p>}
        <button
          type="button"
          className="entry-hint-action"
          disabled={working}
          onClick={() => void enable()}
        >
          {working ? '正在开启…' : '开启'}
        </button>
      </div>
      <button
        type="button"
        className="entry-hint-close"
        aria-label="关闭提示"
        onClick={handleDismiss}
      >
        <XMarkIcon className="w-4 h-4" />
      </button>
    </aside>
  );
};

export default PushEnableHint;
