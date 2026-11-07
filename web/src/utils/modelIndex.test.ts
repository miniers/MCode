// 同 ModelPicker.test.tsx：web/ 目前没有测试运行器，这里不引入任何框架与 node 内置模块，
// 断言失败直接 throw，顶层顺序执行。后续接入运行器后本文件即可直接跑。
import type { RemoteProviderListWithCatalog } from '../context/remoteTypes';
import type { RemoteProvider, RemoteProviderModel, RemoteSwarmPair } from '../types/generated';
import {
  buildPickerSections,
  isMissingSelection,
  modelMatchesQuery,
  normalizeModelID,
  pairUnavailableReason,
  type PickerRow
} from './modelIndex';

type TestModel = RemoteProviderModel & { aliasOf?: string };

function model(overrides: Partial<TestModel>): TestModel {
  return {
    id: 'model',
    name: 'Model',
    detail: null,
    efforts: ['high'],
    defaultEffort: 'high',
    isDefault: false,
    supportsFast: false,
    preferredEffort: null,
    ...overrides
  };
}

function provider(overrides: Omit<Partial<RemoteProvider>, 'models'> & { models?: TestModel[] }): RemoteProvider {
  return {
    id: 'claude',
    name: 'Claude',
    auth: 'signedIn',
    models: [],
    supportsPlanMode: false,
    supportsImages: false,
    supportsFast: false,
    available: true,
    unavailableReason: null,
    ...overrides
  };
}

function providers(
  list: RemoteProvider[],
  catalog?: Array<{ provider: string; modelID: string }>
): RemoteProviderListWithCatalog {
  const result = [...list] as RemoteProviderListWithCatalog;
  if (catalog) result.catalog = catalog;
  return result;
}

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function assertEqual(actual: string | null, expected: string | null, label: string): void {
  if (actual !== expected) throw new Error(`${label} 期望 ${String(expected)}，实际 ${String(actual)}`);
}

function keysOf(rows: PickerRow[]): string[] {
  return rows.map((row) => row.key);
}

function assertKeys(rows: PickerRow[], expected: string[], label: string): void {
  const actual = keysOf(rows);
  if (actual.join('|') !== expected.join('|')) {
    throw new Error(`${label} 期望 [${expected.join(', ')}]，实际 [${actual.join(', ')}]`);
  }
}

function findRow(rows: PickerRow[], key: string): PickerRow {
  const found = rows.find((row) => row.key === key);
  if (!found) throw new Error(`找不到行 ${key}`);
  return found;
}

const visibleAll = () => true;

const opusAlias = model({ id: 'opus', name: 'Opus 5.5', aliasOf: 'auto/claude-opus-5-5[1m]' });
const opusTarget = model({ id: 'auto/claude-opus-5-5[1m]', name: 'Opus 5.5（1M）' });
const opusAuto = model({ id: 'auto/claude-opus-5-5', name: 'Opus 5.5', secondary: true });
const opusGroup = model({ id: 'group/claude-opus-5-5', name: 'Opus 5.5' });
const claude = provider({ id: 'claude', name: 'Claude', models: [opusAlias, opusTarget, opusAuto, opusGroup] });

// (b)(c)(d) 别名折叠、同名消歧、secondary 分流。
const base = buildPickerSections({ providers: providers([claude]), query: '', isProviderVisible: visibleAll });
assert(!base.searching, '空搜索词时 searching 应为 false');
assert(base.current === null, '没有 current 入参时 current 应为 null');
assertKeys(base.favorites, [], '没有 catalog 时 favorites 应为空');
if (base.agents.length !== 1) throw new Error(`没有 catalog 时应只有 claude 一组，实际 ${base.agents.length} 组`);
const claudeSection = base.agents[0];
assertKeys(
  claudeSection.primary,
  ['claude/auto/claude-opus-5-5[1m]', 'claude/group/claude-opus-5-5'],
  '别名折叠后的 primary'
);
assertKeys(claudeSection.secondary, ['claude/auto/claude-opus-5-5'], 'secondary 模型分流');
assertEqual(claudeSection.reason, null, '有模型的组 reason');
assert(
  ![...claudeSection.primary, ...claudeSection.secondary].some((row) => row.key === 'claude/opus'),
  '未被选中且不在 catalog 的别名 opus 应被折叠'
);
assertEqual(
  findRow(claudeSection.primary, 'claude/group/claude-opus-5-5').subtitle,
  'group/claude-opus-5-5',
  '同名行用模型 ID 区分'
);
assertEqual(
  findRow(claudeSection.secondary, 'claude/auto/claude-opus-5-5').subtitle,
  'auto/claude-opus-5-5',
  'primary 与 secondary 一起算重名'
);
assertEqual(
  findRow(claudeSection.primary, 'claude/auto/claude-opus-5-5[1m]').subtitle,
  null,
  '不重名的行没有 subtitle'
);
assertEqual(findRow(claudeSection.primary, 'claude/auto/claude-opus-5-5[1m]').reason, null, '可用行的 reason');
assert(!findRow(claudeSection.primary, 'claude/auto/claude-opus-5-5[1m]').disabled, '可用行不应禁用');

