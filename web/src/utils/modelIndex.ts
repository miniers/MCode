import type { RemoteProviderListWithCatalog } from '../context/remoteTypes';
import type { RemoteProvider, RemoteProviderModel, RemoteSwarmPair } from '../types/generated';

/** 选择器里的一行：某个 provider 下的一个模型。 */
export interface PickerRow {
  provider: RemoteProvider;
  model: RemoteProviderModel;
  /** provider.id + '/' + model.id，行的唯一标识。 */
  key: string;
  /** 同名模型用来区分：默认 null，重名时填模型 ID。 */
  subtitle: string | null;
  /** provider 不可用（available 为 false）时整行禁用。 */
  disabled: boolean;
  /** 行不可用的原因，可用时为 null。 */
  reason: string | null;
}

/** 一个 agent（provider）分组：primary 常驻，secondary 需要展开。 */
export interface AgentSection {
  provider: RemoteProvider;
  primary: PickerRow[];
  secondary: PickerRow[];
  /** 该 provider 一个模型都没有时给出的原因，其余情况为 null。 */
  reason: string | null;
}

/** 选择器需要的全部分区。 */
export interface PickerSections {
  current: PickerRow | null;
  favorites: PickerRow[];
  agents: AgentSection[];
  searching: boolean;
}

/** buildPickerSections 的入参。 */
export interface PickerSectionsInput {
  providers: RemoteProviderListWithCatalog;
  /** 当前选中的 provider 与模型 ID。 */
  current?: { provider: string; model: string } | null;
  query: string;
  isProviderVisible: (provider: RemoteProvider) => boolean;
}

/** 模型 ID 归一：转小写并去掉末尾的 [1m] 长上下文标记。 */
export function normalizeModelID(id: string): string {
  return id.toLowerCase().replace(/\[1m\]$/, '');
}

/** 模型是否命中搜索词：搜索词为空时命中全部，否则匹配名称、ID、说明与 provider 名称、ID。 */
export function modelMatchesQuery(provider: RemoteProvider, model: RemoteProviderModel, query: string): boolean {
  const needle = query.trim().toLowerCase();
  if (!needle) return true;
  const fields: Array<string | null> = [model.name, model.id, model.detail, provider.name, provider.id];
  return fields.some((field) => (field ?? '').toLowerCase().includes(needle));
}

function rowKey(providerID: string, modelID: string): string {
  return `${providerID}/${modelID}`;
}

/** 读取 aliasOf：只有非空字符串才算别名，其余视为普通模型。 */
function aliasTarget(model: RemoteProviderModel): string | null {
  const alias = model.aliasOf;
  return typeof alias === 'string' && alias.trim() !== '' ? alias.trim() : null;
}

function buildRow(provider: RemoteProvider, model: RemoteProviderModel, subtitle: string | null): PickerRow {
  return {
    provider,
    model,
    key: rowKey(provider.id, model.id),
    subtitle,
    disabled: !provider.available,
    reason: provider.available ? null : provider.unavailableReason
  };
}

/** 一组行里出现两次以上的模型名（忽略大小写）。 */
function repeatedNames(rows: PickerRow[]): Set<string> {
  const counts = new Map<string, number>();
  for (const row of rows) {
    const name = row.model.name.toLowerCase();
    counts.set(name, (counts.get(name) ?? 0) + 1);
  }
  const repeated = new Set<string>();
  for (const [name, count] of counts) {
    if (count > 1) repeated.add(name);
  }
  return repeated;
}

