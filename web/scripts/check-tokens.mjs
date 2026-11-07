// 用途：守卫 web/src/components 下的字号与圆角，禁止再写死像素值。
// 写死的 text-[NNpx] / rounded-[NNpx] 已统一归口到 tokens.css 的 mc- 类（text-mc-caption 等、rounded-mc-md 等）；
// 唯一豁免是 text-[16px]：移动端输入框字号低于 16px 时 iOS 会自动放大页面。
// 运行：node web/scripts/check-tokens.mjs（发现问题时以退出码 1 结束）
import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const webRoot = join(dirname(fileURLToPath(import.meta.url)), '..');
const componentsDir = join(webRoot, 'src/components');

const FONT_SIZE = /\btext-\[\d+(?:\.\d+)?px\]/g;
const RADIUS = /\brounded(?:-[a-z]{1,2})?-\[\d+(?:\.\d+)?px\]/g;
const ALLOWED = new Set(['text-[16px]']);

function listTsx(dir) {
  return readdirSync(dir, { withFileTypes: true })
    .flatMap((entry) => {
      const full = join(dir, entry.name);
      if (entry.isDirectory()) return listTsx(full);
      return entry.isFile() && entry.name.endsWith('.tsx') ? [full] : [];
    })
    .sort();
}

const hits = [];
for (const file of listTsx(componentsDir)) {
  const source = readFileSync(file, 'utf8');
  source.split('\n').forEach((line, index) => {
    for (const match of [...line.matchAll(FONT_SIZE), ...line.matchAll(RADIUS)]) {
      if (ALLOWED.has(match[0])) continue;
      hits.push(`${relative(webRoot, file)}:${index + 1} ${match[0]}`);
    }
  });
}

if (hits.length) {
  console.error('发现写死的字号/圆角类，请改用 tokens.css 的 mc- 类：');
  for (const hit of hits) console.error(`  ${hit}`);
  process.exit(1);
}
console.log('字号与圆角检查通过');
