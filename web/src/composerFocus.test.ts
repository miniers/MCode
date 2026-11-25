// @ts-expect-error web/ 未安装 @types/node，测试运行时由 node 提供 node:fs
import { readFileSync } from 'node:fs';

function readText(relativePath: string): string {
  return readFileSync(new URL(relativePath, import.meta.url), 'utf8');
}

function assert(condition: boolean, message: string): void {
  if (!condition) {
    throw new Error(message);
  }
}

const composerTsx = readText('./components/ThreadComposer.tsx');
assert(composerTsx.includes('composer-card'), 'web/src/components/ThreadComposer.tsx 应存在 composer-card 容器');
assert(
  /<textarea[\s\S]*?composer-input[\s\S]*?\/>/.test(composerTsx),
  'web/src/components/ThreadComposer.tsx 的草稿 textarea 应包含 composer-input'
);

const indexCss = readText('./index.css');
assert(
  indexCss.includes('input:focus-visible, select:focus-visible, textarea:focus-visible { outline: 2px solid var(--primary); outline-offset: 2px; }'),
  'web/src/index.css 应保留全局 focus-visible 焦点环规则'
);
assert(
  /\.composer-input:focus,\s*\.composer-input:focus-visible\s*\{\s*outline:\s*none;\s*\}/.test(indexCss),
  'web/src/index.css 应包含 .composer-input 去掉 outline 的规则'
);
assert(
  /\.composer-card:focus-within\s*\{[^}]*border-color:[^}]*\}/.test(indexCss),
  'web/src/index.css 应包含 .composer-card:focus-within 焦点边框规则'
);
assert(
  /@media\s*\(prefers-reduced-motion:\s*reduce\)\s*\{[\s\S]*?\.composer-card\s*\{[^}]*transition:\s*none/.test(indexCss),
  'web/src/index.css 应在 prefers-reduced-motion: reduce 下关闭 .composer-card 过渡'
);
