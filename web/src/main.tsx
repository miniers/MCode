// React 入口文件，挂载应用与条件式安全注册 Service Worker

import ReactDOM from 'react-dom/client';
import App from './App';
import './index.css';
import { computeViewport, isEditableElement } from './utils/viewport';
// 数学公式（KaTeX）样式不再全局引入：由 utils/lazyMath.ts 在首次需要渲染公式时按需加载

// 禁用双指捏合和双击缩放，保持单指正常滑动、文本选中与行交互
if (typeof document !== 'undefined') {
  document.documentElement.style.touchAction = 'pan-x pan-y';
  const preventGesture = (event: Event) => event.preventDefault();
  document.addEventListener('gesturestart', preventGesture, { passive: false });
  document.addEventListener('gesturechange', preventGesture, { passive: false });
  document.addEventListener('gestureend', preventGesture, { passive: false });
  document.addEventListener(
    'touchmove',
    (event: TouchEvent) => {
      if (event.touches.length > 1) {
        event.preventDefault();
      }
    },
    { passive: false }
  );
}

// 维护 --app-height 与 --composer-bottom-inset。软键盘弹出时跟随可视视口。
// 视口补充分两种状态栏处理：半透明状态栏（env(safe-area-inset-top) > 0）下 iOS 的
// window.innerHeight 会少算顶部安全区，底部空出一截，这里按物理屏幕高度补回；
// default 状态栏安全区为 0，视口本来就完整，不做补偿（否则会多算一个状态栏高度）。
// 已安装的 PWA 可能缓存旧的 black-translucent meta，所以以实际安全区为准。
// --composer-bottom-inset 是输入面板下边缘到外壳底边的唯一底部间距：
// 软键盘收起时固定 26px（留给 Home 条），弹起时距键盘 8px。
if (typeof window !== 'undefined') {
  // iPhone/iPad/iPod；iPadOS 桌面模式的 UA 与 Mac 相同，靠触点数量区分
  const isIOS =
    /iPhone|iPad|iPod/.test(navigator.userAgent) ||
    (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);
  const standalone =
    window.matchMedia('(display-mode: standalone)').matches ||
    (navigator as Navigator & { standalone?: boolean }).standalone === true;

  // 探针只需一个：挂到 body 上读 padding-top，把 env(safe-area-inset-top) 变成像素值
  let safeAreaProbe: HTMLDivElement | null = null;
  const readSafeAreaTop = (): number => {
    if (!document.body) return 0;
    if (!safeAreaProbe) {
      safeAreaProbe = document.createElement('div');
      safeAreaProbe.style.cssText =
        'position:fixed;top:0;left:0;width:0;height:0;visibility:hidden;pointer-events:none;padding-top:env(safe-area-inset-top);';
      document.body.appendChild(safeAreaProbe);
    }
    return parseFloat(getComputedStyle(safeAreaProbe).paddingTop) || 0;
  };

  // 键盘弹起期间定时复查：聚焦的输入框被卸载时 iOS 不发 focusout，
  // 键盘收起也可能不发 resize，没有复查 --app-height 会一直停在键盘高度（页面只剩半屏）
  let keyboardWatchTimer = 0;

  // 判定逻辑全在 utils/viewport.ts：只有可视视口明显变矮且焦点在可编辑元素上才算键盘
  const updateViewport = () => {
    const { height, keyboardOpen } = computeViewport({
      windowHeight: window.innerHeight,
      visualViewportHeight: window.visualViewport?.height ?? null,
      editableFocused: isEditableElement(document.activeElement),
      isIOS,
      standalone,
      screenWidth: window.screen.width,
      screenHeight: window.screen.height,
      innerWidth: window.innerWidth,
      safeAreaTop: readSafeAreaTop()
    });

    const root = document.documentElement.style;
    root.setProperty('--app-height', `${Math.round(height)}px`);
    // 唯一底部间距：键盘关闭时表单外框下缘到底边总计 26px（留给 Home 条），弹起时距键盘上缘 8px
    root.setProperty('--composer-bottom-inset', keyboardOpen ? '8px' : '26px');
    // 给只在键盘收起时显示的悬浮元素（如渠道额度条）用
    document.documentElement.toggleAttribute('data-keyboard-open', keyboardOpen);
    if (window.scrollY > 0 || window.scrollX > 0) window.scrollTo(0, 0);
    window.clearTimeout(keyboardWatchTimer);
    if (keyboardOpen) keyboardWatchTimer = window.setTimeout(updateViewport, 500);
  };
  // iOS 从后台切回、键盘收起动画结束时都可能不触发 visualViewport resize，
  // 所以在这些时机主动补算一次，焦点变化后再延迟补算覆盖动画耗时
  let settleTimer = 0;
  const updateViewportLater = (delay = 300) => {
    window.clearTimeout(settleTimer);
    settleTimer = window.setTimeout(updateViewport, delay);
  };

  updateViewport();
  window.addEventListener('resize', updateViewport);
  window.addEventListener('orientationchange', updateViewport);
  window.addEventListener('pageshow', updateViewport);
  window.addEventListener('focus', updateViewport);
  window.visualViewport?.addEventListener('resize', updateViewport);
  window.visualViewport?.addEventListener('scroll', updateViewport);
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState !== 'visible') return;
    updateViewport();
    updateViewportLater();
  });
  document.addEventListener('focusin', updateViewport);
  document.addEventListener('focusout', () => {
    updateViewport();
    updateViewportLater();
  });
}

// 普通局域网 HTTP 不是安全上下文：保持无缓存 Web，不尝试注册 SW。
if (typeof window !== 'undefined' && 'serviceWorker' in navigator && window.isSecureContext) {
  window.addEventListener('load', () => {
    navigator.serviceWorker
      .register('/remote-sw.js', { scope: '/', updateViaCache: 'none' })
      .then((registration) => registration.update())
      .catch(() => {
        // 注册或更新失败不影响在线页面。
      });
  });
}

const rootElement = document.getElementById('root');
if (rootElement) {
  ReactDOM.createRoot(rootElement).render(
    <App />
  );
}
