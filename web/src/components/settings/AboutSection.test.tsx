// 在 DOM 环境中渲染真实组件，用替身模拟宿主的更新接口。
import { act } from 'react-dom/test-utils';
import { createRoot } from 'react-dom/client';
import type { RemoteAppUpdateApi } from '../../context/remoteAppUpdateApi';
import { RpcError } from '../../services/rpc';
import type { AppUpdateStatus } from '../../types/generated';
import { AboutSectionView } from './AboutSection';

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}

async function run(): Promise<void> {
  const container = document.createElement('div');
  document.body.appendChild(container);
  const root = createRoot(container);
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

  let mountID = 0;
  async function mount(next: RemoteAppUpdateApi): Promise<void> {
    mountID += 1;
    await act(async () => {
      root.render(<AboutSectionView key={mountID} api={next} appVersionText="0.2.12" webBuildID="dev" schemaVersion="1" />);
    });
  }
  async function click(label: string): Promise<void> {
    const button = Array.from(container.querySelectorAll('button')).find((item) => item.textContent === label);
    assert(button, `未找到按钮：${label}`);
    await act(async () => { button.click(); });
  }

  try {
    await mount({ ...api, get: async () => { throw new RpcError(-32601, 'Method not found'); } });
    assert(!container.textContent?.includes('检查更新'), '旧宿主不应显示更新行');

    await mount({ ...api, get: async () => update });
    assert(container.textContent?.includes('0.2.13'), '应显示新版本号');
    assert(container.textContent?.includes('发布说明'), '应显示发布说明');
    await click('更新');
    assert(installs === 1, '更新按钮应调用 install');
    assert(container.textContent?.includes('正在更新…'), '应显示更新中');

    await mount(api);
    await click('检查');
    assert(checks === 1, '检查按钮应调用 check');
    assert(container.textContent?.includes('已是最新'), '检查后应显示已是最新');

    await mount({ ...api, get: async () => ({ ...update, installable: false, url: 'https://example.com' }) });
    assert(Array.from(container.querySelectorAll('button')).some((item) => item.textContent === '下载'), '不可安装时应显示下载');
  } finally {
    await act(async () => { root.unmount(); });
    container.remove();
  }
}

void run();
