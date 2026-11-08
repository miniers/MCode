// 决定宽屏双栏的适用路由和会话导航方式。
export function usesSplitLayout(isWide: boolean, routeKind: string): boolean {
  return isWide && ['home', 'settings', 'thread', 'draft'].includes(routeKind);
}

export function threadOpenMode(isWide: boolean, currentRouteKind: string): 'push' | 'replace' {
  return isWide && (currentRouteKind === 'thread' || currentRouteKind === 'draft') ? 'replace' : 'push';
}
