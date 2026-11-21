// @ts-expect-error web/ 未安装 @types/node，测试运行时由 node 提供 node:assert
import assert from 'node:assert/strict';
import type { RemoteProjectSummary, RemoteThreadSummary } from '../types/contract';
import { resolveStewardOpenProjectID, shouldShowStewardRow } from './stewardEntry';

type ProjectItem = Pick<RemoteProjectSummary, 'id' | 'kind'>;
type ThreadItem = Pick<RemoteThreadSummary, 'isSteward' | 'projectID'>;

const mixedProjects: ProjectItem[] = [
  { id: 'steward-proj', kind: 'steward' },
  { id: 'proj-1' },
  { id: 'chats-proj', kind: 'chats' },
  { id: 'proj-2' }
];
const stewardThread: ThreadItem = { isSteward: true, projectID: 'steward-proj' };

// 1. 单项目优先返回 selectedProjectID（即使存在 isSteward 会话或其它项目）
assert.equal(
  resolveStewardOpenProjectID(
    'proj-2',
    mixedProjects,
    [stewardThread]
  ),
  'proj-2'
);

// 2. 全部项目（null）优先复用已有 isSteward 会话的 projectID
assert.equal(
  resolveStewardOpenProjectID(
    null,
    mixedProjects,
    [
      { isSteward: false, projectID: 'proj-1' },
      { isSteward: true, projectID: '' },
      { isSteward: true, projectID: 'steward-proj' }
    ]
  ),
  'steward-proj'
);

// 3. 无可用 isSteward 会话时，按 pickableProjects 顺序取首个可用项目（chats 优先于普通项目）
assert.equal(
  resolveStewardOpenProjectID(
    null,
    mixedProjects,
    [{ isSteward: false, projectID: 'proj-2' }]
  ),
  'chats-proj'
);
assert.equal(
  resolveStewardOpenProjectID(
    null,
    [{ id: 'steward-proj', kind: 'steward' }, { id: 'proj-1' }, { id: 'proj-2' }],
    []
  ),
  'proj-1'
);

// 4. pickableProjects 为空时回退任意有 id 的项目（如仅有内置总管项目）
assert.equal(
  resolveStewardOpenProjectID(
    null,
    [{ id: 'steward-proj', kind: 'steward' }],
    []
  ),
  'steward-proj'
);

// 5. 无任何可用项目时返回 null
assert.equal(resolveStewardOpenProjectID(null, [], []), null);
assert.equal(resolveStewardOpenProjectID(null, [{ id: '' }], []), null);

// 6. shouldShowStewardRow 门控逻辑
const baseOpts = {
  openProjectID: 'chats-proj',
  supportsOpen: true,
  isConnected: true,
  filter: 'all',
  messageNeedle: ''
};
assert.equal(shouldShowStewardRow(baseOpts), true);
assert.equal(shouldShowStewardRow({ ...baseOpts, filter: 'attention' }), true);
assert.equal(shouldShowStewardRow({ ...baseOpts, filter: 'archived' }), false);
assert.equal(shouldShowStewardRow({ ...baseOpts, messageNeedle: 'keyword' }), false);
assert.equal(shouldShowStewardRow({ ...baseOpts, isConnected: false }), false);
assert.equal(shouldShowStewardRow({ ...baseOpts, supportsOpen: false }), false);
assert.equal(shouldShowStewardRow({ ...baseOpts, openProjectID: null }), false);
