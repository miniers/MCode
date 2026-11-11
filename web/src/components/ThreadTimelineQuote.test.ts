// @ts-ignore Vitest is provided by the test runner, not declared as a project dependency.
import { describe, expect, it, vi } from 'vitest';
import { quoteMessageMenuItem } from './ThreadTimeline';

describe('assistant quote menu item', () => {
  it('does not create a quote action for a non-final assistant message', () => {
    expect(quoteMessageMenuItem(true, false, 'message-1', vi.fn())).toBeNull();
  });

  it('creates a quote action that passes the message id', () => {
    const onQuote = vi.fn();
    const item = quoteMessageMenuItem(true, true, 'message-2', onQuote);

    expect(item).toMatchObject({ key: 'quote', label: '引用' });
    item?.onSelect();
    expect(onQuote).toHaveBeenCalledWith('message-2');
  });
});
