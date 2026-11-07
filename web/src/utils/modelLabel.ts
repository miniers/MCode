import type { RemoteProvider } from '../types/generated';

// 模型 ID → 显示名：优先按 provider + 模型 ID 精确匹配（对应桌面 swarmPairModelLabel），
// 再放宽到同一 provider 内不区分大小写、所有 provider 内精确匹配；
// 都找不到时原样返回 ID。已关闭（enabled === false）的 provider 也参与名称解析。
export function modelDisplayName(
  providers: RemoteProvider[] | undefined | null,
  providerID: string | null | undefined,
  modelID: string | null | undefined
): string {
  if (!modelID) return '';
  const list = providers ?? [];
  const nameOf = (provider: RemoteProvider) => {
    const found = provider.models.find((model) => model.id === modelID);
    return found?.name || null;
  };
  if (providerID) {
    const own = list.find((provider) => provider.id === providerID);
    if (own) {
      const exact = nameOf(own);
      if (exact) return exact;
      const needle = modelID.toLowerCase();
      const insensitive = own.models.find((model) => model.id.toLowerCase() === needle);
      if (insensitive?.name) return insensitive.name;
    }
  }
  for (const provider of list) {
    const name = nameOf(provider);
    if (name) return name;
  }
  return modelID;
}
