import {
  decisionReplyText,
  latestDecisionAnswers,
  parseStewardDecision,
  parseStewardResult
} from './StewardMessages';

function assertEqual<T>(actual: T, expected: T, label: string): void {
  if (actual !== expected) throw new Error(`${label}: expected ${String(expected)}, got ${String(actual)}`);
}

const decisionText = JSON.stringify({
  id: 'D-0001',
  task: '任务标题',
  question: '选哪个？',
  options: [{ key: 'A', text: '方案 A' }],
  recommended: 'A'
});
const decision = parseStewardDecision(decisionText);
if (!decision) throw new Error('valid steward decision should parse');
assertEqual(decision.id, 'D-0001', 'decision id');
assertEqual(decision.options[0]?.text, '方案 A', 'decision option');
assertEqual(parseStewardDecision('{bad json'), null, 'invalid decision falls back');
assertEqual(parseStewardDecision(JSON.stringify({ id: 'D-0001' })), null, 'incomplete decision falls back');

const result = parseStewardResult(JSON.stringify({
  task: '任务标题',
  thread: 'thread-1',
  title: '标题',
  summary: '摘要',
  status: 'done'
}));
if (!result) throw new Error('valid steward result should parse');
assertEqual(result.status, 'done', 'result status');
assertEqual(parseStewardResult(JSON.stringify({ title: '标题', summary: '摘要', status: 'unknown' })), null, 'invalid result status falls back');

const answers = latestDecisionAnswers([
  { kind: 'steward_decision_answer', role: 'system', text: JSON.stringify({ id: 'D-0001', answer: 'A' }) },
  { kind: 'steward_decision_answer', role: 'user', text: JSON.stringify({ id: 'D-0001', answer: 'B' }) },
  { kind: 'steward_decision_answer', role: 'assistant', text: JSON.stringify({ id: 'D-0001', answer: 'C' }) }
]);
assertEqual(answers['D-0001'], 'B', 'latest answer only accepts system/user');
assertEqual(decisionReplyText('D-0001', 'A'), 'D-0001 A', 'decision option reply');
assertEqual(decisionReplyText('D-0001', '忽略'), 'D-0001 忽略', 'decision ignore reply');