// (b) 别名就是 current 时保留，并且 current 行单独给出。
const aliasCurrent = buildPickerSections({
  providers: providers([claude]),
  current: { provider: 'claude', model: 'opus' },
  query: '',
  isProviderVisible: visibleAll
});
assert(aliasCurrent.current !== null, 'current 是别名时仍要有 current 行');
assertEqual(aliasCurrent.current?.key ?? null, 'claude/opus', 'current 行的 key');
assertEqual(aliasCurrent.current?.subtitle ?? null, 'Claude', 'current 行的 subtitle 用 provider 名');
assertKeys(
  aliasCurrent.agents[0].primary,
  ['claude/opus', 'claude/auto/claude-opus-5-5[1m]', 'claude/group/claude-opus-5-5'],
  'current 别名保留在 primary'
);

// (b)(f) 别名出现在 catalog 里时保留，并进入 favorites。
const aliasFavorite = buildPickerSections({
  providers: providers([claude], [{ provider: 'claude', modelID: 'opus' }]),
  query: '',
  isProviderVisible: visibleAll
});
assertKeys(aliasFavorite.favorites, ['claude/opus'], 'catalog 里的别名进入 favorites');
assertEqual(findRow(aliasFavorite.favorites, 'claude/opus').subtitle, 'Claude', 'favorites 行的 subtitle');
assertKeys(
  aliasFavorite.agents[0].primary,
  ['claude/opus', 'claude/auto/claude-opus-5-5[1m]', 'claude/group/claude-opus-5-5'],
  'catalog 里的别名保留在 primary'
);

// (d) current 是 secondary 模型时留在 primary，secondary 清空。
const secondaryCurrent = buildPickerSections({
  providers: providers([claude]),
  current: { provider: 'claude', model: 'auto/claude-opus-5-5' },
  query: '',
  isProviderVisible: visibleAll
});
assertKeys(secondaryCurrent.agents[0].secondary, [], 'current 是 secondary 模型时不留 secondary');
assert(
  secondaryCurrent.agents[0].primary.some((row) => row.key === 'claude/auto/claude-opus-5-5'),
  'current 的 secondary 模型应留在 primary'
);
assertEqual(secondaryCurrent.current?.key ?? null, 'claude/auto/claude-opus-5-5', 'current 行指向 secondary 模型');

// (e) 没有模型的 provider：有原因保留空分组，没有原因整组去掉。
const codexEmptyWithReason = provider({
  id: 'codex',
  name: 'Codex',
  models: [],
  available: false,
  unavailableReason: '未安装 Codex CLI'
});
const geminiEmpty = provider({ id: 'gemini', name: 'Gemini', models: [] });
const reasonSections = buildPickerSections({
  providers: providers([claude, codexEmptyWithReason, geminiEmpty]),
  query: '',
  isProviderVisible: visibleAll
});
if (reasonSections.agents.length !== 2) {
  throw new Error(`只有 claude 与 codex 两个分组，实际 ${reasonSections.agents.length} 组`);
}
const codexReasonSection = reasonSections.agents[1];
assertEqual(codexReasonSection.reason, '未安装 Codex CLI', '无模型的 provider 带上原因');
assertKeys(codexReasonSection.primary, [], '只有原因的分组没有 primary');
assertKeys(codexReasonSection.secondary, [], '只有原因的分组没有 secondary');
assert(
  !reasonSections.agents.some((section) => section.provider.id === 'gemini'),
  '无模型且无原因的 provider 应整组去掉'
);

