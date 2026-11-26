// @ts-expect-error web/ 未安装 @types/node，测试运行时由 node 提供 node:assert
import assert from 'node:assert/strict';
import type { RemoteCommand } from '../types/contract';
import { artifactDraft, commandToSuggestion, deleteCompletionToken, fileSearchScope, filterSlashCommands, rebaseCompletionTokens } from './slashCommands';

const commands: RemoteCommand[] = [
  { name: 'compact', kind: 'builtin', isBuiltIn: true, detail: '' },
  { name: 'model', kind: 'builtin', isBuiltIn: true, detail: '' },
  { name: 'open-artifact', kind: 'command', isBuiltIn: false, detail: '创建交付物' }
];
const ordered = filterSlashCommands(commands, '', ['compact']);
assert.deepEqual(ordered.map((command) => command.name), ['open-artifact', 'model', 'compact']);
assert.equal(commandToSuggestion(ordered[1], true).group, '本地操作');
assert.equal(commandToSuggestion(ordered[2], true).hint, '[保留重点]');
assert.equal(filterSlashCommands(commands, '交付物', [])[0]?.name, 'open-artifact');
assert.deepEqual(artifactDraft('/open-artifact slides 产品汇报', [{ id: 'slides', name: 'Slides' }]), {
  prefix: '/open-artifact slides ',
  label: '幻灯片'
});
assert.equal(artifactDraft('/open-artifact unknown 描述', [{ id: 'slides', name: 'Slides' }]), null);
assert.deepEqual(fileSearchScope('src/components/Picker'), { path: 'src/components', query: 'Picker' });
assert.deepEqual(fileSearchScope('src/'), { path: 'src', query: '' });

const before = '@src/file.go @src/file.go 尾部';
const after = '前 ' + before;
const selected = [{ start: 0, end: '@src/file.go'.length }];
const shifted = rebaseCompletionTokens(before, after, selected);
assert.deepEqual(deleteCompletionToken(after, shifted[0].end + 1, 'Backspace', shifted), {
  text: '前 @src/file.go 尾部', caret: 2
});
assert.equal(deleteCompletionToken(after, after.lastIndexOf('@'), 'Delete', shifted), null);
assert.deepEqual(rebaseCompletionTokens(before, before.replace('file.go', 'fil.go'), selected), []);
