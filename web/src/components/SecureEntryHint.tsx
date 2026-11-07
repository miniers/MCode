import React, { useState } from 'react';
import { XMarkIcon } from './Icons';
import { getInstallEnvironment, installHintText } from '../utils/installHint';

export const SecureEntryHint: React.FC = () => {
  const env = getInstallEnvironment();

  const storageKey = !env.secure ? 'mcode.hint.insecure.dismissed' : 'mcode.hint.install.dismissed';

  const [dismissed, setDismissed] = useState<boolean>(() => {
    if (typeof window === 'undefined') return false;
    try {
      return window.localStorage.getItem(storageKey) === '1';
    } catch {
      return false;
    }
  });

  if (env.standalone || dismissed) {
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

  const { title, body } = installHintText(env);

  return (
    <aside className="entry-hint" role="note">
      <div className="entry-hint-content min-w-0 flex-1">
        <strong className="entry-hint-title">{title}</strong>
        <p className="entry-hint-body">{body}</p>
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

export default SecureEntryHint;
