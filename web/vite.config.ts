import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { createHash } from 'node:crypto';
import { readdirSync, readFileSync, renameSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const resourceDirectory = fileURLToPath(new URL('../internal/webassets/dist/', import.meta.url));
const packageMetadata = JSON.parse(readFileSync(new URL('./package.json', import.meta.url), 'utf8')) as { version?: string };
const buildIDOverride = process.env.MCODE_WEB_BUILD_ID;
const placeholderBuildID = buildIDOverride ?? packageMetadata.version ?? 'dev';

// 按文件名排序后依次把目录下所有文件计入哈希，保证同一份产物得到同一个构建标识。
function hashDirectory(hash: ReturnType<typeof createHash>, directory: string): void {
  const names = readdirSync(directory).sort();
  for (const name of names) {
    hash.update(name);
    hash.update(readFileSync(join(directory, name)));
  }
}

// 构建标识默认取产物内容哈希，保证每次内容变化都不同；包版本号不随构建变化，无法用来识别旧页面。
// 拆包后入口只占小部分代码，懒加载 chunk 与字体的内容也必须计入，否则换版本后构建标识不变。
function resolveBuildID(): string {
  if (buildIDOverride) return buildIDOverride;
  const hash = createHash('sha256');
  for (const file of ['remote-app.js', 'remote-app.css']) {
    try {
      hash.update(readFileSync(`${resourceDirectory}${file}`));
    } catch {
      return placeholderBuildID;
    }
  }
  try {
    hashDirectory(hash, join(resourceDirectory, 'assets'));
  } catch {
    // 没有 assets 目录说明产物里不含懒加载 chunk，只按入口哈希即可。
  }
  return `${placeholderBuildID}-${hash.digest('hex').slice(0, 12)}`;
}

// 稳定的第三方依赖按组切 chunk，chunk 名不带上 remote-app 前缀，避免与入口文件混淆。
function manualChunks(id: string): string | undefined {
  const normalized = id.split('\\').join('/');
  if (!normalized.includes('/node_modules/')) return undefined;
  // 公式相关包（remark-math / rehype-katex 及它们的专属依赖，含 katex 本体）不归入任何 vendor 组：
  // 它们只经由 utils/lazyMath.ts 的动态 import 被引用，交给 Rollup 分进懒加载 chunk。
  // 否则下方 vendor-markdown 的正则会匹配 remark-math / rehype-katex，
  // 而 rehype-katex 静态导入 katex，首屏的 vendor-markdown 就会把 KaTeX 一起拉进来。
  if (/\/node_modules\/(katex|rehype-katex|remark-math|micromark-extension-math|mdast-util-math|hast-util-from-html-isomorphic|hast-util-to-text)\//.test(normalized)) return undefined;
  if (/\/node_modules\/(react|react-dom|scheduler)\//.test(normalized)) return 'vendor-react';
  if (/\/node_modules\/@tanstack\//.test(normalized)) return 'vendor-query';
  if (/\/node_modules\/(highlight\.js|lowlight)\//.test(normalized)) return 'vendor-highlight';
  if (/\/node_modules\/(react-markdown|remark[^/]*|rehype[^/]*|unified|micromark[^/]*|mdast[^/]*|hast[^/]*|[a-z0-9]+-util-[^/]*|vfile[^/]*|unist[^/]*|bail|is-plain-obj|trough|devlop|property-information|space-separated-tokens|comma-separated-tokens|character-entities[^/]*|decode-named-character-reference|html-url-attributes|zwitch|longest-streak|ccount|escape-string-regexp|markdown-table|style-to-object|inline-style-parser|trim-lines|unist-util-position)\//.test(normalized)) return 'vendor-markdown';
  return undefined;
}

function updateRemoteManifest(webBuildID: string): void {
  const manifestPath = `${resourceDirectory}remote-manifest.webmanifest`;
  try {
    const manifest = readFileSync(manifestPath, 'utf8');
    const updated = manifest.replace(/("buildID"\s*:\s*")([^"]*)(")/, `$1${webBuildID}$3`);
    if (updated !== manifest) writeFileSync(manifestPath, updated);
  } catch {
    // The host will report a missing manifest during its normal resource check.
  }
}

export default defineConfig({
  plugins: [react(), {
    name: 'bundle-remote-page',
    transformIndexHtml(html) {
      const meta = `<meta name="mcode-build-id" content="${placeholderBuildID}" />`;
      if (html.includes('name="mcode-build-id"')) return html;
      return html.replace('</head>', `  ${meta}\n</head>`);
    },
    closeBundle() {
      const webBuildID = resolveBuildID();
      updateRemoteManifest(webBuildID);
      const indexPath = `${resourceDirectory}index.html`;
      const html = readFileSync(indexPath, 'utf8');
      writeFileSync(indexPath, html.replace(/(<meta name="mcode-build-id" content=")[^"]*(")/, `$1${webBuildID}$2`));
      renameSync(indexPath, `${resourceDirectory}remote-index.html`);
      // emptyOutDir 会清空占位文件；不重建会导致提交删除，干净 worktree 的 go:embed all:dist 编译失败。
      writeFileSync(`${resourceDirectory}.gitkeep`, '');
    }
  }],
  base: './',
  server: {
    host: true,
    port: 5173
  },
  build: {
    outDir: resourceDirectory,
    emptyOutDir: true,
    sourcemap: false,
    cssCodeSplit: false,
    rollupOptions: {
      output: {
        entryFileNames: 'remote-app.js',
        chunkFileNames: 'assets/[name]-[hash].js',
        // 只有一个 CSS 入口，保持固定文件名；字体、图片等静态资源落到 /assets/ 下并带内容哈希。
        assetFileNames: (assetInfo) => {
          // rollup 4.x 起 name 改名为 names，两代字段都读一下，避免锁文件版本差异导致取值落空。
          const info = assetInfo as { names?: string[]; name?: string };
          const name = info.names?.[0] ?? info.name ?? '';
          if (name.endsWith('.css')) return 'remote-app.css';
          return 'assets/[name]-[hash][extname]';
        },
        manualChunks
      }
    }
  }
});
