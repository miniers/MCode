// @ts-expect-error web/ 未安装 @types/node，测试运行时由 node 提供 node:fs
import { readFileSync } from 'node:fs';

const css = readFileSync(new URL('./index.css', import.meta.url), 'utf8');
const tokens = readFileSync(new URL('./tokens.css', import.meta.url), 'utf8');

function extractBlock(selector: string): string {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const match = new RegExp(`(?:^|\\n)[ \\t]*${escaped}[ \\t]*\\{([\\s\\S]*?)\\}`).exec(css);
  if (!match) {
    throw new Error(`index.css 中缺少规则块：${selector}`);
  }
  return match[1];
}

const rootBlock = extractBlock(':root');
const darkBlock = extractBlock(':root.dark');

const aliases: Array<[string, string]> = [
  ['screen', 'surface-main'],
  ['card', 'surface-card'],
  ['card-alt', 'surface-main'],
  ['foreground', 'text-primary'],
  ['foreground-muted', 'text-secondary'],
  ['border', 'border'],
  ['border-subtle', 'hairline'],
  ['row-hover', 'surface-canvas'],
  ['subtle-strong', 'surface-main'],
  ['primary', 'accent'],
  ['quota-low', 'success'],
  ['quota-mid', 'caution'],
  ['quota-high', 'warning'],
  ['warning-foreground', 'warning'],
  ['danger-foreground', 'danger'],
  ['input-border', 'border-strong'],
];

for (const [name, token] of aliases) {
  const expectedDecl = `--${name}: var(--mc-${token});`;
  if (!rootBlock.includes(expectedDecl)) {
    throw new Error(`:root 缺少 token 别名 ${expectedDecl}`);
  }
  if (!new RegExp(`--mc-${token}\\s*:`).test(tokens)) {
    throw new Error(`tokens.css 缺少 token --mc-${token}`);
  }
  if (new RegExp(`--${name}\\s*:`).test(darkBlock)) {
    throw new Error(`:root.dark 不应覆写 --${name}（应随 --mc-${token} 自动切换）`);
  }
}

const forbiddenHardcodedValues = [
  '#0a0a0a',
  '#111111',
  '#191919',
  '#1b4ed8',
  '#346bf1',
  '#e4e4e7',
  '#f4f4f5',
  '#27272a',
  '#6f6f79',
  '#f5f5f5',
  '#838383',
  '#1e1e1e',
];

for (const hex of forbiddenHardcodedValues) {
  if (rootBlock.toLowerCase().includes(hex)) {
    throw new Error(`:root 不应保留硬编码漂移色值 ${hex}`);
  }
  if (darkBlock.toLowerCase().includes(hex)) {
    throw new Error(`:root.dark 不应保留硬编码漂移色值 ${hex}`);
  }
}
