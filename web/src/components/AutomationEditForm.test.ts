import {
  createDebouncedPreview,
  shouldShowAutomationPreview
} from './AutomationEditForm';
import type { RemoteAutomationExtPreviewResult } from '../context/remoteAutomationApi';
// @ts-ignore Vitest is invoked ephemerally in this repository, not added as a web dependency.
import { describe, expect, it } from 'vitest';

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

function wait(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

describe('automation schedule preview', () => {
  it('debounces input and ignores an older response', async () => {
  const stale = deferred<string>();
  const results: Array<string | null> = [];
  const requests: string[] = [];
  const runner = createDebouncedPreview<string, string>(
    (value) => {
      requests.push(value);
      return value === 'old' ? stale.promise : Promise.resolve(value);
    },
    (result) => results.push(result),
    20
  );

  runner.update('superseded');
  await wait(10);
  runner.update('old');
  await wait(25);
  expect(requests).toEqual(['old']);
  runner.update('new');
  await wait(25);
  stale.resolve('stale result');
  await Promise.resolve();
  expect(results[results.length - 1]).toBe('new');
  expect(results).not.toContain('stale result');
  runner.cancel();
  });

const preview: RemoteAutomationExtPreviewResult = { times: ['2030-01-01T00:00:00Z'], exhausted: false };
  it('hides previews when the method is unavailable or no times exist', () => {
    expect(shouldShowAutomationPreview(false, preview)).toBe(false);
    expect(shouldShowAutomationPreview(true, preview)).toBe(true);
    expect(shouldShowAutomationPreview(true, { times: [], exhausted: true })).toBe(true);
    expect(shouldShowAutomationPreview(true, { times: [], exhausted: false })).toBe(false);
  });
});
