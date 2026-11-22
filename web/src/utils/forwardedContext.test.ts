import { parseForwardedTurns, splitForwardedTail } from './forwardedContext';
import { isForwardSlashCommand } from './slashCommands';

function assertEqual(actual: unknown, expected: unknown, label: string): void {
  const left = JSON.stringify(actual);
  const right = JSON.stringify(expected);
  if (left !== right) {
    throw new Error(`${label}: expected ${right}, got ${left}`);
  }
}

// 1. 多轮解析 + 省略标记 + trimmed 后缀替换
const raw = [
  'User:',
  '先帮我排查一下超时问题',
  '',
  'Assistant:',
  '好的，先看日志',
  '[trimmed]',
  '',
  '[… part of the copied conversation was dropped here to stay within the size budget …]',
  '',
  'User:',
  '最后方案是什么？',
  '',
  'Assistant:',
  '改用短超时并加退避。'
].join('\n');

const turns = parseForwardedTurns(raw);
assertEqual(
  turns,
  [
    { role: 'user', text: '先帮我排查一下超时问题' },
    { role: 'assistant', text: '好的，先看日志\n…' },
    { role: 'omitted', text: '[… part of the copied conversation was dropped here to stay within the size budget …]' },
    { role: 'user', text: '最后方案是什么？' },
    { role: 'assistant', text: '改用短超时并加退避。' }
  ],
  '多轮与省略解析'
);

// 2. 切分：默认展开最后一条 user 及之后，更早轮次折叠计数不含 omitted
const split = splitForwardedTail(turns);
assertEqual(split.earlierCount, 2, 'earlierCount 忽略 omitted');
assertEqual(split.earlier.length, 3, 'earlier 包含更早消息与 omitted');
assertEqual(split.tail, [
  { role: 'user', text: '最后方案是什么？' },
  { role: 'assistant', text: '改用短超时并加退避。' }
], 'tail 从最后一条 user 开始');

// 3. 无角色标记时作为单条助手内容回退
assertEqual(
  parseForwardedTurns('plain summary'),
  [{ role: 'assistant', text: 'plain summary' }],
  '纯文本回退'
);

// 4. 空内容返回空数组
assertEqual(parseForwardedTurns('   \n '), [], '空白返回空');

// 5. /forward 本地命令匹配
assertEqual(isForwardSlashCommand('/forward'), true, '/forward');
assertEqual(isForwardSlashCommand('  /handoff  '), true, '/handoff');
assertEqual(isForwardSlashCommand('/FW'), true, '/fw 大小写不敏感');
assertEqual(isForwardSlashCommand('/forward 保留重点'), false, '带参数不误判');
assertEqual(isForwardSlashCommand('forward'), false, '无斜杠不误判');
