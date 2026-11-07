// remote-sw.js 推送纯函数测试：用 node:vm 在假 self 上加载 Service Worker，
// 直接调用顶层函数。不依赖测试框架，断言失败即抛错、退出码非 0。
// 运行：node web/scripts/remote-sw.test.mjs
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import vm from 'node:vm';

const here = dirname(fileURLToPath(import.meta.url));
const source = readFileSync(resolve(here, '../public/remote-sw.js'), 'utf8');

const sandbox = { self: { addEventListener() {} }, console, URL, setTimeout, clearTimeout };
vm.runInNewContext(source, sandbox);

const { parsePushPayload, buildPushNotificationOptions, pushNotificationTag, resolveAnswerAction, buildAnswerResultNotification } = sandbox;

const pushEvent = (payload) => ({ json: () => payload });
// vm 里创建的对象原型来自另一个 realm，deepEqual 前先转成普通 JSON 值。
const plain = (value) => (value === undefined ? value : JSON.parse(JSON.stringify(value)));

const answers = [
  { action: 'ans0', label: '允许', value: '允许' },
  { action: 'ans1', label: '拒绝', value: '拒绝' }
];

const tests = [];
const test = (name, run) => tests.push({ name, run });

test('旧载荷：无 actions、无 requireInteraction，tag 回退 threadID', () => {
  const payload = parsePushPayload(pushEvent({ title: '需要你的审批', body: '允许执行？', threadID: 'thread-1', silent: true }));
  assert.equal(payload.answerable, false);
  const options = buildPushNotificationOptions(payload);
  assert.equal(options.tag, 'thread-1');
  assert.equal(options.silent, true);
  assert.equal(options.renotify, false);
  assert.equal('actions' in options, false);
  assert.equal('requireInteraction' in options, false);
  assert.deepEqual(plain(options.data), { threadID: 'thread-1', messageID: null, requestID: null });
});

test('可答载荷：两个答案加打开，tag 用 requestID，data 保留原始 answers', () => {
  const payload = parsePushPayload(
    pushEvent({
      title: '需要你的审批',
      body: '允许执行？',
      threadID: 'thread-1',
      requestID: 'req-1',
      answerable: true,
      questionID: 'q1',
      answers
    })
  );
  assert.equal(payload.answerable, true);
  assert.equal(pushNotificationTag(payload), 'req-1');
  const options = buildPushNotificationOptions(payload);
  assert.deepEqual(plain(options.actions), [
    { action: 'answer:ans0', title: '允许' },
    { action: 'answer:ans1', title: '拒绝' },
    { action: 'open', title: '打开' }
  ]);
  assert.equal(options.requireInteraction, true);
  assert.equal(options.data.answerable, true);
  assert.equal(options.data.questionID, 'q1');
  assert.deepEqual(plain(options.data.answers), answers);
});

test('畸形 answers：整体降级为不可答', () => {
  const cases = [
    { label: '三项', answers: [...answers, { action: 'ans2', label: '其他', value: '其他' }] },
    { label: 'action 不合规', answers: [{ action: 'answer:x', label: '允许', value: '允许' }] },
    { label: 'label 非字符串', answers: [{ action: 'ans0', label: 1, value: '允许' }] },
    { label: 'label 为空', answers: [{ action: 'ans0', label: '', value: '允许' }] },
    { label: 'value 缺失', answers: [{ action: 'ans0', label: '允许', value: '' }] },
    { label: 'answers 非数组', answers: { action: 'ans0', label: '允许', value: '允许' } }
  ];
  for (const item of cases) {
    const payload = parsePushPayload(pushEvent({ threadID: 'thread-1', answerable: true, answers: item.answers }));
    assert.equal(payload.answerable, false, item.label);
    assert.deepEqual(plain(payload.answers), [], item.label);
    assert.equal('actions' in buildPushNotificationOptions(payload), false, item.label);
  }
});

test('resolveAnswerAction：命中 ans1，其余一律 null', () => {
  assert.deepEqual(plain(resolveAnswerAction('answer:ans1', answers)), { action: 'ans1', label: '拒绝', value: '拒绝' });
  assert.equal(resolveAnswerAction('open', answers), null);
  assert.equal(resolveAnswerAction('', answers), null);
  assert.equal(resolveAnswerAction('answer:ans9', answers), null);
  assert.equal(resolveAnswerAction('answer:ans0', undefined), null);
  assert.equal(resolveAnswerAction('answer:ans0', [{ action: 'ans0', label: '允许' }]), null);
});

test('tag 回退链：requestID → threadID → mcode', () => {
  assert.equal(pushNotificationTag({ requestID: 'req-1', threadID: 'thread-1' }), 'req-1');
  assert.equal(pushNotificationTag({ threadID: 'thread-1' }), 'thread-1');
  assert.equal(pushNotificationTag({}), 'mcode');
  assert.equal(pushNotificationTag(null), 'mcode');
});

test('三种作答结果的通知文案', () => {
  const answer = { action: 'ans0', label: '允许', value: '允许' };
  const data = { threadID: 'thread-1', messageID: 'msg-1', requestID: 'req-1' };

  const answered = buildAnswerResultNotification(200, answer, data);
  assert.equal(answered.title, '已回复');
  assert.equal(answered.options.body, '允许');
  assert.equal(answered.options.tag, 'answered:req-1');
  assert.equal(answered.closeAfterMs, 4000);

  const handled = buildAnswerResultNotification(409, answer, data);
  assert.equal(handled.title, '已在别处处理');
  assert.equal(handled.options.tag, 'answered:req-1');
  assert.equal(handled.closeAfterMs, 4000);

  const failed = buildAnswerResultNotification(0, answer, data);
  assert.equal(failed.title, '回复失败，点开处理');
  assert.equal(failed.options.tag, 'req-1');
  assert.equal(failed.closeAfterMs, 0);
  assert.equal('actions' in failed.options, false);
  assert.deepEqual(plain(failed.options.data), data);
});

test('静音载荷 renotify 为 false，非静音为 true', () => {
  assert.equal(buildPushNotificationOptions(parsePushPayload(pushEvent({ threadID: 't', silent: true }))).renotify, false);
  assert.equal(buildPushNotificationOptions(parsePushPayload(pushEvent({ threadID: 't' }))).renotify, true);
});

for (const { name, run } of tests) {
  run();
  console.log(`ok ${name}`);
}
console.log(`${tests.length} 个用例全部通过`);