// (f) favorites 按 catalog 顺序、按 key 去重，并跳过找不到的 provider 与模型。
const codex = provider({
  id: 'codex',
  name: 'Codex',
  models: [
    model({ id: 'gpt-5', name: 'GPT-5' }),
    model({ id: 'gpt-5-mini', name: 'GPT-5 Mini', secondary: true })
  ]
});
const favoriteList = providers(
  [claude, codex],
  [
    { provider: 'claude', modelID: 'group/claude-opus-5-5' },
    { provider: 'codex', modelID: 'gpt-5' },
    { provider: 'claude', modelID: 'group/claude-opus-5-5' },
    { provider: 'claude', modelID: 'missing-model' },
    { provider: 'ghost', modelID: 'ghost-model' },
    { provider: 'claude', modelID: 'auto/claude-opus-5-5' }
  ]
);
const favoriteResult = buildPickerSections({ providers: favoriteList, query: '', isProviderVisible: visibleAll });
assertKeys(
  favoriteResult.favorites,
  ['claude/group/claude-opus-5-5', 'codex/gpt-5', 'claude/auto/claude-opus-5-5'],
  'favorites 顺序与去重'
);
assertEqual(findRow(favoriteResult.favorites, 'codex/gpt-5').subtitle, 'Codex', 'favorites 不重名时只用 provider 名');
assertEqual(
  findRow(favoriteResult.favorites, 'claude/group/claude-opus-5-5').subtitle,
  'Claude · group/claude-opus-5-5',
  'favorites 同名时带模型 ID'
);

// (g) current 已经在 favorites 里时不再重复出现。
const currentInFavorites = buildPickerSections({
  providers: favoriteList,
  current: { provider: 'claude', model: 'group/claude-opus-5-5' },
  query: '',
  isProviderVisible: visibleAll
});
assert(currentInFavorites.current === null, 'current 已在 favorites 里时 current 应为 null');

// (h) 搜索：按 provider 名也能命中，secondary 并入 primary，空组（含只有原因的组）去掉。
const searchProviders = providers([
  claude,
  codex,
  provider({ id: 'codex-empty', name: 'Codex', models: [], unavailableReason: '未安装 Codex CLI' })
]);
const search = buildPickerSections({
  providers: searchProviders,
  current: { provider: 'claude', model: 'opus' },
  query: '  codex  ',
  isProviderVisible: visibleAll
});
assert(search.searching, '搜索词非空时 searching 应为 true');
assert(search.current === null, '搜索时 current 应为 null');
assertKeys(search.favorites, [], '搜索时 favorites 应为空');
if (search.agents.length !== 1) throw new Error(`搜索 codex 只应命中 codex 一组，实际 ${search.agents.length} 组`);
assertKeys(search.agents[0].primary, ['codex/gpt-5', 'codex/gpt-5-mini'], '搜索时 secondary 并入 primary');
assertKeys(search.agents[0].secondary, [], '搜索时 secondary 应为空');

// (h) 搜索时折叠的别名仍然隐藏。
const searchAlias = buildPickerSections({ providers: providers([claude]), query: 'opus', isProviderVisible: visibleAll });
if (searchAlias.agents.length !== 1) throw new Error(`搜索 opus 应只命中 claude 一组，实际 ${searchAlias.agents.length} 组`);
assert(
  !searchAlias.agents[0].primary.some((row) => row.key === 'claude/opus'),
  '搜索时折叠的别名仍然隐藏'
);
assertKeys(
  searchAlias.agents[0].primary,
  ['claude/auto/claude-opus-5-5[1m]', 'claude/auto/claude-opus-5-5', 'claude/group/claude-opus-5-5'],
  '搜索结果保持模型原始顺序'
);

// (a) 可见性：不可见的 provider 不进 agents，也跳过它的 favorites。
const filtered = buildPickerSections({
  providers: favoriteList,
  query: '',
  isProviderVisible: (candidate) => candidate.id !== 'codex'
});
assert(
  filtered.agents.every((section) => section.provider.id !== 'codex'),
  '不可见的 provider 不应出现在 agents 里'
);
assertKeys(
  filtered.favorites,
  ['claude/group/claude-opus-5-5', 'claude/auto/claude-opus-5-5'],
  'favorites 跳过不可见 provider'
);

