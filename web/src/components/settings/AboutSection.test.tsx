// 同 ComposerQuotaBar.test.tsx：web/ 没有测试运行器，不用 DOM。
// 展示用 renderToStaticMarkup 渲染纯展示层，交互直接调用抽出的纯函数，断言 api 计数与状态变化。
import { renderToStaticMarkup } from 'react-dom/server';
import type { RemoteAppUpdateApi } from '../../context/remoteAppUpdateApi';
import { RpcError } from '../../services/rpc';
import type { AppUpdateStatus } from '../../types/generated';
import {
  AboutSectionContent,
  initialAboutUpdateState,
  loadAboutUpdate,
  runAboutUpdateAction,
  type AboutUpdateState
} from './AboutSection';

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}

function assertIncludes(markup: string, needle: string, message: string): void {
  assert(markup.includes(needle), `${message}（实际为：${markup}）`);
}

function assertExcludes(markup: string, needle: string, message: string): void {
  assert(!markup.includes(needle), message);
}

function render(state: AboutUpdateState): string {
  return renderToStaticMarkup(
    <AboutSectionContent
      state={state}
      appVersionText="0.2.12"
      webBuildID="dev"
      schemaVersion="1"
      onAction={() => {}}
    />
  );
}

/** 与 useState 的函数式更新一致：在旧状态上打补丁。 */
function merged(state: AboutUpdateState, patch: Partial<AboutUpdateState>): AboutUpdateState {
  return { ...state, ...patch };
}

async function run(): Promise<void> {
  const current: AppUpdateStatus = { current: '0.2.12', available: false, installable: false };
  const update: AppUpdateStatus = {
    current: '0.2.12', latest: '0.2.13', available: true, installable: true, notes: '发布说明'
  };
  let installs = 0;
  let checks = 0;
  const api: RemoteAppUpdateApi = {
    get: async () => current,
    check: async () => { checks += 1; return current; },
    install: async () => { installs += 1; return { started: true }; }
  };

  // ---- 旧宿主：get 报方法不存在时整行隐藏 ----
  let oldHost = initialAboutUpdateState;
  await loadAboutUpdate(
    { ...api, get: async () => { throw new RpcError(-32601, 'Method not found'); } },
    (patch) => { oldHost = merged(oldHost, patch); }
  );
  assert(oldHost.unsupported, '旧宿主应标记为不支持');
  assertExcludes(render(oldHost), '检查更新', '旧宿主不应显示更新行');

  // ---- 有新版本：显示版本号与发布说明 ----
  let fresh = initialAboutUpdateState;
  await loadAboutUpdate({ ...api, get: async () => update }, (patch) => { fresh = merged(fresh, patch); });
  const freshMarkup = render(fresh);
  assertIncludes(freshMarkup, '0.2.13', '应显示新版本号');
  assertIncludes(freshMarkup, '发布说明', '应显示发布说明');

  // ---- 点「更新」：调用 install，进入更新中 ----
  let installing = merged(initialAboutUpdateState, { status: update });
  await runAboutUpdateAction(api, installing, (patch) => { installing = merged(installing, patch); });
  assert(installs === 1, '更新按钮应调用 install');
  assert(installing.installing, '更新启动后应进入更新中');
  assertIncludes(render(installing), '正在更新…', '应显示更新中');

  // ---- 点「检查」：调用 check，显示已是最新 ----
  let checked = merged(initialAboutUpdateState, { status: current });
  await runAboutUpdateAction(api, checked, (patch) => { checked = merged(checked, patch); });
  assert(checks === 1, '检查按钮应调用 check');
  assertIncludes(render(checked), '已是最新', '检查后应显示已是最新');

  // ---- 不可安装但给了下载地址：按钮变成「下载」----
  const downloadable = merged(initialAboutUpdateState, {
    status: { ...update, installable: false, url: 'https://example.com' }
  });
  assertIncludes(render(downloadable), '下载', '不可安装时应显示下载');
}

void run();
