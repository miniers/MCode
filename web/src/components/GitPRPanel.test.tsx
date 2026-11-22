// 同 SwarmCapsules.test.tsx：web/ 没有测试运行器，用 react-dom/server 渲染成字符串后顶层断言。
// 校验 prStatusStrip 各分支与优先级，以及 GitPRListRow 的状态点、CI/评审图标和元数据渲染。
import { renderToStaticMarkup } from 'react-dom/server';
import type { RemoteGitPRDetail, RemoteGitPRSummary } from '../context/remoteGitApi';
import { GitPRListRow, prStatusStrip, type PRStatusStripTone } from './GitPRPanel';

function assertStrip(
  actual: { tone: PRStatusStripTone; text: string },
  expectedTone: PRStatusStripTone,
  expectedText: string
): void {
  if (actual.tone !== expectedTone || actual.text !== expectedText) {
    throw new Error(
      `期望状态条为 (${expectedTone}, "${expectedText}")，实际为 (${actual.tone}, "${actual.text}")`
    );
  }
}

function assertIncludes(haystack: string, needle: string): void {
  if (!haystack.includes(needle)) {
    throw new Error(`期望渲染结果包含 “${needle}”，实际为：${haystack}`);
  }
}

function assertExcludes(haystack: string, needle: string): void {
  if (haystack.includes(needle)) {
    throw new Error(`期望渲染结果不包含 “${needle}”，实际为：${haystack}`);
  }
}

function makeDetail(overrides: Partial<RemoteGitPRDetail> = {}): RemoteGitPRDetail {
  return {
    number: 128,
    title: '重构 PR 面板',
    state: 'open',
    isDraft: false,
    author: 'liukai',
    headBranch: 'feat/pr-redesign',
    baseBranch: 'main',
    mergeable: 'MERGEABLE',
    reviewDecision: 'APPROVED',
    checksState: 'success',
    body: '',
    additions: 10,
    deletions: 2,
    changedFiles: 1,
    checks: [{ name: 'web / typecheck', state: 'success' }],
    comments: [],
    ...overrides
  };
}

// 1. merged 优先于冲突、失败检查、草稿和需要修改
assertStrip(
  prStatusStrip(
    makeDetail({
      state: 'merged',
      baseBranch: 'release',
      isDraft: true,
      mergeable: 'CONFLICTING',
      reviewDecision: 'CHANGES_REQUESTED',
      checks: [{ name: 'ci', state: 'failure' }]
    })
  ),
  'plan',
  '已合并到 release'
);

// 2. closed 优先于草稿、冲突和失败检查
assertStrip(
  prStatusStrip(
    makeDetail({
      state: 'closed',
      isDraft: true,
      mergeable: 'CONFLICTING',
      checks: [{ name: 'ci', state: 'failed' }]
    })
  ),
  'neutral',
  '已关闭'
);

// 3. isDraft 优先于冲突和失败检查
assertStrip(
  prStatusStrip(
    makeDetail({
      isDraft: true,
      mergeable: 'CONFLICTING',
      checks: [{ name: 'ci', state: 'failure' }]
    })
  ),
  'neutral',
  '草稿'
);

// 4. 冲突优先于检查失败和需要修改
assertStrip(
  prStatusStrip(
    makeDetail({
      mergeable: 'CONFLICTING',
      reviewDecision: 'CHANGES_REQUESTED',
      checks: [{ name: 'ci', state: 'failure' }]
    })
  ),
  'danger',
  '有冲突'
);

// 5. 检查失败（兼容 failure 与 failed）优先于进行中和评审意见
assertStrip(
  prStatusStrip(
    makeDetail({
      reviewDecision: 'CHANGES_REQUESTED',
      checks: [
        { name: 'go-test', state: 'failure' },
        { name: 'guard', state: 'failed' },
        { name: 'build', state: 'pending' }
      ]
    })
  ),
  'danger',
  '2 项检查失败'
);

