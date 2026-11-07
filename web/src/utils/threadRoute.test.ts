// 本仓库 web/ 没有测试运行器：直接用断言函数，失败即 throw，
// 与 ModelPicker.test.tsx 保持一致（不引入测试框架与 node 内置模块）。
import { parseThreadRoute } from './threadRoute';

function assertEqual(actual: unknown, expected: unknown, label: string): void {
  const left = JSON.stringify(actual);
  const right = JSON.stringify(expected);
  if (left !== right) {
    throw new Error(`${label}: expected ${right}, got ${left}`);
  }
}

// 表驱动：无参数、只有 m、m 与 r 都有、编码字符、非会话路由与非法编码。
const cases: Array<{ name: string; hash: string; expected: ReturnType<typeof parseThreadRoute> }> = [
  { name: '无查询参数', hash: '#/t/thread-1', expected: { threadID: 'thread-1' } },
  { name: '只有 m', hash: '#/t/thread-1?m=msg-1', expected: { threadID: 'thread-1', messageID: 'msg-1' } },
  {
    name: 'm 与 r 都有',
    hash: '#/t/thread-1?m=msg-1&r=req-2',
    expected: { threadID: 'thread-1', messageID: 'msg-1', requestID: 'req-2' }
  },
  {
    name: '只有 r',
    hash: '#/t/thread-1?r=req-2',
    expected: { threadID: 'thread-1', requestID: 'req-2' }
  },
  {
    name: '编码字符',
    hash: '#/t/th%2F1?m=a%20b%26c&r=%E5%AE%A1%E6%89%B9',
    expected: { threadID: 'th/1', messageID: 'a b&c', requestID: '审批' }
  },
  { name: '无 hash', hash: '', expected: null },
  { name: '首页', hash: '#/', expected: null },
  { name: '子页面查询串不当作消息定位', hash: '#/t/thread-1/agents?m=msg-1', expected: null },
  { name: 'threadID 非法编码', hash: '#/t/%E0%A4%A', expected: null }
];

for (const item of cases) {
  assertEqual(parseThreadRoute(item.hash), item.expected, item.name);
}
