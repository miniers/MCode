import type { RemoteProvider } from '../types/generated';
import { providerDisplayName } from './modelLabel';

function assertEqual(actual: string, expected: string, what: string): void {
  if (actual !== expected) {
    throw new Error(`期望${what}为 ${JSON.stringify(expected)}，实际为 ${JSON.stringify(actual)}`);
  }
}

const providers = [
  {
    id: 'custom-432db46a',
    name: '我的网关',
    models: []
  }
] as unknown as RemoteProvider[];

assertEqual(providerDisplayName(providers, 'custom-432db46a'), '我的网关', '命中自定义 provider 显示名');
assertEqual(providerDisplayName(providers, 'unknown-provider'), 'unknown-provider', '未命中时返回原 id');
assertEqual(providerDisplayName(providers, ''), '', '空字符串 id 返回空串');
assertEqual(providerDisplayName(providers, null), '', 'null id 返回空串');
assertEqual(providerDisplayName(providers, undefined), '', 'undefined id 返回空串');
