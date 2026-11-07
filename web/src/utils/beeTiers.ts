// 会话级子代理档位覆盖的序列化工具：会话菜单（ThreadMenuSheet 及其子面板）与输入框
// （ThreadComposer）共用同一套解析/序列化逻辑，保证两边下发的 BeeTiers 字符串完全一致

/**
 * 会话级档位覆盖草稿：键为档位 ID，值为原样保留的宿主字段（provider/model/effort/fast_mode 等）。
 * 桌面端 Thread.BeeTiers 用的是 config.SwarmTierOverride，字段名带下划线，不能重新拼装。
 */
export type TierOverrideDraft = Record<string, Record<string, unknown>>;

// BeeTiers 是宿主下发的 JSON 对象字符串，空串表示没有任何会话级覆盖
export const parseBeeTiers = (raw?: string): TierOverrideDraft => {
  if (!raw || !raw.trim()) return {};
  try {
    const parsed = JSON.parse(raw) as unknown;
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {};
    const result: TierOverrideDraft = {};
    for (const [tierID, value] of Object.entries(parsed as Record<string, unknown>)) {
      if (!value || typeof value !== 'object' || Array.isArray(value)) continue;
      const entry: Record<string, unknown> = {};
      for (const [field, fieldValue] of Object.entries(value as Record<string, unknown>)) {
        if (fieldValue === undefined || fieldValue === null) continue;
        entry[field] = fieldValue;
      }
      if (Object.keys(entry).length > 0) result[tierID] = entry;
    }
    return result;
  } catch {
    return {};
  }
};

// 与桌面端 OnSetTierOverride / OnResetTierOverrides 一致：按档位 ID 序列化，无覆盖时下发空串
export const serializeBeeTiers = (draft: TierOverrideDraft): string => {
  const payload: TierOverrideDraft = {};
  for (const [tierID, override] of Object.entries(draft)) {
    const entry: Record<string, unknown> = {};
    for (const [field, value] of Object.entries(override)) {
      // 空串等于没覆盖，交给宿主回落到全局档位配置
      if (typeof value === 'string') {
        const trimmed = value.trim();
        if (!trimmed) continue;
        entry[field] = trimmed;
      } else {
        entry[field] = value;
      }
    }
    if (Object.keys(entry).length > 0) payload[tierID] = entry;
  }
  return Object.keys(payload).length > 0 ? JSON.stringify(payload) : '';
};

export const overrideCount = (draft: TierOverrideDraft): number => Object.keys(draft).length;
