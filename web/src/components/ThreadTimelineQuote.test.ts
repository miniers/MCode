// web 测试不使用测试框架（见 scripts/run-tests.mjs）：断言失败直接抛 Error。
import { quoteMessageMenuItem } from './ThreadTimeline';

if (quoteMessageMenuItem(true, false, 'message-1', () => {}) !== null) {
  throw new Error('非最终答复不应生成引用项');
}

const quoted: string[] = [];
const item = quoteMessageMenuItem(true, true, 'message-2', (messageID) => quoted.push(messageID));
if (item?.key !== 'quote' || item.label !== '引用') {
  throw new Error(`引用项错误：${JSON.stringify(item)}`);
}
item.onSelect();
if (quoted.length !== 1 || quoted[0] !== 'message-2') {
  throw new Error(`引用回调参数错误：${JSON.stringify(quoted)}`);
}
