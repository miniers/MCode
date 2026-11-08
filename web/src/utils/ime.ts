// 兼容输入法组合状态及 Safari 候选词确认时的键盘事件。
import type React from 'react';

export function isImeComposing(event: React.KeyboardEvent | KeyboardEvent): boolean {
  const nativeEvent = 'nativeEvent' in event ? event.nativeEvent : event;
  return nativeEvent.isComposing || nativeEvent.keyCode === 229;
}
