// 视口与软键盘判定：纯函数，方便单测；main.tsx 只负责采集窗口数据并写入 CSS 变量

/** 键盘弹起的判定阈值：可视视口比窗口矮这么多像素才算键盘（不再各处各写一个阈值） */
export const KEYBOARD_THRESHOLD = 120;

/**
 * iOS 视口补偿的最大差值：只补状态栏量级（约 20~60px）。
 * 差得更多说明是多任务分屏之类的情况，不能当成状态栏补偿，否则会溢出。
 */
export const SAFE_AREA_MAX_GAP = 80;

const EDITABLE_TAGS = new Set(['INPUT', 'TEXTAREA', 'SELECT']);

/** 焦点是否落在可编辑元素上：只有此时的可视视口收缩才可能是软键盘 */
export function isEditableElement(element: Element | null): boolean {
  if (!element) return false;
  if (EDITABLE_TAGS.has(element.tagName)) return true;
  return (element as HTMLElement).isContentEditable === true;
}

export interface ViewportInput {
  /** window.innerHeight */
  windowHeight: number;
  /** window.visualViewport?.height；不支持视觉视口时为 null */
  visualViewportHeight: number | null;
  /** 当前焦点是否在 input/textarea/select/contenteditable 上 */
  editableFocused: boolean;
  /** 是否 iOS（iPhone/iPad/iPod，含 iPad 桌面模式） */
  isIOS: boolean;
  /** 是否以主屏幕 PWA 方式运行（display-mode: standalone） */
  standalone: boolean;
  /** screen.width：物理屏幕宽度的 CSS 像素 */
  screenWidth: number;
  /** screen.height：物理屏幕高度的 CSS 像素 */
  screenHeight: number;
  /** window.innerWidth */
  innerWidth: number;
  /** env(safe-area-inset-top) 的像素值：半透明状态栏下大于 0 */
  safeAreaTop: number;
}

export interface ViewportMetrics {
  /** 应写入 --app-height 的高度 */
  height: number;
  keyboardOpen: boolean;
}

/**
 * 只在「可视视口明显变矮」且「焦点确实在可编辑元素上」时才算键盘弹起；
 * 否则一律按键盘收起处理并用 windowHeight。这样切到后台再回来、
 * iOS 不重发 visualViewport resize 时，残留的键盘高度不会当成常态。
 */
export function computeViewport(input: ViewportInput): ViewportMetrics {
  const {
    windowHeight,
    visualViewportHeight,
    editableFocused,
    isIOS,
    standalone,
    screenWidth,
    screenHeight,
    innerWidth,
    safeAreaTop
  } = input;

  const keyboardOpen =
    visualViewportHeight !== null &&
    editableFocused &&
    windowHeight - visualViewportHeight > KEYBOARD_THRESHOLD;

  let height = keyboardOpen && visualViewportHeight !== null ? visualViewportHeight : windowHeight;

  // 仅「半透明状态栏」（顶部安全区 > 0）时 iOS 会少算视口高度：window.innerHeight
  // 不含顶部安全区，底部就空出一截。default 状态栏安全区为 0，视口本来就完整，
  // 若再按物理屏幕补偿会多算一个状态栏高度。
  // 已安装的 PWA 可能缓存旧 meta（black-translucent），这里以实际安全区为准。
  if (isIOS && standalone && !keyboardOpen && safeAreaTop > 0) {
    const portrait = windowHeight >= innerWidth;
    const physicalHeight = portrait
      ? Math.max(screenWidth, screenHeight)
      : Math.min(screenWidth, screenHeight);
    const gap = physicalHeight - height;
    if (gap > 0 && gap <= SAFE_AREA_MAX_GAP) height = physicalHeight;
  }

  return { height, keyboardOpen };
}
