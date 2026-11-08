// web/ 没有测试运行器，顶层直接断言，失败时 throw。
import { clearHomeQuery, HOME_QUERY_KEY, readHomeQuery, shouldCollapseOnBlur, writeHomeQuery } from './homeSearch';

const values = new Map<string, string>();
const storage = {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => { values.set(key, value); },
  removeItem: (key: string) => { values.delete(key); }
};

function assert(condition: boolean, label: string): void {
  if (!condition) throw new Error(label);
}

writeHomeQuery(storage, '会话');
assert(readHomeQuery(storage) === '会话', '写入后读取搜索词');
writeHomeQuery(storage, '  ');
assert(!values.has(HOME_QUERY_KEY), '空白搜索词删除存储');
writeHomeQuery(storage, '会话');
clearHomeQuery(storage);
assert(readHomeQuery(storage) === '', '清除后读取空串');
assert(readHomeQuery({ ...storage, getItem: () => { throw new Error('不可用'); } }) === '', '读取失败返回空串');
assert(shouldCollapseOnBlur(''), '空串应收起');
assert(shouldCollapseOnBlur('  '), '空白串应收起');
assert(!shouldCollapseOnBlur('会话'), '有内容时保持展开');
