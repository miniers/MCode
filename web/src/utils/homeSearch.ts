export const HOME_QUERY_KEY = 'mcode.home.query';

type HomeSearchStorage = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>;

export function readHomeQuery(storage: HomeSearchStorage): string {
  try {
    return storage.getItem(HOME_QUERY_KEY) ?? '';
  } catch {
    return '';
  }
}

export function writeHomeQuery(storage: HomeSearchStorage, query: string): void {
  try {
    if (query.trim()) storage.setItem(HOME_QUERY_KEY, query);
    else storage.removeItem(HOME_QUERY_KEY);
  } catch {
    // 存储不可用时仍可在当前页面搜索。
  }
}

export function clearHomeQuery(storage: HomeSearchStorage): void {
  try {
    storage.removeItem(HOME_QUERY_KEY);
  } catch {
    // 存储不可用时忽略。
  }
}

export function shouldCollapseOnBlur(query: string): boolean {
  return query.trim() === '';
}
