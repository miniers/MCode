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

assert(
  /import\s*\{[^}]*\buseOverflowFade\b[^}]*\}\s*from\s*'\.\.\/hooks\/useOverflowFade';/.test(composerTsx),
  'web/src/components/ThreadComposer.tsx 应引入 useOverflowFade'
);
assert(
  composerTsx.includes("import '../styles/overflow-fade.css';"),
  'web/src/components/ThreadComposer.tsx 应引入 overflow-fade.css'
);
assert(
  /\buseOverflowFade\(\s*toolbarRef\s*\)/.test(composerTsx),
  'web/src/components/ThreadComposer.tsx 应调用 useOverflowFade(toolbarRef)'
);
assert(
  /\{\/\* Horizontally scrollable row of compact pills \*\/\}\s*<div\s+ref=\{toolbarRef\}\s+className="[^"]*\boverflow-fade\b[^"]*\boverflow-x-auto\b[^"]*"\s*>/.test(
    composerTsx
  ),
  'web/src/components/ThreadComposer.tsx 的工具栏滚动容器应绑定 toolbarRef 并包含 overflow-fade'
);
assert(
  /\{\/\* Controls row \*\/\}\s*<div className="[^"]*\bgap-1\.5\b[^"]*">/.test(composerTsx),
  'web/src/components/ThreadComposer.tsx 的底部工具栏父容器应保留 gap-1.5（6px）间距'
);

const fadeCss = readText('./styles/overflow-fade.css');
assert(
  /@media\s*\(max-width:\s*767px\)\s*\{[\s\S]*?\.overflow-fade\s*\{/.test(fadeCss),
  'web/src/styles/overflow-fade.css 应仅在 max-width: 767px 下启用 .overflow-fade'
);
