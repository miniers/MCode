// 总管（steward）的展示名与头像：昵称留空时显示「总管」，头像留空时用昵称首字，
// 连昵称也没有时用「总」。设置里的 steward.name / steward.avatar 都是原文，可为空串。

/** 昵称留空时的默认称呼。 */
export const STEWARD_FALLBACK_NAME = '总管';

/** 侧栏标签：昵称「阿默」→「阿默 · 总管」，昵称留空 →「总管」。 */
export function stewardLabel(nickname: string | null | undefined): string {
  const name = (nickname ?? '').trim();
  return name ? `${name} · ${STEWARD_FALLBACK_NAME}` : STEWARD_FALLBACK_NAME;
}

/** 侧栏头像：头像优先；否则取昵称首字（按码点取，兼容 emoji）；都没有时用「总」。 */
export function stewardAvatarText(
  nickname: string | null | undefined,
  avatar: string | null | undefined
): string {
  const custom = (avatar ?? '').trim();
  if (custom) return custom;
  const first = Array.from((nickname ?? '').trim())[0];
  return first ?? Array.from(STEWARD_FALLBACK_NAME)[0];
}
