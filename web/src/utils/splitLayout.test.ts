import { threadOpenMode, usesSplitLayout } from './splitLayout';

const routeKinds = ['home', 'settings', 'thread', 'draft', 'board', 'task', 'goal'];
for (const routeKind of routeKinds) {
  const expected = ['home', 'settings', 'thread', 'draft'].includes(routeKind);
  if (usesSplitLayout(true, routeKind) !== expected || usesSplitLayout(false, routeKind)) {
    throw new Error(`双栏布局判断错误：${routeKind}`);
  }
  const mode = routeKind === 'thread' || routeKind === 'draft' ? 'replace' : 'push';
  if (threadOpenMode(true, routeKind) !== mode || threadOpenMode(false, routeKind) !== 'push') {
    throw new Error(`会话导航方式错误：${routeKind}`);
  }
}
