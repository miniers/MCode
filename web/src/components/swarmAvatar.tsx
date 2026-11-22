import React from 'react';

/** 桌面端 25 位默认名册对应色板（小写键，与 internal/ui/theme.go 保持一致） */
export const ROSTER_COLORS: Record<string, string> = {
  aria: '#3B82F6',
  bram: '#E05A47',
  cora: '#10B981',
  dean: '#8B5CF6',
  eva: '#F59E0B',
  finn: '#06B6D4',
  gwen: '#EC4899',
  hugo: '#6366F1',
  iris: '#14B8A6',
  jude: '#F97316',
  kai: '#0EA5E9',
  leo: '#22C55E',
  milo: '#A855F7',
  nora: '#E11D48',
  owen: '#D97706',
  pia: '#2563EB',
  quinn: '#059669',
  remy: '#7C3AED',
  sora: '#0891B2',
  theo: '#DB2777',
  uma: '#CA8A04',
  vera: '#4F46E5',
  wynn: '#0D9488',
  yuki: '#EA580C',
  zoe: '#9333EA'
};

/** 头像底色兜底候选（前 8 个高区分度颜色），用于未在名册中的历史会话或自定义名 */
export const NAME_COLORS = [
  '#3B82F6',
  '#10B981',
  '#8B5CF6',
  '#F59E0B',
  '#E05A47',
  '#06B6D4',
  '#EC4899',
  '#6366F1'
];

function hashName(value: string): number {
  let hash = 0;
  for (let index = 0; index < value.length; index++) {
    hash = (hash * 31 + value.charCodeAt(index)) % 1_000_000_007;
  }
  return Math.abs(hash);
}

/** 按名字取稳定配色：先归一化匹配 25 位名册色表，未命中则按稳定哈希从 NAME_COLORS 取色 */
export function pickByHash(value: string): string {
  const normalized = value.trim().replace(/\s+\d+$/, '').toLowerCase();
  if (Object.prototype.hasOwnProperty.call(ROSTER_COLORS, normalized)) {
    return ROSTER_COLORS[normalized];
  }
  return NAME_COLORS[hashName(value) % NAME_COLORS.length];
}

/** 名字首字做头像文字，空格分隔的名字取第一段 */
export function initialOf(name: string): string {
  const trimmed = name.trim();
  if (!trimmed) return '子';
  return Array.from(trimmed)[0].toUpperCase();
}

const avatarClass =
  'flex h-4 w-4 shrink-0 items-center justify-center rounded-full text-mc-caption font-semibold';

export interface SwarmAvatarProps {
  name: string;
  tint: string;
}

export const SwarmAvatar: React.FC<SwarmAvatarProps> = ({ name, tint }) => (
  <span
    className={avatarClass}
    style={{
      color: tint,
      backgroundImage: `linear-gradient(135deg, color-mix(in srgb, ${tint} 20%, transparent), color-mix(in srgb, ${tint} 7%, transparent))`,
      boxShadow: `inset 0 0 0 1px color-mix(in srgb, ${tint} 34%, transparent)`
    }}
    aria-hidden="true"
  >
    {initialOf(name)}
  </span>
);
