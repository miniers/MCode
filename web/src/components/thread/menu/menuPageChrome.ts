// 每页头部上下文：栈容器注入 leading/onLeading/onClose；Git/Scripts 内容体通过
// setTrailing 把自己的刷新按钮上报到共享标题栏，避免把数据状态提到栈容器。

import React from 'react';

export type MenuPageLeading = 'none' | 'back' | 'close';

export interface MenuPageChrome {
  leading: MenuPageLeading;
  onLeading: () => void;
  onClose?: () => void;
  setTrailing?: (node: React.ReactNode) => void;
}

const noop = () => {};

export const MenuPageChromeContext = React.createContext<MenuPageChrome>({
  leading: 'none',
  onLeading: noop
});

export const useMenuPageChrome = (): MenuPageChrome => React.useContext(MenuPageChromeContext);
