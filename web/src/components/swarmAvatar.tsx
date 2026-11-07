import React from 'react';

/** 头像底色候选，按名字哈希取一个，让同一个子代理在时间线里颜色稳定 */
const NAME_COLORS = ['#007AFF', '#34C759', '#AF52DE', '#FF9500', '#5856D6', '#FF2D55', '#00C7BE', '#A2845E'];

function hashName(value: string): number {
  let hash = 0;
  for (let index = 0; index < value.length; index++) {
    hash = (hash * 31 + value.charCodeAt(index)) % 1_000_000_007;
  }
  return Math.abs(hash);
}

/** 按名字哈希取一个稳定底色：派单胶囊与后台任务行用同一个种子，同一个 bee 颜色一致 */
export function pickByHash(value: string): string {
  return NAME_COLORS[hashName(value) % NAME_COLORS.length];
}

/** 名字首字做头像文字，空格分隔的名字取第一段 */
export function initialOf(name: string): string {
  const trimmed = name.trim();
  if (!trimmed) return '子';
  return Array.from(trimmed)[0].toUpperCase();
}

const avatarClass =
  'flex h-4 w-4 shrink-0 items-center justify-center rounded-full text-mc-caption font-semibold text-white';

interface SwarmAvatarProps {
  name: string;
  tint: string;
}

export const SwarmAvatar: React.FC<SwarmAvatarProps> = ({ name, tint }) => (
  <span className={avatarClass} style={{ backgroundColor: tint }} aria-hidden="true">
    {initialOf(name)}
  </span>
);
