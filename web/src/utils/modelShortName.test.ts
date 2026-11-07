// 同 ModelPicker.test.tsx：web/ 没有测试运行器，直接断言，失败时 throw。
// 覆盖桌面端 internal/ui/model_capsule_view.go 的短名规则与 kebab ID 美化。
import type { RemoteProvider } from '../types/generated';
import { isAutoPairName, modelShortDisplayName, modelShortName } from './modelShortName';

function assertEqual(actual: string, expected: string, what: string): void {
  if (actual !== expected) {
    throw new Error(`期望${what}为 ${expected}，实际为 ${actual}`);
  }
}

function assertIs(actual: boolean, expected: boolean, what: string): void {
  if (actual !== expected) {
    throw new Error(`期望${what}为 ${expected}，实际为 ${actual}`);
  }
}

// ---- 带空格的显示名：去品牌前缀 ----
assertEqual(modelShortName('Claude Opus 5.5', 'claude'), 'Opus 5.5', 'Claude 品牌短名');
assertEqual(modelShortName('Claude Opus 5.5 · routing group', 'claude'), 'Opus 5.5', '带附加说明的短名');
assertEqual(modelShortName('DeepSeek V4.1 Flash (auto)', 'deepseek'), 'V4.1 Flash', '去 (auto) 与品牌前缀');
assertEqual(modelShortName('GPT-5.5', 'codex'), 'GPT-5.5', 'codex 品牌保留 kebab 全大写');

// ---- kebab ID：美化后再去品牌前缀 ----
assertEqual(modelShortName('claude-opus-5-5 (auto)', 'claude'), 'Opus 5.5', 'kebab ID 去 (auto) 后美化');
assertEqual(modelShortName('deepseek-v4.1-flash', 'deepseek'), 'V4.1 Flash', 'kebab ID 去品牌前缀');
assertEqual(modelShortName('gpt-5.6-luna', 'codex'), 'GPT-5.6 Luna', '缩写词段后接数字段用「-」连接');
assertEqual(
  modelShortName('claude-sonnet-4-5-20250929', 'claude'),
  'Sonnet 4.5',
  'kebab ID 去日期后缀后美化'
);

// ---- modelShortDisplayName：按 provider + 模型 ID 取全名，再按模型判断品牌 ----
const providers = [
  {
    id: 'claude',
    name: 'Claude',
    enabled: true,
    models: [{ id: 'claude-opus-5-5', name: 'Claude Opus 5.5' }]
  }
] as unknown as RemoteProvider[];

assertEqual(
  modelShortDisplayName(providers, 'claude', 'claude-opus-5-5'),
  'Opus 5.5',
  '按 provider 解析后的短名'
);

// ---- isAutoPairName：旧版自动生成的全名/旧快速保存名按未命名处理 ----
const fullLead = 'Claude Opus 5.5';
const fullWorker = 'DeepSeek V4.1 Flash';

assertIs(isAutoPairName(`${fullLead} + ${fullWorker}`, fullLead, fullWorker), true, '全名标题（同旧快速保存名）');
assertIs(isAutoPairName(fullLead, fullLead, ''), true, '旧快速保存名（只有 lead）');
assertIs(isAutoPairName(fullWorker, '', fullWorker), true, '旧快速保存名（只有 worker）');
assertIs(isAutoPairName('模型配对', '', ''), true, '旧快速保存名（两侧都空）');
assertIs(isAutoPairName('任意模型 + 跟随主代理', '', ''), true, '两侧都为空的全名标题');
assertIs(isAutoPairName('我的组合', fullLead, fullWorker), false, '自定义名');
assertIs(isAutoPairName(`  ${fullLead} + ${fullWorker}  `, fullLead, fullWorker), true, '前后带空格的全名标题');
