// 设置索引测试：验证精简重组后的分组、页面、设置项登记表与搜索匹配。
// @ts-expect-error web/ 未安装 @types/node，测试运行时由 node 提供 node:assert
import { strict as assert } from 'node:assert';
import {
  SETTINGS_GROUPS,
  SETTINGS_ITEMS,
  SETTINGS_PAGES,
  matchSettingsItems
} from './settingsIndex';

assert.deepEqual(
  SETTINGS_GROUPS.map((group) => group.id),
  ['ai', 'app']
);

const expectedPageIDs = [
  'conversation',
  'swarm',
  'steward',
  'nowledge',
  'skills',
  'appearance',
  'device',
  'about'
];
assert.deepEqual(
  SETTINGS_PAGES.map((page) => page.id),
  expectedPageIDs
);

const groupIDs = new Set(SETTINGS_GROUPS.map((group) => group.id));
for (const page of SETTINGS_PAGES) {
  assert.ok(groupIDs.has(page.group), `页面 ${page.id} 的 group ${page.group} 不在 SETTINGS_GROUPS 中`);
}

const pageIDs = new Set(SETTINGS_PAGES.map((page) => page.id));
for (const item of SETTINGS_ITEMS) {
  assert.ok(pageIDs.has(item.page), `设置项 ${item.id} 的 page ${item.page} 不在 SETTINGS_PAGES 中`);
}

const removedKeys = [
  'startup.tray_quota_hidden',
  'archive.confirm_before_deleting',
  'orchestration.workspace_mode',
  'orchestration.max_rounds',
  'orchestration.retry_failed',
  'orchestration.auto_clear_finished',
  'orchestration.always_bees',
  'orchestration.auto_forward_question_seconds',
  'steward.cli_fallback',
  'steward.name',
  'steward.avatar'
];

const presentKeys = new Set(
  SETTINGS_ITEMS.map((item) => item.settingsKey).filter((key): key is string => Boolean(key))
);

for (const key of removedKeys) {
  assert.ok(!presentKeys.has(key), `不应再包含已删除的 settingsKey：${key}`);
}
for (const key of presentKeys) {
  assert.ok(!key.startsWith('notifications.') || key === 'notifications.webhook_url', `意外的通知设置：${key}`);
}
assert.ok(presentKeys.has('notifications.webhook_url'), '设备设置应包含 Webhook');

const modeItem = SETTINGS_ITEMS.find((item) => item.id === 'row-mode');
assert.equal(modeItem?.page, 'conversation');

const dormantItem = SETTINGS_ITEMS.find((item) => item.id === 'row-auto-dormant-days');
assert.equal(dormantItem?.page, 'conversation');

const permissionHits = matchSettingsItems('权限');
assert.ok(
  permissionHits.some((hit) => hit.item.id === 'row-mode'),
  'matchSettingsItems("权限") 应命中 row-mode'
);

for (const item of SETTINGS_ITEMS) {
  assert.ok(!('advanced' in item), `设置项 ${item.id} 不应再带 advanced 属性`);
  assert.ok(!('desc' in item), `设置项 ${item.id} 不应再带 desc 属性`);
}