// 不可用的 provider：行标记 disabled 并带上原因。
const unavailableClaude = provider({
  id: 'claude',
  name: 'Claude',
  available: false,
  unavailableReason: '未登录',
  models: [opusGroup]
});
const unavailable = buildPickerSections({
  providers: providers([unavailableClaude]),
  query: '',
  isProviderVisible: visibleAll
});
const unavailableRow = findRow(unavailable.agents[0].primary, 'claude/group/claude-opus-5-5');
assert(unavailableRow.disabled, 'provider 不可用时行应为 disabled');
assertEqual(unavailableRow.reason, '未登录', '不可用行的原因');

// normalizeModelID 与 modelMatchesQuery 的独立约定。
assertEqual(normalizeModelID('Auto/Claude-Opus-5-5[1m]'), 'auto/claude-opus-5-5', 'normalizeModelID 去 [1m] 并小写');
assertEqual(normalizeModelID('gpt-5'), 'gpt-5', 'normalizeModelID 保留普通 ID');
assert(modelMatchesQuery(claude, opusGroup, '   '), '空搜索词应命中全部模型');
assert(modelMatchesQuery(claude, opusGroup, 'opus'), '按模型名命中');
assert(modelMatchesQuery(claude, opusTarget, 'auto/claude'), '按模型 ID 命中');
assert(modelMatchesQuery(codex, codex.models[0], 'CODEX'), '按 provider 名命中且忽略大小写');
const detailed = model({ id: 'x', name: 'X', detail: 'Fast & cheap' });
assert(modelMatchesQuery(claude, detailed, 'cheap'), '按 detail 命中');
assert(!modelMatchesQuery(claude, opusGroup, 'gpt'), '没有命中时返回 false');

// pairUnavailableReason：执行 provider 不可用时组合判为不可用。
const pair: RemoteSwarmPair = {
  id: 'pair',
  name: null,
  provider: 'claude',
  leadModel: 'group/claude-opus-5-5',
  leadEffort: null,
  workerProvider: 'codex',
  workerModel: 'gpt-5',
  maxBees: 3
};
const codexOffline = provider({
  id: 'codex',
  name: 'Codex',
  available: false,
  unavailableReason: '未登录',
  models: [model({ id: 'gpt-5', name: 'GPT-5' })]
});
const pairProviders = providers([claude, codexOffline]);
assertEqual(pairUnavailableReason(pair, pairProviders), '未登录', '执行 provider 不可用时组合不可用');
assertEqual(
  pairUnavailableReason({ ...pair, workerModel: null }, pairProviders),
  '未登录',
  '只配执行 provider 时同样检查可用性'
);
assertEqual(
  pairUnavailableReason({ ...pair, provider: 'ghost', workerProvider: null, workerModel: null }, pairProviders),
  null,
  'provider 不在目录里时不判为不可用'
);

// pairUnavailableReason：主导与执行 provider 都可用时返回 null。
const pairProvidersOnline = providers([
  claude,
  provider({ id: 'codex', name: 'Codex', models: [model({ id: 'gpt-5', name: 'GPT-5' })] })
]);
assertEqual(pairUnavailableReason(pair, pairProvidersOnline), null, '两个 provider 都可用时组合可用');

// isMissingSelection：目录里找不到 provider 或模型时判为缺失，ID 为空不判断。
assert(!isMissingSelection(pairProvidersOnline, 'claude', 'group/claude-opus-5-5'), '目录里有的模型不算缺失');
assert(isMissingSelection(pairProvidersOnline, 'claude', 'missing-model'), '模型从目录里消失时判为缺失');
assert(isMissingSelection(pairProvidersOnline, 'ghost', 'group/claude-opus-5-5'), 'provider 不在目录里时判为缺失');
assert(!isMissingSelection(pairProvidersOnline, 'claude', null), 'model ID 为空时不判缺失');
assert(!isMissingSelection(pairProvidersOnline, null, 'group/claude-opus-5-5'), 'provider ID 为空时不判缺失');