// 6. 检查进行中优先于评审意见
assertStrip(
  prStatusStrip(
    makeDetail({
      reviewDecision: 'CHANGES_REQUESTED',
      checks: [
        { name: 'go-test', state: 'success' },
        { name: 'build-1', state: 'pending' },
        { name: 'build-2', state: 'pending' }
      ]
    })
  ),
  'neutral',
  '2 项检查进行中'
);

// 7. 需要修改
assertStrip(
  prStatusStrip(makeDetail({ reviewDecision: 'CHANGES_REQUESTED' })),
  'danger',
  '需要修改'
);

// 8. 等待评审
assertStrip(
  prStatusStrip(makeDetail({ reviewDecision: 'REVIEW_REQUIRED' })),
  'neutral',
  '等待评审'
);

// 9. 可以合并
assertStrip(prStatusStrip(makeDetail()), 'success', '可以合并');

// 列表行渲染校验
function makeSummary(overrides: Partial<RemoteGitPRSummary> = {}): RemoteGitPRSummary {
  return {
    number: 126,
    title: '为 ForgeClient 增加行内评论批量提交接口',
    state: 'open',
    isDraft: false,
    author: 'zhaozhi',
    headBranch: 'feat/pr-inline-review',
    baseBranch: 'main',
    checksState: 'failure',
    reviewDecision: 'CHANGES_REQUESTED',
    ...overrides
  };
}

const noopSelect = () => {};

const failRowHtml = renderToStaticMarkup(
  <GitPRListRow
    item={makeSummary({ updatedAt: new Date().toISOString() })}
    onSelect={noopSelect}
  />
);
assertIncludes(failRowHtml, '为 ForgeClient 增加行内评论批量提交接口');
assertIncludes(failRowHtml, 'data-ci-state="failure"');
assertIncludes(failRowHtml, 'aria-label="CI 失败"');
assertIncludes(failRowHtml, 'data-review-decision="CHANGES_REQUESTED"');
assertIncludes(failRowHtml, '#126 · zhaozhi');
assertIncludes(failRowHtml, '刚刚');
assertIncludes(failRowHtml, 'bg-mc-success');

// pending 静态半圆环不包含循环动画，APPROVED 显示已批准图标
const pendingRowHtml = renderToStaticMarkup(
  <GitPRListRow
    item={makeSummary({
      number: 127,
      state: 'merged',
      checksState: 'pending',
      reviewDecision: 'APPROVED'
    })}
    onSelect={noopSelect}
  />
);
assertIncludes(pendingRowHtml, 'data-ci-state="pending"');
assertIncludes(pendingRowHtml, 'data-review-decision="APPROVED"');
assertIncludes(pendingRowHtml, 'bg-mc-plan');
assertExcludes(pendingRowHtml, 'animate-spin');
assertExcludes(pendingRowHtml, 'animate-pulse');

// checksState=none 与 REVIEW_REQUIRED 不渲染小图标，草稿与 closed 使用次要文字色
const draftRowHtml = renderToStaticMarkup(
  <GitPRListRow
    item={makeSummary({
      number: 129,
      isDraft: true,
      checksState: 'none',
      reviewDecision: 'REVIEW_REQUIRED'
    })}
    onSelect={noopSelect}
  />
);
assertIncludes(draftRowHtml, 'bg-mc-text-secondary');
assertExcludes(draftRowHtml, 'data-ci-state=');
assertExcludes(draftRowHtml, 'data-review-decision=');

const closedRowHtml = renderToStaticMarkup(
  <GitPRListRow
    item={makeSummary({
      number: 130,
      state: 'closed',
      checksState: undefined,
      reviewDecision: undefined
    })}
    onSelect={noopSelect}
  />
);
assertIncludes(closedRowHtml, 'bg-mc-text-secondary');
assertExcludes(closedRowHtml, 'data-ci-state=');
assertExcludes(closedRowHtml, 'data-review-decision=');