export function buildPickerSections(input: PickerSectionsInput): PickerSections {
  const query = input.query.trim();
  const searching = query !== '';
  const visible = input.providers.filter((provider) => input.isProviderVisible(provider));
  const catalog = input.providers.catalog ?? [];
  const catalogKeys = new Set(catalog.map((entry) => rowKey(entry.provider, entry.modelID)));
  const currentRef = input.current ?? null;

  // 当前选中的 provider / 模型：只在可见的 provider 里找，找不到就没有 current。
  const currentProvider = currentRef
    ? visible.find((provider) => provider.id === currentRef.provider) ?? null
    : null;
  const currentModel = currentProvider && currentRef
    ? currentProvider.models.find((model) => model.id === currentRef.model) ?? null
    : null;
  const currentKey = currentProvider && currentModel ? rowKey(currentProvider.id, currentModel.id) : null;

  const agents: AgentSection[] = [];
  for (const provider of visible) {
    // 没有模型的 provider：有原因就留一个只有原因的空分组，没有原因就整组跳过。
    if (provider.models.length === 0) {
      if (!searching && provider.unavailableReason) {
        agents.push({ provider, primary: [], secondary: [], reason: provider.unavailableReason });
      }
      continue;
    }

    // 同 provider 内指向其它模型实体的 CLI 别名行；跨 provider 绝不合并。
    const folded = new Set<string>();
    const ids = new Set(provider.models.map((model) => normalizeModelID(model.id)));
    for (const model of provider.models) {
      const alias = aliasTarget(model);
      if (!alias) continue;
      const target = normalizeModelID(alias);
      if (target === normalizeModelID(model.id) || !ids.has(target)) continue;
      folded.add(model.id);
    }

    const rows: PickerRow[] = [];
    for (const model of provider.models) {
      const key = rowKey(provider.id, model.id);
      // 被折叠的别名仍然隐藏，除非它是当前选中（搜索时不算）或在常用列表里。
      const keepForCurrent = !searching && key === currentKey;
      if (folded.has(model.id) && !keepForCurrent && !catalogKeys.has(key)) continue;
      rows.push(buildRow(provider, model, null));
    }
    if (rows.length === 0) continue;

    const matched = searching ? rows.filter((row) => modelMatchesQuery(provider, row.model, query)) : rows;
    if (matched.length === 0) continue;

    const primary: PickerRow[] = [];
    const secondary: PickerRow[] = [];
    for (const row of matched) {
      // 搜索时 secondary 并入 primary；平时 current 那一行永远留在 primary。
      if (!searching && row.model.secondary === true && row.key !== currentKey) {
        secondary.push(row);
      } else {
        primary.push(row);
      }
    }

    // 同组内（primary 与 secondary 合并算）重名的行补上模型 ID。
    const repeated = repeatedNames([...primary, ...secondary]);
    for (const row of [...primary, ...secondary]) {
      row.subtitle = repeated.has(row.model.name.toLowerCase()) ? row.model.id : null;
    }
    agents.push({ provider, primary, secondary, reason: null });
  }

  const favorites: PickerRow[] = [];
  let current: PickerRow | null = null;
  if (!searching) {
    const seen = new Set<string>();
    for (const entry of catalog) {
      const key = rowKey(entry.provider, entry.modelID);
      if (seen.has(key)) continue;
      const provider = visible.find((candidate) => candidate.id === entry.provider);
      if (!provider) continue;
      const model = provider.models.find((candidate) => candidate.id === entry.modelID);
      if (!model) continue;
      seen.add(key);
      favorites.push(buildRow(provider, model, provider.name));
    }

    const repeatedFavoriteNames = repeatedNames(favorites);
    for (const row of favorites) {
      row.subtitle = repeatedFavoriteNames.has(row.model.name.toLowerCase())
        ? `${row.provider.name} · ${row.model.id}`
        : row.provider.name;
    }

    if (currentProvider && currentModel && currentKey) {
      // 当前行已经在常用列表里就不再重复显示。
      const inFavorites = favorites.some((row) => row.key === currentKey);
      if (!inFavorites) {
        const clashed = favorites.some(
          (row) => row.model.name.toLowerCase() === currentModel.name.toLowerCase()
        );
        current = buildRow(
          currentProvider,
          currentModel,
          clashed ? `${currentProvider.name} · ${currentModel.id}` : currentProvider.name
        );
      }
    }
  }

  return { current, favorites, agents, searching };
}

/**
 * 编排组合不可用的原因：主导 provider 或（指定了执行方时）执行 provider 不可用就给出原因。
 * provider 不在目录里时不做判断，返回 null。
 */
export function pairUnavailableReason(pair: RemoteSwarmPair, providers: RemoteProvider[]): string | null {
  const lead = providers.find((provider) => provider.id === pair.provider);
  if (lead && !lead.available) return lead.unavailableReason ?? 'Provider 不可用';

  if (pair.workerProvider || pair.workerModel) {
    const worker = providers.find((provider) => provider.id === (pair.workerProvider ?? pair.provider));
    if (worker && !worker.available) return worker.unavailableReason ?? 'Provider 不可用';
  }
  return null;
}

/** 已保存的 provider/model 是否在当前目录里找不到：ID 为空时不判断，返回 false。 */
export function isMissingSelection(
  providers: RemoteProvider[],
  providerID: string | null | undefined,
  modelID: string | null | undefined
): boolean {
  if (!providerID || !modelID) return false;
  const provider = providers.find((candidate) => candidate.id === providerID);
  if (!provider) return true;
  return !provider.models.some((model) => model.id === modelID);
}
