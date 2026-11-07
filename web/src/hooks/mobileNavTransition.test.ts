// 同 mobileMotion.test.ts：web/ 没有测试运行器，用普通断言函数，失败直接 throw；也不引用 node 内置类型。
// 覆盖两件事：过场要不要跑（shouldSkipNavTransition），以及过场收尾：
// 删 data-mobile-direction 之前要把已挂载的 .settings-sheet 入场动画冻成 none，
// 否则 index.css 里被它抑制的 slide-up 会从头重播，设置面板会透明闪一下。
import { resolveNavDirection, shouldSkipNavTransition, startMobilePageTransition } from './mobileNavTransition';
import type { NavRouteLike } from './mobileNavTransition';

function assertEqual(label: string, actual: unknown, expected: unknown): void {
  if (actual !== expected) {
    throw new Error(`${label}: 期望 ${String(expected)}，实际 ${String(actual)}`);
  }
}

interface FakeSheet {
  style: { animation?: string };
}

const draftRoute = { kind: 'draft', projectID: 'p' };
assertEqual(
  'draft → thread 替换历史时使用替换过场',
  resolveNavDirection(draftRoute, { kind: 'thread', threadID: 't1' }, true),
  'replace'
);

// 设置是覆盖层、菜单子页有自己的栈动画，这两类切换不该叠整页过场。
const skipCases: Array<{
  label: string;
  prev: NavRouteLike;
  next: NavRouteLike;
  expectSkip: boolean;
}> = [
  { label: 'home → settings', prev: { kind: 'home' }, next: { kind: 'settings' }, expectSkip: true },
  {
    label: 'settings → home',
    prev: { kind: 'settings' },
    next: { kind: 'home' },
    expectSkip: true
  },
  {
    label: 'settings → settings',
    prev: { kind: 'settings', subview: 'general' },
    next: { kind: 'settings', subview: 'remote' },
    expectSkip: true
  },
  {
    label: '同一会话 thread → thread scripts',
    prev: { kind: 'thread', threadID: 't1' },
    next: { kind: 'thread', threadID: 't1', subview: 'scripts' },
    expectSkip: true
  },
  {
    label: '同一会话 thread scripts → thread',
    prev: { kind: 'thread', threadID: 't1', subview: 'scripts' },
    next: { kind: 'thread', threadID: 't1' },
    expectSkip: true
  },
  {
    label: '不同会话 thread → thread',
    prev: { kind: 'thread', threadID: 't1' },
    next: { kind: 'thread', threadID: 't2' },
    expectSkip: false
  },
  {
    label: 'thread → home',
    prev: { kind: 'thread', threadID: 't1' },
    next: { kind: 'home' },
    expectSkip: true
  },
  {
    label: 'thread review → home',
    prev: { kind: 'thread', threadID: 't1', subview: 'review' },
    next: { kind: 'home' },
    expectSkip: true
  },
  {
    label: 'home → thread',
    prev: { kind: 'home' },
    next: { kind: 'thread', threadID: 't1' },
    expectSkip: false
  },
  {
    label: 'home → board',
    prev: { kind: 'home' },
    next: { kind: 'board' },
    expectSkip: false
  }
];

for (const testCase of skipCases) {
  assertEqual(
    `跳过过场 ${testCase.label}`,
    shouldSkipNavTransition(testCase.prev, testCase.next),
    testCase.expectSkip
  );
}

const fakeDataset: { mobileDirection?: string } = {};
const fakeSheet: FakeSheet = { style: {} };
const fakeSheets: FakeSheet[] = [fakeSheet];
const queriedSelectors: string[] = [];

const fakeDocument = {
  documentElement: { dataset: fakeDataset },
  querySelectorAll(selector: string) {
    queriedSelectors.push(selector);
    return selector === '.settings-sheet' ? fakeSheets : [];
  },
  startViewTransition(update: () => void) {
    update();
    return { finished: Promise.resolve() };
  }
};

const fakeWindow = {
  matchMedia() {
    return { matches: false };
  }
};

type DomGlobals = { document?: unknown; window?: unknown };
const globals = globalThis as unknown as DomGlobals;
const savedDocument = globals.document;
const savedWindow = globals.window;

let updateCalls = 0;
let directionDuringUpdate: string | undefined;

try {
  globals.document = fakeDocument;
  globals.window = fakeWindow;
  const transition = startMobilePageTransition('push', () => {
    updateCalls += 1;
    directionDuringUpdate = fakeDataset.mobileDirection;
  });
  if (!transition) throw new Error('支持 View Transition 时不应退化为直接更新');
  await transition.finished;
  // finished 的 finally 回调排在下一个微任务里，等它跑完再断言。
  await Promise.resolve();
} finally {
  globals.document = savedDocument;
  globals.window = savedWindow;
}

assertEqual('更新函数调用次数', updateCalls, 1);
assertEqual('过场期间的方向标记', directionDuringUpdate, 'push');
assertEqual('查询过的选择器', queriedSelectors[0], '.settings-sheet');
assertEqual('sheet 的 inline 入场动画', fakeSheet.style.animation, 'none');
assertEqual('方向标记是否已删除', 'mobileDirection' in fakeDataset, false);
