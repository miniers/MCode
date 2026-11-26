// 校验 PRMergeBar 导出的纯函数：mergeButtonState、stepStatuses、isMergedThreadTitle、resolveMergedPRNumber 与 retryFromStep。
import {
  isMergedThreadTitle,
  mergeButtonState,
  resolveMergedPRNumber,
  retryFromStep,
  stepStatuses
} from './PRMergeBar';

function assertEqual<T>(actual: T, expected: T, message: string): void {
  const actualJson = JSON.stringify(actual);
  const expectedJson = JSON.stringify(expected);
  if (actualJson !== expectedJson) {
    throw new Error(`${message}：期望 ${expectedJson}，实际 ${actualJson}`);
  }
}

// 1. dirty 时禁用，并显示「有未提交的改动」
assertEqual(
  mergeButtonState({ dirty: true, localCommits: 0, crossRepo: false }),
  { label: '有未提交的改动', disabled: true },
  'dirty=true 时主按钮应禁用'
);
assertEqual(
  mergeButtonState({ dirty: true, localCommits: 3, crossRepo: false }),
  { label: '有未提交的改动', disabled: true },
  'dirty=true 且有本地提交时仍应禁用'
);
assertEqual(
  mergeButtonState({ dirty: true, localCommits: 2, crossRepo: true }),
  { label: '有未提交的改动', disabled: true },
  'dirty=true 且为 fork 时仍应禁用'
);

// 2. 有本地提交且同仓时文案为「推送并合并」
assertEqual(
  mergeButtonState({ dirty: false, localCommits: 2, crossRepo: false }),
  { label: '推送并合并', disabled: false },
  '同仓有本地提交时文案应为「推送并合并」'
);

// 3. fork 有本地提交时文案为「合并」；无本地提交时文案也为「合并」
assertEqual(
  mergeButtonState({ dirty: false, localCommits: 2, crossRepo: true }),
  { label: '合并', disabled: false },
  'fork 有本地提交时文案应为「合并」'
);
assertEqual(
  mergeButtonState({ dirty: false, localCommits: 0, crossRepo: false }),
  { label: '合并', disabled: false },
  '无本地提交时文案应为「合并」'
);

// 4. stepStatuses：failedStep=sync 时前两步完成、第三步失败、第四步待处理
assertEqual(
  stepStatuses({ ok: false, failedStep: 'sync', message: '拉取 main 冲突' }),
  [
    { key: 'push', label: '推送', status: 'done' },
    { key: 'merge', label: '合并 PR', status: 'done' },
    { key: 'sync', label: '更新本地 main', status: 'failed' },
    { key: 'cleanup', label: '清理', status: 'pending' }
  ],
  'failedStep=sync 时应将前两步标为 done、第三步标为 failed'
);

// 4b. stepStatuses：ok=true 时四步都标为完成；运行中四步整体为 running
assertEqual(
  stepStatuses({ ok: true }),
  [
    { key: 'push', label: '推送', status: 'done' },
    { key: 'merge', label: '合并 PR', status: 'done' },
    { key: 'sync', label: '更新本地 main', status: 'done' },
    { key: 'cleanup', label: '清理', status: 'done' }
  ],
  'ok=true 时四步应全部为 done'
);
assertEqual(
  stepStatuses(null, true).map((s) => s.status),
  ['running', 'running', 'running', 'running'],
  '进行中四步应整体显示为 running'
);
assertEqual(retryFromStep('sync'), 'sync', '有效步骤名应原样返回');
assertEqual(retryFromStep('unknown'), undefined, '未知步骤名应返回 undefined');

// 5. 标题带「已合并 · 」前缀时识别为已合并态，并提取 PR 编号
assertEqual(isMergedThreadTitle('已合并 · 审查 PR #31'), true, '带前缀标题应识别为已合并');
assertEqual(isMergedThreadTitle('已合并 · 修复登录问题'), true, '不带编号但带前缀的标题也应识别为已合并');
assertEqual(isMergedThreadTitle('审查 PR #31'), false, '普通标题不应识别为已合并');
assertEqual(isMergedThreadTitle(null), false, '空标题不应识别为已合并');
assertEqual(resolveMergedPRNumber('已合并 · 审查 PR #31', 0), 31, '应从标题提取 PR 编号');
assertEqual(resolveMergedPRNumber('已合并 · 审查 PR #31', 42), 42, 'state.number 优先于标题中的编号');
