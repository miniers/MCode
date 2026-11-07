// 「添加到主屏幕」提示的环境判定与文案，首页卡片与设置页共用。
export interface InstallEnvironment {
  /** 是否 HTTPS（安全上下文）。 */
  secure: boolean;
  /** 是否已从主屏幕打开。 */
  standalone: boolean;
  /** 是否 iOS（含 iPad 桌面模式）。 */
  ios: boolean;
}

export function getInstallEnvironment(): InstallEnvironment {
  const secure = typeof window !== 'undefined' ? window.isSecureContext : true;
  const standalone = typeof window !== 'undefined' && Boolean(
    (typeof window.matchMedia === 'function' && window.matchMedia('(display-mode: standalone)').matches) ||
    (navigator as Navigator & { standalone?: boolean }).standalone === true
  );
  const ios = typeof navigator !== 'undefined' && (
    /iPad|iPhone|iPod/.test(navigator.userAgent) ||
    (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1)
  );

  return { secure, standalone, ios };
}

export function installHintText(env: InstallEnvironment): { title: string; body: string } {
  const title = !env.secure ? '当前为明文入口' : '添加到主屏幕';
  const body = !env.secure
    ? '安装到主屏幕和通知需要 HTTPS。可在 Mac 的 设置 › 远程控制 中开启 Tailscale，或在「自定义域名 HTTPS」中配置域名，然后用 https 地址重新打开。'
    : env.ios
    ? '点 Safari 底部的分享按钮，选择“添加到主屏幕”。从主屏幕打开后才能接收通知，并需要在应用内重新配对一次。'
    : '在浏览器菜单里选择“安装应用”或“添加到主屏幕”，之后可像应用一样打开。';

  return { title, body };
}
