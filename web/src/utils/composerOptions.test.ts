import type { RemoteComposerOptions } from '../types/contract';
import { runningThreadUpdateChanges } from './composerOptions';

const cases: Array<{
  name: string;
  previous: RemoteComposerOptions;
  next: RemoteComposerOptions;
  expected: { model?: string; effort?: string } | null;
}> = [
  { name: '只改模型', previous: { model: 'a', effort: 'low' }, next: { model: 'b', effort: 'low' }, expected: { model: 'b' } },
  { name: '只改强度', previous: { model: 'a', effort: 'low' }, next: { model: 'a', effort: 'high' }, expected: { effort: 'high' } },
  { name: '同时改模型和强度', previous: { model: 'a', effort: 'low' }, next: { model: 'b', effort: 'high' }, expected: { model: 'b', effort: 'high' } },
  { name: '换模型时强度交给宿主', previous: { model: 'a', effort: 'low' }, next: { model: 'b' }, expected: { model: 'b' } },
  { name: '清除强度', previous: { model: 'a', effort: 'low' }, next: { model: 'a' }, expected: { effort: '' } },
  { name: '没有变化', previous: { model: 'a', effort: 'low' }, next: { model: 'a', effort: 'low' }, expected: null }
];

for (const { name, previous, next, expected } of cases) {
  const actual = runningThreadUpdateChanges(previous, next);
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new Error(`${name}：期望 ${JSON.stringify(expected)}，实际 ${JSON.stringify(actual)}`);
  }
}
