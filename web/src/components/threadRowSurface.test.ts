import { threadRowSurfaceClass } from './ThreadList';

if (threadRowSurfaceClass() !== 'bg-screen') {
  throw new Error('未选中行应使用 bg-screen');
}

const selected = threadRowSurfaceClass(true);
if (!selected.includes('bg-screen') || !selected.includes('linear-gradient(var(--mc-overlay-selected)')) {
  throw new Error('选中行应在 bg-screen 上叠加选中渐变');
}
if (selected.includes('bg-[var(--mc-overlay-selected)]')) {
  throw new Error('选中行不应直接使用半透明背景色');
}
